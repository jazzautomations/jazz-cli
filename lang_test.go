package main

import (
	"strings"
	"testing"
)

func TestLangIsPT(t *testing.T) {
	pt := []string{"arruma meu wifi", "o wifi ta conectado aqui?", "corrige o parser do csv", "oi", "que dispositivos estao online na tailscale?", "porra arruma ae essa merda", "não altere os testes"}
	en := []string{"fix the bug in f0.go", "how does the router pick a model?", "add a test for the parser", "rename Foo to Bar"}
	for _, s := range pt {
		if !langIsPT(s) {
			t.Errorf("%q should read as Portuguese", s)
		}
	}
	for _, s := range en {
		if langIsPT(s) {
			t.Errorf("%q should not read as Portuguese", s)
		}
	}
}

// A Portuguese request gets "Responda em português do Brasil" at the end of
// the system prompt of the first request; an English one does not.
func TestSystemPromptPTNote(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "pronto"}, fakeResp{Text: "pronto"}), nil)
	if err := h.run(t, "arruma o teste do parser"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "e agora roda de novo"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !strings.HasSuffix(reqs[0].system(), "Responda em português do Brasil, como um colega: direto, sem relatório.") {
		t.Errorf("PT note missing at the end of the system prompt: %q", tail(reqs[0].system(), 120))
	}
	if reqs[0].system() != reqs[len(reqs)-1].system() {
		t.Error("system prompt must stay byte-identical across turns (prompt cache)")
	}
	h2 := newHarness(t, seq(fakeResp{Text: "done"}), nil)
	if err := h2.run(t, "fix the bug in the parser"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h2.fs.requests()[0].system(), "Responda em português") {
		t.Error("an English request gets no PT note")
	}
}

// The final-message rule is a colleague's answer, not a report template.
func TestPromptNoReportTemplate(t *testing.T) {
	for _, bad := range []string{"anything left undone", "Final reply", "at most 8 lines"} {
		if strings.Contains(basePrompt, bad) {
			t.Errorf("basePrompt still has %q", bad)
		}
	}
	if !strings.Contains(basePrompt, "answer like a colleague, in the user's language") {
		t.Error("final-message rule missing")
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
