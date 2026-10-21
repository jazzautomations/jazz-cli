//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanCacheTerms(t *testing.T) {
	got := strings.Join(pcTerms("Corrija os testes do parser de configuração, please"), " ")
	if got != "corrija test parser configuracao" {
		t.Errorf("pcTerms = %q", got)
	}
	if a, b := pcTerms("fix the failing tests"), pcTerms("fix the failing test"); strings.Join(a, " ") != strings.Join(b, " ") {
		t.Errorf("plural should fold: %v vs %v", a, b)
	}
}

func TestPlanCacheMatch(t *testing.T) {
	plans := []pcPlan{
		{ID: "p1", Keys: pcTerms("fix the config parser crash on empty file"), Files: []string{"config.go"}},
		{ID: "p2", Keys: pcTerms("add dark mode to the settings page"), Files: []string{"ui/settings.tsx"}},
		{ID: "p3", Keys: pcTerms("parser crash when the config has comments"), Files: []string{"config.go", "parse.go"}, Wins: 3},
		{ID: "p4", Keys: pcTerms("update the readme")},
	}
	got := pcMatch(plans, "the config parser crashes again", 3)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d: %+v", len(got), got)
	}
	ids := got[0].ID + "," + got[1].ID
	if ids != "p3,p1" && ids != "p1,p3" {
		t.Errorf("wrong plans: %s", ids)
	}
	if m := pcMatch(plans, "deploy to production", 3); len(m) != 0 {
		t.Errorf("unrelated request matched: %+v", m)
	}
	if m := pcMatch(plans, "parser", 3); len(m) == 0 {
		t.Error("a one-word query that matches should find plans")
	}
	if m := pcMatch(plans, "config parser crash", 1); len(m) != 1 {
		t.Errorf("k=1 must cap the result: %d", len(m))
	}
	if m := pcMatch(plans, "a de que", 3); m != nil {
		t.Error("stopword-only query must not match")
	}
}

func TestPlanCacheRenderBudget(t *testing.T) {
	long := strings.Repeat("passo comprido ação ", 80)
	var plans []pcPlan
	for i := 0; i < 3; i++ {
		plans = append(plans, pcPlan{ID: fmt.Sprint(i), Task: long, Files: []string{"a.go", "b.go"}, Steps: []string{long, long}, Dead: []string{long}, Verify: "go test ./...", Hint: long, Wins: 2, NSteps: 20})
	}
	r := pcRender(plans)
	if len(r) > pcMaxChars+40 {
		t.Errorf("reminder is %d chars, budget %d", len(r), pcMaxChars)
	}
	if !strings.HasPrefix(r, "<system-reminder>Plan cache") || !strings.HasSuffix(r, "</system-reminder>") {
		t.Errorf("bad framing: %q", r[:60])
	}
	if !strings.Contains(r, "1. Task:") || !strings.Contains(r, "3. Task:") {
		t.Error("every plan should be listed")
	}
	if !utf8Valid(r) {
		t.Error("render cut a UTF-8 sequence")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestPlanCacheScoreAndEvict(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	if err := pcSave(root, []pcPlan{{ID: "a", Keys: []string{"x"}}, {ID: "b", Keys: []string{"y"}}}); err != nil {
		t.Fatal(err)
	}
	pcScore(root, []string{"a"}, true)
	pcScore(root, []string{"b"}, false)
	pl := pcLoad(root)
	if len(pl) != 2 || pl[0].Wins != 1 || pl[1].Fails != 1 {
		t.Fatalf("after one win/fail: %+v", pl)
	}
	pcScore(root, []string{"b"}, false)
	pl = pcLoad(root)
	if len(pl) != 1 || pl[0].ID != "a" {
		t.Fatalf("two fails must evict: %+v", pl)
	}
	if fi, err := os.Stat(pcPath(root)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("plan file should be 0600: %v", err)
	}
}

func TestPlanCacheStoreDedup(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	k := pcTerms("fix parser crash on empty config")
	pcStore(root, pcPlan{ID: "a", Keys: k, NSteps: 20, Wins: 1, Hint: "old hint"}, nil)
	pcStore(root, pcPlan{ID: "b", Keys: k, NSteps: 14, Wins: 1, Steps: []string{"edit parse.go"}}, nil)
	pl := pcLoad(root)
	if len(pl) != 1 {
		t.Fatalf("same task should refresh, not append: %+v", pl)
	}
	if pl[0].ID != "a" || pl[0].Wins != 2 || pl[0].NSteps != 14 || pl[0].Hint != "old hint" || len(pl[0].Steps) != 1 {
		t.Errorf("refresh kept the wrong fields: %+v", pl[0])
	}
	// a reused plan already got its win from pcScore
	pcStore(root, pcPlan{ID: "c", Keys: k, NSteps: 30, Wins: 1}, []string{"a"})
	if pl = pcLoad(root); pl[0].Wins != 2 || pl[0].NSteps != 14 {
		t.Errorf("reuse double-counted or a longer run replaced the plan: %+v", pl[0])
	}
	pcStore(root, pcPlan{ID: "d", Keys: pcTerms("write the release notes"), Wins: 1}, nil)
	if pl = pcLoad(root); len(pl) != 2 {
		t.Errorf("a different task should be appended: %d", len(pl))
	}
}

// longRun scripts n distinct bash steps, then a final answer.
func longRun(steps int, final string) func(int, recReq) fakeResp {
	return func(n int, r recReq) fakeResp {
		if n < steps {
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: fmt.Sprintf(`{"command":"echo step %d > out%d.txt"}`, n, n)}}}
		}
		return fakeResp{Text: final}
	}
}

