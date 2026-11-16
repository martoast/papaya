package sshx

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// unsafeRe is an allowlist: anything outside word characters, dot, and hyphen
// is replaced. Combined with filepath.Base this makes traversal impossible.
var unsafeRe = regexp.MustCompile(`[^\w.\-]`)

// dotRunRe collapses runs of dots to a single dot.
//
// filepath.Base only understands the host's separator, so a Windows-style name
// like `..\..\windows\system32` reaches us intact on Linux and leaves literal
// ".." segments behind after the allowlist pass. Those cannot traverse without a
// separator, but leaving them is a latent hazard if this name is ever joined by
// something less careful.
var dotRunRe = regexp.MustCompile(`\.{2,}`)

// Upload streams r to the remote host over SFTP and returns the absolute
// remote path.
//
// The SFTP client opens a new channel on the existing pooled SSH connection, so
// an upload costs no extra handshake.
func Upload(client *ssh.Client, uploadDir, name string, r io.Reader) (string, error) {
	sc, err := sftp.NewClient(client,
		sftp.UseConcurrentWrites(true),
		sftp.MaxConcurrentRequestsPerFile(64),
	)
	if err != nil {
		return "", fmt.Errorf("open sftp: %w", err)
	}
	defer sc.Close()

	if err := sc.MkdirAll(uploadDir); err != nil {
		return "", fmt.Errorf("create upload dir: %w", err)
	}

	// Resolve to an absolute path so the injected path works regardless of the
	// shell's current directory.
	absDir, err := sc.RealPath(uploadDir)
	if err != nil {
		absDir = uploadDir
	}
	remote := path.Join(absDir, SafeName(name))

	f, err := sc.Create(remote)
	if err != nil {
		return "", fmt.Errorf("create remote file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		sc.Remove(remote)
		return "", fmt.Errorf("write remote file: %w", err)
	}
	if err := f.Close(); err != nil {
		sc.Remove(remote)
		return "", fmt.Errorf("close remote file: %w", err)
	}

	// Uploads may be screenshots of anything; keep them owner-only.
	if err := sc.Chmod(remote, 0o600); err != nil {
		return remote, nil // file is written; permissions are best-effort
	}
	return remote, nil
}

// SafeName reduces an untrusted client filename to something that cannot escape
// the upload directory or confuse the shell.
func SafeName(n string) string {
	base := filepath.Base(n) // strips any ../ path components
	base = unsafeRe.ReplaceAllString(base, "_")
	base = dotRunRe.ReplaceAllString(base, ".")
	base = strings.TrimLeft(base, ".") // avoid hidden files and bare "."
	if base == "" {
		base = "upload"
	}
	if len(base) > 100 {
		base = base[:100]
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), base)
}

// ShellQuote wraps s in single quotes so the value is inert no matter what
// characters it contains.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// safeUnquotedRe matches paths that carry no meaning to a POSIX shell and so
// need no quoting. It is the conservative allowlist used by shlex.quote: every
// character in it is inert as part of a shell word.
var safeUnquotedRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// QuoteIfNeeded returns s unquoted when it is already inert, and shell-quoted
// otherwise.
//
// This matters because the injected path is not always read by a shell. When the
// tab is running an interactive program - an agent session, an editor - that
// program receives the raw bytes on stdin and a shell never strips the quotes,
// so a always-quoted path arrives as a filename literally beginning with an
// apostrophe and cannot be opened. SafeName already restricts the filename to
// word characters, dots, and hyphens, so in practice the path is inert and goes
// through bare; quoting remains for the unusual case of a home or upload
// directory containing spaces or metacharacters, where a shell is the only thing
// that could make sense of it anyway.
func QuoteIfNeeded(s string) string {
	if s != "" && safeUnquotedRe.MatchString(s) {
		return s
	}
	return ShellQuote(s)
}
