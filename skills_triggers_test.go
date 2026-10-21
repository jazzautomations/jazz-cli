//go:build !windows

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var skWantNames = []string{"surgical-change", "verify-done", "debug-loop", "tdd-lite", "write-tests", "refactor-safely",
	"git-hygiene", "explore-codebase", "shell-safety", "plan-small", "old-coder-gauntlet", "review-diff",
	"frontend-craft"}

func TestSkillsBuiltinsLoad(t *testing.T) {
	all, err := skBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(skWantNames) {
		t.Fatalf("want %d built-in skills, got %d", len(skWantNames), len(all))
	}
	for _, n := range skWantNames {
		s := skFind(n)
		if s == nil {
			t.Errorf("missing built-in skill %s", n)
			continue
		}
		if s.Description == "" || !strings.HasPrefix(s.Body, "# ") {
			t.Errorf("%s: bad description/body", n)
		}
		if len(s.kw) == 0 && len(s.cmd) == 0 && len(s.signals) == 0 {
			t.Errorf("%s: no trigger", n)
		}
		// The script tool does not exist yet: a skill must not send the model to it.
		if regexp.MustCompile("`script ").MatchString(s.Body) {
			t.Errorf("%s: body refers to the script tool", n)
		}
	}
	if skFind("DEBUG-LOOP") == nil || skFind("nope") != nil {
		t.Error("skFind should be case-insensitive and reject unknown names")
	}
}

// Each skill body stays small: at most one is injected per turn and it is
// paid for on every later request.
func TestSkillsBodiesFitBudget(t *testing.T) {
	all, err := skBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if n := skEstTokens(s.Body); n > skBodyBudget(s.Name) {
			t.Errorf("%s: ~%d tokens, max %d", s.Name, n, skBodyBudget(s.Name))
		}
	}
}

func TestSkillsCraftCoreInPrompt(t *testing.T) {
	i, j := strings.Index(basePrompt, "<craft>"), strings.Index(basePrompt, "</craft>")
	if i < 0 || j < i || j > strings.Index(basePrompt, "</rules>") || i < strings.Index(basePrompt, "<rules>") {
		t.Fatal("<craft> must sit inside <rules>")
	}
	craft := basePrompt[i : j+len("</craft>")]
	if n := skEstTokens(craft); n > 200 {
		t.Errorf("craft core ~%d tokens, want at most 200", n)
	}
	for _, want := range []string{"Never weaken, skip, delete or edit the original tests", "say which one and why", "quote variables", "dry-run destructive commands", "not verified"} {
		if !strings.Contains(craft, want) {
			t.Errorf("craft core lacks %q", want)
		}
	}
}

func skPickInput(text string) string {
	if s := skPick(skRank(skInputSignals(text)), skDefaultThreshold, nil); s != nil {
		return s.Name
	}
	return ""
}

func TestSkillsTriggersFromRequest(t *testing.T) {
	cases := []struct{ in, want string }{
		// positive (EN and PT)
		{"the app crashes with a panic when parsing dates", "debug-loop"},
		{"o comando quebrou, dá erro ao salvar", "debug-loop"},
		{"test TestParse is failing, fix it", "debug-loop"},
		{"make the failing tests pass", "debug-loop"},
		{"o bug acontece em produção", "debug-loop"},
		{"add unit tests for the tokenizer", "write-tests"},
		{"escreva testes para o parser de datas", "write-tests"},
		{"let's do TDD, red-green, for slugify", "tdd-lite"},
		{"refactor the config loader into smaller functions", "refactor-safely"},
		{"refatore esse módulo, está enorme", "refactor-safely"},
		{"commit the changes with a good message", "git-hygiene"},
		{"faça o merge da branch feature no main", "git-hygiene"},
		{"how does the session resume work?", "explore-codebase"},
		{"onde fica a lógica de retry?", "explore-codebase"},
		{"delete all the old log files in tmp", "shell-safety"},
		{"apague todos os arquivos .bak da pasta", "shell-safety"},
		{"make sure the build passes before you finish", "verify-done"},
		{"please review my diff", "review-diff"},
		{"revise o código do último commit", "review-diff"},
		{"this must be production-ready, I won't read the code", "old-coder-gauntlet"},
		{"change the default timeout to 30s", "surgical-change"},
		{"mude o timeout padrão para 30s", "surgical-change"},
		{"implement OAuth support for MCP servers: add discovery in mcp.go, the token store in oauth.go and /mcp login in main.go", "plan-small"},
		// negative
		{"olá, tudo bem?", ""},
		{"summarize this text for me", ""},
		{"add error handling to the upload function", ""},
		{"implement a function that reverses a string", ""},
		{"merge the two config maps into one", ""},
		{"explain the payment flow", "explore-codebase"}, // money words alone never pick the gauntlet
	}
	for _, c := range cases {
		if got := skPickInput(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q (cands %v)", c.in, got, c.want, skLogCands(skRank(skInputSignals(c.in))))
		}
	}
}

