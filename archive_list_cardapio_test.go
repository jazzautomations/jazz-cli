//go:build !windows

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// TestArchiveListCardapio runs jazz-scripts/archive_list.py through the script tool over
// the checked-in fixtures (jazz-scripts/testdata/archive_list, made by make_fixtures.py)
// and over archives built here: proves listing without extraction, the unsafe flags
// (../, absolute, escaping symlink), the bomb warning and the refusals.
func TestArchiveListCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	zipBytes := scFixture(t, "archive_list/sample.zip")
	tgzBytes := scFixture(t, "archive_list/sample.tar.gz")
	scWrite(t, root, "in/sample.zip", zipBytes)
	scWrite(t, root, "in/sample.tar.gz", tgzBytes)

	// zip: ../evil and /etc/passwd are unsafe and sorted first; pkg/ is a dir
	v, out := scCall(t, tb, "archive_list", map[string]any{"path": "in/sample.zip"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("zip: %v\n%s", v, out.Text)
	}
	if d["kind"] != "zip" || d["count"] != 5.0 || d["unsafe_count"] != 2.0 || d["total_size"] != 1632.0 || d["truncated"] != false {
		t.Errorf("zip summary: %v", d)
	}
	if cs, _ := d["compressed_size"].(float64); cs <= 0 || cs >= 1632 {
		t.Errorf("compressed_size = %v", d["compressed_size"])
	}
	if r, _ := d["ratio"].(float64); r <= 1 || r > 100 {
		t.Errorf("ratio = %v", d["ratio"])
	}
	entries, _ := d["entries"].([]any)
	var names []string
	for _, e := range entries {
		m := e.(map[string]any)
		names = append(names, m["name"].(string))
	}
	if strings.Join(names, " ") != "../evil /etc/passwd pkg/ pkg/app.py pkg/notes.txt" {
		t.Errorf("zip entries = %v", names)
	}
	if len(entries) == 5 {
		evil := entries[0].(map[string]any)
		dir := entries[2].(map[string]any)
		app := entries[3].(map[string]any)
		if evil["unsafe"] != true || evil["size"] != 21.0 || evil["is_dir"] != false {
			t.Errorf("../evil = %v", evil)
		}
		if dir["is_dir"] != true || dir["unsafe"] != false || dir["type"] != "dir" {
			t.Errorf("pkg/ = %v", dir)
		}
		if app["unsafe"] != false || app["size"] != 960.0 || app["compressed"] == nil {
			t.Errorf("pkg/app.py = %v", app)
		}
	}
	txt, _ := d["text"].(string)
	if !strings.HasPrefix(txt, "zip: 5 entries, ") || !strings.HasSuffix(txt, ", 2 unsafe") {
		t.Errorf("zip text: %q", txt)
	}
	if ws := scWarnings(v); len(ws) != 1 || !strings.Contains(ws[0], "2 unsafe entries") {
		t.Errorf("zip warnings: %v", ws)
	}

	// tar.gz: the escaping symlink is unsafe, the in-tree symlink is not; compressed size is the file
	v, _ = scCall(t, tb, "archive_list", map[string]any{"path": "in/sample.tar.gz"})
	d = scDataOf(v)
	if !scOK(v) || d["kind"] != "tar.gz" || d["count"] != 5.0 || d["unsafe_count"] != 1.0 || d["total_size"] != 990.0 {
		t.Fatalf("tar.gz: %v", v)
	}
	if d["compressed_size"] != float64(len(tgzBytes)) {
		t.Errorf("compressed_size = %v, want %d", d["compressed_size"], len(tgzBytes))
	}
	entries, _ = d["entries"].([]any)
	names = names[:0]
	for _, e := range entries {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if strings.Join(names, " ") != "site/escape site site/current site/index.html site/style.css" {
		t.Errorf("tar entries = %v", names)
	}
	if len(entries) == 5 {
		esc := entries[0].(map[string]any)
		cur := entries[2].(map[string]any)
		if esc["unsafe"] != true || esc["type"] != "symlink" || esc["link"] != "../../outside" {
			t.Errorf("site/escape = %v", esc)
		}
		if cur["unsafe"] != false || cur["type"] != "symlink" || cur["link"] != "index.html" {
			t.Errorf("site/current = %v", cur)
		}
	}
	if txt, _ = d["text"].(string); !strings.HasPrefix(txt, "tar.gz: 5 entries, ") || !strings.HasSuffix(txt, ", 1 unsafe") {
		t.Errorf("tar text: %q", txt)
	}

	// limit: unsafe entries survive the cut; count still says 5; confidence drops
	v, _ = scCall(t, tb, "archive_list", map[string]any{"path": "in/sample.zip", "limit": 2})
	d = scDataOf(v)
	entries, _ = d["entries"].([]any)
	if !scOK(v) || d["count"] != 5.0 || d["truncated"] != true || len(entries) != 2 || scConf(v) > 0.7 {
		t.Errorf("limit=2: %v", d)
	}
	for _, e := range entries {
		if e.(map[string]any)["unsafe"] != true {
			t.Errorf("limit cut dropped an unsafe entry: %v", entries)
		}
	}

	// a zip of 5 MB of zeros compresses >100:1 -> bomb warning, no extraction attempted
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, err := zw.Create("zeros.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 5<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	scWrite(t, root, "in/bomb.zip", zb.String())
	v, _ = scCall(t, tb, "archive_list", map[string]any{"path": "in/bomb.zip"})
	d = scDataOf(v)
	if r, _ := d["ratio"].(float64); !scOK(v) || r <= 100 || d["unsafe_count"] != 0.0 {
		t.Fatalf("bomb: %v", v)
	}
	if !strings.Contains(d["text"].(string), "BOMB?") {
		t.Errorf("bomb text: %q", d["text"])
	}
	bombWarned := false
	for _, w := range scWarnings(v) {
		if strings.Contains(w, "decompression bomb") {
			bombWarned = true
		}
	}
	if !bombWarned {
		t.Errorf("bomb warnings: %v", scWarnings(v))
	}

	// plain .tar: no compression -> compressed_size and ratio are null, not 1.0
	var tb2 bytes.Buffer
	tw := tar.NewWriter(&tb2)
	if err := tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	scWrite(t, root, "in/plain.tar", tb2.String())
	v, _ = scCall(t, tb, "archive_list", map[string]any{"path": "in/plain.tar"})
	d = scDataOf(v)
	if !scOK(v) || d["kind"] != "tar" || d["count"] != 1.0 || d["compressed_size"] != nil || d["ratio"] != nil || d["total_size"] != 3.0 {
		t.Errorf("plain tar: %v", v)
	}
	if !strings.Contains(d["text"].(string), "3 B uncompressed") {
		t.Errorf("plain tar text: %q", d["text"])
	}

	// refusals: no path, missing file, not an archive, zip extension over garbage
	scWrite(t, root, "in/notes.txt", "just text\n")
	scWrite(t, root, "in/garbage.zip", "this is not a zip at all\n")
	for _, args := range []map[string]any{
		nil,
		{"path": "in/nope.zip"},
		{"path": "in/notes.txt"},
		{"path": "in/garbage.zip"},
		{"path": "in"},
	} {
		v, out = scCall(t, tb, "archive_list", args)
		if scOK(v) || !out.IsErr {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
	v, _ = scCall(t, tb, "archive_list", map[string]any{"path": "in/notes.txt"})
	if e, _ := v["error"].(string); !strings.Contains(e, "accepted: .zip") {
		t.Errorf("unknown format must list accepted kinds: %q", e)
	}
}

// scWarnings returns the warnings list of a script result.
func scWarnings(v map[string]any) []string {
	raw, _ := v["warnings"].([]any)
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		s, _ := w.(string)
		out = append(out, s)
	}
	return out
}
