//go:build !windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// provCapture records what a fake server received.
type provCapture struct {
	mu      sync.Mutex
	path    string
	headers http.Header
	body    map[string]any
	raw     []byte
	count   int
}

func (c *provCapture) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = r.URL.Path
	c.headers = r.Header.Clone()
	c.raw = b
	c.body = nil
	_ = json.Unmarshal(b, &c.body)
	c.count++
}

// provWriteSSE writes events as a real SSE server would, flushing after each.
// An entry that starts with "event:" or ":" or "data:" is written verbatim
// (followed by a blank line); anything else is wrapped as "data: <entry>".
func provWriteSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	for _, e := range events {
		if strings.HasPrefix(e, "event:") || strings.HasPrefix(e, ":") || strings.HasPrefix(e, "data:") {
			io.WriteString(w, e+"\n\n")
		} else {
			io.WriteString(w, "data: "+e+"\n\n")
		}
		if fl != nil {
			fl.Flush()
		}
	}
}

// provSSEServer serves the given SSE events on any path and captures the request.
func provSSEServer(t *testing.T, events ...string) (*httptest.Server, *provCapture) {
	t.Helper()
	cap := &provCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		provWriteSSE(w, events...)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// provAnthEv builds an Anthropic-style "event: X\ndata: {...}" frame.
func provAnthEv(typ, data string) string {
	return "event: " + typ + "\ndata: " + data
}

func provJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

type provStreamRec struct {
	mu    sync.Mutex
	parts []string
}

func (r *provStreamRec) fn(kind, s string) {
	r.mu.Lock()
	r.parts = append(r.parts, kind+":"+s)
	r.mu.Unlock()
}

func (r *provStreamRec) joined(kind string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, p := range r.parts {
		if strings.HasPrefix(p, kind+":") {
			b.WriteString(strings.TrimPrefix(p, kind+":"))
		}
	}
	return b.String()
}

func provAPIErr(t *testing.T, err error) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	return ae
}

func provMsgs(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	arr, ok := body[key].([]any)
	if !ok {
		t.Fatalf("body[%q] is %T, want array; body=%v", key, body[key], body)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, a := range arr {
		m, ok := a.(map[string]any)
		if !ok {
			t.Fatalf("element of %q is %T", key, a)
		}
		out = append(out, m)
	}
	return out
}

// provRewriteRT redirects requests for given hosts to a local test server.
type provRewriteRT struct {
	hosts  map[string]*url.URL
	base   http.RoundTripper
	mu     sync.Mutex
	counts map[string]int
}

func (rt *provRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if to, ok := rt.hosts[req.URL.Host]; ok {
		rt.mu.Lock()
		rt.counts[req.URL.Host]++
		rt.mu.Unlock()
		r2 := req.Clone(req.Context())
		u := *req.URL
		u.Scheme, u.Host = to.Scheme, to.Host
		r2.URL = &u
		r2.Host = to.Host
		return rt.base.RoundTrip(r2)
	}
	return rt.base.RoundTrip(req)
}

// provHijackHosts swaps the shared httpClient for one that sends the given
// hosts to local servers (no real network), restoring it after the test.
func provHijackHosts(t *testing.T, hosts map[string]string) *provRewriteRT {
	t.Helper()
	rt := &provRewriteRT{hosts: map[string]*url.URL{}, base: http.DefaultTransport, counts: map[string]int{}}
	for h, target := range hosts {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		rt.hosts[h] = u
	}
	old := httpClient
	httpClient = &http.Client{Transport: rt}
	t.Cleanup(func() { httpClient = old })
	return rt
}

var provTools = []ToolSpec{
	{Name: "read", Description: "read a file", Params: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
	{Name: "bash", Description: "run a command", Params: map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
}

// ---------------------------------------------------------------------------
// httpx.go
// ---------------------------------------------------------------------------

func TestProviderReadSSE(t *testing.T) {
	type ev struct{ event, data string }
	cases := []struct {
		name string
		in   string
		want []ev
	}{
		{"simple", "data: a\n\ndata: b\n\n", []ev{{"", "a"}, {"", "b"}}},
		{"event name", "event: ping\ndata: {}\n\nevent: x\ndata: 1\n\n", []ev{{"ping", "{}"}, {"x", "1"}}},
		{"multi-line data joined with newline", "data: l1\ndata: l2\n\n", []ev{{"", "l1\nl2"}}},
		{"crlf", "data: a\r\n\r\ndata: b\r\n\r\n", []ev{{"", "a"}, {"", "b"}}},
		{"comments ignored", ": keep-alive\n\n: OPENROUTER PROCESSING\ndata: z\n\n", []ev{{"", "z"}}},
		{"no space after colon", "data:{\"a\":1}\n\n", []ev{{"", `{"a":1}`}}},
		{"only one leading space stripped", "data:   x\n\n", []ev{{"", "  x"}}},
		{"final event without trailing blank line", "data: a\n\ndata: last", []ev{{"", "a"}, {"", "last"}}},
		{"event without data is dropped and reset", "event: lonely\n\ndata: d\n\n", []ev{{"", "d"}}},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []ev
			touched := 0
			err := readSSE(strings.NewReader(c.in), func(bool) { touched++ }, func(e, d string) error {
				got = append(got, ev{e, d})
				return nil
			})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v want %#v", got, c.want)
			}
			if c.in != "" && touched == 0 {
				t.Fatal("touch never called")
			}
		})
	}
}

func TestProviderReadSSECallbackErrorStops(t *testing.T) {
	stop := errors.New("stop")
	n := 0
	err := readSSE(strings.NewReader("data: 1\n\ndata: 2\n\ndata: 3\n\n"), func(bool) {}, func(_, d string) error {
		n++
		if d == "2" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

type provErrReader struct {
	data string
	done bool
}

func (r *provErrReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestProviderReadSSEReaderError(t *testing.T) {
	var got []string
	err := readSSE(&provErrReader{data: "data: a\n\ndata: partial"}, func(bool) {}, func(_, d string) error {
		got = append(got, d)
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("got %v", got)
	}
}

func TestProviderPostStreamHeadersAndBody(t *testing.T) {
	srv, cap := provSSEServer(t, "hello")
	resp, touch, done, idleCtx, err := postStream(context.Background(), srv.URL+"/x", map[string]string{"X-Custom": "v", "Authorization": "Bearer k"}, map[string]any{"a": 1}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	defer resp.Body.Close()
	if touch == nil || idleCtx == nil {
		t.Fatal("nil touch/idleCtx")
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "data: hello\n\n" {
		t.Fatalf("body %q", b)
	}
	h := cap.headers
	checks := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"User-Agent":    userAgent,
		"X-Custom":      "v",
		"Authorization": "Bearer k",
	}
	for k, v := range checks {
		if h.Get(k) != v {
			t.Errorf("header %s=%q want %q", k, h.Get(k), v)
		}
	}
	if cap.body["a"] != float64(1) {
		t.Errorf("body %s", cap.raw)
	}
}

func TestProviderPostStreamUserAgentOverride(t *testing.T) {
	srv, cap := provSSEServer(t, "x")
	resp, _, done, _, err := postStream(context.Background(), srv.URL, map[string]string{"User-Agent": "custom/1"}, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	done()
	if cap.headers.Get("User-Agent") != "custom/1" {
		t.Fatalf("UA %q", cap.headers.Get("User-Agent"))
	}
}

func TestProviderPostStreamNon200(t *testing.T) {
	for _, st := range []int{400, 401, 404, 429, 500, 529} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(st)
				io.WriteString(w, "  {\"error\":\"nope\"}\n ")
			}))
			defer srv.Close()
			resp, _, _, _, err := postStream(context.Background(), srv.URL, nil, map[string]any{}, time.Second)
			if resp != nil {
				t.Fatal("resp should be nil")
			}
			ae := provAPIErr(t, err)
			if ae.Status != st || ae.Body != `{"error":"nope"}` {
				t.Fatalf("got %+v", ae)
			}
		})
	}
}

func TestProviderPostStreamMarshalError(t *testing.T) {
	_, _, _, _, err := postStream(context.Background(), "http://127.0.0.1:1", nil, map[string]any{"bad": make(chan int)}, time.Second)
	if err == nil {
		t.Fatal("want marshal error")
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Fatalf("marshal error should not be APIError: %v", err)
	}
}

func TestProviderPostStreamConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	_, _, _, _, err := postStream(context.Background(), addr, nil, map[string]any{}, time.Second)
	ae := provAPIErr(t, err)
	if ae.Status != 0 || !ae.Retryable() {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderStreamIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select { // go silent
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx := context.Background()
	resp, touch, done, idleCtx, err := postStream(ctx, srv.URL, nil, map[string]any{}, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	defer resp.Body.Close()
	var got []string
	start := time.Now()
	rerr := readSSE(resp.Body, touch, func(_, d string) error { got = append(got, d); return nil })
	if rerr == nil {
		t.Fatal("expected read error after idle")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("idle took too long: %v", el)
	}
	if !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("got %v", got)
	}
	if e := streamErr(ctx, idleCtx, rerr); !errors.Is(e, errStreamIdle) {
		t.Fatalf("streamErr=%v want errStreamIdle", e)
	}
}

func TestProviderStreamKeepAliveResetsIdle(t *testing.T) {
	// Comments arriving more often than idle must keep the stream alive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
			time.Sleep(60 * time.Millisecond)
		}
		io.WriteString(w, "data: end\n\n")
	}))
	defer srv.Close()
	resp, touch, done, _, err := postStream(context.Background(), srv.URL, nil, map[string]any{}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	defer resp.Body.Close()
	var got []string
	if err := readSSE(resp.Body, touch, func(_, d string) error { got = append(got, d); return nil }); err != nil {
		t.Fatalf("stream died: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"end"}) {
		t.Fatalf("got %v", got)
	}
}

func TestProviderStreamErr(t *testing.T) {
	if streamErr(context.Background(), nil, nil) != nil {
		t.Fatal("nil err must map to nil")
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := streamErr(cctx, cctx, io.EOF); !errors.Is(e, context.Canceled) {
		t.Fatalf("parent cancelled: %v", e)
	}
	if e := streamErr(context.Background(), cctx, io.EOF); !errors.Is(e, errStreamIdle) {
		t.Fatalf("idle: %v", e)
	}
	e := streamErr(context.Background(), context.Background(), io.ErrUnexpectedEOF)
	ae := provAPIErr(t, e)
	if ae.Status != 0 || !strings.Contains(ae.Body, "stream interrompido") {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("X-K") != "v" || r.Header.Get("User-Agent") != userAgent {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"a":1}`)
		case "/bad":
			io.WriteString(w, `not json`)
		default:
			w.WriteHeader(503)
			io.WriteString(w, "down")
		}
	}))
	defer srv.Close()
	var out struct{ A int }
	if err := getJSON(context.Background(), srv.URL+"/ok", map[string]string{"X-K": "v"}, &out); err != nil || out.A != 1 {
		t.Fatalf("ok: %v %+v", err, out)
	}
	if err := getJSON(context.Background(), srv.URL+"/bad", nil, &out); err == nil || !strings.Contains(err.Error(), "resposta inválida") {
		t.Fatalf("bad: %v", err)
	}
	ae := provAPIErr(t, getJSON(context.Background(), srv.URL+"/x", nil, &out))
	if ae.Status != 503 || ae.Body != "down" {
		t.Fatalf("got %+v", ae)
	}
}

// ---------------------------------------------------------------------------
// provider_openai.go
// ---------------------------------------------------------------------------

func TestProviderOpenAITextDeltas(t *testing.T) {
	srv, cap := provSSEServer(t,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo, "},"finish_reason":null}]}`,
		`: keepalive`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":null}]}`,
		`garbage that is not json`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":64}}}`,
		`[DONE]`,
	)
	p := &OpenAIProvider{Name: "x", BaseURL: srv.URL + "/v1/", Key: "sk-test"}
	rec := &provStreamRec{}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, rec.fn)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Hello, world" || out.Reasoning != "" {
		t.Fatalf("text %q reasoning %q", out.Text, out.Reasoning)
	}
	if rec.joined("text") != "Hello, world" {
		t.Fatalf("streamed %v", rec.parts)
	}
	if out.Stop != "stop" {
		t.Fatalf("stop %q", out.Stop)
	}
	if out.Usage != (Usage{In: 100, Out: 20, CacheRead: 64}) {
		t.Fatalf("usage %+v", out.Usage)
	}
	if len(out.ToolCalls) != 0 || out.Extra != nil {
		t.Fatalf("unexpected tool calls/extra: %+v %s", out.ToolCalls, out.Extra)
	}
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("path %q (trailing slash not trimmed?)", cap.path)
	}
	if cap.headers.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth %q", cap.headers.Get("Authorization"))
	}
	if cap.body["stream"] != true {
		t.Fatal("stream not true")
	}
	so, _ := cap.body["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("stream_options %v", cap.body["stream_options"])
	}
}

