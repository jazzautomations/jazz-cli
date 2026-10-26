package main

// sessions_ui.go: sessions like Claude Code.
//
//   - Titles: a 5–8 word title made from the first prompt, deterministically
//     (no model call), stored as a {"t":"title"} entry; /rename replaces it.
//   - Picker: one arrow-key list widget (Editor.Pick) used by /resume,
//     /sessions, `jazz -r` with no id and /tree: ↑↓/jk move, Enter picks,
//     Esc cancels, typing filters (fuzzy, fuzzy.go), 1-9 pick a row, Tab
//     switches lists. Drawn in place and erased afterwards, like the input box.
//   - Replay: after a resume the conversation is rendered again with the live
//     view's glyphs (prompts, markdown answers, collapsed tool calls, a totals
//     line), capped at the last resume_replay entries (default 200).
//
// Nothing here touches the system prompt or the history sent to the model.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------- titles ----------

// sessMentionRe matches the blocks ExpandMentions puts before the typed text.
var sessMentionRe = regexp.MustCompile(`(?s)<(file|dir|image) path="[^"]*">.*?</(file|dir|image)>`)

var sessFenceRe = regexp.MustCompile("(?s)```.*?(```|$)")

// sessTitleLead are filler phrases dropped from the start of a title (folded).
var sessTitleLead = [][]string{
	{"por", "favor"}, {"oi"}, {"ola"}, {"ei"}, {"hey"}, {"hi"}, {"hello"}, {"please"}, {"pls"}, {"pfv"}, {"pf"},
	{"voce", "pode"}, {"voce", "poderia"}, {"vc", "pode"}, {"pode"}, {"poderia"}, {"can", "you"}, {"could", "you"},
	{"me", "ajuda", "a"}, {"me", "ajude", "a"}, {"jazz"},
}

// sessTitleTail are words a cut title should not end on.
var sessTitleTail = map[string]bool{"de": true, "do": true, "da": true, "dos": true, "das": true, "e": true, "a": true, "o": true,
	"os": true, "as": true, "em": true, "no": true, "na": true, "para": true, "pra": true, "com": true, "por": true, "que": true,
	"um": true, "uma": true, "the": true, "of": true, "and": true, "to": true, "in": true, "for": true, "with": true}

const (
	sessTitleMax   = 8 // words
	sessTitleMin   = 5 // a cut title keeps at least this many words
	sessTitleRunes = 60
)

// sessAutoTitle names a session from its first prompt: harness reminders,
// @file blocks, code fences and filler ("por favor", "você pode") are
// dropped, then up to 8 words are kept (5 at least when the prompt has
// them), without a dangling "de"/"para" at the end. Same input, same title.
func sessAutoTitle(prompt string) string {
	s := acStripReminders(prompt)
	s = sessMentionRe.ReplaceAllString(s, " ")
	s = sessFenceRe.ReplaceAllString(s, " ")
	s = sanitizeTerm(s)
	var words []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, "#*`>_~\"'“”()[]{}")
		if w == "" || strings.HasPrefix(w, "<") {
			continue
		}
		if r := []rune(w); len(r) > 30 {
			w = string(r[:29]) + "…"
		}
		words = append(words, w)
	}
	fold := func(w string) string { return strings.Trim(fzFold(w), ".,;:!?") }
	for again := true; again && len(words) > 0; {
		again = false
		for _, ph := range sessTitleLead {
			if len(words) <= len(ph) {
				continue
			}
			ok := true
			for i, p := range ph {
				if fold(words[i]) != p {
					ok = false
					break
				}
			}
			if ok {
				words, again = words[len(ph):], true
				break
			}
		}
	}
	for len(words) > 1 { // "… por favor" / "… please" at the end
		l := fold(words[len(words)-1])
		if l == "favor" && len(words) > 2 && fold(words[len(words)-2]) == "por" {
			words = words[:len(words)-2]
			continue
		}
		if l == "please" || l == "pls" || l == "pfv" {
			words = words[:len(words)-1]
			continue
		}
		break
	}
	if len(words) > sessTitleMax {
		words = words[:sessTitleMax]
		for len(words) > sessTitleMin && sessTitleTail[fold(words[len(words)-1])] {
			words = words[:len(words)-1]
		}
	}
	if len(words) == 0 {
		return "(sem título)"
	}
	t := strings.TrimRight(strings.Join(words, " "), ".,;:!?…-—")
	if t == "" {
		return "(sem título)"
	}
	r, n := utf8.DecodeRuneInString(t)
	t = string(unicode.ToUpper(r)) + t[n:]
	return oneLine(t, sessTitleRunes)
}

// SetTitle records the session's title (the newest entry wins).
func (s *Session) SetTitle(title string) {
	title = oneLine(sanitizeTerm(strings.TrimSpace(title)), 120)
	if s == nil || s.f == nil || title == "" {
		return
	}
	s.write(sessEntry{T: "title", Title: title})
	s.mu.Lock()
	s.title = title
	s.mu.Unlock()
}

