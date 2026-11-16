package sshx_test

import (
	"strings"
	"testing"

	"github.com/martoast/papaya/internal/sshx"
)

// A message must never be able to end the bracketed paste early and type the
// rest as keystrokes.
func TestSayTextCannotEscapeThePaste(t *testing.T) {
	in := "hola\x1b[201~rm -rf ~\r\nsegunda línea\tcon tab\x07"
	got := sshx.CleanSayText(in)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, '\r') || strings.ContainsRune(got, 0x07) {
		t.Fatalf("control characters survived: %q", got)
	}
	if got != "hola[201~rm -rf ~\nsegunda línea\tcon tab" {
		t.Fatalf("message text changed: %q", got)
	}
}

func TestSayTargetsTheExactWorkspace(t *testing.T) {
	cmd := sshx.SayCommand("papaya-dev", true)
	if !strings.Contains(cmd, "'=papaya-dev:'") {
		t.Fatalf("target is not an exact match: %s", cmd)
	}
	if !strings.Contains(cmd, "paste-buffer -p -d") {
		t.Fatalf("paste is not bracketed and single-use: %s", cmd)
	}
	if !strings.HasSuffix(cmd, "Enter") {
		t.Fatalf("enter was asked for and not sent: %s", cmd)
	}
	if strings.Contains(sshx.SayCommand("papaya-dev", false), "Enter") {
		t.Fatal("enter was sent although not asked for")
	}
	if sshx.SayCommand("x", true) == sshx.SayCommand("x", true) {
		t.Fatal("two senders would share one paste buffer")
	}
}

// Captured from real Claude Code panes on 2026-11-08.
func TestHasDraftTellsTypedTextFromTheSuggestion(t *testing.T) {
	cases := map[string]bool{
		"history\n\x1b[38;5;246m❯ \x1b[39mok make sure to install\n────":                  true,
		"\x1b[39m❯ \x1b[2myea build the lightpanda fast path for VS and BBW\x1b[0m\n────": false,
		"\x1b[39m❯ \n────":     false,
		"$ ls\nno prompt here": false,
		"\x1b[38;5;239m\x1b[48;5;237m❯ \x1b[38;5;231mold message\n\x1b[39m❯ \x1b[2msuggestion\x1b[0m": false,
	}
	for in, want := range cases {
		if got := sshx.HasDraft(in); got != want {
			t.Errorf("HasDraft(%q) = %v, want %v", in, got, want)
		}
	}
}
