package main

import (
	"path/filepath"
	"strings"
)

// ---------- execution provenance ----------
//
// A small model that has just ssh'd somewhere tends to keep "being there":
// the next local `uname -a` gets reported as the remote machine's. The
// harness knows where each command ran, so it says so in the tool result.
// Local results are tagged only after the session touched a remote host —
// zero noise until the ambiguity exists.

// pvSSHOptArgs are ssh/scp options that take a value (skipped with it).
var pvSSHOptArgs = map[string]bool{
	"-p": true, "-P": true, "-l": true, "-i": true, "-o": true, "-J": true, "-F": true,
	"-L": true, "-R": true, "-D": true, "-W": true, "-E": true, "-b": true, "-c": true,
	"-m": true, "-O": true, "-Q": true, "-S": true, "-w": true, "-B": true, "-I": true,
}

// pvRemoteTarget returns the user@host an ssh-class command targets, or ""
// when the command runs only locally. Looks at every simple-command segment
// (pipes, &&, subshells), so `cat x | ssh h 'tee y'` counts as remote.
func pvRemoteTarget(cmd string) string {
	for _, seg := range shellSegments(cmd) {
		f := shellFields(seg)
		for len(f) > 0 && strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-") { // VAR=x prefix
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		switch filepath.Base(f[0]) {
		case "ssh", "mosh", "autossh":
			f = f[1:]
		case "tailscale":
			if len(f) < 2 || f[1] != "ssh" {
				continue
			}
			f = f[2:]
		case "scp", "rsync", "sftp":
			if t := pvCopyTarget(f[1:]); t != "" {
				return t
			}
			continue
		default:
			continue
		}
		if t := pvHostArg(f); t != "" {
			return t
		}
	}
	return ""
}

// pvHostArg: the first non-option argument of an ssh argv.
func pvHostArg(f []string) string {
	for i := 0; i < len(f); i++ {
		a := f[i]
		switch {
		case a == "--":
			if i+1 < len(f) {
				return pvCleanTarget(f[i+1])
			}
			return ""
		case strings.HasPrefix(a, "-"):
			if pvSSHOptArgs[a] {
				i++
			}
		default:
			return pvCleanTarget(a)
		}
	}
	return ""
}

// pvCopyTarget: scp/rsync/sftp arguments of the form user@host:path.
func pvCopyTarget(f []string) string {
	for _, a := range f {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if i := strings.Index(a, ":"); i > 0 && !strings.Contains(a[:i], "/") {
			return pvCleanTarget(a[:i])
		}
	}
	return ""
}

func pvCleanTarget(s string) string {
	s = strings.TrimPrefix(s, "ssh://")
	if i := strings.IndexAny(s, ":/"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 || strings.ContainsAny(s, " \t\n'\"`$") {
		return ""
	}
	return s
}

// pvTag is the trailer appended to a bash result. remote = the target this
// command reached; seen = the session has reached a remote host before.
func pvTag(remote, local string, seen bool) string {
	switch {
	case remote != "":
		return " [ran on REMOTE host " + remote + " via ssh — this output describes " + remote + ", not the local machine" + pvLocalName(local) + "]"
	case seen && local != "":
		return " [ran LOCALLY on " + local + "]"
	}
	return ""
}

func pvLocalName(local string) string {
	if local == "" {
		return ""
	}
	return " (" + local + ")"
}

// pvLocalHost is the short name of the machine the harness runs on.
func pvLocalHost() string {
	if p := epGetLight(); p != nil && p.Host != "" {
		return p.Host
	}
	return ""
}
