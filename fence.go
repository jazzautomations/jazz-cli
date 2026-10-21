package main

// fence.go: work-dir fence (red-team 08). After a blocked fetch the model
// walked up out of the work dir and read the harness's own repository. The
// model's file tools, bash path arguments (cd included) and task prompts are
// kept inside the project root: the directory jazz started in, plus the git
// top-level above it when that directory is part of the repository (not
// git-ignored). Outside paths that are fine to read: system and toolchain
// locations (/usr, /opt, /etc, Go/Rust/Python/Node caches), temp dirs (unless
// the project itself lives in one) and jazz's own spill/job logs and skills.
// The rest of jazz's config and data dir is never readable.
//
// A fenced path asks the user (interactive) or is refused with a steering
// message (-p). --allow-outside, config allow_outside_project or
// JAZZ_ALLOW_OUTSIDE=1 turn it off.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// fenceOn reports whether the work-dir fence applies.
func (a *Agent) fenceOn() bool {
	if a.tools == nil || a.tools.Root == "" {
		return false
	}
	if v := strings.TrimSpace(os.Getenv("JAZZ_ALLOW_OUTSIDE")); v != "" && !olOffWord(v) {
		return false
	}
	return a.cfg == nil || !(a.cfg.AllowOutsideProject || a.cfg.AllowOutsideRun)
}

// fenceRoots is the project root plus, when the root is a tracked part of a
// larger git work tree, that tree's top-level. Computed once per toolbox.
func (t *Toolbox) fenceRoots() []string {
	t.fenceOnce.Do(func() {
		t.fenceDirs = []string{t.Root}
		if top := fenceGitTop(t.Root); top != "" && top != t.Root && pathIn(resolvePath(t.Root), top) {
			t.fenceDirs = append(t.fenceDirs, top)
		}
	})
	return t.fenceDirs
}

// fenceGitTop returns the git top-level of root when root is not git-ignored
// in it ("" otherwise). An ignored dir (an eval work dir inside results/) is
// not part of that repository.
func fenceGitTop(root string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	top := filepath.Clean(strings.TrimSpace(string(out)))
	if top == "" || top == "." {
		return ""
	}
	// exit 0 = ignored, 1 = not ignored
	if err := exec.CommandContext(ctx, "git", "-C", top, "check-ignore", "-q", root).Run(); err == nil {
		return ""
	}
	return top
}

// fencePrivate reports jazz's own config/data paths the model never reads:
// credentials, sessions, trusted folders. Skills, spill and job logs are fine.
func fencePrivate(rp string) bool {
	if fenceJazzOK(rp) {
		return false
	}
	return pathIn(rp, configDir()) || pathIn(rp, dataDir())
}

// fenceJazzOK: jazz files the model is pointed at (skills, spilled output, job logs).
func fenceJazzOK(rp string) bool {
	dirs := []string{filepath.Join(configDir(), "skills"), spillDir(), jobsDir()}
	if home, _ := os.UserHomeDir(); home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	for _, d := range dirs {
		if pathIn(rp, d) {
			return true
		}
	}
	return false
}

// fenceSystem are prefixes outside any project that are fine to read.
var fenceSystem = []string{"/usr", "/opt", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/dev",
	"/proc", "/sys", "/nix", "/snap", "/System", "/Library", "/Applications", "/private/etc", "/home/linuxbrew"}

// fenceHomeOK are toolchain locations under $HOME (module caches, SDKs).
var fenceHomeOK = []string{"go/pkg/mod", "sdk", ".cargo/registry", ".cargo/git", ".rustup/toolchains", ".pyenv/versions",
	".nvm/versions", ".local/lib", ".m2/repository", ".gradle/caches", ".yarn/berry/cache", ".pnpm-store", "Library/pnpm", ".bun/install/cache", ".deno"}

// fenceTemp are the temp dirs.
func fenceTemp() []string {
	return []string{os.TempDir(), "/tmp", "/var/tmp", "/private/tmp", "/private/var/folders"}
}

// fenceAllowed reports whether the model may touch p (absolute).
func (a *Agent) fenceAllowed(p string) bool {
	rp := resolvePath(p)
	root := resolvePath(a.tools.Root)
	inJazz := pathIn(root, configDir()) || pathIn(root, dataDir())
	if !inJazz && fencePrivate(rp) {
		return false
	}
	for _, r := range a.tools.fenceRoots() {
		if pathIn(rp, r) {
			return true
		}
	}
	if fenceJazzOK(rp) {
		return true
	}
	if inJazz {
		return false
	}
	if runtime.GOOS == "windows" {
		for _, e := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
			if d := os.Getenv(e); d != "" && pathIn(rp, d) {
				return true
			}
		}
	} else {
		for _, d := range fenceSystem {
			if pathIn(rp, d) {
				return true
			}
		}
	}
	for _, e := range []string{"GOROOT", "GOMODCACHE", "CARGO_HOME", "RUSTUP_HOME", "PYENV_ROOT", "NVM_DIR"} {
		if d := os.Getenv(e); d != "" && filepath.IsAbs(d) && pathIn(rp, d) {
			return true
		}
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		for _, d := range filepath.SplitList(gp) {
			if filepath.IsAbs(d) && pathIn(rp, filepath.Join(d, "pkg", "mod")) {
				return true
			}
		}
	}
	if home, _ := os.UserHomeDir(); home != "" {
		for _, d := range fenceHomeOK {
			if pathIn(rp, filepath.Join(home, d)) {
				return true
			}
		}
	}
	// temp dirs are scratch space, unless the project itself lives in one:
	// then a sibling temp dir may be the repository the project was copied from
	tmps := fenceTemp()
	for _, d := range tmps {
		if pathIn(root, d) {
			return false
		}
	}
	for _, d := range tmps {
		if d != "" && pathIn(rp, d) {
			return true
		}
	}
	return false
}

