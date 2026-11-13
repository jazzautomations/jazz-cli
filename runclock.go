package main

// Time awareness inside a run. The system prompt carries the session's start
// (fixed, so the prompt cache survives); between steps the loop adds a short
// clock note — local time, time elapsed, and how much of --timeout is left —
// to the end of the last tool result, which has not been sent yet. It is
// never a message of its own (v0.13: a user-role "relógio" every 2 minutes
// read as the user talking, and weak models answered it). With --timeout it
// comes at most every rcClockEvery; without a deadline only after
// rcClockIdle, then at that pace.

import (
	"fmt"
	"strings"
	"time"
)

const (
	rcClockEvery = 2 * time.Minute
	rcClockIdle  = 10 * time.Minute
)

func (a *Agent) rcClock(now time.Time) {
	if a.runStart.IsZero() {
		a.runStart, a.clockAt = now, now
		return
	}
	every := rcClockIdle
	if !a.RunDeadline.IsZero() {
		every = rcClockEvery
	}
	if now.Sub(a.clockAt) < every || len(a.Msgs) == 0 {
		return
	}
	last := &a.Msgs[len(a.Msgs)-1]
	if last.Role != "tool" || strings.Contains(last.Content, "<system-reminder>relógio:") {
		return // only riding on a fresh tool result; tried again next step
	}
	a.clockAt = now
	last.Content += "\n\n" + rcClockText(now, a.runStart, a.RunDeadline)
}

// rcClockText is the reminder: "agora 20:31 · rodando há 7m · faltam 3m de 10m".
func rcClockText(now, start, deadline time.Time) string {
	s := fmt.Sprintf("<system-reminder>relógio: agora %s · rodando há %s", now.Format("15:04"), rcDur(now.Sub(start)))
	if !deadline.IsZero() {
		total, left := deadline.Sub(start), deadline.Sub(now)
		if left < 0 {
			left = 0
		}
		s += fmt.Sprintf(" · faltam %s de %s", rcDur(left), rcDur(total))
	}
	return s + "</system-reminder>"
}

func rcDur(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
