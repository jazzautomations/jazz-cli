//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ptTestSetup forces a multi-core machine and the given setting for one test.
func ptTestSetup(t *testing.T, mode string, ncpu int) {
	t.Helper()
	v, ok := ptParse(mode)
	if !ok {
		t.Fatalf("bad mode %q", mode)
	}
	prev := ptSetSettings(v)
	oldCPU := ptNumCPU
	ptNumCPU = func() int { return ncpu }
	t.Cleanup(func() {
		ptSetSettings(prev)
		ptNumCPU = oldCPU
		ptTestHook = nil
	})
}

// ptBarrier makes every early call wait until n of them are running at the
// same time (or the timeout passes) and records the peak.
type ptBarrier struct {
	n        int
	inFlight atomic.Int32
	peak     atomic.Int32
	mu       sync.Mutex
	cond     *sync.Cond
	arrived  int
}

func ptNewBarrier(n int) *ptBarrier {
	b := &ptBarrier{n: n}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *ptBarrier) hook(name, args string) {
	cur := b.inFlight.Add(1)
	for {
		p := b.peak.Load()
		if cur <= p || b.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	b.mu.Lock()
	b.arrived++
	b.cond.Broadcast()
	deadline := time.Now().Add(3 * time.Second)
	for b.arrived < b.n && time.Now().Before(deadline) {
		b.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		b.mu.Lock()
	}
	b.mu.Unlock()
	b.inFlight.Add(-1)
}

func TestPtParseAndLimits(t *testing.T) {
	for in, want := range map[string]string{"": "auto", "auto": "auto", "on": "on", "OFF": "off", "false": "off", "1": "off", "3": "n"} {
		v, ok := ptParse(in)
		if !ok || v.Mode != want {
			t.Errorf("ptParse(%q) = %+v %v, want %s", in, v, ok, want)
		}
	}
	for _, bad := range []string{"x", "17", "-2"} {
		if _, ok := ptParse(bad); ok {
			t.Errorf("ptParse(%q) accepted", bad)
		}
	}
	cases := []struct {
		mode     string
		ncpu, w  int
		cpu, net bool
	}{
		{"auto", 1, 2, false, true}, // Dell: only network calls overlap
		{"auto", 2, 4, true, true},
		{"auto", 8, 4, true, true},
		{"on", 1, 2, true, true},
		{"off", 8, 0, false, false},
		{"6", 2, 6, true, true},
	}
	for _, c := range cases {
		s, _ := ptParse(c.mode)
		w, cpu, net := ptLimits(s, c.ncpu)
		if w != c.w || cpu != c.cpu || net != c.net {
			t.Errorf("ptLimits(%s, %d) = %d %v %v, want %d %v %v", c.mode, c.ncpu, w, cpu, net, c.w, c.cpu, c.net)
		}
	}
}

func TestPtSettingsPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"tools":{"parallel":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if s := ptLoadSettings(cfg, get); s.Mode != "n" || s.N != 3 {
		t.Fatalf("config number: %+v", s)
	}
	env["JAZZ_PARALLEL_TOOLS"] = "off"
	if s := ptLoadSettings(cfg, get); s.Mode != "off" {
		t.Fatalf("env must win over config: %+v", s)
	}
	if s := ptLoadSettings(filepath.Join(dir, "none.json"), func(string) string { return "" }); s.Mode != "auto" {
		t.Fatalf("default: %+v", s)
	}
	// a full Config with tools.parallel still loads and saves (string or number)
	var c Config
	if err := json.Unmarshal([]byte(`{"tools":{"parallel":"on"}}`), &c); err != nil || c.Tools.Parallel != "on" {
		t.Fatalf("Config.Tools: %v %+v", err, c.Tools)
	}

	ptTestSetup(t, "auto", 4)
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	f := ptRegisterFlags(fs)
	if err := fs.Parse([]string{"--parallel-tools", "off"}); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(); err != nil || ptSettingsNow().Mode != "off" {
		t.Fatalf("flag: %v %+v", err, ptSettingsNow())
	}
	fs2 := flag.NewFlagSet("y", flag.ContinueOnError)
	f2 := ptRegisterFlags(fs2)
	_ = fs2.Parse([]string{"--parallel-tools", "banana"})
	if err := f2.apply(); err == nil {
		t.Fatal("invalid flag value accepted")
	}
	if !opValuedFlags["--parallel-tools"] {
		t.Fatal("reorderFlags must know --parallel-tools takes a value")
	}
}

// Reads and read-only bash of one reply run at the same time; results land
// in the original order and the events carry the lane.
func TestPtBatchRunsConcurrentlyInOrder(t *testing.T) {
	ptTestSetup(t, "auto", 4)
	b := ptNewBarrier(3)
	ptTestHook = b.hook
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "r1", Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt"})},
			{ID: "r2", Name: "bash", Args: jsonArgs(map[string]any{"command": "cat b.txt"})},
			{ID: "r3", Name: "read", Args: jsonArgs(map[string]any{"path": "c.txt"})},
		}},
		fakeResp{Text: "done"},
	), nil)
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(h.root, n+".txt"), []byte("content-"+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.run(t, "look"); err != nil {
		t.Fatal(err)
	}
	if p := b.peak.Load(); p < 3 {
		t.Fatalf("peak concurrency %d, want 3", p)
	}
	var ids []string
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
		}
	}
	if strings.Join(ids, ",") != "r1,r2,r3" {
		t.Fatalf("tool results out of order: %v", ids)
	}
	res := h.fs.requests()[1].toolResults()
	for id, want := range map[string]string{"r1": "content-a", "r2": "content-b", "r3": "content-c"} {
		if !strings.Contains(res[id], want) {
			t.Errorf("%s = %q, want %q", id, res[id], want)
		}
	}
	if !strings.Contains(res["r2"], "[exit code 0]") {
		t.Errorf("one-shot bash result lacks the exit code: %q", res["r2"])
	}
	// the early read still stamps the file: a later edit is not "unread"
	if !h.ag.tools.wasRead(filepath.Join(h.root, "a.txt")) {
		t.Error("read stamp of an early read was not applied")
	}
}

