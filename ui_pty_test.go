//go:build darwin || linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The real TUI in a real pseudo-terminal: the test binary re-runs itself as
// `jazz` (TestPTYChild) on the slave side of an 86x27 PTY, a scripted user
// types on the master side, and every byte jazz writes goes through the
// vtScreen emulator (with VTE-like reflow on resize). The model is a local
// fake SSE server.

// TestPTYChild is the child process: it runs main() with the args in
// JAZZ_PTY_ARGS. Skipped in normal runs.
func TestPTYChild(t *testing.T) {
	if os.Getenv("JAZZ_PTY_CHILD") != "1" {
		t.Skip("child of TestPTYSession")
	}
	os.Args = append([]string{"jazz"}, strings.Split(os.Getenv("JAZZ_PTY_ARGS"), "\x1f")...)
	main()
	os.Exit(0)
}

// ptyModel is the fake provider: the reply depends on the last message.
func ptyModel(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		last := body.Messages[len(body.Messages)-1]
		role, _ := last["role"].(string)
		content := fmt.Sprint(last["content"])
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		send := func(delta map[string]any, finish string) bool {
			ch := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}
			if finish != "" {
				ch["choices"].([]any)[0].(map[string]any)["finish_reason"] = finish
			}
			b, _ := json.Marshal(ch)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			return true
		}
		text := func(s string, every time.Duration) {
			for _, c := range uiChunks(s, 6) {
				if !send(map[string]any{"content": c}, "") {
					return
				}
				if every > 0 {
					time.Sleep(every)
				}
			}
			send(map[string]any{}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		switch {
		case role == "tool":
			text("pronto: 300 linhas.", 0)
		case strings.Contains(content, "oi ação"):
			text(ptyAnswer(), 4*time.Millisecond)
		case strings.Contains(content, "rode seq"):
			args, _ := json.Marshal(map[string]any{"command": "seq 1 300"})
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		case strings.Contains(content, "rode perigoso"):
			args, _ := json.Marshal(map[string]any{"command": "git branch -D jazz-pty-nada"})
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c2", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		case strings.Contains(content, "demora"):
			for i := 0; i < 300; i++ { // a slow stream, cut by Ctrl+C
				if !send(map[string]any{"content": fmt.Sprintf("palavra%d ", i)}, "") {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		case strings.Contains(content, "linha 50 colada"):
			text(fmt.Sprintf("recebi %d linhas", strings.Count(content, "colada")), 0)
		default:
			text("ok", 0)
		}
	}))
}

func ptyAnswer() string {
	var b strings.Builder
	b.WriteString("## Plano em três passos\n\nVou **explicar** o `parser` e depois mostrar código:\n\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "- passo %d: um texto comprido o bastante pra quebrar a linha no terminal estreito de oitenta colunas ação\n", i)
	}
	b.WriteString("\n```go\n")
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "fmt.Println(\"linha %d do código\") // comentário\n", i)
	}
	b.WriteString("```\n\nFIM-DA-RESPOSTA com 🎷 e acentuação.\n")
	return b.String()
}

type ptySession struct {
	t      *testing.T
	master *os.File
	vt     *vtScreen
	mu     sync.Mutex
	raw    strings.Builder
	cmd    *exec.Cmd
	done   chan struct{}
}

func (s *ptySession) write(str string) {
	s.t.Helper()
	if _, err := s.master.WriteString(str); err != nil {
		s.t.Fatalf("write: %v", err)
	}
}

// typeSlow types like a person (one key at a time).
func (s *ptySession) typeSlow(str string) {
	for _, r := range str {
		s.write(string(r))
		time.Sleep(3 * time.Millisecond)
	}
}

func (s *ptySession) screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.vt.Screen(), "\n")
}

func (s *ptySession) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.All()
}

// waitFor polls the screen (or the whole transcript) until cond holds.
func (s *ptySession) waitFor(what string, d time.Duration, cond func(screen string) bool) time.Duration {
	s.t.Helper()
	t0 := time.Now()
	for time.Since(t0) < d {
		if cond(s.screen()) {
			return time.Since(t0)
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	dump := s.vt.Dump()
	s.mu.Unlock()
	s.t.Fatalf("timeout waiting for %s\n%s", what, dump)
	return 0
}

func (s *ptySession) resize(w, h int) {
	s.mu.Lock()
	s.vt.resize(w, h)
	s.mu.Unlock()
	_ = unix.IoctlSetWinsize(int(s.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(h), Col: uint16(w)})
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGWINCH)
	}
}

