package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// One command outside the OS sandbox, on request (bash sandbox:false), like
// Claude Code's per-command escape. A headless Chrome (Remotion, Puppeteer,
// Playwright) does not start under sandbox-exec/bwrap, and caches like
// ~/.cache/huggingface are not writable there (video-forge render on 13/11).
// The model asks for it only after the sandbox broke a command; jazz asks the
// user every time (also in yolo, never remembered). With -p nobody can
// answer: --allow-unsandboxed / JAZZ_ALLOW_UNSANDBOXED=1 is the opt-in.

// usAllow: the user's headless opt-in.
var usAllow = os.Getenv("JAZZ_ALLOW_UNSANDBOXED") == "1"

// usWanted: the call asks to leave the sandbox and there is one to leave.
func usWanted(tool string, args map[string]any) bool {
	if tool != "bash" {
		return false
	}
	v, ok := args["sandbox"].(bool)
	if !ok || v {
		return false
	}
	sb := sbCurrent()
	return sb != nil && sb.Isolates()
}

// runHost runs cmd as the user, outside the sandbox, in the shell's cwd and
// with its exported environment (minus the sandbox's own markers).
func (t *Toolbox) runHost(ctx context.Context, sh *Shell, cmd string, timeout time.Duration) ShellResult {
	t0 := time.Now()
	var env []string
	for _, e := range sh.exportedEnv(ctx) {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "JAZZ_SANDBOX_ACTIVE", "TMPDIR", "TMP", "TEMP":
			continue // the sandbox's private temp dir is not the host's
		}
		env = append(env, e)
	}
	if len(env) == 0 {
		env = scrubEnv(os.Environ())
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, "bash", "-c", cmd)
	c.Dir = sh.Cwd
	if c.Dir == "" {
		c.Dir = t.Root
	}
	c.Env = env
	c.Stdin = nil
	usProcGroup(c) // kill the whole group on timeout (unsandboxed_unix.go)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	r := ShellResult{Output: buf.String(), Duration: time.Since(t0)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.ExitCode = ee.ExitCode()
	} else if err != nil {
		r.ExitCode = -1
	}
	if cctx.Err() == context.DeadlineExceeded {
		r.Note = "[timed out after " + timeout.String() + "; the process group was killed]"
	}
	r.Note += " [ran outside the OS sandbox, approved by the user]"
	return r
}

// Leaving the sandbox never reads secrets. Inside it ~/.ssh, ~/.aws, jazz's
// auth.json and the keychain are hidden; outside they are not, so a command
// that names one is refused in every mode (yolo, --allow-unsandboxed, an
// approval): the output would go to the provider. The text says so plainly
// — the model used to tell the user to rerun with --allow-unsandboxed to
// get a private key printed (Dell QA, 0.14).

// usSecretRead: why a sandbox:false command would read credentials ("" = it
// does not, as far as the secret classifiers see).
func usSecretRead(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return ""
	}
	return nlHostSecret(cmd)
}

// usSecretBlockEN is the model-facing refusal of a secret read outside the sandbox.
func usSecretBlockEN(why string) string {
	return "reading credentials or secrets outside the OS sandbox is never allowed (" + why + "). " +
		"No flag, setting or approval unlocks it — not --allow-unsandboxed, JAZZ_ALLOW_UNSANDBOXED=1, --yolo nor a yes on a prompt — " +
		"so never tell the user to rerun with one; propose a way that does not print the secret (e.g. `ssh-keygen -lf <key>.pub` for a key fingerprint, or ask the user to look at the file themselves)"
}

// usSecretBlockPT is the same refusal for the user.
func usSecretBlockPT(why string) string {
	return "ler credencial/segredo fora do sandbox (" + why + "): nenhuma flag libera isso — nem --allow-unsandboxed, nem JAZZ_ALLOW_UNSANDBOXED=1, nem --yolo"
}
