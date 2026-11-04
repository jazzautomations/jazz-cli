package main

import (
	"strings"
	"testing"
	"time"
)

func TestRunClockText(t *testing.T) {
	start := time.Date(2026, 11, 4, 20, 24, 0, 0, time.Local)
	now := start.Add(7 * time.Minute)
	got := rcClockText(now, start, start.Add(10*time.Minute))
	for _, w := range []string{"agora 20:31", "rodando há 7m", "faltam 3m de 10m"} {
		if !strings.Contains(got, w) {
			t.Errorf("falta %q em %q", w, got)
		}
	}
	if g := rcClockText(now, start, time.Time{}); strings.Contains(g, "faltam") {
		t.Errorf("sem prazo não fala em 'faltam': %q", g)
	}
	if rcDur(90*time.Minute) != "1h30m" || rcDur(20*time.Second) != "<1m" {
		t.Errorf("rcDur: %s %s", rcDur(90*time.Minute), rcDur(20*time.Second))
	}
}

// The clock is appended at most every rcClockEvery and never on the first step.
func TestRunClockCadence(t *testing.T) {
	a := &Agent{}
	t0 := time.Now()
	a.rcClock(t0)
	a.rcClock(t0.Add(time.Minute))
	if len(a.Msgs) != 0 {
		t.Fatalf("too early: %d reminders", len(a.Msgs))
	}
	a.rcClock(t0.Add(3 * time.Minute))
	a.rcClock(t0.Add(4 * time.Minute))
	if len(a.Msgs) != 1 || !strings.Contains(a.Msgs[0].Content, "rodando há 3m") {
		t.Fatalf("want one reminder at 3m, got %+v", a.Msgs)
	}
}
