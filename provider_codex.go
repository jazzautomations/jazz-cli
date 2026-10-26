package main

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"
)

// CodexProvider talks to the ChatGPT-subscription Responses endpoint
// (chatgpt.com/backend-api/codex/responses) with "Sign in with ChatGPT" tokens.
type CodexProvider struct {
	Name      string
	BaseURL   string
	SessionID string
}

func toResponsesInput(msgs []Message) []map[string]any { return pvResponsesInput(msgs, "") }

// pvResponsesInput converts the history; encrypted items are replayed only to
// the provider that produced them (prov == "" accepts any).
func pvResponsesInput(msgs []Message, prov string) []map[string]any {
	var in []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "user":
			parts := []map[string]any{{"type": "input_text", "text": m.Content}}
			for _, im := range m.Images {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + im.Mime + ";base64," + im.Data})
			}
			in = append(in, map[string]any{"type": "message", "role": "user", "content": parts})
		case "assistant":
			// Replay the exact output items we got (reasoning with encrypted_content,
			// messages, function calls) when we have them.
			var ex struct {
				Prov  string           `json:"prov"`
				Items []map[string]any `json:"items"`
			}
			if len(m.Extra) > 0 && json.Unmarshal(m.Extra, &ex) == nil && len(ex.Items) > 0 && (prov == "" || ex.Prov == "" || ex.Prov == prov) {
				seen := map[string]bool{}
				for _, it := range ex.Items {
					delete(it, "id")
					delete(it, "status")
					if id, _ := it["call_id"].(string); id != "" {
						seen[id] = true
					}
					in = append(in, it)
				}
				for _, tc := range m.ToolCalls { // a call cut at the limit (cutoff.go): never orphan its result
					if !seen[tc.ID] {
						args := tc.Args
						if !json.Valid([]byte(args)) {
							args = "{}"
						}
						in = append(in, map[string]any{"type": "function_call", "name": tc.Name, "arguments": args, "call_id": tc.ID})
					}
				}
				continue
			}
			if m.Content != "" {
				in = append(in, map[string]any{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": m.Content}}})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if !json.Valid([]byte(args)) {
					args = "{}"
				}
				in = append(in, map[string]any{"type": "function_call", "name": tc.Name, "arguments": args, "call_id": tc.ID})
			}
		case "tool":
			c := m.Content
			if c == "" {
				c = "(no output)"
			}
			in = append(in, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": c})
		}
	}
	return in
}

func (p *CodexProvider) Chat(ctx context.Context, req ChatRequest, fn StreamFn) (*ChatResponse, error) {
	req = rdOutbound(req, "") // secrets never leave the machine (redact.go)
	resp, err := p.chatOnce(ctx, req, fn, false)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 401 {
		return p.chatOnce(ctx, req, fn, true) // refresh once and retry
	}
	return resp, err
}

