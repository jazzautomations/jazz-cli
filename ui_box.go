package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// The boxed input editor (Editor.ReadInput): a rounded, full-width box around
// the text, with a footer, the slash-command menu or the shortcut list below.
// Every redraw is one write that starts at the top of the previous frame, so
// nothing flickers over a slow SSH link.

// edMaxRows is how many text rows the box shows before it scrolls.
const edMaxRows = 10

// edMenuMax is how many slash commands the menu shows at once.
const edMenuMax = 6

var (
	uiWinchOnce sync.Once
	uiWinchC    chan os.Signal
)

// uiWinch delivers terminal resizes (SIGWINCH) to the editor.
func uiWinch() <-chan os.Signal {
	uiWinchOnce.Do(func() {
		uiWinchC = make(chan os.Signal, 1)
		uiWinchNotify(uiWinchC)
	})
	return uiWinchC
}

// edLayout splits the display runes of buf into rows of at most cw cells and
// finds the cursor (row, col) for pos. Wide characters (CJK, emoji) take two
// cells and never straddle a row end.
func edLayout(disp []rune, pos, cw int) (rows [][]rune, crow, ccol int) {
	rows = [][]rune{{}}
	r, c := 0, 0
	for i, ch := range disp {
		rw := uiRuneW(ch)
		if ch != '\n' && c > 0 && c+rw > cw {
			rows = append(rows, []rune{})
			r, c = r+1, 0
		}
		if i == pos {
			crow, ccol = r, c
		}
		if ch == '\n' {
			rows = append(rows, []rune{})
			r, c = r+1, 0
			continue
		}
		rows[r] = append(rows[r], ch)
		c += rw
	}
	if pos >= len(disp) {
		crow, ccol = r, c
		if c == cw { // cursor after a full row: it sits at the start of the next one
			rows = append(rows, []rune{})
			crow, ccol = r+1, 0
		}
	}
	return rows, crow, ccol
}

// edDisplay maps the buffer to printable runes (newline kept for layout).
func edDisplay(buf []rune) []rune {
	disp := make([]rune, len(buf))
	for i, r := range buf {
		switch {
		case r == '\n':
			disp[i] = '\n'
		case r == '\t':
			disp[i] = ' '
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			disp[i] = '?'
		default:
			disp[i] = r
		}
	}
	return disp
}

// boxFrame renders the whole frame and where the cursor goes inside it.
func (e *Editor) boxFrame(buf []rune, pos, w int) (lines []string, curLine, curCol int) {
	t, g := e.ui.T, e.ui.T.G()
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	bw := w - 1
	cw := bw - 6
	border := dim
	mark := t.fg(cAccent, 36) + t.sgr("1") + g.prompt + reset
	if len(buf) > 0 && buf[0] == '!' {
		border = t.fg(cGold, 33)
		mark = t.fg(cGold, 33) + t.sgr("1") + "!" + reset
	}
	rows, crow, ccol := edLayout(edDisplay(buf), pos, cw)
	top := 0
	if len(rows) > edMaxRows {
		top = min(max(crow-edMaxRows+1, 0), len(rows)-edMaxRows)
	}
	lines = append(lines, border+g.tl+strings.Repeat(g.h, bw-2)+g.tr+reset)
	for i := top; i < len(rows) && i < top+edMaxRows; i++ {
		pre := "  "
		if i == 0 {
			pre = mark + " "
		}
		text := string(rows[i])
		pad := max(cw-uiRunesW(rows[i]), 0)
		lines = append(lines, border+g.v+reset+" "+pre+text+strings.Repeat(" ", pad)+" "+border+g.v+reset)
	}
	lines = append(lines, border+g.bl+strings.Repeat(g.h, bw-2)+g.br+reset)
	curLine, curCol = 1+crow-top, 4+ccol
	switch {
	case len(e.menuItems(true, buf)) > 0:
		lines = append(lines, e.menuLines(buf, w)...)
	case e.help:
		lines = append(lines, edHelpLines(t, w)...)
	case e.note != "":
		lines = append(lines, "  "+t.fg(cGold, 33)+e.note+reset)
	case e.Footer != nil:
		lines = append(lines, clipVisible(e.Footer(), w-1))
	}
	return lines, curLine, curCol
}

// drawBox redraws the boxed input in place (one write). The caret is drawn
// (reverse video) and the real cursor is hidden and parked at column 0 of the
// frame's first row: after a resize the terminal may rewrap the frame (VTE
// does, xterm does not), and only a cursor at the start of the frame's
// content lands on a known spot either way, so "\r\x1b[J" erases the old
// frame without leaving pieces of it behind.
func (e *Editor) drawBox(buf []rune, pos int) {
	w := termWidth()
	lines, cl, cc := e.boxFrame(buf, pos, w)
	if cl >= 0 && cl < len(lines) {
		lines[cl] = uiCaretAt(lines[cl], cc)
	}
	var b strings.Builder
	b.WriteString("\x1b[?25l\r")
	for i, l := range lines {
		b.WriteString(l + "\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J\r")
	if up := len(lines) - 1; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	e.frame = edFrame{drawn: true}
	uiWrite(b.String())
}

// uiCaretAt draws a block caret over the cell at column col of a styled line.
func uiCaretAt(line string, col int) string {
	vis := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			i = uiEscEnd(line, i)
			continue
		}
		r, n := utf8.DecodeRuneInString(line[i:])
		if vis == col {
			return line[:i] + "\x1b[7m" + string(r) + "\x1b[27m" + line[i+n:]
		}
		vis += uiRuneW(r)
		if vis > col {
			return line
		}
		i += n
	}
	return line + "\x1b[7m \x1b[27m"
}

