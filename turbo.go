package main

// turbo.go: the "turbo" permission mode (alias full-auto). Owner: platform.
//
// Turbo never asks about work inside the project and inside the OS sandbox
// (sandbox.go). It asks only when it is really necessary:
//
//	(a) writes/deletes (and reads) outside the project root (fence.go);
//	    "a" remembers the outside directory for this project;
//	(b) a command that sends data to a host not seen in this project (egress
//	    gate); the first approval remembers the host (approvals.go);
//	(c) reading secrets (.env, keys, ~/.ssh, auth.json …) and writing
//	    protected files (hooks, agent policy, .git/config): every time;
//	(d) destructive git/history operations: push --force/--mirror/--delete,
//	    branch -D, filter-branch/filter-repo, update-ref -d, stash drop/clear,
//	    clean -f, and reset --hard/--merge/--keep on a shared branch (one with
//	    an upstream, or main/master/trunk/develop/release*); "a" remembers the
//	    exact command;
//	(e) everything else DangerReason flags (rm -rf of home/root/the project,
//	    disk/format ops, chmod -R on broad paths, sudo/su, curl|sh, download
//	    and execute, fork bombs, shutdown, unparsable commands); "a" is
//	    offered only for curl|sh from a named host;
//	(f) package installs the supply-chain guard flags (typosquat / OSV hit);
//	    clean installs run without asking;
//	(g) what the user or the project configured to ask, and the guards that
//	    are not about the project: ask rules, PreToolUse hooks that ask, MCP
//	    tool descriptions that changed (rug-pull), and actions that change
//	    something after a suspected prompt injection in the same turn.
//
//	(h) a folder that is not trusted yet (F1): bash that is not read-only
//	    asks until /trust (jazz trust --yes in CI), as in auto. Red-team 11
//	    showed why: `make setup` runs the repository's own curl|sh, which no
//	    command classifier sees; the sandbox contained it, but the payload
//	    still ran.
//
// Everything else runs without asking: tests, builds, formatting, git
// add/commit/checkout -b, edits and writes inside the project, reads,
// scripts, clean package installs, network reads. What auto asks and turbo
// does not: git discards a shadow-git checkpoint can undo (checkout -- .,
// restore ., checkout -f, reset --hard on a branch that is not shared), and
// a second upload to a host already approved in this project.
//
// Turbo is deterministic: it never calls a model (smart mode does). It
// needs an OS sandbox that isolates children (sandbox-exec or bwrap); without
// one it applies the auto rules and says so once at startup.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// turboSandboxOK reports whether an OS sandbox isolates what jazz runs.
// Tests replace it.
var turboSandboxOK = func() bool { return sbCurrent().Isolates() }

// tbModeName maps the accepted spellings to the canonical mode name.
func tbModeName(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "turbo", "full-auto", "fullauto", "full_auto":
		return "turbo"
	}
	return m
}

// turboFallbackMsg is shown once when turbo has no sandbox to lean on.
const turboFallbackMsg = "modo turbo sem sandbox do sistema (sandbox-exec/bwrap): usando as regras do auto (pergunta em comando perigoso, saída de dados e fora do projeto)"

var tbWarned atomic.Bool

// turboStartNote returns the fallback notice the first time it applies, "" otherwise.
func turboStartNote(mode string) string {
	if mode != "turbo" || turboSandboxOK() || !tbWarned.CompareAndSwap(false, true) {
		return ""
	}
	return turboFallbackMsg
}

// turboWelcomeHint is the welcome-line suggestion: turbo when a sandbox is present.
func turboWelcomeHint(mode string) string {
	if mode == "turbo" || mode == "yolo" || !turboSandboxOK() {
		return ""
	}
	return "recomendado: /mode turbo (sandbox ativo, só pergunta o necessário)"
}

// tbDangerParts splits DangerReason's verdict by source: whole is the reason
// a whole-command detector gave ("" = none), segs are the simple commands
// segmentDanger flags. ok is false where the split is not available (Windows
// dialects), so callers keep the plain DangerReason behaviour.
func tbDangerParts(cmd string) (whole string, segs [][]string, ok bool) {
	if winIsWindows {
		return "", nil, false
	}
	if r := dangerWholeSh(cmd); r != "" {
		return r, nil, true
	}
	pr := shellParse(cmd)
	if !pr.ok {
		return reUnparsable, nil, true
	}
	if r := downloadExecReason(pr.segs); r != "" {
		return r, nil, true
	}
	for _, code := range pr.code {
		if r := inlineCodeDanger(code); r != "" {
			return r, nil, true
		}
	}
	for _, seg := range pr.segs {
		t := shellFields(seg)
		if segmentDanger(t) != "" {
			segs = append(segs, stripWrappers(t))
		}
	}
	return "", segs, true
}

