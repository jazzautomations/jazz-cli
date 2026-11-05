//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTableRenderCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "table_render/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "table_render", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["rows"] != 1.0 || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	if d["table"] != "count | name\n2     | ana " {
		t.Fatalf("alignment: %v", d)
	}
	v, _ = scCall(t, tb, "table_render", map[string]any{"text": "[1]"})
	if scOK(v) {
		t.Fatal("non-object row accepted")
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "table_render", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "table_render", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
