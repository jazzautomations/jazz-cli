package main

// otlp.go: OTLP/HTTP JSON export of the event stream (obs.go), off by default.
//
//   - Enabled by --otlp-endpoint URL or config otlp.endpoint. No protobuf, no
//     third-party dependency: the JSON encoding of OTLP is built by hand.
//   - session = trace, turn = span, request/tool = child spans, a subagent's
//     turn = a span under the tool that spawned it. Guard decisions, approvals,
//     checkpoints etc. are span events on the tool in flight or on the turn.
//   - Metrics (tokens, cost, requests, tool calls, latency histograms) go to
//     <endpoint>/v1/metrics as delta sums/histograms.
//   - Asynchronous and batched: obLog.emit only does a non-blocking channel
//     send. A full queue or an unreachable endpoint drops the data, counts it
//     (dropped) and prints ONE warning line per process.
//   - Only what the event stream already holds is exported (hashes and sizes,
//     never prompt or file contents); string attributes go through redactSecrets.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OtlpConf is the "otlp" block of config.json.
type OtlpConf struct {
	Endpoint  string            `json:"endpoint,omitempty"`   // base URL of an OTLP/HTTP receiver (empty = off)
	Headers   map[string]string `json:"headers,omitempty"`    // e.g. {"Authorization": "Bearer ..."}
	Metrics   *bool             `json:"metrics,omitempty"`    // export metrics too (default true)
	BatchSize int               `json:"batch_size,omitempty"` // spans per request (default 64)
	FlushMS   int               `json:"flush_ms,omitempty"`   // max wait before a partial batch is sent (default 2000)
	QueueSize int               `json:"queue_size,omitempty"` // events buffered before dropping (default 4096)
}

var (
	otlpMu      sync.Mutex
	otlpSet     OtlpConf // settings chosen at startup
	otlpCur     *otlpExp
	otlpWarnOut io.Writer = os.Stderr
)

// otlpConfigure picks the settings (flag wins over config; JAZZ_OTLP_HEADERS
// adds headers). Returns an error for a bad endpoint.
func otlpConfigure(cfg *Config, flagEndpoint string) error {
	c := OtlpConf{}
	if cfg != nil {
		c = cfg.Otlp
	}
	if flagEndpoint != "" {
		c.Endpoint = flagEndpoint
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("otlp: endpoint inválido %q (use http://host:4318)", c.Endpoint)
		}
	}
	if h := os.Getenv("JAZZ_OTLP_HEADERS"); h != "" {
		m := map[string]string{}
		for k, v := range c.Headers {
			m[k] = v
		}
		for _, kv := range strings.Split(h, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
				m[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		c.Headers = m
	}
	otlpMu.Lock()
	otlpSet = c
	otlpMu.Unlock()
	return nil
}

// otlpGet returns the process exporter, creating it on first use (nil = off).
func otlpGet() *otlpExp {
	otlpMu.Lock()
	defer otlpMu.Unlock()
	if otlpCur == nil && otlpSet.Endpoint != "" {
		otlpCur = otlpStart(otlpSet)
	}
	return otlpCur
}

// otlpShutdown flushes what is pending (bounded) and stops the exporter.
func otlpShutdown() {
	otlpMu.Lock()
	x := otlpCur
	otlpCur = nil
	otlpMu.Unlock()
	if x != nil {
		x.shutdown(3 * time.Second)
	}
}

// ---------- exporter ----------

type otlpMsg struct {
	ev   map[string]any
	done chan struct{} // flush request (ev nil); closed when sent
	stop bool
}

type otlpExp struct {
	conf    OtlpConf
	base    string
	client  *http.Client
	ch      chan otlpMsg
	stopped atomic.Bool
	wdone   chan struct{}
	warned  atomic.Bool
	dropped atomic.Int64
	sent    atomic.Int64 // spans delivered

	// worker-only state
	res   []otAttr
	root  *otSpan
	turns [2]*otSpan // [0] main, [1] subagent
	tools map[string]*otSpan
	spans []*otSpan
	met   otMetrics
}

func otlpStart(c OtlpConf) *otlpExp {
	if c.BatchSize <= 0 {
		c.BatchSize = 64
	}
	if c.FlushMS <= 0 {
		c.FlushMS = 2000
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 4096
	}
	base := strings.TrimRight(c.Endpoint, "/")
	base = strings.TrimSuffix(base, "/v1/traces")
	x := &otlpExp{conf: c, base: base, client: &http.Client{Timeout: 5 * time.Second},
		ch: make(chan otlpMsg, c.QueueSize), wdone: make(chan struct{}), tools: map[string]*otSpan{}}
	x.met.reset()
	go x.run()
	return x
}

// feed hands one event to the worker; never blocks.
func (x *otlpExp) feed(ev map[string]any) {
	if x == nil {
		return
	}
	if x.stopped.Load() {
		x.dropped.Add(1)
		return
	}
	select {
	case x.ch <- otlpMsg{ev: ev}:
	default:
		x.dropped.Add(1)
	}
}

// flush asks the worker to send everything pending and waits up to d.
func (x *otlpExp) flush(d time.Duration) {
	if x == nil || x.stopped.Load() {
		return
	}
	done := make(chan struct{})
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case x.ch <- otlpMsg{done: done}:
	case <-t.C:
		return
	}
	select {
	case <-done:
	case <-t.C:
	}
}

