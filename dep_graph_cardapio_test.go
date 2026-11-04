//go:build !windows

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These two scripts belong to the offline "cardápio" (v2, code/build): dep_graph builds
// the import graph of a project by line regex, dep_outdated compares manifests with
// their lockfiles without touching the network. Both run through the same script
// path as json_edit and port_free (see scripts_test.go for scTestBox / scCall / scFixture).

// scFixtureTree copies jazz-scripts/testdata/<name> into root (node_modules and the
// like included: the script must skip them on its own).
func scFixtureTree(t *testing.T, root, name string) {
	t.Helper()
	src := filepath.Join("jazz-scripts", "testdata", name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		scWrite(t, root, r, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scEdgeSet(d map[string]any) map[string]bool {
	set := map[string]bool{}
	for _, e := range d["edges"].([]any) {
		m := e.(map[string]any)
		set[fmt.Sprint(m["from"], "->", m["to"])] = true
	}
	return set
}

func TestDepGraphCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scFixtureTree(t, root, "dep_graph")

	// Python only: package __init__, relative imports, a models<->util cycle; tests skipped.
	v, out := scCall(t, tb, "dep_graph", map[string]any{"lang": "py"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.8 {
		t.Fatalf("dep_graph py: %v\n%s", v, out.Text)
	}
	if d["text"] != "4 files, 5 edges, 1 cycles" || d["truncated"] != false {
		t.Errorf("py summary: %v", d["text"])
	}
	edges := scEdgeSet(d)
	for _, want := range []string{"app.main->app", "app.main->app.models", "app.main->app.util", "app.models->app.util", "app.util->app.models"} {
		if !edges[want] {
			t.Errorf("py edge %s missing in %v", want, edges)
		}
	}
	if fmt.Sprint(d["cycles"]) != "[[app.models app.util]]" {
		t.Errorf("py cycles = %v", d["cycles"])
	}
	ext := d["externals"].(map[string]any)
	if ext["httpkit"] != 2.0 || ext["os"] != 1.0 || ext["numlib"] != 1.0 {
		t.Errorf("py externals = %v", ext)
	}
	if _, has := ext["app"]; has {
		t.Errorf("internal package counted as external: %v", ext)
	}
	var ids []string
	for _, n := range d["nodes"].([]any) {
		m := n.(map[string]any)
		ids = append(ids, fmt.Sprint(m["id"], ":", m["file"], ":", m["in"], "/", m["out"]))
	}
	if strings.Join(ids, " ") != "app:app/__init__.py:1/0 app.main:app/main.py:0/3 app.models:app/models.py:2/1 app.util:app/util.py:2/1" {
		t.Errorf("py nodes = %v", ids)
	}

	// All languages: Go packages resolve by the go.mod module, JS/TS files by relative
	// path (extension-less, .js -> .ts), node_modules and *_test.go are skipped.
	v, _ = scCall(t, tb, "dep_graph", nil)
	d = scDataOf(v)
	if !scOK(v) || d["text"] != "10 files, 12 edges, 3 cycles" {
		t.Fatalf("auto summary: %v", v)
	}
	edges = scEdgeSet(d)
	for _, want := range []string{
		"example.com/demo->example.com/demo/internal/core",
		"example.com/demo/internal/core->example.com/demo/internal/util",
		"web/index.js->web/lib/a.js", "web/index.js->web/lib/b.ts", "web/lib/a.js->web/lib/b.ts", "web/lib/b.ts->web/index.js",
	} {
		if !edges[want] {
			t.Errorf("edge %s missing", want)
		}
	}
	ext = d["externals"].(map[string]any)
	if ext["@acme/ui"] != 1.0 || ext["example.org/toolkit/log"] != 1.0 || ext["gridkit"] != 1.0 || ext["fmt"] != 1.0 {
		t.Errorf("externals = %v", ext)
	}
	if _, has := ext["leftpad"]; has {
		t.Errorf("node_modules was scanned: %v", ext)
	}
	cyc := fmt.Sprint(d["cycles"])
	if !strings.Contains(cyc, "[web/lib/a.js web/lib/b.ts]") || !strings.Contains(cyc, "[web/index.js web/lib/a.js web/lib/b.ts]") {
		t.Errorf("js cycles = %v", cyc)
	}
	most := d["most_imported"].([]any)
	if len(most) == 0 || most[0].(map[string]any)["in"] != 2.0 {
		t.Errorf("most_imported = %v", most)
	}
	// edges are sorted by (from, to)
	var keys []string
	for k := range edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var got []string
	for _, e := range d["edges"].([]any) {
		m := e.(map[string]any)
		got = append(got, fmt.Sprint(m["from"], "->", m["to"]))
	}
	if strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Errorf("edges not sorted: %v", got)
	}

	// internal_only=false turns externals into nodes; limit truncates edges.
	v, _ = scCall(t, tb, "dep_graph", map[string]any{"lang": "go", "internal_only": false, "limit": 2})
	d = scDataOf(v)
	if !scOK(v) || d["truncated"] != true || len(d["edges"].([]any)) != 2 || d["text"] != "3 files, 6 edges, 0 cycles" {
		t.Errorf("go external nodes / limit: %v", v)
	}
	found := false
	for _, n := range d["nodes"].([]any) {
		m := n.(map[string]any)
		if m["id"] == "fmt" && m["file"] == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("external node fmt missing: %v", d["nodes"])
	}

	// Sub-path keeps ids relative to the root.
	v, _ = scCall(t, tb, "dep_graph", map[string]any{"path": "web"})
	if d = scDataOf(v); !scOK(v) || d["text"] != "3 files, 5 edges, 2 cycles" {
		t.Errorf("sub-path: %v", v)
	}

	// Errors are reported, never raised.
	v, out = scCall(t, tb, "dep_graph", map[string]any{"path": "nope"})
	if scOK(v) || !out.IsErr || !strings.Contains(fmt.Sprint(v["error"]), "path not found") {
		t.Errorf("missing path: %v", v)
	}
}

func TestDepOutdatedCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scFixtureTree(t, root, "dep_outdated")

	// npm: package.json x package-lock.json v3 (nested node_modules entries ignored).
	v, out := scCall(t, tb, "dep_outdated", map[string]any{"path": "node"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("npm: %v\n%s", v, out.Text)
	}
	if d["text"] != "5 deps, 1 unlocked, 1 loose, 1 mismatched" || d["locked"] != 4.0 || d["truncated"] != false {
		t.Errorf("npm summary: %v", d)
	}
	if fmt.Sprint(d["unlocked"]) != "[delta-missing]" || fmt.Sprint(d["dev_only"]) != "[epsilon-test]" {
		t.Errorf("npm unlocked/dev_only: %v %v", d["unlocked"], d["dev_only"])
	}
	loose := d["loose"].([]any)[0].(map[string]any)
	if loose["name"] != "alpha-ui" || loose["spec"] != "^1.2.0" {
		t.Errorf("npm loose = %v", loose)
	}
	mm := d["mismatched"].([]any)[0].(map[string]any)
	if mm["name"] != "beta-core" || mm["spec"] != "1.3.0" || mm["locked"] != "1.3.5" {
		t.Errorf("npm mismatched = %v", mm)
	}
	man := d["manifests"].([]any)[0].(map[string]any)
	if man["file"] != "node/package.json" || man["kind"] != "npm" || man["lock"] != "node/package-lock.json" || man["deps"] != 5.0 {
		t.Errorf("npm manifest = %v", man)
	}

	// pip: pyproject [project] x uv.lock; names normalized (Yaml_Kit == yaml-kit), ~= and >= are loose,
	// == pin outside the lock is mismatched, optional group "dev" is dev_only.
	v, _ = scCall(t, tb, "dep_outdated", map[string]any{"path": "py"})
	d = scDataOf(v)
	if !scOK(v) || d["text"] != "5 deps, 1 unlocked, 3 loose, 1 mismatched" {
		t.Fatalf("pip: %v", v)
	}
	if fmt.Sprint(d["unlocked"]) != "[orphan-pkg]" || fmt.Sprint(d["dev_only"]) != "[testkit]" {
		t.Errorf("pip unlocked/dev_only: %v %v", d["unlocked"], d["dev_only"])
	}
	var looseNames []string
	for _, l := range d["loose"].([]any) {
		looseNames = append(looseNames, fmt.Sprint(l.(map[string]any)["name"]))
	}
	if strings.Join(looseNames, ",") != "httpkit,testkit,yaml-kit" {
		t.Errorf("pip loose = %v", looseNames)
	}
	mm = d["mismatched"].([]any)[0].(map[string]any)
	if mm["name"] != "numlib" || mm["spec"] != "==1.4.0" || mm["locked"] != "1.5.0" {
		t.Errorf("pip mismatched = %v", mm)
	}
	if !strings.Contains(fmt.Sprint(v["warnings"]), "not evaluated") {
		t.Errorf("range specs must be flagged as not evaluated: %v", v["warnings"])
	}

	// requirements.txt without a lock: its own == pins count as locked, the rest is unlocked.
	v, _ = scCall(t, tb, "dep_outdated", map[string]any{"path": "py_req"})
	d = scDataOf(v)
	if !scOK(v) || d["text"] != "3 deps, 1 unlocked, 1 loose, 0 mismatched" || d["locked"] != 2.0 {
		t.Errorf("requirements self-lock: %v", v)
	}
	if fmt.Sprint(d["unlocked"]) != "[floating-lib]" || d["manifests"].([]any)[0].(map[string]any)["lock"] != nil {
		t.Errorf("requirements: %v", d)
	}

	// go: go.mod x go.sum presence; a module only present at another version is mismatched.
	v, _ = scCall(t, tb, "dep_outdated", map[string]any{"path": "go"})
	d = scDataOf(v)
	if !scOK(v) || d["text"] != "4 deps, 1 unlocked, 0 loose, 1 mismatched" {
		t.Fatalf("go: %v", v)
	}
	if fmt.Sprint(d["unlocked"]) != "[example.org/missing]" || fmt.Sprint(d["indirect"]) != "[example.org/other]" {
		t.Errorf("go unlocked/indirect: %v %v", d["unlocked"], d["indirect"])
	}
	mm = d["mismatched"].([]any)[0].(map[string]any)
	if mm["name"] != "example.org/other" || mm["spec"] != "v0.3.0" || mm["locked"] != "v0.4.0" {
		t.Errorf("go mismatched = %v", mm)
	}

	// Whole tree: manifests two levels deep, sorted; limit truncates every list.
	v, _ = scCall(t, tb, "dep_outdated", map[string]any{"limit": 2})
	d = scDataOf(v)
	if !scOK(v) || d["text"] != "17 deps, 2 unlocked, 2 loose, 2 mismatched" || d["truncated"] != true {
		t.Errorf("whole tree with limit: %v", v)
	}
	var files []string
	for _, m := range d["manifests"].([]any) {
		files = append(files, fmt.Sprint(m.(map[string]any)["file"]))
	}
	if strings.Join(files, ",") != "go/go.mod,node/package.json,py/pyproject.toml,py_req/requirements.txt" {
		t.Errorf("manifests = %v", files)
	}

	// No manifest: ok with zero deps, lowered confidence and a warning. The payload must
	// never show evidence of a network call (there is none: the script only reads files).
	tb2, _ := scTestBox(t)
	v, _ = scCall(t, tb2, "dep_outdated", nil)
	d = scDataOf(v)
	if !scOK(v) || d["deps"] != 0.0 || scConf(v) > 0.6 || len(v["warnings"].([]any)) == 0 {
		t.Errorf("no manifest: %v", v)
	}
}