func TestProviderOpenAINilStreamFn(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"reasoning":"r","content":"<think>x</think>t"}}]}`,
		`[DONE]`)
	p := &OpenAIProvider{BaseURL: srv.URL}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "t" || out.Reasoning != "rx" {
		t.Fatalf("%q %q", out.Text, out.Reasoning)
	}
}

func TestProviderOpenAIToolCallsByIndex(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"re","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"ad","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"cmd\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"ls -la\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	p := &OpenAIProvider{BaseURL: srv.URL}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "m", Tools: provTools}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{
		{ID: "call_a", Name: "read", Args: `{"path":"a.go"}`},
		{ID: "call_b", Name: "bash", Args: `{"cmd":"ls -la"}`},
	}
	if !reflect.DeepEqual(out.ToolCalls, want) {
		t.Fatalf("got %+v", out.ToolCalls)
	}
	if out.Stop != "tool_calls" {
		t.Fatalf("stop %q", out.Stop)
	}
}

func TestProviderOpenAIToolCallsParallelInOneChunk(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"read","arguments":"{\"path\":\"a\"}"}},{"index":1,"id":"c1","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]}}]}`,
		`[DONE]`)
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 2 || out.ToolCalls[0].Args != `{"path":"a"}` || out.ToolCalls[1].ID != "c1" || out.ToolCalls[1].Args != `{"path":"b"}` {
		t.Fatalf("got %+v", out.ToolCalls)
	}
}

func TestProviderOpenAIToolCallsWithoutIndex(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"th\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"c2","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"c3","function":{"name":"read","arguments":"{}"}},{"function":{"name":"bash","arguments":"{\"cmd\":\"x\"}"}}]}}]}`,
		`[DONE]`)
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 4 {
		t.Fatalf("want 4 calls, got %+v", out.ToolCalls)
	}
	if out.ToolCalls[0] != (ToolCall{ID: "c1", Name: "read", Args: `{"path":1}`}) {
		t.Fatalf("call0 %+v", out.ToolCalls[0])
	}
	if out.ToolCalls[1] != (ToolCall{ID: "c2", Name: "bash", Args: `{}`}) {
		t.Fatalf("call1 %+v", out.ToolCalls[1])
	}
	if out.ToolCalls[2].ID != "c3" || out.ToolCalls[3].Name != "bash" || out.ToolCalls[3].Args != `{"cmd":"x"}` {
		t.Fatalf("calls 2/3 %+v", out.ToolCalls[2:])
	}
	if out.ToolCalls[3].ID == "" || out.ToolCalls[3].ID == "c3" {
		t.Fatalf("second id-less call in chunk needs its own generated id: %q", out.ToolCalls[3].ID)
	}
}

func TestProviderOpenAIToolCallMissingIDGenerated(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":" read ","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"bash","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"","function":{"name":"","arguments":"{}"}}]}}]}`,
		`[DONE]`)
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 2 {
		t.Fatalf("nameless call must be dropped; got %+v", out.ToolCalls)
	}
	a, b := out.ToolCalls[0], out.ToolCalls[1]
	if a.Name != "read" {
		t.Fatalf("name not trimmed: %q", a.Name)
	}
	if !strings.HasPrefix(a.ID, "call_") || !strings.HasPrefix(b.ID, "call_") || a.ID == b.ID {
		t.Fatalf("generated ids %q %q", a.ID, b.ID)
	}
}

// Some OpenAI-compatible servers (e.g. Ollama, Gemini compat) stream each parallel
// call as a complete object, reusing index 0 with distinct ids.
func TestProviderOpenAIToolCallsSameIndexDistinctIDs(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`[DONE]`)
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{{ID: "c1", Name: "read", Args: `{"path":"a"}`}, {ID: "c2", Name: "bash", Args: `{"cmd":"ls"}`}}
	if !reflect.DeepEqual(out.ToolCalls, want) {
		t.Fatalf("got %+v", out.ToolCalls)
	}
}

func TestProviderOpenAIReasoningDeltas(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"reasoning":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"deep "}}]}`,
		`{"choices":[{"delta":{"reasoning":"more"}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`[DONE]`)
	rec := &provStreamRec{}
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, rec.fn)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reasoning != "think deep more" || out.Text != "answer" {
		t.Fatalf("reasoning %q text %q", out.Reasoning, out.Text)
	}
	want := []string{"reasoning:think ", "reasoning:deep ", "reasoning:more", "text:answer"}
	if !reflect.DeepEqual(rec.parts, want) {
		t.Fatalf("stream %v", rec.parts)
	}
}

func TestProviderOpenAIThinkTagsAcrossChunks(t *testing.T) {
	cases := []struct {
		name         string
		chunks       []string
		text, reason string
	}{
		{"split tags", []string{"Hello <thi", "nk>pl", "an more</th", "ink> answer"}, "Hello  answer", "plan more"},
		{"tag char by char", strings.Split("<think>r</think>t", ""), "t", "r"},
		{"unclosed think flushed as reasoning", []string{"<think>still", " thinking"}, "", "still thinking"},
		{"lone < is text", []string{"a <", " b"}, "a < b", ""},
		{"trailing partial tag flushed as text", []string{"x <thi"}, "x <thi", ""},
		{"two think blocks", []string{"<think>a</think>b<think>c</think>d"}, "bd", "ac"},
		{"stray close tag is text", []string{"a</think>b"}, "a</think>b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ev []string
			for _, ch := range c.chunks {
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": ch}}}})
				ev = append(ev, string(b))
			}
			ev = append(ev, "[DONE]")
			srv, _ := provSSEServer(t, ev...)
			rec := &provStreamRec{}
			out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, rec.fn)
			if err != nil {
				t.Fatal(err)
			}
			if out.Text != c.text || out.Reasoning != c.reason {
				t.Fatalf("text %q reasoning %q", out.Text, out.Reasoning)
			}
			if rec.joined("text") != c.text || rec.joined("reasoning") != c.reason {
				t.Fatalf("streamed %v", rec.parts)
			}
			for _, p := range rec.parts {
				if strings.Contains(p, "<think>") || (c.name != "stray close tag is text" && strings.Contains(p, "</think>")) {
					t.Fatalf("tag leaked into stream: %v", rec.parts)
				}
			}
		})
	}
}

func TestProviderOpenAIMidStreamError(t *testing.T) {
	cases := []struct {
		name   string
		chunk  string
		status int
		body   string
	}{
		{"numeric code", `{"error":{"code":429,"message":"rate limited upstream"}}`, 429, "rate limited upstream"},
		{"string numeric code", `{"error":{"code":"502","message":"bad gateway"}}`, 502, "bad gateway"},
		{"non-numeric code", `{"error":{"code":"server_error","message":"boom"}}`, 0, "boom"},
		{"no code", `{"error":{"message":"x"}}`, 0, "x"},
		{"openrouter shape with choices", `{"id":"g","error":{"code":500,"message":"Provider disconnected"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`, 500, "Provider disconnected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := provSSEServer(t,
				`{"choices":[{"delta":{"content":"partial"}}]}`,
				c.chunk,
				`{"choices":[{"delta":{"content":"never seen"}}]}`,
				`[DONE]`)
			rec := &provStreamRec{}
			out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, rec.fn)
			if out == nil || !out.Partial || out.Text != "partial" {
				t.Fatalf("API error mid-stream: the partial reply comes back marked: %+v", out)
			}
			ae := provAPIErr(t, err)
			if ae.Status != c.status || ae.Body != c.body {
				t.Fatalf("got %+v", ae)
			}
			if strings.Contains(rec.joined("text"), "never seen") {
				t.Fatal("stream continued after error")
			}
		})
	}
}

