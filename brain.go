package main

// brain.go: the offline brain end to end (docs/design/offline-brain.md §1):
// request → interpreter (nlu.go) → task state (task.go) → methods with
// preconditions and verifiers (methods.go) over registered capabilities
// (caps.go) → evidence → recovery by category (recover.go).
//
// It runs where no model is available: `jazz -p --offline`, the interactive
// offline mode (after the fast paths of intents.go, which keep precedence),
// and `jazz eval offline`. A request the brain does not understand, or has no
// method for, is not answered by guessing: it goes to the model when there
// is one, otherwise jazz says what it can do.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// brResult is the outcome of one request.
type brResult struct {
	Handled   bool // the brain answered (no model needed)
	Miss      bool // the brain did not understand: Text is the honest miss (P2)
	Intent    Intent
	Task      *TaskState
	Status    tkStatus
	Text      string
	Questions int
	Commands  int
	Method    string
	Category  rvCat
	Elapsed   time.Duration
	// the ledger's view (signals_emit.go). brRun never writes the ledger
	// itself — the eval and the tests call it directly — so the outcome
	// carries what the callers emit.
	Req       string     // the request this result answers (a part, under multi)
	MissWhy   string     // not_understood | below_act | rejected | edit
	NoMethod  bool       // understood, but no offline method exists
	Confirmed *Intent    // the reading as it was when the owner confirmed it (P1, before the bump)
	Remember  bool       // [l] at the confirm box: turn text -> intent into a personal alias (alias.go, forge layer 1)
	Sub       []brResult // multi: each part's own outcome
}

// brRun interprets text and, when it understands it and has a method, does it.
func brRun(ctx context.Context, env *brEnv, text string) brResult {
	t0 := time.Now()
	res := brResult{}
	defer func() { res.Elapsed = time.Since(t0) }()
	w := env.World
	if w == nil {
		w = nuRuntimeWorld(env.Root, env.Profile)
	}
	in := nuGet().Parse(text, w)
	res.Intent = in
	// multi-intent: "lista os arquivos e mostra o git status" — accept the
	// split only when EVERY part parses to a CONFIDENT intent (the act
	// threshold, not the bare "understood" floor): a false split ("procura
	// por TODO e FIXME" → "FIXME" alone is a classifier-only 0.75) falls back
	// to the single parse instead of running two half-requests
	if parts := brSplitMulti(text, nuGet(), w); !in.Phrase && len(parts) >= 2 && len(parts) <= 4 {
		var ins []Intent
		chain := make([]bool, len(parts))
		for i, p := range parts {
			if i > 0 && len(ins) == i {
				if q := brResolveThere(p, ins[i-1]); q != p {
					p = q
					parts[i] = q
				}
			}
			if fm, ok := brFastMatch(env.Root, p); ok {
				// the part is a read-only fast path of its own (intents.json)
				ins = append(ins, Intent{Name: "fastpath", Slots: map[string]string{"cmd": fm.Cmd}, Confidence: 1, Raw: 1, Phrase: true,
					Evidence: []string{"atalho " + fm.Intent}})
				continue
			}
			pi := nuGet().Parse(p, w)
			// anaphora clause: "e apaga eles" can parse CONFIDENT now that
			// filter slots eat the pronoun (name=eles) — check the strip
			// before trusting the confident read, so chaining still wins.
			if stripped, ana := brStripAnaphora(p); ana && i > 0 {
				if si := nuGet().Parse(stripped, w); si.Understood() && si.Confidence >= brLevel0Min {
					if _, ok := brChainConsumers[si.Name]; ok {
						ins = append(ins, si)
						chain[i] = true
						continue
					}
				}
			}
			if !pi.Understood() || pi.Name == "none" || pi.Confidence < brLevel0Min {
				ins = nil
				break
			}
			ins = append(ins, pi)
		}
		if len(ins) == len(parts) {
			return brRunMulti(ctx, env, parts, ins, chain)
		}
	}
	return brRunIntent(ctx, env, text, in, res)
}

// brSplitMulti splits a request on conjunctions; callers must validate that
// each part still parses — the split is a candidate, not a commitment.
// Parts with no task evidence ("porra e o zz9?" → "porra") are noise, not a
// request: they are discarded here so they cannot veto the split or become
// an action of their own.
var brMultiRe = regexp.MustCompile(`\s*(?:;|,\s*e\s+|\s+e\s+depois\s+|\s+depois\s+|\s+e\s+|,\s*and\s+|\s+and\s+then\s+|\s+then\s+|\s+and\s+)`)

// brRangeLeft: a conjunction after "between N" / "entre N" is the second
// arm of a range, never a split point ("entre 2 e 10 minutos").
var brRangeLeft = regexp.MustCompile(`(?i)\b(between|entre)\s+\S+\s*$`)

// brSaveTail: "…and print/save the list to 'f'" — a cauda de persistência
// não é task própria: sozinha ela re-listaria TUDO (falso sucesso). O corte
// é pulado pra frase inteira parsear produtor+out de uma vez.
var brSaveTailRe = regexp.MustCompile(`(?i)^\s*(print|save|output|write|writes|store|stores|tee|export|exporta|dump|dumps|put|grava|grave|gravar|salva|salve|salvar|escreve|escrever|imprime|imprimir|mostra|mostrar|liste?|lista)\b[^.;]{0,60}\b(list|results?|output|paths?|matches|files|lista|resultados?|arquivos)\b[^.;]{0,40}\b(to|into|para|pra|pro|em|no|na|dentro)\b`)

// brPossTail: "and their group"/"e seus arquivos" — a possessive right
// after the conjunction continues the LEFT noun-phrase (owner and their
// group), never starts a new clause.
var brPossTail = regexp.MustCompile(`(?i)^\s*(their|its|his|her|seu|sua|seus|suas|dele|dela|deles|delas)\b`)

// brMaskQuotes blanks quoted spans ('...', "...", `...`) so a conjunction
// inside a file name ("rock and roll.txt") never becomes a split point.
// The mask preserves offsets — splitting the mask cuts the original right.
func brMaskQuotes(text string) string {
	b := []byte(text)
	var q byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case q == 0 && (c == '\'' || c == '"' || c == '`'):
			q = c
		case q != 0 && c == q:
			q = 0
		case q != 0:
			b[i] = ' '
		}
	}
	return string(b)
}

func brSplitMulti(text string, m *nuModel, w *nuWorld) []string {
	var out []string
	masked := brMaskQuotes(text)
	var cuts [][2]int
	for _, loc := range brMultiRe.FindAllStringIndex(masked, -1) {
		if brRangeLeft.MatchString(text[:loc[0]]) {
			continue // "entre 2 e 10" — o e pertence à faixa
		}
		if brSaveTailRe.MatchString(text[loc[1]:]) {
			continue // "…e salva a lista em f" — cauda de persistência do produtor
		}
		if brPossTail.MatchString(text[loc[1]:]) {
			continue // "and their group" — possessivo continua a oração
		}
		cuts = append(cuts, [2]int{loc[0], loc[1]})
		if len(cuts) >= 4 {
			break
		}
	}
	if len(cuts) == 0 {
		return nil
	}
	prev := 0
	for _, c := range cuts {
		if p := strings.TrimSpace(text[prev:c[0]]); p != "" && m.taskEvidence(p, w) {
			out = append(out, p)
		}
		prev = c[1]
	}
	if p := strings.TrimSpace(text[prev:]); p != "" && m.taskEvidence(p, w) {
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// brMissedBeyond reports whether required slots are still missing besides
// the one the chain itself fills per produced path.
func brMissedBeyond(missing []string, chainSlot string) bool {
	for _, m := range missing {
		if m != chainSlot {
			return true
		}
	}
	return false
}

// brChainConsumers: intents that may consume a producer's file list through
// an anaphora ("apaga eles" → one run per produced path). Value = the slot
// the produced path fills. Allowlist on purpose: only read/verify-shaped or
// explicitly-gated ops chain; rename/run/commit never do.
var brChainConsumers = map[string]string{
	"delete_file": "target",
	// file_move encadeia: "acha X e move eles pra DIR" — o destino vem do
	// re-parse do passo (required), o alvo é cada path produzido
	"file_move": "target",
}

// brProducerIntents: intents whose verified output carries `path=` lines the
// chain can consume.
var brProducerIntents = map[string]bool{
	"file_find": true, "perm_find": true, "fs_recent": true,
}

var brAnaphoraRe = regexp.MustCompile(`(?i)\b(eles|elas|deles|delas|neles|nelas|them|those|these)\b`)

// brStripAnaphora removes standalone anaphora tokens; (text, true) means a
// pronoun was there and the rest is a re-parseable clause.
func brStripAnaphora(text string) (string, bool) {
	if !brAnaphoraRe.MatchString(text) {
		return text, false
	}
	return strings.Join(strings.Fields(brAnaphoraRe.ReplaceAllString(text, " ")), " "), true
}

// brHarvestPaths pulls bounded, root-safe paths out of a producer's
// evidence. nil on truncation — a partial set must never chain.
func brHarvestPaths(r brResult) []string {
	var out []string
	truncated := false
	src := r.Text
	if r.Task != nil {
		for _, ev := range r.Task.Evidence {
			src += "\n" + ev.Text
		}
	}
	for _, ln := range strings.Split(src, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "truncated: true") {
			truncated = true
		}
		if strings.HasPrefix(ln, "path=") {
			p := strings.TrimSpace(strings.TrimPrefix(ln, "path="))
			if sp := strings.IndexByte(p, ' '); sp >= 0 {
				p = p[:sp]
			}
			if p != "" && p != "." && !strings.Contains(p, "..") &&
				!strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
				out = append(out, p)
			}
		}
		if len(out) >= 32 {
			truncated = true
		}
	}
	if truncated {
		return nil
	}
	return out
}

