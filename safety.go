package main

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// ---------- command segmentation ----------

// shellSegments splits a command line into simple-command segments, descending
// into $(…), `…`, <(…), >(…), subshells, heredocs fed to a shell, eval and
// `bash|sh -c '…'` bodies (shellparse.go). It is a heuristic tokenizer, not a
// full parser: good enough to find the command word and arguments of each
// segment. Use shellParse when the caller must know whether the command could
// be analysed at all.
func shellSegments(cmd string) []string { return shellParse(cmd).segs }

var reSplit = lazyRx(`\n|;|&&|\|\||\||&`)

func isShell(w string) bool {
	switch filepath.Base(w) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// shellFields splits on whitespace honouring simple quotes; quotes are removed.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	var q byte
	has := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			// $'ANSI-C': backslash escapes, \' does not close it
			has = true
			for i += 2; i < len(s) && s[i] != '\''; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						cur.WriteByte('\n')
					case 't':
						cur.WriteByte('\t')
					default:
						cur.WriteByte(s[i])
					}
					continue
				}
				cur.WriteByte(s[i])
			}
		case c == '\'' || c == '"':
			q, has = c, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			has = true
		case c == ' ' || c == '\t':
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if cur.Len() > 0 || has {
		out = append(out, cur.String())
	}
	return out
}

// shKeyword: reserved words that precede a command (if cmd; then cmd; fi).
var shKeyword = map[string]bool{"if": true, "then": true, "elif": true, "else": true, "do": true, "while": true, "until": true, "!": true, "{": true, "(": true}

// stripWrappers removes leading env assignments and wrapper commands, keeping a
// "sudo" marker visible to the caller via the returned slice's first element.
func stripWrappers(t []string) []string {
	for len(t) > 0 {
		w := filepath.Base(t[0])
		switch {
		case shKeyword[t[0]]:
			t = t[1:]
		case strings.Contains(t[0], "=") && !strings.HasPrefix(t[0], "-") && !strings.HasPrefix(t[0], "="):
			t = t[1:]
		case w == "env":
			t = t[1:]
			for len(t) > 0 && (strings.HasPrefix(t[0], "-") || strings.Contains(t[0], "=")) {
				t = t[1:]
			}
		case w == "nice" || w == "nohup" || w == "time" || w == "exec" || w == "command" || w == "builtin" || w == "stdbuf" || w == "ionice" || w == "timeout" || w == "xargs":
			t = t[1:]
			for len(t) > 0 && strings.HasPrefix(t[0], "-") {
				t = t[1:]
			}
			if w == "timeout" && len(t) > 0 { // duration argument
				t = t[1:]
			}
		default:
			return t
		}
	}
	return t
}

// ---------- danger detection ----------

// rePWDTarget: the current directory spelled through the shell.
var rePWDTarget = lazyRx(`^(\$PWD|\$\{PWD\}|\$\(pwd\)|` + "`pwd`" + `)(/|/\*|/\.)?$`)

// rmHomeOrRoot flags a recursive rm target that is the home directory, the
// project (current) directory, an ancestor of either, or a .git directory.
func rmHomeOrRoot(tg string) string {
	p := expandTilde(tg)
	if strings.ContainsAny(p, "$`*?[") {
		return ""
	}
	cwd, _ := os.Getwd()
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return ""
		}
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	if filepath.Base(p) == ".git" {
		return "apaga o histórico do git (" + tg + ")"
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{home, cwd} {
		if d != "" && pathUnder(filepath.Clean(d), p) {
			return "apaga a home ou a pasta do projeto (" + tg + ")"
		}
	}
	return ""
}

// expandTilde turns ~ and ~/x into the home directory.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

var broadTarget = lazyRx(`^(/|/\*|~|~/|~/\*|\$HOME|\$HOME/|\$HOME/\*|\$\{HOME\}|\$\{HOME\}/|\$\{HOME\}/\*|\.|\./|\./\*|\.\.|\.\./?.*|\*|/(bin|boot|dev|etc|home|lib|lib32|lib64|opt|proc|root|sbin|srv|sys|usr|var)(/|/\*)?)$`)

