package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// AnthropicProvider speaks the native Messages API with prompt caching and
// extended thinking (G22).
type AnthropicProvider struct {
	Name    string
	BaseURL string
	Key     string
	Headers map[string]string
	cfg     *Config
}

// pvAnthExtra is what an Anthropic answer stores in Message.Extra: the thinking
// blocks (with signatures, or redacted data) that must be replayed verbatim.
type pvAnthExtra struct {
	Prov     string           `json:"prov,omitempty"`
	Thinking []map[string]any `json:"thinking,omitempty"`
}

var pvAnthIDRE = lazyRx(`^[a-zA-Z0-9_-]{1,64}$`)

// pvAnthID makes a foreign tool call id acceptable to Anthropic, deterministically.
func pvAnthID(id string) string {
	if pvAnthIDRE.MatchString(id) {
		return id
	}
	s := sha256.Sum256([]byte(id))
	return "toolu_" + hex.EncodeToString(s[:12])
}

func toAnthropicMessages(msgs []Message) []map[string]any {
	return pvAnthMessages(msgs, "", true)
}

// pvAnthMessages converts the history. Thinking blocks are replayed only for
// messages produced by provider prov (or with unknown provenance when prov == "").
func pvAnthMessages(msgs []Message, prov string, thinking bool) []map[string]any {
	var out []map[string]any
	push := func(role string, block map[string]any) {
		if n := len(out); n > 0 && out[n-1]["role"] == role {
			out[n-1]["content"] = append(out[n-1]["content"].([]map[string]any), block)
			return
		}
		out = append(out, map[string]any{"role": role, "content": []map[string]any{block}})
	}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			t := m.Content
			if strings.TrimSpace(t) == "" && len(m.Images) == 0 {
				t = "(empty)"
			}
			for _, im := range m.Images {
				push("user", map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": im.Mime, "data": im.Data}})
			}
			if strings.TrimSpace(t) != "" {
				push("user", map[string]any{"type": "text", "text": t})
			}
		case "assistant":
			emitted := false
			if thinking && len(m.Extra) > 0 {
				var ex pvAnthExtra
				if json.Unmarshal(m.Extra, &ex) == nil && len(ex.Thinking) > 0 && (prov == "" || ex.Prov == "" || ex.Prov == prov) {
					for _, b := range ex.Thinking {
						push("assistant", b)
					}
					emitted = true
				}
			}
			// Anthropic rejects whitespace-only text blocks (bug 65).
			if txt := strings.TrimSpace(m.Content); txt != "" {
				push("assistant", map[string]any{"type": "text", "text": txt})
				emitted = true
			}
			for _, tc := range m.ToolCalls {
				var input any = map[string]any{}
				if json.Valid([]byte(tc.Args)) {
					var v any
					if json.Unmarshal([]byte(tc.Args), &v) == nil {
						if _, ok := v.(map[string]any); ok {
							input = v
						}
					}
				}
				push("assistant", map[string]any{"type": "tool_use", "id": pvAnthID(tc.ID), "name": tc.Name, "input": input})
				emitted = true
			}
			if !emitted {
				push("assistant", map[string]any{"type": "text", "text": "(empty)"})
			}
		case "tool":
			c := m.Content
			if strings.TrimSpace(c) == "" {
				c = "(no output)"
			}
			push("user", map[string]any{"type": "tool_result", "tool_use_id": pvAnthID(m.ToolCallID), "content": c})
		}
	}
	// Cache breakpoint on the last block: the whole conversation prefix becomes cacheable.
	if n := len(out); n > 0 {
		blocks := out[n-1]["content"].([]map[string]any)
		blocks[len(blocks)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	return out
}

// pvThinkingBudget maps an effort hint to a thinking budget below maxTok (0 = off).
func pvThinkingBudget(effort string, maxTok int) int {
	b := map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 24000}[effort]
	if b == 0 {
		return 0
	}
	if b >= maxTok {
		b = maxTok - 1024
	}
	if b < 1024 {
		return 0
	}
	return b
}

