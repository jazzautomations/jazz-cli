package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// brFixture is a small Go project with a git marker.
func brFixture(t *testing.T, extra ...string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":              "module x\n\ngo 1.22\n",
		"parser.go":           "package x\n\nfunc ParseArgs(a []string) []string { return a }\n",
		"parser_test.go":      "package x\n\nimport \"testing\"\n\nfunc TestParseArgs(t *testing.T) {\n\tif len(ParseArgs([]string{\"a\"})) != 1 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n",
		"lexer.go":            "package x\n\nfunc Tokenize(s string) []string { return nil }\n",
		"internal/db/conn.go": "package db\n\nfunc Open() error { return nil }\n",
		"README.md":           "# x\n",
	}
	for i := 0; i+1 < len(extra); i += 2 {
		files[extra[i]] = extra[i+1]
	}
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

type brRec struct {
	cmds   []string
	events []tkEvent
	asked  []string
}

// brTestEnv: a fake profile, scripted command outputs and answers.
func brTestEnv(t *testing.T, root string, outs map[string]brFakeOut, answer int) (*brEnv, *brRec) {
	t.Helper()
	rec := &brRec{}
	p := epFakeProfile()
	restore := epSetForTest(p)
	t.Cleanup(restore)
	niMu.Lock()
	delete(niCache, root)
	niMu.Unlock()
	env := &brEnv{Root: root, Profile: p,
		Permit: func(ctx context.Context, cmd string) (bool, string) { return true, "" },
		Sink:   func(e tkEvent) { rec.events = append(rec.events, e) },
		Ask: func(q string, opts []string) int {
			rec.asked = append(rec.asked, q+" | "+strings.Join(opts, " | "))
			return answer
		},
		Run: func(ctx context.Context, cmd string) (string, int, bool) {
			rec.cmds = append(rec.cmds, cmd)
			for k, v := range outs {
				if strings.HasPrefix(cmd, k) {
					return v.out, v.exit, false
				}
			}
			return "sh: " + strings.Fields(cmd)[0] + ": not found", 127, false
		}}
	return env, rec
}

type brFakeOut struct {
	out  string
	exit int
}

func brCheckEvents(t *testing.T, ev []tkEvent) {
	t.Helper()
	if len(ev) == 0 || ev[0].To != tkNew {
		t.Fatalf("first event must be new: %+v", ev)
	}
	for i := 1; i < len(ev); i++ {
		if !tkCanMove(ev[i].From, ev[i].To) {
			t.Errorf("illegal transition persisted: %s → %s", ev[i].From, ev[i].To)
		}
		if i > 1 && ev[i].From != ev[i-1].To {
			t.Errorf("broken chain at %d: %s after %s", i, ev[i].From, ev[i-1].To)
		}
	}
}

// The vertical slice: an imprecise name → the file → evidence.
func TestBrainOpenFileTypo(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "abre o parsr.go")
	if !r.Handled || r.Status != tkSucceeded {
		t.Fatalf("status %s handled %v:\n%s", r.Status, r.Handled, r.Text)
	}
	if !strings.Contains(r.Text, "parser.go ·") || !strings.Contains(r.Text, "ParseArgs") {
		t.Errorf("no evidence of parser.go:\n%s", r.Text)
	}
	if len(rec.asked) != 0 || r.Questions != 0 {
		t.Errorf("one clear candidate needs no question: %v", rec.asked)
	}
	if len(rec.cmds) != 0 {
		t.Errorf("opening a file runs no command: %v", rec.cmds)
	}
	brCheckEvents(t, rec.events)
}

func TestBrainOpenTestOfParser(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "abre o teste do parser")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "parser_test.go ·") {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
}

// Ambiguity becomes a numbered question (<= 5); the answer opens the file.
func TestBrainOpenFileAmbiguous(t *testing.T) {
	root := brFixture(t, "api/handler.go", "package api\n", "web/handler.go", "package web\n", "cli/handler.go", "package cli\n")
	env, rec := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "abre o handler.go")
	if r.Status != tkNeedsInput || r.Questions != 1 {
		t.Fatalf("want a question, got %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "1. ") || !strings.Contains(r.Text, "3. ") || strings.Contains(r.Text, "6. ") {
		t.Errorf("numbered list of <= 5:\n%s", r.Text)
	}
	if len(rec.asked) != 1 {
		t.Fatalf("asked %d times", len(rec.asked))
	}
	env2, _ := brTestEnv(t, root, nil, 1)
	r = brRun(context.Background(), env2, "abre o handler.go")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "handler.go ·") {
		t.Fatalf("after the answer: %s\n%s", r.Status, r.Text)
	}
}

