//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// QA 0.14 (lanes skills/python): the checklist's test item accepts an answer
// that names the existing test covering the change; a turn the gate ends as
// failed still prints the kept answer in -p; pytest-style tests get pytest.

const qaOrigAnswer = "Os testes do calc estão verdes agora: soma aceita None como zero nos dois argumentos e o resto do comportamento ficou igual."

// qaCiteRun: a bugfix with new branches and no red run before it, so the
// checklist asks for a test; the reply to it is reply.
func qaCiteRun(t *testing.T, reply string, sink HeadlessSink) (*harness, StopReason) {
	t.Helper()
	broken := "def soma(a, b):\n    return a + b\n"
	gTestNone := gTest + "\n    def test_soma_none(self):\n        self.assertEqual(soma(None, 2), 2)\n"
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTestNone, "calc.py": broken}
	h := newGuardHarness(t, files,
		fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", broken, tgCalcFixed)}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest -q")}},
		fakeResp{Text: qaOrigAnswer},
		fakeResp{Text: reply},
		fakeResp{Text: "pronto (de novo)"})
	if sink != nil {
		h.ag.ui = sink
	}
	stop, err := h.ag.Run(context.Background(), "Corrija o bug em soma (calc.py): None deve contar como zero")
	if err != nil {
		t.Fatal(err)
	}
	asked := false
	for _, r := range h.fs.requests() {
		asked = asked || r.contains("user", "or say which existing test already covers it")
	}
	if !asked {
		tgDumpReqs(t, h)
		t.Fatal("setup: the checklist did not ask for a test")
	}
	return h, stop
}

// (1) Naming a real existing test (file or function) answers the item.
func TestQualityCitedExistingTestAccepted(t *testing.T) {
	for name, reply := range map[string]string{
		"func": "Falso positivo: test_soma_none (tests/test_calc.py:8) já cobre o novo if; " + qaOrigAnswer,
		"file": "O item 1 já está coberto por tests/test_calc.py, que testa soma com None. " + qaOrigAnswer,
	} {
		t.Run(name, func(t *testing.T) {
			h, stop := qaCiteRun(t, reply, nil)
			if stop != StopDone {
				tgDumpReqs(t, h)
				t.Fatalf("stop=%s, want done (the answer named an existing test)", stop)
			}
			if !anyContains(h.ui.notices, "apontou o teste existente") {
				t.Errorf("notice missing: %v", h.ui.notices)
			}
		})
	}
}

// (1) A name that exists nowhere does not; (2) the -p sinks print the
// original final answer together with the reply on that failed path.
func TestQualityCitedMissingTestFailsKeepsAnswer(t *testing.T) {
	// long enough that acMergeAnswer alone would have replaced the kept answer
	reply := "Falso positivo: o teste test_soma_com_nada em tests/test_inexistente.py cobre a mudança nos dois ramos do if novo, então não criei teste novo e não mexi em mais nada no calc.py."
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			var sink HeadlessSink
			if format == "json" {
				sink = NewJSONUI(&out, false)
			} else {
				sink = &HeadlessUI{outw: &out, errw: io.Discard}
			}
			h, stop := qaCiteRun(t, reply, sink)
			if stop != StopQualityFailed {
				tgDumpReqs(t, h)
				t.Fatalf("stop=%s, want quality_failed (nothing real was named)", stop)
			}
			sink.Finish(RunResult{Status: stop})
			got := out.String()
			if format == "json" {
				var obj map[string]any
				if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
					t.Fatal(err)
				}
				got, _ = obj["text"].(string)
			}
			if !strings.Contains(got, qaOrigAnswer) || !strings.Contains(got, "test_inexistente") || !strings.Contains(got, acGateFollowLabel) {
				t.Errorf("stdout must keep the original answer and the reply:\n%s", got)
			}
		})
	}
}

