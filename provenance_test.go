package main

import (
	"strings"
	"testing"
)

func TestPvRemoteTarget(t *testing.T) {
	cases := map[string]string{
		"ssh user@homeserver uname -a":                                   "user@homeserver",
		"ssh -p 2222 -i ~/.ssh/k user@203.0.113.10 'cat /etc/os-release'": "user@203.0.113.10",
		"ssh -o StrictHostKeyChecking=no labbox":                            "labbox",
		"tailscale ssh user@homeserver":                                  "user@homeserver",
		"cat x.txt | ssh host 'tee y'":                                    "host",
		"cd /tmp && ssh -- box ls":                                        "box",
		"scp ./a.bin user@labbox:/tmp/":                                     "user@labbox",
		"rsync -a src/ u@h:/dst":                                          "u@h",
		"ssh ssh://u@h:22/":                                               "u@h",
		"FOO=1 ssh h":                                                     "h",
		"uname -a":                                                        "",
		"cat /proc/cpuinfo | head":                                        "",
		"grep ssh ~/.ssh/config":                                          "",
		"echo 'ssh host' > notes.txt":                                     "",
		"ssh-keygen -t ed25519":                                           "",
		"ssh -V":                                                          "",
		"cp a.txt b:c":                                                    "",
	}
	for cmd, want := range cases {
		if got := pvRemoteTarget(cmd); got != want {
			t.Errorf("%q: want %q got %q", cmd, want, got)
		}
	}
}

func TestPvTag(t *testing.T) {
	if s := pvTag("", "labbox", false); s != "" {
		t.Errorf("local before any remote: no tag, got %q", s)
	}
	if s := pvTag("", "labbox", true); !strings.Contains(s, "LOCALLY on labbox") {
		t.Errorf("local after a remote: %q", s)
	}
	s := pvTag("user@homeserver", "labbox", true)
	if !strings.Contains(s, "REMOTE host user@homeserver") || !strings.Contains(s, "not the local machine (labbox)") {
		t.Errorf("remote tag: %q", s)
	}
}

// After an ssh command, every bash result says where it ran — the "I am
// still on the remote box" confusion (labbox session observada).
func TestBashProvenanceTrailer(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	tb := h.ag.tools
	out := tb.bash(t.Context(), map[string]any{"command": "echo local"}, "")
	if strings.Contains(out.Text, "[ran ") {
		t.Errorf("no remote yet: no provenance noise, got %q", out.Text)
	}
	// ssh to a bogus host: the command fails, the provenance is still true
	out = tb.bash(t.Context(), map[string]any{"command": "ssh -o BatchMode=yes -o ConnectTimeout=1 nobody@192.0.2.1 uname", "timeout": 5}, "")
	if !strings.Contains(out.Text, "REMOTE host nobody@192.0.2.1") {
		t.Errorf("remote tag missing: %q", out.Text)
	}
	out = tb.bash(t.Context(), map[string]any{"command": "echo local"}, "")
	if !strings.Contains(out.Text, "[ran LOCALLY on ") {
		t.Errorf("after a remote, local results are labelled: %q", out.Text)
	}
}

// The system prompt names the machine the harness runs on and says tools run
// there — the model must never infer "where am I" from a previous ssh.
func TestEnvBlockIdentity(t *testing.T) {
	s := envBlock(t.TempDir())
	if !strings.Contains(s, "jazz: v"+version) {
		t.Errorf("version missing: %q", s)
	}
	if p := epGetLight(); p != nil && p.Host != "" {
		if !strings.Contains(s, "host: ") || !strings.Contains(s, p.Host) || !strings.Contains(s, "tools run on THIS machine") {
			t.Errorf("identity line: %q", s)
		}
	}
}
