//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdFuzzyScore(t *testing.T) {
	if _, ok := edFuzzy("mdl", "model"); !ok {
		t.Error("mdl should match model")
	}
	if _, ok := edFuzzy("lm", "model"); ok {
		t.Error("out of order must not match")
	}
	a, _ := edFuzzy("ed", "editor.go")
	b, _ := edFuzzy("ed", "xxexdx.go")
	if a <= b {
		t.Errorf("consecutive/word-start match should score higher: %d <= %d", a, b)
	}
}

func TestEdRankCommands(t *testing.T) {
	cmds := []EdCommand{{"/help", ""}, {"/model", ""}, {"/models", ""}, {"/mode", ""}, {"/compact", ""}, {"/context", ""}}
	names := func(cs []EdCommand) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Name)
		}
		return strings.Join(s, " ")
	}
	if got := names(edRankCommands(cmds, "/mdl")); !strings.HasPrefix(got, "/model") {
		t.Errorf("/mdl = %q", got)
	}
	if got := names(edRankCommands(cmds, "/modle")); !strings.Contains(got, "/model") {
		t.Errorf("typo /modle = %q", got)
	}
	if got := names(edRankCommands(cmds, "/ctx")); got != "/context" {
		t.Errorf("/ctx = %q", got)
	}
	if got := names(edRankCommands(cmds, "/")); got != names(cmds) {
		t.Errorf("bare slash keeps the table order: %q", got)
	}
	// the exact name goes first in the menu, even when the table lists a
	// longer name with the same prefix earlier ("/mode" must not run "/model")
	u, keys := uiTestUI(t)
	e := NewEditor(u)
	e.Commands = func() []EdCommand { return cmds }
	uiSendRunes(keys, "/mode\n")
	if got := edReadT(t, e); got != "/mode" {
		t.Errorf("Enter on /mode ran %q", got)
	}
	// fuzzy pick with Enter
	uiSendRunes(keys, "/cmp\n")
	if got := edReadT(t, e); got != "/compact" {
		t.Errorf("/cmp Enter = %q", got)
	}
	// prefix-only palette behind the flag
	t.Setenv("JAZZ_PALETTE", "prefix")
	if items := e.menuItems(true, []rune("/cmp")); len(items) != 0 {
		t.Errorf("prefix palette matched %v", items)
	}
}

func TestEdHistoryPrefix(t *testing.T) {
	u, keys := uiTestUI(t)
	e := NewEditor(u)
	e.history = []string{"git status", "go test ./...", "git log", "git log"}
	uiSendRunes(keys, "git")
	keys <- Key{Code: KUp} // git log (the duplicate is skipped)
	keys <- Key{Code: KUp} // git status
	keys <- Key{Code: KUp} // nothing older with the prefix: stays
	keys <- Key{Code: KEnter}
	if got, _ := e.ReadLine("> ", 2); got != "git status" {
		t.Errorf("prefix walk = %q", got)
	}
	e.history = []string{"git status", "go test ./...", "git log"}
	uiSendRunes(keys, "git")
	keys <- Key{Code: KUp}
	keys <- Key{Code: KUp}
	keys <- Key{Code: KDown}
	keys <- Key{Code: KDown} // back to what was typed
	keys <- Key{Code: KEnter}
	if got, _ := e.ReadLine("> ", 2); got != "git" {
		t.Errorf("down back to typed = %q", got)
	}
	// empty buffer: every entry
	keys <- Key{Code: KUp}
	keys <- Key{Code: KUp}
	keys <- Key{Code: KEnter}
	if got, _ := e.ReadLine("> ", 2); got != "git log" {
		t.Errorf("unfiltered walk = %q", got) // history is now [... git log, git, ...]
	}
	// flag off: Up ignores the typed text
	t.Setenv("JAZZ_HISTORY_PREFIX", "off")
	e.history = []string{"aaa", "bbb"}
	uiSendRunes(keys, "a")
	keys <- Key{Code: KUp}
	keys <- Key{Code: KEnter}
	if got, _ := e.ReadLine("> ", 2); got != "bbb" {
		t.Errorf("prefix off = %q", got)
	}
}

