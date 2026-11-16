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
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
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

	// tsnet repeats its own one-line version of the sign-in link every few
	// seconds. papaya prints a clearer one with a QR code, so drop the
	// duplicate rather than bury ours under it.
	srv.UserLogf = func(format string, a ...any) {
		if strings.HasPrefix(format, "To start this tsnet server") || strings.HasPrefix(format, "AuthLoop:") {
			return
		}
		log.Printf(format, a...)
	}

	lc, err := srv.LocalClient() // starts the node
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("local client: %w", err)
	}

	// With no auth key, the node waits for its owner to sign in through the
	// browser. Show them where, for as long as it waits.
	signInCtx, stopSignIn := context.WithCancel(ctx)
	defer stopSignIn()
	if cfg.AuthKey == "" {
		go announceSignIn(signInCtx, lc)
	}

	status, err := srv.Up(ctx)
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("tailnet up: %w", err)
	}
	stopSignIn()

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

// announceSignIn prints the "Sign in with Tailscale" link, and a QR code for a
// phone, whenever the node is waiting for one. The link changes if it expires,
// so it is printed again then.
func announceSignIn(ctx context.Context, lc *local.Client) {
	var lastURL, lastState string
	for {
		st, err := lc.StatusWithoutPeers(ctx)
		if err == nil && st != nil {
			switch {
			case st.BackendState == "NeedsLogin" && st.AuthURL != "" && st.AuthURL != lastURL:
				lastURL = st.AuthURL
				printSignIn(st.AuthURL)
			case st.BackendState == "NeedsMachineAuth" && lastState != st.BackendState:
				printBox([]string{
					"Signed in. Your tailnet asks an admin to approve new devices.",
					"",
					"Approve \"papaya\" here, and it will carry on by itself:",
					"https://login.tailscale.com/admin/machines",
				})
			}
			lastState = st.BackendState
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func printSignIn(url string) {
	printBox([]string{
		"Welcome to Papaya. Sign in to connect it to your Tailscale.",
		"",
		"Open this link and sign in (or scan the code below with your phone):",
		"",
		url,
		"",
		"Papaya joins your tailnet as your device, so only you can use it.",
	})
	var qr strings.Builder
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         &qr,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})
	fmt.Fprint(os.Stderr, qr.String())
}

// printBox writes lines to stderr inside a frame that stands out from the log
// lines around it. Plain stderr rather than log: the frame is for a person
// reading a terminal, and timestamps would break it apart.
func printBox(lines []string) {
	width := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > width {
			width = n
		}
	}
	var b strings.Builder
	b.WriteString("\n  ┌" + strings.Repeat("─", width+2) + "┐\n")
	for _, l := range lines {
		b.WriteString("  │ " + l + strings.Repeat(" ", width-len([]rune(l))) + " │\n")
	}
	b.WriteString("  └" + strings.Repeat("─", width+2) + "┘\n\n")
	fmt.Fprint(os.Stderr, b.String())
}

// PrintBox is printBox for the rest of papaya: the address it can be opened at,
// once it is running, is announced the same way.
func PrintBox(lines []string) { printBox(lines) }

// Owner returns the login of the tailnet user this node belongs to, or "" for
// a tagged node, which belongs to no person.
func (n *Node) Owner(ctx context.Context) (string, error) {
	st, err := n.lc.Status(ctx)
	if err != nil {
		return "", err
	}
	return OwnerOf(st), nil
}

// OwnerOf extracts the owning user's login from a status.
func OwnerOf(st *ipnstate.Status) string {
	if st == nil || st.Self == nil {
		return ""
	}
	if st.Self.Tags != nil && st.Self.Tags.Len() > 0 {
		return ""
	}
	if u, ok := st.User[st.Self.UserID]; ok {
		return u.LoginName
	}
	return ""
}

// DNSName is this node's MagicDNS name without the trailing dot, or "" when
// MagicDNS is off.
func (n *Node) DNSName(ctx context.Context) string {
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil || st == nil || st.Self == nil {
		return ""
	}
	return strings.TrimSuffix(st.Self.DNSName, ".")
}

// PeerSSHHostKeys returns the SSH host keys the tailnet advertises for host —
// set for machines running Tailscale SSH — matched by short name, MagicDNS
// name, or address.
func (n *Node) PeerSSHHostKeys(host string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := n.lc.Status(ctx)
	if err != nil {
		return nil
	}
	return PeerSSHHostKeysFrom(st, host)
}

// PeerSSHHostKeysFrom is the lookup behind PeerSSHHostKeys, for tests.
func PeerSSHHostKeysFrom(st *ipnstate.Status, host string) []string {
	if st == nil {
		return nil
	}
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		fqdn := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
		short, _, _ := strings.Cut(fqdn, ".")
		match := h == fqdn || (short != "" && h == short) || strings.EqualFold(h, p.HostName)
		for _, ip := range p.TailscaleIPs {
			if ip.String() == h {
				match = true
			}
		}
		if match {
			return p.SSH_HostKeys
		}
	}
	return nil
}
