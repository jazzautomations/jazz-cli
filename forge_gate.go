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
	"crypto/hmac"
	"crypto/sha256"
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
	Accuracy     float64 `json:"accuracy"`      // fração de acertos [0,1]
	FalseSuccess int     `json:"false_success"` // respostas marcadas como sucesso sem serem — bloqueante
	Coverage     float64 `json:"coverage"`      // fração de entradas cobertas pela resposta [0,1]
	N            int     `json:"n"`             // tamanho do conjunto avaliado
}

// EvalFn avalia um conjunto de exemplos e devolve as métricas agregadas.
// Injetada no portão: quem chama sabe rodar o sistema real (NLU, método,
// harness); o portão só compara números.
type EvalFn func(ex []Example) Metrics

// GateReport é o resultado auditável do portão: a decisão, o motivo
// legível e as quatro medições (baseline × candidato, held-in × held-out).
type GateReport struct {
	Accept  bool    `json:"accept"`
	Reason  string  `json:"reason"`
	BaseIn  Metrics `json:"base_in"`
	BaseOut Metrics `json:"base_out"`
	CandIn  Metrics `json:"cand_in"`
	CandOut Metrics `json:"cand_out"`
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
	return splitBy(examples, heldOutFrac, func(id string) uint64 { return splitHash(seed, id) })
}

// SplitKeyed is Split with a secret key in place of the public seed: the
// order inside each group comes from HMAC-SHA256(key, ID), so held-out
// membership is stable per install but unknowable from the (public) repo —
// a poisoner cannot craft corrections that always land held-in and skip the
// out-of-sample test (spec 0.14b item 8). The key lives at
// dataDir()/forge/split.key (forge.go), never in the ledger nor the overlay.
func SplitKeyed(examples []Example, heldOutFrac float64, key []byte) (in, out []Example) {
	return splitBy(examples, heldOutFrac, func(id string) uint64 {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(id))
		return binary.LittleEndian.Uint64(h.Sum(nil))
	})
}

func splitBy(examples []Example, heldOutFrac float64, hash func(id string) uint64) (in, out []Example) {
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
			return cmp.Compare(hash(examples[a].ID), hash(examples[b].ID))
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
//  2. BLOQUEANTE — o candidato não pode criar sucesso falso: em cada split
//     candidate.FalseSuccess <= baseline.FalseSuccess. Um falso sucesso que
//     a gramática embarcada já comete não é culpa do candidato (é bug dela,
//     pra consertar em release); um falso sucesso NOVO é sinal de asset
//     envenenado e rejeita sempre, mesmo com accuracy maior.
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
	gateDecide(&rep, len(in), len(out), true)
	return rep.Accept, rep
}

// NonInferior is Gate without condition 4: the four measurements are given
// (the caller scores the baseline once and many candidates against it), and
// the benefit is the caller's to prove — the asset-forge proves it with a
// directed test on the corrected phrases, which never score on the held-out
// split for themselves (spec 0.14b item 5). nIn/nOut are the split sizes.
func NonInferior(baseIn, baseOut, candIn, candOut Metrics, nIn, nOut int) GateReport {
	rep := GateReport{BaseIn: baseIn, BaseOut: baseOut, CandIn: candIn, CandOut: candOut}
	gateDecide(&rep, nIn, nOut, false)
	return rep
}

// gateDecide applies conditions 1–3 (and 4 when needGain) to rep.
func gateDecide(rep *GateReport, nIn, nOut int, needGain bool) {
	switch {
	case nIn == 0 || nOut == 0:
		rep.Reason = fmt.Sprintf("rejeitado: split degenerado (held-in=%d, held-out=%d)", nIn, nOut)
	case rep.CandIn.FalseSuccess > rep.BaseIn.FalseSuccess || rep.CandOut.FalseSuccess > rep.BaseOut.FalseSuccess:
		rep.Reason = fmt.Sprintf("rejeitado: false_success novo (held-in %d->%d, held-out %d->%d)",
			rep.BaseIn.FalseSuccess, rep.CandIn.FalseSuccess, rep.BaseOut.FalseSuccess, rep.CandOut.FalseSuccess)
	case rep.CandIn.Accuracy < rep.BaseIn.Accuracy-gateEps:
		rep.Reason = fmt.Sprintf("rejeitado: accuracy held-in regrediu %.3f -> %.3f (eps=%.3f)",
			rep.BaseIn.Accuracy, rep.CandIn.Accuracy, gateEps)
	case rep.CandOut.Accuracy < rep.BaseOut.Accuracy-gateEps:
		rep.Reason = fmt.Sprintf("rejeitado: accuracy held-out regrediu %.3f -> %.3f (eps=%.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, gateEps)
	case needGain && rep.CandOut.Accuracy <= rep.BaseOut.Accuracy && rep.CandOut.Coverage <= rep.BaseOut.Coverage:
		rep.Reason = fmt.Sprintf("rejeitado: sem melhoria no held-out (acc %.3f -> %.3f, cov %.3f -> %.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, rep.BaseOut.Coverage, rep.CandOut.Coverage)
	case needGain:
		rep.Accept = true
		rep.Reason = fmt.Sprintf("aceito: held-in/held-out sem regressão e held-out melhorou (acc %.3f -> %.3f, cov %.3f -> %.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, rep.BaseOut.Coverage, rep.CandOut.Coverage)
	default:
		rep.Accept = true
		rep.Reason = fmt.Sprintf("sem regressão held-in/held-out (acc held-out %.3f -> %.3f, cov %.3f -> %.3f)",
			rep.BaseOut.Accuracy, rep.CandOut.Accuracy, rep.BaseOut.Coverage, rep.CandOut.Coverage)
	}
}

// McNemarExact is the one-sided exact McNemar p-value that the candidate is
// WORSE: b = cases the baseline got right and the candidate wrong, c = the
// reverse. p = P(X ≥ b), X ~ Binomial(b+c, 1/2); no discordant pair = 1.
func McNemarExact(b, c int) float64 {
	n := b + c
	if n == 0 || b == 0 {
		return 1
	}
	p, term := 0.0, math.Pow(0.5, float64(n)) // C(n,0)/2^n
	for k := 0; k <= n; k++ {
		if k > 0 {
			term = term * float64(n-k+1) / float64(k)
		}
		if k >= b {
			p += term
		}
	}
	return min(p, 1)
}
