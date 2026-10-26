//go:build darwin || linux

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Sessions like Claude Code, in a real PTY (sessions_ui.go): the arrow-key
// list for /resume, /sessions and `jazz -r`, the filter, Esc, and the
// resumed conversation rendered again in order (banner, transcript, notice).

// ptySeedSessions writes three sessions of root into home (oldest first:
// alfa, beta, gama) and returns their ids.
func ptySeedSessions(t *testing.T, home, root string) []string {
	t.Helper()
	t.Setenv("JAZZ_HOME", home)
	var ids []string
	for i, name := range []string{"alfa", "beta", "gama"} {
		s, err := NewSession(root, "fake/m1", "SYS")
		if err != nil {
			t.Fatal(err)
		}
		s.Append(Message{Role: "user", Content: "<system-reminder>lembrete interno</system-reminder>\npergunta " + name + " sobre o parser de datas"})
		s.AppendMeta(Message{Role: "assistant", Content: "Vou listar os arquivos.", ToolCalls: []ToolCall{{ID: "c" + name, Name: "bash", Args: `{"command":"ls -la ` + name + `"}`}}},
			&sessMsgMeta{Model: "fake/m1", Usage: Usage{In: 1200, Out: 300}})
		s.Append(Message{Role: "tool", ToolCallID: "c" + name, ToolName: "bash", Content: "total 8. arquivos\n[exit code 0]"})
		s.AppendMeta(Message{Role: "assistant", Content: "## Feito\n\nRESPOSTA-" + strings.ToUpper(name) + " com **negrito**."}, &sessMsgMeta{Model: "fake/m1", Usage: Usage{In: 1500, Out: 100}})
		s.Close()
		ev := `{"kind":"turn.start","mode":"auto"}` + "\n" + `{"kind":"turn.end","stop":"done","dur_ms":1200}` + "\n"
		if err := os.WriteFile(obEventsPath(s.Path), []byte(ev), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-time.Duration(3-i) * time.Hour)
		_ = os.Chtimes(s.Path, when, when)
		ids = append(ids, s.ID)
	}
	return ids
}

func ptySessCfg(t *testing.T) map[string]any {
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	return map[string]any{"model": "fake/m1", "mode": "auto", "providers": map[string]any{
		"fake": map[string]any{"type": "openai", "base_url": srv.URL + "/v1", "no_key": true}}}
}

func (s *ptySession) trustAndPrompt() {
	s.t.Helper()
	s.waitFor("the trust prompt", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "Primeira vez nesta pasta") || ptyPrompt(sc) || strings.Contains(sc, pkHint)
	})
	if strings.Contains(s.screen(), "Primeira vez nesta pasta") {
		time.Sleep(uiApproveGrace + 50*time.Millisecond)
		s.write("\r")
	}
}

func (s *ptySession) allText() string { return strings.Join(s.all(), "\n") }

func TestPTYResumePicker(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedSessions(t, home, root)
	other, err := NewSession(t.TempDir(), "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	other.Append(Message{Role: "user", Content: "pedido de outra pasta qualquer"})
	other.Close()
	cfg := ptySessCfg(t)
	s := ptyStartIn(t, "xterm-256color", cfg, home, root, nil, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)

	// 1. /resume opens the list: newest first, titles, dates, meta line
	s.typeSlow("/resume")
	s.write("\r")
	s.waitFor("the session list", 10*time.Second, func(sc string) bool { return strings.Contains(sc, pkHint) && strings.Contains(sc, "Pergunta alfa") })
	sc := s.screen()
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("list:\n%s", sc)
	}
	ig, ib, ia := strings.Index(sc, "Pergunta gama"), strings.Index(sc, "Pergunta beta"), strings.Index(sc, "Pergunta alfa")
	if !(ig >= 0 && ig < ib && ib < ia) {
		t.Errorf("not newest first:\n%s", sc)
	}
	for _, want := range []string{"há 1 h", "há 3 h", "auto", "4 msgs", "concluída", "tokens", "sessões em"} {
		if !strings.Contains(sc, want) {
			t.Errorf("list lacks %q:\n%s", want, sc)
		}
	}
	if strings.Contains(sc, "lembrete interno") {
		t.Errorf("reminder text in a title:\n%s", sc)
	}
	// 2. Esc cancels: the list is erased, nothing is resumed
	s.write("\x1b")
	s.waitFor("the cancel", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "nenhuma sessão escolhida") && ptyPrompt(sc)
	})
	if n := strings.Count(s.allText(), pkHint); n != 0 {
		t.Errorf("the list was left in the transcript (%d)", n)
	}

	// 3. /sessions + filter: typing "beta" leaves one row; Enter resumes it
	s.typeSlow("/sessions")
	s.write("\r")
	s.waitFor("the session list again", 10*time.Second, func(sc string) bool { return strings.Contains(sc, pkHint) })
	s.typeSlow("beta")
	s.waitFor("the filtered list", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "filtro › beta") && strings.Contains(sc, "Pergunta beta") && !strings.Contains(sc, "Pergunta gama") && !strings.Contains(sc, "Pergunta alfa")
	})
	s.write("\r")
	s.waitFor("the beta transcript", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "RESPOSTA-BETA") && strings.Contains(sc, "retomada (4 mensagens)") && ptyPrompt(sc)
	})
	all := s.allText()
	if os.Getenv("JAZZ_PTY_SHOW") != "" {
		t.Logf("transcript:\n%s", s.screen())
	}
	for _, want := range []string{"› pergunta beta sobre o parser de datas", "● Vou listar os arquivos.", "● Bash(ls -la beta)", "⎿  total 8", "Feito", "RESPOSTA-BETA com negrito.", "✻ conversa anterior · 4 mensagens · 1 ferramenta"} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "lembrete interno") || strings.Contains(all, "**") {
		t.Errorf("raw reminder or markdown in the transcript:\n%s", all)
	}
	// transcript before the notice
	if i, j := strings.Index(all, "RESPOSTA-BETA"), strings.Index(all, "retomada (4 mensagens)"); i < 0 || j < i {
		t.Errorf("notice before the transcript")
	}

	// 4. /rename names the session; the list shows the new name
	s.typeSlow("/rename Nome Novo da Beta")
	s.write("\r")
	s.waitFor("the rename", 10*time.Second, func(sc string) bool { return strings.Contains(sc, ": Nome Novo da Beta") && ptyPrompt(sc) })
	// without a title the current one comes up for editing
	s.typeSlow("/rename")
	s.write("\r")
	s.waitFor("the title editor", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "título › Nome Novo da Beta") })
	s.write("\x15") // ctrl+u
	s.typeSlow("Beta Final")
	s.write("\r")
	s.waitFor("the edited title", 10*time.Second, func(sc string) bool { return strings.Contains(sc, ": Beta Final") && ptyPrompt(sc) })

	// 5. Tab lists every project, grouped; a session of another folder is not
	// loaded here, the notice says how to open it
	s.typeSlow("/resume")
	s.write("\r")
	s.waitFor("the list with the new name", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, pkHint) && strings.Contains(sc, "Beta Final") && strings.Contains(sc, "atual")
	})
	s.write("\t")
	s.waitFor("all folders", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "sessões de todas as pastas") && strings.Contains(sc, "Pedido de outra pasta")
	})
	s.typeSlow("outra")
	s.waitFor("the other folder's row", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "1/1") })
	s.write("\r")
	s.waitFor("the other-folder notice", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "essa sessão é de") && ptyPrompt(sc) })

	// 6. arrows: ↓↓ Enter loads the third session (alfa); no raw escape is echoed
	s.typeSlow("/resume")
	s.write("\r")
	s.waitFor("the list for arrows", 10*time.Second, func(sc string) bool { return strings.Contains(sc, pkHint) })
	s.write("\x1b[B")
	time.Sleep(60 * time.Millisecond)
	s.write("\x1b[B")
	s.waitFor("the third row selected", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "3/3") })
	s.write("\r")
	s.waitFor("the alfa transcript", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "RESPOSTA-ALFA") && ptyPrompt(sc) })
	all = s.allText()
	if strings.Contains(all, "[B") || strings.Contains(all, "número inválido") {
		t.Errorf("raw arrow key echoed:\n%s", all)
	}
	if strings.Count(all, pkHint) != 0 {
		t.Errorf("a list was left in the transcript")
	}
	s.exit()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vt.hidden {
		t.Error("cursor left hidden")
	}
	if len(s.vt.badEscapes) > 0 {
		t.Errorf("unexpected sequences: %q", s.vt.badEscapes)
	}
}

