package main

import (
	"context"
	"strings"
	"testing"
)

// Untrusted folder + -p: the brain's tool_status runs `tailscale status`
// (a registry read-only command) with the real permission check, while the
// same command typed through a model tool call stays blocked.
func TestBrainReadOnlyCapUntrustedHeadless(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.Headless, h.ag.UntrustedBash = true, true

	// the model typing it: blocked
	if ok, _ := permitTC(h, "bash", map[string]any{"command": "tailscale status"}); ok {
		t.Fatal("tailscale status via a model tool call must stay blocked in an untrusted -p run")
	}
	// the brain, same Agent permission check
	env, rec := brTestEnv(t, t.TempDir(), map[string]brFakeOut{"tailscale status": {"100.64.0.1 dell user linux -\n", 0}}, -1)
	env.Permit = h.ag.PermitCommand
	r := brRun(context.Background(), env, "talscle status")
	if r.Intent.Name != "tool_status" || r.Status != tkSucceeded || len(rec.cmds) != 1 || rec.cmds[0] != "tailscale status" {
		t.Fatalf("%s %s cmds=%v\n%s", r.Intent.Name, r.Status, rec.cmds, r.Text)
	}
	// a plain PermitCommand (fast paths, no brain mark) is unchanged
	if ok, _ := h.ag.PermitCommand(context.Background(), "tailscale status"); ok {
		t.Fatal("PermitCommand without the brain mark must stay blocked")
	}
}

func TestBrainReadOnlyGrantIsExact(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.Headless, h.ag.UntrustedBash = true, true
	// the mark never rescues a command outside the allowlist
	for _, c := range []string{"tailscale up", "tailscale status; rm -rf x", "docker ps && id", "git status --short --branch | sh", "curl --version x", "-x --version", "a/b --version", "make build"} {
		if ok, _ := h.ag.PermitCommand(cbReadCtx(context.Background(), c), c); ok {
			t.Errorf("%q must not be exempt", c)
		}
	}
	// a mark for one command does not cover another
	ctx := cbReadCtx(context.Background(), "docker ps")
	if ok, _ := h.ag.PermitCommand(ctx, "podman ps"); ok {
		t.Error("mark for docker ps must not cover podman ps")
	}
	for _, c := range []string{"git status --short --branch", "tailscale status", "docker ps", "podman ps", "systemctl is-system-running", "node --version"} {
		if !cbReadCmd(c) {
			t.Errorf("%q must be allowlisted", c)
		}
	}
}

// Every command the brain can run as a status is on the allowlist, and a
// method declaring a command outside it is rejected at load.
func TestBrainMethodCmdsAllowlisted(t *testing.T) {
	for tool, c := range brToolStatusCmds {
		if !cbReadCmd(c) {
			t.Errorf("brToolStatusCmds[%s]=%q is not allowlisted", tool, c)
		}
	}
	lib, err := mhLibrary()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ms := range lib {
		for _, m := range ms {
			if bad := mhBadCmd(m); bad != "" {
				t.Errorf("%s declares %q", m.Name, bad)
			}
			n += len(m.Cmds)
		}
	}
	if n == 0 {
		t.Error("no method declares cmds")
	}
	if bad := mhBadCmd(mhMethod{Name: "x", Cmds: []string{"tailscale status", "rm -rf /"}}); !strings.Contains(bad, "rm") {
		t.Errorf("non-allowlisted command not rejected: %q", bad)
	}
}