// brRunMulti runs each understood part in sequence and merges the results;
// the worst status wins so a denied part never hides behind a green one.
// chain[i] marks anaphora consumers fed by the last successful producer.
func brRunMulti(ctx context.Context, env *brEnv, parts []string, ins []Intent, chain []bool) brResult {
	res := brResult{Handled: true, Intent: Intent{Name: "multi", Confidence: 1}}
	var texts []string
	var produced []string
	missed := false
	for i, in := range ins {
		var r brResult
		if i < len(chain) && chain[i] {
			slot := brChainConsumers[in.Name]
			switch {
			case len(produced) == 0:
				r = brResult{Handled: true, Intent: in, Status: tkFailed,
					Text: "nada pra " + in.Name + " — o passo anterior não produziu arquivos"}
			case brMissedBeyond(in.Missing, slot):
				// re-parse do passo deixou slot required vazio ALÉM do que o
				// chain preenche ("move eles pra outra pasta" falta dest) —
				// pergunta UMA vez em vez de bloquear cada item do fan-out.
				// O próprio slot da chain não conta: "apaga eles" tem target
				// vazio justamente porque a anáfora É o alvo produzido.
				r = brResult{Handled: true, Intent: in, Status: tkNeedsInput,
					Miss: true,
					Text: in.Name + " precisa de: " + strings.Join(in.Missing, ", ")}
			default:
				r = brRunEach(ctx, env, parts[i], in, slot, produced)
			}
		} else {
			r = brRunIntent(ctx, env, parts[i], in, brResult{})
		}
		if r.Task == nil && (r.Miss || !r.Handled) {
			// um passo que termina em pergunta/menu sai de brRunIntent ANTES
			// de newTask — sem evento terminal, o último task da sessão fica
			// sendo o veredito do passo ANTERIOR e quem lê o stream herda um
			// sucesso que o run nunca teve (falso sucesso de trabalho parcial)
			mt := newTask(parts[i], in.Name, in.Slots, env.Sink)
			mt.must(tkInvestigating, "interpretado")
			mt.must(tkNeedsInput, oneLine(r.Text, 200))
		}
		if brProducerIntents[in.Name] && r.Status == tkSucceeded {
			produced = brHarvestPaths(r)
		} else if r.Status != tkSucceeded {
			produced = nil // a failed producer feeds nothing downstream
		}
		res.Sub = append(res.Sub, r)
		if !r.Handled && !missed { // the menu of a failed part needs its reading
			res.Intent, res.Miss, missed = r.Intent, r.Miss, true
		}
		res.Handled = res.Handled && r.Handled
		res.Questions += r.Questions
		res.Commands += r.Commands
		if brStatusRank(r.Status) > brStatusRank(res.Status) {
			res.Status = r.Status
		}
		t := r.Text
		if r.Task != nil && r.Task.Status == tkNeedsInput {
			t += " (precisa de entrada)"
		}
		texts = append(texts, "» "+strings.TrimSpace(parts[i])+"\n"+t)
		if r.Status == tkFailed && brRestAreReads(env, ins[i+1:], chain[i+1:]) {
			// "roda os testes e mostra o git status": o teste falhar é um
			// resultado, e o resto são leituras independentes — mostrar. O
			// veredito do run continua sendo o pior passo (failed). Nada que
			// muda coisa roda depois de uma falha.
			continue
		}
		if r.Status == tkNeedsInput || r.Status == tkFailed || r.Status == tkBlocked {
			// um passo que pergunta/falha/bloqueia encerra a cadeia —
			// continuar executando passos seguintes e selar com o veredito
			// DELES é o falso sucesso do multi ("Apagar qual arquivo?" →
			// needs_input no passo 1, list_files verde no passo 2)
			if i+1 < len(ins) {
				texts = append(texts, fmt.Sprintf("(parou no passo %d de %d: %s)", i+1, len(ins), r.Status))
			}
			break
		}
	}
	// rollup: o evento-pai carrega o PIOR status da cadeia — quem lê o
	// stream pega o veredito do run, não do último passo que emitiu.
	pt := newTask(strings.Join(parts, " && "), "multi", nil, env.Sink)
	pt.must(tkInvestigating, "cadeia de "+strconv.Itoa(len(ins))+" passos")
	switch res.Status {
	case tkSucceeded:
		pt.AddEvidence("plan", fmt.Sprintf("%d/%d passos verificados", len(res.Sub), len(ins)))
		pt.must(tkPlanning, "planejou")
		pt.must(tkReady, "pronto")
		pt.must(tkRunning, "executou")
		pt.must(tkVerifying, "conferindo")
		pt.must(tkSucceeded, "todos os passos verificados")
	case tkNeedsInput:
		pt.must(tkNeedsInput, oneLine(res.Text, 200))
	case tkBlocked:
		pt.must(tkBlocked, oneLine(res.Text, 200))
	case tkUnverified:
		pt.must(tkPlanning, "planejou")
		pt.must(tkReady, "pronto")
		pt.must(tkRunning, "executou")
		pt.must(tkVerifying, "conferindo")
		pt.must(tkUnverified, oneLine(res.Text, 200))
	default:
		pt.must(tkPlanning, "planejou")
		pt.must(tkReady, "pronto")
		pt.must(tkRunning, "executou")
		pt.must(tkFailed, oneLine(res.Text, 200))
	}
	res.Text = strings.Join(texts, "\n\n")
	return res
}

// brRestAreReads: every remaining step is an independent low-risk read (no
// chained anaphora, no mutation).
func brRestAreReads(env *brEnv, ins []Intent, chain []bool) bool {
	if len(ins) == 0 {
		return false
	}
	for i, in := range ins {
		if i < len(chain) && chain[i] || brIntentRisk(env, in) != cbLow {
			return false
		}
	}
	return true
}

// brRunEach fans a consumer intent out over produced paths — one real run
// per file, each through the normal gates (permission, confinement, verify).
// The worst status wins so one denied delete never hides behind green ones.
func brRunEach(ctx context.Context, env *brEnv, text string, in Intent, slot string, paths []string) brResult {
	res := brResult{Handled: true, Intent: in, Status: tkSucceeded}
	var texts []string
	for _, p := range paths {
		ci := in
		slots := make(map[string]string, len(in.Slots)+1)
		for k, v := range in.Slots {
			slots[k] = v
		}
		slots[slot] = p
		ci.Slots = slots
		ci.Missing = nil
		r := brRunIntent(ctx, env, text+" ["+p+"]", ci, brResult{})
		res.Sub = append(res.Sub, r)
		res.Handled = res.Handled && r.Handled
		res.Questions += r.Questions
		res.Commands += r.Commands
		if brStatusRank(r.Status) > brStatusRank(res.Status) {
			res.Status = r.Status
		}
		texts = append(texts, "» "+p+"\n"+r.Text)
	}
	res.Text = strings.Join(texts, "\n")
	return res
}

func brStatusRank(s tkStatus) int {
	switch s {
	case tkFailed:
		return 5
	case tkNeedsInput:
		return 4
	case tkBlocked:
		return 3
	case tkUnverified:
		return 2
	case tkSucceeded:
		return 1
	}
	return 0
}

