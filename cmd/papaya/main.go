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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/martoast/papaya/internal/config"
	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
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

	startCtx, cancelStart := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelStart()

	node, err := tsnode.Start(startCtx, cfg, verbose)
	if err != nil {
		return err
	}
	defer node.Close()

	pool := sshx.NewPool(node, sshx.Config{
		UseTailscaleSSH:     cfg.UseTailscaleSSH,
		KeyPath:             cfg.KeyPath,
		KnownHostsPath:      cfg.KnownHostsPath,
		InsecureSkipHostKey: cfg.InsecureSkipHostKey,
		UploadDir:           cfg.UploadDir,
		DialTimeout:         10 * time.Second,
	})
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

	api := httpapi.NewWithTranscriber(h,
		tsnode.NewAuth(node.LocalClient(), cfg.AllowedUsers, cfg.AllowedTags),
		node, web.Assets(), transcriber, httpapi.Options{
			DefaultSSHUser: cfg.SSHUser,
			StaticHosts:    cfg.Hosts,
			MaxUploadBytes: cfg.MaxUploadBytes,
			MaxAudioBytes:  cfg.MaxAudioBytes,
			MaxSessions:    cfg.MaxSessions,
			Hostname:       cfg.Hostname,
			HostAllowed:    cfg.HostAllowed,
			SSHUserAllowed: cfg.SSHUserAllowed,
			SSHUserForHost: cfg.UserForHost,
		})

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

	var ln net.Listener
	switch {
	case cfg.TLS && cfg.TLSCert != "":
		// A certificate you supply, which is what allows a custom domain:
		// Tailscale only issues for names inside your tailnet's ts.net domain.
		reloader, err := tlsx.New(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return err
		}
		names, notAfter := reloader.Leaf()
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

	if cfg.TranscriptionEnabled() && !cfg.TLS {
		log.Printf("WARNING: voice transcription is configured but TLS is off. " +
			"Browsers block microphone access outside a secure context, so the " +
			"microphone will not work. Set PAPAYA_TLS=true.")
	}

	srv := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// No WriteTimeout: it would kill long-lived WebSockets.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("serving on tailnet at %s://%s%s", scheme, cfg.Hostname, listenAddr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

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
