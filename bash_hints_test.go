package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBhPipedTest(t *testing.T) {
	yes := []string{"go test ./... | tail -30", "cd x && python3 -m unittest 2>&1 | tail -5", "pytest -q | grep -v warn"}
	no := []string{"go test ./...", "go test ./... || echo falhou", "set -o pipefail; go test ./... | tail", "ls | wc -l", "go test ./... 2>&1 | tee log | tail; echo"}
	for _, c := range yes {
		if !bhPipedTest(c) {
			t.Errorf("piped test not detected: %q", c)
		}
	}
	for _, c := range no[:4] {
		if bhPipedTest(c) {
			t.Errorf("false positive: %q", c)
		}
	}
}

func TestBhExitHint(t *testing.T) {
	cases := []struct {
		cmd  string
		exit int
		out  string
		want string
	}{
		{"go test ./... | tail -3", 0, "--- FAIL: TestX\nFAIL", "shows test FAILURES"},
		{"go test ./... | tail -3", 0, "ok  pkg 0.1s", "status is unknown"},
		{"grep -rn foo .", 1, "", "no match, not an error"},
		{"diff a b", 1, "< x", "files differ"},
		{"make -j4", 137, "Killed", "out of memory"},
		{"python x.py", 127, "bash: python: command not found", "use python3"},
		{"frobnicate", 127, "frobnicate: command not found", "command not found"},
		{"python3 -m notas list", 1, "/usr/bin/python3: No module named notas", "module notas is not installed"},
		{"go test ./...", 0, "ok", ""},
		{"grep foo x | wc -l", 1, "", ""},
	}
	for _, c := range cases {
		got := bhExitHint(c.cmd, c.exit, c.out)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("bhExitHint(%q, %d) = %q, want %q", c.cmd, c.exit, got, c.want)
		}
	}
}

func TestAcAnnouncesAction(t *testing.T) {
	yes := []string{"Entendi o problema. Agora vou rodar os testes.", "Vou verificar o arquivo de config:", "Let me run the tests now.", "Found it. I'll fix the parser next."}
	no := []string{"Pronto: corrigi o parser e os testes passam.", "Quer que eu rode os testes?", "Vou te explicar: o parser lê a linha e depois vou buscar o token, por isso o bug aparece na linha 3 quando o arquivo tem BOM e o leitor ignora. Tudo corrigido e testado.", ""}
	for _, s := range yes {
		if !acAnnouncesAction(s) {
			t.Errorf("announcement not detected: %q", s)
		}
	}
	for _, s := range no {
		if acAnnouncesAction(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

// Unpinned: the model goes empty through the resends; the nudge goes to the
// next candidate instead of ending the turn with an error.
func TestAgentEmptyMovesToNextCandidateUnpinned(t *testing.T) {
	defer func(w time.Duration) { acEmptyWait = w }(acEmptyWait)
	acEmptyWait = time.Millisecond
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{}
		}
		return fakeResp{Text: "from m2"}
	}, func(c *Config) {
		c.Providers["fake2"] = &ProviderConf{Type: "openai", BaseURL: c.Providers["fake"].BaseURL, NoKey: true}
		c.Fallback = []string{"fake2/m2"}
	})
	if h.ag.acPin("main") != "" {
		t.Skip("harness pins the model")
	}
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "from m2" {
		t.Errorf("final = %+v", lm)
	}
}

func TestEnvBlockTestCommand(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b := envBlock(d); !strings.Contains(b, "test command (detected): go test ./...") {
		t.Errorf("env block misses the test command:\n%s", b)
	}
	if b := envBlock(t.TempDir()); strings.Contains(b, "test command") {
		t.Errorf("no project, no test command line")
	}
}

// A change request that only read files and then went silent is not "done".
func TestAgentEmptyMidChangeIsError(t *testing.T) {
	defer func(w time.Duration) { acEmptyWait = w }(acEmptyWait)
	acEmptyWait = time.Millisecond
	rd := fakeCall{ID: "r1", Name: "bash", Args: `{"command":"ls"}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{rd}}, fakeResp{}, fakeResp{}, fakeResp{}, fakeResp{}, fakeResp{}, fakeResp{Text: "never reached"}), nil)
	err := h.run(t, "Adicione uma função dobro(x) em calc.py que devolve 2*x, com teste.")
	if err == nil || !strings.Contains(err.Error(), "parou no meio") {
		t.Fatalf("want the honest mid-change error, got %v", err)
	}
}
