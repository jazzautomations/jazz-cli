//go:build !windows

package main

import (
	"testing"
	"time"
)

// acStickyHarness: the main model a stalls on its first request only; the
// fallback b answers with one bash call and then a final answer.
func acStickyHarness(t *testing.T) *harness {
	t.Helper()
	aSeen := 0
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a:free" {
			aSeen++
			if aSeen == 1 {
				return fakeResp{Stall: true}
			}
			return fakeResp{Text: "fim por a"}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo oi"})}}}
		}
		return fakeResp{Text: "fim por b"}
	}, func(c *Config) {
		c.Model = "fake/a:free"
		c.Fallback = []string{"fake/b:free"}
		c.FirstByteTimeout = 1
		c.Mode = "yolo"
	})
	h.ag.Model = "fake/a:free"
	return h
}

// A fallback must not stay sticky for the whole turn: after acStickyTTL the
// chain starts again from the main model (a headless -p run is one turn).
func TestAgentStickyFallbackExpires(t *testing.T) {
	oldTTL, oldPen := acStickyTTL, acStallPenalty
	t.Cleanup(func() { acStickyTTL, acStallPenalty = oldTTL, oldPen })
	acStickyTTL, acStallPenalty = 0, 0

	h := acStickyHarness(t)
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.fs.countModel("a:free"); got != 2 {
		t.Errorf("main model should be retried after the sticky TTL: a seen %d times", got)
	}
	if got := h.fs.countModel("b:free"); got != 1 {
		t.Errorf("fallback used %d times, want 1", got)
	}
	if h.ag.TurnModel() != "fake/a:free" {
		t.Errorf("turn ended on %s", h.ag.TurnModel())
	}
}

// Within the TTL the fallback stays sticky (no 25 s first-byte wait per step).
func TestAgentStickyFallbackWithinTTL(t *testing.T) {
	oldTTL := acStickyTTL
	t.Cleanup(func() { acStickyTTL = oldTTL })
	acStickyTTL = time.Hour

	h := acStickyHarness(t)
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.fs.countModel("a:free"); got != 1 {
		t.Errorf("main model retried inside the TTL: %d", got)
	}
	if got := h.fs.countModel("b:free"); got != 2 {
		t.Errorf("fallback used %d times, want 2", got)
	}
}
