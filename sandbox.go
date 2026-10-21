package main

// sandbox.go: OS-level sandbox for everything jazz executes (persistent
// shell, background jobs, hooks, MCP stdio servers, embedded scripts, lint
// checkers). Defense in depth UNDER the existing guards (DangerReason, fence,
// egress gate, permission rules): those decide what the model may ask for;
// the sandbox limits what a child process can actually touch if something
// slips through (yolo mode, a guard bypass, a malicious hook or MCP server).
//
// Backends:
//   - macOS: sandbox-exec with a generated SBPL profile (deprecated by Apple
//     but functional; Chrome, Codex and Claude Code use it the same way).
//   - Linux: bubblewrap (bwrap), unprivileged user namespaces.
//   - Windows: no filesystem isolation; children get a job object with UI
//     restrictions, an active-process cap and die-on-unhandled-exception
//     (proc_windows.go). Reported as partial; strict refuses it.
//
// Policy (both real backends): every system path stays readable; $HOME is
// unreadable except the project and toolchain dirs (module caches, SDKs,
// ~/.gitconfig); secrets (~/.ssh, ~/.aws, ~/.gnupg, keychains …) and jazz's
// own config/data dirs are unreadable even when the project contains them,
// except spill/job logs, extracted scripts and skills. Writes go only to the
// project (plus its git top-level), a private temp dir, the spill/job dirs
// and, in auto, the toolchain caches. Network: allowed in auto; strict (or
// sandbox_net: off) leaves only loopback.
//
// The persistent shell is wrapped once at spawn, so every command it runs
// inherits the sandbox and the marker protocol / reset logic are untouched.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sbSeatbelt  = "sandbox-exec"
	sbBwrap     = "bwrap"
	sbJobObject = "jobobject"
)

// Sandbox is the resolved policy for this run. A nil *Sandbox means off.
type Sandbox struct {
	Mode     string // auto | strict
	Backend  string // sbSeatbelt | sbBwrap | sbJobObject
	Bin      string // absolute path of sandbox-exec / bwrap
	Net      bool   // children may reach the network (loopback always works)
	Tmp      string // private temp dir (TMPDIR inside)
	UserTmp  string // macOS per-user temp dir, also writable
	Home     string
	HideHome bool     // false when a root contains $HOME (project = home)
	Roots    []string // read-write: project root (+ git top-level)
	CacheRO  []string // toolchain dirs under $HOME, readable
	CacheRW  []string // toolchain caches, writable (auto only)
	Hide     []string // never readable, even inside a root
	ReadOK   []string // readable inside Hide (scripts, skills)
	WriteOK  []string // read-write inside Hide (spill, jobs)
	Env      []string // extra env for children

	once    sync.Once
	profile string   // seatbelt: SBPL text
	params  []string // seatbelt: -D key=value pairs
}

var sbActive atomic.Pointer[Sandbox]

// sbStatus is the doctor line of the last sbSetup (backend or why none).
var sbStatus atomic.Value // string

// sbCurrent is the sandbox children start under (nil = none).
func sbCurrent() *Sandbox { return sbActive.Load() }

// sbProbe finds the backend for this OS; tests replace it.
var sbProbe = sbProbeOS

// sbResolveMode: --sandbox wins over JAZZ_SANDBOX, then config "sandbox";
// default auto.
func sbResolveMode(flag string, cfg *Config) (string, error) {
	m := strings.ToLower(strings.TrimSpace(flag))
	if m == "" {
		m = strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_SANDBOX")))
	}
	if m == "" && cfg != nil {
		m = strings.ToLower(strings.TrimSpace(cfg.Sandbox))
	}
	switch m {
	case "", "auto", "on", "1", "true":
		return "auto", nil
	case "off", "0", "false", "no", "none":
		return "off", nil
	case "strict":
		return "strict", nil
	}
	return "", fmt.Errorf("--sandbox deve ser auto, off ou strict (recebi %q)", m)
}

