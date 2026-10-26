//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTermuxHomeToolsAndFenceUseProcessHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PREFIX", "/data/data/com.termux/files/usr")
	t.Setenv("TERMUX_VERSION", "0.118")
	t.Setenv("JAZZ_HOME", t.TempDir())

	root := t.TempDir()
	tb := NewToolbox(root)
	defer tb.Shell.Close()

	written := tb.write(map[string]any{"path": "~/termux-check.txt", "content": "before\n"}, "")
	if written.IsErr {
		t.Fatalf("write to ~/ failed: %s", written.Text)
	}
	path := filepath.Join(home, "termux-check.txt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("write escaped process HOME: %v", err)
	}

	read := tb.read(context.Background(), map[string]any{"path": "~/termux-check.txt"}, "")
	if read.IsErr || !strings.Contains(read.Text, "before") {
		t.Fatalf("read from ~/ failed or returned wrong content: %+v", read)
	}
	edited := tb.edit(map[string]any{"path": "~/termux-check.txt", "old_string": "before", "new_string": "after"}, "")
	if edited.IsErr {
		t.Fatalf("edit under ~/ failed: %s", edited.Text)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("edit path = %q, err=%v", data, err)
	}

	a := &Agent{tools: &Toolbox{Root: root}}
	if got := a.fenceExpand("~/termux-check.txt"); got != path {
		t.Fatalf("fence expanded ~/ to %q, want process HOME path %q", got, path)
	}
}
