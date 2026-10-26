package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// HeadlessUI: for `jazz -p`. Final answer goes to stdout, progress (if verbose)
// to stderr. Safe for concurrent ToolStart/ToolEnd (parallel read-only calls).
type HeadlessUI struct {
	Verbose bool
	Last    string

	mu    sync.Mutex
	cur   strings.Builder
	steps int // model requests, from TurnStats
	tools int // tool calls, from TurnStats
	errw  io.Writer
	outw  io.Writer
}

func (h *HeadlessUI) stderr() io.Writer {
	if h.errw != nil {
		return h.errw
	}
	return os.Stderr
}

func (h *HeadlessUI) stdout() io.Writer {
	if h.outw != nil {
		return h.outw
	}
	return os.Stdout
}

// logf writes a progress line to stderr with -v (h.mu held or not needed).
func (h *HeadlessUI) logf(f string, a ...any) {
	if h.Verbose {
		fmt.Fprintf(h.stderr(), f+"\n", a...)
	}
}

func (h *HeadlessUI) StreamText(s string) {
	h.mu.Lock()
	h.cur.WriteString(s)
	h.mu.Unlock()
}
func (h *HeadlessUI) StreamReasoning(s string) {}
func (h *HeadlessUI) Thinking(string)          {}
func (h *HeadlessUI) StopThinking()            {}

// flushText ends the current assistant message (h.mu held).
func (h *HeadlessUI) flushText() {
	if t := strings.TrimSpace(h.cur.String()); t != "" {
		h.Last = t
		h.logf("· %s", oneLine(sanitizeTerm(t), 200))
	}
	h.cur.Reset()
}

func (h *HeadlessUI) ToolStart(id, name, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushText()
	h.logf("● %s %s", name, oneLine(visibleDetail(detail), 300))
}

func (h *HeadlessUI) ToolEnd(id, name string, out ToolOutput, d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sum := out.Display.Summary
	if sum == "" {
		sum = firstSentence(out.Text)
	}
	mark := "⎿"
	if out.IsErr {
		mark = "✗"
	}
	h.logf("  %s %s %s (%s)", mark, name, oneLine(sanitizeTerm(sum), 200), d.Round(time.Millisecond))
}

func (h *HeadlessUI) Notice(m string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logf("· %s", m)
}

func (h *HeadlessUI) Warn(m string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintln(h.stderr(), "! "+m)
}

func (h *HeadlessUI) Error(m string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintln(h.stderr(), "✗ "+m)
}

func (h *HeadlessUI) Approve(ctx context.Context, tool, detail, reason string) (Approval, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	msg := fmt.Sprintf("! bloqueado (modo -p sem confirmação): %s %s", tool, oneLine(visibleDetail(detail), 300))
	if reason != "" {
		msg += " (" + reason + ")"
	}
	fmt.Fprintln(h.stderr(), msg+" — use --allow-tools ou --yolo")
	return Deny, ""
}

func (h *HeadlessUI) SetMode(string) {}
func (h *HeadlessUI) Jobs(int)       {}
func (h *HeadlessUI) Bell()          {}

// Begin is part of HeadlessSink; text mode prints only a -v header.
func (h *HeadlessUI) Begin(info RunInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if info.SessionID != "" || info.Model != "" {
		h.logf("· sessão %s · %s", info.SessionID, info.Model)
	}
}

// Finish prints the final answer (text mode) to stdout.
func (h *HeadlessUI) Finish(res RunResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushText()
	text := res.Text
	if text == "" {
		text = h.Last
	}
	if text != "" {
		// model text must not move the cursor or set the clipboard (OSC52) even
		// in -p: it is piped into other tools and terminals (review LLM05)
		fmt.Fprintln(h.stdout(), sanitizeTerm(text))
	}
	turns, tools := res.Turns, res.Tools
	if turns == 0 {
		turns = h.steps
	}
	if tools == 0 {
		tools = h.tools
	}
	line := fmt.Sprintf("· %s · %d requisições · %d ferramentas · in %d out %d", res.Status, turns, tools, res.Usage.In, res.Usage.Out)
	if res.Cost > 0 {
		line += " · " + fmtCost(res.Cost)
	}
	if res.SessionID != "" {
		line += " · sessão " + res.SessionID
	}
	h.logf("%s", line)
}

func (h *HeadlessUI) TurnEnd(st TurnStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushText()
	h.steps += st.Steps
	h.tools += st.Tools
	line := fmt.Sprintf("✓ %s · %d ferramentas · in %d out %d", st.Dur.Round(time.Second), st.Tools, st.Usage.In, st.Usage.Out)
	if st.Cost > 0 {
		line += " · " + fmtCost(st.Cost)
	}
	if st.Redacted > 0 {
		line += " · " + rdTotalsText(st.Redacted)
	}
	h.logf("%s", line)
}
