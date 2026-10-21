package main

// quality.go: the "old-coder" output bar (docs/research/skills-personas-scripts.md
// section B). When the model wants to stop, acQualityGate (before --until and
// --review) runs a deterministic checklist over the files the turn changed
// through write/edit (pre-images kept by bgPreTool; files changed only by bash
// are not covered):
//
//  1. tests ran after the last edit, when the project has a known test
//     command (skipped with --until, which is stronger);
//  2. no debug prints added in non-test code;
//  3. no new TODO/FIXME/XXX/HACK without a `shortcut:` tag;
//  4. no commented-out code blocks (3+ lines);
//  5. no added or changed function over 60 lines (strict: 40);
//  6. strict only: no throwaway names (tmp, foo, data2, x1 …) outside tests;
//  7. zero new lint/type errors on the added lines when a linter is available
//     (gofmt + go vet, ruff or pyflakes, the project's eslint);
//  8. a test file touched when new logic (branches/loops) was added, per task
//     kind: bugfix only when a test seam exists, refactor never (instead the
//     test count must not drop), strict always.
//
// Failures go back to the model once as a numbered list; a second failure
// ends the turn with StopQualityFailed (exit code 9 in -p; in interactive mode
// it is only reported). Config `quality: off|standard|strict` (default
// standard), flag --quality, env JAZZ_QUALITY. Sensitive tasks (auth, money,
// migrations…, by keywords or changed paths) are checked as strict.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Quality levels.
const (
	qgOff      = "off"
	qgStandard = "standard"
	qgStrict   = "strict"
)

// qgMaxIssues caps the list sent back to the model.
const qgMaxIssues = 12

// qgLintTimeout bounds one linter run (go vet compiles: slow on the Pentium M).
var qgLintTimeout = 2 * time.Minute

// qgPre is a file's content before the turn first changed it.
type qgPre struct {
	existed bool
	skip    bool // too big or unreadable: not checked
	data    string
}

// qgTurn is the gate's per-turn state.
type qgTurn struct {
	fed     bool // the one feedback round was used
	lastKey string
	issues  []qgIssue
}

// qgIssue is one checklist failure.
type qgIssue struct {
	File string // relative path ("" = turn-level)
	Line int
	Msg  string
}

func (i qgIssue) String() string {
	switch {
	case i.File == "":
		return i.Msg
	case i.Line > 0:
		return fmt.Sprintf("%s:%d: %s", i.File, i.Line, i.Msg)
	}
	return i.File + ": " + i.Msg
}

// qgFile is one changed file as the checks see it.
type qgFile struct {
	Rel, Abs string
	Old, New string
	Existed  bool
	Added    map[int]bool // 1-based line numbers of New that are new
	Test     bool
	Code     bool
}

// qualityLevel is the effective quality mode.
func (a *Agent) qualityLevel() string {
	return bgSetting(a.loop.opts.Quality, "JAZZ_QUALITY", a.cfg.Quality, qgStandard, qgOff, qgStandard, qgStrict)
}

// qgRemember keeps the content of abs before its first change this turn.
func (a *Agent) qgRemember(bt *bgTurn, abs string) {
	if _, ok := bt.pre[abs]; ok || a.qualityLevel() == qgOff {
		return
	}
	p := &qgPre{}
	if fi, err := os.Stat(abs); err == nil {
		p.existed = true
		if fi.IsDir() || fi.Size() > bgMaxHash {
			p.skip = true
		} else if b, err := os.ReadFile(abs); err == nil {
			p.data = string(b)
		} else {
			p.skip = true
		}
	}
	bt.pre[abs] = p
	bt.order = append(bt.order, abs)
}