// An unknown name is a question with what is close, never an invented file.
func TestBrainOpenFileNotFound(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "abre o zzqxwv.go")
	if r.Status != tkNeedsInput || r.Category != rvNameNotFound {
		t.Fatalf("%s %s:\n%s", r.Status, r.Category, r.Text)
	}
	if !strings.Contains(r.Text, "não achei") {
		t.Errorf("text: %s", r.Text)
	}
}

func TestBrainRunTests(t *testing.T) {
	root := brFixture(t)
	pass := map[string]brFakeOut{"go test": {"ok  \tx\t0.012s\n", 0}}
	env, rec := brTestEnv(t, root, pass, -1)
	r := brRun(context.Background(), env, "roda os testes")
	if r.Status != tkSucceeded || len(rec.cmds) != 1 || rec.cmds[0] != "go test ./..." {
		t.Fatalf("%s %v:\n%s", r.Status, rec.cmds, r.Text)
	}
	fail := map[string]brFakeOut{"go test": {"--- FAIL: TestParseArgs (0.00s)\n    parser_test.go:7: x\nFAIL\nFAIL\tx\t0.010s\nFAIL\n", 1}}
	env, rec = brTestEnv(t, root, fail, -1)
	r = brRun(context.Background(), env, "roda o teste do parser")
	if r.Status != tkFailed || r.Category != rvTestFailed {
		t.Fatalf("failing tests: %s %s:\n%s", r.Status, r.Category, r.Text)
	}
	if !strings.Contains(r.Text, "TestParseArgs") || !strings.Contains(r.Text, ">   7") {
		t.Errorf("failure and its context:\n%s", r.Text)
	}
	if rec.cmds[0] != "go test ." {
		t.Errorf("the parser test narrows to its package: %v", rec.cmds)
	}
	// no test output recognised and exit 0: unverified, never succeeded
	env, _ = brTestEnv(t, root, map[string]brFakeOut{"go test": {"", 0}}, -1)
	if r = brRun(context.Background(), env, "roda os testes"); r.Status != tkUnverified {
		t.Fatalf("empty output: %s", r.Status)
	}
	// the runner is missing
	env, _ = brTestEnv(t, root, nil, -1)
	if r = brRun(context.Background(), env, "roda os testes"); r.Status == tkSucceeded || r.Category != rvCommandMissing {
		t.Fatalf("missing go: %s %s\n%s", r.Status, r.Category, r.Text)
	}
}

func TestBrainGitAndEnvStatus(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, map[string]brFakeOut{"git status": {"## main\n M parser.go\n", 0}}, -1)
	r := brRun(context.Background(), env, "git status")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "M parser.go") {
		t.Fatalf("git: %s\n%s", r.Status, r.Text)
	}
	brCheckEvents(t, rec.events)
	r = brRun(context.Background(), env, "como está o ambiente")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "linux/386") {
		t.Fatalf("env: %s\n%s", r.Status, r.Text)
	}
	noGit := t.TempDir()
	env, _ = brTestEnv(t, noGit, nil, -1)
	if r = brRun(context.Background(), env, "status do git"); r.Status != tkBlocked {
		t.Fatalf("outside git: %s", r.Status)
	}
}

// "e o lab386?" names the machine the harness is on: the answer is "this
// machine", never a ping or an ssh hint (observado numa sessão local).
func TestBrainHostIsSelf(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, -1)
	if env.Profile.Host != "lab386" {
		t.Fatalf("fake profile hostname: %q", env.Profile.Host)
	}
	r := brRun(context.Background(), env, "status do lab386")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "lab386 é ESTA máquina") {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("no command needed: %v", rec.cmds)
	}
	if r = brRun(context.Background(), env, "status do labbox"); r.Status != tkSucceeded || !strings.Contains(r.Text, "host SSH labbox") {
		t.Fatalf("other host unchanged: %s:\n%s", r.Status, r.Text)
	}
}