func (p *CodexProvider) chatOnce(ctx context.Context, req ChatRequest, fn StreamFn, forceRefresh bool) (*ChatResponse, error) {
	cr, err := chatgptCred(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	var tools []map[string]any
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "strict": false, "parameters": t.Params})
	}
	effort := req.Effort
	if effort == "" {
		effort = "medium"
	}
	body := map[string]any{
		"model":               req.Model,
		"instructions":        req.System,
		"input":               pvResponsesInput(req.Messages, p.Name),
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
		"reasoning":           map[string]any{"effort": effort, "summary": "auto"},
		"store":               false,
		"stream":              true,
		"include":             []string{"reasoning.encrypted_content"},
	}
	if len(tools) > 0 && !req.TextTools {
		body["tools"] = tools
		if req.NoToolUse {
			body["tool_choice"] = "none"
		}
	}
	if p.SessionID != "" {
		body["prompt_cache_key"] = p.SessionID
	}
	h := map[string]string{
		"Authorization":      "Bearer " + cr.Access,
		"ChatGPT-Account-ID": cr.AccountID,
		"originator":         originator,
		"version":            version,
		"User-Agent":         originator + "/" + version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
	}
	if p.SessionID != "" {
		h["session-id"] = p.SessionID
	}
	resp, touch, done, idleCtx, err := postStream(ctx, strings.TrimRight(p.BaseURL, "/")+"/responses", h, body, 150*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	defer resp.Body.Close()

	out := &ChatResponse{}
	var text, reasoning strings.Builder
	var items []map[string]any
	var apiErr error
	var pending *cxPending // function call started but not done
	err = readSSE(resp.Body, touch, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Item     json.RawMessage `json:"item"`
			Response struct {
				Usage struct {
					InputTokens        int `json:"input_tokens"`
					OutputTokens       int `json:"output_tokens"`
					InputTokensDetails struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
			if fn != nil {
				fn("text", ev.Delta)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(ev.Delta)
			if fn != nil {
				fn("reasoning", ev.Delta)
			}
		case "response.reasoning_summary_part.done":
			reasoning.WriteString("\n\n")
		case "response.output_item.added":
			// a function call starts: its arguments stream as deltas; kept so a
			// call cut by max_output_tokens can be continued (cutoff.go)
			var it struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				pending = &cxPending{item: it.ID, call: it.CallID, name: it.Name}
			}
		case "response.function_call_arguments.delta":
			if pending != nil {
				pending.args.WriteString(ev.Delta)
			}
		case "response.output_item.done":
			pending = nil
			var it map[string]any
			if json.Unmarshal(ev.Item, &it) == nil {
				items = append(items, it)
				if it["type"] == "function_call" {
					name, _ := it["name"].(string)
					args, _ := it["arguments"].(string)
					id, _ := it["call_id"].(string)
					out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: name, Args: args})
				}
			}
		case "response.completed", "response.incomplete":
			u := ev.Response.Usage
			out.Usage = Usage{In: u.InputTokens, Out: u.OutputTokens, CacheRead: u.InputTokensDetails.CachedTokens}
			out.Stop = "stop"
			if ev.Type == "response.incomplete" {
				out.Stop = "incomplete" // G1: Truncated() must see it even without a reason
			}
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason != "" {
				out.Stop = ev.Response.IncompleteDetails.Reason
			}
		case "response.failed":
			msg, code := "response failed", ""
			if ev.Response.Error != nil {
				msg, code = ev.Response.Error.Message, ev.Response.Error.Code
			}
			st := 500
			if strings.Contains(code, "rate") || strings.Contains(code, "quota") || strings.Contains(code, "usage") {
				st = 429
			}
			if strings.Contains(code, "context") {
				st = 400
			}
			apiErr = &APIError{Status: st, Body: code + ": " + msg}
			return apiErr
		case "error":
			apiErr = &APIError{Status: 500, Body: ev.Code + ": " + ev.Message}
			return apiErr
		}
		return nil
	})
	partial := func() *ChatResponse { // stream error or cancellation: what arrived
		out.Text, out.Reasoning, out.Partial = text.String(), reasoning.String(), true
		if pending != nil && pending.name != "" {
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: pending.call, Name: pending.name, Args: pending.args.String()})
		}
		return out
	}
	if apiErr != nil {
		if r := partial(); r.Text != "" || r.Reasoning != "" || len(r.ToolCalls) > 0 {
			return r, apiErr
		}
		return nil, apiErr
	}
	if err != nil {
		return partial(), streamErr(ctx, idleCtx, err)
	}
	out.Text, out.Reasoning = text.String(), reasoning.String()
	if pending != nil && pending.name != "" && out.Truncated() {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: pending.call, Name: pending.name, Args: pending.args.String()})
	}
	if len(items) > 0 {
		ex := map[string]any{"items": items}
		if p.Name != "" {
			ex["prov"] = p.Name
		}
		out.Extra, _ = json.Marshal(ex)
	}
	return out, nil
}

// cxPending is a function call whose arguments are still streaming.
type cxPending struct {
	item, call, name string
	args             strings.Builder
}
