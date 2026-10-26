package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Agent struct {
	// NetOffline (interactive): a network failure confirmed by a probe ends
	// the turn with errNoNet at once, so the app goes offline (netfast.go),
	// instead of waiting for the network inside the request (headless does).
	NetOffline bool
	// FastFirst (interactive, model not pinned): the main request starts with
	// the fastest acceptable free model of the chain (acFastFirst).
	FastFirst bool
	// AskStdin (-p --ask-stdin): ask_user reads the answer from stdin.
	AskStdin bool

	apb *apBatch // merged confirmations of the current response (approve_batch.go)

	cfg       *Config
	ui        UI
	tools     *Toolbox
	mcp       *MCPManager
	sess      *Session
	Model     string // provider/model
	System    string
	Msgs      []Message
	Usage     Usage
	SessionID string
	Perms     *PermSet // permission rules (perms.go); nil = none
	Hooks     *HookSet // user hooks (hooks.go); nil = none

	turnModel   string // model used by the current/last turn (sticky fallback)
	turnUsage   Usage  // usage of the current turn (includes compaction and subagents)
	attach      []ImagePart
	lastPrompt  int // input+output tokens of the last response
	countedIdx  int // Msgs index already included in lastPrompt
	allow       map[string]bool
	recent      []string
	steerMu     sync.Mutex
	steer       []string // typed during the turn, not yet delivered
	unsent      []string // steer left over when a turn ended (bug 39); guarded by steerMu
	pendingNote []string

	mq *MsgQueue // messages typed during the turn: steer / follow-up (queue.go); guarded by steerMu

	textRefs   map[string]bool // refs that rejected native tools in this session (G15)
	textSys    string          // cached System + text-tools block
	textSysFor string          // the System textSys was built from
	retryDelay func(err error, attempt int) time.Duration

	sub      bool    // research subagent (G7): read-only, no task tool, no session
	maxSteps int     // overrides cfg.MaxSteps when > 0
	sa       saChild // parallel subagents: id, parent, budget cap, write set, path locks (subagent.go)

	stalled map[string]time.Time // ref -> when it last missed the first-byte deadline
	oj      *ojState             // omni judge: task type, picked model, verifier/fusion state (judge.go)

	smart *smState // smart permission mode: classifier and verdict cache (smart.go)

	apMu sync.Mutex
	ap   *apStore // "sempre" answers to prompts with a risk reason (approvals.go)
	dgAl *apStore // "sempre" answers of the diff review (diffguard.go)

	fileRules      []SteeringRule // fileMatch steering rules (steering.go), loaded lazily
	steeringLoaded bool

	loop acLoop // --until, /goal and --review (agent_loop.go)

	sk  skState  // built-in skills picked by the harness (skills.go)
	hg  *hgState // honest-completion guards for this turn (guard_honest.go)
	bud *bgState // ponytail budget, quality gate, outage wait (budget.go, quality.go, netwait.go)

	uiLint map[string]string // per UI file, hash of the last ui_lint findings ("" = clean) (uilint.go)

	gr grState // injection guard and supply-chain checks (guardrails.go)
	// UntrustedBash: headless run in a folder the user has not trusted (F1):
	// bash commands that are not read-only need an allow rule (--allow-tools)
	// or --yolo, otherwise they are refused.
	UntrustedBash bool
	// Headless: no interactive user (the privacy notice is a stderr line, F4).
	Headless     bool
	PrivacyCheck bool            // main turns on the privacy notice (security.go)
	privacyRef   map[string]bool // Train providers already announced this session

	blk blockTracker // blocked calls in this turn (blockux.go)
	// BlockReport: why the spiral breaker stopped the last turn ("" = it did
	// not); headless prints it as the final answer (blockux.go).
	BlockReport string
	blockWhy    string // PT reason of the last refusal in permit (blockux.go)

	obst atomic.Pointer[obState] // events, write-ahead, deadline grace (obs.go)

	// parallel tool calls (partools.go): the schedule of the current batch,
	// the index of the call being executed (-1 = none), the lane it used
	pt     *ptSched
	ptIdx  int
	ptLane string
	ptOff  bool // never run calls early (tests)
}

// acStallPenalty is how long a ref that stalled before its first byte goes to the
// back of the candidate list, so the next requests do not wait on it again.
// Short on purpose: free gateways stall in bursts of a minute or two, and a
// long penalty leaves a long task on the weakest model of the chain.
var acStallPenalty = 3 * time.Minute

// acStickyTTL bounds how long a fallback model stays sticky inside one turn.
// A headless run is a single turn, so a transient 429 or stall on the main
// model used to hand the whole task to the last (weakest) fallback. After the
// TTL the chain starts again from the main model (stalled refs still demoted).
var acStickyTTL = 3 * time.Minute

// acDemoteStalled moves refs that stalled recently behind the others (order kept).
func (a *Agent) acDemoteStalled(cands []string) []string {
	if len(a.stalled) == 0 || len(cands) < 2 {
		return cands
	}
	var fresh, slow []string
	for _, r := range cands {
		if t, ok := a.stalled[r]; ok && time.Since(t) < acStallPenalty {
			slow = append(slow, r)
		} else {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) == 0 {
		return cands
	}
	return append(fresh, slow...)
}

func NewAgent(cfg *Config, ui UI, tools *Toolbox, mcp *MCPManager, model string) *Agent {
	a := &Agent{cfg: cfg, ui: ui, tools: tools, mcp: mcp, Model: model, allow: map[string]bool{}, ptIdx: -1}
	if tools != nil {
		tools.Window = a.window // bash output budget follows the model in use (outshape.go)
	}
	return a
}

// Steer queues a message typed while the agent is working.
func (a *Agent) Steer(s string) {
	a.steerMu.Lock()
	a.steer = append(a.steer, s)
	a.steerMu.Unlock()
}

// Note adds context that goes in front of the next user message (e.g. after /undo).
func (a *Agent) Note(s string) { a.pendingNote = append(a.pendingNote, s) }

func (a *Agent) takeSteer() []string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	s := a.steer
	a.steer = nil
	return s
}

// parkSteer moves steering that arrived too late for the turn into unsent, so
// it is neither lost nor injected in the middle of an unrelated turn (bug 39).
func (a *Agent) parkSteer() {
	a.steerMu.Lock()
	a.unsent = append(a.unsent, a.steer...)
	a.steer = nil
	a.steerMu.Unlock()
}

// TakeSteer returns (and clears) text the user typed during a turn that the
// model never saw, so the UI can put it back in the editor after the turn.
// If nobody takes it, the next Run prepends it to its input.
func (a *Agent) TakeSteer() string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	all := append(append([]string(nil), a.unsent...), a.steer...)
	a.unsent, a.steer = nil, nil
	return strings.TrimSpace(strings.Join(all, "\n\n"))
}

func (a *Agent) append(m Message) {
	a.Msgs = append(a.Msgs, m)
	a.sess.AppendMeta(m, a.obTakeMeta(m)) // model/tokens/stop of an assistant reply (obs.go)
}

// toolSpecs is the byte-stable spec list: built-ins, task (parent only), MCP.
func (a *Agent) toolSpecs() []ToolSpec {
	var out []ToolSpec
	if a.sub {
		for _, s := range a.tools.Specs() {
			if acSubTools[s.Name] || (a.sa.work && saWorkTools[s.Name]) {
				out = append(out, s)
			}
		}
	} else {
		out = append(append(out, a.tools.Specs()...), a.ojTaskSpecs()...) // task (+ sidekick with --fusion on)
	}
	return append(out, a.mcp.Specs()...)
}

// knownTool maps a tool name written by the model to a real tool ("" = unknown).
func (a *Agent) knownTool(n string) string {
	if a.mcp.Has(n) {
		return n
	}
	c := CanonicalTool(n)
	for _, s := range a.toolSpecs() {
		if s.Name == c {
			return c
		}
	}
	return ""
}

// EnsureSystem builds the system prompt once (MCP tools must be known first).
func (a *Agent) EnsureSystem() {
	if a.System != "" {
		return
	}
	if a.mcp != nil && !a.mcp.Done() {
		a.ui.Thinking("conectando MCP")
		a.mcp.Wait(50 * time.Second)
		a.ui.StopThinking()
	}
	skConfigure(a.cfg) // skills.claude_dir for the user skill list
	sys, dirs := buildSystemPrompt(a.tools.Root, a.mcp.Specs())
	a.System = sys
	a.tools.MarkInstrSeen(dirs)
	if i := strings.Index(sys, "<untrusted suspected_injection="); i >= 0 && a.injMode() != "off" && !a.sub {
		label := sys[i+len("<untrusted suspected_injection="):]
		if j := strings.Index(label, ">"); j > 0 {
			label = strings.Trim(label[:j], `"`)
		}
		a.gr.startHit = label
		a.ui.Warn("possível prompt injection nas instruções do projeto (" + label + "): ações que mudam algo vão pedir confirmação no primeiro turno")
	}
	if a.sess == nil && !a.sub {
		s, err := NewSession(a.tools.Root, a.Model, a.System)
		if err != nil {
			a.ui.Warn("sessão não será salva: " + err.Error())
		}
		a.sess = s
		if s != nil {
			a.SessionID = s.ID
			a.obAttach("startup") // event stream next to the session (obs.go)
		}
		// SessionStart hooks are run by main (pfStartSession), which knows the
		// source (startup/resume); running them here too would fire them twice.
	}
}

func (a *Agent) hookWarnings(hr HookResult) {
	for _, w := range hr.Warnings {
		a.ui.Warn("hook: " + w)
	}
}

func (a *Agent) estTokens() int {
	n := a.lastPrompt
	from := a.countedIdx
	if a.lastPrompt == 0 {
		n = len(a.System) / 4
		from = 0
	}
	chars := 0
	for _, m := range a.Msgs[min(from, len(a.Msgs)):] {
		chars += len(m.Content) + len(m.Reasoning)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Args) + len(tc.Name)
		}
		chars += 3000 * len(m.Images) // rough per-image cost
	}
	return n + chars*10/35
}

// limitsOf returns the window and output cap of ref, with safe defaults.
func (a *Agent) limitsOf(ref string) (int, int) {
	w, out := a.cfg.Limits(ref)
	if w <= 0 {
		w = 128000
	}
	return w, out
}

func (a *Agent) window() int {
	w, _ := a.limitsOf(a.TurnModel())
	return w
}

func acThreshold(w, maxOut int) int {
	t := w - maxOut - 16000
	if alt := w * 8 / 10; alt < t {
		t = alt
	}
	if t < w/2 {
		t = w / 2
	}
	return t
}

// compactThreshold is the smallest threshold across the models this turn may use,
// so falling back to a smaller-window model never overflows.
func (a *Agent) compactThreshold() int {
	if isOmniRef(a.Model) || isOmniRef(a.TurnModel()) {
		// an omni alias expands into many models, some with small windows; the
		// threshold follows the model actually in use (Limits of an alias is its
		// first candidate's window)
		w, out := a.limitsOf(a.TurnModel())
		return acThreshold(w, out)
	}
	refs := a.cfg.Candidates(a.TurnModel(), a.Model)
	if len(refs) == 0 {
		refs = []string{a.TurnModel()}
	}
	best := 0
	for _, r := range refs {
		w, out := a.limitsOf(r)
		if t := acThreshold(w, out); best == 0 || t < best {
			best = t
		}
	}
	return best
}

func (a *Agent) mode() string {
	m := a.cfg.EffMode()
	if m == "" {
		m = "auto"
	}
	return m
}

func (a *Agent) stepLimit() int {
	if a.maxSteps > 0 {
		return a.maxSteps
	}
	if a.cfg.MaxSteps > 0 {
		return a.cfg.MaxSteps
	}
	return 80
}

const planReminder = "<system-reminder>Plan mode is on: investigate with read-only tools (read, read-only bash such as rg/ls/git log, task), then reply with a concise numbered plan (files, changes, verification). Do not edit or write files and do not run commands that change anything.</system-reminder>"

const stepLimitReminder = "<system-reminder>Step limit reached. Do not call any more tools. Give your final answer now: what was done, what is left.</system-reminder>"