// "tem o talscle instalado?" → the tailscale binary, answered from the profile.
func TestBrainToolInfoTypo(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "tem o talscle instalado?")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "tailscale instalado: 1.80.2") {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	r = brRun(context.Background(), env, "tenho o zzqqxx instalado?")
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "não está instalado") {
		t.Fatalf("absent tool: %s:\n%s", r.Status, r.Text)
	}
}

// "conecta no labbx" → the SSH host labbox; jazz does not connect by itself, so
// the task ends unverified.
func TestBrainConnectHost(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "conecta no labbx")
	if r.Status != tkUnverified || !strings.Contains(r.Text, "ssh labbox") {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if len(rec.cmds) != 0 {
		t.Fatalf("no contact with the remote machine: %v", rec.cmds)
	}
}

// Not understood / no method: not handled, so the caller asks or uses a model.
func TestBrainFallsThrough(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	for _, s := range []string{"refatora o módulo de pagamento para usar injeção de dependência", "oi tudo bem", "quanto é 2+2?"} {
		if r := brRun(context.Background(), env, s); r.Handled {
			t.Errorf("%q handled as %s", s, r.Intent.Name)
		}
	}
}

// One far candidate ("handler" for HANDOFF.md) is confirmed, never assumed.
func TestBrainFarTypoIsConfirmed(t *testing.T) {
	root := brFixture(t, "HANDOFF.md", "# notas\n")
	env, rec := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "abre o handler")
	if r.Status != tkNeedsInput || len(rec.asked) != 1 || !strings.Contains(rec.asked[0], "HANDOFF.md") {
		t.Fatalf("%s %v:\n%s", r.Status, rec.asked, r.Text)
	}
	env, _ = brTestEnv(t, root, nil, 0)
	if r = brRun(context.Background(), env, "abre o handler"); r.Status != tkSucceeded {
		t.Fatalf("confirmed: %s", r.Status)
	}
}

