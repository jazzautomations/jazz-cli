//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// TestMemTopCardapio runs the mem_top offline script through the same path
// jazz uses and checks /proc/meminfo totals, the RSS ranking (sorted,
// bounded by `top`) and the name filter. The script itself is a python3
// process while it scans, so filtering by "python3" must match >= 1.
func TestMemTopCardapio(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("no /proc on this system")
	}
	tb, _ := scTestBox(t) // skips if python3 is missing

	v, _ := scCall(t, tb, "mem_top", map[string]any{"top": 5})
	if !scOK(v) {
		t.Fatalf("mem_top not ok: %v", v)
	}
	d := scDataOf(v)

	mem, _ := d["mem"].(map[string]any)
	if mem == nil {
		t.Fatalf("mem block missing: %v", d)
	}
	total, _ := mem["total_mb"].(float64)
	avail, _ := mem["available_mb"].(float64)
	used, _ := mem["used_mb"].(float64)
	pct, _ := mem["used_pct"].(float64)
	if total <= 0 {
		t.Errorf("mem.total_mb = %v, want > 0", mem["total_mb"])
	}
	if avail < 0 || avail > total {
		t.Errorf("mem.available_mb = %v, want 0..total (%v)", mem["available_mb"], total)
	}
	if diff := used + avail - total; diff > 1 || diff < -1 {
		t.Errorf("used+available != total: %v + %v vs %v", used, avail, total)
	}
	if pct < 0 || pct > 100 {
		t.Errorf("mem.used_pct = %v, want 0..100", mem["used_pct"])
	}
	if st, ok := mem["swap_total_mb"].(float64); ok {
		if su, _ := mem["swap_used_mb"].(float64); su < 0 || su > st {
			t.Errorf("swap_used_mb = %v, want 0..swap_total (%v)", mem["swap_used_mb"], st)
		}
	}

	top, _ := d["top"].([]any)
	if len(top) == 0 || len(top) > 5 {
		t.Fatalf("top has %d entries, want 1..5: %v", len(top), d["top"])
	}
	prev := -1.0
	for _, e := range top {
		m, _ := e.(map[string]any)
		if pid, _ := m["pid"].(float64); pid <= 0 {
			t.Errorf("entry without pid: %v", m)
		}
		if comm, _ := m["comm"].(string); comm == "" {
			t.Errorf("entry without comm: %v", m)
		}
		rss, _ := m["rss_mb"].(float64)
		if rss < 0 {
			t.Errorf("negative rss: %v", m)
		}
		if prev >= 0 && rss > prev {
			t.Errorf("top not sorted by rss desc: %v", d["top"])
			break
		}
		prev = rss
	}
	if sc, _ := d["scanned"].(float64); sc < 1 {
		t.Errorf("scanned = %v, want >= 1", d["scanned"])
	}
	if _, isNum := d["skipped"].(float64); !isNum {
		t.Errorf("skipped must be a number: %v", d["skipped"])
	}
	text, _ := d["text"].(string)
	if !strings.Contains(text, "GB total") || !strings.Contains(text, "% used") || !strings.Contains(text, "top: ") {
		t.Errorf("text = %q", text)
	}

	// Name filter: the running script is itself a python3 process.
	v2, _ := scCall(t, tb, "mem_top", map[string]any{"name": "python3", "top": 50})
	if !scOK(v2) {
		t.Fatalf("mem_top (name filter) not ok: %v", v2)
	}
	d2 := scDataOf(v2)
	if matched, _ := d2["matched"].(float64); matched < 1 {
		t.Errorf("matched = %v, want >= 1 (the script itself runs under python3)", d2["matched"])
	}
	top2, _ := d2["top"].([]any)
	for _, e := range top2 {
		m, _ := e.(map[string]any)
		comm, _ := m["comm"].(string)
		cmd, _ := m["cmdline"].(string)
		if !strings.Contains(strings.ToLower(comm+" "+cmd), "python3") {
			t.Errorf("filter leaked a non-matching process: %v", m)
		}
	}
	if t2, _ := d2["text"].(string); !strings.Contains(t2, "matching 'python3'") {
		t.Errorf("filtered text = %q", t2)
	}

	// A filter nothing matches: ok, empty top, matched 0 (never a fake entry).
	// Args travel on stdin, so the token cannot appear in any cmdline.
	v3, _ := scCall(t, tb, "mem_top", map[string]any{"name": "zz-no-such-proc-zz"})
	if !scOK(v3) {
		t.Fatalf("mem_top (no match) not ok: %v", v3)
	}
	d3 := scDataOf(v3)
	if top3, _ := d3["top"].([]any); len(top3) != 0 {
		t.Errorf("no-match filter returned entries: %v", top3)
	}
	if matched, _ := d3["matched"].(float64); matched != 0 {
		t.Errorf("no-match matched = %v, want 0", d3["matched"])
	}
}
