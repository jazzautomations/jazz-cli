//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestDirDiffCardapio runs jazz-scripts/dir_diff.py through the script tool over two
// controlled trees: only-in-a, only-in-b, same-name-different-content and
// identical trees, plus the refusals. Nothing is copied or deleted.
func TestDirDiffCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "a/only-a.txt", "alpha\n")
	scWrite(t, root, "b/only-b.txt", "beta\n")
	scWrite(t, root, "a/same.txt", "SAME\n")
	scWrite(t, root, "b/same.txt", "SAME\n")
	scWrite(t, root, "a/changed.txt", "v1\n")
	scWrite(t, root, "b/changed.txt", "v2\n")
	scWrite(t, root, "a/sub/f.txt", "x\n")
	scWrite(t, root, "b/sub/f.txt", "x\n")
	scWrite(t, root, "a/dir-only/i.txt", "i\n")
	scWrite(t, root, "a/.git/HEAD", "ref\n")
	scWrite(t, root, "b/.git/HEAD", "different\n")

	v, out := scCall(t, tb, "dir_diff", map[string]any{"a": "a", "b": "b"})
	d := scDataOf(v)
	if !scOK(v) {
		t.Fatalf("dir_diff: %v\n%s", v, out.Text)
	}
	// only_a: dir-only + dir-only/i.txt + only-a.txt (3). only_b: only-b.txt (1).
	if d["only_a_count"] != 3.0 || d["only_b_count"] != 1.0 || d["differ_count"] != 1.0 || d["identical"] != 2.0 {
		t.Fatalf("counts: %v", d)
	}
	if diff := d["differ"].([]any); diff[0].(string) != "changed.txt" {
		t.Errorf("differ: %v", diff)
	}
	for _, r := range d["only_a"].([]any) {
		if strings.HasPrefix(r.(string), ".git") {
			t.Errorf(".git leaked: %v", r)
		}
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "3 only in a, 1 only in b, 1 differ, 2 identical") {
		t.Errorf("text: %q", txt)
	}

	// identical trees answer so
	v, _ = scCall(t, tb, "dir_diff", map[string]any{"a": "a", "b": "a"})
	if d = scDataOf(v); !scOK(v) || d["differ_count"] != 0.0 || !strings.Contains(d["text"].(string), "identical") {
		t.Errorf("identical: %v", v)
	}

	// size-differing files count as differ without hashing
	scWrite(t, root, "c/big.txt", "123456789\n")
	scWrite(t, root, "d/big.txt", "1\n")
	v, _ = scCall(t, tb, "dir_diff", map[string]any{"a": "c", "b": "d"})
	if d = scDataOf(v); !scOK(v) || d["differ_count"] != 1.0 {
		t.Errorf("size differ: %v", v)
	}

	// refusals: missing args, file as dir, missing dir, traversal
	for _, args := range []map[string]any{
		{"a": "a"},
		{"b": "b"},
		{},
		{"a": "a/same.txt", "b": "b"},
		{"a": "nope", "b": "b"},
		{"a": "..", "b": "b"},
	} {
		if v, _ = scCall(t, tb, "dir_diff", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
