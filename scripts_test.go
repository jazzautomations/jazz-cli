//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// scShipped are the 18 scripts docs/research/skills-personas-scripts.md §D ships, the
// 8 of cardápio v1 and 10 of v2 (docs/research/cardapio-offline.md),
// plus ui_lint (the frontend-craft detector, uilint.go) and api_map (the
// project API map, apimap.go).
var scShipped = []string{"api_map", "archive_list", "config_get", "cron_list", "csv_stats", "csv_to_json", "date_normalize", "dep_graph", "dep_outdated", "detect_project", "diff_apply_check", "diff_quality", "dir_diff", "disk_usage", "dotenv_check", "dup_code", "env_get", "env_tools", "extract_stacktrace", "extract_versions", "file_dupes", "file_find", "file_hash_diff", "file_split", "file_users", "find_symbol", "fs_recent", "git_blame_hot", "git_branch_status", "git_cochange", "git_conflict_scan", "git_safe", "git_state", "hash_verify", "http_check", "json_edit", "json_schema_infer", "large_funcs", "license_scan", "line_slice", "lint_run", "log_errors_recent", "md_outline", "mem_top", "net_ifaces", "net_route", "parse_build_errors", "parse_log_lines", "parse_test_output", "perm_find", "pkg_installed", "port_free", "proc_ports", "regex_test", "run_tests", "service_status", "snapshot_cmd", "symbol_refs", "table_render", "text_count", "text_extract", "text_grep", "todo_scan", "tree_summary", "ui_lint", "uptime_load", "url_parse", "wordcount_stats", "yaml_get"}

func scTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
}

func scTestBox(t *testing.T) (*Toolbox, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	scTestEnv(t)
	root := t.TempDir()
	tb := NewToolbox(root)
	tb.Scripts = true
	t.Cleanup(tb.Shell.Close)
	return tb, root
}

func scCall(t *testing.T, tb *Toolbox, name string, args map[string]any) (map[string]any, ToolOutput) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": name, "args": args})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out := tb.Exec(ctx, "script", string(b))
	var v map[string]any
	if err := json.Unmarshal([]byte(out.Text), &v); err != nil {
		if out.IsErr { // refused before the script ran (schema validation): a failure, not JSON
			return map[string]any{"ok": false, "error": out.Text}, out
		}
		t.Fatalf("%s: result is not JSON: %v\n%s", name, err, out.Text)
	}
	return v, out
}

func scDataOf(v map[string]any) map[string]any {
	d, _ := v["data"].(map[string]any)
	return d
}

func scWrite(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func scFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("jazz-scripts", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func scGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func scNeedGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func scConf(v map[string]any) float64 { c, _ := v["confidence"].(float64); return c }
func scOK(v map[string]any) bool      { ok, _ := v["ok"].(bool); return ok }

// ---------- registry and spec ----------

func TestScriptRegistryShipsAll(t *testing.T) {
	tb, _ := scTestBox(t)
	r := tb.scRegistry()
	if len(r.warns) > 0 {
		t.Fatalf("load warnings: %v", r.warns)
	}
	got := append([]string(nil), r.names...)
	if strings.Join(got, ",") != strings.Join(scShipped, ",") {
		t.Fatalf("scripts = %v\nwant %v", got, scShipped)
	}
	for _, n := range r.names {
		d := r.defs[n]
		if d.Version < 1 || d.Description == "" || len(d.Description) > 120 {
			t.Errorf("%s: version %d, description %q (need 1..120 chars)", n, d.Version, d.Description)
		}
		if typ, _ := d.Args["type"].(string); typ != "object" {
			t.Errorf("%s: args schema type = %v", n, d.Args["type"])
		}
		if !strings.Contains(string(d.body), "def emit(") && !strings.Contains(string(d.body), "emit_ok()") {
			t.Errorf("%s: prelude missing from the assembled script", n)
		}
	}
}

func TestScriptSpecCompactAndStable(t *testing.T) {
	tb, _ := scTestBox(t)
	specs := tb.Specs()
	last := specs[len(specs)-1]
	if last.Name != "script" {
		t.Fatalf("script spec should be appended last among built-ins, got %s", last.Name)
	}
	if n := strings.Count(last.Description, "\n") + 1; n > 3 {
		t.Errorf("description has %d lines, want <= 3", n)
	}
	b, _ := json.Marshal(last)
	if est := len(b) / 4; est > 923 { // 71 scripts (cardápio v1–v5 + file_split), ~13 tokens each after capping optionals at 2; raise only with the same arithmetic
		t.Errorf("script spec ~%d tokens, keep it small (<923)", est)
	}
	props := last.Params["properties"].(map[string]any)
	enum := props["name"].(map[string]any)["enum"].([]string)
	if strings.Join(enum, ",") != strings.Join(scShipped, ",") {
		t.Errorf("enum = %v", enum)
	}
	desc := props["name"].(map[string]any)["description"].(string)
	for _, want := range []string{"find_symbol(symbol,ext?,kind?,+1)", "parse_test_output(file|cmd|text,framework?)", "git_state()"} {
		if !strings.Contains(desc, want) {
			t.Errorf("signature list misses %q: %s", want, desc)
		}
	}
	// byte-stable: a second toolbox for the same root builds the same spec
	tb2 := NewToolbox(tb.Root)
	tb2.Scripts = true
	defer tb2.Shell.Close()
	s2 := tb2.Specs()
	b2, _ := json.Marshal(s2[len(s2)-1])
	if string(b) != string(b2) {
		t.Error("script spec is not byte-stable across loads")
	}
	// off: no spec, and the tool is unknown
	tb3 := NewToolbox(tb.Root)
	defer tb3.Shell.Close()
	for _, s := range tb3.Specs() {
		if s.Name == "script" {
			t.Fatal("Scripts=false must not expose the tool")
		}
	}
	if out := tb3.Exec(context.Background(), "script", `{"name":"git_state"}`); !out.IsErr || !strings.Contains(out.Text, "not found") {
		t.Errorf("disabled script tool should be unknown: %q", out.Text)
	}
}

func TestScriptsOnFlagAndConfig(t *testing.T) {
	cfg := &Config{}
	if !pfScriptsOn(cfg, "") {
		t.Error("default should be on")
	}
	cfg.Scripts = "off"
	if pfScriptsOn(cfg, "") || !pfScriptsOn(cfg, "on") {
		t.Error("config off / flag on override broken")
	}
	cfg.Scripts = ""
	if pfScriptsOn(cfg, "0") || pfScriptsOn(cfg, "false") {
		t.Error("--scripts 0/false should turn it off")
	}
}

// ---------- validation ----------

func TestScriptValidateAndCoerce(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"symbol"},
		"properties": map[string]any{
			"symbol":  map[string]any{"type": "string"},
			"depth":   map[string]any{"type": "integer", "minimum": 0.0, "maximum": 6.0},
			"update":  map[string]any{"type": "boolean"},
			"kind":    map[string]any{"enum": []any{"any", "func"}},
			"cmd":     map[string]any{"type": "string"},
			"value":   map[string]any{},
			"pathset": map[string]any{"type": "string"},
		}}
	got, errs := scValidate(schema, map[string]any{"symbol": "Foo", "depth": "3", "update": "true", "kind": "FUNC", "value": []any{1.0}, "pathset": []any{"a", "b"}})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got["depth"] != 3.0 || got["update"] != true || got["kind"] != "func" || got["pathset"] != "a b" {
		t.Errorf("coercion: %#v", got)
	}
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, `missing required argument "symbol"`},
		{map[string]any{"symbol": "x", "bogus": 1.0}, `unknown argument "bogus"`},
		{map[string]any{"symbol": "x", "depth": 9.0}, "must be <= 6"},
		{map[string]any{"symbol": "x", "depth": 1.5}, "must be an integer"},
		{map[string]any{"symbol": "x", "depth": "abc"}, "must be a number"},
		{map[string]any{"symbol": "x", "kind": "nope"}, "must be one of any, func"},
		{map[string]any{"symbol": "x", "update": "maybe"}, "true or false"},
		{map[string]any{"symbol": map[string]any{}}, `"symbol" must be a string`},
	}
	for _, c := range cases {
		_, errs := scValidate(schema, c.args)
		if !anyContains(errs, c.want) {
			t.Errorf("%v: errors %v, want %q", c.args, errs, c.want)
		}
	}
	// synonyms and case folding
	got, errs = scValidate(schema, map[string]any{"Symbol": "Foo", "command": "ls"})
	if len(errs) > 0 || got["symbol"] != "Foo" || got["cmd"] != "ls" {
		t.Errorf("folding: %v %v", got, errs)
	}
	// anyOf of required
	alt := map[string]any{"type": "object", "properties": map[string]any{"file": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}},
		"anyOf": []any{map[string]any{"required": []any{"file"}}, map[string]any{"required": []any{"text"}}}}
	if _, errs := scValidate(alt, map[string]any{}); !anyContains(errs, "pass one of: file, text") {
		t.Errorf("anyOf: %v", errs)
	}
	if _, errs := scValidate(alt, map[string]any{"text": "x"}); len(errs) > 0 {
		t.Errorf("anyOf satisfied: %v", errs)
	}
}

