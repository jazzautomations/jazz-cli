package main

// Shared test harness (fake OpenAI-compatible server, recording UI, agent
// harness). Platform-neutral so the Windows test run can use it too.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake OpenAI-compatible server
// ---------------------------------------------------------------------------

type fakeCall struct {
	ID   string
	Name string
	Args string
}

// fakeResp is one scripted reply. Status != 0 && != 200 => HTTP error with Body.
type fakeResp struct {
	Status         int
	Body           string
	Text           string
	Reasoning      string
	Calls          []fakeCall
	HangAfter      bool // stream Text, then block until the client goes away
	Stall          bool // send only keep-alive comments until the client goes away
	UsageIn        int
	UsageOut       int
	Finish         string // overrides finish_reason (e.g. "length")
	ErrChunk       string // after Text, stream an SSE error event with this message and stop
	ErrAfterCalls  string // after the Calls, stream an SSE error event with this message and stop
	ErrCode        int    // code of the ErrAfterCalls event (0 = 502)
	HangAfterCalls bool   // after the Calls, block until the client goes away
}

type recReq struct {
	Model    string
	Messages []map[string]any
	Tools    []any
	HasTools bool
	Raw      map[string]any
}

func (r recReq) system() string {
	if len(r.Messages) > 0 && r.Messages[0]["role"] == "system" {
		s, _ := r.Messages[0]["content"].(string)
		return s
	}
	return ""
}

func (r recReq) last() map[string]any {
	if len(r.Messages) == 0 {
		return nil
	}
	return r.Messages[len(r.Messages)-1]
}

func (r recReq) isCompact() bool {
	l := r.last()
	if l == nil {
		return false
	}
	c, _ := l["content"].(string)
	return l["role"] == "user" && strings.Contains(c, "handoff summary")
}

// toolResults returns tool messages (by tool_call_id) in the request.
func (r recReq) toolResults() map[string]string {
	out := map[string]string{}
	for _, m := range r.Messages {
		if m["role"] == "tool" {
			id, _ := m["tool_call_id"].(string)
			c, _ := m["content"].(string)
			out[id] = c
		}
	}
	return out
}

func (r recReq) contains(role, substr string) bool {
	for _, m := range r.Messages {
		if m["role"] != role {
			continue
		}
		if c, _ := m["content"].(string); strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func (r recReq) toolNames() []string {
	var n []string
	for _, t := range r.Tools {
		tm, _ := t.(map[string]any)
		f, _ := tm["function"].(map[string]any)
		if s, ok := f["name"].(string); ok {
			n = append(n, s)
		}
	}
	return n
}

type fakeServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recReq
	handler func(n int, r recReq) fakeResp
}

func newFakeServer(t *testing.T, h func(n int, r recReq) fakeResp) *fakeServer {
	t.Helper()
	fs := &fakeServer{t: t, handler: h}
	fs.srv = httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(func() {
		fs.srv.Close()
		fs.assertPairedRequests(t)
	})
	return fs
}

// seq returns the scripted replies in order, then a plain final answer.
func seq(resps ...fakeResp) func(int, recReq) fakeResp {
	return func(n int, _ recReq) fakeResp {
		if n < len(resps) {
			return resps[n]
		}
		return fakeResp{Text: "(end of script)"}
	}
}

func (fs *fakeServer) URL() string { return fs.srv.URL }

func (fs *fakeServer) requests() []recReq {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]recReq(nil), fs.reqs...)
}

func (fs *fakeServer) countModel(m string) int {
	n := 0
	for _, r := range fs.requests() {
		if r.Model == m {
			n++
		}
	}
	return n
}

