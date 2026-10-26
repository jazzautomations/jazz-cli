package main

// provider_anthropic_live_test.go: a fake Anthropic Messages API that checks
// each request against the documented contract (headers, required fields,
// block shapes, thinking/sampling rules of current models) and answers with a
// spec-shaped SSE stream (message_start, ping, content_block_start/delta/stop,
// message_delta, message_stop). The real provider code runs end to end against
// it, outbound redaction included.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const anthFakeKey = "sk-ant-api03-TESTKEYabcdefghijklmnopqrstuvwxyz0123456789"

type anthFake struct {
	mu       sync.Mutex
	problems []string
	bodies   []map[string]any
	events   [][2]string // SSE (event, data) frames to answer with
}

func (f *anthFake) bad(format string, args ...any) {
	f.problems = append(f.problems, fmt.Sprintf(format, args...))
}

// validate checks one request; every problem is what the real API answers 400/401 for.
func (f *anthFake) validate(r *http.Request, raw []byte) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		f.bad("endpoint %s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("x-api-key"); got != anthFakeKey {
		f.bad("x-api-key = %q (must be the raw key, never redacted)", got)
	}
	if r.Header.Get("anthropic-version") != "2023-06-01" {
		f.bad("anthropic-version = %q", r.Header.Get("anthropic-version"))
	}
	if !strings.HasPrefix(r.Header.Get("content-type"), "application/json") {
		f.bad("content-type = %q", r.Header.Get("content-type"))
	}
	if r.Header.Get("Authorization") != "" {
		f.bad("an API key goes in x-api-key, not Authorization")
	}
	if strings.Contains(string(raw), anthFakeKey) {
		f.bad("the API key leaked into the request body")
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		f.bad("body is not JSON: %v", err)
		return
	}
	f.bodies = append(f.bodies, b)
	model, _ := b["model"].(string)
	if model == "" {
		f.bad("model missing")
	}
	if mt, ok := b["max_tokens"].(float64); !ok || mt < 1 || mt > 128000 {
		f.bad("max_tokens = %v (required, 1..128000)", b["max_tokens"])
	}
	if b["stream"] != true {
		f.bad("stream must be true")
	}
	modern := pvAnthModern(model)
	if th, ok := b["thinking"].(map[string]any); ok {
		if modern && th["type"] != "adaptive" {
			f.bad("thinking %v: current models reject budget_tokens (use adaptive)", th)
		}
		if _, has := th["budget_tokens"]; has && modern {
			f.bad("budget_tokens sent to %s", model)
		}
	}
	if oc, ok := b["output_config"].(map[string]any); ok {
		switch oc["effort"] {
		case "low", "medium", "high", "xhigh", "max":
		default:
			f.bad("output_config.effort = %v", oc["effort"])
		}
	}
	if _, ok := b["temperature"]; ok && modern {
		f.bad("temperature sent to %s (non-default sampling is rejected)", model)
	}
	if sys, ok := b["system"]; ok {
		blocks, _ := sys.([]any)
		if len(blocks) == 0 {
			f.bad("system must be a string or a non-empty list of text blocks")
		}
		for _, x := range blocks {
			if m, _ := x.(map[string]any); m["type"] != "text" {
				f.bad("system block %v", x)
			}
		}
	}
	breakpoints := strings.Count(string(raw), `"cache_control"`)
	if breakpoints > 4 {
		f.bad("%d cache_control breakpoints (max 4)", breakpoints)
	}
	if tools, ok := b["tools"].([]any); ok {
		for _, x := range tools {
			t, _ := x.(map[string]any)
			name, _ := t["name"].(string)
			if !pvAnthIDRE.MatchString(name) {
				f.bad("tool name %q", name)
			}
			if s, _ := t["input_schema"].(map[string]any); s["type"] != "object" {
				f.bad("tool %s input_schema %v (must be an object schema)", name, t["input_schema"])
			}
		}
	}
	if tc, ok := b["tool_choice"].(map[string]any); ok && (tc["type"] == "any" || tc["type"] == "tool") && modern {
		f.bad("forced tool_choice %v is rejected by current models", tc)
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) == 0 {
		f.bad("messages empty")
		return
	}
	pending := map[string]bool{} // tool_use ids awaiting their tool_result
	prev := ""
	for i, x := range msgs {
		m, _ := x.(map[string]any)
		role, _ := m["role"].(string)
		if i == 0 && role != "user" {
			f.bad("first message role %q", role)
		}
		if role == prev {
			f.bad("message %d repeats role %s (roles must alternate)", i, role)
		}
		prev = role
		blocks, _ := m["content"].([]any)
		if len(blocks) == 0 {
			f.bad("message %d has no content blocks", i)
		}
		if role == "user" && len(pending) > 0 {
			// the tool_result blocks must come first in the next user message
			for j, bx := range blocks {
				bl, _ := bx.(map[string]any)
				if bl["type"] != "tool_result" {
					if len(pending) > 0 {
						f.bad("message %d: block %d (%v) before all tool_result blocks", i, j, bl["type"])
					}
					break
				}
				delete(pending, fmt.Sprint(bl["tool_use_id"]))
			}
			for id := range pending {
				f.bad("tool_use %s has no tool_result right after it", id)
			}
			pending = map[string]bool{}
		}
		for _, bx := range blocks {
			bl, _ := bx.(map[string]any)
			switch bl["type"] {
			case "text":
				if strings.TrimSpace(fmt.Sprint(bl["text"])) == "" {
					f.bad("message %d: whitespace-only text block", i)
				}
			case "tool_use":
				if role != "assistant" {
					f.bad("tool_use in a %s message", role)
				}
				id := fmt.Sprint(bl["id"])
				if !pvAnthIDRE.MatchString(id) {
					f.bad("tool_use id %q", id)
				}
				if _, ok := bl["input"].(map[string]any); !ok {
					f.bad("tool_use input %v must be an object", bl["input"])
				}
				pending[id] = true
			case "tool_result":
				if role != "user" {
					f.bad("tool_result in a %s message", role)
				}
			case "thinking":
				if bl["signature"] == nil || bl["signature"] == "" {
					f.bad("thinking block without signature")
				}
			case "redacted_thinking", "image":
			default:
				f.bad("message %d: unknown block type %v", i, bl["type"])
			}
		}
	}
	if prev != "user" {
		f.bad("last message is %s: prefill is rejected by current models", prev)
	}
}

