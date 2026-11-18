//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mcpLazyNames(specs []ToolSpec) string {
	var n []string
	for _, s := range specs {
		n = append(n, s.Name)
	}
	return strings.Join(n, " ")
}

// mcpLazyLimit sets mcpEagerMax for one test.
func mcpLazyLimit(t *testing.T, max int) {
	t.Helper()
	old := mcpEagerMax
	mcpEagerMax = max
	t.Cleanup(func() { mcpEagerMax = old })
}

// Over the limit, a request carries mcp_find and the loaded tools only; the
// other tools stay callable and are loaded by a search or by a call.
func TestMCPSchemasLoadOnDemandOverTheLimit(t *testing.T) {
	mcpLazyLimit(t, 1) // the fake server exposes two tools
	m, root := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	echo, fail := mcpToolName("fake", "echo"), mcpToolName("fake", "fail")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := mcpLazyNames(m.ModelSpecs())
	if !strings.Contains(got, mcpFindTool) || strings.Contains(got, echo) || strings.Contains(got, fail) {
		t.Fatalf("before any search only mcp_find is sent, got: %s", got)
	}
	if !m.Has(echo) || !m.Has(fail) || !m.Has(mcpFindTool) || !m.ReadOnly(mcpFindTool) {
		t.Fatal("tools not loaded yet must stay callable, and mcp_find is a read-only meta tool")
	}
	if all := mcpLazyNames(m.Specs()); !strings.Contains(all, echo) || !strings.Contains(all, fail) {
		t.Errorf("the full list (for /mcp) must keep every tool: %s", all)
	}

	// the system prompt and a research subagent get the same short list
	sys, _ := buildSystemPrompt(root, m.ModelSpecs())
	if !strings.Contains(sys, "- "+mcpFindTool+":") || strings.Contains(sys, echo) {
		t.Errorf("system prompt must list mcp_find and not the unloaded tools")
	}
	tb := NewToolbox(root)
	t.Cleanup(tb.Shell.Close)
	sub := NewAgent(&Config{Model: "fake/m1", Mode: "auto"}, &recUI{answer: Allow}, tb, m, "fake/m1")
	sub.sub = true
	if s := mcpLazyNames(sub.toolSpecs()); strings.Contains(s, echo) || strings.Contains(s, fail) || !strings.Contains(s, mcpFindTool) {
		t.Errorf("a subagent must not carry unloaded MCP schemas: %s", s)
	}

	out := m.Call(ctx, mcpFindTool, `{"query":"echo text"}`)
	if out.IsErr || !strings.Contains(out.Text, echo) || strings.Contains(out.Text, fail) {
		t.Fatalf("search must return the matching tool only: err=%v %q", out.IsErr, out.Text)
	}
	if got = mcpLazyNames(m.ModelSpecs()); !strings.Contains(got, echo) || strings.Contains(got, fail) {
		t.Errorf("after the search only the match is loaded: %s", got)
	}

	out = m.Call(ctx, mcpFindTool, `{"query":"zzzz"}`)
	if !strings.Contains(out.Text, "No MCP tool matches") || mcpLazyNames(m.ModelSpecs()) != got {
		t.Errorf("a search with no match loads nothing: %q", out.Text)
	}

	m.Call(ctx, fail, `{}`) // called by its name, never searched
	if got = mcpLazyNames(m.ModelSpecs()); !strings.HasSuffix(got, echo+" "+fail) {
		t.Errorf("a tool called by name is loaded after the earlier ones (append-only): %s", got)
	}

	out = m.Call(ctx, mcpFindTool, `{"query":""}`)
	if out.IsErr || !strings.Contains(out.Text, echo) || !strings.Contains(out.Text, fail) {
		t.Errorf("an empty query lists the tool names: %q", out.Text)
	}
}

// At or under the limit nothing changes: every schema is sent and mcp_find
// does not exist.
func TestMCPSchemasAllSentUnderTheLimit(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	if a, b := mcpLazyNames(m.ModelSpecs()), mcpLazyNames(m.Specs()); a != b || strings.Contains(a, mcpFindTool) {
		t.Errorf("under the limit the request carries every MCP spec:\n model: %s\n all:   %s", a, b)
	}
	if m.Has(mcpFindTool) {
		t.Error("mcp_find must not be exposed under the limit")
	}
	if out := m.Call(context.Background(), mcpFindTool, `{"query":"echo"}`); !out.IsErr {
		t.Errorf("mcp_find must be refused under the limit: %q", out.Text)
	}
}

func TestMCPFindLimitIsClamped(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{{nil, mcpFindDefault}, {float64(3), 3}, {float64(0), mcpFindDefault}, {float64(-4), mcpFindDefault}, {float64(999), mcpFindMax}, {"5", 5}, {"x", mcpFindDefault}} {
		if got := mcpFindLimit(c.in); got != c.want {
			t.Errorf("mcpFindLimit(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
