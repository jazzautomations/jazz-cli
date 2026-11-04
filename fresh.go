package main

// fresh.go: `jazz --fresh` (or JAZZ_FRESH=1) opens a context window that is
// independent of the user's memory — the second of the two options the owner
// asked for (the default window carries memory; this one does not).
//
// Memory IN is cut at every injection point: project/global lessons
// (lessons.go, which is also where plan/fix reuse enters), AGENTS.md/CLAUDE.md
// (prompt.go). Memory OUT is cut too: improve.auto does not learn from a fresh
// run (main.go), so an experiment never leaks into the lessons. Everything
// else (sandbox, guardrails, providers) is untouched. Off by default.

import (
	"os"
	"strings"
	"sync/atomic"
)

var pfFreshMode atomic.Bool

// pfFresh reports whether this run is a fresh (memory-independent) window.
func pfFresh() bool { return pfFreshMode.Load() }

// pfConfigureFresh turns the fresh window on from the flag or JAZZ_FRESH, and
// cuts lesson injection with it (lessons.go owns the plan-reuse point too).
func pfConfigureFresh(flag bool) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_FRESH")))
	if flag || v == "1" || v == "true" || v == "on" || v == "yes" {
		pfFreshMode.Store(true)
		lsState.Store(2) // lessons (and plan reuse through them) off
	}
}
