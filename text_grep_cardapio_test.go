//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTextGrepCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "text_grep/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "text_grep", map[string]any{"file": "input.txt", "pattern": "ana"})
	d := scDataOf(v)
	if !scOK(v) || d["matches"] != 2.0 || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	items := d["items"].([]any)
	if items[0].(map[string]any)["line"] != 1.0 || len(items[0].(map[string]any)["after"].([]any)) != 2 {
		t.Fatalf("context: %v", d)
	}
	v, _ = scCall(t, tb, "text_grep", map[string]any{"text": "ana\nana", "pattern": "ana", "limit": 1})
	if scDataOf(v)["truncated"] != true || scDataOf(v)["matches"] != 1.0 {
		t.Fatalf("limit: %v", v)
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "text_grep", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "text_grep", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
	outside := t.TempDir()
	scWrite(t, outside, "secret.txt", "ana\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	v, _ = scCall(t, tb, "text_grep", map[string]any{"file": "link.txt", "pattern": "ana"})
	if scOK(v) {
		t.Fatalf("symlink escape accepted: %v", v)
	}
}
