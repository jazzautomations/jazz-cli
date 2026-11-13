package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// vsInfraErrRE: the test command never started (shell or interpreter error).
var vsInfraErrRE = regexp.MustCompile(`(?i)can't cd|cannot cd|cd: .*(no such|permission|not a directory)|command not found|not found: (python|go|npm|cargo|make)|operation not permitted|no module named (unittest|pytest)\b`)

// vsInfraErr: the output says the runner did not run, with no test verdict.
func vsInfraErr(text string) bool {
	return vsInfraErrRE.MatchString(text) && !vsRedRE.MatchString(text) && !strings.Contains(text, "Ran ") && !strings.Contains(text, "passed")
}

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
	if (st.suiteRan && st.origRan) || a.obDeadlineWarned() {
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
	if !st.suiteRan {
		if red, ran := a.vsRunSuite(ctx, st, full, timeout); red || !ran {
			return red
		}
	}
	if st.origRan || ctx.Err() != nil {
		return false
	}
	st.origRan = true
	return a.vsOriginalTests(ctx, st, full, timeout)
}

// vsRunSuite runs the full suite at the project root. red: the failures went
// back to the model; ran=false: no verdict (interrupted).
func (a *Agent) vsRunSuite(ctx context.Context, st *hgState, full string, timeout time.Duration) (red, ran bool) {
	st.suiteRan = true
	a.ui.ToolStart("verify-suite", "verify", full)
	a.ui.Thinking("rodando a suíte completa")
	line := a.tools.Shell.Dialect().subshellAt(a.tools.Root, full)
	r := a.tools.Shell.Run(ctx, line, timeout, nil)
	a.ui.StopThinking()
	if ctx.Err() != nil {
		a.ui.ToolEnd("verify-suite", "verify", ToolOutput{Text: "interrompido", IsErr: true, Display: Display{Summary: "interrompido"}}, r.Duration)
		return false, false
	}
	text := strings.TrimSpace(redactSecrets(r.Output))
	out := ToolOutput{Text: text, IsErr: r.ExitCode != 0}
	red = vsRed(out)
	sum := fmt.Sprintf("exit %d · suíte completa", r.ExitCode)
	if r.Reset {
		// timed out or the shell died: no verdict, never blame the model
		sum = "sem veredito: " + firstSentence(strings.Trim(r.Note, "[]"))
		red = false
	}
	if !r.Reset && vsNoTestsRE.MatchString(text) && !vsRedRE.MatchString(text) {
		sum, red = "nenhum teste encontrado", false
	}
	if red && vsInfraErr(text) {
		red, sum = false, "sem veredito: "+oneLine(text, 100)
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
		return false, !r.Reset
	}
	a.ui.Notice("guarda de conclusão: a suíte completa falhou — devolvendo ao modelo")
	a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>Before accepting your answer the harness ran the project's full test suite, and it FAILS:\n$ %s\n%s\nDo not report success. Fix what your change broke (existing tests included) and re-run. Do not edit or weaken existing tests to make them pass. If a failure existed before your change and is unrelated, say so explicitly in the final answer.</system-reminder>", full, truncateHeadTail(text, vsMaxOut))})
	return true, true
}

// ---------- the original tests against the new code ----------

const (
	vsCopyMaxFiles = 3000
	vsCopyMaxBytes = 64 << 20
)

