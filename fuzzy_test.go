package main

import (
	"strings"
	"testing"
)

func TestFuzzyDistance(t *testing.T) {
	if d := fzDist("modle", "model", 100); d != fzSlip {
		t.Errorf("transposition = %d", d)
	}
	if fzDist("parsee", "parser", 100) >= fzDist("parsee", "parsel", 100) {
		t.Error("an adjacent key must cost less than an arbitrary letter")
	}
	if fzKey("Test-Calculo.PY") != fzKey("test_calculo.py") || fzFold("Ação") != "acao" {
		t.Error("fold/key")
	}
}

func TestFuzzyCommands(t *testing.T) {
	names := fzCommandNames(nil)
	one := map[string]string{
		"/modle": "/model", "/trre": "/tree", "/aprovals": "/approvals", "/hlep": "/help", "/clera": "/clear", "/cotext": "/context",
		"/compct": "/compact", "/expot": "/export", "/resum": "/resume", "/efort": "/effort", "/perm": "/permissions", "/skil": "/skill",
		"/aprovacoe": "/approvals", "/docter": "/doctor", "/udno": "/undo", "/rewnd": "/rewind",
	}
	for typed, want := range one {
		c, sure := fzResolve(typed, names)
		if len(c) == 0 || c[0] != want || !sure {
			t.Errorf("%s -> %v sure=%v, want %s", typed, c, sure, want)
		}
	}
	// ambiguous: a menu of 2-3, never a guess
	for _, typed := range []string{"/mod", "/ex", "/re"} {
		c, sure := fzResolve(typed, names)
		if sure || len(c) < 2 || len(c) > 3 {
			t.Errorf("%s -> %v sure=%v, want a 2-3 item menu", typed, c, sure)
		}
	}
	if c, _ := fzResolve("/xyzqwerty", names); len(c) != 0 {
		t.Errorf("garbage matched %v", c)
	}
	// exact names and aliases are untouched
	if c, sure := fzResolve("/perms", names); !sure || c[0] != "/permissions" {
		t.Errorf("alias: %v", c)
	}
}

func TestFuzzyFixCommandLine(t *testing.T) {
	fixed, menu, note := fzFixCommand("/modle", nil)
	if fixed != "/model" || menu != nil || !strings.Contains(note, "/modle como /model") {
		t.Errorf("/modle: %q %v %q", fixed, menu, note)
	}
	fixed, _, _ = fzFixCommand("/modle anthropic/claude-x", nil)
	if fixed != "/model anthropic/claude-x" {
		t.Errorf("args kept: %q", fixed)
	}
	// closed-set argument (PT/EN typos)
	for line, want := range map[string]string{"/mode trubo": "/mode turbo", "/mode atuo": "/mode auto", "/mode yol": "/mode yolo", "/effort hihg": "/effort high"} {
		f, m, _ := fzFixCommand(line, nil)
		if f != want {
			t.Errorf("%s -> %q (menu %v), want %q", line, f, m, want)
		}
	}
	// ambiguous argument: menu with full lines
	_, menu, _ = fzFixCommand("/mode a", nil)
	if len(menu) < 2 || menu[0] != "/mode ask" {
		t.Errorf("/mode a: %v", menu)
	}
	// valid input is never touched
	for _, l := range []string{"/mode turbo", "/mode full-auto", "/help", "/model", "/effort low", "/goal fazer tudo", "/xyzqwerty"} {
		if f, m, _ := fzFixCommand(l, nil); f != "" || m != nil {
			t.Errorf("%s changed to %q %v", l, f, m)
		}
	}
	// custom commands take part
	f, _, _ := fzFixCommand("/delpoy now", []string{"deploy"})
	if f != "/deploy now" {
		t.Errorf("custom: %q", f)
	}
}

func TestFuzzyVerbs(t *testing.T) {
	for w, want := range map[string]string{"conserta": "fix", "concerta": "fix", "conzerta": "fix", "arruma": "fix", "ajeita": "fix", "aruma": "fix",
		"roda": "run", "executa": "run", "toca": "run", "rdoa": "run", "exeucta": "run", "procura": "search", "prcura": "search", "Abrë": "open"} {
		if got := fzIntent(w); got != want {
			t.Errorf("fzIntent(%q) = %q, want %q", w, got, want)
		}
	}
	for _, w := range []string{"banana", "casa", "parser", "tudo"} {
		if got := fzIntent(w); got != "" {
			t.Errorf("fzIntent(%q) = %q, want none", w, got)
		}
	}
}