// acQualityGate runs the checklist when the model wants to stop. again=true:
// feedback was appended and the turn continues; failed=true: the checklist
// still fails after its feedback round (the caller ends with StopQualityFailed
// unless a stronger reason applies).
func (a *Agent) acQualityGate(ctx context.Context) (again, failed bool) {
	bt := a.bgTurnState()
	if bt == nil || a.mode() == "plan" || ctx.Err() != nil {
		return false, false
	}
	level := a.qualityLevel()
	if level == qgOff {
		return false, false
	}
	files := a.qgChanges(bt)
	if len(files) == 0 {
		return false, false
	}
	strict := level == qgStrict || bt.task.Sensitive
	for _, f := range files {
		strict = strict || bgSensitiveRE.MatchString(filepath.ToSlash(f.Rel))
	}
	testsAfter := bt.lastTest > bt.lastEdit
	key := qgKey(files, strict, testsAfter)
	issues := bt.qg.issues
	if key != bt.qg.lastKey {
		a.ui.Thinking("checando a qualidade")
		issues = qgCheck(ctx, qgEnv{root: a.tools.Root, kind: bt.task.Kind, strict: strict, until: a.loop.opts.Until != "", testsAfter: testsAfter, noTestEdit: bt.task.NoTestEdit, newTestOnly: a.hg != nil && a.hg.policy != hgAllow && a.hgOn("tests")}, files)
		a.ui.StopThinking()
		if ctx.Err() != nil {
			return false, false
		}
		bt.qg.lastKey, bt.qg.issues = key, issues
		bgScoreRecord(a.TurnModel(), "quality", len(issues) == 0)
		dbg("quality", map[string]any{"files": len(files), "issues": len(issues), "strict": strict, "fed": bt.qg.fed})
	}
	if len(issues) == 0 {
		if bt.qg.fed {
			a.ui.Notice("checagem de qualidade passou")
		}
		return false, false
	}
	if !bt.qg.fed {
		bt.qg.fed = true
		a.ui.Notice(fmt.Sprintf("checagem de qualidade: %d %s — devolvendo ao modelo", len(issues), uiPl(len(issues), "problema", "problemas")))
		a.append(Message{Role: "user", Content: qgFeedback(issues)})
		return true, false
	}
	a.ui.Warn(fmt.Sprintf("checagem de qualidade ainda falha (%d %s): %s", len(issues), uiPl(len(issues), "item", "itens"), oneLine(issues[0].String(), 140)))
	return false, true
}

// qgFeedback is the one structured reminder sent back to the model.
func qgFeedback(issues []qgIssue) string {
	var b strings.Builder
	b.WriteString("<system-reminder>The harness checked the changes of this turn (deterministic checklist) and found:\n")
	for i, is := range issues {
		if i == qgMaxIssues {
			fmt.Fprintf(&b, "… and %d more\n", len(issues)-i)
			break
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, is.String())
	}
	b.WriteString("Fix the real ones, re-run the tests, then give your final answer. If an item is a false positive, say why in one line. This check reports only once.</system-reminder>")
	return b.String()
}

// qgChanges builds the changed files from the pre-images.
func (a *Agent) qgChanges(bt *bgTurn) []qgFile {
	var out []qgFile
	for _, abs := range bt.order {
		p := bt.pre[abs]
		if p == nil || p.skip {
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() || fi.Size() > bgMaxHash {
			continue // deleted or now too big
		}
		b, err := os.ReadFile(abs)
		if err != nil || string(b) == p.data && p.existed {
			continue
		}
		rel := a.tools.rel(abs)
		if filepath.IsAbs(rel) {
			continue // outside the project (scratch files in /tmp are not deliverables)
		}
		f := qgFile{Rel: rel, Abs: abs, Old: toLF(p.data), New: toLF(string(b)), Existed: p.existed, Test: qgIsTestFile(rel), Code: isCodeFile(abs)}
		f.Added = qgAddedLines(f.Old, f.New)
		out = append(out, f)
	}
	return out
}

func qgKey(files []qgFile, strict, testsAfter bool) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.Abs, len(f.New))
		h.Write([]byte(f.New))
	}
	fmt.Fprintf(h, "%v%v", strict, testsAfter)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// qgAddedLines: line numbers of new whose text is not in old (multiset match,
// so moved lines count as unchanged). Cheap: O(lines), no LCS.
func qgAddedLines(old, new string) map[int]bool {
	have := map[string]int{}
	for _, l := range strings.Split(old, "\n") {
		have[strings.TrimRight(l, " \t\r")]++
	}
	out := map[int]bool{}
	for i, l := range strings.Split(new, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if have[l] > 0 {
			have[l]--
			continue
		}
		if strings.TrimSpace(l) != "" {
			out[i+1] = true
		}
	}
	return out
}

