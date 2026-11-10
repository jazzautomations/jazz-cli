# calib-v2 — notas de proveniência e gaps

## O que sobrevive / o que morreu

- **Commit `8b77265` (harness/calib-v2): PERDIDO.** Objeto não existe em
  nenhum repo local (`git show 8b77265` falha no clone principal e
  no clone de estudo; busca ampla em packs/objects nada achou).
  O worktree /tmp/jr-cal morreu no reboot.
- **O que NÃO se perdeu:** os commits de calibração ANTERIORES já estão em
  main (`dec7e6c`, `87b7ba3`, `0d1bbc9`, `6e70f63`) — incluindo `calib.go`,
  `nlu_calib.go` original, `nlu/calib.json`, `nlu_gate_test.go`, a doc
  `docs/research/decision-policy-calibrated.md`.
- **O que os transcripts salvaram:** os payloads completos dos tool-calls do
  worker Codex em /tmp/jr-cal (heredocs `cat > arquivo` + scripts python de
  edição), a especificação, o relatório final e os comandos de validação.

## Reconstrução em `src/`

Reconstruí o estado dos arquivos NO PONTO em que o worker Codex parou
(limite de uso ~00:35 Nov 5), aplicando sobre a base `d7fc0f2`
(cardápio-v4, o pai do trabalho v2):

- `src/nlu_calib.go` — runtime per-regime: asset embed `nlu/calib.json`,
  `nuCalibCurve`, `nuCalibAsset`, `nuCalibGet`, `nuCalibApply(raw, regime)`,
  Platt + isotonic, bounded, guard anti-promoção (`p < brLevel0Min → q=min(p,q)`)
- `src/nlu_calib_data_test.go` — gerador do corpus: FNV folds, seed 42,
  5 folds, gate corpus, geração (examples/drop/filler/synonym/typo/splice/
  negative/gate), negativos censurados, dedup global de superfícies
  geradas (`reserved`/`seen`/`labels`), RNG determinístico por exemplo,
  partition 60/20/20 por grupo, seleção identity/Platt/isotonic por ECE,
  métricas ECE/Brier/TestHits/MeanRaw/MeanCal, campo `Text` p/ checar
  vazamento entre partições
- `src/nlu_calib_test.go` — testes: monotonicidade, anti-promoção abaixo de
  nuMinConf, opt-in default, raw≠cal expostos, leak-check por texto
- `src/nlu.go.vs-d7fc0f2.diff` — `Intent.Regime`, labels por ramo
  (classifier_only/rule_only/agree/tie/typo_only/gate),
  `conf = nuCalibApply(in.Raw, in.Regime)`, remoção do rounding sobre
  fronteiras de decisão
- `src/brain.go.vs-d7fc0f2.diff` — custos explícitos (3/10/20),
  `brLevel0Min = 1 − 3/20 = 0.85`, `brReadActMin = 1 − 3/10 = 0.70`,
  `brRiskActMin(in, risk)`, comentários reescritos (Chow, não "P1 matrix")
- `src/calib.go.vs-d7fc0f2.diff` — PAV com equal scores como observação
  ponderada (remove dependência de ordem de labels em empates)
- `src/nlu_gate_test.go.vs-d7fc0f2.diff` — gate test usa `nuGateCorpus`

## Gaps (o que NÃO foi recuperado)

1. **Delta final do Devin continuation (~+281 linhas).** O worker Devin
   continuou do ponto do Codex, corrigiu mais coisas e comitou `8b77265`.
   Os comandos sobreviventes no transcript mostram edições incrementais mas
   NÃO o diff completo do commit. A reconstrução em `src/` é o estado
   Codex + correções identificáveis; o commit final pode ter ajustes a mais.
2. **`nlu/calib.json` regenerado.** O asset v2 final era produzido por
   `JAZZ_UPDATE_CALIB=1 go test -run 'TestNLUCalibAsset$'`. O JSON do
   transcript não sobreviveu; para regenerar é preciso compilar a branch
   reconstruída (os fontes do gerador estão em `src/`).
3. **Possíveis divergências de whitespace/comentário** entre a reconstrução
   e o commit real — o conteúdo funcional veio dos heredocs verbatim.

## Status de merge/push

Não foi encontrada evidência autoritativa de que `harness/calib-v2` foi
pushada ou mergeada. Não inferir merge/closure a partir dos testes.
