package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// vtScreen is a small terminal emulator for tests: enough of xterm/VTE to
// replay what the TUI writes and check the screen (cells, cursor, scrollback).
// Output processing is the tty's (ONLCR): "\n" moves to column 0 of the next
// row. reflow=true rewraps soft-wrapped lines on resize like VTE does;
// false keeps rows as they are like xterm.
type vtScreen struct {
	t          testing.TB
	w, h       int
	rows       []vtRow // scrollback + screen; the screen is the last h rows
	cx, cy     int     // cursor; cy indexes the screen (0..h-1)
	pendWrap   bool
	hidden     bool
	reflow     bool
	saved      [2]int
	partial    []byte // unfinished UTF-8 or escape sequence
	badEscapes []string
	paste      bool
}

type vtRow struct {
	cells   []rune // 0 = empty; -1 = right half of a wide char
	wrapped bool   // continues on the next row (soft wrap)
}

func newVT(t testing.TB, w, h int) *vtScreen {
	v := &vtScreen{t: t, w: w, h: h}
	for i := 0; i < h; i++ {
		v.rows = append(v.rows, vtRow{cells: make([]rune, w)})
	}
	return v
}

func (v *vtScreen) top() int { return len(v.rows) - v.h }

func (v *vtScreen) row(y int) *vtRow { return &v.rows[v.top()+y] }

func (v *vtScreen) lineFeed() {
	if v.cy < v.h-1 {
		v.cy++
		return
	}
	v.rows = append(v.rows, vtRow{cells: make([]rune, v.w)})
}

func (v *vtScreen) put(r rune) {
	rw := uiRuneW(r)
	if rw == 0 {
		return
	}
	if v.pendWrap || v.cx+rw > v.w {
		v.row(v.cy).wrapped = true
		v.cx, v.pendWrap = 0, false
		v.lineFeed()
	}
	rr := v.row(v.cy)
	rr.cells[v.cx] = r
	if rw == 2 {
		rr.cells[v.cx+1] = -1
	}
	v.cx += rw
	if v.cx >= v.w {
		v.cx, v.pendWrap = v.w-1, true
	}
}

// Write implements io.Writer.
func (v *vtScreen) Write(p []byte) (int, error) {
	v.feed(string(p))
	return len(p), nil
}

func (v *vtScreen) WriteString(s string) { v.feed(s) }

func (v *vtScreen) feed(s string) {
	if len(v.partial) > 0 {
		s = string(v.partial) + s
		v.partial = nil
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			j, ok := v.esc(s, i)
			if !ok {
				v.partial = []byte(s[i:])
				return
			}
			i = j
			continue
		case c == '\r':
			v.cx, v.pendWrap = 0, false
		case c == '\n':
			v.cx, v.pendWrap = 0, false
			v.lineFeed()
		case c == '\b':
			if v.cx > 0 {
				v.cx--
			}
			v.pendWrap = false
		case c == '\a', c == '\t':
		case c < 0x20 || c == 0x7f:
			v.badEscapes = append(v.badEscapes, fmt.Sprintf("control %#x", c))
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 && !utf8.FullRuneInString(s[i:]) {
				v.partial = []byte(s[i:])
				return
			}
			v.put(r)
			i += n
			continue
		}
		i++
	}
}

// esc handles one escape sequence at s[i]; ok=false means it is incomplete.
func (v *vtScreen) esc(s string, i int) (int, bool) {
	if i+1 >= len(s) {
		return 0, false
	}
	switch s[i+1] {
	case '7':
		v.saved = [2]int{v.cx, v.cy}
		return i + 2, true
	case '8':
		v.cx, v.cy = v.saved[0], v.saved[1]
		return i + 2, true
	case '[':
	default:
		v.badEscapes = append(v.badEscapes, strconv.Quote(s[i:i+2]))
		return i + 2, true
	}
	j := i + 2
	for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
		j++
	}
	if j >= len(s) {
		return 0, false
	}
	params, final := s[i+2:j], s[j]
	n := 1
	if p, err := strconv.Atoi(strings.TrimPrefix(params, "?")); err == nil && p > 0 {
		n = p
	}
	switch final {
	case 'm':
	case 'A':
		v.cy = max(v.cy-n, 0)
		v.pendWrap = false
	case 'B':
		v.cy = min(v.cy+n, v.h-1)
		v.pendWrap = false
	case 'C':
		v.cx = min(v.cx+n, v.w-1)
		v.pendWrap = false
	case 'D':
		v.cx = max(v.cx-n, 0)
		v.pendWrap = false
	case 'G':
		v.cx = min(n-1, v.w-1)
		v.pendWrap = false
	case 'H':
		parts := strings.Split(params, ";")
		y, x := 1, 1
		if len(parts) > 0 && parts[0] != "" {
			y, _ = strconv.Atoi(parts[0])
		}
		if len(parts) > 1 && parts[1] != "" {
			x, _ = strconv.Atoi(parts[1])
		}
		v.cy, v.cx, v.pendWrap = min(max(y-1, 0), v.h-1), min(max(x-1, 0), v.w-1), false
	case 'K':
		rr := v.row(v.cy)
		for x := v.cx; x < v.w; x++ {
			rr.cells[x] = 0
		}
		rr.wrapped = false
	case 'J':
		if params == "2" {
			for y := 0; y < v.h; y++ {
				*v.row(y) = vtRow{cells: make([]rune, v.w)}
			}
			break
		}
		rr := v.row(v.cy)
		for x := v.cx; x < v.w; x++ {
			rr.cells[x] = 0
		}
		rr.wrapped = false
		for y := v.cy + 1; y < v.h; y++ {
			*v.row(y) = vtRow{cells: make([]rune, v.w)}
		}
	case 'h', 'l':
		switch params {
		case "?25":
			v.hidden = final == 'l'
		case "?2004":
			v.paste = final == 'h'
		case "?1049", "?47", "?1047":
			v.badEscapes = append(v.badEscapes, "alt screen "+params+string(final))
		}
	case '~':
	default:
		v.badEscapes = append(v.badEscapes, strconv.Quote(s[i:j+1]))
	}
	return j + 1, true
}

