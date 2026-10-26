//go:build !windows

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// post sends one request to the fake server and returns the APIError the real
// HTTP path builds.
func quotaPost(t *testing.T, url string) *APIError {
	t.Helper()
	err := pvPostJSON(context.Background(), url, map[string]string{}, map[string]any{"x": 1}, nil, 5*time.Second)
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T %v", err, err)
	}
	return ae
}

func TestQuota429Variants(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		hdr       map[string]string
		body      string
		quota     bool
		wantAfter time.Duration // 0 = don't check
	}{
		{"kilo promotion, no reset", 429, nil, `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED","message":"Free promotion limit reached for this model"}}`, true, pvQuotaDefault},
		{"kilo promotion + body reset", 429, nil, `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED","message":"limit reached, resets in 2 hours"}}`, true, 2 * time.Hour},
		{"quota exceeded + retry-after", 429, map[string]string{"Retry-After": "300"}, `{"error":{"message":"You exceeded your current quota"}}`, true, 5 * time.Minute},
		{"quota + x-ratelimit-reset epoch s", 429, nil, `{"error":{"message":"quota exceeded","resets_at":"2099-01-01T00:00:00Z"}}`, true, 0},
		{"plain per-minute rate", 429, map[string]string{"Retry-After": "7"}, `{"error":{"message":"Rate limit reached for model X: tokens per minute (TPM)"}}`, false, 7 * time.Second},
		{"slow down", 429, nil, `slow down`, false, 0},
		{"groq per day stays daily not quota", 429, nil, `{"error":{"message":"Rate limit reached for model: requests per day (RPD)"}}`, false, 0},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range c.hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		ae := quotaPost(t, srv.URL)
		srv.Close()
		if ae.Quota != c.quota {
			t.Errorf("%s: Quota=%v want %v (%+v)", c.name, ae.Quota, c.quota, ae)
		}
		if c.quota && !ae.Daily {
			t.Errorf("%s: quota must imply Daily so the agent moves on", c.name)
		}
		if c.wantAfter > 0 && (ae.RetryAfter < c.wantAfter-2*time.Second || ae.RetryAfter > c.wantAfter+2*time.Second) {
			t.Errorf("%s: RetryAfter %v want ~%v", c.name, ae.RetryAfter, c.wantAfter)
		}
	}
}

func TestQuotaBodyReset(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for body, want := range map[string]time.Duration{
		"resets in 30 minutes":               30 * time.Minute,
		"try again in 1h2m3s":                time.Hour + 2*time.Minute + 3*time.Second,
		"retry after 90 seconds":             90 * time.Second,
		`"resets_at":"2026-10-26T18:00:00Z"`: 6 * time.Hour,
		"nothing useful here":                0,
	} {
		if got := pvBodyReset(body, now); got != want {
			t.Errorf("%q: got %v want %v", body, got, want)
		}
	}
}

// A quota 429 from one provider rotates the chain to the next provider, cools the
// whole provider with a "volta em", and the judge counters do not count it.
func TestQuota429RotatesAcrossProviders(t *testing.T) {
	c := pvTestCfg(t)
	now := pvSetClock(t, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	t.Setenv("GROQ_API_KEY", "gsk_test")
	c.Omni.Enabled = true
	first := "kilo/" + kiloDefaultModel
	before := c.Candidates(first)
	if len(before) < 2 || before[0] != first {
		t.Fatalf("chain before: %v", before)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED","message":"limit reached"}}`)
	}))
	defer srv.Close()
	ae := quotaPost(t, srv.URL)
	ojTakeAcc() // reset judge counters
	c.rlBegin(first, 100)
	c.rlEnd(first, 100, Usage{}, ae)

	after := c.Candidates(first)
	for _, r := range after {
		if strings.HasPrefix(r, "kilo/") {
			t.Fatalf("kilo must be out of the chain while cooling: %v", after)
		}
	}
	if len(after) == 0 || !strings.HasPrefix(after[0], "groq/") {
		t.Fatalf("expected rotation to groq, got %v", after)
	}
	st := c.pvState("kilo/minimax/minimax-m3.1-flash:free", 0)
	if !st.Dead || st.Until.Sub(*now) != pvQuotaDefault {
		t.Fatalf("whole provider should cool %v: %+v", pvQuotaDefault, st)
	}
	if acc := ojTakeAcc()[first]; acc == nil || acc.r429 != 0 || acc.reqs != 1 {
		t.Fatalf("judge must not count a quota 429 as a 429 (quality): %+v", acc)
	}
	rows := OmniProviderStatus(c)
	var kilo *ProvRow
	for i := range rows {
		if rows[i].Name == "kilo" {
			kilo = &rows[i]
		}
	}
	if kilo == nil || kilo.State != "429 cota" || kilo.LastKind != "quota" || kilo.Until.IsZero() {
		t.Fatalf("status row: %+v", kilo)
	}
	// time passes: back in the chain
	*now = now.Add(pvQuotaDefault + time.Second)
	if got := c.Candidates(first); got[0] != first {
		t.Fatalf("kilo should return after the cooldown: %v", got)
	}
}

func TestPoolDry(t *testing.T) {
	soon := time.Now().Add(10 * time.Minute)
	rows := []ProvRow{{Name: "a", Free: true, HasKey: true, Until: soon}, {Name: "b", Free: true, HasKey: true, Until: soon.Add(time.Hour)}}
	dry, first := PoolDry(rows)
	if !dry || !first.Equal(soon) {
		t.Fatalf("dry=%v first=%v", dry, first)
	}
	rows[0].Ready = true
	if dry, _ := PoolDry(rows); dry {
		t.Fatal("not dry when one is ready")
	}
	c := pvTestCfg(t)
	if out := FormatProviderStatus(c, []ProvRow{{Name: "kilo", Free: true, HasKey: true, State: "429 cota", Until: soon}}); !strings.Contains(out, "pool SECO") || !strings.Contains(out, "jazz omni setup") {
		t.Fatalf("missing dry warning:\n%s", out)
	}
}