// sbNetAllowed: JAZZ_SANDBOX_NET, then config sandbox_net; default on in
// auto, off in strict.
func sbNetAllowed(mode string, cfg *Config) bool {
	v := strings.TrimSpace(os.Getenv("JAZZ_SANDBOX_NET"))
	if v == "" && cfg != nil {
		v = strings.TrimSpace(cfg.SandboxNet)
	}
	if v != "" {
		return !olOffWord(v)
	}
	return mode != "strict"
}

// errSandboxMissing: strict mode without a usable backend.
var errSandboxMissing = errors.New("sandbox strict: nenhum backend de sandbox do sistema disponível")

// sbSetup resolves the mode, probes the backend and activates the sandbox
// for every child jazz starts from now on. It returns errSandboxMissing
// (wrapped with the reason) in strict mode when nothing usable exists; auto
// silently runs without one (jazz doctor says so).
func sbSetup(cfg *Config, root, flag string) (*Sandbox, error) {
	mode, err := sbResolveMode(flag, cfg)
	if err != nil {
		return nil, err
	}
	sbActive.Store(nil)
	if mode == "off" {
		sbStatus.Store("desligado (--sandbox off / sandbox: off)")
		return nil, nil
	}
	backend, bin, why := sbProbe()
	if backend == "" || (mode == "strict" && backend == sbJobObject) {
		if backend == sbJobObject {
			why = "no Windows só há job object (sem isolamento de arquivos)"
		}
		sbStatus.Store("nenhum backend: " + why + " — comandos rodam sem isolamento do sistema")
		if mode == "strict" {
			return nil, fmt.Errorf("%w (%s)", errSandboxMissing, why)
		}
		return nil, nil
	}
	sb := sbNew(mode, backend, bin, sbNetAllowed(mode, cfg), sbProjectRoots(root))
	if backend != sbJobObject {
		if err := sb.prepare(); err != nil {
			sbStatus.Store("falhou ao preparar: " + err.Error())
			if mode == "strict" {
				return nil, fmt.Errorf("%w (%v)", errSandboxMissing, err)
			}
			return nil, nil
		}
	}
	sbActive.Store(sb)
	sbStatus.Store(sb.Describe())
	return sb, nil
}

// sbProjectRoots is the project root plus its git top-level when the root is
// a tracked part of a bigger work tree (git needs the .git dir).
func sbProjectRoots(root string) []string {
	roots := []string{root}
	if top := fenceGitTop(root); top != "" && top != root && pathIn(resolvePath(root), top) {
		roots = append(roots, top)
	}
	return roots
}

// sbProbeOS returns the backend, its binary, or why there is none.
func sbProbeOS() (backend, bin, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		p, err := exec.LookPath("sandbox-exec")
		if err != nil {
			p = "/usr/bin/sandbox-exec"
			if _, err := os.Stat(p); err != nil {
				return "", "", "sandbox-exec não encontrado"
			}
		}
		if out, err := exec.CommandContext(ctx, p, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			return "", "", "sandbox-exec não funciona aqui: " + oneLine(strings.TrimSpace(string(out))+" "+err.Error(), 120)
		}
		return sbSeatbelt, p, ""
	case "linux":
		p, err := exec.LookPath("bwrap")
		if err != nil {
			return "", "", "bubblewrap (bwrap) não instalado — sudo apt install bubblewrap"
		}
		if out, err := exec.CommandContext(ctx, p, "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-pid", "--die-with-parent", "--", "/bin/true").CombinedOutput(); err != nil {
			return "", "", "bwrap não consegue criar namespaces (user namespaces desligados no kernel?): " + oneLine(strings.TrimSpace(string(out)), 120)
		}
		return sbBwrap, p, ""
	case "windows":
		return sbJobObject, "", ""
	}
	return "", "", "sem backend para " + runtime.GOOS
}

