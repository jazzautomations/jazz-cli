package main

import (
	"path/filepath"
	"strings"
)

// Foreground guard (ported from Hermes Agent, tools/terminal_tool_guards.py):
// a server or a never-ending command in the foreground pins the bash tool
// until its timeout — minutes of a slow machine and a free-tier clock. A
// server is started as a background job instead (one step for the model); a
// follower (ping without -c, tail -f, watch) is refused with the bounded form.

// fgKind: "server", "follow" or "" for a command line.
func fgKind(cmd string) (kind, why string) {
	if strings.Contains(cmd, "--help") || strings.Contains(cmd, "--version") {
		return "", ""
	}
	// shellSegments drops wrappers like timeout: look at the raw words first
	for _, w := range shellFields(cmd) {
		if b := filepath.Base(w); b == "timeout" || b == "nohup" || b == "setsid" {
			return "", "" // bounded or already detached by the user
		}
	}
	for _, seg := range shellSegments(cmd) {
		f := stripWrappers(shellFields(seg))
		for len(f) > 0 && (f[0] == "sudo" || f[0] == "env" || strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-")) {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		w := filepath.Base(f[0])
		args := f[1:]
		has := func(xs ...string) bool {
			for _, a := range args {
				for _, x := range xs {
					if a == x {
						return true
					}
				}
			}
			return false
		}
		hasPrefix := func(p string) bool {
			for _, a := range args {
				if strings.HasPrefix(a, p) {
					return true
				}
			}
			return false
		}
		switch w {
		case "python", "python3":
			if len(args) >= 2 && args[0] == "-m" && (args[1] == "http.server" || args[1] == "uvicorn" || args[1] == "flask" && has("run")) {
				return "server", "python3 -m " + args[1]
			}
			if has("runserver") {
				return "server", "manage.py runserver"
			}
		case "uvicorn", "gunicorn", "hypercorn", "daphne":
			return "server", w
		case "flask":
			if has("run") {
				return "server", "flask run"
			}
		case "npm", "yarn", "pnpm", "bun":
			if has("dev", "start", "serve", "preview") && !has("test") {
				return "server", w + " " + strings.Join(args, " ")
			}
		case "next", "vite", "nuxt", "astro":
			if len(args) == 0 || has("dev", "preview", "start") {
				return "server", w
			}
		case "rails":
			if has("s", "server") {
				return "server", "rails server"
			}
		case "php":
			if has("-S") {
				return "server", "php -S"
			}
		case "hugo":
			if has("server", "serve") {
				return "server", "hugo server"
			}
		case "jekyll":
			if has("serve", "s") {
				return "server", "jekyll serve"
			}
		case "docker", "podman":
			if len(args) >= 2 && args[0] == "compose" && args[1] == "up" && !has("-d", "--detach") {
				return "server", w + " compose up"
			}
		case "docker-compose":
			if has("up") && !has("-d", "--detach") {
				return "server", "docker-compose up"
			}
		case "ping", "ping6":
			if !hasPrefix("-c") && !has("--count") && !hasPrefix("-w") {
				return "follow", "ping without -c"
			}
		case "tail":
			if has("-f", "-F", "--follow") || hasPrefix("--follow") {
				return "follow", "tail -f"
			}
		case "journalctl":
			if has("-f", "--follow") {
				return "follow", "journalctl -f"
			}
		case "watch", "top", "htop":
			if w != "top" || !has("-b", "-n") {
				return "follow", w
			}
		}
	}
	return "", ""
}

const fgFollowMsg = "Refused: `%s` never ends on its own, so it would hold the shell until the timeout. Use a bounded form instead (ping -c 4, tail -n 50, journalctl -n 50 --no-pager, top -b -n 1), or run it with background:true and read it with the job tool."
