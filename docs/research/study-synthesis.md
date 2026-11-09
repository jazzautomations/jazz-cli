# Síntese dos 43 docs de pesquisa vs código atual (v0.13.x)

**Método:** leitura dos 43 arquivos de `docs/research/` cruzada com o código em `main`.
Status verificado por grep/leitura, nunca assumido. Vários docs datam de ~04/11/2026 e
descrevem como "só pesquisado" coisas que a 0.12/0.13 já entregou.

## 1. Matriz de status por documento

| # | Doc | O que propôs | Status | Evidência |
|---|-----|--------------|--------|-----------|
| 1 | offline-brain-research | Arquitetura clássica (HTN-lite, plan-execute-replan, blackboard, NLU semântica, índices) | **adotado** | brain.go, methods.go, task.go, recover.go, nameindex.go, nlu.go |
| 2 | offline-on-device-intelligence | Confiança calibrada, info-state dialogue, CBR, scripts-como-programa, forge em compile-time | **parcial→adotado** | jazz forge (forge.go:132), signals.go:59, nlu_calib.go:34 |
| 3 | cardapio-offline | Biblioteca de scripts determinísticos com schema/efeito/verificação | **adotado** | 69 scripts jazz-scripts/; v5 ligado em nlu.go/methods/*.json |
| 4 | coverage-v5 | Corpus 372; meta-fixes: slot nunca aceita palavra inexplicada; verbo de mutação veta intent só-leitura | **adotado** | falsos-match 70→13, BUG_doable drenado; nlu_slotguard_test.go |
| 5 | plan-reuse-deep | IR tipado de plano + screen estático + replay offline + forja + gate held-out | **não adotado** | piPlan/psScreen/prReplay/mfCompile/pgGate inexistem; pcMatch só no caminho online; brain offline nunca consulta plancache |
| 6 | tocomas-plan-reuse | Screens estruturais e cache verificado do TOCOMAS | **parcial** | plancache BM25 existe (plancache.go:277); screens/replay ausentes |
| 7 | skill-induction-deep | cases.jsonl + indução de método/macro + screen + held-out + promote | **parcial** | overlay de métodos existe (distill.go:110); cases.jsonl/indução/macros ausentes |
| 8 | program-synthesis-library-learning | A–H: overlays, forge DreamCoder, compressão Sequitur, Snuba-lite, PBE-lite, LatPlan-lite, gate, report | **parcial** | A (overlays), B (jazz forge), G (held-out forge_gate.go) adotados; C–F, H ausentes |
| 9 | memory-retrieval-deep | memstore.go append-only+tombstones, BM25, remember/forget/recall | **não adotado** | nenhum memstore/mem_* no código |
| 10 | metaharness-self-improvement | harness.json declarativo, propostas restritas a knobs seguros, holdout oculto | **não adotado** | sem harness.json editável |
| 11 | decision-policy-calibrated | Política Chow custo-sensível: posterior calibrado × bandas de risco → act/ask/menu | **parcial** | brActMin (brain.go:1360, 2 bandas), nlu/calib.json opt-in; bandas por classe/top-k conformal ausentes |
| 12 | math-offline-brain | ECE/Platt/isotônico, suavização KT, IG/χ², resolver probabilístico, instrumentação | **parcial** | calib isotônico adotado (opt-in); IG/χ²/resolver/instrumentação ausentes |
| 13 | unifying-math-offline | Teste adversarial da tese entropia/MDL/xadrez; veredito: Chow no argmax | **parcial** | calib.go agora consumido via nlu_calib.go |
| 14 | negentropy-as-objective | Active inference decomposto: só EVSI (valor da informação de perguntar) é peça nova | **não adotado** | nenhum modelo p(resposta|intenção)/EVSI |
| 15 | thermodynamics-of-the-harness | Lei N* (reusos que pagam a forja) + tarefas-verificadas/token | **parcial** | ledger signals.jsonl existe; N*/custo por tarefa não computados |
| 16 | semantics-of-computation | Contratos semânticos {Pre}método{Verify}; ligar Effects + type-checker da DSL | **parcial** | Capability.Effects existe (caps.go) pouco lido; backchain.go sem chamador de produção |
| 17 | verification-oracles-deep | Oráculos P1–P10: DSL de verify, metamórficos, kill-rate, gates ocultos | **parcial** | verify+evidência estrutural; kill-rate/metamórfico/DSL ausentes |
| 18 | validating-open-ended-assets | Arbitragem entre assets, gates por tipo, kill-rate | **parcial** | forge_gate.go valida exemplos NLU; validação de métodos abertos ausente |
| 19 | terminal-swe-techniques-deep | localize arquivo→elemento→linha, traceback ranking, co-mudança, churn, fix.mechanical, pré-voo | **parcial** | mechrepair.go existe; git_cochange/git_blame são scripts; pipeline localize e pré-voo ausentes |
| 20 | harness-sota-gap | Localize, reparo mecânico, jazz stats, resume cross-mode, NLU mining | **parcial** | mechrepair.go sim; localize/cmdStats/task-resume/nlu mine ausentes |
| 21 | harness-first-offline | Tese harness-primeiro | **adotado** | todo o cérebro offline |
| 22 | asset-scale-coverage | Cobertura por escala de assets | **adotado** | cardápio v1→v5, intents ligados |
| 23 | grep-is-all-you-need | Lexical vence vetorial; prefetch; inline | **adotado** | BM25 puro, nameindex, zero embeddings; prefetch.go (default off) |
| 24 | fusion-architectures | Fusão/hedging de modelos | **adotado** | judge.go, judge_hedge.go, judge_fusion.go |
| 25 | omni-judge | Juiz UCB1 por (modelo,tipo) + roles + hedging + painel | **adotado** | judge*.go, route_task.go, model-scores.json |
| 26 | benchmarks | Panorama de benchmarks | n/a informativo | — |
| 27 | bob-kiro-devin | Steering, smart mode, --until, revisão | **adotado** | smart.go, steering.go, --until/--review |
| 28 | pi-sessions | Árvore de sessão JSONL estilo pi | **adotado** | session.go, /tree, /fork, sessReplay |
| 29 | shepherd-meta-agents | Handoff/discard determinístico em stall + hindsight hints | **parcial** | agent_recover.go (acHandoff-8); hints por verificar completude |
| 30 | skills-personas-scripts | 12 skills embutidas + ponytail budget + cardápio | **parcial** | skills via prompt.go; budget.go/bgClassify existe; enforcement fino por verificar |
| 31 | skill-bodies | Corpos prontos das skills | **adotado** | skills no embed |
| 32 | impeccable-frontend-craft | Skill frontend-craft + hook ui_lint | **parcial** | skills existem; ui_lint hook por confirmar |
| 33 | ux-offline-parity | P1–P12 UX + 8 propostas | **parcial** | miss top-k adotado (brain.go:699-802, in.Alt + olMenu); menu de botões; "conferi"/chip/preview por verificar |
| 34 | ux-intent-confirmation | Matriz confiança×risco, miss→menu top-k, needs_input persistente, campo Confirmed | **parcial** | in.Alt alimenta menu; typo-confirm existe; Confirmed/persistência por confirmar |
| 35 | ux-offline-flow-continuity | Ofertas acionáveis, preview de mutação, anáfora, needs_input persistente, progresso, replay, undo/redo | **parcial** | anáfora (brStripAnaphora), needs_input (task.go), offers.go; preview/progresso/redo ausentes ou parciais |
| 36 | security-gap-audit | Catálogo OWASP/ATLAS: 10 faltantes rankeados | **parcial** | sandbox existe; two-tier injection (v0.13.2); egress allowlist, postinstall --ignore-scripts, cost cap por verificar |
| 37 | security-review-oss | Review pré-release F1–F11 | **parcial** | repo público, two-tier injection, .gitleaks.toml; restos por confirmar |
| 38 | redteam-results | Suíte redteam 14 cenários + correções block-ux | **adotado** | jazz-redteam/, 14/14 BLOCKED |
| 39 | cross-arch-determinism-asset-security | P1 ConfQ, P2 nb.bin det., P3 calib quantizada, P4 assinatura de asset | **parcial** | calib asset existe; ConfQ/nb.bin/assinatura por verificar |
| 40 | deepen-the-winners | P1 ledger vivo, P2 eval --tokens, P3 eval calib, P4 brDecide, P5 top-k conformal, P6 forge eval, P7 forge report, P8 ddmin, P9 margem | **parcial** | P1 (signals.go), P4 (brActMin), P3 parcial; P2/P5–P9 ausentes |
| 41 | autonomy-control-theory | Autonomia como teoria de controle (MAPE-K, bandas) | **parcial** | laços existem; formalização é enquadre |
| 42 | information-geometry-coding | P1 NLL/tarefa, P2 portão MDL, P3 mdl.go, P4 ncd.go, P5 curva R-D, P6 quantização NB, P7 Kraft, P8 ordenação por frequência | **não adotado** | mdl.go/ncd.go inexistem; portão forge é held-out, não MDL |
| 43 | jev-build-time-tickets | Juiz Jev em build-time → tickets T1–T5 | **parcial/ativo** | BUG_doable drenado; T1–T4 abertos; **T5 = stream ativo** |

