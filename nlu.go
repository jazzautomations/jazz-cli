package main

// nlu.go: the offline interpreter (docs/design/offline-brain.md §2). A short
// request in Portuguese or English becomes Intent{Name, Slots, Confidence,
// Evidence} with no model:
//
//  1. normalization: lowercase, no accents, slang and fixed phrases rewritten
//     to canonical words (nlu/lexicon.json "slang": "roda aí" → run), light
//     Portuguese/English stemming for the concept lookup;
//  2. concept tagging: every word (exact form first, then its stem) and fixed
//     phrase carries the concepts of the lexicon (open, run, test, git …);
//     a misspelled word is moved to the nearest lexicon word (fuzzy.go);
//  3. entity spotting: Aho-Corasick over the names of the world (project files,
//     environment entities from envprobe.go), then a fuzzy pass for the words
//     left over ("talscle" → the tailscale binary, "ngnix" → nginx);
//  4. semantic grammar (Phoenix/Snips style): each intent of nlu/intents.json
//     has rules over concepts and entity tags ("all", "any", "none"); the most
//     specific rule wins; slots are filled from typed entities or the words
//     no rule used;
//  5. a multinomial naive Bayes over character 3–5-grams, trained lazily from
//     the dataset's examples with integer log-probabilities, breaks ties and
//     classifies when no rule fires.
//
// Confidence below nuMinConf means "not understood": the caller asks or hands
// the request to the model. Entities are never invented: a slot keeps the
// user's words, and resolving them to a real thing (with a question when
// ambiguous) is the method's job (methods.go).

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed nlu/intents.json nlu/lexicon.json
var nuFS embed.FS

// nuMinConf is the confidence under which an intent is not acted on.
const nuMinConf = 0.6

// Intent is the interpreter's reading of one request.
type Intent struct {
	Name       string            `json:"name"` // "none" = not understood / out of domain
	Slots      map[string]string `json:"slots,omitempty"`
	Confidence float64           `json:"confidence"`    // calibrated: P(this reading is right) — what the brain acts on
	Raw        float64           `json:"raw,omitempty"` // the grammar's own score before nlu/calib.json
	Evidence   []string          `json:"evidence,omitempty"`
	Ents       []nuHit           `json:"entities,omitempty"`
	Missing    []string          `json:"missing,omitempty"`   // required slots nobody filled
	Hint       string            `json:"hint,omitempty"`      // test | symbol | file (cue words)
	Alt        []string          `json:"alt,omitempty"`       // runner-up intents
	Git        bool              `json:"git,omitempty"`       // the request names git/repo explicitly
	TypoOnly   bool              `json:"typo_only,omitempty"` // the intent stands only on a typo-repaired word (conf capped ≤ 0.5)
}

// Understood: a named intent with enough confidence to act on.
func (in Intent) Understood() bool {
	return in.Name != "" && in.Name != "none" && in.Confidence >= nuMinConf
}

// nuHit is one entity found in the text.
type nuHit struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Raw   string `json:"raw"`
	Exact bool   `json:"exact"`
	tok   int
}

// ---------- dataset ----------

type nuRule struct {
	All  []string `json:"all"`
	Any  []string `json:"any,omitempty"`
	None []string `json:"none,omitempty"`
	W    int      `json:"w,omitempty"`
}

type nuSlotDef struct {
	Kinds    []string `json:"kinds"`
	Required bool     `json:"required"`
	From     string   `json:"from,omitempty"`  // rename_from | rename_to
	Shape    string   `json:"shape,omitempty"` // name: the value must look like a name (or come with a cue word)
}

type nuIntentDef struct {
	Name      string               `json:"name"`
	Desc      string               `json:"desc"`
	Rules     []nuRule             `json:"rules"`
	Slots     map[string]nuSlotDef `json:"slots"`
	Examples  []string             `json:"examples"`
	Negatives []string             `json:"negatives"`
}

type nuWorldDef struct {
	Files      []string `json:"files"`
	Dirs       []string `json:"dirs"`
	Symbols    []string `json:"symbols"`
	Services   []string `json:"services"`
	SSHHosts   []string `json:"ssh_hosts"`
	TSPeers    []string `json:"ts_peers"`
	Containers []string `json:"containers"`
	Toolchains []string `json:"toolchains"`
	Bins       []string `json:"bins"`
	Ports      []int    `json:"ports"`
}

type nuDataset struct {
	World   nuWorldDef    `json:"world"`
	Intents []nuIntentDef `json:"intents"`
}

type nuLexiconDef struct {
	Fillers  []string            `json:"fillers"`
	Slang    map[string]string   `json:"slang"`
	Concepts map[string][]string `json:"concepts"`
}

type nuPhrase struct {
	words []string
	out   []string // slang: replacement words; concepts: concept names
}

// nuModel is the compiled interpreter (one per process).
type nuModel struct {
	data     nuDataset
	fillers  map[string]bool
	slang    map[string][]nuPhrase // first word -> phrases, longest first
	cphrase  map[string][]nuPhrase // multiword concept phrases
	exact    map[string][]string   // word -> concepts
	stem     map[string][]string   // stem -> concepts
	vocab    map[string]bool       // lexicon words >= 4 letters (typo repair)
	byName   map[string]int
	nbOnce   sync.Once
	nb       *nuNB
	loadErr  error
	cueConcs map[string]bool
	concs    map[string]bool   // concept names (slang may output them directly)
	aliases  map[string]string // owner's personal shortcuts: canon -> intent (alias.go, forge layer 1)
	embedN   []int             // embedded examples per intent — the NB prior counts only these (spec 0.14b: the overlay never moves the prior)
}

var (
	nuOnce sync.Once
	nuDef  *nuModel
)

// nuGet returns the embedded interpreter plus the owner's forge overlay
// (nlu_overlay.go: examples `jazz forge` accepted through the held-out gate).
// It must never run before pfConfigureFresh (main.go): --fresh turns the
// overlay off, and the first call fixes the model for the process.
func nuGet() *nuModel {
	nuOnce.Do(func() {
		lx, _ := nuFS.ReadFile("nlu/lexicon.json")
		ds, _ := nuFS.ReadFile("nlu/intents.json")
		nuDef = nuCompile(lx, ds)
		if nuDef.loadErr == nil && nuForgeOn() {
			nuLoadOverlay(nuDef)
			nuLoadAliases(nuDef)
			dsLoadNLU(nuDef) // intenções e atalhos aprendidos (distill.go)
		}
	})
	return nuDef
}

// nuCueConcepts are nouns that steer a slot (hint/kind) but are never its value.
var nuCueConcepts = []string{"filenoun", "symnoun", "tonoun", "message", "project", "dep", "files", "service", "container",
	"host", "port", "test", "version", "have", "running", "list", "status", "env", "error", "listen", "who", "process", "remote", "branch", "git"}

func nuCompile(lexJSON, dsJSON []byte) *nuModel {
	m := &nuModel{fillers: map[string]bool{}, slang: map[string][]nuPhrase{}, cphrase: map[string][]nuPhrase{},
		exact: map[string][]string{}, stem: map[string][]string{}, vocab: map[string]bool{}, byName: map[string]int{}, cueConcs: map[string]bool{}, concs: map[string]bool{}}
	var lx nuLexiconDef
	if err := json.Unmarshal(lexJSON, &lx); err != nil {
		m.loadErr = fmt.Errorf("nlu/lexicon.json: %w", err)
		return m
	}
	if err := json.Unmarshal(dsJSON, &m.data); err != nil {
		m.loadErr = fmt.Errorf("nlu/intents.json: %w", err)
		return m
	}
	for _, f := range lx.Fillers {
		m.fillers[f] = true
	}
	for k, v := range lx.Slang {
		w := strings.Fields(k)
		if len(w) == 0 {
			continue
		}
		m.slang[w[0]] = append(m.slang[w[0]], nuPhrase{words: w, out: strings.Fields(v)})
	}
	for _, l := range m.slang {
		sort.Slice(l, func(i, j int) bool { return len(l[i].words) > len(l[j].words) })
	}
	addC := func(mp map[string][]string, k, c string) {
		for _, x := range mp[k] {
			if x == c {
				return
			}
		}
		mp[k] = append(mp[k], c)
	}
	names := make([]string, 0, len(lx.Concepts))
	for c := range lx.Concepts {
		names = append(names, c)
	}
	sort.Strings(names) // deterministic concept order
	for _, c := range names {
		m.concs[c] = true
		for _, w := range lx.Concepts[c] {
			if strings.Contains(w, " ") {
				f := strings.Fields(w)
				m.cphrase[f[0]] = append(m.cphrase[f[0]], nuPhrase{words: f, out: []string{c}})
				continue
			}
			addC(m.exact, w, c)
			if s := nuStem(w); len(s) >= 3 {
				addC(m.stem, s, c)
			}
			if len(w) >= 4 {
				m.vocab[w] = true
			}
		}
	}
	for _, l := range m.cphrase {
		sort.Slice(l, func(i, j int) bool { return len(l[i].words) > len(l[j].words) })
	}
	m.embedN = make([]int, len(m.data.Intents))
	for i, it := range m.data.Intents {
		m.byName[it.Name] = i
		m.embedN[i] = len(it.Examples)
	}
	for _, c := range nuCueConcepts {
		m.cueConcs[c] = true
	}
	return m
}

// ---------- normalization ----------

// nuTok is one word of the request.
type nuTok struct {
	Raw    string   // as typed (case kept), or the slang phrase it replaced
	Fold   string   // folded (canonical word for slang)
	Quoted bool     // came from '...' or "..."
	Concs  []string // concepts
	Tags   []string // entity tags: @file @symbol @tool @host @service @container @number
	Filler bool
	Fixed  string // typo repair: the lexicon word it was moved to
	Weak   bool   // every concept this token carries is stem-derived (the word itself is not in the lexicon — same trust level as a typo repair)
	Slang  bool
}

func (t *nuTok) has(c string) bool {
	for _, x := range t.Concs {
		if x == c {
			return true
		}
	}
	for _, x := range t.Tags {
		if x == c {
			return true
		}
	}
	return false
}

// hasStrong: c is carried by real vocabulary — an entity tag or an exact/
// phrase/slang concept — not merely deduced from a suffix strip. A stem
// collision ("quente"→quent→blame) must never be the sole proof of intent.
func (t *nuTok) hasStrong(c string) bool {
	for _, x := range t.Tags {
		if x == c {
			return true
		}
	}
	if t.Weak {
		return false
	}
	for _, x := range t.Concs {
		if x == c {
			return true
		}
	}
	return false
}

// nuStem is a light PT/EN suffix stripper (one suffix, stem >= 3 letters —
// but >= 4 after a single-letter suffix: "fila"→fil must not reach the stem
// of "files"→fil, "toda"→tod must not reach the stem of "todos"→tod).
var nuSuffixes = []string{"amente", "mente", "acoes", "icoes", "coes", "cao", "ando", "endo", "indo", "aram", "eram", "iram",
	"ados", "adas", "idos", "idas", "ing", "ado", "ada", "ido", "ida", "ed", "ar", "er", "ir", "am", "em", "as", "es", "os", "a", "e", "o", "s"}

func nuStem(w string) string {
	if len(w) <= 3 {
		return w
	}
	for _, s := range nuSuffixes {
		min := 3
		if len(s) == 1 {
			min = 4
		}
		if len(w)-len(s) >= min && strings.HasSuffix(w, s) {
			return w[:len(w)-len(s)]
		}
	}
	return w
}

// nuTrimPunct cuts the punctuation around a word (keeps . / _ - : inside).
func nuTrimPunct(s string) string {
	s = strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '"', '\'', '`', '“', '”', '‘', '’', '(', ')', '[', ']', '{', '}', '<', '>', ',', ';', '!', '?', '…', '¿', '¡':
			return true
		}
		return false
	})
	for len(s) > 1 && (s[len(s)-1] == '.' || s[len(s)-1] == ':') {
		s = s[:len(s)-1]
	}
	return s
}

