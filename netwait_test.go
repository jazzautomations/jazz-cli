package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBgNetClass(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 0, Body: "dial tcp: lookup api.x: no such host"}, true},
		{&APIError{Status: 0, Body: "stream interrompido: connection reset"}, true},
		{&APIError{Status: 0, Body: "linha SSE grande demais"}, false},
		{&APIError{Status: 503, Body: "overloaded"}, false},
		{&APIError{Status: 429, Body: "rate"}, false},
		{errFirstByte, true},
		{errStreamIdle, true},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{&net.OpError{Op: "dial", Err: errors.New("network is unreachable")}, true},
		{errors.New("boom"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := bgNetClass(c.err); got != c.want {
			t.Errorf("bgNetClass(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestBgHostPortAndLocal(t *testing.T) {
	hp := map[string]string{
		"https://api.anthropic.com":      "api.anthropic.com:443",
		"http://10.0.0.5:8088/v1":        "10.0.0.5:8088",
		"http://localhost:11434/v1":      "localhost:11434",
		"https://x.example/{ACCOUNT}/v1": "x.example:443",
		"https://{ACCOUNT}.example/v1":   "",
		"":                               "",
	}
	for in, want := range hp {
		if got := bgHostPort(in); got != want {
			t.Errorf("bgHostPort(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"localhost:1": true, "127.0.0.1:80": true, "192.168.1.9:8080": true, "10.0.0.5:1": true, "box.local:1": true, "1.1.1.1:443": false, "api.groq.com:443": false} {
		if got := bgLocalHost(in); got != want {
			t.Errorf("bgLocalHost(%q) = %v, want %v", in, got, want)
		}
	}
}

// bgNetStub replaces the probe, the backoff and the local skip for a test.
type bgNetStub struct {
	mu    sync.Mutex
	calls [][]string
	up    func(n int) bool
}

func (s *bgNetStub) install(t *testing.T) {
	t.Helper()
	p, b, l := bgNetProbe, bgNetBackoff, bgNetSkipLocal
	bgNetProbe = func(_ context.Context, addrs []string) bool {
		s.mu.Lock()
		n := len(s.calls)
		s.calls = append(s.calls, addrs)
		s.mu.Unlock()
		return s.up(n)
	}
	bgNetBackoff = []time.Duration{5 * time.Millisecond}
	bgNetSkipLocal = false
	t.Cleanup(func() { bgNetProbe, bgNetBackoff, bgNetSkipLocal = p, b, l })
}

func (s *bgNetStub) n() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }

// dropFirst serves fs, but the first n connections are closed without a reply
// (a transport error: status 0, like a WiFi drop).
func dropFirst(t *testing.T, fs *fakeServer, n int32) (*httptest.Server, *int32) {
	var seen int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&seen, 1) <= n {
			hj, _ := w.(http.Hijacker)
			c, _, err := hj.Hijack()
			if err == nil {
				c.Close()
			}
			return
		}
		fs.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestNetWaitOutageRetriesSameCandidate(t *testing.T) {
	st := &bgNetStub{up: func(n int) bool { return n >= 3 }} // down for 3 probes
	st.install(t)
	fs := newFakeServer(t, seq(fakeResp{Text: "answer after the outage"}))
	srv, seen := dropFirst(t, fs, 1)
	h := newHarness(t, seq(fakeResp{Text: "fallback must not answer"}), func(c *Config) {
		c.Providers["fake"].BaseURL = srv.URL
		c.Providers["other"] = &ProviderConf{Type: "openai", BaseURL: "https://api.other.example/v1", NoKey: true}
		c.Fallback = []string{"other/m2"}
	})
	stop, err := h.ag.Run(context.Background(), "hi")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if lastMsg(h.ag.Msgs).Content != "answer after the outage" {
		t.Errorf("final = %q", lastMsg(h.ag.Msgs).Content)
	}
	if atomic.LoadInt32(seen) != 2 || len(h.fs.requests()) != 0 {
		t.Errorf("main candidate tried %d times, fallback server %d; want 2 and 0", atomic.LoadInt32(seen), len(h.fs.requests()))
	}
	if st.n() != 4 {
		t.Errorf("probes = %d, want 4 (1 detection + 3 while waiting)", st.n())
	}
	addrs := strings.Join(st.calls[0], " ")
	if !strings.Contains(addrs, bgNetAnchor) || !strings.Contains(addrs, "api.other.example:443") {
		t.Errorf("probe must cover the anchor and the other candidates' hosts: %v", st.calls[0])
	}
	if !anyContains(h.ui.warns, "sem conexão com a internet") || !anyContains(h.ui.notices, "a rede voltou") {
		t.Errorf("outage messages missing: warns=%v notices=%v", h.ui.warns, h.ui.notices)
	}
	if len(h.ag.stalled) != 0 {
		t.Errorf("an outage must not mark models as stalled: %v", h.ag.stalled)
	}
}

func TestNetWaitNetworkUpFallsBack(t *testing.T) {
	st := &bgNetStub{up: func(int) bool { return true }}
	st.install(t)
	fs := newFakeServer(t, seq(fakeResp{Text: "never"}))
	srv, _ := dropFirst(t, fs, 1000) // the main provider is broken, the network is fine
	h := newHarness(t, seq(fakeResp{Text: "from the fallback"}), func(c *Config) { c.Providers["fake"].BaseURL = srv.URL })
	h.cfg.Providers["fb"] = &ProviderConf{Type: "openai", BaseURL: h.fs.URL(), NoKey: true}
	h.cfg.Fallback = []string{"fb/m2"}
	h.ag.retryDelay = func(error, int) time.Duration { return 0 }
	stop, err := h.ag.Run(context.Background(), "hi")
	if err != nil || stop != StopDone || lastMsg(h.ag.Msgs).Content != "from the fallback" {
		t.Fatalf("stop=%s err=%v final=%q", stop, err, lastMsg(h.ag.Msgs).Content)
	}
	if st.n() != 1 {
		t.Errorf("one probe, then trusted for bgNetFresh: %d", st.n())
	}
	if anyContains(h.ui.warns, "sem conexão") {
		t.Error("no outage when the probe connects")
	}
}

func TestNetWaitStopsAtDeadline(t *testing.T) {
	st := &bgNetStub{up: func(int) bool { return false }}
	st.install(t)
	fs := newFakeServer(t, seq(fakeResp{Text: "never"}))
	srv, _ := dropFirst(t, fs, 1000)
	h := newHarness(t, seq(fakeResp{Text: "never"}), func(c *Config) { c.Providers["fake"].BaseURL = srv.URL })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	stop, err := h.ag.Run(ctx, "hi")
	if stop != StopInterrupted || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("stop=%s err=%v, want the deadline (headless maps it to timeout)", stop, err)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestNetWaitOffAndLocal(t *testing.T) {
	st := &bgNetStub{up: func(int) bool { return false }}
	st.install(t)
	h := newHarness(t, seq(fakeResp{Text: "x"}), nil)
	err := &APIError{Status: 0, Body: "dial tcp: connection refused"}
	t.Setenv("JAZZ_NET_WAIT", "off")
	if h.ag.bgNetWait(context.Background(), "fake/m1", err, nil) || st.n() != 0 {
		t.Error("net_wait off must not probe or wait")
	}
	t.Setenv("JAZZ_NET_WAIT", "")
	bgNetSkipLocal = true
	h.cfg.Providers["fake"].BaseURL = "http://127.0.0.1:9/v1"
	if h.ag.bgNetWait(context.Background(), "fake/m1", err, nil) || st.n() != 0 {
		t.Error("a local server being down is not an outage")
	}
	h.cfg.Providers["fake"].BaseURL = "https://api.fake.example/v1"
	if h.ag.bgNetWait(context.Background(), "fake/m1", &APIError{Status: 500}, nil) || st.n() != 0 {
		t.Error("an HTTP error is not a network error")
	}
	t.Setenv("HTTPS_PROXY", "proxy.corp:3128")
	addrs := h.ag.bgNetAddrs("fake/m1", []string{"fake/m1", "ollama/llama"})
	if fmt.Sprint(addrs) != "[proxy.corp:3128]" { // every request goes through the proxy
		t.Errorf("addrs = %v", addrs)
	}
}
