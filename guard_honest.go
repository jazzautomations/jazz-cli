package main

// guard_honest.go: deterministic "honest completion" guards, found running the
// hard eval (evals/v2) with free models. A model must not be able to finish a
// task by gaming its checks:
//
//  1. test protection: test files, test configuration and CI files that
//     existed when the turn started are read-only unless the request asks for
//     test changes; new test files are fine unless the request forbids
//     touching tests. Enforced before write/edit/script and before bash
//     commands that write those paths (shellparse), and after every mutating
//     tool by comparing a turn-start snapshot (changes are reverted).
//  2. anti-weakening: even where test edits are allowed, a change may not add
//     skip/xfail/only markers, hollow out a test, drop assertions, or add a
//     spree of lint/type suppressions.
//  3. hardcode detection: a source edit that answers a test with a literal
//     (a function of inputs that just returns the expected value, or an
//     `if input == <test input>: return <expected>` special case).
//  4. removal verified: symbols the request says to remove/rename must not be
//     defined any more; after "extract X into m.py without copies", no other
//     module may still define a name m.py provides.
//  5. honest done: a turn that edited code may not end before the tests (or,
//     with no test runner, the changed code) ran after the last edit.
//  6. checker tamper: evaluation checkers and prompts (CHECK, PROMPT, SETUP,
//     .hidden/ next to them) are never read or changed.
//
// All are on by default. `guards: off` in config.json, --guards off or
// JAZZ_GUARDS=off turn them all off; JAZZ_TEST_GUARD, JAZZ_HARDCODE_CHECK,
// JAZZ_DONE_GATE and JAZZ_EVAL_GUARD (=off), or config "guards_off":
// ["tests","hardcode","done","eval"], turn off one. Messages to the model are
// in English (tool results), messages to the user in Portuguese.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hgPolicy is what the request allows on tests.
type hgPolicy int

const (
	hgDefault hgPolicy = iota // existing test files protected, new test files allowed
	hgAllow                   // the request asks for test changes
	hgNewOnly                 // asks for a new test file, forbids editing the existing ones
	hgForbid                  // forbids touching the tests at all
)

func (p hgPolicy) String() string {
	return [...]string{"default", "allow", "new-only", "forbid"}[p]
}

// Snapshot limits (the Dell walks slowly; a project root may be $HOME).
const (
	hgMaxVisit = 6000
	hgMaxSnap  = 600
	hgMaxDepth = 8
)

// hgSnap is one protected file as it was when the turn started.
type hgSnap struct {
	data string
	mode os.FileMode
	size int64     // last seen size and mtime (cheap change detection)
	mod  time.Time //
}

// hgState is the per-turn guard state (Agent.hg).
type hgState struct {
	policy   hgPolicy
	ciOK     bool
	root     string
	rootReal string             // root with symlinks resolved (the shell reports /private/var for /var)
	snap     map[string]*hgSnap // abs -> turn-start content of protected files
	testDirs map[string]bool    // dirs holding test files (forbid: new files there are removed)
	pre      map[string]*string // abs -> content before this turn first changed it (nil = did not exist)

	seq, lastEdit, lastTest int
	codeEdited              bool

	removeIDs []string // symbols the request says to remove/rename
	extract   string   // "extract into X without copies": X (relative path)

	hard        []hgHard // hardcode findings
	nudgedTests bool
	nudgedDone  bool
	edits       map[string]int // single edits per file (batch tip)
	tipped      map[string]bool
}

type hgHard struct {
	abs, line, msg string
}

// ---------- settings ----------

var hgEnvNames = map[string]string{"tests": "JAZZ_TEST_GUARD", "hardcode": "JAZZ_HARDCODE_CHECK", "done": "JAZZ_DONE_GATE", "eval": "JAZZ_EVAL_GUARD"}

// hgOn: whether guard name (tests | hardcode | done | eval) is on.
func (a *Agent) hgOn(name string) bool {
	for _, v := range []string{a.loop.opts.Guards, os.Getenv("JAZZ_GUARDS"), a.cfg.Guards} {
		if strings.TrimSpace(v) != "" {
			if olOffWord(v) {
				return false
			}
			break
		}
	}
	if olOffWord(os.Getenv(hgEnvNames[name])) {
		return false
	}
	for _, n := range a.cfg.GuardsOff {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return false
		}
	}
	return true
}

// ---------- reading the request ----------

var (
	// hgAskTestsRE: the request asks to add or change tests.
	hgAskTestsRE = lazyRx(`(?i)\b(adicion\w*|acrescent\w*|escrev\w*|cri[ea]\w*|atualiz\w*|ajust\w*|ampli\w*|estend\w*|cubr\w*|cobr\w*|inclu\w*|mud\w*|alter\w*|edit\w*|modifiq\w*|modific\w*|reescrev\w*|add\w*|writ\w*|creat\w*|updat\w*|extend\w*|cover\w*|chang\w*|modify\w*|rewrit\w*|includ\w*)\b[^.;:\n]{0,40}?\b(tests?|testes?|specs?)\b|\b(com|with)\s+(\S+\s+){0,2}?(tests?|testes?)\b|\btdd\b|test[- ]first`)
	hgAskCIRE    = lazyRx(`(?i)\b(ci|workflows?|pipelines?|github actions|gitlab-ci|circleci|jenkinsfile)\b`)
	// hgTestsRightRE: "os testes estão certos" also forbids editing them.
	hgTestsRightRE = lazyRx(`(?i)\b(os\s+)?testes\s+est[ãa]o\s+cert|\btests\s+are\s+(correct|right)`)
)

// hgTestPolicy reads the request's stance on tests.
func hgTestPolicy(s string) (hgPolicy, bool) {
	neg := bgNoTestEditRE.MatchString(s) || hgTestsRightRE.MatchString(s)
	rest := hgTestsRightRE.ReplaceAllString(bgNoTestEditRE.ReplaceAllString(s, " "), " ")
	ask := hgAskTestsRE.MatchString(rest)
	ci := hgAskCIRE.MatchString(s)
	switch {
	case neg && ask:
		return hgNewOnly, ci
	case neg:
		return hgForbid, ci
	case ask:
		return hgAllow, ci
	}
	return hgDefault, ci
}

