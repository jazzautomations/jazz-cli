//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func acSteerWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSteeringParseKiro(t *testing.T) {
	cases := []struct {
		src, inc string
		pats     []string
		name     string
	}{
		{"just text", "always", nil, "x"},
		{"---\ninclusion: always\n---\nbody", "always", nil, "x"},
		{"---\ninclusion: fileMatch\nfileMatchPattern: \"components/**/*.tsx\"\n---\nbody", "fileMatch", []string{"components/**/*.tsx"}, "x"},
		{"---\ninclusion: fileMatch\nfileMatchPattern: [\"*.ts\", '*.tsx']\n---\nbody", "fileMatch", []string{"*.ts", "*.tsx"}, "x"},
		{"---\ninclusion: fileMatch\nfileMatchPattern:\n  - \"*.go\"\n  - go.mod\n---\nbody", "fileMatch", []string{"*.go", "go.mod"}, "x"},
		{"---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.{ts,tsx}\"\n---\nbody", "fileMatch", []string{"**/*.{ts,tsx}"}, "x"},
		{"---\ninclusion: fileMatch\n---\nbody", "manual", nil, "x"}, // no pattern: never automatic
		{"---\r\ninclusion: auto\r\nname: api-rules\r\ndescription: REST conventions\r\n---\r\nbody", "auto", nil, "api-rules"},
		{"---\ninclusion: manual\n---\nbody", "manual", nil, "x"},
		{"---\ninclusion: sometimes\n---\nbody", "manual", nil, "x"}, // typo: safest choice
	}
	for i, c := range cases {
		r := acParseSteering("/p/x.md", c.src, false)
		if r.Inclusion != c.inc || strings.Join(r.Patterns, "|") != strings.Join(c.pats, "|") || r.Name != c.name {
			t.Errorf("case %d: got %q %q %q, want %q %q %q", i, r.Inclusion, r.Patterns, r.Name, c.inc, c.pats, c.name)
		}
		if strings.Contains(r.Body, "inclusion") || strings.Contains(r.Body, "---") {
			t.Errorf("case %d: front matter leaked into body: %q", i, r.Body)
		}
	}
	if r := acParseSteering("/p/x.md", "---\ninclusion: auto\ndescription: d\n---\n", false); r.Body != "" || r.Description != "d" {
		t.Errorf("empty body: %+v", r)
	}
	big := "---\ninclusion: always\n---\n" + strings.Repeat("é", acSteerBodyMax)
	if r := acParseSteering("/p/x.md", big, false); len(r.Body) > acSteerBodyMax+40 || !strings.HasSuffix(r.Body, "[… truncated]") {
		t.Errorf("body must be capped, got %d bytes", len(r.Body))
	}
}

func TestSteeringParseCursor(t *testing.T) {
	cases := []struct{ src, inc, pats, desc string }{
		{"---\ndescription: x\nglobs:\nalwaysApply: true\n---\nb", "always", "", "x"},
		{"---\ndescription:\nglobs: src/**/*.ts, lib/*.go\nalwaysApply: false\n---\nb", "fileMatch", "src/**/*.ts|lib/*.go", ""},
		{"---\nglobs: *.{ts,tsx}\n---\nb", "fileMatch", "*.{ts,tsx}", ""},
		{"---\ndescription: RPC boilerplate\nglobs:\nalwaysApply: false\n---\nb", "auto", "", "RPC boilerplate"},
		{"---\ndescription:\nglobs:\nalwaysApply: false\n---\nb", "manual", "", ""},
		{"no front matter", "manual", "", ""},
	}
	for i, c := range cases {
		r := acParseSteering("/p/.cursor/rules/rpc.mdc", c.src, true)
		if r.Inclusion != c.inc || strings.Join(r.Patterns, "|") != c.pats || r.Description != c.desc || r.Name != "rpc" {
			t.Errorf("case %d: got %+v", i, r)
		}
	}
}

func TestSteeringMatch(t *testing.T) {
	root := "/proj"
	m := func(pats []string, p string) bool {
		return acSteeringMatch(SteeringRule{Patterns: pats}, p, root)
	}
	yes := []struct {
		pats []string
		p    string
	}{
		{[]string{"*.go"}, "/proj/a/b/c.go"},
		{[]string{"src/**/*.ts"}, "/proj/src/x/y.ts"},
		{[]string{"src/**/*.ts"}, "/proj/src/y.ts"},
		{[]string{"**/*.{ts,tsx}"}, "/proj/web/a.tsx"},
		{[]string{"/docs/*.md"}, "/proj/docs/a.md"},
		{[]string{"./go.mod"}, "/proj/go.mod"},
		{[]string{"nope", "app/api/**/*"}, "/proj/app/api/v1/h.go"},
	}
	for _, c := range yes {
		if !m(c.pats, c.p) {
			t.Errorf("%v should match %s", c.pats, c.p)
		}
	}
	no := []struct {
		pats []string
		p    string
	}{
		{[]string{"*.go"}, "/elsewhere/c.go"}, // outside the project
		{[]string{"src/**/*.ts"}, "/proj/lib/y.ts"},
		{[]string{"**/*.{ts,tsx}"}, "/proj/a.js"},
		{[]string{""}, "/proj/a.js"},
		{nil, "/proj/a.js"},
	}
	for _, c := range no {
		if m(c.pats, c.p) {
			t.Errorf("%v must not match %s", c.pats, c.p)
		}
	}
	if got := acSteerBraces("a{b,c}d{e,f}"); strings.Join(got, ",") != "abde,abdf,acde,acdf" {
		t.Errorf("braces: %v", got)
	}
}

