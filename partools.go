package main

// partools.go: parallel execution of independent tool calls of one reply.
//
// When a reply carries N>1 tool calls, the read-class calls (read, read-only
// bash, fetch, read-only script) that sit between two mutating calls are
// started together, bounded by a semaphore, BEFORE the sequential loop in
// Agent.Run reaches them. The loop itself is unchanged: every call still goes,
// in the model's order, through hooks, the honest guards, the budget, the
// permission gate, the fence, the injection guard and the spiral breaker, and
// its result is appended in the original order. Only when the gate lets a call
// run does execTool take the result that was computed ahead of time
// (ptTake) instead of executing it again. A call the gate refuses has its
// early result discarded, so the model never sees it.
//
// Why this is safe:
//   - Only calls without side effects are started early. A read defers its
//     two effects (the read stamp that lets a later edit through, and the
//     lazily injected AGENTS.md note) until it is taken (ptEffects).
//   - Mutating calls (write, edit, non-read-only bash, mutating scripts, MCP,
//     task, sidekick) are barriers: nothing after one is started before it
//     ran, so a read after an edit sees the edit, and two edits never overlap.
//   - Nothing is started early when a call would need a prompt or a classifier
//     decision: ask mode or smart mode for network calls, an ask/deny rule,
//     the work-dir fence, a credentials path or command, an egress command, a
//     suspected injection earlier in the turn (fetch/script), or any
//     PreToolUse hook (it may rewrite or veto the call, and expects to run
//     before the tool does).
//   - Read-only bash cannot use the persistent shell concurrently (it is
//     single) and no second persistent shell is spawned. It runs in a
//     short-lived shell with the persistent shell's cwd and exported
//     environment (captured once per shell generation), the same init line,
//     the same env scrubbing and the same OS sandbox (sbWrapCmd). Exit 127
//     (a function or alias the one-shot shell does not have) falls back to the
//     persistent shell at take time; the command is read-only, so running it
//     twice is harmless.
//
// Settings: tools.parallel in config.json, JAZZ_PARALLEL_TOOLS, flag
// --parallel-tools: auto (default) | on | off | N (1-16). auto = min(4,
// 2×NumCPU) workers; on a single CPU the local lane is off and only network
// calls (fetch, local http_check) run two at a time. on = min(4, 2×NumCPU)
// for every lane, also on one CPU. N = N workers for every lane.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- settings ----------

// ToolsConf is the "tools" object of config.json (Config.Tools).
type ToolsConf struct {
	Parallel any `json:"parallel,omitempty"` // "auto" | "on" | "off" | N
}

// ptSettings is the effective parallel-tools setting.
type ptSettings struct {
	Mode string // auto | on | off | n
	N    int    // workers when Mode == "n"
}

var (
	ptSetMu     sync.Mutex
	ptSetVal    ptSettings
	ptSetLoaded bool
	ptNumCPU    = runtime.NumCPU // tests swap it
	// ptTestHook (tests only) runs in the worker before an early call executes.
	ptTestHook func(name, args string)
)

// ptParse reads "auto", "on", "off" (and their usual synonyms) or a number.
func ptParse(s string) (ptSettings, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ptSettings{Mode: "auto"}, true
	case "on", "true", "yes":
		return ptSettings{Mode: "on"}, true
	case "off", "false", "no", "0":
		return ptSettings{Mode: "off"}, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 16 {
		return ptSettings{}, false
	}
	if n == 1 {
		return ptSettings{Mode: "off"}, true
	}
	return ptSettings{Mode: "n", N: n}, true
}

func ptLoadSettings(cfgPath string, getenv func(string) string) ptSettings {
	s := ptSettings{Mode: "auto"}
	var c struct {
		Tools struct {
			Parallel any `json:"parallel"`
		} `json:"tools"`
	}
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &c)
		if c.Tools.Parallel != nil {
			if v, ok := ptParse(fmt.Sprint(c.Tools.Parallel)); ok {
				s = v
			}
		}
	}
	if e := getenv("JAZZ_PARALLEL_TOOLS"); e != "" {
		if v, ok := ptParse(e); ok {
			s = v
		}
	}
	return s
}