// pvAnthLegacyRE: Claude models from before adaptive thinking (3.x, 4.0, 4.1,
// 4.5), which still take thinking.budget_tokens and temperature.
var pvAnthLegacyRE = lazyRx(`^claude-(?:instant|2|3)|^claude-(?:opus|sonnet|haiku)-4(?:-[0-5])?(?:$|[-@]\d{8}|-latest$)`)

// pvAnthModern reports whether model is a current Claude model (4.6 and later):
// adaptive thinking with output_config.effort, no budget_tokens, no temperature.
// Platform prefixes (us.anthropic., anthropic.) are ignored; ids that do not
// name a claude- model keep the legacy request shape.
func pvAnthModern(model string) bool {
	m := strings.ToLower(model)
	i := strings.Index(m, "claude-")
	if i < 0 {
		return false
	}
	return !pvAnthLegacyRE.MatchString(m[i:])
}

// pvAnthEffort maps jazz's effort hint to an output_config.effort level.
func pvAnthEffort(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "medium", "high", "xhigh", "max":
		return effort
	}
	return ""
}

// pvThinkingReplayable: with thinking on, the assistant message that opened the
// current tool loop must start with its own thinking block. When it came from
// another provider (fallback mid-turn) thinking has to stay off for this request.
func pvThinkingReplayable(msgs []Message, prov string) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			if len(m.ToolCalls) == 0 {
				return true
			}
			var ex pvAnthExtra
			return len(m.Extra) > 0 && json.Unmarshal(m.Extra, &ex) == nil && len(ex.Thinking) > 0 && (prov == "" || ex.Prov == "" || ex.Prov == prov)
		default:
			return true
		}
	}
	return true
}

