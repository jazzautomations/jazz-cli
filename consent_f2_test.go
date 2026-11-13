//go:build !windows

package main

// consent_f2_test.go: F2 da 0.14 — CRÍTICO vira pergunta, "a" lembra por
// binário/diretório, sudo sai do sandbox, senha nunca no comando nem no log,
// escrita elevada com backup.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func permitMode(t *testing.T, mode string, answer Approval) *harness {
	t.Helper()
	h := newHarness(t, nil, func(c *Config) { c.Mode = mode })
	h.ui.answer = answer
	return h
}

// The d610 wifi session: modprobe and an edit of /etc/modprobe.d were BLOCKED
// in auto/ask/turbo even with the user's Allow, while `sudo apt install`
// asked and ran. Now all of them are a question with the undo.
func TestSystemOpsAsksInAuto(t *testing.T) {
	for _, mode := range []string{"auto", "ask", "turbo", "smart"} {
		h := permitMode(t, mode, Allow)
		for _, c := range []struct {
			tool string
			args map[string]any
			undo string
		}{
			{"bash", map[string]any{"command": "sudo modprobe ipw2200"}, "modprobe -r ipw2200"},
			{"bash", map[string]any{"command": "modprobe -r ipw2200"}, ""},
			{"edit", map[string]any{"path": "/etc/modprobe.d/ipw2200.conf", "old_string": "a", "new_string": "b"}, "cópia"},
		} {
			ok, msg := permitTC(h, c.tool, c.args)
			if !ok {
				t.Errorf("%s %v: the user said yes and it was refused: %q", mode, c.args, msg)
				continue
			}
			if len(h.ui.approvals) != 1 || !strings.Contains(h.ui.approvals[0].Reason, "CRÍTICO") || !strings.Contains(h.ui.approvals[0].Reason, c.undo) {
				t.Errorf("%s %v: asked once, with the reason and the undo %q: %+v", mode, c.args, c.undo, h.ui.approvals)
			}
		}
	}
}

// An allow rule that names the command or the path covers the critical
// call (headless included: that is the explicit flag); a tool-wide rule does
// not — --allow-tools bash must not open the kernel.
func TestAllowRuleCoversSystemOps(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.Headless = true
	h.ag.Perms = LoadPerms(&Config{}, h.root, []string{"Bash(modprobe:*)", "Edit(/etc/modprobe.d/**)"})
	if ok, msg := permitTC(h, "bash", map[string]any{"command": "modprobe ipw2200"}); !ok {
		t.Errorf("Bash(modprobe:*) must cover modprobe: %q", msg)
	}
	if ok, msg := permitTC(h, "edit", map[string]any{"path": "/etc/modprobe.d/ipw2200.conf", "old_string": "a", "new_string": "b"}); !ok {
		t.Errorf("Edit(/etc/modprobe.d/**) must cover the edit: %q", msg)
	}
	if ok, _ := permitTC(h, "bash", map[string]any{"command": "sysctl -w net.ipv4.ip_forward=1"}); ok {
		t.Error("a modprobe rule must not cover sysctl -w")
	}
	h.ag.Perms = LoadPerms(&Config{}, h.root, []string{"bash", "edit", "write"})
	for _, c := range []string{"modprobe ipw2200", "sysctl -w net.ipv4.ip_forward=1"} {
		if ok, _ := permitTC(h, "bash", map[string]any{"command": c}); ok {
			t.Errorf("tool-wide --allow-tools bash must not cover %q", c)
		}
	}
}

