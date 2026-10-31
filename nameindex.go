package main

// nameindex.go: resolve misspelled names in the user's prompt against a cheap
// repo index (file paths + top-level symbols). The user writes fast; jazz
// should act on the thing they meant, so the harness attaches a one-line note
// with its reasoning to the USER message (never the system prompt, never a
// rewrite of the user's text) and the model can disagree.
//
// It is context-aware, not just edit distance: candidates are ranked by what
// the sentence is about (test / function / file cues and verbs near the
// token), by the file types of the repo, by files touched in this session and
// recently modified, and keyboard slips weigh less than arbitrary edits.
// A plain word is never forced into a file: only name-shaped tokens
// (path, extension, snake_case, camelCase) or words preceded by a cue word
// ("arquivo", "teste", "função") are looked up. No good match -> no note.

import (
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	niMaxFiles    = 20000
	niMaxSymFiles = 3000
	niSymBytes    = 48 << 10
	niTTL         = 30 * time.Second
	niMaxNotes    = 4
)

// niFile is one file of the index. Its strings are views into the index's
// single blob (nameindex_idx.go): no allocation per file.
type niFile struct {
	Rel, Base, Ext           string
	StemKey, CoreKey, DirKey string // CoreKey: stem without test affixes
	Test, Doc                bool
}

type niSym struct {
	Name, Key, Rel string
	mask           uint32 // niMask(Key)
	klen           uint8  // rune length of Key (capped)
}

// niFast is the pointer-free part of a file that the resolver's lower-bound
// filters read before any distance (low-level study §6, item 5): rune length
// and letter mask of StemKey and CoreKey. A slice of these is noscan memory.
type niFast struct {
	stemMask, coreMask uint32
	stemLen, coreLen   uint8
}

// niIndex is the name index of a root. The per-file data lives in a few
// pointer-free arrays and one byte blob (struct of arrays, low-level study
// §5.1, item 8): recs/fast/relOrd/baseOrd index into blob and files are
// string views over it. The same layout is persisted and read back without
// decoding (nameindex_idx.go).
type niIndex struct {
	root     string
	files    []niFile
	fast     []niFast     // parallel to files
	recs     []niRec      // parallel to files: offsets into blob
	blob     []byte       // every string of the index
	relOrd   []uint32     // file indexes sorted by Rel (exact path lookup)
	baseOrd  []uint32     // file indexes sorted by Base
	dirs     []niDirState // the directories the listing came from
	snapNS   int64        // when the listing was taken (unix ns)
	exts     map[string]int
	dominant string // most common code extension
	trunc    bool
	buildMS  float64
	built    time.Time

	symOnce sync.Once
	syms    []niSym
}

var (
	niMu    sync.Mutex
	niCache = map[string]*niIndex{}
)

var niDocExt = map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".csv": true, ".toml": true, ".lock": true, ".rst": true, ".svg": true}
var niCodeExt = map[string]bool{".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".rs": true, ".java": true, ".kt": true, ".rb": true, ".php": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".cs": true, ".swift": true, ".lua": true, ".sh": true, ".vue": true, ".svelte": true, ".html": true, ".css": true, ".scss": true, ".sql": true}

// niGet returns the (cached, 30 s) index of root.
func niGet(root string) *niIndex {
	niMu.Lock()
	defer niMu.Unlock()
	// a big repo is rebuilt less often: the TTL grows with the build time
	if ix, ok := niCache[root]; ok && time.Since(ix.built) < niTTL+time.Duration(ix.buildMS*100)*time.Millisecond {
		return ix
	}
	ix := niBuild(root)
	niCache[root] = ix
	return ix
}

// niGetFresh is niGet for the moment a user opens a listing (the @ picker):
// a cheap index (built in under niFreshMaxMS) is rebuilt so files that just
// appeared show up; a slow one keeps the TTL.
func niGetFresh(root string) *niIndex {
	niMu.Lock()
	defer niMu.Unlock()
	if ix, ok := niCache[root]; ok && ix.buildMS >= niFreshMaxMS {
		return ix
	}
	ix := niBuild(root)
	niCache[root] = ix
	return ix
}

const niFreshMaxMS = 300

// niMask is the letter bag of a key (bit c-'a' per ASCII letter, either
// case; bit 26 for any digit) and its rune length capped at 255.
func niMask(k string) (uint32, uint8) {
	var m uint32
	n := 0
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z':
			m |= 1 << (c - 'a')
		case c >= 'A' && c <= 'Z':
			m |= 1 << (c - 'A')
		case c >= '0' && c <= '9':
			m |= 1 << 26
		}
		if c < 0x80 || c >= 0xC0 { // count runes: skip UTF-8 continuation bytes
			n++
		}
	}
	return m, uint8(min(n, 255))
}

func niFastOf(stem, core string) niFast {
	var f niFast
	f.stemMask, f.stemLen = niMask(stem)
	f.coreMask, f.coreLen = niMask(core)
	return f
}

