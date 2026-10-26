package main

// hardening2.go: second deterministic hardening round (container escapes,
// resource abuse, clones from new hosts, update anti-downgrade, session
// integrity, lesson/plan poisoning). Everything here is pure or file based,
// never calls a model, and is wired into the existing code through one-line
// hooks:
//
//	segmentDanger / dangerWholeSh  -> h2SegmentDanger / h2WholeDanger
//	Shell.initLine (bash/sh)       -> h2LimitsLine
//	pfNoteConfig                   -> h2ApplyConfig
//	cmdUpdate / pfDownloadVerified -> h2DowngradeVerdict, h2NoteSeen
//	Session.writeLocked            -> h2SignLine
//	sessReadEntries / sessReplay   -> h2VerifyEntries, h2UntrustedMsg
//	lsLoad / lsAppend / pcLoad / pcStore -> h2PoisonReason
//	approval of write/edit         -> h2WriteDanger
//
// Outbound redaction of the user's own messages is NOT done here: the redact
// package owns it and this file only documents the hook name,
// redactOutbound(text) (see hardening2_test.go).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 1. container escapes ----------

var h2ContainerRuntimes = map[string]bool{"docker": true, "podman": true, "nerdctl": true}

// h2HostNS are the namespaces whose "host" value removes isolation.
var h2HostNS = map[string]bool{"--pid": true, "--net": true, "--network": true, "--ipc": true, "--uts": true, "--userns": true, "--cgroupns": true}

var h2BadCaps = map[string]bool{"SYS_ADMIN": true, "ALL": true, "SYS_PTRACE": true, "SYS_MODULE": true, "SYS_RAWIO": true, "DAC_READ_SEARCH": true}

var reH2Device = regexp.MustCompile(`^/dev/(sd|hd|vd|xvd|nvme|mmcblk|disk|dm-|mapper/|loop|md|mem$|kmem$|port$)`)

// h2BadMountSrc reports whether a bind-mount source hands the host to the container.
func h2BadMountSrc(src string) bool {
	src = strings.Trim(src, `"'`)
	if src == "" {
		return false
	}
	if strings.Contains(src, "docker.sock") || strings.Contains(src, "podman.sock") || strings.Contains(src, "containerd.sock") {
		return true
	}
	if !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "~") && !strings.HasPrefix(src, "$") {
		return false // named volume or relative path
	}
	e := expandTilde(strings.NewReplacer("${HOME}", "~", "$HOME", "~").Replace(src))
	e = path.Clean(filepath.ToSlash(e))
	switch e {
	case "/", "/etc", "/root", "/proc", "/sys", "/boot", "/dev", "/var", "/var/run", "/run", "/var/lib/docker", "/home", "/Users", "/usr", "/bin", "/sbin", "/lib":
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && e == path.Clean(filepath.ToSlash(home)) {
		return true
	}
	return secretPath(e)
}

// h2MountValues extracts the bind sources of -v/--volume/--mount values.
func h2MountSrc(kind, val string) string {
	if kind == "mount" {
		src, typ := "", "bind"
		for _, kv := range strings.Split(val, ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch strings.TrimSpace(k) {
			case "source", "src":
				src = v
			case "type":
				typ = v
			}
		}
		if typ == "bind" {
			return src
		}
		return ""
	}
	src, _, _ := strings.Cut(val, ":")
	return src
}

