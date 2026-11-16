package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// EnsureKey returns papaya's own SSH key at path, generating an ed25519 key the
// first time. The result is the public half as one authorized_keys line, which
// is what a person pastes onto a machine that does not run Tailscale SSH.
//
// Calling it again returns the same key: it is generated once and kept with the
// rest of papaya's state, so a restart or an upgrade never asks anyone to paste
// a new line.
func EnsureKey(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil {
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
		return authorizedLine(signer.PublicKey()), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "papaya")
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return authorizedLine(sshPub), nil
}

func authorizedLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " papaya"
}
