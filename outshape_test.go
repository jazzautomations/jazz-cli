//go:build !windows

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestToolBudget(t *testing.T) {
	for _, c := range []struct{ win, want int }{
		{0, 30 << 10},       // unknown window: the old fixed cap
		{8000, 6 << 10},     // floor
		{32000, 8960},       // 8% of 32k tokens at 3.5 bytes/token
		{65536, 18350},      //
		{128000, 30 << 10},  // ceiling
		{1000000, 30 << 10}, // ceiling
		{-5, 30 << 10},      // nonsense
		{21943, 6<<10 + 0},  // just under the floor
	} {
		if got := toolBudget(c.win); got != c.want {
			t.Errorf("toolBudget(%d) = %d, want %d", c.win, got, c.want)
		}
	}
}

func TestOpBudget(t *testing.T) {
	s := opDefaultSettings()
	if got := opBudget(s, nil); got != 30<<10 {
		t.Errorf("no window: %d", got)
	}
	if got := opBudget(s, func() int { return 32000 }); got != 8960 {
		t.Errorf("32k window: %d", got)
	}
	s.ToolBudget = 12 << 10
	if got := opBudget(s, func() int { return 32000 }); got != 12<<10 {
		t.Errorf("explicit budget ignored: %d", got)
	}
}

func TestOpLoadSettings(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	env := map[string]string{}
	get := func(k string) string { return env[k] }

	s := opLoadSettings(filepath.Join(dir, "missing.json"), get)
	if s != opDefaultSettings() || !s.Parse || s.GrepMode != "grouped" || s.Prefetch != "off" || s.ToolBudget != 0 {
		t.Fatalf("defaults: %+v", s)
	}
	os.WriteFile(cfg, []byte(`{"parse_output":false,"grep_trunc":"headtail","tool_budget":9000,"prefetch":"grep"}`), 0o600)
	s = opLoadSettings(cfg, get)
	if s.Parse || s.GrepMode != "headtail" || s.ToolBudget != 9000 || s.Prefetch != "grep" {
		t.Fatalf("config: %+v", s)
	}
	env["JAZZ_PARSE_OUTPUT"] = "1"
	env["JAZZ_GREP_TRUNC"] = "grouped"
	env["JAZZ_TOOL_BUDGET"] = "12k"
	env["JAZZ_PREFETCH"] = "off"
	s = opLoadSettings(cfg, get)
	if !s.Parse || s.GrepMode != "grouped" || s.ToolBudget != 12<<10 || s.Prefetch != "off" {
		t.Fatalf("env must win over config: %+v", s)
	}
	// Invalid values are ignored, not fatal.
	env = map[string]string{"JAZZ_PARSE_OUTPUT": "talvez", "JAZZ_GREP_TRUNC": "x", "JAZZ_TOOL_BUDGET": "lots", "JAZZ_PREFETCH": "vector"}
	os.WriteFile(cfg, []byte(`{"grep_trunc":"weird","prefetch":"x"}`), 0o600)
	if s = opLoadSettings(cfg, get); s != opDefaultSettings() {
		t.Fatalf("invalid values must fall back to defaults: %+v", s)
	}
}