func TestProviderOpenAINon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"No endpoints found that support tool use"}}`)
	}))
	defer srv.Close()
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if out != nil {
		t.Fatal("out must be nil")
	}
	ae := provAPIErr(t, err)
	if ae.Status != 404 || !strings.Contains(ae.Body, "No endpoints found") {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderOpenAICancelKeepsPartial(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial text\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(ctx, ChatRequest{Model: "m"}, func(kind, s string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if out == nil || out.Text != "partial text" {
		t.Fatalf("partial lost: %+v", out)
	}
}

func provOpenAIHistory() []Message {
	return []Message{
		{Role: "user", Content: "list files"},
		{Role: "assistant", Content: "", Reasoning: "need ls",
			ToolCalls: []ToolCall{{ID: "call_1", Name: "bash", Args: `{"cmd":"ls"}`}, {ID: "call_2", Name: "read", Args: `{broken`}},
			Extra:     json.RawMessage(`{"reasoning_details":[{"type":"reasoning.encrypted","data":"ENC","index":0}]}`)},
		{Role: "tool", ToolCallID: "call_1", ToolName: "bash", Content: "a.go\n"},
		{Role: "tool", ToolCallID: "call_2", ToolName: "read", Content: ""},
		{Role: "assistant", Content: "done"},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "thanks"},
	}
}

func TestProviderOpenAIRequestBody(t *testing.T) {
	cases := []struct {
		name          string
		basePath      string
		tools         bool
		wantProvider  bool
		wantMaxKey    string
		wantEffortKey string
		wantDetails   bool
		wantReferer   bool
	}{
		{"openrouter with tools", "/openrouter.ai/api/v1", true, true, "max_tokens", "reasoning", true, true},
		{"openrouter no tools", "/openrouter.ai/api/v1", false, false, "max_tokens", "reasoning", true, true},
		{"openai", "/api.openai.com/v1", true, false, "max_completion_tokens", "reasoning_effort", false, false},
		{"generic", "/v1", true, false, "max_tokens", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, cap := provSSEServer(t, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
			req := ChatRequest{Model: "mod", System: "SYS", Messages: provOpenAIHistory(), MaxTokens: 1234, Effort: "high"}
			if c.tools {
				req.Tools = provTools
			}
			if _, err := (&OpenAIProvider{BaseURL: srv.URL + c.basePath, Key: "k"}).Chat(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
			body := cap.body
			if body["model"] != "mod" {
				t.Errorf("model %v", body["model"])
			}
			// tools
			if c.tools {
				ts := provMsgs(t, body, "tools")
				if len(ts) != 2 || ts[0]["type"] != "function" {
					t.Fatalf("tools %v", ts)
				}
				fnm := ts[0]["function"].(map[string]any)
				if fnm["name"] != "read" || fnm["description"] != "read a file" || fnm["parameters"] == nil {
					t.Fatalf("tool fn %v", fnm)
				}
			} else if _, ok := body["tools"]; ok {
				t.Error("tools present without tools")
			}
			// provider.require_parameters
			pv, hasProv := body["provider"].(map[string]any)
			if hasProv != c.wantProvider || (hasProv && pv["require_parameters"] != true) {
				t.Errorf("provider=%v want present=%v", body["provider"], c.wantProvider)
			}
			// max tokens
			for _, k := range []string{"max_tokens", "max_completion_tokens"} {
				_, has := body[k]
				if has != (k == c.wantMaxKey) {
					t.Errorf("%s present=%v", k, has)
				}
			}
			if body[c.wantMaxKey] != float64(1234) {
				t.Errorf("%s=%v", c.wantMaxKey, body[c.wantMaxKey])
			}
			// effort
			switch c.wantEffortKey {
			case "reasoning":
				if r, _ := body["reasoning"].(map[string]any); r["effort"] != "high" {
					t.Errorf("reasoning %v", body["reasoning"])
				}
			case "reasoning_effort":
				if body["reasoning_effort"] != "high" {
					t.Errorf("reasoning_effort %v", body["reasoning_effort"])
				}
			default:
				if _, ok := body["reasoning"]; ok {
					t.Error("unexpected reasoning")
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Error("unexpected reasoning_effort")
				}
			}
			// headers
			if (cap.headers.Get("HTTP-Referer") != "") != c.wantReferer || (cap.headers.Get("X-Title") == "jazz-cli") != c.wantReferer {
				t.Errorf("referer headers: %v", cap.headers)
			}
			// messages
			ms := provMsgs(t, body, "messages")
			if len(ms) != 8 {
				t.Fatalf("messages %d: %s", len(ms), cap.raw)
			}
			if ms[0]["role"] != "system" || ms[0]["content"] != "SYS" {
				t.Errorf("system %v", ms[0])
			}
			if ms[1]["role"] != "user" || ms[1]["content"] != "list files" {
				t.Errorf("user %v", ms[1])
			}
			as := ms[2]
			if c, ok := as["content"]; !ok || c != nil {
				t.Errorf("assistant with tool_calls must have content null, got %v (present=%v)", c, ok)
			}
			tcs := as["tool_calls"].([]any)
			if len(tcs) != 2 {
				t.Fatalf("tool_calls %v", tcs)
			}
			tc0 := tcs[0].(map[string]any)
			f0 := tc0["function"].(map[string]any)
			if tc0["id"] != "call_1" || tc0["type"] != "function" || f0["name"] != "bash" || f0["arguments"] != `{"cmd":"ls"}` {
				t.Errorf("tc0 %v", tc0)
			}
			if f1 := tcs[1].(map[string]any)["function"].(map[string]any); f1["arguments"] != "{}" {
				t.Errorf("invalid args must become {}: %v", f1["arguments"])
			}
			_, hasDetails := as["reasoning_details"]
			if hasDetails != c.wantDetails {
				t.Errorf("reasoning_details present=%v want %v", hasDetails, c.wantDetails)
			}
			if c.wantDetails {
				d := as["reasoning_details"].([]any)[0].(map[string]any)
				if d["data"] != "ENC" || d["type"] != "reasoning.encrypted" {
					t.Errorf("details not echoed intact: %v", d)
				}
			}
			for i, want := range []struct{ id, name, content string }{{"call_1", "bash", "a.go\n"}, {"call_2", "read", "(no output)"}} {
				tm := ms[3+i]
				if tm["role"] != "tool" || tm["tool_call_id"] != want.id || tm["content"] != want.content {
					t.Errorf("tool msg %d: %v", i, tm)
				}
			}
			if ms[5]["role"] != "assistant" || ms[5]["content"] != "done" {
				t.Errorf("assistant text %v", ms[5])
			}
			if _, ok := ms[5]["tool_calls"]; ok {
				t.Error("plain assistant must not carry tool_calls")
			}
			if c, ok := ms[6]["content"]; !ok || c != "" {
				t.Errorf("empty assistant w/o tools must have content \"\": %v", ms[6])
			}
		})
	}
}

func TestProviderOpenAINoKeyNoAuthHeader(t *testing.T) {
	srv, cap := provSSEServer(t, `[DONE]`)
	if _, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if cap.headers.Get("Authorization") != "" {
		t.Fatalf("auth header with empty key: %q", cap.headers.Get("Authorization"))
	}
	if _, ok := cap.body["max_tokens"]; ok {
		t.Error("max_tokens sent with MaxTokens=0")
	}
	if _, ok := cap.body["tools"]; ok {
		t.Error("tools sent without tools")
	}
}

func TestProviderOpenAIToMessagesNoSystem(t *testing.T) {
	ms := toOpenAIMessages("", []Message{{Role: "user", Content: "x"}}, false)
	if len(ms) != 1 || ms[0]["role"] != "user" {
		t.Fatalf("%v", ms)
	}
}

// DESIGN.md: "DeepSeek: reasoning_content devolvido em todo assistant quando há tools (senão HTTP 400)".
func TestProviderOpenAIEchoesReasoningContent(t *testing.T) {
	ms := toOpenAIMessages("", []Message{
		{Role: "user", Content: "x"},
		{Role: "assistant", Reasoning: "plan", ToolCalls: []ToolCall{{ID: "c", Name: "bash", Args: "{}"}}},
		{Role: "tool", ToolCallID: "c", Content: "ok"},
	}, false)
	if ms[1]["reasoning_content"] != "plan" {
		t.Fatalf("assistant reasoning_content missing: %v", ms[1])
	}
}

func TestProviderOpenAIReasoningDetailsMerged(t *testing.T) {
	srv, _ := provSSEServer(t,
		`{"choices":[{"delta":{"reasoning":"a","reasoning_details":[{"type":"reasoning.text","text":"a","index":0,"format":"x"}]}}]}`,
		`{"choices":[{"delta":{"reasoning":"b","reasoning_details":[{"type":"reasoning.text","text":"b","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"E1","index":1}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"E2","index":1,"signature":"S"}]}}]}`,
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`[DONE]`)
	out, err := (&OpenAIProvider{BaseURL: srv.URL + "/openrouter.ai/api/v1"}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ex struct {
		ReasoningDetails []map[string]any `json:"reasoning_details"`
	}
	if err := json.Unmarshal(out.Extra, &ex); err != nil {
		t.Fatalf("extra %s: %v", out.Extra, err)
	}
	d := ex.ReasoningDetails
	if len(d) != 2 || d[0]["text"] != "ab" || d[0]["format"] != "x" || d[1]["data"] != "E1E2" || d[1]["signature"] != "S" {
		t.Fatalf("details %v", d)
	}
	// round trip: echoed back on next openrouter request
	ms := toOpenAIMessages("", []Message{{Role: "assistant", Content: out.Text, Extra: out.Extra}}, true)
	b, _ := json.Marshal(ms[0]["reasoning_details"])
	if !strings.Contains(string(b), "E1E2") {
		t.Fatalf("not echoed: %s", b)
	}
}

func TestProviderMergeReasoningDetails(t *testing.T) {
	acc := mergeReasoningDetails(nil, []map[string]any{{"type": "reasoning.summary", "summary": "x", "index": 0}})
	acc = mergeReasoningDetails(acc, []map[string]any{{"type": "reasoning.summary", "summary": "y", "index": 0}, {"type": "reasoning.text", "text": "t", "index": 1}})
	acc = mergeReasoningDetails(acc, []map[string]any{{"type": "reasoning.text", "text": "u", "index": 1, "id": nil}})
	if len(acc) != 2 || acc[0]["summary"] != "xy" || acc[1]["text"] != "tu" {
		t.Fatalf("%v", acc)
	}
	// input slices must not be aliased
	in := []map[string]any{{"type": "a", "text": "1", "index": 5}}
	acc2 := mergeReasoningDetails(nil, in)
	mergeReasoningDetails(acc2, []map[string]any{{"type": "a", "text": "2", "index": 5}})
	if in[0]["text"] != "1" {
		t.Fatal("input mutated")
	}
}

// ---------------------------------------------------------------------------
// provider_anthropic.go
// ---------------------------------------------------------------------------

func TestProviderAnthropicStream(t *testing.T) {
	srv, cap := provSSEServer(t,
		provAnthEv("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-x","stop_reason":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":30,"cache_read_input_tokens":200,"output_tokens":1}}}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm "}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ok"}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`),
		provAnthEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
		provAnthEv("ping", `{"type": "ping"}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me "}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"check."}}`),
		provAnthEv("content_block_stop", `{"type":"content_block_stop","index":1}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"th\": \"x.go\"}"}}`),
		provAnthEv("content_block_stop", `{"type":"content_block_stop","index":2}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"ls","input":{}}}`),
		provAnthEv("content_block_stop", `{"type":"content_block_stop","index":3}`),
		provAnthEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`),
		provAnthEv("message_stop", `{"type":"message_stop"}`),
	)
	rec := &provStreamRec{}
	p := &AnthropicProvider{BaseURL: srv.URL + "/", Key: "ak"}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "claude-x", Messages: []Message{{Role: "user", Content: "hi"}}}, rec.fn)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Let me check." || out.Reasoning != "hmm ok" {
		t.Fatalf("text %q reasoning %q", out.Text, out.Reasoning)
	}
	want := []string{"reasoning:hmm ", "reasoning:ok", "text:Let me ", "text:check."}
	if !reflect.DeepEqual(rec.parts, want) {
		t.Fatalf("stream %v", rec.parts)
	}
	wantCalls := []ToolCall{{ID: "toolu_1", Name: "read", Args: `{"path": "x.go"}`}, {ID: "toolu_2", Name: "ls", Args: "{}"}}
	if !reflect.DeepEqual(out.ToolCalls, wantCalls) {
		t.Fatalf("calls %+v", out.ToolCalls)
	}
	if out.Stop != "tool_use" {
		t.Fatalf("stop %q", out.Stop)
	}
	if out.Usage != (Usage{In: 240, Out: 42, CacheRead: 200, CacheWrite: 30}) {
		t.Fatalf("usage %+v", out.Usage)
	}
	if cap.path != "/v1/messages" {
		t.Fatalf("path %q", cap.path)
	}
	if cap.headers.Get("x-api-key") != "ak" || cap.headers.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("headers %v", cap.headers)
	}
	if cap.headers.Get("Authorization") != "" {
		t.Fatal("Anthropic must not send a bearer header")
	}
}

func TestProviderAnthropicErrorEvent(t *testing.T) {
	cases := []struct {
		typ    string
		status int
	}{
		{"overloaded_error", 529},
		{"rate_limit_error", 429},
		{"invalid_request_error", 400},
		{"api_error", 500},
		{"", 500},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			srv, _ := provSSEServer(t,
				provAnthEv("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`),
				provAnthEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
				provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`),
				provAnthEv("error", `{"type":"error","error":{"type":"`+c.typ+`","message":"Overloaded"}}`),
				provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"NO"}}`),
			)
			rec := &provStreamRec{}
			out, err := (&AnthropicProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, rec.fn)
			if out != nil && !out.Partial { // what streamed before the error comes back marked partial
				t.Fatalf("out %+v", out)
			}
			ae := provAPIErr(t, err)
			if ae.Status != c.status || ae.Body != "Overloaded" {
				t.Fatalf("got %+v", ae)
			}
			if strings.Contains(rec.joined("text"), "NO") {
				t.Fatal("kept reading after error")
			}
		})
	}
}

func TestProviderAnthropicNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer srv.Close()
	_, err := (&AnthropicProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	ae := provAPIErr(t, err)
	if ae.Status != 529 || !ae.Retryable() {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderAnthropicRequestBody(t *testing.T) {
	srv, cap := provSSEServer(t, provAnthEv("message_stop", `{"type":"message_stop"}`))
	req := ChatRequest{
		Model:  "claude-x",
		System: "SYS",
		Tools:  provTools,
		Messages: []Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", Content: "ok", ToolCalls: []ToolCall{{ID: "t1", Name: "bash", Args: `{"cmd":"ls"}`}, {ID: "t2", Name: "read", Args: `{bad`}}},
			{Role: "tool", ToolCallID: "t1", Content: "out1"},
			{Role: "tool", ToolCallID: "t2", Content: ""},
		},
	}
	if _, err := (&AnthropicProvider{BaseURL: srv.URL, Key: "k"}).Chat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	b := cap.body
	if b["model"] != "claude-x" || b["stream"] != true || b["max_tokens"] != float64(16000) {
		t.Fatalf("basics %s", cap.raw)
	}
	sys := provMsgs(t, b, "system")
	if len(sys) != 1 || sys[0]["type"] != "text" || sys[0]["text"] != "SYS" {
		t.Fatalf("system %v", sys)
	}
	if cc, _ := sys[0]["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("system cache_control %v", sys[0])
	}
	tools := provMsgs(t, b, "tools")
	if len(tools) != 2 || tools[0]["name"] != "read" || tools[0]["input_schema"] == nil || tools[0]["description"] != "read a file" {
		t.Fatalf("tools %v", tools)
	}
	if _, ok := tools[0]["cache_control"]; ok {
		t.Fatal("only the last tool gets cache_control")
	}
	if cc, _ := tools[1]["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last tool cache_control %v", tools[1])
	}
	ms := provMsgs(t, b, "messages")
	if len(ms) != 3 {
		t.Fatalf("messages %s", cap.raw)
	}
	asst := ms[1]["content"].([]any)
	if len(asst) != 3 {
		t.Fatalf("assistant blocks %v", asst)
	}
	if in := asst[2].(map[string]any)["input"].(map[string]any); len(in) != 0 {
		t.Fatalf("invalid args must become {}: %v", in)
	}
	results := ms[2]["content"].([]any)
	if ms[2]["role"] != "user" || len(results) != 2 {
		t.Fatalf("tool results not grouped: %v", ms[2])
	}
	last := results[1].(map[string]any)
	if last["content"] != "(no output)" || last["tool_use_id"] != "t2" {
		t.Fatalf("last result %v", last)
	}
	if cc, _ := last["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last block cache_control %v", last)
	}
	// exactly 3 cache breakpoints: system, last tool, last block (limit is 4)
	if n := strings.Count(string(cap.raw), `"cache_control"`); n != 3 {
		t.Fatalf("cache_control count %d", n)
	}
}

