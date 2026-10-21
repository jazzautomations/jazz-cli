//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// cpNewBox returns a Toolbox with shadow-git checkpoints on, in a temp
// project with its data dir in another temp dir.
func cpNewBox(t *testing.T) (*Toolbox, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tb, root := toolsNewBox(t)
	tb.Checkpoints = true
	return tb, root
}

func cpWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cpBash(t *testing.T, tb *Toolbox, cmd string) {
	t.Helper()
	toolsMustExec(t, tb, "bash", map[string]any{"command": cmd})
}

func cpGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist (err=%v)", p, err)
	}
}

func TestCheckpointUndoBashChanges(t *testing.T) {
	tb, root := cpNewBox(t)
	cpWrite(t, root, "a.txt", "alpha\n")
	cpWrite(t, root, "b.txt", "beta\n")
	cpWrite(t, root, "keep.txt", "keep\n")

	tb.BeginTurn(0)
	if !tb.BashTracked() {
		t.Fatalf("checkpoints should be on: %q", tb.cp.off)
	}
	// sed -i (with a portable backup suffix), a redirect into a new nested
	// file, and an rm: the three ways weak models edit through bash
	cpBash(t, tb, "sed -i.bak 's/alpha/ALPHA/' a.txt && mkdir -p gen/deep && echo made > gen/deep/new.txt && rm b.txt")
	if got := toolsReadFile(t, filepath.Join(root, "a.txt")); got != "ALPHA\n" {
		t.Fatalf("sed did not run: %q", got)
	}

	paths, err := tb.Undo()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "a.txt.bak", "b.txt", filepath.Join("gen", "deep", "new.txt")}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("undo paths = %v, want %v", paths, want)
	}
	if got := toolsReadFile(t, filepath.Join(root, "a.txt")); got != "alpha\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := toolsReadFile(t, filepath.Join(root, "b.txt")); got != "beta\n" {
		t.Errorf("b.txt = %q", got)
	}
	cpGone(t, filepath.Join(root, "a.txt.bak"))
	cpGone(t, filepath.Join(root, "gen", "deep", "new.txt"))
	cpGone(t, filepath.Join(root, "gen")) // emptied directories are pruned too
	if got := toolsReadFile(t, filepath.Join(root, "keep.txt")); got != "keep\n" {
		t.Errorf("untouched file changed: %q", got)
	}

	// the shadow repo lives under the data dir, never in the project
	gd := filepath.Join(dataDir(), "checkpoints", projectSlug(tb.cp.root)+".git")
	if _, err := os.Stat(filepath.Join(gd, "HEAD")); err != nil {
		t.Errorf("shadow repo missing at %s: %v", gd, err)
	}
	cpGone(t, filepath.Join(root, ".git"))

	if paths, err := tb.Undo(); err != nil || paths != nil {
		t.Fatalf("nothing left to undo: %v %v", paths, err)
	}
}

func TestCheckpointRewindAcrossTurns(t *testing.T) {
	tb, root := cpNewBox(t)
	cpWrite(t, root, "f.txt", "v0\n")

	tb.BeginTurn(0)
	cpBash(t, tb, "echo v1 > f.txt")
	tb.BeginTurn(2)
	cpBash(t, tb, "echo v2 > f.txt && echo x > extra.txt")
	toolsMustExec(t, tb, "write", map[string]any{"path": "w.txt", "content": "written\n"})
	tb.BeginTurn(4) // a turn without changes

	paths, err := tb.RewindTo(2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"extra.txt", "f.txt", "w.txt"}) {
		t.Fatalf("rewind paths = %v", paths)
	}
	if got := toolsReadFile(t, filepath.Join(root, "f.txt")); got != "v1\n" {
		t.Fatalf("after rewind to turn 2: %q", got)
	}
	cpGone(t, filepath.Join(root, "extra.txt"))
	cpGone(t, filepath.Join(root, "w.txt"))

	if _, err := tb.RewindTo(0); err != nil {
		t.Fatal(err)
	}
	if got := toolsReadFile(t, filepath.Join(root, "f.txt")); got != "v0\n" {
		t.Fatalf("after rewind to start: %q", got)
	}
}