// h2ContainerDanger inspects a docker/podman/nerdctl invocation (w = program
// base name). Returns a reason or "".
func h2ContainerDanger(w string, args []string) string {
	w = strings.TrimSuffix(w, ".exe")
	compose := w == "docker-compose" || w == "podman-compose"
	if !h2ContainerRuntimes[w] && !compose {
		return ""
	}
	// find the subcommand, skipping global options
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if !strings.Contains(args[i], "=") && (args[i] == "-H" || args[i] == "--host" || args[i] == "--context" || args[i] == "-c" || args[i] == "--config" || args[i] == "-l" || args[i] == "--log-level") {
			i++
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub := args[i]
	rest := args[i+1:]
	if sub == "container" && len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	if sub == "compose" {
		compose, sub = true, ""
		for k, a := range rest {
			if !strings.HasPrefix(a, "-") {
				sub, rest = a, rest[k+1:]
				break
			}
		}
	}
	if compose {
		return h2ComposeRunDanger(sub, rest)
	}
	switch sub {
	case "run", "create":
		return h2RunFlags(rest)
	case "exec":
		return h2ExecFlags(rest)
	}
	return ""
}

// h2FlagVal returns the value of option name at t, either "--name=value" or
// the next token. consumed tells whether the next token was used.
func h2FlagVal(t, name string, next func() string) (string, bool, bool) {
	if t == name {
		return next(), true, true
	}
	if v, ok := strings.CutPrefix(t, name+"="); ok {
		return v, true, false
	}
	return "", false, false
}

func h2RunFlags(a []string) string {
	for i := 0; i < len(a); i++ {
		t := a[i]
		next := func() string {
			if i+1 < len(a) {
				return a[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(t, "-") {
			continue
		}
		if t == "--privileged" || t == "--privileged=true" {
			return "container com --privileged (escapa do isolamento do host)"
		}
		for ns := range h2HostNS {
			if v, ok, used := h2FlagVal(t, ns, next); ok {
				if used {
					i++
				}
				if strings.EqualFold(v, "host") {
					return "container compartilha o namespace do host (" + ns + "=host)"
				}
			}
		}
		if v, ok, used := h2FlagVal(t, "--cap-add", next); ok {
			if used {
				i++
			}
			for _, c := range strings.Split(v, ",") {
				if h2BadCaps[strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")] {
					return "container com capability perigosa (--cap-add=" + c + ")"
				}
			}
		}
		for _, name := range []string{"--volume", "--mount"} {
			if v, ok, used := h2FlagVal(t, name, next); ok {
				if used {
					i++
				}
				kind := "vol"
				if name == "--mount" {
					kind = "mount"
				}
				if src := h2MountSrc(kind, v); h2BadMountSrc(src) {
					return "container monta " + src + " do host (leitura/escrita fora do projeto)"
				}
			}
		}
		if t == "-v" || (strings.HasPrefix(t, "-v") && !strings.HasPrefix(t, "--") && len(t) > 2) {
			v := strings.TrimPrefix(t, "-v")
			if t == "-v" {
				v = next()
				i++
			}
			v = strings.TrimPrefix(v, "=")
			if src := h2MountSrc("vol", v); h2BadMountSrc(src) {
				return "container monta " + src + " do host (leitura/escrita fora do projeto)"
			}
		}
		if v, ok, used := h2FlagVal(t, "--device", next); ok {
			if used {
				i++
			}
			dev, _, _ := strings.Cut(v, ":")
			if reH2Device.MatchString(dev) {
				return "container recebe dispositivo do host (" + dev + ")"
			}
		}
		if v, ok, used := h2FlagVal(t, "--security-opt", next); ok {
			if used {
				i++
			}
			lv := strings.ToLower(v)
			if strings.Contains(lv, "unconfined") || strings.Contains(lv, "label=disable") || strings.Contains(lv, "label:disable") {
				return "container sem seccomp/apparmor (--security-opt " + v + ")"
			}
		}
	}
	return ""
}

func h2ExecFlags(a []string) string {
	for i := 0; i < len(a); i++ {
		t := a[i]
		if t == "--privileged" {
			return "docker exec --privileged"
		}
		var v string
		switch {
		case t == "-u" || t == "--user":
			if i+1 < len(a) {
				v = a[i+1]
			}
		case strings.HasPrefix(t, "--user="):
			v = strings.TrimPrefix(t, "--user=")
		case strings.HasPrefix(t, "-u") && !strings.HasPrefix(t, "--") && len(t) > 2 && !strings.ContainsAny(t[2:], "it"):
			v = strings.TrimPrefix(t, "-u")
		default:
			continue
		}
		u, _, _ := strings.Cut(v, ":")
		if u == "0" || u == "root" {
			return "docker exec como root (-u " + v + ") em container que o jazz não criou"
		}
	}
	return ""
}

// h2ComposeRunDanger: `compose up|run|create` reads the project's compose files.
func h2ComposeRunDanger(sub string, rest []string) string {
	switch sub {
	case "up", "run", "create", "start", "restart":
	default:
		return ""
	}
	var files []string
	_ = sub
	for i := 0; i < len(rest); i++ {
		if (rest[i] == "-f" || rest[i] == "--file") && i+1 < len(rest) {
			files = append(files, rest[i+1])
			i++
		} else if v, ok := strings.CutPrefix(rest[i], "--file="); ok {
			files = append(files, v)
		}
	}
	if len(files) == 0 {
		for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml", "compose.override.yaml", "compose.override.yml", "docker-compose.override.yml", "docker-compose.override.yaml"} {
			files = append(files, n)
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 1<<20 {
			continue
		}
		if r := h2ComposeText(string(b)); r != "" {
			return "compose " + f + ": " + r
		}
	}
	return ""
}

var (
	reH2CompPriv = regexp.MustCompile(`^\s*privileged:\s*["']?true`)
	reH2CompNS   = regexp.MustCompile(`^\s*(network_mode|pid|ipc|userns_mode|uts|cgroup):\s*["']?host`)
	reH2CompSec  = regexp.MustCompile(`(?i)(seccomp|apparmor)\s*[:=]\s*unconfined|label\s*[:=]\s*disable`)
	reH2CompItem = regexp.MustCompile(`^\s*-\s*["']?([^"'\s]+)`)
	reH2CompCap  = regexp.MustCompile(`^\s*cap_add:\s*(.*)$`)
)

// h2ComposeText flags a compose file that grants host-level access.
func h2ComposeText(text string) string {
	inCap := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if i := strings.Index(l, " #"); i >= 0 {
			l = l[:i]
		}
		switch {
		case reH2CompPriv.MatchString(l):
			return "privileged: true"
		case reH2CompNS.MatchString(l):
			return "namespace do host (" + strings.TrimSpace(l) + ")"
		case reH2CompSec.MatchString(l):
			return "seccomp/apparmor desligado"
		}
		if m := reH2CompCap.FindStringSubmatch(l); m != nil {
			inCap = true
			for _, c := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == '[' || r == ']' || r == ' ' || r == '"' || r == '\'' }) {
				if h2BadCaps[strings.TrimPrefix(strings.ToUpper(c), "CAP_")] {
					return "cap_add " + c
				}
			}
			continue
		}
		if m := reH2CompItem.FindStringSubmatch(l); m != nil {
			item := m[1]
			if inCap && h2BadCaps[strings.TrimPrefix(strings.ToUpper(item), "CAP_")] {
				return "cap_add " + item
			}
			if src, _, ok := strings.Cut(item, ":"); ok && h2BadMountSrc(src) {
				return "monta " + src + " do host"
			}
			if dev, _, _ := strings.Cut(item, ":"); reH2Device.MatchString(dev) {
				return "dispositivo do host " + dev
			}
			continue
		}
		if strings.TrimSpace(l) != "" {
			inCap = false
		}
	}
	return ""
}

// h2WriteDanger inspects a write/edit of a docker-compose file: a model that
// adds privileged: true / host mounts to the project's compose file is
// preparing the escape for a later `compose up`. args are the tool arguments.
func h2WriteDanger(args map[string]any) string {
	p, _ := argStr(args, "path")
	b := strings.ToLower(filepath.Base(p))
	if !(strings.HasPrefix(b, "docker-compose") || strings.HasPrefix(b, "compose")) || !(strings.HasSuffix(b, ".yml") || strings.HasSuffix(b, ".yaml")) {
		return ""
	}
	var texts []string
	for _, k := range []string{"content", "new_string"} {
		if s, ok := argStr(args, k); ok {
			texts = append(texts, s)
		}
	}
	if es, ok := args["edits"].([]any); ok {
		for _, e := range es {
			if m, ok := e.(map[string]any); ok {
				if s, ok := argStr(m, "new_string"); ok {
					texts = append(texts, s)
				}
			}
		}
	}
	for _, t := range texts {
		if r := h2ComposeText(t); r != "" {
			return "arquivo compose concede acesso ao host (" + r + ")"
		}
	}
	return ""
}

// ---------- 2. resource limits and abuse patterns ----------

// LimitsConf is the config "limits" block: caps every shell command inherits.
type LimitsConf struct {
	Procs   int `json:"procs,omitempty"`    // headroom of processes above the user's current count (default 512)
	FileMB  int `json:"file_mb,omitempty"`  // largest file a command may write (default 2048)
	Files   int `json:"files,omitempty"`    // open files (default 4096, only raised)
	CPUSecs int `json:"cpu_secs,omitempty"` // CPU seconds per process (0 = off)
}

const (
	h2DefProcs  = 512
	h2DefFileMB = 2048
	h2DefFiles  = 4096
)

var h2Limits atomic.Pointer[LimitsConf]

func h2ApplyConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	h2Limits.Store(cfg.ShellLimits)
}