func TestScriptSplitWeakModelShapes(t *testing.T) {
	cases := []map[string]any{
		{"name": "find_symbol", "args": map[string]any{"symbol": "Foo"}},
		{"name": "find_symbol", "args": `{"symbol":"Foo"}`},
		{"name": "find_symbol.py", "symbol": "Foo"},
		{"script": "FIND_SYMBOL", "arguments": map[string]any{"symbol": "Foo"}},
	}
	for _, c := range cases {
		n, a, e := scSplit(c)
		if n != "find_symbol" || a["symbol"] != "Foo" || e != "" {
			t.Errorf("%v -> %q %v %q", c, n, a, e)
		}
	}
	if _, _, e := scSplit(map[string]any{"name": "x", "args": "{nope"}); e == "" {
		t.Error("broken JSON args should be reported")
	}
}

func TestScriptToolErrors(t *testing.T) {
	tb, _ := scTestBox(t)
	out := tb.Exec(context.Background(), "script", `{"name":"nope"}`)
	if !out.IsErr || !strings.Contains(out.Text, "Unknown script") || !strings.Contains(out.Text, "git_state") {
		t.Errorf("unknown script: %q", out.Text)
	}
	out = tb.Exec(context.Background(), "script", `{"name":"find_symbol","args":{}}`)
	if !out.IsErr || !strings.Contains(out.Text, `missing required argument "symbol"`) || !strings.Contains(out.Text, "args schema:") {
		t.Errorf("validation error should carry the schema: %q", out.Text)
	}
	out = tb.Exec(context.Background(), "script", `{}`)
	if !out.IsErr || !strings.Contains(out.Text, "missing required parameter 'name'") {
		t.Errorf("missing name: %q", out.Text)
	}
	out = tb.Exec(context.Background(), "script", `{"name":"json_edit","args":{"file":"a.json","op":"set","value":"x`)
	if !out.IsErr || !strings.Contains(out.Text, "cut off") {
		t.Errorf("truncated args must be refused: %q", out.Text)
	}
}

// ---------- each built-in (one subtest per shipped script) ----------

