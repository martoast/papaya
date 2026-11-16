// Command papaya serves a web terminal on a tailnet and SSHes out to tailnet
// hosts.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/martoast/papaya/internal/config"
	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/themes"
	"github.com/martoast/papaya/internal/tlsx"
	"github.com/martoast/papaya/internal/transcribe"
	"github.com/martoast/papaya/internal/tsnode"
	"github.com/martoast/papaya/web"
)

func main() {
	var (
		addr    = flag.String("addr", ":80", "tailnet listen address")
		verbose = flag.Bool("verbose", false, "log verbose tailscale backend output")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("papaya: ")

	if err := run(*addr, *verbose); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(addr string, verbose bool) error {
	// Validate before touching the network: a node that has already joined the
	// tailnet with a bad config is worse than one that never started.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration invalid:\n%w", err)
	}
	if len(cfg.AllowedHosts) > 0 {
		log.Printf("hosts restricted to: %s", strings.Join(cfg.AllowedHosts, ", "))
	} else if cfg.OwnerOnly() {
		// A personal install: reaching any of your own machines is the point.
		log.Printf("hosts: any machine on your tailnet (set PAPAYA_ALLOWED_HOSTS to restrict)")
	} else {
		log.Printf("WARNING: PAPAYA_ALLOWED_HOSTS is unset — any tailnet host the ACL permits can be reached")
	}
	if len(cfg.AllowedSSHUsers) > 0 {
		log.Printf("remote users restricted to: %s", strings.Join(sortedKeys(cfg.AllowedSSHUsers), ", "))
	} else {
		log.Printf("remote users: any except root (set PAPAYA_ALLOWED_SSH_USERS to restrict further)")
	}

	if cfg.InsecureSkipHostKey {
		log.Printf("WARNING: PAPAYA_INSECURE_SKIP_HOST_KEY is enabled — host keys are NOT verified")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// With an auth key, joining is automatic and a stall is a fault. Without
	// one, papaya waits for its owner to open the sign-in link, however long
	// that takes.
	startCtx, cancelStart := context.WithCancel(ctx)
	if cfg.AuthKey != "" {
		startCtx, cancelStart = context.WithTimeout(ctx, 2*time.Minute)
	} else {
		log.Printf("no TS_AUTHKEY: waiting for you to sign in to Tailscale (link below)")
	}
	defer cancelStart()

	node, err := tsnode.Start(startCtx, cfg, verbose)
	if err != nil {
		return err
	}
	defer node.Close()

	// Who may use papaya. With no list configured, only the person whose
	// tailnet account papaya was signed in with — never everyone.
	allowedUsers := cfg.AllowedUsers
	owner, err := node.Owner(ctx)
	if err != nil {
		return fmt.Errorf("read tailnet owner: %w", err)
	}
	if cfg.OwnerOnly() {
		if owner == "" {
			return errors.New("this papaya node is tagged, so it belongs to no person and " +
				"cannot default to its owner: set PAPAYA_ALLOWED_USERS (or PAPAYA_ALLOWED_TAGS)")
		}
		allowedUsers = map[string]bool{owner: true}
		log.Printf("access: only %s (the account papaya is signed in with)", owner)
	}

	// The setup screen's choices, papaya's own SSH key, and the host keys it
	// has been told to trust all live in the state directory.
	store, err := setup.Open(cfg.SettingsPath())
	if err != nil {
		return fmt.Errorf("read %s: %w", cfg.SettingsPath(), err)
	}
	keyPath := cfg.KeyPath
	if keyPath == "" {
		keyPath = cfg.GeneratedKeyPath()
	}
	publicKey, err := sshx.EnsureKey(keyPath)
	if err != nil {
		return fmt.Errorf("papaya's SSH key at %s: %w", keyPath, err)
	}

	sshCfg := sshx.Config{
		UseTailscaleSSH:     cfg.UseTailscaleSSH,
		KeyPath:             keyPath,
		KnownHostsPath:      cfg.KnownHostsPath,
		TrustedHostsPath:    cfg.TrustedHostsPath(),
		TailnetHostKeys:     node.PeerSSHHostKeys,
		InsecureSkipHostKey: cfg.InsecureSkipHostKey,
		UploadDir:           cfg.UploadDir,
		DialTimeout:         10 * time.Second,
	}
	pool := sshx.NewPool(node, sshCfg)
	defer pool.CloseAll()

	h := hub.New(pool, hub.Options{
		MaxSessions: cfg.MaxSessions,
		UploadDir:   cfg.UploadDir,
		DetachedTTL: cfg.DetachedTTL,
		Tmux:        cfg.Tmux,
	})
	if cfg.Tmux {
		log.Printf("tmux enabled: sessions persist on the remote host across restarts")
	}
	defer h.CloseAll("server shutting down")

	go h.RunReaper(ctx, time.Minute)

	var transcriber *transcribe.Client
	if cfg.TranscriptionEnabled() {
		transcriber = transcribe.New(transcribe.Options{
			APIKey:    cfg.OpenAIAPIKey,
			BaseURL:   cfg.OpenAIBaseURL,
			Model:     cfg.TranscribeModel,
			Languages: cfg.TranscribeLangs,
			Keywords:  cfg.TranscribeWords,
		})
		log.Printf("voice transcription enabled (model %s)", cfg.TranscribeModel)
	} else {
		log.Printf("voice transcription disabled (set PAPAYA_OPENAI_API_KEY to enable)")
	}

	// Account precedence: "user@host" in PAPAYA_HOSTS, then the account chosen
	// in the setup screen, then PAPAYA_SSH_USER.
	userForHost := setup.AccountResolver(cfg.ExplicitUserForHost, store.UserFor, cfg.SSHUser)

	api := httpapi.NewWithTranscriber(h,
		tsnode.NewAuth(node.LocalClient(), allowedUsers, cfg.AllowedTags),
		node, web.Assets(), transcriber, httpapi.Options{
			DefaultSSHUser: cfg.SSHUser,
			StaticHosts:    append(append([]string(nil), cfg.Hosts...), store.Hosts()...),
			MaxUploadBytes: cfg.MaxUploadBytes,
			MaxAudioBytes:  cfg.MaxAudioBytes,
			MaxSessions:    cfg.MaxSessions,
			Hostname:       cfg.Hostname,
			HostAllowed:    cfg.HostAllowed,
			SSHUserAllowed: cfg.SSHUserAllowed,
			SSHUserForHost: userForHost,
		})

	https := &httpsState{}
	selfName := func() string {
		name, _, _ := strings.Cut(node.DNSName(context.Background()), ".")
		if name == "" {
			name = cfg.Hostname
		}
		return name
	}
	api.EnableSetup(httpapi.SetupDeps{
		Store:          store,
		Peers:          node,
		Dialer:         node,
		SSH:            sshCfg,
		PublicKey:      publicKey,
		Owner:          func() string { return owner },
		HTTPS:          func() httpapi.HTTPSState { return https.get(node) },
		SSHUserAllowed: cfg.SSHUserAllowed,
		Self:           selfName,
	})

	// Themes: colours and animated backdrops, written by agents with
	// papaya-theme and kept in the state directory.
	themeStore, err := themes.Open(filepath.Join(cfg.StateDir, "themes"))
	if err != nil {
		return fmt.Errorf("themes: %w", err)
	}
	api.EnableThemes(themeStore)

	// The listener is the tsnet one, never net.Listen. This is what keeps the
	// terminal off the host's LAN.
	//
	// TLS matters beyond encryption here: browsers only expose the microphone in
	// a secure context, so voice input requires it. The certificate comes from
	// Tailscale, which needs HTTPS enabled for the tailnet.
	scheme, listenAddr := "http", addr
	if cfg.TLS {
		scheme = "https"
		if listenAddr == ":80" {
			listenAddr = ":443"
		}
	}

	srv := &http.Server{
		Handler:           https.redirect(api.Handler()),
		ReadHeaderTimeout: 15 * time.Second,
		// No WriteTimeout: it would kill long-lived WebSockets.
		IdleTimeout: 120 * time.Second,
	}
	errCh := make(chan error, 2)
	serve := func(ln net.Listener, url string) {
		log.Printf("serving on tailnet at %s", url)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}

	var ln net.Listener
	var customName string // the name on a certificate you supplied
	switch {
	case cfg.TLSAuto:
		// Plain HTTP on the tailnet always, so papaya is reachable however
		// the tailnet is set up; HTTPS as well as soon as the tailnet offers
		// a certificate. Once HTTPS is up, page loads over HTTP are sent to it.
		plain, err := node.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("tailnet listen %s: %w", addr, err)
		}
		dns := node.DNSName(ctx)
		host := dns
		if host == "" {
			if ip4, _ := node.TailscaleIPs(); ip4.IsValid() {
				host = ip4.String()
			}
		}
		go serve(plain, "http://"+host)

		if https.tryStart(ctx, node, serve) {
			announce(https.get(node).URL, "")
		} else {
			announce("http://"+host, "HTTPS is off for your tailnet. The page shows how to turn it on.")
			go https.keepTrying(ctx, node, serve)
		}
		goto running

	case cfg.TLS && cfg.TLSCert != "":
		// A certificate you supply, which is what allows a custom domain:
		// Tailscale only issues for names inside your tailnet's ts.net domain.
		reloader, err := tlsx.New(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return err
		}
		names, notAfter := reloader.Leaf()
		if len(names) > 0 {
			customName = names[0]
		}
		log.Printf("serving your own certificate for %v (expires %s)",
			names, notAfter.Format("2006-01-02"))

		// Still the tsnet listener: the certificate changes who the page claims
		// to be, not who can reach it.
		raw, err := node.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("tailnet listen %s: %w", listenAddr, err)
		}
		ln = tls.NewListener(raw, reloader.TLSConfig())

	case cfg.TLS:
		// Pay the issuance cost here rather than in the first visitor's browser.
		certCtx, cancelCert := context.WithTimeout(ctx, 90*time.Second)
		err := node.EnsureCert(certCtx)
		cancelCert()
		if err != nil {
			log.Printf("WARNING: %v — the first HTTPS request will be slow and may time out", err)
		}

		ln, err = node.ListenTLS("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("tailnet TLS listen %s: %w "+
				"(enable HTTPS certificates for the tailnet at https://login.tailscale.com/admin/dns)",
				listenAddr, err)
		}

	default:
		ln, err = node.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("tailnet listen %s: %w", listenAddr, err)
		}
	}

	if cfg.TranscriptionEnabled() && !cfg.TLS && !cfg.TLSAuto {
		log.Printf("WARNING: voice transcription is configured but TLS is off. " +
			"Browsers block microphone access outside a secure context, so the " +
			"microphone will not work. Set PAPAYA_TLS=true.")
	}

	if scheme == "https" {
		https.markOn(node)
	}
	go serve(ln, fmt.Sprintf("%s://%s%s", scheme, cfg.Hostname, listenAddr))
	switch dns := node.DNSName(ctx); {
	case customName != "":
		https.setURL("https://" + customName)
		announce("https://"+customName, "")
	case dns != "":
		announce(scheme+"://"+dns, "")
	}

