package main

// envprobe.go: read-only profile of the machine jazz runs on (docs/design/
// offline-brain.md §8). OS/arch, CPUs, RAM, Termux/WSL/container, toolchains
// and their versions, the binaries on PATH, SSH hosts (~/.ssh/config),
// Tailscale peers (`tailscale status --json`, only when the binary exists,
// 2 s), services (systemd/runit/launchd, listing only), containers
// (docker/podman ps), listening TCP ports (/proc/net/tcp, no external tool)
// and whether the network answers.
//
// Nothing here changes the machine: the only processes started are version
// queries and listings, each with a 2 s timeout, one after the other (no
// fan-out: the Dell has one core). The profile is cached in memory and on
// disk (dataDir()/envprobe.json) for epTTL and thrown away when PATH changes.
// It feeds the offline brain as initial facts and as the environment entities
// of the name resolver (nameindex.go niResolveEntity): "talscle" → the
// tailscale binary, "conecta no labbox" → the SSH host labbox.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	epSchema      = 3
	epTTL         = 10 * time.Minute
	epCmdTimeout  = 2 * time.Second
	epMaxPathBins = 6000
	epMaxList     = 200
)

type epTool struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

type epHost struct {
	Name     string `json:"name"`
	HostName string `json:"hostname,omitempty"`
	User     string `json:"user,omitempty"`
}

type epPeer struct {
	Name   string `json:"name"`
	DNS    string `json:"dns,omitempty"`
	IP     string `json:"ip,omitempty"`
	Online bool   `json:"online"`
	Self   bool   `json:"self,omitempty"`
}

type epService struct {
	Name    string `json:"name"`
	State   string `json:"state,omitempty"`
	Manager string `json:"manager"` // systemd | runit | launchd
}

type epContainer struct {
	Name   string `json:"name"`
	Image  string `json:"image,omitempty"`
	Status string `json:"status,omitempty"`
	Engine string `json:"engine"` // docker | podman
}

type epPort struct {
	Port  int  `json:"port"`
	Local bool `json:"local"` // bound to loopback only
}

// epProfile is the environment as jazz saw it at Probed.
type epProfile struct {
	V          int           `json:"v"`
	OS         string        `json:"os"`
	Arch       string        `json:"arch"`
	CPUs       int           `json:"cpus"`
	Host       string        `json:"host,omitempty"`      // hostname
	User       string        `json:"user,omitempty"`      // login running the harness ($USER/$LOGNAME)
	OSName     string        `json:"os_name,omitempty"`   // /etc/os-release PRETTY_NAME
	Kernel     string        `json:"kernel,omitempty"`    // /proc/sys/kernel/osrelease
	CPUModel   string        `json:"cpu_model,omitempty"` // first cpuinfo model name
	Machine    string        `json:"machine,omitempty"`   // DMI "vendor product" ("Dell Inc. Latitude D610")
	UpSecs     int64         `json:"up_secs,omitempty"`   // /proc/uptime
	MemTotalMB int           `json:"mem_total_mb,omitempty"`
	MemAvailMB int           `json:"mem_avail_mb,omitempty"`
	Termux     bool          `json:"termux,omitempty"`
	WSL        bool          `json:"wsl,omitempty"`
	Container  string        `json:"container,omitempty"` // docker | podman | lxc | kubernetes | "" = none
	Toolchains []epTool      `json:"toolchains,omitempty"`
	PathBins   []string      `json:"path_bins,omitempty"`
	PathTrunc  bool          `json:"path_trunc,omitempty"`
	SSHHosts   []epHost      `json:"ssh_hosts,omitempty"`
	Tailscale  []epPeer      `json:"tailscale,omitempty"`
	Services   []epService   `json:"services,omitempty"`
	Containers []epContainer `json:"containers,omitempty"`
	Ports      []epPort      `json:"ports,omitempty"`
	Online     bool          `json:"online"`
	OnlineWhy  string        `json:"online_why,omitempty"`
	Probed     time.Time     `json:"probed"`
	PathSig    string        `json:"path_sig"`
	ProbeMS    int64         `json:"probe_ms"`
	Light      bool          `json:"light,omitempty"` // filesystem-only probe (epGetLight)
}