var (
	rePipeShell   = lazyRx(`\|\s*(sudo\s+)?(ba|z|da|k)?sh\b|\|\s*(sudo\s+)?(python3?|perl|ruby|node)\s*(-\s*)?($|[;&|])`)
	reShellRemote = lazyRx(`\b((ba|z|da|k)?sh|source|\.|eval|python[0-9.]*|perl|ruby|node|php)\s+(-c\s+)?["']?(\$\(|<\(|` + "`" + `)\s*(curl|wget|fetch|http)\b`)
	reB64Shell    = lazyRx(`(base64\s+(-d|--decode|-D)|xxd\s+-r|openssl\s+(base64|enc)\b[^|]*\s-d)[^|]*\|\s*(sudo\s+)?((ba|z|da|k)?sh|python[0-9.]*|perl|ruby|node|php)\b`)
	reXargsRm     = lazyRx(`\bxargs\b[^|;&]*\brm\s+(-\w*[rR]|--recursive)`)
	reForkBomb    = lazyRx(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}`)
	reDotfileW    = lazyRx(`(>|>>)\s*["']?(~|\$HOME|\$\{HOME\})/\.[A-Za-z]|\btee\s+(-a\s+)?["']?(~|\$HOME|\$\{HOME\})/\.[A-Za-z]`)
	reSecretFile  = lazyRx(`auth\.json|\.ssh/|authorized_keys|\.gnupg|\.aws/credentials|\.netrc|\.git-credentials|jazz-cli/(config|mcp|trusted)\.json`)
	reExfilVerb   = lazyRx(`\b(curl|wget|nc|ncat|scp|rsync|sftp|ftp)\b|(>|>>)\s*\S|\btee\b|\bsed\s+-i|\bcp\b|\bmv\b`)
	reDevTarget   = lazyRx(`^of=/dev/(sd|hd|vd|xvd|nvme|mmcblk|disk|loop|md|dm-|mapper/)`)
	reRawDisk     = lazyRx(`>\s*/dev/(sd|hd|vd|xvd|nvme|mmcblk|disk)`)
)

// DangerReason returns a short reason when a command is destructive or escalates
// privileges, or "" when it looks ordinary. Dangerous commands always need confirmation.
func dangerReasonSh(cmd string) string {
	if r := dangerWholeSh(cmd); r != "" {
		return r
	}
	pr := shellParse(cmd)
	if !pr.ok {
		return reUnparsable
	}
	if r := downloadExecReason(pr.segs); r != "" {
		return r
	}
	for _, seg := range pr.segs {
		if r := segmentDanger(shellFields(seg)); r != "" {
			return r
		}
	}
	for _, code := range pr.code {
		if r := inlineCodeDanger(code); r != "" {
			return r
		}
	}
	return ""
}

// dangerWholeSh runs the detectors that look at the whole command line
// (pipes into a shell, raw disk writes, dotfile overwrites …) rather than at
// one simple command. turbo.go uses it to tell those apart from per-segment
// verdicts.
func dangerWholeSh(cmd string) string {
	if reForkBomb.MatchString(cmd) {
		return "fork bomb"
	}
	if r := h2WholeDanger(cmd); r != "" { // hardening2.go
		return r
	}
	if rePipeShell.MatchString(cmd) && regexp.MustCompile(`\b(curl|wget|base64|nc)\b`).MatchString(cmd) || reShellRemote.MatchString(cmd) || reB64Shell.MatchString(cmd) {
		return "executa script baixado/decodificado direto no shell"
	}
	if reRawDisk.MatchString(cmd) {
		return "escreve direto em disco"
	}
	if reDotfileW.MatchString(cmd) {
		return "sobrescreve arquivo de configuração do usuário"
	}
	if reSecretFile.MatchString(cmd) && reExfilVerb.MatchString(cmd) {
		return "mexe em credenciais ou config sensível"
	}
	if reXargsRm.MatchString(cmd) {
		return "apaga arquivos vindos da entrada (xargs rm -r)"
	}
	return ""
}

const reUnparsable = "comando que não consigo analisar (aspas, parênteses ou heredoc sem fechar)"

// downloadExecReason flags download-then-execute: a segment saves a URL to a
// file (curl -o/-O, wget [-O]) and a later one runs that file (sh f, ./f,
// source f, python f).
func downloadExecReason(segs []string) string {
	saved := map[string]bool{}
	for _, seg := range segs {
		t := stripWrappers(shellFields(seg))
		if len(t) == 0 {
			continue
		}
		w := filepath.Base(t[0])
		if w == "curl" || w == "wget" {
			remoteName := false
			for i := 1; i < len(t); i++ {
				a := t[i]
				switch {
				case (w == "curl" && (a == "-o" || a == "--output")) || (w == "wget" && (a == "-O" || a == "--output-document")):
					if i+1 < len(t) {
						saved[filepath.Base(t[i+1])] = true
						i++
					}
				case strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--output-document="):
					_, v, _ := strings.Cut(a, "=")
					saved[filepath.Base(v)] = true
				case w == "curl" && (a == "-O" || a == "--remote-name" || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "O")):
					remoteName = true
				case strings.Contains(a, "://") && (remoteName || w == "wget"):
					if b := filepath.Base(strings.SplitN(a, "?", 2)[0]); b != "" && b != "." && b != "/" {
						saved[b] = true
					}
				}
			}
			continue
		}
		if len(saved) == 0 {
			continue
		}
		cand := []string{t[0]}
		if isShell(w) || reInterp.MatchString(w) || w == "source" || w == "." || w == "exec" {
			for _, a := range t[1:] {
				if !strings.HasPrefix(a, "-") {
					cand = append(cand, a)
					break
				}
			}
		}
		for i, c := range cand {
			if saved[filepath.Base(c)] && (i > 0 || strings.Contains(c, "/")) {
				return "baixa e executa um script (download seguido de execução)"
			}
		}
	}
	return ""
}

var (
	reCodeSpawn  = lazyRx(`os\.system|os\.popen|os\.exec\w*|subprocess|pty\.spawn|commands\.getoutput|child_process|execSync|spawnSync|\bexecFile|\bspawn\(|\bsystem\s*\(|\bexec\s*\(|` + "`" + `|\bqx[{(/]|Open3|IO\.popen|Kernel\.exec|shell_exec|passthru|proc_open`)
	reCodeNet    = lazyRx(`urllib|urlopen|requests\.(get|post)|http\.client|httpx|\bfetch\(|https?\.(get|request)|axios|LWP|HTTP::Tiny|Net::HTTP|open-uri|URI\.open|file_get_contents\(\s*["']https?:|socket\.|curl_exec`)
	reCodeEval   = lazyRx(`\bexec\s*\(|\beval\s*\(|new\s+Function\(|\bFunction\(|\bcompile\(|instance_eval|marshal\.loads|pickle\.loads`)
	reCodeRm     = lazyRx(`shutil\.rmtree|os\.(remove|unlink|rmdir|removedirs)|fs\.(rm|rmdir|unlink)(Sync)?\(|rm_rf|rmtree|FileUtils\.rm|File\.delete|\bunlink\s*\(`)
	reCodeBroad  = lazyRx(`expanduser\(\s*["']~/?["']\s*\)|["']~/?["']|["']/["']|Path\.home\(\)|os\.homedir\(\)|\$HOME|ENV\[["']HOME["']\]|getenv\(["']HOME["']\)|environ\[["']HOME["']\]`)
	reCodeSecret = lazyRx(`auth\.json|\.ssh|id_rsa|id_ed25519|\.aws|credentials|/proc/[^"']*environ|os\.environ|process\.env|ENV\[|getenv|\.netrc|\.git-credentials|\.env\b`)
	reCodeStr    = lazyRx(`"((?:[^"\\]|\\.){2,400})"|'((?:[^'\\]|\\.){2,400})'`)
)

