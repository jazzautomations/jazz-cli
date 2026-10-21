package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden fixtures live in testdata/outparse: *.txt is the raw tool output and
// *.golden.json the parser result. neg_*.txt are negative controls that must
// fail closed (nil, ok=false or confidence < 0.8). Regenerate the goldens with
// JAZZ_UPDATE_GOLDEN=1 go test -run TestOutparseGolden and review the diff.

func opReadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "outparse", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOutparseGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "outparse", "*.txt"))
	if len(files) < 20 {
		t.Fatalf("only %d fixtures", len(files))
	}
	update := os.Getenv("JAZZ_UPDATE_GOLDEN") == "1"
	for _, f := range files {
		name := filepath.Base(f)
		if strings.HasPrefix(name, "neg_") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			r := opParse(opReadFixture(t, name))
			if r == nil {
				t.Fatal("no parser matched")
			}
			got, _ := json.MarshalIndent(r, "", "  ")
			got = append(got, '\n')
			gp := strings.TrimSuffix(f, ".txt") + ".golden.json"
			if update {
				if err := os.WriteFile(gp, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(gp)
			if err != nil {
				t.Fatalf("missing golden (JAZZ_UPDATE_GOLDEN=1 to create): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("result differs from %s:\n%s", gp, got)
			}
		})
	}
}

