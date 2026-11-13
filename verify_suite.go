package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Verify before "done" (guard "suite", JAZZ_SUITE_GUARD=off disables it).
//
// Evals showed the classic weak-model ending: the tests ran after the edit,
// the run was red or only covered the new test file, and the final answer
// still said "tudo funcionando". Two deterministic checks at stop time, each
// at most once per turn:
//
//  1. the model's last test run after its last edit failed → one reminder
//     with the tail of that output;
//  2. the model never ran the project's full suite after editing → the
//     harness runs it itself (bounded) and, when red, hands the failures back.
//
// A failure that existed before the change is allowed: the reminder asks the
// model to say so in the final answer instead of claiming success.

const (
	vsTimeout = 3 * time.Minute
	vsMaxOut  = 3000
)

var (
	// vsRedRE: failure markers that survive a pipe hiding the exit code
	// (`go test | tail`, `pytest | head`).
	vsRedRE = regexp.MustCompile(`(?m)^FAILED \(|^--- FAIL|^FAIL\s|\b\d+ failed\b|^Tests:.*\b\d+ failed|^ERROR: test|^FAIL: test|\btest result: FAILED`)
	// vsNoTestsRE: the runner found nothing to run — not a verdict.
	vsNoTestsRE = regexp.MustCompile(`(?i)\bRan 0 tests\b|NO TESTS RAN|no tests ran|no test files|collected 0 items|No tests found`)
)

// vsRed: the run failed (exit code, or a failure marker when piped).
func vsRed(out ToolOutput) bool {
	if vsNoTestsRE.MatchString(out.Text) && !vsRedRE.MatchString(out.Text) {
		return false
	}
	return out.IsErr || vsRedRE.MatchString(out.Text)
}

// vsRecord notes the outcome of a test run the model made.
func vsRecord(st *hgState, cmd string, out ToolOutput) {
	st.lastTestRed = vsRed(out)
	st.lastTestCmd = cmd
	st.lastTestOut = truncateHeadTail(redactSecrets(out.Text), vsMaxOut)
	if !st.lastTestRed && vsIsFull(cmd, hgTestCommand(st.root)) {
		st.suiteRan = true // the model ran the whole suite green: nothing to add
	}
}

// vsIsFull: cmd runs the whole suite full (flags like -v/-q/-count=1 aside;
// a path, -run, -k or a file name narrows it).
func vsIsFull(cmd, full string) bool {
	if cmd == "" || full == "" {
		return false
	}
	seg := cmd
	for _, sep := range []string{"&&", ";", "|"} {
		parts := strings.Split(seg, sep)
		for _, p := range parts {
			if bgIsTestCmd(p) {
				seg = p
				break
			}
		}
	}
	norm := func(s string) []string {
		var out []string
		for _, w := range strings.Fields(s) {
			switch {
			case w == "-v" || w == "-q" || w == "-vv" || w == "-qq" || w == "--verbose" || strings.HasPrefix(w, "-count") ||
				w == "-x" || w == "--failfast" || w == "-race" || w == "-b" || w == "--buffer":
				continue
			case w == "python":
				w = "python3"
			}
			out = append(out, w)
		}
		return out
	}
	a, b := norm(seg), norm(full)
	if len(a) != len(b) {
		// `python3 -m unittest discover` == `python3 -m unittest`
		if len(a) == len(b)+1 && a[len(a)-1] == "discover" {
			a = a[:len(a)-1]
		} else {
			return false
		}
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// vsGate runs inside hgDoneGate. again=true: a reminder was appended.
func (a *Agent) vsGate(ctx context.Context, st *hgState) bool {
	if !a.hgOn("done") || !a.hgOn("suite") || !st.codeEdited || a.loop.opts.Until != "" || ctx.Err() != nil {
		return false
	}
	if st.lastTest > st.lastEdit && st.lastTestRed && !st.nudgedRed {
		st.nudgedRed = true
		cmd := st.lastTestCmd
		if cmd == "" {
			cmd = "the test run"
		}
		a.ui.Notice("guarda de conclusão: o último teste falhou — devolvendo ao modelo")
		a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>Your last test run after the edits FAILED:\n$ %s\n%s\nDo not report success. Fix the cause and re-run. If a failure existed before your change and is unrelated, say so explicitly in the final answer.</system-reminder>", cmd, st.lastTestOut)})
		return true
	}
	if st.suiteRan || a.obDeadlineWarned() {
		return false
	}
	full := hgTestCommand(a.tools.Root)
	if full == "" {
		return false
	}
	timeout := vsTimeout
	if !a.RunDeadline.IsZero() {
		left := time.Until(a.RunDeadline) / 4
		if left < 15*time.Second {
			return false
		}
		timeout = min(timeout, left)
	}
	st.suiteRan = true
	a.ui.ToolStart("verify-suite", "verify", full)
	a.ui.Thinking("rodando a suíte completa")
	line := a.tools.Shell.Dialect().subshellAt(a.tools.Root, full)
	r := a.tools.Shell.Run(ctx, line, timeout, nil)
	a.ui.StopThinking()
	if ctx.Err() != nil {
		a.ui.ToolEnd("verify-suite", "verify", ToolOutput{Text: "interrompido", IsErr: true, Display: Display{Summary: "interrompido"}}, r.Duration)
		return false
	}
	text := strings.TrimSpace(redactSecrets(r.Output))
	out := ToolOutput{Text: text, IsErr: r.ExitCode != 0}
	red := vsRed(out)
	sum := fmt.Sprintf("exit %d · suíte completa", r.ExitCode)
	if r.Reset {
		// timed out or the shell died: no verdict, never blame the model
		sum = "sem veredito: " + firstSentence(strings.Trim(r.Note, "[]"))
		red = false
	}
	if !r.Reset && vsNoTestsRE.MatchString(text) && !vsRedRE.MatchString(text) {
		sum, red = "nenhum teste encontrado", false
	}
	lines := nonEmptyLines(text)
	prev := lines
	if len(prev) > 4 {
		prev = prev[len(prev)-4:]
	}
	a.ui.ToolEnd("verify-suite", "verify", ToolOutput{Text: text, IsErr: red, Display: Display{Summary: sum, Preview: prev, Lines: len(lines)}}, r.Duration)
	dbg("suite", map[string]any{"cmd": full, "exit": r.ExitCode, "red": red, "reset": r.Reset})
	bgScoreRecord(a.TurnModel(), "suite", !red)
	if !red {
		return false
	}
	a.ui.Notice("guarda de conclusão: a suíte completa falhou — devolvendo ao modelo")
	a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>Before accepting your answer the harness ran the project's full test suite, and it FAILS:\n$ %s\n%s\nDo not report success. Fix what your change broke (existing tests included) and re-run. Do not edit or weaken existing tests to make them pass. If a failure existed before your change and is unrelated, say so explicitly in the final answer.</system-reminder>", full, truncateHeadTail(text, vsMaxOut))})
	return true
}