// vsChangedTests: test files that existed when the turn began and were
// changed or deleted since (relative paths, sorted by walk order).
func vsChangedTests(st *hgState) []string {
	var out []string
	for abs, sn := range st.snap {
		if sn == nil {
			continue
		}
		rel, err := filepath.Rel(st.root, abs)
		if err != nil || hgKind(rel) != "test" {
			continue
		}
		cur, ok := hgReadFile(abs)
		if !ok || cur != sn.data {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

// vsCopyProject copies root into dst for a throwaway run: regular files only,
// heavy dependency dirs (node_modules, vendor, venvs) linked instead of copied,
// VCS and caches left out. false = too big or unreadable: skip the check.
func vsCopyProject(root, dst string) bool {
	files, bytes := 0, int64(0)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			switch d.Name() {
			case ".git", ".hg", ".svn", ".jazz", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".cache", ".idea", ".vscode", ".tox", ".nox":
				return filepath.SkipDir
			case "node_modules", "vendor", ".venv", "venv", "env", "target", "dist", "build", ".next":
				if err := os.Symlink(p, target); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += fi.Size()
		if files > vsCopyMaxFiles || bytes > vsCopyMaxBytes {
			return errVsTooBig
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, fi.Mode().Perm())
	})
	return err == nil
}

var errVsTooBig = errors.New("projeto grande demais para copiar")

// vsOriginalTests runs the turn-start version of the tests the model changed
// against the new code, in a copy of the project. A model that "updates the
// tests" by rewriting the old assertions to fit a regression passes its own
// suite; the old tests catch it. again=true: a reminder was appended.
func (a *Agent) vsOriginalTests(ctx context.Context, st *hgState, full string, timeout time.Duration) bool {
	changed := vsChangedTests(st)
	if len(changed) == 0 {
		return false
	}
	// inside the project: the Linux sandbox (bwrap) mounts a private tmpfs
	// on /tmp, so a copy there does not exist for the shell (d610, 13/11)
	base := filepath.Join(a.tools.Root, ".jazz")
	if os.MkdirAll(base, 0o755) != nil {
		return false
	}
	tmp, err := os.MkdirTemp(base, "origtests-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if !vsCopyProject(a.tools.Root, tmp) {
		dbg("suite", map[string]any{"orig": "skip-copy"})
		return false
	}
	for _, rel := range changed {
		sn := st.snap[filepath.Join(st.root, filepath.FromSlash(rel))]
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if os.WriteFile(dst, []byte(sn.data), sn.mode|0o200) != nil {
			return false
		}
	}
	label := "testes originais: " + strings.Join(changed, ", ")
	a.ui.ToolStart("verify-orig", "verify", label)
	a.ui.Thinking("rodando os testes originais contra o código novo")
	r := a.tools.Shell.Run(ctx, a.tools.Shell.Dialect().subshellAt(tmp, full), timeout, nil)
	a.ui.StopThinking()
	text := strings.TrimSpace(strings.ReplaceAll(redactSecrets(r.Output), tmp, "."))
	red := !r.Reset && ctx.Err() == nil && vsRed(ToolOutput{Text: text, IsErr: r.ExitCode != 0})
	sum := fmt.Sprintf("exit %d · %s", r.ExitCode, label)
	if r.Reset {
		sum = "sem veredito: " + firstSentence(strings.Trim(r.Note, "[]"))
	}
	if red && vsInfraErr(text) {
		// the runner never ran (cd, missing interpreter): the harness's
		// problem, never the model's
		red, sum = false, "sem veredito: "+oneLine(text, 100)
	}
	a.ui.ToolEnd("verify-orig", "verify", ToolOutput{Text: text, IsErr: red, Display: Display{Summary: oneLine(sum, 160)}}, r.Duration)
	dbg("suite", map[string]any{"orig": changed, "exit": r.ExitCode, "red": red})
	if !red {
		return false
	}
	a.ui.Notice("guarda de conclusão: os testes originais quebram com o código novo — devolvendo ao modelo")
	a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>You changed existing tests (%s). The harness ran their ORIGINAL version (from before this turn) against your new code, and they FAIL:\n$ %s\n%s\nBehavior the old tests pin must keep working unless the request explicitly changes it. Fix the code, not the old assertions; keep your new tests as additions. If the request really changes one of those behaviors, quote the assertion and the sentence of the request that changes it in your final answer.</system-reminder>", strings.Join(changed, ", "), full, truncateHeadTail(text, vsMaxOut))})
	return true
}
