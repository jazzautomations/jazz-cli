//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pvSetClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	now := at
	old := rlNow
	rlNow = func() time.Time { return now }
	t.Cleanup(func() { rlNow = old })
	return &now
}

// ---------------------------------------------------------------------------
// time helpers
// ---------------------------------------------------------------------------

func TestOmniPacificOffset(t *testing.T) {
	cases := []struct {
		at   string
		want time.Duration
	}{
		{"2026-01-15T12:00:00Z", -8 * time.Hour},
		{"2026-03-08T09:59:59Z", -8 * time.Hour}, // 2nd Sunday of March 2026 is the 8th
		{"2026-03-08T10:00:00Z", -7 * time.Hour},
		{"2026-07-01T00:00:00Z", -7 * time.Hour},
		{"2026-11-01T08:59:59Z", -7 * time.Hour}, // 1st Sunday of November 2026 is the 1st
		{"2026-11-01T09:00:00Z", -8 * time.Hour},
		{"2027-03-14T10:00:00Z", -7 * time.Hour},
	}
	for _, c := range cases {
		at, _ := time.Parse(time.RFC3339, c.at)
		if got := pacificOffset(at); got != c.want {
			t.Errorf("%s: got %v want %v", c.at, got, c.want)
		}
	}
	// PT day key + end: 2026-10-19 06:00 UTC is still the 18th in California (PDT, -7h)
	at, _ := time.Parse(time.RFC3339, "2026-10-19T06:00:00Z")
	day, end := rlDay("PT", at)
	if day != "2026-10-18" || !end.Equal(time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("PT day %s end %v", day, end)
	}
	day, end = rlDay("UTC", at)
	if day != "2026-10-19" || !end.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("UTC day %s end %v", day, end)
	}
}

func TestOmniParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	h := func(kv ...string) http.Header {
		hh := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			hh.Set(kv[i], kv[i+1])
		}
		return hh
	}
	cases := []struct {
		name string
		h    http.Header
		body string
		want time.Duration
	}{
		{"retry-after-ms", h("retry-after-ms", "1500"), "", 1500 * time.Millisecond},
		{"retry-after seconds", h("Retry-After", "7"), "", 7 * time.Second},
		{"retry-after date", h("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat)), "", 90 * time.Second},
		{"reset epoch ms", h("X-RateLimit-Reset", "1792411230000"), "", time.UnixMilli(1792411230000).Sub(now)},
		{"reset epoch s", h("X-RateLimit-Reset", "1792411230"), "", time.Unix(1792411230, 0).Sub(now)},
		{"reset relative s", h("X-RateLimit-Reset", "30"), "", 30 * time.Second},
		{"groq duration", h("x-ratelimit-reset-requests", "2m59.56s"), "", 2*time.Minute + 59560*time.Millisecond},
		{"openrouter metadata", nil, `{"error":{"code":429,"metadata":{"headers":{"X-RateLimit-Reset":"1792411260000"}}}}`, time.UnixMilli(1792411260000).Sub(now)},
		{"gemini retryDelay", nil, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"13s"}]}}`, 13 * time.Second},
		{"nothing", nil, `{"error":"x"}`, 0},
		{"past date", h("Retry-After", now.Add(-time.Minute).Format(http.TimeFormat)), "", 0},
	}
	for _, c := range cases {
		if got := pvRetryAfter(c.h, c.body, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestOmniIsDaily(t *testing.T) {
	yes := []struct {
		st   int
		body string
	}{
		{429, `{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits"}}`},
		{429, `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`},
		{429, `Rate limit reached for model openai/gpt-oss-120b on requests per day (RPD): Limit 1000`},
		{429, `tokens per day (TPD): Limit 200000, Used 199000`},
		{402, `{"error":{"type":"quota_for_entity_exceeded"}}`},
		{429, `you have used up your daily free allocation of 10,000 neurons`},
		{429, `{"error":{"code":"insufficient_quota"}}`},
	}
	for _, c := range yes {
		if !pvIsDaily(c.st, c.body) {
			t.Errorf("want daily: %d %s", c.st, c.body)
		}
	}
	no := []struct {
		st   int
		body string
	}{
		{429, `Rate limit reached for requests per minute (RPM)`},
		{429, `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"violations":[{"quotaId":"GenerateRequestsPerMinutePerProject"}]}]}}`},
		{400, `per day`},
		{500, `free-models-per-day`},
	}
	for _, c := range no {
		if pvIsDaily(c.st, c.body) {
			t.Errorf("want not daily: %d %s", c.st, c.body)
		}
	}
}

