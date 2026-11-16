package main

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func factCall(t *testing.T, h *harness, intent string, args map[string]any) (factResult, ToolOutput) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"intent": intent, "args": args})
	out := h.ag.runFact(context.Background(), ToolCall{ID: "f1", Name: factToolName, Args: string(b)})
	var r factResult
	_ = json.Unmarshal([]byte(out.Text), &r)
	return r, out
}

// T3. The brain's methods as a tool: read-only, closed list, redacted.
func TestFactTool(t *testing.T) {
	scNeedGit(t)
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	scGit(t, h.root, "init", "-q")
	scWrite(t, h.root, "a.txt", "x\n")
	r, out := factCall(t, h, "git_status", nil)
	if !r.OK || r.Commands == 0 || r.Source != "offline-brain" || !strings.Contains(r.Text, "a.txt") {
		t.Fatalf("git_status: %+v\n%s", r, out.Text)
	}
	for _, bad := range []string{"kill_process", "config_get", "dotenv_check", "env_status", "env_get", "open_file", "host_status", "list_hosts", "rm", ""} {
		if _, out := factCall(t, h, bad, nil); !out.IsErr || !strings.Contains(out.Text, "intent não permitido") {
			t.Errorf("%q must be refused: %s", bad, out.Text)
		}
	}
	// a secret in the output never reaches the model
	scWrite(t, h.root, "notes.txt", "chave sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA-BBBB\n")
	_, out = factCall(t, h, "text_grep", map[string]any{"pattern": "chave", "file": "notes.txt"})
	if strings.Contains(out.Text, "AAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Errorf("secret leaked: %s", out.Text)
	}
}

// net_status as a tool measures (or says it did not) — never "offline"
// from the light profile.
func TestFactNetStatusMeasures(t *testing.T) {
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	defer epSetForTest(nil)()
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	r, out := factCall(t, h, "net_status", nil)
	if strings.Contains(r.Text, "offline") || !strings.Contains(r.Text, "não testei a rede") {
		t.Errorf("net_status: %+v\n%s", r, out.Text)
	}
}

// The model sees the tool and its result comes back as a tool message.
func TestFactToolEndToEnd(t *testing.T) {
	scNeedGit(t)
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "f1", Name: "fact", Args: `{"intent":"git_status"}`}}},
		fakeResp{Text: "limpo"},
	), nil)
	scGit(t, h.root, "init", "-q")
	if _, err := h.ag.Run(context.Background(), "como ta o repo?"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	found := false
	for _, tl := range reqs[0].Tools {
		if strings.Contains(jsonArgs(tl), `"fact"`) {
			found = true
		}
	}
	if !found {
		t.Error("fact not offered to the model")
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if c, _ := last["content"].(string); last["role"] != "tool" || !strings.Contains(c, `"source":"offline-brain"`) {
		t.Errorf("tool result: %v", last)
	}
}

// fact's methods read files in-process or through a script — not through
// the read tool's credential gate. With the project at $HOME (or a .env in
// the project) a prompt-injected fact{text_grep, .ssh/id_ed25519} sent the
// private key body to the provider: credential paths are refused outright.
func TestFactRefusesSecretFiles(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	t.Setenv("HOME", h.root)
	if runtime.GOOS == "windows" {
		// os.UserHomeDir reads %USERPROFILE% on Windows, not $HOME: without
		// this the credential guard looks for .netrc under the real profile.
		t.Setenv("USERPROFILE", h.root)
	}
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----\n"
	scWrite(t, h.root, ".ssh/id_ed25519", key)
	scWrite(t, h.root, ".netrc", "machine x login u password hunter2supersecret\n")
	scWrite(t, h.root, "app/.env", "DB_PASS=hunter2supersecret\n")
	scWrite(t, h.root, "README.md", "# ok\n")
	for _, c := range []struct {
		in   string
		args map[string]any
	}{
		{"text_grep", map[string]any{"pattern": "A", "file": ".ssh/id_ed25519", "context": 10}},
		{"text_grep", map[string]any{"pattern": "password", "file": ".netrc"}},
		{"text_grep", map[string]any{"pattern": "PASS", "path": "app/.env"}},
		{"wordcount_stats", map[string]any{"target": "~/.ssh/id_ed25519"}},
		{"md_outline", map[string]any{"file": "README.md, .netrc"}},
	} {
		_, out := factCall(t, h, c.in, c.args)
		if !out.IsErr || strings.Contains(out.Text, "b3BlbnNzaC1rZXkt") || strings.Contains(out.Text, "hunter2") || !strings.Contains(out.Text, "credencial") {
			t.Errorf("%s %v must be refused: %s", c.in, c.args, out.Text)
		}
	}
	// an ordinary file still works
	if r, out := factCall(t, h, "text_grep", map[string]any{"pattern": "ok", "file": "README.md"}); !r.OK {
		t.Errorf("README.md: %s", out.Text)
	}
}