func ptSettingsNow() ptSettings {
	ptSetMu.Lock()
	defer ptSetMu.Unlock()
	if !ptSetLoaded {
		ptSetVal = ptLoadSettings(filepath.Join(configDir(), "config.json"), os.Getenv)
		ptSetLoaded = true
	}
	return ptSetVal
}

// ptSetSettings replaces the effective setting and returns the previous one.
func ptSetSettings(s ptSettings) ptSettings {
	prev := ptSettingsNow()
	ptSetMu.Lock()
	ptSetVal, ptSetLoaded = s, true
	ptSetMu.Unlock()
	return prev
}

// ptLimits returns the worker count and which lanes run in parallel.
func ptLimits(s ptSettings, ncpu int) (workers int, cpuLane, netLane bool) {
	def := 2 * ncpu
	if def > 4 {
		def = 4
	}
	if def < 2 {
		def = 2
	}
	switch s.Mode {
	case "off":
		return 0, false, false
	case "on":
		return def, true, true
	case "n":
		return s.N, true, true
	}
	if ncpu <= 1 {
		// a single core gains nothing from running local work side by side,
		// but calls that wait on the network still overlap
		return 2, false, true
	}
	return def, true, true
}

// ptFlags is --parallel-tools.
type ptFlags struct {
	fs  *flag.FlagSet
	val string
}

func init() {
	opValuedFlags["--parallel-tools"] = true // reorderFlags (main.go) learns it takes a value
	opValuedFlags["-parallel-tools"] = true
}

func ptRegisterFlags(fs *flag.FlagSet) *ptFlags {
	f := &ptFlags{fs: fs}
	fs.StringVar(&f.val, "parallel-tools", "", "")
	return f
}

func (f *ptFlags) apply() error {
	set := false
	f.fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "parallel-tools" {
			set = true
		}
	})
	if !set {
		return nil
	}
	v, ok := ptParse(f.val)
	if !ok {
		return errors.New("--parallel-tools deve ser auto, on, off ou um número de 1 a 16")
	}
	ptSetSettings(v)
	return nil
}

// ---------- deferred effects of an early read ----------

type ptCtxKey struct{}

// ptRun is what an early (parallel) execution carries in its context.
type ptRun struct {
	eff ptEffects
	// one-shot bash: cwd and environment of the persistent shell, and the
	// result budget computed by the agent goroutine
	cwd         string
	env         []string
	window      int
	oneShotCode int // exit code of the one-shot shell (-2 = did not start)
}

// ptEffects are the side effects of an early read, applied when it is taken.
type ptEffects struct {
	stampPath string
	stampData []byte
	instrPath string
}

func ptFrom(ctx context.Context) *ptRun {
	r, _ := ctx.Value(ptCtxKey{}).(*ptRun)
	return r
}

// ptStamp records a read stamp now, or defers it for an early read.
func (t *Toolbox) ptStamp(ctx context.Context, p string, data []byte) {
	if r := ptFrom(ctx); r != nil {
		r.eff.stampPath, r.eff.stampData = p, data
		return
	}
	t.stampData(p, data)
}

// ptInstrNote returns the lazy AGENTS.md note now, or defers it.
func (t *Toolbox) ptInstrNote(ctx context.Context, p string) string {
	if r := ptFrom(ctx); r != nil {
		r.eff.instrPath = p
		return ""
	}
	return t.instrNote(p)
}

// commit applies the deferred effects; out is the read's result.
func (t *Toolbox) ptCommit(e ptEffects, out *ToolOutput) {
	if e.stampPath != "" {
		t.stampData(e.stampPath, e.stampData)
	}
	if e.instrPath != "" {
		out.Text += redactSecrets(t.instrNote(e.instrPath))
	}
}

// ---------- one-shot shell for read-only bash ----------

var (
	ptFdOnce          sync.Once
	ptHaveFd, ptHaveF bool
)

// ptOneShotOK: the dialect can run a read-only command in a fresh shell with
// the same meaning (bash/sh on Unix; not PowerShell or MSYS).
func (s *Shell) ptOneShotOK() bool { return s.d.Kind != shPwsh && !s.d.Win }

