//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pfHookCfg(ev, matcher, cmd string) *Config {
	return &Config{Hooks: map[string][]HookMatcher{ev: {{Matcher: matcher, Hooks: []HookCmd{{Type: "command", Command: cmd}}}}}}
}

func TestHooksBlockWithExit2(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	h := LoadHooks(pfHookCfg(HookPreToolUse, "Bash", `cat >/dev/null; echo "no rm here" >&2; exit 2`), root, false)
	if !h.Has(HookPreToolUse) || h.Has(HookStop) {
		t.Fatal("Has wrong")
	}
	r := h.Run(context.Background(), HookPreToolUse, "bash", map[string]any{"tool_input": map[string]any{"command": "rm -rf x"}})
	if !r.Block || r.Reason != "no rm here" || len(r.Ran) != 1 {
		t.Errorf("exit 2 must block with stderr as reason: %+v", r)
	}
	// matcher does not match edit
	if r := h.Run(context.Background(), HookPreToolUse, "edit", nil); r.Block || len(r.Ran) != 0 {
		t.Errorf("matcher Bash ran for edit: %+v", r)
	}
	var nilSet *HookSet
	if nilSet.Has(HookPreToolUse) || nilSet.Run(context.Background(), HookStop, "", nil).Block {
		t.Error("nil HookSet must be inert")
	}
}

func TestHooksPayloadAndJSONOutput(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	dump := filepath.Join(root, "payload.json")
	cmd := `cat > ` + dump + `; echo '{"hookSpecificOutput":{"additionalContext":"remember gofmt","updatedInput":{"command":"ls -la"}},"decision":"approve","reason":"ok by policy"}'`
	h := LoadHooks(pfHookCfg(HookPreToolUse, "Edit|Bash", cmd), root, false)
	r := h.Run(context.Background(), HookPreToolUse, "bash", map[string]any{"session_id": "s1", "tool_input": map[string]any{"command": "ls"}})
	if r.Block || !r.Approve || r.Context != "remember gofmt" || r.UpdatedInput != `{"command":"ls -la"}` {
		t.Errorf("JSON output not merged: %+v", r)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload not JSON: %s", b)
	}
	if p["hook_event_name"] != HookPreToolUse || p["tool_name"] != "Bash" || p["session_id"] != "s1" || p["cwd"] != root {
		t.Errorf("payload: %v", p)
	}
}

func TestHooksDecisionBlockAndWarnings(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	cfg := &Config{Hooks: map[string][]HookMatcher{
		HookStop: {
			{Hooks: []HookCmd{{Command: `exit 1`}}}, // non-blocking error
			{Hooks: []HookCmd{{Command: `echo '{"decision":"block","reason":"tests not run yet"}'`}}},
		},
		HookUserPromptSubmit: {{Hooks: []HookCmd{{Command: `echo "branch: main"`}}}},
	}}
	h := LoadHooks(cfg, root, false)
	r := h.Run(context.Background(), HookStop, "", nil)
	if !r.Block || r.Reason != "tests not run yet" || len(r.Warnings) != 1 {
		t.Errorf("stop hook: %+v", r)
	}
	r = h.Run(context.Background(), HookUserPromptSubmit, "", map[string]any{"prompt": "hi"})
	if r.Block || r.Context != "branch: main" {
		t.Errorf("plain stdout is context for UserPromptSubmit: %+v", r)
	}
}

func TestHooksTimeout(t *testing.T) {
	pfTestHome(t)
	cfg := &Config{Hooks: map[string][]HookMatcher{HookPostToolUse: {{Hooks: []HookCmd{{Command: "sleep 30", Timeout: 1}}}}}}
	h := LoadHooks(cfg, t.TempDir(), false)
	t0 := time.Now()
	r := h.Run(context.Background(), HookPostToolUse, "edit", nil)
	if time.Since(t0) > 5*time.Second {
		t.Fatalf("timeout not enforced: %v", time.Since(t0))
	}
	if r.Block || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "tempo esgotado") {
		t.Errorf("timeout must be a warning: %+v", r)
	}
}

func TestHooksProjectFilesNeedTrust(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".jazz"), 0o755)
	os.WriteFile(filepath.Join(root, ".jazz", "settings.json"), []byte(`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"exit 2"}]}]}}`), 0o644)
	if LoadHooks(&Config{}, root, pfProjectPolicyTrusted(root)).Has(HookPreToolUse) {
		t.Fatal("untrusted project hooks loaded")
	}
	if err := Trust(root, "hooks", pfProjectPolicy(root)); err != nil {
		t.Fatal(err)
	}
	h := LoadHooks(&Config{}, root, pfProjectPolicyTrusted(root))
	if !h.Has(HookPreToolUse) || !h.Run(context.Background(), HookPreToolUse, "read", nil).Block {
		t.Error("trusted project hook not loaded")
	}
}
