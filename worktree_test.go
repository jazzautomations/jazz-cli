package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// wtTestRepo makes a git repo with one commit on branch main.
func wtTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git ausente")
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("um\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("dois\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "inicial")
	return dir
}

func wtCommitIn(t *testing.T, dir, file, body string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644)
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "mudança " + file}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

func TestWtParseArgs(t *testing.T) {
	cases := []struct {
		in   []string
		out  []string
		on   bool
		name string
	}{
		{[]string{"-p", "oi"}, []string{"-p", "oi"}, false, ""},
		{[]string{"--worktree", "-p", "faz x"}, []string{"-p", "faz x"}, true, ""},
		{[]string{"--worktree", "feat-1", "-p", "faz x"}, []string{"-p", "faz x"}, true, "feat-1"},
		{[]string{"--worktree=abc", "conserta o bug"}, []string{"conserta o bug"}, true, "abc"},
		{[]string{"-p", "--worktree", "conserta o bug"}, []string{"-p", "conserta o bug"}, true, ""},
		{[]string{"--worktree", "doctor"}, []string{"doctor"}, true, ""}, // a subcommand is never a name
	}
	for _, c := range cases {
		out, on, name, err := wtParseArgs(c.in)
		if err != nil || !reflect.DeepEqual(out, c.out) || on != c.on || name != c.name {
			t.Errorf("wtParseArgs(%q) = %q %v %q %v; want %q %v %q", c.in, out, on, name, err, c.out, c.on, c.name)
		}
	}
	if _, _, _, err := wtParseArgs([]string{"--worktree=../x"}); err == nil {
		t.Error("nome inválido aceito")
	}
}

func TestWtCreateListMergeRemove(t *testing.T) {
	repo := wtTestRepo(t)
	var log bytes.Buffer
	root, err := wtStart(filepath.Join(repo, "sub"), "feat", &log)
	t.Cleanup(func() { wtActive, wtActiveRepo = nil, "" })
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".jazz", "worktrees", "feat")
	if root != filepath.Join(wt, "sub") {
		t.Fatalf("root = %s; want the same subdir inside the worktree", root)
	}
	if b, _ := wtGit(wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "jazz/feat" {
		t.Fatalf("branch = %q", b)
	}
	if ex, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); !strings.Contains(string(ex), "/.jazz/worktrees/") {
		t.Fatalf("exclude sem o worktree: %s", ex)
	}
	if st, _ := wtGit(repo, "status", "--porcelain"); st != "" {
		t.Fatalf("o repo principal ficou sujo: %q", st)
	}
	if _, _, err := wtCreate(repo, "feat"); err == nil {
		t.Fatal("nome repetido aceito")
	}

	// work in the worktree: one commit and one uncommitted change
	wtCommitIn(t, wt, "c.txt", "três\n")
	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("um mudado\n"), 0o644)

	var out bytes.Buffer
	wtList(repo, &out)
	if s := out.String(); !strings.Contains(s, "feat") || !strings.Contains(s, "jazz/feat") || !strings.Contains(s, "sujo") || !strings.Contains(s, "1 à frente de main") {
		t.Fatalf("list: %s", s)
	}
	var hint bytes.Buffer
	wtExitHint(&hint)
	if !strings.Contains(hint.String(), "jazz worktree merge feat") {
		t.Fatalf("exit hint: %s", hint.String())
	}

	// rm refuses unmerged work
	if err := wtRemove(repo, "feat", false, &out); err == nil {
		t.Fatal("rm apagou trabalho não mesclado")
	}

	// merge without --yes and a "n" answer: nothing happens
	if err := wtMerge(repo, "feat", false, strings.NewReader("n\n"), &out); err == nil {
		t.Fatal("merge sem confirmação")
	}
	// merge --yes commits the pending change and merges with --no-ff
	out.Reset()
	if err := wtMerge(repo, "feat", true, nil, &out); err != nil {
		t.Fatalf("merge: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "c.txt") {
		t.Fatalf("merge sem diff stat: %s", out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "um mudado\n" {
		t.Fatalf("a.txt no principal = %q", b)
	}
	if p, _ := wtGit(repo, "log", "-1", "--format=%P"); len(strings.Fields(p)) != 2 {
		t.Fatalf("merge não é --no-ff (pais %q)", p)
	}

	// merged: rm works without --force
	out.Reset()
	if err := wtRemove(repo, "feat", false, &out); err != nil {
		t.Fatalf("rm após merge: %v", err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("pasta do worktree ficou")
	}
	if _, err := wtGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/jazz/feat"); err == nil {
		t.Fatal("branch ficou")
	}
}

func TestWtRemoveForceAndCmd(t *testing.T) {
	repo := wtTestRepo(t)
	m, _, err := wtCreate(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.Name, "wt-") {
		t.Fatalf("nome gerado = %q", m.Name)
	}
	wtCommitIn(t, m.Path, "x.txt", "x\n")
	var out bytes.Buffer
	if code := cmdWorktree(repo, []string{"rm", m.Name}, nil, &out); code != 1 {
		t.Fatalf("rm sem --force = %d", code)
	}
	if code := cmdWorktree(repo, []string{"rm", m.Name, "--force"}, nil, &out); code != 0 {
		t.Fatalf("rm --force = %d: %s", code, out.String())
	}
	out.Reset()
	if code := cmdWorktree(repo, []string{"list"}, nil, &out); code != 0 || !strings.Contains(out.String(), "nenhum worktree") {
		t.Fatalf("list depois do rm: %d %s", code, out.String())
	}
	if code := cmdWorktree(repo, []string{"merge"}, nil, &out); code != 2 {
		t.Fatalf("merge sem nome = %d", code)
	}
}

// TestWtHeadlessRoot runs the real binary entry (`-p --worktree`) far enough
// to see the root it picked: with no credentials the run fails, but the
// worktree must exist and the exit hint names it.
func TestWtHeadlessRoot(t *testing.T) {
	repo := wtTestRepo(t)
	if os.Getenv("JAZZ_WT_CHILD") == "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWtHeadlessChild$")
	cmd.Env = append(os.Environ(), "JAZZ_WT_CHILD=1", "JAZZ_WT_REPO="+repo, "JAZZ_OFFLINE=1")
	out, _ := cmd.CombinedOutput()
	s := string(out)
	if !strings.Contains(s, "worktree hl criado em "+filepath.Join(repo, ".jazz", "worktrees", "hl")) {
		t.Fatalf("saída do filho:\n%s", s)
	}
	if !strings.Contains(s, "jazz worktree merge hl") {
		t.Fatalf("sem dica de merge:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(repo, ".jazz", "worktrees", "hl", "a.txt")); err != nil {
		t.Fatal("worktree não criado")
	}
}

// TestWtHeadlessChild is the child of TestWtHeadlessRoot: it runs main().
func TestWtHeadlessChild(t *testing.T) {
	if os.Getenv("JAZZ_WT_CHILD") != "1" {
		t.Skip("só como processo filho")
	}
	os.Args = []string{"jazz", "-C", os.Getenv("JAZZ_WT_REPO"), "-p", "--worktree=hl", "--sandbox", "off", "rode os testes"}
	main()
}