func newAnthFake(t *testing.T, events ...[2]string) (*anthFake, *httptest.Server) {
	t.Helper()
	f := &anthFake{events: events}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		before := len(f.problems)
		f.validate(r, raw)
		failed := len(f.problems) > before
		evs := f.events
		f.mu.Unlock()
		if failed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"fake: request violates the Messages API contract"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, ev := range evs {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *anthFake) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.problems {
		t.Error("request:", p)
	}
}

// anthTextStream is a complete spec-shaped answer with a text block and a tool call.
func anthTextStream(text string) [][2]string {
	tj, _ := json.Marshal(text)
	return [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-5-5","stop_reason":null,"usage":{"input_tokens":120,"cache_creation_input_tokens":30,"cache_read_input_tokens":900,"output_tokens":1}}}`},
		{"ping", `{"type":"ping"}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIGx"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":` + string(tj) + `}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01A","name":"read","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

var anthTools = []ToolSpec{
	{Name: "read", Description: "read a file", Params: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}},
	{Name: "bash", Description: "run a command", Params: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
}

// The full exchange against the contract: first request, tool loop replay with
// the signed thinking block, effort as adaptive thinking, temperature dropped,
// and secrets redacted in the body while the x-api-key header stays intact.
func TestAnthropicFakeServerConformance(t *testing.T) {
	rdReset(t, rdOn) // redact as for a remote provider (the fake listens on 127.0.0.1)
	f, srv := newAnthFake(t, anthTextStream("Vou ler.")...)
	p := &AnthropicProvider{Name: "anthropic", BaseURL: srv.URL, Key: anthFakeKey}
	secret := "ghp_" + strings.Repeat("Ab1", 12)
	temp := 0.3
	var streamed strings.Builder
	out, err := p.Chat(context.Background(), ChatRequest{Model: "claude-sonnet-5-5", System: "SYS", Tools: anthTools,
		MaxTokens: 32000, Effort: "high", Temperature: &temp,
		Messages: []Message{{Role: "user", Content: "leia a.go; token " + secret}}},
		func(kind, s string) {
			if kind == "text" {
				streamed.WriteString(s)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	f.check(t)
	if out.Text != "Vou ler." || streamed.String() != "Vou ler." || out.Stop != "tool_use" || len(out.ToolCalls) != 1 ||
		out.ToolCalls[0].ID != "toolu_01A" || out.ToolCalls[0].Args != `{"path":"a.go"}` {
		t.Fatalf("parsed reply %+v", out)
	}
	if out.Usage.In != 1050 || out.Usage.CacheRead != 900 || out.Usage.CacheWrite != 30 || out.Usage.Out != 42 {
		t.Fatalf("usage %+v", out.Usage)
	}
	b := f.bodies[0]
	if th, _ := b["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Fatalf("thinking %v", b["thinking"])
	}
	if oc, _ := b["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config %v", b["output_config"])
	}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret left the machine in the request body")
	}

	// tool loop: the signed thinking block is replayed first, results follow
	hist := []Message{{Role: "user", Content: "leia a.go"},
		{Role: "assistant", Content: out.Text, ToolCalls: out.ToolCalls, Extra: out.Extra},
		{Role: "tool", ToolCallID: "toolu_01A", ToolName: "read", Content: "package main"},
		{Role: "user", Content: "<system-reminder>steer</system-reminder>"}}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "claude-sonnet-5-5", System: "SYS", Tools: anthTools, Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	f.check(t)
	ms := f.bodies[1]["messages"].([]any)
	asst := ms[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "SIGx" {
		t.Fatalf("thinking not replayed first: %v", asst)
	}
	if _, ok := f.bodies[1]["thinking"]; ok {
		t.Fatal("no effort: thinking param must be left to the model default")
	}
}

// Legacy models keep the budget form; ids are classified by family/version.
func TestAnthropicModernModelClassification(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-sonnet-5-5": true, "claude-opus-5-5": true, "claude-fable-5-1": true, "claude-haiku-5-5": true,
		"claude-opus-4-6": true, "claude-sonnet-4-6": true, "claude-opus-4-8": true, "us.anthropic.claude-opus-4-7": true,
		"claude-sonnet-4-5": false, "claude-haiku-4-5": false, "claude-opus-4-1-20250805": false, "claude-sonnet-4-20250514": false,
		"claude-3-7-sonnet-latest": false, "claude-3-5-haiku-20241022": false, "claude": false, "m": false,
	} {
		if got := pvAnthModern(model); got != want {
			t.Errorf("pvAnthModern(%q) = %v, want %v", model, got, want)
		}
	}
	f, srv := newAnthFake(t, anthTextStream("ok")...)
	p := &AnthropicProvider{Name: "anthropic", BaseURL: srv.URL, Key: anthFakeKey}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "claude-sonnet-4-5", MaxTokens: 16000, Effort: "medium",
		Messages: []Message{{Role: "user", Content: "x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	f.check(t)
	if th, _ := f.bodies[0]["thinking"].(map[string]any); th["type"] != "enabled" || th["budget_tokens"] != float64(8192) {
		t.Fatalf("legacy thinking %v", f.bodies[0]["thinking"])
	}
}

// A policy decline (HTTP 200, stop_reason "refusal") reaches the user as
// "o provedor recusou: …" with the provider's reason, not as an empty reply.
func TestAnthropicRefusalSurfaces(t *testing.T) {
	f, srv := newAnthFake(t,
		[2]string{"message_start", `{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"declined by policy"}},"usage":{"output_tokens":0}}`},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	)
	p := &AnthropicProvider{Name: "anthropic", BaseURL: srv.URL, Key: anthFakeKey}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "claude-sonnet-5-5", Messages: []Message{{Role: "user", Content: "x"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.check(t)
	if !out.Refused() || out.Refusal != "cyber: declined by policy" {
		t.Fatalf("refusal not detected: %+v", out)
	}
	msg := friendlyErr(&RefusalError{Ref: "anthropic/claude-sonnet-5-5", Reason: out.Refusal})
	if !strings.HasPrefix(msg, "o provedor recusou: cyber: declined by policy") || !strings.Contains(msg, "/model") {
		t.Fatalf("friendlyErr = %q", msg)
	}
	if (&ChatResponse{Stop: "refusal", ToolCalls: []ToolCall{{ID: "x"}}}).Refused() {
		t.Fatal("a reply with tool calls is not a refusal")
	}
	if !(&ChatResponse{Stop: "content_filter"}).Refused() {
		t.Fatal("OpenAI content_filter is a refusal")
	}
}

// 4xx bodies are shown as the provider's message with the /model hint.
func TestFriendlyErr4xxShowsProviderMessage(t *testing.T) {
	ae := &APIError{Status: 400, Body: `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0: bad block"}}`}
	msg := friendlyErr(ae)
	if !strings.Contains(msg, "o provedor recusou o pedido (HTTP 400: invalid_request_error: messages.0: bad block)") || !strings.Contains(msg, "/model") {
		t.Fatalf("friendlyErr = %q", msg)
	}
	credit := &APIError{Status: 400, Body: `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`}
	if pvClassify(credit) != "credit" || !strings.Contains(friendlyErr(credit), "sem crédito") {
		t.Fatalf("credit balance: %s / %s", pvClassify(credit), friendlyErr(credit))
	}
	if pvErrMessage(`{"error":"plain"}`) != "plain" || pvErrMessage("not json") != "not json" {
		t.Fatal("pvErrMessage shapes")
	}
	var re *RefusalError
	if errors.As(fmt.Errorf("wrap: %w", &RefusalError{Reason: "x"}), &re) == false {
		t.Fatal("RefusalError must unwrap")
	}
}
