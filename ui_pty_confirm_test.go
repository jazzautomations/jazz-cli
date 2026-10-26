//go:build darwin || linux

package main

import (
	"strings"
	"testing"
	"time"
)

// PTY tests of the confirmations (menu.go, confirm.go): the default mode
// with and without an OS sandbox (faked with JAZZ_TEST_SANDBOX), the folder
// trust menu, Shift+Tab, the plan question, type-ahead during an approval,
// ask_user, and the session/message pickers. Nothing here types a word to
// answer a question.

func ptyFakeCfg(s string, mode string) map[string]any {
	cfg := map[string]any{"model": "fake/m1", "providers": map[string]any{
		"fake": map[string]any{"type": "openai", "base_url": s + "/v1", "no_key": true}}}
	if mode != "" {
		cfg["mode"] = mode
	}
	return cfg
}

func ptyEnv(sandbox string) []string {
	return []string{"NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=", "JAZZ_TEST_SANDBOX=" + sandbox}
}

func (s *ptySession) exit() {
	s.t.Helper()
	s.write("\x15\x04") // clear the input first: Ctrl+D exits only on an empty line
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		s.t.Fatal("jazz did not exit on Ctrl+D")
	}
}

func (s *ptySession) transcript() string { return strings.Join(s.all(), "\n") }

// footerWith: the input box is up and its footer shows tag.
func footerWith(tag string) func(string) bool {
	return func(sc string) bool { return strings.Contains(sc, tag+" · fake/m1") && strings.Contains(sc, "╭") }
}

// TestPTYDefaultMode: no mode in config + a sandbox that isolates = turbo,
// explained once in the welcome box; the trust question is a menu answered
// with Enter, remembered for the next launch; Shift+Tab cycles turbo → auto
// → plan → ask → turbo; the plan question is a menu. Without a sandbox the
// default is auto; "Não" on the trust menu keeps the session in ask.
func TestPTYDefaultMode(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	home, root := t.TempDir(), t.TempDir()

	// 1. first launch: trust menu, Enter = trust; turbo by default
	s := ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, ""), home, root, nil, ptyEnv("1")...)
	s.waitFor("the trust menu", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Primeira vez nesta pasta. Confiar?") })
	sc := s.screen()
	for _, want := range []string{"─ pasta nova ─", "▸ 1. Sim, confiar nesta pasta", "2. Só nesta sessão", "3. Não, modo ask", "roda no modo turbo"} {
		if !strings.Contains(sc, want) {
			t.Errorf("trust menu lacks %q:\n%s", want, sc)
		}
	}
	s.boxRows("the trust menu")
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\r")
	s.waitFor("turbo in the footer", 15*time.Second, footerWith("» turbo"))
	all := s.transcript()
	for _, want := range []string{"● pasta confiável:", "» turbo", "sandbox ativo: roda sem perguntar"} {
		if !strings.Contains(all, want) {
			t.Errorf("first launch lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(s.screen(), "Primeira vez nesta pasta") {
		t.Error("the trust menu was not erased")
	}
	// Shift+Tab: turbo → auto → plan → ask → turbo
	for _, tag := range []string{"▸▸ auto", "‖ plano", "▸ ask", "» turbo"} {
		s.write("\x1b[Z")
		s.waitFor("Shift+Tab to "+tag, 5*time.Second, footerWith(tag))
	}
	// plan mode: the "execute the plan?" question is a menu; Enter runs it in turbo
	s.write("\x1b[Z\x1b[Z") // turbo → auto → plan
	s.waitFor("plan mode", 5*time.Second, footerWith("‖ plano"))
	s.typeSlow("oi")
	s.write("\r")
	s.waitFor("the plan menu", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Executar o plano?") })
	if sc := s.screen(); !strings.Contains(sc, "▸ 1. Sim, executar (modo turbo)") || !strings.Contains(sc, "3. Não, continuar planejando") {
		t.Errorf("plan menu:\n%s", sc)
	}
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\r")
	s.waitFor("the plan running in turbo", 15*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "modo turbo: executando o plano") && footerWith("» turbo")(sc)
	})
	s.exit()

	// 2. second launch, same folder: no question, no explanation, still turbo
	s = ptyStartIn(t, "xterm-256color", nil, home, root, nil, ptyEnv("1")...)
	s.waitFor("turbo in the footer", 15*time.Second, footerWith("» turbo"))
	if all := s.transcript(); strings.Contains(all, "Primeira vez") || strings.Contains(all, "sandbox ativo: roda sem perguntar") {
		t.Errorf("second launch asked again or explained again:\n%s", all)
	}
	s.exit()

	// 3. no sandbox: the default is auto (explained once)
	s = ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, ""), t.TempDir(), t.TempDir(), nil, ptyEnv("0")...)
	s.trust()
	s.waitFor("auto in the footer", 15*time.Second, footerWith("▸▸ auto"))
	if all := s.transcript(); !strings.Contains(all, "turbo indisponível") {
		t.Errorf("no-sandbox explanation missing:\n%s", all)
	}
	s.write("\x1b[Z") // auto → plan (no turbo without a sandbox)
	s.waitFor("Shift+Tab skips turbo", 5*time.Second, footerWith("‖ plano"))
	s.exit()

	// 4. a config mode wins over the default
	s = ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, "auto"), t.TempDir(), t.TempDir(), nil, ptyEnv("1")...)
	s.trust()
	s.waitFor("configured auto", 15*time.Second, footerWith("▸▸ auto"))
	s.exit()

	// 5. "Não" on the trust menu (Esc): ask mode, asked again next time
	h5, r5 := t.TempDir(), t.TempDir()
	s = ptyStartIn(t, "xterm-256color", ptyFakeCfg(srv.URL, ""), h5, r5, nil, ptyEnv("1")...)
	s.waitFor("the trust menu", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Primeira vez nesta pasta") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b")
	s.waitFor("ask mode", 15*time.Second, footerWith("▸ ask"))
	if all := s.transcript(); !strings.Contains(all, "pasta não confiada: modo ask nesta sessão") {
		t.Errorf("trust refusal not reported:\n%s", all)
	}
	s.exit()
	s = ptyStartIn(t, "xterm-256color", nil, h5, r5, nil, ptyEnv("1")...)
	s.waitFor("the trust menu again", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "Primeira vez nesta pasta") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b[B\r") // ↓ Enter = só nesta sessão
	s.waitFor("session trust", 15*time.Second, footerWith("» turbo"))
	if all := s.transcript(); !strings.Contains(all, "pasta confiável só nesta sessão") {
		t.Errorf("session-only trust not reported:\n%s", all)
	}
	s.exit()
}

