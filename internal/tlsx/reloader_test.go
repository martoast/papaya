package tlsx_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/tlsx"
)

// writeCert generates a self-signed certificate for the given names.
func writeCert(t *testing.T, dir, name string, dnsNames ...string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")

	certPEM, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certPEM.Close())

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, keyPEM.Close())

	return certPath, keyPath
}

func TestServesTheSuppliedCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "custom", "term.example.com")

	r, err := tlsx.New(certPath, keyPath)
	require.NoError(t, err)

	names, notAfter := r.Leaf()
	require.Equal(t, []string{"term.example.com"}, names)
	require.True(t, notAfter.After(time.Now()))

	// Serve it and confirm a client is offered that name, which is the whole
	// point: a custom domain needs a certificate that matches it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})}
	go srv.Serve(tls.NewListener(ln, r.TLSConfig()))
	defer srv.Close()

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, // self-signed: the name is what is under test
		ServerName:         "term.example.com",
	})
	require.NoError(t, err)
	defer conn.Close()

	leaf := conn.ConnectionState().PeerCertificates[0]
	require.Contains(t, leaf.DNSNames, "term.example.com")
}

// Certificates expire every 90 days, so renewal is routine. Needing a restart to
// pick one up is how a service ends up serving an expired certificate.
func TestPicksUpARenewalWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "first", "old.example.com")

	r, err := tlsx.New(certPath, keyPath)
	require.NoError(t, err)

	names, _ := r.Leaf()
	require.Equal(t, []string{"old.example.com"}, names)

	// Renew in place, as certbot or lego would.
	newCert, newKey := writeCert(t, dir, "second", "new.example.com")
	replace(t, newCert, certPath)
	replace(t, newKey, keyPath)

	// The reloader only stats periodically; reach past that for the test.
	forceCheck(t, r)

	got, err := r.GetCertificate(&tls.ClientHelloInfo{ServerName: "new.example.com"})
	require.NoError(t, err)
	require.Contains(t, got.Leaf.DNSNames, "new.example.com")
}

// A renewal writes two files, and they are not written atomically together. A
// half-written pair must not take TLS down.
func TestKeepsServingWhenReloadFails(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "good", "term.example.com")

	r, err := tlsx.New(certPath, keyPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(certPath, []byte("garbage, mid-write"), 0o600))
	forceCheck(t, r)

	got, err := r.GetCertificate(&tls.ClientHelloInfo{})
	require.NoError(t, err, "a bad reload must not drop the working certificate")
	require.Contains(t, got.Leaf.DNSNames, "term.example.com")
}

func TestMissingMaterialFailsAtStartup(t *testing.T) {
	dir := t.TempDir()

	_, err := tlsx.New(filepath.Join(dir, "nope.crt"), filepath.Join(dir, "nope.key"))
	require.Error(t, err, "a bad path must fail at startup, not on the first connection")

	_, err = tlsx.New("", "")
	require.Error(t, err)
}

func replace(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	require.NoError(t, err)
	// Timestamps have coarse resolution on some filesystems; make the change
	// unambiguous so the mtime check cannot miss it.
	require.NoError(t, os.WriteFile(to, b, 0o600))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(to, future, future))
}

// forceCheck makes the next GetCertificate re-stat the files rather than wait
// out the poll interval.
func forceCheck(t *testing.T, r *tlsx.Reloader) {
	t.Helper()
	r.ForceCheck()
}
