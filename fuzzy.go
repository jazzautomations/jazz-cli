package main

// fuzzy.go: typo tolerance primitives (docs: README "Erros de digitação").
//
// Deterministic and cheap: accent/case folding, a keyboard-aware
// Damerau-Levenshtein distance (a slip to an adjacent key costs half of an
// arbitrary substitution), and a PT/EN verb vocabulary that maps misspelled
// intents ("concerta", "roda", "toca") to a canonical meaning. Used by the
// slash-command resolver (fuzzy_cmd.go), the name resolver (nameindex.go),
// the offline fast paths (intents.go) and the task classifier (route_task.go).

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// fzFold lowercases and strips accents ("Ação" -> "acao").
//
// ASCII input (most names and commands) skips the NFD pass: NFD is the
// identity there and only A-Z change, so an already-lowercase ASCII string
// is returned as is (no allocation) and anything else costs one buffer.
func fzFold(s string) string {
	if i := fzASCIIUpperOrHigh(s); i < 0 {
		return s
	} else if fzIsASCII(s[i:]) {
		b := []byte(s)
		for j := i; j < len(b); j++ {
			if c := b[j]; c >= 'A' && c <= 'Z' {
				b[j] = c + 'a' - 'A'
			}
		}
		return string(b)
	}
	return fzFoldUnicode(s)
}

// fzASCIIUpperOrHigh is the index of the first byte that fzFold may
// change (A-Z or non-ASCII), -1 when there is none.
func fzASCIIUpperOrHigh(s string) int {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || c >= 'A' && c <= 'Z' {
			return i
		}
	}
	return -1
}