func brRunIntent(ctx context.Context, env *brEnv, text string, in Intent, res brResult) brResult {
	res.Intent = in
	res.Req = text
	// pure small talk is decided before the grammar: "valeu" must never
	// read as a config key nor run a command (brain_smalltalk.go)
	if reply, ok := brSmallTalk(text); ok {
		res.Handled, res.Status, res.Text = true, tkSucceeded, reply
		res.Intent = Intent{Name: "smalltalk", Confidence: 1}
		return res
	}
	// typo-only reading (nlu.go): the intent stands on a word we repaired
	// and nothing else. Principle 1 (faz até o fim) says do not hand it
	// back as homework; principle 2 (não enfraquece guardrail) says do not
	// act on a spelling guess silently. So it enters the same one-key
	// confirmation as the medium confidence band below; headless
	// (-p, env.Ask == nil) falls into the honest miss with the top-k menu.
	if !in.Understood() && !(in.TypoOnly && in.Name != "none") {
		res.Miss, res.MissWhy = true, "not_understood"
		res.Text = brNotUnderstood(in)
		if why, ok := brNeedsModel(text); ok { // creative work: say why, what works offline, how to get a model
			res.MissWhy, res.Text = "needs_model", why
		}
		return res
	}
	lib, _ := mhLibrary()
	methods := lib[in.Name]
	if len(methods) == 0 {
		res.NoMethod = true
		res.Text = fmt.Sprintf("entendi '%s' (%s), mas ainda não tenho um método offline pra isso", in.Name, nuGet().descOf(in.Name))
		return res
	}
	// P1: the double band — confidence × risk of the method brExecute would
	// pick. High confidence on a read-only method acts at once (anti-fatigue,
	// UX-VII); the medium band and every mutant (risk ≥ high) get one key.
	// Headless without a TTY never acts on the medium band: it becomes the
	// honest miss. Mutation stays behind the permission guardrail either way
	// (env.Permit): this confirmation is of the *intention*, not a replacement.
	risk := brIntentRisk(env, in)
	// the seal covers the VERB too (brain_honest.go): a question never
	// triggers a mutation, a write request is never answered by its read
	// variant, model work never becomes an empty `create`
	if why, kind := brVerbGuard(env, text, in, risk); why != "" {
		res.Miss, res.MissWhy, res.Text = true, kind, why
		return res
	}
	if env.Ask == nil {
		// the same act threshold as the interactive confirm and level 0
		// (brActMin): a read-only 0.75 runs online and in the session, so it
		// must not end as an honest miss under `jazz -p --offline`
		if !in.Understood() || in.Confidence < brActMin(in) {
			res.Miss, res.MissWhy = true, "below_act"
			res.Text = brNotUnderstood(in)
			return res
		}
	} else if brNeedsConfirm(in, risk) {
		res.Questions++
		switch ans := env.Ask(brConfirmQuestion(text, in), brConfirmOptions(env, in, risk)); {
		case ans == 0 || ans == 3: // Enter/s (or [l]: do it AND remember the pair as a personal alias)
			orig := in // the ledger keeps the reading as it was (a labelled example)
			res.Confirmed = &orig
			res.Remember = ans == 3
			in.TypoOnly = false
			if in.Confidence < nuMinConf {
				in.Confidence = nuMinConf
			}
			res.Intent = in
		case ans == 1: // e: edit the request — show the reading jazz would send
			res.MissWhy = "edit"
			res.Text = "edite o pedido — sugestão: \"" + brSuggestText(text, in) + "\""
			return res
		default: // n/Esc: not this — the alternatives menu (P2)
			res.Miss, res.MissWhy = true, "rejected"
			res.Text = brNotUnderstood(in)
			return res
		}
	}
	res.Handled = true
	task := newTask(text, in.Name, in.Slots, env.Sink)
	res.Task = task
	cx := &brCtx{ctx: ctx, env: env, task: task, in: in, vars: map[string]any{}, attempts: map[rvCat]int{}, explore: newRvExplore(0)}
	p := env.Profile
	if p == nil {
		p = epGet(false)
		env.Profile = p
	}
	tkFactsFromEnv(task, p)
	task.must(tkInvestigating, "interpretado: "+strings.Join(in.Evidence, "; "))
	cx.proj = fpDetect(env.Root)
	cx.facts = brFacts(cx, p)
	if pm := brPlanChain(cx, in, methods); pm != nil {
		// a composed chain (e.g. file_find → take → delete) beats the
		// ask variant when it can derive the missing slot itself
		methods = append([]mhMethod{*pm}, methods...)
	}
	brExecute(cx, methods)
	res.Status = task.Status
	res.Text = strings.TrimRight(cx.out.String(), "\n")
	if cx.stopWhy != "" && !strings.Contains(res.Text, cx.stopWhy) {
		if res.Text != "" {
			res.Text += "\n"
		}
		res.Text += cx.stopWhy
	}
	res.Questions += cx.questions
	res.Commands = cx.commands
	res.Category = cx.cat
	for _, d := range task.Decisions {
		if f := strings.Fields(d.What); len(f) == 2 && f[0] == "método" {
			res.Method = f[1] // the last method tried
		}
	}
	// Alexandria: um procedimento do acervo que rodou conta o uso — sucesso
	// verificado soma, falha objetiva desconta (axUse no-ops fora do acervo
	// e fora do portão nuForgeOn).
	switch task.Status {
	case tkSucceeded:
		axUse(res.Method, true)
	case tkFailed:
		axUse(res.Method, false)
	}
	if res.Remember && res.Status == tkSucceeded && nuForgeOn() {
		// [l] — layer 1 of the forge: the pair text→intent becomes a personal
		// alias (instant, this machine only); mutating targets still ask for
		// permission at execution.
		if al, err := nuAliasAdd(text, res.Intent.Name); err == nil {
			res.Text += fmt.Sprintf("\n(apelido gravado: %q → %s — 'jazz forge --forget' desfaz)", al.Text, al.Intent)
		}
	}
	return res
}

// descOf is the dataset's description of an intent.
func (m *nuModel) descOf(name string) string {
	if i, ok := m.byName[name]; ok {
		return m.data.Intents[i].Desc
	}
	return name
}

// exampleOf is the dataset's first example for an intent — the natural way
// to ask for it ("roda os testes"), used by the miss menu and the edit hint.
func (m *nuModel) exampleOf(name string) string {
	if i, ok := m.byName[name]; ok && len(m.data.Intents[i].Examples) > 0 {
		return m.data.Intents[i].Examples[0]
	}
	return ""
}

// brTypoConfirm is the one-key prompt for a typo-only reading: it names the
// repair and what the intent would do, e.g. "li 'atualzia' como 'atualiza' —
// atualizar o jazz?". It reuses the typo evidence nlu.go already recorded.
func brTypoConfirm(in Intent) string {
	var fixes []string
	for _, e := range in.Evidence {
		if s := strings.TrimSuffix(e, " (erro de digitação)"); s != e {
			fixes = append(fixes, strings.Replace(s, " lido como ", " como ", 1))
		}
	}
	desc := nuGet().descOf(in.Name)
	if len(fixes) > 0 {
		return fmt.Sprintf("li %s — %s?", strings.Join(fixes, ", "), desc)
	}
	return desc + "?"
}

// ---------- P1: intention confirmation (confidence × method risk) ----------

// brNeedsConfirm is the P1 matrix: high confidence on a read-only method is
// the only band that acts without asking. Mutants (risk ≥ high) always ask;
// the medium confidence band (nuMinConf..brLevel0Min) asks too, and so does
// any typo-only reading. Anti-fatigue (UX-VII): the confident read-only case
// never pays a key.
func brNeedsConfirm(in Intent, risk cbRisk) bool {
	// Confirm the READING only when it is uncertain: a typo-only repair or a
	// confidence below the act threshold. A mutating intent understood with
	// high confidence does NOT get an extra "entendi você?" prompt — the
	// mutation consent is the permission guardrail at execution (env.Permit).
	// Asking both is the cried-confirm that kills the offline fast path and
	// over-asks on blocked/impossible tasks (eval regression 04/11).
	_ = risk
	return in.TypoOnly || in.Confidence < brActMin(in)
}

