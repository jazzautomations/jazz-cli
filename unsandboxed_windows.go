//go:build windows

package main

import "os/exec"

// usProcGroup: Windows has no OS sandbox in jazz, so the escape never runs here.
func usProcGroup(c *exec.Cmd) {}