func (x *otlpExp) shutdown(d time.Duration) {
	if x == nil || !x.stopped.CompareAndSwap(false, true) {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case x.ch <- otlpMsg{stop: true}:
	case <-t.C:
		return
	}
	select {
	case <-x.wdone:
	case <-t.C:
	}
}

func (x *otlpExp) run() {
	defer close(x.wdone)
	tick := time.NewTicker(time.Duration(x.conf.FlushMS) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case m := <-x.ch:
			switch {
			case m.stop:
				x.finishRoot()
				x.send()
				return
			case m.done != nil:
				x.send()
				close(m.done)
			default:
				x.handle(m.ev)
				if len(x.spans) >= x.conf.BatchSize {
					x.send()
				}
			}
		case <-tick.C:
			x.send()
		}
	}
}

// ---------- event -> span ----------

type otAttr struct {
	k string
	v any // string | int64 | float64 | bool
}

type otEvt struct {
	t     time.Time
	name  string
	attrs []otAttr
}

type otSpan struct {
	trace, id, parent string
	name              string
	kind              int
	start, end        time.Time
	attrs             []otAttr
	events            []otEvt
	errMsg            string
	isErr             bool
	sub               bool
}

func otNum(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	}
	return 0
}

func otStr(e map[string]any, k string) string { s, _ := e[k].(string); return s }
func otBool(e map[string]any, k string) bool  { b, _ := e[k].(bool); return b }

func otID(s string, n int) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:n])
}

func otTime(e map[string]any) time.Time {
	t, err := time.Parse(time.RFC3339Nano, otStr(e, "ts"))
	if err != nil {
		return time.Now()
	}
	return t
}

// otSkip are fields that become span structure, not attributes.
var otSkip = map[string]bool{"v": true, "seq": true, "ts": true, "sid": true, "kind": true, "sub": true}

func otFields(e map[string]any, prefix string, skip ...string) []otAttr {
	var out []otAttr
	for k, v := range e {
		if otSkip[k] {
			continue
		}
		drop := false
		for _, s := range skip {
			if s == k {
				drop = true
			}
		}
		if drop {
			continue
		}
		switch t := v.(type) {
		case string:
			out = append(out, otAttr{prefix + k, redactSecrets(t)})
		case int, int64, int32:
			out = append(out, otAttr{prefix + k, int64(otNum(t))})
		case float64:
			if t == float64(int64(t)) {
				out = append(out, otAttr{prefix + k, int64(t)})
			} else {
				out = append(out, otAttr{prefix + k, t})
			}
		case bool:
			out = append(out, otAttr{prefix + k, t})
		}
	}
	return out
}

