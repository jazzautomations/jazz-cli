package main

// ui_pager.go: a resumed conversation longer than the screen is shown a page
// at a time, like `less` but inline (no alternate screen: what was read stays
// in the scrollback). Space (or PgDn, f) shows the next page, Enter (↓, j)
// one more line, q (Esc) jumps to the end: the last screenful is printed,
// with the number of lines skipped, so the newest part is always on screen.
// Without a terminal, or with `--full`, everything is written at once.

import (
	"fmt"
	"strings"
)

// pfReplayFull is `jazz resume --full`: the whole transcript, no pager.
var pfReplayFull bool

// pgState is the pager's pure state (tested without a terminal).
type pgState struct {
	lines []string
	shown int // lines already written
	page  int // rows per page
}

// next returns the lines a key shows and whether the pager is done.
// skipped > 0 when q jumped over lines to reach the end.
func (p *pgState) next(k Key) (out []string, skipped int, done bool) {
	n := 0
	switch {
	case k.Code == KPgDn || k.Code == KRune && (k.R == ' ' || k.R == 'f'):
		n = p.page - 1
	case k.Code == KEnter || k.Code == KDown || k.Code == KRune && k.R == 'j':
		n = 1
	case k.Code == KEsc || k.Code == KCtrlC || k.Code == KCtrlD || k.Code == KEnd || k.Code == KRune && (k.R == 'q' || k.R == 'G'):
		tail := max(p.page/2, 3) // the end, with room under it for the notice and the input box
		start := max(len(p.lines)-tail, p.shown)
		skipped = start - p.shown
		out = p.lines[start:]
		p.shown = len(p.lines)
		return out, skipped, true
	default:
		return nil, 0, false
	}
	end := min(p.shown+n, len(p.lines))
	out = p.lines[p.shown:end]
	p.shown = end
	return out, 0, p.shown >= len(p.lines)
}

// pgPrompt is the status row under a page.
func pgPrompt(t Theme, shown, total int) string {
	g := t.G()
	pct := shown * 100 / max(total, 1)
	return t.fg(cGray, 90) + t.sgr("7") + fmt.Sprintf(" %d%% %s espaço próxima página %s enter +1 linha %s q pula pro fim ", pct, g.dot, g.dot, g.dot) + t.sgr("0")
}

// pageOut writes s; when it is taller than the screen and keys are there,
// one page at a time. It reports whether q skipped lines. The caller must
// not hold u.mu.
func (u *TermUI) pageOut(s string) (skipped bool) {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	page := max(termHeight()-2, 5)
	if u.keys == nil || !u.lv.on || pfReplayFull || len(lines) <= page {
		u.print(s)
		return false
	}
	p := &pgState{lines: lines, page: page}
	u.print(strings.Join(lines[:page], "\n") + "\n")
	p.shown = page
	u.print(pgPrompt(u.T, p.shown, len(p.lines)))
	for k := range u.keys.Keys {
		out, n, done := p.next(k)
		if out == nil && !done {
			continue // not a pager key
		}
		var b strings.Builder
		b.WriteString("\r\x1b[K")
		if n > 0 {
			skipped = true
			fmt.Fprintf(&b, "  %s%s %d %s puladas (/expand mostra)%s\n", u.T.fg(cGray, 90), u.T.G().ell, n, uiPl(n, "linha", "linhas"), u.T.sgr("0"))
		}
		if len(out) > 0 {
			b.WriteString(strings.Join(out, "\n") + "\n")
		}
		if !done {
			b.WriteString(pgPrompt(u.T, p.shown, len(p.lines)))
		}
		u.print(b.String())
		if done {
			return skipped
		}
	}
	u.print("\r\x1b[K" + strings.Join(p.lines[p.shown:], "\n") + "\n") // keys closed: the rest at once
	return skipped
}
