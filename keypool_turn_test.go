//go:build !windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// kpDead is what the provider answers for one key of the pool.
type kpDead struct {
	status int
	body   string
}

// newKpHarness is newHarness with provider "fake" holding a key pool and a
// proxy in front of the fake server that answers per API key: keys in dead get
// that status/body, the others go through. sent returns the key of every
// request that reached the provider, in order (recReq keeps no headers).
func newKpHarness(t *testing.T, keys []string, dead map[string]kpDead, script ...fakeResp) (h *harness, sent func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	var target *url.URL
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, k)
		mu.Unlock()
		if d, ok := dead[k]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(d.status)
			_, _ = io.WriteString(w, d.body)
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(px.Close)
	h = newHarness(t, seq(script...), func(c *Config) {
		c.Mode, c.NoVerifyNudge = "yolo", true
		var err error
		if target, err = url.Parse(c.Providers["fake"].BaseURL); err != nil {
			t.Fatal(err)
		}
		c.Providers["fake"] = &ProviderConf{Type: "openai", BaseURL: px.URL}
		if err := SaveCred("fake", &Cred{Type: "api", Key: keys[0], Keys: keys[1:]}); err != nil {
			t.Fatal(err)
		}
	})
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

const (
	kpRefused = `{"error":{"message":"No auth credentials found","code":401}}`
	kpPerDay  = `{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429}}`
)

// The provider refuses a key in the middle of a turn: the same model answers
// with the next key of the pool, and the refused key is never sent again.
func TestKeyPoolTurnRotatesOnRefusedKey(t *testing.T) {
	k1, k2 := "sk-pool-refused-0000000001", "sk-pool-refused-0000000002"
	h, sent := newKpHarness(t, []string{k1, k2}, map[string]kpDead{k1: {401, kpRefused}},
		fakeResp{Text: "pronto"}, fakeResp{Text: "de novo"})
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("the pool had a live key, the turn must not fail: %v", err)
	}
	if got := strings.Join(sent(), " "); got != k1+" "+k2 {
		t.Fatalf("want one request per key, refused first: %q", got)
	}
	if n := h.fs.countModel("m1"); n != 1 {
		t.Errorf("the model must stay the same and answer once, got %d answers", n)
	}
	if !strings.Contains(strings.Join(h.ui.notices, "\n"), "próxima do pool") {
		t.Errorf("the user must be told the key changed: %q", h.ui.notices)
	}
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != 3 || got[2] != k2 {
		t.Errorf("a refused key must not be sent again: %q", got)
	}
}

// OpenRouter's daily cap on one key: the model is not marked exhausted while
// the pool has another key (MarkExhausted would kill it for every key).
func TestKeyPoolTurnRotatesOnDailyCap(t *testing.T) {
	k1, k2 := "sk-pool-perday-00000000001", "sk-pool-perday-00000000002"
	h, sent := newKpHarness(t, []string{k1, k2}, map[string]kpDead{k1: {429, kpPerDay}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("the pool had a live key, the turn must not fail: %v", err)
	}
	if got := strings.Join(sent(), " "); got != k1+" "+k2 {
		t.Fatalf("want one request per key, exhausted first: %q", got)
	}
	if until, dead := ExhaustedUntil("fake/m1"); dead {
		t.Errorf("one exhausted key must not exhaust the model for the pool (until %v)", until)
	}
	if st := h.cfg.pvState("fake/m1", 10); !st.Ready {
		t.Errorf("the model must stay ready on the next key: %+v", st)
	}
}

// Every key exhausted: one request per key, then the turn fails as before.
func TestKeyPoolTurnStopsWhenPoolIsDead(t *testing.T) {
	keys := []string{"sk-pool-alldead-0000000001", "sk-pool-alldead-0000000002", "sk-pool-alldead-0000000003"}
	dead := map[string]kpDead{}
	for _, k := range keys {
		dead[k] = kpDead{429, kpPerDay}
	}
	h, sent := newKpHarness(t, keys, dead, fakeResp{Text: "nunca"})
	if err := h.run(t, "oi"); err == nil {
		t.Error("with the whole pool exhausted the turn must fail")
	}
	if got := sent(); len(got) != len(keys) {
		t.Errorf("want exactly one request per key, got %d: %q", len(got), got)
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Errorf("no request may get through, got %d", n)
	}
}

// A big pool with every key but the last out of quota: the turn still answers,
// each key is tried once, and the next turn goes straight to the live key.
func TestKeyPoolTurnWalksABigPool(t *testing.T) {
	const n = 50
	keys := make([]string, n)
	dead := map[string]kpDead{}
	for i := range keys {
		keys[i] = fmt.Sprintf("sk-pool-big-%014d", i)
		if i < n-1 {
			dead[keys[i]] = kpDead{429, kpPerDay}
		}
	}
	h, sent := newKpHarness(t, keys, dead, fakeResp{Text: "pronto"}, fakeResp{Text: "de novo"})
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("the last key was alive, the turn must not fail: %v", err)
	}
	got := sent()
	if len(got) != n || got[n-1] != keys[n-1] {
		t.Fatalf("want %d requests ending on the live key, got %d", n, len(got))
	}
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != n+1 || got[n] != keys[n-1] {
		t.Errorf("exhausted keys must be skipped on the next turn: %d requests", len(got))
	}
	for _, m := range append(h.ui.notices, h.ui.warns...) {
		if strings.Contains(m, "sk-pool-big-") {
			t.Errorf("a key leaked into a message: %q", m)
		}
	}
}

