//go:build !windows

package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestEnvToolsCardapio runs the env_tools offline script through the same
// path jazz uses (Toolbox.Exec -> script). python3 is guaranteed present by
// scTestBox, so it must be reported with a path and a dotted version; a name
// that does not exist must land in `missing`, never in `tools`.
func TestEnvToolsCardapio(t *testing.T) {
	tb, _ := scTestBox(t) // skips if python3 is missing

	verRe := regexp.MustCompile(`^\d+\.\d+`)

	v, _ := scCall(t, tb, "env_tools", map[string]any{"tools": []string{"python3", "zz-no-such-tool-zz"}})
	if !scOK(v) {
		t.Fatalf("env_tools not ok: %v", v)
	}
	d := scDataOf(v)

	tools, _ := d["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want exactly python3", d["tools"])
	}
	py, _ := tools[0].(map[string]any)
	if n, _ := py["name"].(string); n != "python3" {
		t.Errorf("tools[0].name = %v, want python3", py["name"])
	}
	if p, _ := py["path"].(string); p == "" {
		t.Errorf("python3 path empty: %v", py)
	}
	ver, _ := py["version"].(string)
	if !verRe.MatchString(ver) {
		t.Errorf("python3 version = %q, want like 3.12.3", ver)
	}
	if ok, _ := py["ok"].(bool); !ok {
		t.Errorf("python3 ok = %v (error %v)", py["ok"], py["error"])
	}

	missing, _ := d["missing"].([]any)
	if len(missing) != 1 || missing[0] != "zz-no-such-tool-zz" {
		t.Errorf("missing = %v, want [zz-no-such-tool-zz]", d["missing"])
	}
	osb, _ := d["os"].(map[string]any)
	if s, _ := osb["system"].(string); s == "" {
		t.Errorf("os.system empty: %v", d["os"])
	}
	if _, isBool := d["truncated"].(bool); !isBool {
		t.Errorf("truncated must be a bool: %v", d["truncated"])
	}
	text, _ := d["text"].(string)
	if !strings.HasPrefix(text, "1 of 2 tools present: python3 ") || !strings.Contains(text, "missing: zz-no-such-tool-zz") {
		t.Errorf("text = %q", text)
	}

	// Default list: every one of the 18 names is either present or missing,
	// nothing is dropped, and nothing is duplicated.
	v2, _ := scCall(t, tb, "env_tools", map[string]any{})
	if !scOK(v2) {
		t.Fatalf("env_tools (default list) not ok: %v", v2)
	}
	d2 := scDataOf(v2)
	tools2, _ := d2["tools"].([]any)
	missing2, _ := d2["missing"].([]any)
	seen := map[string]bool{}
	for _, e := range tools2 {
		m, _ := e.(map[string]any)
		n, _ := m["name"].(string)
		if seen[n] {
			t.Errorf("duplicate tool %q", n)
		}
		seen[n] = true
	}
	for _, e := range missing2 {
		n, _ := e.(string)
		if seen[n] {
			t.Errorf("%q both present and missing", n)
		}
		seen[n] = true
	}
	if len(seen) != 18 {
		t.Errorf("present+missing = %d names, want 18 (%v / %v)", len(seen), d2["tools"], d2["missing"])
	}
	if !seen["python3"] || !seen["git"] || !seen["curl"] {
		t.Errorf("default list must include python3, git and curl: %v", seen)
	}

	// limit caps the probed tools and flags truncation honestly.
	v3, _ := scCall(t, tb, "env_tools", map[string]any{"limit": 1})
	if !scOK(v3) {
		t.Fatalf("env_tools (limit 1) not ok: %v", v3)
	}
	d3 := scDataOf(v3)
	if tools3, _ := d3["tools"].([]any); len(tools3) != 1 {
		t.Errorf("limit 1: tools = %v", d3["tools"])
	}
	if tr, _ := d3["truncated"].(bool); !tr && len(tools2) > 1 {
		t.Errorf("limit 1 with %d present tools must set truncated", len(tools2))
	}

	// A path is not a tool name: refuse instead of executing it.
	v4, _ := scCall(t, tb, "env_tools", map[string]any{"tools": []string{"/bin/sh"}})
	if scOK(v4) {
		t.Errorf("a path in tools must be refused: %v", v4)
	}
}
