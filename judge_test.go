package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ojResetDebug(t *testing.T) string {
	t.Helper()
	path, err := pfDebugStart()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pfDbg.mu.Lock()
		if pfDbg.f != nil {
			pfDbg.f.Close()
		}
		pfDbg.f, pfDbg.path, pfDbg.size, pfDbg.ring, pfDbg.sysSeen = nil, "", 0, nil, nil
		pfDbg.mu.Unlock()
		debugOn = false
	})
	return path
}

// ojRoutes returns the "route" records of the debug log.
func ojRoutes(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.Contains(l, `"route"`) {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		if m["kind"] != "route" {
			continue
		}
		if d, ok := m["data"].(map[string]any); ok {
			out = append(out, d)
		} else {
			out = append(out, m)
		}
	}
	return out
}

func ojResetFlags(t *testing.T, v ojFlagVals) {
	t.Helper()
	old := ojRun
	ojRun = v
	t.Cleanup(func() { ojRun = old })
	for _, e := range []string{"JAZZ_OMNI_JUDGE", "JAZZ_VERIFIER", "JAZZ_HEDGE", "JAZZ_PANEL", "JAZZ_FUSION", "JAZZ_JUDGE_EPSILON"} {
		t.Setenv(e, "")
	}
}

func ojResetDB() {
	ojDB.mu.Lock()
	ojDB.path, ojDB.base, ojDB.delta = "", nil, nil
	ojDB.mu.Unlock()
	bgScoreDB.mu.Lock()
	bgScoreDB.path, bgScoreDB.base, bgScoreDB.delta = "", nil, nil
	bgScoreDB.mu.Unlock()
	ojTakeAcc()
}

func TestOjPriorsFromEvals(t *testing.T) {
	c := &Config{}
	pp, n, wall, src := c.ojPriorFor("kilo/poolside/laguna-s-2.1:free", rtBigFile)
	if src != "eval" || n != 6 || wall != 280 || pp/n < 0.9 {
		t.Errorf("laguna big_file prior = %.2f/%.1f %.0f %s", pp, n, wall, src)
	}
	pp, n, _, src = c.ojPriorFor("kilo/minimax/minimax-m3.1-flash:free", rtShell)
	if src != "eval(any)" || n != 5 || pp/n > 0.7 {
		t.Errorf("minimax shell prior = %.2f/%.1f %s", pp, n, src)
	}
	if _, n, _, src = c.ojPriorFor("groq/openai/gpt-oss-120b", rtBugfix); src != "q" || n != 1 {
		t.Errorf("unknown model prior: n=%.1f src=%s", n, src)
	}
}

func TestOjRankUCBAndPenalties(t *testing.T) {
	cands := []ojCand{
		{Ref: "a/slowgood", Mean: 0.9, N: 6, Wall: 900, Ready: true, Free: true},
		{Ref: "a/fastgood", Mean: 0.9, N: 6, Wall: 60, Ready: true, Free: true},
		{Ref: "a/new", Mean: 0.5, N: 1, Ready: true, Free: true},
		{Ref: "a/busy", Mean: 0.95, N: 6, Ready: false, Free: true},
		{Ref: "p/paid", Mean: 0.99, N: 6, Ready: true, Free: false},
	}
	out, why := ojRank(append([]ojCand(nil), cands...), 0, nil)
	if why != "ucb" || out[0].Ref != "a/fastgood" || out[len(out)-1].Ref != "p/paid" {
		t.Errorf("order %v (%s)", refsOf(out), why)
	}
	if idx(out, "a/fastgood") > idx(out, "a/slowgood") || idx(out, "a/busy") < idx(out, "a/slowgood") {
		t.Errorf("latency/busy penalties not applied: %v", refsOf(out))
	}
	// the uncertain newcomer gets an exploration bonus over its mean
	for _, x := range out {
		if x.Ref == "a/new" && x.Score <= 0.5+0.3 {
			t.Errorf("UCB bonus too small: %.3f", x.Score)
		}
	}
	// ε: forced exploration picks another free, ready candidate
	calls := 0
	rnd := func() float64 { calls++; return 0.01 }
	out, why = ojRank(append([]ojCand(nil), cands...), 0.5, rnd)
	if why != "explore" || out[0].Ref == "a/fastgood" || !out[0].Free || !out[0].Ready {
		t.Errorf("explore: %v (%s)", refsOf(out), why)
	}
}

