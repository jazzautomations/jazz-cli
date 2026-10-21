package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ccSolution is a 60-line Python file, the shape of a polyglot solution.
func ccSolution() string {
	var b strings.Builder
	b.WriteString("class StackUnderflowError(Exception):\n    pass\n\n\n")
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&b, "def op_%d(stack):\n    if len(stack) < 2:\n        raise StackUnderflowError(\"Insufficient number of items in stack\")\n    return stack[:-2] + [stack[-2] + %d]\n", i, i)
	}
	return b.String()
}

// ccCutArgs is the JSON arguments of write(path, content) cut after `at`
// bytes of the encoded content (mid-line, like a real max_tokens cut).
func ccCutArgs(path, content string, at int) string {
	p, _ := json.Marshal(path)
	c, _ := json.Marshal(content)
	full := `{"path":` + string(p) + `,"content":` + string(c) + `}` // models send path first
	i := strings.Index(full, `"content":"`) + len(`"content":"`)
	return full[:i+at]
}

func TestCCParseWriteCutMidLine(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 700)
	cut, ok := ccParse(ToolCall{ID: "w1", Name: "write", Args: raw})
	if !ok {
		t.Fatalf("a write cut inside content must be continuable: %s", raw[len(raw)-60:])
	}
	if cut.Path != "forth.py" || cut.Field != "content" {
		t.Fatalf("path=%q field=%q", cut.Path, cut.Field)
	}
	if !strings.HasSuffix(cut.Value, "\n") || !strings.HasPrefix(sol, cut.Value) || len(cut.Value) == 0 {
		t.Fatalf("kept value must be a prefix of the file ending at a complete line: %q", cut.Value)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(cut.Args(sol)), &m); err != nil || m["content"] != sol || m["path"] != "forth.py" {
		t.Fatalf("rebuilt args invalid: %v %v", err, m)
	}
}

func TestCCParseCutEscapes(t *testing.T) {
	for _, tail := range []string{`\`, `\u00`, `\u`, `ab\\`} {
		raw := `{"path":"a.py","content":"x = 1\ny = \"q\"\nz` + tail
		cut, ok := ccParse(ToolCall{Name: "write", Args: raw})
		if !ok || cut.Value != "x = 1\ny = \"q\"\n" {
			t.Errorf("tail %q: ok=%v value=%q", tail, ok, cut)
		}
	}
}

func TestCCParseAliasesAndEdit(t *testing.T) {
	cases := []struct {
		name, tool, raw string
		ok              bool
		field           string
	}{
		{"write alias keys", "write", `{"file_path":"a.py","contents":"a\nb\nc`, true, "content"},
		{"edit new_string", "edit", `{"path":"a.py","old_string":"pass\n","new_string":"x = 1\ny = 2\nz`, true, "new_string"},
		{"edit edits[] last item", "edit", `{"path":"a.py","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d\ne\nf`, true, "new_string"},
		{"cut in old_string", "edit", `{"path":"a.py","old_string":"pass\nfoo`, false, ""},
		{"cut in path", "write", `{"path":"a.p`, false, ""},
		{"no path yet", "write", `{"content":"a\nb\nc`, false, ""},
		{"complete JSON", "write", `{"path":"a.py","content":"a\n"}`, false, ""},
		{"cut after content closed", "write", `{"path":"a.py","content":"a\nb\n"`, false, ""},
		{"first line only", "write", `{"path":"a.py","content":"import o`, false, ""},
		{"bash is not continued", "bash", `{"command":"echo a\necho b\nech`, false, ""},
		{"edit without old_string", "edit", `{"path":"a.py","new_string":"a\nb\nc`, false, ""},
	}
	for _, c := range cases {
		cut, ok := ccParse(ToolCall{Name: c.tool, Args: c.raw})
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.ok)
			continue
		}
		if ok && cut.Field != c.field {
			t.Errorf("%s: field %q", c.name, cut.Field)
		}
		if ok {
			if _, rep, err := parseArgs(cut.Args(cut.Value + "done\n")); err != nil || rep {
				t.Errorf("%s: rebuilt args not clean JSON: %v", c.name, err)
			}
		}
	}
}

func TestCCParseDegenerateLoopNotContinued(t *testing.T) {
	loop := "def f():\n" + strings.Repeat("    x = 1\n    y = 2\n", 40)
	if _, ok := ccParse(ToolCall{Name: "write", Args: ccCutArgs("f.py", loop, len(loop)-3)}); ok {
		t.Error("a repetition loop cut at the limit must not be continued")
	}
	if _, ok := ccParse(ToolCall{Name: "write", Args: ccCutArgs("f.py", ccSolution(), 1500)}); !ok {
		t.Error("real code with repeated shapes is still continued")
	}
}