func h2LimitsEff() LimitsConf {
	l := LimitsConf{}
	if p := h2Limits.Load(); p != nil {
		l = *p
	}
	if l.Procs <= 0 {
		l.Procs = h2DefProcs
	}
	if l.FileMB <= 0 {
		l.FileMB = h2DefFileMB
	}
	if l.Files <= 0 {
		l.Files = h2DefFiles
	}
	return l
}

// h2LimitsLine is the shell prefix that applies the limits (errors ignored:
// a hard limit already lower is kept). "" for PowerShell and for Windows,
// where the job object caps the process tree.
func h2LimitsLine(d shellDialect) string {
	if d.Kind == shPwsh || d.Win {
		return ""
	}
	l := h2LimitsEff()
	fblocks := l.FileMB * 1024 // bash counts 1024-byte blocks, dash 512
	if d.Kind == shSh {
		fblocks *= 2
	}
	var b strings.Builder
	// -u counts every process of the user, so the limit is "what runs now + headroom"
	// (Linux counts threads too). When the count cannot be measured (ps hidden
	// by a sandbox, or tiny), -u is left alone: a wrong guess would make fork fail.
	b.WriteString(`if [ "$(uname)" = Linux ]; then _jn=$(ps -u "$(id -u)" -L -o pid= 2>/dev/null | wc -l); else _jn=$(ps -u "$(id -u)" -o pid= 2>/dev/null | wc -l); fi; `)
	b.WriteString(fmt.Sprintf(`[ "${_jn:-0}" -ge 5 ] && ulimit -u $((_jn+%d)) 2>/dev/null; unset _jn; `, l.Procs))
	b.WriteString(fmt.Sprintf(`ulimit -f %d 2>/dev/null; `, fblocks))
	b.WriteString(fmt.Sprintf(`_jf=$(ulimit -S -n 2>/dev/null); case "$_jf" in unlimited|"") ;; *) [ "$_jf" -lt %d ] && ulimit -S -n %d 2>/dev/null;; esac; unset _jf; `, l.Files, l.Files))
	if l.CPUSecs > 0 {
		b.WriteString(fmt.Sprintf(`ulimit -t %d 2>/dev/null; `, l.CPUSecs))
	}
	return b.String()
}