// epSys is what the probe reads from the system; tests replace every hook.
type epSys struct {
	GOOS, GOARCH string
	Home         string
	PathEnv      string
	Getenv       func(string) string
	ReadFile     func(string) ([]byte, error)
	ReadDir      func(string) ([]os.DirEntry, error)
	Stat         func(string) (os.FileInfo, error)
	LookPath     func(string) (string, error)
	Run          func(timeout time.Duration, name string, args ...string) (string, error)
	Dial         func(addr string, d time.Duration) error
	NumCPU       int
	Hostname     string
}

// epRealRun runs a short read-only query: stdout+stderr (64 KB max), timeout.
func epRealRun(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.WaitDelay = 300 * time.Millisecond
	c.Stdin = nil
	var buf bytes.Buffer
	lw := &epLimitW{w: &buf, n: 64 << 10}
	c.Stdout, c.Stderr = lw, lw
	err := c.Run()
	if ctx.Err() != nil {
		return buf.String(), ctx.Err()
	}
	return buf.String(), err
}

type epLimitW struct {
	w io.Writer
	n int
}

func (l *epLimitW) Write(p []byte) (int, error) {
	k := len(p)
	if l.n <= 0 {
		return k, nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	_, _ = l.w.Write(p)
	return k, nil
}

func epRealSys() epSys {
	h, _ := os.UserHomeDir()
	hn, _ := os.Hostname()
	return epSys{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Home: h, PathEnv: os.Getenv("PATH"), Getenv: os.Getenv,
		ReadFile: os.ReadFile, ReadDir: os.ReadDir, Stat: os.Stat, LookPath: exec.LookPath, Run: epRealRun, Hostname: hn,
		Dial: func(a string, d time.Duration) error {
			c, err := net.DialTimeout("tcp", a, d)
			if err == nil {
				c.Close()
			}
			return err
		}, NumCPU: runtime.NumCPU()}
}

// epPathSig identifies a PATH (and the forced-offline switch): a change
// invalidates the cache.
func epPathSig(path string, offline bool) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s|%v", path, offline)))
	return hex.EncodeToString(s[:8])
}

var (
	epMu    sync.Mutex
	epCache *epProfile
	// epNoNet skips the reachability dial (tests, JAZZ_ENV_NO_NET=1).
	epNoNet = false
)

func epCachePath() string { return filepath.Join(dataDir(), "envprobe.json") }

// epGet returns the cached profile, probing when it is older than epTTL, PATH
// changed, or force is set.
func epGet(force bool) *epProfile {
	epMu.Lock()
	defer epMu.Unlock()
	sig := epPathSig(os.Getenv("PATH"), olForcedEnv())
	fresh := func(p *epProfile) bool {
		return p != nil && p.V == epSchema && p.PathSig == sig && time.Since(p.Probed) < epTTL
	}
	if !force && fresh(epCache) {
		return epCache
	}
	if !force {
		if b, err := os.ReadFile(epCachePath()); err == nil {
			var p epProfile
			if json.Unmarshal(b, &p) == nil && fresh(&p) {
				epCache = &p
				return epCache
			}
		}
	}
	sys := epRealSys()
	if epNoNet || os.Getenv("JAZZ_ENV_NO_NET") == "1" {
		sys.Dial = func(string, time.Duration) error { return fmt.Errorf("sonda de rede desligada") }
	}
	p := epProbe(sys)
	p.PathSig = sig
	epCache = p
	if b, err := json.Marshal(p); err == nil {
		_ = os.MkdirAll(dataDir(), 0o700)
		tmp := epCachePath() + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, epCachePath())
		}
	}
	return p
}