// ptEnv is the persistent shell's exported environment, cached until the
// shell runs another command (gen) or is reset.
func (s *Shell) ptEnv(ctx context.Context) []string {
	s.ptMu.Lock()
	if s.ptEnvVal != nil && s.ptEnvGen == s.gen.Load() {
		e := s.ptEnvVal
		s.ptMu.Unlock()
		return e
	}
	s.ptMu.Unlock()
	env := s.exportedEnv(ctx)
	s.ptMu.Lock()
	s.ptEnvVal, s.ptEnvGen = env, s.gen.Load()
	s.ptMu.Unlock()
	return env
}

// ptCwd is the persistent shell's cwd.
func (s *Shell) ptCwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Cwd == "" {
		return s.home
	}
	return s.Cwd
}

type ptWriter struct {
	mu sync.Mutex
	ob *outBuf
}

func (w *ptWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.ob.write(p)
	w.mu.Unlock()
	return len(p), nil
}

// ptRunOnce runs one read-only command in a short-lived shell. It never
// touches the persistent shell. ExitCode -2 = could not start.
func (s *Shell) ptRunOnce(ctx context.Context, command, cwd string, env []string, timeout time.Duration) ShellResult {
	t0 := time.Now()
	ptFdOnce.Do(func() {
		_, e1 := exec.LookPath("fd")
		_, e2 := exec.LookPath("fdfind")
		ptHaveFd, ptHaveF = e1 == nil, e2 == nil
	})
	// the init line on its own line: aliases and functions it defines apply
	// to the command (bash reads a whole line before running it)
	argv := s.d.oneShotArgv(s.d.initLine(ptHaveFd, ptHaveF) + "\n" + command)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(append([]string(nil), env...),
		"TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "MANPAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true",
		"DEBIAN_FRONTEND=noninteractive", "PIP_NO_INPUT=1", "NPM_CONFIG_YES=true", "CI=1", "JAZZ_CLI=1", "NO_COLOR=1")
	w := &ptWriter{ob: &outBuf{headCap: s.maxKeep / 4, tailCap: s.maxKeep - s.maxKeep/4}}
	cmd.Stdout, cmd.Stderr = w, w // stdin: /dev/null, like the persistent shell's eval
	cmd.WaitDelay = 2 * time.Second
	procPrepare(cmd, false)
	sbWrapCmd(cmd)
	if err := cmd.Start(); err != nil {
		return ShellResult{Output: err.Error(), ExitCode: -2, Duration: time.Since(t0)}
	}
	procAttach(cmd)
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	note := ""
	select {
	case werr = <-done:
	case <-timer.C:
		procKillTree(pid)
		werr = <-done
		note = fmt.Sprintf("[Command timed out after %s and was killed.]", timeout.Round(time.Second))
	case <-ctx.Done():
		procKillTree(pid)
		werr = <-done
		note = "[Interrupted by the user.]"
	}
	procKillTree(pid) // stray children of the group
	procForget(pid)
	code := 0
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		code = ee.ExitCode()
	} else if werr != nil && !errors.Is(werr, exec.ErrWaitDelay) {
		code = -1
	}
	if note != "" {
		code = -1
	}
	w.mu.Lock()
	out, dropped := w.ob.upTo(w.ob.total)
	w.mu.Unlock()
	r := ShellResult{Output: strings.TrimSuffix(out, "\n"), ExitCode: code, Duration: time.Since(t0), Note: note}
	if dropped > 0 && note == "" {
		r.Note = fmt.Sprintf("[%d bytes of output were discarded from the middle (over %d MB)]", dropped, s.maxKeep>>20)
	}
	return r
}

// ---------- the scheduler ----------

// Lanes reported on tool.start/tool.end events.
const (
	ptLaneSeq = "seq" // ran in order, in the agent goroutine
	ptLanePar = "par" // started early, in parallel with its neighbours
)

type ptEntry struct {
	name, args string
	net        bool
	run        *ptRun
	done       chan struct{}
	out        ToolOutput
	dur        time.Duration
	retry      bool // run it again the normal way (one-shot exit 127, start failure)
	taken      bool
}