// TestPTYApproveTypeAhead (audit: "digitar sim"): text typed while the agent
// works is shown above the approval as queued; Enter after a pause is the
// highlighted "Sim", and the queued text comes back in the input box.
func TestPTYApproveTypeAhead(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	s.typeSlow("rode devagar")
	s.write("\r")
	time.Sleep(300 * time.Millisecond)
	s.typeSlow("e depois explica")
	s.waitFor("the approval", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Rodar este comando?") })
	if sc := s.screen(); !strings.Contains(sc, "na fila: e depois explica") || !strings.Contains(sc, "▸ 1. Sim") {
		t.Errorf("queued text or highlight missing:\n%s", sc)
	}
	time.Sleep(uiApproveIdle + 100*time.Millisecond)
	s.write("\r")
	s.waitFor("the turn to end", 15*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "pronto: 300 linhas.") && ptyPrompt(sc)
	})
	all := s.transcript()
	if !strings.Contains(all, "● sim · Bash git branch -D jazz-pty-nada") || strings.Contains(all, "negado") {
		t.Errorf("Enter after typing ahead did not approve:\n%s", all)
	}
	if sc := s.screen(); !strings.Contains(sc, "› e depois explica") {
		t.Errorf("queued text not back in the input box:\n%s", sc)
	}
	s.exit()
}

// TestPTYApproveNarrow: at 80 columns the approval box fits, and a reason is
// never cut (every word of it is on screen).
func TestPTYApproveNarrow(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.resize(80, 27)
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	s.typeSlow("rode perigoso")
	s.write("\r")
	s.waitFor("the approval", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Rodar este comando?") })
	s.boxRows("the approval at 80 columns")
	if sc := s.screen(); !strings.Contains(sc, "⚠ apaga branch do git") || strings.Contains(sc, "apaga branch do g…") {
		t.Errorf("reason cut:\n%s", sc)
	}
	// v prints the whole command and reason above the menu, which stays open
	time.Sleep(uiApproveIdle + 100*time.Millisecond)
	s.write("v")
	s.waitFor("the full view", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "│ git branch -D jazz-pty-nada") && strings.Contains(sc, "Rodar este comando?")
	})
	time.Sleep(100 * time.Millisecond)
	s.write("n")
	s.waitFor("the turn to end", 15*time.Second, func(sc string) bool { return strings.Contains(s.transcript(), "● não · Bash") && ptyPrompt(sc) })
	s.exit()
}