var qgTestFileRE = regexp.MustCompile(`(?i)(_test\.go|(^|/)test_[^/]*\.py|_test\.py|\.(test|spec)\.[cm]?[jt]sx?|_spec\.rb|(^|/)(tests?|__tests__|spec)/|Tests?\.(java|kt|cs|swift))$|(^|/)(tests?|__tests__|spec)/`)

func qgIsTestFile(rel string) bool { return qgTestFileRE.MatchString(filepath.ToSlash(rel)) }

// ---------- the checklist ----------

// qgEnv is what the checks need besides the files.
type qgEnv struct {
	root        string
	kind        string // task kind (budget.go)
	strict      bool
	until       bool // --until set: it proves the tests, item 1 is skipped
	testsAfter  bool // a test command ran after the last edit
	noTestEdit  bool // the request forbids changing tests: never demand one
	newTestOnly bool // existing test files are protected (guard_honest.go): ask for a new one
}

var (
	qgDebugAllRE = regexp.MustCompile(`\[DBG-jz\]|\bbreakpoint\(\)|\bpdb\.set_trace\(|\bipdb\.set_trace\(|^\s*debugger;?\s*$|\bconsole\.(debug|trace)\(|\bdbg!\(|\b(fmt|log)\.Print(ln|f)?\(\s*"(DEBUG|DBG|debug)|\bprintln!\(\s*"(DEBUG|DBG)|\bvar_dump\(|\bprint_r\(|\bputs\s+["']DEBUG|\bSystem\.out\.println\(\s*"(DEBUG|DBG)`)
	qgDebugStdRE = regexp.MustCompile(`\b(print|console\.log)\(\s*(f?["'\x60]\s*(DEBUG|DBG|debug|here|HERE|>>>|xxx|XXX|---)|f["'][^"']*=\})`)
	qgDebugStrRE = regexp.MustCompile(`^\s*(print\(|console\.log\()`)
	qgTodoRE     = regexp.MustCompile(`\b(TODO|FIXME|XXX|HACK)\b`)
	qgCommentRE  = regexp.MustCompile(`^\s*(//|#|--|/\*|\*)\s?(.*)$`)
	// a comment body that is code, not prose: statement punctuation at the
	// end, an assignment, a call on its own, or a keyword followed by code
	qgCodeishRE  = regexp.MustCompile(`[;{}]\s*$|^\s*[\w.\[\]]+\s*(:=|\+=|-=|=[^=])\s*\S|^\s*[\w.]+\(.*\)\s*;?\s*$|^\s*(if|elif|while|for|switch|case|else|def|class)\b.*[:{]\s*$|^\s*(return|import|from|func|var|let|const)\b.*[(=;"'{]|^\s*(print|fmt\.\w+|console\.\w+)\(|^\s*(return|break|continue|pass)\s*;?\s*$`)
	qgLogicRE    = regexp.MustCompile(`^\s*(\}\s*)?(if|for|while|switch|case|elif|else\s+if|match|catch|except|select)\b|\bfor\s*\(|\bwhile\s*\(`)
	qgNameRE     = regexp.MustCompile(`\b(tmp|temp|foo|bar|baz|data[0-9]|x[0-9]|thing|stuff|asdf)\b\s*(:=|=[^=]|,\s*\w+\s*:=)`)
	qgTestFuncRE = regexp.MustCompile(`^func Test|^\s*(async\s+)?def test_|\b(it|test)\(\s*["'\x60]|#\[test\]|@Test\b`)
)