// Run handles one user message until the model gives a final answer.
func (a *Agent) Run(ctx context.Context, input string) (stop StopReason, err error) {
	a.EnsureSystem()
	mode := a.mode()
	a.ui.SetMode(mode)
	a.gr.injection, a.gr.startHit = a.gr.startHit, "" // the injection guard is per turn
	a.blk, a.BlockReport = blockTracker{}, ""         // spiral breaker is per turn (blockux.go)
	t0 := time.Now()
	a.turnUsage = Usage{}
	toolsRun, steps := 0, 0
	turnModel := a.Model
	a.turnModel = turnModel
	var stickSince time.Time // when turnModel became a fallback (sticky TTL)
	stop = StopDone
	var lt *acTurnLoop // completion checks, recovery, plan cache (agent_loop.go, agent_recover.go)

	defer func() {
		a.ptEnd() // early calls still running are cancelled and discarded (partools.go)
		if s := a.dgReview(ctx); s != "" {
			stop = s // risky diff refused at the end of the turn (diffguard.go)
		}
		a.turnModel = turnModel
		a.parkSteer()
		a.qTurnEnd(stop, err) // follow-ups start only after a turn that ended well (queue.go)
		a.bgEndTurn(stop, steps)
		a.acTurnDone(ctx, lt, stop, steps)                 // plan cache (plancache.go)
		a.obAfterStep(steps)                               // traced checkpoint of pending writes (obs.go)
		a.obTurnEnd(stop, steps, toolsRun, time.Since(t0)) // obs.go
		a.ui.TurnEnd(TurnStats{Dur: time.Since(t0), Tools: toolsRun, Usage: a.turnUsage, Model: turnModel, Ctx: a.estTokens(), Win: a.window(), Cost: a.turnUsage.Cost, Stop: string(stop), Steps: steps, Redacted: a.rdTurnCount()})
		if time.Since(t0) > 20*time.Second {
			a.ui.Bell()
		}
	}()

	// Text typed after the previous turn stopped listening (bug 39).
	if s := a.TakeSteer(); s != "" {
		input = s + "\n\n" + input
	}
	if !a.sub {
		input = a.grSanitizeInput(input) // pasted text can smuggle invisible instructions (sanitize.go)
	}
	lt = a.acBeginTurnLoop(ctx, input)
	a.obTurnStart(lt.input)       // obs.go
	a.sessAutoTitleTurn(lt.input) // first turn of a new session: its title (sessions_ui.go)
	if !a.sub && a.Hooks.Has(HookUserPromptSubmit) {
		hr := a.Hooks.Run(ctx, HookUserPromptSubmit, "", map[string]any{"session_id": a.SessionID, "prompt": input})
		a.hookWarnings(hr)
		if hr.Block {
			msg := "um hook bloqueou o prompt"
			if hr.Reason != "" {
				msg += ": " + hr.Reason
			}
			a.ui.Warn(msg)
			stop = StopDenied
			return stop, nil
		}
		if c := strings.TrimSpace(hr.Context); c != "" {
			input = "<system-reminder>\n" + c + "\n</system-reminder>\n" + input
		}
	}
	if len(a.pendingNote) > 0 {
		input = strings.Join(a.pendingNote, "\n") + "\n\n" + input
		a.pendingNote = nil
	}
	input = a.acTurnInput(input)                         // /goal reminder
	a.hgBeginTurn(input)                                 // test policy, protected-file snapshot (guard_honest.go)
	input = a.bgBeginTurn(input)                         // task size/kind, budget reminder (budget.go)
	input = a.skTurnInput(input)                         // built-in skill picked from the request (skills.go)
	if pf := a.acPrefetchNote(ctx, lt.input); pf != "" { // --prefetch=grep, first turn only (prefetch.go)
		input = pf + "\n" + input
	}
	if mode == "plan" && !a.sub {
		input = planReminder + "\n" + input
	}
	turnModel, input = a.ojStartTurn(ctx, lt.input, turnModel, input) // omni judge: task type -> model, fusion/panel notes (judge.go)
	if !a.sub {
		a.tools.BeginTurn(len(a.Msgs))
		OmniBeginTurn() // re-seed the weighted router once per turn, not per request
	}
	// similar verified plans go after the user's text (plancache.go); input
	// itself stays clean for the checks that read it later (acRecap)
	a.cbAttachNotice(turnModel) // a model without vision: say so and name one that sees (caps.go)
	a.append(Message{Role: "user", Content: input + a.pcInject(lt) + a.niInject(lt.input) + a.dcInject(ctx, lt.input), Images: a.attach})
	a.attach = nil

	nudgedVerify, nudgedTodo, nudgedEmpty, nudgedFake := false, false, false, false
	callsIssued := 0 // tool calls the model made in this turn (run or not)
	editedCode, lastDenied := false, false
	textConts, callTruncs, stopHooks := 0, 0, 0
	boostOut := false // the last reply was cut: the next request asks for more output (cutoff.go)
	a.recent = nil
	// lean budget: hard stop at 2x the step budget (budget.go)
	maxSteps := a.bgStepLimit(a.stepLimit())
	compactFailStep := -100 // a failed proactive compaction is retried 5 steps later, not every step

turnLoop:
	for step := 1; ; step++ {
		if ctx.Err() != nil {
			a.append(Message{Role: "user", Content: "<system-reminder>The user interrupted the previous response.</system-reminder>"})
			stop = StopInterrupted
			return stop, ctx.Err()
		}
		if a.obSoftStopped() { // deadline grace: no new request (obs.go)
			stop = StopTimeout
			return stop, nil
		}
		a.obStep(step, turnModel)
		if a.rcTakeReset(lt, step) { // a handoff replaced the history (agent_recover.go)
			nudgedVerify, nudgedTodo, nudgedEmpty, nudgedFake = false, false, false, false
			lastDenied, textConts, callTruncs, compactFailStep = false, 0, 0, -100
			a.bgOnReset() // fresh context: exploration cap and reread index start over (budget.go)
		}
		if len(a.Msgs) >= 4 && step-compactFailStep >= 5 && a.estTokens() > a.compactThreshold() {
			if err := a.Compact(ctx, ""); err != nil && ctx.Err() == nil {
				compactFailStep = step
				a.ui.Warn("compactação falhou: " + err.Error())
			}
		}
		if why := a.overBudget(); why != "" {
			a.ui.Warn("limite do turno atingido: " + why)
			a.append(Message{Role: "user", Content: "<system-reminder>" + why + " Stop now: give your final answer with what was done and what is left. Do not call any more tools.</system-reminder>"})
			stop = StopStepLimit
			return stop, nil
		}
		atLimit := step > maxSteps
		if atLimit {
			a.bgAtLimit()
			a.append(Message{Role: "user", Content: stepLimitReminder})
		}
		steps++
		if turnModel != a.Model && !stickSince.IsZero() && time.Since(stickSince) >= acStickyTTL {
			dbg("sticky", map[string]any{"expired": turnModel, "back_to": a.Model})
			turnModel, a.turnModel, stickSince = a.Model, a.Model, time.Time{}
		}
		boost := boostOut
		boostOut = false
		var resp *ChatResponse
		var used string
		var err error
		var pre *ChatResponse
		if step == 1 && !atLimit { // a write cut by a failed stream, left in the history (cutoff.go)
			var n int
			pre, n = a.ccResume(ctx, turnModel)
			steps += n
		}
		if pre != nil {
			resp, used = pre, turnModel
		} else {
			resp, used, err = a.chatWithFallback(ctx, turnModel, acChatOpt{noToolUse: atLimit, stream: true, boost: boost})
		}
		if used != "" {
			if used != a.Model && used != turnModel {
				stickSince = time.Now()
			}
			turnModel, a.turnModel = used, used
		}
		if resp != nil && err == nil {
			a.addUsage(resp.Usage)
		}
		if err != nil && ctx.Err() == nil && isOverflow(err) && len(a.Msgs) >= 4 {
			a.ui.Notice("contexto estourou — compactando e tentando de novo")
			if cerr := a.Compact(ctx, ""); cerr == nil {
				steps++
				resp, used, err = a.chatWithFallback(ctx, turnModel, acChatOpt{noToolUse: atLimit, stream: true, boost: boost})
				if used != "" {
					turnModel, a.turnModel = used, used
				}
				if resp != nil && err == nil {
					a.addUsage(resp.Usage) // bug 36: the retried response counts too
				}
			} else if ctx.Err() == nil {
				a.ui.Warn("compactação falhou: " + cerr.Error())
			}
		}
		if err != nil && ctx.Err() == nil && isOverflow(err) && a.shrinkLastUser(turnModel) {
			// a huge paste: compaction cannot help (nothing older to summarize)
			steps++
			resp, used, err = a.chatWithFallback(ctx, turnModel, acChatOpt{noToolUse: atLimit, stream: true, boost: boost})
			if used != "" {
				turnModel, a.turnModel = used, used
			}
			if resp != nil && err == nil {
				a.addUsage(resp.Usage)
			}
			if err != nil && ctx.Err() == nil && isOverflow(err) {
				// still too big: never leave it in history to break the next turns
				n := len(a.Msgs) - 1
				a.Msgs = a.Msgs[:n]
				a.sess.Rewind(n)
				a.lastPrompt, a.countedIdx = 0, 0
				a.ui.Warn("a mensagem não coube na janela do modelo nem cortada — tirei do histórico; mande um trecho menor ou ops num arquivo e peça pra ler")
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				if resp != nil && (strings.TrimSpace(resp.Text) != "" || resp.Reasoning != "" || len(resp.ToolCalls) > 0) {
					// partial calls kept as text (obs.go) and structured, for the
					// continuation on --resume (cutoff.go)
					a.append(Message{Role: "assistant", Content: a.obPartialText(resp), Reasoning: resp.Reasoning, PartialCalls: resp.ToolCalls})
				}
				a.append(Message{Role: "user", Content: "<system-reminder>The user interrupted the previous response.</system-reminder>"})
				stop = StopInterrupted
				return stop, ctx.Err()
			}
			if r := acPartialOnly(resp); r != nil { // the stream failed after output arrived: keep it (cutoff.go)
				a.append(Message{Role: "assistant", Content: ccPartialNote(r, err), Reasoning: r.Reasoning, PartialCalls: r.ToolCalls})
				a.append(Message{Role: "user", Content: "<system-reminder>The previous response stream failed before it finished.</system-reminder>"})
			}
			stop = StopError
			return stop, err
		}
		a.lastPrompt = resp.Usage.In + resp.Usage.Out
		if a.lastPrompt > 0 {
			a.countedIdx = len(a.Msgs) + 1
		}

		calls := resp.ToolCalls
		text := strings.TrimSpace(resp.Text) // bug 65: never store whitespace-only text
		if len(calls) == 0 && !atLimit && text != "" && a.cfg.ModelSettings(turnModel).Tools != "none" {
			if tc, rest := parseTextToolCalls(text, a.knownTool, !a.textMode(turnModel)); len(tc) > 0 {
				dbg("repair", map[string]any{"text_tool_calls": len(tc), "model": turnModel})
				calls, text = tc, rest
			}
		}
		for i := range calls { // aliases/case-fix for built-ins; MCP names stay verbatim
			if !a.mcp.Has(calls[i].Name) {
				calls[i].Name = CanonicalTool(calls[i].Name)
			}
		}
		// A write/edit cut inside its content: ask for the rest and splice it
		// instead of throwing the work away (cutoff.go).
		if resp.Truncated() && len(calls) > 0 && !atLimit && a.ccEnabled() {
			full, n, ok := a.ccContinue(ctx, turnModel, text, resp.Reasoning, calls)
			steps += n
			if ok {
				calls = full
				resp.Stop = "tool_calls"
			}
		}
		// The whole output budget went into reasoning, with no answer and no
		// call (aider-polyglot dot-dsl: 65 KB of thinking in 5 min). Replaying
		// that reasoning would bloat every later request and invite more of
		// it; it is dropped, and the next request is not given more room to
		// think (cutoff.go).
		thinkCut := resp.Truncated() && len(calls) == 0 && text == "" && strings.TrimSpace(resp.Reasoning) != "" && a.ccEnabled()
		if thinkCut {
			dbg("cutoff", map[string]any{"reasoning_only": len(resp.Reasoning)})
			a.append(Message{Role: "assistant", Content: ""})
		} else {
			a.append(Message{Role: "assistant", Content: text, Reasoning: resp.Reasoning, ToolCalls: calls, Extra: resp.Extra})
		}
		for _, c := range calls {
			if c.Name != "ask_user" { // a question is not an action (anti-fake guard)
				callsIssued++
			}
		}

		// Bugs 25/37: tool calls that come back after the step limit never run.
		if atLimit {
			a.refuseCalls(calls, "TOOL_NOT_EXECUTED: the step limit was reached, so no more tools run in this turn.")
			if len(calls) > 0 {
				a.ui.Warn("limite de passos atingido — chamadas finais não executadas")
			}
			stop = StopStepLimit
			return stop, nil
		}

		// The provider dropped a tool call cut at max_tokens (acToolCutErr).
		if resp.Stop == acStopToolCut && len(calls) == 0 {
			maxOut := a.outCap(turnModel, acChatOpt{boost: boost})
			a.ui.Warn("chamada de ferramenta cortada no limite de saída — pedindo uma menor")
			callTruncs++
			boostOut = true
			if callTruncs > 2 {
				stop = StopTruncated
				return stop, nil
			}
			a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>Your last tool call was cut off at the output token limit (max_tokens=%d) and did not run. Re-issue it smaller: for a big file write a short skeleton first, then add the rest with several edit calls.</system-reminder>", maxOut)})
			continue
		}

		// G1: output cut by the token limit.
		if resp.Truncated() {
			maxOut := a.outCap(turnModel, acChatOpt{boost: boost})
			boostOut = true
			if len(calls) > 0 {
				a.refuseCalls(calls, fmt.Sprintf("TOOL_NOT_EXECUTED: your response hit the output token limit (max_tokens=%d) so these arguments may be cut off. Re-issue the call; for big files write a skeleton first and add the rest with several edit calls.", maxOut))
				a.ui.Warn("resposta cortada no limite de saída — chamadas não executadas")
				callTruncs++
				if callTruncs > 2 {
					stop = StopTruncated
					return stop, nil
				}
				continue
			}
			if textConts < 2 {
				textConts++
				if thinkCut {
					boostOut = false
					a.ui.Warn("resposta cortada no limite de saída — só raciocínio, sem resposta")
					a.append(Message{Role: "user", Content: ccThinkCutReminder})
					continue
				}
				a.ui.Warn("resposta cortada no limite de saída")
				a.append(Message{Role: "user", Content: "<system-reminder>Your reply was cut off at the output limit. Continue exactly where you stopped.</system-reminder>"})
				continue
			}
			a.ui.Warn("resposta cortada no limite de saída")
			stop = StopTruncated
			return stop, nil
		}

		if len(calls) == 0 {
			// Harness guards: weak models stop early or skip verification.
			if text == "" && !nudgedEmpty {
				nudgedEmpty = true
				dbg("nudge", "empty")
				a.append(Message{Role: "user", Content: "<system-reminder>Your last reply was empty. Continue the task, or give your final answer.</system-reminder>"})
				continue
			}
			// Anti-fake guard: a final answer claiming actions with no tool call
			// in the whole turn is a pretend reply; nudge once, then flag it.
			if callsIssued == 0 && a.cfg.ModelSettings(turnModel).Tools != "none" && acClaimsAction(text) && !acRecap(input, a.Msgs) {
				if !nudgedFake {
					nudgedFake = true
					dbg("nudge", "fake_action")
					a.append(Message{Role: "user", Content: "<system-reminder>" + fakeActionNudge + "</system-reminder>"})
					continue
				}
				a.ui.Warn(unverifiedWarning)
				stop = StopUnverified
				return stop, nil
			}
			if open := a.openTodos(); open != "" && !nudgedTodo && !a.sub {
				nudgedTodo = true
				dbg("nudge", "todo")
				a.append(Message{Role: "user", Content: "<system-reminder>Your todo list still has unfinished items:\n" + open + "\nContinue working on them, or update the list (mark items completed or remove ones that are no longer needed) and explain why.</system-reminder>"})
				continue
			}
			if editedCode && !nudgedVerify && a.cfg.VerifyNudge() {
				nudgedVerify = true
				dbg("nudge", "verify")
				a.append(Message{Role: "user", Content: "<system-reminder>You changed code but did not run anything afterwards. If this project can be built, tested, linted or run, verify your change now with bash and fix any problem you caused. If verification isn't possible, say so in one line and finish.</system-reminder>"})
				continue
			}
			if steer := a.takeSteer(); len(steer) > 0 {
				a.append(Message{Role: "user", Content: strings.Join(steer, "\n\n")})
				continue
			}
			if a.qInject("final") { // queued steer messages (queue.go)
				continue
			}
			if !a.sub && stopHooks < 3 && a.Hooks.Has(HookStop) {
				hr := a.Hooks.Run(ctx, HookStop, "", map[string]any{"session_id": a.SessionID, "stop_hook_active": stopHooks > 0})
				a.hookWarnings(hr)
				if hr.Block && strings.TrimSpace(hr.Reason) != "" {
					stopHooks++
					a.ui.Notice("hook pediu pra continuar: " + oneLine(hr.Reason, 120))
					a.append(Message{Role: "user", Content: "<system-reminder>A Stop hook asked you to continue:\n" + hr.Reason + "</system-reminder>"})
					continue
				}
			}
			if !lastDenied { // --until / --review (agent_loop.go)
				if again, why := a.acStopGate(ctx, lt); again {
					continue
				} else if why != "" {
					stop = why
					return stop, nil
				}
			}
			if lastDenied {
				stop = StopDenied
			}
			return stop, nil
		}

		lastDenied = false
		var imgMsgs []Message
		flushImgs := func() {
			for _, m := range imgMsgs {
				a.append(m)
			}
			imgMsgs = nil
		}
		calls = rdRestoreCalls(calls) // placeholders the model echoed get their real value locally (redact.go)
		a.apBatchAsk(ctx, calls)      // several confirmations: one question (approve_batch.go)
		ps := a.ptBegin(ctx, calls)   // read-class neighbours run in parallel (partools.go)
		var fan saResults             // results of task calls run in parallel in this batch
		for i, tc := range calls {
			if ctx.Err() != nil {
				a.saRefuse(fan, calls[i:], "TOOL_NOT_STARTED: cancelled by the user before this tool ran.")
				flushImgs()
				a.append(Message{Role: "user", Content: "<system-reminder>The user interrupted while tools were running.</system-reminder>"})
				stop = StopInterrupted
				return stop, ctx.Err()
			}
			if i > 0 && a.obSoftStopped() { // deadline grace: the rest of the batch does not start (obs.go)
				a.saRefuse(fan, calls[i:], "TOOL_NOT_STARTED: the run deadline was reached before this tool ran; re-issue it if it is still needed.")
				flushImgs()
				stop = StopTimeout
				return stop, nil
			}
			ps.launchFrom(i)
			// consecutive task calls run together, results kept in order (subagent.go)
			fan = a.saFanoutAt(ctx, step, calls, i, fan)
			out, ran, denied, pre := fan.take(tc.ID)
			if !pre {
				a.ptIdx = i
				out, ran, denied = a.obExecTool(ctx, step, tc) // execTool + write-ahead + audit (obs.go)
				a.ptIdx = -1
			}
			if ran {
				toolsRun++
			}
			lastDenied = lastDenied || denied
			if ctx.Err() != nil && ran {
				out.Text += "\nTOOL_OUTCOME_UNKNOWN: interrupted by the user while running; check the state before retrying."
			}
			if tc.Name == "edit" || tc.Name == "write" {
				if !out.IsErr && isCodeFile(argPath(tc.Args)) {
					editedCode = true
				}
			}
			a.rcObserve(lt, tc, out, denied)
			if out.Blocked != "" && a.blockUX() { // spiral breaker (blockux.go)
				target := a.blockTarget(tc)
				report, rem := a.blockNote(target, out.Blocked)
				dbg("blocked", map[string]any{"tool": tc.Name, "target": target, "why": out.Blocked, "total": a.blk.total})
				if report != "" {
					a.append(Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: out.Text})
					a.saRefuse(fan, calls[i+1:], "TOOL_NOT_STARTED: the turn was stopped after repeated blocked attempts.")
					flushImgs()
					a.ui.Warn(report)
					a.BlockReport = report
					a.append(Message{Role: "user", Content: blockStopReminder(a.blockLabel(target))})
					stop = StopDenied
					return stop, nil
				}
				out.Text += rem
			}
			if tc.Name == "bash" {
				a.ui.Jobs(a.tools.RunningJobs())
				if editedCode {
					editedCode = false // something was run after the change
					nudgedVerify = true
				}
			}
			if out.Image != nil {
				// OpenAI tool messages cannot carry images: send them in a user
				// message after the whole batch of tool results.
				imgMsgs = append(imgMsgs, Message{Role: "user", Content: fmt.Sprintf("Image from %s %s:", tc.Name, toolDetail(tc.Name, tc.Args)), Images: []ImagePart{*out.Image}})
			}
			// loop detection: same (tool, args, result) repeated
			h := hashCall(tc.Name, tc.Args, out.Text)
			a.recent = append(a.recent, h)
			if len(a.recent) > 10 {
				a.recent = a.recent[1:]
			}
			rep := 0
			for _, r := range a.recent {
				if r == h {
					rep++
				}
			}
			if rep >= 6 {
				a.append(Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: out.Text})
				a.saRefuse(fan, calls[i+1:], "TOOL_NOT_STARTED: turn stopped because of a repetition loop.")
				flushImgs()
				a.ptEnd()
				if a.rcOnLoop(ctx, lt, tc.Name+" "+toolDetail(tc.Name, tc.Args)) {
					continue turnLoop // handed off to a clean context
				}
				a.ui.Warn("o modelo entrou em loop (mesma chamada 6×) — parei o turno")
				a.append(Message{Role: "user", Content: "<system-reminder>You were stuck repeating the same tool call. The turn was stopped. Next time, change approach.</system-reminder>"})
				stop = StopLoop
				return stop, nil
			}
			if rep >= 3 {
				out.Text += fmt.Sprintf("\n\n<system-reminder>You have made this exact call %d times with the same result. Stop repeating it: change your approach, or give the final answer.</system-reminder>", rep)
			}
			out.Text += a.acSteeringNote(tc, out)  // fileMatch steering, once per rule
			out.Text += a.dcHintNote(tc, out)      // API error on a pinned library: point to the docs tool (docs.go)
			out.Text += a.skToolNote(tc, out)      // built-in skill triggered by this result (skills.go)
			out.Text += a.uiLintNote(ctx, tc, out) // ui_lint findings after a UI edit (uilint.go)
			out.Text += a.bgAfterTool(tc, out)     // exploration counter, reread index (budget.go)
			out.Text += a.hgAfterTool(tc, out)     // test snapshot revert, hardcode check (guard_honest.go)
			if i == len(calls)-1 {
				out.Text += a.bgStepNote(step + 1) // step budget reminders at 70% / 100%
			}
			a.append(Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: out.Text})
		}
		flushImgs()
		a.ptEnd()
		a.obAfterStep(step) // traced checkpoint when this step changed files (obs.go)
		if ctx.Err() != nil {
			a.append(Message{Role: "user", Content: "<system-reminder>The user interrupted while tools were running.</system-reminder>"})
			stop = StopInterrupted
			return stop, ctx.Err()
		}
		if a.rcAfterBatch(ctx, lt) { // 8 tool errors in a row: hand off (agent_recover.go)
			continue
		}
		if steer := a.takeSteer(); len(steer) > 0 {
			a.append(Message{Role: "user", Content: "<system-reminder>The user sent this while you were working:</system-reminder>\n" + strings.Join(steer, "\n\n")})
		}
		a.qInject("tool") // queued steer messages, at the tool boundary (queue.go)
	}
}

