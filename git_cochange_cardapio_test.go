//go:build !windows

package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestGitCochangeCardapio runs jazz-scripts/git_cochange.py through the script
// tool (same path the agent uses) against a small repository with a known
// history: a.go+b.go, a.go+b.go+c.go, a.go, c.go+d.go.
func TestGitCochangeCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	scNeedGit(t)
	g := t.TempDir()
	tg := NewToolbox(g)
	tg.Scripts = true
	defer tg.Shell.Close()
	scGit(t, g, "init", "-q")
	commit := func(msg string, files ...string) {
		for _, f := range files {
			scWrite(t, g, f, msg+" "+f+"\n")
		}
		scGit(t, g, append([]string{"add", "--"}, files...)...)
		scGit(t, g, "commit", "-q", "-m", msg)
	}
	commit("c1", "a.go", "b.go")
	commit("c2", "a.go", "b.go", "c.go")
	commit("c3", "a.go")
	commit("c4", "c.go", "d.go")

	v, out := scCall(t, tg, "git_cochange", nil)
	d := scDataOf(v)
	if !scOK(v) || d["commits"] != 4.0 || d["files"] != 4.0 || d["truncated"] != false {
		t.Fatalf("git_cochange: %v\n%s", v, out.Text)
	}
	churn := d["churn"].([]any)
	if top := churn[0].(map[string]any); top["file"] != "a.go" || top["commits"] != 3.0 {
		t.Errorf("churn top: %v", churn)
	}
	pairs := d["cochange"].([]any)
	if len(pairs) != 4 {
		t.Fatalf("pairs = %d, want 4: %v", len(pairs), pairs)
	}
	if p := pairs[0].(map[string]any); p["a"] != "a.go" || p["b"] != "b.go" || p["count"] != 2.0 || p["support"] != 1.0 {
		t.Errorf("strongest pair: %v", p)
	}
	// deterministic order: count desc, then support desc (c+d: 1/min(2,1)=1.0 beats a+c: 1/min(3,2)=0.5), then a, b
	var order []string
	for _, p := range pairs {
		m := p.(map[string]any)
		order = append(order, fmt.Sprintf("%s+%s:%v", m["a"], m["b"], m["count"]))
	}
	if got := strings.Join(order, " "); got != "a.go+b.go:2 c.go+d.go:1 a.go+c.go:1 b.go+c.go:1" {
		t.Errorf("pair order: %s", got)
	}
	if txt, _ := d["text"].(string); !strings.Contains(txt, "4 commits") || !strings.Contains(txt, "a.go (3)") {
		t.Errorf("text: %q", txt)
	}
	if ms, ok := d["ms"].(float64); !ok || ms < 0 {
		t.Errorf("ms: %v", d["ms"])
	}

	// seeds: only pairs involving c.go, matched by basename or project path
	v, _ = scCall(t, tg, "git_cochange", map[string]any{"files": []string{"./c.go"}, "n": 2000, "limit": 10})
	d = scDataOf(v)
	pairs = d["cochange"].([]any)
	if !scOK(v) || len(pairs) != 3 {
		t.Fatalf("seeded pairs: %v", v)
	}
	for _, p := range pairs {
		m := p.(map[string]any)
		if m["a"] != "c.go" && m["b"] != "c.go" {
			t.Errorf("pair without the seed: %v", m)
		}
	}
	if sf := d["seed_files"].([]any); len(sf) != 1 || sf[0].(map[string]any)["commits"] != 2.0 {
		t.Errorf("seed_files: %v", d["seed_files"])
	}

	// n limits the window
	v, _ = scCall(t, tg, "git_cochange", map[string]any{"n": 1})
	if d = scDataOf(v); d["commits"] != 1.0 || d["files"] != 2.0 {
		t.Errorf("n=1: %v", d)
	}

	// outside a repository: fails closed
	v, _ = scCall(t, tb, "git_cochange", nil)
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not a git repository") {
		t.Errorf("not a repo: %v", v)
	}
}
