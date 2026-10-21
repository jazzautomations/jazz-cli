//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Process trees, Unix flavour: every child we start leads its own process
// group (Setpgid), so one kill(-pgid) reaches everything it spawned. The
// Windows flavour (proc_windows.go) uses a job object per tree instead.

// procPrepare makes cmd the leader of a new process group. detached is for
// background jobs that must outlive a crash of jazz; Unix starts those through
// setsid in the shell, so it makes no difference here.
func procPrepare(cmd *exec.Cmd, detached bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// procAttach registers a started cmd (job object on Windows). No-op here.
func procAttach(cmd *exec.Cmd) {}

// procForget releases what procAttach kept for pid. No-op here.
func procForget(pid int) {}

// procKillTree SIGKILLs the process group led by pid.
func procKillTree(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// procTermTree asks the process group led by pid to stop (SIGTERM).
func procTermTree(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
}

// procKillPID stops a background job (pid and its setsid group): SIGTERM, or
// SIGKILL when force.
func procKillPID(pid int, force bool) {
	if pid <= 0 {
		return
	}
	s := syscall.SIGTERM
	if force {
		s = syscall.SIGKILL
	}
	_ = syscall.Kill(-pid, s)
	_ = syscall.Kill(pid, s)
}

// procAlive reports whether pid still runs (zombies count as gone on Linux).
func procAlive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 2 && (f[2] == "Z" || f[2] == "X") {
			return false
		}
	}
	return true
}