func TestProviderAnthropicMaxTokensAndNoSystem(t *testing.T) {
	srv, cap := provSSEServer(t, provAnthEv("message_stop", `{"type":"message_stop"}`))
	if _, err := (&AnthropicProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m", MaxTokens: 777, Messages: []Message{{Role: "user", Content: "x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if cap.body["max_tokens"] != float64(777) {
		t.Fatalf("max_tokens %v", cap.body["max_tokens"])
	}
	if _, ok := cap.body["system"]; ok {
		t.Fatal("system sent when empty")
	}
	if _, ok := cap.body["tools"]; ok {
		t.Fatal("tools sent when empty")
	}
}

func TestProviderToAnthropicMessages(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "do it"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Name: "x", Args: `{"k":1}`},
			{ID: "b", Name: "y", Args: `[1,2]`},
			{ID: "c", Name: "z", Args: ``},
			{ID: "d", Name: "w", Args: `"str"`},
		}},
		{Role: "tool", ToolCallID: "a", Content: "ra"},
		{Role: "tool", ToolCallID: "b", Content: "rb"},
		{Role: "tool", ToolCallID: "c", Content: "rc"},
		{Role: "tool", ToolCallID: "d", Content: "rd"},
		{Role: "user", Content: "steer"},
	}
	out := toAnthropicMessages(msgs)
	b, _ := json.Marshal(out)
	var got []struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, m := range got {
		roles = append(roles, m.Role)
	}
	if !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "user"}) {
		t.Fatalf("roles must alternate: %v (%s)", roles, b)
	}
	if got[0].Content[0]["text"] != "(empty)" || got[1].Content[0]["text"] != "(empty)" {
		t.Fatalf("empty placeholders: %s", b)
	}
	tu := got[3].Content
	if len(tu) != 4 || tu[0]["type"] != "tool_use" {
		t.Fatalf("tool_use blocks %v", tu)
	}
	if in := tu[0]["input"].(map[string]any); in["k"] != float64(1) {
		t.Fatalf("valid input %v", in)
	}
	for i := 1; i < 4; i++ {
		in, ok := tu[i]["input"].(map[string]any)
		if !ok || len(in) != 0 {
			t.Fatalf("non-object/invalid args #%d must become {}: %v", i, tu[i]["input"])
		}
	}
	res := got[4].Content
	if len(res) != 5 {
		t.Fatalf("4 tool results + steer text in one user message, got %v", res)
	}
	for i, id := range []string{"a", "b", "c", "d"} {
		if res[i]["type"] != "tool_result" || res[i]["tool_use_id"] != id {
			t.Fatalf("result %d %v", i, res[i])
		}
		if _, ok := res[i]["cache_control"]; ok {
			t.Fatalf("cache_control only on last block, found on %d", i)
		}
	}
	if res[4]["type"] != "text" || res[4]["text"] != "steer" {
		t.Fatalf("steer %v", res[4])
	}
	if cc, _ := res[4]["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last block cache_control %v", res[4])
	}
	if n := strings.Count(string(b), "cache_control"); n != 1 {
		t.Fatalf("cache_control count %d", n)
	}
	if toAnthropicMessages(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}

func TestProviderAnthropicCancelKeepsPartial(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"half\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := (&AnthropicProvider{BaseURL: srv.URL}).Chat(ctx, ChatRequest{Model: "m"}, func(string, string) { cancel() })
	if !errors.Is(err, context.Canceled) || out == nil || out.Text != "half" {
		t.Fatalf("err %v out %+v", err, out)
	}
}

// ---------------------------------------------------------------------------
// provider_codex.go
// ---------------------------------------------------------------------------

