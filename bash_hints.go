package main

import (
	"path/filepath"
	"strings"
)

// One-line notes after "[exit code N]" saying what the status means, so a
// weak model does not retry a non-error or trust a masked success (ported
// from Hermes Agent's terminal hints, tools/terminal_hints.py).

// bhPipedTest: a test command whose output goes through a pipe — the exit
// code the shell reports is the last command's (tail, grep), not the tests'.
func bhPipedTest(cmd string) bool {
	if !bgIsTestCmd(cmd) || strings.Contains(cmd, "pipefail") || strings.Contains(cmd, "PIPESTATUS") {
		return false
	}
	segs := strings.Split(strings.ReplaceAll(cmd, "||", "\x00"), "|")
	if len(segs) < 2 {
		return false
	}
	last := segs[len(segs)-1]
	if k := strings.IndexAny(last, ";&\x00"); k >= 0 {
		last = last[:k]
	}
	return !bgIsTestCmd(last)
}

var bhNoModuleRE = lazyRx(`(?m)No module named '?([\w.]+)'?`)

// bhExitHint returns the note for a finished command ("" = none).
func bhExitHint(cmd string, exit int, out string) string {
	head := out
	if len(head) > 4096 {
		head = head[len(head)-4096:]
	}
	first := ""
	if f := shellFields(cmd); len(f) > 0 {
		first = filepath.Base(stripWrappers(f)[0])
	}
	switch {
	case exit == 0 && bhPipedTest(cmd):
		if vsRedRE.MatchString(head) {
			return "[note: exit 0 is from the last command of the pipe; the output shows test FAILURES]"
		}
		return "[note: exit 0 is from the last command of the pipe, not the tests — their status is unknown; re-run without the pipe (or with set -o pipefail) before calling them green]"
	case exit == 1 && (first == "grep" || first == "rg" || first == "egrep" || first == "fgrep") && !strings.Contains(cmd, "|"):
		return "[note: grep exit 1 = no match, not an error]"
	case exit == 1 && (first == "diff" || first == "cmp") && !strings.Contains(cmd, "|"):
		return "[note: exit 1 = the files differ, not an error]"
	case exit == 137 || exit == -9:
		return "[note: killed with SIGKILL — on this machine usually out of memory; lower parallelism (-p 1, -j1, smaller input) instead of re-running as is]"
	case exit == 124:
		return "[note: exit 124 = the timeout command killed it]"
	case exit == 127 && strings.Contains(head, "python: command not found") || exit == 127 && strings.Contains(head, "python: not found"):
		return "[note: there is no `python` here; use python3]"
	case exit == 127:
		return "[note: exit 127 = command not found; check the name or install it, do not re-run unchanged]"
	case exit != 0 && bhNoModuleRE.MatchString(head):
		m := bhNoModuleRE.FindStringSubmatch(head)
		return "[note: module " + m[1] + " is not installed for this interpreter (or it is the project's own package and the command runs from the wrong directory)]"
	case exit != 0 && (strings.Contains(head, "Permission denied") || strings.Contains(head, "Operation not permitted")):
		return "[note: permission error — re-running unchanged will fail the same way]"
	}
	return ""
}