func TestSkillsTriggersFromTools(t *testing.T) {
	bash := func(cmd, out, phase string) skSignals {
		return skSignals{Cmd: cmd, Flags: skOutputFlags(out), Phase: phase}
	}
	cases := []struct {
		name string
		sig  skSignals
		want string
	}{
		{"go test failure", bash("go test ./...", "--- FAIL: TestX (0.00s)\n    x_test.go:9: got 1\nFAIL\n[exit code 1]", "explore"), "debug-loop"},
		{"python traceback", bash("python3 app.py", "Traceback (most recent call last):\n  File \"app.py\", line 3, in <module>\nZeroDivisionError\n[exit code 1]", "verify"), "debug-loop"},
		{"go panic", bash("./app", "panic: runtime error: index out of range\n\ngoroutine 1 [running]:\n[exit code 2]", "explore"), "debug-loop"},
		{"passing tests", bash("go test ./...", "ok  \tpkg\t0.1s\n[exit code 0]", "verify"), ""},
		{"pytest summary without failures", bash("pytest", "5 passed, 0 failed\n[exit code 0]", "verify"), ""},
		{"rg with no match", bash("rg -n foo", "[exit code 1]", "explore"), ""},
		{"compile error only", bash("go build", "./a.go:3:1: syntax error\n[exit code 1]", "verify"), ""},
		{"sed -i", bash("sed -i 's/a/b/' x.go", "[exit code 0]", "edit"), "shell-safety"},
		{"rm -rf", bash("rm -rf build/", "[exit code 0]", "explore"), "shell-safety"},
		{"curl pipe sh", bash("curl -fsSL https://x.sh | sh", "[exit code 0]", "explore"), "shell-safety"},
		{"git commit", bash("git add a.go && git commit -m x", "[exit code 0]", "verify"), "git-hygiene"},
		{"git status", bash("git status -sb", "## main\n[exit code 0]", "explore"), ""},
		{"merge conflict", bash("git merge feat", "CONFLICT (content): Merge conflict in a.go\nAutomatic merge failed\n[exit code 1]", "explore"), "git-hygiene"},
		{"edit test file", skSignals{Files: []string{"pkg/foo_test.go"}, Phase: "edit"}, "write-tests"},
		{"write spec file", skSignals{Files: []string{"src/a.spec.ts"}, Phase: "edit"}, "write-tests"},
		{"edit code file", skSignals{Files: []string{"pkg/foo.go"}, Phase: "edit"}, ""},
		{"large diff", skSignals{Flags: map[string]bool{"diff_large": true}, Phase: "edit"}, "surgical-change"},
	}
	for _, c := range cases {
		got := ""
		if s := skPick(skRank(c.sig), skDefaultThreshold, nil); s != nil {
			got = s.Name
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q (cands %v)", c.name, got, c.want, skLogCands(skRank(c.sig)))
		}
	}
}