func (x *otlpExp) spanID(e map[string]any) string {
	return otID("span:"+otStr(e, "sid")+":"+strconv.FormatInt(int64(otNum(e["seq"])), 10), 8)
}

func (x *otlpExp) newSpan(e map[string]any, name string, kind int, start, end time.Time, parent string) *otSpan {
	return &otSpan{trace: otID("trace:"+otStr(e, "sid"), 16), id: x.spanID(e), parent: parent, name: name, kind: kind,
		start: start, end: end, sub: otBool(e, "sub")}
}

func (x *otlpExp) emitSpan(s *otSpan) {
	if s != nil {
		x.spans = append(x.spans, s)
	}
}

func subIdx(sub bool) int {
	if sub {
		return 1
	}
	return 0
}

func (x *otlpExp) parentFor(sub bool) string {
	if t := x.turns[subIdx(sub)]; t != nil {
		return t.id
	}
	if x.root != nil {
		return x.root.id
	}
	return ""
}

// addEvent attaches a span event to the tool in flight with the same name,
// else to the current turn, else to the session root.
func (x *otlpExp) addEvent(e map[string]any, name string, attrs []otAttr) {
	ev := otEvt{t: otTime(e), name: name, attrs: attrs}
	sub := otBool(e, "sub")
	if tn := otStr(e, "tool"); tn != "" {
		var best *otSpan
		for _, s := range x.tools {
			if s.name == "tool "+tn && s.sub == sub && (best == nil || s.start.After(best.start)) {
				best = s
			}
		}
		if best != nil {
			best.events = append(best.events, ev)
			return
		}
	}
	if t := x.turns[subIdx(sub)]; t != nil {
		t.events = append(t.events, ev)
	} else if x.root != nil {
		x.root.events = append(x.root.events, ev)
	}
}

