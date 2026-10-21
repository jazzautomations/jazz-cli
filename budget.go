package main

// budget.go: the "ponytail" process budget (docs/research/skills-personas-scripts.md
// section B). The harness, not the prompt, keeps a weak model from wandering:
//
//   - task size S/M/L and kind (bugfix/greenfield/refactor/sensitive/question)
//     are classified from the user's message with regexes (no LLM call);
//   - step budget S 12 / M 25 / L 50: a reminder at 70% and at 100%, and in
//     lean mode a hard stop at 2x (never above --max-steps);
//   - exploration cap S 4 / M 8 / L 15 read/search calls before the first edit:
//     a nudge when it is reached, refused calls at 2x (lean);
//   - reread guard: a second full read of an unchanged file returns a pointer
//     to the earlier tool result instead of the content;
//   - a write that replaces an existing file keeping > 50% of its lines is
//     refused once with "use edit";
//   - final-answer brevity rule (lean), sent with the turn's budget reminder.
//
// Config `budget: lean|normal|off` (default lean), flag --budget, env
// JAZZ_BUDGET. normal doubles the budgets and never hard-stops. Everything is
// appended to user messages or tool results: the system prompt is untouched.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Budget levels.
const (
	bgLean   = "lean"
	bgNormal = "normal"
	bgOff    = "off"
)

// bgMaxHash bounds the files the reread guard and the quality gate hash or keep.
const bgMaxHash = 1 << 20

// bgTask is what the classifier makes of a request.
type bgTask struct {
	Size       byte   // 'S', 'M' or 'L'
	Kind       string // bugfix | greenfield | refactor | question | change
	Sensitive  bool   // auth/money/migrations/secrets: quality strict
	NoTestEdit bool   // the request forbids changing the tests ("não altere tests/")
}

// bgTurn is the per-turn budget and quality bookkeeping.
type bgTurn struct {
	task       bgTask
	level      string
	stepBudget int // 0 = no step budget
	exploreCap int // 0 = no exploration cap
	hardSteps  bool
	explore    int  // read/search calls before the first edit
	edited     bool // a write/edit (or a writing bash) ran
	nudgedExpl bool
	warnedExpl bool
	warned70   bool
	warned100  bool
	refused    map[string]bool // write guard already fired for this path

	// quality gate (quality.go)
	pre      map[string]*qgPre // abs path -> content before its first change this turn
	order    []string          // paths in first-change order
	seq      int
	lastEdit int // seq of the last edit
	lastTest int // seq of the last test run
	qg       qgTurn
}

// bgRead is one earlier full read of a file.
type bgRead struct {
	hash   [32]byte
	msgIdx int    // index of the tool result in Agent.Msgs
	id     string // its tool call id
}

// bgState lives on the Agent (lazily created).
type bgState struct {
	reads map[string]bgRead
	turn  *bgTurn
	netMu sync.Mutex
	netUp int64 // unix nano of the last successful connectivity probe
}

func (a *Agent) bg() *bgState {
	if a.bud == nil {
		a.bud = &bgState{reads: map[string]bgRead{}}
	}
	return a.bud
}

// bgSetting resolves a harness setting: flag value, then env, then config, then def.
func bgSetting(flag, env, conf, def string, valid ...string) string {
	for _, v := range []string{flag, os.Getenv(env), conf} {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		for _, ok := range valid {
			if v == ok {
				return v
			}
		}
	}
	return def
}

// budgetLevel is the effective budget mode.
func (a *Agent) budgetLevel() string {
	return bgSetting(a.loop.opts.Budget, "JAZZ_BUDGET", a.cfg.Budget, bgLean, bgLean, bgNormal, bgOff)
}

// ---------- classification ----------

