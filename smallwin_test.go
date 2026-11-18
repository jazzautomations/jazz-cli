package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Measured on 17/11/2026: in an empty directory, with no project instructions,
// skills or MCP, the request for "oi" was 19019 bytes (system prompt 7747,
// ten tool schemas 10553), about 4.7k tokens. A model with a 4k-8k window
// never sees the task. With a small window the request must be compact.

const swTestPrefix = "You are Jazz, a coding agent"

func swSmall(tools string) func(*Config) {
	return func(c *Config) {
		c.Models = map[string]*ModelConf{c.Model: {Context: 8192, Tools: tools}}
	}
}

func TestSmallWindowModelGetsCompactRequest(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: `{"command":"echo SMALL-OK"}`}}}
		}
		return fakeResp{Text: "feito"}
	}, swSmall(""))
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if got := strings.Join(reqs[0].toolNames(), " "); got != "read write edit bash" {
		t.Errorf("tools of a small-window request = %q, want the four core tools", got)
	}
	if sys := reqs[0].system(); !strings.HasPrefix(sys, swTestPrefix) {
		t.Errorf("system prompt of a small-window request must be the short one, got %d bytes: %.60q", len(sys), sys)
	}
	raw, _ := json.Marshal(reqs[0].Raw)
	if len(raw) > 8000 {
		t.Errorf("small-window request = %d bytes, want at most 8000 (the full one was 19019)", len(raw))
	}
	t.Logf("small-window request: %d bytes, system %d", len(raw), len(reqs[0].system()))
	if reqs[1].system() != reqs[0].system() {
		t.Error("the short system prompt must stay byte-identical across requests (prompt cache)")
	}
	var out string
	for _, s := range reqs[1].toolResults() {
		out += s
	}
	if !strings.Contains(out, "SMALL-OK") {
		t.Errorf("the tool call of the small model must run: %q", out)
	}
}

func TestSmallWindowTextToolsModeListsOnlyCoreTools(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, swSmall("text"))
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	r := h.fs.requests()[0]
	sys := r.system()
	if r.HasTools || !strings.HasPrefix(sys, swTestPrefix) || !strings.Contains(sys, "<tool_calling>") {
		t.Fatalf("text-tools mode on a small window: short prompt plus the text block, no native tools (has_tools=%v): %.60q", r.HasTools, sys)
	}
	if !strings.Contains(sys, "bash") || strings.Contains(sys, "ask_user") || strings.Contains(sys, "fetch") {
		t.Errorf("the text block must describe the core tools only (%d bytes)", len(sys))
	}
}

func TestSmallWindowProjectInstructions(t *testing.T) {
	for _, c := range []struct {
		name, body string
		shown      bool
	}{
		{"short instructions are shown", "Use tabs. MARK-INSTR", true},
		{"long instructions are pointed to, never cut in the middle", "MARK-INSTR " + strings.Repeat("regra longa. ", 400), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, swSmall(""))
			if err := os.WriteFile(filepath.Join(h.root, "AGENTS.md"), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := h.run(t, "oi"); err != nil {
				t.Fatal(err)
			}
			sys := h.fs.requests()[0].system()
			if got := strings.Contains(sys, "MARK-INSTR"); got != c.shown {
				t.Errorf("instructions in the prompt = %v, want %v (%d bytes)", got, c.shown, len(sys))
			}
			if !c.shown && !strings.Contains(sys, "AGENTS.md") {
				t.Error("the prompt must say that instruction files exist")
			}
		})
	}
}

// A model with the default window is not touched.
func TestDefaultWindowRequestKeepsEveryTool(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, nil)
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	r := h.fs.requests()[0]
	got := " " + strings.Join(r.toolNames(), " ") + " "
	for _, want := range []string{" read ", " bash ", " task ", " todo ", " ask_user "} {
		if !strings.Contains(got, want) {
			t.Errorf("default-window request lost the tool%s: %s", want, got)
		}
	}
	if sys := r.system(); strings.HasPrefix(sys, swTestPrefix) || sys != h.ag.System {
		t.Errorf("default-window request must carry the full system prompt")
	}
}
