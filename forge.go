package main

// forge.go: `jazz forge [--dry-run] [--json] [--list] [--forget T] [--add T I] [--revert]`
// — the factory loop of the asset-forge (0.14 etapa B).
//
// Two layers (the owner's design):
//
//  1. PERSONAL aliases (alias.go): "quando eu falar X é Y", [l] at the
//     confirm box or --add — instant, this machine only, no gate. Listed by
//     --list, removed by --forget.
//  2. The GATED forge (this file): the owner's menu picks become grammar
//     examples of the overlay, and only what survives the portão gets in —
//     the public grammar between releases is what the owner reviews and
//     ships, never something an agent wrote on a user's machine.
//
// Pipeline (spec 0.14b — every blocker is a filter below):
//
//  1. provenance: only corrections from an explicit pick at an interactive
//     human menu — how=menu, src=tty, position ≥ 2 (the top item is the
//     biased default: Joachims 2005), outcome=success, with a session id.
//     Enter/default confirms, --ask-stdin answers, tool output and model
//     text never become training labels.
//  2. cluster: near-duplicate corrections (canon trigram Jaccard ≥ 0.7) are
//     ONE candidate; the medoid is promoted, the rest are paraphrases for
//     the directed test. A cluster that contradicts itself (two intents) or
//     a reading the owner rejected at a menu (menu_skip) is "ambíguo" and
//     leaves the queue.
//  3. eligibility: ≥ 2 corrections in ≥ 2 distinct sessions, each verified;
//     never into a MUTATING intent (fgMutating — the list lives in code,
//     outside every editable surface; the n-gram list too).
//  4. per-candidate gate (no bundles): one candidate at a time over the
//     fixed split — grammar examples + eval prompts + negatives, keyed by a
//     per-install secret (split.key, HMAC): NonInferior + exact McNemar per
//     intent (p<0.05) + ZERO regression on mutating intents + strict guard
//     on negatives/eval prompts. The correction itself is NEVER a scored
//     example — it cannot vote for itself. The benefit is a directed test:
//     the corrected phrase and its paraphrases must classify right.
//  5. anti-trigger: a word the intent does not own (absent from its
//     examples and rule-concept lexicon) may not carry the decision —
//     masking it must keep the reading, and inserting it into other
//     intents' examples must not flip theirs.
//  6. caps: 1 correction per candidate, ≤10% of the intent's base examples
//     per round, ≤ fgMaxCand candidates and fgDaily promotions a day.
//  7. accepted → the overlay is rewritten atomically and audited
//     (--revert undoes the last batch). Rejected → nothing is written.
//
// Exit: 0 accepted (written, or would be under --dry-run); 1 rejected or
// not enough data; 2 usage.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	fgHeldOut = 0.3
	fgMinSess = 2    // a cluster needs corrections in >= 2 distinct sessions
	fgMaxCand = 8    // gate queries per run (spec item 8: query budget)
	fgDaily   = 10   // promotions per UTC day, all intents (item 3c)
	fgJaccard = 0.7  // near-duplicate corrections cluster (item 9)
	fgPInt    = 0.1  // per-intent cap per round: <= 10% of the intent's base
	fgMcNemar = 0.05 // per-intent non-inferiority: exact McNemar p
)

// fgMutating: intents the forge never promotes to (spec 0.14b blocker 1).
// In code, outside every editable surface (overlay, lexicon, intents.json
// are data — this list is not): moving a reading toward a mutation is a
// grammar change the owner ships, never something this loop writes.
// TestForgeMutatingListCoversRisk keeps it >= every intent with a
// high/critical-risk method.
var fgMutating = map[string]bool{
	"create": true, "delete_file": true, "rename": true, "fix": true, "format": true,
	"install_dep": true, "update_dep": true, "undo": true, "update_jazz": true,
	"git_checkout": true, "git_commit": true, "git_pull": true, "git_push": true,
	"kill_process":    true,
	"service_restart": true, "service_start": true, "service_stop": true,
	"container_stop": true, "container_up": true, "container_prune": true,
	"text_replace": true, "perm_set": true, "sort_op": true, "file_move": true,
}

// fgIsMutating: the overlay loader also asks (nlu_overlay.go) — a
// hand-edited overlay cannot add mutating-intent examples either.
func fgIsMutating(name string) bool { return fgMutating[name] }

type fgAdded struct {
	Text     string   `json:"text"`
	Intent   string   `json:"intent"`
	Count    int      `json:"count"`
	Source   string   `json:"source"`
	HeldOut  bool     `json:"held_out,omitempty"` // unused in etapa B (corrections never score); kept for report compat
	Sessions int      `json:"sessions,omitempty"`
	Words    []string `json:"words,omitempty"` // distinguishing words the anti-trigger passed
}

type fgDropped struct {
	Text   string `json:"text"`
	Intent string `json:"intent,omitempty"`
	Reason string `json:"reason"`
}