// A mutating call is a barrier: the read after an edit sees the edit, and
// nothing after the edit starts before it ran.
func TestPtBarrierAtMutation(t *testing.T) {
	ptTestSetup(t, "on", 4)
	var mu sync.Mutex
	var early []string
	ptTestHook = func(name, args string) {
		mu.Lock()
		early = append(early, name+" "+args)
		mu.Unlock()
	}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "a1", Name: "read", Args: jsonArgs(map[string]any{"path": "f.txt"})},
			{ID: "a2", Name: "read", Args: jsonArgs(map[string]any{"path": "g.txt"})},
			{ID: "e1", Name: "edit", Args: jsonArgs(map[string]any{"path": "f.txt", "old_string": "old", "new_string": "new"})},
			{ID: "a3", Name: "read", Args: jsonArgs(map[string]any{"path": "f.txt"})},
			{ID: "a4", Name: "bash", Args: jsonArgs(map[string]any{"command": "cat f.txt"})},
		}},
		fakeResp{Text: "done"},
	), nil)
	os.WriteFile(filepath.Join(h.root, "f.txt"), []byte("old\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "g.txt"), []byte("g\n"), 0o644)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()
	if !strings.Contains(res["a1"], "old") || strings.Contains(res["e1"], "Error") {
		t.Fatalf("a1=%q e1=%q", res["a1"], res["e1"])
	}
	if !strings.Contains(res["a3"], "new") || !strings.Contains(res["a4"], "new") {
		t.Fatalf("reads after the edit must see it: a3=%q a4=%q", res["a3"], res["a4"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(early) != 4 {
		t.Fatalf("want 4 early calls (2 before, 2 after the edit), got %v", early)
	}
	for _, e := range early {
		if strings.HasPrefix(e, "edit") {
			t.Fatal("an edit ran early")
		}
	}
}

// Off, a single call, a 1-CPU machine with only local calls: nothing early.
func TestPtNothingEarlyWhenNotUseful(t *testing.T) {
	for _, c := range []struct {
		name string
		mode string
		ncpu int
		n    int
	}{{"off", "off", 8, 3}, {"single", "auto", 8, 1}, {"onecpu", "auto", 1, 3}} {
		t.Run(c.name, func(t *testing.T) {
			ptTestSetup(t, c.mode, c.ncpu)
			var n atomic.Int32
			ptTestHook = func(string, string) { n.Add(1) }
			var calls []fakeCall
			for i := 0; i < c.n; i++ {
				calls = append(calls, fakeCall{ID: "c" + string(rune('0'+i)), Name: "read", Args: jsonArgs(map[string]any{"path": "x.txt"})})
			}
			h := newHarness(t, seq(fakeResp{Calls: calls}, fakeResp{Text: "ok"}), nil)
			os.WriteFile(filepath.Join(h.root, "x.txt"), []byte("x\n"), 0o644)
			if err := h.run(t, "go"); err != nil {
				t.Fatal(err)
			}
			if n.Load() != 0 {
				t.Fatalf("%d calls ran early", n.Load())
			}
		})
	}
}

// The gate still decides: a deny rule on one read refuses it (nothing of it
// reaches the model) and the other reads still overlap.
func TestPtGateStillDecides(t *testing.T) {
	ptTestSetup(t, "auto", 4)
	var mu sync.Mutex
	var early []string
	ptTestHook = func(name, args string) { mu.Lock(); early = append(early, args); mu.Unlock() }
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "p1", Name: "read", Args: jsonArgs(map[string]any{"path": "pub1.txt"})},
			{ID: "s1", Name: "read", Args: jsonArgs(map[string]any{"path": "secret.txt"})},
			{ID: "p2", Name: "read", Args: jsonArgs(map[string]any{"path": "pub2.txt"})},
			{ID: "k1", Name: "read", Args: jsonArgs(map[string]any{"path": ".env"})},
		}},
		fakeResp{Text: "ok"},
	), nil)
	h.ag.Perms = &PermSet{root: h.root, rules: []PermRule{{Kind: "deny", Rule: "Read(secret.txt)"}}}
	for _, n := range []string{"pub1.txt", "pub2.txt", "secret.txt", ".env"} {
		os.WriteFile(filepath.Join(h.root, n), []byte("SECRETVALUE-"+n+"\n"), 0o644)
	}
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()
	if strings.Contains(res["s1"], "SECRETVALUE") || !strings.Contains(strings.ToLower(res["s1"]), "block") {
		t.Fatalf("denied read leaked or not refused: %q", res["s1"])
	}
	if strings.Contains(res["k1"], "SECRETVALUE") {
		t.Fatalf(".env read without approval: %q", res["k1"])
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range early {
		if strings.Contains(a, "secret.txt") || strings.Contains(a, ".env") {
			t.Fatalf("a call that needs the gate ran early: %s", a)
		}
	}
	if len(early) != 2 {
		t.Fatalf("want the 2 public reads early, got %v", early)
	}
}

