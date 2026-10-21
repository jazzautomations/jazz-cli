//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// v0.4 integration: the features merged in parallel must cooperate.

func TestV04RecapSeesThroughAppendedReminders(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "x"}, {Role: "tool", Content: "y"}}
	in := "what did you change?\n\n<system-reminder>Skill \"review-diff\" applies (picked by jazz).\n# Review\n</system-reminder>"
	if !acRecap(in, msgs) {
		t.Error("a recap question followed by a skill reminder must still count as a recap")
	}
	if acRecap("fix it\n\n<system-reminder>is this a question?</system-reminder>", msgs) {
		t.Error("a '?' inside a reminder must not make the request a question")
	}
}

func TestV04PlanCacheCountsQualityFailure(t *testing.T) {
	if !pcFailed(StopQualityFailed) {
		t.Error("quality_failed after reusing a plan is an objective failure of that plan")
	}
}

func TestV04SkillStillTriggersOnParsedOutput(t *testing.T) {
	t.Setenv("JAZZ_SKILLS", "")
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("=== RUN   TestOK" + strings.Repeat("x", i%7) + "\n--- PASS: TestOK (0.00s)\n")
	}
	b.WriteString("=== RUN   TestBad\n    a_test.go:12: got 1 want 2\n--- FAIL: TestBad (0.00s)\nFAIL\nFAIL\tpkg\t0.01s\n")
	cmd := "cat <<'EOF'\n" + b.String() + "EOF\nfalse"
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": cmd})}}},
		fakeResp{Text: "ok"},
	), nil)
	if err := h.run(t, "ok go"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()["b1"]
	if !strings.Contains(res, "[Output parsed as") || !strings.Contains(res, "TestBad") {
		t.Errorf("parsed output lost the failing test: %q", res)
	}
	if !strings.Contains(res, `Skill "`) {
		t.Errorf("a failing test run (parsed by outparse) must still trigger a skill: %q", res)
	}
}

func TestV04HandoffResetsExplorationBudget(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	st := h.ag.bg()
	st.reads["/x"] = bgRead{id: "r1"}
	st.turn = &bgTurn{explore: 9, nudgedExpl: true, warnedExpl: true, stepBudget: 12}
	h.ag.bgOnReset()
	if len(st.reads) != 0 || st.turn.explore != 0 || st.turn.nudgedExpl || st.turn.warnedExpl || st.turn.stepBudget != 12 {
		t.Errorf("after a handoff the exploration budget must restart (step budget kept): %+v reads=%d", st.turn, len(st.reads))
	}
}

func TestV04ScriptsEnvForAB(t *testing.T) {
	cfg := &Config{}
	t.Setenv("JAZZ_SCRIPTS", "off")
	if pfScriptsOn(cfg, "") {
		t.Error("JAZZ_SCRIPTS=off must hide the script tool")
	}
	if !pfScriptsOn(cfg, "on") {
		t.Error("--scripts on wins over the env")
	}
	t.Setenv("JAZZ_SCRIPTS", "on")
	cfg.Scripts = "off"
	if !pfScriptsOn(cfg, "") {
		t.Error("the env wins over config")
	}
}

func TestV04ReorderKeepsScriptsValue(t *testing.T) {
	got := strings.Join(reorderFlags([]string{"fix it", "--scripts", "off", "--recover", "off"}), " ")
	if got != "--scripts off --recover off fix it" {
		t.Errorf("reorderFlags = %q", got)
	}
}

func TestV04CraftEnvForAB(t *testing.T) {
	t.Setenv("JAZZ_CRAFT", "")
	if !strings.Contains(craftPrompt(), "<craft>") {
		t.Fatal("craft core is on by default")
	}
	t.Setenv("JAZZ_CRAFT", "off")
	p := craftPrompt()
	if strings.Contains(p, "craft>") || !strings.Contains(p, "</rules>") || !strings.Contains(p, "Your final message") {
		t.Errorf("JAZZ_CRAFT=off must drop only the craft block:\n%s", p)
	}
}

func TestV04ConfigSaveKeepsOutputKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	f := false
	c := &Config{ParseOutput: &f, GrepTrunc: "headtail", ToolBudget: 9000, Prefetch: "grep"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	s := opLoadSettings(filepath.Join(configDir(), "config.json"), func(string) string { return "" })
	if s.Parse || s.GrepMode != "headtail" || s.ToolBudget != 9000 || s.Prefetch != "grep" {
		t.Errorf("saving config.json must keep the output-shaping keys: %+v", s)
	}
}

