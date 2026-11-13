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
	// Pending (Guard): the unfinished line the user was typing when the box
	// opened; what is typed in the box continues it (shown as one line).
	Pending string
	// TypedHint (Guard): what happens to the line being typed ("" = Enter
	// sends it to the agent, as in a turn). Set, the text ended with Enter
	// stays listed above the box ("no editor").
	TypedHint string
	// Settle (Guard): an option's letter or digit answers only when no other
	// key follows it within uiApproveIdle; otherwise it was the first letter
	// of a message ("sim oi" must not pick "Sim"). For questions where a
	// stray key costs a lot (the startup trust question).
	Settle bool
	Text   bool // ask_user: a letter that is no shortcut starts a free answer (mnText)
	typed  rune // that letter

	// Typed (Guard): keys the user typed as text while the menu was open —
	// letters in the grace/typing window, letters that are no option, and
	// everything after them up to the Enter that ends the line. They are
	// never answers: the caller hands them back to the steering buffer.
	Typed []Key
	// aborted: the menu closed on Ctrl+C or Ctrl+D (stop the whole turn),
	// not on Esc (decline this one).
	aborted bool
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
		if k.Code != KEsc {
			m.sp.aborted = true
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

// isOption reports whether the rune key k picks something (a digit in range,
// an item's letter, an Alt code, v for View).
func (m *mnState) isOption(k Key) bool {
	if k.Code != KRune || m.sp.Filter {
		return false
	}
	r := unicode.ToLower(k.R)
	if r >= '1' && r <= '9' {
		return int(r-'1') < len(m.vis)
	}
	if r == 'v' && m.sp.View != nil {
		return true
	}
	for _, i := range m.vis {
		it := m.sp.Items[i]
		if it.Key != 0 && unicode.ToLower(it.Key) == r || strings.ContainsRune(strings.ToLower(it.Keys), r) {
			return true
		}
	}
	_, ok := m.sp.Alt[r]
	return ok
}

// typingOpen reports whether the user is in the middle of a line typed
// while the menu is open (Typed not yet ended by Enter).
func (sp *mnSpec) typingOpen() bool {
	return len(sp.Typed) > 0 && sp.Typed[len(sp.Typed)-1].Code != KEnter
}

// typingRow is the row above the box about typed text: the line being
// typed (the part typed before the box plus what was typed in it, still
// unsent), or, for a menu with TypedHint, the text that goes back to the
// editor. label "" = no row.
func (sp *mnSpec) typingRow(dot string) (label, line, hint string) {
	ended := false
	for _, k := range sp.Typed {
		if k.Code == KEnter {
			ended = true
			break
		}
	}
	open := sp.typingOpen()
	if open {
		hint = sp.typedHint() + " " + dot + " esc apaga"
	}
	switch {
	case !ended && (sp.Pending != "" || open):
		// the line the box interrupted, still unfinished: shown as one line
		line = string(uiApplyTyped([]rune(sp.Pending), sp.Typed))
		if line == "" {
			return "", "", ""
		}
		return "digitando", line, hint
	case open:
		return "digitando", sp.typedLine(), hint
	case len(sp.Typed) > 0 && sp.TypedHint != "":
		// ended with Enter: kept for the editor, not sent
		return "no editor", strings.TrimSpace(string(uiApplyTyped(nil, sp.Typed))), ""
	}
	return "", "", ""
}

func (sp *mnSpec) typedHint() string {
	if sp.TypedHint != "" {
		return sp.TypedHint
	}
	return "enter manda pro agente"
}

// typedLine is the text of the line being typed in the menu (after the
// last Enter), for the row that shows it.
func (sp *mnSpec) typedLine() string {
	start := 0
	for i, k := range sp.Typed {
		if k.Code == KEnter {
			start = i + 1
		}
	}
	return string(uiApplyTyped(nil, sp.Typed[start:]))
}

// mnTakeTyping decides whether k, under Guard, is text being typed (true:
// it goes to sp.Typed, not to the menu). inWin: within the grace window or
// the typing pause of the previous key. Enter ends an open line and is
// never an answer then; editing keys edit that line.
func (m *mnState) takeTyping(k Key, inWin bool) bool {
	open := m.sp.typingOpen()
	switch k.Code {
	case KRune:
		if inWin { // a letter typed right after another one: still typing
			return true
		}
		// after a pause an option's letter answers (the typing-pause rule);
		// any other letter starts or continues a line of text
		r := unicode.ToLower(k.R)
		return !m.sp.Filter && r != 'j' && r != 'k' && !m.isOption(k) && unicode.IsPrint(k.R)
	case KPaste:
		return true
	case KBackspace, KCtrlU, KCtrlW, KAltEnter:
		return open
	case KEnter:
		return open || inWin
	}
	return false
}

// uiApplyTyped applies text-editing keys to buf (runes, paste, backspace,
// Ctrl+U/W, Alt+Enter). Enter is ignored: the caller decides what it means.
func uiApplyTyped(buf []rune, keys []Key) []rune {
	for _, k := range keys {
		switch k.Code {
		case KRune:
			buf = append(buf, k.R)
		case KPaste:
			buf = append(buf, []rune(strings.ReplaceAll(strings.ReplaceAll(k.Text, "\r\n", "\n"), "\r", "\n"))...)
		case KBackspace:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case KCtrlU:
			buf = nil
		case KCtrlW:
			buf = uiDelWord(buf)
		case KAltEnter:
			buf = append(buf, '\n')
		}
	}
	return buf
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
	if label, line, hint := sp.typingRow(g.dot); label != "" {
		line = sanitizeTerm(strings.ReplaceAll(line, "\n", " "))
		if lw := w - 40; lw > 8 && uiStrW(line) > lw { // the end of the line is what matters
			r := []rune(line)
			line = string(r[max(len(r)-lw, 0):])
		}
		if hint != "" {
			hint = dim + "  (" + hint + ")"
		}
		top = append(top, mnClip("  "+dim+g.arrow+" "+label+": "+reset+line+hint+reset, w))
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
		if t.dumb && drawn > 0 {
			return // no cursor movement: printed once, answered by number
		}
		w, h := io.size()
		lines := m.render(t, w-1, max(h-1, 6))
		var b strings.Builder
		if drawn > 0 {
			b.WriteString("\r\x1b[J")
		} else {
			b.WriteString("\x1b[?25l")
		}
		if t.dumb {
			io.write(strings.Join(lines, "\n") + "\n")
			drawn = len(lines)
			return
		}
		b.WriteString(strings.Join(lines, "\n"))
		fmt.Fprintf(&b, "\n\x1b[%dA", len(lines))
		drawn = len(lines)
		io.write(b.String())
	}
	erase := func() {
		if !t.dumb {
			io.write("\r\x1b[J\x1b[?25h")
		}
	}
	draw()
	since := time.Now()
	prev := sp.Since
	// Settle: an answer key waiting to see whether more typing follows
	var pend *Key
	var pendAt time.Time
	var pendRes int
	var settle <-chan time.Time
	for {
		var k Key
		var ok bool
		select {
		case k, ok = <-io.keys:
			if ok && k.Code == KCtrlD {
				ok = false
				sp.aborted = true
			}
		case <-io.resize:
			draw()
			continue
		case <-settle: // nothing followed the letter: it was the answer
			erase()
			return pendRes
		case <-ctx.Done():
		}
		if !ok {
			erase()
			return mnCancel
		}
		now := time.Now()
		if pend != nil {
			// another key right after the letter: it was the start of a
			// message, never an answer (the letter goes with the text)
			sp.Typed = append(sp.Typed, *pend)
			prev, pend, settle = pendAt, nil, nil
		}
		if sp.Guard {
			inWin := now.Sub(since) < uiApproveGrace || now.Sub(prev) < uiApproveIdle
			if m.takeTyping(k, inWin) {
				// typed text, never an answer: kept for the steering buffer
				sp.Typed = append(sp.Typed, k)
				prev = now
				draw()
				continue
			}
			if k.Code == KEsc && sp.typingOpen() {
				// Esc while typing erases that line (like Esc in the turn)
				for len(sp.Typed) > 0 && sp.Typed[len(sp.Typed)-1].Code != KEnter {
					sp.Typed = sp.Typed[:len(sp.Typed)-1]
				}
				draw()
				continue
			}
		}
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
		if sp.Guard && sp.Settle && k.Code == KRune {
			kk := k
			pend, pendAt, pendRes = &kk, now, res
			settle = time.After(uiApproveIdle)
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
	res := mnRun(ctx, u.T, io, sp)
	u.handBackTyped(sp.Typed)
	return res
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