func refsOf(c []ojCand) []string {
	var s []string
	for _, x := range c {
		s = append(s, x.Ref)
	}
	return s
}

func idx(c []ojCand, ref string) int {
	for i, x := range c {
		if x.Ref == ref {
			return i
		}
	}
	return -1
}

func TestOjMeasuredBeatsPrior(t *testing.T) {
	pfTestHome(t)
	ojResetDB()
	c := &Config{}
	for i := 0; i < 8; i++ {
		ojRecord("kilo/poolside/laguna-s-2.1:free", rtFeature, ojStat{Fail: 1, Wall: 600})
		ojRecord("kilo/cohere/north-mini-code:free", rtFeature, ojStat{OK: 1, Wall: 120})
	}
	out, _ := ojRank(c.ojCands([]string{"kilo/poolside/laguna-s-2.1:free", "kilo/cohere/north-mini-code:free"}, rtFeature), 0, nil)
	if out[0].Ref != "kilo/cohere/north-mini-code:free" {
		t.Errorf("measured failures must outweigh the prior: %+v", out)
	}
	// another task type keeps laguna's prior
	out, _ = ojRank(c.ojCands([]string{"kilo/poolside/laguna-s-2.1:free", "kilo/cohere/north-mini-code:free"}, rtBigFile), 0, nil)
	if out[0].Ref != "kilo/poolside/laguna-s-2.1:free" {
		t.Errorf("per-type scores leaked: %+v", out)
	}
}

func TestOjOutcome(t *testing.T) {
	for _, c := range []struct {
		stop StopReason
		unv  bool
		want string
	}{
		{StopDone, false, "ok"}, {StopDone, true, "fail"}, {StopTimeout, false, "timeout"},
		{StopTruncated, false, "cutoff"}, {StopQualityFailed, false, "fail"}, {StopUntilFailed, false, "fail"},
		{StopStepLimit, false, "fail"}, {StopInterrupted, false, ""}, {StopDenied, false, ""},
	} {
		if got := ojOutcome(c.stop, c.unv); got != c.want {
			t.Errorf("ojOutcome(%s,%v) = %q, want %q", c.stop, c.unv, got, c.want)
		}
	}
}