func (fs *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	rr := recReq{Raw: raw}
	rr.Model, _ = raw["model"].(string)
	if ms, ok := raw["messages"].([]any); ok {
		for _, m := range ms {
			mm, _ := m.(map[string]any)
			rr.Messages = append(rr.Messages, mm)
		}
	}
	if ts, ok := raw["tools"].([]any); ok {
		rr.Tools = ts
		rr.HasTools = len(ts) > 0
	}
	fs.mu.Lock()
	n := len(fs.reqs)
	fs.reqs = append(fs.reqs, rr)
	fs.mu.Unlock()

	resp := fs.handler(n, rr)
	if resp.Status != 0 && resp.Status != 200 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		fmt.Fprint(w, resp.Body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	delta := func(d map[string]any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": d}}}
	}
	if resp.Stall {
		for {
			fmt.Fprint(w, ": keep-alive\n\n")
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	if resp.Reasoning != "" {
		send(delta(map[string]any{"reasoning_content": resp.Reasoning}))
	}
	if resp.Text != "" {
		// split text in two chunks to exercise streaming accumulation
		h := len(resp.Text) / 2
		send(delta(map[string]any{"content": resp.Text[:h]}))
		send(delta(map[string]any{"content": resp.Text[h:]}))
	}
	if resp.HangAfter {
		<-r.Context().Done()
		return
	}
	if resp.ErrChunk != "" {
		send(map[string]any{"error": map[string]any{"message": resp.ErrChunk, "code": 502}})
		return
	}
	for i, c := range resp.Calls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", n, i)
		}
		args := c.Args
		h := len(args) / 2
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": args[:h]}}}}))
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": i,
			"function": map[string]any{"arguments": args[h:]}}}}))
	}
	if resp.ErrAfterCalls != "" {
		code := resp.ErrCode
		if code == 0 {
			code = 502
		}
		send(map[string]any{"error": map[string]any{"message": resp.ErrAfterCalls, "code": code}})
		return
	}
	if resp.HangAfterCalls {
		<-r.Context().Done()
		return
	}
	finish := "stop"
	if len(resp.Calls) > 0 {
		finish = "tool_calls"
	}
	if resp.Finish != "" {
		finish = resp.Finish
	}
	send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
	if resp.UsageIn > 0 || resp.UsageOut > 0 {
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": resp.UsageIn, "completion_tokens": resp.UsageOut}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// assertPairedRequests: every assistant tool_call in every recorded request is
// immediately followed by matching tool results, and no tool result is orphaned.
func (fs *fakeServer) assertPairedRequests(t *testing.T) {
	t.Helper()
	for ri, r := range fs.requests() {
		msgs := r.Messages
		for i := 0; i < len(msgs); i++ {
			m := msgs[i]
			if m["role"] == "tool" {
				t.Errorf("request %d: orphan tool message at %d (id=%v)", ri, i, m["tool_call_id"])
				continue
			}
			if m["role"] != "assistant" {
				continue
			}
			tcs, _ := m["tool_calls"].([]any)
			if len(tcs) == 0 {
				continue
			}
			want := map[string]bool{}
			for _, tc := range tcs {
				tm, _ := tc.(map[string]any)
				id, _ := tm["id"].(string)
				want[id] = true
			}
			j := i + 1
			for ; j < len(msgs) && msgs[j]["role"] == "tool"; j++ {
				id, _ := msgs[j]["tool_call_id"].(string)
				if !want[id] {
					t.Errorf("request %d: tool result %q at %d does not match preceding assistant calls", ri, id, j)
				}
				delete(want, id)
			}
			if len(want) > 0 {
				t.Errorf("request %d: assistant message %d has tool calls without results: %v", ri, i, want)
			}
			i = j - 1
		}
	}
}

// assertPairedMsgs checks the same invariant on the agent's in-memory history.
func assertPairedMsgs(t *testing.T, msgs []Message) {
	t.Helper()
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == "tool" {
			t.Errorf("history: orphan tool message at %d (%s)", i, m.ToolCallID)
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, tc := range m.ToolCalls {
			want[tc.ID] = true
		}
		j := i + 1
		for ; j < len(msgs) && msgs[j].Role == "tool"; j++ {
			if !want[msgs[j].ToolCallID] {
				t.Errorf("history: unexpected tool result %q at %d", msgs[j].ToolCallID, j)
			}
			delete(want, msgs[j].ToolCallID)
		}
		if len(want) > 0 {
			t.Errorf("history: assistant %d has calls without results: %v", i, want)
		}
		i = j - 1
	}
}

// ---------------------------------------------------------------------------
// Recording UI
// ---------------------------------------------------------------------------

type approvalRec struct{ Tool, Detail, Reason string }

type recUI struct {
	mu          sync.Mutex
	text        strings.Builder
	reasoning   strings.Builder
	notices     []string
	warns       []string
	errs        []string
	approvals   []approvalRec
	answer      Approval
	feedback    string // returned with Deny ([e] in the real UI)
	toolStarts  []string
	toolEnds    []ToolOutput
	turns       []TurnStats
	onToolStart func(name, detail string)
	onText      func(s string)
}

func (u *recUI) StreamText(s string) {
	u.mu.Lock()
	u.text.WriteString(s)
	f := u.onText
	u.mu.Unlock()
	if f != nil {
		f(s)
	}
}
func (u *recUI) StreamReasoning(s string) { u.mu.Lock(); u.reasoning.WriteString(s); u.mu.Unlock() }
func (u *recUI) Thinking(string)          {}
func (u *recUI) StopThinking()            {}
func (u *recUI) ToolStart(id, name, detail string) {
	u.mu.Lock()
	u.toolStarts = append(u.toolStarts, name+" "+detail)
	f := u.onToolStart
	u.mu.Unlock()
	if f != nil {
		f(name, detail)
	}
}
func (u *recUI) ToolEnd(id, name string, out ToolOutput, d time.Duration) {
	u.mu.Lock()
	u.toolEnds = append(u.toolEnds, out)
	u.mu.Unlock()
}
func (u *recUI) Notice(m string) { u.mu.Lock(); u.notices = append(u.notices, m); u.mu.Unlock() }
func (u *recUI) Warn(m string)   { u.mu.Lock(); u.warns = append(u.warns, m); u.mu.Unlock() }
func (u *recUI) Error(m string)  { u.mu.Lock(); u.errs = append(u.errs, m); u.mu.Unlock() }
func (u *recUI) Approve(ctx context.Context, tool, detail, reason string) (Approval, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.approvals = append(u.approvals, approvalRec{tool, detail, reason})
	if u.answer == Deny {
		return Deny, u.feedback
	}
	return u.answer, ""
}
func (u *recUI) TurnEnd(st TurnStats) { u.mu.Lock(); u.turns = append(u.turns, st); u.mu.Unlock() }
func (u *recUI) SetMode(string)       {}
func (u *recUI) Jobs(int)             {}
func (u *recUI) Bell()                {}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Agent harness
// ---------------------------------------------------------------------------

type harness struct {
	ag   *Agent
	ui   *recUI
	fs   *fakeServer
	root string
	cfg  *Config
}

func newHarness(t *testing.T, h func(int, recReq) fakeResp, mod func(*Config)) *harness {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	fs := newFakeServer(t, h)
	root := t.TempDir()
	cfg := &Config{
		Model:         "fake/m1",
		Mode:          "auto",
		MaxSteps:      20,
		ContextWindow: 128000,
		MaxTokens:     1000,
		Providers: map[string]*ProviderConf{
			"fake": {Type: "openai", BaseURL: fs.URL(), NoKey: true},
		},
	}
	if mod != nil {
		mod(cfg)
	}
	ui := &recUI{answer: Deny}
	tb := NewToolbox(root)
	t.Cleanup(tb.Shell.Close)
	ag := NewAgent(cfg, ui, tb, nil, cfg.Model)
	hs := &harness{ag: ag, ui: ui, fs: fs, root: root, cfg: cfg}
	t.Cleanup(func() {
		assertPairedMsgs(t, ag.Msgs)
		ag.sess.Close()
	})
	return hs
}

func (h *harness) run(t *testing.T, input string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := h.ag.Run(ctx, input)
	return err
}

func jsonArgs(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func lastMsg(msgs []Message) Message {
	if len(msgs) == 0 {
		return Message{}
	}
	return msgs[len(msgs)-1]
}

// pvTestHome isolates config/data dirs and clears every preset key env var.
func pvTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OLLAMA_HOST", "")
	for _, pc := range defaultProviders() {
		if pc.KeyEnv != "" {
			t.Setenv(pc.KeyEnv, "")
		}
		if pc.AccountEnv != "" {
			t.Setenv(pc.AccountEnv, "")
		}
		for _, alt := range pvAltKeyEnv[pc.KeyEnv] {
			t.Setenv(alt, "")
		}
	}
	pvReachMu.Lock()
	pvReachC = map[string]pvReach{}
	pvReachMu.Unlock()
	return home
}

// pvTestCfg is a loaded config (presets merged) in an isolated home.
func pvTestCfg(t *testing.T) *Config {
	t.Helper()
	pvTestHome(t)
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func pfTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("HOME", t.TempDir()) // no ~/.claude/settings.json from the developer machine
	return home
}