// inlineCodeDanger scans interpreter code given on the command line (python
// -c, node -e, heredoc to python, awk programs) for the patterns DangerReason
// flags in shell: dangerous commands passed to os.system/subprocess,
// recursive deletes of home or root, download-and-exec, and secrets sent over
// the network.
func inlineCodeDanger(code string) string {
	if reCodeSpawn.MatchString(code) {
		for _, m := range reCodeStr.FindAllStringSubmatch(code, 32) {
			lit := m[1]
			if lit == "" {
				lit = m[2]
			}
			if r := DangerReason(lit); r != "" && r != reUnparsable {
				return "código inline roda comando perigoso: " + r
			}
		}
	}
	if reCodeRm.MatchString(code) && reCodeBroad.MatchString(code) {
		return "código inline apaga a home ou a raiz"
	}
	net := reCodeNet.MatchString(code)
	if net && (reCodeEval.MatchString(code) || reCodeSpawn.MatchString(code)) {
		return "código inline baixa e executa código"
	}
	if net && reCodeSecret.MatchString(code) {
		return "código inline envia credenciais pela rede"
	}
	return ""
}

func segmentDanger(t []string) string {
	t = stripWrappers(t)
	if len(t) == 0 {
		return ""
	}
	w := filepath.Base(t[0])
	args := t[1:]
	if r := h2SegmentDanger(w, args); r != "" { // hardening2.go
		return r
	}
	if strings.HasPrefix(w, "mkfs") {
		return "formata disco"
	}
	switch w {
	case "sudo", "doas", "pkexec":
		return "sudo/privilégio (pede senha e trava sem terminal)"
	case "su":
		return "troca de usuário (su)"
	case "rm":
		recursive, endFlags := false, false
		var targets []string
		for _, a := range args {
			if !endFlags && a == "--" {
				endFlags = true
				continue
			}
			if !endFlags && strings.HasPrefix(a, "--") {
				if a == "--recursive" {
					recursive = true
				}
				continue
			}
			if !endFlags && strings.HasPrefix(a, "-") && len(a) > 1 {
				if strings.ContainsAny(a, "rR") {
					recursive = true
				}
				continue
			}
			targets = append(targets, a)
		}
		for _, tg := range targets {
			if broadTarget.MatchString(tg) && (recursive || tg == "/" || strings.HasPrefix(tg, "~") || strings.Contains(tg, "HOME")) {
				return "apaga diretório amplo (" + tg + ")"
			}
			if !recursive {
				continue
			}
			if rePWDTarget.MatchString(tg) {
				return "apaga a pasta do projeto (" + tg + ")"
			}
			if r := rmHomeOrRoot(tg); r != "" {
				return r
			}
		}
	case "find":
		hasDel := false
		for i, a := range args {
			if a == "-delete" || (a == "-exec" || a == "-execdir" || a == "-ok") && i+1 < len(args) && filepath.Base(args[i+1]) == "rm" {
				hasDel = true
			}
		}
		if hasDel {
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					break
				}
				if broadTarget.MatchString(a) && a != "." && a != "./" {
					return "apaga arquivos em massa fora do projeto (find -delete)"
				}
			}
		}
	case "shred":
		return "destrói arquivos (shred)"
	case "wipefs", "mke2fs":
		return "formata disco"
	case "fdisk", "parted", "sfdisk", "gdisk":
		for _, a := range args {
			if a == "-l" || a == "--list" {
				return ""
			}
		}
		return "particiona disco"
	case "dd":
		for _, a := range args {
			if reDevTarget.MatchString(a) {
				return "escreve direto em dispositivo"
			}
		}
	case "shutdown", "reboot", "poweroff", "halt":
		return "desliga/reinicia a máquina"
	case "init", "telinit":
		if len(args) > 0 && (args[0] == "0" || args[0] == "6") {
			return "desliga/reinicia a máquina"
		}
	case "systemctl":
		for _, a := range args {
			switch a {
			case "poweroff", "reboot", "halt", "kexec":
				return "desliga/reinicia a máquina"
			}
		}
	case "chmod", "chown", "chgrp":
		rec := false
		for _, a := range args {
			if a == "-R" || a == "--recursive" || strings.HasPrefix(a, "-") && strings.Contains(a, "R") {
				rec = true
			}
		}
		if rec {
			for _, a := range args {
				if !strings.HasPrefix(a, "-") && broadTarget.MatchString(a) && a != "." && a != "./" {
					return "permissões recursivas em diretório amplo"
				}
			}
		}
	case "mv":
		for _, a := range args {
			if a == "/" || a == "~" || a == "~/" || a == "$HOME" || a == "${HOME}" {
				return "move o diretório home/raiz"
			}
		}
	case "killall":
		return "mata processos em massa"
	case "pkill":
		for _, a := range args {
			if a == "-9" || a == "-KILL" {
				return "mata processos em massa"
			}
		}
	case "kill":
		if len(args) >= 2 && (args[0] == "-9" || args[0] == "-KILL") && args[1] == "-1" {
			return "mata todos os processos"
		}
	case "crontab":
		for _, a := range args {
			if a == "-r" {
				return "apaga o crontab"
			}
		}
	case "git":
		return gitDanger(args)
	}
	return ""
}