// write/edit and bash on the same file in one turn: whichever ran first,
// undo must go back to the turn-start content.
func TestCheckpointMixedToolsSameFile(t *testing.T) {
	tb, root := cpNewBox(t)
	cpWrite(t, root, "a.txt", "start-a\n")
	cpWrite(t, root, "b.txt", "start-b\n")

	tb.BeginTurn(0)
	// a: edit first, then bash
	toolsMustExec(t, tb, "read", map[string]any{"path": "a.txt"})
	toolsMustExec(t, tb, "edit", map[string]any{"path": "a.txt", "old_string": "start-a", "new_string": "edited-a"})
	cpBash(t, tb, "echo bashed-a > a.txt")
	// b: bash first, then write (the in-memory snapshot is post-bash)
	cpBash(t, tb, "echo bashed-b > b.txt")
	toolsMustExec(t, tb, "read", map[string]any{"path": "b.txt"})
	toolsMustExec(t, tb, "write", map[string]any{"path": "b.txt", "content": "written-b\n"})

	paths, err := tb.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"a.txt", "b.txt"}) {
		t.Fatalf("paths = %v", paths)
	}
	if got := toolsReadFile(t, filepath.Join(root, "a.txt")); got != "start-a\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := toolsReadFile(t, filepath.Join(root, "b.txt")); got != "start-b\n" {
		t.Errorf("b.txt = %q", got)
	}
}

// Edits the user makes by hand between turns are not the agent's: undoing
// the agent's turn must keep them.
func TestCheckpointKeepsManualEditsBetweenTurns(t *testing.T) {
	tb, root := cpNewBox(t)
	cpWrite(t, root, "agent.txt", "a0\n")
	cpWrite(t, root, "mine.txt", "m0\n")

	tb.BeginTurn(0)
	cpBash(t, tb, "echo a1 > agent.txt")
	cpWrite(t, root, "mine.txt", "m1 by hand\n") // after the turn, before the next
	tb.BeginTurn(2)

	paths, err := tb.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"agent.txt"}) {
		t.Fatalf("paths = %v", paths)
	}
	if got := toolsReadFile(t, filepath.Join(root, "agent.txt")); got != "a0\n" {
		t.Errorf("agent.txt = %q", got)
	}
	if got := toolsReadFile(t, filepath.Join(root, "mine.txt")); got != "m1 by hand\n" {
		t.Errorf("manual edit lost: %q", got)
	}
}

func TestCheckpointRespectsGitignoreAndSizeCap(t *testing.T) {
	tb, root := cpNewBox(t)
	old := cpMaxFileSize
	cpMaxFileSize = 64
	t.Cleanup(func() { cpMaxFileSize = old })
	cpWrite(t, root, ".gitignore", "build/\n*.log\n")
	cpWrite(t, root, "build/out.bin", "o0")
	cpWrite(t, root, "run.log", "l0")
	cpWrite(t, root, "big.dat", strings.Repeat("x", 100))
	cpWrite(t, root, "src.txt", "s0")

	tb.BeginTurn(0)
	cpBash(t, tb, "echo o1 > build/out.bin; echo l1 > run.log; echo small > big.dat; echo s1 > src.txt")
	paths, err := tb.Undo()
	if err != nil {
		t.Fatal(err)
	}
	// big.dat was over the cap at turn start, so its old content is not in
	// the checkpoint; it now fits, so it shows up as added and is removed
	if !reflect.DeepEqual(paths, []string{"big.dat", "src.txt"}) {
		t.Fatalf("paths = %v", paths)
	}
	if got := toolsReadFile(t, filepath.Join(root, "src.txt")); got != "s0" {
		t.Errorf("src.txt = %q", got)
	}
	if got := toolsReadFile(t, filepath.Join(root, "build/out.bin")); got != "o1\n" {
		t.Errorf("ignored file must not be touched: %q", got)
	}
	if got := toolsReadFile(t, filepath.Join(root, "run.log")); got != "l1\n" {
		t.Errorf("ignored file must not be touched: %q", got)
	}

	// a file that grows past the cap is dropped from the index, not stored
	tb.BeginTurn(2)
	cpBash(t, tb, "head -c 200 /dev/zero > grown.txt")
	tree := tb.turns[len(tb.turns)-1].cpLast
	out, err := tb.cp.cmd(nil, nil, "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "grown.txt") || strings.Contains(string(out), "build/") || strings.Contains(string(out), "run.log") {
		t.Errorf("tree has skipped files:\n%s", out)
	}
}

