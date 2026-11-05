//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestEnvGetCardapio runs jazz-scripts/env_get.py through the script tool: list mode
// never prints values, a single name resolves to its value only when the name is
// not secret-looking, and secret names are reported set/unset with redacted:true.
func TestEnvGetCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	t.Setenv("JZ_TEST_PLAIN", "hello-plain")
	t.Setenv("JZ_TEST_API_SECRET", "supersecret-value")

	// list mode: names sorted, values never emitted, secret-named flagged
	v, out := scCall(t, tb, "env_get", map[string]any{})
	d := scDataOf(v)
	if !scOK(v) || d["count"].(float64) < 2 {
		t.Fatalf("list: %v\n%s", v, out.Text)
	}
	var sawPlain, sawSecret bool
	for _, e := range d["vars"].([]any) {
		m := e.(map[string]any)
		if _, leak := m["value"]; leak {
			t.Fatalf("list mode emitted a value: %v", m)
		}
		if m["name"] == "JZ_TEST_PLAIN" {
			sawPlain = true
		}
		if m["name"] == "JZ_TEST_API_SECRET" {
			sawSecret = true
			if m["redacted"] != true {
				t.Errorf("secret-named var not flagged redacted: %v", m)
			}
		}
	}
	// the harness scrubs secret-looking variables from a script's environment
	// before it runs: the secret may be absent (stronger) or listed redacted
	if !sawPlain {
		t.Errorf("plain test var missing from list: %v", d["vars"])
	}
	if sawSecret && d["secret_named"].(float64) < 1 {
		t.Errorf("secret_named count: %v", d)
	}

	// single-name lookup: plain var shows value
	v, _ = scCall(t, tb, "env_get", map[string]any{"name": "JZ_TEST_PLAIN"})
	d = scDataOf(v)
	if !scOK(v) || d["set"] != true || d["value"] != "hello-plain" || d["redacted"] != false {
		t.Errorf("plain lookup: %v", v)
	}

	// secret-looking name: set is reported, value is not
	v, _ = scCall(t, tb, "env_get", map[string]any{"name": "JZ_TEST_API_SECRET"})
	d = scDataOf(v)
	if !scOK(v) || (d["set"] == true && d["redacted"] != true) {
		t.Fatalf("secret lookup: %v", v)
	}
	if _, leak := d["value"]; leak {
		t.Fatalf("secret value leaked: %v", d)
	}
	if txt, _ := d["text"].(string); strings.Contains(txt, "supersecret-value") {
		t.Fatalf("secret value in text: %q", txt)
	}

	// unset var is a clean set:false; prefix filters; bad shapes refuse
	v, _ = scCall(t, tb, "env_get", map[string]any{"name": "JZ_TEST_NOPE"})
	if d = scDataOf(v); !scOK(v) || d["set"] != false {
		t.Errorf("unset: %v", v)
	}
	v, _ = scCall(t, tb, "env_get", map[string]any{"prefix": "JZ_TEST_"})
	if d = scDataOf(v); !scOK(v) || d["count"].(float64) < 1 { // the secret-named one is scrubbed by the harness
		t.Errorf("prefix: %v", v)
	}
	for _, args := range []map[string]any{
		{"name": "A", "prefix": "B"},
		{"name": "A=B"},
	} {
		if v, _ = scCall(t, tb, "env_get", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