// Title is the session's stored title ("" = none yet).
func (s *Session) Title() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// sessAutoTitleTurn stores the automatic title on the first turn of a new
// session (Agent.Run). Loaded sessions keep theirs; old ones without a title
// entry get the same title computed on the fly by ListSessions.
func (a *Agent) sessAutoTitleTurn(input string) {
	if a.sub || a.sess == nil || a.sess.Title() != "" || len(a.UserTurns()) > 0 {
		return
	}
	if strings.TrimSpace(acStripReminders(input)) == "" {
		return
	}
	a.sess.SetTitle(sessAutoTitle(input))
}

// ---------- relative dates ----------

var sessMonths = []string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}

// sessAgo is a relative date in Portuguese: "agora", "há 5 min", "há 2 h",
// "ontem", "há 3 dias", then the date ("12 out", "12 out 2025").
func sessAgo(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	case d < 48*time.Hour:
		return "ontem"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("há %d dias", int(d.Hours()/24))
	}
	s := fmt.Sprintf("%d %s", t.Day(), sessMonths[t.Month()-1])
	if t.Year() != now.Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

// sessTildeDir shows a path with the home directory as ~.
func sessTildeDir(dir string) string {
	if home, _ := os.UserHomeDir(); home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

// ---------- picker ----------

// pkItem is one row of the picker.
type pkItem struct {
	Group string        // header above the first row of a group ("" = none)
	Title string        // main text (plain)
	Right string        // dim text after the title ("há 2 h")
	Mark  string        // highlighted tag after Right ("atual")
	Meta  func() string // second, dim line (lazy: may read files); nil = one line
	Key   string        // more text the filter looks at
	Off   bool          // shown but not selectable
	Val   any           // what the caller gets back
}

// pkSpec describes one picker run. Tab calls Tab (when set) for another list.
type pkSpec struct {
	Head  string
	Hint  string
	Items []pkItem
	Sel   int // item selected at the start
	Tab   func() (items []pkItem, head string)
	metas map[int]string
}

// pkState is the picker's pure state machine (tested without a terminal).
type pkState struct {
	sp     *pkSpec
	query  []rune
	typing bool  // "/" or a letter started the filter: j/k and digits are text
	view   []int // items shown, in order
	sel    int   // position in view
	top    int   // first position on screen
}

func newPkState(sp *pkSpec) *pkState {
	st := &pkState{sp: sp}
	st.refilter()
	for p, i := range st.view {
		if i == sp.Sel {
			st.sel = p
		}
	}
	if st.sel < len(st.view) && sp.Items[st.view[st.sel]].Off {
		st.move(1)
	}
	return st
}

// pkMatch scores how well the folded query terms match hay (-1 = no match):
// substring beats a subsequence inside one word beats a word within typo
// distance (fuzzy.go).
func pkMatch(hay string, terms []string) int {
	words := strings.FieldsFunc(hay, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	score := 0
	for _, q := range terms {
		if i := strings.Index(hay, q); i >= 0 {
			score += 100 - min(i, 50)
			continue
		}
		best := -1
		for _, w := range words { // in-word subsequence: "prsr" finds "parser"
			if g := pkSubseq(w, q); g >= 0 && (best < 0 || g < best) {
				best = g
			}
		}
		if best >= 0 {
			score += 40 - min(best, 30)
			continue
		}
		for _, w := range words {
			if d, ok := fzNearWord(q, w); ok && (best < 0 || d < best) {
				best = d
			}
			if len([]rune(w)) > len([]rune(q)) { // a typo in a prefix: "sesao" ~ "sessoes"
				if d, ok := fzNearWord(q, string([]rune(w)[:len([]rune(q))])); ok && (best < 0 || d < best) {
					best = d
				}
			}
		}
		if best < 0 {
			return -1
		}
		score += 20 - best
	}
	return score
}

// pkSubseq: q's runes appear in order in s; returns the gaps (-1 = no).
func pkSubseq(s, q string) int {
	rs, rq := []rune(s), []rune(q)
	gaps, j, last := 0, 0, -1
	for i := 0; i < len(rs) && j < len(rq); i++ {
		if rs[i] == rq[j] {
			if last >= 0 && i != last+1 {
				gaps++
			}
			last = i
			j++
		}
	}
	if j < len(rq) {
		return -1
	}
	return gaps
}

func (st *pkState) refilter() {
	st.view = st.view[:0]
	terms := strings.Fields(fzFold(string(st.query)))
	if len(terms) == 0 {
		for i := range st.sp.Items {
			st.view = append(st.view, i)
		}
		st.sel, st.top = 0, 0
		return
	}
	type sc struct{ i, s int }
	var hits []sc
	for i, it := range st.sp.Items {
		if it.Off {
			continue
		}
		if s := pkMatch(fzFold(it.Title+" "+it.Right+" "+it.Group+" "+it.Key), terms); s >= 0 {
			hits = append(hits, sc{i, s})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].s > hits[b].s })
	for _, h := range hits {
		st.view = append(st.view, h.i)
	}
	st.sel, st.top = 0, 0
}

// move goes d rows, skipping rows that cannot be picked.
func (st *pkState) move(d int) {
	p := st.sel
	for {
		p += d
		if p < 0 || p >= len(st.view) {
			return
		}
		if !st.sp.Items[st.view[p]].Off {
			st.sel = p
			return
		}
	}
}

func (st *pkState) current() int {
	if st.sel < 0 || st.sel >= len(st.view) {
		return -1
	}
	if i := st.view[st.sel]; !st.sp.Items[i].Off {
		return i
	}
	return -1
}

// key applies one key. done: the picker closes, with pick (-1 = cancelled).
func (st *pkState) key(k Key) (done bool, pick int) {
	switch k.Code {
	case KUp:
		st.move(-1)
	case KDown:
		st.move(1)
	case KPgUp:
		for i := 0; i < 8; i++ {
			st.move(-1)
		}
	case KPgDn:
		for i := 0; i < 8; i++ {
			st.move(1)
		}
	case KHome:
		st.sel = 0
		if st.current() < 0 {
			st.move(1)
		}
	case KEnd:
		st.sel = len(st.view) - 1
		if st.current() < 0 {
			st.move(-1)
		}
	case KEnter:
		if i := st.current(); i >= 0 {
			return true, i
		}
	case KEsc:
		if len(st.query) > 0 || st.typing {
			st.query, st.typing = nil, false
			st.refilter()
			return false, 0
		}
		return true, -1
	case KCtrlC, KCtrlD:
		return true, -1
	case KCtrlU:
		st.query, st.typing = nil, false
		st.refilter()
	case KBackspace:
		if len(st.query) > 0 {
			st.query = st.query[:len(st.query)-1]
			if len(st.query) == 0 {
				st.typing = false
			}
			st.refilter()
		} else {
			st.typing = false
		}
	case KTab:
		if st.sp.Tab != nil {
			st.sp.Items, st.sp.Head = st.sp.Tab()
			st.sp.metas = nil
			st.query, st.typing = nil, false
			st.refilter()
		}
	case KPaste:
		st.query = append(st.query, []rune(strings.Join(strings.Fields(k.Text), " "))...)
		st.typing = true
		st.refilter()
	case KRune:
		r := k.R
		if !st.typing && len(st.query) == 0 {
			switch {
			case r == 'j':
				st.move(1)
				return false, 0
			case r == 'k':
				st.move(-1)
				return false, 0
			case r == '/':
				st.typing = true
				return false, 0
			case r >= '1' && r <= '9':
				if p := int(r - '1'); p < len(st.view) && !st.sp.Items[st.view[p]].Off {
					return true, st.view[p]
				}
				return false, 0
			}
		}
		if unicode.IsPrint(r) {
			st.query = append(st.query, r)
			st.typing = true
			st.refilter()
		}
	}
	return false, 0
}

// rows is how many screen lines item at view position p takes.
func (st *pkState) rows(p int) int {
	it := st.sp.Items[st.view[p]]
	n := 1
	if it.Meta != nil {
		n++
	}
	if it.Group != "" && (p == st.top || st.sp.Items[st.view[p-1]].Group != it.Group) {
		n++
	}
	return n
}

func (st *pkState) meta(i int) string {
	if st.sp.metas == nil {
		st.sp.metas = map[int]string{}
	}
	if m, ok := st.sp.metas[i]; ok {
		return m
	}
	m := st.sp.Items[i].Meta()
	st.sp.metas[i] = m
	return m
}

// lines renders the picker in at most h lines of width w.
func (st *pkState) lines(t Theme, w, h int) []string {
	g := t.G()
	reset, dim, bold := t.sgr("0"), t.fg(cGray, 90), t.sgr("1")
	acc := t.fg(cAccent, 36)
	head := "  " + acc + bold + st.sp.Head + reset
	if st.typing || len(st.query) > 0 { // the filter first: a long head is what gets clipped
		head = "  " + dim + "filtro " + reset + acc + g.prompt + reset + " " + sanitizeTerm(string(st.query)) + "\x1b[7m \x1b[27m" + dim + "  " + g.dot + " " + st.sp.Head + reset
	}
	out := []string{clipVisible(head, w)}
	budget := max(h-2, 2)
	if st.sel < st.top {
		st.top = st.sel
	}
	for st.top < st.sel {
		used := 0
		for p := st.top; p <= st.sel; p++ {
			used += st.rows(p)
		}
		if used <= budget {
			break
		}
		st.top++
	}
	used := 0
	last := st.top - 1
	for p := st.top; p < len(st.view); p++ {
		if used+st.rows(p) > budget {
			break
		}
		used += st.rows(p)
		last = p
		i := st.view[p]
		it := st.sp.Items[i]
		if it.Group != "" && (p == st.top || st.sp.Items[st.view[p-1]].Group != it.Group) {
			out = append(out, clipVisible("  "+dim+bold+sanitizeTerm(it.Group)+reset, w))
		}
		num := "  "
		if p < 9 && !it.Off {
			num = dim + strconv.Itoa(p+1) + reset + " "
		}
		right := ""
		if it.Right != "" {
			right = "  " + dim + sanitizeTerm(it.Right) + reset
		}
		if it.Mark != "" {
			right += "  " + t.fg(cGold, 33) + sanitizeTerm(it.Mark) + reset
		}
		room := max(w-6-uiVisLen(right), 10)
		title := sanitizeTerm(it.Title)
		if uiStrW(title) > room {
			title = clipVisible(title, room-1) + g.ell
		}
		var l string
		switch {
		case p == st.sel && !it.Off:
			l = "  " + acc + bold + g.ptr + reset + " " + num + bold + title + reset + right
		case it.Off:
			l = "    " + num + dim + title + reset + right
		default:
			l = "    " + num + title + right
		}
		out = append(out, clipVisible(l, w))
		if it.Meta != nil {
			out = append(out, clipVisible("        "+dim+sanitizeTerm(st.meta(i))+reset, w))
		}
	}
	if len(st.view) == 0 {
		msg := "nada encontrado"
		if len(st.query) > 0 {
			msg = "nada com “" + string(st.query) + "”"
		}
		out = append(out, "    "+dim+sanitizeTerm(msg)+reset)
	}
	pos := ""
	if len(st.view) > 0 {
		pos = fmt.Sprintf("%d/%d", st.sel+1, len(st.view))
		if more := len(st.view) - 1 - last; more > 0 {
			pos += fmt.Sprintf(" %s +%d", g.ell, more)
		}
		pos += "  "
	}
	out = append(out, clipVisible("  "+dim+pos+st.sp.Hint+reset, w))
	return out
}

// pkHint is the key help under every list.
const pkHint = "↑↓ jk move · enter abre · esc cancela · digite filtra"

// Pick shows the list and returns the chosen item (an index into sp.Items,
// which Tab may have replaced) or -1. The list is drawn in place and erased.
func (e *Editor) Pick(sp *pkSpec) int {
	if e.ui.keys == nil || len(sp.Items) == 0 {
		return -1
	}
	e.ui.Flush()
	e.ui.mu.Lock()
	e.ui.ensureLine()
	e.ui.mu.Unlock()
	st := newPkState(sp)
	draw := func() {
		h := max(min(termHeight()-2, 26), 6)
		lines := st.lines(e.ui.T, termWidth()-1, h)
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
		uiWrite(b.String())
	}
	draw()
	defer uiWrite("\r\x1b[J\x1b[?25h")
	winch := uiWinch()
	for {
		select {
		case <-winch:
			uiWidthInvalidate()
			draw()
			continue
		case k, ok := <-e.ui.keys.Keys:
			if !ok {
				return -1
			}
			if done, pick := st.key(k); done {
				return pick
			}
			if edCoalesce && len(e.ui.keys.Keys) > 0 {
				continue // keys already queued: one redraw for all
			}
			draw()
		}
	}
}

// ---------- session rows ----------

// sessPickItems turns sessions into picker rows; all = group by project.
func sessPickItems(list []SessionInfo, current string, all bool, now time.Time) []pkItem {
	items := make([]pkItem, 0, len(list))
	if all { // groups: the newest group first, newest sessions first inside
		idx := map[string]int{}
		var order []string
		for _, si := range list {
			d := si.Cwd
			if d == "" {
				d = filepath.Dir(si.Path)
			}
			if _, ok := idx[d]; !ok {
				idx[d] = len(order)
				order = append(order, d)
			}
		}
		sort.SliceStable(list, func(i, j int) bool {
			di, dj := list[i].Cwd, list[j].Cwd
			if di == "" {
				di = filepath.Dir(list[i].Path)
			}
			if dj == "" {
				dj = filepath.Dir(list[j].Path)
			}
			return idx[di] < idx[dj]
		})
	}
	for _, si := range list {
		si := si
		it := pkItem{Title: si.Title, Right: sessAgo(si.Mod, now), Key: si.ID + " " + si.Model, Val: si}
		if all {
			it.Group = sessTildeDir(si.Cwd)
			if si.Cwd == "" {
				it.Group = filepath.Base(filepath.Dir(si.Path))
			}
		}
		if si.ID == current {
			it.Mark = "atual"
		}
		it.Meta = func() string { return sessMetaLine(si) }
		items = append(items, it)
	}
	return items
}

// sessMetaLine is the second line of a session row: mode, messages, tokens,
// cost, how it ended (from the event stream, obs.go) and the project dir.
func sessMetaLine(si SessionInfo) string {
	evs, _ := obReadEvents(obEventsPath(si.Path))
	s := obSummarize(evs)
	if e := sessLastEnd(si.Path); e != nil && s.Runs == 0 {
		s.Status, s.Outcome = e.Status, e.Outcome
	}
	var parts []string
	if s.Mode != "" {
		parts = append(parts, s.Mode)
	}
	parts = append(parts, fmt.Sprintf("%d msgs", si.Msgs))
	in, out, cost := si.Usage.In, si.Usage.Out, si.Usage.Cost
	if in+out == 0 {
		in, out = s.In, s.Out
	}
	if cost == 0 {
		cost = s.Cost
	}
	if in+out > 0 {
		parts = append(parts, fmtK(in+out)+" tokens")
	}
	if cost > 0 {
		parts = append(parts, fmtCost(cost))
	}
	if s.Outcome != "" {
		parts = append(parts, obOutcomePT(s.Outcome))
	}
	if si.Cwd != "" { // last: a long path is what gets clipped
		parts = append(parts, sessTildeDir(si.Cwd))
	}
	return strings.Join(parts, " · ")
}

// ---------- replay ----------

const replayDefault = 200

// replayLimit reads resume_replay (JAZZ_RESUME_REPLAY wins): all = -1
// (every entry), off/0 = 0 (no replay), N = the last N entries.
func replayLimit(cfg *Config) int {
	v := strings.TrimSpace(os.Getenv("JAZZ_RESUME_REPLAY"))
	if v == "" && cfg != nil && cfg.ResumeReplay != nil {
		v = strings.TrimSpace(fmt.Sprint(cfg.ResumeReplay))
	}
	switch strings.ToLower(v) {
	case "":
		return replayDefault
	case "all", "tudo", "-1":
		return -1
	case "off", "0", "false", "no":
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
		return int(f)
	}
	return replayDefault
}

// sessToolErrs is which tool calls failed, from the write-ahead records.
func sessToolErrs(path string) map[string]bool {
	ents, err := sessReadEntries(path)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range ents {
		if e.T == "tool_result" && e.Tool != nil && e.Tool.IsErr {
			out[e.Tool.CallID] = true
		}
	}
	return out
}

// rpUserText is what the user typed: reminders and @file blocks removed.
func rpUserText(s string) string {
	s = acStripReminders(s)
	s = sessMentionRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// uiEchoLine is how a submitted prompt stays in the transcript ("› texto"),
// wrapped with a hanging indent like the answers.
func uiEchoLine(t Theme, s string) string {
	mark := t.fg(cGray, 90) + t.G().prompt + t.sgr("0") + " "
	if strings.HasPrefix(s, "!") {
		mark = t.fg(cGold, 33) + "!" + t.sgr("0") + " "
		s = strings.TrimPrefix(s, "!")
	}
	w := max(termWidth()-3, 20)
	var lines []string
	for _, l := range strings.Split(sanitizeTerm(strings.ReplaceAll(s, "\t", " ")), "\n") {
		lines = append(lines, uiWrapWords(l, w)...)
	}
	return mark + strings.Join(lines, "\n  ") + "\n"
}

// rpEditCounts counts the lines an edit call removed and added (from its args).
func rpEditCounts(raw string) (path string, adds, dels int) {
	m, _, err := parseArgs(raw)
	if err != nil {
		return "", 0, 0
	}
	m = normalizeArgs("edit", m)
	path, _ = argStr(m, "path")
	count := func(s string) int {
		if s == "" {
			return 0
		}
		return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
	}
	pair := func(o, n string) {
		// lines both sides share do not count (the live view shows the hunk only)
		ol, nl := strings.Split(o, "\n"), strings.Split(n, "\n")
		for len(ol) > 0 && len(nl) > 0 && ol[0] == nl[0] {
			ol, nl = ol[1:], nl[1:]
		}
		for len(ol) > 0 && len(nl) > 0 && ol[len(ol)-1] == nl[len(nl)-1] {
			ol, nl = ol[:len(ol)-1], nl[:len(nl)-1]
		}
		dels += count(strings.Join(ol, "\n"))
		adds += count(strings.Join(nl, "\n"))
	}
	if arr, ok := m["edits"].([]any); ok && len(arr) > 0 {
		for _, x := range arr {
			if em, ok := x.(map[string]any); ok {
				o, _ := argStr(em, "old_string")
				n, _ := argStr(em, "new_string")
				pair(o, n)
			}
		}
		return path, adds, dels
	}
	o, _ := argStr(m, "old_string")
	n, _ := argStr(m, "new_string")
	pair(o, n)
	return path, adds, dels
}

// replayTool renders one tool call with its result, collapsed like the live
// view: "● Nome(detalhe)" with the outcome's color and "⎿  resumo".
func (u *TermUI) replayTool(tc ToolCall, res *Message, isErr bool, w int) string {
	t, g := u.T, u.T.G()
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	name := CanonicalTool(tc.Name)
	if name == "" {
		name = tc.Name
	}
	dot := dim
	text := ""
	if res != nil {
		text = strings.TrimSpace(acStripReminders(res.Content))
		isErr = isErr || strings.HasPrefix(text, "BLOCKED") || strings.HasPrefix(text, "TOOL_")
		dot = t.fg(cGreen, 32)
		if isErr {
			dot = t.fg(cRed, 31)
		}
	}
	var b strings.Builder
	b.WriteString(clipVisible(dot+g.tool+reset+" "+u.toolHead(name, toolDetail(tc.Name, tc.Args)), w) + "\n")
	sum := firstSentence(text)
	switch {
	case res == nil:
		sum = "sem resultado (a sessão parou antes)"
	case isErr:
	case name == "edit":
		if p, adds, dels := rpEditCounts(tc.Args); p != "" {
			sum = fmt.Sprintf("Atualizou %s com %d %s e %d %s", t.sgr("1")+sanitizeTerm(p)+t.sgr("22"), adds, uiPl(adds, "adição", "adições"), dels, uiPl(dels, "remoção", "remoções"))
		}
	case name == "write":
		m, _, err := parseArgs(tc.Args)
		if err == nil {
			m = normalizeArgs("write", m)
			p, _ := argStr(m, "path")
			c, _ := argStr(m, "content")
			if n := strings.Count(strings.TrimRight(c, "\n"), "\n") + 1; p != "" && c != "" {
				sum = fmt.Sprintf("Escreveu %d %s em %s", n, uiPl(n, "linha", "linhas"), t.sgr("1")+sanitizeTerm(p)+t.sgr("22"))
			}
		}
	}
	col := dim
	if isErr {
		col = t.fg(cRed, 31)
	}
	b.WriteString(clipVisible(fmt.Sprintf("  %s%s  %s%s", dim, g.res, col+oneLine(sanitizeTerm(sum), 400), reset), w) + "\n")
	return b.String()
}

// replayBlocks renders the conversation as the blocks the live view prints:
// prompts, answers (markdown), tool calls. Reasoning is not shown.
func (u *TermUI) replayBlocks(msgs []Message, errs map[string]bool, w int) []string {
	t, g := u.T, u.T.G()
	reset, dim := t.sgr("0"), t.fg(cGray, 90)
	results := map[string]*Message{}
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolCallID != "" {
			results[msgs[i].ToolCallID] = &msgs[i]
		}
	}
	var out []string
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if strings.HasPrefix(m.Content, "<summary>") {
				out = append(out, "  "+dim+g.dot+" (resumo da conversa anterior, compactada)"+reset+"\n")
				continue
			}
			s := rpUserText(m.Content)
			if s == "" && len(m.Images) == 0 {
				continue // a harness note, not something the user typed
			}
			blk := uiEchoLine(t, s)
			if len(m.Images) > 0 {
				blk += "  " + dim + fmt.Sprintf("[%d %s]", len(m.Images), uiPl(len(m.Images), "imagem", "imagens")) + reset + "\n"
			}
			out = append(out, blk)
		case "assistant":
			if txt := strings.TrimSpace(m.Content); txt != "" {
				md := mdRenderer{t: t, lineStart: true, indent: "  "}
				md.begin(2, w)
				var z uiSan
				s := t.fg(cAccent, 36) + g.bot + reset + " " + md.Feed(z.filter(txt)) + md.Flush()
				if !strings.HasSuffix(s, "\n") {
					s += "\n"
				}
				out = append(out, s+reset)
			}
			for _, tc := range m.ToolCalls {
				out = append(out, u.replayTool(tc, results[tc.ID], errs[tc.ID], w))
			}
		}
	}
	return out
}

