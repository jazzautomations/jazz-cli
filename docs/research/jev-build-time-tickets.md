# Jev build-time → tickets internos (2025-01)

Juiz (TypeSafe Jev `jev-1.14.0`, `noul`) como oráculo de **build-time** em
`/tmp/v5-work` — nunca no produto. O que ele pontuou no corpus v20 (1711
records): 41 `FALSE_SUCCESS`, 488 `MISSED_refusal`, 461 `BUG_doable`.
Resultado dos fixes: `058969f` (enum/fan-out/nonempty/metachar) +
`aa9d4b2` (multi-rollup + conf=p em empate) — dos 41, ~38 agora honestos.

## Fechados

1. **Fan-out parcial** — "dirs X e Y" criava só o 1º e selava verde.
   `targets`/`enum`/`create.dirs` + `mkdir_in`/`create.each_dir` +
   verify `each_clean`+`list_exists`: sucesso = TODOS os itens.
2. **Vacuous zero-match** — `nonempty` + `brHasShellOp`: "0 match(es)"
   e padrão com metacaractere viram `unverified`.
3. **Multi-step herda verde** — needs_input/failed/blocked quebra a
   cadeia; evento-pai carrega o pior status.
4. **Empate infla confiança** — `conf=p`: ambiguidade vira menu.
5. **Fallback pra verde errado** — `scope`/`targets` set exclui métodos
   single-target (sem `mkdir` solto depois de fan-out falhar).

## Abertos

### T1 — verify consciente-do-objetivo (nb-0062)
Recover replaneja pra método mais fraco (write bloqueado → read) e o
verify prova só o read. O objetivo "save to output.file" morre sem rastro.
Fix: o replan deve carregar quais sub-objetivos foram DROPADOS; veredito
final ≤ unverified quando um deles não tem prova. Classe profunda —
precisa de representação de sub-objetivos no task state.

### T2 — bandas de risco → τ por classe
Hoje 2 bandas (read 0.7 / resto 0.85). Jev labels permitem medir custo
real por classe: read / reversível-mutation (create, rename c/ undo) /
irreversível (delete massa, perm -R). τ derivado de custo observado,
não constante. Insumo: `results.v20.jev.jsonl` + outcomes por intent.

### T3 — refit `nlu/calib.json` com labels Jev
Tabela isotônica atual é priori (~644 amostras de laboratório). Labels
Jev sobre tráfego real (req + outcome + status) re-ajustam P(parse certo).
Pipeline determinístico: score → split treino/heldout → refit → gate de
não-inferioridade → embed. NUNCA online.

### T4 — tripwires no produto
`tripwires.py` hoje é harness-only. Portar pra dentro: pós-run, antes do
selo — `succeeded` sem evidência concreta → unverified (já estrutural);
"resposta ecoa `» step` sem conteúdo" → unverified; producer truncado →
já coberto. O resto é auditoria de harness, fica fora.

### T5 — conf-cliff 0.45–0.59 (454 refusals)
Jev `doable` diz que a maioria era fazível = parser rastejando pra
debaixo do gate, não recusa honesta. Cada banda de conf precisa de causa
nomeada: "parse fraco" ≠ "pedido sem suporte". O menu honesto já existe
— a métrica é quantos entram por menu vs quantos resolvem por NLU.

### T6 — ensemble Jev × juiz local
Julia-1 (144M, 73.15% vs 72.70%) precisa de backend sem AVX2 — Ivy
Bridge só tem AVX1. Ollaya trouxe `libggml-cpu-ivybridge.so` → GGUF
decoder (kev:0.8b) é o caminho. Quando os dois pontuarem os mesmos
registros, o dissenso vira fila de inspeção — nunca voto.

### T7 — d610 real
i686 provado em emu; falta hardware real pra validar o binário +
latência do cérebro offline em Pentium M.

## Invariantes (não-negociáveis)

- Juiz pontua DEPOIS do gate determinístico, nunca dentro dele.
- Dissenso = dado de revisão, nunca veto nem autorização.
- `succeeded` exige verify + evidência cobrindo TODOS os operandos.
- Produtor vazio/truncado não alimenta mutação.
- Nada de Jev/API no binário — ele destila tabela/regra/seed e morre.

## Re-score v21 (redo1051 — MISSED_refusal + BUG_doable)

Mesmos 1051 casos, binário novo, Jev nos mesmos labels:

| label | v20 | v21 |
|---|---|---|
| BUG_doable | 819 | 810 |
| honest_refusal | 232 | 238 |
| FALSE_SUCCESS | 0 | 3 |

Os 3 FALSE_SUCCESS dissecados: `nb-0714`/`nb-0746` são artefato de
timeout do runner (exit 124 — produto selou succeeded, morreu no
wrap-up, a Warn de deadline vazou como "answer"). `nb-1920` é gap
real: `net_ifaces` parseou `slots={}` — `pattern "venet0:0|eth0"` e
`first` nunca viraram slot → `brSlotIgnored` não pode ver operando que
não existe. Nova classe: **operando nunca extraído** — residência em
`unfed`/residual devia derrubar o selo quando uma string citada do
pedido não alimentou nenhum slot.

### T8 — operando citado sem slot (nb-1920)
Pedido com `"pattern X"` / `first` que nenhum slot consome → hoje
verde com listagem não filtrada. Dois remendos possíveis: (a) slot
`pattern`+`first` em `net_ifaces` com filtro real; (b) tripwire de
"quoted operand não consumido" — genérico, cobre a classe. O (b) é o
aprendizado estrutural; (a) é cobertura caso-a-caso.

### T9 — deadline ≠ succeeded no events stream [fechado]
`nb-0714`/`nb-0746`: exit 124 com último evento `to:succeeded` —
a task selou e o deadline bateu no wrap-up (sem `deadline.soft`
no stream). Fix no harness (redo-1051.py + runner3.py): evento
`deadline.soft` depois de succeeded → `interrupted`; `exit≠0` com
status succeeded → `unverified`. Estado final dos 3 FALSE_SUCCESS
do v21: 2 viraram unverified por exit-cap, 1 por Dropped (T8) —
**zero falso-sucesso medido nos 1051**.
