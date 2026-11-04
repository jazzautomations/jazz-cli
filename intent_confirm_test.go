package main

// intent_confirm_test.go: the two flagship items of the offline UX
// (docs/research/ux-intent-confirmation.md, P1 and P2).
//
//   P1 — intention confirmation in a double band: confidence × risk of the
//   method brExecute would pick. A confident read-only reading acts at once
//   (anti-fatigue); the medium confidence band and every mutant (risk ≥ high)
//   get one key: "entendi 'X' como 'Y' — [enter] faz · [e] edita · [n] não".
//   Headless without a TTY never acts on the medium band: honest miss.
//
//   P2 — the honest miss is a top-k menu built from the reading, the
//   interpreter's runner-ups (in.Alt, computed by nlu.go and unused until
//   now) and the contextual actions; headless prints the same list numbered.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- P1 ----------

// A confident read-only reading never pays a key (UX-VII, anti-fatigue).
func TestIntentConfirmHighConfReadOnlyActs(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "abre o parser.go")
	if !r.Handled || r.Status != tkSucceeded {
		t.Fatalf("abre o parser.go: handled=%v status=%s\n%s", r.Handled, r.Status, r.Text)
	}
	if len(rec.asked) != 0 || r.Questions != 0 {
		t.Errorf("read-only confiante não pergunta: asked=%v questions=%d", rec.asked, r.Questions)
	}
}

// The medium band (nuMinConf..brLevel0Min) shows the one-key confirmation:
// the reading, the three ways out and the method risk.
func TestIntentConfirmMediumBandAsksOneKey(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "quem usa a porta 8080") // find, conf 0.84
	if len(rec.asked) != 1 {
		t.Fatalf("banda média deve confirmar uma vez, asked=%v\n%s", rec.asked, r.Text)
	}
	q := rec.asked[0]
	for _, w := range []string{
		"entendi 'quem usa a porta 8080' como",
		"[enter] faz · [e] edita · [n] não",
		"Sim, achar símbolo ou texto no código",
		"Editar o pedido",
		"Não — ver outras opções",
	} {
		if !strings.Contains(q, w) {
			t.Errorf("confirmação sem %q:\n%s", w, q)
		}
	}
	if !r.Handled || r.Questions != 1 {
		t.Errorf("confirmado, o intent roda: handled=%v questions=%d\n%s", r.Handled, r.Questions, r.Text)
	}
}

// A mutant always asks, even at maximum confidence, and says the risk.
func TestIntentConfirmMutantAlwaysAsks(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "apaga o parser.go") // delete_file, risk high, conf 0.90
	if len(rec.asked) != 1 {
		t.Fatalf("mutante deve confirmar, asked=%v\n%s", rec.asked, r.Text)
	}
	if !strings.Contains(rec.asked[0], "risco alto") {
		t.Errorf("confirmação não nomeia o risco:\n%s", rec.asked[0])
	}
	if !r.Handled || r.Status != tkSucceeded {
		t.Fatalf("confirmado, o arquivo sai: %s\n%s", r.Status, r.Text)
	}
	if _, err := os.Stat(filepath.Join(root, "parser.go")); err == nil {
		t.Error("parser.go ainda existe depois de confirmar a remoção")
	}
}

// [n] (or Esc) does not run: the P2 menu takes over.
func TestIntentConfirmNoFallsToMissMenu(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 2) // "Não — ver outras opções"
	r := brRun(context.Background(), env, "apaga o parser.go")
	if r.Handled || !r.Miss {
		t.Fatalf("recusar deve virar miss: handled=%v miss=%v\n%s", r.Handled, r.Miss, r.Text)
	}
	if len(rec.cmds) != 0 {
		t.Errorf("nada roda depois do não: %v", rec.cmds)
	}
	if _, err := os.Stat(filepath.Join(root, "parser.go")); err != nil {
		t.Error("parser.go sumiu mesmo sem confirmação")
	}
	if !strings.Contains(r.Text, "você quis:") {
		t.Errorf("o não deve cair no menu P2:\n%s", r.Text)
	}
}