var (
	hgRemoveVerbRE = lazyRx(`(?i)\b(remov\w*|apag\w*|delet\w*|exclu[ai]\w*|elimin\w*|renome\w*|rename\w*|dedup\w*|deduplic\w*|substitu\w*|replac\w*|drop)\b`)
	hgNegBeforeRE  = lazyRx(`(?i)\b(n[ãa]o|nem|sem|don'?t|do not|never|without)\s+(\S+\s+){0,1}$`)
	hgToRE         = lazyRx(`(?i)\s(para|to|por|with|pelo|pela|into|em)\s|→|->`)
	hgBacktickRE   = lazyRx("`([A-Za-z_][\\w.]*)(?:\\(\\))?`")
	hgCodeIdentRE  = lazyRx(`\b([A-Za-z_][A-Za-z0-9]*(?:_[A-Za-z0-9_]+|[a-z][A-Z][A-Za-z0-9_]*))(?:\(\))?`)
	hgFuncCallRE   = lazyRx(`\b([A-Za-z_]\w*)\(\)`)
)

// hgRemovalIDs: identifiers the request says to remove, rename or replace
// (only the old names: what comes after "para/to/por" is the new one).
func hgRemovalIDs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range hgRemoveVerbRE.FindAllStringIndex(s, -1) {
		if hgNegBeforeRE.MatchString(s[max(0, m[0]-20):m[0]]) {
			continue
		}
		span := s[m[1]:]
		if i := strings.IndexAny(span, ".;\n"); i >= 0 {
			// a dot inside an identifier or a file name is not the sentence end
			for i >= 0 && i+1 < len(span) && span[i] == '.' && span[i+1] != ' ' {
				j := strings.IndexAny(span[i+1:], ".;\n")
				if j < 0 {
					i = -1
					break
				}
				i += 1 + j
			}
			if i >= 0 {
				span = span[:i]
			}
		}
		if len(span) > 160 {
			span = span[:160]
		}
		if loc := hgToRE.FindStringIndex(span); loc != nil {
			span = span[:loc[0]]
		}
		var ids []string
		for _, x := range hgBacktickRE.FindAllStringSubmatch(span, -1) {
			ids = append(ids, x[1])
		}
		for _, x := range hgCodeIdentRE.FindAllStringSubmatch(span, -1) {
			ids = append(ids, x[1])
		}
		for _, x := range hgFuncCallRE.FindAllStringSubmatch(span, -1) {
			ids = append(ids, x[1])
		}
		for _, id := range ids {
			if i := strings.LastIndex(id, "."); i >= 0 {
				if bgFileRE.MatchString(id) {
					continue // a file name, not a symbol
				}
				id = id[i+1:]
			}
			if len(id) < 3 || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

var (
	hgExtractRE  = lazyRx(`(?i)\b(extra[ií]\w*|extract\w*|mov[ae]\w*|mover|consolid\w*|centraliz\w*|unifi\w*|junt\w*)\b[^;\n]{0,200}?\s(?:para|into|to|em|no|na|in)\s+(?:\S+\s+){0,4}?([\w./-]+\.(?:py|go|js|mjs|ts|jsx|tsx|rb|rs|java|kt|php|lua))\b`)
	hgNoCopiesRE = lazyRx(`(?i)\bsem\s+(deixar\s+)?(nenhuma\s+)?(as\s+)?(c[óo]pias?|duplica\w*)|n[ãa]o\s+deix\w*\s+(nenhuma\s+)?c[óo]pias?|\bno\s+(more\s+)?(copies|duplicates?)\b|\bwithout\s+(leaving\s+)?(any\s+)?(copies|duplicates?|duplication)\b|\bremov\w*\s+(as\s+|the\s+)?(c[óo]pias|duplica\w*|copies)\b|\bdedup\w*`)
)

// hgExtractTarget: "extract … into m.py … without copies" names m.py.
func hgExtractTarget(s string) string {
	if !hgNoCopiesRE.MatchString(s) {
		return ""
	}
	if m := hgExtractRE.FindStringSubmatch(s); m != nil {
		return m[2]
	}
	return ""
}

// ---------- protected paths ----------

var (
	hgTestCfgRE = lazyRx(`(?i)(^|/)(pytest\.ini|tox\.ini|noxfile\.py|conftest\.py|\.coveragerc|phpunit\.xml(\.dist)?|karma\.conf\.[cm]?[jt]s|\.mocharc(\.\w+)?|(jest|vitest|playwright|cypress)\.config\.[cm]?[jt]s|run_?tests?\.\w+)$`)
	hgCIRE      = lazyRx(`(?i)(^|/)(\.github/workflows/[^/]+|\.gitlab-ci\.ya?ml|\.circleci/[^/]+|azure-pipelines\.ya?ml|Jenkinsfile|\.travis\.ya?ml|bitbucket-pipelines\.ya?ml|\.woodpecker\.ya?ml|\.drone\.ya?ml)$`)
	hgSectionRE = lazyRx(`(^|/)(GNUmakefile|[Mm]akefile|pyproject\.toml|setup\.cfg|package\.json)$`)
)

// hgKind classifies a path: "test", "testcfg", "ci", "section" (only the test
// part of the file is protected) or "" (not protected).
func hgKind(rel string) string {
	r := filepath.ToSlash(rel)
	for _, part := range strings.Split(r, "/") {
		switch part {
		case "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "node_modules":
			return "" // caches are not tests
		}
	}
	if strings.HasSuffix(r, ".pyc") {
		return ""
	}
	switch {
	case hgCIRE.MatchString(r):
		return "ci"
	case hgTestCfgRE.MatchString(r):
		return "testcfg"
	case qgIsTestFile(r):
		return "test"
	case hgSectionRE.MatchString(r):
		return "section"
	}
	return ""
}

var (
	hgMakeTargetRE = lazyRx(`^(test|tests|check|unittest|unit|coverage|ci)[\w.-]*\s*:`)
	hgTomlHeadRE   = lazyRx(`^\s*\[`)
	hgTomlTestRE   = lazyRx(`^\s*\[+\s*(tool\.(pytest|coverage)|tool:pytest|pytest|coverage:)`)
)

// hgSection extracts the test-related part of a "section" file.
func hgSection(rel, s string) string {
	base := filepath.Base(rel)
	lines := strings.Split(toLF(s), "\n")
	var b strings.Builder
	switch {
	case strings.HasSuffix(strings.ToLower(base), "makefile"):
		in := false
		for _, l := range lines {
			switch {
			case hgMakeTargetRE.MatchString(l):
				in = true
			case in && (strings.HasPrefix(l, "\t") || strings.TrimSpace(l) == ""):
			default:
				in = false
			}
			if in {
				b.WriteString(l + "\n")
			}
		}
	case base == "package.json":
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, `"test`) || strings.HasPrefix(t, `"jest"`) || strings.HasPrefix(t, `"vitest"`) || strings.HasPrefix(t, `"coverage`) {
				b.WriteString(t + "\n")
			}
		}
	default: // pyproject.toml, setup.cfg
		in := false
		for _, l := range lines {
			if hgTomlHeadRE.MatchString(l) {
				in = hgTomlTestRE.MatchString(l)
			}
			if in {
				b.WriteString(l + "\n")
			}
		}
	}
	return b.String()
}

