package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// saIsChild: the request comes from a subagent.
func saIsChild(r recReq) bool {
	s := r.system()
	return strings.HasPrefix(s, "You are a research subagent") || strings.HasPrefix(s, "You are a worker subagent")
}

// saGoal: the tag (GOAL-x) in the child's first user message.
func saGoal(r recReq) string {
	for _, m := range r.Messages {
		if m["role"] != "user" {
			continue
		}
		c, _ := m["content"].(string)
		if i := strings.Index(c, "GOAL-"); i >= 0 {
			return c[i : i+6]
		}
		return ""
	}
	return ""
}

// saConc tracks how many children are inside the fake provider at once.
type saConc struct {
	cur, max atomic.Int32
}

func (c *saConc) enter() {
	n := c.cur.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			return
		}
	}
}
func (c *saConc) leave() { c.cur.Add(-1) }

func saTaskCall(id, goal string) fakeCall {
	return fakeCall{ID: id, Name: "task", Args: jsonArgs(map[string]any{"description": "item " + goal, "prompt": "Investigate " + goal})}
}

// parentToolOrder: the tool_call_ids of the tool messages in the parent's
// second request, in order.
func parentToolOrder(r recReq) []string {
	var ids []string
	for _, m := range r.Messages {
		if m["role"] == "tool" {
			id, _ := m["tool_call_id"].(string)
			ids = append(ids, id)
		}
	}
	return ids
}

func TestSubagentsFanOutThree(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	var conc saConc
	var linesSeen atomic.Int32
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			conc.enter()
			defer conc.leave()
			if len(SubagentStatusLines()) == 3 {
				linesSeen.Add(1)
			}
			time.Sleep(300 * time.Millisecond)
			g := saGoal(r)
			return fakeResp{Text: "REPORT " + g, UsageIn: 100, UsageOut: 10}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B"), saTaskCall("t3", "GOAL-C")}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	t0 := time.Now()
	if err := h.run(t, "explain the architecture"); err != nil {
		t.Fatal(err)
	}
	wall := time.Since(t0)
	if conc.max.Load() != 3 {
		t.Errorf("max concurrent children = %d, want 3", conc.max.Load())
	}
	if wall > 2*time.Second {
		t.Errorf("3 children of 300 ms took %s: not parallel", wall)
	}
	if linesSeen.Load() == 0 {
		t.Error("SubagentStatusLines must show the 3 running children")
	}
	if SubagentStatusLines() != nil {
		t.Errorf("status lines left after the fan-out: %v", SubagentStatusLines())
	}
	var parent []recReq
	for _, r := range h.fs.requests() {
		if !saIsChild(r) {
			parent = append(parent, r)
		}
	}
	if len(parent) != 2 {
		t.Fatalf("parent requests = %d", len(parent))
	}
	if got := strings.Join(parentToolOrder(parent[1]), ","); got != "t1,t2,t3" {
		t.Errorf("tool results order = %s", got)
	}
	res := parent[1].toolResults()
	for id, g := range map[string]string{"t1": "GOAL-A", "t2": "GOAL-B", "t3": "GOAL-C"} {
		if !strings.Contains(res[id], "REPORT "+g) || strings.Count(res[id], "REPORT") != 1 {
			t.Errorf("%s result = %q", id, res[id])
		}
	}
	if h.ag.Usage.In < 300 || h.ag.Usage.Out < 30 {
		t.Errorf("children usage must reach the parent: %+v", h.ag.Usage)
	}
	// child sessions: a branch of the parent's tree, never listed as sessions
	sub := sessSubDir(h.ag.sess.Path)
	ents, err := os.ReadDir(sub)
	if err != nil || len(ents) != 3 {
		t.Fatalf("child sessions in %s: %v %v", sub, ents, err)
	}
	b, _ := os.ReadFile(filepath.Join(sub, ents[0].Name()))
	var meta sessEntry
	_ = json.Unmarshal([]byte(strings.SplitN(string(b), "\n", 2)[0]), &meta)
	if meta.T != "meta" || meta.Parent != h.ag.SessionID || meta.Sub == "" || meta.ParentEntry == "" {
		t.Errorf("child meta = %+v", meta)
	}
	for _, si := range ListSessions(h.root) {
		if si.ID != h.ag.SessionID {
			t.Errorf("child session listed: %s", si.ID)
		}
	}
	// events carry the sub ids
	ev, _ := os.ReadFile(obEventsPath(h.ag.sess.Path))
	ids := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(ev)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(l), &e) == nil && e["kind"] == "request.end" {
			if s, _ := e["sub_id"].(string); s != "" {
				ids[s] = true
			}
		}
	}
	if len(ids) != 3 {
		t.Errorf("request.end events with sub_id: %v", ids)
	}
}