// sbNew builds the path policy for roots (pure: no probing, no mkdir).
func sbNew(mode, backend, bin string, net bool, roots []string) *Sandbox {
	sb := &Sandbox{Mode: mode, Backend: backend, Bin: bin, Net: net}
	home, _ := os.UserHomeDir()
	sb.Home = sbReal(home)
	for _, r := range roots {
		if r != "" {
			sb.Roots = append(sb.Roots, sbReal(r))
		}
	}
	sb.HideHome = sb.Home != "" && sb.Home != "/"
	for _, r := range sb.Roots {
		if pathIn(sb.Home, r) {
			sb.HideHome = false // project is $HOME (or above it): secrets list still applies
		}
	}
	if sb.Home != "" && sb.Home != "/" {
		for _, rel := range sbHomeRO {
			sb.CacheRO = append(sb.CacheRO, filepath.Join(sb.Home, rel))
		}
		if mode != "strict" {
			for _, rel := range sbHomeRW {
				sb.CacheRW = append(sb.CacheRW, filepath.Join(sb.Home, rel))
			}
		}
		for _, rel := range sbHomeSecrets {
			sb.Hide = append(sb.Hide, filepath.Join(sb.Home, rel))
		}
	}
	// toolchain locations moved by env (only meaningful under $HOME; outside
	// it they are readable anyway)
	for _, k := range []string{"GOPATH", "GOMODCACHE", "CARGO_HOME", "RUSTUP_HOME", "PYENV_ROOT", "NVM_DIR"} {
		if v := os.Getenv(k); v != "" && filepath.IsAbs(v) {
			sb.CacheRO = append(sb.CacheRO, sbReal(strings.Split(v, string(os.PathListSeparator))[0]))
		}
	}
	if mode != "strict" {
		for _, k := range []string{"GOCACHE", "GOMODCACHE", "npm_config_cache", "PIP_CACHE_DIR", "YARN_CACHE_FOLDER"} {
			if v := os.Getenv(k); v != "" && filepath.IsAbs(v) {
				sb.CacheRW = append(sb.CacheRW, sbReal(v))
			}
		}
	}
	cfg, data := sbReal(configDir()), sbReal(dataDir())
	sb.Hide = append(sb.Hide, cfg, data)
	sb.ReadOK = []string{sbReal(filepath.Join(configDir(), "skills")), sbReal(filepath.Join(dataDir(), "scripts"))}
	sb.WriteOK = []string{sbReal(spillDir()), sbReal(jobsDir())}
	if runtime.GOOS == "darwin" {
		// macOS mktemp and friends ignore TMPDIR and use the per-user temp
		// dir (confstr _CS_DARWIN_USER_TEMP_DIR = os.TempDir())
		sb.UserTmp = sbReal(sbDarwinUserTemp())
	}
	sb.Hide = sbUniq(sb.Hide)
	sb.CacheRO = sbUniq(sb.CacheRO)
	sb.CacheRW = sbUniq(sb.CacheRW)
	return sb
}

// sbHomeRO are toolchain dirs under $HOME a build needs to read (the fence's
// list plus binaries and git's user config).
var sbHomeRO = []string{"go", "sdk", ".cargo", ".rustup", ".pyenv", ".nvm", ".local/lib", ".local/bin", ".local/share/pnpm",
	".m2", ".gradle", ".yarn", ".pnpm-store", "Library/pnpm", ".bun", ".deno", ".volta", ".asdf", ".sdkman",
	".gitconfig", ".config/git", ".gitignore_global", ".npm"}

// sbHomeRW are package/build caches (auto mode only). Cache poisoning is the
// accepted cost: making them read-only breaks every go build / npm install.
var sbHomeRW = []string{"go/pkg/mod", ".npm/_cacache", ".npm/_logs", ".cargo/registry", ".cargo/git",
	".cache/go-build", ".cache/pip", ".cache/uv", ".cache/yarn", ".cache/pnpm", ".cache/node-gyp", ".cache/ms-playwright",
	"Library/Caches/go-build", "Library/Caches/pip", "Library/Caches/Yarn", "Library/Caches/pnpm", "Library/Caches/node-gyp",
	"Library/Caches/ms-playwright", ".m2/repository", ".gradle/caches", ".yarn/berry/cache", ".pnpm-store", "Library/pnpm/store", ".bun/install/cache"}

