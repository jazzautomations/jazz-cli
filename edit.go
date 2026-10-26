package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Port of pi's edit-diff.ts: exact match first; if any edit needs it, match in a
// per-line-normalized space and rewrite only the touched lines, copying every other
// line from the original byte for byte.

type editOp struct {
	Old string
	New string
	All bool
}

type editHunk struct {
	Line int // 1-based line in the new file where the change starts
	Old  []string
	New  []string
}

type editResult struct {
	Content string
	Fuzzy   bool
	Count   int // replacements applied
	Hunks   []editHunk
}

var fuzzyReplacer = strings.NewReplacer(
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-", "−", "-",
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ",
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", "　", " ",
)

// fuzzyNorm keeps the line count stable (required by applyPreservingLines).
func fuzzyNorm(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = norm.NFKC.String(l)
		l = strings.TrimRightFunc(l, func(r rune) bool { return r != '\n' && (unicode.IsSpace(r) || r == '\uFEFF') })
		lines[i] = fuzzyReplacer.Replace(l)
	}
	return strings.Join(lines, "\n")
}

func toLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

var lineNumPrefix = lazyRx(`^\s*\d+\t`)

// stripLineNumbers removes "   12\t" prefixes copied from read output, if every
// non-empty line has one. Returns ok=false when the text doesn't look like that.
func stripLineNumbers(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	seen := 0
	for _, l := range lines {
		if l == "" {
			continue
		}
		if !lineNumPrefix.MatchString(l) {
			return s, false
		}
		seen++
	}
	if seen == 0 {
		return s, false
	}
	for i, l := range lines {
		lines[i] = lineNumPrefix.ReplaceAllString(l, "")
	}
	return strings.Join(lines, "\n"), true
}

type match struct {
	edit   int
	index  int
	length int
	repl   string
}

func countNonOverlapping(hay, needle string) []int {
	var idx []int
	if needle == "" {
		return idx
	}
	for off := 0; ; {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			return idx
		}
		idx = append(idx, off+i)
		off += i + len(needle)
	}
}

// applyEdits applies all edits against the same (LF-normalized) content, atomically.
func applyEdits(content string, edits []editOp, path string) (*editResult, error) {
	n := len(edits)
	label := func(i int) string {
		if n == 1 {
			return "the text"
		}
		return fmt.Sprintf("edits[%d]", i)
	}
	ops := make([]editOp, n)
	for i, e := range edits {
		ops[i] = editOp{Old: toLF(e.Old), New: toLF(e.New), All: e.All}
		if ops[i].Old == "" {
			if n == 1 {
				return nil, fmt.Errorf("old_string must not be empty in %s. To create or replace a whole file use write", path)
			}
			return nil, fmt.Errorf("edits[%d].old_string must not be empty in %s", i, path)
		}
		// repair: model pasted read's line-number prefixes
		if !strings.Contains(content, ops[i].Old) && !strings.Contains(fuzzyNorm(content), fuzzyNorm(ops[i].Old)) {
			if s, ok := stripLineNumbers(ops[i].Old); ok {
				ops[i].Old = s
				if t, ok2 := stripLineNumbers(ops[i].New); ok2 {
					ops[i].New = t
				}
			}
		}
	}

	fuzzy := false
	for _, o := range ops {
		if !strings.Contains(content, o.Old) {
			fuzzy = true
			break
		}
	}
	base := content
	if fuzzy {
		base = fuzzyNorm(content)
	}

	var ms []match
	for i, o := range ops {
		needle := o.Old
		occ := countNonOverlapping(base, needle)
		if len(occ) == 0 && fuzzy {
			needle = fuzzyNorm(o.Old)
			occ = countNonOverlapping(base, needle)
		}
		if len(occ) == 0 {
			if n == 1 {
				return nil, fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.%s", path, nearestHint(content, o.Old))
			}
			return nil, fmt.Errorf("Could not find edits[%d] in %s. The old_string must match exactly including all whitespace and newlines.%s", i, path, nearestHint(content, o.Old))
		}
		if len(occ) > 1 && !o.All {
			return nil, fmt.Errorf("Found %d occurrences of %s in %s. The text must be unique. Please provide more context to make it unique, or set replace_all: true to change every occurrence.", len(occ), label(i), path)
		}
		for _, at := range occ {
			ms = append(ms, match{edit: i, index: at, length: len(needle), repl: o.New})
			if !o.All {
				break
			}
		}
	}
	sort.Slice(ms, func(a, b int) bool { return ms[a].index < ms[b].index })
	for i := 1; i < len(ms); i++ {
		p, c := ms[i-1], ms[i]
		if p.index+p.length > c.index {
			return nil, fmt.Errorf("edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.", p.edit, c.edit, path)
		}
	}

	var out string
	var err error
	if fuzzy {
		out, err = applyPreservingLines(content, base, ms)
		if err != nil {
			return nil, err
		}
	} else {
		out = applyReplacements(base, ms, 0)
	}
	if out == content {
		return nil, fmt.Errorf("No changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.", path)
	}
	return &editResult{Content: out, Fuzzy: fuzzy, Count: len(ms), Hunks: buildHunks(base, ms)}, nil
}

