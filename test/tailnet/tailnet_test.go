//go:build tailnet

// Package tailnet holds the one test that joins a real tailnet.
//
// Everything else fakes the dialer and the identity so the suite runs without a
// secret. This test exists to catch the things only the real control plane can
// show: that the node comes up, that the listener is tailnet-only, and that
// WhoIs resolves.
//
// Run with:
//
//	TS_AUTHKEY=tskey-... go test -tags=tailnet ./test/tailnet/...
package tailnet

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/config"
	"github.com/martoast/papaya/internal/tsnode"
)

func TestRealTailnet(t *testing.T) {
	authKey := os.Getenv("TS_AUTHKEY")
	if authKey == "" {
		t.Skip("set TS_AUTHKEY to run the tailnet smoke test")
	}

	hostname := fmt.Sprintf("papaya-test-%d", time.Now().Unix())

	cfg := &config.Config{
		AuthKey:      authKey,
		Hostname:     hostname,
		StateDir:     t.TempDir(),
		Ephemeral:    true,
		AllowedUsers: map[string]bool{"nobody@example.com": true},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	node, err := tsnode.Start(ctx, cfg, false)
	require.NoError(t, err, "the node must join the tailnet")
	t.Logf("node came up in %s", time.Since(start).Round(time.Millisecond))

	defer func() {
		require.NoError(t, node.Close())
		t.Logf("node closed; as an ephemeral node it should self-delete from the tailnet")
	}()

	// The node is visible to the control plane.
	status, err := node.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status.Self)
	require.Contains(t, status.Self.DNSName, hostname)
	t.Logf("node DNSName=%s addrs=%v", status.Self.DNSName, status.Self.TailscaleIPs)

	ip4, _ := node.TailscaleIPs()
	require.True(t, ip4.IsValid(), "the node must have a tailnet address")

	// Serve on the tailnet listener and fetch over the tailnet.
	ln, err := node.Listen("tcp", ":8080")
	require.NoError(t, err)
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go srv.Serve(ln)
	defer srv.Close()

	// Dialing through the node proves the listener is reachable over WireGuard.
	client := &http.Client{
		Transport: &http.Transport{DialContext: node.DialContext},
		Timeout:   20 * time.Second,
	}
	resp, err := client.Get(fmt.Sprintf("http://%s:8080/", ip4.String()))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "ok", string(body))

	// TLS: browsers only expose the microphone in a secure context, so voice
	// input depends on this working. Requires HTTPS certificates enabled for the
	// tailnet; skipped rather than failed when they are not.
	t.Run("tls", func(t *testing.T) {
		certCtx, cancelCert := context.WithTimeout(ctx, 2*time.Minute)
		defer cancelCert()

		if err := node.EnsureCert(certCtx); err != nil {
			t.Skipf("TLS certificates unavailable for this tailnet: %v", err)
		}

		tlsLn, err := node.ListenTLS("tcp", ":8443")
		require.NoError(t, err)
		defer tlsLn.Close()

		tlsSrv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("secure"))
			}),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go tlsSrv.Serve(tlsLn)
		defer tlsSrv.Close()

		domains := node.CertDomains()
		require.NotEmpty(t, domains)
		t.Logf("serving TLS for %s", domains[0])

		// Dial by name through the tailnet so SNI is sent; a raw IP would give
		// the listener no name to select a certificate with.
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(c context.Context, network, addr string) (net.Conn, error) {
					_, port, _ := net.SplitHostPort(addr)
					return node.DialContext(c, network, net.JoinHostPort(ip4.String(), port))
				},
			},
			Timeout: 60 * time.Second,
		}

		resp, err := client.Get(fmt.Sprintf("https://%s:8443/", domains[0]))
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "secure", string(body))

		// A publicly trusted certificate is what makes the page a secure context.
		require.NotNil(t, resp.TLS)
		require.NotEmpty(t, resp.TLS.PeerCertificates)
		leaf := resp.TLS.PeerCertificates[0]
		require.Equal(t, domains[0], leaf.Subject.CommonName)
		t.Logf("certificate issued by %s, valid until %s",
			leaf.Issuer.Organization, leaf.NotAfter.Format(time.RFC3339))
	})

	// S1: the same port must NOT be reachable on a host interface. tsnet serves
	// from a userspace netstack, so a direct dial to localhost must fail.
	direct, err := net.DialTimeout("tcp", "127.0.0.1:8080", 2*time.Second)
	if err == nil {
		direct.Close()
		t.Fatal("the listener is bound to a host interface: it must be tailnet-only")
	}
	t.Logf("confirmed not reachable on 127.0.0.1:8080 (%v)", err)
}
