//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Windows-only process, lock, path and shell tests (run by the windows-latest
// CI job).

func winPowerShell(t *testing.T) string {
	t.Helper()
	for _, n := range []string{"pwsh", "powershell"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	t.Skip("no PowerShell")
	return ""
}

// A child that starts a grandchild: procKillTree must take both down (the job
// object), which Process.Kill alone would not.
func TestProcKillTreeKillsGrandchildren(t *testing.T) {
	ps := winPowerShell(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := "$p = Start-Process -PassThru -WindowStyle Hidden " + pwshQuote(ps) + " -ArgumentList '-NoProfile','-Command','Start-Sleep 60'; " +
		"Set-Content -LiteralPath " + pwshQuote(pidFile) + " $p.Id; Start-Sleep 60"
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script)
	procPrepare(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	procAttach(cmd)
	defer procForget(cmd.Process.Pid)
	var child int
	for i := 0; i < 200 && child == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		if b, err := os.ReadFile(pidFile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff")))
		}
	}
	if child == 0 {
		procKillTree(cmd.Process.Pid)
		t.Fatal("grandchild never started")
	}
	if !procAlive(cmd.Process.Pid) || !procAlive(child) {
		t.Fatal("tree should be alive before the kill")
	}
	procKillTree(cmd.Process.Pid)
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("leader survived procKillTree")
	}
	for i := 0; i < 50 && procAlive(child); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if procAlive(child) {
		t.Fatal("grandchild survived procKillTree (job object)")
	}
}

func TestPfTryLockExcludesSecondHandle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := pvFlock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if busy, err := pfTryLock(f); err != nil || !busy {
		t.Fatalf("second lock should be busy: busy=%v err=%v", busy, err)
	}
	unlock()
	if busy, err := pfTryLock(f); err != nil || busy {
		t.Fatalf("lock should be free after unlock: busy=%v err=%v", busy, err)
	}
	pfUnlock(f)
}

func TestWindowsDirsUseAppData(t *testing.T) {
	t.Setenv("JAZZ_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("APPDATA", `C:\Users\u\AppData\Roaming`)
	t.Setenv("LOCALAPPDATA", `C:\Users\u\AppData\Local`)
	if got := configDir(); got != `C:\Users\u\AppData\Roaming\jazz-cli` {
		t.Errorf("configDir = %q", got)
	}
	if got := dataDir(); got != `C:\Users\u\AppData\Local\jazz-cli` {
		t.Errorf("dataDir = %q", got)
	}
}

func TestWindowsPathsAreCaseInsensitive(t *testing.T) {
	if !pathEq(`C:\Users\Me\.SSH`, `c:\users\me\.ssh`) || !pathUnder(`C:\USERS\me\.ssh\id_rsa`, `c:\Users\me\.ssh`) {
		t.Fatal("Windows paths must compare case-insensitively")
	}
}

func TestWindowsEnvScrubIsCaseInsensitive(t *testing.T) {
	pfNoteConfig(&Config{Providers: map[string]*ProviderConf{"x": {KeyEnv: "MY_PROVIDER_KEY_VAR"}}})
	out := scrubEnv([]string{"my_provider_key_var=s3cr3t", "Path=C:\\Windows", "github_token=x"})
	for _, kv := range out {
		if strings.HasPrefix(strings.ToLower(kv), "my_provider_key_var=") || strings.HasPrefix(strings.ToLower(kv), "github_token=") {
			t.Fatalf("secret survived scrubbing: %q", kv)
		}
	}
	if len(out) != 1 {
		t.Fatalf("Path should survive: %q", out)
	}
}

func TestWindowsProtectedPaths(t *testing.T) {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		t.Skip("no APPDATA")
	}
	startup := filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "x.lnk")
	if !protectedPath(startup, "") {
		t.Errorf("startup folder must be protected: %s", startup)
	}
	if home, _ := os.UserHomeDir(); home != "" && !protectedPath(filepath.Join(home, ".ssh", "authorized_keys"), "") {
		t.Error("~/.ssh must be protected")
	}
	if !protectedPath(filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts"), "") {
		t.Error("the Windows directory must be protected")
	}
}

func TestWindowsDangerWrapperUsesBothRuleSets(t *testing.T) {
	if DangerReason(`Remove-Item -Recurse -Force C:\Users\me\proj`) == "" {
		t.Error("PowerShell recursive force delete must be flagged")
	}
	if DangerReason(`rm -rf ~`) == "" {
		t.Error("POSIX rules still apply on Windows")
	}
	if IsReadOnly(`cat C:\Users\me\.ssh\id_rsa`) {
		t.Error("backslash path to a key must not be read-only")
	}
	if SecretReason(`type C:\Users\me\.ssh\id_rsa`) == "" {
		t.Error("reading a key via type must be a secret read")
	}
}

// The shell picked on this machine starts and speaks the marker protocol.
func TestWindowsDetectedShellRuns(t *testing.T) {
	s := NewShell(t.TempDir())
	defer s.Close()
	t.Logf("shell: %s (%s)", s.Dialect().Name(), s.Dialect().Path)
	r := s.Run(context.Background(), "echo jazz-ok", 60*time.Second, nil)
	if r.Reset || r.ExitCode != 0 || !strings.Contains(r.Output, "jazz-ok") {
		t.Fatalf("echo: %+v", r)
	}
	if s.Cwd == "" {
		t.Fatal("cwd not reported")
	}
}

// Background jobs start outside the shell, log to a file and can be killed.
func TestWindowsBackgroundJob(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	tb := NewToolbox(t.TempDir())
	defer tb.Shell.Close()
	cmd := "echo started; Start-Sleep 30"
	if !tb.Shell.Dialect().IsPwsh() {
		cmd = "echo started; sleep 30"
	}
	out := tb.startJob(context.Background(), cmd)
	if out.IsErr {
		t.Fatalf("startJob: %+v", out)
	}
	if tb.RunningJobs() != 1 {
		t.Fatalf("running jobs = %d", tb.RunningJobs())
	}
	if n := tb.KillJobs(); n != 1 {
		t.Fatalf("KillJobs = %d", n)
	}
}