// `jazz key <prov> < keys.txt` has no pool size: a thousand keys come in whole
// (64 KiB of input used to be the cut), and oversized input is refused.
func TestKeyPoolReadsAThousandKeys(t *testing.T) {
	const n = 1000
	var in strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&in, "sk-or-v1-%064d\n", i)
	}
	keys, err := pkReadKeys(strings.NewReader(in.String()), pkKeysMax)
	if err != nil || len(keys) != n {
		t.Fatalf("want %d keys, got %d (err %v)", n, len(keys), err)
	}
	if want := fmt.Sprintf("sk-or-v1-%064d", n-1); keys[n-1] != want {
		t.Errorf("the last key came in cut: %d bytes", len(keys[n-1]))
	}
	c := pvPoolCfg(t)
	if _, err := storeKeys(c, "or", keys); err != nil {
		t.Fatal(err)
	}
	if got := c.APIKeys("or"); len(got) != n || got[0] != keys[0] || got[n-1] != keys[n-1] {
		t.Errorf("the stored pool must hold all %d keys in order, got %d", n, len(got))
	}
	if got, err := pkReadKeys(strings.NewReader("a,b;c d\te\r\nf"), pkKeysMax); err != nil || strings.Join(got, "|") != "a|b|c|d|e|f" {
		t.Errorf("separators: %q (err %v)", got, err)
	}
	if got, err := pkReadKeys(strings.NewReader("sk-aaaa\nsk-bbbb\n"), 10); err == nil {
		t.Errorf("input over the limit must be an error, not a cut: %q", got)
	}
}

func TestKeyPoolNextBounds(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-bound-1", "sk-or-bound-2", "sk-or-bound-3", "sk-or-bound-4")
	ref := "or/m"
	for _, tc := range []struct {
		kind  string
		tried int
		want  bool
	}{
		{"auth", 0, true}, {"daily", 2, true}, {"credit", 2, true},
		{"daily", 3, false}, // every key of the pool already had its turn
		{"rate", 1, true},
		{"rate", poolRateTries, false}, // a busy upstream answers 429 to every key
		{"transient", 0, false}, {"gone", 0, false}, {"overflow", 0, false}, {"", 0, false},
	} {
		if got := c.poolNext(ref, tc.kind, 10, tc.tried); got != tc.want {
			t.Errorf("poolNext(%q, tried %d) = %v, want %v", tc.kind, tc.tried, got, tc.want)
		}
	}
	if single := pvPoolCfg(t, "sk-or-only-1"); single.poolNext(ref, "auth", 10, 0) {
		t.Error("a single key has nothing to rotate to")
	}
}
