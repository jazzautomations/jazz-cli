package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type otRecv struct {
	mu     sync.Mutex
	traces []map[string]any
	mets   []map[string]any
	hdr    http.Header
	posts  int
}

func newOtRecv(t *testing.T, status int) (*otRecv, *httptest.Server) {
	r := &otRecv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		var m map[string]any
		if json.Unmarshal(b, &m) != nil || q.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(400)
			return
		}
		r.mu.Lock()
		r.posts++
		r.hdr = q.Header.Clone()
		switch q.URL.Path {
		case "/v1/traces":
			r.traces = append(r.traces, m)
		case "/v1/metrics":
			r.mets = append(r.mets, m)
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *otRecv) spans() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, m := range r.traces {
		for _, rs := range m["resourceSpans"].([]any) {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					out = append(out, s.(map[string]any))
				}
			}
		}
	}
	return out
}

func otAttrMap(s map[string]any) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, a := range s["attributes"].([]any) {
		kv := a.(map[string]any)
		m[kv["key"].(string)] = kv["value"].(map[string]any)
	}
	return m
}

func otTestLog(t *testing.T, x *otlpExp) *obLog {
	l, err := obOpen(filepath.Join(t.TempDir(), "s.jsonl"), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	l.ox = x
	t.Cleanup(l.close)
	return l
}

func TestOtlpSessionShape(t *testing.T) {
	r, srv := newOtRecv(t, 200)
	x := otlpStart(OtlpConf{Endpoint: srv.URL + "/", Headers: map[string]string{"Authorization": "Bearer tok"}})
	l := otTestLog(t, x)

	l.emit("session.start", map[string]any{"source": "startup", "model": "p/m", "version": "0.6.0", "os": "linux", "arch": "386", "pid": 1}, false)
	l.emit("turn.start", map[string]any{"prompt_sha": "abc", "input_bytes": 10, "model": "p/m", "msgs": 1}, false)
	l.emit("request.start", map[string]any{"step": 1, "model": "m", "provider": "p"}, false)
	l.emit("request.end", map[string]any{"step": 1, "model": "m", "provider": "p", "latency_ms": 1200, "in": 100, "out": 20, "cache_read": 80, "cache_write": 5, "cost": 0.01, "finish": "tool_calls", "tool_calls": 3}, false)
	for i, n := range []string{"read", "bash", "edit"} {
		id := "call_" + string(rune('a'+i))
		l.emit("tool.start", map[string]any{"step": 1, "id": id, "name": n, "args_sha": "x", "args_bytes": 7}, false)
		if n == "bash" {
			l.emit("guard", map[string]any{"step": 1, "guard": "permission", "tool": "bash", "target": "rm -rf /", "reason": "perigoso", "outcome": "refused"}, false)
		}
		l.emit("tool.end", map[string]any{"step": 1, "id": id, "name": n, "dur_ms": 30, "bytes": 123, "is_err": n == "bash", "ran": n != "bash", "truncated": n == "read"}, false)
	}
	l.emit("request.start", map[string]any{"step": 2, "model": "m", "provider": "p"}, false)
	l.emit("request.end", map[string]any{"step": 2, "model": "m", "provider": "p", "latency_ms": 800, "in": 150, "out": 30, "cost": 0.02, "finish": "stop"}, false)
	l.emit("turn.end", map[string]any{"stop": "done", "steps": 2, "tools": 3, "dur_ms": 2500, "in": 250, "out": 50, "cost": 0.03}, false)
	l.emit("run.end", map[string]any{"status": "done", "outcome": "completed", "exit": 0}, false)
	x.shutdown(2 * time.Second)

	spans := r.spans()
	byName := map[string][]map[string]any{}
	var names []string
	for _, s := range spans {
		n := s["name"].(string)
		names = append(names, n)
		k := strings.SplitN(n, " ", 2)[0]
		byName[k] = append(byName[k], s)
	}
	if len(byName["session"]) != 1 || len(byName["turn"]) != 1 || len(byName["request"]) != 2 || len(byName["tool"]) != 3 {
		t.Fatalf("spans: %v", names)
	}
	root, turn := byName["session"][0], byName["turn"][0]
	if turn["parentSpanId"] != root["spanId"] {
		t.Errorf("turn parent = %v want %v", turn["parentSpanId"], root["spanId"])
	}
	tid := root["traceId"].(string)
	if len(tid) != 32 || len(root["spanId"].(string)) != 16 {
		t.Errorf("id sizes: %q %q", tid, root["spanId"])
	}
	for _, s := range spans {
		if s["traceId"] != tid {
			t.Errorf("span %v has another trace", s["name"])
		}
		if s["name"] != "session" && s["name"] != "turn" && s["parentSpanId"] != turn["spanId"] {
			t.Errorf("span %v parent = %v want turn", s["name"], s["parentSpanId"])
		}
		if s["startTimeUnixNano"] == "" || s["endTimeUnixNano"] == "" {
			t.Errorf("span %v without times", s["name"])
		}
	}
	ra := otAttrMap(byName["request"][0])
	if ra["gen_ai.request.model"]["stringValue"] != "m" || ra["gen_ai.usage.input_tokens"]["intValue"] != "100" ||
		ra["gen_ai.usage.output_tokens"]["intValue"] != "20" || ra["jazz.tokens.cache_read"]["intValue"] != "80" ||
		ra["jazz.latency_ms"]["intValue"] != "1200" || ra["gen_ai.response.finish_reasons"]["stringValue"] != "tool_calls" {
		t.Errorf("request attrs: %v", ra)
	}
	var bash, read map[string]any
	for _, s := range byName["tool"] {
		switch s["name"] {
		case "tool bash":
			bash = s
		case "tool read":
			read = s
		}
	}
	ba := otAttrMap(bash)
	if ba["jazz.tool.bytes"]["intValue"] != "123" || ba["jazz.tool.is_error"]["boolValue"] != true {
		t.Errorf("bash attrs: %v", ba)
	}
	if otAttrMap(read)["jazz.tool.truncated"]["boolValue"] != true {
		t.Errorf("read not truncated: %v", otAttrMap(read))
	}
	if bash["status"].(map[string]any)["code"] != float64(2) {
		t.Errorf("bash status: %v", bash["status"])
	}
	evs, _ := bash["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["name"] != "guard.permission" {
		t.Fatalf("guard event: %v", bash["events"])
	}
	ga := otAttrMap(map[string]any{"attributes": evs[0].(map[string]any)["attributes"]})
	if ga["jazz.outcome"]["stringValue"] != "refused" || ga["jazz.reason"]["stringValue"] != "perigoso" {
		t.Errorf("guard attrs: %v", ga)
	}
	if r.hdr.Get("Authorization") != "Bearer tok" {
		t.Errorf("header: %v", r.hdr)
	}

	if len(r.mets) != 1 {
		t.Fatalf("metric posts = %d", len(r.mets))
	}
	b, _ := json.Marshal(r.mets[0])
	for _, want := range []string{`"jazz.tokens"`, `"jazz.cost"`, `"jazz.request.duration"`, `"jazz.tool.duration"`, `"aggregationTemporality":1`, `"asInt":"250"`, `"count":"2"`, `"explicitBounds"`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("metrics missing %s in %s", want, b)
		}
	}
}

