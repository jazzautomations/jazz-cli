//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffApplyCheckCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "diff_apply_check/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "diff_apply_check", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["applies"] != true || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	scWrite(t, root, "ana.txt", "changed\n")
	v, _ = scCall(t, tb, "diff_apply_check", map[string]any{"file": "input.txt"})
	if !scOK(v) || scDataOf(v)["applies"] != false {
		t.Fatalf("conflicting patch: %v", v)
	}
	scWrite(t, root, "ana.txt", "ana\n")
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "diff_apply_check", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "diff_apply_check", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
