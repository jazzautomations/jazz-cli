//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// uiLines splits rendered output into visible lines (ANSI removed).
func uiLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, stripANSI(l))
	}
	return out
}

// uiAllASCII: no glyphs a Linux console font lacks (box drawing, symbols,
// arrows, ellipsis). Accented Portuguese letters are fine.
func uiAllASCII(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if r >= 0x2000 {
			t.Fatalf("%s: glyph %q in TERM=linux output: %q", what, r, s)
		}
	}
}

func TestUiWelcomeBoxFits86(t *testing.T) {
	in := WelcomeInfo{Version: "0.3.0", Model: "kilo/poolside/laguna-s-2.1:free", Dir: "~/jazz-smoke3", Mode: "auto"}
	out := Welcome(Theme{color: true}, 86, in)
	ls := uiLines(out)
	if !strings.HasPrefix(ls[0], "╭") || !strings.HasSuffix(ls[0], "╮") {
		t.Fatalf("top border: %q", ls[0])
	}
	w := len([]rune(ls[0]))
	if w > 85 {
		t.Fatalf("box %d columns wide in an 86-column terminal", w)
	}
	for _, l := range ls {
		if l == "" {
			continue
		}
		if n := len([]rune(l)); n != w {
			t.Errorf("ragged box line (%d vs %d): %q", n, w, l)
		}
	}
	vis := strings.Join(ls, "\n")
	for _, want := range []string{"✻ jazz", "v0.3.0", in.Model, in.Dir, "▸▸ auto", "/help", "esc", "@ menciona arquivo"} {
		if !strings.Contains(vis, want) {
			t.Errorf("welcome lacks %q:\n%s", want, vis)
		}
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("welcome ends with a blank line")
	}
	// ASCII fallback and narrow terminals
	a := Welcome(Theme{ascii: true}, 86, in)
	uiAllASCII(t, "welcome", a)
	if !strings.HasPrefix(a, "+--") {
		t.Errorf("ascii welcome: %q", a)
	}
	if n := strings.Count(Welcome(Theme{}, 30, in), "\n"); n != 2 {
		t.Errorf("narrow welcome should be one line, got %d lines", n)
	}
}

func TestUiMarkdownWrapsWithHangingIndent(t *testing.T) {
	m := &mdRenderer{t: Theme{color: true}, indent: "  "}
	m.begin(2, 30)
	text := "Este parágrafo é comprido o bastante pra quebrar em várias linhas no terminal.\n- um item de lista que também é bem comprido e quebra\nfim"
	var b strings.Builder
	for _, r := range text { // streamed one rune at a time
		b.WriteString(m.Feed(string(r)))
	}
	b.WriteString(m.Flush())
	ls := strings.Split(mcpUiVisible("● "+b.String()), "\n")
	for _, l := range ls {
		if n := len([]rune(l)); n > 30 {
			t.Errorf("line wider than 30: %d %q", n, l)
		}
	}
	if !strings.HasPrefix(ls[0], "● Este") || !strings.HasPrefix(ls[1], "  ") || strings.HasPrefix(ls[1], "   ") {
		t.Errorf("paragraph hang wrong: %q", ls)
	}
	bullet := -1
	for i, l := range ls {
		if strings.HasPrefix(l, "  • um item") {
			bullet = i
		}
	}
	if bullet < 0 || !strings.HasPrefix(ls[bullet+1], "    ") {
		t.Fatalf("list item continuation not under the text: %q", ls)
	}
	joined := strings.Join(strings.Fields(strings.Join(ls, " ")), " ")
	if !strings.Contains(joined, "várias linhas no terminal.") || !strings.Contains(joined, "também é bem comprido e quebra") {
		t.Errorf("words lost while wrapping: %q", joined)
	}
}

func TestUiFencedCodeFrame(t *testing.T) {
	got := mcpUiVisible(mcpUiMD(Theme{}, []string{"```go\nx := 1\n\ny := 2\n```\nok"}, true))
	want := "╭─ go\n  │ x := 1\n  │\n  │ y := 2\n  ╰── \n  ok"
	if got != want {
		t.Errorf("fence\n got %q\nwant %q", got, want)
	}
}

