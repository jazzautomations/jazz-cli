package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Windows command safety. On Windows the bash tool runs Git Bash or
// PowerShell, and the model also reaches cmd.exe built-ins through either, so
// the Unix analysis (safety.go) is not enough on its own:
//
//   - paths use backslashes, which the bash parser reads as escapes
//     (C:\Users\me\.ssh\id_rsa would become C:Usersme.sshid_rsa), so every
//     check also runs on a copy with \ turned into / and the stricter verdict
//     wins;
//   - PowerShell and cmd have their own destructive verbs (Remove-Item
//     -Recurse -Force, rd /s /q, format, diskpart, iwr | iex, reg delete,
//     schtasks, net user …), checked by winDangerReason;
//   - a small allowlist of PowerShell cmdlets that only read (Get-ChildItem,
//     Get-Content, Select-String …) counts as read-only, with the same
//     secret-file checks as the Unix allowlist.
//
// These functions are pure and run on every OS in tests; DangerReason,
// IsReadOnly and SecretReason call them only when runtime.GOOS is windows, so
// Linux/macOS verdicts do not change.

var winIsWindows = runtime.GOOS == "windows"

// winPwsh reports whether the bash tool runs PowerShell (Windows without
// Git Bash). Detected once: the shell does not change during a session.
var winPwsh = sync.OnceValue(func() bool { return winIsWindows && detectShell().IsPwsh() })

