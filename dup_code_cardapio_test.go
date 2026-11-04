//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dupCodeFixture copies jazz-scripts/testdata/dup_code into root (node_modules, a
// README.md copy of the block and a binary .py included: the script must skip them).
func dupCodeFixture(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join("jazz-scripts", "testdata", "dup_code")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		scWrite(t, root, r, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func dupCodeOccs(g map[string]any) []string {
	var out []string
	for _, o := range g["occurrences"].([]any) {
		m := o.(map[string]any)
		out = append(out, fmt.Sprintf("%s:%v-%v", m["file"], m["start"], m["end"]))
	}
	return out
}

// TestDupCodeCardapio runs jazz-scripts/dup_code.py through the script tool (same path
// the agent uses): two Python files share an 8-line block that differs only in
// comments, blank lines and spacing; two JS files share a 6-line block; util.py repeats
// nothing.
func TestDupCodeCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	dupCodeFixture(t, root)

	v, out := scCall(t, tb, "dup_code", nil)
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("dup_code: %v\n%s", v, out.Text)
	}
	// orders.py, billing.py, util.py, app.js, admin.js; blob.py is binary, README.md is not code, node_modules is skipped
	if d["files_scanned"] != 5.0 || d["total_groups"] != 2.0 || d["total_dup_lines"] != 14.0 || d["truncated"] != false {
		t.Errorf("counts: %v", d)
	}
	groups := d["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups: %v", groups)
	}
	g0 := groups[0].(map[string]any)
	if g0["lines"] != 8.0 || g0["sample"] != "rows = []" {
		t.Errorf("biggest group first: %v", g0)
	}
	if got := strings.Join(dupCodeOccs(g0), " "); got != "src/billing.py:6-15 src/orders.py:6-13" {
		t.Errorf("python occurrences: %s", got)
	}
	g1 := groups[1].(map[string]any)
	if g1["lines"] != 6.0 {
		t.Errorf("js group: %v", g1)
	}
	if got := strings.Join(dupCodeOccs(g1), " "); got != "web/admin.js:3-9 web/app.js:3-9" {
		t.Errorf("js occurrences: %s", got)
	}
	for _, g := range groups {
		for _, o := range dupCodeOccs(g.(map[string]any)) {
			if strings.Contains(o, "node_modules") || strings.Contains(o, "README") || strings.Contains(o, "util.py") {
				t.Errorf("decoy matched: %s", o)
			}
		}
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "2 duplicate groups, 14 lines") {
		t.Errorf("text: %q", txt)
	}

	// a second run is byte-identical (deterministic)
	v2, _ := scCall(t, tb, "dup_code", nil)
	if fmt.Sprint(scDataOf(v2)) != fmt.Sprint(d) {
		t.Errorf("not deterministic:\n%v\n%v", scDataOf(v2), d)
	}

	// lang narrows (alias "py" accepted); a smaller window still merges into the same 8-line block
	v, _ = scCall(t, tb, "dup_code", map[string]any{"lang": "py", "window": 4})
	d = scDataOf(v)
	if !scOK(v) || d["files_scanned"] != 3.0 || d["total_groups"] != 1.0 || d["window"] != 4.0 {
		t.Errorf("lang=py: %v", d)
	}
	if g := d["groups"].([]any)[0].(map[string]any); g["lines"] != 8.0 {
		t.Errorf("window 4 should merge into 8 lines: %v", g)
	}

	// min_block drops the 6-line JS group; limit truncates
	v, _ = scCall(t, tb, "dup_code", map[string]any{"min_block": 8})
	if d = scDataOf(v); d["total_groups"] != 1.0 || d["truncated"] != false {
		t.Errorf("min_block: %v", d)
	}
	v, _ = scCall(t, tb, "dup_code", map[string]any{"limit": 1})
	if d = scDataOf(v); d["total_groups"] != 2.0 || len(d["groups"].([]any)) != 1 || d["truncated"] != true {
		t.Errorf("limit: %v", d)
	}

	// path narrows to a subtree; a single file with nothing repeated is empty, not an error
	v, _ = scCall(t, tb, "dup_code", map[string]any{"path": "web"})
	if d = scDataOf(v); d["files_scanned"] != 2.0 || d["total_groups"] != 1.0 {
		t.Errorf("subtree: %v", d)
	}
	v, _ = scCall(t, tb, "dup_code", map[string]any{"path": "src/util.py"})
	if d = scDataOf(v); !scOK(v) || d["total_groups"] != 0.0 || d["files_scanned"] != 1.0 {
		t.Errorf("single file: %v", d)
	}

	// refusals: unknown language and missing path fail inside the script (JSON, ok=false)
	for _, args := range []map[string]any{{"lang": "cobol"}, {"path": "nope"}} {
		if v, _ = scCall(t, tb, "dup_code", args); scOK(v) {
			t.Errorf("%v must fail: %v", args, v)
		}
	}
	// a window below the schema minimum is refused by the harness before the script runs
	b, _ := json.Marshal(map[string]any{"name": "dup_code", "args": map[string]any{"window": 2}})
	if o := tb.Exec(context.Background(), "script", string(b)); !o.IsErr || !strings.Contains(o.Text, "Validation failed") {
		t.Errorf("window=2 must fail validation: %v", o.Text)
	}
}