// TestOutparseFixtures pins the facts the model needs from each fixture, so a
// golden refresh cannot silently lose them.
func TestOutparseFixtures(t *testing.T) {
	type want struct {
		kind, format       string
		usable             bool // ok and confidence >= 0.8
		passed, failed     int
		errors, skipped    int
		first              string // first failure "Test@file:line" or first diag "file:line:col" or trace "Exception@file:line"
		summaryHas         []string
		minFrames, minDiag int
	}
	cases := map[string]want{
		"gotest_verbose.txt": {kind: "test", format: "gotest", usable: true, passed: 35, failed: 2, skipped: 1,
			first: "TestAddWrong@calc_test.go:17", summaryHas: []string{"FAIL TestTable/sum#01 at calc_test.go:26: Add(2, 3) = 5 / want 6", "suites: 1 ok, 1 failed: example.com/gt/calc"}},
		"gotest_short.txt": {kind: "test", format: "gotest", usable: true, failed: 2, first: "TestAddWrong@calc_test.go:17"},
		"gotest_panic.txt": {kind: "test", format: "gotest", usable: true, failed: 1, first: "TestSum@/home/u/proj/gt/pan/pan_test.go:15",
			summaryHas: []string{"go panic: runtime error: invalid memory address", "/home/u/proj/gt/pan/pan_test.go:15 in example.com/gt/pan.TestSum"}, minFrames: 3},
		"pytest.txt": {kind: "test", format: "pytest", usable: true, passed: 54, failed: 1, errors: 1, skipped: 1,
			first:      "tests/test_api.py::test_create_user@tests/test_api.py:42",
			summaryHas: []string{"ERROR tests/test_utils.py::test_read_config at app/config.py:8: FileNotFoundError"}},
		"unittest_verbose.txt": {kind: "test", format: "unittest", usable: true, passed: 40, failed: 1, errors: 1, skipped: 1,
			first: "tests.test_calc.TestAdd.test_error@/home/u/proj/py/tests/test_calc.py:131", summaryHas: []string{"FAIL tests.test_calc.TestAdd.test_wrong at /home/u/proj/py/tests/test_calc.py:128: AssertionError: 4 != 5"}},
		"unittest_short.txt": {kind: "test", format: "unittest", usable: true, passed: 40, failed: 1, errors: 1, skipped: 1,
			first: "tests.test_calc.TestAdd.test_error@/home/u/proj/py/tests/test_calc.py:131"},
		"jest.txt": {kind: "test", format: "jest", usable: true, passed: 38, failed: 2, skipped: 1,
			first: "ApiClient › retries on 503@src/api/client.test.js:44", summaryHas: []string{"Expected: 3 / Received: 1", "at src/api/client.js:14"}},
		"vitest.txt": {kind: "test", format: "vitest", usable: true, passed: 24, failed: 1,
			first: "Cart > applies discount@src/cart.test.ts:23"},
		"cargo_test.txt": {kind: "test", format: "cargo", usable: true, passed: 3, failed: 2, skipped: 1,
			first: "parser::tests::rejects_garbage@src/parser.rs:88", summaryHas: []string{"attempt to divide by zero"}},
		"gobuild.txt":     {kind: "build", format: "go", usable: true, errors: 8, first: "./a.go:5:2", minDiag: 8},
		"govet.txt":       {kind: "build", format: "go", usable: true, errors: 1, first: "./a.go:9:31"},
		"clang.txt":       {kind: "build", format: "gcc", usable: true, errors: 5, first: "main.c:7:22", minDiag: 6, summaryHas: []string{"util.c:3:13: error: expected ';' after return statement"}},
		"gcc.txt":         {kind: "build", format: "gcc", usable: true, errors: 2, first: "net.c:42:9", summaryHas: []string{"util.h:12:6: error: conflicting types"}},
		"tsc_plain.txt":   {kind: "build", format: "tsc", usable: true, errors: 5, first: "src/api/client.ts:14:7", summaryHas: []string{"error[TS2307]"}},
		"tsc_pretty.txt":  {kind: "build", format: "tsc", usable: true, errors: 3, first: "src/api/client.ts:14:7"},
		"rustc_build.txt": {kind: "build", format: "rustc", usable: true, errors: 2, first: "src/main.rs:12:13", summaryHas: []string{"src/parser.rs:7:9: warning: unused variable"}},
		"pysyntax.txt":    {kind: "build", format: "python", usable: true, errors: 1, first: "/home/u/proj/py/bad.py:2:0", summaryHas: []string{"error[SyntaxError]: expected ':'"}},
		"pytrace.txt": {kind: "trace", format: "python", usable: true, first: "AttributeError@/home/u/proj/py/app/service.py:11",
			summaryHas: []string{"/home/u/proj/py/app/models.py:6 in greet", "python RuntimeError: service failed"}, minFrames: 3},
		"nodetrace.txt": {kind: "trace", format: "node", usable: true, first: "TypeError@/home/u/proj/js/lib/parse.js:2",
			summaryHas: []string{"(+7 library/runtime frames)"}, minFrames: 3},
		// Three lines of program output before a short panic: the parser is
		// right, but too little of the output is explained to replace it.
		"gopanic.txt":    {kind: "trace", format: "go", usable: false, first: "panic@/home/u/proj/gp/p.go:7", minFrames: 3},
		"log_iso.txt":    {kind: "log", format: "log", usable: true, summaryHas: []string{"log: 11 entries", "fatal 1, error 3, warn 2, info 5", "3× [error] ERROR worker: job 4411", "python TimeoutError"}},
		"log_json.txt":   {kind: "log", format: "log", usable: true, summaryHas: []string{"error 3, warn 1, info 4", "3× [error] upstream refused"}},
		"log_syslog.txt": {kind: "log", format: "log", usable: true, summaryHas: []string{"Oct 19 09:58:01 → Oct 19 10:02:03", "[error] sync.sh: error: rsync"}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			r := opParse(opReadFixture(t, name))
			if r == nil {
				t.Fatal("no parser matched")
			}
			d := r.Data
			if d.Kind != w.kind || d.Format != w.format {
				t.Errorf("parsed as %s/%s, want %s/%s", d.Kind, d.Format, w.kind, w.format)
			}
			if usable := r.OK && r.Confidence >= opMinConfidence; usable != w.usable {
				t.Errorf("usable=%v (ok=%v confidence=%.3f), want %v; unparsed: %q", usable, r.OK, r.Confidence, w.usable, r.Unparsed)
			}
			if d.Passed != w.passed || d.Failed != w.failed || d.Errors != w.errors || d.Skipped != w.skipped {
				t.Errorf("counts passed=%d failed=%d errors=%d skipped=%d, want %d/%d/%d/%d", d.Passed, d.Failed, d.Errors, d.Skipped, w.passed, w.failed, w.errors, w.skipped)
			}
			first := ""
			switch {
			case len(d.Failures) > 0:
				f := d.Failures[0]
				first = f.Test + "@" + f.File + ":" + itoa(f.Line)
			case len(d.Diags) > 0:
				x := d.Diags[0]
				first = x.File + ":" + itoa(x.Line) + ":" + itoa(x.Col)
			case len(d.Traces) > 0 && len(d.Traces[0].Frames) > 0:
				tr := d.Traces[0]
				f := tr.Frames[0]
				first = tr.Exception + "@" + f.File + ":" + itoa(f.Line)
			}
			if w.first != "" && first != w.first {
				t.Errorf("first = %q, want %q", first, w.first)
			}
			sum := strings.Join(opSummary(r), "\n")
			for _, s := range w.summaryHas {
				if !strings.Contains(sum, s) {
					t.Errorf("summary lacks %q:\n%s", s, sum)
				}
			}
			if n := len(opSummary(r)); n > opSummaryLines {
				t.Errorf("summary has %d lines", n)
			}
			frames := 0
			for _, tr := range d.Traces {
				frames += len(tr.Frames)
			}
			if frames < w.minFrames {
				t.Errorf("%d frames, want ≥ %d", frames, w.minFrames)
			}
			if len(d.Diags) < w.minDiag {
				t.Errorf("%d diags, want ≥ %d", len(d.Diags), w.minDiag)
			}
			if r.Unparsed == nil || r.Warnings == nil {
				t.Error("unparsed/warnings must be [] not null (script output convention)")
			}
		})
	}
	// Every positive fixture is covered by the table.
	files, _ := filepath.Glob(filepath.Join("testdata", "outparse", "*.txt"))
	for _, f := range files {
		n := filepath.Base(f)
		if _, ok := cases[n]; !ok && !strings.HasPrefix(n, "neg_") {
			t.Errorf("fixture %s has no expectations", n)
		}
	}
}

