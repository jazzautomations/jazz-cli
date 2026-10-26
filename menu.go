package main

// menu.go: the one widget every question uses (approvals, yes/no questions,
// folder trust, ask_user, the session pickers). Claude Code style: a rounded
// box with the subject (a command, a diff, a reason), the question, and the
// options as a vertical list. ↑↓ (or j/k) move the highlight, Enter picks it,
// a digit or the option's letter picks directly, Esc cancels. Pickers (many
// rows) filter as you type and scroll. Nothing ever needs a typed word.
//
// Drawing follows the live region's invariant (ui_live.go): while the menu is
// on screen the cursor is parked at column 0 of its first row, so a redraw is
// "\r\x1b[J" plus the rows, also after a resize that rewraps lines. When the
// menu closes it is erased and the caller prints a one-line summary.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// mnItem is one option.
type mnItem struct {
	Label string // plain text (callers sanitize what came from outside)
	Hint  string // dim text after the label (pickers: date, size, model)
	Sub   string // dim second line under the label (pickers: last reply)
	Key   rune   // one-key shortcut besides the number (0 = none)
	Keys  string // more shortcut runes for the same option
	Find  string // text the filter matches (default: Label + Hint + Sub)
}

// mnSpec describes a menu.
type mnSpec struct {
	Tag    string        // label in the top border ("Bash", "sessões")
	Body   []string      // styled rows above the question (command, diff, reason)
	Above  string        // styled line printed above the box (queued text)
	Title  string        // the question (bold)
	Items  []mnItem      //
	Sel    int           // option highlighted at start
	Filter bool          // pickers: letters filter the list (no letter shortcuts, no digits)
	Rows   int           // options visible at once (0 = all that fit)
	Color  string        // border SGR ("" = accent)
	Keys   string        // hint line under the box ("" = default)
	Alt    map[rune]int  // extra shortcuts that return a code (e.g. n = plain "no")
	View   func() string // v: printed above the menu for good (whole command, diff)
	Guard  bool          // approvals: grace period and the typing-pause rule
	Since  time.Time     // last key the user typed before the menu opened (Guard)
	Text   bool          // ask_user: a letter that is no shortcut starts a free answer (mnText)
	typed  rune          // that letter
}

const (
	mnCancel = -1 // Esc, Ctrl+C, EOF, ctx done
	mnText   = -3 // Text: the user started typing an answer (sp.typed)
	mnView   = -9 // internal: v
)

// mnState is the menu's pure state (no terminal): tests drive it with keys.
type mnState struct {
	sp     *mnSpec
	filter []rune
	vis    []int // indexes into sp.Items that match the filter
	sel    int   // position in vis
	top    int   // first visible position (scroll)
}

func newMenuState(sp *mnSpec) *mnState {
	m := &mnState{sp: sp}
	m.refilter()
	if sp.Sel > 0 && sp.Sel < len(sp.Items) {
		m.sel = sp.Sel
	}
	return m
}

func (m *mnState) refilter() {
	m.vis = m.vis[:0]
	words := strings.Fields(fzFold(string(m.filter)))
	for i, it := range m.sp.Items {
		hay := it.Find
		if hay == "" {
			hay = it.Label + " " + it.Hint + " " + it.Sub
		}
		hay = fzFold(hay)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			m.vis = append(m.vis, i)
		}
	}
	m.sel, m.top = 0, 0
}

