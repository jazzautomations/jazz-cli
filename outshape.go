package main

// outshape.go: how a long bash result is shaped for the model, and the
// settings behind it (all A/B-testable, docs/research/grep-is-all-you-need.md
// rec. 1 and skills-personas-scripts.md C.6):
//
//   - parse_output  (--parse-output, JAZZ_PARSE_OUTPUT, default on): outputs over
//     2 KB that a parser in outparse.go explains (confidence ≥ 0.8) become a
//     structured summary + unexplained lines + the last 30 lines + spill path.
//   - grep_trunc    (--grep-trunc, JAZZ_GREP_TRUNC, default grouped): rg/grep
//     output over the budget keeps matches in order, at most 5 per file.
//     headtail = the old 20/80 cut.
//   - tool_budget   (--tool-budget, JAZZ_TOOL_BUDGET, default 0 = auto): bytes of
//     one bash result; auto = 8% of the model's window (3.5 bytes/token),
//     clamped to 6–30 KB.
//   - prefetch      (--prefetch, JAZZ_PREFETCH, default off): see prefetch.go.
//
// Precedence: flag > env > config.json > default.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type opSettings struct {
	Parse      bool   // parse long test/build/trace/log output
	GrepMode   string // grouped | headtail
	ToolBudget int    // bytes per bash result; 0 = from the context window
	Prefetch   string // off | grep
}

var (
	opSetMu     sync.Mutex
	opSetLoaded bool
	opSetVal    opSettings
)

func opDefaultSettings() opSettings {
	return opSettings{Parse: true, GrepMode: "grouped", Prefetch: "off"}
}

// opSettingsNow returns the effective settings (loaded once from config.json and env).
func opSettingsNow() opSettings {
	opSetMu.Lock()
	defer opSetMu.Unlock()
	if !opSetLoaded {
		opSetVal = opLoadSettings(filepath.Join(configDir(), "config.json"), os.Getenv)
		opSetLoaded = true
	}
	return opSetVal
}

// opSetSettings replaces the effective settings (flags, tests); it returns the
// previous value so tests can restore it.
func opSetSettings(s opSettings) opSettings {
	prev := opSettingsNow()
	opSetMu.Lock()
	opSetVal, opSetLoaded = s, true
	opSetMu.Unlock()
	return prev
}

func opLoadSettings(cfgPath string, getenv func(string) string) opSettings {
	s := opDefaultSettings()
	var c struct {
		ParseOutput *bool  `json:"parse_output"`
		GrepTrunc   string `json:"grep_trunc"`
		ToolBudget  int    `json:"tool_budget"`
		Prefetch    string `json:"prefetch"`
	}
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.ParseOutput != nil {
		s.Parse = *c.ParseOutput
	}
	if opValidGrepMode(c.GrepTrunc) {
		s.GrepMode = c.GrepTrunc
	}
	if c.ToolBudget > 0 {
		s.ToolBudget = c.ToolBudget
	}
	if opValidPrefetch(c.Prefetch) {
		s.Prefetch = c.Prefetch
	}
	if v := getenv("JAZZ_PARSE_OUTPUT"); v != "" {
		if b, err := opParseBool(v); err == nil {
			s.Parse = b
		}
	}
	if v := getenv("JAZZ_GREP_TRUNC"); opValidGrepMode(v) {
		s.GrepMode = v
	}
	if v := getenv("JAZZ_TOOL_BUDGET"); v != "" {
		if n, err := opParseSize(v); err == nil {
			s.ToolBudget = n
		}
	}
	if v := getenv("JAZZ_PREFETCH"); opValidPrefetch(v) {
		s.Prefetch = v
	}
	return s
}

func opValidGrepMode(s string) bool { return s == "grouped" || s == "headtail" }
func opValidPrefetch(s string) bool { return s == "off" || s == "grep" }

func opParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	}
	return false, errors.New("bad bool")
}

