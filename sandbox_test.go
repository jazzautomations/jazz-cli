package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSandboxResolveMode(t *testing.T) {
	t.Setenv("JAZZ_SANDBOX", "")
	cases := []struct {
		flag, env, cfg, want string
	}{
		{"", "", "", "auto"},
		{"", "", "strict", "strict"},
		{"", "off", "strict", "off"},
		{"strict", "off", "", "strict"},
		{"OFF", "", "", "off"},
	}
	for _, c := range cases {
		t.Setenv("JAZZ_SANDBOX", c.env)
		got, err := sbResolveMode(c.flag, &Config{Sandbox: c.cfg})
		if err != nil || got != c.want {
			t.Errorf("flag=%q env=%q cfg=%q: got %q, %v; want %q", c.flag, c.env, c.cfg, got, err, c.want)
		}
	}
	if _, err := sbResolveMode("maybe", nil); err == nil {
		t.Error("invalid mode accepted")
	}
	t.Setenv("JAZZ_SANDBOX_NET", "")
	if !sbNetAllowed("auto", &Config{}) || sbNetAllowed("strict", &Config{}) {
		t.Error("default net: on in auto, off in strict")
	}
	if !sbNetAllowed("strict", &Config{SandboxNet: "on"}) || sbNetAllowed("auto", &Config{SandboxNet: "off"}) {
		t.Error("sandbox_net must override the mode default")
	}
}

// sbTestPolicy builds a policy over a fake home with secrets.
func sbTestPolicy(t *testing.T, mode, backend string, net bool) (sb *Sandbox, home, proj, jhome string) {
	t.Helper()
	// not t.TempDir(): the macOS per-user temp dir (where it lives) is
	// writable inside the sandbox, and the "outside" probes must not be
	base, err := os.MkdirTemp("/tmp", "jzsbx-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home = filepath.Join(base, "home")
	proj = filepath.Join(base, "proj")
	jhome = filepath.Join(base, "jazzhome")
	for _, d := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "go", "pkg", "mod"), proj, filepath.Join(jhome, "data")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte("SECRET-KNOWN-HOSTS\n"), 0o600)
	os.WriteFile(filepath.Join(home, "notes.txt"), []byte("SECRET-HOME-NOTE\n"), 0o600)
	os.WriteFile(filepath.Join(jhome, "auth.json"), []byte(`{"k":"SECRET-AUTH"}`), 0o600)
	t.Setenv("HOME", home)
	t.Setenv("JAZZ_HOME", jhome)
	sb = sbNew(mode, backend, "/usr/bin/"+backend, net, []string{proj})
	return sb, sbReal(home), sbReal(proj), sbReal(jhome)
}

func TestSandboxSeatbeltProfileShape(t *testing.T) {
	sb, home, proj, jhome := sbTestPolicy(t, "auto", sbSeatbelt, true)
	sb.Tmp = filepath.Join(t.TempDir(), "tmp")
	prof, params := sbSeatbeltProfile(sb)
	if !strings.HasPrefix(prof, "(version 1)\n(deny default)\n") {
		t.Fatalf("profile must deny by default:\n%s", prof)
	}
	for _, line := range strings.Split(prof, "\n") {
		// a rule with operations but no filter would match every path
		for _, op := range []string{"(deny file-read-data file-read-xattr)", "(allow file-read-data file-read-xattr)", "(allow file-write*)", "(deny file-write*)"} {
			if strings.TrimSpace(line) == op {
				t.Errorf("unfiltered rule %q", line)
			}
		}
	}
	joined := strings.Join(params, "\n")
	for _, want := range []string{home, proj, jhome, filepath.Join(home, ".ssh"), sb.Tmp} {
		if !strings.Contains(joined, "="+want+"\n") && !strings.HasSuffix(joined, "="+want) {
			t.Errorf("profile params miss %s:\n%s", want, joined)
		}
	}
	if !strings.Contains(prof, "(allow network*)") {
		t.Error("auto must allow network")
	}
	sb.Net = false
	prof, _ = sbSeatbeltProfile(sb)
	if strings.Contains(prof, "(allow network*)\n") || !strings.Contains(prof, `localhost:*`) {
		t.Error("net off must allow only loopback")
	}
}

