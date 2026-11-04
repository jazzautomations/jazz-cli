//go:build !windows

package main

import (
	"os/exec"
	"regexp"
	"testing"
)

// TestCardapioPkgInstalled drives pkg_installed through tb.Exec("script", ...) on the
// jazz-scripts/testdata/pkg_installed tree (synthetic node_modules + go.mod). The
// project sources (node_modules read directly, go.mod require block) are asserted
// exactly; the system managers (dpkg/rpm/pip/npm -g) depend on the machine, so only
// their shape and their `available` flag (= binary in PATH) are checked.
func TestCardapioPkgInstalled(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is absent
	scFixtureTree(t, root, "pkg_installed")

	// go: the require block of go.mod (one-line and block form), sorted, versions kept
	v, out := scCall(t, tb, "pkg_installed", map[string]any{"manager": "go"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.8 {
		t.Fatalf("go: %v\n%s", v, out.Text)
	}
	mgrs, _ := d["managers"].([]any)
	if len(mgrs) != 1 {
		t.Fatalf("manager=go must report only the go manager: %v", d["managers"])
	}
	m0 := mgrs[0].(map[string]any)
	if m0["name"] != "go" || m0["available"] != true || m0["count"] != 3.0 || m0["skipped"] != false || m0["source"] != "go.mod" {
		t.Errorf("go manager = %v", m0)
	}
	if _, err := exec.LookPath("go"); err == nil {
		if ver, _ := m0["version"].(string); !regexp.MustCompile(`^go\d+(\.\d+)*`).MatchString(ver) {
			t.Errorf("go toolchain version = %v, want go1.x", m0["version"])
		}
	} else if m0["version"] != nil {
		t.Errorf("no go binary: version must be null, got %v", m0["version"])
	}
	pk, _ := d["packages"].([]any)
	wantGo := [][2]string{{"github.com/ana/httpkit", "v1.4.0"}, {"github.com/bruno/cli", "v0.2.0"}, {"gopkg.in/yaml.v3", "v3.0.1"}}
	if len(pk) != len(wantGo) || d["total"] != 3.0 || d["truncated"] != false {
		t.Fatalf("go packages = %v (total %v truncated %v)", pk, d["total"], d["truncated"])
	}
	for i, w := range wantGo {
		p := pk[i].(map[string]any)
		if p["manager"] != "go" || p["name"] != w[0] || p["version"] != w[1] {
			t.Errorf("go packages[%d] = %v, want %v", i, p, w)
		}
	}
	if d["text"] != "go 3 packages" {
		t.Errorf("go text = %q", d["text"])
	}

	// name filter: counts stay per manager, total = matches
	v, _ = scCall(t, tb, "pkg_installed", map[string]any{"manager": "go", "name": "YAML"})
	d = scDataOf(v)
	if pk, _ = d["packages"].([]any); len(pk) != 1 || d["total"] != 1.0 || d["text"] != "go 3 packages; 1 match 'yaml'" {
		t.Errorf("name filter: %v", d)
	}

	// limit: truncated:true, total is the real count, the first by name is kept
	v, _ = scCall(t, tb, "pkg_installed", map[string]any{"manager": "go", "limit": 1})
	d = scDataOf(v)
	pk, _ = d["packages"].([]any)
	if len(pk) != 1 || d["total"] != 3.0 || d["truncated"] != true || pk[0].(map[string]any)["name"] != "github.com/ana/httpkit" {
		t.Errorf("limit=1: %v", d)
	}
	if scConf(v) < 0.8 {
		t.Errorf("a list cut by limit is still complete: confidence %v", scConf(v))
	}

	// npm: project packages read from node_modules (scoped included); global list only if npm exists
	v, out = scCall(t, tb, "pkg_installed", map[string]any{"manager": "npm"})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("npm: %v\n%s", v, out.Text)
	}
	mgrs, _ = d["managers"].([]any)
	if len(mgrs) != 2 || mgrs[0].(map[string]any)["name"] != "npm" || mgrs[1].(map[string]any)["name"] != "npm(project)" {
		t.Fatalf("manager=npm must report npm and npm(project): %v", d["managers"])
	}
	_, npmErr := exec.LookPath("npm")
	if mgrs[0].(map[string]any)["available"] != (npmErr == nil) {
		t.Errorf("npm available = %v, want %v", mgrs[0].(map[string]any)["available"], npmErr == nil)
	}
	proj := mgrs[1].(map[string]any)
	if proj["available"] != true || proj["count"] != 2.0 || proj["source"] != "node_modules/*/package.json" {
		t.Errorf("npm(project) manager = %v", proj)
	}
	got := map[string]string{}
	for _, x := range d["packages"].([]any) {
		p := x.(map[string]any)
		if p["manager"] == "npm(project)" {
			got[p["name"].(string)], _ = p["version"].(string)
		}
	}
	if len(got) != 2 || got["pkgx"] != "1.2.3" || got["@scope/pkgy"] != "0.4.0" {
		t.Errorf("npm(project) packages = %v", got)
	}

	// go.sum fallback when there is no go.mod; path= points at a sub-project
	scWrite(t, root, "sumonly/go.sum", "github.com/ana/httpkit v1.4.0 h1:abc=\ngithub.com/ana/httpkit v1.4.0/go.mod h1:def=\n")
	v, _ = scCall(t, tb, "pkg_installed", map[string]any{"manager": "go", "path": "sumonly"})
	d = scDataOf(v)
	pk, _ = d["packages"].([]any)
	if !scOK(v) || len(pk) != 1 || pk[0].(map[string]any)["name"] != "github.com/ana/httpkit" || pk[0].(map[string]any)["version"] != "v1.4.0" {
		t.Errorf("go.sum fallback: %v", d)
	}
	if mgrs, _ = d["managers"].([]any); len(mgrs) != 1 || mgrs[0].(map[string]any)["source"] != "go.sum" {
		t.Errorf("go.sum fallback source: %v", d["managers"])
	}

	// auto on an empty dir: every manager listed with its availability; project sources absent
	scWrite(t, root, "empty/.keep", "")
	v, out = scCall(t, tb, "pkg_installed", map[string]any{"path": "empty"})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("auto: %v\n%s", v, out.Text)
	}
	mgrs, _ = d["managers"].([]any)
	if len(mgrs) != 6 {
		t.Fatalf("auto must list 6 managers: %v", d["managers"])
	}
	avail := map[string]bool{}
	for _, x := range mgrs {
		m := x.(map[string]any)
		if _, ok := m["available"].(bool); !ok {
			t.Errorf("manager %v: available must be a bool", m)
		}
		if _, ok := m["skipped"].(bool); !ok {
			t.Errorf("manager %v: skipped must be a bool", m)
		}
		avail[m["name"].(string)] = m["available"].(bool)
	}
	if avail["npm(project)"] || avail["go"] {
		t.Errorf("empty dir: npm(project)/go must be unavailable: %v", avail)
	}
	for _, bin := range [][2]string{{"dpkg", "dpkg-query"}, {"rpm", "rpm"}, {"npm", "npm"}} {
		_, err := exec.LookPath(bin[1])
		if avail[bin[0]] != (err == nil) {
			t.Errorf("%s available = %v, want %v (%s in PATH)", bin[0], avail[bin[0]], err == nil, bin[1])
		}
	}
	if _, ok := d["total"].(float64); !ok {
		t.Errorf("total must be a number: %v", d["total"])
	}

	// an explicitly requested manager that is not installed is ok:false-free: a warning, not a failure
	if _, err := exec.LookPath("rpm"); err != nil {
		v, out = scCall(t, tb, "pkg_installed", map[string]any{"manager": "rpm"})
		d = scDataOf(v)
		if !scOK(v) || out.IsErr || d["text"] != "rpm not available" || d["total"] != 0.0 {
			t.Errorf("rpm missing: %v", v)
		}
	}

	// refusals: unknown manager (schema enum), path that is not a directory
	_, out = scCall(t, tb, "pkg_installed", map[string]any{"manager": "brew"})
	if !out.IsErr {
		t.Errorf("manager=brew must fail schema validation: %s", out.Text)
	}
	v, out = scCall(t, tb, "pkg_installed", map[string]any{"manager": "go", "path": "go.mod"})
	if scOK(v) || !out.IsErr {
		t.Errorf("path to a file must be refused: %v", v)
	}
}