func TestEdMultilineKeys(t *testing.T) {
	buf := []rune("abc\ndefgh\nij")
	if p, ok := edVertical(buf, 7, -1); !ok || p != 3 { // col 3 on "defgh" -> end of "abc"
		t.Errorf("up = %d %v", p, ok)
	}
	if p, ok := edVertical(buf, 6, 1); !ok || p != 12 { // col 2 -> end of "ij"
		t.Errorf("down = %d %v", p, ok)
	}
	if _, ok := edVertical(buf, 1, -1); ok {
		t.Error("up from the first line")
	}
	if b, p := edKillBack(buf, 7); string(b) != "abc\ngh\nij" || p != 4 {
		t.Errorf("ctrl+u = %q %d", string(b), p)
	}
	if b, p := edKillBack(buf, 4); string(b) != "abcdefgh\nij" || p != 3 {
		t.Errorf("ctrl+u at line start = %q %d", string(b), p)
	}
	if b := edKillFwd(buf, 5); string(b) != "abc\nd\nij" {
		t.Errorf("ctrl+k = %q", string(b))
	}
	if b := edKillFwd(buf, 3); string(b) != "abcdefgh\nij" {
		t.Errorf("ctrl+k at line end = %q", string(b))
	}
	// in the editor: Shift+Enter (KAltEnter) adds a line, Up moves inside the
	// input instead of loading history, Ctrl+A goes to the line start
	u, keys := uiTestUI(t)
	e := NewEditor(u)
	e.history = []string{"OLD"}
	uiSendRunes(keys, "um")
	keys <- Key{Code: KAltEnter}
	uiSendRunes(keys, "dois")
	keys <- Key{Code: KUp}
	keys <- Key{Code: KCtrlA}
	uiSendRunes(keys, ">")
	keys <- Key{Code: KEnter}
	if got := edReadT(t, e); got != ">um\ndois" {
		t.Errorf("multi-line edit = %q", got)
	}
}

