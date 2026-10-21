package main

// sanitize.go: content sanitization against hidden and steganographic
// instructions (guardrails, part 3). Everything that reaches the model from
// an untrusted source (file reads, bash output, fetched pages, MCP results,
// project instruction files, pasted text) goes through sanitizeForModel:
//
//   - invisible Unicode is removed: zero-width characters (U+200B–U+200F,
//     U+2060–U+2064, U+FEFF), bidi controls (U+202A–U+202E, U+2066–U+2069),
//     Unicode tag characters (U+E0000–U+E007F, "ASCII smuggling": their hidden
//     ASCII is decoded and shown to the user), and variation selectors used as
//     carriers (runs, or after plain ASCII). Emoji sequences keep their ZWJ
//     and VS16, so 👨‍👩‍👧 and ❤️ pass unchanged;
//   - terminal escape sequences and C0/C1 controls are removed (they were only
//     cleaned for display before);
//   - Cyrillic/Greek look-alike letters mixed into a Latin word (сurl with a
//     Cyrillic с) are made visible as \u{0441};
//   - hidden HTML (display:none, visibility:hidden, font-size:0, opacity:0,
//     white-on-white, the hidden attribute, aria-hidden) is removed from
//     fetched pages and MCP results; in file reads it is only scanned.
//
// What was removed is counted ("N caracteres invisíveis removidos") in the
// tool result and in the UI, and hidden content that carries instructions is
// flagged like any prompt injection (guardrails.go). Config sanitize:
// off | strip (default) | strict (strict also escapes every look-alike letter
// and removes hidden HTML from file reads too).

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// snReport is what sanitizeForModel removed.
type snReport struct {
	Invisible  int    // zero-width, bidi, tag and carrier variation selectors
	Bidi       int    // bidi controls among Invisible
	Tags       string // ASCII decoded from Unicode tag characters
	Controls   int    // terminal escapes and control characters
	Homoglyphs int    // look-alike letters made visible
	Hidden     []string
}

// suspect returns an injection label when what was removed looks deliberate.
func (r snReport) suspect() string {
	switch {
	case r.Tags != "":
		return "texto invisível (caracteres de tag Unicode)"
	case r.Bidi > 0:
		return "controle bidi escondendo texto"
	case r.Invisible >= 4:
		return "texto com caracteres invisíveis (zero-width)"
	case r.Homoglyphs > 0:
		return "letras parecidas (homóglifos) misturadas em palavras"
	}
	for _, h := range r.Hidden {
		if injScan(h) != "" || grCommentBad.MatchString(h) {
			return "texto HTML escondido com instruções"
		}
	}
	return ""
}