// brIntentRisk is the risk of the method brExecute would pick first: the
// first applicable one, in library order (methods/*.json "risk").
func brIntentRisk(env *brEnv, in Intent) cbRisk {
	lib, _ := mhLibrary()
	methods := lib[in.Name]
	if len(methods) == 0 {
		return cbLow
	}
	p := env.Profile
	if p == nil {
		p = epGet(false)
	}
	probe := &brCtx{env: env, in: in, task: &TaskState{Slots: in.Slots, Facts: map[string]string{}}}
	probe.proj = fpDetect(env.Root)
	facts := brFacts(probe, p)
	for _, m := range methods {
		if mhApplicable(m, facts) {
			return cbParseRisk(m.Risk)
		}
	}
	return cbLow
}

// brRiskWord names the risk the way the confirmation box shows it.
func brRiskWord(r cbRisk) string {
	switch r {
	case cbCritical, cbHigh:
		return "risco alto"
	case cbMed:
		return "risco médio"
	}
	return "só leitura"
}

// brConfirmQuestion is the "entendi X como Y" line of the P1 box (UX-III:
// one key, never a typed word).
func brConfirmQuestion(text string, in Intent) string {
	what := brTypoConfirm(in)
	if in.TypoOnly {
		what = strings.TrimSuffix(what, "?")
	} else {
		what = fmt.Sprintf("entendi '%s' como '%s'", oneLine(text, 60), nuGet().descOf(in.Name))
	}
	return what + " — [enter] faz · [e] edita · [n] não"
}

// brConfirmOptions are the ways out of the P1 box: [Enter/s] does,
// [e] edits the request, [n/Esc] shows the alternatives (P2), and [l] does
// AND remembers — "lembrar isso" turns the pair text→intent into a personal
// alias (forge layer 1, alias.go: instant, this machine only). "Mandar pro
// modelo" does not exist offline (there is no model to send it to). No [l]
// on a typo-only reading: remembering a typo is cementing noise.
func brConfirmOptions(env *brEnv, in Intent, risk cbRisk) []string {
	hint := brRiskWord(risk)
	if env.BeginTurn != nil {
		hint += " · coberto por /undo" // shadow-git makes the turn reversible (UX-VI)
	}
	opts := []string{
		brAskOpt("Sim, "+nuGet().descOf(in.Name), "sy", hint),
		brAskOpt("Editar o pedido", "e", ""),
		brAskOpt("Não — ver outras opções", "n", ""),
	}
	if in.Name != "" && in.Name != "none" && !in.TypoOnly {
		opts = append(opts, brAskOpt("Sim, e lembrar que é isso", "l", "apelido instantâneo, só nesta máquina"))
	}
	return opts
}

// brAskKeySep separates an env.Ask option label from its one-key shortcuts
// and its dim hint ("Sim, apagar\x1fsy\x1frisco alto"). Options that carry it
// are a complete menu: the App wrapper does not append "Nenhum desses".
const brAskKeySep = "\x1f"

func brAskOpt(label, keys, hint string) string {
	return label + brAskKeySep + keys + brAskKeySep + hint
}

// brAskLabel is the visible label of an env.Ask option (drops the key spec).
func brAskLabel(o string) string {
	if i := strings.IndexByte(o, brAskKeySep[0]); i >= 0 {
		return o[:i]
	}
	return o
}

// brAskItems turns env.Ask options into menu items (label, keys, hint).
// complete = the caller brought its own exit options.
func brAskItems(opts []string) (items []mnItem, complete bool) {
	items = make([]mnItem, 0, len(opts))
	for _, o := range opts {
		f := strings.Split(o, brAskKeySep)
		it := mnItem{Label: f[0]}
		if len(f) > 1 {
			complete = true
			if f[1] != "" {
				it.Key = rune(f[1][0])
				it.Keys = f[1][1:]
			}
		}
		if len(f) > 2 {
			it.Hint = f[2]
		}
		items = append(items, it)
	}
	return items, complete
}

// brSuggestText is the reading jazz would send if the user edited the
// request: the repaired canonical text when there was a repair, else the
// dataset's own example for the intent.
func brSuggestText(text string, in Intent) string {
	m := nuGet()
	canon := nuCanonText(m.normalize(nuTokenize(text)))
	if canon != "" && canon != strings.ToLower(strings.TrimSpace(text)) {
		return canon
	}
	if ex := m.exampleOf(in.Name); ex != "" {
		return ex
	}
	return text
}

// ---------- P2: the honest miss is a menu ----------

// brMissGuesses are the readings worth offering on a miss: the hypothesis
// (when there is one) followed by the interpreter's runner-ups (in.Alt, which
// nlu.go already computes and nothing used to read).
func brMissGuesses(in Intent) []Intent {
	var out []Intent
	add := func(name string, conf float64) {
		if name == "" || name == "none" {
			return
		}
		for _, g := range out {
			if g.Name == name {
				return
			}
		}
		out = append(out, Intent{Name: name, Slots: in.Slots, Confidence: conf})
	}
	if in.Name != "none" && in.Confidence > 0 {
		add(in.Name, in.Confidence)
	}
	for _, a := range in.Alt {
		if len(out) >= 3 {
			break
		}
		add(a, 0)
	}
	return out
}

// brSafeGuesses are the requests jazz always knows how to answer: they fill
// the miss menu when the interpreter has no runner-up at all.
var brSafeGuessesList = []string{"open_file", "explain", "find", "run_tests", "project_summary"}

func brSafeGuesses() []Intent {
	out := make([]Intent, 0, len(brSafeGuessesList))
	for _, n := range brSafeGuessesList {
		out = append(out, Intent{Name: n, Confidence: nuMinConf})
	}
	return out
}

// brGuessLine is one line of the miss menu: what the intent does and how to
// ask for it in words.
func brGuessLine(g Intent) string {
	m := nuGet()
	desc := m.descOf(g.Name)
	if ex := m.exampleOf(g.Name); ex != "" {
		return desc + " — \"" + ex + "\""
	}
	return desc
}

// brMissLines is the stable numbered list of the miss menu: the same request
// always prints the same list (headless -p shows exactly this).
func brMissLines(in Intent) []string {
	guesses := brMissGuesses(in)
	var out []string
	for i, g := range guesses {
		l := brGuessLine(g)
		if i == 0 && g.Name == in.Name && in.Confidence > 0 {
			l += fmt.Sprintf(" (confiança %.2f)", in.Confidence)
		}
		out = append(out, l)
	}
	for _, g := range brSafeGuesses() {
		if len(out) >= 3 {
			break
		}
		dup := false
		for _, x := range guesses {
			dup = dup || x.Name == g.Name
		}
		if !dup {
			out = append(out, brGuessLine(g))
		}
	}
	return append(out, "digitar de novo (reformule o pedido)")
}

// brNotUnderstood is the honest miss, now a menu (P2) instead of the dead
// paragraph: what jazz thinks the request is, the runner-ups the interpreter
// already computed and, failing those, the safe requests it always knows.
func brNotUnderstood(in Intent) string {
	var b strings.Builder
	if in.Name != "none" && in.Confidence > 0 {
		fmt.Fprintf(&b, "não tenho certeza do pedido (talvez %s, confiança %.2f)", in.Name, in.Confidence)
	} else {
		b.WriteString("não entendi o pedido sem o modelo")
	}
	b.WriteString(". você quis:\n")
	for i, l := range brMissLines(in) {
		fmt.Fprintf(&b, "  %d %s\n", i+1, l)
	}
	return strings.TrimRight(b.String(), "\n")
}

// brOfferIntent maps a contextual menu action to the intent it stands for,
// so the miss menu never offers the same reading twice.
var brOfferIntent = map[string]string{
	"read": "open_file", "error_location": "explain", "search": "find", "test": "run_tests",
	"build": "build", "git_status": "git_status", "log": "git_log", "summary": "project_summary",
	"manual": "help", "sudo": "sudo",
}

