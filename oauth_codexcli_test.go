package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ccFakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

// With no jazz ChatGPT login, a valid Codex CLI session is borrowed read-only;
// an expired one is reported, never refreshed (that would log the CLI out).
func TestChatGPTBorrowsCodexCLISession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cx := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", cx)
	os.MkdirAll(cx, 0o700)
	write := func(exp time.Time) {
		b := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"refresh_token":"r-cli","account_id":"acct-cli"}}`, ccFakeJWT(exp))
		if err := os.WriteFile(filepath.Join(cx, "auth.json"), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Now().Add(2 * time.Hour))
	cr, err := chatgptCred(t.Context(), false)
	if err != nil || cr == nil || cr.AccountID != "acct-cli" || cr.Refresh != "" {
		t.Fatalf("borrow: cr=%+v err=%v", cr, err)
	}
	write(time.Now().Add(-time.Hour))
	if _, err := chatgptCred(t.Context(), false); err == nil || !strings.Contains(err.Error(), "Codex CLI expirou") {
		t.Fatalf("expired borrow must say so, got %v", err)
	}
	os.Remove(filepath.Join(cx, "auth.json"))
	if _, err := chatgptCred(t.Context(), false); err == nil || !strings.Contains(err.Error(), "jazz login chatgpt") {
		t.Fatalf("no session: %v", err)
	}
}
