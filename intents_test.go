//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fpGoRepo(t *testing.T) string {
	t.Helper()
	scTestEnv(t)
	root := t.TempDir()
	scWrite(t, root, "go.mod", "module x\n")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFastPathMatchPositive(t *testing.T) {
	root := fpGoRepo(t)
	p := fpDetect(root)
	set := fpIntents(root)
	cases := []struct{ in, intent, cmd string }{
		{"rode os testes", "test", "go test ./..."},
		{"Rode os testes!", "test", "go test ./..."},
		{"por favor, roda os testes", "test", "go test ./..."},
		{"rode os testes por favor", "test", "go test ./..."},
		{"run the tests", "test", "go test ./..."},
		{"run tests again", "test", "go test ./..."},
		{"execute os testes de novo", "test", "go test ./..."},
		{"testa tudo", "test", "go test ./..."},
		{"compila", "build", "go build ./..."},
		{"faz o build", "build", "go build ./..."},
		{"rode o lint", "lint", "go vet ./..."},
		{"checa a formatação", "fmt", "gofmt -l ."},
		{"git status", "git_status", "git status --short --branch"},
		{"status do git?", "git_status", "git status --short --branch"},
		{"o que mudou?", "diff", "git status --short && git diff --stat && git diff"},
		{"O que mudou", "diff", "git status --short && git diff --stat && git diff"},
		{"what changed?", "diff", "git status --short && git diff --stat && git diff"},
		{"mostra o diff", "diff", "git status --short && git diff --stat && git diff"},
		{"últimos commits", "log", "git log --oneline -n 15"},
		{"onde está a função ParseArgs?", "where", "rg -n --no-heading -S -w -- ParseArgs . | head -n 80"},
		{"onde fica parseArgs", "where", "rg -n --no-heading -S -w -- parseArgs . | head -n 80"},
		{"where is main()", "where", "rg -n --no-heading -S -w -- main . | head -n 80"},
		{"procura parseArgs", "search", "rg -n --no-heading -S -- parseArgs . | head -n 80"},
		{"find MaxSteps", "search", "rg -n --no-heading -S -- MaxSteps . | head -n 80"},
		{"busca acStickyTTL", "search", "rg -n --no-heading -S -- acStickyTTL . | head -n 80"},
	}
	for _, c := range cases {
		m, ok := set.Match(c.in, p, true)
		if !ok || m.Intent != c.intent || m.Cmd != c.cmd {
			t.Errorf("%q -> %+v %v, want %s: %s", c.in, m, ok, c.intent, c.cmd)
		}
	}
	// grep when rg is missing
	if m, ok := set.Match("onde está a função ParseArgs", p, false); !ok || !strings.HasPrefix(m.Cmd, "grep -rnI") || !strings.Contains(m.Cmd, "-w -- ParseArgs .") {
		t.Errorf("grep fallback: %+v", m)
	}
}

func TestFastPathMatchNegative(t *testing.T) {
	root := fpGoRepo(t)
	p := fpDetect(root)
	set := fpIntents(root)
	for _, in := range []string{
		"rode os testes e conserte o que falhar",
		"run the tests and fix the failures",
		"os testes estão falhando",
		"por que os testes falham?",
		"o que mudou no arquivo main.go",
		"explique o diff",
		"test",
		"teste",
		"testes",
		"status do pedido 123",
		"procura o bug",
		"find the bug",
		"onde está o erro?",
		"where is the problem",
		"onde está o arquivo de config",
		"compila e roda",
		"rode os testes\ndepois o lint",
		"/help",
		"!ls",
		"rode os testes @main.go",
		"git status; rm -rf /",
		"onde está Foo`rm`",
		strings.Repeat("rode os testes ", 20),
		"",
	} {
		if m, ok := set.Match(in, p, true); ok {
			t.Errorf("%q must not fire, got %+v", in, m)
		}
	}
	// no project: test/build have no command; no repository: git intents do not apply
	empty := t.TempDir()
	ep := fpDetect(empty)
	for _, in := range []string{"rode os testes", "compila", "git status", "o que mudou"} {
		if m, ok := fpIntents(empty).Match(in, ep, true); ok && !ep.Git {
			t.Errorf("%q in an empty non-repo dir fired: %+v", in, m)
		}
	}
}