// shrinkLastUser cuts the middle of an oversized newest user message (a big
// paste) so it fits the window of ref, keeping the full text in a spill file.
// It reports whether the message changed.
func (a *Agent) shrinkLastUser(ref string) bool {
	n := len(a.Msgs)
	if n == 0 || a.Msgs[n-1].Role != "user" {
		return false
	}
	m := &a.Msgs[n-1]
	w, out := a.limitsOf(ref)
	budget := max(2000, (w-out)/2*35/10) // half the room left, in chars
	if len(m.Content) <= budget {
		return false
	}
	m.Content = truncateHeadTail(m.Content, budget) + "\n[The user's message was cut in the middle: it did not fit the context window.]"
	a.sess.Rewind(n - 1)
	a.sess.Append(*m)
	a.lastPrompt, a.countedIdx = 0, 0
	a.ui.Warn("mensagem grande demais pra janela do modelo — cortei o meio (o texto inteiro ficou salvo num arquivo)")
	return true
}

// refuseCalls gives every call a synthetic result so the history stays valid.
func (a *Agent) refuseCalls(calls []ToolCall, text string) {
	for _, tc := range calls {
		a.append(Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: text})
	}
}

func (a *Agent) addUsage(u Usage) {
	a.turnUsage.Add(u)
	a.Usage.Add(u)
}

