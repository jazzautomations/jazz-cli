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
	if strings.HasPrefix(w, "@") {
		w = strings.TrimPrefix(w, "@")
	}
	home, _ := os.UserHomeDir()
	for _, pair := range [][2]string{{"${HOME}", home}, {"$HOME", home}, {"${JAZZ_HOME}", os.Getenv("JAZZ_HOME")}, {"$JAZZ_HOME", os.Getenv("JAZZ_HOME")}, {"${XDG_CONFIG_HOME}", os.Getenv("XDG_CONFIG_HOME")}, {"$XDG_CONFIG_HOME", os.Getenv("XDG_CONFIG_HOME")}, {"${XDG_DATA_HOME}", os.Getenv("XDG_DATA_HOME")}, {"$XDG_DATA_HOME", os.Getenv("XDG_DATA_HOME")}} {
		if pair[1] != "" && strings.HasPrefix(w, pair[0]) {
			w = pair[1] + w[len(pair[0]):]
			break
		}
	}
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
	cwd := a.fenceCwd()
	var segs [][]string
	for _, seg := range shellParse(cmd).segs {
		segs = append(segs, stripWrappers(shellFields(seg)))
		if runtime.GOOS == "windows" && strings.Contains(seg, `\`) {
			// C:\Users\... : cmd/pwsh (and a model that thinks so) keep the
			// backslash literal, the POSIX split would eat it; check both readings
			segs = append(segs, stripWrappers(shellFields(strings.ReplaceAll(seg, `\`, `\\`))))
		}
	}
	for _, t := range segs {
		if len(t) == 0 {
			continue
		}
		w := filepath.Base(t[0])
		if (w == "cd" || w == "pushd") && len(t) > 1 {
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
	for _, seg := range shellParse(cmd).segs {
		t := stripWrappers(shellFields(seg))
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
	return false
}
