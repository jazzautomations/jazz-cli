//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestGitBranchStatusCardapio runs jazz-scripts/git_branch_status.py through the
// script tool against a repository with a local "origin" (a bare repo on disk, so
// no network): main ahead 1 / behind 1 of origin/main, feature (unmerged),
// old-merged (merged, older tip), gone-b (upstream gone) and stale-x (committed
// in 2020, unmerged).
func TestGitBranchStatusCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	scNeedGit(t)
	g := t.TempDir()
	bare := t.TempDir()
	tg := NewToolbox(g)
	tg.Scripts = true
	defer tg.Shell.Close()
	commit := func(msg, file string, extra ...string) {
		scWrite(t, g, file, msg+"\n")
		scGit(t, g, "add", "--", file)
		scGit(t, g, append(extra, "commit", "-q", "-m", msg)...)
	}
	scGit(t, g, "init", "-q")
	commit("c1", "a.txt")
	commit("c2", "a.txt")
	scGit(t, g, "branch", "old-merged", "HEAD~1")
	scGit(t, bare, "init", "-q", "--bare")
	scGit(t, g, "remote", "add", "origin", bare)
	scGit(t, g, "push", "-q", "origin", "main")
	scGit(t, g, "branch", "-q", "--set-upstream-to=origin/main", "main")
	scGit(t, g, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	scGit(t, g, "checkout", "-q", "-b", "feature")
	commit("c3", "b.txt")
	scGit(t, g, "push", "-q", "origin", "feature:main") // origin/main moves past main: behind 1
	scGit(t, g, "checkout", "-q", "main")
	commit("c4", "c.txt") // ahead 1
	scGit(t, g, "branch", "gone-b")
	scGit(t, g, "config", "branch.gone-b.remote", "origin")
	scGit(t, g, "config", "branch.gone-b.merge", "refs/heads/nope")
	scGit(t, g, "checkout", "-q", "-b", "stale-x", "main~1")
	scWrite(t, g, "s.txt", "s\n")
	scGit(t, g, "add", "--", "s.txt")
	old := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=ana", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "old", "--date=2020-01-01T00:00:00Z")
	old.Dir = g
	old.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z")
	if out, err := old.CombinedOutput(); err != nil {
		t.Fatalf("old commit: %v\n%s", err, out)
	}
	scGit(t, g, "checkout", "-q", "main")

	v, out := scCall(t, tg, "git_branch_status", nil)
	d := scDataOf(v)
	if !scOK(v) || d["current"] != "main" || d["default_branch"] != "main" || d["detached"] != false || d["upstream"] != "origin/main" {
		t.Fatalf("git_branch_status: %v\n%s", v, out.Text)
	}
	if d["ahead"] != 1.0 || d["behind"] != 1.0 || d["total"] != 5.0 || d["truncated"] != false {
		t.Errorf("head: ahead=%v behind=%v total=%v truncated=%v", d["ahead"], d["behind"], d["total"], d["truncated"])
	}
	if fmt.Sprint(d["unmerged"]) != "[feature stale-x]" || fmt.Sprint(d["stale"]) != "[stale-x]" || fmt.Sprint(d["gone"]) != "[gone-b]" {
		t.Errorf("lists: unmerged=%v stale=%v gone=%v", d["unmerged"], d["stale"], d["gone"])
	}
	rows := d["branches"].([]any)
	if len(rows) != 5 {
		t.Fatalf("branches = %v", rows)
	}
	// deterministic order: days desc, then name
	var order []string
	for _, r := range rows {
		m := r.(map[string]any)
		order = append(order, fmt.Sprintf("%s:%v", m["name"], m["merged"]))
	}
	if got := strings.Join(order, " "); got != "stale-x:false feature:false gone-b:true main:true old-merged:true" {
		t.Errorf("branch order: %s", got)
	}
	if s := rows[0].(map[string]any); s["days"].(float64) < 2000 || s["author"] != "ana" || s["upstream"] != nil {
		t.Errorf("stale row: %v", s)
	}
	for _, r := range rows {
		m := r.(map[string]any)
		switch m["name"] {
		case "main":
			if m["ahead"] != 1.0 || m["behind"] != 1.0 || m["upstream"] != "origin/main" || m["gone"] != false {
				t.Errorf("main row: %v", m)
			}
		case "gone-b":
			if m["gone"] != true || m["upstream"] != "origin/nope" {
				t.Errorf("gone row: %v", m)
			}
		}
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "on main (ahead 1, behind 1 of origin/main); 5 branches, 2 unmerged into main, 1 stale (>=30d), 1 upstream gone") {
		t.Errorf("text: %q", txt)
	}

	// limit truncates the rows, stale_days=0 marks every non-default branch stale
	v, _ = scCall(t, tg, "git_branch_status", map[string]any{"limit": 2, "stale_days": 0})
	d = scDataOf(v)
	if !scOK(v) || len(d["branches"].([]any)) != 2 || d["truncated"] != true || d["total"] != 5.0 || len(d["stale"].([]any)) != 4 {
		t.Errorf("limit/stale_days: %v", d)
	}

	// detached HEAD
	scGit(t, g, "checkout", "-q", "--detach")
	v, _ = scCall(t, tg, "git_branch_status", map[string]any{"path": "."})
	d = scDataOf(v)
	if !scOK(v) || d["detached"] != true || d["upstream"] != nil || d["ahead"] != 0.0 || !strings.HasPrefix(fmt.Sprint(d["text"]), "detached at ") {
		t.Errorf("detached: %v", d)
	}
	scGit(t, g, "checkout", "-q", "main")

	// outside a repository: fails closed
	v, _ = scCall(t, tb, "git_branch_status", nil)
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not a git repository") {
		t.Errorf("not a repo: %v", v)
	}
}
