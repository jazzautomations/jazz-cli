//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPTYEditor drives the input editor in a real PTY at the two sizes that
// matter (80x24 and the Dell's 86x27 roxterm): the fuzzy "/" palette, Ctrl+R
// and prefix-filtered Up, Shift+Enter / backslash-Enter / bracketed paste
// kept verbatim, the "@" picker, Ctrl+O expand and collapse, Esc clearing the
// typed-ahead text before it interrupts, and the "?" list.
func TestPTYEditor(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	for _, sz := range [][2]int{{80, 24}, {86, 27}} {
		t.Run(itoa(sz[0])+"x"+itoa(sz[1]), func(t *testing.T) { ptyEditorScript(t, sz[0], sz[1]) })
	}
}

func ptyEditorScript(t *testing.T, w, h int) {
	s := ptyStart(t, "xterm-256color")
	if w != 86 || h != 27 {
		s.resize(w, h)
	}
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	all := func() string { return strings.Join(s.all(), "\n") }
	line := func(sub string) string { // the screen row holding sub
		for _, l := range strings.Split(s.screen(), "\n") {
			if strings.Contains(l, sub) {
				return l
			}
		}
		return ""
	}
	settle := func() { time.Sleep(120 * time.Millisecond) }
	show := func(what string) { // JAZZ_PTY_SHOW=1: log the screen at each step
		if os.Getenv("JAZZ_PTY_SHOW") != "" {
			t.Logf("%dx%d %s:\n%s", w, h, what, s.screen())
		}
	}

	// 1. "/" opens the palette with one-line descriptions and the keys
	s.write("/")
	s.waitFor("the palette", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "/help") && strings.Contains(sc, "esta ajuda") && strings.Contains(sc, "enter usa")
	})
	s.boxAligned("palette open")
	show("palette")
	// fuzzy filtering as you type: "hlp" -> /help highlighted; Enter runs it
	s.typeSlow("hlp")
	s.waitFor("the fuzzy match", 5*time.Second, func(sc string) bool { return strings.Contains(line("▸ /help"), "esta ajuda") })
	s.write("\r")
	s.waitFor("/help output", 5*time.Second, func(sc string) bool {
		return strings.Contains(all(), "!comando roda no shell direto") && ptyPrompt(sc)
	})
	if strings.Contains(all(), "comando desconhecido") {
		t.Error("the palette pick was not run as /help")
	}
	// arrows move the selection; Tab completes into the box with a space
	s.typeSlow("/mo")
	s.waitFor("the /mo matches", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "▸ /model") })
	s.write("\x1b[B")
	s.waitFor("the selection to move", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "▸ /models") })
	s.write("\t")
	s.waitFor("Tab completion", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "│ › /models") && !strings.Contains(sc, "▸ /model")
	})
	// Esc once clears the input
	s.write("\x1b")
	settle()
	s.waitFor("Esc to clear", 5*time.Second, func(sc string) bool { return !strings.Contains(sc, "│ › /models") && ptyPrompt(sc) })

	// 2. history: a prompt, then Ctrl+R finds it and prefix-filtered Up too
	s.typeSlow("primeira pergunta xyz")
	s.write("\r")
	s.waitFor("the first answer", 10*time.Second, func(sc string) bool { return strings.Contains(all(), "● ok") && ptyPrompt(sc) })
	s.write("\x12")
	s.typeSlow("xyz")
	s.waitFor("the search line", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "(busca) 'xyz': primeira pergunta xyz")
	})
	show("ctrl+r")
	s.write("\r") // accept: back in the box, not sent
	s.waitFor("the accepted match", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "│ › primeira pergunta xyz") && ptyPrompt(sc)
	})
	s.write("\x15") // Ctrl+U clears the line
	s.waitFor("Ctrl+U", 5*time.Second, func(sc string) bool { return !strings.Contains(sc, "│ › primeira pergunta xyz") })
	s.typeSlow("prim")
	s.write("\x1b[A") // Up skips "/help" (no "prim" prefix)
	s.waitFor("prefix Up", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "│ › primeira pergunta xyz") })
	s.write("\x15")
	settle()

	// 3. multi-line: Shift+Enter (CSI u and xterm forms), backslash-Enter and a
	// bracketed paste, all kept verbatim (indentation, tab)
	s.typeSlow("ECO-LINHAS um")
	s.write("\x1b[13;2u")
	s.typeSlow("  dois")
	s.write("\x1b[27;2;13~")
	s.write("\x1b[200~\ttres\nquatro\x1b[201~")
	s.typeSlow("\\")
	s.write("\r")
	s.typeSlow("cinco")
	s.waitFor("a five-row box", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "› ECO-LINHAS um") && strings.Contains(sc, "│     dois") && strings.Contains(sc, "cinco")
	})
	s.boxAligned("multi-line input")
	show("multi-line")
	s.write("\r")
	s.waitFor("the multi-line answer", 10*time.Second, func(sc string) bool { return strings.Contains(all(), "recuo=true tab=true") && ptyPrompt(sc) })
	if strings.Contains(all(), "recuo=false") {
		t.Error("multi-line input lost its indentation")
	}

	// 4. "@" opens the file picker from the name index; Enter inserts the path
	if err := os.MkdirAll(filepath.Join(s.root, "pacote"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"pacote/editor_util.go", "main.go", "README.md"} {
		if err := os.WriteFile(filepath.Join(s.root, f), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.typeSlow("veja @edut")
	s.waitFor("the @ picker", 5*time.Second, func(sc string) bool { return strings.Contains(line("▸ "), "pacote/editor_util.go") })
	s.boxAligned("@ picker open")
	show("picker")
	s.write("\r")
	s.waitFor("the inserted path", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "│ › veja @pacote/editor_util.go") && !strings.Contains(line("▸ "), "editor_util")
	})
	s.write("\r")
	s.waitFor("the mention sent", 10*time.Second, func(sc string) bool {
		return strings.Count(all(), "veja @pacote/editor_util.go") == 1 && ptyPrompt(sc)
	})

	// 5. Ctrl+O expands the last tool output and collapses it again
	s.typeSlow("rode curto")
	s.write("\r")
	s.waitFor("the short tool output", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "ctrl+o pra expandir") && ptyPrompt(sc)
	})
	s.write("\x0f")
	s.waitFor("the expansion", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "saída completa") && strings.Contains(sc, "ctrl+o recolhe") && ptyPrompt(sc)
	})
	s.boxAligned("after expanding")
	show("expanded")
	s.write("\x0f")
	s.waitFor("the collapse", 5*time.Second, func(sc string) bool {
		return !strings.Contains(all(), "saída completa") && ptyPrompt(sc)
	})
	s.boxAligned("after collapsing")

	// 6. during a turn: Esc clears the typed-ahead text, the next Esc interrupts
	s.typeSlow("demora")
	s.write("\r")
	s.waitFor("the slow stream", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "palavra2") })
	s.typeSlow("abc")
	s.waitFor("the typed-ahead text", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "› abc") })
	s.write("\x1b")
	s.waitFor("Esc to clear the typed text", 5*time.Second, func(sc string) bool {
		return !strings.Contains(sc, "› abc") && strings.Contains(sc, "esc pra interromper")
	})
	if strings.Contains(all(), "interrompendo") {
		t.Error("the first Esc with typed text interrupted the turn")
	}
	settle()
	s.write("\x1b")
	s.waitFor("the interrupt", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "interrompendo") && ptyPrompt(sc) })

	// 7. "?" lists the shortcuts in the space under the box, then hides them
	s.write("?")
	s.waitFor("the shortcut list", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "busca no histórico") && strings.Contains(sc, "quebra linha")
	})
	s.boxAligned("shortcut list")
	show("shortcuts")
	s.write("?")
	s.waitFor("the footer again", 5*time.Second, func(sc string) bool { return !strings.Contains(sc, "busca no histórico") && ptyPrompt(sc) })
	if l := line("? atalhos"); uiStrW(l) > w {
		t.Errorf("footer wider than the terminal: %q", l)
	}

	s.write("\x04")
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("jazz did not exit on Ctrl+D")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.vt.badEscapes) > 0 {
		t.Errorf("unexpected sequences: %q", s.vt.badEscapes)
	}
	for _, l := range s.vt.All() {
		if strings.Contains(l, "[B") || strings.Contains(l, "[13;2u") || strings.Contains(l, "\x1b") {
			t.Errorf("a key sequence leaked as text: %q", l)
		}
	}
}
