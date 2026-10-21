package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------- theme ----------

type Theme struct {
	color, c256, truecolor, ascii bool
}

func DetectTheme() Theme {
	if runtime.GOOS == "windows" {
		return winTheme(termVT(), os.Getenv)
	}
	term := os.Getenv("TERM")
	t := Theme{color: os.Getenv("NO_COLOR") == "" && term != "dumb"}
	ct := os.Getenv("COLORTERM")
	t.truecolor = t.color && (ct == "truecolor" || ct == "24bit")
	t.c256 = t.color && (t.truecolor || strings.Contains(term, "256color") || strings.Contains(term, "kitty") || strings.Contains(term, "alacritty") || os.Getenv("TERM_PROGRAM") != "")
	t.ascii = term == "linux" || term == "vt100" || term == "vt220" || os.Getenv("JAZZ_ASCII") == "1"
	return t
}

// winTheme is the Windows theme. TERM is usually unset there, so it goes by
// what the console accepted: without VT processing (legacy console) there are
// no colors and the glyphs are ASCII. Windows Terminal (WT_SESSION) and
// terminals that set TERM_PROGRAM (VS Code, WezTerm) get truecolor and the
// Unicode glyphs; plain ConHost gets 256 colors and ASCII glyphs, since its
// default fonts lack several of them. JAZZ_ASCII=1/0 forces either set.
func winTheme(vt bool, env func(string) string) Theme {
	modern := env("WT_SESSION") != "" || env("TERM_PROGRAM") != "" || env("ConEmuANSI") == "ON"
	t := Theme{color: vt && env("NO_COLOR") == "" && env("TERM") != "dumb"}
	t.c256 = t.color
	t.truecolor = t.color && (modern || env("COLORTERM") == "truecolor" || env("COLORTERM") == "24bit")
	t.ascii = !vt || !modern
	switch env("JAZZ_ASCII") {
	case "1":
		t.ascii = true
	case "0":
		t.ascii = !vt
	}
	return t
}

type rgb struct{ r, g, b int }

// uiFgCache memoizes fg: it is called several times per streamed line and
// per redraw, and Sprintf allocates every time.
var uiFgCache sync.Map // uiFgKey -> string

type uiFgKey struct {
	t     Theme
	c     rgb
	basic int
}

func (t Theme) fg(c rgb, basic int) string {
	if !t.color {
		return ""
	}
	k := uiFgKey{t, c, basic}
	if v, ok := uiFgCache.Load(k); ok {
		return v.(string)
	}
	s := t.fgCompute(c, basic)
	uiFgCache.Store(k, s)
	return s
}

func (t Theme) fgCompute(c rgb, basic int) string {
	switch {
	case !t.color:
		return ""
	case t.truecolor:
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b)
	case t.c256:
		return fmt.Sprintf("\x1b[38;5;%dm", 16+36*(c.r*5/255)+6*(c.g*5/255)+(c.b*5/255))
	}
	return fmt.Sprintf("\x1b[%dm", basic)
}

// bg is a subtle background tint: truecolor rgb, or a 256-colour index. With
// 16 colours there is no tint that reads well, so it returns "" (fg-only).
func (t Theme) bg(c rgb, idx256 int) string {
	switch {
	case !t.color:
		return ""
	case t.truecolor:
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.r, c.g, c.b)
	case t.c256:
		return fmt.Sprintf("\x1b[48;5;%dm", idx256)
	}
	return ""
}

func (t Theme) sgr(code string) string {
	if !t.color {
		return ""
	}
	return "\x1b[" + code + "m"
}

// Nord-ish palette. The basic (16-colour) codes are the matching ANSI slots,
// so a terminal with a Nord scheme shows the same colours without truecolor.
var (
	cAccent  = rgb{136, 192, 208} // #88C0D0 frost      (36)
	cBlue    = rgb{129, 161, 193} // #81A1C1            (34)
	cMagenta = rgb{180, 142, 173} // #B48EAD purple     (35)
	cGreen   = rgb{163, 190, 140} // #A3BE8C            (32)
	cGold    = rgb{235, 203, 139} // #EBCB8B            (33)
	cRed     = rgb{191, 97, 106}  // #BF616A            (31)
	cOrange  = rgb{208, 135, 112} // #D08770            (33)
	cGray    = rgb{118, 130, 153} // muted text         (90)
	cCyan    = cAccent

	cDelBg  = rgb{67, 44, 52} // diff tints (truecolor); 256: 52 / 22
	cAddBg  = rgb{44, 62, 50}
	cCodeBg = rgb{59, 66, 82} // #3B4252 inline code (256: 236)
)

type glyphs struct {
	spin                                           []string
	bot, tool, res, prompt, ok, fail, warn, dot    string
	todoOpen, todoDoing, todoDone, arrow, bar, sep string
	bullet, ell, nl, block, shade                  string
	// boxes and menus
	tl, tr, bl, br, h, v string
	star, ptr            string // brand mark, menu pointer
	modeAuto, modeAsk    string
	modeYolo, modePlan   string
	modeSmart            string
	up, down             string
}

// G returns the glyph set. The Unicode set avoids code points that fall back
// to emoji or serif fonts on a plain Linux desktop (⏺ ⏵ ⏸ ✳ ☑ render as
// coloured emoji there): ● ⎿ ✻ ✽ ✶ ✢ ☐ ◐ ☒ ▸ › are in JetBrains/DejaVu Mono.
// The prompt is "›": with JetBrains Mono on old VTE, " >" draws as an arrow.
func (t Theme) G() glyphs {
	if t.ascii {
		return uiGlyphsASCII
	}
	return uiGlyphsUni
}

// The two glyph sets, built once (G is called per streamed rune).
var (
	uiGlyphsASCII = glyphs{spin: []string{"|", "/", "-", "\\"}, bot: "*", tool: "*", res: "`-", prompt: ">", ok: "ok", fail: "x", warn: "!", dot: "-",
		todoOpen: "[ ]", todoDoing: "[~]", todoDone: "[x]", arrow: "->", bar: "|", sep: "-",
		bullet: "*", ell: "...", nl: "~", block: "#", shade: ".",
		tl: "+", tr: "+", bl: "+", br: "+", h: "-", v: "|", star: "*", ptr: ">",
		modeAuto: ">>", modeAsk: ">", modeYolo: ">>>", modePlan: "||", modeSmart: "?>", up: "^", down: "v"}
	uiGlyphsUni = glyphs{spin: []string{"·", "✢", "✶", "✻", "✽", "✻", "✶", "✢"}, bot: "●", tool: "●", res: "⎿", prompt: "›", ok: "✓", fail: "✗", warn: "⚠", dot: "·",
		todoOpen: "☐", todoDoing: "◐", todoDone: "☒", arrow: "↳", bar: "│", sep: "─",
		bullet: "•", ell: "…", nl: "↵", block: "█", shade: "░",
		tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│", star: "✻", ptr: "▸",
		modeAuto: "▸▸", modeAsk: "▸", modeYolo: "▸▸▸", modePlan: "‖", modeSmart: "◆", up: "↑", down: "↓"}
)

// ---------- banner and welcome ----------

var bannerArt = []string{
	"     ██╗ █████╗ ███████╗███████╗",
	"     ██║██╔══██╗╚══███╔╝╚══███╔╝",
	"     ██║███████║  ███╔╝   ███╔╝ ",
	"██   ██║██╔══██║ ███╔╝   ███╔╝  ",
	"╚█████╔╝██║  ██║███████╗███████╗",
	" ╚════╝ ╚═╝  ╚═╝╚══════╝╚══════╝",
}

func lerp(a, b rgb, f float64) rgb {
	return rgb{a.r + int(float64(b.r-a.r)*f), a.g + int(float64(b.g-a.g)*f), a.b + int(float64(b.b-a.b)*f)}
}

// Banner is the big ANSI-shadow logo (jazz --banner, or a terminal ≥ 100 columns).
func Banner(t Theme, info []string) string {
	reset := t.sgr("0")
	if termWidth() < 64 {
		return fmt.Sprintf("%s%s JAZZ%s %s\n", t.fg(cAccent, 36), t.sgr("1"), reset, strings.Join(info, "  "))
	}
	var b strings.Builder
	b.WriteString("\n")
	stops := []rgb{cAccent, cBlue, cMagenta, cGreen, cGold}
	basics := []int{36, 34, 35, 32, 33}
	for i, line := range bannerArt {
		rs := []rune(line)
		b.WriteString("  ")
		for j, r := range rs {
			if r == ' ' {
				b.WriteRune(r)
				continue
			}
			f := float64(j) / float64(len(rs)-1)
			seg := f * float64(len(stops)-1)
			k := min(int(seg), len(stops)-2)
			c := lerp(stops[k], stops[k+1], seg-float64(k))
			basic := basics[min(int(f*float64(len(basics))), len(basics)-1)]
			if r == '█' {
				b.WriteString(t.fg(c, basic) + string(r))
			} else {
				b.WriteString(t.fg(lerp(c, rgb{46, 52, 64}, 0.55), 90) + string(r))
			}
		}
		b.WriteString(reset)
		if i >= 1 && i-1 < len(info) {
			b.WriteString("   " + info[i-1])
		}
		b.WriteString("\n")
	}
	note := "♪"
	if t.ascii {
		note = "~"
	}
	fmt.Fprintf(&b, "  %s%s improvisa código no terminal %s %s/help%s · %sesc%s interrompe · %s\\%s+enter quebra linha%s\n\n",
		t.fg(cGold, 33), note, t.fg(cGray, 90), t.sgr("1"), t.sgr("22")+t.fg(cGray, 90), t.sgr("1"), t.sgr("22")+t.fg(cGray, 90), t.sgr("1"), t.sgr("22")+t.fg(cGray, 90), reset)
	return b.String()
}

