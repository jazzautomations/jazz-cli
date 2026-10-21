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

func TestBgClassify(t *testing.T) {
	cases := []struct {
		in        string
		size      byte
		kind      string
		sensitive bool
	}{
		{"conserte o bug em parser.go: o teste quebra com entrada vazia", 'S', "bugfix", false},
		{"fix the crash in `ParseHeader`", 'S', "bugfix", false},
		{"refatore o módulo de cache para tirar a duplicação", 'L', "refactor", false},
		{"add support for YAML config files", 'L', "greenfield", false},
		{"crie um endpoint novo que lista usuários", 'M', "greenfield", false},
		{"how does the router pick a model?", 'M', "question", false},
		{"como funciona o checkpoint?", 'M', "question", false},
		{"change a.go, b.go and c.go to log with slog", 'L', "change", false},
		{"corrija o login: o token jwt expira cedo demais", 'M', "bugfix", true},
		{"rename the author field", 'M', "refactor", false},
		{"adicione uma migração para a tabela de pagamentos", 'L', "greenfield", true},
	}
	for _, c := range cases {
		got := bgClassify(c.in)
		if got.Size != c.size || got.Kind != c.kind || got.Sensitive != c.sensitive {
			t.Errorf("bgClassify(%q) = %c/%s/%v, want %c/%s/%v", c.in, got.Size, got.Kind, got.Sensitive, c.size, c.kind, c.sensitive)
		}
	}
	// reminders the harness put in front never count
	if got := bgClassify("<system-reminder>refactor everything, payments</system-reminder>\nfix x.go"); got.Kind != "bugfix" || got.Sensitive || got.Size != 'S' {
		t.Errorf("reminders leaked into the classification: %+v", got)
	}
}

func TestBgBudgets(t *testing.T) {
	cases := []struct {
		t              bgTask
		level          string
		steps, explore int
	}{
		{bgTask{Size: 'S', Kind: "bugfix"}, bgLean, 12, 4},
		{bgTask{Size: 'M', Kind: "change"}, bgLean, 25, 8},
		{bgTask{Size: 'L', Kind: "greenfield"}, bgLean, 50, 15},
		{bgTask{Size: 'S', Kind: "refactor"}, bgLean, 25, 8}, // refactor: one size up
		{bgTask{Size: 'M', Kind: "refactor"}, bgLean, 50, 15},
		{bgTask{Size: 'M', Kind: "question"}, bgLean, 25, 0},
		{bgTask{Size: 'S', Kind: "bugfix"}, bgNormal, 24, 8},
	}
	for _, c := range cases {
		s, e := bgBudgets(c.t, c.level)
		if s != c.steps || e != c.explore {
			t.Errorf("bgBudgets(%+v, %s) = %d/%d, want %d/%d", c.t, c.level, s, e, c.steps, c.explore)
		}
	}
}