// opParseSize reads "12000", "12k", "12KB"; 0 or "auto" = from the window.
func opParseSize(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "auto" {
		return 0, nil
	}
	mul := 1
	for _, suf := range []string{"kib", "kb", "k"} {
		if strings.HasSuffix(s, suf) {
			s, mul = strings.TrimSuffix(s, suf), 1024
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, errors.New("bad size")
	}
	n *= mul
	if n > 0 && n < 1024 {
		n = 1024
	}
	return n, nil
}

// opBoolFlag is a bool flag that remembers whether it was given.
type opBoolFlag struct {
	set, v bool
}

func (f *opBoolFlag) String() string { return strconv.FormatBool(f.v) }
func (f *opBoolFlag) Set(s string) error {
	b, err := opParseBool(s)
	if err != nil {
		return err
	}
	f.set, f.v = true, b
	return nil
}
func (f *opBoolFlag) IsBoolFlag() bool { return true }

// opFlags holds the command-line overrides registered by opRegisterFlags.
type opFlags struct {
	parse                    opBoolFlag
	grep, budget, prefetch   string
	grepSet, budSet, prefSet bool
	fs                       *flag.FlagSet
}

// opValuedFlags are the flags that take a separate value (for reorderFlags).
var opValuedFlags = map[string]bool{"--grep-trunc": true, "-grep-trunc": true, "--tool-budget": true, "-tool-budget": true, "--prefetch": true, "-prefetch": true}

// opRegisterFlags adds --parse-output, --grep-trunc, --tool-budget and --prefetch.
func opRegisterFlags(fs *flag.FlagSet) *opFlags {
	f := &opFlags{fs: fs}
	fs.Var(&f.parse, "parse-output", "")
	fs.StringVar(&f.grep, "grep-trunc", "", "")
	fs.StringVar(&f.budget, "tool-budget", "", "")
	fs.StringVar(&f.prefetch, "prefetch", "", "")
	return f
}

// apply validates the flags and makes them the effective settings.
func (f *opFlags) apply() error {
	f.fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "grep-trunc":
			f.grepSet = true
		case "tool-budget":
			f.budSet = true
		case "prefetch":
			f.prefSet = true
		}
	})
	s := opSettingsNow()
	if f.parse.set {
		s.Parse = f.parse.v
	}
	if f.grepSet {
		if !opValidGrepMode(f.grep) {
			return errors.New("--grep-trunc deve ser grouped ou headtail")
		}
		s.GrepMode = f.grep
	}
	if f.budSet {
		n, err := opParseSize(f.budget)
		if err != nil {
			return errors.New("--tool-budget deve ser um tamanho em bytes (ex.: 12000, 12k) ou auto")
		}
		s.ToolBudget = n
	}
	if f.prefSet {
		if !opValidPrefetch(f.prefetch) {
			return errors.New("--prefetch deve ser off ou grep")
		}
		s.Prefetch = f.prefetch
	}
	opSetSettings(s)
	return nil
}

// toolBudget is how many bytes of one tool result the model sees, given its
// context window in tokens: 8% of the window at ~3.5 bytes/token, clamped to
// 6–30 KB (30 KB is ~8–10k tokens: two of those fill a third of a 32k window).
func toolBudget(win int) int {
	if win <= 0 {
		return maxToolBytes
	}
	b := int(float64(win) * 0.08 * 3.5)
	if b < 6<<10 {
		b = 6 << 10
	}
	if b > maxToolBytes {
		b = maxToolBytes
	}
	return b
}

// opBudget is the bash result budget for a toolbox (the agent sets Window).
func opBudget(s opSettings, window func() int) int {
	if s.ToolBudget > 0 {
		return s.ToolBudget
	}
	w := 0
	if window != nil {
		w = window()
	}
	return toolBudget(w)
}

// opShapeOutput is what the model sees of a (redacted) bash output.
func opShapeOutput(cmd, out string, budget int, s opSettings) string {
	if opIsGrepCmd(cmd) {
		if len(out) <= budget {
			return out
		}
		if s.GrepMode == "grouped" {
			return truncateGrep(out, budget)
		}
		return truncateHeadTail(out, budget)
	}
	if s.Parse && len(out) > opParseMin {
		if txt, ok := opParsedView(out, budget); ok {
			return txt
		}
	}
	return truncateHeadTail(out, budget)
}

// opParsedView returns the parsed rendering when a parser is confident and the
// rendering fits the budget. When the raw output fits too, the rendering must
// also save at least a quarter of it; otherwise the raw text is worth more.
func opParsedView(out string, budget int) (string, bool) {
	r := opParse(out)
	if r == nil || !r.OK || r.Confidence < opMinConfidence {
		if r != nil {
			dbg("outparse", map[string]any{"format": r.Data.Format, "ok": r.OK, "confidence": r.Confidence, "bytes": len(out), "used": false})
		}
		return "", false
	}
	lines := opClean(out)
	txt := opRender(r, lines, "", len(out))
	const pathRoom = 200
	if len(txt)+pathRoom > budget || len(out) <= budget && len(txt)+pathRoom > len(out)*3/4 {
		dbg("outparse", map[string]any{"format": r.Data.Format, "confidence": r.Confidence, "bytes": len(out), "rendered": len(txt), "used": false})
		return "", false
	}
	txt = opRender(r, lines, spill(out), len(out))
	dbg("outparse", map[string]any{"format": r.Data.Format, "confidence": r.Confidence, "bytes": len(out), "rendered": len(txt), "used": true})
	return txt, true
}

// --- rg/grep grouped truncation --------------------------------------------

// opGrepFilters are pipeline stages that keep grep-shaped output.
var opGrepFilters = map[string]bool{"head": true, "tail": true, "sort": true, "uniq": true, "cut": true, "less": true, "more": true, "cat": true, "column": true, "nl": true}

