//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"
)

// Provider bodies behind the F1.1 findings (PROV-2). The phrases come from
// the session evidence ("reached its end of life on 2026-10-28", "Please pass
// a valid API key", "does not exist", "'tools' : maximum number of items is
// 128", PROMOTION_MODEL_LIMIT_REACHED + "Sign up for free"); the JSON shape
// around them is each provider's documented error format.
const (
	rcBody410     = `{"status":410,"title":"Gone","detail":"The model 'z-ai/glm-5.3' has reached its end of life on 2026-10-28."}`
	rcBodyGemKey  = `[{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}]}}]`
	rcBody404     = `{"error":{"message":"Model poolside/laguna-s-2.1:free does not exist","code":404}}`
	rcBodyTools   = `{"error":{"message":"'tools' : maximum number of items is 128","type":"invalid_request_error"}}`
	rcBodyPromo   = `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED","message":"Free promotion limit reached. Sign up for free to continue, or come back later."}}`
	rcBody413     = `{"error":{"message":"Request too large for model openai/gpt-oss-120b on tokens per minute (TPM): Limit 8000, Requested 9512","type":"tokens","code":"rate_limit_exceeded"}}`
	rcBodyBadArgs = `{"error":{"message":"Failed to parse tool call arguments as JSON","type":"invalid_request_error","code":"tool_use_failed"}}`
)

func TestPvClassifyRealBodies(t *testing.T) {
	cases := []struct {
		name string
		ae   *APIError
		kind string
	}{
		{"nvidia 410 end of life", &APIError{Status: 410, Body: rcBody410}, "gone"},
		{"400 end of life", &APIError{Status: 400, Body: `{"error":{"message":"model x has been retired"}}`}, "gone"},
		{"gemini 400 bad key", &APIError{Status: 400, Body: rcBodyGemKey}, "auth"},
		{"gemini bad key never gone", &APIError{Status: 400, Body: `{"error":{"message":"API key not valid: model not available"}}`}, "auth"},
		{"kilo 404 laguna", &APIError{Status: 404, Body: rcBody404}, "gone"},
		{"groq 128 tools", &APIError{Status: 400, Body: rcBodyTools}, "toomanytools"},
		{"groq 413 tpm", &APIError{Status: 413, Body: rcBody413}, "toobig"},
		{"tool call args status 0", &APIError{Status: 0, Body: "Failed to parse tool call arguments as JSON"}, "toolcall"},
		{"tool call args 400", &APIError{Status: 400, Body: rcBodyBadArgs}, "toolcall"},
	}
	for _, c := range cases {
		if k := pvClassify(c.ae); k != c.kind {
			t.Errorf("%s: kind %q want %q", c.name, k, c.kind)
		}
		// one classifier: none of these is worth waiting for on the same ref
		if c.kind != "toolcall" && RetryDelay(c.ae, 0) != 0 {
			t.Errorf("%s: RetryDelay must be 0 (next candidate now)", c.name)
		}
	}
	promo := &APIError{Status: 429, Body: rcBodyPromo}
	pvMarkQuota(promo, time.Now())
	if k := pvClassify(promo); k != "daily" || !promo.Quota {
		t.Errorf("kilo PROMOTION: kind %q quota %v", k, promo.Quota)
	}
}

// omniHealthKind reads the same classifier: what `jazz omni health` calls dead
// is what the router tombstones, and a bad key is never a dead model.
func TestOmniHealthKindUsesClassifier(t *testing.T) {
	cases := []struct {
		ae   *APIError
		kind string
	}{
		{&APIError{Status: 410, Body: rcBody410}, "dead"},
		{&APIError{Status: 404, Body: rcBody404}, "dead"},
		{&APIError{Status: 400, Body: rcBodyGemKey}, "key"},
		{&APIError{Status: 401, Body: "unauthorized"}, "key"},
		{&APIError{Status: 429, Body: "slow down"}, "quota"},
		{&APIError{Status: 503, Body: "overloaded"}, "down"},
	}
	for _, c := range cases {
		if k, _ := omniHealthKind(c.ae); k != c.kind {
			t.Errorf("%d %s: kind %q want %q", c.ae.Status, oneLine(c.ae.Body, 40), k, c.kind)
		}
	}
}