// DangerReason returns why cmd is destructive or hijacks the machine, or "".
// On Windows the POSIX analysis also runs on the slash-normalized copy, and
// the PowerShell/cmd rules apply too.
func DangerReason(cmd string) string {
	r := dangerReasonSh(cmd)
	if !winIsWindows || r != "" && r != reUnparsable {
		return r
	}
	if w := winDangerReason(cmd); w != "" {
		return w
	}
	if strings.Contains(cmd, `\`) {
		if s := dangerReasonSh(winSlashes(cmd)); s != "" && s != reUnparsable {
			return s
		}
	}
	return r
}

// IsReadOnly is a conservative check: true only for plainly read-only
// commands. On Windows a POSIX-allowlisted command must stay read-only with
// backslashes read as path separators, and under PowerShell the read-only
// cmdlets (winReadOnly) count too.
func IsReadOnly(cmd string) bool {
	if !winIsWindows {
		return isReadOnlySh(cmd)
	}
	if winDangerReason(cmd) != "" {
		return false
	}
	if isReadOnlySh(cmd) && isReadOnlySh(winSlashes(cmd)) && winSecretReason(cmd) == "" {
		return true
	}
	return winPwsh() && winReadOnly(cmd)
}

// SecretReason returns why cmd would read credentials, or "".
func SecretReason(cmd string) string {
	if r := secretReasonSh(cmd); r != "" || !winIsWindows {
		return r
	}
	if r := secretReasonSh(winSlashes(cmd)); r != "" {
		return r
	}
	return winSecretReason(cmd)
}

// winSlashes is the analysis-only copy of cmd with backslashes as slashes.
func winSlashes(cmd string) string { return strings.ReplaceAll(cmd, `\`, "/") }

// winSplit cuts a PowerShell/cmd line into pipeline/statement segments.
var winSplit = lazyRx("\r?\n|;|&&|\\|\\||\\|")

// winFields splits a segment on blanks, dropping quotes around words and the
// call operators (& and .) in front of a command.
func winFields(seg string) []string {
	var out []string
	for _, f := range strings.Fields(seg) {
		f = strings.Trim(f, `"'`+"`")
		if f == "" || f == "&" || f == "." && len(out) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// winCmdName is the lower-cased command name: no path, no .exe/.com/.cmd.
func winCmdName(w string) string {
	w = strings.ToLower(winSlashes(w))
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		w = w[i+1:]
	}
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat"} {
		w = strings.TrimSuffix(w, ext)
	}
	return w
}

// winParam reports whether args hold the PowerShell parameter name (any
// unambiguous prefix of at least min letters, case-insensitive, -Name or
// -Name:value) or, for cmd built-ins, the /x switch.
func winParam(args []string, name string, min int) bool {
	name = strings.ToLower(name)
	for _, a := range args {
		a = strings.ToLower(a)
		if !strings.HasPrefix(a, "-") {
			continue
		}
		a = strings.TrimPrefix(a, "-")
		if i := strings.IndexByte(a, ':'); i > 0 {
			a = a[:i]
		}
		if len(a) >= min && strings.HasPrefix(name, a) {
			return true
		}
	}
	return false
}

func winSwitch(args []string, sw string) bool {
	for _, a := range args {
		if strings.EqualFold(a, sw) {
			return true
		}
	}
	return false
}

// winBroadTarget: drive roots, the home and system folders, and wildcards or
// the current directory, as written in PowerShell or cmd (slashes already
// normalized).
var winBroadTarget = lazyRx(`(?i)^([a-z]:/?\*?|/|/\*|~|~/\*?|\$home/?\*?|\$env:(userprofile|homepath|systemroot|windir|programfiles|programdata|appdata|localappdata|systemdrive)(/\*?)?|%(userprofile|systemroot|windir|programfiles|programdata|appdata|localappdata|systemdrive)%(/\*?)?|[a-z]:/(windows|users|program files|program files \(x86\)|programdata)(/\*?)?|\.|\./\*?|\.\.|\.\./.*|\*|\$pwd|\(get-location\)|\$env:homedrive\$env:homepath)$`)

var (
	winDownload = lazyRx(`(?i)\b(iwr|irm|invoke-webrequest|invoke-restmethod|curl|wget|start-bitstransfer|downloadstring|downloadfile|downloaddata|net\.webclient|frombase64string|certutil)\b`)
	winIEX      = lazyRx(`(?i)(\b(iex|invoke-expression)\b|\[scriptblock\]::create)`)
	winEncoded  = lazyRx(`(?i)\b(powershell|pwsh)(\.exe)?\b[^;|&\n]*\s-(e|ec|en|enc|enco|encod|encode|encoded|encodedc|encodedco|encodedcom|encodedcomm|encodedcomma|encodedcomman|encodedcommand)\s`)
	winHive     = lazyRx(`(?i)\b(hklm|hkcu|hkcr|hku|hkcc|hkey_[a-z_]+)(:|/)|registry::`)
	winStartup  = lazyRx(`(?i)start menu/programs/startup|shell:(common )?startup`)
	winWriteVb  = lazyRx(`(?i)(>|\b(copy|move|xcopy|robocopy|cp|mv|copy-item|move-item|new-item|ni|set-content|sc|add-content|ac|out-file|tee-object|mklink)\b)`)
	winProfile  = lazyRx(`(?i)(>|\b(set-content|sc|add-content|ac|out-file|tee-object|new-item|copy-item|cp|move-item|mv)\b)[^;|\n]*\$profile\b`)
	winExfil    = lazyRx(`(?i)\b(iwr|irm|invoke-webrequest|invoke-restmethod|curl|wget|scp|sftp|ftp|copy-item|cp|xcopy|robocopy|set-content|out-file|send-mailmessage)\b|>`)
	winSecret   = lazyRx(`(?i)auth\.json|\.ssh/|authorized_keys|\.gnupg|\.aws/credentials|\.netrc|\.git-credentials|jazz-cli/(config|mcp|trusted)\.json|microsoft/(credentials|protect)/|\$env:[a-z_]*(key|token|secret|password)`)
)

// winDangerReason flags destructive or hijacking PowerShell/cmd commands.
// It returns "" when none applies.
func winDangerReason(cmd string) string {
	text := winSlashes(cmd)
	if winIEX.MatchString(text) && winDownload.MatchString(text) {
		return "executa script baixado/decodificado direto no shell (iwr | iex)"
	}
	if winEncoded.MatchString(text) {
		return "executa comando codificado (-EncodedCommand)"
	}
	if winStartup.MatchString(text) && winWriteVb.MatchString(text) {
		return "grava na pasta de inicialização do Windows"
	}
	if winProfile.MatchString(text) {
		return "sobrescreve o perfil do PowerShell ($PROFILE)"
	}
	if winSecret.MatchString(text) && winExfil.MatchString(text) {
		return "mexe em credenciais ou config sensível"
	}
	saved := map[string]bool{}
	for _, seg := range winSplit.Split(text, -1) {
		t := winFields(seg)
		if len(t) == 0 {
			continue
		}
		w, args := winCmdName(t[0]), t[1:]
		if r := winSegmentDanger(w, args, seg, saved); r != "" {
			return r
		}
	}
	return ""
}

func winSegmentDanger(w string, args []string, seg string, saved map[string]bool) string {
	lower := strings.ToLower(seg)
	// a file saved from the network earlier in the line and run now
	if saved[w] || w == "start-process" || w == "saps" || w == "start" || w == "invoke-item" || w == "ii" {
		for _, a := range append([]string{w}, args...) {
			if saved[winCmdName(a)] {
				return "baixa e executa um arquivo"
			}
		}
	}
	switch w {
	case "iwr", "invoke-webrequest", "curl", "wget", "irm", "invoke-restmethod", "start-bitstransfer":
		for i, a := range args {
			la := strings.ToLower(a)
			if (winParam([]string{la}, "outfile", 2) || la == "-o" || la == "--output" || winParam([]string{la}, "destination", 2)) && i+1 < len(args) {
				saved[winCmdName(args[i+1])] = true
			}
		}
	}
	if winHive.MatchString(seg) {
		switch w {
		case "remove-item", "ri", "rm", "del", "erase", "rd", "rmdir", "remove-itemproperty", "rp", "set-itemproperty", "sp", "new-itemproperty", "new-item", "ni", "set-item", "si", "rename-itemproperty", "rename-item", "move-item", "copy-item", "clear-item", "clear-itemproperty":
			return "altera o registro do Windows"
		}
	}
	switch w {
	case "remove-item", "ri", "rm", "rmdir", "rd", "del", "erase":
		recursive := winParam(args, "recurse", 1) || winSwitch(args, "/s")
		force := winParam(args, "force", 2) || winSwitch(args, "/q")
		for _, a := range args {
			if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") && len(a) == 2 {
				continue
			}
			if recursive && winBroadTarget.MatchString(a) {
				return "apaga diretório amplo (" + a + ")"
			}
		}
		if recursive && force {
			if winSwitch(args, "/q") {
				return "apaga em massa sem confirmação (rd /s /q)"
			}
			return "apaga recursivamente à força (Remove-Item -Recurse -Force)"
		}
	case "format", "format-volume", "clear-disk", "initialize-disk", "remove-partition", "new-partition", "resize-partition", "set-partition", "diskpart", "mountvol":
		return "formata ou particiona disco"
	case "cipher":
		if strings.Contains(lower, "/w") {
			return "apaga o espaço livre do disco (cipher /w)"
		}
	case "vssadmin", "wbadmin", "bcdedit", "wmic":
		if w == "bcdedit" || strings.Contains(lower, "delete") || strings.Contains(lower, "shadowcopy") {
			return "apaga cópias de segurança ou mexe na inicialização do Windows"
		}
	case "set-executionpolicy":
		return "muda a política de execução do PowerShell"
	case "reg":
		if len(args) > 0 {
			switch strings.ToLower(args[0]) {
			case "delete", "add", "import", "restore", "load", "unload", "copy":
				return "altera o registro do Windows"
			}
		}
	case "regedit", "regini":
		return "altera o registro do Windows"
	case "schtasks":
		if winSwitch(args, "/create") || winSwitch(args, "/delete") || winSwitch(args, "/change") || winSwitch(args, "/run") {
			return "agenda ou altera tarefa do Windows (schtasks)"
		}
	case "register-scheduledtask", "unregister-scheduledtask", "set-scheduledtask", "new-scheduledtask", "start-scheduledtask":
		return "agenda ou altera tarefa do Windows"
	case "net", "net1":
		if len(args) > 0 {
			switch strings.ToLower(args[0]) {
			case "user", "localgroup", "group", "accounts":
				if len(args) >= 3 || winSwitch(args, "/add") || winSwitch(args, "/delete") || strings.Contains(lower, "/active") {
					return "altera contas de usuário do Windows"
				}
			case "share", "use":
				if winSwitch(args, "/delete") || len(args) >= 3 {
					return "altera compartilhamentos de rede"
				}
			}
		}
	case "new-localuser", "remove-localuser", "set-localuser", "enable-localuser", "disable-localuser", "rename-localuser", "add-localgroupmember", "remove-localgroupmember":
		return "altera contas de usuário do Windows"
	case "stop-computer", "restart-computer":
		return "desliga ou reinicia a máquina"
	case "shutdown":
		if !winSwitch(args, "/a") {
			return "desliga ou reinicia a máquina"
		}
	case "set-mppreference", "add-mppreference", "remove-mppreference":
		return "mexe na proteção do Windows Defender"
	case "netsh":
		if strings.Contains(lower, "firewall") && (strings.Contains(lower, "off") || strings.Contains(lower, "delete") || strings.Contains(lower, "add")) {
			return "altera o firewall do Windows"
		}
	case "start-process", "saps", "start":
		if winParam(args, "verb", 1) && strings.Contains(lower, "runas") {
			return "pede elevação de privilégio (UAC)"
		}
	case "runas", "gsudo":
		return "pede elevação de privilégio"
	case "sc":
		if len(args) > 0 {
			switch strings.ToLower(args[0]) {
			case "delete", "create", "config", "failure", "sdset":
				return "altera serviço do Windows (sc)"
			}
		}
	case "new-service", "remove-service", "set-service":
		return "altera serviço do Windows"
	case "takeown":
		return "toma posse de arquivos (takeown)"
	case "icacls", "cacls":
		if winSwitch(args, "/grant") || winSwitch(args, "/deny") || winSwitch(args, "/reset") || winSwitch(args, "/setowner") || strings.Contains(lower, "/grant") {
			return "muda permissões de arquivos (icacls)"
		}
	case "wevtutil":
		if len(args) > 0 && (strings.EqualFold(args[0], "cl") || strings.EqualFold(args[0], "clear-log")) {
			return "apaga logs do sistema"
		}
	case "clear-eventlog", "remove-eventlog":
		return "apaga logs do sistema"
	}
	return ""
}

// winReadOnlyCmd: PowerShell cmdlets (and their aliases) and cmd built-ins
// that only read. Commands taking a script block are left out on purpose
// (ForEach-Object, Where-Object { … }): a block can run anything.
var winReadOnlyCmd = map[string]bool{
	"get-childitem": true, "gci": true, "dir": true, "ls": true, "get-content": true, "gc": true, "cat": true, "type": true,
	"get-item": true, "gi": true, "get-location": true, "gl": true, "pwd": true, "select-string": true, "sls": true,
	"test-path": true, "resolve-path": true, "rvpa": true, "split-path": true, "join-path": true, "measure-object": true,
	"measure": true, "sort-object": true, "sort": true, "select-object": true, "select": true, "format-table": true, "ft": true,
	"format-list": true, "fl": true, "format-wide": true, "fw": true, "out-string": true, "get-command": true, "gcm": true,
	"get-date": true, "get-filehash": true, "write-output": true, "echo": true, "write": true, "get-process": true, "gps": true,
	"get-service": true, "gsv": true, "get-unique": true, "gu": true, "group-object": true, "group": true, "findstr": true,
	"hostname": true, "whoami": true, "tree": true, "get-psdrive": true, "gdr": true, "get-host": true,
	"get-alias": true, "gal": true, "get-module": true, "gmo": true, "compare-object": true, "compare": true, "diff": true,
	"convertto-json": true, "convertfrom-json": true,
}

// winBadArg: parameters of allowlisted cmdlets that write or run something.
var winBadArg = lazyRx(`(?i)(^|\s)-(outf|outfi|outfil|outfile|outv|outvar|outvari|outvaria|outvariab|outvariabl|outvariable|pv|pipelinevariable|ev|errorvariable|iv|informationvariable|wv|warningvariable|filepath|literalpath:?\$)`)

// winReadOnly is true for a PowerShell/cmd line built only from read-only
// cmdlets, with no expressions, redirections or secret files.
func winReadOnly(cmd string) bool {
	if strings.TrimSpace(cmd) == "" || strings.ContainsAny(cmd, "\n\r(){}`$>@%&<") {
		return false
	}
	if winBadArg.MatchString(cmd) {
		return false
	}
	for _, seg := range regexp.MustCompile(`;|\|`).Split(cmd, -1) {
		t := winFields(seg)
		if len(t) == 0 {
			continue
		}
		if !winReadOnlyCmd[winCmdName(t[0])] {
			return false
		}
		if winSecretArgs(t[1:]) {
			return false
		}
	}
	return true
}

