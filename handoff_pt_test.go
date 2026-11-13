package main

import (
	"fmt"
	"strings"
	"testing"
)

// Three edit steps, then the provider fails: the user gets a Portuguese
// summary naming the changed files and how to continue, and the handoff note
// for the model is built from the same facts.
func TestProviderDropShowsPTSummary(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if n < 3 {
			return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("w%d", n), Name: "write", Args: fmt.Sprintf(`{"path":"arq%d.txt","content":"x"}`, n)}}}
		}
		// 400, not 429: the summary does not depend on the error class and a
		// 429 costs ~20 s of backoff in the test
		return fakeResp{Status: 400, Body: `{"error":{"message":"upstream rejected the request"}}`}
	}, func(c *Config) { c.Mode = "yolo" })
	h.ui.answer = Allow
	err := h.run(t, "cria tres arquivos")
	if err == nil {
		t.Fatal("want the provider error")
	}
	var sum string
	for _, n := range h.ui.notices {
		if strings.Contains(n, "o provedor caiu no meio do trabalho") {
			sum = n
		}
	}
	t.Log(sum)
	if sum == "" {
		t.Fatalf("no PT summary for the user; notices=%q", h.ui.notices)
	}
	for _, want := range []string{"nada se perdeu", "arq0.txt", "arq2.txt", "continua", "motivo:"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary misses %q: %q", want, sum)
		}
	}
	if note := h.ag.obHandoff(StopError, err.Error(), ""); !strings.Contains(note, "arq2.txt") {
		t.Errorf("model handoff note lost the files: %q", note)
	}
}

// No tool ran: nothing to keep, no summary (the plain error is enough).
func TestProviderDropNoToolsNoSummary(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Status: 400, Body: `{"error":{"message":"bad request"}}`}), nil)
	_ = h.run(t, "oi")
	for _, n := range h.ui.notices {
		if strings.Contains(n, "provedor caiu") {
			t.Errorf("summary without work: %q", n)
		}
	}
}
