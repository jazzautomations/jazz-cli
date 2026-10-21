//go:build !windows

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// acFastRetry keeps the retry policy's shape (retry or move on) without the waits.
func acFastRetry(err error, attempt int) time.Duration {
	if RetryDelay(err, attempt) > 0 {
		return 5 * time.Millisecond
	}
	return 0
}

func acMsgsWithRole(msgs []map[string]any, role string) int {
	n := 0
	for _, m := range msgs {
		if m["role"] == role {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// G1: truncation guard
// ---------------------------------------------------------------------------

func TestAgentTruncatedToolCallsNotExecuted(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Finish: "length", Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"half.txt","content":"only the first half"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: `{"path":"full.txt","content":"complete"}`}}},
		fakeResp{Text: "done"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "write it")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "half.txt")); err == nil {
		t.Errorf("a write from a truncated response must not run")
	}
	if _, err := os.Stat(filepath.Join(h.root, "full.txt")); err != nil {
		t.Errorf("the re-issued write should run")
	}
	res := h.fs.requests()[1].toolResults()["w1"]
	if !strings.Contains(res, "TOOL_NOT_EXECUTED") || !strings.Contains(res, "max_tokens=1000") {
		t.Errorf("w1 result = %q", res)
	}
	if !anyContains(h.ui.warns, "cortada") {
		t.Errorf("warn missing: %v", h.ui.warns)
	}
}

func TestAgentTruncatedTextContinuedThenStops(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Text: "part " + string(rune('A'+n)), Finish: "length"}
	}, nil)
	stop, err := h.ag.Run(context.Background(), "long answer")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopTruncated {
		t.Errorf("stop = %s, want truncated", stop)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 1 + 2 continuations = 3 requests, got %d", len(reqs))
	}
	if !reqs[1].contains("user", "cut off at the output limit") {
		t.Errorf("continuation reminder missing")
	}
	if h.ui.turns[0].Stop != string(StopTruncated) {
		t.Errorf("TurnStats.Stop = %q", h.ui.turns[0].Stop)
	}
}

// ---------------------------------------------------------------------------
// bug 25/37: step limit
// ---------------------------------------------------------------------------

func TestAgentStepLimitIsHardBound(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Calls: []fakeCall{{Name: "bash", Args: `{"command":"echo x` + string(rune('a'+n%26)) + ` >> ran.txt"}`}}}
	}, func(c *Config) { c.MaxSteps = 3 })
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil || stop != StopStepLimit {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 4 {
		t.Errorf("requests = %d, want MaxSteps+1 = 4", n)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, "ran.txt"))
	if n := strings.Count(string(b), "\n"); n != 3 {
		t.Errorf("%d commands ran, want 3", n)
	}
}

// ---------------------------------------------------------------------------
// bug 65 / 69, G15: text tool calls
// ---------------------------------------------------------------------------

func TestAgentWhitespaceOnlyTextNotStored(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "\n\n", Calls: []fakeCall{{ID: "e1", Name: "bash", Args: `{"command":"echo hi"}`}}},
		fakeResp{Text: "ok"},
	), nil)
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if c := h.ag.Msgs[1].Content; c != "" {
		t.Errorf("assistant content = %q, want empty", c)
	}
}

func TestAgentTextCallExamplesNotExecuted(t *testing.T) {
	cases := map[string]string{
		"fenced example":     "A Hermes call looks like this:\n```\n<tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"echo EXAMPLE > ex.txt\"}}</tool_call>\n```\nThat is all.",
		"example then prose": "Example: <tool_call>{\"name\":\"bash\",\"arguments\":{\"command\":\"echo EXAMPLE > ex.txt\"}}</tool_call> and the model stops there.",
		"unknown tool":       "<tool_call>{\"name\":\"launch_rockets\",\"arguments\":{}}</tool_call>",
	}
	for name, txt := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, seq(fakeResp{Text: txt}), nil)
			if err := h.run(t, "how does a tool call look?"); err != nil {
				t.Fatal(err)
			}
			if n := len(h.fs.requests()); n != 1 {
				t.Errorf("requests = %d, want 1 (no call executed)", n)
			}
			if _, err := os.Stat(filepath.Join(h.root, "ex.txt")); err == nil {
				t.Errorf("example call was executed")
			}
			if lm := lastMsg(h.ag.Msgs); len(lm.ToolCalls) != 0 || lm.Content != strings.TrimSpace(txt) {
				t.Errorf("answer must stay as text: %+v", lm)
			}
		})
	}
}

