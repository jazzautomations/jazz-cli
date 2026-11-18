//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cache cleanup (cargo, npm, a tool running in parallel) deletes third-party
// tests kept under a hidden tool directory. The guard had snapshotted them as
// the project's tests, wrote them back and blamed the model: carcasses of 13
// crates in ~/.cargo/registry/src on 2026-11-18.
func TestGuardLeavesHiddenToolTreesAlone(t *testing.T) {
	h := newGuardHarness(t, map[string]string{
		".cargo/registry/src/idx/anyhow-1.0/tests/test_fmt.rs":        "#[test]\nfn fmt() {}\n",
		".cargo/registry/src/idx/anyhow-1.0/.github/workflows/ci.yml": "on: push\n",
		".github/workflows/ci.yml":                                    "on: push\n",
		"tests/test_calc.py":                                          gTest,
		"calc.py":                                                     "def soma(a, b):\n    return a + b\n",
	},
		fakeResp{Calls: []fakeCall{bashCall("b1", "cd .cargo && rm -rf registry; cd ..")}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "cd .github/workflows && printf '# x' >> ci.yml; cd ../..")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Ajuste calc.py"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if s := lastToolText(reqs[1]); strings.Contains(s, "REVERTED") {
		t.Errorf("a tool's package store is not the project's tests: %q", s)
	}
	if _, err := os.Stat(filepath.Join(h.root, ".cargo/registry")); err == nil {
		t.Error("the guard wrote files back into the deleted package store")
	}
	if !reqs[2].contains("tool", "REVERTED your change to .github/workflows/ci.yml") {
		t.Errorf("the project's CI configuration stays protected: %q", lastToolText(reqs[2]))
	}
}

func TestGuardToolTree(t *testing.T) {
	cases := map[string]bool{
		".cargo/registry/src/idx/anyhow-1.0/tests/test_fmt.rs":        true,
		".cargo/registry/src/idx/anyhow-1.0/.github/workflows/ci.yml": true,
		"web/.yarn/cache/pkg/a.spec.ts":                               true,
		".circleci/config.yml":                                        false,
		"svc/.github/workflows/ci.yml":                                false,
		".github/actions/x/__tests__/a.test.ts":                       false,
		".gitlab-ci.yml":                                              false,
		".mocharc.json":                                               false,
		"tests/test_a.py":                                             false,
		"../outro/tests/test_a.py":                                    false,
	}
	for p, want := range cases {
		if got := hgToolTree(p); got != want {
			t.Errorf("%s: tool tree %v want %v", p, got, want)
		}
	}
}

// jazz opened in the home directory, work done in ~/proj: the snapshot budget
// went to whatever sorts first under home, the project's own tests were left
// unprotected, and other trees were "restored" instead.
func TestGuardHomeRootProtectsTheProjectTheShellIsIn(t *testing.T) {
	files := map[string]string{"proj/tests/test_calc.py": gTest, "proj/calc.py": "def soma(a, b):\n    return a + b\n"}
	const other = "def test_x():\n    assert True\n"
	for i := 0; i < hgMaxSnap+20; i++ {
		files[fmt.Sprintf("aaa/t%02d/test_%03d.py", i/50, i)] = other
	}
	h := newGuardHarness(t, files,
		fakeResp{Calls: []fakeCall{bashCall("b1", "cd proj")}},
		fakeResp{Text: "ok"},
		fakeResp{Calls: []fakeCall{bashCall("b2", "cd tests && printf '# y' >> test_calc.py; cd ..")}},
		fakeResp{Calls: []fakeCall{bashCall("b3", "cd ../aaa/t00 && printf '# z' >> test_000.py; cd ../../proj")}},
		fakeResp{Text: "pronto"})
	t.Setenv("HOME", h.root)
	if err := h.run(t, "entre em proj"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "Ajuste calc.py"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[3].contains("tool", "REVERTED your change to proj/tests/test_calc.py") {
		t.Errorf("the tests of the project the shell is in must be protected: %q", lastToolText(reqs[3]))
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "proj/tests/test_calc.py")); string(b) != gTest {
		t.Errorf("project test not restored: %q", b)
	}
	if s := lastToolText(reqs[4]); strings.Contains(s, "REVERTED") {
		t.Errorf("another tree under home is not this project: %q", s)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "aaa/t00/test_000.py")); string(b) != other+"# z" {
		t.Errorf("a file outside the project was rewritten: %q", b)
	}
}
