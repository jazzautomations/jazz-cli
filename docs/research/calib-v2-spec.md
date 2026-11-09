# calib-v2 — especificação recuperada do transcript

Fonte: prompt do worker no transcript Claude (b740eaae) → Codex em /tmp/jr-cal.

> "make the offline brain's confidence a real probability, and make the
> act/ask/refuse decision a principled risk-aware policy — without breaking
> any veto."

## 1. Corpus de calibração determinístico e representativo

- exemplos held-out da gramática com separação K-fold
- negativos de todo intent
- corpus gate não-task como hard negatives (`porra`, `responde só: ok`, `oi`)
- perturbações geradas: word drops, synonym swaps de `nlu/lexicon.json`,
  filler insertion, typos, cross-intent splices
- seed fixo (42), 5 folds (FNV hash por texto)
- miss rate realista — não um corpus artificialmente fácil a 5%
- particionamento 60% fit / 20% seleção de método / 20% teste intocado,
  agrupado por texto-base para impedir leakage de derivados

## 2. Calibração por regime de evidência

Regimes: `agree`, `rule_only`, `tie`, `classifier_only`, `typo_only`, `gate`.
Por regime escolhe identity / Platt / isotonic pelo ECE em held-out.
Restrições duras:

- saída sempre bounded e monotônica
- NUNCA mapear raw < nuMinConf para >= nuMinConf (guard `p < brLevel0Min → q=min(p,q)`)
- pode baixar confiança livremente
- promoção para cima só dentro da banda act
- vetos de recognition e typo continuam gates independentes de segurança

## 3. Política Chow (act / ask / honest-miss)

Custos explícitos em código (unidades de "uma pergunta"):
- custo da pergunta = 3
- erro em leitura verificável = 10 → threshold `1 − 3/10 = 0.70`
- erro em mutação/interpretação = 20 → threshold `1 − 3/20 = 0.85`
- recognition floor 0.60 inalterado (veto duro; abaixo disso = miss honesto)
- `brRiskActMin(in, risk)`: risk > cbLow → 0.85, senão `brActMin(in)` (read-only 0.70)

## 4. Re-ligar por default SÓ se todos os gates passarem

- ECE held-out melhora
- nenhum teste de veto/negativo regride
- eval offline com zero falso-sucesso
- perguntas feitas ≤ necessário
- senão: permanece opt-in e documenta os números

## 5. Testes para cada garantia + 6. regenerar asset

`JAZZ_UPDATE_CALIB=1 go test -run 'TestNLUCalibAsset$'`

## 7. Verificação low-memory

`CGO_ENABLED=0 GOMAXPROCS=2 GOGC=50`; `gofmt -l .` vazio; `go vet .`;
`GOOS=windows go vet ./...`; suite filtrada
`go test -p=1 -count=1 -run 'NLU|Calib|Brain|IntentConfirm|Offers|EvalOffline|Script' .`;
`go build -o /tmp/jz-cal .` + `/tmp/jz-cal eval offline` (all tasks, false success 0).

## 8. Commit

identidade configurada, mensagem em português, trailer
`Co-Authored-By: Devin (swe-2)` (continuation) — sem push.