// hgSkipDir: directories the snapshot never enters.
func hgSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "venv", "env", "__pycache__", "dist", "build", "target", ".tox", ".nox", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".next", ".cache", ".idea", ".vscode", ".jazz":
		return true
	}
	return false
}

// hgWalk visits the project's files (bounded), calling fn with abs and rel.
func hgWalk(root string, fn func(abs, rel string)) {
	visited := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > hgMaxVisit {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if p != root && (hgSkipDir(d.Name()) || strings.Count(filepath.ToSlash(rel), "/") >= hgMaxDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		fn(p, rel)
		return nil
	})
}

// snapshot records the protected files that exist when the turn starts.
func (st *hgState) snapshot() {
	hgWalk(st.root, func(abs, rel string) {
		if len(st.snap) >= hgMaxSnap || hgKind(rel) == "" {
			return
		}
		fi, err := os.Stat(abs)
		if err != nil || fi.Size() > bgMaxHash {
			return
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return
		}
		st.snap[abs] = &hgSnap{data: string(b), mode: fi.Mode().Perm(), size: fi.Size(), mod: fi.ModTime()}
		if hgKind(rel) == "test" {
			st.testDirs[filepath.Dir(abs)] = true
		}
	})
}

// ---------- judging a change ----------

// judge says why changing rel from old to new (del: deleted) is refused
// ("" = allowed); weak: the reason is a weakening, not a protected path.
// orig: the file existed when the turn started.
func (st *hgState) judge(rel string, orig bool, old, new string, del bool) (why string, weak bool) {
	kind := hgKind(rel)
	weakening := func(test bool) (string, bool) {
		if del {
			return "", false
		}
		w := hgWeakening(rel, old, new, test)
		return w, w != ""
	}
	switch kind {
	case "":
		return weakening(false)
	case "ci":
		if st.ciOK {
			return "", false
		}
		return fmt.Sprintf("%s is CI configuration and the user did not ask to change CI", rel), false
	case "section":
		if !orig || st.policy == hgAllow || hgSection(rel, old) == hgSection(rel, new) {
			return "", false
		}
		return fmt.Sprintf("this changes the test configuration in %s and the user did not ask to change tests", rel), false
	}
	// test or test configuration
	if orig {
		if st.policy != hgAllow {
			return st.protectedWhy(rel), false
		}
		return weakening(true)
	}
	if st.policy == hgForbid {
		return fmt.Sprintf("the user said not to change the tests, so no test file may be added either (%s)", rel), false
	}
	return weakening(kind == "test")
}

func (st *hgState) protectedWhy(rel string) string {
	switch st.policy {
	case hgForbid:
		return fmt.Sprintf("the user said not to change the tests, so %s is read-only in this task", rel)
	case hgNewOnly:
		return fmt.Sprintf("the user asked for a NEW test file and said not to edit the existing tests, so %s is read-only", rel)
	}
	return fmt.Sprintf("%s is an existing test file and the user did not ask to change tests", rel)
}

// hgRefusal is the text the model gets for a refused change.
func hgRefusal(why string, weakening bool) string {
	if weakening {
		return "Refused by the test guard: " + why + ". Tests must keep checking the behaviour: fix the code under test instead."
	}
	return "Refused by the test guard: " + why + ". Make the tests pass by fixing the code. If you want to add tests, put them in a NEW test file (when the task allows). If you are sure an existing test is wrong, stop and explain it in your final answer instead of editing it."
}

var (
	hgSkipRE      = lazyRx(`@pytest\.mark\.(skip|skipif|xfail)\b|@unittest\.(skip|skipIf|skipUnless|expectedFailure)\b|\bunittest\.skip\w*\(|\.skipTest\(|\bpytest\.(skip|xfail)\(|\bt\.Skip(Now|f)?\(|\b(it|test|describe|context|suite)\.(skip|only|todo)\(|\b(xit|xdescribe|xtest|fit|fdescribe)\(|@Disabled\b|@Ignore\b|#\[ignore\]`)
	hgAssertRE    = lazyRx(`\bassert\w*\b|\bexpect\(|\bt\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\b|\.should\b|\bself\.fail\(|\brequire\.\w+\(|\bassert\.\w+\(`)
	hgTrivialRE   = lazyRx(`^\s*(assert\s+(True|1|not\s+False)\s*$|self\.assertTrue\(\s*True\s*\)|expect\(\s*true\s*\)\.toBe\(\s*true\s*\)|assert\.True\(t,\s*true\))`)
	hgSuppressRE  = lazyRx(`#\s*noqa\b|#\s*type:\s*ignore\b|//\s*nolint\b|@ts-ignore\b|@ts-nocheck\b|eslint-disable|#\[allow\(|@SuppressWarnings\b|#\s*pragma:\s*no\s*cover|#\s*pylint:\s*disable`)
	hgPyTestDefRE = lazyRx(`^(\s*)(async\s+)?def\s+(test\w*)\s*\(`)
	hgGoTestDefRE = lazyRx(`^func\s+(Test\w*)\s*\(`)
)

// hgWeakening: why the change from old to new weakens tests or checks ("" = no).
func hgWeakening(rel, old, new string, test bool) string {
	old, new = toLF(old), toLF(new)
	added := qgAddedLines(old, new)
	lines := strings.Split(new, "\n")
	supp := 0
	for n := range added {
		l := lines[n-1]
		if hgSuppressRE.MatchString(l) {
			supp++
		}
		if !test {
			continue
		}
		if hgSkipRE.MatchString(l) {
			return fmt.Sprintf("it adds a skip/xfail/only marker in %s (`%s`)", rel, oneLine(strings.TrimSpace(l), 80))
		}
		if hgTrivialRE.MatchString(l) {
			return fmt.Sprintf("it adds an assertion that is always true in %s (`%s`)", rel, oneLine(strings.TrimSpace(l), 80))
		}
	}
	if supp >= 3 {
		return fmt.Sprintf("it adds %d lint/type suppressions (noqa, type: ignore, nolint…) in %s", supp, rel)
	}
	if !test {
		return ""
	}
	if o, n := len(qgMatchLines(old, hgAssertRE.R())), len(qgMatchLines(new, hgAssertRE.R())); n < o {
		return fmt.Sprintf("it removes %d assertion line(s) from %s", o-n, rel)
	}
	oldHollow := hgHollowTests(old)
	for name := range hgHollowTests(new) {
		if !oldHollow[name] {
			return fmt.Sprintf("it leaves test %s in %s without a real check (only pass/return/assert True)", name, rel)
		}
	}
	return ""
}