// nuTokenize splits text into words; '...' and "..." spans are one token.
func nuTokenize(text string) []nuTok {
	out := make([]nuTok, 0, 16)
	rs := []rune(text)
	i := 0
	for i < len(rs) {
		r := rs[i]
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if r == '"' || r == '\'' || r == '“' || r == '‘' {
			closeR := map[rune]rune{'"': '"', '\'': '\'', '“': '”', '‘': '’'}[r]
			j := i + 1
			for j < len(rs) && rs[j] != closeR && !(closeR == '"' && rs[j] == '”') {
				j++
			}
			// an apostrophe inside a word ("what's") is not a quote
			if j < len(rs) && j > i+1 && (i == 0 || unicode.IsSpace(rs[i-1])) {
				q := strings.TrimSpace(string(rs[i+1 : j]))
				if q != "" {
					out = append(out, nuTok{Raw: q, Fold: fzFold(q), Quoted: true, Tags: []string{"@quoted"}})
				}
				i = j + 1
				continue
			}
		}
		j := i
		for j < len(rs) && !unicode.IsSpace(rs[j]) {
			j++
		}
		w := nuTrimPunct(string(rs[i:j]))
		i = j
		if w == "" {
			continue
		}
		f := fzFold(w)
		if k := strings.IndexByte(f, '\''); k > 0 && !strings.ContainsAny(f, "./") { // what's -> what
			f, w = f[:k], w[:k]
		}
		out = append(out, nuTok{Raw: w, Fold: f})
	}
	return out
}

// normalize: slang phrases → canonical words, concept phrases, word
// concepts, fillers.
func (m *nuModel) normalize(toks []nuTok) []nuTok {
	out := make([]nuTok, 0, len(toks)+4)
	for i := 0; i < len(toks); {
		t := toks[i]
		if !t.Quoted {
			if ph, n := m.matchPhrase(m.slang, toks, i); n > 0 {
				raw := make([]string, 0, n)
				for k := i; k < i+n; k++ {
					raw = append(raw, toks[k].Raw)
				}
				for _, w := range ph.out {
					out = append(out, nuTok{Raw: strings.Join(raw, " "), Fold: w, Slang: true})
				}
				i += n
				continue
			}
		}
		out = append(out, t)
		i++
	}
	for i := 0; i < len(out); {
		if out[i].Quoted {
			i++
			continue
		}
		if ph, n := m.matchPhrase(m.cphrase, out, i); n > 0 {
			for k := i; k < i+n; k++ {
				out[k].Concs = append(out[k].Concs, ph.out...)
			}
			i += n
			continue
		}
		i++
	}
	for i := range out {
		t := &out[i]
		if t.Quoted || len(t.Concs) > 0 {
			continue
		}
		if t.Slang && m.concs[t.Fold] {
			t.Concs = append(t.Concs, t.Fold)
			continue
		}
		if c, ok := m.exact[t.Fold]; ok {
			t.Concs = append(t.Concs, c...)
			continue
		}
		// a name with an archive extension IS an archive: "o que tem
		// dentro do release.zip" wants the listing, not a tool card
		if nuArchiveName(t.Fold) {
			t.Concs = append(t.Concs, "archive")
			continue
		}
		if m.fillers[t.Fold] {
			t.Filler = true
			continue
		}
		if !nuNameShaped(t.Raw) {
			if c, ok := m.stem[nuStem(t.Fold)]; ok {
				t.Concs = append(t.Concs, c...)
				t.Weak = true // this word isn't in the lexicon; every concept it carries was deduced from a suffix strip
			}
		}
	}
	return out
}

func (m *nuModel) matchPhrase(idx map[string][]nuPhrase, toks []nuTok, i int) (nuPhrase, int) {
	for _, ph := range idx[toks[i].Fold] {
		if i+len(ph.words) > len(toks) {
			continue
		}
		ok := true
		for k, w := range ph.words {
			if toks[i+k].Quoted || toks[i+k].Fold != w {
				ok = false
				break
			}
		}
		if ok {
			return ph, len(ph.words)
		}
	}
	return nuPhrase{}, 0
}

// nuNameShaped: a path, file name, identifier or number — never stemmed or
// typo-repaired.
func nuNameShaped(s string) bool {
	if strings.ContainsAny(s, "./_\\:@=^$*+?[](){}|") {
		return true
	}
	digits, upperInside := false, false
	for i, r := range s {
		if r >= '0' && r <= '9' {
			digits = true
		}
		if i > 0 && unicode.IsUpper(r) {
			upperInside = true
		}
	}
	return digits || upperInside
}

// ---------- world (entities) ----------

// nuEnt is one name of the world.
type nuEnt struct {
	Kind string // file | test | dir | symbol | toolchain | bin | ssh_host | ts_peer | service | container | port
	Name string
	Key  string // folded
}

// nuWorld is the entity index the interpreter spots names with.
type nuWorld struct {
	ents []nuEnt
	ac   *acAuto
	pats [][]int32 // pattern -> entity indexes
}

// nuTagOf maps an entity kind to the tag rules use.
func nuTagOf(kind string) string {
	switch kind {
	case "file", "test", "dir":
		return "@file"
	case "symbol":
		return "@symbol"
	case "toolchain", "bin":
		return "@tool"
	case "ssh_host", "ts_peer":
		return "@host"
	case "service":
		return "@service"
	case "container":
		return "@container"
	case "port":
		return "@number"
	}
	return ""
}

// newNuWorld indexes ents (Aho-Corasick over the folded names).
func newNuWorld(ents []nuEnt) *nuWorld {
	w := &nuWorld{ents: ents}
	keys := map[string]int{}
	var pats []string
	for i := range ents {
		k := ents[i].Key
		if len(k) < 2 {
			continue
		}
		pi, ok := keys[k]
		if !ok {
			pi = len(pats)
			keys[k] = pi
			pats = append(pats, k)
			w.pats = append(w.pats, nil)
		}
		w.pats[pi] = append(w.pats[pi], int32(i))
	}
	w.ac = acBuild(pats)
	return w
}

// nuWorldFromDef is the fixed world of the dataset (tests, eval).
func nuWorldFromDef(d nuWorldDef) *nuWorld {
	var e []nuEnt
	add := func(kind, name string) { e = append(e, nuEnt{Kind: kind, Name: name, Key: fzFold(name)}) }
	dirs := map[string]bool{}
	addDir := func(name string) {
		base := name
		if j := strings.LastIndexByte(name, '/'); j >= 0 {
			base = name[j+1:]
		}
		key := fzFold(base)
		if len(key) < 2 || dirs[key] {
			return
		}
		dirs[key] = true
		e = append(e, nuEnt{Kind: "dir", Name: name, Key: key})
	}
	for _, f := range d.Files {
		kind := "file"
		base := f
		if j := strings.LastIndexByte(f, '/'); j >= 0 {
			base = f[j+1:]
		}
		stem := strings.TrimSuffix(base, fileExt(base))
		if fzKey(stem) != fzKey(niCore(stem)) {
			kind = "test"
		}
		add(kind, f)
		if base != f {
			e = append(e, nuEnt{Kind: kind, Name: f, Key: fzFold(base)})
		}
		if stem != base && len(stem) >= 3 {
			e = append(e, nuEnt{Kind: kind, Name: f, Key: fzFold(stem)})
		}
		// every parent of a listed path is a dir of the world
		for j := len(f) - 1; j > 0; j-- {
			if f[j] == '/' {
				addDir(f[:j])
			}
		}
	}
	for _, dir := range d.Dirs {
		addDir(dir)
	}
	for _, s := range d.Symbols {
		add("symbol", s)
	}
	for _, s := range d.Services {
		add("service", s)
	}
	for _, s := range d.SSHHosts {
		add("ssh_host", s)
	}
	for _, s := range d.TSPeers {
		add("ts_peer", s)
	}
	for _, s := range d.Containers {
		add("container", s)
	}
	for _, s := range d.Toolchains {
		add("toolchain", s)
	}
	for _, s := range d.Bins {
		add("bin", s)
	}
	for _, p := range d.Ports {
		add("port", strconv.Itoa(p))
	}
	return newNuWorld(e)
}

func fileExt(base string) string {
	if j := strings.LastIndexByte(base, '.'); j > 0 {
		return base[j:]
	}
	return ""
}

var (
	nuWMu    sync.Mutex
	nuWCache = map[string]*nuWorld{}
	nuWStamp = map[string]string{}
)

// nuRuntimeWorld is the project (files) plus the environment profile.
// Rebuilt when the file index or the profile changes.
func nuRuntimeWorld(root string, p *epProfile) *nuWorld {
	if p == nil {
		p = epPeek()
	}
	if p == nil {
		p = epGet(false)
	}
	var ix *niIndex
	if root != "" {
		ix = niGet(root)
	}
	stamp := fmt.Sprintf("%p|%p", p, ix)
	nuWMu.Lock()
	defer nuWMu.Unlock()
	if w := nuWCache[root]; w != nil && nuWStamp[root] == stamp {
		return w
	}
	ents := make([]nuEnt, 0, 1024)
	if ix != nil {
		dirs := map[string]bool{}
		for i := range ix.files {
			f := &ix.files[i]
			kind := "file"
			if f.Test {
				kind = "test"
			}
			// base name and stem only: a typed path is name-shaped and goes
			// to the resolver as is (keeps the automaton small on big repos)
			ents = append(ents, nuEnt{Kind: kind, Name: f.Rel, Key: fzFold(f.Base)})
			if stem := strings.TrimSuffix(f.Base, f.Ext); stem != f.Base && len(stem) >= 3 {
				ents = append(ents, nuEnt{Kind: kind, Name: f.Rel, Key: fzFold(stem)})
			}
			// the parents of every indexed path are dirs of the world, so
			// "a pasta web" tags an entity instead of a bare word
			for j := len(f.Rel) - 1; j > 0; j-- {
				if f.Rel[j] != '/' {
					continue
				}
				dir := f.Rel[:j]
				base := dir
				if k := strings.LastIndexByte(dir, '/'); k >= 0 {
					base = dir[k+1:]
				}
				key := fzFold(base)
				if len(key) < 2 || dirs[key] {
					continue
				}
				dirs[key] = true
				ents = append(ents, nuEnt{Kind: "dir", Name: dir, Key: key})
			}
		}
	}
	for _, e := range epEntities(p) {
		ents = append(ents, nuEnt{Kind: e.Kind, Name: e.Name, Key: fzFold(e.Name)})
	}
	w := newNuWorld(ents)
	nuWCache[root], nuWStamp[root] = w, stamp
	return w
}

// spot tags the tokens with the entities found by Aho-Corasick, then a fuzzy
// pass over the words nothing explained.
func (m *nuModel) spot(toks []nuTok, w *nuWorld) []nuHit {
	if w == nil {
		return nil
	}
	var hits []nuHit
	// joined text with token offsets
	var b strings.Builder
	starts := make([]int, len(toks))
	for i, t := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		starts[i] = b.Len()
		if t.Slang || t.Quoted {
			b.WriteString("\x00") // a canonical word or a quoted message never names an entity
			continue
		}
		b.WriteString(t.Fold)
	}
	text := b.String()
	tokAt := func(off int) int {
		k := sort.SearchInts(starts, off+1) - 1
		if k < 0 {
			k = 0
		}
		return k
	}
	tagged := make([]bool, len(toks))
	w.ac.find(text, func(pat, end int) {
		start := end - len(w.ac.pats[pat])
		if start > 0 && text[start-1] != ' ' || end < len(text) && text[end] != ' ' {
			return
		}
		ti, tj := tokAt(start), tokAt(end-1)
		for _, ei := range w.pats[pat] {
			e := w.ents[ei]
			if !m.tagOK(&toks[ti], e.Kind) {
				continue
			}
			tag := nuTagOf(e.Kind)
			for k := ti; k <= tj; k++ {
				toks[k].Tags = nuAddStr(toks[k].Tags, tag)
				if e.Kind == "test" {
					toks[k].Tags = nuAddStr(toks[k].Tags, "@test")
				}
				tagged[k] = true
			}
			hits = append(hits, nuHit{Kind: e.Kind, Name: e.Name, Raw: toks[ti].Raw, Exact: true, tok: ti})
		}
	})
	// numbers
	for i := range toks {
		if nuIsNumber(toks[i].Fold) {
			toks[i].Tags = nuAddStr(toks[i].Tags, "@number")
		}
	}
	// typo repair of concept words, then fuzzy entities for what is left
	for i := range toks {
		t := &toks[i]
		if tagged[i] || t.Quoted || t.Slang || t.Filler || len(t.Concs) > 0 || len(t.Fold) < 4 || nuNameShaped(t.Raw) {
			continue
		}
		bestE, bdE := -1, 1<<30
		var eHits []int32
		for ei := range w.ents {
			e := &w.ents[ei]
			switch e.Kind {
			case "file", "test", "dir", "symbol", "port":
				continue // project names are resolved by the method (nameindex), not guessed here
			}
			if e.Key == "" || e.Key[0] != t.Fold[0] {
				continue // typos keep the first letter
			}
			d, ok := niEnvScore(t.Fold, e.Key)
			if !ok || d == 0 || len(t.Fold) <= 4 && d > fzSlip {
				continue
			}
			switch {
			case d < bdE:
				bestE, bdE, eHits = ei, d, []int32{int32(ei)}
			case d == bdE:
				eHits = append(eHits, int32(ei))
			}
		}
		fixed, dC := m.repair(t.Fold)
		if fixed != "" && (bestE < 0 || dC <= bdE) {
			t.Fixed = fixed
			t.Concs = append(t.Concs, m.exact[fixed]...)
		}
		if bestE >= 0 && (fixed == "" || bdE <= dC) { // a tie keeps both readings: the rules decide
			for _, ei := range eHits {
				e := w.ents[ei]
				t.Tags = nuAddStr(t.Tags, nuTagOf(e.Kind))
				hits = append(hits, nuHit{Kind: e.Kind, Name: e.Name, Raw: t.Raw, Exact: false, tok: i})
			}
		}
	}
	return hits
}

