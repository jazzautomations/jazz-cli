//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------- helpers (prefixed to avoid collisions with other suites) ----------

func editNewToolbox(t *testing.T) (*Toolbox, string) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	dir := t.TempDir()
	return NewToolbox(dir), dir
}

func editExec(t *testing.T, tb *Toolbox, tool string, args map[string]any) ToolOutput {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tb.Exec(context.Background(), tool, string(b))
}

func editPut(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func editGet(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const editBOM = string(rune(0xFEFF))

// editBumpMtime gives the file a fresh, never-before-used mtime so stale
// detection does not depend on filesystem/kernel timestamp granularity (on
// Linux two writes inside one jiffy share an mtime).
var editMtimeSeq atomic.Int64

func editBumpMtime(t *testing.T, path string) {
	t.Helper()
	nt := time.Now().Add(time.Hour + time.Duration(editMtimeSeq.Add(1))*time.Minute).Truncate(time.Second)
	if err := os.Chtimes(path, nt, nt); err != nil {
		t.Fatal(err)
	}
}

// ---------- applyEdits: exact matching ----------

func TestEditApplyExactSingle(t *testing.T) {
	cases := []struct {
		name, content, old, new, want string
	}{
		{"single line", "a\nb\nc\n", "b", "B", "a\nB\nc\n"},
		{"mid line", "x := foo(1)\n", "foo", "bar", "x := bar(1)\n"},
		{"multi line", "func f() {\n\treturn 1\n}\n", "\treturn 1\n}", "\treturn 2\n}", "func f() {\n\treturn 2\n}\n"},
		{"start of file", "first\nsecond\n", "first", "1st", "1st\nsecond\n"},
		{"end without newline", "a\nlast", "last", "LAST", "a\nLAST"},
		{"delete text", "keep\ndrop\nkeep2\n", "drop\n", "", "keep\nkeep2\n"},
		{"insert lines", "a\nc\n", "a\n", "a\nb\n", "a\nb\nc\n"},
		{"CRLF in args normalized", "a\nb\nc\n", "a\r\nb", "A\r\nB", "A\nB\nc\n"},
		{"unicode exact", "olá “mundo”\n", "“mundo”", "“terra”", "olá “terra”\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := applyEdits(c.content, []editOp{{Old: c.old, New: c.new}}, "f.txt")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Content != c.want {
				t.Errorf("content = %q, want %q", res.Content, c.want)
			}
			if res.Fuzzy {
				t.Errorf("exact match reported as fuzzy")
			}
			if res.Count != 1 {
				t.Errorf("count = %d, want 1", res.Count)
			}
		})
	}
}

func TestEditApplyNotFound(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	_, err := applyEdits(content, []editOp{{Old: "zz", New: "y"}}, "f.go")
	if err == nil {
		t.Fatal("expected error")
	}
	want := "Could not find the exact text in f.go. The old text must match exactly including all whitespace and newlines."
	if err.Error() != want {
		t.Errorf("single not-found:\n got %q\nwant %q", err.Error(), want)
	}

	_, err = applyEdits(content, []editOp{{Old: "alpha", New: "A"}, {Old: "zz", New: "y"}}, "f.go")
	if err == nil {
		t.Fatal("expected error")
	}
	want = "Could not find edits[1] in f.go. The old_string must match exactly including all whitespace and newlines."
	if err.Error() != want {
		t.Errorf("multi not-found:\n got %q\nwant %q", err.Error(), want)
	}
}

func TestEditApplyNotFoundHint(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tprintln(1)\n}\n"
	_, err := applyEdits(content, []editOp{{Old: "func mian() {", New: "x"}}, "m.go")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Closest line is 3:") || !strings.Contains(err.Error(), `"func main() {"`) {
		t.Errorf("expected closest-line hint pointing at line 3, got %q", err.Error())
	}
	_, err = applyEdits(content, []editOp{{Old: "completely unrelated words here", New: "x"}}, "m.go")
	if err == nil || !strings.HasSuffix(err.Error(), " Re-read the file to get the current text.") {
		t.Errorf("expected re-read hint, got %v", err)
	}
}

func TestEditApplyDuplicate(t *testing.T) {
	content := "x = 1\nx = 1\nx = 1\ny\n"
	_, err := applyEdits(content, []editOp{{Old: "x = 1", New: "x = 2"}}, "d.py")
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	want := "Found 3 occurrences of the text in d.py. The text must be unique. Please provide more context to make it unique, or set replace_all: true to change every occurrence."
	if err.Error() != want {
		t.Errorf("single dup:\n got %q\nwant %q", err.Error(), want)
	}
	_, err = applyEdits(content, []editOp{{Old: "y", New: "z"}, {Old: "x = 1", New: "x = 2"}}, "d.py")
	if err == nil || !strings.HasPrefix(err.Error(), "Found 3 occurrences of edits[1] in d.py. The text must be unique.") {
		t.Errorf("multi dup: got %v", err)
	}
}

func TestEditApplyReplaceAll(t *testing.T) {
	cases := []struct {
		name, content, old, new, want string
		count                         int
	}{
		{"three", "x = 1\nx = 1\nx = 1\n", "x = 1", "x = 2", "x = 2\nx = 2\nx = 2\n", 3},
		{"single occurrence", "a b c", "b", "B", "a B c", 1},
		{"non overlapping", "aaaa", "aa", "b", "bb", 2},
		{"odd non overlapping", "aaa", "aa", "b", "ba", 1},
		{"grow", "ab ab", "ab", "abab", "abab abab", 2},
		{"delete all", "-a-a-", "a", "", "---", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := applyEdits(c.content, []editOp{{Old: c.old, New: c.new, All: true}}, "f")
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if res.Content != c.want || res.Count != c.count {
				t.Errorf("got %q (%d), want %q (%d)", res.Content, res.Count, c.want, c.count)
			}
		})
	}
}

func TestEditApplyEmptyOld(t *testing.T) {
	_, err := applyEdits("abc", []editOp{{Old: "", New: "x"}}, "e.txt")
	if err == nil || !strings.HasPrefix(err.Error(), "old_string must not be empty in e.txt.") {
		t.Errorf("single empty: %v", err)
	}
	_, err = applyEdits("abc", []editOp{{Old: "a", New: "x"}, {Old: "", New: "y"}}, "e.txt")
	if err == nil || err.Error() != "edits[1].old_string must not be empty in e.txt" {
		t.Errorf("multi empty: %v", err)
	}
	// "\r" alone normalizes to "\n", not empty; "\r\n" -> "\n" also not empty
	if _, err := applyEdits("a\nb", []editOp{{Old: "\r\n", New: " "}}, "e.txt"); err != nil {
		t.Errorf("CRLF old should normalize to LF and match: %v", err)
	}
}

