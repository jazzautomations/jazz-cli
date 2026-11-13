//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// "oi" goes the conversation lane: one request, nothing injected after the
// text, a short output cap, no checkpoint scan, and the tools still offered.
func TestChatLaneOi(t *testing.T) {
	log := cpCountingGit(t)
	h := newHarness(t, seq(fakeResp{Text: "oi! no que posso ajudar?"}), func(c *Config) { c.MaxTokens = 16000 })
	h.ag.tools.Checkpoints = true
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	last := reqs[0].Messages[len(reqs[0].Messages)-1]
	if c, _ := last["content"].(string); strings.TrimSpace(c) != "oi" || strings.Contains(c, "da última vez") {
		t.Errorf("chat message carries injections: %q", c)
	}
	mt, _ := reqs[0].Raw["max_tokens"].(float64)
	if mt2, ok := reqs[0].Raw["max_completion_tokens"].(float64); ok {
		mt = mt2
	}
	if mt == 0 || mt > acChatOut {
		t.Errorf("chat output cap: max tokens %v, want <= %d", mt, acChatOut)
	}
	if !reqs[0].HasTools {
		t.Error("tools stay available in the conversation lane")
	}
	if n := cpCalls(log); n != 0 {
		b, _ := os.ReadFile(log)
		t.Errorf("chat turn: no checkpoint git call, got %d:\n%s", n, b)
	}
}

// Go-aheads and tasks are never the conversation lane.
func TestChatLaneNotForTasks(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	for _, s := range []string{"bora", "marcha", "continua", "arruma meu wifi", "oi, corrige o teste do parser", "pode seguir"} {
		if h.ag.acChatLane(s) {
			t.Errorf("%q must not be the conversation lane", s)
		}
	}
	for _, s := range []string{"oi", "valeu", "tudo bem?", "salve jazz", "bom dia"} {
		if !h.ag.acChatLane(s) {
			t.Errorf("%q is small talk", s)
		}
	}
	// the model just asked something: "perfeito"/"show" answers it
	h.ag.Msgs = append(h.ag.Msgs, Message{Role: "user", Content: "ajusta o nginx"}, Message{Role: "assistant", Content: "achei o erro na linha 12. posso aplicar a correção?"})
	for _, s := range []string{"perfeito", "show", "valeu"} {
		if h.ag.acChatLane(s) {
			t.Errorf("%q after a question from the model is an answer, not chat", s)
		}
	}
	h.ag.Msgs = append(h.ag.Msgs, Message{Role: "user", Content: "sim"}, Message{Role: "assistant", Content: "aplicado e testado."})
	if !h.ag.acChatLane("valeu") {
		t.Error(`"valeu" after a finished task is small talk`)
	}
	h.ag.Msgs = nil
	// an open todo list is a pending task: "valeu" does not drop it
	h.ag.tools.Todos = []Todo{{Content: "rodar testes", Status: "in_progress"}}
	if h.ag.acChatLane("valeu") {
		t.Error("open todos: not the conversation lane")
	}
}