// nuEntityFirst scopes a generic verb to the entity it talks about: in
// "docker ta rodando" or "talscle status" the word that names an installed
// tool is that tool, not the generic noun its name also happens to be in the
// lexicon (docker = container, tailscale = host). When the sentence carries a
// generic status verb (status, running, "como ta"), a token already tagged as
// a tool entity drops those noun concepts, so "tool_status" is not outvoted by
// container_list/list_hosts/git_status. Other nouns in the sentence ("status
// dos containers docker") keep their concepts and still veto.
func nuEntityFirst(toks []nuTok) {
	generic := false
	for i := range toks {
		if toks[i].has("status") || toks[i].has("running") || toks[i].has("howis") {
			generic = true
			break
		}
	}
	if !generic {
		return
	}
	for i := range toks {
		t := &toks[i]
		if !t.has("@tool") || t.has("@host") || t.has("@service") || t.has("@container") {
			continue
		}
		kept := t.Concs[:0:0]
		for _, c := range t.Concs {
			if c != "host" && c != "container" && c != "net" {
				kept = append(kept, c)
			}
		}
		t.Concs = kept
	}
}

// tagOK: a plain PATH binary named like a lexicon word ("open", "test",
// "find") is not an entity of the sentence; curated toolchains always are.
func (m *nuModel) tagOK(t *nuTok, kind string) bool {
	if kind != "bin" {
		return true
	}
	return len(t.Concs) == 0 && !t.Filler
}

// repair moves a misspelled word to the nearest lexicon word (unique best).
// Stricter than fzLimit: typos keep the first letter, a 4-letter word may
// only slip (neighbour key, transposition), a 5-letter one edit.
func (m *nuModel) repair(w string) (string, int) {
	lim := fzEdit + fzSlip
	switch n := len([]rune(w)); {
	case n <= 4:
		lim = fzSlip
	case n == 5:
		lim = fzEdit
	}
	best, bd, tie := "", 1<<30, false
	for v := range m.vocab {
		if v[0] != w[0] {
			continue
		}
		d := fzDist(w, v, lim)
		if d > lim {
			continue
		}
		switch {
		case d < bd:
			best, bd, tie = v, d, false
		case d == bd && v != best:
			if !nuSameConcepts(m.exact[v], m.exact[best]) {
				tie = true
			}
		}
	}
	if tie || best == "" {
		return "", 0
	}
	return best, bd
}

