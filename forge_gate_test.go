package main

import (
	"fmt"
	"strings"
	"testing"
)

// fgExamples monta um conjunto de validação fixo: 3 grupos de 8 exemplos.
// Com frac=0.25 o split estratificado tira exatamente 2 por grupo.
func fgExamples() []Example {
	var exs []Example
	for _, g := range []string{"nlu", "metodo", "shell"} {
		for i := 0; i < 8; i++ {
			exs = append(exs, Example{
				ID:    fmt.Sprintf("%s-%02d", g, i),
				Input: "entrada " + g,
				Want:  "saida " + g,
				Group: g,
			})
		}
	}
	return exs
}

// fgOutSet devolve os IDs que o split manda para o held-out com os mesmos
// parâmetros do portão — o mock usa isso para saber qual conjunto recebeu.
func fgOutSet(exs []Example, frac float64, seed int64) map[string]bool {
	_, out := Split(exs, frac, seed)
	m := make(map[string]bool, len(out))
	for _, e := range out {
		m[e.ID] = true
	}
	return m
}

// fgEval é um EvalFn mock: devolve inM para o held-in e outM para o
// held-out. Como o portão sempre avalia conjuntos puros, basta achar um
// ID do held-out para saber qual métrica retornar. N sai preenchido.
func fgEval(outIDs map[string]bool, inM, outM Metrics) EvalFn {
	return func(ex []Example) Metrics {
		m := inM
		for _, e := range ex {
			if outIDs[e.ID] {
				m = outM
				break
			}
		}
		m.N = len(ex)
		return m
	}
}

// O split é determinístico (mesma seed => mesmos conjuntos, na mesma
// ordem), particiona o conjunto todo sem sobreposição e é estratificado:
// cada grupo contribui com round(n*frac) exemplos para o held-out.
func TestSplitDeterministic(t *testing.T) {
	exs := fgExamples()
	in1, out1 := Split(exs, 0.25, 42)
	in2, out2 := Split(exs, 0.25, 42)
	if len(in1) != len(in2) || len(out1) != len(out2) {
		t.Fatalf("mesma seed => tamanhos diferentes: in %d vs %d, out %d vs %d",
			len(in1), len(in2), len(out1), len(out2))
	}
	for i := range in1 {
		if in1[i].ID != in2[i].ID {
			t.Fatalf("mesma seed => held-in diverge na posição %d: %q vs %q", i, in1[i].ID, in2[i].ID)
		}
	}
	for i := range out1 {
		if out1[i].ID != out2[i].ID {
			t.Fatalf("mesma seed => held-out diverge na posição %d: %q vs %q", i, out1[i].ID, out2[i].ID)
		}
	}

	// Partição completa e disjunta.
	seen := map[string]int{}
	for _, e := range in1 {
		seen[e.ID]++
	}
	for _, e := range out1 {
		seen[e.ID]++
	}
	if len(seen) != len(exs) {
		t.Fatalf("split não é partição: %d exemplos, %d IDs únicos em in+out", len(exs), len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("exemplo %q aparece %d vezes em in+out", id, n)
		}
	}

	// Estratificação: 0.25 de cada grupo de 8 => exatamente 2 por grupo.
	outByGroup := map[string]int{}
	for _, e := range out1 {
		outByGroup[e.Group]++
	}
	for _, g := range []string{"nlu", "metodo", "shell"} {
		if outByGroup[g] != 2 {
			t.Errorf("grupo %q com %d exemplos no held-out, esperava 2", g, outByGroup[g])
		}
	}

	// Outra seed => outra atribuição (o conjunto held-out muda).
	out3 := fgOutSet(exs, 0.25, 7)
	same := len(out3) == len(out1)
	for _, e := range out1 {
		if !out3[e.ID] {
			same = false
		}
	}
	if same {
		t.Errorf("seeds 42 e 7 produziram o mesmo held-out: %#v", out3)
	}
}

