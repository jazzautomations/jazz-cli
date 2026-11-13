package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func usFakeSandbox(t *testing.T) {
	prev := sbActive.Load()
	t.Cleanup(func() { sbActive.Store(prev) })
	sbActive.Store(&Sandbox{Backend: sbBwrap, Bin: "/nonexistent/bwrap"})
}

// sandbox:false is a question every time, also in yolo; denied, nothing runs.
func TestUnsandboxedAsksEvenInYolo(t *testing.T) {
	usFakeSandbox(t)
	defer func(v bool) { usAllow = v }(usAllow)
	usAllow = false
	call := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"touch escaped.txt","sandbox":false}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{call}}, fakeResp{Text: "negado; não rodei"}), func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "renderiza"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 1 || !strings.Contains(h.ui.approvals[0].Reason, "FORA do sandbox") {
		t.Fatalf("want one question about leaving the sandbox, got %+v", h.ui.approvals)
	}
	if _, err := os.Stat(filepath.Join(h.root, "escaped.txt")); err == nil {
		t.Error("a denied escape ran")
	}
}

// runHost runs in the shell's cwd, as the user, with the timeout applied.
func TestRunHost(t *testing.T) {
	h := newHarness(t, nil, nil)
	r := h.ag.tools.runHost(context.Background(), h.ag.tools.Shell, "pwd; echo $JAZZ_SANDBOX_ACTIVE; exit 3", 10*time.Second)
	if r.ExitCode != 3 || !strings.Contains(r.Output, filepath.Base(h.root)) || !strings.Contains(r.Note, "outside the OS sandbox") {
		t.Errorf("runHost = %+v", r)
	}
	r = h.ag.tools.runHost(context.Background(), h.ag.tools.Shell, "sleep 5", 300*time.Millisecond)
	if !strings.Contains(r.Note, "timed out") {
		t.Errorf("timeout note missing: %+v", r)
	}
}