// acSteerProject writes one rule of each kind into a project and a global rule.
func acSteerProject(t *testing.T, root string) {
	t.Helper()
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "style.md"), "---\ninclusion: always\n---\nALWAYS-STYLE-BODY")
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "api.md"), "---\ninclusion: auto\nname: api\ndescription: REST API conventions\n---\nAUTO-API-BODY")
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "release.md"), "---\ninclusion: manual\n---\nMANUAL-RELEASE-BODY")
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "go.md"), "---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\nGO-FILEMATCH-BODY")
	acSteerWrite(t, filepath.Join(root, ".cursor", "rules", "ts.mdc"), "---\ndescription:\nglobs: *.md\nalwaysApply: false\n---\nTS-CURSOR-BODY")
	acSteerWrite(t, filepath.Join(root, ".cursor", "rules", "base.mdc"), "---\nalwaysApply: true\n---\nCURSOR-ALWAYS-BODY")
}

func TestSteeringPromptAndTrust(t *testing.T) {
	home := pfTestHome(t)
	root := t.TempDir()
	acSteerWrite(t, filepath.Join(home, "steering", "global.md"), "GLOBAL-ALWAYS-BODY")
	acSteerProject(t, root)

	sys := BuildSystemPrompt(root, nil)
	if !strings.Contains(sys, "GLOBAL-ALWAYS-BODY") {
		t.Error("global steering is always trusted")
	}
	for _, s := range []string{"ALWAYS-STYLE-BODY", "CURSOR-ALWAYS-BODY", "api (auto)", ".jazz/steering/release.md"} {
		if strings.Contains(sys, s) {
			t.Errorf("untrusted project steering leaked into the prompt: %q", s)
		}
	}
	summary, kinds := pfTrustSummary(root)
	if strings.Join(kinds, ",") != "steering" || !strings.Contains(summary, ".cursor/rules/ts.mdc") || !strings.Contains(summary, "fileMatch **/*.go") {
		t.Fatalf("trust summary: %v %q", kinds, summary)
	}
	var w bytes.Buffer
	if pfTrustGate(root, false, nil, &w) || !strings.Contains(w.String(), "steering") {
		t.Errorf("headless must skip untrusted steering and say so: %q", w.String())
	}
	if !pfTrustGate(root, true, strings.NewReader("s\n"), &bytes.Buffer{}) {
		t.Fatal("answer s must trust")
	}

	sys = BuildSystemPrompt(root, nil)
	for _, s := range []string{"GLOBAL-ALWAYS-BODY", "ALWAYS-STYLE-BODY", "CURSOR-ALWAYS-BODY", "- api (auto): REST API conventions (.jazz/steering/api.md)", "- release (manual) (.jazz/steering/release.md)", "first time you read or change a matching file"} {
		if !strings.Contains(sys, s) {
			t.Errorf("trusted prompt misses %q", s)
		}
	}
	for _, s := range []string{"AUTO-API-BODY", "MANUAL-RELEASE-BODY", "GO-FILEMATCH-BODY", "TS-CURSOR-BODY"} {
		if strings.Contains(sys, s) {
			t.Errorf("only always rules go into the prompt, found %q", s)
		}
	}
	if sys != BuildSystemPrompt(root, nil) {
		t.Error("system prompt must be byte-stable")
	}

	// a changed rule needs trust again
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "style.md"), "---\ninclusion: always\n---\nCHANGED")
	if strings.Contains(BuildSystemPrompt(root, nil), "CHANGED") {
		t.Error("changed steering must not load before it is trusted again")
	}
	if _, kinds := pfTrustSummary(root); strings.Join(kinds, ",") != "steering" {
		t.Errorf("changed steering must ask again: %v", kinds)
	}
}

func TestSteeringProjectOverridesGlobal(t *testing.T) {
	home := pfTestHome(t)
	root := t.TempDir()
	acSteerWrite(t, filepath.Join(home, "steering", "style.md"), "GLOBAL-STYLE")
	acSteerWrite(t, filepath.Join(root, ".jazz", "steering", "style.md"), "PROJECT-STYLE")
	if err := Trust(root, "steering", acProjectSteeringBlob(root)); err != nil {
		t.Fatal(err)
	}
	sys := BuildSystemPrompt(root, nil)
	if strings.Contains(sys, "GLOBAL-STYLE") || !strings.Contains(sys, "PROJECT-STYLE") {
		t.Error("a project rule replaces the global rule with the same name")
	}
}

