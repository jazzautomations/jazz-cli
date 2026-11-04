//go:build !windows

package main

import (
	"strings"
	"testing"
)

// TestDiskUsageCardapio runs the disk_usage offline script through the same
// path jazz uses and checks the statvfs totals plus the biggest dirs/files.
func TestDiskUsageCardapio(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is missing

	scWrite(t, root, "a/big.bin", strings.Repeat("x", 20000))
	scWrite(t, root, "a/b/mid.bin", strings.Repeat("y", 5000))
	scWrite(t, root, "c/small.txt", "hello")

	v, _ := scCall(t, tb, "disk_usage", map[string]any{"path": ".", "top": 10})
	if !scOK(v) {
		t.Fatalf("disk_usage not ok: %v", v)
	}
	d := scDataOf(v)

	fs, _ := d["fs"].(map[string]any)
	if fs == nil {
		t.Fatalf("no fs block: %v", d)
	}
	if total, _ := fs["total"].(float64); total <= 0 {
		t.Errorf("fs.total = %v, want > 0", fs["total"])
	}
	if used, _ := fs["used"].(float64); used < 0 {
		t.Errorf("fs.used = %v", fs["used"])
	}

	if sc, _ := d["scanned"].(float64); int(sc) < 3 {
		t.Errorf("scanned = %v, want >= 3", d["scanned"])
	}

	dirs, _ := d["dirs"].([]any)
	if len(dirs) == 0 {
		t.Fatalf("no dirs returned: %v", d)
	}
	// dirs must be sorted by bytes, descending.
	prev := -1.0
	for _, e := range dirs {
		m, _ := e.(map[string]any)
		b, _ := m["bytes"].(float64)
		if prev >= 0 && b > prev {
			t.Errorf("dirs not sorted descending: %v", dirs)
			break
		}
		prev = b
	}

	files, _ := d["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("no files returned: %v", d)
	}
	top, _ := files[0].(map[string]any)
	if p, _ := top["path"].(string); !strings.Contains(p, "big.bin") {
		t.Errorf("biggest file = %v, want a/big.bin", top)
	}
	if b, _ := top["bytes"].(float64); int(b) != 20000 {
		t.Errorf("biggest file bytes = %v, want 20000", top["bytes"])
	}
}
