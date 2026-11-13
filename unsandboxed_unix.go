//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// usProcGroup: the command and its children (a browser, a render) share a
// process group that a timeout kills as a whole.
func usProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
}
