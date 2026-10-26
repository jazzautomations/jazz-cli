package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// shellDialect is the shell behind the bash tool and how jazz talks to it.
// On Unix it is always bash (or /bin/sh); on Windows it is Git Bash / MSYS
// bash when installed, else PowerShell (pwsh, then Windows PowerShell). Every
// dialect speaks the same marker protocol (Shell.Run): one physical line per
// command, then a line with a per-call nonce, the exit code and the cwd.
type shellDialect struct {
	Kind shellKind
	Path string
	Win  bool // running on Windows: MSYS bash reports cwd with pwd -W
}

type shellKind int

const (
	shBash shellKind = iota
	shSh
	shPwsh
)

func (d shellDialect) IsPwsh() bool { return d.Kind == shPwsh }

// Name is what the model and /doctor are told the shell is.
func (d shellDialect) Name() string {
	switch d.Kind {
	case shPwsh:
		if strings.EqualFold(strings.TrimSuffix(path.Base(strings.ReplaceAll(d.Path, `\`, "/")), ".exe"), "powershell") {
			return "Windows PowerShell"
		}
		return "PowerShell"
	case shSh:
		return "sh"
	}
	if d.Win {
		return "bash (Git Bash/MSYS)"
	}
	return "bash"
}

// detectShell picks the persistent shell for this OS.
func detectShell() shellDialect {
	if runtime.GOOS == "windows" {
		return pickWindowsShell(exec.LookPath, func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && !fi.IsDir()
		}, os.Getenv)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return shellDialect{Kind: shSh, Path: fallbackShPath()}
	}
	return shellDialect{Kind: shBash, Path: bash}
}

// pickWindowsShell: JAZZ_SHELL (bash|pwsh|powershell or a path) wins; then a
// real bash (never the WSL launcher in System32 / WindowsApps: jazz runs
// natively, without WSL), then pwsh, then Windows PowerShell, which every
// supported Windows ships.
func pickWindowsShell(look func(string) (string, error), exists func(string) bool, env func(string) string) shellDialect {
	bashOK := func(p string) bool {
		l := strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
		return p != "" && !strings.Contains(l, "/windows/system32/") && !strings.Contains(l, "/windows/syswow64/") && !strings.Contains(l, "/windowsapps/")
	}
	kindOf := func(p string) shellKind {
		switch strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(p, `\`, "/")), ".exe")) {
		case "pwsh", "powershell":
			return shPwsh
		case "sh":
			return shSh
		}
		return shBash
	}
	if want := strings.TrimSpace(env("JAZZ_SHELL")); want != "" {
		p := want
		if !strings.ContainsAny(want, `/\`) {
			p, _ = look(want)
		}
		if p != "" && exists(p) {
			return shellDialect{Kind: kindOf(p), Path: p, Win: true}
		}
	}
	if p, err := look("bash"); err == nil && bashOK(p) {
		return shellDialect{Kind: shBash, Path: p, Win: true}
	}
	for _, base := range []string{env("ProgramFiles"), env("ProgramW6432"), env("ProgramFiles(x86)"), filepath.Join(env("LOCALAPPDATA"), "Programs")} {
		if base == "" || base == "Programs" {
			continue
		}
		if p := filepath.Join(base, "Git", "bin", "bash.exe"); exists(p) {
			return shellDialect{Kind: shBash, Path: p, Win: true}
		}
	}
	if p, err := look("pwsh"); err == nil {
		return shellDialect{Kind: shPwsh, Path: p, Win: true}
	}
	if p, err := look("powershell"); err == nil {
		return shellDialect{Kind: shPwsh, Path: p, Win: true}
	}
	root := env("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return shellDialect{Kind: shPwsh, Path: filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), Win: true}
}

// pwshFlags start PowerShell without profiles or prompts. RemoteSigned lets
// the project's own .ps1 scripts run (the Windows client default, Restricted,
// refuses every script) while downloaded, unsigned ones stay blocked.
var pwshFlags = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "RemoteSigned"}

// persistentArgv is the command line of the long-lived shell.
func (d shellDialect) persistentArgv() []string {
	switch d.Kind {
	case shPwsh:
		return append(append([]string{d.Path}, pwshFlags...), "-Command", "-")
	case shSh:
		return []string{d.Path}
	}
	return []string{d.Path, "--noprofile", "--norc"}
}

// oneShotArgv runs one command line in a fresh shell of this dialect (hooks,
// background jobs on Windows).
func (d shellDialect) oneShotArgv(command string) []string {
	if d.Kind == shPwsh {
		return append(append([]string{d.Path}, pwshFlags...), "-EncodedCommand", pwshEncoded(command))
	}
	return []string{d.Path, "-c", command}
}

// initLine is sent once after start: quiet, non-interactive defaults.
func (d shellDialect) initLine(haveFd, haveFdfind bool) string {
	if d.Kind == shPwsh {
		return "$ProgressPreference='SilentlyContinue'; $ConfirmPreference='None'; $global:ErrorActionPreference='Continue'; " +
			"try { [Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $OutputEncoding=[Text.UTF8Encoding]::new($false) } catch {}; " +
			"try { $PSStyle.OutputRendering='PlainText' } catch {}"
	}
	init := h2LimitsLine(d) + "set +o history 2>/dev/null; shopt -s expand_aliases 2>/dev/null; export LC_ALL=${LC_ALL:-C.UTF-8}"
	if !haveFd && haveFdfind {
		init += "; fd(){ fdfind \"$@\"; }; export -f fd 2>/dev/null"
	}
	return init
}

// Quote renders s as one literal argument for this shell, on one physical line
// for bash ($'…') and PowerShell ('…' with quotes doubled; PowerShell also
// treats the typographic single quotes as quote characters).
func (d shellDialect) Quote(s string) string {
	if d.Kind == shPwsh {
		return pwshQuote(s)
	}
	return bashQuote(s)
}

func pwshQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteRune(r)
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// pwshEncoded is the -EncodedCommand form: base64 of UTF-16LE.
func pwshEncoded(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// wrapLine builds the single physical line that runs command and then prints
// "\n" marker <exit> "_" <cwd> "_" marker "E\n". Bash evals the $'…' quoted
// text with stdin from /dev/null; PowerShell decodes it from base64 (stdin of
// PowerShell is the console code page, so raw non-ASCII text could arrive
// mangled) and dot-sources it, so variables, functions and Set-Location
// persist like in bash. PowerShell's exit code is $LASTEXITCODE of the last
// native program, 1 when a terminating error escaped, 1 when the command added
// errors to $Error, else 0.
func (d shellDialect) wrapLine(command, marker string) string {
	if d.Kind == shPwsh {
		enc := base64.StdEncoding.EncodeToString([]byte(command))
		return "$__jzC=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + enc + "')); " +
			"$global:LASTEXITCODE=0; $__jzE=$Error.Count; $__jzX=0; " +
			"try { . ([ScriptBlock]::Create($__jzC)) } catch { [Console]::Out.WriteLine(($_ | Out-String -Width 4096).TrimEnd()); $__jzX=1 }; " +
			"if ($__jzX -eq 0) { if ($global:LASTEXITCODE) { $__jzX=$global:LASTEXITCODE } elseif ($Error.Count -gt $__jzE) { $__jzX=1 } }; " +
			"[Console]::Out.Write(\"`n" + marker + "\" + $__jzX + '_' + (Get-Location).ProviderPath + '_" + marker + "E' + \"`n\"); [Console]::Out.Flush()\n"
	}
	if d.Win {
		// MSYS $PWD is /c/…; pwd -W prints C:/… which Windows tools and Go understand
		return fmt.Sprintf("eval %s </dev/null 2>&1; __jz=$?; printf '\\n%s%%d_%%s_%sE\\n' \"$__jz\" \"$(pwd -W 2>/dev/null || pwd)\"\n", bashQuote(command), marker, marker)
	}
	return fmt.Sprintf("eval %s </dev/null 2>&1; printf '\\n%s%%d_%%s_%sE\\n' \"$?\" \"$PWD\"\n", bashQuote(command), marker, marker)
}

// subshellAt runs command at dir without touching the shell's own cwd or
// environment (bash: a subshell; PowerShell: Push-Location/Pop-Location).
func (d shellDialect) subshellAt(dir, command string) string {
	if d.Kind == shPwsh {
		return "Push-Location -LiteralPath " + pwshQuote(dir) + "; try {\n" + command + "\n} finally { Pop-Location }"
	}
	return "(cd " + bashQuote(dir) + " && " + command + "\n)"
}

// windowsShellNote tells the model which shell the bash tool really runs on
// Windows, so it writes commands in the right syntax.
func windowsShellNote(d shellDialect) string {
	if d.Kind == shPwsh {
		return "shell: " + d.Name() + " (native Windows). The bash tool runs PowerShell, not bash: use PowerShell syntax (Get-ChildItem, Select-String, $env:NAME, `;` between commands, & to call programs). Paths look like C:\\dir\\file; forward slashes also work."
	}
	return "shell: " + d.Name() + " on native Windows. Paths: C:/dir or /c/dir inside bash; Windows programs (go, git, node) want C:/dir. There is no /proc, apt, systemd or sudo."
}
