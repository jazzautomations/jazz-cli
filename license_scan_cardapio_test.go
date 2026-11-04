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

// licenseScanFixture copies jazz-scripts/testdata/license_scan into root: a MIT LICENSE,
// package.json (MIT), tools/pyproject.toml (GPL-3.0), a CC0 docs/LICENSE, a Go file
// with an Apache-2.0 SPDX header, a Python file with a MIT header, one file with no
// header, and a vendor/ tree the script must skip.
func licenseScanFixture(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join("jazz-scripts", "testdata", "license_scan")
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

// TestLicenseScanCardapio runs jazz-scripts/license_scan.py through the script tool
// (same path the agent uses) over the fixture tree.
func TestLicenseScanCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	licenseScanFixture(t, root)

	v, out := scCall(t, tb, "license_scan", nil)
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("license_scan: %v\n%s", v, out.Text)
	}
	var pl []string
	for _, e := range d["project_licenses"].([]any) {
		m := e.(map[string]any)
		pl = append(pl, fmt.Sprintf("%s=%s@%v", m["file"], m["id"], m["confidence"]))
	}
	if got := strings.Join(pl, " "); got != "LICENSE=MIT@0.95 docs/LICENSE=CC0-1.0@0.9" {
		t.Errorf("project_licenses: %s", got)
	}
	var ml []string
	for _, e := range d["manifest_licenses"].([]any) {
		m := e.(map[string]any)
		ml = append(ml, fmt.Sprintf("%s=%s", m["file"], m["id"]))
	}
	if got := strings.Join(ml, " "); got != "package.json=MIT tools/pyproject.toml=GPL-3.0" {
		t.Errorf("manifest_licenses: %s", got)
	}
	hdr := d["spdx_headers"].(map[string]any)
	if hdr["Apache-2.0"] != 1.0 || hdr["MIT"] != 1.0 || len(hdr) != 2 {
		t.Errorf("spdx_headers: %v", hdr)
	}
	// src/server.go, src/util.go, src/cli.py; vendor/lib/lib.go is skipped
	if d["files_scanned"] != 3.0 || d["files_with_header"] != 2.0 || d["files_without_header"] != 1.0 || d["truncated"] != false {
		t.Errorf("counts: %v", d)
	}
	conflicts := d["conflicts"].([]any)
	if len(conflicts) != 2 {
		t.Fatalf("conflicts: %v", conflicts)
	}
	if c := conflicts[0].(string); !strings.Contains(c, "LICENSE says MIT") || !strings.Contains(c, "tools/pyproject.toml says GPL-3.0") {
		t.Errorf("manifest conflict: %q", c)
	}
	if c := conflicts[1].(string); !strings.Contains(c, "LICENSE says MIT") || !strings.Contains(c, "SPDX Apache-2.0") || !strings.Contains(c, "src/server.go") {
		t.Errorf("header conflict: %q", c)
	}
	for _, c := range conflicts {
		if s := c.(string); strings.Contains(s, "vendor") || strings.Contains(s, "CC0") || strings.Contains(s, "package.json") {
			t.Errorf("false conflict: %s", s)
		}
	}
	txt, _ := d["text"].(string)
	if !strings.HasPrefix(txt, "project: MIT (LICENSE); 2 files with SPDX, 1 without") || !strings.Contains(txt, "2 conflicts") {
		t.Errorf("text: %q", txt)
	}

	// a second run is byte-identical (deterministic)
	v2, _ := scCall(t, tb, "license_scan", nil)
	if fmt.Sprint(scDataOf(v2)) != fmt.Sprint(d) {
		t.Errorf("not deterministic:\n%v\n%v", scDataOf(v2), d)
	}

	// path narrows to a subtree with no license file; limit truncates the header scan
	v, _ = scCall(t, tb, "license_scan", map[string]any{"path": "src", "limit": 2})
	d = scDataOf(v)
	if !scOK(v) || d["files_scanned"] != 2.0 || d["truncated"] != true || len(d["project_licenses"].([]any)) != 0 || len(d["conflicts"].([]any)) != 0 {
		t.Errorf("subtree: %v", d)
	}
	if txt, _ := d["text"].(string); !strings.HasPrefix(txt, "project: no license file") {
		t.Errorf("subtree text: %q", txt)
	}

	// a single license file is identified on its own
	v, _ = scCall(t, tb, "license_scan", map[string]any{"path": "LICENSE"})
	d = scDataOf(v)
	if pl := d["project_licenses"].([]any); !scOK(v) || len(pl) != 1 || pl[0].(map[string]any)["id"] != "MIT" {
		t.Errorf("single file: %v", d)
	}

	// an empty tree is not an error, just low confidence
	scWrite(t, root, "empty/.keep", "")
	v, _ = scCall(t, tb, "license_scan", map[string]any{"path": "empty"})
	if d = scDataOf(v); !scOK(v) || scConf(v) > 0.5 || d["files_scanned"] != 0.0 {
		t.Errorf("empty: %v %v", v, d)
	}

	// refusals: a missing path fails inside the script (JSON, ok=false); a limit below the
	// schema minimum is refused by the harness before the script runs
	if v, _ = scCall(t, tb, "license_scan", map[string]any{"path": "nope"}); scOK(v) {
		t.Errorf("missing path must fail: %v", v)
	}
	b, _ := json.Marshal(map[string]any{"name": "license_scan", "args": map[string]any{"limit": 0}})
	if o := tb.Exec(context.Background(), "script", string(b)); !o.IsErr || !strings.Contains(o.Text, "Validation failed") {
		t.Errorf("limit=0 must fail validation: %v", o.Text)
	}
}
