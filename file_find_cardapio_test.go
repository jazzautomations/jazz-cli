//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFileFindCardapio runs jazz-scripts/file_find.py through the script tool over a
// tree with mixed names, extensions, sizes, mtimes and emptiness: proves each
// filter, the skips (.git, node_modules, hidden, symlinks), the ordering and the
// at-least-one-filter and outside-root refusals.
func TestFileFindCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "proj/src/app.go", "package main\n")
	scWrite(t, root, "proj/src/util.go", "package main\nfunc u() {}\n")
	scWrite(t, root, "proj/README.md", "readme\n")
	scWrite(t, root, "proj/logs/today.log", "new\n")
	scWrite(t, root, "proj/logs/old.log", "old\n")
	scWrite(t, root, "proj/tmp/empty.txt", "")
	scWrite(t, root, "proj/node_modules/x/i.js", "vendored\n")
	scWrite(t, root, "proj/.git/HEAD", "ref\n")
	scWrite(t, root, "proj/.hidden", "dot\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "proj/logs/old.log"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(root, "proj/srclink")); err != nil {
		t.Fatal(err)
	}

	// ext filter finds only .go files, sorted
	v, out := scCall(t, tb, "file_find", map[string]any{"path": "proj", "ext": "go"})
	d := scDataOf(v)
	if !scOK(v) || d["count"] != 2.0 || d["scanned"] == 0.0 {
		t.Fatalf("ext=go: %v\n%s", v, out.Text)
	}
	files, _ := d["files"].([]any)
	if files[0].(map[string]any)["path"] != "proj/src/app.go" || files[1].(map[string]any)["path"] != "proj/src/util.go" {
		t.Errorf("ext=go order: %v", files)
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "2 match(es)") {
		t.Errorf("text: %q", txt)
	}

	// name glob matches base names; a plain word is EXACT (named X ≠ file1)
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "name": "*.log"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 2.0 {
		t.Errorf("name=*.log: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "name": "app.go"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 {
		t.Errorf("name=app.go exato: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "namepat": "*app*"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 {
		t.Errorf("namepat=*app* substring: %v", v)
	}

	// mtime window: old.log only in the older-than view, today.log only in newer
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj/logs", "ext": "log", "older_min": 60 * 24})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files"].([]any)[0].(map[string]any)["path"] != "proj/logs/old.log" {
		t.Errorf("older_min: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj/logs", "ext": "log", "newer_min": 60})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files"].([]any)[0].(map[string]any)["path"] != "proj/logs/today.log" {
		t.Errorf("newer_min: %v", v)
	}

	// empty finds the empty file and the empty dir never appears because tmp has files
	scWrite(t, root, "proj/vacant/.keep", "k\n")
	if err := os.Remove(filepath.Join(root, "proj/vacant/.keep")); err != nil {
		t.Fatal(err)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "empty": true})
	if d = scDataOf(v); !scOK(v) || d["count"] != 2.0 {
		t.Fatalf("empty: %v", v)
	}
	got := map[string]bool{}
	for _, f := range d["files"].([]any) {
		m := f.(map[string]any)
		got[m["path"].(string)] = m["type"].(string) == "dir"
	}
	fIsDir, okFile := got["proj/tmp/empty.txt"]
	dIsDir, okDir := got["proj/vacant"]
	if !okFile || fIsDir || !okDir || !dIsDir {
		t.Errorf("empty set: %v", got)
	}

	// type=dir lists only dirs; size filter keeps util.go (26 B) out of <=10 B
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "type": "dir", "name": "src"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files"].([]any)[0].(map[string]any)["type"] != "dir" {
		t.Errorf("type=dir: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj/src", "ext": "go", "max_size": 15})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files"].([]any)[0].(map[string]any)["path"] != "proj/src/app.go" {
		t.Errorf("max_size: %v", v)
	}

	// skipped and hidden entries never appear; outside root and bad filters refuse.
	// Bare path is now a legitimate unfiltered find-all (bounded by limit) —
	// the corpus asks "find all files under current directory".
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj", "type": "file", "min_size": 0})
	for _, f := range scDataOf(v)["files"].([]any) {
		p := f.(map[string]any)["path"].(string)
		if strings.Contains(p, ".git") || strings.Contains(p, "node_modules") || strings.HasSuffix(p, ".hidden") || strings.Contains(p, "srclink") {
			t.Errorf("skipped/hidden/symlink leaked: %s", p)
		}
	}
	if v, _ = scCall(t, tb, "file_find", map[string]any{"path": "proj"}); !scOK(v) {
		t.Errorf("unfiltered find-all must be allowed: %v", v)
	}
	for _, args := range []map[string]any{
		{"path": "proj", "ext": "go", "min_size": 100, "max_size": 5},
		{"path": "proj", "ext": "g o"},
		{"path": "proj/srclink", "type": "dir"},
		{"path": "nope", "type": "dir"},
		{"path": "..", "type": "dir"},
		{"path": "proj", "ext": "go", "newer_min": 60, "older_min": 1440},
	} {
		if v, _ = scCall(t, tb, "file_find", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