func gitDanger(args []string) string {
	// skip global options like -C dir, -c k=v
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-C" || args[0] == "-c") && len(args) > 1 {
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return ""
	}
	sub, rest := args[0], args[1:]
	has := func(xs ...string) bool {
		for _, a := range rest {
			for _, x := range xs {
				if a == x {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "push":
		if has("--force", "-f", "--force-with-lease", "--mirror", "--delete", "-d") {
			return "git push forçado"
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") && len(a) > 1 {
				return "git push forçado"
			}
		}
	case "reset":
		if has("--hard", "--merge", "--keep") {
			return "descarta mudanças do git (reset --hard)"
		}
	case "clean":
		for _, a := range rest {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f") || a == "--force" {
				return "apaga arquivos não versionados (git clean)"
			}
		}
	case "checkout":
		if has("--") && (has(".") || has("*")) || has("-f", "--force") {
			return "descarta mudanças do git (checkout)"
		}
	case "restore":
		if has(".", "*", ":/") && !has("--staged") {
			return "descarta mudanças do git (restore)"
		}
	case "branch":
		if has("-D", "--delete") || has("-d") && has("--force", "-f") {
			return "apaga branch do git"
		}
	case "stash":
		if len(rest) > 0 && (rest[0] == "clear" || rest[0] == "drop") {
			return "apaga stash do git"
		}
	case "filter-branch", "filter-repo":
		return "reescreve o histórico do git"
	case "update-ref":
		if has("-d") {
			return "apaga ref do git"
		}
	}
	return ""
}

// ---------- read-only allowlist (used by "ask" and plan modes) ----------

var (
	readOnlyCmd    = lazyRx(`^(ls|ll|la|cat|head|tail|wc|pwd|echo|printf|which|type|file|stat|du|df|tree|rg|grep|egrep|fgrep|fd|fdfind|find|sort|uniq|cut|diff|cmp|whoami|uname|id|uptime|free|ps|jq|realpath|readlink|dirname|basename|nproc|true|false|test|\[|sha256sum|md5sum|cksum|column|tr|nl|seq|go\s+(version|env|list|doc|vet)|python3?\s+(--version|-V)|node\s+(--version|-v)|git\s+(status|diff|log|show|rev-parse|ls-files|blame|describe|shortlog|grep|ls-tree|cat-file)|git\s+branch(\s+(-a|-r|-v|-vv|-l|--list|--show-current|--all|--remotes))*|git\s+tag(\s+(-l|--list))?|git\s+remote(\s+(-v|show|get-url)(\s+\S+)?)?)(\s|$)`)
	readOnlyBadArg = lazyRx(`(^|\s)(-o|--output|--output=\S*|-fprint\w*|-fls|-ok\w*|-exec\w*|-delete|--pre(=\S*)?|-toolexec|-vettool\S*|--exec\S*|-w|--write)(\s|$)`)
)

var (
	reGitBranchList = lazyRx(`^git\s+branch(\s+(-a|-r|-v|-vv|-l|--list|--show-current|--all|--remotes|--merged|--no-merged|--contains|--sort=\S+|--format=\S+))*\s*$`)
	reGitTagList    = lazyRx(`^git\s+tag(\s+(-l|--list|-n\d*|--sort=\S+|--contains)(\s+[^-\s]\S*)?)*\s*$`)
)

// isReadOnlySh is a conservative check: true only for plainly read-only
// commands (POSIX shell syntax). IsReadOnly (winsafety.go) wraps it.
func isReadOnlySh(cmd string) bool {
	if strings.ContainsAny(cmd, "\n\r(){}`$>") || strings.Contains(cmd, "<<") || strings.Contains(cmd, "<(") {
		return false
	}
	if readOnlyBadArg.MatchString(cmd) || regexp.MustCompile(`\bsort\b[^|;&]*\s-o`).MatchString(cmd) {
		return false
	}
	for _, part := range reSplit.Split(cmd, -1) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if !readOnlyCmd.MatchString(p) {
			return false
		}
		// git subcommands with mutating flags
		if strings.HasPrefix(p, "git ") && regexp.MustCompile(`\s(-d|-D|-m|-M|-f|--delete|--force|add|remove|rm|rename|set-url|prune)(\s|$)`).MatchString(p) {
			return false
		}
		// git branch/tag with a name creates one; only listing forms are read-only
		if strings.HasPrefix(p, "git branch") && !reGitBranchList.MatchString(p) || strings.HasPrefix(p, "git tag") && !reGitTagList.MatchString(p) {
			return false
		}
		// uniq/tree with an output file argument
		if regexp.MustCompile(`^(uniq\s+\S+\s+\S+|tree\b.*\s-o)`).MatchString(p) {
			return false
		}
		if toks := shellFields(p); roUnsafeArgs(toks) || roSecretArgs(toks) {
			return false
		}
	}
	return true
}