func TestSteeringSkipsSymlinksAndBigFiles(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "id_rsa")
	acSteerWrite(t, secret, "SECRET-KEY-MATERIAL")
	dir := filepath.Join(root, ".jazz", "steering")
	acSteerWrite(t, filepath.Join(dir, "ok.md"), "OK-BODY")
	if err := os.Symlink(secret, filepath.Join(dir, "key.md")); err != nil {
		t.Skip("no symlinks:", err)
	}
	acSteerWrite(t, filepath.Join(dir, "huge.md"), strings.Repeat("x", acSteerFileMax+1))
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(root, ".cursor")); err != nil {
		t.Fatal(err)
	}
	if blob := string(acProjectSteeringBlob(root)); strings.Contains(blob, "SECRET") || strings.Contains(blob, "huge") || !strings.Contains(blob, "OK-BODY") {
		t.Errorf("blob: %q", blob)
	}
	Trust(root, "steering", acProjectSteeringBlob(root))
	if sys := BuildSystemPrompt(root, nil); strings.Contains(sys, "SECRET") || !strings.Contains(sys, "OK-BODY") {
		t.Error("symlinked rules must be skipped")
	}
}

func TestAgentSteeringFileMatchOnce(t *testing.T) {
	read := func(id, p string) fakeCall {
		return fakeCall{ID: id, Name: "read", Args: jsonArgs(map[string]any{"path": p})}
	}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{read("r1", "notes.txt"), read("r2", "missing.go"), read("r3", "pkg/a.go"), read("r4", "pkg/a.go")}},
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "web/x.md", "content": "# x\n"})}}},
		fakeResp{Text: "ok"},
	), nil)
	acSteerProject(t, h.root)
	acSteerWrite(t, filepath.Join(h.root, "notes.txt"), "notes\n")
	acSteerWrite(t, filepath.Join(h.root, "pkg", "a.go"), "package a\n")
	if err := Trust(h.root, "steering", acProjectSteeringBlob(h.root)); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 3 requests, got %d", len(reqs))
	}
	if reqs[0].system() != reqs[2].system() || strings.Contains(reqs[0].system(), "GO-FILEMATCH-BODY") {
		t.Error("fileMatch rules must never touch the system prompt")
	}
	res := reqs[2].toolResults()
	if strings.Contains(res["r1"], "Steering rule") {
		t.Errorf("non-matching file got a rule: %q", res["r1"])
	}
	if strings.Contains(res["r2"], "GO-FILEMATCH-BODY") {
		t.Errorf("a failed read must not inject: %q", res["r2"])
	}
	if !strings.Contains(res["r3"], `<system-reminder>Steering rule ".jazz/steering/go.md" (applies to files matching **/*.go):`) || !strings.Contains(res["r3"], "GO-FILEMATCH-BODY") {
		t.Errorf("first matching read must carry the rule: %q", res["r3"])
	}
	if strings.Contains(res["r4"], "GO-FILEMATCH-BODY") {
		t.Errorf("rule must be injected once per session: %q", res["r4"])
	}
	if !strings.Contains(res["w1"], "TS-CURSOR-BODY") || strings.Contains(res["w1"], "GO-FILEMATCH-BODY") {
		t.Errorf("write of a .md file must carry the cursor rule only: %q", res["w1"])
	}
	if !anyContains(h.ui.notices, "regra de steering aplicada: go") {
		t.Errorf("the user should see which rule was applied: %v", h.ui.notices)
	}

	// once the injection leaves the history (rewind/compaction), it comes back
	tc := ToolCall{ID: "x", Name: "read", Args: jsonArgs(map[string]any{"path": "pkg/a.go"})}
	if n := h.ag.acSteeringNote(tc, ToolOutput{Text: "ok"}); n != "" {
		t.Errorf("already injected: %q", n)
	}
	saved := h.ag.Msgs
	h.ag.Msgs = h.ag.Msgs[:1]
	if n := h.ag.acSteeringNote(tc, ToolOutput{Text: "ok"}); !strings.Contains(n, "GO-FILEMATCH-BODY") {
		t.Errorf("rule must be injected again after it left the history: %q", n)
	}
	h.ag.Msgs = saved
}

func TestAgentSteeringUntrustedNotInjected(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: jsonArgs(map[string]any{"path": "a.go"})}}},
		fakeResp{Text: "ok"},
	), nil)
	acSteerProject(t, h.root)
	acSteerWrite(t, filepath.Join(h.root, "a.go"), "package a\n")
	if err := h.run(t, "go"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if r := reqs[len(reqs)-1].toolResults()["r1"]; strings.Contains(r, "GO-FILEMATCH-BODY") {
		t.Errorf("untrusted project rules must not be injected: %q", r)
	}
}
