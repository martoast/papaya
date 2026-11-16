package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"

	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/tsnode"
	"github.com/martoast/papaya/test/harness"
)

type whoIs struct{ login string }

func (w whoIs) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	// Every request in these tests comes from the "laptop" machine, which is
	// what the one-line join reads to know which machine ran it.
	return &apitype.WhoIsResponse{Node: &tailcfg.Node{Name: "laptop.tail1234.ts.net."}, UserProfile: &tailcfg.UserProfile{LoginName: w.login}}, nil
}

// peers is a fake tailnet whose machines a test can change.
type peers struct {
	mu sync.Mutex
	st *ipnstate.Status
}

func (p *peers) Status(context.Context) (*ipnstate.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st, nil
}

func (p *peers) add(name, os string, online bool, hostKeys ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st == nil {
		p.st = &ipnstate.Status{
			Peer:           map[key.NodePublic]*ipnstate.PeerStatus{},
			CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "tail1234.ts.net"},
		}
	}
	p.st.Peer[key.NewNode().Public()] = &ipnstate.PeerStatus{
		HostName:     name,
		DNSName:      name + ".tail1234.ts.net.",
		OS:           os,
		Online:       online,
		SSH_HostKeys: hostKeys,
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")},
	}
}

type fixture struct {
	srv     *httptest.Server
	store   *setup.Store
	trusted string
	tsHost  *harness.SSHServer // runs "Tailscale SSH": advertises its host key
	keyHost *harness.SSHServer // runs its own sshd: nothing vouches for it
	peers   *peers
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()

	tsHost := harness.NewSSHServer(t, harness.AuthNone)
	keyHost := harness.NewSSHServer(t, harness.AuthNone)
	dialer := harness.NewCountingDialer(map[string]string{"server": tsHost.Addr, "laptop": keyHost.Addr})

	p := &peers{}
	p.add("server", "linux", true, tsHost.AuthorizedHostKey())
	p.add("laptop", "macOS", true)
	p.add("old-box", "windows", false)

	store, err := setup.Open(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	trusted := filepath.Join(dir, "known_hosts")

	sshCfg := sshx.Config{
		UseTailscaleSSH:  true,
		KnownHostsPath:   filepath.Join(dir, "no-operator-known-hosts"),
		TrustedHostsPath: trusted,
		TailnetHostKeys: func(host string) []string {
			st, _ := p.Status(context.Background())
			return tsnode.PeerSSHHostKeysFrom(st, host)
		},
	}
	h := hub.New(sshx.NewPool(dialer, sshCfg), hub.Options{UploadDir: filepath.Join(dir, "up")})
	t.Cleanup(func() { h.CloseAll("test over") })

	allowed := func(u string) bool { return u != "" && u != "root" }
	api := httpapi.New(h,
		tsnode.NewAuth(whoIs{"alice@example.com"}, map[string]bool{"alice@example.com": true}, nil),
		p, nil, httpapi.Options{Hostname: "papaya", SSHUserAllowed: allowed})
	api.EnableSetup(httpapi.SetupDeps{
		Store:     store,
		Peers:     p,
		Dialer:    dialer,
		SSH:       sshCfg,
		PublicKey: "ssh-ed25519 AAAAtest papaya",
		Owner:     func() string { return "alice@example.com" },
		HTTPS: func() httpapi.HTTPSState {
			return httpapi.HTTPSState{Available: false, URL: "http://papaya.tail1234.ts.net"}
		},
		SSHUserAllowed: allowed,
		Self:           func() string { return "papaya" },
	})

	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, store: store, trusted: trusted, tsHost: tsHost, keyHost: keyHost, peers: p}
}

func (f *fixture) call(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	if out != nil && res.StatusCode < 300 && res.StatusCode != http.StatusNoContent {
		require.NoError(t, json.NewDecoder(res.Body).Decode(out))
	}
	return res.StatusCode
}

func machine(v httpapi.SetupView, name string) httpapi.MachineView {
	for _, m := range v.Machines {
		if m.Name == name {
			return m
		}
	}
	return httpapi.MachineView{}
}

