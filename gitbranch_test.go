package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// gitBranch must print what `git branch --show-current` prints: normal
// branch, slash in the name, detached HEAD, and a linked worktree.
func TestGitBranchMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git ausente")
	}
	dir := t.TempDir()
	run := func(d string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(dir, "init", "-q", "-b", "main")
	run(dir, "commit", "-q", "--allow-empty", "-m", "a")
	check := func(d string) {
		t.Helper()
		want := gitOut(d, "branch", "--show-current")
		if got := gitBranch(d); got != want {
			t.Errorf("%s: gitBranch=%q, git=%q", d, got, want)
		}
	}
	check(dir)
	run(dir, "checkout", "-q", "-b", "feature/x-y")
	check(dir)
	run(dir, "checkout", "-q", "--detach")
	check(dir)
	wt := filepath.Join(t.TempDir(), "wt")
	run(dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	check(wt)
	if gitBranch(wt) != "wt-branch" {
		t.Errorf("worktree: %q", gitBranch(wt))
	}
	if got := gitBranch(filepath.Join(t.TempDir(), "nogit")); got != "" {
		t.Errorf("sem repo: %q", got)
	}
}
