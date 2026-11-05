package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fgTestHome isolates the forge: data dir, ledger and overlay in temp dirs,
// no audit records.
func fgTestHome(t *testing.T) fgOpts {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("JAZZ_AUDIT", "0")
	p := filepath.Join(home, "signals.jsonl")
	old := sigPath
	sigPath = func() string { return p }
	t.Cleanup(func() { sigPath = old })
	return fgOpts{Overlay: nuOverlayPath()}
}

func fgCorrect(t *testing.T, intent string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if err := sigAppend(Signal{Kind: "correction", Class: "intent", Text: s, Chosen: intent, Extra: map[string]string{"how": "menu"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// A real miss: "lista as funções mais compridas" has no rule and the
// shipped classifier is not sure enough to act (0.66 < 0.7). The owner's
// corrections teach it; the held-out one is fixed by the others, so the
// gate accepts, the overlay is written and the runtime model acts.
func TestForgeAcceptsCorrectionThatFixesMiss(t *testing.T) {
	o := fgTestHome(t)
	target := "lista as funções mais compridas"
	base := nuFreshModel(t)
	w := nuWorldFromDef(base.data.World)
	if in := base.Parse(target, w); fgActs(in) && in.Name == "large_funcs" {
		t.Fatalf("pré-condição: a gramática embarcada não deve agir em %q (%s %.2f)", target, in.Name, in.Confidence)
	}
	fgCorrect(t, "large_funcs", target, "quais as funções mais compridas", "me diz as funções mais compridas", target)
	fgCorrect(t, "large_funcs", "cadê as funções mais compridas") // a find rule decides: an example cannot move it

	o.DryRun = true
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Accept || len(rep.Added) != 3 || rep.Wrote {
		t.Fatalf("dry-run deve aceitar 3 e não gravar: %+v", rep)
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Fatal("--dry-run gravou o overlay")
	}
	g := rep.Gate
	if g.CandOut.Accuracy <= g.BaseOut.Accuracy && g.CandOut.Coverage <= g.BaseOut.Coverage || g.CandOut.FalseSuccess != 0 {
		t.Errorf("o portão aceitou sem melhoria held-out: %+v", g)
	}
	heldOut := 0
	for _, a := range rep.Added {
		if a.HeldOut {
			heldOut++
		}
		if a.Text == target && a.Count != 2 {
			t.Errorf("correções idênticas colapsam com contagem: %+v", a)
		}
	}
	if heldOut == 0 {
		t.Error("ao menos um exemplo novo deve ir pro held-out")
	}
	dropped := map[string]string{}
	for _, d := range rep.Dropped {
		dropped[d.Text] = d.Reason
	}
	if !strings.Contains(dropped["cadê as funções mais compridas"], "não muda a leitura") {
		t.Errorf("leitura decidida por regra deve cair: %v", dropped)
	}

	o.DryRun = false
	rep, err = fgRun(o)
	if err != nil || !rep.Accept || !rep.Wrote {
		t.Fatalf("forja real: %v %+v", err, rep)
	}
	ov, err := nuReadOverlay(o.Overlay)
	if err != nil || ov == nil || len(ov.Examples["large_funcs"]) != 3 || len(ov.Provenance) != 3 || ov.BaseSHA256 != nuEmbeddedSHA() {
		t.Fatalf("overlay gravado: %v %+v", err, ov)
	}
	m := nuFreshModel(t)
	nuApplyOverlay(m, ov)
	if in := m.Parse(target, w); !(fgActs(in) && in.Name == "large_funcs") {
		t.Errorf("com o overlay o cérebro deve agir em %q: %s %.2f", target, in.Name, in.Confidence)
	}

	// idempotent: the same ledger adds nothing the second time
	before, _ := os.ReadFile(o.Overlay)
	rep, err = fgRun(o)
	if err != nil || rep.Accept || rep.Wrote {
		t.Fatalf("segunda rodada não tem o que forjar: %v %+v", err, rep)
	}
	after, _ := os.ReadFile(o.Overlay)
	if string(before) != string(after) {
		t.Error("rodada rejeitada mexeu no overlay")
	}
}

// A poisoned label: the owner (or a bad pick) says "mostra os logs do nginx
// <x>" is open_file. The exact negative is dropped up front; the variants
// would make the negative itself land on open_file — false success, the
// gate blocks, nothing is written.
func TestForgeRejectsPoisonedCorrection(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "open_file", "mostra os logs do nginx", "mostra os logs do nginx agora", "mostra os logs do nginx aqui",
		"mostra os logs do nginx rapidinho", "mostra os logs do nginx de novo", "mostra os logs do nginx inteiros")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept || rep.Wrote {
		t.Fatalf("rótulo envenenado não pode entrar: %+v", rep)
	}
	if !strings.Contains(rep.Reason, "false_success") || rep.Gate == nil || rep.Gate.CandIn.FalseSuccess+rep.Gate.CandOut.FalseSuccess == 0 {
		t.Errorf("a recusa deve ser por falso sucesso: %s", rep.Reason)
	}
	found := false
	for _, d := range rep.Dropped {
		if d.Text == "mostra os logs do nginx" && strings.Contains(d.Reason, "negativo") {
			found = true
		}
	}
	if !found {
		t.Errorf("o negativo exato deve cair antes do portão: %+v", rep.Dropped)
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Error("recusa gravou o overlay")
	}
}

// Labels that are not intents, oversized texts, contradictory labels and a
// lone correction never reach the gate.
func TestForgeDropsBadLabelsAndNeedsData(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "go test ./...", "roda os testes do pacote") // a command, not an intent
	fgCorrect(t, "large_funcs", strings.Repeat("funções compridas ", 20))
	fgCorrect(t, "large_funcs", "quais funções passam do limite")
	fgCorrect(t, "dup_code", "quais funções passam do limite") // same text, other label
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept || !strings.Contains(rep.Reason, "dados insuficientes") {
		t.Fatalf("uma correção só não passa: %+v", rep)
	}
	var reasons []string
	for _, d := range rep.Dropped {
		reasons = append(reasons, d.Reason)
	}
	all := strings.Join(reasons, " | ")
	for _, w := range []string{"não é uma intenção", "200 caracteres", "intenções diferentes"} {
		if !strings.Contains(all, w) {
			t.Errorf("descarte sem %q: %s", w, all)
		}
	}
	if rep.Signals != 5 {
		t.Errorf("signals=%d, esperado 5", rep.Signals)
	}
}

// The command: usage, exit codes and --json.
func TestForgeCommand(t *testing.T) {
	o := fgTestHome(t)
	if cmdForge([]string{"--nope"}, &strings.Builder{}) != 2 {
		t.Error("flag desconhecida = uso (2)")
	}
	var b strings.Builder
	if code := cmdForge([]string{"--json"}, &b); code != 1 {
		t.Errorf("ledger vazio = nada a forjar (1), veio %d", code)
	}
	var rep fgReport
	if err := json.Unmarshal([]byte(b.String()), &rep); err != nil || rep.Accept || rep.Overlay != o.Overlay {
		t.Errorf("--json: %v %+v\n%s", err, rep, b.String())
	}
	fgCorrect(t, "large_funcs", "lista as funções mais compridas", "quais as funções mais compridas", "me diz as funções mais compridas")
	b.Reset()
	if code := cmdForge([]string{"--dry-run"}, &b); code != 0 {
		t.Errorf("dry-run aceito = 0, veio %d\n%s", code, b.String())
	}
	for _, w := range []string{"forja: 3 correção(ões)", "portão: aceito", "cand held-out", "dry-run: nada gravado"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("relatório sem %q:\n%s", w, b.String())
		}
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Error("--dry-run gravou")
	}
}
