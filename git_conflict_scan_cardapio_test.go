//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestGitConflictScanCardapio runs jazz-scripts/git_conflict_scan.py through the
// script tool: a clean tree, then leftover markers in a file (while a bare
// "=======" underline, a vendored file and a binary are ignored), then a real
// unmerged path in the index after a failed merge.
func TestGitConflictScanCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	scNeedGit(t)
	g := t.TempDir()
	tg := NewToolbox(g)
	tg.Scripts = true
	defer tg.Shell.Close()
	scGit(t, g, "init", "-q")
	scWrite(t, g, "a.txt", "base\n")
	scGit(t, g, "add", "--", "a.txt")
	scGit(t, g, "commit", "-q", "-m", "c1")

	v, out := scCall(t, tg, "git_conflict_scan", nil)
	d := scDataOf(v)
	if !scOK(v) || d["clean"] != true || d["count"] != 0.0 || d["text"] != "clean" || d["files_scanned"] != 1.0 {
		t.Fatalf("clean tree: %v\n%s", v, out.Text)
	}

	// markers in the working tree; look-alikes are not counted
	scWrite(t, g, "z.txt", "x\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> feature\n")
	scWrite(t, g, "doc.rst", "Title\n=======\n\ntext\n")
	scWrite(t, g, "node_modules/m.txt", "<<<<<<< a\n=======\n>>>>>>> b\n")
	scWrite(t, g, "bin.dat", "<<<<<<< a\n\x00=======\n>>>>>>> b\n")
	v, _ = scCall(t, tg, "git_conflict_scan", map[string]any{"path": "."})
	d = scDataOf(v)
	if !scOK(v) || d["clean"] != false || d["count"] != 3.0 || d["files_with_markers"] != 1.0 || d["truncated"] != false {
		t.Fatalf("markers: %v", v)
	}
	if ix := d["in_index"].([]any); len(ix) != 0 {
		t.Errorf("in_index: %v", ix)
	}
	var order []string
	for _, h := range d["in_files"].([]any) {
		m := h.(map[string]any)
		order = append(order, fmt.Sprintf("%s:%v:%s", m["file"], m["line"], m["marker"]))
	}
	if got := strings.Join(order, " "); got != "z.txt:2:<<<<<<< z.txt:4:======= z.txt:6:>>>>>>>" {
		t.Errorf("markers order: %s", got)
	}
	if d["text"] != "3 conflict markers in 1 file" {
		t.Errorf("text: %q", d["text"])
	}

	// limit truncates the rows, count stays total
	v, _ = scCall(t, tg, "git_conflict_scan", map[string]any{"limit": 1})
	d = scDataOf(v)
	if !scOK(v) || len(d["in_files"].([]any)) != 1 || d["count"] != 3.0 || d["truncated"] != true {
		t.Errorf("limit: %v", d)
	}

	// a real conflict: both sides change a.txt, the merge stops with a.txt unmerged
	scGit(t, g, "checkout", "-q", "-b", "side")
	scWrite(t, g, "a.txt", "left\n")
	scGit(t, g, "commit", "-q", "-am", "left")
	scGit(t, g, "checkout", "-q", "main")
	scWrite(t, g, "a.txt", "right\n")
	scGit(t, g, "commit", "-q", "-am", "right")
	merge := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "merge", "side")
	merge.Dir = g
	if err := merge.Run(); err == nil {
		t.Fatal("merge should have conflicted")
	}
	v, _ = scCall(t, tg, "git_conflict_scan", nil)
	d = scDataOf(v)
	if !scOK(v) || fmt.Sprint(d["in_index"]) != "[a.txt]" || d["clean"] != false || d["files_with_markers"] != 2.0 || d["count"] != 6.0 {
		t.Fatalf("index conflict: %v", v)
	}
	if d["text"] != "6 conflict markers in 2 files; 1 file unmerged in the index" {
		t.Errorf("text: %q", d["text"])
	}

	// a single file as path: only that file is scanned, the index filter follows it
	v, _ = scCall(t, tg, "git_conflict_scan", map[string]any{"path": "a.txt"})
	d = scDataOf(v)
	if !scOK(v) || d["files_scanned"] != 1.0 || d["count"] != 3.0 || fmt.Sprint(d["in_index"]) != "[a.txt]" {
		t.Errorf("single file: %v", d)
	}

	// outside a repository: fails closed
	v, _ = scCall(t, tb, "git_conflict_scan", nil)
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not a git repository") {
		t.Errorf("not a repo: %v", v)
	}
}
