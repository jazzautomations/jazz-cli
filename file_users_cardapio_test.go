//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// TestFileUsersCardapio runs jazz-scripts/file_users.py through the script tool: the
// test process itself holds the fixture file open, so the scan must find our own
// pid with the exact resolved path; a file nobody holds answers cleanly.
func TestFileUsersCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc on this system")
	}
	p := scWrite(t, root, "data/held.db", "row\n")
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	v, out := scCall(t, tb, "file_users", map[string]any{"path": "data/held.db"})
	d := scDataOf(v)
	if !scOK(v) || d["count"].(float64) < 1 {
		t.Fatalf("holders: %v\n%s", v, out.Text)
	}
	self := os.Getpid()
	found := false
	for _, h := range d["holders"].([]any) {
		m := h.(map[string]any)
		if m["pid"].(float64) == float64(self) {
			found = true
			if s, _ := m["comm"].(string); !strings.HasSuffix(s, "test") {
				t.Errorf("comm: %v", m["comm"])
			}
		}
	}
	if !found {
		t.Errorf("self pid %d not in holders: %v", self, d["holders"])
	}
	if txt, _ := d["text"].(string); !strings.Contains(txt, "holding data/held.db") {
		t.Errorf("text: %q", txt)
	}

	// nobody holds this file: count 0, still ok
	scWrite(t, root, "data/free.txt", "x\n")
	v, _ = scCall(t, tb, "file_users", map[string]any{"path": "data/free.txt"})
	if d = scDataOf(v); !scOK(v) || d["count"] != 0.0 {
		t.Errorf("free file: %v", v)
	}

	// refusals: missing arg, missing path, outside root
	for _, args := range []map[string]any{
		{},
		{"path": "data/nope"},
		{"path": ".."},
	} {
		if v, _ = scCall(t, tb, "file_users", args); scOK(v) {
			t.Errorf("%v must be refused: %v", args, v)
		}
	}
}