func TestOtlpSubagentUnderTool(t *testing.T) {
	r, srv := newOtRecv(t, 200)
	x := otlpStart(OtlpConf{Endpoint: srv.URL})
	l := otTestLog(t, x)
	l.emit("session.start", map[string]any{"source": "startup"}, false)
	l.emit("turn.start", map[string]any{}, false)
	l.emit("tool.start", map[string]any{"id": "t1", "name": "task"}, false)
	l.emit("turn.start", map[string]any{"sub": true}, false)
	l.emit("request.end", map[string]any{"sub": true, "model": "m", "latency_ms": 5}, false)
	l.emit("turn.end", map[string]any{"sub": true, "dur_ms": 10}, false)
	l.emit("tool.end", map[string]any{"id": "t1", "name": "task", "dur_ms": 20}, false)
	l.emit("turn.end", map[string]any{"dur_ms": 30}, false)
	x.shutdown(2 * time.Second)
	var task, sub, req map[string]any
	for _, s := range r.spans() {
		switch s["name"] {
		case "tool task":
			task = s
		case "subagent":
			sub = s
		case "request m":
			req = s
		}
	}
	if task == nil || sub == nil || req == nil || sub["parentSpanId"] != task["spanId"] || req["parentSpanId"] != sub["spanId"] {
		t.Fatalf("hierarchy broken: task=%v sub=%v req=%v", task, sub, req)
	}
}

func TestOtlpBatching(t *testing.T) {
	r, srv := newOtRecv(t, 200)
	x := otlpStart(OtlpConf{Endpoint: srv.URL, BatchSize: 3, FlushMS: 60000})
	l := otTestLog(t, x)
	for i := 0; i < 7; i++ {
		l.emit("request.end", map[string]any{"model": "m", "latency_ms": 1}, false)
	}
	x.flush(2 * time.Second)
	r.mu.Lock()
	n := len(r.traces)
	r.mu.Unlock()
	// 7 spans, batch 3: two full batches + the flushed remainder
	if n != 3 {
		t.Fatalf("trace posts = %d, want 3", n)
	}
	if got := len(r.spans()); got != 7 {
		t.Fatalf("spans = %d", got)
	}
	x.shutdown(time.Second)
	if x.sent.Load() != 7 || x.dropped.Load() != 0 {
		t.Errorf("sent=%d dropped=%d", x.sent.Load(), x.dropped.Load())
	}
}

func TestOtlpTimerFlush(t *testing.T) {
	r, srv := newOtRecv(t, 200)
	x := otlpStart(OtlpConf{Endpoint: srv.URL, BatchSize: 100, FlushMS: 20})
	defer x.shutdown(time.Second)
	otTestLog(t, x).emit("request.end", map[string]any{"model": "m"}, false)
	for i := 0; i < 100 && len(r.spans()) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if len(r.spans()) != 1 {
		t.Fatal("partial batch not flushed by the timer")
	}
}