func TestSubagentsTasksArrayKeepsOrder(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	delay := map[string]time.Duration{"GOAL-A": 400 * time.Millisecond, "GOAL-B": 50 * time.Millisecond, "GOAL-C": 200 * time.Millisecond}
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			g := saGoal(r)
			time.Sleep(delay[g])
			return fakeResp{Text: "REPORT " + g}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: jsonArgs(map[string]any{"tasks": []any{
				map[string]any{"goal": "look at GOAL-A", "description": "dir a"},
				map[string]any{"goal": "look at GOAL-B", "description": "dir b"},
				map[string]any{"goal": "look at GOAL-C", "description": "dir c"},
			}})}}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	if err := h.run(t, "one subagent per directory"); err != nil {
		t.Fatal(err)
	}
	var last recReq
	for _, r := range h.fs.requests() {
		if !saIsChild(r) {
			last = r
		}
	}
	res := last.toolResults()["t1"]
	a, b, c := strings.Index(res, "REPORT GOAL-A"), strings.Index(res, "REPORT GOAL-B"), strings.Index(res, "REPORT GOAL-C")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Errorf("joined report out of order: %q", res)
	}
	for _, s := range []string{"## [1/3] dir a", "## [2/3] dir b", "## [3/3] dir c"} {
		if !strings.Contains(res, s) {
			t.Errorf("missing heading %q in %q", s, res)
		}
	}
}

func TestSubagentsParallelLimit(t *testing.T) {
	for _, tc := range []struct {
		par  string
		want int32
	}{{"2", 2}, {"1", 1}} {
		t.Run("parallel="+tc.par, func(t *testing.T) {
			t.Setenv("JAZZ_SUBAGENTS_PARALLEL", tc.par)
			var conc saConc
			var omu sync.Mutex
			var order []string
			h := newHarness(t, func(n int, r recReq) fakeResp {
				if saIsChild(r) {
					omu.Lock()
					order = append(order, saGoal(r))
					omu.Unlock()
					conc.enter()
					defer conc.leave()
					time.Sleep(150 * time.Millisecond)
					return fakeResp{Text: "REPORT " + saGoal(r)}
				}
				if len(r.toolResults()) == 0 {
					return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B"), saTaskCall("t3", "GOAL-C")}}
				}
				return fakeResp{Text: "done"}
			}, nil)
			if err := h.run(t, "go"); err != nil {
				t.Fatal(err)
			}
			if conc.max.Load() != tc.want {
				t.Errorf("max concurrent = %d, want %d", conc.max.Load(), tc.want)
			}
			if tc.par == "1" && strings.Join(order, ",") != "GOAL-A,GOAL-B,GOAL-C" {
				t.Errorf("parallel=1 must run the items in order: %v", order)
			}
		})
	}
}

func TestSubagentsParallelConfig(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "")
	saFlagParallel = 0
	cfg := &Config{Providers: map[string]*ProviderConf{
		"remote": {Type: "openai", BaseURL: "https://api.example.com/v1"},
		"lan":    {Type: "openai", BaseURL: "http://192.168.0.9:8080/v1", Local: true},
		"lo":     {Type: "openai", BaseURL: "http://127.0.0.1:8088/v1"},
	}}
	a := &Agent{cfg: cfg}
	if n := a.saParallel("remote/x"); n != 3 {
		t.Errorf("remote auto = %d", n)
	}
	if n := a.saParallel("lan/x"); n != 2 {
		t.Errorf("local auto = %d", n)
	}
	if n := a.saParallel("lo/x"); n != 2 {
		t.Errorf("loopback auto = %d", n)
	}
	cfg.Subagents.Parallel = 1
	if n := a.saParallel("remote/x"); n != 1 {
		t.Errorf("config = %d", n)
	}
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "4")
	if n := a.saParallel("remote/x"); n != 4 {
		t.Errorf("env = %d", n)
	}
	saFlagParallel = 5
	defer func() { saFlagParallel = 0 }()
	if n := a.saParallel("remote/x"); n != 5 {
		t.Errorf("flag = %d", n)
	}
}

