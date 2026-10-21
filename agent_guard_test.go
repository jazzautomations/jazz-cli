package main

import (
	"strings"
	"testing"
)

func TestAcClaimsAction(t *testing.T) {
	yes := []string{
		"Criei o arquivo hello.txt com a palavra oi.",
		"Pronto! Editei o main.go e rodei os testes.",
		"- Instalei as dependências",
		"I created hello.txt containing oi.",
		"I've fixed the bug and ran the tests.",
		"We just updated the config.",
		"Criei o arquivo, mas não testei.",
	}
	no := []string{
		"",
		"Não criei nenhum arquivo ainda.",
		"Esta função cria o arquivo se ele não existir.",
		"Quer que eu crie o arquivo?",
		"Vou criar o arquivo agora.",
		"The function creates the file and runs the tests.",
		"I will create the file next.",
		"I didn't create anything.",
		"It was installed by the package manager.",
		"O arquivo foi criado pelo script de build.",
		"Recriei? Não: a palavra recriei está aqui só como exemplo de borda xcriei.",
	}
	for _, s := range yes {
		if !acClaimsAction(s) {
			t.Errorf("should claim: %q", s)
		}
	}
	for _, s := range no {
		if acClaimsAction(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

// A pretend reply gets one nudge; the model then really calls the tool.
func TestAgentFakeActionNudgeThenTool(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "Criei o arquivo hello.txt com oi."},
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: jsonArgs(map[string]any{"path": "hello.txt", "content": "oi\n"})}}},
		fakeResp{Text: "Criei o arquivo hello.txt."},
	), nil)
	stop, err := h.ag.Run(t.Context(), "crie hello.txt com oi")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopDone {
		t.Fatalf("stop %s", stop)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 || !reqs[1].contains("user", "You did not call any tool in this turn") {
		t.Fatalf("nudge missing; %d requests", len(reqs))
	}
	if anyContains(h.ui.warns, "não verificada") {
		t.Errorf("no warning once a tool ran: %v", h.ui.warns)
	}
	if h.ui.turns[0].Stop != string(StopDone) {
		t.Errorf("TurnStats.Stop %q", h.ui.turns[0].Stop)
	}
}

// A model that keeps pretending ends the turn flagged as unverified.
func TestAgentFakeActionPersistentWarns(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "Criei o arquivo hello.txt."},
		fakeResp{Text: "Já criei o arquivo, está pronto."},
		fakeResp{Text: "never reached"},
	), nil)
	stop, err := h.ag.Run(t.Context(), "crie hello.txt com oi")
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopUnverified || stop.ExitCode() != 7 {
		t.Fatalf("stop %s", stop)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Fatalf("exactly one nudge: %d requests", n)
	}
	if !anyContains(h.ui.warns, unverifiedWarning) {
		t.Errorf("warning missing: %v", h.ui.warns)
	}
	if h.ui.turns[0].Stop != string(StopUnverified) {
		t.Errorf("TurnStats.Stop %q", h.ui.turns[0].Stop)
	}
}

// Pure explanations and questions never trigger the guard.
func TestAgentFakeActionNoFalsePositive(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "Esta função cria o arquivo e roda os testes."}), nil)
	if stop, _ := h.ag.Run(t.Context(), "o que faz a função?"); stop != StopDone {
		t.Fatalf("stop %s", stop)
	}
	if n := len(h.fs.requests()); n != 1 {
		t.Fatalf("no nudge for explanations: %d requests", n)
	}
	for _, m := range h.ag.Msgs {
		if strings.Contains(m.Content, "You did not call any tool") {
			t.Fatal("nudge sent")
		}
	}
}