func TestCheckpointReadOnlyBashTakesNoSnapshot(t *testing.T) {
	tb, root := cpNewBox(t)
	cpWrite(t, root, "f.txt", "x")
	tb.BeginTurn(0)
	start := tb.turns[0].cpStart
	cpWrite(t, root, "f.txt", "changed by someone else")
	cpBash(t, tb, "cat f.txt")
	if tb.turns[0].cpLast != start || len(tb.turns[0].bash) != 0 {
		t.Fatalf("read-only command recorded changes: %v", tb.turns[0].bash)
	}
}

func TestCheckpointDisabledWithoutGit(t *testing.T) {
	tb, root := cpNewBox(t)
	old := cpGitBin
	cpGitBin = "jazz-no-such-git-binary"
	t.Cleanup(func() { cpGitBin = old })
	cpWrite(t, root, "f.txt", "v0")

	tb.BeginTurn(0)
	if tb.BashTracked() {
		t.Fatal("checkpoints must be off without git")
	}
	cpBash(t, tb, "echo v1 > f.txt")
	toolsMustExec(t, tb, "write", map[string]any{"path": "w.txt", "content": "w"})
	// fallback: the in-memory snapshots still undo write/edit
	paths, err := tb.Undo()
	if err != nil || !reflect.DeepEqual(paths, []string{"w.txt"}) {
		t.Fatalf("undo = %v %v", paths, err)
	}
	cpGone(t, filepath.Join(root, "w.txt"))
	if got := toolsReadFile(t, filepath.Join(root, "f.txt")); got != "v1\n" {
		t.Errorf("bash change can't be undone without git: %q", got)
	}
}

func TestCheckpointDisabledAboveFileCap(t *testing.T) {
	tb, root := cpNewBox(t)
	old := cpMaxFiles
	cpMaxFiles = 3
	t.Cleanup(func() { cpMaxFiles = old })
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		cpWrite(t, root, "f"+n+".txt", n)
	}
	tb.BeginTurn(0)
	if tb.BashTracked() || tb.turns[0].cpStart != "" {
		t.Fatal("checkpoints must turn off above the file cap")
	}
	if !strings.Contains(tb.cp.off, "3 arquivos") {
		t.Errorf("reason = %q", tb.cp.off)
	}
}

func TestCheckpointOffByDefault(t *testing.T) {
	tb, root := toolsNewBox(t)
	tb.BeginTurn(0)
	if tb.BashTracked() || tb.cp != nil {
		t.Fatal("checkpoints must be opt-in")
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "checkpoints")); !os.IsNotExist(err) {
		t.Fatalf("no shadow repo expected, stat err=%v (root %s)", err, root)
	}
}

func TestCheckpointGitVersion(t *testing.T) {
	cases := map[string]bool{
		"git version 2.47.3":               true,
		"git version 2.50.1 (Apple Git-1)": true,
		"git version 2.25.0":               true,
		"git version 2.20.1":               false,
		"git version 3.0":                  true,
		"garbage":                          false,
	}
	for v, want := range cases {
		if got := cpGitAtLeast(v, 2, 25); got != want {
			t.Errorf("%q: got %v", v, got)
		}
	}
}
