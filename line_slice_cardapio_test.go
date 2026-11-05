//go:build !windows

package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestLineSliceCardapio runs jazz-scripts/line_slice.py through the script tool:
// tail, ranges, single lines, out-of-bounds and arg-shape refusals.
func TestLineSliceCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	scWrite(t, root, "logs/app.log", b.String())

	// tail returns the last N lines with real numbers
	v, out := scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "tail": 3})
	d := scDataOf(v)
	if !scOK(v) || d["shown"] != 3.0 || d["total_lines"] != 20.0 {
		t.Fatalf("tail: %v\n%s", v, out.Text)
	}
	lines := d["lines"].([]any)
	if lines[0].(map[string]any)["line"] != 18.0 || lines[2].(map[string]any)["text"] != "line-20" {
		t.Errorf("tail lines: %v", lines)
	}
	if txt, _ := d["text"].(string); !strings.Contains(txt, "last 3") {
		t.Errorf("text: %q", txt)
	}

	// start/end is an inclusive 1-based range; start alone is one line
	v, _ = scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "start": 2, "end": 4})
	d = scDataOf(v)
	lines = d["lines"].([]any)
	if !scOK(v) || len(lines) != 3 || lines[0].(map[string]any)["line"] != 2.0 || lines[2].(map[string]any)["text"] != "line-04" {
		t.Errorf("range: %v", lines)
	}
	v, _ = scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "start": 7})
	if d = scDataOf(v); !scOK(v) || d["shown"] != 1.0 || d["lines"].([]any)[0].(map[string]any)["text"] != "line-07" {
		t.Errorf("single line: %v", v)
	}

	// end past the file is clamped; start past the file is a clean empty answer
	v, _ = scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "start": 19, "end": 99})
	if d = scDataOf(v); !scOK(v) || d["shown"] != 2.0 || d["end"] != 20.0 {
		t.Errorf("clamp: %v", v)
	}
	v, _ = scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "start": 99})
	if d = scDataOf(v); !scOK(v) || d["shown"] != 0.0 || !strings.Contains(d["text"].(string), "past the end") {
		t.Errorf("past end: %v", v)
	}

	// tail bigger than the file gives the whole file
	v, _ = scCall(t, tb, "line_slice", map[string]any{"file": "logs/app.log", "tail": 99})
	if d = scDataOf(v); !scOK(v) || d["shown"] != 20.0 || d["start"] != 1.0 {
		t.Errorf("big tail: %v", v)
	}

	// refusals: conflicting shape, no selector, zero/inverted ranges, missing file
	for _, args := range []map[string]any{
		{"file": "logs/app.log", "tail": 2, "start": 1},
		{"file": "logs/app.log"},
		{"file": "logs/app.log", "start": 0},
		{"file": "logs/app.log", "end": 0},
		{"file": "logs/app.log", "start": 9, "end": 3},
		{"file": "logs/app.log", "tail": 0},
		{"file": "logs/nope.log", "tail": 5},
		{"file": "../outside.log", "tail": 5},
		{"tail": 2, "cmd": "cat x"},
	} {
		if v, _ = scCall(t, tb, "line_slice", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