// sbHomeSecrets stay unreadable even when the project is $HOME.
var sbHomeSecrets = []string{".ssh", ".aws", ".gnupg", ".azure", ".kube", ".docker", ".netrc", ".git-credentials",
	".npmrc", ".pypirc", ".config/gh", ".config/gcloud", ".config/hub", ".config/op", ".password-store",
	".claude", ".claude.json", ".codex", ".gemini", ".config/opencode", ".local/share/opencode",
	"Library/Keychains", "Library/Cookies", "Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
	".mozilla", ".config/google-chrome", ".config/chromium", ".bash_history", ".zsh_history"}

func sbUniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// sbReal resolves symlinks in p, also for paths that do not exist yet (the
// longest existing prefix is resolved): seatbelt matches real paths only
// (/tmp is /private/tmp on macOS).
func sbReal(p string) string {
	if p == "" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == p {
		return p
	}
	return filepath.Join(sbReal(dir), base)
}

// sbDarwinUserTemp is confstr(_CS_DARWIN_USER_TEMP_DIR), what mktemp uses
// even when TMPDIR points elsewhere.
func sbDarwinUserTemp() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); filepath.IsAbs(d) {
			return d
		}
	}
	return os.TempDir()
}

// prepare creates the private temp dir and the spill/job dirs (they must
// exist to be bound) and builds the backend's static arguments.
func (s *Sandbox) prepare() error {
	for _, d := range s.WriteOK {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	base := filepath.Join(os.TempDir(), "jazz-sbx-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	sbPruneTmp(base)
	tmp, err := os.MkdirTemp(base, fmt.Sprintf("%d-", os.Getpid()))
	if err != nil {
		return err
	}
	s.Tmp = sbReal(tmp)
	s.Env = []string{"TMPDIR=" + s.Tmp, "TMP=" + s.Tmp, "TEMP=" + s.Tmp, "JAZZ_SANDBOX_ACTIVE=" + s.Backend}
	if s.Mode == "strict" {
		// caches are read-only in strict: builds keep a private cache
		s.Env = append(s.Env, "GOCACHE="+filepath.Join(s.Tmp, "go-build"), "npm_config_cache="+filepath.Join(s.Tmp, "npm"),
			"PIP_CACHE_DIR="+filepath.Join(s.Tmp, "pip"))
	}
	return nil
}

// sbPruneTmp removes private temp dirs left by jazz processes that are gone
// (older than a day: a dead pid may be reused).
func sbPruneTmp(base string) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < 24*time.Hour {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

// Close removes the private temp dir.
func (s *Sandbox) Close() {
	if s != nil && s.Tmp != "" {
		_ = os.RemoveAll(s.Tmp)
	}
}

// Isolates reports whether children get filesystem isolation (not just a
// Windows job object).
func (s *Sandbox) Isolates() bool {
	return s != nil && (s.Backend == sbSeatbelt || s.Backend == sbBwrap)
}

// PidNamespace: background jobs cannot report host pids with $! (bwrap).
func (s *Sandbox) PidNamespace() bool { return s != nil && s.Backend == sbBwrap }

// Describe is the one-line status for jazz doctor.
func (s *Sandbox) Describe() string {
	if s == nil {
		return "desligado"
	}
	net := "rede liberada"
	if !s.Net {
		net = "rede só loopback"
	}
	switch s.Backend {
	case sbSeatbelt:
		return fmt.Sprintf("sandbox-exec (macOS) · modo %s · %s · escrita só no projeto e temporários", s.Mode, net)
	case sbBwrap:
		return fmt.Sprintf("bubblewrap · modo %s · %s · escrita só no projeto e temporários", s.Mode, net)
	case sbJobObject:
		return fmt.Sprintf("parcial: job object do Windows (limite de processos, sem UI/área de transferência) · modo %s · sem isolamento de arquivos", s.Mode)
	}
	return s.Backend
}

// Wrap rewrites cmd so it starts inside the sandbox. Call it after Dir and
// Env are set and before Start. No-op without isolation, or when exec could
// not resolve the program (cmd.Err keeps its error).
func (s *Sandbox) Wrap(cmd *exec.Cmd) {
	if !s.Isolates() || cmd == nil || cmd.Err != nil || cmd.Path == "" {
		return
	}
	dir := cmd.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	var pre []string
	switch s.Backend {
	case sbSeatbelt:
		s.once.Do(func() { s.profile, s.params = sbSeatbeltProfile(s) })
		pre = []string{s.Bin}
		for _, kv := range s.params {
			pre = append(pre, "-D", kv)
		}
		pre = append(pre, "-p", s.profile, "--")
	case sbBwrap:
		pre = append([]string{s.Bin}, sbBwrapArgs(s, sbReal(dir))...)
		pre = append(pre, "--")
	}
	args := append(pre, cmd.Path)
	if len(cmd.Args) > 1 {
		args = append(args, cmd.Args[1:]...)
	}
	cmd.Path, cmd.Args = s.Bin, args
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, s.Env...)
}

// sbWrapCmd wraps cmd with the active sandbox (if any).
func sbWrapCmd(cmd *exec.Cmd) { sbCurrent().Wrap(cmd) }

// ---------- macOS: seatbelt profile ----------

// sbSeatbeltDenyMach are services that would let a child act outside the
// sandbox or read secrets: keychain, pasteboard, LaunchServices (open -a
// Terminal …), Apple events (osascript tell application …).
var sbSeatbeltDenyMach = []string{"com.apple.SecurityServer", "com.apple.securityd.xpc", "com.apple.security.agent",
	"com.apple.pasteboard.1", "com.apple.coreservices.launchservicesd", "com.apple.coreservices.appleevents",
	"com.apple.coreservices.quarantine-resolver", "com.apple.tccd", "com.apple.tccd.system", "com.apple.ScreenCapture"}

// sbSeatbeltProfile renders the SBPL profile; paths go in as -D parameters
// (no quoting issues with odd directory names). Later rules win in SBPL.
func sbSeatbeltProfile(s *Sandbox) (string, []string) {
	var params []string
	ref := func(p string) string {
		k := "P" + strconv.Itoa(len(params))
		params = append(params, k+"="+p)
		return `(param "` + k + `")`
	}
	filt := func(paths ...[]string) string {
		var b strings.Builder
		for _, list := range paths {
			for _, p := range list {
				if p == "" {
					continue
				}
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					b.WriteString(" (literal " + ref(p) + ")")
				} else {
					b.WriteString(" (subpath " + ref(p) + ")")
				}
			}
		}
		return b.String()
	}
	rule := func(b *strings.Builder, verb, ops, f string) {
		if strings.TrimSpace(f) != "" { // an empty filter would match everything
			fmt.Fprintf(b, "(%s %s%s)\n", verb, ops, f)
		}
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	b.WriteString("(allow process-fork process-exec process-info*)\n(allow signal (target same-sandbox))\n")
	b.WriteString("(allow sysctl-read)\n(allow mach-lookup)\n")
	b.WriteString("(deny mach-lookup")
	for _, m := range sbSeatbeltDenyMach {
		b.WriteString(` (global-name "` + m + `")`)
	}
	b.WriteString(` (global-name-regex #"^com\.apple\.lsd\.")`)
	b.WriteString(")\n")
	b.WriteString("(allow ipc-posix* ipc-sysv*)\n(allow file-ioctl)\n(allow pseudo-tty)\n(allow system-socket)\n(allow user-preference-read)\n")
	// reads: everything, minus $HOME and secrets, plus the project and toolchains
	const rd = "file-read-data file-read-xattr"
	b.WriteString("(allow file-read*)\n")
	var hidden []string
	if s.HideHome {
		hidden = append(hidden, s.Home)
	}
	rule(&b, "deny", rd, filt(hidden, s.Hide))
	rule(&b, "allow", rd, filt(s.Roots, s.CacheRO, s.CacheRW, []string{s.Tmp}))
	rule(&b, "deny", rd, filt(s.Hide))
	rule(&b, "allow", rd, filt(s.ReadOK, s.WriteOK))
	// writes: project, private tmp, caches, spill/jobs, a few devices
	rule(&b, "allow", "file-write*", filt(s.Roots, []string{s.Tmp, s.UserTmp}, s.CacheRW, s.WriteOK)+
		` (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper") (subpath "/dev/fd")`)
	rule(&b, "deny", "file-write*", filt(s.Hide))
	rule(&b, "allow", "file-write*", filt(s.WriteOK))
	// no controlling terminal: no keystroke injection into the user's tty
	b.WriteString(`(deny file-read-data file-write-data file-ioctl (literal "/dev/tty") (regex #"^/dev/ttys[0-9]+$"))` + "\n")
	if s.Net {
		b.WriteString("(allow network*)\n")
		// launchd's per-user sockets (ssh-agent and friends)
		b.WriteString(`(deny network-outbound (remote unix-socket (path-regex #"^/private/tmp/com\.apple\.launchd\.")))` + "\n")
	} else {
		// loopback only; no DNS (mDNSResponder is a way out too); unix
		// sockets only inside the project and the private temp dir
		b.WriteString(`(allow network-bind network-inbound (local ip "localhost:*"))` + "\n")
		b.WriteString(`(allow network-outbound (remote ip "localhost:*"))` + "\n")
		var socks strings.Builder
		for _, p := range append(append([]string(nil), s.Roots...), s.Tmp) {
			if p != "" {
				socks.WriteString(" (subpath " + ref(p) + ")")
			}
		}
		if socks.Len() > 0 {
			b.WriteString("(allow network-outbound (remote unix-socket" + socks.String() + "))\n")
			b.WriteString("(allow network-bind (local unix-socket" + socks.String() + "))\n")
		}
	}
	return b.String(), params
}

// ---------- Linux: bubblewrap ----------

// sbBwrapArgs are the bwrap options (before "--") for a child starting in dir.
// Mount order matters: later binds go on top of earlier ones.
func sbBwrapArgs(s *Sandbox, dir string) []string {
	a := []string{"--die-with-parent", "--unshare-pid", "--new-session"}
	if !s.Net {
		a = append(a, "--unshare-net") // loopback only
	}
	a = append(a, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		a = append(a, "--tmpfs", sbReal(x)) // session bus, agents, pulse
	} else if _, err := os.Stat(fmt.Sprintf("/run/user/%d", os.Getuid())); err == nil {
		a = append(a, "--tmpfs", fmt.Sprintf("/run/user/%d", os.Getuid()))
	}
	if s.HideHome {
		a = append(a, "--tmpfs", s.Home)
		for _, p := range s.CacheRO {
			a = append(a, "--ro-bind-try", p, p)
		}
	}
	for _, p := range s.CacheRW {
		a = append(a, "--bind-try", p, p)
	}
	a = append(a, "--bind", s.Tmp, s.Tmp)
	for _, r := range s.Roots {
		a = append(a, "--bind", r, r)
	}
	for _, p := range s.Hide {
		fi, err := os.Stat(p)
		switch {
		case err != nil:
		case fi.IsDir():
			a = append(a, "--tmpfs", p)
		default:
			a = append(a, "--ro-bind", "/dev/null", p)
		}
	}
	for _, p := range s.ReadOK {
		a = append(a, "--ro-bind-try", p, p)
	}
	for _, p := range s.WriteOK {
		a = append(a, "--bind-try", p, p)
	}
	if dir != "" {
		a = append(a, "--chdir", dir)
	}
	return a
}

// ---------- doctor ----------

// sbDoctor prints the sandbox lines of jazz doctor: backend + status and a
// live check (a sandboxed shell runs, and jazz's config/data files are
// unreadable). verbose also prints the exact wrapper argv; probe prints
// every path the live check tried and what it got.
func sbDoctor(ok func(bool) string, gray func(string) string, verbose, probe bool) {
	st, _ := sbStatus.Load().(string)
	sb := sbCurrent()
	fmt.Printf("  %s sandbox: %s\n", ok(sb.Isolates()), st)
	if sb.Isolates() {
		if verbose || probe {
			fmt.Println("  " + gray("argv: "+sbArgvString(sb)))
		}
		items, err := sbLiveProbe(sb)
		res := sbProbeVerdict(items, err)
		fmt.Printf("  %s sandbox ao vivo: %s\n", ok(res == ""), map[bool]string{true: "shell isolado ok, config e dados do jazz ilegíveis", false: res}[res == ""])
		if probe {
			for _, it := range items {
				fmt.Printf("    %s %s %s\n", ok(it.Got == it.Want), it.Label, it.Path)
				fmt.Println("      " + gray(it.Note))
			}
		}
	} else if probe {
		fmt.Println("  " + gray("sem sandbox ativo: nada para sondar"))
	}
	fmt.Println("  " + gray("teste rápido: jazz --sandbox strict -p 'echo ok'"))
}

// sbArgvString is the wrapper command line a child of the project root gets
// (the SBPL profile is summarized: it is long and multi-line).
func sbArgvString(sb *Sandbox) string {
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = sb.Roots[0]
	sb.Wrap(cmd)
	var parts []string
	for i, a := range cmd.Args {
		if i > 0 && cmd.Args[i-1] == "-p" && sb.Backend == sbSeatbelt {
			a = fmt.Sprintf("<perfil SBPL, %d bytes>", len(a))
		}
		parts = append(parts, sbShQuote(a))
	}
	return strings.Join(parts, " ")
}

// sbShQuote is POSIX single quoting (dash has no $'…').
func sbShQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,+@%") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sbProbeItem is one path the live check tried inside the sandbox.
type sbProbeItem struct {
	Label string // what it is (PT)
	Path  string
	Want  bool // true: must be readable inside
	Exec  bool // also executable (the jazz binary: child shells call jazz)
	Got   bool // readable inside
	Note  string
}

// sbProbeFiles are regular files of jazz's config/data dirs the live check
// tries to read (outside the dirs given back to children), at most max per
// dir: top-level files first (auth.json, config.json), then one level down.
func sbProbeFiles(sb *Sandbox, max int) []string {
	var out []string
	for _, d := range []string{sbReal(configDir()), sbReal(dataDir())} {
		var top, sub []string
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			p := filepath.Join(d, e.Name())
			if sbUnder(p, sb.ReadOK, sb.WriteOK) {
				continue
			}
			if e.Type().IsRegular() {
				top = append(top, p)
			} else if e.IsDir() {
				kids, _ := os.ReadDir(p)
				for _, k := range kids {
					if k.Type().IsRegular() {
						sub = append(sub, filepath.Join(p, k.Name()))
						break // one per subdir is enough
					}
				}
			}
		}
		all := append(top, sub...)
		if len(all) > max {
			all = all[:max]
		}
		for _, p := range all {
			if !sbUnder(p, out) {
				out = append(out, p)
			}
		}
	}
	return out
}

func sbUnder(p string, lists ...[]string) bool {
	for _, l := range lists {
		for _, q := range l {
			if q != "" && pathIn(p, q) {
				return true
			}
		}
	}
	return false
}

// sbLiveProbe runs one sandboxed /bin/sh that tries to read jazz's config
// and data files (plus a canary written to the data dir for the run), lists
// both dirs, and reads a control file in the private temp dir that must stay
// readable. A hidden dir may still exist inside (bwrap mounts an empty tmpfs
// on it, seatbelt allows stat): only its contents count.
func sbLiveProbe(sb *Sandbox) ([]sbProbeItem, error) {
	var items []sbProbeItem
	files := sbProbeFiles(sb, 6)
	if dd := sbReal(dataDir()); os.MkdirAll(dd, 0o700) == nil {
		canary := filepath.Join(dd, fmt.Sprintf(".sbx-canary-%d", os.Getpid()))
		if os.WriteFile(canary, []byte("JAZZ-SBX-CANARY\n"), 0o600) == nil {
			defer os.Remove(canary)
			files = append(files, canary)
		}
	}
	for _, f := range files {
		items = append(items, sbProbeItem{Label: "arquivo do jazz (deve ser ilegível)", Path: f})
	}
	var control string
	if sb.Tmp != "" {
		control = filepath.Join(sb.Tmp, "sbx-control")
		if os.WriteFile(control, []byte("ok\n"), 0o600) == nil {
			defer os.Remove(control)
			items = append(items, sbProbeItem{Label: "controle no temporário privado (deve ser legível)", Path: control, Want: true})
		}
	}
	if exe, err := os.Executable(); err == nil && !strings.HasSuffix(exe, ".test") {
		if exe = sbReal(exe); !sbUnder(exe, sb.Hide) {
			items = append(items, sbProbeItem{Label: "binário do jazz (deve ser executável)", Path: exe, Want: true, Exec: true})
		}
	}
	var dirs []string
	for _, d := range []string{sbReal(configDir()), sbReal(dataDir())} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	var sc strings.Builder
	sc.WriteString("echo SBX-OK\n")
	for i, it := range items {
		x := ""
		if it.Exec {
			x = "[ -x " + sbShQuote(it.Path) + " ] && "
		}
		fmt.Fprintf(&sc, "if %scat %s >/dev/null 2>&1; then echo 'R %d 1'; else echo 'R %d 0'; fi\n", x, sbShQuote(it.Path), i, i)
	}
	for j, d := range dirs {
		fmt.Fprintf(&sc, "ls -A %s 2>/dev/null | while IFS= read -r n; do echo \"L %d $n\"; done\n", sbShQuote(d), j)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", sc.String())
	cmd.Dir = sb.Roots[0]
	cmd.Env = scrubEnv(os.Environ())
	sb.Wrap(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "SBX-OK") {
		if err == nil {
			err = errors.New("saída inesperada")
		}
		return items, fmt.Errorf("%s %v", oneLine(strings.TrimSpace(string(out)), 160), err)
	}
	listed := map[int][]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.SplitN(ln, " ", 3)
		if len(f) < 3 {
			continue
		}
		n, e := strconv.Atoi(f[1])
		if e != nil {
			continue
		}
		switch {
		case f[0] == "R" && n < len(items):
			items[n].Got = f[2] == "1"
		case f[0] == "L" && n < len(dirs):
			listed[n] = append(listed[n], f[2])
		}
	}
	for i := range items {
		switch {
		case items[i].Got && items[i].Want:
			items[i].Note = "legível (esperado)"
		case items[i].Got:
			items[i].Note = "LEGÍVEL dentro do sandbox — vazamento"
		case items[i].Want:
			items[i].Note = "ilegível — o teste não consegue ler nem o que devia"
		default:
			items[i].Note = "ilegível (esperado)"
		}
	}
	// a listing may show the dirs given back (skills, spill, jobs, scripts)
	// and mount points; any other name that exists on the host is a leak
	for j, d := range dirs {
		allowed := map[string]bool{}
		for _, p := range append(append([]string(nil), sb.ReadOK...), sb.WriteOK...) {
			if rel, err := filepath.Rel(d, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				allowed[strings.Split(rel, string(filepath.Separator))[0]] = true
			}
		}
		var leaked []string
		for _, n := range listed[j] {
			if _, err := os.Lstat(filepath.Join(d, n)); err == nil && !allowed[n] {
				leaked = append(leaked, n)
			}
		}
		it := sbProbeItem{Label: "listagem da pasta do jazz (só pode mostrar o liberado)", Path: d, Got: len(leaked) > 0}
		switch {
		case len(leaked) > 0:
			it.Note = "VISÍVEL dentro do sandbox: " + oneLine(strings.Join(leaked, ", "), 120)
		case len(listed[j]) > 0:
			it.Note = "só o liberado: " + oneLine(strings.Join(listed[j], ", "), 120)
		default:
			it.Note = "vazia ou ilegível (esperado)"
		}
		items = append(items, it)
	}
	return items, nil
}

// sbProbeVerdict is "" when every probe got what it wanted, else the reason.
func sbProbeVerdict(items []sbProbeItem, err error) string {
	if err != nil {
		return "o shell isolado não iniciou: " + oneLine(err.Error(), 160)
	}
	for _, it := range items {
		if it.Got != it.Want {
			if it.Exec {
				return "binário do jazz inacessível dentro do sandbox: " + it.Path
			}
			if it.Want {
				return "controle ilegível (teste inconclusivo): " + it.Path
			}
			return "config do jazz legível dentro do sandbox: " + it.Path
		}
	}
	return ""
}

// sbSelfTest runs the live probe; "" means isolated.
func sbSelfTest(sb *Sandbox) string {
	items, err := sbLiveProbe(sb)
	return sbProbeVerdict(items, err)
}