// The first screen lists the tailnet's machines with what the person needs to
// choose: online or not, and whether Tailscale SSH is already on.
func TestSetupListsTheTailnetsMachines(t *testing.T) {
	f := newFixture(t)
	var v httpapi.SetupView
	require.Equal(t, http.StatusOK, f.call(t, "GET", "/api/setup", nil, &v))

	require.False(t, v.Complete, "a fresh install starts in setup")
	require.Equal(t, "alice@example.com", v.Owner)
	require.Equal(t, "tail1234.ts.net", v.Tailnet)
	require.Contains(t, v.PolicySnippet, `"accept"`)
	require.Equal(t, "ssh-ed25519 AAAAtest papaya", v.PublicKey)
	require.Len(t, v.Machines, 3)
	require.False(t, v.Machines[2].Online, "offline machines sort last")

	server := machine(v, "server")
	require.True(t, server.TailscaleSSH)
	require.Equal(t, setup.MethodTailscale, server.Method)
	laptop := machine(v, "laptop")
	require.False(t, laptop.TailscaleSSH)
	require.Equal(t, setup.MethodKey, laptop.Method, "a Mac defaults to papaya's key: Tailscale SSH is Linux-only")
}

func TestSetupRefusesRoot(t *testing.T) {
	f := newFixture(t)
	code := f.call(t, "PUT", "/api/setup/machines", map[string]any{
		"machines": []map[string]string{{"name": "server", "user": "root"}},
	}, nil)
	require.Equal(t, http.StatusBadRequest, code)

	var res httpapi.TestResult
	f.call(t, "POST", "/api/setup/test", map[string]string{"host": "server", "user": "root"}, &res)
	require.Equal(t, "error", res.Status)
	require.Equal(t, "root", res.Code)
}

// A Tailscale SSH machine passes on the first try: its host key comes from the
// tailnet, so nothing needs confirming.
func TestATailscaleSSHMachineIsReadyInOneClick(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusOK, f.call(t, "PUT", "/api/setup/machines", map[string]any{
		"machines": []map[string]string{{"name": "server", "user": "alice", "method": "tailscale"}},
	}, nil))

	var res httpapi.TestResult
	f.call(t, "POST", "/api/setup/test", map[string]string{"host": "server", "user": "alice", "method": "tailscale"}, &res)
	require.Equal(t, "ok", res.Status, res.Message)

	m, ok := f.store.Machine("server")
	require.True(t, ok)
	require.True(t, m.Ready, "a passed test is remembered")
	require.Equal(t, "alice", f.store.UserFor("server"))
}

// A machine nothing vouches for shows its fingerprint first; once the person
// trusts it, it connects, and stays trusted.
func TestANewHostIsTrustedAfterConfirmation(t *testing.T) {
	f := newFixture(t)

	var res httpapi.TestResult
	f.call(t, "POST", "/api/setup/test", map[string]string{"host": "laptop", "user": "alice", "method": "key"}, &res)
	require.Equal(t, "trust", res.Status, res.Message)
	require.NotEmpty(t, res.Fingerprint)
	require.True(t, strings.HasPrefix(res.Fingerprint, "SHA256:"))

	f.call(t, "POST", "/api/setup/test", map[string]string{
		"host": "laptop", "user": "alice", "method": "key", "trust": "SHA256:not-the-one",
	}, &res)
	require.Equal(t, "trust", res.Status, "a different fingerprint trusts nothing")

	var fp = res.Fingerprint
	f.call(t, "POST", "/api/setup/test", map[string]string{
		"host": "laptop", "user": "alice", "method": "key", "trust": fp,
	}, &res)
	require.Equal(t, "ok", res.Status, res.Message)

	raw, err := os.ReadFile(f.trusted)
	require.NoError(t, err)
	require.Contains(t, string(raw), "laptop", "the host key is saved in papaya's known_hosts")

	f.call(t, "POST", "/api/setup/test", map[string]string{"host": "laptop", "user": "alice", "method": "key"}, &res)
	require.Equal(t, "ok", res.Status, "no second confirmation")
}

func TestAnUnreachableMachineSaysSo(t *testing.T) {
	f := newFixture(t)
	var res httpapi.TestResult
	f.call(t, "POST", "/api/setup/test", map[string]string{"host": "old-box.invalid", "user": "alice"}, &res)
	require.Equal(t, "error", res.Status)
	require.Equal(t, "offline", res.Code, res.Message)
	require.Contains(t, res.Message, "connected to Tailscale")
}

