package main

// prefetch.go: lexical pre-fetch at the start of a task (--prefetch=grep,
// docs/research/grep-is-all-you-need.md rec. 2). When the first user message of
// a task names identifiers, paths or error strings, one rg run looks them up
// and the hits go in front of the message as a <system-reminder>, saving the
// first one to three "find it" steps. Default off until an A/B run says it
// helps. Never touches the system prompt (cache), never re-runs on later turns.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	acPrefetchTimeout  = 3 * time.Second
	acPrefetchMaxTerms = 6
	acPrefetchMaxPaths = 3
	acPrefetchMaxHits  = 12
	acPrefetchPerFile  = 3
	acPrefetchVague    = 40 // a term with more hits than this is not distinctive
)

// acPrefetchNote returns the reminder for this turn, or "" (flag off, not the
// first user turn of the task, subagent, nothing distinctive found).
func (a *Agent) acPrefetchNote(ctx context.Context, input string) string {
	if a.sub || opSettingsNow().Prefetch != "grep" || a.tools == nil {
		return ""
	}
	for _, m := range a.Msgs {
		if m.Role == "user" {
			return "" // not the first turn of this task
		}
	}
	t0 := time.Now()
	out := acPrefetch(ctx, a.tools.Root, input, 1000)
	dbg("prefetch", map[string]any{"ms": time.Since(t0).Milliseconds(), "bytes": len(out)})
	return out
}

type acPfRegexes struct {
	backtick, frame, fileLine, path, quoted, camel, mixed, snake, dotted *regexp.Regexp
}

var (
	acPfOnce sync.Once
	acPfRx   *acPfRegexes
)

func acPfRegex() *acPfRegexes {
	acPfOnce.Do(func() {
		c := regexp.MustCompile
		acPfRx = &acPfRegexes{
			backtick: c("`([^`\n]{3,80})`"),
			frame:    c(`File "([^"\n]+)", line \d+`),
			fileLine: c(`([\w./-]+\.[A-Za-z]{1,5}):\d+`),
			path:     c(`(?:^|[\s(\[<'"])((?:\.{0,2}/)?(?:[\w.-]+/)+[\w.-]+\.[A-Za-z0-9]{1,6}|[\w-]+\.(?:go|py|js|mjs|cjs|ts|tsx|jsx|rs|c|h|cc|cpp|hpp|java|kt|rb|php|cs|swift|lua|sh|toml|yaml|yml|json|sql|vue|svelte))\b`),
			quoted:   c(`"([^"\n]{8,80})"|'([^'\n]{8,80})'`),
			camel:    c(`\b[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]*)+\b`),
			mixed:    c(`\b[a-z][a-z0-9]*(?:[A-Z][a-z0-9]*)+\b`),
			snake:    c(`\b[a-zA-Z][a-zA-Z0-9]*(?:_[a-zA-Z0-9]+)+\b`),
			dotted:   c(`\b[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)+\(?`),
		}
	})
	return acPfRx
}

// acPrefetchStop are frequent words that look like identifiers but are not.
var acPrefetchStop = map[string]bool{
	"JavaScript": true, "TypeScript": true, "GitHub": true, "GitLab": true, "PowerShell": true, "YouTube": true,
	"OpenAI": true, "ChatGPT": true, "OpenRouter": true, "iPhone": true, "macOS": true, "iOS": true, "PostgreSQL": true,
	"MySQL": true, "MongoDB": true, "NodeJS": true, "README": true, "WhatsApp": true, "LinkedIn": true, "DeepSeek": true,
	"e.g": true, "i.e": true, "etc.": true, "node.js": true, "Node.js": true, "Next.js": true, "Vue.js": true,
}