func TestCCClean(t *testing.T) {
	kept := "def a():\n    return 1\n\n\ndef b():\n    return 2\n"
	cases := []struct{ name, cont, want string }{
		{"plain", "def c():\n    return 3\n", "def c():\n    return 3\n"},
		{"fenced", "```python\ndef c():\n    return 3\n```\n", "def c():\n    return 3\n"},
		{"prose then fence", "Here is the rest:\n```\ndef c():\n    return 3\n```", "def c():\n    return 3\n"},
		{"repeated tail", "def b():\n    return 2\ndef c():\n    return 3\n", "def c():\n    return 3\n"},
		{"short repeat kept", "\ndef c():\n    pass\n", "\ndef c():\n    pass\n"},
	}
	for _, c := range cases {
		got, restart := ccClean(c.cont, kept)
		if got != c.want || restart {
			t.Errorf("%s: got %q restart=%v", c.name, got, restart)
		}
	}
	if got, restart := ccClean(kept+"def c():\n    return 3\n", kept); !restart || !strings.HasPrefix(got, "def a()") {
		t.Errorf("whole file again must be a restart: %v %q", restart, got)
	}
}

// OpenAI-style stream: finish_reason=length inside write.content. The rest
// comes from a continuation request; the tool runs once with the whole file.
func TestAgentCutWriteContinuedAndSpliced(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 900)
	pre, _ := ccParse(ToolCall{Name: "write", Args: raw})
	rest := strings.TrimPrefix(sol, pre.Value)
	half := rest[:strings.Index(rest[len(rest)/2:], "\n")+len(rest)/2+1]
	h := newHarness(t, seq(
		fakeResp{Text: "Writing the solution.", Finish: "length", Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}},
		fakeResp{Text: "```python\n" + half + "def cut_mid", Finish: "length"}, // cut again: fence opened, partial line
		fakeResp{Text: strings.TrimPrefix(rest, half)},
		fakeResp{Text: "pronto"},
	), func(c *Config) {
		c.Models = map[string]*ModelConf{"fake/m1": {MaxOut: 4000}}
		c.NoVerifyNudge = true
	})
	if err := h.run(t, "implemente forth.py"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(h.root, "forth.py"))
	if err != nil || string(b) != sol {
		t.Fatalf("spliced file differs (err=%v):\n%s", err, b)
	}
	reqs := h.fs.requests()
	if len(reqs) != 4 {
		t.Fatalf("want 1 + 2 continuations + 1 = 4 requests, got %d", len(reqs))
	}
	c1 := reqs[1]
	if last, _ := c1.last()["content"].(string); !strings.Contains(last, "ONLY the remaining lines") || !strings.Contains(last, ccTail(pre.Value, ccTailLines)) {
		t.Fatalf("continuation prompt must carry the instruction and the tail: %q", last)
	}
	if c1.Raw["tool_choice"] != "none" {
		t.Errorf("continuation must not call tools: %v", c1.Raw["tool_choice"])
	}
	if c1.Raw["max_tokens"] != float64(2000) || reqs[0].Raw["max_tokens"] != float64(1000) {
		t.Errorf("adaptive budget: first %v, continuation %v (want 1000, 2000)", reqs[0].Raw["max_tokens"], c1.Raw["max_tokens"])
	}
	// cache: same system prompt and the same history prefix in every request
	for i, r := range reqs {
		if r.system() != reqs[0].system() {
			t.Fatalf("request %d changed the system prompt", i)
		}
	}
	p0, _ := json.Marshal(reqs[0].Messages)
	p1, _ := json.Marshal(reqs[1].Messages[:len(reqs[0].Messages)])
	p3, _ := json.Marshal(reqs[3].Messages[:len(reqs[0].Messages)])
	if string(p0) != string(p1) || string(p0) != string(p3) {
		t.Error("the history prefix must stay byte-identical (prompt cache)")
	}
	// the history keeps one assistant message with the complete call, no
	// continuation chatter
	res := reqs[3].toolResults()["w1"]
	if strings.Contains(res, "TOOL_NOT_EXECUTED") || res == "" {
		t.Errorf("w1 result = %q", res)
	}
	if reqs[3].contains("user", "ONLY the remaining lines") {
		t.Error("continuation prompts must not stay in the history")
	}
	h.fs.assertPairedRequests(t)
}