// Level 0: with a model available, "abre o README.md" and "git status" are
// answered with no request (the Dell baseline of 0.8.1 sent both to the model).
func TestBrainLevel0NoModelCall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t, seq(fakeResp{Text: "model was called"}), nil)
	if out, err := exec.Command("git", "-C", h.root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(h.root, "README.md"), []byte("# proj\n\nlinha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	defer epSetForTest(nil)()
	ui := &HeadlessUI{}
	for _, q := range []string{"abre o README.md", "git status"} {
		res, _ := olHeadless(context.Background(), h.ag, h.ag.tools, ui, h.cfg, q, pfOpts{})
		if res == nil || res.Status != StopDone || res.Model != "offline-brain" {
			t.Fatalf("%q: %+v", q, res)
		}
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Fatalf("%d model requests for level-0 phrases", n)
	}
	// anything else still goes to the model
	if res, p := olHeadless(context.Background(), h.ag, h.ag.tools, ui, h.cfg, "refatora o parser para usar generics", pfOpts{}); res != nil || p != "refatora o parser para usar generics" {
		t.Fatalf("not level 0: %+v", res)
	}
	t.Setenv("JAZZ_BRAIN", "off")
	if res, _ := olHeadless(context.Background(), h.ag, h.ag.tools, ui, h.cfg, "git status", pfOpts{}); res != nil {
		t.Fatal("JAZZ_BRAIN=off must send it to the model")
	}
}

// Entity-first: "talscle status" is the status OF tailscale, not a git error
// in a folder without .git (measured on the Dell).
func TestBrainToolStatusEntityFirst(t *testing.T) {
	noGit := t.TempDir()
	env, rec := brTestEnv(t, noGit, map[string]brFakeOut{"tailscale status": {"100.64.0.1 dell user linux -\n", 0}}, -1)
	for _, s := range []string{"talscle status", "status do tailscale", "tailscale ta rodando?"} {
		rec.cmds = nil
		r := brRun(context.Background(), env, s)
		if r.Intent.Name != "tool_status" || r.Intent.Slots["tool"] == "" {
			t.Fatalf("%q: intent %s slots %v", s, r.Intent.Name, r.Intent.Slots)
		}
		if r.Status != tkSucceeded || !strings.Contains(r.Text, "100.64.0.1") || len(rec.cmds) != 1 || rec.cmds[0] != "tailscale status" {
			t.Fatalf("%q: %s cmds=%v\n%s", s, r.Status, rec.cmds, r.Text)
		}
		if strings.Contains(r.Text, "repositório git") {
			t.Fatalf("%q answered with a git error:\n%s", s, r.Text)
		}
	}
}

// A tool with no safe status command gets its info and says so; an absent
// tool fails with a clear message.
func TestBrainToolStatusNoCommandAndAbsent(t *testing.T) {
	env, rec := brTestEnv(t, t.TempDir(), nil, -1)
	r := brRun(context.Background(), env, "status do python3")
	if r.Intent.Name != "tool_status" || r.Status != tkSucceeded || !strings.Contains(r.Text, "comando de status seguro") || len(rec.cmds) != 0 {
		t.Fatalf("%s %s cmds=%v\n%s", r.Intent.Name, r.Status, rec.cmds, r.Text)
	}
}

func TestBrainToolStatusAbsentBinary(t *testing.T) {
	cx := &brCtx{env: &brEnv{Profile: epFakeProfile()}, task: newTask("x", "tool_status", nil, nil)}
	st := brToolStatus(cx, map[string]string{"name": "", "asked": "zzqqxx"})
	if st.stop != tkBlocked || !strings.Contains(st.why, "não está instalado") || cx.cat != rvCommandMissing {
		t.Fatalf("%+v cat=%s", st, cx.cat)
	}
}

// "status" alone: git repo → git status; no repo → the environment, not a git error.
func TestBrainBareStatus(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, map[string]brFakeOut{"git status": {"## main\n", 0}}, -1)
	if r := brRun(context.Background(), env, "status"); r.Intent.Name != "git_status" || r.Status != tkSucceeded {
		t.Fatalf("repo: %s %s\n%s", r.Intent.Name, r.Status, r.Text)
	}
	env, _ = brTestEnv(t, t.TempDir(), nil, -1)
	r := brRun(context.Background(), env, "status")
	if r.Status != tkSucceeded || strings.Contains(r.Text, "repositório git") || !strings.Contains(r.Text, "linux/386") {
		t.Fatalf("no repo: %s\n%s", r.Status, r.Text)
	}
	if r = brRun(context.Background(), env, "git status"); r.Status != tkBlocked || !strings.Contains(r.Text, "repositório git") {
		t.Fatalf("explicit git outside repo: %s\n%s", r.Status, r.Text)
	}
}

