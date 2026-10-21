package main

// uilint.go: the post-edit UI lint hook. After a successful write/edit of a
// UI file (the frontend-craft skill's globs) the harness runs the built-in
// ui_lint script (jazz-scripts/ui_lint.py, rules derived from the Impeccable
// detector, see docs/research/impeccable-frontend-craft.md) on that file and
// appends the findings to the tool result as a short <system-reminder>: at
// most uiLintMaxLines findings, errors first. A clean file adds nothing; a
// file that was dirty and is now clean gets one line; the same findings as
// the last run on that file get one line instead of the list again.
//
// It runs whether or not the script tool is exposed to the model (it needs
// python3). Off with --ui-lint off, JAZZ_UI_LINT=off or "ui_lint": "off".

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	uiLintMaxLines = 12               // findings shown in the tool result
	uiLintTimeout  = 15 * time.Second // one run on one file
)

// uiLintGlobs are the base-name globs of a UI file; they are the file trigger
// of the frontend-craft skill (skills/triggers.json, checked by a test).
var uiLintGlobs = []string{"*.tsx", "*.jsx", "*.vue", "*.svelte", "*.css", "*.scss", "*.html", "tailwind.config.*", "*.astro"}

// isUIFile reports whether p is a UI file the lint hook looks at.
func isUIFile(p string) bool {
	if p == "" {
		return false
	}
	base := path.Base(filepath.ToSlash(p))
	for _, g := range uiLintGlobs {
		if ok, _ := path.Match(g, strings.ToLower(base)); ok {
			return true
		}
	}
	return false
}

// pfUILintOn decides whether the hook runs: --ui-lint wins over JAZZ_UI_LINT,
// which wins over config "ui_lint"; default on.
func pfUILintOn(cfg *Config, flag string) bool {
	if strings.TrimSpace(flag) != "" {
		return !olOffWord(flag)
	}
	if e := strings.TrimSpace(os.Getenv("JAZZ_UI_LINT")); e != "" {
		return !olOffWord(e)
	}
	return cfg == nil || !olOffWord(cfg.UILint)
}

// uiLintFinding is one entry of ui_lint's data.findings.
type uiLintFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Msg      string `json:"msg"`
}

type uiLintResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Data  struct {
		Findings []uiLintFinding `json:"findings"`
		Summary  struct {
			Findings int `json:"findings"`
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
			Info     int `json:"info"`
		} `json:"summary"`
	} `json:"data"`
}

// uiLintRun runs ui_lint on the files (absolute or root-relative paths).
// ok=false means the script is unavailable or failed; nothing is reported then.
func (t *Toolbox) uiLintRun(ctx context.Context, files []string) (uiLintResult, bool) {
	var res uiLintResult
	r := t.scRegistry()
	d := r.get("ui_lint")
	if d == nil {
		return res, false
	}
	ctx, cancel := context.WithTimeout(ctx, uiLintTimeout)
	defer cancel()
	out := r.scExec(ctx, d, map[string]any{"files": files, "max": 60}, t.Root)
	if out.Err != nil || out.TimedOut || json.Unmarshal(out.Stdout, &res) != nil || !res.OK {
		dbg("ui_lint", map[string]any{"files": files, "err": fmt.Sprint(out.Err), "timeout": out.TimedOut, "exit": out.Exit, "error": res.Error})
		return res, false
	}
	return res, true
}

// uiLintNote returns the reminder to append to the result of tc ("" = none).
func (a *Agent) uiLintNote(ctx context.Context, tc ToolCall, out ToolOutput) string {
	if a.tools == nil || !a.tools.UILint || out.IsErr || ctx.Err() != nil {
		return ""
	}
	if n := CanonicalTool(tc.Name); n != "edit" && n != "write" {
		return ""
	}
	p := argPath(tc.Args)
	if !isUIFile(p) {
		return ""
	}
	abs := a.tools.abs(p)
	rel := filepath.ToSlash(a.tools.rel(abs))
	t0 := time.Now()
	res, ok := a.tools.uiLintRun(ctx, []string{abs})
	if !ok {
		return ""
	}
	fs := res.Data.Findings
	sum := res.Data.Summary
	h := sha256.New()
	for _, f := range fs {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", f.Rule, f.Severity, f.Msg)
	}
	key := ""
	if len(fs) > 0 {
		key = hex.EncodeToString(h.Sum(nil)[:8])
	}
	if a.uiLint == nil {
		a.uiLint = map[string]string{}
	}
	prev, seen := a.uiLint[abs]
	a.uiLint[abs] = key
	dbg("ui_lint", map[string]any{"file": rel, "findings": sum.Findings, "errors": sum.Errors, "warnings": sum.Warnings,
		"info": sum.Info, "ms": time.Since(t0).Milliseconds(), "repeat": seen && prev == key && key != ""})
	if len(fs) == 0 {
		a.ui.Notice("ui_lint: " + rel + " limpo")
		if seen && prev != "" {
			return "\n\n<system-reminder>ui_lint: " + rel + " is clean now.</system-reminder>"
		}
		return ""
	}
	a.ui.Notice(fmt.Sprintf("ui_lint: %s — %d achado(s) (%d erro(s), %d aviso(s), %d info)", rel, sum.Findings, sum.Errors, sum.Warnings, sum.Info))
	if seen && prev == key {
		return fmt.Sprintf("\n\n<system-reminder>ui_lint: %s still has the same %d finding(s) as after your last edit.</system-reminder>", rel, sum.Findings)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n<system-reminder>ui_lint (frontend-craft detector) on %s: %d error(s), %d warning(s), %d info.\n", rel, sum.Errors, sum.Warnings, sum.Info)
	for i, f := range fs {
		if i == uiLintMaxLines {
			fmt.Fprintf(&b, "- … %d more (script ui_lint lists them all)\n", sum.Findings-uiLintMaxLines)
			break
		}
		fmt.Fprintf(&b, "- %s:%d %s %s: %s\n", f.File, f.Line, f.Severity, f.Rule, oneLine(f.Msg, 160))
	}
	if len(fs) < sum.Findings && len(fs) <= uiLintMaxLines {
		fmt.Fprintf(&b, "- … %d more (script ui_lint lists them all)\n", sum.Findings-len(fs))
	}
	b.WriteString("Fix the real problems in your next edit batch. Keep one on purpose only with a one-line reason (or a \"ui-lint-disable <rule>\" comment in the file).</system-reminder>")
	return b.String()
}