// ptSched schedules one batch of tool calls.
type ptSched struct {
	a        *Agent
	calls    []ToolCall
	ctx      context.Context
	cancel   context.CancelFunc
	sem      chan struct{}
	cpu, net bool
	visited  []bool
	ents     map[int]*ptEntry
	wg       sync.WaitGroup

	launched, used, discarded, segments int
}

// ptBegin prepares the schedule of a batch; nil when nothing can overlap.
func (a *Agent) ptBegin(ctx context.Context, calls []ToolCall) *ptSched {
	a.ptEnd()
	if len(calls) < 2 || a.ptOff {
		return nil
	}
	workers, cpu, net := ptLimits(ptSettingsNow(), ptNumCPU())
	if workers < 2 || !cpu && !net {
		return nil
	}
	c, cancel := context.WithCancel(ctx)
	s := &ptSched{a: a, calls: calls, ctx: c, cancel: cancel, sem: make(chan struct{}, workers), cpu: cpu, net: net,
		visited: make([]bool, len(calls)), ents: map[int]*ptEntry{}}
	a.pt = s
	return s
}

// ptEnd stops what is still running early and discards it.
func (a *Agent) ptEnd() {
	s := a.pt
	if s == nil {
		return
	}
	a.pt = nil
	s.cancel()
	s.wg.Wait()
	for _, e := range s.ents {
		if !e.taken {
			s.discarded++
		}
	}
	if s.launched > 0 {
		dbg("parallel", map[string]any{"calls": len(s.calls), "segments": s.segments, "launched": s.launched, "used": s.used, "discarded": s.discarded})
	}
}

// ptClass: is the call read-class (no side effects; does not split a
// segment), and which lane would it use.
func (a *Agent) ptClass(tc ToolCall) (readClass, net bool, args map[string]any) {
	if a.mcp.Has(tc.Name) {
		return false, false, nil
	}
	args = acArgsMap(tc.Name, tc.Args)
	switch tc.Name {
	case "read", "todo":
		return true, false, args
	case "fetch":
		return a.tools.Web, true, args
	case "bash":
		if j, _ := argStr(args, "job"); strings.TrimSpace(j) != "" || args["action"] != nil || argBool(args, "background") {
			return false, false, args
		}
		cmd, _ := argStr(args, "command")
		cmd = strings.TrimSpace(cmd)
		return cmd != "" && IsReadOnly(cmd), false, args
	case "script":
		if !a.tools.Scripts {
			return false, false, args
		}
		p := a.tools.scPolicyArgs(args)
		return p.readOnly(), p.effect == "net", args
	}
	return false, false, args
}

// ptEligible: may this read-class call start before the gate looked at it.
func (a *Agent) ptEligible(tc ToolCall, net bool, args map[string]any) bool {
	if tc.Name == "todo" || a.Hooks.Has(HookPreToolUse) {
		return false
	}
	if dec, _ := a.Perms.Check(tc.Name, args); dec == PermDeny || dec == PermAsk {
		return false
	}
	cmd := ""
	if tc.Name == "bash" {
		cmd, _ = argStr(args, "command")
		cmd = strings.TrimSpace(cmd)
	}
	if a.fenceCheck(tc.Name, args, cmd, false) != "" {
		return false
	}
	mode := a.mode()
	switch tc.Name {
	case "read":
		p, _ := argStr(args, "path")
		if strings.TrimSpace(p) == "" || secretPath(a.tools.abs(p)) {
			return false
		}
	case "bash":
		if DangerReason(cmd) != "" || SecretReason(cmd) != "" || !a.tools.Shell.ptOneShotOK() {
			return false
		}
		if _, eg := EgressTarget(cmd); eg {
			return false
		}
	}
	if net || tc.Name == "script" {
		// network or script calls: only where the gate never asks or
		// classifies them, and never after a suspected injection
		if a.gr.injection != "" {
			return false
		}
		if net && mode != "auto" && mode != "turbo" && mode != "yolo" {
			return false
		}
		if !net && (mode == "ask" || mode == "smart") {
			return false
		}
	}
	return true
}