// provCodexSetup stores a valid ChatGPT credential under a temp JAZZ_HOME.
func provCodexSetup(t *testing.T, access string, expires int64) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	if err := SaveCred("chatgpt", &Cred{Type: "oauth", Access: access, Refresh: "refresh-1", AccountID: "acct-1", Expires: expires, LastRefresh: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

func provCodexEv(typ, data string) string { return "event: " + typ + "\ndata: " + data }

var provCodexStream = []string{
	provCodexEv("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`),
	provCodexEv("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`),
	provCodexEv("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"Think"}`),
	provCodexEv("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"ing"}`),
	provCodexEv("response.reasoning_summary_part.done", `{"type":"response.reasoning_summary_part.done"}`),
	provCodexEv("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"Thinking"}]}}`),
	provCodexEv("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hel"}`),
	provCodexEv("response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`),
	provCodexEv("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`),
	provCodexEv("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","delta":"{\"cmd\""}`),
	provCodexEv("response.output_item.done", `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`),
	provCodexEv("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":500,"input_tokens_details":{"cached_tokens":400},"output_tokens":50,"total_tokens":550}}}`),
}

func TestProviderCodexStream(t *testing.T) {
	access := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	provCodexSetup(t, access, time.Now().Add(time.Hour).UnixMilli())
	srv, cap := provSSEServer(t, provCodexStream...)
	rec := &provStreamRec{}
	p := &CodexProvider{BaseURL: srv.URL + "/backend-api/codex/", SessionID: "sess-9"}
	req := ChatRequest{Model: "gpt-x", System: "SYS", Tools: provTools, Messages: []Message{{Role: "user", Content: "hi"}}}
	out, err := p.Chat(context.Background(), req, rec.fn)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Hello" || out.Reasoning != "Thinking\n\n" {
		t.Fatalf("text %q reasoning %q", out.Text, out.Reasoning)
	}
	if rec.joined("text") != "Hello" || rec.joined("reasoning") != "Thinking" {
		t.Fatalf("stream %v", rec.parts)
	}
	if !reflect.DeepEqual(out.ToolCalls, []ToolCall{{ID: "call_1", Name: "bash", Args: `{"cmd":"ls"}`}}) {
		t.Fatalf("calls %+v", out.ToolCalls)
	}
	if out.Usage != (Usage{In: 500, Out: 50, CacheRead: 400}) || out.Stop != "stop" {
		t.Fatalf("usage %+v stop %q", out.Usage, out.Stop)
	}
	var ex struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out.Extra, &ex); err != nil || len(ex.Items) != 3 {
		t.Fatalf("extra %s", out.Extra)
	}
	// request
	if cap.path != "/backend-api/codex/responses" {
		t.Fatalf("path %q", cap.path)
	}
	h := cap.headers
	if h.Get("Authorization") != "Bearer "+access || h.Get("ChatGPT-Account-ID") != "acct-1" || h.Get("session-id") != "sess-9" || h.Get("originator") != originator || h.Get("version") != version {
		t.Fatalf("headers %v", h)
	}
	if !strings.HasPrefix(h.Get("User-Agent"), originator+"/"+version+" (") {
		t.Fatalf("UA %q", h.Get("User-Agent"))
	}
	b := cap.body
	if b["model"] != "gpt-x" || b["instructions"] != "SYS" || b["store"] != false || b["stream"] != true || b["prompt_cache_key"] != "sess-9" || b["tool_choice"] != "auto" || b["parallel_tool_calls"] != true {
		t.Fatalf("body %s", cap.raw)
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "medium" || r["summary"] != "auto" {
		t.Fatalf("reasoning %v", b["reasoning"])
	}
	if inc, _ := b["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include %v", b["include"])
	}
	tools := provMsgs(t, b, "tools")
	if len(tools) != 2 || tools[0]["type"] != "function" || tools[0]["name"] != "read" || tools[0]["strict"] != false || tools[0]["parameters"] == nil {
		t.Fatalf("tools %v", tools)
	}
	in := provMsgs(t, b, "input")
	if len(in) != 1 || in[0]["role"] != "user" {
		t.Fatalf("input %v", in)
	}

	// Round trip: the stored items are replayed verbatim minus id/status.
	hist := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: out.Text, ToolCalls: out.ToolCalls, Extra: out.Extra},
		{Role: "tool", ToolCallID: "call_1", Content: "a.go"},
	}
	items := toResponsesInput(hist)
	if len(items) != 5 {
		t.Fatalf("items %v", items)
	}
	for _, it := range items[1:4] {
		if _, ok := it["id"]; ok {
			t.Fatalf("id not stripped: %v", it)
		}
		if _, ok := it["status"]; ok {
			t.Fatalf("status not stripped: %v", it)
		}
	}
	if items[1]["type"] != "reasoning" || items[1]["encrypted_content"] != "ENC" {
		t.Fatalf("reasoning item %v", items[1])
	}
	if items[3]["type"] != "function_call" || items[3]["call_id"] != "call_1" {
		t.Fatalf("fc item %v", items[3])
	}
	if items[4]["type"] != "function_call_output" || items[4]["call_id"] != "call_1" || items[4]["output"] != "a.go" {
		t.Fatalf("output item %v", items[4])
	}
}

func TestProviderCodexToResponsesInputNoExtra(t *testing.T) {
	items := toResponsesInput([]Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Args: "{bad"}, {ID: "c2", Name: "read", Args: `{"p":1}`}}},
		{Role: "tool", ToolCallID: "c1", Content: ""},
		{Role: "assistant", Content: ""},
		{Role: "assistant", Content: "x", Extra: json.RawMessage(`{"reasoning_details":[]}`)},
		{Role: "assistant", Content: "y", Extra: json.RawMessage(`not json`)},
	})
	want := []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "q"}}},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "a"}}},
		{"type": "function_call", "name": "bash", "arguments": "{}", "call_id": "c1"},
		{"type": "function_call", "name": "read", "arguments": `{"p":1}`, "call_id": "c2"},
		{"type": "function_call_output", "call_id": "c1", "output": "(no output)"},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "x"}}},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "y"}}},
	}
	if !reflect.DeepEqual(items, want) {
		gb, _ := json.Marshal(items)
		t.Fatalf("got %s", gb)
	}
}

