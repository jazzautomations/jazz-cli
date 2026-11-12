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

// The clock rides on the last tool result (never a user message of its
// own): every rcClockEvery with a deadline, after rcClockIdle without one.
func TestRunClockCadence(t *testing.T) {
	a := &Agent{}
	t0 := time.Now()
	a.RunDeadline = t0.Add(10 * time.Minute)
	a.rcClock(t0)
	a.Msgs = append(a.Msgs, Message{Role: "tool", ToolCallID: "c1", Content: "ok"})
	a.rcClock(t0.Add(time.Minute))
	if a.Msgs[0].Content != "ok" {
		t.Fatalf("too early: %q", a.Msgs[0].Content)
	}
	a.rcClock(t0.Add(3 * time.Minute))
	a.rcClock(t0.Add(4 * time.Minute))
	if len(a.Msgs) != 1 || strings.Count(a.Msgs[0].Content, "rodando há 3m") != 1 || strings.Count(a.Msgs[0].Content, "relógio") != 1 {
		t.Fatalf("want one clock note at 3m on the tool result, got %+v", a.Msgs)
	}
	// a user message last: no clock, no new message
	a.Msgs = append(a.Msgs, Message{Role: "user", Content: "<system-reminder>x</system-reminder>"})
	a.rcClock(t0.Add(8 * time.Minute))
	if len(a.Msgs) != 2 || strings.Contains(a.Msgs[1].Content, "relógio") {
		t.Fatalf("clock must not become or ride on a user message: %+v", a.Msgs)
	}

	// no deadline: nothing before 10 minutes
	b := &Agent{}
	b.rcClock(t0)
	b.Msgs = append(b.Msgs, Message{Role: "tool", ToolCallID: "c1", Content: "ok"})
	b.rcClock(t0.Add(5 * time.Minute))
	if b.Msgs[0].Content != "ok" {
		t.Fatalf("no deadline: no clock at 5m: %q", b.Msgs[0].Content)
	}
	b.rcClock(t0.Add(11 * time.Minute))
	if !strings.Contains(b.Msgs[0].Content, "rodando há 11m") {
		t.Fatalf("no deadline: clock after 10m: %q", b.Msgs[0].Content)
	}
}