// TestPlanCacheEndToEnd: a long verified run stores a plan; a similar request
// gets it injected after the user's text; two objective failures evict it.
func TestPlanCacheEndToEnd(t *testing.T) {
	var turnStart int
	var script func(int, recReq) fakeResp
	h := newHarness(t, func(n int, r recReq) fakeResp { return script(n-turnStart, r) }, func(c *Config) { c.MaxSteps = 30 })
	script = longRun(12, "parser fixed")
	h.ag.SetLoopOpts(LoopOpts{Until: "true"})
	if stop, err := h.ag.Run(context.Background(), "fix the config parser crash on empty files"); stop != StopDone || err != nil {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	pl := pcLoad(h.root)
	if len(pl) != 1 {
		t.Fatalf("a verified 13-step run should store one plan, got %d", len(pl))
	}
	p := pl[0]
	if p.Verify != "true" || p.NSteps != 13 || len(p.Files) != 0 || len(p.Steps) == 0 || !strings.Contains(strings.Join(p.Keys, " "), "parser") {
		t.Errorf("bad template: %+v", p)
	}
	if len(p.Steps) > pcMaxApproach {
		t.Errorf("approach not condensed: %d steps", len(p.Steps))
	}
	if p.Kind != lrTaskClass("fix the config parser crash on empty files") || p.Session == "" { // lessons_reuse.go
		t.Errorf("task class / session not stored: %+v", p)
	}

	// similar request → reminder after the user's text, in the same message
	turnStart = len(h.fs.requests())
	script = func(int, recReq) fakeResp { return fakeResp{Text: "tried"} }
	h.ag.SetLoopOpts(LoopOpts{Until: "false", UntilRounds: 1})
	if stop, _ := h.ag.Run(context.Background(), "the config parser crashes on empty input"); stop != StopUntilFailed {
		t.Fatalf("stop = %s", stop)
	}
	req := h.fs.requests()[turnStart]
	last := userContents(req)
	u := last[len(last)-1]
	if !strings.HasPrefix(strings.TrimSpace(acStripReminders(u)), "the config parser crashes") || strings.Index(u, "the config parser crashes") > strings.Index(u, "<system-reminder>Plan cache") || !strings.Contains(u, "<system-reminder>Plan cache") || !strings.Contains(u, "fix the config parser crash") {
		t.Errorf("plan not injected after the request:\n%s", u)
	}
	if !anyContains(h.ui.notices, "cache de planos: 1 plano") {
		t.Errorf("notice missing: %v", h.ui.notices)
	}
	if pl = pcLoad(h.root); len(pl) != 1 || pl[0].Fails != 1 {
		t.Fatalf("failure after reuse should count: %+v", pl)
	}

	// unrelated request: nothing injected, no accounting
	turnStart = len(h.fs.requests())
	h.ag.Run(context.Background(), "write release notes for version two")
	if u := userContents(h.fs.requests()[turnStart]); strings.Contains(u[len(u)-1], "Plan cache") {
		t.Error("unrelated request got a plan")
	}
	if pl = pcLoad(h.root); len(pl) != 1 || pl[0].Fails != 1 {
		t.Fatalf("unrelated failure must not count: %+v", pl)
	}

	turnStart = len(h.fs.requests())
	h.ag.Run(context.Background(), "parser crash with an empty config again")
	if pl = pcLoad(h.root); len(pl) != 0 {
		t.Fatalf("second failure after reuse should evict: %+v", pl)
	}
}

func TestPlanCacheNeedsVerifiedLongRun(t *testing.T) {
	cases := []struct {
		name  string
		steps int
		until string
		mod   func(*Config)
	}{
		{"short", 5, "true", nil},
		{"unverified", 12, "", nil},
		{"off", 12, "true", func(c *Config) { c.PlanCache = "off" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod := func(c *Config) {
				c.MaxSteps = 30
				if tc.mod != nil {
					tc.mod(c)
				}
			}
			h := newHarness(t, longRun(tc.steps, "done"), mod)
			h.ag.SetLoopOpts(LoopOpts{Until: tc.until})
			h.ag.Run(context.Background(), "refactor the storage layer")
			if n := len(pcLoad(h.root)); n != 0 {
				t.Errorf("stored %d plans", n)
			}
		})
	}
}

// TestPlanCacheVerifyBash: without --until, a final passing build/test bash
// counts as verification; an edit after it does not.
func TestPlanCacheVerifyBash(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch {
		case n < 11:
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: fmt.Sprintf(`{"command":"echo %d > f%d.txt"}`, n, n)}}}
		case n == 11:
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"make --version >/dev/null 2>&1 || true"}`}}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.MaxSteps = 30 })
	h.ag.Run(context.Background(), "tidy up the build scripts")
	pl := pcLoad(h.root)
	if len(pl) != 1 || !strings.Contains(pl[0].Verify, "make") {
		t.Fatalf("a passing verify bash should count: %+v", pl)
	}
}

// TestPlanCacheHindsight: with hindsight on and a strong model in the chain,
// one extra call writes the hint stored with the plan.
func TestPlanCacheHindsight(t *testing.T) {
	var asked []string
	old := hdChat
	hdChat = func(ctx context.Context, a *Agent, ref, system, user string) (string, error) {
		asked = append(asked, ref+"\n"+user)
		return "Edit parse.go: handle the empty buffer in readHeader. Skip config_test.go, it was fine. " + strings.Repeat("x", 2000), nil
	}
	defer func() { hdChat = old }()
	h := newHarness(t, longRun(12, "fixed"), func(c *Config) {
		c.MaxSteps = 30
		c.Hindsight = "on"
	})
	h.ag.SetLoopOpts(LoopOpts{Until: "true"})
	h.ag.Run(context.Background(), "fix the parser crash")
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "fake/m1\n") || !strings.Contains(asked[0], "fix the parser crash") || !strings.Contains(asked[0], "echo step 3") {
		t.Fatalf("hindsight call: %v", asked)
	}
	pl := pcLoad(h.root)
	if len(pl) != 1 || !strings.HasPrefix(pl[0].Hint, "Edit parse.go") || len([]rune(pl[0].Hint)) > hdMaxHint {
		t.Fatalf("hint not stored or not clipped: %+v", pl)
	}
	if r := pcRender(pl); !strings.Contains(r, "Hint: Edit parse.go") {
		t.Error("the best match should carry its hint")
	}
}

func TestPlanCacheHindsightOffOrWeak(t *testing.T) {
	calls := 0
	old := hdChat
	hdChat = func(context.Context, *Agent, string, string, string) (string, error) { calls++; return "hint", nil }
	defer func() { hdChat = old }()
	// default (off): no call
	h := newHarness(t, longRun(12, "fixed"), func(c *Config) { c.MaxSteps = 30 })
	h.ag.SetLoopOpts(LoopOpts{Until: "true"})
	h.ag.Run(context.Background(), "fix the parser crash")
	// on, but only a weak free model: no call
	h2 := newHarness(t, longRun(12, "fixed"), func(c *Config) {
		c.MaxSteps = 30
		c.Hindsight = "on"
		c.Omni.Paid = "never"
		c.Models = map[string]*ModelConf{"fake/m1": {Q: 50}}
	})
	h2.ag.SetLoopOpts(LoopOpts{Until: "true"})
	h2.ag.Run(context.Background(), "fix the parser crash")
	if calls != 0 {
		t.Errorf("hindsight ran %d times", calls)
	}
	if len(pcLoad(h2.root)) != 1 {
		t.Error("the plan is stored even without a hint")
	}
}

// TestRecoverDiscardRevertsBash: with shadow-git checkpoints, discard also
// reverts files bash created.
func TestRecoverDiscardRevertsBash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch {
		case n == 0:
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo x > made.txt"}`}}}
		case n <= 6:
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo same"}`}}}
		}
		return fakeResp{Text: "ok"}
	}, nil)
	h.ag.tools.Checkpoints = true
	h.ag.SetLoopOpts(LoopOpts{Recover: "discard"})
	if stop, _ := h.ag.Run(context.Background(), "go"); stop != StopDone {
		t.Fatalf("stop = %s", stop)
	}
	if !h.ag.tools.BashTracked() {
		t.Skip("checkpoints unavailable here")
	}
	if _, err := os.Stat(filepath.Join(h.root, "made.txt")); !os.IsNotExist(err) {
		t.Errorf("discard should remove the file bash created: %v", err)
	}
}
