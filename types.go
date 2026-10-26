package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Message is the provider-agnostic conversation unit.
type ImagePart struct {
	Mime string `json:"mime"`
	Data string `json:"data"` // base64
}

type Message struct {
	Role       string          `json:"role"` // user | assistant | tool
	Content    string          `json:"content,omitempty"`
	Images     []ImagePart     `json:"images,omitempty"`
	Reasoning  string          `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"` // opaque provider data (e.g. encrypted reasoning)
	// PartialCalls: tool calls whose arguments were cut when the reply stream
	// failed (never sent to a provider; resumed by cutoff.go on --resume).
	PartialCalls []ToolCall `json:"partial_calls,omitempty"`
}

type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type ToolSpec struct {
	Name        string
	Description string
	Params      map[string]any
}

type Usage struct {
	In         int     `json:"in"`
	Out        int     `json:"out"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Cost       float64 `json:"cost,omitempty"` // USD when the provider reports it
}

func (u *Usage) Add(o Usage) {
	u.In += o.In
	u.Out += o.Out
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Cost += o.Cost
}

// Truncated reports whether the model's output was cut by the token limit.
func (r *ChatResponse) Truncated() bool {
	switch r.Stop {
	case "length", "max_tokens", "max_output_tokens", "incomplete", acStopToolCut:
		return true
	}
	return false
}

type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Effort      string   // reasoning effort hint: "", low, medium, high
	Temperature *float64 // optional override
	NoToolUse   bool     // offer tools (so history stays valid) but ask the model not to call them
	TextTools   bool     // model has no native function calling: tools described in text
}

type ChatResponse struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     Usage
	Stop      string
	Extra     json.RawMessage
	// Partial: the stream failed or was cancelled; this is what arrived
	// before (tool call arguments may be cut). Returned with the error.
	Partial bool
	// Refusal: the provider's own reason when it declined the request
	// (Anthropic stop_details, OpenAI delta.refusal); may be empty.
	Refusal string
}

// Refused reports a policy decline: Anthropic stop_reason "refusal", OpenAI
// finish_reason "content_filter" or a structured refusal message.
func (r *ChatResponse) Refused() bool {
	if r == nil || len(r.ToolCalls) > 0 {
		return false
	}
	return r.Stop == "refusal" || r.Stop == "content_filter" || r.Refusal != ""
}

// RefusalError ends a turn the provider declined (HTTP 200 with a refusal stop
// reason). It is not retried on another model automatically: the user decides
// whether to rephrase or switch (/model).
type RefusalError struct {
	Ref    string // provider/model that declined
	Reason string // short provider reason, or the model's own text
}

func (e *RefusalError) Error() string {
	r := strings.TrimSpace(e.Reason)
	if r == "" {
		r = "sem motivo informado"
	}
	if len([]rune(r)) > 200 {
		r = string([]rune(r)[:200]) + "…"
	}
	return "o provedor recusou: " + oneLineRefusal(r)
}

func oneLineRefusal(s string) string { return strings.Join(strings.Fields(s), " ") }

// StreamFn receives incremental output. kind is "text" or "reasoning".
type StreamFn func(kind, text string)

type Provider interface {
	Chat(ctx context.Context, req ChatRequest, fn StreamFn) (*ChatResponse, error)
}

// APIError is an HTTP-level failure from a provider.
type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // from Retry-After / X-RateLimit-Reset when present
	Daily      bool          // provider says the daily/free quota is exhausted
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 400 {
		b = b[:400] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, b)
}

// Retryable reports whether trying again (same or another model) can help.
func (e *APIError) Retryable() bool {
	return e.Status == 429 || e.Status == 402 || e.Status == 408 || e.Status >= 500 || e.Status == 0
}

// Limiter lets a rate-limit policy veto or observe requests per model ref (G4).
// Allow reports whether ref may be tried now (and, when not, roughly how long
// until it may); Report is told about every failed request. agent-core consults
// DefaultLimiter in its fallback loop; the providers area may replace it (for
// example from an init function) with a real implementation.
type Limiter interface {
	Allow(ref string) (bool, time.Duration)
	Report(ref string, err error)
}

type noopLimiter struct{}

func (noopLimiter) Allow(string) (bool, time.Duration) { return true, 0 }
func (noopLimiter) Report(string, error)               {}

// DefaultLimiter is the Limiter used by every Agent. nil behaves like a no-op.
var DefaultLimiter Limiter = noopLimiter{}
