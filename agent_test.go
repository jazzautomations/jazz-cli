//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests: basic loop
// ---------------------------------------------------------------------------

func TestAgentReadToolThenFinalAnswer(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "Reading.", Calls: []fakeCall{{ID: "c1", Name: "read", Args: jsonArgs(map[string]any{"path": "hello.txt"})}}},
		fakeResp{Text: "The file says hello world."},
	), nil)
	if err := os.WriteFile(filepath.Join(h.root, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "what is in hello.txt?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	if reqs[0].Model != "m1" {
		t.Errorf("model sent = %q, want m1 (provider prefix stripped)", reqs[0].Model)
	}
	if !reqs[0].HasTools {
		t.Errorf("first request must offer tools")
	}
	names := strings.Join(reqs[0].toolNames(), ",")
	for _, n := range []string{"read", "write", "edit", "bash", "todo"} {
		if !strings.Contains(names, n) {
			t.Errorf("tool %q missing from request tools %s", n, names)
		}
	}
	if reqs[0].system() == "" || reqs[0].system() != reqs[1].system() {
		t.Errorf("system prompt must be present and byte-identical across requests")
	}
	if reqs[0].system() != h.ag.System {
		t.Errorf("system prompt sent differs from agent.System")
	}
	res := reqs[1].toolResults()
	if !strings.Contains(res["c1"], "hello world") {
		t.Errorf("tool result for c1 should contain file content, got %q", res["c1"])
	}
	if !reqs[1].contains("user", "what is in hello.txt?") {
		t.Errorf("user message missing from second request")
	}
	// history: user, assistant(call), tool, assistant(final)
	if got := len(h.ag.Msgs); got != 4 {
		t.Fatalf("history len = %d, want 4: %+v", got, h.ag.Msgs)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Role != "assistant" || lm.Content != "The file says hello world." {
		t.Errorf("last message = %+v", lm)
	}
	if h.ag.Msgs[1].Content != "Reading." || len(h.ag.Msgs[1].ToolCalls) != 1 || h.ag.Msgs[1].ToolCalls[0].Args != `{"path":"hello.txt"}` {
		t.Errorf("assistant tool-call message not reassembled from stream: %+v", h.ag.Msgs[1])
	}
	if !strings.Contains(h.ui.text.String(), "hello world") {
		t.Errorf("final text not streamed to UI: %q", h.ui.text.String())
	}
	if len(h.ui.turns) != 1 || h.ui.turns[0].Tools != 1 || h.ui.turns[0].Model != "fake/m1" {
		t.Errorf("TurnEnd stats = %+v", h.ui.turns)
	}
}

func TestAgentBashExecutesInProject(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo built > out.txt && cat out.txt && pwd"})}}},
		fakeResp{Text: "done"},
	), nil)
	if err := h.run(t, "make out.txt"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(h.root, "out.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "built" {
		t.Fatalf("bash did not run in project root: %q %v", b, err)
	}
	res := h.fs.requests()[1].toolResults()["b1"]
	if !strings.Contains(res, "built") || !strings.Contains(res, "[exit code 0]") {
		t.Errorf("bash result = %q", res)
	}
	real, _ := filepath.EvalSymlinks(h.root)
	if !strings.Contains(res, h.root) && !strings.Contains(res, real) {
		t.Errorf("pwd output should be the project root %s, got %q", h.root, res)
	}
}

func TestAgentBashNonZeroExitReported(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: `{"command":"echo oops; exit_status_fn() { return 3; }; exit_status_fn"}`}}},
		fakeResp{Text: "done"},
	), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()["b1"]
	if !strings.Contains(res, "[exit code 3]") || !strings.Contains(res, "oops") {
		t.Errorf("bash result = %q", res)
	}
}

func TestAgentUnknownToolReturnsErrorAndContinues(t *testing.T) {
	cases := []struct {
		name, tool, want string
	}{
		{"plain unknown", "frobnicate", "not found"},
		{"mcp not connected", "mcp__srv__thing", "not available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, seq(
				fakeResp{Calls: []fakeCall{{ID: "u1", Name: tc.tool, Args: `{}`}}},
				fakeResp{Text: "ok, giving up on that"},
			), nil)
			if err := h.run(t, "go"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			reqs := h.fs.requests()
			if len(reqs) != 2 {
				t.Fatalf("want 2 requests, got %d", len(reqs))
			}
			res := reqs[1].toolResults()["u1"]
			if !strings.Contains(res, tc.want) {
				t.Errorf("result = %q, want substring %q", res, tc.want)
			}
			if tc.tool == "frobnicate" && !strings.Contains(res, "read, write, edit, bash, todo") {
				t.Errorf("unknown tool error should list valid tools: %q", res)
			}
		})
	}
}

func TestAgentToolAliasCanonicalized(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "a1", Name: "functions.read_file", Args: `{"file_path":"x.txt"}`}}},
		fakeResp{Text: "ok"},
	), nil)
	os.WriteFile(filepath.Join(h.root, "x.txt"), []byte("ALIASED\n"), 0o644)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if n := h.ag.Msgs[1].ToolCalls[0].Name; n != "read" {
		t.Errorf("tool name not canonicalized in history: %q", n)
	}
	if res := h.fs.requests()[1].toolResults()["a1"]; !strings.Contains(res, "ALIASED") {
		t.Errorf("aliased read did not run: %q", res)
	}
}

func TestAgentTextEmbeddedToolCall(t *testing.T) {
	cases := []struct {
		name, text, wantRest string
	}{
		{"tool_call tag", `Let me check.<tool_call>{"name":"bash","arguments":{"command":"echo EMBEDDED_OK"}}</tool_call>`, "Let me check."},
		{"tool_call stringified args", `<tool_call>{"name":"bash","arguments":"{\"command\":\"echo EMBEDDED_OK\"}"}</tool_call>`, ""},
		{"function tag", `Running <function=bash>{"command":"echo EMBEDDED_OK"}</function>`, "Running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, seq(fakeResp{Text: tc.text}, fakeResp{Text: "finished"}), nil)
			if err := h.run(t, "go"); err != nil {
				t.Fatal(err)
			}
			reqs := h.fs.requests()
			if len(reqs) != 2 {
				t.Fatalf("want 2 requests, got %d", len(reqs))
			}
			a := h.ag.Msgs[1]
			if a.Role != "assistant" || len(a.ToolCalls) != 1 || a.ToolCalls[0].Name != "bash" {
				t.Fatalf("embedded call not recovered: %+v", a)
			}
			if a.Content != tc.wantRest {
				t.Errorf("remaining text = %q, want %q", a.Content, tc.wantRest)
			}
			found := false
			for _, c := range reqs[1].toolResults() {
				found = found || strings.Contains(c, "EMBEDDED_OK")
			}
			if !found {
				t.Errorf("embedded tool call output missing from next request: %v", reqs[1].toolResults())
			}
		})
	}
}