// epGetLight is the profile without running anything: the cached one when
// fresh, otherwise a filesystem-only probe (PATH, ~/.ssh/config, /proc) —
// no version queries, no listings, no dial. Not written to disk. For the
// level-0 answers of jazz -p that must stay under a few hundred ms.
func epGetLight() *epProfile {
	epMu.Lock()
	sig := epPathSig(os.Getenv("PATH"), olForcedEnv())
	if p := epCache; p != nil && p.PathSig == sig && time.Since(p.Probed) < epTTL {
		epMu.Unlock()
		return p
	}
	epMu.Unlock()
	if b, err := os.ReadFile(epCachePath()); err == nil {
		var p epProfile
		if json.Unmarshal(b, &p) == nil && p.V == epSchema && p.PathSig == sig && time.Since(p.Probed) < epTTL {
			epMu.Lock()
			epCache = &p
			epMu.Unlock()
			return &p
		}
	}
	sys := epRealSys()
	sys.Run = func(time.Duration, string, ...string) (string, error) { return "", fmt.Errorf("sonda leve") }
	sys.Dial = func(string, time.Duration) error { return fmt.Errorf("sonda leve: rede não testada") }
	p := epProbe(sys)
	p.PathSig, p.Light = sig, true
	return p
}

// epSetForTest installs a profile (tests of the resolver and the brain).
func epSetForTest(p *epProfile) func() {
	epMu.Lock()
	old := epCache
	if p != nil {
		p.V = epSchema
		p.Probed = time.Now()
		p.PathSig = epPathSig(os.Getenv("PATH"), olForcedEnv())
	}
	epCache = p
	epMu.Unlock()
	return func() { epMu.Lock(); epCache = old; epMu.Unlock() }
}

// epPeek returns the in-memory profile without probing (nil = none yet).
func epPeek() *epProfile {
	epMu.Lock()
	defer epMu.Unlock()
	return epCache
}

// epInstall makes p the cached profile that epPeek/epGet hand out (while it is
// fresh) and returns the previous one. The offline eval installs its synthetic
// profile so name resolution ("talscle", "labbx") sees the same hosts and tools
// on every machine, CI included; the cache file on disk is not touched.
func epInstall(p *epProfile) *epProfile {
	epMu.Lock()
	defer epMu.Unlock()
	prev := epCache
	epCache = p
	return prev
}

// epToolchains are the tools whose version matters to a plan.
var epToolchains = []struct{ name, flag string }{
	{"go", "version"}, {"python3", "--version"}, {"node", "--version"}, {"npm", "--version"}, {"rustc", "--version"},
	{"cargo", "--version"}, {"java", "-version"}, {"gcc", "--version"}, {"clang", "--version"}, {"make", "--version"},
	{"git", "--version"}, {"docker", "--version"}, {"podman", "--version"}, {"tailscale", "version"}, {"ssh", "-V"},
	{"rg", "--version"}, {"pip3", "--version"}, {"deno", "--version"}, {"bun", "--version"}, {"ruby", "--version"},
}

// epProbe builds a profile from sys (sequential, bounded).
func epProbe(sys epSys) *epProfile {
	t0 := time.Now()
	p := &epProfile{V: epSchema, OS: sys.GOOS, Arch: sys.GOARCH, CPUs: sys.NumCPU, Host: sys.Hostname}
	if p.User = sys.Getenv("USER"); p.User == "" {
		p.User = sys.Getenv("LOGNAME")
	}
	p.Termux = sys.Getenv("TERMUX_VERSION") != "" || strings.HasSuffix(filepath.ToSlash(sys.Getenv("PREFIX")), "/com.termux/files/usr")
	if sys.GOOS == "linux" {
		if b, err := sys.ReadFile("/proc/sys/kernel/osrelease"); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
			p.WSL = true
		}
		if sys.Getenv("WSL_DISTRO_NAME") != "" {
			p.WSL = true
		}
		p.Container = epContainerKind(sys)
		p.MemTotalMB, p.MemAvailMB = epMeminfo(sys)
		p.Ports = epListenPorts(sys)
		p.Kernel = epRead1(sys, "/proc/sys/kernel/osrelease")
		p.OSName = epOSRelease(sys)
		p.CPUModel = epCPUModel(sys)
		p.Machine = epMachineModel(sys)
		p.UpSecs = epUptimeSecs(sys)
		if p.Host == "" {
			p.Host = epRead1(sys, "/proc/sys/kernel/hostname")
		}
	} else if sys.GOOS == "darwin" {
		if out, err := sys.Run(epCmdTimeout, "sysctl", "-n", "hw.memsize"); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
				p.MemTotalMB = int(n >> 20)
			}
		}
	}
	p.PathBins, p.PathTrunc = epPathBins(sys)
	have := map[string]bool{}
	for _, b := range p.PathBins {
		have[b] = true
	}
	for _, tc := range epToolchains {
		name := tc.name
		if !have[name] && !have[name+".exe"] {
			continue
		}
		path, err := sys.LookPath(name)
		if err != nil {
			continue
		}
		t := epTool{Name: name, Path: path}
		if out, _ := sys.Run(epCmdTimeout, path, tc.flag); out != "" {
			t.Version = epVersion(out)
		}
		p.Toolchains = append(p.Toolchains, t)
	}
	p.SSHHosts = epSSHHosts(sys)
	if have["tailscale"] || have["tailscale.exe"] {
		p.Tailscale = epTailscale(sys)
	}
	p.Services = epServices(sys, have)
	for _, eng := range []string{"docker", "podman"} {
		if have[eng] || have[eng+".exe"] {
			p.Containers = append(p.Containers, epContainers(sys, eng)...)
		}
	}
	p.Online, p.OnlineWhy = epOnline(sys)
	p.Probed = time.Now()
	p.ProbeMS = time.Since(t0).Milliseconds()
	return p
}