// eraseFrame removes the boxed input from the screen; the cursor ends where
// the frame started, at column 0, visible again.
func (e *Editor) eraseFrame() {
	if !e.frame.drawn {
		return
	}
	s := "\r\x1b[J\x1b[?25h"
	if e.frame.up > 0 {
		s = fmt.Sprintf("\x1b[%dA", e.frame.up) + s
	}
	uiWrite(s)
	e.frame = edFrame{}
}

// echo is how a submitted input stays in the transcript.
func (e *Editor) echo(s string) string {
	t := e.ui.T
	mark := t.fg(cGray, 90) + t.G().prompt + t.sgr("0") + " "
	if strings.HasPrefix(s, "!") {
		mark = t.fg(cGold, 33) + "!" + t.sgr("0") + " "
		s = strings.TrimPrefix(s, "!")
	}
	// wrapped to the terminal with a hanging indent, like the answers
	w := max(termWidth()-3, 20)
	var lines []string
	for _, l := range strings.Split(sanitizeTerm(strings.ReplaceAll(s, "\t", " ")), "\n") {
		lines = append(lines, uiWrapWords(l, w)...)
	}
	return mark + strings.Join(lines, "\n  ") + "\n"
}

// menuItems are the slash commands matching the buffer ("/" + letters, no space).
func (e *Editor) menuItems(boxed bool, buf []rune) []EdCommand {
	if !boxed || e.Commands == nil || e.menuOff || len(buf) == 0 || buf[0] != '/' {
		return nil
	}
	s := string(buf)
	if strings.ContainsAny(s, " \n\t") {
		return nil
	}
	var out []EdCommand
	for _, c := range e.Commands() {
		if strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(s)) {
			out = append(out, c)
		}
	}
	if len(out) == 1 && out[0].Name == s {
		return nil // already complete: show the footer again
	}
	return out
}

func (e *Editor) menuLines(buf []rune, w int) []string {
	t, g := e.ui.T, e.ui.T.G()
	items := e.menuItems(true, buf)
	sel := min(e.menuSel, len(items)-1)
	off := 0
	if sel >= edMenuMax {
		off = sel - edMenuMax + 1
	}
	nameW := 0
	for _, it := range items {
		nameW = max(nameW, utf8.RuneCountInString(it.Name))
	}
	nameW = min(nameW, 16)
	var out []string
	for i := off; i < len(items) && i < off+edMenuMax; i++ {
		it := items[i]
		name := it.Name + strings.Repeat(" ", max(nameW-utf8.RuneCountInString(it.Name), 0))
		var l string
		if i == sel {
			l = "  " + t.fg(cAccent, 36) + t.sgr("1") + g.ptr + " " + name + t.sgr("0") + "  " + sanitizeTerm(it.Help)
		} else {
			l = "    " + name + "  " + t.fg(cGray, 90) + sanitizeTerm(it.Help)
		}
		out = append(out, clipVisible(l+t.sgr("0"), w-1))
	}
	if rest := len(items) - (off + edMenuMax); rest > 0 {
		out = append(out, "    "+t.fg(cGray, 90)+fmt.Sprintf("%s +%d", g.ell, rest)+t.sgr("0"))
	}
	return out
}

// edHelpLines is the "?" shortcut list, two columns when they fit.
func edHelpLines(t Theme, w int) []string {
	items := [][2]string{
		{"!", "roda no shell"}, {"@", "menciona arquivo"},
		{"/", "comandos"}, {"esc", "interrompe ou limpa"},
		{"ctrl+r", "busca no histórico"}, {"ctrl+g", "abre no editor"},
		{"\\ + enter", "quebra linha"}, {"shift+tab", "troca o modo"},
		{"ctrl+o", "expande a última saída"}, {"ctrl+c 2×", "sai"},
	}
	if t.ascii {
		items[9][0] = "ctrl+c 2x"
	}
	cell := func(it [2]string) string {
		return t.sgr("1") + it[0] + t.sgr("0") + " " + t.fg(cGray, 90) + it[1] + t.sgr("0")
	}
	colW := 36
	var out []string
	if w-2 >= 2*colW {
		for i := 0; i < len(items); i += 2 {
			a := cell(items[i])
			l := "  " + a + strings.Repeat(" ", max(colW-uiVisLen(a), 1))
			if i+1 < len(items) {
				l += cell(items[i+1])
			}
			out = append(out, clipVisible(l, w-1))
		}
		return out
	}
	for _, it := range items {
		out = append(out, clipVisible("  "+cell(it), w-1))
	}
	return out
}