// WelcomeInfo is what the welcome box shows.
type WelcomeInfo struct {
	Version, Model, Dir, Mode string
}

// Welcome renders the compact start screen: a rounded box with the brand,
// model, folder and mode, then a few tips. It fits w columns (≥ 86 is the
// the Dell's welcome terminal); below 40 columns it collapses to one line.
func Welcome(t Theme, w int, in WelcomeInfo) string {
	g := t.G()
	reset := t.sgr("0")
	acc, dim, bold := t.fg(cAccent, 36), t.fg(cGray, 90), t.sgr("1")
	if w < 40 {
		return fmt.Sprintf("%s%s%s jazz%s %s%s · %s%s\n\n", acc, bold, g.star, reset, dim, in.Model, in.Dir, reset)
	}
	bw := min(w-1, 100) // same width as the input box below it
	inner := bw - 4
	row := func(s string) string {
		s = clipVisible(s, inner)
		pad := max(inner-uiVisLen(s), 0)
		return acc + g.v + reset + " " + s + reset + strings.Repeat(" ", pad) + " " + acc + g.v + reset + "\n"
	}
	var b strings.Builder
	b.WriteString(acc + g.tl + strings.Repeat(g.h, bw-2) + g.tr + reset + "\n")
	b.WriteString(row(acc + bold + g.star + " jazz" + reset + dim + "  v" + in.Version + " " + g.dot + " agente de código no terminal"))
	b.WriteString(row(""))
	b.WriteString(row("  " + dim + "modelo " + reset + " " + in.Model))
	b.WriteString(row("  " + dim + "pasta  " + reset + " " + in.Dir))
	b.WriteString(row("  " + dim + "modo   " + reset + " " + uiModeTag(t, in.Mode)))
	b.WriteString(row(""))
	tip := func(k, d string) string { return bold + k + reset + dim + " " + d + reset }
	sep := dim + "  " + g.dot + "  " + reset
	b.WriteString(row("  " + tip("/help", "ajuda") + sep + tip("esc", "interrompe") + sep + tip("@", "menciona arquivo")))
	b.WriteString(row("  " + tip("!", "roda no shell") + sep + tip("shift+tab", "troca o modo") + sep + tip("?", "atalhos")))
	b.WriteString(acc + g.bl + strings.Repeat(g.h, bw-2) + g.br + reset + "\n\n")
	return b.String()
}

// uiModeTag is the coloured mode marker used by the welcome box and the footer.
func uiModeTag(t Theme, mode string) string {
	g := t.G()
	switch mode {
	case "plan":
		return t.fg(cBlue, 34) + t.sgr("1") + g.modePlan + " plano" + t.sgr("0")
	case "yolo":
		return t.fg(cRed, 31) + t.sgr("1") + g.modeYolo + " yolo" + t.sgr("0")
	case "ask":
		return t.fg(cGold, 33) + g.modeAsk + " ask" + t.sgr("0")
	case "smart":
		return t.fg(cMagenta, 35) + g.modeSmart + " smart" + t.sgr("0")
	case "auto", "":
		return t.fg(cGreen, 32) + g.modeAuto + " auto" + t.sgr("0")
	}
	return t.fg(cAccent, 36) + mode + t.sgr("0")
}

// uiVisLen is the visible width of s (escape sequences removed), in runes.
func uiVisLen(s string) int { return utf8.RuneCountInString(stripANSI(s)) }

// ---------- streaming markdown ----------

// uiWrapper word-wraps streamed, already-styled text to width columns with a
// hanging indent. Escape sequences are zero-width. width 0 = pass-through.
// A word is held back until the space or newline after it arrives.
type uiWrapper struct {
	width, col, hang int
	nowrap           bool         // fenced code: never wrapped (the terminal wraps it)
	out, word        bytes.Buffer // Reset keeps the capacity: no allocation per chunk
	wordW            int
}

func (w *uiWrapper) WriteRune(r rune) {
	if w.width <= 0 {
		w.out.WriteRune(r)
		return
	}
	w.rune(r)
}

// rune is one visible (or newline) rune; widths are terminal cells.
func (w *uiWrapper) rune(r rune) {
	switch {
	case r == '\n':
		w.flush()
		w.out.WriteByte('\n')
		w.col = 0
	case w.nowrap:
		w.flush()
		w.out.WriteRune(r)
		w.col += uiRuneW(r)
	case r == ' ':
		w.flush()
		if w.col < w.width {
			w.out.WriteByte(' ')
			w.col++
		}
	default:
		w.word.WriteRune(r)
		w.wordW += uiRuneW(r)
	}
}

func (w *uiWrapper) WriteString(s string) {
	if w.width <= 0 {
		w.out.WriteString(s)
		return
	}
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := uiEscEnd(s, i)
			if w.word.Len() > 0 {
				w.word.WriteString(s[i:j])
			} else {
				w.out.WriteString(s[i:j])
			}
			i = j
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		w.rune(r)
	}
}

// flush writes the pending word, moving it to a new indented line when it
// does not fit (unless the line holds nothing but the indent).
func (w *uiWrapper) flush() {
	if w.word.Len() == 0 {
		return
	}
	if w.wordW > 0 && w.col+w.wordW > w.width && w.col > w.hang {
		w.out.WriteString("\n" + strings.Repeat(" ", w.hang))
		w.col = w.hang
	}
	w.out.Write(w.word.Bytes())
	w.col += w.wordW
	if w.width > 0 && w.col > w.width { // a word longer than the line: the terminal wrapped it
		w.col %= w.width
	}
	w.word.Reset()
	w.wordW = 0
}

func (w *uiWrapper) take() string {
	s := w.out.String()
	w.out.Reset()
	return s
}

type mdRenderer struct {
	t         Theme
	lineStart bool
	lineBuf   string // undecided start-of-line chars
	fence     bool
	header    bool
	bold      bool
	code      bool
	quote     bool
	star      bool // pending single '*'
	indent    string
	first     bool // first line sits right after the bullet: no indent
	width     int  // wrap width (0 = no wrapping)
	w         uiWrapper
}

func (m *mdRenderer) ind() string {
	if m.first {
		m.first = false
		return ""
	}
	return m.indent
}

func (m *mdRenderer) reset() {
	*m = mdRenderer{t: m.t, lineStart: true, indent: m.indent, width: m.width}
}

// begin starts a message whose first line is already at column col (after the bullet).
func (m *mdRenderer) begin(col, width int) {
	m.lineStart, m.first = true, true
	m.width = width
	m.w = uiWrapper{width: width, col: col, hang: utf8.RuneCountInString(m.indent)}
}

func (m *mdRenderer) codeColor() string { return m.t.fg(cCyan, 36) }

// Feed converts a chunk of markdown into ANSI text.
func (m *mdRenderer) Feed(s string) string {
	m.w.width = m.width
	for _, r := range s {
		m.feedRune(r, &m.w)
	}
	return m.w.take()
}

// FeedTo is Feed appending to dst (no string per chunk).
func (m *mdRenderer) FeedTo(s string, dst *strings.Builder) {
	m.w.width = m.width
	for _, r := range s {
		m.feedRune(r, &m.w)
	}
	dst.Write(m.w.out.Bytes())
	m.w.out.Reset()
}

func (m *mdRenderer) styleNow() string {
	t := m.t
	st := t.sgr("0")
	if m.fence {
		return st + m.codeColor()
	}
	if m.header {
		st += t.sgr("1") + t.fg(cMagenta, 35)
	}
	if m.quote {
		st += t.sgr("3") + t.fg(cGray, 90)
	}
	if m.bold {
		st += t.sgr("1")
	}
	if m.code {
		st += m.codeColor()
	}
	return st
}

// fenceBar is the dim frame drawn left of each line of a fenced code block.
func (m *mdRenderer) fenceBar() string {
	return m.t.fg(cGray, 90) + m.t.G().v + " " + m.styleNow()
}

// mdNumbered: "1. " / "12) " list marker length, or 0.
func mdNumbered(s string) int {
	i := 0
	for i < len(s) && i < 3 && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(s) || (s[i] != '.' && s[i] != ')') || s[i+1] != ' ' {
		return 0
	}
	return i + 2
}

// mdUndecided: the start of a line could still become a marker (fence,
// heading, bullet, quote, numbered item), so keep buffering it.
func mdUndecided(trim string) bool {
	if trim == "" || trim == "-" || trim == "*" || trim == "+" || trim == ">" {
		return true
	}
	if strings.HasPrefix("```", trim) && len(trim) < 3 {
		return true
	}
	if strings.HasPrefix(trim, "#") && strings.Trim(trim, "#") == "" {
		return true
	}
	d := strings.TrimRight(trim, "0123456789")
	if d == "" && len(trim) <= 3 {
		return true
	}
	if (strings.HasSuffix(trim, ".") || strings.HasSuffix(trim, ")")) && len(trim) <= 4 && strings.Trim(trim[:len(trim)-1], "0123456789") == "" && len(trim) > 1 {
		return true
	}
	return false
}

