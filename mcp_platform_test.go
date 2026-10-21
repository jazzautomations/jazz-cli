//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pfSpecNames(m *MCPManager) []string {
	var out []string
	for _, s := range m.Specs() {
		out = append(out, s.Name)
	}
	return out
}

func TestMcpPfListChangedAppendsOnly(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	before := pfSpecNames(m)
	if out := m.Call(context.Background(), "mcp__fake__addtool", "{}"); out.IsErr {
		t.Fatalf("addtool: %+v", out)
	}
	deadline := time.Now().Add(10 * time.Second)
	var after []string
	for time.Now().Before(deadline) {
		after = pfSpecNames(m)
		if len(after) > len(before) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("list_changed did not add the new tool: %v", after)
	}
	if strings.Join(after[:len(before)], ",") != strings.Join(before, ",") {
		t.Errorf("existing specs reordered:\n%v\n%v", before, after)
	}
	if after[len(after)-1] != "mcp__fake__added" {
		t.Errorf("new tool must be appended at the end: %v", after)
	}
	if out := m.Call(context.Background(), "mcp__fake__added", "{}"); out.IsErr || out.Text != "added ok" {
		t.Errorf("new tool not callable: %+v", out)
	}
}

func TestMcpPfToolFilter(t *testing.T) {
	c := mcpUiFakeConf(t, "")
	c.EnabledTools = []string{"echo", "s*"}
	c.DisabledTools = []string{"slow"}
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": c})
	got := strings.Join(pfSpecNames(m), ",")
	if got != "mcp__fake__echo,mcp__fake__status,mcp__fake__structured" {
		t.Errorf("filtered specs = %s", got)
	}
	if m.Has("mcp__fake__slow") || m.Has("mcp__fake__crash") {
		t.Error("filtered tools must not be callable")
	}
}

func TestMcpPfCloseKillsServerIgnoringTerm(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "ignore_term")})
	s := mcpUiServer(t, m, "fake")
	if mcpUiSt(s) != "ready" {
		t.Fatalf("status %s", mcpUiSt(s))
	}
	s.mu.Lock()
	pid := s.pid
	s.mu.Unlock()
	t0 := time.Now()
	m.Close()
	if el := time.Since(t0); el > 6*time.Second {
		t.Errorf("Close took %v", el)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("server %d still alive after Close (SIGKILL fallback must be synchronous)", pid)
	}
	m.Close() // idempotent
}

func TestMcpPfStdioEnvScrubbed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-TESTKEY-should-not-leak-0000000000")
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	out := m.Call(context.Background(), "mcp__fake__env", "{}")
	if out.IsErr || strings.HasPrefix(out.Text, "sk-") {
		t.Errorf("provider key leaked into MCP server env: %+v", out)
	}
}

func TestMcpPfProjectServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-TESTKEY-should-not-leak-0000000000")
	t.Setenv("MCPUI_ALLOWED", "allowed-value")
	global := mcpUiFakeConf(t, "")
	mcpUiWriteJSON(t, filepath.Join(home, "mcp.json"), map[string]any{
		"mcpServers":  map[string]any{"shared": global},
		"project_env": []string{"MCPUI_ALLOWED", "OPENAI_API_KEY"},
	})
	root := t.TempDir()
	proj := mcpUiFakeConf(t, "")
	proj.Env["MCPUI_PROJ"] = "${OPENAI_API_KEY:-none}"
	proj.Env["MCPUI_SAFE"] = "${MCPUI_ALLOWED}"
	evil := MCPServerConf{Command: "/bin/false"}
	mcpUiWriteJSON(t, filepath.Join(root, ".mcp.json"), map[string]any{"mcpServers": map[string]any{"proj": proj, "shared": evil}})

	// untrusted: only the global server
	m := StartMCP(root)
	m.Wait(60 * time.Second)
	if len(m.Servers) != 1 || m.Servers[0].Name != "shared" {
		t.Fatalf("untrusted project servers must not start: %d servers", len(m.Servers))
	}
	m.Close()

	raw, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err := Trust(root, "mcp", raw); err != nil {
		t.Fatal(err)
	}
	m = StartMCP(root)
	t.Cleanup(m.Close)
	m.Wait(60 * time.Second)
	if len(m.Servers) != 2 {
		t.Fatalf("trusted project server missing: %d", len(m.Servers))
	}
	if s := mcpUiServer(t, m, "shared"); s.Conf.Command != global.Command || s.Project {
		t.Errorf("project must not override the global server: %+v", s.Conf)
	}
	if s := mcpUiServer(t, m, "proj"); !s.Project || mcpUiSt(s) != "ready" {
		t.Fatalf("proj: project=%v status=%s err=%v", s.Project, mcpUiSt(s), mcpUiErr(s))
	}
	out := m.Call(context.Background(), "mcp__proj__env", "{}")
	if out.Text != "|none|allowed-value" {
		t.Errorf("project env expansion = %q, want |none|allowed-value (provider keys never expand)", out.Text)
	}
}

func TestMcpPfExpandProjectEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("MCPUI_X", "x")
	t.Setenv("HOME", "/home/test")
	f := pfExpandProjectEnv(map[string]bool{"MCPUI_X": true, "ANTHROPIC_API_KEY": true})
	cases := map[string]string{
		"${HOME}/a":                 "/home/test/a",
		"${MCPUI_X}":                "x",
		"${ANTHROPIC_API_KEY}":      "",
		"${ANTHROPIC_API_KEY:-def}": "def",
		"${NOT_ALLOWED:-d}":         "d",
	}
	for in, want := range cases {
		if got := f(in); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}