// 410 is a permanent tombstone (no 24h expiry); a bad key is remembered per
// key fingerprint, so a new key works at once.
func TestClassifierPersistsGoneAndAuthByKey(t *testing.T) {
	c := pvTestCfg(t)
	now := pvSetClock(t, time.Now())
	ref := "nvidia/z-ai/glm-5.3"
	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 410, Body: rcBody410})
	if st := c.pvState(ref, 0); !st.Dead {
		t.Fatal("410 must mark the model dead")
	}
	*now = now.Add(30 * 24 * time.Hour)
	if st := c.pvState(ref, 0); !st.Dead {
		t.Fatal("a retired model must stay dead after 30 days (no 24h expiry)")
	}
	if _, ok := ExhaustedUntil(ref); !ok {
		t.Fatal("ExhaustedUntil must report a gone model")
	}
	ResetRateLimits("nvidia")
	if st := c.pvState(ref, 0); st.Dead {
		t.Fatal("jazz omni reset must lift the tombstone")
	}

	t.Setenv("GEMINI_API_KEY", "AIza-old-key")
	gem := "gemini/gemini-3.8-flash"
	c.rlEnd(gem, 0, Usage{}, &APIError{Status: 400, Body: rcBodyGemKey})
	if st := c.pvState(gem, 0); !st.Dead || !strings.Contains(st.Why, "credencial") {
		t.Fatalf("400 bad key must mark the key refused: %+v", st)
	}
	if st := c.pvState("gemini/gemini-2.5-pro", 0); !st.Dead {
		t.Fatal("a refused key is refused for every model of the provider")
	}
	t.Setenv("GEMINI_API_KEY", "AIza-new-key")
	if st := c.pvState(gem, 0); st.Dead {
		t.Fatal("a new key must not inherit the old key's refusal")
	}
}

// No final message carries raw provider JSON, and each names its remedy.
func TestFriendlyErrNoRawJSON(t *testing.T) {
	promo := &APIError{Status: 429, Body: rcBodyPromo}
	pvMarkQuota(promo, time.Now())
	cases := []struct {
		err  error
		want []string
	}{
		{&APIError{Status: 410, Body: rcBody410}, []string{"aposentado", "/model"}},
		{&APIError{Status: 400, Body: rcBodyGemKey}, []string{"credencial recusada", "API key not valid", "/key"}},
		{&APIError{Status: 404, Body: rcBody404}, []string{"aposentado/removido", "/model"}},
		{&APIError{Status: 400, Body: rcBodyTools}, []string{"ferramentas demais", "MCP"}},
		{promo, []string{"cota grátis", "/key"}},
		{&APIError{Status: 413, Body: rcBody413}, []string{"grande demais", "/compact"}},
		{&APIError{Status: 0, Body: "Failed to parse tool call arguments as JSON"}, []string{"chamada de ferramenta"}},
		{&APIError{Status: 500, Body: `{"error":{"message":"upstream exploded","code":500}}`}, []string{"upstream exploded"}},
		{&APIError{Status: 418, Body: `[{"weird":{"nested":true}}]`}, []string{"HTTP 418"}},
	}
	for _, c := range cases {
		msg := friendlyErr(c.err)
		if strings.Contains(msg, "{") || strings.Contains(msg, "[{") {
			t.Errorf("raw JSON in %q", msg)
		}
		if strings.Contains(msg, "falha de rede") {
			t.Errorf("not a network failure: %q", msg)
		}
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%q: missing %q", msg, w)
			}
		}
	}
}

func TestPvErrMessageShapes(t *testing.T) {
	cases := map[string]string{
		rcBodyGemKey: "API key not valid. Please pass a valid API key.",
		rcBody410:    "Gone: The model 'z-ai/glm-5.3' has reached its end of life on 2026-10-28.",
		`{"title":"Not Found"}`: "Not Found",
		`{"error":"plain"}`:     "plain",
	}
	for body, want := range cases {
		if got := pvErrMessage(body); got != want {
			t.Errorf("pvErrMessage(%s) = %q want %q", oneLine(body, 40), got, want)
		}
	}
	if got := pvPlainErr(`{"no":"message"}`); got != "sem detalhe legível" {
		t.Errorf("pvPlainErr of a bare object = %q", got)
	}
}

// A retired model in the chain: the run moves on to the next candidate, says
// why in PT-BR, and the next turn never asks the retired model again.
func TestAgentGoneMovesToNextCandidate(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a" {
			return fakeResp{Status: 410, Body: rcBody410}
		}
		return fakeResp{Text: "ok por b"}
	}, func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/b"}
	})
	h.ag.Model = "fake/a"
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("a 410 with a live fallback must not kill the run: %v", err)
	}
	if !anyContains(h.ui.warns, "aposentado pelo provedor em 28/10") {
		t.Errorf("warns %q: want the PT-BR retirement notice", h.ui.warns)
	}
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	if n := h.fs.countModel("a"); n != 1 {
		t.Errorf("retired model asked %d times, want 1 (tombstone)", n)
	}
	if n := h.fs.countModel("b"); n != 2 {
		t.Errorf("fallback asked %d times, want 2", n)
	}
}