// note is the line appended to the tool result ("" = nothing removed).
func (r snReport) note() string {
	var parts []string
	if r.Invisible > 0 {
		parts = append(parts, fmt.Sprintf("%d caracteres invisíveis removidos", r.Invisible))
	}
	if r.Tags != "" {
		parts = append(parts, fmt.Sprintf("texto escondido em caracteres de tag: %q", oneLine(r.Tags, 200)))
	}
	if r.Homoglyphs > 0 {
		parts = append(parts, fmt.Sprintf("%d letras parecidas marcadas como \\u{…}", r.Homoglyphs))
	}
	if len(r.Hidden) > 0 {
		parts = append(parts, fmt.Sprintf("%d blocos de HTML escondido removidos", len(r.Hidden)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[jazz: " + strings.Join(parts, "; ") + "]"
}

// snMode is the configured sanitizer mode.
func snMode(cfg *Config) string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_SANITIZE"))); v != "" {
		return v
	}
	if cfg != nil {
		switch m := strings.ToLower(strings.TrimSpace(cfg.Sanitize)); m {
		case "off", "strict":
			return m
		}
	}
	return "strip"
}

func snIsEmoji(r rune) bool {
	return r >= 0x1F000 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF || r >= 0x2190 && r <= 0x21FF || r >= 0x2300 && r <= 0x23FF ||
		r >= 0x2B00 && r <= 0x2BFF || r == 0x00A9 || r == 0x00AE || r == 0x203C || r == 0x2049 || r == 0x2122 || r == 0x2139 || r >= 0x3030 && r <= 0x303D || r == 0x3297 || r == 0x3299
}

// snConfusable maps Cyrillic/Greek letters that look like Latin ones.
var snConfusable = map[rune]bool{
	'а': true, 'с': true, 'е': true, 'о': true, 'р': true, 'х': true, 'у': true, 'і': true, 'ј': true, 'ѕ': true, 'һ': true, 'ԁ': true, 'ԛ': true, 'ԝ': true,
	'А': true, 'В': true, 'С': true, 'Е': true, 'Н': true, 'І': true, 'Ј': true, 'К': true, 'М': true, 'О': true, 'Р': true, 'Ѕ': true, 'Т': true, 'Х': true, 'У': true,
	'α': true, 'ο': true, 'ρ': true, 'ν': true, 'ι': true, 'κ': true, 'τ': true, 'χ': true, 'ε': true,
	'Α': true, 'Β': true, 'Ε': true, 'Ζ': true, 'Η': true, 'Ι': true, 'Κ': true, 'Μ': true, 'Ν': true, 'Ο': true, 'Ρ': true, 'Τ': true, 'Υ': true, 'Χ': true,
}

// sanitizeForModel removes invisible and control content from s (see the
// file comment). mode "off" returns s unchanged.
func sanitizeForModel(s, mode string) (string, snReport) {
	var rep snReport
	if mode == "off" || s == "" || !snNeeds(s) {
		return s, rep
	}
	var b strings.Builder
	b.Grow(len(s))
	var tags strings.Builder
	prev := rune(0)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		next := rune(0)
		if i+size < len(s) {
			next, _ = utf8.DecodeRuneInString(s[i+size:])
		}
		switch {
		case r == 0x1b: // terminal escape sequence
			j := skipEscape(s, i)
			rep.Controls++
			i = j + 1
			continue
		case r < 0x20 && r != '\n' && r != '\t' && r != '\r' || r == 0x7f || r >= 0x80 && r <= 0x9f:
			rep.Controls++
		case r == 0xFEFF && i == 0: // a byte-order mark: drop quietly
		case r >= 0xE0000 && r <= 0xE007F:
			rep.Invisible++
			if c := r - 0xE0000; c >= 0x20 && c < 0x7f {
				tags.WriteRune(c)
			}
		case r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069:
			rep.Invisible++
			rep.Bidi++
		case r == 0x200D && (snIsEmoji(prev) || prev == 0xFE0F) && snIsEmoji(next):
			b.WriteRune(r) // emoji ZWJ sequence
		case r >= 0x200B && r <= 0x200F || r >= 0x2060 && r <= 0x2064 || r == 0xFEFF:
			rep.Invisible++
		case r >= 0xE0100 && r <= 0xE01EF:
			rep.Invisible++
		case r >= 0xFE00 && r <= 0xFE0F:
			if (prev < 0x80 && next != 0x20E3) || prev >= 0xFE00 && prev <= 0xFE0F || prev == 0 { // #️⃣ keycaps keep theirs
				rep.Invisible++ // a carrier: after ASCII or in a run
			} else {
				b.WriteRune(r)
			}
		case r == utf8.RuneError && size == 1:
			b.WriteByte(s[i]) // keep invalid bytes as they are
		default:
			b.WriteRune(r)
		}
		prev = r
		i += size
	}
	out := b.String()
	rep.Tags = strings.TrimSpace(tags.String())
	out, rep.Homoglyphs = snHomoglyphs(out, mode == "strict")
	return out, rep
}

// snNeeds is a fast pre-check: only non-ASCII or control bytes can need work.
func snNeeds(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c < 0x20 && c != '\n' && c != '\t' && c != '\r' || c == 0x7f {
			return true
		}
	}
	return false
}

// snHomoglyphs escapes Cyrillic/Greek look-alikes inside words that also
// contain ASCII letters (all words in strict mode). Real Cyrillic or Greek
// text (no Latin letters in the word) is left alone.
func snHomoglyphs(s string, strict bool) (string, int) {
	has := false
	for _, r := range s {
		if snConfusable[r] {
			has = true
			break
		}
	}
	if !has {
		return s, 0
	}
	n := 0
	var b strings.Builder
	word := func(w string) {
		latin, conf := false, false
		for _, r := range w {
			if r < 0x80 && unicode.IsLetter(r) {
				latin = true
			}
			if snConfusable[r] {
				conf = true
			}
		}
		if !conf || !latin && !strict {
			b.WriteString(w)
			return
		}
		for _, r := range w {
			if snConfusable[r] {
				fmt.Fprintf(&b, `\u{%04x}`, r)
				n++
			} else {
				b.WriteRune(r)
			}
		}
	}
	start := -1
	for i, r := range s {
		in := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
		switch {
		case in && start < 0:
			start = i
		case !in && start >= 0:
			word(s[start:i])
			start = -1
			b.WriteRune(r)
		case !in:
			b.WriteRune(r)
		}
	}
	if start >= 0 {
		word(s[start:])
	}
	return b.String(), n
}