// key applies one key. It returns the picked item index (≥ 0), mnCancel, an
// Alt code, mnView, or ok=false when the menu stays open.
func (m *mnState) key(k Key) (res int, done bool) {
	n := len(m.vis)
	move := func(d int) {
		if n == 0 {
			return
		}
		m.sel = ((m.sel+d)%n + n) % n
	}
	switch k.Code {
	case KUp, KShiftTab:
		move(-1)
	case KDown, KTab:
		move(1)
	case KHome:
		m.sel = 0
	case KEnd:
		m.sel = max(n-1, 0)
	case KPgUp:
		m.sel = max(m.sel-m.page(), 0)
	case KPgDn:
		m.sel = min(m.sel+m.page(), max(n-1, 0))
	case KEnter:
		if n > 0 {
			return m.vis[m.sel], true
		}
	case KEsc, KCtrlC, KCtrlD:
		if m.sp.Filter && len(m.filter) > 0 && k.Code == KEsc {
			m.filter = nil // first Esc clears the filter
			m.refilter()
			return 0, false
		}
		return mnCancel, true
	case KBackspace:
		if m.sp.Filter && len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.refilter()
		}
	case KCtrlU:
		if m.sp.Filter {
			m.filter = nil
			m.refilter()
		}
	case KPaste:
		if m.sp.Filter {
			m.filter = append(m.filter, []rune(strings.Join(strings.Fields(sanitizeTerm(k.Text)), " "))...)
			m.refilter()
		}
	case KRune:
		if m.sp.Filter {
			m.filter = append(m.filter, k.R)
			m.refilter()
			return 0, false
		}
		r := unicode.ToLower(k.R)
		switch {
		case r == 'k' && !m.sp.Text:
			move(-1)
			return 0, false
		case r == 'j' && !m.sp.Text:
			move(1)
			return 0, false
		case r >= '1' && r <= '9':
			if i := int(r - '1'); i < n {
				return m.vis[i], true
			}
			return 0, false
		case r == 'v' && m.sp.View != nil:
			return mnView, true
		}
		for _, i := range m.vis {
			it := m.sp.Items[i]
			if it.Key != 0 && unicode.ToLower(it.Key) == r || strings.ContainsRune(strings.ToLower(it.Keys), r) {
				return i, true
			}
		}
		if c, ok := m.sp.Alt[r]; ok {
			return c, true
		}
		if m.sp.Text && unicode.IsPrint(k.R) {
			m.sp.typed = k.R
			return mnText, true
		}
	}
	return 0, false
}

// isAnswer reports whether k would close the menu (the typing-pause rule
// only holds back answers; moving the highlight is always fine).
func (m *mnState) isAnswer(k Key) bool {
	switch k.Code {
	case KEnter:
		return true
	case KRune:
		if m.sp.Filter {
			return false
		}
		r := unicode.ToLower(k.R)
		if r == 'j' || r == 'k' {
			return false
		}
		return true
	}
	return false
}

func (m *mnState) page() int {
	if m.sp.Rows > 0 {
		return m.sp.Rows
	}
	return 10
}

// mnClip cuts a styled string to w cells (wide runes count 2).
func mnClip(s string, w int) string {
	if uiStrW(s) <= w {
		return s
	}
	var b strings.Builder
	vis := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := uiEscEnd(s, i)
			if i+1 < len(s) && s[i+1] == '[' {
				b.WriteString(s[i:j])
			}
			i = j
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		rw := uiRuneW(r)
		if r < 0x20 || r == 0x7f {
			rw = 0
		}
		if vis+rw > w-1 {
			b.WriteString("…\x1b[0m")
			return b.String()
		}
		b.WriteString(s[i : i+n])
		vis += rw
		i += n
	}
	return b.String()
}

// mnWrap wraps plain text at spaces to w cells; a word longer than w is cut.
func mnWrap(s string, w int) []string {
	w = max(w, 8)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		for _, l := range uiWrapWords(para, w) {
			for uiStrW(l) > w {
				r := []rune(l)
				cut, cw := 0, 0
				for cut < len(r) && cw+uiRuneW(r[cut]) <= w {
					cw += uiRuneW(r[cut])
					cut++
				}
				cut = max(cut, 1)
				out = append(out, string(r[:cut]))
				l = string(r[cut:])
			}
			out = append(out, l)
		}
	}
	return out
}

// mnBoxWidth is the width of a menu box on a w-column line.
func mnBoxWidth(w int) int { return max(min(w, 100), 24) }