// "a" (sempre) remembers the binary or the directory, never the class.
func TestAlwaysRemembersBinaryNotClass(t *testing.T) {
	h := permitMode(t, "auto", AllowAlways)
	if ok, msg := permitTC(h, "bash", map[string]any{"command": "sudo modprobe ipw2200"}); !ok {
		t.Fatalf("first modprobe: %q", msg)
	}
	h.ui.answer = Deny
	if ok, msg := permitTC(h, "bash", map[string]any{"command": "sudo modprobe ipw2200 associate=0"}); !ok || len(h.ui.approvals) != 0 {
		t.Errorf("modprobe again must not ask after 'a': ok=%v %q %+v", ok, msg, h.ui.approvals)
	}
	if ok, _ := permitTC(h, "bash", map[string]any{"command": "sudo sysctl -w net.ipv4.ip_forward=1"}); ok || len(h.ui.approvals) != 1 {
		t.Errorf("approving modprobe with 'a' must not let sysctl -w through: %+v", h.ui.approvals)
	}
	// files: by directory
	h.ui.answer = AllowAlways
	edit := func(p string) (bool, string) {
		return permitTC(h, "edit", map[string]any{"path": p, "old_string": "a", "new_string": "b"})
	}
	if ok, msg := edit("/etc/modprobe.d/ipw2200.conf"); !ok {
		t.Fatalf("edit: %q", msg)
	}
	h.ui.answer = Deny
	if ok, _ := edit("/etc/modprobe.d/blacklist.conf"); !ok || len(h.ui.approvals) != 0 {
		t.Errorf("same directory must not ask again: %+v", h.ui.approvals)
	}
	if ok, _ := edit("/etc/sysctl.conf"); ok {
		t.Error("another directory of /etc must ask again")
	}
	// a file right under /etc is remembered by itself: "/etc/**" would be the
	// whole class again (sudoers, passwd, fstab)
	if tg, _ := cbCritTarget("edit", map[string]any{"path": "/etc/sysctl.conf"}, ""); tg != "path:/etc/sysctl.conf" {
		t.Errorf("file under /etc: %q", tg)
	}
	if tg, _ := cbCritTarget("bash", nil, "echo x | sudo tee -a /etc/sysctl.conf"); tg != "bin:tee@/etc/sysctl.conf" {
		t.Errorf("tee into /etc: %q", tg)
	}
	h.ui.answer = AllowAlways
	if ok, _ := edit("/etc/sysctl.conf"); !ok {
		t.Fatal("edit /etc/sysctl.conf")
	}
	h.ui.answer = Deny
	if ok, msg := edit("/etc/sysctl.conf"); !ok || len(h.ui.approvals) != 0 {
		t.Errorf("'a' on /etc/sysctl.conf remembers that file: %v %q %+v", ok, msg, h.ui.approvals)
	}
	if ok, _ := edit("/etc/fstab"); ok {
		t.Error("'a' on /etc/sysctl.conf must not cover /etc/fstab")
	}
}

// "não pode usar sudo, mas pode mexer no wifi" granted system-ops: the
// negated clause wins.
func TestNegatedSudoNotGranted(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.nlScanConsent("nao pode usar sudo, mas pode mexer no wifi")
	if h.ag.nlHas(nlClassSystemOps) || h.ag.nlHas(nlClassGeneric) {
		t.Error("a negated sudo clause must not grant system-ops")
	}
	h.ag.nlScanConsent("autorizo operações de baixo nível")
	if !h.ag.nlHas(nlClassSystemOps) {
		t.Fatal("grant")
	}
	h.ag.nlScanConsent("pensando bem, sem sudo")
	if h.ag.nlHas(nlClassSystemOps) {
		t.Error("a later 'sem sudo' revokes the grant")
	}
	if !strings.Contains(nlAudit(t), `"consent.revoke"`) {
		t.Error("revocation is audited")
	}
}

// The Podman case: disproportionate for a container task always asks, no
// "a" is offered, and no class grant covers it.
func TestDisproportionateAlwaysAsks(t *testing.T) {
	h := permitMode(t, "auto", AllowAlways)
	h.ag.oj = &ojState{input: "sobe o container do postgres com podman"}
	h.ag.nlScanConsent("pode torar. carta branca")
	cmd := map[string]any{"command": "sudo apt-get remove -y podman"}
	ok, _ := permitTC(h, "bash", cmd)
	if !ok || len(h.ui.approvals) != 1 || !strings.Contains(h.ui.approvals[0].Reason, "desproporcional") {
		t.Fatalf("disproportionate must be asked even under a broad grant: ok=%v %+v", ok, h.ui.approvals)
	}
	if ok, _ := permitTC(h, "bash", cmd); !ok || len(h.ui.approvals) != 1 {
		t.Errorf("'a' must not remember a disproportionate action: %+v", h.ui.approvals)
	}
	h.ag.Headless = true
	h.ui.answer = Deny // what the -p UI answers
	if ok, _ := permitTC(h, "bash", cmd); ok {
		t.Error("headless: disproportionate is refused")
	}
}