// An early read leaves no trace until it is taken: no read stamp, no
// AGENTS.md note consumed; a discarded one never applies them.
func TestPtEarlyReadDefersEffects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JAZZ_HOME", t.TempDir())
	tb := NewToolbox(root)
	t.Cleanup(tb.Shell.Close)
	sub := filepath.Join(root, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("use tabs"), 0o644)
	p := filepath.Join(sub, "f.go")
	os.WriteFile(p, []byte("package x\n"), 0o644)

	run := &ptRun{}
	ctx := context.WithValue(context.Background(), ptCtxKey{}, run)
	out := tb.Exec(ctx, "read", jsonArgs(map[string]any{"path": "sub/f.go"}))
	if strings.Contains(out.Text, "use tabs") || tb.wasRead(p) {
		t.Fatalf("early read applied its effects: wasRead=%v text=%q", tb.wasRead(p), out.Text)
	}
	tb.ptCommit(run.eff, &out)
	if !tb.wasRead(p) || !strings.Contains(out.Text, "use tabs") {
		t.Fatalf("commit did not apply the effects: %q", out.Text)
	}
	// the note is consumed once
	if again := tb.Exec(context.Background(), "read", jsonArgs(map[string]any{"path": "sub/f.go"})); strings.Contains(again.Text, "use tabs") {
		t.Fatal("AGENTS.md note injected twice")
	}
}