// hgHollowTests: test functions whose body checks nothing.
func hgHollowTests(s string) map[string]bool {
	out := map[string]bool{}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if m := hgPyTestDefRE.FindStringSubmatch(l); m != nil {
			ind := len(m[1])
			body := 0
			hollow := true
			inDoc := false
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" {
					continue
				}
				if len(lines[j])-len(strings.TrimLeft(lines[j], " \t")) <= ind {
					break
				}
				if strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, `'''`) {
					if !(len(t) >= 6 && (strings.HasSuffix(t, `"""`) || strings.HasSuffix(t, `'''`))) {
						inDoc = !inDoc
					}
					continue
				}
				if inDoc || strings.HasPrefix(t, "#") {
					continue
				}
				body++
				switch t {
				case "pass", "return", "return None", "...", "assert True", "self.assertTrue(True)":
				default:
					hollow = false
				}
			}
			if hollow && body > 0 {
				out[m[3]] = true
			}
		}
		if m := hgGoTestDefRE.FindStringSubmatch(l); m != nil {
			t := strings.TrimSpace(l)
			if strings.HasSuffix(t, "{}") {
				out[m[1]] = true
				continue
			}
			var body []string
			for j := i + 1; j < len(lines) && lines[j] != "}"; j++ {
				if x := strings.TrimSpace(lines[j]); x != "" && !strings.HasPrefix(x, "//") {
					body = append(body, x)
				}
			}
			if len(body) == 0 || len(body) == 1 && (strings.HasPrefix(body[0], "t.Skip") || body[0] == "return") {
				out[m[1]] = true
			}
		}
	}
	return out
}

// ---------- checker tamper ----------

var hgEvalNames = map[string]bool{"CHECK": true, "PROMPT": true, "SETUP": true, ".CHECK": true, ".PROMPT": true, ".SETUP": true}

// hgEvalPath: abs is an evaluation checker, prompt or hidden fixture.
func hgEvalPath(abs string) bool {
	base := filepath.Base(abs)
	dir := filepath.Dir(abs)
	hasChecker := func(d string) bool {
		for _, n := range []string{"CHECK", ".CHECK"} {
			if _, err := os.Lstat(filepath.Join(d, n)); err == nil {
				return true
			}
		}
		return false
	}
	switch base {
	case "CHECK", ".CHECK", ".PROMPT", ".SETUP":
		return true
	case "PROMPT", "SETUP":
		if hasChecker(dir) {
			return true
		}
		if _, err := os.Lstat(abs); err != nil && !os.IsNotExist(err) {
			return true // unreadable (locked by the eval runner)
		}
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i, p := range parts {
		if p == ".hidden" && i > 0 && hasChecker(filepath.FromSlash(strings.Join(parts[:i], "/"))) {
			return true
		}
	}
	return false
}

// ---------- shell targets ----------

// hgShellTargets: paths a bash command writes, deletes or moves (best effort;
// the post-tool snapshot comparison is the backstop).
func hgShellTargets(cmd string) (writes []string, all []string) {
	p := shellParse(cmd)
	for _, seg := range p.segs {
		toks := stripWrappers(shellFields(seg))
		var clean []string
		for i := 0; i < len(toks); i++ {
			t := toks[i]
			all = append(all, t)
			switch {
			case t == ">" || t == ">>" || t == "1>" || t == "1>>" || t == "&>" || t == ">|" || t == "&>>":
				if i+1 < len(toks) {
					writes = append(writes, toks[i+1])
					all = append(all, toks[i+1])
					i++
				}
				continue
			case t == "2>" || t == "2>>" || t == "<":
				i++
				continue
			case strings.HasPrefix(t, ">>") || strings.HasPrefix(t, "&>") || strings.HasPrefix(t, "1>") || strings.HasPrefix(t, ">"):
				w := strings.TrimLeft(t, "&1>|")
				if w != "" && !strings.HasPrefix(w, "&") {
					writes = append(writes, w)
				}
				continue
			case strings.HasPrefix(t, "2>") || strings.HasPrefix(t, "<"):
				continue
			}
			clean = append(clean, t)
		}
		if len(clean) == 0 {
			continue
		}
		w := filepath.Base(clean[0])
		args := clean[1:]
		nonFlag := func(from []string) []string {
			var out []string
			for _, a := range from {
				if !strings.HasPrefix(a, "-") {
					out = append(out, a)
				}
			}
			return out
		}
		switch w {
		case "rm", "unlink", "shred", "truncate", "tee", "mv", "rmdir":
			writes = append(writes, nonFlag(args)...)
		case "cp", "ln", "install", "rsync":
			if nf := nonFlag(args); len(nf) > 0 {
				writes = append(writes, nf[len(nf)-1])
			}
		case "sed", "gsed", "perl", "ruby":
			inPlace := false
			for _, a := range args {
				if strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i") && w != "sed") {
					inPlace = true
				}
			}
			if inPlace {
				nf := nonFlag(args)
				if len(nf) > 1 {
					writes = append(writes, nf[1:]...) // first one is the script
				}
			}
		case "dd":
			for _, a := range args {
				if strings.HasPrefix(a, "of=") {
					writes = append(writes, strings.TrimPrefix(a, "of="))
				}
			}
		case "git":
			if len(args) > 0 {
				switch args[0] {
				case "checkout", "restore", "rm", "mv":
					for _, a := range nonFlag(args[1:]) {
						if a != "HEAD" && !strings.HasPrefix(a, "HEAD~") && !strings.HasPrefix(a, "origin/") {
							writes = append(writes, a)
						}
					}
				}
			}
		}
	}
	return writes, all
}

// ---------- turn hooks ----------

// hgBeginTurn reads the request and snapshots the protected files.
func (a *Agent) hgBeginTurn(input string) {
	a.hg = nil
	if a.sub || a.tools == nil {
		return
	}
	s := strings.TrimSpace(acStripReminders(input))
	st := &hgState{root: a.tools.Root, snap: map[string]*hgSnap{}, testDirs: map[string]bool{}, pre: map[string]*string{}, edits: map[string]int{}, tipped: map[string]bool{}}
	st.policy, st.ciOK = hgTestPolicy(s)
	if r, err := filepath.EvalSymlinks(st.root); err == nil {
		st.rootReal = r
	}
	st.removeIDs = hgRemovalIDs(s)
	st.extract = hgExtractTarget(s)
	if a.hgOn("tests") || a.hgOn("hardcode") {
		st.snapshot()
	}
	a.hg = st
	dbg("guard", map[string]any{"policy": st.policy.String(), "snap": len(st.snap), "remove": st.removeIDs, "extract": st.extract})
}

