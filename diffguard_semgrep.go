package main

// diffguard_semgrep.go: optional semgrep pass of the diff review. When the
// semgrep binary is on PATH it runs the public security-audit and secrets
// packs over the files the turn changed (60 s cap) and keeps the results that
// fall on added lines: ERROR -> CRITICAL, WARNING -> HIGH, INFO -> MEDIUM.
// Offline or without cached packs semgrep cannot fetch its rules: one notice,
// then the pass is skipped (the built-in rules already ran).

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	dgSemgrepTimeout = 60 * time.Second
	dgSemgrepNotice  sync.Once
)

type dgSemgrepOut struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		Extra struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Lines    string `json:"lines"`
		} `json:"extra"`
	} `json:"results"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// dgSemgrepSev maps semgrep severities to the engine's.
func dgSemgrepSev(s string) string {
	switch strings.ToUpper(s) {
	case "ERROR":
		return "CRITICAL"
	case "WARNING":
		return "HIGH"
	}
	return "MEDIUM"
}

func dgSemgrepPath() string {
	p, err := lookPath("semgrep")
	if err != nil {
		return ""
	}
	return p
}

// dgSemgrepFindings runs semgrep on the changed project files; nil when it is
// not installed, off, or could not run.
func (a *Agent) dgSemgrepFindings(ctx context.Context, files []dgFile) []dgFinding {
	bin := dgSemgrepPath()
	if bin == "" || ctx.Err() != nil {
		return nil
	}
	out, err := dgRunSemgrep(ctx, bin, a.tools.Root, files)
	if err != nil {
		dgSemgrepNotice.Do(func() { a.ui.Notice("semgrep: " + err.Error()) })
		return nil
	}
	return out
}

func dgRunSemgrep(ctx context.Context, bin, root string, files []dgFile) ([]dgFinding, error) {
	added := map[string]map[int]bool{}
	text := map[string][]string{}
	var args []string
	for _, f := range files {
		if f.Outside || f.Deleted {
			continue
		}
		added[filepath.ToSlash(f.Path)] = qgAddedLines(f.Old, f.New)
		text[filepath.ToSlash(f.Path)] = strings.Split(f.New, "\n")
		args = append(args, filepath.FromSlash(f.Path))
	}
	if len(args) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, dgSemgrepTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"--config", "p/security-audit", "--config", "p/secrets", "--json", "--metrics=off", "--quiet", "--"}, args...)...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var res dgSemgrepOut
	jerr := json.Unmarshal(stdout.Bytes(), &res)
	if ctx.Err() != nil {
		return nil, dgErr("passou de 60 s, pulei")
	}
	if jerr != nil || runErr != nil && len(res.Results) == 0 {
		return nil, dgErr("não rodou (sem rede e sem pacotes de regras em cache?); pulei. Rode uma vez com rede: semgrep --config p/security-audit --config p/secrets .")
	}
	for _, e := range res.Errors {
		if len(res.Results) == 0 && (strings.Contains(e.Message, "download") || strings.Contains(e.Message, "fetch") || strings.Contains(e.Type, "Fetch")) {
			return nil, dgErr("sem rede para baixar os pacotes de regras; pulei. Rode uma vez com rede para guardar em cache")
		}
	}
	var out []dgFinding
	for _, r := range res.Results {
		p := filepath.ToSlash(filepath.Clean(r.Path))
		lines, ok := added[p]
		if !ok || !lines[r.Start.Line] {
			continue // only what this turn wrote
		}
		id := "semgrep:" + r.CheckID
		src := r.Extra.Lines // recent semgrep answers "requires login": read the line from the file
		if ls := text[p]; r.Start.Line <= len(ls) && (src == "" || src == "requires login") {
			src = ls[r.Start.Line-1]
		}
		out = append(out, dgFinding{Rule: id, Severity: dgSemgrepSev(r.Extra.Severity), File: p, Line: r.Start.Line,
			Hash: dgHash(id, p, src), Message: oneLine(r.Extra.Message, 240), Text: dgShow(src),
			Hunk: []string{"+ " + itoa(r.Start.Line) + ": " + dgShow(src)}})
	}
	return out, nil
}

type dgErr string

func (e dgErr) Error() string { return string(e) }