var (
	bgSensitiveRE = regexp.MustCompile(`(?i)\b(auth(?:[nz]\w*|entic\w*|ori[sz]\w*)?|login|oauth|passwords?|senhas?|credenciai?s?|credentials?|tokens?|jwt|secrets?|segredos?|crypt\w*|payments?|pagamentos?|billing|cobran[çc]a\w*|invoices?|faturas?|money|dinheiro|saldo|balance|refunds?|reembolso|migrations?|migra[çc](ão|ões|ao|oes)|schema|permiss(ion|ões|oes)\w*)\b`)
	bgRefactorRE  = regexp.MustCompile(`(?i)\b(refactor\w*|refator\w*|clean[ -]?up|limpe\w*|limpar|extract\w*|extrai\w*|extrair|renam\w*|renomei\w*|renomear|split|divid\w*|dedup\w*|reorgani[sz]\w*|simplif\w*)\b`)
	bgBugRE       = regexp.MustCompile(`(?i)\b(bugs?|fix\w*|conserta\w*|corrig\w*|corrij\w*|errors?|erros?|falh\w*|quebr\w*|broken|traceback|panic\w*|crash\w*|exceptions?|failing|fails?|flaky|regress\w*)\b|n[ãa]o (funciona|roda|compila|passa)`)
	bgNewRE       = regexp.MustCompile(`(?i)\b(add\w*|adicion\w*|create\w*|cri[ae]\w*|implement\w*|build|constru\w*|feature|funcionalidade|recurso|new|nov[oa]s?|write|escrev\w*|support\w*|suport\w*|suporte)\b`)
	bgLargeRE     = regexp.MustCompile(`(?i)\b(features?|funcionalidade\w*|refactor\w*|refator\w*|migrat\w*|migr[ae]\w*|rewrite|reescrev\w*|port(ar|e)?\b|implement\w* (a|o|um|uma|the)?\s*(full|complet\w*|whole|inteir\w*)|from scratch|do zero|add support|adicion\w* suporte|end[- ]to[- ]end|across|em todo)\b`)
	bgQuestionRE  = regexp.MustCompile(`(?i)^\s*(how|what|why|where|which|who|when|explain|describe|tell me|is there|does|do|can|como|o que|por ?que|onde|qual|quais|quem|quando|explique|explica|descreva|me (diga|fala|explica)|existe|tem)\b`)
	bgActionRE    = regexp.MustCompile(`(?i)\b(fix|add|create|implement|change|edit|write|update|remove|delete|rename|refactor|make|conserte|corrija|adicione|crie|implemente|mude|altere|edite|escreva|atualize|remova|apague|renomeie|refatore|fa[çc]a)\b`)
	bgFileRE      = regexp.MustCompile(`[\w./-]*\w\.(go|py|js|jsx|ts|tsx|rs|c|h|cc|cpp|java|kt|rb|php|sh|lua|swift|cs|md|json|ya?ml|toml|sql|html|css|vue|svelte)\b`)
	bgSymbolRE    = regexp.MustCompile("`[^`\n]{2,60}`")
	// bgNegatedRE: a verb the user forbids ("não refatore", "don't refactor",
	// "sem unificar") says nothing about the kind of task.
	bgNegatedRE = regexp.MustCompile(`(?i)\b(n[ãa]o|nem|sem|don'?t|do not|never|without|no)\s+(\w+\s+){0,2}?(refator\w*|refactor\w*|unifi\w*|unify\w*|rename\w*|renome\w*|extra(ia|ir|ct\w*)|limp\w*|clean\w*)`)
	// bgSpecErrRE: errors the request specifies as behaviour ("valor inválido é
	// erro de uso", "dá o mesmo erro", "raises an error") are not a bug report.
	bgSpecErrRE = regexp.MustCompile(`(?i)\b(é|e|d[áa]|dar|gera|gerar|levanta|lan[çc]a|retorna|devolve|vira|mesmo|mesma|raises?|returns?|throws?|same|an?)\s+(um\s+|o\s+|a\s+)?(mesmo\s+)?(erro|error|exce[çc][ãa]o|exception)s?\b|\b(mensage(m|ns)|messages?)\s+(do|de|of)\s+(erro|error)s?\b|\bexce[çc][õo]es\s+e\s+mensagens\b`)
	// bgNoTestEditRE: the request forbids changing tests.
	bgNoTestEditRE = regexp.MustCompile(`(?i)\b(n[ãa]o|nem|never|don'?t|do not)\s+(\w+\s+){0,2}?(alter\w*|mex\w*|edit\w*|modifi\w*|toqu\w*|toca\w*|change|modify|touch|edit)\b[^.\n]{0,25}\b(tests?/?|testes?)(\b|/)`)
	// bgLeadNewRE: the request opens with "add/implement/create": a feature.
	bgLeadNewRE = regexp.MustCompile(`(?i)^\W*(add|adicione|crie|create|implement\w*|implemente|build|construa|escreva|write|fa[çc]a)\b`)
)