func TestSubagentsBudgetSplit(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	var childReqs atomic.Int32
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			childReqs.Add(1)
			// 2000 tokens per request: over the 1000-token share after one
			return fakeResp{Calls: []fakeCall{{ID: "c" + saGoal(r), Name: "bash", Args: `{"command":"ls"}`}}, UsageIn: 2000}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B"), saTaskCall("t3", "GOAL-C")}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.MaxTurnTokens = 3000 })
	_ = h.run(t, "go")
	if n := childReqs.Load(); n != 3 {
		t.Errorf("child requests = %d, want 1 each (share of the cap)", n)
	}
	if h.ag.Usage.In < 6000 {
		t.Errorf("parent totals must include children: %+v", h.ag.Usage)
	}
	// the share itself
	a := &Agent{cfg: &Config{MaxTurnTokens: 9000, MaxTurnCost: 3}}
	a.turnUsage = Usage{In: 2000, Out: 1000, Cost: 0.6}
	c := a.saSplitCap(3)
	if c.tokens != 2000 || c.cost < 0.79 || c.cost > 0.81 {
		t.Errorf("cap = %+v", *c)
	}
	k := &Agent{sub: true, sa: saChild{cap: c}}
	k.turnUsage = Usage{In: 2001}
	if k.overBudget() == "" {
		t.Error("child over its share must stop")
	}
	k.turnUsage = Usage{In: 1999}
	if k.overBudget() != "" {
		t.Error("child under its share must go on")
	}
}

func TestSubagentsSameFileSerialized(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	var mu sync.Mutex
	span := map[string][2]time.Time{}
	note := func(g string, end bool) {
		mu.Lock()
		defer mu.Unlock()
		s := span[g]
		if end {
			s[1] = time.Now()
		} else if s[0].IsZero() {
			s[0] = time.Now()
		}
		span[g] = s
	}
	files := map[string]string{"GOAL-A": "a.txt", "GOAL-B": "a.txt", "GOAL-C": "c.txt"}
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			g := saGoal(r)
			if len(r.toolResults()) == 0 {
				note(g, false)
				time.Sleep(200 * time.Millisecond)
				return fakeResp{Calls: []fakeCall{{ID: "w" + g, Name: "write", Args: jsonArgs(map[string]any{"path": files[g], "content": g + "\n"})}}}
			}
			note(g, true)
			return fakeResp{Text: "wrote " + files[g]}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: jsonArgs(map[string]any{"tasks": []any{
				map[string]any{"goal": "GOAL-A write", "files": []any{"a.txt"}},
				map[string]any{"goal": "GOAL-B write", "files": "a.txt"},
				map[string]any{"goal": "GOAL-C write", "files": []any{"c.txt"}},
			}})}}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	a, b, c := span["GOAL-A"], span["GOAL-B"], span["GOAL-C"]
	if a[1].IsZero() || b[0].IsZero() || c[0].IsZero() {
		t.Fatalf("spans = %v", span)
	}
	if b[0].Before(a[1]) {
		t.Errorf("B (same file) started %s before A ended", a[1].Sub(b[0]))
	}
	if !c[0].Before(a[1]) {
		t.Error("C (other file) must run alongside A")
	}
	got, _ := os.ReadFile(filepath.Join(h.root, "a.txt"))
	if string(got) != "GOAL-B\n" {
		t.Errorf("a.txt = %q (B must write after A)", got)
	}
	var reqs []recReq
	for _, r := range h.fs.requests() {
		if saIsChild(r) {
			reqs = append(reqs, r)
		}
	}
	for _, r := range reqs {
		if !strings.HasPrefix(r.system(), "You are a worker subagent") {
			t.Errorf("a child with files must be a worker")
		}
		names := strings.Join(r.toolNames(), ",")
		if !strings.Contains(names, "write") || strings.Contains(names, "task") || strings.Contains(names, "todo") {
			t.Errorf("worker tools = %s", names)
		}
	}
}