// [e] does not run either: it hands back the reading jazz would send.
func TestIntentConfirmEditSuggestsRequest(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 1) // "Editar o pedido"
	r := brRun(context.Background(), env, "apaga o parser.go")
	if r.Handled || r.Miss {
		t.Fatalf("editar não executa: handled=%v miss=%v\n%s", r.Handled, r.Miss, r.Text)
	}
	if !strings.Contains(r.Text, "edite o pedido — sugestão:") {
		t.Errorf("editar deve sugerir o pedido corrigido:\n%s", r.Text)
	}
	if len(rec.cmds) != 0 {
		t.Errorf("editar não roda comando: %v", rec.cmds)
	}
}

// Headless without a TTY (-p, env.Ask == nil) never acts on the medium band:
// the honest miss with the numbered options is the answer.
func TestIntentConfirmHeadlessMediumMiss(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, nil, 0)
	env.Ask = nil
	r := brRun(context.Background(), env, "quem usa a porta 8080")
	if r.Handled || !r.Miss {
		t.Fatalf("headless na banda média não age: handled=%v miss=%v\n%s", r.Handled, r.Miss, r.Text)
	}
	if len(rec.cmds) != 0 {
		t.Errorf("headless não roda comando: %v", rec.cmds)
	}
	for _, w := range []string{"confiança 0.8", "você quis:", "  1 ", "digitar de novo"} {
		if !strings.Contains(r.Text, w) {
			t.Errorf("honest-miss sem %q:\n%s", w, r.Text)
		}
	}
}

// The typo-only reading uses the same one-key box (one question, not two).
func TestIntentConfirmTypoOnlyOneKey(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, map[string]brFakeOut{"go test": {"ok  \tx\t0.01s\n", 0}}, 0)
	env.World = nuWorldFromDef(nuGet().data.World)
	r := brRun(context.Background(), env, "tesr")
	if len(rec.asked) != 1 {
		t.Fatalf("typo-only deve confirmar exatamente uma vez: %v", rec.asked)
	}
	for _, w := range []string{"li 'tesr' como 'test'", "[enter] faz", "[e] edita", "[n] não"} {
		if !strings.Contains(rec.asked[0], w) {
			t.Errorf("confirmação de typo sem %q:\n%s", w, rec.asked[0])
		}
	}
	if !r.Handled || r.Questions != 1 {
		t.Errorf("confirmado, o typo roda: handled=%v questions=%d\n%s", r.Handled, r.Questions, r.Text)
	}
}

// A reading picked from the P2 menu runs with no second confirmation (the
// pick *is* the confirmation of the reading).
func TestIntentConfirmPickedReadingDoesNotReask(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, map[string]brFakeOut{"go test": {"ok  \tx\t0.01s\n", 0}}, 0)
	in := Intent{Name: "run_tests", Confidence: brLevel0Min, Slots: map[string]string{}}
	r := brRunIntent(context.Background(), env, "testa e builda", in, brResult{Intent: in})
	if len(rec.asked) != 0 {
		t.Fatalf("leitura escolhida não pergunta de novo: %v", rec.asked)
	}
	if !r.Handled || r.Status != tkSucceeded {
		t.Fatalf("leitura escolhida roda: %s\n%s", r.Status, r.Text)
	}
}

// ---------- P2 ----------

