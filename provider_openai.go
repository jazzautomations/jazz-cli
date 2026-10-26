package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OpenAIProvider speaks the OpenAI-compatible /chat/completions API
// (OpenRouter, Kilo, OpenAI with API key, Groq, Gemini's compat layer, NVIDIA,
// Mistral, Ollama, llama.cpp…). Per-provider quirks come from ProviderConf
// (Router, Quirks); when those are empty (tests, hand-made configs) they are
// guessed from the base URL.
type OpenAIProvider struct {
	Name    string
	BaseURL string
	Key     string
	Headers map[string]string // extra headers (already expandEnv'd)
	Router  string            // "openrouter" | "kilo" | "" (guess from URL)
	Quirks  []string          // see ProviderConf.Quirks
	Ollama  bool              // send options.num_ctx
	cfg     *Config           // limits, rate limiter, models cache (nil in tests)
}

func (p *OpenAIProvider) router() string {
	if p.Router != "" {
		return p.Router
	}
	switch {
	case strings.Contains(p.BaseURL, "openrouter.ai"):
		return "openrouter"
	case strings.Contains(p.BaseURL, "kilo.ai"):
		return "kilo"
	}
	return ""
}

func (p *OpenAIProvider) quirk(q string) bool {
	for _, x := range p.Quirks {
		if x == q {
			return true
		}
	}
	u := p.BaseURL
	switch q {
	case "deepseek-reasoning":
		return strings.Contains(u, "deepseek.com") || strings.Contains(u, "opencode.ai/zen")
	case "gemini-thought-sig":
		return strings.Contains(u, "generativelanguage.googleapis.com")
	case "mistral-ids9":
		return strings.Contains(u, "mistral.ai")
	case "cohere-effort":
		return strings.Contains(u, "cohere.ai") || strings.Contains(u, "cohere.com")
	case "effort":
		return strings.Contains(u, "api.openai.com") || strings.Contains(u, "groq.com") ||
			strings.Contains(u, "generativelanguage.googleapis.com") || strings.Contains(u, "cerebras.ai")
	}
	return false
}

func (p *OpenAIProvider) headers() map[string]string {
	h := map[string]string{}
	if p.Key != "" {
		h["Authorization"] = "Bearer " + p.Key
	}
	if p.router() != "" {
		h["HTTP-Referer"] = "https://github.com/jazzautomations/jazz-cli"
		h["X-Title"] = "jazz-cli"
	}
	for k, v := range p.Headers {
		h[k] = v
	}
	return h
}

// pvOAOpts selects what the serializer echoes for the current provider.
type pvOAOpts struct {
	prov       string // current provider name: provider-specific Extra is echoed only to its producer
	details    bool   // echo reasoning_details (OpenRouter/Kilo)
	rc         bool   // echo reasoning_content on assistant tool-call messages (DeepSeek)
	ids9       bool   // Mistral: tool call ids must be exactly 9 alphanumerics
	thoughtSig bool   // Gemini: echo tool_calls[].extra_content (thought signatures)
	cache      bool   // OpenRouter → Anthropic/Gemini: cache_control on system + last user message
}

// pvExtra is the provider-specific data stored in Message.Extra.
type pvExtra struct {
	Prov             string                     `json:"prov,omitempty"`
	ReasoningDetails json.RawMessage            `json:"reasoning_details,omitempty"`
	TCExtra          map[string]json.RawMessage `json:"tc_extra,omitempty"` // tool call id -> extra_content
}

func toOpenAIMessages(system string, msgs []Message, echoReasoning bool) []map[string]any {
	return pvOAMessages(system, msgs, pvOAOpts{details: echoReasoning, rc: !echoReasoning, thoughtSig: true})
}

