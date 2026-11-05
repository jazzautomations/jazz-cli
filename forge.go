package main

// forge.go: `jazz forge [--dry-run] [--json]` — the factory loop of the
// asset-forge (0.14). The owner's corrections (signals.jsonl, kind
// correction: the request and the intent he named) become grammar examples,
// and only what improves OUT OF SAMPLE gets in:
//
//  1. collect: corrections whose Chosen is an existing intent; identical
//     texts collapse (the count is provenance); a text corrected to two
//     different intents is dropped whole.
//  2. filter: empty or > 200 chars, already an example (of that intent:
//     duplicate; of another: conflict), a NEGATIVE of the chosen intent (a
//     poisoned label: the grammar says explicitly it is not that), or a
//     text the current grammar already acts on without asking.
//  3. gate (forge_gate.go): examples = grammar examples + offline eval
//     prompts with their expected intent + negatives + the new labelled
//     examples, split held-in/held-out (0.3, fixed seed). Baseline and
//     candidate are compiled from scratch and trained on the HELD-IN split
//     only; accuracy = acts on the right intent without asking, coverage =
//     understood as the right intent, false success = a negative landing on
//     its forbidden intent or an eval prompt understood as the wrong intent
//     at act confidence (blocking). Then a strict guard: no eval prompt or
//     negative the baseline got right may go wrong.
//  4. accepted → the overlay (nlu_overlay.go) is rewritten atomically
//     (temp + rename) and audited; rejected → nothing is written. --dry-run
//     never writes.
//
// Exit: 0 accepted (written, or would be under --dry-run); 1 rejected or
// not enough data; 2 usage.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	fgHeldOut       = 0.3
	fgSeed    int64 = 0x6a617a7a // fixed: the same ledger always splits the same way
	fgMinNew        = 2          // with fewer, no new example reaches the held-out split
)

type fgAdded struct {
	Text    string `json:"text"`
	Intent  string `json:"intent"`
	Count   int    `json:"count"`
	Source  string `json:"source"`
	HeldOut bool   `json:"held_out"`
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
}

// cmdForge implements `jazz forge [--dry-run] [--json]`.
func cmdForge(args []string, w io.Writer) int {
	o, asJSON := fgOpts{Overlay: nuOverlayPath()}, false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			o.DryRun = true
		case "--json":
			asJSON = true
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz forge [--dry-run] [--json]")
			return 2
		}
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