type fgReport struct {
	Accept           bool        `json:"accept"`
	Reason           string      `json:"reason"`
	DryRun           bool        `json:"dry_run"`
	Overlay          string      `json:"overlay"`
	Wrote            bool        `json:"wrote"`
	Signals          int         `json:"signals"` // corrections read from the ledger
	Added            []fgAdded   `json:"added"`
	Dropped          []fgDropped `json:"dropped,omitempty"`
	Gate             *GateReport `json:"gate,omitempty"`
	Regressed        []string    `json:"regressed,omitempty"`         // guard cases (eval prompts, negatives) the candidate broke
	GrammarRegressed int         `json:"grammar_regressed,omitempty"` // grammar examples right before, wrong after (within the gate's eps)
	CalibStale       bool        `json:"calib_stale,omitempty"`
}

type fgOpts struct {
	DryRun  bool
	Overlay string // overlay path (nuOverlayPath())
	Seed    int64  // != 0: deterministic split for tests (production: split.key)
}

// cmdForge implements `jazz forge`.
func cmdForge(args []string, w io.Writer) int {
	o, asJSON := fgOpts{Overlay: nuOverlayPath()}, false
	var list, revert bool
	var forget, add []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			o.DryRun = true
		case "--json":
			asJSON = true
		case "--list", "-l":
			list = true
		case "--revert":
			revert = true
		case "--forget":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz forge --forget \"<texto do apelido>\"")
				return 2
			}
			i++
			forget = []string{args[i]}
		case "--add":
			if i+2 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz forge --add \"<texto>\" <intenção>")
				return 2
			}
			add = []string{args[i+1], args[i+2]}
			i += 2
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz forge [--dry-run] [--json] [--list] [--forget T] [--add T I] [--revert]")
			return 2
		}
	}
	switch {
	case forget != nil:
		al, err := nuAliasForget(forget[0])
		if err != nil {
			fmt.Fprintln(w, "jazz forge --forget:", err)
			return 1
		}
		fmt.Fprintf(w, "esquecido: %q → %s\n", al.Text, al.Intent)
		return 0
	case add != nil:
		al, err := nuAliasAdd(add[0], add[1])
		if err != nil {
			fmt.Fprintln(w, "jazz forge --add:", err)
			return 1
		}
		fmt.Fprintf(w, "apelido gravado: %q → %s (instantâneo, só nesta máquina)\n", al.Text, al.Intent)
		if fgIsMutating(al.Intent) {
			fmt.Fprintln(w, "nota: intenção mutante — a execução continua pedindo permissão")
		}
		return 0
	case list:
		fgPrintList(w)
		return 0
	case revert:
		return fgRevert(o, w)
	}
	rep, err := fgRun(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz forge:", err)
		return 1
	}
	if asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(w, string(b))
	} else {
		fgPrint(w, rep)
	}
	if rep.Accept {
		return 0
	}
	return 1
}

// fgPrintList is layer 1 (aliases) + what the gate promoted (overlay).
func fgPrintList(w io.Writer) {
	af, err := nuReadAliases()
	if err != nil {
		fmt.Fprintln(w, "apelidos: arquivo inválido:", err)
	} else {
		fmt.Fprintf(w, "apelidos (%d — instantâneos, só desta máquina; --forget \"texto\" remove):\n", len(af.Aliases))
		var keys []string
		for c := range af.Aliases {
			keys = append(keys, c)
		}
		sort.Strings(keys)
		for _, c := range keys {
			a := af.Aliases[c]
			fmt.Fprintf(w, "  %q → %s (%s)\n", a.Text, a.Intent, time.Unix(a.Ts, 0).UTC().Format("2006-01-02"))
		}
	}
	ov, _ := nuReadOverlay(nuOverlayPath())
	if ov == nil || len(ov.Provenance) == 0 {
		fmt.Fprintln(w, "overlay da forja: vazio")
		return
	}
	fmt.Fprintf(w, "overlay da forja (%d exemplos — --revert desfaz o último lote):\n", len(ov.Provenance))
	per := map[string]int{}
	for _, p := range ov.Provenance {
		per[p.Intent]++
	}
	var names []string
	for n := range per {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %s: %d\n", n, per[n])
	}
}

