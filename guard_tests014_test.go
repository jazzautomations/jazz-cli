//go:build !windows

package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 0.14.0 QA (Dell, group test-guards): the test guards and the quality gate
// fought correct work. One regression test per defect.

const tgCalcFixed = "def soma(a, b):\n    if a is None:\n        return b\n    if b is None:\n        return a\n    return a + b\n"

func tgDumpReqs(t *testing.T, h *harness) {
	t.Helper()
	for i, r := range h.fs.requests() {
		m := r.Messages[len(r.Messages)-1]
		t.Logf("req %d last %v: %.400v", i, m["role"], m["content"])
	}
}

// (1) A test file created through bash (heredoc) counts for the quality gate:
// no "no test file was touched" and no quality_failed on correct work.
func TestQualityGateSeesBashWrittenTest(t *testing.T) {
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"}
	heredoc := "cat > tests/test_none.py <<'EOF'\nimport unittest\nfrom calc import soma\n\n\nclass N(unittest.TestCase):\n    def test_none(self):\n        self.assertEqual(soma(None, 2), 2)\n        self.assertEqual(soma(2, None), 2)\nEOF"
	h := newGuardHarness(t, files,
		fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", "def soma(a, b):\n    return a + b\n", tgCalcFixed)}},
		fakeResp{Calls: []fakeCall{bashCall("b1", heredoc)}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "python3 -m unittest -q")}},
		fakeResp{Text: "pronto: soma aceita None, teste em tests/test_none.py"},
		fakeResp{Text: "pronto (de novo)"})
	stop, err := h.ag.Run(context.Background(), "Faça soma em calc.py aceitar None como zero")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range h.fs.requests() {
		if r.contains("user", "no test file was touched") {
			tgDumpReqs(t, h)
			t.Fatal("a test file written by bash must count as a touched test")
		}
	}
	if stop != StopDone {
		tgDumpReqs(t, h)
		t.Errorf("stop=%s, want done", stop)
	}
	if bt := h.ag.bgTurnState(); bt != nil {
		if p := bt.pre[filepath.Join(h.root, "tests", "test_none.py")]; p == nil || p.existed {
			t.Errorf("the bash-created test has no 'new file' pre-image: %+v", p)
		}
	}
}

// (2) A bugfix whose existing test went red→green across the edit needs no
// new test file; without the red run before, the gate still asks (reworded).
func TestQualityRedGreenExistingTestCovers(t *testing.T) {
	broken := "def soma(a, b):\n    return a - b\n"
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": broken}
	h := newGuardHarness(t, files,
		fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest -q")}},
		fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", broken, tgCalcFixed)}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "python3 -m unittest -q")}},
		fakeResp{Text: "corrigido: soma somava errado; tests/test_calc.py agora passa"},
		fakeResp{Text: "pronto (de novo)"})
	stop, err := h.ag.Run(context.Background(), "Corrija o bug em soma (calc.py), o teste está falhando")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range h.fs.requests() {
		if r.contains("user", "no test file was touched") {
			tgDumpReqs(t, h)
			t.Fatal("existing tests went red→green: no new test may be demanded")
		}
	}
	if stop != StopDone {
		t.Errorf("stop=%s, want done", stop)
	}
	if !h.ag.hg.redGreen {
		t.Error("red→green not recorded")
	}

	// control: same change, no red run before it → the item is still there,
	// and it allows naming the existing test that covers it
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := qgFile{Rel: "calc.py", Abs: filepath.Join(root, "calc.py"), Old: broken, New: tgCalcFixed, Existed: true, Code: true}
	f.Added = qgAddedLines(f.Old, f.New)
	issues := qgCheck(context.Background(), qgEnv{root: root, kind: "bugfix", testsAfter: true}, []qgFile{f})
	if !qgHas(issues, "or say which existing test already covers it") {
		t.Errorf("want the reworded item, got %v", issues)
	}
	if qgHas(qgCheck(context.Background(), qgEnv{root: root, kind: "bugfix", testsAfter: true, covered: true}, []qgFile{f}), "no test file was touched") {
		t.Error("covered: no test item")
	}
	if !qgNeedTest(qgEnv{root: root, kind: "greenfield", strict: true, covered: true}, nil, "go test ./...") {
		t.Error("strict non-bugfix still needs its test even when red→green")
	}
}