// TestOutparseNegativeControls: prose, source code, CSV, JSON, ls, and program
// output with a short panic at the end must never be summarized (fail closed).
func TestOutparseNegativeControls(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "outparse", "neg_*.txt"))
	if len(files) < 8 {
		t.Fatalf("only %d negative controls", len(files))
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			raw := opReadFixture(t, name)
			if r := opParse(raw); r != nil && r.OK && r.Confidence >= opMinConfidence {
				t.Errorf("parsed as %s/%s with confidence %.3f", r.Data.Kind, r.Data.Format, r.Confidence)
			}
			// Also through the bash shaping path, padded over the 2 KB floor.
			t.Setenv("JAZZ_HOME", t.TempDir())
			big := raw
			for len(big) < 3<<10 {
				big += raw
			}
			if got := opShapeOutput("cat x", big, 30<<10, opDefaultSettings()); strings.Contains(got, "[Output parsed") {
				t.Errorf("negative control was summarized:\n%s", got)
			}
		})
	}
}

func TestOutparseConfidenceIsShareOfLines(t *testing.T) {
	r := opParse(opReadFixture(t, "gopanic.txt"))
	// 13 non-blank lines, 3 of them program output.
	if r.Confidence != 0.769 {
		t.Errorf("confidence = %v, want 10/13", r.Confidence)
	}
	if len(r.Unparsed) != 3 || r.Unparsed[0] != "worker 0 starting" {
		t.Errorf("unparsed = %q", r.Unparsed)
	}
	// Without the signature line (summary/diag/frames) confidence is capped.
	p := opParse(strings.Repeat("=== RUN   TestX\n", 50))
	if p == nil || p.OK || p.Confidence > 0.5 {
		t.Errorf("no signature: %+v", p)
	}
}

func TestOutparseANSIAndCarriageReturn(t *testing.T) {
	raw := "\x1b[32mok  \x1b[0m\texample.com/a\t0.10s\n" + "progress 10%\rprogress 100%\r--- FAIL: TestX (0.00s)\n    x_test.go:3: boom\nFAIL\texample.com/b\t0.20s\n"
	r := opParse(raw)
	if r == nil || !r.OK || r.Data.Format != "gotest" || len(r.Data.Failures) != 1 || r.Data.Failures[0].Message != "boom" {
		t.Fatalf("got %+v", r)
	}
	if r.Confidence != 1 {
		t.Errorf("confidence %v (ANSI/CR not cleaned): %q", r.Confidence, r.Unparsed)
	}
}

func TestOutparseHugeInputSkipped(t *testing.T) {
	if r := opParse(strings.Repeat("x", opMaxParseBytes+1)); r != nil {
		t.Error("over the size cap must not be parsed")
	}
}

func BenchmarkOpParseGoTest1MB(b *testing.B) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "outparse", "gotest_verbose.txt"))
	s := strings.Repeat(string(raw), (1<<20)/len(raw))
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		opParse(s)
	}
}

func BenchmarkOpParseProse1MB(b *testing.B) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "outparse", "neg_contract.txt"))
	s := strings.Repeat(string(raw), (1<<20)/len(raw))
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		opParse(s)
	}
}
