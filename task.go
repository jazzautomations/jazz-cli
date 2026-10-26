package main

// task.go: explicit task state for the offline brain (docs/design/
// offline-brain.md §3). A request becomes a TaskState whose Status moves only
// along tkTransitions; every transition is one event in the session JSONL
// ("t":"task") and in the event stream ("kind":"task", shown by jazz trace).
// "succeeded" needs evidence: only verifying leads there, and only with at
// least one evidence item attached. A run whose result could not be checked
// ends "unverified" (the spec's "não verificado"), never "succeeded".

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

type tkStatus string

const (
	tkNew           tkStatus = "new"
	tkInvestigating tkStatus = "investigating"
	tkNeedsInput    tkStatus = "needs_input"
	tkPlanning      tkStatus = "planning"
	tkReady         tkStatus = "ready"
	tkRunning       tkStatus = "running"
	tkVerifying     tkStatus = "verifying"
	tkRecovering    tkStatus = "recovering"
	tkBlocked       tkStatus = "blocked"
	tkSucceeded     tkStatus = "succeeded"
	tkUnverified    tkStatus = "unverified"
	tkFailed        tkStatus = "failed"
	tkCancelled     tkStatus = "cancelled"
)

// tkTransitions is the state machine: from -> allowed targets. cancelled is
// reachable from every non-terminal state.
var tkTransitions = map[tkStatus][]tkStatus{
	tkNew:           {tkInvestigating},
	tkInvestigating: {tkNeedsInput, tkPlanning, tkBlocked, tkFailed},
	tkNeedsInput:    {tkInvestigating, tkPlanning, tkBlocked},
	tkPlanning:      {tkReady, tkNeedsInput, tkBlocked},
	tkReady:         {tkRunning},
	tkRunning:       {tkVerifying, tkRecovering, tkNeedsInput, tkBlocked, tkFailed},
	tkVerifying:     {tkSucceeded, tkUnverified, tkRecovering, tkFailed},
	tkRecovering:    {tkRunning, tkPlanning, tkNeedsInput, tkBlocked, tkFailed},
	tkBlocked:       {tkPlanning, tkNeedsInput, tkFailed},
}

// tkTerminal: no transition leaves these.
func tkTerminal(s tkStatus) bool {
	switch s {
	case tkSucceeded, tkUnverified, tkFailed, tkCancelled:
		return true
	}
	return false
}

// tkCanMove reports whether from -> to is a legal transition.
func tkCanMove(from, to tkStatus) bool {
	if tkTerminal(from) {
		return false
	}
	if to == tkCancelled {
		return true
	}
	for _, x := range tkTransitions[from] {
		if x == to {
			return true
		}
	}
	return false
}

// tkEv is one piece of evidence (a file read, a command and its exit, a
// parsed test result, an answer from the user).
type tkEv struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // file | cmd | tests | env | answer | resolve | note
	Text string `json:"text"`
}

type tkDecision struct {
	At   tkStatus `json:"at"`
	What string   `json:"what"`
}

type tkBudget struct {
	Cmds     int `json:"cmds"`      // commands the task may run
	LLMCalls int `json:"llm_calls"` // model calls (0 offline)
	Seconds  int `json:"seconds"`
	UsedCmds int `json:"used_cmds"`
	UsedLLM  int `json:"used_llm"`
}

// TaskStep is one step of the plan.
type TaskStep struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	Pre         []string          `json:"pre,omitempty"`
	Action      string            `json:"action"` // capability name (caps.go)
	Expected    string            `json:"expected,omitempty"`
	Verifier    string            `json:"verifier,omitempty"`
	Status      string            `json:"status"` // pending | running | done | failed | skipped
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
}

// TaskState is one request being worked on.
type TaskState struct {
	ID        string            `json:"id"`
	Goal      string            `json:"goal"`
	Intent    string            `json:"intent"`
	Slots     map[string]string `json:"slots,omitempty"`
	Status    tkStatus          `json:"status"`
	Facts     map[string]string `json:"facts,omitempty"`
	Unknowns  []string          `json:"unknowns,omitempty"`
	Plan      []TaskStep        `json:"plan,omitempty"`
	Evidence  []tkEv            `json:"evidence,omitempty"`
	Decisions []tkDecision      `json:"decisions,omitempty"`
	Attempts  int               `json:"attempts"`
	Budget    tkBudget          `json:"budget"`
	Started   time.Time         `json:"started"`

	sink func(tkEvent) // persistence (nil = in memory only)
}

// tkEvent is the persisted record of one transition.
type tkEvent struct {
	Task   string            `json:"task"`
	Goal   string            `json:"goal,omitempty"` // first event only
	Intent string            `json:"intent,omitempty"`
	From   tkStatus          `json:"from,omitempty"`
	To     tkStatus          `json:"to"`
	Why    string            `json:"why,omitempty"`
	Step   string            `json:"step,omitempty"`
	Slots  map[string]string `json:"slots,omitempty"`
	Ev     int               `json:"ev,omitempty"` // evidence items so far
}