func (m *mdRenderer) feedRune(r rune, out *uiWrapper) {
	g := m.t.G()
	indW := utf8.RuneCountInString(m.indent)
	if m.lineStart {
		if r == '\n' {
			if m.lineBuf == "" && m.fence {
				out.WriteString(m.ind() + strings.TrimRight(m.fenceBar(), " "))
			}
			m.flushLineStart(out)
			out.WriteString("\n")
			m.lineStart = true
			m.first = false
			return
		}
		m.lineBuf += string(r)
		lb := m.lineBuf
		trim := strings.TrimLeft(lb, " ")
		if mdUndecided(trim) && !(m.fence && !strings.HasPrefix("```", trim)) {
			return
		}
		m.lineStart = false
		m.lineBuf = ""
		out.WriteString(m.ind())
		pad := lb[:len(lb)-len(trim)]
		out.hang = indW
		switch {
		case strings.HasPrefix(trim, "```"):
			m.fence = !m.fence
			lang := strings.TrimPrefix(trim, "```")
			dim := m.t.fg(cGray, 90)
			if m.fence {
				out.WriteString(dim + g.tl + g.h + " " + lang)
				out.nowrap = true
			} else {
				out.WriteString(dim + g.bl + g.h + g.h + " " + lang)
				out.nowrap = false
			}
			m.header = true // swallow rest of fence line styling as dim
			return
		case m.fence:
			out.WriteString(m.fenceBar() + lb)
			return
		case uiIsHeading(trim):
			m.header = true
			out.WriteString(m.styleNow() + strings.TrimLeft(strings.TrimLeft(trim, "#"), " "))
			return
		case len(trim) >= 2 && (trim[0] == '-' || trim[0] == '*' || trim[0] == '+') && trim[1] == ' ':
			out.hang = indW + len(pad) + 2
			out.WriteString(pad + m.t.fg(cGold, 33) + g.bullet + m.styleNow() + " ")
			for _, rr := range trim[2:] {
				m.inline(rr, out)
			}
			return
		case len(trim) >= 2 && trim[0] == '>' && trim[1] == ' ':
			m.quote = true
			out.hang = indW + len(pad) + 2
			out.WriteString(pad + m.t.fg(cGray, 90) + g.v + " " + m.styleNow())
			for _, rr := range trim[2:] {
				m.inline(rr, out)
			}
			return
		case mdNumbered(trim) > 0:
			n := mdNumbered(trim)
			out.hang = indW + len(pad) + n
			out.WriteString(pad + m.t.fg(cBlue, 34) + trim[:n-1] + m.styleNow() + " ")
			for _, rr := range trim[n:] {
				m.inline(rr, out)
			}
			return
		}
		for _, rr := range lb {
			m.inline(rr, out)
		}
		return
	}
	if r == '\n' {
		if m.star {
			out.WriteString("*")
			m.star = false
		}
		m.header = false
		m.quote = false
		if !m.fence {
			m.code = false
			m.bold = false // a stray ** must not bold the rest of the message
		}
		out.WriteString(m.t.sgr("0") + "\n")
		m.lineStart = true
		return
	}
	if m.fence {
		out.WriteRune(r)
		return
	}
	m.inline(r, out)
}

func (m *mdRenderer) inline(r rune, out *uiWrapper) {
	if m.fence {
		out.WriteRune(r)
		return
	}
	if m.star {
		m.star = false
		if r == '*' {
			m.bold = !m.bold
			out.WriteString(m.styleNow())
			return
		}
		out.WriteString("*")
	}
	switch r {
	case '*':
		if !m.code {
			m.star = true
			return
		}
	case '`':
		m.code = !m.code
		out.WriteString(m.styleNow())
		return
	}
	out.WriteRune(r)
}

func (m *mdRenderer) flushLineStart(out *uiWrapper) {
	if m.lineBuf != "" {
		out.WriteString(m.ind())
		if m.fence { // a lone "-", "+", "#" or "*" inside a fence is still code
			out.WriteString(m.fenceBar())
		}
		out.WriteString(m.lineBuf)
		m.lineBuf = ""
	}
}

// uiIsHeading: 1-6 '#' followed by a space (CommonMark ATX heading).
func uiIsHeading(trim string) bool {
	rest := strings.TrimLeft(trim, "#")
	n := len(trim) - len(rest)
	return n >= 1 && n <= 6 && strings.HasPrefix(rest, " ")
}

// Flush ends the message, closing any open style.
func (m *mdRenderer) Flush() string {
	m.w.width = m.width
	m.flushLineStart(&m.w)
	if m.star {
		m.w.WriteString("*")
	}
	m.w.flush()
	m.w.WriteString(m.t.sgr("0"))
	s := m.w.take()
	m.reset()
	return s
}

// ---------- terminal UI ----------

type TermUI struct {
	T      Theme
	keys   *KeyReader
	cfg    *Config
	mu     sync.Mutex
	col0   bool
	inText bool
	md     mdRenderer
	san    uiSan // escape filter for streamed answer text (bug 32)
	rsan   uiSan // escape filter for streamed reasoning

	spinOn    bool
	spinLabel string
	spinStart time.Time
	spinLive  string // last line of a running command
	reasonN   int
	typed     string // what the user is typing during a turn (G17)

	// Approval/Ask key handshake (bugs 4, 11, 13, 41). While a WatchTurn runs it
	// owns the key channel; a prompt sends a fresh channel on grabCh to receive
	// keys and nil to release it. Keys never outlive the prompt that grabbed them.
	grabCh    chan chan Key
	watchDone chan struct{} // done channel of the running watcher; nil when none
	leftover  []rune        // text typed during the last turn, not yet sent

	lastTool string // id of the last ToolStart (parallel calls)

	lastKey    time.Time // when the watcher last saw a key (typing in progress)
	alwaysRule string    // rule [t] would save for the next Approve (ApproveHint)

	mode string // effective permission mode (SetMode)
	jobs int    // running background jobs (Jobs)

	nl         int    // newlines at the end of the output (2 = a blank line is there)
	toolLine   string // "Name(detail)" of the last ToolStart line
	toolLineID string
	toolFresh  bool      // nothing was printed since that line (ToolEnd repaints its dot)
	lastOut    uiLastOut // for ctrl+o / /expand

	lv uiLive // live region: unfinished stream line + status line (ui_live.go)

	offers    []olAction     // collapsed menu while the first token is late (offers.go)
	offerRun  func(olAction) // runs a picked offer
	lastErr   string         // text of the last failed tool call
	edReading bool           // the input editor is up (AsyncNotice)
	async     chan string    // notices for the editor to print
}

// uiApproveGrace: keys that arrive this soon after a prompt is drawn were typed
// before the user could read it and never count as an answer.
const uiApproveGrace = 250 * time.Millisecond

// uiApproveIdle: a single-key answer counts only when the previous key came at
// least this long before it; a faster key is the user still typing (steering
// text such as "vamos usar pnpm" must not answer the prompt with its letters).
const uiApproveIdle = 600 * time.Millisecond

// uiApproveMax caps how much of a command is shown before "[v] ver tudo".
const uiApproveMax = 2000

func NewTermUI(cfg *Config, keys *KeyReader) *TermUI {
	t := DetectTheme()
	if cfg == nil {
		cfg = &Config{}
	}
	u := &TermUI{T: t, keys: keys, cfg: cfg, col0: true, nl: 2, grabCh: make(chan chan Key)}
	u.md = mdRenderer{t: t, lineStart: true, indent: "  "}
	u.lv.on = uiForceLive || isTTY(os.Stdout)
	if u.lv.on && !uiForceLive {
		uiOnResize(u.liveResize)
	}
	return u
}

// rawWrite writes s after committing the live region (u.mu held).
func (u *TermUI) rawWrite(s string) {
	u.liveCommit()
	if s == "" {
		return
	}
	uiWrite(s)
	u.toolFresh = false
	u.uiTrackNL(s)
	u.lv.sgr = sgrTrack(u.lv.sgr, s)
}

// Flush commits the live region: the output is final and the cursor is at
// its end (before something else, such as the input editor, draws).
func (u *TermUI) Flush() {
	u.mu.Lock()
	u.liveCommit()
	u.mu.Unlock()
}

func (u *TermUI) print(s string) {
	u.mu.Lock()
	u.rawWrite(s)
	u.mu.Unlock()
}

func (u *TermUI) ensureLine() {
	u.liveCommit()
	if !u.col0 {
		u.rawWrite("\n")
	}
}

func (u *TermUI) endText() {
	if u.inText {
		if u.lv.on {
			u.lv.pend.WriteString(u.md.Flush())
		} else {
			u.rawWrite(u.md.Flush())
		}
		u.inText = false
		u.lv.msgBytes = 0
		u.ensureLine()
	}
}

