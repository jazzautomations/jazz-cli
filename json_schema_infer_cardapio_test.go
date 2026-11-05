//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJsonSchemaInferCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "input.txt", scFixture(t, "json_schema_infer/input.txt"))
	scWrite(t, root, "ana.txt", "ana\n")
	v, _ := scCall(t, tb, "json_schema_infer", map[string]any{"file": "input.txt"})
	d := scDataOf(v)
	if !scOK(v) || d["text"] != "Inferred object schema from observed values" || d["truncated"] != false {
		t.Fatalf("result: %v", v)
	}
	schema := d["schema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if props["count"].(map[string]any)["type"] != "integer" || props["name"].(map[string]any)["type"] != "string" {
		t.Fatalf("schema: %v", schema)
	}
	v, _ = scCall(t, tb, "json_schema_infer", map[string]any{"text": "[true, null, 2]"})
	schema = scDataOf(v)["schema"].(map[string]any)
	if len(schema["items"].(map[string]any)["anyOf"].([]any)) != 3 {
		t.Fatalf("union: %v", v)
	}
	v, _ = scCall(t, tb, "json_schema_infer", map[string]any{"text": "{"})
	if scOK(v) {
		t.Fatal("invalid JSON accepted")
	}
	before, err := os.ReadFile(filepath.Join(root, "ana.txt"))
	if err != nil || string(before) != "ana\n" {
		t.Fatalf("target modified: %q, %v", before, err)
	}
	v, _ = scCall(t, tb, "json_schema_infer", map[string]any{"file": "missing.txt"})
	if scOK(v) {
		t.Fatalf("missing input accepted: %v", v)
	}
	v, _ = scCall(t, tb, "json_schema_infer", map[string]any{"file": "../outside.txt"})
	if scOK(v) {
		t.Fatalf("outside path accepted: %v", v)
	}
	v, _ = scCall(t, tb, "json_schema_infer", map[string]any{"text": "[NaN]"})
	if scOK(v) {
		t.Fatal("non-finite JSON accepted")
	}
}