func pvTextOrParts(text string, imgs []ImagePart, cache bool) any {
	if len(imgs) == 0 && !cache {
		return text
	}
	parts := []map[string]any{}
	if text != "" || len(imgs) == 0 {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, im := range imgs {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + im.Mime + ";base64," + im.Data}})
	}
	if cache {
		parts[len(parts)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	return parts
}

func pvOAMessages(system string, msgs []Message, o pvOAOpts) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": pvTextOrParts(system, nil, o.cache)})
	}
	lastUser := -1
	if o.cache {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				lastUser = i
				break
			}
		}
	}
	id := func(s string) string {
		if o.ids9 {
			return pvID9(s)
		}
		return s
	}
	for i, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "content": pvTextOrParts(m.Content, m.Images, i == lastUser)})
		case "assistant":
			am := map[string]any{"role": "assistant"}
			if m.Content != "" {
				am["content"] = m.Content
			} else {
				am["content"] = nil
			}
			var ex pvExtra
			if len(m.Extra) > 0 {
				_ = json.Unmarshal(m.Extra, &ex)
			}
			same := ex.Prov == "" || o.prov == "" || ex.Prov == o.prov
			if len(m.ToolCalls) > 0 {
				var tcs []map[string]any
				for _, tc := range m.ToolCalls {
					args := tc.Args
					if !json.Valid([]byte(args)) {
						args = "{}"
					}
					t := map[string]any{
						"id": id(tc.ID), "type": "function",
						"function": map[string]any{"name": tc.Name, "arguments": args},
					}
					if o.thoughtSig && same {
						if xc, ok := ex.TCExtra[tc.ID]; ok && len(xc) > 0 {
							t["extra_content"] = xc
						}
					}
					tcs = append(tcs, t)
				}
				am["tool_calls"] = tcs
				if o.rc && same && m.Reasoning != "" {
					am["reasoning_content"] = m.Reasoning
				}
			} else if m.Content == "" {
				am["content"] = ""
			}
			if o.details && same && len(ex.ReasoningDetails) > 0 {
				am["reasoning_details"] = ex.ReasoningDetails
			}
			out = append(out, am)
		case "tool":
			c := m.Content
			if c == "" {
				c = "(no output)"
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": id(m.ToolCallID), "name": m.ToolName, "content": c})
		}
	}
	return out
}

var pvID9RE = lazyRx(`^[a-zA-Z0-9]{9}$`)

const pvB62 = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// pvID9 maps any tool call id to 9 alphanumerics (Mistral), deterministically,
// so an assistant tool_call and its tool result still match.
func pvID9(id string) string {
	if pvID9RE.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	n := binary.BigEndian.Uint64(sum[:8]) // 62^9 < 2^64
	var b [9]byte
	for i := range b {
		b[i] = pvB62[n%62]
		n /= 62
	}
	return string(b[:])
}