func (u *TermUI) StreamText(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	head := ""
	if !u.inText {
		u.gap()
		head = u.T.fg(cAccent, 36) + u.T.G().bot + u.T.sgr("0") + " "
		u.md.begin(2, termWidth()-1)
		u.inText = true
		u.san = uiSan{}
		u.lv.msgBytes = 0
	}
	if !u.lv.on {
		u.rawWrite(head + u.md.Feed(u.san.filter(s)))
		return
	}
	// live: the token path only buffers; the render goroutine writes frames
	u.lv.pend.WriteString(head)
	u.md.FeedTo(u.san.filter(s), &u.lv.pend)
	u.lv.msgBytes += len(s)
	u.lv.toks++
	u.lv.lastTok = time.Now()
	if u.lv.phase != "gerando" && u.spinOn {
		u.lv.phase = "gerando"
	}
	u.liveRender(false) // the first chunk after a pause shows at once
	u.liveKick()
}

func (u *TermUI) StreamReasoning(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cfg.ShowThinking {
		if u.inText {
			u.endText()
		}
		u.rawWrite(u.T.sgr("3") + u.T.fg(cGray, 90) + strings.ReplaceAll(u.rsan.filter(s), "\n", "\n  ") + u.T.sgr("0"))
		return
	}
	u.reasonN += len(s)
	if u.spinOn {
		u.spinLabel = "raciocinando"
	}
}

func (u *TermUI) Thinking(label string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	if !u.toolFresh && !u.spinOn {
		u.gap()
	} else {
		u.ensureLine()
	}
	u.spinLabel = label
	u.spinLive = ""
	if u.spinOn {
		return
	}
	u.spinOn = true
	u.spinStart = time.Now()
	u.reasonN = 0
	u.lv.phase, u.lv.lastTok, u.lv.toks = "", time.Time{}, 0
	// the status line shows at once (honest feedback within one frame), then
	// the render goroutine animates it at 8 fps (2 fps in low-power mode)
	u.liveRender(true)
	u.liveKick()
}

// spinLine renders the spinner line (u.mu held).
func (u *TermUI) spinLine(frame int) string {
	t, g := u.T, u.T.G()
	el := time.Since(u.spinStart).Round(time.Second)
	w := termWidth() - 1
	info := []string{el.String()}
	info = append(info, u.livePhaseInfo()...)
	if tok := u.reasonN / 4; tok > 0 {
		info = append(info, g.down+" "+fmtK(tok)+" tokens")
	} else if u.lv.toks > 0 {
		info = append(info, g.down+" "+fmtK(u.lv.toks)+" tokens")
	}
	if ol := u.offerLineNow(); ol != "" && u.typed == "" {
		info = append(info, t.fg(cGold, 33)+ol+t.fg(cGray, 90))
	} else if u.typed == "" {
		info = append(info, "esc pra interromper")
	}
	head := fmt.Sprintf("%s%s %s%s%s %s(%s)", t.fg(cAccent, 36), g.spin[frame%len(g.spin)], t.sgr("1"), u.spinLabelText()+g.ell, t.sgr("0"), t.fg(cGray, 90), strings.Join(info, " "+g.dot+" "))
	if u.spinLive != "" {
		head += " " + g.dot + " " + u.spinLive
	}
	if u.typed == "" {
		return clipVisible(head+t.sgr("0"), w)
	}
	// The typed text goes last and keeps its tail (where the cursor is) visible.
	pre := t.sgr("0") + t.fg(cGold, 33) + g.prompt + t.sgr("0") + " "
	room := w - uiStrW(head) - 3 - uiStrW(g.prompt) - 1
	typed := []rune(u.typed)
	if room < 12 { // narrow terminal: the typed text replaces the status
		head, room = "", w-uiStrW(g.prompt)-1
	} else {
		head += " " + g.dot + " "
	}
	if uiRunesW(typed) > room {
		// keep the tail (where the cursor is) visible
		ell := []rune(g.ell)
		k := len(typed)
		for k > 0 && uiRunesW(typed[k-1:])+uiRunesW(ell) <= room {
			k--
		}
		typed = append(append([]rune{}, ell...), typed[k:]...)
	}
	return clipVisible(head+pre+string(typed), w)
}

func (u *TermUI) StopThinking() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.spinOn {
		u.spinOn = false
		u.liveRender(true) // drops the status line, keeps the stream tail
	}
}

// ShellOutput shows the latest line of a running command in the spinner.
func (u *TermUI) ShellOutput(chunk []byte) {
	if chunk == nil {
		return
	}
	s := strings.TrimRight(string(chunk), "\n\r ")
	if i := strings.LastIndexAny(s, "\r\n"); i >= 0 {
		s = s[i+1:]
	}
	s = sanitizeTerm(stripANSI(strings.TrimSpace(s)))
	if s == "" {
		return
	}
	u.mu.Lock()
	u.spinLive = oneLine(s, 70)
	u.mu.Unlock()
}

func (u *TermUI) toolColor(name string) string {
	switch name {
	case "bash":
		return u.T.fg(cGold, 33)
	case "edit", "write":
		return u.T.fg(cGreen, 32)
	case "read":
		return u.T.fg(cBlue, 34)
	case "todo":
		return u.T.fg(cMagenta, 35)
	}
	return u.T.fg(cCyan, 36)
}

var toolLabel = map[string]string{"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash", "todo": "Plano", "task": "Tarefa", "fetch": "Fetch", "ask_user": "Pergunta"}

func uiToolLabel(name string) string {
	if l := toolLabel[name]; l != "" {
		return l
	}
	l := strings.TrimPrefix(name, "mcp__")
	return strings.Replace(l, "__", "/", 1)
}

// uiDetail makes a model-controlled detail safe to print: control characters
// become visible text (bug 10) and, on the ASCII theme, so does the newline mark.
func (u *TermUI) uiDetail(s string) string {
	v := visibleDetail(s)
	if u.T.ascii {
		v = strings.ReplaceAll(v, "⏎", `\n`)
	}
	return v
}

func (u *TermUI) ToolStart(id, name, detail string) {
	u.mu.Lock()
	u.endText()
	u.gap()
	head := u.toolHead(name, detail)
	u.rawWrite(clipVisible(u.T.fg(cGray, 90)+u.T.G().tool+u.T.sgr("0")+" "+head, termWidth()-1) + "\n")
	u.toolLine, u.toolLineID, u.toolFresh = head, id, true
	u.lastTool = id
	u.mu.Unlock()
	if name == "bash" || strings.HasPrefix(name, "mcp__") || name == "task" {
		u.Thinking("rodando")
	}
}

func (u *TermUI) ToolEnd(id, name string, out ToolOutput, d time.Duration) {
	u.StopThinking()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	t, g := u.T, u.T.G()
	w := termWidth() - 1
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	dot := t.fg(cGreen, 32)
	if out.IsErr {
		dot = t.fg(cRed, 31)
	}
	sum := out.Display.Summary
	if sum == "" {
		sum = firstSentence(out.Text)
	}
	sum = oneLine(sanitizeTerm(sum), 400)
	adds, dels := 0, 0
	for _, h := range out.Display.Diff {
		adds += len(h.New)
		dels += len(h.Old)
	}
	if !out.IsErr && out.Display.Path != "" {
		p := sanitizeTerm(out.Display.Path)
		switch {
		case name == "edit" && len(out.Display.Diff) > 0:
			sum = fmt.Sprintf("Atualizou %s com %d %s e %d %s", t.sgr("1")+p+t.sgr("22"), adds, uiPl(adds, "adição", "adições"), dels, uiPl(dels, "remoção", "remoções"))
		case name == "write" && out.Display.Lines > 0:
			sum = fmt.Sprintf("Escreveu %d %s em %s", out.Display.Lines, uiPl(out.Display.Lines, "linha", "linhas"), t.sgr("1")+p+t.sgr("22"))
		}
	}
	if u.toolFresh && u.toolLineID == id && u.toolLine != "" {
		// the call line is right above: repaint its dot with the outcome
		u.rawWrite("\x1b[1A\r" + clipVisible(dot+g.tool+reset+" "+u.toolLine, w) + "\x1b[K\n")
	} else if id != "" && id != u.lastTool { // another call started since: say which one ended
		sum = uiToolLabel(name) + ": " + sum
	}
	col := dim
	if out.IsErr {
		col = t.fg(cRed, 31)
		u.lastErr = truncateHeadTail(out.Text, 4000)
	}
	u.rawWrite(clipVisible(fmt.Sprintf("  %s%s  %s%s", dim, g.res, col+sum, reset), w) + "\n")
	u.lastOut = uiLastOut{label: stripANSI(u.toolHead(name, "")), text: out.Text}
	if u.toolLineID == id {
		u.lastOut.label = stripANSI(u.toolLine)
	}
	more := func(n int) {
		if n > 0 {
			u.rawWrite(fmt.Sprintf("     %s%s +%d %s %s(ctrl+o pra expandir)%s\n", dim, g.ell, n, uiPl(n, "linha", "linhas"), t.sgr("3"), reset))
		}
	}
	if prev := out.Display.Preview; len(prev) > 0 && len(out.Display.Diff) == 0 {
		shown := min(len(prev), uiPreviewMax)
		for _, l := range prev[:shown] {
			u.rawWrite(clipVisible("     "+dim+uiClean(l)+reset, w) + "\n")
		}
		total := max(out.Display.Lines, len(prev))
		more(total - shown)
	}
	if len(out.Display.Diff) > 0 {
		u.rawWrite(uiDiffView(t, out.Display.Diff, w, 12))
		if total := adds + dels; total > 12 {
			more(total - 12)
		}
	}
	for _, td := range out.Display.Todos {
		mark, c := g.todoOpen, ""
		switch td.Status {
		case "in_progress":
			mark, c = g.todoDoing, t.fg(cAccent, 36)+t.sgr("1")
		case "completed":
			mark, c = g.todoDone, dim
			if !t.ascii {
				c += t.sgr("9")
			}
		}
		u.rawWrite(clipVisible("     "+c+mark+" "+oneLine(sanitizeTerm(td.Content), 300)+reset, w) + "\n")
	}
}