// winSecretArgs: an argument names a secret file, written either way.
func winSecretArgs(args []string) bool {
	home, _ := os.UserHomeDir()
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if i := strings.IndexByte(a, ':'); i > 0 {
				a = a[i+1:]
			} else {
				continue
			}
		}
		s := winSlashes(a)
		low := strings.ToLower(s)
		for _, v := range []string{"$env:userprofile", "$home", "%userprofile%", "~"} {
			if strings.HasPrefix(low, v) && home != "" {
				s = winSlashes(home) + s[len(v):]
				break
			}
		}
		if reSecretArg.MatchString(s) || winSecret.MatchString(s) || secretName(filepath.Base(filepath.FromSlash(s))) {
			return true
		}
		p := filepath.FromSlash(s)
		if ap, err := filepath.Abs(p); err == nil && (filepath.IsAbs(p) || strings.HasPrefix(s, ".")) && secretPath(ap) {
			return true
		}
	}
	return false
}

// winSecretReason: a PowerShell/cmd line that reads credentials.
func winSecretReason(cmd string) string {
	text := winSlashes(cmd)
	if winSecret.MatchString(text) {
		return "lê credenciais ou config sensível"
	}
	for _, seg := range winSplit.Split(text, -1) {
		if t := winFields(seg); len(t) > 1 && winSecretArgs(t[1:]) {
			return "lê arquivo sensível (credenciais)"
		}
	}
	return ""
}

