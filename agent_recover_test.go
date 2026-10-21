//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userContents returns the content of every user message of a request.
func userContents(r recReq) []string {
	var out []string
	for _, m := range r.Messages {
		if m["role"] == "user" {
			c, _ := m["content"].(string)
			out = append(out, c)
		}
	}
	return out
}

// TestRecoverLoopHandsOff: the repetition guard hands the turn off to a clean
// context instead of stopping; the model then finishes.
func TestRecoverLoopHandsOff(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 6 {
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo same"}`}}}
		}
		return fakeResp{Text: "done another way"}
	}, nil)
	stop, err := h.ag.Run(context.Background(), "build the widget")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v warns=%v", stop, err, h.ui.warns)
	}
	reqs := h.fs.requests()
	if len(reqs) != 7 {
		t.Fatalf("requests = %d, want 7 (6 looping + 1 after the handoff)", len(reqs))
	}
	after := reqs[6]
	if len(after.Messages) != 2 || after.Messages[0]["role"] != "system" {
		t.Fatalf("after the handoff the history must be system + note, got %d messages", len(after.Messages))
	}
	if after.system() != reqs[0].system() {
		t.Error("the system prompt must stay byte-identical across the handoff")
	}
	note := userContents(after)[0]
	for _, want := range []string{"Handoff note", "build the widget", "kept repeating the same tool call", "Do not repeat `bash echo same`", "bash echo same → exit 0", "The files on disk keep its changes"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if got := strings.Count(note, "\n1. "); got != 1 {
		t.Errorf("trail should be numbered once: %d", got)
	}
	if h.ag.LastRecovery() != rcWhyLoop {
		t.Errorf("LastRecovery = %q", h.ag.LastRecovery())
	}
	if !anyContains(h.ui.warns, "recomecei com contexto limpo") {
		t.Errorf("warn missing: %v", h.ui.warns)
	}
	b, _ := os.ReadFile(h.ag.sess.Path)
	if !strings.Contains(string(b), `"t":"recover"`) || !strings.Contains(string(b), `"summary":"loop"`) {
		t.Error("the handoff reason must be recorded in the session file")
	}
	// -c resumes from the note, not from the looping history
	s2, msgs, _, _, err := LoadSession(h.ag.sess.Path)
	if err != nil || len(msgs) == 0 || !strings.Contains(msgs[0].Content, "Handoff note") {
		t.Errorf("resumed history should start with the note: err=%v n=%d", err, len(msgs))
	}
	s2.Close()
}

// TestRecoverOncePerTurn: a second stall in the same turn stops as before.
func TestRecoverOncePerTurn(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo same"}`}}}
	}, nil)
	stop, _ := h.ag.Run(context.Background(), "go")
	if stop != StopLoop {
		t.Fatalf("stop = %s, want loop", stop)
	}
	if n := len(h.fs.requests()); n != 12 {
		t.Errorf("requests = %d, want 12 (one handoff, then the guard stops)", n)
	}
}

// TestRecoverNeedsStepsLeft: no handoff when the step budget is nearly spent.
func TestRecoverNeedsStepsLeft(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo same"}`}}}
	}, func(c *Config) { c.MaxSteps = 7 })
	stop, _ := h.ag.Run(context.Background(), "go")
	if stop != StopLoop || h.ag.LastRecovery() != "" {
		t.Fatalf("stop = %s recovery=%q, want a plain loop stop", stop, h.ag.LastRecovery())
	}
}

// TestRecoverToolErrorStreak: 8 failing tool calls in a row hand off.
func TestRecoverToolErrorStreak(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 8 {
			return fakeResp{Calls: []fakeCall{{Name: "read", Args: fmt.Sprintf(`{"path":"missing-%d.txt"}`, n)}}}
		}
		return fakeResp{Text: "ok"}
	}, nil)
	stop, err := h.ag.Run(context.Background(), "read the config")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 9 {
		t.Fatalf("requests = %d, want 9", len(reqs))
	}
	note := userContents(reqs[8])[0]
	if len(reqs[8].Messages) != 2 || !strings.Contains(note, "last 8 tool calls all failed") || !strings.Contains(note, "missing-7.txt") {
		t.Errorf("bad handoff after the error streak:\n%s", note)
	}
	if h.ag.LastRecovery() != rcWhyErrors {
		t.Errorf("LastRecovery = %q", h.ag.LastRecovery())
	}
}

// TestRecoverStreakResetBySuccess: errors split by a success do not hand off.
func TestRecoverStreakResetBySuccess(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch {
		case n == 4:
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo fine"}`}}}
		case n < 10:
			return fakeResp{Calls: []fakeCall{{Name: "read", Args: fmt.Sprintf(`{"path":"missing-%d.txt"}`, n)}}}
		}
		return fakeResp{Text: "ok"}
	}, nil)
	if stop, _ := h.ag.Run(context.Background(), "go"); stop != StopDone || h.ag.LastRecovery() != "" {
		t.Fatalf("stop=%s recovery=%q", stop, h.ag.LastRecovery())
	}
}

