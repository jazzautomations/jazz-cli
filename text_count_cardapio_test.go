//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestTextCountCardapio runs jazz-scripts/text_count.py through the script tool:
// literal counting counts occurrences not just matching lines, regex mode honors
// ignore_case, top_lines are the densest, and bad input refuses honestly.
func TestTextCountCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "logs/app.log", ""+
		"INFO boot\n"+
		"ERROR first\n"+
		"INFO mid ERROR second\n"+
		"ERROR third ERROR same line\n"+
		"INFO done\n")

	// literal count: 5 occurrences over 3 matching lines
	v, out := scCall(t, tb, "text_count", map[string]any{"pattern": "ERROR", "file": "logs/app.log"})
	d := scDataOf(v)
	if !scOK(v) || d["count"] != 5.0 || d["lines_matched"] != 3.0 || d["lines_scanned"] != 5.0 {
		t.Fatalf("count: %v\n%s", v, out.Text)
	}
	top := d["top_lines"].([]any)
	if top[0].(map[string]any)["line"] != 4.0 || top[0].(map[string]any)["matches"] != 2.0 {
		t.Errorf("top_lines: %v", top)
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "5 occurrence(s) of 'ERROR'") {
		t.Errorf("text: %q", txt)
	}

	// literal is case-sensitive by default; ignore_case fixes it
	v, _ = scCall(t, tb, "text_count", map[string]any{"pattern": "error", "file": "logs/app.log"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 0.0 {
		t.Errorf("case sensitive: %v", v)
	}
	v, _ = scCall(t, tb, "text_count", map[string]any{"pattern": "error", "file": "logs/app.log", "ignore_case": true})
	if d = scDataOf(v); !scOK(v) || d["count"] != 5.0 {
		t.Errorf("ignore_case: %v", v)
	}

	// regex mode: digits counted per occurrence
	scWrite(t, root, "logs/n.log", "a1 b22 c333\nnone\n")
	v, _ = scCall(t, tb, "text_count", map[string]any{"pattern": `\d+`, "file": "logs/n.log", "regex": true})
	if d = scDataOf(v); !scOK(v) || d["count"] != 3.0 || d["lines_matched"] != 1.0 {
		t.Errorf("regex: %v", v)
	}

	// no match is a clean zero
	v, _ = scCall(t, tb, "text_count", map[string]any{"pattern": "PANIC", "file": "logs/app.log"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 0.0 || d["lines_matched"] != 0.0 {
		t.Errorf("zero: %v", v)
	}

	// refusals: missing/empty/long pattern, bad regex, missing file, traversal, cmd
	for _, args := range []map[string]any{
		{"file": "logs/app.log"},
		{"pattern": "", "file": "logs/app.log"},
		{"pattern": strings.Repeat("a", 513), "file": "logs/app.log"},
		{"pattern": "[unclosed", "file": "logs/app.log", "regex": true},
		{"pattern": "x", "file": "logs/nope.log"},
		{"pattern": "x", "file": "../outside.log"},
		{"pattern": "x", "cmd": "cat x"},
	} {
		if v, _ = scCall(t, tb, "text_count", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