// Read-only bash runs in a short-lived shell with the persistent shell's cwd
// and exports, and leaves the persistent shell alone.
func TestPtOneShotShell(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	sh := NewShell(root)
	t.Cleanup(sh.Close)
	ctx := context.Background()
	if r := sh.Run(ctx, "cd sub && export PT_FOO=bar-42", 10*time.Second, nil); r.ExitCode != 0 {
		t.Fatalf("setup: %+v", r)
	}
	env := sh.ptEnv(ctx)
	if env == nil {
		t.Fatal("no env snapshot")
	}
	gen := sh.gen.Load()
	if again := sh.ptEnv(ctx); sh.gen.Load() != gen || len(again) != len(env) {
		t.Fatal("env snapshot not cached within a shell generation")
	}
	cwd := sh.ptCwd()
	r := sh.ptRunOnce(ctx, "pwd; echo $PT_FOO; type fd >/dev/null 2>&1; echo ok", cwd, env, 10*time.Second)
	if r.ExitCode != 0 || !strings.Contains(r.Output, "sub") || !strings.Contains(r.Output, "bar-42") {
		t.Fatalf("one-shot result: %+v", r)
	}
	if sh.gen.Load() != gen {
		t.Fatal("one-shot touched the persistent shell")
	}
	// timeout kills only the one-shot shell
	r = sh.ptRunOnce(ctx, "sleep 5", cwd, env, 300*time.Millisecond)
	if r.ExitCode != -1 || !strings.Contains(r.Note, "timed out") || r.Reset {
		t.Fatalf("timeout: %+v", r)
	}
	if p := sh.Run(ctx, "echo $PT_FOO", 10*time.Second, nil); !strings.Contains(p.Output, "bar-42") {
		t.Fatalf("persistent shell lost its state: %+v", p)
	}
	// cancellation
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if r := sh.ptRunOnce(cctx, "sleep 5", cwd, env, 10*time.Second); r.ExitCode != -1 || r.Duration > 3*time.Second {
		t.Fatalf("cancel: %+v", r)
	}
}

// Exit 127 in the one-shot shell (a name only the persistent shell knows)
// falls back to the persistent shell when the call is taken.
func TestPtOneShot127FallsBack(t *testing.T) {
	ptTestSetup(t, "on", 4)
	h := newHarness(t, seq(fakeResp{Text: "x"}), nil)
	calls := []ToolCall{
		{ID: "x1", Name: "bash", Args: jsonArgs(map[string]any{"command": "ls"})},
		{ID: "x2", Name: "bash", Args: jsonArgs(map[string]any{"command": "ls"})},
	}
	s := h.ag.ptBegin(context.Background(), calls)
	if s == nil {
		t.Fatal("no schedule")
	}
	h.ag.tools.Shell.ptMu.Lock()
	h.ag.tools.Shell.ptEnvVal = []string{"PATH=" + t.TempDir()} // no ls on this PATH
	h.ag.tools.Shell.ptEnvGen = h.ag.tools.Shell.gen.Load()
	h.ag.tools.Shell.ptMu.Unlock()
	s.launchFrom(0)
	if len(s.ents) != 2 {
		t.Fatalf("entries: %d", len(s.ents))
	}
	if _, _, ok := h.ag.ptTake(context.Background(), 0, calls[0]); ok {
		t.Fatal("a 127 result must not be taken")
	}
	h.ag.ptEnd()
}