// replayView joins the last n blocks (n < 0 = all). hidden is the plain
// text of the blocks left out (for /expand) and nh how many there were.
func (u *TermUI) replayView(msgs []Message, errs map[string]bool, n, w int) (view, hidden string, nh int) {
	blocks := u.replayBlocks(msgs, errs, w)
	if n == 0 || len(blocks) == 0 {
		return "", "", 0
	}
	t := u.T
	var b strings.Builder
	if n > 0 && len(blocks) > n {
		nh = len(blocks) - n
		hidden = rpPlain(strings.Join(blocks[:nh], "\n"))
		blocks = blocks[nh:]
		fmt.Fprintf(&b, "  %s%s (%d mais acima, /expand)%s\n\n", t.fg(cGray, 90), t.G().ell, nh, t.sgr("0"))
	}
	b.WriteString(strings.Join(blocks, "\n"))
	return b.String(), hidden, nh
}

// rpPlain drops the styling of every line (stripANSI alone eats newlines).
func rpPlain(s string) string {
	ls := strings.Split(s, "\n")
	for i := range ls {
		ls[i] = stripANSI(ls[i])
	}
	return strings.Join(ls, "\n")
}

// replayTotals is the line under a replayed conversation.
func replayTotals(t Theme, si SessionInfo, msgs int) string {
	g := t.G()
	parts := []string{"conversa anterior", fmt.Sprintf("%d %s", msgs, uiPl(msgs, "mensagem", "mensagens"))}
	if si.Tools > 0 {
		parts = append(parts, fmt.Sprintf("%d ferramenta%s", si.Tools, plural(si.Tools)))
	}
	if si.Usage.In+si.Usage.Out > 0 {
		parts = append(parts, fmt.Sprintf("%s %s %s %s", g.up, fmtK(si.Usage.In), g.down, fmtK(si.Usage.Out)))
	}
	if si.Usage.Cost > 0 {
		parts = append(parts, fmtCost(si.Usage.Cost))
	}
	return fmt.Sprintf("  %s%s %s%s\n", t.fg(cGray, 90), g.star, strings.Join(parts, " "+g.dot+" "), t.sgr("0"))
}

