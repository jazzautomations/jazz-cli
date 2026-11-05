//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestTextExtractCardapio runs jazz-scripts/text_extract.py through the script tool:
// named patterns pull typed values out of a file, custom regex works with
// ignore_case, unique collapses repeats, and bad input refuses honestly.
func TestTextExtractCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "logs/access.log", ""+
		"192.0.2.10 - - [01/Jan/2026:10:00:01 +0000] \"GET /a HTTP/1.1\" 200 10\n"+
		"192.0.2.11 - - [01/Jan/2026:10:00:03 +0000] \"GET /b HTTP/1.1\" 200 20\n"+
		"192.0.2.10 - - [01/Jan/2026:10:01:21 +0000] \"GET /c HTTP/1.1\" 404 9\n"+
		"mail ana@example.com and bruno@test.org\n")

	// ipv4 over the access log: 4 matches, 3 unique, line numbers kept
	v, out := scCall(t, tb, "text_extract", map[string]any{"pattern": "ipv4", "file": "logs/access.log"})
	d := scDataOf(v)
	if !scOK(v) || d["total"] != 4.0 || d["unique"] != 3.0 || d["lines_matched"] != 4.0 {
		t.Fatalf("ipv4: %v\n%s", v, out.Text)
	}
	m := d["matches"].([]any)
	if m[0].(map[string]any)["match"] != "192.0.2.10" || m[0].(map[string]any)["line"] != 1.0 {
		t.Errorf("first match: %v", m[0])
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "4 match(es) of pattern:ipv4") {
		t.Errorf("text: %q", txt)
	}

	// unique collapses to the distinct values in first-seen order
	v, _ = scCall(t, tb, "text_extract", map[string]any{"pattern": "ipv4", "file": "logs/access.log", "unique": true})
	d = scDataOf(v)
	m = d["matches"].([]any)
	if !scOK(v) || len(m) != 3 || m[0].(map[string]any)["match"] != "192.0.2.10" || m[2].(map[string]any)["match"] != "192.0.2.11" {
		t.Errorf("unique: %v", m)
	}
	if _, hasLine := m[0].(map[string]any)["line"]; hasLine {
		t.Errorf("unique output must not carry line numbers: %v", m[0])
	}

	// email and custom regex (with ignore_case) on the same file
	v, _ = scCall(t, tb, "text_extract", map[string]any{"pattern": "email", "file": "logs/access.log"})
	if d = scDataOf(v); !scOK(v) || d["unique"] != 2.0 {
		t.Errorf("email: %v", v)
	}
	v, _ = scCall(t, tb, "text_extract", map[string]any{"regex": `"[A-Z]+ /[a-z]`, "file": "logs/access.log"})
	if d = scDataOf(v); !scOK(v) || d["total"] != 3.0 || d["matches"].([]any)[0].(map[string]any)["match"] != `"GET /a` {
		t.Errorf("regex: %v", v)
	}

	// inline text works; a non-matching pattern is a clean zero, not an error
	v, _ = scCall(t, tb, "text_extract", map[string]any{"pattern": "url", "text": "see https://example.org/x and http://a.b/c"})
	if d = scDataOf(v); !scOK(v) || d["total"] != 2.0 || d["lines_scanned"] != 1.0 {
		t.Errorf("url inline: %v", v)
	}
	v, _ = scCall(t, tb, "text_extract", map[string]any{"pattern": "uuid", "file": "logs/access.log"})
	if d = scDataOf(v); !scOK(v) || d["total"] != 0.0 {
		t.Errorf("uuid none: %v", v)
	}

	// refusals: both/neither selector, unknown pattern, bad/long regex, missing file,
	// traversal and cmd input
	for _, args := range []map[string]any{
		{"file": "logs/access.log"},
		{"pattern": "ipv4", "regex": "x", "file": "logs/access.log"},
		{"pattern": "ip", "file": "logs/access.log"},
		{"regex": "[unclosed", "file": "logs/access.log"},
		{"regex": strings.Repeat("a", 513), "file": "logs/access.log"},
		{"pattern": "ipv4", "file": "nope.log"},
		{"pattern": "ipv4", "file": "../outside.log"},
		{"pattern": "ipv4", "cmd": "cat x"},
	} {
		if v, _ = scCall(t, tb, "text_extract", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