// bgClassify sizes and types a request from its text (reminders stripped).
func bgClassify(input string) bgTask {
	s := strings.TrimSpace(acStripReminders(input))
	t := bgTask{Size: 'M', Kind: "change", NoTestEdit: bgNoTestEditRE.MatchString(s)}
	k := bgSpecErrRE.ReplaceAllString(bgNegatedRE.ReplaceAllString(s, " "), " ") // for the kind only
	switch {
	case bgRefactorRE.MatchString(k):
		t.Kind = "refactor"
	case bgLeadNewRE.MatchString(k):
		t.Kind = "greenfield"
	case bgBugRE.MatchString(k):
		t.Kind = "bugfix"
	case bgQuestionRE.MatchString(s) && !bgActionRE.MatchString(s):
		t.Kind = "question"
	case bgNewRE.MatchString(k):
		t.Kind = "greenfield"
	}
	t.Sensitive = bgSensitiveRE.MatchString(s)
	files := map[string]bool{}
	for _, f := range bgFileRE.FindAllString(s, -1) {
		files[f] = true
	}
	syms := len(bgSymbolRE.FindAllString(s, -1))
	switch {
	case len(files) >= 3 || bgLargeRE.MatchString(bgNegatedRE.ReplaceAllString(s, " ")):
		t.Size = 'L'
	case (len(files) == 1 || (len(files) == 0 && syms == 1)) && len(s) < 400:
		t.Size = 'S'
	}
	return t
}

// bgBudgets returns the step budget and exploration cap of a task.
func bgBudgets(t bgTask, level string) (steps, explore int) {
	size := t.Size
	if t.Kind == "refactor" { // refactors get one size more
		switch size {
		case 'S':
			size = 'M'
		case 'M':
			size = 'L'
		}
	}
	switch size {
	case 'S':
		steps, explore = 12, 4
	case 'L':
		steps, explore = 50, 15
	default:
		steps, explore = 25, 8
	}
	if t.Kind == "question" {
		explore = 0 // nothing to edit: reading is the task
	}
	if level == bgNormal {
		steps, explore = steps*2, explore*2
	}
	return steps, explore
}

// ---------- turn hooks (called from Agent.Run) ----------

// bgBeginTurn classifies the request, resets the per-turn counters and
// returns input with the budget reminder in front (lean/normal, parent agent).
func (a *Agent) bgBeginTurn(input string) string {
	st := a.bg()
	st.turn = nil
	if a.sub {
		return input
	}
	bt := &bgTurn{task: bgClassify(input), level: a.budgetLevel(), refused: map[string]bool{}, pre: map[string]*qgPre{}}
	st.turn = bt
	if bt.level == bgOff {
		return input
	}
	if a.mode() == "plan" {
		bt.task.Kind = "question" // plan mode only reads: no exploration cap
	}
	bt.stepBudget, bt.exploreCap = bgBudgets(bt.task, bt.level)
	bt.hardSteps = bt.level == bgLean
	dbg("budget", map[string]any{"size": string(bt.task.Size), "kind": bt.task.Kind, "sensitive": bt.task.Sensitive, "steps": bt.stepBudget, "explore": bt.exploreCap, "level": bt.level})
	if bt.task.Kind == "question" || len(strings.TrimSpace(acStripReminders(input))) < 12 {
		return input
	}
	return bgReminder(bt) + "\n" + input
}

// bgReminder is the short per-turn budget note (about 70 tokens).
func bgReminder(bt *bgTurn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<system-reminder>Budget for this task (size %c): about %d steps; at most %d read/search calls before your first edit; do not re-read a file you already read unless it changed (use offset/limit or rg).", bt.task.Size, bt.stepBudget, bt.exploreCap)
	if h := bgKindHint(bt.task); h != "" {
		b.WriteString(" " + h)
	}
	if bt.level == bgLean {
		b.WriteString(" Final reply: at most 8 lines: what changed, the evidence (command and result), one line on what you skipped or did not verify.")
	}
	b.WriteString("</system-reminder>")
	return b.String()
}