// epContainerKind: are we inside a container (and which)?
func epContainerKind(sys epSys) string {
	if _, err := sys.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if _, err := sys.Stat("/run/.containerenv"); err == nil {
		return "podman"
	}
	if b, err := sys.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "kubepods"):
			return "kubernetes"
		case strings.Contains(s, "/docker"):
			return "docker"
		case strings.Contains(s, "libpod"):
			return "podman"
		case strings.Contains(s, "/lxc"):
			return "lxc"
		}
	}
	return ""
}

// epMeminfo reads MemTotal and MemAvailable from /proc/meminfo (MB).
func epMeminfo(sys epSys) (total, avail int) {
	b, err := sys.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "MemTotal:":
			total = n / 1024
		case "MemAvailable:":
			avail = n / 1024
		}
	}
	return total, avail
}

// epRead1 is the first line of a proc/sys file ("" when absent).
func epRead1(sys epSys, path string) string {
	b, err := sys.ReadFile(path)
	if err != nil {
		return ""
	}
	s, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(s)
}

// epOSRelease is PRETTY_NAME of /etc/os-release ("Ubuntu 24.04 LTS").
func epOSRelease(sys epSys) string {
	b, err := sys.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(ln, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// epCPUModel is the first cpuinfo model ("model name"; ARM: "Model"/"Hardware").
func epCPUModel(sys epSys) string {
	b, err := sys.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	fallback := ""
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model":
			return strings.TrimSpace(v)
		case "Hardware":
			if fallback == "" {
				fallback = strings.TrimSpace(v)
			}
		}
	}
	return fallback
}

// epDMIJunk: DMI strings vendors leave unfilled (exact match after folding).
func epDMIJunk(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return true
	}
	for _, j := range []string{"to be filled by o.e.m.", "not specified", "not available", "none", "unknown", "system product name", "product name", "default string"} {
		if s == j {
			return true
		}
	}
	return false
}

// epMachineModel is the DMI product ("Dell Inc. Latitude D610").
func epMachineModel(sys epSys) string {
	name := epRead1(sys, "/sys/class/dmi/id/product_name")
	if epDMIJunk(name) {
		return ""
	}
	vendor := epRead1(sys, "/sys/class/dmi/id/sys_vendor")
	if epDMIJunk(vendor) {
		return name
	}
	if !strings.Contains(strings.ToLower(name), strings.ToLower(vendor)) {
		return vendor + " " + name
	}
	return name
}

// epUptimeSecs parses the first field of /proc/uptime.
func epUptimeSecs(sys epSys) int64 {
	f, _, _ := strings.Cut(epRead1(sys, "/proc/uptime"), " ")
	d, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return 0
	}
	return int64(d)
}