// fgCand is one labelled text after collapsing the ledger.
type fgCand struct {
	text    string
	canon   string
	intents map[string]int
	count   int
	firstTs int64
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

// fgScore scores a model; verdicts are kept per ID for the strict guard.
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

	// 1. collect
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
		c := base.canonOf(text)
		if c == "" {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: text, Intent: s.Chosen, Reason: "sem palavras"})
			continue
		}
		fc := byCanon[c]
		if fc == nil {
			fc = &fgCand{text: text, canon: c, intents: map[string]int{}, firstTs: s.Ts}
			byCanon[c] = fc
		}
		fc.intents[s.Chosen]++
		fc.count++
	}
	keys := make([]string, 0, len(byCanon))
	for k := range byCanon {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 2. filter
	ix := nuIndexOf(base)
	extra := map[string][]string{}
	var news []*fgAdded
	for _, k := range keys {
		fc := byCanon[k]
		if len(fc.intents) > 1 {
			var ns []string
			for n := range fc.intents {
				ns = append(ns, n)
			}
			sort.Strings(ns)
			rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Reason: "corrigido pra intenções diferentes: " + strings.Join(ns, ", ")})
			continue
		}
		var name string
		for n := range fc.intents {
			name = n
		}
		drop := func(why string) {
			rep.Dropped = append(rep.Dropped, fgDropped{Text: fc.text, Intent: name, Reason: why})
		}
		switch owner := ix.ex[k]; {
		case ix.neg[name+"\x00"+k]:
			drop("conflito: é um negativo de " + name + " (rótulo envenenado)")
			continue
		case owner == name:
			drop("já é exemplo da gramática")
			continue
		case owner != "":
			drop("conflito: já é exemplo de " + owner)
			continue
		}
		if in := base.Parse(fc.text, w); fgActs(in) && in.Name == name {
			drop("a gramática atual já age nisso sem perguntar")
			continue
		}
		extra[name] = append(extra[name], fc.text)
		news = append(news, &fgAdded{Text: fc.text, Intent: name, Count: fc.count, Source: "correction"})
	}
	// learnability: examples only move the classifier, so a reading a rule of
	// another intent decides stays wrong whatever the grammar learns. A text
	// the candidate does not get right even trained on everything is noise
	// for the gate (it only uses the candidate's own texts, never the
	// held-out grammar or the eval prompts).
	if len(news) > 0 {
		full := fgModel(lx, ds, cur, extra, nil)
		kept := news[:0]
		extra = map[string][]string{}
		for _, a := range news {
			if in := full.Parse(a.Text, w); !(fgActs(in) && in.Name == a.Intent) {
				why := "um exemplo não muda a leitura"
				if len(in.Evidence) > 0 {
					why += " (" + in.Evidence[0] + ")"
				}
				rep.Dropped = append(rep.Dropped, fgDropped{Text: a.Text, Intent: a.Intent, Reason: why})
				continue
			}
			kept = append(kept, a)
			extra[a.Intent] = append(extra[a.Intent], a.Text)
		}
		news = kept
	}
	if len(news) < fgMinNew {
		rep.Reason = fmt.Sprintf("dados insuficientes: %d exemplo(s) novo(s), o portão precisa de ≥%d (ao menos um vai pro held-out)", len(news), fgMinNew)
		return rep, nil
	}

	// 3. gate
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
	newIDs := map[string]*fgAdded{}
	for _, a := range news {
		c := base.canonOf(a.Text)
		id := "s:" + a.Intent + ":" + c
		exs = append(exs, Example{ID: id, Input: a.Text, Want: a.Intent, Group: "signal"})
		keyOf[id] = a.Intent + "\x00" + c
		newIDs[id] = a
	}
	_, outEx := Split(exs, fgHeldOut, fgSeed) // the same split Gate computes
	outKeys := map[string]bool{}
	for _, ex := range outEx {
		if k, ok := keyOf[ex.ID]; ok {
			outKeys[k] = true
		}
		if a := newIDs[ex.ID]; a != nil {
			a.HeldOut = true
		}
	}
	baseM := fgModel(lx, ds, cur, nil, outKeys)
	candM := fgModel(lx, ds, cur, extra, outKeys)
	bv, cv := map[string]fgVerdict{}, map[string]fgVerdict{}
	ok, gr := Gate(exs, fgScore(baseM, w, bv), fgScore(candM, w, cv), fgHeldOut, fgSeed)
	rep.Gate = &gr
	rep.Accept, rep.Reason = ok, gr.Reason

	// strict guard: the gate's eps tolerates ~1pp; a guard case may not move
	for _, ex := range exs {
		b, c := bv[ex.ID], cv[ex.ID]
		switch {
		case ex.Group == "neg" && !b.falseSucc && c.falseSucc,
			ex.Group == "eval" && b.acts && !c.acts:
			rep.Regressed = append(rep.Regressed, ex.ID)
		case strings.HasPrefix(ex.Group, "g:") && b.under && !c.under:
			rep.GrammarRegressed++
		}
	}
	if rep.Accept && len(rep.Regressed) > 0 {
		rep.Accept = false
		rep.Reason = fmt.Sprintf("rejeitado: %d caso(s) de guarda regrediram (%s)", len(rep.Regressed), strings.Join(rep.Regressed[:min(3, len(rep.Regressed))], ", "))
	}
	if !rep.Accept {
		return rep, nil
	}
	for _, a := range news {
		rep.Added = append(rep.Added, *a)
	}
	rep.CalibStale = true
	if o.DryRun {
		return rep, nil
	}

	// 4. write
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
		ov.Provenance = append(ov.Provenance, nuOverlayProv{Text: a.Text, Intent: a.Intent, Source: a.Source, Count: a.Count, Ts: now})
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
		ho := ""
		if a.HeldOut {
			ho = " · held-out"
		}
		fmt.Fprintf(w, "  + %q → %s (×%d%s)\n", a.Text, a.Intent, a.Count, ho)
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
