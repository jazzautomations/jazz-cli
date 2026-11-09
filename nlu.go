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
	"os"
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
	Dropped    []string          `json:"dropped,omitempty"`   // quoted/role operands no slot consumed — "pattern 'x'" num intent sem pattern
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
	// GoalPassthrough: o método consome $goal inteiro (ex.: config_get
	// passa o pedido cru pro script casar a chave) — palavra residual
	// livre já está coberta pelo goal, não é operando perdido
	GoalPassthrough bool `json:"goal_passthrough,omitempty"`
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
	if strings.Trim(s, ".") == "" || strings.Trim(s, ":") == "" {
		return s // ".."/"..."/":" — pontuação pura é o próprio token
	}
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
				if q == "" && j > i+1 {
					// "' '" — whitespace aspado é literal (espaço/tab
					// são operandos reais de replace/delim); mantém o
					// interior cru pra tab não virar espaço
					q = string(rs[i+1 : j])
				}
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
		// "${var}" — o } de fechamento faz parte da var shell, não é
		// pontuação a podar: reanexa quando o token abriu ${
		if strings.HasPrefix(w, "${") && !strings.HasSuffix(w, "}") &&
			strings.HasSuffix(string(rs[i:j]), "}") {
			w += "}"
		}
		i = j
		if w == "" {
			continue
		}
		f := fzFold(w)
		if k := strings.IndexByte(f, '\''); k > 0 && !strings.ContainsAny(f, "./") { // what's -> what
			poss := f[k:] == "'s" // possessivo "aluno1's" — marca o dono
			f, w = f[:k], w[:k]
			out = append(out, nuTok{Raw: w, Fold: f})
			if poss {
				// filler: atravessa phrasing sem peso mas segue
				// visível pro scan possessivo que checa o fold
				out = append(out, nuTok{Raw: "'s", Fold: "'s", Filler: true})
			}
			continue
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
		// token com curinga é um glob de nome — sintético, não vem do
		// léxico: "files matching *.log" separa "matching ERROR" (conteúdo).
		// Glob chars: * ? [ — "file??" é padrão de nome tanto quanto "*.log"
		if strings.ContainsAny(t.Raw, "*?[") {
			t.Concs = append(t.Concs, "glob")
			continue
		}
		// GOPATH/JAVA_HOME junto a cue de var ("valor de X",
		// "variable X") é env-var — SCREAMING_CASE é a forma.
		if len(t.Raw) >= 3 && t.Raw == strings.ToUpper(t.Raw) &&
			strings.IndexFunc(t.Raw, unicode.IsLetter) >= 0 {
			for _, dir := range []int{-1, 1} {
				for j := i + dir; j >= 0 && j < len(out) &&
					j != i+4*dir && j != i-4*dir; j += dir {
					u := &out[j]
					if u.Quoted {
						break
					}
					if u.has("keynoun") || u.has("envvars") {
						t.Concs = append(t.Concs, "envvars")
						break
					}
					if !u.Filler {
						break // palavra de conteúdo fecha a janela
					}
				}
			}
		}
		// "g+w"/"o+x" soltos são sintaxe de permissão — conceito próprio
		// pra vetar buscas de nome ("files missing o+w" ≠ name "o+w")
		if nuPermBitToken(t.Fold) {
			t.Concs = append(t.Concs, "permbit")
		}
		// "newer than report.txt": o alvo do than é REFERÊNCIA temporal,
		// nunca predicado de nome — marca antes de qualquer sponge pegar.
		// 'than' costuma virar stop-word: o alvo é o não-tempo logo após
		// o cue newer/older (com ou sem o 'than' materializado).
		switch t.Fold {
		case "newer", "older", "novo", "nova", "velho", "velha",
			"antigo", "antiga":
			thanSeen := false
			for j := i + 1; j < len(out) && j <= i+4; j++ {
				u := &out[j]
				switch u.Fold {
				case "than", "que", "do":
					thanSeen = true
					continue
				}
				if u.Filler || u.Fold == "a" || u.Fold == "the" ||
					u.Fold == "that" {
					continue
				}
				if nuIsNumber(u.Fold) || timeWords[u.Fold] {
					break // "3 days" é janela em minutos, não arquivo
				}
				// "newer than the file 'Nov'" — a palavra-classe é
				// ponte; o alvo é o operando depois dela
				if u.Fold == "file" || u.Fold == "arquivo" ||
					u.Fold == "files" || u.Fold == "named" {
					continue
				}
				// sem 'than' explícito, só marca se o alvo tem forma de
				// arquivo — "arquivos recentes importantes" não compara
				if thanSeen || u.Quoted || strings.ContainsAny(u.Fold, "./") {
					u.Concs = append(u.Concs, "timeref")
				}
				break
			}
		}
		// "print list to a file named 'rm'" / "save the list to 'out.txt'" —
		// destino de ESCRITA, não nome de busca: prep+file-cue na janela
		// de lookback marca outtgt. Roda ANTES do shape e do cue de nome:
		// "to 'out.txt'" tem forma de arquivo E é destino. Aspa com forma
		// de arquivo dispensa o file-cue ("save to 'x.txt'"); palavra
		// nua ('rm') exige o cue pra não marcar destino à toa.
		if t.Quoted && i > 0 {
			shaped := nuFileShaped(t.Fold) || strings.Contains(t.Raw, ".") || strings.Contains(t.Raw, "/")
			toSeen, fileCue := false, false
			for j := i - 1; j >= 0 && j >= i-5; j-- {
				u := &out[j]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "to", "para", "pra", "pro", "into", "tee":
					toSeen = true
				case "file", "arquivo", "named", "called", "output":
					fileCue = true
				case "and", "or", "e", "ou", "which", "that", "because", "que":
					j = 0 // cláusula nova encerra a janela
				}
				if j == 0 {
					break
				}
				if toSeen && (fileCue || shaped) {
					t.Concs = append(t.Concs, "outtgt")
					break
				}
			}
			if t.has("outtgt") {
				continue
			}
		}
		// aspas com forma de arquivo ("foo.txt", "/etc/x", "file1") =
		// nome de arquivo citado — vence forquoted: "find the file \"x.c\"".
		// Terminada em "/" é forma de PASTA ('/usr/ports/'), nunca file.
		if t.Quoted && !strings.HasSuffix(t.Raw, "/") &&
			(nuFileShaped(t.Fold) || strings.Contains(t.Raw, ".") || strings.Contains(t.Raw, "/")) {
			t.Concs = append(t.Concs, "filequoted")
			continue
		}
		// aspa precedida de cue de nome de arquivo é o NOME mesmo sem
		// ponto: "locate file 'file1'", "files whose filename is 'x'",
		// "a file named 'apt'" — o cue marca o slot, não a forma.
		// Multi-palavra segue texto, nunca nome.
		if t.Quoted && i > 0 && !strings.Contains(t.Raw, " ") {
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				u := &out[j]
				// aspa após prep de escopo ("in 'filename'") em contexto
				// de conteúdo (grep/count/slice) é o ARQUIVO alvo — sem
				// o contexto seria dir ("find files in 'docs'"). A prep
				// É filler — checar antes do skip. Só preposição real:
				// "letter e or f" tem 'or' conjunção, não escopo.
				isScopePrep := false
				switch u.Fold {
				case "in", "inside", "under", "within", "into",
					"em", "no", "na", "nos", "nas", "dentro",
					"on", "onto", "at":
					isScopePrep = true
				}
				// "under 'X'"/"inside 'X'" — preposição de escopo
				// exclusiva: um arquivo não fica "under" outro arquivo;
				// a aspa é o DIRETÓRIO mesmo com contexto de conteúdo
				scopeOnly := false
				switch u.Fold {
				case "under", "underneath", "beneath", "inside",
					"within", "abaixo", "dentro", "embaixo":
					scopeOnly = true
				}
				if scopeOnly {
					continue
				}
				if isScopePrep {
					// "in 'a' and 'b'" — dois operandos de arquivo: o
					// slot é único, marcar um calaria o outro (falso
					// sucesso). Sem tag → dropped honesto.
					ambiguous := false
					for k := i + 1; k < len(out) && k <= i+4; k++ {
						w := &out[k]
						if w.Filler || w.Fold == "," {
							continue
						}
						switch w.Fold {
						case "and", "or", "e", "ou", "mas":
							for l := k + 1; l < len(out) && l <= k+3; l++ {
								if out[l].Quoted {
									ambiguous = true
								}
							}
						}
						break
					}
					if !ambiguous {
						for k := range out {
							if out[k].has("content") || out[k].has("grepword") ||
								out[k].has("count") || out[k].has("lineselect") {
								t.Concs = append(t.Concs, "filequoted")
								break
							}
						}
					}
					break
				}
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "file", "filename", "filenames", "name", "names",
					"arquivo", "nome", "nomes", "is", "are":
					t.Concs = append(t.Concs, "filequoted")
				case "directory", "dir", "diretorio", "diretório",
					"folder", "pasta":
					// "look for directory 'cookbook'" — a aspa é o
					// nome da pasta; MAS "in the folder 'x'" é
					// escopo (o dir_cued reivindica), não nome
					scoped := false
					for k := j - 1; k >= 0 && k >= j-3; k-- {
						w := &out[k]
						if w.Fold == "the" || w.Fold == "a" ||
							w.Fold == "an" || w.Fold == "o" {
							continue
						}
						if nuReplScopeCue[w.Fold] || w.Fold == "in" ||
							w.Fold == "em" || w.Fold == "de" {
							scoped = true
						}
						break
					}
					if !scoped {
						t.Concs = append(t.Concs, "filequoted")
					}
				case "named", "called":
					// "contain a file named 'bin'" — a aspa rotula o
					// item contido, não o arquivo da busca
					contained := false
					for k := j - 1; k >= 0 && k >= j-5; k-- {
						w := &out[k]
						if w.Filler || nuArcTypeWord(w.Fold) ||
							nuAllFormatWords(w.Fold) ||
							w.Fold == "file" || w.Fold == "files" {
							continue
						}
						switch w.Fold {
						case "contain", "contains", "containing", "contem",
							"contém", "contendo", "tem", "tenha", "hold",
							"holds", "holding", "have", "has", "having",
							"with", "without", "missing", "lacking":
							contained = true
						}
						break
					}
					if !contained {
						t.Concs = append(t.Concs, "filequoted")
					} else {
						// marcação negativa: a aspa pertence à cláusula
						// "contains a file named X" — não é padrão de
						// conteúdo nem nome do alvo da busca
						t.Concs = append(t.Concs, "conttgt")
					}
				}
				// "lines in 'filename'" — a aspa após unidade de contagem
				// é o OPERANDO (arquivo), não escopo de diretório. Só
				// lineselect: "count 'x'" conta o padrão x, não um file.
				if u.has("lineselect") {
					t.Concs = append(t.Concs, "filequoted")
				}
				j = 0 // só a primeira palavra útil manda
			}
			if t.has("filequoted") {
				continue
			}
		}
		// aspa seguida de "in their name"/"path names" é alvo de NOME,
		// não conteúdo: "files with the word 'fstab' in their name" é
		// file_find name=fstab. O lookahead vence o forquoted lookback.
		if t.Quoted && i > 0 {
			nameTgt := false
			for j := i + 1; j < len(out) && j <= i+5; j++ {
				if out[j].Quoted {
					continue // aspa citada é operando, nunca cue-palavra
				}
				switch out[j].Fold {
				case "name", "names", "nome", "nomes", "filename", "filenames", "pathname", "pathnames":
					nameTgt = true
				}
			}
			if nameTgt {
				t.Concs = append(t.Concs, "nametgt")
				continue
			}
		}
		// aspas depois de "for"/"por" = alvo de CONTEÚDO (o padrão do
		// grep), não nome de arquivo: "search files for \"foo\""
		if t.Quoted && i > 0 {
			for j := i - 1; j >= 0; j-- {
				// prep de escopo encerra o lookback: a aspa depois de
				// "in"/"em" é o ALVO (arquivo/pasta), nunca o padrão —
				// "count 'x' characters in 'filename'" tem 'filename'
				// como file, não como forquoted
				if nuReplScopeCue[out[j].Fold] {
					break
				}
				// "for"/"por" são fillers também — checar ANTES do skip.
				// meta-palavras de conteúdo são transparentes: "for the
				// string \"x\"" marca do mesmo jeito que "for \"x\"";
				// "strings \"x\"" e "matching \"x\"" marcam pelo
				// próprio cue de conteúdo
				if out[j].Fold == "for" || out[j].Fold == "por" ||
					out[j].has("content") || out[j].has("grepword") {
					// "names contain 'log'": um predicado de posse com
					// palavra de nome à esquerda descreve o NOME —
					// a aspa é alvo de nome, não conteúdo
					isName := false
					for k := j - 1; k >= 0 && k >= j-3; k-- {
						if out[k].has("nameattr") {
							isName = true
							break
						}
						if !out[k].Filler {
							break
						}
					}
					if isName {
						t.Concs = append(t.Concs, "nametgt")
					} else {
						t.Concs = append(t.Concs, "forquoted")
					}
					break
				}
				// flag de case é modificador transparente: "containing
				// any-cased 'error'" marca como "containing 'error'"
				if out[j].Filler || out[j].has("icase") || nuCaseFlags[out[j].Fold] {
					continue
				}
				break // a primeira palavra útil não era cue
			}
		}
		// "search for aaa in all files" — palavra NUA entre "for" e a
		// prep de escopo é o padrão de conteúdo, não um nome: exige
		// verbo de busca à esquerda e escopo à direita pra não marcar
		// qualquer "for X" ("look for files in docs" → files é classe)
		if !t.Quoted && len(t.Concs) == 0 && i > 0 {
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				u := &out[j]
				if u.Fold == "for" || u.Fold == "por" {
					verb := false
					for k := j - 1; k >= 0 && k >= j-3; k-- {
						if out[k].has("find") || out[k].has("grepword") ||
							out[k].has("content") {
							verb = true
							break
						}
						if !out[k].Filler {
							break
						}
					}
					scope := false
					for k := i + 1; k < len(out) && k <= i+4; k++ {
						if nuReplScopeCue[out[k].Fold] {
							scope = true
							break
						}
						if !out[k].Filler && !nuAllFormatWords(out[k].Fold) {
							break
						}
					}
					// concs ainda não foram atribuídos neste ponto —
					// o fold é a única prova de substantivo-classe
					// ("search for all files" — files é o objeto,
					// nunca o padrão citado)
					if verb && scope && !nuFileCueWords[t.Fold] &&
						!t.has("files") && !t.has("filenoun") &&
						!t.has("dirnoun") && !t.has("dir") && !nuIsNumber(t.Fold) {
						t.Concs = append(t.Concs, "forquoted")
					}
				}
				if u.Filler {
					continue
				}
				break
			}
		}
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
	for i := range out {
		t := &out[i]
		// conceitos sintéticos ADITIVOS — rodam depois do léxico pra não
		// esconder o conceito real ("tar" é archive E palavra de ext)
		if nuCaseFlags[t.Fold] {
			// flag de case é modificador de comparação, nunca nome:
			// "case-insensitive example.com file" não tem name "insensitive"
			t.Concs = append(t.Concs, "icase")
		}
		// palavra de extensão é filtro de busca — "list the text
		// files" tem extcue ("text"→txt), "list the files" não tem
		switch {
		case nuExtWords[t.Fold], nuPureExt(t.Fold),
			t.Fold == "text", t.Fold == "texto", t.Fold == "textos":
			t.Concs = append(t.Concs, "extcue")
		}
		// aspa que é arg de nome (ext/glob/nome-de-arquivo) não é
		// "texto livre" — perde a tag @quoted pra não vetar file_find:
		// 'the "jpg" file' filtra por ext; "'timeout' nos arquivos"
		// continua texto e veta (vai pro find/grep).
		if t.Quoted && (t.has("extcue") || t.has("glob") || t.has("filequoted")) {
			kept := t.Tags[:0]
			for _, tg := range t.Tags {
				if tg != "@quoted" {
					kept = append(kept, tg)
				}
			}
			t.Tags = kept
		}
	}
	// operando CITADO de cláusula de exceção ("omitting directory
	// 'omit-directory'") — a aspa marcaria @quoted e vetaria as regras
	// find+files; 'exctgt' devolve a evidência de exclusão pro
	// file_find sem fingir nome nem conteúdo
	_, exOps, _ := nuExceptOperands(out, nil)
	for _, j := range exOps {
		u := &out[j]
		if u.Quoted && len(u.Concs) == 0 {
			u.Concs = append(u.Concs, "exctgt")
			kept := u.Tags[:0]
			for _, tg := range u.Tags {
				if tg != "@quoted" {
					kept = append(kept, tg)
				}
			}
			u.Tags = kept
		}
	}
	// "occurrences of each word in X" — semântica por-item ("para cada
	// palavra") que nenhum método determinístico cobre. O tag vai na
	// PALAVRA-ITEM (each é filler — regra não lê conceito de filler).
	for i, t := range out {
		switch t.Fold {
		case "each", "every", "cada", "per", "por":
		default:
			continue
		}
		for j := i + 1; j < len(out) && j <= i+3; j++ {
			u := &out[j]
			if u.Filler || u.Slang {
				continue
			}
			switch u.Fold {
			case "word", "words", "palavra", "palavras", "entry", "entries",
				"character", "characters", "caractere", "caracteres",
				"ip", "ips", "email", "emails":
				u.Concs = append(u.Concs, "eachitem")
				j = len(out)
			case "unique", "distinct", "different", "diferentes",
				"unicas", "únicas", "unicos", "únicos", "individual":
				continue // "each UNIQUE word" — qualificador transparente
			default:
				j = len(out) // a primeira palavra útil manda
			}
		}
	}
	// "files that contain spaces in their names" — contain+name-word é
	// predicado de NOME, não grep de conteúdo: o grepword do "contain"
	// demora pra "have" e deixa de vetar list_files/file_find.
	nameDomain := false
	for _, t := range out {
		switch t.Fold {
		case "name", "names", "nome", "nomes", "filename", "filenames",
			"pathname", "pathnames", "basename", "basenames":
			nameDomain = true
		}
	}
	if nameDomain {
		for i := range out {
			t := &out[i]
			// posse/contenção perde o veto grep; verbos de busca
			// (search/grep/find) seguem valendo
			switch t.Fold {
			case "contain", "contains", "containing", "have", "has",
				"tem", "contem", "contém", "contendo", "possui",
				"possuem", "with", "com", "that":
			default:
				continue
			}
			if !t.has("grepword") {
				continue
			}
			kept := t.Concs[:0]
			for _, c := range t.Concs {
				if c == "grepword" {
					continue
				}
				kept = append(kept, c)
			}
			t.Concs = kept
		}
	} else {
		// SEM substantivo "name" na frase, "matching X" é grep de
		// conteúdo, não predicado de nome — o nameattr espúrio veta a
		// rota find e a file_find ao mesmo tempo (parse vazio). Cue de
		// padrão adiante ("matching regex 'x'") mantém o nameattr.
		for i := range out {
			t := &out[i]
			switch t.Fold {
			case "match", "matching", "matches", "casa":
			default:
				continue
			}
			if !t.has("grepword") || !t.has("nameattr") {
				continue
			}
			patCue := false
			for j := i + 1; j < len(out) && j <= i+3; j++ {
				u := &out[j]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "regex", "regexp", "pattern", "patterns", "padrao",
					"padrão", "expression", "expressions", "glob":
					patCue = true
				}
				break
			}
			if patCue {
				continue
			}
			kept := t.Concs[:0]
			for _, c := range t.Concs {
				if c != "nameattr" {
					kept = append(kept, c)
				}
			}
			t.Concs = kept
		}
	}
	// "dirs that contain a file named 'bin'" — o 'named' rotula o item
	// CONTIDO (predicado existencial), não o filtro de nome da busca:
	// o nameattr residual roubava 'bin' pro slot name
	for i := range out {
		t := &out[i]
		switch t.Fold {
		case "named", "called", "nomeado", "nomeada", "chamado", "chamada":
		default:
			continue
		}
		if !t.has("nameattr") {
			continue
		}
		for j := i - 1; j >= 0 && j >= i-5; j-- {
			u := &out[j]
			if u.Filler || nuArcTypeWord(u.Fold) || nuAllFormatWords(u.Fold) ||
				u.Fold == "file" || u.Fold == "files" {
				continue // "a file/directory named" — substantivo-classe é ponte
			}
			switch u.Fold {
			case "contain", "contains", "containing", "contem", "contém",
				"contendo", "tem", "tenha", "hold", "holds", "holding",
				"have", "has", "having", "with", "without", "missing",
				"lacking", "falta", "faltando":
				kept := t.Concs[:0]
				for _, c := range t.Concs {
					if c != "nameattr" {
						kept = append(kept, c)
					}
				}
				t.Concs = kept
				// o 'contain' também perde grepword/content — seu
				// objeto é uma descrição de arquivo ("a file named"),
				// não um padrão de texto; senão o pedido roteia pra
				// text_grep e finge busca de conteúdo
				ck := u.Concs[:0]
				for _, c := range u.Concs {
					if c != "grepword" && c != "content" {
						ck = append(ck, c)
					}
				}
				u.Concs = ck
			}
			break
		}
	}
	// "search the X folder FOR files larger than..." — o 'for' introduz
	// o conjunto enumerável com predicado, não um padrão de conteúdo: o
	// grepword residual vetava o file_find e caía no classifier
	for i := range out {
		t := &out[i]
		switch t.Fold {
		case "for", "por", "para":
		default:
			continue
		}
		if !t.has("grepword") {
			continue
		}
		for j := i + 1; j < len(out) && j <= i+3; j++ {
			u := &out[j]
			if u.Filler {
				continue
			}
			switch u.Fold {
			case "files", "file", "arquivos", "arquivo", "documents",
				"documentos", "images", "imagens", "dirs", "directories",
				"diretorios", "diretórios", "folders", "pastas", "ones":
				kept := t.Concs[:0]
				for _, c := range t.Concs {
					if c != "grepword" {
						kept = append(kept, c)
					}
				}
				t.Concs = kept
			}
			break
		}
	}
	// "the user's home directory" — 'user' é possessivo do escopo ~,
	// não filtro de owner: o conceito user/env vetaria o file_find e
	// rotearia pra perm_find ("ends in .tar-gz" com owner:h)
	for i, t := range out {
		switch t.Fold {
		case "user", "usuario", "usuário":
		default:
			continue
		}
		for j := i + 1; j < len(out) && j <= i+3; j++ {
			u := &out[j]
			if u.Filler || u.Fold == "'s" || u.Fold == "s" {
				continue
			}
			if u.Fold == "home" || u.Fold == "casa" {
				for _, tk := range []*nuTok{&out[i], u} {
					kept := tk.Concs[:0]
					for _, c := range tk.Concs {
						if c != "user" && c != "env" {
							kept = append(kept, c)
						}
					}
					tk.Concs = kept
				}
			}
			break
		}
	}
	// "directory structure"/"folder tree" — o substantivo-lugar é
	// phrasing de escopo ("a árvore deste projeto"), nunca operando
	for i := 1; i < len(out); i++ {
		switch out[i].Fold {
		case "structure", "tree", "hierarchy", "subtree", "layout",
			"estrutura", "arvore", "árvore", "hierarquia":
		default:
			continue
		}
		p := i - 1
		for p >= 0 && out[p].Filler {
			p--
		}
		if p >= 0 && (out[p].has("files") || out[p].has("dirnoun") ||
			out[p].has("filenoun")) {
			out[i].Filler = true
		}
	}
	// "force delete"/"permanently remove" — o adverbio é manner da
	// deleção (a operação já é rm -f semântico), não flag extra
	for i := 1; i < len(out); i++ {
		switch out[i].Fold {
		case "force", "forcibly", "forcefully", "permanently",
			"permanentemente", "definitivamente", "forcada", "forçada",
			"forcado", "forçado":
		default:
			continue
		}
		p := i - 1
		for p >= 0 && out[p].Filler {
			p--
		}
		del := p >= 0 && (out[p].has("delete") || out[p].has("mutate") ||
			out[p].has("removeword"))
		// "force delete" — advérbio ANTES do verbo também é manner
		if !del {
			n := i + 1
			for n < len(out) && out[n].Filler {
				n++
			}
			del = n < len(out) && (out[n].has("delete") ||
				out[n].has("mutate") || out[n].has("removeword"))
		}
		if del {
			out[i].Filler = true
		}
	}
	// "symbolic links" — o adjetivo só descreve o TIPO do link; não é
	// nome literal ("symbolic" no slot name procuraria arquivo 'symbolic')
	for i, t := range out {
		switch t.Fold {
		case "symbolic", "symbolically", "simbolico", "simbólico",
			"simbolica", "simbólica":
		default:
			continue
		}
		for j := i + 1; j < len(out) && j <= i+2; j++ {
			u := &out[j]
			if u.Filler || u.Slang {
				continue
			}
			if u.has("linknoun") {
				out[i].Filler = true
			}
			break
		}
	}
	// "write permission"/"read permission" — o bit de permissão não é
	// o verbo 'write'/'create' ('create' no none do perm_find vetava a
	// busca legítima e a mutação ganhava de bandeja)
	for i, t := range out {
		switch t.Fold {
		case "write", "writes", "escrita", "escrever", "read",
			"leitura", "ler", "execute", "executar", "execucao",
			"execução":
		default:
			continue
		}
		n := i + 1
		for n < len(out) && out[n].Filler {
			n++
		}
		if n < len(out) && nuPermModeCues[out[n].Fold] {
			nc := t.Concs[:0]
			for _, c := range t.Concs {
				if c != "create" && c != "run" && c != "open" {
					nc = append(nc, c)
				}
			}
			out[i].Concs = append(nc, "permattr")
		}
	}
	// "permission set for everybody" — 'set' DEPOIS de substantivo de
	// permissão é particípio ("tendo a permissão"), não o verbo de
	// mutação "set permission to 700". Sem isso uma busca vira chmod.
	for i, t := range out {
		if !t.has("setperm") {
			continue
		}
		p := i - 1
		for p >= 0 && out[p].Filler {
			p--
		}
		if p >= 0 && nuPermModeCues[out[p].Fold] {
			nc := t.Concs[:0]
			for _, c := range t.Concs {
				if c != "setperm" {
					nc = append(nc, c)
				}
			}
			out[i].Concs = nc
		}
	}
	// TODOs/FIXME maiúsculo é a tarefa marcada no código; 'todos'
	// minúsculo segue ambíguo com "all" em PT e fica em `list`
	for i := range out {
		t := &out[i]
		switch t.Raw {
		case "TODO", "TODOs", "FIXME", "FIXMEs", "HACK", "HACKs", "XXX":
			if !t.has("todo") {
				t.Concs = append(t.Concs, "todo")
			}
		}
	}
	// "suppress(ing) error messages"/"silence errors" — stderr é efeito
	// colateral do comando, não o critério da busca: a frase inteira é
	// filler (o verbo vira nome falso, error/message vetam file_find).
	for i := range out {
		t := &out[i]
		switch t.Fold {
		case "suppress", "suppresses", "suppressing", "silence",
			"silencing", "hide", "hiding",
			"supress", "supressing", "discard", "discarding":
		default:
			continue
		}
		t.Filler = true
		for j := i + 1; j < len(out) && j <= i+3; j++ {
			u := &out[j]
			kept := u.Concs[:0]
			for _, c := range u.Concs {
				if c == "error" || c == "message" || c == "logs" {
					continue
				}
				kept = append(kept, c)
			}
			u.Concs = kept
			switch u.Fold {
			case "error", "errors", "erro", "erros", "stderr",
				"message", "messages", "mensagem", "mensagens",
				"output", "saida", "saída":
				u.Filler = true
			}
		}
	}
	// "'error' pattern" — a palavra-meta depois do padrão citado é
	// vocabulário (o que é X), não predicado de nome: solta o nameattr
	// pra não vetar o grep de conteúdo
	for i := 1; i < len(out); i++ {
		if out[i-1].has("forquoted") && out[i].has("nameattr") {
			kept := out[i].Concs[:0]
			for _, c := range out[i].Concs {
				if c != "nameattr" {
					kept = append(kept, c)
				}
			}
			out[i].Concs = kept
		}
	}
	// predicado de nome explícito sem aspas — "named test",
	// "names contain log", "have foo in their names": o objeto da posse
	// é o NOME, e um token com conceito próprio (test→run_tests) sairia
	// do residual e deixaria o slot name vazio. "spaces" fica de fora:
	// "spaces in their names" é o glob * *, do extrator namepat.
	for i, t := range out {
		if t.Filler || t.Slang {
			continue
		}
		isNameCue := t.has("nameattr") &&
			(t.Fold == "named" || t.Fold == "called" || t.Fold == "nomeado" ||
				t.Fold == "chamado" || t.Fold == "chamada" || t.Fold == "name" ||
				t.Fold == "nome")
		isContainCue := nameDomain &&
			(t.Fold == "contain" || t.Fold == "contains" || t.Fold == "containing" ||
				t.Fold == "have" || t.Fold == "has" || t.Fold == "tem" ||
				t.Fold == "contem" || t.Fold == "contendo")
		if !isNameCue && !isContainCue {
			continue
		}
	mark:
		for j := i + 1; j < len(out) && j <= i+4; j++ {
			u := &out[j]
			if u.Filler || u.Slang || u.has("article") || u.has("nameattr") ||
				u.has("content") || u.has("grepword") {
				continue // "the word/string 'x'" — meta-palavra é transparente
			}
			switch u.Fold {
			case "space", "spaces", "espaco", "espaço", "espacos", "espaços":
				continue // "spaces in their names" → namepat * *, não name
			case "in", "em", "no", "na", "with", "com", "that", "which",
				"to", "para", "and", "or", "e", "ou", "after", "apos",
				"após", "under", "by":
				break mark // "named after X" / "spaces IN their names"
			}
			if u.has("nametgt") || u.has("forquoted") || u.has("glob") ||
				u.has("outtgt") {
				break mark
			}
			u.Concs = append(u.Concs, "nametgt")
			break mark
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
	// hífen interno/trailler é nome composto ("master-", "my-dir") —
	// só não vale o hífen de flag na posição 0
	if strings.Contains(s[1:], "-") {
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
		if fixed == t.Fold {
			fixed = "" // "reparar" pattern→pattern não é typo: só devolveria o conceito que um passe anterior removeu de propósito
		}
		if fixed == "mkdir" && strings.HasSuffix(t.Fold, "dir") && t.Fold != "mkdir" {
			// mydir/somedir/workdir são nomes de pasta legítimos — um
			// sufixo 'dir' nunca é typo de 'mkdir' (o typo do verbo vem
			// antes do 'dir': mkidr, mdkir…)
			fixed = ""
		}
		if nuNoRepair[t.Fold] {
			fixed = "" // palavra inglesa real, não typo do vocab ('separated'→'separator' apaga o sentido de particípio)
		}
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

// nuFilesystemAt: "filesystem" ou o par "file system" — o filesystem
// inteiro (raiz), nunca uma pasta chamada "file"/"system"
func nuFilesystemAt(toks []nuTok, i int) bool {
	if toks[i].Fold == "filesystem" {
		return true
	}
	if (toks[i].Fold == "file" || toks[i].Fold == "files") &&
		i+1 < len(toks) &&
		(toks[i+1].Fold == "system" || toks[i+1].Fold == "sistema" ||
			toks[i+1].Fold == "systems") {
		return true
	}
	return false
}

// nuNumWord: "one"/"two"/"um"/"três" → 1/2/3 — palavra-número nos
// mesmos lugares onde um dígito vale ("one minute ago" ≈ "1 minute ago")
func nuNumWord(s string) (int, bool) {
	switch s {
	case "one", "um", "uma":
		return 1, true
	case "two", "dois", "duas":
		return 2, true
	case "three", "três", "tres":
		return 3, true
	case "four", "quatro":
		return 4, true
	case "five", "cinco":
		return 5, true
	case "six", "seis":
		return 6, true
	case "seven", "sete":
		return 7, true
	case "eight", "oito":
		return 8, true
	case "nine", "nove":
		return 9, true
	case "ten", "dez":
		return 10, true
	case "eleven", "onze":
		return 11, true
	case "twelve", "doze":
		return 12, true
	case "thirteen", "treze":
		return 13, true
	case "fourteen", "catorze", "quatorze":
		return 14, true
	case "fifteen", "quinze":
		return 15, true
	case "sixteen", "dezesseis", "dezasseis":
		return 16, true
	case "seventeen", "dezessete", "dezassete":
		return 17, true
	case "eighteen", "dezoito":
		return 18, true
	case "nineteen", "dezenove", "dezanove":
		return 19, true
	case "twenty", "vinte":
		return 20, true
	}
	return 0, false
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
				var alTaken map[int]bool
				in.Slots, in.Missing, in.Hint, _, alTaken = m.fill(&m.data.Intents[di], toks, hits, used, w)
				in.Dropped = nuDroppedOps(toks, alTaken, in.Slots)
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
		if strings.HasPrefix(m.data.Intents[choice].Name, dsLearnedPrefix) {
			// paráfrase de intenção aprendida é palpite, não veredito: nunca
			// auto-executa e cede a liderança ao melhor candidato
			// determinístico. O atalho exato (alias/shape, 0.97) não passa
			// por aqui — continua disparando normal.
			var allow []int
			for i, d := range m.data.Intents {
				if !strings.HasPrefix(d.Name, dsLearnedPrefix) {
					allow = append(allow, i)
				}
			}
			if c2, p2 := m.classify(canon, allow); c2 >= 0 {
				in.Alt = append([]string{m.data.Intents[choice].Name}, in.Alt...)
				in.Evidence = append(in.Evidence, fmt.Sprintf("aprendida %s por paráfrase cedeu a %s", m.data.Intents[choice].Name, m.data.Intents[c2].Name))
				choice, conf = c2, p2*0.75
			} else {
				conf *= 0.5 // nada determinístico perto: segue sugestão fraca
			}
		}
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
		// Empate é evidência de ambiguidade, não de confiança: p é a
		// probabilidade do classificador ENTRE os empatados — usar direto
		// (0.64 entre find×env_status não pode virar 0.84 e agir sozinho;
		// vira miss/menu com os empatados em in.Alt).
		conf = p
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
	var taken map[int]bool
	in.Slots, in.Missing, in.Hint, unfed, taken = m.fill(def, toks, hits, used, w)
	// namelen/namepat destilam pra glob no slot name — "names 33
	// characters long"→?×33, "config files"→*config*; são padrões de
	// nome, não argumentos separados. Só funde quando o intent TEM slot
	// name — text_grep lê namepat direto no método .each_name.
	for _, k := range []string{"namelen", "namepat"} {
		if v := in.Slots[k]; v != "" {
			if _, hasName := def.Slots["name"]; hasName {
				// meta-palavra de domínio ("letter", "name") cedida pelo
				// sponge não bloqueia o padrão real que o namepat achou
				cur := in.Slots["name"]
				generic := cur == "letter" || cur == "letters" ||
					cur == "name" || cur == "names" || cur == "filename" ||
					cur == "basename" || cur == "caractere" ||
					cur == "letra" || cur == "letras" ||
					// cue-vocábulo puro ("the word 'data'") — papel
					// sintático que introduz o padrão, nunca operando
					cur == "word" || cur == "words" || cur == "string" ||
					cur == "strings" || cur == "term" || cur == "termo" ||
					cur == "text" || cur == "texto" || cur == "phrase" ||
					cur == "frase" || cur == "pattern" || cur == "padrao" ||
					cur == "palavra" || cur == "palavras"
				if cur == "" || generic {
					in.Slots["name"] = v
				} else if strings.ContainsAny(v, "*?[") &&
					!strings.ContainsAny(cur, "*?[") {
					// o glob real vence a palavra nua — "where $V is a
					// variable" deixa '$version' solto; o padrão é o
					// '*$version*' com curinga. Palavra contida no
					// próprio glob é metainfo coberta, não drop.
					if !strings.Contains(strings.ToLower(v),
						strings.ToLower(cur)) {
						seen := false
						for _, d0 := range in.Dropped {
							if d0 == cur {
								seen = true
							}
						}
						if !seen {
							in.Dropped = append(in.Dropped, cur)
						}
					}
					in.Slots["name"] = v
				} else {
					// cola do sponge feita SÓ de vocábulo-meta
					// ("null character delimiter") não é nome de
					// arquivo — o glob real vence e a cola dropa
					phrasing := true
					for _, w0 := range strings.Fields(cur) {
						meta := false
						for i := range toks {
							if toks[i].Raw != w0 {
								continue
							}
							if toks[i].has("content") || toks[i].has("grepword") ||
								nuAllFormatWords(toks[i].Fold) {
								meta = true
							}
						}
						if !meta {
							phrasing = false
						}
					}
					if phrasing {
						// cue-vocábulo puro ("the word 'x'") substitui
						// calado — é papel sintático, não operando;
						// glue com payload ("null character delimiter")
						// segue pro Dropped — a restrição não é coberta
						pureCue := map[string]bool{
							"word": true, "words": true, "string": true,
							"strings": true, "term": true, "termo": true,
							"termos": true, "palavra": true, "palavras": true,
							"frase": true, "frases": true, "text": true,
							"texto": true, "textos": true, "name": true,
							"nome": true, "nomes": true, "the": true,
							"a": true, "de": true, "of": true, "do": true,
							"da": true, "o": true,
						}
						cueOnly := true
						for _, w0 := range strings.Fields(cur) {
							if !pureCue[w0] {
								cueOnly = false
							}
						}
						if !cueOnly {
							seen := false
							for _, d0 := range in.Dropped {
								if d0 == cur {
									seen = true
								}
							}
							if !seen {
								in.Dropped = append(in.Dropped, cur)
							}
						}
						in.Slots["name"] = v
					}
				}
				delete(in.Slots, k)
			}
		}
	}
	// nametgt = aspa reivindicada como predicado de nome ("names contain
	// 'log'", "word 'fstab' in their name") — o sponge para em "whose"
	// antes de chegar nela, então o valor vai direto pro slot:
	// name→literal (o script vira *log*); sem slot name, namepat→*x*.
	// Um name genérico ("word", "text", "string", "pattern", "path")
	// cedido pelo sponge é substituído — a aspa é o predicado real.
	_, hasName := def.Slots["name"]
	_, hasPat := def.Slots["namepat"]
	genericName := map[string]bool{
		"word": true, "words": true, "text": true, "texto": true,
		"string": true, "strings": true, "pattern": true, "padrao": true,
		"padrão": true, "name": true, "nome": true, "path": true,
		"filename": true, "term": true, "termo": true, "phrase": true,
		"frase": true, "line": true, "linha": true,
		// cue-verbs que introduzem o predicado ("containing the word
		// 'x'") — o verbo em si nunca é o nome buscado
		"containing": true, "contains": true, "contain": true,
		"contem": true, "contém": true, "contendo": true,
		"tem": true, "have": true, "has": true,
	}
	for i := range toks {
		if !toks[i].has("nametgt") || toks[i].has("outtgt") {
			continue // outtgt vence: "a file named 'rm'" é destino de saída
		}
		// qual cue marcou a aspa: "names CONTAIN 'log'" é substring
		// (namepat=*log*); "named/called 'file'" é basename exato (name)
		contains := false
		for j := i - 1; j >= 0 && j >= i-4; j-- {
			switch toks[j].Fold {
			case "contain", "contains", "containing", "contem", "contém",
				"contendo", "have", "has", "tem":
				contains = true
			case "named", "called", "nomeado", "nomeada", "chamado", "chamada":
				contains = false
			case "name", "names", "filename", "nome", "nomes":
				continue // atravessa o substantivo-domínio até o verbo
			default:
				if !toks[j].Filler && !toks[j].has("article") {
					goto cueDone // outra palavra fecha a janela
				}
			}
		}
	cueDone:
		if contains && hasPat && in.Slots["namepat"] == "" {
			in.Slots["namepat"] = "*" + toks[i].Raw + "*"
			if genericName[in.Slots["name"]] {
				delete(in.Slots, "name") // "in their path names" — meta-palavra não é nome
			}
		} else if contains && !hasPat && hasName {
			in.Slots["name"] = "*" + toks[i].Raw + "*"
		} else if hasName && (in.Slots["name"] == "" || genericName[in.Slots["name"]]) {
			in.Slots["name"] = toks[i].Raw
		} else if !hasName && hasPat && in.Slots["namepat"] == "" {
			in.Slots["namepat"] = "*" + toks[i].Raw + "*"
		}
		break
	}
	// name == name_re: o mesmo literal alimentou os dois slots ("names
	// matching 'a+'"). Como glob, "a+" casa só o literal 'a+' — junto com
	// o regex restringiria demais. O regex é o predicado real.
	if re := in.Slots["name_re"]; re != "" && in.Slots["name"] == re &&
		!strings.ContainsAny(re, "*?[") {
		delete(in.Slots, "name")
	}
	// "under 'Books' directory tree located in user's home" — o dir
	// relativo qualificado por cláusula de home do usuário resolve
	// composto "~/Books"; solto, procuraria "Books" na raiz
	if d := in.Slots["dir"]; d != "" && !strings.HasPrefix(d, "/") &&
		!strings.HasPrefix(d, "~") && !strings.HasPrefix(d, ".") &&
		!strings.HasPrefix(d, "$") && !strings.ContainsAny(d, "/\\") {
		for i, t := range toks {
			if t.Fold != "home" && t.Fold != "casa" {
				continue
			}
			ownerNear := false
			for k := i - 1; k >= 0 && k >= i-3; k-- {
				u := &toks[k]
				if u.Filler || u.Slang || u.Fold == "s" || u.Raw == "'s" {
					continue
				}
				switch u.Fold {
				case "user", "usuario", "usuário", "owner", "dono":
					ownerNear = true
				}
				break
			}
			if ownerNear {
				in.Slots["dir"] = "~/" + d
				break
			}
		}
	}
	// "symlinks pointing to anywhere within /mnt" — restrição por ALVO
	// do link não é predicado suportado; dropar calado acharia TODOS
	// os links (superset fingindo sucesso). O operando vai pro Dropped.
	if in.Slots["type"] == "link" {
		for i, t := range toks {
			if t.Fold != "pointing" && t.Fold != "targeting" &&
				t.Fold != "aiming" && t.Fold != "apontando" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "to", "para", "at", "em", "pro", "pra":
					continue
				}
				if !nuInExceptClause(toks, j) {
					seen := false
					for _, d0 := range in.Dropped {
						if d0 == u.Raw {
							seen = true
						}
					}
					if !seen {
						in.Dropped = append(in.Dropped, u.Raw)
					}
				}
				break
			}
		}
	}
	// invariante de exceção: token dentro de cláusula "except/without/…"
	// só alimenta o slot exclude — qualquer outro slot que tenha
	// engolido o operando ("excluding paths containing 'a'" → pattern=a,
	// "except in tmp" → dir=tmp, "except those named foo" → name=foo)
	// perde o valor; o operando volta pro exclude — ou vira Dropped
	// quando o intent nem tem o slot. O pedido fica honestamente
	// incompleto, nunca verde com exclusão ignorada.
	// EXCEÇÃO da invariante: slots de NEGAÇÃO/limite existem exatamente
	// pro operando atrás do cue — "files not owned by wnj" (not_owner),
	// "without 777 permission" (not_mode), "pelo menos 5 linhas"
	// (min_lines). Aí o cue não exclui um conjunto, é a própria
	// semântica do slot; tirar o valor apagaria o pedido inteiro.
	deletedRaw := map[string]bool{}
	for i := range toks {
		if !nuInExceptClause(toks, i) || toks[i].Raw == "" {
			continue
		}
		raw := toks[i].Raw
		for name, sv := range in.Slots {
			if name == "exclude" || strings.HasPrefix(name, "not_") ||
				strings.HasPrefix(name, "no_") || strings.HasPrefix(name, "min_") ||
				strings.HasPrefix(name, "max_") ||
				name == "nx" || name == "nww" || name == "orphan" ||
				name == "newer" || name == "older" {
				continue
			}
			// o token alimentou o slot: direto ("docs/x"→dir), embrulhado
			// ("a"→namepat "*a*"), ou ext que trima o ponto (".git"→"git")
			if sv != "" && (strings.Contains(sv, raw) || sv == strings.TrimLeft(raw, ".")) {
				delete(in.Slots, name)
				deletedRaw[raw] = true
			}
		}
	}
	// todo operando da cláusula precisa pousar em exclude (intent com
	// o slot) ou em Dropped (sem o slot → unverified). Match exato por
	// item — "a" não pode fingir que já está em "alpha|beta".
	_, hasExclude := def.Slots["exclude"]
	_, ops, _ := nuExceptOperands(toks, nil)
	opSet := map[int]bool{}
	drop := func(r string) {
		for _, d := range in.Dropped {
			if d == r {
				return
			}
		}
		if os.Getenv("NUDBG") != "" {
			fmt.Println("DBG drop:", r)
		}
		in.Dropped = append(in.Dropped, r)
	}
	for _, j := range ops {
		opSet[j] = true
		r := nuExcludeRaw(&toks[j])
		if r == "" {
			continue
		}
		if !hasExclude {
			drop(r)
			continue
		}
		seen := false
		for _, p := range strings.Split(in.Slots["exclude"], "|") {
			if p == r {
				seen = true
				break
			}
		}
		if !seen {
			if in.Slots["exclude"] == "" {
				in.Slots["exclude"] = r
			} else {
				in.Slots["exclude"] += "|" + r
			}
		}
	}
	// operando varrido de um slot que não coube no exclude (newest→sort,
	// filtro de tempo/dono — não vira glob): não pode sumir em silêncio.
	for i := range toks {
		if opSet[i] || !nuInExceptClause(toks, i) || toks[i].Raw == "" {
			continue
		}
		if deletedRaw[toks[i].Raw] {
			drop(nuExcludeRaw(&toks[i]))
		}
	}
	for _, r := range nuDroppedOps(toks, taken, in.Slots) {
		drop(r)
	}
	// "with group owner X" / "belonging to root": X é predicado de ORIGEM;
	// como alvo do chgrp/chown seria no-op (mesmo grupo/dono) e esconderia
	// que o destino real ficou vazio.
	if in.Slots["src_group"] != "" && in.Slots["group"] == in.Slots["src_group"] {
		delete(in.Slots, "group")
	}
	if in.Slots["src_owner"] != "" && in.Slots["owner"] == in.Slots["src_owner"] {
		delete(in.Slots, "owner")
	}
	// "to www-data:www-data" — a forma user:group alimenta OS DOIS
	// slots; deixar o par colado num slot só quebraria o lookup do
	// chown ("www-data:www-data" não é usuário)
	if in.Name == "perm_set" {
		split := func(k, other string) {
			v := in.Slots[k]
			a, b, ok := strings.Cut(v, ":")
			if !ok || a == "" || b == "" || strings.Contains(b, ":") {
				return
			}
			in.Slots[k] = a
			if in.Slots[other] == "" || in.Slots[other] == v {
				in.Slots[other] = b
			}
		}
		split("owner", "group")
		split("group", "owner")
	}
	if in.Name == "rename" || in.Name == "file_move" {
		// "rename all files to lowerase" — a transformação de caixa
		// não é nome de destino; "new:$Name" é expansão de var que
		// o runner não resolve — vira literal errado no disco
		for _, k := range []string{"to", "new", "old", "prefix", "to_ext",
			"target", "name", "dest"} {
			v := in.Slots[k]
			if v == "" {
				continue
			}
			// var de shell só é operando em path (dir:$DIR é aceito);
			// em NOME novo/antigo é expansão que o runner não faz
			bad := (strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%")) &&
				k != "dest" && k != "dir" && k != "srcdir"
			switch strings.ToLower(v) {
			case "lowercase", "lowerase", "lower", "uppercase",
				"upper", "ucase", "lcase", "titlecase", "title-case",
				"capitalize", "capitalized", "camelcase", "snakecase",
				"kebab-case", "minúsculas", "minusculas", "maiusculas",
				"maiúsculas", "minuscula", "minúscula", "maiuscula",
				"maiúscula":
				bad = true
			}
			if bad {
				delete(in.Slots, k)
				drop(v)
			}
		}
		// "rename ... if the name doesn't contain X" — a condição
		// colada no padrão é uma cláusula não suportada
		if v := in.Slots["namepat"]; strings.HasPrefix(v, "if") ||
			strings.HasPrefix(v, "when") || strings.HasPrefix(v, "se ") {
			for i, t := range toks {
				if t.Raw != v && t.Fold != v {
					continue
				}
				for j := i + 1; j < len(toks) && j <= i+5; j++ {
					switch toks[j].Fold {
					case "doesn't", "doesnt", "doesn`t", "not", "não",
						"nao", "unless", "contain", "contains",
						"already", "ja", "já":
						delete(in.Slots, "namepat")
						drop(v)
						j = len(toks)
					}
				}
				break
			}
		}
	}
	if in.Name == "delete_file" {
		// "Remove newline characters from file.txt" — o removido é
		// uma classe de caractere (text-strip), não o arquivo. Sem
		// o guard o alvo virava delete de verdade.
		for i, t := range toks {
			switch t.Fold {
			case "remove", "remova", "remover", "delete", "apaga",
				"apague", "strip", "erase", "elimina", "eliminate",
				"tira", "retire", "squeeze", "deleta":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "newline", "newlines", "linebreaks", "line-breaks",
					"carriage", "whitespace", "tabs", "tab",
					"characters", "character", "caracteres",
					"caractere", "blank", "blanks", "espacos",
					"espaços", "quebra", "quebras", "trailing",
					"leading":
					drop(u.Raw)
					delete(in.Slots, "target")
				}
				break
			}
		}
	}
	if in.Name == "link_create" {
		for _, k := range []string{"target", "link", "dest"} {
			v := in.Slots[k]
			if v == "" {
				continue
			}
			bad := strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%")
			// "change the timestamp of symlink X to date/time" — o
			// pedido é touch -h, não ln; o vocábulo de tempo não é
			// um arquivo-alvo ("date/time", "current date")
			for _, p := range strings.FieldsFunc(strings.ToLower(v),
				func(r rune) bool { return r == ' ' || r == '/' }) {
				switch p {
				case "date", "time", "timestamp", "datetime",
					"hora", "current", "atual":
					bad = true
				}
			}
			if bad {
				delete(in.Slots, k)
				drop(v)
			}
		}
	}
	if in.Name == "file_move" {
		// "files with '.x' extension" — o target ponteado é filtro
		// de sufixo, não arquivo único; funde no pat glob
		if v := in.Slots["target"]; strings.HasPrefix(v, ".") &&
			len(v) > 1 && !strings.Contains(v, "/") &&
			in.Slots["pat"] == "*" {
			extCue := false
			for i, t := range toks {
				if t.Raw != v {
					continue
				}
				for j := i - 1; j >= 0 && j >= i-4; j-- {
					u := &toks[j]
					cue := u.Fold == "extension" || u.Fold == "ext" ||
						u.Fold == "suffix" || u.Fold == "sufixo" ||
						u.Fold == "with" || u.Fold == "com"
					if u.Filler && !cue {
						continue
					}
					if cue {
						extCue = true
					}
					break
				}
				break
			}
			if extCue {
				in.Slots["pat"] = "*" + v
				delete(in.Slots, "target")
			}
		}
	}
	// name/dir/exclude formado só de vocábulo temporal quando já há filtro
	// de tempo é leitura roubada da janela ("in March 2007"→dir:March,
	// "an hour ago"→name:hour, "those modified today"→exclude:modified):
	// vai pro Dropped em vez de virar nome de arquivo de mentira.
	if in.Slots["newer"] != "" || in.Slots["older"] != "" ||
		in.Slots["newer_date"] != "" || in.Slots["older_date"] != "" ||
		in.Slots["tfield"] != "" {
		for _, k := range []string{"name", "dir", "target", "exclude"} {
			v := in.Slots[k]
			if v == "" || !nuAllTemporal(v) {
				continue
			}
			delete(in.Slots, k)
			// o token já foi consumido pelo bound/banda ("March" em
			// "in March 2007") — é phrasing, não operando órfão
			swallowed := false
			for _, p := range strings.FieldsFunc(v, func(r rune) bool {
				return r == ' ' || r == '|'
			}) {
				for i := range toks {
					if taken[i] && (toks[i].Raw == p ||
						strings.EqualFold(toks[i].Fold, p)) {
						swallowed = true
					}
				}
			}
			if !swallowed {
				drop(v)
			}
		}
	}
	// "find files and delete them" — 'them' é anáfora do conjunto
	// achado: quando NENHUM slot material especifica o objeto da
	// mutação (só type/manner sobraram), o pronome é o único operando
	// — e está não-resolvido. Cai no Dropped em vez de fingir que a
	// frase traz alvo (mass-delete sem filtro seria falso sucesso).
	// "remove them" com name/dir/mass já especificado continua phrasing.
	if in.Name == "delete_file" || in.Name == "file_move" ||
		in.Name == "link_create" || in.Name == "rename" ||
		in.Name == "file_split" {
		objectish := false
		for k := range in.Slots {
			switch k {
			case "type", "icase", "follow", "sort", "limit",
				"include_hidden", "exclude", "op":
			default:
				objectish = true
			}
		}
		if !objectish {
			for _, t := range toks {
				switch t.Fold {
				case "them", "it", "eles", "elas", "deles", "delas":
					seen := false
					for _, d0 := range in.Dropped {
						if d0 == t.Raw {
							seen = true
						}
					}
					if !seen {
						in.Dropped = append(in.Dropped, t.Raw)
					}
				}
			}
		}
	}
	// "count duplicate lines" — wordcount_stats conta TODAS as linhas;
	// o qualificador dup/unique não é suportado por nenhum método. Vai
	// pro Dropped em vez de contar errado em silêncio.
	if in.Name == "wordcount_stats" || in.Name == "text_count" {
		for _, t := range toks {
			switch t.Fold {
			case "duplicate", "duplicates", "dup", "dups", "repeated",
				"unique", "uniques", "duplicada", "duplicadas",
				"duplicado", "duplicados", "repetida", "repetidas",
				"repetido", "repetidos":
				in.Dropped = append(in.Dropped, t.Raw)
			}
		}
	}
	// "user $1 belongs to group $2" / "membro do grupo" — pertencimento
	// de usuário a grupo NÃO é consulta de permissão de arquivo; o
	// perm_find que casou responderia perms de arquivo no lugar de
	// membership. Dropa o verbo de membership e o operando de grupo.
	for i, t := range toks {
		switch t.Fold {
		case "belong", "belongs", "belonging", "member", "members",
			"membro", "membros", "pertence", "pertencem", "pertencer":
			for k := i + 1; k < len(toks) && k <= i+5; k++ {
				u := &toks[k]
				if u.Fold == "group" || u.Fold == "grupo" {
					in.Dropped = append(in.Dropped, t.Raw, u.Raw)
					if k+1 < len(toks) && strings.HasPrefix(toks[k+1].Raw, "$") {
						in.Dropped = append(in.Dropped, toks[k+1].Raw)
					}
					break
				}
			}
		}
	}
	// "loop over the results of a find" / "for each" — controle de
	// fluxo não é intenção suportada; a query colada não é operando.
	// Aspas são conteúdo: "contain the string 'loop'" busca 'loop';
	// "named loop" também é literal (nametgt já reivindicou).
	for _, t := range toks {
		if t.Quoted || t.has("nametgt") {
			continue
		}
		switch t.Fold {
		case "loop", "iterate", "iterating", "foreach":
			if q := in.Slots["query"]; q != "" {
				in.Dropped = append(in.Dropped, q)
				delete(in.Slots, "query")
			}
			in.Dropped = append(in.Dropped, t.Raw)
		}
	}
	// "all but 5 newest ones" — keep-N-newest é semântica de exceção
	// temporal que nenhum slot expressa; o superlativo vai pro Dropped
	// em vez de virar limit+sort numa busca qualquer
	for i, t := range toks {
		switch t.Fold {
		case "newest", "oldest", "latest":
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				u := &toks[j]
				if u.Filler || nuIsNumber(u.Fold) {
					continue // "but 5 newest" — o número atravessa
				}
				if nuExceptCues[u.Fold] || u.Fold == "but" ||
					u.Fold == "save" || u.Fold == "keep" ||
					u.Fold == "keeping" || u.Fold == "exceto" ||
					u.Fold == "menos" {
					in.Dropped = append(in.Dropped, u.Raw, t.Raw)
				}
				break
			}
		}
	}
	// "with null character as the delimiter" / "separated by null" —
	// formato de SAÍDA (print0/delimitador) não é suportado por
	// nenhum método find; a cláusula vai pro Dropped em vez de vazar
	// pro sponge como name
	if in.Name == "file_find" || in.Name == "perm_find" {
		delimAt := -1
		for i, t := range toks {
			switch t.Fold {
			case "delimiter", "delimitador", "separator", "separador",
				"print0":
				delimAt = i
			}
		}
		if delimAt >= 0 {
			for j := delimAt - 4; j <= delimAt; j++ {
				if j < 0 || j >= len(toks) {
					continue
				}
				switch toks[j].Fold {
				case "null", "character", "caractere", "caracter",
					"newline", "newlines", "zero", "nulo", "nula":
					drop(toks[j].Raw)
				}
			}
			drop(toks[delimAt].Raw)
		}
	}
	// "files named count/time" — a palavra depois do cue named/called
	// é o basename literal mesmo quando carrega conceito de ação; o
	// sponge a rejeita por conceito, então o cue a reivindica aqui
	if in.Slots["name"] == "" {
		if _, has := def.Slots["name"]; has {
			for i, t := range toks {
				switch t.Fold {
				case "named", "called", "nomeado", "nomeada",
					"chamado", "chamada", "denominado", "denominada":
				default:
					continue
				}
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					u := &toks[j]
					if u.Filler || u.Slang || taken[j] {
						continue
					}
					if u.has("forquoted") || u.has("outtgt") {
						break // a aspa já tem papel semântico próprio
					}
					in.Slots["name"] = u.Raw
					taken[j] = true
					break
				}
				break
			}
		}
	}
	// "Create thumbnails ... under /path/to/dir" — o path depois de
	// cue de escopo não é o objeto criado; um target que só existe
	// como escopo vira Dropped em vez de alucinar o arquivo-alvo
	if in.Name == "create" || in.Name == "link_create" {
		for _, sk := range []string{"target", "link"} {
			v := in.Slots[sk]
			if v == "" {
				continue
			}
			for i, t := range toks {
				if t.Raw != v && t.Fold != v {
					continue
				}
				// o cue de escopo pode estar marcado filler
				// ("under"/"em" são preposição comum) — o fold
				// bruto decide, não o status
				for p := i - 1; p >= 0 && p >= i-2; p-- {
					switch toks[p].Fold {
					case "under", "in", "inside", "within", "em",
						"no", "na", "nos", "nas", "dentro":
						delete(in.Slots, sk)
						drop(v)
					}
				}
			}
		}
	}
	// "find ... and execute/run X for each of them" — o fan-out por
	// arquivo encontrado é uma SEGUNDA operação que o find não faz;
	// o verbo e o quantificador vão pro Dropped em vez de fingir
	// que a listagem executa algo
	if in.Name == "file_find" || in.Name == "perm_find" ||
		in.Name == "dir_diff" {
		execVerb, fanout := false, false
		for _, t := range toks {
			switch t.Fold {
			case "execute", "executes", "executar", "executa",
				"run", "rodar", "roda", "rode", "xargs",
				"command", "comando":
				execVerb = true
			case "process", "processar":
				if fanout {
					execVerb = true
				}
			case "each", "every", "cada", "them", "eles", "elas":
				fanout = true
			}
		}
		if execVerb && fanout {
			for _, t := range toks {
				switch t.Fold {
				case "execute", "executes", "executar", "executa",
					"run", "rodar", "roda", "rode",
					"xargs", "process", "processar",
					"command", "comando", "mv", "cp",
					"each", "every", "cada", "them", "eles", "elas":
					drop(t.Raw)
				}
			}
		}
	}
	// "find ... and sort them" — ordenar sem campo é segunda operação
	// que o find não expressa; o verbo solto dropa em vez de fingir
	// que a listagem sai ordenada
	if (in.Name == "file_find" || in.Name == "perm_find" ||
		in.Name == "dir_diff") && in.Slots["sort"] == "" {
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "sort", "sorted", "sorting", "ordena", "ordenar",
				"ordenado", "ordenada", "ordenados":
				drop(t.Raw)
			}
		}
	}
	// "print their path names separated by zeroes" — formato de saída
	// (print0/delimitador) não é suportado; a cláusula dropa em vez
	// de colar no slot name como se fosse predicado
	if in.Name == "file_find" || in.Name == "perm_find" ||
		in.Name == "dir_diff" {
		for i, t := range toks {
			switch t.Fold {
			case "separated", "delimited", "terminated", "separados",
				"separadas", "separado", "separada", "delimitados":
				drop(t.Raw)
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					if u.Filler || u.Fold == "by" || u.Fold == "por" ||
						u.Fold == "com" || u.Fold == "with" {
						continue
					}
					switch u.Fold {
					case "zero", "zeroes", "zeros", "null", "nul",
						"newline", "newlines", "space", "spaces",
						"comma", "commas", "tab", "tabs", "nulo",
						"nulos", "nula", "nulas", "\\0":
						drop(u.Raw)
					}
					break
				}
			}
		}
	}
	// text_replace: o slot file só vale nome file-shaped ou aspa de
	// escopo ("in 'x.txt'"); palavra-conteúdo ("IP", "NEEDLE") é
	// operando do from/to, não arquivo. E descriptor computado
	// ("the system IP address") como `to` não é literal — sed não
	// avalia hostname; vai pro Dropped em vez de inserir o texto cru.
	if in.Name == "text_replace" {
		// o verbo real do pedido não é replace: "Run mycommand" é
		// execução de comando — sed -i 's/mycommand//' em /tmp
		// mutaria arquivos por engano
		hasRun, hasReplace := false, false
		for i, t := range toks {
			switch t.Fold {
			case "run", "runs", "ran", "execute", "executes",
				"executed", "executing", "exec", "rodar", "roda",
				"rode", "executa", "corre", "corra", "launch",
				"launches", "start", "starts", "invoke",
				"invokes", "call", "calls":
				hasRun = true
			case "change", "changes", "changed", "changing",
				"mudar", "muda", "mude":
				// "change directory/shell/cwd" é navegação,
				// não replace de texto
				cd := false
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					u := &toks[j]
					if u.Filler {
						continue
					}
					switch u.Fold {
					case "directory", "dir", "folder", "pasta",
						"diretorio", "diretório", "shell",
						"cwd", "location", "path":
						cd = true
					}
					break
				}
				if !cd {
					hasReplace = true
				}
			case "replace", "replaces", "replaced", "replacing",
				"substitui", "substitua", "substituir", "troca",
				"trocar", "troque", "alter", "alters", "altera",
				"altere", "swap", "swaps", "override",
				"overwrite", "substitute", "substituting":
				hasReplace = true
			}
		}
		if hasRun && !hasReplace {
			for _, t := range toks {
				switch t.Fold {
				case "run", "execute", "exec", "rodar", "roda",
					"executa", "corre", "launch", "start",
					"invoke", "call":
					drop(t.Raw)
				}
			}
		}
		// "change directory to X" — cd não é operação de texto
		for i, t := range toks {
			if (t.Fold == "change" || t.Fold == "muda" ||
				t.Fold == "mude" || t.Fold == "switch") &&
				i+1 < len(toks) {
				switch toks[i+1].Fold {
				case "directory", "dir", "folder", "diretorio",
					"diretório", "pasta":
					drop(t.Raw)
					drop(toks[i+1].Raw)
				}
			}
		}
		// "in every config" = todo arquivo de configuração, não a
		// pasta 'config' — o sed varreria o dir errado
		if dv := in.Slots["dir"]; dv == "config" ||
			dv == "configs" || dv == "configuration" {
			for _, t := range toks {
				switch t.Fold {
				case "every", "each", "all", "any", "cada",
					"todo", "toda", "todos", "todas":
					delete(in.Slots, "dir")
					drop(dv)
				}
			}
		}
		// classe de caractere não é literal — "non-punctuation"
		// descreve um conjunto que o replace não expressa
		for _, sk := range []string{"from", "to", "new", "old"} {
			v := in.Slots[sk]
			switch strings.ToLower(v) {
			case "punctuation", "non-punctuation", "alphanumeric",
				"alphanumerics", "digit", "digits", "numeric",
				"numerics", "uppercase", "lowercase",
				"whitespace", "non-whitespace", "printable",
				"non-printable", "alpha", "alphabetic", "vowel",
				"vowels", "consonant", "consonants",
				"character", "characters", "chars":
				delete(in.Slots, sk)
				drop(v)
			}
		}
		// aspa de escopo sem forma de pasta é o ARQUIVO-alvo — 'a,b'
		// em `in "a,b"` é onde o sed opera, não um diretório
		if dv := in.Slots["dir"]; dv != "" && in.Slots["file"] == "" {
			dirish := strings.HasPrefix(dv, "/") ||
				strings.HasPrefix(dv, "~") ||
				strings.HasPrefix(dv, ".") ||
				strings.HasSuffix(dv, "/") || dv == ".."
			quoted := false
			for _, t := range toks {
				if t.Quoted && t.Raw == dv {
					quoted = true
				}
			}
			if quoted && !dirish {
				delete(in.Slots, "dir")
				in.Slots["file"] = dv
			}
		}
		if fv := in.Slots["file"]; fv != "" {
			fileish := strings.ContainsAny(fv, "./\\") ||
				strings.HasSuffix(fv, "/") || nuFileShaped(fv)
			if !fileish {
				// aspado é nome explícito — 'a,b' é arquivo literal
				for _, t := range toks {
					if t.Quoted && t.Raw == fv {
						fileish = true
					}
				}
			}
			if !fileish {
				// aspa de escopo livre é o arquivo real
				target := ""
				for i, t := range toks {
					if !t.Quoted {
						continue
					}
					for j := i - 1; j >= 0 && j >= i-3; j-- {
						switch toks[j].Fold {
						case "in", "em", "no", "na", "inside", "of",
							"de", "do", "da", "under", "within":
							target = t.Raw
						}
						if target != "" || !toks[j].Filler {
							break
						}
					}
					if target != "" {
						break
					}
				}
				delete(in.Slots, "file")
				if target != "" {
					in.Slots["file"] = target
					// a aspa de escopo pode ter virado dir antes —
					// o alvo do sed é arquivo, não pasta
					if in.Slots["dir"] == target {
						delete(in.Slots, "dir")
					}
					drop(fv)
				} else if in.Slots["from"] == fv {
					// já é o texto velho — o 'file' era a mesma
					// palavra relida
					_ = fv
				} else if in.Slots["from"] == "" {
					in.Slots["from"] = fv
				} else {
					drop(fv)
				}
			}
		}
		// "files with '.php, '.xml'..." — a vírgula de sobra dentro
		// do grupo regex ('php,|xml') é mangle da fonte: casa ".php,"
		// literal e nunca bate. O exts compilado limpo
		// ('\.(php|xml|phtml)$') segue; lista-vírgula pura também.
		for _, k := range []string{"ext", "exts"} {
			v := in.Slots[k]
			mangled := strings.ContainsAny(v, ",;") &&
				strings.ContainsAny(v, "()[]{}^$\\")
			if k == "ext" && strings.ContainsAny(v, "()[]{}^$\\") {
				mangled = true // ext é lista-vírgula pura, sem meta
			}
			if v != "" && mangled {
				delete(in.Slots, k)
				drop(v)
			}
		}
		// "file:.php," — resto de lista de extensão colado no file
		if v := in.Slots["file"]; strings.HasSuffix(v, ",") ||
			strings.HasSuffix(v, ";") {
			delete(in.Slots, "file")
			drop(v)
		}
		// "replace X with a tab" — o artigo grudou no literal;
		// "a tab"→\t como os demais literais de palavra
		for _, k := range []string{"to", "from"} {
			v := in.Slots[k]
			f := strings.Fields(strings.ToLower(v))
			if len(f) == 2 && (f[0] == "a" || f[0] == "an" ||
				f[0] == "the" || f[0] == "um" || f[0] == "uma") {
				switch f[1] {
				case "tab", "tabs", "space", "spaces", "newline",
					"newlines", "underscore", "underscores", "comma",
					"pipe", "slash", "dash", "blank", "blanks",
					"carriage", "semicolon", "colon", "quote",
					"espaco", "espaço", "virgula", "vírgula",
					"sublinhado", "traco", "traço":
					in.Slots[k] = f[1]
				}
			}
		}
		// "substitui localhost por X em todos os configs" — a classe
		// do arquivo (configs/settings/dotfiles) é ESCOPO que o
		// método não filtra; sem ela o sed rodava contra tudo
		for i, t := range toks {
			confScope := t.has("config") || t.Fold == "configs" ||
				t.Fold == "conf" || t.Fold == "settings" ||
				t.Fold == "preferences" || t.Fold == "preferências" ||
				t.Fold == "dotfiles"
			if !confScope || taken[i] {
				continue
			}
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				u := &toks[j]
				if u.Filler && u.Fold != "em" && u.Fold != "in" &&
					u.Fold != "no" && u.Fold != "na" &&
					u.Fold != "nos" && u.Fold != "nas" {
					continue
				}
				switch u.Fold {
				case "em", "in", "no", "na", "nos", "nas", "todos",
					"todas", "all", "every", "across", "under",
					"within", "inside":
					drop(t.Raw)
				}
				break
			}
		}
		// "from" que sobrou sendo cue-vocábulo: valor real é a
		// palavra seguinte livre
		if in.Slots["from"] != "" {
			switch in.Slots["from"] {
			case "word", "string", "term", "text", "texto",
				"palavra", "termo":
				for i := range toks {
					if taken[i] || toks[i].Filler || toks[i].Quoted {
						continue
					}
					f := toks[i].Fold
					if f == "word" || f == "string" || f == "term" ||
						f == "text" || f == "texto" || f == "the" ||
						f == "a" || f == "an" {
						continue
					}
					in.Slots["from"] = toks[i].Raw
					break
				}
			}
		}
		// `to`/`from` com descriptor computado não é texto literal
		for _, k := range []string{"to", "from"} {
			v := in.Slots[k]
			if v == "" {
				continue
			}
			desc := strings.Contains(v, "://") ||
				strings.HasPrefix(v, "www.")
			// "regular/normal files" como literal — substantivos-
			// classe não são texto a substituir
			if strings.Contains(v, "/") && !strings.Contains(v, "://") {
				allClass := true
				for _, p := range strings.Split(v, "/") {
					switch strings.ToLower(strings.TrimSpace(p)) {
					case "file", "files", "regular", "normal", "plain",
						"text", "empty", "directory", "directories",
						"folder", "folders", "link", "links", "type",
						"standard", "ordinary", "hidden":
					default:
						allClass = false
					}
				}
				if allClass {
					desc = true
				}
			}
			words := strings.Fields(strings.ToLower(v))
			// cláusula inteira não-aspada não é literal — "replacing
			// all consecutive space characters a single newline" é a
			// DESCRIÇÃO da transformação, não o texto novo
			wasQuoted := false
			for _, t := range toks {
				if t.Quoted && t.Raw == v {
					wasQuoted = true
				}
			}
			if len(words) >= 5 && !wasQuoted {
				desc = true
			}
			for _, f := range words {
				switch f {
				case "system", "ip", "address", "hostname", "host",
					"date", "time", "random", "timestamp", "endereço",
					"endereco", "data", "hora",
					"replacing", "replace", "substituting",
					"substitute", "substituindo", "substituir":
					desc = true
				}
			}
			if desc {
				delete(in.Slots, k)
				drop(v)
				continue
			}
			// literal + cauda de escopo colada — "underscores all
			// files"/"newlines from standard input": o literal é a
			// cabeça, o resto é o escopo que o extrator engoliu. Só
			// corta valor NÃO-aspado (aspado pode ter 'and/the' real)
			// e só em palavra-classe forte de escopo.
			if !wasQuoted {
				cut := -1
				for i, f := range words {
					if i == 0 {
						continue
					}
					switch f {
					case "files", "file", "directories", "directory",
						"folders", "standard", "input", "tree",
						"stdin", "stdout":
						cut = i
					}
					if cut >= 0 {
						break
					}
				}
				if cut > 0 {
					hws := words[:cut]
					// a cabeça não pode terminar em prep/artigo —
					// "underscores all files" corta em files, e o
					// 'all' pendurado pertence à cauda
					for len(hws) > 0 {
						switch hws[len(hws)-1] {
						case "all", "for", "in", "the", "a", "an",
							"of", "to", "from", "with", "and", "or",
							"em", "de", "do", "da", "para", "com":
							hws = hws[:len(hws)-1]
						default:
							goto headDone
						}
					}
				headDone:
					head := strings.Join(hws, " ")
					if head != "" {
						in.Slots[k] = head
						// cauda de phrasing já coberta por mass/dir/
						// stdin não é operando — não vai pro Dropped
						covered := in.Slots["mass"] != "" ||
							in.Slots["dir"] != "" ||
							in.Slots["stdin"] != "" ||
							in.Slots["namefile"] != ""
						if !covered {
							drop(strings.Join(words[cut:], " "))
						}
					} else {
						delete(in.Slots, k)
						drop(v)
					}
				}
			}
		}
		// "replaces occurrences of X with file '/a/b'" — troca de
		// arquivo-por-arquivo é cp/mv, não sed. Cue 'file' antes do
		// to-aspado + to com cara de path → operação não suportada.
		if tv := in.Slots["to"]; tv != "" && strings.Contains(tv, "/") {
			for i, t := range toks {
				if !t.Quoted || t.Raw != tv {
					continue
				}
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					u := &toks[j]
					if u.Fold == "file" || u.Fold == "arquivo" {
						delete(in.Slots, "to")
						drop(tv)
						if fv2 := in.Slots["from"]; fv2 != "" {
							delete(in.Slots, "from")
							drop(fv2)
						}
					}
					if !u.Filler {
						break
					}
				}
			}
		}
		// 'file' com escapes literais ou comando dentro é ENTRADA de
		// texto (a transformação roda na string, não num arquivo):
		// "Testing\r\nTested" / "seq 1 10000" não são path.
		if fv := in.Slots["file"]; fv != "" {
			inputText := strings.Contains(fv, `\`) ||
				strings.Contains(fv, " ") &&
					!strings.ContainsAny(fv, "/~")
			if inputText {
				cmdish := false
				fw := strings.ToLower(strings.Fields(fv + " x")[0])
				for _, c := range []string{"seq", "echo", "cat", "ls",
					"find", "grep", "head", "tail", "sort", "wc",
					"printf", "print", "date", "who", "ps", "tr"} {
					if fw == c {
						cmdish = true
					}
				}
				if strings.Contains(fv, `\`) || cmdish {
					delete(in.Slots, "file")
					drop(fv)
				}
			}
		}
		// 'file' que é pontuação solta nunca é nome ('.', '..',
		// '~' são escopo, não arquivo)
		if fv := in.Slots["file"]; fv != "" && fv != "." && fv != ".." &&
			fv != "~" &&
			strings.IndexFunc(fv, unicode.IsLetter) < 0 &&
			strings.IndexFunc(fv, unicode.IsDigit) < 0 {
			delete(in.Slots, "file")
			drop(fv)
		}
		// dir que é palavra-descritor do efeito ("underscores",
		// "newlines") é a palavra do 'to' relida como pasta — a
		// convenção de literal ('spaces'→' ', 'underscores'→'_')
		// vale em from/to, nunca como diretório. Não é operando:
		// o 'to' real já cobre — some quieto, sem Dropped.
		if dv := in.Slots["dir"]; dv != "" {
			switch strings.ToLower(dv) {
			case "newline", "newlines", "underscores", "underscore",
				"tabs", "tab", "whitespace", "whitespaces",
				"carriage", "blank", "blanks", "spaces",
				"quebra", "quebras", "espacos", "espaços":
				delete(in.Slots, "dir")
			}
		}
	}
	// "under a certain/some path" — determinante indefinido torna o
	// substantivo-classe escopo NÃO-RESOLVIDO; como name é vazamento
	for i, t := range toks {
		switch t.Fold {
		case "path", "location", "caminho", "filepath", "dirpath":
		default:
			continue
		}
		if in.Slots["name"] != t.Raw && in.Slots["namepat"] != t.Raw {
			continue
		}
		indef := false
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			u := &toks[j]
			switch u.Fold {
			case "named", "called", "nomeado", "nomeada", "chamado",
				"chamada":
				// "file named path" — literal, não escopo vago
			case "certain", "some", "a", "an", "um", "uma",
				"qualquer", "algum", "alguma", "unspecified",
				"certa", "certo", "random":
				indef = true
			}
			if !u.Filler && !u.Slang {
				break
			}
		}
		if indef {
			drop(t.Raw)
			delete(in.Slots, "name")
			delete(in.Slots, "namepat")
		}
	}
	// "how many X files are there" / "quantos arquivos X" — pergunta
	// de CONTAGEM sobre o conjunto; o find lista, não conta — a
	// interrogação dropa em vez de responder listagem como contagem
	if in.Name == "file_find" || in.Name == "perm_find" ||
		in.Name == "dir_diff" {
		for i, t := range toks {
			switch t.Fold {
			case "quantos", "quantas":
				drop(t.Raw)
			case "many", "much":
				if i > 0 && toks[i-1].Fold == "how" {
					drop(toks[i-1].Raw)
					drop(t.Raw)
				}
			}
		}
	}
	// "find all files in X and count them" — o 'count' é uma SEGUNDA
	// operação que file_find não performa; vai pro Dropped em vez de
	// fingir que a listagem responde a contagem
	if in.Name == "file_find" || in.Name == "perm_find" ||
		in.Name == "dir_diff" {
		for i, t := range toks {
			switch t.Fold {
			case "count", "conta", "contar", "conte":
				// só quando é cláusula de contagem separada —
				// "count files" no início já roteia wordcount_stats;
				// "named count" usa a palavra como basename literal
				if i > 0 && in.Slots["name"] != t.Raw {
					drop(t.Raw)
				}
			}
		}
	}
	// "search only in jfs and ext2 filesystems" — restrição de TIPO de
	// filesystem (find -fstype) não é suportada por nenhum método; a
	// cláusula dropa em vez de alargar a busca pra todo fs
	if in.Name == "file_find" || in.Name == "perm_find" {
		hasFsName := false
		for _, t := range toks {
			if nuFsTypeWords[t.Fold] {
				hasFsName = true
			}
		}
		if hasFsName {
			for _, t := range toks {
				if nuFsTypeWords[t.Fold] || t.Fold == "filesystems" ||
					t.Fold == "filesystem" {
					drop(t.Raw)
				}
			}
		}
	}
	// "output two lines of '-tcp'"/"list -name" — valor com cara de
	// flag é opcional de comando digitado, não nome de arquivo; o
	// find leria "-tcp" como predicado, silent-wrong
	for _, k := range []string{"name", "target", "pat", "namepat", "file"} {
		if v := in.Slots[k]; strings.HasPrefix(v, "-") && len(v) > 1 &&
			!strings.HasPrefix(v, "--") && in.Name != "run" {
			drop(v)
			delete(in.Slots, k)
		}
	}
	// "decompress X to standard output"/"extract to stdout" — stream
	// pra stdout não é extração em diretório; o método gravaria no
	// cwd e fingiria o modo pedido
	if in.Name == "archive_extract" || in.Name == "archive_create" {
		for i, t := range toks {
			isStdout := t.Fold == "stdout" ||
				(t.Fold == "output" && i > 0 &&
					(toks[i-1].Fold == "standard" || toks[i-1].Fold == "std")) ||
				t.Fold == "padrão" || t.Fold == "padrao"
			if isStdout {
				drop(t.Raw)
				if i > 0 && (toks[i-1].Fold == "standard" || toks[i-1].Fold == "std") {
					drop(toks[i-1].Raw)
				}
				if in.Slots["dir"] == "output" || in.Slots["dir"] == "stdout" ||
					in.Slots["dir"] == "standard" {
					drop(in.Slots["dir"])
					delete(in.Slots, "dir")
				}
			}
		}
	}
	// predicado temporal que os slots de perm_find não cobrem:
	// superlativo (newest/oldest sem sort+limit), btime (created/
	// touched) e janela vaga ("recently"). Token já consumido por
	// newer/older/tfield não é resto — não dropa.
	if in.Name == "perm_find" {
		timeish := map[string]bool{
			"today": true, "hoje": true, "yesterday": true,
			"ontem": true, "created": true, "criado": true,
			"criada": true, "touched": true, "recent": true,
			"recente": true, "recently": true, "recentemente": true,
			"newest": true, "oldest": true, "latest": true,
		}
		for i, t := range toks {
			if taken[i] {
				continue
			}
			if timeish[t.Fold] || t.has("timeref") {
				drop(t.Raw)
			}
		}
	}
	// "move them to another folder" — destino VAGO é operando expresso
	// mas não-resolvido: faltou o path concreto; dropa e recusa em vez
	// de mover pra cwd
	if in.Name == "file_move" && in.Slots["dest"] == "" {
		for i, t := range toks {
			vague := t.Fold == "another" || t.Fold == "other" || t.Fold == "some" ||
				t.Fold == "outra" || t.Fold == "outro" || t.Fold == "qualquer" ||
				t.Fold == "any" || t.Fold == "algum" || t.Fold == "alguma"
			if !vague {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "folder", "directory", "dir", "pasta", "place",
					"lugar", "location", "local", "somewhere", "elsewhere":
					drop(t.Raw)
					drop(u.Raw)
				}
				break
			}
		}
	}
	// "convert to HTML"/"transforma em pdf" — conversão de formato não
	// é suportada; o verbo de mutação e o formato alvo dropam.
	// date_show converte epoch↔data legitimamente — não conta.
	if in.Name != "date_show" {
		for i, t := range toks {
			switch t.Fold {
			case "convert", "converting", "converter", "transform",
				"transformar", "export", "exportar", "turn", "virar":
				drop(t.Raw)
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					if u.Filler || u.Fold == "to" || u.Fold == "into" ||
						u.Fold == "para" || u.Fold == "em" || u.Fold == "them" ||
						u.Fold == "eles" {
						continue
					}
					if nuPureExt(u.Fold) || nuExtWords[u.Fold] ||
						u.Fold == "html" || u.Fold == "pdf" ||
						u.Fold == "files" || u.Fold == "file" {
						drop(u.Raw)
					}
					break
				}
			}
		}
	}
	// "files present in the pattern text file"/"listed in manifest.txt"
	// — filtro por lista/manifesto não é suportado; a cláusula dropa
	for i, t := range toks {
		switch t.Fold {
		case "present", "presentes", "listed", "listados", "contained",
			"appearing", "mencionados", "mentioned":
			for j := i + 1; j < len(toks) && j <= i+6; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "in" || u.Fold == "em" ||
					u.Fold == "the" || u.Fold == "a" {
					continue
				}
				if u.Fold == "pattern" || u.Fold == "patterns" ||
					u.Fold == "list" || u.Fold == "manifest" ||
					nuFileShaped(u.Fold) || u.Quoted {
					drop(t.Raw)
					for k := i + 1; k <= j; k++ {
						drop(toks[k].Raw)
					}
				}
				break
			}
		}
	}
	// "2 lines of context"/"line 111 in file X" — contexto de grep e
	// endereço-por-linha não são suportados; o predicado posicional
	// dropa em vez de virar filtro de contagem
	for i, t := range toks {
		if t.Fold == "context" || t.Fold == "contexto" {
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				u := &toks[j]
				if u.Filler || u.Fold == "of" || u.Fold == "de" {
					continue
				}
				if u.Fold == "lines" || u.Fold == "linhas" || nuIsNumber(u.Fold) {
					drop(u.Raw)
				}
				break
			}
			drop(t.Raw)
		}
		if (t.Fold == "line" || t.Fold == "linha") && i+1 < len(toks) &&
			nuIsNumber(toks[i+1].Fold) &&
			(in.Name == "file_find" || in.Name == "perm_find" ||
				in.Name == "list_files") {
			// "line 111" singular é posição, não filtro de contagem —
			// o slot 'lines' leria como "arquivo com 111 linhas"
			drop(t.Raw)
			drop(toks[i+1].Raw)
		}
	}
	// "suppressing error messages"/"discard errors" — supressão de
	// stderr não é parâmetro; a cláusula dropa
	for i, t := range toks {
		switch t.Fold {
		case "suppress", "suppressing", "discard", "discarding",
			"hide", "hiding", "ignore", "ignoring", "silence",
			"silencing", "suprimindo", "descartando":
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "any" || u.Fold == "the" {
					continue
				}
				switch u.Fold {
				case "error", "errors", "erro", "erros", "messages",
					"mensagens", "stderr", "warnings", "avisos", "output":
					drop(t.Raw)
					drop(u.Raw)
				}
				break
			}
		}
	}
	// "from the start of the day"/"desde o início do dia" — o par
	// start/início é âncora temporal, não pasta
	for i, t := range toks {
		switch t.Fold {
		case "start", "inicio", "início", "beginning", "começo", "comeco":
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "of" || u.Fold == "de" ||
					u.Fold == "the" || u.Fold == "do" || u.Fold == "da" {
					continue
				}
				if u.Fold == "day" || u.Fold == "dia" || u.Fold == "today" ||
					u.Fold == "hoje" || timeWords[u.Fold] {
					drop(t.Raw)
					drop(u.Raw)
				}
				break
			}
		}
	}
	// o verbo-cue de exceção nunca é nome: "names end with X but
	// ignoring Y" — 'but' colando no glob é franken
	for _, k := range []string{"name", "namepat"} {
		v := in.Slots[k]
		if v == "" {
			continue
		}
		first := strings.Fields(v)
		if len(first) > 0 && (nuExceptCues[first[0]] || first[0] == "but" ||
			first[0] == "but*") {
			drop(v)
			delete(in.Slots, k)
		}
	}
	// "starting with letter 'a'"/"começando com a letra 'a'" — a letra
	// citada é PREFIXO do nome, não nome exato: vira glob 'a*'
	if v := in.Slots["name"]; v != "" && !strings.ContainsAny(v, "*?") &&
		in.Name == "file_find" {
		pref := false
		for _, t := range toks {
			switch t.Fold {
			case "letter", "letters", "letra", "letras", "starting",
				"start", "starts", "begins", "begin", "beginning",
				"prefix", "prefixo", "começando", "comecando",
				"iniciando", "inicial":
				pref = true
			}
		}
		switch v {
		case "letter", "letters", "letra", "letras", "start",
			"starts", "starting", "prefix", "prefixo", "inicial",
			"beginning", "comecando", "começando":
			// "files named letter" — a palavra-classe É o nome
			// literal pedido, não o cue de prefixo
			pref = false
		}
		if pref {
			in.Slots["name"] = v + "*"
		}
	}
	// "the latest modified file" (singular) — superlativo sobre item
	// único é top-1: o sort já ordena; o limit fixa o primeiro
	if in.Slots["sort"] != "" && in.Slots["limit"] == "" {
		for i, t := range toks {
			sup := t.Fold == "latest" || t.Fold == "newest" ||
				t.Fold == "oldest" || t.Fold == "recentest"
			if !sup {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.has("timeref") || nuTemporalVocab[u.Fold] {
					continue
				}
				switch u.Fold {
				case "file", "arquivo", "document", "documento",
					"directory", "dir", "folder", "pasta":
					in.Slots["limit"] = "1"
				}
				break
			}
		}
	}
	// preposição nua ou classe-lugar solta nunca é basename — "count
	// of files" não tem name "of"; "in the path /x" tem dir /x
	for _, k := range []string{"name", "namepat"} {
		v := in.Slots[k]
		switch v {
		case "of", "de", "do", "da", "the":
			drop(v)
			delete(in.Slots, k)
		case "path", "location", "where", "caminho":
			if in.Slots["dir"] != "" {
				drop(v)
				delete(in.Slots, k)
			}
		}
	}
	// su/sudo: "login as user X", "switch user to X", "run ksh as
	// user X", "simulate a full login", "get root user access" —
	// impersonação não é suportada por nenhum intent; o nome-alvo vai
	// pro Dropped em vez de alimentar um intent de leitura que
	// fingiria a troca de conta.
	{
		imp := false
		cueAt := -1
		for i, t := range toks {
			switch t.Fold {
			case "login":
				// "login as/of user X" — 'login' sozinho ("quem está
				// no login") não é impersonação
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					switch toks[j].Fold {
					case "as", "of", "como":
						imp, cueAt = true, j
					}
					if !toks[j].Filler {
						break
					}
				}
			case "switch":
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					if nuOwnerCues[toks[j].Fold] {
						imp, cueAt = true, i
					}
					switch toks[j].Fold {
					case "to", "para", "pra", "pro", "into":
						continue // "switch to user X" — direção transparente
					}
					if !toks[j].Filler {
						break
					}
				}
			case "simulate", "simular", "become":
				for j := i + 1; j < len(toks) && j <= i+5; j++ {
					switch toks[j].Fold {
					case "login", "shell":
						imp, cueAt = true, j
					case "full", "completa", "completo", "total":
						continue // "a full login" — intensificador é transparente
					}
					if !toks[j].Filler {
						break
					}
				}
			case "as", "como":
				if i+1 < len(toks) && nuOwnerCues[toks[i+1].Fold] {
					for j := i - 1; j >= 0 && j >= i-5; j-- {
						switch toks[j].Fold {
						case "run", "execute", "executar", "rodar",
							"shell", "ksh", "bash", "sh", "zsh",
							"command", "comando", "simulate":
							imp, cueAt = true, i
						}
						if !toks[j].Filler {
							break
						}
					}
				}
			case "access", "acesso":
				for j := i - 3; j <= i+3 && j < len(toks); j++ {
					if j < 0 {
						continue
					}
					switch toks[j].Fold {
					case "root", "superuser", "admin", "administrator":
						imp, cueAt = true, i
					}
				}
			}
		}
		if imp {
			for _, t := range toks {
				switch t.Fold {
				case "login", "switch", "as", "como", "access",
					"acesso", "simulate", "simular", "become",
					"sudo", "user", "users", "usuario", "usuário",
					"shell", "ksh", "full":
					drop(t.Raw)
				}
			}
			// o nome-alvo depois do cue ("as user postgres" →
			// postgres): algumas posições à frente do cue
			for j := cueAt + 1; j < len(toks) && j <= cueAt+4; j++ {
				u := &toks[j]
				if u.Filler || nuOwnerCues[u.Fold] ||
					u.Fold == "user" || u.Fold == "users" {
					continue
				}
				if u.Raw != "" {
					drop(u.Raw)
					break
				}
			}
		}
	}
	// "names shorter than 25 characters" — o namelen recusa
	// comparativo (glob não expressa); sem o drop o '25' vaza pra
	// limit e 'characters' pra name como se fossem operandos
	if in.Slots["namelen"] == "" {
		for i, t := range toks {
			switch t.Fold {
			case "characters", "character", "chars", "char",
				"caracteres", "caractere", "letters", "letras":
			default:
				continue
			}
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				if toks[j].Filler {
					continue
				}
				if !nuIsNumber(toks[j].Fold) {
					break
				}
				cmp := false
				for k := j - 1; k >= 0 && k >= j-4; k-- {
					if toks[k].Filler {
						continue
					}
					switch toks[k].Fold {
					case "shorter", "longer", "less", "fewer", "more",
						"greater", "menor", "menores", "maior",
						"maiores", "menos", "mais", "than":
						cmp = true
					}
					break
				}
				if cmp {
					drop(toks[j].Raw)
					drop(t.Raw)
				}
				break
			}
		}
	}
	// owner/group com vocábulo de exibição/estado não é nome de
	// conta — "user, group, and full path" são COLUNAS de saída;
	// "logado" é estado da sessão; "has/what" é a pergunta colada
	for _, k := range []string{"owner", "group", "not_owner",
		"not_group", "src_owner", "src_group"} {
		v := in.Slots[k]
		if v == "" {
			continue
		}
		switch strings.ToLower(v) {
		case "full", "path", "list", "permissions", "permission",
			"column", "columns", "name", "names", "user", "users",
			"group", "groups", "logado", "logged", "login", "session",
			"current", "display", "belong", "has", "what", "time",
			"terminal", "file", "access", "confirmation", "confirmação",
			"after", "before", "process", "type",
			// vocábulo de modo caiu no dono — "read-execute
			// permission for owner": o perm-por-classe é a spec,
			// não um usuário chamado "read-execute"
			"read", "write", "execute", "read-write", "read-execute",
			"write-execute", "read-write-execute", "rwx", "rw", "rx",
			"wx", "xr", "r-x", "rw-", "rwxr", "readable", "writable",
			"executable", "leitura", "escrita", "execução", "execucao":
			delete(in.Slots, k)
			drop(v)
		}
	}
	// dir com vocábulo de tempo/membership/unidade é vazamento, não
	// pasta — "with modification time" não tem dir "time"; "do not
	// belong to user" não tem dir "belong". Roda ANTES da migração
	// name→dir pra liberar o slot pro escopo real. Quando um slot
	// temporal já cobriu a janela ("older" pegou o 10-minutes-ago),
	// 'time' é phrasing do predicado — some quieto, sem Dropped.
	if v := in.Slots["dir"]; v != "" {
		temporal := in.Slots["newer"] != "" || in.Slots["older"] != "" ||
			in.Slots["newer_date"] != "" || in.Slots["older_date"] != "" ||
			in.Slots["tfield"] != ""
		switch strings.ToLower(v) {
		case "time", "date", "hora", "modification",
			"modificação":
			delete(in.Slots, "dir")
			if !temporal {
				drop(v)
			}
		case "belong", "belongs", "pertence":
			delete(in.Slots, "dir")
			// "files which do not belong to user john" — belong é
			// o verbo do predicado not_owner já coberto; quieto
			if in.Slots["not_owner"] == "" && in.Slots["not_group"] == "" &&
				in.Slots["src_owner"] == "" && in.Slots["src_group"] == "" {
				drop(v)
			}
		case "access", "acesso", "login", "session", "user", "users",
			"usuario", "usuário", "group", "grupo", "string",
			"termo", "term", "word", "palavra", "up", "down",
			"containing", "contendo", "script", "variable",
			"variavel", "variável", "depth", "level", "nivel",
			"nível", "descriptor", "saved", "ouput", "output",
			"arguments", "argumentos",
			// descritor de transformação/posição/entrada — "by
			// appending X"/"from the beginning"/"inode numbers
			// on stdin": nunca são pasta
			"appending", "prepending", "adding", "removing",
			"replacing", "substituting", "beginning", "ending",
			"middle", "numbers", "number", "números", "numeros",
			"inode", "inodes", "stdin", "start", "starting",
			// posição de leitura — "grep from bottom of file":
			// o cursor do comando, nunca uma pasta
			"bottom", "top", "end", "tail", "head", "front",
			"back",
			// flag de recursão colada ao escopo — "under current
			// directory no-recursively" não tem dir "no-recursively"
			"no-recursively", "norecursively", "no-recursive",
			"norecursive", "not-recursively", "not-recursive",
			"non-recursively", "nonrecursive", "non-recursive",
			"no-recursion", "non-recursion",
			// verbo de pipeline vazou pro escopo — "process it with
			// sed" não tem pasta "process"; "have full access" não
			// tem pasta "have"
			"process", "processes", "processo", "have", "has",
			"had", "tem", "possui", "tenha":
			delete(in.Slots, "dir")
			drop(v)
		}
	}
	// file/target com substantivo-classe é a CLASSE relida como alvo —
	// "all regular files" tem type:file, não um arquivo chamado
	// "regular". Fatal em mutação: chmod num file "regular" inventado.
	for _, fk := range []string{"file", "target"} {
		v := in.Slots[fk]
		// adjetivo-classe nunca é basename literal
		switch strings.ToLower(v) {
		case "regular", "normal", "plain", "type", "sub-directories",
			"subdirectories", "sub-folders", "subfolders", "contain",
			"containing", "standard", "directory", "whitespaces",
			"whitespace", "confirmation":
			delete(in.Slots, fk)
			drop(v)
			continue
		}
		// "GID=2000" é predicado de atributo, não nome de arquivo —
		// o filtro numérico não é suportado; recusa honesta
		if strings.Contains(v, "=") && !strings.ContainsAny(v, " ") {
			if k, _, ok := strings.Cut(v, "="); ok &&
				len(k) > 1 && nuIsNumber(strings.TrimPrefix(v, k+"=")) {
				delete(in.Slots, fk)
				drop(v)
				continue
			}
		}
		// união de substantivos-classe com '/' ("regular/normal
		// files") — a classe relida como nome literal
		if strings.Contains(v, "/") && !strings.Contains(v, "//") &&
			!strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "~/") &&
			!strings.HasPrefix(v, "./") {
			allClass := true
			for _, part := range strings.Split(v, "/") {
				switch strings.ToLower(part) {
				case "file", "files", "regular", "normal", "plain",
					"directory", "directories", "folder", "folders",
					"text", "empty", "hidden", "ordinary", "type",
					"standard", "link", "links", "symlink", "symlinks",
					"arquivo", "arquivos", "pasta", "pastas":
				default:
					allClass = false
				}
			}
			if allClass {
				delete(in.Slots, fk)
				drop(v)
				continue
			}
		}
		// substantivo-classe só é suspeito em contexto de massa —
		// "all regular files" não tem arquivo "files"; "mkdir
		// folder" é legítimo
		if in.Slots["mass"] == "" {
			continue
		}
		switch strings.ToLower(v) {
		case "file", "files", "arquivo", "arquivos", "directory",
			"directories", "dir", "dirs", "folder", "folders",
			"pasta", "pastas", "link", "links", "symlink", "symlinks":
			delete(in.Slots, fk)
			drop(v)
		}
	}
	// name/namepat com forma de CAMINHO é escopo relido — "/var" e
	// "$homeDirData" são a pasta-alvo, não basename
	for _, k := range []string{"name", "namepat"} {
		v := in.Slots[k]
		if v == "" {
			continue
		}
		dirish := strings.HasSuffix(v, "/")
		if !dirish && strings.HasPrefix(v, "/") &&
			!strings.ContainsAny(v, "*?") {
			dirish = true
		}
		if !dirish && strings.HasPrefix(v, "$") {
			low := strings.ToLower(v)
			dirish = strings.Contains(low, "dir") ||
				strings.Contains(low, "home") ||
				strings.Contains(low, "folder") ||
				strings.Contains(low, "pasta") ||
				strings.Contains(low, "path")
		}
		if dirish {
			v = strings.TrimPrefix(v, "*")
		}
		if !dirish {
			continue
		}
		delete(in.Slots, k)
		if _, has := def.Slots["dir"]; has && in.Slots["dir"] == "" {
			in.Slots["dir"] = v
		} else {
			drop(v)
		}
	}
	// kill_process: o método roda "kill -TERM <pid>" — alvo que é
	// preposição/adjetivo de estado nunca é pid, e sinal ≠ TERM é
	// operando que o método não consegue mandar (SIGKILL calado
	// viraria TERM — mutação errada).
	if in.Name == "kill_process" {
		if v := in.Slots["target"]; v != "" {
			switch strings.ToLower(v) {
			case "to", "para", "of", "de", "do", "in", "on", "at",
				"by", "from", "with", "active", "inactive",
				"background", "foreground", "last", "first",
				"all", "every", "each", "sent", "current",
				"job", "jobs", "process", "processes", "signal":
				delete(in.Slots, "target")
				drop(v)
			}
		}
		for _, t := range toks {
			f := t.Fold
			if strings.HasPrefix(f, "sig") && len(f) > 3 ||
				f == "9" || f == "-9" {
				switch f {
				case "sigterm", "signal", "signals":
					// default do método — redundante, não dropa
				default:
					drop(t.Raw)
				}
			}
		}
		// "kill all jobs"/"background jobs" é jobspec de shell
		// (%1 %2...), não pid — o método sem alvo mataria por
		// engano ou falharia; jobs não é suportado
		if in.Slots["target"] == "" {
			for _, t := range toks {
				switch t.Fold {
				case "jobs", "job", "background",
					"foreground", "all", "every",
					"each", "todos", "todas", "cada":
					drop(t.Raw)
				}
			}
		}
	}
	// "update the permission" é chmod roteado pra apt — vocábulo
	// de permissão sem pacote denuncia o misroute; "update deps"/
	// "instala o tailscale" genérico segue (dep vazio vira
	// Missing e o método pergunta)
	if in.Name == "update_dep" || in.Name == "install_dep" {
		if in.Slots["dep"] == "" {
			permVocab := false
			for _, t := range toks {
				switch t.Fold {
				case "permission", "permissions", "permissao",
					"permissão", "permissoes", "permissões",
					"chmod", "mode", "umask", "perms",
					"writable", "readable", "executable":
					permVocab = true
				}
			}
			if permVocab {
				for _, t := range toks {
					switch t.Fold {
					case "update", "updates", "updated",
						"upgrade", "upgrades", "upgraded",
						"atualiza", "atualize", "atualizar",
						"install", "installs", "installed",
						"installing", "instala", "instale":
						drop(t.Raw)
					}
				}
			}
		}
	}
	// "set variable X to ..." / "run command as user $USERNAME" —
	// env_get lê variável, não SETA; nome de var que é comando ou
	// meta-palavra é vazamento, não operando
	if in.Name == "env_get" || in.Name == "env_status" {
		// o nome da var é o token SCREAMING marcado envvars — o
		// extrator genérico pula palavra-com-conceito, mas aqui o
		// conceito É a evidência do operando
		if in.Slots["name"] == "" {
			for i, t := range toks {
				if t.has("envvars") && len(t.Raw) >= 3 &&
					t.Raw == strings.ToUpper(t.Raw) &&
					strings.IndexFunc(t.Raw, unicode.IsLetter) >= 0 {
					in.Slots["name"] = t.Raw
					taken[i] = true
					break
				}
			}
		}
		for i, t := range toks {
			switch t.Fold {
			case "set", "assign", "defina", "define", "export":
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					switch toks[j].Fold {
					case "variable", "variables", "var", "vars",
						"variavel", "variável", "variaveis",
						"variáveis", "environment":
						drop(t.Raw)
						drop(toks[j].Raw)
					}
					if !toks[j].Filler {
						break
					}
				}
			}
		}
		if v := in.Slots["name"]; v != "" {
			// "get PATH"/"valor GOPATH"/"variable JAVA_HOME" — o
			// extrator colou o vocábulo-cue no nome; sobra 1 palavra
			// nomeável, ela é o operando. 2+ palavras = sentença.
			fs := strings.Fields(v)
			for len(fs) > 1 {
				switch strings.ToLower(fs[0]) {
				case "get", "gets", "fetch", "valor", "value",
					"qual", "the", "o", "a", "de", "do", "da",
					"variable", "variavel", "variável", "var",
					"name", "nome", "key", "chave", "env", "envvar":
					fs = fs[1:]
				default:
					goto trimDone
				}
			}
		trimDone:
			if len(fs) == 1 && fs[0] != v &&
				nuNameShaped(fs[0]) {
				in.Slots["name"] = fs[0]
				v = fs[0]
			}
			meta := strings.ContainsAny(v, ";/") ||
				strings.Contains(v, " ")
			for _, f := range strings.Fields(strings.ToLower(v)) {
				switch f {
				case "command", "comando", "login", "session",
					"current", "variable", "var", "output",
					"result", "list", "puts", "value", "operating":
					meta = true
				}
			}
			if meta {
				delete(in.Slots, "name")
				drop(v)
			}
		}
	}
	// name/namepat que é vocábulo de ação/listagem não é basename —
	// "long listing" e "display" são formato de saída. Mas "files
	// named list" É literal — cue named/called reivindica a palavra.
	for _, k := range []string{"name", "namepat"} {
		v := in.Slots[k]
		if v == "" {
			continue
		}
		if strings.ContainsAny(v, "*?") {
			continue // glob é nome explícito — 'file??' é basename
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && t.Raw == v {
				quoted = true
			}
		}
		if quoted {
			continue
		}
		switch strings.ToLower(strings.TrimRight(v, "*?")) {
		case "listing", "ilsting", "list", "display", "print",
			"output", "format", "formato", "detailed", "long",
			"sleep", "wait", "shutdown", "reboot", "halt",
			"returns", "sources", "contain", "containing", "standard",
			"directory", "directories", "folder", "folders", "file",
			"files", "regular", "normal", "whitespace", "whitespaces",
			"extension", "input", "stdin", "type":
		default:
			continue
		}
		named := false
		for i, t := range toks {
			if t.Raw != strings.TrimRight(v, "*?") &&
				t.Fold != strings.ToLower(strings.TrimRight(v, "*?")) {
				continue
			}
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				switch toks[j].Fold {
				case "named", "called", "nomeado", "nomeada",
					"chamado", "chamada", "name":
					named = true
				}
				if !toks[j].Filler {
					break
				}
			}
			break
		}
		if !named {
			delete(in.Slots, k)
			drop(v)
		}
	}
	// dir com forma de extensão (".user.gz") é sufixo relido;
	// ".." é parent-dir real, não sufixo
	if v := in.Slots["dir"]; strings.HasPrefix(v, ".") && len(v) > 1 &&
		v != ".." && !strings.ContainsAny(v, "/ ") &&
		!strings.HasPrefix(v, "./") {
		delete(in.Slots, "dir")
		drop(v)
	}
	// "excluding all regular files" — o substantivo-arquivo dentro da
	// cláusula de exceção foi relido como type:file (o INVERSO do
	// pedido: excluir regulares, não buscá-los). O tipo negado não é
	// expressável — vai pro Dropped com a palavra.
	if in.Slots["type"] != "" {
		for i, t := range toks {
			if !nuExceptCues[t.Fold] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "all" || u.Fold == "todos" ||
					u.Fold == "todas" || u.Fold == "the" {
					continue
				}
				// só dropa quando a classe excluída CONTRADIZ o type —
				// "excluding directories" com type:file é redundante
				// consistente; "excluding regular files" com
				// type:file é o inverso do pedido
				switch u.Fold {
				case "regular", "file", "files", "arquivo", "arquivos":
					if in.Slots["type"] == "file" {
						delete(in.Slots, "type")
						drop(u.Raw)
					}
				case "directory", "directories", "dir", "dirs",
					"folder", "folders", "pasta", "pastas":
					if in.Slots["type"] == "dir" {
						delete(in.Slots, "type")
						drop(u.Raw)
					}
				case "link", "links", "symlink", "symlinks":
					if in.Slots["type"] == "link" {
						delete(in.Slots, "type")
						drop(u.Raw)
					}
				}
				break
			}
		}
	}
	// "ending .user or beginning admin" — união de predicados não é
	// expressável (os slots combinam AND); o 'or' e o operando à
	// direita vão pro Dropped em vez de fingir interseção
	if in.Name == "file_find" || in.Name == "list_files" ||
		in.Name == "perm_find" || in.Name == "text_grep" {
		for i, t := range toks {
			if t.Fold != "or" && t.Fold != "ou" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				opShape := u.Quoted ||
					strings.HasPrefix(u.Raw, ".") ||
					strings.HasPrefix(u.Raw, "*") ||
					strings.HasSuffix(u.Raw, "*") ||
					strings.HasPrefix(u.Fold, "$") ||
					nuNameShaped(u.Raw)
				if opShape && !nuExceptClass[u.Fold] &&
					!u.has("files") {
					drop(t.Raw)
					drop(u.Raw)
				}
				break
			}
		}
	}
	// "sleep for 5 seconds"/"in the last half minute (30 seconds)" —
	// número + unidade de tempo é DURAÇÃO, nunca limit de resultados.
	// O par number+unit vai pro Dropped em vez de cortar a listagem.
	if v := in.Slots["limit"]; v != "" {
		for i, t := range toks {
			if t.Raw != v && t.Fold != v {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+2; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				if timeWords[u.Fold] || u.has("timeref") {
					delete(in.Slots, "limit")
					drop(t.Raw)
					drop(u.Raw)
				}
				break
			}
			break
		}
	}
	// "files present in the file foo" — arquivo como CONTAINER de
	// nomes (comm/join entre listas) não é diretório nem escopo
	// suportado; o nome vai pro Dropped. Só vale pra intents sem
	// slot file legítimo: "line 111 in file x.rb" tem file=x.rb.
	_, hasFileSlot := def.Slots["file"]
	if !hasFileSlot {
		for i, t := range toks {
			switch t.Fold {
			case "in", "em", "no", "na", "inside", "within":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				if u.Fold == "file" || u.Fold == "arquivo" {
					// "in file foo" — file singular + nome ADJACENTE
					// é container (o dir já foi bloqueado por
					// nuFileShaped no dirScan). Plural ou prep no
					// meio ("in all files under X") não é container.
					if j+1 < len(toks) {
						v := &toks[j+1]
						namey := v.Raw != "" && len(v.Concs) == 0 &&
							!v.Filler
						switch v.Fold {
						case "under", "inside", "within", "sob",
							"in", "em", "no", "na", "from", "de",
							"of", "to", "para", "and", "e", "or",
							"except", "but", "with", "com", "where",
							"that", "which":
							namey = false // prep/conjunção, nunca nome
						}
						if namey {
							drop(v.Raw)
							drop(u.Raw)
							if in.Slots["dir"] == v.Raw {
								delete(in.Slots, "dir")
							}
							if in.Slots["file"] == v.Raw {
								delete(in.Slots, "file")
							}
						}
					}
				}
				break
			}
		}
	}
	// "make directories for each line in X" / "a, b, c, ..., z" —
	// enumeração de arquivo ou faixa não é a fan-out fixa do create;
	// a cláusula vai pro Dropped em vez de criar um item inventado
	if in.Name == "create" {
		for i, t := range toks {
			switch t.Fold {
			case "each", "cada", "line", "lines", "linha", "linhas":
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					u := &toks[j]
					if u.Filler {
						continue
					}
					if u.Fold == "line" || u.Fold == "lines" ||
						u.Fold == "linha" || u.Fold == "linhas" ||
						u.Quoted || nuFileShaped(u.Fold) {
						drop(t.Raw)
						drop(u.Raw)
					}
					break
				}
			}
		}
		// "a, b, c, ..., z" — o "..." tokeniza como "." solto. Uma
		// faixa enumerada não é a fan-out fixa do create; o alvo
		// colhido de dentro é inventado
		for i, t := range toks {
			if t.Fold != "." && t.Raw != "." &&
				!(strings.Trim(t.Raw, ".") == "" && len(t.Raw) > 1) {
				continue
			}
			letters := 0
			for k := i - 4; k <= i+4 && k < len(toks); k++ {
				if k >= 0 && len(toks[k].Fold) == 1 && toks[k].Fold != "." {
					letters++
				}
			}
			if letters < 2 {
				continue // "." isolado pode ser path corrente
			}
			drop(t.Raw)
			for k := i - 4; k <= i+4 && k < len(toks); k++ {
				if k >= 0 && len(toks[k].Fold) == 1 &&
					toks[k].Fold != "." {
					drop(toks[k].Raw)
				}
			}
			if v := in.Slots["target"]; v != "" {
				delete(in.Slots, "target")
				drop(v)
			}
			if v := in.Slots["targets"]; v != "" {
				delete(in.Slots, "targets")
				drop(v)
			}
			break
		}
		if v := in.Slots["targets"]; v != "" {
			// cada item da enumeração tem que ser um nome plausível —
			// "have|write|access" são verbos/attrs de um predicado
			// (a busca por permissão não é criação de arquivos)
			parts := strings.Split(v, "|")
			bad := false
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" || strings.Contains(p, " ") {
					bad = true
					continue
				}
				if strings.HasPrefix(p, "$") || strings.HasPrefix(p, "%") {
					bad = true // var de ambiente na enum — faixa, não nome
					continue
				}
				switch strings.ToLower(p) {
				case "have", "write", "access", "read", "execute",
					"permission", "permissions", "user", "group",
					"others", "owner", "line", "lines", "linha",
					"linhas", "cada", "each",
					// phrasing de cláusula colhida pelo fan-out —
					// "save the log"/"excluding those"/"in a path":
					// a enum inteira é parse-garbage. 'logs',
					// 'tmp', 'backup', 'path' são nomes reais —
					// só palavras que nunca são nome de pasta
					"save", "saved", "saving", "excluding",
					"except", "exceto", "present", "specific",
					"file", "files", "directory", "directories",
					"folder", "folders", "named", "name", "those",
					"that", "are", "the", "and", "or", "ou", "of",
					"in", "on", "to", "into":
					bad = true
					continue
				}
				if nuFileShaped(p) || nuNameShaped(p) || nuIsNumber(p) {
					continue
				}
			}
			if bad || strings.Contains(v, "line") ||
				strings.Contains(v, "linha") {
				delete(in.Slots, "targets")
				drop(v)
			}
		}
		// "create a 100MB test file" — o número+unidade é o TAMANHO
		// do arquivo, não o nome; o método não recebe size
		if v := in.Slots["target"]; v != "" {
			low := strings.ToLower(v)
			size := false
			for _, u := range []string{"b", "kb", "mb", "gb", "tb",
				"kib", "mib", "gib", "tib", "k", "m", "g"} {
				if strings.HasSuffix(low, u) {
					if n := strings.TrimSuffix(low, u); n != "" &&
						nuIsNumber(n) {
						size = true
					}
				}
			}
			if size {
				delete(in.Slots, "target")
				drop(v)
			}
		}
		// "create a backup of the files" — backup é a OPERAÇÃO
		// (cópia/arquivo), não um mkdir chamado "backup"
		for i, t := range toks {
			if t.Fold != "backup" && t.Fold != "copia" &&
				t.Fold != "cópia" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+2; j++ {
				u := &toks[j]
				gen := u.Fold == "of" || u.Fold == "de" ||
					u.Fold == "do" || u.Fold == "da"
				if u.Filler && !gen {
					continue
				}
				if gen {
					drop(t.Raw)
					delete(in.Slots, "target")
					delete(in.Slots, "targets")
				}
				break
			}
		}
		// "Prints name of temporary file but doesn't create nothing"
		// — a criação é NEGADA: o pedido é só o nome (mktemp -u).
		// Sem o guard o alvo colhido da frase vira arquivo real.
		for i, t := range toks {
			switch t.Fold {
			case "create", "creates", "creating", "criar", "crie",
				"make", "makes", "making", "mkdir":
			default:
				continue
			}
			negated := false
			for k := i - 1; k >= 0 && k >= i-3; k-- {
				u := &toks[k]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "not", "n't", "no", "never", "not_create",
					"doesn't", "doesnt", "doesn`t", "don't", "dont",
					"don`t", "didn't", "didnt", "won't", "wont",
					"cannot", "can't", "cant", "without", "sem",
					"não", "nao", "nunca", "jamais":
					negated = true
				}
				break
			}
			if !negated {
				continue
			}
			drop(t.Raw)
			delete(in.Slots, "target")
			delete(in.Slots, "targets")
			delete(in.Slots, "type")
			delete(in.Slots, "mode")
			break
		}
	}
	// dep com cláusula de diagnóstico colada — "python pip figure
	// wrong"/"permission php"/"Homebrew's path": o pacote é uma
	// palavra-nome, o resto é frase. Vocábulo de arquivo-op/
	// diagnóstico no valor derruba o slot inteiro (instalar o
	// pacote errado é mutação); vocábulo de gerenciador ("pip
	// install flask") é phrasing — apara e fica com o nome.
	if v := in.Slots["dep"]; v != "" {
		bad := false
		var keep []string
		for _, wd := range strings.Fields(strings.ToLower(v)) {
			switch wd {
			case "pip", "pip3", "npm", "brew", "apt", "apt-get",
				"yum", "dnf", "pacman", "gem", "cargo", "go",
				"package", "packages", "pacote", "pacotes",
				"install", "installer", "installed", "instalar",
				"instalado", "instalada", "instale", "instala":
				// phrasing do gerenciador — não é parte do nome
			case "figure", "wrong", "importable", "permission",
				"permissions", "files", "file", "path", "paths",
				"out", "what", "whats", "what's", "why", "is",
				"broke", "broken", "can't", "cant", "my", "system",
				"make", "it", "update", "updating", "display",
				"displays", "the", "a", "an", "and", "with", "for",
				"word", "words", "name", "variable", "output",
				"save", "directory", "current", "homebrew's",
				"homebrew":
				bad = true
			default:
				keep = append(keep, wd)
			}
		}
		switch {
		case bad || strings.ContainsAny(v, "'\""):
			delete(in.Slots, "dep")
			drop(v)
		case len(keep) == 0:
			// valor inteiro era phrasing de gerenciador ("npm")
			delete(in.Slots, "dep")
			drop(v)
		case len(keep) < len(strings.Fields(v)):
			in.Slots["dep"] = strings.Join(keep, " ")
		}
		// "names matching each word in variable X" — o citado é o
		// NOME DA VAR, não pacote: dep:variable-name é fan-out de
		// create, não install/update
		if in.Slots["dep"] != "" {
			for i, t := range toks {
				switch t.Fold {
				case "variable", "variavel", "variável", "var":
					for j := i + 1; j < len(toks) && j <= i+3; j++ {
						u := &toks[j]
						if u.Filler {
							continue
						}
						if u.Raw == in.Slots["dep"] ||
							u.Fold == in.Slots["dep"] {
							delete(in.Slots, "dep")
							drop(t.Raw)
							drop(u.Raw)
						}
						break
					}
				}
			}
		}
	}
	// pedido de DIAGNÓSTICO ("figure out what's wrong", "make it
	// importable") não é instalação — o verbo-diagnóstico solto
	// é o objetivo real, sem slot que o cubra
	for i, t := range toks {
		if taken[i] {
			continue // 'figure' consumido como dep legítimo fica
		}
		switch t.Fold {
		case "figure", "diagnose", "diagnosticar", "importable":
			drop(t.Raw)
		}
	}
	// "top-ten utilities from history"/"most used commands" —
	// histórico de shell não é git log. Só os marcadores de
	// ranking (utilities/top/most) discriminam — 'history' e
	// 'commits' sozinhos são descritores legítimos do git.
	if in.Name == "git_log" {
		shellRank, hasCmd, hasHist, hasRank := false, false, false, false
		for _, t := range toks {
			switch t.Fold {
			case "utilities", "utilitarios", "utilitários":
				shellRank = true
			case "commands", "comandos":
				hasCmd = true
			case "history", "histórico", "historico":
				hasHist = true
			case "top", "topten", "most", "mais", "used", "usados", "usadas":
				hasRank = true
			}
		}
		shellRank = shellRank || (hasCmd && (hasHist || hasRank))
		if shellRank {
			for i, t := range toks {
				if taken[i] {
					continue
				}
				switch t.Fold {
				case "history", "histórico", "historico", "commands",
					"comandos", "shell", "utilities", "utilitarios",
					"utilitários", "top", "topten", "used", "usados",
					"usadas":
					drop(t.Raw)
				}
			}
		}
	}
	// extração de campo/coluna (awk/cut) não é data nem busca —
	// "second section of space-separated data" é operação não
	// suportada, não um parâmetro. Tokens consumidos por slots
	// (sort key/delim) ficam fora.
	for i, t := range toks {
		if taken[i] {
			continue
		}
		switch t.Fold {
		case "section", "sections", "column", "columns", "field",
			"fields", "separated", "separado", "separada", "delimiter",
			"delimitador":
			drop(t.Raw)
		}
	}
	// pattern/query com descriptor vago não é literal — "search for
	// a word"/"search for a pattern" não diz QUAL padrão; executar
	// com 'word'/'pattern' literal é o silent-wrong clássico.
	// Aspado é literal explícito ("replace 'word'").
	for _, k := range []string{"query", "pattern", "from"} {
		v := in.Slots[k]
		quoted := false
		for _, t := range toks {
			if t.Quoted && t.Raw == v {
				quoted = true
			}
		}
		if quoted {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "a word", "word", "a pattern", "pattern", "padrão",
			"padrao", "palavra", "something", "anything", "a string",
			"string", "a term", "term":
			delete(in.Slots, k)
			drop(v)
		}
	}
	// pattern que é CLÁUSULA-DESCRIÇÃO não é literal de grep:
	// "last occurrence between two tags" descreve posição/âncoras,
	// "non localhost users" descreve o conjunto — grep acharia a
	// frase, não o dado. Aspado continua literal.
	for _, k := range []string{"pattern", "query"} {
		v := in.Slots[k]
		quoted := false
		for _, t := range toks {
			if t.Quoted && t.Raw == v {
				quoted = true
			}
		}
		if v == "" || quoted {
			continue
		}
		descHits := 0
		for _, f := range strings.Fields(strings.ToLower(v)) {
			switch f {
			case "occurrence", "occurrences", "between", "tags",
				"tag", "users", "user", "characters", "character",
				"least", "containing", "contain", "lines", "line",
				"non", "first", "last", "most", "number", "count",
				"position", "each", "every", "records", "record",
				"one", "ones", "files", "file", "word", "words",
				"string", "strings", "text", "more", "longer",
				"shorter", "exists", "present":
				descHits++
			}
		}
		if descHits >= 2 {
			delete(in.Slots, k)
			drop(v)
		}
	}
	// glob nu não consumido — "*fink*" em intent sem slot de
	// padrão (name/namepat/pat/targets/exclude) é operando que
	// nenhum filtro aplica → T8: vai pro Dropped
	globSlots := map[string]bool{
		"name": true, "namepat": true, "pat": true, "targets": true,
		"target": true, "exclude": true, "name_re": true, "srcs": true,
		"file": true, "query": true,
	}
	consumesGlob := false
	for k := range def.Slots {
		if globSlots[k] && in.Slots[k] != "" {
			consumesGlob = true
		}
	}
	if !consumesGlob {
		for i, t := range toks {
			if taken[i] || t.Filler {
				continue
			}
			if strings.ContainsAny(t.Raw, "*?") && len(t.Raw) > 1 {
				drop(t.Raw)
			}
		}
	}
	// "year-month-day hour:minute:second" — spec de formato livre
	// não é suportada (date_show emite formato fixo); o operando
	// de formato vai pro Dropped
	if in.Name == "date_show" {
		for i, t := range toks {
			if taken[i] || t.Filler {
				continue
			}
			f := strings.ToLower(t.Fold)
			if strings.Contains(f, "year") || strings.Contains(f, "month") ||
				strings.Contains(f, "hour") && strings.Contains(f, ":") ||
				strings.HasPrefix(f, "yyyy") || strings.HasPrefix(f, "yy") ||
				strings.HasPrefix(f, "dd") || strings.HasPrefix(f, "mm") ||
				strings.Contains(f, "epoch") || f == "timestamp" {
				drop(t.Raw)
			}
		}
	}
	// "venet0:0|eth0" — alternação regex em slot de nome literal:
	// o matcher é exato/substring, 'a|b' não casa iface nenhuma.
	// dir usa '|' pra enum legit (asia|emea) — name/file não.
	// exclude usa '|' pra enum suportada (alpha|beta), igual dir
	for _, k := range []string{"name", "file", "target", "pat",
		"namepat", "nametgt", "out", "arc", "link", "dep"} {
		if v := in.Slots[k]; v != "" && strings.Contains(v, "|") {
			delete(in.Slots, k)
			drop(v)
		}
	}
	// glob em slot de PATH — dir/dest/srcdir apontam pra UMA pasta;
	// '*'/[classe] é padrão de arquivo vazado, nunca casa
	for _, sk := range []string{"dir", "srcdir", "dest"} {
		v := in.Slots[sk]
		if v != "" && strings.ContainsAny(v, "*?[]") {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// metacaractere em conta — usuário/grupo nunca tem '[]&;*?'
	for _, sk := range []string{"owner", "group", "not_owner"} {
		v := in.Slots[sk]
		if v != "" && strings.ContainsAny(v, "[]*?&;") {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// '/' dentro de padrão de EXTENSÃO é path vazado —
	// '\.(css|/cms/.../tests)$' casa ext nenhuma
	for _, sk := range []string{"ext", "exts"} {
		v := in.Slots[sk]
		if v != "" && strings.Contains(v, "/") {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// '+' depois de classe/fecho é quantificador regex em slot
	// glob ('[0-9]+' não é glob); 'g++' tem '+' após letra — fica.
	// Aspas tipográficas dentro do valor são mangle de quoting
	for _, sk := range []string{"name", "namepat", "pat", "nametgt",
		"exclude", "file", "target"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		if strings.ContainsAny(v, "“”‘’") {
			delete(in.Slots, sk)
			drop(v)
			continue
		}
		for i := 1; i < len(v); i++ {
			if v[i] == '+' && (v[i-1] == ']' || v[i-1] == '*' ||
				v[i-1] == '?' || v[i-1] == ')' || v[i-1] == '.') {
				delete(in.Slots, sk)
				drop(v)
				break
			}
		}
	}
	// "first network interface" sem filtro de nome — 'first' é
	// seletor que o script não aplica (com name o filtro já resolve)
	if in.Name == "net_ifaces" && in.Slots["name"] == "" {
		for _, t := range toks {
			switch t.Fold {
			case "first", "primeiro", "primeira":
				drop(t.Raw)
			}
		}
	}
	// um valor dropado não pode ficar num slot — "dir:start" com
	// 'start' dropado é evidência podada, não resto executável
	dropSet := map[string]bool{}
	for _, d := range in.Dropped {
		dropSet[d] = true
	}
	for k, v := range in.Slots {
		if dropSet[v] || dropSet[strings.TrimRight(v, "*?")] {
			delete(in.Slots, k)
			continue
		}
		// cola que embute palavra dropada é evidência podada —
		// "path separated zeroes" não é basename, é frase truncada
		for _, w := range strings.Fields(v) {
			if dropSet[w] || dropSet[strings.TrimRight(w, "*?")] {
				delete(in.Slots, k)
				break
			}
		}
	}
	// "anaconda.*" é regex, não glob: o glob do script leria ".*" como
	// literal+um-char — valores com sintaxe regex migram pra name_re.
	// ".*" isolado fica: como glob ele casa dotfiles (o mapa hidden o
	// produz); como regex casaria TUDO — silent-wrong. Charclass
	// "[ef]" também segue glob legítimo no name.
	if v := in.Slots["name"]; v != "" && v != ".*" && in.Slots["name_re"] == "" {
		if strings.Contains(v, ".*") || strings.HasPrefix(v, "^") ||
			strings.HasSuffix(v, "$") || strings.ContainsAny(v, "|()+\\") {
			in.Slots["name_re"] = v
			delete(in.Slots, "name")
		}
	}
	// "in their contents"/"in contents" — a palavra de domínio marca
	// a busca como de CONTEÚDO; colada como name num intent que não é
	// de conteúdo vira Dropped (o file_find responderia nome-regex).
	// Quando a regra vencedora USA o conc content ("lista o conteúdo
	// do tar.gz" — archive_list), a palavra é phrasing legítimo.
	if in.Name != "text_grep" && in.Name != "text_count" &&
		in.Name != "wordcount_stats" && in.Name != "line_slice" &&
		!used["content"] {
		for _, t := range toks {
			switch t.Fold {
			case "contents", "content", "conteúdo", "conteudo", "conteudos":
				in.Dropped = append(in.Dropped, t.Raw)
			}
		}
	}
	// valor com forma de PASTA no slot de arquivo único — "/path/" não
	// é arquivo; "$DIR" se declara pasta no próprio nome. Quando a
	// intent tem dir, migra (o escopo é o pedido); senão dropa.
	for _, fk := range []string{"file", "target"} {
		v := in.Slots[fk]
		if v == "" {
			continue
		}
		dirish := strings.HasSuffix(v, "/")
		if !dirish && strings.HasPrefix(v, "$") {
			low := strings.ToLower(v)
			dirish = strings.Contains(low, "dir") ||
				strings.Contains(low, "diretorio") ||
				strings.Contains(low, "folder") ||
				strings.Contains(low, "pasta") ||
				strings.Contains(low, "home") ||
				strings.Contains(low, "path")
		}
		if !dirish {
			continue
		}
		if _, hasDir := def.Slots["dir"]; hasDir && in.Slots["dir"] == "" {
			in.Slots["dir"] = v
		} else {
			in.Dropped = append(in.Dropped, v)
		}
		delete(in.Slots, fk)
	}
	// unidade/descriptor nos slots dirish vem do fill genérico —
	// "in 1 KiB blocks" não tem pasta "KiB"; vai pro Dropped em vez
	// de procurar diretório imaginário
	for _, dk := range []string{"dir", "file", "target", "dest", "path",
		"srcs", "arc", "out", "dir2"} {
		v := in.Slots[dk]
		if v == "" {
			continue
		}
		bad := false
		switch strings.ToLower(v) {
		case "kib", "mib", "gib", "tib", "kb", "mb", "gb", "tb",
			"byte", "bytes", "block", "blocks", "bloco", "blocos",
			"symbol", "symbols", "simbolo", "símbolo", "command",
			"commands", "comando", "comandos", "input", "output",
			"entrada", "saida", "saída", "plus", "multiple",
			"ownership", "user", "users", "usuario", "usuário":
			bad = true
		}
		if dk == "dir" || dk == "dest" || dk == "path" {
			switch strings.ToLower(v) {
			// vocábulo-descritor sem nome — "in specific path
			// (saved in $x)" aponta a var, não uma pasta "path"
			case "path", "paths", "caminho", "caminhos",
				"location", "locations", "local", "locais":
				bad = true
			}
		}
		// spec remota ("user@host:/path") ou comando com flag
		// ("agrep -dEOE 'grep'") não é path de projeto — ambos são
		// operandos de uma operação que o método não implementa
		f0 := strings.Fields(v)
		for fi, wd := range f0 {
			// placeholder de printf ("%s", "%d") — formatação de
			// saída não-resolvida, nunca path
			if strings.HasPrefix(wd, "%") {
				bad = true
			}
			if strings.Contains(wd, "@") && strings.Contains(wd, ":") ||
				strings.HasPrefix(wd, "-") && len(wd) > 1 &&
					unicode.IsLetter(rune(wd[1])) {
				bad = true
			}
			// "|" é alternação de regex — exceto nos slots de
			// enumeração (dir:asia|emea é sintaxe suportada)
			if strings.Contains(wd, "|") && dk != "dir" && dk != "dir2" {
				bad = true
			}
			// "sed 's.\\..g'" — verbo de shell + argumento é o
			// comando do pipe, nunca um path
			if fi == 0 && len(f0) > 1 {
				switch strings.ToLower(wd) {
				case "sed", "awk", "gawk", "mawk", "grep", "egrep",
					"fgrep", "agrep", "tr", "sort", "uniq", "cat",
					"echo", "printf", "xargs", "find", "perl",
					"python", "python3", "ruby", "node", "bash",
					"sh", "cut", "head", "tail", "wc", "comm",
					"diff", "join", "paste", "seq", "yes", "tar",
					"gzip", "gunzip", "curl", "wget", "ssh",
					"rsync", "scp", "mycommand":
					bad = true
				}
			}
		}
		if bad {
			delete(in.Slots, dk)
			in.Dropped = append(in.Dropped, v)
		}
	}
	// var simbólica dentro de cláusula de duração é o operando de
	// tempo, não um arquivo — "older than $DAYS_TO_SAVE days": a var
	// não resolve pra número, então nenhum filtro de data pode ser
	// fabricado em volta dela
	for _, sk := range []string{"target", "name", "file", "nametgt"} {
		v := in.Slots[sk]
		if v == "" || !strings.ContainsAny(v, "$%") &&
			!(strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")) {
			continue
		}
		idx := -1
		for i, t := range toks {
			if t.Raw == v {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		for k := idx - 3; k <= idx+3 && k < len(toks); k++ {
			if k < 0 || k == idx {
				continue
			}
			switch toks[k].Fold {
			case "older", "newer", "than", "ago", "days", "day",
				"hours", "hour", "minutes", "minute", "weeks",
				"months", "years", "dias", "dia", "horas", "hora",
				"minutos", "semanas", "meses", "anos", "modified",
				"modificado", "accessed", "acessado", "created",
				"há", "ha":
				delete(in.Slots, sk)
				drop(v)
				k = len(toks)
			}
		}
	}
	// var de shell em slot de NOME/PADRÃO vira literal silencioso —
	// "pattern:$SEARCH" grepa "$SEARCH" literal; "namepat:$srch*"
	// globa o prefixo com cifrão. Em slots de path (dir/dest/file)
	// o brPath já recusa com erro honesto, então lá pode ficar.
	for _, sk := range []string{"pattern", "query", "namepat", "nametgt",
		"name", "pat", "dep"} {
		v := in.Slots[sk]
		if v != "" && (strings.HasPrefix(v, "$") ||
			strings.HasPrefix(v, "%")) {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// padrão de NOME com barra é path-glob, não basename — o matcher
	// de namepat não atravessa "/", então nunca bate (sucesso vazio).
	// name aspado pode ser path relativo real ("your/dir")
	for _, sk := range []string{"name", "namepat", "pat", "nametgt"} {
		v := in.Slots[sk]
		if v != "" && strings.Contains(v, "/") {
			quoted := false
			if sk == "name" {
				for _, t := range toks {
					if t.Quoted && t.Raw == v {
						quoted = true
						break
					}
				}
			}
			// aspado só salva path puro ("your/dir"); '.*'/'['
			// denuncia regex relido como nome
			if strings.ContainsAny(v, "*?[") ||
				strings.HasPrefix(v, ".") ||
				strings.ContainsAny(v, "^$\\|") {
				quoted = false
			}
			if !quoted {
				delete(in.Slots, sk)
				drop(v)
			}
		}
		// vírgula no glob é brace-mangle ("*,txt") — não é sintaxe
		if v := in.Slots[sk]; v != "" && strings.Contains(v, ",") {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// brace-expansion, placeholder <filetype>, content-regex <tag>
	// e parêntese de frase não são basename nem path — o matcher
	// casa literal e o alvo errado (ou nada) sai mutado. ${VAR} em
	// path segue pro refuse honesto do runtime, que explica a var.
	// '<'/'>' nunca é nome real (nenhum basename começa com '<') —
	// cai até quando aspado ('<?xml' é padrão de CONTEÚDO, não dir)
	for _, sk := range []string{"name", "namepat", "pat", "nametgt",
		"name_re", "dir", "srcdir", "dest", "target", "file",
		"file2", "arc", "out", "link", "a", "b", "dep", "exclude"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		if strings.ContainsAny(v, "<>") {
			delete(in.Slots, sk)
			drop(v)
			continue
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && (t.Raw == v || t.Fold == strings.ToLower(v)) {
				quoted = true
				break
			}
		}
		if quoted {
			continue
		}
		if strings.ContainsAny(v, "()") ||
			(strings.ContainsAny(v, "{}") &&
				!strings.Contains(v, "${")) {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// intents de contagem/fatia roubando pedido de diff ou contagem
	// de diretório — "number of different rows between F1 and F2" é
	// comm/diff; "number of sub-directories" é find|wc -l de dirs
	if in.Name == "wordcount_stats" || in.Name == "text_count" ||
		in.Name == "line_slice" || in.Name == "sort_op" ||
		in.Name == "list_files" || in.Name == "text_grep" {
		for i, t := range toks {
			switch t.Fold {
			case "different", "difference", "differ", "diff",
				"diferentes", "diferente", "diferenca", "diferença",
				"between", "compare", "comparing", "comparação",
				"comparacao", "indentical", "identical":
				// "between 5 and 10" é cue de faixa já coberta pelos
				// slots start/end — só dropa quando o que segue é um
				// segundo OPERANDO (comm/diff de dois arquivos)
				operand := false
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					if u.Filler {
						continue
					}
					if t.Fold == "between" && nuIsNumber(u.Fold) {
						break
					}
					if nuNameShaped(u.Raw) || nuFileShaped(u.Fold) ||
						u.Quoted {
						operand = true
						drop(t.Raw)
						drop(u.Raw)
					}
					break
				}
				if !operand && t.Fold != "between" {
					drop(t.Raw)
				}
			case "number", "count", "quantas", "quantos", "how":
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					if u.Filler {
						continue
					}
					switch u.Fold {
					case "directories", "directory", "dirs", "dir",
						"folders", "folder", "pastas", "pasta",
						"sub-directories", "subdirectories",
						"subdirs", "diretórios", "diretorios":
						drop(t.Raw)
						drop(u.Raw)
					}
					break
				}
			case "many":
				for j := i - 1; j >= 0 && j >= i-2; j-- {
					if toks[j].Fold == "how" {
						for k := j + 1; k < len(toks) && k <= j+4; k++ {
							u := &toks[k]
							if u.Filler {
								continue
							}
							switch u.Fold {
							case "directories", "directory", "dirs",
								"folders", "folder", "pastas",
								"sub-directories", "subdirectories",
								"subdirs", "diretórios", "diretorios":
								drop(toks[j].Raw)
								drop(t.Raw)
								drop(u.Raw)
							}
							break
						}
					}
					break
				}
			}
		}
	}
	// valor multi-palavra NÃO-aspado em slot de nome é resíduo de
	// phrasing, nunca um operando — "name 'of lines'" casa nada
	// (sucesso vazio), "name 'my key phrase'" aspado é literal real
	for _, sk := range []string{"name", "namepat", "name_re", "pat",
		"nametgt", "target", "file", "dep", "ext", "link", "arc",
		"out", "dir", "dest", "srcdir", "dir2", "path", "file2",
		"to", "from", "new", "old", "prefix", "to_ext",
		"a", "b"} {
		v := in.Slots[sk]
		if v == "" || !strings.Contains(v, " ") {
			continue
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && (t.Raw == v || t.Fold == v) {
				quoted = true
			}
		}
		if quoted {
			// string aspada com espaço pode ser literal com espaço
			// ("linkin park") OU sentença de print/echo ("$x is
			// empty", "Empty dir") — sentença tem verbo/var, payload
			// de print-verb nunca é basename
			nf := len(strings.Fields(v))
			sentence := nf >= 4 ||
				strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%") ||
				strings.Contains(v, " is ") || strings.Contains(v, " are ") ||
				strings.Contains(v, " was ") || strings.Contains(v, " were ") ||
				strings.Contains(v, " has ") || strings.Contains(v, " have ") ||
				strings.Contains(v, " will ") || strings.Contains(v, " not ") ||
				strings.Contains(v, " é ") || strings.Contains(v, " está ")
			if !sentence && nf >= 2 && !strings.ContainsAny(v, "/*") {
				for _, t := range toks {
					switch t.Fold {
					case "print", "echo", "printf", "say", "says",
						"escreva", "escrever", "imprime", "imprimir",
						"mostra", "exibe", "display", "displays":
						sentence = true
					}
				}
			}
			if sentence {
				delete(in.Slots, sk)
				drop(v)
			}
			continue
		}
		// "* *" = nome contém espaço — glob multi-palavra é semântica
		// real do find -name; path-glob "./es*" não é basename
		allGlob := true
		for _, wd := range strings.Fields(v) {
			if !strings.ContainsAny(wd, "*?[]") ||
				strings.Contains(wd, "/") {
				allGlob = false
			}
		}
		if allGlob {
			continue
		}
		// slots de path: resgata a ÚNICA palavra path-shaped — "files
		// that reside under /home/x" deixou "reside /home/x" no dir
		if sk == "dir" || sk == "dest" || sk == "srcdir" ||
			sk == "dir2" || sk == "path" {
			var keep string
			n := 0
			for _, wd := range strings.Fields(v) {
				if strings.Contains(wd, "://") {
					continue
				}
				if strings.ContainsAny(wd, "/~$") ||
					strings.HasPrefix(wd, ".") {
					keep = wd
					n++
				}
			}
			if n == 1 {
				for _, wd := range strings.Fields(v) {
					if wd != keep {
						drop(wd)
					}
				}
				in.Slots[sk] = keep
				continue
			}
		}
		delete(in.Slots, sk)
		for _, wd := range strings.Fields(v) {
			drop(wd)
		}
	}
	// vocábulo-padrão prefixando o padrão real — "match the regex
	// ^.*666$" capturou "regex ^.*666$"; "glob {EA,FS}_*" idem
	for _, sk := range []string{"name_re", "pattern", "query", "name",
		"namepat", "pat", "nametgt"} {
		v := in.Slots[sk]
		for _, pfx := range []string{"regex ", "regexp ", "glob ",
			"pattern ", "posix-egrep ", "ERE ", "BRE ", "egrep ",
			"the regex ", "the regexp ", "the pattern ",
			"the patterns ", "the word ", "the string ",
			"the regular expression ", "a regex ", "a regexp ",
			"a pattern ", "a word ", "a string ",
			"o regex ", "o padrão ", "o padrao ", "a palavra ",
			"o texto ", "a expressão ", "a expressao ",
			"expressão ", "expressao ", "padrão ", "padrao "} {
			if strings.HasPrefix(v, pfx) {
				v = strings.TrimPrefix(v, pfx)
				in.Slots[sk] = v
			}
		}
	}
	// brace-expansion/regex-meta em slot de GLOB é sintaxe que o
	// fnmatch não entende — "{EA,FS}_*" casaria o literal, não a
	// alternação. vírgula dentro de glob idem ("pl| *.pm")
	for _, sk := range []string{"name", "namepat", "pat", "nametgt",
		"ext"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		if strings.ContainsAny(v, "{}") ||
			strings.ContainsAny(v, "()") ||
			(strings.ContainsAny(v, "^$") && !strings.HasSuffix(v, "$")) ||
			(strings.Contains(v, ",") && sk != "ext") {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// comando/programa em slot de NOME é payload de exec que o find
	// não roda — awk/perl/python/sed mesmo aspado nunca é basename
	for _, sk := range []string{"name", "namepat", "name_re", "pat",
		"nametgt", "target", "file", "file2", "dep", "out", "arc",
		"link", "a", "b", "dest", "srcdir", "dir"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		if strings.Contains(v, ";") || strings.Contains(v, "$(") ||
			strings.Contains(v, "$0") || strings.Contains(v, "`") ||
			strings.HasPrefix(v, "{") {
			delete(in.Slots, sk)
			drop(v)
			continue
		}
		w1 := v
		if i := strings.IndexAny(w1, " \t"); i > 0 {
			w1 = w1[:i]
		}
		if w1 == v {
			continue // palavra única pode ser basename ("named test")
		}
		switch strings.ToLower(w1) {
		case "import", "from", "perl", "python", "python3", "ruby",
			"awk", "gawk", "sed", "bash", "sh", "node", "php",
			"xargs", "exec", "eval", "system",
			// comandos/utilitários — "print 'basename /file/path' for
			// each" colou o comando no slot de nome, não um basename
			"basename", "dirname", "md5sum", "sha1sum", "sha256sum",
			"sha512sum", "wc", "tr", "comm", "join", "cut",
			"paste", "uniq", "tee", "head", "tail", "cat", "cp",
			"mv", "rm", "ln", "mkdir", "rmdir", "touch", "tar",
			"zip", "unzip", "gzip", "gunzip", "bzip2", "xz",
			"locate", "which", "whereis", "stat", "du", "df",
			"ps", "kill", "killall", "top", "htop", "free",
			"uptime", "ping", "wget", "curl", "ssh", "scp",
			"rsync", "chmod", "chown", "chgrp", "sudo", "su",
			"cal", "bc", "expr", "seq", "timeout", "watch",
			"nl", "od", "strings", "column", "rev", "tac",
			"shuf", "env", "printenv", "sleep", "date", "time",
			"true", "false", "yes", "test", "id", "whoami",
			"uname", "hostname", "ls", "dir", "find", "grep",
			"fgrep", "egrep", "cmp", "diff", "patch", "mktemp",
			"readlink", "realpath", "mount", "umount", "fdisk":
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// vocábulo de papel em slot de dono/grupo — "group users have
	// full access" colou 'which'/'all' como nome de grupo
	for _, sk := range []string{"owner", "group", "src_owner",
		"src_group", "user"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		switch strings.ToLower(v) {
		case "which", "all", "group", "groups", "user",
			"owner", "owners", "everyone", "everybody", "anyone",
			"anybody", "someone", "somebody", "that", "what",
			"who", "whom", "whose", "full", "access",
			"permission", "permissions", "permissao", "usuário",
			"usuario", "usuarios", "usuários", "grupo", "grupos",
			"dono", "donos", "todos", "todas", "qualquer",
			"read", "write", "execute", "read-only", "writable",
			"readable", "executable", "the", "a", "an", "current",
			"logado", "logged", "this", "these", "those", "it",
			"its", "their", "theirs", "same", "other", "another",
			"each", "every", "any", "some", "no", "none",
			"but", "not", "for", "and", "or", "with", "without",
			"set", "unset", "by", "per", "only", "just", "mas",
			"para", "com", "sem", "have", "has", "had", "do",
			"does", "did":
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// referência a arquivo no slot de padrão — "search for the text
	// file 'file.txt'" tem ALVO arquivo, não padrão de conteúdo
	for _, sk := range []string{"pattern", "query", "nametgt"} {
		v := in.Slots[sk]
		for _, pfx := range []string{"the file ", "a file ",
			"the text file ", "o arquivo ", "um arquivo ",
			"the directory ", "o diretório ", "o diretorio "} {
			if strings.HasPrefix(strings.ToLower(v), pfx) {
				delete(in.Slots, sk)
				drop(v)
			}
		}
		// "for the text foo bar" — phrasing de busca apara, o
		// literal continua
		for _, pfx := range []string{"the text ", "a text ",
			"the word ", "a word ", "the string ", "a string ",
			"o texto ", "um texto ", "a palavra ", "o padrao ",
			"o padrão ", "a cadeia "} {
			if rest := strings.TrimPrefix(strings.ToLower(v), pfx); rest != strings.ToLower(v) {
				drop(strings.TrimSpace(v[:len(v)-len(rest)]))
				in.Slots[sk] = v[len(v)-len(rest):]
			}
		}
	}
	// slots de busca-textual (grep de símbolo, padrão de conteúdo)
	// guardam identificador único ou literal aspado — frase nua
	// ("Show manual page of find", "Prints between 1 10") é
	// pedido de man/shell/número roteado errado, não padrão
	for _, sk := range []string{"query", "pattern", "pat"} {
		q := in.Slots[sk]
		if !strings.Contains(q, " ") {
			continue
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && (t.Raw == q || t.Fold == q) {
				quoted = true
			}
		}
		if quoted {
			continue
		}
		words := strings.FieldsFunc(q, func(r rune) bool {
			return r == ' ' || r == '-'
		})
		bad := len(words) >= 3
		for _, w := range words {
			switch w {
			case "manual", "man", "page", "pages", "help",
				"info", "doc", "docs", "explanation",
				"options", "usage", "utility", "command",
				"commands", "exec", "sed", "awk", "grep",
				"regular", "expression", "regex", "regexp",
				"case", "insensitive", "sensitive", "hidden",
				"dot", "files", "file", "directories", "directory",
				"current", "changed", "modification",
				"modified", "stored", "browsers", "browser",
				"users", "logged", "sessions", "session",
				"pids", "pid", "videos", "video", "edit",
				"editing", "gimp", "open", "opening", "run",
				"running", "classes", "ignoring", "filename",
				"differs", "perform", "search", "searched",
				"using", "display", "view", "show", "list",
				"flash", "media", "audio", "images", "photos",
				"abstract", "find", "finds", "match",
				"matches", "matching", "to", "the", "a", "an",
				"of", "in", "on", "for", "with", "and", "or",
				"by", "all", "only", "which", "that", "having",
				"count", "counts", "number", "numbers", "random",
				"print", "prints", "between", "jobs", "job",
				"amount", "argument", "arguments", "bash",
				"form", "feed", "leaf", "inode", "word", "words",
				"get", "zeroes", "leading", "non", "zero",
				"from", "as", "is", "are", "was", "were",
				"does", "do", "not", "no", "be", "been",
				"it", "its", "this", "these", "those", "at",
				"whose", "who", "what", "when", "where", "how":
				bad = true
			}
		}
		if bad {
			delete(in.Slots, sk)
			drop(q)
		}
	}
	// metacaractere de código em padrão de CONTEÚDO — "$0"/"`"/"$("
	// são shell, nunca literal de grep
	for _, sk := range []string{"pattern", "query"} {
		v := in.Slots[sk]
		if v != "" && (strings.HasPrefix(v, "{") ||
			strings.Contains(v, "$(") || strings.Contains(v, "`") ||
			strings.Contains(v, "$0")) {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// âncora de linha/dois-pontos/aspas em slot de NOME é regex de
	// conteúdo roteado errado — "^Subject:.*unique" é header de
	// e-mail, não basename. "^test.*\.go$" em name_re é regex
	// legítimo (âncoras fazem parte do slot)
	for _, sk := range []string{"name", "namepat", "pat", "nametgt",
		"name_re", "target", "file", "file2", "link", "arc", "out",
		"dep", "dest", "a", "b"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		bad := strings.Contains(v, ":") || strings.Contains(v, "\"") ||
			strings.Contains(v, "'")
		if sk != "name_re" && strings.HasPrefix(v, "^") {
			bad = true
		}
		if bad {
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// padrão aspado dentro de cláusula 'except/excluding' é o
	// operando de EXCLUSÃO, não um name_re — "except files matching
	// regex 'exclude these'" deleta tudo MENOS o padrão
	for _, sk := range []string{"name_re", "name", "namepat", "pat",
		"nametgt", "pattern"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		qidx, isQ := -1, false
		for i, t := range toks {
			if t.Quoted && (t.Raw == v || t.Fold == v) {
				qidx, isQ = i, true
			}
		}
		if !isQ {
			continue
		}
		for j := qidx - 1; j >= 0 && j >= qidx-6; j-- {
			switch toks[j].Fold {
			case "except", "excluding", "exclude", "excluindo",
				"exceto", "menos", "without", "sem", "ignoring",
				"ignore", "ignorando", "outside", "apart":
				if in.Slots["exclude"] == "" {
					in.Slots["exclude"] = v
				}
				delete(in.Slots, sk)
			}
		}
	}
	// vocábulo-descritor como padrão literal — "whose pathnames match
	// X" grepava "pathnames", não o glob citado
	for _, sk := range []string{"pattern", "query", "nametgt"} {
		v := in.Slots[sk]
		if v == "" {
			continue
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && (t.Raw == v || t.Fold == v) {
				quoted = true
			}
		}
		if quoted {
			continue
		}
		switch strings.ToLower(v) {
		case "pathnames", "pathname", "filenames", "filename",
			"occurrence", "occurrences", "matches", "results",
			"output", "list", "listing", "the word", "the string",
			"the text", "the pattern", "the regex", "a word",
			"a string", "a pattern", "o texto", "a palavra",
			"o padrão", "o padrao", "text", "texto", "word",
			"palavra", "string", "content", "conteúdo",
			"conteudo", "phrase", "frase":
			delete(in.Slots, sk)
			drop(v)
		}
	}
	// name/dir de UMA letra ou estilo env-var é descriptor de contexto,
	// não pasta real — "in the X window system" não tem dir "X";
	// "in a TMPDIR folder" aponta a var de ambiente, não um literal
	for _, dk := range []string{"dir", "name"} {
		v := in.Slots[dk]
		if len(v) == 1 && v != "." && v != "/" && v != "~" ||
			dk == "dir" && len(v) > 2 && strings.ToUpper(v) == v &&
				strings.HasSuffix(v, "DIR") && !strings.HasPrefix(v, "$") {
			delete(in.Slots, dk)
			in.Dropped = append(in.Dropped, v)
		}
	}
	// conector/descritor de UMA palavra em slot de nome — "and then
	// send output" deixou "then" no name; "files named list" tem o
	// cue 'named' que prova o literal. Sem cue, é phrasing solto.
	cuedVals := map[string]bool{}
	for i, t := range toks {
		switch t.Fold {
		case "named", "called", "nomeado", "nomeada", "chamado",
			"chamada", "name", "nome", "names":
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				if toks[j].Filler {
					continue
				}
				cuedVals[strings.ToLower(toks[j].Raw)] = true
				cuedVals[toks[j].Fold] = true
				break
			}
		}
	}
	for _, sk := range []string{"name", "namepat", "pat", "nametgt",
		"target", "file", "dep", "ext", "link", "arc", "out",
		"file2", "a", "b", "not_owner", "owner", "group",
		"to", "from", "new", "old", "prefix"} {
		v := in.Slots[sk]
		if v == "" || strings.Contains(v, " ") {
			continue
		}
		lv := strings.ToLower(v)
		if cuedVals[lv] && (sk == "name" || sk == "namepat" ||
			sk == "pat" || sk == "nametgt") {
			continue
		}
		quoted := false
		for _, t := range toks {
			if t.Quoted && (t.Raw == v || t.Fold == lv) {
				quoted = true
				break
			}
		}
		if quoted {
			continue
		}
		switch lv {
		case "then", "also", "too", "instead", "afterward",
			"afterwards", "next", "lastly", "firstly", "once",
			"twice", "again", "later", "earlier", "previously",
			"meanwhile", "subsequently", "lines", "line", "output",
			"ouput", "result", "results", "parents", "regex",
			"regexp", "pattern", "patterns", "padrao", "padrão",
			"paths", "pathnames", "pathname", "number", "numbers",
			"count", "names", "list", "listing", "each", "some",
			"few", "several", "search", "searched", "searching",
			"enter", "entered", "feed", "fed", "redirect",
			"redirected", "calculate", "calculated", "upto",
			"print", "prints", "printed", "display", "displays",
			"displayed", "shown", "sends", "sent", "process",
			"processed", "processing", "sort", "sorted", "sorting",
			"bypassing", "ensuring", "maximum", "minimum",
			"writable", "writeable", "readable", "executable",
			"preceded", "followed", "matching", "matched",
			"existing", "remaining", "taken", "given", "looking",
			"residing", "located", "beneath", "ignoring",
			"excluding", "skipping", "within", "beginning",
			"ending", "starting", "contain", "containing", "type",
			"types", "information", "info", "strings",
			"characters", "copies", "backups", "symlinks",
			"entries", "things", "stuff", "contents", "access",
			"modes", "perm", "permissions", "permission",
			"world", "groups", "owners", "users", "global",
			"universal", "public", "private", "local",
			"standard", "regular", "normal", "plain", "common",
			"general", "default", "special", "specific",
			"certain", "particular", "following", "above",
			"below", "among", "during", "ago", "everywhere",
			"anywhere", "somewhere", "nowhere", "approximately",
			"roughly", "nearly", "almost", "basically", "simply",
			"merely", "otherwise", "rather", "quite", "even",
			"really", "actually", "both", "others", "another",
			"same", "such", "these", "those", "itself", "theirs",
			"everything", "anything", "something", "nothing",
			"whatever", "whichever", "either", "neither",
			"all", "every", "any", "none", "various",
			"multiple", "many", "most", "more", "less",
			"least", "much", "numerous", "countless",
			"single", "individual", "respective",
			"corresponding", "whole", "entire", "total",
			"full",
			"file", "files", "directory", "directories",
			"folder", "folders", "item", "items", "thing",
			"symlink", "nonempty", "non-empty", "hidden",
			"have", "has", "had", "do", "does", "did",
			"get", "gets", "got", "make", "makes", "made",
			"take", "takes", "put", "puts", "keep", "keeps",
			"let", "lets", "go", "goes", "went", "come",
			"comes", "came", "say", "says", "said", "tell",
			"tells", "told", "know", "knows", "knew", "see",
			"sees", "saw", "want", "wants", "wanted", "need",
			"needs", "needed", "try", "tries", "tried":
			delete(in.Slots, sk)
			drop(v)
		}
		// função-words/pronomes nunca são operando — "size between
		// 5kB and 10kB" não tem arquivo 'between', "files you
		// modified" não tem 'you'. ext/a/b isentos: .go/.so/.a e
		// "compare a b" são operandos reais de uma letra
		if sk != "ext" && sk != "a" && sk != "b" {
			switch lv {
			case "between", "be", "been", "being", "am", "is",
				"are", "was", "were", "you", "your", "yours",
				"we", "they", "them", "he", "she", "his",
				"her", "its", "our", "their", "i", "me",
				"us", "it", "can", "could", "will", "would",
				"shall", "should", "may", "might", "must",
				"not", "no", "and", "or", "but", "if",
				"then", "else", "than", "as", "so", "the",
				"a", "an", "to", "of", "in", "on", "at",
				"by", "for", "with", "from", "into",
				"about", "through", "here", "there",
				"this", "who",
				"whom", "whose", "what", "when", "where",
				"why", "how", "very", "just",
				"only", "also", "still", "yet", "now",
				"first", "last", "noone",
				"somebody", "anybody", "everybody",
				"anyone", "everyone", "someone",
				"de", "em", "para", "pra", "com", "por",
				"que", "qual", "quais", "onde", "quando",
				"como", "quanto", "quantos", "um", "uma",
				"uns", "umas", "o", "os", "dos", "das",
				"do", "da", "mas", "se", "nem", "ja",
				"já", "ainda", "sempre", "nunca", "todo",
				"toda", "todos", "todas", "cada", "outro",
				"outra", "mesmo", "mesma", "isso", "isto",
				"aquilo", "esse", "essa", "este", "esta",
				"e", "ou":
				delete(in.Slots, sk)
				drop(v)
			}
		}
	}
	// exclude com parêntese/conjunção colada é frase, não padrão —
	// "excluding './d1' (except './d1/s') and './d2'" nunca é glob
	if ev := in.Slots["exclude"]; ev != "" &&
		(strings.ContainsAny(ev, "()") ||
			strings.HasSuffix(ev, " and") ||
			strings.HasSuffix(ev, " or") ||
			strings.HasSuffix(ev, " e") ||
			strings.HasSuffix(ev, " ou")) {
		delete(in.Slots, "exclude")
		drop(ev)
	}
	// "without regards to case" — a cue `without` atrai `regards`
	// pra enum de exclusão; é phrasing de case, nunca operando
	if ev := in.Slots["exclude"]; strings.Contains(ev, "|") {
		var kept []string
		for _, p := range strings.Split(ev, "|") {
			switch p {
			case "regards", "regardless", "irrespective",
				"consideration", "account", "despite",
				"regard", "matter", "care":
				continue
			default:
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(in.Slots, "exclude")
		} else {
			in.Slots["exclude"] = strings.Join(kept, "|")
		}
	}
	// regex em slot glob: `[.]`, `\.`, `\{n,m}`, `^`/`$`/`.*` não
	// são glob — o padrão certo é name_re; sem ele o find casa o
	// literal e devolve vazio disfarçado de sucesso
	for _, gsk := range []string{"name", "namepat", "pat"} {
		gv := in.Slots[gsk]
		if gv == "" {
			continue
		}
		// ".*" isolado fica: como glob ele casa dotfiles (é o que o
		// mapa hidden produz); como regex casaria TUDO
		regexy := gv != ".*" && (strings.Contains(gv, "\\.") ||
			strings.Contains(gv, "[.]") ||
			strings.Contains(gv, "\\{") ||
			strings.Contains(gv, "\\(") ||
			strings.Contains(gv, "\\|") ||
			strings.HasPrefix(gv, "^") ||
			strings.HasPrefix(gv, ".*") ||
			strings.HasSuffix(gv, "$"))
		if !regexy {
			continue
		}
		// só file_find/delete_file têm name_re — nos demais o
		// regex migrado seria ignorado e a op rodaria sem o
		// filtro de nome
		if in.Name == "file_find" || in.Name == "delete_file" {
			if in.Slots["name_re"] == "" {
				in.Slots["name_re"] = gv
				delete(in.Slots, gsk)
				continue
			}
		}
		delete(in.Slots, gsk)
		drop(gv)
	}
	// "in the home directory" — home+cue de pasta resolve "~" (o
	// mesmo do user's home); sem ele o alvo nasce no cwd calado
	if in.Slots["dir"] == "" {
		for i, t := range toks {
			if t.Fold != "home" && t.Fold != "casa" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := toks[j]
				if u.Filler {
					continue
				}
				switch u.Fold {
				case "directory", "directories", "folder",
					"folders", "dir", "dirs", "pasta",
					"pastas", "diretorio", "diretório",
					"diretorios", "diretórios":
					in.Slots["dir"] = "~"
				}
				break
			}
			break
		}
	}
	// "on host node"/"via ssh no servidor" — remoto não é suportado;
	// ignorar o destino e executar LOCAL é falso-sucesso clássico.
	// "rename host to server" não casa: `to` é prep de direção, e
	// host/server sem prep de lugar são nomes legítimos. Intents do
	// domínio host/serviço têm ssh/host/machine como vocábulo, não
	// spec remoto — o guard não roda neles.
	hostDomain := in.Name == "connect_host" || in.Name == "host_status" ||
		in.Name == "list_hosts" || in.Name == "logged_in" ||
		in.Name == "net_ifaces" || in.Name == "net_route" ||
		in.Name == "net_status" || in.Name == "file_users" ||
		strings.HasPrefix(in.Name, "service_")
	remoteVocab := false
	hostIdx := -1
	if !hostDomain {
		for i, t := range toks {
			switch t.Fold {
			case "remote", "remoto", "remota":
				remoteVocab = true
			case "ssh", "host", "hosts", "node", "nodes", "server",
				"servers", "machine", "machines", "servidor",
				"servidores", "maquina", "máquina":
				demo := false
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					u := toks[j]
					// demonstrativo/possessivo entre a prep e
					// a palavra-host: "on this machine" é
					// localhost, não spec remoto
					switch u.Fold {
					case "this", "these", "my", "your", "our",
						"local", "localhost", "current",
						"atual", "esse", "essa", "este",
						"esta", "isso", "isto", "minha",
						"meu", "nossa", "nosso", "que",
						"qual", "quais":
						demo = true
						continue
					}
					switch u.Fold {
					case "on", "at", "em", "no", "na", "via",
						"ssh", "remote", "remoto":
						if !demo {
							remoteVocab = true
							if hostIdx < 0 {
								hostIdx = i
							}
						}
					}
					if u.Filler {
						continue
					}
					break // primeira palavra-conteúdo decide
				}
			}
		}
	}
	if remoteVocab {
		for i, t := range toks {
			switch t.Fold {
			case "on", "at", "via", "host", "hosts",
				"node", "nodes", "server", "servers",
				"machine", "machines",
				"ssh", "remote", "remoto", "remota",
				"servidor", "servidores", "maquina",
				"máquina":
				if t.Raw != "" {
					drop(t.Raw)
				} else {
					drop(t.Fold)
				}
			default:
				// o nome do host depois do cue ("on host
				// node") também é spec remoto
				if i == hostIdx+1 || (i > hostIdx && i <= hostIdx+2 && toks[i-1].Filler) {
					if hostIdx >= 0 && !t.Filler {
						if t.Raw != "" {
							drop(t.Raw)
						}
					}
				}
			}
		}
	}
	// srcs é lista de paths separada por espaço — verbo/classe entre
	// eles é phrasing do pedido, não fonte ("archive files that
	// exist" não tem fonte chamada "exist")
	if sv := in.Slots["srcs"]; strings.Contains(sv, " ") {
		var kept []string
		for _, wd := range strings.Fields(sv) {
			switch strings.ToLower(wd) {
			case "if", "create", "creates", "creating", "created",
				"existing", "updating", "update", "updates",
				"one", "last", "block", "regular",
				"non-compressed", "extensions", "extension",
				"first", "then", "also", "too", "all", "every",
				"each", "file", "files", "directory",
				"directories", "folder", "folders", "the", "a",
				"an", "of", "in", "on", "at", "to", "for",
				"with", "without", "and", "or", "but", "as",
				"by", "is", "are", "was", "were", "be", "been",
				"it", "its", "this", "that", "these", "those",
				"new", "old", "same", "other", "another",
				"more", "most", "less", "least", "found",
				"located", "residing", "matching", "containing",
				"named", "called", "using", "keep", "keeped",
				"kept", "compress", "compressed", "compressing",
				"archive", "archives", "archived", "archiving":
				drop(wd)
			default:
				kept = append(kept, wd)
			}
		}
		if len(kept) == 0 {
			delete(in.Slots, "srcs")
		} else {
			in.Slots["srcs"] = strings.Join(kept, " ")
		}
	}
	// "files ending with undo" — undo é operando-nome ali, não o
	// verbo: undo.last restauraria o checkpoint sem o pedido pedir.
	// Contexto de arquivo/busca junto ao intent undo denuncia o
	// roteamento errado
	if in.Name == "undo" || in.Name == "redo" {
		fileCtx := false
		for _, t := range toks {
			// só phrasing de BUSCA conta — "reverta o arquivo
			// parser.go" é undo legit com nome de arquivo
			switch t.Fold {
			case "ending", "ends", "named", "match",
				"matching", "pattern", "glob",
				"search", "find", "size", "calculate",
				"sum", "total", "count", "suffix",
				"containing", "starts", "starting", "prefix",
				"files", "directories", "folders":
				fileCtx = true
			}
		}
		if fileCtx {
			for _, t := range toks {
				switch t.Fold {
				case "undo", "redo", "desfaz", "desfazer",
					"reverte", "reverter":
					if t.Raw != "" {
						drop(t.Raw)
					} else {
						drop(t.Fold)
					}
				}
			}
		}
	}
	// "UID=1000"/"user=wnj" como owner/group: o rótulo é o TIPO do
	// operando, o valor é o id/nome — find -user aceita o número,
	// o rótulo grudado falharia quieto no runtime
	for _, osk := range []string{"owner", "group", "not_owner"} {
		ov := in.Slots[osk]
		for _, pfx := range []string{"uid=", "gid=", "user=",
			"group=", "uid:", "gid:"} {
			if strings.HasPrefix(strings.ToLower(ov), pfx) {
				in.Slots[osk] = ov[len(pfx):]
				break
			}
		}
	}
	// "specified by variable X" torna a fonte uma indireção que o
	// runtime não resolve — archive da variável literal mutaria
	// o alvo errado (ou o cwd inteiro quando '.' vaza junto)
	if in.Slots["srcs"] != "" {
		for _, t := range toks {
			if t.Fold == "variable" || t.Fold == "variavel" ||
				t.Fold == "variável" {
				drop(in.Slots["srcs"])
				delete(in.Slots, "srcs")
				break
			}
		}
	}
	// name colado com verbo/substantivo de descrição é sentença
	// explicativa, não basename: "Creates temporary tmp.XXX" descreve
	// o mktemp; "window double-clicked terminal window" descreve a
	// GUI. Vai pro Dropped em vez de procurar arquivo-sentença.
	if nv := in.Slots["name"]; strings.Contains(nv, " ") {
		descGlue := false
		fields := strings.Fields(strings.ToLower(nv))
		for _, f0 := range fields {
			switch f0 {
			case "creates", "create", "creating", "temporary", "temp",
				"window", "windows", "double-clicked", "clicked",
				"selected", "terminal", "system", "ie", "eg",
				"sources", "source", "returns", "return", "read",
				"reads", "get", "gets", "line", "descriptor",
				"arguments", "argument", "stack", "input", "store",
				"received", "variable", "seconds", "half", "sleep":
				descGlue = true
			}
		}
		// "get PATH"/"fetch NAME" — verbo-fetch + operando único é
		// phrasing do próprio pedido; apara o verbo e fica o nome
		if descGlue && len(fields) == 2 {
			switch fields[0] {
			case "get", "gets", "fetch", "pega", "mostra", "show":
				in.Slots["name"] = strings.Fields(nv)[1]
				descGlue = false
			}
		}
		if descGlue {
			in.Dropped = append(in.Dropped, nv)
			delete(in.Slots, "name")
		}
	}
	// perm_set sem payload (sem mode/owner/group/sym*) já roteia
	// pro método 'perm_set.ask' — reporta needs_input honesto em
	// vez de mutar. Não precisa de guard de slot aqui.
	// verbo de aritmética é segunda operação não suportada — "sum
	// each line in infile" não é file_find de arquivo chamado infile
	if in.Name == "file_find" || in.Name == "list_files" {
		for _, t := range toks {
			switch t.Fold {
			case "sum", "total", "totals", "average", "multiply",
				"mathematically", "somar", "soma", "media",
				"média", "multiplicar":
				in.Dropped = append(in.Dropped, t.Raw)
			}
		}
	}
	// "sort strings by second field"/"by the end field" — sort -k por
	// campo não é suportado por nenhum método de linha; o operando de
	// campo vai pro Dropped em vez de fingir um slice.
	if in.Name == "line_slice" || in.Name == "wordcount_stats" ||
		in.Name == "text_count" {
		// 'file' que é comando+args é entrada-pipe ("seq 1 10000"),
		// não path; 'file' de pontuação solta não é nome
		if fv := in.Slots["file"]; fv != "" {
			cmdish := false
			if strings.Contains(fv, " ") && !strings.ContainsAny(fv, "/~") {
				fw := strings.ToLower(strings.Fields(fv + " x")[0])
				for _, c := range []string{"seq", "echo", "cat", "ls",
					"find", "grep", "head", "tail", "sort", "wc",
					"printf", "print", "date", "who", "ps", "tr",
					"comm", "diff", "join"} {
					if fw == c {
						cmdish = true
					}
				}
			}
			punct := fv != "." && fv != ".." && fv != "~" &&
				strings.IndexFunc(fv, unicode.IsLetter) < 0 &&
				strings.IndexFunc(fv, unicode.IsDigit) < 0
			if cmdish || punct {
				delete(in.Slots, "file")
				drop(fv)
			}
		}
		// args de processo ("command line args passed to a running
		// process") não é fatia de arquivo
		for _, t := range toks {
			switch t.Fold {
			case "args", "arguments", "argv", "passed":
				drop(t.Raw)
			}
		}
		for i, t := range toks {
			switch t.Fold {
			case "field", "fields", "campo", "campos", "column",
				"columns", "coluna", "colunas":
				in.Dropped = append(in.Dropped, t.Raw)
				if i > 0 {
					switch toks[i-1].Fold {
					case "second", "third", "fourth", "fifth", "the",
						"from", "segundo", "terceiro", "end", "last":
						in.Dropped = append(in.Dropped, toks[i-1].Raw)
					}
				}
			}
		}
	}
	// "unique lines of A compared to B" é comm(1), não sort -u —
	// o 'to' capturou o segundo arquivo como se fosse destino.
	if in.Name == "sort_op" {
		for i, t := range toks {
			switch t.Fold {
			case "compared", "compare", "comparison", "comparado",
				"comparada", "comparando", "versus", "vs":
				drop(t.Raw)
				for _, k := range []string{"to", "to_file", "file2"} {
					if v := in.Slots[k]; v != "" {
						delete(in.Slots, k)
						drop(v)
					}
				}
				_ = i
			}
		}
	}
	// valor vivo num slot não é operando-rejeitado: a poda de um slot
	// duplicado ("dir=x" quando from já tem "x") não pode derrubar a
	// confiança do slot irmão que cobre o mesmo literal
	liveVals := map[string]bool{}
	for _, v := range in.Slots {
		liveVals[v] = true
	}
	keptDrops := in.Dropped[:0]
	for _, d := range in.Dropped {
		if !liveVals[d] {
			keptDrops = append(keptDrops, d)
		}
	}
	in.Dropped = keptDrops
	// operando semântico em Dropped = o pedido carrega restrição que
	// nenhum slot cobriu — reportar sucesso seria fingir cobertura.
	// (não dobra com unfed: drop costuma ser vocábulo de papel, fora
	// da contagem de palavra livre)
	if len(in.Dropped) > 0 {
		conf *= 0.45
		in.Evidence = append(in.Evidence,
			fmt.Sprintf("operando sem cobertura de slot: %s",
				strings.Join(in.Dropped, ", ")))
	}
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
			if in.Slots["type"] == "dir" || in.Slots["type"] == "directory" {
				continue // "mkdir data": nome de pasta não precisa de forma de arquivo
			}
			conf *= 0.6 // "escreve um poema": no file name, no cue word
			in.Evidence = append(in.Evidence, fmt.Sprintf("'%s' não parece nome de arquivo", v))
		}
	}
	if res := m.residualCount(toks, used, taken); res >= 3 {
		conf *= 0.6 // a long free-form request is a job for the model, not a template
		in.Evidence = append(in.Evidence, fmt.Sprintf("%d palavras fora da gramática: pedido livre", res))
	}
	// "unique lines in file_a AND file_b": um segundo operando-arquivo
	// órfão num intent de arquivo único rodaria só no primeiro = falso
	// sucesso. O método não honra multi-entrada → clarify honesto.
	if _, single := def.Slots["file"]; single && in.Slots["file"] != "" {
		if _, hasList := def.Slots["files"]; !hasList {
			fed := map[string]bool{}
			for _, v := range in.Slots {
				fed[v] = true
			}
			for i := range toks {
				if taken[i] || toks[i].Filler {
					continue
				}
				t := &toks[i]
				if fed[t.Fold] || fed[t.Raw] {
					continue // o token já alimentou um slot (file, dir, out…)
				}
				orphan := t.has("filequoted") || nuFileShaped(t.Fold)
				if !orphan && t.Quoted &&
					!t.has("forquoted") && !t.has("nametgt") && !t.has("outtgt") {
					orphan = true // aspa solta sem papel = 2º operando
				}
				if orphan {
					conf = math.Min(conf, 0.3)
					in.Evidence = append(in.Evidence,
						"segundo arquivo citado não cabe neste método (arquivo único)")
					break
				}
			}
		}
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
	"split": true,
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
	// a purely numeric slot takes digits only — a name-shaped leftover
	// ("regular/normal") is never a limit or a port
	if (kinds["number"] || kinds["port"]) && !kinds["text"] {
		return nuIsNumber(t.Fold)
	}
	fileish := kinds["file"] || kinds["test"] || kinds["dir"] || kinds["symbol"]
	// a pure extension word (".conf") is a filter, never a file/dir name —
	// "arquivos .conf" describes a set of files, not a directory ".conf"
	if fileish && nuPureExt(t.Raw) {
		return false
	}
	if t.Quoted {
		if fileish {
			// aspa multi-palavra é TEXTO ('The quick brown fox'), não
			// nome de arquivo — mesmo citada entre aspas
			return !strings.Contains(t.Raw, " ")
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
	// palavra-com-conceito-forte é vocabulário ("have", "sorted"), nunca
	// nome — conceito fraco (stem) ou de reparo de typo não conta: "parsr"
	// ainda pode fuzar parser.go
	if len(t.Concs) > 0 && !t.Weak && t.Fixed == "" {
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
	// ".c"/".h" têm 2 chars mas são extensões de verdade — exigir len>=3
	// deixava ".c files" escapar pra dir/name
	if len(raw) < 2 || raw[0] != '.' {
		return false
	}
	if len(raw) == 2 && !nuExtWords[raw[1:]] {
		return false
	}
	for _, r := range raw[1:] {
		// ".tar-gz"/".tar-bz2" — ext composta com hífen continua ext
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
			return false
		}
	}
	return true
}

// nuFileShaped: "a.txt"/"config.ini" — nome com sufixo de extensão
// conhecida. Diferente de nuNameShaped (qualquer token com ponto): aqui o
// sufixo precisa ser uma extensão do vocabulário, então "release.2" e
// "my.dir" continuam podendo nomear diretórios.
func nuFileShaped(fold string) bool {
	// "file1"/"arquivo2" — file+dígitos é nome de arquivo de corpus,
	// nunca diretório nem palavra solta
	for _, p := range []string{"file", "arquivo", "fichero"} {
		if rest := strings.TrimPrefix(fold, p); rest != fold && rest != "" {
			digits := true
			for _, r := range rest {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if digits {
				return true
			}
		}
	}
	dot := strings.LastIndexByte(fold, '.')
	if dot <= 0 || dot == len(fold)-1 {
		return false
	}
	return nuExtWords[fold[dot+1:]]
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
		// um glob consumido pela regra ainda é material de nome:
		// "files matching *.log" — o conceito classifica, o token é
		// o padrão; sem isso o slot name fica vazio
		if !explained || t.has("glob") {
			out = append(out, i)
		}
	}
	return out
}

func (m *nuModel) residualCount(toks []nuTok, used map[string]bool, taken map[int]bool) int {
	n := 0
	for _, i := range m.residual(toks, used) {
		if taken[i] {
			continue // a slot já consumiu esta palavra — não é fora-da-gramática
		}
		if len(toks[i].Tags) == 0 && !toks[i].Quoted && len(toks[i].Concs) == 0 {
			n++
		}
	}
	return n
}

var nuToWords = map[string]bool{"para": true, "pra": true, "pro": true, "to": true, "por": true, "->": true, "=>": true, "em": true}

// timeWords: unidades e âncoras de janela de tempo — "3 days ago" é
// filtro temporal, nunca nome de diretório nem arquivo-referência.
var timeWords = map[string]bool{
	"last": true, "past": true, "next": true, "previous": true,
	"future": true, "recent": true, "today": true, "yesterday": true,
	"tomorrow": true, "ago": true, "week": true, "weeks": true,
	"month": true, "months": true, "year": true, "years": true,
	"day": true, "days": true, "hour": true, "hours": true,
	"minute": true, "minutes": true, "second": true, "seconds": true,
	"fortnight": true, "fortnights": true,
	"quinzena": true, "quinzenas": true,
	"minuto": true, "minutos": true, "hora": true, "horas": true,
	"dia": true, "dias": true, "semana": true, "semanas": true,
	"mes": true, "mês": true, "meses": true, "ano": true, "anos": true,
	// âncoras PT — "no log de hoje" qualifica QUANDO, nunca é pasta
	"hoje": true, "ontem": true, "amanha": true, "amanhã": true,
	"anteontem": true, "agora": true,
}

// nuNumTok: o token é quantidade — dígito ("365"), palavra-número
// ("one"/"vinte") ou produto escrito ("365*24"). Número com letra
// colada ("10k") não entra.
func nuNumTok(fold string) bool {
	if nuIsNumber(fold) {
		return true
	}
	if _, ok := nuNumWord(fold); ok {
		return true
	}
	if strings.Count(fold, "*") >= 1 {
		for _, p := range strings.Split(fold, "*") {
			if !nuIsNumber(p) {
				return false
			}
		}
		return true
	}
	return false
}

// nuCaseFlags: modificadores de comparação ("any-cased 'error'") —
// nunca nome de arquivo, e transparentes pro lookahead do forquoted.
// nuNoRepair: palavras inglesas reais que o typo-repair destruiria —
// 'separated'→'separator' apaga o particípio ("separated by newlines"),
// 'spaces'→'specs' apaga o sentido de nome-com-espaço do find.
var nuNoRepair = map[string]bool{
	"separated": true, "spaces": true, "instead": true,
	"forward": true, "backwards": true, "newlines": true,
	"records": true, "entries": true,
}

// nuCountNouns: substantivos de contagem que nunca viram conteúdo de um
// pattern — "count matches/times" fala da métrica, não do texto buscado.
var nuCountNouns = map[string]bool{
	"match": true, "matches": true, "time": true, "times": true,
	"vez": true, "vezes": true, "occurrence": true, "occurrences": true,
	"ocorrencia": true, "ocorrência": true, "aparicao": true,
	"aparição": true, "aparicoes": true, "aparições": true,
	"number": true, "numero": true, "número": true, "count": true,
}

// nuPermBitToken: "g+w"/"o+w"/"u+x"/"a-r" — sintaxe simbólica de
// permissão, nunca nome de arquivo/dir. "missing g+w" é predicado de
// modo negado, não name.
func nuPermBitToken(f string) bool {
	_, ok := nuPermBitMask(f)
	return ok
}

// nuPermBitMask: "g+w"→020, "ug+x"→110, "a-r"→444 — classes antes de
// +/- somam os blocos octal das letras de modo depois.
func nuPermBitMask(f string) (int, bool) {
	if len(f) < 3 || len(f) > 7 {
		return 0, false
	}
	i := strings.IndexAny(f, "+-")
	if i < 1 || i >= len(f)-1 {
		return 0, false
	}
	mask := 0
	for _, c := range f[i+1:] {
		var bit int
		switch c {
		case 'r':
			bit = 4
		case 'w':
			bit = 2
		case 'x':
			bit = 1
		default:
			return 0, false
		}
		for _, k := range f[:i] {
			switch k {
			case 'u':
				mask |= bit << 6
			case 'g':
				mask |= bit << 3
			case 'o':
				mask |= bit
			case 'a':
				mask |= bit * 0o111
			default:
				return 0, false
			}
		}
	}
	return mask, true
}

// nuFormatWords: colunas de saída pedidas junto ao find — "with their
// timestamp and paths" descreve o LISTING desejado, nunca um nome de
// arquivo ou diretório. Auxiliares verbais ("been modified") idem.
var nuFormatWords = map[string]bool{
	"timestamp": true, "timestamps": true, "paths": true,
	"attributes": true, "sizes": true, "dates": true,
	"permissions": true, "owners": true,
	"been": true, "being": true, "sido": true, "modified": true,
	"modificado": true, "modificada": true,
	// auxiliar verbal solto é phrasing de janela/cláusula — "that
	// were modified less than..." não tem name "were"
	"were": true, "was": true, "are": true, "is": true,
	// coluna de exibição / escopo de filesystem — "their modification
	// time", "disk partition": phrasing, nunca nome de arquivo
	"modification": true, "modificacao": true, "modificação": true,
	"access": true, "acesso": true, "time": true, "tempo": true,
	"disk": true, "disks": true, "disco": true,
	"partition": true, "partitions": true, "particao": true,
	"partição": true, "volume": true, "volumes": true,
	"filesystem": true, "mount": true, "mounts": true,
	// "top-level subfolders" / "sorted by number of contained files":
	// profundidade e contagem são predicados, não basename
	"top": true, "top-level": true, "toplevel": true,
	"level": true, "levels": true, "nivel": true, "nível": true,
	"number": true, "numero": true, "número": true, "count": true,
	"contained": true, "contido": true, "contida": true,
	"subfolder": true, "subfolders": true, "subdirectory": true,
	"subdirectories": true, "subdir": true, "subdirs": true,
	"prints": true, "listing": true, "list": true,
	// tipo de arquivo e phrasing de filtro — "normal/regular files"
	// descreve o tipo (não o nome); "long file listing" é o formato;
	// "including hidden ones" introduz o conjunto
	"normal": true, "regular": true, "including": true,
	"long": true, "hidden": true, "ones": true,
	"invisible": true, "oculto": true, "oculta": true,
	"ocultos": true, "ocultas": true, "escondido": true,
	"escondida": true, "escondidos": true, "escondidas": true,
	// predicado de conjunto vazio — "empty directories" não tem name
	// "empty" (o slot empty já cobre; um nome literal vem só via
	// nametgt "named empty")
	"empty": true, "vazio": true, "vazia": true,
	"vazios": true, "vazias": true,
	// auxiliar negado/contraído é phrasing de filtro, nunca nome:
	// "that doesn't contain 'bin'" não tem name "doesn"
	"doesn": true, "doesnt": true, "dont": true, "don": true,
	"didn": true, "didnt": true, "isn": true, "isnt": true,
	"aren": true, "arent": true, "wasn": true, "wasnt": true,
	"weren": true, "werent": true, "haven": true, "havent": true,
	"hasn": true, "hasnt": true, "hadn": true, "hadnt": true,
	"won": true, "wont": true, "wouldn": true, "wouldnt": true,
	"couldn": true, "couldnt": true, "shouldn": true, "shouldnt": true,
	"mustn": true, "mustnt": true, "can": true, "cannot": true,
	// sujeito possessivo é escopo, não nome: "in user's home" não tem
	// name "user home" — o dir_cued resolve "~"
	"home": true, "casa": true,
	// "whose inode has been modified" — metadado de filtro, nunca nome;
	// "no later than" é cue temporal; "system" em "file system" é
	// escopo-resolvido (nuFilesystemAt) mas seguro como phrasing solto
	"inode": true, "inodes": true, "later": true, "system": true,
	// superlativo/conjunção/campo de sort nunca é nome: "but 5 newest
	// ones" é exceção temporal; "by second field" é cláusula sort -k
	// (não suportada) — nenhum dos dois vira basename livre
	"newest": true, "oldest": true, "but": true, "mas": true,
	"ignoring": true, "ignore": true, "ignorando": true,
	"field": true, "fields": true, "campo": true, "campos": true,
	"column": true, "columns": true, "coluna": true, "colunas": true,
	"second": true, "third": true, "fourth": true, "fifth": true,
	"primeiro": true, "primeira": true, "segundo": true, "segunda": true,
	"terceiro": true, "terceira": true,
	// "atleast"/"least"/"below N levels" — quantificador de
	// profundidade colado ou solto, nunca basename
	"atleast": true, "least": true, "below": true, "abaixo": true,
	// adjetivo de escopo vago é phrasing de local, nunca parte de um
	// glob: "in the specified directory" não tem name "specified"
	// (typo "specfied" chega sem reparo — o fold fica sujo)
	"specified": true, "specfied": true, "specfic": true,
	"specific": true, "certain": true, "particular": true,
	"given": true, "determinado": true, "determinada": true,
	"especificado": true, "especificada": true,
	"especifico": true, "específico": true,
	// metainfo do pedido, nunca operando: "where $V is a variable",
	// "sorted based on name" — a cola 'variable'/'based' não é
	// parte do glob nem do diretório
	"variable": true, "variavel": true, "variável": true,
	"variaveis": true, "variáveis": true, "based": true,
	"baseado": true, "baseada": true,
	// condicional/direcional é phrasing da restrição, nunca nome:
	// "following symlinks if needed" não tem name "if"; "pointing to
	// anywhere within /mnt" não tem name "pointing anywhere"
	"if": true, "needed": true, "necessario": true, "necessário": true,
	"necessaria": true, "preciso": true, "precisa": true,
	"pointing": true, "targeting": true, "aiming": true,
	"apontando": true, "anywhere": true, "anyplace": true,
	"everywhere": true, "somewhere": true,
	// abreviação de exemplo é phrasing, nunca nome: "(ie. X)"/"etc."
	"ie": true, "eg": true, "etc": true, "ex": true,
}

// nuAllFormatWords: alternância "normal/regular"/"files-or-dirs" — todo
// pedaço é phrasing, então o token inteiro é phrasing (não vira name).
func nuAllFormatWords(fold string) bool {
	if !strings.Contains(fold, "/") {
		return false
	}
	for _, p := range strings.Split(fold, "/") {
		if p != "" && !nuFormatWords[p] && !nuArcTypeWords[p] {
			return false
		}
	}
	return true
}

// nuToolWords: nome de ferramenta/flag de comando e verbo de exibição —
// "awk used here for print" descreve COMO o usuário faria na mão, nunca
// um nome de arquivo ou pasta. Cobre os vazamentos tipo dir:"-exec" e
// name:"will output awk used".
var nuToolWords = map[string]bool{
	"awk": true, "sed": true, "xargs": true, "exec": true,
	"find": true, "grep": true, "egrep": true, "fgrep": true,
	"will": true, "would": true, "should": true, "shall": true,
	"output": true, "print": true, "printf": true, "print0": true,
	"display": true, "displaying": true, "details": true,
	"discard": true, "discarding": true, "used": true, "using": true,
	"only": true, "just": true, "somente": true, "here": true,
	"command": true, "commands": true, "comando": true, "comandos": true,
}

// nuFileCueWords: substantivo-cue de arquivo/pasta — antes de um nome,
// marca operando de arquivo, nunca nome de grupo/dono nem diretório.
var nuFileCueWords = map[string]bool{
	"file": true, "files": true, "arquivo": true, "arquivos": true,
	"folder": true, "folders": true, "pasta": true, "pastas": true,
	"directory": true, "directories": true, "diretorio": true,
	"diretório": true, "diretorios": true, "diretórios": true,
	"dir": true, "dirs": true,
}

// nuSizeCueWords: morfologia do predicado de tamanho — "size greater than
// 100 bytes" é filtro de min_size, o sponge de nome não pode comer
// "size greater" como se fosse basename.
var nuSizeCueWords = map[string]bool{
	"size": true, "sized": true, "tamanho": true,
	"bytes": true, "byte": true, "kilobytes": true, "kb": true,
	"megabytes": true, "mb": true, "gigabytes": true, "gb": true,
	"greater": true, "greatest": true, "larger": true, "largest": true,
	"bigger": true, "biggest": true, "smaller": true, "smallest": true,
	"least": true, "most": true, "over": true, "above": true,
	"below": true, "under": true, "exceeding": true, "longer": true,
	"maior": true, "maiores": true, "menor": true, "menores": true,
	"menos": true, "acima": true, "abaixo": true,
}

var nuCaseFlags = map[string]bool{
	"case-insensitive": true, "insensitive": true, "case-sensitive": true,
	"caseinsensitive": true, "sensitive": true, "any-cased": true,
	"anycase": true, "any-case": true, "nocase": true, "caseless": true,
	"insensitively": true, "sensitively": true, "case-insensitively": true,
	// "case" solto: "(case insensitive)" tokeniza em duas palavras e o
	// "case" virava operando de nome/dir (delete_file name:case — deleção
	// no literal). "in this case" já está no notDirWords do dir_cued.
	"case": true, "cases": true,
	// PT: "sem diferenciar maiúsculas de minúsculas"
	"maiúsculas": true, "minúsculas": true, "maiúscula": true,
	"minúscula": true, "maiusculas": true, "minusculas": true,
	"maiúsculo": true, "minúsculo": true,
	"capitalizado": true, "capitalizada": true,
}

// fill extracts the slots of def. It also counts unfed — free words a
// world-kind slot refused — so Parse can lower confidence instead of
// inventing values. A refused word only counts when the slot needed it
// (required, or a service/host kind — a unit is never a leftover noun)
// and the word carries no concept of its own: "arquivo", "zip" and
// "conteúdo" are phrasing, not failed names.
func (m *nuModel) fill(def *nuIntentDef, toks []nuTok, hits []nuHit, used map[string]bool, w *nuWorld) (map[string]string, []string, string, int, map[int]bool) {
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
			case sd.From == "rename:old" || sd.From == "rename:new":
				// "rename them by replacing 'a' with 'b'" — o par de
				// substituição só existe com verbo de troca explícito;
				// "rename png to jpg" (to/intocue) não é replace.
				// O 'by' que introduz o MÉTODO não é separador — o split
				// é o 'with/por' DEPOIS do verbo de troca.
				wantNew := sd.From == "rename:new"
				verbAt := -1
				for i, t := range toks {
					switch t.Fold {
					case "replace", "replaces", "replacing", "substitute",
						"substituting", "swap", "swapping", "substitui",
						"substituindo", "substitua", "troca", "trocando":
						if verbAt < 0 {
							verbAt = i
						}
					}
				}
				if verbAt >= 0 {
					sep := -1
					oldAt, newAt := -1, -1
					for i := verbAt + 1; i < len(toks); i++ {
						f := toks[i].Fold
						if f == "with" || f == "por" || f == "com" ||
							f == "para" || f == "into" {
							if sep < 0 && oldAt >= 0 {
								sep = i
							}
							continue
						}
						if toks[i].Filler || toks[i].has("article") ||
							toks[i].has("nameattr") || toks[i].has("content") {
							continue
						}
						if f == "and" || f == "e" || f == "or" || f == "in" ||
							f == "em" || f == "under" || f == "on" {
							break
						}
						if oldAt < 0 {
							oldAt = i
						} else if sep >= 0 && newAt < 0 {
							newAt = i
							break
						}
					}
					if wantNew && newAt >= 0 {
						v = toks[newAt].Raw
						taken[newAt] = true
					} else if !wantNew && oldAt >= 0 {
						v = toks[oldAt].Raw
						taken[oldAt] = true
					}
				}
			case sd.From == "rename:prefix":
				// "rename them prefixing 'v1'"/"prefixing names with v1"
				for i, t := range toks {
					if t.Fold != "prefix" && t.Fold != "prefixing" &&
						t.Fold != "prefixed" && t.Fold != "prefixo" {
						continue
					}
					for j := i + 1; j < len(toks) && j <= i+5; j++ {
						u := &toks[j]
						if u.Filler || u.has("nameattr") || u.has("article") ||
							u.Fold == "with" || u.Fold == "com" {
							continue
						}
						if u.Fold == "and" || u.Fold == "e" || u.Fold == "or" {
							break
						}
						v = u.Raw
						taken[i], taken[j] = true, true
						break
					}
					break
				}
			case sd.From == "rename:to_ext" || sd.From == "rename:ext":
				// "rename png files to jpg files": ext = extcue ANTES do
				// to; to_ext = extcue depois — os dois lados do swap
				mark := -1
				for i := range toks {
					if nuToWords[toks[i].Fold] && i > 0 {
						mark = i
					}
				}
				if sd.From == "rename:to_ext" {
					for i := mark + 1; mark >= 0 && i < len(toks); i++ {
						if toks[i].has("extcue") {
							v = toks[i].Fold
							taken[i] = true
							break
						}
					}
				} else {
					for i := 0; mark >= 0 && i < mark; i++ {
						if toks[i].has("extcue") {
							v = toks[i].Fold
							taken[i] = true
							break
						}
					}
				}
			case sd.From == "repl_from" || sd.From == "repl_to":
				v = nuReplacePart(toks, res, sd.From == "repl_to", taken)
			case strings.HasPrefix(sd.From, "repl:"):
				v = nuReplFlag(toks, sd.From[5:], taken)
			case strings.HasPrefix(sd.From, "find:"):
				v = nuFindArg(toks, sd.From[5:], taken)
			case strings.HasPrefix(sd.From, "perm:"):
				v = nuPermArg(toks, sd.From[5:], taken)
			case sd.From == "extract:pat":
				v = nuExtractPat(toks)
			case strings.HasPrefix(sd.From, "slice:"):
				v = nuSliceArg(toks, sd.From[6:], taken)
			case sd.From == "pkg:dep":
				v = nuPkgDep(toks, taken)
			case sd.From == "svc:name":
				v = nuSvcName(toks, taken)
			case sd.From == "net:name":
				v = nuIfaceName(toks, taken)
			case sd.From == "disk:path":
				v = nuDiskPath(toks, taken)
			case strings.HasPrefix(sd.From, "arc:"):
				v = nuArchiveArg(toks, sd.From[4:], taken)
			case strings.HasPrefix(sd.From, "diffpair:"):
				v = nuDiffPart(toks, sd.From[9:] == "b", taken)
			case strings.HasPrefix(sd.From, "grep:"):
				v = nuGrepArg(toks, sd.From[5:], taken)
			case sd.From == "wc:what":
				v = nuWcWhat(toks)
			case strings.HasPrefix(sd.From, "sort:"):
				v = nuSortArg(toks, sd.From[5:], taken)
			case strings.HasPrefix(sd.From, "mv:"):
				v = nuMvArg(toks, sd.From[3:], taken)
			case strings.HasPrefix(sd.From, "link:"):
				v = nuLinkArg(toks, sd.From[5:], taken)
			case strings.HasPrefix(sd.From, "split:"):
				v = nuSplitArg(toks, sd.From[6:], taken)
			case strings.HasPrefix(sd.From, "date:"):
				v = nuDateArg(toks, sd.From[5:], taken)
			case sd.From == "mk:scope":
				// "in every directory" é ESCopo do create, não enumeração
				// — casa antes do prefixo genérico mk: (nuMkArg só conhece
				// "targets" e retornaria "" de qualquer forma).
				v = nuMkScope(toks, taken)
			case sd.From == "mk:scopedir":
				v = nuMkScopeDir(toks, hits, taken)
			case strings.HasPrefix(sd.From, "mk:"):
				v = nuMkArg(toks, sd.From[3:], taken)
			}
			// any From is helper-or-nothing: the generic fallbacks would
			// stuff leftovers into slots the helper deliberately left
			// empty ("repl_to" sem separador → missing honesto, não
			// "hello a.txt" colado dos residuais)
			derived := sd.From != ""
			kinds := map[string]bool{}
			for _, k := range sd.Kinds {
				kinds[k] = true
			}
			strict := nuWorldOnly(kinds)
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
						if toks[h.tok].Filler {
							continue // "print OUT the names" — partícula, não o dir 'out'
						}
						if nuInExceptClause(toks, h.tok) {
							continue // operando de exclusão nunca é valor de outro slot
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
				// create.target é mundo ao contrário: o nome NÃO existe
				// ainda — a palavra logo depois do cue de criação
				// ("directory novad") É o alvo, mesmo fora de res/shape/
				// entidade ("novad" carrega conceito create/new e some do
				// residual).
				if def.Name == "create" && name == "target" {
					for i, t := range toks {
						if !(t.has("files") || t.has("filenoun") || t.has("dirnoun") || t.has("dir")) {
							continue
						}
						for j := i + 1; j < len(toks); j++ {
							u := toks[j]
							if nuClauseMark[u.Fold] {
								break // marcador de oração fecha o alvo — "a file that logs"
							}
							if u.Filler || u.has("nameattr") || u.has("intocue") {
								continue // "a folder named scratch" — named aponta o nome
							}
							if taken[j] || nuToolWords[u.Fold] || nuPureExt(u.Raw) ||
								u.has("files") || u.has("filenoun") || u.has("dirnoun") ||
								u.has("glob") || u.has("permbit") {
								break
							}
							v, taken[j] = u.Raw, true
							break
						}
						break
					}
				}
				// the words no rule explained, as typed
				var parts []string
				clause := false
				for _, i := range res {
					if taken[i] {
						continue
					}
					if nuInExceptClause(toks, i) {
						continue // "except X" — X alimenta só o slot exclude
					}
					if strict {
						if nuIsNumber(toks[i].Fold) || len(toks[i].Tags) > 0 {
							continue // a number or an entity of another kind is not a free name
						}
						if _, isNW := nuNumWord(toks[i].Fold); isNW {
							continue // palavra-número solta ("one minute ago") é janela, não nome
						}
						// verbo de ação nunca é basename livre —
						// "Removes all files but ..." não tem nome
						// "Removes" (o verbo roteia a intenção)
						act := false
						for _, c := range toks[i].Concs {
							switch c {
							case "removeword", "delete", "mutate", "move",
								"copy", "rename", "create", "write",
								"compress", "archiveverb", "extract",
								"kill", "exec", "run", "install":
								act = true
							}
						}
						if act {
							continue
						}
						// qualificador ANTES do cue de nome não é alvo:
						// "a rust file" — rust precede 'file', é o tipo.
						// E palavra DEPOIS de oração descreve, não nomeia:
						// "a file that logs errors" — logs não é alvo.
						if def.Name == "create" && name == "target" {
							n := i + 1
							for n < len(toks) && toks[n].Filler {
								n++
							}
							if n < len(toks) && (toks[n].has("files") || toks[n].has("filenoun") ||
								toks[n].has("dirnoun") || toks[n].has("dir")) {
								continue
							}
							p := i - 1
							for p >= 0 && toks[p].Filler {
								p--
							}
							if p >= 0 && (nuClauseMark[toks[p].Fold] ||
								toks[p].has("files") || toks[p].has("filenoun") ||
								toks[p].has("dirnoun") || toks[p].has("dir")) {
								continue
							}
						}
						if toks[i].has("glob") || toks[i].has("timeref") {
							continue // glob é namepat; timeref é comparação temporal — nunca UM nome
						}
						// 'report.txt' citado é nome de arquivo explícito
						// (filequoted) — os demais quoted são literais
						if toks[i].Quoted && !toks[i].has("filequoted") {
							continue
						}
						if m.slotTokOK(kinds, &toks[i], w) {
							parts = append(parts, toks[i].Raw)
							taken[i] = true
							break
						}
						continue
					}
					if toks[i].Quoted {
						if toks[i].has("forquoted") || toks[i].has("grepword") {
							continue // padrão de conteúdo citado — é pattern, não name
						}
						if toks[i].has("outtgt") || toks[i].has("nametgt") || toks[i].has("timeref") {
							continue // destino de escrita / predicado de nome / referência temporal
						}
						parts = []string{toks[i].Raw}
						taken[i] = true
						break
					}
					if nuIsNumber(toks[i].Fold) && !(kinds["number"] || kinds["text"]) {
						continue
					}
					if kinds["number"] && !kinds["text"] && !nuIsNumber(toks[i].Fold) {
						continue // a number slot takes digits only, never a leftover name
					}
					// verbo de ação nunca cola como nome livre —
					// "Removes all files but ..." não tem name "Removes"
					actWord := false
					for _, c := range toks[i].Concs {
						switch c {
						case "removeword", "delete", "mutate", "move",
							"copy", "rename", "create", "write",
							"compress", "archiveverb", "extract",
							"kill", "exec", "run", "install":
							actWord = true
						}
					}
					if actWord {
						continue
					}
					switch toks[i].Fold {
					// marcador de oração fecha o nome — "make a rust file
					// that also compiles" não tem target "rust that also";
					// "except/without" abrem cláusula de exceção idem
					case "that", "which", "whose", "where", "when", "que", "onde", "quando", "because", "pois", "porque",
						"except", "exceto", "menos", "without", "excluding", "excluindo", "save":
						clause = true
						continue
					}
					if clause {
						break
					}
					if used["count"] &&
						(toks[i].has("count") || nuCountNouns[toks[i].Fold]) {
						// "count occurrences of foo" — a palavra de
						// contagem é linguagem de consulta, nunca pattern
						continue
					}
					if toks[i].has("grepword") || toks[i].has("nameattr") ||
						toks[i].has("sort") || toks[i].has("icase") ||
						toks[i].has("intocue") || toks[i].has("timeref") ||
						toks[i].has("forquoted") || toks[i].Fold == "by" {
						// cue de filtro ("matching"), de nome ("named") ou
						// de ordenação ("sorted by") nunca é o valor — o
						// nome/glob vem no token seguinte
						continue
					}
					switch toks[i].Fold {
					// advérbio de escopo é lugar-de-busca, nunca nome:
					// "files here" não tem name "here"
					case "here", "aqui", "ali", "lá", "now", "agora",
						"currently", "atualmente", "nearby":
						continue
					// possessivo/determinante nunca é nome: "your home
					// directory" não tem name "your"
					case "your", "yours", "my", "our", "ours", "their",
						"theirs", "its", "his", "her", "hers",
						"seu", "sua", "seus", "suas", "meu", "minha",
						"nosso", "nossa", "dele", "dela", "deles", "delas":
						continue
					// conjunção adverbial abre oração nova — "along with
					// their size" não faz parte do nome
					case "along", "together", "junto", "juntamente":
						clause = true
						continue
					}
					// "user('s) home" — o par é escopo ~, nunca name:
					// "in user's home directory" não tem name "user"/"home"
					if toks[i].Fold == "user" || toks[i].Fold == "usuario" ||
						toks[i].Fold == "usuário" {
						nearHome := false
						for k := i + 1; k < len(toks) && k <= i+3; k++ {
							if toks[k].Fold == "home" || toks[k].Fold == "casa" {
								nearHome = true
								break
							}
							if !toks[k].Filler && toks[k].Fold != "'s" &&
								toks[k].Fold != "s" {
								break
							}
						}
						if nearHome {
							continue
						}
					}
					if nuFormatWords[toks[i].Fold] || nuAllFormatWords(toks[i].Fold) {
						continue // coluna de saída/auxiliar, nunca nome
					}
					if nuPermBitToken(toks[i].Fold) {
						continue // "missing o+w" é sintaxe de permissão
					}
					if nuToolWords[toks[i].Fold] || strings.HasPrefix(toks[i].Fold, "-") {
						continue // ferramenta/flag ("-exec", "awk") nunca é nome
					}
					if nuSizeCueWords[toks[i].Fold] {
						continue // "size greater than" é predicado, não nome
					}
					switch toks[i].Fold {
					case "missing", "missed", "lacking", "faltando",
						"falta", "ausente", "ausentes":
						continue // auxiliar de negação ("files missing o+w")
					}
					if def.Name == "create" && name == "target" {
						// vizinho não-filler fecha o nome: qualificador
						// ANTES do cue ("a rust file" — rust é tipo) e
						// palavra DEPOIS de oração ("a file that logs"
						// — logs descreve, não nomeia) nunca são alvo.
						pn := i - 1
						for pn >= 0 && toks[pn].Filler {
							pn--
						}
						nn := i + 1
						for nn < len(toks) && toks[nn].Filler {
							nn++
						}
						isCue := func(t nuTok) bool {
							return t.has("files") || t.has("filenoun") ||
								t.has("dirnoun") || t.has("dir")
						}
						if (pn >= 0 && (nuClauseMark[toks[pn].Fold] || isCue(toks[pn]))) ||
							(nn < len(toks) && isCue(toks[nn])) {
							continue
						}
					}
					parts = append(parts, toks[i].Raw)
					taken[i] = true
					if !kinds["text"] || len(parts) >= 4 {
						break
					}
				}
				if v == "" {
					v = strings.Join(parts, " ")
				}
			}
			if v == "" && sd.Required && !derived {
				// a word whose concept no rule used ("instala o git": git)
				for i := len(toks) - 1; i >= 0; i-- {
					t := &toks[i]
					if t.Filler || t.Slang || taken[i] || nuInExceptClause(toks, i) {
						continue
					}
					if t.has("glob") {
						continue // '*.cpp' é filtro de conjunto, nunca UM arquivo
					}
					if t.has("forquoted") || t.has("nametgt") || t.has("outtgt") {
						continue // a aspa tem papel semântico (padrão/nome/destino)
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
						if def.Name == "create" && name == "target" {
							// mesma regra do res-parts: qualificador
							// antes do cue e palavra dentro de oração
							// nunca nomeiam o que se vai criar.
							p := i - 1
							for p >= 0 && toks[p].Filler {
								p--
							}
							nx := i + 1
							for nx < len(toks) && toks[nx].Filler {
								nx++
							}
							cue := func(x nuTok) bool {
								return x.has("files") || x.has("filenoun") ||
									x.has("dirnoun") || x.has("dir")
							}
							if (p >= 0 && (nuClauseMark[toks[p].Fold] || cue(toks[p]))) ||
								(nx < len(toks) && cue(toks[nx])) {
								continue
							}
						}
						v, taken[i] = t.Raw, true
						break
					}
				}
			}
			if v != "" {
				slots[name] = v
				if os.Getenv("NUDBG") != "" {
					println("SLOT", name, "=", v, "via", sd.From)
				}
			} else if sd.Required {
				missing = append(missing, name)
			}
		}
	}
	// unfed conta DEPOIS de todos os slots: um extrator posterior pode
	// consumir a palavra ("last 50 lines" — file roda antes de tail e
	// 'last' ainda estava livre na hora). Mantém o escopo antigo: só
	// quando há slot required/svc que cobraria palavra residual.
	unfed = 0
	countUnfed := missing != nil && !def.GoalPassthrough
	if !countUnfed && !def.GoalPassthrough {
		for _, sd := range def.Slots {
			kinds := map[string]bool{}
			for _, k := range sd.Kinds {
				kinds[k] = true
			}
			if sd.Required || nuSvcOnly(kinds) {
				countUnfed = true
				break
			}
		}
	}
	if countUnfed {
		for _, i := range res {
			if taken[i] || toks[i].Filler || len(toks[i].Concs) > 0 {
				continue
			}
			fed := false
			for _, v := range slots {
				if v == toks[i].Raw || v == toks[i].Fold {
					fed = true
					break
				}
			}
			if !fed {
				unfed++
			}
		}
	}
	// "delete everything"/"remove all .php in /var/www": o flag mass JÁ é
	// o alvo — exigir `target` preenchido transformaria um pedido claro em
	// pergunta ("apagar qual arquivo?") em vez de execução/recusa honesta
	if (slots["mass"] != "" || slots["targets"] != "") && len(missing) > 0 {
		kept := missing[:0]
		for _, name := range missing {
			if name == "target" {
				continue
			}
			kept = append(kept, name)
		}
		missing = kept
	}
	return slots, missing, hint, unfed, taken
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
	slots, _, _, _, _ := m.fill(&m.data.Intents[di], toks, hits, used, w)
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
	// mass rename por extensão: "rename png files to jpg files" — o lado
	// é um glob *.ext, não um nome literal. O método extswap decide.
	if to {
		for i := mark + 1; i < len(toks); i++ {
			if toks[i].has("extcue") {
				return "*." + toks[i].Fold
			}
		}
	} else {
		for i := 0; i < mark; i++ {
			if toks[i].has("extcue") {
				return "*." + toks[i].Fold
			}
		}
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

// nuWithWords: os separadores entre o texto velho e o novo — "replace A
// with B", "troca A por B", "substitui A para B", "swap A for B".
var nuWithWords = map[string]bool{
	"with": true, "by": true, "por": true, "para": true, "com": true,
	"to": true, "for": true, "into": true, "->": true, "=>": true,
}

// nuReplScopeCue: palavras que abrem o trecho de escopo (em qual arquivo/
// diretório) ou fecham a oração — o "to" do replace morre nelas.
var nuReplScopeCue = map[string]bool{
	"em": true, "in": true, "no": true, "na": true, "nos": true,
	"nas": true, "dentro": true, "under": true, "inside": true,
	"across": true, "throughout": true, "on": true, "onto": true,
	"and": true, "or": true, "but": true, "e": true, "ou": true,
	"mas": true, "then": true, "depois": true, "sob": true,
}

// nuReplacePart: o texto velho (from) ou o novo (to) de um pedido de
// substituição. Aspas ganham sempre: 1ª citada = from, a citada após o
// separador (ou a 2ª) = to. Sem aspas, o from é o trecho entre o verbo e
// o separador e o to é o que segue o separador — devolve a frase inteira
// quando ela é multi-palavra ("local host"→"local.host").
func nuReplacePart(toks []nuTok, res []int, to bool, taken map[int]bool) string {
	// O separador from→to é o PRIMEIRO "with/por" — "with '.php' extension"
	// lá no fim da frase é seletor de escopo, não o destino da troca.
	mark := -1
	for i := range toks {
		if nuWithWords[toks[i].Fold] && i > 0 {
			mark = i
			break
		}
	}
	// citadas em ordem: "replace 'a' with 'b'" → a,b. Aspa após cue de
	// escopo ("in 'filename'") é arquivo-alvo — sai do jogo de from/to
	// e fica livre pro slot file.
	var q []int
	for i := range toks {
		if !toks[i].Quoted {
			continue
		}
		scoped := false
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			// o cue de escopo decide pelo fold — pode estar marcado
			// filler ("in"/"em" são preposição comum)
			switch toks[j].Fold {
			case "in", "em", "no", "na", "nos", "nas", "inside",
				"under", "within", "dentro":
				scoped = true
			}
			if scoped || !toks[j].Filler {
				break
			}
		}
		if !scoped {
			q = append(q, i)
		}
	}
	if to {
		for _, i := range q {
			if mark < 0 || i > mark {
				taken[i] = true
				return toks[i].Raw
			}
		}
		if len(q) > 1 {
			taken[q[1]] = true
			return toks[q[1]].Raw
		}
		// sem aspas: o trecho após o último separador, parando no cue de
		// escopo — "por oi em a.txt" → oi; sem o corte o nome do arquivo
		// entrava no texto novo.
		if mark >= 0 {
			var parts []string
			var idx []int
			for i := mark + 1; i < len(toks); i++ {
				// cue de escopo fecha o texto novo sempre — "por oi em
				// a.txt" morre no em; um cue não pode ser texto literal
				if nuReplScopeCue[toks[i].Fold] || toks[i].has("file") {
					break
				}
				if nuWithWords[toks[i].Fold] || nuFileShaped(toks[i].Fold) {
					continue
				}
				// fillers contam: "troca x por oi" — oi é o texto, não
				// phrasing. Artigos no meio ("por o valor") são letra.
				parts = append(parts, toks[i].Raw)
				idx = append(idx, i)
			}
			if len(parts) > 0 {
				taken[mark] = true
				for _, i := range idx {
					taken[i] = true
				}
			}
			return strings.TrimSpace(strings.Join(parts, " "))
		}
		return ""
	}
	// from: a citada antes do separador (ou a 1ª de todas)
	for _, i := range q {
		if mark < 0 || i < mark {
			taken[i] = true
			return toks[i].Raw
		}
	}
	if len(q) > 1 {
		taken[q[0]] = true
		return toks[q[0]].Raw
	}
	// sem aspas: o trecho não-arquivo entre o verbo e o separador
	for _, i := range res {
		if mark >= 0 && i >= mark {
			break
		}
		t := toks[i]
		if t.Filler || t.Quoted || nuWithWords[t.Fold] || t.has("replace") {
			continue
		}
		if t.has("file") || nuNameShaped(t.Raw) && strings.Contains(t.Raw, ".") {
			continue // o arquivo não é o texto velho
		}
		switch t.Fold {
		// substantivo de contagem é phrasing ("replace all occurrences
		// of X"), nunca o texto velho — senão from="occurrences"
		case "occurrence", "occurrences", "ocorrencia", "ocorrência",
			"ocorrencias", "ocorrências", "instance", "instances",
			"instancia", "instância", "instancias", "instâncias",
			"match", "matches", "vez", "vezes", "time", "times",
			"appearance", "appearances":
			continue
		// cue-vocábulo introduz o texto ("the word NEEDLE") — o valor
		// é a palavra seguinte, não o cue
		case "word", "words", "string", "strings", "term", "termo",
			"text", "texto", "palavra", "palavras", "phrase",
			"frase", "pattern", "padrao", "padrão":
			continue
		}
		if t.has("grepword") || t.has("count") {
			continue
		}
		taken[i] = true
		return t.Raw
	}
	return ""
}

// nuArchiveArg: slots das intenções de archive. "out" é o archive a criar
// (com cue — "em/para/into/como/named" — ou o único nome-archive da frase);
// "arc" é o archive a extrair; "srcs" é a lista de fontes (arquivos, pastas
// com cue, "diretório atual"→".", tudo não-taken que parece nome).
func nuArchiveArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "out":
		outCues := map[string]bool{
			"em": true, "para": true, "pra": true, "pro": true,
			"into": true, "como": true, "as": true, "named": true,
			"chamado": true, "chamada": true, "called": true, "to": true,
			"->": true, "=>": true, "sob": true,
		}
		// pass 0: a aspa marcada outtgt ("to the file 'logfile'") é o
		// destino explícito — ganha de qualquer cue ("with 'bzip2'"
		// seria engolido como out sem ela).
		for i, t := range toks {
			if t.has("outtgt") && !taken[i] {
				taken[i] = true
				return t.Raw
			}
		}
		// pass 1: token com cue — aceita archive-shaped ou nome simples
		// ("Archive all files to copy_of_folder1": o destino pode não
		// ter extensão — o cap resolve o formato). Substantivo-arquivo
		// ("to the file X") é transparente até o nome.
		for i, t := range toks {
			if !outCues[t.Fold] || taken[i] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] {
					continue
				}
				switch u.Fold {
				case "file", "files", "arquivo", "arquivos", "output":
					continue
				}
				if nuIsNumber(u.Fold) {
					break // "directories named 0" — o número é operando
					// de outro predicado, nunca o nome do pacote
				}
				if nuArchiveWord(u.Fold) || nuNameShaped(u.Raw) || u.Quoted ||
					(len(u.Concs) == 0 && u.Fold != "") {
					taken[i], taken[j] = true, true
					return u.Raw
				}
				break
			}
		}
		// pass 1.5: "with/using/com FORMAT" — a ferramenta cita o formato,
		// não o nome ("Archive file with bzip2" → formato bzip2; o cap
		// recusa o que não suporta, mas não se perde a especificação).
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "with", "using", "via", "com", "atraves", "através":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] || u.has("article") {
					continue
				}
				if nuArchiveWord(u.Fold) {
					taken[i], taken[j] = true, true
					return u.Raw
				}
				break
			}
		}
		// pass 2: um nome-archive solto ("comprime logs.tar.gz" implícito
		// não existe — mas "create filename.tar.gz" sem cue vale), ou a
		// palavra-formato nua ("create a zip file" → formato zip).
		for i := range toks {
			if !taken[i] && nuArchiveWord(toks[i].Fold) {
				taken[i] = true
				return toks[i].Raw
			}
		}
		return ""
	case "arc":
		for i := range toks {
			if taken[i] || !nuArchiveName(toks[i].Fold) {
				continue
			}
			// "*.zip"/".gz" são filtro de conjunto ou ext pura — o
			// mass-extract (each) é quem os usa, não o slot arc
			if toks[i].has("glob") || strings.HasPrefix(toks[i].Fold, ".") {
				continue
			}
			taken[i] = true
			return toks[i].Raw
		}
		return ""
	case "srcs":
		srcCues := map[string]bool{
			"pasta": true, "pastas": true, "folder": true, "folders": true,
			"diretorio": true, "diretório": true, "diretorios": true,
			"diretórios": true, "dir": true, "dirs": true,
			"directory": true, "directories": true,
			"arquivo": true, "arquivos": true, "file": true, "files": true,
		}
		var srcs []string
		arcSrcStop := map[string]bool{
			"to": true, "into": true, "para": true, "pra": true,
			"pro": true, "em": true, "as": true, "como": true,
		}
		actionConcs := map[string]bool{
			"mutate": true, "remote": true, "net": true, "run": true,
			"delete": true, "kill": true, "exec": true, "install": true,
			"download": true, "service": true, "connect": true, "host": true,
			// o verbo de roteamento não é fonte: "Archive file X" não tem
			// fonte "Archive"; "compress files" não tem fonte "compress"
			"compress": true, "archiveverb": true, "extract": true,
			// cue de filtro ("files matching X") descreve o padrão,
			// não a fonte
			"grepword": true,
			// verbos de outra cláusula — "Find directories named 0
			// and create a tar of their RS* subdirs": o Find é a
			// primeira operação, não fonte do pacote
			"find": true, "findverb": true, "list": true,
			"listverb": true, "open": true, "count": true,
			"findattr": true,
		}
		actionWord := func(u *nuTok) bool {
			if nuIsNumber(u.Fold) {
				return true
			}
			for _, c := range u.Concs {
				if actionConcs[c] {
					return true
				}
			}
			return false
		}
		add := func(i int) {
			if taken[i] || arcSrcStop[toks[i].Fold] ||
				toks[i].has("archive") || nuArchiveWord(toks[i].Fold) {
				return // preposição de destino e formato/ferramenta
				// ("with bzip2") nunca são fonte
			}
			switch toks[i].Fold {
			// phrasing de cláusula — "excluding files matching a
			// pattern"/"save the log"/"on host X": cue de exceção,
			// saída e remoto nunca são fonte
			case "excluding", "except", "exceto", "save", "saved",
				"saving", "ouput", "program", "programa",
				"command", "comando", "matching", "match",
				"pattern", "padrao", "padrão", "tool", "ferramenta",
				"pipe", "host", "hostname":
				return
			// "save the log/output" — após cue de saída é destino
			// de redirect, não fonte; "pasta logs" é fonte de verdade
			case "log", "logs", "output", "stdout", "stderr":
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					u := &toks[j]
					if u.Filler {
						continue
					}
					switch u.Fold {
					case "save", "saved", "saving", "write",
						"writing", "redirect", "output", "ouput",
						"salva", "salvar", "grava", "gravar":
						return
					}
					break
				}
			}
			if strings.HasSuffix(toks[i].Fold, "_host") ||
				strings.HasSuffix(toks[i].Fold, "_hostname") {
				return // operando de host remoto — rsync/ssh não suportado
			}
			taken[i] = true
			srcs = append(srcs, toks[i].Raw)
		}
		// "current/current directory/pasta atual/tudo" → o projeto inteiro
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "tudo", "everything", "todas", "todos":
				if i+1 < len(toks) && srcCues[toks[i+1].Fold] {
					taken[i], taken[i+1] = true, true
					srcs = append(srcs, ".")
				}
			case "current", "atual":
				if i+1 < len(toks) && srcCues[toks[i+1].Fold] {
					taken[i], taken[i+1] = true, true
					srcs = append(srcs, ".")
				}
			}
		}
		// nome com cue de pasta/arquivo ("a pasta logs", "the folder src")
		for i, t := range toks {
			if !srcCues[t.Fold] || taken[i] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+2; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] || u.has("article") {
					continue
				}
				if arcSrcStop[u.Fold] {
					break // preposição de destino encerra a fonte — "files to 'x'"
				}
				if nuArchiveWord(u.Fold) || u.has("archive") ||
					nuPureExt(u.Fold) || nuIsNumber(u.Fold) {
					break // o archive é do slot out; ".txt" é filtro de ext
				}
				if nuArcTypeWord(u.Fold) {
					continue // "files/directories/structure" descreve o quê, não a fonte
				}
				if actionWord(u) {
					break // verbo/ação depois do cue ("pasta move X") não é fonte
				}
				add(j) // com cue de pasta/arquivo a palavra é a fonte —
				// "a pasta logs": logs tem conceito próprio e mesmo assim é src
				break
			}
			// EN "the logs folder": o nome vem ANTES do cue
			for j := i - 1; j >= 0 && j >= i-2; j-- {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] || u.has("article") {
					continue
				}
				if nuArchiveWord(u.Fold) || nuPureExt(u.Fold) || nuIsNumber(u.Fold) ||
					nuArcTypeWord(u.Fold) || actionWord(u) {
					break
				}
				add(j)
				break
			}
		}
		// "create archive X from src" — 'from' introduz a fonte direto;
		// quoted depois de pattern-cue é do slot pat, não fonte
		patCueWords := map[string]bool{
			"matching": true, "match": true, "pattern": true,
			"padrao": true, "padrão": true, "named": true, "called": true,
			"like": true, "como": true,
		}
		for i, t := range toks {
			if (t.Fold == "from" || t.Fold == "de") && !taken[i] {
				for j := i + 1; j < len(toks) && j <= i+2; j++ {
					u := &toks[j]
					if u.Filler || u.Slang || taken[j] || u.has("article") {
						continue
					}
					if nuArchiveWord(u.Fold) || nuIsNumber(u.Fold) ||
						nuArcTypeWord(u.Fold) || actionWord(u) {
						break
					}
					add(j)
					break
				}
			}
		}
		// tokens com cara de nome/path que sobraram ("a.txt", "src/x")
		for i, t := range toks {
			if taken[i] || t.Filler || t.Slang || actionWord(&toks[i]) {
				continue
			}
			if t.has("outtgt") {
				continue // destino de escrita é do slot out
			}
			if t.Quoted {
				// 'filepattern' depois de match/pattern é do slot pat
				isPat := false
				for j := i - 1; j >= 0 && j >= i-2; j-- {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					isPat = patCueWords[u.Fold]
					break
				}
				if isPat {
					continue
				}
			}
			if nuNameShaped(t.Raw) || nuFileShaped(t.Fold) ||
				strings.Contains(t.Raw, "/") || (t.Quoted && t.Raw != "") {
				if !nuArchiveName(t.Fold) && !nuPureExt(t.Fold) &&
					!nuArcAllTypeWords(t.Raw) && !t.has("archive") {
					add(i)
				}
			}
		}
		return strings.Join(srcs, " ")
	case "pat":
		// "archive files matching *.log / pattern X / named work" — o
		// filtro do producer. Glob solto também vale ("archive *.log").
		patCues := map[string]bool{
			"matching": true, "match": true, "pattern": true,
			"padrao": true, "padrão": true, "named": true, "called": true,
			"like": true, "como": true,
		}
		for i, t := range toks {
			if taken[i] || !patCues[t.Fold] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] || u.has("article") ||
					patCues[u.Fold] {
					continue // "match pattern 'x'" — cue não é o valor
				}
				if nuArchiveWord(u.Fold) || nuIsNumber(u.Fold) {
					break
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
		for i, t := range toks {
			if !taken[i] && strings.Contains(t.Raw, "*") {
				taken[i] = true
				return t.Raw
			}
		}
		return ""
	}
	return ""
}

// nuGrepArg: o padrão buscado — texto entre aspas, ou o trecho depois de
// "for/por/containing/contêm" até o cue de escopo ("in FILE", "no log").
// Palavras-meta ("the string foo", "the word bar") são conceito content,
// não parte do padrão.
func nuGrepArg(toks []nuTok, what string, taken map[int]bool) string {
	if what != "pat" {
		return ""
	}
	// aspas primeiro: o conteúdo citado é o padrão — com duas citações,
	// a marcada forquoted (depois de "for") é o padrão, a outra é alvo:
	// 'search files "f.c" for the string "can'"' → pattern=can, file=f.c
	for i, t := range toks {
		if !taken[i] && t.Quoted && t.Raw != "" && t.has("forquoted") {
			taken[i] = true
			return t.Raw
		}
	}
	for i, t := range toks {
		if !taken[i] && t.Quoted && t.Raw != "" && !t.has("filequoted") &&
			!t.has("conttgt") {
			taken[i] = true
			return t.Raw
		}
	}
	patCues := map[string]bool{
		"for": true, "por": true, "de": true,
		// Fold tira acento — "contêm" chega como "contem"
		"containing": true, "contains": true, "contain": true,
		"contem": true, "contendo": true, "contenha": true, "matching": true,
		"com": true, "with": true,
	}
	var parts []string
	for i, t := range toks {
		cue := patCues[t.Fold]
		if !cue && t.Fold == "of" && i > 0 {
			// "count/occurrences/number of foo" — o 'of' introduz o
			// padrão depois de palavra de contagem; 'of' sozinho é
			// posse ("files of the project"), não cue
			prev := &toks[i-1]
			cue = prev.has("count") || nuCountNouns[prev.Fold]
		}
		if !cue || taken[i] {
			continue
		}
		// "contain a file/directory named 'bin'" — o objeto da
		// contenção é uma DESCRIÇÃO ESTRUTURAL (existe/não-existe um
		// arquivo assim), não um padrão de texto. O primeiro
		// substantivo após o cue sendo file/dir-noun fecha essa
		// interpretação — o cue é ignorado e a aspa cai no Dropped.
		structural := false
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			u := &toks[j]
			if u.Filler || u.Slang || u.has("article") {
				continue
			}
			// quantificador de escopo é transparente: "search for all
			// files" — o substantivo-classe ainda é o primeiro objeto
			switch u.Fold {
			case "all", "every", "any", "each", "todos", "todas",
				"todo", "toda", "cada", "qualquer", "quaisquer":
				continue
			}
			if nuArcTypeWord(u.Fold) || nuArcAllTypeWords(u.Fold) ||
				nuFileCueWords[u.Fold] || u.has("files") ||
				u.has("filenoun") || u.has("dirnoun") {
				structural = true
			}
			break
		}
		if structural {
			continue
		}
		taken[i] = true
		for j := i + 1; j < len(toks) && len(parts) < 6; j++ {
			u := &toks[j]
			// cue de escopo/conjunção fecha o padrão — "error in app.log":
			// o arquivo é do slot file, não do padrão
			switch u.Fold {
			case "in", "em", "no", "na", "nos", "nas", "under", "inside",
				"across", "throughout", "dentro", "on", "onto",
				"and", "or", "but", "e", "ou", "mas", "then",
				"except", "without", "exceto", "menos":
				return strings.Join(parts, " ")
			}
			if u.Filler || u.Slang || taken[j] || u.has("article") {
				continue
			}
			if u.has("content") || nuIsNumber(u.Fold) && len(parts) == 0 {
				continue // "the string foo" — meta, não padrão
			}
			parts = append(parts, u.Raw)
			taken[j] = true
		}
		break
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	// sem cue ("find TODO", "procura parser"): sobras exceto palavra-meta
	// (conteúdo `content` = string/word/texto, não padrão). "error" tem
	// conceito `error` mas É alvo de busca — coletável.
	for i, u := range toks {
		if taken[i] || u.Filler || u.Slang || u.has("article") ||
			u.has("content") || u.has("grepword") || u.has("find") ||
			// "localiza o arquivo que tem X" — a classe do substantivo
			// e o verbo de posse são phrasing; o padrão é X. "whose
			// names begin" idem: vocábulo de nome, não de conteúdo
			u.has("filenoun") || u.has("dirnoun") || u.has("have") ||
			u.has("nameattr") || u.has("files") ||
			u.has("count") || nuCountNouns[u.Fold] ||
			nuArchiveWord(u.Fold) || nuPureExt(u.Fold) ||
			nuNameShaped(u.Raw) && strings.ContainsAny(u.Raw, "./") {
			continue
		}
		switch u.Fold {
		case "in", "em", "no", "na", "nos", "nas", "under", "inside", "across", "on", "onto":
			continue
		}
		parts = append(parts, u.Raw)
		taken[i] = true
		if len(parts) >= 4 {
			break
		}
	}
	return strings.Join(parts, " ")
}

// nuDiffPart: um lado do par "A vs B" do dir_diff — "compare dir a vs
// dir b". A letra solta depois do substantivo-classe é o LABEL do
// operando (dir a/dir b), mesmo quando ela é artigo-filler ("a").
func nuDiffPart(toks []nuTok, wantB bool, taken map[int]bool) string {
	sep := -1
	for i, t := range toks {
		switch t.Fold {
		case "vs", "versus", "e", "and", "com":
			if sep < 0 {
				sep = i
			}
		}
	}
	if sep < 0 {
		return ""
	}
	dirWord := func(f string) bool {
		switch f {
		case "dir", "dirs", "directory", "directories", "folder",
			"folders", "pasta", "pastas", "diretorio", "diretório",
			"diretorios", "diretórios", "between", "compare",
			"compara", "diff", "diferenca", "diferença":
			return true
		}
		return false
	}
	label := func(i int) bool {
		// letra solta ("a", "b", "x") depois do substantivo-classe
		// é o label do operando — mesmo quando ela é artigo
		t := &toks[i]
		if len(t.Fold) != 1 {
			return false
		}
		for p := i - 1; p >= 0; p-- {
			if toks[p].Filler {
				continue
			}
			return dirWord(toks[p].Fold)
		}
		return false
	}
	if !wantB {
		for i := sep - 1; i >= 0; i-- {
			t := &toks[i]
			if dirWord(t.Fold) || t.Slang || taken[i] {
				continue
			}
			if !t.Filler || label(i) {
				taken[i] = true
				return t.Raw
			}
		}
		return ""
	}
	for i := sep + 1; i < len(toks); i++ {
		t := &toks[i]
		if dirWord(t.Fold) || t.Slang || taken[i] {
			continue
		}
		if !t.Filler || label(i) {
			taken[i] = true
			return t.Raw
		}
	}
	return ""
}

// nuWcWhat: qual grandeza contar — a resposta errada é somar "lines"
// quando pediram "words". Cue explícito vence; sem cue, lines (pedido
// dominante do domínio).
func nuWcWhat(toks []nuTok) string {
	for _, t := range toks {
		switch t.Fold {
		case "word", "words", "palavra", "palavras", "vocábulo", "vocabulos":
			return "words"
		case "char", "chars", "character", "characters", "caractere", "caracteres",
			"byte", "bytes", "carateres":
			return "characters"
		case "line", "lines", "linha", "linhas", "row", "rows":
			return "lines"
		}
	}
	return "lines"
}

// nuSortArg: slots do sort — key (coluna 1-based), flags (numeric,
// reverse, unique), to (destino name-shaped depois de cue), delim
// (separator explícito; .csv no pedido implica ",").
func nuSortArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "key":
		ords := map[string]int{
			"first": 1, "primeira": 1, "primeiro": 1,
			"second": 2, "segunda": 2, "segundo": 2,
			"third": 3, "terceira": 3, "terceiro": 3,
			"fourth": 4, "quarta": 4, "quarto": 4,
			"fifth": 5, "quinta": 5, "quinto": 5,
			"sixth": 6, "sexta": 6, "sexto": 6,
		}
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "column", "coluna", "field", "campo", "col", "k", "key":
				for _, j := range []int{i - 1, i + 1, i + 2} {
					if j < 0 || j >= len(toks) || taken[j] {
						continue
					}
					u := &toks[j]
					if n, err := strconv.Atoi(u.Fold); err == nil && n > 0 {
						taken[i], taken[j] = true, true
						return strconv.Itoa(n)
					}
					// "4th"/"2nd"/"1st": dígito + sufixo ordinal
					f := u.Fold
					for _, suf := range []string{"th", "st", "nd", "rd", "o", "a"} {
						if strings.HasSuffix(f, suf) && len(f) > len(suf) {
							if n, err := strconv.Atoi(f[:len(f)-len(suf)]); err == nil && n > 0 {
								taken[i], taken[j] = true, true
								return strconv.Itoa(n)
							}
						}
					}
					if n, ok := ords[u.Fold]; ok {
						taken[i], taken[j] = true, true
						return strconv.Itoa(n)
					}
				}
			}
		}
	case "flags":
		var f []string
		for i, t := range toks {
			if taken[i] {
				continue
			}
			// -u / -n / -r / -nr / -un — forma curta literal do sort(1)
			if strings.HasPrefix(t.Fold, "-") && len(t.Fold) <= 4 {
				letters := strings.TrimLeft(t.Fold, "-")
				if letters != "" && strings.IndexFunc(letters, func(r rune) bool {
					return r != 'n' && r != 'r' && r != 'u'
				}) < 0 {
					for _, r := range letters {
						switch r {
						case 'n':
							f = append(f, "numeric")
						case 'r':
							f = append(f, "reverse")
						case 'u':
							f = append(f, "unique")
						}
					}
					taken[i] = true
					continue
				}
			}
			switch t.Fold {
			case "numeric", "numerically", "numericamente", "numerico", "numerica":
				f = append(f, "numeric")
				taken[i] = true
			case "reverse", "reversa", "reverso", "inverted", "invertida", "invertido":
				f = append(f, "reverse")
				taken[i] = true
			case "unique", "uniq", "unica", "unicas", "unico", "unicos",
				"distinct", "dedupe", "dedup":
				f = append(f, "unique")
				taken[i] = true
			}
		}
		return strings.Join(f, ",")
	case "to":
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "to", "para", "into", "em", "save", "salva", "output", "grava":
				for j := i + 1; j < len(toks) && j < i+4; j++ {
					u := &toks[j]
					if taken[j] || u.Filler || u.has("article") {
						continue
					}
					if nuNameShaped(u.Raw) {
						taken[i], taken[j] = true, true
						return u.Raw
					}
				}
			}
		}
	case "delim":
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "separador", "separator", "delimiter", "delimitador", "sep", "separado":
				for j := i + 1; j < len(toks) && j < i+4; j++ {
					u := &toks[j]
					if taken[j] || u.Filler || u.has("article") {
						continue
					}
					if u.Quoted && u.Raw != "" {
						taken[i], taken[j] = true, true
						return u.Raw
					}
					if len(u.Raw) <= 3 && !nuNameShaped(u.Raw) {
						taken[i], taken[j] = true, true
						return u.Raw
					}
				}
			}
		}
		// ".csv" no pedido → o delimitador implícito é vírgula
		for _, t := range toks {
			if strings.HasSuffix(strings.ToLower(t.Raw), ".csv") || t.Fold == "csv" {
				return ","
			}
		}
	}
	return ""
}

// nuLinkArg: operandos do link_create — path (o link NOVO: "named X"),
// target (pra onde aponta: "to Y"/"for Y"), hard (hard link → os.Link).
// O link é sempre o lado novo da mutação; o alvo é o caminho existente
// — inverter os dois criaria um link no lugar errado (silent-wrong).
func nuLinkArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "hard":
		// "hard link" pode chegar fundido (linknoun contém "hard") ou
		// solto ("create a hard link" → 'hard' + 'link')
		for i, t := range toks {
			if !t.has("linknoun") {
				continue
			}
			if strings.Contains(t.Fold, "hard") ||
				strings.Contains(t.Fold, "fisico") ||
				strings.Contains(t.Fold, "físico") {
				taken[i] = true
				return "true"
			}
		}
		for i, t := range toks {
			if t.Fold != "hard" && t.Fold != "fisico" && t.Fold != "físico" {
				continue
			}
			for j := i - 3; j <= i+3 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				u := &toks[j]
				if u.has("linknoun") || u.Fold == "link" || u.Fold == "links" {
					taken[i], taken[j] = true, true
					return "true"
				}
			}
		}
		return ""
	case "target":
		// "named X to Y"/"link X to Y"/"link for Y" — a preposição de
		// destino introduz o caminho EXISTENTE que o link apontará
		cues := map[string]bool{
			"to": true, "para": true, "pra": true, "pro": true,
			"for": true, "por": true, "towards": true,
			"apontando": true, "pointing": true, "targeting": true,
			"at": true,
		}
		for i, t := range toks {
			if taken[i] || !cues[t.Fold] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				// "apontando para X"/"pointing to X" — a preposição
				// direcional depois do cue é ponte, não alvo
				switch u.Fold {
				case "para", "pra", "pro", "to", "a", "o", "em", "no":
					continue
				}
				// "to file X"/"to the file X" — o substantivo-classe
				// é ponte; o path vem depois
				if nuFileCueWords[u.Fold] || nuArcTypeWord(u.Fold) {
					continue
				}
				if taken[j] {
					return ""
				}
				if len(u.Concs) > 0 && !u.Quoted {
					return "" // palavra-conceito é predicado, não path
				}
				if !u.Quoted && !nuNameShaped(u.Raw) &&
					!strings.ContainsAny(u.Raw, "/.$~") {
					return ""
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
		return ""
	case "path":
		// "named X"/"called X"/"com o nome X" — depois do cue de nome
		// vem o caminho do link novo; sem ele o pedido não tem lado
		// novo definido ("create link for each file" → missing honesto)
		for i, t := range toks {
			if taken[i] || !t.has("nameattr") {
				continue
			}
			// "with the same name" — nameattr de igualdade é phrasing,
			// não cue de nome do link ('same' é filler: checar a fold
			// ANTES de pular, senão ele escapa da peneira)
			same := false
			for k := i - 1; k >= 0 && k >= i-3; k-- {
				u := &toks[k]
				if u.Fold == "same" || u.Fold == "mesmo" || u.Fold == "mesma" ||
					u.Fold == "mesmos" || u.Fold == "mesmas" {
					same = true
					break
				}
				if u.Filler || u.Slang {
					continue
				}
				break
			}
			if same {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				if taken[j] {
					return ""
				}
				taken[i], taken[j] = true, true
				name := u.Raw
				// "create link in DIR named X" — o path do link é
				// DIR/X, não X na raiz do confinamento
				if !strings.ContainsAny(name, "/\\") {
					for k := 0; k+1 < len(toks); k++ {
						switch toks[k].Fold {
						case "in", "em", "no", "na", "inside", "under", "dentro":
						default:
							continue
						}
						for l := k + 1; l < len(toks) && l <= k+3; l++ {
							v := &toks[l]
							if v.Filler || v.Slang || nuArcTypeWord(v.Fold) ||
								nuFileCueWords[v.Fold] {
								continue
							}
							if l == j || taken[l] ||
								(!v.Quoted && len(v.Concs) > 0) {
								break
							}
							taken[k], taken[l] = true, true
							return v.Raw + "/" + name
						}
					}
				}
				return name
			}
		}
		// "create symlink subl to X"/"hard link foo to bar" — a palavra
		// nua logo depois do substantivo-link (antes do cue de alvo) é
		// o nome do link novo; sem nameattr ela ficava solta e o slot
		// required saía missing. Nome de link é palavra livre — a
		// posição após o substantivo JÁ é a evidência, não a forma.
		linkNameCues := map[string]bool{
			"to": true, "para": true, "pra": true, "pro": true,
			"for": true, "por": true, "at": true, "named": true,
			"called": true, "nome": true, "chamado": true, "in": true,
			"em": true, "pointing": true, "targeting": true,
			"apontando": true, "from": true, "de": true, "of": true,
		}
		for i, t := range toks {
			// a âncora linknoun pode já estar taken ("hard link foo" —
			// o slot hard marcou 'hard'+'link'): o que importa é o
			// candidato livre depois dela
			if !t.has("linknoun") && t.Fold != "link" &&
				t.Fold != "links" && t.Fold != "symlink" &&
				t.Fold != "symlinks" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				if taken[j] || len(u.Concs) > 0 || u.has("linknoun") ||
					linkNameCues[u.Fold] {
					break
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
		return ""
	}
	return ""
}

// nuMvArg: slots do file_move — dest (dir destino após cue to/para/into),
// pat (glob do file_find: "*.$ext" > nome literal > "*"), type (file|dir).
var mvDestCues = map[string]bool{"to": true, "para": true, "pra": true, "into": true,
	"em": true, "inside": true, "under": true, "dentro": true}

var mvScopeAll = map[string]bool{"all": true, "every": true, "each": true,
	"todos": true, "todas": true, "cada": true}

func nuMvArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "dest":
		destCue := mvDestCues
		dirWord := map[string]bool{"folder": true, "pasta": true, "dir": true, "directory": true,
			"diretorio": true, "diretório": true, "destino": true, "target": true,
			"subdirectory": true, "subdirectories": true, "subfolder": true, "folders": true,
			"directories": true, "dirs": true, "pastas": true}
		vague := map[string]bool{"another": true, "outra": true, "outro": true,
			"some": true, "alguma": true, "algum": true, "other": true, "nova": true, "new": true}
		for i, t := range toks {
			if taken[i] || !destCue[t.Fold] {
				continue
			}
			if t.Fold == "under" {
				// "under X" é destino só quando segue o verbo de
				// deslocamento ("move it under /x"); antes do verbo
				// ("find files under /a and move them to /b") é
				// escopo-fonte — o srcdir reclama
				mv := false
				for _, p := range toks[:i] {
					if p.has("move") || p.has("copy") || p.has("mutate") {
						mv = true
						break
					}
				}
				if !mv {
					continue
				}
			}
			for j := i + 1; j < len(toks) && j < i+4; j++ {
				u := &toks[j]
				if taken[j] || u.Filler || u.has("article") || dirWord[u.Fold] || vague[u.Fold] {
					continue
				}
				// "under current directory to x": 'current'/'this' são
				// escopo (este projeto), não destino
				switch u.Fold {
				case "current", "atual", "this", "este", "esta":
					continue
				}
				if destCue[u.Fold] || u.has("intocue") {
					continue // "under current directory to ./newdir" — o 'to' é cue, não destino
				}
				switch u.Fold {
				case "and", "or", "e", "ou", "except", "without", "that", "which":
					return ""
				}
				if u.has("mutate") || u.has("delete") || u.has("create") || nuArchiveWord(u.Fold) {
					return ""
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
	case "pat":
		// nome literal depois de cue — "directories named work" → "work"
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "named", "called", "nome", "nomeado", "chamado", "chamada", "name":
				for j := i + 1; j < len(toks) && j < i+3; j++ {
					u := &toks[j]
					if taken[j] || u.Filler || u.has("article") {
						continue
					}
					switch u.Fold {
					case "and", "or", "e", "ou", "to", "para", "into", "which", "that",
						"dir", "directory", "folder", "pasta", "file", "arquivo":
						return ""
					}
					if u.has("mutate") || u.has("delete") || u.has("create") {
						return ""
					}
					taken[i], taken[j] = true, true
					return u.Raw
				}
			}
		}
		for i, t := range toks {
			// família-ext alimenta pat MESMO se o slot ext já consumiu o
			// token — o find precisa do glob (*.txt) como filtro de nome,
			// então o check de taken vem só depois desses ramos.
			if strings.HasPrefix(t.Raw, "*.") || nuPureExt(t.Fold) {
				taken[i] = true
				return "*." + strings.TrimPrefix(strings.TrimPrefix(t.Fold, "*"), ".")
			}
			// extensão nua ("jpg files", "every jpg") precisa de âncora:
			// palavra-arquivo ou escopo-all colado — e nunca depois de cue
			// de destino ("files to go" não pode virar "*.go").
			if nuExtWords[t.Fold] && (i == 0 || !mvDestCues[toks[i-1].Fold]) {
				near := false
				for j := i - 2; j <= i+2 && !near; j++ {
					if j >= 0 && j < len(toks) && j != i &&
						(toks[j].has("files") || toks[j].has("filenoun") ||
							mvScopeAll[toks[j].Fold]) {
						near = true
					}
				}
				if near {
					taken[i] = true
					return "*." + t.Fold
				}
			}
			// plural de extensão é evidência suficiente sozinho: "jpgs"→*.jpg
			if f := strings.TrimSuffix(t.Fold, "s"); f != t.Fold && nuExtWords[f] {
				taken[i] = true
				return "*." + f
			}
			if taken[i] {
				continue
			}
		}
		// "*" só quando o pedido explicita o conjunto inteiro — "move all
		// files": scopeall + palavra-arquivo. Sem filtro E sem cue, pat=""
		// → needs_input honesto em vez de mover tudo por engano.
		hasAll, hasFiles := false, false
		for _, t := range toks {
			if mvScopeAll[t.Fold] {
				hasAll = true
			}
			if t.has("files") || t.Fold == "everything" || t.Fold == "tudo" {
				hasFiles = true
			}
		}
		if hasAll && hasFiles {
			return "*"
		}
		return ""
	case "type":
		// o "quê" do move é o primeiro substantivo file/dir depois do
		// verbo — "move the files in sub directories" move ARQUIVOS;
		// uma palavra-dir precedida de cue de escopo/origem/destino é
		// lugar ("in sub directories", "under current directory",
		// "to the images directory"), nunca o tipo movido
		dirNouns := map[string]bool{
			"directory": true, "directories": true, "diretorio": true,
			"diretório": true, "diretorios": true, "folder": true,
			"folders": true, "pasta": true, "pastas": true,
			"subdir": true, "subdirectory": true,
		}
		for i, t := range toks {
			isDir := dirNouns[t.Fold]
			isFile := t.Fold == "file" || t.Fold == "files" ||
				t.Fold == "arquivo" || t.Fold == "arquivos"
			if !isDir && !isFile {
				continue
			}
			if i > 0 {
				switch toks[i-1].Fold {
				case "in", "em", "no", "na", "nos", "inside", "under",
					"dentro", "sub", "current", "atual", "to", "para",
					"pra", "into", "from", "de", "of", "the", "a", "an",
					"non-recursively", "recursively", "recursive",
					"nonrecursive":
					// "the" precisa de olhar um a mais: "to the dir"
					// é destino, "move the files" é tipo — decide o
					// destNear abaixo para dir; file aceita direto
					if toks[i-1].Fold == "the" || toks[i-1].Fold == "a" ||
						toks[i-1].Fold == "an" {
						break
					}
					continue
				}
			}
			if isDir {
				destNear := false
			scan:
				for j := i - 1; j >= 0 && j >= i-4; j-- {
					u := &toks[j]
					switch u.Fold {
					case "and", "or", "e", "ou", "except", "without", "which", "that", "from", "de":
						break scan
					}
					if u.has("mutate") || u.has("delete") || u.has("create") {
						break scan
					}
					switch u.Fold {
					case "to", "para", "pra", "into", "inside", "under", "dentro", "in":
						destNear = true
						break scan
					}
				}
				if destNear {
					continue
				}
				return "dir"
			}
			return "file"
		}
		return "file"
	case "srcdir":
		// diretório FONTE — "in dir X"/"under X"/"from X". Não reusa
		// find:dir_cued porque "directories named work" é cue de nome, não de
		// dir; e "em/no" ficam de fora porque em PT são cue de destino.
		srcCue := map[string]bool{"in": true, "inside": true, "under": true,
			"from": true, "de": true, "below": true}
		for i, t := range toks {
			if taken[i] || !srcCue[t.Fold] {
				continue
			}
			for j := i + 1; j < len(toks) && j < i+4; j++ {
				u := &toks[j]
				if taken[j] || u.Filler || u.has("article") ||
					u.Fold == "dir" || u.Fold == "directory" || u.Fold == "folder" ||
					u.Fold == "diretorio" || u.Fold == "diretório" || u.Fold == "pasta" ||
					u.Fold == "tree" || u.Fold == "current" || u.Fold == "atual" {
					continue
				}
				// "in sub directories" / "non-recursively" descrevem a
				// recursão, não uma pasta chamada 'sub'; plurais genéricos
				// ("in directories", "in folders") idem
				switch u.Fold {
				case "sub", "non-recursively", "nonrecursive", "non-recursive",
					"subdirectories", "subdirectory", "subdirs", "subfolders",
					"directories", "dirs", "folders", "pastas", "diretorios",
					"diretórios", "files", "arquivos":
					continue
				}
				switch u.Fold {
				case "and", "or", "e", "ou", "to", "para", "into", "which", "that", "move", "rename":
					return ""
				}
				if u.has("mutate") || u.has("delete") || u.has("create") || nuArchiveWord(u.Fold) || nuPureExt(u.Fold) {
					return ""
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
	case "op":
		// copy preserva a fonte (cp -r), move/rename desloca (rename
		// POSIX). O intent só casa com o conceito move|copy na regra,
		// então o fallback 'rename' é seguro.
		for _, t := range toks {
			if t.has("copy") {
				return "copy"
			}
		}
		return "rename"
	case "target":
		// operando ÚNICO do move/copy: citado, path ou cara-de-arquivo.
		// O sponge genérico engolia o próprio verbo ("target:copy"),
		// pronomes ("them"), o filtro de padrão ("jpgs" quando pat já
		// está *.jpg) e o tamanho ("1MB") — e file_move.one disparava
		// rename no literal. Alvo só vale se tiver evidência de nome.
		mvDestCues2 := map[string]bool{"to": true, "para": true, "pra": true,
			"into": true, "em": true, "no": true, "na": true, "as": true}
		// anáfora consome-se como alvo implícito: "move them to x" — o
		// pronome não é operando mas não pode contar palavra solta. Em
		// outros intents ("delete them") segue livre e derruba a conf.
		for i, t := range toks {
			switch t.Fold {
			case "them", "it", "eles", "elas", "isso", "isto":
				taken[i] = true
			}
		}
		for i := range toks {
			t := &toks[i]
			if taken[i] || t.Filler {
				continue
			}
			if i > 0 && mvDestCues2[toks[i-1].Fold] {
				continue // depois de cue de destino é o dest, não o alvo
			}
			if nuInExceptClause(toks, i) {
				continue
			}
			// "1MB"/"10k" é limite de tamanho, "30" é idade da janela —
			// nenhum é nome de arquivo
			if t.Fold != "" && !t.Quoted && (nuDigitSize(t.Fold) || nuIsNumber(t.Fold)) {
				continue
			}
			// "$VAR" é escopo/ambiente, nunca alvo literal — "delete
			// hidden files under $D" tem dir=$D, não target=$D
			if strings.HasPrefix(t.Raw, "$") {
				continue
			}
			// path depois de cue de escopo é o ONDE (dir), nunca o alvo:
			// "remove them under '/usr/ports/'" não deleta /usr/ports
			if i > 0 {
				switch toks[i-1].Fold {
				case "under", "below", "in", "inside", "within", "into",
					"em", "no", "na", "nos", "nas", "down", "abaixo",
					"from", "de", "dentro":
					continue
				}
			}
			// "the folder src" / "directory data": substantivo SINGULAR
			// cita o operando ("copy the folder src"). Plural é filtro
			// de conjunto ("files larger than 1MB"), "directory tree" é
			// substantivo composto e stopword é o próprio cue ("directory
			// to ./x") — nenhum cita nome.
			if i > 0 && nuOperandBare(t.Fold) && !nuStopBare[t.Fold] {
				switch toks[i-1].Fold {
				case "folder", "directory", "diretorio", "diretório",
					"pasta", "dir", "file", "arquivo":
					taken[i] = true
					return t.Raw
				}
			}
			if t.Quoted || nuNameShaped(t.Raw) ||
				(strings.Contains(t.Raw, "/") && !strings.ContainsAny(t.Raw, "*?[")) {
				taken[i] = true
				return t.Raw
			}
		}
	}

	return ""
}

// nuSplitArg: operandos do file_split — "pieces per 100 lines" /
// "partes de 100 linhas". `lines` é o número logo ANTES da palavra linha
// ("per 100 lines", "de 100 linhas", "every 500 lines", "a cada 1000 linhas");
// `pieces` é o número antes do substantivo de parte ("into 4 pieces",
// "em 4 partes"). Um número fora dessas posições não preenche nada: pedido
// de divisão por quantidade de partes ou por bytes fica honestamente sem
// critério (needs_input/recusa), nunca vira contagem de linhas por engano.
func nuSplitArg(toks []nuTok, what string, taken map[int]bool) string {
	var isCue func(t *nuTok) bool
	switch what {
	case "lines":
		isCue = func(t *nuTok) bool {
			switch t.Fold {
			case "line", "lines", "linha", "linhas":
				return true
			}
			return false
		}
	case "pieces":
		isCue = func(t *nuTok) bool { return t.has("splitnoun") }
	default:
		return ""
	}
	for i := range toks {
		if taken[i] || !isCue(&toks[i]) {
			continue
		}
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			u := &toks[j]
			if taken[j] {
				continue
			}
			if nuIsNumber(u.Fold) {
				taken[i], taken[j] = true, true
				return u.Fold
			}
			if !u.Filler {
				return "" // a palavra densa antes do cue não é número — sem critério
			}
		}
		return ""
	}
	return ""
}

// nuArcTypeWord: palavras que descrevem o TIPO do que vai ser arquivado —
// nunca são o nome da fonte ("archive the directory structure", "archive
// files/directories under data").
var nuArcTypeWords = map[string]bool{
	"structure": true, "estrutura": true, "contents": true, "content": true,
	"conteudo": true, "conteúdo": true, "files": true, "file": true,
	"directories": true, "directory": true, "dirs": true, "dir": true,
	"folders": true, "folder": true, "subdirectories": true, "subdirs": true,
	"arquivos": true, "arquivo": true, "pastas": true, "pasta": true,
	"diretorios": true, "diretório": true, "diretorio": true,
	// "data/ directory tree" — tree/hierarchy descrevem o passeio,
	// não a fonte: srcs=data/, nunca srcs=tree
	"tree": true, "arvore": true, "árvore": true,
	"hierarchy": true, "hierarquia": true,
}

func nuArcTypeWord(f string) bool { return nuArcTypeWords[f] }

// nuArcAllTypeWords: "files/directories" — todos os pedaços são tipo.
func nuArcAllTypeWords(raw string) bool {
	for _, p := range strings.Split(raw, "/") {
		if p != "" && !nuArcTypeWords[p] {
			return false
		}
	}
	return strings.Contains(raw, "/")
}

// nuReplFlag: flags do replace — "first occurrence/only the first" →
// first_per_line; "case-insensitive/ignoring case" → ignore_case.
func nuReplFlag(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "first":
		for i := range toks {
			if toks[i].Fold == "first" || toks[i].Fold == "primeira" ||
				toks[i].Fold == "primeiro" {
				return "true"
			}
		}
	case "icase":
		// "case insensitive" pode vir em 1 ou 2 tokens; "case sensitive"
		// volta "" — o replace default JÁ é sensível, é o pedido exato
		for i, t := range toks {
			switch t.Fold {
			case "case-insensitive", "case-insensitively", "caseinsensitive",
				"caseless", "nocase", "any-cased", "anycase", "any-case",
				"insensitively":
				return "true"
			case "insensitive":
				for j := i - 3; j <= i+3; j++ {
					if j >= 0 && j < len(toks) && j != i && toks[j].Fold == "case" {
						return "true"
					}
				}
				return "true" // "insensitive" solto já é flag de case
			case "ignoring", "ignore", "ignorando", "regardless":
				for j := i + 1; j <= i+3 && j < len(toks); j++ {
					if toks[j].Fold == "case" || toks[j].Fold == "casing" {
						return "true"
					}
				}
			case "case":
				for j := i + 1; j <= i+3 && j < len(toks); j++ {
					switch toks[j].Fold {
					case "insensitive", "insensitively":
						return "true"
					}
				}
			case "diferenciar", "diferenciando":
				for j := i + 1; j <= i+4 && j < len(toks); j++ {
					switch toks[j].Fold {
					case "maiúsculas", "minúsculas", "maiusculas", "minusculas":
						return "true"
					}
				}
			}
		}
	case "stdin":
		// "in standard input", "from stdin", "inputfile" — origem não é
		// arquivo do projeto: nenhum método text_replace aplica (recusa
		// honesta, não sucesso vacuo).
		for i := range toks {
			if toks[i].has("stdin") || toks[i].Fold == "stdin" || toks[i].Fold == "inputfile" {
				return "1"
			}
			if toks[i].Fold == "standard" && i+1 < len(toks) && toks[i+1].Fold == "input" {
				return "1"
			}
		}
	case "namefile":
		// "in file names", "dos nomes de arquivo" — o alvo é o NOME, não o
		// conteúdo: text_replace é a ferramenta errada, recusa honesta.
		// Exceção: "keep backups with .bakup extension" — a palavra
		// "extension" descreve o sufixo do backup, não um alvo de nome.
		// "extension" só é nome-alvo quando não descreve um sufixo real:
		// com .xxx presente (ou backup na frase) ela é seletor, não alvo.
		extTok := false
		bak := false
		for i := range toks {
			f := toks[i].Fold
			if strings.HasPrefix(f, ".") && len(f) > 1 {
				extTok = true
			}
			switch f {
			case "backup", "backups", "cópia", "copia", "cópias", "copias":
				bak = true
			}
		}
		for i := range toks {
			if !toks[i].has("nameattr") {
				continue
			}
			if extTok || bak {
				switch toks[i].Fold {
				case "extension", "extensions", "ext", "extensão", "extensões", "extensao", "extensoes", "suffix", "sufixo":
					continue
				}
			}
			return "1"
		}
	case "backup":
		// "keep backups with .bakup extension" / "guarda backup .orig" —
		// devolve o sufixo (com ponto) pra backup_ext do text_op.
		seen := false
		seenAt := -1
		for i := range toks {
			switch toks[i].Fold {
			case "backup", "backups", "cópia", "copia", "cópias", "copias":
				if !seen {
					seen, seenAt = true, i
				}
				continue
			}
			if !seen {
				continue
			}
			f := toks[i].Fold
			if strings.HasPrefix(f, ".") && len(f) > 1 {
				taken[i], taken[seenAt] = true, true
				return f
			}
			if nuExtWords[f] && toks[i].Quoted {
				taken[i], taken[seenAt] = true, true
				return "." + f
			}
		}
	case "exts":
		// "in '.php', '.xml' and '.phtml' files" — várias extensões viram
		// alternância ancorada pro name_re do file_find.
		var exts []string
		for i := range toks {
			f := toks[i].Fold
			var e string
			if strings.HasPrefix(f, "*.") || (strings.HasPrefix(f, ".") && len(f) > 1) {
				e = strings.TrimPrefix(strings.TrimPrefix(f, "*"), ".")
			} else if nuExtWords[f] && toks[i].Quoted {
				e = f
			}
			if e == "" {
				continue
			}
			dup := false
			for _, x := range exts {
				if x == e {
					dup = true
				}
			}
			if !dup {
				exts = append(exts, regexp.QuoteMeta(e))
			}
		}
		if len(exts) > 1 {
			return `\.(` + strings.Join(exts, "|") + `)$`
		}
	}
	return ""
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

// nuIfaceName — nome de interface de rede: forma eth0/wlan0/enp*/wlp*/
// venet*/tailscale*/lo/tun*/veth* (livre ou aspada). Regex NÃO entra —
// o script faz match de substring; "venet0:0|eth0" ficaria vacuoso.
func nuIfaceName(toks []nuTok, taken map[int]bool) string {
	ifaceShaped := func(s string) bool {
		if s == "lo" {
			return true
		}
		for _, p := range []string{"eth", "wlan", "enp", "wlp", "ens",
			"eno", "venet", "tailscale", "tun", "tap", "veth", "ppp",
			"docker", "br-", "bond", "virbr"} {
			if strings.HasPrefix(s, p) {
				return true
			}
		}
		return false
	}
	for i, t := range toks {
		if taken[i] {
			continue
		}
		if !ifaceShaped(t.Raw) && !ifaceShaped(t.Fold) {
			continue
		}
		if strings.ContainsAny(t.Raw, "|()[]") {
			continue // regex — o script não aplica
		}
		taken[i] = true
		return t.Raw
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

// nuPkgDep picks the dependency/package operand: the name right after a
// package/install cue ("pacote requests", "install flask"), skipping
// package-manager phrasing ("npm install requests" → requests, não npm).
// Fallbacks: quoted free token, then last name-ish free word.
func nuPkgDep(toks []nuTok, taken map[int]bool) string {
	mgrWord := func(s string) bool {
		switch s {
		case "pip", "pip3", "npm", "npx", "yarn", "brew", "gem",
			"cargo", "go", "apt", "apt-get", "yum", "dnf", "pacman",
			"zypper", "apk", "port", "install", "installer", "instal",
			"instala", "instale", "instalado", "instalada", "installed",
			"instalar", "update", "updates", "upgrade", "atualiza",
			"atualize", "baixa", "baixar", "download", "get":
			return true
		}
		return false
	}
	// 1) cue dep/install — o nome está na janela à frente (ou na aspa
	// imediatamente antes, "updates 'openssl' package"). Filler que
	// não é artigo/preposição É candidato: "requests" é filler no
	// léxico e é o nome do pacote mais pedido do mundo.
	depCand := func(j int) bool {
		u := &toks[j]
		if taken[j] {
			return false
		}
		if u.has("dep") || mgrWord(u.Fold) {
			return false
		}
		if strings.ContainsAny(u.Raw, ",;.:") {
			return false
		}
		if u.Quoted || nuNameShaped(u.Raw) {
			return true
		}
		// palavra nua sem conceito — nome de pacote comum ("requests")
		return len(u.Concs) == 0 && len(u.Fold) > 1
	}
	for i, t := range toks {
		if taken[i] {
			continue
		}
		if !t.has("dep") && !mgrWord(t.Fold) {
			continue
		}
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			u := &toks[j]
			if u.Filler {
				switch u.Fold {
				case "o", "a", "os", "as", "the", "um", "uma",
					"de", "do", "da", "para", "for", "to",
					"no", "na", "em", "in", "of":
					continue // artigo/prep — transparente
				}
				// filler real ("requests") é o nome — para aqui
			}
			if u.has("dep") || mgrWord(u.Fold) {
				continue // phrasing do gerenciador/cue, não nome
			}
			if depCand(j) {
				taken[j] = true
				return u.Raw
			}
			break // palavra de conteúdo não-nomeável fecha a janela
		}
	}
	// 2) aspa livre ('mysql')
	for i, t := range toks {
		if taken[i] || !t.Quoted || t.Filler {
			continue
		}
		if mgrWord(t.Fold) || strings.ContainsAny(t.Raw, ",;") {
			continue
		}
		taken[i] = true
		return t.Raw
	}
	// 3) última palavra nomeável livre
	best := -1
	for i, t := range toks {
		if taken[i] || t.Filler || t.Quoted {
			continue
		}
		if mgrWord(t.Fold) || t.has("dep") {
			continue
		}
		if nuNameShaped(t.Raw) || len(t.Concs) == 0 && len(t.Fold) > 1 {
			best = i
		}
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
		dir, file, link := false, false, false
		for i, t := range toks {
			// "files/directories" tokeniza como um átomo só — a união
			// pede os DOIS tipos (o default "any" do script responde).
			// Um path real ("/path/to/dir") também tem '/': só é
			// união de tipos quando TODA parte é substantivo-classe.
			if strings.Contains(t.Fold, "/") {
				classWords := map[string]bool{
					"file": true, "files": true, "arquivo": true,
					"arquivos": true, "dir": true, "dirs": true,
					"directory": true, "directories": true,
					"folder": true, "folders": true, "pasta": true,
					"pastas": true, "diretorio": true, "diretório": true,
					"diretorios": true, "diretórios": true,
					"symlink": true, "symlinks": true, "link": true,
					"links": true,
				}
				union := true
				for _, part := range strings.Split(t.Fold, "/") {
					if !classWords[part] {
						union = false
					}
				}
				if union {
					for _, part := range strings.Split(t.Fold, "/") {
						switch part {
						case "file", "files", "arquivo", "arquivos":
							file = true
						case "dir", "dirs", "directory", "directories",
							"folder", "folders", "pasta", "pastas",
							"diretorio", "diretório", "diretorios", "diretórios":
							dir = true
						case "symlink", "symlinks", "link", "links":
							link = true
						}
					}
				}
				continue
			}
			switch {
			// substantivo-link é TIPO quando nenhum outro slot o
			// consumiu ("2 links" quantificado vira links:2, não tipo)
			case t.has("linknoun") || t.Fold == "symlink" ||
				t.Fold == "symlinks" || t.Fold == "link" || t.Fold == "links" ||
				t.Fold == "atalho" || t.Fold == "atalhos":
				if !taken[i] {
					link = true
					taken[i] = true
				}
			}
			switch t.Fold {
			case "pasta", "pastas", "dir", "dirs", "diretorio", "diretório", "diretorios", "diretórios", "directory", "directories", "folder", "folders", "subpasta", "subpastas",
				"subfolder", "subfolders", "subdirectory", "subdirectories",
				"subdir", "subdirs":
				// "in the current directory" — a dir-word depois de
				// preposição de escopo é LOCALIZAÇÃO, não tipo pedido
				scope := false
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					// preposições de escopo são fillers — checar a fold
					// ANTES do skip, senão "in" some na peneira
					switch toks[j].Fold {
					case "of":
						// "names/list/count OF all directories" — o
						// descritor de saída faz genitivo: a classe é o
						// ALVO. "files of the dir" é locativo de verdade
						scope = true
						for k := j - 1; k >= 0 && k >= j-3; k-- {
							u := &toks[k]
							if u.Filler || u.Slang {
								continue
							}
							if u.has("nameattr") || u.has("intocue") ||
								u.has("content") {
								switch u.Fold {
								case "name", "names", "list", "lista",
									"count", "number", "contents",
									"conteudo", "conteúdo", "sizes",
									"quantity", "quantidade", "total":
									scope = false
								}
							}
							break
						}
						if !scope {
							continue
						}
						fallthrough
					case "in", "on", "em", "no", "na", "under", "inside", "sob", "through", "within",
						"from", "de", "do", "da":
						if toks[j].Fold != "of" {
							scope = true
						}
						// "chmod on all directories" — prep após verbo
						// de mutação marca o ALVO, não o lugar
						for k := j - 1; k >= 0 && k >= j-3; k-- {
							u := &toks[k]
							if u.Filler || u.Slang || u.has("permattr") ||
								nuOctalShaped(u.Fold) || nuIsNumber(u.Fold) {
								continue // '755'/'u+x' são operandos do verbo
							}
							if u.has("mutate") || u.has("execperm") ||
								u.has("setperm") || u.has("delete") ||
								u.has("create") || u.has("move") ||
								u.has("copy") {
								scope = false
							}
							break
						}
					}
					if toks[j].Filler || scope {
						continue
					}
					break
				}
				// "all directories under /srv" — a dir-word continua
				// sendo a CLASSE do objeto quando a localização real
				// vem numa prep-escopo à frente; sem âncora à frente,
				// "in the current directory" é só lugar (scope fica)
				if scope {
					for j := i + 1; j < len(toks) && j <= i+3; j++ {
						u := &toks[j]
						switch u.Fold {
						case "under", "inside", "within", "sob", "through":
							for k := j + 1; k < len(toks) && k <= j+3; k++ {
								if toks[k].Filler || toks[k].Slang {
									continue
								}
								if toks[k].Fold != "" {
									scope = false // âncora real = o lugar
								}
								break
							}
						}
						if u.Filler || u.Slang {
							continue
						}
						break
					}
				}
				// "the X directory tree/structure" — a dir-word é o
				// NOME do recorte (a hierarquia sob X), não o tipo de
				// objeto pedido: sem isso "broken symlinks in the X
				// directory tree" virava type:dir e ignorava os links
				if !scope {
					for j := i + 1; j < len(toks) && j <= i+2; j++ {
						u := &toks[j]
						switch u.Fold {
						case "tree", "trees", "structure", "hierarchy",
							"arvore", "árvore", "arvores", "árvores",
							"estrutura", "estruturas", "hierarquia":
							// a palavra-lugar pode já estar marcada
							// Filler (normalize) — o fold decide
							scope = true
							taken[j] = true
						}
						if u.Filler || u.Slang {
							continue
						}
						break
					}
				}
				if !scope {
					dir = true
				}
			case "archive", "archives", "tarball", "tarballs",
				"zipfile", "zipfiles", "arquivo compactado":
				// substantivo-pacote buscado é sempre arquivo
				// regular — "find .gz archives" é type:file
				file = true
			case "arquivo", "arquivos", "file", "files":
				// "contained files"/"containing files" — o substantivo-
				// file é a métrica do sort ("number of contained
				// files"), não o objeto da busca
				metric := false
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					u := &toks[j]
					if u.Filler {
						continue
					}
					switch u.Fold {
					case "contained", "containing", "contain", "contido",
						"contida", "contidos", "neles", "nelas", "inside",
						"within", "dentro":
						metric = true
					}
					break
				}
				if !metric {
					file = true
				}
			case "mkdir":
				// verbo-literal: "mkdir data" pede um diretório, sem
				// depender de substantivo — o scope-check não se aplica
				dir = true
			}
		}
		if link && !dir && !file {
			return "link"
		}
		if dir == file {
			return "" // both or neither: the script's "any" default applies
		}
		if dir {
			return "dir"
		}
		return "file"
	case "links":
		// contagem de hardlinks: "exactly 2 links"/"have 2 links" —
		// o número adjacente quantifica o substantivo link
		for i, t := range toks {
			if taken[i] || (!t.has("linknoun") && t.Fold != "link" &&
				t.Fold != "links") {
				continue
			}
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				if nuIsNumber(u.Fold) {
					taken[i], taken[j] = true, true
					return u.Fold
				}
				break
			}
		}
		return ""
	case "orphan", "not_orphan":
		// symlink quebrado: "broken/dangling/cyclic/orphaned links" —
		// o qualificador tem que valer pra palavra-link (não pra
		// arquivo qualquer: "broken files" não é predicado suportado).
		// "not broken"/"which are not dangling" = link com alvo vivo.
		broken := map[string]bool{
			"broken": true, "dangling": true, "cyclic": true,
			"dead": true, "orphaned": true, "stale": true,
			"quebrado": true, "quebrada": true, "quebrados": true,
			"quebradas": true, "orfao": true, "orfão": true,
			"orfa": true, "orfas": true, "órfã": true, "orfãs": true,
			"corrompido": true, "corrompida": true,
		}
		for i, t := range toks {
			if taken[i] || !broken[t.Fold] {
				continue
			}
			if nuNegationNear(toks, i) != (what == "not_orphan") {
				continue
			}
			// "links in the current directory which are not broken" —
			// a cláusula relativa põe o link até ~10 tokens atrás
			for j := i - 10; j <= i+4 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				u := &toks[j]
				if u.has("linknoun") || u.Fold == "link" || u.Fold == "links" {
					// todo qualificador-broken do grupo é predicado —
					// "broken and cyclic links" não deixa 'cyclic' solto
					for k := i - 4; k <= i+4 && k < len(toks); k++ {
						if k >= 0 && broken[toks[k].Fold] {
							taken[k] = true
						}
					}
					// o substantivo-link NÃO é marcado: ele é também o
					// TYPE do pedido ("broken symlinks" = links órfãos)
					// — o slot type o reivindica como type:link. Marcar
					// aqui deixaria "directory tree" roubar o tipo.
					return "true"
				}
			}
		}
		// "links that point to nothing"/"apontam pra lugar nenhum" —
		// alvo inexistente é o mesmo predicado órfão
		if what == "orphan" {
			for i, t := range toks {
				switch t.Fold {
				case "point", "points", "pointing", "pointe",
					"aponta", "apontam", "apontando", "lead", "leads":
				default:
					continue
				}
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					switch u.Fold {
					case "to", "para", "pra", "pro", "at", "em":
						continue // direção é transparente: "point to nothing"
					}
					switch u.Fold {
					case "nothing", "nowhere", "nonexistent",
						"non-existent", "inexistente", "nada",
						"null", "void", "limbo", "dead":
						for k := i - 10; k <= j && k < len(toks); k++ {
							if k < 0 {
								continue
							}
							if toks[k].has("linknoun") ||
								toks[k].Fold == "link" ||
								toks[k].Fold == "links" {
								taken[i], taken[j] = true, true
								return "true"
							}
						}
					}
					if !u.Filler {
						break
					}
				}
			}
		}
		return ""
	case "follow":
		// "following symlinks"/"seguindo links" — o verbo follow só
		// vale como flag -L quando o objeto é link ("the following
		// files" é phrasing, nunca traversal)
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "follow", "follows", "following", "followed",
				"seguir", "seguindo", "siga", "sigam":
			default:
				continue
			}
			for j := i - 3; j <= i+3 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				u := &toks[j]
				if u.has("linknoun") || u.Fold == "link" || u.Fold == "links" ||
					u.Fold == "symlink" || u.Fold == "symlinks" {
					taken[i], taken[j] = true, true
					return "true"
				}
			}
		}
		return ""
	case "sort":
		for i, t := range toks {
			if taken[i] {
				continue
			}
			switch t.Fold {
			case "larger", "bigger", "smaller":
				// comparativo EN é predicado de TAMANHO ("larger than
				// 1MB" → min_size), nunca ordenação — superlativo
				// (largest) é que pede sort; sem 'than' fica ambíguo
				continue
			case "maior", "menor", "maiores", "menores":
				// PT "os maiores arquivos" é superlativo; "maior que
				// 1MB" é predicado de tamanho → não ordena
				cmp := false
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					u := &toks[j]
					if u.Filler {
						continue
					}
					cmp = u.Fold == "que" || u.Fold == "de" ||
						u.Fold == "do" || u.Fold == "than"
					break
				}
				if cmp {
					continue
				}
				taken[i] = true
				return "size"
			case "largest", "biggest", "heaviest", "smallest",
				"pesado", "pesada", "pesados", "pesadas":
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
			// "sort them based on name"/"ordenar por tamanho" — o
			// verbo sort seguido de ponte (by/on/based/em) + campo
			case "sort", "sorting", "ordenar", "ordenando",
				"classificar", "classificando", "arrange":
				for j := i + 1; j < len(toks) && j <= i+5; j++ {
					u := &toks[j]
					if u.Filler || u.Fold == "by" || u.Fold == "on" ||
						u.Fold == "por" || u.Fold == "em" || u.Fold == "de" ||
						u.Fold == "based" || u.Fold == "baseado" ||
						u.Fold == "baseada" || u.Fold == "them" ||
						u.Fold == "eles" || u.Fold == "the" {
						continue
					}
					switch u.Fold {
					case "size", "tamanho", "sizes":
						taken[i], taken[j] = true, true
						return "size"
					case "time", "date", "mtime", "tempo", "data",
						"modification", "age", "idade",
						"oldest", "newest", "latest", "recent",
						"modified", "created", "accessed":
						taken[i], taken[j] = true, true
						return "mtime"
					case "name", "nome", "names", "filename", "alphabetically",
						"alphabetic", "alfabetica", "alfabeticamente",
						"alfabetico", "alfabético":
						taken[i], taken[j] = true, true
						return "name"
					case "count", "lines", "number":
						// sort-por-contagem não é suportado — devolve
						// vazio e o 'count' fica livre pra drop
						return ""
					}
					break // primeira palavra útil não era campo
				}
			// "sorted/ordered by size|time|name" — particípio + by + campo.
			// "by name" é a ordem default do find: consome a frase mas
			// não emite slot (evidência fraca pra ordenação redundante).
			case "sorted", "ordered", "arranged", "ordenado", "ordenada",
				"ordenados", "ordenadas", "classificado", "classificada":
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					u := &toks[j]
					if u.Filler || u.Fold == "by" || u.Fold == "por" ||
						u.Fold == "de" || u.Fold == "em" {
						continue
					}
					switch u.Fold {
					case "size", "tamanho", "sizes":
						taken[i], taken[j] = true, true
						return "size"
					case "time", "date", "mtime", "tempo", "data",
						"modification", "age", "idade",
						"oldest", "newest", "latest", "recent",
						"modified", "created", "accessed":
						taken[i], taken[j] = true, true
						return "mtime"
					case "name", "nome", "names", "filename":
						taken[i], taken[j] = true, true
						return "name"
					}
					// campo não suportado ("number of files") fica livre
					// → unfed honesto em vez de ordenação errada calada
					return ""
				}
				taken[i] = true
			}
		}
	case "newer", "older":
		return nuFindWindow(toks, what == "newer", taken)
	case "newer_file", "older_file":
		// "newer than report.txt": o alvo já foi marcado `timeref` no
		// normalize (cue newer/older + than implícito + forma de arquivo)
		newer := what == "newer_file"
		cueAt := -1
		for i, t := range toks {
			isCue := (newer && (t.Fold == "newer" || t.Fold == "novo" ||
				t.Fold == "nova" || t.Fold == "recente")) ||
				(!newer && (t.Fold == "older" || t.Fold == "velho" ||
					t.Fold == "velha" || t.Fold == "antigo" || t.Fold == "antiga"))
			if isCue {
				cueAt = i
				break
			}
		}
		if cueAt < 0 {
			return ""
		}
		for j := cueAt + 1; j < len(toks) && j <= cueAt+5; j++ {
			u := &toks[j]
			if !u.has("timeref") || nuTemporalVocab[u.Fold] {
				continue // unidade de tempo não é arquivo-referência
			}
			taken[cueAt], taken[j] = true, true
			return u.Raw
		}
		return ""
	case "min_size", "max_size":
		return nuFindSize(toks, what == "min_size", taken)
	case "ext":
		// "backups with .bakup extension" — o .bakup é sufixo do backup,
		// não escopo de busca: pula .xxx seguido de "extension" quando um
		// backup-word aparece antes.
		bakAt := -1
		for i := range toks {
			switch toks[i].Fold {
			case "backup", "backups", "cópia", "copia", "cópias", "copias":
				if bakAt < 0 {
					bakAt = i
				}
			}
		}
		var exts []string
		addExt := func(e string) {
			for _, x := range exts {
				if x == e {
					return
				}
			}
			exts = append(exts, e)
		}
		for i := range toks {
			if taken[i] {
				continue // "except xml files": o xml já é alvo do exclude
			}
			if toks[i].has("nametgt") || toks[i].has("forquoted") {
				continue // a aspa é predicado de nome/conteúdo, não ext
			}
			f := toks[i].Fold
			// ext é ".log"/"*.log" — "./storage/" começa com ponto mas é
			// caminho; o sufixo tem que ser alfanumérico como no nuPureExt
			extShaped := strings.HasPrefix(f, ".") && len(f) > 1 &&
				!strings.ContainsAny(f, "/\\") && nuPureExt(f)
			if strings.HasPrefix(f, "*.") || extShaped {
				if bakAt >= 0 && i > bakAt && i+1 < len(toks) {
					switch toks[i+1].Fold {
					case "extension", "extensions", "ext", "extensão", "extensões", "extensao", "extensoes", "suffix", "sufixo":
						continue
					}
				}
				taken[i] = true
				// lista ".cpp, .c, .h" — todas são filtro, não só a 1ª
				addExt(strings.TrimPrefix(strings.TrimPrefix(f, "*"), "."))
				continue
			}
			// "text files"/"arquivos de texto" — convenção do corpus:
			// é o glob *.txt, não um tipo de conteúdo qualquer
			if f == "text" || f == "texto" || f == "textos" {
				isFileCtx := false
				for _, j := range []int{i + 1, i + 2, i - 1, i - 2} {
					if j < 0 || j >= len(toks) {
						continue
					}
					switch toks[j].Fold {
					case "file", "files", "arquivo", "arquivos":
						isFileCtx = true
					}
				}
				if isFileCtx {
					taken[i] = true
					addExt("txt")
					continue
				}
			}
			// "C, CPP, Header files" — convenção do corpus: header = *.h
			if f == "header" || f == "headers" {
				isFileCtx := false
				for _, j := range []int{i + 1, i - 1} {
					if j < 0 || j >= len(toks) {
						continue
					}
					switch toks[j].Fold {
					case "file", "files", "arquivo", "arquivos":
						isFileCtx = true
					}
				}
				if isFileCtx {
					taken[i] = true
					addExt("h")
					continue
				}
			}
			if nuExtWords[f] {
				// "chgrp to h"/"muda para zip": a palavra-ext depois de
				// to/para/into é DESTINO (usuário, formato-alvo), não
				// filtro "*.h" — o alias .ext com ponto segue valendo
				if i > 0 {
					switch toks[i-1].Fold {
					case "to", "para", "into", "pra", "pro", "por", "by":
						continue
					}
				}
				// "names begin/end with log": depois de âncora de nome
				// (begin/start/end/prefix/suffix, ou with/com sob
				// nameattr), a palavra-ext é literal de prefixo/sufixo —
				// o namepat gera "log*"/"*log", não um filtro "*.log"
				anchor := false
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					p := &toks[j]
					if p.Filler || p.has("article") {
						continue
					}
					switch p.Fold {
					case "begin", "begins", "beginning", "start", "starts",
						"starting", "prefix", "prefixed", "end", "ends",
						"ending", "suffix", "suffixed":
						anchor = true
					case "with", "com":
						for k := j - 1; k >= 0; k-- {
							if toks[k].has("nameattr") {
								anchor = true
								break
							}
						}
					}
					break
				}
				if anchor {
					continue
				}
				// "a zip file": ext-word + file substantivo + artigo
				// indefinido é o NOME do artefato, não um filtro de
				// extensão — "the .zip files" (plural/definido) segue filtro.
				if i > 0 && i+1 < len(toks) {
					nxt := toks[i+1].Fold
					// "a tar OF files": formato do artefato, não filtro
					if nxt == "file" || nxt == "arquivo" || nxt == "of" || nxt == "de" {
						switch toks[i-1].Fold {
						case "a", "an", "um", "uma":
							continue
						}
					}
				}
				taken[i] = true
				addExt(f)
			}
		}
		if len(exts) > 0 {
			return strings.Join(exts, ",")
		}
	case "name_re":
		// "names match regex '^x$'" / "nome casa regex 'x'" — only runs when a
		// regex/match cue is present, otherwise plain 'name' owns the pattern.
		// "regular" só é cue com "expression" junto — "regular files" é
		// -type f, não regexp.
		cue, strongRe := false, false
		for i, t := range toks {
			switch t.Fold {
			case "regex", "regexp":
				cue, strongRe = true, true
			case "regular":
				for j := i + 1; j < len(toks) && j <= i+2; j++ {
					switch toks[j].Fold {
					case "expression", "expressions", "expressao", "expressão":
						cue, strongRe = true, true
					}
					if !toks[j].Filler {
						break
					}
				}
			case "expressao", "expressão", "match", "matching", "matches", "casa", "case", "pattern", "padrão", "padrao":
				cue = true
			}
		}
		if !cue {
			return ""
		}
		// glob não é regex: "pattern '*.rb'" quer name=*.rb, não name_re
		// quebrando no compile. Só vira name_re com sintaxe de expressão
		// de verdade (^ $ | + ( ) \) ou cue explícita "regex".
		globShaped := func(s string) bool {
			return strings.ContainsAny(s, "*?[") && !strings.ContainsAny(s, "^$|+()\\")
		}
		reMeta := func(s string) bool {
			return strings.ContainsAny(s, "^$|+()\\")
		}
		// cue-words de domínio ("regex", "regular expression", "matching")
		// são consumidos junto — senão o sponge os cola como name literal
		eatReCues := func() {
			for i, t := range toks {
				switch t.Fold {
				case "regex", "regexp", "regular", "expression", "expressions",
					"expressao", "expressão", "match", "matching", "matches":
					taken[i] = true
				}
			}
		}
		for i, t := range toks {
			// quoted literal sem meta-regex é NOME ('your_pattern'),
			// não expressão — só "regex 'abc'" explícito força name_re
			if t.Quoted && t.Fold != "" && !globShaped(t.Fold) && (strongRe || reMeta(t.Fold)) {
				taken[i] = true
				eatReCues()
				return t.Fold
			}
		}
		for i := len(toks) - 1; i >= 0; i-- {
			t := &toks[i]
			if t.Fold != "" && !t.Filler && len(t.Concs) == 0 && !taken[i] && nuNameShaped(t.Raw) && !globShaped(t.Fold) && reMeta(t.Fold) {
				taken[i] = true
				eatReCues()
				return t.Fold
			}
		}
	case "exclude":
		// "except git files" / "exceto os .o" / "excluding paths
		// containing 'a'" / "except alpha and beta" — os operandos vêm
		// depois do cue atravessando pontes; listas saem unidas por "|"
		// (o script casa cada glob). O que a cláusula marca NUNCA é
		// alvo de outro slot — a invariante pós-fill (Parse) reembola
		// aqui o que outro extrator roubou ("except in tmp" → dir).
		cues, ops, last := nuExceptOperands(toks, taken)
		if len(ops) > 0 {
			var parts []string
			for _, c := range cues {
				// o span cue→último operando é phrasing da cláusula
				// ("omitting directory 'x'" consome o 'directory'
				// ponte; "except those named foo" consome 'those named')
				for j := c; j <= last[c] && j < len(toks); j++ {
					taken[j] = true
				}
			}
			for _, j := range ops {
				parts = append(parts, nuExcludeRaw(&toks[j]))
			}
			return strings.Join(parts, "|")
		}
	case "min_depth", "max_depth":
		return nuFindDepth(toks, what == "min_depth", taken)
	case "uid", "gid":
		// "with uid=1000" / "uid:1000" / "uid 1000" — find -uid é
		// predicado de dono NUMÉRICO, nunca um nome literal
		for i, t := range toks {
			f := t.Fold
			if strings.HasPrefix(f, what+"=") || strings.HasPrefix(f, what+":") {
				v := f[len(what)+1:]
				if nuIsNumber(v) {
					taken[i] = true
					return v
				}
			}
			if f == what && i+1 < len(toks) && nuIsNumber(toks[i+1].Fold) {
				taken[i], taken[i+1] = true, true
				return toks[i+1].Fold
			}
			// "user id 1000"/"group id 1000" — o 'id' fecha o par
			if f == "id" && i > 0 && i+1 < len(toks) && nuIsNumber(toks[i+1].Fold) {
				w := toks[i-1].Fold
				isUID := w == "user" || w == "usuario" || w == "usuário" || w == "owner" || w == "dono"
				isGID := w == "group" || w == "grupo"
				if (what == "uid" && isUID) || (what == "gid" && isGID) {
					taken[i-1], taken[i], taken[i+1] = true, true, true
					return toks[i+1].Fold
				}
			}
		}
		return ""
	case "newer_date", "older_date":
		return nuFindDateBound(toks, what == "newer_date", taken)
	case "icase":
		// "(case insensitive)": modificador de match que pode vir em 1 ou
		// 2 tokens. "case SENSITIVE" volta marcador — o match do glob é
		// sempre insensitivo e fingir o contrário seria silent wrong.
		ins, sens := false, false
		near := func(i int, words ...string) bool {
			for j := i - 3; j <= i+3; j++ {
				if j < 0 || j >= len(toks) || j == i {
					continue
				}
				for _, w := range words {
					if toks[j].Fold == w {
						return true
					}
				}
			}
			return false
		}
		for i, t := range toks {
			switch t.Fold {
			case "case-insensitive", "case-insensitively", "caseinsensitive",
				"caseless", "nocase", "any-cased", "anycase", "any-case":
				ins = true
			case "case-sensitive", "case-sensitively":
				sens = true
			case "insensitive", "insensitively":
				ins = ins || near(i, "case")
				ins = ins || t.Fold == "insensitively"
			case "sensitive", "sensitively":
				sens = sens || near(i, "case")
			case "case":
				sens = sens || near(i, "sensitive", "sensitively")
				ins = ins || near(i, "insensitive", "insensitively",
					"ignoring", "ignore", "regardless", "without")
			case "ignoring", "ignore", "ignorando", "regardless":
				ins = ins || near(i, "case", "casing")
			case "diferenciar", "diferenciando", "distinction":
				ins = ins || near(i, "maiúsculas", "minúsculas", "maiusculas",
					"minusculas", "maiúscula", "minúscula", "case")
			case "maiúsculas", "maiusculas", "minúsculas", "minusculas",
				"maiúscula", "minúscula":
				ins = ins || near(i, "diferenciar", "diferenciando",
					"ignorando", "ignore", "sem", "ou", "e")
			}
		}
		if sens && !ins {
			return "sensitive"
		}
		if ins {
			return "1"
		}
		return ""
	case "namelen":
		// "names are 33 characters long" → name="?×N" (fnmatch ? = 1
		// char). Só comprimento EXATO — "shorter/longer than N" glob
		// não expressa e fica de fora honestamente.
		for i, t := range toks {
			switch t.Fold {
			case "name", "names", "nome", "nomes", "filename", "filenames", "pathname", "pathnames":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+6; j++ {
				if !nuIsNumber(toks[j].Fold) {
					continue
				}
				// comparativo de comprimento não é exato — recusa
				lessMore := false
				for k := j - 3; k < j+3 && k < len(toks); k++ {
					if k < 0 {
						continue
					}
					switch toks[k].Fold {
					case "shorter", "longer", "less", "fewer", "more", "greater", "menor", "menores", "maior", "maiores", "menos", "mais":
						lessMore = true
					}
				}
				if lessMore {
					return ""
				}
				for k := j + 1; k < len(toks) && k <= j+2; k++ {
					switch toks[k].Fold {
					case "characters", "character", "chars", "char", "caracteres", "caractere", "letras", "letters":
						if n, err := strconv.Atoi(toks[j].Fold); err == nil && n >= 1 && n <= 200 {
							taken[i], taken[j], taken[k] = true, true, true
							if k+1 < len(toks) {
								switch toks[k+1].Fold {
								case "long", "comprido", "comprida":
									taken[k+1] = true
								}
							}
							return strings.Repeat("?", n)
						}
					}
				}
			}
		}
		return ""
	case "out":
		// "print the list to a file named 'rm'" — a aspa marcada como
		// outtgt é o arquivo de saída; desmarcada, "to file X"/"tee X"
		// também resolve.
		for i, t := range toks {
			if t.has("outtgt") {
				taken[i] = true
				return t.Raw
			}
		}
		// "print ... to filesOfInterest.txt file"/"save into out.txt" —
		// 'to' + token com forma de arquivo é destino de saída
		for i, t := range toks {
			if taken[i] || (t.Fold != "to" && t.Fold != "into" &&
				t.Fold != "para" && t.Fold != "tee") {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "a" || u.Fold == "the" ||
					u.Fold == "o" || u.Fold == "um" {
					continue
				}
				if taken[j] || !nuFileShaped(u.Fold) {
					break
				}
				// "to file X"/"to filesOfInterest.txt file" — o par
				// arquivo+classe-fechando fecha a cláusula
				taken[i], taken[j] = true, true
				if j+1 < len(toks) && (toks[j+1].Fold == "file" ||
					toks[j+1].Fold == "arquivo") {
					taken[j+1] = true
				}
				return u.Raw
			}
		}
		return ""
	case "namepat":
		// "files with spaces in their names" → glob '* *' — o predicado
		// é "nome contém espaço", não a palavra 'spaces'.
		for i, t := range toks {
			switch t.Fold {
			case "space", "spaces", "espaço", "espaco", "espaços", "espacos":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+5; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "name", "names", "nome", "nomes", "filename", "filenames":
					taken[i], taken[j] = true, true
					return "* *"
				}
				break
			}
			// "names contain spaces" — o substantivo de nome veio ANTES
			// ('whose names contain' já vetou o grep): olha pra trás.
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "contain", "contains", "containing", "contem", "contém",
					"have", "has", "with", "com":
					taken[i], taken[j] = true, true
					return "* *"
				}
				break
			}
		}
		// "first letter e or f and last (letter|one) x" → glob ancorado:
		// fnmatch casa classe [ef] e sufixo literal — "e*x" e "f*x".
		firsts, lasts := "", ""
		for i, t := range toks {
			pos := ""
			switch t.Fold {
			case "first", "primeiro", "primeira", "inicial", "iniciais":
				pos = "first"
			case "last", "final", "ultimo", "último", "ultima", "última":
				pos = "last"
			case "letter", "letters", "letra", "letras", "caractere",
				"one", "um", "uma":
				// "letter"/"one" precisam do marcador de posição antes
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					p := &toks[j]
					switch p.Fold {
					case "first", "primeiro", "primeira", "inicial", "iniciais":
						pos = "first"
					case "last", "final", "ultimo", "último", "ultima", "última":
						pos = "last"
					}
					if !p.Filler && !p.Slang {
						break
					}
				}
			}
			if pos == "" {
				continue
			}
			// anchor direto em pos-word ("last x files" = último N, não
			// sufixo) exige cue de letra antes de letra nua; letra
			// citada dispensa o cue. Anchor em "letter"/"one" já é cue.
			cueSeen := toks[i].Fold == "letter" || toks[i].Fold == "letters" ||
				toks[i].Fold == "letra" || toks[i].Fold == "letras" ||
				toks[i].Fold == "caractere" || toks[i].Fold == "one" ||
				toks[i].Fold == "um" || toks[i].Fold == "uma" ||
				strings.Contains(toks[i].Raw, "one") ||
				strings.Contains(toks[i].Raw, "letter")
			var got []string
			var idx []int
			for j := i + 1; j < len(toks) && j <= i+6; j++ {
				u := &toks[j]
				// letra de 1 rune citada OU confirmada por cue de letra:
				// candidata mesmo sendo filler ("first letter e or f" —
				// o 'e' nu é filler PT mas aqui é operando real)
				if len([]rune(u.Fold)) == 1 && len(u.Concs) == 0 && !taken[j] &&
					(u.Quoted || cueSeen) {
					got = append(got, u.Fold)
					idx = append(idx, j)
					continue
				}
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "letter", "letters", "letra", "letras", "caractere",
					"one", "um", "uma":
					cueSeen = true
					continue
				case "or", "ou", "and", ",":
					continue
				}
				break
			}
			if len(got) > 0 {
				seg := got[0]
				if len(got) > 1 {
					seg = "[" + strings.Join(got, "") + "]"
				}
				for _, j := range idx {
					taken[j] = true
				}
				taken[i] = true
				if pos == "first" {
					firsts = seg
				} else {
					lasts = seg
				}
			}
		}
		if firsts != "" || lasts != "" {
			pat := firsts + "*"
			pat += lasts
			for i, t := range toks {
				switch t.Fold {
				case "first", "last", "primeiro", "primeira", "final",
					"ultimo", "último", "ultima", "última", "inicial":
					taken[i] = true
				}
			}
			return pat
		}
		// "(names) begin/start/end with X" → glob com âncora: X* / *X.
		// A cadeia de cues nameattr ("names begin with") não é o valor.
		for i, t := range toks {
			pre, suf := "", ""
			switch t.Fold {
			case "begin", "begins", "beginning", "start", "starts",
				"starting", "prefix", "prefixed", "começa", "comeca",
				"começam", "comecam", "iniciado", "iniciados":
				suf = "*"
			case "end", "ends", "ending", "suffix", "suffixed",
				"termina", "terminam", "acaba", "acabam":
				pre = "*"
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] {
					continue
				}
				if u.has("nameattr") || u.has("grepword") || u.has("content") {
					continue // "with"/"names"/"the word" são cue/meta
				}
				// "starting with letter 'a'" — a classe ('letter')
				// é ponte; o valor é a letra citada depois dela
				switch u.Fold {
				case "letter", "letters", "letra", "letras",
					"caractere", "char", "character":
					continue
				}
				if nuIsNumber(u.Fold) || (len(u.Fold) < 2 && !u.Quoted) {
					continue
				}
				taken[j] = true
				return pre + u.Raw + suf
			}
		}
		// "hidden files"/"dot files" — o conjunto dos dotfiles; dentro de
		// cláusula except o coletor de exclusão é quem consome
		for i, t := range toks {
			if taken[i] || nuInExceptClause(toks, i) {
				continue
			}
			switch t.Fold {
			case "hidden", "escondido", "escondida", "escondidos", "escondidas",
				"oculto", "oculta", "ocultos", "ocultas", "invisible", "dot":
				fileWord := func(u *nuTok) bool {
					switch u.Fold {
					case "file", "files", "arquivo", "arquivos", "dir", "dirs",
						"directory", "directories", "diretorio", "diretório",
						"diretorios", "diretórios", "pasta", "pastas",
						"folder", "folders", "ones", "ficheros":
						return true
					}
					return false
				}
				for j := i + 1; j < len(toks) && j <= i+2; j++ {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					if fileWord(u) {
						taken[i], taken[j] = true, true
						return ".*"
					}
					break
				}
				// "arquivos ocultos"/"files hidden" — adjetivo depois
				// do substantivo (PT e parte do EN)
				for j := i - 1; j >= 0 && j >= i-2; j-- {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					if fileWord(u) {
						taken[i], taken[j] = true, true
						return ".*"
					}
					break
				}
			case "dotfiles", "dot-files", "dotfile", "hiddenfiles":
				taken[i] = true
				return ".*"
			}
		}
		// "*conf* files" — glob não-citado antes de substantivo de arquivo
		// é o padrão do conjunto, mesmo sem cue ("matching").
		for i, t := range toks {
			if !t.has("glob") || t.Quoted || taken[i] {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+5; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				if u.has("icase") || u.has("matchmode") {
					continue // "$srch* (case insensitive) files" — o
					// modificador de match não fecha a janela
				}
				if u.has("files") {
					// "files"/"files/directories"/"regular files" — o
					// substantivo-classe (conc files) fecha a âncora
					taken[i] = true
					return t.Raw
				}
				j = len(toks) // a primeira palavra útil manda
			}
		}
		// "files matching 'www-backup.tar.*'" — glob citado vira padrão
		// literal do conjunto (arquivo ou archive), não regex nem ext.
		for i, t := range toks {
			if !t.Quoted || !t.has("glob") || taken[i] {
				continue
			}
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "matching", "match", "matches", "like", "named", "called",
					"pattern", "padrao", "padrão", "glob":
					taken[i] = true
					return t.Raw
				}
				j = 0 // a primeira palavra útil manda
			}
		}
		// "X files"/"arquivos X" nomeia o CONJUNTO por padrão de nome:
		// "grep 'timeout' in all config files" → name=*config* (não é
		// dir "config" nem ext). Qualificadores do conjunto ficam fora.
		qualif := map[string]bool{
			"all": true, "every": true, "each": true, "any": true,
			"todos": true, "todas": true, "todo": true, "toda": true,
			"cada": true, "qualquer": true, "quaisquer": true,
			"regular": true, "regulars": true, "normal": true, "normais": true,
			"ordinary": true, "comuns": true, "text": true, "texto": true,
			"hidden": true, "empty": true, "new": true, "old": true,
			"large": true, "small": true, "some": true, "certa": true,
			"certo": true, "other": true, "outros": true, "outras": true,
			"the": true, "a": true, "an": true, "o": true, "os": true,
			"as": true, "um": true, "uma": true, "current": true,
		}
		for i := 0; i+1 < len(toks); i++ {
			f := toks[i+1].Fold
			if f != "files" && f != "arquivos" {
				continue
			}
			u := &toks[i]
			if u.Filler || u.Slang || taken[i] || u.Quoted || nuIsNumber(u.Fold) ||
				qualif[u.Fold] || nuAllFormatWords(u.Fold) {
				continue
			}
			// "config" é keynoun léxico MAS em "config files" é nome do
			// conjunto — conceitos coexistíveis não bloqueiam o filtro.
			// O candidato é palavra comum (não name-shaped): o vizinho
			// "files" é que o torna filtro de nome.
			concsOK := len(u.Concs) == 0
			for _, c := range u.Concs {
				if c == "config" || c == "keynoun" {
					concsOK = true
				}
			}
			if !concsOK || len(u.Fold) < 2 {
				continue
			}
			taken[i] = true
			return "*" + u.Fold + "*"
		}
		return ""
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
	case "lines", "min_lines", "max_lines":
		// "files that contain only one line" → lines=1; "more than 10
		// lines" → min_lines=11; "com 3 linhas" → lines=3
		for i, t := range toks {
			f := t.Fold
			if f != "line" && f != "lines" && f != "linha" && f != "linhas" {
				continue
			}
			// número antes ("10 lines") ou depois ("linhas: 10", "de 10 linhas")
			var num string
			numAt := -1
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				if toks[j].Filler {
					continue
				}
				if nuIsNumber(toks[j].Fold) {
					num, numAt = toks[j].Fold, j
				}
				break
			}
			if num == "" {
				for j := i + 1; j < len(toks) && j <= i+3; j++ {
					if toks[j].Filler {
						continue
					}
					if nuIsNumber(toks[j].Fold) {
						num, numAt = toks[j].Fold, j
					}
					break
				}
			}
			// "only one line" / "uma única linha" — palavra-numero vale
			if num == "" {
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					if toks[j].Filler {
						continue
					}
					switch toks[j].Fold {
					case "one", "single", "um", "uma", "única", "unica", "único", "unico":
						num, numAt = "1", j
					}
					break
				}
			}
			if num == "" {
				continue
			}
			// comparador perto do número: "more than", "at least", "mais de"
			cmp := ""
			for j := numAt - 1; j >= 0 && j >= numAt-4; j-- {
				fj := toks[j].Fold
				// "pelo menos N" é UMA unidade (at least) — não deixar
				// "menos" solto vencer o "pelo" que vem antes dele
				if fj == "menos" && j > 0 && toks[j-1].Fold == "pelo" {
					cmp = "min"
					break
				}
				switch fj {
				case "more", "mais", "over", "acima", "above", "greater", "maior":
					cmp = "min+1"
					// "no more than N" / "não mais de N" = no máximo N
					if j > 0 && (toks[j-1].Fold == "no" || toks[j-1].Fold == "não" || toks[j-1].Fold == "nao") {
						cmp = "max"
					}
				case "least", "minimo", "mínimo", "minimum", "pelo":
					cmp = "min"
				case "fewer", "less", "menos", "below", "abaixo", "under":
					cmp = "max-1" // "menos de 20" = 19 no máximo
				case "maximo", "máximo", "maximum":
					cmp = "max"
				case "only", "somente", "só", "just", "exactly", "exatamente", "exata":
					cmp = "exact"
				}
				if cmp != "" {
					break
				}
				if nuIsNumber(toks[j].Fold) || toks[j].Filler {
					continue
				}
				break
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				continue
			}
			switch what {
			case "lines":
				if cmp == "" || cmp == "exact" {
					taken[i], taken[numAt] = true, true
					return num
				}
			case "min_lines":
				if cmp == "min+1" {
					taken[i], taken[numAt] = true, true
					return strconv.Itoa(n + 1)
				}
				if cmp == "min" {
					taken[i], taken[numAt] = true, true
					return num
				}
			case "max_lines":
				if cmp == "max" {
					taken[i], taken[numAt] = true, true
					return num
				}
				if cmp == "max-1" && n > 1 {
					taken[i], taken[numAt] = true, true
					return strconv.Itoa(n - 1)
				}
			}
			return ""
		}
	case "empty", "not_empty":
		for i, t := range toks {
			switch t.Fold {
			case "vazio", "vazia", "vazios", "vazias", "empty", "empties":
			default:
				continue
			}
			neg := nuNegationNear(toks, i)
			if neg != (what == "not_empty") {
				continue
			}
			taken[i] = true
			return "true"
		}
	case "name_cued":
		// delete_file's name is a FILTER, not the leftover-name sponge that
		// file_find uses: it only fills when a nameattr cue ("chamados X",
		// "named X") is present — a bare filename is target material.
		// "names begin/end with X": a cadeia de cues (names/begin/with são
		// TODOS nameattr) não é o valor — o glob preserva prefixo/sufixo.
		for i, t := range toks {
			if !t.has("nameattr") {
				continue
			}
			pre, suf := "", ""
			for j := i + 1; j < len(toks) && j <= i+5; j++ {
				u := &toks[j]
				if taken[j] || u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "begin", "begins", "beginning", "start", "starts",
					"starting", "prefix", "prefixed", "começa", "comeca",
					"começam", "comecam", "iniciado", "iniciados":
					suf = "*"
					continue
				case "end", "ends", "ending", "suffix", "suffixed",
					"termina", "terminam", "acaba", "acabam":
					pre = "*"
					continue
				}
				if u.has("nameattr") || u.has("icase") {
					continue // "names"/"with"/"filenames" são cue, não
					// valor; flag de case também ("with .tgz extension
					// (case insensitive)" não tem name "case")
				}
				taken[i], taken[j] = true, true
				return pre + u.Raw + suf
			}
			return ""
		}
		// "hidden files"/"dot files" — mass-delete só de dotfiles; sem
		// esse filtro o delete rodaria o diretório INTEIRO (catastrófico)
		for i, t := range toks {
			if taken[i] || nuInExceptClause(toks, i) {
				continue
			}
			switch t.Fold {
			case "hidden", "escondido", "escondida", "escondidos", "escondidas",
				"oculto", "oculta", "ocultos", "ocultas", "invisible", "dot":
				fileWord := func(u *nuTok) bool {
					switch u.Fold {
					case "file", "files", "arquivo", "arquivos", "dir", "dirs",
						"directory", "directories", "diretorio", "diretório",
						"diretorios", "diretórios", "pasta", "pastas",
						"folder", "folders", "ones", "ficheros":
						return true
					}
					return false
				}
				for j := i + 1; j < len(toks) && j <= i+2; j++ {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					if fileWord(u) {
						taken[i], taken[j] = true, true
						return ".*"
					}
					break
				}
				// "arquivos ocultos"/"files hidden" — adjetivo depois
				// do substantivo (PT e parte do EN)
				for j := i - 1; j >= 0 && j >= i-2; j-- {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					if fileWord(u) {
						taken[i], taken[j] = true, true
						return ".*"
					}
					break
				}
			case "dotfiles", "dot-files", "dotfile", "hiddenfiles":
				taken[i] = true
				return ".*"
			}
		}
		// sem cue de nome: um GLOB solto ainda é o filtro do delete —
		// "Delete all 1US* files" tem "1us*" explícito e sem isso o
		// mass-delete rodaria SEM nome (catastrófico)
		for i, t := range toks {
			if taken[i] || t.Filler || t.has("nametgt") || t.has("forquoted") ||
				t.has("grepword") || t.has("nameattr") || t.has("icase") {
				continue
			}
			if strings.ContainsAny(t.Raw, "*?[") && !strings.Contains(t.Raw, "/") {
				taken[i] = true
				return t.Raw
			}
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
			"into": true,
			// "remove X from DIR"/"arquivos da pasta Y" — from/de é
			// cue de ORIGEM=escopo; sem ele o path vazava pro target e
			// o delete mass apagava a pasta inteira. ("of" inglês é
			// genitivo — "count of duplicate lines" não tem dir
			// "duplicate" — e fica de fora; gated por arcCtx abaixo)
			"from": true, "de": true, "do": true, "da": true,
			"dos": true, "das": true,
			// "2 levels down the temp paths" — o 'down' da frase de
			// profundidade é também o cue do escopo
			"down": true, "abaixo": true,
		}
		scopeFillers := map[string]bool{
			"projeto": true, "project": true, "repo": true, "repositorio": true,
			"repositório": true, "repository": true, "current": true,
			"atual": true, "geral": true,
			"root": true, "aqui": true, "here": true, "isso": true, "isto": true,
			"arquivos": true, "files": true, "file": true, "arquivo": true,
			// "directory tree"/"folder structure" = este projeto recursivo,
			// não um diretório chamado tree; "in its subdirectories" idem
			"tree": true, "hierarchy": true, "structure": true, "subtree": true,
			"subdir": true, "subdirs": true, "subdirectory": true,
			"subdirectories": true, "subfolder": true, "subfolders": true,
			// "directory tree downwards" = este projeto recursivo
			"downwards": true, "downward": true, "upwards": true,
			"upward": true, "recursively": true, "recursive": true,
			// "in all files" / "em todos os arquivos" — quantificador de
			// escopo, não nome de pasta; consumido como este-projeto
			"all": true, "every": true, "each": true, "any": true,
			"todos": true, "todas": true, "todo": true, "toda": true,
			"cada": true, "qualquer": true, "quaisquer": true,
			// typo de "current" antes de directory/folder = este
			// projeto ("in currect directory" não tem pasta 'currect')
			"currect": true, "curent": true, "currnet": true,
			"corrent": true, "corrente": true,
			// "in some directory" = escopo indefinido — não é nome real
			"some": true, "certa": true, "certo": true,
			// "in the entire/whole tree" = este projeto — não dir "entire"
			"entire": true, "whole": true, "inteiro": true, "inteira": true,
			"completo": true, "completa": true,
			// "in text files" = arquivos de texto em geral, não dir "text"
			"text": true, "texto": true,
		}
		fileWords := map[string]bool{
			"arquivos": true, "files": true, "diretorios": true, "diretórios": true,
			"pastas": true, "folders": true, "dirs": true, "directories": true,
		}
		// a unit after "in" is a format ("space in megabytes"), a time word is
		// a window ("in the last week") — neither names a directory. Units are
		// consumed as phrasing; time words stay free for the newer/older slots.
		unitWords := map[string]bool{
			"bytes": true, "byte": true, "kilobytes": true, "megabytes": true,
			"gigabytes": true, "terabytes": true, "kb": true, "mb": true,
			"gb": true, "tb": true, "kbytes": true, "mbytes": true, "gbytes": true,
		}
		// words that can never name a directory — verbs, degree/size words,
		// filesystem types — are skipped, not returned for an honest miss
		notDirWords := map[string]bool{
			"found": true, "located": true, "matching": true, "residing": true,
			"were": true, "was": true, "are": true, "being": true, "been": true,
			"existing": true, "present": true,
			"minimum": true, "maximum": true, "min": true, "max": true,
			"level": true, "levels": true, "depth": true, "depths": true,
			"pesada": true, "pesado": true, "heavy": true, "large": true,
			"small": true, "bigger": true, "smaller": true, "larger": true,
			"ext2": true, "ext3": true, "ext4": true, "ntfs": true,
			"fat32": true, "vfat": true, "btrfs": true, "xfs": true,
			"output": true, "input": true, "given": true, "specified": true,
			"specfied": true, "specfic": true, "especificado": true,
			"especificada": true, "determinado": true, "determinada": true,
			"regular": true, "normal": true, "standard": true, "certain": true,
			"particular": true, "specific": true, "common": true,
			"where": true, "path": true, "location": true, "place": true,
			// vocab de filesystem descreve o alvo, não um diretório —
			// "in ext2 partitions" não tem dir "partitions"
			"partition": true, "partitions": true,
			"volume": true, "volumes": true, "mount": true, "mounts": true,
			// unidade de contagem é atributo, não path — "with 20 lines"
			// não tem dir "lines"
			"line": true, "lines": true, "linha": true, "linhas": true,
			// atributo de tamanho nunca é path — "less than 1MB in size"
			"size": true, "tamanho": true, "bytes": true,
			// negação/verbo de permissão não é path — "lacking execute
			// permissions" não tem dir "lacking"/"execute"
			"lack": true, "lacks": true, "lacking": true, "falta": true,
			"execute": true, "execution": true, "read": true,
			"write": true, "permission": true, "permissions": true,
			"permissao": true, "permissões": true, "owned": true,
			// editor/ferramenta é o COMO, não o ONDE — "open in nano
			// text editor" não tem dir "nano"
			"nano": true, "vim": true, "vi": true, "emacs": true,
			"editor": true, "editores": true, "awk": true, "sed": true,
			"grep": true, "xargs": true, "head": true, "tail": true,
			"sort": true, "uniq": true, "wc": true, "cat": true,
			// "in this case" é idiomático, não uma pasta "case"
			"case": true, "cases": true,
			// cue temporal/comparativo nunca é path — "no later than",
			// "modified later than" não tem dir "later"
			"later": true, "sooner": true, "earlier": true,
			"antes": true, "depois": true,
			// superlativo temporal é cláusula de sort/janela — "but 5
			// newest ones" não tem dir "newest"; "a files list" é o
			// CONJUNTO achado, não uma pasta "list"
			"newest": true, "oldest": true,
			"list": true, "lista": true,
			"but": true, "mas": true, "ignoring": true, "ignore": true,
			"ignorando": true,
			// metadado de arquivo é atributo de filtro — "whose inode"
			// não nomeia pasta
			"inode": true, "inodes": true,
			// "using regular expressions" — método, não escopo: o
			// gerúndio não nomeia diretório
			"using": true, "use": true, "usando": true, "utilizando": true,
			"via": true, "através": true,
			// vocabulário de padrão/regex nunca é pasta:
			// "in folders matching the pattern" não tem dir "pattern"
			"pattern": true, "patterns": true, "padrao": true, "padrão": true,
			"regex": true, "regexp": true, "expression": true,
			"expressions": true, "expressao": true, "expressão": true,
			"glob": true, "wildcard": true, "wildcards": true,
			// particípio/flag de formato descreve a SAÍDA, não um lugar:
			// "working directory separated by newlines" não tem dir
			// "separated"; "print0 is used" não tem path "used"
			"separated": true, "instead": true, "forward": true,
			"slashes": true, "slash": true, "newline": true,
			"newlines": true, "used": true, "handle": true,
			"null": true,
			// phrasing de recursão: "non-recursively"/"in sub directories"
			// descreve o passeio, não uma pasta
			"non-recursively": true, "nonrecursive": true,
			"non-recursive": true, "sub": true,
			// flags de saída do find não são pasta: "(print0 for each)"
			"print0": true, "printf": true, "print": true,
			"listing": true, "listagem": true,
			// anáforas/apontadores nunca são diretório: "grep x in them"
			"them": true, "those": true, "these": true, "it": true,
			"eles": true, "elas": true, "deles": true, "delas": true,
			"neles": true, "nelas": true, "isso": true, "isto": true,
			"this": true, "that": true,
			// verbos de exibição/ação não nomeiam pasta
			"display": true, "displaying": true, "show": true, "showing": true,
			"mostra": true, "mostrar": true, "exibe": true, "exibir": true,
			"printing": true, "enter": true, "search": true, "searching": true,
			// colunas de saída pedidas junto ("with their timestamp and
			// paths") descrevem o LISTING, nunca um diretório
			"timestamp": true, "timestamps": true, "paths": true,
			"attributes": true, "sizes": true, "dates": true,
			// possessivo/condicional/conjunção adverbial nunca é pasta:
			// "under your home directory" não tem dir "your"; "if it is
			// empty" não tem dir "if"
			"your": true, "yours": true, "my": true, "our": true,
			"ours": true, "their": true, "theirs": true, "its": true,
			"his": true, "her": true, "hers": true, "if": true,
			"whether": true, "unless": true, "along": true,
			// phrasing de janela temporal nunca é pasta: "in their
			// names modified less than 2 days ago" — 'modified' pode
			// estar livre ainda (o fill de newer corre em ordem
			// aleatória vs dir_cued)
			"modified": true, "modificado": true, "modificada": true,
			"modificados": true, "modificadas": true, "accessed": true,
			"changed": true, "created": true, "ago": true, "ha": true,
			"há": true, "less": true, "fewer": true, "than": true,
			"within": true, "last": true, "past": true, "older": true,
			"newer": true, "recent": true, "recente": true,
			// particípio de ordenação é cláusula de sort, não path:
			// "in the current directory sorted by size" não tem dir
			// "sorted"; "disk partition" é escopo de filesystem
			"sorted": true, "ordering": true, "ordered": true,
			"arranged": true, "sorting": true, "ordenado": true,
			"ordenada": true, "classificado": true,
			"disk": true, "disks": true, "disco": true,
			// coluna de exibição pedida na frase ("and their
			// modification time") descreve o LISTING, não um lugar
			"modification": true, "modificacao": true, "modificação": true,
			"access": true, "acesso": true,
			// atributo de contagem depois de preposição é métrica, não
			// path: "sorted by number of files" não tem dir "number"
			"number": true, "numero": true, "número": true,
			"count": true, "quantidade": true,
			// ordinal de campo é posição, não path: "second field in
			// the first (file)" não tem dir "first"
			"first": true, "second": true, "third": true,
			"fourth": true, "fifth": true, "field": true,
			"fields": true, "campo": true, "campos": true,
			"primeiro": true, "segundo": true, "terceiro": true,
			// phrasing de filtro/formato nunca é pasta: "including
			// hidden ones" introduz conjunto, "long listing" é
			// formato de saída
			"including": true, "hidden": true, "long": true,
			"ones": true, "oculto": true, "oculta": true,
			"ocultos": true, "ocultas": true, "escondido": true,
			"escondida": true, "invisible": true,
			// preposição/agente nunca é pasta — "owned by user X" não
			// tem dir "by"; vocábulo de posse é tratado acima (abort
			// de escopo) e no check user-home, não por notDir
			"by": true, "por": true,
			"kib": true, "mib": true,
			"gib": true, "tib": true, "block": true, "blocks": true,
			"bloco": true, "blocos": true,
			// frase de explicação do comando não nomeia pasta — "Plus
			// symbol at the end is used to give ... to the chmod
			// command" não tem dir "Plus"/"symbol"/"command"
			"plus": true, "symbol": true, "symbols": true,
			"simbolo": true, "símbolo": true, "command": true,
			"commands": true, "comando": true, "comandos": true,
			"multiple": true, "multiplos": true, "múltiplos": true,
			// "on the system" é o filesystem inteiro, não uma pasta
			// chamada system — o escopo fica sem dir (honesto)
			"system": true, "sistema": true, "machine": true,
			"maquina": true, "máquina": true, "computer": true,
			"computador": true, "host": true, "server": true,
			"servidor": true,
		}
		// a conjunction/verb closes the scope phrase — words after "under X
		// and keep Y" are the next clause, not part of the dir name
		clauseBoundaries := map[string]bool{
			"and": true, "or": true, "but": true, "then": true,
			"e": true, "ou": true, "mas": true, "então": true, "entao": true,
			// cláusula de exceção/limitação fecha o escopo — "files in the
			// folder except those in workspace" não tem dir "except"
			"except": true, "exceto": true, "menos": true, "without": true,
			"save": true, "excluding": true, "excluindo": true,
			// pronome relativo abre oração nova — "directory which have
			// size 40" não tem dir "have"
			"which": true, "that": true, "who": true, "whose": true,
			"whom": true, "que": true, "quem": true,
			// preposição de destino abre o ALVO, não mais o escopo —
			// "directory to antoniod" não tem dir "to"
			"to": true, "para": true, "pra": true, "pro": true,
			"into": true, "as": true, "named": true, "called": true,
		}
		// cue de pasta substantivo (pasta/folder/directory) introduz o ALVO
		// num pedido de archive — "compacta a pasta logs" tem srcs=logs, não
		// dir=logs. Preposições (em/in/na) continuam marcando escopo.
		nounCues := map[string]bool{
			"pasta": true, "pastas": true, "diretorio": true, "diretório": true,
			"diretorios": true, "diretórios": true, "folder": true,
			"folders": true, "directory": true, "directories": true,
		}
		arcCtx := false
		for _, t := range toks {
			if t.has("compress") || t.has("archiveverb") || t.has("extract") ||
				t.has("archive") {
				arcCtx = true
				break
			}
		}
		for i, t := range toks {
			cue := dirCues[t.Fold]
			if !cue && arcCtx && (t.Fold == "to" || t.Fold == "para") {
				// "extract archive to ./x" — no contexto archive o
				// destino É o diretório; fora dele, 'to' é manner
				cue = true
			}
			if !cue || taken[i] {
				continue
			}
			if t.Fold == "in" || t.Fold == "em" {
				// "ends in tar-gz"/"acaba em .zip" — o 'in' fecha a
				// âncora de sufixo, não abre escopo de diretório
				endAnchor := false
				for j := i - 1; j >= 0 && j >= i-2; j-- {
					switch toks[j].Fold {
					case "end", "ends", "ending", "suffix", "suffixed",
						"termina", "terminam", "acaba", "acabam":
						endAnchor = true
					case "files", "file", "arquivos", "arquivo":
						continue
					}
					break
				}
				if endAnchor {
					continue
				}
			}
			if arcCtx && nounCues[t.Fold] {
				continue
			}
			// "create archive X from src"/"extrai tar de backup" — no
			// contexto archive o from/de introduz a FONTE (slot srcs),
			// nunca o escopo de busca
			if arcCtx {
				switch t.Fold {
				case "from", "de", "do", "da", "dos", "das":
					continue
				}
			}
			// "no owner"/"no group": EN negation, not the PT "em+o" preposition
			if t.Fold == "no" && i+1 < len(toks) && nuNegatableNouns[toks[i+1].Fold] {
				continue
			}
			// "the user's home directory" — o cue substantivo vem DEPOIS
			// do par possessivo; olha pra trás e resolve "~" como o
			// your/my home adiante
			if nounCues[t.Fold] {
				for j := i - 1; j >= 0 && j >= i-4; j-- {
					u := &toks[j]
					if u.Filler || u.Fold == "'s" || u.Fold == "s" {
						continue
					}
					if u.Fold == "home" || u.Fold == "casa" {
						for k := j; k <= i; k++ {
							taken[k] = true
						}
						for k := j - 1; k >= 0 && k >= j-3; k-- {
							switch toks[k].Fold {
							case "user", "usuario", "usuário", "'s", "s",
								"current", "atual", "owner", "dono":
								taken[k] = true
							default:
								k = -1
							}
						}
						return "~"
					}
					break
				}
			}
		dirScan:
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if clauseBoundaries[u.Fold] {
					break
				}
				// "in the entire file system" — o tokenizer cola "file
				// system" como slang; o par é a raiz inteira, não uma
				// pasta — resolve "/" antes do skip de slang
				if u.Slang && nuFilesystemAt(toks, j) {
					taken[i], taken[j] = true, true
					return "/"
				}
				if u.Filler || u.Slang || taken[j] || nuIsNumber(u.Fold) || u.has("nameattr") || dirCues[u.Fold] {
					continue
				}
				if nuInExceptClause(toks, j) {
					continue // "under docs except path docs/x" — x é exclusão, não escopo
				}
				if u.has("filequoted") || u.has("forquoted") || u.has("nametgt") ||
					u.has("exctgt") {
					continue // a aspa já foi reivindicada como nome/conteúdo/exclusão
				}
				if nuPureExt(u.Fold) || nuExtWords[u.Fold] {
					continue // ".txt files" é seletor do slot ext, não dir —
					// "./storage/" começa com ponto mas é caminho (tem /)
				}
				// cue de posse/permissão ABORTA o escopo — "arquivos do
				// usuário matheus" é filtro de dono, não pasta "matheus".
				// user/dono ficam de fora: "user's home" ainda resolve
				// ~ no check adiante
				switch u.Fold {
				case "ownership", "group", "groups", "grupo", "grupos",
					"permission", "permissions", "permissao", "permissão":
					break dirScan
				}
				if nuFileShaped(u.Fold) {
					continue // "em a.txt" — o alvo é o slot file, não um dir
				}
				if nuArchiveWord(u.Fold) {
					continue // "into a tarball" — é o formato do out, não dir
				}
				if timeWords[u.Fold] || notDirWords[u.Fold] ||
					nuToolWords[u.Fold] || strings.HasPrefix(u.Fold, "-") {
					continue // a window, a verb or a tool/flag is never a dir — leave it for the time slots or unfed
				}
				// palavra-número é quantidade de janela, nunca pasta:
				// "no later than one minute" não tem dir "one" (o
				// dígito "2020" segue valendo como nome de pasta)
				if _, isNW := nuNumWord(u.Fold); isNW {
					continue
				}
				// "$VAR sozinho" é ambíguo (arquivo ou pasta) — fica
				// livre, A MENOS que o próprio nome diga "dir/folder/
				// path/home" ("$some_directory" é escopo, não alvo);
				// "$VAR/sub/dir" com barra é path = diretório
				if strings.HasPrefix(u.Raw, "$") && !strings.Contains(u.Raw, "/") {
					low := strings.ToLower(u.Raw)
					// marcador de palavra inteira: "$DIR" nu é var
					// ambígua (unfed), "$some_directory"/"$BUILD_DIR"
					// se declaram pasta
					dirish := strings.Contains(low, "directory") ||
						strings.Contains(low, "diretorio") ||
						strings.Contains(low, "folder") ||
						strings.Contains(low, "pasta") ||
						strings.Contains(low, "home") ||
						strings.Contains(low, "path") ||
						strings.Contains(low, "_dir") ||
						strings.Contains(low, "-dir") ||
						strings.Contains(low, "dir_") ||
						strings.Contains(low, "dir-")
					if !dirish {
						// "$dir"/"$path"/"$folder" nus — o nome da
						// var INTEIRO declara o tipo. Só minúsculo:
						// "$DIR" é env var opaca (o usuário não
						// escreveu um nome descritivo)
						nm := strings.TrimLeft(u.Raw, "$")
						nm = strings.Trim(nm, "{}")
						dirish = nm == "dir" || nm == "path" ||
							nm == "folder" || nm == "home" ||
							nm == "pasta" || nm == "diretorio" ||
							nm == "directory"
					}
					if !dirish {
						continue
					}
				}
				if unitWords[u.Fold] {
					taken[i], taken[j] = true, true
					continue // a format spec is explained phrasing, not a path
				}
				if scopeFillers[u.Fold] {
					// "in the entire filesystem" — filesystem inteiro
					// é a raiz, não este projeto
					for k := j + 1; k < len(toks) && k <= j+2; k++ {
						v := &toks[k]
						if v.Filler || v.Slang {
							continue
						}
						if nuFilesystemAt(toks, k) || v.Fold == "system" ||
							v.Fold == "sistema" {
							taken[i], taken[j], taken[k] = true, true, true
							return "/"
						}
						break
					}
					taken[i], taken[j] = true, true
					break // "no projeto"/"directory tree" falam DESTE escopo — consome a frase, não de um dir
				}
				if nuFilesystemAt(toks, j) {
					taken[i], taken[j] = true, true
					return "/" // "in the filesystem"/"file system" — a raiz toda
				}
				// qualquer candidato antes de "filesystem" é phrasing —
				// "in the entrie filesystem" (typo ou não) tem dir=/,
				// nunca dir=entrie
				for k := j + 1; k < len(toks) && k <= j+3; k++ {
					v := &toks[k]
					if v.Filler || v.Slang {
						continue
					}
					if nuFilesystemAt(toks, k) {
						taken[i], taken[j], taken[k] = true, true, true
						return "/"
					}
					break
				}
				// "config files" — a palavra antes de files descreve o
				// conjunto (*config*), não a pasta onde procurar.
				// "in a git repository" / "in the directory tree" — o
				// par todo é escopo: repo/tree descrevem ESTE projeto.
				// Advérbio de travessia não fecha nome: "in mydir
				// recursively" tem dir=mydir — 'recursively' descreve
				// o passeio, não o conjunto.
				if j+1 < len(toks) && (fileWords[toks[j+1].Fold] ||
					scopeFillers[toks[j+1].Fold]) {
					switch toks[j+1].Fold {
					case "recursively", "recursive", "recursivamente",
						"upward", "downward":
						// advérbio de travessia não fecha o nome
					default:
						continue
					}
				}
				// "the user's home directory" — 'user' não é dir; quando
				// 'home/casa' vem logo depois (com ou sem 's), o escopo
				// inteiro resolve pra "~" como no your/my home
				switch u.Fold {
				case "user", "users", "usuario", "usuário", "usuarios",
					"usuários", "owner", "owners", "dono", "donos":
					hasHome := false
					for k := j + 1; k < len(toks) && k <= j+3; k++ {
						if toks[k].Fold == "home" || toks[k].Fold == "casa" {
							hasHome = true
							taken[i], taken[j], taken[k] = true, true, true
							// "the Movies folder in user's home" — o
							// substantivo-cue carrega o nome da subpasta:
							// escopo composto "~/Movies", nunca "~" pelado
							for b := i - 1; b >= 0 && b >= i-3; b-- {
								v := &toks[b]
								if v.Filler || v.Slang || taken[b] || nuIsNumber(v.Fold) ||
									nounCues[v.Fold] {
									// folder/directory é substantivo-cue —
									// phrasing transparente, não fronteira
									continue
								}
								if dirCues[v.Fold] || scopeFillers[v.Fold] ||
									notDirWords[v.Fold] || v.has("nameattr") ||
									nuPureExt(v.Fold) || strings.HasPrefix(v.Raw, "$") {
									break
								}
								taken[b] = true
								return "~/" + v.Raw
							}
							return "~"
						}
					}
					// "files of user X"/"arquivos do usuário X" — cue de
					// posse sem home: o escopo fecha aqui, X é dono
					if !hasHome {
						break dirScan
					}
				}
				// "your/my home directory" é a HOME do usuário — resolve pra
				// "~" e deixa o confinamento recusar fora do root (honesto),
				// em vez de procurar uma pasta literal chamada "home"
				if u.Fold == "home" || u.Fold == "casa" {
					if j+1 < len(toks) && nounCues[toks[j+1].Fold] {
						taken[i], taken[j], taken[j+1] = true, true, true
						return "~"
					}
					for k := i + 1; k < j; k++ {
						switch toks[k].Fold {
						case "your", "my", "our", "seu", "sua", "minha", "meu", "nosso", "nossa":
							taken[i], taken[j], taken[k] = true, true, true
							return "~"
						}
					}
				}
				taken[j] = true
				// "<classe> in <nome>" — "names of all directories in
				// mydir": a preposição de escopo entre o substantivo-
				// cue e o candidato marca a classe como ALVO da busca
				// (type), não como container. Deixar o cue livre pro
				// extrator de type; "files in the folder X" não tem
				// prep entre folder e X → folder continua consumido.
				cueIsTargetClass := false
				if nounCues[t.Fold] {
					for k := i + 1; k < j; k++ {
						switch toks[k].Fold {
						case "in", "em", "no", "na", "inside",
							"within", "dentro", "under", "sob":
							cueIsTargetClass = true
						}
					}
				}
				if !cueIsTargetClass {
					taken[i] = true
				}
				// enumeração: "under asia and emea directory" — nomes
				// ligados por and/, antes de cue de pasta plural viram
				// "asia|emea" (o script itera cada raiz)
				val := u.Raw
				if val == "" {
					val = u.Fold
				}
				extra := 0
				for k := j + 1; k+1 < len(toks) && extra < 3; {
					if !nuEnumSeps[toks[k].Fold] {
						break
					}
					v := &toks[k+1]
					okName := !v.Filler && !v.Slang && !taken[k+1] &&
						v.Fold != "" && !nuIsNumber(v.Fold) &&
						!nuFileShaped(v.Fold) && !nuPureExt(v.Fold) &&
						len(v.Concs) == 0 && !nuToolWords[v.Fold] &&
						!timeWords[v.Fold] && !scopeFillers[v.Fold] &&
						!unitWords[v.Fold] && !clauseBoundaries[v.Fold]
					// o último nome pode ser seguido do substantivo
					// ("emea directory") — intermediários não
					trail := k+2 < len(toks) &&
						(nounCues[toks[k+2].Fold] || nuEnumSeps[toks[k+2].Fold])
					if !okName || !trail {
						break
					}
					name := v.Raw
					if name == "" {
						name = v.Fold
					}
					val += "|" + name
					taken[k], taken[k+1] = true, true
					if nounCues[toks[k+2].Fold] {
						taken[k+2] = true
					}
					k += 2
					extra++
				}
				return val // tagged entities (@dir, /etc) are exactly the right values
			}
			// sem break: uma cue que só achou escopo/janela ("in all files")
			// não pode esconder a próxima ("under /myprojects")
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
	case "rx":
		// "the regex '^aaa$'" / "using regular expressions" — flag de
		// modo pro text_count/text_grep; sem ela o padrão é literal.
		for i, t := range toks {
			switch t.Fold {
			case "regex", "regexp", "eregex", "pcre", "ere":
				taken[i] = true
				return "1"
			case "regular", "expressão", "expressao", "expression":
				for j := i + 1; j < len(toks) && j <= i+2; j++ {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					switch u.Fold {
					case "expression", "expressions", "expressão", "expressões",
						"expressao", "expressoes", "engine", "syntax":
						taken[i], taken[j] = true, true
						return "1"
					}
					break
				}
			}
		}
		return ""
	case "mass":
		// separa "apaga o arquivo mais novo" (um alvo descrito — fill+take)
		// de "apaga todos os .tmp" (conjunto — for_each). Massa quando há
		// quantificador universal, ou plural de arquivos com filtro real;
		// "apaga os arquivos" sem filtro é ambíguo e vai perguntar.
		for _, t := range toks {
			switch t.Fold {
			case "todos", "todas", "todo", "toda", "all", "every", "cada", "qualquer", "quaisquer", "tudinho",
				"everything", "tudo":
				return "1"
			}
		}
		for _, t := range toks {
			switch t.Fold {
			case "regular", "regulars", "normal", "normais", "ordinary", "comuns":
				return "1" // "regular/normal files" — o próprio tipo é o filtro (-type f)
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
		// filtro de CONTEÚDO também é escopo: "search files for X" — o
		// conjunto é "todos os arquivos", o padrão seleciona dentro deles
		for _, t := range toks {
			if t.has("grepword") || t.has("forquoted") {
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
	olderSeen, newerSeen, agoSeen, laterThanSeen := false, false, false, false
	for i, u := range toks {
		switch u.Fold {
		case "older", "antigos", "antigo", "antiga", "velhos", "velho", "velha", "before", "antes", "more", "mais":
			olderSeen = true
		case "ultimos", "últimos", "ultimas", "últimas", "past", "within", "newer", "less", "menos", "recent", "recente", "recentes":
			newerSeen = true
		case "last":
			// "last 7 days"/"last 24 hours" — 'last' com quantidade é
			// janela recente; "last accessed"/"last touched" é o
			// superlativo de campo (atime), não direção
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				if nuNumTok(u.Fold) || timeWords[u.Fold] {
					newerSeen = true
				}
				break
			}
		case "later":
			// "modified no later than one minute ago" ≈ "modified less
			// than one minute ago" — recência (-mmin -N), não idade.
			// 'later' sem 'than' não é cue de janela ("for later").
			if i+1 < len(toks) && toks[i+1].Fold == "than" {
				laterThanSeen = true
			}
		case "ago", "há", "ha":
			agoSeen = true
		}
	}
	// "ago" = idade → older, EXCETO quando um comparativo já marcou a
	// direção: "less than 60 minutes ago" é newer (o ago é marcador
	// de tempo, não direção); "no later than X ago" idem.
	if agoSeen && !newerSeen && !laterThanSeen {
		olderSeen = true
	}
	if laterThanSeen {
		newerSeen = true
	}
	// "exactly 10*24 hours ago" — instante, não piso: a idade fica na
	// faixa [N, N+unidade). older recebe o piso, newer o teto.
	exact := false
	for _, u := range toks {
		switch u.Fold {
		case "exactly", "exact", "exatamente", "exata", "exato",
			"precisely", "precisamente":
			exact = true
		}
	}
	// negação de janela: "not modified in the last 24h" e "except those
	// modified today" querem o lado de FORA da janela — vira older. A
	// polaridade flipa a direção.
	negated := false
	for i, t := range toks {
		if t.Fold != "not" && t.Fold != "não" && t.Fold != "never" &&
			t.Fold != "haven't" && t.Fold != "hasn't" && t.Fold != "didn't" {
			continue
		}
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			u := &toks[j]
			if u.Filler || u.Fold == "" {
				continue
			}
			switch u.Fold {
			case "modified", "modificado", "modificada", "modificados",
				"modificadas", "changed", "alterado", "alterada",
				"accessed", "acessado", "acessada", "created", "criado",
				"criada", "touched", "been":
				negated = true
			}
			break
		}
	}
	// "except/without those|that <participle>" — a cláusula de exceção
	// temporal é uma negação de janela, não uma exclusão de nome
	for i, t := range toks {
		if negated {
			break
		}
		if !nuExceptCues[t.Fold] {
			continue
		}
		for j := i + 1; j < len(toks) && j <= i+4; j++ {
			u := &toks[j]
			if u.Filler || u.Fold == "" {
				continue
			}
			k := j
			if u.Fold == "those" || u.Fold == "that" || u.Fold == "the" ||
				u.Fold == "os" || u.Fold == "as" || u.Fold == "que" {
				for k = j + 1; k < len(toks) && k <= j+3; k++ {
					if toks[k].Filler || toks[k].Fold == "" {
						continue
					}
					break
				}
			}
			if k < len(toks) {
				switch toks[k].Fold {
				case "modified", "modificado", "modificada", "modificados",
					"modificadas", "changed", "alterado", "alterada",
					"accessed", "acessado", "acessada", "created", "criado",
					"criada", "touched", "been":
					negated = true
				}
			}
			break
		}
	}
	// nome de dia: "modified yesterday" é a banda [ontem,hoje) — older
	// recebe o piso (1d), newer o teto (2d). Negada, a banda só é
	// expressável quando o piso é zero ("not modified today" →
	// older:1440); banda interior negada é devolvida vazia e o pós-fill
	// a manda pro Dropped em vez de fingir cobertura.
	for i, t := range toks {
		var lo, hi string
		switch t.Fold {
		case "yesterday", "ontem":
			lo, hi = "1440", "2880"
			for k := i - 2; k < i; k++ {
				if k >= 0 && (toks[k].Fold == "before" || toks[k].Fold == "antes") {
					lo, hi = "2880", "4320" // day before yesterday
				}
			}
		case "anteontem":
			lo, hi = "2880", "4320"
		case "today", "hoje":
			lo, hi = "", "1440"
		}
		if hi == "" {
			continue
		}
		// o dia e a phrasing em volta são consumidos — senão "modified"/
		// "those" ficam soltos e o sponge de name/dir os transforma em
		// operando falso
		for k := i - 6; k <= i+1 && k < len(toks); k++ {
			if k < 0 {
				continue
			}
			switch toks[k].Fold {
			case "modified", "modificado", "modificada", "modificados",
				"modificadas", "accessed", "acessado", "acessada",
				"changed", "alterado", "alterada", "created", "criado",
				"criada", "touched", "been", "were", "was", "that",
				"which", "those", "these", "the", "in", "on", "of", "a",
				"an", "not", "não", "before", "antes", "day":
				taken[k] = true
			}
		}
		if negated {
			for k := i - 6; k <= i && k < len(toks); k++ {
				if k >= 0 && nuExceptCues[toks[k].Fold] {
					taken[k] = true // "except those modified today"
				}
			}
		}
		if negated {
			if !newer && lo == "" {
				taken[i] = true
				return hi // complemento de [0,hi) é [hi,∞) → older
			}
			continue // banda interior negada: sem bound único honesto
		}
		taken[i] = true
		if newer {
			return hi
		}
		return lo
	}
	if negated {
		olderSeen, newerSeen = newerSeen, olderSeen
	}
	if newer != newerSeen || olderSeen == newerSeen {
		return ""
	}
	for i, t := range toks {
		if i+1 >= len(toks) {
			continue
		}
		if !nuNumTok(t.Fold) {
			continue
		}
		prod := -1
		if f := t.Fold; strings.Count(f, "*") >= 1 {
			// "365*24 hours" — produto escrito na frase; avalia antes
			// de desistir do caminho numérico
			parts := strings.Split(f, "*")
			prod = 1
			for _, p := range parts {
				v, err := strconv.Atoi(p)
				if err != nil {
					prod = -1
					break
				}
				prod *= v
			}
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
		case "fortnight", "fortnights", "quinzena", "quinzenas":
			mult = 20160
		case "month", "months", "mes", "mês", "meses":
			mult = 43200
		case "year", "years", "ano", "anos":
			mult = 525600
		}
		if mult == 0 {
			continue
		}
		n, err := strconv.Atoi(t.Fold)
		if prod >= 0 {
			n, err = prod, nil
		} else if err != nil {
			if v, ok := nuNumWord(t.Fold); ok {
				n, err = v, nil
			}
		}
		if err == nil {
			taken[i], taken[i+1] = true, true
			// a phrasing da janela é consumida junto — "modified less
			// than 2 days ago" deixava "modified" solto e virava dir
			for k := i - 4; k <= i+3; k++ {
				if k < 0 || k >= len(toks) {
					continue
				}
				switch toks[k].Fold {
				case "modified", "modificado", "modificada", "modificados",
					"modificadas", "modification", "modificação",
					"accessed", "acessado", "acessada", "access",
					"changed", "alterado", "alterada", "created", "criado",
					"criada", "less", "more", "ago", "ha", "há", "within",
					"last", "past", "older", "newer", "recent", "recente",
					"than", "that", "do", "que", "in", "em", "at",
					"least", "most",
					"later", "no", "exactly", "exact", "exatamente",
					"exata", "exato", "precisely", "precisamente",
					"were", "was", "are", "been", "is", "which",
					"those", "these", "them", "the", "of", "a", "an", "s",
					"have", "has", "not", "touched", "over", "for",
					"time", "tempo", "with", "com":
					taken[k] = true
				}
			}
			if exact {
				if newer {
					return strconv.Itoa((n + 1) * mult)
				}
				return strconv.Itoa(n * mult)
			}
			return strconv.Itoa(n * mult)
		}
	}
	// unidade nua com cue: "within the last month"/"past year"/"an hour
	// ago" ≈ 1× — sem isso a unidade colava no sponge de name/dir
	for i, t := range toks {
		mult := 0
		switch t.Fold {
		case "month", "months", "mes", "mês", "meses":
			mult = 43200
		case "year", "years", "ano", "anos":
			mult = 525600
		case "week", "weeks", "semana", "semanas":
			mult = 10080
		case "fortnight", "fortnights", "quinzena", "quinzenas":
			mult = 20160
		case "day", "days", "dia", "dias":
			mult = 1440
		case "hour", "hours", "hora", "horas":
			mult = 60
		case "minute", "minutes", "minuto", "minutos":
			mult = 1
		}
		if mult == 0 {
			continue
		}
		// "older than $DAYS days" — a quantidade é uma var simbólica;
		// multiplicar a unidade nua fabrica uma duração que ninguém
		// pediu (1440 min). A var fica solta pro Dropped.
		varBound := false
		for k := i - 4; k < i; k++ {
			if k < 0 {
				continue
			}
			if r := toks[k].Raw; strings.HasPrefix(r, "$") ||
				strings.HasPrefix(r, "%") ||
				(strings.HasPrefix(r, "<") && strings.HasSuffix(r, ">")) {
				varBound = true
			}
		}
		if varBound {
			continue
		}
		// "an hour ago" / "a day ago" — o "ago" logo depois da unidade é
		// cue de idade, mas "less than a day ago" segue sendo newer:
		// idade < unidade. recência/less vence o ago; more/older vencem.
		recentCue, lessCue, moreCue, agoCue := false, false, false, false
		for k := i - 4; k < i; k++ {
			if k < 0 {
				continue
			}
			switch toks[k].Fold {
			case "last", "past", "within", "últimos", "últimas", "ultimos",
				"ultimas", "recent", "recente", "newer":
				recentCue = true
			case "less", "menos":
				lessCue = true
			case "more", "mais", "older", "over", "before", "antes":
				moreCue = true
			}
		}
		for k := i + 1; k < len(toks) && k <= i+2; k++ {
			switch toks[k].Fold {
			case "ago", "há", "ha", "antes", "after", "later":
				agoCue = true
			}
		}
		wantNewer := (recentCue || lessCue) && !moreCue
		wantOlder := (moreCue || agoCue) && !recentCue && !lessCue
		if !wantNewer && !wantOlder {
			continue
		}
		if (newer && wantNewer) || (!newer && wantOlder) {
			taken[i] = true
			for k := i - 4; k <= i+2 && k < len(toks); k++ {
				if k < 0 {
					continue
				}
				switch toks[k].Fold {
				case "modified", "modificado", "accessed", "changed",
					"created", "last", "past", "within", "more", "less",
					"than", "ago", "older", "newer", "recent", "over",
					"the", "a", "an", "in", "em", "of", "were", "was",
					"that", "which", "those", "been", "have", "has", "not":
					taken[k] = true
				}
			}
			return strconv.Itoa(mult)
		}
	}
	return ""
}

// nuFindRange detects "N to M <unit>" / "between N and M <unit>" time ranges
// and returns (lo, hi) in minutes. Only fires on an explicit range cue —
// "to", "-", "a", "até", or "between … and" — so a lone "N minutes" never
// becomes a range.
// nuTimeUnitMult: unidade temporal → minutos ("days"→1440); 0 quando
// o token não é unidade.
func nuTimeUnitMult(fold string) int {
	switch fold {
	case "min", "mins", "minuto", "minutos", "minute", "minutes", "m":
		return 1
	case "h", "hora", "horas", "hour", "hours":
		return 60
	case "d", "dia", "dias", "day", "days":
		return 1440
	case "semana", "semanas", "week", "weeks", "w":
		return 10080
	case "fortnight", "fortnights", "quinzena", "quinzenas":
		return 20160
	case "mes", "mês", "meses", "month", "months":
		return 43200
	case "ano", "anos", "year", "years":
		return 525600
	}
	return 0
}

var nuRangeTokRe = regexp.MustCompile(`^(\d+)-(\d+)$`)

func nuFindRange(toks []nuTok, taken map[int]bool) (string, string, bool) {
	// "2-5 days" — a faixa vem num token só
	for i, t := range toks {
		m := nuRangeTokRe.FindStringSubmatch(t.Fold)
		if m == nil || i+1 >= len(toks) {
			continue
		}
		mult := nuTimeUnitMult(toks[i+1].Fold)
		if mult == 0 {
			continue
		}
		lo, err1 := strconv.Atoi(m[1])
		hi, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || lo >= hi {
			continue
		}
		taken[i], taken[i+1] = true, true
		return strconv.Itoa(lo * mult), strconv.Itoa(hi * mult), true
	}
	// "more than 50 days but less than 100 days ago" — dois
	// comparativos delimitam a faixa: piso no 'more', teto no 'less'
	for i, t := range toks {
		loCue := t.Fold == "more" || t.Fold == "mais" || t.Fold == "over" ||
			t.Fold == "older" || t.Fold == "above" || t.Fold == "greater" ||
			t.Fold == "acima"
		if !loCue {
			continue
		}
		// primeiro número depois do comparativo-de-piso
		n1, u1 := -1, 0
		for j := i + 1; j < len(toks) && j <= i+3; j++ {
			if nuIsNumber(toks[j].Fold) {
				n1 = j
			}
			if n1 < 0 {
				continue
			}
			if toks[j].Fold == "than" || toks[j].Fold == "que" ||
				toks[j].Fold == "do" {
				continue
			}
			break
		}
		if n1 < 0 {
			continue
		}
		if n1+1 < len(toks) {
			u1 = nuTimeUnitMult(toks[n1+1].Fold)
		}
		// comparativo-de-teto + segundo número
		for k := n1 + 1; k < len(toks) && k <= n1+8; k++ {
			hiCue := toks[k].Fold == "less" || toks[k].Fold == "menos" ||
				toks[k].Fold == "fewer" || toks[k].Fold == "under" ||
				toks[k].Fold == "below" || toks[k].Fold == "younger"
			if !hiCue {
				continue
			}
			n2, u2 := -1, 0
			for j := k + 1; j < len(toks) && j <= k+3; j++ {
				if nuIsNumber(toks[j].Fold) {
					n2 = j
				}
				if n2 < 0 {
					continue
				}
				break
			}
			if n2 < 0 {
				break
			}
			if n2+1 < len(toks) {
				u2 = nuTimeUnitMult(toks[n2+1].Fold)
			}
			mult := u2
			if mult == 0 {
				mult = u1 // unidade compartilhada: "more than 50 days but less than 100"
			}
			if mult == 0 {
				break
			}
			lo, _ := strconv.Atoi(toks[n1].Fold)
			hi, _ := strconv.Atoi(toks[n2].Fold)
			if lo >= hi {
				break
			}
			for _, x := range []int{i, n1, n1 + 1, k, n2, n2 + 1} {
				if x >= 0 && x < len(toks) {
					taken[x] = true
				}
			}
			return strconv.Itoa(lo * mult), strconv.Itoa(hi * mult), true
		}
		break
	}
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
	"executable":     {"executavel", "executável", "executaveis", "executáveis", "executable", "executables", "execute", "execution", "rodavel", "rodável"},
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
		case "not", "non", "não", "nao", "nunca", "sem", "without", "except", "excluding", "exceto", "lack", "lacks", "lacking", "falta", "faltando", "missing", "missed", "ausente", "ausentes":
			return true
		case "no":
			if j+1 < len(toks) && nuNegatableNouns[toks[j+1].Fold] {
				return true
			}
		}
	}
	return false
}

// nuMissingNear: "files missing g+w"/"missing read permission" — a
// ausência explícita de um bit é predicado negado (not_bits), não
// presença. Janela curta pra trás: o verbo de ausência precede o bit.
func nuMissingNear(toks []nuTok, i int) bool {
	for j := i - 4; j < i; j++ {
		if j < 0 {
			continue
		}
		switch toks[j].Fold {
		case "missing", "missed", "lacking", "lacks", "lack",
			"falta", "faltando", "ausente", "ausentes":
			return true
		}
	}
	return false
}

// nuCueFamilyConcs: conceitos que barram candidato a nome de owner/group —
// palavras da família cue (user/files/permattr) e verbos de ação (a
// próxima cláusula começa com eles), NÃO nomes de serviço/host.
var nuCueFamilyConcs = map[string]bool{
	"user": true, "who": true, "files": true, "filenoun": true,
	"env": true, "permattr": true, "group": true,
	"find": true, "grepword": true, "count": true, "lineselect": true,
	"move": true, "deleteword": true, "create": true, "permword": true,
	"mutateverb": true, "archiveverb": true, "copyword": true,
	// verbo de mutação de permissão nunca é username — "set read
	// permission for group" não tem dono chamado 'set'
	"setperm": true, "run": true, "execperm": true, "open": true,
}

// nuFsTypeWords: nomes de tipo de filesystem (find -fstype) — não são
// username e a restrição por eles não é suportada pelos métodos.
var nuFsTypeWords = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "ntfs": true,
	"fat32": true, "vfat": true, "btrfs": true, "xfs": true,
	"jfs": true, "nfs": true, "ufs": true, "zfs": true,
	"hfs": true, "apfs": true, "exfat": true, "tmpfs": true,
}

// nuOwnerLikeArg: candidate word after a cue — skip fillers, quoted and
// already-taken tokens, bounded radius (the 'owner is daniel'→is fix).
func nuOwnerLikeArg(toks []nuTok, cues map[string]bool, neg bool, taken map[int]bool) string {
	// auto-referência: "the current user", "usuário atual", "to me" —
	// resolve pro nome real da conta (o verify confere via LookupId).
	// "current"/"me" podem ser fillers, então o scan normal não os vê.
	if !neg {
		selfWords := map[string]bool{
			"me": true, "myself": true, "eu": true,
			"atual": true, "current": true, "próprio": true, "proprio": true,
		}
		for i, t := range toks {
			if !selfWords[t.Fold] {
				continue
			}
			// "to me"/"pra mim" — a preposição de destino fecha a frase;
			// só vale pra família owner ("change group to me" é ambíguo)
			if i > 0 && cues["owner"] {
				switch toks[i-1].Fold {
				case "to", "para", "pro", "pra", "por":
					if cur := nuCurrentUser(); cur != "" {
						taken[i-1], taken[i] = true, true
						return cur
					}
				}
			}
			for j := i - 2; j <= i+2 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				// só a cue da família pedida vale: "current user" é
				// dono, não grupo; "current group" é grupo, não dono
				if cues[toks[j].Fold] {
					if cur := nuCurrentUser(); cur != "" {
						taken[i], taken[j] = true, true
						return cur
					}
				}
			}
		}
	}
	// passada set-phrasing: "change owner … to NAME" / "…para NAME" — o
	// nome fecha a frase depois da preposição, longe da palavra-cue.
	// Só dispara quando a cue da família aparece no texto ("owner" pro
	// owner, "group"/"grupo" pro group) — senão o scan rouba o nome do
	// slot vizinho.
	if !neg {
		// posição da cue da família — o alvo é o to/para DEPOIS dela:
		// "owner to a and group to b" → a/b; "owner and group to X" → X/X
		// (o mesmo alvo vale pras duas famílias — um não rouba do outro)
		cueAt := -1
		for i, t := range toks {
			if cues[t.Fold] {
				cueAt = i
				break
			}
		}
		if cueAt < 0 {
			goto window
		}
		for i := cueAt + 1; i+1 < len(toks); i++ {
			switch toks[i].Fold {
			case "to", "para", "pro", "pra", "por":
			default:
				continue
			}
			j := i + 1
			for j < len(toks) && toks[j].Filler {
				j++
			}
			// "to user and group kent" / "para o usuário e grupo X" —
			// a enumeração das famílias é phrasing; o alvo é o ÚLTIMO
			// nome (vale pras duas famílias — owner e group recebem X)
			for j < len(toks) &&
				(nuOwnerCues[toks[j].Fold] || nuGroupCues[toks[j].Fold] ||
					toks[j].Fold == "and" || toks[j].Fold == "e" ||
					toks[j].Fold == "both") {
				j++
				for j < len(toks) && toks[j].Filler {
					j++
				}
			}
			if j >= len(toks) {
				continue
			}
			n := toks[j].Fold
			cueish := false
			for _, c := range toks[j].Concs {
				if nuCueFamilyConcs[c] {
					cueish = true
				}
			}
			isFile := false
			for _, tag := range toks[j].Tags {
				if tag == "@file" {
					isFile = true
				}
			}
			if n != "" && !cueish && !isFile && !strings.ContainsAny(n, "/.") && !nuIsNumber(n) &&
				!nuOctalShaped(n) && !nuGroupCues[n] && j+1 >= len(toks) {
				taken[i], taken[j] = true, true
				return n
			}
		}
	}
window:
	// família GRUPO: "owned by X group"/"belonging to the admin group" —
	// o nome precede a palavra-cue (padrão invertido do owner).
	if cues["group"] {
		for i, t := range toks {
			if !nuGroupCues[t.Fold] || i == 0 || taken[i] {
				continue
			}
			if nuNegationNear(toks, i) != neg {
				continue
			}
			j := i - 1
			// só artigos/determinantes são transparentes — filler genérico
			// ('and', 'to') PARA o lookback: "group to staff" tem o nome
			// DEPOIS do cue, e pular 'and' roubaria o valor do slot vizinho
			for j > 0 {
				switch toks[j].Fold {
				case "the", "a", "an", "o", "um", "uma", "this", "that",
					"these", "those", "their", "its", "seu", "sua":
					j--
				default:
					goto nameAt
				}
			}
		nameAt:
			if j < 0 {
				continue
			}
			n := toks[j].Fold
			if n == "" || taken[j] || strings.ContainsAny(n, "/.") ||
				nuIsNumber(n) || nuGroupCues[n] {
				continue
			}
			cueish := false
			for _, c := range toks[j].Concs {
				if nuCueFamilyConcs[c] {
					cueish = true
				}
			}
			if cueish {
				continue
			}
			// verbos auxiliares/preposições/conjunções logo antes não são
			// nome de grupo
			switch n {
			case "to", "by", "in", "on", "of", "the", "a", "an", "is", "are",
				"was", "were", "no", "any", "and", "or", "e", "ou", "nor",
				"change", "set", "assign", "give", "make", "with", "for",
				"com", "para", "pra", "pro", "em", "de", "do", "da":
				continue
			}
			// "the file myfile to group friends": 'to' é stop-word e some —
			// o sinal que sobra é o cue de ARQUIVO antes do substantivo.
			// myfile é o alvo do pedido, não o nome do grupo.
			if j > 0 && nuFileCueWords[toks[j-1].Fold] {
				continue
			}
			taken[i], taken[j] = true, true
			return n
		}
	}
	for i, t := range toks {
		cue := cues[t.Fold]
		// 'owned/belonging by' é cue de DONO — nunca de grupo
		isOwner := len(cues) > 0 && cues["owner"]
		if !cue && isOwner && t.Fold == "by" && i > 0 && (toks[i-1].Fold == "owned" || toks[i-1].Fold == "belonging") {
			cue = true
		}
		// "belong to <name>" — o nome segue a preposição sem palavra-cue
		if !cue && isOwner && t.Fold == "to" && i > 0 && (toks[i-1].Fold == "belong" || toks[i-1].Fold == "belongs" || toks[i-1].Fold == "belonged") {
			cue = true
		}
		if !cue {
			continue
		}
		if nuNegationNear(toks, i) != neg {
			continue
		}
		jStop := false
		for j := i + 1; j < len(toks) && j <= i+6; j++ {
			if jStop {
				break
			}
			if toks[j].Filler || taken[j] {
				continue
			}
			n := toks[j].Fold
			// "group of the file X to group friends": a frase do outro cue
			// só abre quando o PRIMEIRO não-filler após `to` é a cue —
			// "to group friends". "to alice and group..." tem o nome da
			// MINHA família logo ali (alice) — não é boundary.
			if n == "to" || n == "into" || n == "para" || n == "pra" {
				for k := j + 1; k < len(toks); k++ {
					if toks[k].Filler {
						continue
					}
					if nuGroupCues[toks[k].Fold] || toks[k].Fold == "owner" ||
						toks[k].Fold == "dono" || toks[k].Fold == "user" ||
						toks[k].Fold == "usuario" || toks[k].Fold == "usuário" {
						jStop = true
					}
					break
				}
			}
			if n == "or" || n == "and" || n == "e" || n == "ou" || strings.ContainsAny(n, "/.") ||
				n == "under" || n == "in" || n == "on" || n == "at" || n == "from" ||
				n == "to" || n == "into" || n == "dentro" || n == "para" ||
				// pronome/determinante não é username — "writable by their
				// owner" fala do dono genérico, não do usuário "their"
				n == "their" || n == "its" || n == "his" || n == "her" ||
				n == "the" || n == "a" || n == "an" || n == "this" || n == "that" ||
				n == "seu" || n == "sua" || n == "seus" || n == "suas" ||
				n == "dele" || n == "dela" || n == "known" || n == "any" ||
				n == "conhecido" || n == "conhecida" || n == "conhecidos" ||
				n == "conhecidas" || n == "valid" || n == "valido" ||
				n == "valida" || n == "validos" || n == "validas" ||
				n == "either" || n == "both" || n == "neither" || n == "each" ||
				n == "pro" || n == "pra" || n == "por" ||
				// substantivo de dados não é username — "merge user
				// records" fala de registros de usuários, não do dono
				// 'records'
				n == "records" || n == "record" || n == "data" || n == "dados" ||
				n == "details" || n == "detail" || n == "info" || n == "entries" ||
				n == "entry" || n == "fields" || n == "field" || n == "columns" ||
				n == "column" || n == "rows" || n == "row" || n == "sources" ||
				n == "source" || n == "fontes" || n == "names" ||
				// fstype ("ext2", "jfs") é restrição de filesystem na
				// cláusula seguinte — nunca username
				nuFsTypeWords[n] {
				continue // a conjunction, a preposition or a path is never a user/group name
			}
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
			// "the file X to group friends": substantivo atrás de cue de
			// arquivo é o alvo, não o dono/grupo (o 'to' some como stop-word)
			if j > 0 && nuFileCueWords[toks[j-1].Fold] {
				continue
			}
			if nuGroupCues[n] {
				continue // "user group staff" → o 'staff' pertence ao 'group'
			}
			// "owned by X group"/"belong to the X group": o substantivo
			// 'group' DEPOIS do nome manda ele pra família grupo — o dono
			// não pode comê-lo ("shadow group" ≠ usuário shadow)
			if j+1 < len(toks) && nuGroupCues[toks[j+1].Fold] && !cues["group"] {
				continue
			}
			taken[i], taken[j] = true, true
			return n
		}
	}
	return ""
}

// nuCurrentUser: o nome da conta que está rodando — "usuário atual" é
// semântico, não literal; vazio quando o ambiente não diz (falha honesta).
// USER/LOGNAME são POSIX; no Windows a equivalente é USERNAME (sem ela o
// self-reference "current user"/"me"/"usuário atual" nunca resolvia).
func nuCurrentUser() string {
	for _, k := range []string{"USER", "LOGNAME", "USERNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return ""
}

var nuOwnerCues = map[string]bool{
	"usuario": true, "usuário": true, "usuarios": true, "usuários": true,
	"user": true, "users": true, "dono": true, "donos": true,
	"owner": true, "owners": true, "ownership": true,
	"propriedade": true,
}

// nuPermClass: a classe de permissão perto da palavra de modo —
// "remove write FOR GROUP" é g-w, não a-w. Sem cue explícita: a (todos).
var nuPermClassWords = map[string]string{
	"group": "g", "groups": "g", "grupo": "g", "grupos": "g",
	"owner": "u", "user": "u", "usuario": "u", "usuário": "u",
	"dono": "u", "donos": "u",
	"others": "o", "other": "o", "outros": "o", "outras": "o",
	"all": "a", "everyone": "a", "everybody": "a", "anyone": "a", "anybody": "a",
	"todos": "a", "todas": "a", "todo": "a", "toda": "a",
}

func nuPermClass(toks []nuTok, at int) string {
	// a classe se anuncia por cue de escopo perto do modo:
	// "writable for the group", "permissão de escrita do grupo",
	// "writable by the owner". Um cue que aponta palavra não-classe
	// ("writable by root" — usuário, não classe) retorna desconhecido,
	// nunca "a": tratar como "mundo" seria over-report.
	for j := at + 1; j < len(toks) && j <= at+7; j++ {
		f := toks[j].Fold
		if f == "for" || f == "para" || f == "pro" || f == "pra" ||
			f == "of" || f == "de" || f == "do" || f == "da" ||
			f == "to" || f == "ao" || f == "by" || f == "por" ||
			f == "pelo" || f == "pela" {
			for k := j + 1; k < len(toks) && k <= j+4; k++ {
				if c, ok := nuPermClassWords[toks[k].Fold]; ok {
					return c
				}
				if !toks[k].Filler && toks[k].Fold != "both" &&
					toks[k].Fold != "their" && toks[k].Fold != "its" &&
					toks[k].Fold != "and" && toks[k].Fold != "or" {
					return "" // "for backup group"/"by root" não é classe
				}
			}
		}
	}
	return "a"
}

// nuPermClassStrict — classe só quando o cue de escopo aponta uma
// palavra-classe de fato. Sem o default "a": "find all writable
// files" não declara escopo mundo (o writable é do usuário).
func nuPermClassStrict(toks []nuTok, at int) string {
	for j := at + 1; j < len(toks) && j <= at+7; j++ {
		f := toks[j].Fold
		if f == "for" || f == "para" || f == "pro" || f == "pra" ||
			f == "of" || f == "de" || f == "do" || f == "da" ||
			f == "to" || f == "ao" || f == "by" || f == "por" ||
			f == "pelo" || f == "pela" {
			for k := j + 1; k < len(toks) && k <= j+4; k++ {
				if c, ok := nuPermClassWords[toks[k].Fold]; ok {
					return c
				}
				if !toks[k].Filler && toks[k].Fold != "both" &&
					toks[k].Fold != "their" && toks[k].Fold != "its" &&
					toks[k].Fold != "and" && toks[k].Fold != "or" {
					return ""
				}
			}
		}
	}
	return ""
}

// nuDepthWords — metawords de profundidade/limite que nunca são
// nome de arquivo ("under maximum 2 level down", "minimum 1 level")
var nuDepthWords = map[string]bool{
	"maximum": true, "max": true, "maximo": true, "máximo": true,
	"minimum": true, "min": true, "minimo": true, "mínimo": true,
	"level": true, "levels": true, "nivel": true, "nível": true,
	"niveis": true, "níveis": true, "depth": true, "profundidade": true,
	"down": true, "abaixo": true, "least": true, "most": true,
}

// nuPermModeCues — substantivos de permissão que ancoram o modo
// ("permission to 7", "permissions 755", "mode 644"). O singular
// "permission"/"permissão" vale tanto quanto o plural.
var nuPermModeCues = map[string]bool{
	"mode": true, "modo": true, "permission": true, "permissions": true,
	"permissao": true, "permissoes": true, "permissaos": true,
	"perm": true, "perms": true, "rights": true, "direitos": true,
}

func nuPermArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "file":
		// alvo da mutação: "the file X", "a pasta X" (cue→nome) e
		// "the data folder" (nome→cue). "all the files in X" não tem
		// nome depois do cue plural → alvo massivo via mass, não file.
		for i, t := range toks {
			if !nuFileCueWords[t.Fold] || taken[i] {
				continue
			}
			// nome→cue ("the data folder"): olha pra trás
			if i > 0 {
				n := toks[i-1].Fold
				// possessivo "aluno1's files" — o nome é DONO de
				// origem (src_owner), não um arquivo chamado aluno1
				poss := false
				for k := i - 1; k >= 0 && k >= i-3; k-- {
					if toks[k].Fold == "'s" || toks[k].Fold == "s" {
						poss = true
						break
					}
					if toks[k].Filler || toks[k].Slang {
						continue
					}
					break
				}
				cueish := false
				for _, c := range toks[i-1].Concs {
					if nuCueFamilyConcs[c] {
						cueish = true
					}
				}
				if !taken[i-1] && n != "" && !toks[i-1].Filler && !cueish &&
					!poss &&
					!strings.ContainsAny(n, "/.") && !nuIsNumber(n) &&
					!nuFileCueWords[n] && !nuGroupCues[n] &&
					n != "the" && n != "a" && n != "an" &&
					n != "current" && n != "atual" && n != "this" && n != "that" {
					taken[i], taken[i-1] = true, true
					return n
				}
			}
			// cue→nome ("the file myfile"): olha pra frente, mas plural
			// seguido de preposição/cue é massivo ("all files in dir")
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler {
					continue
				}
				n := u.Fold
				stop := false
				switch n {
				case "in", "on", "at", "of", "de", "do", "da", "em", "no",
					"na", "under", "inside", "to", "para", "with", "that",
					"which", "whose", "named", "called", "as", "como",
					"like", "for":
					stop = true
				}
				// meta-palavra de explicação nunca é alvo — "give
				// multiple files as input to the chmod command" não
				// tem arquivo 'input'
				switch n {
				case "input", "output", "symbol", "symbols", "plus",
					"command", "commands", "multiple", "end", "ends",
					"give", "used", "entrada", "saida", "saída",
					"comando", "comandos", "sinal", "simbolo",
					"símbolo", "way", "forma", "format", "formato":
					stop = true
				}
				if stop || n == "" || taken[j] || nuIsNumber(n) || nuOctalShaped(n) ||
					nuGroupCues[n] || nuFileCueWords[n] || nuDepthWords[n] ||
					nuNegationNear(toks, j) {
					break
				}
				cueish := false
				for _, c := range u.Concs {
					if nuCueFamilyConcs[c] {
						cueish = true
					}
				}
				if cueish {
					break
				}
				// verbo/meta não é alvo: "and change their permission to
				// 600" — 'change'/'permission' não são arquivo
				if u.has("mutate") || u.has("replace") || u.has("permattr") ||
					u.has("create") || u.has("delete") || u.has("move") ||
					u.has("copy") || u.has("find") || u.has("open") ||
					u.has("setperm") || u.has("findattr") || u.has("intocue") ||
					u.has("nameattr") || u.has("run") || u.has("execperm") {
					break
				}
				// "files with the read permission" — 'read' é o BIT de
				// permissão que antecede o substantivo, não um arquivo
				k := j + 1
				for k < len(toks) && toks[k].Filler {
					k++
				}
				if k < len(toks) && nuPermModeCues[toks[k].Fold] {
					break
				}
				taken[i], taken[j] = true, true
				return u.Fold
			}
		}
		// "owner of X to Y" / "permissions of myfile": o alvo do chown/
		// chmod mora depois do 'of' que segue o substantivo de permissão
		for i, t := range toks {
			switch t.Fold {
			case "owner", "dono", "group", "grupo", "permission",
				"permissions", "permissao", "permissões", "mode":
			default:
				continue
			}
			j := i + 1
			if j >= len(toks) {
				continue
			}
			switch toks[j].Fold {
			case "of", "de", "do", "da", "on":
			default:
				continue
			}
			for k := j + 1; k < len(toks) && k <= j+3; k++ {
				u := &toks[k]
				if u.Filler || u.Slang {
					continue
				}
				f := u.Fold
				if f == "" || taken[k] || nuIsNumber(f) || nuOctalShaped(f) ||
					nuFileCueWords[f] || nuGroupCues[f] || nuStopBare[f] ||
					u.has("mutate") || u.has("permattr") || u.has("intocue") ||
					strings.ContainsAny(f, "=*|") {
					continue
				}
				taken[i], taken[j], taken[k] = true, true, true
				return u.Fold
			}
			break
		}
		// sem cue: file-shaped solto ("chmod +x script.sh", "on app.log")
		for i, t := range toks {
			if taken[i] || t.Filler {
				continue
			}
			isFile := false
			for _, tag := range t.Tags {
				if tag == "@file" {
					isFile = true
				}
			}
			if isFile || (nuFileShaped(t.Fold) && !strings.HasPrefix(t.Fold, ".")) {
				if i > 0 && (nuFileCueWords[toks[i-1].Fold] || nuGroupCues[toks[i-1].Fold]) {
					continue // era nome de grupo/pasta, não alvo solto
				}
				taken[i] = true
				return t.Fold
			}
		}
		return ""
	case "src_owner", "src_group":
		// filtro de ORIGEM da mutação: "change owner of files which
		// belong to root TO admin" — root é o conjunto-fonte, admin o
		// alvo. Sem esse slot o chown correria em TODOS os arquivos
		// (mutação ampla demais = insegura). "belong to X" vale dono;
		// "belong to group X" vale grupo — um não rouba do outro.
		for i, t := range toks {
			switch t.Fold {
			case "belong", "belongs", "belonging", "owned", "pertence",
				"pertencem", "pertencentes", "pertencidos", "held":
			default:
				continue
			}
			val, grp := "", false
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang {
					continue
				}
				switch u.Fold {
				case "to", "a", "de", "do", "da", "ao", "by", "por", "pelo", "pela":
					continue
				case "group", "grupo", "groupe":
					grp = true
					continue
				case "user", "usuario", "usuário", "owner", "dono":
					continue
				}
				if nuIsNumber(u.Fold) || len(u.Fold) < 2 || u.has("outtgt") {
					continue
				}
				val = u.Fold
				break
			}
			if val == "" {
				continue
			}
			if grp != (what == "src_group") {
				return "" // belong-to-group não é dono; belong-to-user não é grupo
			}
			taken[i] = true
			return val
		}
		// possessivo "aluno1's files" — o dono do conjunto-fonte é o
		// possuidor, não um arquivo chamado aluno1 (chown FROM aluno1)
		if what == "src_owner" {
			for i, t := range toks {
				if !nuFileCueWords[t.Fold] {
					continue
				}
				for j := i - 1; j >= 0 && j >= i-3; j-- {
					u := &toks[j]
					if u.Fold == "'s" || u.Fold == "s" {
						continue
					}
					if u.Filler || u.Slang || taken[j] {
						break
					}
					n := u.Fold
					if n == "" || nuIsNumber(n) || nuFileCueWords[n] ||
						nuGroupCues[n] || n == "user" || n == "users" ||
						n == "current" || n == "atual" {
						break
					}
					// verbo/metaword não é dono
					cueish := false
					for _, c := range u.Concs {
						if nuCueFamilyConcs[c] {
							cueish = true
						}
					}
					if cueish {
						break
					}
					// só possessivo explícito: 's' ou s solto entre
					// o nome e o substantivo-arquivo
					poss := false
					for k := j + 1; k < i; k++ {
						if toks[k].Fold == "'s" || toks[k].Fold == "s" {
							poss = true
						}
					}
					if !poss {
						break
					}
					taken[i], taken[j] = true, true
					return n
				}
			}
		}
		// "files with group owner vboxusers" / "whose group is vboxusers":
		// o grupo é predicado de ORIGEM (conjunto-fonte), não o alvo do
		// chgrp. Só src_group — "owner X" solto é alvo ambíguo demais.
		if what == "src_group" {
			for i, t := range toks {
				if t.Fold != "group" && t.Fold != "grupo" && t.Fold != "groupe" {
					continue
				}
				j := i + 1
				if j >= len(toks) {
					continue
				}
				switch toks[j].Fold {
				case "owner", "dono", "is", "é", "was", "=":
					j++
				default:
					continue
				}
				for ; j < len(toks) && j <= i+4; j++ {
					u := &toks[j]
					if u.Filler || u.Slang {
						continue
					}
					if nuIsNumber(u.Fold) || len(u.Fold) < 2 || u.has("outtgt") {
						break
					}
					taken[i] = true
					return u.Fold
				}
			}
		}
		return ""
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
		// "files that do not belong to a known user" / "não pertencem a
		// um usuário conhecido" — dono sem conta = arquivo órfão. O
		// qualificador pode vir antes ("a known user") ou depois
		// ("usuário conhecido") da cue.
		known := map[string]bool{
			"known": true, "valid": true, "conhecido": true, "conhecida": true,
			"conhecidos": true, "conhecidas": true, "valido": true, "valida": true,
			"validos": true, "validas": true,
			// "do not belong to any user"/"não pertence a nenhum
			// usuário" — o indefinido sob negação é o mesmo nouser
			"any": true, "nenhum": true, "nenhuma": true,
		}
		for i, t := range toks {
			if !known[t.Fold] {
				continue
			}
			for j := i - 2; j <= i+2 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				// a negação pode ancorar na cue ("não pertencem a um
				// usuário conhecido") ou no qualificador ("not a known user")
				neg := nuNegationNear(toks, i) || nuNegationNear(toks, j)
				if !neg {
					continue
				}
				if want["no_owner"] && nuOwnerCues[toks[j].Fold] {
					taken[i], taken[j] = true, true
					return "true"
				}
				if want["no_group"] && nuGroupCues[toks[j].Fold] {
					taken[i], taken[j] = true, true
					return "true"
				}
			}
		}
		return ""
	case "mode":
		for i, t := range toks {
			if nuPermModeCues[t.Fold] && !nuNegationNear(toks, i) {
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
			if nuPermModeCues[t.Fold] && nuNegationNear(toks, i) {
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
	case "writable", "not_writable":
		// "writable"/"not read-only" → writable:true (find -writable);
		// "read-only"/"read only"/"not writable" → not_writable. O
		// script mede os.access(W_OK) do usuário atual.
		for i, t := range toks {
			ro, w := false, false
			switch t.Fold {
			case "read-only", "readonly", "read_only":
				ro = true
			case "somente-leitura", "so-leitura":
				ro = true
			case "writable", "writables", "escrevivel", "escrevível",
				"gravavel", "gravável":
				w = true
			case "read", "leitura", "lida", "lido":
				// "read only"/"so leitura" — par de tokens
				if i+1 < len(toks) && (toks[i+1].Fold == "only" ||
					toks[i+1].Fold == "somente" || toks[i+1].Fold == "so" ||
					toks[i+1].Fold == "só") {
					ro = true
				} else {
					continue
				}
			case "somente", "so", "só":
				if i+1 < len(toks) && (toks[i+1].Fold == "leitura" ||
					toks[i+1].Fold == "read") {
					ro = true
				} else {
					continue
				}
			default:
				continue
			}
			neg := nuNegationNear(toks, i)
			// escopo de classe declarado desloca o predicado pro bits/
			// not_bits/ww: "writable by the group" é g+w, não o
			// os.access(W_OK) do usuário que o slot mede
			cls := nuPermClassStrict(toks, i)
			if cls == "g" || cls == "o" || cls == "a" {
				continue
			}
			want := (w && !neg) || (ro && neg)  // writable:true
			wantN := (w && neg) || (ro && !neg) // not_writable:true
			if what == "writable" && want {
				taken[i] = true
				return "true"
			}
			if what == "not_writable" && wantN {
				taken[i] = true
				return "true"
			}
		}
		return ""
	case "symbolic":
		// modo simbólico: "u+x", "ug=rwx,o=", "a-w" — token cru com
		// operador e bits, geralmente depois de "chmod"/"permission"
		for i, t := range toks {
			if taken[i] {
				continue
			}
			f := t.Fold
			if len(f) < 2 || len(f) > 24 {
				continue
			}
			ok := true
			// cada grupo separado por vírgula é uma cláusula própria:
			// "ug=rwx,o=" ⇒ ["ug=rwx","o="]
			for _, piece := range strings.Split(f, ",") {
				op := strings.IndexAny(piece, "+-=")
				if op < 0 || (op == 0 && piece[0] == '=') {
					ok = false
					break
				}
				for _, c := range piece[:op] {
					if c != 'u' && c != 'g' && c != 'o' && c != 'a' {
						ok = false
					}
				}
				for _, c := range piece[op+1:] {
					if c != 'r' && c != 'w' && c != 'x' && c != 'X' && c != 's' && c != 't' {
						ok = false
					}
				}
			}
			if ok {
				taken[i] = true
				return f
			}
		}
		return ""
	case "recursive":
		for i, t := range toks {
			if !taken[i] && (t.Fold == "recursively" || t.Fold == "recursive" ||
				t.Fold == "recursivo" || t.Fold == "recursiva" || t.Fold == "recursivamente") {
				taken[i] = true
				return "true"
			}
		}
		return ""
	case "symexec":
		// "make x executable" → a+x; negado fica de fora (recusa honesta)
		for i, t := range toks {
			for _, w := range []string{"executavel", "executável", "executable", "executaveis", "executáveis", "rodavel", "rodável"} {
				if t.Fold == w && !nuNegationNear(toks, i) {
					taken[i] = true
					return nuPermClass(toks, i) + "+x"
				}
			}
		}
		// "add execute to the permissions" — 'execute' nu só vale com
		// verbo de concessão por perto (senão "execute the script"
		// viraria chmod indevido)
		for i, t := range toks {
			if t.Fold != "execute" && t.Fold != "execucao" && t.Fold != "execução" {
				continue
			}
			if nuNegationNear(toks, i) {
				continue
			}
			grant := false
			for j := i - 3; j <= i+3; j++ {
				if j < 0 || j >= len(toks) {
					continue
				}
				switch toks[j].Fold {
				case "add", "give", "grant", "permit", "conceder",
					"dar", "acrescentar", "adicionar", "allow", "deixar":
					grant = true
				}
			}
			if grant {
				taken[i] = true
				// consome o verbo de concessão e o substantivo de
				// permissão ao redor — "add execute to the permissions"
				// deixava phrasing solto e derrubava a confiança
				for j := i - 3; j <= i+3; j++ {
					if j < 0 || j >= len(toks) || j == i {
						continue
					}
					switch toks[j].Fold {
					case "add", "give", "grant", "permit", "conceder",
						"dar", "acrescentar", "adicionar", "allow",
						"deixar", "permission", "permissions",
						"permissao", "permissões", "permissoes":
						taken[j] = true
					}
				}
				return nuPermClass(toks, i) + "+x"
			}
		}
		return ""
	case "symw":
		// "make it writable" → a+w
		for i, t := range toks {
			for _, w := range []string{"writable", "escrevivel", "escrevível", "gravavel", "gravável"} {
				if t.Fold == w && !nuNegationNear(toks, i) {
					taken[i] = true
					return nuPermClass(toks, i) + "+w"
				}
			}
		}
		return ""
	case "symnw":
		// "read-only"/"remove write"/"somente leitura" → a-w
		for i, t := range toks {
			f := t.Fold
			rm := false
			for j := i - 5; j < i; j++ { // "tira a permissão de escrita": cue longe
				if j < 0 {
					continue
				}
				switch toks[j].Fold {
				case "remove", "remover", "remova", "tira", "tirar", "strip", "drop":
					rm = true
				}
			}
			if (f == "writable" || f == "write" || f == "escrita" || f == "escrever") && (nuNegationNear(toks, i) || rm) {
				taken[i] = true
				return nuPermClass(toks, i) + "-w"
			}
			if f == "read-only" || f == "readonly" ||
				(f == "leitura" && i > 0 &&
					(toks[i-1].Fold == "somente" || toks[i-1].Fold == "only" || toks[i-1].Fold == "so" || toks[i-1].Fold == "só")) {
				taken[i] = true
				return nuPermClass(toks, i) + "-w"
			}
		}
		return ""
	case "symread":
		// "set read permission for group" → g+r — a classe declara o
		// bit-r do alvo; sem a frase-permissão ao lado, um 'read' solto
		// ("read the log") nunca é chmod
		for i, t := range toks {
			f := t.Fold
			isRead := f == "read" || f == "readable" || f == "leitura" ||
				f == "legivel" || f == "legível"
			if !isRead || nuNegationNear(toks, i) {
				continue
			}
			// precisa do substantivo-permissão E do verbo de concessão
			// na vizinhança — "files which have at least read
			// permission" é filtro (u+r da busca), não o modo alvo;
			// "set read permission for group" tem o 'set' antes
			perm, grant := false, false
			for j := i - 4; j <= i+4 && j < len(toks); j++ {
				if j < 0 || j == i {
					continue
				}
				switch toks[j].Fold {
				case "permission", "permissions", "permissao", "permissão",
					"permissoes", "permissões", "perm", "perms", "right",
					"rights", "direito", "direitos", "access", "acesso":
					perm = true
				case "set", "give", "grant", "add", "allow", "permit",
					"dar", "conceder", "definir", "acrescentar",
					"adicionar", "deixar", "change", "make", "assign",
					"enable", "turn", "apply":
					grant = true
				}
			}
			if !perm || !grant {
				continue
			}
			taken[i] = true
			return nuPermClass(toks, i) + "+r"
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
	case "exall":
		// "execute permission set for everybody/everyone" — todos os bits x
		// ligados (a+x), mais forte que o "algum exec" do slot x.
		for i, t := range toks {
			for _, w := range nuPermWords["executable"] {
				if t.Fold == w && !nuNegationNear(toks, i) && nuPermClass(toks, i) == "a" {
					taken[i] = true
					return "true"
				}
			}
		}
		return ""
	case "uread", "aread":
		// "read permission for the user" → u+r (0400); "read permission for
		// everybody" → a+r (0444). A classe na frase escolhe o slot.
		for i, t := range toks {
			f := t.Fold
			isRead := f == "read" || f == "readable" || f == "leitura" ||
				f == "legivel" || f == "legível" || f == "lida" || f == "lido"
			if !isRead || nuNegationNear(toks, i) {
				continue
			}
			// "read-only"/"readonly" é semântica de -w, não filtro de leitura
			if i+1 < len(toks) && (toks[i+1].Fold == "only" || toks[i+1].Fold == "-only") {
				continue
			}
			cls := nuPermClass(toks, i)
			if (what == "uread" && cls == "u") || (what == "aread" && cls == "a") {
				taken[i] = true
				return "true"
			}
		}
		return ""
	case "bits", "not_bits":
		// "writable by both their owner and their group" = AND-mask 220 —
		// o ww (S_IWOTH) é outra coisa: mundo, não dono+grupo. Cada
		// palavra-classe depois de cue soma o seu bloco octal. "missing
		// g+w"/"not writable by group" = not_bits (bit AUSENTE exigido).
		permWord := map[string]int{
			"writable": 2, "write": 2, "escrevivel": 2, "escrevível": 2,
			"gravavel": 2, "gravável": 2, "escrita": 2,
			"readable": 4, "read": 4, "legivel": 4, "legível": 4,
			"leitura": 4, "lida": 4, "lido": 4,
			"executable": 1, "executavel": 1, "executável": 1,
			"execute": 1, "rodavel": 1, "rodável": 1,
			// forma composta num token só: "read-write access" = r+w
			"read-write": 6, "read/write": 6, "read+write": 6, "rw": 6,
			"leitura-escrita": 6, "leitura/escrita": 6,
			"write-read": 6, "write/read": 6,
		}
		for i, t := range toks {
			// "missing g+w and o+w": a forma simbólica já é a máscara —
			// tokens permbit encadeados por 'and' somam seus bits
			if m0, ok := nuPermBitMask(t.Fold); ok {
				neg := nuNegationNear(toks, i) || nuMissingNear(toks, i)
				if neg != (what == "not_bits") {
					continue
				}
				taken[i] = true
				for j := i + 1; j < len(toks) && j <= i+4; j++ {
					switch toks[j].Fold {
					case "and", "e", ",":
						continue
					}
					if m1, ok := nuPermBitMask(toks[j].Fold); ok {
						taken[j] = true
						m0 |= m1
						continue
					}
					break
				}
				return fmt.Sprintf("%03o", m0)
			}
			bit, isPerm := permWord[t.Fold]
			neg := isPerm && (nuNegationNear(toks, i) || nuMissingNear(toks, i))
			if !isPerm || neg != (what == "not_bits") {
				continue
			}
			skip := func(f string) bool {
				switch f {
				case "by", "for", "para", "of", "de", "to", "por",
					"both", "their", "its", "his", "her", "and", "e",
					"the", "a", "an", "seu", "sua", "seus",
					"permission", "permissions", "permissao", "permissões",
					"permissoes", "mode", "have", "has", "com",
					"access", "acesso", "acessos":
					return true // "by both their owner and their group" /
					// "not have read permission to all" /
					// "read-write access for others"
				}
				return false
			}
			mask, saw := 0, false
			for j := i + 1; j < len(toks) && j <= i+9; j++ {
				u := toks[j].Fold
				if skip(u) {
					continue
				}
				c, ok := nuPermClassWords[u]
				if !ok {
					break // a primeira palavra útil não era classe
				}
				saw = true
				switch c {
				case "u":
					mask |= bit << 6
				case "g":
					mask |= bit << 3
				case "o":
					mask |= bit
				case "a":
					mask |= bit * 0o111
				}
			}
			if !saw {
				continue
			}
			taken[i] = true
			// o span classe+phrasing é phrasing do predicado — sem
			// consumir, "access"/"for others" vazam pro sponge
			for j := i + 1; j < len(toks) && j <= i+9; j++ {
				u := toks[j].Fold
				if skip(u) {
					taken[j] = true
					continue
				}
				if _, ok := nuPermClassWords[u]; !ok {
					break
				}
				taken[j] = true
			}
			return fmt.Sprintf("%03o", mask)
		}
		return ""
	}
	for i, t := range toks {
		for _, w := range nuPermWords[what] {
			if t.Fold == w && !nuNegationNear(toks, i) {
				if what == "world_writable" {
					// "writable by owner and group" NÃO é world — o bits
					// cobre classes u/g; ww só com escopo mundo explícito.
					// Sem cue de escopo ("find all writable files"), o
					// writable é do usuário — nuPermClass devolve "a" por
					// default e fingiria escopo world que a frase não tem
					world := t.Fold == "world-writable" ||
						nuPermClassStrict(toks, i) == "o" ||
						nuPermClassStrict(toks, i) == "a"
					if !world {
						continue
					}
				}
				return "true"
			}
		}
	}
	return ""
}

// nuOwnerArg takes the word after a possession cue — "do usuário matheus",
// "owned by root", "files of user h". Without the cue, a loose word must
// not become a user filter ("suid bit set" ≠ owner "set").
var nuPluralDirCues = map[string]bool{
	"dirs": true, "directories": true, "folders": true, "subdirs": true,
	"pastas": true, "diretorios": true, "diretórios": true, "subpastas": true,
	// formas stem: o fold reduz "directories"→"directory", "folders"→"folder"
	"directory": true, "folder": true, "pasta": true, "diretorio": true,
	"subdir": true, "subpasta": true,
}

var nuClauseMark = map[string]bool{
	"that": true, "which": true, "who": true, "whose": true, "where": true,
	"when": true, "que": true, "quem": true, "onde": true, "com": true,
	"with": true, "to": true, "para": true, "in": true, "em": true,
	"de": true, "of": true, "and": true, "e": true, "ou": true, "or": true,
}

var nuEnumSeps = map[string]bool{
	"and": true, "e": true, "ou": true, "&": true, ",": true, "+": true,
}

var nuEnumStops = map[string]bool{
	"in": true, "on": true, "inside": true, "under": true, "within": true,
	"em": true, "no": true, "na": true, "nos": true, "nas": true,
	"de": true, "do": true, "da": true, "of": true, "at": true,
	"with": true, "com": true, "to": true, "para": true, "pra": true,
	"where": true, "that": true, "which": true, "called": true,
	"named": true, "into": true, "dentro": true, "every": true, "each": true,
	"all": true, "current": true, "directory": true, "folder": true,
}

// nuMkArg — alvos de criação. "targets": enumeração explícita após cue de
// pasta plural ("make directories mnt and point", "cria as pastas a b e c"):
// coleta nomes citados/soltos separados por and/vírgula até um boundary.
// Só retorna com ≥2 nomes — alvo único fica no slot `target` de sempre.
func nuMkArg(toks []nuTok, what string, taken map[int]bool) string {
	if what != "targets" {
		return ""
	}
	for i, t := range toks {
		if !nuPluralDirCues[t.Fold] {
			continue
		}
		var names []string
		var used []int
		locPrep := false
		for j := i + 1; j < len(toks); j++ {
			u := toks[j]
			// prep de lugar/remoto encerra a enum — "mkdir a b in
			// the home" / "on host node": o que segue é escopo ou
			// host, nunca alvo. (são fillers — checamos antes do
			// skip de filler)
			switch u.Fold {
			case "in", "on", "at", "under", "into", "inside",
				"within", "to", "from", "em", "no", "na",
				"para", "dentro", "host", "node", "server",
				"machine", "ssh":
				locPrep = true
				continue
			}
			if locPrep {
				break
			}
			if u.Filler {
				continue
			}
			if nuEnumSeps[u.Fold] || u.Fold == "e" {
				continue
			}
			// quantificador/descritor encerra — "each unique mime
			// type" é classe de pasta, não nome enumerável
			switch u.Fold {
			case "each", "every", "cada", "unique", "respective",
				"corresponding", "individual", "per":
				locPrep = true
			}
			if locPrep {
				break
			}
			if nuEnumStops[u.Fold] || u.has("permbit") {
				break
			}
			// taken NÃO bloqueia: "mnt and point" — o primeiro nome já
			// virou `target` antes do targets rodar; a enumeração
			// reivindica de volta os nomes contíguos à cue plural.
			// O boundary (in/of/at…) já nos separa de slots distantes.
			_ = taken
			// nome: citado, file-shaped, ou palavra sem conceito de
			// ação/filtro — "mnt", "point" (pega "mountpoint" via frase
			// multiword mas como nome é legítimo), "backup-dir" valem.
			isName := !u.Filler && u.Fold != "" && !nuToolWords[u.Fold] &&
				!u.has("permbit") && !u.has("files") && !u.has("filenoun") &&
				!u.has("dirnoun") && !u.has("dir") && !u.has("extcue") &&
				!u.has("permattr") && !u.has("findattr") &&
				!nuIsNumber(u.Fold) && !nuOctalShaped(u.Fold)
			if !isName {
				break
			}
			name := u.Raw
			if name == "" {
				name = u.Fold
			}
			names = append(names, name)
			used = append(used, j)
		}
		if len(names) >= 2 {
			for _, j := range used {
				taken[j] = true
			}
			return strings.Join(names, "|")
		}
		return ""
	}
	return ""
}

// nuMkScope — escopo do create. Retorna "every_dir" quando o alvo vai
// DENTRO de cada pasta existente: "mkdir x in every directory", "cria a
// pasta x em cada pasta", "em todas as pastas". A pista é uma palavra-pasta
// precedida (num raio curto, por cima de fillers) por quantificador de
// varredura — e quantificadores/preposições são TODOS fillers no léxico,
// então a regra lê fold, não token-kind. Sem o quantificador ("in the
// current directory") é só localização e a frase não casa.
// Os tokens casados viram taken pra palavra-pasta e a base do escopo
// ("in the current directory") não vazarem como resíduo pro slot target.
func nuMkScope(toks []nuTok, taken map[int]bool) string {
	quants := map[string]bool{
		"every": true, "each": true, "all": true, "any": true,
		"cada": true, "toda": true, "todas": true, "todo": true, "todos": true,
	}
	preps := map[string]bool{
		"in": true, "inside": true, "under": true, "within": true, "into": true,
		"em": true, "no": true, "na": true, "nos": true, "nas": true,
		"sob": true, "dentro": true, "por": true,
	}
	for i, t := range toks {
		if t.Filler || t.Quoted || !nuPluralDirCues[t.Fold] {
			continue
		}
		// quantificador nos até-3 anteriores; qualquer palavra real
		// (não-filler) fecha a janela — "a new directory" não casa.
		q := -1
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if quants[toks[j].Fold] {
				q = j
				break
			}
			if !toks[j].Filler {
				break
			}
		}
		if q < 0 {
			continue
		}
		start := q
		if q > 0 && preps[toks[q-1].Fold] {
			start = q - 1 // "in every directory" — a preposição é da frase
		}
		end := i
		// base do escopo à direita ("in the current directory",
		// "no diretório atual"): preposição + palavra-pasta — marca junto
		// pra não vazar resíduo. Qualquer outra palavra real encerra.
		seenPrep := false
		for j := i + 1; j < len(toks) && j <= i+5; j++ {
			u := toks[j]
			if u.Filler {
				if preps[u.Fold] {
					seenPrep = true
				}
				continue
			}
			if seenPrep && nuPluralDirCues[u.Fold] {
				end = j
				// adjetivo de lugar colado ("diretório atual")
				if j+1 < len(toks) && !toks[j+1].Filler {
					switch toks[j+1].Fold {
					case "atual", "current", "corrente", "atualmente":
						end = j + 1
					}
				}
			}
			break
		}
		for j := start; j <= end; j++ {
			taken[j] = true
		}
		return "every_dir"
	}
	return ""
}

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
	// "non-recursive"/"not recursively"/"sem recursão" — só os filhos
	// diretos do top (depth_of = 1 no script), não a árvore toda
	if !wantMin {
		for i, t := range toks {
			nr := t.Fold == "non-recursive" || t.Fold == "non-recursively" ||
				t.Fold == "nonrecursive" || t.Fold == "nao-recursivo" ||
				t.Fold == "não-recursivo" || t.Fold == "nao-recursiva" ||
				t.Fold == "não-recursiva"
			if !nr && (t.Fold == "not" || t.Fold == "não" || t.Fold == "nao" ||
				t.Fold == "sem" || t.Fold == "without" || t.Fold == "no") &&
				i+1 < len(toks) {
				n := toks[i+1].Fold
				nr = n == "recursive" || n == "recursively" ||
					n == "recursivo" || n == "recursiva" ||
					n == "recursivamente" || n == "recursion" ||
					n == "recursão" || n == "recursao"
			}
			if nr {
				taken[i], taken[i+1] = true, true
				return "1"
			}
		}
		// "do not search in sub directories"/"no subdirectories"/"sem
		// buscar nas subpastas" — a negação de subpasta é a mesma
		// semântica do non-recursive
		for i, t := range toks {
			sub := t.Fold == "sub" || t.Fold == "subdirectories" ||
				t.Fold == "subdirectory" || t.Fold == "subdirs" ||
				t.Fold == "subdir" || t.Fold == "subfolder" ||
				t.Fold == "subfolders" || t.Fold == "subpastas" ||
				t.Fold == "subpasta" || t.Fold == "subdiretorios" ||
				t.Fold == "subdiretórios" || t.Fold == "subdiretorio"
			if !sub {
				continue
			}
			if t.Fold == "sub" && i+1 < len(toks) {
				n := toks[i+1].Fold
				if n != "directories" && n != "directory" && n != "dirs" &&
					n != "folders" && n != "folder" && n != "pastas" &&
					n != "diretorios" && n != "diretórios" {
					continue // "sub folder" sem par dir não é profundidade
				}
			}
			for j := i - 1; j >= 0 && j >= i-5; j-- {
				u := &toks[j]
				// negação é filler — checar ANTES do skip senão o
				// 'not' é engolido como stop-word
				switch u.Fold {
				case "not", "no", "não", "nao", "sem", "without", "never",
					"don't", "doesn't", "didn't", "except", "excluding",
					"excepting", "omit", "omitting", "skip",
					"skipping", "avoid", "avoiding":
					// a cláusula inteira é phrasing do predicado de
					// profundidade: "do not search in sub dirs"
					for k := j; k <= i+1 && k < len(toks); k++ {
						taken[k] = true
					}
					return "1"
				}
				if u.Filler || u.Fold == "in" || u.Fold == "em" ||
					u.Fold == "the" || u.Fold == "into" || u.Fold == "within" ||
					u.has("find") || u.Fold == "search" || u.Fold == "look" ||
					u.Fold == "descend" || u.Fold == "go" || u.Fold == "recurse" {
					continue
				}
				break
			}
		}
	}
	cue, minSeen, maxSeen := -1, -1, -1
	for i, t := range toks {
		switch t.Fold {
		case "level", "levels", "nível", "níveis", "nivel", "niveis", "depth", "profundidade", "deep":
			if cue < 0 {
				cue = i
			}
		case "minimum", "minimo", "mínimo", "least", "at least", "more", "mais", "over", "acima", "deeper":
			if minSeen < 0 {
				minSeen = i
			}
		case "maximum", "maximo", "máximo", "most", "at most", "only", "ate", "até", "up to", "below", "abaixo", "shallow":
			if maxSeen < 0 {
				maxSeen = i
			}
		}
	}
	if cue < 0 {
		return ""
	}
	if wantMin != (minSeen >= 0) || (minSeen >= 0 && maxSeen >= 0) {
		return ""
	}
	if minSeen < 0 && maxSeen < 0 && !wantMin {
		// "level 2" with no comparative is an upper bound
	} else if !wantMin && maxSeen < 0 {
		return ""
	}
	for i, t := range toks {
		if t.Fold == "" || !nuIsNumber(t.Fold) {
			continue
		}
		if i < cue-2 || i > cue+2 {
			continue
		}
		taken[i], taken[cue] = true, true
		// o comparativo é phrasing da janela — "maximum levels temp"
		// solto colava no sponge de name
		if minSeen >= 0 {
			taken[minSeen] = true
		}
		if maxSeen >= 0 {
			taken[maxSeen] = true
		}
		// "2 levels deep" — todos os cue-words da janela são phrasing;
		// 'deep' solto virava name:deep
		for j, u := range toks {
			switch u.Fold {
			case "level", "levels", "nível", "níveis", "nivel", "niveis",
				"depth", "profundidade", "deep":
				taken[j] = true
			}
		}
		return t.Fold
	}
	return ""
}

// nuFindSize turns "maiores que 10MB" / "larger than 100k" into bytes for
// min_size/max_size; the comparative cue picks the direction, so plain
// "maiores arquivos" stays a sort, not a filter.
func nuFindSize(toks []nuTok, min bool, taken map[int]bool) string {
	minSeen, maxSeen, exact := false, false, false
	minCueAt, maxCueAt := -1, -1
	// ">100" e ">=100" não são o mesmo filtro — comparativo estrito
	// ajusta o limiar em ±1 byte (todos os valores viram bytes)
	strictMin, strictMax := false, false
	sharedUnit := ""
	for i, t := range toks {
		switch t.Fold {
		case "least", "minimum", "minimo", "mínimo":
			minSeen = true
			if minCueAt < 0 {
				minCueAt = i
			}
		case "maior", "maiores", "larger", "bigger", "over", "above", "acima", "exceeding", "more",
			"greater", "greatest", "largest", "longer":
			minSeen, strictMin = true, true
			if minCueAt < 0 {
				minCueAt = i
			}
		case "most", "maximum", "maximo", "máximo", "ate", "até":
			maxSeen = true
			if maxCueAt < 0 {
				maxCueAt = i
			}
		case "menor", "menores", "menos", "smaller", "under", "below", "less", "abaixo",
			"smallest", "fewer":
			maxSeen, strictMax = true, true
			if maxCueAt < 0 {
				maxCueAt = i
			}
		case "size", "sized", "tamanho", "bytes", "kilobytes", "megabytes", "gigabytes":
			exact = true // a size cue without a comparative is an exact-size ask
		}
		if m := nuSizeRe.FindStringSubmatch(t.Fold); m != nil && m[2] != "" {
			exact = true // "10mb" carries its own unit: self-evident size
		}
	}
	// "between X and Y" é faixa posicional: o piso vem depois de
	// 'between', o teto depois de 'and'. Sem isso a faixa colapsava
	// pra exact (min=max=X) e o Y vazava pra outro slot
	if betweenAt := func() int {
		for i, t := range toks {
			if t.Fold == "between" || t.Fold == "entre" {
				return i
			}
		}
		return -1
	}(); betweenAt >= 0 {
		minSeen, maxSeen = true, true
		minCueAt = betweenAt
		maxCueAt = -1
		for j := betweenAt + 1; j < len(toks); j++ {
			if toks[j].Fold == "and" || toks[j].Fold == "e" {
				maxCueAt = j
				break
			}
		}
		if maxCueAt < 0 {
			maxCueAt = len(toks)
		}
		// os cues da faixa são consumidos pelo extrator — não
		// podem sobrar como operando solto no Dropped
		taken[betweenAt] = true
		if maxCueAt < len(toks) {
			taken[maxCueAt] = true
		}
		// unidade compartilhada: "between 10 and 50 MB" — o 10
		// herda o MB do fim da faixa, senão ele vazava pra
		// outro slot (limit) e o piso sumia
		for j := betweenAt + 1; j < len(toks); j++ {
			if _, ok := nuSizeUnit[toks[j].Fold]; ok {
				sharedUnit = toks[j].Fold
				break
			}
		}
	} else if minSeen == maxSeen {
		if !exact {
			return ""
		}
		// exact size: min and max converge on the same value
	} else if min != minSeen {
		return ""
	}
	// faixa "bigger than X and less than Y" — cada slot pega o SEU
	// lado: o min procura o tamanho depois do comparativo-de-piso,
	// o max depois do comparativo-de-teto; sem isso os dois slots
	// pegam o primeiro número e a faixa colapsa (min=max=X)
	lo, hi := 0, len(toks)
	if minSeen && maxSeen {
		if min {
			lo = minCueAt
			if maxCueAt > minCueAt {
				hi = maxCueAt
			}
		} else {
			lo = maxCueAt
			if minCueAt > maxCueAt {
				hi = minCueAt
			}
		}
	}
	for i := lo; i < hi; i++ {
		t := toks[i]
		m := nuSizeRe.FindStringSubmatch(t.Fold)
		if m == nil {
			continue
		}
		unit := m[2]
		if unit == "" {
			if i+1 < len(toks) {
				u := toks[i+1].Fold
				if _, ok := nuSizeUnit[u]; ok {
					unit = u
					taken[i+1] = true
				}
			}
			if unit == "" {
				unit = sharedUnit
			}
			if unit == "" {
				continue
			}
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		taken[i] = true
		v := int64(n * float64(nuSizeUnit[unit]))
		if minSeen != maxSeen { // comparativo estrito ajusta ±1 byte
			if min && strictMin {
				v++
			} else if !min && strictMax && v > 0 {
				v--
			}
		}
		return strconv.FormatInt(v, 10)
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
	// "tail the last 50 lines of syslog" — o arquivo vem depois do
	// 'of'/'in'. Meta-substantivos ("context", "file") são transparentes.
	if what == "file" {
		fileWord := func(u *nuTok, j int) bool {
			if u.Filler || u.Slang || taken[j] {
				return false
			}
			switch u.Fold {
			case "context", "contexto", "file", "files", "arquivo",
				"arquivos", "text", "texto", "input":
				return false
			}
			// substantivos de arquivo (syslog→logs) são nome válido;
			// conceitos de outro domínio não nomeiam arquivo
			for _, c := range u.Concs {
				switch c {
				case "logs", "file", "filenoun", "filequoted":
				default:
					return false
				}
			}
			return !nuIsNumber(u.Fold)
		}
		ambiguousAhead := func(i int) bool {
			for k := i + 1; k < len(toks) && k <= i+4; k++ {
				w := &toks[k]
				if w.Filler || w.Fold == "," {
					continue
				}
				switch w.Fold {
				case "and", "or", "e", "ou", "mas":
					for l := k + 1; l < len(toks) && l <= k+3; l++ {
						v := &toks[l]
						if v.Quoted || nuFileShaped(v.Fold) ||
							strings.ContainsAny(v.Raw, "./") {
							return true
						}
					}
				}
				break
			}
			return false
		}
		for i, t := range toks {
			switch t.Fold {
			case "of", "de", "do", "da", "from", "in", "em", "no", "na":
			default:
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+3; j++ {
				u := &toks[j]
				if u.Filler || u.Slang || taken[j] {
					continue
				}
				switch u.Fold {
				case "context", "contexto", "file", "files", "arquivo",
					"arquivos", "text", "texto", "input":
					continue
				}
				if !fileWord(u, j) {
					return ""
				}
				// "in 'a' and 'b'" — slot único não pode escolher um
				if ambiguousAhead(j) {
					return ""
				}
				taken[i], taken[j] = true, true
				return u.Raw
			}
		}
		// sem cue: token com cara de arquivo é o alvo ("line 5 of
		// a.txt" já caiu no cue; "slice active_record.rb" precisa disto)
		for i, t := range toks {
			if taken[i] || t.Filler || t.Slang {
				continue
			}
			if nuFileShaped(t.Fold) || strings.ContainsAny(t.Raw, "./") {
				if ambiguousAhead(i) {
					return ""
				}
				taken[i] = true
				return t.Raw
			}
		}
		return ""
	}
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
	// "tail the LAST 50 lines": cue-words em sequência — o primeiro acha
	// o número e retorna; os demais não podem sobrar como unfed.
	markSliceCues := func(upto int) {
		for j := 0; j <= upto && j < len(toks); j++ {
			switch toks[j].Fold {
			case "ultimas", "últimas", "ultimos", "últimos", "last",
				"tail", "finais", "final", "primeiras", "primeiros",
				"first", "head", "iniciais":
				taken[j] = true
			}
		}
	}
	for i, t := range toks {
		switch t.Fold {
		case "ultimas", "últimas", "ultimos", "últimos", "last", "tail", "finais", "final":
			if what == "tail" {
				if n := numAfter(i); n != "" {
					markSliceCues(i + 3)
					return n
				}
			}
		case "primeiras", "primeiros", "first", "head", "iniciais":
			if what == "start" {
				markSliceCues(i + 3)
				return "1"
			}
			if what == "end" {
				if n := numAfter(i); n != "" {
					markSliceCues(i + 3)
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
	"php": true, "htm": true, "jsx": true, "tsx": true,
	"vue": true, "kt": true, "scala": true, "lua": true,
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

// nuArchiveWord: archive-shaped OU palavra-formato solta ("tarball", "a
// zip", "num tgz") — o cap resolve o formato e deriva o nome da fonte.
func nuArchiveWord(f string) bool {
	switch f {
	case "tar", "tarball", "zip", "tgz", "gz", "gzip", "jar", "whl", "tar.gz",
		// formatos citados na frase mesmo sem suporte — "with bzip2" é
		// especificação de formato, nunca nome de fonte ou dir
		"bzip2", "bzip", "pbzip2", "xz", "lzma", "lz4", "zstd", "zstdmt",
		"7z", "7za", "rar", "unrar", "cab", "arj", "lzh",
		"bz2", "tbz", "tbz2", "txz", "tlz4",
		// nomes de ferramenta de compressão — "using 'pigz' as a
		// compress program" é a FERRAMENTA, nunca fonte ou destino
		"pigz", "plzip", "lbzip2", "lzip", "pixz", "brotli", "lzop",
		"zcat", "snzip", "unar", "p7zip", "zipinfo", "7zr", "7zz":
		return true
	}
	return nuArchiveName(f)
}

// nuMkScopeDir — a BASE do escopo "every directory": "…in every directory
// inside backups" → "backups"; "…in the current directory" → "."
// (auto-referência); sem base explícita → "" (o método assume "."). O path
// casa com a MESMA janela de cue do nuMkScope; auto-referências
// (current/directory/.) e paths inseguros são normalizados.
func nuMkScopeDir(toks []nuTok, hits []nuHit, taken map[int]bool) string {
	quants := map[string]bool{
		"every": true, "each": true, "all": true, "any": true,
		"cada": true, "toda": true, "todas": true, "todo": true, "todos": true,
	}
	preps := map[string]bool{
		"in": true, "inside": true, "under": true, "within": true, "into": true,
		"em": true, "no": true, "na": true, "nos": true, "nas": true,
		"sob": true, "dentro": true, "por": true,
	}
	selfRef := map[string]bool{
		"directory": true, "dir": true, "folder": true, "pasta": true,
		"diretorio": true, "atual": true, "current": true, "corrente": true,
		".": true, "aqui": true, "here": true,
	}
	for i, t := range toks {
		if t.Filler || t.Quoted || !nuPluralDirCues[t.Fold] {
			continue
		}
		q := -1
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if quants[toks[j].Fold] {
				q = j
				break
			}
			if !toks[j].Filler {
				break
			}
		}
		if q < 0 {
			continue
		}
		// varre à direita do cue: primeira prep + próxima palavra real.
		// Preps de escopo são FILLERS no léxico ("in", "em", "inside") —
		// checar preps ANTES do salto de filler ou elas evaporam.
		for j := i + 1; j < len(toks) && j <= i+6; j++ {
			u := toks[j]
			if preps[u.Fold] {
				for k := j + 1; k < len(toks) && k <= j+4; k++ {
					c := toks[k]
					if c.Filler {
						continue
					}
					if selfRef[c.Fold] || nuPluralDirCues[c.Fold] {
						return "."
					}
					p := c.Raw
					if p == "" {
						p = c.Fold
					}
					if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") ||
						strings.HasPrefix(p, "~") || strings.ContainsAny(p, "\\\x00") {
						return ""
					}
					taken[k] = true
					taken[j] = true
					return p
				}
				return ""
			}
			if u.Filler {
				continue
			}
			break
		}
		return ""
	}
	return ""
}

// nuDroppedOps — operando citado ou com papel semântico (nametgt/
// forquoted/grepword/outtgt) que NENHUM slot consumiu: "matches
// pattern 'venet0:0|eth0'" num intent sem slot de padrão. A aspa
// sobrevive ao fill solta (taken=false) e seu Raw não aparece em
// valor algum — ignorá-la muda o pedido. O cérebro lê essa lista e
// derruba pra unverified em vez de selar sucesso incompleto (T8).
func nuDroppedOps(toks []nuTok, taken map[int]bool, slots map[string]string) []string {
	var out []string
	for i := range toks {
		t := &toks[i]
		if !t.Quoted || taken[i] || t.Raw == "" {
			continue // só aspa é operando; cue não-citado ("containing") é palavra-ponte
		}
		consumed := false
		for _, sv := range slots {
			if sv != "" && strings.Contains(sv, t.Raw) {
				consumed = true
				break
			}
		}
		if !consumed {
			out = append(out, t.Raw)
		}
	}
	return out
}

// nuExceptCues — marcadores que abrem cláusula de EXCEÇÃO: o que vem
// depois é o que NÃO se quer, nunca o alvo de um slot comum.
var nuExceptCues = map[string]bool{
	"except": true, "exceto": true, "excluding": true, "excluindo": true,
	"without": true, "ignoring": true, "ignore": true, "menos": true,
	"expect": true, "not": true,
	// "omitting directory X"/"skipping the .git folder" — prune é a
	// mesma exclusão de glob que o except produz
	"omit": true, "omits": true, "omitted": true, "omitting": true,
	"omite": true, "omitir": true, "omitindo": true,
	"skip": true, "skips": true, "skipped": true, "skipping": true,
	"skiping": true, "pula": true, "pulando": true, "pule": true,
}

// nuInExceptClause — o token i está dentro do SPAN de uma cláusula de
// exceção? O span vai do cue ao seu último operando ("except path
// docs/x" cobre path e docs/x; "except a.txt IN dir" NÃO cobre dir —
// o escopo depois do operando já é outra cláusula).
func nuInExceptClause(toks []nuTok, i int) bool {
	cues, _, last := nuExceptOperands(toks, nil)
	for _, cue := range cues {
		if i >= cue && i <= last[cue] {
			return true
		}
	}
	return false
}

// nuExceptBridge — palavras-ponte entre o cue e o operando ("except
// PATHS CONTAINING 'a'", "except IN tmp", "except those NAMED foo").
// Só contam ANTES do primeiro operando — depois dele, "in dir" já é
// escopo novo ("except a.txt in dir" não exclui dir).
var nuExceptBridge = map[string]bool{
	"path": true, "paths": true, "caminho": true, "caminhos": true,
	"dir": true, "directory": true, "directories": true,
	"folder": true, "folders": true, "pasta": true, "pastas": true,
	"named": true, "called": true, "nomeado": true, "chamado": true,
	"containing": true, "matching": true, "match": true,
	"contendo": true, "pattern": true, "padrao": true, "padrão": true,
	"like": true, "como": true, "the": true, "o": true, "a": true,
	"os": true, "as": true, "in": true, "inside": true, "under": true,
	"em": true, "no": true, "na": true, "from": true, "de": true,
	"regular": true, "those": true, "that": true,
	// verbos de locação: "not stored/kept/located in ./pdf" — o que se
	// exclui é o LUGAR, não o verbo
	"stored": true, "kept": true, "located": true, "placed": true,
	"present": true, "residing": true, "saved": true, "contained": true,
	"held": true, "sitting": true, "living": true, "currently": true,
	"armazenado": true, "armazenada": true, "armazenados": true,
	"guardado": true, "guardada": true, "guardados": true,
	"localizado": true, "localizada": true, "localizados": true,
	"presente": true, "presentes": true, "salvo": true, "salva": true,
	"salvos": true, "contido": true, "contida": true, "contidos": true,
	// quantificador dentro da exceção é ponte — "excluding all hidden
	// files" tem 'hidden' no span; sem 'all' a cláusula fechava no cue
	"all": true, "every": true, "each": true, "any": true,
	"todos": true, "todas": true, "todo": true, "toda": true,
	"cada": true, "qualquer": true, "quaisquer": true,
}

// nuExceptClass — substantivo-classe que ROTULA o operando sem ser um
// ("except git FILES", "except hidden files", "without the EXTENSION
// .tmp") — nunca entra na lista.
var nuExceptClass = map[string]bool{
	"files": true, "file": true, "dirs": true, "directories": true,
	"folders": true, "arquivos": true, "arquivo": true,
	"pastas": true, "subdirs": true, "subdirectories": true,
	"ones": true, "one": true, "uns": true, "umas": true,
	"extension": true, "extensão": true, "extensao": true,
	"permission": true, "permissions": true, "permissão": true,
	"permissões": true, "permissoes": true, "mode": true, "modo": true,
	"owner": true, "dono": true, "user": true, "usuario": true,
	"usuário": true, "group": true, "grupo": true, "size": true,
	"tamanho": true, "name": true, "nome": true, "names": true,
}

// nuExceptOperands — cue→operandos de cada cláusula de exceção.
// Depois do cue atravessa fillers/pontes/classes até o 1º operando;
// a lista continua via sep ("except a AND b"); palavra solta depois
// do operando fica PENDENTE — um sep posterior a confirma como item
// ("alpha beta and gamma" → os 3), sem sep ela só estende o span pra
// ninguém roubar ("alpha beta" → beta nunca vira name). `last` é o
// fim do span de cada cue. taken!=nil pula o consumido (extrator).
func nuExceptOperands(toks []nuTok, taken map[int]bool) (cues, ops []int, last map[int]int) {
	last = map[int]int{}
	for i, t := range toks {
		if !nuExceptCues[t.Fold] {
			continue
		}
		if t.Fold == "not" || t.Fold == "não" {
			// "not modified/changed/accessed in the last X" é negação de
			// JANELA temporal (older), não exclusão — "not" como cue
			// roubava o particípio (exclude:modified) E invertia o filtro
			timeNeg := false
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "" {
					continue
				}
				switch u.Fold {
				case "modified", "modificado", "modificada", "modificados",
					"modificadas", "changed", "alterado", "alterada",
					"accessed", "acessado", "acessada", "created", "criado",
					"criada", "touched", "been":
					timeNeg = true
					continue
				}
				break
			}
			if timeNeg {
				continue
			}
		}
		if t.Fold == "menos" || t.Fold == "less" {
			// "less than 2 days ago" / "menos de 5 dias" é COMPARATIVO
			// de quantidade/tempo, não exceção — o slang folda "less
			// than"→"menos" e o cue engolia a unidade (exclude:days).
			comparative := false
			for j := i + 1; j < len(toks) && j <= i+5; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "" {
					continue
				}
				if nuIsNumber(u.Fold) || nuDigitSize(u.Fold) ||
					u.Fold == "que" || u.Fold == "than" || u.Fold == "de" ||
					u.Fold == "do" || u.Fold == "of" || u.Fold == "to" ||
					u.Fold == "a" {
					comparative = true
					continue
				}
				break
			}
			if comparative {
				continue
			}
		}
		var myOps, pending []int
		span := i
		wantOperand := true
		for j := i + 1; j < len(toks) && j <= i+6; j++ {
			u := &toks[j]
			f := u.Fold
			if nuExceptCues[f] {
				break // outro cue fecha a cláusula
			}
			if nuEnumStops[f] && !nuExceptBridge[f] {
				break // escopo/destino fecha ("with/where/every") — mas
				// "in/under/named" são PONTE: "except in tmp" marca tmp
			}
			if nuEnumSeps[f] {
				myOps = append(myOps, pending...) // "a b and c": b era pendente
				pending = nil
				wantOperand = true
				continue
			}
			if f == "" || u.Filler || nuIsNumber(f) {
				continue
			}
			if !wantOperand {
				// palavra solta pós-operando: item suspeito de lista sem
				// sep — pendente até um sep confirmar; o span cobre pra
				// ninguém reivindicar como name/dir.
				if len(u.Concs) == 0 && !u.Quoted && u.Fold != "" {
					pending = append(pending, j)
					span = j
					continue
				}
				break
			}
			if nuExceptBridge[f] && !u.Quoted {
				continue
			}
			if nuExceptClass[f] && !u.Quoted {
				continue // "except files in tmp" — a classe rotula, não exclui
			}
			// "not empty"/"não vazio"/"not read-only" — 'not' + findattr/
			// permattr é NEGAÇÃO DE PREDICADO (not_empty/nx/not_writable
			// cobrem), não glob de exclusão. "except hidden" segue
			// exclusão — o cue real é 'except'.
			if (t.Fold == "not" || t.Fold == "não" || t.Fold == "nao") &&
				(u.has("findattr") || u.has("permattr")) && !u.Quoted {
				break
			}
			if taken != nil && taken[j] {
				continue
			}
			// operando: palavra solta, literal citado, ou conceito-META
			// de nome ("except those named foo" → foo é nametgt; "hidden
			// files" → hidden é findattr; "the extension .tmp" → extcue).
			// Conceito de OUTRO slot (newest→sort, user→owner…) fecha a
			// cláusula — aquilo não cabe em glob de exclusão e vira
			// Dropped honesto na invariante, nunca exclusão inventada.
			classNext := j+1 < len(toks) && nuExceptClass[toks[j+1].Fold]
			metaName := u.has("nametgt") || u.has("nameattr") || u.has("extcue") ||
				u.has("glob") || u.has("findattr") || u.has("filequoted") ||
				u.has("forquoted") || u.has("outtgt") || u.has("intocue")
			concsOK := len(u.Concs) == 0 || metaName || classNext || u.Quoted
			if !concsOK {
				// conceito de outro slot antes de ponte/classe é
				// qualificador ("shell PATTERN 'x'", "newest FILES") —
				// atravessa; sozinho ("owned BY wnj") fecha a cláusula
				// e o operando dele fica livre pro próprio slot.
				nf := ""
				for k := j + 1; k < len(toks) && k <= j+3; k++ {
					if toks[k].Filler || toks[k].Fold == "" {
						continue
					}
					nf = toks[k].Fold
					break
				}
				if nf != "" && (nuExceptBridge[nf] || nuExceptClass[nf]) {
					span = j
					continue
				}
				break
			}
			myOps = append(myOps, j)
			span = j
			wantOperand = false
		}
		if len(myOps) > 0 || span > i {
			cues = append(cues, i)
			ops = append(ops, myOps...)
			last[i] = span
		}
	}
	return cues, ops, last
}

// nuExcludeRaw — o valor do operando pro slot exclude: "hidden" vira o
// glob de dotfile; o resto vai como foi digitado (Raw preserva aspa/
// maiúscula; Fold é o fallback).
func nuExcludeRaw(t *nuTok) string {
	switch t.Fold {
	case "hidden", "oculto", "oculta", "ocultos", "ocultas", "dotfiles",
		"escondido", "escondida", "escondidos", "escondidas",
		"invisible", "dot", "dot-files", "dotfile", "hiddenfiles":
		return ".*"
	}
	if t.Raw != "" {
		return t.Raw
	}
	return t.Fold
}

// nuDigitSize: "1mb", "10k", "512b", "2g" — número colado a unidade de
// tamanho. Filtro de tamanho em pedidos de move/copy não pode virar o
// alvo do rename ("move files >1MB to x" movia o literal '1MB').
func nuDigitSize(f string) bool {
	i := 0
	for i < len(f) && f[i] >= '0' && f[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	switch f[i:] {
	case "b", "byte", "bytes", "k", "kb", "kbytes", "m", "mb", "mbytes",
		"g", "gb", "gbytes", "t", "tb", "tbytes":
		return true
	}
	return false
}

// nuStopBare: palavras que nuOperandBare deixa passar mas nunca são um
// nome citado — "directory to ./x": o "to" é o cue, não o alvo.
var nuStopBare = map[string]bool{
	"tree": true, "current": true, "atual": true, "to": true, "the": true,
	"a": true, "an": true, "in": true, "for": true, "of": true,
	"with": true, "from": true, "into": true, "and": true, "or": true,
	"de": true, "em": true, "para": true, "pra": true, "com": true,
	"por": true, "at": true, "on": true, "as": true, "by": true,
	"that": true, "which": true, "under": true, "over": true,
	// phrasing de faixa ("between 5kB and 10kB") — o extrator de
	// tamanho consome como cue; nunca é nome de arquivo
	"between": true, "entre": true,
}

// nuOperandBare: palavra solta sem conceito que vale como nome — o
// complemento de nuNameShaped para operandos citados por substantivo
// ("the folder src": 'src' não tem cara de path mas é o nome citado).
func nuOperandBare(f string) bool {
	if f == "" {
		return false
	}
	for _, r := range f {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// nuMonthNames: mês → MM para datas absolutas ("modified after
// February 1" / "in March 2007"). EN + PT.
// nuTemporalVocab: palavras que só pertencem a frase de tempo — quando
// um slot de nome/dir/exclude sai formado só delas, é vazamento da
// janela temporal e não operando real.
var nuTemporalVocab = map[string]bool{
	"modified": true, "modificado": true, "modificada": true,
	"modificados": true, "modificadas": true, "accessed": true,
	"acessado": true, "acessada": true, "changed": true,
	"alterado": true, "alterada": true, "created": true, "criado": true,
	"criada": true, "touched": true, "last": true, "past": true,
	"ago": true, "within": true, "recent": true, "recente": true,
	"recentes": true, "recently": true, "newer": true, "older": true,
	"yesterday": true, "today": true, "tomorrow": true, "ontem": true,
	"hoje": true, "amanha": true, "amanhã": true, "anteontem": true,
	"minute": true, "minutes": true, "min": true, "mins": true,
	"hour": true, "hours": true, "day": true, "days": true,
	"week": true, "weeks": true, "month": true, "months": true,
	"year": true, "years": true, "fortnight": true, "fortnights": true,
	"minuto": true, "minutos": true, "hora": true, "horas": true,
	"dia": true, "dias": true, "semana": true, "semanas": true,
	"mes": true, "mês": true, "meses": true, "ano": true, "anos": true,
	"antes": true, "depois": true, "before": true, "after": true,
	"since": true, "until": true, "desde": true, "até": true, "ate": true,
	"há": true, "ha": true, "over": true,
	// auxiliares e phrasing que viajam na cláusula temporal
	"were": true, "was": true, "been": true, "are": true, "is": true,
	"that": true, "which": true, "those": true, "these": true,
	"them": true, "the": true, "of": true, "a": true, "an": true,
	"s": true, "have": true, "has": true, "in": true, "em": true,
	"on": true, "at": true, "to": true, "current": true, "atual": true,
	"this": true, "este": true, "esta": true, "and": true, "or": true,
	"e": true, "ou": true, "between": true, "entre": true, "from": true,
	"de": true, "do": true, "da": true, "for": true, "by": true,
	"not": true, "more": true, "less": true, "than": true,
	"most": true, "least": true, "about": true, "around": true,
}

// nuAllTemporal: todo fragmento do valor é vocábulo temporal (ou número
// de data) — o "nome" é a frase de tempo colada, nunca um arquivo.
func nuAllTemporal(v string) bool {
	for _, p := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == '|'
	}) {
		lp := strings.ToLower(p)
		if nuTemporalVocab[lp] {
			continue
		}
		if _, isM := nuMonthNames[lp]; isM {
			continue
		}
		if nuIsNumber(lp) || nuIsoDateRe.MatchString(lp) {
			continue
		}
		return false
	}
	return true
}

var nuMonthNames = map[string]string{
	"january": "01", "february": "02", "march": "03", "april": "04",
	"may": "05", "june": "06", "july": "07", "august": "08",
	"september": "09", "october": "10", "november": "11", "december": "12",
	"janeiro": "01", "fevereiro": "02", "março": "03", "marco": "03",
	"abril": "04", "maio": "05", "junho": "06", "julho": "07",
	"agosto": "08", "setembro": "09", "outubro": "10",
	"novembro": "11", "dezembro": "12",
}

// nuDateTok: o token i é data absoluta? ISO "2014-08-25", "August 10th",
// "March 2007" (mês+ano → primeiro dia do mês), "1 February". Devolve
// "YYYY-MM-DD" e quantos tokens a data ocupa.
func nuDateTok(toks []nuTok, i int) (string, int) {
	t := &toks[i]
	if m := nuIsoDateRe.FindStringSubmatch(t.Fold); m != nil {
		return t.Fold, 1
	}
	if mm, isM := nuMonthNames[t.Fold]; isM && i+1 < len(toks) {
		d := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
			strings.TrimSuffix(toks[i+1].Fold, "th"), "st"), "nd"), "rd")
		if nuIsNumber(d) && len(d) <= 2 {
			yr := "cur"
			if i+2 < len(toks) && len(toks[i+2].Fold) == 4 && nuIsNumber(toks[i+2].Fold) {
				yr = toks[i+2].Fold
				return yr + "-" + mm + "-" + pad2(d), 3
			}
			return yr + "-" + mm + "-" + pad2(d), 2
		}
		if len(toks[i+1].Fold) == 4 && nuIsNumber(toks[i+1].Fold) {
			return toks[i+1].Fold + "-" + mm + "-01", 2
		}
		return "cur-" + mm + "-01", 1
	}
	if nuIsNumber(t.Fold) && len(t.Fold) <= 2 && i+1 < len(toks) {
		if mm, isM := nuMonthNames[toks[i+1].Fold]; isM {
			yr := "cur"
			if i+2 < len(toks) && len(toks[i+2].Fold) == 4 && nuIsNumber(toks[i+2].Fold) {
				yr = toks[i+2].Fold
				return yr + "-" + mm + "-" + pad2(t.Fold), 3
			}
			return yr + "-" + mm + "-" + pad2(t.Fold), 2
		}
	}
	return "", 0
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

var nuIsoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// nuFindDateBound: bordas absolutas de data — "between 2014-08-25 and
// 2014-08-26" (newer=A, older=B~inclusive), "after February 1" (newer),
// "before X" (older), "in March 2007" (newer=03-01, older=04-01).
// Sufixo "~" marca borda inclusiva-ate-o-fim-do-dia; "cur" = ano corrente
// (o script resolve o ano mais recente possível).
func nuFindDateBound(toks []nuTok, newer bool, taken map[int]bool) string {
	// "between A and B": A é piso (newer), B é teto inclusivo (older~)
	for i, t := range toks {
		if t.Fold != "between" && t.Fold != "entre" {
			continue
		}
		var d1, d2 string
		var e1, e2 int
		for j := i + 1; j < len(toks) && j <= i+8; j++ {
			d, n := nuDateTok(toks, j)
			if n == 0 {
				continue
			}
			if d1 == "" {
				d1, e1 = d, j+n-1
				j += n - 1
				continue
			}
			d2, e2 = d, j+n-1
			break
		}
		if d1 == "" || d2 == "" {
			return ""
		}
		for k := i; k <= e2; k++ {
			taken[k] = true
		}
		_ = e1
		if newer {
			return d1
		}
		return d2 + "~"
	}
	// "in March 2007" / "in March" — o mês inteiro é a janela
	for i, t := range toks {
		if t.Fold != "in" && t.Fold != "em" {
			continue
		}
		j := i + 1
		for j < len(toks) && toks[j].Filler {
			j++
		}
		if j >= len(toks) {
			continue
		}
		mm, isM := nuMonthNames[toks[j].Fold]
		if !isM {
			continue
		}
		yr := "cur"
		end := j + 1
		if end < len(toks) && len(toks[end].Fold) == 4 && nuIsNumber(toks[end].Fold) {
			yr = toks[end].Fold
		}
		mNext := "01"
		if n, _ := strconv.Atoi(mm); n == 12 {
			yr = incrYear(yr)
		} else {
			mNext = pad2(strconv.Itoa(n + 1))
		}
		for k := i; k <= end; k++ {
			taken[k] = true
		}
		if newer {
			return yr + "-" + mm + "-01"
		}
		return yr + "-" + mNext + "-01"
	}
	// "after/since/from X" → piso newer; "before/until/older than X" →
	// teto older (exclusivo)
	for i, t := range toks {
		dir := 0
		switch t.Fold {
		case "after", "since", "from", "depois", "desde", "após", "apos":
			dir = 1
		case "before", "until", "antes", "até", "ate", "prior":
			dir = -1
		}
		if dir == 0 {
			continue
		}
		for j := i + 1; j < len(toks) && j <= i+5; j++ {
			d, n := nuDateTok(toks, j)
			if n == 0 {
				continue
			}
			for k := i; k <= j+n-1; k++ {
				taken[k] = true
			}
			nuConsumeYearTail(toks, j+n, taken)
			if (dir > 0) == newer {
				return d
			}
			return ""
		}
	}
	return ""
}

// nuConsumeYearTail: depois de um bound ("after February 1"), a cauda
// "of the current year" / "this year" é phrasing da data — sem consumi-la
// "current year" colava no sponge de name.
func nuConsumeYearTail(toks []nuTok, from int, taken map[int]bool) {
	end := from + 6
	if end > len(toks) {
		end = len(toks)
	}
	seenYear, seenCue := false, false
	for k := from; k < end; k++ {
		switch toks[k].Fold {
		case "year", "ano", "anos":
			seenYear = true
		case "current", "atual", "this", "este", "esta":
			seenCue = true
		}
	}
	if !seenYear || !seenCue {
		return
	}
	for k := from; k < end; k++ {
		switch toks[k].Fold {
		case "of", "de", "the", "o", "a", "current", "atual", "this",
			"este", "esta", "year", "ano", "anos":
			taken[k] = true
		}
	}
}

func incrYear(yr string) string {
	if yr == "cur" {
		return "next"
	}
	if n, err := strconv.Atoi(yr); err == nil {
		return strconv.Itoa(n + 1)
	}
	return yr
}

// nuDateArg: operandos do date_show — epoch (unix seconds), nome de dia
// (yesterday/tomorrow/today) e offset em minutos com sinal
// ("+2 days +10 minutes"=+2890, "222 days ago"=-320640).
func nuDateArg(toks []nuTok, what string, taken map[int]bool) string {
	switch what {
	case "epoch":
		for i, t := range toks {
			if t.Fold != "epoch" && t.Fold != "unix" && t.Fold != "timestamp" &&
				t.Fold != "timestamps" && t.Fold != "second" && t.Fold != "seconds" {
				continue
			}
			for j := i + 1; j < len(toks) && j <= i+4; j++ {
				u := &toks[j]
				if u.Filler || u.Fold == "" {
					continue
				}
				if nuAllDigits(u.Fold) && len(u.Fold) >= 6 {
					taken[i], taken[j] = true, true
					return u.Fold
				}
			}
		}
		// número longo solto já é epoch ("convert 1267619929 to date")
		for i, t := range toks {
			if taken[i] || !nuAllDigits(t.Fold) || len(t.Fold) < 6 {
				continue
			}
			taken[i] = true
			return t.Fold
		}
	case "when":
		for i, t := range toks {
			switch t.Fold {
			case "yesterday", "ontem", "tomorrow", "amanhã", "amanha",
				"today", "hoje", "now", "agora":
				taken[i] = true
				if i+1 < len(toks) && toks[i+1].Fold == "s" {
					taken[i+1] = true // "yesterday's"
				}
				for k := i - 2; k < i; k++ {
					if k < 0 {
						continue
					}
					switch toks[k].Fold {
					case "for", "the", "de", "em", "in", "of", "a", "para",
						"pra":
						taken[k] = true // "date for tomorrow"
					}
				}
				return t.Fold
			}
		}
	case "offset":
		// soma "+N unit" / "N unit" com sinal: "ago"/"antes" negativa,
		// "+N"/"in N"/"mais N" positiva. Sem cue, "N days" é positivo.
		total, found := 0, false
		for i, t := range toks {
			if taken[i] || !nuIsNumber(t.Fold) {
				continue
			}
			j := i + 1
			if j >= len(toks) {
				break
			}
			mult := 0
			switch toks[j].Fold {
			case "min", "mins", "minute", "minutes", "minuto", "minutos":
				mult = 1
			case "h", "hour", "hours", "hora", "horas":
				mult = 60
			case "d", "day", "days", "dia", "dias":
				mult = 1440
			case "week", "weeks", "semana", "semanas":
				mult = 10080
			case "month", "months", "mes", "mês", "meses":
				mult = 43200
			case "second", "seconds", "segundo", "segundos":
				continue // epoch já tem slot próprio
			}
			if mult == 0 {
				continue
			}
			n, _ := strconv.Atoi(t.Fold)
			sign := 1
			for k := i - 1; k >= 0 && k >= i-3; k-- {
				switch toks[k].Fold {
				case "ago", "antes", "before", "há", "ha", "-":
					sign = -1
				case "+", "plus", "mais", "in", "em", "after", "depois":
					sign = 1
				}
				if sign == -1 || toks[k].Fold == "+" || toks[k].Fold == "plus" ||
					toks[k].Fold == "mais" {
					break
				}
			}
			// "3 days ago" — o ago vem DEPOIS da unidade
			if j+1 < len(toks) {
				switch toks[j+1].Fold {
				case "ago", "antes", "há", "ha", "back":
					sign = -1
				}
			}
			total += sign * n * mult
			found = true
			taken[i], taken[j] = true, true
			// phrasing do offset não pode contar palavra solta
			for k := i - 2; k < i; k++ {
				if k < 0 {
					continue
				}
				switch toks[k].Fold {
				case "+", "plus", "mais", "in", "em", "for", "the", "a",
					"para", "pra", "of":
					taken[k] = true
				}
			}
			if j+1 < len(toks) {
				switch toks[j+1].Fold {
				case "ago", "antes", "há", "ha", "back", "later":
					taken[j+1] = true
				}
			}
		}
		if found {
			return strconv.Itoa(total)
		}
	}
	return ""
}

// nuAllDigits: nuIsNumber tem teto de 6 dígitos (idade em minutos) —
// epoch/unix precisa de dígitos puros sem limite.
func nuAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