func TestQgCitesExistingTest(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tests"), 0o755)
	os.WriteFile(filepath.Join(root, "tests", "test_calc.py"), []byte("def test_media_vazia():\n    pass\n\nclass T:\n    def test_mediana_par(self):\n        pass\n"), 0o644)
	os.WriteFile(filepath.Join(root, "calc_test.go"), []byte("package m\n\nfunc TestSoma(t *testing.T) {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "calc.py"), []byte("def test_fake():\n    pass\n"), 0o644)
	for text, want := range map[string]bool{
		"test_media_vazia (tests/test_calc.py:4) cobre o if":  true,
		"coberto por `./tests/test_calc.py`":                  true,
		"o test_mediana_par cobre":                            true,
		"TestSoma cobre":                                      true,
		"calc_test.go cobre":                                  true,
		"test_nao_existe cobre":                               false,
		"tests/test_outro.py cobre":                           false,
		"calc.py já cobre":                                    false, // not a test file
		"test_fake cobre":                                     false, // defined only outside test files
		"../../etc/test_x.py cobre":                           false,
		"falso positivo, os testes existentes já cobrem isso": false,
	} {
		if got := qgCitesExistingTest(text, root); got != want {
			t.Errorf("%q: got %v want %v", text, got, want)
		}
	}
	issues := []qgIssue{{Msg: "a"}, {Msg: "gap", TestGap: true}}
	if left := qgDropAnsweredTestGap(issues, "TestSoma cobre", root); len(left) != 1 || left[0].Msg != "a" {
		t.Errorf("drop: %v", left)
	}
	if left := qgDropAnsweredTestGap(issues[:1], "TestSoma cobre", root); len(left) != 1 {
		t.Errorf("no gap item: %v", left)
	}
}

func TestAcMergeFailed(t *testing.T) {
	long := strings.Repeat("resposta longa ao checklist ", 10)
	if got := acMergeFailed(qaOrigAnswer, long); !strings.HasPrefix(got, qaOrigAnswer) || !strings.Contains(got, acGateFollowLabel) {
		t.Errorf("long reply must not replace the kept answer: %q", got)
	}
	if got := acMergeFailed(qaOrigAnswer, "ok"); got != qaOrigAnswer {
		t.Errorf("ack: %q", got)
	}
	if got := acMergeFailed(qaOrigAnswer, qaOrigAnswer+" E mais."); got != qaOrigAnswer+" E mais." {
		t.Errorf("containing reply: %q", got)
	}
	if got := acMergeFailed("", "só isso"); got != "só isso" {
		t.Errorf("nothing kept: %q", got)
	}
	// the failed flag lasts one turn
	var out bytes.Buffer
	h := &HeadlessUI{outw: &out, errw: io.Discard}
	h.StreamText("primeira")
	h.KeepAnswer()
	h.GateFailed()
	h.StreamText(long)
	h.TurnEnd(TurnStats{})
	if !strings.HasPrefix(h.Last, "primeira") {
		t.Errorf("turn 1: %q", h.Last)
	}
	if h.failed {
		t.Error("failed flag must reset at turn end")
	}
}

// qaPytestProbe makes the pytest probe answer ok for the test.
func qaPytestProbe(t *testing.T, ok bool) {
	t.Helper()
	prev := hgPytestProbe
	hgPytestProbe = func() bool { return ok }
	hgPytestOnce = sync.Once{}
	t.Cleanup(func() { hgPytestProbe = prev; hgPytestOnce = sync.Once{} })
}

// (3) pytest-style tests without a marker file: pytest when installed;
// unittest plus an environment note when not; unittest-style unchanged.
func TestHgTestCommandPytestStyle(t *testing.T) {
	write := func(root, rel, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(s), 0o644)
	}
	styles := map[string]string{
		"bare":    "from textkit import capitalizar\n\ndef test_capitalizar():\n    assert capitalizar('a') == 'A'\n",
		"import":  "import pytest\nfrom estoque import baixa\n\nclass TestBaixa:\n    def test_zero(self):\n        with pytest.raises(ValueError):\n            baixa(0)\n",
		"fixture": "class TestExport:\n    def test_csv(self, tmp_path):\n        assert tmp_path.exists()\n",
	}
	for name, src := range styles {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(root, "tests/test_x.py", src)
			qaPytestProbe(t, true)
			if c := hgTestCommand(root); c != "python3 -m pytest -q" {
				t.Errorf("pytest installed: %q", c)
			}
			if n := hgPyTestNote(root, hgTestCommand(root)); n != "" {
				t.Errorf("no note with pytest: %q", n)
			}
			qaPytestProbe(t, false)
			c := hgTestCommand(root)
			if c != "python3 -m unittest" {
				t.Errorf("pytest missing: %q", c)
			}
			if n := hgPyTestNote(root, c); !strings.Contains(n, "pytest is not installed") || !strings.Contains(n, "do not rewrite") {
				t.Errorf("note missing: %q", n)
			}
		})
	}
	root := t.TempDir()
	write(root, "tests/test_calc.py", gTest)
	qaPytestProbe(t, true)
	if c := hgTestCommand(root); c != "python3 -m unittest" {
		t.Errorf("unittest-style must stay unittest: %q", c)
	}
	qaPytestProbe(t, false)
	if n := hgPyTestNote(root, "python3 -m unittest"); n != "" {
		t.Errorf("unittest-style: no note: %q", n)
	}
	// the environment line carries the note
	write(root, "tests/test_bare.py", styles["bare"])
	if env := envBlock(root); !strings.Contains(env, "test note: the tests look pytest-style") {
		t.Errorf("environment line without the note:\n%s", env)
	}
}
