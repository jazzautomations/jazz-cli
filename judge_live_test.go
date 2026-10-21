package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// TestOjLiveHedgeLatency measures the task classifier with and without
// hedging against the real providers of $JAZZ_HOME. Opt-in: JAZZ_LIVE_HEDGE=1.
func TestOjLiveHedgeLatency(t *testing.T) {
	if os.Getenv("JAZZ_LIVE_HEDGE") != "1" {
		t.Skip("live test: set JAZZ_LIVE_HEDGE=1 and JAZZ_HOME")
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: cfg, ui: &recUI{}, Model: cfg.Model}
	src := os.Getenv("JAZZ_LIVE_TASKS") // a copy: run.sh locks evals/v2/tasks while it runs
	if src == "" {
		src = filepath.Join("evals", "v2", "tasks")
	}
	dirs, _ := filepath.Glob(filepath.Join(src, "*"))
	sort.Strings(dirs)
	var on, off []time.Duration
	for i, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "PROMPT"))
		if err != nil {
			t.Fatal(err)
		}
		sys, user := rtLLMPrompt(string(b))
		run := func(hedge bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			t0 := time.Now()
			resp, ref, err := a.ojHedge(ctx, "classifier", a.ojSmallCands(1200), hedge, 45*time.Second, func(ref, mid string) ChatRequest {
				return ChatRequest{Model: mid, System: sys, Messages: []Message{{Role: "user", Content: user}}, MaxTokens: 300, Effort: "low"}
			})
			dt := time.Since(t0)
			txt := ""
			if resp != nil {
				txt = rtParseLLM(resp.Text)
			}
			t.Logf("%s hedge=%v %6.1fs %-45s -> %q err=%v", filepath.Base(d), hedge, dt.Seconds(), ref, txt, err)
			if hedge {
				on = append(on, dt)
			} else {
				off = append(off, dt)
			}
		}
		if i%2 == 0 { // alternate the order so drift hits both arms
			run(false)
			run(true)
		} else {
			run(true)
			run(false)
		}
	}
	p50 := func(x []time.Duration) time.Duration {
		s := append([]time.Duration(nil), x...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return (s[(len(s)-1)/2] + s[len(s)/2]) / 2
	}
	t.Logf("cands=%v", a.ojSmallCands(1200))
	t.Logf("p50 without hedge %.1fs, with hedge %.1fs (n=%d each)", p50(off).Seconds(), p50(on).Seconds(), len(on))
}