func TestSubagentsPathLock(t *testing.T) {
	l := newSaLocks()
	ctx := context.Background()
	if m := l.acquire(ctx, "sub1", "/p/x", "x", nil); m != "" {
		t.Fatal(m)
	}
	if m := l.acquire(ctx, "sub2", "/p/y", "y", nil); m != "" {
		t.Fatal(m)
	}
	got := make(chan string, 1)
	go func() { got <- l.acquire(ctx, "sub2", "/p/x", "x", nil) }() // waits for sub1
	time.Sleep(50 * time.Millisecond)
	select {
	case m := <-got:
		t.Fatalf("sub2 must wait for sub1, got %q", m)
	default:
	}
	// sub1 now wants y, owned by sub2 who waits for sub1: refused, no deadlock
	if m := l.acquire(ctx, "sub1", "/p/y", "y", nil); !strings.Contains(m, "PATH_LOCKED") {
		t.Fatalf("cycle must be refused, got %q", m)
	}
	l.release("sub1")
	select {
	case m := <-got:
		if m != "" {
			t.Fatalf("after release sub2 gets x, got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sub2 never woke up")
	}
	// cancellation wakes a waiter
	cctx, cancel := context.WithCancel(ctx)
	go l.wakeOn(cctx)
	go func() { got <- l.acquire(cctx, "sub3", "/p/x", "x", nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case m := <-got:
		if !strings.Contains(m, "cancelled") {
			t.Fatalf("cancelled wait = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not wake the waiter")
	}
}

func TestSubagentsUndeclaredSamePathWaits(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "2")
	var mu sync.Mutex
	var order []string
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			g := saGoal(r)
			switch len(r.toolResults()) {
			case 0:
				if g == "GOAL-B" {
					time.Sleep(100 * time.Millisecond) // A writes shared.txt first
				}
				return fakeResp{Calls: []fakeCall{{ID: "s" + g, Name: "write", Args: jsonArgs(map[string]any{"path": "shared.txt", "content": g + "\n"})}}}
			default:
				mu.Lock()
				order = append(order, g)
				mu.Unlock()
				if g == "GOAL-A" {
					time.Sleep(300 * time.Millisecond) // A keeps working; B must still wait
				}
				return fakeResp{Text: "ok " + g}
			}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: jsonArgs(map[string]any{"tasks": []any{
				map[string]any{"goal": "GOAL-A", "files": []any{"a.txt"}},
				map[string]any{"goal": "GOAL-B", "files": []any{"b.txt"}},
			}})}}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "GOAL-A,GOAL-B" {
		t.Errorf("B's write to the path A owns must wait for A to end: %v", order)
	}
	got, _ := os.ReadFile(filepath.Join(h.root, "shared.txt"))
	if string(got) != "GOAL-B\n" {
		t.Errorf("shared.txt = %q", got)
	}
}

func TestSubagentsCancelKeepsPartialWork(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	var started sync.WaitGroup
	started.Add(3)
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			started.Done()
			return fakeResp{Text: "PARTIAL " + saGoal(r), HangAfter: true}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B"), saTaskCall("t3", "GOAL-C")}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		started.Wait()
		time.Sleep(200 * time.Millisecond) // let the partial text arrive
		cancel()
	}()
	stop, _ := h.ag.Run(ctx, "go")
	if stop != StopInterrupted {
		t.Errorf("stop = %s", stop)
	}
	got := map[string]string{}
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" {
			got[m.ToolCallID] = m.Content
		}
	}
	for id, g := range map[string]string{"t1": "GOAL-A", "t2": "GOAL-B", "t3": "GOAL-C"} {
		if !strings.Contains(got[id], "interrupted") || !strings.Contains(got[id], "PARTIAL "+g) {
			t.Errorf("%s = %q", id, got[id])
		}
	}
	// the partial replies are on disk in the child sessions
	ents, _ := os.ReadDir(sessSubDir(h.ag.sess.Path))
	if len(ents) != 3 {
		t.Fatalf("child sessions = %d", len(ents))
	}
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(sessSubDir(h.ag.sess.Path), e.Name()))
		if !strings.Contains(string(b), "PARTIAL GOAL-") || !strings.Contains(string(b), `"t":"end"`) {
			t.Errorf("child session %s lacks the partial reply or end: %s", e.Name(), b)
		}
	}
}

func TestSubagentsSoftStopNotStarted(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "1")
	h := newHarness(t, nil, nil)
	h.ag.SoftStop("test")
	out := h.ag.runTask(context.Background(), ToolCall{ID: "x", Name: "task", Args: `{"tasks":[{"goal":"GOAL-A"},{"goal":"GOAL-B"}]}`})
	if strings.Count(out.Text, "TOOL_NOT_STARTED") != 2 {
		t.Errorf("past the deadline grace no child starts: %q", out.Text)
	}
	if h.fs != nil && len(h.fs.requests()) != 0 {
		t.Errorf("requests = %d", len(h.fs.requests()))
	}
}

