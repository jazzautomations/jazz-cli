package main

// recover.go: recovery by observable category (docs/design/offline-brain.md
// §6) and the exploration budget.
//
// A failed step is classified from what can be observed (exit code, output,
// candidate counts, the verifier) — never from what the step "meant" — and
// the table says what to do and how many times. The exploration budget stops
// the spiral the first external user hit (one find / after another): N
// read-only "let me see" commands in a row that bring no new fact → stop and
// ask instead of looking again.

import (
	"hash/fnv"
	"strings"
)

type rvCat string

const (
	rvNameNotFound        rvCat = "name_not_found"
	rvAmbiguous           rvCat = "ambiguous"
	rvCommandMissing      rvCat = "command_missing"
	rvTestFailed          rvCat = "test_failed"
	rvTimeout             rvCat = "timeout"
	rvPermission          rvCat = "permission_denied"
	rvEditConflict        rvCat = "edit_conflict"
	rvPlanImpossible      rvCat = "plan_impossible"
	rvBudgetExhausted     rvCat = "budget_exhausted"
	rvVerifierUnavailable rvCat = "verifier_unavailable"
	rvUnknown             rvCat = "unknown"
)

// rvObs is what a step left behind.
type rvObs struct {
	Exit       int    // -1 = no command ran
	Output     string // last part of the output
	TimedOut   bool
	Denied     bool // the permission check or the user said no
	Cands      int  // resolver candidates (-1 = not a resolution)
	Tied       int  // candidates tied at the top
	Failures   int  // failed tests (outparse)
	Parsed     bool // outparse understood the output
	NoMethod   bool // no method has its preconditions
	BudgetLeft int  // commands left (-1 = unlimited)
	NoVerifier bool // nothing can check the result
	EditStale  bool // old_string not found / file changed under us
}

func rvNoCmd() rvObs { return rvObs{Exit: -1, Cands: -1, BudgetLeft: -1} }

// rvClassify maps an observation to its category (first match wins).
func rvClassify(o rvObs) rvCat {
	low := strings.ToLower(o.Output)
	switch {
	case o.BudgetLeft == 0:
		return rvBudgetExhausted
	case o.NoMethod:
		return rvPlanImpossible
	case o.Denied:
		return rvPermission
	case o.TimedOut || o.Exit == 124:
		return rvTimeout
	case o.Cands == 0:
		return rvNameNotFound
	case o.Cands > 1 && o.Tied > 1:
		return rvAmbiguous
	case o.EditStale:
		return rvEditConflict
	case strings.HasPrefix(strings.TrimSpace(low), "bwrap:") || o.Exit == 127 || strings.Contains(low, "command not found") || strings.Contains(low, "comando não encontrado") ||
		strings.Contains(low, "not recognized as an internal or external command") || strings.Contains(low, "executable file not found"):
		return rvCommandMissing
	case o.Exit == 126 || strings.Contains(low, "permission denied") || strings.Contains(low, "permissão negada") || strings.Contains(low, "operation not permitted"):
		return rvPermission
	case o.Parsed && o.Failures > 0:
		return rvTestFailed
	case o.NoVerifier:
		return rvVerifierUnavailable
	}
	return rvUnknown
}

type rvAction string

const (
	rvAsk            rvAction = "ask"              // decisive question (menu when there are candidates)
	rvSuggestInstall rvAction = "suggest_install"  // offer the install_tool capability (confirmation)
	rvFixFailingTest rvAction = "fix_failing_test" // hand to the fix method (LLM optional)
	rvRetrySmaller   rvAction = "retry_smaller"    // narrow the target and run once more
	rvRereadReapply  rvAction = "reread_reapply"   // read the file again and redo the edit
	rvExplainStop    rvAction = "explain_stop"     // say what is missing and stop
	rvHandoff        rvAction = "handoff"          // write the handoff note and stop
	rvMarkUnverified rvAction = "mark_unverified"  // finish as "unverified", never "done"
	rvNextMethod     rvAction = "next_method"
	rvNoRetry        rvAction = "no_retry" // permission: never repeat, try another method or block
)

// rvRule is one row of the recovery table.
type rvRule struct {
	Cat     rvCat
	Detect  string     // what observation means this category (docs/trace)
	Actions []rvAction // in order; attempt k takes Actions[min(k, len-1)]
	Limit   int        // attempts before the task is blocked (0 = act once, never repeat)
	Then    tkStatus   // state after the actions are exhausted
}

