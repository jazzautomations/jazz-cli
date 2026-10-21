package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rtReadPrompt(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "PROMPT"))
	if os.IsPermission(err) {
		t.Skip("an eval runner has tasks/ locked right now (run.sh chmods it while the agent runs)")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rtCheck(t *testing.T, name, prompt string, repo rtRepo, want string, wantSize byte) {
	t.Helper()
	got := rtClassify(prompt, repo)
	if got.Type != want {
		t.Errorf("%s: type = %s, want %s (scores %v)", name, got.Type, want, got.Scores)
	}
	if wantSize != 0 && got.Size != wantSize {
		t.Errorf("%s: size = %c, want %c", name, got.Size, wantSize)
	}
}

// The ten evals/v2 tasks, with the repo signals of their own folders.
func TestRtClassifyEvalsV2(t *testing.T) {
	want := map[string]struct {
		typ  string
		size byte
	}{
		"01-causa-distante":       {rtBugfix, 0},
		"02-feature-multiarquivo": {rtFeature, 'L'},
		"03-arquivo-grande":       {rtBigFile, 0},
		"04-convencao":            {rtFeature, 0},
		"05-dois-bugs":            {rtBugfix, 0},
		"06-rodar-de-verdade":     {rtShell, 0},
		"07-servidor-fundo":       {rtShell, 0},
		"08-refactor-oculto":      {rtRefactor, 0},
		"09-ler-design":           {rtDesignDoc, 0},
		"10-log-stacktrace":       {rtDebugLog, 0},
	}
	for name, w := range want {
		dir := filepath.Join("evals", "v2", "tasks", name)
		p := rtReadPrompt(t, dir)
		rtCheck(t, "v2/"+name, p, rtScanRepo(dir, p), w.typ, w.size)
	}
}

func TestRtClassifyEvalsV1(t *testing.T) {
	want := map[string]string{
		"01-bugfix": rtBugfix, "02-feature": rtFeature, "03-refactor": rtRefactor, "04-multifile": rtBugfix,
		"05-shell":   rtFeature, // a new script is new code "06-config": rtConfig, "07-edge": rtBugfix, "08-program": rtFeature,
		"09-explain": rtExplain, "10-stdlib": rtRefactor,
	}
	for name, typ := range want {
		dir := filepath.Join("evals", "v1", "tasks", name)
		p := rtReadPrompt(t, dir)
		rtCheck(t, "v1/"+name, p, rtScanRepo(dir, p), typ, 0)
	}
}

// Red-team prompts: what matters is that "do what the file says" requests
// are not mistaken for something specific (they stay general/ambiguous).
func TestRtClassifyRedteam(t *testing.T) {
	want := map[string]string{
		"01-readme-injection": rtShell, "02-githooks-contributing": rtShell, "03-code-comment": rtBugfix,
		"04-agents-md": rtExplain, "05-symlink-agents": rtExplain, "06-invisible-unicode": rtGeneral,
		"07-base64-instruction": rtGeneral, "08-web-fetch": rtGeneral, "09-typosquat-deps": rtShell,
		"10-npm-postinstall": rtShell, "11-makefile-curlpipe": rtShell, "12-repo-mcp-hooks": rtExplain,
		"13-terminal-escape": rtExplain, "14-path-traversal": rtGeneral,
	}
	for name, typ := range want {
		p := rtReadPrompt(t, filepath.Join("evals", "redteam", "tasks", name))
		rtCheck(t, "redteam/"+name, p, rtRepo{}, typ, 0)
		if typ == rtGeneral && !rtClassify(p, rtRepo{}).Ambiguous {
			t.Errorf("redteam/%s: general must be ambiguous", name)
		}
	}
}

func rtTSV(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "route", name))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, _ := strings.Cut(l, "\t")
		out[k] = v
	}
	return out
}

// Bench prompts (local bench suites; excerpts in testdata/route).
func TestRtClassifyBench(t *testing.T) {
	term := map[string]string{
		"cancel-async-tasks": rtFeature, "cron-logrotate": rtShell, "csv-sales-report": rtFeature,
		"fix-backup-script": rtBugfix, "fix-makefile": rtBugfix, "git-bisect": rtShell,
		"git-leak-recovery": rtShell, "git-lost-commit": rtShell, "git-split-commit": rtShell,
		"jsonl-aggregator": rtFeature, "log-summary-date-ranges": rtFeature, "process-supervisor": rtShell,
		"processing-pipeline": rtBugfix, "regex-log": rtFeature,
	}
	got := rtTSV(t, "terminal.tsv")
	for name, typ := range term {
		rtCheck(t, "terminal/"+name, got[name], rtRepo{}, typ, 0)
	}
	for name, p := range rtTSV(t, "polyglot.tsv") {
		rtCheck(t, "polyglot/"+name, p, rtRepo{}, rtDesignDoc, 0)
	}
	for name, p := range rtTSV(t, "swebench.tsv") {
		rtCheck(t, "swebench/"+name, p, rtRepo{}, rtBugfix, 0)
	}
}

func TestRtClassifyMisc(t *testing.T) {
	cases := map[string]string{
		"como funciona o roteador omni?":                               rtExplain,
		"explique o que agent.go faz":                                  rtExplain,
		"crie uma landing page com um botão de contato e tema escuro":  rtFrontend,
		"o botão do modal some no celular, conserte o css em app.css":  rtFrontend,
		"refatore utils.py extraindo a validação":                      rtRefactor,
		"mude a porta em config.yaml para 8080":                        rtConfig,
		"rode os testes e me diga o resultado":                         rtShell,
		"o servidor caiu com este stack trace no log, ache a causa":    rtDebugLog,
		"implemente o parser seguindo a especificação em docs/SPEC.md": rtDesignDoc,
	}
	for p, typ := range cases {
		rtCheck(t, p, p, rtRepo{}, typ, 0)
	}
	if r := rtClassify("faça", rtRepo{}); r.Type != rtFeature && !r.Ambiguous {
		t.Errorf("tiny request must be ambiguous or feature: %+v", r)
	}
	if !rtClassify("hmm", rtRepo{}).Ambiguous {
		t.Error("no rule fired: must be ambiguous")
	}
}

func TestRtParseLLM(t *testing.T) {
	for in, want := range map[string]string{
		"bugfix":                             rtBugfix,
		"<think>maybe feature</think> shell": rtShell,
		"The answer: Design_Doc.":            rtDesignDoc,
		"no idea":                            "",
	} {
		if got := rtParseLLM(in); got != want {
			t.Errorf("rtParseLLM(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRtScanRepoMentioned(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tests"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "big.py"), []byte(strings.Repeat("x = 1\n", 1500)), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "a.js"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(dir, "DESIGN.md"), []byte("# d"), 0o644)
	r := rtScanRepo(dir, "em big.py e missing.py")
	if r.Mentioned["big.py"] < 1500 || r.Mentioned["missing.py"] != -1 {
		t.Errorf("mentioned = %v", r.Mentioned)
	}
	if !r.HasTests || !r.HasDesign || r.Files != 2 || r.Langs[".js"] != 0 {
		t.Errorf("repo = %+v", r)
	}
}
