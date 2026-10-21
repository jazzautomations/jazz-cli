//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// --until
// ---------------------------------------------------------------------------

func acCountStarts(u *recUI, prefix string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, s := range u.toolStarts {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func TestAgentUntilFailsThenPasses(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "all good"},
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"done.txt","content":"x"}`}}},
		fakeResp{Text: "now it is there"},
	), nil)
	h.ag.SetLoopOpts(LoopOpts{Until: "echo checking; test -f done.txt"})
	stop, err := h.ag.Run(context.Background(), "make the file")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	if !reqs[1].contains("user", "exit 1, check 1 of 5") || !reqs[1].contains("user", "$ echo checking; test -f done.txt") || !reqs[1].contains("user", "checking") {
		t.Errorf("until reminder missing: %v", reqs[1].last())
	}
	if n := acCountStarts(h.ui, "until "); n != 2 {
		t.Errorf("until ran %d times, want 2", n)
	}
	if !anyContains(h.ui.notices, "--until passou") {
		t.Errorf("success notice missing: %v", h.ui.notices)
	}
}

func TestAgentUntilRoundsExhausted(t *testing.T) {
	h := newHarness(t, func(int, recReq) fakeResp { return fakeResp{Text: "finished"} }, nil)
	h.ag.SetLoopOpts(LoopOpts{Until: "false", UntilRounds: 2})
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopUntilFailed || stop.ExitCode() != 8 {
		t.Fatalf("stop = %s (exit %d), want until_failed (8)", stop, stop.ExitCode())
	}
	if n := acCountStarts(h.ui, "until "); n != 2 {
		t.Errorf("until ran %d times, want 2", n)
	}
	if len(h.fs.requests()) != 2 {
		t.Errorf("requests = %d, want 2", len(h.fs.requests()))
	}
	if !anyContains(h.ui.warns, "ainda falha depois de 2 rodadas") {
		t.Errorf("warn missing: %v", h.ui.warns)
	}
	if len(h.ag.pendingNote) != 1 || !strings.Contains(h.ag.pendingNote[0], "still failed") {
		t.Errorf("next turn should learn the check is red: %v", h.ag.pendingNote)
	}
}

func TestAgentUntilBoundedByStepLimit(t *testing.T) {
	h := newHarness(t, func(int, recReq) fakeResp { return fakeResp{Text: "finished"} }, func(c *Config) { c.MaxSteps = 2 })
	h.ag.SetLoopOpts(LoopOpts{Until: "false", UntilRounds: 50})
	stop, _ := h.ag.Run(context.Background(), "go")
	if stop != StopStepLimit {
		t.Fatalf("stop = %s, want step_limit", stop)
	}
	if n := len(h.fs.requests()); n != 3 {
		t.Errorf("requests = %d, want 3 (2 steps + final)", n)
	}
}

func TestAgentUntilRunsAtRootAndKeepsShellState(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: `{"command":"mkdir -p sub && cd sub && export JZ_LOOP=1"}`}}},
		fakeResp{Text: "finished"},
	), nil)
	if err := os.WriteFile(filepath.Join(h.root, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// relative path works only at the root; the env var shows it is the same shell
	h.ag.SetLoopOpts(LoopOpts{Until: `test -f marker && test "$JZ_LOOP" = 1 && cd /`})
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v warns=%v", stop, err, h.ui.warns)
	}
	if !strings.HasSuffix(h.ag.tools.Shell.Cwd, string(filepath.Separator)+"sub") {
		t.Errorf("the check must not move the model's shell: cwd=%s", h.ag.tools.Shell.Cwd)
	}
}

func TestAgentUntilSkippedAfterDenial(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "d1", Name: "write", Args: `{"path":"a.txt","content":"a"}`}}},
		fakeResp{Text: "ok, I won't"},
	), func(c *Config) { c.Mode = "ask" })
	h.ag.SetLoopOpts(LoopOpts{Until: "false"})
	stop, _ := h.ag.Run(context.Background(), "go")
	if stop != StopDenied {
		t.Errorf("stop = %s, want denied", stop)
	}
	if n := acCountStarts(h.ui, "until "); n != 0 {
		t.Errorf("until must not run after the user denied a tool: %d", n)
	}
}

func TestReorderFlagsUntil(t *testing.T) {
	got := strings.Join(reorderFlags([]string{"fix it", "--until", "go test ./...", "--review", "--until-rounds", "3"}), "|")
	want := "--until|go test ./...|--review|--until-rounds|3|fix it"
	if got != want {
		t.Errorf("reorderFlags = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// /goal
// ---------------------------------------------------------------------------

func TestAgentGoalReminderSessionAndCompaction(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.isCompact() {
			return fakeResp{Text: "SUMMARY"}
		}
		return fakeResp{Text: "working"}
	}, nil)
	h.ag.SetGoal("ship the parser")
	if err := h.run(t, "first step please"); err != nil {
		t.Fatal(err)
	}
	r0 := h.fs.requests()[0]
	if !r0.contains("user", "Standing goal for this session (set by the user with /goal): ship the parser") {
		t.Errorf("goal reminder missing at turn start: %v", r0.last())
	}
	if strings.Contains(r0.system(), "ship the parser") {
		t.Errorf("the goal must never enter the system prompt (cache)")
	}
	// the turn that is the goal itself does not repeat it
	if err := h.run(t, "ship the parser"); err != nil {
		t.Fatal(err)
	}
	if c, _ := h.fs.requests()[1].last()["content"].(string); strings.Contains(c, "Standing goal") {
		t.Errorf("goal repeated in its own turn: %q", c)
	}

	// survives compaction
	h.ag.Msgs = append(h.ag.Msgs, Message{Role: "user", Content: "more"}, Message{Role: "assistant", Content: big(100000)})
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(h.ag.Msgs[0].Content, "SUMMARY") || !strings.Contains(h.ag.Msgs[0].Content, "ship the parser") {
		t.Errorf("goal lost by compaction: %q", oneLine(h.ag.Msgs[0].Content, 300))
	}

	// stored in the session JSONL and restored by LoadSession; fork carries it
	path := h.ag.sess.Path
	h.ag.sess.Close()
	ag2 := NewAgent(h.cfg, &recUI{}, h.ag.tools, nil, h.cfg.Model)
	if err := ag2.LoadSession(SessionInfo{Path: path}); err != nil {
		t.Fatal(err)
	}
	defer ag2.Close()
	if ag2.Goal() != "ship the parser" {
		t.Errorf("goal after LoadSession = %q", ag2.Goal())
	}
	if _, err := ag2.Fork(len(ag2.Msgs)); err != nil {
		t.Fatal(err)
	}
	if g := acLoadGoal(ag2.sess.Path); g != "ship the parser" {
		t.Errorf("fork lost the goal: %q", g)
	}
	ag2.SetGoal("")
	if g := acLoadGoal(ag2.sess.Path); g != "" {
		t.Errorf("cleared goal still stored: %q", g)
	}
}

func TestAgentGoalNotInSubagent(t *testing.T) {
	a := &Agent{sub: true}
	a.loop.goal = "x"
	if a.acGoalReminder() != "" || a.acTurnInput("hi") != "hi" {
		t.Errorf("subagents must not get the goal reminder")
	}
}

// ---------------------------------------------------------------------------
// --review
// ---------------------------------------------------------------------------

func acLoopGitRepo(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func acIsReviewer(r recReq) bool {
	return strings.HasPrefix(r.system(), "You are a research subagent")
}

func TestAgentReviewFeedsDefectsOnce(t *testing.T) {
	reviews := 0
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if acIsReviewer(r) {
			reviews++
			return fakeResp{Text: "1. b.go:1 the package name is wrong (b instead of a)."}
		}
		switch {
		case !r.contains("user", "reviewer") && len(r.toolResults()) == 0:
			return fakeResp{Calls: []fakeCall{
				{ID: "w1", Name: "write", Args: `{"path":"b.go","content":"package b\n"}`},
				{ID: "e1", Name: "bash", Args: `{"command":"echo '// changed' >> a.go"}`},
			}}
		default:
			return fakeResp{Text: "done"}
		}
	}, func(c *Config) { c.NoVerifyNudge = true })
	acLoopGitRepo(t, h.root)
	h.ag.SetLoopOpts(LoopOpts{Review: true})
	stop, err := h.ag.Run(context.Background(), "add package b")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v warns=%v", stop, err, h.ui.warns)
	}
	if reviews != 1 {
		t.Fatalf("reviewer requests = %d, want 1", reviews)
	}
	var rev, parent []recReq
	for _, r := range h.fs.requests() {
		if acIsReviewer(r) {
			rev = append(rev, r)
		} else {
			parent = append(parent, r)
		}
	}
	p, _ := rev[0].last()["content"].(string)
	if !strings.Contains(p, "add package b") || !strings.Contains(p, "Changed tracked files: a.go") || !strings.Contains(p, "New files (read them): b.go") || !strings.Contains(p, "git diff ") {
		t.Errorf("review prompt = %s", p)
	}
	if len(parent) != 3 {
		t.Fatalf("parent requests = %d, want 3", len(parent))
	}
	if !parent[2].contains("user", "the package name is wrong") || parent[2].contains("user", "[subagent used") {
		t.Errorf("defects not fed back cleanly: %v", parent[2].last())
	}
	if !anyContains(h.ui.notices, "revisão apontou problemas") {
		t.Errorf("notice missing: %v", h.ui.notices)
	}
	// the snapshot must not touch the repository state
	cmd := exec.Command("git", "stash", "list")
	cmd.Dir = h.root
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("review created a stash entry: %s", out)
	}
}

func TestAgentReviewLGTMAndNoChanges(t *testing.T) {
	reviews := 0
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if acIsReviewer(r) {
			reviews++
			return fakeResp{Text: "**LGTM**"}
		}
		if r.contains("user", "edit please") && len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"c.txt","content":"c"}`}}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	acLoopGitRepo(t, h.root)
	h.ag.SetLoopOpts(LoopOpts{Review: true})
	if err := h.run(t, "just answer"); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Errorf("a turn without changes must not be reviewed")
	}
	if err := h.run(t, "edit please"); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 || !anyContains(h.ui.notices, "revisão: LGTM") {
		t.Errorf("reviews=%d notices=%v", reviews, h.ui.notices)
	}
	// the untracked c.txt existed before this turn: not "new" any more
	if err := h.run(t, "just answer again"); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Errorf("unchanged turn reviewed again: %d", reviews)
	}
}

func TestAgentReviewOutsideGitIsSkipped(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"c.txt","content":"c"}`}}},
		fakeResp{Text: "done"},
	), nil)
	h.ag.SetLoopOpts(LoopOpts{Review: true})
	stop, err := h.ag.Run(context.Background(), "edit")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	for _, r := range h.fs.requests() {
		if acIsReviewer(r) {
			t.Fatal("no review outside a git repository")
		}
	}
}

func TestAcReviewLGTM(t *testing.T) {
	for in, want := range map[string]bool{
		"LGTM":                          true,
		"**LGTM**":                      true,
		"lgtm.":                         true,
		"LGTM — the change is minimal.": true,
		"LGTM\n1. a.go:3 off by one":    false,
		"LGTM, but:\n- missing test":    false,
		"1. a.go:3 off by one":          false,
		"Looks fine, LGTM":              false,
		"":                              false,
	} {
		if got := acReviewLGTM(in); got != want {
			t.Errorf("acReviewLGTM(%q) = %v, want %v", in, got, want)
		}
	}
	if got := acReviewReport("1. bug\n[subagent used 2 tools, 30 tokens]"); got != "1. bug" {
		t.Errorf("acReviewReport = %q", got)
	}
}