var (
	reH2ForkNamed = regexp.MustCompile(`([\w:.-]+)\s*\(\)\s*\{\s*([\w:.-]+)\s*\|\s*([\w:.-]+)\s*&\s*\}\s*;?\s*([\w:.-]+)`)
	reH2ForkPerl  = regexp.MustCompile(`\bfork\s+while\b|while\s*\(\s*1\s*\)\s*\{\s*fork`)
	reH2Yes       = regexp.MustCompile(`(^|[;&|(]\s*)yes\b[^;&|<>\n]*>>?\s*(\S+)`)
	reH2Urandom   = regexp.MustCompile(`/dev/(u?random|zero)\b[^|;&\n]*>>?\s*([^\s&|;]+)|\b(cat|head|pv)\b[^|;&\n]*/dev/(u?random|zero)\b[^|;&\n]*>>?\s*([^\s&|;]+)`)
	reH2HeadC     = regexp.MustCompile(`\bhead\s+(?:-c\s*|--bytes[= ])([0-9]+[kKmMgGtT]?)`)
	reH2While     = regexp.MustCompile(`(?s)\b(while\s+(true|:|\[\s*(1|true)\s*\]|\(\(\s*1\s*\)\))|until\s+(false|\[\s*0\s*\])|for\s*\(\(\s*;\s*;\s*\)\))\s*;?\s*do\b(.*)\bdone`)
	reH2WriteOp   = regexp.MustCompile(`>>|\btee\b|\bdd\b|\bfallocate\b|\bcp\b|\btruncate\b|\bhead\s+-c|/dev/(u?random|zero)`)
)

// h2WholeDanger covers abuse patterns that need the whole command line.
func h2WholeDanger(cmd string) string {
	for _, m := range reH2ForkNamed.FindAllStringSubmatch(cmd, 4) {
		if m[1] == m[2] && m[2] == m[3] && m[3] == m[4] {
			return "fork bomb"
		}
	}
	if reH2ForkPerl.MatchString(cmd) {
		return "fork bomb"
	}
	if m := reH2Yes.FindStringSubmatch(cmd); m != nil && strings.Trim(m[2], `"'`) != "/dev/null" {
		return "yes redirecionado para arquivo (enche o disco)"
	}
	bounded := false
	for _, m := range reH2HeadC.FindAllStringSubmatch(cmd, 4) {
		if n := h2Bytes(m[1]); n > 0 && n <= 1<<30 {
			bounded = true // head -c 16 /dev/urandom > key: a small, bounded read
		}
	}
	for _, m := range reH2Urandom.FindAllStringSubmatch(cmd, 8) {
		if bounded {
			break
		}
		tgt := m[2]
		if tgt == "" {
			tgt = m[5]
		}
		if tgt != "" && strings.Trim(tgt, `"'`) != "/dev/null" && !strings.HasPrefix(strings.Trim(tgt, `"'`), "&") {
			return "despeja /dev/zero|urandom em arquivo (enche o disco)"
		}
	}
	if m := reH2While.FindStringSubmatch(cmd); m != nil {
		body := m[len(m)-1]
		if reH2WriteOp.MatchString(body) && !strings.Contains(body, "sleep") && !regexp.MustCompile(`\bbreak\b|\bexit\b`).MatchString(body) {
			return "laço infinito que escreve em disco"
		}
	}
	return ""
}

var reH2Size = regexp.MustCompile(`^([0-9]+)([kKmMgGtT]?)[bB]?$`)

// h2Bytes parses sizes such as 100G, 4096, 1M.
func h2Bytes(s string) int64 {
	m := reH2Size.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch strings.ToLower(m[2]) {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	case "t":
		n <<= 40
	}
	return n
}

const h2HugeFile = 4 << 30

