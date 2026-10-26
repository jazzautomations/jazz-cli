package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mnKeys(m *mnState, ks ...Key) (int, bool) {
	for _, k := range ks {
		if r, done := m.key(k); done {
			return r, true
		}
	}
	return 0, false
}

func mnRune(r rune) Key { return Key{Code: KRune, R: r} }

func TestMenuStateKeys(t *testing.T) {
	items := []mnItem{{Label: "Sim", Key: 's', Keys: "y"}, {Label: "Sim, sempre", Key: 'a', Keys: "t"}, {Label: "Não, instruir", Key: 'e'}}
	for _, c := range []struct {
		keys []Key
		want int
		done bool
	}{
		{[]Key{{Code: KEnter}}, 0, true},
		{[]Key{mnRune('S')}, 0, true},
		{[]Key{mnRune('y')}, 0, true},
		{[]Key{mnRune('a')}, 1, true},
		{[]Key{mnRune('t')}, 1, true},
		{[]Key{mnRune('2')}, 1, true},
		{[]Key{mnRune('3')}, 2, true},
		{[]Key{mnRune('e')}, 2, true},
		{[]Key{mnRune('n')}, apMenuNo, true},
		{[]Key{{Code: KEsc}}, mnCancel, true},
		{[]Key{{Code: KCtrlC}}, mnCancel, true},
		{[]Key{{Code: KDown}, {Code: KEnter}}, 1, true},
		{[]Key{{Code: KTab}, {Code: KTab}, {Code: KEnter}}, 2, true},
		{[]Key{{Code: KUp}, {Code: KEnter}}, 2, true},
		{[]Key{mnRune('j'), mnRune('j'), mnRune('k'), {Code: KEnter}}, 1, true},
		{[]Key{mnRune('9')}, 0, false}, // no 9th option
		{[]Key{mnRune('x')}, 0, false},
		{[]Key{mnRune('v')}, 0, false}, // no View
	} {
		sp := &mnSpec{Items: items, Alt: map[rune]int{'n': apMenuNo}}
		got, done := mnKeys(newMenuState(sp), c.keys...)
		if got != c.want || done != c.done {
			t.Errorf("%+v: %d %v want %d %v", c.keys, got, done, c.want, c.done)
		}
	}
	sp := &mnSpec{Items: items, View: func() string { return "" }}
	if r, done := newMenuState(sp).key(mnRune('v')); !done || r != mnView {
		t.Error("v with a View")
	}
	// Text (ask_user): an unknown letter starts a free answer; j is a letter there
	sp = &mnSpec{Items: []mnItem{{Label: "SQLite"}, {Label: "Postgres"}}, Text: true}
	if r, done := newMenuState(sp).key(mnRune('j')); !done || r != mnText || sp.typed != 'j' {
		t.Errorf("text mode: %d %v %q", r, done, sp.typed)
	}
	if r, _ := newMenuState(sp).key(mnRune('2')); r != 1 {
		t.Error("text mode: digits still pick")
	}
}