// uiWrapMsg word-wraps a notice/warning that follows a 4-cell "  ● " mark:
// continuation rows are indented under the text (lines of the message that
// start with spaces keep them), so words are never cut at the terminal edge.
func uiWrapMsg(msg string, w int) string {
	if w < 30 || uiStrW(msg)+4 <= w && !strings.Contains(msg, "\n") {
		return msg
	}
	var out []string
	for i, para := range strings.Split(msg, "\n") {
		lead := len(para) - len(strings.TrimLeft(para, " "))
		body := para[lead:]
		ind := 4 + lead
		if i == 0 {
			ind = 4
		}
		lines := uiWrapWords(body, max(w-ind, 20))
		pad := strings.Repeat(" ", lead)
		if i == 0 {
			pad = ""
		}
		out = append(out, pad+strings.Join(lines, "\n"+strings.Repeat(" ", ind)))
	}
	return strings.Join(out, "\n")
}

// uiClean prepares one line of file content / tool output for display:
// escape sequences and control characters removed, tabs expanded.
func uiClean(l string) string {
	return strings.ReplaceAll(sanitizeTerm(stripANSI(strings.ReplaceAll(l, "\t", "  "))), "\n", " ")
}

func (u *TermUI) Notice(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	u.rawWrite(fmt.Sprintf("  %s%s %s%s\n", u.T.fg(cGray, 90), u.T.G().dot, uiWrapMsg(sanitizeTerm(msg), termWidth()-1), u.T.sgr("0")))
}

func (u *TermUI) Warn(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	u.rawWrite(fmt.Sprintf("  %s%s %s%s\n", u.T.fg(cGold, 33), u.T.G().warn, uiWrapMsg(sanitizeTerm(msg), termWidth()-1), u.T.sgr("0")))
}

func (u *TermUI) Error(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	u.rawWrite(fmt.Sprintf("  %s%s %s%s\n", u.T.fg(cRed, 31), u.T.G().fail, uiWrapMsg(sanitizeTerm(msg), termWidth()-1), u.T.sgr("0")))
}

// SetMode records the effective permission mode for the status line.
func (u *TermUI) SetMode(mode string) {
	u.mu.Lock()
	u.mode = mode
	u.mu.Unlock()
}

// Jobs records the number of running background jobs for the status line.
func (u *TermUI) Jobs(n int) {
	u.mu.Lock()
	u.jobs = n
	u.mu.Unlock()
}

// Bell notifies the user that a long turn finished: BEL by default, an OSC
// desktop notification with notify=osc, nothing with notify=off or without a TTY.
func (u *TermUI) Bell() {
	if !isTTY(os.Stdout) {
		return
	}
	s := uiBellSeq(u.cfg.Notify, u.T.ascii, os.Getenv("TERM_PROGRAM"))
	if s == "" {
		return
	}
	u.mu.Lock()
	uiWrite(s) // nothing visible: col0 and the spinner are unaffected
	u.mu.Unlock()
}

func uiBellSeq(notify string, ascii bool, termProgram string) string {
	switch notify {
	case "off", "none", "false":
		return ""
	case "osc":
		if ascii { // the Linux console has no desktop notifications
			return "\a"
		}
		switch termProgram {
		case "iTerm.app", "WezTerm", "ghostty":
			return "\x1b]9;jazz: turno concluído\x07"
		}
		return "\x1b]777;notify;jazz;turno concluído\x07"
	}
	return "\a"
}

// modeLabel renders the permission mode in its own colour; plan stands out.
func (u *TermUI) modeLabel(mode string) string {
	t := u.T
	switch mode {
	case "plan":
		return t.sgr("1;7") + t.fg(cBlue, 34) + " plan " + t.sgr("0")
	case "yolo":
		return t.sgr("1") + t.fg(cRed, 31) + "yolo" + t.sgr("0")
	case "ask":
		return t.fg(cGold, 33) + "ask" + t.sgr("0")
	case "smart":
		return t.fg(cMagenta, 35) + "smart" + t.sgr("0")
	case "auto":
		return t.fg(cGreen, 32) + "auto" + t.sgr("0")
	}
	return t.fg(cCyan, 36) + mode + t.sgr("0")
}

// StatusLine renders the line printed above the prompt.
func (u *TermUI) StatusLine(model string, ctxTok, win, mcpReady, mcpTotal int) string {
	t := u.T
	g := t.G()
	u.mu.Lock()
	mode, jobs := u.mode, u.jobs
	u.mu.Unlock()
	if mode == "" {
		mode = u.cfg.EffMode()
	}
	gray, reset := t.fg(cGray, 90), t.sgr("0")
	parts := []string{gray + model, u.modeLabel(mode) + gray}
	if ctxTok > 0 {
		c := fmt.Sprintf("ctx %s/%s", fmtK(ctxTok), fmtK(win))
		if win > 0 && ctxTok*100/win >= 80 {
			c = t.fg(cGold, 33) + c + gray
		}
		parts = append(parts, c)
	}
	if mcpTotal > 0 {
		parts = append(parts, fmt.Sprintf("mcp %d/%d", mcpReady, mcpTotal))
	}
	if jobs > 0 {
		parts = append(parts, t.fg(cGold, 33)+fmt.Sprintf("jobs %d", jobs)+gray)
	}
	return "  " + strings.Join(parts, " "+g.dot+" ") + reset + "\n"
}

// ShowContext prints the /context breakdown: one bar per row, relative to the
// window. Rows whose label starts with a space are details of the row above
// and are not added to the total; a row labelled "total" overrides the sum.
func (u *TermUI) ShowContext(parts []ContextPart, win int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	u.rawWrite(uiContextView(u.T, parts, win))
}

func uiContextView(t Theme, parts []ContextPart, win int) string {
	g := t.G()
	total, explicit := 0, -1
	lw := 0
	var rows []ContextPart
	for _, p := range parts {
		if p.Label == "total" {
			explicit = p.Tokens
			continue
		}
		if !strings.HasPrefix(p.Label, " ") {
			total += p.Tokens
		}
		if n := utf8.RuneCountInString(p.Label); n > lw {
			lw = n
		}
		rows = append(rows, p)
	}
	if explicit >= 0 {
		total = explicit
	}
	lw = min(lw, 28)
	base := win
	if base <= 0 {
		base = total
	}
	const barW = 20
	var b strings.Builder
	gray, reset := t.fg(cGray, 90), t.sgr("0")
	for _, p := range rows {
		label := []rune(sanitizeTerm(p.Label))
		if len(label) > lw {
			label = append(label[:lw-1], '~')
		}
		pad := strings.Repeat(" ", lw-len(label))
		fill := 0
		if base > 0 && p.Tokens > 0 {
			fill = min((p.Tokens*barW+base-1)/base, barW)
		}
		col := t.fg(cMagenta, 35)
		if strings.HasPrefix(p.Label, " ") {
			col = t.fg(cCyan, 36)
		}
		pct := ""
		if base > 0 {
			pct = fmt.Sprintf("%5.1f%%", float64(p.Tokens)*100/float64(base))
		}
		fmt.Fprintf(&b, "  %s%s %s%s%s%s%s %6s %s%s%s\n", string(label), pad, col, strings.Repeat(g.block, fill), gray, strings.Repeat(g.shade, barW-fill), reset, fmtK(p.Tokens), gray, pct, reset)
	}
	if win > 0 {
		free := max(win-total, 0)
		fmt.Fprintf(&b, "  %susado %s de %s (%d%%) %s livre %s%s\n", gray, fmtK(total), fmtK(win), total*100/win, g.dot, fmtK(free), reset)
	} else {
		fmt.Fprintf(&b, "  %susado %s%s\n", gray, fmtK(total), reset)
	}
	return b.String()
}

// ---------- key handshake (approvals and questions) ----------

func uiClosedKeys() <-chan Key {
	c := make(chan Key)
	close(c)
	return c
}

// grabKeys gives a prompt exclusive access to the keys typed from now on.
// During a turn it asks the watcher for a fresh channel; otherwise it reads the
// key channel directly after dropping keys typed ahead. release must be called.
func (u *TermUI) grabKeys(ctx context.Context) (keys <-chan Key, release func()) {
	u.mu.Lock()
	done := u.watchDone
	u.mu.Unlock()
	direct := func() (<-chan Key, func()) {
		for { // typed ahead, before the prompt existed: never an answer
			select {
			case _, ok := <-u.keys.Keys:
				if !ok {
					return uiClosedKeys(), func() {}
				}
				continue
			default:
			}
			return u.keys.Keys, func() {}
		}
	}
	if done == nil {
		return direct()
	}
	ch := make(chan Key, 64)
	select {
	case u.grabCh <- ch:
		return ch, func() {
			select {
			case u.grabCh <- nil:
			case <-done:
			}
		}
	case <-done:
		return direct()
	case <-ctx.Done():
		return uiClosedKeys(), func() {}
	}
}

