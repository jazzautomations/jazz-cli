package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The suite has the size and the categories of the spec, and the tasks that
// need no toolchain pass with no false success.
func TestEvalOfflineSuite(t *testing.T) {
	var suite struct {
		Tasks []eoTask `json:"tasks"`
	}
	if err := json.Unmarshal(eoTasksJSON, &suite); err != nil {
		t.Fatal(err)
	}
	if len(suite.Tasks) < 30 {
		t.Fatalf("%d tasks, the spec skeleton needs >= 30", len(suite.Tasks))
	}
	cats := map[string]bool{}
	ids := map[string]bool{}
	for _, tk := range suite.Tasks {
		cats[tk.Cat] = true
		if ids[tk.ID] {
			t.Errorf("duplicate id %s", tk.ID)
		}
		ids[tk.ID] = true
	}
	for _, c := range []string{"atalho", "impreciso", "ambíguo", "falha recuperável", "impossível", "segurança", "composta"} {
		if !cats[c] {
			t.Errorf("no task of category %s", c)
		}
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	defer epSetForTest(nil)()
	only := map[string]bool{"open-exact": true, "open-typo": true, "open-stem": true, "ambiguous-answer": true, "ambiguous-noanswer": true,
		"tool-typo": true, "host-typo": true, "open-not-found": true, "chitchat": true, "tests-no-runner": true,
		"guard-etc-containers": true, "guard-run-ok": true, "guard-remove-engine": true}
	var out bytes.Buffer
	rep, err := eoRun(only, false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.N != len(only) || rep.Summary.FalseSuccess != 0 || rep.Summary.Success != rep.Summary.N {
		t.Fatalf("summary %+v\n%s", rep.Summary, out.String())
	}
	dir := t.TempDir()
	if code := cmdEval(dir, []string{"offline", "--only", "open-exact,guard-wsl", "--out", filepath.Join(dir, "results")}, &out); code != 0 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	m, _ := filepath.Glob(filepath.Join(dir, "results", "offline-*.json"))
	if len(m) != 1 {
		t.Fatalf("results file: %v", m)
	}
	b, _ := os.ReadFile(m[0])
	for _, k := range []string{`"false_success"`, `"latency_p50_ms"`, `"peak_rss_kb"`, `"questions_needed"`, `"no_llm_rate"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("results lack %s", k)
		}
	}
}

func TestEvalFixtureBaseUsesSandboxTmp(t *testing.T) {
	tmp := t.TempDir()
	base, err := eoFixtureBase(&Sandbox{Backend: sbBwrap, Tmp: tmp})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if filepath.Dir(base) != tmp {
		t.Fatalf("fixtures at %s, want under %s", base, tmp)
	}
	b2, _ := eoFixtureBase(nil)
	defer os.RemoveAll(b2)
	if filepath.Dir(b2) != filepath.Dir(t.TempDir()) && !strings.HasPrefix(b2, os.TempDir()) {
		t.Fatalf("no sandbox: %s", b2)
	}
}

func TestRecoverBwrapErrorIsCommandMissing(t *testing.T) {
	if c := rvClassify(func() rvObs {
		o := rvNoCmd()
		o.Exit, o.Output = 1, "bwrap: Can't chdir to /tmp/x: No such file or directory"
		return o
	}()); c != rvCommandMissing {
		t.Fatalf("category %s", c)
	}
}
