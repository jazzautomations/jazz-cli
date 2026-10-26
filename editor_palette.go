package main

// editor_palette.go: the pop-ups under the input box (the "/" command palette
// and the "@" file picker), their fuzzy ranking, history navigation with
// prefix filtering and the line-aware editing keys of a multi-line buffer.
// Everything here is computed from the buffer on the key path, so it is
// cached per input and kept cheap (low-power machines: one rank per key,
// narrowed from the previous result while the query only grows).

import (
	"os"
	"path"
	"sort"
	"strings"
	"unicode"
)

// edPopSlash / edPopMention: what the pop-up under the box is.
const (
	edPopNone = iota
	edPopSlash
	edPopMention
)

// edPop is the pop-up for the current buffer: its items and the rune range
// [start, end) of the buffer a pick replaces.
type edPop struct {
	kind       int
	items      []EdCommand
	start, end int
}

// edMentionMax caps the @ picker's candidates (ranked, before display).
const edMentionMax = 50

// edFuzzy scores cand against query (both already lowercase): every query
// rune must appear in order. Higher is better; ok=false = no match. Bonuses
// for runs of consecutive matches and for matches at a word start (after
// / _ - . space), a small penalty for gaps inside the match and for length.
func edFuzzy(query, cand string) (score int, ok bool) {
	return edFuzzyR([]rune(query), cand)
}

func edFuzzyR(q []rune, cand string) (score int, ok bool) {
	if len(q) == 0 {
		return 0, true
	}
	qi, run, prev := 0, 0, rune('/')
	for _, r := range cand {
		if qi < len(q) && r == q[qi] {
			score += 2
			if run > 0 {
				score += 3 * run
			}
			switch prev {
			case '/', '_', '-', '.', ' ':
				score += 6
			}
			run++
			qi++
		} else {
			if run == 0 && qi > 0 && qi < len(q) {
				score-- // gap inside the match
			}
			run = 0
			if qi == len(q) {
				break // matched: the rest only costs length
			}
		}
		prev = r
	}
	if qi < len(q) {
		return 0, false
	}
	return score - len(cand)/8, true
}

// edRankCommands filters the commands for the typed "/word": prefix matches
// first, then substring, then in-order (fuzzy), then typos; stable inside a tier.
func edRankCommands(cmds []EdCommand, typed string) []EdCommand {
	q := fzFold(strings.TrimPrefix(typed, "/"))
	if q == "" {
		return cmds
	}
	type ranked struct {
		c           EdCommand
		tier, score int
		i           int
	}
	var rs []ranked
	for i, c := range cmds {
		name := fzFold(strings.TrimPrefix(c.Name, "/"))
		tier, score := 0, 0
		switch {
		case strings.HasPrefix(name, q):
			tier = 4
		case strings.Contains(name, q):
			tier = 3
		default:
			if s, ok := edFuzzy(q, name); ok {
				tier, score = 2, s
			} else if len([]rune(q)) >= 3 {
				// a typo of a whole name, or of the start of a longer one
				pre := name
				if rn := []rune(name); len(rn) > len([]rune(q)) {
					pre = string(rn[:len([]rune(q))])
				}
				if d, ok := fzNearWord(q, pre); ok {
					tier, score = 1, -d
				}
			}
		}
		if tier > 0 {
			rs = append(rs, ranked{c, tier, score, i})
		}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].tier != rs[b].tier {
			return rs[a].tier > rs[b].tier
		}
		if rs[a].score != rs[b].score {
			return rs[a].score > rs[b].score
		}
		return rs[a].i < rs[b].i
	})
	out := make([]EdCommand, len(rs))
	for i, r := range rs {
		out[i] = r.c
	}
	return out
}

// edMentionTok finds an "@token" ending at pos (the cursor): start is the
// index of the '@'. The '@' must start the buffer or follow a space, so
// e-mail addresses never open the picker.
func edMentionTok(buf []rune, pos int) (start int, tok string, ok bool) {
	s := pos
	for s > 0 && !unicode.IsSpace(buf[s-1]) {
		s--
	}
	if s >= len(buf) || buf[s] != '@' {
		return 0, "", false
	}
	if pos < len(buf) && !unicode.IsSpace(buf[pos]) {
		return 0, "", false // cursor inside a word
	}
	return s, string(buf[s+1 : pos]), true
}