// Replay prints a resumed conversation in one write (after committing the
// live region): one syscall for the whole transcript, cheap on one core.
func (u *TermUI) Replay(msgs []Message, errs map[string]bool, n int, totals string) {
	view, hidden, nh := u.replayView(msgs, errs, n, termWidth()-1)
	if view == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.gap()
	if totals != "" {
		view += "\n" + totals
	}
	u.rawWrite(view)
	if nh > 0 {
		u.lastOut = uiLastOut{label: fmt.Sprintf("conversa anterior (%d entradas)", nh), text: hidden}
	}
}

// ---------- App: /resume, /sessions, /tree, /rename ----------

// pfPickResume is the -r value that opens the picker (`jazz -r` with no id).
const pfPickResume = "\x00pick"

// pfResumeArgs turns a bare -r/--resume (last word, or followed by another
// flag) into the picker request, so `jazz -r` works like `claude --resume`.
func pfResumeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i:]...)
		}
		if a == "-r" || a == "--resume" || a == "-resume" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") && args[i+1] != "-" {
				out = append(out, a+"="+pfPickResume)
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// pickerOn: the arrow-key list (session_picker: on, default) or the old
// numbered list (off; JAZZ_SESSION_PICKER wins).
func (a *App) pickerOn() bool {
	v := os.Getenv("JAZZ_SESSION_PICKER")
	if v == "" {
		v = a.cfg.SessionPicker
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no":
		return false
	}
	return a.ed != nil && a.ui.keys != nil
}

