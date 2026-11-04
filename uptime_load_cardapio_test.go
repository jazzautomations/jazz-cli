//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// TestUptimeLoadCardapio runs the uptime_load offline script through the same
// path jazz uses and checks the /proc-derived numbers are present, sane and
// internally consistent (cpu percentages add up, sample_ms is clamped).
func TestUptimeLoadCardapio(t *testing.T) {
	if _, err := os.Stat("/proc/uptime"); err != nil {
		t.Skip("no /proc on this system")
	}
	tb, _ := scTestBox(t) // skips if python3 is missing

	v, _ := scCall(t, tb, "uptime_load", map[string]any{"sample_ms": 100})
	if !scOK(v) {
		t.Fatalf("uptime_load not ok: %v", v)
	}
	d := scDataOf(v)

	if up, _ := d["uptime_s"].(float64); up <= 0 {
		t.Errorf("uptime_s = %v, want > 0", d["uptime_s"])
	}
	if ut, _ := d["uptime_text"].(string); ut == "" {
		t.Errorf("uptime_text empty")
	}

	load, _ := d["load"].(map[string]any)
	if load == nil {
		t.Fatalf("load missing/null: %v", d)
	}
	for _, k := range []string{"1", "5", "15"} {
		if f, ok := load[k].(float64); !ok || f < 0 {
			t.Errorf("load[%s] = %v, want number >= 0", k, load[k])
		}
	}

	if cpus, _ := d["cpus"].(float64); cpus < 1 {
		t.Errorf("cpus = %v, want >= 1", d["cpus"])
	}

	cp, _ := d["cpu_pct"].(map[string]any)
	if cp == nil {
		t.Fatalf("cpu_pct missing: %v", d)
	}
	// user+system+idle+iowait may be null only if /proc/stat failed; when
	// they are numbers they must add up to ~100 (steal/guest are folded in).
	if _, isNum := cp["busy"].(float64); isNum {
		sum := 0.0
		for _, k := range []string{"user", "system", "idle", "iowait"} {
			f, ok := cp[k].(float64)
			if !ok || f < 0 || f > 100 {
				t.Errorf("cpu_pct[%s] = %v, want 0..100", k, cp[k])
			}
			sum += f
		}
		if sum < 98.5 || sum > 101.5 {
			t.Errorf("cpu_pct does not add up to 100: %v (sum %.1f)", cp, sum)
		}
		busy, _ := cp["busy"].(float64)
		idle, _ := cp["idle"].(float64)
		iowait, _ := cp["iowait"].(float64)
		if diff := busy + idle + iowait - 100; diff > 1.5 || diff < -1.5 {
			t.Errorf("busy+idle+iowait = %.1f, want ~100", busy+idle+iowait)
		}
	} else if cp["busy"] != nil {
		t.Errorf("cpu_pct.busy must be a number or null: %v", cp["busy"])
	}

	procs, _ := d["procs"].(map[string]any)
	if total, _ := procs["total"].(float64); total < 1 {
		t.Errorf("procs.total = %v, want >= 1", procs["total"])
	}
	if sm, _ := d["sample_ms"].(float64); int(sm) != 100 {
		t.Errorf("sample_ms = %v, want 100", d["sample_ms"])
	}
	text, _ := d["text"].(string)
	if !strings.HasPrefix(text, "up ") || !strings.Contains(text, "load ") {
		t.Errorf("text = %q, want 'up <time>, load a/b/c on N cpus (X% busy)'", text)
	}

	// sample_ms above the ceiling is clamped to 1000, not rejected.
	v2, _ := scCall(t, tb, "uptime_load", map[string]any{"sample_ms": 5000})
	if !scOK(v2) {
		t.Fatalf("uptime_load (clamp) not ok: %v", v2)
	}
	if sm, _ := scDataOf(v2)["sample_ms"].(float64); int(sm) != 1000 {
		t.Errorf("sample_ms 5000 should clamp to 1000, got %v", scDataOf(v2)["sample_ms"])
	}
}
