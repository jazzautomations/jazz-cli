# Inteligência offline/on-device de alta qualidade sem LLM grande (0.9.x em diante)

Pesquisa de 04/11/2026, depois de `harness-first-offline.md` (tese), `offline-brain-research.md` (agentes
clássicos, NLU sem transformer, entender o projeto — **implementado na 0.9**) e `tocomas-plan-reuse.md`
(APC/AgentReuse, receitas). Este doc aprofunda o que ficou de fora: (a) diálogo por estado de informação;
(b) classificadores leves online e calibração de confiança; (c) retrieval sem embeddings por índice
invertido; (d) reuso de planos como raciocínio baseado em casos (CBR) e macro-operadores; (e) "scripts >
prompts": ação como programa instanciado e verificado; (f) destilar um LLM forte em assets que rodam offline.
Nada aqui repete o que os outros docs já fundamentaram — quando o tema já tem doc, cito o doc, não o paper.

Convenções: cada afirmação traz a fonte primária. **(estimativa)** marca número meu, não medido. "Custo em
1 núcleo" vale para o alvo Dell (Pentium M 1,7 GHz, linux/386, 2 GB, HDD), mesma máquina de
`results/dell-0.9-progress.md`. Identificadores do código em inglês.

---

## 1. A conta: por que offline ganha no Dell

**Caminho com LLM (medido, 0.8.1 e baseline de modelo grátis).**

| pedido | custo medido |
|---|---|
| `jazz -p "abre o README.md"` | 14,3–21,5 s, 2 requisições, **32,3 k tokens** de entrada |
| `jazz -p "git status"` | 34,6–36,1 s, foi ao modelo e bloqueou |
| requisição a modelo grátis (kilo etc.) | 20–60 s cada, ~200 req/h por IP (`fusion-architectures.md`, `tocomas-plan-reuse.md` §3) |

