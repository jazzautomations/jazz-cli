package main

// Time awareness inside a run. The system prompt carries the session's start
// (fixed, so the prompt cache survives); between steps the loop appends a
// short clock reminder — local time, time elapsed, and how much of --timeout
// is left — at most every rcClockEvery, and only once there is something to
// say (the run is older than that interval or has a deadline). Appended, never
// rewritten: earlier messages stay byte-identical.

import (
	"fmt"
	"time"
)

const rcClockEvery = 2 * time.Minute

func (a *Agent) rcClock(now time.Time) {
	if a.runStart.IsZero() {
		a.runStart, a.clockAt = now, now
		return
	}
	if now.Sub(a.clockAt) < rcClockEvery {
		return
	}
	a.clockAt = now
	a.append(Message{Role: "user", Content: rcClockText(now, a.runStart, a.RunDeadline)})
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