func nuSameConcepts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nuAddStr(l []string, s string) []string {
	if s == "" {
		return l
	}
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

func nuIsNumber(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------- grammar ----------

type nuMatch struct {
	intent int
	rule   int
	score  int
	used   map[string]bool
}

// groupOK: one "all" group ("a|b|@c") is present in the sentence.
func nuGroupOK(toks []nuTok, g string) (string, bool) {
	for _, alt := range strings.Split(g, "|") {
		for i := range toks {
			if toks[i].has(alt) {
				return alt, true
			}
		}
	}
	return "", false
}

// nuGroupOKVeto is the none-group check: a token naming a filesystem
// object (@file covers file/dir/test kinds) contributes its tag, not
// its word — "a pasta downloads" is a dir, not the act of downloading.
// Tools, ports, services and plain words keep their veto power: "make"
// the binary still vetoes as the verb it shares a name with.
func nuGroupOKVeto(toks []nuTok, g string) (string, bool) {
	for _, alt := range strings.Split(g, "|") {
		for i := range toks {
			if toks[i].has(alt) && !nuIsTag(toks[i].Tags, "@file") {
				return alt, true
			}
		}
	}
	return "", false
}

func nuIsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// rules returns the best rule of each intent that matches, most specific
// first, and the intents a rule explicitly excluded ("none" hit).
func (m *nuModel) rules(toks []nuTok) ([]nuMatch, map[int]bool) {
	var out []nuMatch
	vetoed := map[int]bool{}
	for ii := range m.data.Intents {
		it := &m.data.Intents[ii]
		best := nuMatch{intent: -1}
		for ri, r := range it.Rules {
			if len(r.All) == 0 {
				continue
			}
			used := map[string]bool{}
			ok := true
			for _, g := range r.All {
				c, has := nuGroupOK(toks, g)
				if !has {
					ok = false
					break
				}
				used[c] = true
			}
			if !ok {
				continue
			}
			for _, n := range r.None {
				if _, has := nuGroupOKVeto(toks, n); has {
					ok = false
					break
				}
			}
			if !ok {
				vetoed[ii] = true
				continue
			}
			score := 10*len(r.All) + r.W
			for _, a := range r.Any {
				if c, has := nuGroupOK(toks, a); has {
					score += 5
					used[c] = true
					break
				}
			}
			if score > best.score {
				best = nuMatch{intent: ii, rule: ri, score: score, used: used}
			}
		}
		if best.intent >= 0 {
			out = append(out, best)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	for _, x := range out {
		delete(vetoed, x.intent)
	}
	return out, vetoed
}

// ---------- parse ----------

// nuTaskEvidence reports whether the tokens carry anything a task could be
// made of: a concept, an entity tag, a quoted string, a name-shaped word
// (path, identifier, number) or a spotted entity. A request that is only
// fillers, greetings and profanity has none — Parse marks it "chitchat"
// instead of letting the classifier guess an intent for it ("porra" scored
// list_ports, "oi" is not a git_status), and brSplitMulti drops such parts
// from compound requests.
func nuTaskEvidence(toks []nuTok, hits []nuHit) bool {
	if len(hits) > 0 {
		return true
	}
	for i := range toks {
		t := &toks[i]
		if t.Quoted || len(t.Concs) > 0 || len(t.Tags) > 0 || nuNameShaped(t.Raw) {
			return true
		}
	}
	return false
}

// taskEvidence applies the non-task gate to a raw text (one part of a
// compound request) without the grammar/classifier work of Parse.
func (m *nuModel) taskEvidence(text string, w *nuWorld) bool {
	if m == nil || m.loadErr != nil {
		return true
	}
	toks := m.normalize(nuTokenize(text))
	hits := m.spot(toks, w)
	return nuTaskEvidence(toks, hits)
}

// Parse interprets one request against world w (nil = no entities).
func (m *nuModel) Parse(text string, w *nuWorld) Intent {
	in := Intent{Name: "none"}
	if m == nil || m.loadErr != nil {
		if m != nil {
			in.Evidence = []string{m.loadErr.Error()}
		}
		return in
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 400 {
		return in
	}
	toks := m.normalize(nuTokenize(text))
	hits := m.spot(toks, w)
	nuEntityFirst(toks)
	in.Ents = hits
	if len(m.aliases) > 0 {
		canon := nuCanonText(toks)
		name, ok := m.aliases[canon]
		if !ok && len(hits) > 0 {
			// a learned masked shape ("comprime @dir") hits any variant —
			// "comprime src" — the literal canon never does (distill.go)
			if sc := nuShapeCanon(toks, hits); sc != canon {
				name, ok = m.aliases[sc]
			}
		}
		if ok {
			// the owner's personal shortcut (alias.go): his word, his intent.
			// It classifies, never authorizes — a mutating target still hits
			// the permission guardrail at execution.
			in.Name = name
			in.Confidence = 0.97
			in.Evidence = append(in.Evidence, "apelido do dono → "+name)
			// the shortcut fills slots too: an intent with a schema gets
			// the values of the entities the text named (distill slots)
			if di, has := m.byName[name]; has && len(m.data.Intents[di].Slots) > 0 {
				used := map[string]bool{}
				matches, _ := m.rules(toks)
				best, bs := -1, 0
				for i, x := range matches {
					if x.intent == di && (best < 0 || x.score > bs) {
						best, bs = i, x.score
					}
				}
				if best >= 0 {
					used = matches[best].used
				}
				in.Slots, in.Missing, in.Hint, _ = m.fill(&m.data.Intents[di], toks, hits, used, w)
			}
			return in
		}
	}
	for i := range toks {
		if toks[i].Fixed != "" {
			in.Evidence = append(in.Evidence, fmt.Sprintf("'%s' lido como '%s' (erro de digitação)", toks[i].Raw, toks[i].Fixed))
		}
	}
	matches, vetoed := m.rules(toks)
	canon := nuCanonText(toks)
	var choice = -1
	var conf float64
	switch {
	case len(matches) == 0:
		if !nuTaskEvidence(toks, hits) {
			in.Hint = "chitchat"
			in.Evidence = append(in.Evidence, "sem evidência de tarefa (saudação, desabafo ou ruído)")
			return in
		}
		cls, p := m.classify(canon, nil)
		if cls < 0 {
			return in
		}
		// no rule: the classifier alone is weaker evidence, and an intent a
		// rule explicitly excluded ("conecta no wifi" is not connect_host) weaker still
		choice, conf = cls, p*0.75
		if vetoed[cls] {
			conf *= 0.5
		}
		in.Evidence = append(in.Evidence, fmt.Sprintf("nenhuma regra; classificador: %s p=%.2f", m.data.Intents[cls].Name, p))
	case len(matches) == 1 || matches[0].score > matches[1].score:
		choice = matches[0].intent
		conf = 0.9
		cls, p := m.classify(canon, nil)
		if cls == choice {
			conf = 0.97
		} else if cls >= 0 && m.data.Intents[cls].Name == "none" && p > 0.9 {
			conf = 0.7
		}
		in.Evidence = append(in.Evidence, fmt.Sprintf("regra %s#%d (pontos %d)", m.data.Intents[choice].Name, matches[0].rule+1, matches[0].score))
		for _, x := range matches[1:] {
			if len(in.Alt) < 3 {
				in.Alt = append(in.Alt, m.data.Intents[x.intent].Name)
			}
		}
	default:
		var tied []int
		for _, x := range matches {
			if x.score == matches[0].score {
				tied = append(tied, x.intent)
			}
		}
		cls, p := m.classify(canon, tied)
		choice = cls
		conf = 0.55 + 0.45*p
		var names []string
		for _, t := range tied {
			names = append(names, m.data.Intents[t].Name)
			if t != cls && len(in.Alt) < 3 {
				in.Alt = append(in.Alt, m.data.Intents[t].Name)
			}
		}
		in.Evidence = append(in.Evidence, fmt.Sprintf("empate entre %s; classificador escolheu %s p=%.2f", strings.Join(names, ", "), m.data.Intents[cls].Name, p))
	}
	if choice < 0 {
		return in
	}
	def := &m.data.Intents[choice]
	in.Name = def.Name
	for i := range toks {
		if toks[i].has("git") {
			in.Git = true
		}
	}
	used := map[string]bool{}
	for _, x := range matches {
		if x.intent == choice {
			used = x.used
		}
	}
	var unfed int
	in.Slots, in.Missing, in.Hint, unfed = m.fill(def, toks, hits, used, w)
	if unfed > 0 {
		conf *= 0.5 // a free word is not a path, a unit or a tool just because it sat there
		in.Evidence = append(in.Evidence, fmt.Sprintf("%d palavra(s) solta(s) não parecem nome do slot", unfed))
	}
	// a mutation verb ("apaga", "reinicia", "move") is never answered by a
	// read-only intent: refuse the read instead of showing the wrong table.
	// A word that also names a read concept ("mudaram" = diff+mutate) is
	// excused — the sentence may legitimately be about the changed state.
	if brLevel0Intents[def.Name] {
		// the concepts this intent's grammar speaks about
		concOf := map[string]bool{}
		for _, r := range def.Rules {
			for _, g := range r.All {
				for _, alt := range strings.Split(g, "|") {
					concOf[alt] = true
				}
			}
			for _, g := range r.Any {
				for _, alt := range strings.Split(g, "|") {
					concOf[alt] = true
				}
			}
		}
		mut := ""
		opn := ""
		for i := range toks {
			t := &toks[i]
			if t.Quoted || t.Filler || nuToWords[t.Fold] || t.Fixed != "" || len(t.Tags) > 0 {
				continue // a typo guess or an entity name is a noun, not an order
			}
			m, other, op := false, false, false
			for _, c := range t.Concs {
				if nuMutConcepts[c] && !used[c] {
					m = true
				} else if nuOpinionConcepts[c] && !used[c] {
					op = true
				} else if concOf[c] {
					other = true // the same word feeds this intent's grammar
				}
			}
			if m && !other {
				mut = t.Fold
			}
			if op && !other {
				opn = t.Fold
			}
		}
		if mut != "" {
			conf *= 0.5
			in.Evidence = append(in.Evidence, fmt.Sprintf("verbo de mutação '%s' não combina com leitura", mut))
		}
		// a judgment phrase ("o que tu acha", "what would you") asks for the
		// model's take, not a level-0 answer — the offline brain must never
		// speak where an opinion was requested
		if opn != "" {
			conf *= 0.5
			in.Evidence = append(in.Evidence, fmt.Sprintf("frase de opinião '%s' não é comando", opn))
		}
	}
	// typo-only: the rule fired, but every concept/tag it relied on is carried
	// only by a token we typo-repaired, and no correctly-spelled token names a
	// real entity (@file/@host/@tool/…) to corroborate it. The intent then
	// stands on spelling guesses alone, so we cap confidence under nuMinConf
	// (and under brLevel0Min, so the online path never short-circuits on it)
	// and let the caller offer a one-key confirmation instead of acting.
	// Structural, not a token count: a named entity the rule did not formally
	// consume ("explca o parser.go") still corroborates and keeps the intent.
	// weak-only: the rule fired, but every concept it relied on is carried
	// only by a guessed token (typo-repaired or stem-derived — "fila"→fil,
	// "quente"→quent) and no correctly-spelled token names a real entity
	// (@file/@host/@tool/…) to corroborate it. The intent then stands on
	// guesses alone, so we cap confidence under nuMinConf (and under
	// brLevel0Min, so the online path never short-circuits on it) and let
	// the caller offer a one-key confirmation instead of acting.
	// Structural, not a token count: a named entity the rule did not
	// formally consume ("explca o parser.go") still corroborates.
	if len(used) > 0 {
		weakOnly, guessed := true, false
		for c := range used {
			for i := range toks {
				if toks[i].Fixed != "" || toks[i].Weak {
					guessed = true
				}
				if toks[i].Fixed == "" && toks[i].hasStrong(c) {
					weakOnly = false
					break
				}
			}
			if !weakOnly {
				break
			}
		}
		if weakOnly {
			for i := range toks {
				if toks[i].Fixed == "" && !toks[i].Weak && len(toks[i].Tags) > 0 {
					weakOnly = false
					break
				}
			}
		}
		if weakOnly {
			in.TypoOnly = true
			conf = math.Min(conf, 0.5)
			if guessed {
				in.Evidence = append(in.Evidence, "baseado só em palavra deduzida (correção ou radical)")
			} else {
				in.Evidence = append(in.Evidence, "baseado só em correção de digitação")
			}
		}
	}
	for name, sd := range def.Slots {
		if v := in.Slots[name]; sd.Shape == "name" && v != "" && in.Hint == "" && !nuNameShaped(v) {
			conf *= 0.6 // "escreve um poema": no file name, no cue word
			in.Evidence = append(in.Evidence, fmt.Sprintf("'%s' não parece nome de arquivo", v))
		}
	}
	if res := m.residualCount(toks, used); res >= 3 {
		conf *= 0.6 // a long free-form request is a job for the model, not a template
		in.Evidence = append(in.Evidence, fmt.Sprintf("%d palavras fora da gramática: pedido livre", res))
	}
	in.Raw = math.Round(conf*100) / 100
	// the shipped calibration turns the score into a probability (nlu_calib.go);
	// a typo-only reading stays capped so the confirm rule keeps its meaning
	conf = nuCalibApply(in.Raw)
	if in.TypoOnly {
		conf = math.Min(conf, 0.5)
	}
	in.Confidence = math.Round(conf*100) / 100
	if def.Name == "none" {
		in.Confidence, in.Raw = 0, 0
	}
	return in
}

// nuCanonText is the normalized text the classifier sees.
func nuCanonText(toks []nuTok) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		f := t.Fold
		if t.Fixed != "" {
			f = t.Fixed
		}
		b.WriteString(f)
	}
	return b.String()
}

// nuShapeCanon is nuCanonText with every token a spotted entity covers
// replaced by "@<kind>" — "comprime docs" reads "comprime @dir". Two
// requests that differ only in entity values share the shape (plan-cache
// merge, learned-alias lookup and the distill slot proof all key on it).
func nuShapeCanon(toks []nuTok, hits []nuHit) string {
	cover := make([]int, len(toks))
	for i := range cover {
		cover[i] = -1
	}
	for hi, h := range hits {
		n := len(strings.Fields(fzFold(h.Name)))
		if n < 1 {
			n = 1
		}
		for j := h.tok; j < len(toks) && j < h.tok+n; j++ {
			if cover[j] < 0 {
				cover[j] = hi
			}
		}
	}
	var out []string
	prev := -1
	for i, t := range toks {
		h := cover[i]
		if h < 0 {
			f := t.Fold
			if t.Fixed != "" {
				f = t.Fixed
			}
			out = append(out, f)
			prev = -1
			continue
		}
		if h != prev { // adjacent entities of one kind keep their slots apart
			out = append(out, "@"+hits[h].Kind)
			prev = h
		}
	}
	return strings.Join(out, " ")
}

// nuVerbConcepts are actions: a verb of another clause ("roda os testes e
// me mostra …") is never the value of a slot.
var nuVerbConcepts = map[string]bool{"open": true, "explain": true, "find": true, "run": true, "build": true, "lint": true, "format": true,
	"commit": true, "push": true, "pull": true, "checkout": true, "stash": true, "undo": true, "rename": true, "create": true, "delete": true,
	"fix": true, "install": true, "update": true, "connect": true, "start": true, "stop": true, "restart": true, "kill": true}

// nuMutConcepts mark a request that changes something: delete, move,
// restart, install, enable... A read-only level-0 intent must never answer
// one — an honest "not understood" beats a confident wrong listing.
var nuMutConcepts = map[string]bool{
	"delete": true, "create": true, "install": true, "update": true, "fix": true,
	"start": true, "stop": true, "restart": true, "kill": true, "rename": true,
	"format": true, "build": true, "compress": true,
	"mutate": true, "recover": true, "extract": true, "enable": true,
}

// nuOpinionConcepts mark an utterance about the assistant's own judgment
// ("o que tu acha", "o que tu melhoraria", "what would you change") —
// conversation for the model, never a level-0 command the brain may
// answer. Same treatment as mutation verbs: the read still classifies,
// it just can't act on an opinion question.
var nuOpinionConcepts = map[string]bool{
	"opinion": true,
}

// nuWorldKinds are slot kinds that name a real thing: a file or dir of the
// project, a service of the machine, an installed tool, a host. A word
// that fills one of these slots has to look like that thing — a spotted
// entity, a quoted name, a name-shaped word, or a name the index resolves
// — otherwise the slot stays empty and the reading loses confidence
// instead of inventing a path, a unit or a tool out of a free word.
var nuWorldKinds = map[string]bool{
	"file": true, "test": true, "dir": true, "symbol": true,
	"service": true, "container": true, "toolchain": true, "bin": true,
	"ssh_host": true, "ts_peer": true, "port": true, "number": true,
}

// nuWorldOnly: every kind the slot accepts names a thing of the world, so
// free text is not a candidate.
func nuWorldOnly(kinds map[string]bool) bool {
	if len(kinds) == 0 {
		return false
	}
	for k := range kinds {
		if !nuWorldKinds[k] {
			return false
		}
	}
	return true
}

// nuSvcKinds are slot kinds naming a managed unit or a machine: a leftover
// word is never one — it must come tagged or resolve in the world.
var nuSvcKinds = map[string]bool{"service": true, "container": true, "ssh_host": true, "ts_peer": true}

// nuSvcOnly: every kind the slot accepts is a service/host kind.
func nuSvcOnly(kinds map[string]bool) bool {
	if len(kinds) == 0 {
		return false
	}
	for k := range kinds {
		if !nuSvcKinds[k] {
			return false
		}
	}
	return true
}

// slotTokOK: a bare word may fill a world-kind slot only when it looks
// like a name of that world — name-shaped (parser.go, internal/db), a
// quoted name (file-ish kinds only), or a project name the fuzzy resolver
// would resolve ("parsr" for parser.go). Services, hosts and tools are
// already tagged by spot when they exist, so a leftover word is never
// one of them; a bare name does name a tool candidate ("tenho o kubectl
// instalado?" — checking existence is the script's job, not the index's).
func (m *nuModel) slotTokOK(kinds map[string]bool, t *nuTok, w *nuWorld) bool {
	fileish := kinds["file"] || kinds["test"] || kinds["dir"] || kinds["symbol"]
	// a pure extension word (".conf") is a filter, never a file/dir name —
	// "arquivos .conf" describes a set of files, not a directory ".conf"
	if fileish && nuPureExt(t.Raw) {
		return false
	}
	if t.Quoted {
		if fileish {
			return true
		}
		if w == nil {
			return false
		}
		for ei := range w.ents {
			e := &w.ents[ei]
			if nuKindAllowed(kinds, e.Kind) && e.Key == t.Fold {
				return true
			}
		}
		return false
	}
	if nuNameShaped(t.Raw) {
		return true
	}
	if !fileish && !nuSvcOnly(kinds) && len(t.Concs) == 0 && (kinds["toolchain"] || kinds["bin"]) {
		return true
	}
	if w == nil || !fileish || len(t.Fold) < 3 {
		return false
	}
	lim := fzLimit(len([]rune(t.Fold)))
	for ei := range w.ents {
		e := &w.ents[ei]
		switch e.Kind {
		case "file", "test", "dir", "symbol":
		default:
			continue
		}
		if !nuKindAllowed(kinds, e.Kind) || e.Key == "" || e.Key[0] != t.Fold[0] {
			continue
		}
		if fzDist(t.Fold, e.Key, lim) <= lim {
			return true
		}
	}
	return false
}

// nuPureExt: the token is just a dot-extension (".conf", ".log") — a file
// filter, not a file name. A real ".git" dir is entity-tagged upstream and
// never reaches the leftover-slot check.
func nuPureExt(raw string) bool {
	if len(raw) < 3 || raw[0] != '.' {
		return false
	}
	for _, r := range raw[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// residual: the words that are not fillers and that no rule or cue explained.
func (m *nuModel) residual(toks []nuTok, used map[string]bool) []int {
	var out []int
	for i := range toks {
		t := &toks[i]
		if t.Filler || t.Slang {
			continue
		}
		if t.Quoted {
			out = append(out, i)
			continue
		}
		explained := false
		for _, c := range t.Concs {
			if used[c] || m.cueConcs[c] || nuVerbConcepts[c] {
				explained = true
			}
		}
		if !explained {
			out = append(out, i)
		}
	}
	return out
}

func (m *nuModel) residualCount(toks []nuTok, used map[string]bool) int {
	n := 0
	for _, i := range m.residual(toks, used) {
		if len(toks[i].Tags) == 0 && !toks[i].Quoted && len(toks[i].Concs) == 0 {
			n++
		}
	}
	return n
}

var nuToWords = map[string]bool{"para": true, "pra": true, "pro": true, "to": true, "por": true, "->": true, "=>": true, "em": true}

// fill extracts the slots of def. It also counts unfed — free words a
// world-kind slot refused — so Parse can lower confidence instead of
// inventing values. A refused word only counts when the slot needed it
// (required, or a service/host kind — a unit is never a leftover noun)
// and the word carries no concept of its own: "arquivo", "zip" and
// "conteúdo" are phrasing, not failed names.
func (m *nuModel) fill(def *nuIntentDef, toks []nuTok, hits []nuHit, used map[string]bool, w *nuWorld) (map[string]string, []string, string, int) {
	slots := map[string]string{}
	var missing []string
	hint := ""
	unfed := 0
	for i := range toks {
		switch {
		case toks[i].has("test") && !used["test"]:
			hint = "test"
		case toks[i].has("symnoun") && hint == "":
			hint = "symbol"
		case toks[i].has("filenoun") && hint == "":
			hint = "file"
		case toks[i].has("files") && hint == "":
			hint = "dir"
		}
	}
	if used["test"] && def.Name != "run_tests" {
		hint = "test"
	}
	res := m.residual(toks, used)
	taken := map[int]bool{}
	names := make([]string, 0, len(def.Slots))
	for k := range def.Slots {
		names = append(names, k)
	}
	sort.Strings(names)
	// Two passes: "from"-derived slots resolve first — the helpers mark their
	// tokens taken, so generic fills can't steal them (".log" must reach ext
	// before dir sees a name-shaped word; "10MB" must reach min_size before
	// limit sees digits).
	for _, helperPhase := range []bool{true, false} {
		for _, name := range names {
			sd := def.Slots[name]
			if (sd.From != "") != helperPhase {
				continue
			}
			v := ""
			switch {
			case sd.From == "rename_from" || sd.From == "rename_to":
				v = nuRenamePart(toks, res, sd.From == "rename_to")
			case strings.HasPrefix(sd.From, "find:"):
				v = nuFindArg(toks, sd.From[5:], taken)
			case strings.HasPrefix(sd.From, "perm:"):
				v = nuPermArg(toks, sd.From[5:], taken)
			case sd.From == "extract:pat":
				v = nuExtractPat(toks)
			case strings.HasPrefix(sd.From, "slice:"):
				v = nuSliceArg(toks, sd.From[6:], taken)
			case sd.From == "svc:name":
				v = nuSvcName(toks, taken)
			case sd.From == "disk:path":
				v = nuDiskPath(toks, taken)
			}
			// a "x:y" From is helper-or-nothing: the generic fallbacks would
			// stuff leftovers into enum/flag slots (e.g. a bare "10" into tail)
			derived := v == "" && strings.Contains(sd.From, ":")
			kinds := map[string]bool{}
			for _, k := range sd.Kinds {
				kinds[k] = true
			}
			strict := nuWorldOnly(kinds)
			countUnfed := sd.Required || nuSvcOnly(kinds)
			if v == "" && kinds["message"] {
				v = nuMessage(toks)
			}
			if v == "" && !derived {
				// typed entity found in the text (exact first)
				for _, exact := range []bool{true, false} {
					for _, h := range hits {
						if h.Exact != exact || taken[h.tok] || !nuKindAllowed(kinds, h.Kind) {
							continue
						}
						v = h.Name
						if h.Kind == "file" || h.Kind == "test" || h.Kind == "dir" {
							v = toks[h.tok].Raw // keep what was typed: the method resolves it with context
						}
						taken[h.tok] = true
						break
					}
					if v != "" {
						break
					}
				}
			}
			if v == "" && !derived && (kinds["number"] || kinds["port"]) {
				for i := range toks {
					if nuIsNumber(toks[i].Fold) && !taken[i] {
						v, taken[i] = toks[i].Fold, true
						break
					}
				}
			}
			if v == "" && !derived {
				// the words no rule explained, as typed
				var parts []string
				for _, i := range res {
					if taken[i] {
						continue
					}
					if strict {
						if nuIsNumber(toks[i].Fold) || len(toks[i].Tags) > 0 || toks[i].Quoted {
							continue // a number, an entity of another kind or a quoted literal is not a free name
						}
						if m.slotTokOK(kinds, &toks[i], w) {
							parts = append(parts, toks[i].Raw)
							taken[i] = true
							break
						}
						if countUnfed && len(toks[i].Concs) == 0 {
							unfed++
						}
						continue
					}
					if toks[i].Quoted {
						parts = []string{toks[i].Raw}
						taken[i] = true
						break
					}
					if nuIsNumber(toks[i].Fold) && !(kinds["number"] || kinds["text"]) {
						continue
					}
					parts = append(parts, toks[i].Raw)
					taken[i] = true
					if !kinds["text"] || len(parts) >= 4 {
						break
					}
				}
				v = strings.Join(parts, " ")
			}
			if v == "" && sd.Required && !derived {
				// a word whose concept no rule used ("instala o git": git)
				for i := len(toks) - 1; i >= 0; i-- {
					t := &toks[i]
					if t.Filler || t.Slang || taken[i] {
						continue
					}
					usedTok := false
					for _, c := range t.Concs {
						if used[c] {
							usedTok = true
						}
					}
					if !usedTok && len(t.Concs) > 0 && !nuAllCue(t, m) {
						if strict && !m.slotTokOK(kinds, t, w) {
							continue
						}
						v, taken[i] = t.Raw, true
						break
					}
				}
			}
			if v != "" {
				slots[name] = v
			} else if sd.Required {
				missing = append(missing, name)
			}
		}
	}
	return slots, missing, hint, unfed
}

// FillAs re-reads text under one intent's slot schema: chain sub-tasks
// ("apaga o arquivo mais novo da pasta downloads" read as file_find
// yields dir=downloads, sort=mtime) feed the producer steps of a plan
// chain. It is a fill, not a verdict: the primary parse already chose
// the intent; this only extracts the producer's view of the same words.
func (m *nuModel) FillAs(intent, text string, w *nuWorld) map[string]string {
	if m == nil || m.loadErr != nil {
		return nil
	}
	di, ok := m.byName[intent]
	if !ok {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 400 {
		return nil
	}
	toks := m.normalize(nuTokenize(text))
	hits := m.spot(toks, w)
	nuEntityFirst(toks)
	matches, _ := m.rules(toks)
	used := map[string]bool{}
	best, bestScore := -1, 0
	for i, x := range matches {
		if x.intent == di && (best < 0 || x.score > bestScore) {
			best, bestScore = i, x.score
		}
	}
	if best >= 0 {
		used = matches[best].used
	}
	slots, _, _, _ := m.fill(&m.data.Intents[di], toks, hits, used, w)
	return slots
}

func nuAllCue(t *nuTok, m *nuModel) bool {
	for _, c := range t.Concs {
		if !m.cueConcs[c] || c == "git" || c == "container" || c == "host" || c == "service" {
			return false
		}
	}
	return true
}

func nuKindAllowed(kinds map[string]bool, k string) bool {
	if kinds[k] {
		return true
	}
	switch k {
	case "test":
		return kinds["file"]
	case "file":
		return kinds["test"]
	case "toolchain":
		return kinds["bin"]
	case "ts_peer":
		return kinds["ssh_host"]
	case "port":
		return kinds["number"]
	}
	return false
}

// nuRenamePart: "renomeia A para B" → A (from) or B (to).
func nuRenamePart(toks []nuTok, res []int, to bool) string {
	mark := -1
	for i := range toks {
		if nuToWords[toks[i].Fold] && i > 0 {
			mark = i
		}
	}
	if mark < 0 {
		return ""
	}
	if to {
		for i := mark + 1; i < len(toks); i++ {
			if !toks[i].Filler && !nuToWords[toks[i].Fold] {
				return toks[i].Raw
			}
		}
		return ""
	}
	last := ""
	for _, i := range res {
		if i < mark && !nuToWords[toks[i].Fold] {
			last = toks[i].Raw
		}
	}
	return last
}

// nuMessage: a quoted text, or what follows "mensagem"/"msg"/"-m"/":".
func nuMessage(toks []nuTok) string {
	for _, t := range toks {
		if t.Quoted {
			return t.Raw
		}
	}
	for i := range toks {
		if toks[i].has("message") || strings.HasSuffix(toks[i].Raw, ":") {
			var parts []string
			for k := i + 1; k < len(toks); k++ {
				parts = append(parts, toks[k].Raw)
			}
			return strings.TrimSpace(strings.Join(parts, " "))
		}
	}
	return ""
}

// ---------- naive Bayes over char n-grams ----------

const (
	nuBuckets = 1 << 14
	nuScale   = 256 // log-probabilities in 1/256 nat
	nuBeta    = 6.0 // posterior sharpness per n-gram
)

// nuNB is a multinomial naive Bayes with hashed character 3–5-grams and
// integer log-probabilities (one int16 table: classes × buckets).
type nuNB struct {
	classes []int // intent indexes
	table   []int16
	prior   []int32
}

// nuNgrams calls fn with the FNV-1a hash bucket of each 3–5-gram of " s ".
func nuNgrams(s string, fn func(b uint32)) {
	b := make([]byte, 0, len(s)+2)
	b = append(b, ' ')
	b = append(b, s...)
	b = append(b, ' ')
	for n := 3; n <= 5; n++ {
		for i := 0; i+n <= len(b); i++ {
			h := uint32(2166136261)
			for k := i; k < i+n; k++ {
				h ^= uint32(b[k])
				h *= 16777619
			}
			h ^= uint32(n)
			fn(h & (nuBuckets - 1))
		}
	}
}

// nuTrain builds the classifier from the examples (canonical text).
func (m *nuModel) nuTrain(skip func(intent, ex int) bool) *nuNB {
	nb := &nuNB{}
	counts := make([][]uint16, 0, len(m.data.Intents))
	totals := make([]int, 0, len(m.data.Intents))
	nex := make([]int, 0, len(m.data.Intents))
	for ii, it := range m.data.Intents {
		if len(it.Examples) == 0 {
			continue
		}
		c := make([]uint16, nuBuckets)
		tot, n := 0, 0
		for ei, ex := range it.Examples {
			if skip != nil && skip(ii, ei) {
				continue
			}
			n++
			nuNgrams(nuCanonText(m.normalize(nuTokenize(ex))), func(b uint32) {
				if c[b] < 65000 {
					c[b]++
				}
				tot++
			})
		}
		nb.classes = append(nb.classes, ii)
		counts = append(counts, c)
		totals = append(totals, tot)
		nex = append(nex, n)
	}
	// Frozen prior (spec 0.14b item 7): only the embedded examples set the
	// class prior — overlay/forge examples never raise it (the real cause of
	// "forgetting" is prior drift, so the prior is pinned to the grammar).
	// Held-out skips (the forge's split) still lower it: the prior tracks
	// what was actually trained from the embedded set.
	capped := func(ci int) int {
		ii := nb.classes[ci]
		if len(m.embedN) == len(m.data.Intents) && nex[ci] > m.embedN[ii] {
			return m.embedN[ii]
		}
		return nex[ci]
	}
	allEx := 0
	for ci := range nex {
		allEx += capped(ci)
	}
	nb.table = make([]int16, len(nb.classes)*nuBuckets)
	nb.prior = make([]int32, len(nb.classes))
	const alpha = 0.5
	for ci := range nb.classes {
		den := float64(totals[ci]) + alpha*nuBuckets
		row := nb.table[ci*nuBuckets : (ci+1)*nuBuckets]
		// Quase todos os baldes são zero: o valor deles é o mesmo para a
		// classe inteira, então math.Log roda uma vez por classe mais uma vez
		// por balde ocupado (no Pentium M eram 786 mil logs por partida).
		zero := math.Log((float64(0)+alpha)/den) * nuScale
		if zero < -32000 {
			zero = -32000
		}
		z16 := int16(zero)
		for b := range row {
			row[b] = z16
		}
		for b, c := range counts[ci] {
			if c == 0 {
				continue
			}
			v := math.Log((float64(c)+alpha)/den) * nuScale
			if v < -32000 {
				v = -32000
			}
			row[b] = int16(v)
		}
		nb.prior[ci] = int32(math.Log((float64(capped(ci))+1)/float64(allEx+len(nex))) * nuScale)
	}
	return nb
}

// classify returns the best intent (restricted to among, nil = all) and its
// posterior probability.
func (m *nuModel) classify(canon string, among []int) (int, float64) {
	m.nbOnce.Do(func() { m.nb = m.nuTrain(nil) })
	return m.nb.predict(canon, among)
}

func (nb *nuNB) predict(canon string, among []int) (int, float64) {
	if nb == nil || len(nb.classes) == 0 {
		return -1, 0
	}
	scores := make([]int32, len(nb.classes))
	copy(scores, nb.prior)
	n := 0
	nuNgrams(canon, func(b uint32) {
		n++
		for ci := range nb.classes {
			scores[ci] += int32(nb.table[ci*nuBuckets+int(b)])
		}
	})
	if n == 0 {
		return -1, 0
	}
	allowed := func(ci int) bool {
		if among == nil {
			return true
		}
		for _, a := range among {
			if a == nb.classes[ci] {
				return true
			}
		}
		return false
	}
	best := -1
	for ci := range nb.classes {
		if allowed(ci) && (best < 0 || scores[ci] > scores[best]) {
			best = ci
		}
	}
	if best < 0 {
		return -1, 0
	}
	// posterior: softmax of the per-n-gram log-likelihood difference
	sum := 0.0
	for ci := range nb.classes {
		if allowed(ci) {
			sum += math.Exp(float64(scores[ci]-scores[best]) / nuScale * nuBeta / float64(n))
		}
	}
	return nb.classes[best], 1 / sum
}

// ---------- Aho-Corasick ----------

// acAuto is a byte-level Aho-Corasick automaton with sorted edge lists
// (compact: no per-node map).
type acAuto struct {
	pats  []string
	first []int32 // node -> first edge index (-1)
	edges []acEdge
	fail  []int32
	out   []int32 // node -> first output index (-1)
	outs  []acOut
}

type acEdge struct {
	ch   byte
	to   int32
	next int32 // next edge of the same node
}

type acOut struct {
	pat  int32
	next int32
}

func (a *acAuto) child(n int32, c byte) int32 {
	for e := a.first[n]; e >= 0; e = a.edges[e].next {
		if a.edges[e].ch == c {
			return a.edges[e].to
		}
	}
	return -1
}

func (a *acAuto) newNode() int32 {
	a.first = append(a.first, -1)
	a.fail = append(a.fail, 0)
	a.out = append(a.out, -1)
	return int32(len(a.first) - 1)
}

func acBuild(pats []string) *acAuto {
	a := &acAuto{pats: pats}
	a.newNode()
	for pi, p := range pats {
		n := int32(0)
		for i := 0; i < len(p); i++ {
			c := p[i]
			nx := a.child(n, c)
			if nx < 0 {
				nx = a.newNode()
				a.edges = append(a.edges, acEdge{ch: c, to: nx, next: a.first[n]})
				a.first[n] = int32(len(a.edges) - 1)
			}
			n = nx
		}
		a.outs = append(a.outs, acOut{pat: int32(pi), next: a.out[n]})
		a.out[n] = int32(len(a.outs) - 1)
	}
	// BFS for failure links
	queue := make([]int32, 0, len(a.first))
	for e := a.first[0]; e >= 0; e = a.edges[e].next {
		a.fail[a.edges[e].to] = 0
		queue = append(queue, a.edges[e].to)
	}
	for qi := 0; qi < len(queue); qi++ {
		r := queue[qi]
		for e := a.first[r]; e >= 0; e = a.edges[e].next {
			s, c := a.edges[e].to, a.edges[e].ch
			queue = append(queue, s)
			f := a.fail[r]
			for f > 0 && a.child(f, c) < 0 {
				f = a.fail[f]
			}
			if nx := a.child(f, c); nx >= 0 && nx != s {
				a.fail[s] = nx
			} else {
				a.fail[s] = 0
			}
		}
	}
	return a
}

// find calls fn(pattern, end offset) for every occurrence in text.
func (a *acAuto) find(text string, fn func(pat, end int)) {
	if a == nil || len(a.pats) == 0 {
		return
	}
	n := int32(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		for n > 0 && a.child(n, c) < 0 {
			n = a.fail[n]
		}
		if nx := a.child(n, c); nx >= 0 {
			n = nx
		}
		for s := n; s > 0; s = a.fail[s] {
			for o := a.out[s]; o >= 0; o = a.outs[o].next {
				fn(int(a.outs[o].pat), i+1)
			}
		}
	}
}

// nuSvcName pulls a concrete unit name out of the text. Service words live in
// the `service` concept (sshd, nginx…), so they arrive "explained" and never
// reach generic residual fills — exactly the file/dir-entity trap in reverse.
// Generic service nouns are skipped: "status do serviço" stays unnamed and the
// method falls back to probing, which is honest.
func nuSvcName(toks []nuTok, taken map[int]bool) string {
	for i, t := range toks {
		if taken[i] {
			continue
		}
		switch t.Fold {
		case "servico", "serviço", "servicos", "serviços", "service", "services", "daemon", "daemons", "unit", "units":
			continue
		}
		if t.has("service") {
			taken[i] = true
			return t.Raw
		}
	}
	return ""
}

// nuDiskPath picks the token that looks like a filesystem path. Residual joins
// would glue "source /some/path" together; a path slot wants the token with
// path punctuation, else the last name-shaped leftover.
func nuDiskPath(toks []nuTok, taken map[int]bool) string {
	best := -1
	for i, t := range toks {
		if taken[i] || len(t.Concs) > 0 || t.Fixed != "" || t.Quoted && !strings.ContainsAny(t.Raw, "/~.") {
			continue
		}
		if !nuNameShaped(t.Raw) && !strings.ContainsAny(t.Raw, "/~.") {
			continue
		}
		if strings.ContainsAny(t.Raw, "/~") || strings.HasPrefix(t.Raw, ".") {
			taken[i] = true
			return t.Raw
		}
		best = i // name-shaped fallback, keep the last one
	}
	if best >= 0 {
		taken[best] = true
		return toks[best].Raw
	}
	return ""
}

// nuFindArg derives file_find arguments straight from the tokens: type,
// sort, mtime window and the empty flag live in concept words that a
// residual slot never sees (like nuRenamePart does for names).
func nuFindArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "type":
		dir, file := false, false
		for _, t := range toks {
			switch t.Fold {
			case "pasta", "pastas", "dir", "dirs", "diretorio", "diretório", "diretorios", "diretórios", "directory", "directories", "folder", "folders", "subpasta", "subpastas":
				dir = true
			case "arquivo", "arquivos", "file", "files":
				file = true
			}
		}
		if dir == file {
			return "" // both or neither: the script's "any" default applies
		}
		if dir {
			return "dir"
		}
		return "file"
	case "sort":
		for i, t := range toks {
			switch t.Fold {
			case "maior", "maiores", "largest", "biggest", "larger", "bigger", "heaviest", "pesado", "pesada", "pesados", "pesadas", "menor", "menores", "smallest":
				taken[i] = true
				return "size"
			case "newest", "latest", "oldest", "ultimo", "último", "ultima", "última",
				"ultimos", "últimos", "ultimas", "últimas", "recente", "recentes",
				"antigo", "antiga", "velho", "velha":
				taken[i] = true
				return "mtime"
			case "mais":
				if i+1 < len(toks) {
					switch toks[i+1].Fold {
					case "novo", "nova", "novos", "novas", "recente", "recentes", "velho", "velha", "antigo", "antiga":
						taken[i], taken[i+1] = true, true
						return "mtime"
					}
				}
			}
		}
	case "newer", "older":
		return nuFindWindow(toks, what == "newer", taken)
	case "min_size", "max_size":
		return nuFindSize(toks, what == "min_size", taken)
	case "ext":
		for i := range toks {
			f := toks[i].Fold
			if strings.HasPrefix(f, "*.") || (strings.HasPrefix(f, ".") && len(f) > 1) {
				taken[i] = true
				return strings.TrimPrefix(strings.TrimPrefix(f, "*"), ".")
			}
			if nuExtWords[f] {
				taken[i] = true
				return f
			}
		}
	case "name_re":
		// "names match regex '^x$'" / "nome casa regex 'x'" — only runs when a
		// regex/match cue is present, otherwise plain 'name' owns the pattern
		cue := false
		for _, t := range toks {
			switch t.Fold {
			case "regex", "regexp", "regular", "expressao", "expressão", "match", "matching", "matches", "casa", "case", "pattern", "padrão", "padrao":
				cue = true
			}
		}
		if !cue {
			return ""
		}
		for i, t := range toks {
			if t.Quoted && t.Fold != "" {
				taken[i] = true
				return t.Fold
			}
		}
		for i := len(toks) - 1; i >= 0; i-- {
			t := &toks[i]
			if t.Fold != "" && !t.Filler && len(t.Concs) == 0 && !taken[i] && nuNameShaped(t.Raw) {
				taken[i] = true
				return t.Fold
			}
		}
	case "exclude":
		// "except git files" / "exceto os .o" — the pattern follows the cue
		for i, t := range toks {
			switch t.Fold {
			case "except", "exceto", "excluding", "excluindo", "without", "ignoring", "ignore", "not":
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					f := toks[j].Fold
					if f != "" && !toks[j].Filler && len(toks[j].Concs) == 0 && !nuIsNumber(f) {
						if nuNameShaped(toks[j].Raw) || strings.HasPrefix(f, ".") {
							taken[i], taken[j] = true, true
							return f
						}
					}
				}
			}
		}
	case "min_depth", "max_depth":
		return nuFindDepth(toks, what == "min_depth", taken)
	case "tfield":
		for i, t := range toks {
			switch t.Fold {
			case "accessed", "access", "acessado", "acessada", "acessados", "acessadas", "atime":
				taken[i] = true
				return "atime"
			case "ctime", "changed":
				taken[i] = true
				return "ctime"
			}
		}
	case "empty":
		for _, t := range toks {
			switch t.Fold {
			case "vazio", "vazia", "vazios", "vazias", "empty", "empties":
				return "true"
			}
		}
	case "name_cued":
		// delete_file's name is a FILTER, not the leftover-name sponge that
		// file_find uses: it only fills when a nameattr cue ("chamados X",
		// "named X") is present — a bare filename is target material.
		for i, t := range toks {
			if !t.has("nameattr") {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if taken[j] || u.Filler || u.Slang {
					continue
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
			return ""
		}
	case "dir_cued":
		// "na pasta downloads" / "tmp de arquivos" / "logs of files" — the
		// scope is the word joined to a dir cue or glued to "de/of files".
		// Bare lowercase words aren't name-shaped, so only cued reads count;
		// a dir that doesn't exist fails honestly at runtime ("path not
		// found") instead of poisoning the parse with unfed.
		dirCues := map[string]bool{
			"pasta": true, "pastas": true, "diretorio": true, "diretório": true,
			"diretorios": true, "diretórios": true, "folder": true, "folders": true,
			"directory": true, "directories": true,
			"em": true, "no": true, "na": true, "nos": true, "nas": true,
			"in": true, "inside": true, "under": true, "within": true, "dentro": true,
		}
		scopeFillers := map[string]bool{
			"projeto": true, "project": true, "repo": true, "repositorio": true,
			"repositório": true, "current": true, "atual": true, "geral": true,
			"root": true, "aqui": true, "here": true, "isso": true, "isto": true,
			"arquivos": true, "files": true, "file": true, "arquivo": true,
		}
		fileWords := map[string]bool{
			"arquivos": true, "files": true, "diretorios": true, "diretórios": true,
			"pastas": true, "folders": true, "dirs": true, "directories": true,
		}
		for i, t := range toks {
			if !dirCues[t.Fold] || taken[i] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] || nuIsNumber(u.Fold) || u.has("nameattr") {
					continue
				}
				if scopeFillers[u.Fold] {
					break // "no projeto"/"em geral" falam DESTE escopo, não de um dir
				}
				taken[i], taken[j] = true, true
				return u.Raw // tagged entities (@dir, /etc) are exactly the right values
			}
			break
		}
		// X "de/do/of" <file-word>: "o tmp de arquivos", "a pasta logs of files"
		for i := 0; i+2 < len(toks); i++ {
			if !fileWords[toks[i+2].Fold] {
				continue
			}
			mid := toks[i+1].Fold
			if mid != "de" && mid != "do" && mid != "da" && mid != "of" {
				continue
			}
			u := &toks[i]
			if u.Filler || u.Slang || taken[i] || scopeFillers[u.Fold] || fileWords[u.Fold] {
				continue
			}
			if nuIsNumber(u.Fold) || len(u.Concs) > 0 || len(u.Tags) > 0 {
				continue
			}
			taken[i] = true
			return u.Raw
		}
		return ""
	case "mass":
		// separa "apaga o arquivo mais novo" (um alvo descrito — fill+take)
		// de "apaga todos os .tmp" (conjunto — for_each). Massa quando há
		// quantificador universal, ou plural de arquivos com filtro real;
		// "apaga os arquivos" sem filtro é ambíguo e vai perguntar.
		for _, t := range toks {
			switch t.Fold {
			case "todos", "todas", "todo", "toda", "all", "every", "cada", "qualquer", "quaisquer", "tudinho":
				return "1"
			}
		}
		hasPlural := false
		for _, t := range toks {
			switch t.Fold {
			case "arquivos", "files", "diretorios", "diretórios", "pastas", "folders", "directories", "dirs":
				hasPlural = true
			}
		}
		if !hasPlural {
			return ""
		}
		probe := map[int]bool{}
		for _, w := range []string{"ext", "newer", "older", "empty", "min_size", "max_size", "name_re", "exclude", "min_depth", "max_depth"} {
			if nuFindArg(toks, w, probe) != "" {
				return "1"
			}
		}
		for _, t := range toks {
			if t.has("nameattr") {
				return "1"
			}
		}
		if nuFindArg(toks, "type", probe) == "dir" {
			return "1" // "apaga os diretórios" — recorte por tipo é filtro real
		}
		return ""
	}
	return ""
}

// nuFindWindow turns "últimos 30 minutos" / "last 2 days" into minutes for
// file_find's newer_min/older_min. The direction cue decides which slot
// takes the value; without one neither does.
func nuFindWindow(toks []nuTok, newer bool, taken map[int]bool) string {
	// faixa explícita: "2 to 6 minutes ago" / "between 2 and 6 minutes" —
	// a idade fica entre os dois: newer recebe o teto, older o piso.
	if lo, hi, ok := nuFindRange(toks, taken); ok {
		if newer {
			return hi
		}
		return lo
	}
	olderSeen, newerSeen := false, false
	for _, u := range toks {
		switch u.Fold {
		case "older", "antigos", "antigo", "antiga", "velhos", "velho", "velha", "before", "antes", "more", "mais":
			olderSeen = true
		case "ultimos", "últimos", "ultimas", "últimas", "last", "past", "within", "newer", "less", "menos", "recent", "recente", "recentes":
			newerSeen = true
		}
	}
	if newer != newerSeen || olderSeen == newerSeen {
		return ""
	}
	for i, t := range toks {
		if !nuIsNumber(t.Fold) || i+1 >= len(toks) {
			continue
		}
		mult := 0
		switch toks[i+1].Fold {
		case "min", "mins", "minuto", "minutos", "minute", "minutes", "m":
			mult = 1
		case "h", "hora", "horas", "hour", "hours":
			mult = 60
		case "d", "dia", "dias", "day", "days":
			mult = 1440
		case "semana", "semanas", "week", "weeks", "w":
			mult = 10080
		}
		if mult == 0 {
			continue
		}
		n, err := strconv.Atoi(t.Fold)
		if err == nil {
			taken[i], taken[i+1] = true, true
			return strconv.Itoa(n * mult)
		}
	}
	return ""
}

// nuFindRange detects "N to M <unit>" / "between N and M <unit>" time ranges
// and returns (lo, hi) in minutes. Only fires on an explicit range cue —
// "to", "-", "a", "até", or "between … and" — so a lone "N minutes" never
// becomes a range.
func nuFindRange(toks []nuTok, taken map[int]bool) (string, string, bool) {
	for i := 0; i+3 < len(toks); i++ {
		if !nuIsNumber(toks[i].Fold) || !nuIsNumber(toks[i+2].Fold) {
			continue
		}
		sep := toks[i+1].Fold
		rangeCue := sep == "to" || sep == "a" || sep == "até" || sep == "ate" || sep == "-"
		if !rangeCue && (sep == "and" || sep == "e") {
			rangeCue = i > 0 && (toks[i-1].Fold == "between" || toks[i-1].Fold == "entre")
		}
		if !rangeCue {
			continue
		}
		mult := 0
		switch toks[i+3].Fold {
		case "min", "mins", "minuto", "minutos", "minute", "minutes", "m":
			mult = 1
		case "h", "hora", "horas", "hour", "hours":
			mult = 60
		case "d", "dia", "dias", "day", "days":
			mult = 1440
		case "semana", "semanas", "week", "weeks", "w":
			mult = 10080
		}
		if mult == 0 {
			continue
		}
		lo, err1 := strconv.Atoi(toks[i].Fold)
		hi, err2 := strconv.Atoi(toks[i+2].Fold)
		if err1 != nil || err2 != nil || lo == hi {
			continue
		}
		if lo > hi {
			lo, hi = hi, lo
		}
		taken[i], taken[i+1], taken[i+2], taken[i+3] = true, true, true, true
		if i > 0 && (toks[i-1].Fold == "between" || toks[i-1].Fold == "entre") {
			taken[i-1] = true // o cue não vira name
		}
		return strconv.Itoa(lo * mult), strconv.Itoa(hi * mult), true
	}
	return "", "", false
}

var nuPermWords = map[string][]string{
	"world_writable": {"world-writable", "writable", "escreviveis", "escrevível", "gravavel", "gravável", "gravaveis", "graváveis"},
	"suid":           {"suid"},
	"sgid":           {"sgid"},
	"executable":     {"executavel", "executável", "executaveis", "executáveis", "executable", "executables", "rodavel", "rodável"},
}

var nuGroupCues = map[string]bool{
	"group": true, "groups": true, "grupo": true, "grupos": true, "gid": true,
}

// nuNegationNear reports whether a negation word appears near the cue —
// "files not owned by root" (antes) ou "permissions not 777" (depois).
// "no" is two-faced: EN negation ("with no owner") but PT "em+o" ("777 no
// projeto" = in the project) — it only negates when the next token is a
// negatable noun of the domain; a plain noun like "projeto" makes it a
// preposition.
// nuOctalShaped: 3-4 digits all 0-7 — a permission mode literal (777, 600,
// 4755). Only perm_find's helpers use it, so a bare octal reads as mode.
func nuOctalShaped(s string) bool {
	if len(s) < 3 || len(s) > 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

var nuNegatableNouns = map[string]bool{
	"owner": true, "dono": true, "group": true, "grupo": true,
	"permission": true, "permissions": true, "permissao": true,
	"permissão": true, "permissões": true, "mode": true,
	"writable": true, "executable": true, "executables": true,
	"executavel": true, "executável": true, "executaveis": true,
	"executáveis": true, "execute": true, "exec": true,
	"rodavel": true, "rodável": true, "user": true, "usuario": true,
	"usuário": true, "access": true,
}

func nuNegationNear(toks []nuTok, i int) bool {
	for j := i - 4; j <= i+2 && j < len(toks); j++ {
		if j < 0 || j == i {
			continue
		}
		switch toks[j].Fold {
		case "not", "non", "não", "nao", "nunca", "sem", "without", "except", "excluding", "exceto":
			return true
		case "no":
			if j+1 < len(toks) && nuNegatableNouns[toks[j+1].Fold] {
				return true
			}
		}
	}
	return false
}

// nuCueFamilyConcs: conceitos que barram candidato a nome de owner/group —
// palavras da família cue (user/files/permattr), NÃO nomes de serviço/host.
var nuCueFamilyConcs = map[string]bool{
	"user": true, "who": true, "files": true, "filenoun": true,
	"env": true, "permattr": true, "group": true,
}

// nuOwnerLikeArg: candidate word after a cue — skip fillers, quoted and
// already-taken tokens, bounded radius (the 'owner is daniel'→is fix).
func nuOwnerLikeArg(toks []nuTok, cues map[string]bool, neg bool, taken map[int]bool) string {
	for i, t := range toks {
		cue := cues[t.Fold]
		// 'owned/belonging by' é cue de DONO — nunca de grupo
		isOwner := len(cues) > 0 && cues["owner"]
		if !cue && isOwner && t.Fold == "by" && i > 0 && (toks[i-1].Fold == "owned" || toks[i-1].Fold == "belonging") {
			cue = true
		}
		if !cue {
			continue
		}
		if nuNegationNear(toks, i) != neg {
			continue
		}
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			if toks[j].Filler || toks[j].Quoted || taken[j] {
				continue
			}
			n := toks[j].Fold
			// só a família-cue bloqueia nome: stem/typo-repair são palpites
			// ('staff'→'start') e nomes de serviço são users reais (apache)
			guessed := toks[j].Weak || toks[j].Fixed != ""
			cueish := false
			if !guessed {
				for _, c := range toks[j].Concs {
					if nuCueFamilyConcs[c] {
						cueish = true
					}
				}
			}
			if n == "" || cueish {
				continue
			}
			// entidade @service/@host PODE ser nome de usuário (apache,
			// www-data, nginx têm contas reais); só @file é impossível
			isFile := false
			for _, tag := range toks[j].Tags {
				if tag == "@file" {
					isFile = true
				}
			}
			if isFile {
				continue
			}
			if nuGroupCues[n] {
				continue // "user group staff" → o 'staff' pertence ao 'group'
			}
			taken[i], taken[j] = true, true
			return n
		}
	}
	return ""
}

var nuOwnerCues = map[string]bool{
	"usuario": true, "usuário": true, "usuarios": true, "usuários": true,
	"user": true, "users": true, "dono": true, "donos": true,
	"owner": true, "owners": true,
}

func nuPermArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "owner":
		return nuOwnerLikeArg(toks, nuOwnerCues, false, taken)
	case "not_owner":
		return nuOwnerLikeArg(toks, nuOwnerCues, true, taken)
	case "group":
		return nuOwnerLikeArg(toks, nuGroupCues, false, taken)
	case "not_group":
		return nuOwnerLikeArg(toks, nuGroupCues, true, taken)
	case "no_owner", "no_group", "orphan":
		want := map[string]bool{
			"no_owner": what == "no_owner" || what == "orphan",
			"no_group": what == "no_group" || what == "orphan",
		}
		for i := 0; i+1 < len(toks); i++ {
			n1, n2 := toks[i].Fold, toks[i+1].Fold
			no := n1 == "no" || n1 == "sem" || n1 == "without"
			if no && want["no_owner"] && (nuOwnerCues[n2] || n2 == "owner") {
				taken[i], taken[i+1] = true, true
				return "true"
			}
			if no && want["no_group"] && nuGroupCues[n2] {
				taken[i], taken[i+1] = true, true
				return "true"
			}
		}
		return ""
	case "mode":
		for i, t := range toks {
			if (t.Fold == "mode" || t.Fold == "permissions" || t.Fold == "permissao" || t.Fold == "permissões") && !nuNegationNear(toks, i) {
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					if nuIsNumber(toks[j].Fold) {
						taken[i], taken[j] = true, true
						return toks[j].Fold
					}
				}
			}
		}
		// "which files are 777": bare octal — no cue word needed
		for i, t := range toks {
			if !taken[i] && nuOctalShaped(t.Fold) && !nuNegationNear(toks, i) {
				taken[i] = true
				return t.Fold
			}
		}
		return ""
	case "not_mode":
		for i, t := range toks {
			if (t.Fold == "mode" || t.Fold == "permissions" || t.Fold == "permissao" || t.Fold == "permissões") && nuNegationNear(toks, i) {
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					if nuIsNumber(toks[j].Fold) {
						taken[i], taken[j] = true, true
						return toks[j].Fold
					}
				}
			}
		}
		for i, t := range toks {
			if !taken[i] && nuOctalShaped(t.Fold) && nuNegationNear(toks, i) {
				taken[i] = true
				return t.Fold
			}
		}
		return ""
	case "not_exec", "not_ww":
		words := map[string][]string{
			"not_exec": {"executavel", "executável", "executaveis", "executáveis", "executable", "executables", "execute", "execution", "exec", "rodavel", "rodável", "executar"},
			"not_ww":   {"writable", "world-writable", "escreviveis", "escrevível"},
		}
		for i, t := range toks {
			for _, w := range words[what] {
				if t.Fold == w && nuNegationNear(toks, i) {
					taken[i] = true
					return "true"
				}
			}
		}
		return ""
	}
	for i, t := range toks {
		for _, w := range nuPermWords[what] {
			if t.Fold == w && !nuNegationNear(toks, i) {
				return "true"
			}
		}
	}
	return ""
}