// bgKindHint is the one-line mode for the task type (section B, "picking by
// task type"): bugfix = reproduce first (debug-loop), greenfield = test the
// new logic (tdd-lite), refactor = behaviour and test count unchanged,
// sensitive = strict checks. The full skills live elsewhere; this is the
// harness-enforced core, checked again by the quality gate at Stop.
func bgKindHint(t bgTask) string {
	var h string
	switch t.Kind {
	case "bugfix":
		h = "Bugfix: reproduce the failure with a command first, fix the cause, re-run the same command; add a regression test where a test already exists nearby."
	case "greenfield":
		h = "New code: the smallest thing that works; new branches or loops get a test."
	case "refactor":
		h = "Refactor: run the tests before and after; behaviour and the number of tests stay the same; touch only what the change needs."
	}
	if t.Sensitive {
		h = strings.TrimSpace(h + " High-stakes path (auth/money/data): validate inputs, handle errors, never weaken a check or a test.")
	}
	return h
}

// bgStepLimit lowers the turn's step limit to twice the step budget (lean).
func (a *Agent) bgStepLimit(max int) int {
	bt := a.bgTurnState()
	if bt == nil || !bt.hardSteps || bt.stepBudget <= 0 {
		return max
	}
	if h := 2 * bt.stepBudget; h < max {
		return h
	}
	bt.hardSteps = false // --max-steps is lower: it stays the only limit
	return max
}

// bgStepNote is the reminder to append before request number step ("" = none).
func (a *Agent) bgStepNote(step int) string {
	bt := a.bgTurnState()
	if bt == nil || bt.stepBudget <= 0 {
		return ""
	}
	used := step - 1
	switch {
	case used >= bt.stepBudget && !bt.warned100:
		bt.warned100, bt.warned70 = true, true
		dbg("budget", map[string]any{"steps_reached": used})
		return fmt.Sprintf("<system-reminder>You have used the %d steps budgeted for this task. Stop exploring: verify what you have and give the final answer now; say what is left.</system-reminder>", bt.stepBudget)
	case used*10 >= bt.stepBudget*7 && !bt.warned70:
		bt.warned70 = true
		return fmt.Sprintf("<system-reminder>You have used %d of about %d steps budgeted for this task. Start wrapping up: finish the change, verify it once, answer.</system-reminder>", used, bt.stepBudget)
	}
	return ""
}

// bgAtLimit explains a step-limit stop caused by the budget's hard cap.
func (a *Agent) bgAtLimit() {
	if bt := a.bgTurnState(); bt != nil && bt.hardSteps {
		a.ui.Warn(fmt.Sprintf("orçamento de passos da tarefa esgotado (tamanho %c: %d passos, parada no dobro) — para tarefas longas use --budget normal ou off", bt.task.Size, bt.stepBudget))
	}
}

func (a *Agent) bgTurnState() *bgTurn {
	if a.sub || a.bud == nil {
		return nil
	}
	return a.bud.turn
}

// ---------- tool hooks (called from Agent.execTool and the Run loop) ----------

// bgPreTool runs before permissions. hit=true: out replaces the tool call
// (reread pointer, write refused, exploration cap); the tool does not run.
// It also records quality-gate pre-images of files about to change.
func (a *Agent) bgPreTool(tc ToolCall) (out ToolOutput, hit bool) {
	bt := a.bgTurnState()
	if bt == nil || a.mcp.Has(tc.Name) {
		return out, false
	}
	m := acArgsMap(tc.Name, tc.Args)
	p, _ := argStr(m, "path")
	abs := ""
	if strings.TrimSpace(p) != "" {
		abs = a.tools.abs(p)
	}
	if (tc.Name == "write" || tc.Name == "edit") && abs != "" {
		a.qgRemember(bt, abs)
	}
	if tc.Name == "script" {
		if _, _, _, sp := a.bgScriptKind(m); sp != "" {
			a.qgRemember(bt, a.tools.abs(sp)) // json_edit: the gate sees it like an edit
		}
	}
	if bt.level == bgOff {
		return out, false
	}
	switch tc.Name {
	case "read":
		if abs != "" && bgFullRead(m) {
			if o, ok := a.bgReread(abs, p); ok {
				return o, true
			}
		}
	case "write":
		if abs != "" && !bt.refused[abs] {
			content, _ := argStr(m, "content")
			if share, n := bgUnchangedShare(abs, content); n >= 10 && share > 0.5 {
				bt.refused[abs] = true
				dbg("budget", map[string]any{"write_refused": p, "unchanged": share})
				return ToolOutput{IsErr: true, Text: fmt.Sprintf("Refused: this write would replace %s but keeps %d%% of its lines unchanged. Use edit (old_string/new_string, several changes at once with edits[]) for the parts that change.", p, int(share*100)),
					Display: Display{Summary: "recusado: use edit (arquivo quase igual)", Path: a.tools.rel(abs)}}, true
			}
		}
	}
	if bt.level == bgLean && !bt.edited && bt.exploreCap > 0 && bt.explore >= 2*bt.exploreCap && a.bgExplore(tc.Name, m) {
		if !bt.warnedExpl {
			bt.warnedExpl = true
			a.ui.Warn(fmt.Sprintf("orçamento de exploração esgotado (%d leituras/buscas sem editar) — o modelo precisa editar ou perguntar", bt.explore))
		}
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("TOOL_NOT_EXECUTED: exploration budget used up (%d read/search calls before any edit; budget %d for this size-%c task). You have enough context: make the edit now with what you know, or ask the user one concrete question.", bt.explore, bt.exploreCap, bt.task.Size),
			Display: Display{Summary: "orçamento de exploração esgotado"}}, true
	}
	return out, false
}