// (3) The one-shot reviewer sees the changed tests and README, and is told
// about the files it does not see.
func TestArOneShotMaterialShowsTestsAndDocs(t *testing.T) {
	files := []qgFile{
		{Rel: "calc.py", New: "def soma(a, b):\n    return a + b\n", Code: true},
		{Rel: "tests/test_calc.py", New: "def test_soma():\n    assert soma(1, 2) == 3\n", Code: true, Test: true},
		{Rel: "README.md", New: "# calc\nsoma(a, b)\n"},
		{Rel: "config.json", New: "{}\n"},
	}
	inline, note, n, ok := arOneShotMaterial(files)
	if !ok || n != 1 {
		t.Fatalf("ok=%v n=%d", ok, n)
	}
	for _, p := range []string{`<file path="calc.py">`, `<file path="tests/test_calc.py">`, `<file path="README.md">`} {
		if !strings.Contains(inline, p) {
			t.Errorf("missing %s in the inline material", p)
		}
	}
	if strings.Index(inline, "calc.py") > strings.Index(inline, "tests/test_calc.py") {
		t.Error("code must come first")
	}
	if !strings.Contains(note, "config.json") || !strings.Contains(note, "do not report them as missing") {
		t.Errorf("note: %q", note)
	}
	// a test too big to fit is listed, not dropped silently
	big := qgFile{Rel: "tests/test_big.py", New: strings.Repeat("x = 1\n", arOneShotMax/4), Code: true, Test: true}
	inline, note, _, ok = arOneShotMaterial(append(files[:1:1], big))
	if !ok || strings.Contains(inline, "test_big.py") || !strings.Contains(note, "tests/test_big.py") {
		t.Errorf("big test: ok=%v note=%q", ok, note)
	}
	prompt := strings.Count(arOneShotPrompt, "%s")
	if prompt != 3 {
		t.Errorf("arOneShotPrompt takes request, files and note: %d verbs", prompt)
	}
}

// (4) The harness's own suite run leaves no __pycache__ in the user's tree.
func TestVsRunSuiteLeavesNoPycache(t *testing.T) {
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"}
	h := newGuardHarness(t, files,
		fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", "    return a + b\n", "    return b + a\n")}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest tests.test_calc; rm -rf __pycache__ tests/__pycache__")}},
		fakeResp{Text: "pronto, árvore limpa"})
	if err := h.run(t, "Troque a ordem da soma em calc.py"); err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, s := range h.ui.toolStarts {
		ran = ran || s == "verify python3 -m unittest"
	}
	if !ran {
		t.Fatalf("the harness did not run the full suite: %v", h.ui.toolStarts)
	}
	var left []string
	_ = filepath.WalkDir(h.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && (d.Name() == "__pycache__" || d.Name() == ".pytest_cache") {
			left = append(left, p)
		}
		return nil
	})
	if len(left) > 0 {
		t.Errorf("caches left in the project: %v", left)
	}
	// caches that were there before the run stay
	keep := filepath.Join(h.root, "lib", "__pycache__")
	_ = os.MkdirAll(keep, 0o755)
	before := vsCacheDirs(h.root)
	_ = os.MkdirAll(filepath.Join(h.root, "__pycache__"), 0o755)
	vsRemoveNewCaches(h.root, before)
	if _, err := os.Stat(keep); err != nil {
		t.Error("a cache that existed before must stay")
	}
	if _, err := os.Stat(filepath.Join(h.root, "__pycache__")); err == nil {
		t.Error("a new cache must go")
	}
}

func TestVsNoBytecode(t *testing.T) {
	if got := vsNoBytecode(shellDialect{Kind: shBash}, "python3 -m unittest"); got != "export PYTHONDONTWRITEBYTECODE=1 && python3 -m unittest" {
		t.Errorf("bash: %q", got)
	}
	ps := vsNoBytecode(shellDialect{Kind: shPwsh}, "python -m pytest")
	if !strings.Contains(ps, "$env:PYTHONDONTWRITEBYTECODE='1'") || !strings.Contains(ps, "finally { $env:PYTHONDONTWRITEBYTECODE=$__jzPyB }") {
		t.Errorf("pwsh must set and restore the variable: %q", ps)
	}
}