// opIsGrepCmd reports whether cmd's output is search matches: the last
// command of the line (after && ; ||) is rg/grep/ag/ack/git grep, possibly
// piped through head/sort/uniq…
func opIsGrepCmd(cmd string) bool {
	stages := opLastPipeline(cmd)
	for k := len(stages) - 1; k >= 0; k-- {
		f := strings.Fields(stages[k])
		for len(f) > 0 && (strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-") || f[0] == "command" || f[0] == "time" || f[0] == "nice" || f[0] == "env" || f[0] == "xargs") {
			f = f[1:]
		}
		if len(f) == 0 {
			return false
		}
		switch filepath.Base(f[0]) {
		case "rg", "grep", "egrep", "fgrep", "ag", "ack", "ugrep", "zgrep":
			return true
		case "git":
			for j := 1; j < len(f); j++ {
				w := f[j]
				if w == "-C" || w == "-c" {
					j++ // option with a separate value
					continue
				}
				if !strings.HasPrefix(w, "-") {
					return w == "grep"
				}
			}
			return false
		}
		if !opGrepFilters[filepath.Base(f[0])] {
			return false
		}
	}
	return false
}

// opLastPipeline returns the stages of the last pipeline of a command line
// (after the last && || ; or newline), splitting on | outside quotes.
func opLastPipeline(cmd string) []string {
	var stages []string
	var q byte
	start := 0
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' && i+1 < len(cmd) {
				i++
			}
		case c == '\'' || c == '"':
			q = c
		case c == '\\' && i+1 < len(cmd):
			i++
		case c == ';' || c == '\n' || c == '&' && i+1 < len(cmd) && cmd[i+1] == '&' || c == '|' && i+1 < len(cmd) && cmd[i+1] == '|':
			stages = stages[:0]
			if c == '&' || c == '|' {
				i++
			}
			start = i + 1
		case c == '|':
			stages = append(stages, cmd[start:i])
			start = i + 1
		}
	}
	return append(stages, cmd[start:])
}

var opGrepLineRx = regexp.MustCompile(`^([^:\s][^:]*?)[:-](\d+)[:-]`)

// opGrepKey is the file a grep output line belongs to ("" when unknown).
func opGrepKey(l string) string {
	if m := opGrepLineRx.FindStringSubmatch(l); m != nil {
		return m[1]
	}
	if i := strings.IndexByte(l, ':'); i > 0 {
		p := l[:i]
		if !strings.ContainsAny(p, " \t") && strings.ContainsAny(p, "/.") {
			return p
		}
	}
	return ""
}

// truncateGrep keeps matches in their order, at most 5 lines per file, until
// the budget is used, then says how many matches in how many files were left
// out and where the full output is. One-file output is cut at the end instead.
func truncateGrep(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const perFile = 5
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	keys := make([]string, len(lines))
	files := map[string]int{}
	for i, l := range lines {
		keys[i] = opGrepKey(l)
		if keys[i] != "" {
			files[keys[i]]++
		}
	}
	perCap := perFile
	if len(files) <= 1 {
		perCap = len(lines) // one file (or unknown shape): plain head cut
	}
	room := max - 900 // footer: counts, up to 12 file names, spill path
	if room < max/2 {
		room = max / 2
	}
	var b strings.Builder
	shown := map[string]int{}
	omitted := map[string]int{}
	var omitOrder []string
	omittedLines, lastKept := 0, true
	for i, l := range lines {
		k := keys[i]
		if l == "--" { // context separator: only between kept lines
			if lastKept && b.Len()+3 <= room {
				b.WriteString("--\n")
			}
			continue
		}
		cl := opClip(l, 400)
		if (k == "" || shown[k] < perCap) && b.Len()+len(cl)+1 <= room {
			b.WriteString(cl)
			b.WriteByte('\n')
			shown[k]++
			lastKept = true
			continue
		}
		lastKept = false
		omittedLines++
		if k != "" {
			if omitted[k] == 0 {
				omitOrder = append(omitOrder, k)
			}
			omitted[k]++
		}
	}
	out := strings.TrimRight(b.String(), "\n")
	if omittedLines == 0 {
		return out
	}
	note := fmt.Sprintf("\n[+%d more lines", omittedLines)
	if len(omitOrder) > 0 {
		note += fmt.Sprintf(" in %d files (%d files matched; at most %d lines shown per file)", len(omitOrder), len(files), perCap)
		var parts []string
		for k, f := range omitOrder {
			if k == 12 {
				parts = append(parts, fmt.Sprintf("… +%d files", len(omitOrder)-k))
				break
			}
			parts = append(parts, fmt.Sprintf("%s +%d", f, omitted[f]))
		}
		note += "; more in: " + opClip(strings.Join(parts, ", "), 300)
	}
	note += "."
	if p := spill(s); p != "" {
		note += fmt.Sprintf(" Full output saved to %s — narrow the search (path, -g glob, -w) or rg the file instead of re-running.", p)
	}
	note += "]"
	for len(out)+len(note) > max { // a very long spill path: drop matches, never the footer
		i := strings.LastIndexByte(out, '\n')
		if i < 0 {
			out = ""
			break
		}
		out = out[:i]
	}
	return out + note
}
