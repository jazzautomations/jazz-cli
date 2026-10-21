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
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// fzFold lowercases and strips accents ("Ação" -> "acao").
func fzFold(s string) string {
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
func fzDist(a, b string, max int) int {
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
	for k, v := range fzVerbs {
		if len([]rune(k)) < 4 {
			continue
		}
		if d, ok := fzNearWord(w, k); ok && (len([]rune(w)) >= 5 || d <= fzSlip) {
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
		for v := range vocab {
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