func TestOpFlags(t *testing.T) {
	t.Cleanup(func() { opSetSettings(opDefaultSettings()) })
	opSetSettings(opDefaultSettings())
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	f := opRegisterFlags(fs)
	if err := fs.Parse([]string{"--parse-output=false", "--grep-trunc", "headtail", "--tool-budget", "10k", "--prefetch", "grep"}); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(); err != nil {
		t.Fatal(err)
	}
	s := opSettingsNow()
	if s.Parse || s.GrepMode != "headtail" || s.ToolBudget != 10<<10 || s.Prefetch != "grep" {
		t.Fatalf("flags not applied: %+v", s)
	}
	// Bare --parse-output turns it on; untouched flags keep the current value.
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	f = opRegisterFlags(fs)
	fs.Parse([]string{"--parse-output"})
	f.apply()
	if s := opSettingsNow(); !s.Parse || s.GrepMode != "headtail" {
		t.Fatalf("bare bool flag: %+v", s)
	}
	for _, bad := range [][]string{{"--grep-trunc", "tail"}, {"--prefetch", "vector"}, {"--tool-budget", "muito"}} {
		fs = flag.NewFlagSet("t", flag.ContinueOnError)
		f = opRegisterFlags(fs)
		fs.Parse(bad)
		if err := f.apply(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// reorderFlags keeps the value next to its flag after a positional prompt.
	got := reorderFlags([]string{"conserta o teste", "--grep-trunc", "headtail", "--prefetch", "grep", "--tool-budget", "8k"})
	want := "--grep-trunc headtail --prefetch grep --tool-budget 8k conserta o teste"
	if strings.Join(got, " ") != want {
		t.Errorf("reorderFlags = %q", got)
	}
}

func TestOpIsGrepCmd(t *testing.T) {
	yes := []string{"rg -n foo", "rg foo src/ | head -50", "grep -rn 'x' .", "cd sub && rg TODO", "git grep -n Foo", "git -C x grep foo",
		"LC_ALL=C grep -r x .", "/usr/bin/rg x", "egrep 'a|b' f.txt", "rg x | sort | uniq", "cat log | grep ERROR"}
	no := []string{"go test ./...", "rg --version && go build", "echo rg", "git log --grep foo", "grep -c x f | wc -l", "find . -name '*.go'", "pytest -q", ""}
	for _, c := range yes {
		if !opIsGrepCmd(c) {
			t.Errorf("%q should be a grep command", c)
		}
	}
	for _, c := range no {
		if opIsGrepCmd(c) {
			t.Errorf("%q should not be a grep command", c)
		}
	}
}

func opGrepOutput(files, per int) string {
	var b strings.Builder
	for f := 0; f < files; f++ {
		for k := 1; k <= per; k++ {
			fmt.Fprintf(&b, "src/pkg%02d/file%02d.go:%d:\tfoo := bar(%d) // some matched source text here\n", f, f, k*10, k)
		}
	}
	return b.String()
}

func TestTruncateGrepGrouped(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	out := opGrepOutput(40, 30) // 1200 matches in 40 files
	got := truncateGrep(out, 8<<10)
	if len(got) > 8<<10 {
		t.Errorf("over budget: %d", len(got))
	}
	lines := strings.Split(got, "\n")
	perFile := map[string]int{}
	lastFile := -1
	for _, l := range lines[:len(lines)-1] {
		m := regexp.MustCompile(`^src/pkg(\d+)/`).FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("unexpected line %q", l)
		}
		n, _ := strconv.Atoi(m[1])
		if n < lastFile {
			t.Fatalf("order not kept: %q after file %d", l, lastFile)
		}
		lastFile = n
		perFile[m[1]]++
	}
	for f, n := range perFile {
		if n > 5 {
			t.Errorf("file %s shows %d matches (max 5)", f, n)
		}
	}
	if len(perFile) < 10 {
		t.Errorf("only %d files shown: grouping should spread the budget", len(perFile))
	}
	foot := lines[len(lines)-1]
	if !strings.HasPrefix(foot, "[+") || !strings.Contains(foot, "more lines in 40 files") || !strings.Contains(foot, "Full output saved to ") {
		t.Errorf("footer: %q", foot)
	}
	p := regexp.MustCompile(`saved to (\S+) `).FindStringSubmatch(foot)
	if p == nil {
		t.Fatal("no spill path")
	}
	if b, err := os.ReadFile(p[1]); err != nil || string(b) != out {
		t.Errorf("spill file does not hold the full output: %v", err)
	}
}

func TestTruncateGrepSingleFileAndSmall(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	small := opGrepOutput(3, 3)
	if got := truncateGrep(small, 30<<10); got != small {
		t.Error("output under the budget must be untouched")
	}
	one := opGrepOutput(1, 2000)
	got := truncateGrep(one, 6<<10)
	if !strings.HasPrefix(got, "src/pkg00/file00.go:10:") || len(got) > 6<<10 {
		t.Errorf("single file: head cut expected, got %d bytes", len(got))
	}
	if strings.Count(got, "\n") < 20 {
		t.Error("single-file output must not be limited to 5 lines")
	}
}

func TestOpShapeOutput(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	set := opDefaultSettings()

	// Small output: untouched, whatever its shape.
	short := opReadFixture(t, "gotest_short.txt")
	if got := opShapeOutput("go test ./...", short, 30<<10, set); got != short {
		t.Error("output under 2 KB must reach the model as is")
	}

	// Long go test -v output over the budget: summary + tail + spill.
	raw := opReadFixture(t, "gotest_verbose.txt")
	big := strings.Repeat(strings.Split(raw, "--- FAIL")[0], 30) + raw
	got := opShapeOutput("go test -v ./...", big, 8960, set)
	for _, s := range []string{"[Output parsed as gotest test (100% of", "FAIL TestAddWrong at calc_test.go:17: Add(2, 2) = 4, want 5",
		"--- last 30 lines ---", "Full output saved to "} {
		if !strings.Contains(got, s) {
			t.Errorf("parsed view lacks %q:\n%s", s, got)
		}
	}
	if len(got) > 8960 {
		t.Errorf("parsed view over budget: %d", len(got))
	}
	if n := strings.Count(got, "\n"); n > 1+opSummaryLines+opUnparsedShown+2+opTailLines+1 {
		t.Errorf("parsed view has %d lines", n)
	}

	// Flag off: the old 20/80 cut.
	set.Parse = false
	got = opShapeOutput("go test -v ./...", big, 8960, set)
	if strings.Contains(got, "[Output parsed") || !strings.Contains(got, "bytes omitted from the middle") {
		t.Errorf("--parse-output=false must use head/tail:\n%.300s", got)
	}

	// Mixed program output (confidence < 0.8) over 2 KB: head/tail.
	set.Parse = true
	mixed := opReadFixture(t, "neg_mixed_panic.txt")
	mixed = strings.Repeat(mixed[:strings.Index(mixed, "panic:")], 3) + mixed
	if got := opShapeOutput("./run", mixed, 3<<10, set); strings.Contains(got, "[Output parsed") {
		t.Error("low-confidence output must not be summarized")
	}

	// rg output goes through the grouped cut, never the parsers.
	g := opGrepOutput(30, 30)
	got = opShapeOutput("rg -n foo", g, 6<<10, set)
	if !strings.Contains(got, "more lines in 30 files") {
		t.Errorf("grouped grep cut not used:\n%.300s", got)
	}
	set.GrepMode = "headtail"
	got = opShapeOutput("rg -n foo", g, 6<<10, set)
	if !strings.Contains(got, "bytes omitted from the middle") {
		t.Errorf("JAZZ_GREP_TRUNC=headtail not honoured:\n%.300s", got)
	}
}

func TestOpRenderUnexplainedAndTail(t *testing.T) {
	var b strings.Builder
	b.WriteString("starting suite\nweird line one\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "=== RUN   TestN%d\n--- PASS: TestN%d (0.00s)\n", i, i)
	}
	b.WriteString("--- FAIL: TestBad (0.01s)\n    bad_test.go:9: got 1 want 2\nFAIL\nFAIL\texample.com/p\t0.02s\n")
	raw := b.String()
	r := opParse(raw)
	if r == nil || !r.OK || r.Confidence < opMinConfidence {
		t.Fatalf("parse: %+v", r)
	}
	lines := opClean(raw)
	got := opRender(r, lines, "/tmp/spill.txt", len(raw))
	if !strings.Contains(got, "--- unexplained lines (2) ---\nstarting suite\nweird line one\n") {
		t.Errorf("unexplained lines missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "FAIL\texample.com/p\t0.02s\n[Full output saved to /tmp/spill.txt — use rg on it or read it with offset/limit instead of re-running.]") {
		t.Errorf("tail/spill:\n%s", got)
	}
	if !strings.Contains(got, "FAIL TestBad at bad_test.go:9: got 1 want 2") || !strings.Contains(got, "gotest: 1 failed, 60 passed") {
		t.Errorf("summary:\n%s", got)
	}
}

// TestBashToolUsesShaping runs the real bash tool: a long go-test-shaped output
// comes back summarized, and the window callback sizes the budget.
func TestBashToolUsesShaping(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	prev := opSetSettings(opDefaultSettings())
	t.Cleanup(func() { opSetSettings(prev) })
	root := t.TempDir()
	fx, _ := filepath.Abs(filepath.Join("testdata", "outparse", "gotest_verbose.txt"))
	tb := NewToolbox(root)
	defer tb.Shell.Close()
	tb.Window = func() int { return 32000 }
	cmd := fmt.Sprintf("for i in 1 2 3 4 5 6; do cat %q; done", fx)
	out := tb.bash(t.Context(), map[string]any{"command": cmd}, "")
	if !strings.Contains(out.Text, "[Output parsed as gotest test") || !strings.Contains(out.Text, "[exit code 0]") {
		t.Errorf("bash result not shaped:\n%s", out.Text)
	}
	if len(out.Text) > 8960+200 {
		t.Errorf("bash result %d bytes over the 32k-window budget", len(out.Text))
	}
}