// rvTable is the recovery table of the spec (§6).
var rvTable = []rvRule{
	{rvNameNotFound, "0 candidatos", []rvAction{rvAsk}, 1, tkNeedsInput},
	{rvAmbiguous, "2+ candidatos empatados", []rvAction{rvAsk}, 1, tkNeedsInput},
	{rvCommandMissing, "exit 127 / \"command not found\"", []rvAction{rvNextMethod, rvSuggestInstall}, 2, tkBlocked},
	{rvTestFailed, "outparse: falhas > 0", []rvAction{rvFixFailingTest}, 1, tkFailed},
	{rvTimeout, "watchdog / exit 124", []rvAction{rvRetrySmaller}, 1, tkBlocked},
	{rvPermission, "negado / EACCES", []rvAction{rvNoRetry, rvNextMethod}, 1, tkBlocked},
	{rvEditConflict, "trecho não bate / arquivo mudou", []rvAction{rvRereadReapply}, 1, tkBlocked},
	{rvPlanImpossible, "nenhum método com as pré-condições", []rvAction{rvExplainStop}, 0, tkBlocked},
	{rvBudgetExhausted, "orçamento de comandos/tempo zerado", []rvAction{rvHandoff}, 0, tkBlocked},
	{rvVerifierUnavailable, "nenhum verificador aplicável", []rvAction{rvMarkUnverified}, 0, tkUnverified},
	{rvUnknown, "falha sem categoria", []rvAction{rvNextMethod}, 1, tkFailed},
}

// rvRuleOf returns the table row of cat.
func rvRuleOf(cat rvCat) rvRule {
	for _, r := range rvTable {
		if r.Cat == cat {
			return r
		}
	}
	return rvTable[len(rvTable)-1]
}

// rvDecide: the action for attempt number attempt (0-based) of cat, and
// whether the task must stop (then = the state to stop in).
func rvDecide(cat rvCat, attempt int) (act rvAction, stop bool, then tkStatus) {
	r := rvRuleOf(cat)
	if attempt > r.Limit || (r.Limit == 0 && attempt > 0) {
		return r.Actions[len(r.Actions)-1], true, r.Then
	}
	i := attempt
	if i >= len(r.Actions) {
		i = len(r.Actions) - 1
	}
	act = r.Actions[i]
	switch act {
	case rvAsk, rvExplainStop, rvHandoff, rvMarkUnverified, rvFixFailingTest, rvSuggestInstall:
		return act, true, r.Then // these end the automatic part: the user (or a model) continues
	}
	return act, false, ""
}

// rvTextPT is the user-facing explanation of a category.
func rvTextPT(cat rvCat) string {
	switch cat {
	case rvNameNotFound:
		return "não achei esse nome"
	case rvAmbiguous:
		return "mais de um candidato serve"
	case rvCommandMissing:
		return "o comando não existe nesta máquina"
	case rvTestFailed:
		return "testes falharam"
	case rvTimeout:
		return "estourou o tempo"
	case rvPermission:
		return "sem permissão"
	case rvEditConflict:
		return "o arquivo mudou e a edição não bate mais"
	case rvPlanImpossible:
		return "não tenho um método offline que dê conta disso"
	case rvBudgetExhausted:
		return "o orçamento de comandos acabou"
	case rvVerifierUnavailable:
		return "não há como verificar o resultado"
	}
	return "falhou"
}

// ---------- exploration budget ----------

// rvExplore counts read-only "to see" commands that brought no new fact.
type rvExplore struct {
	Limit  int
	streak int
	total  int
	seen   map[uint64]bool
}

// rvDefaultExplore is N: three looks in a row with nothing new.
const rvDefaultExplore = 3

func newRvExplore(n int) *rvExplore {
	if n <= 0 {
		n = rvDefaultExplore
	}
	return &rvExplore{Limit: n, seen: map[uint64]bool{}}
}

// rvFactKey normalizes an output to the fact it carries (order of lines and
// spacing do not make a fact new).
func rvFactKey(out string) (uint64, bool) {
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, false
	}
	lines := strings.Split(out, "\n")
	h := fnv.New64a()
	var sum uint64
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			continue
		}
		h.Reset()
		h.Write([]byte(l))
		sum += h.Sum64() // order-independent
	}
	return sum, true
}

// Observe records one command. A command that changes something resets the
// streak; a read-only one with an empty or already-seen output extends it.
// stop = the streak reached the limit: stop and ask.
func (e *rvExplore) Observe(cmd string, readOnly bool, output string) (stop bool) {
	e.total++
	if !readOnly {
		e.streak = 0
		return false
	}
	if k, ok := rvFactKey(output); ok && !e.seen[k] {
		e.seen[k] = true
		e.streak = 0
		return false
	}
	e.streak++
	return e.streak >= e.Limit
}

// Streak is the current run of fruitless looks.
func (e *rvExplore) Streak() int { return e.streak }

// rvExploreQuestion is what jazz says when the budget stops it.
func rvExploreQuestion(n int) string {
	return "olhei " + itoa(n) + " vezes sem descobrir nada novo; em vez de continuar procurando: onde está o que você quer (arquivo, pasta ou comando)?"
}
