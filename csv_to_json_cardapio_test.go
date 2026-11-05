//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCsvToJsonCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "csv_to_json/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "csv_to_json", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["text"] != "2 CSV row(s), columns: count, name" || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	rows := d["rows"].([]any)
	if rows[0].(map[string]any)["name"] != "ana" || rows[1].(map[string]any)["count"] != "3" {
		t.Fatalf("rows: %v", rows)
	}
	v, _ = scCall(t, tb, "csv_to_json", map[string]any{"text": "name,name\nana,bruno"})
	if scOK(v) {
		t.Fatal("duplicate headers accepted")
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "csv_to_json", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "csv_to_json", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