// roUnsafeArgs reports options of allowlisted commands that run a program or
// write a file: git's pager/diff drivers, rg's preprocessors, fd's exec, and
// sort/tree output files (GNU getopt and git accept abbreviated long options,
// so prefixes are matched).
func roUnsafeArgs(toks []string) bool {
	if len(toks) == 0 {
		return false
	}
	hasPre := func(a string, pres ...string) bool {
		for _, p := range pres {
			if strings.HasPrefix(a, p) {
				return true
			}
		}
		return false
	}
	w := filepath.Base(toks[0])
	for _, a := range toks[1:] {
		if a == "--" {
			break
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		long := strings.HasPrefix(a, "--")
		short := a[1:] // the cluster of a short option, e.g. "uo" in -uo
		switch w {
		case "git":
			if long && hasPre(a, "--op", "--ext", "--textc", "--upload", "--exec", "--out", "--filters") || !long && strings.ContainsRune(short, 'O') {
				return true
			}
		case "rg":
			if long && hasPre(a, "--pre", "--hostname-bin") {
				return true
			}
		case "fd", "fdfind":
			if long && hasPre(a, "--exec") || !long && strings.ContainsAny(short, "xX") {
				return true
			}
		case "sort":
			if long && hasPre(a, "--o", "--co") || !long && strings.ContainsRune(short, 'o') {
				return true
			}
		case "tree":
			if !long && strings.ContainsRune(short, 'o') || long && hasPre(a, "--o") {
				return true
			}
		}
	}
	return false
}

// reSecretArg matches arguments that name credential files, so `cat` and
// friends of those paths are not treated as read-only (they ask in ask mode
// and are refused in plan mode and by the research subagent).
var reSecretArg = lazyRx(`(^|/)\.ssh(/|$)|(^|/)\.gnupg(/|$)|(^|/)\.aws/|\.netrc$|\.git-credentials$|\.pgpass$|(^|/)\.npmrc$|(^|/)\.pypirc$|authorized_keys|(^|/)id_(rsa|dsa|ecdsa|ed25519)(_sk)?$|jazz-cli/(auth|config|mcp|trusted)\.json|jazz-cli/[^/]*[*?\[]|\.codex/auth\.json|\.credentials\.json$|(^|/)\.kube/|/etc/(shadow|gshadow|sudoers)|/proc/[^/]+/environ$|(^|/)\.git/config$|\.(pem|p12|pfx)$`)

// roSecretArgs reports whether any argument (or --opt=value) names a secret
// file. $HOME/~ are expanded and globs (cat ~/.config/jazz-cli/au*) are
// matched against the file system, so a pattern cannot hide the target.
func roSecretArgs(toks []string) bool {
	if len(toks) < 2 {
		return false
	}
	home, _ := os.UserHomeDir()
	for _, a := range toks[1:] {
		if i := strings.IndexByte(a, '='); i > 0 && strings.HasPrefix(a, "-") {
			a = a[i+1:]
		} else if strings.HasPrefix(a, "-") {
			continue
		}
		if a == "" {
			continue
		}
		if home != "" {
			for _, h := range []string{"${HOME}", "$HOME"} {
				if strings.HasPrefix(a, h) {
					a = home + a[len(h):]
				}
			}
		}
		if reSecretArg.MatchString(a) || secretName(filepath.Base(a)) {
			return true
		}
		p := expandTilde(a)
		if !filepath.IsAbs(p) && !strings.HasPrefix(a, ".") && !strings.ContainsAny(a, "*?[") {
			continue
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if secretPath(ap) {
			return true
		}
		if strings.ContainsAny(ap, "*?[") {
			ms, _ := filepath.Glob(ap)
			for i, m := range ms {
				if i >= 64 {
					break
				}
				if secretPath(m) {
					return true
				}
			}
		}
	}
	return false
}

// secretName reports file names that hold secrets wherever they live: dotenv
// files (not the .example/.sample templates), PEM/PKCS#12 bundles and SSH
// private keys (not the .pub half).
func secretName(base string) bool {
	b := strings.ToLower(base)
	if b == ".env" || strings.HasPrefix(b, ".env.") {
		switch strings.TrimPrefix(b, ".env.") {
		case "example", "sample", "template", "dist", "defaults", "schema":
			return false
		}
		return true
	}
	if strings.HasSuffix(b, ".pem") || strings.HasSuffix(b, ".p12") || strings.HasSuffix(b, ".pfx") {
		return true
	}
	for _, k := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"} {
		if strings.HasPrefix(b, k) && !strings.HasSuffix(b, ".pub") {
			return true
		}
	}
	return false
}

// SecretReason returns why a bash command would read credentials (".env",
// ~/.ssh, auth.json, /proc/<pid>/environ, .git/config …), or "". In every
// mode except yolo such a command is confirmed: its output goes to the model
// provider.
func secretReasonSh(cmd string) string {
	if reSecretFile.MatchString(cmd) {
		return "lê credenciais ou config sensível"
	}
	for _, seg := range shellSegments(cmd) {
		if roSecretArgs(stripWrappers(shellFields(seg))) {
			return "lê arquivo sensível (credenciais)"
		}
	}
	if reProcEnviron.MatchString(cmd) {
		return "lê o ambiente de um processo (chaves)"
	}
	return ""
}

var reProcEnviron = lazyRx(`/proc/[^/\s]+/environ`)

var (
	reUploadFlag = lazyRx(`(^|\s)(-d|--data|--data-\S+|-F|--form|-T|--upload-file|--post-file)(\s|=|$)`)
	reHostPort   = lazyRx(`^(\[[0-9A-Fa-f:]+\]|[0-9A-Za-z._-]+)`)
)

// EgressTarget returns the destination host of a command that sends data out of
// the machine (curl/wget upload, nc/ncat to a remote, scp/rsync/sftp to a
// remote, an interpreter one-liner that POSTs), or "" when the command sends
// nothing out. The host lets the caller show where data would go and check an
// allow-list. Localhost/loopback targets return "" (not egress off the box).
func EgressTarget(cmd string) (host string, isEgress bool) {
	for _, seg := range shellSegments(cmd) {
		t := stripWrappers(shellFields(seg))
		if len(t) == 0 {
			continue
		}
		w := filepath.Base(t[0])
		switch {
		case w == "curl" || w == "wget":
			if !reUploadFlag.MatchString(seg) {
				continue // a plain download is not egress
			}
			for _, a := range t[1:] {
				if h := egHostOfURL(a); h != "" && !egLocal(h) {
					return h, true
				}
			}
		case w == "nc" || w == "ncat" || w == "netcat" || w == "socat" || w == "telnet":
			for _, a := range t[1:] {
				if strings.HasPrefix(a, "-") || egIsPort(a) {
					continue
				}
				if h := egBareHost(a); h != "" && !egLocal(h) {
					return h, true
				}
			}
		case w == "scp" || w == "rsync" || w == "sftp":
			for _, a := range t[1:] {
				if strings.HasPrefix(a, "-") {
					continue
				}
				if h := egRemoteSpec(a); h != "" && !egLocal(h) {
					return h, true
				}
			}
		}
	}
	// interpreter one-liners that POST (python/node/ruby/perl/php -c/-e)
	pr := shellParse(cmd)
	for _, code := range pr.code {
		if reCodeNet.MatchString(code) && regexp.MustCompile(`(?i)\.post\(|method\s*=\s*["']POST["']|urlopen\([^)]*data=|urllib\.request\.Request|requests\.post|http\.request|fetch\([^)]*method|curl_setopt|\.write\(`).MatchString(code) {
			for _, h := range egURLsIn(code) {
				if !egLocal(h) {
					return h, true
				}
			}
			return "(host no código inline)", true
		}
	}
	return "", false
}

func egHostOfURL(a string) string {
	a = strings.Trim(a, `"'`)
	if !strings.Contains(a, "://") {
		return ""
	}
	return egBareHost(strings.SplitN(a, "://", 2)[1])
}

// egBareHost extracts host from host, host:port, user@host, or a URL remainder.
func egBareHost(a string) string {
	a = strings.Trim(a, `"'`)
	if i := strings.Index(a, "@"); i >= 0 {
		a = a[i+1:]
	}
	if i := strings.IndexAny(a, "/?#"); i >= 0 {
		a = a[:i]
	}
	m := reHostPort.FindString(a)
	return strings.TrimSuffix(strings.TrimPrefix(m, "["), "]")
}

// egRemoteSpec returns the host of an scp/rsync target (user@host:path or
// rsync://host/…); "" for a local path.
func egRemoteSpec(a string) string {
	if strings.Contains(a, "://") {
		return egHostOfURL(a)
	}
	i := strings.Index(a, ":")
	if i <= 0 || strings.Contains(a[:i], "/") {
		return "" // local path (no colon before a slash)
	}
	return egBareHost(a[:i])
}

func egIsPort(a string) bool {
	if a == "" {
		return false
	}
	for _, c := range a {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func egURLsIn(code string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`https?://[^\s"')]+`).FindAllString(code, 8) {
		if h := egHostOfURL(m); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// egLocal reports a loopback / link-local / this-machine host.
func egLocal(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"))
	switch h {
	case "localhost", "localhost.localdomain", "ip6-localhost", "", "0.0.0.0", "::1":
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return strings.HasSuffix(h, ".local")
}

// egressAllowed reports whether host is in the configured allow-list (exact or
// a parent domain, "*.example.com" style).
func egressAllowed(host string, allow []string) bool {
	h := strings.ToLower(host)
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == h || strings.HasSuffix(h, "."+strings.TrimPrefix(a, "*.")) || a == "*" {
			return true
		}
	}
	return false
}

// ---------- protected paths ----------

// pathEq compares two cleaned paths; macOS and Windows file systems are
// case-insensitive, so ~/.SSH is ~/.ssh there.
func pathEq(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathUnder reports whether p is dir or inside it (case-folded on macOS).
func pathUnder(p, dir string) bool {
	if dir == "" {
		return false
	}
	if pathEq(p, dir) {
		return true
	}
	pre := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(p), strings.ToLower(pre))
	}
	return strings.HasPrefix(p, pre)
}

// resolvePath resolves symlinks in the longest existing prefix of p, so a
// file that does not exist yet inside a symlinked directory still resolves.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for d := p; ; d = filepath.Dir(d) {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
	}
}

// pathIn reports whether the resolved path rp is dir or inside it, comparing
// both the plain and the symlink-resolved spelling of dir.
func pathIn(rp, dir string) bool {
	if dir == "" {
		return false
	}
	return pathUnder(rp, dir) || pathUnder(rp, resolvePath(dir))
}

// protectedPath reports whether p is a credential, shell-startup, PATH or
// policy location that the model must not change without an explicit
// confirmation (in every mode except yolo). root is the project root ("" =
// unknown): its .git config/hooks and jazz/Claude policy files are protected
// too, so the model cannot plant hooks or MCP servers the user later "trusts".
func protectedPath(p, root string) bool {
	rp := resolvePath(p)
	if pathIn(rp, configDir()) || pathIn(rp, filepath.Join(dataDir(), "sessions")) {
		return true
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, d := range []string{".ssh", ".gnupg", ".aws", ".config/autostart", ".claude", ".codex", ".docker", ".kube",
			".local/bin", ".config/systemd/user", ".config/fish", ".config/git", ".config/environment.d", "bin",
			"Library/LaunchAgents", "Library/LaunchDaemons", ".config/nvim", ".vim", ".cargo/bin", ".config/zsh", ".oh-my-zsh/custom"} {
			if pathIn(rp, filepath.Join(home, d)) {
				return true
			}
		}
		for _, f := range []string{".netrc", ".git-credentials", ".bashrc", ".bash_profile", ".profile", ".zshrc", ".zprofile", ".zshenv", ".bash_logout",
			".bash_aliases", ".bash_login", ".inputrc", ".gitconfig", ".pgpass", ".npmrc", ".pypirc", ".xinitrc", ".xprofile", ".xsession", ".pam_environment",
			".vimrc", ".gvimrc", ".tmux.conf", ".screenrc", ".cargo/config.toml", ".cargo/config", ".wgetrc", ".curlrc", ".gemrc", ".yarnrc", ".condarc", ".pip/pip.conf", ".config/pip/pip.conf"} {
			if pathIn(rp, filepath.Join(home, f)) {
				return true
			}
		}
	}
	if root != "" {
		// policy, auto-run and instruction files of the project: hooks and MCP
		// servers, direnv, editor tasks, submodules, and the AGENTS.md/skills
		// text that steers the next session (F9)
		for _, f := range []string{".jazz/settings.json", ".claude/settings.json", ".claude/settings.local.json", ".mcp.json", ".git/config", ".git/hooks",
			".envrc", ".vscode/tasks.json", ".vscode/settings.json", ".vscode/launch.json", ".gitmodules", ".jazz/skills", ".jazz/commands", ".jazz/steering",
			".jazz/scripts", ".jazz/intents.json", ".jazz/approvals.json", ".jazz/scan-allow.json", ".jazz/diffrules.d", ".claude/skills", ".claude/commands", ".claude/agents", ".kiro/steering", ".cursor/rules", ".idea/workspace.xml"} {
			if pathIn(rp, filepath.Join(root, f)) {
				return true
			}
		}
		if pathIn(rp, root) || pathIn(filepath.Clean(p), root) {
			switch filepath.Base(rp) {
			case "AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "JAZZ.md":
				return true
			}
		}
	}
	if pathIn(rp, "/etc") || pathIn(rp, "/boot") || pathIn(rp, "/usr") || pathIn(rp, "/var/spool/cron") {
		return true
	}
	if winIsWindows && winProtectedPath(rp) {
		return true
	}
	return false
}

// secretPath reports whether reading p would expose credentials (SSH/GPG
// keys, cloud and registry credentials, jazz's own auth and sessions). It is
// narrower than protectedPath: reading /usr/include or a skill is fine.
func secretPath(p string) bool {
	rp := resolvePath(p)
	for _, f := range []string{"auth.json", "config.json", "mcp.json", "trusted.json", "redact.key"} {
		if pathIn(rp, filepath.Join(configDir(), f)) {
			return true
		}
	}
	if pathIn(rp, filepath.Join(dataDir(), "sessions")) {
		return true
	}
	if home, _ := os.UserHomeDir(); home != "" {
		for _, f := range []string{".ssh", ".gnupg", ".aws", ".kube", ".password-store", ".docker/config.json", ".netrc", ".git-credentials",
			".pgpass", ".npmrc", ".pypirc", ".config/gh/hosts.yml", ".codex/auth.json", ".claude/.credentials.json", ".config/gcloud"} {
			if pathIn(rp, filepath.Join(home, f)) {
				return true
			}
		}
	}
	for _, f := range []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/ssh"} {
		if pathIn(rp, f) {
			return true
		}
	}
	if secretName(filepath.Base(rp)) || secretName(filepath.Base(p)) {
		return true
	}
	// .git/config can carry a token in a remote URL (actions/checkout does this)
	if strings.EqualFold(filepath.Base(rp), "config") && strings.EqualFold(filepath.Base(filepath.Dir(rp)), ".git") {
		return true
	}
	if strings.HasPrefix(rp, "/proc/") && filepath.Base(rp) == "environ" || reProcEnviron.MatchString(filepath.ToSlash(p)) {
		return true
	}
	if winIsWindows && winSecretPath(rp) {
		return true
	}
	return false
}