func ptyStart(t *testing.T, term string) *ptySession {
	t.Helper()
	srv := ptyModel(t)
	t.Cleanup(srv.Close)
	cfg := map[string]any{"model": "fake/m1", "mode": "auto", "providers": map[string]any{
		"fake": map[string]any{"type": "openai", "base_url": srv.URL + "/v1", "no_key": true}}}
	return ptyStartWith(t, term, cfg, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
}

// ptyStartWith starts jazz in a PTY with cfg as config.json (nil = none:
// the keyless defaults) and extra environment.
func ptyStartWith(t *testing.T, term string, cfg map[string]any, env ...string) *ptySession {
	t.Helper()
	master, slaveName, err := ptyOpen()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Skipf("pty slave: %v", err)
	}
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 27, Col: 86})
	home, root := t.TempDir(), t.TempDir()
	if cfg != nil {
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPTYChild$")
	cmd.Env = append(os.Environ(), "JAZZ_PTY_CHILD=1", "JAZZ_PTY_ARGS="+strings.Join([]string{"-C", root}, "\x1f"),
		"JAZZ_HOME="+home, "TERM="+term, "COLORTERM=", "TERM_PROGRAM=", "NO_COLOR=", "JAZZ_NO_UPDATE_CHECK=1", "JAZZ_OFFLINE=")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	s := &ptySession{t: t, master: master, vt: newVT(t, 86, 27), cmd: cmd, done: make(chan struct{})}
	s.vt.reflow = true // VTE (roxterm) rewraps on resize
	go func() {
		defer close(s.done)
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.vt.feed(string(buf[:n]))
				s.raw.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				if err != io.EOF {
					return
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		if p := os.Getenv("JAZZ_PTY_RAW"); p != "" {
			s.mu.Lock()
			_ = os.WriteFile(p, []byte(s.raw.String()), 0o600)
			_ = os.WriteFile(p+".screen", []byte(s.vt.Dump()), 0o600)
			s.mu.Unlock()
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		master.Close()
	})
	return s
}

func TestPTYSession(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	for _, term := range []string{"xterm", "xterm-256color"} {
		t.Run(term, func(t *testing.T) { ptyScript(t, term) })
	}
}

// ptyPrompt: the input box with its footer is on screen (the welcome box
// has "? atalhos" too, but not the mode/model footer).
func ptyPrompt(sc string) bool {
	return strings.Contains(sc, "▸▸ auto · fake/m1") && strings.Contains(sc, "╭")
}

func ptyScript(t *testing.T, term string) {
	s := ptyStart(t, term)
	s.waitFor("the trust prompt", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "[s/N]") || ptyPrompt(sc) })
	if strings.Contains(s.screen(), "[s/N]") {
		s.write("s\r")
	}
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	if sc := s.screen(); !strings.Contains(sc, "modelo m1") {
		t.Errorf("no startup line:\n%s", sc)
	}

	// 1. accents and emoji in the input, honest status within 300 ms of Enter
	s.typeSlow("oi ação 🎷 — teste")
	s.waitFor("the typed text", 3*time.Second, func(sc string) bool { return strings.Contains(sc, "oi ação 🎷 — teste") })
	// the box borders line up with a wide character (emoji) inside
	s.boxAligned("with an emoji")
	// resize while the box is up: 86x27 -> 80x24 -> 86x27 leaves one box, no pieces
	s.resize(80, 24)
	time.Sleep(200 * time.Millisecond)
	s.resize(86, 27)
	time.Sleep(200 * time.Millisecond)
	s.boxAligned("after a resize")
	tops := 0
	for _, l := range s.all() {
		if strings.HasPrefix(l, "╭") || strings.HasPrefix(l, "╰") {
			tops++
		}
	}
	if tops != 4 { // welcome box + input box, top and bottom each
		s.mu.Lock()
		t.Errorf("%d box borders in the transcript after a resize at the prompt (want 4: leftover frames)\n%s", tops, s.vt.Dump())
		s.mu.Unlock()
	}
	if n := strings.Count(strings.Join(s.all(), "\n"), "▸▸ auto · fake/m1"); n != 1 {
		s.mu.Lock()
		t.Errorf("%d footers in the transcript after a resize at the prompt (leftover frames)\n%s", n, s.vt.Dump())
		s.mu.Unlock()
	}
	s.write("\r")
	took := s.waitFor("the status line", 3*time.Second, func(sc string) bool {
		return strings.Contains(sc, "esc pra interromper") || strings.Contains(sc, "gerando")
	})
	t.Logf("%s: status line %s after Enter", term, took.Round(time.Millisecond))
	if took > 300*time.Millisecond {
		t.Errorf("status line took %s (> 300 ms)", took)
	}
	// 2. resize in the middle of the stream: 86x27 -> 80x24 -> 86x27
	s.waitFor("the code block", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "linha 5 do código") })
	s.resize(80, 24)
	time.Sleep(150 * time.Millisecond)
	s.resize(86, 27)
	s.waitFor("the end of the answer", 15*time.Second, func(sc string) bool {
		return strings.Contains(sc, "FIM-DA-RESPOSTA") && ptyPrompt(sc)
	})
	all := s.all()
	count := func(sub string) int {
		n := 0
		for _, l := range all {
			if strings.Contains(l, sub) {
				n++
			}
		}
		return n
	}
	for _, line := range []string{"FIM-DA-RESPOSTA", "linha 25 do código", "Plano em três passos", "oi ação 🎷 — teste"} {
		if n := count(line); n != 1 {
			t.Errorf("%q appears %d times (overlap or leftover)\n%s", line, n, strings.Join(all, "\n"))
		}
	}
	for _, junk := range []string{"esc pra interromper", "gerando", "aguardando 1º token"} {
		if n := count(junk); n != 0 {
			t.Errorf("status %q left in the transcript (%d)\n%s", junk, n, strings.Join(all, "\n"))
		}
	}

	// 3. a 300-line tool output is cut with the spill hint
	s.typeSlow("rode seq")
	s.write("\r")
	s.waitFor("the tool result", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "pronto: 300 linhas.") && ptyPrompt(sc) })
	if sc := s.screen(); !strings.Contains(sc, "+297 linhas") || !strings.Contains(sc, "ctrl+o pra expandir") || strings.Contains(sc, "\n     150\n") {
		t.Errorf("tool output not truncated with the spill hint:\n%s", sc)
	}

	// 4. Ctrl+C in the middle of a stream
	s.typeSlow("demora")
	s.write("\r")
	s.waitFor("the slow stream", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "palavra3") })
	s.write("\x03")
	s.waitFor("the interrupt", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "interrompendo") && ptyPrompt(sc) })

	// 5. ESC clears the input; /help and /model
	s.typeSlow("lixo")
	s.write("\x1b")
	time.Sleep(150 * time.Millisecond) // a lone ESC is told apart from a sequence by a pause
	s.typeSlow("/help")
	s.write("\r")
	s.waitFor("/help", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "!comando roda no shell direto") && ptyPrompt(sc) })
	if strings.Contains(s.screen(), "lixo/help") {
		t.Error("ESC did not clear the input")
	}
	s.typeSlow("/model")
	s.write("\r")
	s.waitFor("/model", 5*time.Second, func(sc string) bool {
		return strings.Contains(strings.Join(s.all(), "\n"), "modelo atual: fake/m1") && ptyPrompt(sc)
	})

	// 6. a paste of 50 lines (bracketed) collapses to a marker and is sent whole
	var paste strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&paste, "linha %d colada\n", i)
	}
	s.write("\x1b[200~" + paste.String() + "\x1b[201~")
	s.waitFor("the paste marker", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "[colado #1 +50 linhas]") })
	s.write("\r")
	s.waitFor("the paste answer", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "recebi 50 linhas") && ptyPrompt(sc) })

	// the box sits at the bottom exactly once, nothing overlaps it
	sc := strings.Split(s.screen(), "\n")
	tops = 0
	for _, l := range sc {
		if strings.HasPrefix(l, "╭") {
			tops++
		}
	}
	if tops != 1 {
		t.Errorf("%d input boxes on screen\n%s", tops, strings.Join(sc, "\n"))
	}
	// 7. exit with Ctrl+D
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
	if s.vt.hidden {
		t.Error("cursor left hidden after exit")
	}
	for _, l := range s.vt.All() {
		if strings.Contains(l, "[0m") || strings.Contains(l, "[38;") || strings.Contains(l, "\x1b") {
			t.Errorf("escape sequence leaked as text: %q", l)
		}
	}
	if strings.Contains(s.raw.String(), "\x1b[?1049h") {
		t.Error("alternate screen used")
	}
}

