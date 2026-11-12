//go:build !windows

package main

import (
	"fmt"
	"strings"
	"testing"
)

// Real phrases from the v0.13 sessions (anonymized) and the kind they need.
// The study found 35 of 42 human questions classified as M/change, with the
// "New code" hint on a wifi request.
func TestBgClassifyRealPhrases(t *testing.T) {
	cases := []struct{ in, kind string }{
		{"arruma meu wifi", "ops"},
		{"conecta no wifi", "ops"},
		{"o wifi ta conectado aqui?", "ops"},
		{"ta ligado no wifi?", "ops"},
		{"wifi ta funcionando?", "ops"},
		{"o wifi ta ligado?", "ops"},
		{"acho que o wifi n ta funcionando", "ops"},
		{"que dispositivos estao online na tailscale?", "ops"},
		{"o hexstrike ta online?", "ops"},
		{"instala o docker aqui", "ops"},
		{"reinicia o nginx", "ops"},
		{"quanto de disco livre tem?", "ops"},
		{"libera a porta 8080 no firewall", "ops"},
		{"porra arruma ae essa merda", "change"},
		{"oi", "chat"},
		{"salve", "chat"},
		{"salve jazz", "chat"},
		{"oi tudo bem?", "chat"},
		{"valeu", "chat"},
		{"top né", "chat"},
		{"tá aí jazz?", "question"},
		{"cade o video?", "question"},
		{"o que tu melhoraria?", "question"},
		{"tem algum jeito de ver isso", "question"},
		{"que pista?", "question"},
		{"corrige o parser do csv", "bugfix"},
		{"por que o teste de config ta falhando?", "bugfix"},
		{"adiciona a porta 8080 no config.go", "greenfield"},
		{"muda a porta do servidor no código", "change"},
		{"how does the router pick a model?", "question"},
		{"como funciona o checkpoint?", "question"},
	}
	ok := 0
	for _, c := range cases {
		got := bgClassify(c.in).Kind
		if got == c.kind {
			ok++
		} else {
			t.Errorf("bgClassify(%q).Kind = %s, want %s", c.in, got, c.kind)
		}
	}
	if ok*10 < len(cases)*9 {
		t.Errorf("only %d/%d right (want >= 90%%)", ok, len(cases))
	}
	// go-aheads carry the previous task on: never chat
	for _, s := range []string{"bora", "marcha", "continua", "pode", "faz"} {
		if k := bgClassify(s).Kind; k == "chat" {
			t.Errorf("%q must not be chat", s)
		}
	}
}

func TestBgOpsNeverCodeWithFileOrSymbol(t *testing.T) {
	for _, s := range []string{"adiciona a porta 8080 no config.go", "o servidor em `main.go` não sobe", "fix the ssh dial in `Dial`", "muda o timeout da rede na função connect"} {
		if k := bgClassify(s).Kind; k == "ops" {
			t.Errorf("%q: a code request must not be ops", s)
		}
	}
	steps, explore := bgBudgets(bgClassify("arruma meu wifi"), bgLean)
	if explore != 0 || steps < 40 {
		t.Errorf("ops budget: steps=%d explore=%d, want >=40 and 0", steps, explore)
	}
	if h := bgKindHint(bgTask{Kind: "ops", Size: 'M'}); h != "" {
		t.Errorf("ops gets no coding hint: %q", h)
	}
}

func TestBgReminderPTNoFinalReplyTemplate(t *testing.T) {
	for _, k := range []string{"bugfix", "greenfield", "refactor", "change"} {
		bt := &bgTurn{task: bgTask{Size: 'M', Kind: k, Sensitive: true}, level: bgLean, stepBudget: 25, exploreCap: 8}
		r := bgReminder(bt)
		for _, bad := range []string{"Final reply", "8 lines", "Budget for", "read/search", "New code", "High-stakes"} {
			if strings.Contains(r, bad) {
				t.Errorf("%s: reminder still has %q: %s", k, bad, r)
			}
		}
		if !strings.Contains(r, "Orçamento desta tarefa (tamanho M): uns 25 passos") {
			t.Errorf("%s: PT reminder: %s", k, r)
		}
		if n := len([]rune(r)); n > 340 { // the v0.13 English note with the 8-line rule was ~560
			t.Errorf("%s: reminder too long (%d chars): %s", k, n, r)
		}
	}
}

// v0.14: no exploration call is ever refused, in ops or in code tasks.
func TestBudgetNoExploreBlock(t *testing.T) {
	for _, prompt := range []string{"arruma meu wifi", "fix the bug in f0.go"} {
		h := newHarness(t, func(n int, r recReq) fakeResp {
			if n < 16 { // under the lean hard stop of an S task (1.5x12): only the cap is under test
				return fakeResp{Calls: []fakeCall{{ID: fmt.Sprintf("b%d", n), Name: "bash", Args: fmt.Sprintf(`{"command":"cat f%d.go"}`, n%10)}}}
			}
			return fakeResp{Text: "pronto"}
		}, func(c *Config) { c.Quality = qgOff; c.MaxSteps = 60 })
		bgWriteFiles(t, h.root, 10)
		if err := h.run(t, prompt); err != nil {
			t.Fatal(err)
		}
		reqs := h.fs.requests()
		last := reqs[len(reqs)-1]
		if len(reqs) < 17 {
			t.Fatalf("%q: the turn was cut at %d requests", prompt, len(reqs))
		}
		for id, res := range last.toolResults() {
			if strings.Contains(res, "TOOL_NOT_EXECUTED") {
				t.Errorf("%q: %s refused: %q", prompt, id, res)
			}
		}
		notes := 0
		for _, res := range last.toolResults() {
			notes += strings.Count(res, "sem editar")
		}
		if prompt == "arruma meu wifi" && notes != 0 {
			t.Errorf("ops: no exploration note at all, got %d", notes)
		}
		if prompt != "arruma meu wifi" && notes != 1 {
			t.Errorf("code task: exactly one soft exploration note, got %d", notes)
		}
		if reqs[0].contains("user", "Orçamento desta tarefa") && prompt == "arruma meu wifi" {
			t.Error("ops gets no budget reminder")
		}
	}
}

func TestBudgetChatHasNoBudget(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "oi!"}), nil)
	if got := h.ag.bgBeginTurn("oi"); got != "oi" {
		t.Errorf("chat gets no reminder: %q", got)
	}
	if bt := h.ag.bgTurnState(); bt == nil || bt.task.Kind != "chat" || bt.hardSteps || bt.exploreCap != 0 {
		t.Errorf("chat turn: no cap, no hard stop (quality gate kept): %+v", bt)
	}
	h.ag.bgBeginTurn("arruma meu wifi")
	bt := h.ag.bgTurnState()
	if bt == nil || bt.task.Kind != "ops" || bt.hardSteps || bt.exploreCap != 0 {
		t.Errorf("ops turn: %+v", bt)
	}
}