func TestOmniClassifyAndRetryDelay(t *testing.T) {
	cases := []struct {
		err  error
		kind string
		zero bool
	}{
		{&APIError{Status: 429, Body: "slow down"}, "rate", false},
		{&APIError{Status: 429, Body: "free-models-per-day", Daily: true}, "daily", true},
		{&APIError{Status: 413, Body: "Request too large for model in organization on tokens per minute (TPM): Limit 8000, Requested 9512"}, "toobig", true},
		{&APIError{Status: 400, Body: "Request too large: Limit 8000, Requested 9000"}, "toobig", true},
		{&APIError{Status: 402, Body: "insufficient credits"}, "credit", true},
		{&APIError{Status: 401, Body: "bad key"}, "auth", true},
		{&APIError{Status: 403, Body: `{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}`}, "freetier", true},
		{&APIError{Status: 404, Body: "No endpoints found that support tool use"}, "notools", true},
		{&APIError{Status: 400, Body: "this model does not support tools"}, "notools", true},
		{&APIError{Status: 404, Body: "not here"}, "gone", true},
		{&APIError{Status: 400, Body: "The model `x` does not exist"}, "gone", true},
		{&APIError{Status: 400, Body: "maximum context length is 8192 tokens"}, "overflow", true},
		{&APIError{Status: 503, Body: "overloaded"}, "transient", false},
		{&APIError{Status: 0, Body: "dial tcp: refused"}, "transient", false},
		{errStreamIdle, "transient", false},
		{context.Canceled, "cancel", false},
	}
	for _, c := range cases {
		if k := pvClassify(c.err); k != c.kind {
			t.Errorf("%v: kind %q want %q", c.err, k, c.kind)
		}
		d := RetryDelay(c.err, 0)
		if (d == 0) != c.zero && c.kind != "cancel" {
			t.Errorf("%v: RetryDelay=%v zero=%v", c.err, d, c.zero)
		}
	}
	// jitter ±20% around 2s, 6s, 15s; nothing after the 3rd retry
	for i, base := range []time.Duration{2 * time.Second, 6 * time.Second, 15 * time.Second} {
		for n := 0; n < 20; n++ {
			d := RetryDelay(&APIError{Status: 500}, i)
			if d < base*8/10 || d > base*12/10 {
				t.Fatalf("attempt %d: %v outside ±20%% of %v", i, d, base)
			}
		}
	}
	if RetryDelay(&APIError{Status: 500}, 3) != 0 {
		t.Fatal("4th attempt must move on")
	}
	if d := RetryDelay(&APIError{Status: 429, RetryAfter: 5 * time.Minute}, 0); d != 60*time.Second {
		t.Fatalf("RetryAfter cap: %v", d)
	}
	if d := RetryDelay(&APIError{Status: 429, RetryAfter: 3 * time.Second}, 0); d != 3*time.Second {
		t.Fatalf("RetryAfter: %v", d)
	}
}

// ---------------------------------------------------------------------------
// limiter
// ---------------------------------------------------------------------------

func TestOmniLimiterRPMAndRPD(t *testing.T) {
	c := pvTestCfg(t)
	c.Providers["t"] = &ProviderConf{Type: "openai", BaseURL: "http://x", NoKey: true, Limits: &Limits{RPM: 2, RPD: 3}}
	now := pvSetClock(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	ref := "t/m"
	for i := 0; i < 2; i++ {
		if st := c.pvState(ref, 10); !st.Ready {
			t.Fatalf("request %d not ready: %+v", i, st)
		}
		c.rlBegin(ref, 10)
		c.rlEnd(ref, 10, Usage{In: 5, Out: 5}, nil)
	}
	st := c.pvState(ref, 10)
	if st.Ready || st.Dead || st.Wait <= 0 || st.Wait > time.Minute {
		t.Fatalf("RPM should make it wait: %+v", st)
	}
	*now = now.Add(61 * time.Second)
	if st := c.pvState(ref, 10); !st.Ready {
		t.Fatalf("ready after a minute: %+v", st)
	}
	c.rlBegin(ref, 10)
	c.rlEnd(ref, 10, Usage{}, nil)
	st = c.pvState(ref, 10)
	if !st.Dead || !strings.Contains(st.Why, "diário") {
		t.Fatalf("RPD 3 reached: %+v", st)
	}
	if !st.Until.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dead until next UTC midnight, got %v", st.Until)
	}
	if b := c.rlBudget(ref); b != "3/3 hoje" {
		t.Fatalf("budget %q", b)
	}
	*now = time.Date(2026, 10, 20, 0, 0, 1, 0, time.UTC)
	if st := c.pvState(ref, 10); !st.Ready {
		t.Fatalf("new day resets: %+v", st)
	}
}

func TestOmniLimiterUnlimitedRefsLeaveNoState(t *testing.T) {
	c := pvTestCfg(t)
	c.Providers["t"] = &ProviderConf{Type: "openai", BaseURL: "http://x", NoKey: true}
	c.rlBegin("t/m", 100)
	c.rlEnd("t/m", 100, Usage{In: 1}, nil)
	FlushRateLimits()
	if _, err := os.Stat(rlPath()); err == nil {
		b, _ := os.ReadFile(rlPath())
		var f struct {
			Buckets map[string]any `json:"buckets"`
		}
		_ = json.Unmarshal(b, &f)
		if len(f.Buckets) != 0 {
			t.Fatalf("unlimited ref left state: %s", b)
		}
	}
}

