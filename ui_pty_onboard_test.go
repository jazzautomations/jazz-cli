//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The onboarding in a real PTY (ui_onboard.go): `jazz setup` shows the three
// steps; the OpenAI key is typed hidden, saved, and the test call answers.

func TestPTYOnboarding(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	cfg := map[string]any{"mode": "auto", "providers": map[string]any{
		"openai": map[string]any{"type": "openai", "base_url": srv.URL + "/v1", "key_env": "JAZZ_TEST_OPENAI_KEY", "default_model": "m1"}}}
	home := t.TempDir()
	s := ptyStartIn(t, "xterm-256color", cfg, home, t.TempDir(), []string{"setup"}, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=", "JAZZ_TEST_OPENAI_KEY=")
	// step 1: the five ways to run
	s.waitFor("step 1", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "1/3 · como rodar") && strings.Contains(sc, "Ollama local")
	})
	sc := s.screen()
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("step 1:\n%s", sc)
	}
	for _, want := range []string{"bem-vindo ao jazz", "Grátis (pool de modelos grátis)", "Chave da Anthropic", "Chave da OpenAI", "Login do ChatGPT"} {
		if !strings.Contains(sc, want) {
			t.Errorf("step 1 lacks %q:\n%s", want, sc)
		}
	}
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b[B\x1b[B") // arrows: the third option (OpenAI)
	time.Sleep(100 * time.Millisecond)
	s.write("\r")
	// step 2: hidden key
	s.waitFor("step 2", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "2/3 · cole a chave de openai") })
	key := "sk-teste-0123456789abcdefXYZ" //gitleaks:allow (chave de brinquedo do teste)
	s.typeSlow(key)
	s.waitFor("the bullets", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "(28 caracteres)") })
	if strings.Contains(s.screen(), "0123456789") {
		t.Fatalf("the key is visible:\n%s", s.screen())
	}
	s.write("\r")
	// step 3: the test call, then the session
	s.waitFor("step 3", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "3/3 · testando openai/m1") && strings.Contains(sc, "✓ HTTP 200") && strings.Contains(sc, "pronto: modelo openai/m1")
	})
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("step 3:\n%s", s.screen())
	}
	s.trustAndPrompt()
	s.waitFor("the input box", 15*time.Second, func(sc string) bool { return ptyPrompt2(sc, "openai/m1") })
	b, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || !strings.Contains(string(b), key) {
		t.Errorf("key not stored in auth.json: %v", err)
	}
	if c, _ := os.ReadFile(filepath.Join(home, "config.json")); !strings.Contains(string(c), `"model": "openai/m1"`) {
		t.Errorf("model not saved:\n%s", c)
	}
}

// TestOnboardFirstRun: only a missing config.json with no key anywhere opens it.
func TestOnboardFirstRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("JAZZ_ONBOARD", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, pc := range cfg.Providers {
		if pc.KeyEnv != "" {
			t.Setenv(pc.KeyEnv, "")
		}
		for _, alt := range pvAltKeyEnv[pc.KeyEnv] {
			t.Setenv(alt, "")
		}
	}
	if !onbFirstRun(cfg) {
		t.Fatal("fresh home: want the onboarding")
	}
	t.Setenv("JAZZ_ONBOARD", "off")
	if onbFirstRun(cfg) {
		t.Error("JAZZ_ONBOARD=off still opens it")
	}
	t.Setenv("JAZZ_ONBOARD", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	if onbFirstRun(cfg) {
		t.Error("a key in the environment still opens it")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if onbFirstRun(cfg) {
		t.Error("an existing config.json still opens it")
	}
}