// uiReadKey waits for a key; ok is false on EOF or when ctx is cancelled.
func uiReadKey(ctx context.Context, keys <-chan Key) (Key, bool) {
	select {
	case k, ok := <-keys:
		if ok && k.Code == KCtrlD {
			return k, false // the key reader sends Ctrl+D right before EOF
		}
		return k, ok
	case <-ctx.Done():
		return Key{}, false
	}
}

// readLine is a minimal line editor for prompts (approval answers, feedback).
// It redraws the whole line on each key. ok is false on Esc/Ctrl+C/EOF/ctx.
// Keys arriving before since+uiApproveGrace are ignored.
func (u *TermUI) readLine(ctx context.Context, keys <-chan Key, prompt string, since time.Time) (string, bool) {
	var buf []rune
	pw := utf8.RuneCountInString(stripANSI(prompt))
	draw := func() {
		vis := buf
		if room := termWidth() - 1 - pw; room > 4 && len(vis) > room {
			vis = vis[len(vis)-room:]
		}
		u.print("\r\x1b[K" + prompt + string(vis))
	}
	draw()
	for {
		k, ok := uiReadKey(ctx, keys)
		if !ok {
			u.print("\n")
			return "", false
		}
		if time.Since(since) < uiApproveGrace {
			continue
		}
		switch k.Code {
		case KEnter:
			u.print("\n")
			return string(buf), true
		case KEsc, KCtrlC:
			u.print("\n")
			return "", false
		case KBackspace:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case KCtrlU:
			buf = nil
		case KCtrlW:
			buf = uiDelWord(buf)
		case KRune:
			buf = append(buf, k.R)
		case KPaste:
			buf = append(buf, []rune(strings.Join(strings.Fields(sanitizeTerm(k.Text)), " "))...)
		default:
			continue
		}
		draw()
	}
}

func uiDelWord(buf []rune) []rune {
	p := len(buf)
	for p > 0 && unicode.IsSpace(buf[p-1]) {
		p--
	}
	for p > 0 && !unicode.IsSpace(buf[p-1]) {
		p--
	}
	return buf[:p]
}

// uiWrap splits s into lines of at most w runes (hard wrap: a command is shown
// exactly, character by character).
func uiWrap(s string, w int) []string {
	w = max(w, 10)
	var out []string
	r := []rune(s)
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	return append(out, string(r))
}

// Approve asks the user to confirm a tool call (see contract.go for the rules).
// The whole detail is shown in a box with control characters made visible
// (bug 10). Normal prompts are a numbered menu (↑↓ + enter, digits, or the
// letters s/t/e/n). A dangerous action (reason != "") needs "sim" plus Enter
// and never offers "always". Option 3 / [e] denies and returns what the user
// typed as feedback (G11).
func (u *TermUI) Approve(ctx context.Context, tool, detail, reason string) (Approval, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	u.mu.Lock()
	rule := u.alwaysRule
	u.alwaysRule = ""
	// text being typed when the prompt opens: answers need a whole word + Enter
	typing := u.typed != ""
	lastKey := u.lastKey
	u.mu.Unlock()
	u.StopThinking()
	t := u.T
	g := t.G()
	w := termWidth() - 1
	bw := max(min(w, 100), 24)
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	border := t.fg(cGold, 33)
	if reason != "" {
		border = t.fg(cRed, 31)
	}
	question := map[string]string{"bash": "Rodar este comando?", "write": "Escrever este arquivo?", "edit": "Aplicar esta edição?"}[tool]
	if question == "" {
		question = "Usar " + uiToolLabel(tool) + "?"
	}
	full := []rune(u.uiDetail(detail))
	shown, more := full, 0
	if len(full) > uiApproveMax {
		shown, more = full[:uiApproveMax], len(full)-uiApproveMax
	}
	row := func(s string) string {
		pad := max(bw-4-uiVisLen(s), 0)
		return border + g.v + reset + " " + s + reset + strings.Repeat(" ", pad) + " " + border + g.v + reset + "\n"
	}
	cmdLines := func(rs []rune) string {
		var b strings.Builder
		for _, l := range uiWrap(string(rs), bw-4) {
			b.WriteString(row(l))
		}
		return b.String()
	}
	var b strings.Builder
	title := " " + t.sgr("1") + uiToolLabel(tool) + reset + border + " "
	if reason != "" {
		title = " " + t.sgr("1") + uiToolLabel(tool) + " " + g.dot + " perigoso" + reset + border + " "
	}
	b.WriteString(border + g.tl + g.h + title + strings.Repeat(g.h, max(bw-4-uiVisLen(title), 0)) + g.h + g.tr + reset + "\n")
	if len(full) > 0 {
		b.WriteString(cmdLines(shown))
	}
	if more > 0 {
		b.WriteString(row(t.fg(cGold, 33) + fmt.Sprintf("%s +%d caracteres %s v ver tudo", g.ell, more, g.dot)))
	}
	if reason != "" {
		b.WriteString(row(""))
		for i, l := range uiWrapWords(oneLine(sanitizeTerm(reason), 300), bw-4-8) {
			lead := "        "
			if i == 0 {
				lead = "perigo: "
			}
			b.WriteString(row(t.fg(cRed, 31) + t.sgr("1") + lead + l))
		}
	}
	b.WriteString(border + g.bl + strings.Repeat(g.h, bw-2) + g.br + reset + "\n")
	b.WriteString("  " + t.sgr("1") + question + reset + "\n")

	always := "Sim, e não perguntar de novo"
	switch tool {
	case "bash":
		always += " pra comandos que começam igual"
	case "write", "edit":
		always += " nesta pasta"
	}
	if rule != "" {
		always = "Sim, e não perguntar de novo pra " + oneLine(visibleDetail(rule), 120)
	}
	opts := []string{"Sim", always, "Não, e dizer o que fazer"}
	if reason != "" {
		b.WriteString("  " + dim + "digite " + reset + t.sgr("1") + "sim" + reset + dim + " + enter pra confirmar " + g.dot + " e + enter: não, e dizer o que fazer " + g.dot + " outra coisa nega" + reset + "\n")
	} else if typing || u.keys == nil {
		for i, o := range opts {
			b.WriteString(clipVisible(fmt.Sprintf("    %d. %s", i+1, o), w) + "\n")
		}
	}
	u.mu.Lock()
	u.endText()
	u.gap()
	u.rawWrite(b.String())
	u.mu.Unlock()
	if u.keys == nil {
		u.print("  " + dim + "não (sem terminal interativo)" + reset + "\n")
		return Deny, ""
	}
	keys, release := u.grabKeys(ctx)
	defer release()
	since := time.Now()
	showAll := func() {
		if more > 0 {
			var b strings.Builder
			for _, l := range uiWrap(string(full[uiApproveMax:]), w-6) {
				b.WriteString("    " + dim + g.bar + reset + " " + l + "\n")
			}
			u.print("\r\x1b[K" + b.String())
			more = 0
		}
	}
	feedback := func() (Approval, string) {
		fb, ok := u.readLine(ctx, keys, "  "+t.fg(cGold, 33)+"o que fazer em vez disso?"+reset+" "+g.prompt+" ", time.Time{})
		if !ok {
			return Deny, ""
		}
		return Deny, strings.TrimSpace(fb)
	}
	denied := func() { u.print("  " + t.fg(cRed, 31) + g.tool + reset + " não\n") }
	if reason != "" {
		ask := "  " + t.fg(cRed, 31) + "confirmar?" + reset + " " + g.prompt + " "
		for {
			line, ok := u.readLine(ctx, keys, ask, since)
			if !ok {
				return Deny, ""
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "s", "sim", "y", "yes":
				return Allow, ""
			case "e":
				return feedback()
			case "v":
				showAll()
				continue
			}
			denied()
			return Deny, ""
		}
	}
	if typing {
		// steering text was being typed: a stray letter must not answer
		ask := "  " + t.fg(cGold, 33) + "você estava digitando: responda (1-3) e tecle enter" + reset + " " + g.prompt + " "
		for {
			line, ok := u.readLine(ctx, keys, ask, since)
			if !ok {
				return Deny, ""
			}
			switch l := strings.ToLower(strings.TrimSpace(line)); l {
			case "1", "s", "sim", "y", "yes":
				return Allow, ""
			case "2", "t", "sempre":
				return AllowAlways, ""
			case "3", "e":
				return feedback()
			case "v":
				showAll()
				continue
			case "", "n", "não", "nao", "no":
			default:
				// the user kept typing an instruction: deny and pass it on
				denied()
				return Deny, strings.TrimSpace(line)
			}
			denied()
			return Deny, ""
		}
	}
	sel := 0
	menu := func(redraw bool) {
		var b strings.Builder
		if redraw {
			fmt.Fprintf(&b, "\x1b[%dA", len(opts)+1)
		}
		for i, o := range opts {
			l := fmt.Sprintf("    %d. %s", i+1, o)
			if i == sel {
				l = "  " + t.fg(cAccent, 36) + t.sgr("1") + g.ptr + " " + fmt.Sprintf("%d. %s", i+1, o) + reset
			}
			b.WriteString("\r" + clipVisible(l, w) + "\x1b[K\n")
		}
		b.WriteString("\r" + clipVisible("  "+dim+g.up+g.down+" escolhe "+g.dot+" enter confirma "+g.dot+" 1-3 atalho "+g.dot+" esc nega"+reset, w) + "\x1b[K\n")
		u.print(b.String())
	}
	// done collapses the menu into the chosen answer.
	done := func(i int) {
		c := t.fg(cGreen, 32)
		if i == 2 || i < 0 {
			c = t.fg(cRed, 31)
		}
		label := "não"
		if i >= 0 {
			label = opts[i]
		}
		u.print(fmt.Sprintf("\x1b[%dA\r\x1b[J  %s%s%s %s\n", len(opts)+1, c, g.tool, reset, clipVisible(label, w-4)))
	}
	menu(false)
	prev := lastKey
	for {
		k, ok := uiReadKey(ctx, keys)
		if !ok {
			done(-1)
			return Deny, ""
		}
		now := time.Now()
		gap := now.Sub(prev) // since the last typed character, not the last arrow
		if k.Code == KRune || k.Code == KPaste {
			prev = now
		}
		if now.Sub(since) < uiApproveGrace {
			continue // typed before the prompt could be read (bug 11)
		}
		switch k.Code {
		case KEsc, KCtrlC:
			done(-1)
			return Deny, ""
		case KUp, KDown:
			if k.Code == KUp {
				sel = (sel + len(opts) - 1) % len(opts)
			} else {
				sel = (sel + 1) % len(opts)
			}
			menu(true)
			continue
		}
		if gap < uiApproveIdle {
			continue // still typing: not an answer
		}
		choice := -2
		switch k.Code {
		case KEnter:
			choice = sel
		case KRune:
			switch unicode.ToLower(k.R) {
			case '1', 's', 'y':
				choice = 0
			case '2', 't': // not 'a': the most common letter in Portuguese text
				choice = 1
			case '3', 'e':
				choice = 2
			case 'n':
				choice = -1
			case 'v':
				if more > 0 {
					showAll()
					menu(false)
				}
			}
		}
		switch choice {
		case 0:
			done(0)
			return Allow, ""
		case 1:
			done(1)
			return AllowAlways, ""
		case 2:
			done(2)
			return feedback()
		case -1:
			done(-1)
			return Deny, ""
		}
	}
}