func (x *otlpExp) handle(e map[string]any) {
	kind := otStr(e, "kind")
	ts := otTime(e)
	sub := otBool(e, "sub")
	i := subIdx(sub)
	dur := func(k string) time.Duration { return time.Duration(otNum(e[k]) * float64(time.Millisecond)) }
	turnName := "turn"
	if sub {
		turnName = "subagent"
	}
	switch kind {
	case "session.start":
		x.finishRoot()
		x.res = []otAttr{{"service.name", "jazz-cli"}, {"service.version", otStr(e, "version")},
			{"os.type", otStr(e, "os")}, {"host.arch", otStr(e, "arch")}, {"process.pid", int64(otNum(e["pid"]))},
			{"session.id", otStr(e, "sid")}}
		x.root = x.newSpan(e, "session", 1, ts, ts, "")
		x.root.attrs = otFields(e, "jazz.", "version", "os", "arch", "pid")
	case "turn.start":
		parent := ""
		if sub {
			// a subagent runs inside a tool of the main agent
			var best *otSpan
			for _, s := range x.tools {
				if !s.sub && (best == nil || s.start.After(best.start)) {
					best = s
				}
			}
			if best != nil {
				parent = best.id
			}
		}
		if parent == "" {
			parent = x.parentFor(false)
		}
		x.turns[i] = x.newSpan(e, turnName, 1, ts, ts, parent)
		x.turns[i].attrs = otFields(e, "jazz.")
	case "turn.end":
		t := x.turns[i]
		if t == nil {
			t = x.newSpan(e, turnName, 1, ts, ts, x.parentFor(false))
		}
		t.start, t.end = ts.Add(-dur("dur_ms")), ts
		t.attrs = append(t.attrs, otFields(e, "jazz.")...)
		x.emitSpan(t)
		x.turns[i] = nil
	case "request.end":
		s := x.newSpan(e, "request "+otStr(e, "model"), 3, ts.Add(-dur("latency_ms")), ts, x.parentFor(sub))
		s.attrs = append(otFields(e, "jazz.", "model", "provider", "in", "out", "cache_read", "cache_write", "latency_ms", "finish", "error", "payload"),
			otAttr{"gen_ai.request.model", otStr(e, "model")}, otAttr{"gen_ai.system", otStr(e, "provider")},
			otAttr{"gen_ai.usage.input_tokens", int64(otNum(e["in"]))}, otAttr{"gen_ai.usage.output_tokens", int64(otNum(e["out"]))},
			otAttr{"jazz.tokens.cache_read", int64(otNum(e["cache_read"]))}, otAttr{"jazz.tokens.cache_write", int64(otNum(e["cache_write"]))},
			otAttr{"jazz.latency_ms", int64(otNum(e["latency_ms"]))}, otAttr{"gen_ai.response.finish_reasons", otStr(e, "finish")})
		if er := otStr(e, "error"); er != "" {
			s.isErr, s.errMsg = true, redactSecrets(er)
		}
		x.emitSpan(s)
		x.met.request(e)
	case "tool.start":
		s := x.newSpan(e, "tool "+otStr(e, "name"), 1, ts, ts, x.parentFor(sub))
		s.attrs = []otAttr{{"jazz.tool.name", otStr(e, "name")}, {"jazz.tool.id", otStr(e, "id")},
			{"jazz.tool.args_sha", otStr(e, "args_sha")}, {"jazz.tool.args_bytes", int64(otNum(e["args_bytes"]))}}
		x.tools[otStr(e, "id")] = s
	case "tool.end":
		id := otStr(e, "id")
		s := x.tools[id]
		delete(x.tools, id)
		if s == nil {
			s = x.newSpan(e, "tool "+otStr(e, "name"), 1, ts, ts, x.parentFor(sub))
			s.attrs = []otAttr{{"jazz.tool.name", otStr(e, "name")}, {"jazz.tool.id", id}}
		}
		s.start, s.end = ts.Add(-dur("dur_ms")), ts
		s.attrs = append(s.attrs, otAttr{"jazz.tool.bytes", int64(otNum(e["bytes"]))},
			otAttr{"jazz.tool.truncated", otBool(e, "truncated")}, otAttr{"jazz.tool.is_error", otBool(e, "is_err")},
			otAttr{"jazz.tool.ran", otBool(e, "ran")}, otAttr{"jazz.tool.denied", otBool(e, "denied")},
			otAttr{"jazz.tool.blocked", otBool(e, "blocked")})
		if otBool(e, "is_err") {
			s.isErr, s.errMsg = true, "tool error"
		}
		x.emitSpan(s)
		x.met.tool(e)
	case "guard", "approval", "checkpoint", "compact", "recover", "deadline.soft", "budget", "skill", "provider":
		name := kind
		if kind == "guard" {
			if g := otStr(e, "guard"); g != "" {
				name = "guard." + g
			} else if g := otStr(e, "src"); g != "" {
				name = "guard." + g
			}
		}
		x.addEvent(e, name, otFields(e, "jazz."))
	case "run.end":
		if x.root != nil {
			x.root.attrs = append(x.root.attrs, otFields(e, "jazz.run.")...)
			x.root.end = ts
		}
		x.finishRoot()
		x.send()
	}
}

// finishRoot closes the session span (once per session.start), first
// closing turns and tools that never ended (crash, kill) so nothing is lost.
func (x *otlpExp) finishRoot() {
	if x.root == nil {
		return
	}
	now := x.root.end
	if !now.After(x.root.start) {
		now = time.Now()
		x.root.end = now
	}
	for i, t := range x.turns {
		if t != nil {
			if t.end.Before(now) {
				t.end = now
			}
			t.attrs = append(t.attrs, otAttr{"jazz.unfinished", true})
			x.emitSpan(t)
			x.turns[i] = nil
		}
	}
	for id, s := range x.tools {
		if s.end.Before(now) {
			s.end = now
		}
		s.attrs = append(s.attrs, otAttr{"jazz.unfinished", true})
		x.emitSpan(s)
		delete(x.tools, id)
	}
	x.emitSpan(x.root)
	x.root = nil
}

// ---------- metrics ----------

var otBounds = []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}

type otHist struct {
	counts []int64
	sum    float64
	n      int64
}

