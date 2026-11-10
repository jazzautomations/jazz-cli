package main

// agent_toolcall_test.go: the provider rejecting the model's own generated
// tool call ("toolcall" errors) — classification, the one reminder retry, the
// move to the next candidate or text tools, and the honest final message.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Real bodies, scrubbed (no account/org/token). Groq rejects the generation
// with HTTP 400 code tool_use_failed, or mid-stream as an SSE error event —
// which arrives here as a status-0 APIError (the "HTTP 0" of the suite).
const tcBodyUnknown = `{"error":{"message":"Tool call validation failed: attempted to call tool 'repo_browser.search' which was not in request.tools","type":"invalid_request_error","code":"tool_use_failed"}}`
const tcBodyBadArgs = `{"error":{"message":"Failed to parse tool call arguments as JSON","type":"invalid_request_error","code":"tool_use_failed"}}`

func TestPVClassifyToolCall(t *testing.T) {
	yes := []struct {
		st   int
		body string
	}{
		{400, tcBodyUnknown},
		{400, tcBodyBadArgs},
		{0, "Tool call validation failed: attempted to call tool 'repo_browser.search' which was not in request.tools"},
		{0, "Failed to parse tool call arguments as JSON"},
		{422, `{"error":{"code":"tool_use_failed","message":"generation produced an invalid tool call"}}`},
	}
	for _, c := range yes {
		ae := &APIError{Status: c.st, Body: c.body}
		if k := pvClassify(ae); k != "toolcall" {
			t.Errorf("status %d: kind %q want toolcall (%s)", c.st, k, c.body)
		}
		if d := RetryDelay(ae, 0); d != 0 {
			t.Errorf("status %d: RetryDelay=%v want 0", c.st, d)
		}
	}
	no := []struct {
		st   int
		body string
		kind string
	}{
		{400, "this model does not support tools", "notools"},
		{404, "No endpoints found that support tool use", "notools"},
		{0, "dial tcp: connection refused", "transient"},
		{400, `{"error":{"message":"messages.0.content.0.tool_use.id: input should be a valid string"}}`, "other"}, // a request-side validation bug, not the model's
	}
	for _, c := range no {
		if k := pvClassify(&APIError{Status: c.st, Body: c.body}); k != c.kind {
			t.Errorf("status %d: kind %q want %q (%s)", c.st, k, c.kind, c.body)
		}
	}
	// the attempted name is extractable only when the provider names it
	if n := pvToolCallName(&APIError{Status: 400, Body: tcBodyUnknown}); n != "repo_browser.search" {
		t.Errorf("pvToolCallName = %q", n)
	}
	if n := pvToolCallName(&APIError{Status: 400, Body: tcBodyBadArgs}); n != "" {
		t.Errorf("pvToolCallName on args error = %q, want \"\"", n)
	}
}

// A rejected generation followed by a valid answer ends the turn well; the
// retried request carries the reminder with the invented name and the exact
// valid tool names.
func TestAgentToolCallReminderRetry(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Status: 400, Body: tcBodyUnknown},
		fakeResp{Text: "recovered"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (rejection + reminder retry)", len(reqs))
	}
	if !reqs[0].HasTools || !reqs[1].HasTools {
		t.Fatal("both requests must carry native tools")
	}
	last := reqs[1].last()
	if last["role"] != "user" {
		t.Fatalf("reminder must ride as a user message, got %v", last["role"])
	}
	c, _ := last["content"].(string)
	for _, want := range []string{"repo_browser.search", "reject", "bash", "read", "edit", "write", "todo"} {
		if !strings.Contains(c, want) {
			t.Errorf("reminder lacks %q:\n%s", want, c)
		}
	}
	if !anyContains(h.ui.notices, "inválida") {
		t.Errorf("recovery notice missing: %v", h.ui.notices)
	}
}

// The production shape: 200 OK, then an SSE error event with a string code —
// a status-0 APIError that used to be retried as "transient" until the chain
// died as "falha de rede".
func TestAgentToolCallStreamErrorRetry(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{ErrChunk: "Tool call validation failed: attempted to call tool 'repo_browser.search' which was not in request.tools", ErrCodeStr: "tool_use_failed"},
		fakeResp{Text: "recovered"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	c, _ := h.fs.requests()[1].last()["content"].(string)
	if !strings.Contains(c, "repo_browser.search") {
		t.Errorf("reminder lacks the attempted name: %q", c)
	}
}

// Two rejections on the requested model with a reserve available: the reserve
// answers. The first model gets exactly two requests (native + reminder).
func TestAgentToolCallFallsToSecondCandidate(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{Status: 400, Body: tcBodyUnknown}
		}
		return fakeResp{Text: "from m2"}
	}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "from m2" {
		t.Fatalf("final = %q", lm.Content)
	}
	if n := h.fs.countModel("m1"); n != 2 {
		t.Errorf("m1 requests = %d, want 2 (native + reminder retry)", n)
	}
	if n := h.fs.countModel("m2"); n != 1 {
		t.Errorf("m2 requests = %d, want 1", n)
	}
	if !anyContains(h.ui.warns, "tentando") && !anyContains(h.ui.notices, "tentando") {
		t.Errorf("fallback notice missing: %v %v", h.ui.warns, h.ui.notices)
	}
}

// No candidate left and text tools exhausted too: the final error blames the
// model, never the network.
func TestAgentToolCallHonestError(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 400, Body: tcBodyBadArgs}
	}, nil)
	_, err := h.ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("want error")
	}
	msg := friendlyErr(err)
	if strings.Contains(msg, "falha de rede") {
		t.Errorf("must not blame the network: %q", msg)
	}
	for _, want := range []string{"fake/m1", "ferramenta", "/model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("honest message lacks %q: %q", want, msg)
		}
	}
	var tce *acToolCallError
	if !errors.As(err, &tce) {
		t.Fatalf("err must carry acToolCallError: %v", err)
	}
	if tce.Ref != "fake/m1" || tce.Tries < 2 {
		t.Errorf("acToolCallError = %+v", tce)
	}
	// native + reminder retry + text-mode last resort, then it stops
	if n := len(h.fs.requests()); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

// The chosen model fails and the reserve fails too: the message cites the
// chosen model's error first and summarizes the reserves after — not the
// other way around.
func TestAgentFallbackErrorNamesChosenModel(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch r.Model {
		case "m1":
			return fakeResp{Status: 500, Body: "primary exploded"}
		case "m2":
			return fakeResp{Status: 400, Body: "reserve choked"}
		}
		return fakeResp{Text: "never"}
	}, func(c *Config) { c.Fallback = []string{"fake/m2"} })
	h.ag.retryDelay = func(error, int) time.Duration { return 0 }
	_, err := h.ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("want error")
	}
	msg := friendlyErr(err)
	if !strings.Contains(msg, "primary exploded") {
		t.Errorf("must cite the chosen model's error first: %q", msg)
	}
	if !strings.Contains(msg, "reserva") || !strings.Contains(msg, "m2") {
		t.Errorf("must summarize the failed reserve: %q", msg)
	}
	if strings.Contains(msg, "reserve choked") && !strings.Contains(msg, "primary exploded") {
		t.Errorf("the reserve's error must not be the headline: %q", msg)
	}
	var fe *acFailError
	if !errors.As(err, &fe) || fe.Ref != "fake/m1" {
		t.Errorf("err must carry acFailError for fake/m1: %v", err)
	}
}