func TestFastPathAmbiguousAndOverrides(t *testing.T) {
	root := fpGoRepo(t)
	p := fpDetect(root)
	// two intents matching the same text = ambiguous = no fast path
	scWrite(t, configDir(), "intents.json", `{"intents":[{"name":"test2","cmd":"make check","patterns":["rode os testes"]}]}`)
	fpCacheMu.Lock()
	delete(fpCache, root)
	fpCacheMu.Unlock()
	if m, ok := fpIntents(root).Match("rode os testes", p, true); ok {
		t.Errorf("ambiguous match fired: %+v", m)
	}
	// user override replaces by name; disabled removes
	scWrite(t, configDir(), "intents.json", `{"intents":[{"name":"test","cmd":"go test -short ./...","patterns":["rode os testes"]},{"name":"diff","disabled":true}]}`)
	fpCacheMu.Lock()
	delete(fpCache, root)
	fpCacheMu.Unlock()
	set := fpIntents(root)
	if m, ok := set.Match("rode os testes", p, true); !ok || m.Cmd != "go test -short ./..." {
		t.Errorf("user override: %+v %v", m, ok)
	}
	if _, ok := set.Match("o que mudou", p, true); ok {
		t.Error("disabled intent still fires")
	}
	// project intents need trust (kind "scripts")
	scWrite(t, root, ".jazz/intents.json", `{"intents":[{"name":"deploy","cmd":"./deploy.sh","patterns":["deploy"]}]}`)
	fpCacheMu.Lock()
	delete(fpCache, root)
	fpCacheMu.Unlock()
	if _, ok := fpIntents(root).Match("deploy", p, true); ok {
		t.Error("untrusted project intent fired")
	}
	if err := Trust(root, "scripts", scProjectBlob(root)); err != nil {
		t.Fatal(err)
	}
	fpCacheMu.Lock()
	delete(fpCache, root)
	fpCacheMu.Unlock()
	if m, ok := fpIntents(root).Match("deploy", p, true); !ok || m.Cmd != "./deploy.sh" {
		t.Errorf("trusted project intent: %+v %v", m, ok)
	}
	// a broken pattern is skipped, not fatal
	scWrite(t, configDir(), "intents.json", `{"intents":[{"name":"bad","cmd":"x","patterns":["(unclosed"]}]}`)
	if s := fpBuild(root); len(s.warns) == 0 {
		t.Error("bad regex should be reported")
	}
}