func TestOjScoreboardPersists(t *testing.T) {
	pfTestHome(t)
	ojResetDB()
	bgScoreRecord("p/m", "quality", true)
	ojRecord("p/m", rtBugfix, ojStat{OK: 1, Reqs: 4, LatN: 4, MS: 4000, Wall: 100})
	bgScoresFlush()
	b, err := os.ReadFile(bgScoresPath())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		V     int                `json:"v"`
		Refs  map[string]bgScore `json:"refs"`
		Tasks map[string]ojStat  `json:"tasks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.V != 2 || f.Refs["p/m"].QualOK != 1 || f.Tasks["p/m|bugfix"].OK != 1 || f.Tasks["p/m|bugfix"].Reqs != 4 {
		t.Errorf("file = %s", b)
	}
	ojResetDB() // reload from disk
	if s := ojStatOf("p/m", rtBugfix); s.OK != 1 || s.MS != 4000 {
		t.Errorf("reloaded %+v", s)
	}
	var out bytes.Buffer
	ojPrintScores(&Config{}, &out)
	if !strings.Contains(out.String(), "p/m") || !strings.Contains(out.String(), "bugfix") || !strings.Contains(out.String(), "100%") || !strings.Contains(out.String(), "sementes") {
		t.Errorf("print:\n%s", out.String())
	}
}

// Several jazz processes (eval lanes) flush the same model-scores.json and
// ratelimit.json: nothing may be lost (flock + merge).
func TestOjScoresConcurrentProcesses(t *testing.T) {
	if os.Getenv("JAZZ_OJ_CHILD") != "" {
		ojChildMain(t)
		return
	}
	home := pfTestHome(t)
	ojResetDB()
	const procs, each = 4, 15
	var cmds []*exec.Cmd
	for i := 0; i < procs; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOjScoresConcurrentProcesses$", "-test.count=1")
		cmd.Env = append(os.Environ(), "JAZZ_OJ_CHILD="+strconv.Itoa(each), "JAZZ_HOME="+home)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
		t.Cleanup(func() {
			if t.Failed() {
				t.Log(buf.String())
			}
		})
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child: %v", err)
		}
	}
	ojResetDB()
	if s := ojStatOf("lane/m", rtFeature); s.OK != procs*each || s.Reqs != procs*each*2 {
		t.Errorf("tasks lost under concurrency: %+v (want ok=%d)", s, procs*each)
	}
	if s := bgScoreOf("lane/m"); s.QualOK != procs*each {
		t.Errorf("refs lost under concurrency: %+v", s)
	}
	m, err := rlReadFile(rlPath())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for k, b := range m {
		if strings.Contains(k, "lane") {
			total += b.Req
		}
	}
	if total != procs*each {
		t.Errorf("ratelimit.json requests = %d, want %d (%v)", total, procs*each, m)
	}
}

func ojChildMain(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("JAZZ_OJ_CHILD"))
	cfg := &Config{Providers: map[string]*ProviderConf{"lane": {Type: "openai", NoKey: true, Limits: &Limits{RPD: 100000, Scope: "key"}}}}
	for i := 0; i < n; i++ {
		ojRecord("lane/m", rtFeature, ojStat{OK: 1, Reqs: 2})
		bgScoreRecord("lane/m", "quality", true)
		bgScoresFlush()
		cfg.rlBegin("lane/m", 10)
		cfg.rlEnd("lane/m", 10, Usage{In: 5, Out: 5}, nil)
		FlushRateLimits()
		time.Sleep(time.Millisecond)
	}
}

func TestOjHedgeFirstWinsAndCancelsLoser(t *testing.T) {
	pfTestHome(t)
	var cancelled atomic.Int32
	old := ojChatFn
	t.Cleanup(func() { ojChatFn = old })
	ojChatFn = func(ctx context.Context, a *Agent, ref string, req ChatRequest) (*ChatResponse, error) {
		d := 10 * time.Millisecond
		if ref == "p/slow" {
			d = 2 * time.Second
		}
		select {
		case <-time.After(d):
			return &ChatResponse{Text: "answer from " + ref}, nil
		case <-ctx.Done():
			cancelled.Add(1)
			return nil, ctx.Err()
		}
	}
	a := &Agent{cfg: &Config{}, ui: &recUI{}}
	t0 := time.Now()
	resp, ref, err := a.ojHedge(context.Background(), "test", []string{"p/slow", "p/fast"}, true, 5*time.Second, func(ref, mid string) ChatRequest { return ChatRequest{Model: mid} })
	if err != nil || ref != "p/fast" || !strings.Contains(resp.Text, "p/fast") {
		t.Fatalf("hedge: %v %s %v", resp, ref, err)
	}
	if time.Since(t0) > time.Second || cancelled.Load() != 1 {
		t.Errorf("loser not cancelled: %s, cancelled=%d", time.Since(t0), cancelled.Load())
	}
	// no hedge: strictly sequential, the first answers
	resp, ref, _ = a.ojHedge(context.Background(), "test", []string{"p/slow", "p/fast"}, false, 5*time.Second, func(ref, mid string) ChatRequest { return ChatRequest{Model: mid} })
	if ref != "p/slow" {
		t.Errorf("sequential: %s", ref)
	}
	// a failing first group falls through to the next candidates
	ojChatFn = func(ctx context.Context, a *Agent, ref string, req ChatRequest) (*ChatResponse, error) {
		if ref != "p/c" {
			return nil, errors.New("boom")
		}
		return &ChatResponse{Text: "ok"}, nil
	}
	if _, ref, err = a.ojHedge(context.Background(), "test", []string{"p/a", "p/b", "p/c"}, true, time.Second, func(ref, mid string) ChatRequest { return ChatRequest{} }); ref != "p/c" || err != nil {
		t.Errorf("fallthrough: %s %v", ref, err)
	}
}

func TestOjLineDiff(t *testing.T) {
	d := ojLineDiff("a\nb\nc\nd\ne\n", "a\nb\nX\nd\ne\n", 1)
	if !strings.Contains(d, "- c") || !strings.Contains(d, "+ X") || !strings.Contains(d, "  b") || strings.Contains(d, "  a") {
		t.Errorf("diff:\n%s", d)
	}
	if ojLineDiff("same", "same", 3) != "" {
		t.Error("no change must be empty")
	}
}

// End to end with the fake server: the judge picks the editor by task type,
// logs the routing decision, and records the outcome for the model used.
func TestOjJudgePicksAndRecords(t *testing.T) {
	ojResetFlags(t, ojFlagVals{judge: "on", hedge: "off"})
	h := newHarness(t, seq(fakeResp{Text: "Feito: nada a mudar."}), func(c *Config) {
		c.Providers["fake"].Free = true
		c.Fallback = []string{"fake/m2"}
		c.Judge.Epsilon = new(float64)
	})
	ojResetDB()
	log := ojResetDebug(t)
	for i := 0; i < 6; i++ { // m2 is measured good at bugfix, m1 bad
		ojRecord("fake/m2", rtBugfix, ojStat{OK: 1, Wall: 30})
		ojRecord("fake/m1", rtBugfix, ojStat{Fail: 1, Wall: 30})
	}
	if err := h.run(t, "Os testes em calc_test.py falham. Corrija o bug em calc.py"); err != nil {
		t.Fatal(err)
	}
	if n := h.fs.countModel("m2"); n != 1 {
		t.Errorf("judge did not route to m2: m1=%d m2=%d", h.fs.countModel("m1"), n)
	}
	if s := ojStatOf("fake/m2", rtBugfix); s.OK != 7 || s.Reqs != 1 {
		t.Errorf("outcome not recorded for the model used: %+v", s)
	}
	var roles []string
	for _, r := range ojRoutes(t, log) {
		roles = append(roles, fmt.Sprint(r["role"]))
		if r["role"] == "editor" && (r["chosen"] != "fake/m2" || r["reason"] != "ucb" || r["cands"] == nil) {
			t.Errorf("editor record: %v", r)
		}
	}
	if strings.Join(roles, ",") != "classify,editor,outcome" {
		t.Errorf("route records: %v", roles)
	}
}

// With the judge off the model stays, but the turn is still classified and scored.
func TestOjJudgeOffStillRecords(t *testing.T) {
	ojResetFlags(t, ojFlagVals{})
	h := newHarness(t, seq(fakeResp{Text: "É uma função de soma."}), func(c *Config) { c.Fallback = []string{"fake/m2"} })
	ojResetDB()
	if err := h.run(t, "explique o que calc.py faz"); err != nil {
		t.Fatal(err)
	}
	if h.fs.countModel("m1") != 1 {
		t.Error("judge off must keep the configured model")
	}
	if s := ojStatOf("fake/m1", rtExplain); s.OK != 1 {
		t.Errorf("not recorded: %+v", s)
	}
}

// --verifier: a second model reviews the diff once; its defects go back.
func TestOjVerifierFeedsDefectsOnce(t *testing.T) {
	ojResetFlags(t, ojFlagVals{judge: "on", verifier: "on", hedge: "off"})
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m2" {
			return fakeResp{Text: "1. a.py: f returns 2, the request asked for 1"}
		}
		switch n {
		case 0:
			return fakeResp{Calls: []fakeCall{{ID: "w", Name: "write", Args: jsonArgs(map[string]string{"path": "a.py", "content": "def f():\n    return 2\n"})}}}
		default:
			return fakeResp{Text: "pronto"}
		}
	}, func(c *Config) {
		c.Mode = "yolo"
		c.NoVerifyNudge = true
		c.Quality = qgOff
		c.Providers["fake"].Free = true
		c.Fallback = []string{"fake/m2"}
		c.Judge.Epsilon = new(float64)
		c.Roles.Editor = "fake/m1"
	})
	ojResetDB()
	if err := h.run(t, "crie a.py com f que devolve 1"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	found := false
	for _, r := range reqs {
		if r.Model == "m1" && r.contains("user", "A second model (fake/m2) reviewed your changes") {
			found = true
		}
	}
	if !found || h.fs.countModel("m2") != 1 {
		t.Errorf("verifier feedback missing: m2 requests=%d", h.fs.countModel("m2"))
	}
}

// --fusion: the lead delegates to the sidekick, which edits in its own
// context; the edit is registered on the lead's turn.
func TestOjFusionSidekickEdits(t *testing.T) {
	ojResetFlags(t, ojFlagVals{judge: "on", fusion: "on", hedge: "off"})
	var sideReqs atomic.Int32
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m2" { // sidekick
			k := sideReqs.Add(1)
			if k == 1 {
				if !r.contains("user", "Goal: write b.py") || r.contains("user", "Fusion mode") {
					return fakeResp{Text: "bad handoff"}
				}
				return fakeResp{Calls: []fakeCall{{ID: "sw", Name: "write", Args: jsonArgs(map[string]string{"path": "b.py", "content": "X = 1\n"})}}}
			}
			return fakeResp{Text: "wrote b.py; no tests here"}
		}
		if n == 0 {
			hasSide := false
			for _, tl := range r.Tools {
				if strings.Contains(fmt.Sprint(tl), "sidekick") {
					hasSide = true
				}
			}
			if !hasSide {
				return fakeResp{Text: "no sidekick tool offered"}
			}
			if !r.contains("user", "Fusion mode: you are the lead") {
				return fakeResp{Text: "no lead note"}
			}
			return fakeResp{Calls: []fakeCall{{ID: "sk", Name: "sidekick", Args: ojArgsJSON(map[string]string{"goal": "write b.py with X = 1", "files": "b.py", "acceptance": "python3 -c 'import b'"})}}}
		}
		return fakeResp{Text: "b.py criado pelo sidekick e revisado."}
	}, func(c *Config) {
		c.Mode = "yolo"
		c.NoVerifyNudge = true
		c.Providers["fake"].Free = true
		c.Fallback = []string{"fake/m2"}
		c.Fusion.Lead = "fake/m1"
		c.Fusion.Sidekick = "fake/m2"
	})
	ojResetDB()
	if err := h.run(t, "adicione b.py com X = 1"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(h.root, "b.py")); err != nil || string(b) != "X = 1\n" {
		t.Fatalf("sidekick did not write: %q %v", b, err)
	}
	last := lastMsg(h.ag.Msgs)
	if !strings.Contains(last.Content, "revisado") {
		t.Errorf("lead did not finish: %+v", last)
	}
	var toolOut string
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" && m.ToolName == "sidekick" {
			toolOut = m.Content
		}
	}
	if !strings.Contains(toolOut, "files changed by the sidekick: b.py") || !strings.Contains(toolOut, "wrote b.py") {
		t.Errorf("sidekick report: %q", toolOut)
	}
	if st := h.ag.oj.side; st == nil || len(st.Msgs) < 3 {
		t.Error("sidekick context must persist on the lead")
	}
}

// Cold start on the keyless kilo chain: the measured model leads every type.
func TestOjColdStartPrefersMeasured(t *testing.T) {
	pfTestHome(t)
	ojResetDB()
	c := &Config{Models: presetModelMap()}
	refs := []string{"kilo/poolside/laguna-s-2.1:free", "kilo/minimax/minimax-m3.1-flash:free", "kilo/cohere/north-mini-code:free",
		"kilo/nvidia/nemotron-3-super-120b-a12b:free", "kilo/nvidia/nemotron-3.5-lightning:free"}
	for _, typ := range rtTypes {
		out, _ := ojRank(c.ojCands(refs, typ), 0, nil)
		if out[0].Ref != refs[0] {
			t.Errorf("%s: cold start picked %s (%v)", typ, out[0].Ref, refsOf(out))
		}
		if testing.Verbose() {
			var s []string
			for _, x := range out {
				s = append(s, fmt.Sprintf("%s=%.2f", x.Ref[5:20], x.Score))
			}
			t.Logf("%-10s %s", typ, strings.Join(s, " "))
		}
	}
}

// --panel: a size-L request gets a plan from 2–3 models merged by the turn's model.
func TestOjPanelPlanForLargeTask(t *testing.T) {
	ojResetFlags(t, ojFlagVals{judge: "on", panel: "on", hedge: "off"})
	var panel, synth atomic.Int32
	h := newHarness(t, func(n int, r recReq) fakeResp {
		sys := r.system()
		switch {
		case strings.Contains(sys, "You plan a coding task"):
			panel.Add(1)
			return fakeResp{Text: "1. read notas.py\n2. add the field (" + r.Model + ")"}
		case strings.Contains(sys, "You merge independent answers"):
			synth.Add(1)
			return fakeResp{Text: "Agree on notas.py. Plan: 1. read notas.py 2. add field"}
		}
		if r.contains("user", "Suggested plan") && r.contains("user", "Agree on notas.py") {
			return fakeResp{Text: "ok, plano recebido"}
		}
		return fakeResp{Text: "sem plano"}
	}, func(c *Config) {
		c.Providers["fake"].Free = true
		c.Fallback = []string{"fake/m2", "fake/m3"}
		c.Judge.Epsilon = new(float64)
		c.Roles.Editor = "fake/m1"
	})
	ojResetDB()
	if err := h.run(t, "Adicione prioridade às notas:\n- add aceita --prioridade\n- novo comando prioridade\n- list filtra\n- ordem por prioridade\nAtualize os testes."); err != nil {
		t.Fatal(err)
	}
	if panel.Load() != 3 || synth.Load() != 1 {
		t.Errorf("panel=%d synth=%d, want 3 and 1", panel.Load(), synth.Load())
	}
	if last := lastMsg(h.ag.Msgs); !strings.Contains(last.Content, "plano recebido") {
		t.Errorf("plan not injected: %q", last.Content)
	}
}

// A headless --timeout cancels the turn: it is scored as a timeout.
func TestOjEndTurnHeadlessTimeout(t *testing.T) {
	pfTestHome(t)
	ojResetDB()
	old := ojTimedOut
	t.Cleanup(func() { ojTimedOut = old })
	ojTimedOut = func() bool { return true }
	a := &Agent{cfg: &Config{}, ui: &recUI{}, Model: "p/m", turnModel: "p/m", oj: &ojState{typed: true, task: rtTask{Type: rtFeature}, t0: time.Now()}}
	a.ojEndTurn(StopInterrupted)
	if s := ojStatOf("p/m", rtFeature); s.Timeout != 1 {
		t.Errorf("timeout not recorded: %+v", s)
	}
	ojTimedOut = func() bool { return false }
	a.oj.typed = true
	a.ojEndTurn(StopInterrupted)
	if s := ojStatOf("p/m", rtFeature); s.turns() != 1 {
		t.Errorf("a user interruption must not be scored: %+v", s)
	}
}

// Closer to the live run: the lead reads and runs a command first, the
// sidekick edits an existing file and creates another; both are registered.
func TestOjFusionSidekickEditsExisting(t *testing.T) {
	ojResetFlags(t, ojFlagVals{judge: "on", fusion: "on", hedge: "off"})
	var sideReqs atomic.Int32
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m2" {
			switch sideReqs.Add(1) {
			case 1:
				return fakeResp{Calls: []fakeCall{{ID: "se", Name: "edit", Args: jsonArgs(map[string]string{"path": "a.py", "old_string": "X = 1", "new_string": "X = 2"})}}}
			case 2:
				return fakeResp{Calls: []fakeCall{{ID: "sw", Name: "write", Args: jsonArgs(map[string]string{"path": "tests/test_a.py", "content": "import a\n"})}}}
			case 3: // the live sidekick wrote files with cat > in bash
				return fakeResp{Calls: []fakeCall{{ID: "sb", Name: "bash", Args: jsonArgs(map[string]string{"command": "printf 'Y = 1\\n' > c.py"})}}}
			}
			return fakeResp{Text: "done"}
		}
		switch n {
		case 0:
			return fakeResp{Calls: []fakeCall{{ID: "r", Name: "read", Args: jsonArgs(map[string]string{"path": "a.py"})}, {ID: "b", Name: "bash", Args: jsonArgs(map[string]string{"command": "ls"})}}}
		case 1:
			return fakeResp{Calls: []fakeCall{{ID: "sk", Name: "sidekick", Args: ojArgsJSON(map[string]string{"goal": "set X to 2 and add a test"})}}}
		}
		return fakeResp{Text: "ok"}
	}, func(c *Config) {
		c.Mode = "yolo"
		c.NoVerifyNudge = true
		c.Quality = qgOff
		c.Providers["fake"].Free = true
		c.Fallback = []string{"fake/m2"}
		c.Fusion.Lead = "fake/m1"
		c.Fusion.Sidekick = "fake/m2"
	})
	os.WriteFile(filepath.Join(h.root, "a.py"), []byte("X = 1\n"), 0o644)
	ojResetDB()
	if err := h.run(t, "corrija o bug em a.py: X deve ser 2"); err != nil {
		t.Fatal(err)
	}
	var toolOut string
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" && m.ToolName == "sidekick" {
			toolOut = m.Content
		}
	}
	if !strings.Contains(toolOut, "a.py, c.py, tests/test_a.py") {
		for _, m := range h.ag.Msgs {
			t.Logf("%s %s %q %v", m.Role, m.ToolName, oneLine(m.Content, 200), m.ToolCalls)
		}
		t.Errorf("changes not registered: %q", toolOut)
	}
}
