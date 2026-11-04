//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFsRecentCardapio runs jazz-scripts/fs_recent.py through the script tool over a
// tree with explicit mtimes: proves the window, the ordering (newest first), the
// skips (.git, node_modules, hidden, symlinks) and the limit/truncated contract.
func TestFsRecentCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	now := time.Now()
	age := func(rel string, d time.Duration) {
		t.Helper()
		ts := now.Add(-d)
		if err := os.Chtimes(filepath.Join(root, rel), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	scWrite(t, root, "proj/src/new.py", "new\n")
	scWrite(t, root, "proj/src/app.py", "app\n")
	scWrite(t, root, "proj/README.md", "readme\n")
	scWrite(t, root, "proj/sub/old.txt", "old\n")
	scWrite(t, root, "proj/node_modules/x/i.js", "vendored\n")
	scWrite(t, root, "proj/.git/HEAD", "ref\n")
	scWrite(t, root, "proj/.hidden", "dot\n")
	age("proj/src/new.py", 0)
	age("proj/src/app.py", 5*time.Minute)
	age("proj/README.md", 30*time.Minute)
	age("proj/sub/old.txt", 3*time.Hour)
	age("proj/.hidden", 0)
	if err := os.Symlink("src/app.py", filepath.Join(root, "proj/link.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(root, "proj/sublink")); err != nil {
		t.Fatal(err)
	}

	v, out := scCall(t, tb, "fs_recent", map[string]any{"path": "proj"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("fs_recent: %v\n%s", v, out.Text)
	}
	// new.py, app.py, README.md inside 60 min; old.txt is 3 h old; .git, node_modules,
	// .hidden and both symlinks are never counted
	if d["count"] != 3.0 || d["files_scanned"] != 4.0 || d["truncated"] != false || d["minutes"] != 60.0 {
		t.Errorf("counts: %v", d)
	}
	files, _ := d["files"].([]any)
	var got []string
	for _, f := range files {
		got = append(got, f.(map[string]any)["path"].(string))
	}
	if strings.Join(got, " ") != "proj/src/new.py proj/src/app.py proj/README.md" {
		t.Errorf("order = %v", got)
	}
	if len(files) == 3 {
		app := files[1].(map[string]any)
		if a, _ := app["age_min"].(float64); a < 4.5 || a > 6.5 {
			t.Errorf("app.py age_min = %v, want ~5", app["age_min"])
		}
		if app["size"] != 4.0 {
			t.Errorf("app.py size = %v", app["size"])
		}
		if iso, _ := app["mtime_iso"].(string); len(iso) < 19 || !strings.Contains(iso, "T") {
			t.Errorf("mtime_iso = %q", iso)
		}
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "3 file(s) changed in the last 60 min (newest: proj/src/new.py, 0 min ago)") {
		t.Errorf("text: %q", txt)
	}

	// a wider window reaches old.txt, last because it is the oldest
	v, _ = scCall(t, tb, "fs_recent", map[string]any{"path": "proj", "minutes": 600})
	d = scDataOf(v)
	files, _ = d["files"].([]any)
	if d["count"] != 4.0 || len(files) != 4 || files[3].(map[string]any)["path"] != "proj/sub/old.txt" {
		t.Errorf("minutes=600: %v", d)
	}

	// a narrow window with nothing in it is still ok=true and says so
	v, _ = scCall(t, tb, "fs_recent", map[string]any{"path": "proj/sub", "minutes": 1})
	d = scDataOf(v)
	if !scOK(v) || d["count"] != 0.0 || !strings.Contains(d["text"].(string), "no file changed") {
		t.Errorf("empty window: %v", v)
	}

	// include_hidden adds .hidden (not .git); limit truncates and lowers confidence
	v, _ = scCall(t, tb, "fs_recent", map[string]any{"path": "proj", "include_hidden": true, "limit": 2})
	d = scDataOf(v)
	files, _ = d["files"].([]any)
	if d["count"] != 4.0 || d["files_scanned"] != 5.0 || d["truncated"] != true || len(files) != 2 || scConf(v) > 0.7 {
		t.Errorf("include_hidden+limit: %v", d)
	}
	for _, f := range files {
		if p := f.(map[string]any)["path"].(string); strings.Contains(p, ".git/") || strings.Contains(p, "node_modules") {
			t.Errorf("skipped dir leaked: %s", p)
		}
	}

	// a single file works; symlinks and missing paths are refused; bad args are refused
	v, _ = scCall(t, tb, "fs_recent", map[string]any{"path": "proj/src/app.py"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files_scanned"] != 1.0 {
		t.Errorf("single file: %v", v)
	}
	for _, args := range []map[string]any{
		{"path": "proj/sublink"},
		{"path": "proj/link.py"},
		{"path": "nope"},
		{"path": "proj", "minutes": 0},
		{"path": "proj", "minutes": 99999},
		{"path": "proj", "limit": "many"},
	} {
		if v, _ = scCall(t, tb, "fs_recent", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