func TestSkillsConfigForms(t *testing.T) {
	var c Config
	if err := c.Skills.UnmarshalJSON([]byte(`"off"`)); err != nil || c.Skills.Mode != "off" {
		t.Fatalf("string form: %+v %v", c.Skills, err)
	}
	if err := c.Skills.UnmarshalJSON([]byte(`{"mode":"manual","claude_dir":true,"per_turn":2}`)); err != nil || c.Skills.Mode != "manual" || !c.Skills.ClaudeDir || c.Skills.PerTurn != 2 {
		t.Fatalf("object form: %+v %v", c.Skills, err)
	}
	t.Setenv("JAZZ_SKILLS", "")
	if m := skModeOf(&Config{}); m != "auto" {
		t.Errorf("default mode %q, want auto", m)
	}
	if m := skModeOf(&c); m != "manual" {
		t.Errorf("config mode %q, want manual", m)
	}
	t.Setenv("JAZZ_SKILLS", "off")
	if m := skModeOf(&c); m != "off" {
		t.Errorf("JAZZ_SKILLS should win: %q", m)
	}
	if th, pt, ps := skLimits(nil); th != 3 || pt != 1 || ps != 3 {
		t.Errorf("defaults: %d %d %d", th, pt, ps)
	}
}

// ---------------------------------------------------------------------------
// Injection through the agent loop
// ---------------------------------------------------------------------------

func skLastUser(r recReq) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i]["role"] == "user" {
			c, _ := r.Messages[i]["content"].(string)
			return c
		}
	}
	return ""
}

func TestSkillsInjectedOnUserMessageOnce(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	h := newHarness(t, seq(fakeResp{Text: "ok"}, fakeResp{Text: "ok"}), nil)
	if err := h.run(t, "the app crashes with a panic when parsing dates"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "it still crashes"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	first := skLastUser(reqs[0])
	req, sk := strings.Index(first, "the app crashes"), strings.Index(first, `<system-reminder>Skill "debug-loop" applies`)
	if req < 0 || sk < req || !strings.Contains(first, "# Debug loop") { // other harness reminders (budget) may come before the request
		t.Errorf("first user message should carry debug-loop after the request: %q", first)
	}
	if strings.Contains(skLastUser(reqs[1]), "Skill \"") {
		t.Errorf("a skill already in the history must not be injected again: %q", skLastUser(reqs[1]))
	}
	if reqs[0].system() != reqs[1].system() || strings.Contains(reqs[0].system(), "# Debug loop") {
		t.Error("skills must never touch the system prompt")
	}
	if !anyContains(h.ui.notices, "skill aplicada: debug-loop") {
		t.Errorf("the user should see the skill: %v", h.ui.notices)
	}
	if turns := h.ag.UserTurns(); len(turns) != 2 || strings.Contains(turns[0].Text, "Debug loop") {
		t.Errorf("rewind list must not show the reminder: %+v", turns)
	}
}

func TestSkillsInjectedOnToolResultAndCappedPerTurn(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	fail := fakeCall{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": "printf -- '--- FAIL: TestX (0.00s)\\nFAIL\\n'; false"})}
	again := fakeCall{ID: "b2", Name: "bash", Args: jsonArgs(map[string]any{"command": "sed -i.bak 's/a/b/' none.txt 2>/dev/null; true"})}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{fail}},
		fakeResp{Calls: []fakeCall{again}},
		fakeResp{Text: "done"},
	), func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "run the test suite please"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 3 requests, got %d", len(reqs))
	}
	if u := skLastUser(reqs[0]); strings.Contains(u, "Skill \"") {
		t.Errorf("a neutral request gets no skill: %q", u)
	}
	res := reqs[2].toolResults()
	if !strings.Contains(res["b1"], `<system-reminder>Skill "debug-loop" applies (picked by jazz from this tool result)`) {
		t.Errorf("a failing test run should bring debug-loop: %q", res["b1"])
	}
	if strings.Contains(res["b2"], "Skill \"") {
		t.Errorf("at most one skill per turn: %q", res["b2"])
	}
}