// TestPTYAskUserMenu: ask_user is a menu; the digit picks an option.
func TestPTYAskUserMenu(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	s.typeSlow("pergunte algo")
	s.write("\r")
	s.waitFor("the question", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Qual banco usar?") })
	if sc := s.screen(); !strings.Contains(sc, "▸ 1. SQLite  arquivo local") || !strings.Contains(sc, "3. Outra resposta (escrever)") {
		t.Errorf("ask_user menu:\n%s", sc)
	}
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b[B\r") // ↓ Enter = Postgres
	s.waitFor("the turn to end", 15*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "pronto: 300 linhas.") && ptyPrompt(sc)
	})
	if all := s.transcript(); !strings.Contains(all, "● Qual banco usar?  Postgres") {
		t.Errorf("answer not shown:\n%s", all)
	}
	s.exit()
}

// TestPTYPickers: /resume is a picker (↑↓, type to filter, Enter, Esc) that
// never echoes a raw key and is erased afterwards; /rewind picks a message;
// /forget asks with a menu.
func TestPTYPickers(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	for _, q := range []string{"pergunta um", "pergunta dois", "pergunta tres"} {
		n := strings.Count(s.transcript(), "● ok")
		s.typeSlow(q)
		s.write("\r")
		s.waitFor("the answer to "+q, 10*time.Second, func(sc string) bool {
			return strings.Count(s.transcript(), "● ok") > n && ptyPrompt(sc)
		})
		s.typeSlow("/clear")
		s.write("\r")
		s.waitFor("a new conversation", 10*time.Second, ptyPrompt)
		time.Sleep(1100 * time.Millisecond) // distinct modification times (newest first)
	}
	// the session list itself is sessions_ui.go (TestPTYResumePicker covers it
	// in depth); here: ↓↓ Enter opens the third, typing filters, Esc cancels
	picker := func(sc string) bool { return strings.Contains(sc, pkHint) }
	s.typeSlow("/resume")
	s.write("\r")
	s.waitFor("the session picker", 10*time.Second, picker)
	sc := s.screen()
	for _, want := range []string{"sessões em", "▸ 1 Pergunta tres", "Pergunta dois", "Pergunta um", "2 msgs"} {
		if !strings.Contains(sc, want) {
			t.Errorf("picker lacks %q:\n%s", want, sc)
		}
	}
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b[B")
	time.Sleep(50 * time.Millisecond)
	s.write("\x1b[B")
	time.Sleep(50 * time.Millisecond)
	s.write("\r")
	s.waitFor("the third session", 10*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "retomada (") && strings.Contains(s.transcript(), "): Pergunta um") && ptyPrompt(sc)
	})
	if strings.Contains(s.screen(), pkHint) || strings.Contains(s.screen(), "[B") {
		t.Errorf("picker not erased or raw key echoed:\n%s", s.screen())
	}
	// type to filter
	s.typeSlow("/resume")
	s.write("\r")
	s.waitFor("the session picker", 10*time.Second, picker)
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.typeSlow("dois")
	s.waitFor("the filter", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "▸ 1 Pergunta dois") && !strings.Contains(sc, "Pergunta tres")
	})
	s.write("\r")
	s.waitFor("the filtered session", 10*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "): Pergunta dois") && ptyPrompt(sc)
	})
	// Esc cancels
	s.typeSlow("/sessions")
	s.write("\r")
	s.waitFor("the session picker", 10*time.Second, picker)
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b")
	s.waitFor("cancelled", 10*time.Second, func(sc string) bool { return !picker(sc) && ptyPrompt(sc) })
	// /rewind: the message picker, newest highlighted
	s.typeSlow("/rewind")
	s.write("\r")
	s.waitFor("the message picker", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Voltar para antes de qual mensagem?") })
	if sc := s.screen(); !strings.Contains(sc, "▸ pergunta dois") {
		t.Errorf("rewind picker:\n%s", sc)
	}
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\r")
	s.waitFor("rewound", 10*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "conversa voltou para antes da mensagem 1") && strings.Contains(sc, "› pergunta dois")
	})
	// clear the prefilled text, then /forget: Esc keeps, Enter deletes
	s.write("\x15")
	s.typeSlow("/forget")
	s.write("\r")
	s.waitFor("the forget menu", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Apagar do disco a sessão") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\x1b")
	s.waitFor("kept", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "cancelado") && ptyPrompt(sc) })
	s.typeSlow("/forget")
	s.write("\r")
	s.waitFor("the forget menu", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "Apagar do disco a sessão") })
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	s.write("\r")
	s.waitFor("deleted", 10*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "apagada; começando uma conversa nova") && ptyPrompt(sc)
	})
	all := s.transcript()
	for _, bad := range []string{"[B", "[A", "número inválido", "número (enter cancela)"} {
		if strings.Contains(all, bad) {
			t.Errorf("transcript has %q:\n%s", bad, all)
		}
	}
	s.exit()
}