// hgPreTool runs before permissions: hit=true refuses the call with out.
func (a *Agent) hgPreTool(tc ToolCall) (out ToolOutput, hit bool) {
	st := a.hg
	if st == nil || a.mcp.Has(tc.Name) {
		return out, false
	}
	m := acArgsMap(tc.Name, tc.Args)
	p, _ := argStr(m, "path")
	abs := ""
	if strings.TrimSpace(p) != "" {
		abs = a.hgNorm(a.tools.abs(p))
	}
	refuse := func(text, summary, warn string) (ToolOutput, bool) {
		a.ui.Warn(warn)
		dbg("guard", map[string]any{"refused": tc.Name, "why": oneLine(text, 200)})
		return ToolOutput{IsErr: true, Text: text, Display: Display{Summary: summary, Path: a.tools.rel(abs)}}, true
	}
	if a.hgOn("eval") {
		var paths []string
		if abs != "" {
			paths = append(paths, abs)
		}
		if tc.Name == "bash" {
			cmd, _ := argStr(m, "command")
			_, all := hgShellTargets(cmd)
			for _, t := range all {
				if hgEvalNames[filepath.Base(t)] || strings.Contains(t, ".hidden") {
					// a word like CHECK in a grep pattern is not a path: only
					// tokens naming something that is there (or is locked)
					x := a.hgAbsShell(t)
					if _, err := os.Lstat(x); err == nil || !os.IsNotExist(err) {
						paths = append(paths, x)
					}
				}
			}
		}
		for _, x := range paths {
			if hgEvalPath(x) {
				return refuse(fmt.Sprintf("Refused: %s is an evaluation checker/prompt file. Do not read or change it: solve the task from the request and the project's own code and tests.", a.tools.rel(x)),
					"bloqueado: arquivo de verificação", "guarda: acesso a arquivo de verificação bloqueado ("+a.tools.rel(x)+")")
			}
		}
	}
	if !a.hgOn("tests") && !a.hgOn("hardcode") {
		return out, false
	}
	switch tc.Name {
	case "write", "edit":
		if abs == "" {
			return out, false
		}
		cur, existed := hgReadFile(abs)
		if _, ok := st.pre[abs]; !ok {
			if existed {
				c := cur
				st.pre[abs] = &c
			} else {
				st.pre[abs] = nil
			}
		}
		if !a.hgOn("tests") {
			return out, false
		}
		var next string
		if tc.Name == "write" {
			next, _ = argStr(m, "content")
		} else {
			ops, bad := editOpsArgs(m, tc.Args)
			if bad != nil || !existed {
				return out, false // the tool reports the problem
			}
			res, err := applyEdits(toLF(strings.TrimPrefix(cur, "\uFEFF")), ops, p)
			if err != nil {
				return out, false
			}
			next = res.Content
		}
		_, orig := st.snap[abs]
		if why, weak := st.judge(a.tools.rel(abs), orig, cur, next, false); why != "" {
			return refuse(hgRefusal(why, weak), "bloqueado: guarda de testes", "guarda de testes: alteração recusada em "+a.tools.rel(abs))
		}
	case "script":
		if !a.hgOn("tests") {
			return out, false
		}
		if _, _, _, sp := a.bgScriptKind(m); sp != "" {
			sa := a.hgNorm(a.tools.abs(sp))
			if _, orig := st.snap[sa]; orig && st.policy != hgAllow && hgKind(a.tools.rel(sa)) != "section" {
				return refuse(hgRefusal(st.protectedWhy(a.tools.rel(sa)), false), "bloqueado: guarda de testes", "guarda de testes: alteração recusada em "+a.tools.rel(sa))
			}
		}
	case "bash":
		if !a.hgOn("tests") {
			return out, false
		}
		cmd, _ := argStr(m, "command")
		writes, _ := hgShellTargets(cmd)
		for _, w := range writes {
			for _, x := range a.hgExpand(w) {
				rel := a.tools.rel(x)
				if fi, err := os.Stat(x); err == nil && fi.IsDir() && st.policy != hgAllow {
					for k := range st.snap {
						if strings.HasPrefix(k, x+string(filepath.Separator)) && hgKind(a.tools.rel(k)) != "section" {
							return refuse(hgRefusal(st.protectedWhy(a.tools.rel(k)), false), "bloqueado: guarda de testes", "guarda de testes: comando recusado (mexe em "+rel+")")
						}
					}
				}
				kind := hgKind(rel)
				if kind == "" || kind == "section" {
					continue
				}
				_, orig := st.snap[x]
				var why string
				switch {
				case kind == "ci" && !st.ciOK:
					why = fmt.Sprintf("%s is CI configuration and the user did not ask to change CI", rel)
				case kind != "ci" && orig && st.policy != hgAllow:
					why = st.protectedWhy(rel)
				case kind != "ci" && !orig && st.policy == hgForbid && !hgExists(x):
					why = fmt.Sprintf("the user said not to change the tests, so no test file may be added either (%s)", rel)
				}
				if why != "" {
					return refuse(hgRefusal(why, false), "bloqueado: guarda de testes", "guarda de testes: comando recusado (mexe em "+rel+")")
				}
			}
		}
	}
	return out, false
}

func hgExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func hgReadFile(abs string) (string, bool) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// hgAbsShell resolves a shell path against the shell's cwd.
func (a *Agent) hgAbsShell(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		cwd := a.tools.Root
		if a.tools.Shell != nil && a.tools.Shell.Cwd != "" {
			cwd = a.tools.Shell.Cwd
		}
		p = filepath.Join(cwd, p)
	}
	return a.hgNorm(p)
}

// hgNorm maps a path under the symlink-resolved root back to the root.
func (a *Agent) hgNorm(p string) string {
	p = filepath.Clean(p)
	if st := a.hg; st != nil && st.rootReal != "" && st.rootReal != st.root {
		if p == st.rootReal || strings.HasPrefix(p, st.rootReal+string(filepath.Separator)) {
			p = st.root + p[len(st.rootReal):]
		}
	}
	return p
}

// hgExpand resolves a shell path, expanding globs.
func (a *Agent) hgExpand(p string) []string {
	x := a.hgAbsShell(p)
	if strings.ContainsAny(x, "*?[") {
		if ms, err := filepath.Glob(x); err == nil {
			return ms
		}
	}
	return []string{x}
}

