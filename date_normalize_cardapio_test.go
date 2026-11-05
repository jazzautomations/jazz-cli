//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDateNormalizeCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "date_normalize/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "date_normalize", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["text"] != "2024-01-02 = 2024-01-02T00:00:00" || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	v, _ = scCall(t, tb, "date_normalize", map[string]any{"text": "2024-01-02T03:00:00+03:00\n02/03/04"})
	items := scDataOf(v)["items"].([]any)
	if items[0].(map[string]any)["normalized"] != nil || items[1].(map[string]any)["normalized"] != "2024-01-02T00:00:00+00:00" {
		t.Fatalf("dates: %v", v)
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "date_normalize", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "date_normalize", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