func applyReplacements(s string, ms []match, offset int) string {
	var b strings.Builder
	last := 0
	for _, m := range ms {
		i := m.index - offset
		b.WriteString(s[last:i])
		b.WriteString(m.repl)
		last = i + m.length
	}
	b.WriteString(s[last:])
	return b.String()
}

type span struct{ start, end int }

func splitKeepNL(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func lineSpans(s string) []span {
	var sp []span
	off := 0
	for _, l := range splitKeepNL(s) {
		sp = append(sp, span{off, off + len(l)})
		off += len(l)
	}
	return sp
}

// applyPreservingLines: replacements were matched in `base` (normalized view of
// `orig`); touched lines come from base, untouched lines from orig.
func applyPreservingLines(orig, base string, ms []match) (string, error) {
	ol := splitKeepNL(orig)
	bl := lineSpans(base)
	if len(ol) != len(bl) {
		return "", fmt.Errorf("internal: normalized content has a different line count; use an exact old_string")
	}
	type group struct {
		s, e int
		ms   []match
	}
	var gs []group
	for _, m := range ms {
		sl := -1
		for i, l := range bl {
			if m.index >= l.start && m.index < l.end {
				sl = i
				break
			}
		}
		if sl < 0 {
			return "", fmt.Errorf("internal: replacement outside content")
		}
		el := sl
		for el < len(bl) && bl[el].end < m.index+m.length {
			el++
		}
		if el >= len(bl) {
			el = len(bl) - 1
		}
		if n := len(gs); n > 0 && sl < gs[n-1].e {
			if el+1 > gs[n-1].e {
				gs[n-1].e = el + 1
			}
			gs[n-1].ms = append(gs[n-1].ms, m)
			continue
		}
		gs = append(gs, group{sl, el + 1, []match{m}})
	}
	var b strings.Builder
	oi := 0
	for _, g := range gs {
		b.WriteString(strings.Join(ol[oi:g.s], ""))
		gs0, ge := bl[g.s].start, bl[g.e-1].end
		b.WriteString(applyReplacements(base[gs0:ge], g.ms, gs0))
		oi = g.e
	}
	b.WriteString(strings.Join(ol[oi:], ""))
	return b.String(), nil
}

func buildHunks(base string, ms []match) []editHunk {
	splitL := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	var hs []editHunk
	delta := 0 // line shift caused by earlier replacements
	for _, m := range ms {
		line := strings.Count(base[:m.index], "\n") + 1
		oldS := base[m.index : m.index+m.length]
		hs = append(hs, editHunk{Line: line + delta, Old: splitL(oldS), New: splitL(m.repl)})
		delta += strings.Count(m.repl, "\n") - strings.Count(oldS, "\n")
	}
	return hs
}

// omissionMarker detects "lazy" placeholders that would delete real code
// (Gemini CLI has a dedicated detector for this failure of weak models).
var omissionRe = lazyRx(`(?i)^\s*(//|#|/\*|<!--|--|;|\*)?\s*(\.\.\.|…)?\s*\(?(rest|remainder|remaining|existing|previous|original|other|unchanged|same)\b.*\b(code|implementation|content|contents|methods|functions|logic|file|body|here|above|below|unchanged|same|omitted)\b|^\s*(//|#|/\*|<!--|--)?\s*(\.\.\.|…)\s*(\*/|-->)?\s*$|^\s*(//|#)\s*(TODO:?\s*)?(implement|add|insert|fill in|put)\s+(the\s+)?(rest|remaining|existing|other)\b`)

func omissionMarker(newText, oldText string) string {
	for _, l := range strings.Split(newText, "\n") {
		if strings.TrimSpace(l) == "" || !omissionRe.MatchString(l) {
			continue
		}
		if strings.Contains(oldText, strings.TrimSpace(l)) {
			continue // the placeholder already existed in the file
		}
		return strings.TrimSpace(l)
	}
	return ""
}

func detectCRLF(s string) bool {
	i := strings.IndexByte(s, '\n')
	return i > 0 && s[i-1] == '\r'
}

// nearestHint points the model at the most similar line when old_string wasn't found.
func nearestHint(content, old string) string {
	first := ""
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	if len(first) < 4 {
		return ""
	}
	best, bestLine, bestScore := "", 0, 0.0
	for i, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if s := similarity(first, t); s > bestScore {
			best, bestLine, bestScore = l, i+1, s
		}
	}
	if bestScore < 0.6 {
		return " Re-read the file to get the current text."
	}
	if len(best) > 160 {
		best = best[:160] + "…"
	}
	return fmt.Sprintf(" Closest line is %d: %q — re-read around it and copy the text exactly.", bestLine, best)
}

