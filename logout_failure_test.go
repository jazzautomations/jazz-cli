//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogoutOnlyReportsSuccessAfterSaving(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	original := []byte(`{"chatgpt":broken}`)
	if err := os.WriteFile(filepath.Join(configDir(), "auth.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	u, read := uiTestTermUI(t, Theme{})
	a := &App{cfg: &Config{}, ui: u, root: t.TempDir()}
	a.command("/logout chatgpt")
	out := stripANSI(read())
	if !strings.Contains(out, "não consegui remover") || strings.Contains(out, "removida") {
		t.Fatalf("logout hid failure: %q", out)
	}
	b, err := os.ReadFile(filepath.Join(configDir(), "auth.json"))
	if err != nil || string(b) != string(original) {
		t.Fatalf("invalid auth was overwritten: %q %v", b, err)
	}
}
