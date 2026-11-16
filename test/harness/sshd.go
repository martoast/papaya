package harness

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHD is a real OpenSSH server running in a container.
type SSHD struct {
	// Addr is the host:port the container is reachable on.
	Addr string
	// KeyPath is the private key authorized on the server.
	KeyPath string
	// KnownHosts pins the server's real host key under the logical name.
	KnownHosts string
	// User is the remote login.
	User string
	// LogicalHost is the name tests dial, mapped to Addr by the dialer.
	LogicalHost string
}

// StartSSHD brings up the OpenSSH container and waits until it accepts
// connections. It skips the test unless integration testing is requested.
func StartSSHD(t *testing.T) *SSHD {
	t.Helper()

	if os.Getenv("PAPAYA_INTEGRATION") == "" {
		t.Skip("integration test: set PAPAYA_INTEGRATION=1 (requires Docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("integration test: docker not available")
	}

	testDir := testDataDir(t)
	keysDir := filepath.Join(testDir, "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		t.Fatalf("create keys dir: %v", err)
	}

	// The key pair is generated rather than committed. A private key in the
	// repository is a liability even when it is only meant for tests.
	keyPath := filepath.Join(keysDir, "id_test")
	generateTestKey(t, keyPath)

	compose := filepath.Join(testDir, "docker-compose.test.yml")
	down := func() {
		cmd := exec.Command("docker", "compose", "-f", compose, "down", "-v", "--timeout", "5")
		cmd.Dir = testDir
		cmd.Run()
	}

	down() // clear any leftover container from a previous run

	up := exec.Command("docker", "compose", "-f", compose, "up", "-d", "--wait")
	up.Dir = testDir
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("docker compose up failed: %v\n%s", err, out)
	}
	t.Cleanup(down)

	addr := "127.0.0.1:2222"
	waitForPort(t, addr, 60*time.Second)

	// Give sshd a moment past the port opening to finish key setup.
	hostKey := scanHostKey(t, addr, 60*time.Second)

	const logical = "devbox-1"
	knownHosts := filepath.Join(keysDir, "known_hosts")
	line := fmt.Sprintf("%s %s\n", logical,
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))))
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	return &SSHD{
		Addr:        addr,
		KeyPath:     keyPath,
		KnownHosts:  knownHosts,
		User:        "tester",
		LogicalHost: logical,
	}
}

// Pause suspends the container's processes, producing a black-holed connection
// rather than a clean reset. This is the case keepalive exists to detect.
func (s *SSHD) Pause(t *testing.T) {
	t.Helper()
	run(t, "docker", "pause", "papaya-test-sshd")
	t.Cleanup(func() { exec.Command("docker", "unpause", "papaya-test-sshd").Run() })
}

// Unpause resumes a paused container.
func (s *SSHD) Unpause(t *testing.T) {
	t.Helper()
	run(t, "docker", "unpause", "papaya-test-sshd")
}

// Stop kills the container, producing a clean connection reset.
func (s *SSHD) Stop(t *testing.T) {
	t.Helper()
	run(t, "docker", "stop", "-t", "1", "papaya-test-sshd")
}

// Exec runs a command inside the container and returns its combined output.
func (s *SSHD) Exec(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"exec", "papaya-test-sshd"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TryExec runs a command inside the container, tolerating failure.
func (s *SSHD) TryExec(args ...string) (string, error) {
	full := append([]string{"exec", "papaya-test-sshd"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return string(out), err
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func waitForPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(500 * time.Millisecond) // polling for container readiness
	}
	t.Fatalf("sshd at %s never became reachable within %s", addr, timeout)
}

// scanHostKey retrieves the server's host key by attempting a handshake.
func scanHostKey(t *testing.T, addr string, timeout time.Duration) ssh.PublicKey {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var captured ssh.PublicKey

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		cfg := &ssh.ClientConfig{
			User: "probe",
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				captured = key
				return nil
			},
			Timeout: 5 * time.Second,
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		c, _, _, err := ssh.NewClientConn(conn, addr, cfg)
		if c != nil {
			c.Close()
		}
		conn.Close()

		// Auth is expected to fail; only the host key matters here.
		if captured != nil {
			return captured
		}
		_ = err
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatal("could not retrieve the sshd host key")
	return nil
}

func generateTestKey(t *testing.T, path string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
}

// testDataDir locates the test/ directory from the running test's working
// directory.
func testDataDir(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	dir := wd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "test", "docker-compose.test.yml")
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Join(dir, "test")
		}
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.test.yml")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	t.Fatalf("could not locate the test directory from %s", wd)
	return ""
}
