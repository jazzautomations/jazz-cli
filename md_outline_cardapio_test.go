//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMdOutlineCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "md_outline/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "md_outline", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["text"] != "1 headings, 2 links, 1 broken anchors" || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	if len(d["headings"].([]any)) != 1 || len(d["broken_anchors"].([]any)) != 1 {
		t.Fatalf("outline: %v", d)
	}
	v, _ = scCall(t, tb, "md_outline", map[string]any{"text": "# ana\n# ana\n[ok](#ana-1)\n```\n# hidden\n```\nhttpkit\n========"})
	d = scDataOf(v)
	if len(d["headings"].([]any)) != 3 || len(d["broken_anchors"].([]any)) != 0 {
		t.Fatalf("fences/duplicates: %v", v)
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "md_outline", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "md_outline", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