// hgAfterTool updates the counters, reverts forbidden changes to protected
// files and checks source edits for hardcoded answers; the returned text is
// appended to the tool result.
func (a *Agent) hgAfterTool(tc ToolCall, out ToolOutput) string {
	st := a.hg
	if st == nil {
		return ""
	}
	m := acArgsMap(tc.Name, tc.Args)
	st.seq++
	var notes []string
	switch tc.Name {
	case "write", "edit":
		p, _ := argStr(m, "path")
		if !out.IsErr && p != "" && isCodeFile(p) {
			st.codeEdited = true
			st.lastEdit = st.seq
		}
		if tc.Name == "edit" && !out.IsErr && p != "" {
			if _, multi := m["edits"].([]any); !multi {
				abs := a.hgNorm(a.tools.abs(p))
				st.edits[abs]++
				if st.edits[abs] == 3 && !st.tipped[abs] {
					st.tipped[abs] = true
					notes = append(notes, "Tip: that is the 3rd separate edit to this file. Put the remaining changes to it in ONE edit call with edits[] (each round-trip is slow).")
				}
			}
		}
	case "bash":
		cmd, _ := argStr(m, "command")
		writes, _ := hgShellTargets(cmd)
		for _, w := range writes {
			if isCodeFile(w) {
				st.codeEdited = true
				st.lastEdit = st.seq
			}
		}
		if bgIsTestCmd(cmd) {
			st.lastTest = st.seq
			notes = append(notes, a.ojAfterTest(cmd, out)...) // root-cause panel after 2 failed runs (judge_fusion.go)
		}
	case "script":
		_, edit, test, sp := a.bgScriptKind(m)
		if edit && sp != "" && isCodeFile(sp) {
			st.codeEdited = true
			st.lastEdit = st.seq
		}
		if test {
			st.lastTest = st.seq
		}
	}
	switch tc.Name {
	case "read", "todo", "fetch":
	default:
		if a.hgOn("tests") {
			notes = append(notes, a.hgCheckSnap()...)
		}
	}
	if (tc.Name == "write" || tc.Name == "edit") && !out.IsErr && a.hgOn("hardcode") {
		p, _ := argStr(m, "path")
		notes = append(notes, a.hgHardcodeCheck(a.hgNorm(a.tools.abs(p)))...)
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n<system-reminder>" + strings.Join(notes, "\n") + "</system-reminder>"
}

// hgCheckSnap compares the protected files with the snapshot and reverts
// forbidden changes (and, when tests are forbidden, removes new test files).
func (a *Agent) hgCheckSnap() []string {
	st := a.hg
	var notes []string
	keys := make([]string, 0, len(st.snap))
	for k := range st.snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, abs := range keys {
		sn := st.snap[abs]
		fi, err := os.Stat(abs)
		if err == nil && fi.Size() == sn.size && fi.ModTime().Equal(sn.mod) {
			continue
		}
		cur, ok := hgReadFile(abs)
		if ok && cur == sn.data {
			sn.size, sn.mod = fi.Size(), fi.ModTime()
			continue
		}
		rel := a.tools.rel(abs)
		why, _ := st.judge(rel, true, sn.data, cur, !ok)
		if why == "" {
			if fi != nil {
				sn.size, sn.mod = fi.Size(), fi.ModTime()
			}
			continue
		}
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, []byte(sn.data), sn.mode); err != nil {
			notes = append(notes, fmt.Sprintf("The test guard could not restore %s (%v): %s. Restore it yourself.", rel, errText(err), why))
			continue
		}
		if fi2, err := os.Stat(abs); err == nil {
			sn.size, sn.mod = fi2.Size(), fi2.ModTime()
		}
		a.tools.stampData(abs, []byte(sn.data))
		a.ui.Warn("guarda de testes: alteração em " + rel + " desfeita")
		dbg("guard", map[string]any{"reverted": rel, "why": why})
		notes = append(notes, fmt.Sprintf("The harness REVERTED your change to %s: %s. Fix the code under test instead; if you are sure a test is wrong, say so in your final answer.", rel, why))
	}
	if st.policy == hgForbid {
		dirs := make([]string, 0, len(st.testDirs))
		for d := range st.testDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			ents, _ := os.ReadDir(d)
			for _, e := range ents {
				abs := filepath.Join(d, e.Name())
				if e.IsDir() || st.snap[abs] != nil {
					continue
				}
				rel := a.tools.rel(abs)
				if hgKind(rel) != "test" {
					continue
				}
				if os.Remove(abs) == nil {
					a.ui.Warn("guarda de testes: arquivo novo de teste removido (" + rel + ")")
					notes = append(notes, fmt.Sprintf("The harness REMOVED %s: the user said not to change the tests, so no test file may be added either.", rel))
				}
			}
		}
	}
	return notes
}

// ---------- hardcode detection ----------

var (
	hgLitRE       = lazyRx(`"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|-?\b\d+(?:\.\d+)?\b`)
	hgCallNameRE  = lazyRx(`\b([A-Za-z_]\w*)\s*\(`)
	hgAssertLnRE  = lazyRx(`assert|expect\(|==|Equal|toBe|should|t\.Error|t\.Fatal`)
	hgReturnLitRE = lazyRx(`\breturn\s+(` + `"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|-?\d+(?:\.\d+)?` + `)\s*(?:,\s*nil\s*)?;?\s*$`)
	hgIfEqRE      = lazyRx(`\bif\b.*?(?:==|===)\s*(` + `"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|-?\d+(?:\.\d+)?` + `)`)
	hgDefRE       = lazyRx(`^(\s*)(?:async\s+)?def\s+(\w+)\s*\(([^)]*)\)|^func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(([^)]*)\)|^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\(([^)]*)\)`)
	hgNotFnName   = map[string]bool{"assert": true, "assertEqual": true, "assertEquals": true, "assertTrue": true, "assertFalse": true, "assertIn": true, "assertAlmostEqual": true, "assertRaises": true, "assertIs": true, "assertIsNone": true, "assertRaisesRegex": true, "assertCountEqual": true, "assertListEqual": true, "assertDictEqual": true, "expect": true, "toBe": true, "toEqual": true, "toStrictEqual": true, "equal": true, "deepEqual": true, "strictEqual": true, "Equal": true, "Errorf": true, "Fatalf": true, "Error": true, "Fatal": true, "len": true, "str": true, "int": true, "float": true, "round": true, "list": true, "dict": true, "set": true, "tuple": true, "sorted": true, "print": true, "repr": true, "isinstance": true, "type": true, "bool": true, "Decimal": true, "date": true, "time": true, "datetime": true, "if": true, "for": true, "while": true, "return": true}
)