running:
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}

	// A tab closed moments ago may still be tearing down its remote tmux
	// session. Exiting now would abandon it and leave the shell running on the
	// target host, which is the leak this cleanup exists to prevent. Each
	// teardown is individually bounded, so this cannot wait indefinitely.
	h.WaitForCleanup()
	return nil
}

// sortedKeys returns map keys in a stable order for logging.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// announce prints the address papaya can be opened at, framed so it stands
// out from the log.
func announce(url, note string) {
	lines := []string{
		"Papaya is running. Open it from any device on your tailnet:",
		"",
		"    " + url,
	}
	if note != "" {
		lines = append(lines, "", note)
	}
	tsnode.PrintBox(lines)
}

// httpsState tracks whether HTTPS is serving, for the automatic mode and for
// the setup screen's hint.
type httpsState struct {
	mu  sync.Mutex
	on  bool
	url string
}

func (h *httpsState) markOn(node *tsnode.Node) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.on = true
	if d := node.CertDomains(); len(d) > 0 {
		h.url = "https://" + d[0]
	}
}

func (h *httpsState) setURL(url string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.url = url
}

func (h *httpsState) get(node *tsnode.Node) httpapi.HTTPSState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := httpapi.HTTPSState{On: h.on, URL: h.url, Available: len(node.CertDomains()) > 0}
	if !h.on {
		if dns := node.DNSName(context.Background()); dns != "" {
			st.URL = "http://" + dns
		}
	}
	return st
}