func TestOmniLimiterErrorKinds(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("GROQ_API_KEY", "gsk_test")
	now := pvSetClock(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))

	// rate: cooldown ladder 20s, 60s; success resets strikes
	ref := "groq/openai/gpt-oss-120b"
	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 429, Body: "slow"})
	if st := c.pvState(ref, 0); st.Ready || st.Wait != 20*time.Second {
		t.Fatalf("first 429 → 20s: %+v", st)
	}
	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 429, Body: "slow", RetryAfter: 30 * time.Second})
	if st := c.pvState(ref, 0); st.Wait != 60*time.Second {
		t.Fatalf("second 429 → 60s: %+v", st)
	}
	if s, last, _ := rlInfo(ref); s != 2 || !strings.Contains(last, "429") {
		t.Fatalf("strikes %d last %q", s, last)
	}
	*now = now.Add(2 * time.Minute)
	c.rlEnd(ref, 0, Usage{}, nil)
	if s, last, _ := rlInfo(ref); s != 0 || last != "" {
		t.Fatalf("success must reset: %d %q", s, last)
	}

	// toobig: learn MaxReq from "Limit N", no cooldown
	c.rlEnd(ref, 9000, Usage{}, &APIError{Status: 413, Body: "Request too large for model: Limit 8000, Requested 9512"})
	if _, _, mr := rlInfo(ref); mr != 7200 {
		t.Fatalf("MaxReq %d", mr)
	}
	if st := c.pvState(ref, 7300); !st.TooBig {
		t.Fatalf("7300 > 7200 must be too big: %+v", st)
	}
	if st := c.pvState(ref, 5000); !st.Ready {
		t.Fatalf("small request still fine: %+v", st)
	}

	// daily: dead until reset, visible through ExhaustedUntil
	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 429, Body: "requests per day (RPD): Limit 1000", Daily: true})
	until, ok := ExhaustedUntil(ref)
	if !ok || !until.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("exhausted until %v %v", until, ok)
	}

	// auth: refused key → credencial recusada; a new key clears it
	ref2 := "groq/qwen/qwen3.8-27b"
	c.rlEnd(ref2, 0, Usage{}, &APIError{Status: 401, Body: "invalid api key"})
	if st := c.pvState(ref2, 0); !st.Dead || !strings.Contains(st.Why, "credencial") {
		t.Fatalf("auth: %+v", st)
	}
	t.Setenv("GROQ_API_KEY", "gsk_other")
	if st := c.pvState(ref2, 0); st.Dead {
		t.Fatalf("new key must clear auth state: %+v", st)
	}

	// notools: persisted as text tools in the models cache
	c.rlEnd("groq/x", 0, Usage{}, &APIError{Status: 400, Body: "tool calling is not supported for this model"})
	if ms := c.ModelSettings("groq/x"); ms.Tools != "text" {
		t.Fatalf("notools → %+v", ms)
	}
	// gone: dead for a day for that model only
	c.rlEnd("groq/y", 0, Usage{}, &APIError{Status: 404, Body: "model not found"})
	if st := c.pvState("groq/y", 0); !st.Dead {
		t.Fatalf("gone: %+v", st)
	}
	// overflow: learned window
	c.Models["groq/z"] = &ModelConf{Context: 131072}
	c.rlEnd("groq/z", 50000, Usage{}, &APIError{Status: 400, Body: "This model's maximum context length is 32768 tokens"})
	if w, _ := c.Limits("groq/z"); w != 45000 {
		t.Fatalf("learned ctx %d", w)
	}
}