// fgRevert undoes the most recent promotion batch (one ts = one forge run).
func fgRevert(o fgOpts, w io.Writer) int {
	ov, err := nuReadOverlay(o.Overlay)
	if err != nil || ov == nil || len(ov.Provenance) == 0 {
		fmt.Fprintln(w, "nada a reverter")
		return 1
	}
	var last int64
	for _, p := range ov.Provenance {
		if p.Ts > last {
			last = p.Ts
		}
	}
	var kept []nuOverlayProv
	var removed []nuOverlayProv
	for _, p := range ov.Provenance {
		if p.Ts == last {
			removed = append(removed, p)
		} else {
			kept = append(kept, p)
		}
	}
	for _, p := range removed {
		exs := ov.Examples[p.Intent]
		for i, e := range exs {
			if e == p.Text {
				ov.Examples[p.Intent] = append(exs[:i], exs[i+1:]...)
				break
			}
		}
		if len(ov.Examples[p.Intent]) == 0 {
			delete(ov.Examples, p.Intent)
		}
		fmt.Fprintf(w, "  - %q → %s\n", p.Text, p.Intent)
	}
	ov.Provenance = kept
	if err := fgWriteOverlay(o.Overlay, ov); err != nil {
		fmt.Fprintln(w, "jazz forge --revert:", err)
		return 1
	}
	auditRec("forge.reverted", map[string]any{"path": o.Overlay, "removed": len(removed)})
	fmt.Fprintf(w, "revertido: %d exemplo(s) do lote %s\n", len(removed), time.Unix(last, 0).UTC().Format("2006-01-02 15:04"))
	return 0
}

// fgCand is one labelled canon after collapsing the ledger.
type fgCand struct {
	text     string
	canon    string
	intents  map[string]int
	count    int
	sessions map[string]bool
	firstTs  int64
}

// fgEligible: the provenance bar of etapa B (blockers 2 and 4). Only an
// explicit pick at an interactive human menu counts — position >= 2, the
// top item being the biased default (Joachims 2005) — with outcome=success
// and a session id. Everything else returns the reason it is not evidence.
func fgEligible(s Signal) string {
	switch {
	case s.Extra["how"] != "menu":
		return "proveniência: só pick de menu conta (Enter/confirm/stdin não ensinam)"
	case s.Extra["src"] != "tty":
		return "proveniência: fora de terminal interativo"
	case !s.Verified:
		return "a execução não confirmou sucesso"
	case s.Extra["sess"] == "":
		return "sem sessão registrada"
	}
	if pos, _ := strconv.Atoi(s.Extra["pos"]); pos < 2 {
		return "posição 1 do menu: viés de posição, não é evidência"
	}
	return ""
}

// fgModel compiles a model from scratch: the embedded grammar, the current
// overlay, the extra examples, trained without the held-out keys.
func fgModel(lx, ds []byte, cur *nuOverlay, extra map[string][]string, out map[string]bool) *nuModel {
	m := nuCompile(lx, ds)
	if m.loadErr != nil {
		return m
	}
	nuApplyOverlay(m, cur)
	if len(extra) > 0 {
		nuApplyOverlay(m, &nuOverlay{Version: nuOverlayVersion, Examples: extra})
	}
	m.nb = m.nuTrain(func(ii, ei int) bool {
		return len(out) > 0 && out[m.data.Intents[ii].Name+"\x00"+m.canonOf(m.data.Intents[ii].Examples[ei])]
	})
	m.nbOnce.Do(func() {}) // the held-in table is the classifier
	return m
}

// fgActs: the brain acts on this reading without asking.
func fgActs(in Intent) bool {
	return in.Name != "" && in.Name != "none" && !in.TypoOnly && in.Confidence >= brActMin(in) && in.Understood()
}

// fgVerdict is one example scored by one model.
type fgVerdict struct {
	acts, under, falseSucc bool
}

// fgScore scores a model; verdicts are kept per ID for the strict guard and
// the per-intent McNemar.
func fgScore(m *nuModel, w *nuWorld, keep map[string]fgVerdict) EvalFn {
	return func(exs []Example) Metrics {
		var met Metrics
		acc, cov := 0, 0
		for _, ex := range exs {
			in := m.Parse(ex.Input, w)
			var v fgVerdict
			if forb, ok := strings.CutPrefix(ex.Want, "!"); ok {
				v.falseSucc = in.Understood() && in.Name == forb
			} else {
				met.N++
				v.acts = fgActs(in) && in.Name == ex.Want
				v.under = in.Understood() && in.Name == ex.Want
				v.falseSucc = ex.Group == "eval" && fgActs(in) && in.Name != ex.Want
				if v.acts {
					acc++
				}
				if v.under {
					cov++
				}
			}
			if v.falseSucc {
				met.FalseSuccess++
			}
			keep[ex.ID] = v
		}
		if met.N > 0 {
			met.Accuracy = float64(acc) / float64(met.N)
			met.Coverage = float64(cov) / float64(met.N)
		}
		return met
	}
}

// fgEvalPrompts are the offline eval prompts with one expected intent.
func fgEvalPrompts() []Example {
	var suite struct {
		Tasks []eoTask `json:"tasks"`
	}
	if json.Unmarshal(eoTasksJSON, &suite) != nil {
		return nil
	}
	var out []Example
	for _, t := range suite.Tasks {
		if t.Prompt == "" || t.Expect.Intent == "" || t.Expect.Intent == "multi" {
			continue
		}
		out = append(out, Example{ID: "e:" + t.ID, Input: t.Prompt, Want: t.Expect.Intent, Group: "eval"})
	}
	return out
}

