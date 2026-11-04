//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCardapioServiceStatus drives service_status through tb.Exec("script", ...):
// a running process is observed as active, and an absent name resolves to a valid
// "not found" oracle answer (ok:true), never an error.
func TestCardapioServiceStatus(t *testing.T) {
	tb, _ := scTestBox(t) // skips if python3 is absent

	// absent service: auto walks the backends and returns a clean "not found"
	absent := fmt.Sprintf("jazz_absent_%d_%d", os.Getpid(), time.Now().UnixNano())
	v, out := scCall(t, tb, "service_status", map[string]any{"name": absent})
	d := scDataOf(v)
	if !scOK(v) || out.IsErr {
		t.Fatalf("absent service must still be ok (valid oracle answer): %v", v)
	}
	if d["active"] != false || d["state"] != "not found" {
		t.Errorf("absent: active=%v state=%v", d["active"], d["state"])
	}
	if d["pid"] != nil {
		t.Errorf("absent pid = %v, want null", d["pid"])
	}
	if _, ok := d["checked"].([]any); !ok {
		t.Errorf("checked must be a list: %v", d["checked"])
	}

	// name is required: the harness refuses it at schema validation, before the
	// script runs (so the reply is a plain error, not the script's JSON)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out := tb.Exec(ctx, "script", `{"name":"service_status","args":{}}`); !out.IsErr || !strings.Contains(out.Text, "name") {
		t.Errorf("missing name must be refused: %+v", out)
	}

	// a running process is observed as active via kind=process
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available for the live-process check")
	}
	cmd := exec.Command("sleep", "45")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start sleep:", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	v, _ = scCall(t, tb, "service_status", map[string]any{"name": "sleep", "kind": "process"})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("process check not ok: %v", v)
	}
	if d["active"] != true || d["state"] != "running" {
		t.Errorf("running process: active=%v state=%v", d["active"], d["state"])
	}
	if pid, ok := d["pid"].(float64); !ok || pid <= 0 {
		t.Errorf("running process pid = %v, want a positive int", d["pid"])
	}
	if d["kind_used"] != "process" {
		t.Errorf("kind_used = %v, want process", d["kind_used"])
	}
	if d["text"] != "sleep: running (process)" {
		t.Errorf("text = %q", d["text"])
	}
}
