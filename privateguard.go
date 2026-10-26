package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// jazzPrivateStatePath identifies Jazz's private configuration and session
// state, including alternate XDG/JAZZ_HOME locations and symlink aliases.
// Intentionally exposed skill and job-output directories retain their existing
// fence exceptions.
func jazzPrivateStatePath(p string) bool {
	if p == "" {
		return false
	}
	rp := resolvePath(filepath.Clean(p))
	if fenceJazzOK(rp) {
		return false
	}
	return pathIn(rp, configDir()) || pathIn(rp, dataDir())
}

func jazzPrivatePathArg(w, cwd string) string {
	w = strings.TrimLeft(w, "<>")
	w = strings.TrimRight(w, ",;|&")
	if i := strings.IndexByte(w, '='); i > 0 && strings.HasPrefix(w, "--") {
		w = w[i+1:]
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(w, "-") {
		// PowerShell -Path:C:\x (the value is glued to the parameter)
		if i := strings.IndexByte(w, ':'); i > 1 {
			w = w[i+1:]
		}
	}
	if strings.HasPrefix(w, "@") {
		w = strings.TrimPrefix(w, "@")
	}
	home, _ := os.UserHomeDir()
	w = jazzExpandVars(w, home, os.Getenv)
	if w == "~" {
		w = home
	} else if strings.HasPrefix(w, "~/") {
		w = filepath.Join(home, strings.TrimPrefix(w, "~/"))
	}
	if w == "" || strings.HasPrefix(w, "-") || strings.Contains(w, "://") || strings.ContainsAny(w, "`'$()") {
		return ""
	}
	if i := strings.IndexAny(w, "*?["); i >= 0 {
		w = filepath.Dir(w[:i])
	}
	if !filepath.IsAbs(w) {
		w = filepath.Join(cwd, w)
	}
	return filepath.Clean(w)
}

// jazzVarForms are the spellings of the variables that lead to Jazz's state:
// POSIX ($X, ${X}), PowerShell ($env:X, case-insensitive) and cmd (%X%).
var jazzVarForms = func() [][2]string {
	var out [][2]string
	for _, v := range []string{"JAZZ_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOME"} {
		out = append(out, [2]string{"${" + v + "}", v}, [2]string{"$" + v, v}, [2]string{"$env:" + v, v}, [2]string{"%" + v + "%", v})
	}
	return out
}()

// jazzExpandVars expands a leading variable reference in w. home stands in
// for HOME/USERPROFILE when they are unset (PowerShell's $HOME is the
// profile dir); env looks the others up. Only a whole variable name matches:
// $HOMEX is left alone.
func jazzExpandVars(w, home string, env func(string) string) string {
	for _, f := range jazzVarForms {
		pre, name := f[0], f[1]
		if len(w) < len(pre) || !strings.EqualFold(w[:len(pre)], pre) {
			continue
		}
		// case-insensitive only where the shell is (PowerShell, cmd)
		if !strings.HasPrefix(pre, "$env:") && !strings.HasPrefix(pre, "%") && w[:len(pre)] != pre {
			continue
		}
		rest := w[len(pre):]
		if !strings.HasSuffix(pre, "}") && !strings.HasSuffix(pre, "%") && rest != "" && rest[0] != '/' && rest[0] != '\\' {
			continue
		}
		val := env(name)
		if val == "" && (name == "HOME" || name == "USERPROFILE") {
			val = home
		}
		if val == "" {
			return w
		}
		return val + rest
	}
	return w
}

// jazzPrivateShellWords returns the readings of cmd the guard inspects, each
// a list of word lists (one per command segment). win adds the Windows readings: backslashes are path
// separators for cmd and PowerShell (the POSIX parser would eat them as
// escapes, even before the segment reaches us), so the line is also parsed
// with them turned into slashes.
func jazzPrivateShellWords(cmd string, win bool) [][][]string {
	var out [][][]string
	readings := []string{cmd}
	if win && strings.Contains(cmd, `\`) {
		readings = append(readings, winSlashes(cmd))
	}
	for _, c := range readings {
		var r [][]string
		for _, seg := range shellParse(c).segs {
			r = append(r, stripWrappers(shellFields(seg)))
		}
		out = append(out, r)
	}
	// shellParse hands back segments re-joined without their quotes, so a
	// quoted path with a space ("C:\Users\John Smith\...") falls apart there;
	// this last reading splits the raw line itself and keeps quoted words whole
	raw := cmd
	if win {
		raw = winSlashes(cmd)
	}
	var r [][]string
	for _, seg := range winSplit.Split(raw, -1) {
		t := shellFields(seg)
		for len(t) > 0 && (t[0] == "&" || t[0] == ".") { // PowerShell call operators
			t = t[1:]
		}
		r = append(r, stripWrappers(t))
	}
	return append(out, r)
}

// jazzPrivateToolAccess is a hard boundary for model-facing file and shell
// tools. It does not claim to parse arbitrary shell programs; OS sandboxing
// remains the containment layer for commands the parser cannot understand.
func (a *Agent) jazzPrivateToolAccess(tool string, args map[string]any, cmd string) string {
	if tool == "read" || tool == "write" || tool == "edit" {
		if p, _ := argStr(args, "path"); p != "" {
			p = a.tools.abs(p)
			if jazzPrivateStatePath(p) {
				return p
			}
		}
	}
	if tool != "bash" || cmd == "" {
		return ""
	}
	for _, segs := range jazzPrivateShellWords(cmd, runtime.GOOS == "windows") {
		if p := jazzPrivateInSegs(segs, a.fenceCwd()); p != "" {
			return p
		}
	}
	return ""
}

// jazzPrivateInSegs returns the first private-state path one reading of a
// command names, following cd/pushd/Set-Location from cwd.
func jazzPrivateInSegs(segs [][]string, cwd string) string {
	for _, t := range segs {
		if len(t) == 0 {
			continue
		}
		w := strings.ToLower(filepath.Base(t[0]))
		if (w == "cd" || w == "pushd" || w == "chdir" || w == "set-location" || w == "sl") && len(t) > 1 {
			if p := jazzPrivatePathArg(t[1], cwd); p != "" {
				if jazzPrivateStatePath(p) {
					return p
				}
				cwd = p
			}
			continue
		}
		for _, raw := range t[1:] {
			raw = reFenceRedir.ReplaceAllString(raw, "")
			if p := jazzPrivatePathArg(raw, cwd); p != "" && jazzPrivateStatePath(p) {
				return p
			}
		}
	}
	return ""
}

// jazzSensitiveEgress reports an outbound command that also names a local
// protected/secret input. It deliberately prompts on uncertain heuristics.
func jazzSensitiveEgress(cmd string, root, cwd string) bool {
	if _, ok := EgressTarget(cmd); !ok {
		return false
	}
	for _, segs := range jazzPrivateShellWords(cmd, runtime.GOOS == "windows") {
		for _, t := range segs {
			if len(t) == 0 {
				continue
			}
			for _, raw := range t[1:] {
				if strings.HasPrefix(raw, "-") && !strings.Contains(raw, "=") {
					continue
				}
				p := jazzPrivatePathArg(raw, cwd)
				if p == "" || strings.Contains(p, "://") {
					continue
				}
				if protectedPath(p, root) || secretPath(resolvePath(p)) {
					return true
				}
			}
		}
	}
	return false
}