// fgTrigrams: character trigrams of the canon — the near-duplicate test of
// spec item 9 (the NB featurizer's smallest gram), Drain-style.
func fgTrigrams(canon string) map[string]bool {
	s := map[string]bool{}
	for i := 0; i+3 <= len(canon); i++ {
		s[canon[i:i+3]] = true
	}
	if len(canon) > 0 && len(canon) < 3 {
		s[canon] = true
	}
	return s
}

func fgJaccardSim(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// fgCluster groups candidates whose canons are near-duplicates
// (single-linkage over Jaccard ≥ fgJaccard). Returns lists sorted by the
// smallest canon, so the run is deterministic.
func fgCluster(keys []string, cands map[string]*fgCand) [][]*fgCand {
	tri := map[string]map[string]bool{}
	for _, k := range keys {
		tri[k] = fgTrigrams(k)
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for _, k := range keys {
		parent[k] = k
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if fgJaccardSim(tri[keys[i]], tri[keys[j]]) >= fgJaccard {
				a, b := find(keys[i]), find(keys[j])
				if a != b {
					parent[b] = a
				}
			}
		}
	}
	byRoot := map[string][]*fgCand{}
	for _, k := range keys {
		r := find(k)
		byRoot[r] = append(byRoot[r], cands[k])
	}
	var out [][]*fgCand
	var roots []string
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	for _, r := range roots {
		ms := byRoot[r]
		sort.Slice(ms, func(i, j int) bool { return ms[i].canon < ms[j].canon })
		out = append(out, ms)
	}
	return out
}

// fgMedoid: the member closest to the rest (the one the grammar learns);
// ties break on count, then age, then canon — deterministic.
func fgMedoid(members []*fgCand) *fgCand {
	best, bestScore := 0, -1.0
	for i, a := range members {
		score := 0.0
		ta := fgTrigrams(a.canon)
		for j, b := range members {
			if i != j {
				score += fgJaccardSim(ta, fgTrigrams(b.canon))
			}
		}
		if score > bestScore || score == bestScore &&
			(members[i].count > members[best].count ||
				members[i].count == members[best].count && members[i].firstTs < members[best].firstTs) {
			best, bestScore = i, score
		}
	}
	return members[best]
}

// fgSplitKey: the per-install split secret — 32 random bytes at
// dataDir()/forge/split.key (0600), created once. The repo never learns it,
// so a poisoner cannot craft corrections that always land held-in (spec
// item 8). Tests pass fgOpts.Seed instead.
func fgSplitKey(o fgOpts) []byte {
	if o.Seed != 0 {
		h := sha256.Sum256([]byte("forge-seed:" + strconv.FormatInt(o.Seed, 10)))
		return h[:]
	}
	p := filepath.Join(dataDir(), "forge", "split.key")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 16 {
		return b
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return b // crypto failure is inconceivable; a weak key still beats the public seed
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
		if err := os.WriteFile(p, b, 0o600); err == nil {
			return b
		}
	}
	return b // read-only data dir: the split rotates per run — safe side
}

// fgSupportWords: the vocabulary the intent already owns — words of its
// current examples plus the words of the lexicon concepts its rules name
// (spec item 3a: a distinguishing word must be attested here).
func fgSupportWords(m *nuModel, conc map[string][]string, name string, extra map[string][]string) map[string]bool {
	words := map[string]bool{}
	add := func(s string) {
		for _, w := range strings.Fields(m.canonOf(s)) {
			words[w] = true
		}
	}
	if ii, ok := m.byName[name]; ok {
		for _, e := range m.data.Intents[ii].Examples {
			add(e)
		}
		for _, r := range m.data.Intents[ii].Rules {
			for _, c := range r.All {
				for _, w := range conc[c] {
					add(w)
				}
			}
			for _, c := range r.Any {
				for _, w := range conc[c] {
					add(w)
				}
			}
		}
	}
	for _, e := range extra[name] {
		add(e)
	}
	return words
}

// fgAntiTrigger: the distinguishing-word checks (spec item 3a/3b). D = canon
// words of the medoid the intent does not own. (a) masking D must keep the
// candidate's reading — the decision may not rest on new vocabulary; (b)
// inserting D into other intents' examples must not flip their readings.
// Returns D (audit) or a reject reason.
func fgAntiTrigger(medoid, name string, support map[string]bool, seqBaseM, candM *nuModel, w *nuWorld, probe map[string][]string) ([]string, string) {
	var d []string
	for _, wd := range strings.Fields(medoid) {
		if !support[wd] {
			d = append(d, wd)
		}
	}
	if len(d) == 0 {
		return nil, ""
	}
	// (a) mask: without the new words the intent must still win — if the
	// reading collapses, the new words were carrying it (a trigger).
	var kept []string
	for _, wd := range strings.Fields(medoid) {
		if support[wd] {
			kept = append(kept, wd)
		}
	}
	masked := strings.Join(kept, " ")
	if masked == "" {
		return d, "gatilho: o exemplo é só palavra nova, nada da intenção"
	}
	if in := candM.Parse(masked, w); !(in.Name == name && in.Understood()) {
		return d, fmt.Sprintf("gatilho: sem %v lê %s, não %s (a decisão depende de palavra nova)", d, orDash(in.Name), name)
	}
	// (b) insertion: the new words pasted into other intents' examples may
	// not move any reading.
	ins := " " + strings.Join(d, " ")
	for other, exs := range probe {
		if other == name {
			continue
		}
		for _, e := range exs {
			p := e + ins
			if candM.Parse(p, w).Name != seqBaseM.Parse(p, w).Name {
				return d, fmt.Sprintf("gatilho: %v inserido em %q vira %s (era %s)", d, oneLine(e, 40), candM.Parse(p, w).Name, other)
			}
		}
	}
	return d, ""
}

// fgLexicon parses the embedded lexicon's concept -> words map.
func fgLexicon(lx []byte) map[string][]string {
	var def nuLexiconDef
	if json.Unmarshal(lx, &def) != nil {
		return nil
	}
	return def.Concepts
}

// fgClusterCand is one gate candidate: the cluster's medoid plus the
// paraphrases that form its directed test.
type fgClusterCand struct {
	medoid  *fgCand
	members []*fgCand
	name    string
	sess    int
}

// fgRun is the whole loop (cmdForge minus the printing).
func fgRun(o fgOpts) (*fgReport, error) {
	rep := &fgReport{DryRun: o.DryRun, Overlay: o.Overlay, Added: []fgAdded{}}
	lx, _ := nuFS.ReadFile("nlu/lexicon.json")
	ds, _ := nuFS.ReadFile("nlu/intents.json")
	cur, err := nuReadOverlay(o.Overlay)
	if err != nil {
		return nil, fmt.Errorf("overlay %s inválido (%v): conserte ou apague antes de forjar", o.Overlay, err)
	}
	base := fgModel(lx, ds, cur, nil, nil) // what the runtime uses now
	if base.loadErr != nil {
		return nil, base.loadErr
	}
	w := nuWorldFromDef(base.data.World)
	conc := fgLexicon(lx)

	// 1. collect — provenance filter first (nothing else counts).
	sigs, err := sigRead(func(s Signal) bool { return s.Kind == "correction" })
	if err != nil {
		return nil, err
	}
	rep.Signals = len(sigs)
	byCanon := map[string]*fgCand{}
	for _, s := range sigs {
		text, ok := nuExampleText(s.Text)
		if !ok {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: oneLine(s.Text, 80), Intent: s.Chosen, Reason: "vazio, multilinha ou maior que 200 caracteres"})
			continue
		}
		if _, ok := base.byName[s.Chosen]; !ok {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: text, Intent: s.Chosen, Reason: "não é uma intenção da gramática"})
			continue
		}
		if why := fgEligible(s); why != "" {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: text, Intent: s.Chosen, Reason: why})
			continue
		}
		c := base.canonOf(text)
		if c == "" {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: text, Intent: s.Chosen, Reason: "sem palavras"})
			continue
		}
		fc := byCanon[c]
		if fc == nil {
			fc = &fgCand{text: text, canon: c, intents: map[string]int{}, sessions: map[string]bool{}, firstTs: s.Ts}
			byCanon[c] = fc
		}
		fc.intents[s.Chosen]++
		fc.count++
		fc.sessions[s.Extra["sess"]] = true
		if s.Ts < fc.firstTs || fc.firstTs == 0 {
			fc.firstTs = s.Ts
		}
	}
	keys := make([]string, 0, len(byCanon))
	for k := range byCanon {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// readings the owner rejected at menus (menu_skip): canon+intent he said
	// "not this" — a later correction claiming it is a contradiction.
	skipped := map[string]bool{}
	if sks, err := sigRead(func(s Signal) bool { return s.Kind == "menu_skip" }); err == nil {
		for _, s := range sks {
			if c := base.canonOf(s.Text); c != "" && s.Chosen != "" {
				skipped[c+"\x00"+s.Chosen] = true
			}
		}
	}

	// 2. cluster + 3. eligibility
	ix := nuIndexOf(base)
	var clusters [][]*fgCand
	for _, cl := range fgCluster(keys, byCanon) {
		var names []string
		for _, fc := range cl {
			for n := range fc.intents {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		single := names[0]
		for _, n := range names[1:] {
			if n != single {
				single = ""
			}
		}
		if single == "" {
			var ns []string
			seen := map[string]bool{}
			for _, n := range names {
				if !seen[n] {
					seen[n] = true
					ns = append(ns, n)
				}
			}
			for _, fc := range cl {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Reason: "ambíguo: o cluster foi corrigido pra intenções diferentes (" + strings.Join(ns, ", ") + ") — sai da fila"})
			}
			continue
		}
		if fgIsMutating(single) {
			for _, fc := range cl {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: single, Reason: "intenção mutante: nunca auto-promovida (2ª confirmação explícita no mínimo — a gramática não aprende mutação sozinha)"})
			}
			continue
		}
		clusters = append(clusters, cl)
	}

	// member-level drops (need the intent): negative of it, embedded example,
	// base already acting, or impossible to learn. What survives forms the
	// session evidence and the directed test.
	var candList []fgClusterCand
	allExtra := map[string][]string{}
	for _, cl := range clusters {
		name := ""
		for n := range cl[0].intents {
			name = n
		}
		var keep []*fgCand
		for _, fc := range cl {
			drop := func(why string) {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: name, Reason: why})
			}
			switch owner := ix.ex[fc.canon]; {
			case ix.neg[name+"\x00"+fc.canon]:
				drop("conflito: é um negativo de " + name + " (rótulo envenenado)")
			case owner == name:
				drop("já é exemplo da gramática")
			case owner != "":
				drop("conflito: já é exemplo de " + owner)
			case skipped[fc.canon+"\x00"+name]:
				drop("contradição: o dono já rejeitou essa leitura no menu")
			default:
				if in := base.Parse(fc.text, w); fgActs(in) && in.Name == name {
					drop("a gramática atual já age nisso sem perguntar")
					continue
				}
				keep = append(keep, fc)
				allExtra[name] = append(allExtra[name], fc.text)
			}
		}
		if len(keep) > 0 {
			candList = append(candList, fgClusterCand{members: keep, name: name})
		}
	}

	// learnability: a member even the full candidate cannot read is noise —
	// remove it before counting evidence (a rule of another intent decides
	// that text no matter what the classifier learns).
	if len(candList) > 0 {
		full := fgModel(lx, ds, cur, allExtra, nil)
		var kept []fgClusterCand
		for _, c := range candList {
			var ms []*fgCand
			for _, fc := range c.members {
				if in := full.Parse(fc.text, w); !(fgActs(in) && in.Name == c.name) {
					why := "um exemplo não muda a leitura"
					if len(in.Evidence) > 0 {
						why += " (" + in.Evidence[0] + ")"
					}
					rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: c.name, Reason: why})
					continue
				}
				ms = append(ms, fc)
			}
			if len(ms) > 0 {
				c.members = ms
				kept = append(kept, c)
			}
		}
		candList = kept
	}

	// evidence: >= fgMinSess distinct sessions inside the surviving cluster.
	var ready []fgClusterCand
	for _, c := range candList {
		sess := map[string]bool{}
		for _, fc := range c.members {
			for s := range fc.sessions {
				sess[s] = true
			}
		}
		c.sess = len(sess)
		if len(sess) < fgMinSess {
			for _, fc := range c.members {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: c.name, Reason: fmt.Sprintf("evidência de %d sessão(ões) só — precisa de ≥%d distintas", len(sess), fgMinSess)})
			}
			continue
		}
		ready = append(ready, c)
	}
	if len(ready) == 0 {
		rep.Reason = "dados insuficientes: nenhuma correção elegível (menu, terminal, ≥2 sessões, sucesso) sobrou pro portão"
		return rep, nil
	}
	// query budget: strongest evidence first, deterministic.
	sort.Slice(ready, func(i, j int) bool {
		if len(ready[i].members) != len(ready[j].members) {
			return len(ready[i].members) > len(ready[j].members)
		}
		return ready[i].members[0].canon < ready[j].members[0].canon
	})
	if len(ready) > fgMaxCand {
		for _, c := range ready[fgMaxCand:] {
			for _, fc := range c.members {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: c.name, Reason: "orçamento do portão por rodada — fica pra próxima"})
			}
		}
		ready = ready[:fgMaxCand]
	}

	// 4. the fixed scored set — the correction never joins it (it cannot
	// vote for itself): embedded examples + negatives + eval prompts.
	var exs []Example
	keyOf := map[string]string{} // example ID -> training key (intent \x00 canon)
	for _, it := range base.data.Intents {
		for _, e := range it.Examples {
			c := base.canonOf(e)
			id := "g:" + it.Name + ":" + c
			exs = append(exs, Example{ID: id, Input: e, Want: it.Name, Group: "g:" + it.Name})
			keyOf[id] = it.Name + "\x00" + c
		}
		for _, n := range it.Negatives {
			exs = append(exs, Example{ID: "n:" + it.Name + ":" + base.canonOf(n), Input: n, Want: "!" + it.Name, Group: "neg"})
		}
	}
	exs = append(exs, fgEvalPrompts()...)
	inEx, outEx := SplitKeyed(exs, fgHeldOut, fgSplitKey(o))
	outKeys := map[string]bool{}
	for _, ex := range outEx {
		if k, ok := keyOf[ex.ID]; ok {
			outKeys[k] = true
		}
	}
	// the release anchor: the shipped grammar alone — no ratchet, the
	// overlay must never regress what the release shipped (spec item 5).
	anchorM := fgModel(lx, ds, nil, nil, outKeys)
	av := map[string]fgVerdict{}
	anchorIn := fgScore(anchorM, w, av)(inEx)
	anchorOut := fgScore(anchorM, w, av)(outEx)
	// probes for anti-trigger (b): up to 2 examples per other intent.
	probe := map[string][]string{}
	for _, it := range base.data.Intents {
		for i, e := range it.Examples {
			if i >= 2 {
				break
			}
			probe[it.Name] = append(probe[it.Name], e)
		}
	}

	// 5. per-candidate gate — sequential composition: each candidate's
	// baseline is the model with the extras accepted so far.
	seqExtra := map[string][]string{}
	accPerIntent := map[string]int{}
	today := time.Now().UTC().Format("20060102")
	dailyLeft := fgDaily
	if cur != nil {
		for _, p := range cur.Provenance {
			if time.Unix(p.Ts, 0).UTC().Format("20060102") == today {
				dailyLeft--
			}
		}
	}
	for _, c := range ready {
		c.medoid = fgMedoid(c.members)
		if dailyLeft <= 0 {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: c.medoid.text, Intent: c.name, Reason: "limite diário de promoções"})
			continue
		}
		if ii, ok := base.byName[c.name]; ok {
			if cap := int(float64(base.embedN[ii]) * fgPInt); accPerIntent[c.name] >= max(1, cap) {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: c.medoid.text, Intent: c.name, Reason: fmt.Sprintf("teto por intenção nesta rodada (%.0f%% da base)", fgPInt*100)})
				continue
			}
		}
		extra1 := map[string][]string{}
		for k, v := range seqExtra {
			extra1[k] = append([]string{}, v...)
		}
		extra1[c.name] = append(extra1[c.name], c.medoid.text)
		seqBaseM := fgModel(lx, ds, cur, seqExtra, outKeys)
		candM := fgModel(lx, ds, cur, extra1, outKeys)
		bv, cv := map[string]fgVerdict{}, map[string]fgVerdict{}
		bIn := fgScore(seqBaseM, w, bv)(inEx)
		bOut := fgScore(seqBaseM, w, bv)(outEx)
		cIn := fgScore(candM, w, cv)(inEx)
		cOut := fgScore(candM, w, cv)(outEx)
		gr := NonInferior(bIn, bOut, cIn, cOut, len(inEx), len(outEx))
		rep.Gate = &gr
		drop := func(why string) {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: c.medoid.text, Intent: c.name, Reason: why})
		}
		if !gr.Accept {
			drop(gr.Reason)
			continue
		}
		// no ratchet vs the release anchor.
		if ga := NonInferior(anchorIn, anchorOut, cIn, cOut, len(inEx), len(outEx)); !ga.Accept {
			drop("vs release: " + ga.Reason)
			continue
		}
		// per-intent non-inferiority (exact McNemar) + zero regression on
		// mutating intents, on held-out.
		bad := ""
		perIntent := map[string]*[2]int{} // intent -> [b, c] discordant pairs
		for _, ex := range outEx {
			b, cc := bv[ex.ID], cv[ex.ID]
			var want string
			if strings.HasPrefix(ex.Group, "g:") {
				want = strings.TrimPrefix(ex.Group, "g:")
				if b.under == cc.under {
					continue
				}
			} else if ex.Group == "eval" {
				want = ex.Want
				if b.acts == cc.acts {
					continue
				}
			} else {
				continue
			}
			pr := perIntent[want]
			if pr == nil {
				pr = &[2]int{}
				perIntent[want] = pr
			}
			right := func(v fgVerdict) bool {
				if ex.Group == "eval" {
					return v.acts
				}
				return v.under
			}
			if right(b) && !right(cc) {
				pr[0]++
			} else {
				pr[1]++
			}
		}
		for name, pr := range perIntent {
			if pr[0] > 0 && fgIsMutating(name) {
				bad = fmt.Sprintf("regressão em intenção mutante %s: %d exemplo(s) held-out pioraram", name, pr[0])
				break
			}
			if p := McNemarExact(pr[0], pr[1]); p < fgMcNemar {
				bad = fmt.Sprintf("regressão significativa em %s (McNemar p=%.3f, %d↓%d↑)", name, p, pr[0], pr[1])
				break
			}
		}
		if bad != "" {
			drop(bad)
			continue
		}
		// strict guard: a guard case the baseline had right may not move.
		var regressed []string
		for _, ex := range exs {
			b, cc := bv[ex.ID], cv[ex.ID]
			if ex.Group == "neg" && !b.falseSucc && cc.falseSucc || ex.Group == "eval" && b.acts && !cc.acts {
				regressed = append(regressed, ex.ID)
			}
		}
		if len(regressed) > 0 {
			rep.Regressed = append(rep.Regressed, regressed...)
			drop(fmt.Sprintf("%d caso(s) de guarda regrediram (%s)", len(regressed), strings.Join(regressed[:min(3, len(regressed))], ", ")))
			continue
		}
		// anti-trigger.
		support := fgSupportWords(base, conc, c.name, seqExtra)
		words, why := fgAntiTrigger(c.medoid.canon, c.name, support, seqBaseM, candM, w, probe)
		if why != "" {
			drop(why)
			continue
		}
		// directed benefit: the corrected phrase AND its paraphrases all
		// read right on the candidate (the only place corrections count).
		okAll := true
		for _, fc := range c.members {
			if in := candM.Parse(fc.text, w); !(fgActs(in) && in.Name == c.name) {
				rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: c.name, Reason: "teste dirigido: a paráfrase não classifica na candidata"})
				okAll = false
			}
		}
		if !okAll {
			continue
		}
		// accepted.
		seqExtra[c.name] = append(seqExtra[c.name], c.medoid.text)
		accPerIntent[c.name]++
		dailyLeft--
		rep.Added = append(rep.Added, fgAdded{Text: c.medoid.text, Intent: c.name, Count: c.medoid.count, Source: "correction", Sessions: c.sess, Words: words})
	}
	if len(rep.Added) == 0 {
		rep.Reason = "rejeitado: nenhuma correção passou o portão"
		if rep.Gate != nil && rep.Gate.Reason != "" {
			rep.Reason = rep.Gate.Reason
		}
		return rep, nil
	}
	rep.Accept = true
	rep.Reason = fmt.Sprintf("aceito: %d exemplo(s) promovido(s), cada um não-inferior no portão por intenção", len(rep.Added))
	rep.CalibStale = true
	if o.DryRun {
		return rep, nil
	}

	// 6. write
	ov := cur
	if ov == nil {
		ov = &nuOverlay{Version: nuOverlayVersion}
	}
	if ov.Examples == nil {
		ov.Examples = map[string][]string{}
	}
	ov.BaseSHA256 = nuEmbeddedSHA()
	now := time.Now().Unix()
	for _, a := range rep.Added {
		ov.Examples[a.Intent] = append(ov.Examples[a.Intent], a.Text)
		ov.Provenance = append(ov.Provenance, nuOverlayProv{Text: a.Text, Intent: a.Intent, Source: a.Source, Count: a.Count, Ts: now, Sessions: a.Sessions, Words: a.Words})
	}
	if err := fgWriteOverlay(o.Overlay, ov); err != nil {
		return nil, err
	}
	rep.Wrote = true
	auditRec("forge.applied", map[string]any{"path": o.Overlay, "added": len(rep.Added), "reason": rep.Reason})
	return rep, nil
}

