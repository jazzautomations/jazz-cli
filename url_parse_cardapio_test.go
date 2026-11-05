//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUrlParseCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "url_parse/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "url_parse", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["text"] != "1 URL(s) parsed: /docs" || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	item := d["items"].([]any)[0].(map[string]any)
	if item["host"] != nil || item["port"] != nil || item["fragment"] != "intro" || len(item["query"].([]any)) != 2 {
		t.Fatalf("URL components: %v", item)
	}
	v, _ = scCall(t, tb, "url_parse", map[string]any{"text": "//["})
	if scOK(v) {
		t.Fatal("invalid URL accepted")
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "url_parse", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "url_parse", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
}