// render draws the menu at width w (columns usable) and at most h rows.
func (m *mnState) render(t Theme, w, h int) []string {
	g := t.G()
	sp := m.sp
	reset, dim, bold := t.sgr("0"), t.fg(cGray, 90), t.sgr("1")
	acc := t.fg(cAccent, 36)
	border := sp.Color
	if border == "" {
		border = acc
	}
	bw := mnBoxWidth(w)
	inner := bw - 4
	row := func(s string) string {
		s = mnClip(s, inner)
		pad := max(inner-uiStrW(s), 0)
		return border + g.v + reset + " " + s + reset + strings.Repeat(" ", pad) + " " + border + g.v + reset
	}
	var top []string
	if sp.Above != "" {
		top = append(top, mnClip(sp.Above, w))
	}
	if sp.Tag != "" {
		tag := " " + bold + sp.Tag + reset + border + " "
		top = append(top, border+g.tl+g.h+tag+strings.Repeat(g.h, max(bw-4-uiStrW(tag), 0))+g.h+g.tr+reset)
	} else {
		top = append(top, border+g.tl+strings.Repeat(g.h, bw-2)+g.tr+reset)
	}
	var body []string
	for _, l := range sp.Body {
		body = append(body, row(l))
	}
	if len(body) > 0 && sp.Title != "" {
		body = append(body, row(""))
	}
	if sp.Title != "" {
		for _, l := range mnWrap(stripANSI(sp.Title), inner) {
			body = append(body, row(bold+l))
		}
	}
	if sp.Filter {
		f := dim + "filtrar: " + reset + string(m.filter) + acc + "▏" + reset
		if t.ascii {
			f = dim + "filtrar: " + reset + string(m.filter) + "_"
		}
		body = append(body, row(f))
	}
	keys := sp.Keys
	if keys == "" {
		keys = g.up + g.down + " escolhe " + g.dot + " enter confirma " + g.dot + " esc cancela"
		if sp.Filter {
			keys = g.up + g.down + " escolhe " + g.dot + " digite pra filtrar " + g.dot + " enter abre " + g.dot + " esc cancela"
		}
	}
	bottom := []string{border + g.bl + strings.Repeat(g.h, bw-2) + g.br + reset, mnClip("  "+dim+keys+reset, w)}
	// options: as many as fit (the box never scrolls the screen away)
	per := 1
	for _, i := range m.vis {
		if sp.Items[i].Sub != "" {
			per = 2
			break
		}
	}
	room := h - len(top) - len(body) - len(bottom)
	if room < 3*per && len(body) > 0 { // tall body: shorten it, keep the options
		keep := max(h-len(top)-len(bottom)-3*per-1, 1)
		if keep < len(body) {
			body = append(body[:keep], row(dim+g.ell+" (v ver tudo)"))
		}
		room = h - len(top) - len(body) - len(bottom)
	}
	rows := max(room/per, 1)
	if sp.Rows > 0 {
		rows = min(rows, sp.Rows)
	}
	more := len(m.vis) > rows
	if more {
		rows = max(rows-1, 1) // one row for the "↑ n · ↓ n" line
	}
	if m.sel < m.top {
		m.top = m.sel
	}
	if m.sel >= m.top+rows {
		m.top = m.sel - rows + 1
	}
	m.top = max(min(m.top, len(m.vis)-rows), 0)
	var opts []string
	if len(m.vis) == 0 {
		opts = append(opts, row(dim+"nada encontrado"))
	}
	numbered := !sp.Filter && len(sp.Items) <= 9
	for p := m.top; p < len(m.vis) && p < m.top+rows; p++ {
		it := sp.Items[m.vis[p]]
		ptr, lab := "  ", sanitizeTerm(it.Label)
		if numbered {
			lab = fmt.Sprintf("%d. %s", p+1, lab)
		}
		if p == m.sel {
			ptr = acc + bold + g.ptr + " "
			lab = acc + bold + lab + reset
		}
		l := ptr + reset + lab
		if it.Hint != "" {
			l += "  " + dim + sanitizeTerm(it.Hint) + reset
		}
		opts = append(opts, row(l))
		if it.Sub != "" {
			opts = append(opts, row("    "+dim+sanitizeTerm(it.Sub)+reset))
		}
	}
	if more {
		up, down := m.top, len(m.vis)-m.top-rows
		opts = append(opts, row(dim+fmt.Sprintf("%s %d %s %s %d %s %d de %d", g.up, up, g.dot, g.down, max(down, 0), g.dot, m.sel+1, len(m.vis))+reset))
	}
	out := append(top, body...)
	out = append(out, opts...)
	return append(out, bottom...)
}

// mnTermIO is what Menu needs from a terminal: keys, a writer, the size.
type mnTermIO struct {
	keys   <-chan Key
	write  func(string)
	size   func() (w, h int)
	resize <-chan struct{}
}