func TestOmniLimiterPersistsAndMerges(t *testing.T) {
	c := pvTestCfg(t)
	pvSetClock(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	MarkExhausted("openrouter/a:free", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	// another process wrote a bucket meanwhile
	b, err := os.ReadFile(rlPath())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Buckets map[string]*rlBucket `json:"buckets"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	f.Buckets["nvidia"] = &rlBucket{DeadUntil: time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC).UnixMilli()}
	nb, _ := json.Marshal(f)
	if err := os.WriteFile(rlPath(), nb, 0o600); err != nil {
		t.Fatal(err)
	}
	c.rlEnd("groq/m", 0, Usage{}, &APIError{Status: 401, Body: "x"}) // urgent save → merge
	omniRL.mu.Lock()
	omniRL.path = "" // force a reload from disk
	omniRL.mu.Unlock()
	if _, ok := ExhaustedUntil("openrouter/a:free"); !ok {
		t.Fatal("own state lost")
	}
	if _, ok := ExhaustedUntil("nvidia/z-ai/glm-5.3"); !ok {
		t.Fatal("other process state lost in merge")
	}
	if fi, err := os.Stat(rlPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ratelimit.json perms %v %v", fi, err)
	}
	// corrupt file → start empty, never crash
	if err := os.WriteFile(rlPath(), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	omniRL.mu.Lock()
	omniRL.path = ""
	omniRL.mu.Unlock()
	if _, ok := ExhaustedUntil("openrouter/a:free"); ok {
		t.Fatal("corrupt file should read as empty")
	}
}

func TestOmniLimiterHeaders(t *testing.T) {
	c := pvTestCfg(t)
	pvSetClock(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "0")
	h.Set("x-ratelimit-reset-requests", "2h0m0s")
	c.rlObserve("groq/openai/gpt-oss-120b", "https://api.groq.com/openai/v1", h)
	st := c.pvState("groq/openai/gpt-oss-120b", 0)
	if !st.Dead || !st.Until.Equal(time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("groq remaining-requests is per DAY: %+v", st)
	}
	h = http.Header{}
	h.Set("x-ratelimit-remaining-requests", "0")
	h.Set("x-ratelimit-reset-requests", "20")
	h.Set("x-ratelimit-remaining-requests-day", "7")
	c.rlObserve("sambanova/DeepSeek-V3.1", "https://api.sambanova.ai/v1", h)
	st = c.pvState("sambanova/DeepSeek-V3.1", 0)
	if st.Dead || st.Ready || st.Wait != 20*time.Second {
		t.Fatalf("sambanova minute cooldown: %+v", st)
	}
	if b := c.rlBudget("sambanova/DeepSeek-V3.1"); !strings.Contains(b, "/20 hoje") {
		t.Fatalf("budget %q", b)
	}
}

// ---------------------------------------------------------------------------
// router
// ---------------------------------------------------------------------------

func TestOmniCandidatesPlain(t *testing.T) {
	c := pvTestCfg(t)
	c.Fallback = []string{"groq/a", "x/b", "groq/a"}
	got := c.Candidates("x/b", "anthropic/c")
	want := []string{"x/b", "anthropic/c", "groq/a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
	MarkExhausted("anthropic/c", time.Now().Add(time.Hour))
	if got := c.Candidates("x/b", "anthropic/c"); strings.Join(got, ",") != "x/b,groq/a" {
		t.Fatalf("exhausted not skipped: %v", got)
	}
	MarkExhausted("x/b", time.Now().Add(time.Hour))
	MarkExhausted("groq/a", time.Now().Add(time.Hour))
	if got := c.Candidates("x/b", "anthropic/c"); len(got) != 3 {
		t.Fatalf("all exhausted → unfiltered, got %v", got)
	}
}

func TestOmniImplicitFreeChain(t *testing.T) {
	c := pvTestCfg(t)
	got := c.Candidates("openrouter/foo:free")
	if strings.Join(got, ",") != "openrouter/foo:free,kilo/poolside/laguna-s-2.1:free,kilo/minimax/minimax-m3.1-flash:free,kilo/cohere/north-mini-code:free,kilo/nvidia/nemotron-3-super-120b-a12b:free,kilo/nvidia/nemotron-3.5-lightning:free" {
		t.Fatalf("got %v", got)
	}
	t.Setenv("GROQ_API_KEY", "gsk_x")
	got = c.Candidates("openrouter/foo:free")
	if strings.Join(got, ",") != "openrouter/foo:free,kilo/poolside/laguna-s-2.1:free,kilo/minimax/minimax-m3.1-flash:free,kilo/cohere/north-mini-code:free,kilo/nvidia/nemotron-3-super-120b-a12b:free,kilo/nvidia/nemotron-3.5-lightning:free,groq/openai/gpt-oss-120b" {
		t.Fatalf("got %v", got)
	}
	// a paid primary gets no implicit chain; an explicit fallback replaces it
	if got := c.Candidates("anthropic/x"); len(got) != 1 {
		t.Fatalf("paid primary: %v", got)
	}
	c.Fallback = []string{"groq/y"}
	if got := c.Candidates("openrouter/foo:free"); strings.Join(got, ",") != "openrouter/foo:free,groq/y" {
		t.Fatalf("fallback: %v", got)
	}
}

func TestOmniRankFreeFirst(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("NVIDIA_API_KEY", "nvapi-x")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	t.Setenv("DEEPSEEK_API_KEY", "sk-x")
	got := c.CandidatesFor(Need{Tools: true}, "omni/coder")
	if len(got) == 0 || got[0] != "nvidia/z-ai/glm-5.3" {
		t.Fatalf("best free coder first: %v", got)
	}
	idx := map[string]int{}
	for i, r := range got {
		idx[r] = i
	}
	for _, r := range got {
		if isOmniRef(r) {
			t.Fatalf("omni ref leaked: %v", got)
		}
	}
	if _, ok := idx["kilo/poolside/laguna-s-2.1:free"]; !ok {
		t.Fatalf("keyless kilo missing: %v", got)
	}
	// paid only in an emergency: after every free one; cheapest paid first
	ds, an := idx["deepseek/deepseek-flash"], idx["anthropic/claude-sonnet-5-5"]
	if ds == 0 || an == 0 || ds > an || ds < idx["kilo/poolside/laguna-s-2.1:free"] {
		t.Fatalf("paid order wrong: %v", got)
	}
	// never: no paid at all
	c.Omni.Paid = "never"
	for _, r := range c.CandidatesFor(Need{}, "omni/coder") {
		if r == "deepseek/deepseek-flash" || r == "anthropic/claude-sonnet-5-5" {
			t.Fatalf("paid with paid=never: %v", r)
		}
	}
	c.Omni.Paid = ""
	// no_training drops nvidia/kilo/openrouter/gemini/mistral
	c.Omni.NoTraining = true
	for _, r := range c.CandidatesFor(Need{}, "omni/coder") {
		if p, _ := SplitModel(r); c.Providers[p].Train {
			t.Fatalf("training provider kept: %v", r)
		}
	}
	c.Omni.NoTraining = false
	// exclude glob
	c.Omni.Exclude = []string{"nvidia"}
	for _, r := range c.CandidatesFor(Need{}, "omni/coder") {
		if strings.HasPrefix(r, "nvidia/") {
			t.Fatalf("excluded nvidia kept: %v", r)
		}
	}
	c.Omni.Exclude = nil
	// a dead free candidate drops out; a cooling one goes behind the ready ones
	MarkExhausted("nvidia/z-ai/glm-5.3", time.Now().Add(time.Hour))
	got = c.CandidatesFor(Need{}, "omni/coder")
	if got[0] == "nvidia/z-ai/glm-5.3" || got[0] != "nvidia/moonshotai/kimi-k2.6" {
		t.Fatalf("dead not skipped: %v", got)
	}
	// big request: models with a small window are filtered out
	for _, r := range c.CandidatesFor(Need{Tokens: 300000}, "omni/long") {
		if w, _ := c.Limits(r); w < 300000 {
			t.Fatalf("%s window %d < need", r, w)
		}
	}
	// sticky preference
	got = c.CandidatesFor(Need{Prefer: "nvidia/deepseek-ai/deepseek-v4.1-flash"}, "omni/coder")
	if got[0] != "nvidia/deepseek-ai/deepseek-v4.1-flash" {
		t.Fatalf("prefer bonus: %v", got)
	}
}

func TestOmniFastTierAndMaxReq(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("GROQ_API_KEY", "gsk_x")
	got := c.CandidatesFor(Need{Tokens: 3000}, "omni/fast")
	if len(got) == 0 || got[0] != "groq/openai/gpt-oss-120b" {
		t.Fatalf("groq first for small fast requests: %v", got)
	}
	for _, r := range c.CandidatesFor(Need{Tokens: 9000}, "omni/fast") {
		if strings.HasPrefix(r, "groq/") {
			t.Fatalf("groq cannot take 9000 tokens (MaxReq 7500): %v", r)
		}
	}
}

func TestOmniEnabledAppendsFreeChainAndUserTiers(t *testing.T) {
	c := pvTestCfg(t)
	c.Omni.Enabled = true
	got := c.Candidates("anthropic/x")
	if got[0] != "anthropic/x" || len(got) < 2 {
		t.Fatalf("omni/free appended: %v", got)
	}
	if c.PickDefaultModel() != "omni/coder" {
		t.Fatal("omni enabled → omni/coder")
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or-x")
	c.Omni.Tiers = map[string][]string{"smart": {"openrouter/*:free", "kilo/kilo-auto/free"}}
	c.Omni.Strategy = "ordered"
	got = c.CandidatesFor(Need{}, "omni/smart")
	if len(got) < 2 || !strings.HasPrefix(got[0], "openrouter/") || got[len(got)-1] != "kilo/kilo-auto/free" {
		t.Fatalf("user tier order: %v", got)
	}
}

func TestOmniWeightedStablePerTurn(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("NVIDIA_API_KEY", "nvapi-x")
	c.Omni.Strategy = "weighted"
	a := c.CandidatesFor(Need{}, "omni/coder")
	b := c.CandidatesFor(Need{}, "omni/coder")
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("weighted order must be stable within a turn:\n%v\n%v", a, b)
	}
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		OmniBeginTurn()
		seen[c.CandidatesFor(Need{}, "omni/coder")[0]] = true
	}
	if len(seen) < 2 {
		t.Fatalf("weighted never spreads: %v", seen)
	}
}

func TestOmniAllDeadMessage(t *testing.T) {
	c := pvTestCfg(t)
	c.Omni.Exclude = []string{"openrouter", "vercel", "sambanova", "gemini"}
	for _, m := range append([]string{kiloDefaultModel}, kiloFastFree...) {
		MarkExhausted("kilo/"+m, time.Now().Add(30*time.Minute))
	}
	msg := OmniExhaustedMessage(c, "omni/free")
	if !strings.Contains(msg, "esgotados") || !strings.Contains(msg, "kilo em") {
		t.Fatalf("msg %q", msg)
	}
	if got := c.CandidatesFor(Need{}, "omni/free"); len(got) == 0 {
		t.Fatal("never empty: dead candidates are still returned")
	}
	if !c.HasCreds("omni") {
		t.Fatal("omni has creds while kilo exists")
	}
}

func TestOmniResolveAndLimits(t *testing.T) {
	c := pvTestCfg(t)
	for in, want := range map[string]string{"omni": "omni/coder", "omni/fast": "omni/fast"} {
		if got, err := c.ResolveModel(in); err != nil || got != want {
			t.Errorf("%s → %s %v", in, got, err)
		}
	}
	if _, err := c.ResolveModel("omni/nope"); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if _, err := c.NewProvider("omni"); err == nil {
		t.Fatal("omni is virtual")
	}
	if w, _ := c.Limits("omni/free"); w <= 0 {
		t.Fatal("omni limits")
	}
	for _, n := range c.ProviderNames() {
		if n == "omni" {
			t.Fatal("omni must not be a real provider entry")
		}
	}
}

func TestOmniStatusAndSetup(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("GROQ_API_KEY", "gsk_x")
	MarkExhausted("groq/openai/gpt-oss-120b", time.Now().Add(2*time.Hour))
	rows := OmniStatus(c)
	if len(rows) < 10 || rows[0].Provider != "kilo" {
		t.Fatalf("rows %d first %+v", len(rows), rows[0])
	}
	var groq, gem *OmniRow
	for i := range rows {
		switch rows[i].Provider + "/" + rows[i].Model {
		case "groq/openai/gpt-oss-120b":
			groq = &rows[i]
		case "gemini/gemini-3.8-flash":
			gem = &rows[i]
		}
	}
	if groq == nil || !groq.HasKey || !strings.HasPrefix(groq.State, "esgotado") {
		t.Fatalf("groq row %+v", groq)
	}
	if gem == nil || gem.HasKey || gem.State != "sem chave" || gem.Budget != "0/20 hoje" {
		t.Fatalf("gemini row %+v", gem)
	}
	if s := FormatOmniStatus(rows); !strings.Contains(s, "provedor") || !strings.Contains(s, "groq") {
		t.Fatalf("table %s", s)
	}
	steps := OmniSetupSteps(c)
	if steps[0].Name != "kilo" || !steps[0].NoKey || !steps[0].Done {
		t.Fatalf("first step %+v", steps[0])
	}
	if steps[1].Name != "openrouter" || steps[1].Signup == "" || steps[1].KeyEnv != "OPENROUTER_API_KEY" || steps[1].Note == "" {
		t.Fatalf("second step %+v", steps[1])
	}
	var cf OmniStep
	for _, s := range steps {
		if s.Name == "cloudflare" {
			cf = s
		}
	}
	if !cf.Account || cf.AccountEnv != "CLOUDFLARE_ACCOUNT_ID" {
		t.Fatalf("cloudflare step %+v", cf)
	}
	if w := KeyFormatWarning(c, "groq", "sk-123"); w == "" {
		t.Fatal("groq key should start with gsk_")
	}
	if w := KeyFormatWarning(c, "groq", "gsk_abc"); w != "" {
		t.Fatal(w)
	}
	if !strings.Contains(OmniPrivacyNotice, "treinar") {
		t.Fatal("privacy notice")
	}
}

func TestOmniProbeKey(t *testing.T) {
	c := pvTestCfg(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"bad key"}`)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	c.Providers["p1"] = &ProviderConf{Type: "openai", BaseURL: srv.URL, KeyEnv: "PV_P1_KEY"}
	c.Providers["p2"] = &ProviderConf{Type: "openai", BaseURL: srv.URL, KeyEnv: "PV_P1_KEY", Probe: "chat1", DefaultModel: "m"}
	t.Setenv("PV_P1_KEY", "bad")
	if err := OmniProbeKey(context.Background(), c, "p1"); err == nil || !strings.Contains(err.Error(), "recusou") {
		t.Fatalf("bad key: %v", err)
	}
	t.Setenv("PV_P1_KEY", "good")
	if err := OmniProbeKey(context.Background(), c, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := OmniProbeKey(context.Background(), c, "p2"); err != nil {
		t.Fatal(err)
	}
	if paths[len(paths)-1] != "POST /chat/completions" || paths[0] != "GET /models" {
		t.Fatalf("paths %v", paths)
	}
}

func TestOmniRefreshOpenRouterQuota(t *testing.T) {
	c := pvTestCfg(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/key":
			io.WriteString(w, `{"data":{"free_model_daily_requests":{"used":998,"limit":1000,"remaining":2}}}`)
		case "/api/v1/models":
			io.WriteString(w, `{"data":[{"id":"new/coder:free","context_length":200000,"supported_parameters":["tools"],"pricing":{"prompt":"0","completion":"0"}},
				{"id":"paid/x","context_length":200000,"supported_parameters":["tools"],"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c.Providers["openrouter"].BaseURL = srv.URL + "/api/v1"
	t.Setenv("OPENROUTER_API_KEY", "sk-or-x")
	pvRefreshMu.Lock()
	pvORQuotaAt = time.Time{}
	pvRefreshMu.Unlock()
	OmniRefresh(context.Background(), c)
	if b := c.rlBudget("openrouter/any:free"); b != "998/1000 hoje" {
		t.Fatalf("budget %q", b)
	}
	// discovered free model joins omni/free with Q 50; paid price known for G21
	found := false
	live, _ := c.omniCollect("free", Need{Tools: true})
	for _, cd := range live {
		found = found || (cd.ref == "openrouter/new/coder:free" && cd.q == 50)
	}
	if !found {
		t.Fatal("discovered free model missing from omni/free")
	}
	if ms := c.omniPool()["openrouter/paid/x"]; len(ms.Tiers) != 0 {
		t.Fatal("paid catalog entries are not discovered")
	}
	if in, out, ok := c.Price("openrouter/paid/x"); !ok || in != 0.000001 || out != 0.000002 {
		t.Fatalf("price %v %v %v", in, out, ok)
	}
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

func TestOmniConfigSaveSkipsPresetsAndOverride(t *testing.T) {
	c := pvTestCfg(t)
	c.ModeOverride = "yolo"
	c.Providers["mine"] = &ProviderConf{Type: "openai", BaseURL: "http://10.0.0.2:8080/v1", NoKey: true}
	c.Models["mine/m"] = &ModelConf{Context: 32768}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(configDir(), "config.json"))
	s := string(b)
	if strings.Contains(s, "yolo") || strings.Contains(s, "kilo") || strings.Contains(s, "groq") || strings.Contains(s, "nvidia/z-ai") {
		t.Fatalf("presets/override saved: %s", s)
	}
	if !strings.Contains(s, `"mine"`) || !strings.Contains(s, `"mine/m"`) {
		t.Fatalf("user entries lost: %s", s)
	}
	c2, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c2.EffMode() != "auto" || c2.Providers["kilo"] == nil || c2.Models["nvidia/z-ai/glm-5.3"] == nil || c2.Models["mine/m"].Context != 32768 {
		t.Fatalf("reload: mode %s", c2.EffMode())
	}
	c2.SetMode("plan")
	if c2.EffMode() != "plan" || c2.Mode != "" || c2.BaseMode() != "auto" {
		t.Fatal("plan is session-only")
	}
	c2.SetMode("ask")
	if c2.EffMode() != "ask" || c2.Mode != "ask" || c2.ModeOverride != "" {
		t.Fatal("/mode ask")
	}
}

func TestOmniConfigMigratesZenAndPlan(t *testing.T) {
	pvTestHome(t)
	old := `{"model":"zen/deepseek-v4.1-flash-free","mode":"plan","providers":{"zen":{"type":"openai","base_url":"https://opencode.ai/zen/v1","key_env":"OPENCODE_API_KEY","default_model":"deepseek-v4.1-flash-free","no_key":true},
		"openrouter":{"type":"openai","base_url":"https://openrouter.ai/api/v1","key_env":"OPENROUTER_API_KEY","default_model":"openrouter/free"}}}`
	if err := os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["zen"].NoKey || c.HasCreds("zen") {
		t.Fatal("zen free tier is closed: needs a key now")
	}
	if c.Mode != "" || c.EffMode() != "auto" {
		t.Fatalf("plan loaded as %q", c.Mode)
	}
	or := c.Providers["openrouter"]
	if or.DefaultModel != "openrouter/free" || or.Router != "openrouter" || !or.FreeIDs || or.Limits == nil {
		t.Fatalf("user entry not completed from preset: %+v", or)
	}
	if got := c.PickDefaultModel(); got != "kilo/poolside/laguna-s-2.1:free" {
		t.Fatalf("keyless default %q", got)
	}
}

func TestOmniAuthCorruptNeverOverwritten(t *testing.T) {
	pvTestHome(t)
	if err := SaveCred("groq", &Cred{Type: "api", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveCred("openrouter", &Cred{Type: "api", Key: "k2"}); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(authPath())
	bak, err := os.ReadFile(authPath() + ".bak")
	if err != nil || !strings.Contains(string(bak), "k1") {
		t.Fatalf(".bak missing: %v %s", err, bak)
	}
	if err := os.WriteFile(authPath(), []byte(`{"groq":{"type":"api","key":"k1"`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = SaveCred("chatgpt", &Cred{Type: "oauth", Refresh: "r"})
	if err == nil || !strings.Contains(err.Error(), "corrompido") {
		t.Fatalf("corrupt auth.json must not be overwritten: %v", err)
	}
	if b, _ := os.ReadFile(authPath()); !strings.HasPrefix(string(b), `{"groq":{"type":"api","key":"k1"`) || len(b) == len(good) {
		t.Fatalf("file changed: %s", b)
	}
	if len(LoadAuth()) != 0 {
		t.Fatal("corrupt file reads as empty")
	}
	if w := AuthWarning(); !strings.Contains(w, "corrompido") {
		t.Fatalf("warning %q", w)
	}
	if AuthWarning() != "" {
		t.Fatal("warning only once")
	}
	if err := os.WriteFile(authPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveCred("x", &Cred{Type: "api", Key: "y"}); err == nil {
		t.Fatal("empty auth.json must not be overwritten either")
	}
}

func TestOmniLimitsAndCache(t *testing.T) {
	c := pvTestCfg(t)
	c.ContextWindow, c.MaxTokens = 128000, 16000
	c.Models["l/small"] = &ModelConf{Context: 8192}
	if w, m := c.Limits("l/small"); w != 8192 || m != 2048 {
		t.Fatalf("small window clamp: %d %d", w, m)
	}
	if w, m := c.Limits("l/unknown"); w != 128000 || m != 16000 {
		t.Fatalf("globals: %d %d", w, m)
	}
	c.Models["l/big"] = &ModelConf{MaxTokens: 50000}
	if _, m := c.Limits("l/big"); m != 50000 {
		t.Fatalf("explicit MaxTokens wins: %d", m)
	}
	pvCacheStoreList("l", []ModelInfo{{ID: "cached", Ctx: 64000, MaxOut: 4000, Tools: false}})
	if w, m := c.Limits("l/cached"); w != 64000 || m != 4000 {
		t.Fatalf("cache: %d %d", w, m)
	}
	if ms := c.ModelSettings("l/cached"); ms.Tools != "text" || ms.Context != 64000 {
		t.Fatalf("cache settings %+v", ms)
	}
	no := false
	pvCacheStoreList("l", []ModelInfo{{ID: "blind", Tools: true, Images: &no}})
	if c.SupportsImages("l/blind") || !c.SupportsImages("l/cached") {
		t.Fatal("images")
	}
	c.MarkTextTools("l/blind")
	if c.ModelSettings("l/blind").Tools != "text" {
		t.Fatal("MarkTextTools")
	}
	// survives a reload from disk
	pvCacheMu.Lock()
	pvCache = nil
	pvCacheMu.Unlock()
	if c.ModelSettings("l/blind").Tools != "text" {
		t.Fatal("models cache not persisted")
	}
	// a refreshed catalog keeps the learned text mode
	pvCacheStoreList("l", []ModelInfo{{ID: "blind", Tools: true}})
	if c.ModelSettings("l/blind").Tools != "text" {
		t.Fatal("learned text mode lost on refresh")
	}
	if _, _, ok := c.Price("kilo/kilo-auto/free"); !ok {
		t.Fatal("free model price is known (0)")
	}
	if in, _, ok := c.Price("deepseek/deepseek-flash"); !ok || in <= 0 {
		t.Fatal("deepseek price")
	}
}

func TestOmniDetectWindowLocal(t *testing.T) {
	c := pvTestCfg(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			io.WriteString(w, `{"model_info":{"qwen3.context_length":131072},"parameters":"temperature 0.7\nnum_ctx 4096"}`)
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"qwen3"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c.Providers["ol"] = &ProviderConf{Type: "openai", BaseURL: srv.URL + "/v1", NoKey: true, Local: true, Ollama: true}
	w, err := DetectWindow(context.Background(), c, "ol/qwen3")
	if err != nil || w != 4096 {
		t.Fatalf("ollama num_ctx: %d %v", w, err)
	}
	if !strings.Contains(c.ModelWarning("ol/qwen3"), "OLLAMA_CONTEXT_LENGTH") {
		t.Fatalf("warning %q", c.ModelWarning("ol/qwen3"))
	}
	if win, _ := c.Limits("ol/qwen3"); win != 4096 {
		t.Fatalf("cached window %d", win)
	}

	lc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			io.WriteString(w, `{"default_generation_settings":{"n_ctx":32768}}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer lc.Close()
	c.Providers["lc"] = &ProviderConf{Type: "openai", BaseURL: lc.URL + "/v1", NoKey: true, Local: true}
	if w, err := DetectWindow(context.Background(), c, "lc/any"); err != nil || w != 32768 {
		t.Fatalf("llama.cpp n_ctx: %d %v", w, err)
	}
}

func TestOmniAddProviderAndReachability(t *testing.T) {
	c := pvTestCfg(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Team") != "jazz" {
			w.WriteHeader(403)
			return
		}
		io.WriteString(w, `{"data":[{"id":"ornith-1.5","max_context_length":65536}]}`)
	}))
	t.Setenv("PV_TEAM", "jazz")
	ms, err := c.AddProvider(context.Background(), "mac", ProviderConf{BaseURL: srv.URL + "/v1/", Headers: map[string]string{"X-Team": "${PV_TEAM}"}})
	if err != nil || len(ms) != 1 || ms[0].Ctx != 65536 {
		t.Fatalf("add: %v %+v", err, ms)
	}
	pc := c.Providers["mac"]
	if !pc.Local || !pc.NoKey || pc.DefaultModel != "ornith-1.5" || pc.BaseURL != srv.URL+"/v1" {
		t.Fatalf("provider %+v", pc)
	}
	c2, _ := LoadConfig()
	if c2.Providers["mac"] == nil {
		t.Fatal("not saved")
	}
	if !c.HasCreds("mac") {
		t.Fatal("reachable local server has creds")
	}
	if w, _ := c.Limits("mac/ornith-1.5"); w != 65536 {
		t.Fatalf("window from catalog %d", w)
	}
	if got := c.PickDefaultModel(); got != "kilo/poolside/laguna-s-2.1:free" {
		t.Fatalf("pick %s", got) // user-added locals are not auto-picked
	}
	srv.Close()
	pvReachMu.Lock()
	pvReachC = map[string]pvReach{}
	pvReachMu.Unlock()
	start := time.Now()
	if c.HasCreds("mac") {
		t.Fatal("dead local server has no creds")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("probe too slow")
	}
	if _, err := c.AddProvider(context.Background(), "Bad/Name", ProviderConf{BaseURL: "http://x"}); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := c.AddProvider(context.Background(), "gone", ProviderConf{BaseURL: srv.URL}); err == nil || c.Providers["gone"] != nil {
		t.Fatal("failed probe must not keep the provider")
	}
	if ok, _, err := ProbeProvider(context.Background(), c, "mac"); ok || err == nil {
		t.Fatalf("probe of a dead server: %v %v", ok, err)
	}
}

func TestOmniNewProviderWiring(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "cf")
	if _, err := c.NewProvider("cloudflare"); err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Fatalf("missing account: %v", err)
	}
	if c.HasCreds("cloudflare") {
		t.Fatal("no account id → no creds")
	}
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acc123")
	p, err := c.NewProvider("cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if op := p.(*OpenAIProvider); op.BaseURL != "https://api.cloudflare.com/client/v4/accounts/acc123/ai/v1" {
		t.Fatalf("base %s", op.BaseURL)
	}
	t.Setenv("GOOGLE_API_KEY", "AIza-alt")
	p, err = c.NewProvider("gemini")
	if err != nil {
		t.Fatal(err)
	}
	gp := p.(*OpenAIProvider)
	if gp.Key != "AIza-alt" || !gp.quirk("gemini-thought-sig") || !gp.quirk("effort") {
		t.Fatalf("gemini %+v", gp)
	}
	if p, err := c.NewProvider("kilo"); err != nil || p.(*OpenAIProvider).Router != "kilo" {
		t.Fatalf("kilo keyless: %v", err)
	}
	if _, err := c.NewProvider("groq"); err == nil {
		t.Fatal("groq without key")
	}
}

func TestOmniFlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.lock")
	unlock, err := pvFlock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pvFlock(p, 200*time.Millisecond); err == nil {
		t.Fatal("second lock must wait/fail while held")
	}
	unlock()
	u2, err := pvFlock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	u2()
}

func TestOmniWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := writeFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != "two" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s %v", b, fi.Mode())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left: %v", ents)
	}
	if err := writeFileAtomic(filepath.Join(dir, "nope", "x"), nil, 0o600); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: %v", err)
	}
}
