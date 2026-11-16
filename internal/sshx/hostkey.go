package sshx

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCallback builds the verification callback for outgoing SSH.
//
// Verification is on by default. The insecure escape hatch logs loudly on every
// connection so it cannot be enabled in production and forgotten.
func hostKeyCallback(cfg Config) (ssh.HostKeyCallback, error) {
	if cfg.InsecureSkipHostKey {
		var once sync.Once
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			once.Do(func() {
				log.Printf("SECURITY WARNING: host key verification is DISABLED " +
					"(PAPAYA_INSECURE_SKIP_HOST_KEY=true). Connections are open to " +
					"machine-in-the-middle attacks. Do not use this outside development.")
			})
			log.Printf("insecure: accepting unverified host key for %s (%s)",
				hostname, ssh.FingerprintSHA256(key))
			return nil
		}, nil
	}

	path := cfg.KnownHostsPath
	if path == "" {
		return nil, errors.New("known_hosts path is empty and host key checking is enabled")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("known_hosts %q unreadable: %w "+
			"(populate it, or set PAPAYA_INSECURE_SKIP_HOST_KEY=true for development)", path, err)
	}

	inner, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("parse known_hosts %q: %w", path, err)
	}

	// knownhosts' own errors are terse. Rewrite them into something that tells
	// the operator exactly which fingerprint to pin or investigate.
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := inner(hostname, remote, key)
		if err == nil {
			return nil
		}

		fp := ssh.FingerprintSHA256(key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) > 0 {
				return fmt.Errorf("host key mismatch for %s: offered %s %s, but %s pins a different key. "+
					"This is either a re-provisioned host or a machine-in-the-middle attack",
					hostname, key.Type(), fp, path)
			}
			return fmt.Errorf("unknown host %s: %s %s is not in %s. "+
				"Verify the fingerprint out-of-band, then add it with: ssh-keyscan %s >> %s",
				hostname, key.Type(), fp, path, hostname, path)
		}
		return fmt.Errorf("host key verification failed for %s (%s): %w", hostname, fp, err)
	}, nil
}
