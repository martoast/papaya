// Package arch holds structural guards over the production source.
//
// These encode invariants that a reviewer would otherwise have to re-check by
// hand on every change: they are cheap, they survive refactors, and they fail
// with a file:line rather than a code review comment.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sourceRoots are the directories holding production code.
var sourceRoots = []string{"internal", "cmd", "web"}

type callSite struct {
	File string
	Line int
	Expr string
}

// findCalls walks the production source and reports every selector call whose
// method name matches one of names.
func findCalls(t *testing.T, names ...string) []callSite {
	t.Helper()

	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}

	repo := repoRoot(t)
	fset := token.NewFileSet()
	var found []callSite

	for _, root := range sourceRoots {
		dir := filepath.Join(repo, root)
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Structural rules govern production code; tests may do as they like.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if !want[sel.Sel.Name] {
					return true
				}

				pos := fset.Position(call.Pos())
				rel, _ := filepath.Rel(repo, pos.Filename)
				found = append(found, callSite{
					File: filepath.ToSlash(rel),
					Line: pos.Line,
					Expr: exprString(sel),
				})
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	return found
}

func exprString(sel *ast.SelectorExpr) string {
	if ident, ok := sel.X.(*ast.Ident); ok {
		return ident.Name + "." + sel.Sel.Name
	}
	return "." + sel.Sel.Name
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)

	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate the repository root from %s", wd)
	return ""
}

// TestArchSingleWSWriter enforces the gorilla single-writer rule structurally.
// Frame writes must exist in exactly one file, so a future change cannot
// reintroduce concurrent writers without tripping this.
func TestArchSingleWSWriter(t *testing.T) {
	const allowed = "internal/hub/ws.go"

	for _, c := range findCalls(t, "WriteMessage", "NextWriter", "WriteJSON", "WritePreparedMessage") {
		require.Equalf(t, allowed, c.File,
			"%s:%d calls %s outside %s; gorilla permits exactly one concurrent writer, "+
				"so all frame writes must go through the write pump",
			c.File, c.Line, c.Expr, allowed)
	}
}

// TestArchNoParseMultipartForm keeps uploads streaming. ParseMultipartForm
// buffers the entire body to disk before the handler runs, which defeats both
// the size limit and the memory guarantee.
func TestArchNoParseMultipartForm(t *testing.T) {
	found := findCalls(t, "ParseMultipartForm", "FormFile", "ParseForm")
	for _, c := range found {
		t.Errorf("%s:%d calls %s; use r.MultipartReader() so the body streams",
			c.File, c.Line, c.Expr)
	}
}

// TestArchNoNetListen is the core deployment invariant: the HTTP server must
// bind the tsnet listener, never a host interface. A net.Listen in production
// code would expose the terminal to the LAN.
func TestArchNoNetListen(t *testing.T) {
	for _, c := range findCalls(t, "Listen", "ListenAndServe", "ListenAndServeTLS", "ListenPacket") {
		require.NotContainsf(t, c.Expr, "net.",
			"%s:%d calls %s; the listener must come from the tsnet node so the "+
				"server is unreachable from the host LAN",
			c.File, c.Line, c.Expr)
		require.NotContainsf(t, c.Expr, "http.",
			"%s:%d calls %s; serve on the tsnet listener instead",
			c.File, c.Line, c.Expr)
	}
}

// TestArchNoInsecureHostKeyCallback ensures host key verification is never
// bypassed by the stdlib helper. The insecure path is a deliberate,
// env-gated callback that logs on every connection.
func TestArchNoInsecureHostKeyCallback(t *testing.T) {
	for _, c := range findCalls(t, "InsecureIgnoreHostKey") {
		t.Errorf("%s:%d calls %s; host key handling must go through hostKeyCallback, "+
			"which logs a warning per connection when checking is disabled",
			c.File, c.Line, c.Expr)
	}
}

// TestArchNoPanicsInProduction keeps a malformed request from taking the server
// down. Panics in request paths become 500s at best and a dead process at worst.
func TestArchNoPanicsInProduction(t *testing.T) {
	repo := repoRoot(t)
	fset := token.NewFileSet()

	for _, root := range sourceRoots {
		dir := filepath.Join(repo, root)
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || ident.Name != "panic" {
					return true
				}
				pos := fset.Position(call.Pos())
				rel, _ := filepath.Rel(repo, pos.Filename)
				t.Errorf("%s:%d panics in production code; return an error instead",
					filepath.ToSlash(rel), pos.Line)
				return true
			})
			return nil
		})
	}
}

// TestArchSecretsNeverLogged is a coarse guard against a private key or auth key
// reaching the logs.
func TestArchSecretsNeverLogged(t *testing.T) {
	repo := repoRoot(t)

	banned := []string{"cfg.AuthKey", "c.AuthKey", "AuthKey)", "signer)", "pem)"}

	for _, root := range sourceRoots {
		dir := filepath.Join(repo, root)
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			for i, line := range strings.Split(string(data), "\n") {
				lower := strings.ToLower(line)
				if !strings.Contains(lower, "log.print") && !strings.Contains(lower, "logf") {
					continue
				}
				for _, b := range banned {
					if strings.Contains(line, b) {
						rel, _ := filepath.Rel(repo, path)
						t.Errorf("%s:%d appears to log a secret (%q): %s",
							filepath.ToSlash(rel), i+1, b, strings.TrimSpace(line))
					}
				}
			}
			return nil
		})
	}
}