// `jazz -r` with no id opens the list after the banner; `jazz -c` renders
// banner, transcript and notice in that order.
func TestPTYResumeStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedSessions(t, home, root)
	env := []string{"NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy="}
	t.Run("-r", func(t *testing.T) {
		s := ptyStartIn(t, "xterm-256color", ptySessCfg(t), home, root, []string{"-r"}, env...)
		s.trustAndPrompt()
		s.waitFor("the list at startup", 15*time.Second, func(sc string) bool { return strings.Contains(sc, pkHint) && strings.Contains(sc, "Pergunta gama") })
		if all := s.allText(); !strings.Contains(all, "v"+version) || strings.Index(all, "v"+version) > strings.Index(all, "Pergunta gama") {
			t.Errorf("banner not above the list:\n%s", all)
		}
		s.write("2") // digit shortcut: second row = beta
		s.waitFor("beta resumed", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "RESPOSTA-BETA") && ptyPrompt(sc) })
		s.exit()
	})
	t.Run("-c", func(t *testing.T) {
		s := ptyStartIn(t, "xterm-256color", ptySessCfg(t), home, root, []string{"-c"}, env...)
		s.trustAndPrompt()
		s.waitFor("gama resumed", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "RESPOSTA-GAMA") && ptyPrompt(sc) })
		all := s.allText()
		ib, ip, ia, in := strings.Index(all, "v"+version), strings.Index(all, "› pergunta gama"), strings.Index(all, "RESPOSTA-GAMA"), strings.Index(all, "retomada (")
		if !(ib >= 0 && ib < ip && ip < ia && ia < in) {
			t.Errorf("order banner < prompt < answer < notice broken: %d %d %d %d\n%s", ib, ip, ia, in, all)
		}
		s.exit()
	})
}

// /tree shows the session tree in the same list; Enter continues from the
// picked message and the branch is rendered.
func TestPTYTreePicker(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	home, root := t.TempDir(), t.TempDir()
	ptySeedSessions(t, home, root)
	s := ptyStartIn(t, "xterm-256color", ptySessCfg(t), home, root, []string{"-c"}, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trustAndPrompt()
	s.waitFor("resumed", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "RESPOSTA-GAMA") && ptyPrompt(sc) })
	s.typeSlow("/tree")
	s.write("\r")
	s.waitFor("the tree list", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "árvore da sessão") && strings.Contains(sc, "atual") && strings.Contains(sc, "3/3")
	})
	// ↑↑ goes to the first message (the prompt): continue from there
	s.write("\x1b[A")
	time.Sleep(60 * time.Millisecond)
	s.write("\x1b[A")
	s.waitFor("the first row", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "1/3") })
	s.write("\r")
	s.waitFor("the branch", 10*time.Second, func(sc string) bool {
		return strings.Contains(sc, "conversa agora continua de") && strings.Contains(sc, "(1 mensagens)") && ptyPrompt(sc)
	})
	s.exit()
}