// niBound is a lower bound of fzDist(q, k) from rune lengths and letter
// bags: each unit of length difference needs an insertion or deletion
// (fzEdit); a substitution flips at most 2 bits of the bag, an insertion or
// deletion 1, a transposition none, and no edit costs less than fzSlip, so
// dist >= fzSlip*ceil(bits/2). It never exceeds the real distance, so
// filtering by it changes no result (TestNiBoundIsLowerBound).
func niBound(qm uint32, ql uint8, m uint32, l uint8) int {
	dl := int(ql) - int(l)
	if dl < 0 {
		dl = -dl
	}
	b := dl * fzEdit
	if x := fzSlip * ((bits.OnesCount32(qm^m) + 1) / 2); x > b {
		b = x
	}
	return b
}

var niTestAffix = lazyRx(`(?i)^(?:test[_-]?|spec[_-]?)|(?:[_.-]?(?:tests?|spec|specs))$`)

// niHasTestWord: s contains "test" or "spec", ASCII case-insensitively.
func niHasTestWord(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		c0 := s[i] | 0x20
		if c0 != 't' && c0 != 's' {
			continue
		}
		w := [4]byte{c0, s[i+1] | 0x20, s[i+2] | 0x20, s[i+3] | 0x20}
		if w == [4]byte{'t', 'e', 's', 't'} || w == [4]byte{'s', 'p', 'e', 'c'} {
			return true
		}
	}
	return false
}

// niCore strips test affixes: test_calculo, calculo_test, calculo.test -> calculo.
func niCore(stem string) string {
	if fzIsASCII(stem) && !niHasTestWord(stem) {
		return stem // the affix pattern needs "test" or "spec" (non-ASCII may case-fold into them)
	}
	for i := 0; i < 2; i++ {
		s := niTestAffix.ReplaceAllString(stem, "")
		if s == "" || s == stem {
			break
		}
		stem = s
	}
	return stem
}

var niSymRE = map[string]*lazyRe{
	"go":   lazyRx(`(?m)^(?:func (?:\([^)]*\) )?|type |var |const )(\w{4,})`),
	"py":   lazyRx(`(?m)^(?:async )?(?:def|class) (\w{4,})`),
	"js":   lazyRx(`(?m)^(?:export )?(?:default )?(?:async )?(?:function\*? |class |const |let )(\w{4,})`),
	"rs":   lazyRx(`(?m)^\s*(?:pub(?:\([^)]*\))? )?(?:async )?(?:fn|struct|enum|trait) (\w{4,})`),
	"misc": lazyRx(`(?m)^\s*(?:(?:public|private|protected|static|final|abstract|async|def|fun|func|function|class|interface|struct)\s+)+(?:[\w<>\[\],.]+\s+)?(\w{4,})`),
}

func niSymLang(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "py"
	case ".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte":
		return "js"
	case ".rs":
		return "rs"
	case ".java", ".kt", ".rb", ".php", ".cs", ".swift", ".lua", ".c", ".h", ".cc", ".cpp":
		return "misc"
	}
	return ""
}

// symbols extracts top-level symbols once (lazy: only identifier lookups pay).
func (ix *niIndex) symbols() []niSym {
	ix.symOnce.Do(func() {
		n := 0
		for _, f := range ix.files {
			lang := niSymLang(f.Ext)
			if lang == "" {
				continue
			}
			if n++; n > niMaxSymFiles {
				break
			}
			fh, err := os.Open(filepath.Join(ix.root, filepath.FromSlash(f.Rel)))
			if err != nil {
				continue
			}
			buf := make([]byte, niSymBytes)
			k, _ := fh.Read(buf)
			fh.Close()
			seen := map[string]bool{}
			for _, m := range niSymRE[lang].FindAllSubmatch(buf[:k], -1) {
				name := string(m[1])
				if !seen[name] {
					seen[name] = true
					k := fzKey(name)
					m, l := niMask(k)
					ix.syms = append(ix.syms, niSym{Name: name, Key: k, Rel: f.Rel, mask: m, klen: l})
				}
			}
		}
	})
	return ix.syms
}

// ---------- session context ----------

var niTouched struct {
	mu   sync.Mutex
	list []string
}

// niTouch records a file the agent read, edited or wrote (most recent last).
func niTouch(abs string) {
	niTouched.mu.Lock()
	defer niTouched.mu.Unlock()
	l := niTouched.list[:0]
	for _, p := range niTouched.list {
		if p != abs {
			l = append(l, p)
		}
	}
	l = append(l, abs)
	if len(l) > 32 {
		l = l[len(l)-32:]
	}
	niTouched.list = l
}

func niIsTouched(abs string) bool {
	niTouched.mu.Lock()
	defer niTouched.mu.Unlock()
	for _, p := range niTouched.list {
		if p == abs {
			return true
		}
	}
	return false
}

func niResetTouched() {
	niTouched.mu.Lock()
	niTouched.list = nil
	niTouched.mu.Unlock()
}

// ---------- tokens ----------

