//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegexTestCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "regex_test/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "regex_test", map[string]any{"file": "input.txt", "pattern": "^ana$"})
	d := scDataOf(v)
	if !scOK(v) || d["matches"] != 1.0 || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	v, _ = scCall(t, tb, "regex_test", map[string]any{"text": strings.Repeat("a", 2000) + "!", "pattern": "^(a+)+$"})
	d = scDataOf(v)
	items := d["items"].([]any)
	if !scOK(v) || d["truncated"] != true || items[0].(map[string]any)["matched"] != nil || items[0].(map[string]any)["timed_out"] != true {
		t.Fatalf("timeout: %v", v)
	}
	v, _ = scCall(t, tb, "regex_test", map[string]any{"text": "ana", "pattern": "["})
	if scOK(v) {
		t.Fatal("invalid regex accepted")
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "regex_test", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "regex_test", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
	v, _ = scCall(t, tb, "regex_test", map[string]any{"text": strings.Repeat("a", 4096) + "!", "pattern": "a$"})
	d = scDataOf(v)
	if d["truncated"] != true || d["items"].([]any)[0].(map[string]any)["matched"] != nil {
		t.Fatalf("clipped input must be unknown: %v", v)
	}
}
