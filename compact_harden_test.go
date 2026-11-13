package main

import (
	"context"
	"strings"
	"testing"
)

// The model summary works: the user's own words and the touched files are
// appended verbatim, and the summarizer saw old tool output pruned.
func TestCompactAnchorsAndPrune(t *testing.T) {
	big := strings.Repeat("linha de log\n", 2000)
	var sawBig bool
	h := newHarness(t, func(n int, r recReq) fakeResp {
		for _, m := range r.Messages {
			if c, _ := m["content"].(string); strings.Count(c, "linha de log") > 500 {
				sawBig = true
			}
		}
		return fakeResp{Text: "## Goal\nresumo do modelo"}
	}, nil)
	h.ag.EnsureSystem()
	h.ag.Msgs = []Message{
		{Role: "user", Content: "Adicione prioridade:\n- alta\n- baixa"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "edit", Args: `{"path":"notas/cli.py","old_string":"a","new_string":"b"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "feito"},
		{Role: "user", Content: "agora o list"},
		{Role: "assistant", Content: "ok"},
	}
	if err := h.ag.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if sawBig {
		t.Error("the summarizer must get old tool output pruned")
	}
	c := h.ag.Msgs[0].Content
	for _, want := range []string{"resumo do modelo", "## User messages (verbatim, oldest first)", "> Adicione prioridade:\n> - alta\n> - baixa", "notas/cli.py", "tools remain fully available"} {
		if !strings.Contains(c, want) {
			t.Errorf("summary misses %q:\n%s", want, c)
		}
	}
}

func TestChUserVerbatimKeepsNewest(t *testing.T) {
	var msgs []Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("x", 500) + string(rune('a'+i))})
	}
	us := chUserVerbatim(msgs)
	if len(us) == 0 || !strings.HasSuffix(us[len(us)-1], "t") || len(strings.Join(us, "")) > chUserTotal {
		t.Errorf("want the newest messages within budget, got %d items", len(us))
	}
}