// fgWriteOverlay writes the overlay atomically (temp + rename).
func fgWriteOverlay(path string, ov *nuOverlay) error {
	b, err := json.MarshalIndent(ov, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// fgPrint is the human report.
func fgPrint(w io.Writer, r *fgReport) {
	fmt.Fprintf(w, "forja: %d correção(ões) no ledger · %d exemplo(s) novo(s) · %d descartado(s)\n", r.Signals, len(r.Added), len(r.Dropped))
	for _, a := range r.Added {
		fmt.Fprintf(w, "  + %q → %s (×%d, %d sessões)\n", a.Text, a.Intent, a.Count, a.Sessions)
	}
	for _, d := range r.Dropped {
		fmt.Fprintf(w, "  - %q → %s: %s\n", d.Text, orDash(d.Intent), d.Reason)
	}
	fmt.Fprintln(w, "portão:", r.Reason)
	if g := r.Gate; g != nil {
		fmt.Fprintln(w, "              acc    cov  falso    n")
		for _, row := range []struct {
			l string
			m Metrics
		}{{"base held-in ", g.BaseIn}, {"base held-out", g.BaseOut}, {"cand held-in ", g.CandIn}, {"cand held-out", g.CandOut}} {
			fmt.Fprintf(w, "  %s %.3f  %.3f  %5d %4d\n", row.l, row.m.Accuracy, row.m.Coverage, row.m.FalseSuccess, row.m.N)
		}
	}
	if r.GrammarRegressed > 0 {
		fmt.Fprintf(w, "  aviso: %d exemplo(s) da gramática pioraram dentro da tolerância do portão\n", r.GrammarRegressed)
	}
	switch {
	case r.Wrote:
		fmt.Fprintln(w, "gravado:", r.Overlay)
	case r.Accept && r.DryRun:
		fmt.Fprintln(w, "dry-run: nada gravado (seria:", r.Overlay+")")
	default:
		fmt.Fprintln(w, "nada gravado")
	}
	if r.CalibStale {
		fmt.Fprintln(w, "nota: nlu/calib.json foi ajustado à gramática embarcada; com o overlay a confiança calibrada fica aproximada")
	}
}