func TestFastPathNormalize(t *testing.T) {
	cases := map[string]string{
		"  Rode   os testes?!  ":        "Rode os testes",
		"por favor, rode os testes":     "rode os testes",
		"jazz: git status":              "git status",
		"pode rodar os testes pra mim?": "rodar os testes",
		"checa a formatação…":           "checa a formatacao",
		"o que mudou, por favor?":       "o que mudou",
		"please run the tests, thanks":  "run the tests, thanks",
		"rode os testes\nagora":         "",
		"/model x":                      "",
		strings.Repeat("a", 130):        "",
	}
	for in, want := range cases {
		if got := fpNormalize(in); got != want {
			t.Errorf("fpNormalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFastPathTestCommandTarget(t *testing.T) {
	gop := fpProject{Test: "go test ./..."}
	cases := []struct {
		p      fpProject
		target string
		want   string
	}{
		{gop, "", "go test ./..."},
		{gop, "internal/x", "go test ./internal/x"},
		{gop, "./pkg/...", "go test ./pkg/..."},
		{gop, "TestFoo", "go test -run TestFoo ./..."},
		{gop, "Test Foo'x", `go test -run 'Test Foo'\''x' ./...`},
		{fpProject{Test: "python3 -m pytest"}, "tests/test_a.py::test_b", "python3 -m pytest tests/test_a.py::test_b"},
		{fpProject{Test: "pnpm test"}, "src/a.test.ts", "pnpm test -- src/a.test.ts"},
		{fpProject{Test: "make test"}, "x", "make test"},
		{fpProject{}, "x", ""},
	}
	for _, c := range cases {
		if got := fpTestCommand(c.p, c.target); got != c.want {
			t.Errorf("fpTestCommand(%q, %q) = %q, want %q", c.p.Test, c.target, got, c.want)
		}
	}
}

// fpDetect (Go, used by fast paths and run_tests) must agree with the
// detect_project script the model sees.
func TestFastPathDetectMatchesScript(t *testing.T) {
	tb, _ := scTestBox(t)
	fixtures := map[string]map[string]string{
		"go":       {"go.mod": "module x\n"},
		"npm":      {"package.json": `{"scripts":{"test":"jest","build":"tsc","lint":"eslint ."}}`},
		"pnpm":     {"package.json": `{"scripts":{"test":"vitest","format":"prettier -w ."}}`, "pnpm-lock.yaml": ""},
		"npm-none": {"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`},
		"pytest":   {"pyproject.toml": "[tool.pytest.ini_options]\n[tool.ruff]\n"},
		"unittest": {"setup.py": "from setuptools import setup\n"},
		"cargo":    {"Cargo.toml": "[package]\nname='x'\n"},
		"make":     {"Makefile": "test:\n\techo ok\nbuild: dep\n\techo b\nVAR := 1\n"},
		"go+make":  {"go.mod": "module x\n", "Makefile": "test:\n\tgo test ./...\n"},
		"multi":    {"go.mod": "module x\n", "package.json": `{"scripts":{"test":"jest"}}`, "Makefile": "test:\n\tmake all\n"},
		"empty":    {},
	}
	for name, files := range fixtures {
		dir := filepath.Join(tb.Root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, c := range files {
			scWrite(t, dir, f, c)
		}
		p := fpDetect(dir)
		v, _ := scCall(t, tb, "detect_project", map[string]any{"path": name})
		d := scDataOf(v)
		for k, got := range map[string]string{"test": p.Test, "build": p.Build, "lint": p.Lint, "fmt": p.Fmt} {
			if d[k] != got {
				t.Errorf("%s/%s: Go %q, script %q", name, k, got, d[k])
			}
		}
		if name == "pnpm" && p.Test != "pnpm test" || name == "npm-none" && p.Test != "" || name == "make" && (p.Test != "make test" || p.Build != "make build") ||
			name == "multi" && p.Test != "make test" || name == "go+make" && p.Test != "go test ./..." || name == "pytest" && (p.Test != "python3 -m pytest" || p.Lint != "ruff check .") {
			t.Errorf("%s: unexpected detection %+v", name, p)
		}
	}
}

func TestFastPathShQuote(t *testing.T) {
	for in, want := range map[string]string{"Foo": "Foo", "a.b/c": "a.b/c", "a b": "'a b'", "it's": `'it'\''s'`, "$(x)": "'$(x)'"} {
		if got := fpShQuote(in); got != want {
			t.Errorf("fpShQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// The fast path runs through permit(): a deny rule wins, and plan mode only
// allows read-only commands.
func TestFastPathPermitCommand(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	if ok, why := h.ag.PermitCommand(context.Background(), "git status --short --branch"); !ok {
		t.Errorf("git status denied: %s", why)
	}
	h.ag.Perms = &PermSet{root: h.root, rules: []PermRule{{Kind: "deny", Rule: "Bash(go test:*)"}}}
	if ok, why := h.ag.PermitCommand(context.Background(), "go test ./..."); ok || !strings.Contains(why, "go test") {
		t.Errorf("deny rule ignored: %v %s", ok, why)
	}
	h.ag.Perms = nil
	h.cfg.ModeOverride = "plan"
	if ok, _ := h.ag.PermitCommand(context.Background(), "go build ./..."); ok {
		t.Error("plan mode ran a build")
	}
	if ok, _ := h.ag.PermitCommand(context.Background(), "git diff --stat"); !ok {
		t.Error("plan mode refused a read-only command")
	}
}

// Headless --fast-path answers "git status" with no model request at all.
func TestFastPathHeadlessNoModelCall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t, seq(fakeResp{Text: "model was called"}), nil)
	scGit(t, h.root, "init", "-q")
	scWrite(t, h.root, "a.txt", "x\n")
	ui := &HeadlessUI{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, _ := olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "git status", pfOpts{fastPath: true})
	if res == nil || res.Status != StopDone || !strings.Contains(res.Text, "?? a.txt") {
		t.Fatalf("fast path result: %+v", res)
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Errorf("%d model requests for a fast path", n)
	}
	// without --fast-path the fast path does not fire (the offline brain's
	// level 0 would answer "git status" itself: switched off here, brain_test.go covers it)
	t.Setenv("JAZZ_BRAIN", "off")
	if res, p := olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "git status", pfOpts{}); res != nil || p != "git status" {
		t.Errorf("fast path fired without the flag: %+v", res)
	}
	// a failing fast path hands its output to the model
	if _, err := exec.LookPath("make"); err != nil {
		return
	}
	scWrite(t, h.root, "Makefile", "test:\n\t@echo boom; exit 2\n")
	res, p := olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "rode os testes", pfOpts{fastPath: true})
	if res != nil || !strings.Contains(p, "failed (exit 2)") || !strings.Contains(p, "boom") || !strings.HasPrefix(p, "rode os testes") {
		t.Errorf("failed fast path should extend the prompt: %+v %q", res, p)
	}
	// offline: the failure ends the run (no model to hand it to)
	res, _ = olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "rode os testes", pfOpts{offline: true})
	if res == nil || res.Status != StopError || !strings.Contains(res.Text, "boom") {
		t.Errorf("offline failed fast path: %+v", res)
	}
}