// resize changes the size; with reflow, soft-wrapped lines are rewrapped and
// the cursor follows its cell, as in VTE.
func (v *vtScreen) resize(w, h int) {
	if !v.reflow {
		for i := range v.rows {
			c := make([]rune, w)
			copy(c, v.rows[i].cells)
			v.rows[i].cells = c
		}
		for len(v.rows) < h {
			v.rows = append(v.rows, vtRow{cells: make([]rune, w)})
		}
		cyAbs := v.top() + v.cy
		v.w, v.h = w, h
		v.cy = min(max(cyAbs-v.top(), 0), h-1)
		v.cx = min(v.cx, w-1)
		v.pendWrap = false
		return
	}
	// join rows into logical lines, remembering the cursor's offset
	type line struct{ cells []rune }
	var lines []line
	cur := []rune{}
	curLine, curOff := 0, 0
	cyAbs := v.top() + v.cy
	for i, r := range v.rows {
		cells := r.cells
		if !r.wrapped {
			end := len(cells)
			for end > 0 && cells[end-1] == 0 {
				end--
			}
			if i == cyAbs {
				end = max(end, v.cx)
			}
			cells = cells[:end]
		}
		if i == cyAbs {
			curLine, curOff = len(lines), len(cur)+v.cx
		}
		cur = append(cur, cells...)
		if !r.wrapped {
			lines = append(lines, line{cur})
			cur = []rune{}
		}
	}
	if len(cur) > 0 {
		lines = append(lines, line{cur})
	}
	var rows []vtRow
	ncx, ncy := 0, 0
	for li, l := range lines {
		start := len(rows)
		cells := l.cells
		if len(cells) == 0 {
			rows = append(rows, vtRow{cells: make([]rune, w)})
		}
		for len(cells) > 0 {
			k := min(w, len(cells))
			if k < len(cells) && cells[k] == -1 {
				k-- // do not split a wide char
			}
			c := make([]rune, w)
			copy(c, cells[:k])
			cells = cells[k:]
			rows = append(rows, vtRow{cells: c, wrapped: len(cells) > 0})
		}
		if li == curLine {
			ncy = start + curOff/w
			ncx = curOff % w
			for ncy >= len(rows) {
				rows = append(rows, vtRow{cells: make([]rune, w)})
			}
		}
	}
	// keep the cursor row on screen: drop trailing empty rows below it
	for len(rows) > ncy+1 && len(rows) > h && vtEmpty(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	for len(rows) < h {
		rows = append(rows, vtRow{cells: make([]rune, w)})
	}
	v.rows, v.w, v.h = rows, w, h
	v.cx, v.pendWrap = ncx, false
	v.cy = ncy - v.top()
	if v.cy < 0 {
		v.cy = 0
	}
}

func vtEmpty(r vtRow) bool {
	for _, c := range r.cells {
		if c != 0 {
			return false
		}
	}
	return true
}

func vtText(r vtRow) string {
	var b strings.Builder
	for _, c := range r.cells {
		switch c {
		case 0:
			b.WriteByte(' ')
		case -1:
		default:
			b.WriteRune(c)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// Screen is the visible screen, one string per row (trailing spaces trimmed).
func (v *vtScreen) Screen() []string {
	var out []string
	for y := 0; y < v.h; y++ {
		out = append(out, vtText(*v.row(y)))
	}
	return out
}

// All is scrollback + screen with trailing empty rows removed.
func (v *vtScreen) All() []string {
	var out []string
	for _, r := range v.rows {
		out = append(out, vtText(r))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func (v *vtScreen) Dump() string {
	var b strings.Builder
	for i, l := range v.All() {
		mark := "  "
		if i == v.top()+v.cy {
			mark = "> "
		}
		fmt.Fprintf(&b, "%s%3d|%s\n", mark, i, l)
	}
	return b.String()
}

func TestVTBasics(t *testing.T) {
	v := newVT(t, 10, 3)
	v.feed("abc\r\n12345678901\x1b[1A\r\x1b[Jx")
	if got := strings.Join(v.All(), "|"); got != "abc|x" {
		t.Fatalf("erase: %q", got)
	}
	v = newVT(t, 10, 3)
	v.feed("ação 🎷!")
	if s := v.Screen()[0]; s != "ação 🎷!" || v.cx != 8 {
		t.Fatalf("wide: %q cx=%d", s, v.cx)
	}
	v = newVT(t, 6, 4)
	v.reflow = true
	v.feed("abcdefghij\nxy")
	v.resize(4, 4)
	if a := v.All(); strings.Join(a, "|") != "abcd|efgh|ij|xy" || v.cx != 2 {
		t.Fatalf("reflow: %q cx=%d cy=%d", a, v.cx, v.cy)
	}
}