// edPaths is the candidate list of the @ picker: the files of the name index
// (nameindex.go) plus their directories ("dir/"), built once per index with
// the lowercase forms precomputed (the key path only compares).
type edPaths struct {
	ix    *niIndex
	all   []edPath
	lastQ string    // previous query and its matches: a longer query narrows them
	lastM []*edPath // (nil = recompute from all)
}

type edPath struct {
	s, lc, base string // path, lowercase path, lowercase base name
	dir         bool
}

func (p *edPaths) refresh(root string) {
	ix := niGet(root)
	if ix == p.ix {
		return
	}
	p.ix = ix
	dirs := map[string]bool{}
	var list []string
	for _, f := range ix.files {
		list = append(list, f.Rel)
		for d := path.Dir(f.Rel); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		list = append(list, d+"/")
	}
	p.set(list)
}

// set replaces the candidates: shallow and short first, so an empty query
// lists the top of the project.
func (p *edPaths) set(list []string) {
	depth := func(s string) int {
		d := strings.Count(s, "/")
		if strings.HasSuffix(s, "/") {
			d--
		}
		return d
	}
	sort.Slice(list, func(a, b int) bool {
		if da, db := depth(list[a]), depth(list[b]); da != db {
			return da < db
		}
		return list[a] < list[b]
	})
	p.all = make([]edPath, len(list))
	for i, c := range list {
		lc := strings.ToLower(c)
		p.all[i] = edPath{s: c, lc: lc, base: path.Base(strings.TrimSuffix(lc, "/")), dir: strings.HasSuffix(c, "/")}
	}
	p.lastQ, p.lastM = "", nil
}

// match ranks the paths for query: the base name counts double, so "edit"
// finds editor.go before docs/editing/x.md.
func (p *edPaths) match(query string) []string {
	q := strings.ToLower(query)
	if q == "" {
		p.lastQ, p.lastM = "", nil
		out := make([]string, 0, edMentionMax)
		for i := 0; i < len(p.all) && i < edMentionMax; i++ {
			out = append(out, p.all[i].s)
		}
		return out
	}
	qr := []rune(q)
	type ranked struct {
		s     string
		score int
	}
	var rs []ranked
	var keep []*edPath
	try := func(c *edPath) {
		sc, ok := edFuzzyR(qr, c.lc)
		if !ok {
			return
		}
		keep = append(keep, c)
		if bs, ok := edFuzzyR(qr, c.base); ok {
			sc += bs + 10
			if strings.HasPrefix(c.base, q) {
				sc += 20
			}
		}
		if c.dir {
			sc -= 8 // a file beats a directory with the same match
		}
		// keep only the best edMentionMax, in order (no full sort of 20k paths;
		// equal scores keep the shallow-first order of p.all)
		if len(rs) == edMentionMax && sc <= rs[len(rs)-1].score {
			return
		}
		i := len(rs)
		for i > 0 && rs[i-1].score < sc {
			i--
		}
		if len(rs) < edMentionMax {
			rs = append(rs, ranked{})
		}
		copy(rs[i+1:], rs[i:len(rs)-1])
		rs[i] = ranked{c.s, sc}
	}
	if p.lastM != nil && p.lastQ != "" && strings.HasPrefix(q, p.lastQ) {
		for _, c := range p.lastM { // the query only grew: its matches are a subset
			try(c)
		}
	} else {
		for i := range p.all {
			try(&p.all[i])
		}
	}
	p.lastQ, p.lastM = q, keep
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.s
	}
	return out
}

// popup is the pop-up for buf with the cursor at pos (cached per input).
func (e *Editor) popup(boxed bool, buf []rune, pos int) edPop {
	if !boxed || e.menuOff || len(buf) == 0 {
		return edPop{}
	}
	key := string(buf) + "\x00" + itoa(pos)
	if e.popKey == key {
		return e.popVal
	}
	p := edPop{}
	if items := e.menuItems(boxed, buf); len(items) > 0 {
		p = edPop{kind: edPopSlash, items: items, start: 0, end: len(buf)}
	} else if start, tok, ok := edMentionTok(buf, pos); ok && e.mentions() {
		root := e.Root
		if root == "" {
			root, _ = os.Getwd()
		}
		e.paths.refresh(root)
		var items []EdCommand
		for _, m := range e.paths.match(tok) {
			if m != tok { // "@src/" itself: picking it would change nothing
				items = append(items, EdCommand{Name: m})
			}
		}
		if !(len(items) == 1 && items[0].Name == tok) && len(items) > 0 {
			p = edPop{kind: edPopMention, items: items, start: start, end: pos}
		}
	}
	e.popKey, e.popVal = key, p
	return p
}

