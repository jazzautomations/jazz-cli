//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// Cardápio v2 (docs/research/cardapio-offline.md): lint_run, symbol_refs and
// large_funcs run through the same script path the agent uses (Toolbox.Exec ->
// script). Fixtures live in jazz-scripts/testdata/cardapio_*.

func TestLintRunCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not installed")
	}
	// A valid but unformatted Go file: gofmt -l must list it.
	scWrite(t, root, "sub/bad.go", "package main\nfunc  main( ){\nx:=1\n_ = x\n}\n")
	scWrite(t, root, "sub/good.go", "package main\n\nfunc other() {}\n")

	v, out := scCall(t, tb, "lint_run", map[string]any{"tools": []string{"gofmt", "ruff"}})
	d := scDataOf(v)
	if !scOK(v) {
		t.Fatalf("lint_run: %v\n%s", v, out.Text)
	}
	run, _ := d["tools_run"].([]any)
	if len(run) != 1 {
		t.Fatalf("tools_run = %v, want only gofmt", d["tools_run"])
	}
	if r := run[0].(map[string]any); r["tool"] != "gofmt" || r["findings"] != 1.0 || r["exit"] != 0.0 {
		t.Errorf("gofmt entry: %v", r)
	}
	if ms, ok := run[0].(map[string]any)["ms"].(float64); !ok || ms < 0 {
		t.Errorf("ms: %v", run[0])
	}
	if fmt.Sprint(d["tools_missing"]) != "[ruff]" {
		t.Errorf("tools_missing = %v", d["tools_missing"])
	}
	fs, _ := d["findings"].([]any)
	if len(fs) != 1 {
		t.Fatalf("findings = %v", d["findings"])
	}
	f := fs[0].(map[string]any)
	if f["tool"] != "gofmt" || f["file"] != "sub/bad.go" || f["code"] != "gofmt" || f["severity"] != "warning" {
		t.Errorf("finding: %v", f)
	}
	if d["total"] != 1.0 || d["truncated"] != false || d["text"] != "1 finding from 1 tool (1 missing)" {
		t.Errorf("summary: total=%v truncated=%v text=%q", d["total"], d["truncated"], d["text"])
	}

	// limit truncates and says so.
	scWrite(t, root, "sub/bad2.go", "package main\nfunc  two( ){\n}\n")
	v, _ = scCall(t, tb, "lint_run", map[string]any{"tools": []string{"gofmt"}, "limit": 1})
	d = scDataOf(v)
	if fs, _ := d["findings"].([]any); len(fs) != 1 || d["total"] != 2.0 || d["truncated"] != true {
		t.Errorf("limit: %v", d)
	}

	// No requested linter installed: ok=true, empty findings, a warning, never a failure.
	v, out = scCall(t, tb, "lint_run", map[string]any{"tools": []string{"eslint", "shellcheck"}})
	d = scDataOf(v)
	if !scOK(v) || out.IsErr || len(d["findings"].([]any)) != 0 || len(d["tools_run"].([]any)) != 0 {
		t.Fatalf("no linter: %v", v)
	}
	if fmt.Sprint(d["tools_missing"]) != "[eslint shellcheck]" || !strings.Contains(fmt.Sprint(v["warnings"]), "no linter available") {
		t.Errorf("no linter: missing=%v warnings=%v", d["tools_missing"], v["warnings"])
	}
	if d["text"] != "0 findings from 0 tools (2 missing)" {
		t.Errorf("text = %q", d["text"])
	}

	// Unknown tool name fails closed (never passed to a shell).
	v, _ = scCall(t, tb, "lint_run", map[string]any{"tools": []string{"rm -rf"}})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "unknown tool") {
		t.Errorf("unknown tool: %v", v)
	}
}

func TestSymbolRefsCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "menu.py", scFixture(t, "cardapio_refs.py"))
	scWrite(t, root, "menu.go", scFixture(t, "cardapio_long.go"))
	scWrite(t, root, "node_modules/pkg/index.js", "var tally = 1; tally++;\n")

	// tally: defined once (line 79), used 3 times in code, once in the header comment.
	v, out := scCall(t, tb, "symbol_refs", map[string]any{"symbol": "tally"})
	d := scDataOf(v)
	if !scOK(v) || d["symbol"] != "tally" || d["files"] != 1.0 || d["truncated"] != false {
		t.Fatalf("symbol_refs: %v\n%s", v, out.Text)
	}
	refs, _ := d["refs"].([]any)
	if len(refs) != 4 || d["text"] != "4 refs in 1 file" {
		t.Fatalf("refs = %d (%v), text %q", len(refs), d["refs"], d["text"])
	}
	var code, comment int
	var order []string
	for _, r := range refs {
		m := r.(map[string]any)
		order = append(order, fmt.Sprintf("%v:%v", m["file"], m["line"]))
		if m["in_comment"] == true {
			comment++
		} else if m["in_string"] == false {
			code++
		}
	}
	if code != 3 || comment != 1 {
		t.Errorf("want 3 code refs + 1 comment ref, got %d/%d: %v", code, comment, refs)
	}
	if got := strings.Join(order, " "); got != "menu.py:2 menu.py:80 menu.py:81 menu.py:82" {
		t.Errorf("sorted order: %s", got)
	}
	if r := refs[1].(map[string]any); r["col"] != 15.0 || r["text"] != "doubled = tally * 2" {
		t.Errorf("first code ref: %v", r)
	}
	if fmt.Sprint(d["defs"]) != "[map[file:menu.py line:79]]" {
		t.Errorf("defs = %v", d["defs"])
	}
	if fmt.Sprint(d["by_file"]) != "map[menu.py:4]" {
		t.Errorf("by_file = %v (node_modules must be skipped)", d["by_file"])
	}

	// include_defs counts the definition line too.
	v, _ = scCall(t, tb, "symbol_refs", map[string]any{"symbol": "tally", "include_defs": true})
	if d = scDataOf(v); len(d["refs"].([]any)) != 5 || d["text"] != "5 refs in 1 file" {
		t.Errorf("include_defs: %v", d["text"])
	}

	// Strings and block comments in Go are flagged, not dropped; a method def is a def.
	scWrite(t, root, "svc.go", "package x\n\nfunc (s *S) Apply() {}\n\nfunc use() {\n\ts.Apply()\n\tmsg := \"call Apply now\"\n\t/* Apply is\n\t   documented */\n\t_ = msg\n}\n")
	v, _ = scCall(t, tb, "symbol_refs", map[string]any{"symbol": "Apply", "lang": "go"})
	d = scDataOf(v)
	refs, _ = d["refs"].([]any)
	if len(refs) != 3 || fmt.Sprint(d["defs"]) != "[map[file:svc.go line:3]]" {
		t.Fatalf("go refs: %v", d)
	}
	kinds := []string{}
	for _, r := range refs {
		m := r.(map[string]any)
		kinds = append(kinds, fmt.Sprintf("%v:c=%v,s=%v", m["line"], m["in_comment"], m["in_string"]))
	}
	if got := strings.Join(kinds, " "); got != "6:c=false,s=false 7:c=false,s=true 8:c=true,s=false" {
		t.Errorf("flags: %s", got)
	}

	// Word boundary: "total" never matches "subtotal"/"line_total"; unknown symbol -> 0 refs, ok.
	v, _ = scCall(t, tb, "symbol_refs", map[string]any{"symbol": "nothing_here_xyz"})
	if d = scDataOf(v); !scOK(v) || d["files"] != 0.0 || d["text"] != "0 refs in 0 files" {
		t.Errorf("no refs: %v", d)
	}
	v, _ = scCall(t, tb, "symbol_refs", map[string]any{"symbol": "bad name"})
	if scOK(v) {
		t.Errorf("invalid identifier must fail: %v", v)
	}
}

func TestLargeFuncsCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "menu.py", scFixture(t, "cardapio_refs.py"))
	scWrite(t, root, "menu.go", scFixture(t, "cardapio_long.go"))
	scWrite(t, root, "vendor/big.go", "package v\n\nfunc Vendored() {\n"+strings.Repeat("\t_ = 1\n", 100)+"}\n")

	v, out := scCall(t, tb, "large_funcs", nil)
	d := scDataOf(v)
	if !scOK(v) || d["count"] != 2.0 || d["files_scanned"] != 2.0 || d["truncated"] != false {
		t.Fatalf("large_funcs: %v\n%s", v, out.Text)
	}
	fs := d["funcs"].([]any)
	first, second := fs[0].(map[string]any), fs[1].(map[string]any)
	if first["name"] != "compute_rollup" || first["file"] != "menu.py" || first["line"] != 6.0 || first["lines"] != 70.0 || first["lang"] != "py" {
		t.Errorf("python func: %v", first)
	}
	if second["name"] != "Rollup" || second["file"] != "menu.go" || second["line"] != 22.0 || second["lines"] != 60.0 || second["lang"] != "go" {
		t.Errorf("go func (braces in strings/comments must not count): %v", second)
	}
	if c, _ := first["confidence"].(float64); c <= 0 || c > 1 {
		t.Errorf("confidence: %v", first["confidence"])
	}
	if d["text"] != "2 functions over 60 lines (max: compute_rollup menu.py:6 70 lines)" {
		t.Errorf("text = %q", d["text"])
	}

	// min_lines, lang filter and limit.
	v, _ = scCall(t, tb, "large_funcs", map[string]any{"min_lines": 5, "lang": "py"})
	d = scDataOf(v)
	if fs = d["funcs"].([]any); len(fs) != 2 || fs[1].(map[string]any)["name"] != "record" || fs[1].(map[string]any)["lines"] != 5.0 {
		t.Errorf("min_lines/lang: %v", d["funcs"])
	}
	v, _ = scCall(t, tb, "large_funcs", map[string]any{"min_lines": 5, "limit": 1})
	if d = scDataOf(v); len(d["funcs"].([]any)) != 1 || d["count"] != 3.0 || d["truncated"] != true {
		t.Errorf("limit: count=%v truncated=%v", d["count"], d["truncated"])
	}

	// JS/TS: function, arrow and class method, counted by braces; nothing over 60 -> clean text.
	scWrite(t, root, "app.ts", "export function a() {\n"+strings.Repeat("  x();\n", 8)+"}\n"+
		"const b = async (q: number): Promise<void> => {\n"+strings.Repeat("  y();\n", 6)+"};\n"+
		"class K {\n  async run(): Promise<void> {\n"+strings.Repeat("    z();\n", 4)+"  }\n  if (x) {\n  }\n}\n")
	v, _ = scCall(t, tb, "large_funcs", map[string]any{"min_lines": 3, "lang": "js"})
	d = scDataOf(v)
	var names []string
	for _, f := range d["funcs"].([]any) {
		m := f.(map[string]any)
		names = append(names, fmt.Sprintf("%v=%v", m["name"], m["lines"]))
	}
	if got := strings.Join(names, " "); got != "a=10 b=8 run=6" {
		t.Errorf("js funcs: %s", got)
	}
	v, _ = scCall(t, tb, "large_funcs", map[string]any{"lang": "js"})
	if d = scDataOf(v); d["count"] != 0.0 || d["text"] != "no function over 60 lines in 1 files" {
		t.Errorf("empty: %v", d)
	}
}
