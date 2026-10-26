package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

type epFakeEnt struct {
	name string
	dir  bool
}

func (e epFakeEnt) Name() string               { return e.name }
func (e epFakeEnt) IsDir() bool                { return e.dir }
func (e epFakeEnt) Type() fs.FileMode          { return 0 }
func (e epFakeEnt) Info() (fs.FileInfo, error) { return nil, errors.New("no") }

// epFakeSys is a Linux box with files, dirs and command outputs in maps.
func epFakeSys(files map[string]string, dirs map[string][]string, cmds map[string]string) epSys {
	return epSys{GOOS: "linux", GOARCH: "386", Home: "/home/u", PathEnv: "/usr/bin:/bin", NumCPU: 1,
		Getenv: func(k string) string { return files["env:"+k] },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		ReadDir: func(p string) ([]os.DirEntry, error) {
			l, ok := dirs[p]
			if !ok {
				return nil, os.ErrNotExist
			}
			var out []os.DirEntry
			for _, n := range l {
				out = append(out, epFakeEnt{name: n})
			}
			return out, nil
		},
		Stat: func(p string) (os.FileInfo, error) {
			if _, ok := files[p]; ok {
				return nil, nil
			}
			return nil, os.ErrNotExist
		},
		LookPath: func(n string) (string, error) {
			for _, b := range dirs["/usr/bin"] {
				if b == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", os.ErrNotExist
		},
		Run: func(_ time.Duration, name string, args ...string) (string, error) {
			k := strings.TrimSpace(name + " " + strings.Join(args, " "))
			if v, ok := cmds[k]; ok {
				return v, nil
			}
			return "", errors.New("not found: " + k)
		},
		Dial: func(string, time.Duration) error { return errors.New("offline") },
	}
}

const epProcTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 0100007F:B5A2 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 3 1 0000000000000000 20 4 30 10 -1
`

const epTSJSON = `{"Self":{"HostName":"labbox","DNSName":"labbox.example.ts.net.","TailscaleIPs":["198.51.100.7"],"Online":true},
"Peer":{"nodekey:a":{"HostName":"macbook","DNSName":"macbook.example.ts.net.","TailscaleIPs":["100.64.0.3"],"Online":true},
"nodekey:b":{"HostName":"localhost","DNSName":"fono.example.ts.net.","TailscaleIPs":["198.51.100.8"],"Online":false}}}`

func epFakeProfile() *epProfile {
	files := map[string]string{
		"/proc/meminfo":                 "MemTotal:        2061260 kB\nMemFree:          100000 kB\nMemAvailable:    1024000 kB\n",
		"/proc/net/tcp":                 epProcTCP,
		"/proc/sys/kernel/osrelease":    "4.19.0-antix\n",
		"/run/systemd/system":           "",
		"/home/u/.ssh/config":           "Host labbox\n  HostName 198.51.100.7\n  User ops\n\nHost vps prod\n  HostName 1.2.3.4\nHost *\n  ServerAliveInterval 30\nInclude extra.conf\n",
		"/home/u/.ssh/extra.conf":       "Host media01\n  HostName 10.0.0.9\n",
		"/etc/service/sshd/supervise/x": "",
	}
	dirs := map[string][]string{
		"/usr/bin": {"go", "git", "python3", "tailscale", "ssh", "systemctl", "ls", "grep", "open", "docker"},
		"/bin":     {"sh", "ls"},
	}
	cmds := map[string]string{
		"/usr/bin/go version":        "go version go1.27.2 linux/386\n",
		"/usr/bin/git --version":     "git version 2.47.1\n",
		"/usr/bin/python3 --version": "Python 3.11.2\n",
		"/usr/bin/tailscale version": "1.80.2\n  tailscale commit: abc\n",
		"/usr/bin/ssh -V":            "OpenSSH_9.2p1 Debian-2, OpenSSL 3.0.15\n",
		"/usr/bin/docker --version":  "Docker version 27.3.1, build ce12230\n",
		"tailscale status --json":    epTSJSON,
		"systemctl list-units --type=service --state=running --no-legend --no-pager --plain": "nginx.service loaded active running A high performance web server\nssh.service loaded active running OpenBSD Secure Shell server\n",
		"docker ps --format {{.Names}}\t{{.Image}}\t{{.Status}}":                             "web\tnginx:1.27\tUp 2 hours\ndb\tpostgres:16\tUp 2 hours\n",
	}
	return epProbe(epFakeSys(files, dirs, cmds))
}

func TestEnvProbeProfile(t *testing.T) {
	p := epFakeProfile()
	if p.OS != "linux" || p.Arch != "386" || p.CPUs != 1 {
		t.Fatalf("os/arch/cpus: %+v", p)
	}
	if p.MemTotalMB != 2012 || p.MemAvailMB != 1000 {
		t.Errorf("meminfo: %d %d", p.MemTotalMB, p.MemAvailMB)
	}
	if len(p.Ports) != 2 || p.Ports[0].Port != 22 || p.Ports[0].Local || p.Ports[1].Port != 8080 || !p.Ports[1].Local {
		t.Errorf("ports (only LISTEN, loopback flag): %+v", p.Ports)
	}
	ver := map[string]string{}
	for _, tc := range p.Toolchains {
		ver[tc.Name] = tc.Version
	}
	for k, want := range map[string]string{"go": "1.27.2", "git": "2.47.1", "python3": "3.11.2", "tailscale": "1.80.2", "docker": "27.3.1"} {
		if ver[k] != want {
			t.Errorf("version %s = %q, want %q", k, ver[k], want)
		}
	}
	var hosts []string
	for _, h := range p.SSHHosts {
		hosts = append(hosts, h.Name)
	}
	if strings.Join(hosts, ",") != "labbox,vps,prod,media01" {
		t.Errorf("ssh hosts (no wildcards, Include followed): %v", hosts)
	}
	if p.SSHHosts[0].User != "ops" || p.SSHHosts[0].HostName != "198.51.100.7" {
		t.Errorf("ssh labbox: %+v", p.SSHHosts[0])
	}
	if len(p.Tailscale) != 3 || !p.Tailscale[0].Self || p.Tailscale[2].Name != "fono" || p.Tailscale[2].Online {
		t.Errorf("tailscale: %+v", p.Tailscale)
	}
	if len(p.Services) < 2 || p.Services[0].Name != "nginx" || p.Services[0].Manager != "systemd" {
		t.Errorf("services: %+v", p.Services)
	}
	if len(p.Containers) != 2 || p.Containers[0].Name != "web" || p.Containers[0].Engine != "docker" {
		t.Errorf("containers: %+v", p.Containers)
	}
	if p.Online {
		t.Error("the dial failed: offline")
	}
	if !strings.Contains(epText(p), "tailscale") || !strings.Contains(epText(p), "8080(local)") {
		t.Errorf("text view:\n%s", epText(p))
	}
}

// Tailscale is probed only when the binary is on PATH.
func TestEnvProbeNoTailscaleWithoutBinary(t *testing.T) {
	called := false
	sys := epFakeSys(map[string]string{}, map[string][]string{"/usr/bin": {"ls"}}, nil)
	run := sys.Run
	sys.Run = func(d time.Duration, n string, a ...string) (string, error) {
		if n == "tailscale" {
			called = true
		}
		return run(d, n, a...)
	}
	p := epProbe(sys)
	if called || len(p.Tailscale) != 0 {
		t.Fatal("tailscale status ran without the binary")
	}
}

func TestEnvProbeContainerAndWSL(t *testing.T) {
	sys := epFakeSys(map[string]string{"/.dockerenv": "", "/proc/sys/kernel/osrelease": "5.15.153.1-microsoft-standard-WSL2"}, nil, nil)
	p := epProbe(sys)
	if p.Container != "docker" || !p.WSL {
		t.Fatalf("container/wsl: %q %v", p.Container, p.WSL)
	}
}

func TestEnvVersionParse(t *testing.T) {
	for in, want := range map[string]string{
		"go version go1.22.1 linux/amd64":              "1.22.1",
		"Python 3.12.0":                                "3.12.0",
		"openjdk version \"17.0.9\" 2023-10-17":        "17.0.9",
		"gcc (Debian 12.2.0-14) 12.2.0":                "12.2.0-14",
		"v20.11.1":                                     "20.11.1",
		"Docker version 27.3.1, build ce12230":         "27.3.1",
		"rustc 1.83.0 (90b35a623 2024-11-26)":          "1.83.0",
		"OpenSSH_9.2p1 Debian-2, OpenSSL 3.0.15 3 Sep": "3.0.15",
	} {
		if got := epVersion(in); got != want {
			t.Errorf("epVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// Environment entities resolve with the slot's type: "talscle" is the
// tailscale binary, "labbox" (and "labbx") the SSH host; nothing is invented.
func TestEnvEntitiesResolve(t *testing.T) {
	defer epSetForTest(epFakeProfile())()
	c := niResolveEntity("", "talscle", []string{"toolchain", "bin"}, "", 5)
	if len(c) == 0 || c[0].Name != "tailscale" || c[0].Kind != "toolchain" {
		t.Fatalf("talscle: %+v", c)
	}
	c = niResolveEntity("", "labbox", []string{"ssh_host", "ts_peer"}, "", 5)
	if len(c) != 1 || c[0].Name != "labbox" || !c[0].Exact || c[0].Kind != "ssh_host" {
		t.Fatalf("labbox: %+v", c)
	}
	c = niResolveEntity("", "labbx", []string{"ssh_host", "ts_peer"}, "", 5)
	if len(c) == 0 || c[0].Name != "labbox" {
		t.Fatalf("labbx: %+v", c)
	}
	// type-aware: the same text as a service is nginx, not a binary
	c = niResolveEntity("", "ngnix", []string{"service"}, "", 5)
	if len(c) == 0 || c[0].Name != "nginx" || c[0].Kind != "service" {
		t.Fatalf("ngnix: %+v", c)
	}
	if c := niResolveEntity("", "zzqxw", []string{"ssh_host", "bin"}, "", 5); len(c) != 0 {
		t.Fatalf("an unknown name must not resolve: %+v", c)
	}
}

func TestEnvCacheInvalidatesOnPathChange(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	defer epSetForTest(nil)()
	p1 := epGet(false)
	if p2 := epGet(false); p2 != p1 {
		t.Fatal("second call within the TTL must hit the cache")
	}
	t.Setenv("PATH", os.Getenv("PATH")+":/nonexistent-jazz")
	if p3 := epGet(false); p3 == p1 || p3.PathSig == p1.PathSig {
		t.Fatal("a PATH change must re-probe")
	}
}

func TestJaroWinkler(t *testing.T) {
	if jw := fzJaroWinkler("martha", "marhta"); jw < 955 || jw > 965 {
		t.Errorf("martha/marhta = %d, want ~961", jw)
	}
	if jw := fzJaroWinkler("abc", "xyz"); jw != 0 {
		t.Errorf("disjoint = %d", jw)
	}
}