func TestMenuFilterAndScroll(t *testing.T) {
	var items []mnItem
	for _, s := range []string{"pergunta um", "pergunta dois", "Ação três", "outra coisa"} {
		items = append(items, mnItem{Label: s})
	}
	sp := &mnSpec{Items: items, Filter: true, Rows: 2}
	m := newMenuState(sp)
	if r, done := mnKeys(m, mnRune('a'), mnRune('c'), mnRune('a'), mnRune('o'), Key{Code: KEnter}); !done || r != 2 {
		t.Errorf("accent-folded filter: %d %v", r, done)
	}
	m = newMenuState(sp)
	mnKeys(m, mnRune('z'), mnRune('z'))
	if len(m.vis) != 0 {
		t.Error("no match must list nothing")
	}
	if _, done := m.key(Key{Code: KEnter}); done {
		t.Error("enter with nothing listed must not pick")
	}
	if _, done := m.key(Key{Code: KEsc}); done || len(m.filter) != 0 || len(m.vis) != 4 {
		t.Error("first esc clears the filter")
	}
	if r, done := m.key(Key{Code: KEsc}); !done || r != mnCancel {
		t.Error("second esc cancels")
	}
	// letters are filter text, digits too: no shortcuts in a picker
	m = newMenuState(sp)
	if _, done := m.key(mnRune('1')); done {
		t.Error("digit picked in a filter menu")
	}
	// scrolling: 2 rows, the highlight stays visible, an "↑ ↓" line counts the rest
	m = newMenuState(&mnSpec{Items: items, Filter: true, Rows: 2})
	mnKeys(m, Key{Code: KEnd})
	out := stripANSI(strings.Join(m.render(Theme{}, 80, 30), "\n"))
	if !strings.Contains(out, "▸ outra coisa") || strings.Contains(out, "pergunta um") || !strings.Contains(out, "4 de 4") {
		t.Errorf("scroll:\n%s", out)
	}
	mnKeys(m, Key{Code: KPgUp})
	if m.sel != 1 {
		t.Errorf("PgUp: sel = %d", m.sel)
	}
}

// Every row of the box has the same width (wide runes count 2), nothing is
// wider than the line, and a long reason is wrapped, never cut.
func TestMenuRenderWidths(t *testing.T) {
	reason := "instalação de pacote: axios está marcado como malicioso no OSV (MAL-2026-2307); chalk está marcado como malicioso no OSV (MAL-2025-46969)"
	for _, w := range []int{40, 79, 85, 120} {
		sp := &mnSpec{Tag: "Bash", Title: "Rodar este comando?", Items: []mnItem{{Label: "Sim"}, {Label: "Sim, e não perguntar de novo neste projeto para supply: npm install express lodash axios chalk"}, {Label: "Não 🎷"}},
			Body: []string{"npm install express lodash axios chalk"}, Above: "  ↳ na fila: e depois explica"}
		for i, p := range apReasonParts(reason) {
			for _, l := range mnWrap(p, mnBoxWidth(w)-6) {
				if i == 0 {
					l = "⚠ " + l
				}
				sp.Body = append(sp.Body, l)
			}
		}
		lines := newMenuState(sp).render(Theme{}, w, 40)
		boxW := -1
		for _, l := range lines {
			if uiStrW(l) > w {
				t.Errorf("w=%d: line wider than the terminal (%d): %q", w, uiStrW(l), stripANSI(l))
			}
			if s := stripANSI(l); strings.HasPrefix(s, "╭") || strings.HasPrefix(s, "│") || strings.HasPrefix(s, "╰") {
				if boxW < 0 {
					boxW = uiStrW(l)
				} else if uiStrW(l) != boxW {
					t.Errorf("w=%d: box row width %d != %d: %q", w, uiStrW(l), boxW, s)
				}
			}
		}
		flat := ""
		for _, l := range lines {
			flat += strings.Trim(strings.TrimSpace(strings.Trim(stripANSI(l), "│")), " ") + " "
		}
		for _, word := range strings.Fields(reason) {
			if word = strings.TrimSuffix(word, ";"); !strings.Contains(flat, word) {
				t.Errorf("w=%d: reason word %q missing (cut)", w, word)
			}
		}
		if strings.Contains(flat, "MAL-2026-2307);") {
			t.Errorf("w=%d: the two problems must be on separate rows", w)
		}
	}
}

// A tall body is shortened so the options always show.
func TestMenuRenderTallBody(t *testing.T) {
	sp := &mnSpec{Title: "Aplicar esta edição?", Items: []mnItem{{Label: "Sim"}, {Label: "Não"}}}
	for i := 0; i < 60; i++ {
		sp.Body = append(sp.Body, "+ linha")
	}
	lines := newMenuState(sp).render(Theme{}, 80, 20)
	out := stripANSI(strings.Join(lines, "\n"))
	if len(lines) > 20 || !strings.Contains(out, "▸ 1. Sim") || !strings.Contains(out, "2. Não") || !strings.Contains(out, "v ver tudo") {
		t.Errorf("tall body (%d lines):\n%s", len(lines), out)
	}
}

