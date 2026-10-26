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
	"strconv"
	"strings"
	"time"
)

// brResult is the outcome of one request.
type brResult struct {
	Handled   bool // the brain answered (no model needed)
	Intent    Intent
	Task      *TaskState
	Status    tkStatus
	Text      string
	Questions int
	Commands  int
	Method    string
	Category  rvCat
	Elapsed   time.Duration
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
	if !in.Understood() {
		res.Text = brNotUnderstood(in)
		return res
	}
	lib, _ := mhLibrary()
	methods := lib[in.Name]
	if len(methods) == 0 {
		res.Text = fmt.Sprintf("entendi '%s' (%s), mas ainda não tenho um método offline pra isso", in.Name, nuGet().descOf(in.Name))
		return res
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
	brExecute(cx, methods)
	res.Status = task.Status
	res.Text = strings.TrimRight(cx.out.String(), "\n")
	if cx.stopWhy != "" && !strings.Contains(res.Text, cx.stopWhy) {
		if res.Text != "" {
			res.Text += "\n"
		}
		res.Text += cx.stopWhy
	}
	res.Questions, res.Commands, res.Category = cx.questions, cx.commands, cx.cat
	for _, d := range task.Decisions {
		if f := strings.Fields(d.What); len(f) == 2 && f[0] == "método" {
			res.Method = f[1] // the last method tried
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

func brNotUnderstood(in Intent) string {
	if in.Name != "none" && in.Confidence > 0 {
		return fmt.Sprintf("não tenho certeza do pedido (talvez %s, confiança %.2f)", in.Name, in.Confidence)
	}
	return "não entendi o pedido sem o modelo"
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
	if cx.proj.Git {
		f["project.git"] = "1"
	}
	if p != nil {
		if p.Online {
			f["env.online"] = "1"
		}
		for _, b := range []string{"git", "go", "python3", "node", "npm", "make", "cargo", "docker", "podman", "tailscale", "ssh"} {
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
	tried := 0
	for mi := 0; mi < len(methods); mi++ {
		m := methods[mi]
		if !mhApplicable(m, cx.facts) {
			continue
		}
		tried++
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
			fn := brCaps[st.Cap]
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
		// the last failure explains the stop better than "no method"
		_, _, then := rvDecide(cx.cat, 99)
		msg := rvTextPT(cx.cat)
		if cx.cat == rvCommandMissing {
			msg += "; posso oferecer a instalação (capacidade install_tool, com confirmação)"
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
		*next = true
		return false
	case act == rvRetrySmaller && !stop:
		cx.vars["retry_smaller"] = true
		return false
	case act == rvNoRetry && !stop:
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

// ---------- headless (jazz -p --offline) ----------

// brHeadless answers a -p request offline. nil = the brain did not take it.
func brHeadless(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, prompt string, o pfOpts) *RunResult {
	env := &brEnv{Root: tools.Root, Shell: tools.Shell, Permit: ag.PermitCommand}
	if ag.sess != nil {
		env.Sink = tkSessionSink(ag.sess)
	}
	if o.askStdin {
		rd := bufio.NewReader(askStdin)
		env.Ask = func(q string, opts []string) int {
			fmt.Fprintln(os.Stderr, "? "+q)
			for i, l := range opts {
				fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, l)
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
	if !r.Handled {
		if ag.sess != nil {
			ag.sess.Append(Message{Role: "assistant", Content: r.Text})
		}
		return nil
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
var brLevel0Intents = map[string]bool{"open_file": true, "git_status": true, "env_status": true, "tool_info": true, "tool_status": true}

// brLevel0Min is the confidence a level-0 answer needs.
const brLevel0Min = 0.85

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
	if !in.Understood() || in.Confidence < brLevel0Min || !brLevel0Intents[in.Name] {
		return nil
	}
	if in.Name == "env_status" || in.Name == "tool_info" || in.Name == "tool_status" {
		p = epGet(false) // versions and listings matter here
	}
	var events []tkEvent
	env := &brEnv{Root: tools.Root, Shell: tools.Shell, Permit: ag.PermitCommand, Profile: p,
		Sink: func(e tkEvent) { events = append(events, e) }}
	r := brRun(ctx, env, prompt)
	ok := r.Handled && (r.Status == tkSucceeded || r.Status == tkBlocked && r.Intent.Name == "git_status" && r.Category != rvPermission || r.Status == tkBlocked && r.Intent.Name == "tool_status" && r.Category == rvCommandMissing)
	if !ok {
		dbg("brain", map[string]any{"level0": "model", "intent": r.Intent.Name, "status": string(r.Status)})
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

// ---------- interactive (offline mode) ----------

// brInteractive tries the brain on an offline request: true = answered.
func (a *App) brInteractive(ctx context.Context, text string) bool {
	env := &brEnv{Root: a.root, Shell: a.tools.Shell, Permit: a.agent.PermitCommand}
	if a.agent.sess != nil {
		env.Sink = tkSessionSink(a.agent.sess)
	}
	env.Ask = func(q string, opts []string) int {
		items := make([]mnItem, 0, len(opts)+1)
		for _, o := range opts {
			items = append(items, mnItem{Label: o})
		}
		items = append(items, mnItem{Label: "Nenhum desses", Key: 'n'})
		i := a.ui.Choose(q, items, 0)
		if i < 0 || i >= len(opts) {
			return -1
		}
		return i
	}
	r := brRun(ctx, env, text)
	if !r.Handled {
		return false
	}
	if r.Text != "" {
		a.ui.print(sanitizeTerm(r.Text) + "\n")
	}
	a.ui.Notice(strings.TrimPrefix(brStatusLine(r), "· "))
	a.agent.Note(fmt.Sprintf("<system-reminder>While offline the harness's deterministic brain handled %q as %s: %s. Output:\n%s</system-reminder>", oneLine(text, 200), r.Intent.Name, r.Status, truncateHeadTail(r.Text, 6000)))
	return true
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