// ApproveHint sets the rule that [t] "always" would save, shown by the next Approve.
func (u *TermUI) ApproveHint(rule string) {
	u.mu.Lock()
	u.alwaysRule = rule
	u.mu.Unlock()
}

// Ask shows a question and waits for one of the runes in keys (case-insensitive).
// It returns the lower-case rune pressed, or 0 on Esc/Ctrl+C/EOF/no terminal.
func (u *TermUI) Ask(question, keys string) rune {
	u.StopThinking()
	u.mu.Lock()
	u.endText()
	u.ensureLine()
	u.rawWrite("  " + u.T.fg(cAccent, 36) + u.T.sgr("1") + "?" + u.T.sgr("0") + " " + question + " " + u.T.fg(cAccent, 36) + u.T.G().prompt + u.T.sgr("0") + " ")
	u.mu.Unlock()
	if u.keys == nil {
		u.print("\n")
		return 0
	}
	ctx := context.Background()
	ch, release := u.grabKeys(ctx)
	defer release()
	since := time.Now()
	valid := strings.ToLower(keys)
	for {
		k, ok := uiReadKey(ctx, ch)
		if !ok {
			u.print("\n")
			return 0
		}
		if time.Since(since) < uiApproveGrace {
			continue
		}
		switch k.Code {
		case KEsc, KCtrlC:
			u.print("\n")
			return 0
		case KEnter: // only when the caller lists "\r" among the keys (fast paths)
			if strings.ContainsRune(valid, '\r') {
				u.print("\n")
				return '\r'
			}
		case KRune:
			if r := unicode.ToLower(k.R); strings.ContainsRune(valid, r) {
				u.print(string(r) + "\n")
				return r
			}
		}
	}
}

func (u *TermUI) TurnEnd(st TurnStats) {
	u.StopThinking()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.gap()
	u.rawWrite(uiTurnLine(u.T, st))
}

func uiTurnLine(t Theme, st TurnStats) string {
	g := t.G()
	parts := []string{st.Dur.Round(100 * time.Millisecond).String()}
	if st.Tools > 0 {
		parts = append(parts, fmt.Sprintf("%d ferramenta%s", st.Tools, plural(st.Tools)))
	}
	if st.Usage.In+st.Usage.Out > 0 {
		p := fmt.Sprintf("%s %s %s %s", g.up, fmtK(st.Usage.In), g.down, fmtK(st.Usage.Out))
		if st.Usage.CacheRead > 0 && st.Usage.In > 0 {
			p += fmt.Sprintf(" cache %d%%", st.Usage.CacheRead*100/st.Usage.In)
		}
		parts = append(parts, p)
	}
	if st.Cost > 0 {
		parts = append(parts, fmtCost(st.Cost))
	}
	if st.Win > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d%%", st.Ctx*100/st.Win))
	}
	return fmt.Sprintf("  %s%s %s%s\n\n", t.fg(cGray, 90), g.star, strings.Join(parts, " "+g.dot+" "), t.sgr("0"))
}

// fmtCost renders a USD amount with enough digits for cheap turns.
func fmtCost(c float64) string {
	switch {
	case c >= 1:
		return fmt.Sprintf("$%.2f", c)
	case c >= 0.01:
		return fmt.Sprintf("$%.3f", c)
	}
	return fmt.Sprintf("$%.4f", c)
}

