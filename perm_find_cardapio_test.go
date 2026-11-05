//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPermFindCardapio runs jazz-scripts/perm_find.py through the script tool over a
// tree with explicit modes: exact octal match, suid, world-writable and
// executable filters, plus the no-filter and bad-arg refusals.
func TestPermFindCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	mk := func(rel, content string, mode os.FileMode) {
		t.Helper()
		p := scWrite(t, root, rel, content)
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mk("perm/normal.txt", "x\n", 0o644)
	mk("perm/allopen.sh", "x\n", 0o777)
	mk("perm/suidbin", "x\n", 0o755|os.ModeSetuid)
	mk("perm/owner-x.sh", "x\n", 0o755)
	mk("perm/ww.log", "x\n", 0o646)
	mk("perm/sub/inner777", "x\n", 0o777)

	// exact octal match finds only the two 0777 files
	v, out := scCall(t, tb, "perm_find", map[string]any{"path": "perm", "mode": "777"})
	d := scDataOf(v)
	if !scOK(v) || d["count"] != 2.0 {
		t.Fatalf("mode=777: %v\n%s", v, out.Text)
	}
	for _, f := range d["files"].([]any) {
		m := f.(map[string]any)
		if m["mode"] != "0777" || m["type"] != "file" {
			t.Errorf("mode=777 row: %v", m)
		}
	}

	// suid finds the setuid binary; world-writable finds o+w regardless of x
	v, _ = scCall(t, tb, "perm_find", map[string]any{"path": "perm", "suid": true})
	if d = scDataOf(v); !scOK(v) || d["count"] != 1.0 || d["files"].([]any)[0].(map[string]any)["path"] != "perm/suidbin" {
		t.Errorf("suid: %v", v)
	}
	v, _ = scCall(t, tb, "perm_find", map[string]any{"path": "perm", "world_writable": true})
	if d = scDataOf(v); !scOK(v) || d["count"] != 3.0 {
		t.Fatalf("world_writable: %v", v)
	}
	for _, f := range d["files"].([]any) {
		p := f.(map[string]any)["path"].(string)
		if p != "perm/allopen.sh" && p != "perm/ww.log" && p != "perm/sub/inner777" {
			t.Errorf("unexpected ww hit: %s", p)
		}
	}

	// executable files: 644 and 646 stay out
	v, _ = scCall(t, tb, "perm_find", map[string]any{"path": "perm", "executable": true, "type": "file"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 4.0 {
		t.Fatalf("executable: %v", v)
	}
	for _, f := range d["files"].([]any) {
		if strings.HasSuffix(f.(map[string]any)["path"].(string), ".txt") ||
			strings.HasSuffix(f.(map[string]any)["path"].(string), ".log") {
			t.Errorf("non-exec leaked: %v", f)
		}
	}

	// refusals: no filter at all, bad octal, non-dir path, traversal, bogus user
	for _, args := range []map[string]any{
		{"path": "perm"},
		{"path": "perm", "mode": "999"},
		{"path": "perm", "mode": "abc"},
		{"path": "perm/normal.txt", "suid": true},
		{"path": "..", "suid": true},
		{"path": "perm", "owner": "no such user"},
	} {
		if v, _ = scCall(t, tb, "perm_find", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