func TestSubagentsResearchStaysReadOnlyInParallel(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "2")
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			if len(r.toolResults()) == 0 {
				return fakeResp{Calls: []fakeCall{
					{ID: "w" + saGoal(r), Name: "write", Args: `{"path":"evil.txt","content":"x"}`},
					{ID: "b" + saGoal(r), Name: "bash", Args: `{"command":"cd / && pwd"}`},
				}}
			}
			return fakeResp{Text: "report " + saGoal(r)}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B")}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "evil.txt")); err == nil {
		t.Error("a research child wrote a file")
	}
	// the children's shells are their own: the parent's cwd is untouched
	if h.ag.tools.Shell.Cwd != h.root {
		t.Errorf("parent shell cwd = %s", h.ag.tools.Shell.Cwd)
	}
}

func TestSubagentsPlanModeIsResearch(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "2")
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			if !strings.HasPrefix(r.system(), "You are a research subagent") {
				return fakeResp{Text: "WRONG worker in plan mode"}
			}
			return fakeResp{Text: "report"}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: `{"tasks":[{"goal":"GOAL-A","files":["a.go"]},{"goal":"GOAL-B","files":["b.go"]}]}`}}}
		}
		return fakeResp{Text: "plan"}
	}, func(c *Config) { c.ModeOverride = "plan" })
	if err := h.run(t, "plan it"); err != nil {
		t.Fatal(err)
	}
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "WRONG") {
			t.Errorf("plan mode must give research children: %q", m.Content)
		}
	}
}

func TestSubagentsHeadlessStatusLines(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "2")
	old := saStatusEvery
	saStatusEvery = 50 * time.Millisecond
	defer func() { saStatusEvery = old }()
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			time.Sleep(250 * time.Millisecond)
			return fakeResp{Text: "report"}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B")}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	h.ag.Headless = true
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if !anyContains(h.ui.notices, "subagente sub") || !anyContains(h.ui.notices, "item GOAL-A") {
		t.Errorf("headless status lines missing: %v", h.ui.notices)
	}
}

func TestSubagentsParseSpecs(t *testing.T) {
	sp, msg := saParseSpecs(`{"prompt":"find X","files":"a.go, b.go\nc.go","acceptance":"go test"}`)
	if msg != "" || len(sp) != 1 || strings.Join(sp[0].Files, "|") != "a.go|b.go|c.go" || sp[0].Acceptance != "go test" || !sp[0].work() {
		t.Errorf("single = %+v %q", sp, msg)
	}
	sp, msg = saParseSpecs(`{"tasks":"[{\"goal\":\"g1\"},{\"goal\":\"g2\",\"files\":[\"x\"]}]"}`)
	if msg != "" || len(sp) != 2 || sp[0].work() || !sp[1].work() {
		t.Errorf("tasks as string = %+v %q", sp, msg)
	}
	sp, _ = saParseSpecs(`{"tasks":["plain one","plain two"]}`)
	if len(sp) != 2 || sp[1].Goal != "plain two" {
		t.Errorf("string items = %+v", sp)
	}
	if _, msg = saParseSpecs(`{"tasks":[{"files":["x"]}]}`); msg == "" {
		t.Error("an item without goal must fail")
	}
	if _, msg = saParseSpecs(`{}`); !strings.Contains(msg, "prompt") {
		t.Errorf("empty = %q", msg)
	}
	if _, msg = saParseSpecs(`{"description":"only a label"}`); msg != "" {
		t.Errorf("description falls back as the prompt: %q", msg)
	}
}

func TestSubagentsTaskSpecStable(t *testing.T) {
	b1, _ := json.Marshal(taskSpec)
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "1")
	b2, _ := json.Marshal(taskSpec)
	if string(b1) != string(b2) || !strings.Contains(string(b1), `"tasks"`) {
		t.Error("the task spec must not depend on the config")
	}
}

func TestSubagentsTasksArraySequentialInOrder(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "1")
	var mu sync.Mutex
	var order []string
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			mu.Lock()
			order = append(order, saGoal(r))
			mu.Unlock()
			return fakeResp{Text: "REPORT " + saGoal(r)}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: `{"tasks":[{"goal":"GOAL-A"},{"goal":"GOAL-B"},{"goal":"GOAL-C"}]}`}}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "GOAL-A,GOAL-B,GOAL-C" {
		t.Errorf("order = %v", order)
	}
}