func TestSandboxBwrapArgsOrder(t *testing.T) {
	sb, home, proj, jhome := sbTestPolicy(t, "strict", sbBwrap, false)
	sb.Tmp = sbReal(t.TempDir())
	a := sbBwrapArgs(sb, proj)
	s := strings.Join(a, " ")
	idx := func(sub string) int {
		i := strings.Index(s, sub)
		if i < 0 {
			t.Fatalf("missing %q in %s", sub, s)
		}
		return i
	}
	if idx("--unshare-net") < 0 || idx("--die-with-parent") < 0 || idx("--unshare-pid") < 0 {
		t.Fatal("namespaces")
	}
	if !(idx("--tmpfs "+home) < idx("--bind "+proj+" "+proj)) {
		t.Error("home must be hidden before the project is bound")
	}
	if !(idx("--bind "+proj+" "+proj) < idx("--tmpfs "+filepath.Join(home, ".ssh")) && idx("--tmpfs "+jhome) > 0) {
		t.Error("secrets and the jazz dir must be hidden after the roots")
	}
	if !strings.HasSuffix(s, "--chdir "+proj) {
		t.Error("chdir last")
	}
	if strings.Contains(s, "--bind-try "+filepath.Join(home, "go", "pkg", "mod")) {
		t.Error("strict: caches are not writable")
	}
	sb2, _, _, _ := sbTestPolicy(t, "auto", sbBwrap, true)
	sb2.Tmp = sb.Tmp
	if s2 := strings.Join(sbBwrapArgs(sb2, ""), " "); strings.Contains(s2, "--unshare-net") || !strings.Contains(s2, "go/pkg/mod") {
		t.Error("auto: network on, module cache writable")
	}
}

// sbLive activates a real backend for the test or skips.
func sbLive(t *testing.T, mode string, net bool) (sb *Sandbox, home, proj, jhome string) {
	t.Helper()
	backend, bin, why := sbProbe()
	if backend != sbSeatbelt && backend != sbBwrap {
		if runtime.GOOS == "darwin" {
			t.Fatalf("macOS must have a working sandbox-exec: %s", why)
		}
		t.Skip("no sandbox backend: " + why)
	}
	sb, home, proj, jhome = sbTestPolicy(t, mode, backend, net)
	sb.Bin = bin
	if err := sb.prepare(); err != nil {
		t.Fatal(err)
	}
	sbActive.Store(sb)
	t.Cleanup(func() { sbActive.Store(nil); sb.Close() })
	return
}

func TestSandboxShellBlocksSecrets(t *testing.T) {
	_, home, proj, jhome := sbLive(t, "auto", true)
	sh := NewShell(proj)
	defer sh.Close()
	run := func(c string) ShellResult {
		return sh.Run(context.Background(), c, 20*time.Second, nil)
	}
	for _, c := range []string{
		"cat ~/.ssh/known_hosts",
		"cat " + bashQuote(filepath.Join(home, ".ssh", "known_hosts")),
		"cat ~/notes.txt",
		// bwrap hides a dir with an empty tmpfs (ls succeeds, shows nothing);
		// seatbelt denies the readdir: either way auth.json must not show
		"ls -A " + bashQuote(jhome) + " | grep auth",
		"cat " + bashQuote(filepath.Join(jhome, "auth.json")),
		"ls -A \"$JAZZ_HOME\" | grep auth",
	} {
		r := run(c)
		if r.ExitCode == 0 || strings.Contains(r.Output, "SECRET") {
			t.Errorf("%s: must fail inside the sandbox, got exit %d: %s", c, r.ExitCode, r.Output)
		}
	}
	for _, c := range []string{"touch ~/planted", "touch " + bashQuote(filepath.Join(jhome, "planted")), "touch " + bashQuote(filepath.Join(filepath.Dir(proj), "outside"))} {
		if r := run(c); r.ExitCode == 0 {
			t.Errorf("%s: write outside the project must fail", c)
		}
	}
	// normal work: write in the project, persistent cwd/env, temp dir, spill dir
	if r := run("mkdir -p sub && cd sub && echo hi > f.txt && cat f.txt && export JZX=1"); r.ExitCode != 0 || strings.TrimSpace(r.Output) != "hi" {
		t.Fatalf("project write: %d %q %s", r.ExitCode, r.Output, r.Note)
	}
	if want := filepath.Join(proj, "sub"); sbReal(sh.Cwd) != want {
		t.Errorf("cwd reporting: %q, want %q", sh.Cwd, want)
	}
	if r := run("echo $JZX; t=$(mktemp) && echo tmp > \"$t\" && cat \"$t\""); strings.TrimSpace(r.Output) != "1\ntmp" {
		t.Errorf("env persistence / private tmp: %q", r.Output)
	}
	if r := run("echo spill > " + bashQuote(filepath.Join(spillDir(), "x.txt")) + " && cat " + bashQuote(filepath.Join(spillDir(), "x.txt"))); strings.TrimSpace(r.Output) != "spill" {
		t.Errorf("spill dir must stay writable: %d %q", r.ExitCode, r.Output)
	}
	// reset, never repair: a new shell after exit is sandboxed again
	if r := run("exit 3"); !r.Reset {
		t.Fatalf("exit must reset: %+v", r)
	}
	if r := run("cat ~/.ssh/known_hosts"); r.ExitCode == 0 || strings.Contains(r.Output, "SECRET") {
		t.Errorf("fresh shell after reset escaped the sandbox: %q", r.Output)
	}
}