// qgCheck runs the checklist; issues come back in file order.
func qgCheck(ctx context.Context, env qgEnv, files []qgFile) []qgIssue {
	var issues []qgIssue
	add := func(f qgFile, line int, msg string) {
		issues = append(issues, qgIssue{File: f.Rel, Line: line, Msg: msg})
	}
	anyCode, anyTest, logic := false, false, 0
	testsBefore, testsNow := 0, 0
	for _, f := range files {
		anyCode = anyCode || f.Code
		anyTest = anyTest || (f.Test && f.Code)
		if f.Test {
			testsBefore += len(qgMatchLines(f.Old, qgTestFuncRE))
			testsNow += len(qgMatchLines(f.New, qgTestFuncRE))
		}
		if !f.Code {
			continue
		}
		lines := strings.Split(f.New, "\n")
		retuned := qgRetunedLines(f.Old, f.New, f.Added)
		run, runStart := 0, 0
		for n := 1; n <= len(lines); n++ {
			l := lines[n-1]
			if !f.Added[n] {
				if run >= 3 {
					add(f, runStart, fmt.Sprintf("commented-out code block (%d lines): delete it (git keeps history)", run))
				}
				run = 0
				continue
			}
			cm := qgCommentRE.FindStringSubmatch(l)
			if cm != nil && qgCodeishRE.MatchString(cm[2]) && !strings.HasPrefix(strings.TrimSpace(l), "//go:") && !strings.HasPrefix(strings.TrimSpace(l), "#!") {
				if run == 0 {
					runStart = n
				}
				run++
			} else {
				if run >= 3 {
					add(f, runStart, fmt.Sprintf("commented-out code block (%d lines): delete it (git keeps history)", run))
				}
				run = 0
			}
			if m := qgTodoRE.FindStringIndex(l); m != nil && qgInComment(l, m[0]) && !strings.Contains(l, "shortcut:") {
				add(f, n, fmt.Sprintf("new %s without a `shortcut: <limit>, <when to upgrade>` tag: finish it or tag it", l[m[0]:m[1]]))
			}
			if f.Test {
				continue
			}
			if qgDebugAllRE.MatchString(l) || qgDebugStdRE.MatchString(l) || (env.strict && qgDebugStrRE.MatchString(l)) {
				add(f, n, "debug print or breakpoint left in non-test code: "+oneLine(strings.TrimSpace(l), 80))
			}
			if env.strict && qgNameRE.MatchString(l) {
				add(f, n, "throwaway name in new code: "+oneLine(strings.TrimSpace(l), 80))
			}
			if cm == nil && qgLogicRE.MatchString(l) && !retuned[n] {
				logic++
			}
		}
		if run >= 3 {
			add(f, runStart, fmt.Sprintf("commented-out code block (%d lines): delete it (git keeps history)", run))
		}
		limit := 60
		if env.strict {
			limit = 40
		}
		for _, fn := range qgFuncs(f.Rel, lines) {
			if fn.end-fn.start+1 > limit && qgTouches(f.Added, fn.start, fn.end) {
				add(f, fn.start, fmt.Sprintf("function %s is %d lines (limit %d): split it", fn.name, fn.end-fn.start+1, limit))
			}
		}
	}
	testCmd := qgTestCommand(env.root)
	if !env.until && anyCode && testCmd != "" && !env.testsAfter {
		issues = append(issues, qgIssue{Msg: fmt.Sprintf("tests were not run after your last edit: run `%s` (or the narrowest test that covers the change) and read the result", testCmd)})
	}
	if env.kind == "refactor" && testsNow < testsBefore {
		issues = append(issues, qgIssue{Msg: fmt.Sprintf("the refactor removed %d test function(s): a refactor keeps the test count", testsBefore-testsNow)})
	}
	if logic >= 2 && !anyTest && qgNeedTest(env, files, testCmd) {
		how := "add or extend a test that covers it"
		if env.newTestOnly {
			how = "add a test that covers it in a NEW test file (the existing test files are protected in this task)"
		}
		issues = append(issues, qgIssue{Msg: fmt.Sprintf("new logic (%d branch/loop lines) but no test file was touched: %s", logic, how)})
	}
	if ctx.Err() == nil {
		issues = append(issues, qgLintRun(ctx, env.root, files)...)
	}
	return issues
}

// qgNeedTest: whether new logic must come with a touched test file.
func qgNeedTest(env qgEnv, files []qgFile, testCmd string) bool {
	switch {
	case env.noTestEdit, env.kind == "refactor": // moved code is not new logic
		return false
	case env.strict: // only where the project tests at all: no new test suite nobody asked for
		return testCmd != "" || qgHasSeam(env.root, files)
	case env.kind == "bugfix":
		return qgHasSeam(env.root, files)
	}
	return testCmd != "" || qgHasSeam(env.root, files)
}