// TestRecoverUntilSameFailure: the same --until failure 3 times in a row
// hands off with the check output in the note; failures did not grow, so
// files are kept.
func TestRecoverUntilSameFailure(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"a.txt","content":"x"}`}}},
	), nil)
	h.ag.SetLoopOpts(LoopOpts{Until: "echo '--- FAIL: TestWidget (0.01s)'; false"})
	stop, err := h.ag.Run(context.Background(), "fix TestWidget")
	if err != nil || stop != StopUntilFailed {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	// 1 write + 3 stops (until 1..3) → handoff → 2 stops (until 4, 5)
	if len(reqs) != 6 {
		t.Fatalf("requests = %d, want 6", len(reqs))
	}
	if len(reqs[4].Messages) != 2 {
		t.Fatalf("request after the handoff should hold only system + note: %d", len(reqs[4].Messages))
	}
	note := userContents(reqs[4])[0]
	for _, want := range []string{"completion check failed 3 times", "--- FAIL: TestWidget", "(exit 1)", "- a.txt", "keep its changes", "write a.txt → ok"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "a.txt")); err != nil {
		t.Error("handoff without growing failures must keep the files")
	}
	if n := acCountStarts(h.ui, "until "); n != 5 {
		t.Errorf("until ran %d times, want 5 (rounds are not reset)", n)
	}
}

// TestRecoverUntilDiscardWhenFailuresGrew: more failures than the first
// round → the turn's files are reverted before the handoff.
func TestRecoverUntilDiscardWhenFailuresGrew(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"target.txt","content":"v1"}`}}},
		fakeResp{Text: "done"}, // until 1: one failure
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: `{"path":"target.txt","content":"v2"}`}}},
		fakeResp{Text: "done"}, // until 2: two failures
		fakeResp{Text: "done"}, // until 3: same
		fakeResp{Text: "done"}, // until 4: same → handoff with discard
		fakeResp{Text: "done"}, // until 5: exhausted
	), nil)
	if err := os.WriteFile(filepath.Join(h.root, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.ag.SetLoopOpts(LoopOpts{Until: `if grep -q v1 target.txt 2>/dev/null; then echo "FAIL one"; else echo "FAIL one"; echo "FAIL two"; fi; false`})
	stop, _ := h.ag.Run(context.Background(), "make it pass")
	if stop != StopUntilFailed {
		t.Fatalf("stop = %s", stop)
	}
	if h.ag.LastRecovery() != rcWhyUntil {
		t.Fatalf("LastRecovery = %q", h.ag.LastRecovery())
	}
	if _, err := os.Stat(filepath.Join(h.root, "target.txt")); !os.IsNotExist(err) {
		t.Errorf("target.txt was created by the turn and should be gone after the discard: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "keep.txt")); string(b) != "mine" {
		t.Error("files the turn did not touch must stay")
	}
	reqs := h.fs.requests()
	note := userContents(reqs[6])[0]
	if !strings.Contains(note, "Previous attempt reverted") || !strings.Contains(note, "(now reverted)") {
		t.Errorf("note should say the attempt was reverted:\n%s", note)
	}
	if !anyContains(h.ui.warns, "desfiz as mudanças do turno") {
		t.Errorf("warn missing: %v", h.ui.warns)
	}
}

// TestRecoverHandoffModeNeverReverts: --recover handoff keeps the files even
// when failures grew.
func TestRecoverHandoffModeNeverReverts(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"target.txt","content":"v1"}`}}},
		fakeResp{Text: "done"},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: `{"path":"target.txt","content":"v2"}`}}},
	), nil)
	h.ag.SetLoopOpts(LoopOpts{Until: `if grep -q v1 target.txt 2>/dev/null; then echo "FAIL one"; else echo "FAIL one"; echo "FAIL two"; fi; false`, Recover: "handoff"})
	h.ag.Run(context.Background(), "make it pass")
	if h.ag.LastRecovery() != rcWhyUntil {
		t.Fatalf("LastRecovery = %q", h.ag.LastRecovery())
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "target.txt")); string(b) != "v2" {
		t.Errorf("handoff mode must keep the files: %q", b)
	}
}

// TestRecoverOff: --recover off keeps the old behaviour.
func TestRecoverOff(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "finished"} }, nil)
	h.ag.SetLoopOpts(LoopOpts{Until: "echo FAIL; false", Recover: "off"})
	stop, _ := h.ag.Run(context.Background(), "go")
	if stop != StopUntilFailed || h.ag.LastRecovery() != "" {
		t.Fatalf("stop=%s recovery=%q", stop, h.ag.LastRecovery())
	}
	for i, r := range h.fs.requests() {
		if i > 0 && len(r.Messages) < 3 {
			t.Errorf("request %d: history was replaced with recovery off", i)
		}
	}
}

func TestRecoverModeResolution(t *testing.T) {
	t.Setenv("JAZZ_RECOVER", "")
	a := &Agent{cfg: &Config{}}
	if a.rcMode() != "on" {
		t.Errorf("default = %q, want on", a.rcMode())
	}
	a.cfg.Recover = "handoff"
	if a.rcMode() != "handoff" {
		t.Errorf("config = %q", a.rcMode())
	}
	t.Setenv("JAZZ_RECOVER", "off")
	if a.rcMode() != "off" {
		t.Errorf("env should beat config: %q", a.rcMode())
	}
	a.loop.opts.Recover = "discard"
	if a.rcMode() != "discard" {
		t.Errorf("flag should beat env: %q", a.rcMode())
	}
	for _, s := range []string{"on", "off", "handoff", "discard", "OFF"} {
		if !rcValidMode(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	if rcValidMode("maybe") {
		t.Error("maybe is not a mode")
	}
}

func TestRecoverHelpers(t *testing.T) {
	a := rcUntilSig("--- FAIL: TestX (0.01s)\nok pkg 1.2s\nFAIL", 1)
	b := rcUntilSig("--- FAIL: TestX (0.37s)\nok pkg 3.4s\nFAIL", 1)
	if a != b {
		t.Error("timings must not change the failure signature")
	}
	if a == rcUntilSig("--- FAIL: TestY (0.01s)\nFAIL", 1) {
		t.Error("a different failing test is a different signature")
	}
	if n := rcCountFails("--- FAIL: A\n--- FAIL: B\nFAIL\tpkg\nok"); n != 3 {
		t.Errorf("rcCountFails = %d, want 3", n)
	}
	if rcExitCode("out\n[exit code 2] [cwd: /x]") != 2 || rcExitCode("no marker") != -1 {
		t.Error("rcExitCode")
	}
	if !rcIsVerifyCmd("cd x && go test ./...") || rcIsVerifyCmd("ls -la") {
		t.Error("rcIsVerifyCmd")
	}
	e := rcEvent{tool: "bash", target: strings.Repeat("x", 400), exit: 1, isErr: true, errTxt: "boom"}
	if n := len([]rune(e.summary())); n > rcSumRunes {
		t.Errorf("summary has %d runes, max %d", n, rcSumRunes)
	}
}

// TestRecoverReasonInJSONResult: -p --output-format json reports the handoff
// reason, so evals can measure pass rate after a handoff; absent otherwise.
func TestRecoverReasonInJSONResult(t *testing.T) {
	for _, tc := range []string{rcWhyUntil, ""} {
		var buf bytes.Buffer
		j := NewJSONUI(&buf, false)
		j.Begin(RunInfo{})
		j.Finish(RunResult{Status: StopDone, Recover: tc})
		var m map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
			t.Fatalf("bad json %q: %v", buf.String(), err)
		}
		got, ok := m["recover"].(string)
		if got != tc || ok != (tc != "") {
			t.Errorf("recover = %v (present %v), want %q", m["recover"], ok, tc)
		}
	}
}

// TestPlanCacheInjectionKeepsRecapCheck: the plan-cache reminder goes into
// the user message only, so a recap question ("…?") after earlier tool work
// is still recognised and its past-tense answer is not nudged as a fake action.
func TestPlanCacheInjectionKeepsRecapCheck(t *testing.T) {
	var start int
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n == 1 {
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo hi"}`}}}
		}
		if n == 2 {
			return fakeResp{Text: "ran it"}
		}
		return fakeResp{Text: "I fixed the config parser crash earlier."}
	}, nil)
	if _, err := h.ag.Run(context.Background(), "say hi"); err != nil {
		t.Fatal(err)
	}
	if err := pcStore(h.root, pcPlan{ID: "p1", Task: "fix the config parser crash", Keys: pcUniq(pcTerms("fix the config parser crash")), NSteps: 14, Wins: 1}, nil); err != nil {
		t.Fatal(err)
	}
	start = len(h.fs.requests())
	if _, err := h.ag.Run(context.Background(), "what did you change in the config parser crash fix?"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()[start:]
	u := userContents(reqs[0])
	if msg := u[len(u)-1]; !strings.HasPrefix(strings.TrimSpace(acStripReminders(msg)), "what did you change") || !strings.Contains(msg, "Plan cache") {
		t.Fatalf("plan should follow the request in the same message:\n%s", msg)
	}
	if len(reqs) != 1 {
		t.Errorf("a recap answer must not get the fake-action nudge: %d requests", len(reqs))
	}
}