// brMissActions composes the interactive P2 menu: the readings (hypothesis +
// in.Alt), the contextual actions of olMenu, "digitar de novo" and /online.
// When the hypothesis is unsure and mutating, the safe actions come first —
// item 1 is the safest, never the most likely (UX-II/UX-VII). RotSeed
// rotates positions 2..k of the readings per session (spec 0.14b item 6):
// the forge reads position as evidence, so rank may not hide in it.
func brMissActions(in Intent, mi olMenuIn) []olAction {
	text := mi.Text
	guesses := brMissGuesses(in)
	seen := map[string]bool{}
	var reads []olAction
	for i, g := range guesses {
		forced := g
		forced.Confidence = brLevel0Min // the pick *is* the confirmation of the reading
		forced.TypoOnly = false
		label := "acho que é: " + brGuessLine(g)
		if i == 0 && g.Name == in.Name && in.Confidence > 0 {
			label = fmt.Sprintf("acho que é: %s (confiança %.2f)", brGuessLine(g), in.Confidence)
		}
		orig := in // what the brain read before the owner picked (signals_emit.go)
		reads = append(reads, olAction{Key: g.Name, Label: label, Kind: "brain", Intent: g.Name, In: &forced, Orig: &orig, Text: text})
		seen[g.Name] = true
	}
	if len(reads) > 2 && mi.RotSeed != "" {
		if rot := int(splitHash(0, mi.RotSeed+"\x00"+text) % uint64(len(reads)-1)); rot > 0 {
			tail := append([]olAction{}, reads[1+rot:]...)
			tail = append(tail, reads[1:1+rot]...)
			reads = append(reads[:1], tail...)
		}
	}
	var safe []olAction
	for _, act := range olMenu(mi) {
		if act.Kind == "online" || act.Kind == "wait" || seen[brOfferIntent[act.Intent]] {
			continue
		}
		safe = append(safe, act)
		if len(safe) >= 2 {
			break
		}
	}
	// An unsure guess never takes item 1: below nuMinConf the safe contextual
	// actions come first (UX-II/UX-VII — item 1 is the safest, not the most
	// likely), and a mutating guess is never one key away from a low-confidence
	// reading.
	unsure := len(guesses) > 0 && in.Confidence < nuMinConf
	items := make([]olAction, 0, olMenuMax+1)
	if unsure {
		items = append(items, safe...)
		items = append(items, reads...)
	} else {
		items = append(items, reads...)
		items = append(items, safe...)
	}
	edit := olAction{Key: "editar", Label: "digitar de novo (editar o pedido)", Kind: "edit", Intent: "edit"}
	if in.Name != "none" {
		edit.Cmd = "sugestão: \"" + brSuggestText(text, in) + "\""
	}
	items = append(items, edit)
	if mi.Offline && len(items) < olMenuMax+1 {
		items = append(items, olAction{Key: "online", Label: "/online — tentar o modelo de novo", Kind: "online", Intent: "online"})
	}
	return items
}

// brFacts are the facts the preconditions read.
func brFacts(cx *brCtx, p *epProfile) map[string]string {
	f := map[string]string{}
	for k, v := range cx.task.Slots {
		f["slot."+k] = v
	}
	f["hint"] = cx.in.Hint
	if cx.in.Git {
		f["ask.git"] = "1"
	}
	f["project.test"] = cx.proj.Test
	f["project.build"] = cx.proj.Build
	f["project.lint"] = cx.proj.Lint
	f["project.fmt"] = cx.proj.Fmt
	brRealFacts(cx, f)
	if cx.proj.Git {
		f["project.git"] = "1"
	}
	if p != nil {
		if p.Online {
			f["env.online"] = "1"
		}
		for _, b := range []string{"git", "go", "python3", "node", "npm", "make", "cargo", "docker", "podman", "tailscale", "ssh", "systemctl", "journalctl"} {
			if epHasBin(p, b) || epHasBin(p, b+".exe") {
				f["bin."+b] = "1"
			}
		}
	}
	for k, v := range f {
		if v != "" {
			cx.task.Fact(k, v)
		}
	}
	return f
}

// brExecute is the Pyhop loop: first applicable method, recovery on a
// failed step (possibly the next method), verification at the end.
func brExecute(cx *brCtx, methods []mhMethod) {
	t := cx.task
	reg := cbRegistry(nil)
	if cx.env.BeginTurn != nil {
		cx.env.BeginTurn() // shadow-git: this task's writes become /undo-able
	}
	tried := 0
	wroteTried := false
	for mi := 0; mi < len(methods); mi++ {
		m := methods[mi]
		if !mhApplicable(m, cx.facts) {
			continue
		}
		if wroteTried && m.Risk == "low" && !brReportOnly(m) {
			// a method that changes things was tried and did not go through
			// (denied, failed). Its read-only sibling is NOT a fallback: "guarda
			// no stash" with the push denied ran `git stash list` and sealed
			// feito e verificado. The request stops with the real reason.
			continue
		}
		wroteTried = wroteTried || m.Risk != "low"
		tried++
		cx.mslots = m.Slots // tipos dos slots do método em execução (distill)
		if t.Status == tkInvestigating || t.Status == tkRecovering || t.Status == tkNeedsInput || t.Status == tkBlocked {
			t.must(tkPlanning, "método "+m.Name)
		}
		t.Plan = t.Plan[:0]
		for i, st := range m.Steps {
			t.Plan = append(t.Plan, TaskStep{ID: fmt.Sprintf("s%d", i+1), Method: m.Name, Inputs: st.In, Action: st.Cap, Status: "pending"})
		}
		if len(m.Verify) > 0 {
			var vs []string
			for _, v := range m.Verify {
				vs = append(vs, v.Check)
			}
			for i := range t.Plan {
				t.Plan[i].Verifier = strings.Join(vs, "+")
			}
		}
		t.must(tkReady, "")
		t.must(tkRunning, "")
		next := false
		for si := 0; si < len(m.Steps); si++ {
			st := m.Steps[si]
			ps := &t.Plan[si]
			ps.Status = "running"
			fn := brCap(st.Cap)
			if c, ok := cbFind(reg, st.Cap); ok {
				if ok2, why := cbAvailable(c, cx.env.Profile); !ok2 {
					ps.Status = "failed"
					obs := rvNoCmd()
					obs.Exit, obs.Output = 127, why
					if brRecover(cx, obs, why, &next) {
						return
					}
					break
				}
			}
			if fn == nil {
				ps.Status = "failed"
				t.must(tkRecovering, "capacidade desconhecida: "+st.Cap)
				t.must(tkFailed, "bug na biblioteca de métodos")
				return
			}
			r := fn(cx, st.In)
			ps.EvidenceIDs = brLastEvidence(t)
			if r.ok {
				ps.Status = "done"
				if st.Out != "" {
					cx.vars[st.Out] = r.val
				}
				if cx.stop != "" { // the exploration budget fired
					t.must(cx.stop, cx.stopWhy)
					return
				}
				continue
			}
			ps.Status = "failed"
			if r.stop != "" && r.obs.Cands < 0 && !r.obs.Denied && r.obs.Exit < 0 {
				// a report or a question ends the task here
				cx.stopWhy = r.why
				if r.stop == tkNeedsInput {
					t.must(tkNeedsInput, r.why)
				} else {
					t.must(tkRecovering, r.why)
					t.must(r.stop, r.why)
				}
				return
			}
			if r.stop == tkNeedsInput { // a menu the user did not answer (headless)
				cx.stopWhy, cx.cat = r.why, rvAmbiguous
				t.must(tkNeedsInput, "pergunta: "+oneLine(r.why, 120))
				return
			}
			if brRecover(cx, r.obs, r.why, &next) {
				return
			}
			if retry, _ := cx.vars["retry_smaller"].(bool); retry && !next {
				si-- // the same step once more, narrower
				ps.Status = "pending"
				t.must(tkRunning, "repetindo com alvo menor")
				continue
			}
			break
		}
		if next {
			continue
		}
		if t.Status != tkRunning {
			return
		}
		t.must(tkVerifying, "")
		ok, unavailable, why := brVerify(cx, m)
		switch {
		case ok:
			if ignored := brSlotIgnored(cx, m); ignored != "" {
				cx.say("não verificado: %s", ignored)
				t.must(tkUnverified, ignored)
				return
			}
			if len(cx.in.Dropped) > 0 {
				why := fmt.Sprintf("operando citado %q nunca foi consumido por %s", cx.in.Dropped[0], m.Name)
				cx.say("não verificado: %s", why)
				t.must(tkUnverified, why)
				return
			}
			t.must(tkSucceeded, "verificado: "+brChecks(m))
		case unavailable:
			cx.cat = rvVerifierUnavailable
			cx.say("não verificado: %s", why)
			t.must(tkUnverified, why)
		default:
			r, _ := cx.vars["result"].(brTestRes)
			if r.Parsed != nil && r.Parsed.Data.Failed+r.Parsed.Data.Errors > 0 || r.Exit > 0 && r.Cmd != "" {
				cx.cat = rvTestFailed
				act, _, then := rvDecide(rvTestFailed, 0)
				t.must(tkRecovering, why)
				cx.say("%s; próximo passo: %s (precisa de um modelo pra propor a correção; sem modelo eu paro aqui com a evidência)", why, act)
				t.must(then, why)
				return
			}
			cx.say("verificação falhou: %s", why)
			t.must(tkFailed, why)
		}
		return
	}
	// no applicable method left
	why := "nenhum método aplicável"
	if tried == 0 || cx.cat == "" {
		cx.cat = rvPlanImpossible
	}
	if tried == 0 && len(cx.in.Missing) > 0 {
		why = "falta " + strings.Join(cx.in.Missing, ", ")
		cx.say("%s: diga qual (ex.: o nome do arquivo)", why)
		t.must(tkNeedsInput, why)
		return
	}
	if tried > 0 && cx.cat != "" && cx.cat != rvPlanImpossible {
		// the last step's reason explains the stop better than the
		// generic category text ("nenhum processo 'x'" > "falhou")
		_, _, then := rvDecide(cx.cat, 99)
		msg := cx.stopWhy
		if msg == "" {
			msg = rvTextPT(cx.cat)
		}
		if cx.cat == rvCommandMissing {
			msg += "; posso oferecer a instalação (capacidade install_tool, com confirmação)"
		}
		if cx.cat == rvPermission && !strings.Contains(msg, "--allow-tools") {
			msg += " (sem permissão — --allow-tools ou --yolo)"
		}
		cx.say("%s", msg)
		t.must(then, msg)
		return
	}
	if tried > 0 {
		why = "os métodos disponíveis falharam"
	}
	cx.say("%s (%s)", rvTextPT(rvPlanImpossible), why)
	if t.Status == tkInvestigating || t.Status == tkRecovering || t.Status == tkNeedsInput || t.Status == tkPlanning {
		t.must(tkBlocked, why)
	} else if !tkTerminal(t.Status) {
		t.must(tkBlocked, why)
	}
}

