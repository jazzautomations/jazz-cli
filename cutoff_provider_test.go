//go:build !windows

package main

// Provider-level fixtures for cutoff.go (the stream helpers live in
// provider_test.go, which does not build on Windows).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Anthropic-style stream: tool_use input cut by stop_reason max_tokens.
func TestProviderAnthropicCutToolUseContinuable(t *testing.T) {
	sol := ccSolution()
	raw := ccCutArgs("forth.py", sol, 300)
	a, b := raw[:len(raw)/2], raw[len(raw)/2:]
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	srv, _ := provSSEServer(t,
		provAnthEv("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(ja)+`}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(jb)+`}}`),
		provAnthEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":1000}}`),
		provAnthEv("message_stop", `{"type":"message_stop"}`),
	)
	out, err := (&AnthropicProvider{BaseURL: srv.URL + "/", Key: "k"}).Chat(context.Background(), ChatRequest{Model: "c", Messages: []Message{{Role: "user", Content: "x"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated() || len(out.ToolCalls) != 1 || out.ToolCalls[0].Args != raw {
		t.Fatalf("stop=%q calls=%+v", out.Stop, out.ToolCalls)
	}
	if cut, ok := ccParse(out.ToolCalls[0]); !ok || !strings.HasPrefix(sol, cut.Value) {
		t.Fatal("anthropic cut call must be continuable")
	}
}

// OpenAI-style stream with the provider error after the partial call.
func TestProviderOpenAIToolCutErrorKeepsPartialCall(t *testing.T) {
	raw := ccCutArgs("forth.py", ccSolution(), 300)
	ja, _ := json.Marshal(raw)
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"write","arguments":`+string(ja)+`}}]}}]}`,
		`{"error":{"code":502,"message":"Upstream error from Poolside: HttpError: HTTP 400: Tool calls cutoff by max_tokens."}}`,
	)
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if !acToolCutErr(err) || out == nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].Args != raw || out.ToolCalls[0].ID != "c1" {
		t.Fatalf("err=%v out=%+v", err, out)
	}
}

// Codex (Responses API): a function call cut by max_output_tokens never gets
// output_item.done; its streamed arguments are kept.
func TestProviderCodexCutFunctionCallKept(t *testing.T) {
	provCodexSetup(t, "tok", 4102444800000)
	raw := ccCutArgs("forth.py", ccSolution(), 300)
	ja, _ := json.Marshal(raw)
	srv, _ := provSSEServer(t,
		provCodexEv("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"write","arguments":""}}`),
		provCodexEv("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","delta":`+string(ja)+`}`),
		provCodexEv("response.incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":4}}}`),
	)
	out, err := (&CodexProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated() || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call_1" || out.ToolCalls[0].Args != raw {
		t.Fatalf("%+v", out)
	}
	// replay: a completed cut call is sent even when output items exist
	ex, _ := json.Marshal(map[string]any{"items": []map[string]any{{"type": "reasoning", "encrypted_content": "e"}}})
	in := pvResponsesInput([]Message{{Role: "assistant", Extra: ex, ToolCalls: []ToolCall{{ID: "call_1", Name: "write", Args: `{"path":"a","content":"b"}`}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"}}, "")
	found := false
	for _, it := range in {
		if it["type"] == "function_call" && it["call_id"] == "call_1" {
			found = true
		}
	}
	if !found {
		t.Fatal("the completed call must be replayed next to the encrypted items")
	}
}