func (h *otHist) add(ms float64) {
	if h.counts == nil {
		h.counts = make([]int64, len(otBounds)+1)
	}
	i := 0
	for i < len(otBounds) && ms > otBounds[i] {
		i++
	}
	h.counts[i]++
	h.sum += ms
	h.n++
}

type otMetrics struct {
	since    time.Time
	tokens   map[string]int64 // "type|model"
	cost     map[string]float64
	requests map[string]int64 // "model|ok/error"
	tools    map[string]int64 // "name|ok/error"
	reqHist  map[string]*otHist
	toolHist map[string]*otHist
}

func (m *otMetrics) reset() {
	*m = otMetrics{since: time.Now(), tokens: map[string]int64{}, cost: map[string]float64{}, requests: map[string]int64{},
		tools: map[string]int64{}, reqHist: map[string]*otHist{}, toolHist: map[string]*otHist{}}
}

func (m *otMetrics) empty() bool { return len(m.requests) == 0 && len(m.tools) == 0 }

func hist(mp map[string]*otHist, k string) *otHist {
	h := mp[k]
	if h == nil {
		h = &otHist{}
		mp[k] = h
	}
	return h
}

func (m *otMetrics) request(e map[string]any) {
	model, st := otStr(e, "model"), "ok"
	if otStr(e, "error") != "" {
		st = "error"
	}
	m.requests[model+"|"+st]++
	hist(m.reqHist, model).add(otNum(e["latency_ms"]))
	if st == "ok" {
		m.tokens["input|"+model] += int64(otNum(e["in"]))
		m.tokens["output|"+model] += int64(otNum(e["out"]))
		m.tokens["cache_read|"+model] += int64(otNum(e["cache_read"]))
		m.tokens["cache_write|"+model] += int64(otNum(e["cache_write"]))
		m.cost[model] += otNum(e["cost"])
	}
}

func (m *otMetrics) tool(e map[string]any) {
	st := "ok"
	if otBool(e, "is_err") {
		st = "error"
	}
	m.tools[otStr(e, "name")+"|"+st]++
	hist(m.toolHist, otStr(e, "name")).add(otNum(e["dur_ms"]))
}

// ---------- JSON encoding ----------

func otVal(v any) map[string]any {
	switch t := v.(type) {
	case string:
		return map[string]any{"stringValue": t}
	case int:
		return map[string]any{"intValue": strconv.Itoa(t)}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(t, 10)}
	case float64:
		return map[string]any{"doubleValue": t}
	case bool:
		return map[string]any{"boolValue": t}
	}
	return map[string]any{"stringValue": fmt.Sprint(v)}
}

func otAttrs(a []otAttr) []map[string]any {
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		if s, ok := x.v.(string); ok && s == "" {
			continue
		}
		out = append(out, map[string]any{"key": x.k, "value": otVal(x.v)})
	}
	return out
}

func otNano(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func (x *otlpExp) resource() map[string]any {
	res := x.res
	if len(res) == 0 {
		res = []otAttr{{"service.name", "jazz-cli"}, {"service.version", version}}
	}
	return map[string]any{"attributes": otAttrs(res)}
}

func otSpanJSON(s *otSpan) map[string]any {
	m := map[string]any{"traceId": s.trace, "spanId": s.id, "name": s.name, "kind": s.kind,
		"startTimeUnixNano": otNano(s.start), "endTimeUnixNano": otNano(s.end), "attributes": otAttrs(s.attrs)}
	if s.parent != "" {
		m["parentSpanId"] = s.parent
	}
	if s.isErr {
		m["status"] = map[string]any{"code": 2, "message": s.errMsg}
	} else {
		m["status"] = map[string]any{"code": 1}
	}
	if len(s.events) > 0 {
		evs := make([]map[string]any, 0, len(s.events))
		for _, e := range s.events {
			evs = append(evs, map[string]any{"timeUnixNano": otNano(e.t), "name": e.name, "attributes": otAttrs(e.attrs)})
		}
		m["events"] = evs
	}
	return m
}

func (x *otlpExp) tracesBody() map[string]any {
	spans := make([]map[string]any, 0, len(x.spans))
	for _, s := range x.spans {
		spans = append(spans, otSpanJSON(s))
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   x.resource(),
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "jazz-cli", "version": version}, "spans": spans}},
	}}}
}

