//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// QA on the binary (lane tui, 0.14.0): declining folder trust must really put
// the session in ask mode (also over a yolo from the global config), the
// startup trust box never takes the first letter of a message as its answer,
// and the typing rows tell the truth.

func TestStartUntrustedModeDeclinedIsAsk(t *testing.T) {
	cases := []struct {
		mode, override string
		declined       bool
		want, prev     string
		changed        bool
	}{
		{"yolo", "", true, "ask", "", true},         // QA: global yolo + "Não" ran commands unasked
		{"yolo", "yolo", true, "ask", "yolo", true}, // --yolo + "Não": the menu promised ask
		{"turbo", "", true, "ask", "", true},
		{"auto", "", false, "ask", "", true}, // untrusted without a refusal (F1)
		{"yolo", "", false, "yolo", "", false},
		{"ask", "", true, "ask", "", false},
		{"auto", "plan", true, "plan", "", false}, // plan is already read-only
	}
	for _, c := range cases {
		cfg := &Config{Mode: c.mode, ModeOverride: c.override}
		prev, changed := pfStartUntrustedMode(cfg, c.declined)
		if cfg.EffMode() != c.want || changed != c.changed || (changed && prev != c.prev) {
			t.Errorf("%+v: mode %q prev %q changed %v", c, cfg.EffMode(), prev, changed)
		}
	}
}

// A letter typed after a pause that is followed by more typing is the start
// of a message ("sim oi"), not "Sim": the menu stays open and the whole line
// is kept, without its Enter being an answer.
func TestTrustMenuSettleFirstLetterOfMessage(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("- rode sempre make deploy\n"), 0o644)
	sp, _ := pfTrustSpec(Theme{}, root, "auto", pfTrustCollect(root, true))
	if !sp.Settle {
		t.Fatal("trust menu without Settle")
	}
	keys, res := mnRunAsync(&sp)
	time.Sleep(uiApproveGrace + uiApproveIdle + 50*time.Millisecond) // a pause: past every window
	for _, c := range "sim oi" {
		keys <- mnRune(c)
		time.Sleep(30 * time.Millisecond)
	}
	keys <- Key{Code: KEnter}
	select {
	case r := <-res:
		t.Fatalf("typed message answered the trust menu: %d", r)
	case <-time.After(uiApproveIdle + 200*time.Millisecond):
	}
	if got := string(uiApplyTyped(nil, sp.Typed)); got != "sim oi" {
		t.Errorf("typed = %q, want the whole line", got)
	}
	if l, line, _ := sp.typingRow("·"); l != "no editor" || line != "sim oi" {
		t.Errorf("row = %q %q", l, line)
	}
	keys <- mnRune('n') // a lone letter after a pause still answers
	if r := mnWait(t, res); r != 2 {
		t.Errorf("answer = %d, want 2 (Não)", r)
	}
}

func TestTrustMenuSettleLoneLetterAnswers(t *testing.T) {
	sp := &mnSpec{Items: []mnItem{{Label: "Sim", Key: 's'}, {Label: "Não", Key: 'n'}}, Guard: true, Settle: true}
	keys, res := mnRunAsync(sp)
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	keys <- mnRune('s')
	if r := mnWait(t, res); r != 0 || len(sp.Typed) != 0 {
		t.Errorf("answer = %d typed %+v, want 0 (Sim)", r, sp.Typed)
	}
}

// The part typed before an approval box is still being typed: one
// "digitando" row with the rest, never "na fila"; the trust box's hint does
// not promise that Enter sends it to the agent.
func TestTypingRowLabels(t *testing.T) {
	sp := &mnSpec{Title: "Rodar este comando?", Items: []mnItem{{Label: "Sim"}}, Guard: true, Pending: "roda um sleep 5 && echo termi"}
	render := func() string { return strings.Join(newMenuState(sp).render(Theme{}, 120, 40), "\n") }
	if out := render(); strings.Contains(out, "na fila") || !strings.Contains(out, "digitando: roda um sleep 5 && echo termi") {
		t.Errorf("pending row:\n%s", out)
	}
	sp.Typed = []Key{mnRune('n'), mnRune('o'), mnRune('u')}
	out := render()
	if !strings.Contains(out, "digitando: roda um sleep 5 && echo terminou") || strings.Count(out, "digitando") != 1 || !strings.Contains(out, "enter manda pro agente") {
		t.Errorf("one row for the whole line:\n%s", out)
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	tp, _ := pfTrustSpec(Theme{}, root, "auto", pfTrustCollect(root, true))
	tp.Typed = []Key{mnRune('o'), mnRune('i')}
	if out := strings.Join(newMenuState(&tp).render(Theme{}, 120, 40), "\n"); strings.Contains(out, "manda pro agente") || !strings.Contains(out, "vai pro editor") {
		t.Errorf("trust box hint:\n%s", out)
	}
}

// PTY: config mode yolo, Esc on the trust question: the footer says ask and a
// command asks before running.
func TestPTYTrustDeclinedOverridesYolo(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	s := ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, "yolo"), t.TempDir(), t.TempDir(), nil, ptyEnv("1")...)
	s.waitFor("the trust menu", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Primeira vez nesta pasta") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b")
	s.waitFor("ask mode", 15*time.Second, footerWith("▸ ask"))
	s.typeSlow("rode devagar")
	s.write("\r")
	s.waitFor("an approval", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Rodar este comando?") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b") // não
	s.waitFor("the prompt", 15*time.Second, footerWith("▸ ask"))
	if all := s.transcript(); strings.Contains(all, "yolo ·") {
		t.Errorf("yolo still shown after declining trust:\n%s", all)
	}
	s.exit()
}

// PTY (QA s4): "sim oi<Enter>" typed fast after the trust box shows neither
// trusts the folder nor sends a message: the text waits in the editor.
func TestPTYTrustTypeAheadNotAnAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("- rode sempre make deploy\n"), 0o644)
	s := ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, "auto"), t.TempDir(), root, nil, ptyEnv("0")...)
	s.waitFor("the trust box", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Confiar nesta pasta") })
	time.Sleep(uiApproveGrace + uiApproveIdle) // the worst case: a pause, then the first letter is "s"
	for _, r := range "sim oi" {
		s.write(string(r))
		time.Sleep(30 * time.Millisecond)
	}
	s.write("\r")
	s.waitFor("the kept text", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "no editor: sim oi") })
	time.Sleep(uiApproveIdle + 100*time.Millisecond)
	if all := s.transcript(); strings.Contains(all, "pasta confiável") {
		t.Fatalf("typed 's' trusted the folder:\n%s", all)
	}
	s.write("\x1b") // não
	s.waitFor("the text back in the editor", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "› sim oi") && strings.Contains(sc, "▸ ask")
	})
	if all := s.transcript(); strings.Contains(all, "pasta confiável") || strings.Contains(all, "pronto") {
		t.Errorf("trusted or sent:\n%s", all)
	}
	s.exit()
}