type niTok struct {
	Raw, Name, Ext string // Name = raw without :line and extension
	Shape          string // path | ext | snake | camel | plain
	Hint           string // file | test | symbol | dir ("" = none)
	CueWord        string // noun cue that set Hint
	Verb, VerbWord string // canonical intent of the nearest verb before the token
	OpenOnly       bool   // plain word eligible only through an "open" verb
}

var (
	niNounCue = map[string]string{
		"arquivo": "file", "arquivos": "file", "file": "file", "script": "file", "modulo": "file", "module": "file", "pacote": "dir", "package": "dir",
		"pasta": "dir", "diretorio": "dir", "dir": "dir", "folder": "dir",
		"teste": "test", "testes": "test", "test": "test", "tests": "test", "spec": "test", "specs": "test",
		"funcao": "symbol", "function": "symbol", "func": "symbol", "metodo": "symbol", "method": "symbol", "classe": "symbol", "class": "symbol",
		"variavel": "symbol", "constante": "symbol", "simbolo": "symbol", "struct": "symbol",
	}
	niFiller = map[string]bool{"o": true, "a": true, "os": true, "as": true, "um": true, "uma": true, "de": true, "do": true, "da": true, "dos": true, "das": true,
		"no": true, "na": true, "nos": true, "nas": true, "em": true, "pro": true, "pra": true, "para": true, "esse": true, "essa": true, "este": true, "esta": true,
		"the": true, "this": true, "that": true, "in": true, "of": true, "to": true, "chamado": true, "chamada": true, "named": true, "called": true, "meu": true, "minha": true, "ao": true, "que": true}
	niNotExt = map[string]bool{"com": true, "br": true, "org": true, "net": true, "io": true, "dev": true, "app": true, "ai": true, "gov": true, "edu": true, "e": true, "g": true}
)

// Character tests of the tokenizer as byte loops (they were the regexps
// ^[a-z][a-z0-9]{0,5}$, :\d+(?::\d+)?$, [a-z0-9][A-Z], [A-Za-z]_[A-Za-z]
// and ^\p{L}+$; low-level study §8.2, item 9). Same answers, no regexp
// engine per token. TestNiCharTestsMatchRegexps keeps them equal.

// niExtOK: a plausible file extension (lowercase letter, then up to five
// lowercase letters or digits).
func niExtOK(s string) bool {
	if len(s) == 0 || len(s) > 6 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// niStripLine drops a trailing :line or :line:col.
func niStripLine(s string) string {
	for k := 0; k < 2; k++ {
		i := len(s)
		for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
			i--
		}
		if i == len(s) || i == 0 || s[i-1] != ':' {
			break
		}
		s = s[:i-1]
	}
	return s
}

// niCamel: a lowercase letter or digit followed by an uppercase letter.
func niCamel(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if c, d := s[i], s[i+1]; (c >= 'a' && c <= 'z' || c >= '0' && c <= '9') && d >= 'A' && d <= 'Z' {
			return true
		}
	}
	return false
}

// niSnake: an underscore between two ASCII letters.
func niSnake(s string) bool {
	for i := 1; i+1 < len(s); i++ {
		if s[i] == '_' && niIsLetterASCII(s[i-1]) && niIsLetterASCII(s[i+1]) {
			return true
		}
	}
	return false
}