// Found in eval v2 arm A (08-refactor-oculto, 03, 02): words the request uses to
// specify behaviour or to forbid something must not pick the task kind.
func TestV04ClassifyIgnoresNegationAndSpecErrors(t *testing.T) {
	cases := []struct {
		in, kind string
		noTest   bool
	}{
		{"Em pedidos.py mude o desconto máximo para 30% e a mensagem do erro deve ser \"x\". Não refatore nem unifique as cópias.", "change", false},
		{"Adicione prioridade às notas. Valor inválido é erro de uso; id inexistente dá o mesmo erro dos outros comandos. Atualize os testes em tests/.", "greenfield", false},
		{"Extraia as funções auxiliares para comum.py. O comportamento (valores, exceções e mensagens) tem que continuar igual. Não altere tests/.", "refactor", true},
		{"O worker caiu três vezes com traceback; corrija o código. Não edite os testes existentes.", "bugfix", true},
		{"Corrija o erro de parsing em datas.py", "bugfix", false},
		{"the app crashes with a panic when parsing dates", "bugfix", false},
		{"refactor the parser into two files and don't touch the tests", "refactor", true},
	}
	for _, c := range cases {
		got := bgClassify(c.in)
		if got.Kind != c.kind || got.NoTestEdit != c.noTest {
			t.Errorf("%q: kind=%s noTestEdit=%v, want %s %v", c.in, got.Kind, got.NoTestEdit, c.kind, c.noTest)
		}
	}
	if bgClassify("Em a.py troque 50 por 30. Não refatore nada.").Size == 'L' {
		t.Error("a forbidden refactor must not make the task large")
	}
}

func TestV04QualityNoTestDemandOnRefactorOrForbidden(t *testing.T) {
	if qgNeedTest(qgEnv{kind: "refactor", strict: true}, nil, "python3 -m unittest") {
		t.Error("a refactor (even strict) moves logic; it must not demand a new test")
	}
	if qgNeedTest(qgEnv{kind: "greenfield", strict: true, noTestEdit: true}, nil, "go test ./...") {
		t.Error("the request forbids changing tests: never demand one")
	}
	if !qgNeedTest(qgEnv{kind: "greenfield", strict: true}, nil, "go test ./...") {
		t.Error("strict greenfield still demands a test")
	}
	// 06-rodar-de-verdade: a strict (migration) bugfix in a project with no tests
	// made the model write a test suite nobody asked for.
	if qgNeedTest(qgEnv{root: t.TempDir(), kind: "bugfix", strict: true}, nil, "") {
		t.Error("strict must not demand a test in a project without tests")
	}
}

func TestV04SkillsFollowTaskKind(t *testing.T) {
	top := func(in string) string {
		c := skRank(skInputSignals(in))
		if len(c) == 0 {
			return ""
		}
		return c[0].s.Name
	}
	if got := top("Extraia as funções duplicadas para comum.py; exceções e mensagens têm que continuar iguais. É um refactor."); got != "refactor-safely" {
		t.Errorf("refactor request picked %q", got)
	}
	if got := top("Adicione o comando prioridade; id inexistente dá o mesmo erro dos outros comandos."); got == "debug-loop" {
		t.Error("an error spec inside a feature request must not pick debug-loop")
	}
	for _, c := range skRank(skInputSignals("Mude o limite para 30. Não refatore nem unifique os métodos.")) {
		if c.s.Name == "refactor-safely" {
			t.Error("\"não refatore\" must not pick refactor-safely")
		}
	}
	if got := top("o app quebra com traceback quando parseia datas, conserte"); got != "debug-loop" {
		t.Errorf("a bug report must still pick debug-loop, got %q", got)
	}
}

func TestV04DirListingIsNotExploration(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	bgWriteFiles(t, h.root, 1)
	if h.ag.bgExplore("read", map[string]any{"path": "."}) {
		t.Error("listing a directory must not count toward the exploration cap")
	}
	if !h.ag.bgExplore("read", map[string]any{"path": "f0.go"}) {
		t.Error("reading a file is exploration")
	}
}

// Eval v2 arm A, 09-ler-design: Poolside (via kilo) answered 502 "Upstream
// error ... HTTP 400: Tool calls cutoff by max_tokens" after streaming text,
// and the run died with an error. Now it is a truncated reply: one reminder,
// and the model issues a smaller call.
func TestV04ToolCutProviderErrorBecomesTruncation(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Status: 502, Body: `{"error":{"message":"Upstream error from Poolside: HttpError: HTTP 400: Tool calls cutoff by max_tokens.","code":502}}`},
		fakeResp{Text: "done"},
	), nil)
	if err := h.run(t, "write the module"); err != nil {
		t.Fatalf("the turn must survive a tool-cut provider error: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	if !reqs[1].contains("user", "cut off at the output token limit") {
		t.Error("the retry must tell the model its tool call was cut")
	}
	if !acToolCutErr(errString("HTTP 502: Upstream error from Poolside: HttpError: HTTP 400: Tool calls cutoff by max_tokens.")) || acToolCutErr(errString("HTTP 502: bad gateway")) {
		t.Error("acToolCutErr classification")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
