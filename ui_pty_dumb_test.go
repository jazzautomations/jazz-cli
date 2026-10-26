//go:build darwin || linux

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TERM=dumb in a real PTY: no live status line, no box, no cursor movement
// for menus; the session still works (ui.go Theme.dumb).
func TestPTYDumbTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "dumb")
	s.waitFor("the trust question", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Primeira vez nesta pasta") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("1")
	s.waitFor("the prompt", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "pasta confiável") && strings.HasSuffix(strings.TrimRight(sc, " \n"), ">")
	})
	s.typeSlow("oi tudo bem")
	s.write("\r")
	s.waitFor("the answer", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "* ok") })
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("dumb:\n%s", s.screen())
	}
	s.mu.Lock()
	raw := s.raw.String()
	s.mu.Unlock()
	for _, bad := range []string{"\x1b[?25l", "╭", "●", "\x1b[38;"} {
		if strings.Contains(raw, bad) {
			t.Errorf("dumb terminal got %q", bad)
		}
	}
}