// Hard gates stay hard: a yes, a grant and yolo do not open them.
func TestHardGateUnchanged(t *testing.T) {
	for _, mode := range []string{"auto", "yolo"} {
		h := permitMode(t, mode, Allow)
		h.ag.nlScanConsent("pode torar. carta branca")
		for _, c := range []map[string]any{
			{"command": "echo hunter2 | sudo -S apt install x"},
			{"command": "sudo -S modprobe x <<< hunter2"},
			{"command": "SUDO_ASKPASS=./pw.sh sudo -A modprobe x"},
			{"command": "echo hunter2 | su -c 'modprobe x'"},
			{"command": "sudo -p '' -S true"},
			{"command": "echo hunter2 | sudo -HS true"},
		} {
			if ok, msg := permitTC(h, "bash", c); ok || !strings.Contains(msg, "plain `sudo") {
				t.Errorf("%s %v: a password in the command must be refused with the right step: %v %q", mode, c, ok, msg)
			}
		}
		if ok, _ := permitTC(h, "write", map[string]any{"path": filepath.Join(configDir(), "config.json"), "content": "{}"}); ok {
			t.Errorf("%s: jazz private data stays a hard gate", mode)
		}
	}
	h := permitMode(t, "auto", Allow)
	if ok, _ := permitTC(h, "bash", map[string]any{"command": "sudo modprobe ipw2200"}); !ok {
		t.Error("plain sudo is not a password in the command")
	}
	// an option's value is not a flag: -uADMIN is the user ADMIN, -pS a prompt
	for _, c := range []string{"sudo -uADMIN ls", "sudo -u SA ls", "sudo -pS true"} {
		if why := nlPasswordInCommand(c); why != "" {
			t.Errorf("%q is not a password in the command: %q", c, why)
		}
	}
}

// What the model reads after a refusal never sends the user to do it by
// hand nor names CLI flags (on the d610 the model ran `modprobe --allow-tools
// ipw2200`).
func TestBlockTextNoCLIFlags(t *testing.T) {
	bad := []string{"themselves", "run it yourself", "have them run", "--yolo", "--allow-tools", "senha do sudo é"}
	for _, headless := range []bool{true, false} {
		h := newHarness(t, nil, nil)
		h.ag.Headless = headless
		for _, gate := range []string{nlGateFence, nlGateSysOps, nlGateSudo, nlGateDanger, nlGateSecret, nlGateEgress, nlGateSupply, nlGateHigh,
			nlGateInject, nlGatePrompt, nlGateRuleAsk, nlGateRuleDeny, nlGateHostile, nlGatePrivate, nlGatePolicy, nlGateUserDeny, nlGateHook, nlGateCredCmd, nlGateDisprop} {
			msg := h.ag.nlDenied(gate, blockMsg("bash `x`", "why"))
			for _, b := range bad {
				if strings.Contains(msg, b) {
					t.Errorf("headless=%v gate %s: %q in %q", headless, gate, b, msg)
				}
			}
		}
		for _, c := range []string{"sudo modprobe ipw2200", "modprobe ipw2200", "sudo apt update"} {
			_, msg := permitTC(h, "bash", map[string]any{"command": c})
			for _, b := range bad {
				if strings.Contains(msg, b) {
					t.Errorf("headless=%v %q: %q in %q", headless, c, b, msg)
				}
			}
		}
	}
	nlStubSudo(t, nil)
	h := newHarness(t, nil, nil)
	h.ag.Headless = true
	out := h.ag.nlExecMaybeElevated(context.Background(), ToolCall{Name: "bash", Args: jsonArgs(map[string]any{"command": "sudo apt update"})})
	for _, b := range bad {
		if strings.Contains(out.Text, b) {
			t.Errorf("sudo NOT RUN: %q in %q", b, out.Text)
		}
	}
}