func TestAgentTextToolCallInvalidJSONLeftAsText(t *testing.T) {
	txt := `<tool_call>{"name": broken}</tool_call>`
	h := newHarness(t, seq(fakeResp{Text: txt}), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if len(h.fs.requests()) != 1 {
		t.Fatalf("invalid embedded call must not loop; requests=%d", len(h.fs.requests()))
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != txt || len(lm.ToolCalls) != 0 {
		t.Errorf("invalid embedded call should stay as text: %+v", lm)
	}
}

// ---------------------------------------------------------------------------
// Retries and fallback
// ---------------------------------------------------------------------------

func TestAgentRetries429ThenSucceeds(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Status: 429, Body: `{"error":{"message":"rate limited"}}`},
		fakeResp{Text: "hi there"},
	), nil)
	t0 := time.Now()
	if err := h.run(t, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 || reqs[0].Model != "m1" || reqs[1].Model != "m1" {
		t.Fatalf("want 2 requests to m1, got %d", len(reqs))
	}
	if time.Since(t0) < 1500*time.Millisecond {
		t.Errorf("retry should back off ~2s")
	}
	if !anyContains(h.ui.notices, "tentando de novo") {
		t.Errorf("retry notice missing: %v", h.ui.notices)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "hi there" {
		t.Errorf("final = %+v", lm)
	}
	if len(h.ag.Msgs) != 2 {
		t.Errorf("failed attempt must not leave messages in history: %+v", h.ag.Msgs)
	}
}

func TestAgentPersistent429FallsBackAndSticks(t *testing.T) {
	m2 := 0
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{Status: 429, Body: `{"error":{"message":"rate limited"}}`}
		}
		m2++
		if m2 == 1 {
			return fakeResp{Calls: []fakeCall{{ID: "f1", Name: "bash", Args: `{"command":"echo fb"}`}}}
		}
		return fakeResp{Text: "done on m2"}
	}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// G4: waits of 2, 6 and 15 s (RetryDelay), so 4 tries before falling back
	if got := h.fs.countModel("m1"); got != 4 {
		t.Errorf("m1 should be tried 4 times then abandoned for the turn; got %d", got)
	}
	if got := h.fs.countModel("m2"); got != 2 {
		t.Errorf("m2 should serve both steps (sticky); got %d", got)
	}
	reqs := h.fs.requests()
	if reqs[len(reqs)-1].Model != "m2" {
		t.Errorf("second step must go straight to m2")
	}
	if !anyContains(h.ui.notices, "usando fake/m2") {
		t.Errorf("fallback notice missing: %v", h.ui.notices)
	}
	if len(h.ui.turns) != 1 || h.ui.turns[0].Model != "fake/m2" {
		t.Errorf("TurnStats.Model = %+v, want fake/m2", h.ui.turns)
	}
	if h.ag.Model != "fake/m1" {
		t.Errorf("agent's configured model must not change: %s", h.ag.Model)
	}
}

func TestAgentAuthAndClientErrorsMoveToNextCandidate(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantWarn string
	}{
		{401, `{"error":{"message":"bad key"}}`, "recusou a credencial (401)"},
		{403, `{"error":{"message":"forbidden"}}`, "recusou a credencial (403)"},
		{404, `{"error":{"message":"no such model"}}`, "fake/m1"},
		{400, `{"error":{"message":"invalid parameter foo"}}`, "fake/m1"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			h := newHarness(t, func(n int, r recReq) fakeResp {
				if r.Model == "m1" {
					return fakeResp{Status: tc.status, Body: tc.body}
				}
				return fakeResp{Text: "from m2"}
			}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
			t0 := time.Now()
			if err := h.run(t, "go"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := h.fs.countModel("m1"); got != 1 {
				t.Errorf("m1 must not be retried on %d; got %d tries", tc.status, got)
			}
			if time.Since(t0) > 1500*time.Millisecond {
				t.Errorf("no backoff expected on %d", tc.status)
			}
			if lm := lastMsg(h.ag.Msgs); lm.Content != "from m2" {
				t.Errorf("final = %+v", lm)
			}
			if !anyContains(h.ui.warns, tc.wantWarn) {
				t.Errorf("warns %v missing %q", h.ui.warns, tc.wantWarn)
			}
		})
	}
}

func TestAgentAllCandidatesFailReturnsError(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 401, Body: `{"error":"nope"}`}
	}, func(c *Config) { c.Fallback = []string{"fake/m2", "missing/x"} })
	err := h.run(t, "go")
	var ae *APIError
	if err == nil || (!errors.As(err, &ae) && !strings.Contains(err.Error(), "missing")) {
		t.Fatalf("expected an error after all candidates fail, got %v", err)
	}
	if h.fs.countModel("m1") != 1 || h.fs.countModel("m2") != 1 {
		t.Errorf("each candidate tried once: m1=%d m2=%d", h.fs.countModel("m1"), h.fs.countModel("m2"))
	}
	// history must stay valid: only the user message
	if len(h.ag.Msgs) != 1 || h.ag.Msgs[0].Role != "user" {
		t.Errorf("history after failure = %+v", h.ag.Msgs)
	}
}

func TestAgentNonRetryableErrorStopsImmediately(t *testing.T) {
	// 418 is not retryable and not a "next candidate" status.
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 418, Body: `teapot`}
	}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
	err := h.run(t, "go")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 418 {
		t.Fatalf("want APIError 418, got %v", err)
	}
	if len(h.fs.requests()) != 1 {
		t.Errorf("non-retryable error must not retry or fall back; requests=%d", len(h.fs.requests()))
	}
}

// ---------------------------------------------------------------------------
// Loop detection and nudges
// ---------------------------------------------------------------------------