// Candidato que não regride no held-in e melhora o held-out (accuracy e
// coverage), com false_success = 0, é promovido.
func TestGateAcceptsImprovement(t *testing.T) {
	exs := fgExamples()
	outIDs := fgOutSet(exs, 0.25, 42)
	base := fgEval(outIDs,
		Metrics{Accuracy: 0.80, Coverage: 0.70},
		Metrics{Accuracy: 0.80, Coverage: 0.70})
	cand := fgEval(outIDs,
		Metrics{Accuracy: 0.82, Coverage: 0.70},
		Metrics{Accuracy: 0.90, Coverage: 0.75})
	ok, rep := Gate(exs, base, cand, 0.25, 42)
	if !ok || !rep.Accept {
		t.Fatalf("melhoria limpa rejeitada: %q", rep.Reason)
	}
	if rep.CandOut.Accuracy != 0.90 || rep.CandOut.N != 6 || rep.CandIn.N != 18 {
		t.Errorf("relatório com métricas erradas: %+v", rep)
	}
}

// false_success > 0 bloqueia a promoção mesmo quando o candidato melhora
// a accuracy do held-out — é a regra que barra asset envenenado.
func TestGateRejectsFalseSuccess(t *testing.T) {
	exs := fgExamples()
	outIDs := fgOutSet(exs, 0.25, 42)
	base := fgEval(outIDs,
		Metrics{Accuracy: 0.80, Coverage: 0.70},
		Metrics{Accuracy: 0.80, Coverage: 0.70})
	cand := fgEval(outIDs,
		Metrics{Accuracy: 0.95, Coverage: 0.90},
		Metrics{Accuracy: 0.99, FalseSuccess: 1, Coverage: 0.95})
	ok, rep := Gate(exs, base, cand, 0.25, 42)
	if ok || rep.Accept {
		t.Fatalf("false_success=1 promovido: %+v", rep)
	}
	if !strings.Contains(rep.Reason, "false_success") {
		t.Errorf("motivo não cita false_success: %q", rep.Reason)
	}
}

// Queda de accuracy no held-out além do eps rejeita, ainda que o held-in
// melhore — o portão não aceita overfitting no conjunto de desenvolvimento.
func TestGateRejectsRegression(t *testing.T) {
	exs := fgExamples()
	outIDs := fgOutSet(exs, 0.25, 42)
	base := fgEval(outIDs,
		Metrics{Accuracy: 0.80, Coverage: 0.70},
		Metrics{Accuracy: 0.80, Coverage: 0.70})
	cand := fgEval(outIDs,
		Metrics{Accuracy: 0.88, Coverage: 0.80},
		Metrics{Accuracy: 0.70, Coverage: 0.80})
	ok, rep := Gate(exs, base, cand, 0.25, 42)
	if ok || rep.Accept {
		t.Fatalf("regressão held-out promovida: %+v", rep)
	}
	if !strings.Contains(rep.Reason, "held-out") {
		t.Errorf("motivo não cita regressão no held-out: %q", rep.Reason)
	}
}

// Empate total (mesma accuracy, mesma coverage, sem false_success)
// rejeita: a regra exige ao menos uma métrica do held-out melhorando —
// empatar não justifica promover um asset novo.
func TestGateRejectsNoImprovement(t *testing.T) {
	exs := fgExamples()
	outIDs := fgOutSet(exs, 0.25, 42)
	m := Metrics{Accuracy: 0.80, Coverage: 0.70}
	base := fgEval(outIDs, m, m)
	cand := fgEval(outIDs, m, m)
	ok, rep := Gate(exs, base, cand, 0.25, 42)
	if ok || rep.Accept {
		t.Fatalf("empate total promovido: %+v", rep)
	}
	if !strings.Contains(rep.Reason, "sem melhoria") {
		t.Errorf("motivo não cita ausência de melhoria: %q", rep.Reason)
	}
}
