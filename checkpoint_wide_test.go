//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cpCountingGit puts a fake git first in PATH that logs every call and then
// runs the real one; returns the log path.
func cpCountingGit(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func cpCalls(log string) int {
	b, _ := os.ReadFile(log)
	return strings.Count(string(b), "\n")
}

// jazz started in $HOME: no git call at all, /undo says why.
func TestCheckpointHomeNoGit(t *testing.T) {
	log := cpCountingGit(t)
	tb, root := cpNewBox(t)
	t.Setenv("HOME", root)
	cpWrite(t, root, "f.txt", "x")
	tb.BeginTurn(0)
	cpBash(t, tb, "echo y > f.txt")
	if n := cpCalls(log); n != 0 {
		b, _ := os.ReadFile(log)
		t.Fatalf("root == $HOME: want 0 git calls, got %d:\n%s", n, b)
	}
	if tb.BashTracked() {
		t.Error("checkpoints must be off in $HOME")
	}
	if why := tb.CheckpointOff(); !strings.Contains(why, "rode dentro do projeto") {
		t.Errorf("off reason: %q", why)
	}
	if !cpWideRoot("/") {
		t.Error("/ is a wide root")
	}
	if cpWideRoot(filepath.Join(root, "proj")) {
		t.Error("a project inside $HOME is not a wide root")
	}
}

// "too many files" is remembered for 48h: the next run makes no git call;
// a stale verdict is dropped and the scan runs again.
func TestCheckpointOffPersisted(t *testing.T) {
	tb, root := cpNewBox(t)
	old := cpMaxFiles
	cpMaxFiles = 3
	t.Cleanup(func() { cpMaxFiles = old })
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		cpWrite(t, root, "f"+n+".txt", n)
	}
	tb.BeginTurn(0)
	if tb.BashTracked() {
		t.Fatal("over the cap: off")
	}
	off := filepath.Join(tb.cp.gitDir, cpOffFile)
	if _, err := os.Stat(off); err != nil {
		t.Fatalf("OFF not persisted: %v", err)
	}

	log := cpCountingGit(t)
	tb2 := NewToolbox(root)
	t.Cleanup(tb2.Shell.Close)
	tb2.Checkpoints = true
	tb2.BeginTurn(0)
	if n := cpCalls(log); n != 0 {
		t.Fatalf("persisted OFF: want 0 git calls, got %d", n)
	}
	if why := tb2.CheckpointOff(); !strings.Contains(why, "48h") {
		t.Errorf("reason: %q", why)
	}

	stale := time.Now().Add(-49 * time.Hour)
	if err := os.Chtimes(off, stale, stale); err != nil {
		t.Fatal(err)
	}
	if cpOffPersisted(tb.cp.gitDir) != "" {
		t.Error("a 49h-old OFF must expire")
	}
	if _, err := os.Stat(off); !os.IsNotExist(err) {
		t.Error("the stale OFF file is removed")
	}
}