// launchFrom starts the segment that begins at call i: the read-class calls
// up to the next mutating call. It does nothing when i was already covered,
// when call i is a barrier, or when fewer than two calls of the segment can
// start early (one call alone gains nothing).
func (s *ptSched) launchFrom(i int) {
	if s == nil || i >= len(s.calls) || s.visited[i] {
		return
	}
	type cand struct {
		i   int
		net bool
	}
	var cands []cand
	hasBash := false
	for k := i; k < len(s.calls); k++ {
		rc, net, args := s.a.ptClass(s.calls[k])
		if !rc {
			break
		}
		s.visited[k] = true
		if net && !s.net || !net && !s.cpu {
			continue
		}
		if !s.a.ptEligible(s.calls[k], net, args) {
			continue
		}
		cands = append(cands, cand{k, net})
		hasBash = hasBash || s.calls[k].Name == "bash"
	}
	if len(cands) < 2 {
		return
	}
	var cwd string
	var env []string
	if hasBash {
		sh := s.a.tools.Shell
		if own := saShellFrom(s.ctx); own != nil {
			// a parallel subagent: its own shell's cwd and environment (subagent.go)
			sh = own
		}
		env = sh.ptEnv(s.ctx)
		cwd = sh.ptCwd()
	}
	win := 0
	if s.a.tools.Window != nil {
		win = s.a.tools.Window()
	}
	var order []*ptEntry
	var idx []int
	for _, c := range cands {
		tc := s.calls[c.i]
		if tc.Name == "bash" && env == nil {
			continue // no environment snapshot: bash keeps to the persistent shell
		}
		order = append(order, &ptEntry{name: tc.Name, args: tc.Args, net: c.net, done: make(chan struct{}),
			run: &ptRun{cwd: cwd, env: env, window: win}})
		idx = append(idx, c.i)
	}
	if len(order) < 2 {
		return
	}
	for j, e := range order {
		s.ents[idx[j]] = e
	}
	s.segments++
	s.launched += len(order)
	s.wg.Add(1)
	// one dispatcher per segment: workers start in the model's order, so the
	// call the loop waits on first is never starved by later ones
	safeGo(func() {
		defer s.wg.Done()
		for _, e := range order {
			select {
			case s.sem <- struct{}{}:
			case <-s.ctx.Done():
				e.retry = true
				close(e.done)
				continue
			}
			s.wg.Add(1)
			e := e
			safeGo(func() {
				defer s.wg.Done()
				defer func() { <-s.sem }()
				s.a.ptRunEntry(s.ctx, e)
			})
		}
	})
}

// ptRunEntry executes one early call.
func (a *Agent) ptRunEntry(ctx context.Context, e *ptEntry) {
	defer close(e.done)
	t0 := time.Now()
	ctx = context.WithValue(ctx, ptCtxKey{}, e.run)
	if ptTestHook != nil {
		ptTestHook(e.name, e.args)
	}
	e.out = a.tools.Exec(ctx, e.name, e.args)
	e.dur = time.Since(t0)
	if ctx.Err() != nil {
		e.retry = true // cancelled: never hand a half result to the model
		return
	}
	if e.name == "bash" && (e.run.oneShotCode == 127 || e.run.oneShotCode == -2) {
		e.retry = true
	}
}

// ptTake returns the early result of call idx when the gate let it run with
// the same name and arguments. ok=false: run it the normal way.
func (a *Agent) ptTake(ctx context.Context, idx int, tc ToolCall) (ToolOutput, time.Duration, bool) {
	s := a.pt
	if s == nil {
		return ToolOutput{}, 0, false
	}
	e := s.ents[idx]
	if e == nil || e.taken || e.name != tc.Name || e.args != tc.Args {
		return ToolOutput{}, 0, false
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return ToolOutput{}, 0, false
	}
	e.taken = true
	if e.retry {
		return ToolOutput{}, 0, false
	}
	s.used++
	out := e.out
	a.tools.ptCommit(e.run.eff, &out)
	return out, e.dur, true
}

// ptLaneOf is the lane call idx would use if it were taken now.
func (a *Agent) ptLaneOf(idx int, tc ToolCall) string {
	if s := a.pt; s != nil {
		if e := s.ents[idx]; e != nil && !e.taken && e.name == tc.Name && e.args == tc.Args {
			return ptLanePar
		}
	}
	return ptLaneSeq
}