// boxAligned: on screen there is exactly one input box and every row of it
// ends at the same cell as its top border.
func (s *ptySession) boxAligned(when string) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	end := -1
	boxes := 0
	start := 0 // the input box is the last one (the welcome box may be above)
	for y := 0; y < s.vt.h; y++ {
		if c := s.vt.row(y).cells[0]; c == '╭' {
			start = y
		}
	}
	for y := start; y < s.vt.h; y++ {
		r := s.vt.row(y)
		last := -1
		for x, c := range r.cells {
			if c != 0 && c != ' ' {
				last = x
			}
		}
		if last < 0 {
			continue
		}
		switch r.cells[0] {
		case '╭':
			boxes++
			end = last
		case '│', '╰':
			if end >= 0 && last != end {
				s.t.Errorf("%s: box row %d ends at cell %d, top border at %d\n%s", when, y, last, end, s.vt.Dump())
				return
			}
		}
	}
	if boxes != 1 {
		s.t.Errorf("%s: %d input boxes on screen\n%s", when, boxes, s.vt.Dump())
	}
}

// TestPTYApproveKeys: a confirmation is one line answered by one key, no
// typing + Enter: n denies, Enter runs, a (sempre) runs and is never asked
// again in this project.
func TestPTYApproveKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	s := ptyStart(t, "xterm-256color")
	s.waitFor("the trust prompt", 15*time.Second, func(sc string) bool { return strings.Contains(sc, "[s/N]") || ptyPrompt(sc) })
	if strings.Contains(s.screen(), "[s/N]") {
		s.write("s\r")
	}
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	count := func(sub string) int { return strings.Count(strings.Join(s.all(), "\n"), sub) }
	prompts := func() int { return count("│ Bash  git branch -D jazz-pty-nada") } // the answer replaces the question line
	results := func() int { return count("pronto: 300 linhas.") }
	ask := func(key string, wantPrompt bool) {
		t.Helper()
		p0, r0 := prompts(), results()
		s.typeSlow("rode perigoso")
		s.write("\r")
		if wantPrompt {
			s.waitFor("the one-line prompt", 10*time.Second, func(sc string) bool { return prompts() > p0 && strings.Contains(sc, "? Rodar este comando?") })
			var line string
			for _, l := range strings.Split(s.screen(), "\n") {
				if strings.Contains(l, "? Rodar este comando?") {
					line = l
				}
			}
			for _, want := range []string{"perigo: apaga branch do git", "enter sim", "a sempre", "n não"} {
				if !strings.Contains(line, want) {
					t.Errorf("prompt line lacks %q: %q", want, line)
				}
			}
			time.Sleep(uiApproveIdle + 100*time.Millisecond) // a key typed at once is not an answer (bug 11)
			s.write(key)
		}
		s.waitFor("the turn to end", 15*time.Second, func(sc string) bool { return results() > r0 && ptyPrompt(sc) })
		if got := prompts() - p0; wantPrompt && got != 1 || !wantPrompt && got != 0 {
			s.mu.Lock()
			t.Errorf("key %q: %d prompts (want prompt=%v)\n%s", key, got, wantPrompt, s.vt.Dump())
			s.mu.Unlock()
		}
	}
	ask("n", true)
	if count("● não") < 1 && count("* não") < 1 {
		t.Error("n did not deny")
	}
	ask("\r", true)
	ask("a", true)
	if count("não pergunto de novo neste projeto") != 1 {
		t.Error("a (sempre) did not remember")
	}
	ask("", false) // remembered: runs without a prompt
	if strings.Contains(strings.Join(s.all(), "\n"), "confirmar?") || strings.Contains(strings.Join(s.all(), "\n"), "digite sim") {
		t.Error("old typed confirmation still shown")
	}
	s.write("\x04")
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("jazz did not exit on Ctrl+D")
	}
}