// winProtectedDirs / winProtectedFiles extend protectedPath on Windows:
// autostart, PowerShell profiles, PATH dirs, system folders, registry hives.
func winProtectedPath(rp string) bool {
	home, _ := os.UserHomeDir()
	env := func(k string) string { return os.Getenv(k) }
	var dirs []string
	add := func(base string, rel ...string) {
		if base != "" {
			dirs = append(dirs, filepath.Join(append([]string{base}, rel...)...))
		}
	}
	add(env("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	add(env("ProgramData"), "Microsoft", "Windows", "Start Menu", "Programs", "StartUp")
	add(env("APPDATA"), "Microsoft", "Windows", "PowerShell") // PSReadLine history etc.
	add(env("LOCALAPPDATA"), "Microsoft", "WindowsApps")      // on PATH: a planted exe shadows real tools
	add(env("SystemRoot"))
	add(env("ProgramFiles"))
	add(env("ProgramFiles(x86)"))
	add(env("ProgramData"), "Microsoft")
	add(home, "Documents", "PowerShell")
	add(home, "Documents", "WindowsPowerShell")
	add(home, "OneDrive", "Documents", "PowerShell")
	add(home, "OneDrive", "Documents", "WindowsPowerShell")
	add(home, ".azure")
	for _, d := range dirs {
		if pathIn(rp, d) {
			return true
		}
	}
	switch strings.ToLower(filepath.Base(rp)) {
	case "ntuser.dat", "ntuser.ini", "usrclass.dat":
		return true
	}
	return false
}

// winSecretPath extends secretPath on Windows: Credential Manager and DPAPI
// key folders, browser profiles, cloud CLIs, the SAM/SYSTEM hives.
func winSecretPath(rp string) bool {
	var dirs []string
	add := func(base string, rel ...string) {
		if base != "" {
			dirs = append(dirs, filepath.Join(append([]string{base}, rel...)...))
		}
	}
	for _, k := range []string{"APPDATA", "LOCALAPPDATA"} {
		add(os.Getenv(k), "Microsoft", "Credentials")
		add(os.Getenv(k), "Microsoft", "Protect")
		add(os.Getenv(k), "Microsoft", "Vault")
	}
	add(os.Getenv("APPDATA"), "GitHub CLI")
	add(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data")
	add(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "User Data")
	add(os.Getenv("APPDATA"), "Mozilla", "Firefox", "Profiles")
	add(os.Getenv("SystemRoot"), "System32", "config")
	if home, _ := os.UserHomeDir(); home != "" {
		add(home, ".azure")
	}
	for _, d := range dirs {
		if pathIn(rp, d) {
			return true
		}
	}
	return false
}
