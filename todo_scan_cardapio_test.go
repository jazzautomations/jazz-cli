//go:build !windows

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// todoScanFixture copies jazz-scripts/testdata/todo_scan into root (node_modules included:
// the script must skip it on its own).
func todoScanFixture(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join("jazz-scripts", "testdata", "todo_scan")
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

// TestTodoScanCardapio runs jazz-scripts/todo_scan.py through the script tool
// (same path the agent uses) over the fixture tree.
func TestTodoScanCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	todoScanFixture(t, root)

	v, out := scCall(t, tb, "todo_scan", nil)
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("todo_scan: %v\n%s", v, out.Text)
	}
	// app.py, notes.md, sub/main.go, sub/main_test.go; blob.c is binary, data.unknown and node_modules are skipped
	if d["files_scanned"] != 4.0 || d["total"] != 8.0 || d["truncated"] != false || d["files_with_markers"] != 4.0 {
		t.Errorf("counts: %v", d)
	}
	byTag := d["by_tag"].(map[string]any)
	want := map[string]float64{"BUG": 2, "FIXME": 1, "HACK": 1, "TODO": 3, "XXX": 1}
	for k, n := range want {
		if byTag[k] != n {
			t.Errorf("by_tag[%s] = %v, want %v", k, byTag[k], n)
		}
	}
	items := d["items"].([]any)
	var got []string
	for _, it := range items {
		m := it.(map[string]any)
		got = append(got, fmt.Sprintf("%s:%v %s %s", m["file"], m["line"], m["tag"], m["text"]))
	}
	wantItems := []string{
		"app.py:6 TODO (ana) handle missing file",
		"app.py:8 FIXME encoding is assumed utf-8",
		"notes.md:3 TODO write the release checklist",
		"notes.md:4 BUG login page 500 when the cookie is stale",
		"sub/main.go:3 HACK sleep until the port is free; replace with a real wait.",
		"sub/main.go:5 XXX this leaks the handle",
		"sub/main.go:6 BUG nil on windows",
		"sub/main_test.go:3 TODO table-driven cases",
	}
	if strings.Join(got, "\n") != strings.Join(wantItems, "\n") {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantItems, "\n"))
	}
	for _, s := range got {
		if strings.Contains(s, "node_modules") || strings.Contains(s, "DEBUG") || strings.Contains(s, "todos") {
			t.Errorf("decoy matched: %s", s)
		}
	}
	if txt, _ := d["text"].(string); !strings.Contains(txt, "8 marker(s)") || !strings.Contains(txt, "TODO 3") {
		t.Errorf("text: %q", txt)
	}

	// include_tests=false drops sub/main_test.go; tags narrows; limit truncates
	v, _ = scCall(t, tb, "todo_scan", map[string]any{"include_tests": false, "tags": []string{"TODO", "BUG"}, "limit": 2})
	d = scDataOf(v)
	if !scOK(v) || d["total"] != 4.0 || d["truncated"] != true || len(d["items"].([]any)) != 2 {
		t.Errorf("filtered: %v", d)
	}
	if bt := d["by_tag"].(map[string]any); bt["TODO"] != 2.0 || bt["BUG"] != 2.0 || len(bt) != 2 {
		t.Errorf("by_tag filtered: %v", bt)
	}

	// path narrows to a subtree; a single file works too
	v, _ = scCall(t, tb, "todo_scan", map[string]any{"path": "sub"})
	if d = scDataOf(v); d["total"] != 4.0 || d["files_scanned"] != 2.0 {
		t.Errorf("subtree: %v", d)
	}
	v, _ = scCall(t, tb, "todo_scan", map[string]any{"path": "notes.md"})
	if d = scDataOf(v); d["total"] != 2.0 || d["files_scanned"] != 1.0 {
		t.Errorf("single file: %v", d)
	}
	v, _ = scCall(t, tb, "todo_scan", map[string]any{"path": "nope"})
	if scOK(v) {
		t.Errorf("missing path must fail: %v", v)
	}
}
