// Package tsnode owns the embedded tailnet node.
//
// papaya joins the tailnet in-process via tsnet: no tailscaled, no TUN device,
// no NET_ADMIN. The critical consequence is that the HTTP server listens on a
// tsnet listener rather than net.Listen, so it is reachable over WireGuard only
// and never from the host's LAN.
package tsnode

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	"github.com/martoast/papaya/internal/config"
)

// Node is a running tailnet node plus a client for its local API.
type Node struct {
	srv *tsnet.Server
	lc  *local.Client
}

// Start brings up the tailnet node and blocks until it is connected.
//
// Callers must not accept connections before this returns: the first start
// performs key exchange and DERP discovery and is slow.
func Start(ctx context.Context, cfg *config.Config, verbose bool) (*Node, error) {
	// tsnet requires Dir to exist before Listen; create it up front rather than
	// discovering the failure at listener setup.
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %q: %w", cfg.StateDir, err)
	}

	srv := &tsnet.Server{
		Dir:       cfg.StateDir,
		Hostname:  cfg.Hostname,
		AuthKey:   cfg.AuthKey,
		Ephemeral: cfg.Ephemeral,
		UserLogf:  log.Printf,
		Logf:      func(string, ...any) {}, // backend logs are very verbose
	}
	if verbose {
		srv.Logf = log.Printf
	}

	status, err := srv.Up(ctx)
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("tailnet up: %w", err)
	}

	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("local client: %w", err)
	}

	if status != nil && status.Self != nil {
		log.Printf("tailnet: node %q up, addrs=%v", status.Self.DNSName, status.TailscaleIPs)
	}
	return &Node{srv: srv, lc: lc}, nil
}

// Listen returns a listener bound to the tailnet only.
func (n *Node) Listen(network, addr string) (net.Listener, error) {
	return n.srv.Listen(network, addr)
}

// EnsureCert provisions the node's TLS certificate before any client asks for
// it.
//
// Tailscale issues certificates lazily, during the first TLS handshake. That
// first handshake then blocks for as long as issuance takes — often ten to
// twenty seconds — which browsers frequently give up on, so the very first visit
// fails rather than merely being slow. Fetching it at startup moves that cost
// somewhere it does not matter.
//
// A failure is not fatal: the listener still works, the first request is just
// slow again.
func (n *Node) EnsureCert(ctx context.Context) error {
	domains := n.srv.CertDomains()
	if len(domains) == 0 {
		return fmt.Errorf("no certificate domain available (is MagicDNS enabled?)")
	}

	// Issuance is retried: the ACME challenge is validated against this node's
	// DNS record, and immediately after a node registers that record may not
	// have propagated yet, which surfaces as an "order status: invalid" that
	// succeeds moments later.
	const attempts = 3
	start := time.Now()

	var err error
	for i := 1; i <= attempts; i++ {
		if _, _, err = n.lc.CertPair(ctx, domains[0]); err == nil {
			log.Printf("tls: certificate ready for %s (%s)", domains[0],
				time.Since(start).Round(time.Millisecond))
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if i < attempts {
			log.Printf("tls: certificate attempt %d/%d for %s failed, retrying: %v",
				i, attempts, domains[0], err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(i) * 5 * time.Second):
			}
		}
	}

	return fmt.Errorf("provision certificate for %s after %d attempts: %w",
		domains[0], attempts, err)
}

// CertDomains lists the names this node can obtain certificates for.
func (n *Node) CertDomains() []string { return n.srv.CertDomains() }

// ListenTLS returns a TLS listener on the tailnet, using a certificate issued
// by Tailscale. Requires HTTPS certificates to be enabled for the tailnet.
//
// Beyond encryption, this is what puts the page in a secure context, which
// browsers require before granting microphone access.
func (n *Node) ListenTLS(network, addr string) (net.Listener, error) {
	return n.srv.ListenTLS(network, addr)
}

// DialContext opens a connection to another tailnet host. The name matches
// *net.Dialer so tests can substitute a plain dialer for the tailnet.
func (n *Node) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// LocalClient exposes the local API client for WhoIs and Status.
func (n *Node) LocalClient() *local.Client { return n.lc }

// TailscaleIPs returns the node's tailnet addresses.
func (n *Node) TailscaleIPs() (ip4, ip6 netip.Addr) { return n.srv.TailscaleIPs() }

// Status returns the current tailnet status, including peers.
func (n *Node) Status(ctx context.Context) (*ipnstate.Status, error) {
	return n.lc.Status(ctx)
}

// Close shuts the node down. For an ephemeral node this deregisters it.
func (n *Node) Close() error { return n.srv.Close() }
