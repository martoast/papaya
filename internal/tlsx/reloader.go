// Package tlsx serves a certificate you supply, rather than one Tailscale issues.
//
// Tailscale can only issue certificates for names inside your tailnet's ts.net
// domain. Serving papaya under your own domain therefore means bringing your own
// certificate — and because the node is not reachable from the public internet,
// that certificate has to come from a DNS-01 challenge.
package tlsx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// checkInterval bounds how often the files are stat'd. Certificates change
// every couple of months; checking on every handshake would be pointless work.
const checkInterval = 30 * time.Second

// Reloader holds a certificate and picks up replacements without a restart.
//
// Certificates from Let's Encrypt expire after 90 days, so renewal is routine
// rather than exceptional. Requiring a restart to pick one up is how a service
// ends up serving an expired certificate at an inconvenient moment.
type Reloader struct {
	certPath string
	keyPath  string

	mu        sync.RWMutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

// New loads the pair once so a bad path or an unreadable key fails at startup
// rather than on the first connection.
func New(certPath, keyPath string) (*Reloader, error) {
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("both a certificate and a key path are required")
	}

	r := &Reloader{certPath: certPath, keyPath: keyPath}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// Leaf reports the names the loaded certificate is valid for, for logging.
func (r *Reloader) Leaf() (names []string, notAfter time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.cert == nil || r.cert.Leaf == nil {
		return nil, time.Time{}
	}
	names = append(names, r.cert.Leaf.DNSNames...)
	if len(names) == 0 && r.cert.Leaf.Subject.CommonName != "" {
		names = append(names, r.cert.Leaf.Subject.CommonName)
	}
	return names, r.cert.Leaf.NotAfter
}

// GetCertificate satisfies tls.Config.GetCertificate.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.maybeReload()

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cert == nil {
		return nil, fmt.Errorf("no certificate loaded")
	}
	return r.cert, nil
}

// TLSConfig returns a config that serves the reloading certificate.
func (r *Reloader) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: r.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// maybeReload reloads when either file has changed on disk.
func (r *Reloader) maybeReload() {
	r.mu.RLock()
	due := time.Since(r.lastCheck) >= checkInterval
	r.mu.RUnlock()
	if !due {
		return
	}

	r.mu.Lock()
	r.lastCheck = time.Now()
	certMod, keyMod := r.certMod, r.keyMod
	r.mu.Unlock()

	cs, err1 := os.Stat(r.certPath)
	ks, err2 := os.Stat(r.keyPath)
	if err1 != nil || err2 != nil {
		return // keep serving what we have rather than dropping TLS entirely
	}
	if cs.ModTime().Equal(certMod) && ks.ModTime().Equal(keyMod) {
		return
	}

	// A failed reload leaves the previous certificate in place: half-written
	// files during a renewal are normal, and the old certificate is still valid.
	_ = r.load()
}

func (r *Reloader) load() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return fmt.Errorf("load certificate %s: %w", r.certPath, err)
	}

	// Parsing the leaf up front lets callers report the names and expiry, and
	// saves the handshake doing it every time.
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		if leaf, err := parseLeaf(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		}
	}

	cs, _ := os.Stat(r.certPath)
	ks, _ := os.Stat(r.keyPath)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.cert = &cert
	if cs != nil {
		r.certMod = cs.ModTime()
	}
	if ks != nil {
		r.keyMod = ks.ModTime()
	}
	return nil
}

func parseLeaf(der []byte) (*x509.Certificate, error) {
	return x509.ParseCertificate(der)
}

// ForceCheck makes the next GetCertificate re-stat the files instead of waiting
// out the poll interval. Used by tests; harmless elsewhere.
func (r *Reloader) ForceCheck() {
	r.mu.Lock()
	r.lastCheck = time.Time{}
	r.mu.Unlock()
}