// edOpt reads one UIConf option of the editor's config (env wins).
func (e *Editor) edOpt(env string, get func(UIConf) string) bool {
	conf := ""
	if e.ui != nil && e.ui.cfg != nil {
		conf = get(e.ui.cfg.UI)
	}
	return uiOpt(env, conf)
}

// mentions: the @ picker is on (ui.mention_picker, JAZZ_MENTION_PICKER).
func (e *Editor) mentions() bool {
	return e.edOpt("JAZZ_MENTION_PICKER", func(c UIConf) string { return c.MentionPicker })
}

// pick replaces the pop-up's range with item i. Commands get a trailing
// space only on Tab (complete); a picked path always does (a directory keeps
// its slash so the picker goes on inside it).
func (p edPop) pick(buf []rune, i int, complete bool) ([]rune, int) {
	it := p.items[min(i, len(p.items)-1)].Name
	var repl string
	switch p.kind {
	case edPopSlash:
		repl = it
		if complete {
			repl += " "
		}
	default:
		repl = "@" + it
		if !strings.HasSuffix(it, "/") {
			repl += " "
		}
	}
	nb := append(append(append([]rune{}, buf[:p.start]...), []rune(repl)...), buf[p.end:]...)
	return nb, p.start + len([]rune(repl))
}

// ---------- history with prefix filtering ----------

// edHist walks the history: with text typed before the first Up, only the
// entries that start with it (Claude Code / fish style); never the same text
// twice in a row.
type edHist struct {
	on     bool
	idx    int // index of the entry shown (len(history) = the typed text)
	prefix string
	saved  string
}

func (h *edHist) reset() { *h = edHist{} }

// up returns the next older entry, or ok=false at the end.
func (h *edHist) up(hist []string, cur string) (string, bool) {
	if !h.on {
		*h = edHist{on: true, idx: len(hist), prefix: cur, saved: cur}
	}
	for i := h.idx - 1; i >= 0; i-- {
		if strings.HasPrefix(hist[i], h.prefix) && hist[i] != cur {
			h.idx = i
			return hist[i], true
		}
	}
	return "", false
}

// down returns the next newer entry, and the text typed before the walk
// after the newest one.
func (h *edHist) down(hist []string, cur string) (string, bool) {
	if !h.on {
		return "", false
	}
	for i := h.idx + 1; i < len(hist); i++ {
		if strings.HasPrefix(hist[i], h.prefix) && hist[i] != cur {
			h.idx = i
			return hist[i], true
		}
	}
	s := h.saved
	h.reset()
	return s, true
}

// ---------- line-aware editing (multi-line buffers) ----------

// edLineStart / edLineEnd: rune index of the start / end of the logical line
// (between '\n's) that holds pos.
func edLineStart(buf []rune, pos int) int {
	for pos > 0 && buf[pos-1] != '\n' {
		pos--
	}
	return pos
}

func edLineEnd(buf []rune, pos int) int {
	for pos < len(buf) && buf[pos] != '\n' {
		pos++
	}
	return pos
}

// edVertical moves the cursor one logical line up (dir -1) or down (+1),
// keeping the column; ok=false when there is no such line.
func edVertical(buf []rune, pos, dir int) (int, bool) {
	ls := edLineStart(buf, pos)
	col := pos - ls
	if dir < 0 {
		if ls == 0 {
			return pos, false
		}
		ps := edLineStart(buf, ls-1)
		return min(ps+col, ls-1), true
	}
	le := edLineEnd(buf, pos)
	if le >= len(buf) {
		return pos, false
	}
	ns := le + 1
	return min(ns+col, edLineEnd(buf, ns)), true
}

// edKillBack is Ctrl+U: delete from the start of the line to the cursor (at
// a line start, the newline before it).
func edKillBack(buf []rune, pos int) ([]rune, int) {
	ls := edLineStart(buf, pos)
	if ls == pos && pos > 0 {
		ls = pos - 1
	}
	return append(append([]rune{}, buf[:ls]...), buf[pos:]...), ls
}

// edKillFwd is Ctrl+K: delete from the cursor to the end of the line (at a
// line end, the newline after it).
func edKillFwd(buf []rune, pos int) []rune {
	le := edLineEnd(buf, pos)
	if le == pos && pos < len(buf) {
		le = pos + 1
	}
	return append(append([]rune{}, buf[:pos]...), buf[le:]...)
}