func TestUiQuoteAndNumberedList(t *testing.T) {
	got := mcpUiVisible(mcpUiMD(Theme{}, []string{"> citação\n1. um\n12) doze\n1.5 não é lista"}, true))
	want := "│ citação\n  1. um\n  12) doze\n  1.5 não é lista"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func uiTestTermUI(t *testing.T, th Theme) (*TermUI, func() string) {
	t.Helper()
	read := uiTestStdout(t)
	u := NewTermUI(&Config{}, nil)
	u.T = th
	u.md.t = th
	return u, read
}

func TestUiToolCallClaudeStyle(t *testing.T) {
	u, read := uiTestTermUI(t, Theme{color: true})
	u.ToolStart("c1", "bash", "go test ./...")
	u.ToolEnd("c1", "bash", ToolOutput{Text: "a\nb\nc\nd\ne\n[exit code 0]", Display: Display{Summary: "exit 0 · 5 linhas · 1s", Preview: []string{"a", "b", "c", "d"}, Lines: 5}}, time.Second)
	out := read()
	vis := mcpUiVisible(out)
	for _, want := range []string{"● Bash(go test ./...)", "  ⎿  exit 0 · 5 linhas · 1s", "     a\n     b\n     c\n", "… +2 linhas (ctrl+o pra expandir)"} {
		if !strings.Contains(vis, want) {
			t.Errorf("missing %q in:\n%s", want, vis)
		}
	}
	if strings.Contains(vis, "     d") {
		t.Error("preview must stop at 3 lines")
	}
	if !strings.Contains(out, "\x1b[1A\r\x1b[32m●") {
		t.Errorf("the call line is not repainted green on success: %q", out)
	}
	if !strings.Contains(out, "\x1b[1mBash\x1b[0m") {
		t.Errorf("tool name not bold: %q", out)
	}
	// ctrl+o / /expand prints the whole output
	u.ExpandLast()
	all := mcpUiVisible(read())
	if !strings.Contains(all, "saída completa") || !strings.Contains(all, "     e\n") || !strings.Contains(all, "[exit code 0]") {
		t.Errorf("expand: %s", all)
	}
	// an error repaints red
	u.ToolStart("c2", "read", "nope.go")
	u.ToolEnd("c2", "read", ToolOutput{IsErr: true, Text: "Cannot read", Display: Display{Summary: "não encontrado"}}, 0)
	if out := read(); !strings.Contains(out, "\x1b[1A\r\x1b[31m●") {
		t.Errorf("error not red: %q", out)
	}
}

func TestUiEditDiffAndTodos(t *testing.T) {
	u, read := uiTestTermUI(t, Theme{color: true, c256: true})
	u.ToolStart("e1", "edit", "ui.go")
	u.ToolEnd("e1", "edit", ToolOutput{Text: "ok", Display: Display{Summary: "+3 −1", Path: "ui.go",
		Diff: []editHunk{{Line: 10, Old: []string{"old"}, New: []string{"n1", "n2", "n3"}}}}}, 0)
	out := read()
	vis := mcpUiVisible(out)
	for _, want := range []string{"● Edit(ui.go)", "⎿  Atualizou ui.go com 3 adições e 1 remoção", "  10 - old", "  10 + n1", "  12 + n3"} {
		if !strings.Contains(vis, want) {
			t.Errorf("missing %q in:\n%s", want, vis)
		}
	}
	if !strings.Contains(out, "\x1b[48;5;52m") || !strings.Contains(out, "\x1b[48;5;22m") {
		t.Errorf("256-colour diff lacks the red/green tint: %q", out)
	}
	u.ToolStart("t1", "todo", "")
	u.ToolEnd("t1", "todo", ToolOutput{Text: "ok", Display: Display{Summary: "1/3 feitos", Todos: []Todo{
		{Content: "ler", Status: "completed"}, {Content: "editar", Status: "in_progress"}, {Content: "testar", Status: "pending"}}}}, 0)
	out = read()
	vis = mcpUiVisible(out)
	for _, want := range []string{"● Atualizou o plano", "⎿  1/3 feitos", "     ☒ ler", "     ◐ editar", "     ☐ testar"} {
		if !strings.Contains(vis, want) {
			t.Errorf("missing %q in:\n%s", want, vis)
		}
	}
	if !strings.Contains(out, "\x1b[9m") {
		t.Error("done item should be struck through")
	}
	// 16 colours: fg only, no background tint
	u2, read2 := uiTestTermUI(t, Theme{color: true})
	u2.ToolEnd("e2", "edit", ToolOutput{Display: Display{Path: "a", Diff: []editHunk{{Line: 1, Old: []string{"x"}, New: []string{"y"}}}}}, 0)
	if strings.Contains(read2(), "\x1b[4") {
		t.Error("16-colour diff must not paint backgrounds")
	}
}

func TestUiSpinnerLine(t *testing.T) {
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{}
	u.spinStart = time.Now()
	u.spinLabel = "pensando"
	u.reasonN = 4800
	l := u.spinLine(3)
	found := false
	for _, w := range uiSpinWords {
		if strings.Contains(l, w+"…") {
			found = true
		}
	}
	if !found || !strings.Contains(l, "(0s · ↓ 1.2k tokens · esc pra interromper)") || !strings.HasPrefix(l, "✻ ") {
		t.Errorf("spin line = %q", l)
	}
	u.spinLabel = "rodando"
	if l := u.spinLine(0); !strings.Contains(l, "Rodando…") {
		t.Errorf("tool spinner = %q", l)
	}
	u.T = Theme{ascii: true}
	uiAllASCII(t, "spinner", u.spinLine(1))
}

func TestUiFooter(t *testing.T) {
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true}
	u.SetMode("plan")
	u.Jobs(1)
	f := stripANSI(u.Footer("kilo/x:free", 1500, 10000, 1, 2))
	for _, want := range []string{"‖ plano", "kilo/x:free", "ctx 15%", "mcp 1/2", "jobs 1", "? atalhos"} {
		if !strings.Contains(f, want) {
			t.Errorf("footer %q lacks %q", f, want)
		}
	}
	u.SetMode("auto")
	if f := stripANSI(u.Footer("m", 0, 0, 0, 0)); !strings.HasPrefix(f, "  ▸▸ auto") || strings.Contains(f, "ctx") {
		t.Errorf("footer = %q", f)
	}
}