// mnRun: keys inside the grace period never answer; with Guard, a key while
// the user is still typing does not either; v prints the view and the menu
// stays open; the menu is erased and the cursor shown again.
func TestMenuRunGuards(t *testing.T) {
	run := func(sp *mnSpec, script func(keys chan Key)) (int, string) {
		keys := make(chan Key, 64)
		var out strings.Builder
		io := mnTermIO{keys: keys, write: func(s string) { out.WriteString(s) }, size: func() (int, int) { return 80, 24 }}
		res := make(chan int, 1)
		go func() { res <- mnRun(context.Background(), Theme{}, io, sp) }()
		script(keys)
		select {
		case r := <-res:
			return r, out.String()
		case <-time.After(3 * time.Second):
			close(keys)
			return <-res, out.String()
		}
	}
	items := []mnItem{{Label: "Sim", Key: 's'}, {Label: "Não", Key: 'n'}}
	r, out := run(&mnSpec{Items: items}, func(k chan Key) {
		k <- mnRune('n') // typed before the menu could be read
		time.Sleep(uiApproveGrace + 50*time.Millisecond)
		k <- Key{Code: KEnter}
	})
	if r != 0 {
		t.Errorf("grace: %d", r)
	}
	if !strings.HasSuffix(out, "\r\x1b[J\x1b[?25h") || !strings.Contains(out, "\x1b[?25l") {
		t.Errorf("menu not erased or cursor not restored: %q", out[max(len(out)-40, 0):])
	}
	r, _ = run(&mnSpec{Items: items, Guard: true, Since: time.Now()}, func(k chan Key) {
		time.Sleep(uiApproveGrace + 50*time.Millisecond)
		for _, c := range "vamos" { // still typing: ignored
			k <- mnRune(c)
			time.Sleep(20 * time.Millisecond)
		}
		k <- Key{Code: KEnter} // right after typing: ignored too
		time.Sleep(uiApproveIdle + 50*time.Millisecond)
		k <- Key{Code: KDown}
		k <- Key{Code: KEnter}
	})
	if r != 1 {
		t.Errorf("guard: %d", r)
	}
	viewed := false
	r, out = run(&mnSpec{Items: items, View: func() string { viewed = true; return "TUDO\n" }}, func(k chan Key) {
		time.Sleep(uiApproveGrace + 50*time.Millisecond)
		k <- mnRune('v')
		k <- mnRune('v') // view only once
		k <- mnRune('s')
	})
	if r != 0 || !viewed || strings.Count(out, "TUDO") != 1 {
		t.Errorf("view: %d %v %q", r, viewed, out)
	}
	r, _ = run(&mnSpec{Items: items}, func(k chan Key) { close(k) })
	if r != mnCancel {
		t.Error("EOF must cancel")
	}
}