// Calls still running when the batch stops are cancelled and discarded; no
// goroutine outlives ptEnd.
func TestPtEndDiscards(t *testing.T) {
	ptTestSetup(t, "on", 4)
	release := make(chan struct{})
	ptTestHook = func(string, string) { <-release }
	h := newHarness(t, seq(fakeResp{Text: "x"}), nil)
	os.WriteFile(filepath.Join(h.root, "a.txt"), []byte("a\n"), 0o644)
	calls := []ToolCall{
		{ID: "1", Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt"})},
		{ID: "2", Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt", "offset": 1})},
	}
	s := h.ag.ptBegin(context.Background(), calls)
	s.launchFrom(0)
	close(release)
	h.ag.ptEnd()
	if s.discarded != 2 || h.ag.pt != nil {
		t.Fatalf("discarded=%d pt=%v", s.discarded, h.ag.pt)
	}
	if h.ag.tools.wasRead(filepath.Join(h.root, "a.txt")) {
		t.Fatal("a discarded read left a read stamp")
	}
}

// Eligibility: network calls only where the gate never asks; MCP, writes and
// hooks are never early.
func TestPtEligibility(t *testing.T) {
	ptTestSetup(t, "auto", 4)
	h := newHarness(t, seq(fakeResp{Text: "x"}), nil)
	fetch := ToolCall{Name: "fetch", Args: jsonArgs(map[string]any{"url": "https://example.com"})}
	_, net, args := h.ag.ptClass(fetch)
	if !net {
		t.Fatal("fetch must be the network lane")
	}
	for mode, want := range map[string]bool{"auto": true, "yolo": true, "ask": false, "smart": false, "plan": false} {
		h.cfg.ModeOverride = mode
		if got := h.ag.ptEligible(fetch, true, args); got != want {
			t.Errorf("fetch in %s: %v, want %v", mode, got, want)
		}
	}
	h.cfg.ModeOverride = "auto"
	h.ag.gr.injection = "test"
	if h.ag.ptEligible(fetch, true, args) {
		t.Error("fetch early after a suspected injection")
	}
	h.ag.gr.injection = ""
	for _, tc := range []ToolCall{
		{Name: "write", Args: jsonArgs(map[string]any{"path": "a", "content": "b"})},
		{Name: "bash", Args: jsonArgs(map[string]any{"command": "rm -rf x"})},
		{Name: "bash", Args: jsonArgs(map[string]any{"command": "ls", "background": true})},
		{Name: "task", Args: `{"prompt":"x"}`},
	} {
		if rc, _, _ := h.ag.ptClass(tc); rc {
			t.Errorf("%s %s classed read-only", tc.Name, tc.Args)
		}
	}
	rd := ToolCall{Name: "read", Args: jsonArgs(map[string]any{"path": "x"})}
	_, _, ra := h.ag.ptClass(rd)
	if !h.ag.ptEligible(rd, false, ra) {
		t.Error("plain read not eligible")
	}
	if rd2 := (ToolCall{Name: "read", Args: jsonArgs(map[string]any{"path": "../../etc/hosts"})}); h.ag.ptEligible(rd2, false, acArgsMap("read", rd2.Args)) {
		t.Error("read outside the project (fence) eligible")
	}
	h.ag.Hooks = &HookSet{byEvent: map[string][]pfHookEntry{HookPreToolUse: {{HookMatcher: HookMatcher{Hooks: []HookCmd{{Type: "command", Command: "true"}}}}}}, root: h.root}
	if !h.ag.Hooks.Has(HookPreToolUse) || h.ag.ptEligible(rd, false, ra) {
		t.Error("early run with a PreToolUse hook")
	}
}

// Concurrency audit under -race: several parallel batches in a row with
// guards active (budget, honest guards, spiral breaker counters).
func TestPtRaceManyBatches(t *testing.T) {
	ptTestSetup(t, "on", 4)
	var resps []fakeResp
	for i := 0; i < 4; i++ {
		resps = append(resps, fakeResp{Calls: []fakeCall{
			{ID: "a" + string(rune('0'+i)), Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt", "offset": i + 1})},
			{ID: "b" + string(rune('0'+i)), Name: "bash", Args: jsonArgs(map[string]any{"command": "rg -n line ."})},
			{ID: "c" + string(rune('0'+i)), Name: "bash", Args: jsonArgs(map[string]any{"command": "ls -la"})},
			{ID: "d" + string(rune('0'+i)), Name: "read", Args: jsonArgs(map[string]any{"path": "missing.txt"})},
		}})
	}
	resps = append(resps, fakeResp{Text: "done"})
	h := newHarness(t, seq(resps...), nil)
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(filepath.Join(h.root, "a.txt"), []byte(b.String()), 0o644)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" {
			n++
		}
	}
	if n != 16 {
		t.Fatalf("%d tool results, want 16", n)
	}
}

// tool.start/tool.end carry the lane in the event stream.
func TestPtLaneInEvents(t *testing.T) {
	ptTestSetup(t, "auto", 4)
	cfg, root, _ := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "l1", Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt"})},
			{ID: "l2", Name: "read", Args: jsonArgs(map[string]any{"path": "b.txt"})},
		}},
		fakeResp{Calls: []fakeCall{{ID: "l3", Name: "read", Args: jsonArgs(map[string]any{"path": "a.txt"})}}},
		fakeResp{Text: "ok"},
	))
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o644)
	_, res := obRun(t, cfg, root, "look", pfOpts{})
	sid, _ := res["session_id"].(string)
	si, err := pfFindSession(root, sid)
	if err != nil {
		t.Fatal(err)
	}
	lanes := map[string]string{}
	for _, e := range obEvents(t, si.Path) {
		if k := e.str("kind"); k == "tool.end" || k == "tool.start" {
			if e.str("lane") == "" {
				t.Fatalf("%s without lane: %v", k, e)
			}
			if k == "tool.end" {
				lanes[e.str("id")] = e.str("lane")
			}
		}
	}
	if lanes["l1"] != ptLanePar || lanes["l2"] != ptLanePar || lanes["l3"] != ptLaneSeq {
		t.Fatalf("lanes = %v", lanes)
	}
}
