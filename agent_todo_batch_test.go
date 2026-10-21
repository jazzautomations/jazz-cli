package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The prompt asks the model to update the todo in the same response as the next
// action (each todo-only step cost a ~14 s round-trip in eval v2), so a todo
// call batched with a write must run both, in order, within one step.
func TestAgentTodoBatchedWithAction(t *testing.T) {
	if sys, _ := buildSystemPrompt(t.TempDir(), nil); !strings.Contains(sys, "Update the todo in the same response as your next tool call") {
		t.Fatal("system prompt lost the todo batching rule")
	}
	todo := jsonArgs(map[string]any{"todos": []map[string]any{
		{"content": "criar a.txt", "status": "completed"},
		{"content": "conferir", "status": "completed"},
	}})
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{
			{ID: "t1", Name: "todo", Args: todo},
			{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "a.txt", "content": "oi\n"})},
		}},
		fakeResp{Text: "pronto"},
	), nil)
	if err := h.run(t, "crie a.txt"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests (todo+write in one step), got %d: %v", len(reqs), reqs[len(reqs)-1].last())
	}
	res := reqs[1].toolResults()
	if !strings.Contains(res["t1"], "conferir") || !strings.Contains(res["w1"], "a.txt") {
		t.Fatalf("results %v", res)
	}
	if b, err := os.ReadFile(filepath.Join(h.root, "a.txt")); err != nil || string(b) != "oi\n" {
		t.Fatalf("write did not run: %q %v", b, err)
	}
}