func TestDefaultModeAndBaseMode(t *testing.T) {
	old := turboSandboxOK
	t.Cleanup(func() { turboSandboxOK = old })
	turboSandboxOK = func() bool { return true }
	if pfDefaultMode() != "turbo" {
		t.Error("sandbox: turbo")
	}
	turboSandboxOK = func() bool { return false }
	if pfDefaultMode() != "auto" {
		t.Error("no sandbox: auto")
	}
	c := &Config{}
	c.applyDefaults()
	if c.Mode != "" || c.EffMode() != "auto" {
		t.Errorf("unset mode: %q %q", c.Mode, c.EffMode())
	}
	c.DefaultMode = "turbo"
	if c.EffMode() != "turbo" || c.BaseMode() != "turbo" {
		t.Error("default mode")
	}
	c.Mode = "ask"
	if c.EffMode() != "ask" {
		t.Error("config mode wins over the default")
	}
	c.ModeOverride = "plan"
	if c.EffMode() != "plan" || c.BaseMode() != "ask" {
		t.Error("override wins, base stays")
	}
	for in, want := range map[string]string{"plan": "", "bogus": "", "full-auto": "turbo", "smart": "smart"} {
		c := &Config{Mode: in}
		c.applyDefaults()
		if c.Mode != want {
			t.Errorf("load %q = %q want %q", in, c.Mode, want)
		}
	}
	// an unset mode is not written back as "auto" (it would pin auto forever)
	t.Setenv("JAZZ_HOME", t.TempDir())
	c = &Config{DefaultMode: "turbo"}
	c.applyDefaults()
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if strings.Contains(string(b), `"mode"`) {
		t.Errorf("unset mode saved: %s", b)
	}
	if note, _ := modeNote(&Config{DefaultMode: "turbo"}); !strings.Contains(note, "sandbox") {
		t.Errorf("turbo intro = %q", note)
	}
	if note, _ := modeNote(&Config{DefaultMode: "turbo", ModeIntro: "turbo"}); note != "" {
		t.Error("intro shown twice")
	}
}

func TestYoloWithoutSandboxRequiresExplicitHeadlessAck(t *testing.T) {
	if !pfHeadlessYoloNeedsAck("yolo", false, true) {
		t.Fatal("headless yolo without OS sandbox must require explicit acknowledgement")
	}
	for _, c := range []struct {
		mode     string
		sandbox  bool
		headless bool
	}{{"auto", false, true}, {"yolo", true, true}, {"yolo", false, false}} {
		if pfHeadlessYoloNeedsAck(c.mode, c.sandbox, c.headless) {
			t.Errorf("unexpected ack requirement: %+v", c)
		}
	}
	warning := strings.ToLower(pfYoloNoSandboxWarning())
	if !strings.Contains(warning, "sem sandbox do sistema") || !strings.Contains(warning, "não ficam isolados") {
		t.Fatalf("warning must explain absent OS isolation: %q", warning)
	}
}

func TestTrustSessionOnly(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	st := pfTrustCollect(root, true)
	if len(st.kinds) != 1 || st.kinds[0] != "folder" {
		t.Fatalf("empty folder kinds = %v", st.kinds)
	}
	sp, only := pfTrustSpec(Theme{}, root, "turbo", st)
	if !only || sp.Title != "Primeira vez nesta pasta. Confiar?" || !strings.Contains(strings.Join(sp.Body, "\n"), "modo turbo") {
		t.Errorf("mild question for an empty folder: %+v", sp)
	}
	pfTrustSession(root, st)
	if !pfFolderTrusted(root) {
		t.Fatal("session trust not applied")
	}
	if b, _ := os.ReadFile(pfTrustPath()); bytes.Contains(b, []byte(pfTrustRoot(root))) {
		t.Error("session trust written to disk")
	}
	if err := pfUntrust(root); err != nil || pfFolderTrusted(root) {
		t.Error("revoke must drop session trust too")
	}
	// a folder with config gets the full question with the summary
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("regras"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = pfTrustCollect(root, true)
	sp, only = pfTrustSpec(Theme{}, root, "turbo", st)
	if only || !strings.Contains(strings.Join(sp.Body, "\n"), "AGENTS.md") || sp.View == nil {
		t.Errorf("config folder question: %+v", sp)
	}
}

func TestCliChooseFallback(t *testing.T) {
	items := yesNo("Sim", "Não")
	for in, want := range map[string]int{"s\n": 0, "sim\n": 0, "y\n": 0, "1\n": 0, "2\n": 1, "n\n": 1, "não\n": 1, "\n": 1, "talvez\n": mnCancel} {
		var w bytes.Buffer
		if got := cliChoose(strings.NewReader(in), &w, "Confiar?", items, 1); got != want {
			t.Errorf("%q = %d want %d", in, got, want)
		}
	}
}
