//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// qgTestFile builds a changed file the way qgChanges does.
func qgTestFile(rel, old, new string) qgFile {
	f := qgFile{Rel: rel, Abs: "/r/" + rel, Old: old, New: new, Existed: old != "", Test: qgIsTestFile(rel), Code: isCodeFile(rel)}
	f.Added = qgAddedLines(old, new)
	return f
}

// qgNoLint replaces the linter for the duration of a test.
func qgNoLint(t *testing.T) {
	t.Helper()
	prev := qgLintRun
	qgLintRun = func(context.Context, string, []qgFile) []qgIssue { return nil }
	t.Cleanup(func() { qgLintRun = prev })
}

func qgHas(issues []qgIssue, sub string) bool {
	for _, i := range issues {
		if strings.Contains(i.String(), sub) {
			return true
		}
	}
	return false
}

func TestQgAddedLines(t *testing.T) {
	got := qgAddedLines("a\nb\nc\n", "a\nX\nb\nc\nb\n")
	if len(got) != 2 || !got[2] || !got[5] {
		t.Errorf("added = %v, want lines 2 and 5", got)
	}
}

func TestQgChecklistItems(t *testing.T) {
	qgNoLint(t)
	root := t.TempDir()
	env := qgEnv{root: root, kind: "change", testsAfter: true}
	old := "package x\n\nfunc A() int {\n\treturn 1\n}\n"
	new := old + `
func B() {
	fmt.Println("DEBUG value", 1)
	// TODO: handle errors
	// TODO shortcut: no retries, add them when the API is flaky
	s := "TODO in a string is not a comment"
	_ = s
	// x := compute(1)
	// if x > 2 {
	// 	return
	// }
}

// Prose about the function: if the input is empty
// for some reason the caller gets nothing back,
// and return values are documented elsewhere.
func C() {}
`
	issues := qgCheck(context.Background(), env, []qgFile{qgTestFile("x.go", old, new)})
	for _, want := range []string{"debug print", "new TODO without", "commented-out code block (4 lines)"} {
		if !qgHas(issues, want) {
			t.Errorf("missing %q in %v", want, issues)
		}
	}
	n := 0
	for _, i := range issues {
		if strings.Contains(i.Msg, "TODO") {
			n++
		}
		if strings.Contains(i.Msg, "commented-out") && i.Line > 20 {
			t.Errorf("prose comment flagged as code: %v", i)
		}
	}
	if n != 1 {
		t.Errorf("exactly one TODO should be flagged (tagged one and the string are fine): %v", issues)
	}
	// the same debug print in a test file is fine
	issues = qgCheck(context.Background(), env, []qgFile{qgTestFile("x_test.go", "", "package x\n\nfunc TestB(t *testing.T) {\n\tfmt.Println(\"DEBUG\")\n}\n")})
	if qgHas(issues, "debug print") {
		t.Errorf("test files may print: %v", issues)
	}
}

func TestQgLongFunctionAndStrict(t *testing.T) {
	qgNoLint(t)
	var b strings.Builder
	b.WriteString("package x\n\nfunc Long() {\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "\tv%d := %d\n\t_ = v%d\n", i, i, i)
	}
	b.WriteString("}\n")
	f := qgTestFile("long.go", "", b.String())
	env := qgEnv{root: t.TempDir(), kind: "change", testsAfter: true}
	if is := qgCheck(context.Background(), env, []qgFile{f}); !qgHas(is, "function Long is 102 lines (limit 60)") {
		t.Errorf("long function not flagged: %v", is)
	}
	short := "package x\n\nfunc S() {\n" + strings.Repeat("\ty := 1\n\t_ = y\n", 22) + "}\n"
	env.strict = false
	if is := qgCheck(context.Background(), env, []qgFile{qgTestFile("s.go", "", short)}); qgHas(is, "function S") {
		t.Errorf("46 lines is fine in standard: %v", is)
	}
	env.strict = true
	is := qgCheck(context.Background(), env, []qgFile{qgTestFile("s.go", "", short+"\nfunc T() {\n\ttmp := 2\n\tprint(tmp)\n}\n")})
	for _, want := range []string{"function S is 46 lines (limit 40)", "throwaway name", "debug print"} {
		if !qgHas(is, want) {
			t.Errorf("strict: missing %q in %v", want, is)
		}
	}
	// an untouched long function never counts
	if is := qgCheck(context.Background(), qgEnv{root: env.root, testsAfter: true}, []qgFile{qgTestFile("long.go", b.String(), b.String()+"\nvar Z = 1\n")}); qgHas(is, "function Long") {
		t.Errorf("unchanged long function flagged: %v", is)
	}
}