// resumeCmd is /resume and /sessions: an id resumes it, nothing opens the list.
func (a *App) resumeCmd(args []string) {
	if len(args) > 0 {
		a.resume(args[0])
		return
	}
	a.resumePick()
}

// resumePick opens the session list; Tab shows every project.
func (a *App) resumePick() bool {
	list := ListSessions(a.root)
	if len(list) == 0 {
		a.ui.Notice("nenhuma sessão salva nesta pasta")
		return false
	}
	if !a.pickerOn() {
		return a.resumeNumbered(list)
	}
	now := time.Now()
	here := "sessões em " + sessTildeDir(a.root)
	all := false
	sp := &pkSpec{Head: here, Hint: pkHint + " · tab: todas as pastas", Items: sessPickItems(list, a.agent.SessionID, false, now)}
	sp.Tab = func() ([]pkItem, string) {
		all = !all
		if all {
			sp.Hint = pkHint + " · tab: só esta pasta"
			return sessPickItems(ListAllSessions(), a.agent.SessionID, true, now), "sessões de todas as pastas"
		}
		sp.Hint = pkHint + " · tab: todas as pastas"
		return sessPickItems(ListSessions(a.root), a.agent.SessionID, false, now), here
	}
	i := a.ed.Pick(sp)
	if i < 0 {
		a.ui.Notice("nenhuma sessão escolhida")
		return false
	}
	si := sp.Items[i].Val.(SessionInfo)
	if filepath.Dir(si.Path) != sessionsDir(a.root) {
		dir := sessTildeDir(si.Cwd)
		a.ui.Notice("essa sessão é de " + dir + " — abra lá: jazz -C " + dir + " -r " + si.ID)
		return false
	}
	a.loadSession(si)
	return true
}

