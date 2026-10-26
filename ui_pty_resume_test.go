//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// `jazz resume <n>` in a real PTY: the conversation is shown again a page at
// a time (space pages, q jumps to the end), then the session goes on, and
// /tree shows the branch with the current node.

// ptySeedLong writes one session of root with n exchanges (taller than the screen).
func ptySeedLong(t *testing.T, home, root string, n int) string {
	t.Helper()
	t.Setenv("JAZZ_HOME", home)
	s, err := NewSession(root, "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		s.Append(Message{Role: "user", Content: fmt.Sprintf("PEDIDO-%02d sobre o parser", i)})
		s.AppendMeta(Message{Role: "assistant", Content: fmt.Sprintf("RESPOSTA-%02d feita.", i)}, &sessMsgMeta{Model: "fake/m1", Usage: Usage{In: 100, Out: 10}})
	}
	s.Close()
	return s.ID
}

func TestPTYResumePaged(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedLong(t, home, root, 30)
	s := ptyStartIn(t, "xterm-256color", ptySessCfg(t), home, root, []string{"resume", "1"}, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	// 1. the first page and the pager line
	s.waitFor("the pager", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "espaço próxima página") && strings.Contains(sc, "PEDIDO-01")
	})
	if sc := s.screen(); strings.Contains(sc, "RESPOSTA-30") || ptyPrompt(sc) {
		t.Fatalf("the whole transcript went out at once:\n%s", sc)
	}
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("page 1:\n%s", s.screen())
	}
	// 2. space: the next page
	s.write(" ")
	s.waitFor("page 2", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "PEDIDO-12") && strings.Contains(sc, "espaço próxima página")
	})
	// 3. q: the end, the skipped count, the notice and the prompt
	s.write("q")
	s.waitFor("the end", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "RESPOSTA-30") && strings.Contains(sc, "puladas") && strings.Contains(sc, "retomada (60 mensagens)") && ptyPrompt(sc)
	})
	if n := strings.Count(s.allText(), "espaço próxima página"); n != 0 {
		t.Errorf("the pager line was left in the transcript (%d)", n)
	}
	// 4. the session goes on: a new message gets an answer
	s.typeSlow("oi de novo")
	s.write("\r")
	s.waitFor("the answer", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "● ok") && ptyPrompt(sc) })
	// 5. /tree: the branch, the current node marked
	s.typeSlow("/tree")
	s.write("\r")
	s.waitFor("the tree", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "árvore da sessão") && strings.Contains(sc, "atual")
	})
	if sc := s.screen(); !strings.Contains(sc, "oi de novo") {
		t.Errorf("tree lacks the newest message:\n%s", sc)
	}
	s.write("\x1b")
	s.waitFor("the tree closed", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "árvore: nada mudou") && ptyPrompt(sc) })
}

// TestPTYResumeFull: --full writes everything at once, no pager.
func TestPTYResumeFull(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedLong(t, home, root, 30)
	s := ptyStartIn(t, "xterm", ptySessCfg(t), home, root, []string{"resume", "--full"}, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	s.waitFor("the session list", 15*time.Second, func(sc string) bool { return strings.Contains(sc, pkHint) })
	s.write("1")
	s.waitFor("the full transcript", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "retomada (60 mensagens)") && ptyPrompt(sc)
	})
	all := s.allText()
	for _, want := range []string{"PEDIDO-01", "PEDIDO-15", "RESPOSTA-30"} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript lacks %s", want)
		}
	}
	if strings.Contains(all, "espaço próxima página") {
		t.Errorf("--full paged")
	}
}

// TestPTYSessionsCommand: `jazz sessions` on a terminal is the list (digits
// pick), and the picked session is shown and goes on.
func TestPTYSessionsCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedSessions(t, home, root) // alfa, beta, gama (gama newest)
	s := ptyStartIn(t, "xterm-256color", ptySessCfg(t), home, root, []string{"sessions"}, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	s.waitFor("the session list", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, pkHint) && strings.Contains(sc, "1 Pergunta gama") && strings.Contains(sc, "2 Pergunta beta")
	})
	s.write("2")
	s.waitFor("beta resumed", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "RESPOSTA-BETA") && strings.Contains(sc, "retomada (4 mensagens)") && ptyPrompt(sc)
	})
}