func TestQgTestsRanAndTestTouched(t *testing.T) {
	qgNoLint(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)
	logic := "package x\n\nfunc F(n int) int {\n\tif n > 1 {\n\t\treturn 2\n\t}\n\tfor i := 0; i < n; i++ {\n\t}\n\treturn n\n}\n"
	f := qgTestFile("f.go", "", logic)
	is := qgCheck(context.Background(), qgEnv{root: root, kind: "greenfield"}, []qgFile{f})
	if !qgHas(is, "tests were not run after your last edit: run `go test ./...`") {
		t.Errorf("tests-not-run item missing: %v", is)
	}
	if !qgHas(is, "no test file was touched") {
		t.Errorf("test-touched item missing: %v", is)
	}
	// --until proves the tests; a touched test file satisfies the second rule
	tf := qgTestFile("f_test.go", "", "package x\n\nfunc TestF(t *testing.T) {}\n")
	if is := qgCheck(context.Background(), qgEnv{root: root, kind: "greenfield", until: true}, []qgFile{f, tf}); len(is) != 0 {
		t.Errorf("want no issues, got %v", is)
	}
	// bugfix without a test seam nearby: no test required
	if is := qgCheck(context.Background(), qgEnv{root: t.TempDir(), kind: "bugfix", testsAfter: true}, []qgFile{f}); qgHas(is, "no test file") {
		t.Errorf("bugfix without seam must not require a test: %v", is)
	}
	// refactor: never requires a new test, but must keep the test count
	oldT := "package x\n\nfunc TestA(t *testing.T) {}\n\nfunc TestB(t *testing.T) {}\n"
	is = qgCheck(context.Background(), qgEnv{root: root, kind: "refactor", testsAfter: true}, []qgFile{f, qgTestFile("a_test.go", oldT, "package x\n\nfunc TestA(t *testing.T) {}\n")})
	if !qgHas(is, "removed 1 test function") || qgHas(is, "no test file") {
		t.Errorf("refactor rules: %v", is)
	}
}

func TestQgFuncsLanguages(t *testing.T) {
	py := "def a(x):\n    if x:\n        return 1\n    return 2\n\nclass K:\n    def m(self):\n        pass\n"
	fs := qgFuncs("m.py", strings.Split(py, "\n"))
	if len(fs) != 2 || fs[0].name != "a" || fs[0].end != 4 || fs[1].name != "m" {
		t.Errorf("python spans: %+v", fs)
	}
	js := "export function a(x) {\n  if (x) {\n    return 1;\n  }\n  return \"}\";\n}\nconst b = (y) => {\n  return y;\n};\n"
	fs = qgFuncs("m.js", strings.Split(js, "\n"))
	if len(fs) != 2 || fs[0].name != "a" || fs[0].end != 6 || fs[1].name != "b" || fs[1].end != 9 {
		t.Errorf("js spans: %+v", fs)
	}
	goSrc := "package x\n\nfunc (r *T) M() {\n\tif x {\n\t}\n}\n\nfunc One() { return }\n"
	fs = qgFuncs("m.go", strings.Split(goSrc, "\n"))
	if len(fs) != 1 || fs[0].name != "M" || fs[0].start != 3 || fs[0].end != 6 {
		t.Errorf("go spans: %+v", fs)
	}
}

func TestQgFilterLintOnlyAddedLines(t *testing.T) {
	f := qgTestFile("a.go", "package a\n\nvar x = 1\n", "package a\n\nvar x = 1\nvar y = 2\n")
	f.Abs = "/r/a.go"
	out := "# a\n./a.go:3:5: old problem\n./a.go:4:5: new problem\nvet: ./a.go:4:1: also new\n"
	is := qgFilterLint("/r", "/r", out, []qgFile{f}, "go vet")
	if len(is) != 2 || !qgHas(is, "go vet: new problem") || qgHas(is, "old problem") {
		t.Errorf("lint filter: %v", is)
	}
}