// tbGitArgs returns the git subcommand and its arguments for a plain
// `git <sub> …` segment (no global options such as -C or --git-dir, which
// could point the command at another repository).
func tbGitArgs(t []string) (sub string, rest []string, ok bool) {
	if len(t) < 2 || filepath.Base(t[0]) != "git" || t[0] != "git" && !filepath.IsAbs(t[0]) {
		return "", nil, false
	}
	if strings.HasPrefix(t[1], "-") {
		return "", nil, false
	}
	return t[1], t[2:], true
}

func tbHas(rest []string, xs ...string) bool {
	for _, a := range rest {
		for _, x := range xs {
			if a == x {
				return true
			}
		}
	}
	return false
}

// tbChangesDir reports whether any segment changes directory (cd/pushd):
// the git state checked here would then not be the one the command uses.
func tbChangesDir(cmd string) bool {
	for _, seg := range shellSegments(cmd) {
		t := stripWrappers(shellFields(seg))
		if len(t) > 0 && (t[0] == "cd" || t[0] == "pushd" || t[0] == "popd") {
			return true
		}
	}
	return false
}

// tbGitShared reports whether the branch checked out in dir is shared: it has
// an upstream, or its name is one teams share (main, master, trunk, develop,
// release*, prod*). Any error counts as shared.
var tbGitShared = func(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		c.Env = append(scrubEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		out, err := c.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := run("rev-parse", "--git-dir"); err != nil {
		return true
	}
	br, err := run("symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return false // detached HEAD: reset only moves HEAD; the reflog keeps the commits
	}
	if reSharedBranch.MatchString(br) {
		return true
	}
	if up, err := run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && up != "" {
		return true
	}
	return false
}

var reSharedBranch = lazyRx(`^(main|master|trunk|develop|development|dev|stable|release.*|prod.*|production|gh-pages)$`)

// tbGitRecoverable reports whether every dangerous part of cmd is a git
// discard that the shadow-git checkpoint (checkpoint.go) can undo: checkout
// -- . / checkout -f / restore . and, on a branch that is not shared, reset
// --hard/--merge/--keep. dir is where the command runs.
func tbGitRecoverable(cmd, dir string) bool {
	whole, segs, ok := tbDangerParts(cmd)
	if !ok || whole != "" || len(segs) == 0 || tbChangesDir(cmd) {
		return false
	}
	for _, t := range segs {
		sub, rest, ok := tbGitArgs(t)
		if !ok {
			return false
		}
		switch sub {
		case "checkout", "restore":
			// discards of tracked files (gitDanger flags them); nothing else
		case "reset":
			if !tbHas(rest, "--hard", "--merge", "--keep") || tbGitShared(dir) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

var reTbURLHost = lazyRx(`https?://([A-Za-z0-9.-]+)`)

// tbDangerClass returns the approval class and target for a dangerous command
// that "a" (sempre) may remember: destructive git ("git", the exact command)
// and curl|sh from one named host ("pipe-shell", the host). Every other
// danger is not rememberable ("", "").
func tbDangerClass(cmd string) (class, target string) {
	whole, segs, ok := tbDangerParts(cmd)
	if !ok {
		return "", ""
	}
	if whole == "" && len(segs) > 0 {
		for _, t := range segs {
			if _, _, ok := tbGitArgs(t); !ok {
				return "", ""
			}
		}
		return "git", strings.Join(strings.Fields(cmd), " ")
	}
	if strings.HasPrefix(whole, "executa script baixado") {
		hosts := map[string]bool{}
		for _, m := range reTbURLHost.FindAllStringSubmatch(cmd, -1) {
			hosts[strings.ToLower(m[1])] = true
		}
		if len(hosts) == 1 {
			for h := range hosts {
				return "pipe-shell", h
			}
		}
	}
	return "", ""
}

// tbIsChange reports whether a call may change something (the calls turbo
// approves on its own, and records in the audit log).
func (a *Agent) tbIsChange(tool, cmd string, isMCP, jobKill bool) bool {
	switch {
	case isMCP:
		return a.mcp == nil || !a.mcp.ReadOnly(tool)
	case tool == "bash":
		return jobKill || cmd != "" && !IsReadOnly(cmd)
	case tool == "write", tool == "edit", tool == "script", tool == "fetch":
		return true
	}
	return false
}

// apRecordAuto writes an auto-approval to the audit log: the call ran without
// a prompt because of rule (turbo:…, approvals:…, perm:…, hook).
func (a *Agent) apRecordAuto(tool, detail, rule string) {
	f := map[string]any{"tool": tool, "target": oneLine(redactSecrets(detail), 200), "rule": rule, "mode": a.mode()}
	dbg("auto_approval", f)
	auditRec("auto_approval", f)
}

// cpUsable reports whether shadow-git checkpoints can undo what bash changes.
func (t *Toolbox) cpUsable() bool {
	c := t.checkpoints()
	return c != nil && c.usable()
}

// apDetailOf is the text an audit record names for a call.
func (a *Agent) apDetailOf(tool, detail, cmd string, args map[string]any) string {
	if tool == "bash" && cmd != "" {
		return cmd
	}
	if p, _ := argStr(args, "path"); p != "" && (tool == "write" || tool == "edit") {
		return p
	}
	return detail
}

// approveAlwaysOffer is implemented by UIs that can offer "a" (sempre) on a
// prompt with a risk reason; label names what would be remembered.
type approveAlwaysOffer interface{ ApproveAlways(label string) }

// approveExtra is implemented by UIs whose "v" (ver detalhes) can show more
// than the detail line: the diff of an edit, the start of a write.
type approveExtra interface{ ApproveExtra(text string) }

// apPreview is what "v" shows for edits and writes ("" = only the detail).
func apPreview(tool string, args map[string]any) string {
	switch tool {
	case "edit":
		old, _ := argStr(args, "old_string")
		cur, _ := argStr(args, "new_string")
		if old == "" && cur == "" {
			return ""
		}
		var b strings.Builder
		for _, l := range strings.Split(old, "\n") {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range strings.Split(cur, "\n") {
			b.WriteString("+ " + l + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	case "write":
		c, _ := argStr(args, "content")
		if c == "" {
			return ""
		}
		lines := strings.Split(c, "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], "… +"+itoa(len(lines)-40)+" linhas")
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// turboModeNotice is what /mode turbo prints.
func turboModeNotice() string {
	if !turboSandboxOK() {
		return turboFallbackMsg
	}
	return "modo turbo: roda sem perguntar o que fica no projeto e no sandbox; pergunta só fora do projeto, saída pra host novo, segredos, git destrutivo, pacote suspeito e comando catastrófico"
}

// pfHintSuffix appends a dim hint to a banner line ("" = nothing).
func pfHintSuffix(dim, dot, hint string) string {
	if hint == "" {
		return ""
	}
	return "  " + dot + "  " + hint
}

// approvalsCmd implements /approvals: list (default) and clear.
func (a *App) approvalsCmd(args []string) {
	ui := a.ui
	st := a.agent.approvals()
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "clear", "limpar", "reset", "rm":
			if err := st.Clear(); err != nil {
				ui.Error("não consegui limpar: " + err.Error())
				return
			}
			ui.Notice("aprovações deste projeto apagadas: a próxima vez pergunta de novo")
			return
		default:
			ui.Error("uso: /approvals [clear]")
			return
		}
	}
	list, ignored := st.List()
	var b strings.Builder
	if ignored {
		b.WriteString("  .jazz/approvals.json foi alterado fora do jazz: ignorado (/approvals clear recomeça)\n")
	}
	if len(list) == 0 {
		b.WriteString("  nenhuma aprovação \"sempre\" neste projeto")
		ui.Notice(strings.TrimRight(b.String(), "\n"))
		return
	}
	fmt.Fprintf(&b, "  %d aprovação(ões) \"sempre\" neste projeto (.jazz/approvals.json):\n", len(list))
	for _, e := range list {
		fmt.Fprintf(&b, "    %s  %s\n", e.At.Local().Format("2006-01-02 15:04"), visibleDetail(e.Label))
	}
	b.WriteString("  /approvals clear apaga todas")
	ui.Notice(b.String())
}