func TestEdKeyParserShiftEnterAltArrows(t *testing.T) {
	cases := []struct {
		in   string
		want []KeyCode
	}{
		{"\x1b[13;2u", []KeyCode{KAltEnter}},
		{"\x1b[27;2;13~", []KeyCode{KAltEnter}},
		{"\x1b\r", []KeyCode{KAltEnter}},
		{"\x1b[1;3D\x1b[1;3C", []KeyCode{KWordLeft, KWordRight}},
		{"\x1bb\x1bf", []KeyCode{KWordLeft, KWordRight}},
		{"\x1b\x1b[D\x1b\x1b[C", []KeyCode{KWordLeft, KWordRight}},
		{"\x1b\x1b[A", []KeyCode{KEsc, KUp}},
		{"\x15\x0b", []KeyCode{KCtrlU, KCtrlK}},
		{"\x12\x0f", []KeyCode{KCtrlR, KCtrlO}},
	}
	for _, c := range cases {
		kr := uiKeyReaderFrom(strings.NewReader(c.in))
		var got []KeyCode
		for k := range kr.Keys {
			if k.Code == KCtrlD { // EOF
				break
			}
			got = append(got, k.Code)
		}
		if len(got) != len(c.want) {
			t.Errorf("%q -> %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q -> %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestEdMentionPicker(t *testing.T) {
	u, keys := uiTestUI(t)
	read := uiTestStdout(t)
	u.T = Theme{}
	root := t.TempDir()
	for _, f := range []string{"editor.go", "docs/editing/guide.md", "main.go", "src/util/helper.go"} {
		mcpUiWrite(t, filepath.Join(root, f), "x")
	}
	e := NewEditor(u)
	e.Root = root
	if _, _, ok := edMentionTok([]rune("mail me@x"), 9); ok {
		t.Error("an e-mail opened the picker")
	}
	// fuzzy "edt" -> editor.go first (base name beats a directory hit)
	uiSendRunes(keys, "veja @edt")
	keys <- Key{Code: KEnter} // picks, does not submit
	uiSendRunes(keys, "agora\n")
	if got := edReadT(t, e); got != "veja @editor.go agora" {
		t.Errorf("picked = %q", got)
	}
	if out := read(); !strings.Contains(out, "▸ editor.go") {
		t.Errorf("picker not drawn: %q", out)
	}
	// a directory keeps the picker going inside it; Down + Tab picks the 2nd
	uiSendRunes(keys, "@src/")
	keys <- Key{Code: KTab}
	keys <- Key{Code: KEnter}
	if got := edReadT(t, e); got != "@src/util/helper.go " {
		t.Errorf("dir walk = %q", got)
	}
	// Esc closes the picker; Enter then submits as typed
	uiSendRunes(keys, "@hel")
	keys <- Key{Code: KEsc}
	keys <- Key{Code: KEnter}
	if got := edReadT(t, e); got != "@hel" {
		t.Errorf("esc + enter = %q", got)
	}
	// flag off
	t.Setenv("JAZZ_MENTION_PICKER", "off")
	uiSendRunes(keys, "@edt\n")
	if got := edReadT(t, e); got != "@edt" {
		t.Errorf("picker off = %q", got)
	}
}

func TestEdFooterAdapts(t *testing.T) {
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true}
	u.SetMode("auto")
	u.Jobs(2)
	for _, w := range []int{120, 86, 80, 50, 32} {
		uiWidthOverride.Store(int32(w))
		f := stripANSI(u.Footer("openrouter/qwen/qwen3-coder:free", 9000, 10000, 1, 2))
		if uiStrW(f) > w-1 {
			t.Errorf("w=%d footer too wide (%d): %q", w, uiStrW(f), f)
		}
		if !strings.Contains(f, "▸▸ auto") || !strings.HasSuffix(f, "? atalhos") {
			t.Errorf("w=%d footer lost mode or help: %q", w, f)
		}
		if w >= 86 && !strings.Contains(f, "qwen3-coder") {
			t.Errorf("w=%d footer dropped the model: %q", w, f)
		}
		if w == 50 && !strings.Contains(f, "ctx 90%") {
			t.Errorf("w=%d a near-full context must stay: %q", w, f)
		}
	}
	uiWidthOverride.Store(0)
}

func TestEdErrorLineStyle(t *testing.T) {
	raw := uiErrorLine(Theme{}, "deu ruim", 80)
	if l := stripANSI(raw); l != "  ✗ deu ruim" || !strings.HasSuffix(raw, "\n") {
		t.Errorf("error line = %q", l)
	}
	if l := uiErrorLine(Theme{ascii: true}, "x", 80); strings.ContainsAny(l, "✗") {
		t.Errorf("ascii error line = %q", l)
	}
}

func TestEdToggleLast(t *testing.T) {
	u, _ := uiTestUI(t)
	read := uiTestStdout(t)
	u.T = Theme{}
	u.lastOut = uiLastOut{label: "Bash(seq)", text: "1\n2\n3\n"}
	seen := 0
	next := func() string { // output since the last call
		out := read()
		s := out[seen:]
		seen = len(out)
		return s
	}
	u.ToggleLast()
	if out := next(); !strings.Contains(out, "saída completa") || !strings.Contains(out, "ctrl+o recolhe") {
		t.Fatalf("expand: %q", out)
	}
	u.ToggleLast() // nothing printed since: erase it (header + 3 lines + hint)
	if out := next(); out != "\x1b[5A\r\x1b[J" {
		t.Errorf("collapse: %q", out)
	}
	u.ToggleLast()
	next()
	u.Notice("outra coisa")
	u.ToggleLast() // something was printed since: expands again
	if out := next(); !strings.Contains(out, "saída completa") {
		t.Errorf("re-expand after output: %q", out)
	}
}

// edReadT is ReadInput with a deadline: a key script that leaves the editor
// waiting fails the test instead of hanging it.
func edReadT(t *testing.T, e *Editor) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		s, _ := e.ReadInput()
		ch <- s
	}()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatalf("ReadInput did not return (keys left the editor waiting)")
		return ""
	}
}

// BenchmarkEdMentionRank: one keystroke of the @ picker over 20k paths (the
// name index cap). A growing query narrows the previous matches, so the
// typical key costs much less than the first one.
func BenchmarkEdMentionRank(b *testing.B) {
	var p edPaths
	var list []string
	for i := 0; i < 20000; i++ {
		list = append(list, "pkg"+itoa(i%97)+"/sub"+itoa(i%13)+"/file_"+itoa(i)+".go")
	}
	p.set(list)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.lastQ, p.lastM = "", nil
		p.match("f")
		p.match("fi")
		p.match("fil")
	}
}
