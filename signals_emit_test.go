package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// sigTestLedger points the ledger at a temp file and turns emission on for
// this test only (a test binary never writes the owner's ledger otherwise).
func sigTestLedger(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "signals.jsonl")
	old := sigPath
	sigPath = func() string { return p }
	sigTestOn.Store(true)
	t.Cleanup(func() { sigPath = old; sigTestOn.Store(false) })
	return p
}

func sigKinds(ss []Signal) string {
	var k []string
	for _, s := range ss {
		k = append(k, s.Kind)
	}
	return strings.Join(k, ",")
}

// Emission is off inside a test binary unless a test asks for it, and the
// off switches (JAZZ_SIGNALS=off, --fresh, the eval's mute) always win.
func TestSignalEmitMutedUnderTest(t *testing.T) {
	if !jzUnderTest() {
		t.Fatal("jzUnderTest deve ser true dentro do go test")
	}
	if sigEnabled() {
		t.Fatal("sinais não podem ser emitidos por um binário de teste sem opt-in")
	}
	sigTestLedger(t)
	if !sigEnabled() {
		t.Fatal("opt-in do teste deve ligar a emissão")
	}
	t.Setenv("JAZZ_SIGNALS", "off")
	if sigEnabled() {
		t.Error("JAZZ_SIGNALS=off deve desligar")
	}
	t.Setenv("JAZZ_SIGNALS", "")
	sigMuted.Store(true)
	if sigEnabled() {
		t.Error("sigMuted (eval) deve desligar")
	}
	sigMuted.Store(false)
	pfFreshMode.Store(true)
	if sigEnabled() {
		t.Error("--fresh deve desligar")
	}
	pfFreshMode.Store(false)
}

// Each brain outcome maps to the documented kinds; a verified success the
// brain did by itself is not a signal.
func TestSignalEmitKinds(t *testing.T) {
	low := Intent{Name: "run_tests", Confidence: 0.69, Raw: 0.66, Alt: []string{"build"}}
	cases := []struct {
		r    brResult
		want string
	}{
		{brResult{Miss: true, MissWhy: "not_understood", Intent: Intent{Name: "none"}}, "unknown_intent"},
		{brResult{Miss: true, MissWhy: "not_understood", Intent: Intent{Name: "build", Confidence: 0.4}}, "unknown_intent"},
		{brResult{Miss: true, MissWhy: "below_act", Intent: low}, "miss"},
		{brResult{Miss: true, MissWhy: "rejected", Intent: low}, "miss"},
		{brResult{MissWhy: "edit", Intent: low}, "miss"},
		{brResult{NoMethod: true, Intent: Intent{Name: "create", Confidence: 0.9}}, "new_method"},
		{brResult{Handled: true, Status: tkUnverified, Method: "m1", Intent: low}, "verifier_missing"},
		{brResult{Handled: true, Status: tkSucceeded, Intent: low}, ""},
		{brResult{Handled: true, Status: tkSucceeded, Intent: Intent{Name: "run_tests", Confidence: 0.69}, Confirmed: &low}, "correction"},
	}
	for i, c := range cases {
		if got := sigKinds(brSignals("roda parser", c.r)); got != c.want {
			t.Errorf("caso %d: %q, esperado %q", i, got, c.want)
		}
	}
	s := brSignals("roda parser", cases[2].r)[0]
	if s.Extra["reason"] != "below_act" || s.Extra["intent"] != "run_tests" || s.Extra["confidence"] != "0.69" || s.Extra["raw"] != "0.66" || s.Extra["alt"] != "build" {
		t.Errorf("leitura não empacotada no extra: %+v", s.Extra)
	}
	c := brSignals("roda parser", cases[8].r)[0]
	if c.Chosen != "run_tests" || !c.Verified || c.Extra["how"] != "confirm" || c.Class != "intent" {
		t.Errorf("correção do confirm: %+v", c)
	}
	// multi: one set per part, each with its own text
	m := brResult{Handled: false, Intent: Intent{Name: "multi"}, Sub: []brResult{
		{Req: "lista os arquivos", Handled: true, Status: tkSucceeded},
		{Req: "cria algo", NoMethod: true, Intent: Intent{Name: "create", Confidence: 0.9}},
	}}
	ss := brSignals("lista os arquivos e cria algo", m)
	if sigKinds(ss) != "new_method" || ss[0].Text != "cria algo" {
		t.Errorf("multi deve emitir por parte: %+v", ss)
	}
}