// h2SegmentDanger is the per-command hook of segmentDanger (w = base name).
func h2SegmentDanger(w string, args []string) string {
	if r := h2ContainerDanger(w, args); r != "" {
		return r
	}
	switch w {
	case "fallocate":
		for i, a := range args {
			if (a == "-l" || a == "--length") && i+1 < len(args) && h2Bytes(args[i+1]) > h2HugeFile {
				return "fallocate de arquivo enorme (enche o disco)"
			}
			if v, ok := strings.CutPrefix(a, "--length="); ok && h2Bytes(v) > h2HugeFile {
				return "fallocate de arquivo enorme (enche o disco)"
			}
		}
	case "truncate":
		for i, a := range args {
			if (a == "-s" || a == "--size") && i+1 < len(args) && h2Bytes(strings.TrimPrefix(args[i+1], "+")) > h2HugeFile {
				return "truncate para arquivo enorme (enche o disco)"
			}
		}
	case "dd":
		in, of, bs, count := "", "", int64(512), int64(-1)
		for _, a := range args {
			switch {
			case strings.HasPrefix(a, "if="):
				in = a[3:]
			case strings.HasPrefix(a, "of="):
				of = a[3:]
			case strings.HasPrefix(a, "bs="):
				bs = h2Bytes(a[3:])
			case strings.HasPrefix(a, "count="):
				if n, err := strconv.ParseInt(a[6:], 10, 64); err == nil {
					count = n
				}
			}
		}
		if (in == "/dev/zero" || in == "/dev/urandom" || in == "/dev/random") && of != "" && of != "/dev/null" {
			if count < 0 || count*bs > 1<<30 {
				return "dd de /dev/zero|urandom sem limite pequeno (enche o disco)"
			}
		}
	}
	if r := h2CloneDanger(w, args); r != "" {
		return r
	}
	return ""
}

// ---------- 6. clones and git installs from a new host ----------

var (
	reH2URLHost = regexp.MustCompile(`(?i)^(?:git\+)?(?:https?|ssh|git|ftp)://(?:[^/@\s]+@)?([A-Za-z0-9.-]+)`)
	reH2SCPHost = regexp.MustCompile(`^(?:[\w.-]+@)([A-Za-z0-9.-]+):`)
)

// h2RootFn returns the project directory (tests replace it).
var h2RootFn = func() string { d, _ := os.Getwd(); return d }

// h2GitHost returns the remote host named by a git source ("" = local path).
func h2GitHost(s string) string {
	s = strings.Trim(s, `"'`)
	if m := reH2URLHost.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}
	if m := reH2SCPHost.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}
	for _, p := range [][2]string{{"github:", "github.com"}, {"gitlab:", "gitlab.com"}, {"bitbucket:", "bitbucket.org"}} {
		if strings.HasPrefix(s, p[0]) {
			return p[1]
		}
	}
	return ""
}