func TestEditApplyNoChange(t *testing.T) {
	_, err := applyEdits("a\nb\n", []editOp{{Old: "b", New: "b"}}, "n.txt")
	want := "No changes made to n.txt. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected."
	if err == nil || err.Error() != want {
		t.Errorf("no-change: got %v", err)
	}
	// multiple identity edits
	_, err = applyEdits("a\nb\n", []editOp{{Old: "a", New: "a"}, {Old: "b", New: "b"}}, "n.txt")
	if err == nil || !strings.HasPrefix(err.Error(), "No changes made to n.txt.") {
		t.Errorf("multi no-change: got %v", err)
	}
	// CRLF-only differences in new_string are normalized away => no change
	_, err = applyEdits("a\nb\n", []editOp{{Old: "a\nb", New: "a\r\nb"}}, "n.txt")
	if err == nil || !strings.HasPrefix(err.Error(), "No changes made") {
		t.Errorf("CRLF-only change should be no-op: %v", err)
	}
}

// ---------- applyEdits: multi-edit ----------

func TestEditApplyMultiEdit(t *testing.T) {
	content := "one\ntwo\nthree\nfour\n"
	res, err := applyEdits(content, []editOp{
		{Old: "four", New: "FOUR"},
		{Old: "one", New: "ONE"},
		{Old: "three", New: "THREE\n3.5"},
	}, "m.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ONE\ntwo\nTHREE\n3.5\nFOUR\n"; res.Content != want {
		t.Errorf("got %q want %q", res.Content, want)
	}
	if res.Count != 3 {
		t.Errorf("count %d", res.Count)
	}

	// All edits match against the ORIGINAL: edits[1] cannot target text produced by edits[0].
	_, err = applyEdits("a b\n", []editOp{{Old: "a", New: "c"}, {Old: "c b", New: "d"}}, "m.txt")
	if err == nil || !strings.HasPrefix(err.Error(), "Could not find edits[1] in m.txt.") {
		t.Errorf("edits must be matched against original content, got %v", err)
	}

	// Swap via edits works because matching is against the original.
	res, err = applyEdits("x y\n", []editOp{{Old: "x", New: "y"}, {Old: "y", New: "x"}}, "m.txt")
	if err != nil || res.Content != "y x\n" {
		t.Errorf("swap: %v %+v", err, res)
	}

	// Mixing replace_all and single edits.
	res, err = applyEdits("a.a.a\nb\n", []editOp{{Old: "a", New: "A", All: true}, {Old: "b", New: "B"}}, "m.txt")
	if err != nil || res.Content != "A.A.A\nB\n" || res.Count != 4 {
		t.Errorf("mixed all: %v %+v", err, res)
	}
}

func TestEditApplyOverlap(t *testing.T) {
	cases := []struct {
		name  string
		edits []editOp
		want  string // "" => no error expected
	}{
		{"contained", []editOp{{Old: "hello world", New: "x"}, {Old: "world", New: "y"}}, "edits[0] and edits[1] overlap in o.txt. Merge them into one edit or target disjoint regions."},
		{"partial", []editOp{{Old: "lo wo", New: "x"}, {Old: "hello", New: "y"}}, "edits[1] and edits[0] overlap in o.txt. Merge them into one edit or target disjoint regions."},
		{"identical", []editOp{{Old: "world", New: "x"}, {Old: "world", New: "y"}}, "overlap in o.txt"},
		{"replace_all vs single", []editOp{{Old: "o", New: "0", All: true}, {Old: "wor", New: "W"}}, "overlap in o.txt"},
		{"adjacent ok", []editOp{{Old: "hello", New: "H"}, {Old: " world", New: " W"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := applyEdits("hello world\n", c.edits, "o.txt")
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				if res.Content != "H W\n" {
					t.Errorf("adjacent got %q", res.Content)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want containing %q", err, c.want)
			}
		})
	}
}

// ---------- applyEdits: fuzzy fallback ----------

func TestEditApplyFuzzy(t *testing.T) {
	cases := []struct {
		name, content, old, new, want string
	}{
		{"trailing whitespace",
			"keep  \nfunc f() {   \n\treturn 1  \n}\nkeep2\t\n",
			"func f() {\n\treturn 1\n}", "func f() {\n\treturn 2\n}",
			"keep  \nfunc f() {\n\treturn 2\n}\nkeep2\t\n"},
		{"smart double quotes",
			"a = ‘x’\nb = “y”\nc = “z”\n",
			`b = "y"`, `b = "Y"`,
			"a = ‘x’\nb = \"Y\"\nc = “z”\n"},
		{"smart single quotes",
			"it‘s\nit’s ok\nit‛s\n",
			"it's ok", "it is ok",
			"it‘s\nit is ok\nit‛s\n"},
		{"low-9 quotes", "„q‟\n‚s‛\n", `"q"`, `"Q"`, "\"Q\"\n‚s‛\n"},
		{"en dash", "x – y\nrange 1–2\nz — w\n", "range 1-2", "range 1-3", "x – y\nrange 1-3\nz — w\n"},
		{"em dash", "x – y\nrange 1–2\nz — w\n", "z - w", "z = w", "x – y\nrange 1–2\nz = w\n"},
		{"minus sign", "n = −1\nm = −2\n", "m = -2", "m = -3", "n = −1\nm = -3\n"},
		{"hyphens and bar", "a‐b\nc‑d―e‒f\n", "c-d-e-f", "cdef", "a‐b\ncdef\n"},
		{"nbsp", "a b\nc d\n", "c d", "c e", "a b\nc e\n"},
		{"narrow nbsp", "a b\nc d\n", "c d", "c e", "a b\nc e\n"},
		{"ideographic space", "a　b\nc　d\n", "c d", "c e", "a　b\nc e\n"},
		{"em space", "a b\nc d\n", "c d", "c e", "a b\nc e\n"},
		{"nfkc fullwidth", "ｆｏｏ()\nｂａｒ()\n", "bar()", "baz()", "ｆｏｏ()\nbaz()\n"},
		{"nfkc ligature", "ﬁle := 1\nﬁle2 := 2\n", "file2 := 2", "file2 := 3", "ﬁle := 1\nfile2 := 3\n"},
		{"old has smart quotes, file ascii", "say(\"hi\")  \nother  \n", "say(“hi”)", "say(“yo”)", "say(“yo”)\nother  \n"},
		{"touched line normalized outside match", "x := 1   \ny := 2   \nz := 3   \n", "x := 1\ny", "x := 9\ny", "x := 9\ny := 2\nz := 3   \n"},
		{"no trailing newline", "a  \nb “c”", `b "c"`, `b "d"`, "a  \nb \"d\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := applyEdits(c.content, []editOp{{Old: c.old, New: c.new}}, "z.txt")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.Fuzzy {
				t.Errorf("expected fuzzy match")
			}
			if res.Content != c.want {
				t.Errorf("content:\n got %q\nwant %q", res.Content, c.want)
			}
		})
	}
}