// hgLitKey normalises a literal ("" = trivial: 0, 1, -1, 2 or a 0/1-char string).
func hgLitKey(lit string) string {
	if lit == "" {
		return ""
	}
	if lit[0] == '"' || lit[0] == '\'' {
		s := lit[1 : len(lit)-1]
		if len([]rune(s)) < 2 {
			return ""
		}
		return "s:" + s
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || f == 0 || f == 1 || f == -1 || f == 2 {
		return ""
	}
	return "n:" + strconv.FormatFloat(f, 'g', -1, 64)
}

// hgExpectPairs: (function called in an assertion line, literal on that line).
func (a *Agent) hgExpectPairs() map[string]string {
	st := a.hg
	out := map[string]string{}
	var files []string
	for abs := range st.snap {
		files = append(files, abs)
	}
	for abs := range st.pre {
		files = append(files, abs)
	}
	sort.Strings(files)
	for _, abs := range files {
		rel := a.tools.rel(abs)
		if hgKind(rel) != "test" {
			continue
		}
		s, ok := hgReadFile(abs)
		if !ok {
			continue
		}
		for i, l := range strings.Split(toLF(s), "\n") {
			if !hgAssertLnRE.MatchString(l) {
				continue
			}
			var fns []string
			for _, c := range hgCallNameRE.FindAllStringSubmatch(l, -1) {
				if !hgNotFnName[c[1]] && !strings.HasPrefix(c[1], "assert") && !strings.HasPrefix(c[1], "test") {
					fns = append(fns, c[1])
				}
			}
			for _, lit := range hgLitRE.FindAllString(l, -1) {
				k := hgLitKey(lit)
				if k == "" {
					continue
				}
				for _, f := range fns {
					if _, seen := out[f+"\x00"+k]; !seen {
						out[f+"\x00"+k] = fmt.Sprintf("%s:%d", rel, i+1)
					}
				}
			}
		}
	}
	return out
}

// hgHardcodeCheck looks at the lines this turn added to source file abs for a
// function of inputs that answers a test with its expected literal.
func (a *Agent) hgHardcodeCheck(abs string) []string {
	st := a.hg
	rel := a.tools.rel(abs)
	if hgKind(rel) != "" || !isCodeFile(abs) {
		return nil
	}
	cur, ok := hgReadFile(abs)
	if !ok {
		return nil
	}
	old := ""
	if p := st.pre[abs]; p != nil {
		old = *p
	}
	cur, old = toLF(cur), toLF(old)
	added := qgAddedLines(old, cur)
	if len(added) == 0 {
		return nil
	}
	var pairs map[string]string
	lines := strings.Split(cur, "\n")
	var notes []string
	// keep earlier findings for other files; recompute this file's
	kept := st.hard[:0]
	for _, h := range st.hard {
		if h.abs != abs {
			kept = append(kept, h)
		}
	}
	st.hard = kept
	nums := make([]int, 0, len(added))
	for n := range added {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		l := lines[n-1]
		m := hgReturnLitRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		fn, params, body := hgEnclosingFunc(lines, n-1)
		if fn == "" || strings.TrimSpace(params) == "" || hgOnlySelf(params) {
			continue
		}
		outKey := hgLitKey(m[1])
		if outKey == "" {
			continue
		}
		if pairs == nil {
			pairs = a.hgExpectPairs()
		}
		where, hit := pairs[fn+"\x00"+outKey]
		if !hit {
			continue
		}
		special := ""
		// if <input> == <test input>: return <expected>  (same line or the line before)
		cond := l
		if !hgIfEqRE.MatchString(cond) && n >= 2 {
			cond = lines[n-2]
		}
		if c := hgIfEqRE.FindStringSubmatch(cond); c != nil {
			if k := hgLitKey(c[1]); k != "" {
				if _, in := pairs[fn+"\x00"+k]; in {
					special = c[1]
				}
			}
		}
		if special == "" && body != 1 {
			continue // a real function that happens to have this literal as one of its answers
		}
		var msg string
		if special != "" {
			msg = fmt.Sprintf("%s:%d: `%s` special-cases the test input %s of %s() to return the value the test expects (%s): that is a hardcoded answer, not an implementation", rel, n, oneLine(strings.TrimSpace(l), 80), special, fn, where)
		} else {
			msg = fmt.Sprintf("%s:%d: %s() now just returns %s, exactly the value the test expects (%s): that is a hardcoded answer, not an implementation", rel, n, fn, m[1], where)
		}
		st.hard = append(st.hard, hgHard{abs: abs, line: strings.TrimSpace(l), msg: msg})
		notes = append(notes, "Warning (hardcode check): "+msg+". Implement the real logic.")
		a.ui.Warn("guarda: possível resposta fixa (hardcode) em " + rel + ":" + strconv.Itoa(n))
	}
	return notes
}

func hgOnlySelf(params string) bool {
	p := strings.TrimSpace(params)
	return p == "self" || p == "cls"
}

// hgEnclosingFunc: name, parameter list and number of statements of the
// function holding line i (Python by indentation; others: nearest decl).
func hgEnclosingFunc(lines []string, i int) (name, params string, body int) {
	ind := len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
	for j := i; j >= 0; j-- {
		m := hgDefRE.FindStringSubmatch(lines[j])
		if m == nil {
			continue
		}
		switch {
		case m[2] != "":
			if len(m[1]) >= ind && j != i {
				continue
			}
			name, params = m[2], m[3]
			// count statements of the body
			dInd := len(m[1])
			for k := j + 1; k < len(lines); k++ {
				t := strings.TrimSpace(lines[k])
				if t == "" {
					continue
				}
				if len(lines[k])-len(strings.TrimLeft(lines[k], " \t")) <= dInd {
					break
				}
				if !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, `"""`) && !strings.HasPrefix(t, `'''`) {
					body++
				}
			}
			return
		case m[4] != "":
			name, params = m[4], m[5]
		default:
			name, params = m[6], m[7]
		}
		for k := j + 1; k < len(lines) && lines[k] != "}"; k++ {
			if t := strings.TrimSpace(lines[k]); t != "" && t != "}" && !strings.HasPrefix(t, "//") {
				body++
			}
		}
		return
	}
	return "", "", 0
}

// ---------- the done gate ----------

var hgDefFmt = `(?m)^\s*(?:export\s+)?(?:pub(?:\([\w:]+\))?\s+)?(?:async\s+)?(?:def|class|func|function|type|const|let|var|fn|struct|interface|enum)\s+(?:\([^)]*\)\s*)?%s\b|^%s\s*(?::[^=\n]*)?=[^=]`

