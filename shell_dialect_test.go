package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// Platform-neutral tests of the shell dialects (shell_dialect.go). The ones
// that need a real PowerShell run on Windows (powershell.exe always exists)
// and anywhere JAZZ_TEST_PWSH points at a pwsh binary.

func TestPickWindowsShellPrefersRealBashOverWSL(t *testing.T) {
	env := map[string]string{"ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`}
	files := map[string]bool{}
	look := func(paths map[string]string) func(string) (string, error) {
		return func(n string) (string, error) {
			if p, ok := paths[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		}
	}
	exists := func(p string) bool { return files[p] }
	getenv := func(k string) string { return env[k] }

	// the WSL launcher in System32 is not a usable bash
	d := pickWindowsShell(look(map[string]string{"bash": `C:\Windows\System32\bash.exe`, "pwsh": `C:\Program Files\PowerShell\7\pwsh.exe`}), exists, getenv)
	if d.Kind != shPwsh || !strings.Contains(d.Path, "pwsh") {
		t.Fatalf("WSL bash must be skipped for pwsh, got %+v", d)
	}
	d = pickWindowsShell(look(map[string]string{"bash": `C:\Users\me\AppData\Local\Microsoft\WindowsApps\bash.exe`}), exists, getenv)
	if d.Kind != shPwsh || !strings.HasSuffix(strings.ToLower(d.Path), "powershell.exe") {
		t.Fatalf("WindowsApps bash must be skipped, fallback is Windows PowerShell, got %+v", d)
	}
	// Git Bash on PATH wins over pwsh
	d = pickWindowsShell(look(map[string]string{"bash": `C:\Program Files\Git\usr\bin\bash.exe`, "pwsh": `C:\pwsh.exe`}), exists, getenv)
	if d.Kind != shBash || !d.Win {
		t.Fatalf("Git Bash on PATH should win, got %+v", d)
	}
	// Git Bash found in its default install dir when not on PATH
	files[filepath.Join(`C:\Program Files`, "Git", "bin", "bash.exe")] = true
	d = pickWindowsShell(look(map[string]string{"pwsh": `C:\pwsh.exe`}), exists, getenv)
	if d.Kind != shBash {
		t.Fatalf("Git Bash in Program Files should win, got %+v", d)
	}
	// JAZZ_SHELL overrides everything
	env["JAZZ_SHELL"] = "pwsh"
	d = pickWindowsShell(look(map[string]string{"pwsh": `C:\pwsh.exe`, "bash": `C:\Program Files\Git\usr\bin\bash.exe`}), func(string) bool { return true }, getenv)
	if d.Kind != shPwsh {
		t.Fatalf("JAZZ_SHELL=pwsh should win, got %+v", d)
	}
}

func TestPwshQuote(t *testing.T) {
	cases := map[string]string{
		``:         `''`,
		`a b`:      `'a b'`,
		`it's`:     `'it''s'`,
		"$x `y`":   "'$x `y`'",
		"‘smart’":  "'‘‘smart’’'",
		`C:\a\b c`: `'C:\a\b c'`,
	}
	for in, want := range cases {
		if got := pwshQuote(in); got != want {
			t.Errorf("pwshQuote(%q) = %q want %q", in, got, want)
		}
	}
}

func TestPwshEncodedIsUTF16LEBase64(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(pwshEncoded("echo ção"))
	if err != nil || len(b)%2 != 0 {
		t.Fatalf("bad encoding: %v", err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	if got := string(utf16.Decode(u)); got != "echo ção" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestWrapLineIsOnePhysicalLine(t *testing.T) {
	cmd := "echo 'a'\nprintf \"%s\" \"$HOME\"\r\n\techo ção ‘q’"
	for _, d := range []shellDialect{{Kind: shBash}, {Kind: shBash, Win: true}, {Kind: shSh}, {Kind: shPwsh}} {
		line := d.wrapLine(cmd, "__JZ_abc_")
		if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") || strings.Contains(line, "\r") {
			t.Errorf("%v: wrapLine must be exactly one line: %q", d.Name(), line)
		}
		if !strings.Contains(line, "__JZ_abc_") {
			t.Errorf("%v: marker missing: %q", d.Name(), line)
		}
	}
	// the Unix line is unchanged from the bash-only implementation
	if got, want := (shellDialect{Kind: shBash}).wrapLine("ls", "M"), "eval $'ls' </dev/null 2>&1; printf '\\nM%d_%s_ME\\n' \"$?\" \"$PWD\"\n"; got != want {
		t.Errorf("unix wrapLine changed:\n got %q\nwant %q", got, want)
	}
	// PowerShell carries the command as base64, never as raw text
	if l := (shellDialect{Kind: shPwsh}).wrapLine("Remove-Item 'x'", "M"); strings.Contains(l, "Remove-Item") {
		t.Errorf("pwsh line must not carry the raw command: %q", l)
	}
}

func TestSubshellAtDialects(t *testing.T) {
	if got := (shellDialect{Kind: shBash}).subshellAt("/p q", "make test"); got != "(cd $'/p q' && make test\n)" {
		t.Errorf("bash subshellAt = %q", got)
	}
	got := (shellDialect{Kind: shPwsh}).subshellAt(`C:\p q`, "go test ./...")
	if !strings.HasPrefix(got, "Push-Location -LiteralPath 'C:\\p q'") || !strings.HasSuffix(got, "finally { Pop-Location }") {
		t.Errorf("pwsh subshellAt = %q", got)
	}
}

// testPwsh returns a PowerShell to test against, or skips.
func testPwsh(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("JAZZ_TEST_PWSH"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, n := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
	}
	t.Skip("no PowerShell (set JAZZ_TEST_PWSH=/path/to/pwsh)")
	return ""
}

func pwshShell(t *testing.T) *Shell {
	dir := t.TempDir()
	s := NewShell(dir)
	s.d = shellDialect{Kind: shPwsh, Path: testPwsh(t), Win: runtime.GOOS == "windows"}
	t.Cleanup(s.Close)
	return s
}

func TestPwshShellMarkerProtocol(t *testing.T) {
	s := pwshShell(t)
	ctx := context.Background()
	run := func(c string) ShellResult {
		t.Helper()
		r := s.Run(ctx, c, 60*time.Second, nil)
		if r.Reset {
			t.Fatalf("%q reset the shell: %+v", c, r)
		}
		return r
	}
	if r := run("Write-Output 'olá ção'"); r.ExitCode != 0 || strings.TrimSpace(r.Output) != "olá ção" {
		t.Fatalf("echo: %+v", r)
	}
	// variables and functions persist (dot-sourced)
	run("$jzv = 41; function jzf { $jzv + 1 }")
	if r := run("jzf"); strings.TrimSpace(r.Output) != "42" {
		t.Fatalf("state did not persist: %+v", r)
	}
	// cwd persists and is reported, even with spaces and quotes in the name
	sub := filepath.Join(s.home, "a b'c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run("Set-Location -LiteralPath " + pwshQuote(sub))
	if got, _ := filepath.EvalSymlinks(s.Cwd); !pathEq(got, mustEval(t, sub)) {
		t.Fatalf("cwd = %q want %q", s.Cwd, sub)
	}
	// cmdlet error -> exit 1, output kept
	if r := run("Get-Item -LiteralPath 'definitely-not-here-jz'"); r.ExitCode != 1 {
		t.Fatalf("non-terminating error should exit 1: %+v", r)
	}
	if r := run("throw 'boom'"); r.ExitCode != 1 || !strings.Contains(r.Output, "boom") {
		t.Fatalf("throw should exit 1 with the message: %+v", r)
	}
	// parse error -> exit 1, shell survives
	if r := run("if ("); r.ExitCode != 1 {
		t.Fatalf("parse error should exit 1: %+v", r)
	}
	// native exit code comes through $LASTEXITCODE
	native := "& " + pwshQuote(s.d.Path) + " -NoProfile -NonInteractive -Command 'exit 3'"
	if r := run(native); r.ExitCode != 3 {
		t.Fatalf("native exit code: %+v", r)
	}
	if r := run("Write-Output ok"); r.ExitCode != 0 {
		t.Fatalf("exit code must reset between commands: %+v", r)
	}
	// subshellAt does not move the shell
	before := s.Cwd
	if r := run(s.d.subshellAt(s.home, "Write-Output (Get-Location).ProviderPath")); !strings.Contains(r.Output, filepath.Base(s.home)) {
		t.Fatalf("subshellAt output: %+v", r)
	}
	if s.Cwd != before {
		t.Fatalf("subshellAt moved the shell: %q -> %q", before, s.Cwd)
	}
	// a fake marker in the output does not end the command early
	if r := run("Write-Output '__JZ_0000_0_x___JZ_0000_E'; Write-Output after"); !strings.Contains(r.Output, "after") {
		t.Fatalf("fake marker cut the output: %+v", r)
	}
}

func TestPwshShellTimeoutResets(t *testing.T) {
	s := pwshShell(t)
	r := s.Run(context.Background(), "Start-Sleep -Seconds 30", 2*time.Second, nil)
	if !r.Reset || r.ExitCode != -1 {
		t.Fatalf("timeout should reset: %+v", r)
	}
	if r := s.Run(context.Background(), "Write-Output fresh", 60*time.Second, nil); strings.TrimSpace(r.Output) != "fresh" {
		t.Fatalf("fresh shell after reset: %+v", r)
	}
}

func TestPwshShellExitResets(t *testing.T) {
	s := pwshShell(t)
	r := s.Run(context.Background(), "exit 5", 60*time.Second, nil)
	if !r.Reset {
		t.Fatalf("exit should end the shell and reset: %+v", r)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