var reH2ConfURL = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)`)

// h2SeenHosts are the git hosts the repository already talks to: remotes in
// .git/config and submodule urls in .gitmodules.
func h2SeenHosts(root string) map[string]bool {
	seen := map[string]bool{}
	for _, f := range []string{filepath.Join(root, ".git", "config"), filepath.Join(root, ".gitmodules")} {
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 1<<20 {
			continue
		}
		for _, m := range reH2ConfURL.FindAllStringSubmatch(string(b), -1) {
			if h := h2GitHost(m[1]); h != "" {
				seen[h] = true
			}
		}
	}
	return seen
}

func h2NewHost(src string) (string, bool) {
	h := h2GitHost(src)
	if h == "" || egLocal(h) {
		return "", false
	}
	return h, !h2SeenHosts(h2RootFn())[h]
}

var h2Installers = map[string]bool{"pip": true, "pip3": true, "pipx": true, "uv": true, "poetry": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "cargo": true}

func h2CloneDanger(w string, args []string) string {
	var cands []string
	switch {
	case w == "git":
		sub := ""
		for i := 0; i < len(args); i++ {
			if strings.HasPrefix(args[i], "-") {
				if args[i] == "-C" || args[i] == "-c" {
					i++
				}
				continue
			}
			sub = args[i]
			args = args[i+1:]
			break
		}
		switch sub {
		case "clone":
			for _, a := range args {
				if !strings.HasPrefix(a, "-") {
					cands = append(cands, a)
					break
				}
			}
		case "submodule":
			if len(args) > 0 && args[0] == "add" {
				for _, a := range args[1:] {
					if !strings.HasPrefix(a, "-") {
						cands = append(cands, a)
						break
					}
				}
			}
		}
	case h2Installers[w] || w == "python" || w == "python3":
		for i, a := range args {
			if strings.HasPrefix(a, "-") {
				if a == "--git" && i+1 < len(args) {
					cands = append(cands, args[i+1])
				} else if v, ok := strings.CutPrefix(a, "--git="); ok {
					cands = append(cands, v)
				}
				continue
			}
			base := strings.SplitN(a, "#", 2)[0]
			if h2GitHost(a) != "" && (strings.HasPrefix(a, "git+") || strings.HasPrefix(a, "git://") || strings.HasSuffix(base, ".git") ||
				strings.HasPrefix(a, "github:") || strings.HasPrefix(a, "gitlab:") || strings.HasPrefix(a, "bitbucket:")) {
				cands = append(cands, a)
			}
		}
	}
	for _, c := range cands {
		if h, isNew := h2NewHost(c); isNew {
			return "busca código de um host novo no repositório (" + h + "): clone/instalação git"
		}
	}
	return ""
}

// ---------- 3. update anti-downgrade ----------

type h2SeenRec struct {
	Version string `json:"version"`
	Date    string `json:"date"`
}

func h2SeenPath() string { return filepath.Join(dataDir(), "update_seen.json") }

func h2LoadSeen() h2SeenRec {
	var r h2SeenRec
	if b, err := os.ReadFile(h2SeenPath()); err == nil {
		_ = json.Unmarshal(b, &r)
		if !pfVersionRe.MatchString(r.Version) {
			r = h2SeenRec{}
		}
	}
	return r
}

// h2NoteSeen records v as the newest version seen (only ever moves forward).
func h2NoteSeen(v string) {
	v = strings.TrimPrefix(v, "v")
	if !pfVersionRe.MatchString(v) {
		return
	}
	if cur := h2LoadSeen(); cur.Version != "" && !pfVersionNewer(v, cur.Version) {
		return
	}
	b, _ := json.Marshal(h2SeenRec{Version: v, Date: time.Now().UTC().Format("2006-01-02")})
	if os.MkdirAll(dataDir(), 0o700) == nil {
		_ = writeFileAtomic(h2SeenPath(), b, 0o600)
	}
}

// h2DowngradeVerdict compares an offered version with the running one and
// with the newest ever seen. warn is non-empty for any downgrade (shown by
// `update --check` too); err is set when it must be refused (no allow).
func h2DowngradeVerdict(candidate, current string, allow bool) (warn string, err error) {
	candidate, current = strings.TrimPrefix(candidate, "v"), strings.TrimPrefix(current, "v")
	switch {
	case pfVersionNewer(current, candidate):
		warn = fmt.Sprintf("a atualização oferecida (%s) é MAIS ANTIGA que a versão em execução (%s): possível downgrade", candidate, current)
	default:
		if seen := h2LoadSeen(); seen.Version != "" && pfVersionNewer(seen.Version, candidate) {
			warn = fmt.Sprintf("a atualização oferecida (%s) é mais antiga que a última vista (%s em %s): possível downgrade", candidate, seen.Version, seen.Date)
		}
	}
	if warn != "" && !allow {
		return warn, errors.New(warn + "; recuso (use --allow-downgrade se for de propósito)")
	}
	return warn, nil
}

// ---------- 4. session integrity ----------

var (
	h2KeyMu sync.Mutex
	h2Keys  = map[string][]byte{}
)

func h2KeyPath() string { return filepath.Join(dataDir(), "session.key") }

// h2SessionKey returns the per-install HMAC key (32 random bytes, 0600),
// creating it on first use. nil when the data dir is not writable (signing
// and verification are then skipped rather than flagging everything).
func h2SessionKey() []byte {
	p := h2KeyPath()
	h2KeyMu.Lock()
	defer h2KeyMu.Unlock()
	if k, ok := h2Keys[p]; ok {
		return k
	}
	var key []byte
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		key = b
	} else if err != nil && os.IsNotExist(err) {
		nk := make([]byte, 32)
		if _, err := rand.Read(nk); err == nil && os.MkdirAll(filepath.Dir(p), 0o700) == nil {
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				_, werr := f.Write(nk)
				f.Close()
				if werr == nil {
					key = nk
				}
			} else if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
				key = b // lost a creation race
			}
		}
	}
	h2Keys[p] = key
	return key
}

const h2MacLen = 16 // bytes of HMAC-SHA256 kept (32 hex chars)

func h2Mac(key, body []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil)[:h2MacLen])
}

// h2SignLine appends the HMAC trailer to one marshalled JSON object:
// {"t":"msg",...} -> {"t":"msg",...,"h":"<32 hex>"}. Other readers just see
// one more field.
func h2SignLine(b []byte) []byte {
	key := h2SessionKey()
	if key == nil || len(b) < 2 || b[len(b)-1] != '}' {
		return b
	}
	out := make([]byte, 0, len(b)+40)
	out = append(out, b[:len(b)-1]...)
	out = append(out, `,"h":"`...)
	out = append(out, h2Mac(key, b)...)
	return append(out, `"}`...)
}

const (
	h2Unsigned = iota
	h2Valid
	h2Invalid
)

var reH2Trailer = regexp.MustCompile(`,"h":"([0-9a-f]{32})"\}$`)

