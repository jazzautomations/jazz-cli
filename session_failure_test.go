package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentStopsWhenSessionCannotPersist(t *testing.T) {
	h := newHarness(t, func(_ int, _ recReq) fakeResp { return fakeResp{Text: "done"} }, nil)
	h.ag.EnsureSystem()
	h.ag.sess.f.Close()
	stop, err := h.ag.Run(context.Background(), "hello")
	if stop != StopError || err == nil {
		t.Fatalf("unsaved session continued: stop=%s err=%v", stop, err)
	}
	if len(h.fs.requests()) != 0 {
		t.Fatal("provider was called after persistence failed")
	}
}

func TestFailedWriteAheadNeverRunsTool(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.ag.EnsureSystem()
	h.ag.sess.f.Close()
	tc := ToolCall{ID: "write-failure", Name: "bash", Args: `{"command":"echo changed > must-not-exist.txt"}`}
	out, ran, _ := h.ag.obExecTool(context.Background(), 1, tc)
	if ran || !out.IsErr {
		t.Fatalf("unlogged tool ran: ran=%v output=%+v", ran, out)
	}
	if _, err := os.Stat(filepath.Join(h.root, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("tool wrote a file: %v", err)
	}
}
