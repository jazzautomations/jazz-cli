//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func askTestArgs() string {
	return jsonArgs(map[string]any{"question": "Qual banco usar?", "options": []map[string]any{
		{"label": "SQLite", "description": "arquivo local"}, {"label": "Postgres"}, {"label": "manter o atual"}}})
}

func askInteractive(t *testing.T, typed string) (string, bool, string) {
	t.Helper()
	u, keys := uiTestUI(t)
	read := uiTestStdout(t)
	q, bad := askParse(askTestArgs())
	if bad != "" {
		t.Fatal(bad)
	}
	type res struct {
		ans string
		ok  bool
	}
	done := make(chan res, 1)
	go func() {
		a, ok := u.AskUser(context.Background(), q)
		done <- res{a, ok}
	}()
	time.Sleep(uiApproveGrace + 50*time.Millisecond)
	uiSendRunes(keys, typed)
	select {
	case r := <-done:
		return r.ans, r.ok, stripANSI(read())
	case <-time.After(3 * time.Second):
		t.Fatal("AskUser did not return")
	}
	return "", false, ""
}

func TestAskUserInteractivePick(t *testing.T) {
	ans, ok, out := askInteractive(t, "2\n")
	if !ok || ans != "Postgres" {
		t.Fatalf("pick = %q %v", ans, ok)
	}
	for _, w := range []string{"─ pergunta ─", "│ Qual banco usar?", "1. SQLite  arquivo local", "2. Postgres", "3. manter o atual", "4. Outra resposta (escrever)", "comece a digitar a resposta", "esc pula"} {
		if !strings.Contains(out, w) {
			t.Errorf("menu misses %q:\n%s", w, out)
		}
	}
}

func TestAskUserInteractiveFreeText(t *testing.T) {
	ans, ok, _ := askInteractive(t, "usa o que já tem no docker-compose\n")
	if !ok || ans != "usa o que já tem no docker-compose" {
		t.Fatalf("free text = %q %v", ans, ok)
	}
	if a, _, _ := askInteractive(t, "sq\n"); a != "SQLite" {
		t.Errorf("first letters = %q", a)
	}
}

// Headless: the tool never blocks; the model is told to pick a default and
// say so. The question does not count as an action.
func TestAskUserHeadlessDefault(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "q1", Name: "ask_user", Args: askTestArgs()}}},
		fakeResp{Text: "Assumi SQLite (padrão) porque não havia usuário pra responder."},
	), nil)
	h.ag.ui = &HeadlessUI{}
	if err := h.run(t, "adiciona persistência"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range h.ag.Msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "headless: no user available") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no headless default result in %+v", h.ag.Msgs)
	}
	// the spec is offered (byte-stable list, main agent only)
	reqs := h.fs.requests()
	if len(reqs) == 0 || !strings.Contains(jsonArgs(reqs[0].Tools), `"ask_user"`) {
		t.Error("ask_user not in the tool list")
	}
	// --ask-stdin reads one line
	h2 := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "q1", Name: "ask_user", Args: askTestArgs()}}},
		fakeResp{Text: "ok"},
	), nil)
	h2.ag.ui = &HeadlessUI{}
	h2.ag.AskStdin = true
	old := askStdin
	askStdin = strings.NewReader("3\n")
	t.Cleanup(func() { askStdin = old })
	if err := h2.run(t, "adiciona persistência"); err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, m := range h2.ag.Msgs {
		if m.Role == "tool" {
			got = m.Content
		}
	}
	if got != "The user answered: manter o atual" {
		t.Errorf("--ask-stdin result %q", got)
	}
	// bad arguments are a validation error, not a hang
	if _, bad := askParse(`{"question":"x","options":["só uma"]}`); bad == "" {
		t.Error("one option accepted")
	}
}
