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

	mu     sync.Mutex
	cur    strings.Builder
	kept   string // final answer saved before a harness gate reopened the turn
	failed bool   // that gate ended the turn as failed (GateFailed)
	steps  int    // model requests, from TurnStats
	tools  int    // tool calls, from TurnStats
	errw   io.Writer
	outw   io.Writer
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

// KeepAnswer: a harness gate (quality checklist, auto-review, todo/verify/
// fake-action/stall nudge, Stop hook) reopens the turn after the model's
// final answer. That answer is kept, so stdout does not end up with only the
// reply to the gate (answer.go).
func (h *HeadlessUI) KeepAnswer() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushText()
	h.kept = acMergeAnswer(h.kept, h.Last)
	h.Last = ""
}

// GateFailed: the gate that reopened the turn still fails and ends it; the
// kept answer is printed with the reply instead of being replaced (answer.go).
func (h *HeadlessUI) GateFailed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failed = true
}

// endAnswer folds the kept answer into Last at the end of a turn (h.mu held).
func (h *HeadlessUI) endAnswer() {
	if h.kept != "" {
		if h.failed {
			h.Last = acMergeFailed(h.kept, h.Last)
		} else {
			h.Last = acMergeAnswer(h.kept, h.Last)
		}
		h.kept = ""
	}
	h.failed = false
}

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
	// actionable: the exact --allow-tools spec for this call, not just the flag name
	spec := CanonicalTool(tool)
	if spec == "bash" {
		spec = "Bash"
		if w := strings.Fields(strings.TrimSpace(detail)); len(w) > 0 {
			prefix := w[0]
			if len(w) > 1 && !strings.HasPrefix(w[1], "-") && !strings.ContainsAny(w[1], "=/'\"~$") {
				prefix += " " + w[1]
			}
			spec = fmt.Sprintf("Bash(%s:*)", prefix)
		}
	}
	if hlSecretReason(reason) {
		// credentials: no flag is the answer; never point at --yolo for a secret
		fmt.Fprintln(h.stderr(), msg+" — mexe em credenciais: nenhuma flag libera isso no -p; se precisar, abra o arquivo você mesmo")
		return Deny, ""
	}
	if reason == usReason {
		// the model asked to leave the sandbox: --yolo does not cover that (unsandboxed.go)
		fmt.Fprintln(h.stderr(), msg+" — o modelo pediu pra sair do sandbox; ele foi orientado a repetir dentro dele. Pra liberar no -p: --allow-unsandboxed")
		return Deny, ""
	}
	if !hlRuleCovers(tool, detail, reason) {
		// a rule does not cover this risk (agent.permit): suggesting one
		// would send the user to a flag that changes nothing
		fmt.Fprintln(h.stderr(), msg+" — isso uma regra --allow-tools não libera: rode no modo interativo (jazz) pra aprovar, ou --yolo se você confia no comando")
		return Deny, ""
	}
	fmt.Fprintln(h.stderr(), msg+" — use --allow-tools '"+spec+"' ou --yolo")
	return Deny, ""
}

// hlRuleCovers: an allow rule naming this call would let it run (mirrors
// agent.permit). A prompt with no risk of its own (untrusted folder, ask
// mode) is covered; with a risk, only sudo of a benign command and a
// CRITICAL system change are — a dangerous command, a secret read, egress,
// the fence or an injection suspicion are confirmed every time.
func hlRuleCovers(tool, detail, reason string) bool {
	if reason == "" {
		return true
	}
	if CanonicalTool(tool) != "bash" {
		return false // the hint would name the whole tool, which never covers a risk
	}
	cmd := strings.TrimSpace(detail)
	if cmd == "" {
		return false
	}
	if nlSudoCmd(cmd) {
		return DangerReason(nlStripSudo(cmd)) == "" && nlHostSecret(cmd) == ""
	}
	if DangerReason(cmd) != "" || SecretReason(cmd) != "" {
		return false
	}
	if _, eg := EgressTarget(cmd); eg {
		return false
	}
	for _, bad := range []string{"injection", "hook", "alvo de build", "fora da pasta", "dados do jazz"} {
		if strings.Contains(reason, bad) {
			return false
		}
	}
	for _, seg := range shellSegments(cmd) {
		if cbSegCritical(seg) != "" {
			return true
		}
	}
	return false
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
	h.endAnswer()
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
	h.endAnswer()
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

// hlSecretReason: the prompt is about credentials or sensitive config.
func hlSecretReason(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "credenc") || strings.Contains(r, "secret") || strings.Contains(r, "segredo") || strings.Contains(r, "chave privada")
}