// connect_host declares it needs ssh: without it, a clear message.
func TestBrainConnectHostNeedsSSH(t *testing.T) {
	env, _ := brTestEnv(t, t.TempDir(), nil, -1)
	p := *env.Profile
	p.Toolchains, p.PathBins = nil, nil
	env.Profile = &p
	r := brRun(context.Background(), env, "conecta no labbox")
	if r.Status != tkBlocked || !strings.Contains(r.Text, "ssh não está no PATH") {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
}

// ---------- script:<name> dispatch (methods.go brScript, §5 do design) ----------

// Every capability the embedded methods declare must resolve to code:
// a named Go cap or script:<name> through the Toolbox registry.
func TestBrainMethodCapsResolve(t *testing.T) {
	lib, err := mhLibrary()
	if err != nil {
		t.Fatal(err)
	}
	for intent, ms := range lib {
		for _, m := range ms {
			for _, st := range m.Steps {
				if brCap(st.Cap) == nil {
					t.Errorf("%s (%s): capacidade não resolvida: %s", m.Name, intent, st.Cap)
				}
			}
		}
	}
}

// brScript runs a script with $-substituted args, validates against its
// schema, counts budget, and fails honestly on unknown names/bad args.
func TestBrainScriptDispatch(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	env, _ := brTestEnv(t, brFixture(t), nil, -1)
	cx := &brCtx{ctx: context.Background(), env: env,
		task: newTask("procura ParseArgs", "find", map[string]string{"query": "ParseArgs"}, nil),
		vars: map[string]any{}, attempts: map[rvCat]int{}}
	st := brScript(cx, "find_symbol", map[string]string{"symbol": "$slot.query"})
	if !st.ok {
		t.Fatalf("find_symbol com slot: %s", st.why)
	}
	if v, _ := st.val.(map[string]any); v["ok"] != true {
		t.Fatalf("envelope não confirmou: %v", st.val)
	}
	if cx.task.Budget.UsedCmds != 1 || cx.commands != 1 {
		t.Errorf("orçamento: used=%d commands=%d", cx.task.Budget.UsedCmds, cx.commands)
	}
	if n := len(cx.task.Evidence); n == 0 || !strings.Contains(cx.task.Evidence[n-1].Text, "script:") {
		t.Errorf("evidência de script ausente: %+v", cx.task.Evidence)
	}
	if st = brScript(cx, "nao_existe", nil); st.ok || !strings.Contains(st.why, "desconhecido") {
		t.Fatalf("script inexistente: %+v", st)
	}
	if st = brScript(cx, "find_symbol", map[string]string{}); st.ok || !strings.Contains(st.why, "symbol") {
		t.Fatalf("arg obrigatório ausente: %+v", st)
	}
}

// A script with a non-read effect goes through the permission hook; a
// denial costs no command budget.
func TestBrainScriptPermit(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	deny := &brEnv{Root: t.TempDir(),
		Permit: func(context.Context, string) (bool, string) { return false, "negado" }}
	cx := &brCtx{ctx: context.Background(), env: deny,
		task: newTask("x", "run_tests", nil, nil), vars: map[string]any{}, attempts: map[rvCat]int{}}
	st := brScript(cx, "run_tests", map[string]string{"cmd": "echo hi"})
	if st.ok || !st.obs.Denied || !strings.Contains(st.why, "negada") {
		t.Fatalf("efeito exec devia pedir permissão: %+v", st)
	}
	if cx.task.Budget.UsedCmds != 0 {
		t.Errorf("negado não conta comando: %d", cx.task.Budget.UsedCmds)
	}
	// read effects never ask: find_symbol with a Permit that denies everything
	cx2 := &brCtx{ctx: context.Background(), env: deny,
		task: newTask("x", "find", map[string]string{"query": "x"}, nil),
		vars: map[string]any{}, attempts: map[rvCat]int{}}
	if st := brScript(cx2, "find_symbol", map[string]string{"symbol": "$slot.query"}); st.obs.Denied {
		t.Fatalf("efeito read não deve pedir permissão: %+v", st)
	}
}

// brScriptLines renders nested script data deterministically.
func TestBrainScriptLines(t *testing.T) {
	v := map[string]any{"ok": true, "data": map[string]any{
		"name": "x", "items": []any{"a", "b"}, "n": 3.0,
		"nested": map[string]any{"inner": 1.0}}}
	got := strings.Join(brScriptLines(v, 40), "\n")
	for _, want := range []string{"items (2):", "  a", "name: x", "n: 3", "nested:", "inner: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q em:\n%s", want, got)
		}
	}
}

// git_log via read_cmd: repo runs the allowlisted argv; no repo is an
// honest "blocked", never a fabricated log.
func TestBrainGitLog(t *testing.T) {
	env, rec := brTestEnv(t, brFixture(t), map[string]brFakeOut{"git log": {"abc123 init\n", 0}}, -1)
	r := brRun(context.Background(), env, "últimos commits")
	if !r.Handled || r.Status != tkSucceeded || !strings.Contains(r.Text, "abc123") ||
		len(rec.cmds) != 1 || rec.cmds[0] != "git log --oneline -20" {
		t.Fatalf("%s cmds=%v\n%s", r.Status, rec.cmds, r.Text)
	}
	env, _ = brTestEnv(t, t.TempDir(), nil, -1)
	r = brRun(context.Background(), env, "últimos commits")
	if r.Status != tkBlocked || !strings.Contains(r.Text, "repositório") {
		t.Fatalf("%s\n%s", r.Status, r.Text)
	}
}
