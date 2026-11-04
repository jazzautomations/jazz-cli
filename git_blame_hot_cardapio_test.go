//go:build !windows

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestGitBlameHotCardapio runs jazz-scripts/git_blame_hot.py through the script
// tool against a file with a known authorship: five lines by ana, then line 2
// and 4 rewritten by bruno, then line 3 rewritten by dev.
func TestGitBlameHotCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	scNeedGit(t)
	g := t.TempDir()
	tg := NewToolbox(g)
	tg.Scripts = true
	defer tg.Shell.Close()
	scGit(t, g, "init", "-q")
	commit := func(who, msg, content string) {
		scWrite(t, g, "f.go", content)
		scGit(t, g, "add", "--", "f.go")
		scGit(t, g, "-c", "user.name="+who, "commit", "-q", "-m", msg)
	}
	commit("ana", "c1", "a\nb\nc\nd\ne\n")
	commit("bruno", "c2", "a\nB\nc\nD\ne\n")
	commit("dev", "c3", "a\nB\nC\nD\ne\n")

	v, out := scCall(t, tg, "git_blame_hot", map[string]any{"path": "f.go"})
	d := scDataOf(v)
	if !scOK(v) || d["file"] != "f.go" || d["lines"] != 5.0 || d["commits"] != 3.0 || d["recent_commits"] != 3.0 || d["truncated"] != false {
		t.Fatalf("git_blame_hot: %v\n%s", v, out.Text)
	}
	authors := d["authors"].([]any)
	if len(authors) != 3 {
		t.Fatalf("authors = %v", authors)
	}
	// deterministic order: lines desc, then author name (ana and bruno tie at 2)
	var order []string
	for _, a := range authors {
		m := a.(map[string]any)
		order = append(order, fmt.Sprintf("%s:%v:%v", m["author"], m["lines"], m["pct"]))
	}
	if got := strings.Join(order, " "); got != "ana:2:40 bruno:2:40 dev:1:20" {
		t.Errorf("author order: %s", got)
	}
	lc := d["last_change"].(map[string]any)
	if lc["author"] != "dev" || len(fmt.Sprint(lc["commit"])) != 12 || len(fmt.Sprint(lc["date"])) != 10 {
		t.Errorf("last_change: %v", lc)
	}
	hot := d["hot_lines"].([]any)
	if len(hot) != 5 {
		t.Fatalf("hot_lines = %v", hot)
	}
	if h := hot[0].(map[string]any); h["line"] != 1.0 || h["commits"] != 3.0 {
		t.Errorf("hottest line: %v", h)
	}
	if txt, _ := d["text"].(string); !strings.Contains(txt, "5 lines, top author ana (40.0%)") || !strings.Contains(txt, "by dev") {
		t.Errorf("text: %q", txt)
	}

	// a line range: only bruno and dev own lines 2..3
	v, _ = scCall(t, tg, "git_blame_hot", map[string]any{"path": "f.go", "start": 2, "end": 3, "n": 10})
	d = scDataOf(v)
	rng, _ := d["range"].(map[string]any)
	if !scOK(v) || d["lines"] != 2.0 || d["commits"] != 2.0 || rng["start"] != 2.0 || rng["end"] != 3.0 {
		t.Fatalf("range: %v", v)
	}
	if a := d["authors"].([]any); len(a) != 2 || a[0].(map[string]any)["author"] != "bruno" || a[0].(map[string]any)["pct"] != 50.0 {
		t.Errorf("range authors: %v", a)
	}

	// an untracked file fails closed
	scWrite(t, g, "u.txt", "u\n")
	v, _ = scCall(t, tg, "git_blame_hot", map[string]any{"path": "u.txt"})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not tracked") {
		t.Errorf("untracked: %v", v)
	}

	// path is required: the registry rejects the call before the script runs
	if o := tg.Exec(context.Background(), "script", `{"name":"git_blame_hot","args":{}}`); !o.IsErr || !strings.Contains(o.Text, `missing required argument "path"`) {
		t.Errorf("missing path: %+v", o)
	}

	// outside a repository: fails closed
	scWrite(t, tb.Root, "f.go", "x\n")
	v, _ = scCall(t, tb, "git_blame_hot", map[string]any{"path": "f.go"})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not a git repository") {
		t.Errorf("not a repo: %v", v)
	}
}