## 2. O que a pesquisa achava que faltava e já existe (honestidade)

- `jazz forge` completo: correções → exemplos com held-out gate (forge.go, forge_gate.go, nlu_overlay.go, alias.go).
- Ledger `signals.jsonl` (signals.go:59) com unknown_intent/miss/new_method/verifier_missing/correction.
- Calibração isotônica `nlu/calib.json` embutida (opt-in) — calib.go não está mais morto.
- Limiar por risco `brActMin` (brain.go:1360) — 2 bandas, Chow assimétrico.
- Miss honesto com top-k: `in.Alt` consumido no menu interativo (brain.go:699-802).
- Anáfora (brStripAnaphora, brain.go:187-272), needs_input no state machine (task.go), menu de botões, Robô Ed, env_probe, mechrepair.go, agent_recover.go (handoff Shepherd), judge*.go omni-judge completo.
- Scripts v5 todos ligados à NLU (file_find/perm_find/env_get/net_route com dezenas de regras em nlu.go).
- headless.go passa por sanitize — gap OSC52 do audit aparentemente fechado (confirmar).

## 3. Lista rankeada de melhorias (9 itens, todos verificados como ausentes)

| # | Ideia | O que é | Onde pluga | Impacto | Tam. | Medir primeiro? |
|---|-------|---------|-----------|---------|------|-----------------|
| 1 | **Bandas de risco por classe + τ medido (T2)** | brActMin hoje tem 2 bandas (0.7/0.85); derivar τ por classe (read/mutação-reversível/irreversível) dos custos observados nos labels Jev | brain.go:1360, methods.go (Risk), results.v20.jev.jsonl | Menos perguntas desnecessárias e menos risco em mutação — direto em under-refusal e false-block | **S–M** | Sim — labels já em disco |
| 2 | **Refit nlu/calib.json com tráfego real + ligar por padrão (T3)** | Tabela isotônica atual é priori de laboratório e opt-in; refit com labels Jev + gate de não-inferioridade + embed como default | nlu_calib.go, nlu/calib.json, pipeline JAZZ_UPDATE_CALIB | Confiança calibrada real → limiares significam algo; pré-requisito do #1 | **S–M** | Sim — trabalho de medição/pipeline |
| 3 | **`localize` determinístico** | Pipeline arquivo→elemento→linha: nameindex + traceback rank + git_cochange (script já existe!) + churn, com abstenção honesta | novo localize.go; insumos nameindex.go, outparse.go, gitindex.go | Maior alavanca para tarefas de código sem modelo; alimenta o 1º turno online | **M** | Sim — eval de localização antes |
| 4 | **`memstore.go` + intents remember/forget/recall** | Loja de memória unificada: append-only JSONL, tombstones, BM25/trigrama, proveniência wins/fails, compactação determinística | novo memstore.go (~400 ln), hooks em brRunIntent/acTurnDone, caps mem_* | Continuidade cross-sessão real no D610; lições hoje são prosa solta | **M–L** | Parcial — desenho fechado no doc |
| 5 | **IR tipado de plano + screen + replay offline** | piPlan/psScreen/prReplay: plancache hoje só injeta prosa online; replay verificado offline com fingerprint de estado, grade A/B/C | novo planir.go, plancache.go:479, brain.go | no_llm_rate sobe de verdade — reusa vitórias verificadas sem token | **L** | Sim — doc manda medir candidatos primeiro |
| 6 | **Preview de mutação + "conferi" + progresso** | Antes de mutação: plano de 3 linhas (o que muda, onde, como desfaz); depois: prova da verificação em linguagem humana; progresso por passo | brain.go (menu/brRunIntent), offers.go, task.go | UX D610: confiança sem ler JSON; reduz "fez o quê?" | **S–M** | Não — implementação direta |
| 7 | **Egress allowlist para bash** | curl/wget/nc/scp para host fora de net_allow → confirm (auto)/deny (untrusted); hoje só injScan/SecretReason barram exfil | safety.go (perto de DangerReason/SecretReason) | Fecha o último canal de exfil trivial em auto mode | **S–M** | Não — direto; não duplicar P0 consent |
| 8 | **`jazz forge report` / métricas de produção** | Agregar signals.jsonl → relatório de cobertura por classe, fila de misses, custo amortizado da forja (N*, no_llm, tokens/tarefa); opcional eval --tokens | forge.go (novo subcomando), signals.go, evaloffline.go | Transforma o ledger morto em fila de trabalho medida — pré-requisito de #4/#5 | **S–M** | Não — ledger já gravado; é agregação |
| 9 | **Checker estático de efeitos/contratos da DSL** | No load: método level-0 não pode ter efeito write/exec; método com mutação exige verifier; $result consumido deve ser produzido | methods.go load path, caps.go:74 | Pega bugs de asset em compile-time; barato e permanente | **S** | Não — direto |

**Excluídos por overlap com streams ativos:** T5 under-refusal por cluster, P0 consent/session-scope/sudo -S/denials pedagógicos, P2 tutoriais offline.
**Excluídos por já existir:** forge+gate, calib opt-in, brActMin 2-band, miss top-k, anáfora, mechrepair, handoff Shepherd, omni-judge, cardápio v5, signals ledger.
**Rejeitados pelos docs:** embeddings/vector store, LLM em runtime, modelos ≥3B, supervisor LLM por tick, replay de trajetória arbitrária, CCG aprendido em runtime, provadores/SMT.

## 4. Não verificado (confirmar antes de incluir)

- `jazz eval calib` como subcomando separado.
- `ui_lint` hook do impeccable e enforcement fino do ponytail budget.
- Itens restantes do security-gap-audit (postinstall, MCP rug-pull, cost cap, rot13).
- Persistência cross-processo de needs_input.
- ConfQ / formato nb.bin int-only (cross-arch P1/P2).
