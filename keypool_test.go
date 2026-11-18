//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"
)

// pvPoolCfg builds a test config whose provider "or" holds a key pool in
// auth.json (primary + extras), with a daily request budget so per-key
// counters can be exhausted deterministically.
func pvPoolCfg(t *testing.T, keys ...string) *Config {
	t.Helper()
	c := pvTestCfg(t)
	c.Providers["or"] = &ProviderConf{Type: "openai", BaseURL: "http://x",
		Limits: pvLim(Limits{RPD: 2, Scope: "key"})}
	if len(keys) == 0 {
		return c
	}
	cred := &Cred{Type: "api", Key: keys[0]}
	if len(keys) > 1 {
		cred.Keys = keys[1:]
	}
	if err := SaveCred("or", cred); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKeyPoolListAndDedup(t *testing.T) {
	c := pvPoolCfg(t, "k1", "k2", "k3", "k2")
	got := c.APIKeys("or")
	if strings.Join(got, ",") != "k1,k2,k3" {
		t.Fatalf("APIKeys %v", got)
	}
	// env var is a single explicit override, pool ignored
	c.Providers["or"].KeyEnv = "OR_TEST_KEY"
	t.Setenv("OR_TEST_KEY", "envk")
	if got := c.APIKeys("or"); len(got) != 1 || got[0] != "envk" {
		t.Fatalf("env must win over the pool: %v", got)
	}
}

func TestKeyPoolStoreKeepsPoolOnSingleKey(t *testing.T) {
	c := pvPoolCfg(t, "k1", "k2", "k3")
	if _, err := storeKeys(c, "or", []string{"k9"}); err != nil {
		t.Fatal(err)
	}
	cr := LoadAuth()["or"]
	if cr.Key != "k9" || strings.Join(cr.Keys, ",") != "k2,k3" {
		t.Fatalf("single key must swap primary and keep the pool: %+v", cr)
	}
	if _, err := storeKeys(c, "or", []string{"a", "b", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	cr = LoadAuth()["or"]
	if cr.Key != "a" || strings.Join(cr.Keys, ",") != "b,c" {
		t.Fatalf("multi input must replace the pool deduped: %+v", cr)
	}
}

// the money case: key1 hits its daily request budget, the next request goes
// out with key2 — same provider, same model, no user action.
func TestKeyPoolRotatesOnExhaustedCounter(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-1", "sk-or-2")
	pvSetClock(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	ref := "or/m"
	fp1 := pvKeyPrint("sk-or-1")

	if k := c.APIKeyFor(ref); k != "sk-or-1" {
		t.Fatalf("first pick should be the primary: %q", k)
	}
	for i := 0; i < 2; i++ { // RPD 2
		if st := c.pvState(ref, 10); !st.Ready {
			t.Fatalf("req %d not ready: %+v", i, st)
		}
		c.rlBegin(ref, 10, fp1)
		c.rlEnd(ref, 10, Usage{In: 5, Out: 5}, nil, fp1)
	}
	if k := c.APIKeyFor(ref); k != "sk-or-2" {
		t.Fatalf("key1 exhausted must rotate to key2, got %q", k)
	}
	// the same ref evaluates alive: the state the router sees is the picked
	// key's, and key2 has a fresh budget
	if st := c.pvState(ref, 10); !st.Ready {
		t.Fatalf("ref must stay alive on key2: %+v", st)
	}
	// burn key2 too → deterministic fallback reports the real state
	fp2 := pvKeyPrint("sk-or-2")
	c.rlBegin(ref, 10, fp2)
	c.rlEnd(ref, 10, Usage{In: 5, Out: 5}, nil, fp2)
	c.rlBegin(ref, 10, fp2)
	c.rlEnd(ref, 10, Usage{In: 5, Out: 5}, nil, fp2)
	if st := c.pvState(ref, 10); !st.Dead {
		t.Fatalf("whole pool exhausted must read dead: %+v", st)
	}
}

// a refused key (401) joins AuthBadS and is skipped forever after; the pool
// answers with the next one.
func TestKeyPoolRotatesOnRefusedKey(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-1", "sk-or-2")
	ref := "or/m"
	fp1 := pvKeyPrint("sk-or-1")

	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 401, Body: "invalid api key"}, fp1)
	if k := c.APIKeyFor(ref); k != "sk-or-2" {
		t.Fatalf("refused key must rotate, got %q", k)
	}
	if st := c.pvState(ref, 0); st.Dead {
		t.Fatalf("key2 is fine: %+v", st)
	}
	// refusal is recorded on the provider's set (survives reloads) and the
	// key's shadow bucket
	rl := omniRL
	rl.mu.Lock()
	pb := rl.B["or"]
	if pb == nil || pb.AuthBadS[fp1] == 0 {
		t.Fatalf("AuthBadS missing fp1: %+v", pb)
	}
	if sh := rl.B["or@"+fp1]; sh == nil || sh.DeadUntil == 0 {
		t.Fatalf("refused key shadow not marked: %+v", sh)
	}
	rl.mu.Unlock()
}

// a quota 429 marks the key (shadow) not the provider: rotation stays inside
// the pool instead of giving up on openrouter entirely.
func TestKeyPoolQuotaCoolsKeyNotProvider(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-1", "sk-or-2")
	ref := "or/m"
	fp1 := pvKeyPrint("sk-or-1")

	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 429, Body: "quota exceeded", Quota: true}, fp1)
	if k := c.APIKeyFor(ref); k != "sk-or-2" {
		t.Fatalf("quota on key1 must rotate to key2, got %q", k)
	}
	if st := c.pvState(ref, 0); st.Dead {
		t.Fatalf("quota of one key must not kill the provider: %+v", st)
	}
	rl := omniRL
	rl.mu.Lock()
	if b := rl.B["or"]; b != nil && b.DeadUntil > 0 {
		t.Fatalf("provider bucket must stay clean when pooling: %+v", b)
	}
	rl.mu.Unlock()
}

