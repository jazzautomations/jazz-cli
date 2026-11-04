package main

import "testing"

// --fresh cuts memory in: lessons (and the plan reuse that rides on them) go
// off with it; the switch is off by default so nothing else changes.
func TestFreshCutsLessons(t *testing.T) {
	pfFreshMode.Store(false)
	lsState.Store(1)
	t.Setenv("JAZZ_FRESH", "")
	pfConfigureFresh(false)
	if pfFresh() || !lsEnabled() {
		t.Fatalf("sem --fresh: fresh=%v lessons=%v (queria false/true)", pfFresh(), lsEnabled())
	}
	pfConfigureFresh(true)
	if !pfFresh() || lsEnabled() {
		t.Fatalf("com --fresh: fresh=%v lessons=%v (queria true/false)", pfFresh(), lsEnabled())
	}
	// env alone turns it on too
	pfFreshMode.Store(false)
	lsState.Store(1)
	t.Setenv("JAZZ_FRESH", "1")
	pfConfigureFresh(false)
	if !pfFresh() || lsEnabled() {
		t.Fatalf("JAZZ_FRESH=1: fresh=%v lessons=%v", pfFresh(), lsEnabled())
	}
	pfFreshMode.Store(false)
	lsState.Store(1)
}