// Cap: after ccMaxConts cut continuations the call is refused as before.
func TestAgentCutWriteContinuationCap(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 400)
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch {
		case n == 0:
			return fakeResp{Finish: "length", Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}}
		case n <= ccMaxConts:
			return fakeResp{Text: fmt.Sprintf("x%d = %d\ny%d = ", n, n, n), Finish: "length"}
		case n == ccMaxConts+1:
			return fakeResp{Text: "vou escrever em partes"}
		}
		return fakeResp{Text: "(end)"}
	}, nil)
	if err := h.run(t, "implemente forth.py"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "forth.py")); err == nil {
		t.Error("a call still cut after the cap must not run")
	}
	reqs := h.fs.requests()
	if len(reqs) != ccMaxConts+2 {
		t.Fatalf("want 1 + %d continuations + 1 = %d requests, got %d", ccMaxConts, ccMaxConts+2, len(reqs))
	}
	if res := reqs[ccMaxConts+1].toolResults()["w1"]; !strings.Contains(res, "TOOL_NOT_EXECUTED") {
		t.Errorf("fallback refusal missing: %q", res)
	}
	h.fs.assertPairedRequests(t)
}

func TestAgentCutWriteContinueOff(t *testing.T) {
	for _, how := range []string{"env", "config", "flag"} {
		t.Run(how, func(t *testing.T) {
			raw := ccCutArgs("forth.py", ccSolution(), 400)
			h := newHarness(t, seq(
				fakeResp{Finish: "length", Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}},
				fakeResp{Text: "ok"},
			), func(c *Config) {
				switch how {
				case "config":
					c.CutoffContinue = "off"
				case "flag":
					c.CutoffContinue, c.CutoffContinueRun = "on", "off"
				}
			})
			if how == "env" {
				t.Setenv("JAZZ_CUTOFF_CONTINUE", "off")
			}
			if err := h.run(t, "implemente forth.py"); err != nil {
				t.Fatal(err)
			}
			reqs := h.fs.requests()
			if len(reqs) != 2 || !strings.Contains(reqs[1].toolResults()["w1"], "TOOL_NOT_EXECUTED") {
				t.Fatalf("off: the old refusal path, requests=%d", len(reqs))
			}
		})
	}
}

// Provider error variant: "Tool calls cutoff by max_tokens" after the call
// streamed (Poolside via OpenRouter/Kilo). The partial call is kept and continued.
func TestAgentToolCutErrorWithPartialCallContinued(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 500)
	pre, _ := ccParse(ToolCall{Name: "write", Args: raw})
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}, ErrAfterCalls: "Upstream error from Poolside: HttpError: HTTP 400: Tool calls cutoff by max_tokens."},
		fakeResp{Text: strings.TrimPrefix(sol, pre.Value)},
		fakeResp{Text: "pronto"},
	), nil)
	if err := h.run(t, "implemente forth.py"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "forth.py")); string(b) != sol {
		t.Fatalf("file not completed:\n%s", b)
	}
	h.fs.assertPairedRequests(t)
}