// otWarnBuf is a goroutine-safe sink for the exporter's warning line.
type otWarnBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *otWarnBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
func (w *otWarnBuf) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

func TestOtlpEndpointDownDropsWithOneWarning(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	warn := &otWarnBuf{}
	old := otlpWarnOut
	otlpWarnOut = warn
	defer func() { otlpWarnOut = old }()

	x := otlpStart(OtlpConf{Endpoint: url, BatchSize: 2, FlushMS: 60000})
	l := otTestLog(t, x)
	start := time.Now()
	for i := 0; i < 10; i++ {
		l.emit("request.end", map[string]any{"model": "m", "latency_ms": 1}, false)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("emit blocked for %v", d)
	}
	x.shutdown(5 * time.Second)
	if x.dropped.Load() != 10 || x.sent.Load() != 0 {
		t.Errorf("dropped=%d sent=%d", x.dropped.Load(), x.sent.Load())
	}
	if n := strings.Count(warn.String(), "\n"); n != 1 || !strings.Contains(warn.String(), "otlp") {
		t.Errorf("warnings = %q", warn.String())
	}
}

func TestOtlpServerErrorDrops(t *testing.T) {
	r, srv := newOtRecv(t, 503)
	warn := &otWarnBuf{}
	old := otlpWarnOut
	otlpWarnOut = warn
	defer func() { otlpWarnOut = old }()
	x := otlpStart(OtlpConf{Endpoint: srv.URL, BatchSize: 1})
	l := otTestLog(t, x)
	l.emit("request.end", map[string]any{"model": "m"}, false)
	l.emit("request.end", map[string]any{"model": "m"}, false)
	x.shutdown(2 * time.Second)
	r.mu.Lock()
	posts := r.posts
	r.mu.Unlock()
	if x.dropped.Load() != 2 || posts < 2 || strings.Count(warn.String(), "\n") != 1 {
		t.Errorf("dropped=%d posts=%d warn=%q", x.dropped.Load(), posts, warn.String())
	}
}

func TestOtlpFullQueueDropsWithoutBlocking(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	x := otlpStart(OtlpConf{Endpoint: srv.URL, BatchSize: 1, QueueSize: 8})
	start := time.Now()
	for i := 0; i < 500; i++ {
		x.feed(map[string]any{"kind": "request.end", "model": "m", "ts": "2026-01-01T00:00:00.000Z"})
	}
	if time.Since(start) > time.Second {
		t.Fatal("feed blocked on a stuck endpoint")
	}
	if x.dropped.Load() == 0 {
		t.Error("expected drops when the queue is full")
	}
}

func TestOtlpOffByDefault(t *testing.T) {
	otlpMu.Lock()
	otlpSet, otlpCur = OtlpConf{}, nil
	otlpMu.Unlock()
	if otlpGet() != nil {
		t.Fatal("exporter exists without endpoint")
	}
	var x *otlpExp
	x.feed(map[string]any{}) // nil-safe
	x.shutdown(time.Millisecond)
}

// End to end: a real headless run exports through the obLog hook.
func TestOtlpHeadlessRunExports(t *testing.T) {
	r, srv := newOtRecv(t, 200)
	cfg, root, _ := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo oi"})}}, UsageIn: 10, UsageOut: 2},
		fakeResp{Text: "pronto", UsageIn: 12, UsageOut: 3},
	))
	if err := otlpConfigure(&Config{}, srv.URL); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otlpConfigure(&Config{}, ""); otlpShutdown() }()
	code, res := obRun(t, cfg, root, "diga oi", pfOpts{timeout: time.Minute})
	if code != 0 {
		t.Fatalf("exit %d: %v", code, res)
	}
	names := map[string]int{}
	for _, s := range r.spans() {
		names[strings.SplitN(s["name"].(string), " ", 2)[0]]++
	}
	if names["session"] != 1 || names["turn"] != 1 || names["request"] != 2 || names["tool"] != 1 {
		t.Fatalf("spans = %v", names)
	}
}

func TestOtlpConfigure(t *testing.T) {
	t.Setenv("JAZZ_OTLP_HEADERS", "X-A=1, Authorization=Bearer z")
	cfg := &Config{Otlp: OtlpConf{Endpoint: "http://cfg:4318", Headers: map[string]string{"K": "v"}}}
	if err := otlpConfigure(cfg, "http://flag:4318"); err != nil {
		t.Fatal(err)
	}
	if otlpSet.Endpoint != "http://flag:4318" || otlpSet.Headers["Authorization"] != "Bearer z" || otlpSet.Headers["K"] != "v" || otlpSet.Headers["X-A"] != "1" {
		t.Errorf("settings: %+v", otlpSet)
	}
	if err := otlpConfigure(cfg, "ftp://x"); err == nil {
		t.Error("bad scheme accepted")
	}
	t.Setenv("JAZZ_OTLP_HEADERS", "")
	_ = otlpConfigure(&Config{}, "")
	if otlpSet.Endpoint != "" {
		t.Error("should be off")
	}
}