// The headless miss is a stable numbered menu: the hypothesis, the runner-ups
// (in.Alt) and "digitar de novo" — never the dead capabilities paragraph.
func TestMissHeadlessNumberedStable(t *testing.T) {
	in := Intent{Name: "none", Confidence: 0, Alt: []string{"build", "open_file"}}
	a, b := brNotUnderstood(in), brNotUnderstood(in)
	if a != b {
		t.Errorf("o menu do miss deve ser estável:\n%s\n---\n%s", a, b)
	}
	for _, w := range []string{"não entendi o pedido sem o modelo", "você quis:", "  1 compilar", "  2 abrir/mostrar um arquivo", "  3 ", "  4 digitar de novo"} {
		if !strings.Contains(a, w) {
			t.Errorf("menu headless sem %q:\n%s", w, a)
		}
	}
	if strings.Contains(a, "Posso: listar e buscar arquivos") {
		t.Errorf("o parágrafo morto voltou:\n%s", a)
	}
}

// The hypothesis line carries the confidence and the intent's own example.
func TestMissLinesHypothesisAndAlt(t *testing.T) {
	in := Intent{Name: "run_tests", Confidence: 0.52, Alt: []string{"build"}}
	lines := brMissLines(in)
	if len(lines) < 3 {
		t.Fatalf("poucas linhas: %q", lines)
	}
	if !strings.Contains(lines[0], "rodar testes") || !strings.Contains(lines[0], "confiança 0.52") {
		t.Errorf("hipótese sem descrição/confiança: %q", lines[0])
	}
	if !strings.Contains(lines[1], "compilar") {
		t.Errorf("in.Alt não virou item: %q", lines[1])
	}
	if !strings.Contains(lines[len(lines)-1], "digitar de novo") {
		t.Errorf("sem saída para digitar de novo: %q", lines)
	}
}

// The interactive menu runs the reading without re-parsing and keeps the
// exits: "digitar de novo" and /online.
func TestMissActionsBrainAndExits(t *testing.T) {
	root := brFixture(t)
	in := Intent{Name: "run_tests", Confidence: 0.52, Alt: []string{"build"}}
	mi := olMenuIn{Text: "testa e builda", Offline: true, Proj: fpProject{Test: "go test ./..."}, Root: root}
	items := brMissActions(in, mi)
	var brain []string
	var hasEdit, hasOnline bool
	for _, it := range items {
		switch it.Kind {
		case "brain":
			if it.In == nil || it.In.Name != it.Intent {
				t.Errorf("ação brain sem leitura forçada: %+v", it)
			}
			brain = append(brain, it.Intent)
		case "edit":
			hasEdit = true
		case "online":
			hasOnline = true
		}
	}
	if strings.Join(brain, ",") != "run_tests,build" {
		t.Errorf("palpites no menu = %v, queria [run_tests build]", brain)
	}
	if !hasEdit || !hasOnline {
		t.Errorf("menu sem saídas: edit=%v online=%v", hasEdit, hasOnline)
	}
	if len(items) > olMenuMax+1 {
		t.Errorf("%d itens, queria no máximo %d", len(items), olMenuMax+1)
	}
}

// An unsure guess never takes item 1: the safe contextual actions come first
// (UX-II/UX-VII — item 1 is the safest, not the most likely).
func TestMissActionsSafeFirstWhenUnsure(t *testing.T) {
	root := brFixture(t)
	mi := olMenuIn{Text: "apaga o parser.go mesmo", Offline: true, Root: root}
	unsure := brMissActions(Intent{Name: "delete_file", Confidence: 0.3}, mi)
	if len(unsure) == 0 || unsure[0].Kind == "brain" {
		t.Fatalf("palpite incerto não pode ser o item 1: %+v", unsure)
	}
	found := false
	for _, it := range unsure {
		found = found || it.Kind == "brain" && it.Intent == "delete_file"
	}
	if !found {
		t.Errorf("o palpite incerto sumiu do menu: %+v", unsure)
	}
	sure := brMissActions(Intent{Name: "run_tests", Confidence: 0.9}, mi)
	if len(sure) == 0 || sure[0].Kind != "brain" || sure[0].Intent != "run_tests" {
		t.Errorf("hipótese confiante deve ser o item 1: %+v", sure)
	}
}