// resumeNumbered is the list without arrow keys (session_picker: off).
func (a *App) resumeNumbered(list []SessionInfo) bool {
	t := a.ui.T
	if len(list) > 15 {
		list = list[:15]
	}
	var b strings.Builder
	now := time.Now()
	for i, s := range list {
		fmt.Fprintf(&b, "  %s%2d%s %s%-9s%s %s %s(%d msgs · %s)%s\n", t.fg(cMagenta, 35), i+1, t.sgr("0"), t.fg(cGray, 90), sessAgo(s.Mod, now), t.sgr("0"), sanitizeTerm(s.Title), t.fg(cGray, 90), s.Msgs, s.Model, t.sgr("0"))
	}
	a.ui.print(b.String())
	if n := a.pickNumber("número (enter cancela)", len(list)); n > 0 {
		a.loadSession(list[n-1])
		return true
	}
	return false
}

// pickNumber reads a 1-based number from the input line (0 = cancelled).
func (a *App) pickNumber(label string, max int) int {
	t := a.ui.T
	sel, err := a.ed.ReadLine(t.fg(cGray, 90)+"  "+label+" "+t.G().prompt+" "+t.sgr("0"), len([]rune(label))+4)
	if err != nil || strings.TrimSpace(sel) == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscan(sel, &n); err != nil || n < 1 || n > max {
		a.ui.Error("número inválido")
		return 0
	}
	return n
}