// ---------- hidden HTML ----------

var (
	reOpenTag      = regexp.MustCompile(`(?is)<([a-z][a-z0-9]*)\b([^>]*)>`)
	reHiddenAttr   = regexp.MustCompile(`(?is)(^|\s)hidden(\s|=|$)|aria-hidden\s*=\s*["']?true`)
	reStyleAttr    = regexp.MustCompile(`(?is)\bstyle\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reStyleHidden  = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden|font-size\s*:\s*0(px|em|rem|pt|%)?\s*(;|$)|opacity\s*:\s*0(\.0+)?\s*(;|$)|(left|top|text-indent)\s*:\s*-[0-9]{4,}`)
	reStyleColor   = regexp.MustCompile(`(?i)(^|[;\s])color\s*:\s*(#fff(fff)?|white|rgb\(\s*255\s*,\s*255\s*,\s*255\s*\))`)
	reStyleBgWhite = regexp.MustCompile(`(?i)background(-color)?\s*:\s*(#fff(fff)?|white|rgb\(\s*255\s*,\s*255\s*,\s*255\s*\))`)
	reHTMLAttrText = regexp.MustCompile(`(?is)\b(alt|title|aria-label|data-[a-z-]+)\s*=\s*["']([^"']{12,})["']`)
)

// snHiddenHTML removes elements the browser would not show and returns the
// cleaned HTML plus the text of what was removed (also HTML comments and
// long alt/title attributes, which are kept but returned for scanning).
func snHiddenHTML(s string, remove bool) (string, []string) {
	var hidden []string
	lower := strings.ToLower(s)
	var b strings.Builder
	pos := 0
	for {
		loc := snNextHidden(s, pos)
		if loc == nil {
			break
		}
		start, openEnd := loc[0], loc[1]
		tag := strings.ToLower(s[loc[2]:loc[3]])
		end := snCloseTag(lower, tag, openEnd)
		inner := s[openEnd:end]
		hidden = append(hidden, strings.TrimSpace(reTag.ReplaceAllString(inner, " ")))
		b.WriteString(s[pos:start])
		if !remove {
			b.WriteString(s[start:end])
		}
		pos = end
		if len(hidden) > 200 {
			break
		}
	}
	b.WriteString(s[pos:])
	out := b.String()
	for _, m := range reComments.FindAllString(s, 100) {
		hidden = append(hidden, strings.TrimSuffix(strings.TrimPrefix(m, "<!--"), "-->"))
	}
	for _, m := range reHTMLAttrText.FindAllStringSubmatch(s, 100) {
		if injScan(m[2]) != "" || grCommentBad.MatchString(m[2]) {
			hidden = append(hidden, m[2])
		}
	}
	return out, hidden
}

// snNextHidden returns the submatch indexes (absolute) of the next opening
// tag at or after pos that hides its element, or nil.
func snNextHidden(s string, pos int) []int {
	for pos < len(s) {
		loc := reOpenTag.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			return nil
		}
		attrs := s[pos+loc[4] : pos+loc[5]]
		hid := reHiddenAttr.MatchString(attrs)
		if !hid {
			if m := reStyleAttr.FindStringSubmatch(attrs); m != nil {
				st := m[1] + m[2]
				hid = reStyleHidden.MatchString(st) || reStyleColor.MatchString(st) && reStyleBgWhite.MatchString(st)
			}
		}
		if hid {
			for i := range loc {
				loc[i] += pos
			}
			return loc // absolute indexes
		}
		pos += loc[1]
	}
	return nil
}

// snCloseTag finds the end (after </tag>) of the element whose opening tag
// ends at from, counting nested tags of the same name; void elements end at once.
func snCloseTag(lower, tag string, from int) int {
	switch tag {
	case "img", "input", "br", "hr", "meta", "link", "source", "area", "wbr":
		return from
	}
	depth := 1
	open, close := "<"+tag, "</"+tag
	i := from
	for i < len(lower) {
		o := strings.Index(lower[i:], open)
		c := strings.Index(lower[i:], close)
		if c < 0 {
			return len(lower)
		}
		if o >= 0 && o < c {
			nx := i + o + len(open)
			if nx < len(lower) && (lower[nx] == ' ' || lower[nx] == '>' || lower[nx] == '\t' || lower[nx] == '\n') {
				depth++
			}
			i = nx
			continue
		}
		depth--
		i += c + len(close)
		if gt := strings.IndexByte(lower[i:], '>'); gt >= 0 {
			i += gt + 1
		}
		if depth == 0 {
			return i
		}
	}
	return len(lower)
}