// qgHasSeam: the project already has tests near the change (a test file in the
// directory of a changed code file, or a top-level tests directory).
func qgHasSeam(root string, files []qgFile) bool {
	for _, d := range []string{"tests", "test", "__tests__", "spec"} {
		if fi, err := os.Stat(filepath.Join(root, d)); err == nil && fi.IsDir() {
			return true
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		dir := filepath.Dir(f.Abs)
		if !f.Code || seen[dir] {
			continue
		}
		seen[dir] = true
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && qgIsTestFile(e.Name()) {
				return true
			}
		}
	}
	return false
}

var qgLiteralRE = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|\b\d+(?:\.\d+)?\b`)

// qgRetunedLines: added lines that only change literals of a removed line
// (`if pct > 50:` -> `if pct > 30:`). Retuning a constant adds no branch, so it
// does not count as new logic that needs a new test (eval v2, 03-arquivo-grande).
func qgRetunedLines(old, new string, added map[int]bool) map[int]bool {
	shape := func(l string) string { return strings.TrimSpace(qgLiteralRE.ReplaceAllString(l, "\x00")) }
	removed := map[string]int{}
	have := map[string]int{}
	for _, l := range strings.Split(new, "\n") {
		have[strings.TrimRight(l, " \t\r")]++
	}
	for _, l := range strings.Split(old, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if have[l] > 0 {
			have[l]--
			continue
		}
		removed[shape(l)]++
	}
	out := map[int]bool{}
	lines := strings.Split(new, "\n")
	for n := range added {
		if s := shape(lines[n-1]); removed[s] > 0 {
			removed[s]--
			out[n] = true
		}
	}
	return out
}

// qgInComment: position i of l is inside a line comment (//, #, --, /* or a
// leading * of a block), not in code or a string before it.
func qgInComment(l string, i int) bool {
	pre := l[:i]
	if t := strings.TrimSpace(pre); strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
		return true
	}
	for _, mk := range []string{"//", "#", "-- ", "/*"} {
		if j := strings.Index(pre, mk); j >= 0 && strings.Count(pre[:j], `"`)%2 == 0 && strings.Count(pre[:j], "'")%2 == 0 {
			return true
		}
	}
	return false
}

func qgMatchLines(s string, re *regexp.Regexp) []int {
	var out []int
	for i, l := range strings.Split(s, "\n") {
		if re.MatchString(l) {
			out = append(out, i+1)
		}
	}
	return out
}

func qgTouches(added map[int]bool, start, end int) bool {
	for n := start; n <= end; n++ {
		if added[n] {
			return true
		}
	}
	return false
}

// ---------- function spans ----------

type qgFunc struct {
	name       string
	start, end int // 1-based, inclusive
}

