package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// evUI is recUI that can also ask ask_user questions and merged
// confirmations (as the terminal does), counting each one.
type evUI struct {
	*recUI
	asks, batches int
}

func (u *evUI) AskUser(ctx context.Context, q askArgs) (string, bool) {
	u.asks++
	return "go test ./...", true
}

func (u *evUI) ApproveBatch(ctx context.Context, items []apBatchItem) ([]Approval, bool) {
	u.batches++
	out := make([]Approval, len(items))
	for i := range out {
		out[i] = Allow
	}
	return out, false
}

// TestEvalPromptsPerTask is a scripted task in ask mode: the model asks for
// the test command, then writes two files and runs a command in one
// response, then ends with an offer. prompts (obs.go) counts every time the
// user had to answer. Before this change (JAZZ_ASK_FACTS=off,
// JAZZ_APPROVE_BATCH=off) that is 5; now the question is answered from
// go.mod and the three confirmations are one: 2.
func TestEvalPromptsPerTask(t *testing.T) {
	run := func(t *testing.T) (obSummary, *evUI) {
		h := newHarness(t, seq(
			fakeResp{Calls: []fakeCall{{ID: "q1", Name: "ask_user", Args: `{"question":"Qual o comando de teste do projeto?","options":[{"label":"go test ./..."},{"label":"make test"}]}`}}},
			fakeResp{Calls: []fakeCall{
				{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "a.txt", "content": "a\n"})},
				{ID: "w2", Name: "write", Args: jsonArgs(map[string]any{"path": "b.txt", "content": "b\n"})},
				{ID: "b1", Name: "bash", Args: `{"command":"echo feito > c.txt"}`},
			}},
			fakeResp{Text: "Pronto: a.txt, b.txt e c.txt. Quer que eu rode os testes também?"},
		), func(c *Config) { c.Mode = "ask" })
		if err := os.WriteFile(filepath.Join(h.root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ui := &evUI{recUI: h.ui}
		h.ag.ui = ui
		if err := h.run(t, "crie os três arquivos e teste"); err != nil {
			t.Fatal(err)
		}
		evs, err := obReadEvents(obEventsPath(h.ag.sess.Path))
		if err != nil {
			t.Fatal(err)
		}
		if t.Name() == "TestEvalPromptsPerTask/depois" {
			for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
				if _, err := os.Stat(filepath.Join(h.root, f)); err != nil {
					t.Errorf("%s not written after the merged yes: %v", f, err)
				}
			}
		}
		return obSummarize(evs), ui
	}
	t.Run("antes", func(t *testing.T) {
		t.Setenv("JAZZ_ASK_FACTS", "off")
		t.Setenv("JAZZ_APPROVE_BATCH", "off")
		s, ui := run(t)
		t.Logf("antes: prompts=%d (ask_user %d, confirmações %d, pergunta no fim 1)", s.Prompts, ui.asks, len(ui.approvals))
		if s.Prompts != 5 || ui.asks != 1 || len(ui.approvals) != 3 {
			t.Errorf("before: prompts=%d asks=%d approvals=%d, want 5/1/3", s.Prompts, ui.asks, len(ui.approvals))
		}
	})
	t.Run("depois", func(t *testing.T) {
		s, ui := run(t)
		t.Logf("depois: prompts=%d (ask_user %d, respondidas pelo projeto %d, confirmação juntada %d, avulsas %d)", s.Prompts, ui.asks, s.AutoAnswered, ui.batches, len(ui.approvals))
		if s.Prompts != 2 || ui.asks != 0 || ui.batches != 1 || len(ui.approvals) != 0 || s.AutoAnswered != 1 {
			t.Errorf("after: prompts=%d asks=%d batches=%d approvals=%d auto=%d, want 2/0/1/0/1", s.Prompts, ui.asks, ui.batches, len(ui.approvals), s.AutoAnswered)
		}
	})
}