// A P2 menu pick is the strong label: one correction (how=menu), the
// reading the brain had in extra, never doubled by the confirm of a mutant.
func TestSignalEmitMenuPick(t *testing.T) {
	orig := Intent{Name: "build", Confidence: 0.42}
	r := brResult{Handled: true, Status: tkSucceeded, Intent: Intent{Name: "run_tests", Confidence: brLevel0Min}, Confirmed: &Intent{Name: "run_tests"}}
	ss := sigPickSignals("testa tudo", &orig, r)
	if sigKinds(ss) != "correction" {
		t.Fatalf("um pick = uma correção: %s", sigKinds(ss))
	}
	if ss[0].Chosen != "run_tests" || !ss[0].Verified || ss[0].Extra["how"] != "menu" || ss[0].Extra["intent"] != "build" || ss[0].Extra["confidence"] != "0.42" {
		t.Errorf("correção do menu: %+v", ss[0])
	}
}

// The brain marks the outcome (pure) and the flush writes it, redacted.
func TestSignalEmitConfirmWritesLedger(t *testing.T) {
	sigTestLedger(t)
	root := brFixture(t)
	env, _ := brTestEnv(t, root, map[string]brFakeOut{"go test": {"ok  \tx\t0.01s\n", 0}}, 0)
	r := brRun(context.Background(), env, "roda parser") // run_tests 0.69: the confirm box, Enter
	if r.Confirmed == nil || r.Confirmed.Confidence >= brLevel0Min {
		t.Fatalf("o confirm deve guardar a leitura original: %+v", r.Confirmed)
	}
	if got, _ := sigRead(nil); len(got) != 0 {
		t.Fatalf("brRun é puro, não escreve no ledger: %+v", got)
	}
	sigFlush("roda parser", r, map[string]string{"where": "test"})
	sigFlush("roda parser sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", brResult{Miss: true, MissWhy: "not_understood", Intent: Intent{Name: "none"}}, nil)
	got, err := sigRead(nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("ledger: %v %+v", err, got)
	}
	if got[0].Kind != "correction" || got[0].Chosen != "run_tests" || got[0].Extra["where"] != "test" {
		t.Errorf("correção: %+v", got[0])
	}
	if strings.Contains(got[1].Text, "AAAAAAAAAAAAAAAAAAAA") {
		t.Errorf("segredo no ledger: %q", got[1].Text)
	}
}

// The eval runs the brain on fixtures: nothing lands in the ledger.
func TestSignalEmitEvalOfflineMuted(t *testing.T) {
	p := sigTestLedger(t)
	if _, err := eoRun(map[string]bool{"self-version": true}, false, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if sigEnabled() != true {
		t.Error("o eval deve restaurar a emissão ao sair")
	}
	if got, _ := sigRead(nil); len(got) != 0 {
		t.Errorf("o eval escreveu em %s: %+v", p, got)
	}
}

// Headless --offline acts at the same threshold as the session and level 0:
// a read-only reading at 0.75 runs, a non-read one is an honest miss.
func TestSignalEmitHeadlessActThreshold(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	env.Ask = nil
	r := brRunIntent(context.Background(), env, "lista os arquivos", Intent{Name: "list_files", Confidence: 0.75, Slots: map[string]string{}}, brResult{})
	if r.Miss || r.MissWhy != "" || !r.Handled {
		t.Errorf("leitura só-leitura a 0.75 age headless: handled=%v miss=%v why=%q\n%s", r.Handled, r.Miss, r.MissWhy, r.Text)
	}
	r = brRunIntent(context.Background(), env, "roda os testes", Intent{Name: "run_tests", Confidence: 0.75, Slots: map[string]string{}}, brResult{})
	if !r.Miss || r.MissWhy != "below_act" {
		t.Errorf("mutante/execução a 0.75 é miss honesto headless: miss=%v why=%q", r.Miss, r.MissWhy)
	}
}
