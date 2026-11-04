//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCardapioLogErrorsRecent drives log_errors_recent through tb.Exec("script", ...)
// on a synthetic syslog-shaped file (host "node1", 30 lines, 4 repeated messages):
// grouping by normalized message, unit, first/last, level filter, limit/truncated,
// the time window, and the journal/auto path (shape only: it is never an error).
func TestCardapioLogErrorsRecent(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is absent
	scWrite(t, root, "errors_recent.log", scFixture(t, "errors_recent.log"))

	// errors in the whole file (minutes 0 = no window for a file)
	v, _ := scCall(t, tb, "log_errors_recent", map[string]any{"path": "errors_recent.log", "minutes": 0})
	d := scDataOf(v)
	if !scOK(v) {
		t.Fatalf("file source not ok: %v", v)
	}
	if d["source_used"] != "file:errors_recent.log" || d["window_min"] != 0.0 || d["min_level"] != "err" {
		t.Errorf("source/window/min_level: %v %v %v", d["source_used"], d["window_min"], d["min_level"])
	}
	if d["matched"] != 19.0 || d["distinct"] != 3.0 || d["total_lines"] != 30.0 {
		t.Errorf("matched=%v distinct=%v total_lines=%v, want 19/3/30", d["matched"], d["distinct"], d["total_lines"])
	}
	if d["truncated"] != false || d["permission_denied"] != false {
		t.Errorf("truncated=%v permission_denied=%v", d["truncated"], d["permission_denied"])
	}
	groups, _ := d["groups"].([]any)
	if len(groups) != 3 {
		t.Fatalf("groups = %v", d["groups"])
	}
	g0, _ := groups[0].(map[string]any)
	// sorted by count desc: sshd ×10 first, numbers/ips normalized, host never in the sample
	if g0["count"] != 10.0 || g0["unit"] != "sshd" || g0["level"] != "err" {
		t.Errorf("top group: %v", g0)
	}
	if g0["sample"] != "Connection closed by <ip> port <n> [preauth]" {
		t.Errorf("sample = %q", g0["sample"])
	}
	if g0["first"] != "2026-10-19T10:00:01+00:00" || g0["last"] != "2026-10-19T10:00:30+00:00" {
		t.Errorf("first/last = %v / %v", g0["first"], g0["last"])
	}
	if ex, _ := g0["example"].(string); !strings.Contains(ex, "10.0.0.5") {
		t.Errorf("example keeps the raw message: %q", ex)
	}
	g1, _ := groups[1].(map[string]any)
	g2, _ := groups[2].(map[string]any)
	if g1["unit"] != "app" || g1["count"] != 6.0 || g1["sample"] != "db timeout after <n>ms (attempt <n>)" {
		t.Errorf("second group: %v", g1)
	}
	if g2["level"] != "crit" || g2["count"] != 3.0 || g2["sample"] != "cannot open <path>: permission denied" {
		t.Errorf("third group (CRIT, path normalized): %v", g2)
	}
	for _, g := range groups {
		if s := fmt.Sprint(g.(map[string]any)["sample"]); strings.Contains(s, "node1") {
			t.Errorf("hostname leaked into a sample: %q", s)
		}
	}
	text, _ := d["text"].(string)
	if !strings.HasPrefix(text, "19 error lines in the whole file, 3 distinct: sshd ×10 ") {
		t.Errorf("text = %q", text)
	}

	// min_level warning adds the kernel WARN group; INFO/DEBUG lines never count
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "errors_recent.log", "minutes": 0, "min_level": "warning"})
	d = scDataOf(v)
	if !scOK(v) || d["matched"] != 24.0 || d["distinct"] != 4.0 {
		t.Fatalf("warning level: matched=%v distinct=%v", d["matched"], d["distinct"])
	}
	groups, _ = d["groups"].([]any)
	seenWarn := false
	for _, g := range groups {
		m := g.(map[string]any)
		if m["unit"] == "kernel" && m["level"] == "warning" && m["count"] == 5.0 {
			seenWarn = true
		}
	}
	if !seenWarn {
		t.Errorf("kernel warning group missing: %v", groups)
	}

	// limit caps the groups and says so
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "errors_recent.log", "minutes": 0, "limit": 2})
	d = scDataOf(v)
	if groups, _ = d["groups"].([]any); len(groups) != 2 || d["truncated"] != true || d["distinct"] != 3.0 {
		t.Errorf("limit 2: groups=%d truncated=%v distinct=%v", len(groups), d["truncated"], d["distinct"])
	}

	// the window: fresh stamps are kept, 5-hour-old ones dropped
	now := time.Now()
	var sb strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&sb, "%s node1 app[1]: ERROR db timeout after 5000ms\n", now.Add(-time.Duration(i)*time.Minute).Format("2006-01-02T15:04:05-0700"))
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&sb, "%s node1 app[1]: ERROR db timeout after 5000ms\n", now.Add(-5*time.Hour-time.Duration(i)*time.Minute).Format("2006-01-02T15:04:05-0700"))
	}
	scWrite(t, root, "fresh.log", sb.String())
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "fresh.log"})
	d = scDataOf(v)
	if !scOK(v) || d["window_min"] != 60.0 || d["matched"] != 3.0 {
		t.Errorf("60 min window: window=%v matched=%v (%v)", d["window_min"], d["matched"], v)
	}
	if text, _ = d["text"].(string); !strings.HasPrefix(text, "3 error lines in the last 60 min, 1 distinct: app ×3 ") {
		t.Errorf("window text = %q", text)
	}
	// the fixture's 2026-10-19 lines are outside any window measured from now
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "errors_recent.log", "minutes": 60})
	if d = scDataOf(v); !scOK(v) || d["matched"] != 0.0 || d["text"] != "no errors in the last 60 min" {
		t.Errorf("old lines inside a 60 min window: %v", v)
	}

	// a file without levels or timestamps: keyword fallback, lower confidence, says so
	scWrite(t, root, "plain.log", "boot ok\nfailed to mount /dev/sdb1\nfailed to mount /dev/sdb2\nall good\n")
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "plain.log"})
	d = scDataOf(v)
	if !scOK(v) || d["matched"] != 2.0 || scConf(v) > 0.7 {
		t.Errorf("keyword fallback: matched=%v conf=%v", d["matched"], scConf(v))
	}
	if groups, _ = d["groups"].([]any); len(groups) != 1 || groups[0].(map[string]any)["sample"] != "failed to mount <path>" {
		t.Errorf("keyword group: %v", d["groups"])
	}
	if w := fmt.Sprint(v["warnings"]); !strings.Contains(w, "keywords") {
		t.Errorf("keyword fallback must be announced in warnings: %v", w)
	}

	// a missing file is a clean failure, with the window echoed
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"path": "nope.log"})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not found") {
		t.Errorf("missing file: %v", v)
	}

	// source=file without a path is refused by the script
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"source": "file"})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "path") {
		t.Errorf("source=file without path: %v", v)
	}

	// an unknown source is refused (by the schema enum or by the script, never ok:true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out := tb.Exec(ctx, "script", `{"name":"log_errors_recent","args":{"source":"nope"}}`); !out.IsErr {
		var bad map[string]any
		if err := json.Unmarshal([]byte(out.Text), &bad); err != nil || scOK(bad) {
			t.Errorf("bad source must not be ok: %+v", out)
		}
	}

	// auto on this box: whatever the journal/syslog permissions are, the answer is
	// ok:true with the contract's keys (permission problems are data, not failures)
	v, _ = scCall(t, tb, "log_errors_recent", map[string]any{"minutes": 5})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("auto source must be ok: %v", v)
	}
	for _, k := range []string{"source_used", "window_min", "groups", "matched", "distinct", "total_lines", "truncated", "permission_denied", "text"} {
		if _, ok := d[k]; !ok {
			t.Errorf("auto: data misses %q: %v", k, d)
		}
	}
	if _, ok := d["permission_denied"].(bool); !ok {
		t.Errorf("permission_denied must be a bool: %v", d["permission_denied"])
	}
	if _, ok := d["groups"].([]any); !ok {
		t.Errorf("groups must be a list: %v", d["groups"])
	}
	if su, _ := d["source_used"].(string); d["source_used"] != nil && su != "journal" && !strings.HasPrefix(su, "file:/var/log/") {
		t.Errorf("auto source_used = %v", d["source_used"])
	}
}