var (
	qgGoFuncRE = regexp.MustCompile(`^func\s+(\([^)]*\)\s*)?(\w+)`)
	qgPyFuncRE = regexp.MustCompile(`^(\s*)(async\s+)?def\s+(\w+)`)
	// brace languages: one regexp per shape, group 1 = the name
	qgBraceFuncREs = []*regexp.Regexp{
		regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\*?\s*(\w*)`),
		regexp.MustCompile(`^\s*(?:pub(?:\([\w:]+\))?\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s+(\w+)`),
		regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|\w+)\s*=>\s*\{\s*$`),
		regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|override|async|virtual|inline)\s+)*[\w<>\[\],.*&:]+\s+(\w+)\s*\([^;]*\)\s*(?:const\s*)?(?:throws\s+[\w., ]+)?\{\s*$`),
		regexp.MustCompile(`^\s*(?:async\s+)?(\w+)\s*\([^)]*\)\s*\{\s*$`),
	}
	qgNotFuncRE  = regexp.MustCompile(`^\s*(\}\s*)?(if|for|while|switch|catch|else|do|try|return|with|new)\b`)
	qgMakeTestRE = regexp.MustCompile(`(?m)^(test|check):`)
)

// qgFuncs finds function spans by language: Go (gofmt'd: ends at a lone "}"),
// Python (indentation) and brace languages (brace counting).
func qgFuncs(rel string, lines []string) []qgFunc {
	ext := strings.ToLower(filepath.Ext(rel))
	var out []qgFunc
	switch ext {
	case ".go":
		for i, l := range lines {
			m := qgGoFuncRE.FindStringSubmatch(l)
			if m == nil || strings.HasSuffix(strings.TrimSpace(l), "}") {
				continue
			}
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == "}" {
					out = append(out, qgFunc{m[2], i + 1, j + 1})
					break
				}
				if qgGoFuncRE.MatchString(lines[j]) {
					break
				}
			}
		}
	case ".py":
		for i, l := range lines {
			m := qgPyFuncRE.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			ind, end := len(m[1]), i
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" {
					continue
				}
				if len(lines[j])-len(strings.TrimLeft(lines[j], " \t")) <= ind {
					break
				}
				end = j
			}
			out = append(out, qgFunc{m[3], i + 1, end + 1})
		}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".rs", ".java", ".kt", ".c", ".h", ".cc", ".cpp", ".cs", ".swift", ".scala", ".php", ".dart", ".vue", ".svelte":
		for i, l := range lines {
			if qgNotFuncRE.MatchString(l) {
				continue
			}
			var m []string
			for _, re := range qgBraceFuncREs {
				if m = re.FindStringSubmatch(l); m != nil {
					break
				}
			}
			if m == nil {
				continue
			}
			name := m[1]
			if name == "" {
				name = "(anonymous)"
			}
			depth, opened := 0, false
			for j := i; j < len(lines); j++ {
				o, c := qgBraces(lines[j])
				depth += o - c
				opened = opened || o > 0
				if opened && depth <= 0 {
					out = append(out, qgFunc{name, i + 1, j + 1})
					break
				}
			}
		}
	}
	return out
}

// qgBraces counts { and } outside simple string literals and // comments.
func qgBraces(l string) (open, close int) {
	var q byte
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case q != 0:
			if c == '\\' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'' || c == '`':
			q = c
		case c == '/' && i+1 < len(l) && l[i+1] == '/':
			return
		case c == '{':
			open++
		case c == '}':
			close++
		}
	}
	return
}

// ---------- test command detection ----------

// qgTestCommand guesses the project's test command from its marker files ("" = unknown).
func qgTestCommand(root string) string {
	has := func(n string) bool { _, err := os.Stat(filepath.Join(root, n)); return err == nil }
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(root, n))
		if len(b) > 256<<10 {
			b = b[:256<<10]
		}
		return string(b)
	}
	switch {
	case has("go.mod"):
		return "go test ./..."
	case has("Cargo.toml"):
		return "cargo test"
	case has("package.json"):
		if s := read("package.json"); strings.Contains(s, `"test"`) && !strings.Contains(s, "no test specified") {
			return "npm test"
		}
	}
	if has("pytest.ini") || has("conftest.py") || strings.Contains(read("pyproject.toml"), "[tool.pytest") || strings.Contains(read("setup.cfg"), "[tool:pytest]") || has("tox.ini") {
		return "python3 -m pytest -q"
	}
	if qgMakeTestRE.MatchString(read("Makefile")) {
		return "make test"
	}
	return ""
}

// ---------- linters ----------

// qgLintRun is the lint step (a variable so tests can replace it).
var qgLintRun = qgLint

var qgLintLineRE = regexp.MustCompile(`^(?:vet: )?(.+?):(\d+):(?:(\d+):)?\s*(.*)$`)

// qgLint runs the linters available for the changed files and keeps only
// findings on added lines (so old findings never count as new).
func qgLint(ctx context.Context, root string, files []qgFile) []qgIssue {
	byExt := map[string][]qgFile{}
	for _, f := range files {
		if f.Code {
			ext := strings.ToLower(filepath.Ext(f.Abs))
			byExt[ext] = append(byExt[ext], f)
		}
	}
	var issues []qgIssue
	if gs := byExt[".go"]; len(gs) > 0 {
		issues = append(issues, qgLintGo(ctx, root, gs)...)
	}
	if ps := byExt[".py"]; len(ps) > 0 {
		issues = append(issues, qgLintPy(ctx, root, ps)...)
	}
	var js []qgFile
	for _, e := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} {
		js = append(js, byExt[e]...)
	}
	if len(js) > 0 {
		if bin := filepath.Join(root, "node_modules", ".bin", "eslint"); qgExists(bin) {
			out, _ := qgRun(ctx, root, nil, bin, append([]string{"--format", "unix"}, qgAbsList(js)...)...)
			issues = append(issues, qgFilterLint(root, root, out, js, "eslint")...)
		}
	}
	return issues
}