// brReportOnly: the method only says something (report/ask), runs nothing.
func brReportOnly(m mhMethod) bool {
	for _, s := range m.Steps {
		if s.Cap != "report" && s.Cap != "ask_slot" {
			return false
		}
	}
	return true
}

// brRecover applies the recovery table to a failed step. done = the task
// stopped; *next = try the next method.
func brRecover(cx *brCtx, obs rvObs, why string, next *bool) (done bool) {
	t := cx.task
	cat := rvClassify(obs)
	cx.cat = cat
	att := cx.attempts[cat]
	cx.attempts[cat]++
	t.Attempts++
	act, stop, then := rvDecide(cat, att)
	t.must(tkRecovering, string(cat)+": "+why)
	switch {
	case act == rvNextMethod && !stop:
		cx.stopWhy = why // remembered for the exhausted-methods text
		*next = true
		return false
	case act == rvRetrySmaller && !stop:
		cx.vars["retry_smaller"] = true
		return false
	case act == rvNoRetry && !stop:
		cx.stopWhy = why
		*next = true
		return false
	}
	msg := why
	if msg == "" {
		msg = rvTextPT(cat)
	}
	switch cat {
	case rvCommandMissing:
		msg += "; posso oferecer a instalação (capacidade install_tool, com confirmação)"
	case rvBudgetExhausted:
		msg += "; parei para não gastar mais comandos"
	}
	cx.stopWhy = msg
	if then == tkNeedsInput {
		t.must(tkNeedsInput, msg)
	} else {
		t.must(then, msg)
	}
	return true
}

// brObjectiveSlots — operandos que MUDAM o pedido se ignorados: "saves to
// output.file" preenche `out`; um método que nunca o referencia respondeu
// outra pergunta. Filtros (ext, name, newer…) não entram — método sem eles
// ainda cumpre o mesmo objetivo, só mais largo.
var brObjectiveSlots = []string{"out", "save", "dest", "to_dir", "new_name", "new_ext", "prefix", "suffix"}

// brSlotIgnored: slot-objetivo preenchido no intent mas nunca referenciado
// pelo método vencedor (steps, verify, need) — o pedaço do pedido que
// ninguém executou. Retorna "" ou a razão.
func brSlotIgnored(cx *brCtx, m mhMethod) string {
	uses := map[string]bool{}
	for _, st := range m.Steps {
		for _, v := range st.In {
			for _, s := range brObjectiveSlots {
				if strings.Contains(v, "$slot."+s) {
					uses[s] = true
				}
			}
		}
		if st.Cap == "need" {
			for _, s := range strings.Split(st.In["slots"], "|") {
				uses[strings.TrimSpace(s)] = true
			}
		}
	}
	for _, v := range m.Verify {
		for _, s := range brObjectiveSlots {
			if strings.Contains(v.Arg, "$slot."+s) || strings.Contains(v.Arg2, "$slot."+s) {
				uses[s] = true
			}
		}
	}
	for _, s := range brObjectiveSlots {
		if v := cx.task.Slots[s]; v != "" && !uses[s] {
			return fmt.Sprintf("operando %s=%q ignorado — método %s não o consome", s, v, m.Name)
		}
	}
	return ""
}

func brLastEvidence(t *TaskState) []string {
	if n := len(t.Evidence); n > 0 {
		return []string{t.Evidence[n-1].ID}
	}
	return nil
}

func brChecks(m mhMethod) string {
	var c []string
	for _, v := range m.Verify {
		c = append(c, v.Check)
	}
	return strings.Join(c, ", ")
}

// brStatusLine is the last line of a brain answer.
func brStatusLine(r brResult) string {
	if r.Task == nil {
		return ""
	}
	label := map[tkStatus]string{tkSucceeded: "feito e verificado", tkUnverified: "feito, NÃO verificado", tkFailed: "falhou",
		tkBlocked: "bloqueado", tkNeedsInput: "preciso de uma resposta", tkCancelled: "cancelado"}[r.Status]
	if label == "" {
		label = string(r.Status)
	}
	return fmt.Sprintf("· cérebro offline: %s · %s · %d evidência(s) · %d comando(s) · %d pergunta(s)", r.Intent.Name, label, len(r.Task.Evidence), r.Commands, r.Questions)
}

// brHandoffNote: quando o cérebro recusa, o modelo recebia só o texto cru —
// agora leva o pacote do que o offline tentou (leitura, slots, por que parou,
// evidência já coletada), pra não re-rodar nem re-explorar. "" quando não
// há nada pra carregar (ruído puro).
func brHandoffNote(text string, r brResult) string {
	in := r.Intent
	if !in.Understood() && in.Confidence == 0 && in.Name == "none" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<system-reminder>The deterministic offline layer already processed this request (no model tokens spent).\n")
	if in.Name != "" && in.Name != "none" {
		fmt.Fprintf(&b, "Reading: intent %q (confidence %.2f) — slots %v", in.Name, in.Confidence, in.Slots)
		if len(in.Missing) > 0 {
			fmt.Fprintf(&b, ", missing %v", in.Missing)
		}
		b.WriteString("\n")
	}
	if len(in.Alt) > 0 {
		fmt.Fprintf(&b, "Alternatives considered: %v.\n", in.Alt)
	}
	why := r.MissWhy
	if r.NoMethod {
		why += "|no_method"
	}
	fmt.Fprintf(&b, "Why it did not finish: %s (%s, %s).\n", why, r.Status, r.Category)
	if r.Task != nil && len(r.Task.Evidence) > 0 {
		b.WriteString("Evidence already gathered (verified — do not re-run):\n")
		for i, ev := range r.Task.Evidence {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "- %s: %s\n", ev.Kind, oneLine(ev.Text, 160))
		}
	}
	b.WriteString("Do not re-run commands it already ran. If the reading is wrong, start from the raw request. If it stopped on a denied command, the same command will hit the same wall — use --allow-tools/--yolo guidance or another approach.</system-reminder>")
	return b.String()
}

// ---------- headless (jazz -p --offline) ----------