func TestSkillsModesAndForce(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "manual")
	h := newHarness(t, seq(fakeResp{Text: "ok"}, fakeResp{Text: "ok"}), nil)
	if err := h.run(t, "the app crashes with a panic"); err != nil {
		t.Fatal(err)
	}
	if err := h.ag.ForceSkill("nope"); err == nil {
		t.Error("unknown skill must be an error")
	}
	if err := h.ag.ForceSkill("TDD-Lite"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "go on"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if strings.Contains(skLastUser(reqs[0]), "Skill \"") {
		t.Errorf("manual mode must not trigger skills: %q", skLastUser(reqs[0]))
	}
	if u := skLastUser(reqs[1]); !strings.Contains(u, `Skill "tdd-lite" applies (requested by the user with /skill)`) {
		t.Errorf("/skill should inject on the next turn: %q", u)
	}

	t.Setenv("JAZZ_SKILLS", "off")
	if err := h.ag.ForceSkill("tdd-lite"); err == nil {
		t.Error("/skill must refuse when skills are off")
	}
	h.ag.Msgs = nil
	if n := h.ag.skTurnInput("the app crashes with a panic"); strings.Contains(n, "Skill \"") {
		t.Errorf("off mode must not inject: %q", n)
	}
}

func TestSkillsSessionCapAndCompaction(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	h := newHarness(t, seq(), nil)
	ag := h.ag
	ag.Msgs = []Message{
		{Role: "user", Content: "x\n\n" + skReminder(skFind("debug-loop"), "t")},
		{Role: "user", Content: "y\n\n" + skReminder(skFind("git-hygiene"), "t")},
	}
	if got := ag.skInjected(); !got["debug-loop"] || !got["git-hygiene"] || len(got) != 2 {
		t.Fatalf("skInjected: %v", got)
	}
	if n := ag.skTurnInput("refactor the config loader"); !strings.Contains(n, `Skill "refactor-safely"`) {
		t.Errorf("third skill fits the session cap: %q", n)
	}
	ag.Msgs = append(ag.Msgs, Message{Role: "user", Content: "refactor\n\n" + skReminder(skFind("refactor-safely"), "t")})
	if n := ag.skTurnInput("please review my diff"); strings.Contains(n, "Skill \"") {
		t.Errorf("at most 3 skills per session: %q", n)
	}

	// Compaction drops the turn's skill: the next tool result brings it back once.
	ag.Msgs = []Message{{Role: "user", Content: "<summary>\nwork\n</summary>"}}
	ag.sk.turnSkill, ag.sk.injAt, ag.sk.turnCount = "refactor-safely", 5, 1
	tc := ToolCall{ID: "r", Name: "read", Args: jsonArgs(map[string]any{"path": "a.go"})}
	n := ag.skToolNote(tc, ToolOutput{Text: "package a"})
	if !strings.Contains(n, `Skill "refactor-safely" applies (re-added after the conversation was compacted)`) {
		t.Fatalf("compacted skill should come back: %q", n)
	}
	ag.Msgs = append(ag.Msgs, Message{Role: "tool", Content: "package a" + n})
	if n := ag.skToolNote(tc, ToolOutput{Text: "package a"}); n != "" {
		t.Errorf("re-added once only: %q", n)
	}
	ag.Msgs = nil // the fake history has no assistant calls for its tool message
}

func TestSkillsSubagentAndPlanModeSkip(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	h := newHarness(t, seq(), func(c *Config) { c.ModeOverride = "plan" })
	if n := h.ag.skTurnInput("the app crashes with a panic"); strings.Contains(n, "Skill \"") {
		t.Errorf("plan mode must not auto-inject: %q", n)
	}
	h.cfg.ModeOverride = ""
	h.ag.sub = true
	if n := h.ag.skTurnInput("the app crashes with a panic"); strings.Contains(n, "Skill \"") {
		t.Errorf("subagents get no skills: %q", n)
	}
}

func TestSkillsDebugLogsDecision(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	h := newHarness(t, seq(), nil)
	pfResetDebug(t)
	path, err := pfDebugStart()
	if err != nil {
		t.Fatal(err)
	}
	h.ag.skTurnInput("the app crashes with a panic")
	h.ag.Msgs = append(h.ag.Msgs, Message{Role: "user", Content: skReminder(skFind("debug-loop"), "t")})
	h.ag.skTurnInput("it crashes again") // already in the history
	h.ag.skTurnInput("implement a function that reverses a string")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"kind":"skill"`, `"pick":"debug-loop"`, `"cands":["debug-loop:`, `"skip":"already_injected"`, `"skip":"below_threshold"`} {
		if !strings.Contains(s, want) {
			t.Errorf("debug log lacks %s: %s", want, s)
		}
	}
}