// ---------- secrets: env scrubbing and redaction ----------

// secretEnvNames: variables scrubbed from child environments and whose values
// are redacted. Generic suffixes (…_API_KEY, …_TOKEN, …_SECRET_KEY, …_DSN,
// …_WEBHOOK_URL), anything with SECRET in the name, provider prefixes, and
// well-known names (AWS session/secret, Stripe, database URLs, Postgres/MySQL
// passwords, GitHub/npm tokens, the SSH agent socket).
var secretEnvNames = lazyRx(`(?i)(^|_)(API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?|ACCESS_?KEY|SECRET_?KEY|PRIVATE_?KEY|SIGNING_?KEY|DSN|WEBHOOK(_URL)?|COOKIE)$|SECRET|^(OPENROUTER|ANTHROPIC|OPENAI|GROQ|OPENCODE|DEEPSEEK|GEMINI|GOOGLE_API|MISTRAL|XAI|COHERE|TOGETHER|FIREWORKS|HF|HUGGINGFACE|STRIPE)_|^(AWS_SESSION_TOKEN|DATABASE_URL|[A-Z0-9]+_DATABASE_URL|REDIS_URL|MONGO(DB)?_(URI|URL)|PGPASSWORD|PGPASSFILE|MYSQL_PWD|SSH_AUTH_SOCK|GITHUB_TOKEN|GH_TOKEN|GH_ENTERPRISE_TOKEN|NPM_TOKEN|NODE_AUTH_TOKEN|SLACK_[A-Z0-9_]*|TWILIO_AUTH_TOKEN|SENTRY_DSN|VAULT_TOKEN)$`)