func TestAgentLoopDetectionWarnsThenStops(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo same"}`}}}
	}, func(c *Config) { c.Recover = "off" }) // the handoff path is in agent_recover_test.go
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 6 {
		t.Fatalf("turn should stop after the 6th identical call; requests=%d", len(reqs))
	}
	// request k (0-based) carries results of calls 1..k
	for k := 1; k < len(reqs); k++ {
		res := reqs[k].toolResults()[fmt.Sprintf("call_%d_0", k-1)]
		warned := strings.Contains(res, "exact call")
		if want := k >= 3; warned != want {
			t.Errorf("call #%d: warned=%v want %v (%q)", k, warned, want, res)
		}
		if k >= 3 && !strings.Contains(res, fmt.Sprintf("%d times", k)) {
			t.Errorf("call #%d: warning should count repetitions: %q", k, res)
		}
	}
	lm := lastMsg(h.ag.Msgs)
	if lm.Role != "user" || !strings.Contains(lm.Content, "stuck repeating") {
		t.Errorf("last message should be loop reminder: %+v", lm)
	}
	if !anyContains(h.ui.warns, "loop") {
		t.Errorf("loop warning missing: %v", h.ui.warns)
	}
}

func TestAgentLoopDetectionNotTriggeredByDifferentResults(t *testing.T) {
	// same args but different results each time (counter file) => no warning
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 5 {
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo x >> c.txt; wc -l < c.txt"}`}}}
		}
		return fakeResp{Text: "done"}
	}, nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.fs.requests() {
		for _, c := range r.toolResults() {
			if strings.Contains(c, "exact call") {
				t.Fatalf("false-positive loop warning: %q", c)
			}
		}
	}
	if len(h.fs.requests()) != 6 {
		t.Errorf("requests=%d", len(h.fs.requests()))
	}
}

func TestAgentEmptyReplyNudgedOnce(t *testing.T) {
	h := newHarness(t, seq(fakeResp{}, fakeResp{}, fakeResp{Text: "never reached"}), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("empty reply nudged exactly once => 2 requests, got %d", len(reqs))
	}
	if !reqs[1].contains("user", "Your last reply was empty") {
		t.Errorf("nudge missing from 2nd request")
	}
	n := 0
	for _, m := range h.ag.Msgs {
		if strings.Contains(m.Content, "Your last reply was empty") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("nudge count = %d", n)
	}
}

func TestAgentVerifyNudge(t *testing.T) {
	writeGo := fakeCall{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "main.go", "content": "package main\n"})}
	writeTxt := fakeCall{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "notes.txt", "content": "hi\n"})}
	runBash := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"echo built"}`}
	failGo := fakeCall{ID: "w1", Name: "write", Args: `{"path":"main.go"}`} // missing content => error
	cases := []struct {
		name      string
		script    []fakeResp
		noNudge   bool
		wantNudge bool
		wantReqs  int
	}{
		{"edit go then stop", []fakeResp{{Calls: []fakeCall{writeGo}}, {Text: "done"}, {Text: "verified"}}, false, true, 3},
		{"edit go then bash", []fakeResp{{Calls: []fakeCall{writeGo}}, {Calls: []fakeCall{runBash}}, {Text: "done"}}, false, false, 3},
		{"edit go and bash same step", []fakeResp{{Calls: []fakeCall{writeGo, runBash}}, {Text: "done"}}, false, false, 2},
		{"edit txt", []fakeResp{{Calls: []fakeCall{writeTxt}}, {Text: "done"}}, false, false, 2},
		{"failed write", []fakeResp{{Calls: []fakeCall{failGo}}, {Text: "done"}}, false, false, 2},
		{"disabled", []fakeResp{{Calls: []fakeCall{writeGo}}, {Text: "done"}}, true, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, seq(tc.script...), func(c *Config) { c.NoVerifyNudge = tc.noNudge })
			if err := h.run(t, "go"); err != nil {
				t.Fatal(err)
			}
			reqs := h.fs.requests()
			if len(reqs) != tc.wantReqs {
				t.Fatalf("requests=%d want %d", len(reqs), tc.wantReqs)
			}
			got := reqs[len(reqs)-1].contains("user", "You changed code but did not run anything")
			if got != tc.wantNudge {
				t.Errorf("verify nudge = %v, want %v", got, tc.wantNudge)
			}
		})
	}
}

func TestAgentOpenTodoNudge(t *testing.T) {
	todo := fakeCall{ID: "t1", Name: "todo", Args: jsonArgs(map[string]any{"todos": []any{
		map[string]any{"content": "write parser", "status": "completed"},
		map[string]any{"content": "add tests", "status": "in_progress"},
	}})}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{todo}},
		fakeResp{Text: "all done"},
		fakeResp{Text: "really done"},
		fakeResp{Text: "never"},
	), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("todo nudge must fire exactly once; requests=%d", len(reqs))
	}
	l := reqs[2].last()
	c, _ := l["content"].(string)
	if l["role"] != "user" || !strings.Contains(c, "unfinished items") || !strings.Contains(c, "add tests") {
		t.Errorf("todo nudge = %q", c)
	}
	if strings.Contains(c, "write parser") {
		t.Errorf("completed items must not be listed: %q", c)
	}
}

func TestAgentNoTodoNudgeWhenAllCompleted(t *testing.T) {
	todo := fakeCall{ID: "t1", Name: "todo", Args: `{"todos":[{"content":"a","status":"done"}]}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{todo}}, fakeResp{Text: "done"}), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Errorf("requests=%d want 2", n)
	}
}

func TestAgentMaxStepsForcesFinalNoToolsRequest(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.contains("user", "Step limit reached") {
			return fakeResp{Text: "summary of work"}
		}
		return fakeResp{Calls: []fakeCall{{Name: "bash", Args: fmt.Sprintf(`{"command":"echo step%d"}`, n)}}}
	}, func(c *Config) { c.MaxSteps = 2 })
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopStepLimit {
		t.Errorf("stop = %s, want step_limit", stop)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests=%d want 3", len(reqs))
	}
	// bug 37: tools stay in the request (Anthropic rejects tool_use history
	// without tools); the provider sends tool_choice none instead.
	if !reqs[0].HasTools || !reqs[1].HasTools || !reqs[2].HasTools {
		t.Errorf("tools offered = %v %v %v, want all true", reqs[0].HasTools, reqs[1].HasTools, reqs[2].HasTools)
	}
	if tc, ok := reqs[2].Raw["tool_choice"]; ok && tc != "none" {
		t.Errorf("final request tool_choice = %v, want none", tc)
	}
	l := reqs[2].last()
	if c, _ := l["content"].(string); l["role"] != "user" || !strings.Contains(c, "Step limit reached") {
		t.Errorf("last message of final request = %v", l)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "summary of work" {
		t.Errorf("final = %+v", lm)
	}
}