// bgAfterTool updates the counters after a call ran; the returned text (maybe
// "") is appended to the tool result. Call it right before the result is
// appended to Msgs, so len(a.Msgs) is the result's index.
func (a *Agent) bgAfterTool(tc ToolCall, out ToolOutput) string {
	bt := a.bgTurnState()
	if bt == nil || a.mcp.Has(tc.Name) {
		return ""
	}
	m := acArgsMap(tc.Name, tc.Args)
	explore := a.bgExplore(tc.Name, m)
	switch tc.Name {
	case "script":
		_, edit, test, _ := a.bgScriptKind(m)
		if edit && !out.IsErr {
			bt.edited = true
			bt.seq++
			bt.lastEdit = bt.seq
		}
		if test {
			bt.seq++
			bt.lastTest = bt.seq
		}
	case "read":
		p, _ := argStr(m, "path")
		if !out.IsErr && strings.TrimSpace(p) != "" && bgFullRead(m) && !strings.HasPrefix(out.Text, "File unchanged since") {
			abs := a.tools.abs(p)
			if h, ok := bgHashFile(abs); ok {
				a.bud.reads[abs] = bgRead{hash: h, msgIdx: len(a.Msgs), id: tc.ID}
			}
		}
	case "write", "edit":
		if !out.IsErr {
			bt.edited = true
			bt.seq++
			bt.lastEdit = bt.seq
		}
	case "bash":
		cmd, _ := argStr(m, "command")
		if bgBashWrites(cmd) {
			bt.edited = true
			bt.seq++
			bt.lastEdit = bt.seq
		}
		if bgIsTestCmd(cmd) {
			bt.seq++
			bt.lastTest = bt.seq
		}
	}
	if !explore || bt.edited || bt.level == bgOff || bt.exploreCap <= 0 {
		return ""
	}
	bt.explore++
	if bt.explore >= bt.exploreCap && !bt.nudgedExpl {
		bt.nudgedExpl = true
		dbg("budget", map[string]any{"explore_reached": bt.explore})
		return fmt.Sprintf("\n\n<system-reminder>That was read/search call %d without an edit (budget %d for this task). You have enough context: make the edit now, or ask the user one concrete question.</system-reminder>", bt.explore, bt.exploreCap)
	}
	return ""
}

// bgOnReset runs after a --recover handoff replaced the history: the fresh
// context must be able to look again, so the exploration counter, its
// nudges and the reread index start over (the step budget does not).
func (a *Agent) bgOnReset() {
	st := a.bg()
	st.reads = map[string]bgRead{}
	if bt := st.turn; bt != nil {
		bt.explore, bt.nudgedExpl, bt.warnedExpl = 0, false, false
	}
}

// bgEndTurn logs the turn's budget metrics and saves the model scores.
func (a *Agent) bgEndTurn(stop StopReason, steps int) {
	if bt := a.bgTurnState(); bt != nil && debugOn {
		dbg("budget", map[string]any{"end": string(stop), "steps": steps, "explore": bt.explore, "edited": bt.edited, "size": string(bt.task.Size), "kind": bt.task.Kind})
	}
	if !a.sub {
		a.ojEndTurn(stop) // omni judge scoreboard (judge.go)
		bgScoresFlush()
	}
}

