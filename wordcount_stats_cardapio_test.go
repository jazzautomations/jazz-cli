//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordcountStatsCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "wordcount_stats/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "wordcount_stats", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["words"] != 3.0 || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	items := d["frequencies"].([]any)
	if items[0].(map[string]any)["word"] != "ana" || items[0].(map[string]any)["count"] != 2.0 {
		t.Fatalf("frequencies: %v", d)
	}
	v, _ = scCall(t, tb, "wordcount_stats", map[string]any{"text": strings.Repeat("a ", 150000)})
	if scDataOf(v)["truncated"] != true {
		t.Fatalf("input budget: %v", v)
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "wordcount_stats", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "wordcount_stats", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