func TestAgentTextCallFormats(t *testing.T) {
	known := func(n string) string {
		switch c := CanonicalTool(n); c {
		case "read", "bash", "write", "edit", "todo":
			return c
		}
		return ""
	}
	cases := []struct {
		name, in, tool, args, rest string
	}{
		{"mistral array", `[TOOL_CALLS][{"name":"read","arguments":{"path":"a.go"}}]`, "read", `{"path":"a.go"}`, ""},
		{"mistral v11", `[TOOL_CALLS]bash[ARGS]{"command":"ls"}`, "bash", `{"command":"ls"}`, ""},
		{"fenced json", "I'll list files.\n```json\n{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}\n```", "bash", `{"command":"ls"}`, "I'll list files."},
		{"whole json", `{"name":"read","parameters":{"path":"x"}}`, "read", `{"path":"x"}`, ""},
		{"whole json tool key", `{"tool":"bash","arguments":"{\"command\":\"pwd\"}"}`, "bash", `{"command":"pwd"}`, ""},
		{"harmony", `<|start|>assistant<|channel|>commentary to=functions.bash <|constrain|>json<|message|>{"command":"ls -la"}<|call|>`, "bash", `{"command":"ls -la"}`, ""},
		{"llama python_tag", `<|python_tag|>{"name":"read","parameters":{"path":"go.mod"}}<|eom_id|>`, "read", `{"path":"go.mod"}`, ""},
		{"tool_call special", `<|tool_call|>{"name":"bash","arguments":{"command":"ls"}}`, "bash", `{"command":"ls"}`, ""},
		{"qwen xml", "<tool_call>\n<function=bash>\n<parameter=command>\nls -1\n</parameter>\n</function>\n</tool_call>", "bash", `{"command":"ls -1"}`, ""},
		{"unclosed tool_call at end", `Checking. <tool_call>{"name":"read","arguments":{"path":"a"}}`, "read", `{"path":"a"}`, "Checking."},
		{"alias name", `<tool_call>{"name":"functions.Bash","arguments":{"command":"ls"}}</tool_call>`, "bash", `{"command":"ls"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, rest := parseTextToolCalls(c.in, known, true)
			if len(calls) != 1 {
				t.Fatalf("calls = %+v", calls)
			}
			if calls[0].Name != c.tool || calls[0].Args != c.args {
				t.Errorf("call = %+v, want %s %s", calls[0], c.tool, c.args)
			}
			if rest != c.rest {
				t.Errorf("rest = %q, want %q", rest, c.rest)
			}
		})
	}
	if calls, _ := parseTextToolCalls(`{"name":"nope","arguments":{}}`, known, true); len(calls) != 0 {
		t.Errorf("unknown tool accepted: %+v", calls)
	}
	if calls, _ := parseTextToolCalls("```json\n{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}\n```\nThis is how you would call it.", known, true); len(calls) != 0 {
		t.Errorf("fenced example followed by prose accepted in strict mode: %+v", calls)
	}
}

func TestAgentTextToolsMode(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: `Reading.<tool_call>{"name":"bash","arguments":{"command":"echo TEXTMODE"}}</tool_call>`},
		fakeResp{Text: "all done"},
	), func(c *Config) { c.Models = map[string]*ModelConf{"fake/m1": {Tools: "text"}} })
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for i, r := range reqs {
		if _, ok := r.Raw["tools"]; ok {
			t.Errorf("request %d: text mode must not send tools", i)
		}
		if !strings.Contains(r.system(), "<tool_calling>") || !strings.Contains(r.system(), "- bash(") {
			t.Errorf("request %d: system lacks the text tools block", i)
		}
	}
	if reqs[0].system() != reqs[1].system() {
		t.Errorf("system prompt must be byte-stable across requests")
	}
	r := reqs[1]
	if acMsgsWithRole(r.Messages, "tool") != 0 {
		t.Errorf("text mode must not send tool messages")
	}
	if !r.contains("user", "<tool_result name=\"bash\"") || !r.contains("user", "TEXTMODE") {
		t.Errorf("tool result not fed back as user message: %v", r.Messages)
	}
	if !r.contains("assistant", `<tool_call>{"name":"bash"`) {
		t.Errorf("assistant call not rendered as text")
	}
	// history keeps the native shape
	if a := h.ag.Msgs[1]; len(a.ToolCalls) != 1 || h.ag.Msgs[2].Role != "tool" {
		t.Errorf("native history changed: %+v", h.ag.Msgs)
	}
}

func TestAgentToolsUnsupportedSwitchesToTextMode(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.HasTools {
			return fakeResp{Status: 400, Body: `{"error":"registry.ollama.ai/library/tiny does not support tools"}`}
		}
		return fakeResp{Text: "hello from text mode"}
	}, nil)
	if err := h.run(t, "hi"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 || reqs[1].Model != "m1" || reqs[1].HasTools {
		t.Fatalf("want a retry on the same model without tools; got %d requests", len(reqs))
	}
	if !anyContains(h.ui.notices, "modo texto") {
		t.Errorf("notice missing: %v", h.ui.notices)
	}
	if err := h.run(t, "again"); err != nil {
		t.Fatal(err)
	}
	if reqs := h.fs.requests(); reqs[len(reqs)-1].HasTools {
		t.Errorf("text mode must stick for the session")
	}
}

// ---------------------------------------------------------------------------
// G12: plan mode
// ---------------------------------------------------------------------------

func TestAgentPlanMode(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "p1", Name: "write", Args: `{"path":"x.txt","content":"x"}`},
			{ID: "p2", Name: "bash", Args: `{"command":"touch y.txt"}`},
			{ID: "p3", Name: "bash", Args: `{"command":"ls"}`},
		}},
		fakeResp{Text: "1. change x"},
	), func(c *Config) { c.ModeOverride = "plan" })
	stop, err := h.ag.Run(context.Background(), "plan it")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	r := h.fs.requests()
	if !strings.HasPrefix(h.ag.Msgs[0].Content, "<system-reminder>Plan mode is on") {
		t.Errorf("plan reminder must prefix the user message: %q", oneLine(h.ag.Msgs[0].Content, 80))
	}
	if strings.Contains(r[0].system(), "Plan mode is on") {
		t.Errorf("plan reminder must never go into System")
	}
	res := r[1].toolResults()
	if !strings.Contains(res["p1"], "Plan mode") || !strings.Contains(res["p2"], "Plan mode") {
		t.Errorf("write / mutating bash must be refused: %v", res)
	}
	if !strings.Contains(res["p3"], "[exit code 0]") {
		t.Errorf("read-only bash must run: %q", res["p3"])
	}
	for _, f := range []string{"x.txt", "y.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, f)); err == nil {
			t.Errorf("%s must not exist in plan mode", f)
		}
	}
	if len(h.ui.approvals) != 0 {
		t.Errorf("plan mode refuses without asking: %+v", h.ui.approvals)
	}
}

// ---------------------------------------------------------------------------
// G11 agent side / G5: denial feedback and StopDenied
// ---------------------------------------------------------------------------

func TestAgentDenyWithFeedbackAndStopDenied(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "d1", Name: "write", Args: `{"path":"a.txt","content":"a"}`}}},
		fakeResp{Text: "ok, I won't"},
	), func(c *Config) { c.Mode = "ask" })
	h.ui.feedback = "use b.txt instead"
	stop, err := h.ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopDenied {
		t.Errorf("stop = %s, want denied", stop)
	}
	if res := h.fs.requests()[1].toolResults()["d1"]; res != "The user denied this and said: use b.txt instead" {
		t.Errorf("result = %q", res)
	}
}

// ---------------------------------------------------------------------------
// bug 39: steering
// ---------------------------------------------------------------------------

func TestAgentLateSteerParkedAndPrepended(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n == 0 {
			return fakeResp{Status: 418, Body: "teapot"}
		}
		return fakeResp{Text: "ok"}
	}, nil)
	h.ag.Steer("typed during the failing turn")
	if err := h.run(t, "first"); err == nil {
		t.Fatal("want error")
	}
	// The UI may take it back for the editor...
	h.ag.Steer("late text")
	if s := h.ag.TakeSteer(); !strings.Contains(s, "late text") {
		t.Errorf("TakeSteer = %q", s)
	}
	if s := h.ag.TakeSteer(); s != "" {
		t.Errorf("TakeSteer must clear: %q", s)
	}
	// ...or, if nobody takes it, the next Run prepends it to its input.
	h.ag.Steer("leftover")
	h.ag.parkSteer()
	if err := h.run(t, "second"); err != nil {
		t.Fatal(err)
	}
	last := h.fs.requests()[1]
	if !last.contains("user", "leftover\n\nsecond") {
		t.Errorf("parked steer not prepended: %v", last.Messages)
	}
}

// ---------------------------------------------------------------------------
// G4: fallback for compaction, limiter
// ---------------------------------------------------------------------------

type acTestLimiter struct {
	mu      sync.Mutex
	blocked map[string]bool
	reports []string
}

func (l *acTestLimiter) Allow(ref string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.blocked[ref], time.Minute
}
func (l *acTestLimiter) Report(ref string, err error) {
	l.mu.Lock()
	l.reports = append(l.reports, ref)
	l.mu.Unlock()
}

func TestAgentLimiterSkipsBlockedCandidate(t *testing.T) {
	lim := &acTestLimiter{blocked: map[string]bool{"fake/m1": true}}
	old := DefaultLimiter
	DefaultLimiter = lim
	t.Cleanup(func() { DefaultLimiter = old })
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m2" {
			return fakeResp{Status: 500, Body: "boom"}
		}
		return fakeResp{Text: "from " + r.Model}
	}, func(c *Config) { c.Fallback = []string{"fake/m2", "fake/m3"} })
	h.ag.retryDelay = acFastRetry
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	if h.fs.countModel("m1") != 0 {
		t.Errorf("blocked m1 must be skipped")
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "from m3" {
		t.Errorf("final = %+v", lm)
	}
	if len(lim.reports) == 0 || lim.reports[0] != "fake/m2" {
		t.Errorf("failures must be reported: %v", lim.reports)
	}
}

func TestAgentCompactOverflowShrinksPrefix(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.isCompact() {
			total := 0
			for _, m := range r.Messages {
				c, _ := m["content"].(string)
				total += len(c)
			}
			if total > 30000 {
				return fakeResp{Status: 400, Body: `{"error":{"code":"context_length_exceeded"}}`}
			}
			return fakeResp{Text: "SMALL SUMMARY"}
		}
		return fakeResp{Text: "x"}
	}, nil)
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "bash", Args: `{"command":"cat big"}`}}},
		{Role: "tool", ToolCallID: "t1", ToolName: "bash", Content: big(200000)},
		{Role: "assistant", Content: "seen"},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: big(100000)},
	}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(h.ag.Msgs[0].Content, "SMALL SUMMARY") {
		t.Errorf("summary = %q", oneLine(h.ag.Msgs[0].Content, 100))
	}
}

func TestAgentCompactOverflowFallsBackToDropNote(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 400, Body: `{"error":{"code":"context_length_exceeded"}}`}
	}, nil)
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}, {Role: "assistant", Content: "d"}}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(h.ag.Msgs[0].Content, "no longer fit") {
		t.Errorf("drop note missing: %q", h.ag.Msgs[0].Content)
	}
}

// ---------------------------------------------------------------------------
// G7: task subagent
// ---------------------------------------------------------------------------

func TestAgentTaskSubagent(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if strings.HasPrefix(r.system(), "You are a research subagent") {
			if len(r.toolResults()) == 0 {
				return fakeResp{Calls: []fakeCall{
					{ID: "s1", Name: "write", Args: `{"path":"evil.txt","content":"x"}`},
					{ID: "s2", Name: "bash", Args: `{"command":"touch evil2.txt"}`},
					{ID: "s3", Name: "bash", Args: `{"command":"ls"}`},
				}, UsageIn: 100, UsageOut: 10}
			}
			return fakeResp{Text: "FOUND: main.go:12", UsageIn: 200, UsageOut: 20}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "t1", Name: "task", Args: `{"description":"find main","prompt":"where is main?"}`}}}
		}
		return fakeResp{Text: "main is in main.go"}
	}, nil)
	if err := h.run(t, "where is main"); err != nil {
		t.Fatal(err)
	}
	var child []recReq
	var parent []recReq
	for _, r := range h.fs.requests() {
		if strings.HasPrefix(r.system(), "You are a research subagent") {
			child = append(child, r)
		} else {
			parent = append(parent, r)
		}
	}
	if len(child) != 2 || len(parent) != 2 {
		t.Fatalf("child=%d parent=%d requests", len(child), len(parent))
	}
	if !strings.Contains(strings.Join(parent[0].toolNames(), ","), "task") {
		t.Errorf("parent must offer task: %v", parent[0].toolNames())
	}
	for _, n := range child[0].toolNames() {
		if n == "task" || n == "write" || n == "edit" || n == "todo" {
			t.Errorf("child must not offer %s", n)
		}
	}
	cr := child[1].toolResults()
	if !strings.Contains(cr["s1"], "read-only") || !strings.Contains(cr["s2"], "read-only") || !strings.Contains(cr["s3"], "[exit code 0]") {
		t.Errorf("child results = %v", cr)
	}
	for _, f := range []string{"evil.txt", "evil2.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, f)); err == nil {
			t.Errorf("subagent created %s", f)
		}
	}
	res := parent[1].toolResults()["t1"]
	if !strings.Contains(res, "FOUND: main.go:12") || !strings.Contains(res, "[subagent used 1 tools, 330 tokens]") {
		t.Errorf("task result = %q", res)
	}
	if h.ag.Usage.In < 300 {
		t.Errorf("child usage must be added to the parent: %+v", h.ag.Usage)
	}
	if len(h.ui.turns) != 1 {
		t.Errorf("subagent turn must not reach the parent UI's TurnEnd: %d", len(h.ui.turns))
	}
	if !anyContains(h.ui.notices, "│ bash ls") {
		t.Errorf("subagent tool lines missing: %v", h.ui.notices)
	}
}

// ---------------------------------------------------------------------------
// images
// ---------------------------------------------------------------------------

var acPNG = func() []byte {
	b, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	return b
}()

func TestAgentImageFromToolGoesInUserMessageAfterResults(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "i1", Name: "read", Args: `{"path":"shot.png"}`},
			{ID: "i2", Name: "bash", Args: `{"command":"echo after"}`},
		}},
		fakeResp{Text: "I see a pixel"},
	), nil)
	if err := os.WriteFile(filepath.Join(h.root, "shot.png"), acPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "look"); err != nil {
		t.Fatal(err)
	}
	m := h.ag.Msgs
	// user, assistant(i1,i2), tool i1, tool i2, user(image), assistant
	if len(m) != 6 || m[2].Role != "tool" || m[3].Role != "tool" || m[4].Role != "user" || len(m[4].Images) != 1 {
		t.Fatalf("history shape = %+v", m)
	}
	if m[4].Images[0].Mime != "image/png" {
		t.Errorf("mime = %q", m[4].Images[0].Mime)
	}
}

func TestAgentAttachAndSessionBlobs(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "nice picture"}), nil)
	img := ImagePart{Mime: "image/png", Data: base64.StdEncoding.EncodeToString(acPNG)}
	h.ag.Attach(img)
	if err := h.run(t, "what is this?"); err != nil {
		t.Fatal(err)
	}
	if len(h.ag.Msgs[0].Images) != 1 {
		t.Fatalf("user message must carry the image")
	}
	raw, _ := os.ReadFile(h.ag.sess.Path)
	if strings.Contains(string(raw), img.Data) || !strings.Contains(string(raw), `"data":"blob:`) {
		t.Errorf("session must store a blob reference, not the image")
	}
	_, msgs, _, _, err := LoadSession(h.ag.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs[0].Images) != 1 || msgs[0].Images[0].Data != img.Data {
		t.Errorf("image not restored from blob: %+v", msgs[0].Images)
	}
}

// ---------------------------------------------------------------------------
// G13: rewind, fork, export
// ---------------------------------------------------------------------------

func TestAgentRewindForkExport(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"a.txt","content":"one"}`}}},
		fakeResp{Text: "wrote a"},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: `{"path":"b.txt","content":"two"}`}}},
		fakeResp{Text: "wrote b"},
	), nil)
	if err := h.run(t, "first"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "second"); err != nil {
		t.Fatal(err)
	}
	turns := h.ag.UserTurns()
	if len(turns) != 2 || turns[1].Text != "second" {
		t.Fatalf("turns = %+v", turns)
	}

	md, err := h.ag.Export("md")
	if err != nil || !strings.Contains(md, "## Você\n\nfirst") || !strings.Contains(md, "**write**") || !strings.Contains(md, "wrote b") {
		t.Errorf("export md = %q (%v)", md, err)
	}
	page, err := h.ag.Export("html")
	if err != nil || !strings.Contains(page, "<pre>") || !strings.Contains(page, "&lt;details&gt;") {
		t.Errorf("export html (%v)", err)
	}
	if _, err := h.ag.Export("pdf"); err == nil {
		t.Errorf("unknown format must fail")
	}

	path := h.ag.sess.Path
	if err := h.ag.RewindTo(turns[1].Index); err != nil {
		t.Fatal(err)
	}
	if len(h.ag.Msgs) != turns[1].Index {
		t.Errorf("Msgs not truncated: %d", len(h.ag.Msgs))
	}
	if _, err := os.Stat(filepath.Join(h.root, "b.txt")); err == nil {
		t.Errorf("files written after the rewind point must be restored")
	}
	if _, err := os.Stat(filepath.Join(h.root, "a.txt")); err != nil {
		t.Errorf("earlier files must stay")
	}
	_, msgs, _, _, err := LoadSession(path)
	if err != nil || len(msgs) != turns[1].Index {
		t.Errorf("LoadSession must honour the rewind entry: %d msgs (%v)", len(msgs), err)
	}

	old := h.ag.SessionID
	id, err := h.ag.Fork(2)
	if err != nil || id == old || h.ag.SessionID != id {
		t.Fatalf("fork id=%q old=%q err=%v", id, old, err)
	}
	ents := readEntries(t, h.ag.sess.Path)
	if ents[0].T != "meta" || ents[0].Parent != old {
		t.Errorf("fork meta = %+v", ents[0])
	}
	_, fmsgs, _, _, _ := LoadSession(h.ag.sess.Path)
	if len(fmsgs) != len(h.ag.Msgs) {
		t.Errorf("fork session has %d msgs, agent %d", len(fmsgs), len(h.ag.Msgs))
	}
	if err := h.ag.RewindTo(99); err == nil {
		t.Errorf("out-of-range rewind must fail")
	}
}

func TestAgentContextBreakdown(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "x"}), nil)
	if err := h.run(t, "please explain how the build works in this project"); err != nil {
		t.Fatal(err)
	}
	parts := h.ag.ContextBreakdown()
	labels := map[string]int{}
	for _, p := range parts {
		labels[p.Label] = p.Tokens
	}
	for _, want := range []string{"sistema: base", "sistema: ambiente", "ferramentas embutidas", "mensagens do usuário"} {
		if labels[want] <= 0 {
			t.Errorf("%q missing or zero in %+v", want, parts)
		}
	}
}

func TestAgentStopReasonsAndErrors(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Status: 418, Body: "teapot"} }, nil)
	stop, err := h.ag.Run(context.Background(), "go")
	var ae *APIError
	if stop != StopError || !errors.As(err, &ae) {
		t.Errorf("stop=%s err=%v", stop, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop, err = h.ag.Run(ctx, "go")
	if stop != StopInterrupted || !errors.Is(err, context.Canceled) {
		t.Errorf("stop=%s err=%v", stop, err)
	}
}
