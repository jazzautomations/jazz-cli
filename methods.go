package main

// methods.go: the method library (light HTN, docs/design/offline-brain.md §4)
// and its executor.
//
// A method is {name, intent, priority, pre, steps, verify, risk, cost}; steps
// call registered capabilities (caps.go), never free shell. Resolution is
// Pyhop-style: the methods of the intent, highest priority first; the first
// whose preconditions hold runs; a failed step goes through the recovery
// table (recover.go), which may move on to the next method; when none is
// left the task is blocked with an explanation. Success needs evidence
// (task.go): every method declares verifiers, and a method without one ends
// "unverified".
//
// Conditions are a closed mini-language: {"fact": "slot.target", "op":
// "set" | "unset" | "eq", "val": ...}. Facts: slot.<name>, hint,
// project.test|build|git, bin.<name>, env.online.

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed methods/*.json
var mhFS embed.FS

type mhCond struct {
	Fact string `json:"fact"`
	Op   string `json:"op"`
	Val  string `json:"val,omitempty"`
}

type mhStep struct {
	Cap string            `json:"cap"`
	In  map[string]string `json:"in"`
	Out string            `json:"out,omitempty"`
}

type mhCheck struct {
	Check string `json:"check"`
	Arg   string `json:"arg"`
}

// mhMethod is one way of doing an intent.
type mhMethod struct {
	Name     string    `json:"name"`
	Intent   string    `json:"intent"`
	Doc      string    `json:"doc,omitempty"`
	Priority int       `json:"priority"`
	Pre      []mhCond  `json:"pre"`
	Steps    []mhStep  `json:"steps"`
	Verify   []mhCheck `json:"verify"`
	Needs    []string  `json:"needs,omitempty"` // facts that must be set ("bin.ssh"): the tool the method uses is present
	Cmds     []string  `json:"cmds,omitempty"`  // literal commands the method runs as read-only: each must be in cbReadCmds
	Risk     string    `json:"risk"`
	CostMS   int       `json:"cost_ms"`
}

var (
	mhOnce sync.Once
	mhLib  map[string][]mhMethod
	mhErr  error
)

// mhLibrary loads the embedded methods, grouped by intent, best first.
func mhLibrary() (map[string][]mhMethod, error) {
	mhOnce.Do(func() {
		mhLib = map[string][]mhMethod{}
		ents, _ := mhFS.ReadDir("methods")
		for _, e := range ents {
			b, _ := mhFS.ReadFile("methods/" + e.Name())
			var f struct {
				Methods []mhMethod `json:"methods"`
			}
			if err := json.Unmarshal(b, &f); err != nil {
				mhErr = fmt.Errorf("methods/%s: %w", e.Name(), err)
				continue
			}
			for _, m := range f.Methods {
				if m.Name == "" || m.Intent == "" {
					continue
				}
				if bad := mhBadCmd(m); bad != "" {
					mhErr = fmt.Errorf("methods/%s: %s: comando fora da lista de leitura: %q", e.Name(), m.Name, bad)
					continue
				}
				mhLib[m.Intent] = append(mhLib[m.Intent], m)
			}
		}
		for k := range mhLib {
			l := mhLib[k]
			sort.SliceStable(l, func(i, j int) bool { return l[i].Priority > l[j].Priority })
		}
	})
	return mhLib, mhErr
}

// mhBadCmd returns the first declared command that is not an allowlisted
// read-only registry command ("" = all fine).
func mhBadCmd(m mhMethod) string {
	for _, c := range m.Cmds {
		if !cbReadCmd(c) {
			return c
		}
	}
	return ""
}

// mhHolds evaluates one precondition against the facts.
func mhHolds(c mhCond, facts map[string]string) bool {
	v, ok := facts[c.Fact]
	set := ok && v != ""
	switch c.Op {
	case "set", "":
		return set
	case "unset":
		return !set
	case "eq":
		return v == c.Val
	case "ne":
		return v != c.Val
	}
	return false
}

func mhApplicable(m mhMethod, facts map[string]string) bool {
	for _, n := range m.Needs {
		if !mhHolds(mhCond{Fact: n, Op: "set"}, facts) {
			return false
		}
	}
	for _, c := range m.Pre {
		if !mhHolds(c, facts) {
			return false
		}
	}
	return true
}

// ---------- executor ----------

// brEnv is what the brain may use. Every hook is optional except Root.
type brEnv struct {
	Root    string
	Shell   *Shell
	Permit  func(ctx context.Context, cmd string) (bool, string)
	Ask     func(q string, opts []string) int                         // chosen index, -1 = no answer
	AskText func(q string) (string, bool)                             // free answer
	Sink    func(tkEvent)                                             // task persistence
	Profile *epProfile                                                // nil = epGet(false)
	World   *nuWorld                                                  // nil = project + environment
	Run     func(ctx context.Context, cmd string) (string, int, bool) // nil = Shell (tests)
}

// brCtx is one execution.
type brCtx struct {
	ctx       context.Context
	env       *brEnv
	task      *TaskState
	in        Intent
	vars      map[string]any
	facts     map[string]string
	out       strings.Builder
	questions int
	commands  int
	explore   *rvExplore
	stop      tkStatus // set by a step that ends the task
	stopWhy   string
	cat       rvCat
	attempts  map[rvCat]int
	proj      fpProject
}

// brStep is what a capability returns.
type brStep struct {
	val  any
	obs  rvObs
	ok   bool
	why  string
	stop tkStatus // a step may end the task (report, question)
}

func (cx *brCtx) say(f string, a ...any) {
	fmt.Fprintf(&cx.out, f, a...)
	if !strings.HasSuffix(f, "\n") {
		cx.out.WriteByte('\n')
	}
}

// subst resolves "$var" and "$slot.x" in an input.
func (cx *brCtx) subst(v string) any {
	if !strings.HasPrefix(v, "$") {
		return v
	}
	k := v[1:]
	if strings.HasPrefix(k, "slot.") {
		if s, ok := cx.task.Slots[k[5:]]; ok {
			return s
		}
		return ""
	}
	if k == "hint" {
		return cx.in.Hint
	}
	if x, ok := cx.vars[k]; ok {
		return x
	}
	return ""
}

func (cx *brCtx) str(in map[string]string, k string) string {
	if v, ok := cx.subst(in[k]).(string); ok {
		return v
	}
	return ""
}

// runCmd runs a command through the permission check and the shell.
func (cx *brCtx) runCmd(cmd string, timeout time.Duration) (out string, exit int, obs rvObs) {
	return cx.runCmdAs(cmd, timeout, false)
}

// runCmdAs is runCmd; read=true marks an allowlisted registry command
// (caps.go cbReadCmds) as read-only for the permission check. Still sandboxed
// and audited like any other command.
func (cx *brCtx) runCmdAs(cmd string, timeout time.Duration, read bool) (out string, exit int, obs rvObs) {
	obs = rvNoCmd()
	t := cx.task
	if t.Budget.Cmds > 0 && t.Budget.UsedCmds >= t.Budget.Cmds {
		obs.BudgetLeft = 0
		return "", -1, obs
	}
	if cx.env.Permit != nil {
		pctx := cx.ctx
		if read {
			pctx = cbReadCtx(pctx, cmd)
		}
		if ok, why := cx.env.Permit(pctx, cmd); !ok {
			obs.Denied = true
			obs.Output = why
			t.AddEvidence("cmd", "negado: "+cmd+" ("+why+")")
			return "", -1, obs
		}
	}
	t.Budget.UsedCmds++
	cx.commands++
	timedOut := false
	if cx.env.Run != nil {
		out, exit, timedOut = cx.env.Run(cx.ctx, cmd)
	} else if cx.env.Shell != nil {
		r := cx.env.Shell.Run(cx.ctx, cmd, timeout, nil)
		out, exit = redactSecrets(r.Output), r.ExitCode
		timedOut = strings.Contains(strings.ToLower(r.Note), "timeout") || strings.Contains(strings.ToLower(r.Note), "tempo")
	} else {
		obs.Exit = 127
		obs.Output = "sem shell"
		return "", 127, obs
	}
	obs.Exit, obs.Output, obs.TimedOut = exit, truncateHeadTail(out, 4000), timedOut
	t.AddEvidence("cmd", fmt.Sprintf("`%s` → exit %d", cmd, exit))
	if cx.explore != nil && cx.explore.Observe(cmd, IsReadOnly(cmd), out) {
		cx.stop, cx.stopWhy = tkNeedsInput, rvExploreQuestion(cx.explore.Streak())
	}
	return out, exit, obs
}

// brCaps are the implementations of the brain capabilities.
var brCaps = map[string]func(cx *brCtx, in map[string]string) brStep{
	"resolve_name": brResolve,
	"choose":       brChoose,
	"ask_slot":     brAskSlot,
	"read_file":    brReadFile,
	"run_tests":    brRunTests,
	"test_context": brTestContext,
	"git_status":   brGitStatus,
	"env_status":   brEnvStatus,
	"tool_report":  brToolReport,
	"tool_status":  brToolStatus,
	"host_report":  brHostReport,
	"report":       brReport,
}

func brResolve(cx *brCtx, in map[string]string) brStep {
	q := cx.str(in, "q")
	kinds := strings.Split(cx.str(in, "kinds"), ",")
	obs := rvNoCmd()
	if q == "" {
		obs.Cands = 0
		return brStep{obs: obs, why: "nome vazio"}
	}
	c := niResolveEntity(cx.env.Root, q, kinds, cx.str(in, "hint"), 8)
	obs.Cands = len(c)
	if len(c) > 0 {
		tied := 1
		for tied < len(c) && c[tied].Score-c[0].Score < 2*fzSlip*8 && !c[0].Exact {
			tied++
		}
		if c[0].Exact {
			tied = 0
			for _, x := range c {
				if x.Exact {
					tied++
				}
			}
		}
		obs.Tied = tied
		var names []string
		for i, x := range c {
			if i == 3 {
				break
			}
			names = append(names, x.Display())
		}
		cx.task.AddEvidence("resolve", fmt.Sprintf("'%s' → %d candidato(s): %s", q, len(c), strings.Join(names, "; ")))
		return brStep{val: c, obs: obs, ok: true}
	}
	if in["empty"] == "ok" {
		cx.task.AddEvidence("resolve", fmt.Sprintf("'%s' → nenhum candidato (%s)", q, strings.Join(kinds, ",")))
		return brStep{val: []niEntity{}, obs: obs, ok: true}
	}
	// name_not_found: show what is close, never invent the name
	sim := niSimilarPaths(cx.env.Root, q, 3*fzEdit)
	msg := fmt.Sprintf("não achei '%s'", q)
	if len(sim) > 0 {
		msg += "; parecidos: " + strings.Join(sim, ", ")
	}
	cx.task.AddEvidence("resolve", msg)
	return brStep{obs: obs, why: msg}
}

// brSafeTypo: the candidate is within one edit (plus a slip), or the typed
// word only dropped or doubled letters of it ("talscle" → "tailscale",
// "parsr" → "parser") — the fast-typing errors. "handler" → "HANDOFF.md"
// is neither, so it is confirmed first.
func brSafeTypo(e niEntity) bool {
	if e.Exact || e.Dist <= fzEdit+fzSlip {
		return true
	}
	q := fzKey(e.Query)
	k := fzKey(e.Name)
	if e.Kind == "file" || e.Kind == "test" || e.Kind == "dir" {
		base := e.Name
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if j := strings.LastIndexByte(base, '.'); j > 0 && !strings.Contains(e.Query, ".") {
			base = base[:j]
		}
		k = fzKey(base)
	}
	return q != "" && (brSubseq(q, k) || brSubseq(k, q))
}

// brSubseq: every rune of a appears in b, in order.
func brSubseq(a, b string) bool {
	rb := []rune(b)
	j := 0
	for _, r := range a {
		for j < len(rb) && rb[j] != r {
			j++
		}
		if j == len(rb) {
			return false
		}
		j++
	}
	return true
}

// brPick decides whether candidates can be acted on without asking: one
// exact name, a single candidate within the typo limit, or a clear leader.
func brPick(c []niEntity) (int, string) {
	if len(c) == 0 {
		return -1, ""
	}
	exact := 0
	for _, x := range c {
		if x.Exact {
			exact++
		}
	}
	switch {
	case exact == 1 && c[0].Exact:
		return 0, "nome exato"
	case exact > 1:
		return -1, ""
	case len(c) == 1 && brSafeTypo(c[0]):
		return 0, "único nome parecido"
	case len(c) == 1:
		return -1, "" // one far candidate: confirm, never assume
	case c[1].Score-c[0].Score >= fzSlip*8:
		return 0, "bem mais parecido que o segundo (" + c[1].Display() + ")"
	}
	return -1, ""
}

func brChoose(cx *brCtx, in map[string]string) brStep {
	opts, _ := cx.subst(in["options"]).([]niEntity)
	what := in["what"]
	maxN, _ := strconv.Atoi(in["max"])
	if maxN <= 0 || maxN > 5 {
		maxN = 5
	}
	obs := rvNoCmd()
	if len(opts) == 0 {
		if in["empty"] == "ok" {
			return brStep{val: "", obs: obs, ok: true}
		}
		obs.Cands = 0
		return brStep{obs: obs, why: "nenhum " + what}
	}
	if i, why := brPick(opts); i >= 0 {
		e := opts[i]
		why2 := why
		if len(e.Why) > 0 {
			why2 += "; " + strings.Join(e.Why, "; ")
		}
		cx.task.AddEvidence("resolve", fmt.Sprintf("escolhido %s (%s)", e.Display(), why2))
		if !e.Exact {
			cx.say("entendi '%s' como %s (%s)", cx.lastQuery(), e.Display(), why)
		}
		return brStep{val: e.Name, obs: obs, ok: true}
	}
	if len(opts) > maxN {
		opts = opts[:maxN]
	}
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.Display()
	}
	q := fmt.Sprintf("Qual %s você quis dizer com '%s'?", what, cx.lastQuery())
	cx.questions++
	idx := -1
	if cx.env.Ask != nil {
		idx = cx.env.Ask(q, labels)
	}
	if idx < 0 || idx >= len(opts) {
		var b strings.Builder
		b.WriteString(q + "\n")
		for i, l := range labels {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, l)
		}
		b.WriteString("responda com o número (ou o nome certo)")
		obs.Cands, obs.Tied = len(opts), len(opts)
		return brStep{obs: obs, why: b.String(), stop: tkNeedsInput}
	}
	e := opts[idx]
	cx.task.AddEvidence("answer", fmt.Sprintf("usuário escolheu %d: %s", idx+1, e.Display()))
	return brStep{val: e.Name, obs: obs, ok: true}
}

// lastQuery is the raw slot the current resolution came from.
func (cx *brCtx) lastQuery() string {
	for _, k := range []string{"target", "tool", "host", "service", "container", "query"} {
		if v := cx.task.Slots[k]; v != "" {
			return v
		}
	}
	return ""
}

func brAskSlot(cx *brCtx, in map[string]string) brStep {
	slot, q := in["slot"], in["q"]
	cx.questions++
	if cx.env.AskText != nil {
		if a, ok := cx.env.AskText(q); ok && strings.TrimSpace(a) != "" {
			if cx.task.Slots == nil {
				cx.task.Slots = map[string]string{}
			}
			cx.task.Slots[slot] = strings.TrimSpace(a)
			cx.task.AddEvidence("answer", q+" → "+a)
			return brStep{val: a, obs: rvNoCmd(), ok: true}
		}
	}
	return brStep{obs: rvNoCmd(), why: q, stop: tkNeedsInput}
}

// brMaxRead caps what read_file loads (the excerpt is smaller still).
const brMaxRead = 256 << 10

func brReadFile(cx *brCtx, in map[string]string) brStep {
	rel := cx.str(in, "path")
	obs := rvNoCmd()
	abs := filepath.Join(cx.env.Root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		obs.Cands = 0
		return brStep{obs: obs, why: "não consegui abrir " + rel + ": " + err.Error()}
	}
	if fi.IsDir() {
		ents, _ := os.ReadDir(abs)
		var names []string
		for i, e := range ents {
			if i == 40 {
				names = append(names, fmt.Sprintf("… +%d", len(ents)-40))
				break
			}
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		cx.say("%s/ (%d entradas)\n  %s", rel, len(ents), strings.Join(names, "  "))
		cx.task.AddEvidence("file", fmt.Sprintf("pasta %s, %d entradas", rel, len(ents)))
		return brStep{val: strings.Join(names, "\n"), obs: obs, ok: true}
	}
	f, err := os.Open(abs)
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	buf := make([]byte, min(fi.Size(), brMaxRead))
	n, _ := f.Read(buf)
	f.Close()
	buf = buf[:n]
	sum := sha256.Sum256(buf)
	lines := strings.Split(string(buf), "\n")
	syms := brSymbols(rel, buf)
	niTouch(abs)
	cx.say("%s · %d bytes · %d linhas · modificado %s · sha256 %s", rel, fi.Size(), len(lines), fi.ModTime().Format("2006-01-02 15:04"), hex.EncodeToString(sum[:6]))
	if len(syms) > 0 {
		cx.say("símbolos: %s", strings.Join(syms, ", "))
	}
	show := 40
	if len(lines) < show {
		show = len(lines)
	}
	for i := 0; i < show; i++ {
		fmt.Fprintf(&cx.out, "%5d  %s\n", i+1, sanitizeTerm(oneLineKeep(lines[i], 200)))
	}
	if len(lines) > show {
		cx.say("… (%d linhas a mais)", len(lines)-show)
	}
	cx.task.AddEvidence("file", fmt.Sprintf("%s: %d bytes, %d linhas, mtime %s, sha256 %s, %d símbolo(s)", rel, fi.Size(), len(lines), fi.ModTime().Format(time.RFC3339), hex.EncodeToString(sum[:6]), len(syms)))
	return brStep{val: string(buf), obs: obs, ok: true}
}

// oneLineKeep clips a line without collapsing its indentation.
func oneLineKeep(s string, n int) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// brSymbols are the top-level names of a file (nameindex regexes, cold path).
func brSymbols(rel string, b []byte) []string {
	lang := niSymLang(strings.ToLower(filepath.Ext(rel)))
	if lang == "" {
		return nil
	}
	if len(b) > niSymBytes {
		b = b[:niSymBytes]
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range niSymRE[lang].FindAllSubmatch(b, -1) {
		s := string(m[1])
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
		if len(out) == 12 {
			break
		}
	}
	return out
}

// brTestRes is the outcome of run_tests.
type brTestRes struct {
	Cmd    string
	Exit   int
	Out    string
	Parsed *opResult
}

// brTestCmd narrows the project's test command to target: a resolved test
// file becomes its package/path; otherwise fpTestCommand's narrowing.
func brTestCmd(cx *brCtx, target, hint string) string {
	p := cx.proj
	if target == "" {
		return p.Test
	}
	c := niResolveEntity(cx.env.Root, target, []string{"test", "file"}, "test", 4)
	if i, _ := brPick(c); i >= 0 && (c[i].Kind == "test" || c[i].Kind == "file") {
		rel := c[i].Name
		cx.task.AddEvidence("resolve", fmt.Sprintf("alvo '%s' → %s", target, rel))
		switch {
		case p.Test == "go test ./...":
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir == "." {
				return "go test ."
			}
			return "go test ./" + dir
		case strings.HasPrefix(p.Test, "python3 -m pytest"), strings.HasSuffix(p.Test, " test") && p.PM != "":
			return fpTestCommand(p, rel)
		case strings.HasPrefix(p.Test, "python3 -m unittest"):
			return "python3 -m unittest " + fpShQuote(strings.TrimSuffix(strings.ReplaceAll(rel, "/", "."), ".py"))
		}
	}
	if p.Test == "go test ./..." {
		return "go test -run " + fpShQuote("(?i)"+target) + " ./..."
	}
	return fpTestCommand(p, target)
}

func brRunTests(cx *brCtx, in map[string]string) brStep {
	target := cx.str(in, "target")
	cmd := brTestCmd(cx, target, cx.str(in, "hint"))
	if retry, _ := cx.vars["retry_smaller"].(bool); retry && target != "" {
		cmd = cx.proj.Test // narrowing failed: the whole suite once
	}
	cx.say("$ %s", cmd)
	if f := strings.Fields(cmd); len(f) > 0 && cx.env.Run == nil && !strings.ContainsAny(f[0], "/=$") {
		if _, err := exec.LookPath(f[0]); err != nil { // needs: the runner binary is present
			obs := rvNoCmd()
			obs.Exit, obs.Output = 127, f[0]+": command not found"
			return brStep{obs: obs, why: f[0] + " não está no PATH: não dá pra rodar os testes"}
		}
	}
	out, exit, obs := cx.runCmd(cmd, 10*time.Minute)
	if obs.Exit < 0 || obs.Denied || obs.BudgetLeft == 0 {
		return brStep{obs: obs, why: obs.Output}
	}
	r := brTestRes{Cmd: cmd, Exit: exit, Out: out, Parsed: opParse(out)}
	obs.Parsed = r.Parsed != nil && r.Parsed.Data.Kind == "test"
	if obs.Parsed {
		obs.Failures = r.Parsed.Data.Failed + r.Parsed.Data.Errors
		d := r.Parsed.Data
		cx.say("%s: %d passaram · %d falharam · %d pulados", d.Format, d.Passed, d.Failed+d.Errors, d.Skipped)
		cx.task.AddEvidence("tests", fmt.Sprintf("%s exit %d: %d ok, %d falhas, %d pulados", d.Format, exit, d.Passed, d.Failed+d.Errors, d.Skipped))
	} else {
		cx.say("%s", strings.TrimRight(truncateHeadTail(out, 3000), "\n"))
	}
	cx.vars["result"] = r
	switch {
	case exit == 127 || exit == 126 || strings.HasPrefix(strings.TrimSpace(out), "bwrap:"):
		return brStep{val: r, obs: obs, why: "o comando de teste não rodou (exit " + strconv.Itoa(exit) + ")"}
	case obs.TimedOut:
		return brStep{val: r, obs: obs, why: "os testes estouraram o tempo"}
	}
	// a failing run is a result, not a broken step: test_context shows it
	return brStep{val: r, obs: obs, ok: true}
}

func brTestContext(cx *brCtx, in map[string]string) brStep {
	r, _ := cx.subst(in["result"]).(brTestRes)
	obs := rvNoCmd()
	if r.Parsed == nil || len(r.Parsed.Data.Failures) == 0 {
		return brStep{val: "", obs: obs, ok: true}
	}
	n := 0
	for _, f := range r.Parsed.Data.Failures {
		if n == 3 {
			cx.say("… +%d falha(s)", len(r.Parsed.Data.Failures)-3)
			break
		}
		n++
		loc := f.File
		if rel, err := filepath.Rel(cx.env.Root, f.File); err == nil && filepath.IsAbs(f.File) && !strings.HasPrefix(rel, "..") {
			loc = filepath.ToSlash(rel)
		}
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		cx.say("✗ %s %s %s", f.Test, loc, oneLine(f.Message, 160))
		if f.File == "" || f.Line <= 0 {
			continue
		}
		abs := f.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cx.env.Root, filepath.FromSlash(f.File))
			if _, err := os.Stat(abs); err != nil { // go prints the file relative to its package
				for _, c := range niResolveEntity(cx.env.Root, filepath.Base(f.File), []string{"test", "file"}, "test", 2) {
					if c.Exact {
						abs = filepath.Join(cx.env.Root, filepath.FromSlash(c.Name))
						break
					}
				}
			}
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		lo, hi := max(1, f.Line-3), min(len(lines), f.Line+3)
		for k := lo; k <= hi; k++ {
			mark := " "
			if k == f.Line {
				mark = ">"
			}
			fmt.Fprintf(&cx.out, "  %s%4d  %s\n", mark, k, sanitizeTerm(oneLineKeep(lines[k-1], 160)))
		}
		cx.task.AddEvidence("file", fmt.Sprintf("contexto da falha %s:%d", f.File, f.Line))
	}
	return brStep{val: "ok", obs: obs, ok: true}
}

// brCmdRes is a finished read-only command.
type brCmdRes struct {
	Cmd  string
	Exit int
	Out  string
}

func brGitStatus(cx *brCtx, in map[string]string) brStep {
	cmd := "git status --short --branch"
	out, exit, obs := cx.runCmdAs(cmd, 20*time.Second, true)
	if obs.Exit < 0 || obs.Denied {
		return brStep{obs: obs, why: obs.Output}
	}
	cx.say("$ %s\n%s", cmd, strings.TrimRight(truncateHeadTail(out, 4000), "\n"))
	if exit != 0 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, why: "git status falhou"}
	}
	changed := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" && !strings.HasPrefix(l, "##") {
			changed++
		}
	}
	if changed == 0 {
		cx.say("nada mudou desde o último commit")
	}
	cx.task.AddEvidence("cmd", fmt.Sprintf("git status: %d arquivo(s) com mudança", changed))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

func brEnvStatus(cx *brCtx, in map[string]string) brStep {
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	cx.say("%s", strings.TrimRight(epText(p), "\n"))
	proj := cx.proj
	if len(proj.Langs) > 0 || proj.Test != "" {
		cx.say("projeto   %s · testes: %s · build: %s · git: %v", strings.Join(proj.Langs, ","), orDash(proj.Test), orDash(proj.Build), proj.Git)
	}
	cx.task.AddEvidence("env", fmt.Sprintf("perfil %s/%s, %d ferramentas, %d hosts, %d portas", p.OS, p.Arch, len(p.Toolchains), len(p.SSHHosts)+len(p.Tailscale), len(p.Ports)))
	return brStep{val: p, obs: rvNoCmd(), ok: true}
}

func brToolReport(cx *brCtx, in map[string]string) brStep {
	name, asked := cx.str(in, "name"), cx.str(in, "asked")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if name == "" {
		cx.say("%s não está instalado: nada com esse nome (nem parecido) entre os %d binários do PATH", asked, len(p.PathBins))
		cx.task.AddEvidence("env", fmt.Sprintf("'%s' ausente do PATH (%d binários)", asked, len(p.PathBins)))
		return brStep{val: p, obs: rvNoCmd(), ok: true}
	}
	for _, t := range p.Toolchains {
		if t.Name == name {
			v := t.Version
			if v == "" {
				v = "versão desconhecida"
			}
			cx.say("%s instalado: %s (%s)", name, v, t.Path)
			cx.task.AddEvidence("env", fmt.Sprintf("%s %s em %s", name, v, t.Path))
			return brStep{val: p, obs: rvNoCmd(), ok: true}
		}
	}
	path := ""
	if lp, err := exec.LookPath(name); err == nil {
		path = lp
	}
	if path == "" && !epHasBin(p, name) {
		cx.say("%s não está no PATH", name)
		cx.task.AddEvidence("env", name+" ausente do PATH")
		return brStep{val: p, obs: rvNoCmd(), ok: true}
	}
	cx.say("%s instalado (%s)", name, orDash(path))
	cx.task.AddEvidence("env", fmt.Sprintf("%s no PATH (%s)", name, orDash(path)))
	return brStep{val: p, obs: rvNoCmd(), ok: true}
}

// brToolStatusCmds is the read-only status command of each tool the brain
// knows. Anything else is answered with the tool's path and version.
var brToolStatusCmds = map[string]string{
	"git":       "git status --short --branch",
	"tailscale": "tailscale status",
	"docker":    "docker ps",
	"podman":    "podman ps",
	"systemctl": "systemctl is-system-running",
}

// brToolStatus runs the read-only status command of the resolved tool
// ("talscle status" → tailscale status). Entity-first: the generic verb is
// scoped to the tool. A tool with no safe status command gets its info.
func brToolStatus(cx *brCtx, in map[string]string) brStep {
	name, asked := cx.str(in, "name"), cx.str(in, "asked")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if name == "" {
		w := orDash(asked)
		cx.task.AddEvidence("env", "'"+asked+"' ausente do PATH ("+strconv.Itoa(len(p.PathBins))+" binários)")
		cx.cat = rvCommandMissing
		return brStep{obs: rvNoCmd(), why: w + " não está instalado: nada com esse nome (nem parecido) entre os binários do PATH", stop: tkBlocked}
	}
	path := ""
	if lp, err := exec.LookPath(name); err == nil {
		path = lp
	}
	if path == "" && !epHasBin(p, name) && !epHasBin(p, name+".exe") {
		cx.task.AddEvidence("env", name+" ausente do PATH")
		cx.cat = rvCommandMissing
		return brStep{obs: rvNoCmd(), why: name + " não está no PATH: não dá pra ver o status", stop: tkBlocked}
	}
	cmd, known := brToolStatusCmds[name]
	if !known {
		step := brToolReport(cx, map[string]string{"name": name, "asked": asked})
		cx.say("não conheço um comando de status seguro para %s: só informei o caminho e a versão", name)
		if step.ok {
			step.val = name
		}
		return step
	}
	out, exit, obs := cx.runCmdAs(cmd, 20*time.Second, true)
	if obs.Exit < 0 || obs.Denied {
		return brStep{obs: obs, why: obs.Output}
	}
	body := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if exit != 0 {
		cx.say("$ %s (saiu com código %d)\n%s", cmd, exit, body)
	} else {
		cx.say("$ %s\n%s", cmd, body)
	}
	cx.task.AddEvidence("cmd", fmt.Sprintf("status de %s: `%s` → exit %d", name, cmd, exit))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

func brHostReport(cx *brCtx, in map[string]string) brStep {
	name := cx.str(in, "name")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	for _, h := range p.SSHHosts {
		if h.Name == name {
			d := h.HostName
			if h.User != "" {
				d = h.User + "@" + d
			}
			cx.say("host SSH %s (%s): para entrar, rode !ssh %s", name, orDash(d), name)
			cx.task.AddEvidence("env", "host "+name+" em ~/.ssh/config")
			return brStep{val: name, obs: rvNoCmd(), ok: true}
		}
	}
	for _, t := range p.Tailscale {
		if t.Name == name {
			st := "offline"
			if t.Online {
				st = "online"
			}
			cx.say("máquina Tailscale %s (%s, %s segundo o tailscale status): para entrar, rode !ssh %s", name, orDash(t.IP), st, name)
			cx.task.AddEvidence("env", "peer Tailscale "+name+" "+st)
			return brStep{val: name, obs: rvNoCmd(), ok: true}
		}
	}
	return brStep{obs: rvNoCmd(), why: name + " sumiu do perfil"}
}

func brReport(cx *brCtx, in map[string]string) brStep {
	text := cx.str(in, "text")
	st := tkStatus(in["status"])
	if st == "" {
		st = tkBlocked
	}
	if c := in["category"]; c != "" {
		cx.cat = rvCat(c)
	}
	return brStep{obs: rvNoCmd(), why: text, stop: st}
}

// ---------- verification ----------

// brVerify runs a method's checks: ok, or why not; unavailable = nothing
// could check (the result is "unverified", never "succeeded").
func brVerify(cx *brCtx, m mhMethod) (ok bool, unavailable bool, why string) {
	if len(m.Verify) == 0 {
		return false, true, "este método não tem verificador"
	}
	for _, c := range m.Verify {
		arg := cx.subst(c.Arg)
		switch c.Check {
		case "file_exists":
			rel, _ := arg.(string)
			if _, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(rel))); rel == "" || err != nil {
				return false, false, "o arquivo não existe: " + rel
			}
		case "read_ok":
			if _, isStr := arg.(string); !isStr {
				return false, false, "o conteúdo não foi lido"
			}
		case "exit_zero":
			r, isRes := arg.(brCmdRes)
			if !isRes || r.Exit != 0 {
				return false, false, "o comando não terminou com 0"
			}
		case "status_obtained":
			if arg == nil || arg == "" {
				return false, false, "não consegui o status da ferramenta"
			}
		case "profile_present":
			if arg == nil || arg == "" {
				return false, false, "sem perfil do ambiente"
			}
		case "tests_passed":
			r, isRes := arg.(brTestRes)
			if !isRes {
				return false, true, "os testes não rodaram"
			}
			if r.Parsed == nil || r.Parsed.Data.Kind != "test" {
				if r.Exit == 0 {
					return false, true, "o comando passou, mas não reconheci a saída de testes: não dá pra afirmar quantos rodaram"
				}
				return false, false, fmt.Sprintf("os testes falharam (exit %d)", r.Exit)
			}
			d := r.Parsed.Data
			if d.Failed+d.Errors > 0 || r.Exit != 0 {
				return false, false, fmt.Sprintf("%d teste(s) falharam", max(1, d.Failed+d.Errors))
			}
			if d.Passed == 0 && d.Total == 0 && len(d.Suites) == 0 {
				return false, true, "nenhum teste rodou"
			}
		default:
			return false, true, "verificador desconhecido: " + c.Check
		}
	}
	return true, false, ""
}