func TestQgTestCommand(t *testing.T) {
	root := t.TempDir()
	if c := qgTestCommand(root); c != "" {
		t.Errorf("empty project: %q", c)
	}
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`), 0o644)
	if c := qgTestCommand(root); c != "" {
		t.Errorf("npm placeholder test script is not a test command: %q", c)
	}
	os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\tcc x.c\ntest: build\n\t./t\n"), 0o644)
	if c := qgTestCommand(root); c != "make test" {
		t.Errorf("Makefile: %q", c)
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n"), 0o644)
	if c := qgTestCommand(root); c != "go test ./..." {
		t.Errorf("go: %q", c)
	}
}

// End to end: one feedback round, then quality_failed (exit code 9).
func TestQualityGateFeedbackThenFail(t *testing.T) {
	qgNoLint(t)
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w", Name: "write", Args: jsonArgs(map[string]string{"path": "a.py", "content": "def f():\n    print(\"DEBUG here\")\n    return 1\n"})}}},
		fakeResp{Text: "done"},
		fakeResp{Text: "still done"},
	), func(c *Config) { c.Mode = "yolo"; c.NoVerifyNudge = true })
	stop, err := h.ag.Run(context.Background(), "create a.py with f")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopQualityFailed || stop.ExitCode() != 9 {
		t.Fatalf("stop = %s (%d), want quality_failed (9)", stop, stop.ExitCode())
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 || !reqs[2].contains("user", "deterministic checklist") || !reqs[2].contains("user", "a.py:2: debug print") {
		t.Errorf("feedback round missing; requests=%d", len(reqs))
	}
	if !anyContains(h.ui.warns, "checagem de qualidade ainda falha") {
		t.Errorf("warning missing: %v", h.ui.warns)
	}
	if s := bgScoreOf("fake/m1"); s.QualFail != 1 {
		t.Errorf("quality failure not scored: %+v", s)
	}
}

func TestQualityGateFixedPasses(t *testing.T) {
	qgNoLint(t)
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w", Name: "write", Args: jsonArgs(map[string]string{"path": "a.py", "content": "def f():\n    print(\"DEBUG here\")\n    return 1\n"})}}},
		fakeResp{Text: "done"},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "write", Args: jsonArgs(map[string]string{"path": "a.py", "content": "def f():\n    return 1\n"})}}},
		fakeResp{Text: "fixed"},
	), func(c *Config) { c.Mode = "yolo"; c.NoVerifyNudge = true })
	stop, err := h.ag.Run(context.Background(), "create a.py with f")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if !anyContains(h.ui.notices, "checagem de qualidade passou") {
		t.Errorf("pass notice missing: %v", h.ui.notices)
	}
}

func TestQualityGateOffAndUntilOrder(t *testing.T) {
	qgNoLint(t)
	write := fakeResp{Calls: []fakeCall{{ID: "w", Name: "write", Args: jsonArgs(map[string]string{"path": "a.py", "content": "def f():\n    print(\"DEBUG\")\n"})}}}
	h := newHarness(t, seq(write, fakeResp{Text: "done"}), func(c *Config) { c.Mode = "yolo"; c.Quality = qgOff; c.NoVerifyNudge = true })
	if stop, _ := h.ag.Run(context.Background(), "create a.py"); stop != StopDone || len(h.fs.requests()) != 2 {
		t.Errorf("quality off: stop=%s requests=%d", stop, len(h.fs.requests()))
	}
	// quality runs before --until: the first stop gets the checklist, not the until output
	h = newHarness(t, seq(write, fakeResp{Text: "done"}, fakeResp{Text: "done again"}), func(c *Config) { c.Mode = "yolo"; c.NoVerifyNudge = true })
	h.ag.SetLoopOpts(LoopOpts{Until: "true"})
	stop, _ := h.ag.Run(context.Background(), "create a.py")
	reqs := h.fs.requests()
	if !reqs[2].contains("user", "deterministic checklist") {
		t.Error("quality feedback must come first")
	}
	if stop != StopQualityFailed {
		t.Errorf("until passed but the checklist still fails: stop=%s", stop)
	}
	if s := bgScoreOf("fake/m1"); s.UntilOK != 1 {
		t.Errorf("--until pass not scored: %+v", s)
	}
}