// brHeadless answers a -p request offline. nil = the brain did not take it.
func brHeadless(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, prompt string, o pfOpts) *RunResult {
	env := &brEnv{Root: tools.Root, Shell: tools.Shell, Permit: ag.PermitCommand, Scripts: tools.scRegistry(),
		BeginTurn: func() { tools.BeginTurn(len(ag.Msgs)) }, AfterWrite: tools.cpAfterBash, Undo: tools.Undo, HostRun: brHostExec}
	if ag.sess != nil {
		env.Sink = tkSessionSink(ag.sess)
	}
	if o.askStdin {
		rd := bufio.NewReader(askStdin)
		env.Ask = func(q string, opts []string) int {
			fmt.Fprintln(os.Stderr, "? "+q)
			for i, l := range opts {
				fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, brAskLabel(l))
			}
			line, err := rd.ReadString('\n')
			if err != nil && line == "" {
				return -1
			}
			n, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || n < 1 || n > len(opts) {
				return -1
			}
			return n - 1
		}
		env.AskText = func(q string) (string, bool) {
			fmt.Fprintln(os.Stderr, "? "+q)
			line, err := rd.ReadString('\n')
			if err != nil && line == "" {
				return "", false
			}
			return strings.TrimSpace(line), true
		}
	}
	if ag.sess != nil { // the request and the answer are part of the session (jazz sessions, trace, -c)
		ag.sess.Append(Message{Role: "user", Content: prompt})
	}
	r := brRun(ctx, env, prompt)
	sigFlush(prompt, r, nil)
	if !r.Handled {
		if ag.sess != nil {
			ag.sess.Append(Message{Role: "assistant", Content: r.Text})
		}
		if strings.TrimSpace(r.Text) == "" {
			return nil
		}
		// understood-or-not, the brain already wrote the honest answer
		// ("entendi 'create' mas não tenho método"); it is terminal, not
		// resumable — Err ends in "blocked" to suppress the resume hint
		return &RunResult{Status: StopError, Model: "offline-brain", Text: r.Text,
			Err: "cérebro offline: blocked"}
	}
	if ag.sess != nil {
		ag.sess.Append(Message{Role: "assistant", Content: r.Text + "\n" + brStatusLine(r)})
	}
	ui.ToolStart("brain", "offline", r.Intent.Name)
	ui.ToolEnd("brain", "offline", ToolOutput{Text: r.Text, IsErr: r.Status != tkSucceeded, Display: Display{Summary: brStatusLine(r)}}, r.Elapsed)
	text := r.Text
	if sl := brStatusLine(r); sl != "" {
		text += "\n" + sl
	}
	rr := &RunResult{Text: text, Model: "offline-brain", Tools: r.Commands}
	switch r.Status {
	case tkSucceeded:
		rr.Status = StopDone
	case tkUnverified:
		rr.Status = StopUnverified
	default:
		rr.Status = StopError
		rr.Err = "cérebro offline: " + string(r.Status)
		if r.Status == tkNeedsInput {
			rr.Err += " (responda e rode de novo, ou use --ask-stdin)"
		}
	}
	return rr
}

// ---------- level 0 (jazz -p with a model) ----------

// brLevel0Intents are answered without the model even when one is
// available: read-only, deterministic, and verifiable ("abre o README.md",
// "git status" went to the model in 0.8.1 for 15–36 s and 32k tokens).
var brLevel0Intents = map[string]bool{
	"wordcount_stats":   true,
	"url_parse":         true,
	"date_normalize":    true,
	"csv_to_json":       true,
	"json_schema_infer": true,
	"table_render":      true,
	"regex_test":        true,
	"text_grep":         true,
	"md_outline":        true,
	"diff_apply_check":  true,

	"open_file": true, "git_status": true, "env_status": true, "tool_info": true, "tool_status": true,
	// 0.9.1: the deterministic coverage expanded — all read-only and verified
	"list_files": true, "project_summary": true, "explain": true, "find": true,
	"git_log": true, "git_diff": true, "port_owner": true, "list_ports": true,
	"disk_usage": true, "mem_usage": true, "list_processes": true, "help": true,
	"container_list": true, "service_status": true, "service_logs": true,
	"host_status": true, "list_hosts": true, "net_status": true, "git_branch": true,
	"self_info": true,
	// cardápio offline (docs/research/cardapio-offline.md): scripts só-leitura, verificados por script_ok
	"todo_scan": true, "git_cochange": true, "git_blame": true, "git_branch_status": true,
	"git_conflict_scan": true, "dotenv_check": true, "config_get": true, "file_snapshot": true,
	"dep_graph": true, "dep_outdated": true, "dup_code": true, "license_scan": true, "large_funcs": true,
	"symbol_refs": true,
	"uptime_load": true, "fs_recent": true, "archive_list": true, "hash_verify": true,
	"pkg_installed": true, "cron_list": true, "log_errors_recent": true, "net_ifaces": true,
	// v5: o cardápio de arquivos/texto/permissões vira caminho do cérebro
	"file_find": true, "perm_find": true, "text_count": true, "text_extract": true,
	"line_slice": true, "file_users": true, "net_route": true, "file_dupes": true,
	"env_get": true, "dir_diff": true,
}

// brGroundIntents are interpretive: online, the brain's facts ground the
// model instead of replacing it ("explique o código" wants an explanation,
// not a dump).
var brGroundIntents = map[string]bool{"explain": true, "project_summary": true}

// brLevel0Min is the confidence a level-0 answer needs.
const brLevel0Min = 0.85

// brReadActMin is the (calibrated) probability a READ-ONLY reading needs
// to run without a confirm question. Chow's rule with asymmetric costs: a
// wrong read-only answer costs one re-ask and shows its own evidence; a
// confirm question costs a round-trip every time. Mutations keep
// brLevel0Min (the permission gate is their consent, not this).
const brReadActMin = 0.7

// brActMin is the act threshold for a reading: read-only level-0 intents
// act at brReadActMin, everything else at brLevel0Min.
func brActMin(in Intent) float64 {
	if brLevel0Intents[in.Name] && !brGroundIntents[in.Name] {
		return brReadActMin
	}
	return brLevel0Min
}

// brLevel0On: JAZZ_BRAIN=off sends everything to the model again.
func brLevel0On() bool { return !olOffWord(os.Getenv("JAZZ_BRAIN")) }

// brHeadlessLevel0 answers a -p request with no model call when it is a
// clear read-only intent and the method finishes (succeeded, or a
// deterministic "not a git repository"). nil = the model takes it.
func brHeadlessLevel0(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, prompt string) *RunResult {
	if !brLevel0On() || len(prompt) > 200 || strings.ContainsAny(prompt, "\n`") {
		return nil
	}
	p := epGetLight()
	in := nuGet().Parse(prompt, nuRuntimeWorld(tools.Root, p))
	if !in.Understood() || in.Confidence < brActMin(in) || !brLevel0Intents[in.Name] {
		brLevel0Signal(prompt, in) // what went to the model, and why
		return nil
	}
	if in.Name == "env_status" || in.Name == "tool_info" || in.Name == "tool_status" {
		p = epGet(false) // versions and listings matter here
	}
	var events []tkEvent
	env := &brEnv{Root: tools.Root, Shell: tools.Shell, Permit: ag.PermitCommand, Profile: p,
		Scripts:   tools.scRegistry(),
		BeginTurn: func() { tools.BeginTurn(len(ag.Msgs)) }, AfterWrite: tools.cpAfterBash, Undo: tools.Undo,
		HostRun: brHostExec,
		Sink:    func(e tkEvent) { events = append(events, e) }}
	r := brRun(ctx, env, prompt)
	ok := r.Handled && (r.Status == tkSucceeded || r.Status == tkBlocked && r.Intent.Name == "git_status" && r.Category != rvPermission || r.Status == tkBlocked && r.Intent.Name == "tool_status" && r.Category == rvCommandMissing)
	sigFlush(prompt, r, brLevel0Extra(ok))
	if !ok {
		dbg("brain", map[string]any{"level0": "model", "intent": r.Intent.Name, "status": string(r.Status)})
		if note := brHandoffNote(prompt, r); note != "" {
			ag.Note(note)
		}
		return nil
	}
	if ag.sess != nil { // only an accepted answer becomes part of the session
		sink := tkSessionSink(ag.sess)
		ag.sess.Append(Message{Role: "user", Content: prompt})
		for _, e := range events {
			sink(e)
		}
		ag.sess.Append(Message{Role: "assistant", Content: r.Text + "\n" + brStatusLine(r)})
	}
	ui.ToolStart("brain", "offline", r.Intent.Name)
	ui.ToolEnd("brain", "offline", ToolOutput{Text: r.Text, IsErr: r.Status != tkSucceeded, Display: Display{Summary: brStatusLine(r)}}, r.Elapsed)
	rr := &RunResult{Text: r.Text + "\n" + brStatusLine(r), Model: "offline-brain", Tools: r.Commands, Status: StopDone}
	if r.Status != tkSucceeded {
		rr.Status, rr.Err = StopError, "cérebro offline: "+string(r.Status)
	}
	return rr
}