// reCredURL: a URL with an embedded password (postgres://u:p@h, https://x:token@github.com).
var reCredURL = lazyRx(`(?i)\b([a-z][a-z0-9+.-]*://[^/\s:@'"]+:)([^@\s/'"]{3,})@`)

// pfKeyEnvNames holds the KeyEnv of every configured provider (set by main via
// pfNoteConfig), so custom providers' keys are scrubbed and redacted too.
var (
	pfKeyEnvMu    sync.Mutex
	pfKeyEnvNames = map[string]bool{}
)

// pfNoteConfig records the providers' key variables and refreshes the redaction set.
func pfNoteConfig(cfg *Config) {
	h2ApplyConfig(cfg) // shell resource limits (hardening2.go)
	pfKeyEnvMu.Lock()
	if cfg != nil {
		for _, pc := range cfg.Providers {
			if pc != nil && pc.KeyEnv != "" {
				pfKeyEnvNames[envKey(pc.KeyEnv)] = true
			}
		}
	}
	pfKeyEnvMu.Unlock()
	refreshSecrets()
}

// pfIsKeyEnv reports whether name holds a provider key or looks like a secret.
func pfIsKeyEnv(name string) bool {
	if secretEnvNames.MatchString(name) || strings.HasPrefix(name, "JAZZ_SECRET") {
		return true
	}
	pfKeyEnvMu.Lock()
	defer pfKeyEnvMu.Unlock()
	return pfKeyEnvNames[envKey(name)]
}

// envKey normalizes an environment variable name for lookups: Windows names
// are case-insensitive (Path, PATH), Unix names are not.
func envKey(name string) string {
	if winIsWindows {
		return strings.ToUpper(name)
	}
	return name
}