// showResumed renders the loaded conversation, then says where it is.
func (a *App) showResumed(si SessionInfo) {
	if si.Path != "" {
		if fresh := sessListOne(si.Path); fresh.ID != "" {
			si = fresh
		}
	}
	a.ui.Replay(a.agent.Msgs, sessToolErrs(si.Path), replayLimit(a.cfg), replayTotals(a.ui.T, si, len(a.agent.Msgs)))
	title := a.agent.sess.Title()
	if title == "" {
		title = si.Title
	}
	a.ui.Notice(fmt.Sprintf("sessão %s retomada (%d mensagens): %s", a.agent.SessionID, len(a.agent.Msgs), title))
}

// sessListOne is the SessionInfo of one session file (zero when unreadable).
func sessListOne(path string) SessionInfo {
	for _, si := range sessListDir(filepath.Dir(path)) {
		if si.Path == path {
			return si
		}
	}
	return SessionInfo{}
}

// renameCmd is /rename [título]: without a title the current one is offered for editing.
func (a *App) renameCmd(argStr string) {
	s := a.agent.sess
	if s == nil {
		a.ui.Notice("esta conversa ainda não foi salva: mande a primeira mensagem e depois /rename")
		return
	}
	title := strings.TrimSpace(argStr)
	if title == "" {
		cur := s.Title()
		if cur == "" {
			cur = sessAutoTitle(firstUserText(a.agent.Msgs))
		}
		t := a.ui.T
		a.ed.Prefill(cur)
		line, err := a.ed.ReadLine(t.fg(cGray, 90)+"  título "+t.G().prompt+" "+t.sgr("0"), 10)
		if err != nil || strings.TrimSpace(line) == "" {
			a.ui.Notice("título mantido")
			return
		}
		title = line
	}
	s.SetTitle(title)
	a.ui.Notice("sessão " + a.agent.SessionID + ": " + s.Title())
}

