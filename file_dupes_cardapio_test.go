//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestFileDupesCardapio runs jazz-scripts/file_dupes.py through the script tool over
// a tree with known duplicates: identical content groups regardless of name,
// same-size-but-different files do not group, zero-byte and hidden/skip dirs stay
// out, and the wasted-bytes math is exact.
func TestFileDupesCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	dup := "the very same bytes\n"
	scWrite(t, root, "proj/src/dup1.txt", dup)
	scWrite(t, root, "proj/docs/copy.txt", dup)
	scWrite(t, root, "proj/tmp/another.txt", dup)
	scWrite(t, root, "proj/src/uniq.go", "package u\n")
	scWrite(t, root, "proj/docs/samesize-a.txt", "aaaaa\n")
	scWrite(t, root, "proj/docs/samesize-b.txt", "bbbbb\n")
	scWrite(t, root, "proj/empty1.txt", "")
	scWrite(t, root, "proj/empty2.txt", "")
	scWrite(t, root, "proj/.git/HEAD", "ref\n")
	scWrite(t, root, "proj/node_modules/x/i.js", dup)

	v, out := scCall(t, tb, "file_dupes", map[string]any{"path": "proj"})
	d := scDataOf(v)
	if !scOK(v) || d["group_count"] != 1.0 || d["dup_files"] != 3.0 {
		t.Fatalf("dupes: %v\n%s", v, out.Text)
	}
	g := d["groups"].([]any)[0].(map[string]any)
	files, _ := g["files"].([]any)
	got := []string{}
	for _, f := range files {
		got = append(got, f.(string))
	}
	if strings.Join(got, " ") != "proj/docs/copy.txt proj/src/dup1.txt proj/tmp/another.txt" {
		t.Errorf("group files: %v", got)
	}
	// wasted = size * (copies - 1) = 20 * 2
	if g["size"] != 20.0 || d["wasted_bytes"] != 40.0 {
		t.Errorf("wasted: %v", d)
	}
	if len(g["sha256"].(string)) != 16 {
		t.Errorf("hash prefix: %v", g["sha256"])
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "1 duplicate group(s), 3 file(s)") {
		t.Errorf("text: %q", txt)
	}

	// min_size=0 pulls the two empty files into a second group
	v, _ = scCall(t, tb, "file_dupes", map[string]any{"path": "proj", "min_size": 0})
	if d = scDataOf(v); !scOK(v) || d["group_count"] != 2.0 {
		t.Errorf("min_size=0: %v", v)
	}

	// a tree with no duplicates is ok:true and says so
	scWrite(t, root, "solo/a.txt", "1\n")
	scWrite(t, root, "solo/b.txt", "22\n")
	v, _ = scCall(t, tb, "file_dupes", map[string]any{"path": "solo"})
	if d = scDataOf(v); !scOK(v) || d["group_count"] != 0.0 || !strings.Contains(d["text"].(string), "no duplicate files") {
		t.Errorf("no dupes: %v", v)
	}

	// refusals: file as path, missing, traversal, symlink root
	for _, args := range []map[string]any{
		{"path": "proj/src/uniq.go"},
		{"path": "nope"},
		{"path": ".."},
	} {
		if v, _ = scCall(t, tb, "file_dupes", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