func (x *otlpExp) metricsBody() map[string]any {
	m := &x.met
	t0, t1 := otNano(m.since), otNano(time.Now())
	pt := func(a []otAttr, k string, v any) map[string]any {
		return map[string]any{"attributes": otAttrs(a), "startTimeUnixNano": t0, "timeUnixNano": t1, k: v}
	}
	sum := func(name, unit string, pts []map[string]any) map[string]any {
		return map[string]any{"name": name, "unit": unit, "sum": map[string]any{"dataPoints": pts, "aggregationTemporality": 1, "isMonotonic": true}}
	}
	histo := func(name string, mp map[string]*otHist) map[string]any {
		var pts []map[string]any
		for k, h := range mp {
			bc := make([]string, len(h.counts))
			for i, c := range h.counts {
				bc[i] = strconv.FormatInt(c, 10)
			}
			p := pt([]otAttr{{"name", k}}, "count", strconv.FormatInt(h.n, 10))
			p["sum"], p["bucketCounts"], p["explicitBounds"] = h.sum, bc, otBounds
			pts = append(pts, p)
		}
		return map[string]any{"name": name, "unit": "ms", "histogram": map[string]any{"dataPoints": pts, "aggregationTemporality": 1}}
	}
	var tok, cost, req, tl []map[string]any
	for k, v := range m.tokens {
		ty, model, _ := strings.Cut(k, "|")
		tok = append(tok, pt([]otAttr{{"type", ty}, {"model", model}}, "asInt", strconv.FormatInt(v, 10)))
	}
	for k, v := range m.cost {
		cost = append(cost, pt([]otAttr{{"model", k}}, "asDouble", v))
	}
	for k, v := range m.requests {
		model, st, _ := strings.Cut(k, "|")
		req = append(req, pt([]otAttr{{"model", model}, {"status", st}}, "asInt", strconv.FormatInt(v, 10)))
	}
	for k, v := range m.tools {
		name, st, _ := strings.Cut(k, "|")
		tl = append(tl, pt([]otAttr{{"name", name}, {"status", st}}, "asInt", strconv.FormatInt(v, 10)))
	}
	metrics := []any{sum("jazz.tokens", "{token}", tok), sum("jazz.cost", "USD", cost), sum("jazz.requests", "{request}", req),
		sum("jazz.tool.calls", "{call}", tl), histo("jazz.request.duration", m.reqHist), histo("jazz.tool.duration", m.toolHist)}
	return map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     x.resource(),
		"scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": "jazz-cli", "version": version}, "metrics": metrics}},
	}}}
}

// ---------- sending ----------

// send posts the pending spans (and metrics) and clears them. On failure the
// batch is dropped and counted; the first failure prints one warning.
func (x *otlpExp) send() {
	if n := len(x.spans); n > 0 {
		if err := x.post("/v1/traces", x.tracesBody()); err != nil {
			x.dropped.Add(int64(n))
			x.warn(err)
		} else {
			x.sent.Add(int64(n))
		}
		x.spans = nil
	}
	if !x.met.empty() {
		if x.conf.Metrics == nil || *x.conf.Metrics {
			if err := x.post("/v1/metrics", x.metricsBody()); err != nil {
				x.warn(err)
			}
		}
		x.met.reset()
	}
}

func (x *otlpExp) warn(err error) {
	if x.warned.CompareAndSwap(false, true) {
		fmt.Fprintf(otlpWarnOut, "jazz: otlp: não consegui exportar para %s (%v); descartando telemetria, sem novos avisos\n", x.base, err)
	}
}

func (x *otlpExp) post(path string, body map[string]any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequest("POST", x.base+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "jazz-cli/"+version)
	for k, v := range x.conf.Headers {
		req.Header.Set(k, v)
	}
	resp, err := x.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