func TestAgentMaxStepsToolCallsAfterLimitNotExecuted(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 8 { // model ignores the missing tools and keeps calling
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: fmt.Sprintf(`{"command":"echo x%d >> ran.txt"}`, n)}}}
		}
		return fakeResp{Text: "final"}
	}, func(c *Config) { c.MaxSteps = 1 })
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.fs.requests()); n > 2 {
		t.Errorf("MaxSteps=1 should end the turn after the forced final request; got %d requests", n)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, "ran.txt"))
	if lines := strings.Count(string(b), "\n"); lines > 1 {
		t.Errorf("%d tool calls ran; calls after the step limit must not execute", lines)
	}
}

// ---------------------------------------------------------------------------
// Cancellation and steering
// ---------------------------------------------------------------------------

func TestAgentCancelMidToolKeepsHistoryPaired(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "s1", Name: "bash", Args: `{"command":"sleep 20"}`},
			{ID: "s2", Name: "read", Args: `{"path":"a.txt"}`},
			{ID: "s3", Name: "bash", Args: `{"command":"echo never > never.txt"}`},
		}},
		fakeResp{Text: "resumed fine"},
	), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ui.onToolStart = func(name, detail string) {
		if strings.Contains(detail, "sleep") {
			go func() { time.Sleep(300 * time.Millisecond); cancel() }()
		}
	}
	t0 := time.Now()
	_, err := h.ag.Run(ctx, "go")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if time.Since(t0) > 10*time.Second {
		t.Fatalf("cancel did not interrupt the running tool")
	}
	byID := map[string]string{}
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" {
			byID[m.ToolCallID] = m.Content
		}
	}
	if !strings.Contains(byID["s1"], "TOOL_OUTCOME_UNKNOWN") {
		t.Errorf("running tool should be marked outcome-unknown: %q", byID["s1"])
	}
	for _, id := range []string{"s2", "s3"} {
		if !strings.Contains(byID[id], "TOOL_NOT_STARTED") {
			t.Errorf("%s should be TOOL_NOT_STARTED: %q", id, byID[id])
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "never.txt")); err == nil {
		t.Errorf("tool after cancellation must not run")
	}
	if lm := lastMsg(h.ag.Msgs); lm.Role != "user" || !strings.Contains(lm.Content, "interrupted") {
		t.Errorf("last message = %+v", lm)
	}
	assertPairedMsgs(t, h.ag.Msgs)

	// The next turn must produce a valid request (global invariant checked in cleanup).
	if err := h.run(t, "continue"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "resumed fine" {
		t.Errorf("second turn final = %+v", lm)
	}
}

func TestAgentCancelDuringStreamKeepsPartialText(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "partial answer", HangAfter: true}, fakeResp{Text: "ok"}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ui.onText = func(s string) { cancel() }
	done := make(chan error, 1)
	go func() { _, err := h.ag.Run(ctx, "go"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel during stream")
	}
	h.ui.onText = nil
	var asst *Message
	for i := range h.ag.Msgs {
		if h.ag.Msgs[i].Role == "assistant" {
			asst = &h.ag.Msgs[i]
		}
	}
	if asst == nil || !strings.Contains(asst.Content, "[interrupted by the user]") || !strings.HasPrefix(asst.Content, "partial") {
		t.Errorf("partial text not kept: %+v", h.ag.Msgs)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Role != "user" || !strings.Contains(lm.Content, "interrupted the previous response") {
		t.Errorf("last = %+v", lm)
	}
	if len(h.fs.requests()) != 1 {
		t.Errorf("cancellation must not retry; requests=%d", len(h.fs.requests()))
	}
}

func TestAgentSteeringInjectedAfterToolResults(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "k1", Name: "bash", Args: `{"command":"echo working"}`}}},
		fakeResp{Text: "done"},
	), nil)
	h.ui.onToolStart = func(name, detail string) { h.ag.Steer("also update the README") }
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	r := h.fs.requests()[1]
	// order must be: ... assistant(k1), tool(k1), user(steer)
	n := len(r.Messages)
	if n < 3 {
		t.Fatalf("too few messages: %d", n)
	}
	last := r.Messages[n-1]
	c, _ := last["content"].(string)
	if last["role"] != "user" || !strings.Contains(c, "also update the README") || !strings.Contains(c, "while you were working") {
		t.Errorf("steering message = %v", last)
	}
	if r.Messages[n-2]["role"] != "tool" || r.Messages[n-2]["tool_call_id"] != "k1" {
		t.Errorf("steering must come after the tool result: %v", r.Messages[n-2])
	}
}

func TestAgentSteeringAfterFinalAnswerContinuesTurn(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "first answer"}, fakeResp{Text: "handled steer"}), nil)
	once := sync.Once{}
	h.ui.onText = func(string) { once.Do(func() { h.ag.Steer("one more thing") }) }
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("queued steer must continue the turn; requests=%d", len(reqs))
	}
	if l := reqs[1].last(); l["role"] != "user" || l["content"] != "one more thing" {
		t.Errorf("steer message = %v", l)
	}
}

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

func TestAgentDangerousBashApproval(t *testing.T) {
	// A real `git push --force` is flagged by DangerReason, and in the harness's
	// temp dir (not a git repo, no remote) it fails harmlessly with a non-zero exit.
	danger := `{"command":"git push --force"}`
	cases := []struct {
		name        string
		mode        string
		answer      Approval
		wantAsked   bool
		wantRan     bool
		wantInReply string
	}{
		{"auto deny", "auto", Deny, true, false, "denied"},
		{"auto allow", "auto", Allow, true, true, "[exit code"},
		{"ask deny", "ask", Deny, true, false, "git push forçado"},
		{"yolo", "yolo", Deny, false, true, "[exit code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, seq(
				fakeResp{Calls: []fakeCall{{ID: "d1", Name: "bash", Args: danger}}},
				fakeResp{Text: "ok"},
			), func(c *Config) { c.Mode = tc.mode })
			h.ui.answer = tc.answer
			if err := h.run(t, "go"); err != nil {
				t.Fatal(err)
			}
			if asked := len(h.ui.approvals) > 0; asked != tc.wantAsked {
				t.Fatalf("approval asked=%v want %v", asked, tc.wantAsked)
			}
			if tc.wantAsked {
				a := h.ui.approvals[0]
				if a.Tool != "bash" || a.Reason != "git push forçado" || !strings.Contains(a.Detail, "git push --force") {
					t.Errorf("approval = %+v", a)
				}
			}
			res := h.fs.requests()[1].toolResults()["d1"]
			if !strings.Contains(res, tc.wantInReply) {
				t.Errorf("result %q missing %q", res, tc.wantInReply)
			}
			ran := strings.Contains(res, "[exit code")
			if ran != tc.wantRan {
				t.Errorf("ran=%v want %v (%q)", ran, tc.wantRan, res)
			}
			if !tc.wantRan && h.ui.turns[0].Tools != 0 {
				t.Errorf("denied tool must not count as run: %+v", h.ui.turns[0])
			}
		})
	}
}

