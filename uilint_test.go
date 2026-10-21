//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The lint hook and the skill trigger share one list of UI globs.
func TestUILintGlobsMatchSkillTrigger(t *testing.T) {
	s := skFind("frontend-craft")
	if s == nil {
		t.Fatal("frontend-craft skill missing")
	}
	if strings.Join(s.files, ",") != strings.Join(uiLintGlobs, ",") {
		t.Errorf("triggers.json files %v != uiLintGlobs %v", s.files, uiLintGlobs)
	}
	for _, p := range []string{"index.html", "src/App.tsx", "a/b/Card.jsx", "Page.vue", "x.svelte", "style.css", "theme.scss", "tailwind.config.js", "tailwind.config.ts", "pages/index.astro", "STYLE.CSS"} {
		if !isUIFile(p) {
			t.Errorf("%s should be a UI file", p)
		}
	}
	for _, p := range []string{"", "main.go", "app.ts", "README.md", "config.json", "tailwind.js", "x.html.bak"} {
		if isUIFile(p) {
			t.Errorf("%s should not be a UI file", p)
		}
	}
}

func TestFrontendCraftTriggers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"crie uma landing page de uma padaria em index.html + style.css", "frontend-craft"},
		{"make the dashboard layout responsive on phones", "frontend-craft"},
		{"build a landing page for my bakery", "frontend-craft"},
		{"melhore o design da tela de login", "frontend-craft"},
		{"ajuste o estilo do componente Login.tsx", "frontend-craft"},
		{"o css do site está feio, deixa mais bonito", "frontend-craft"},
		// negative: design words that are not UI work
		{"design the REST API for orders", ""},
		{"implement the Reader interface in Go", ""},
		{"escreva o design doc do sistema de filas", ""},
		{"o teste de paginação falha", "debug-loop"},
	}
	for _, c := range cases {
		if got := skPickInput(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q (cands %v)", c.in, got, c.want, skLogCands(skRank(skInputSignals(c.in))))
		}
	}
	edit := func(p string) string {
		if s := skPick(skRank(skSignals{Files: []string{p}, Phase: "edit"}), skDefaultThreshold, nil); s != nil {
			return s.Name
		}
		return ""
	}
	for p, want := range map[string]string{"src/App.tsx": "frontend-craft", "style.css": "frontend-craft", "tailwind.config.js": "frontend-craft",
		"index.astro": "frontend-craft", "src/App.test.tsx": "write-tests", "main.go": ""} {
		if got := edit(p); got != want {
			t.Errorf("edit %s: got %q, want %q", p, got, want)
		}
	}
	if s := skFind("frontend-craft"); s == nil || !strings.Contains(s.Body, "prefers-reduced-motion") || !strings.Contains(s.Body, "Persuade") ||
		!strings.Contains(s.Body, "One look, fix the whole batch, then look again") {
		t.Error("frontend-craft body lacks the modes, the motion rule or the verify loop")
	}
}

func TestUILintOnFlagEnvConfig(t *testing.T) {
	t.Setenv("JAZZ_UI_LINT", "")
	if !pfUILintOn(&Config{}, "") || !pfUILintOn(nil, "") {
		t.Error("default must be on")
	}
	if pfUILintOn(&Config{UILint: "off"}, "") || !pfUILintOn(&Config{UILint: "off"}, "on") {
		t.Error("config off, flag wins")
	}
	t.Setenv("JAZZ_UI_LINT", "0")
	if pfUILintOn(&Config{}, "") || !pfUILintOn(&Config{}, "on") {
		t.Error("env off, flag wins over env")
	}
}

// End to end: a write of a sloppy page gets the findings appended, the same
// findings again get one line, the fixed file gets "clean now", a non-UI
// file and --ui-lint off get nothing.
func TestUILintHookAppendsFindings(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	slop, err := os.ReadFile(filepath.Join("jazz-scripts", "testdata", "ui_lint", "slop.html"))
	if err != nil {
		t.Fatal(err)
	}
	css, _ := os.ReadFile(filepath.Join("jazz-scripts", "testdata", "ui_lint", "slop.css"))
	clean, _ := os.ReadFile(filepath.Join("jazz-scripts", "testdata", "ui_lint", "clean.html"))
	// the page with its stylesheet inlined: more findings than the cap
	page := strings.Replace(string(slop), "</head>", "<style>\n"+string(css)+"</style>\n</head>", 1)
	w := func(id, p, content string) fakeCall {
		return fakeCall{ID: id, Name: "write", Args: jsonArgs(map[string]any{"path": p, "content": content})}
	}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{w("w1", "index.html", page)}},
		fakeResp{Calls: []fakeCall{{ID: "w2", Name: "edit", Args: jsonArgs(map[string]any{"path": "index.html", "old_string": "Get started", "new_string": "Start now"})}}},
		fakeResp{Calls: []fakeCall{w("w3", "index.html", string(clean))}},
		fakeResp{Calls: []fakeCall{w("w4", "notes.txt", "lorem ipsum")}},
		fakeResp{Text: "done"},
	), func(c *Config) { c.Mode = "yolo" })
	h.ag.tools.UILint = true
	if err := h.run(t, "write the page"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[4].toolResults()
	r1 := res["w1"]
	if !strings.Contains(r1, "<system-reminder>ui_lint (frontend-craft detector) on index.html:") ||
		!strings.Contains(r1, "index.html:5 error zoom-blocked") || !strings.Contains(r1, "more (script ui_lint lists them all)") {
		t.Errorf("first write should carry the findings: %q", r1)
	}
	if n := strings.Count(r1, "\n- index.html:"); n != uiLintMaxLines {
		t.Errorf("findings shown = %d, want the cap %d", n, uiLintMaxLines)
	}
	if !strings.Contains(res["w2"], "still has the same") || strings.Contains(res["w2"], "\n- index.html:") {
		t.Errorf("same findings must collapse to one line: %q", res["w2"])
	}
	if !strings.Contains(res["w3"], "ui_lint: index.html is clean now.") {
		t.Errorf("fixed file should say clean: %q", res["w3"])
	}
	if strings.Contains(res["w4"], "<system-reminder>ui_lint") {
		t.Errorf("non-UI file must not be linted: %q", res["w4"])
	}
	if !anyContains(h.ui.notices, "ui_lint: index.html — ") || !anyContains(h.ui.notices, "ui_lint: index.html limpo") {
		t.Errorf("the user should see the lint runs: %v", h.ui.notices)
	}

	off := newHarness(t, seq(fakeResp{Calls: []fakeCall{w("w1", "index.html", string(slop))}}, fakeResp{Text: "done"}),
		func(c *Config) { c.Mode = "yolo" })
	if err := off.run(t, "write the page"); err != nil {
		t.Fatal(err)
	}
	if r := off.fs.requests()[1].toolResults()["w1"]; strings.Contains(r, "<system-reminder>ui_lint") {
		t.Errorf("UILint=false must not lint: %q", r)
	}
}