// Finishing setup is remembered, and a machine that joins afterwards is
// pointed out as new.
func TestFinishingSetupAndNewMachines(t *testing.T) {
	f := newFixture(t)
	var v httpapi.SetupView
	require.Equal(t, http.StatusOK, f.call(t, "POST", "/api/setup/complete", map[string]bool{"complete": true}, &v))
	require.True(t, v.Complete)
	require.Equal(t, 0, v.NewMachines, "machines present at setup are not new")

	f.peers.add("new-box", "linux", true)
	f.call(t, "GET", "/api/setup", nil, &v)
	require.Equal(t, 1, v.NewMachines)
	require.True(t, machine(v, "new-box").New)

	require.Equal(t, http.StatusNoContent, f.call(t, "POST", "/api/setup/seen", nil, nil))
	f.call(t, "GET", "/api/setup", nil, &v)
	require.Equal(t, 0, v.NewMachines)
}

// papaya belongs to whoever signed it in; a tagged node belongs to nobody.
func TestOwnerOf(t *testing.T) {
	st := &ipnstate.Status{
		Self: &ipnstate.PeerStatus{UserID: 7},
		User: map[tailcfg.UserID]tailcfg.UserProfile{7: {LoginName: "alice@example.com"}},
	}
	require.Equal(t, "alice@example.com", tsnode.OwnerOf(st))
	require.Equal(t, "", tsnode.OwnerOf(nil))
	require.Equal(t, "", tsnode.OwnerOf(&ipnstate.Status{}))

	tags := views.SliceOf([]string{"tag:server"})
	st.Self.Tags = &tags
	require.Equal(t, "", tsnode.OwnerOf(st), "a tagged node has no owner to default to")
}

// With no access list, only the owner gets in.
func TestOwnerOnlyAccess(t *testing.T) {
	owner := "alice@example.com"
	mk := func(login string) int {
		auth := tsnode.NewAuth(whoIs{login}, map[string]bool{owner: true}, nil)
		rec := httptest.NewRecorder()
		auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).
			ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		return rec.Code
	}
	require.Equal(t, http.StatusOK, mk(owner))
	require.Equal(t, http.StatusForbidden, mk("bob@example.com"))
	require.Equal(t, http.StatusForbidden, mk(""))
}

// The one-line join: the script carries Papaya's address and key, and running
// it tells Papaya the account on the machine Tailscale says it came from.
func TestOneLineJoinConnectsTheCallingMachine(t *testing.T) {
	f := newFixture(t)

	res, err := http.Get(f.srv.URL + "/join")
	require.NoError(t, err)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	script := string(b)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, script, "ssh-ed25519 AAAAtest papaya")
	require.Contains(t, script, "PAPAYA='"+f.srv.URL+"'")
	require.Contains(t, script, "/api/setup/join")

	var v httpapi.SetupView
	require.Equal(t, 400, f.call(t, "POST", "/api/setup/join", map[string]any{"user": "root", "sshd": true}, nil), "root is refused")
	require.Equal(t, 400, f.call(t, "POST", "/api/setup/join", map[string]any{"user": "a b", "sshd": true}, nil))
	require.Equal(t, 200, f.call(t, "POST", "/api/setup/join", map[string]any{"user": "alex", "sshd": false, "os": "Darwin"}, nil))

	require.Equal(t, 200, f.call(t, "GET", "/api/setup", nil, &v))
	m := machine(v, "laptop")
	require.True(t, m.Selected, "running the line chooses the machine")
	require.True(t, m.Joined)
	require.False(t, m.SSHD, "Remote Login was off")
	require.Equal(t, "alex", m.User)
	require.Equal(t, "key", m.Method)
	require.False(t, machine(v, "server").Joined, "only the calling machine is affected")

	// Host keys sent with the join are pinned for that machine, so its first
	// connection needs no fingerprint prompt.
	pub := f.keyHost.AuthorizedHostKey()
	require.Equal(t, 200, f.call(t, "POST", "/api/setup/join", map[string]any{"user": "alex", "sshd": true, "hostkeys": []string{pub, "garbage"}}, nil))
	known, err := os.ReadFile(f.trusted)
	require.NoError(t, err)
	require.Contains(t, string(known), "laptop ")
	require.Contains(t, string(known), strings.Fields(pub)[1])
}