func TestEditApplyFuzzyUntouchedLinesByteIdentical(t *testing.T) {
	// Many "dirty" lines (trailing whitespace, smart quotes, dashes, NBSP,
	// fullwidth) around one fuzzy edit: every line except the touched one
	// must survive byte for byte.
	lines := []string{
		"// header — “quoted”   ",
		"\tvar a = ‘x’\t",
		"ｆｕｌｌ width ﬁ",
		"target “line”   ",
		"tail – end  ",
		"",
		"last　line",
	}
	content := strings.Join(lines, "\n") + "\n"
	res, err := applyEdits(content, []editOp{{Old: `target "line"`, New: `target "LINE"`}}, "u.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(res.Content, "\n")
	if len(got) != len(lines)+1 {
		t.Fatalf("line count changed: %d vs %d", len(got), len(lines)+1)
	}
	for i, l := range lines {
		if i == 3 {
			if got[i] != `target "LINE"` {
				t.Errorf("touched line = %q", got[i])
			}
			continue
		}
		if got[i] != l {
			t.Errorf("untouched line %d changed: %q -> %q", i+1, l, got[i])
		}
	}
}

func TestEditApplyFuzzyMultiEditMixed(t *testing.T) {
	// One exact edit + one that needs fuzzy: whole op runs in normalized space,
	// but untouched lines keep their bytes.
	content := "one  \ntwo\nthree “q”\nfour\t\nfive  \n"
	res, err := applyEdits(content, []editOp{
		{Old: "two", New: "TWO"},
		{Old: `three "q"`, New: `three "Q"`},
	}, "mx.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "one  \nTWO\nthree \"Q\"\nfour\t\nfive  \n"; res.Content != want {
		t.Errorf("got %q want %q", res.Content, want)
	}
	if !res.Fuzzy || res.Count != 2 {
		t.Errorf("fuzzy=%v count=%d", res.Fuzzy, res.Count)
	}

	// exact edit whose old_string has trailing whitespace (exact in the original)
	// still matches once the operation switches to normalized space.
	content = "keep\t\nalpha   \nbeta\ngamma “g”\n"
	res, err = applyEdits(content, []editOp{
		{Old: "alpha   \nbeta", New: "ALPHA\nBETA"},
		{Old: `gamma "g"`, New: "GAMMA"},
	}, "mx.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "keep\t\nALPHA\nBETA\nGAMMA\n"; res.Content != want {
		t.Errorf("got %q want %q", res.Content, want)
	}

	// two fuzzy edits on the same line are grouped and both applied
	res, err = applyEdits("f(“a”, “b”)  \nz\n", []editOp{{Old: `"a"`, New: "1"}, {Old: `"b"`, New: "2"}}, "mx.txt")
	if err != nil || res.Content != "f(1, 2)\nz\n" {
		t.Errorf("same-line fuzzy edits: %v %+v", err, res)
	}
}

func TestEditApplyFuzzyDuplicateNormalized(t *testing.T) {
	// The normalized text appears twice => ambiguity error, nothing applied.
	_, err := applyEdits("x = “a”\nx = \"a\"  \nx = 1\n", []editOp{{Old: "x = \"a\"\nx = 1", New: "y"}}, "fd.txt")
	if err != nil {
		t.Fatalf("unique multi-line target should match: %v", err)
	}
	_, err = applyEdits("a—b  \nmid\na-b  \n", []editOp{{Old: "a-b\n", New: "c\n"}}, "fd.txt")
	if err == nil {
		t.Fatalf("expected an error (either not-unique)")
	}
	if !strings.HasPrefix(err.Error(), "Found 2 occurrences of the text in fd.txt.") {
		t.Errorf("got %v", err)
	}
	// Duplicate normalized lines must not be aligned to the wrong occurrence.
	res, err := applyEdits("dup  \nX—\ndup\n", []editOp{{Old: "X-", New: "Y"}}, "fd.txt")
	if err != nil || res.Content != "dup  \nY\ndup\n" {
		t.Errorf("got %v %+v", err, res)
	}
}

func TestEditApplyFuzzyTrailingFormFeed(t *testing.T) {
	content := "a\f\nb\n"
	res, err := applyEdits(content, []editOp{{Old: "a\nb", New: "a\nB"}}, "ff.txt")
	if err != nil {
		t.Fatalf("pi matches this via trimEnd(): %v", err)
	}
	if res.Content != "a\nB\n" {
		t.Errorf("got %q", res.Content)
	}
}

// ---------- line-number prefix repair ----------

func TestEditStripLineNumbers(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"     2\tbar\n     3\tbaz", "bar\nbaz", true},
		{"12\tx", "x", true},
		{"     1\t\n     2\tfoo", "\nfoo", true}, // empty file line still has prefix
		{"     1\tfoo\n\n     3\tbar", "foo\n\nbar", true},
		{"     1\tfoo\nbar", "     1\tfoo\nbar", false}, // not every line has it
		{"foo", "foo", false},
		{"", "", false},
		{"\n\n", "\n\n", false},
		{"  1\t  indented", "  indented", true},
	}
	for _, c := range cases {
		got, ok := stripLineNumbers(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("stripLineNumbers(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestEditApplyLineNumberPrefixRepair(t *testing.T) {
	content := "foo\nbar\nbaz\n"
	// both old and new carry prefixes
	res, err := applyEdits(content, []editOp{{Old: "     2\tbar\n     3\tbaz", New: "     2\tBAR\n     3\tbaz"}}, "p.txt")
	if err != nil || res.Content != "foo\nBAR\nbaz\n" {
		t.Errorf("both prefixed: %v %+v", err, res)
	}
	// only old carries them: new is kept verbatim
	res, err = applyEdits(content, []editOp{{Old: "     2\tbar", New: "BAR"}}, "p.txt")
	if err != nil || res.Content != "foo\nBAR\nbaz\n" {
		t.Errorf("old prefixed: %v %+v", err, res)
	}
	// literal tab-number text in a file must NOT be stripped when it matches as-is
	tsv := "1\tone\n2\ttwo\n"
	res, err = applyEdits(tsv, []editOp{{Old: "2\ttwo", New: "2\tTWO"}}, "t.tsv")
	if err != nil || res.Content != "1\tone\n2\tTWO\n" {
		t.Errorf("tsv: %v %+v", err, res)
	}
	// multi-edit: repair applies per edit
	res, err = applyEdits(content, []editOp{{Old: "     1\tfoo", New: "FOO"}, {Old: "baz", New: "BAZ"}}, "p.txt")
	if err != nil || res.Content != "FOO\nbar\nBAZ\n" {
		t.Errorf("multi: %v %+v", err, res)
	}
	// prefixes + fuzzy content
	res, err = applyEdits("a\nsay “hi”  \nc\n", []editOp{{Old: "     2\tsay \"hi\"", New: "say \"yo\""}}, "p.txt")
	if err != nil || res.Content != "a\nsay \"yo\"\nc\n" {
		t.Errorf("prefix+fuzzy: %v %+v", err, res)
	}
}

// ---------- hunks ----------

func TestEditBuildHunksMultipleEdits(t *testing.T) {
	content := "a\nb\nc\nd\ne\n"
	res, err := applyEdits(content, []editOp{
		{Old: "d", New: "D"},          // line 4 originally
		{Old: "a", New: "a1\na2\na3"}, // +2 lines at line 1
		{Old: "b\nc", New: "bc"},      // -1 line at line 2
	}, "h.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "a1\na2\na3\nbc\nD\ne\n"; res.Content != want {
		t.Fatalf("content %q", res.Content)
	}
	want := []editHunk{
		{Line: 1, Old: []string{"a"}, New: []string{"a1", "a2", "a3"}},
		{Line: 4, Old: []string{"b", "c"}, New: []string{"bc"}},
		{Line: 5, Old: []string{"d"}, New: []string{"D"}},
	}
	if !reflect.DeepEqual(res.Hunks, want) {
		t.Errorf("hunks:\n got %+v\nwant %+v", res.Hunks, want)
	}
	newLines := strings.Split(res.Content, "\n")
	for _, h := range res.Hunks {
		if got := newLines[h.Line-1]; got != h.New[0] {
			t.Errorf("hunk at line %d: new file has %q, hunk says %q", h.Line, got, h.New[0])
		}
	}
}

func TestEditBuildHunksReplaceAllAndDelete(t *testing.T) {
	res, err := applyEdits("x\ny\nx\ny\nx\n", []editOp{{Old: "y\n", New: "", All: true}}, "h.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "x\nx\nx\n" {
		t.Fatalf("content %q", res.Content)
	}
	if len(res.Hunks) != 2 {
		t.Fatalf("hunks %+v", res.Hunks)
	}
	if res.Hunks[0].Line != 2 || res.Hunks[1].Line != 3 {
		t.Errorf("line numbers %d,%d want 2,3", res.Hunks[0].Line, res.Hunks[1].Line)
	}
	for _, h := range res.Hunks {
		if h.New != nil {
			t.Errorf("deletion hunk should have nil New, got %#v", h.New)
		}
	}
}

func TestEditBuildHunksTrailingNewlinePhantomLine(t *testing.T) {
	res, err := applyEdits("a\nb\nc\n", []editOp{{Old: "b\n", New: "B\n"}}, "h.txt")
	if err != nil {
		t.Fatal(err)
	}
	h := res.Hunks[0]
	if !reflect.DeepEqual(h.Old, []string{"b"}) || !reflect.DeepEqual(h.New, []string{"B"}) {
		t.Errorf("hunk Old=%q New=%q, want [b] [B]", h.Old, h.New)
	}
	res, err = applyEdits("a\nb\nc\n", []editOp{{Old: "b\n", New: ""}}, "h.txt")
	if err != nil {
		t.Fatal(err)
	}
	if h := res.Hunks[0]; !reflect.DeepEqual(h.Old, []string{"b"}) {
		t.Errorf("deleting one line: hunk Old=%q, want [b]", h.Old)
	}
}

func TestEditFuzzyNormKeepsLineCount(t *testing.T) {
	in := "a  \n“b”\t\n\n　c \nﬁ—\n"
	out := fuzzyNorm(in)
	if strings.Count(in, "\n") != strings.Count(out, "\n") {
		t.Fatalf("line count changed: %q", out)
	}
	if want := "a\n\"b\"\n\n c\nfi-\n"; out != want {
		t.Errorf("fuzzyNorm = %q want %q", out, want)
	}
}

// ---------- Toolbox.edit ----------

func TestEditToolExactAndOutput(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "src", "a.go")
	editPut(t, p, "package a\n\nfunc F() int {\n\treturn 1\n}\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "src/a.go", "old_string": "return 1", "new_string": "return 2"})
	if out.IsErr {
		t.Fatalf("edit failed: %s", out.Text)
	}
	if got := editGet(t, p); got != "package a\n\nfunc F() int {\n\treturn 2\n}\n" {
		t.Errorf("file = %q", got)
	}
	if !strings.HasPrefix(out.Text, "Edited src/a.go (1 replacement). Lines 1-6 now:\n") {
		t.Errorf("text = %q", out.Text)
	}
	if !strings.Contains(out.Text, "     4\t\treturn 2\n") {
		t.Errorf("post-edit view missing the edited line: %q", out.Text)
	}
	if strings.Contains(out.Text, "Note: matched after normalizing") {
		t.Errorf("exact edit must not carry fuzzy note")
	}
	if out.Display.Summary != "+1 −1" || out.Display.Path != filepath.Join("src", "a.go") {
		t.Errorf("display = %+v", out.Display)
	}
	if len(out.Display.Diff) != 1 || out.Display.Diff[0].Line != 4 {
		t.Errorf("diff = %+v", out.Display.Diff)
	}
}

func TestEditToolPreservesMode(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "run.sh")
	editPut(t, p, "#!/bin/sh\necho hi\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := editExec(t, tb, "edit", map[string]any{"path": p, "old_string": "hi", "new_string": "yo"}); out.IsErr {
		t.Fatal(out.Text)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestEditToolReplaceAllCount(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "r.txt")
	editPut(t, p, "foo\nfoo\nfoo\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "r.txt", "old_string": "foo", "new_string": "bar", "replace_all": true})
	if out.IsErr || !strings.HasPrefix(out.Text, "Edited r.txt (3 replacements).") {
		t.Fatalf("out = %+v", out)
	}
	if editGet(t, p) != "bar\nbar\nbar\n" {
		t.Errorf("file %q", editGet(t, p))
	}
	// replace_all passed as string "true" (weak models)
	editPut(t, p, "x x\n")
	tb2 := NewToolbox(dir)
	out = editExec(t, tb2, "edit", map[string]any{"path": "r.txt", "old_string": "x", "new_string": "y", "replace_all": "true"})
	if out.IsErr || editGet(t, p) != "y y\n" {
		t.Errorf("string replace_all: %+v / %q", out, editGet(t, p))
	}
	// duplicate without replace_all is refused with pi wording
	editPut(t, p, "x x\n")
	tb3 := NewToolbox(dir)
	out = editExec(t, tb3, "edit", map[string]any{"path": "r.txt", "old_string": "x", "new_string": "y"})
	if !out.IsErr || !strings.HasPrefix(out.Text, "Found 2 occurrences of the text in r.txt.") {
		t.Errorf("dup: %+v", out)
	}
	if editGet(t, p) != "x x\n" {
		t.Errorf("file modified on error")
	}
}

func TestEditToolMultiEditAtomic(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "m.txt")
	orig := "alpha\nbeta\ngamma\n"
	editPut(t, p, orig)
	before, _ := os.Stat(p)
	cases := []struct {
		name  string
		edits []any
		msg   string
	}{
		{"second not found", []any{
			map[string]any{"old_string": "alpha", "new_string": "A"},
			map[string]any{"old_string": "nope", "new_string": "N"},
		}, "Could not find edits[1] in m.txt."},
		{"first duplicate", []any{
			map[string]any{"old_string": "a\n", "new_string": "X\n"},
			map[string]any{"old_string": "beta", "new_string": "B"},
		}, "Found 3 occurrences of edits[0] in m.txt."},
		{"overlap", []any{
			map[string]any{"old_string": "beta\ngamma", "new_string": "Z"},
			map[string]any{"old_string": "gamma", "new_string": "G"},
		}, "edits[0] and edits[1] overlap in m.txt."},
		{"empty old", []any{
			map[string]any{"old_string": "beta", "new_string": "B"},
			map[string]any{"old_string": "", "new_string": "E"},
		}, "edits[1].old_string must not be empty in m.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := editExec(t, tb, "edit", map[string]any{"path": "m.txt", "edits": c.edits})
			if !out.IsErr || !strings.HasPrefix(out.Text, c.msg) {
				t.Errorf("got %+v, want prefix %q", out, c.msg)
			}
			if got := editGet(t, p); got != orig {
				t.Errorf("file changed on failed edit: %q", got)
			}
			after, _ := os.Stat(p)
			if !after.ModTime().Equal(before.ModTime()) {
				t.Errorf("file was rewritten on failed edit")
			}
		})
	}
	// and a successful multi-edit using aliases inside edits[]
	out := editExec(t, tb, "edit", map[string]any{"path": "m.txt", "edits": []any{
		map[string]any{"old_str": "alpha", "new_str": "A"},
		map[string]any{"oldText": "gamma", "newText": "G"},
	}})
	if out.IsErr || editGet(t, p) != "A\nbeta\nG\n" || !strings.Contains(out.Text, "(2 replacements)") {
		t.Errorf("multi ok: %+v %q", out, editGet(t, p))
	}
}

func TestEditToolCRLF(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "w.txt")
	editPut(t, p, "one\r\ntwo\r\nthree\r\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "w.txt", "old_string": "two\nthree", "new_string": "TWO\nnew\nTHREE"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	got := editGet(t, p)
	if want := "one\r\nTWO\r\nnew\r\nTHREE\r\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("bare LF leaked into CRLF file: %q", got)
	}
	// old_string sent with CRLF also works; new_string with CRLF doesn't double \r
	out = editExec(t, tb, "edit", map[string]any{"path": "w.txt", "old_string": "one\r\nTWO", "new_string": "1\r\n2"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if got := editGet(t, p); got != "1\r\n2\r\nnew\r\nTHREE\r\n" {
		t.Errorf("got %q", got)
	}
	// fuzzy edit in a CRLF file keeps CRLF and untouched dirty lines
	editPut(t, p, "a  \r\nb “c”\r\nd\t\r\n")
	tb2 := NewToolbox(dir)
	out = editExec(t, tb2, "edit", map[string]any{"path": "w.txt", "old_string": `b "c"`, "new_string": `b "C"`})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if got := editGet(t, p); got != "a  \r\nb \"C\"\r\nd\t\r\n" {
		t.Errorf("fuzzy CRLF got %q", got)
	}
	if !strings.Contains(out.Text, "Note: matched after normalizing") {
		t.Errorf("fuzzy note missing: %q", out.Text)
	}
}

func TestEditToolMixedLineEndings(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "mixed.txt")
	editPut(t, p, "a\nb\nc\r\nd\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "mixed.txt", "old_string": "b", "new_string": "B"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	got := editGet(t, p)
	if got != "a\nB\nc\nd\n" && got != "a\nB\nc\r\nd\n" {
		t.Errorf("LF file with one stray CRLF was rewritten as %q", got)
	}
}

func TestEditToolBOM(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "bom.txt")
	editPut(t, p, editBOM+"first\nsecond\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "bom.txt", "old_string": "first", "new_string": "FIRST"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if got := editGet(t, p); got != editBOM+"FIRST\nsecond\n" {
		t.Errorf("got %q", got)
	}
	// BOM + CRLF together
	editPut(t, p, editBOM+"x\r\ny\r\n")
	tb2 := NewToolbox(dir)
	out = editExec(t, tb2, "edit", map[string]any{"path": "bom.txt", "old_string": "x\ny", "new_string": "X\nY"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if got := editGet(t, p); got != editBOM+"X\r\nY\r\n" {
		t.Errorf("got %q", got)
	}
	// file without BOM doesn't gain one
	editPut(t, p, "plain\n")
	tb3 := NewToolbox(dir)
	editExec(t, tb3, "edit", map[string]any{"path": "bom.txt", "old_string": "plain", "new_string": "p"})
	if got := editGet(t, p); got != "p\n" {
		t.Errorf("got %q", got)
	}
}

func TestEditToolReadOutputRoundTrip(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "rt.py")
	editPut(t, p, "def f():\n    x = 1\n\n    return x\n")
	r := editExec(t, tb, "read", map[string]any{"path": "rt.py"})
	if r.IsErr {
		t.Fatal(r.Text)
	}
	// model copies lines 2-4 verbatim from read output, including prefixes
	lines := strings.Split(r.Text, "\n")
	old := strings.Join(lines[1:4], "\n")
	if !strings.HasPrefix(old, "     2\t    x = 1") {
		t.Fatalf("unexpected read output %q", r.Text)
	}
	out := editExec(t, tb, "edit", map[string]any{"path": "rt.py", "old_string": old, "new_string": "    x = 2\n\n    return x"})
	if out.IsErr {
		t.Fatalf("edit with read prefixes failed: %s", out.Text)
	}
	if got := editGet(t, p); got != "def f():\n    x = 2\n\n    return x\n" {
		t.Errorf("got %q", got)
	}
}

func TestEditToolStaleDetection(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "s.txt")
	editPut(t, p, "v1\n")
	if r := editExec(t, tb, "read", map[string]any{"path": "s.txt"}); r.IsErr {
		t.Fatal(r.Text)
	}
	// external change (different size)
	editPut(t, p, "v1 changed\n")
	editBumpMtime(t, p)
	out := editExec(t, tb, "edit", map[string]any{"path": "s.txt", "old_string": "v1", "new_string": "v2"})
	if !out.IsErr || !strings.HasPrefix(out.Text, "s.txt changed since you last read it") {
		t.Fatalf("stale edit not refused: %+v", out)
	}
	if editGet(t, p) != "v1 changed\n" {
		t.Errorf("stale edit modified file")
	}
	// same size, different mtime is still stale
	editExec(t, tb, "read", map[string]any{"path": "s.txt"})
	editPut(t, p, "V1 changed\n")
	editBumpMtime(t, p)
	out = editExec(t, tb, "edit", map[string]any{"path": "s.txt", "old_string": "changed", "new_string": "x"})
	if !out.IsErr || !strings.Contains(out.Text, "changed since you last read it") {
		t.Fatalf("same-size stale not refused: %+v", out)
	}
	// re-read clears staleness
	editExec(t, tb, "read", map[string]any{"path": "s.txt"})
	out = editExec(t, tb, "edit", map[string]any{"path": "s.txt", "old_string": "changed", "new_string": "x"})
	if out.IsErr {
		t.Fatalf("edit after re-read failed: %s", out.Text)
	}
	// consecutive edits don't need re-reading (edit re-stamps)
	out = editExec(t, tb, "edit", map[string]any{"path": "s.txt", "old_string": "x", "new_string": "y"})
	if out.IsErr || editGet(t, p) != "V1 y\n" {
		t.Fatalf("second edit: %+v %q", out, editGet(t, p))
	}
	// a change after the edit is detected again
	editPut(t, p, "external\n")
	editBumpMtime(t, p)
	out = editExec(t, tb, "edit", map[string]any{"path": "s.txt", "old_string": "external", "new_string": "z"})
	if !out.IsErr {
		t.Fatalf("change after edit not detected")
	}
}

func TestEditToolUnreadFileAllowed(t *testing.T) {
	// edit does not require a prior read (only write does); stale check applies only after a read
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "u.txt")
	editPut(t, p, "hello\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "u.txt", "old_string": "hello", "new_string": "bye"})
	if out.IsErr || editGet(t, p) != "bye\n" {
		t.Fatalf("%+v", out)
	}
}

func TestEditToolErrorsAndValidation(t *testing.T) {
	tb, dir := editNewToolbox(t)
	editPut(t, filepath.Join(dir, "f.txt"), "abc\n")
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing path", map[string]any{"old_string": "a", "new_string": "b"}, `Validation failed for tool "edit": missing required parameter 'path'`},
		{"missing old", map[string]any{"path": "f.txt", "new_string": "b"}, "missing required parameter 'old_string' (or 'edits')"},
		{"missing new", map[string]any{"path": "f.txt", "old_string": "a"}, "missing required parameter 'new_string'"},
		{"edits not object", map[string]any{"path": "f.txt", "edits": []any{"x"}}, "edits[0] must be an object"},
		{"edits missing new", map[string]any{"path": "f.txt", "edits": []any{map[string]any{"old_string": "a"}}}, "edits[0] needs old_string and new_string"},
		{"no such file", map[string]any{"path": "nope.txt", "old_string": "a", "new_string": "b"}, "nope.txt does not exist. Use write to create it."},
		{"empty old single", map[string]any{"path": "f.txt", "old_string": "", "new_string": "b"}, "old_string must not be empty in f.txt."},
		{"no change", map[string]any{"path": "f.txt", "old_string": "abc", "new_string": "abc"}, "No changes made to f.txt."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := editExec(t, tb, "edit", c.args)
			if !out.IsErr || !strings.Contains(out.Text, c.want) {
				t.Errorf("got %+v, want containing %q", out, c.want)
			}
		})
	}
	if editGet(t, filepath.Join(dir, "f.txt")) != "abc\n" {
		t.Errorf("file modified by failing calls")
	}
	// tool + parameter aliases
	out := tb.Exec(context.Background(), "str_replace", `{"file_path":"f.txt","old_str":"abc","new_str":"xyz"}`)
	if out.IsErr || editGet(t, filepath.Join(dir, "f.txt")) != "xyz\n" {
		t.Errorf("aliases: %+v", out)
	}
	// numbers are stringified by argStr
	editPut(t, filepath.Join(dir, "n.txt"), "v = 1\n")
	tb2 := NewToolbox(dir)
	out = tb2.Exec(context.Background(), "edit", `{"path":"n.txt","old_string":"1","new_string":2}`)
	if out.IsErr || editGet(t, filepath.Join(dir, "n.txt")) != "v = 2\n" {
		t.Errorf("numeric new_string: %+v", out)
	}
}