func TestUiInputBoxFrame(t *testing.T) {
	u, _ := uiTestUI(t)
	u.T = Theme{color: true}
	e := NewEditor(u)
	e.Footer = func() string { return "  rodapé" }
	lines, cl, cc := e.boxFrame([]rune("oi"), 2, 40)
	vis := make([]string, len(lines))
	for i, l := range lines {
		vis[i] = stripANSI(l)
	}
	want := []string{
		"╭" + strings.Repeat("─", 37) + "╮",
		"│ › oi" + strings.Repeat(" ", 32) + "│",
		"╰" + strings.Repeat("─", 37) + "╯",
		"  rodapé",
	}
	if strings.Join(vis, "\n") != strings.Join(want, "\n") {
		t.Fatalf("frame\n%s\nwant\n%s", strings.Join(vis, "\n"), strings.Join(want, "\n"))
	}
	if cl != 1 || cc != 6 {
		t.Errorf("cursor at line %d col %d, want 1,6", cl, cc)
	}
	// multi-line input grows the box; long lines wrap inside it
	lines, cl, cc = e.boxFrame([]rune("a\nb\n"+strings.Repeat("x", 40)), 3, 40)
	if len(lines) != 2+4+1 {
		t.Fatalf("grown box: %d lines %q", len(lines), lines)
	}
	if cl != 2 || cc != 5 { // after "b": row 1 of the text, column 1
		t.Errorf("cursor %d,%d", cl, cc)
	}
	if !strings.HasPrefix(stripANSI(lines[2]), "│   b") {
		t.Errorf("continuation row: %q", stripANSI(lines[2]))
	}
	// a shell escape tints the box
	if l, _, _ := e.boxFrame([]rune("!ls"), 3, 40); !strings.Contains(stripANSI(l[1]), "│ ! !ls") {
		t.Errorf("bash mode: %q", stripANSI(l[1]))
	}
	// ASCII
	u.T = Theme{ascii: true}
	l, _, _ := e.boxFrame([]rune("oi"), 2, 40)
	uiAllASCII(t, "box", strings.Join(l, "\n"))
}