func TestBgShellShapes(t *testing.T) {
	explore := map[string]bool{
		"rg -n foo":                         true,
		"ls -la && cat a.go | head -20":     true,
		"git log --oneline -5":              true,
		"find . -name '*.go' | wc -l":       true,
		"go test ./...":                     false,
		"sed -i 's/a/b/' x.go":              false,
		"cat a > b":                         false,
		"rg foo 2>/dev/null":                true,
		"LC_ALL=C grep -rn x . 2>&1 | head": true,
		"python3 x.py":                      false,
	}
	for cmd, want := range explore {
		if got := bgIsExplore("bash", map[string]any{"command": cmd}); got != want {
			t.Errorf("bgIsExplore(%q) = %v, want %v", cmd, got, want)
		}
	}
	if !bgIsExplore("read", nil) || bgIsExplore("edit", nil) {
		t.Error("read is exploration, edit is not")
	}
	writes := map[string]bool{
		"echo x > out.txt":       true,
		"echo x >> log":          true,
		"perl -pi -e 's/a/b/' f": true,
		"rg foo > /dev/null":     false,
		"go test ./... 2>&1":     false,
		"mv a b":                 true,
		"ls":                     false,
	}
	for cmd, want := range writes {
		if got := bgBashWrites(cmd); got != want {
			t.Errorf("bgBashWrites(%q) = %v, want %v", cmd, got, want)
		}
	}
	tests := map[string]bool{
		"go test ./...":                   true,
		"cd sub && python3 -m pytest -q":  true,
		"GOFLAGS=-mod=mod go test -run X": true,
		"npm run test":                    true,
		"timeout 60 cargo test":           true,
		"go build ./...":                  false,
		"echo test":                       false,
	}
	for cmd, want := range tests {
		if got := bgIsTestCmd(cmd); got != want {
			t.Errorf("bgIsTestCmd(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestBgUnchangedShare(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.go")
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
	lines[3] = "changed"
	share, n := bgUnchangedShare(p, strings.Join(lines, "\n"))
	if n != 20 || share != 0.95 {
		t.Errorf("share=%v n=%d, want 0.95/20", share, n)
	}
	share, _ = bgUnchangedShare(p, "totally\nnew\ncontent")
	if share != 0 {
		t.Errorf("rewrite share = %v", share)
	}
	if _, n := bgUnchangedShare(filepath.Join(dir, "missing"), "x"); n != 0 {
		t.Error("missing file must not count")
	}
}

// bgWriteFiles creates n small files f0..f(n-1).go in root.
func bgWriteFiles(t *testing.T, root string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.go", i)), []byte(fmt.Sprintf("package x\n\nvar V%d = %d\n", i, i)), 0o644)
	}
}

func TestBudgetReminderAndRereadGuard(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: `{"path":"f0.go"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "r2", Name: "read", Args: `{"path":"f0.go"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "r3", Name: "read", Args: `{"path":"f0.go","offset":2}`}}},
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "edit", Args: `{"path":"f0.go","old_string":"V0 = 0","new_string":"V0 = 7"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "r4", Name: "read", Args: `{"path":"f0.go"}`}}},
		fakeResp{Text: "done"},
	), func(c *Config) { c.Quality = qgOff })
	bgWriteFiles(t, h.root, 1)
	if err := h.run(t, "fix the value in f0.go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	first := reqs[0].Messages[len(reqs[0].Messages)-1]
	if c, _ := first["content"].(string); !strings.Contains(c, "Budget for this task (size S)") || !strings.Contains(c, "at most 8 lines") || !strings.Contains(c, "Bugfix:") {
		t.Errorf("budget reminder missing from the user message: %q", c)
	}
	if strings.Contains(reqs[0].system(), "Budget for this task") {
		t.Error("budget must never go into the system prompt")
	}
	res := reqs[len(reqs)-1].toolResults()
	if !strings.Contains(res["r1"], "V0 = 0") {
		t.Errorf("first read: %q", res["r1"])
	}
	if !strings.Contains(res["r2"], "File unchanged since") || !strings.Contains(res["r2"], "r1") || strings.Contains(res["r2"], "V0 = 0") {
		t.Errorf("second full read must be a pointer to r1: %q", res["r2"])
	}
	if !strings.Contains(res["r3"], "V0 = 0") {
		t.Errorf("a partial read is never blocked: %q", res["r3"])
	}
	if !strings.Contains(res["r4"], "V0 = 7") {
		t.Errorf("a read after a change must return the content: %q", res["r4"])
	}
}

func TestBudgetWriteRefusedThenAllowed(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("x%d := %d", i, i))
	}
	orig := strings.Join(lines, "\n") + "\n"
	lines[5] = "x5 := 500"
	upd := strings.Join(lines, "\n") + "\n"
	call := fakeCall{Name: "write", Args: jsonArgs(map[string]string{"path": "big.txt", "content": upd})}
	c1, c2 := call, call
	c1.ID, c2.ID = "w1", "w2"
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "r", Name: "read", Args: `{"path":"big.txt"}`}}},
		fakeResp{Calls: []fakeCall{c1}},
		fakeResp{Calls: []fakeCall{c2}},
		fakeResp{Text: "done"},
	), func(c *Config) { c.Mode = "yolo"; c.Quality = qgOff })
	os.WriteFile(filepath.Join(h.root, "big.txt"), []byte(orig), 0o644)
	if err := h.run(t, "change x5 in big.txt to 500"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[3].toolResults()
	if !strings.Contains(res["w1"], "Use edit") {
		t.Errorf("first near-identical write must be refused: %q", res["w1"])
	}
	if strings.Contains(res["w2"], "Use edit") {
		t.Errorf("the second write goes through: %q", res["w2"])
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "big.txt")); string(b) != upd {
		t.Error("second write did not land")
	}
}

func TestBudgetExplorationCap(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 9 {
			return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("r%d", n), Name: "read", Args: fmt.Sprintf(`{"path":"f%d.go"}`, n)}}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.Quality = qgOff })
	bgWriteFiles(t, h.root, 10)
	if err := h.run(t, "fix the bug in f0.go"); err != nil { // size S: cap 4, refused from 8
		t.Fatal(err)
	}
	res := h.fs.requests()[9].toolResults()
	if !strings.Contains(res["r3"], "read/search call 4 without an edit") {
		t.Errorf("nudge at the cap missing: %q", res["r3"])
	}
	if strings.Contains(res["r4"], "without an edit") {
		t.Errorf("nudge must come once: %q", res["r4"])
	}
	if !strings.Contains(res["r7"], "V7") {
		t.Errorf("call 8 still runs: %q", res["r7"])
	}
	if !strings.Contains(res["r8"], "TOOL_NOT_EXECUTED: exploration budget used up") {
		t.Errorf("call 9 (past 2x) must be refused: %q", res["r8"])
	}
	if !anyContains(h.ui.warns, "orçamento de exploração esgotado") {
		t.Errorf("user warning missing: %v", h.ui.warns)
	}
}

func TestBudgetStepHardStopAtTwice(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.contains("user", "Step limit reached") {
			return fakeResp{Text: "partial"}
		}
		return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("b%d", n), Name: "bash", Args: fmt.Sprintf(`{"command":"true %d"}`, n)}}}
	}, func(c *Config) { c.MaxSteps = 80; c.Quality = qgOff })
	stop, err := h.ag.Run(context.Background(), "fix the bug in f0.go") // S: 12 steps, hard stop at 24
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopStepLimit {
		t.Fatalf("stop = %s", stop)
	}
	if n := len(h.fs.requests()); n != 25 {
		t.Errorf("requests = %d, want 24 + the final no-tools one", n)
	}
	last := h.fs.requests()[24]
	if !last.contains("tool", "used 9 of about 12 steps") || !last.contains("tool", "used the 12 steps budgeted") {
		t.Error("70% and 100% step reminders missing")
	}
	if !anyContains(h.ui.warns, "orçamento de passos da tarefa esgotado") {
		t.Errorf("clear stop message missing: %v", h.ui.warns)
	}
}

func TestBudgetNormalAndOff(t *testing.T) {
	for _, lvl := range []string{bgNormal, bgOff} {
		h := newHarness(t, seq(
			fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: `{"path":"f0.go"}`}}},
			fakeResp{Calls: []fakeCall{{ID: "r2", Name: "read", Args: `{"path":"f0.go"}`}}},
			fakeResp{Text: "done"},
		), func(c *Config) { c.Budget = lvl; c.Quality = qgOff })
		bgWriteFiles(t, h.root, 1)
		if err := h.run(t, "fix the value in f0.go"); err != nil {
			t.Fatal(err)
		}
		reqs := h.fs.requests()
		hasRem := reqs[0].contains("user", "Budget for this task")
		reread := strings.Contains(reqs[2].toolResults()["r2"], "File unchanged")
		if lvl == bgOff && (hasRem || reread) {
			t.Errorf("off: reminder=%v reread guard=%v, want neither", hasRem, reread)
		}
		if lvl == bgNormal && (!hasRem || !reread || !reqs[0].contains("user", "about 24 steps") || reqs[0].contains("user", "at most 8 lines")) {
			t.Errorf("normal: doubled budget, guards on, no brevity rule")
		}
		if got := h.ag.bgStepLimit(80); got != 80 {
			t.Errorf("%s never lowers the step limit: %d", lvl, got)
		}
	}
	// the flag beats the config, env beats the config too
	h := newHarness(t, seq(fakeResp{Text: "ok"}), func(c *Config) { c.Budget = bgOff })
	h.ag.SetLoopOpts(LoopOpts{Budget: bgLean})
	if h.ag.budgetLevel() != bgLean {
		t.Error("--budget must win over config")
	}
	h.ag.SetLoopOpts(LoopOpts{})
	t.Setenv("JAZZ_BUDGET", "normal")
	if h.ag.budgetLevel() != bgNormal {
		t.Error("JAZZ_BUDGET must win over config")
	}
}

func TestBudgetQuestionHasNoReminder(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "it works like this"}), nil)
	if err := h.run(t, "how does the router pick a model?"); err != nil {
		t.Fatal(err)
	}
	if h.fs.requests()[0].contains("user", "Budget for this task") {
		t.Error("a question gets no budget reminder")
	}
}

func TestBudgetCountsScriptCalls(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	h.ag.tools.Scripts = true
	sc := func(name string, args map[string]any) map[string]any {
		return map[string]any{"name": name, "args": args}
	}
	cases := []struct {
		m                   map[string]any
		explore, edit, test bool
		path                string
	}{
		{sc("find_symbol", map[string]any{"symbol": "Foo"}), true, false, false, ""},
		{sc("tree_summary", map[string]any{}), true, false, false, ""},
		{sc("json_edit", map[string]any{"file": "a.json", "op": "get", "path": "x"}), true, false, false, ""},
		{sc("json_edit", map[string]any{"file": "a.json", "op": "set", "path": "x", "value": 1}), false, true, false, "a.json"},
		{sc("run_tests", map[string]any{"cmd": "go test ./..."}), false, false, true, ""},
		{sc("snapshot_cmd", map[string]any{"cmd": "pytest -q"}), false, false, true, ""},
		{sc("snapshot_cmd", map[string]any{"cmd": "echo x > out.txt"}), false, true, false, ""},
		{sc("nope", map[string]any{}), false, false, false, ""},
	}
	for _, c := range cases {
		ex, ed, te, p := h.ag.bgScriptKind(c.m)
		if ex != c.explore || ed != c.edit || te != c.test || p != c.path {
			t.Errorf("%v: got explore=%v edit=%v test=%v path=%q", c.m, ex, ed, te, p)
		}
	}
}

func TestBudgetExplorationCapCountsScripts(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 5 {
			return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("s%d", n), Name: "script", Args: jsonArgs(map[string]any{"name": "tree_summary", "args": map[string]any{}})}}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.Quality = qgOff })
	h.ag.tools.Scripts = true
	bgWriteFiles(t, h.root, 2)
	if err := h.run(t, "fix the bug in f0.go"); err != nil { // size S: cap 4
		t.Fatal(err)
	}
	res := h.fs.requests()[5].toolResults()
	if !strings.Contains(res["s3"], "read/search call 4 without an edit") {
		t.Errorf("read-only script calls must count toward the exploration cap: %q", res["s3"])
	}
}