// similarity: Dice coefficient over character bigrams (cheap, good enough).
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	bg := map[string]int{}
	for i := 0; i < len(a)-1; i++ {
		bg[a[i:i+2]]++
	}
	inter := 0
	for i := 0; i < len(b)-1; i++ {
		k := b[i : i+2]
		if bg[k] > 0 {
			bg[k]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(a)+len(b)-2)
}

// ---------- dangling renames ----------

var editIdentRE = lazyRx(`[A-Za-z_][A-Za-z0-9_]*`)

func editIdentCounts(s string) map[string]int {
	out := map[string]int{}
	for _, id := range editIdentRE.FindAllString(s, -1) {
		out[id]++
	}
	return out
}

// editLineWith returns the 1-based line of s holding id as a whole word and
// that line's text (0, "" = none).
func editLineWith(s, id string) (int, string) {
	for i, l := range strings.Split(s, "\n") {
		if editIdentCounts(l)[id] > 0 {
			return i + 1, l
		}
	}
	return 0, ""
}

// editDanglingRenames flags an identifier the edit replaced by a near-identical
// one (PedidoBalcao -> PedidoBalcone) while the old name is still used
// elsewhere in the file and the new name nowhere else: almost always a typo in
// the context lines the model copied around its change (eval v2,
// 03-arquivo-grande: the run spent 10 minutes on such a typo). It returns a
// note for the model with the exact original line ("" = nothing found).
func editDanglingRenames(content string, ops []editOp) string {
	var notes []string
	var file map[string]int
	for _, op := range ops {
		if op.All {
			continue
		}
		o, n := editIdentCounts(toLF(op.Old)), editIdentCounts(toLF(op.New))
		var gone, added []string
		for id := range o {
			if n[id] == 0 && len(id) >= 4 {
				gone = append(gone, id)
			}
		}
		for id := range n {
			if o[id] == 0 && len(id) >= 4 {
				added = append(added, id)
			}
		}
		if len(gone) == 0 || len(added) == 0 {
			continue
		}
		sort.Strings(gone)
		sort.Strings(added)
		if file == nil {
			file = editIdentCounts(content)
		}
		for _, g := range gone {
			if file[g] == 0 {
				continue // renamed everywhere: intended
			}
			for _, a := range added {
				limit := 2
				if len(g) >= 8 && len(a) >= 8 {
					limit = 3 // PedidoBalcao -> PedidoBalcone
				}
				if file[a] != n[a] || editLevenshtein(g, a) > limit {
					continue
				}
				_, orig := editLineWith(toLF(op.Old), g)
				ln, now := editLineWith(content, a)
				un, _ := editLineWith(content, g)
				notes = append(notes, fmt.Sprintf("Warning: this edit changed `%s` to `%s` (line %d is now `%s`), but `%s` is still used at line %d and `%s` appears nowhere else in the file. If you did not mean to rename it, put that line back exactly as it was: `%s`",
					g, a, ln, strings.TrimSpace(now), g, un, a, strings.TrimSpace(orig)))
			}
		}
	}
	return strings.Join(notes, "\n")
}

// editLevenshtein is the edit distance of two short ASCII-ish strings.
func editLevenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > 3 || d < -3 {
		return 4
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := prev[j-1]
			if ra[i-1] != rb[j-1] {
				c++
			}
			cur[j] = min(c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