// h2CheckLine classifies one raw line.
func h2CheckLine(key, line []byte) int {
	m := reH2Trailer.FindSubmatchIndex(line)
	if m == nil {
		return h2Unsigned
	}
	body := append(append([]byte{}, line[:m[0]]...), '}')
	want := h2Mac(key, body)
	if hmac.Equal([]byte(want), line[m[2]:m[3]]) {
		return h2Valid
	}
	return h2Invalid
}

// h2LineVerifier classifies the lines of one file in order. A file's unsigned
// lines before its first signed line are "legacy" (written before the
// feature); an unsigned line after a signed one was stripped or injected.
type h2LineVerifier struct {
	key        []byte
	seenSigned bool
}

func newH2Verifier() *h2LineVerifier { return &h2LineVerifier{key: h2SessionKey()} }

// check returns (tampered, legacy).
func (v *h2LineVerifier) check(line []byte) (bool, bool) {
	if v.key == nil {
		return false, false
	}
	switch h2CheckLine(v.key, line) {
	case h2Valid:
		v.seenSigned = true
		return false, false
	case h2Invalid:
		v.seenSigned = true
		return true, false
	}
	if v.seenSigned {
		return true, false
	}
	return false, true
}

// h2TamperStats counts flagged and legacy entries.
func h2TamperStats(ents []sessEntry) (tampered, legacy int) {
	for _, e := range ents {
		if e.Tampered {
			tampered++
		}
		if e.Legacy {
			legacy++
		}
	}
	return
}

// h2UntrustedMsg turns a message from a tampered line into inert, marked
// data: a user message wrapped like suspected injections, with tool calls
// flattened to text and images dropped.
func h2UntrustedMsg(m Message) Message {
	var b strings.Builder
	b.WriteString("[" + m.Role + " message from a session line altered outside jazz]\n")
	b.WriteString(m.Content)
	for _, tc := range m.ToolCalls {
		b.WriteString("\n[tool call " + tc.Name + " " + tc.Args + "]")
	}
	return Message{Role: "user", Content: grWrapUntrusted("sessão alterada fora do jazz", b.String())}
}

// h2DropOrphanTools removes tool results whose call is not among the calls
// of the assistant message just before them (the call may have been demoted
// to untrusted text). ids stays aligned with msgs.
func h2DropOrphanTools(msgs []Message, ids []string) ([]Message, []string) {
	var om []Message
	var oi []string
	calls := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			calls = map[string]bool{}
			for _, tc := range m.ToolCalls {
				calls[tc.ID] = true
			}
		case "tool":
			if !calls[m.ToolCallID] {
				continue
			}
		default:
			calls = map[string]bool{}
		}
		om = append(om, m)
		if i < len(ids) {
			oi = append(oi, ids[i])
		}
	}
	return om, oi
}

// h2SessionNotice is the text shown on resume ("" = nothing to say).
func h2SessionNotice(tampered int) string {
	if tampered <= 0 {
		return ""
	}
	return fmt.Sprintf("sessão alterada fora do jazz: %d linhas (carregadas como NÃO CONFIÁVEIS)", tampered)
}

var h2AllowTampered, h2AllowDowngrade bool