func toOpenAITools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Params,
		}})
	}
	return out
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string           `json:"content"`
			Refusal          string           `json:"refusal"` // structured refusal (OpenAI)
			Reasoning        string           `json:"reasoning"`
			ReasoningContent string           `json:"reasoning_content"`
			ReasoningDetails []map[string]any `json:"reasoning_details"`
			ToolCalls        []struct {
				Index        *int            `json:"index"`
				ID           string          `json:"id"`
				ExtraContent json.RawMessage `json:"extra_content"`
				Function     struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int      `json:"prompt_tokens"`
		CompletionTokens    int      `json:"completion_tokens"`
		Cost                *float64 `json:"cost"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// pvEstTokens is a cheap estimate of prompt + max output tokens for the limiter.
func pvEstTokens(req ChatRequest) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Content) + 16
		for _, tc := range m.ToolCalls {
			n += len(tc.Args) + len(tc.Name) + 16
		}
		for _, im := range m.Images {
			_ = im
			n += 3000 // ~850 tokens per image
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + 200
	}
	return n*10/35 + req.MaxTokens
}

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest, fn StreamFn) (*ChatResponse, error) {
	req = rdOutbound(req, p.BaseURL) // secrets never leave the machine (redact.go)
	router := p.router()
	model := req.Model
	if p.quirk("gemini-thought-sig") {
		model = strings.TrimPrefix(model, "models/")
	}
	ref := p.Name + "/" + model
	cacheCtl := router == "openrouter" && (strings.HasPrefix(model, "anthropic/") || strings.HasPrefix(model, "google/gemini"))
	opts := pvOAOpts{prov: p.Name, details: router != "", rc: p.quirk("deepseek-reasoning") || strings.HasPrefix(strings.ToLower(model), "deepseek"),
		ids9: p.quirk("mistral-ids9"), thoughtSig: p.quirk("gemini-thought-sig"), cache: cacheCtl}
	if router != "" {
		opts.rc = false // routers carry reasoning in reasoning_details
	}
	body := map[string]any{
		"model":          model,
		"messages":       pvOAMessages(req.System, req.Messages, opts),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(req.Tools) > 0 && !req.TextTools {
		body["tools"] = toOpenAITools(req.Tools)
		if req.NoToolUse {
			body["tool_choice"] = "none"
		}
		if router == "openrouter" { // never get routed to a provider that silently drops tools
			body["provider"] = map[string]any{"require_parameters": true}
		}
	}
	if router != "" {
		body["usage"] = map[string]any{"include": true} // usage.cost (G21)
	}
	isOpenAI := strings.Contains(p.BaseURL, "api.openai.com")
	if req.MaxTokens > 0 {
		if isOpenAI {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.Effort != "" {
		switch {
		case router != "":
			body["reasoning"] = map[string]any{"effort": req.Effort}
		case p.quirk("cohere-effort"):
			if req.Effort != "low" { // the compat API only knows none|high
				body["reasoning_effort"] = "high"
			}
		case p.quirk("effort"):
			body["reasoning_effort"] = req.Effort
		}
	}
	if p.Ollama && p.cfg != nil {
		if win, _ := p.cfg.Limits(ref); win > 0 {
			body["options"] = map[string]any{"num_ctx": win}
		}
	}

	tokens := pvEstTokens(req)
	p.cfg.rlBegin(ref, tokens)
	out, hdrErr := p.chatStream(ctx, body, fn)
	var u Usage
	if out != nil {
		u = out.Usage
	}
	p.cfg.rlEnd(ref, tokens, u, hdrErr)
	if hdrErr == nil && p.cfg != nil && router == "openrouter" && pvFreeID(model) {
		go func() {
			defer func() { _ = recover() }()
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pvRefreshORQuota(c, p.cfg)
		}()
	}
	return out, hdrErr
}

func (p *OpenAIProvider) chatStream(ctx context.Context, body map[string]any, fn StreamFn) (*ChatResponse, error) {
	resp, touch, done, idleCtx, err := postStream(ctx, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", p.headers(), body, 120*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	defer resp.Body.Close()
	if p.cfg != nil {
		model, _ := body["model"].(string)
		p.cfg.rlObserve(p.Name+"/"+model, p.BaseURL, resp.Header)
	}

	type acc struct {
		id, name string
		args     strings.Builder
		extra    json.RawMessage
	}
	var accs []*acc
	byIndex := map[int]*acc{}
	out := &ChatResponse{}
	var text, reasoning strings.Builder
	var details []map[string]any
	think := &thinkSplitter{}
	emit := func(kind, s string) {
		if s == "" {
			return
		}
		if kind == "reasoning" {
			reasoning.WriteString(s)
		} else {
			text.WriteString(s)
		}
		if fn != nil {
			fn(kind, s)
		}
	}

	var apiErr error
	events := 0
	sawDone := false
	err = readSSE(resp.Body, touch, func(_, data string) error {
		events++
		if data == "[DONE]" {
			sawDone = true
			return nil
		}
		var c oaChunk
		if json.Unmarshal([]byte(data), &c) != nil {
			return nil // tolerate junk lines
		}
		if c.Error != nil {
			code := pvCodeInt(c.Error.Code)
			if s, ok := c.Error.Code.(string); ok && code == 0 && strings.Contains(s, "rate_limit") {
				code = 429
			}
			ae := &APIError{Status: code, Body: c.Error.Message}
			ae.Daily = pvIsDaily(code, c.Error.Message)
			pvMarkQuota(ae, time.Now())
			apiErr = ae
			return apiErr
		}
		if c.Usage != nil {
			out.Usage.In = c.Usage.PromptTokens
			out.Usage.Out = c.Usage.CompletionTokens
			if c.Usage.PromptTokensDetails != nil {
				out.Usage.CacheRead = c.Usage.PromptTokensDetails.CachedTokens
			}
			if c.Usage.Cost != nil {
				out.Usage.Cost = *c.Usage.Cost
			}
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			emit("reasoning", d.Reasoning)
			emit("reasoning", d.ReasoningContent)
			details = mergeReasoningDetails(details, d.ReasoningDetails)
			if d.Content != "" {
				think.feed(d.Content, emit)
			}
			for i, tc := range d.ToolCalls {
				var a *acc
				if tc.Index != nil {
					a = byIndex[*tc.Index]
					// Some servers (Ollama, Gemini compat) send each complete parallel
					// call with the same index and a new id: that is a new call (bug 19).
					if a == nil || (tc.ID != "" && a.id != "" && a.id != tc.ID) {
						a = &acc{}
						byIndex[*tc.Index] = a
						accs = append(accs, a)
					}
				} else if tc.ID != "" || len(accs) == 0 || (i > 0) {
					a = &acc{}
					accs = append(accs, a)
				} else {
					a = accs[len(accs)-1]
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function.Name != "" {
					a.name += tc.Function.Name
				}
				if len(tc.ExtraContent) > 0 && string(tc.ExtraContent) != "null" {
					a.extra = tc.ExtraContent
				}
				a.args.WriteString(tc.Function.Arguments)
			}
			if ch.Delta.Refusal != "" && len(out.Refusal) < 2000 {
				out.Refusal += ch.Delta.Refusal
			}
			if ch.FinishReason != "" {
				out.Stop = ch.FinishReason
			}
		}
		return nil
	})
	think.flush(emit)
	// Any stream error or cancellation: hand back what arrived (text, reasoning,
	// partial tool calls), marked Partial, with the error, so the loop can
	// persist it and continue a cut write on --resume (cutoff.go).
	partial := func() *ChatResponse {
		out.Text, out.Reasoning, out.Partial = text.String(), reasoning.String(), true
		for i, a := range accs {
			if a.name != "" {
				out.ToolCalls = append(out.ToolCalls, ToolCall{ID: pvOACallID(a.id, i), Name: strings.TrimSpace(a.name), Args: a.args.String()})
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
	if events == 0 {
		// 200 with an empty body: never report it as a successful empty answer (bug 66)
		return nil, &APIError{Status: 502, Body: "resposta vazia do servidor (nenhum evento SSE)"}
	}
	if !sawDone && out.Stop == "" {
		// the body ended cleanly but without finish_reason or [DONE]: a proxy
		// or gateway dropped the upstream mid-answer. "incomplete" sends it
		// down the truncation path (tool calls not executed, text continued).
		out.Stop = "incomplete"
	}
	out.Text, out.Reasoning = text.String(), reasoning.String()
	ex := pvExtra{}
	if len(details) > 0 {
		ex.ReasoningDetails, _ = json.Marshal(details)
	}
	for i, a := range accs {
		if a.name == "" {
			continue
		}
		id := pvOACallID(a.id, i)
		if len(a.extra) > 0 {
			if ex.TCExtra == nil {
				ex.TCExtra = map[string]json.RawMessage{}
			}
			ex.TCExtra[id] = a.extra
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: strings.TrimSpace(a.name), Args: a.args.String()})
	}
	rcProv := out.Reasoning != "" && len(out.ToolCalls) > 0 && p.quirk("deepseek-reasoning") && p.Name != ""
	if len(ex.ReasoningDetails) > 0 || len(ex.TCExtra) > 0 || rcProv {
		ex.Prov = p.Name
		out.Extra, _ = json.Marshal(ex)
	}
	return out, nil
}

// pvOACallID is the call id, generated when the server sent none.
func pvOACallID(id string, i int) string {
	if id == "" {
		id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1e9, i)
	}
	return id
}

// thinkSplitter routes <think>…</think> spans found inside content to "reasoning".
// Works across chunk boundaries.
type thinkSplitter struct {
	in  bool
	buf string
}

func (t *thinkSplitter) feed(s string, emit func(kind, s string)) {
	t.buf += s
	for {
		tag := "<think>"
		if t.in {
			tag = "</think>"
		}
		i := strings.Index(t.buf, tag)
		if i >= 0 {
			kind := "text"
			if t.in {
				kind = "reasoning"
			}
			emit(kind, t.buf[:i])
			t.buf = t.buf[i+len(tag):]
			t.in = !t.in
			continue
		}
		// keep a possible partial tag at the end
		keep := 0
		for k := len(tag) - 1; k > 0; k-- {
			if strings.HasSuffix(t.buf, tag[:k]) {
				keep = k
				break
			}
		}
		kind := "text"
		if t.in {
			kind = "reasoning"
		}
		emit(kind, t.buf[:len(t.buf)-keep])
		t.buf = t.buf[len(t.buf)-keep:]
		return
	}
}

func (t *thinkSplitter) flush(emit func(kind, s string)) {
	if t.buf != "" {
		kind := "text"
		if t.in {
			kind = "reasoning"
		}
		emit(kind, t.buf)
		t.buf = ""
	}
}

// mergeReasoningDetails accumulates streamed reasoning_details fragments in order,
// joining consecutive fragments of the same block (same type+index).
func mergeReasoningDetails(acc []map[string]any, in []map[string]any) []map[string]any {
	for _, d := range in {
		if n := len(acc); n > 0 {
			last := acc[n-1]
			if last["type"] == d["type"] && fmt.Sprint(last["index"]) == fmt.Sprint(d["index"]) {
				for k, v := range d {
					if sv, ok := v.(string); ok && (k == "text" || k == "summary" || k == "data") {
						prev, _ := last[k].(string)
						last[k] = prev + sv
					} else if v != nil {
						last[k] = v
					}
				}
				continue
			}
		}
		cp := make(map[string]any, len(d))
		for k, v := range d {
			cp[k] = v
		}
		acc = append(acc, cp)
	}
	return acc
}
