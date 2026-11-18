package main

import (
	"fmt"
	"strings"
	"testing"
)

// Session of 17/11/2026: a turn that reached max_turn_tokens stopped in
// silence (the "Pare agora" reminder was appended and the loop returned
// without asking the model), and three research subagents came back "without
// a report" for the same reason. When the request about to be sent is the last
// one that fits under the cap, it must run without tools so the turn ends with
// an answer, and the cap itself must hold.

func TestTurnCapEndsWithAnswer(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Raw["tool_choice"] == "none" {
			return fakeResp{Text: "RESUMO-FINAL", UsageIn: 400}
		}
		return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("c%d", n), Name: "bash", Args: `{"command":"ls"}`}}, UsageIn: 400}
	}, func(c *Config) { c.MaxTurnTokens = 1000 })
	_ = h.run(t, "go")

	reqs := h.fs.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one tool step, then the final answer)", len(reqs))
	}
	if reqs[1].Raw["tool_choice"] != "none" {
		t.Errorf("the last request must not offer tool use: tool_choice = %v", reqs[1].Raw["tool_choice"])
	}
	last := h.ag.Msgs[len(h.ag.Msgs)-1]
	if last.Role != "assistant" || last.Content != "RESUMO-FINAL" {
		t.Errorf("the turn must end with the model's answer, got %s: %q", last.Role, last.Content)
	}
	if n := h.ag.turnUsage.In + h.ag.turnUsage.Out; n > 1000 {
		t.Errorf("turn tokens = %d, the cap (1000) must hold", n)
	}
}

func TestNearBudgetCostCap(t *testing.T) {
	a := &Agent{cfg: &Config{MaxTurnCost: 1.0}}
	a.lastPrompt = 500
	a.turnUsage = Usage{In: 1000, Cost: 0.5} // the next two requests cost about 0.25 each: 1.0, still fits
	if why := a.nearBudget(); why != "" {
		t.Errorf("two more requests fit under the cost cap: %q", why)
	}
	a.turnUsage.Cost = 0.6 // 0.6 + 2×0.3 = 1.2
	if why := a.nearBudget(); !strings.Contains(why, "custo") {
		t.Errorf("only one more request fits under the cost cap: %q", why)
	}
	a.turnUsage = Usage{} // a new turn: nothing measured yet
	if why := a.nearBudget(); why != "" {
		t.Errorf("a fresh turn is not near its cap: %q", why)
	}
}

func TestSubagentCapReturnsReport(t *testing.T) {
	t.Setenv("JAZZ_SUBAGENTS_PARALLEL", "3")
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if saIsChild(r) {
			if r.Raw["tool_choice"] == "none" {
				return fakeResp{Text: "RELATORIO " + saGoal(r), UsageIn: 300}
			}
			return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("k%d", n), Name: "bash", Args: `{"command":"ls"}`}}, UsageIn: 300}
		}
		if len(r.toolResults()) == 0 {
			return fakeResp{Calls: []fakeCall{saTaskCall("t1", "GOAL-A"), saTaskCall("t2", "GOAL-B"), saTaskCall("t3", "GOAL-C")}}
		}
		return fakeResp{Text: "done"}
	}, func(c *Config) { c.MaxTurnTokens = 3000 }) // 1000 tokens for each child
	_ = h.run(t, "go")

	got := map[string]string{}
	for _, r := range h.fs.requests() {
		if saIsChild(r) {
			continue
		}
		for id, s := range r.toolResults() {
			got[id] = s
		}
	}
	for id, goal := range map[string]string{"t1": "GOAL-A", "t2": "GOAL-B", "t3": "GOAL-C"} {
		if !strings.Contains(got[id], "RELATORIO "+goal) || strings.Contains(got[id], "without a report") {
			t.Errorf("%s: the parent must get the child's report, got %q", id, got[id])
		}
	}
	if n := h.ag.turnUsage.In + h.ag.turnUsage.Out; n > 3000 {
		t.Errorf("turn tokens = %d, the cap (3000) must hold", n)
	}
}