// snLooksHTML reports HTML content worth the hidden-element pass.
func snLooksHTML(s string) bool {
	h := strings.ToLower(s[:min(len(s), 4096)])
	return strings.Contains(h, "<html") || strings.Contains(h, "<!doctype html") || strings.Contains(h, "<body") || strings.Contains(h, "<div") || strings.Contains(h, "<span") || strings.Contains(h, "<p")
}

// ---------- agent integration ----------

// grSanitizeResult cleans a tool result before the model sees it. Hidden HTML
// is removed from fetched pages and MCP results (and from everything in strict
// mode); in file reads and command output it is only scanned. It returns the
// injection label the removed content implies ("" = none).
func (a *Agent) grSanitizeResult(tool string, isMCP bool, out *ToolOutput) string {
	mode := snMode(a.cfg)
	if mode == "off" || out == nil || out.Text == "" || tool == "todo" {
		return ""
	}
	text, rep := sanitizeForModel(out.Text, mode)
	if snLooksHTML(text) {
		remove := isMCP || tool == "fetch" || mode == "strict"
		var hidden []string
		text, hidden = snHiddenHTML(text, remove)
		if remove {
			rep.Hidden = hidden
		} else {
			for _, h := range hidden { // only flag, keep the file as it is
				if injScan(h) != "" || grCommentBad.MatchString(h) {
					rep.Hidden = append(rep.Hidden, h)
				}
			}
		}
	}
	out.Text = text
	if n := rep.note(); n != "" {
		out.Text += "\n\n" + n
		a.ui.Notice(tool + ": " + strings.TrimSuffix(strings.TrimPrefix(n, "[jazz: "), "]"))
	}
	label := rep.suspect()
	if label == "" && out.Suspect != "" {
		label = out.Suspect
	}
	return label
}

// grSanitizeInput cleans text the user pasted (invisible characters only).
func (a *Agent) grSanitizeInput(s string) string {
	mode := snMode(a.cfg)
	if mode == "off" {
		return s
	}
	out, rep := sanitizeForModel(s, mode)
	if rep.Invisible > 0 || rep.Homoglyphs > 0 {
		msg := fmt.Sprintf("texto colado: %d caracteres invisíveis removidos", rep.Invisible)
		if rep.Tags != "" {
			msg += fmt.Sprintf(" (texto escondido: %q)", oneLine(rep.Tags, 120))
		}
		a.ui.Warn(msg)
	}
	return out
}

// cmdSanitize implements `jazz sanitize <arquivo>`: shows what would be
// stripped before the content reaches the model, without changing the file.
// Exit 1 when something suspicious was found.
func cmdSanitize(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: jazz sanitize <arquivo>")
		return 2
	}
	bad := 0
	for _, p := range args {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "jazz:", err)
			return 1
		}
		text, rep := sanitizeForModel(string(b), "strip")
		if snLooksHTML(text) {
			_, rep.Hidden = snHiddenHTML(text, true)
		}
		label := injScan(text)
		if label == "" {
			label = rep.suspect()
		}
		fmt.Fprintf(w, "%s:\n", p)
		fmt.Fprintf(w, "  caracteres invisíveis: %d (bidi %d)\n", rep.Invisible, rep.Bidi)
		if rep.Tags != "" {
			fmt.Fprintf(w, "  texto escondido em tags Unicode: %q\n", rep.Tags)
		}
		if rep.Controls > 0 {
			fmt.Fprintf(w, "  sequências de terminal/controle: %d\n", rep.Controls)
		}
		if rep.Homoglyphs > 0 {
			fmt.Fprintf(w, "  letras parecidas (homóglifos): %d\n", rep.Homoglyphs)
		}
		for _, h := range rep.Hidden {
			if h = strings.TrimSpace(h); h != "" {
				fmt.Fprintf(w, "  HTML escondido/comentário: %q\n", oneLine(visibleDetail(h), 160))
			}
		}
		if label != "" {
			bad++
			fmt.Fprintf(w, "  ⚠ possível prompt injection: %s\n", label)
		} else {
			fmt.Fprintln(w, "  nada suspeito")
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}