func TestFastPathTypos(t *testing.T) {
	p := fpProject{Langs: []string{"go"}, Test: "go test ./...", Build: "go build ./...", Git: true}
	set := fpIntents(t.TempDir())
	cases := map[string]string{
		"roda os teste":                "go test ./...",
		"roda os tesets":               "go test ./...",
		"rodar os testes":              "go test ./...",
		"toca os testes":               "go test ./...",
		"rdoa os testes":               "go test ./...",
		"git stauts":                   "git status --short --branch",
		"gti status":                   "", // 3-letter words are never repaired: stay literal
		"mostra o stauts":              "git status --short --branch",
		"o que mduou":                  "git status --short && git diff --stat && git diff",
		"procura parsr no codigo":      "-- parsr .",
		"procra parsr no codgio":       "-- parsr .",
		"acha parsr no codigo":         "-- parsr .",
		"ultimos comits":               "git log --oneline -n 15",
		"compila o projeto":            "go build ./...",
		"complia o projeto":            "go build ./...",
		"fala sobre a vida":            "",
		"procra Parsr no codgio de tw": "",
	}
	for in, want := range cases {
		m, ok := set.Match(in, p, true)
		switch {
		case want == "" && ok:
			t.Errorf("%q matched %q, want no fast path", in, m.Cmd)
		case want != "" && (!ok || !strings.Contains(m.Cmd, want)):
			t.Errorf("%q -> %q ok=%v, want it to contain %q", in, m.Cmd, ok, want)
		}
	}
	// a captured symbol is never "repaired" into another word
	m, ok := set.Match("procura teste no codigo", p, true)
	if ok && !strings.Contains(m.Cmd, "teste") {
		t.Errorf("symbol rewritten: %q", m.Cmd)
	}
}

func TestClassifierTypos(t *testing.T) {
	cases := []struct{ in, want string }{
		{"roda os teste", rtShell},
		{"rdoa os testes", rtShell},
		{"concerta o bug do login", rtBugfix},
		{"arruma o erro no parser", rtBugfix},
		{"ajeita esse crash no servidor", rtBugfix},
		{"exlique como o cache funciona", rtExplain},
		{"explica como o cache funciona", rtExplain},
		{"refatroe o modulo de pagamento", rtRefactor},
		{"intala as dependencias", rtShell},
		{"cira um comando novo pra exportar", rtFeature},
		{"toca o build e ve se passa", rtShell},
	}
	for _, c := range cases {
		got := rtClassify(c.in, rtRepo{})
		if got.Type != c.want {
			t.Errorf("%q: %s (via %s, scores %v), want %s", c.in, got.Type, got.Via, got.Scores, c.want)
		}
	}
	// clean prompts keep the exact old verdict (the repair only runs when undecided)
	if got := rtClassify("refatore o modulo de pagamento", rtRepo{}); got.Type != rtRefactor || got.Via != "rules" {
		t.Errorf("clean prompt: %+v", got)
	}
}

func TestSystemPromptTypoRuleIsStatic(t *testing.T) {
	root := t.TempDir()
	a, _ := buildSystemPrompt(root, nil)
	b, _ := buildSystemPrompt(root, nil)
	if a != b {
		t.Fatal("system prompt changes between builds (cache)")
	}
	i := strings.Index(a, "The user types fast")
	if i < 0 {
		t.Fatal("typo rule missing from the system prompt")
	}
	rule := a[i:]
	rule = rule[:strings.IndexByte(rule, '\n')]
	if len(rule) > 330 {
		t.Errorf("rule too long (%d bytes): %s", len(rule), rule)
	}
	// nothing dynamic leaks into the system prompt: the note is per user message
	niTouch("/tmp/zzz.go")
	if c, _ := buildSystemPrompt(root, nil); c != a {
		t.Error("session context changed the system prompt")
	}
	if strings.Contains(a, "nomes aproximados:") && strings.Contains(a, "→") {
		t.Error("a resolved name leaked into the system prompt")
	}
}
