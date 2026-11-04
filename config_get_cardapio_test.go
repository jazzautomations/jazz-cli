//go:build !windows

package main

import (
	"fmt"
	"strings"
	"testing"
)

// These two scripts belong to the offline "cardápio": config_get reads a key
// from any common config file without a dependency, dotenv_check audits a .env
// against its example. Both run through the same script path as json_edit and
// port_free (see scripts_test.go for scTestBox / scCall / scFixture).

func TestConfigGetCardapio(t *testing.T) {
	tb, root := scTestBox(t)

	// YAML: scalar, list index, whole document.
	scWrite(t, root, "cfg.yaml", scFixture(t, "cfg.yaml"))
	v, _ := scCall(t, tb, "config_get", map[string]any{"path": "cfg.yaml", "key": "server.port"})
	d := scDataOf(v)
	if !scOK(v) || d["format_used"] != "yaml" || d["value"] != 8080.0 || d["found"] != true {
		t.Fatalf("yaml scalar: %v", v)
	}
	if d["text"] != "server.port = 8080" {
		t.Errorf("yaml text = %v", d["text"])
	}
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.yaml", "key": "features[2]"})
	if scDataOf(v)["value"] != "menu v2" {
		t.Errorf("yaml list index: %v", v)
	}
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.yaml"})
	keys := fmt.Sprint(scDataOf(v)["keys"])
	for _, k := range []string{"name", "server", "features", "limits"} {
		if !strings.Contains(keys, k) {
			t.Errorf("whole-doc keys %v miss %q", keys, k)
		}
	}

	// TOML: nested table and array-of-tables.
	scWrite(t, root, "cfg.toml", scFixture(t, "cfg.toml"))
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.toml", "key": "server.limits.max_files"})
	if d = scDataOf(v); !scOK(v) || d["format_used"] != "toml" || d["value"] != 20000.0 {
		t.Fatalf("toml nested: %v", v)
	}
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.toml", "key": "owners[1].name"})
	if scDataOf(v)["value"] != "bruno" {
		t.Errorf("toml array-of-tables: %v", v)
	}

	// INI: section.key via configparser (values are strings).
	scWrite(t, root, "cfg.ini", scFixture(t, "cfg.ini"))
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.ini", "key": "database.pool"})
	if d = scDataOf(v); !scOK(v) || d["format_used"] != "ini" || d["value"] != "10" {
		t.Errorf("ini: %v", v)
	}

	// ENV: last value wins; comments and export handled.
	scWrite(t, root, ".env", scFixture(t, "sample.env"))
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": ".env", "key": "PORT"})
	if d = scDataOf(v); !scOK(v) || d["format_used"] != "env" || d["value"] != "9090" {
		t.Errorf("env last-wins: %v", v)
	}

	// Missing key: found=false, value nil, not an error.
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "cfg.yaml", "key": "server.nope"})
	if d = scDataOf(v); !scOK(v) || d["found"] != false || d["value"] != nil || d["text"] != "server.nope not found" {
		t.Errorf("missing key: %v", v)
	}

	// Anchors: best-effort with lowered confidence and an unparsed line.
	scWrite(t, root, "anchor.yaml", "base: &b\n  x: 1\nother:\n  <<: *b\n")
	v, _ = scCall(t, tb, "config_get", map[string]any{"path": "anchor.yaml", "key": "base.x"})
	if scConf(v) >= 0.8 || len(v["unparsed"].([]any)) == 0 {
		t.Errorf("anchors must lower confidence and land in unparsed: %v", v)
	}

	// Oversized file is refused.
	scWrite(t, root, "big.json", "{\"a\":\""+strings.Repeat("x", (2<<20)+16)+"\"}")
	v, out := scCall(t, tb, "config_get", map[string]any{"path": "big.json"})
	if scOK(v) || !out.IsErr {
		t.Errorf("file over 2 MB must fail: %v", v)
	}
}

func TestDotenvCheckCardapio(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, ".env", scFixture(t, "sample.env"))
	scWrite(t, root, ".env.example", scFixture(t, "sample.env.example"))

	v, _ := scCall(t, tb, "dotenv_check", map[string]any{"path": "."})
	d := scDataOf(v)
	if !scOK(v) || d["env_file"] != ".env" || d["example_file"] != ".env.example" {
		t.Fatalf("files not found: %v", v)
	}
	if fmt.Sprint(d["missing"]) != "[SECRET_TOKEN]" {
		t.Errorf("missing = %v", d["missing"])
	}
	if fmt.Sprint(d["empty"]) != "[DEBUG]" {
		t.Errorf("empty = %v", d["empty"])
	}
	if fmt.Sprint(d["duplicated"]) != "[PORT]" {
		t.Errorf("duplicated = %v", d["duplicated"])
	}
	if d["count"] != 5.0 || d["ok_all"] != false || d["text"] != "1 missing, 2 extra, 1 empty" {
		t.Errorf("summary: %v", d)
	}

	// Values must never leak: only names appear anywhere in the payload.
	whole := fmt.Sprint(v)
	if strings.Contains(whole, "secret-value") || strings.Contains(whole, "postgres://") {
		t.Errorf("dotenv_check leaked a variable value: %v", v)
	}

	// required adds names to the missing set.
	v, _ = scCall(t, tb, "dotenv_check", map[string]any{"path": ".env", "example": ".env.example", "required": []string{"EXTRA_ONE"}})
	if !strings.Contains(fmt.Sprint(scDataOf(v)["missing"]), "EXTRA_ONE") {
		t.Errorf("required extra: %v", scDataOf(v)["missing"])
	}

	// No .env and no example: ok with count 0 and a warning.
	tb2, _ := scTestBox(t)
	v, _ = scCall(t, tb2, "dotenv_check", map[string]any{"path": "."})
	d = scDataOf(v)
	if !scOK(v) || d["count"] != 0.0 || d["ok_all"] != true || len(v["warnings"].([]any)) == 0 {
		t.Errorf("no files: %v", v)
	}
}