func TestEditToolStringifiedEdits(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "se.txt")
	editPut(t, p, "a\nb\n")
	out := editExec(t, tb, "edit", map[string]any{"path": "se.txt", "edits": `[{"old_string":"a","new_string":"A"},{"old_string":"b","new_string":"B"}]`})
	if out.IsErr || editGet(t, p) != "A\nB\n" {
		t.Errorf("stringified edits: %+v %q", out, editGet(t, p))
	}
}

// ---------- Toolbox.write ----------

func TestEditWriteRules(t *testing.T) {
	tb, dir := editNewToolbox(t)
	// new file, nested dirs created
	out := editExec(t, tb, "write", map[string]any{"path": "deep/er/n.txt", "content": "a\nb\nc"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if want := "Wrote 3 lines (5 B) to deep/er/n.txt."; out.Text != want {
		t.Errorf("text %q want %q", out.Text, want)
	}
	if editGet(t, filepath.Join(dir, "deep/er/n.txt")) != "a\nb\nc" {
		t.Errorf("content mismatch")
	}
	// a file the tool itself wrote can be overwritten without reading
	out = editExec(t, tb, "write", map[string]any{"path": "deep/er/n.txt", "content": "x\n"})
	if out.IsErr || out.Text != "Wrote 1 lines (2 B) to deep/er/n.txt." {
		t.Errorf("rewrite own file: %+v", out)
	}

	// existing unread file is refused
	p := filepath.Join(dir, "exist.txt")
	editPut(t, p, "precious\n")
	out = editExec(t, tb, "write", map[string]any{"path": "exist.txt", "content": "clobber"})
	if !out.IsErr || !strings.HasPrefix(out.Text, "exist.txt already exists. Read it first before overwriting it") {
		t.Errorf("unread overwrite: %+v", out)
	}
	if editGet(t, p) != "precious\n" {
		t.Fatalf("unread file clobbered")
	}
	// after read it's allowed, mode preserved
	os.Chmod(p, 0o600)
	editExec(t, tb, "read", map[string]any{"path": "exist.txt"})
	out = editExec(t, tb, "write", map[string]any{"path": "exist.txt", "content": "new\n"})
	if out.IsErr || editGet(t, p) != "new\n" {
		t.Errorf("write after read: %+v", out)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v", fi.Mode().Perm())
	}
	// stale: changed after read
	editPut(t, p, "changed externally\n")
	editBumpMtime(t, p)
	out = editExec(t, tb, "write", map[string]any{"path": "exist.txt", "content": "mine\n"})
	if !out.IsErr || !strings.HasPrefix(out.Text, "exist.txt changed since you last read it.") {
		t.Errorf("stale write: %+v", out)
	}
	if editGet(t, p) != "changed externally\n" {
		t.Errorf("stale write clobbered file")
	}
	// directory target
	os.Mkdir(filepath.Join(dir, "adir"), 0o755)
	out = editExec(t, tb, "write", map[string]any{"path": "adir", "content": "x"})
	if !out.IsErr || out.Text != "adir is a directory." {
		t.Errorf("dir: %+v", out)
	}
	// validation
	out = editExec(t, tb, "write", map[string]any{"path": "x.txt"})
	if !out.IsErr || !strings.Contains(out.Text, "missing required parameter 'content'") {
		t.Errorf("missing content: %+v", out)
	}
	out = editExec(t, tb, "write", map[string]any{"content": "x"})
	if !out.IsErr || !strings.Contains(out.Text, "missing required parameter 'path'") {
		t.Errorf("missing path: %+v", out)
	}
	// empty content is valid and writes an empty file
	out = editExec(t, tb, "write", map[string]any{"path": "empty.txt", "content": ""})
	if out.IsErr || out.Text != "Wrote 0 lines (0 B) to empty.txt." {
		t.Errorf("empty write: %+v", out)
	}
	// write then edit works without a read
	out = editExec(t, tb, "edit", map[string]any{"path": "deep/er/n.txt", "old_string": "x", "new_string": "y"})
	if out.IsErr {
		t.Errorf("edit after write: %s", out.Text)
	}
}

// ---------- Toolbox.read ----------

func TestEditReadBasic(t *testing.T) {
	tb, dir := editNewToolbox(t)
	editPut(t, filepath.Join(dir, "r.txt"), editBOM+"a\r\nb\r\n\r\nd")
	out := editExec(t, tb, "read", map[string]any{"path": "r.txt"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	if want := "     1\ta\n     2\tb\n     3\t\n     4\td\n"; out.Text != want {
		t.Errorf("read = %q want %q", out.Text, want)
	}
	if out.Display.Summary != "4 linhas" {
		t.Errorf("summary %q", out.Display.Summary)
	}
	// empty file
	editPut(t, filepath.Join(dir, "e.txt"), "")
	out = editExec(t, tb, "read", map[string]any{"path": "e.txt"})
	if out.IsErr || out.Text != "e.txt is empty." {
		t.Errorf("empty: %+v", out)
	}
	// missing
	out = editExec(t, tb, "read", map[string]any{"path": "zz.txt"})
	if !out.IsErr || !strings.Contains(out.Text, "Cannot read zz.txt") || !strings.Contains(out.Text, "Use bash (fd/find/ls) to locate it.") {
		t.Errorf("missing: %+v", out)
	}
	// missing path parameter
	out = editExec(t, tb, "read", map[string]any{})
	if !out.IsErr || !strings.Contains(out.Text, "missing required parameter 'path'") {
		t.Errorf("no path: %+v", out)
	}
}

func TestEditReadOffsetLimit(t *testing.T) {
	tb, dir := editNewToolbox(t)
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	editPut(t, filepath.Join(dir, "ten.txt"), sb.String())
	cases := []struct {
		name     string
		args     map[string]any
		first    string
		last     string
		footer   string
		summary  string
		isErr    bool
		errMatch string
	}{
		{"offset+limit", map[string]any{"offset": 3, "limit": 4}, "     3\tline3", "     6\tline6", "[Showing lines 3-6 of 10. Use offset=7 to continue.]", "linhas 3-6 de 10", false, ""},
		{"offset only", map[string]any{"offset": 8}, "     8\tline8", "    10\tline10", "", "linhas 8-10 de 10", false, ""},
		{"limit only", map[string]any{"limit": 2}, "     1\tline1", "     2\tline2", "[Showing lines 1-2 of 10. Use offset=3 to continue.]", "linhas 1-2 de 10", false, ""},
		{"offset zero", map[string]any{"offset": 0, "limit": 1}, "     1\tline1", "     1\tline1", "Use offset=2", "linhas 1-1 de 10", false, ""},
		{"limit zero => default", map[string]any{"limit": 0}, "     1\tline1", "    10\tline10", "", "10 linhas", false, ""},
		{"string numbers", map[string]any{"offset": "9", "limit": "1"}, "     9\tline9", "     9\tline9", "Use offset=10", "linhas 9-9 de 10", false, ""},
		{"alias start_line", map[string]any{"start_line": 10}, "    10\tline10", "    10\tline10", "", "linhas 10-10 de 10", false, ""},
		{"offset past end", map[string]any{"offset": 11}, "", "", "", "", true, "offset 11 is past the end of ten.txt (10 lines)."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := map[string]any{"path": "ten.txt"}
			for k, v := range c.args {
				args[k] = v
			}
			out := editExec(t, tb, "read", args)
			if c.isErr {
				if !out.IsErr || out.Text != c.errMatch {
					t.Errorf("got %+v", out)
				}
				return
			}
			if out.IsErr {
				t.Fatal(out.Text)
			}
			body := out.Text
			if i := strings.Index(body, "\n\n["); i >= 0 {
				body = body[:i+1]
			}
			ls := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
			if ls[0] != c.first || ls[len(ls)-1] != c.last {
				t.Errorf("first/last = %q/%q want %q/%q", ls[0], ls[len(ls)-1], c.first, c.last)
			}
			if c.footer == "" && strings.Contains(out.Text, "[Showing") {
				t.Errorf("unexpected footer: %q", out.Text)
			}
			if c.footer != "" && !strings.Contains(out.Text, c.footer) {
				t.Errorf("footer missing %q in %q", c.footer, out.Text)
			}
			if out.Display.Summary != c.summary {
				t.Errorf("summary %q want %q", out.Display.Summary, c.summary)
			}
		})
	}
}

func TestEditReadDefaultLineAndByteCaps(t *testing.T) {
	tb, dir := editNewToolbox(t)
	// 2500 short lines => capped at 2000 lines
	var sb strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&sb, "%d\n", i)
	}
	editPut(t, filepath.Join(dir, "many.txt"), sb.String())
	out := editExec(t, tb, "read", map[string]any{"path": "many.txt"})
	if !strings.Contains(out.Text, "  2000\t2000\n") || strings.Contains(out.Text, "\t2001\n") {
		t.Errorf("2000-line cap not applied")
	}
	if !strings.Contains(out.Text, "[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]") {
		t.Errorf("footer missing")
	}
	// long lines => byte cap (50 KB) before line cap
	sb.Reset()
	row := strings.Repeat("x", 199)
	for i := 0; i < 1000; i++ {
		sb.WriteString(row + "\n")
	}
	editPut(t, filepath.Join(dir, "wide.txt"), sb.String())
	out = editExec(t, tb, "read", map[string]any{"path": "wide.txt"})
	body := out.Text[:strings.Index(out.Text, "\n\n[Showing")+1]
	if len(body) > maxReadBytes {
		t.Errorf("body %d bytes exceeds cap %d", len(body), maxReadBytes)
	}
	n := strings.Count(body, "\n")
	if n < 200 || n >= 1000 {
		t.Errorf("unexpected line count %d", n)
	}
	if !strings.Contains(out.Text, fmt.Sprintf("[Showing lines 1-%d of 1000. Use offset=%d to continue.]", n, n+1)) {
		t.Errorf("byte-cap footer wrong: %q", out.Text[len(body):])
	}
}

func TestEditReadHugeLineClipped(t *testing.T) {
	tb, dir := editNewToolbox(t)
	editPut(t, filepath.Join(dir, "min.js"), "short\n"+strings.Repeat("a", 5000)+"\nend\n")
	out := editExec(t, tb, "read", map[string]any{"path": "min.js"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	want := "     2\t" + strings.Repeat("a", maxLineChars) + "… [line truncated, 5000 bytes]\n"
	if !strings.Contains(out.Text, want) {
		t.Errorf("huge line not clipped as expected")
	}
	if !strings.Contains(out.Text, "     3\tend\n") {
		t.Errorf("lines after the huge one missing")
	}
	// exactly at the limit: untouched
	if got := clipLine(strings.Repeat("b", maxLineChars)); got != strings.Repeat("b", maxLineChars) {
		t.Errorf("line at limit was clipped")
	}
}

func TestEditReadHugeLineUTF8Safe(t *testing.T) {
	tb, dir := editNewToolbox(t)
	editPut(t, filepath.Join(dir, "u.txt"), strings.Repeat("€", 1000)+"\n")
	out := editExec(t, tb, "read", map[string]any{"path": "u.txt"})
	if !utf8.ValidString(out.Text) {
		t.Errorf("read output is not valid UTF-8")
	}
}

func TestEditReadDirectory(t *testing.T) {
	tb, dir := editNewToolbox(t)
	editPut(t, filepath.Join(dir, "d", "b.txt"), "12345")
	editPut(t, filepath.Join(dir, "d", "a.txt"), "")
	os.MkdirAll(filepath.Join(dir, "d", "zsub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "d", "asub"), 0o755)
	out := editExec(t, tb, "read", map[string]any{"path": "d"})
	if out.IsErr {
		t.Fatal(out.Text)
	}
	want := "Directory d (4 entries):\nasub/\nzsub/\na.txt  (0 B)\nb.txt  (5 B)\n"
	if out.Text != want {
		t.Errorf("got %q want %q", out.Text, want)
	}
	if out.Display.Summary != "4 itens" {
		t.Errorf("summary %q", out.Display.Summary)
	}
	// >500 entries are cut
	big := filepath.Join(dir, "big")
	os.MkdirAll(big, 0o755)
	for i := 0; i < 503; i++ {
		os.WriteFile(filepath.Join(big, fmt.Sprintf("f%03d", i)), nil, 0o644)
	}
	out = editExec(t, tb, "read", map[string]any{"path": "big"})
	if !strings.HasPrefix(out.Text, "Directory big (503 entries):\n") || !strings.HasSuffix(out.Text, "… 3 more\n") {
		t.Errorf("big dir: %q…%q", out.Text[:40], out.Text[len(out.Text)-20:])
	}
	if strings.Contains(out.Text, "f500") {
		t.Errorf("entry beyond 500 listed")
	}
}

func TestEditReadBinaryAndTooLarge(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "img.bin")
	editPut(t, p, "PNG\x00\x01\x02")
	out := editExec(t, tb, "read", map[string]any{"path": "img.bin"})
	if !out.IsErr || !strings.HasPrefix(out.Text, "img.bin is a binary file (6 B).") {
		t.Errorf("binary: %+v", out)
	}
	// a binary read does not count as a read for write
	out = editExec(t, tb, "write", map[string]any{"path": "img.bin", "content": "x"})
	if !out.IsErr {
		t.Errorf("write over binary allowed after failed read")
	}
	// NUL only beyond the first 8 KiB is not detected (documented heuristic)
	editPut(t, filepath.Join(dir, "late.txt"), strings.Repeat("a", 9000)+"\x00\n")
	out = editExec(t, tb, "read", map[string]any{"path": "late.txt"})
	if out.IsErr {
		t.Errorf("late NUL treated as binary: %s", out.Text)
	}
	// > 25 MB refused (sparse file, cheap)
	big := filepath.Join(dir, "huge.log")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(26 << 20)
	f.Close()
	out = editExec(t, tb, "read", map[string]any{"path": "huge.log"})
	if !out.IsErr || !strings.Contains(out.Text, "too large to read") {
		t.Errorf("huge: %+v", out)
	}
}

func TestEditReadAbsoluteAndRelative(t *testing.T) {
	tb, dir := editNewToolbox(t)
	p := filepath.Join(dir, "x", "y.txt")
	editPut(t, p, "hi\n")
	for _, ps := range []string{p, "x/y.txt", "./x/../x/y.txt", "  x/y.txt  "} {
		out := editExec(t, tb, "read", map[string]any{"path": ps})
		if out.IsErr || out.Text != "     1\thi\n" {
			t.Errorf("read %q: %+v", ps, out)
		}
		if out.Display.Path != filepath.Join("x", "y.txt") {
			t.Errorf("display path %q", out.Display.Path)
		}
	}
	// read via one spelling, write via another: same canonical key
	out := editExec(t, tb, "write", map[string]any{"path": p, "content": "bye\n"})
	if out.IsErr {
		t.Errorf("write after read under another spelling: %s", out.Text)
	}
}
