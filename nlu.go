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
	Confidence float64           `json:"confidence"`
	Evidence   []string          `json:"evidence,omitempty"`
	Ents       []nuHit           `json:"entities,omitempty"`
	Missing    []string          `json:"missing,omitempty"` // required slots nobody filled
	Hint       string            `json:"hint,omitempty"`    // test | symbol | file (cue words)
	Alt        []string          `json:"alt,omitempty"`     // runner-up intents
	Git        bool              `json:"git,omitempty"`     // the request names git/repo explicitly
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
	concs    map[string]bool // concept names (slang may output them directly)
}

var (
	nuOnce sync.Once
	nuDef  *nuModel
)

// nuGet returns the embedded interpreter.
func nuGet() *nuModel {
	nuOnce.Do(func() {
		lx, _ := nuFS.ReadFile("nlu/lexicon.json")
		ds, _ := nuFS.ReadFile("nlu/intents.json")
		nuDef = nuCompile(lx, ds)
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
	for i, it := range m.data.Intents {
		m.byName[it.Name] = i
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

// nuStem is a light PT/EN suffix stripper (one suffix, stem >= 3 letters).
var nuSuffixes = []string{"amente", "mente", "acoes", "icoes", "coes", "cao", "ando", "endo", "indo", "aram", "eram", "iram",
	"ados", "adas", "idos", "idas", "ing", "ado", "ada", "ido", "ida", "ed", "ar", "er", "ir", "am", "em", "as", "es", "os", "a", "e", "o", "s"}

func nuStem(w string) string {
	if len(w) <= 3 {
		return w
	}
	for _, s := range nuSuffixes {
		if len(w)-len(s) >= 3 && strings.HasSuffix(w, s) {
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
					out = append(out, nuTok{Raw: q, Fold: fzFold(q), Quoted: true})
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
		if m.fillers[t.Fold] {
			t.Filler = true
			continue
		}
		if !nuNameShaped(t.Raw) {
			if c, ok := m.stem[nuStem(t.Fold)]; ok {
				t.Concs = append(t.Concs, c...)
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
	if strings.ContainsAny(s, "./_\\:@=") {
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
				if _, has := nuGroupOK(toks, n); has {
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
	in.Slots, in.Missing, in.Hint = m.fill(def, toks, hits, used)
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
	in.Confidence = math.Round(conf*100) / 100
	if def.Name == "none" {
		in.Confidence = 0
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

// nuVerbConcepts are actions: a verb of another clause ("roda os testes e
// me mostra …") is never the value of a slot.
var nuVerbConcepts = map[string]bool{"open": true, "explain": true, "find": true, "run": true, "build": true, "lint": true, "format": true,
	"commit": true, "push": true, "pull": true, "checkout": true, "stash": true, "undo": true, "rename": true, "create": true, "delete": true,
	"fix": true, "install": true, "update": true, "connect": true, "start": true, "stop": true, "restart": true, "kill": true}

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

// fill extracts the slots of def.
func (m *nuModel) fill(def *nuIntentDef, toks []nuTok, hits []nuHit, used map[string]bool) (map[string]string, []string, string) {
	slots := map[string]string{}
	var missing []string
	hint := ""
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
	for _, name := range names {
		sd := def.Slots[name]
		v := ""
		switch sd.From {
		case "rename_from", "rename_to":
			v = nuRenamePart(toks, res, sd.From == "rename_to")
		}
		kinds := map[string]bool{}
		for _, k := range sd.Kinds {
			kinds[k] = true
		}
		if v == "" && kinds["message"] {
			v = nuMessage(toks)
		}
		if v == "" {
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
		if v == "" && (kinds["number"] || kinds["port"]) {
			for i := range toks {
				if nuIsNumber(toks[i].Fold) && !taken[i] {
					v, taken[i] = toks[i].Fold, true
					break
				}
			}
		}
		if v == "" {
			// the words no rule explained, as typed
			var parts []string
			for _, i := range res {
				if taken[i] {
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
		if v == "" && sd.Required {
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
	return slots, missing, hint
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
	allEx := 0
	for _, n := range nex {
		allEx += n
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
		nb.prior[ci] = int32(math.Log((float64(nex[ci])+1)/float64(allEx+len(nex))) * nuScale)
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