func TestProviderCodexIncomplete(t *testing.T) {
	provCodexSetup(t, "tok", time.Now().Add(time.Hour).UnixMilli())
	srv, cap := provSSEServer(t,
		provCodexEv("response.output_text.delta", `{"type":"response.output_text.delta","delta":"cut"}`),
		provCodexEv("response.incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":4}}}`),
	)
	out, err := (&CodexProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m", Effort: "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stop != "max_output_tokens" || out.Text != "cut" || out.Usage.In != 3 || out.Usage.Out != 4 {
		t.Fatalf("%+v", out)
	}
	if out.Extra != nil {
		t.Fatalf("no items => no extra, got %s", out.Extra)
	}
	if r, _ := cap.body["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Fatalf("effort %v", cap.body["reasoning"])
	}
	if _, ok := cap.body["tools"]; ok {
		t.Fatal("tools sent without tools")
	}
	if _, ok := cap.body["prompt_cache_key"]; ok {
		t.Fatal("prompt_cache_key without session")
	}
	if cap.headers.Get("session-id") != "" {
		t.Fatal("session-id without session")
	}
}

func TestProviderCodexFailed(t *testing.T) {
	cases := []struct {
		name   string
		data   string
		status int
		body   string
	}{
		{"rate", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"slow down"}}}`, 429, "rate_limit_exceeded: slow down"},
		{"usage", `{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"limit"}}}`, 429, "usage_limit_reached: limit"},
		{"quota", `{"type":"response.failed","response":{"error":{"code":"insufficient_quota","message":"q"}}}`, 429, "insufficient_quota: q"},
		{"context", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too long"}}}`, 400, "context_length_exceeded: too long"},
		{"server", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"oops"}}}`, 500, "server_error: oops"},
		{"no error obj", `{"type":"response.failed","response":{}}`, 500, ": response failed"},
		{"error event", `{"type":"error","code":"bad","message":"broken"}`, 500, "bad: broken"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			provCodexSetup(t, "tok", time.Now().Add(time.Hour).UnixMilli())
			srv, _ := provSSEServer(t,
				provCodexEv("response.output_text.delta", `{"type":"response.output_text.delta","delta":"x"}`),
				"data: "+c.data,
				provCodexEv("response.completed", `{"type":"response.completed","response":{}}`),
			)
			out, err := (&CodexProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
			if out != nil && (!out.Partial || out.Text != "x") { // partial text comes back with the error
				t.Fatalf("out %+v", out)
			}
			ae := provAPIErr(t, err)
			if ae.Status != c.status || ae.Body != c.body {
				t.Fatalf("got %+v", ae)
			}
		})
	}
}

func TestProviderCodexNotLoggedIn(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	_, err := (&CodexProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "jazz login chatgpt") {
		t.Fatalf("err %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("must not hit the API without credentials")
	}
}

// provAuthServer is a fake auth.openai.com token endpoint.
func provAuthServer(t *testing.T, newAccess string, status int) (*httptest.Server, *provCapture) {
	t.Helper()
	cap := &provCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		if r.URL.Path != "/oauth/token" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
		if status == 200 {
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  newAccess,
				"refresh_token": "refresh-2",
				"id_token":      provJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"}}),
				"expires_in":    3600,
			})
		} else {
			io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh token reused"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestProviderCodex401RefreshAndRetry(t *testing.T) {
	oldAccess := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "v": 1})
	newExp := time.Now().Add(2 * time.Hour).Unix()
	newAccess := provJWT(map[string]any{"exp": newExp, "v": 2})
	provCodexSetup(t, oldAccess, time.Now().Add(time.Hour).UnixMilli())
	authSrv, authCap := provAuthServer(t, newAccess, 200)
	rt := provHijackHosts(t, map[string]string{"auth.openai.com": authSrv.URL})

	var calls int32
	var mu sync.Mutex
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization")+"|"+r.Header.Get("ChatGPT-Account-ID"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+newAccess {
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":"token expired"}`)
			return
		}
		provWriteSSE(w,
			provCodexEv("response.output_text.delta", `{"type":"response.output_text.delta","delta":"ok"}`),
			provCodexEv("response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	defer api.Close()

	out, err := (&CodexProvider{BaseURL: api.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "ok" {
		t.Fatalf("text %q", out.Text)
	}
	if atomic.LoadInt32(&calls) != 2 || rt.counts["auth.openai.com"] != 1 {
		t.Fatalf("api calls %d refreshes %d", calls, rt.counts["auth.openai.com"])
	}
	if seen[1] != "Bearer "+newAccess+"|acct-2" {
		t.Fatalf("retry headers %v", seen)
	}
	if authCap.body["grant_type"] != "refresh_token" || authCap.body["refresh_token"] != "refresh-1" || authCap.body["client_id"] != codexClientID {
		t.Fatalf("refresh body %s", authCap.raw)
	}
	cr := LoadAuth()["chatgpt"]
	if cr == nil || cr.Access != newAccess || cr.Refresh != "refresh-2" || cr.AccountID != "acct-2" || cr.Expires != newExp*1000 {
		t.Fatalf("saved cred %+v", cr)
	}
}

func TestProviderCodex401TwiceGivesUp(t *testing.T) {
	provCodexSetup(t, "old", time.Now().Add(time.Hour).UnixMilli())
	authSrv, _ := provAuthServer(t, "new", 200)
	rt := provHijackHosts(t, map[string]string{"auth.openai.com": authSrv.URL})
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
	}))
	defer api.Close()
	_, err := (&CodexProvider{BaseURL: api.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if ae := provAPIErr(t, err); ae.Status != 401 {
		t.Fatalf("got %+v", ae)
	}
	if atomic.LoadInt32(&calls) != 2 || rt.counts["auth.openai.com"] != 1 {
		t.Fatalf("api calls %d refreshes %d (must retry exactly once)", calls, rt.counts["auth.openai.com"])
	}
}

func TestProviderCodex401RefreshRejected(t *testing.T) {
	provCodexSetup(t, "old", time.Now().Add(time.Hour).UnixMilli())
	authSrv, _ := provAuthServer(t, "", 401)
	provHijackHosts(t, map[string]string{"auth.openai.com": authSrv.URL})
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
	}))
	defer api.Close()
	_, err := (&CodexProvider{BaseURL: api.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "jazz login chatgpt") || !strings.Contains(err.Error(), "refresh token reused") {
		t.Fatalf("err %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("api calls %d", calls)
	}
	if cr := LoadAuth()["chatgpt"]; cr.Access != "old" {
		t.Fatal("cred must not change on failed refresh")
	}
}

func TestProviderCodexNon401NoRetry(t *testing.T) {
	provCodexSetup(t, "tok", time.Now().Add(time.Hour).UnixMilli())
	rt := provHijackHosts(t, map[string]string{"auth.openai.com": "http://127.0.0.1:1"})
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(429)
		io.WriteString(w, "slow")
	}))
	defer api.Close()
	_, err := (&CodexProvider{BaseURL: api.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if ae := provAPIErr(t, err); ae.Status != 429 || ae.Body != "slow" {
		t.Fatalf("got %+v", ae)
	}
	if calls != 1 || rt.counts["auth.openai.com"] != 0 {
		t.Fatalf("calls %d refresh %d", calls, rt.counts["auth.openai.com"])
	}
}

func TestProviderChatgptCredStaleRefreshes(t *testing.T) {
	newAccess := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	cases := []struct {
		name    string
		expires int64
		last    int64
		refresh bool
	}{
		{"fresh", time.Now().Add(time.Hour).UnixMilli(), time.Now().UnixMilli(), false},
		{"near expiry", time.Now().Add(2 * time.Minute).UnixMilli(), time.Now().UnixMilli(), true},
		{"no expiry", 0, time.Now().UnixMilli(), true},
		{"old refresh", time.Now().Add(time.Hour).UnixMilli(), time.Now().Add(-9 * 24 * time.Hour).UnixMilli(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("JAZZ_HOME", t.TempDir())
			if err := SaveCred("chatgpt", &Cred{Type: "oauth", Access: "old", Refresh: "r", AccountID: "a", Expires: c.expires, LastRefresh: c.last}); err != nil {
				t.Fatal(err)
			}
			authSrv, _ := provAuthServer(t, newAccess, 200)
			rt := provHijackHosts(t, map[string]string{"auth.openai.com": authSrv.URL})
			cr, err := chatgptCred(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			did := rt.counts["auth.openai.com"] == 1
			if did != c.refresh {
				t.Fatalf("refreshed=%v want %v", did, c.refresh)
			}
			if c.refresh && cr.Access != newAccess {
				t.Fatalf("access %q", cr.Access)
			}
			if !c.refresh && cr.Access != "old" {
				t.Fatalf("access %q", cr.Access)
			}
		})
	}
}

func TestProviderChatgptCredNetworkHiccupKeepsValidToken(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	// stale by age, still valid by expiry
	if err := SaveCred("chatgpt", &Cred{Access: "still", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(), LastRefresh: time.Now().Add(-10 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	provHijackHosts(t, map[string]string{"auth.openai.com": deadURL})
	cr, err := chatgptCred(context.Background(), false)
	if err != nil || cr.Access != "still" {
		t.Fatalf("cr %+v err %v", cr, err)
	}
	if _, err := chatgptCred(context.Background(), true); err == nil {
		t.Fatal("forced refresh with network down must fail")
	}
}

// ---------------------------------------------------------------------------
// oauth.go JWT helpers
// ---------------------------------------------------------------------------

func TestProviderJWTClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u1","n":5}`))
	padded := base64.URLEncoding.EncodeToString([]byte(`{"sub":"u2"}`))
	cases := []struct {
		name string
		tok  string
		sub  any
		nil_ bool
	}{
		{"valid", "h." + payload + ".s", "u1", false},
		{"two parts", "h." + payload, "u1", false},
		{"padded payload", "h." + padded + ".s", "u2", false},
		{"one part", payload, nil, true},
		{"empty", "", nil, true},
		{"bad base64", "h.@@@.s", nil, true},
		{"not json", "h." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".s", nil, true},
		{"json array", "h." + base64.RawURLEncoding.EncodeToString([]byte("[1]")) + ".s", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := jwtClaims(c.tok)
			if (m == nil) != c.nil_ {
				t.Fatalf("claims %v", m)
			}
			if !c.nil_ && m["sub"] != c.sub {
				t.Fatalf("sub %v", m["sub"])
			}
		})
	}
}

func TestProviderJWTExpiry(t *testing.T) {
	if got := jwtExpiry(provJWT(map[string]any{"exp": 1700000000})); got != 1700000000*1000 {
		t.Fatalf("exp ms %d", got)
	}
	for _, tok := range []string{"garbage", provJWT(map[string]any{"sub": "x"}), provJWT(map[string]any{"exp": "soon"})} {
		before := time.Now().Add(time.Hour).UnixMilli()
		got := jwtExpiry(tok)
		after := time.Now().Add(time.Hour).UnixMilli()
		if got < before-1000 || got > after+1000 {
			t.Fatalf("fallback expiry for %q = %d, want ~now+1h", tok, got)
		}
	}
}

func TestProviderAccountIDFrom(t *testing.T) {
	top := provJWT(map[string]any{"chatgpt_account_id": "top"})
	nested := provJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "nested"}})
	orgs := provJWT(map[string]any{"organizations": []any{map[string]any{"id": "org-1"}, map[string]any{"id": "org-2"}}})
	all := provJWT(map[string]any{
		"chatgpt_account_id":          "top",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "nested"},
		"organizations":               []any{map[string]any{"id": "org-1"}},
	})
	nestedOrg := provJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "nested"},
		"organizations":               []any{map[string]any{"id": "org-1"}},
	})
	emptyTop := provJWT(map[string]any{"chatgpt_account_id": "", "organizations": []any{map[string]any{"id": "org-x"}}})
	none := provJWT(map[string]any{"sub": "x", "organizations": []any{}})
	badOrg := provJWT(map[string]any{"organizations": []any{"str"}})
	cases := []struct {
		name string
		toks []string
		want string
	}{
		{"top-level", []string{top}, "top"},
		{"nested auth claim", []string{nested}, "nested"},
		{"organizations[0]", []string{orgs}, "org-1"},
		{"precedence top>nested>org", []string{all}, "top"},
		{"precedence nested>org", []string{nestedOrg}, "nested"},
		{"empty string skipped", []string{emptyTop}, "org-x"},
		{"first token without id falls to second", []string{none, nested}, "nested"},
		{"garbage then valid", []string{"garbage", "", top}, "top"},
		{"first wins", []string{orgs, top}, "org-1"},
		{"bad org shape", []string{badOrg}, ""},
		{"none", []string{none, "x.y.z"}, ""},
		{"no tokens", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := accountIDFrom(c.toks...); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestProviderTrimBodyAndRandB64(t *testing.T) {
	if trimBody([]byte("  x \n")) != "x" {
		t.Fatal("trim")
	}
	long := strings.Repeat("a", 400)
	if got := trimBody([]byte(long)); got != strings.Repeat("a", 300)+"…" {
		t.Fatalf("len %d", len(got))
	}
	a, b := randB64(32), randB64(32)
	if a == b || len(a) != 43 || strings.ContainsAny(a, "+/=") {
		t.Fatalf("randB64 %q %q", a, b)
	}
}

func TestProviderAPIErrorRetryable(t *testing.T) {
	for st, want := range map[int]bool{0: true, 400: false, 401: false, 402: true, 404: false, 408: true, 429: true, 500: true, 529: true} {
		if (&APIError{Status: st}).Retryable() != want {
			t.Errorf("status %d retryable!=%v", st, want)
		}
	}
	e := &APIError{Status: 500, Body: strings.Repeat("x", 500)}
	if !strings.HasPrefix(e.Error(), "HTTP 500: ") || !strings.HasSuffix(e.Error(), "…") {
		t.Fatalf("%q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// providers: findings 16/18/19/60/65/66, G1/G4/G9/G15/G21/G22 and quirks
// ---------------------------------------------------------------------------

func TestProviderPostStreamRetryAfterAndDaily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"Rate limit exceeded: free-models-per-day"}}`)
	}))
	defer srv.Close()
	_, _, _, _, err := postStream(context.Background(), srv.URL, nil, map[string]any{}, time.Second)
	ae := provAPIErr(t, err)
	if ae.Status != 429 || ae.RetryAfter != 12*time.Second || !ae.Daily {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderPostStreamJSON200(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"error object", `{"error":{"code":429,"message":"too many"}}`, 429},
		{"error without code", `{"error":{"message":"upstream failed"}}`, 502},
		{"garbage", `not json`, 502},
		{"no choices", `{"object":"x"}`, 502},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			_, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
			if ae := provAPIErr(t, err); ae.Status != c.status {
				t.Fatalf("got %+v", ae)
			}
		})
	}
	// a non-streamed completion (server ignored stream:true) is still understood
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"choices":[{"message":{"content":"hi","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	out, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "hi" || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "read" || out.Stop != "tool_calls" || out.Usage.In != 3 {
		t.Fatalf("%+v", out)
	}
}

func TestProviderOpenAIEmptyStreamIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, err := (&OpenAIProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if ae := provAPIErr(t, err); ae.Status != 502 {
		t.Fatalf("got %+v", ae)
	}
}

func TestProviderReadSSEBounded(t *testing.T) {
	old := pvMaxSSELine
	pvMaxSSELine = 1024
	defer func() { pvMaxSSELine = old }()
	err := readSSE(strings.NewReader("data: "+strings.Repeat("x", 5000)+"\n\n"), func(bool) {}, func(_, _ string) error { return nil })
	if ae := provAPIErr(t, err); !strings.Contains(ae.Body, "grande demais") {
		t.Fatalf("got %v", err)
	}
	// many small data lines that add up to a huge event are cut too
	err = readSSE(strings.NewReader(strings.Repeat("data: "+strings.Repeat("y", 500)+"\n", 5)+"\n"), func(bool) {}, func(_, _ string) error { return nil })
	if ae := provAPIErr(t, err); !strings.Contains(ae.Body, "grande demais") {
		t.Fatalf("got %v", err)
	}
	// a line longer than the bufio buffer but under the cap still works
	pvMaxSSELine = 200 << 10
	var got string
	if err := readSSE(strings.NewReader("data: "+strings.Repeat("z", 100<<10)+"\n\n"), func(bool) {}, func(_, d string) error { got = d; return nil }); err != nil || len(got) != 100<<10 {
		t.Fatalf("long line: %v %d", err, len(got))
	}
	// the whole stream is capped
	r := &pvCapReader{r: io.NopCloser(strings.NewReader(strings.Repeat("a", 100))), left: 10}
	b, err := io.ReadAll(r)
	if len(b) != 10 || err == nil {
		t.Fatalf("cap reader %d %v", len(b), err)
	}
}

func TestProviderOpenAIRequestFeatures(t *testing.T) {
	temp := 0.3
	srv, cap := provSSEServer(t, `{"choices":[{"delta":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0.0042}}`, `[DONE]`)
	req := ChatRequest{Model: "m", Tools: provTools, NoToolUse: true, Temperature: &temp,
		Messages: []Message{{Role: "user", Content: "see", Images: []ImagePart{{Mime: "image/png", Data: "QUJD"}}}}}
	out, err := (&OpenAIProvider{BaseURL: srv.URL + "/openrouter.ai/api/v1"}).Chat(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := cap.body
	if b["tool_choice"] != "none" || b["temperature"] != 0.3 {
		t.Fatalf("tool_choice/temperature: %s", cap.raw)
	}
	if u, _ := b["usage"].(map[string]any); u["include"] != true {
		t.Fatalf("usage.include: %v", b["usage"])
	}
	if out.Usage.Cost != 0.0042 {
		t.Fatalf("cost %v", out.Usage.Cost)
	}
	parts := provMsgs(t, b, "messages")[0]["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("image parts %v", parts)
	}
	// TextTools: tools never sent
	srv2, cap2 := provSSEServer(t, `[DONE]`)
	if _, err := (&OpenAIProvider{BaseURL: srv2.URL + "/openrouter.ai/api/v1"}).Chat(context.Background(), ChatRequest{Model: "m", Tools: provTools, TextTools: true}, nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tools", "tool_choice", "provider"} {
		if _, ok := cap2.body[k]; ok {
			t.Fatalf("%s sent in text-tools mode", k)
		}
	}
}

func TestProviderOpenAICacheControlViaOpenRouter(t *testing.T) {
	for _, c := range []struct {
		model string
		want  int
	}{{"anthropic/claude-sonnet-5-5", 2}, {"google/gemini-3.8-flash", 2}, {"openai/gpt-x", 0}} {
		srv, cap := provSSEServer(t, `[DONE]`)
		req := ChatRequest{Model: c.model, System: "SYS", Messages: []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}}
		if _, err := (&OpenAIProvider{BaseURL: srv.URL + "/openrouter.ai/api/v1"}).Chat(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(cap.raw), `"cache_control"`); n != c.want {
			t.Fatalf("%s: %d cache_control in %s", c.model, n, cap.raw)
		}
		if c.want > 0 {
			ms := provMsgs(t, cap.body, "messages")
			if _, ok := ms[1]["content"].(string); !ok {
				t.Fatalf("only the LAST user message gets parts: %v", ms[1])
			}
		}
	}
}

func TestProviderOpenAIDeepSeekReasoningContent(t *testing.T) {
	srv, cap := provSSEServer(t,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":"{}"}}]}}]}`,
		`[DONE]`)
	p := &OpenAIProvider{Name: "deepseek", BaseURL: srv.URL + "/api.deepseek.com"}
	out, err := p.Chat(context.Background(), ChatRequest{Model: "deepseek-flash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Extra), `"prov":"deepseek"`) {
		t.Fatalf("provenance %s", out.Extra)
	}
	hist := []Message{{Role: "user", Content: "x"},
		{Role: "assistant", Reasoning: out.Reasoning, ToolCalls: out.ToolCalls, Extra: out.Extra},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "assistant", Reasoning: "foreign", ToolCalls: []ToolCall{{ID: "c2", Name: "bash", Args: "{}"}}, Extra: json.RawMessage(`{"prov":"groq"}`)},
		{Role: "tool", ToolCallID: "c2", Content: "ok"}}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "deepseek-flash", Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	ms := provMsgs(t, cap.body, "messages")
	if ms[1]["reasoning_content"] != "think" {
		t.Fatalf("own reasoning_content not echoed: %v", ms[1])
	}
	if _, ok := ms[3]["reasoning_content"]; ok {
		t.Fatalf("another provider's reasoning must not be echoed: %v", ms[3])
	}
	// generic providers never get reasoning_content
	srv2, cap2 := provSSEServer(t, `[DONE]`)
	if _, err := (&OpenAIProvider{Name: "g", BaseURL: srv2.URL}).Chat(context.Background(), ChatRequest{Model: "llama", Messages: hist[:3]}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cap2.raw), "reasoning_content") {
		t.Fatalf("generic provider got reasoning_content: %s", cap2.raw)
	}
}

func TestProviderOpenAIGeminiThoughtSignature(t *testing.T) {
	srv, cap := provSSEServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"g1","extra_content":{"google":{"thought_signature":"SIG"}},"function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`,
		`[DONE]`)
	p := &OpenAIProvider{Name: "gemini", BaseURL: srv.URL}
	pvGeminiSetup(p)
	out, err := p.Chat(context.Background(), ChatRequest{Model: "models/gemini-3.8-flash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cap.body["model"] != "gemini-3.8-flash" {
		t.Fatalf("models/ prefix not stripped: %v", cap.body["model"])
	}
	hist := []Message{{Role: "user", Content: "x"}, {Role: "assistant", ToolCalls: out.ToolCalls, Extra: out.Extra}, {Role: "tool", ToolCallID: "g1", Content: "ok"}}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "gemini-3.8-flash", Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	tc := provMsgs(t, cap.body, "messages")[1]["tool_calls"].([]any)[0].(map[string]any)
	if g := tc["extra_content"].(map[string]any)["google"].(map[string]any); g["thought_signature"] != "SIG" {
		t.Fatalf("signature not echoed: %v", tc)
	}
	// another provider never sees it
	ms := pvOAMessages("", hist, pvOAOpts{prov: "openrouter", thoughtSig: true, details: true})
	if _, ok := ms[1]["tool_calls"].([]map[string]any)[0]["extra_content"]; ok {
		t.Fatal("thought signature leaked to another provider")
	}
}

func TestProviderOpenAIMistralIDs(t *testing.T) {
	hist := []Message{{Role: "user", Content: "x"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_abc-123_long", Name: "bash", Args: "{}"}, {ID: "Ab3dE6gH9", Name: "read", Args: "{}"}}},
		{Role: "tool", ToolCallID: "call_abc-123_long", Content: "a"}, {Role: "tool", ToolCallID: "Ab3dE6gH9", Content: "b"}}
	ms := pvOAMessages("", hist, pvOAOpts{ids9: true})
	tcs := ms[1]["tool_calls"].([]map[string]any)
	id0 := tcs[0]["id"].(string)
	if !pvID9RE.MatchString(id0) || ms[2]["tool_call_id"] != id0 {
		t.Fatalf("mapped id %q / %v", id0, ms[2]["tool_call_id"])
	}
	if tcs[1]["id"] != "Ab3dE6gH9" || ms[3]["tool_call_id"] != "Ab3dE6gH9" {
		t.Fatal("valid ids kept")
	}
	if pvID9("x") != pvID9("x") || pvID9("x") == pvID9("y") {
		t.Fatal("deterministic and distinct")
	}
	if hist[1].ToolCalls[0].ID != "call_abc-123_long" {
		t.Fatal("history mutated")
	}
}

func TestProviderOpenAIQuirkBodies(t *testing.T) {
	cases := []struct {
		name, path, effort string
		quirks             []string
		key                string
		want               any
	}{
		{"groq effort", "/api.groq.com/openai/v1", "low", nil, "reasoning_effort", "low"},
		{"cohere high", "/v1", "medium", []string{"cohere-effort"}, "reasoning_effort", "high"},
		{"cohere low omitted", "/v1", "low", []string{"cohere-effort"}, "reasoning_effort", nil},
	}
	for _, c := range cases {
		srv, cap := provSSEServer(t, `[DONE]`)
		if _, err := (&OpenAIProvider{BaseURL: srv.URL + c.path, Quirks: c.quirks}).Chat(context.Background(), ChatRequest{Model: "m", Effort: c.effort}, nil); err != nil {
			t.Fatal(err)
		}
		if got := cap.body[c.key]; got != c.want {
			t.Errorf("%s: %s=%v want %v", c.name, c.key, got, c.want)
		}
	}
	// Ollama: options.num_ctx from the per-model window; custom headers merged
	t.Setenv("JAZZ_HOME", t.TempDir())
	srv, cap := provSSEServer(t, `[DONE]`)
	cfg := &Config{ContextWindow: 128000, MaxTokens: 1000, Providers: map[string]*ProviderConf{
		"ol": {Type: "openai", BaseURL: srv.URL, NoKey: true, Ollama: true, Headers: map[string]string{"X-A": "b"}}},
		Models: map[string]*ModelConf{"ol/q": {Context: 32768}}}
	p, err := cfg.NewProvider("ol")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "q"}, nil); err != nil {
		t.Fatal(err)
	}
	if o, _ := cap.body["options"].(map[string]any); o["num_ctx"] != float64(32768) {
		t.Fatalf("num_ctx %v", cap.body["options"])
	}
	if cap.headers.Get("X-A") != "b" {
		t.Fatal("custom header")
	}
}