// hgDefinitions: where id is still defined in the project's non-test code.
func (a *Agent) hgDefinitions(id string) []string {
	re, err := regexp.Compile(fmt.Sprintf(hgDefFmt, regexp.QuoteMeta(id), regexp.QuoteMeta(id)))
	if err != nil {
		return nil
	}
	var out []string
	hgWalk(a.tools.Root, func(abs, rel string) {
		if len(out) >= 5 || !isCodeFile(abs) || hgKind(rel) != "" {
			return
		}
		s, ok := hgReadFile(abs)
		if !ok || len(s) > bgMaxHash || !strings.Contains(s, id) {
			return
		}
		if loc := re.FindStringIndex(s); loc != nil {
			out = append(out, fmt.Sprintf("%s:%d", rel, strings.Count(s[:loc[0]], "\n")+1))
		}
	})
	return out
}

var hgTopDefRE = lazyRx(`(?m)^(?:async\s+)?(?:def|class)\s+(\w+)|^func\s+(\w+)|^type\s+(\w+)|^(?:export\s+)?(?:async\s+)?(?:function|class|const|let)\s+(\w+)`)

func hgTopDefs(s string) map[string]int {
	out := map[string]int{}
	for _, m := range hgTopDefRE.FindAllStringSubmatchIndex(s, -1) {
		for g := 1; g <= 4; g++ {
			if m[2*g] >= 0 {
				out[s[m[2*g]:m[2*g+1]]] = strings.Count(s[:m[0]], "\n") + 1
				break
			}
		}
	}
	return out
}

// hgCopiesLeft: after "extract into target without copies", other modules
// that still define a name the target provides.
func (a *Agent) hgCopiesLeft() []string {
	st := a.hg
	target := a.tools.abs(st.extract)
	s, ok := hgReadFile(target)
	if !ok {
		base := filepath.Base(st.extract)
		hgWalk(a.tools.Root, func(abs, rel string) {
			if !ok && filepath.Base(abs) == base && strings.HasSuffix(filepath.ToSlash(abs), filepath.ToSlash(st.extract)) {
				target, ok = abs, true
				s, _ = hgReadFile(abs)
			}
		})
		if !ok {
			return nil
		}
	}
	provided := hgTopDefs(s)
	if len(provided) == 0 {
		return nil
	}
	var out []string
	hgWalk(filepath.Dir(target), func(abs, rel string) {
		if abs == target || !isCodeFile(abs) || filepath.Ext(abs) != filepath.Ext(target) || hgKind(a.tools.rel(abs)) != "" {
			return
		}
		src, ok := hgReadFile(abs)
		if !ok {
			return
		}
		defs := hgTopDefs(src)
		var names []string
		for n := range defs {
			if _, dup := provided[n]; dup {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, fmt.Sprintf("%s:%d still defines %s, which %s now provides", a.tools.rel(abs), defs[n], n, a.tools.rel(target)))
		}
	})
	return out
}

// hgTestCommand: the project's test command, also for plain unittest/go
// layouts the quality gate's detector leaves out ("" = none found).
func hgTestCommand(root string) string {
	if c := qgTestCommand(root); c != "" {
		return c
	}
	has := func(n string) bool { _, err := os.Stat(filepath.Join(root, n)); return err == nil }
	for _, n := range []string{"run_tests.py", "runtests.py"} {
		if has(n) {
			return "python3 " + n
		}
	}
	py := false
	hgWalk(root, func(abs, rel string) {
		if !py && strings.HasSuffix(abs, ".py") && hgKind(rel) == "test" && strings.HasPrefix(filepath.Base(abs), "test") {
			py = true
		}
	})
	if py {
		return "python3 -m unittest"
	}
	return ""
}

// hgDoneGate runs when the model wants to stop (before the quality gate).
// again=true: a reminder was appended and the turn goes on; why: the stop
// reason when the turn ends with the guard still failing.
func (a *Agent) hgDoneGate(ctx context.Context) (again bool, why StopReason) {
	st := a.hg
	if st == nil || ctx.Err() != nil || a.mode() == "plan" {
		return false, ""
	}
	var issues []string
	if a.hgOn("done") && (st.codeEdited || st.lastEdit > 0) {
		for _, id := range st.removeIDs {
			if defs := a.hgDefinitions(id); len(defs) > 0 {
				issues = append(issues, fmt.Sprintf("the request says to remove/rename %s, but it is still defined at %s", id, strings.Join(defs, ", ")))
			}
		}
		if st.extract != "" {
			if left := a.hgCopiesLeft(); len(left) > 0 {
				issues = append(issues, fmt.Sprintf("the request says to extract into %s without leaving copies, but: %s. Import it from there; a wrapper with the same name is still a copy", st.extract, strings.Join(left, "; ")))
			}
		}
	}
	if a.hgOn("hardcode") {
		for _, h := range st.hard {
			if s, ok := hgReadFile(h.abs); ok && strings.Contains(s, h.line) {
				issues = append(issues, h.msg)
			}
		}
	}
	if len(issues) > 0 {
		if !st.nudgedDone {
			st.nudgedDone = true
			a.ui.Notice(fmt.Sprintf("guarda de conclusão: %d %s — devolvendo ao modelo", len(issues), uiPl(len(issues), "pendência", "pendências")))
			var b strings.Builder
			b.WriteString("<system-reminder>The task is not done yet (deterministic check):\n")
			for i, is := range issues {
				fmt.Fprintf(&b, "%d. %s\n", i+1, is)
			}
			b.WriteString("Fix these, re-run the tests, then give your final answer.</system-reminder>")
			a.append(Message{Role: "user", Content: b.String()})
			return true, ""
		}
		a.ui.Warn("guarda de conclusão ainda falha: " + oneLine(issues[0], 140))
		return false, StopQualityFailed
	}
	// Tests must run after the last code edit. The quality gate already asks
	// for it when it knows the test command; the agent's verify nudge covers
	// "ran nothing" (and "no way to verify: say so") in projects without one.
	if a.hgOn("done") && st.codeEdited && st.lastEdit > st.lastTest && !st.nudgedTests && a.loop.opts.Until == "" {
		cmd := hgTestCommand(a.tools.Root)
		if cmd != "" && (a.qualityLevel() == qgOff || qgTestCommand(a.tools.Root) == "") {
			st.nudgedTests = true
			a.ui.Notice("guarda de conclusão: testes não rodaram depois da última edição — pedindo ao modelo")
			a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>You changed code but did not run the tests after your last edit. Run `%s` (or the narrowest test that covers the change), read the result, then give your final answer.</system-reminder>", cmd)})
			return true, ""
		}
	}
	return false, ""
}