// acPrefetchTerms picks search terms (most specific first) and path-like tokens.
func acPrefetchTerms(input string) (terms, paths []string) {
	rx := acPfRegex()
	seen := map[string]bool{}
	addPath := func(p string) {
		p = strings.Trim(p, `"'.,;:()[]<>`)
		if p == "" || seen["p:"+p] || len(paths) >= acPrefetchMaxPaths || strings.Contains(p, "://") {
			return
		}
		seen["p:"+p] = true
		paths = append(paths, p)
	}
	add := func(t string) {
		t = strings.TrimSpace(t)
		t = strings.TrimSuffix(t, "(")
		t = strings.TrimSuffix(t, "()")
		if len(t) < 4 || len(t) > 80 || seen[t] || acPrefetchStop[t] || len(terms) >= acPrefetchMaxTerms {
			return
		}
		if strings.Count(t, " ") > 4 {
			return // prose, not a code span
		}
		if strings.HasPrefix(t, "-") || strings.Contains(t, "://") {
			return // a flag or a URL
		}
		seen[t] = true
		terms = append(terms, t)
	}
	for _, m := range rx.frame.FindAllStringSubmatch(input, -1) {
		addPath(m[1])
	}
	for _, m := range rx.fileLine.FindAllStringSubmatch(input, -1) {
		addPath(m[1])
	}
	for _, m := range rx.backtick.FindAllStringSubmatch(input, -1) {
		s := strings.TrimSpace(m[1])
		if rx.path.MatchString(" "+s) && !strings.ContainsAny(s, " ()") {
			addPath(s)
			continue
		}
		add(s)
	}
	for _, m := range rx.path.FindAllStringSubmatch(input, -1) {
		addPath(m[1])
	}
	for _, m := range rx.quoted.FindAllStringSubmatch(input, -1) {
		add(m[1] + m[2])
	}
	for _, r := range []*regexp.Regexp{rx.camel, rx.mixed, rx.snake, rx.dotted} {
		for _, m := range r.FindAllString(input, -1) {
			if seen["p:"+m] || rx.path.MatchString(" "+m) && strings.Contains(m, ".") {
				continue // file names were taken as paths
			}
			add(m)
		}
	}
	return terms, paths
}

// acPrefetch searches root for the distinctive terms of input and returns a
// <system-reminder> of at most budgetTok tokens (~3.5 bytes each), or "".
func acPrefetch(ctx context.Context, root, input string, budgetTok int) string {
	terms, paths := acPrefetchTerms(input)
	if len(terms) == 0 && len(paths) == 0 {
		return ""
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		dbg("prefetch", map[string]any{"skip": "no rg"})
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, acPrefetchTimeout)
	defer cancel()
	budget := budgetTok * 7 / 2
	var lines []string

	// Paths: the file itself when it exists, else a file-name lookup.
	var globs []string
	for _, p := range paths {
		rel := strings.TrimPrefix(p, "./")
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, rel)
		}
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
				lines = append(lines, "file exists: "+r)
			}
			continue
		}
		globs = append(globs, "-g", "**/"+filepath.Base(rel))
	}
	if len(globs) > 0 {
		args := append([]string{"--files", "--color", "never"}, globs...)
		if out, err := acPfRun(ctx, rg, root, args); err == nil {
			n := 0
			for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
				if f != "" && n < 5 {
					lines = append(lines, "file: "+strings.TrimPrefix(f, "./"))
					n++
				}
			}
		}
	}

	if len(terms) > 0 {
		args := []string{"-n", "-F", "--no-heading", "--color", "never", "--max-count", "3", "--max-columns", "160", "--max-columns-preview", "--max-filesize", "1M"}
		for _, t := range terms {
			args = append(args, "-e", t)
		}
		out, err := acPfRun(ctx, rg, root, append(args, "--", "."))
		if err != nil && ctx.Err() != nil {
			dbg("prefetch", map[string]any{"skip": "timeout"})
			return "" // on timeout nothing is injected
		}
		hits := strings.Split(strings.TrimSpace(out), "\n")
		count := map[string]int{}
		for _, h := range hits {
			for _, t := range terms {
				if strings.Contains(h, t) {
					count[t]++
				}
			}
		}
		perFile := map[string]int{}
		kept := 0
		for _, h := range hits {
			if h == "" || kept >= acPrefetchMaxHits {
				continue
			}
			good := false
			for _, t := range terms {
				if count[t] <= acPrefetchVague && strings.Contains(h, t) {
					good = true
					break
				}
			}
			if !good {
				continue
			}
			f := opGrepKey(h)
			if perFile[f] >= acPrefetchPerFile {
				continue
			}
			perFile[f]++
			kept++
			lines = append(lines, strings.TrimPrefix(opClip(h, 200), "./"))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<system-reminder>Auto-grep (may be incomplete, verify before editing):\n")
	for _, l := range lines {
		if b.Len()+len(l)+40 > budget {
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString("</system-reminder>")
	return b.String()
}

func acPfRun(ctx context.Context, bin, dir string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = scrubEnv(os.Environ())
	var out bytes.Buffer
	cmd.Stdout = &acPfLimit{w: &out, n: 256 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // no matches
		}
		if out.Len() == 0 {
			return "", fmt.Errorf("rg: %w", err)
		}
	}
	return out.String(), nil
}

// acPfLimit keeps at most n bytes of rg output and drops the rest.
type acPfLimit struct {
	w *bytes.Buffer
	n int
}

func (l *acPfLimit) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