// epUpText renders an uptime: "3d5h", "40m".
func epUpText(s int64) string {
	switch {
	case s >= 86400:
		return fmt.Sprintf("%dd%dh", s/86400, (s%86400)/3600)
	case s >= 3600:
		return fmt.Sprintf("%dh%dm", s/3600, (s%3600)/60)
	case s >= 60:
		return fmt.Sprintf("%dm", s/60)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// epListenPorts parses /proc/net/tcp and tcp6: state 0A = LISTEN.
func epListenPorts(sys epSys) []epPort {
	seen := map[int]int{} // port -> index
	var out []epPort
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := sys.ReadFile(f)
		if err != nil {
			continue
		}
		out = epParseProcTCP(b, seen, out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// epParseProcTCP adds the LISTEN sockets of one /proc/net/tcp* file.
func epParseProcTCP(b []byte, seen map[int]int, out []epPort) []epPort {
	sc := bufio.NewScanner(bytes.NewReader(b))
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 {
			continue
		}
		// loopback: 0100007F (v4) or ...01000000 (v6 ::1)
		local := addr == "0100007F" || addr == "00000000000000000000000001000000"
		if i, ok := seen[int(port)]; ok {
			out[i].Local = out[i].Local && local
			continue
		}
		seen[int(port)] = len(out)
		out = append(out, epPort{Port: int(port), Local: local})
	}
	return out
}

// epPathBins lists the names in the PATH directories (no stat per entry).
func epPathBins(sys epSys) ([]string, bool) {
	sep := ":"
	if sys.GOOS == "windows" {
		sep = ";"
	}
	seen := map[string]bool{}
	out := make([]string, 0, 1024)
	trunc := false
	for _, d := range strings.Split(sys.PathEnv, sep) {
		if d == "" {
			continue
		}
		ents, err := sys.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if sys.GOOS == "windows" {
				ext := strings.ToLower(filepath.Ext(n))
				if ext != ".exe" && ext != ".bat" && ext != ".cmd" && ext != ".ps1" {
					continue
				}
				n = strings.TrimSuffix(n, filepath.Ext(n))
			}
			if seen[n] || strings.HasPrefix(n, ".") {
				continue
			}
			if len(out) >= epMaxPathBins {
				trunc = true
				break
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, trunc
}

// epVersion extracts the first version-looking token (1.2, 1.2.3, go1.22.1).
func epVersion(s string) string {
	for _, f := range strings.Fields(s) {
		t := strings.Trim(f, "(),;\"'v")
		t = strings.TrimPrefix(t, "go")
		dot := strings.IndexByte(t, '.')
		if dot <= 0 || t[0] < '0' || t[0] > '9' {
			continue
		}
		end := 0
		for end < len(t) && (t[end] >= '0' && t[end] <= '9' || t[end] == '.' || t[end] == '-' || t[end] >= 'a' && t[end] <= 'z' && end > dot) {
			end++
		}
		if end > dot+1 {
			return t[:end]
		}
	}
	return "" // no version in the output (a stub, an error): unknown, not the text
}

// epSSHHosts parses Host/HostName/User of ~/.ssh/config (no wildcards; one
// level of Include for files inside ~/.ssh). Keys are never read.
func epSSHHosts(sys epSys) []epHost {
	if sys.Home == "" {
		return nil
	}
	var out []epHost
	seen := map[string]bool{}
	var parse func(p string, depth int)
	parse = func(p string, depth int) {
		b, err := sys.ReadFile(p)
		if err != nil || len(b) > 1<<20 {
			return
		}
		var cur []int // indexes of the hosts of the current block
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") {
				continue
			}
			k, v := ln, ""
			if i := strings.IndexAny(ln, " \t="); i > 0 {
				k, v = ln[:i], strings.TrimSpace(strings.TrimLeft(ln[i:], " \t="))
			}
			switch strings.ToLower(k) {
			case "include":
				if depth == 0 {
					for _, inc := range strings.Fields(v) {
						if !filepath.IsAbs(inc) && !strings.HasPrefix(inc, "~") {
							inc = filepath.Join(sys.Home, ".ssh", inc)
						}
						inc = strings.Replace(inc, "~", sys.Home, 1)
						if strings.ContainsAny(inc, "*?") {
							continue
						}
						parse(inc, depth+1)
					}
				}
			case "host":
				cur = cur[:0]
				for _, h := range strings.Fields(v) {
					if strings.ContainsAny(h, "*?!") || seen[h] {
						continue
					}
					seen[h] = true
					cur = append(cur, len(out))
					out = append(out, epHost{Name: h})
				}
			case "match":
				cur = cur[:0]
			case "hostname":
				for _, i := range cur {
					out[i].HostName = v
				}
			case "user":
				for _, i := range cur {
					out[i].User = v
				}
			}
			if len(out) >= epMaxList {
				return
			}
		}
	}
	parse(filepath.Join(sys.Home, ".ssh", "config"), 0)
	return out
}

// epTailscale reads the peers from `tailscale status --json` (read-only).
func epTailscale(sys epSys) []epPeer {
	out, err := sys.Run(epCmdTimeout, "tailscale", "status", "--json")
	if err != nil || out == "" {
		return nil
	}
	return epParseTailscale([]byte(out))
}

func epParseTailscale(b []byte) []epPeer {
	type node struct {
		HostName     string   `json:"HostName"`
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	}
	var st struct {
		Self *node           `json:"Self"`
		Peer map[string]node `json:"Peer"`
	}
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	conv := func(n node, self bool) epPeer {
		p := epPeer{Name: strings.ToLower(n.HostName), Online: n.Online || self, Self: self}
		if d := strings.TrimSuffix(n.DNSName, "."); d != "" {
			p.DNS = d
			if p.Name == "" || p.Name == "localhost" {
				p.Name, _, _ = strings.Cut(d, ".")
			}
		}
		if len(n.TailscaleIPs) > 0 {
			p.IP = n.TailscaleIPs[0]
		}
		return p
	}
	var out []epPeer
	if st.Self != nil {
		out = append(out, conv(*st.Self, true))
	}
	keys := make([]string, 0, len(st.Peer))
	for k := range st.Peer {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= epMaxList {
			break
		}
		out = append(out, conv(st.Peer[k], false))
	}
	return out
}

// epServices lists running services: systemd units, runit service dirs,
// launchd user agents. Listing only, never a state change.
func epServices(sys epSys, have map[string]bool) []epService {
	var out []epService
	switch sys.GOOS {
	case "linux":
		if _, err := sys.Stat("/run/systemd/system"); err == nil && have["systemctl"] {
			o, err := sys.Run(epCmdTimeout, "systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--no-pager", "--plain")
			if err == nil {
				for _, ln := range strings.Split(o, "\n") {
					f := strings.Fields(ln)
					if len(f) == 0 || !strings.HasSuffix(f[0], ".service") {
						continue
					}
					out = append(out, epService{Name: strings.TrimSuffix(f[0], ".service"), State: "running", Manager: "systemd"})
					if len(out) >= epMaxList {
						break
					}
				}
			}
		}
		for _, d := range []string{"/etc/service", "/var/service", "/run/runit/service", "/etc/runit/runsvdir/default"} {
			ents, err := sys.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range ents {
				st := "?"
				if b, err := sys.ReadFile(filepath.Join(d, e.Name(), "supervise", "stat")); err == nil {
					st = strings.TrimSpace(string(b))
				}
				out = append(out, epService{Name: e.Name(), State: st, Manager: "runit"})
			}
			break // the first runit dir that exists is the live one
		}
	case "darwin":
		o, err := sys.Run(epCmdTimeout, "launchctl", "list")
		if err == nil {
			for _, ln := range strings.Split(o, "\n") {
				f := strings.Fields(ln)
				if len(f) < 3 || f[0] == "PID" || strings.HasPrefix(f[2], "com.apple.") || strings.HasPrefix(f[2], "application.") {
					continue
				}
				st := "loaded"
				if f[0] != "-" {
					st = "running"
				}
				out = append(out, epService{Name: f[2], State: st, Manager: "launchd"})
				if len(out) >= epMaxList {
					break
				}
			}
		}
	}
	return out
}

// epContainers lists running containers of one engine (`ps`, 2 s).
func epContainers(sys epSys, engine string) []epContainer {
	o, err := sys.Run(epCmdTimeout, engine, "ps", "--format", "{{.Names}}\t{{.Image}}\t{{.Status}}")
	if err != nil {
		return nil
	}
	var out []epContainer
	for _, ln := range strings.Split(o, "\n") {
		f := strings.Split(strings.TrimSpace(ln), "\t")
		if len(f) < 1 || f[0] == "" {
			continue
		}
		c := epContainer{Name: f[0], Engine: engine}
		if len(f) > 1 {
			c.Image = f[1]
		}
		if len(f) > 2 {
			c.Status = f[2]
		}
		out = append(out, c)
		if len(out) >= epMaxList {
			break
		}
	}
	return out
}

// epOnline: forced offline, no usable interface, or one TCP dial (700 ms).
func epOnline(sys epSys) (bool, string) {
	if v := strings.TrimSpace(sys.Getenv("JAZZ_OFFLINE")); v != "" && !olOffWord(v) {
		return false, "forçado (JAZZ_OFFLINE)"
	}
	if err := sys.Dial("1.1.1.1:443", 700*time.Millisecond); err != nil {
		return false, oneLine(err.Error(), 80)
	}
	return true, "1.1.1.1:443 respondeu"
}

// ---------- environment entities (for the name resolver) ----------

// epEntity is one named thing of the environment.
type epEntity struct {
	Kind   string // bin | toolchain | ssh_host | ts_peer | service | container | port
	Name   string
	Key    string // fzKey(Name)
	Detail string
}

// epGenericHost: hostnames that are also ordinary words in a request
// ("instala no ubuntu") — never spotted as the local host entity.
var epGenericHost = map[string]bool{"localhost": true, "ubuntu": true, "debian": true, "linux": true, "arch": true,
	"fedora": true, "mint": true, "kali": true, "android": true, "termux": true, "server": true, "host": true,
	"machine": true, "maquina": true, "desktop": true, "laptop": true, "notebook": true, "computer": true}

// epEntities flattens a profile into resolvable names. Toolchains come first
// (they are the "frequent" names that win ties).
func epEntities(p *epProfile) []epEntity {
	if p == nil {
		return nil
	}
	out := make([]epEntity, 0, len(p.PathBins)+len(p.SSHHosts)+len(p.Tailscale)+len(p.Services)+len(p.Containers)+16)
	add := func(kind, name, detail string) {
		if name == "" {
			return
		}
		out = append(out, epEntity{Kind: kind, Name: name, Key: fzKey(name), Detail: detail})
	}
	for _, t := range p.Toolchains {
		add("toolchain", t.Name, strings.TrimSpace(t.Version+" "+t.Path))
	}
	for _, h := range p.SSHHosts {
		d := h.HostName
		if h.User != "" {
			d = h.User + "@" + d
		}
		add("ssh_host", h.Name, d)
	}
	for _, t := range p.Tailscale {
		st := "offline"
		if t.Online {
			st = "online"
		}
		add("ts_peer", t.Name, strings.TrimSpace(t.IP+" "+st))
	}
	if len(p.Host) >= 3 && !epGenericHost[strings.ToLower(p.Host)] { // the machine itself is a host the user may name ("e o labbox?")
		add("ts_peer", p.Host, "esta máquina")
	}
	for _, s := range p.Services {
		add("service", s.Name, s.Manager+" "+s.State)
	}
	for _, c := range p.Containers {
		add("container", c.Name, c.Engine+" "+c.Image)
	}
	for _, pt := range p.Ports {
		add("port", strconv.Itoa(pt.Port), "LISTEN")
	}
	for _, b := range p.PathBins {
		if len(b) >= 2 {
			add("bin", b, "PATH")
		}
	}
	return out
}

// ---------- jazz env ----------

// cmdEnv implements `jazz env [--json] [--refresh]`.
func cmdEnv(args []string, w io.Writer) int {
	raw, force := false, false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			raw = true
		case "--refresh", "-refresh", "-r":
			force = true
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz env [--json] [--refresh]")
			return 2
		}
	}
	p := epGet(force)
	if raw {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Fprintln(w, string(b))
		return 0
	}
	fmt.Fprint(w, epText(p))
	return 0
}

// epText is the human view of a profile.
func epText(p *epProfile) string {
	var b strings.Builder
	flags := []string{}
	if p.Termux {
		flags = append(flags, "Termux")
	}
	if p.WSL {
		flags = append(flags, "WSL")
	}
	if p.Container != "" {
		flags = append(flags, "contêiner "+p.Container)
	}
	var mi []string
	if p.Host != "" {
		mi = append(mi, p.Host)
	}
	if p.User != "" {
		mi = append(mi, "usuário "+p.User)
	}
	if p.Machine != "" {
		mi = append(mi, p.Machine)
	}
	if p.OSName != "" {
		mi = append(mi, p.OSName)
	}
	if p.Kernel != "" {
		mi = append(mi, "kernel "+p.Kernel)
	}
	if p.CPUModel != "" {
		mi = append(mi, p.CPUModel)
	}
	if p.UpSecs > 0 {
		mi = append(mi, "ligada há "+epUpText(p.UpSecs))
	}
	if len(mi) > 0 {
		fmt.Fprintf(&b, "máquina   %s\n", strings.Join(mi, " · "))
	}
	fmt.Fprintf(&b, "sistema   %s/%s · %d núcleo(s)", p.OS, p.Arch, p.CPUs)
	if p.MemTotalMB > 0 {
		fmt.Fprintf(&b, " · RAM %d MB", p.MemTotalMB)
		if p.MemAvailMB > 0 {
			fmt.Fprintf(&b, " (%d livres)", p.MemAvailMB)
		}
	}
	if len(flags) > 0 {
		b.WriteString(" · " + strings.Join(flags, ", "))
	}
	b.WriteString("\n")
	net := "offline"
	if p.Online {
		net = "online"
	}
	fmt.Fprintf(&b, "rede      %s (%s)\n", net, p.OnlineWhy)
	if len(p.Toolchains) > 0 {
		var t []string
		for _, x := range p.Toolchains {
			s := x.Name
			if x.Version != "" {
				s += " " + x.Version
			}
			t = append(t, s)
		}
		fmt.Fprintf(&b, "ferramentas %s\n", strings.Join(t, " · "))
	}
	trunc := ""
	if p.PathTrunc {
		trunc = "+ (cortado)"
	}
	fmt.Fprintf(&b, "PATH      %d binários%s\n", len(p.PathBins), trunc)
	if len(p.SSHHosts) > 0 {
		var h []string
		for _, x := range p.SSHHosts {
			h = append(h, x.Name)
		}
		fmt.Fprintf(&b, "ssh       %s\n", strings.Join(h, " · "))
	}
	if len(p.Tailscale) > 0 {
		var h []string
		for _, x := range p.Tailscale {
			s := x.Name
			if x.Self {
				s += " (este)"
			} else if !x.Online {
				s += " (offline)"
			}
			h = append(h, s)
		}
		fmt.Fprintf(&b, "tailscale %s\n", strings.Join(h, " · "))
	}
	if len(p.Services) > 0 {
		var h []string
		for i, x := range p.Services {
			if i == 12 {
				h = append(h, fmt.Sprintf("+%d", len(p.Services)-12))
				break
			}
			h = append(h, x.Name)
		}
		fmt.Fprintf(&b, "serviços  %s (%s)\n", strings.Join(h, " · "), p.Services[0].Manager)
	}
	if len(p.Containers) > 0 {
		var h []string
		for _, x := range p.Containers {
			h = append(h, x.Name+" ("+x.Engine+")")
		}
		fmt.Fprintf(&b, "contêineres %s\n", strings.Join(h, " · "))
	}
	if len(p.Ports) > 0 {
		var h []string
		for _, x := range p.Ports {
			s := strconv.Itoa(x.Port)
			if x.Local {
				s += "(local)"
			}
			h = append(h, s)
		}
		fmt.Fprintf(&b, "portas    %s\n", strings.Join(h, " "))
	}
	age := time.Since(p.Probed).Round(time.Second)
	fmt.Fprintf(&b, "sondado há %s em %d ms (jazz env --refresh sonda de novo)\n", age, p.ProbeMS)
	return b.String()
}