// A password seen this session is [REDACTED] in tool output, events,
// approvals and the audit chain — whatever its length or shape.
func TestKnownCredRedactedInToolOutputAndAudit(t *testing.T) {
	const pw = "pw42x"
	h := newHarness(t, nil, nil)
	ui := &askUI{recUI: h.ui, answers: []string{pw}}
	h.ag.ui = ui
	nlStubSudo(t, func(ctx context.Context, argv []string, dir string, env []string, stdin io.Reader, timeout time.Duration) (string, int, error) {
		return "options ipw2200 " + pw, 0, nil // the d610 file that held the password
	})
	out := h.ag.nlExecMaybeElevated(context.Background(), ToolCall{Name: "bash", Args: jsonArgs(map[string]any{"command": "sudo cat /etc/modprobe.d/ipw2200.conf"})})
	if strings.Contains(out.Text, pw) || !strings.Contains(out.Text, "[REDACTED]") {
		t.Errorf("tool output: %q", out.Text)
	}
	if got := rdToolText("linha com "+pw, "tool:bash"); strings.Contains(got, pw) || strings.Contains(got, "<secreto:") {
		t.Errorf("one-way redaction (never a usable placeholder): %q", got)
	}
	h.ag.obApproval("bash", "echo "+pw+" > x", "", Allow)
	auditRec("test.cred", map[string]any{"cmd": "x " + pw, "nested": map[string]any{"v": pw}})
	if a := nlAudit(t); strings.Contains(a, pw) {
		t.Error("the password reached the audit chain")
	}
	home := os.Getenv("JAZZ_HOME")
	filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, e := os.ReadFile(p); e == nil && strings.Contains(string(b), pw) {
				t.Errorf("the password was written to %s", strings.TrimPrefix(p, home))
			}
		}
		return nil
	})
}

// sudo inside the bwrap shell answers "no new privileges" and a system
// command without sudo gets EPERM: both are annotated so the model does not
// conclude "no root here, do it yourself".
func TestSandboxEPERMAnnotated(t *testing.T) {
	prev := sbActive.Load()
	t.Cleanup(func() { sbActive.Store(prev) })
	sbActive.Store(&Sandbox{Backend: sbBwrap, Bin: "/nonexistent/bwrap"})
	out := nlRootNote("ls /etc", ToolOutput{IsErr: true, Text: "sudo: The \"no new privileges\" flag is set, which prevents sudo from running as root.\n[exit code 1]"})
	if !strings.Contains(out.Text, "isso é o sandbox") {
		t.Errorf("sandbox artifact: %q", out.Text)
	}
	out = nlRootNote("modprobe ipw2200", ToolOutput{IsErr: true, Text: "modprobe: ERROR: could not insert 'ipw2200': Operation not permitted\n[exit code 1]"})
	if !strings.Contains(out.Text, "repita com sudo") {
		t.Errorf("EPERM: %q", out.Text)
	}
	out = nlRootNote("go test ./...", ToolOutput{IsErr: true, Text: "open x: permission denied\n[exit code 1]"})
	if strings.Contains(out.Text, "[jazz:") {
		t.Errorf("a project command's EPERM is not a root problem: %q", out.Text)
	}
}

// sudo cat of a secret stays a secret read: the elevated path does not
// open ~/.ssh (the sandbox's Hide), not even under a system-ops grant.
func TestHostViaKeepsSecretsHidden(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.Headless = true
	h.ag.nlScanConsent("autorizo operações de baixo nível")
	for _, c := range []string{"cat ~/.ssh/id_ed25519", "sudo cat ~/.ssh/id_rsa", "sudo cat /etc/shadow"} {
		if ok, _ := permitTC(h, "bash", map[string]any{"command": c}); ok {
			t.Errorf("%q: secret reads stay gated under a system-ops grant", c)
		}
	}
	// and the elevated runner itself refuses them, yolo or not
	nlStubSudo(t, func(ctx context.Context, argv []string, dir string, env []string, stdin io.Reader, timeout time.Duration) (string, int, error) {
		t.Errorf("a secret read must never reach the elevated runner: %v", argv)
		return "", 0, nil
	})
	nlSudoProbe = func(ctx context.Context) error { return nil }
	prev := sbActive.Load()
	t.Cleanup(func() { sbActive.Store(prev) })
	home, _ := os.UserHomeDir()
	sbActive.Store(&Sandbox{Backend: sbBwrap, Bin: "/nonexistent/bwrap", Hide: []string{filepath.Join(home, ".gnupg")}})
	for _, c := range []string{"sudo cat ~/.ssh/id_rsa", "sudo cat /etc/shadow", "sudo tar czf /tmp/k.tgz " + filepath.Join(home, ".gnupg")} {
		out := h.ag.nlExecMaybeElevated(context.Background(), ToolCall{Name: "bash", Args: jsonArgs(map[string]any{"command": c})})
		if !out.IsErr || !strings.Contains(out.Text, "NOT RUN") {
			t.Errorf("%q: must not run as root on the host: %+v", c, out)
		}
	}
}