func TestAgentAllowAlwaysCachesExceptDangerous(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "w1", Name: "write", Args: `{"path":"a.txt","content":"a"}`},
			{ID: "w2", Name: "write", Args: `{"path":"b.txt","content":"b"}`},
			{ID: "d1", Name: "bash", Args: `{"command":"git push --force"}`},
			{ID: "d2", Name: "bash", Args: `{"command":"git push -f"}`},
			{ID: "r1", Name: "bash", Args: `{"command":"ls"}`},
		}},
		fakeResp{Text: "ok"},
	), func(c *Config) { c.Mode = "ask" })
	// G11: 'always' derives a pattern rule (Edit(<dir>/**)); use a bare rule set
	// so the developer's global settings files do not leak into the test.
	h.ag.Perms = &PermSet{cfg: h.cfg, root: h.root}
	h.ui.answer = AllowAlways
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, a := range h.ui.approvals {
		tools = append(tools, a.Tool+":"+a.Reason)
	}
	// write asked once (then cached), both dangerous bash asked, read-only ls never asked
	want := []string{"write:", "bash:git push forçado", "bash:git push forçado"}
	if strings.Join(tools, "|") != strings.Join(want, "|") {
		t.Errorf("approvals = %v, want %v", tools, want)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	if b, err := os.ReadFile(filepath.Join(h.root, ".jazz", "settings.json")); err != nil || !strings.Contains(string(b), "Edit(") {
		t.Errorf("'always' must persist an Edit(...) rule: %q %v", b, err)
	}
}

func TestAgentAutoModeDoesNotAskForSafeCommands(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "w1", Name: "write", Args: `{"path":"a.txt","content":"a"}`},
			{ID: "b1", Name: "bash", Args: `{"command":"touch z.txt"}`},
		}},
		fakeResp{Text: "ok"},
	), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 0 {
		t.Errorf("auto mode should not ask for writes/safe bash: %+v", h.ui.approvals)
	}
}

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------

func summaryServer(next func(n int, r recReq) fakeResp) func(int, recReq) fakeResp {
	return func(n int, r recReq) fakeResp {
		if r.isCompact() {
			return fakeResp{Text: "## Goal\nSUMMARY-TEXT"}
		}
		return next(n, r)
	}
}

func big(n int) string { return strings.Repeat("x", n) }