// fenceExpand turns a shell word into an absolute path (~ and $HOME expanded,
// relative words against the shell's cwd).
func (a *Agent) fenceExpand(w string) string {
	return a.fenceExpandIn(w, "")
}

// fenceExpandIn is fenceExpand against cwd ("" = the shell's cwd).
func (a *Agent) fenceExpandIn(w, cwd string) string {
	home, _ := os.UserHomeDir()
	switch {
	case w == "~":
		w = home
	case strings.HasPrefix(w, "~/"):
		w = filepath.Join(home, w[2:])
	case strings.HasPrefix(w, "$HOME"):
		w = home + w[len("$HOME"):]
	case strings.HasPrefix(w, "${HOME}"):
		w = home + w[len("${HOME}"):]
	}
	if !filepath.IsAbs(w) {
		if cwd == "" {
			cwd = a.fenceCwd()
		}
		w = filepath.Join(cwd, w)
	}
	return filepath.Clean(w)
}

// fenceCwd is the persistent shell's cwd (the root before the first command).
func (a *Agent) fenceCwd() string {
	if a.tools.Shell != nil && a.tools.Shell.Cwd != "" {
		return a.tools.Shell.Cwd
	}
	return a.tools.Root
}

// fenceExists: only paths that are there count (a word like "/api/v1" in a
// grep pattern is not a path).
func fenceExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !os.IsNotExist(err)
}

// fenceBash returns the first path a shell command reaches outside the fence.
func (a *Agent) fenceBash(cmd string) string {
	home, _ := os.UserHomeDir()
	cwd := a.fenceCwd() // follows "cd x && … cd .." inside the command
	for _, seg := range shellParse(cmd).segs {
		toks := stripWrappers(shellFields(seg))
		if len(toks) == 0 {
			continue
		}
		w := filepath.Base(toks[0])
		if w == "cd" || w == "pushd" {
			dst := home
			if len(toks) > 1 {
				if toks[1] == "-" {
					continue
				}
				dst = a.fenceExpandIn(toks[1], cwd)
			}
			if dst == "" {
				continue
			}
			if !a.fenceAllowed(dst) {
				return dst
			}
			cwd = dst
			continue
		}
		if w == "echo" || w == "printf" {
			continue // prints its words, reads nothing
		}
		for _, t := range toks[1:] {
			t = reFenceRedir.ReplaceAllString(t, "")
			if t == "" || len(t) > 1 && strings.Trim(t, "/") == "" || strings.HasPrefix(t, "-") || strings.Contains(t, "://") || strings.ContainsAny(t, "*?[") {
				continue
			}
			if !strings.ContainsAny(t, `/\`) && t != "~" && t != ".." && !strings.HasPrefix(t, "$HOME") && !strings.HasPrefix(t, "${HOME}") {
				continue // a plain word: a name in the cwd at most
			}
			p := a.fenceExpandIn(t, cwd)
			if fenceExists(p) && !a.fenceAllowed(p) {
				return p
			}
		}
	}
	return ""
}

// reFenceRedir is a redirection glued to its target ("2>/dev/null", ">out").
var reFenceRedir = regexp.MustCompile(`^(?:[0-9]*[<>]+[|&]?|&>+)`)

// reFencePath finds absolute and ~/ paths in free text (task prompts).
var reFencePath = regexp.MustCompile("(?:^|[\\s\"'`(=:,])((?:/|~/)[^\\s\"'`),;:]+)")

// fenceText returns the first existing path in s outside the fence.
func (a *Agent) fenceText(s string) string {
	for _, m := range reFencePath.FindAllStringSubmatch(s, 50) {
		p := strings.TrimRight(m[1], ".")
		if p == "/" || p == "~/" {
			continue
		}
		x := a.fenceExpand(p)
		if fenceExists(x) && !a.fenceAllowed(x) {
			return x
		}
	}
	return ""
}

// fenceCheck returns the outside path a call would reach ("" = inside).
func (a *Agent) fenceCheck(tool string, args map[string]any, cmd string, isMCP bool) string {
	if isMCP || !a.fenceOn() {
		return ""
	}
	switch tool {
	case "read", "write", "edit":
		if p, _ := argStr(args, "path"); strings.TrimSpace(p) != "" {
			if x := a.tools.abs(p); !a.fenceAllowed(x) {
				return x
			}
		}
	case "script":
		for _, k := range []string{"path", "dir", "file"} {
			if p, _ := argStr(args, k); strings.TrimSpace(p) != "" {
				if x := a.tools.abs(p); fenceExists(x) && !a.fenceAllowed(x) {
					return x
				}
			}
		}
	case "bash":
		if cmd != "" {
			return a.fenceBash(cmd)
		}
	case "task":
		p, _ := argStr(args, "prompt")
		d, _ := argStr(args, "description")
		return a.fenceText(p + "\n" + d)
	}
	return ""
}