func TestUiInputSlashMenuAndEcho(t *testing.T) {
	u, keys := uiTestUI(t)
	read := uiTestStdout(t)
	u.T = Theme{}
	e := NewEditor(u)
	e.Commands = func() []EdCommand {
		return []EdCommand{{"/help", "ajuda"}, {"/model", "troca o modelo"}, {"/models", "lista modelos"}}
	}
	uiSendRunes(keys, "/mo")
	keys <- Key{Code: KDown}
	keys <- Key{Code: KEnter}
	got, err := e.ReadInput()
	if err != nil || got != "/models" {
		t.Fatalf("menu pick = %q, %v", got, err)
	}
	out := read()
	if !strings.Contains(out, "▸ /model ") || !strings.Contains(out, "troca o modelo") || !strings.Contains(out, "▸ /models") {
		t.Errorf("menu not drawn: %q", out)
	}
	if !strings.HasSuffix(out, "› /models\n") {
		t.Errorf("echo after submit: %q", out[max(0, len(out)-60):])
	}
	// Tab completes the selection; Esc closes the menu
	uiSendRunes(keys, "/he")
	keys <- Key{Code: KTab}
	uiSendRunes(keys, "x\n")
	if got, _ := e.ReadInput(); got != "/help x" {
		t.Errorf("tab = %q", got)
	}
	// "?" on an empty line shows the shortcuts instead of typing
	keys <- Key{Code: KRune, R: '?'}
	keys <- Key{Code: KRune, R: '?'}
	uiSendRunes(keys, "ok?\n")
	if got, _ := e.ReadInput(); got != "ok?" {
		t.Errorf("? handling = %q", got)
	}
	if !strings.Contains(read(), "busca no histórico") {
		t.Error("shortcut list not shown")
	}
}

func TestUiApproveMenuKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []Key
		want Approval
	}{
		{"enter is yes", []Key{{Code: KEnter}}, Allow},
		{"s is yes", []Key{{Code: KRune, R: 's'}}, Allow},
		{"a is always", []Key{{Code: KRune, R: 'a'}}, AllowAlways},
		{"arrows are ignored", []Key{{Code: KDown}, {Code: KUp}, {Code: KRune, R: 'n'}}, Deny},
		{"e is no+feedback", []Key{{Code: KRune, R: 'e'}, {Code: KRune, R: 'x'}, {Code: KEnter}}, Deny},
		{"digit 2", []Key{{Code: KRune, R: '2'}}, AllowAlways},
		{"esc", []Key{{Code: KEsc}}, Deny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, keys := uiTestUI(t)
			read := uiTestStdout(t)
			u.T = Theme{}
			res := uiApproveAsync(u, context.Background(), "bash", "npm test", "")
			time.Sleep(uiApproveGrace + 50*time.Millisecond)
			for _, k := range c.keys {
				keys <- k
			}
			r := uiWaitApprove(t, res, 2*time.Second)
			if r.ap != c.want {
				t.Fatalf("approve = %v want %v", r.ap, c.want)
			}
			out := read()
			for _, want := range []string{"│ Bash  npm test", "? Rodar este comando?", "enter sim", "a sempre", "n não", "e instruir"} {
				if !strings.Contains(out, want) {
					t.Errorf("approval lacks %q: %q", want, out)
				}
			}
		})
	}
}

func TestUiASCIIToolAndApprove(t *testing.T) {
	mcpUiClearThemeEnv(t)
	t.Setenv("TERM", "linux")
	th := DetectTheme()
	u, read := uiTestTermUI(t, th)
	u.ToolStart("1", "edit", "a.go")
	u.ToolEnd("1", "edit", ToolOutput{Display: Display{Path: "a.go", Diff: []editHunk{{Line: 1, Old: []string{"x"}, New: []string{"y"}}}}}, 0)
	u.Approve(context.Background(), "bash", "rm -rf build", "apaga")
	u.TurnEnd(TurnStats{Dur: time.Second, Usage: Usage{In: 10, Out: 5}, Win: 100, Ctx: 5})
	out := read()
	uiAllASCII(t, "tool/approve/turn", out)
	if !strings.Contains(stripANSI(out), "Atualizou a.go com 1 adição e 1 remoção") {
		t.Errorf("ascii edit header: %q", out)
	}
}