// scrubEnv drops provider keys and other obvious secrets from a child environment.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if pfIsKeyEnv(k) || reCredURL.MatchString(v) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

var (
	secretMu     sync.Mutex
	secretValues []string
	// token shapes: Anthropic, OpenRouter, OpenAI, Groq, GitHub (ghp_/gho_/ghu_/ghs_/ghr_,
	// fine-grained), GitLab, npm, Slack (tokens and webhooks), AWS key ids,
	// Google API keys and OAuth tokens, Stripe, JWTs. Test fixtures use a
	// TEST marker inside the same shapes (sk-ant-TESTKEY-0000…), which still matches.
	secretShapes = lazyRx(`\b(sk-ant-[A-Za-z0-9_-]{20,}|sk-or-v1-[A-Za-z0-9]{20,}|sk-(proj-)?[A-Za-z0-9_-]{20,}|gsk_[A-Za-z0-9]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|npm_[A-Za-z0-9]{36}|xox[abeprs]-[A-Za-z0-9-]{10,}|(AKIA|ASIA)[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|ya29\.[0-9A-Za-z_-]{20,}|(sk|rk)_(live|test)_[0-9A-Za-z]{16,}|whsec_[0-9A-Za-z]{20,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b|https://hooks\.slack\.com/services/[A-Za-z0-9/_-]{20,}|https://discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]+`)
)

// refreshSecrets collects known secret values (env + auth.json) for redaction.
func refreshSecrets() {
	var vals []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if k == "SSH_AUTH_SOCK" {
			continue // a socket path: scrubbed, but not worth redacting
		}
		if (pfIsKeyEnv(k) || reCredURL.MatchString(v)) && len(v) >= 12 {
			vals = append(vals, v)
		}
	}
	for _, c := range LoadAuth() {
		for _, v := range []string{c.Key, c.Access, c.Refresh, c.IDToken} {
			if len(v) >= 12 {
				vals = append(vals, v)
			}
		}
	}
	secretMu.Lock()
	secretValues = vals
	secretMu.Unlock()
}

// redactSecrets replaces known secret values and common token shapes.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	secretMu.Lock()
	vals := secretValues
	secretMu.Unlock()
	for _, v := range vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	// rdReplaceKeepPH (redact.go) leaves <secreto:…> placeholders alone
	s = rdReplaceKeepPH(secretShapes.R(), s, "[REDACTED]")
	if strings.Contains(s, "PRIVATE KEY-----") {
		s = rdReplaceKeepPH(secretPrivKey.R(), s, "[REDACTED PRIVATE KEY]")
	}
	if strings.Contains(s, "://") {
		s = rdReplaceKeepPH(reCredURL.R(), s, "${1}[REDACTED]@")
	}
	s = rdReplaceKeepPH(secretEnvKV.R(), s, "${1}[REDACTED]")
	return rdReplaceKeepPH(secretKV.R(), s, "${1}[REDACTED]${2}")
}

var (
	// PEM private keys (OpenSSH, RSA, EC, PKCS#8, encrypted)
	secretPrivKey = lazyRx(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	// credentials-file style assignments; only literal values (no $VAR,
	// ${…} or function calls), so code that reads a secret stays editable
	// dotenv / shell style KEY=value lines whose name looks secret
	secretEnvKV = lazyRx(`(?m)^([ \t]*(?:export[ \t]+)?[A-Z][A-Z0-9_]*(?:API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|ACCESS_KEY|PRIVATE_KEY|_DSN|WEBHOOK_URL|DATABASE_URL)[A-Z0-9_]*[ \t]*=[ \t]*["']?)[^\s"'$#]{8,}`)
	secretKV    = lazyRx(`(?im)((?:secret_access_key|session_token|secret_key|password|passwd)[ \t]*[=:][ \t]*["']?)[A-Za-z0-9/+=_\-!@#%^&*.~]{8,}(["']?[ \t]*(?:$|[,;#\r]))`)
)

// ---------- terminal sanitizing ----------

// sanitizeTerm removes escape sequences and control characters (keeps \n and \t)
// so model output and file content can't move the cursor, rewrite lines or set the clipboard.
func sanitizeTerm(s string) string {
	if !needsSanitize(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		if c == 0xc2 && i+1 < len(s) { // 8-bit OSC/DCS/SOS/PM/APC introducers (U+0090 U+0098 U+009D U+009E U+009F): drop the string too
			switch s[i+1] {
			case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
				i = h2SkipC1String(s, i+2)
				continue
			}
		}
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			continue
		}
		b.WriteByte(c)
	}
	out := b.String()
	// C1 controls and bidi overrides (as runes)
	return strings.Map(func(r rune) rune {
		if r >= 0x80 && r <= 0x9f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return -1
		}
		return r
	}, out)
}

func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f || c == 0xc2 || c == 0xe2 {
			return true
		}
	}
	return false
}

// skipEscape returns the index of the last byte of the escape sequence starting at i.
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i
	}
	switch s[i+1] {
	case '[': // CSI
		j := i + 2
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		return j
	case ']', 'P', '_', '^': // OSC / DCS / APC / PM: until BEL or ST
		j := i + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return j
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 1
			}
			j++
		}
		return len(s) - 1
	case '(', ')', '*', '+', '#', '%':
		if i+2 < len(s) {
			return i + 2
		}
		return i + 1
	}
	return i + 1
}

// visibleDetail renders a command/path for an approval prompt: control chars made visible.
func visibleDetail(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0x1b:
			b.WriteString("^[")
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\n':
			b.WriteString("⏎ ")
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			b.WriteString("\\x" + strings.ToUpper(string("0123456789abcdef"[r>>4])+string("0123456789abcdef"[r&15])))
		case r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069:
			b.WriteString("\\u{bidi}")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