// tryStart serves HTTPS if the tailnet offers a certificate now.
func (h *httpsState) tryStart(ctx context.Context, node *tsnode.Node, serve func(net.Listener, string)) bool {
	domains := node.CertDomains()
	if len(domains) == 0 {
		return false
	}
	certCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	err := node.EnsureCert(certCtx)
	cancel()
	if err != nil {
		log.Printf("tls: %v — staying on HTTP for now, will retry", err)
		return false
	}
	ln, err := node.ListenTLS("tcp", ":443")
	if err != nil {
		log.Printf("tls: listen :443: %v — staying on HTTP for now, will retry", err)
		return false
	}
	h.markOn(node)
	go serve(ln, "https://"+domains[0])
	return true
}

// keepTrying checks every so often whether HTTPS has been switched on for the
// tailnet, and starts serving it when it has — no restart needed.
func (h *httpsState) keepTrying(ctx context.Context, node *tsnode.Node, serve func(net.Listener, string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		if h.tryStart(ctx, node, serve) {
			log.Printf("HTTPS is now on: %s", h.get(node).URL)
			return
		}
	}
}

// redirect sends page loads that arrive over HTTP to the HTTPS address once it
// is serving. Only top-level navigations: an API call or a WebSocket from a
// page still open on the HTTP address keeps working until it reloads.
func (h *httpsState) redirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		on, url := h.on, h.url
		h.mu.Unlock()
		if on && url != "" && r.TLS == nil && r.Method == http.MethodGet &&
			r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.URL.Path != "/healthz" {
			http.Redirect(w, r, url+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}
