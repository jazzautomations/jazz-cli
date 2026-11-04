//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestCardapioHashDiff drives file_hash_diff through the same path the agent uses
// (tb.Exec("script", ...)): snapshot a tree, mutate it, and prove the diff names the
// changed/added/removed files; then prove an unmodified tree reports no changes.
func TestCardapioHashDiff(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is absent

	scWrite(t, root, "proj/a.txt", "alpha\n")
	scWrite(t, root, "proj/b.txt", "beta\n")
	scWrite(t, root, "proj/sub/c.txt", "gamma\n")

	// snapshot
	v, _ := scCall(t, tb, "file_hash_diff", map[string]any{"path": "proj", "op": "snapshot"})
	d := scDataOf(v)
	if !scOK(v) {
		t.Fatalf("snapshot not ok: %v", v)
	}
	if d["files"] != 3.0 {
		t.Errorf("snapshot files = %v, want 3", d["files"])
	}
	rootSha, _ := d["root_sha256"].(string)
	if len(rootSha) != 64 {
		t.Errorf("root_sha256 = %q, want 64 hex chars", rootSha)
	}
	if d["truncated"] != false {
		t.Errorf("truncated = %v, want false", d["truncated"])
	}
	entries, _ := d["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v, want 3", d["entries"])
	}
	e0 := entries[0].(map[string]any)
	if e0["path"] != "proj/a.txt" || e0["sha256"] == "" || e0["size"] == nil {
		t.Errorf("first entry = %v", e0)
	}

	// base for the diff = the snapshot's data object, as a JSON string
	base, _ := json.Marshal(d)

	// a second snapshot of the unchanged tree must hash the same (deterministic)
	v2, _ := scCall(t, tb, "file_hash_diff", map[string]any{"path": "proj", "op": "snapshot"})
	if scDataOf(v2)["root_sha256"] != rootSha {
		t.Errorf("root_sha256 not stable across runs: %v vs %v", scDataOf(v2)["root_sha256"], rootSha)
	}

	// mutate: change b.txt, add d.txt, remove a.txt
	scWrite(t, root, "proj/b.txt", "beta-CHANGED\n")
	scWrite(t, root, "proj/d.txt", "delta\n")
	if err := os.Remove(filepath.Join(root, "proj/a.txt")); err != nil {
		t.Fatal(err)
	}

	v, _ = scCall(t, tb, "file_hash_diff", map[string]any{"path": "proj", "op": "diff", "base": string(base)})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("diff not ok: %v", v)
	}
	if d["same"] != false {
		t.Errorf("same = %v, want false", d["same"])
	}
	if got := fmt.Sprint(d["added"]); got != "[proj/d.txt]" {
		t.Errorf("added = %v, want [proj/d.txt]", got)
	}
	if got := fmt.Sprint(d["removed"]); got != "[proj/a.txt]" {
		t.Errorf("removed = %v, want [proj/a.txt]", got)
	}
	changed, _ := d["changed"].([]any)
	if len(changed) != 1 || changed[0].(map[string]any)["path"] != "proj/b.txt" {
		t.Fatalf("changed = %v, want one entry proj/b.txt", d["changed"])
	}
	c0 := changed[0].(map[string]any)
	if c0["old_sha"] == c0["new_sha"] || c0["old_sha"] == "" || c0["new_sha"] == "" {
		t.Errorf("changed sha pair looks wrong: %v", c0)
	}
	if d["unchanged"] != 1.0 {
		t.Errorf("unchanged = %v, want 1", d["unchanged"])
	}
	if d["text"] != "1 changed, 1 added, 1 removed" {
		t.Errorf("text = %q", d["text"])
	}

	// restore the tree to match the base exactly -> no changes
	scWrite(t, root, "proj/a.txt", "alpha\n")
	scWrite(t, root, "proj/b.txt", "beta\n")
	if err := os.Remove(filepath.Join(root, "proj/d.txt")); err != nil {
		t.Fatal(err)
	}
	v, _ = scCall(t, tb, "file_hash_diff", map[string]any{"path": "proj", "op": "diff", "base": string(base)})
	d = scDataOf(v)
	if d["same"] != true || d["text"] != "no changes" {
		t.Errorf("restored tree: same=%v text=%q", d["same"], d["text"])
	}

	// op=diff without base is refused
	v, out := scCall(t, tb, "file_hash_diff", map[string]any{"path": "proj", "op": "diff"})
	if scOK(v) || !out.IsErr {
		t.Errorf("diff without base must be refused: %v", v)
	}
}