func (c *Config) VerifyNudge() bool { return c.NoVerifyNudge == false }

func (a *Agent) openTodos() string {
	var b strings.Builder
	for _, t := range a.tools.TodoList() {
		if t.Status != "completed" {
			fmt.Fprintf(&b, "- [%s] %s\n", t.Status, t.Content)
		}
	}
	return b.String()
}

func argPath(raw string) string {
	m, _, err := parseArgs(raw)
	if err != nil {
		return ""
	}
	for _, k := range []string{"path", "file_path", "file", "filename"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

var codeExt = map[string]bool{".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".rs": true, ".c": true, ".h": true,
	".cpp": true, ".cc": true, ".java": true, ".kt": true, ".rb": true, ".php": true, ".sh": true, ".bash": true, ".lua": true, ".swift": true,
	".cs": true, ".scala": true, ".zig": true, ".ex": true, ".exs": true, ".mjs": true, ".cjs": true, ".vue": true, ".svelte": true, ".dart": true, ".pl": true}

func isCodeFile(p string) bool { return codeExt[strings.ToLower(filepath.Ext(p))] }

func hashCall(name, args, result string) string {
	h := sha256.Sum256([]byte(name + "\x00" + args + "\x00" + result))
	return hex.EncodeToString(h[:8])
}

func isOverflow(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	b := strings.ToLower(ae.Body)
	if ae.Status != 400 && ae.Status != 413 && ae.Status != 422 {
		return false
	}
	return strings.Contains(b, "context_length") || strings.Contains(b, "prompt is too long") || strings.Contains(b, "too many tokens") ||
		(strings.Contains(b, "context") && (strings.Contains(b, "exceed") || strings.Contains(b, "too long") || strings.Contains(b, "maximum") || strings.Contains(b, "window") || strings.Contains(b, "length")))
}

// ---------- requests: retries, fallback chain, text tools ----------

type acChatOpt struct {
	msgs      []Message // nil = a.Msgs
	noToolUse bool      // tools stay in the request but the model must not call them
	stream    bool      // forward text/reasoning to the UI
	maxOut    int       // cap on MaxTokens (0 = model limit)
	boost     bool      // after a cutoff: raise MaxTokens toward the provider limit (cutoff.go)
	quiet     bool      // no "usando X" notices (compaction)
	label     string    // spinner label
	role      string    // router role: main | compact | sub (omni)
}

func (a *Agent) limiter() Limiter {
	if DefaultLimiter == nil {
		return noopLimiter{}
	}
	return DefaultLimiter
}

func (a *Agent) retryWait(err error, attempt int) time.Duration {
	if a.retryDelay != nil {
		return a.retryDelay(err, attempt)
	}
	return RetryDelay(err, attempt)
}

// textMode reports whether ref gets tools described in text instead of natively.
func (a *Agent) textMode(ref string) bool {
	switch a.cfg.ModelSettings(ref).Tools {
	case "text", "none":
		return true
	}
	return a.textRefs[ref]
}

func (a *Agent) markText(ref string) {
	if a.textRefs == nil {
		a.textRefs = map[string]bool{}
	}
	a.textRefs[ref] = true
	a.cfg.MarkTextTools(ref)
}

// textSystem is System plus the text-tools block, built once per System so
// the prompt stays byte-stable across requests.
func (a *Agent) textSystem() string {
	if a.textSys == "" || a.textSysFor != a.System {
		a.textSys = a.System + "\n" + acTextToolsBlock(a.toolSpecs())
		a.textSysFor = a.System
	}
	return a.textSys
}

// outCap is the MaxTokens a request to ref carries: the model limit, raised
// for one request after a cutoff (o.boost), lowered by o.maxOut.
func (a *Agent) outCap(ref string, o acChatOpt) int {
	_, maxOut := a.limitsOf(ref)
	if o.boost {
		if b := a.cfg.BoostedOut(ref); b > maxOut {
			maxOut = b
		}
	}
	if o.maxOut > 0 && (maxOut <= 0 || o.maxOut < maxOut) {
		maxOut = o.maxOut
	}
	return maxOut
}

func (a *Agent) buildRequest(ref, mid string, o acChatOpt) ChatRequest {
	maxOut := a.outCap(ref, o)
	ms := a.cfg.ModelSettings(ref)
	effort := ms.Effort
	if effort == "" {
		effort = a.cfg.Effort
	}
	msgs := o.msgs
	if msgs == nil {
		msgs = a.Msgs
	}
	if !a.cfg.SupportsImages(ref) {
		msgs = acStripImages(msgs, ref)
	}
	req := ChatRequest{Model: mid, System: a.System, Messages: msgs, MaxTokens: maxOut, Effort: effort, Temperature: ms.Temperature, NoToolUse: o.noToolUse}
	switch {
	case ms.Tools == "none":
		req.TextTools = true
		req.Messages = acTextHistory(msgs)
	case a.textMode(ref):
		req.TextTools = true
		req.System = a.textSystem()
		req.Messages = acTextHistory(msgs)
	default:
		req.Tools = a.toolSpecs()
	}
	return req
}

// acStripImages replaces images with a text note for models without vision.
func acStripImages(msgs []Message, ref string) []Message {
	has := false
	for _, m := range msgs {
		has = has || len(m.Images) > 0
	}
	if !has {
		return msgs
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		if len(m.Images) > 0 {
			m.Content = strings.TrimSpace(m.Content + fmt.Sprintf("\n[%d image(s) omitted: %s does not accept image input]", len(m.Images), ref))
			m.Images = nil
		}
		out[i] = m
	}
	return out
}

func acResetTime(ae *APIError) time.Time {
	if ae.RetryAfter > 0 {
		return time.Now().Add(ae.RetryAfter)
	}
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, time.UTC)
}

// priceUsage fills Usage.Cost from the price table when the provider did not.
func (a *Agent) priceUsage(ref string, u *Usage) {
	if u.Cost != 0 {
		return
	}
	if in, out, ok := a.cfg.Price(ref); ok {
		u.Cost = float64(u.In)*in + float64(u.Out)*out
	}
}

// acInteractiveFirstByte caps the first-byte deadline of a free model with a
// fallback when a person is waiting (NetOffline = interactive).
const acInteractiveFirstByte = 20 * time.Second

// chatWithFallback sends one request with retries and the model fallback chain
// (G4). It returns the model actually used, which stays sticky for the turn.
func (a *Agent) chatWithFallback(ctx context.Context, model string, o acChatOpt) (*ChatResponse, string, error) {
	role := o.role
	if role == "" {
		role = "main"
		if a.sub {
			role = "sub"
		}
	}
	maxOut := a.outCap(model, o)
	need := Need{Tokens: a.estTokens() + maxOut, Tools: !a.textMode(model), Role: role, Prefer: model}
	refs := []string{model, a.Model}
	if role == "compact" && (a.cfg.Omni.Enabled || isOmniRef(a.Model)) {
		refs = append(refs, "omni/long")
	}
	cands := a.acDemoteStalled(a.ojOrder(role, a.bgOrderCands(a.cfg.CandidatesFor(need, refs...))))
	if a.FastFirst && role == "main" && model == a.Model {
		cands = a.acFastFirst(cands)
	}
	if len(cands) == 0 {
		cands = []string{model}
	}
	lim := a.limiter()
	label := o.label
	if label == "" {
		label = "pensando"
	}
	var lastErr error
	var lastPartial *ChatResponse // output of a stream that failed, kept if nothing better comes (cutoff.go)
	why := ""                     // why the previous candidate was left (shown with the next one)
	for ci, ref := range cands {
		if ok, wait := lim.Allow(ref); !ok && ci+1 < len(cands) {
			lastErr = fmt.Errorf("%s: limite de uso (libera em %s)", ref, wait.Round(time.Second))
			continue
		}
		pname, mid := SplitModel(ref)
		prov, err := a.cfg.NewProvider(pname)
		if err != nil {
			lastErr = err
			if ci == 0 {
				a.ui.Warn(err.Error())
			}
			continue
		}
		if cp, ok := prov.(*CodexProvider); ok {
			cp.SessionID = a.SessionID
		}
		a.privacyNotice(pname) // F4: first request to a provider that may train on prompts
		// A free model with another candidate after it must start streaming
		// quickly; a stall before the first byte moves on instead of retrying.
		cctx, fb := ctx, time.Duration(0)
		if ci+1 < len(cands) {
			fb = a.cfg.FirstByte(ref)
			if a.NetOffline && fb > 0 {
				fb = min(fb, acInteractiveFirstByte) // a person is waiting: move on sooner
			}
			cctx = WithFirstByte(ctx, fb)
		}
		if ph, ok := a.ui.(uiPhaser); ok && !o.quiet {
			r := ref
			cctx = WithPhase(cctx, func(p string) { ph.Phase(r, p) })
		}
		if ci > 0 && !o.quiet {
			// say why the model changed and to what (honest fallback)
			if why != "" {
				a.ui.Warn(why + " → tentando " + uiShortModel(ref))
			} else {
				a.ui.Notice("tentando " + uiShortModel(ref))
			}
		}
		why = ""
		triedText, netWaits := false, 0
	attempts:
		for attempt := 0; attempt < 6; attempt++ {
			req := a.buildRequest(ref, mid, o)
			a.ui.Thinking(label)
			var gotText bool
			var streamed strings.Builder // text already shown (acToolCutErr)
			var fn StreamFn
			if o.stream {
				fn = func(kind, s string) {
					if kind == "reasoning" {
						a.ui.StreamReasoning(s)
						return
					}
					streamed.WriteString(s)
					if !gotText {
						gotText = true
						if k, ok := a.ui.(uiStatusKeeper); !ok || !k.KeepsStatus() {
							a.ui.StopThinking()
						}
					}
					a.ui.StreamText(s)
				}
			}
			tReq := time.Now()
			a.obReqStart(ref, ci, attempt) // obs.go
			resp, err := prov.Chat(cctx, req, fn)
			a.obReqEnd(ref, ci, attempt, resp, err)
			a.rdNotice() // first redacted secret: one-time notice (redact.go)
			a.ui.StopThinking()
			if err == nil {
				bgScoreOK(ref, time.Since(tReq))
				if resp == nil {
					resp = &ChatResponse{}
				}
				if resp.Refused() {
					// a policy decline is an answer, not an outage: another
					// fallback model would silently change who answers
					why := resp.Refusal
					if why == "" {
						why = firstSentence(strings.TrimSpace(resp.Text))
					}
					dbg("refusal", map[string]any{"model": ref, "stop": resp.Stop, "why": oneLine(why, 200)})
					return nil, ref, &RefusalError{Ref: ref, Reason: why}
				}
				a.priceUsage(ref, &resp.Usage)
				if ci > 0 && !o.quiet {
					a.ui.Notice(fmt.Sprintf("usando %s por enquanto (o modelo principal volta a ser tentado em %s)", ref, acStickyTTL.Round(time.Second)))
				}
				return resp, ref, nil
			}
			if ctx.Err() != nil {
				return resp, ref, err
			}
			if r := acPartialOnly(resp); r != nil {
				lastPartial = r
			}
			if acToolCutErr(err) {
				// The provider rejected the reply because a tool call hit
				// max_tokens (Poolside via OpenRouter: 502 wrapping a 400). Same
				// as a truncated reply: the loop asks for a smaller call.
				dbg("repair", map[string]any{"tool_cut_error": oneLine(err.Error(), 200), "model": ref})
				cr := &ChatResponse{Text: streamed.String(), Stop: acStopToolCut}
				if resp != nil { // partial tool calls streamed before the error (cutoff.go)
					cr.ToolCalls, cr.Reasoning, cr.Extra, cr.Usage = resp.ToolCalls, resp.Reasoning, resp.Extra, resp.Usage
					if cr.Text == "" {
						cr.Text = resp.Text
					}
				}
				return cr, ref, nil
			}
			lastErr = err
			lim.Report(ref, err)
			var ae *APIError
			if errors.As(err, &ae) {
				if isOverflow(err) {
					return nil, ref, err
				}
				if !triedText && !req.TextTools && len(req.Tools) > 0 && acToolsUnsupported(ae) {
					triedText = true
					a.markText(ref)
					a.ui.Notice(ref + " não aceita tools nativas — usando tools em modo texto")
					continue
				}
				if ae.Daily {
					MarkExhausted(ref, acResetTime(ae))
					a.ui.Warn(fmt.Sprintf("%s: cota diária esgotada", ref))
					break attempts
				}
				if ae.Status == 401 || ae.Status == 403 {
					a.ui.Warn(fmt.Sprintf("%s recusou a credencial (%d)", pname, ae.Status))
					break attempts // next candidate
				}
				if ae.Status == 400 || ae.Status == 404 || ae.Status == 402 || ae.Status == 422 {
					a.ui.Warn(fmt.Sprintf("%s: %s", ref, firstSentence(ae.Error())))
					break attempts
				}
				if !ae.Retryable() {
					// classified as "try another model" (413 too big, model gone,
					// no tool support …): RetryDelay 0 means next candidate now
					if ci+1 < len(cands) && a.retryWait(err, attempt) == 0 {
						a.ui.Warn(fmt.Sprintf("%s: %s", ref, firstSentence(ae.Error())))
						break attempts
					}
					return acPartialOnly(resp), ref, err
				}
			} else if !errors.Is(err, errStreamIdle) {
				return acPartialOnly(resp), ref, err
			}
			if gotText { // partial answer already on screen; don't silently retry
				return acPartialOnly(resp), ref, err
			}
			// Interactive: no network at all ends the turn now, and the app
			// switches to offline mode (netfast.go, offline.go).
			if a.NetOffline && a.acNetDown(ctx, ref, err, cands) {
				dbg("netwait", map[string]any{"ref": ref, "offline": true, "err": oneLine(err.Error(), 200)})
				return nil, ref, errNoNet
			}
			// WiFi outage: every candidate would fail the same way; wait for the
			// network instead of burning the chain (netwait.go).
			if netWaits < bgNetMaxRounds && a.bgNetWait(ctx, ref, err, cands) {
				netWaits++
				if ctx.Err() != nil {
					return nil, ref, ctx.Err()
				}
				attempt-- // the outage does not count as an attempt
				continue
			}
			if errors.Is(err, errFirstByte) {
				bgScoreStall(ref)
				if a.stalled == nil {
					a.stalled = map[string]time.Time{}
				}
				a.stalled[ref] = time.Now()
				dbg("stall", map[string]any{"ref": ref, "first_byte_s": fb.Seconds()})
				msg := fmt.Sprintf("%s não respondeu em %s", uiShortModel(ref), fb.Round(time.Second))
				if ci+1 < len(cands) {
					why = msg // printed with the next candidate's name
				} else if !o.quiet {
					a.ui.Warn(msg)
				}
				break attempts // next candidate now
			}
			wait := a.retryWait(err, attempt)
			if wait <= 0 {
				break
			}
			if wait > 20*time.Second && ci+1 < len(cands) {
				break // a long wait: try the next model now
			}
			a.ui.Notice(fmt.Sprintf("%s: %s — tentando de novo em %s", ref, firstSentence(err.Error()), wait.Round(time.Second)))
			select {
			case <-ctx.Done():
				return nil, ref, ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("nenhum modelo disponível — configure uma chave (/key) ou faça login (/login)")
	}
	return lastPartial, model, lastErr
}

// acPartialOnly keeps a reply that failed mid-stream (text and cut tool calls
// already received) so the loop can persist it; anything else is dropped.
func acPartialOnly(r *ChatResponse) *ChatResponse {
	if r != nil && r.Partial && (strings.TrimSpace(r.Text) != "" || r.Reasoning != "" || len(r.ToolCalls) > 0) {
		return r
	}
	return nil
}

// acStopToolCut marks a reply rebuilt from a "tool call cut by max_tokens"
// provider error (acToolCutErr).
const acStopToolCut = "tool_cut"

// acToolCutErr: the provider failed the request because the model's tool call
// ran into max_tokens ("Tool calls cutoff by max_tokens"). Retrying the same
// request gives the same cut; the model must issue a smaller call instead.
func acToolCutErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "max_tokens") && (strings.Contains(s, "cutoff") || strings.Contains(s, "cut off") || strings.Contains(s, "truncat")) && strings.Contains(s, "tool")
}

// ---------- tool execution and permissions ----------

// execTool runs one call through hooks, permissions and the tool itself.
// ran = the tool actually executed; denied = refused by the user, a rule or a hook.
func (a *Agent) execTool(ctx context.Context, tc ToolCall) (out ToolOutput, ran, denied bool) {
	tc, o, denied, ok := a.execGate(ctx, tc)
	if !ok {
		return o, false, denied
	}
	a.ui.ToolStart(tc.ID, tc.Name, toolDetail(tc.Name, tc.Args))
	t0 := time.Now()
	tc, out, d := a.execBody(ctx, tc)
	if d < 0 {
		d = time.Since(t0)
	}
	return a.execFinish(ctx, tc, out, d), true, false
}

// execGate runs everything that may refuse a call before it starts: PreToolUse
// hooks (which may rewrite the arguments), the honest-completion and budget
// guards and the permission check. ok=false: o is the refusal (already shown).
func (a *Agent) execGate(ctx context.Context, tc ToolCall) (_ ToolCall, o ToolOutput, denied, ok bool) {
	detail := toolDetail(tc.Name, tc.Args)
	isMCP := a.mcp.Has(tc.Name)
	refuse := func(text, summary string) ToolOutput {
		o := ToolOutput{IsErr: true, Text: text, Display: Display{Summary: summary}}
		a.ui.ToolStart(tc.ID, tc.Name, detail)
		a.ui.ToolEnd(tc.ID, tc.Name, o, 0)
		return o
	}
	if !isMCP && strings.HasPrefix(tc.Name, "mcp__") {
		return tc, refuse(fmt.Sprintf("MCP tool %q is not available (server not connected).", tc.Name), "MCP indisponível"), false, false
	}
	const notStarted = "TOOL_NOT_STARTED: cancelled by the user before this tool ran."
	var hook acHookVerdict
	if a.Hooks.Has(HookPreToolUse) {
		hr := a.Hooks.Run(ctx, HookPreToolUse, tc.Name, map[string]any{"session_id": a.SessionID, "tool_input": acArgsMap(tc.Name, tc.Args)})
		a.hookWarnings(hr)
		if ctx.Err() != nil {
			// Esc while a slow hook ran: the tool must not start (fail closed)
			return tc, refuse(notStarted, "cancelado"), false, false
		}
		if hr.Block {
			a.obSetGuard("hook")
			why := strings.TrimSpace(hr.Reason)
			if why == "" {
				why = "no reason given"
			}
			if a.blockUX() {
				o := refuse(blockMsg(blockWhat(tc.Name, detail), "a PreToolUse hook blocked it ("+oneLine(why, 160)+")"), "bloqueado por hook")
				o.Blocked = "um hook bloqueou: " + oneLine(why, 160)
				return tc, o, true, false
			}
			return tc, refuse("Blocked by a PreToolUse hook: "+why, "bloqueado por hook"), true, false
		}
		if u := strings.TrimSpace(hr.UpdatedInput); u != "" && json.Valid([]byte(u)) {
			tc.Args = u
			detail = toolDetail(tc.Name, tc.Args)
		}
		hook = acHookVerdict{approve: hr.Approve, ask: hr.Ask, reason: strings.TrimSpace(hr.Reason)}
	}
	if ctx.Err() != nil {
		return tc, refuse(notStarted, "cancelado"), false, false
	}
	if o, hit := a.hgPreTool(tc); hit { // test protection, checker tamper (guard_honest.go)
		a.obSetGuard("honest")
		if a.blockUX() {
			// steering text, but not counted by the spiral breaker: the model is
			// expected to go on and fix the code, and stopping would fail the task
			o.Text = "BLOCKED: " + o.Text + " " + blockSteer
		}
		a.ui.ToolStart(tc.ID, tc.Name, detail)
		a.ui.ToolEnd(tc.ID, tc.Name, o, 0)
		return tc, o, false, false
	}
	if o, hit := a.bgPreTool(tc); hit { // reread pointer, write->edit, exploration cap (budget.go)
		a.obSetGuard("budget")
		a.ui.ToolStart(tc.ID, tc.Name, detail)
		a.ui.ToolEnd(tc.ID, tc.Name, o, 0)
		return tc, o, false, false
	}
	a.blockWhy = ""
	if allowed, why, userDenied := a.permit(ctx, tc, detail, isMCP, hook); !allowed {
		o := refuse(why, "negado")
		if a.blockUX() && strings.HasPrefix(why, "BLOCKED: ") {
			o.Blocked = a.blockWhy
			if o.Blocked == "" {
				o.Blocked = "negado"
			}
		}
		return tc, o, userDenied, false
	}
	if ctx.Err() != nil {
		// cancelled while the permission prompt (or a slow check) was open
		return tc, refuse(notStarted, "cancelado"), false, false
	}
	return tc, ToolOutput{}, false, true
}

// execBody runs an admitted call (the guarded write/edit path locks of
// parallel subagents first). It returns the call as run (an install command
// may have been hardened), its output, and how long it ran when the result
// was computed early by a parallel batch (-1 otherwise, partools.go).
func (a *Agent) execBody(ctx context.Context, tc ToolCall) (ToolCall, ToolOutput, time.Duration) {
	isMCP := a.mcp.Has(tc.Name)
	var out ToolOutput
	runDur := time.Duration(-1)
	switch {
	case tc.Name == "task" && !isMCP:
		out = a.runTask(ctx, tc)
	case tc.Name == "ask_user" && !isMCP:
		out = a.runAskUser(ctx, tc)
	case tc.Name == ojSidekickTool && !isMCP && a.ojFusionActive():
		out = a.ojRunSidekick(ctx, tc)
	case isMCP:
		out = a.mcp.Call(ctx, tc.Name, tc.Args)
		out.Text = fmt.Sprintf("<untrusted_tool_output server=%q>\n%s\n</untrusted_tool_output>", acMCPServerFor(tc), out.Text)
	default:
		if msg := a.saLockPath(ctx, tc); msg != "" { // a sibling subagent owns the file (subagent.go)
			return tc, ToolOutput{IsErr: true, Text: msg, Display: Display{Summary: "arquivo de outro subagente"}}, -1
		}
		var locks map[string]map[string]string
		lockCmd, hardenNote := "", ""
		if tc.Name == "bash" {
			if c, _ := argStr(acArgsMap(tc.Name, tc.Args), "command"); c != "" {
				if hardened, note := sgHardenInstall(c); note != "" && a.sgEnabled() {
					tc.Args = acReplaceCommand(tc.Args, c, hardened)
					hardenNote = note
				}
				if sgIsInstall(c) {
					lockCmd, locks = c, sgLockSnapshot(a.tools.lockDir())
				}
			}
		}
		if early, d, ok := a.ptTake(ctx, a.ptIdx, tc); ok { // computed in parallel (partools.go)
			out, runDur = early, d
			a.ptLane = ptLanePar
		} else {
			out = a.tools.Exec(ctx, tc.Name, tc.Args)
		}
		if hardenNote != "" {
			out.Text += "\n\n" + hardenNote
		}
		if lockCmd != "" {
			if d := sgLockDiff(locks, sgLockSnapshot(a.tools.lockDir())); d != "" {
				out.Text += "\n\n[lockfile changes]\n" + d
				out.Display.Summary = strings.TrimSpace(out.Display.Summary + " · lockfile mudou")
				a.ui.Notice("lockfile: " + oneLine(d, 200))
			}
		}
	}
	return tc, out, runDur
}

// execFinish is what follows every call that ran: the injection detector,
// the UI line and the PostToolUse hooks.
func (a *Agent) execFinish(ctx context.Context, tc ToolCall, out ToolOutput, d time.Duration) ToolOutput {
	isMCP := a.mcp.Has(tc.Name)
	a.grMarkResult(tc.Name, isMCP, &out) // prompt-injection detector (guardrails.go)
	a.ui.ToolEnd(tc.ID, tc.Name, out, d)
	if a.Hooks.Has(HookPostToolUse) {
		hr := a.Hooks.Run(ctx, HookPostToolUse, tc.Name, map[string]any{"session_id": a.SessionID, "tool_input": acArgsMap(tc.Name, tc.Args), "tool_response": out.Text})
		a.hookWarnings(hr)
		if c := strings.TrimSpace(hr.Context); c != "" {
			out.Text += "\n\n<system-reminder>PostToolUse hook:\n" + c + "\n</system-reminder>"
		}
		if hr.Block && strings.TrimSpace(hr.Reason) != "" {
			out.Text += "\n\n<system-reminder>PostToolUse hook: " + hr.Reason + "</system-reminder>"
		}
	}
	return out
}

// acMCPServer extracts the server part of mcp__<server>__<tool>.
func acMCPServer(name string) string {
	s := strings.TrimPrefix(name, "mcp__")
	if i := strings.Index(s, "__"); i > 0 {
		return s[:i]
	}
	return s
}

func acArgsMap(tool, raw string) map[string]any {
	m, _, err := parseArgs(raw)
	if err != nil || m == nil {
		return map[string]any{}
	}
	return normalizeArgs(CanonicalTool(tool), m)
}

// acHookVerdict is what PreToolUse hooks said about a call.
type acHookVerdict struct {
	approve bool   // permissionDecision "allow" / decision "approve": skip the prompt
	ask     bool   // permissionDecision "ask": always confirm, no [t]
	reason  string // the hook's reason, shown in the prompt
}

// permit applies, in order: subagent policy, deny rules, yolo, plan mode, and
// the prompt rules (danger, protected paths, hook "ask", ask rules, ask mode,
// then the smart-mode classifier when nothing else prompted).
// It returns whether the call may run, the refusal text for the model, and
// whether the refusal came from the user or a rule (for StopDenied).
func (a *Agent) permit(ctx context.Context, tc ToolCall, detail string, isMCP bool, hook acHookVerdict) (bool, string, bool) {
	tool := tc.Name
	args := acArgsMap(tool, tc.Args)
	cmd := ""
	if tool == "bash" {
		cmd, _ = argStr(args, "command")
		cmd = strings.TrimSpace(cmd)
	}
	fetchURL := ""
	if tool == "fetch" && !isMCP {
		fetchURL, _ = argStr(args, "url")
		fetchURL = strings.TrimSpace(fetchURL)
	}
	jobKill := tool == "bash" && cmd == "" && strings.EqualFold(fmt.Sprint(args["action"]), "kill")
	if a.sub && !isMCP && !a.ojIsSidekick() { // the fusion sidekick may edit (judge_fusion.go)
		switch tool {
		case "write", "edit", "todo", "task":
			if a.sa.work && (tool == "write" || tool == "edit") {
				break // a worker subagent edits through the normal rules (subagent.go)
			}
			return false, "The research subagent is read-only: do not modify files. Report what should change instead.", false
		case "bash":
			if a.sa.work {
				break
			}
			if jobKill || (cmd != "" && (!IsReadOnly(cmd) || DangerReason(cmd) != "")) {
				return false, "The research subagent may only run read-only commands (rg, grep, ls, cat, git log/diff/show …). Use read or a read-only command instead.", false
			}
		}
	}
	what := blockWhat(tool, detail)
	if tool == "bash" && cmd != "" {
		what = blockWhat(tool, cmd)
	}
	if !isMCP {
		if p := a.jazzPrivateToolAccess(tool, args, cmd); p != "" {
			why := "model tool access to Jazz private configuration, credentials, trust data, or sessions is blocked (" + p + ")"
			if a.blockUX() {
				a.blockWhy = why
				return false, blockMsg(what, why), true
			}
			return false, "Blocked: " + why + ". Use Jazz's built-in configuration or session features instead.", true
		}
	}
	dec, rule := a.Perms.Check(tool, args)
	if dec == PermDeny {
		if a.blockUX() {
			a.blockWhy = "regra de permissão " + rule
			return false, blockMsg(what, "permission rule "+rule+" forbids it"), true
		}
		return false, fmt.Sprintf("Blocked by permission rule %s. Do not retry it; choose another approach or ask the user.", rule), true
	}
	if a.sub && isMCP && dec != PermAllow && !a.mcp.ReadOnly(tool) {
		// the subagent is advertised as side-effect free (mirrors the plan-mode rule)
		return false, "The research subagent may not call MCP tools that can change state. Report what should be done instead.", false
	}
	// work-dir fence (fence.go): outside the project asks, jazz's own config never
	fenceReason, fenceEN := "", ""
	if out := a.fenceCheck(tool, args, cmd, isMCP); out != "" {
		if fencePrivate(resolvePath(out)) {
			fenceReason = "configuração/dados do jazz (credenciais, sessões): " + out
			fenceEN = "it reaches jazz's own config/data directory (" + out + "), which holds credentials and sessions"
		} else {
			fenceReason = "fora da pasta do projeto: " + out
			fenceEN = "it reaches outside the project root (" + out + "); tools stay inside the project unless the user runs jazz with --allow-outside"
		}
	}
	// build targets, repo scripts and archives the command would run are read and
	// classified like direct commands (buildtarget.go): CRITICAL blocks in every
	// mode, HIGH asks in every mode, a clean target never prompts
	btHigh := ""
	if tool == "bash" && !isMCP && cmd != "" {
		var btCrit string
		if btCrit, btHigh = btCheckWithSupply(a.tools.lockDir(), cmd, a.cfg.EgressAllow, func(nested, nestedCwd string) string {
			why, _ := a.sgCheckCommandIn(ctx, nested, dec == PermAllow, nestedCwd)
			return why
		}); btCrit != "" {
			if a.blockUX() {
				a.blockWhy = btCrit
				return false, blockMsg(what, "the build target or script it runs is hostile: "+btCrit), true
			}
			return false, "Blocked: the build target or script it runs is hostile (" + btCrit + "). Do not retry it; choose another approach or ask the user.", true
		}
	}
	// menor intervenção (caps.go): writing /etc, the kernel, system binaries or
	// container-engine/WSL configuration is CRITICAL in every mode (yolo/turbo too)
	if !isMCP {
		goal := ""
		if a.oj != nil {
			goal = a.oj.input
		}
		if crit := cbMinimalIntervention(tool, args, cmd, goal); crit != "" {
			if a.blockUX() {
				a.blockWhy = crit
				return false, blockMsg(what, crit), true
			}
			return false, "Blocked: " + crit + ". Do not retry it or work around it: do the task inside the project, or tell the user what they would have to change themselves.", true
		}
	}
	mode := a.mode()
	sensitiveEgress := tool == "bash" && !isMCP && jazzSensitiveEgress(cmd, a.tools.Root, a.fenceCwd())
	// turbo (turbo.go) applies the auto rules plus its own relaxations, and
	// only while an OS sandbox isolates what runs; without one it is auto
	turbo := mode == "turbo" && turboSandboxOK()
	if mode == "turbo" {
		mode = "auto"
	}
	autoRule := ""                           // why a call that some mode would confirm runs without a prompt (audit)
	apClass, apTarget, apLabel := "", "", "" // what "a" (sempre) remembers for a prompt with a reason (approvals.go)
	apOnAllow := false                       // turbo egress: the first approval already remembers the host
	// Approval memory is scoped to a risk class. A target-level HIGH finding
	// must not inherit an earlier egress or supply-chain "always" approval.
	if btHigh != "" {
		apClass, apTarget, apLabel, apOnAllow = "", "", "", false
	}
	if tool == "script" && !isMCP { // read-only scripts only in plan/subagent; Bash deny rules cover a script's cmd (scripts.go)
		if deny, byRule := a.scGate(args, mode); deny != "" {
			return false, deny, byRule
		}
	}
	// a suspected prompt injection earlier in this turn: anything that changes
	// something is confirmed, also in auto mode (and in yolo with strict)
	injReason := ""
	if inj := a.gr.injection; inj != "" && !a.sub {
		if im := a.injMode(); im != "off" && (mode != "yolo" || im == "strict") && a.grNonReadOnly(tool, cmd, fetchURL, isMCP, jobKill) && !a.injEditOK(tool, args, isMCP, im) {
			injReason = "possível prompt injection neste turno (" + inj + ")"
		}
	}
	if mode == "yolo" && injReason == "" && fenceReason == "" && btHigh == "" && !sensitiveEgress {
		return true, "", false
	}
	if mode == "plan" {
		switch {
		case tool == "write" || tool == "edit":
			return false, "Plan mode: do not modify files; present your plan instead.", false
		case tool == "bash" && (jobKill || (cmd != "" && !IsReadOnly(cmd))):
			return false, "Plan mode: only read-only commands are allowed. Present your plan instead.", false
		case isMCP && dec != PermAllow && !pfMCPMeta(tool): // resources/prompts only read
			return false, "Plan mode: MCP tools are disabled unless a permission rule allows them. Present your plan instead.", false
		case fetchURL != "" && dec != PermAllow:
			return false, "Plan mode: fetching URLs is disabled unless a WebFetch permission rule allows the domain; web search (query) is allowed. Present your plan instead.", false
		}
	}
	reason, whyEN := "", "" // whyEN: the same reason in English for the model (blockux.go)
	need := false
	switch {
	case tool == "bash":
		why := DangerReason(cmd)
		if cmd != "" && why != "" && turbo && a.tools.cpUsable() && tbGitRecoverable(cmd, a.tools.lockDir()) {
			why, autoRule = "", "turbo:git-discard-checkpoint" // /undo restores it (checkpoint.go)
		}
		if cmd != "" && why != "" {
			reason, need = why, true // dangerous commands are always confirmed
			whyEN = "it is a destructive or dangerous command (" + why + ")"
			if c, tg := tbDangerClass(cmd); c != "" {
				apClass, apTarget, apLabel = c, tg, c+": "+tg
			}
		} else if why := SecretReason(cmd); cmd != "" && why != "" && mode != "yolo" {
			reason, need = why, true // its output (credentials) would go to the provider
			whyEN = "it would read credentials or secrets (" + why + ")"
		} else if why := a.sgGuard(ctx, cmd, dec == PermAllow); cmd != "" && why != "" && mode != "yolo" {
			reason, need = why, true // supply-chain guard (guardrails.go)
			whyEN = "a package failed the supply-chain check (" + why + ")"
			apClass, apTarget = "supply", strings.Join(strings.Fields(cmd), " ")
			apLabel = "supply: " + apTarget
		} else if h, eg := EgressTarget(cmd); eg && (sensitiveEgress || mode != "yolo" && !egressAllowed(h, a.cfg.EgressAllow)) {
			reason, need = "envia dados para fora da máquina (destino: "+h+")", true // egress gate (AML.T0086)
			whyEN = "it sends data off this machine (to " + h + ")"
			if !strings.HasPrefix(h, "(") { // "(host no código inline)": no host to remember
				apClass, apTarget, apLabel = "egress", strings.ToLower(h), "egress: "+strings.ToLower(h)
				apOnAllow = turbo
			}
		} else if (mode == "ask" || a.UntrustedBash) && (jobKill || cmd != "" && !IsReadOnly(cmd) && !cbReadGranted(ctx, cmd)) {
			need = true
			if a.UntrustedBash {
				whyEN = "this folder is not trusted, so only read-only commands run without the user's approval"
			} else {
				whyEN = "ask mode needs the user's approval for commands that change things"
			}
		}
		if btHigh != "" && reason == "" {
			reason, need = "alvo de build ou script do projeto: "+btHigh, true
			whyEN = "the build target or script it runs does something risky (" + btHigh + ")"
		}
		if ctx.Err() != nil {
			return false, "TOOL_NOT_STARTED: cancelled by the user before this tool ran.", false
		}
	case tool == "read" && !isMCP:
		if p, _ := argStr(args, "path"); p != "" && secretPath(a.tools.abs(p)) {
			reason, need = "arquivo sensível (credenciais)", true
			whyEN = "the file holds credentials"
		}
	case (tool == "write" || tool == "edit") && !isMCP:
		if p, _ := argStr(args, "path"); p != "" && protectedPath(a.tools.abs(p), a.tools.Root) {
			reason, need = "arquivo sensível (credenciais ou configuração)", true
			whyEN = "it is a sensitive file (credentials, hooks or agent configuration)"
		} else if why := h2WriteDanger(args); why != "" { // hardening2.go
			reason, need = why, true
			whyEN = "it writes a compose file that gives a container host access (" + why + ")"
		} else if mode == "ask" {
			need = true
			whyEN = "ask mode needs the user's approval for file changes"
		}
	case (tool == "fetch" || tool == "docs") && !isMCP:
		need = mode == "ask"
		whyEN = "ask mode needs the user's approval"
	case tool == "script" && !isMCP:
		reason, need = a.scPrompt(args, mode)
		whyEN = "this script needs the user's approval"
	default:
		need = isMCP && mode == "ask"
		whyEN = "ask mode needs the user's approval"
	}
	if sensitiveEgress {
		// Sensitive input egress always gets a fresh, scoped confirmation. Never
		// inherit yolo, a permission rule, or an earlier remembered approval.
		if h, eg := EgressTarget(cmd); eg {
			reason, need = "envia arquivo/configuração sensível para fora da máquina (destino: "+h+")", true
			whyEN = "it sends a protected or secret local file off this machine (to " + h + ")"
			apClass, apTarget, apLabel, apOnAllow = "", "", "", false
		}
	}
	if isMCP && mode != "yolo" {
		if changed, _ := a.mcp.Poisoned(tool); changed {
			reason, need = "a descrição desta ferramenta MCP mudou desde que você confiou no servidor (rug-pull): confirme antes de usar", true
			whyEN = "the MCP tool's description changed since the user trusted the server"
		}
	}
	if fenceReason != "" && reason == "" {
		reason, need, whyEN = fenceReason, true, fenceEN
		if out := a.fenceCheck(tool, args, cmd, isMCP); out != "" && !fencePrivate(resolvePath(out)) {
			// Remember the exact fenced target. Saving its parent (especially
			// $HOME) made one approval silently cover unrelated siblings.
			target := filepath.Clean(out)
			apClass, apTarget, apLabel = "fence", target, "alvo exato fora do projeto: "+target
		}
	} else if fenceReason != "" {
		apClass = "" // two risks in one call: remembering one must not skip the other
	}
	if injReason != "" || hook.ask {
		apClass = "" // a suspected injection or a hook that asks: never answered from the store
	}
	if injReason != "" && reason == "" {
		reason, need = injReason, true
		whyEN = "a suspected prompt injection was read earlier in this turn, so actions that change things need the user's approval"
	}
	if hook.ask && reason == "" {
		reason, need = "hook pediu confirmação", true
		if hook.reason != "" {
			reason += ": " + hook.reason
		}
		whyEN = "a hook asked for the user's approval"
	}
	if dec == PermAsk {
		need = true
		if reason == "" {
			whyEN = "a permission rule requires the user's approval"
		}
	}
	if (dec == PermAllow || hook.approve) && reason == "" {
		if need {
			autoRule = "hook"
			if dec == PermAllow {
				autoRule = "perm:" + rule
			}
		}
		need = false
	}
	if isMCP && reason != "" {
		apClass = "" // rug-pull: never remembered
	}
	if mode == "ask" {
		apClass = "" // ask mode asks every time
	}
	if btHigh != "" {
		apClass, apTarget, apLabel, apOnAllow = "", "", "", false
	}
	if need && reason != "" && apClass != "" && dec != PermAsk && a.approvals().Has(apClass, apTarget) {
		need, autoRule = false, "approvals:"+apClass // "a" (sempre) earlier in this project
	}
	if mode == "smart" && !need && dec != PermAllow && !hook.approve {
		need = a.smartNeedsPrompt(ctx, tool, args, cmd, fetchURL, isMCP, jobKill)
		whyEN = "the safety classifier wants the user's approval"
		if ctx.Err() != nil {
			return false, "TOOL_NOT_STARTED: cancelled by the user before this tool ran.", false
		}
	}
	if !need {
		if a.apBatchCollecting() {
			return true, "", false
		}
		switch {
		case autoRule != "":
			a.apRecordAuto(tool, a.apDetailOf(tool, detail, cmd, args), autoRule)
		case turbo && a.tbIsChange(tool, cmd, isMCP, jobKill):
			a.apRecordAuto(tool, a.apDetailOf(tool, detail, cmd, args), "turbo:projeto+sandbox")
		}
		return true, "", false
	}
	// the approval shows the whole command (bug 10); toolDetail clips it
	apDetail := detail
	if tool == "bash" && cmd != "" {
		apDetail = cmd
	} else if tool == "script" && !isMCP {
		apDetail = scDetail(args, 0) // whole script call, cmd included
	}
	key := tool + "\x00" + apDetail
	if fetchURL != "" {
		key = tool + "\x00" + acFetchHost(fetchURL) // [t] for fetch is per domain
	}
	if reason == "" && a.allow[key] {
		return true, "", false
	}
	if a.apBatchCollecting() {
		a.apBatchAdd(tool, apDetail, reason)
		return false, "", false
	}
	if ap, ok := a.apBatchAnswer(tc.ID); ok {
		a.apb.answering = true
		defer func() { a.apb.answering = false }()
		return a.permitAnswer(ctx, tc, tool, args, key, apDetail, reason, whyEN, apClass, apTarget, apLabel, apOnAllow, ap, "")
	}
	if h, ok := a.ui.(approveHinter); ok && reason == "" && a.Perms != nil {
		scope := "neste projeto"
		if a.alwaysSession(tool) {
			scope = "nesta sessão"
		}
		if r := strings.Join(a.Perms.pfDeriveRules(tool, args), ", "); r != "" {
			h.ApproveHint(scope + " para " + r)
		} else {
			h.ApproveHint(scope)
		}
	}
	if reason != "" && dec == PermAsk {
		apClass = "" // an ask rule asks every time
	}
	if h, ok := a.ui.(approveAlwaysOffer); ok && reason != "" && apClass != "" {
		h.ApproveAlways(apLabel)
	}
	if h, ok := a.ui.(approveExtra); ok {
		h.ApproveExtra(apPreview(tool, args))
	}
	ap, feedback := a.ui.Approve(ctx, tool, apDetail, reason)
	return a.permitAnswer(ctx, tc, tool, args, key, apDetail, reason, whyEN, apClass, apTarget, apLabel, apOnAllow, ap, feedback)
}

// permitAnswer applies the user's answer to a confirmation (asked alone or
// merged with others, approve_batch.go): audit, remembered approvals and
// rules, and the refusal text for the model.
func (a *Agent) permitAnswer(ctx context.Context, tc ToolCall, tool string, args map[string]any, key, apDetail, reason, whyEN, apClass, apTarget, apLabel string, apOnAllow bool, ap Approval, feedback string) (bool, string, bool) {
	a.obApproval(tool, apDetail, reason, ap) // overrides are audited (obs.go)
	if reason != "" && apClass != "" && (ap == AllowAlways || ap == Allow && apOnAllow) {
		if err := a.approvals().Add(apClass, apTarget, apLabel); err != nil {
			a.ui.Warn("não consegui salvar a aprovação: " + err.Error())
		} else {
			a.ui.Notice("não pergunto de novo neste projeto: " + oneLine(visibleDetail(apLabel), 120) + "  (/approvals lista e limpa)")
		}
		return true, "", false
	}
	switch ap {
	case AllowAlways:
		if reason == "" && a.alwaysSession(tool) {
			if r := a.Perms.RememberSession(tool, args); r != "" {
				a.ui.Notice("não pergunto de novo nesta sessão: " + r)
			}
		} else if reason == "" {
			if r, err := a.Perms.Remember(tool, args); err != nil {
				a.ui.Warn("não consegui salvar a regra: " + err.Error())
			} else if r != "" {
				a.ui.Notice("regra salva neste projeto: " + r + "  (/permissions lista e remove)")
			}
		}
		if reason == "" {
			if a.allow == nil {
				a.allow = map[string]bool{}
			}
			a.allow[key] = true
		}
		return true, "", false
	case Allow:
		return true, "", false
	}
	if ctx.Err() != nil {
		return false, "TOOL_NOT_STARTED: cancelled by the user before this tool ran.", false
	}
	if fb := strings.TrimSpace(feedback); fb != "" {
		return false, "The user denied this and said: " + fb, true
	}
	if a.blockUX() {
		a.blockWhy = reason
		if a.blockWhy == "" {
			switch {
			case a.UntrustedBash && tool == "bash":
				a.blockWhy = "pasta não confiável: só comandos de leitura rodam sem confirmação"
			case a.Headless:
				a.blockWhy = "precisa de confirmação e o modo -p não pergunta"
			default:
				a.blockWhy = "você negou"
			}
		}
		why := whyEN
		if why == "" {
			why = "it needs the user's approval"
		}
		if a.Headless {
			why += "; nobody can approve it in this non-interactive run"
		} else {
			why = "the user denied it (" + why + ")"
		}
		return false, blockMsg(blockWhat(tool, apDetail), why), true
	}
	msg := "The user denied this action"
	if reason != "" {
		msg += " (" + reason + ")"
	}
	return false, msg + ". Do not retry it; ask the user or choose another approach.", true
}

// alwaysSession reports whether "Sim, e não perguntar de novo" lasts only
// for this session (config always_scope): "auto" (default) does what Claude
// Code does, edits and writes for the session, commands, domains and tools
// saved as a project rule (.jazz/settings.json).
func (a *Agent) alwaysSession(tool string) bool {
	scope := ""
	if a.cfg != nil {
		scope = strings.ToLower(a.cfg.AlwaysScope)
	}
	switch scope {
	case "session", "sessao", "sessão":
		return true
	case "project", "projeto":
		return false
	}
	return tool == "edit" || tool == "write"
}

// approveHinter is implemented by UIs that can show which rule [t] "always" saves.
type approveHinter interface{ ApproveHint(rule string) }

// acFetchHost is the lower-cased host of a fetch URL ("" when unparsable).
func acFetchHost(raw string) string {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func toolDetail(name, raw string) string {
	m, _, err := parseArgs(raw)
	if err != nil {
		return oneLine(raw, 100)
	}
	m = normalizeArgs(CanonicalTool(name), m)
	switch CanonicalTool(name) {
	case mcpResTool, mcpPromptTool:
		return mcpMetaDetail(CanonicalTool(name), m)
	case "read":
		p, _ := argStr(m, "path")
		if off, ok := argInt(m, "offset"); ok && off > 1 {
			p += fmt.Sprintf(":%d", off)
		}
		return p
	case "write", "edit":
		p, _ := argStr(m, "path")
		return p
	case "bash":
		c, _ := argStr(m, "command")
		if c == "" {
			j, _ := argStr(m, "job")
			act, _ := argStr(m, "action")
			return strings.TrimSpace("job " + j + " " + act)
		}
		return oneLine(c, 160)
	case "todo":
		return ""
	case "script":
		return scDetail(m, 160)
	case "ask_user":
		q, _ := argStr(m, "question")
		return oneLine(q, 120)
	case "task":
		d, _ := argStr(m, "description")
		if d == "" {
			d, _ = argStr(m, "prompt")
		}
		return oneLine(d, 100)
	}
	b, _ := json.Marshal(m)
	return oneLine(string(b), 120)
}

// ---------- compaction ----------

const compactPrompt = `<system-reminder>The conversation is getting long and will be compacted. Write a handoff summary of everything above so another instance of you can continue the work seamlessly. Do NOT call any tools. Use exactly this structure:

## Goal
What the user wants, in their own terms.
## Constraints & Preferences
Explicit instructions, style rules, things to avoid.
## Progress
### Done
### In Progress
### Blocked
## Key Decisions
Decisions taken and why.
## Next Steps
Ordered, concrete.
## Critical Context
Exact file paths, function names, commands, error messages, values and URLs needed to continue.

Preserve exact file paths, function names and error messages. Be dense; no filler.%s</system-reminder>`

const droppedHistoryNote = "(The earlier part of this conversation no longer fit in the context window and was dropped without a summary. If something important seems to be missing, ask the user.)"

// Compact summarizes older messages, keeping roughly the last 20k tokens verbatim.
// The summary request reuses the live prefix byte-for-byte so it hits the prompt
// cache, and goes through the same fallback chain as the turn (bugs 26, 36; G4).
func (a *Agent) Compact(ctx context.Context, focus string) error {
	if len(a.Msgs) < 4 {
		return errors.New("conversa curta demais pra compactar")
	}
	keepTokens := 20000
	if w := a.window(); w < 64000 {
		keepTokens = w / 6
	}
	cut, acc := len(a.Msgs), 0
	for cut > 1 {
		m := a.Msgs[cut-1]
		acc += (len(m.Content) + len(m.Reasoning)) * 10 / 35
		for _, tc := range m.ToolCalls {
			acc += len(tc.Args) * 10 / 35
		}
		if acc > keepTokens {
			break
		}
		cut--
	}
	for cut < len(a.Msgs) && a.Msgs[cut].Role == "tool" { // never start with orphan tool results
		cut++
	}
	if cut <= 1 {
		cut = len(a.Msgs) - 1
		for cut > 0 && a.Msgs[cut].Role == "tool" {
			cut--
		}
	}
	if cut <= 0 {
		return errors.New("nada pra compactar")
	}
	extra := ""
	if focus != "" {
		extra = "\nThe user asked to focus the summary on: " + focus
	}
	dbg("compact", map[string]any{"cut": cut, "msgs": len(a.Msgs), "model": a.TurnModel()})
	ask := Message{Role: "user", Content: fmt.Sprintf(compactPrompt, extra)}
	head := a.Msgs[:cut]
	var summary string
	for round := 0; ; round++ {
		prefix := append(append([]Message{}, head...), ask)
		s, err := a.summarize(ctx, prefix)
		if err == nil {
			summary = s
			break
		}
		if !isOverflow(err) || ctx.Err() != nil {
			return err
		}
		// The prefix itself overflows (bug 36): shrink it and try again.
		head = acShrinkHistory(head, round)
		if head == nil || round >= 3 {
			summary = droppedHistoryNote
			a.ui.Warn("não deu pra resumir o histórico antigo (estouro de contexto) — ele foi descartado")
			break
		}
	}
	kept := append([]Message{{Role: "user", Content: "<summary>\n" + summary + "\n</summary>\n<system-reminder>The earlier part of this conversation was compacted into the summary above. Continue from where it left off.</system-reminder>"}}, a.Msgs[cut:]...)
	offset := cut - 1 // Msgs[cut] lands at index 1
	if len(kept) > 1 && kept[1].Role == "user" {
		offset = cut // merged into the summary message
		kept[0].Content += "\n\n" + kept[1].Content
		kept[0].Images = append(kept[0].Images, kept[1].Images...)
		kept = append(kept[:1], kept[2:]...)
	}
	if r := a.acGoalReminder(); r != "" {
		kept[0].Content += "\n" + r // /goal survives compaction
	}
	a.Msgs = kept
	if !a.sub {
		a.tools.RebaseTurns(cut, offset) // /rewind checkpoints follow the new indexes
	}
	a.sess.Compact(summary, kept)
	a.lastPrompt, a.countedIdx = 0, 0
	a.ui.Notice(fmt.Sprintf("contexto compactado (~%dk tokens mantidos literais)", acc/1000))
	return nil
}

// summarize asks for the handoff summary (tools offered with NoToolUse, so
// histories with tool calls stay valid for every provider), retrying once when
// the model returns no text.
func (a *Agent) summarize(ctx context.Context, prefix []Message) (string, error) {
	if s, ok := a.ojHedgedSummary(ctx, prefix); ok { // two candidates, first answer wins (judge_hedge.go)
		return s, nil
	}
	model := a.TurnModel()
	for try := 0; try < 2; try++ {
		resp, used, err := a.chatWithFallback(ctx, model, acChatOpt{msgs: prefix, noToolUse: true, maxOut: 8000, quiet: true, label: "compactando contexto", role: "compact"})
		if err != nil {
			return "", err
		}
		model = used
		a.addUsage(resp.Usage)
		if s := strings.TrimSpace(resp.Text); s != "" {
			return s, nil
		}
	}
	return "", errors.New("o modelo devolveu um resumo vazio")
}

// acShrinkHistory makes a summary prefix smaller after an overflow: round 0
// clips big messages (tool output first), later rounds drop the oldest half
// (starting at a user message so tool calls stay paired). nil = cannot shrink.
func acShrinkHistory(head []Message, round int) []Message {
	if round == 0 {
		out := make([]Message, len(head))
		changed := false
		for i, m := range head {
			limit := 8000
			if m.Role == "tool" {
				limit = 2000
			}
			if len(m.Content) > limit {
				m.Content = truncateHeadTail(m.Content, limit)
				changed = true
			}
			if len(m.Reasoning) > 2000 {
				m.Reasoning = m.Reasoning[:2000]
				changed = true
			}
			if len(m.Images) > 0 {
				m.Images = nil
				m.Content += "\n[image omitted]"
				changed = true
			}
			out[i] = m
		}
		if changed {
			return out
		}
	}
	for i := len(head) / 2; i < len(head); i++ {
		if head[i].Role == "user" && i > 0 {
			return head[i:]
		}
	}
	return nil
}

// ---------- contract API (used by main.go / headless; see CONTRACT.md) ----------

// TurnModel is the model the current (or last) turn actually used, after any
// sticky fallback; it equals Model before the first turn.
func (a *Agent) TurnModel() string {
	if a.turnModel != "" {
		return a.turnModel
	}
	return a.Model
}

// Window is the context window of TurnModel.
func (a *Agent) Window() int { return a.window() }

// TokenEstimate is the estimated number of tokens currently in context.
func (a *Agent) TokenEstimate() int { return a.estTokens() }

// SetModel switches the primary model and records it in the session.
func (a *Agent) SetModel(ref string) {
	a.Model, a.turnModel = ref, ""
	a.FastFirst = false // the user chose: no reordering
	a.sess.Model(ref)
}

// Attach queues images for the next user message.
func (a *Agent) Attach(imgs ...ImagePart) { a.attach = append(a.attach, imgs...) }

// Close flushes and closes the session file.
func (a *Agent) Close() {
	a.sess.Close()
	if !a.sub {
		obCloseCur()
		otlpShutdown() // flush the OTLP exporter (otlp.go); no-op when off
	}
}

// LoadSession replaces the conversation with a saved session.
func (a *Agent) LoadSession(si SessionInfo) error { return a.LoadSessionAt(si, "") }

// LoadSessionAt loads a session with the leaf at entry at ("" = newest):
// the next message continues from there as a new branch (session.go).
func (a *Agent) LoadSessionAt(si SessionInfo, at string) error {
	sess, msgs, model, system, err := LoadSessionAt(si.Path, at)
	if err != nil {
		return err
	}
	a.sess.Close()
	if system == "" && sess.Tampered > 0 {
		system = a.System // a tampered meta line must not blank the system prompt (hardening2.go)
	}
	a.sess, a.Msgs, a.System, a.SessionID = sess, msgs, system, sess.ID
	a.h2ResumeNotice(sess)
	a.obAttach("resume") // obs.go
	a.loop.goal = acLoadGoal(si.Path)
	if model != "" && a.cfg.HasCreds(mustProvider(model)) {
		a.Model = model
	}
	a.turnModel = ""
	a.lastPrompt, a.countedIdx = 0, 0
	a.recent = nil
	// checkpoints, todos and read stamps belong to the previous conversation:
	// a /rewind here must never restore files from another session
	a.tools.Reset()
	a.pendingNote, a.attach = nil, nil
	return nil
}

// UserTurns lists the real user messages (newest last) for /rewind and /fork.
func (a *Agent) UserTurns() []TurnRef {
	var out []TurnRef
	for i, m := range a.Msgs {
		if m.Role != "user" || strings.HasPrefix(m.Content, "<summary>") {
			continue
		}
		t := strings.TrimSpace(acStripReminders(m.Content))
		if t == "" {
			continue
		}
		out = append(out, TurnRef{Index: i, Text: t})
	}
	return out
}

// RewindTo truncates the conversation to Msgs[:n] and restores files changed since.
func (a *Agent) RewindTo(n int) error {
	if n < 0 || n > len(a.Msgs) {
		return fmt.Errorf("ponto de rewind inválido: %d (conversa tem %d mensagens)", n, len(a.Msgs))
	}
	if n < len(a.Msgs) && a.Msgs[n].Role == "tool" {
		return fmt.Errorf("ponto de rewind inválido: %d cai no meio de uma chamada de ferramenta", n)
	}
	_, ferr := a.tools.RewindTo(n)
	a.Msgs = a.Msgs[:n]
	a.sess.Rewind(n)
	a.lastPrompt, a.countedIdx = 0, 0
	a.recent = nil
	a.turnModel = ""
	if ferr != nil {
		return fmt.Errorf("conversa voltou, mas nem todos os arquivos foram restaurados: %w", ferr)
	}
	return nil
}

// Fork copies the conversation up to Msgs[:n] into a new session and switches to it.
func (a *Agent) Fork(n int) (string, error) {
	if n < 0 || n > len(a.Msgs) {
		return "", fmt.Errorf("ponto de fork inválido: %d", n)
	}
	a.EnsureSystem()
	parent := a.SessionID
	s, err := NewSessionFrom(a.tools.Root, a.Model, a.System, parent)
	if err != nil {
		return "", err
	}
	msgs := repairHistory(append([]Message(nil), a.Msgs[:n]...))
	for _, m := range msgs {
		s.Append(m)
	}
	if a.loop.goal != "" {
		acWriteGoal(s, a.loop.goal)
	}
	// edits made after the fork point stay on disk; a later /rewind in the
	// fork must not revert them, so drop (without restoring) their checkpoints
	a.tools.DropTurnsFrom(n)
	a.sess.Close()
	a.sess, a.SessionID, a.Msgs = s, s.ID, msgs
	a.obAttach("fork") // obs.go
	a.lastPrompt, a.countedIdx = 0, 0
	a.recent = nil
	return s.ID, nil
}

// Export renders the conversation as "md" or "html".
func (a *Agent) Export(format string) (string, error) {
	md := exportMarkdown(a.SessionID, a.TurnModel(), a.Msgs)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "md", "markdown":
		return md, nil
	case "html":
		return exportHTML(a.SessionID, md), nil
	}
	return "", fmt.Errorf("formato de exportação desconhecido: %q (use md ou html)", format)
}

// ContextBreakdown estimates what fills the window, for /context.
func (a *Agent) ContextBreakdown() []ContextPart {
	tok := func(n int) int { return n * 10 / 35 }
	var parts []ContextPart
	sys := a.System
	sections := []struct{ label, open string }{
		{"sistema: instruções do projeto", "<project_instructions"},
		{"sistema: skills", "<skills>"},
		{"sistema: ambiente", "<environment>"},
	}
	rest := len(sys)
	for _, s := range sections {
		n := 0
		for i := 0; ; {
			j := strings.Index(sys[i:], s.open)
			if j < 0 {
				break
			}
			start := i + j
			closeTag := "</" + strings.TrimLeft(strings.Fields(s.open)[0], "<")
			closeTag = strings.TrimSuffix(closeTag, ">") + ">"
			k := strings.Index(sys[start:], closeTag)
			if k < 0 {
				break
			}
			n += k + len(closeTag)
			i = start + k + len(closeTag)
		}
		if n > 0 {
			parts = append(parts, ContextPart{Label: s.label, Tokens: tok(n)})
			rest -= n
		}
	}
	parts = append([]ContextPart{{Label: "sistema: base", Tokens: tok(max(rest, 0))}}, parts...)
	specLen := func(specs []ToolSpec) int {
		b, _ := json.Marshal(specs)
		return len(b)
	}
	parts = append(parts, ContextPart{Label: "ferramentas embutidas", Tokens: tok(specLen(append(append([]ToolSpec{}, a.tools.Specs()...), taskSpec)))})
	byServer := map[string][]ToolSpec{}
	var servers []string
	for _, s := range a.mcp.Specs() {
		srv := acMCPServer(s.Name)
		if _, ok := byServer[srv]; !ok {
			servers = append(servers, srv)
		}
		byServer[srv] = append(byServer[srv], s)
	}
	for _, srv := range servers {
		parts = append(parts, ContextPart{Label: "MCP " + srv, Tokens: tok(specLen(byServer[srv]))})
	}
	var user, asst, tools int
	type big struct {
		name string
		n    int
	}
	var bigs []big
	for _, m := range a.Msgs {
		n := len(m.Content) + len(m.Reasoning) + 3000*len(m.Images)
		for _, tc := range m.ToolCalls {
			n += len(tc.Args) + len(tc.Name)
		}
		switch m.Role {
		case "user":
			user += n
		case "assistant":
			asst += n
		case "tool":
			tools += n
			bigs = append(bigs, big{m.ToolName, n})
		}
	}
	parts = append(parts, ContextPart{"mensagens do usuário", tok(user)}, ContextPart{"respostas do modelo", tok(asst)}, ContextPart{"resultados de ferramentas", tok(tools)})
	for i := 0; i < len(bigs); i++ { // top 5 tool results, largest first
		for j := i + 1; j < len(bigs); j++ {
			if bigs[j].n > bigs[i].n {
				bigs[i], bigs[j] = bigs[j], bigs[i]
			}
		}
		if i >= 4 {
			bigs = bigs[:i+1]
			break
		}
	}
	for _, b := range bigs {
		parts = append(parts, ContextPart{Label: "  maior resultado: " + b.name, Tokens: tok(b.n)})
	}
	return parts
}

// acStripReminders removes <system-reminder> blocks from user text.
func acStripReminders(s string) string {
	for {
		i := strings.Index(s, "<system-reminder>")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "</system-reminder>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("</system-reminder>"):]
	}
}