func h2TamperedAllowed() bool {
	if h2AllowTampered {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_ALLOW_TAMPERED")))
	return v == "1" || v == "true" || v == "yes"
}

// h2LegacyNoticeOnce returns the one-time migration notice for sessions
// written before the HMAC trailer existed ("" after the first time).
func h2LegacyNoticeOnce() string {
	p := filepath.Join(dataDir(), "session_hmac_notice")
	if _, err := os.Stat(p); err == nil {
		return ""
	}
	if os.MkdirAll(dataDir(), 0o700) == nil {
		_ = os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	}
	return "sessões antigas não têm assinatura de integridade: abertas sem verificação (as novas linhas já são assinadas)"
}

// ---------- 5. lesson and plan-cache poisoning ----------

var h2PoisonRes = []struct {
	label string
	re    *regexp.Regexp
}{
	{"URL externa", regexp.MustCompile(`(?i)\b(?:https?|ftp|ssh|git|wss?)://(?:[^/\s@]+@)?(?:[a-z0-9-]+\.)+[a-z0-9-]+|\b(?:https?|ftp|ssh|git|wss?)://(?:\d{1,3}\.){3}\d{1,3}`)},
	{"endereço IP", regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)},
	{"nome de host", regexp.MustCompile(`(?i)\b(?:[a-z0-9][a-z0-9-]*\.)+(?:com|net|org|io|dev|app|xyz|ru|cn|ai|info|biz|cloud|tk|top|site|online|link|click|ly|gg|cc|to)\b`)},
	{"comando de rede", regexp.MustCompile(`(?i)\b(?:curl|wget|nc|ncat|netcat|scp|sftp|socat|telnet|rsync)\s+\S|\bssh\s+(?:-|[\w.-]+@)|\|\s*(?:ba|z)?sh\b`)},
	{"operação com segredo", regexp.MustCompile(`(?i)\b(?:cat|type|less|head|tail|base64|cp|mv|tar|zip|printenv|env|echo|send|upload)\b[^\n]*(?:\.env\b|id_rsa|id_ed25519|\.ssh/|auth\.json|\.aws/|\.netrc|\.git-credentials|\.gnupg|api[_-]?key|\bsecrets?\b|\$\{?[A-Z_]*(?:KEY|TOKEN|SECRET|PASSWORD)\b)|\$\{?[A-Z_]*(?:API_KEY|TOKEN|SECRET|PASSWORD)\}?`)},
	{"desliga proteções", regexp.MustCompile(`(?i)\b(?:desativ\w*|desabilit\w*|desligu?\w*|disable\w*|turn\s+off|bypass\w*|skip\w*|pul[ae]\w*|contorn\w*|remov\w*|remove)\b[^\n]{0,40}\b(?:guard\w*|sandbox|redact\w*|reda[cç]\w*|prote[cç]\w*|seguran[cç]a|safety|confirma\w*|permiss\w*|approval|aprova\w*|hooks?|audit\w*|trust|confian[cç]a|modo\b|mode\b|injection|injeção)|--(?:yolo|turbo|dangerously\w*|no-sandbox|allow-outside|full-auto)\b|sandbox\s*[=:]?\s*off|\b(?:modo|mode)\s+(?:yolo|turbo)\b|\byolo\b`)},
	{"instrução ao agente", regexp.MustCompile(`(?i)\b(?:sempre|always)\s+(?:rode|rodar|execute|executar|run|exec)\b|\b(?:ignor\w+|desconsider\w+|esque[çc]a|forget|disregard)\b[^\n]{0,30}\b(?:instru\w*|instruction\w*|previous|anterior\w*|regras|rules|prompt|usu[aá]rio|user)\b|\b(?:n[aã]o|nunca|never|don'?t|do\s+not)\s+(?:pergunt\w*|peça|peca|pedir|ask|confirm\w*|avis\w*|tell|mention\w*|mostr\w*|reveal|revel\w*)\b|\b(?:sem|without)\s+(?:perguntar|pedir|confirma\w*|asking|confirmation|approval|aprova\w*)\b|\b(?:voc[êe]|you)\s+(?:deve|must|should)\s+(?:sempre|always|obey|obedecer)\b|\bobe(?:y|ça|deça)\b`)},
}

// h2PoisonReason returns why a lesson or a reused-plan line must be rejected
// ("" = plain engineering advice). Loopback addresses are allowed.
func h2PoisonReason(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	t := strings.NewReplacer("localhost", "", "127.0.0.1", "", "0.0.0.0", "", "::1", "").Replace(s)
	for _, p := range h2PoisonRes {
		if p.re.MatchString(t) {
			return p.label
		}
	}
	if l := injScan(s); l != "" {
		return l
	}
	return ""
}

func h2PlanPoison(p pcPlan) string {
	parts := []string{p.Task, p.Verify, p.Hint}
	parts = append(parts, p.Steps...)
	parts = append(parts, p.Dead...)
	for _, s := range parts {
		if r := h2PoisonReason(s); r != "" {
			return r
		}
	}
	return ""
}

// h2ResumeNotice tells the user about altered lines (every time) and about
// unsigned legacy lines (once ever).
func (a *Agent) h2ResumeNotice(s *Session) {
	if a.ui == nil || s == nil {
		return
	}
	if n := h2SessionNotice(s.Tampered); n != "" {
		a.ui.Warn(n)
		auditRec("session_tampered", map[string]any{"session": s.ID, "lines": s.Tampered})
	}
	if s.Legacy > 0 {
		if n := h2LegacyNoticeOnce(); n != "" {
			a.ui.Notice(n)
		}
	}
}

// h2TamperedAbort reports whether a headless resume must fail (exit 5): the
// session has altered lines and --allow-tampered / JAZZ_ALLOW_TAMPERED is off.
func h2TamperedAbort(s *Session) bool {
	return s != nil && s.Tampered > 0 && !h2TamperedAllowed()
}

// h2CleanLessons drops the lessons that read like poisoning.
func h2CleanLessons(in []lsLesson) []lsLesson {
	out := in[:0:0]
	for _, l := range in {
		if h2PoisonReason(l.lsLine()) == "" {
			out = append(out, l)
		}
	}
	return out
}

// h2SkipC1String returns the index of the last byte of a C1 control string
// that started before from, ending at ST (U+009C), BEL or ESC \.
func h2SkipC1String(s string, from int) int {
	for j := from; j < len(s); j++ {
		switch {
		case s[j] == 0x07:
			return j
		case s[j] == 0xc2 && j+1 < len(s) && s[j+1] == 0x9c:
			return j + 1
		case s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\':
			return j + 1
		}
	}
	return len(s) - 1
}
