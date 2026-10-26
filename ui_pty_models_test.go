//go:build darwin || linux

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The model picker in a real PTY (ui_models.go): /model opens the grouped
// list; a digit, the arrows and the filter each pick a model; Esc keeps it.

func ptyModelsCfg(t *testing.T) map[string]any {
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	return map[string]any{"model": "fake/m1", "mode": "auto",
		"providers": map[string]any{"fake": map[string]any{"type": "openai", "base_url": srv.URL + "/v1", "no_key": true, "default_model": "m1"}},
		"models":    map[string]any{"fake/m2": map[string]any{}, "fake/m3": map[string]any{}}}
}

func TestPTYModelPicker(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStartIn(t, "xterm-256color", ptyModelsCfg(t), t.TempDir(), t.TempDir(), nil, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)

	open := func() string {
		s.typeSlow("/model")
		s.write("\r")
		s.waitFor("the model picker", 15*time.Second, func(sc string) bool {
			return strings.Contains(sc, "modelo atual: fake/m1") && strings.Contains(sc, pkHint)
		})
		return s.screen()
	}
	// 1. grouped by provider, the current model first and marked, tags
	sc := open()
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("picker:\n%s", sc)
	}
	for _, want := range []string{"fake", "1 fake/m1", "atual", "fake/m2", "fake/m3", "pago"} {
		if !strings.Contains(sc, want) {
			t.Errorf("picker lacks %q:\n%s", want, sc)
		}
	}
	if !strings.Contains(sc, "sem chave") {
		t.Errorf("no provider shows the no-key state:\n%s", sc)
	}
	// 2. Esc keeps the model and erases the list
	s.write("\x1b")
	s.waitFor("the cancel", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "modelo atual: fake/m1 (nada mudou)") && ptyPrompt(sc)
	})
	if strings.Contains(s.allText(), pkHint) {
		t.Errorf("the picker was left in the transcript")
	}
	// 3. a digit picks the row (two-digit entry: the list has more than 9
	// rows, so "2" waits for a second digit and Enter takes row 2)
	open()
	s.write("2")
	s.waitFor("the pending number", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "nº 2_") })
	s.write("\r")
	s.waitFor("fake/m2", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "modelo: fake/m2 (salvo como padrão)") && strings.Contains(sc, "fake/m2") && ptyPrompt2(sc, "fake/m2")
	})
	// 4. arrows: down from the current row (m2) is m3
	s.typeSlow("/model")
	s.write("\r")
	s.waitFor("the picker again", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "modelo atual: fake/m2") })
	s.write("\x1b[B")
	time.Sleep(100 * time.Millisecond)
	s.write("\r")
	s.waitFor("fake/m3", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "modelo: fake/m3 (salvo como padrão)") })
	// 5. typing filters: "m1" leaves the one row, Enter picks it
	s.typeSlow("/model")
	s.write("\r")
	s.waitFor("the picker once more", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "modelo atual: fake/m3") })
	s.typeSlow("fake/m1")
	s.waitFor("the filtered list", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "filtro › fake/m1") && !strings.Contains(sc, "fake/m2  ")
	})
	s.write("\r")
	s.waitFor("fake/m1 again", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "modelo: fake/m1 (salvo como padrão)") })
}

// ptyPrompt2 is ptyPrompt for another model name in the footer.
func ptyPrompt2(sc, model string) bool {
	return strings.Contains(sc, "▸▸ auto · "+model) && strings.Contains(sc, "╭")
}