// (5) A test file jazz created in an earlier turn of the same session is its
// own: a later turn (-c) may change it; weakening is still refused, an
// existing user test is still protected, and the set survives the session file.
func TestGuardOwnTestEditableNextTurn(t *testing.T) {
	slugTest := "import unittest\nfrom slug import slug\n\n\nclass S(unittest.TestCase):\n    def test_basico(self):\n        self.assertEqual(slug('A B'), 'A-B')\n"
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"}
	h := newGuardHarness(t, files,
		// turn 1: slug.py and its test
		fakeResp{Calls: []fakeCall{
			writeCall("w1", "slug.py", "def slug(s):\n    return s.replace(' ', '-')\n"),
			writeCall("w2", "tests/test_slug.py", slugTest),
			bashCall("b1", "python3 -m unittest -q"),
		}},
		fakeResp{Text: "pronto: slug.py e tests/test_slug.py"},
		// turn 2: lowercase; the test it wrote must change with it
		fakeResp{Calls: []fakeCall{
			editCall("e1", "slug.py", "s.replace(' ', '-')", "s.lower().replace(' ', '-')"),
			editCall("e2", "tests/test_slug.py", "'A-B')", "'a-b')"),
		}},
		fakeResp{Calls: []fakeCall{editCall("e3", "tests/test_slug.py", "    def test_basico", "    @unittest.skip('x')\n    def test_basico")}},
		fakeResp{Calls: []fakeCall{editCall("e4", "tests/test_calc.py", "5)", "6)")}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "python3 -m unittest -q")}},
		fakeResp{Text: "pronto: minúsculas"})
	if err := h.run(t, "Crie slug.py com slug(s) que troca espaço por hífen"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "beleza, agora deixa em minúscula"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) < 6 {
		tgDumpReqs(t, h)
		t.Fatalf("requests=%d", len(reqs))
	}
	if strings.Contains(lastToolText(reqs[3]), "Refused") {
		t.Errorf("editing the agent's own test was refused: %q", lastToolText(reqs[3]))
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "tests", "test_slug.py")); !strings.Contains(string(b), "'a-b')") {
		t.Errorf("own test not updated: %q", b)
	}
	if !strings.Contains(lastToolText(reqs[4]), "skip/xfail/only marker") {
		t.Errorf("weakening an own test must still be refused: %q", lastToolText(reqs[4]))
	}
	if !strings.Contains(lastToolText(reqs[5]), "existing test file") {
		t.Errorf("a user's existing test must stay protected: %q", lastToolText(reqs[5]))
	}
	// -c: the set comes back from the session file; a forged line does not
	path := h.ag.sess.Path
	own := hgLoadOwnTests(path, h.root)
	if !own[filepath.Join(h.root, "tests", "test_slug.py")] || own[filepath.Join(h.root, "tests", "test_calc.py")] {
		t.Errorf("loaded own set: %v", own)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"t":"owntests","id":"forged1","summary":"tests/test_calc.py"}` + "\n")
	f.Close()
	if hgLoadOwnTests(path, h.root)[filepath.Join(h.root, "tests", "test_calc.py")] {
		t.Error("an unsigned (forged) owntests line must not unprotect a test")
	}
	if s := hgLoadOwnTests(filepath.Join(t.TempDir(), "none.jsonl"), h.root); len(s) != 0 {
		t.Errorf("missing file: %v", s)
	}
}

// (6) The suite is still red after its reminder: the turn ends as
// quality_failed, unless the answer says the failure is old and unrelated;
// a green re-run after the reminder ends done.
func TestGuardSuiteStillRedIsNotSuccess(t *testing.T) {
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"}
	run := func(last ...fakeResp) (*harness, StopReason) {
		script := append([]fakeResp{
			{Calls: []fakeCall{editCall("e1", "calc.py", "a + b", "a - b"), bashCall("b1", "python3 -m unittest 2>&1 | tail -3")}},
			{Text: "tudo funcionando"},
		}, last...)
		h := newGuardHarness(t, files, script...)
		stop, err := h.ag.Run(context.Background(), "Mude calc.py")
		if err != nil {
			t.Fatal(err)
		}
		return h, stop
	}
	h, stop := run(fakeResp{Text: "não consegui deixar verde"})
	if stop != StopQualityFailed {
		tgDumpReqs(t, h)
		t.Errorf("still red: stop=%s, want quality_failed", stop)
	}
	if !anyContains(h.ui.warns, "o turno termina sem sucesso") {
		t.Errorf("the user must see why: %v", h.ui.warns)
	}
	pre := fakeResp{Text: "test_calc já falhava antes da minha mudança e não tem relação com ela"}
	if h, stop = run(pre, pre); stop != StopDone {
		tgDumpReqs(t, h)
		t.Errorf("pre-existing failure said explicitly: stop=%s, want done", stop)
	}
	h, stop = run(
		fakeResp{Calls: []fakeCall{editCall("e2", "calc.py", "a - b", "(a) + (b)") /* new size: the red run's .pyc must not look fresh */, bashCall("b2", "python3 -m unittest")}},
		fakeResp{Text: "corrigido, verde"})
	if stop != StopDone {
		tgDumpReqs(t, h)
		t.Errorf("green after the reminder: stop=%s, want done", stop)
	}
}