// The real ~/.ssh of the machine running the tests (as the task asks): the
// project lives in a temp dir, so the whole home is hidden.
func TestSandboxRealHomeSSH(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	kh := filepath.Join(realHome, ".ssh", "known_hosts")
	if _, err := os.Stat(kh); err != nil {
		t.Skip("no ~/.ssh/known_hosts on this machine")
	}
	backend, bin, why := sbProbe()
	if backend != sbSeatbelt && backend != sbBwrap {
		t.Skip("no sandbox backend: " + why)
	}
	proj := t.TempDir()
	t.Setenv("JAZZ_HOME", t.TempDir())
	sb := sbNew("auto", backend, bin, true, []string{proj})
	if err := sb.prepare(); err != nil {
		t.Fatal(err)
	}
	sbActive.Store(sb)
	defer func() { sbActive.Store(nil); sb.Close() }()
	sh := NewShell(proj)
	defer sh.Close()
	r := sh.Run(context.Background(), "cat ~/.ssh/known_hosts | head -c 64; ls ~/.ssh", 20*time.Second, nil)
	if r.ExitCode == 0 || !strings.Contains(r.Output, "ermi") && !strings.Contains(r.Output, "No such") {
		t.Errorf("real ~/.ssh readable inside the sandbox: exit %d %q", r.ExitCode, r.Output)
	}
	if r := sh.Run(context.Background(), "echo ok", 10*time.Second, nil); strings.TrimSpace(r.Output) != "ok" {
		t.Errorf("shell broken: %q", r.Output)
	}
}

func TestSandboxStrictNetworkAndJobsAndHooks(t *testing.T) {
	_, _, proj, jhome := sbLive(t, "strict", false)
	if _, err := lookPath("python3"); err != nil {
		t.Skip("python3 missing")
	}
	tb := NewToolbox(proj)
	defer tb.Shell.Close()
	defer tb.KillJobs()
	// outbound network refused at the OS level (no packet needed to tell)
	r := tb.Shell.Run(context.Background(), `python3 -I -c "
import socket
s=socket.socket(); s.settimeout(3)
try:
    s.connect(('1.1.1.1', 443)); print('CONNECTED')
except OSError as e:
    print('blocked', e.errno)
"`, 20*time.Second, nil)
	if !strings.Contains(r.Output, "blocked") {
		t.Errorf("strict must block outbound network: %q", r.Output)
	}
	// loopback still works (test servers)
	r = tb.Shell.Run(context.Background(), `python3 -I -c "
import socket
s=socket.socket(); s.bind(('127.0.0.1',0)); s.listen(1)
c=socket.socket(); c.connect(s.getsockname()); print('loop ok')
"`, 20*time.Second, nil)
	if !strings.Contains(r.Output, "loop ok") {
		t.Errorf("loopback must work under strict: %q", r.Output)
	}
	// background job: sandboxed too, and still killable
	out := tb.startJob(context.Background(), "cat "+bashQuote(filepath.Join(jhome, "auth.json"))+" > leak.txt 2>&1; echo done >> leak.txt; sleep 30")
	if out.IsErr {
		t.Fatalf("job: %s", out.Text)
	}
	deadline := time.Now().Add(5 * time.Second)
	var b []byte
	for time.Now().Before(deadline) {
		b, _ = os.ReadFile(filepath.Join(proj, "leak.txt"))
		if strings.Contains(string(b), "done") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(string(b), "done") || strings.Contains(string(b), "SECRET") {
		t.Errorf("background job not sandboxed (or did not run): %q", b)
	}
	if n := tb.KillJobs(); n != 1 {
		t.Errorf("job kill: %d", n)
	}
	// hooks run under the same wrapper
	hs := &HookSet{root: proj}
	code, stdout, _, err := hs.exec(context.Background(), HookCmd{Command: "cat " + bashQuote(filepath.Join(jhome, "auth.json"))}, nil)
	if err != nil || code == 0 || strings.Contains(stdout, "SECRET") {
		t.Errorf("hook read jazz's auth.json: code %d err %v out %q", code, err, stdout)
	}
}