// An approved write to a system file the user cannot write goes through
// sudo on the host, after a backup; never an in-process root write.
func TestSysEditGoesElevated(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "ipw2200.conf")
	if err := os.WriteFile(target, []byte("old line\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var argv0 []string
	nlStubSudo(t, func(ctx context.Context, argv []string, d string, env []string, stdin io.Reader, timeout time.Duration) (string, int, error) {
		argv0 = argv
		i := 0
		for i < len(argv) && argv[i] != "--" {
			i++
		}
		c := exec.CommandContext(ctx, argv[i+1], argv[i+2:]...) // the real script, without sudo
		b, err := c.CombinedOutput()
		code := 0
		if err != nil {
			code = 1
		}
		return string(b), code, err
	})
	h := newHarness(t, nil, nil)
	h.ag.ui = &askUI{recUI: h.ui, answers: []string{"pw-sys"}}
	note, err := h.ag.nlSysWrite(target, []byte("options ipw2200 associate=0\n"), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv0) < 2 || argv0[0] != "sudo" {
		t.Fatalf("must go through sudo: %v", argv0)
	}
	if b, _ := os.ReadFile(target); string(b) != "options ipw2200 associate=0\n" {
		t.Errorf("content: %q", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode kept: %v", fi.Mode())
	}
	baks, _ := filepath.Glob(filepath.Join(nlSysbakDir(), "*ipw2200.conf"))
	if len(baks) != 1 {
		t.Fatalf("one backup: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "old line\n" {
		t.Errorf("backup content: %q", b)
	}
	if !strings.Contains(note, baks[0]) || !strings.Contains(note, "to undo") {
		t.Errorf("the note says how to undo: %q", note)
	}
	if !strings.Contains(nlAudit(t), `"elevated.write"`) {
		t.Error("elevated write is audited")
	}
	// a second write in the same second keeps the first backup (the original)
	if _, err := h.ag.nlSysWrite(target, []byte("third\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	baks, _ = filepath.Glob(filepath.Join(nlSysbakDir(), "*ipw2200.conf"))
	if len(baks) != 2 {
		t.Fatalf("each write keeps its own backup: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "old line\n" {
		t.Errorf("the original's backup was overwritten: %q", b)
	}

	// the write tool falls back to SysWrite only on a permission error on a
	// system path
	tb := NewToolbox(t.TempDir())
	defer tb.Shell.Close()
	var got string
	tb.SysWrite = func(p string, data []byte, mode os.FileMode) (string, error) {
		got = p
		return " (root)", nil
	}
	sys := "/etc/jazz-f2-test-" + filepath.Base(dir) + ".conf"
	if fi, err := os.Stat("/etc"); err != nil || fi.Mode().Perm()&0o002 != 0 || os.Getuid() == 0 {
		t.Skip("/etc is writable here")
	}
	out := tb.Exec(context.Background(), "write", jsonArgs(map[string]any{"path": sys, "content": "x\n"}))
	if out.IsErr || got != sys || !strings.Contains(out.Text, "(root)") {
		t.Errorf("write to %s must go elevated: got=%q %+v", sys, got, out)
	}
	got = ""
	if out := tb.Exec(context.Background(), "write", jsonArgs(map[string]any{"path": "ok.txt", "content": "x\n"})); out.IsErr || got != "" {
		t.Errorf("a project write never elevates: %q %+v", got, out)
	}
}

// The backup of a credential file (/etc/shadow, sudoers, ssh host keys) stays
// root's: chown to the user would leave the secret readable by any command.
func TestSysWriteSecretBackupStaysRoot(t *testing.T) {
	var owner, hasOwner = "", false
	nlStubSudo(t, func(ctx context.Context, argv []string, d string, env []string, stdin io.Reader, timeout time.Duration) (string, int, error) {
		for i, a := range argv {
			if a == "jazz-sysw" && i+4 < len(argv) {
				owner, hasOwner = argv[i+4], true
			}
		}
		return "", 0, nil
	})
	h := newHarness(t, nil, nil)
	h.ag.ui = &askUI{recUI: h.ui, answers: []string{"pw-sys"}}
	if _, err := h.ag.nlSysWrite("/etc/sudoers", []byte("x\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if !hasOwner || owner != "" {
		t.Errorf("credential file backup must not be handed to the user: owner=%q", owner)
	}
	if _, err := h.ag.nlSysWrite("/etc/modprobe.d/x.conf", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if owner == "" {
		t.Error("an ordinary system file's backup goes to the user (to undo)")
	}
}
