//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A temp dir removed under a live session is recreated before the next
// spawn (bwrap binds it: a missing source killed every command).
func TestSandboxTmpRecreatedBeforeSpawn(t *testing.T) {
	base := t.TempDir()
	sb := &Sandbox{Backend: sbBwrap, Bin: "/bin/true", Tmp: filepath.Join(base, fmt.Sprintf("%d-x", os.Getpid()))}
	if !sb.ensureTmp() {
		t.Fatal("a missing temp dir must be recreated")
	}
	if fi, err := os.Stat(sb.Tmp); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("temp dir not recreated private: %v %v", fi, err)
	}
	if sb.ensureTmp() {
		t.Error("an existing temp dir is left alone")
	}
	_ = os.RemoveAll(sb.Tmp)
	cmd := exec.Command("/bin/echo", "x")
	sb.Wrap(cmd)
	if _, err := os.Stat(sb.Tmp); err != nil {
		t.Errorf("Wrap must recreate the temp dir before the child starts: %v", err)
	}
}

// The prune removes only the dirs of dead jazz processes, whatever their age.
func TestSandboxPruneOnlyDeadPids(t *testing.T) {
	base := t.TempDir()
	tr, err := exec.LookPath("true")
	if err != nil {
		t.Skip(err)
	}
	dead := exec.Command(tr)
	if err := dead.Run(); err != nil {
		t.Skip(err)
	}
	live := filepath.Join(base, fmt.Sprintf("%d-live", os.Getppid()))
	gone := filepath.Join(base, fmt.Sprintf("%d-gone", dead.Process.Pid))
	odd := filepath.Join(base, "notapid")
	for _, d := range []string{live, gone, odd} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sbPruneTmp(base)
	if _, err := os.Stat(live); err != nil {
		t.Error("a live process's temp dir must survive the prune (even if old)")
	}
	if _, err := os.Stat(gone); err == nil && !procAlive(dead.Process.Pid) {
		t.Error("a dead process's temp dir must be pruned")
	}
	if _, err := os.Stat(odd); err != nil {
		t.Error("a dir that is not <pid>-… is not ours to remove")
	}
}

// The persistent shell survives a vanished bwrap source: the first spawn
// fails before the command runs, the temp dir is recreated and the command
// runs once more.
func TestShellRetriesWhenSandboxSourceVanished(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "failed-once")
	// a fake "shell" that fails like bwrap once, then is a real bash
	fake := filepath.Join(dir, "fakesh")
	script := "#!/bin/sh\nif [ ! -e " + flag + " ]; then touch " + flag + "; echo \"bwrap: Can't find source path /tmp/jazz-sbx-1/1-x: No such file or directory\"; exit 1; fi\nexec bash --norc --noprofile\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash missing")
	}
	sh := NewShell(dir)
	defer sh.Close()
	sh.d = shellDialect{Kind: shBash, Path: fake}
	r := sh.Run(t.Context(), "echo hello-after-heal", 10e9, nil)
	if !strings.Contains(r.Output, "hello-after-heal") || r.ExitCode != 0 {
		t.Fatalf("the command must run after the heal: %+v", r)
	}
}

// Only an output that is bwrap's mount error alone means "never started": a
// command that printed the message itself must not run twice.
func TestSandboxSourceMissingOnlyBwrap(t *testing.T) {
	if !sbSourceMissing("bwrap: Can't find source path /tmp/jazz-sbx-1/1-x: No such file or directory\n") {
		t.Error("bwrap's own error is the vanished source")
	}
	if sbSourceMissing("grep: log\nbwrap: Can't find source path /x\n") || sbSourceMissing("old.log: bwrap: Can't find source path /x") {
		t.Error("a command's own output is not bwrap failing to start")
	}
}