// WatchTurn reads keys while the agent works: ESC/Ctrl+C cancel, typed text is
// shown on the spinner line and Enter queues it for the agent (steer). A nil
// steer (shell escape, /compact) keeps the text for the next prompt instead.
// Whatever is typed but not sent when the returned stop func runs is handed to
// the next Editor.ReadLine (bug 39). Prompts (Approve/Ask) borrow keys through
// grabKeys; keys are never forwarded with a blocking send (bug 13).
func (u *TermUI) WatchTurn(cancel func(), steer func(string)) func() {
	if u.keys == nil {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	nl := u.T.G().nl
	u.mu.Lock()
	buf := u.leftover
	u.leftover = nil
	u.watchDone = done
	u.typed = uiTyped(buf, nl)
	u.mu.Unlock()
	keys := u.keys.Keys
	save := func() {
		u.mu.Lock()
		u.leftover = buf
		u.typed = ""
		u.mu.Unlock()
	}
	safeGo(func() {
		defer close(done)
		var grab chan Key
		defer func() {
			if grab != nil {
				close(grab) // a prompt still waiting sees EOF and denies (bug 41)
			}
		}()
		for {
			select {
			case <-stop:
				save()
				return
			case g := <-u.grabCh:
				grab = g
			case k, ok := <-keys:
				if !ok {
					save()
					cancel()
					return
				}
				if grab != nil {
					select {
					case grab <- k:
					default: // the prompt is not reading: drop, never block
					}
					continue
				}
				// typing outside a prompt (steering): the next prompt waits
				// for a pause before a single key counts as an answer
				u.mu.Lock()
				u.lastKey = time.Now()
				u.mu.Unlock()
				changed := true
				switch k.Code {
				case KEsc, KCtrlC:
					u.StopThinking()
					u.Warn("interrompendo…")
					cancel()
					changed = false
				case KEnter:
					if s := strings.TrimSpace(string(buf)); s != "" && steer != nil {
						steer(s)
						buf = nil
						u.Notice(u.T.G().arrow + " na fila pro agente: " + oneLine(s, 80))
					}
				case KAltEnter:
					buf = append(buf, '\n')
				case KBackspace:
					if len(buf) > 0 {
						buf = buf[:len(buf)-1]
					}
				case KCtrlU:
					buf = nil
				case KCtrlW:
					buf = uiDelWord(buf)
				case KRune:
					if len(buf) == 0 && u.offerPick(k.R) {
						changed = false
						break
					}
					buf = append(buf, k.R)
				case KPaste:
					buf = append(buf, []rune(strings.ReplaceAll(strings.ReplaceAll(k.Text, "\r\n", "\n"), "\r", "\n"))...)
				default:
					changed = false
				}
				if changed {
					u.mu.Lock()
					u.typed = uiTyped(buf, nl)
					u.mu.Unlock()
				}
			}
		}
	})
	return func() {
		close(stop)
		<-done
		u.mu.Lock()
		if u.watchDone == done {
			u.watchDone = nil
		}
		u.mu.Unlock()
	}
}

// uiTyped is the one-line, sanitized form of the text being typed.
func uiTyped(buf []rune, nl string) string {
	if len(buf) == 0 {
		return ""
	}
	s := strings.ReplaceAll(string(buf), "\n", nl)
	return sanitizeTerm(strings.ReplaceAll(s, "\t", " "))
}

// takeLeftover returns (and clears) text typed during the last turn.
func (u *TermUI) takeLeftover() []rune {
	u.mu.Lock()
	defer u.mu.Unlock()
	l := u.leftover
	u.leftover = nil
	return l
}

// ---------- helpers ----------

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func fmtK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// uiEscEnd returns the index just past the escape sequence starting at s[i]
// (s[i] == ESC): CSI up to its final byte, OSC/DCS/APC/PM up to BEL or ST,
// charset selection (ESC ( B) as three bytes, anything else as two (bug 57).
func uiEscEnd(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	switch s[j] {
	case '[':
		j++
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		return min(j+1, len(s))
	case ']', 'P', '_', '^', 'X':
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b {
				if j+1 < len(s) && s[j+1] == '\\' {
					return j + 2
				}
				return j // ESC aborts the string and starts the next sequence
			}
		}
		return len(s)
	case '(', ')', '*', '+', '-', '.', '/', '#', '%':
		return min(j+2, len(s))
	}
	return j + 1
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = uiEscEnd(s, i)
			continue
		}
		if s[i] < 0x20 && s[i] != '\t' || s[i] == 0x7f {
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// clipVisible truncates to w visible columns. CSI sequences (our colours) are
// kept; other escape sequences (OSC, charset selection) are dropped.
func clipVisible(s string, w int) string {
	if w <= 0 {
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
		if vis >= w-1 && i+n < len(s) && hasVisibleAfter(s[i:]) {
			b.WriteString("…\x1b[0m")
			return b.String()
		}
		b.WriteRune(r)
		vis++
		i += n
	}
	return b.String()
}

// hasVisibleAfter counts runes, not bytes (bug 53).
func hasVisibleAfter(s string) bool {
	return utf8.RuneCountInString(stripANSI(s)) > 1
}

// uiSan removes terminal control sequences from streamed text (bug 32). Its
// state survives across chunks, so a sequence split between chunks is dropped too.
type uiSan struct{ st uint8 }

const (
	uiSanText = iota
	uiSanEsc
	uiSanCSI
	uiSanStr    // OSC/DCS/APC/PM body
	uiSanStrEsc // ESC inside a string
	uiSanCharset
)

func (z *uiSan) filter(in string) string {
	if z.st == uiSanText && !uiNeedsSan(in) {
		return in
	}
	var b strings.Builder
	for _, r := range in {
		switch z.st {
		case uiSanEsc:
			z.afterEsc(r)
			continue
		case uiSanCSI:
			if r >= 0x40 && r <= 0x7e {
				z.st = uiSanText
			}
			continue
		case uiSanStr:
			if r == 0x07 {
				z.st = uiSanText
			} else if r == 0x1b {
				z.st = uiSanStrEsc
			}
			continue
		case uiSanStrEsc:
			if r == '\\' {
				z.st = uiSanText
			} else {
				z.afterEsc(r)
			}
			continue
		case uiSanCharset:
			z.st = uiSanText
			continue
		}
		switch {
		case r == 0x1b:
			z.st = uiSanEsc
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
		case r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (z *uiSan) afterEsc(r rune) {
	switch r {
	case '[':
		z.st = uiSanCSI
	case ']', 'P', '_', '^', 'X':
		z.st = uiSanStr
	case '(', ')', '*', '+', '-', '.', '/', '#', '%':
		z.st = uiSanCharset
	case 0x1b:
		z.st = uiSanEsc
	default:
		z.st = uiSanText
	}
}

// uiNeedsSan is a fast pre-check: only ASCII controls, C1 (UTF-8 lead 0xc2) and
// bidi controls (lead 0xe2) can need filtering.
func uiNeedsSan(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f || c == 0xc2 || c == 0xe2 {
			return true
		}
	}
	return false
}

// gapLocked is gap for callers outside the UI lock (main's prompt loop).
func (u *TermUI) gapLocked() {
	u.mu.Lock()
	u.gap()
	u.mu.Unlock()
}

// gap leaves one blank line before a new block (message, tool call, prompt).
func (u *TermUI) gap() {
	u.liveCommit()
	if !u.col0 {
		u.rawWrite("\n")
	}
	if u.nl < 2 {
		u.rawWrite("\n")
	}
}

// spinLabelText turns the spinner label into its display form.
func (u *TermUI) spinLabelText() string {
	switch l := u.spinLabel; l {
	case "", "pensando", "raciocinando":
		n := int(time.Since(u.spinStart)/(4*time.Second)) + int(u.spinStart.UnixNano()/7%int64(len(uiSpinWords)))
		return uiSpinWords[(n%len(uiSpinWords)+len(uiSpinWords))%len(uiSpinWords)]
	default:
		r, n := utf8.DecodeRuneInString(l)
		return string(unicode.ToUpper(r)) + l[n:]
	}
}

// toolHead is the "Name(detail)" part of a tool line, name in bold.
func (u *TermUI) toolHead(name, detail string) string {
	t := u.T
	if name == "todo" {
		return t.sgr("1") + "Atualizou o plano" + t.sgr("0")
	}
	h := t.sgr("1") + uiToolLabel(name) + t.sgr("0")
	if detail != "" {
		h += "(" + u.uiDetail(detail) + ")"
	}
	return h
}

// uiDiffView renders edit hunks: line numbers, red "-" and green "+" lines
// with a subtle background tint (256/truecolor) padded to the width.
func uiDiffView(t Theme, hunks []editHunk, w, maxLines int) string {
	var b strings.Builder
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	shown := 0
	line := func(n int, sign string, fg, bg string, text string) {
		body := fmt.Sprintf("%s%4d %s%s %s", dim, n, fg, sign, uiClean(text))
		vis := 5 + 2 + uiVisLen(uiClean(text))
		pad := ""
		if bg != "" && vis < w-5 {
			pad = strings.Repeat(" ", w-5-vis)
		}
		b.WriteString(clipVisible("     "+bg+body+pad, w) + reset + "\n")
		shown++
	}
	for _, h := range hunks {
		for i, l := range h.Old {
			if shown >= maxLines {
				return b.String()
			}
			line(h.Line+i, "-", t.fg(cRed, 31), t.bg(cDelBg, 52), l)
		}
		for i, l := range h.New {
			if shown >= maxLines {
				return b.String()
			}
			line(h.Line+i, "+", t.fg(cGreen, 32), t.bg(cAddBg, 22), l)
		}
	}
	return b.String()
}

// uiPl picks the singular or plural word for n.
func uiPl(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ExpandLast prints the whole output of the last tool call (ctrl+o, /expand).
func (u *TermUI) ExpandLast() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	t, g := u.T, u.T.G()
	lo := u.lastOut
	if strings.TrimSpace(lo.text) == "" {
		u.rawWrite("  " + t.fg(cGray, 90) + g.dot + " nenhuma saída de ferramenta pra expandir" + t.sgr("0") + "\n")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s%s  saída completa %s %s%s\n", t.fg(cGray, 90), g.res, g.dot, sanitizeTerm(lo.label), t.sgr("0"))
	lines := strings.Split(strings.TrimRight(lo.text, "\n"), "\n")
	const maxLines = 400
	for i, l := range lines {
		if i == maxLines {
			fmt.Fprintf(&b, "     %s%s +%d linhas%s\n", t.fg(cGray, 90), g.ell, len(lines)-maxLines, t.sgr("0"))
			break
		}
		b.WriteString("     " + uiClean(l) + "\n")
	}
	u.rawWrite(b.String())
}

// Footer is the one-line hint under the input box: mode, model, context use,
// MCP/jobs and the shortcut hint.
func (u *TermUI) Footer(model string, ctxTok, win, mcpReady, mcpTotal int) string {
	t, g := u.T, u.T.G()
	u.mu.Lock()
	mode, jobs := u.mode, u.jobs
	u.mu.Unlock()
	if mode == "" {
		mode = u.cfg.EffMode()
	}
	gray, reset := t.fg(cGray, 90), t.sgr("0")
	parts := []string{uiModeTag(t, mode) + gray, model}
	if ctxTok > 0 && win > 0 {
		pct := ctxTok * 100 / win
		c := fmt.Sprintf("ctx %d%%", pct)
		if pct >= 80 {
			c = t.fg(cGold, 33) + c + gray
		}
		parts = append(parts, c)
	}
	if mcpTotal > 0 {
		parts = append(parts, fmt.Sprintf("mcp %d/%d", mcpReady, mcpTotal))
	}
	if jobs > 0 {
		parts = append(parts, t.fg(cGold, 33)+fmt.Sprintf("jobs %d", jobs)+gray)
	}
	parts = append(parts, "? atalhos")
	return "  " + gray + strings.Join(parts, " "+g.dot+" ") + reset
}

// uiSpinWords rotate while the model is thinking.
var uiSpinWords = []string{"Pensando", "Improvisando", "Afinando", "Swingando", "Compondo", "Arranjando", "Solando", "Harmonizando", "Batucando", "Ensaiando"}

// uiPreviewMax is how many lines of tool output show under the result line.
const uiPreviewMax = 3

// uiLastOut is the last tool output, for ctrl+o / /expand.
type uiLastOut struct{ label, text string }