// mnRun shows the menu and waits for an answer. It returns the item index,
// mnCancel, or an Alt code. The menu is erased before it returns.
func mnRun(ctx context.Context, t Theme, io mnTermIO, sp *mnSpec) int {
	m := newMenuState(sp)
	drawn := 0
	draw := func() {
		w, h := io.size()
		lines := m.render(t, w-1, max(h-1, 6))
		var b strings.Builder
		if drawn > 0 {
			b.WriteString("\r\x1b[J")
		} else {
			b.WriteString("\x1b[?25l")
		}
		b.WriteString(strings.Join(lines, "\n"))
		fmt.Fprintf(&b, "\n\x1b[%dA", len(lines))
		drawn = len(lines)
		io.write(b.String())
	}
	erase := func() { io.write("\r\x1b[J\x1b[?25h") }
	draw()
	since := time.Now()
	prev := sp.Since
	for {
		var k Key
		var ok bool
		select {
		case k, ok = <-io.keys:
			if ok && k.Code == KCtrlD {
				ok = false
			}
		case <-io.resize:
			draw()
			continue
		case <-ctx.Done():
		}
		if !ok {
			erase()
			return mnCancel
		}
		now := time.Now()
		if now.Sub(since) < uiApproveGrace {
			if k.Code == KRune || k.Code == KPaste {
				prev = now
			}
			continue // typed before the menu could be read (bug 11)
		}
		if sp.Guard && m.isAnswer(k) && now.Sub(prev) < uiApproveIdle {
			if k.Code == KRune || k.Code == KPaste {
				prev = now
			}
			continue // still typing: not an answer
		}
		res, done := m.key(k)
		if !done {
			if k.Code == KRune && !sp.Filter && m.isAnswer(k) {
				prev = now // a letter that is not an option: the user is typing
			}
			draw()
			continue
		}
		if res == mnView {
			erase()
			drawn = 0
			io.write(sp.View())
			sp.View = nil
			draw()
			continue
		}
		erase()
		return res
	}
}

// Menu shows sp in the terminal and returns the answer (see mnRun). Without
// a terminal it returns mnCancel at once.
func (u *TermUI) Menu(ctx context.Context, sp *mnSpec) int {
	if ctx == nil {
		ctx = context.Background()
	}
	u.StopThinking()
	if u.keys == nil {
		return mnCancel
	}
	keys, release := u.grabKeys(ctx)
	defer release()
	return u.menuKeys(ctx, keys, sp)
}

// menuKeys is Menu on keys the caller already grabbed (a follow-up line,
// such as "o que fazer em vez disso?", reads the same keys without a gap).
func (u *TermUI) menuKeys(ctx context.Context, keys <-chan Key, sp *mnSpec) int {
	u.mu.Lock()
	u.endText()
	u.gap()
	u.mu.Unlock()
	io := mnTermIO{
		keys:   keys,
		write:  func(s string) { u.mu.Lock(); uiWrite(s); u.mu.Unlock() },
		size:   func() (int, int) { return termWidth(), termHeight() },
		resize: u.menuResize,
	}
	return mnRun(ctx, u.T, io, sp)
}

// menuDone prints the one-line summary that replaces a closed menu.
func (u *TermUI) menuDone(ok bool, text string) {
	t, g := u.T, u.T.G()
	col := t.fg(cGreen, 32)
	if !ok {
		col = t.fg(cRed, 31)
	}
	u.print("  " + col + g.tool + t.sgr("0") + " " + mnClip(text, termWidth()-5) + t.sgr("0") + "\n")
}

// Choose is a short question with options (yes/no and friends). def is the
// option highlighted at start; it returns the index or mnCancel.
func (u *TermUI) Choose(question string, items []mnItem, def int) int {
	i := u.Menu(context.Background(), &mnSpec{Title: question, Items: items, Sel: def})
	switch {
	case i >= 0 && i < len(items):
		u.menuDone(true, stripANSI(question)+"  "+u.T.fg(cGray, 90)+items[i].Label)
	default:
		u.menuDone(false, stripANSI(question)+"  "+u.T.fg(cGray, 90)+"cancelado")
	}
	return i
}
