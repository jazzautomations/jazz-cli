//go:build darwin || linux

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Two dangerous calls in one response: one numbered box, Enter says yes to
// both, no per-call prompt follows (approve_batch.go, ui_batch.go).
func TestPTYApproveBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	s.typeSlow("rode dois perigosos")
	s.write("\r")
	s.waitFor("the merged box", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "2 confirmações") && strings.Contains(sc, "git branch -D jazz-pty-a") && strings.Contains(sc, "git branch -D jazz-pty-b")
	})
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("box:\n%s", s.screen())
	}
	if sc := s.screen(); !strings.Contains(sc, "1. Bash") || !strings.Contains(sc, "2. Bash") || !strings.Contains(sc, "apaga branch") {
		t.Errorf("box lacks the numbered calls or the reason:\n%s", sc)
	}
	time.Sleep(uiApproveGrace + uiApproveIdle)
	s.write("\r")
	s.waitFor("the end of the turn", 20*time.Second, func(sc string) bool {
		return strings.Contains(sc, "pronto: 300 linhas.") && ptyPrompt(sc)
	})
	all := s.allText()
	if n := strings.Count(all, "Rodar este comando?"); n != 0 {
		t.Errorf("%d single prompts after the merged one\n%s", n, all)
	}
	if !strings.Contains(all, "sim pra tudo · 2 ações") {
		t.Errorf("no summary line\n%s", all)
	}
	if n := strings.Count(all, "Bash(git branch -D jazz-pty-"); n != 2 {
		t.Errorf("want both calls run, got %d\n%s", n, all)
	}
}
