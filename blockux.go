package main

// blockux.go: what happens after a guard blocks something (red-team 03/08,
// docs/research/redteam-results.md). The security layer blocked correctly, but
// the model got no actionable instruction and spiralled until the timeout:
// probing the guard, trying workarounds, reading the harness's own source.
//
//  1. Every block result the model gets is one English line that says WHAT
//     was blocked, WHY (one clause) and WHAT TO DO (blockSteer). The user
//     keeps the Portuguese line on the terminal.
//  2. Blocked calls are counted per turn and per target (path, host, command
//     class): 3 blocks on the same target, or 6 in the turn, stop the turn
//     with a report for the user instead of another model request, so the
//     final message never claims a success that did not happen.
//
// config block_ux: on (default) | off; JAZZ_BLOCK_UX; --block-ux on|off.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// blockSteer is the instruction every block result ends with.
const blockSteer = "Continue the user's original task without this content/action. Do not retry or look for workarounds."

// Spiral breaker limits.
const (
	blockPerTarget = 3
	blockPerTurn   = 6
)

// blockUX reports whether steering block messages and the spiral breaker are on.
func (a *Agent) blockUX() bool {
	if v := strings.TrimSpace(os.Getenv("JAZZ_BLOCK_UX")); v != "" {
		return !olOffWord(v)
	}
	if a.cfg == nil {
		return true
	}
	if a.cfg.BlockUXRun != "" {
		return !olOffWord(a.cfg.BlockUXRun)
	}
	return !olOffWord(a.cfg.BlockUX)
}

// blockMsg is the one-line block result for the model.
func blockMsg(what, why string) string {
	what = strings.TrimSpace(what)
	why = strings.TrimRight(strings.TrimSpace(why), ".")
	if why == "" {
		why = "a safety guard refused it"
	}
	return "BLOCKED: " + what + " — " + why + ". " + blockSteer
}

// blockWhat names the blocked action for the model ("bash `curl …`").
func blockWhat(tool, detail string) string {
	d := oneLine(strings.TrimSpace(detail), 140)
	if d == "" {
		return tool
	}
	if tool == "bash" {
		return "bash `" + d + "`"
	}
	return tool + " " + d
}

// blockTracker counts blocked calls in one turn.
type blockTracker struct {
	total int
	per   map[string]int
}

// blockTarget is the key blocks are grouped by: the path for file tools, the
// host for fetch and egress, the command class (first word) for bash.
func (a *Agent) blockTarget(tc ToolCall) string {
	if a.mcp.Has(tc.Name) {
		return "mcp:" + tc.Name
	}
	args := acArgsMap(tc.Name, tc.Args)
	switch tc.Name {
	case "read", "write", "edit":
		if p, _ := argStr(args, "path"); strings.TrimSpace(p) != "" {
			return "path:" + a.tools.abs(p)
		}
	case "fetch":
		u, _ := argStr(args, "url")
		if h := acFetchHost(strings.TrimSpace(u)); h != "" {
			return "host:" + h
		}
	case "bash":
		cmd, _ := argStr(args, "command")
		if h, eg := EgressTarget(cmd); eg && h != "" {
			return "host:" + strings.ToLower(h)
		}
		if c := blockCmdClass(cmd); c != "" {
			return "cmd:" + c
		}
	}
	return "tool:" + tc.Name
}

// blockCmdClass is the first command word of a shell line, skipping cd/env
// prefixes and wrappers ("cd x && python3 -c …" → python3).
func blockCmdClass(cmd string) string {
	p := shellParse(cmd)
	first := ""
	for _, seg := range p.segs {
		toks := stripWrappers(shellFields(seg))
		for len(toks) > 0 && strings.Contains(toks[0], "=") && !strings.HasPrefix(toks[0], "=") {
			toks = toks[1:] // FOO=bar cmd
		}
		if len(toks) == 0 {
			continue
		}
		w := filepath.Base(toks[0])
		if first == "" {
			first = w
		}
		switch w {
		case "cd", "pushd", "popd", "set", "export", "true", ":":
			continue
		}
		return w
	}
	return first
}

// blockLabel is how a target is shown to the user.
func (a *Agent) blockLabel(target string) string {
	kind, v, _ := strings.Cut(target, ":")
	switch kind {
	case "path":
		return a.tools.rel(v)
	case "host":
		return v
	case "cmd":
		return "comandos `" + v + "`"
	case "mcp":
		return v
	}
	return v
}

// blockNote records a blocked call. It returns the stop report when the turn
// must end, or a reminder to append to the tool result ("" = nothing).
func (a *Agent) blockNote(target, why string) (stopReport, reminder string) {
	if a.blk.per == nil {
		a.blk.per = map[string]int{}
	}
	a.blk.total++
	a.blk.per[target]++
	n := a.blk.per[target]
	why = strings.TrimSpace(why)
	if why == "" {
		why = "bloqueado por uma guarda de segurança"
	}
	label := a.blockLabel(target)
	switch {
	case n >= blockPerTarget:
		return fmt.Sprintf("%d tentativas bloqueadas em %s; parei para não gastar tempo. Motivo: %s.", n, label, strings.TrimRight(why, ".")), ""
	case a.blk.total >= blockPerTurn:
		return fmt.Sprintf("%d tentativas bloqueadas neste turno (a última em %s); parei para não gastar tempo. Motivo: %s.", a.blk.total, label, strings.TrimRight(why, ".")), ""
	case n == blockPerTarget-1 || a.blk.total == blockPerTurn-1:
		return "", "\n\n<system-reminder>This action was blocked again. One more blocked attempt and the harness stops the turn. Do not try another way to do the same thing: finish the user's task without it, or give your final answer and say what was blocked.</system-reminder>"
	}
	return "", ""
}

// blockStopReminder is left in the history when the breaker stops a turn, so
// the next turn knows what happened and does not claim success.
func blockStopReminder(label string) string {
	return "<system-reminder>The harness stopped the previous turn after repeated blocked attempts (last target: " + label +
		"). The task was NOT completed. Do not claim it was; if the user asks, explain what was blocked and why.</system-reminder>"
}