func TestProviderLimiterIntegration(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "40")
		w.WriteHeader(429)
		io.WriteString(w, "slow down")
	}))
	defer srv.Close()
	cfg := &Config{ContextWindow: 128000, MaxTokens: 1000, Providers: map[string]*ProviderConf{
		"lim": {Type: "openai", BaseURL: srv.URL, NoKey: true, Limits: &Limits{RPM: 100}}}}
	p, _ := cfg.NewProvider("lim")
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if ae := provAPIErr(t, err); ae.RetryAfter != 40*time.Second {
		t.Fatalf("%+v", ae)
	}
	st := cfg.pvState("lim/m", 0)
	if st.Ready || st.Wait < 39*time.Second {
		t.Fatalf("429 must cool the candidate down: %+v", st)
	}
	if b := cfg.rlBudget("lim/m"); b != "100/min" {
		t.Fatalf("budget %q", b)
	}
}

func TestProviderAnthropicThinkingRoundTrip(t *testing.T) {
	srv, cap := provSSEServer(t,
		provAnthEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me"}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG1"}}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"RED"}}`),
		provAnthEv("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}`),
		provAnthEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`),
		provAnthEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`),
	)
	p := &AnthropicProvider{Name: "anthropic", BaseURL: srv.URL, Key: "k"}
	temp := 0.2
	out, err := p.Chat(context.Background(), ChatRequest{Model: "claude", MaxTokens: 16000, Effort: "high", Temperature: &temp,
		Messages: []Message{{Role: "user", Content: "go"}}, Tools: provTools}, nil)
	if err != nil {
		t.Fatal(err)
	}
	th, _ := cap.body["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != float64(14976) {
		t.Fatalf("thinking %v (budget clamped below max_tokens)", cap.body["thinking"])
	}
	if _, ok := cap.body["temperature"]; ok {
		t.Fatal("temperature must be omitted with thinking")
	}
	if out.Reasoning != "let me" || len(out.ToolCalls) != 1 || out.Stop != "tool_use" {
		t.Fatalf("%+v", out)
	}
	var ex pvAnthExtra
	if err := json.Unmarshal(out.Extra, &ex); err != nil || len(ex.Thinking) != 2 || ex.Thinking[0]["signature"] != "SIG1" || ex.Thinking[1]["data"] != "RED" || ex.Prov != "anthropic" {
		t.Fatalf("extra %s", out.Extra)
	}
	hist := []Message{{Role: "user", Content: "go"},
		{Role: "assistant", Content: "  \n", Reasoning: out.Reasoning, ToolCalls: out.ToolCalls, Extra: out.Extra},
		{Role: "tool", ToolCallID: "toolu_1", Content: "a.go"}}
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "claude", Effort: "high", Messages: hist, Tools: provTools, NoToolUse: true}, nil); err != nil {
		t.Fatal(err)
	}
	asst := provMsgs(t, cap.body, "messages")[1]["content"].([]any)
	if len(asst) != 3 || asst[0].(map[string]any)["type"] != "thinking" || asst[1].(map[string]any)["type"] != "redacted_thinking" || asst[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("replay order (thinking first, whitespace text dropped): %v", asst)
	}
	if tc, _ := cap.body["tool_choice"].(map[string]any); tc["type"] != "none" {
		t.Fatalf("tool_choice %v", cap.body["tool_choice"])
	}
	// a tool loop opened by ANOTHER provider: thinking must stay off
	hist[1].Extra = json.RawMessage(`{"prov":"openrouter","reasoning_details":[]}`)
	if _, err := p.Chat(context.Background(), ChatRequest{Model: "claude", Effort: "high", Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := cap.body["thinking"]; ok {
		t.Fatal("thinking on although the open tool loop has no thinking block")
	}
	if pvThinkingBudget("low", 1500) != 0 || pvThinkingBudget("medium", 32000) != 8192 || pvThinkingBudget("", 32000) != 0 {
		t.Fatal("budget map")
	}
}

func TestProviderAnthropicImagesAndIDs(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "look", Images: []ImagePart{{Mime: "image/jpeg", Data: "SU1H"}}},
		{Role: "assistant", Content: "\n\n", ToolCalls: []ToolCall{{ID: "call.with:bad/chars", Name: "read", Args: "{}"}}},
		{Role: "tool", ToolCallID: "call.with:bad/chars", Content: "x"},
		{Role: "assistant", Content: " \t "},
	}
	out := pvAnthMessages(msgs, "anthropic", false)
	u := out[0]["content"].([]map[string]any)
	if u[0]["type"] != "image" || u[0]["source"].(map[string]any)["media_type"] != "image/jpeg" || u[1]["text"] != "look" {
		t.Fatalf("image block %v", u)
	}
	a := out[1]["content"].([]map[string]any)
	if len(a) != 1 || a[0]["type"] != "tool_use" {
		t.Fatalf("whitespace-only text must be dropped (bug 65): %v", a)
	}
	id := a[0]["id"].(string)
	if !pvAnthIDRE.MatchString(id) || out[2]["content"].([]map[string]any)[0]["tool_use_id"] != id {
		t.Fatalf("ids %q", id)
	}
	if last := out[3]["content"].([]map[string]any); last[0]["text"] != "(empty)" {
		t.Fatalf("whitespace-only assistant → placeholder: %v", last)
	}
}

