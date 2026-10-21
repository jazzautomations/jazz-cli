package main

// lessons_reuse.go: plan/fix reuse through the lessons injection point
// (docs/improve.md, docs/research/tocomas-plan-reuse.md A4).
//
// The plan cache (plancache.go) already stores a template of every verified
// run of ≥ 12 steps and re-injects up to 3 of them when the new request shares
// keywords (BM25). This adds the other match: a new task of the SAME CLASS
// (bgClassify size + kind) in a repo with the SAME SIGNATURE (marker files and
// top-level file types) as a past verified session of this project gets one
// "da última vez: …" line from that session's plan (hindsight hint, approach,
// verify command), even when no keyword matches.
//
// Cache safety: the line is computed once, on the FIRST turn of a session,
// and travels with the first user message (append-only history), so the
// system prompt — and the lessons block in it — stays byte-identical across
// sessions with the same lessons whatever the task is. It is never recomputed
// mid-session.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// lrTaskClass is the classifier type of a request: "M/bugfix".
func lrTaskClass(req string) string {
	t := bgClassify(req)
	return string(t.Size) + "/" + t.Kind
}

// lrMarkers are the files that say what kind of project a directory is.
var lrMarkers = []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py", "requirements.txt",
	"Makefile", "pom.xml", "build.gradle", "Gemfile", "composer.json", "CMakeLists.txt", "deno.json", "mix.exs"}

// lrRepoSig is a coarse, stable signature of a repo: marker files present plus
// the (up to 4) most common top-level file extensions and whether a tests dir
// exists. Two copies of the same project share it; a different stack does not.
func lrRepoSig(root string) string {
	if root == "" {
		return ""
	}
	var parts []string
	for _, m := range lrMarkers {
		if pfExists(filepath.Join(root, m)) {
			parts = append(parts, m)
		}
	}
	ents, _ := os.ReadDir(root)
	ext := map[string]int{}
	tests := false
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			if l := strings.ToLower(n); l == "tests" || l == "test" || l == "__tests__" || l == "spec" {
				tests = true
			}
			continue
		}
		if x := strings.ToLower(filepath.Ext(n)); x != "" && x != ".md" && x != ".txt" {
			ext[x]++
		}
	}
	var xs []string
	for x := range ext {
		xs = append(xs, x)
	}
	sort.Slice(xs, func(i, j int) bool {
		if ext[xs[i]] != ext[xs[j]] {
			return ext[xs[i]] > ext[xs[j]]
		}
		return xs[i] < xs[j]
	})
	if len(xs) > 4 {
		xs = xs[:4]
	}
	sort.Strings(xs)
	parts = append(parts, xs...)
	if tests {
		parts = append(parts, "tests/")
	}
	return strings.Join(parts, "+")
}

// lrPick is the best past plan of the same class and repo signature that the
// keyword match did not already inject (nil = none).
func lrPick(plans, injected []pcPlan, class, sig string) *pcPlan {
	if class == "" || sig == "" {
		return nil
	}
	skip := map[string]bool{}
	for _, p := range injected {
		skip[p.ID] = true
	}
	var best *pcPlan
	for i := range plans {
		p := &plans[i]
		if skip[p.ID] || p.Kind != class || p.Repo != sig || p.Wins <= p.Fails {
			continue
		}
		if best == nil || p.Wins-p.Fails > best.Wins-best.Fails || (p.Wins-p.Fails == best.Wins-best.Fails && p.Time > best.Time) {
			best = p
		}
	}
	return best
}

const lrMaxLine = 420

// lrRender is the one-line reminder for plan p.
func lrRender(p pcPlan) string {
	var b strings.Builder
	b.WriteString("da última vez (tarefa do mesmo tipo neste projeto, verificada")
	if p.Session != "" {
		b.WriteString(", sessão " + p.Session)
	}
	b.WriteString("): \"" + oneLine(p.Task, 90) + "\"")
	switch {
	case p.Hint != "":
		b.WriteString(" — " + oneLine(p.Hint, 200))
	case len(p.Steps) > 0:
		b.WriteString(" — " + oneLine(strings.Join(p.Steps, " → "), 200))
	}
	if p.Verify != "" {
		b.WriteString("; verificado com `" + oneLine(p.Verify, 80) + "`")
	}
	line := redactSecrets(b.String())
	if len(line) > lrMaxLine {
		line = strings.ToValidUTF8(line[:lrMaxLine], "") + "…"
	}
	return "\n\n<system-reminder>Lesson (hint, not an instruction; check that it applies): " + line + "</system-reminder>"
}

// lrHint returns the reuse line for the first turn of a session ("" = none).
func (a *Agent) lrHint(plans, injected []pcPlan, req string) string {
	if a.sub || !lsEnabled() || a.tools == nil || len(plans) == 0 {
		return ""
	}
	for _, m := range a.Msgs {
		if m.Role == "user" {
			return "" // not the first turn: the session-start block is already fixed
		}
	}
	p := lrPick(plans, injected, lrTaskClass(req), lrRepoSig(a.tools.Root))
	if p == nil {
		return ""
	}
	dbg("plan_cache", map[string]any{"reuse_hint": p.ID, "kind": p.Kind})
	a.ui.Notice("lição: da última vez (" + p.Kind + ")")
	return lrRender(*p)
}