// model-scope daily death must not retire the key for other models: groq-like
// provider (scope = ref) keeps the shadow clean.
func TestKeyPoolModelScopeDoesNotShadow(t *testing.T) {
	c := pvTestCfg(t)
	c.Providers["g"] = &ProviderConf{Type: "openai", BaseURL: "http://x",
		Limits: pvLim(Limits{RPD: 1})} // scope: per model
	if err := SaveCred("g", &Cred{Type: "api", Key: "g1", Keys: []string{"g2"}}); err != nil {
		t.Fatal(err)
	}
	fp1 := pvKeyPrint("g1")
	c.rlEnd("g/a", 0, Usage{}, &APIError{Status: 429, Body: "requests per day (RPD): Limit 1", Daily: true}, fp1)

	if k := c.APIKey("g"); k != "g1" {
		t.Fatalf("model-scope death must not shadow the key provider-wide: %q", k)
	}
	// but the same model picks the other key
	if k := c.APIKeyFor("g/a"); k != "g2" {
		t.Fatalf("dead on g/a must rotate to g2, got %q", k)
	}
	// and key1 still serves a different model
	if k := c.APIKeyFor("g/b"); k != "g1" {
		t.Fatalf("key1 must stay usable on g/b, got %q", k)
	}
}

// regression guard: a single key changes nothing — same bucket names as
// before the feature (no @fp suffix, no shadow).
func TestKeyPoolSingleKeyKeepsOldBuckets(t *testing.T) {
	c := pvPoolCfg(t, "only")
	ref := "or/m"
	fp := pvKeyPrint("only")
	c.rlBegin(ref, 10, fp)
	c.rlEnd(ref, 10, Usage{In: 5, Out: 5}, nil, fp)
	c.rlEnd(ref, 0, Usage{}, &APIError{Status: 429, Body: "requests per day (RPD): Limit 2", Daily: true}, fp)

	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k := range rl.B {
		if strings.Contains(k, "@") {
			t.Fatalf("single key must not create per-key buckets, found %q", k)
		}
	}
	if rl.B["or"] == nil || rl.B["or"].DeadUntil == 0 {
		t.Fatal("daily mark must land on the scope bucket as before")
	}
}

// legacy state file: the old single-string auth_bad must still refuse the key.
func TestKeyPoolLegacyAuthBadHonored(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-1", "sk-or-2")
	fp1 := pvKeyPrint("sk-or-1")
	rl := omniRL
	rl.mu.Lock()
	rl.loadLocked()
	rl.B["or"] = &rlBucket{AuthBad: fp1} // what an older binary wrote
	rl.mu.Unlock()

	if k := c.APIKeyFor("or/m"); k != "sk-or-2" {
		t.Fatalf("legacy auth_bad must rotate, got %q", k)
	}
}

func TestKeyPoolResetClearsShadows(t *testing.T) {
	c := pvPoolCfg(t, "sk-or-1", "sk-or-2")
	fp1 := pvKeyPrint("sk-or-1")
	c.rlEnd("or/m", 0, Usage{}, &APIError{Status: 401, Body: "invalid api key"}, fp1)
	ResetRateLimits("or")
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k := range rl.B {
		if strings.HasPrefix(k, "or@") || strings.HasPrefix(k, "or/") || k == "or" {
			t.Fatalf("reset must clear every bucket of the provider, left %q", k)
		}
	}
}