// ---------- reread guard ----------

func bgFullRead(m map[string]any) bool {
	off, _ := argInt(m, "offset")
	lim, _ := argInt(m, "limit")
	return off <= 1 && lim <= 0
}

func bgHashFile(abs string) ([32]byte, bool) {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > bgMaxHash {
		return [32]byte{}, false
	}
	if _, img := imageExt[strings.ToLower(filepath.Ext(abs))]; img {
		return [32]byte{}, false
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(b), true
}

// bgReread returns the pointer result when abs was fully read before, is
// unchanged, and that earlier result is still in the history (not compacted
// or rewound away).
func (a *Agent) bgReread(abs, shown string) (ToolOutput, bool) {
	r, ok := a.bud.reads[abs]
	if !ok {
		return ToolOutput{}, false
	}
	if r.msgIdx >= len(a.Msgs) || a.Msgs[r.msgIdx].Role != "tool" || a.Msgs[r.msgIdx].ToolCallID != r.id {
		delete(a.bud.reads, abs)
		return ToolOutput{}, false
	}
	h, ok := bgHashFile(abs)
	if !ok || h != r.hash {
		return ToolOutput{}, false
	}
	dbg("budget", map[string]any{"reread_blocked": shown})
	return ToolOutput{Text: fmt.Sprintf("File unchanged since your earlier full read of %s (tool call %s); its content is in that tool result above. Use it instead of re-reading; for one part, read with offset/limit or search with rg -n.", shown, r.id),
		Display: Display{Summary: "já lido (inalterado)", Path: a.tools.rel(abs)}}, true
}

// ---------- write guard ----------

// bgUnchangedShare: the share of the existing file's non-blank lines that the
// new content keeps verbatim, and how many non-blank lines the file has.
func bgUnchangedShare(abs, content string) (float64, int) {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > bgMaxHash {
		return 0, 0
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return 0, 0
	}
	have := map[string]int{}
	for _, l := range strings.Split(toLF(content), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			have[l]++
		}
	}
	kept, n := 0, 0
	for _, l := range strings.Split(toLF(string(b)), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		n++
		if have[l] > 0 {
			have[l]--
			kept++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return float64(kept) / float64(n), n
}

// ---------- tool-call shapes ----------

var (
	bgExploreCmds = map[string]bool{"rg": true, "grep": true, "egrep": true, "fgrep": true, "ag": true, "ack": true, "find": true, "fd": true, "fdfind": true,
		"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true, "wc": true, "tree": true, "file": true, "stat": true, "nl": true,
		"awk": true, "sed": true, "cd": true, "pwd": true, "echo": true, "sort": true, "uniq": true, "cut": true, "du": true, "realpath": true, "basename": true, "dirname": true}
	bgGitExplore  = map[string]bool{"grep": true, "log": true, "show": true, "diff": true, "status": true, "ls-files": true, "blame": true, "branch": true}
	bgSplitCmdRE  = regexp.MustCompile(`&&|\|\||[;|\n]`)
	bgWriteBashRE = regexp.MustCompile(`(^|[;&|(]\s*)(sed\s+(-\w+\s+)*-i|perl\s+-\w*i|tee\b|patch\b|git\s+(apply|checkout\s+--|restore)\b|mv\b|cp\b|rm\b|touch\b|truncate\b|dd\b)|(^|[^0-9&>])>>?\s*[^\s&|;>]`)
	bgTestCmdRE   = regexp.MustCompile(`^(\S*/)?(go\s+test|pytest|py\.test|python[0-9.]*\s+(-m\s+(pytest|unittest|doctest)|\S*test\S*\.py)|(npm|yarn|pnpm|bun)\s+(run\s+)?test|npx\s+(jest|vitest|mocha|ava|tap)|jest|vitest|mocha|cargo\s+(test|nextest)|make\s+(\S+\s+)*(test|check|tests)|ctest|mvn\s+(\S+\s+)*test|gradlew?\s+test|rspec|bundle\s+exec\s+rspec|tox|nox|bats|deno\s+test|mix\s+test|dotnet\s+test|swift\s+test|zig\s+build\s+test|phpunit|run_?tests\S*|test\.sh)\b`)
)

// bgSegWords splits a shell command into its simple commands, each as words
// with leading VAR=x assignments and wrappers (env, time, timeout N, nice) dropped.
func bgSegWords(cmd string) [][]string {
	var out [][]string
	for _, seg := range bgSplitCmdRE.Split(cmd, -1) {
		f := strings.Fields(strings.Trim(strings.TrimSpace(seg), "()"))
		for len(f) > 0 {
			w := f[0]
			if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") || w == "env" || w == "time" || w == "nice" || w == "command" || w == "exec" {
				f = f[1:]
			} else if w == "timeout" && len(f) > 1 {
				f = f[2:]
			} else {
				break
			}
		}
		if len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// bgExplore is bgIsExplore plus script calls: a read-only script that runs no
// command (find_symbol, tree_summary, git_state…) is a read/search call.
func (a *Agent) bgExplore(tool string, m map[string]any) bool {
	if tool == "script" {
		explore, _, _, _ := a.bgScriptKind(m)
		return explore
	}
	if tool == "read" && a.tools != nil {
		p, _ := argStr(m, "path")
		return strings.TrimSpace(p) != "" && !bgReadIsDir(map[string]any{"path": a.tools.abs(p)}) && !bgIsDoc(p)
	}
	return bgIsExplore(tool, m)
}

// bgScriptKind classifies a script call for the budget and the quality gate:
// explore = read effect and no command; edit = write effect (path = the file
// it changes) or a command that writes; test = run_tests or a test command.
func (a *Agent) bgScriptKind(m map[string]any) (explore, edit, test bool, path string) {
	if a.tools == nil {
		return
	}
	pol := a.tools.scPolicyArgs(m)
	if !pol.known {
		return
	}
	explore = pol.effect == "read" && pol.cmd == ""
	edit = pol.effect == "write" || pol.cmd != "" && bgBashWrites(pol.cmd)
	test = pol.name == "run_tests" || pol.cmd != "" && bgIsTestCmd(pol.cmd)
	if pol.effect == "write" {
		path = pol.path
	}
	return
}

// bgIsExplore: a read, or a bash command made only of read/search programs.
func bgIsExplore(tool string, m map[string]any) bool {
	switch tool {
	case "read":
		return !bgReadIsDir(m)
	case "bash":
	default:
		return false
	}
	cmd, _ := argStr(m, "command")
	if strings.TrimSpace(cmd) == "" || bgBashWrites(cmd) {
		return false
	}
	found := false
	for _, f := range bgSegWords(cmd) {
		w := filepath.Base(f[0])
		if w == "git" && len(f) > 1 && bgGitExplore[f[1]] {
			found = true
			continue
		}
		if !bgExploreCmds[w] {
			return false
		}
		found = true
	}
	return found
}

// bgIsDoc: a design/spec/readme document. Reading the docs the task points to
// is part of the task, not exploration (eval v2, 09-ler-design: DESIGN.md and
// README.md used two of the four reads a size-S task gets).
func bgIsDoc(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".rst", ".adoc", ".markdown":
		return true
	}
	return false
}

// bgReadIsDir: a read of a directory (a listing) is cheap navigation, not
// exploration; m carries "_root" from bgExplore when known.
func bgReadIsDir(m map[string]any) bool {
	p, _ := argStr(m, "path")
	root, _ := m["_root"].(string)
	if strings.TrimSpace(p) == "" {
		return false
	}
	if !filepath.IsAbs(p) && root != "" {
		p = filepath.Join(root, p)
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// bgIsTestCmd: one of the simple commands runs a test suite.
func bgIsTestCmd(cmd string) bool {
	for _, f := range bgSegWords(cmd) {
		if bgTestCmdRE.MatchString(strings.Join(f, " ")) {
			return true
		}
	}
	return false
}

// bgBashWrites: the command changes files (sed -i, redirects, mv/cp/rm…).
func bgBashWrites(cmd string) bool {
	for _, ix := range bgWriteBashRE.FindAllStringIndex(cmd, -1) {
		t := strings.TrimLeft(cmd[ix[0]:ix[1]], " \t;&|(0123456789")
		if strings.HasPrefix(t, ">") {
			// the redirect target starts inside the match: look at the rest of the line
			rest := strings.TrimLeft(cmd[ix[0]+strings.Index(cmd[ix[0]:], ">"):], "> \t")
			if strings.HasPrefix(rest, "/dev/") || strings.HasPrefix(rest, "&") {
				continue
			}
		}
		return true
	}
	return false
}