func niIsLetterASCII(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// niLetters: non-empty and every rune a Unicode letter.
func niLetters(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func niTrim(s string) string {
	s = strings.Trim(s, "\"'`“”‘’()[]{}<>,;:!?*")
	s = strings.TrimRight(s, ".")
	return niStripLine(s)
}

// niTokens finds the name-like tokens of text with their sentence context.
func niTokens(text string, known func(ext string) bool) []niTok {
	text = acStripReminders(text)
	if len(text) > 2000 {
		text = text[:2000]
	}
	fields := strings.Fields(text)
	var out []niTok
	seen := map[string]bool{}
	for i, f := range fields {
		if strings.HasPrefix(f, "@") || strings.Contains(f, "://") || strings.HasPrefix(f, "-") || strings.HasPrefix(f, "$") {
			continue
		}
		raw := niTrim(f)
		if len(raw) < 3 || len(raw) > 80 || seen[raw] || strings.ContainsAny(raw, "=(){}|&;<>") {
			continue
		}
		fw := fzFold(raw)
		if _, isCue := niNounCue[fw]; isCue || niFiller[fw] || olStop[fw] {
			continue
		}
		tk := niTok{Raw: raw}
		name := raw
		base := raw
		if j := strings.LastIndexByte(base, '/'); j >= 0 {
			base = base[j+1:]
		}
		if j := strings.LastIndexByte(base, '.'); j > 0 && j < len(base)-1 {
			ext := strings.ToLower(base[j:])
			if niExtOK(ext[1:]) && !niNotExt[ext[1:]] && (niCodeExt[ext] || niDocExt[ext] || known(ext)) {
				tk.Ext = ext
				name = raw[:len(raw)-len(base)] + base[:j]
			}
		}
		tk.Name = name
		switch {
		case strings.Contains(raw, "/"):
			if tk.Ext == "" && len(raw) < 6 {
				continue // "e/ou", "s/n"
			}
			tk.Shape = "path"
		case tk.Ext != "":
			tk.Shape = "ext"
		case strings.Contains(raw, "_") && strings.Trim(raw, "_") != "" && niSnake(raw):
			tk.Shape = "snake"
		case niCamel(raw):
			tk.Shape = "camel"
		case niLetters(raw) && len(raw) >= 4:
			tk.Shape = "plain"
		default:
			continue
		}
		// context: the 4 words before the token
		for k := i - 1; k >= 0 && k >= i-4; k-- {
			w := fzFold(niTrim(fields[k]))
			if h, ok := niNounCue[w]; ok && tk.Hint == "" && i-k <= 3 {
				tk.Hint, tk.CueWord = h, niTrim(fields[k])
			}
			if v := fzIntent(w); v != "" && tk.Verb == "" && len(w) >= 3 {
				tk.Verb, tk.VerbWord = v, niTrim(fields[k])
			}
			if k < i-1 && strings.ContainsAny(fields[k][len(fields[k])-1:], ".!?") {
				break // previous sentence
			}
		}
		if tk.Shape == "plain" {
			switch {
			case tk.Hint != "":
			case tk.Verb == "open" && niOnlyFillers(fields, i, fzFold(tk.VerbWord)):
				tk.OpenOnly = true
			default:
				continue // a plain word is not forced into a name
			}
		}
		if tk.Hint == "" && fzFold(raw) != "" && strings.Contains(fzFold(raw), "test") {
			tk.Hint = "test"
		}
		seen[raw] = true
		out = append(out, tk)
	}
	return out
}

// niOnlyFillers: between the verb and the token there are only articles.
func niOnlyFillers(fields []string, i int, verb string) bool {
	for k := i - 1; k >= 0 && k >= i-3; k-- {
		w := fzFold(niTrim(fields[k]))
		if w == verb || fzIntent(w) == "open" {
			return true
		}
		if !niFiller[w] {
			return false
		}
	}
	return false
}

// ---------- resolution ----------

type niCand struct {
	Rel    string
	Symbol string // set when the candidate is a symbol of Rel
	Score  int
	d      int
	Why    []string
	Test   bool
	Ext    string
}

func (c niCand) Display() string {
	if c.Symbol != "" {
		return c.Symbol + " (" + c.Rel + ")"
	}
	return c.Rel
}

// resolve ranks the candidates for one token and keeps the winner or the
// tied leaders (nil = nothing to say).
func (ix *niIndex) resolve(tk niTok) []niCand { return niTop(ix.resolveAll(tk)) }

// resolveAll is every candidate within the typo limit, best first (nil when
// the name exists as written or nothing is close).
func (ix *niIndex) resolveAll(tk niTok) []niCand {
	nameKey := fzKey(tk.Name)
	var dirKey string
	if j := strings.LastIndexByte(tk.Name, '/'); j >= 0 {
		dirKey = fzKey(strings.TrimPrefix(tk.Name[:j], "./"))
		nameKey = fzKey(tk.Name[j+1:])
	}
	if len(nameKey) < 3 {
		return nil
	}
	wantSym := tk.Hint == "symbol" || tk.Shape == "snake" || tk.Shape == "camel"
	wantFile := tk.Hint != "symbol" || tk.Shape == "path" || tk.Shape == "ext"
	if tk.Shape == "path" || tk.Shape == "ext" {
		wantSym = false
	}
	// it exists as written: nothing to fix
	if _, ok := ix.relIndex(strings.TrimPrefix(tk.Raw, "./")); ok {
		return nil
	}
	if tk.Shape == "ext" && ix.hasBase(tk.Raw) {
		return nil
	}
	lim := fzLimit(len([]rune(nameKey)))
	qm, ql := niMask(nameKey)
	var cands []niCand
	if wantFile {
		for i := range ix.files {
			f := &ix.files[i]
			fa := &ix.fast[i]
			if tk.OpenOnly {
				if f.StemKey == nameKey && !f.Doc || f.StemKey == nameKey && tk.Raw != f.Base {
					cands = append(cands, niCand{Rel: f.Rel, Test: f.Test, Ext: f.Ext, d: 0, Score: 0})
				}
				continue
			}
			var d int
			if tk.Ext == "" && tk.Shape != "path" && f.Base == tk.Raw {
				return nil // a file with exactly this name exists
			}
			// lower bounds first: most files are rejected without a distance
			d = lim + 1
			if niBound(qm, ql, fa.stemMask, fa.stemLen) <= lim {
				d = fzDist(nameKey, f.StemKey, lim)
			}
			viaCore := false
			if f.CoreKey != f.StemKey && niBound(qm, ql, fa.coreMask, fa.coreLen) <= lim {
				if dc := fzDist(nameKey, f.CoreKey, lim); dc < d {
					d, viaCore = dc, true
				}
			}
			if d > lim {
				continue
			}
			score := d
			var why []string
			if viaCore && tk.Hint != "test" && tk.Verb != "test" {
				score += fzSlip // matched only through its test affix, and the sentence is not about tests
			}
			if dirKey != "" {
				dd := ix.dirDist(f, dirKey)
				if dd > fzLimit(len([]rune(dirKey))) {
					continue
				}
				score += dd
			}
			if tk.Ext != "" {
				switch {
				case f.Ext == tk.Ext:
					why = append(why, "mesma extensão")
				case fzDist(strings.TrimPrefix(tk.Ext, "."), strings.TrimPrefix(f.Ext, "."), fzSlip) <= fzSlip:
					score += fzSlip
				default:
					continue
				}
			} else if f.Ext == ix.dominant && ix.dominant != "" && !wantSym {
				score -= fzSlip
				why = append(why, "linguagem principal do repo ("+ix.dominant+")")
			} else if f.Doc && tk.Hint != "file" {
				score += fzSlip
			}
			switch {
			case tk.Hint == "test" || tk.Verb == "test" && f.Test:
				if f.Test {
					score -= 3
					why = append(why, "você falou de teste e este é arquivo de teste")
				} else if tk.Hint == "test" {
					score += fzSlip
				}
			case tk.Hint == "symbol":
				score += 2 * fzEdit
			case tk.Verb == "run" && f.Test:
				score -= fzSlip
			}
			if tk.Hint == "dir" {
				score += fzSlip
			}
			if niIsTouched(filepath.Join(ix.root, filepath.FromSlash(f.Rel))) {
				score -= fzSlip
				why = append(why, "já mexemos nele nesta sessão")
			}
			cands = append(cands, niCand{Rel: f.Rel, Test: f.Test, Ext: f.Ext, d: d, Score: score, Why: why})
		}
	}
	if tk.OpenOnly {
		if len(cands) == 0 || len(cands) > 3 {
			return nil
		}
		for i := range cands {
			cands[i].Why = []string{"arquivo com esse nome, depois de '" + tk.VerbWord + "'"}
		}
		return niTop(cands)
	}
	if wantSym {
		for _, s := range ix.symbols() {
			if s.Name == tk.Raw {
				return nil // the symbol exists as written
			}
			if niBound(qm, ql, s.mask, s.klen) > lim {
				continue
			}
			d := fzDist(nameKey, s.Key, lim)
			if d > lim {
				continue
			}
			score := d
			var why []string
			if tk.Hint == "symbol" {
				score -= 3
				why = append(why, "você falou de "+tk.CueWord+", então símbolo e não arquivo")
			}
			if niIsTouched(filepath.Join(ix.root, filepath.FromSlash(s.Rel))) {
				score -= fzSlip
				why = append(why, "no arquivo que já mexemos nesta sessão")
			}
			cands = append(cands, niCand{Rel: s.Rel, Symbol: s.Name, d: d, Score: score, Why: why})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	ix.recency(cands)
	niSort(cands)
	return cands
}

// dirDist compares the directory part of a token with the file's trailing dirs.
func (ix *niIndex) dirDist(f *niFile, dirKey string) int {
	j := strings.LastIndexByte(f.Rel, '/')
	if j < 0 {
		return 1 << 20
	}
	segs := strings.Split(f.Rel[:j], "/") // folded dir segments
	for i := range segs {
		segs[i] = fzKey(segs[i])
	}
	best := 1 << 20
	for k := 1; k <= len(segs); k++ { // any suffix of the file's dir path
		cand := strings.Join(segs[len(segs)-k:], "")
		if d := fzDist(dirKey, cand, 3*fzEdit); d < best {
			best = d
		}
	}
	return best
}

// recency lowers the score of files modified in the last day (top few only).
func (ix *niIndex) recency(c []niCand) {
	sort.SliceStable(c, func(i, j int) bool { return c[i].Score < c[j].Score })
	for i := 0; i < len(c) && i < 5; i++ {
		if fi, err := os.Stat(filepath.Join(ix.root, filepath.FromSlash(c[i].Rel))); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
			c[i].Score -= fzSlip
			c[i].Why = append(c[i].Why, "modificado hoje")
		}
	}
}

// niSort orders candidates: score, then shorter path, then name.
func niSort(c []niCand) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Score != c[j].Score {
			return c[i].Score < c[j].Score
		}
		if len(c[i].Rel) != len(c[j].Rel) {
			return len(c[i].Rel) < len(c[j].Rel)
		}
		return c[i].Rel+c[i].Symbol < c[j].Rel+c[j].Symbol
	})
}