func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest, fn StreamFn) (*ChatResponse, error) {
	req = rdOutbound(req, p.BaseURL) // secrets never leave the machine (redact.go)
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 16000
	}
	modern := pvAnthModern(req.Model)
	budget := 0
	adaptive := ""
	if req.Effort != "" && pvThinkingReplayable(req.Messages, p.Name) {
		if modern {
			adaptive = pvAnthEffort(req.Effort)
		} else {
			budget = pvThinkingBudget(req.Effort, maxTok)
		}
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTok,
		"stream":     true,
		// Current models think by default (Opus/Sonnet 5.5 cannot turn it off), so
		// their signed thinking blocks are always replayed for this provider.
		"messages": pvAnthMessages(req.Messages, p.Name, budget > 0 || modern),
	}
	switch {
	case adaptive != "":
		// Claude 4.6+ rejects budget_tokens (HTTP 400 on 4.7+ and 5.x): adaptive
		// thinking with the effort level is the supported form.
		body["thinking"] = map[string]any{"type": "adaptive"}
		body["output_config"] = map[string]any{"effort": adaptive}
	case budget > 0:
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	case req.Temperature != nil && !modern:
		// thinking requires the default temperature; current models reject
		// non-default sampling parameters altogether
		body["temperature"] = *req.Temperature
	}
	if req.System != "" {
		body["system"] = []map[string]any{{"type": "text", "text": req.System, "cache_control": map[string]any{"type": "ephemeral"}}}
	}
	if len(req.Tools) > 0 && !req.TextTools {
		var ts []map[string]any
		for _, t := range req.Tools {
			ts = append(ts, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Params})
		}
		ts[len(ts)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		body["tools"] = ts
		if req.NoToolUse {
			body["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	h := map[string]string{"x-api-key": p.Key, "anthropic-version": "2023-06-01"}
	for k, v := range p.Headers {
		h[k] = v
	}
	ref := p.Name + "/" + req.Model
	tokens := pvEstTokens(req)
	p.cfg.rlBegin(ref, tokens)
	out, err := p.stream(ctx, h, body, fn)
	var u Usage
	if out != nil {
		u = out.Usage
	}
	p.cfg.rlEnd(ref, tokens, u, err)
	return out, err
}

func (p *AnthropicProvider) stream(ctx context.Context, h map[string]string, body map[string]any, fn StreamFn) (*ChatResponse, error) {
	resp, touch, done, idleCtx, err := postStream(ctx, strings.TrimRight(p.BaseURL, "/")+"/v1/messages", h, body, 120*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	defer resp.Body.Close()

	type block struct {
		kind, id, name string
		buf            strings.Builder
		sig            string
		data           string
	}
	blocks := map[int]*block{}
	var order []int
	out := &ChatResponse{}
	var text, reasoning strings.Builder
	var apiErr error

	sawStop := false
	err = readSSE(resp.Body, touch, func(event, data string) error {
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					CacheRead    int `json:"cache_read_input_tokens"`
					CacheCreate  int `json:"cache_creation_input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Data      string `json:"data"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
				StopDetails *struct {
					Category    string `json:"category"`
					Explanation string `json:"explanation"`
				} `json:"stop_details"`
			} `json:"delta"`
			StopDetails *struct {
				Category    string `json:"category"`
				Explanation string `json:"explanation"`
			} `json:"stop_details"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "message_start":
			u := ev.Message.Usage
			out.Usage.In = u.InputTokens + u.CacheRead + u.CacheCreate
			out.Usage.CacheRead, out.Usage.CacheWrite = u.CacheRead, u.CacheCreate
		case "content_block_start":
			cb := ev.ContentBlock
			b := &block{kind: cb.Type, id: cb.ID, name: cb.Name, sig: cb.Signature, data: cb.Data}
			if cb.Thinking != "" {
				b.buf.WriteString(cb.Thinking)
			}
			blocks[ev.Index] = b
			order = append(order, ev.Index)
		case "content_block_delta":
			b := blocks[ev.Index]
			switch ev.Delta.Type {
			case "text_delta":
				text.WriteString(ev.Delta.Text)
				if fn != nil {
					fn("text", ev.Delta.Text)
				}
			case "thinking_delta":
				reasoning.WriteString(ev.Delta.Thinking)
				if b != nil {
					b.buf.WriteString(ev.Delta.Thinking)
				}
				if fn != nil {
					fn("reasoning", ev.Delta.Thinking)
				}
			case "signature_delta":
				if b != nil {
					b.sig += ev.Delta.Signature
				}
			case "input_json_delta":
				if b != nil {
					b.buf.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			out.Stop = ev.Delta.StopReason
			// a policy decline (HTTP 200, stop_reason "refusal"): keep the
			// provider's category/explanation for the UI (RefusalError)
			for _, d := range []*struct {
				Category    string `json:"category"`
				Explanation string `json:"explanation"`
			}{ev.Delta.StopDetails, ev.StopDetails} {
				if d != nil && out.Refusal == "" {
					out.Refusal = strings.Trim(d.Category+": "+d.Explanation, ": ")
				}
			}
			out.Usage.Out = ev.Usage.OutputTokens
		case "message_stop":
			sawStop = true
		case "error":
			status := 500
			switch ev.Error.Type {
			case "overloaded_error":
				status = 529
			case "rate_limit_error":
				status = 429
			case "invalid_request_error":
				status = 400
			}
			apiErr = &APIError{Status: status, Body: ev.Error.Message}
			return apiErr
		}
		return nil
	})
	// stream error or cancellation: what arrived, partial tool input included
	partial := func() *ChatResponse {
		out.Text, out.Reasoning, out.Partial = text.String(), reasoning.String(), true
		for _, i := range order {
			if b := blocks[i]; b.kind == "tool_use" && b.name != "" {
				out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.id, Name: b.name, Args: b.buf.String()})
			}
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
	if !sawStop && out.Stop == "" {
		out.Stop = "incomplete" // body ended without message_stop/stop_reason: cut upstream
	}
	var thinking []map[string]any
	for _, i := range order {
		b := blocks[i]
		switch b.kind {
		case "tool_use":
			args := b.buf.String()
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.id, Name: b.name, Args: args})
		case "thinking":
			if b.sig != "" {
				thinking = append(thinking, map[string]any{"type": "thinking", "thinking": b.buf.String(), "signature": b.sig})
			}
		case "redacted_thinking":
			thinking = append(thinking, map[string]any{"type": "redacted_thinking", "data": b.data})
		}
	}
	if len(thinking) > 0 {
		out.Extra, _ = json.Marshal(pvAnthExtra{Prov: p.Name, Thinking: thinking})
	}
	return out, nil
}