// brLevel0Interactive gives the deterministic brain first shot at an ONLINE
// interactive request too: the -p level-0 rule — a clearly understood
// read-only intent the brain finishes never reaches the model. Anything else
// (writes, questions, low confidence) goes to the model untouched.
func (a *App) brLevel0Interactive(ctx context.Context, text string) bool {
	if !brLevel0On() || len(text) > 200 || strings.ContainsAny(text, "\n`") {
		return false
	}
	p := epGetLight()
	in := nuGet().Parse(text, nuRuntimeWorld(a.root, p))
	if !in.Understood() || in.Confidence < brActMin(in) || !brLevel0Intents[in.Name] {
		brLevel0Signal(text, in)
		return false
	}
	if in.Name == "env_status" || in.Name == "tool_info" || in.Name == "tool_status" {
		p = epGet(false) // versions and listings matter here
	}
	env := &brEnv{Root: a.root, Shell: a.tools.Shell, Permit: a.agent.PermitCommand, Profile: p, Scripts: a.tools.scRegistry(),
		BeginTurn: func() { a.tools.BeginTurn(len(a.agent.Msgs)) }, AfterWrite: a.tools.cpAfterBash, Undo: a.tools.Undo, HostRun: brHostExec}
	if a.agent.sess != nil {
		env.Sink = tkSessionSink(a.agent.sess)
	}
	env.Ask = func(q string, opts []string) int {
		items, complete := brAskItems(opts)
		if !complete {
			items = append(items, mnItem{Label: "Nenhum desses", Key: 'n'})
		}
		i := a.ui.Choose(q, items, 0)
		if i < 0 || i >= len(opts) {
			return -1
		}
		return i
	}
	r := brRun(ctx, env, text)
	ok := r.Handled && (r.Status == tkSucceeded || r.Status == tkBlocked && r.Intent.Name == "git_status" && r.Category != rvPermission || r.Status == tkBlocked && r.Intent.Name == "tool_status" && r.Category == rvCommandMissing)
	extra := brLevel0Extra(ok)
	for k, v := range a.sigTTY() {
		extra[k] = v
	}
	sigFlush(text, r, extra)
	if !ok {
		if note := brHandoffNote(text, r); note != "" {
			a.agent.Note(note)
		}
		return false // nothing shown: the model gets the request + what we tried
	}
	if brGroundIntents[in.Name] {
		// interpretive request: the facts go to the model as grounding, the
		// model does the explaining (it starts from evidence, not from scratch)
		a.ui.Notice("cérebro: " + in.Name + " coletado como contexto pro modelo")
		a.agent.Note(fmt.Sprintf("<system-reminder>Deterministic facts the harness gathered for %q (intent %s, verified): use them as the basis of your answer instead of re-exploring.\n%s</system-reminder>", oneLine(text, 200), in.Name, truncateHeadTail(r.Text, 6000)))
		return false
	}
	if r.Text != "" {
		a.ui.print(sanitizeTerm(r.Text) + "\n")
	}
	a.ui.Notice(strings.TrimPrefix(brStatusLine(r), "· "))
	a.agent.Note(fmt.Sprintf("<system-reminder>The harness's deterministic brain handled %q as %s: %s — no model call was needed. Output:\n%s</system-reminder>", oneLine(text, 200), r.Intent.Name, r.Status, truncateHeadTail(r.Text, 6000)))
	return true
}

// ---------- interactive (offline mode) ----------

// brAppEnv is the interactive brain environment: the same hooks brInteractive
// always installed, plus the key-aware Ask that turns brConfirmOptions into a
// one-key menu (Enter/s does · e edits · n/Esc shows the alternatives).
func (a *App) brAppEnv() *brEnv {
	env := &brEnv{Root: a.root, Shell: a.tools.Shell, Permit: a.agent.PermitCommand, Scripts: a.tools.scRegistry(),
		BeginTurn: func() { a.tools.BeginTurn(len(a.agent.Msgs)) }, AfterWrite: a.tools.cpAfterBash, Undo: a.tools.Undo, HostRun: brHostExec}
	if a.agent.sess != nil {
		env.Sink = tkSessionSink(a.agent.sess)
	}
	env.Ask = func(q string, opts []string) int {
		items, complete := brAskItems(opts)
		if !complete {
			items = append(items, mnItem{Label: "Nenhum desses", Key: 'n'})
		}
		i := a.ui.Choose(q, items, 0)
		if i < 0 || i >= len(opts) {
			return -1
		}
		return i
	}
	return env
}

// brInteractive tries the brain on an offline request: true = answered.
func (a *App) brInteractive(ctx context.Context, text string) bool {
	env := a.brAppEnv()
	r := brRun(ctx, env, text)
	sigFlush(text, r, a.sigTTY())
	if !r.Handled && !a.brMissNow() {
		if note := brHandoffNote(text, r); note != "" {
			a.agent.Note(note)
		}
		return false // let olBefore probe: a reachable model should answer this
	}
	return a.brShow(text, r)
}

// brMissNow: the P2 menu replaces the generic offline menu only when there is
// no model to hand the request to — forced offline, or inside the probe
// window (the last probe failed). Outside it olBefore probes first, so a
// reachable provider still gets the request.
func (a *App) brMissNow() bool {
	return a.ol.forced || a.ol.on && time.Since(a.ol.lastProbe) < olProbeEvery
}

// brRunReading runs an already-interpreted reading (a pick of the P2 menu)
// without parsing the text again. act.Orig is the reading before the pick:
// the pick is the strongest label the forge gets (the owner named the
// intent), and it goes to the ledger with its provenance — terminal or not,
// session, menu position and the readings offered (spec 0.14b: a pick at
// position ≥2 by a human at the terminal is evidence; the default is not).
func (a *App) brRunReading(ctx context.Context, act olAction) bool {
	env := a.brAppEnv()
	in := *act.In
	r := brRunIntent(ctx, env, act.Text, in, brResult{Intent: in})
	extra := a.sigTTY()
	extra["pos"], extra["menu"] = strconv.Itoa(act.Pos), act.Menu
	sigPick(act.Text, act.Orig, r, extra)
	return a.brShow(act.Text, r)
}

// sigSession is the session id the signals carry (the forge wants evidence
// from distinct sessions); without one, the UTC day stands in.
func (a *App) sigSession() string {
	if a.agent != nil && a.agent.SessionID != "" {
		return a.agent.SessionID
	}
	return "day:" + time.Now().UTC().Format("20060102")
}

// sigTTY is the provenance of an interactive turn: src=tty only when stdin
// and stdout are a terminal — checked here, in code, so text piped from a
// file, a tool or a model never becomes an eligible correction for the forge.
func (a *App) sigTTY() map[string]string {
	ex := map[string]string{"sess": a.sigSession()}
	if isTTY(os.Stdin) && isTTY(os.Stdout) {
		ex["src"] = "tty"
	}
	return ex
}

// brShow prints a brain result — or the P2 menu when the brain did not
// understand — and records the note for the model.
func (a *App) brShow(text string, r brResult) bool {
	if !r.Handled {
		if r.Text != "" && !r.Miss {
			a.ui.print(sanitizeTerm(r.Text) + "\n")
		}
		a.brMissOffer(text, r.Intent)
		return true
	}
	if r.Text != "" {
		a.ui.print(sanitizeTerm(r.Text) + "\n")
	}
	if sl := strings.TrimPrefix(brStatusLine(r), "· "); strings.TrimSpace(sl) != "" { // small talk has no task: no empty "·" line
		a.ui.Notice(sl)
	}
	a.agent.Note(fmt.Sprintf("<system-reminder>While offline the harness's deterministic brain handled %q as %s: %s. Output:\n%s</system-reminder>", oneLine(text, 200), r.Intent.Name, r.Status, truncateHeadTail(r.Text, 6000)))
	return true
}

// brMissOffer shows the honest-miss menu (P2) above the input and remembers
// it: the next reply may pick an item (olTakePick, offers.go).
func (a *App) brMissOffer(text string, in Intent) {
	mi := a.olMenuState(text, true, false)
	mi.RotSeed = a.sigSession() // rotate positions 2..k per session (spec item 6)
	items := brMissActions(in, mi)
	a.olOffer("não peguei — você quis:", items)
}

// brWrite prints a result (used by tests and jazz eval).
func brWrite(w io.Writer, r brResult) {
	if r.Text != "" {
		fmt.Fprintln(w, r.Text)
	}
	if sl := brStatusLine(r); sl != "" {
		fmt.Fprintln(w, sl)
	}
}