// nuOwnerArg takes the word after a possession cue — "do usuário matheus",
// "owned by root", "files of user h". Without the cue, a loose word must
// not become a user filter ("suid bit set" ≠ owner "set").
func nuOwnerArg(toks []nuTok, taken map[int]bool) string {
	for i, t := range toks {
		cue := false
		switch t.Fold {
		case "usuario", "usuário", "usuarios", "usuários", "user", "users", "dono", "donos", "owner", "owners":
			cue = true
		case "by":
			cue = i > 0 && toks[i-1].Fold == "owned"
		}
		if !cue {
			continue
		}
		// candidato: primeira palavra "limpa" num raio curto — fillers
		// ("owner is daniel"→is), aspas e tokens já consumidos vazavam
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			if toks[j].Filler || toks[j].Quoted || taken[j] {
				continue
			}
			n := toks[j].Fold
			if len(n) < 2 || len(toks[j].Concs) > 0 || len(toks[j].Tags) > 0 {
				continue
			}
			taken[i], taken[j] = true, true
			return n
		}
	}
	return ""
}

var nuSizeUnit = map[string]int64{
	"b": 1, "byte": 1, "bytes": 1,
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10, "kilobyte": 1 << 10, "kilobytes": 1 << 10, "kbyte": 1 << 10, "kbytes": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20, "megabyte": 1 << 20, "megabytes": 1 << 20, "mbyte": 1 << 20, "mbytes": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30, "gigabyte": 1 << 30, "gigabytes": 1 << 30, "gbyte": 1 << 30, "gbytes": 1 << 30,
}

var nuSizeRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)(b|kb|mb|gb|kib|mib|gib|k|m|g|bytes?)?$`)

// nuFindDepth turns "minimum 1 level down" / "2 níveis de profundidade" /
// "only the top level" into min_depth/max_depth. The comparative cue picks
// the bound: minimum/more/at least -> min_depth; level N alone or
// "at most/only" -> max_depth.
func nuFindDepth(toks []nuTok, wantMin bool, taken map[int]bool) string {
	cue, minSeen, maxSeen := -1, false, false
	for i, t := range toks {
		switch t.Fold {
		case "level", "levels", "nível", "níveis", "nivel", "niveis", "depth", "profundidade", "deep":
			if cue < 0 {
				cue = i
			}
		case "minimum", "minimo", "mínimo", "least", "at least", "more", "mais", "over", "acima", "deeper":
			minSeen = true
		case "maximum", "maximo", "máximo", "most", "at most", "only", "ate", "até", "up to", "below", "abaixo", "shallow":
			maxSeen = true
		}
	}
	if cue < 0 {
		return ""
	}
	if wantMin != minSeen || (minSeen && maxSeen) {
		return ""
	}
	if !minSeen && !maxSeen && !wantMin {
		// "level 2" with no comparative is an upper bound
	} else if !wantMin && !maxSeen {
		return ""
	}
	for i, t := range toks {
		if t.Fold == "" || !nuIsNumber(t.Fold) {
			continue
		}
		if i < cue-2 || i > cue+2 {
			continue
		}
		taken[i] = true
		return t.Fold
	}
	return ""
}

// nuFindSize turns "maiores que 10MB" / "larger than 100k" into bytes for
// min_size/max_size; the comparative cue picks the direction, so plain
// "maiores arquivos" stays a sort, not a filter.
func nuFindSize(toks []nuTok, min bool, taken map[int]bool) string {
	minSeen, maxSeen, exact := false, false, false
	for _, t := range toks {
		switch t.Fold {
		case "maior", "maiores", "larger", "bigger", "over", "above", "acima", "exceeding", "least", "more", "minimo", "mínimo", "minimum":
			minSeen = true
		case "menor", "menores", "smaller", "under", "below", "less", "ate", "até", "abaixo", "maximo", "máximo", "maximum":
			maxSeen = true
		case "size", "sized", "tamanho", "bytes", "kilobytes", "megabytes", "gigabytes":
			exact = true // a size cue without a comparative is an exact-size ask
		}
		if m := nuSizeRe.FindStringSubmatch(t.Fold); m != nil && m[2] != "" {
			exact = true // "10mb" carries its own unit: self-evident size
		}
	}
	if minSeen == maxSeen {
		if !exact {
			return ""
		}
		// exact size: min and max converge on the same value
	} else if min != minSeen {
		return ""
	}
	for i, t := range toks {
		m := nuSizeRe.FindStringSubmatch(t.Fold)
		if m == nil {
			continue
		}
		unit := m[2]
		if unit == "" {
			if i+1 >= len(toks) {
				continue
			}
			u := toks[i+1].Fold
			if _, ok := nuSizeUnit[u]; !ok {
				continue
			}
			unit = u
			taken[i+1] = true
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		taken[i] = true
		return strconv.FormatInt(int64(n*float64(nuSizeUnit[unit])), 10)
	}
	return ""
}

// nuExtractPat maps the word the user used ("ips", "emails") to the
// pattern enum the script validates. First match wins; ipv6 before ip.
var nuExtractPats = []struct {
	words []string
	pat   string
}{
	{[]string{"ipv6"}, "ipv6"},
	{[]string{"ips", "ip", "ipv4", "enderecos", "endereços"}, "ipv4"},
	{[]string{"emails", "email", "e-mail", "e-mails", "mail", "mails"}, "email"},
	{[]string{"urls", "url", "links", "link"}, "url"},
	{[]string{"datas", "dates", "date", "data"}, "date"},
	{[]string{"uuids", "uuid"}, "uuid"},
	{[]string{"hex", "hexadecimal", "hexes"}, "hex"},
	{[]string{"numeros", "números", "numbers", "number", "numero", "número"}, "number"},
}

func nuExtractPat(toks []nuTok) string {
	for _, e := range nuExtractPats {
		for _, t := range toks {
			for _, w := range e.words {
				if t.Fold == w {
					return e.pat
				}
			}
		}
	}
	return ""
}

// nuSliceArg pulls line_slice numbers: "últimas 10" → tail=10,
// "primeiras 20" → start=1,end=20, "linhas 5 a 20" → start=5,end=20,
// "linha 42" → start=end=42.
func nuSliceArg(toks []nuTok, what string, taken map[int]bool) string {
	// "line 111 with 2 lines of context": a janela é a linha ± o contexto.
	if what == "start" || what == "end" {
		if st, en, ok := nuSliceContext(toks, taken); ok {
			if what == "start" {
				return st
			}
			return en
		}
	}
	numAfter := func(i int) string {
		for j := i + 1; j < len(toks) && j < i+4; j++ {
			if nuIsNumber(toks[j].Fold) {
				taken[j] = true
				return toks[j].Fold
			}
		}
		return ""
	}
	var nums []string
	var numIdx []int
	for i, t := range toks {
		switch t.Fold {
		case "ultimas", "últimas", "ultimos", "últimos", "last", "tail", "finais", "final":
			if what == "tail" {
				if n := numAfter(i); n != "" {
					return n
				}
			}
		case "primeiras", "primeiros", "first", "head", "iniciais":
			if what == "start" {
				return "1"
			}
			if what == "end" {
				if n := numAfter(i); n != "" {
					return n
				}
			}
		case "linha", "linhas", "line", "lines":
			for j := i + 1; j < len(toks); j++ {
				f := toks[j].Fold
				if nuIsNumber(f) {
					nums = append(nums, f)
					numIdx = append(numIdx, j)
					continue
				}
				if f == "a" || f == "to" || f == "ate" || f == "até" || f == "-" || f == "de" || f == "of" {
					continue
				}
				break
			}
		}
	}
	if len(nums) == 0 {
		return ""
	}
	if what == "start" {
		taken[numIdx[0]] = true
		return nums[0]
	}
	if what == "end" {
		if len(nums) > 1 {
			taken[numIdx[1]] = true
			return nums[1]
		}
		taken[numIdx[0]] = true
		return nums[0]
	}
	return ""
}

// nuSliceContext: "line 111 with 2 lines of context" / "linha 10 com 3 de
// contexto" → janela [L-C, L+C]. Só dispara com a palavra context/contexto
// explícita — um segundo número solto não vira raio.
func nuSliceContext(toks []nuTok, taken map[int]bool) (string, string, bool) {
	ctxIdx, lineIdx := -1, -1
	var lineNo, ctxNo, lineTok, ctxTok int
	for i, t := range toks {
		switch t.Fold {
		case "context", "contexto":
			ctxIdx = i
		case "linha", "line":
			if lineIdx < 0 {
				lineIdx = i
			}
		}
	}
	if ctxIdx < 0 || lineIdx < 0 {
		return "", "", false
	}
	// o número do contexto é o que vem antes de "context" ("2 lines of context")
	for j := ctxIdx - 1; j >= 0 && j >= ctxIdx-4; j-- {
		if n, err := strconv.Atoi(toks[j].Fold); err == nil && nuIsNumber(toks[j].Fold) {
			ctxNo, ctxTok = n, j
			break
		}
	}
	// a linha-âncora é o número que vem depois de "line"
	for j := lineIdx + 1; j < len(toks) && j <= lineIdx+3; j++ {
		if n, err := strconv.Atoi(toks[j].Fold); err == nil && nuIsNumber(toks[j].Fold) {
			lineNo, lineTok = n, j
			break
		}
	}
	if lineNo <= 0 || ctxNo <= 0 || lineTok == ctxTok {
		return "", "", false
	}
	lo := lineNo - ctxNo
	if lo < 1 {
		lo = 1
	}
	taken[lineTok], taken[ctxTok], taken[ctxIdx], taken[lineIdx] = true, true, true, true
	return strconv.Itoa(lo), strconv.Itoa(lineNo + ctxNo), true
}

// nuExtWords are plain words that name an extension ("find log files").
var nuExtWords = map[string]bool{
	"log": true, "txt": true, "md": true, "py": true, "go": true, "js": true,
	"ts": true, "json": true, "yaml": true, "yml": true, "toml": true,
	"conf": true, "cfg": true, "ini": true, "sh": true, "c": true, "h": true,
	"cpp": true, "rs": true, "rb": true, "java": true, "csv": true, "xml": true,
	"html": true, "css": true, "sql": true, "db": true, "sqlite": true,
	"zip": true, "tar": true, "gz": true, "pdf": true, "png": true, "jpg": true,
	"jpeg": true, "gif": true, "mp3": true, "mp4": true, "wav": true,
}

// nuArchiveName reports whether a token names an archive by extension.
func nuArchiveName(f string) bool {
	for _, e := range []string{".zip", ".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".whl", ".jar", ".war", ".rar", ".7z", ".gz", ".bz2", ".xz"} {
		if strings.HasSuffix(f, e) {
			return true
		}
	}
	return false
}
