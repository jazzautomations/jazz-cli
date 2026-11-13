package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	vsRedRE = lazyRx(`(?m)^FAILED \(|^--- FAIL|^FAIL\s|\b\d+ failed\b|^Tests:.*\b\d+ failed|^ERROR: test|^FAIL: test|\btest result: FAILED`)
	// vsNoTestsRE: the runner found nothing to run — not a verdict.
	vsNoTestsRE = lazyRx(`(?i)\bRan 0 tests\b|NO TESTS RAN|no tests ran|no test files|collected 0 items|No tests found`)
)

// vsInfraErrRE: the test command never started (shell or interpreter error).
var vsInfraErrRE = lazyRx(`(?i)can't cd|cannot cd|cd: .*(no such|permission|not a directory)|command not found|not found: (python|go|npm|cargo|make)|operation not permitted|no module named (unittest|pytest)\b`)

// vsSetupErrRE: the Go runner found no module to test; its FAIL line is
// about the environment, not a test verdict. "[setup failed]" is NOT here:
// it is what a broken import the model wrote prints, and that is red.
var vsSetupErrRE = lazyRx(`go\.mod file not found|does not contain main module|cannot find main module`)

// vsInfraErr: the output says the runner did not run, with no test verdict.
func vsInfraErr(text string) bool {
	if vsSetupErrRE.MatchString(text) && !strings.Contains(text, "--- FAIL") && !strings.Contains(text, "\nok ") {
		return true
	}
	return vsInfraErrRE.MatchString(text) && !vsRedRE.MatchString(text) && !strings.Contains(text, "Ran ") && !strings.Contains(text, "passed")
}

// vsRed: the run failed (exit code, or a failure marker when piped).
func vsRed(out ToolOutput) bool {
	if vsNoTestsRE.MatchString(out.Text) && !vsRedRE.MatchString(out.Text) {
		return false
	}
	return out.IsErr || vsRedRE.MatchString(out.Text)
}

// vsRecord notes the outcome of a test run the model made. It also tracks
// red→green: a red run (a real verdict, not a runner that never started)
// followed by an edit and then a green run means existing tests cover the
// change (quality.go does not demand a new test file then).
func vsRecord(st *hgState, cmd string, out ToolOutput) {
	st.lastTestRed = vsRed(out)
	st.lastTestCmd = cmd
	st.lastTestOut = truncateHeadTail(redactSecrets(out.Text), vsMaxOut)
	switch {
	case st.lastTestRed && !vsInfraErr(out.Text):
		st.redAt, st.redGreen = st.seq, false
	case st.lastTestRed: // no verdict
	case st.redAt > 0 && st.lastEdit > st.redAt:
		st.redGreen = true
	}
	if !st.lastTestRed && !bhPipedTest(cmd) && vsIsFull(cmd, hgTestCommand(st.root)) {
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

// vsPreexistingRE: the final answer says the remaining failure existed
// before the change and is unrelated to it.
var vsPreexistingRE = lazyRx(`(?i)pr[ée][- ]?existen|j[áa]\s+(falhava|estava\s+(falhando|quebrad)|quebrad|vinha\s+falhando)|(falha|falhava|quebrad\w*)\s+antes\s+d[aeo]s?\s+(minha\s+)?(mudan[çc]a|altera[çc][ãa]o|edi[çc][ãa]o)|existia\s+antes|n[ãa]o\s+(tem\s+)?relacionad|sem\s+rela[çc][ãa]o\s+com|n[ãa]o\s+tem\s+rela[çc][ãa]o|pre-?existing|already\s+(failing|failed|broken)|unrelated\s+to|failed\s+before\s+(my|the|this)\s+change`)

// vsSaidPreexisting: the model's latest answer (this turn) states the
// failure is old and unrelated: the one honest way to stop with a red suite.
func (a *Agent) vsSaidPreexisting() bool {
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		m := a.Msgs[i]
		if m.Role == "user" && !strings.HasPrefix(strings.TrimSpace(m.Content), "<system-reminder>") {
			return false // only this turn's answers count
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			return vsPreexistingRE.MatchString(m.Content)
		}
	}
	return false
}

// vsGate runs inside hgDoneGate. again=true: a reminder was appended. It
// sets st.failRed when the last test run after the last edit is still red
// after its one reminder and the answer does not say the failure is old and
// unrelated: hgDoneGate then ends the turn as StopQualityFailed, not done.
func (a *Agent) vsGate(ctx context.Context, st *hgState) bool {
	st.failRed = false
	if !a.hgOn("done") || !a.hgOn("suite") || !st.codeEdited || a.loop.opts.Until != "" || ctx.Err() != nil {
		return false
	}
	if st.nudgedRed && st.lastTest > st.lastEdit && st.lastTestRed && !vsInfraErr(st.lastTestOut) && !a.vsSaidPreexisting() {
		st.failRed = true
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
	// the harness's own run must leave no bytecode caches in the user's
	// tree (the model never sees them, so its "árvore limpa" would be false)
	caches := vsCacheDirs(a.tools.Root)
	d := a.tools.Shell.Dialect()
	line := d.subshellAt(a.tools.Root, vsNoBytecode(d, full))
	r := a.tools.Shell.Run(ctx, line, timeout, nil)
	vsRemoveNewCaches(a.tools.Root, caches)
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
	// the reminder below counts as the red-run reminder: a stop without a
	// green re-run after it ends the turn unverified (vsGate)
	st.seq++
	st.lastTest, st.lastTestRed, st.nudgedRed = st.seq, true, true
	st.lastTestCmd, st.lastTestOut = full, truncateHeadTail(text, vsMaxOut)
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
		if sn == nil || sn.own { // the agent's own tests from an earlier turn pin nothing the user asked for
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

// vsNoBytecode wraps the harness's suite command so Python writes no
// __pycache__ (PYTHONDONTWRITEBYTECODE=1, inherited by make/tox children);
// the persistent shell's own environment is left as it was.
func vsNoBytecode(d shellDialect, cmd string) string {
	if d.Kind == shPwsh {
		return "$__jzPyB=$env:PYTHONDONTWRITEBYTECODE; $env:PYTHONDONTWRITEBYTECODE='1'; try {\n" + cmd + "\n} finally { $env:PYTHONDONTWRITEBYTECODE=$__jzPyB }"
	}
	return "export PYTHONDONTWRITEBYTECODE=1 && " + cmd // inside subshellAt's subshell
}

// vsCacheNames: tool caches a test run may create in the project.
var vsCacheNames = map[string]bool{"__pycache__": true, ".pytest_cache": true}

// vsCacheDirs lists the cache directories under root (bounded walk; heavy
// and VCS dirs skipped).
func vsCacheDirs(root string) map[string]bool {
	out := map[string]bool{}
	visited := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		visited++
		if visited > hgMaxVisit {
			return filepath.SkipAll
		}
		if p == root {
			return nil
		}
		if vsCacheNames[d.Name()] {
			out[p] = true
			return filepath.SkipDir
		}
		if hgSkipDir(d.Name()) {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// vsRemoveNewCaches removes the cache directories the harness's run created
// (ones that were there before stay).
func vsRemoveNewCaches(root string, before map[string]bool) {
	for p := range vsCacheDirs(root) {
		if !before[p] {
			_ = os.RemoveAll(p)
		}
	}
}