// niTop sorts and keeps the winner, or the tied leaders (<= 3).
func niTop(c []niCand) []niCand {
	if len(c) == 0 {
		return nil
	}
	niSort(c)
	n := 1
	for n < len(c) && n < 3 && c[n].Score-c[0].Score < fzSlip {
		n++
	}
	return c[:n]
}

// niNote builds the note for the user's message ("" = nothing to say).
func niNote(root, text string) string {
	if root == "" || strings.TrimSpace(text) == "" {
		return ""
	}
	// tokens first: a prompt with no name-like word never builds the index
	toks := niTokens(text, func(ext string) bool { return niGet(root).exts[ext] > 0 })
	if len(toks) == 0 {
		return ""
	}
	ix := niGet(root)
	if len(ix.files) == 0 {
		return ""
	}
	var parts []string
	for _, tk := range toks {
		c := ix.resolve(tk)
		if len(c) == 0 {
			continue
		}
		parts = append(parts, niFormat(tk, c, ix))
		if len(parts) >= niMaxNotes {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "nomes aproximados (deduzi de erros de digitação; confira e discorde se estiver errado): " + strings.Join(parts, "; ")
}

func niFormat(tk niTok, c []niCand, ix *niIndex) string {
	var names []string
	for _, x := range c {
		names = append(names, "`"+x.Display()+"`")
	}
	why := niWhy(tk, c, ix)
	if len(c) > 1 {
		return "`" + tk.Raw + "` → " + strings.Join(names, " ou ") + " (empate: " + why + ")"
	}
	return "`" + tk.Raw + "` → " + names[0] + " (" + why + ")"
}

// niWhy is the one-line reasoning (<= 3 reasons).
func niWhy(tk niTok, c []niCand, ix *niIndex) string {
	var w []string
	switch {
	case tk.Hint != "" && tk.CueWord != "":
		w = append(w, "mencionado depois de '"+tk.CueWord+"'")
	case tk.Verb != "" && tk.VerbWord != "":
		kind := "nome"
		if tk.Shape == "ext" || tk.Shape == "path" {
			kind = "arquivo"
		}
		w = append(w, kind+" mencionado junto de '"+tk.VerbWord+"'")
	}
	w = append(w, c[0].Why...)
	if len(c) == 1 {
		w = append(w, ix.uniqueWhy(c[0], tk))
	}
	var out []string
	seen := map[string]bool{}
	for _, x := range w {
		if x != "" && !seen[x] && len(out) < 3 {
			seen[x] = true
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		out = append(out, "o mais parecido")
	}
	return strings.Join(out, "; ")
}

// uniqueWhy says when the winner is the only file of its kind with this stem.
func (ix *niIndex) uniqueWhy(c niCand, tk niTok) string {
	if c.Symbol != "" || c.Ext == "" {
		return ""
	}
	nameKey := fzKey(tk.Name)
	if j := strings.LastIndexByte(tk.Name, '/'); j >= 0 {
		nameKey = fzKey(tk.Name[j+1:])
	}
	lim := fzLimit(len([]rune(nameKey)))
	qm, ql := niMask(nameKey)
	n := 0
	for i := range ix.files {
		if ix.files[i].Ext == c.Ext && niBound(qm, ql, ix.fast[i].stemMask, ix.fast[i].stemLen) <= lim && fzDist(nameKey, ix.files[i].StemKey, lim) <= lim {
			n++
		}
	}
	if n == 1 {
		return "único " + c.Ext + " com esse radical"
	}
	return ""
}

// niSimilarPaths: closest existing files for a path that does not exist
// (read/edit/write error messages). Context-free except the session.
func niSimilarPaths(root, path string, maxD int) []string {
	if root == "" || strings.TrimSpace(path) == "" {
		return nil
	}
	ix := niGet(root)
	rel := path
	if filepath.IsAbs(path) {
		r, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(r, "..") {
			return nil
		}
		rel = r
	}
	rel = filepath.ToSlash(rel)
	tk := niTok{Raw: rel, Name: rel, Shape: "path"}
	if e := strings.ToLower(filepath.Ext(rel)); e != "" && niExtOK(e[1:]) {
		tk.Ext = e
		tk.Name = strings.TrimSuffix(rel, filepath.Ext(rel))
	}
	if !strings.Contains(rel, "/") {
		tk.Shape = "ext"
		if tk.Ext == "" {
			tk.Shape = "plain"
		}
	}
	var out []string
	for _, c := range ix.resolve(tk) {
		if c.d <= maxD {
			out = append(out, c.Rel)
		}
	}
	return out
}

// niSimilarSymbols: near matches for an identifier that a search did not find.
func niSimilarSymbols(root, ident string) []string {
	if root == "" || len(ident) < 4 {
		return nil
	}
	ix := niGet(root)
	key := fzKey(ident)
	lim := fzLimit(len([]rune(key)))
	qm, ql := niMask(key)
	type sc struct {
		s string
		d int
	}
	var all []sc
	seen := map[string]bool{}
	for _, s := range ix.symbols() {
		if s.Name == ident || seen[s.Name] {
			continue
		}
		if niBound(qm, ql, s.mask, s.klen) > lim {
			continue
		}
		if d := fzDist(key, s.Key, lim); d <= lim {
			seen[s.Name] = true
			all = append(all, sc{s.Name + " (" + s.Rel + ")", d})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].d < all[j].d })
	var out []string
	for i, a := range all {
		if i >= 3 {
			break
		}
		out = append(out, a.s)
	}
	return out
}

// niInject is the note appended to the user's message (cache-safe: it lives
// in the user turn, never in the system prompt).
func (a *Agent) niInject(input string) string {
	if a.sub || a.tools == nil {
		return ""
	}
	n := niNote(a.tools.Root, input)
	if n == "" {
		return ""
	}
	return "\n\n<system-reminder>\n" + n + "\n</system-reminder>"
}

var niSearchSym = lazyRx(`-- ('[^']*'|\S+) \.`)

// niEmptySearchHint: a symbol search (fast paths where/search) found nothing;
// suggest near symbols from the index. "" = nothing to add.
func niEmptySearchHint(root, intent, cmd, out string, code int) string {
	if (intent != "where" && intent != "search" && intent != "search_code") || code != 1 || strings.TrimSpace(out) != "" {
		return ""
	}
	m := niSearchSym.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	sim := niSimilarSymbols(root, strings.Trim(m[1], "'"))
	if len(sim) == 0 {
		return ""
	}
	return "sem resultado; parecido: " + strings.Join(sim, ", ")
}

// ---------- typed entity resolution (offline brain) ----------

// niEntity is one candidate of niResolveEntity: a file, a symbol, or a thing
// of the environment (envprobe.go: binary, toolchain, SSH host, Tailscale
// peer, service, container, port).
type niEntity struct {
	Kind   string // file | test | dir | symbol | bin | toolchain | ssh_host | ts_peer | service | container | port
	Name   string // path for files, identifier for symbols, name otherwise
	Rel    string // file of a symbol
	Detail string
	Score  int    // lower is better (x8 fz units, with context bonuses)
	Dist   int    // edit distance alone (fz units; 0 = exact)
	Query  string // what was typed
	Exact  bool
	Why    []string
}

// Display is how a candidate is shown in a numbered menu.
func (e niEntity) Display() string {
	switch {
	case e.Kind == "symbol" && e.Rel != "":
		return e.Name + " (" + e.Rel + ")"
	case e.Detail != "" && e.Kind != "file" && e.Kind != "test":
		return e.Name + " — " + niKindPT[e.Kind] + " " + e.Detail
	}
	return e.Name
}

var niKindPT = map[string]string{"file": "arquivo", "test": "teste", "dir": "pasta", "symbol": "símbolo", "bin": "binário",
	"toolchain": "ferramenta", "ssh_host": "host SSH", "ts_peer": "máquina Tailscale", "service": "serviço", "container": "contêiner", "port": "porta"}

// niEnvKinds are the kinds that come from the environment profile.
var niEnvKinds = map[string]bool{"bin": true, "toolchain": true, "ssh_host": true, "ts_peer": true, "service": true, "container": true, "port": true}

// fzJaroWinkler is the Jaro-Winkler similarity of two folded strings in
// thousandths (1000 = equal). Allocation: two small bool slices.
func fzJaroWinkler(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 || lb == 0 {
		return 0
	}
	if a == b {
		return 1000
	}
	win := max(la, lb)/2 - 1
	if win < 0 {
		win = 0
	}
	ma := make([]bool, la)
	mb := make([]bool, lb)
	m := 0
	for i := 0; i < la; i++ {
		lo, hi := max(0, i-win), min(lb-1, i+win)
		for j := lo; j <= hi; j++ {
			if !mb[j] && ra[i] == rb[j] {
				ma[i], mb[j] = true, true
				m++
				break
			}
		}
	}
	if m == 0 {
		return 0
	}
	t, k := 0, 0
	for i := 0; i < la; i++ {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if ra[i] != rb[k] {
			t++
		}
		k++
	}
	// jaro*3000 = m/la + m/lb + (m-t/2)/m, all scaled by 1000
	jaro := (1000*m/la + 1000*m/lb + 1000*(2*m-t)/(2*m)) / 3
	p := 0
	for p < 4 && p < la && p < lb && ra[p] == rb[p] {
		p++
	}
	return jaro + p*(1000-jaro)/10
}

// niEnvScore: distance of q to an environment name; ok=false = too far.
// Accepts Damerau within the typo limit or Jaro-Winkler >= 0.88.
func niEnvScore(q, key string) (int, bool) {
	if q == key {
		return 0, true
	}
	lim := fzLimit(len([]rune(q)))
	if d := fzDist(q, key, lim); d <= lim {
		return d, true
	}
	if len(q) >= 5 {
		if jw := fzJaroWinkler(q, key); jw >= 880 {
			return 2*fzEdit + (1000-jw)/20, true
		}
	}
	return 0, false
}

// niResolveEntity resolves q against the project index and the environment,
// restricted to kinds (nil = every kind). Exact names win outright; otherwise
// the fuzzy candidates are ranked by distance, then by the order of kinds
// (the slot's expected type comes first), then toolchains before plain PATH
// binaries ("frequency"). Returns the candidates best first (at most max).
func niResolveEntity(root, q string, kinds []string, hint string, max int) []niEntity {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	if max <= 0 {
		max = 5
	}
	want := map[string]int{}
	for i, k := range kinds {
		if _, ok := want[k]; !ok {
			want[k] = i
		}
	}
	allow := func(k string) bool {
		if len(kinds) == 0 {
			return true
		}
		_, ok := want[k]
		if !ok && k == "test" {
			_, ok = want["file"]
		}
		return ok
	}
	rank := func(k string) int {
		if i, ok := want[k]; ok {
			return i
		}
		return len(kinds)
	}
	var out []niEntity
	key := fzKey(q)
	// files and symbols of the project
	if root != "" && (allow("file") || allow("test") || allow("symbol") || allow("dir")) {
		ix := niGet(root)
		out = append(out, ix.entityFiles(q, key, hint, allow)...)
	}
	// the environment
	envAllowed := len(kinds) == 0
	for k := range want {
		envAllowed = envAllowed || niEnvKinds[k]
	}
	if envAllowed {
		p := epPeek()
		if p == nil {
			p = epGet(false)
		}
		seen := map[string]int{} // name -> index in out (one entry per name: the best kind)
		for _, e := range epEntities(p) {
			if !allow(e.Kind) || len(e.Key) == 0 {
				continue
			}
			d, ok := niEnvScore(key, e.Key)
			if !ok {
				continue
			}
			c := niEntity{Kind: e.Kind, Name: e.Name, Detail: e.Detail, Score: d*8 + rank(e.Kind), Dist: d, Exact: d == 0}
			if e.Kind == "toolchain" {
				c.Score -= 2
			}
			if d > 0 {
				c.Why = append(c.Why, "nome parecido com '"+q+"'")
			}
			if i, dup := seen[e.Name]; dup {
				if c.Score < out[i].Score {
					out[i] = c
				}
				continue
			}
			seen[e.Name] = len(out)
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Exact != out[j].Exact {
			return out[i].Exact
		}
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	// exact matches hide the fuzzy ones
	if len(out) > 0 && out[0].Exact {
		n := 1
		for n < len(out) && out[n].Exact {
			n++
		}
		out = out[:n]
	}
	if len(out) > max {
		out = out[:max]
	}
	for i := range out {
		out[i].Query = q
	}
	return out
}

// entityFiles: exact file/stem/symbol matches first, then the context-aware
// fuzzy ranking of resolveAll. Scores are scaled to the env scale (x8).
func (ix *niIndex) entityFiles(q, key, hint string, allow func(string) bool) []niEntity {
	var out []niEntity
	kindOf := func(f *niFile) string {
		if f.Test {
			return "test"
		}
		return "file"
	}
	if allow("file") || allow("test") {
		if i, ok := ix.relIndex(strings.TrimPrefix(filepath.ToSlash(q), "./")); ok {
			f := &ix.files[i]
			return []niEntity{{Kind: kindOf(f), Name: f.Rel, Exact: true, Why: []string{"caminho exato"}}}
		}
		for i := range ix.files {
			f := &ix.files[i]
			if f.Base == q || f.StemKey == key && key != "" || hint == "test" && f.Test && f.CoreKey == key && key != "" {
				if hint == "test" && !f.Test && f.Base != q {
					continue
				}
				why := "nome exato"
				if f.Base != q {
					why = "mesmo nome sem extensão"
				}
				out = append(out, niEntity{Kind: kindOf(f), Name: f.Rel, Exact: true, Why: []string{why}})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if allow("symbol") && (hint == "symbol" || niLooksIdent(q)) {
		for _, s := range ix.symbols() {
			if s.Name == q {
				out = append(out, niEntity{Kind: "symbol", Name: s.Name, Rel: s.Rel, Exact: true, Why: []string{"símbolo com esse nome"}})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	tk := niTok{Raw: q, Name: q, Shape: "plain", Hint: hint}
	if hint == "" {
		tk.Hint = "file"
	}
	if j := strings.LastIndexByte(q, '.'); j > 0 && j < len(q)-1 && niExtOK(strings.ToLower(q[j+1:])) {
		tk.Ext, tk.Name, tk.Shape = strings.ToLower(q[j:]), q[:j], "ext"
	}
	if strings.Contains(q, "/") {
		tk.Shape = "path"
	}
	if hint == "symbol" || (niLooksIdent(q) && tk.Shape == "plain" && !allow("file")) {
		tk.Hint, tk.Shape = "symbol", "camel"
	}
	for _, c := range ix.resolveAll(tk) {
		e := niEntity{Name: c.Rel, Score: c.Score * 8, Dist: c.d, Why: c.Why, Kind: "file"}
		if c.Symbol != "" {
			if !allow("symbol") {
				continue
			}
			e.Kind, e.Name, e.Rel = "symbol", c.Symbol, c.Rel
		} else if c.Test {
			e.Kind = "test"
		}
		if !allow(e.Kind) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// niLooksIdent: camelCase, snake_case or CapWord (a code identifier).
func niLooksIdent(s string) bool {
	if len(s) < 3 {
		return false
	}
	up, low, us := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			low = true
		case c == '_':
			us = true
		case c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return (up && low) || (us && low)
}
