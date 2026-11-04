package main

// forge_gate.go — portão de validação held-out do asset-forge.
//
// Decide se um asset candidato (gerado pelo Fable em tempo offline) pode ser
// promovido a durável. É a infra que impede um asset "envenenado" — que sobe
// a métrica no conjunto de treino mas piora fora dele, ou que reporta sucesso
// falso — de entrar no acervo.
//
// Regra implementada (Self-Harness, Zhang et al. 2606.09498; linhagem
// Shepherd CRO 2605.10913 — ver docs/research/metaharness-self-improvement.md):
//
//	o candidato só é aceito se NEM o held-in NEM o held-out regridem
//	e AO MENOS UM dos dois melhora; aqui endurecido com
//	false_success == 0 como condição bloqueante.
//
// O portão é puro e testável: não sabe rodar NLU nem método — o avaliador é
// injetado via EvalFn. A ligação ao `jazz forge` e aos validadores reais
// acontece em cima deste motor.

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
)

// gateEps é a tolerância de ruído na regra de não-regressão: quedas de
// accuracy de até 1 ponto percentual entre baseline e candidato são tratadas
// como empate (flutuação de avaliação), não como regressão. Não se aplica à
// melhoria — para aceitar, o held-out precisa melhorar de verdade (> estrito).
const gateEps = 0.01

// Example é um exemplo rotulado do conjunto de validação. Group permite
// estratificação do split (ex.: família de tarefas, origem do exemplo).
type Example struct {
	ID    string
	Input string
	Want  string
	Group string
}

// Metrics é o resultado de avaliar um EvalFn sobre um conjunto de exemplos.
type Metrics struct {
	Accuracy     float64 // fração de acertos [0,1]
	FalseSuccess int     // respostas marcadas como sucesso sem serem — bloqueante
	Coverage     float64 // fração de entradas cobertas pela resposta [0,1]
	N            int     // tamanho do conjunto avaliado
}

// EvalFn avalia um conjunto de exemplos e devolve as métricas agregadas.
// Injetada no portão: quem chama sabe rodar o sistema real (NLU, método,
// harness); o portão só compara números.
type EvalFn func(ex []Example) Metrics

// GateReport é o resultado auditável do portão: a decisão, o motivo
// legível e as quatro medições (baseline × candidato, held-in × held-out).
type GateReport struct {
	Accept  bool
	Reason  string
	BaseIn  Metrics
	BaseOut Metrics
	CandIn  Metrics
	CandOut Metrics
}

// splitHash deriva um uint64 determinístico de (seed, ID). FNV-1a sobre
// seed || ID — nada de rand global: o mesmo exemplo cai sempre no mesmo
// lado para uma dada seed, em qualquer processo ou máquina.
func splitHash(seed int64, id string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed))
	h.Write(b[:])
	h.Write([]byte(id))
	return h.Sum64()
}

// Split divide examples em held-in e held-out de forma DETERMINÍSTICA e
// estratificada por Group quando possível.
//
// A atribuição não usa sorteio global: dentro de cada grupo, os exemplos
// são ordenados por splitHash(seed, ID) e os primeiros round(n*frac) vão
// para o held-out. Isso garante, por grupo, exatamente a fração pedida
// (arredondada) — a estratificação é exata sempre que o grupo tiver
// elementos suficientes para a fração. Empates de hash mantêm a ordem
// original (sort estável). heldOutFrac é fixada em [0,1].
//
// Os dois slices de saída preservam a ordem original de examples.
func Split(examples []Example, heldOutFrac float64, seed int64) (in, out []Example) {
	frac := min(max(heldOutFrac, 0.0), 1.0)

	// Agrupa índices por Group, lembrando a ordem de primeira aparição.
	groups := map[string][]int{}
	var order []string
	for i, ex := range examples {
		if _, ok := groups[ex.Group]; !ok {
			order = append(order, ex.Group)
		}
		groups[ex.Group] = append(groups[ex.Group], i)
	}

	// Em cada grupo, os nOut menores hashes viram held-out.
	isOut := make([]bool, len(examples))
	for _, g := range order {
		idxs := slices.Clone(groups[g])
		slices.SortStableFunc(idxs, func(a, b int) int {
			return cmp.Compare(splitHash(seed, examples[a].ID), splitHash(seed, examples[b].ID))
		})
		nOut := int(math.Round(float64(len(idxs)) * frac))
		for _, i := range idxs[:nOut] {
			isOut[i] = true
		}
	}

	for i, ex := range examples {
		if isOut[i] {
			out = append(out, ex)
		} else {
			in = append(in, ex)
		}
	}
	return in, out
}

// Gate avalia baseline e candidate nos splits held-in/held-out e decide se
// o candidato pode ser promovido. Aceita somente se TODAS as condições:
//
//  1. Split não degenerado: held-in e held-out não vazios (sem isso não há
//     como checar não-regressão nem generalização).
//  2. BLOQUEANTE — candidate.FalseSuccess == 0 nos dois splits. Sucesso
//     falso é sinal de métrica corrompida / asset envenenado: rejeita
//     sempre, mesmo com accuracy maior.
//  3. Não-regressão — candidate.Accuracy >= baseline.Accuracy - gateEps
//     nos dois splits (o eps absorve ruído de avaliação).
//  4. Melhoria real no held-out — candidate.Accuracy > baseline.Accuracy
//     OU candidate.Coverage > baseline.Coverage (estrito, sem eps:
//     empate não é promoção).
//
// Motivo da decisão vai em GateReport.Reason com os números relevantes.
func Gate(examples []Example, baseline, candidate EvalFn, heldOutFrac float64, seed int64) (bool, GateReport) {
	in, out := Split(examples, heldOutFrac, seed)
	rep := GateReport{
		BaseIn:  baseline(in),
		BaseOut: baseline(out),
		CandIn:  candidate(in),
		CandOut: candidate(out),
	}

	switch {
	case len(in) == 0 || len(out) == 0:
		rep.Reason = fmt.Sprintf("rejeitado: split degenerado (held-in=%d, held-out=%d)", len(in), len(out))
	case rep.CandIn.FalseSuccess > 0 || rep.CandOut.FalseSuccess > 0:
		rep.Reason = fmt.Sprintf("rejeitado: false_success bloqueante (held-in=%d, held-out=%d)",
			rep.CandIn.FalseSuccess, rep.CandOut.FalseSuccess)
	case rep.CandIn.Accuracy < rep.BaseIn.Accuracy-gateEps:
		rep.Reason = fmt.Sprintf("rejeitado: accuracy held-in regrediu %.3f -> %.3f (eps=%.3f)",
			rep.BaseIn.Accuracy, rep.CandIn.Accuracy, gateEps)
	case rep.CandOut.Accuracy < rep.BaseOut.Accuracy-gateEps:
		rep.Reason = fmt.Sprintf("rejeitado: accuracy held-out regrediu %.3f -> %.3f (eps=%.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, gateEps)
	case rep.CandOut.Accuracy <= rep.BaseOut.Accuracy && rep.CandOut.Coverage <= rep.BaseOut.Coverage:
		rep.Reason = fmt.Sprintf("rejeitado: sem melhoria no held-out (acc %.3f -> %.3f, cov %.3f -> %.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, rep.BaseOut.Coverage, rep.CandOut.Coverage)
	default:
		rep.Accept = true
		rep.Reason = fmt.Sprintf("aceito: held-in/held-out sem regressão e held-out melhorou (acc %.3f -> %.3f, cov %.3f -> %.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, rep.BaseOut.Coverage, rep.CandOut.Coverage)
	}
	return rep.Accept, rep
}