// newTask starts a task in state new and records it.
func newTask(goal, intent string, slots map[string]string, sink func(tkEvent)) *TaskState {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	t := &TaskState{ID: "t" + hex.EncodeToString(b), Goal: goal, Intent: intent, Slots: slots, Status: tkNew,
		Facts: map[string]string{}, Budget: tkBudget{Cmds: 8, Seconds: 600}, Started: time.Now(), sink: sink}
	if sink != nil {
		sink(tkEvent{Task: t.ID, Goal: oneLine(goal, 200), Intent: intent, To: tkNew, Slots: slots})
	}
	return t
}

// To moves the task; an illegal transition is an error and changes nothing.
func (t *TaskState) To(s tkStatus, why string) error {
	if !tkCanMove(t.Status, s) {
		return fmt.Errorf("transição inválida %s → %s", t.Status, s)
	}
	if s == tkSucceeded && len(t.Evidence) == 0 {
		return fmt.Errorf("succeeded sem evidência: use unverified")
	}
	from := t.Status
	t.Status = s
	if why != "" {
		t.Decisions = append(t.Decisions, tkDecision{At: s, What: why})
	}
	if t.sink != nil {
		t.sink(tkEvent{Task: t.ID, From: from, To: s, Why: oneLine(why, 300), Ev: len(t.Evidence)})
	}
	return nil
}

// must is To for transitions the code knows are legal (a bug otherwise:
// recorded as a decision, never a panic).
func (t *TaskState) must(s tkStatus, why string) {
	if err := t.To(s, why); err != nil {
		t.Decisions = append(t.Decisions, tkDecision{At: t.Status, What: "bug: " + err.Error()})
		dbg("task", map[string]any{"task": t.ID, "error": err.Error()})
	}
}

// AddEvidence attaches one item and returns its id.
func (t *TaskState) AddEvidence(kind, text string) string {
	id := fmt.Sprintf("e%d", len(t.Evidence)+1)
	t.Evidence = append(t.Evidence, tkEv{ID: id, Kind: kind, Text: oneLine(text, 400)})
	return id
}

// Fact records a fact (env profile, project detection, answers).
func (t *TaskState) Fact(k, v string) {
	if t.Facts == nil {
		t.Facts = map[string]string{}
	}
	t.Facts[k] = v
}

// Summary is the one-paragraph view (headless output, handoff, eval).
func (t *TaskState) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tarefa %s · %s · %s", t.ID, t.Intent, t.Status)
	if len(t.Plan) > 0 {
		var st []string
		for _, s := range t.Plan {
			st = append(st, s.Action+":"+s.Status)
		}
		b.WriteString(" · passos " + strings.Join(st, " "))
	}
	if n := len(t.Evidence); n > 0 {
		fmt.Fprintf(&b, " · %d evidência(s)", n)
	}
	return b.String()
}

// tkFactsFromEnv seeds the facts every task starts with (envprobe.go).
func tkFactsFromEnv(t *TaskState, p *epProfile) {
	if p == nil {
		return
	}
	t.Fact("os", p.OS+"/"+p.Arch)
	t.Fact("cpus", fmt.Sprint(p.CPUs))
	if p.MemTotalMB > 0 {
		t.Fact("ram_mb", fmt.Sprint(p.MemTotalMB))
	}
	if p.Online {
		t.Fact("net", "online")
	} else {
		t.Fact("net", "offline")
	}
	if p.WSL {
		t.Fact("wsl", "1")
	}
	if p.Termux {
		t.Fact("termux", "1")
	}
	if p.Container != "" {
		t.Fact("container", p.Container)
	}
	if p.CPUs <= 1 {
		t.Budget.Seconds *= 2 // one core: sequential and slower
	}
}

// tkFactKeys lists the facts in a stable order (trace, tests).
func (t *TaskState) tkFactKeys() []string {
	k := make([]string, 0, len(t.Facts))
	for x := range t.Facts {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// ---------- persistence ----------

// tkSessionSink writes each transition to the session JSONL and the event
// stream of this process (both optional).
func tkSessionSink(s *Session) func(tkEvent) {
	return func(e tkEvent) {
		if s != nil {
			s.TaskEvent(e)
		}
		f := map[string]any{"task": e.Task, "to": string(e.To)}
		if e.From != "" {
			f["from"] = string(e.From)
		}
		if e.Why != "" {
			f["why"] = e.Why
		}
		if e.Intent != "" {
			f["intent"] = e.Intent
		}
		if e.Goal != "" {
			f["goal"] = e.Goal
		}
		if e.Ev > 0 {
			f["ev"] = e.Ev
		}
		obEmit("task", f)
	}
}

// TaskEvent records one task transition in the session tree.
func (s *Session) TaskEvent(e tkEvent) { s.write(sessEntry{T: "task", Task: &e}) }

// tkTraceLine renders a "task" event for jazz trace.
func tkTraceLine(e obEvent) string {
	if e.str("from") == "" {
		line := fmt.Sprintf("tarefa %s nova: %s", e.str("task"), e.str("intent"))
		if g := e.str("goal"); g != "" {
			line += fmt.Sprintf(" (%q)", oneLine(g, 80))
		}
		return line
	}
	line := fmt.Sprintf("tarefa %s: %s → %s", e.str("task"), e.str("from"), e.str("to"))
	if w := e.str("why"); w != "" {
		line += " · " + oneLine(w, 120)
	}
	if n := e.int("ev"); n > 0 {
		line += fmt.Sprintf(" · %d evidência(s)", n)
	}
	return line
}