func qgLintGo(ctx context.Context, root string, files []qgFile) []qgIssue {
	var issues []qgIssue
	if gofmt, err := exec.LookPath("gofmt"); err == nil {
		for _, f := range files {
			if qgGofmtDiffers(ctx, gofmt, f.New) && (!f.Existed || !qgGofmtDiffers(ctx, gofmt, f.Old)) {
				issues = append(issues, qgIssue{File: f.Rel, Msg: "not gofmt-formatted: run gofmt -w " + f.Rel})
			}
		}
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		return issues
	}
	mods := map[string][]qgFile{} // module root -> files
	for _, f := range files {
		if m := qgFindUp(filepath.Dir(f.Abs), root, "go.mod"); m != "" {
			mods[m] = append(mods[m], f)
		}
	}
	for mod, fs := range mods {
		pkgs := map[string]bool{}
		for _, f := range fs {
			if r, err := filepath.Rel(mod, filepath.Dir(f.Abs)); err == nil {
				pkgs["./"+filepath.ToSlash(r)] = true
			}
		}
		args := []string{"vet"}
		for p := range pkgs {
			args = append(args, p)
		}
		sort.Strings(args[1:])
		out, _ := qgRun(ctx, mod, nil, gobin, args...)
		issues = append(issues, qgFilterLint(root, mod, out, fs, "go vet")...)
	}
	return issues
}

func qgLintPy(ctx context.Context, root string, files []qgFile) []qgIssue {
	if ruff, err := exec.LookPath("ruff"); err == nil {
		out, _ := qgRun(ctx, root, nil, ruff, append([]string{"check", "--output-format=concise", "--no-cache"}, qgAbsList(files)...)...)
		return qgFilterLint(root, root, out, files, "ruff")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil
	}
	out, _ := qgRun(ctx, root, nil, py, append([]string{"-m", "pyflakes"}, qgAbsList(files)...)...)
	if strings.Contains(out, "No module named") {
		return nil
	}
	return qgFilterLint(root, root, out, files, "pyflakes")
}

// qgFilterLint keeps "file:line[:col]: msg" findings that land on added lines.
func qgFilterLint(root, dir, out string, files []qgFile, tool string) []qgIssue {
	byAbs := map[string]qgFile{}
	for _, f := range files {
		byAbs[f.Abs] = f
	}
	var issues []qgIssue
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		m := qgLintLineRE.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		p := m[1]
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		f, ok := byAbs[filepath.Clean(p)]
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if !f.Added[n] || seen[l] {
			continue
		}
		seen[l] = true
		issues = append(issues, qgIssue{File: f.Rel, Line: n, Msg: tool + ": " + oneLine(m[4], 160)})
	}
	return issues
}

func qgGofmtDiffers(ctx context.Context, gofmt, src string) bool {
	if strings.TrimSpace(src) == "" {
		return false
	}
	out, err := qgRun(ctx, "", []byte(src), gofmt)
	return err == nil && out != src
}

// qgRun runs a checker with the scrubbed env and the lint timeout; it returns
// stdout+stderr (checkers report findings with a non-zero exit).
func qgRun(ctx context.Context, dir string, stdin []byte, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, qgLintTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = scrubEnv(os.Environ())
	sbWrapCmd(cmd) // eslint configs are project code
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.Output() // gofmt: stdout only
		return string(out), err
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return string(out), err
}

func qgAbsList(fs []qgFile) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Abs
	}
	return out
}

func qgExists(p string) bool { _, err := os.Stat(p); return err == nil }

// qgFindUp returns the nearest directory from dir up to root holding name.
func qgFindUp(dir, root, name string) string {
	for {
		if qgExists(filepath.Join(dir, name)) {
			return dir
		}
		if dir == root || dir == filepath.Dir(dir) || !strings.HasPrefix(dir, root) {
			return ""
		}
		dir = filepath.Dir(dir)
	}
}
