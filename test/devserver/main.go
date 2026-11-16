// Command devserver runs the full papaya stack over plain HTTP on localhost for
// browser tests.
//
// It is test infrastructure, deliberately outside cmd/: it calls net.Listen and
// fakes the tailnet identity, both of which the architectural guards forbid in
// production code. Never deploy this.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/themes"
	"github.com/martoast/papaya/internal/transcribe"
	"github.com/martoast/papaya/internal/tsnode"
	"github.com/martoast/papaya/test/harness"
	"github.com/martoast/papaya/web"
)

var setupMode, httpsAvailable *bool

// fakeTailnet stands in for tailnet status in the setup screen.
type fakeTailnet struct {
	mu sync.Mutex
	st *ipnstate.Status
}

func (f *fakeTailnet) Status(context.Context) (*ipnstate.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}

// fakeWhoIs reports a fixed identity for every request.
type fakeWhoIs struct{ login string }

func (f fakeWhoIs) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	return &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{},
		UserProfile: &tailcfg.UserProfile{LoginName: f.login},
	}, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address")
	user := flag.String("user", "alice@example.com", "fake tailnet identity")
	setupMode = flag.Bool("setup", false, "start with setup unfinished, as a fresh install")
	httpsAvailable = flag.Bool("https-available", false, "pretend the tailnet offers HTTPS certificates")
	flag.Parse()

	if err := run(*addr, *user); err != nil {
		log.Fatalf("devserver: %v", err)
	}
}

func run(addr, user string) error {
	work, err := os.MkdirTemp("", "papaya-devserver-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// Two in-process SSH servers so the host picker and the pooling behaviour
	// are both exercisable from the browser.
	hosts := map[string]*harness.SSHServer{}
	rewrite := map[string]string{}
	var knownHosts strings.Builder

	for _, name := range []string{"devbox-1", "devbox-2"} {
		srv, err := harness.StartSSHServer(harness.AuthNone, filepath.Join(work, name))
		if err != nil {
			return err
		}
		defer srv.Close()

		// One host reports leftover work, so the resume path has something to
		// show without the browser test having to fake a papaya restart.
		if name == "devbox-1" {
			srv.SetWorkspaces("papaya-leftover", "papaya-devbox-1-2")
		}

		hosts[name] = srv
		rewrite[name] = srv.Addr
		knownHosts.WriteString(fmt.Sprintf("%s %s\n", name, srv.AuthorizedHostKey()))
	}

	khPath := filepath.Join(work, "known_hosts")
	if err := os.WriteFile(khPath, []byte(knownHosts.String()), 0o600); err != nil {
		return err
	}

	// Machines for the setup screen, on a fake tailnet: one running Tailscale
	// SSH (it advertises its host key, so it needs no confirmation), one Mac
	// with its own sshd (its key must be confirmed), and one offline.
	tailnet := &fakeTailnet{st: &ipnstate.Status{
		Peer:           map[key.NodePublic]*ipnstate.PeerStatus{},
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "tail1234.ts.net"},
	}}
	for _, m := range []struct {
		name, os string
		online   bool
		tsSSH    bool
	}{
		{"web-server", "linux", true, true},
		{"macbook", "macOS", true, false},
		{"nas", "linux", false, false},
	} {
		peer := &ipnstate.PeerStatus{
			HostName: m.name, DNSName: m.name + ".tail1234.ts.net.", OS: m.os, Online: m.online,
		}
		if m.online {
			srv, err := harness.StartSSHServer(harness.AuthNone, filepath.Join(work, m.name))
			if err != nil {
				return err
			}
			defer srv.Close()
			rewrite[m.name] = srv.Addr
			if m.tsSSH {
				peer.SSH_HostKeys = []string{srv.AuthorizedHostKey()}
			}
		}
		tailnet.st.Peer[key.NewNode().Public()] = peer
	}
	trustedPath := filepath.Join(work, "trusted_known_hosts")
	tailnetKeys := func(host string) []string {
		st, _ := tailnet.Status(context.Background())
		return tsnode.PeerSSHHostKeysFrom(st, host)
	}

	// Setup starts finished, so every other browser test sees the normal app;
	// the setup tests reopen it through the API.
	store, err := setup.Open(filepath.Join(work, "settings.json"))
	if err != nil {
		return err
	}
	if err := store.Update(func(s *setup.Settings) {
		s.Complete = !*setupMode
		if s.Complete {
			s.Seen = []string{"macbook", "nas", "web-server"}
		}
	}); err != nil {
		return err
	}

	uploadDir := filepath.Join(work, "uploads")
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		return err
	}

	dialer := harness.NewCountingDialer(rewrite)
	sshCfg := sshx.Config{
		UseTailscaleSSH:  true,
		KnownHostsPath:   khPath,
		TrustedHostsPath: trustedPath,
		TailnetHostKeys:  tailnetKeys,
	}
	pool := sshx.NewPool(dialer, sshCfg)
	defer pool.CloseAll()

	h := hub.New(pool, hub.Options{
		MaxSessions: 10,
		UploadDir:   uploadDir,
		DetachedTTL: 30 * time.Minute,
		// Resuming only means anything with tmux on, since that is what makes a
		// remote shell outlive the session in the first place.
		Tmux: true,
	})
	defer h.CloseAll("devserver shutting down")

	assets := web.Assets()
	if assets == nil {
		return fmt.Errorf("web/dist is not built: run `npm --prefix web run build` first")
	}

	// A stub transcription endpoint so the voice UI is exercisable without an
	// API key. It echoes a fixed phrase rather than recognising anything.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"git status --short","languages":[{"code":"en"}]}`)
	}))
	defer stub.Close()

	transcriber := transcribe.New(transcribe.Options{
		APIKey:  "sk-devserver-stub",
		BaseURL: stub.URL,
		Model:   "gpt-transcribe",
	})

	// No tailnet status here; the hosts come from the static list and are
	// offered with unknown state, exactly as they would be without peer status.
	api := httpapi.NewWithTranscriber(h,
		tsnode.NewAuth(fakeWhoIs{login: user}, map[string]bool{user: true}, map[string]bool{}),
		nil,
		assets,
		transcriber,
		httpapi.Options{
			DefaultSSHUser: "tester",
			StaticHosts:    []string{"devbox-1", "devbox-2"},
			MaxUploadBytes: 25 << 20,
			MaxAudioBytes:  25 << 20,
			MaxSessions:    10,
			Hostname:       "localhost",
			SSHUserForHost: setup.AccountResolver(nil, store.UserFor, ""),
		})
	api.EnableSetup(httpapi.SetupDeps{
		Store:     store,
		Peers:     tailnet,
		Dialer:    dialer,
		SSH:       sshCfg,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDevServerExampleKeyNotARealOne0000000000 papaya",
		Owner:     func() string { return user },
		HTTPS: func() httpapi.HTTPSState {
			return httpapi.HTTPSState{Available: *httpsAvailable, URL: "http://" + addr}
		},
		SSHUserAllowed: func(u string) bool { return u != "" && u != "root" },
		Self:           func() string { return "papaya" },
	})

	themeStore, err := themes.Open(filepath.Join(work, "themes"))
	if err != nil {
		return err
	}
	api.EnableThemes(themeStore)

	log.Printf("devserver listening on http://%s (identity %s)", addr, user)
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	return srv.ListenAndServe()
}