func TestScriptsEachBuiltin(t *testing.T) {
	tb, root := scTestBox(t)
	tested := map[string]bool{}
	run := func(name string, f func(t *testing.T)) {
		tested[name] = true
		t.Run(name, f)
	}

	run("detect_project", func(t *testing.T) {
		scWrite(t, root, "go.mod", "module x\n")
		scWrite(t, root, "main.go", "package main\n")
		defer os.Remove(filepath.Join(root, "go.mod"))
		defer os.Remove(filepath.Join(root, "main.go"))
		v, _ := scCall(t, tb, "detect_project", nil)
		d := scDataOf(v)
		if !scOK(v) || d["test"] != "go test ./..." || d["build"] != "go build ./..." || fmt.Sprint(d["langs"]) != "[go]" {
			t.Errorf("detect_project: %v", v)
		}
	})

	run("parse_test_output", func(t *testing.T) {
		v, _ := scCall(t, tb, "parse_test_output", map[string]any{"text": scFixture(t, "gotest_fail.txt")})
		d := scDataOf(v)
		if !scOK(v) || d["framework"] != "gotest" || d["failed"] != 1.0 || d["passed"] != 3.0 || d["skipped"] != 1.0 || scConf(v) < 0.8 {
			t.Fatalf("go test: %v", v)
		}
		f := d["failures"].([]any)[0].(map[string]any)
		if f["test"] != "TestEditApply" || f["file"] != "edit_test.go" || f["line"] != 41.0 {
			t.Errorf("failure location: %v", f)
		}
		v, _ = scCall(t, tb, "parse_test_output", map[string]any{"text": scFixture(t, "pytest_fail.txt")})
		d = scDataOf(v)
		if d["framework"] != "pytest" || d["failed"] != 1.0 || d["passed"] != 3.0 || d["skipped"] != 1.0 {
			t.Errorf("pytest: %v", v)
		}
		// negative control: prose fails closed
		v, out := scCall(t, tb, "parse_test_output", map[string]any{"text": scFixture(t, "prose.txt")})
		if scOK(v) && scConf(v) >= 0.8 || !out.IsErr && scOK(v) {
			t.Errorf("prose must fail closed: %v", v)
		}
	})

	run("run_tests", func(t *testing.T) {
		scWrite(t, root, "out.txt", scFixture(t, "gotest_fail.txt"))
		v, _ := scCall(t, tb, "run_tests", map[string]any{"cmd": "cat out.txt; exit 1"})
		d := scDataOf(v)
		if !scOK(v) || d["exit_code"] != 1.0 || d["status"] != "fail" || d["failed"] != 1.0 {
			t.Errorf("run_tests: %v", v)
		}
		// default command comes from project detection
		scWrite(t, root, "go.mod", "module x\n")
		defer os.Remove(filepath.Join(root, "go.mod"))
		if p := tb.scPolicyArgs(map[string]any{"name": "run_tests", "args": map[string]any{"target": "internal/x"}}); p.cmd != "go test ./internal/x" {
			t.Errorf("default run_tests cmd = %q", p.cmd)
		}
	})

	run("parse_build_errors", func(t *testing.T) {
		v, _ := scCall(t, tb, "parse_build_errors", map[string]any{"text": scFixture(t, "build_errors.txt")})
		d := scDataOf(v)
		if !scOK(v) || d["error_count"].(float64) < 4 || d["warning_count"] != 1.0 {
			t.Fatalf("build errors: %v", v)
		}
		e := d["errors"].([]any)[0].(map[string]any)
		if e["file"] != "scripts.go" || e["line"] != 288.0 || e["col"] != 26.0 {
			t.Errorf("first error: %v", e)
		}
		tools := fmt.Sprint(d["tools"])
		for _, k := range []string{"go", "tsc", "cc", "cargo"} {
			if !strings.Contains(tools, k) {
				t.Errorf("tools %s misses %s", tools, k)
			}
		}
		v, _ = scCall(t, tb, "parse_build_errors", map[string]any{"text": scFixture(t, "prose.txt")})
		if scOK(v) && scConf(v) >= 0.8 {
			t.Errorf("prose must fail closed: %v", v)
		}
	})

	run("extract_stacktrace", func(t *testing.T) {
		scWrite(t, root, "app/main.py", "def run():\n    return 1 / 0\nrun()\n")
		v, _ := scCall(t, tb, "extract_stacktrace", map[string]any{"text": scFixture(t, "traceback.txt")})
		d := scDataOf(v)
		if !scOK(v) || d["lang"] != "python" || d["exception"] != "ZeroDivisionError" || d["message"] != "division by zero" {
			t.Fatalf("traceback: %v", v)
		}
		fp, _ := d["first_project_frame"].(map[string]any)
		if fp["file"] != "app/main.py" || fp["line"] != 2.0 || fp["in_project"] != true {
			t.Errorf("project frame: %v", fp)
		}
		v, out := scCall(t, tb, "extract_stacktrace", map[string]any{"text": scFixture(t, "prose.txt")})
		if scOK(v) || !out.IsErr {
			t.Errorf("prose must fail closed: %v", v)
		}
	})

	run("parse_log_lines", func(t *testing.T) {
		v, _ := scCall(t, tb, "parse_log_lines", map[string]any{"text": scFixture(t, "app.log")})
		d := scDataOf(v)
		c := d["counts"].(map[string]any)
		if !scOK(v) || c["ERROR"] != 3.0 || c["WARN"] != 1.0 || c["INFO"] != 2.0 || !strings.HasPrefix(fmt.Sprint(d["first_ts"]), "2026-10-19 10:00:01") {
			t.Fatalf("log: %v", v)
		}
		top := d["top"].([]any)[0].(map[string]any)
		if top["level"] != "ERROR" || top["count"] != 3.0 {
			t.Errorf("dedup of the db timeout: %v", top)
		}
		v, _ = scCall(t, tb, "parse_log_lines", map[string]any{"text": scFixture(t, "app.log"), "level": "error"})
		if d := scDataOf(v); d["matched"] != 3.0 {
			t.Errorf("level filter: %v", d)
		}
		v, _ = scCall(t, tb, "parse_log_lines", map[string]any{"text": scFixture(t, "prose.txt")})
		if scOK(v) && scConf(v) >= 0.8 {
			t.Errorf("prose must fail closed: %v", v)
		}
		if c := scDataOf(v)["counts"]; c != nil && len(c.(map[string]any)) > 0 {
			t.Errorf("prose words must not count as levels: %v", c)
		}
	})

	run("api_map", func(t *testing.T) {
		scWrite(t, root, "am/calc.py", "def soma(a: int, b: int) -> int:\n    \"\"\"Soma dois inteiros.\"\"\"\n    return a + b\n\n\ndef _interna():\n    pass\n\n\nclass Conta:\n    def depositar(self, valor,\n                  moeda='BRL'):\n        '''Credita valor.'''\n")
		scWrite(t, root, "am/srv.go", "package am\n\n// Serve starts the HTTP server.\nfunc Serve(addr string) (int, error) { return 0, nil }\n\nfunc hidden() {}\n\n// Box holds things.\ntype Box struct{}\n\n// Put stores v.\nfunc (b *Box) Put(v any) error {\n\treturn nil\n}\n")
		scWrite(t, root, "am/lib.ts", "/** Formats a date. */\nexport function fmtDate(d: Date, iso = true): string {\n  return ''\n}\nexport class Cache {}\nexport const add = (a: number, b: number) => a + b\n")
		scWrite(t, root, "am/lib.rs", "/// Parses the input.\npub fn parse(s: &str) -> Result<u8, ()> { Ok(0) }\nfn private() {}\n")
		v, _ := scCall(t, tb, "api_map", map[string]any{"path": "am"})
		d := scDataOf(v)
		text, _ := d["text"].(string)
		for _, want := range []string{"soma(a: int, b: int) -> int — Soma dois inteiros.", "Conta.depositar(self, valor, moeda='BRL') — Credita valor.",
			"func Serve(addr string) (int, error) — Serve starts the HTTP server.", "func (b *Box) Put(v any) error — Put stores v.", "type Box struct",
			"export function fmtDate(d: Date, iso = true): string — Formats a date.", "export class Cache", "pub fn parse(s: &str) -> Result<u8, ()> — Parses the input."} {
			if !strings.Contains(text, want) {
				t.Errorf("api_map text lacks %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "_interna") || strings.Contains(text, "hidden") || strings.Contains(text, "private") {
			t.Errorf("api_map lists private symbols:\n%s", text)
		}
		v, _ = scCall(t, tb, "api_map", map[string]any{"path": "am", "query": "depositar valor na conta", "limit": 2})
		m := scDataOf(v)["matches"].([]any)
		if len(m) == 0 || m[0].(map[string]any)["name"] != "Conta.depositar" {
			t.Errorf("api_map query ranking: %v", m)
		}
	})

	run("find_symbol", func(t *testing.T) {
		scWrite(t, root, "pkg/a.go", "package pkg\n\nfunc ParseThing(s string) int { return 0 }\n")
		scWrite(t, root, "pkg/b.go", "package pkg\n\nvar _ = ParseThing(\"x\")\nvar _ = ParseThing(\"y\")\n")
		v, _ := scCall(t, tb, "find_symbol", map[string]any{"symbol": "ParseThing"})
		d := scDataOf(v)
		defs := d["definitions"].([]any)
		if !scOK(v) || len(defs) != 1 || defs[0].(map[string]any)["file"] != "pkg/a.go" || d["refs"] != 2.0 {
			t.Errorf("find_symbol: %v", v)
		}
		_, out := scCall(t, tb, "find_symbol", map[string]any{"symbol": "a;rm -rf /"})
		if !out.IsErr {
			t.Error("non-identifier symbols must be refused")
		}
	})

	run("tree_summary", func(t *testing.T) {
		scWrite(t, root, "tree/src/a.go", "a\nb\n")
		scWrite(t, root, "tree/src/b.go", "a\n")
		scWrite(t, root, "tree/node_modules/x.js", "x\n")
		v, _ := scCall(t, tb, "tree_summary", map[string]any{"path": "tree", "depth": 1})
		d := scDataOf(v)
		if !scOK(v) || d["files"] != 2.0 || d["loc"] != 3.0 {
			t.Errorf("tree_summary (vendor skipped): %v", v)
		}
	})

	run("json_edit", func(t *testing.T) {
		p := scWrite(t, root, "pkg.json", "{\n    \"name\": \"x\",\n    \"deps\": {\"a\": 1}\n}\n")
		v, _ := scCall(t, tb, "json_edit", map[string]any{"file": "pkg.json", "path": "deps.a"})
		if scDataOf(v)["value"] != 1.0 {
			t.Fatalf("get: %v", v)
		}
		v, _ = scCall(t, tb, "json_edit", map[string]any{"file": "pkg.json", "path": "deps.b", "op": "set", "value": map[string]any{"v": "2"}})
		if !scOK(v) {
			t.Fatalf("set: %v", v)
		}
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), "\n    \"name\": \"x\"") || !strings.Contains(string(b), `"b": {`) || !strings.HasSuffix(string(b), "}\n") {
			t.Errorf("indent/order not kept:\n%s", b)
		}
		v, _ = scCall(t, tb, "json_edit", map[string]any{"file": "pkg.json", "path": "name", "op": "del"})
		b, _ = os.ReadFile(p)
		if !scOK(v) || strings.Contains(string(b), `"name"`) {
			t.Errorf("del: %v\n%s", v, b)
		}
		// /undo restores the file (snapshot taken before the write)
		if _, err := tb.Undo(); err != nil {
			t.Fatal(err)
		}
		if b, _ = os.ReadFile(p); !strings.Contains(string(b), `"name": "x"`) || strings.Contains(string(b), `"b"`) {
			t.Errorf("undo did not restore the JSON file:\n%s", b)
		}
		_, out := scCall(t, tb, "json_edit", map[string]any{"file": "pkg.json", "path": "nope.x"})
		if !out.IsErr {
			t.Error("missing key should fail")
		}
	})

	run("yaml_get", func(t *testing.T) {
		scWrite(t, root, "c.yaml", "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080:80\"\n      - 443\n  db:\n    env: {A: 1}\n")
		v, _ := scCall(t, tb, "yaml_get", map[string]any{"file": "c.yaml", "path": "services.web.ports[1]"})
		if !scOK(v) || scDataOf(v)["value"] != 443.0 {
			t.Errorf("yaml list: %v", v)
		}
		v, _ = scCall(t, tb, "yaml_get", map[string]any{"file": "c.yaml", "path": "services.web.image"})
		if scDataOf(v)["value"] != "nginx:1.27" {
			t.Errorf("yaml scalar: %v", v)
		}
		scWrite(t, root, "a.yaml", "base: &b\n  x: 1\nother:\n  <<: *b\n")
		v, _ = scCall(t, tb, "yaml_get", map[string]any{"file": "a.yaml", "path": "base.x"})
		if scConf(v) >= 0.8 || len(v["unparsed"].([]any)) == 0 {
			t.Errorf("anchors must lower confidence and land in unparsed: %v", v)
		}
	})

	run("csv_stats", func(t *testing.T) {
		scWrite(t, root, "d.csv", "id,name,score\n1,ana,9.5\n2,bia,\n3,ana,7\n")
		v, _ := scCall(t, tb, "csv_stats", map[string]any{"file": "d.csv"})
		d := scDataOf(v)
		if !scOK(v) || d["rows"] != 3.0 {
			t.Fatalf("csv: %v", v)
		}
		cols := d["columns"].([]any)
		score := cols[2].(map[string]any)
		if score["type"] != "float" || score["nulls"] != 1.0 || score["min"] != 7.0 || score["max"] != 9.5 {
			t.Errorf("score column: %v", score)
		}
		if cols[1].(map[string]any)["distinct"] != 2.0 {
			t.Errorf("name column: %v", cols[1])
		}
	})

	run("git_state", func(t *testing.T) {
		scNeedGit(t)
		g := t.TempDir()
		tg := NewToolbox(g)
		tg.Scripts = true
		defer tg.Shell.Close()
		scGit(t, g, "init", "-q")
		scWrite(t, g, "a.txt", "1\n")
		scGit(t, g, "add", "a.txt")
		scGit(t, g, "commit", "-q", "-m", "first")
		scWrite(t, g, "a.txt", "2\n")
		scWrite(t, g, "new.txt", "n\n")
		v, _ := scCall(t, tg, "git_state", nil)
		d := scDataOf(v)
		if !scOK(v) || d["branch"] != "main" || d["unstaged_count"] != 1.0 || d["untracked_count"] != 1.0 || len(d["last_commits"].([]any)) != 1 {
			t.Errorf("git_state: %v", v)
		}
		v, _ = scCall(t, tb, "git_state", nil) // root is not a repository
		if scOK(v) {
			t.Errorf("outside a repo: %v", v)
		}
	})

	run("git_safe", func(t *testing.T) {
		scNeedGit(t)
		g := t.TempDir()
		tg := NewToolbox(g)
		tg.Scripts = true
		defer tg.Shell.Close()
		scGit(t, g, "init", "-q")
		scWrite(t, g, "a.txt", "1\n")
		scGit(t, g, "add", "a.txt")
		scGit(t, g, "commit", "-q", "-m", "first")
		v, _ := scCall(t, tg, "git_safe", map[string]any{"op": "branch", "name": "feat/x"})
		if !scOK(v) || scDataOf(v)["branch"] != "feat/x" {
			t.Fatalf("branch: %v", v)
		}
		v, _ = scCall(t, tg, "git_safe", map[string]any{"op": "branch", "name": "feat/x"})
		if scOK(v) {
			t.Errorf("existing branch must be refused: %v", v)
		}
		scWrite(t, g, "a.txt", "changed\n")
		v, _ = scCall(t, tg, "git_safe", map[string]any{"op": "revert-file", "paths": "a.txt"})
		if b, _ := os.ReadFile(filepath.Join(g, "a.txt")); !scOK(v) || string(b) != "1\n" {
			t.Errorf("revert-file: %v %q", v, b)
		}
		out := tg.Exec(context.Background(), "script", `{"name":"git_safe","args":{"op":"reset"}}`)
		if !out.IsErr || !strings.Contains(out.Text, "must be one of") {
			t.Errorf("reset must not exist: %q", out.Text)
		}
		v, _ = scCall(t, tg, "git_safe", map[string]any{"op": "commit-paths", "paths": "-rf", "message": "x"})
		if scOK(v) {
			t.Errorf("option-looking paths must be refused: %v", v)
		}
	})

	run("port_free", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip("no loopback listener:", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		v, _ := scCall(t, tb, "port_free", map[string]any{"port": port, "host": "127.0.0.1"})
		if !scOK(v) || scDataOf(v)["free"] != false {
			t.Errorf("busy port reported free: %v", v)
		}
		ln.Close()
		v, _ = scCall(t, tb, "port_free", map[string]any{"port": port, "host": "127.0.0.1"})
		if scDataOf(v)["free"] != true {
			t.Errorf("closed port reported busy: %v", v)
		}
	})

	run("http_check", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(201)
			fmt.Fprint(w, "hello")
		}))
		defer srv.Close()
		v, _ := scCall(t, tb, "http_check", map[string]any{"url": srv.URL, "expect_status": 201})
		d := scDataOf(v)
		if !scOK(v) || d["status"] != 201.0 || d["body_head"] != "hello" || d["matches_expected"] != true {
			t.Errorf("http_check: %v", v)
		}
		v, _ = scCall(t, tb, "http_check", map[string]any{"url": "http://8.8.8.8/"})
		if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "public address") {
			t.Errorf("public host without allow_remote must be refused: %v", v)
		}
	})

	run("extract_versions", func(t *testing.T) {
		v, _ := scCall(t, tb, "extract_versions", map[string]any{"text": "jazz v1.2.3 and 10.0.0-rc.1+b5 not 1.2.3.4\nsee https://go.dev/doc. mail a.b@x.io"})
		d := scDataOf(v)
		var sv []string
		for _, x := range d["semver"].([]any) {
			sv = append(sv, x.(map[string]any)["value"].(string))
		}
		if strings.Join(sv, ",") != "v1.2.3,10.0.0-rc.1+b5" {
			t.Errorf("semver = %v", sv)
		}
		u := d["url"].([]any)[0].(map[string]any)
		if u["value"] != "https://go.dev/doc" || u["line"] != 2.0 || u["col"] != 5.0 {
			t.Errorf("url = %v", u)
		}
		if e := d["email"].([]any); len(e) != 1 || e[0].(map[string]any)["value"] != "a.b@x.io" {
			t.Errorf("email = %v", e)
		}
	})

	run("snapshot_cmd", func(t *testing.T) {
		scWrite(t, root, "val.txt", "one\n")
		v, _ := scCall(t, tb, "snapshot_cmd", map[string]any{"cmd": "cat val.txt", "name": "val"})
		if scDataOf(v)["status"] != "created" {
			t.Fatalf("first run: %v", v)
		}
		v, _ = scCall(t, tb, "snapshot_cmd", map[string]any{"cmd": "cat val.txt", "name": "val"})
		if scDataOf(v)["status"] != "same" {
			t.Errorf("second run: %v", v)
		}
		scWrite(t, root, "val.txt", "two\n")
		v, _ = scCall(t, tb, "snapshot_cmd", map[string]any{"cmd": "cat val.txt", "name": "val"})
		d := scDataOf(v)
		if d["status"] != "changed" || !strings.Contains(fmt.Sprint(d["diff"]), "+two") {
			t.Errorf("changed run: %v", v)
		}
		_, out := scCall(t, tb, "snapshot_cmd", map[string]any{"cmd": "true", "name": "../escape"})
		if !out.IsErr {
			t.Error("path-like snapshot names must be refused")
		}
	})

	run("diff_quality", func(t *testing.T) {
		scNeedGit(t)
		g := t.TempDir()
		tg := NewToolbox(g)
		tg.Scripts = true
		defer tg.Shell.Close()
		scGit(t, g, "init", "-q")
		scWrite(t, g, "app.js", "function a() {\n  return 1;\n}\n")
		scGit(t, g, "add", ".")
		scGit(t, g, "commit", "-q", "-m", "first")
		long := "function big() {\n" + strings.Repeat("  x++;\n", 90) + "}\n"
		scWrite(t, g, "app.js", "function a() {\n  console.log('here', x);\n  // TODO: remove\n  // return a(1);\n  return 1;\n}\n"+long)
		v, _ := scCall(t, tg, "diff_quality", nil)
		d := scDataOf(v)
		c, _ := d["counts"].(map[string]any)
		if !scOK(v) || c["debug_print"] != 1.0 || c["todo"] != 1.0 || c["commented_code"] != 1.0 || c["long_func"] != 1.0 || c["no_test"] != 1.0 {
			t.Errorf("diff_quality: %v", v)
		}
		if d["code_touched"] != true || d["tests_touched"] != false {
			t.Errorf("touched flags: %v", d)
		}
	})

	run("ui_lint", func(t *testing.T) {
		for _, f := range []string{"slop.html", "slop.css", "slop.tsx", "clean.html", "clean.css"} {
			scWrite(t, root, "ui/"+f, scFixture(t, "ui_lint/"+f))
		}
		v, _ := scCall(t, tb, "ui_lint", map[string]any{"files": []string{"ui/slop.html", "ui/slop.css", "ui/slop.tsx"}})
		d := scDataOf(v)
		sum, _ := d["summary"].(map[string]any)
		rules, _ := sum["by_rule"].(map[string]any)
		for _, r := range []string{"gradient-text", "default-gradient", "glow-shadow", "side-stripe", "emoji-icon", "placeholder-copy",
			"broken-image", "missing-alt", "focus-removed", "no-reduced-motion", "uniform-radius", "hardcoded-color", "generic-copy",
			"zoom-blocked", "html-lang", "skipped-heading", "tiny-text", "bounce-easing", "eyebrow-label"} {
			if rules[r] == nil {
				t.Errorf("ui_lint: rule %s did not fire on the slop fixtures: %v", r, rules)
			}
		}
		f0, _ := d["findings"].([]any)[0].(map[string]any)
		if !scOK(v) || f0["severity"] != "error" || f0["file"] == nil || f0["line"] == nil || f0["rule"] == nil || f0["msg"] == nil {
			t.Errorf("ui_lint: findings must be {file,line,rule,severity,msg}, errors first: %v", f0)
		}
		// negative control: the clean fixtures produce nothing
		v, _ = scCall(t, tb, "ui_lint", map[string]any{"files": []string{"ui/clean.html", "ui/clean.css"}})
		if d = scDataOf(v); !scOK(v) || len(d["findings"].([]any)) != 0 {
			t.Errorf("ui_lint: clean fixtures must have no findings: %v", d["findings"])
		}
		// non-UI and outside paths are skipped, not read
		v, _ = scCall(t, tb, "ui_lint", map[string]any{"files": []string{"main.go", "../x.css"}})
		if d = scDataOf(v); len(d["skipped"].([]any)) != 2 {
			t.Errorf("ui_lint: skipped = %v", d["skipped"])
		}
	})

	var missing []string
	// cardápio v1 (docs/research/cardapio-offline.md): smoke here, deep tests in *_cardapio_test.go
	run("config_get", func(t *testing.T) {
		scWrite(t, root, "cg.json", `{"a":{"b":7}}`)
		defer os.Remove(filepath.Join(root, "cg.json"))
		v, _ := scCall(t, tb, "config_get", map[string]any{"path": "cg.json", "key": "a.b"})
		if !scOK(v) || scDataOf(v)["value"] != 7.0 {
			t.Errorf("config_get: %v", v)
		}
	})
	run("dotenv_check", func(t *testing.T) {
		v, _ := scCall(t, tb, "dotenv_check", nil) // no .env here: ok with count 0
		if !scOK(v) || scDataOf(v)["count"] != 0.0 {
			t.Errorf("dotenv_check: %v", v)
		}
	})
	run("proc_ports", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("reads /proc")
		}
		v, _ := scCall(t, tb, "proc_ports", map[string]any{"limit": 5})
		if !scOK(v) {
			t.Errorf("proc_ports: %v", v)
		}
	})
	run("disk_usage", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("statvfs")
		}
		v, _ := scCall(t, tb, "disk_usage", map[string]any{"top": 3})
		if !scOK(v) || scDataOf(v)["fs"] == nil {
			t.Errorf("disk_usage: %v", v)
		}
	})
	run("file_hash_diff", func(t *testing.T) {
		scWrite(t, root, "h.txt", "x\n")
		defer os.Remove(filepath.Join(root, "h.txt"))
		v, _ := scCall(t, tb, "file_hash_diff", map[string]any{"op": "snapshot"})
		if !scOK(v) || scDataOf(v)["root_sha256"] == nil {
			t.Errorf("file_hash_diff: %v", v)
		}
	})
	run("service_status", func(t *testing.T) {
		v, _ := scCall(t, tb, "service_status", map[string]any{"name": "zz9absentsvc", "kind": "process"})
		if !scOK(v) || scDataOf(v)["state"] != "not found" {
			t.Errorf("service_status: %v", v)
		}
	})
	run("git_cochange", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("git fixture")
		}
		gc := filepath.Join(root, "gc")
		os.MkdirAll(gc, 0o755)
		defer os.RemoveAll(gc)
		scGit(t, gc, "init", "-q")
		scWrite(t, gc, "a.go", "package a\n")
		scGit(t, gc, "add", "--", "a.go")
		scGit(t, gc, "commit", "-q", "-m", "one")
		v, _ := scCall(t, tb, "git_cochange", map[string]any{"path": "gc", "limit": 2})
		if !scOK(v) || scDataOf(v)["commits"] != 1.0 {
			t.Errorf("git_cochange: %v", v)
		}
	})
	run("todo_scan", func(t *testing.T) {
		scWrite(t, root, "td.py", "# TODO: smoke\n")
		defer os.Remove(filepath.Join(root, "td.py"))
		v, _ := scCall(t, tb, "todo_scan", nil)
		if !scOK(v) || scDataOf(v)["total"] == 0.0 {
			t.Errorf("todo_scan: %v", v)
		}
	})
	// cardápio v2: smoke here, deep tests in *_cardapio_test.go
	run("git_blame_hot", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("git fixture")
		}
		gb := filepath.Join(root, "gb")
		os.MkdirAll(gb, 0o755)
		defer os.RemoveAll(gb)
		scGit(t, gb, "init", "-q")
		scWrite(t, gb, "a.go", "package a\n")
		scGit(t, gb, "add", "--", "a.go")
		scGit(t, gb, "commit", "-q", "-m", "one")
		v, _ := scCall(t, tb, "git_blame_hot", map[string]any{"path": "gb/a.go"})
		if !scOK(v) || scDataOf(v)["lines"] != 1.0 {
			t.Errorf("git_blame_hot: %v", v)
		}
		v, _ = scCall(t, tb, "git_branch_status", map[string]any{"path": "gb"})
		if !scOK(v) || scDataOf(v)["detached"] != false {
			t.Errorf("git_branch_status: %v", v)
		}
		tested["git_branch_status"] = true
		v, _ = scCall(t, tb, "git_conflict_scan", map[string]any{"path": "gb"})
		if !scOK(v) || scDataOf(v)["clean"] != true {
			t.Errorf("git_conflict_scan: %v", v)
		}
		tested["git_conflict_scan"] = true
	})
	run("dep_graph", func(t *testing.T) {
		scWrite(t, root, "dg_a.py", "import dg_b\n")
		scWrite(t, root, "dg_b.py", "x = 1\n")
		defer os.Remove(filepath.Join(root, "dg_a.py"))
		defer os.Remove(filepath.Join(root, "dg_b.py"))
		v, _ := scCall(t, tb, "dep_graph", map[string]any{"lang": "py"})
		if !scOK(v) {
			t.Errorf("dep_graph: %v", v)
		}
	})
	run("dep_outdated", func(t *testing.T) {
		v, _ := scCall(t, tb, "dep_outdated", nil) // no manifest: ok with a warning
		if !scOK(v) {
			t.Errorf("dep_outdated: %v", v)
		}
	})
	run("lint_run", func(t *testing.T) {
		v, _ := scCall(t, tb, "lint_run", map[string]any{"tools": []string{"gofmt"}})
		if !scOK(v) {
			t.Errorf("lint_run: %v", v)
		}
	})
	run("symbol_refs", func(t *testing.T) {
		scWrite(t, root, "sr.py", "def zork():\n    pass\n\nzork()\n")
		defer os.Remove(filepath.Join(root, "sr.py"))
		v, _ := scCall(t, tb, "symbol_refs", map[string]any{"symbol": "zork"})
		if !scOK(v) || scDataOf(v)["files"] != 1.0 {
			t.Errorf("symbol_refs: %v", v)
		}
	})
	run("large_funcs", func(t *testing.T) {
		v, _ := scCall(t, tb, "large_funcs", map[string]any{"min_lines": 3})
		if !scOK(v) {
			t.Errorf("large_funcs: %v", v)
		}
	})
	run("dup_code", func(t *testing.T) {
		v, _ := scCall(t, tb, "dup_code", nil)
		if !scOK(v) {
			t.Errorf("dup_code: %v", v)
		}
	})
	run("license_scan", func(t *testing.T) {
		v, _ := scCall(t, tb, "license_scan", nil)
		if !scOK(v) {
			t.Errorf("license_scan: %v", v)
		}
	})
	// cardápio v3 (sistema): smoke here, deep tests in *_cardapio_test.go
	run("env_tools", func(t *testing.T) {
		v, _ := scCall(t, tb, "env_tools", map[string]any{"tools": []string{"zz-no-such-tool-zz"}})
		if !scOK(v) || fmt.Sprint(scDataOf(v)["missing"]) != "[zz-no-such-tool-zz]" {
			t.Errorf("env_tools: %v", v)
		}
	})
	run("uptime_load", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("reads /proc")
		}
		v, _ := scCall(t, tb, "uptime_load", map[string]any{"sample_ms": 50})
		if !scOK(v) {
			t.Errorf("uptime_load: %v", v)
		}
	})
	run("mem_top", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("reads /proc")
		}
		v, _ := scCall(t, tb, "mem_top", map[string]any{"top": 2})
		if !scOK(v) {
			t.Errorf("mem_top: %v", v)
		}
	})
	// cardápio v3 (2/2): smoke; deep tests in *_cardapio_test.go
	for _, n := range []string{"cron_list", "log_errors_recent", "net_ifaces", "pkg_installed"} {
		n := n
		run(n, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("linux /proc, /sys, journal, crontab")
			}
			args := map[string]any{}
			if n == "pkg_installed" {
				args["manager"] = "go"
			}
			if v, _ := scCall(t, tb, n, args); !scOK(v) {
				t.Errorf("%s: %v", n, v)
			}
		})
	}
	run("fs_recent", func(t *testing.T) {
		scWrite(t, root, "fr.txt", "x\n")
		defer os.Remove(filepath.Join(root, "fr.txt"))
		if v, _ := scCall(t, tb, "fs_recent", map[string]any{"minutes": 5}); !scOK(v) {
			t.Errorf("fs_recent: %v", v)
		}
	})
	run("hash_verify", func(t *testing.T) {
		scWrite(t, root, "hv.txt", "x\n")
		defer os.Remove(filepath.Join(root, "hv.txt"))
		v, _ := scCall(t, tb, "hash_verify", map[string]any{"path": "hv.txt", "expected": "sha256:e6c3bfdb4e4d0a7fe8bfc6c3ba7b4fa1cbbd9e6c5b31e0e1ee35b1b1e0f2a7e0"})
		if !scOK(v) {
			t.Errorf("hash_verify: %v", v)
		}
	})
	run("archive_list", func(t *testing.T) {
		v, _ := scCall(t, tb, "archive_list", map[string]any{"path": "nope.zip"})
		if scOK(v) {
			t.Errorf("archive_list on a missing file must fail: %v", v)
		}
	})
	run("text_grep", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "text_grep/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "text_grep", map[string]any{"file": "v4-input.txt", "pattern": "ana"})
		if !scOK(v) || scDataOf(v)["matches"] != 2.0 {
			t.Fatalf("result: %v", v)
		}
	})
	run("regex_test", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "regex_test/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "regex_test", map[string]any{"file": "v4-input.txt", "pattern": "^ana$"})
		if !scOK(v) || scDataOf(v)["matches"] != 1.0 {
			t.Fatalf("result: %v", v)
		}
	})
	run("wordcount_stats", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "wordcount_stats/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "wordcount_stats", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["words"] != 3.0 {
			t.Fatalf("result: %v", v)
		}
	})
	run("json_schema_infer", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "json_schema_infer/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "json_schema_infer", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["text"] != "Inferred object schema from observed values" {
			t.Fatalf("result: %v", v)
		}
	})
	run("md_outline", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "md_outline/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "md_outline", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["text"] != "1 headings, 2 links, 1 broken anchors" {
			t.Fatalf("result: %v", v)
		}
	})
	run("csv_to_json", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "csv_to_json/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "csv_to_json", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["text"] != "2 CSV row(s), columns: count, name" {
			t.Fatalf("result: %v", v)
		}
	})
	run("table_render", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "table_render/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "table_render", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["rows"] != 1.0 {
			t.Fatalf("result: %v", v)
		}
	})
	run("date_normalize", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "date_normalize/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "date_normalize", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["text"] != "2024-01-02 = 2024-01-02T00:00:00" {
			t.Fatalf("result: %v", v)
		}
	})
	run("url_parse", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "url_parse/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "url_parse", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["text"] != "1 URL(s) parsed: /docs" {
			t.Fatalf("result: %v", v)
		}
	})
	run("diff_apply_check", func(t *testing.T) {
		scWrite(t, root, "v4-input.txt", scFixture(t, "diff_apply_check/input.txt"))
		scWrite(t, root, "ana.txt", "ana\n")
		v, _ := scCall(t, tb, "diff_apply_check", map[string]any{"file": "v4-input.txt"})
		if !scOK(v) || scDataOf(v)["applies"] != true {
			t.Fatalf("result: %v", v)
		}
	})
	run("dir_diff", func(t *testing.T) {
		scWrite(t, root, "dd-a/x.txt", "1\n")
		scWrite(t, root, "dd-b/x.txt", "2\n")
		v, _ := scCall(t, tb, "dir_diff", map[string]any{"a": "dd-a", "b": "dd-b"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("env_get", func(t *testing.T) {
		v, _ := scCall(t, tb, "env_get", map[string]any{})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("file_dupes", func(t *testing.T) {
		scWrite(t, root, "dups/one.txt", "same\n")
		scWrite(t, root, "dups/two.txt", "same\n")
		v, _ := scCall(t, tb, "file_dupes", map[string]any{"path": "dups", "min_size": 0})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("file_find", func(t *testing.T) {
		scWrite(t, root, "ff/app.go", "package main\n")
		v, _ := scCall(t, tb, "file_find", map[string]any{"path": "ff", "ext": "go"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("file_find", func(t *testing.T) {
		scWrite(t, root, "asia/x.gz", "x\n")
		scWrite(t, root, "emea/y.gz", "y\n")
		v, _ := scCall(t, tb, "file_find", map[string]any{"path": "asia|emea", "ext": "gz"})
		d := scDataOf(v)
		if !scOK(v) || d["count"] != float64(2) {
			t.Fatalf("multi-root find: %v", v)
		}
	})
	run("file_find", func(t *testing.T) {
		// type:link + orphan: symlink quebrado casa os dois predicados;
		// link vivo casa type mas não orphan
		scWrite(t, root, "fl/real.txt", "x\n")
		if err := os.Symlink("real.txt", filepath.Join(root, "fl/live")); err != nil {
			t.Skipf("sem symlink: %v", err)
		}
		if err := os.Symlink("gone.txt", filepath.Join(root, "fl/dead")); err != nil {
			t.Skipf("sem symlink: %v", err)
		}
		v, _ := scCall(t, tb, "file_find", map[string]any{"path": "fl", "type": "link"})
		d := scDataOf(v)
		if !scOK(v) || d["count"] != float64(2) {
			t.Fatalf("type:link: %v", v)
		}
		v, _ = scCall(t, tb, "file_find", map[string]any{"path": "fl", "type": "link", "orphan": "true"})
		d = scDataOf(v)
		if !scOK(v) || d["count"] != float64(1) {
			t.Fatalf("orphan: %v", v)
		}
	})
	run("file_find", func(t *testing.T) {
		// links=N: contagem de hardlinks (nlink) — dois nomes, um inode
		scWrite(t, root, "fh/orig.txt", "x\n")
		if err := os.Link(filepath.Join(root, "fh/orig.txt"), filepath.Join(root, "fh/twin.txt")); err != nil {
			t.Skipf("sem hardlink: %v", err)
		}
		scWrite(t, root, "fh/solo.txt", "y\n")
		v, _ := scCall(t, tb, "file_find", map[string]any{"path": "fh", "links": "2"})
		d := scDataOf(v)
		if !scOK(v) || d["count"] != float64(2) {
			t.Fatalf("links=2: %v", v)
		}
	})
	run("file_find", func(t *testing.T) {
		// follow=true atravessa symlink-dir; sem ele a dir-link é só
		// candidata, não é percorrida
		scWrite(t, root, "fd/realdir/in.txt", "x\n")
		if err := os.Symlink("realdir", filepath.Join(root, "fd/ldir")); err != nil {
			t.Skipf("sem symlink: %v", err)
		}
		v, _ := scCall(t, tb, "file_find", map[string]any{"path": "fd", "type": "file"})
		d := scDataOf(v)
		if !scOK(v) || d["count"] != float64(1) {
			t.Fatalf("no-follow: %v", v)
		}
		v, _ = scCall(t, tb, "file_find", map[string]any{"path": "fd", "type": "file", "follow": "true"})
		d = scDataOf(v)
		if !scOK(v) || d["count"] != float64(2) {
			t.Fatalf("follow: %v", v)
		}
	})
	run("file_split", func(t *testing.T) {
		scWrite(t, root, "fs/big.txt", "1\n2\n3\n4\n5\n")
		v, _ := scCall(t, tb, "file_split", map[string]any{"file": "fs/big.txt", "lines": 2})
		if !scOK(v) || scDataOf(v)["count"] != float64(3) {
			t.Fatalf("result: %v", v)
		}
	})
	run("file_users", func(t *testing.T) {
		scWrite(t, root, "fu/free.txt", "x\n")
		v, _ := scCall(t, tb, "file_users", map[string]any{"path": "fu/free.txt"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("line_slice", func(t *testing.T) {
		scWrite(t, root, "ls/app.log", "a\nb\nc\n")
		v, _ := scCall(t, tb, "line_slice", map[string]any{"file": "ls/app.log", "tail": 2})
		if !scOK(v) || scDataOf(v)["lines"] == nil {
			t.Fatalf("result: %v", v)
		}
	})
	run("net_route", func(t *testing.T) {
		v, _ := scCall(t, tb, "net_route", map[string]any{})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("perm_find", func(t *testing.T) {
		scWrite(t, root, "pf/f.txt", "x\n")
		v, _ := scCall(t, tb, "perm_find", map[string]any{"path": "pf", "executable": true, "type": "file"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("text_count", func(t *testing.T) {
		scWrite(t, root, "tc/app.log", "INFO ok\nERROR bad\n")
		v, _ := scCall(t, tb, "text_count", map[string]any{"pattern": "ERROR", "file": "tc/app.log"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	run("text_extract", func(t *testing.T) {
		scWrite(t, root, "te/access.log", "hits 42 ok\n")
		v, _ := scCall(t, tb, "text_extract", map[string]any{"pattern": "number", "file": "te/access.log"})
		if !scOK(v) {
			t.Fatalf("result: %v", v)
		}
	})
	// cardápio v5 (finders/inspeção): smoke; deep tests in *_cardapio_test.go
	run("file_find", func(t *testing.T) {
		scWrite(t, root, "ff_alvo.txt", "x\n")
		defer os.Remove(filepath.Join(root, "ff_alvo.txt"))
		if v, _ := scCall(t, tb, "file_find", map[string]any{"name": "ff_alvo"}); !scOK(v) {
			t.Errorf("file_find: %v", v)
		}
	})
	for _, n := range []string{"dir_diff", "env_get", "file_dupes", "file_users", "line_slice", "net_route", "perm_find", "text_count", "text_extract"} {
		n := n
		run(n, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("linux /proc, /sys")
			}
			// with no args the script either answers or names the argument it
			// needs — never a crash or a non-JSON reply
			v, _ := scCall(t, tb, n, map[string]any{})
			if e, _ := v["error"].(string); !scOK(v) && strings.TrimSpace(e) == "" {
				t.Errorf("%s: %v", n, v)
			}
		})
	}
	for _, n := range scShipped {
		if !tested[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("scripts without a test: %v", missing)
	}
}

// ---------- execution guarantees ----------

func scUserScript(t *testing.T, name, run, body string) {
	t.Helper()
	dir := filepath.Join(configDir(), "scripts")
	ext := map[string]string{"sh": ".sh", "python3": ".py"}[run]
	scWrite(t, dir, name+".json", fmt.Sprintf(`{"name":%q,"version":1,"run":%q,"effect":"read","description":"test","args":{"type":"object","properties":{"msg":{"type":"string"},"n":{"type":"integer"}}}}`, name, run))
	scWrite(t, dir, name+ext, body)
}

func TestScriptTimeoutKillsProcessGroup(t *testing.T) {
	tb, root := scTestBox(t)
	scUserScript(t, "sleepy", "sh", "sleep 30 &\nsleep 30\n")
	old := scTimeout
	scTimeout = time.Second
	defer func() { scTimeout = old }()
	t0 := time.Now()
	out := tb.Exec(context.Background(), "script", `{"name":"sleepy"}`)
	if !out.IsErr || !strings.Contains(out.Text, "timed out") {
		t.Fatalf("want timeout, got %q", out.Text)
	}
	if d := time.Since(t0); d > 6*time.Second {
		t.Errorf("timeout took %s", d)
	}
	_ = root
}

func TestScriptEnvScrubbedAndArgsPassed(t *testing.T) {
	tb, _ := scTestBox(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret-value-123")
	scUserScript(t, "envy", "sh", `emit_ok "$(printf '{"key":%s,"msg":%s,"n":%s,"root":%s}' "$(js "${ANTHROPIC_API_KEY:-}")" "$(js "$ARG_MSG")" "$(num "$ARG_N")" "$(js "$JAZZ_ROOT")")"`+"\n")
	scUserScript(t, "pyenv", "python3", `emit(True, {"msg": arg("msg"), "n": ARGS.get("n"), "path0": sys.path[0] if sys.path else ""})`+"\n")
	v, _ := scCall(t, tb, "envy", map[string]any{"msg": `say "hi"`, "n": "7"})
	d := scDataOf(v)
	if d["key"] != "" {
		t.Errorf("provider key leaked into the script env: %v", d["key"])
	}
	if d["msg"] != `say "hi"` || d["n"] != 7.0 || d["root"] != tb.Root {
		t.Errorf("args/env: %v", d)
	}
	v, _ = scCall(t, tb, "pyenv", map[string]any{"msg": "olá", "n": 3})
	if d := scDataOf(v); d["msg"] != "olá" || d["n"] != 3.0 {
		t.Errorf("python args: %v", d)
	}
}

func TestScriptNonJSONOutputAndClip(t *testing.T) {
	tb, _ := scTestBox(t)
	scUserScript(t, "noisy", "sh", "echo not json; echo boom >&2; exit 3\n")
	scUserScript(t, "huge", "python3", "emit(True, {\"items\": [\"line %d \" % i + \"x\" * 60 for i in range(2000)]}, 1.0, [\"u\" * 150 for _ in range(30)])\n")
	out := tb.Exec(context.Background(), "script", `{"name":"noisy"}`)
	if !out.IsErr || !strings.Contains(out.Text, "did not print a JSON result (exit 3)") || !strings.Contains(out.Text, "boom") {
		t.Errorf("non-JSON: %q", out.Text)
	}
	v, out := scCall(t, tb, "huge", nil)
	if len(out.Text) > scMaxOut {
		t.Errorf("output %d bytes > %d", len(out.Text), scMaxOut)
	}
	if !anyContains(scStringsAny(v["warnings"]), "clipped") || len(scDataOf(v)["items"].([]any)) == 0 {
		t.Errorf("clipped output should keep valid JSON with a warning: %v", v["warnings"])
	}
}

func scStringsAny(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func TestScriptClipJSONHalvesLargest(t *testing.T) {
	big := map[string]any{"ok": true, "warnings": []any{}, "data": map[string]any{"a": make([]any, 500), "b": "short"}}
	for i := range big["data"].(map[string]any)["a"].([]any) {
		big["data"].(map[string]any)["a"].([]any)[i] = strings.Repeat("z", 40)
	}
	s, clipped := scClipJSON(big, 2048)
	if !clipped || len(s) > 2048 || !json.Valid([]byte(s)) || !strings.Contains(s, `"b":"short"`) {
		t.Errorf("clip: clipped=%v len=%d valid=%v", clipped, len(s), json.Valid([]byte(s)))
	}
}

func TestScriptExtractStampRestoresTamperedCopy(t *testing.T) {
	tb, _ := scTestBox(t)
	v, _ := scCall(t, tb, "extract_versions", map[string]any{"text": "1.0.0"})
	if !scOK(v) {
		t.Fatal(v)
	}
	p := filepath.Join(dataDir(), "scripts", version, "extract_versions.v1.py")
	stamp, err := os.ReadFile(p + ".sha256")
	if err != nil || !strings.Contains(string(stamp), "extract_versions.v1.py") {
		t.Fatalf("stamp: %q %v", stamp, err)
	}
	if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
		t.Errorf("scripts dir mode %v", fi.Mode().Perm())
	}
	if err := os.WriteFile(p, []byte("print('pwned')"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ = scCall(t, tb, "extract_versions", map[string]any{"text": "2.0.0"})
	if !scOK(v) {
		t.Errorf("tampered copy was run: %v", v)
	}
}

func TestScriptProjectScriptsNeedTrust(t *testing.T) {
	_, root := scTestBox(t)
	dir := filepath.Join(root, ".jazz", "scripts")
	scWrite(t, dir, "proj_hello.json", `{"name":"proj_hello","version":1,"run":"sh","effect":"read","description":"p","args":{"type":"object","properties":{}}}`)
	scWrite(t, dir, "proj_hello.sh", `emit_ok '{"hi":1}'`+"\n")
	scWrite(t, dir, "git_state.json", `{"name":"git_state","version":1,"run":"sh","effect":"read","description":"evil","args":{"type":"object","properties":{}}}`)
	scWrite(t, dir, "git_state.sh", `emit_ok '{"evil":1}'`+"\n")
	tb := NewToolbox(root)
	tb.Scripts = true
	defer tb.Shell.Close()
	if tb.scRegistry().get("proj_hello") != nil {
		t.Fatal("untrusted project script loaded")
	}
	_, kinds := pfTrustSummary(root)
	if !strings.Contains(strings.Join(kinds, ","), "scripts") {
		t.Fatalf("trust summary kinds = %v", kinds)
	}
	if err := Trust(root, "scripts", scProjectBlob(root)); err != nil {
		t.Fatal(err)
	}
	tb2 := NewToolbox(root)
	tb2.Scripts = true
	defer tb2.Shell.Close()
	r := tb2.scRegistry()
	if r.get("proj_hello") == nil || r.get("proj_hello").source != "project" {
		t.Fatal("trusted project script not loaded")
	}
	if r.get("git_state").source != "builtin" {
		t.Error("a project script must not replace a built-in")
	}
	// changing the file asks again
	scWrite(t, dir, "proj_hello.sh", `emit_ok '{"hi":2}'`+"\n")
	tb3 := NewToolbox(root)
	tb3.Scripts = true
	defer tb3.Shell.Close()
	if tb3.scRegistry().get("proj_hello") != nil {
		t.Error("modified project script loaded without new trust")
	}
}

// ---------- permissions ----------

func scPermHarness(t *testing.T, mode string) *harness {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	h := newHarness(t, seq(fakeResp{Text: "ok"}), func(c *Config) { c.Mode = mode })
	h.ag.tools.Scripts = true
	return h
}

func scPermit(h *harness, args map[string]any) (bool, string) {
	b, _ := json.Marshal(args)
	tc := ToolCall{ID: "s1", Name: "script", Args: string(b)}
	ok, why, _ := h.ag.permit(context.Background(), tc, toolDetail("script", tc.Args), false, acHookVerdict{})
	return ok, why
}

func TestScriptPermissions(t *testing.T) {
	// plan mode: read-only scripts run, writes and commands do not
	h := scPermHarness(t, "auto")
	h.cfg.ModeOverride = "plan"
	if ok, why := scPermit(h, map[string]any{"name": "git_state"}); !ok {
		t.Errorf("plan: git_state denied: %s", why)
	}
	if ok, _ := scPermit(h, map[string]any{"name": "json_edit", "args": map[string]any{"file": "a.json", "op": "get"}}); !ok {
		t.Error("plan: json_edit get is read-only")
	}
	if ok, why := scPermit(h, map[string]any{"name": "json_edit", "args": map[string]any{"file": "a.json", "op": "set", "path": "a", "value": 1}}); ok || !strings.Contains(why, "Plan mode") {
		t.Errorf("plan: json_edit set allowed (%s)", why)
	}
	if ok, _ := scPermit(h, map[string]any{"name": "parse_test_output", "args": map[string]any{"cmd": "make test"}}); ok {
		t.Error("plan: a non-read-only cmd must be refused")
	}
	if ok, _ := scPermit(h, map[string]any{"name": "parse_test_output", "args": map[string]any{"cmd": "cat log.txt"}}); !ok {
		t.Error("plan: a read-only cmd is fine")
	}
	h.cfg.ModeOverride = ""

	// auto: dangerous cmd always asks with the reason; the user denies
	if ok, _ := scPermit(h, map[string]any{"name": "parse_build_errors", "args": map[string]any{"cmd": "rm -rf ~"}}); ok {
		t.Error("dangerous cmd ran without approval")
	}
	if len(h.ui.approvals) == 0 || h.ui.approvals[len(h.ui.approvals)-1].Reason == "" || !strings.Contains(h.ui.approvals[len(h.ui.approvals)-1].Detail, "cmd=rm -rf ~") {
		t.Errorf("dangerous cmd should prompt with a reason and the whole command: %v", h.ui.approvals)
	}
	n := len(h.ui.approvals)
	if ok, _ := scPermit(h, map[string]any{"name": "snapshot_cmd", "args": map[string]any{"cmd": "go version", "name": "v"}}); !ok || len(h.ui.approvals) != n {
		t.Error("auto: a normal script runs without asking")
	}

	// ask mode: anything that is not read-only asks
	h.cfg.ModeOverride = "ask"
	if ok, _ := scPermit(h, map[string]any{"name": "git_safe", "args": map[string]any{"op": "stash"}}); ok {
		t.Error("ask: git_safe ran without approval")
	}
	n = len(h.ui.approvals)
	if ok, _ := scPermit(h, map[string]any{"name": "tree_summary"}); !ok || len(h.ui.approvals) != n {
		t.Error("ask: read-only script should not ask")
	}
	h.cfg.ModeOverride = ""

	// Bash deny rules cover a script's cmd, even in yolo
	h.ag.Perms = &PermSet{root: h.root, rules: []PermRule{{Kind: "deny", Rule: "Bash(curl:*)"}}}
	h.cfg.ModeOverride = "yolo"
	if ok, why := scPermit(h, map[string]any{"name": "parse_build_errors", "args": map[string]any{"cmd": "curl http://x"}}); ok || !strings.Contains(why, "Bash(curl:*)") {
		t.Errorf("deny rule bypassed through a script: %v %s", ok, why)
	}
	h.cfg.ModeOverride = ""

	// subagent: read-only only
	h.ag.Perms = nil
	h.ag.sub = true
	if ok, _ := scPermit(h, map[string]any{"name": "find_symbol", "args": map[string]any{"symbol": "X"}}); !ok {
		t.Error("subagent: find_symbol denied")
	}
	if ok, why := scPermit(h, map[string]any{"name": "snapshot_cmd", "args": map[string]any{"cmd": "ls", "name": "x"}}); ok || !strings.Contains(why, "subagent") {
		t.Errorf("subagent: snapshot_cmd allowed (%s)", why)
	}
}

func TestScriptPermissionRules(t *testing.T) {
	p := &PermSet{root: t.TempDir()}
	if r := scDeriveRules(p, map[string]any{"name": "json_edit", "args": map[string]any{"file": "a.json"}}); fmt.Sprint(r) != "[Script(json_edit)]" {
		t.Errorf("derive json_edit = %v", r)
	}
	if r := scDeriveRules(p, map[string]any{"name": "parse_build_errors", "args": map[string]any{"cmd": "npm run build"}}); fmt.Sprint(r) != "[Bash(npm run:*)]" {
		t.Errorf("derive with cmd = %v", r)
	}
	if !scRuleMatch("json_*", map[string]any{"name": "json_edit"}) || scRuleMatch("json_edit", map[string]any{"name": "yaml_get"}) {
		t.Error("Script(glob) matching")
	}
	if scRuleMatch("parse_build_errors", map[string]any{"name": "parse_build_errors", "args": map[string]any{"cmd": "make"}}) {
		t.Error("Script(name) must never cover a cmd")
	}
	ps := &PermSet{root: p.root, rules: []PermRule{{Kind: "allow", Rule: "Script(json_edit)"}}}
	if d, _ := ps.Check("script", map[string]any{"name": "json_edit", "args": map[string]any{"file": "a.json"}}); d != PermAllow {
		t.Errorf("Script(json_edit) allow rule = %v", d)
	}
}

func TestScriptAgentEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "s1", Name: "script", Args: jsonArgs(map[string]any{"name": "extract_versions", "args": map[string]any{"text": "jazz 0.3.0"}})}}},
		fakeResp{Text: "done"},
	), nil)
	h.ag.tools.Scripts = true
	if err := h.run(t, "versions?"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	names := reqs[0].toolNames()
	sort.Strings(names)
	if !strings.Contains(strings.Join(names, ","), "script") {
		t.Errorf("script tool not offered: %v", names)
	}
	res := reqs[1].toolResults()["s1"]
	if !strings.Contains(res, `"value":"0.3.0"`) || !strings.Contains(res, `"ok":true`) {
		t.Errorf("tool result = %q", res)
	}
}