func firstUserText(msgs []Message) string {
	for _, m := range msgs {
		if m.Role == "user" && !strings.HasPrefix(m.Content, "<summary>") {
			if t := rpUserText(m.Content); t != "" {
				return t
			}
		}
	}
	return ""
}

// treePick is /tree with no argument: the session tree in the picker; Enter
// continues from the chosen message (a new branch), like /tree <id>.
func (a *App) treePick() {
	rows, err := sessTreeRows(a.agent.sess.Path)
	if err != nil {
		a.ui.Error(err.Error())
		return
	}
	if len(rows) == 0 {
		a.ui.Notice("sessão vazia")
		return
	}
	if !a.pickerOn() {
		var lines []string
		for _, r := range rows {
			lines = append(lines, r.line())
		}
		a.ui.print(strings.Join(lines, "\n") + "\n\n/tree <id> continua a partir daquela mensagem (o ramo atual fica salvo)\n")
		return
	}
	sp := &pkSpec{Head: "árvore da sessão · enter continua dali (novo ramo)", Hint: pkHint}
	g := a.ui.T.G()
	for i, r := range rows {
		mark := "○"
		if r.Active {
			mark = "●"
		}
		if a.ui.T.ascii {
			mark = map[bool]string{true: "*", false: "o"}[r.Active]
		}
		it := pkItem{Title: strings.Repeat("  ", r.Depth) + mark + " " + r.Who + " " + g.dot + " " + r.Text, Right: r.ID, Key: r.ID, Val: r.ID}
		if r.Leaf {
			it.Mark = "atual"
			sp.Sel = i
		}
		sp.Items = append(sp.Items, it)
	}
	i := a.ed.Pick(sp)
	if i < 0 {
		a.ui.Notice("árvore: nada mudou")
		return
	}
	a.treeGo(sp.Items[i].Val.(string))
}

// treeGo continues the conversation from entry id and shows the branch.
func (a *App) treeGo(id string) {
	si := SessionInfo{ID: a.agent.SessionID, Path: a.agent.sess.Path}
	if err := a.agent.LoadSessionAt(si, id); err != nil {
		a.ui.Error(err.Error())
		return
	}
	a.ui.Replay(a.agent.Msgs, sessToolErrs(si.Path), replayLimit(a.cfg), "")
	a.ui.Notice(fmt.Sprintf("conversa agora continua de %s (%d mensagens); os arquivos não foram alterados — use /undo se precisar", id, len(a.agent.Msgs)))
}