func TestAgentCompactSkipsOrphanToolAndMergesUser(t *testing.T) {
	h := newHarness(t, summaryServer(func(int, recReq) fakeResp { return fakeResp{Text: "x"} }),
		func(c *Config) { c.ContextWindow = 12000 }) // keep ≈ 2000 tokens ≈ 7000 chars
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "first task"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "read", Args: `{"path":"a"}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "read", Content: big(4000)},
		{Role: "assistant", Content: big(8000), ToolCalls: []ToolCall{{ID: "c2", Name: "bash", Args: `{"command":"ls"}`}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "bash", Content: "small result"},
		{Role: "user", Content: "latest question"},
	}
	before := append([]Message(nil), h.ag.Msgs...)
	if err := h.ag.Compact(context.Background(), "the parser"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(h.ag.Msgs) != 1 {
		t.Fatalf("want summary merged with trailing user msg, got %+v", h.ag.Msgs)
	}
	m := h.ag.Msgs[0]
	if m.Role != "user" || !strings.HasPrefix(m.Content, "<summary>") || !strings.Contains(m.Content, "SUMMARY-TEXT") || !strings.Contains(m.Content, "latest question") {
		t.Errorf("compacted head = %+v", m)
	}
	reqs := h.fs.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	r := reqs[0]
	if r.system() != h.ag.System {
		t.Errorf("summary request must reuse the exact system prompt")
	}
	if !r.HasTools {
		t.Errorf("summary request should keep tools (cache prefix)")
	}
	if c, _ := r.last()["content"].(string); !strings.Contains(c, "focus the summary on: the parser") {
		t.Errorf("focus not passed to summary prompt")
	}
	// prefix must be the old history up to (and including) the c2 results, byte-identical
	if got := len(r.Messages) - 2; got != len(before)-1 { // minus system & compact prompt; cut excludes "latest question"
		t.Errorf("summary prefix has %d history msgs, want %d", got, len(before)-1)
	}
	if c, _ := r.Messages[1]["content"].(string); c != "first task" {
		t.Errorf("prefix must start with original first message")
	}
}

func TestAgentCompactKeepsAssistantWithItsToolResults(t *testing.T) {
	h := newHarness(t, summaryServer(func(int, recReq) fakeResp { return fakeResp{Text: "x"} }),
		func(c *Config) { c.ContextWindow = 12000 })
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: big(9000)},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c3", Name: "bash", Args: `{"command":"ls"}`}}},
		{Role: "tool", ToolCallID: "c3", ToolName: "bash", Content: "r"},
		{Role: "assistant", Content: "final"},
	}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, m := range h.ag.Msgs {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" {
		t.Errorf("roles = %v", roles)
	}
	if h.ag.Msgs[1].ToolCalls[0].ID != "c3" || h.ag.Msgs[2].ToolCallID != "c3" {
		t.Errorf("kept tool pair broken: %+v", h.ag.Msgs)
	}
	if h.ag.lastPrompt != 0 || h.ag.countedIdx != 0 {
		t.Errorf("token accounting must reset after compaction")
	}
}

func TestAgentCompactNeverStartsWithToolTable(t *testing.T) {
	// Sweep window sizes so the cut lands on many different positions.
	for _, w := range []int{3000, 6000, 9000, 12000, 20000, 40000, 63000} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			h := newHarness(t, summaryServer(func(int, recReq) fakeResp { return fakeResp{Text: "x"} }),
				func(c *Config) { c.ContextWindow = w })
			h.ag.EnsureSystem()
			var msgs []Message
			msgs = append(msgs, Message{Role: "user", Content: "start"})
			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("c%d", i)
				msgs = append(msgs,
					Message{Role: "assistant", Content: big(300 * (i%4 + 1)), ToolCalls: []ToolCall{{ID: id, Name: "bash", Args: `{"command":"ls"}`}, {ID: id + "b", Name: "read", Args: `{"path":"x"}`}}},
					Message{Role: "tool", ToolCallID: id, ToolName: "bash", Content: big(1500 * (i%3 + 1))},
					Message{Role: "tool", ToolCallID: id + "b", ToolName: "read", Content: big(700)},
				)
			}
			h.ag.Msgs = msgs
			if err := h.ag.Compact(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			if len(h.ag.Msgs) == 0 || h.ag.Msgs[0].Role != "user" || !strings.HasPrefix(h.ag.Msgs[0].Content, "<summary>") {
				t.Fatalf("head = %+v", h.ag.Msgs)
			}
			if len(h.ag.Msgs) > 1 && h.ag.Msgs[1].Role == "tool" {
				t.Errorf("orphan tool right after summary")
			}
			assertPairedMsgs(t, h.ag.Msgs)
		})
	}
}

func TestAgentCompactEmptySummaryRetries(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: ""}, fakeResp{Text: "the summary"}), nil)
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"}, {Role: "assistant", Content: "d"},
	}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	// Both keep the tool specs (bug 37: Anthropic needs them when the history
	// has tool calls); NoToolUse asks the model not to call them.
	if len(reqs) != 2 || !reqs[0].HasTools || !reqs[1].HasTools {
		t.Fatalf("want 2 summary requests, both with tools; got %d", len(reqs))
	}
	if !strings.Contains(h.ag.Msgs[0].Content, "the summary") {
		t.Errorf("summary not used: %+v", h.ag.Msgs[0])
	}
}

func TestAgentCompactFailuresLeaveHistoryUntouched(t *testing.T) {
	cases := []struct {
		name string
		msgs []Message
		h    func(int, recReq) fakeResp
	}{
		{"too short", []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}, seq()},
		{"empty summary twice", []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}, {Role: "assistant", Content: "d"}},
			func(int, recReq) fakeResp { return fakeResp{} }},
		{"http error", []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}, {Role: "assistant", Content: "d"}},
			func(int, recReq) fakeResp { return fakeResp{Status: 500, Body: "boom"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.h, nil)
			h.ag.EnsureSystem()
			h.ag.Msgs = append([]Message(nil), tc.msgs...)
			if err := h.ag.Compact(context.Background(), ""); err == nil {
				t.Fatalf("expected error")
			}
			if len(h.ag.Msgs) != len(tc.msgs) {
				t.Errorf("history changed on failure: %+v", h.ag.Msgs)
			}
		})
	}
}

func TestAgentOverflowCompactsAndRetries(t *testing.T) {
	overflowed := false
	h := newHarness(t, summaryServer(func(n int, r recReq) fakeResp {
		if !overflowed {
			overflowed = true
			return fakeResp{Status: 400, Body: `{"error":{"message":"This model's maximum context length is 8192 tokens","code":"context_length_exceeded"}}`}
		}
		return fakeResp{Text: "answer after compaction"}
	}), func(c *Config) { c.Fallback = []string{"fake/m2"} })
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "old q"}, {Role: "assistant", Content: big(100000)},
		{Role: "user", Content: "old q2"}, {Role: "assistant", Content: big(100000)},
	}
	if err := h.run(t, "new question"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("want overflow, summary, retry = 3 requests; got %d", len(reqs))
	}
	if !reqs[1].isCompact() {
		t.Errorf("2nd request should be the summary request")
	}
	if reqs[2].Model != "m1" {
		t.Errorf("overflow must not fall back to another model; got %s", reqs[2].Model)
	}
	first, _ := reqs[2].Messages[1]["content"].(string)
	if !strings.HasPrefix(first, "<summary>") {
		t.Errorf("retry should start with the summary: %q", oneLine(first, 80))
	}
	if !reqs[2].contains("user", "new question") {
		t.Errorf("retry must still contain the new user message")
	}
	if !anyContains(h.ui.notices, "contexto estourou") {
		t.Errorf("overflow notice missing: %v", h.ui.notices)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "answer after compaction" {
		t.Errorf("final = %+v", lm)
	}
}

func TestAgentIsOverflow(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 400, Body: `{"error":{"code":"context_length_exceeded"}}`}, true},
		{&APIError{Status: 400, Body: "prompt is too long: 210000 tokens > 200000 maximum"}, true},
		{&APIError{Status: 413, Body: "Request too large: too many tokens"}, true},
		{&APIError{Status: 422, Body: "input exceeds the context window"}, true},
		{&APIError{Status: 400, Body: "invalid tool schema"}, false},
		{&APIError{Status: 429, Body: "context_length_exceeded"}, false},
		{&APIError{Status: 500, Body: "context too long"}, false},
		{errors.New("context_length_exceeded"), false},
		{fmt.Errorf("wrapped: %w", &APIError{Status: 400, Body: "maximum context length exceeded"}), true},
	}
	for i, tc := range cases {
		if got := isOverflow(tc.err); got != tc.want {
			t.Errorf("case %d (%v): isOverflow=%v want %v", i, tc.err, got, tc.want)
		}
	}
}

func TestAgentProactiveCompactionWhenOverThreshold(t *testing.T) {
	h := newHarness(t, summaryServer(func(n int, r recReq) fakeResp {
		if len(r.toolResults()) == 0 {
			// report a huge prompt so the next step crosses the threshold
			return fakeResp{Calls: []fakeCall{{ID: "p1", Name: "bash", Args: `{"command":"echo hi"}`}}, UsageIn: 110000, UsageOut: 10}
		}
		return fakeResp{Text: "done", UsageIn: 1000, UsageOut: 10}
	}), nil)
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer"}}
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 || !reqs[1].isCompact() {
		t.Fatalf("expected tool step, summary, final; got %d (compact@1=%v)", len(reqs), len(reqs) > 1 && reqs[1].isCompact())
	}
	if h.ui.turns[0].Usage.In != 110000+1000 {
		t.Errorf("turn usage = %+v", h.ui.turns[0].Usage)
	}
}

func TestAgentOverflowAfterFallbackCompactsWithTurnModel(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{Status: 429, Body: `{"error":{"message":"rate limited"}}`}
		}
		if r.isCompact() {
			return fakeResp{Text: "SUMMARY"}
		}
		if len(r.Messages) > 3 {
			return fakeResp{Status: 400, Body: `{"error":{"code":"context_length_exceeded"}}`}
		}
		return fakeResp{Text: "ok after compaction"}
	}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}, {Role: "assistant", Content: "d"}}
	h.ag.retryDelay = acFastRetry
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "ok after compaction" {
		t.Errorf("final = %+v", lm)
	}
	for _, r := range h.fs.requests() {
		if r.isCompact() && r.Model != "m2" {
			t.Errorf("summary request went to %s, want the turn's fallback m2", r.Model)
		}
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func readEntries(t *testing.T, path string) []sessEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []sessEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e sessEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAgentSessionJSONLWrittenAndReloaded(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "j1", Name: "bash", Args: `{"command":"echo s"}`}}},
		fakeResp{Text: "first done"},
		fakeResp{Text: "second done"},
	), nil)
	if err := h.run(t, "turn one"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "turn two"); err != nil {
		t.Fatal(err)
	}
	if h.ag.sess == nil || h.ag.SessionID == "" {
		t.Fatal("session not created")
	}
	if !strings.HasPrefix(h.ag.sess.Path, filepath.Join(os.Getenv("JAZZ_HOME"), "data", "sessions")) {
		t.Errorf("session stored outside JAZZ_HOME: %s", h.ag.sess.Path)
	}
	fi, err := os.Stat(h.ag.sess.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("session file perms = %v (%v)", fi.Mode().Perm(), err)
	}
	ents := readEntries(t, h.ag.sess.Path)
	if ents[0].T != "meta" || ents[0].Model != "fake/m1" || ents[0].System != h.ag.System || ents[0].Cwd != h.root {
		t.Errorf("meta entry = %+v", ents[0])
	}
	got := 0
	for _, e := range ents {
		if e.T == "msg" {
			got++
		}
	}
	if got != len(h.ag.Msgs) {
		t.Errorf("msg entries = %d, history = %d", got, len(h.ag.Msgs))
	}
	_, msgs, model, system, err := LoadSession(h.ag.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if model != "fake/m1" || system != h.ag.System {
		t.Errorf("model/system = %q / len %d", model, len(system))
	}
	if len(msgs) != len(h.ag.Msgs) {
		t.Fatalf("loaded %d msgs, want %d", len(msgs), len(h.ag.Msgs))
	}
	for i := range msgs {
		a, b := msgs[i], h.ag.Msgs[i]
		if a.Role != b.Role || a.Content != b.Content || a.ToolCallID != b.ToolCallID || len(a.ToolCalls) != len(b.ToolCalls) {
			t.Errorf("msg %d differs: %+v vs %+v", i, a, b)
		}
	}
	list := ListSessions(h.root)
	if len(list) != 1 || list[0].ID != h.ag.SessionID || list[0].Title != "turn one" || list[0].Msgs != len(h.ag.Msgs) {
		t.Errorf("ListSessions = %+v", list)
	}
}

func TestAgentSessionHonoursCompactEntries(t *testing.T) {
	h := newHarness(t, summaryServer(seq(fakeResp{Text: "a1"}, fakeResp{Text: "a2"}, fakeResp{Text: "a3"})), nil)
	if err := h.run(t, "q1"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "q2"); err != nil {
		t.Fatal(err)
	}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "q3"); err != nil {
		t.Fatal(err)
	}
	h.ag.sess.Close()
	s, msgs, _, _, err := LoadSession(h.ag.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(msgs) != len(h.ag.Msgs) {
		t.Fatalf("loaded %d, live %d: %+v", len(msgs), len(h.ag.Msgs), msgs)
	}
	for i := range msgs {
		if msgs[i].Content != h.ag.Msgs[i].Content || msgs[i].Role != h.ag.Msgs[i].Role {
			t.Errorf("msg %d: %q vs %q", i, msgs[i].Content, h.ag.Msgs[i].Content)
		}
	}
	if !strings.HasPrefix(msgs[0].Content, "<summary>") {
		t.Errorf("first loaded message must be the summary: %q", msgs[0].Content)
	}
	for _, m := range msgs {
		if m.Content == "q1" {
			t.Errorf("pre-compaction message leaked into reload")
		}
	}
	ents := readEntries(t, h.ag.sess.Path)
	nc := 0
	for _, e := range ents {
		if e.T == "compact" {
			nc++
			if !strings.Contains(e.Summary, "SUMMARY-TEXT") {
				t.Errorf("compact entry summary = %q", e.Summary)
			}
		}
	}
	if nc != 1 {
		t.Errorf("compact entries = %d", nc)
	}
	// appending after reload keeps working
	s.Append(Message{Role: "user", Content: "after reload"})
	s.Close()
	s2, msgs2, _, _, _ := LoadSession(h.ag.sess.Path)
	s2.Close()
	if lastMsg(msgs2).Content != "after reload" {
		t.Errorf("append after LoadSession lost: %+v", lastMsg(msgs2))
	}
}

func TestAgentLoadSessionToleratesTornLineAndRepairs(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "x.jsonl")
	lines := []string{
		jsonArgs(sessEntry{T: "meta", Model: "fake/m1", System: "SYS"}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "user", Content: "hi"}}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "z1", Name: "bash", Args: "{}"}, {ID: "z2", Name: "read", Args: "{}"}}}}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "tool", ToolCallID: "z1", ToolName: "bash", Content: "ok"}}),
		jsonArgs(sessEntry{T: "model", Model: "fake/m2"}),
		`{"t":"msg","msg":{"role":"tool","tool_ca`, // torn
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	s, msgs, model, system, err := LoadSession(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if model != "fake/m2" || system != "SYS" || s.ID != "x" {
		t.Errorf("model=%q system=%q id=%q", model, system, s.ID)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[3].ToolCallID != "z2" || !strings.Contains(msgs[3].Content, "TOOL_OUTCOME_UNKNOWN") {
		t.Errorf("missing z2 not repaired: %+v", msgs[3])
	}
	assertPairedMsgs(t, msgs)
	if _, _, _, _, err := LoadSession(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Errorf("LoadSession on missing file should fail")
	}
}

func TestAgentRepairHistory(t *testing.T) {
	asst := func(ids ...string) Message {
		m := Message{Role: "assistant"}
		for _, id := range ids {
			m.ToolCalls = append(m.ToolCalls, ToolCall{ID: id, Name: "bash"})
		}
		return m
	}
	tool := func(id string) Message {
		return Message{Role: "tool", ToolCallID: id, ToolName: "bash", Content: "r-" + id}
	}
	user := func(s string) Message { return Message{Role: "user", Content: s} }
	cases := []struct {
		name string
		in   []Message
		want []string // role:id-or-content; "U:<id>" = synthetic unknown
	}{
		{"empty", nil, nil},
		{"complete", []Message{user("q"), asst("a"), tool("a"), {Role: "assistant", Content: "x"}}, []string{"user:q", "assistant:", "tool:r-a", "assistant:x"}},
		{"all missing at end", []Message{user("q"), asst("a", "b")}, []string{"user:q", "assistant:", "U:a", "U:b"}},
		{"partial", []Message{asst("a", "b"), tool("b"), user("next")}, []string{"assistant:", "tool:r-b", "U:a", "user:next"}},
		{"missing then user", []Message{asst("a"), user("next"), asst("c"), tool("c")}, []string{"assistant:", "U:a", "user:next", "assistant:", "tool:r-c"}},
		{"two assistants", []Message{asst("a"), asst("b"), tool("b")}, []string{"assistant:", "U:a", "assistant:", "tool:r-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := repairHistory(tc.in)
			var got []string
			for _, m := range out {
				switch {
				case m.Role == "tool" && strings.HasPrefix(m.Content, "TOOL_OUTCOME_UNKNOWN"):
					got = append(got, "U:"+m.ToolCallID)
					if m.ToolName != "bash" {
						t.Errorf("synthetic result should carry tool name")
					}
				default:
					got = append(got, m.Role+":"+m.Content)
				}
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
			assertPairedMsgs(t, out)
		})
	}
}

func TestAgentNilSessionIsSafe(t *testing.T) {
	var s *Session
	s.Append(Message{Role: "user"})
	s.Model("x")
	s.Compact("sum", []Message{{Role: "user"}})
	s.Close()
}

func TestAgentProjectSlug(t *testing.T) {
	if got := projectSlug("/"); got != "root" {
		t.Errorf("slug(/) = %q", got)
	}
	if got := projectSlug("/home/u/proj"); got != "home-u-proj" {
		t.Errorf("slug = %q", got)
	}
	long := "/" + strings.Repeat("abc/", 40) + "tail"
	got := projectSlug(long)
	if len(got) != 80 || !strings.HasSuffix(got, "-tail") {
		t.Errorf("long slug = %q (len %d); want last 80 chars", got, len(got))
	}
	if projectSlug("/a/b") == projectSlug("/a/c") {
		t.Errorf("different roots must not share a slug")
	}
}

// ---------------------------------------------------------------------------
// Pure helpers in agent.go
// ---------------------------------------------------------------------------

func TestAgentExtractTextToolCalls(t *testing.T) {
	cases := []struct {
		in       string
		names    []string
		args     []string
		wantRest string
	}{
		{`no calls here`, nil, nil, "no calls here"},
		{`<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call>`, []string{"read"}, []string{`{"path":"a"}`}, ""},
		{`a <tool_call>{"name":"read","parameters":{"path":"a"}}</tool_call> b`, []string{"read"}, []string{`{"path":"a"}`}, "a  b"},
		{"<tool_call>\n{\"name\":\"bash\"}\n</tool_call>", []string{"bash"}, []string{"{}"}, ""},
		{`<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call><tool_call>{"name":"bash","arguments":{"command":"ls"}}</tool_call>`,
			[]string{"read", "bash"}, []string{`{"path":"a"}`, `{"command":"ls"}`}, ""},
		{`<function=write>{"path":"x","content":"y"}</function>`, []string{"write"}, []string{`{"path":"x","content":"y"}`}, ""},
		{`<tool_call>{"arguments":{}}</tool_call>`, nil, nil, `<tool_call>{"arguments":{}}</tool_call>`},
	}
	for i, tc := range cases {
		calls, rest := extractTextToolCalls(tc.in)
		if len(calls) != len(tc.names) {
			t.Errorf("case %d: %d calls, want %d (%+v)", i, len(calls), len(tc.names), calls)
			continue
		}
		ids := map[string]bool{}
		for j, c := range calls {
			if c.Name != tc.names[j] || c.Args != tc.args[j] {
				t.Errorf("case %d call %d = %+v", i, j, c)
			}
			if c.ID == "" || ids[c.ID] {
				t.Errorf("case %d: call IDs must be unique and non-empty: %q", i, c.ID)
			}
			ids[c.ID] = true
		}
		if rest != tc.wantRest {
			t.Errorf("case %d rest = %q want %q", i, rest, tc.wantRest)
		}
	}
}

func TestAgentCompactThreshold(t *testing.T) {
	cases := []struct{ win, maxOut, want int }{
		{128000, 16000, 96000}, // min(102400, 96000)
		{200000, 8000, 160000}, // min(160000, 176000)
		{32000, 16000, 16000},  // negative -> floor w/2
		{0, 1000, 102400},      // default window 128000
	}
	for _, tc := range cases {
		a := &Agent{cfg: &Config{ContextWindow: tc.win, MaxTokens: tc.maxOut}}
		if got := a.compactThreshold(); got != tc.want {
			t.Errorf("win=%d maxOut=%d: threshold=%d want %d", tc.win, tc.maxOut, got, tc.want)
		}
	}
}

func TestAgentIsCodeFileAndArgPath(t *testing.T) {
	for p, want := range map[string]bool{"a.go": true, "A.PY": true, "x/y.tsx": true, "README.md": false, "Makefile": false, "notes.txt": false} {
		if isCodeFile(p) != want {
			t.Errorf("isCodeFile(%q) != %v", p, want)
		}
	}
	for raw, want := range map[string]string{`{"path":"a.go"}`: "a.go", `{"file_path":"b.go"}`: "b.go", `not json`: "", `{}`: ""} {
		if got := argPath(raw); got != want {
			t.Errorf("argPath(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestAgentHashCallDistinguishesFields(t *testing.T) {
	a := hashCall("bash", "x", "y")
	if a != hashCall("bash", "x", "y") {
		t.Error("hash not deterministic")
	}
	for _, o := range []string{hashCall("read", "x", "y"), hashCall("bash", "x2", "y"), hashCall("bash", "x", "y2"), hashCall("bashx", "", "y")} {
		if o == a {
			t.Error("hash collision across fields")
		}
	}
}