// The reply after a refused cut call asks for more output tokens (once).
func TestAgentAdaptiveBudgetAfterCut(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Finish: "length", Calls: []fakeCall{{ID: "w1", Name: "write", Args: `{"path":"a.txt","content":"x"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: `{"path":"a.txt","content":"x"}`}}},
		fakeResp{Text: "ok"},
	), func(c *Config) { c.Models = map[string]*ModelConf{"fake/m1": {MaxOut: 1500}} })
	if err := h.run(t, "escreva"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	got := []any{reqs[0].Raw["max_tokens"], reqs[1].Raw["max_tokens"], reqs[2].Raw["max_tokens"]}
	if got[0] != float64(1000) || got[1] != float64(1500) || got[2] != float64(1000) {
		t.Errorf("max_tokens per request = %v, want 1000 1500 1000", got)
	}
	if res := reqs[1].toolResults()["w1"]; !strings.Contains(res, "max_tokens=1000") {
		t.Errorf("refusal reports the cap that was used: %q", res)
	}
}

func TestBoostedOut(t *testing.T) {
	pvTestHome(t)
	c := &Config{ContextWindow: 128000, MaxTokens: 16000, Models: map[string]*ModelConf{
		"p/known":  {MaxOut: 32768},
		"p/big":    {MaxOut: 524288},
		"p/small":  {MaxOut: 8000},
		"p/window": {MaxOut: 65536, Context: 40000},
	}}
	cases := map[string]int{
		"p/unknown": 16000, // no known ceiling: never above max_tokens
		"p/known":   32000,
		"p/big":     32000, // twice the cap at most
		"p/small":   16000,
		"p/window":  20000, // at most half the window
	}
	for ref, want := range cases {
		if got := c.BoostedOut(ref); got != want {
			t.Errorf("%s: %d want %d", ref, got, want)
		}
	}
	c.MaxOutputTokens = 24000
	if got := c.BoostedOut("p/known"); got != 24000 {
		t.Errorf("max_output_tokens caps the raise: %d", got)
	}
	if got := c.BoostedOut("p/unknown"); got != 24000 {
		t.Errorf("max_output_tokens sets the ceiling when nothing is known: %d", got)
	}
	// the default free model has a known ceiling
	d, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := d.BoostedOut("kilo/" + kiloDefaultModel); got <= 16000 {
		t.Errorf("kilo default model must be raised after a cut, got %d", got)
	}
}

func TestPromptRuleSkeletonForLongFiles(t *testing.T) {
	rule := "A new file longer than ~150 lines: write a skeleton first"
	if strings.Count(basePrompt, rule) != 1 {
		t.Fatal("static skeleton rule missing from the base prompt")
	}
	i := strings.Index(basePrompt, rule)
	if i < strings.Index(basePrompt, "<rules>") || i > strings.Index(basePrompt, "</rules>") {
		t.Fatal("skeleton rule must sit inside <rules>")
	}
	if !strings.Contains(basePrompt[i:i+250], "edits[]") {
		t.Error("the rule must point to edit with edits[]")
	}
}

// A reply cut while still reasoning (no text, no call): the reasoning is not
// replayed, the reminder says to act, and max_tokens is not raised.
func TestAgentReasoningOnlyCutDropped(t *testing.T) {
	long := strings.Repeat("hmm, maybe the test wants X. ", 400)
	h := newHarness(t, seq(
		fakeResp{Reasoning: long, Finish: "length"},
		fakeResp{Text: "ok"},
	), func(c *Config) { c.Models = map[string]*ModelConf{"fake/m1": {MaxOut: 4000}} })
	if err := h.run(t, "implemente"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	b, _ := json.Marshal(reqs[1].Messages)
	if strings.Contains(string(b), "maybe the test wants") {
		t.Error("the cut reasoning must not be replayed")
	}
	if !reqs[1].contains("user", "whole output token limit on internal reasoning") {
		t.Error("act-now reminder missing")
	}
	if reqs[1].Raw["max_tokens"] != float64(1000) {
		t.Errorf("no extra room to think: max_tokens=%v", reqs[1].Raw["max_tokens"])
	}
}

// A stream that errors in the middle of a write: the partial reply (text +
// cut call) is persisted, and the next turn (--resume / -c) continues the
// write from it instead of starting over.
func TestAgentStreamErrorMidToolCallPersistedAndResumed(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 800)
	pre, _ := ccParse(ToolCall{Name: "write", Args: raw})
	h := newHarness(t, seq(
		fakeResp{Text: "Writing it.", Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}, ErrAfterCalls: "upstream connection reset", ErrCode: 400},
		fakeResp{Text: strings.TrimPrefix(sol, pre.Value)},
		fakeResp{Text: "pronto"},
	), func(c *Config) { c.NoVerifyNudge = true })
	if err := h.run(t, "implemente forth.py"); err == nil {
		t.Fatal("the failed stream must surface as an error")
	}
	var last Message
	for _, m := range h.ag.Msgs {
		if m.Role == "assistant" {
			last = m
		}
	}
	if len(last.PartialCalls) != 1 || last.PartialCalls[0].Args != raw || !strings.Contains(last.Content, "Writing it.") || !strings.Contains(last.Content, "stream failed") {
		t.Fatalf("partial reply not persisted: %+v", last)
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("JAZZ_HOME"), "data", "sessions", "*", "*.jsonl"))
	disk := ""
	for _, f := range files {
		b, _ := os.ReadFile(f)
		disk += string(b)
	}
	if !strings.Contains(disk, `"partial_calls"`) {
		t.Fatal("the partial call must reach the session file")
	}
	if err := h.run(t, "continue"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "forth.py")); string(b) != sol {
		t.Fatalf("resumed write not completed:\n%s", b)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 || !reqs[1].contains("user", "ONLY the remaining lines") {
		t.Fatalf("want failed + continuation + final = 3 requests, got %d", len(reqs))
	}
	h.fs.assertPairedRequests(t)
}

// The run deadline (context cancelled) while a write streams: the partial
// call is kept in the history, structured.
func TestAgentDeadlineMidToolCallKeepsPartial(t *testing.T) {
	raw := ccCutArgs("forth.py", ccSolution(), 800)
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: raw}}, HangAfterCalls: true}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if _, err := h.ag.Run(ctx, "implemente forth.py"); err == nil {
		t.Fatal("want the deadline error")
	}
	found := false
	for _, m := range h.ag.Msgs {
		if m.Role == "assistant" && len(m.PartialCalls) == 1 && m.PartialCalls[0].Args == raw {
			found = true
		}
	}
	if !found {
		t.Fatal("partial write lost at the deadline")
	}
}