**LLM on-device (publicado + estimativa honesta).** llama.cpp num Raspberry Pi 5 (4× Cortex-A76 2,4 GHz,
16 GB, NEON) faz ~4,4 tok/s com Llama-3.2-3B e ~10–11 tok/s com modelos de 1 B
([ggml-org/llama.cpp#31103](https://github.com/ggml-org/llama.cpp/discussions/31103);
[edge bench](https://edgellm-bench.vercel.app/results.csv)). Num Pi 4 de 4 GB o LLaMA-7B sai a ~10 s/token
([issue #58](https://github.com/ggml-org/llama.cpp/issues/58)). O Dell tem 1 núcleo, FSB 400 MHz
(≤ ~3,2 GB/s teóricos de banda, `low-level-performance.md`) e **sem SIMD útil** (SSE2 apenas):

- Q4_0 3B ≈ 1,8–2,0 GB de pesos: **não cabe** em 2 GB junto com o SO — descartado;
- 1 B Q4_K_M ≈ 0,7–0,8 GB cabe, mas ~0,5–1,5 tok/s (estimativa por banda ÷ modelo): uma resposta de
  500 tokens leva **5–16 min**;
- e um SLM dessa faixa é fraco justamente em tool-use agentic (EMNLP 2024, já citado em
  `harness-first-offline.md`).

**Cérebro offline (medido, 0.9 ab1efdf, mesma máquina).**

| pedido | custo medido |
|---|---|
| `jazz -p --offline "abre o README.md"` | **120–139 ms, 0 requisições, 0 tokens, RSS 27 MB** |
| `jazz -p --offline "git status"` (repo) | **133–203 ms**, resposta correta |
| `jazz -p "talscle status"` (typo) | 189–366 ms, `tailscale status` executado |
| `jazz eval offline` (39 tarefas, época) | 39/39 · falso sucesso 0 · p50 **14 ms** · p95 362 ms · RSS 59 MB |

Diferença: **2–3 ordens de magnitude** de latência, zero tokens, e funciona com o WiFi morto — que no Dell
é o caso comum, não a exceção. O gargalo deixa de ser "modelo" e vira **cobertura determinística**: quanto
mais pedidos o interpretador entende e a biblioteca de métodos cobre, mais o agente resolve sem gastar
nada. Cobertura se constrói em tempo de compilação — a filosofia deste doc.

Referência de sanidade externa: o pipeline NLU do Snips (determinístico→probabilístico, mesmo formato do
nosso) cabe em "algumas centenas de KB" e roda a 60 ms num Raspberry Pi 3, 220 ms num Pi Zero
([Coucke et al., arXiv:1805.10190](https://arxiv.org/abs/1805.10190)).

---

## 2. Princípios operacionais (11)

Cada um: afirmação curta + o que muda no jazz + fonte.

**P1 — Determinístico primeiro, probabilístico de apoio, veto sempre.** Parser exato cobre o que sabe
(F1 = 1 no que cobre); o classificador só decide no miss ou no desempate, e abaixo do limiar de confiança o
sistema não age — pergunta ou devolve. Fonte: Snips NLU, arXiv:1805.10190 (mesma arquitetura: regex gerada
dos exemplos, depois logistic + CRF por intenção; F1 de slots 0,79 com 70 exemplos, batendo
Luis/Dialogflow/Wit). No jazz: `nlu.go:897-1036` (regras → NB), `nuMinConf` 0,6 em `nlu.go:45`,
`brLevel0Min` 0,85 em `brain.go:593`.

**P2 — Palavra livre nunca vira entidade; o mundo resolve.** O slot guarda as palavras do usuário; a
resolução contra o repositório/ambiente é trabalho do método: 1 candidato → age; 2–3 → pergunta numerada
com motivo; 0 → dizer que não achou e mostrar os mais próximos. Fonte: SHRDLU/Winograd, AI TR-235
(o parser consulta o modelo do mundo para desambiguar — o mundo do jazz é o repo + o envprobe). No jazz:
contrato documentado em `nlu.go:24-27`, implementado por `resolve_name`/`choose` (`methods.go:1157`) e
`methods/open_file.json`.

**P3 — Gerenciar diálogo por estado de informação, não por transcript.** O diálogo é um registro de
fatos/slots/agenda atualizado por regras de update; "o segundo", "o parser.go", "cancela" só fazem sentido
contra o estado aberto. Fonte: Traum & Larsson, "The Information State Approach to Dialogue Management",
em *Current and New Directions in Discourse and Dialogue*, Springer 2003; Larsson, "Issue-Based Dialogue
Management" (tese, Univ. Gothenburg, 2002) e a implementação GODIS; RavenClaw pergunta só o
slot obrigatório vazio (Bohus & Rudnicky 2009, já citado). No jazz: `TaskState` com Facts/Slots/Plan
(`task.go:112-184`) existe por tarefa; **falta** o frame persistir entre pedidos (proposta G).

**P4 — Confiança tem que ser calibrada para decidir agir vs perguntar.** NB dá probabilidades tipicamente
mal calibradas; uma sigmoide de Platt ou uma isotônica ajustada em dados held-out transforma score em
probabilidade que pode governar um threshold. Fontes: Niculescu-Mizil & Caruana, "Predicting Good
Probabilities with Supervised Learning", ICML 2005
([dl.acm.org/doi/10.1145/1102351.1102430](https://dl.acm.org/doi/10.1145/1102351.1102430)); Platt 1999;
Zadrozny & Elkan, ICML 2001 e KDD 2002. No jazz: a confiança é heurística (`nlu.go:935-967`: 0,9 regra, 0,97 se o
NB concorda, p×0,75 sem regra) — funciona, mas os limiares 0,6/0,85 nunca foram medidos (proposta D).

**P5 — Aprender online é O(#features); promover a asset é que é caro.** Perceptron averaged
(Collins, "Discriminative Training Methods…", EMNLP 2002, [W02-1001](https://aclanthology.org/W02-1001/)),
Winnow (Littlestone, "Learning quickly when irrelevant attributes abound", Machine Learning 2(4), 1988) e
SGD logístico (Bottou, COMPSTAT 2010) atualizam em microssegundos e vivem de features já extraídas — servem
para **desempatar e sugerir**, nunca para vetar regra nem agir sozinhos; o caminho usuário→candidato→promoção
continua sendo a regra. No jazz: a confirmação de menu/typo já existe (`brain.go:127-148`,
`offers.go:253-351`); falta o modelo online que a registra (proposta F).

**P6 — Plano bom é plano aprendido de sucesso verificado (macro-operadores / CBR).** A IA clássica
aprende operadores compostos de soluções: macro-operadores de Korf ("Learning to Solve Problems by
Searching for Macro-Operators", 1985), EBL/PRODIGY generaliza de um exemplo com a teoria do domínio
(DeJong & Mooney, Machine Learning 1(2), 1986), HTN-MAKER extrai métodos HTN de traços solucionados +
tarefas anotadas, sound e complete (Hogg, Muñoz-Avila, Kuter, AAAI 2008,
[aaai.org](https://aaai.org/papers/00950-aaai08-151-htn-maker-learning-htns-with-minimal-additional-knowledge-engineering-required/)).
O equivalente LLM: AWM induz "workflows" de traços passados — +51,1% relativo no WebArena, +24,6% no
Mind2Web (arXiv:2409.07429). A regra que atravessa tudo: **só entra na biblioteca o que passou em
verificação objetiva** (a Darwin-Gödel-Machine é o contraexemplo canônico de otimizar sem verificação,
`metaharness-self-improvement.md`). No jazz: `plancache.go` guarda templates após `--until`/gate/exit 0,
nunca porque o modelo disse que acabou
(`acTurnDone`, `plancache.go:377-401`); CBR no brain é proposta C.

**P7 — Script > prompt: ação como programa instanciado e verificado.** Ações geradas como código
executável superam JSON/texto predefinido: até +20 pontos de sucesso e −30% de ações no benchmark
multi-tool (CodeAct, arXiv:2402.01030; gpt-4-1106: 74,4% vs 53,7% com JSON). A leitura para o offline é a
metade da lição: um programa é **verificável e reversível** — valide antes (linter, schema, allowlist) e
observe o efeito depois (o `try` da PaSh roda o comando em overlayfs e só comita o que você aceita,
OSDI'26, [binpa.sh/try](https://binpa.sh/try/)). No jazz: capacidades já são chamadas tipadas com
verificador (`caps.go:67-187`, `methods.go:1680-1760`); falta síntese por template validada (proposta E).

**P8 — Separar planejar de observar economiza a moeda rara.** ReWOO: planner emite o plano com
placeholders, workers executam sem repensar, solver fecha — 5× eficiência de token e +4% de acurácia
(arXiv:2305.18323; ~80% menos tokens no relato da IBM). No Dell a moeda rara não é token: é **segundo de
requisição** e ciclo de CPU. A forma já existe: métodos declaram todos os passos antes de executar
(`brExecute`, `brain.go:258-421`), então entre passos não há "repensar". A lição é manter essa fronteira:
observações voltam como fatos, não como novo plano.

**P9 — Índice invertido é o piso de retrieval; embeddings são luxo.** Trigramas: Cox, "Regular Expression
Matching with a Trigram Index", jan/2012
([swtch.com/~rsc/regexp/regexp4.html](https://swtch.com/~rsc/regexp/regexp4.html)) — no kernel 3.1.3,
"hello world" reduz 36.972 arquivos a 25 candidatos; Zoekt usa trigramas **posicionais**, índice ~3× o
corpus (2× offsets + 1× conteúdo) mas tocando só algumas posting lists por query, meta sub-50 ms em ~2 GB
de texto ([doc/design.md](https://github.com/sourcegraph/zoekt/blob/main/doc/design.md)). E grep inline
vence vector inline em 10/10 pares modelo×harness, maior justamente em modelos fracos
(arXiv:2605.15184, analisado em `grep-is-all-you-need.md`). BM25 basta para ranquear planos e lições
(Robertson & Zaragoza, FnTIR 2009) — já em uso, `plancache.go:244-301`. **Sem índice de conteúdo** ainda —
proposta B.

**P10 — Inteligência em tempo de compilação: destilar o LLM forte em assets.** O otimizador roda uma vez,
o artefato deploya para sempre: DSPy compila pipelines declarativos e os compilados batem few-shot por
>25%/>65% nos estudos (arXiv:2310.03714); GEPA reflete sobre traços e supera MIPROv2 por >10% usando até
35× menos rollouts que RL (arXiv:2507.19457); Snorkel trata heurísticas (aqui: o LLM forte) como funções
de rotulagem fracas que treinam um classificador final (Ratner et al., VLDB 2017,
[arXiv:1711.10160](https://arxiv.org/abs/1711.10160)); Self-Instruct gera exemplos a partir de poucas sementes
(arXiv:2212.10560); "distill step-by-step" mostra que raciocínio destilado em dados pequenos supera modelos
maiores na tarefa (Hsieh et al., ACL 2023, arXiv:2305.02301); destilação clássica em Hinton et al. 2015
(arXiv:1503.02531). E o pano de fundo arquitetural: resultados de ponta vêm de **sistemas compostos**, não
de um modelo (Zaharia et al., "The Shift from Models to Compound AI Systems", BAIR 18/02/2024,
[bairblog.github.io](https://bairblog.github.io/2024/02/18/compound-ai-systems/)). No jazz: `improve.go`
já destila lições em prosa; falta destilar **assets executáveis** (exemplos NLU, léxico, métodos) —
proposta A; a spec §7 (`docs/design/offline-brain.md`) já prevê e `learn.go` ainda não existe.

**P11 — k-NN/CBR é o classificador de uma linha: a base de exemplos é o modelo.** Classificar = votar os
k exemplos mais próximos (Cover & Hart, "Nearest neighbor pattern classification", IEEE Trans. Inf. Theory
13(1), 1967); com similaridade por n-grama/BM25 sobre os exemplos já existentes não há treino nenhum — cada
exemplo novo distilado melhora o classificador sem retreino e serve de "caso" explicável ("classifiquei
como X porque parece com o exemplo Y"). Em CBR formal: retrieve → reuse → revise → retain
(Aamodt & Plaza, AI Communications 7(1), 1994). No jazz: o NB já vive dos exemplos (`nlu.go:1327-1389`);
um voto k-NN sobre `examples` custa uma varredura de ~600 docs × n-gramas — <1 ms (estimativa).

---

## 3. Mapa no código (file:line, feito/parcial/só pesquisado)

| mecanismo | onde no jazz | estado |
|---|---|---|
| fast path regex → comando (atalhos) | `intents.go`, `intents.json` | feito |
| pipeline NLU completo (normalize → conceitos → AC → regras → NB) | `nlu.go:897-1036` (Parse), normalização `nlu.go:337-395`, stemming light `nlu.go:260-273` | feito |
| gramática semântica all/any/none por intenção | `nlu.go:838-892`; dataset `nlu/intents.json` (50 intenções, 644 exemplos) | feito |
| léxico curado (159 slang, 63 conceitos, 216 fillers) | `nlu/lexicon.json` | feito |
| slot-filling por entidade tipada + residuais + required | `nlu.go:1098-1214` (fill), `nlu.go:1061-1093` (residual) | feito |
| entidades do mundo: Aho-Corasick sobre arquivos+perfil | `nlu.go:443-491`, `nlu.go:558-597` (nuRuntimeWorld), autômato `nlu.go:1448-1540` | feito |
| reparo de typo (Damerau c/ custo de teclado; TypoOnly → confirmação 1-tecla) | `fuzzy.go:104-141`, `nlu.go:747-777`, `brain.go:127-148` | feito |
| NB char 3–5-gramas, hash FNV 2¹⁴, int16 log-probs | `nlu.go:1291-1442` (tabela ~50×16K int16 ≈ 1,6 MB) | feito; **calibração não medida** — parcial (P4) |
| dataset = teste (≥95% exemplos; cego 100 medido, nunca ajustado, piso 70%) | `nlu_test.go:29-104`, `nlu/blind_test.json` | feito |
| biblioteca HTN-lite (48 arquivos, pre/steps/verify/needs/cmds) | `methods.go:60-153`, `methods/*.json` | feito |
| execução Pyhop: método aplicável → passos → falha → recuperação → verify | `brain.go:258-421` | feito |
| recuperação por categoria observável (10 categorias + streak de exploração) | `recover.go:18-151`, `recover.go:180-242` | feito |
| registro de capacidades (risk/effects/needs/verifier/timeout/undo) | `caps.go:67-187`; dispatch `script:*` `methods.go:335-343` | feito |
| verificação por evidência (file_exists, tests_passed, script_ok…) | `methods.go:1680-1760` | feito |
| estado de tarefa + máquina de estados + evidência/decisões | `task.go:20-184` | feito; **não persiste entre pedidos** — parcial (P3) |
| pergunta decisiva / menu numerado (SHRDLU rule) | `methods.go:1157-1222` (choose/ask_slot), `offers.go:253-351` | feito |
| "investigar antes de perguntar" (fatos que o disco responde) | `askfacts.go:35-116` | feito |
| nível 0: mesmo online, intenção read-only confiante pula o modelo | `brain.go:576-624` | feito |
| multi-intent com validação por-parte | `brain.go:54-108` | feito |
| plan cache verificado (BM25 puro, wins/fails, eviction, ≤3 injetados) | `plancache.go:244-364`, `479-552` | feito — **só alimenta o modelo**; CBR no brain é proposta C |
| hindsight por modelo forte (destilação light de traço→dica) | `plancache.go:561-601` | feito |
| lições destiladas de runs (determinístico + 1 chamada tool-less + --apply) | `improve.go:3-23`, `lessons.go`, `improve_load.go` | feito (prosa; não assets executáveis) |
| índice de nomes persistido (struct-of-arrays, validade por dir/mtime) | `nameindex.go`, `nameindex_idx.go:35-95` | feito (nomes; **sem conteúdo**) |
| símbolos por regex + ranking por contexto | `nameindex.go` (`niSymRE`, cues), `apimap.go` | feito |
| pre-fetch lexical no 1º turno | `prefetch.go:34+` | feito (flag off até A/B) |
| env probe (online, bins, hosts, serviços, containers, RAM) | `envprobe.go` (`epProfile`, `epOnline`, `epEntities`) | feito |
| modo offline completo (detecção, banner, menus, probe 30 s) | `offline.go:62-217`, `342-399` | feito |
| eval offline: 75 tarefas, no_llm_rate/false_success/false_blocks/p50/RSS | `evaloffline.go:91-249`, `evals/offline/tasks.json` | feito |
| índice trigrama de conteúdo | — | só pesquisado (P9; Cox/Zoekt) |
| calibração de confiança (Platt/isotônica) | — | só pesquisado (P4) |
| aprendizado online (perceptron/Winnow/k-NN de usuário) | — | só pesquisado (P5/P11) |
| CBR de casos (intenção+shape→binding) | `plancache.go` cobre o modelo, não o brain | parcial (P6) |
| destilação LLM→assets executáveis (spec §7 learn.go) | `improve.go` gera prosa, não assets | parcial (P10) |
| síntese de script por template + linter + efeito observado | manifestos `jazz-scripts/*.json`, parser shell `shellparse.go` | parcial (P7) |
| PageRank repomap / co-mudança git | — | só pesquisado (`offline-brain-research.md` §C) |

---

## 4. Propostas (ordenadas por impacto esperado)

Todas em Go stdlib, medidas no Dell. Métrica = a que a proposta move em `jazz eval offline`
(no_llm_rate, sucesso verificado, false_success, false_blocks, perguntas, comandos/tarefa, p50/p95, RSS).

### A. `jazz forge`: destilar o LLM em assets executáveis (spec §7, ainda não implementada)
- **Mecanismo:** quando online e a tarefa **verificada** tinha `handled=false` (NLU "none") ou usou método
  novo: 1 chamada tool-less ao modelo forte escreve candidatos — (a) exemplos NLU para intenções
  existentes, (b) gírias/sinônimos para `lexicon.json`, (c) `methods/*.json` para intenção nova/composta.
  Validação **determinística offline**: cada exemplo tem que parsear na própria intenção (reusa `nuGet`);
  cada método passa schema-check (caps existem, `mhBadCmd` na allowlist, pre/steps/verify válidos) e um
  dry-run em fixture. `--promote` lista o diff e confirma; assets vão para
  `dataDir()/brain/{examples.jsonl,lexicon.json,methods/}` com proveniência {sessão, modelo, wins} —
  overlay sobre os embeds (confiança: embed > user > project). É P10+P5+P6 juntos: Snorkel (LLM = labeling
  function fraca), HTN-MAKER (métodos de traços), DSPy (compilar uma vez).
- **Arquivos:** novo `forge.go` (~300 ln), `improve.go` (subcomando), `nlu.go` (overlay loader de
  exemplos/léxico), `methods.go` (`mhLibrary` com overlay + trust como `intents.json`).
- **Custo no 386:** zero em runtime; validação <1 s por lote; assets em KB.
- **Métrica:** no_llm_rate ↑↑ (cobertura cresce a cada run online verificado), sucesso verificado ↑.
- **Impacto 5 · Esforço L · Risco médio** (asset envenenado → validação + promote manual + proveniência,
  mesma tela de `mhBadCmd`).

### B. Índice trigrama de conteúdo puro-Go (codesearch-lite, retira a dependência de rg)
- **Mecanismo:** por arquivo ≤1 MB e extensão de código (`niCodeExt`), posting lists trigrama→fileID (por
  documento, não posicional — mais leve que Zoekt, estilo Cox), persistido em `dataDir()/ni/<hash>.tri`
  reaproveitando a validação por-dir/mtime de `nameindex_idx.go`. Query: extrai trigramas da literal/regex
  (análise conservadora à la `Query{}` do codesearch), intersecta postings, e o `regexp` (RE2) confirma só
  nos candidatos. Alimenta um cap `find_text`/`search` do brain e o investigador — hoje `find` precisa de
  `rg` (ausência possível no Dell) ou `python3` (`find.symbol` tem `needs: bin.python3`).
- **Arquivos:** novo `trindex.go` (~400–600 ln), `nameindex_idx.go` (persistência), `methods/*.json`
  (find), `brCaps`.
- **Custo no 386:** índice ~5–15% do corpus (estimativa; trigrama→arquivo sem posições, varint — bem
  abaixo do ~3× que o Zoekt documenta para o formato posicional com offsets); build ~1–3 s em repo de
  50 MB (HDD-bound, incremental por mtime); query <50 ms.
- **Métrica:** no_llm_rate ↑ ("procura X" sem rg nem python3), p50 ~igual ou ↓ em repo grande.
- **Impacto 5 · Esforço M · Risco médio** (índice em monorepo → cap de corpus e `rtSkipDir`/`niCodeExt`).

### C. CBR no brain: cache de casos (intenção + slot-shape → binding resolvido)
- **Mecanismo:** ao `tkSucceeded`, gravar caso {intent, shapes dos slots (não valores), método usado,
  bindings-chave (ex.: "o teste do parser" → `parser_test.go`), repo-sig, wins} em
  `dataDir()/brain/cases.jsonl`. Num `Parse` com mesmo intent+shape, `pcMatch` (BM25 já pronto) ranqueia
  casos; o binding aprendido propõe a resolução **antes** de `resolve_name`+`choose`, com confirmação se
  >1 caso próximo ou binding obsoleto (verifica existência). Retain: wins/fails à la `pcScore`, eviction
  com `pcEvictFails`. É o 4-R de Aamodt & Plaza: retrieve=BM25, reuse=binding, revise=confirmação do
  usuário, retain=score.
- **Arquivos:** novo `brcase.go` (~200 ln), `brain.go` (hook em `brRunIntent`), `methods.go` (`brResolve`
  consulta casos).
- **Custo no 386:** leitura + score <1 ms; 1 JSON por tarefa.
- **Métrica:** p50 ↓ (pula resolve+choose nos casos repetidos — AgentReuse mede ~30% de pedidos
  repetidos/parecidos, `tocomas-plan-reuse.md`), perguntas ↓, no_llm_rate ↑.
- **Impacto 4 · Esforço M · Risco médio** (caso velho em repo mudado → repo-sig + existência do binding).

### D. Calibração de confiança medida (Platt-lite sobre held-out)
- **Mecanismo:** gerar pares (raw conf → acertou?) por k-fold determinístico dos 644 exemplos + o cego;
  ajustar sigmoide (Platt, 5 iters de Newton, ~30 ln) ou mapa isotônico por 10 faixas; gravar
  `nlu/calib.json` (gerado por `jazz nlu-calibrate`, offline, determinístico, embed). Em `Parse`,
  `conf = calib(raw)`. Recalibrar `nuMinConf`/`brLevel0Min` pela curva precisão×faixa publicada no
  relatório. Nunca muda a intenção escolhida — só decide agir/perguntar (P4).
- **Arquivos:** `nlu.go` (uso da calibração em `Parse`), novo `nlu_calibrate.go` (~200 ln),
  `nlu/calib.json` embed.
- **Custo no 386:** <0,1 ms runtime; treino <1 s; +KB embed.
- **Métrica:** false_success ↓ (menos ação com confiança inflada), no_llm_rate ↑ (limiar justo → menos
  "miss honesta"), perguntas corretas ↑.
- **Impacto 4 · Esforço S · Risco baixo**.

### E. Síntese de script por template + linter + efeito observado (CodeAct-lite offline)
- **Mecanismo:** cap `script_gen`: template = {argv com slots, schema de saída, efeito esperado,
  verifier}. O brain só **instancia** — nunca gera shell livre: ex. "roda `<cmd>` em todo arquivo que casa
  `<glob>`" mapeia candidatos do nameindex. Antes de executar: (1) linter próprio sobre `shellparse.go`
  (pipe para `sh -c`, expansão sem aspas, escrita fora de Root, comando fora da allowlist/`permit`);
  (2) efeito observado via shadow-git já existente (`checkpoint.go`) — o diff antes/depois é a evidência,
  inspirado no `try` (OSDI'26) sem precisar de overlayfs, pois o bwrap do `sandbox.go` já existe;
  (3) `verify` do método.
- **Arquivos:** novo `scriptlint.go` (~150 ln), `methods.go` (cap `script_gen`), `methods/*.json`
  (`run_each`, `batch_apply`), `sandbox.go`/`checkpoint.go` (reuso).
- **Custo no 386:** lint <1 ms; efeito = custo do comando + 1 fork de bwrap (~9 ms medido).
- **Métrica:** false_success ↓ (efeito verificado, não declarado), no_llm_rate ↑ (tarefas compostas).
- **Impacto 4 · Esforço M-L · Risco médio** (falso-negativo do linter em shell exótico → allowlist
  estreita, fallback = relatório honesto, nunca inventa).

### F. Aprendizado online assistido: modelo do usuário que só desempata
- **Mecanismo:** linear hashed nos mesmos features `nuNgrams` + features do mundo ("casa arquivo",
  "casa símbolo"), formato int16 como `nuNB`; update perceptron-averaged **só em confirmações reais**
  (escolha de menu, confirmação de typo, brain vencendo onde regras disseram none) — O(#features) por
  evento, persistido em `dataDir()/nlu/user.bin`. Uso restrito: desempate entre matches empatados e
  boost de caso C — **nunca veta regra nem cria intenção** (guardrail). Cada confirmação também enfileira
  exemplo candidato para `examples.jsonl` (promoção ≥3 usos, já prevista na spec §10).
- **Arquivos:** novo `nuonline.go` (~150 ln), `nlu.go` (hook no desempate `nlu.go:950-967`), `brain.go`
  (hooks de confirmação).
- **Custo no 386:** ~1,6 MB int16 (ou ~400 KB int8), update <0,1 ms.
- **Métrica:** no_llm_rate ↑ (personalização ao jeito do dono de falar), p50 ~igual.
- **Impacto 3 · Esforço M · Risco médio** (drift de modelo online → só desempata, rollback pelo cego).

### G. Estado de informação persistente entre turnos (frame aberto)
- **Mecanismo:** `TaskState` já vai à sessão via sink; gravar adicionalmente
  `dataDir()/tasks/open.json` = {id, intent, slots, missing, step_idx, candidatos, TTL}. A próxima
  mensagem curta primeiro tenta resolver contra o frame ("o segundo" → `cands[1]`, "parser.go" → slot
  pendente, "cancela" → descarta); slot preenchido → retoma o método no passo que esperava (re-execução
  barata dos read-only); senão → `brRun` normal. É GODIS-lite: resposta ancorada em slot pendente (P3).
- **Arquivos:** `task.go` (persistir frame aberto), `brain.go` (`brResume` antes de `brRun`),
  `offers.go`.
- **Custo no 386:** 1 JSON; <1 ms.
- **Métrica:** perguntas necessárias ↓, sucesso verificado ↑ (tarefas param de morrer em needs_input).
- **Impacto 3 · Esforço M · Risco médio** (frame velho capturando mensagem nova → TTL ~10 min +
  só se NeedsInput + "cancela/esquece" explícito).

### H. Cego 100→300 + mutação sintética de robustez (medida antes de alegar)
- **Mecanismo:** expandir `nlu/blind_test.json` com 200 itens escritos sem olhar saídas (protocolo já
  documentado no arquivo); mutador determinístico no teste: slip de teclado (`fzSlip` neighbours),
  drop de acentos, troca de slang por sinônimo do léxico, reordenação de multi-intent — reporta acurácia
  por categoria, **medida e não gate** além do piso 70% atual. Alimenta a calibração de D.
- **Arquivos:** `nlu/blind_test.json`, `nlu_test.go` (mutador ~100 ln), `evals/offline/tasks.json`
  (tasks das classes fracas medidas).
- **Custo no 386:** zero em runtime.
- **Métrica:** qualidade da afirmação ↑ (habilita thresholds de D e a afirmação real de no_llm_rate).
- **Impacto 3 · Esforço S · Risco nenhum**.

### Não-propostas registradas
- **Embeddings/vetores:** não cabem no orçamento e não mostraram ganho para código (grep-doc; P9).
- **CRF de slots:** só se o cego expandido (H) mostrar slots livres errados; gazetteers do mundo bastam
  (Snips).
- **Modelo local ≥3B:** não cabe em 2 GB com SO (§1).
- **Planner de ordem superior/Rete:** <300 regras, varredura basta (`offline-brain-research.md` §A.6).
- **tree-sitter/gotreesitter:** custo de binário 386 não medido; `niSymRE` cobre (mesmo doc §C).

---

## 5. Fontes consolidadas

NLU/diálogo: Snips arXiv:1805.10190 · Winograd AI TR-235 · Traum & Larsson 2003 (info-state) · Larsson
GODIS 2002 · Bohus & Rudnicky 2009 (RavenClaw). Classificadores: Collins EMNLP 2002 (perceptron) ·
Littlestone 1988 (Winnow) · Bottou COMPSTAT 2010 (SGD) · Niculescu-Mizil & Caruana ICML 2005, Platt 1999,
Zadrozny & Elkan 2001 (calibração) · Cover & Hart 1967 (k-NN). Planos/casos: Korf 1985 (macro-ops) ·
DeJong & Mooney 1986 (EBL) · Hogg/Muñoz-Avila/Kuter AAAI 2008 (HTN-MAKER) · Aamodt & Plaza 1994 (CBR) ·
AWM arXiv:2409.07429 · APC arXiv:2506.14852, AgentReuse arXiv:2512.21309 (doc tocomas). Scripts/ação:
CodeAct arXiv:2402.01030 · try/semisolates OSDI'26 ([binpa.sh/try](https://binpa.sh/try/)) · ReWOO
arXiv:2305.18323. Retrieval: Cox 2012 ([swtch.com/~rsc/regexp/regexp4.html](https://swtch.com/~rsc/regexp/regexp4.html))
· Zoekt design ([github.com/sourcegraph/zoekt](https://github.com/sourcegraph/zoekt/blob/main/doc/design.md))
· Robertson & Zaragoza FnTIR 2009 (BM25) · arXiv:2605.15184 (grep-doc). Destilação/compilação: DSPy
arXiv:2310.03714 · GEPA arXiv:2507.19457 · Snorkel arXiv:1711.10160 · Self-Instruct arXiv:2212.10560 ·
Hsieh ACL 2023 arXiv:2305.02301 · Hinton arXiv:1503.02531 · Zaharia et al. BAIR 02/2024. On-device:
llama.cpp discussions [#31103](https://github.com/ggml-org/llama.cpp/discussions/31103),
[#30069](https://github.com/ggml-org/llama.cpp/discussions/30069), issue #58 · medidas próprias em
`results/dell-0.9-progress.md`, `results/dell-baseline-0.8.1.md`.