// fzFoldUnicode is the general path of fzFold.
func fzFoldUnicode(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// fzKey folds s and drops the separators people mix up: "Test-Calculo.py"
// and "test_calculo.py" share the key "testcalculopy"; camelCase keys equal
// their snake_case form.
func fzKey(s string) string {
	if fzIsASCII(s) {
		// one pass: lowercase and drop separators; unchanged input is
		// returned without allocating
		k := 0
		for ; k < len(s); k++ {
			if c := s[k]; c >= 'A' && c <= 'Z' || c == '_' || c == '-' || c == '.' || c == ' ' {
				break
			}
		}
		if k == len(s) {
			return s
		}
		b := make([]byte, k, len(s))
		copy(b, s[:k])
		for ; k < len(s); k++ {
			switch c := s[k]; {
			case c == '_' || c == '-' || c == '.' || c == ' ':
			case c >= 'A' && c <= 'Z':
				b = append(b, c+'a'-'A')
			default:
				b = append(b, c)
			}
		}
		return string(b)
	}
	var b strings.Builder
	for _, r := range fzFold(s) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Distance units: an arbitrary edit costs fzEdit, a slip to a neighbouring
// key or a transposition costs fzSlip.
const (
	fzEdit = 4
	fzSlip = 2
)

var fzPos = func() map[rune][2]int {
	m := map[rune][2]int{}
	rows := []string{"1234567890", "qwertyuiop", "asdfghjklç", "zxcvbnm"}
	off := []int{0, 1, 2, 3} // x offset in half keys: rows are staggered
	for r, row := range rows {
		for c, ch := range []rune(row) {
			m[ch] = [2]int{r, c*2 + off[r]}
		}
	}
	return m
}()

// fzNeighbour: a and b sit next to each other on a QWERTY/ABNT2 keyboard.
func fzNeighbour(a, b rune) bool {
	pa, ok1 := fzPos[a]
	pb, ok2 := fzPos[b]
	if !ok1 || !ok2 || a == b {
		return false
	}
	dr := pa[0] - pb[0]
	dx := pa[1] - pb[1]
	if dr < 0 {
		dr = -dr
	}
	if dx < 0 {
		dx = -dx
	}
	return dr <= 1 && dx <= 2 && (dr == 1 || dx == 2)
}

// fzDist is the weighted optimal-string-alignment distance of two already
// folded strings, in fzEdit units (stops early past max; returns max+1).
//
// ASCII strings (folded keys almost always are) with b of at most fzRowMax
// bytes take an allocation-free path: the three DP rows live in stack arrays
// and the keyboard-neighbour test is a bitset lookup instead of two map
// probes (low-level study §4.1: 15x faster, 0 allocs; on the 386 target the
// map hash has no AES-NI). Anything else falls back to fzDistRunes; both give
// the same result (TestFzDistASCIIEqualsRunes, FuzzFzDist).
func fzDist(a, b string, max int) int {
	if len(b) <= fzRowMax && max >= 0 && max <= 1<<30 && fzIsASCII(a) && fzIsASCII(b) {
		return fzDistASCII(a, b, max)
	}
	return fzDistRunes(a, b, max)
}

// fzRowMax is the longest b the stack rows of fzDistASCII hold.
const fzRowMax = 64

func fzIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// fzNb[x] is the set of ASCII bytes adjacent to x on the keyboard (bit y of
// word y>>6), the same relation as fzNeighbour. 2 KB: fits in L1.
var fzNb = func() (t [128][2]uint64) {
	for x := 0; x < 128; x++ {
		for y := 0; y < 128; y++ {
			if fzNeighbour(rune(x), rune(y)) {
				t[x][y>>6] |= 1 << (y & 63)
			}
		}
	}
	return t
}()

func fzDistASCII(a, b string, max int) int {
	if d := len(a) - len(b); d > max/fzEdit+1 || -d > max/fzEdit+1 {
		return max + 1
	}
	var r0, r1, r2 [fzRowMax + 1]int32
	n := len(b)
	prev2, prev, cur := r0[:n+1], r1[:n+1], r2[:n+1]
	for j := range prev {
		prev[j] = int32(j * fzEdit)
	}
	m := int32(max)
	for i := 1; i <= len(a); i++ {
		ca := a[i-1]
		cur[0] = int32(i * fzEdit)
		lo := cur[0]
		nb := &fzNb[ca&0x7f]
		for j := 1; j <= n; j++ {
			cb := b[j-1]
			var sub int32
			if ca != cb {
				sub = fzEdit
				if nb[cb>>6]>>(cb&63)&1 != 0 {
					sub = fzSlip
				}
			}
			v := prev[j-1] + sub
			if x := prev[j] + fzEdit; x < v {
				v = x
			}
			if x := cur[j-1] + fzEdit; x < v {
				v = x
			}
			if i > 1 && j > 1 && ca == b[j-2] && a[i-2] == cb {
				if x := prev2[j-2] + fzSlip; x < v {
					v = x
				}
			}
			cur[j] = v
			if v < lo {
				lo = v
			}
		}
		if lo > m {
			return max + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	if prev[n] > m {
		return max + 1
	}
	return int(prev[n])
}

// fzDistRunes is the general (Unicode) path of fzDist.
func fzDistRunes(a, b string, max int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > max/fzEdit+1 || -d > max/fzEdit+1 {
		return max + 1
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j * fzEdit
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i * fzEdit
		lo := cur[0]
		for j := 1; j <= len(rb); j++ {
			sub := 0
			if ra[i-1] != rb[j-1] {
				sub = fzEdit
				if fzNeighbour(ra[i-1], rb[j-1]) {
					sub = fzSlip
				}
			}
			v := prev[j-1] + sub
			if x := prev[j] + fzEdit; x < v {
				v = x
			}
			if x := cur[j-1] + fzEdit; x < v {
				v = x
			}
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if x := prev2[j-2] + fzSlip; x < v {
					v = x
				}
			}
			cur[j] = v
			if v < lo {
				lo = v
			}
		}
		if lo > max {
			return max + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	if prev[len(rb)] > max {
		return max + 1
	}
	return prev[len(rb)]
}

// fzLimit is how far a word of n runes may be from its target: one edit up to
// 4 letters, two edits (or four neighbour slips) from 7.
func fzLimit(n int) int {
	switch {
	case n <= 3:
		return fzEdit / 2 // a single neighbour slip or transposition
	case n <= 4:
		return fzEdit
	case n <= 6:
		return fzEdit + fzSlip
	}
	return 2 * fzEdit
}

// fzNearWord: typed is within the typo limit of target (both folded).
func fzNearWord(typed, target string) (int, bool) {
	lim := fzLimit(len([]rune(typed)))
	d := fzDist(typed, target, lim)
	return d, d <= lim
}

// fzVerbs maps verb spellings (folded) to a canonical intent. Misspellings
// are resolved by fzIntent through fzNearWord.
var fzVerbs = map[string]string{
	// fix
	"conserta": "fix", "conserte": "fix", "consertar": "fix", "concerta": "fix", "concerte": "fix", "arruma": "fix", "arrume": "fix",
	"arrumar": "fix", "ajeita": "fix", "ajeite": "fix", "corrige": "fix", "corrija": "fix", "corrigir": "fix", "fix": "fix", "resolve": "fix", "resolva": "fix",
	// run
	"roda": "run", "rode": "run", "rodar": "run", "executa": "run", "execute": "run", "executar": "run", "toca": "run", "run": "run", "rola": "run", "dispara": "run",
	// open / read
	"abre": "open", "abra": "open", "abrir": "open", "open": "open", "le": "open", "leia": "open", "ler": "open", "read": "open", "mostra": "open", "mostre": "open", "show": "open", "cat": "open", "ve": "open",
	// edit
	"edita": "edit", "edite": "edit", "editar": "edit", "edit": "edit", "muda": "edit", "mude": "edit", "mudar": "edit", "altera": "edit", "altere": "edit", "alterar": "edit", "change": "edit", "atualiza": "edit", "atualize": "edit", "update": "edit",
	// search
	"procura": "search", "procure": "search", "procurar": "search", "busca": "search", "busque": "search", "buscar": "search", "acha": "search", "ache": "search", "achar": "search", "encontra": "search", "encontre": "search", "find": "search", "search": "search", "grep": "search",
	// create
	"cria": "create", "crie": "create", "criar": "create", "add": "create", "adiciona": "create", "adicione": "create", "adicionar": "create", "create": "create", "gera": "create", "gere": "create",
	// explain
	"explica": "explain", "explique": "explain", "explicar": "explain", "explain": "explain", "resume": "explain", "resuma": "explain", "descreve": "explain", "descreva": "explain",
	// install
	"instala": "install", "instale": "install", "instalar": "install", "install": "install",
	// test
	"testa": "test", "teste": "test", "testar": "test", "test": "test",
}

// fzVerbEnt is one fzVerbs entry with its rune length.
type fzVerbEnt struct {
	k, v string
	n    int
}

// fzVerbList is fzVerbs as a slice sorted by key (built once; the result of
// fzIntent does not depend on the order, the slice only avoids the map walk).
var fzVerbList = sync.OnceValue(func() []fzVerbEnt {
	out := make([]fzVerbEnt, 0, len(fzVerbs))
	for k, v := range fzVerbs {
		out = append(out, fzVerbEnt{k, v, len([]rune(k))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].k < out[j].k })
	return out
})

// fzIntent resolves a (possibly misspelled) verb to its canonical intent.
func fzIntent(w string) string {
	w = fzFold(w)
	if v, ok := fzVerbs[w]; ok {
		return v
	}
	if len([]rune(w)) < 3 {
		return ""
	}
	best, bd, tie := "", 1<<30, false
	n := len([]rune(w))
	lim := fzLimit(n)
	for _, e := range fzVerbList() {
		if e.n < 4 {
			continue
		}
		if dl := e.n - n; dl*fzEdit > lim || -dl*fzEdit > lim {
			continue // lower bound: each length unit is an insertion or deletion
		}
		k, v := e.k, e.v
		if d, ok := fzNearWord(w, k); ok && (n >= 5 || d <= fzSlip) {
			switch {
			case d < bd:
				best, bd, tie = v, d, false
			case d == bd && v != best:
				tie = true
			}
		}
	}
	if tie {
		return ""
	}
	return best
}

// fzRepairLine rewrites the words of s that are within the typo limit of a
// vocabulary word (len >= 4, not already a vocabulary word) to that word.
// Returns the repaired text and whether anything changed. Unique best match
// only; words with digits, paths or capitals-inside are left alone.
func fzRepairLine(s string, vocab map[string]bool) (string, bool) {
	words := strings.Fields(s)
	changed := false
	for i, w := range words {
		f := fzFold(w)
		if len([]rune(f)) < 4 || vocab[f] || strings.ContainsAny(f, "0123456789/\\._@-`'\"") {
			continue
		}
		best, bd, tie := "", 1<<30, false
		n := len([]rune(f))
		lim := fzLimit(n)
		for v := range vocab {
			if dl := len(v) - n; (dl*fzEdit > lim || -dl*fzEdit > lim) && fzIsASCII(v) {
				continue // lower bound (ASCII v: bytes = runes): one edit per length unit
			}
			if d, ok := fzNearWord(f, v); ok {
				switch {
				case d < bd:
					best, bd, tie = v, d, false
				case d == bd && v != best:
					tie = true
				}
			}
		}
		if best != "" && !tie {
			words[i] = best
			changed = true
		}
	}
	return strings.Join(words, " "), changed
}