func TestProviderCodexImagesAndStop(t *testing.T) {
	in := pvResponsesInput([]Message{{Role: "user", Content: "x", Images: []ImagePart{{Mime: "image/png", Data: "QQ=="}}},
		{Role: "assistant", Content: "fallback text", Extra: json.RawMessage(`{"prov":"other","items":[{"type":"message","role":"assistant"}]}`)}}, "chatgpt")
	parts := in[0]["content"].([]map[string]any)
	if len(parts) != 2 || parts[1]["type"] != "input_image" || parts[1]["image_url"] != "data:image/png;base64,QQ==" {
		t.Fatalf("parts %v", parts)
	}
	if in[1]["type"] != "message" || in[1]["content"].([]map[string]any)[0]["text"] != "fallback text" {
		t.Fatalf("foreign items must not be replayed: %v", in[1])
	}
	provCodexSetup(t, "tok", time.Now().Add(time.Hour).UnixMilli())
	srv, cap := provSSEServer(t, provCodexEv("response.incomplete", `{"type":"response.incomplete","response":{"usage":{}}}`))
	out, err := (&CodexProvider{BaseURL: srv.URL}).Chat(context.Background(), ChatRequest{Model: "m", Tools: provTools, NoToolUse: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stop != "incomplete" || !out.Truncated() {
		t.Fatalf("stop %q", out.Stop)
	}
	if cap.body["tool_choice"] != "none" {
		t.Fatalf("tool_choice %v", cap.body["tool_choice"])
	}
}

func TestProviderOAuthCallbackIgnoresStrays(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		t.Skip("porta 1455 ocupada")
	}
	ln.Close()
	access := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "chatgpt_account_id": "acct-9"})
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "good-code" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "r1", "id_token": access})
	}))
	defer tok.Close()
	// only auth.openai.com is redirected; the loopback callback requests below are untouched
	provHijackHosts(t, map[string]string{"auth.openai.com": tok.URL})
	urlCh := make(chan string, 1)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		errCh <- LoginChatGPTBrowser(ctx, func(s string) {
			for _, f := range strings.Fields(s) {
				if strings.HasPrefix(f, "https://auth.openai.com/oauth/authorize?") {
					urlCh <- f
				}
			}
		})
	}()
	var authURL string
	select {
	case authURL = <-urlCh:
	case err := <-errCh:
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	redirect := u.Query().Get("redirect_uri")
	get := func(q string) int {
		resp, err := http.Get(redirect + "?" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := get("state=wrong&code=x"); st != 400 {
		t.Fatalf("wrong state → %d", st)
	}
	if st := get("state=" + url.QueryEscape(state)); st != 400 {
		t.Fatalf("missing code → %d", st)
	}
	if st := get("state=" + url.QueryEscape(state) + "&code=good-code"); st != 200 {
		t.Fatalf("good callback → %d", st)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("login: %v", err)
	}
	if cr := LoadAuth()["chatgpt"]; cr == nil || cr.Refresh != "r1" || cr.AccountID != "acct-9" {
		t.Fatalf("cred %+v", cr)
	}
}

func TestProviderChatgptCredReusesRotatedToken(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	if err := SaveCred("chatgpt", &Cred{Type: "oauth", Access: "old", Refresh: "r-old", AccountID: "a", Expires: time.Now().Add(time.Minute).UnixMilli(), LastRefresh: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	fresh := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// another jazz process rotated the token a moment ago
		_ = SaveCred("chatgpt", &Cred{Type: "oauth", Access: fresh, Refresh: "r-new", AccountID: "a", Expires: time.Now().Add(time.Hour).UnixMilli(), LastRefresh: time.Now().UnixMilli()})
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()
	provHijackHosts(t, map[string]string{"auth.openai.com": srv.URL})
	cr, err := chatgptCred(context.Background(), false)
	if err != nil || cr.Refresh != "r-new" || cr.Access != fresh {
		t.Fatalf("cr %+v err %v", cr, err)
	}
}

func TestProviderChatgptCredSaveFailureKeepsSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	if err := SaveCred("chatgpt", &Cred{Type: "oauth", Access: "old", Refresh: "r", AccountID: "a", LastRefresh: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	newAccess := provJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	authSrv, _ := provAuthServer(t, newAccess, 200)
	provHijackHosts(t, map[string]string{"auth.openai.com": authSrv.URL})
	if err := os.Chmod(home, 0o500); err != nil { // read-only config dir: SaveCred fails
		t.Skip(err)
	}
	defer os.Chmod(home, 0o700)
	cr, err := chatgptCred(context.Background(), true)
	if err != nil || cr.Access != newAccess || cr.Refresh != "refresh-2" {
		t.Fatalf("fresh credential must be returned even when saving fails: %+v %v", cr, err)
	}
	if w := ChatGPTWarning(); !strings.Contains(w, "não consegui salvar") {
		t.Fatalf("warning %q", w)
	}
}
