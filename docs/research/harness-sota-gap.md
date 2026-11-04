# Harness SOTA vs jazz-cli: estado da arte e gap analysis (2026-11-04)

Alvo: `Dell` (Linux i686, 1 CPU, 2 GB, WiFi flaky, binário linux/386, Go stdlib).
Filosofia: inteligência em tempo de compilação — assets forjados uma vez por um modelo forte,
reusados para sempre a custo zero de token no alvo.

**Não repete** (ler antes): `harness-first-offline.md` (tese harness-primeiro),
`offline-brain-research.md` (clássicos: HTN/BT/NLU), `metaharness-self-improvement.md`
(Meta-Harness/Self-Harness/RHO/DGM), `shepherd-meta-agents.md` (handoff, hindsight),
`tocomas-plan-reuse.md` (plan cache, recipes A3, intents A1), `grep-is-all-you-need.md`
(retrieval lexical), `omni-judge.md` (roteador/bandit/fusão), `bob-kiro-devin.md`
(steering, spec, sandbox, subagentes comerciais), `benchmarks.md` (números no Dell).

Este doc cobre o que falta: **estado da arte de harness em números (2026)**, comparação
concreta dos 6 harnesses de referência, e um gap analysis priorizado com propostas
que rodam no Dell.

---

## 1. O SOTA em números

### SWE-bench Verified (Vals AI, set/2026) [S1]

O leaderboard virou comparação de **modelos**: quase todas as linhas rodam o mesmo
harness, `mini-swe-agent` v2 — porque quando o harness varia, a comparação morre.

| faixa | valores observados |
|---|---|
| topo | Opus 5 97.0% · DeepSeek V4 Pro 96.4% · GPT-5.6 Sol 96.2% |
| custo/tarefa | $0.01 (GLM 5.3 Flash) → $2.42 (Opus 4.7) |
| tokens/tarefa | 0.5M → 2.7M |
| duração/tarefa | 3 min → 64 min |
| Claude Opus 4.8 | mini-SWE-agent **88.6%** vs Claude Code **85.8%** — mesmo modelo, −2.8 pts só de harness |

### Terminal-Bench 2.0 (tbench.ai, mar/2026) [S2]

89 tarefas, cada uma com ambiente próprio, solução humana e **testes abrangentes**
[paper: S3]. Topo: Forge Code/Gemini 3.1 Pro 78.4%, Droid/GPT-5.3-Codex 77.3%,
Simple Codex 75.1%, Terminus-KIRA 74.7%, Mux 74.6%. Claude Code ~58% e
Meta-Harness (Opus) 76.4% [S4 — ver `metaharness-self-improvement.md`].
Nota do paper: modelos+agentes frontier < 65% na época — a cabeça do ranking
hoje é **harness**, não backbone.

### Duas lições estruturais

1. **O harness é efeito de primeira ordem.** No paper "Is Grep All You Need" [S5],
   o mesmo Opus marca 93.1% no harness Chronos e 76.7% no Claude Code; o mesmo
   Haiku marca 83.6% e 55.2%. O intervalo modelo×harness engole diferenças de
   retrieval. Em SWE-bench, Opus 4.8 perde 2.8 pts trocando de harness.
2. **O kernel que ganha é mínimo.** O `mini-swe-agent` que lidera tem <100 linhas
   no laço: bash-only, histórico append-only, `subprocess.run` por comando [S6].
   O topo de TB2 também é harness magro (Simple Codex, Terminus). Complexidade
   mora em avaliação e seleção, não no laço.

---

## 2. O que cada harness faz de bom — e onde falha

### Claude Code (Anthropic)
**Bom:** gestão de contexto mais documentada — auto-compact com resumo por LLM,
re-injeção pós-compact de CLAUDE.md/memória/plano/até 5 arquivos recentes lidos
do disco (não da memória do modelo) [S7]; prompt caching com camadas
(system > projeto > conversa; `/rewind` volta a prefixo já cacheado) [S8];
subagentes com contexto separado; plan mode.
**Falha:** zero modo offline; custo ($0.67–2.42/tarefa na tabela); compactação
gasta uma chamada grande e destrói o cache da conversa; assume modelo frontier —
o mesmo harness cai 27 pts com Haiku [S5]; nenhuma verificação objetiva nativa
("done" é declaração do modelo, amenizada por hooks externos).

### OpenAI Codex
**Bom:** melhor ACI de escrita — `apply_patch` verificado antes de aplicar,
executado **dentro do sandbox** (corrige hardlink/symlink escape, TOCTOU) [S9];
`execpolic`/approval granular com cache de decisão por chave; AGENTS.md;
execução em nuvem quando o usuário quer.
**Falha:** cloud-first, zero offline; apply_patch teve regressões reais sob
sandbox (PRs #1705, #18296 — "retry-without-sandbox"); sem verificação por
execução embutida (depende de CI fora); Rust binário ok, mas o gargalo é rede.

### OpenCode (SST/Anomaly)
**Bom:** o melhor padrão de subagente — permissão é **ruleset por papel**
(plan-agent nem vê ferramenta de escrita; "tools the LLM can't see, it can't
call") [S10]; `task` delega com contexto separado e retorna relatório; LSP real.
**Falha:** stack TS/Effect/SQLite/servidor — pesada demais para 2 GB confortável;
subagente multiplica tokens; nada funciona sem modelo.

### Aider
**Bom:** repo map por PageRank sobre grafo de defs/refs com orçamento de tokens
e personalização (arquivos citados ×10, chat ×50) [S11]; dispatch de edit-format
por capacidade do modelo (`whole` p/ fracos, `diff` p/ fortes) — a camada
modelo→disco como protocolo explícito; auto-lint/auto-test com reflexão
bounded (`max_reflections`); auto-commit = undo por design.
**Falha:** interativo, não autônomo; tree-sitter (cgo) inviável em binário 386
estático; verificação = lint/test que o modelo decide rodar; sem offline.

### SWE-agent / mini-SWE-agent (Princeton/SWE-bench)
**Bom:** a teoria do ACI [S12]: ações compactas e de alto nível (não flags de
bash), feedback "informativo mas conciso" (file viewer de 100 linhas, busca que
só lista arquivos), guardrail **dentro** da ferramenta (linter no edit — edição
inválida não aplica), resposta explícita a output vazio. mini: laço mínimo
auditável, linear, stateless [S6].
**Falha:** mini não é produto — `sed` corrompe silenciosamente, sem edição
tipada, sem compaction, sem undo, sem guardas; SWE-agent completo tem orquestra
pesada; nenhum modo offline; nenhum dos dois mede custo por tarefa para o usuário.

### OpenHands (All-Hands)
**Bom:** event-sourcing completo — tudo é evento imutável; **Condenser**: drop
seletivo + resumo por LLM preservando pontas (first N + last M); custo por passo
deixa de crescer quadraticamente → linear; security analyzer revisa ação antes
de executar [S13].
**Falha:** resumo de condensação = mais uma chamada grande; stack Python pesada;
sem offline; o evento "esquecido" sai da visão mas o modelo não sabe o que perdeu.

### Agentless (OpenAutoCoder)
**Bom:** prova de que **pipeline fixa** rivaliza com agente livre — localização
hierárquica repo→arquivos→elementos→linhas (LLM + retrieval + filtro de pastas
irrelevantes), N patches amostrados, **seleção por testes de reprodução +
regressão**, re-rank [S14]. 32% em SWE-bench Lite a custo baixo para a época.
**Falha:** LLM em todos os estágios (localizar, gerar patch, gerar teste);
embeddings para retrieval; só atende o formato issue→patch.

### Test-time compute (CodeMonkeys, RTV/PDR, SWE-Replay, TEX)
**Bom:** maiores ganhos documentados depois do harness — CodeMonkeys 57.4%
Verified com serial+parallel scaling e **votação por testes gerados**; ensemble
selection 66.2% [S15]. RTV sobre resumos de trajetória: Opus 70.9→77.6% [S16].
SWE-Replay: ramificar de passos críticos em vez de recomeçar, −17.4% custo [S17].
**Falha:** caro demais para free tier ($2.3k por benchmark no CodeMonkeys);
best-of-n cego não funciona (Shepherd App. D: re-amostragem sem hint não fecha
o gap [S4]). O que sobra para nós: **seleção com evidência de execução** e
**reuse de trajetória**, ambos determinísticos.

### Onde TODOS falham igual (o espaço do jazz)
1. **Nenhum resolve tarefa com 0 tokens.** Offline = morto (banner de erro) ou
   degradado (menus). O jazz já responde 24 intenções sem modelo mesmo quando
   há um disponível (`brLevel0Intents`, brain.go:576) e tem 103 métodos
   determinísticos cobrindo 49 das 50 intenções do NLU; ninguém mais mede
   `no_llm_rate`.
2. **Custo por tarefa não é métrica de produto para o usuário.** Está no
   leaderboard, não no CLI.
3. **Verificação independente da ação é exceção** (Agentless, TB2 verifiers,
   `--until` do jazz), não regra — a norma é "o modelo disse que acabou".
4. **Nenhum propõe parar cedo.** Todo mundo descobre que a tarefa é impossível
   depois de N chamadas; o jazz descobre na pré-condição do método (0 chamadas).

---

## 3. Princípios operacionais (P1–P14)

**P1. Harness é efeito de primeira ordem — medir o harness, não só o modelo.**
Mesmo modelo, ±20 pts entre harnesses; padronizar o harness é a única comparação
honesta. Fonte: [S5] arXiv 2605.15184 Tabela 1; [S1] Vals AI.

**P2. Laço mínimo auditável: append-only, uma ação por passo, execução
stateless.** O kernel de <100 linhas lidera SWE-bench; cada primitiva extra é
passível de ablação. Fonte: [S6] swebench.com/post-250820-mini-roulette.

**P3. ACI > API: feedback informativo-mas-curto, guardrail dentro da ferramenta.**
File viewer com janela, busca que lista arquivos (não trechos), linter no edit,
"sucesso sem saída" explícito. Fonte: [S12] arXiv 2405.15793 + docs/background/aci.md.

**P4. Contexto é orçamento, não buffer — e o disco re-preenche o que o resumo
apaga.** Re-injetar fatos do disco pós-compact; resumir o meio, manter pontas;
context rot é medida, não lenda. Fonte: [S7] code.claude.com/docs/en/context-window;
[S13] OpenHands Condenser.

**P5. Localização é o gargalo — decompor repo→arquivo→elemento→linha.**
A etapa mais estruturada e mais barata de melhorar; combina retrieval lexical +
sinais estruturais + modelo só no topo da pirâmide. Fonte: [S14] arXiv 2407.01489.

**P6. Busca lexical primeiro; resultado inline; hits agrupados por arquivo.**
Grep bate vector retrieval em 10/10 pares inline (até +23 pts em modelo fraco);
entrega em arquivo derruba 18–38 pts. Fonte: [S5] arXiv 2605.15184 §4.1.

**P7. Verificação é execução, não declaração — e o verificador fica fora do
controle do agente.** Testes de reprodução+regressão selecionam patch; tarefas
de benchmark trazem testes abrangentes; "done" sem evidência é bug do harness.
Fonte: [S14] Agentless patch validation; [S3] arXiv 2601.11868; DGM lesson
(checker escondido) em `metaharness-self-improvement.md`.

**P8. Test-time compute só paga com seletor barato — votar com evidência de
execução, não com LLM.** Voting por testes gerados; sem seleção, best-of-n não
fecha gap. Fonte: [S15] arXiv 2501.14723; [S16] arXiv 2604.16529; [S4] App. D.

**P9. Trajetória é recurso reusável — ramificar de pontos críticos, guardar
hindsight curto.** Replay de prefixos corta custo ~17% com qualidade igual;
hint de "arquivo certo + beco sem saída" corta chamadas ~60%. Fonte: [S17]
arXiv 2601.22129; [S4] App. D (21.4→8.9 calls).

**P10. Subagente é escopo de permissão e contexto, não persona.** Plan-agent
sem ferramenta de escrita > prompt pedindo para não editar; relatório compacto
(paths+linhas) > transcript; handoff com boundary falho custa 40 pts.
Fonte: [S10] opencode.ai/docs/agents; TOCOMAS ablation (`tocomas-plan-reuse.md`).

**P11. Falha é observação classificável — tabela de recuperação por categoria,
não retry cego.** Exit code/contagem/saída → ação; "3–5 retries é normal, 10+
idênticas é loop". Fonte: [S4] supervisor heuristic; já implementado
(recover.go:108 `rvTable`).

**P12. Reversibilidade antes de mutação — snapshot a cada passo que escreve.**
Checkpoint shadow-git por turno e por bash-write; discard/handoff como degrau
acima de inject. Fonte: Q CLI checkpoint.rs (`bob-kiro-devin.md`); [S4] scopes;
checkpoint.go:257.

**P13. Pergunta só quando a resposta muda o plano — e o disco responde primeiro.**
Branch/test runner/PM/existência são fatos; menu numerado resolve ambiguidade
com 1 toque. Fonte: SHRDLU (Winograd 1971, `offline-brain-research.md` B);
askfacts.go:35 (feito).

**P14. Segurança na ferramenta, não no prompt — sandbox + patch verificado +
policy; fail closed.** apply_patch dentro do sandbox; approval cache; ação
desproporcional ao objetivo bloqueada com explicação. Fonte: [S9] openai/codex
safety.rs/apply_patch.rs; caps.go:356/553 (feito).

---

## 4. Mapa concreto: o que o jazz já tem (file:line)

| capacidade SOTA | no jazz | status |
|---|---|---|
| laço agente (modelo) | agent.go:385 `Run`, tool loop ~800-880 | feito |
| kernel offline (laço sem modelo) | brain.go:41 `brRun` → :258 `brExecute` (Pyhop-lite) | feito — **único do mercado** |
| NLU→intenção+slots sem LLM | nlu.go:1-27 pipeline, :601 `spot` (Aho-Corasick+fuzzy); nlu/intents.json 50 intents/644 ex.; nlu/lexicon.json; nlu/blind_test.json | feito |
| métodos c/ pré-cond+verificador | methods.go:60 `mhMethod`, :141 `mhApplicable`; methods/*.json (49 arquivos, 103 métodos) | feito |
| capacidades c/ risco/needs/undo | caps.go:67 `Capability`, :110 `cbRegistry`, :161 `cbAvailable` | feito |
| estado de tarefa + "unverified" | task.go:40 `tkTransitions`, :160 succeeded exige evidência | feito |
| recuperação por categoria | recover.go:53 `rvClassify`, :108 `rvTable`, :134 `rvDecide` | feito |
| orçamento de exploração | recover.go:191 `rvExplore` (3 looks sem fato novo→pergunta), :223 `Observe`; budget.go:58/:224 `exploreCap` | feito |
| fast paths regex→comando | intents.go:397 `Match`, intents.json embutido (:25) + override do usuário :285-287 | feito |
| menus determinísticos | offers.go:158 `olMenu`, :253 `olPick`, :568 `olWaitOffers` | feito |
| pergunta respondida pelo disco | askfacts.go:35 `askFromFacts` (branch/test/PM/exists) | feito |
| honest-done + testes protegidos | guard_honest.go:355 snapshot, :381 `judge`, :428 refusal; guarda anti-trapaça medida (benchmarks.md:32) | feito |
| quality gate | quality.go:148 `acQualityGate` → `qgCheck` | feito |
| verificação objetiva (--until) | agent_loop.go:196 `acUntilCheck`, :78-90 `/goal` | feito |
| review em contexto fresco | agent_loop.go:184; verifier 2º modelo judge_hedge.go | feito |
| loop detector + spiral breaker | agent.go:842-869 (detecção no laço), :968 `hashCall`, :978 `acLoopLen` (ciclos ABAB); blockux.go via :813 | feito |
| handoff/discard determinístico | agent_recover.go (nota ~150B/ferramenta, escada Shepherd) | feito |
| checkpoints shadow-git | checkpoint.go:257 `Snapshot`, :367 `Restore`, :466 `cpAfterBash`, :515 `cpRestore` | feito |
| plan cache + hindsight | plancache.go:244 `pcMatch` (BM25 k≤3), :305 `pcRender` ≤600tok, :433 `pcStore`, :561 `hdWrite`; evict por fails | feito |
| prefetch lexical | prefetch.go:34 `acPrefetchNote` (default off) | feito |
| truncamento head/tail + spill | truncate.go:121 `truncateHeadTail`, :28 `spill` | feito |
| roteamento UCB1 + hedge + fusão | judge.go, judge_hedge.go, judge_fusion.go (medido em `omni-judge.md`) | feito |
| classificação S/M/L + 11 tipos | route_task.go:185 `rtClassify`; budget.go:147 `bgClassify` | feito |
| índice de nomes/símbolos | nameindex.go:92 `niGet`, :516 `resolve`; usado offline methods.go:1436 | feito |
| parsers de saída | outparse.go (go/pytest/unittest/jest/cargo/build/trace) | feito |
| edição tipada p/ modelo | edit.go + guardas (apply verificado, diff guard) | feito |
| sandbox bwrap | sandbox*.go, `sbCurrent` | feito |
| eval offline c/ métricas | evaloffline.go:119 `cmdEval`, :171 `eoRun`; evals/offline/tasks.json **75 tarefas**: no_llm_rate, false_success, false_blocks, p50/p95, RSS | feito |
| lições de runs medidas | improve.go:280 `imExtract`; lessons.go:191 `lsPromptBlock` | feito |
| subagente `task` (research/worker) | subagent.go:45 `taskSpec`, :75/:85 prompts, saMaxItems:99 | feito (só com modelo) |
| modo offline interativo+headless | offline.go:342 `olBefore`, :567 `olHeadless`, :592 `olHeadlessFallback`, :162 `olProbe` | feito |

### O que falta / é parcial (o gap real)

| # | lacuna | estado hoje | consequência |
|---|---|---|---|
| G1 | **localização hierárquica determinística** (Agentless sem LLM) | nameindex resolve nome citado; test_context mostra falha c/ contexto (methods.go:1408). **Sem** ranking de suspeito por sinais combinados, sem co-change | o brain e o modelo começam a procurar do zero toda vez; fix.diagnose entrega falha mas não o arquivo mais provável |
| G2 | **reparo sem LLM** | methods/fix.json: fix.diagnose roda testes + `test_context` e para — o `doc` do próprio método declara: *"o reparo em si vai pelo modelo — reparar código exige raciocínio, não é mecânico"* | a fronteira do offline é justamente onde ninguém mais está — com tudo (outparse+índice+verify+undo) já pago; a afirmação é verdade no geral, mas não para classes mecânicas (ver proposta 2) |
| G3 | seleção/test-time barato | handoff+hindsight existem; sem re-execução por assinatura de flake, sem comparação de evidências entre tentativas | flake vira failed; uma segunda tentativa só existe via LLM |
| G4 | verifier registry por domínio | brVerify = switch fixo de 11 checks (methods.go:1680-1760) | "unverified" honesto, mas domínios novos exigem código |
| G5 | métodos candidatos aprendidos (spec §7: run verificada → método candidato → promoção) | plancache guarda template; **não vira método executável** | o offline não cresce sozinho com uso |
| G6 | contexto estrutural p/ o modelo (env card) | prefetch só grep de identificadores, off; fatos envprobe só no brain | modelo grátis gasta 2-3 passos descobrindo stack/test cmd que já está medido |
| G7 | resume de tarefa entre modos/sessões | TaskState grava eventos na sessão JSONL (task.go:256 `tkSessionSink`) mas não é retomável; handoff só dentro do turno | tarefa que bateu em needs_input/offline morre; modelo volta e ninguém retoma |
| G8 | flywheel de cobertura NLU | dataset+blind test existem; misses em produção não viram candidatos | cobertura estagnada no que o autor previu |
| G9 | no_llm_rate em produção | só no eval (evaloffline.go:98) | a métrica que define o produto não existe fora de teste |
| G10 | recipes declarativas (A3) | só métodos embutidos; `jazz run <recipe>` não existe | tarefa rotineira composta ainda precisa de modelo |
| G11 | edição tipada do brain | `file_op` = create/write/append/mkdir/delete/rename (methods.go:512 `brFileOp`); sem str_replace c/ confirmação de diff | reparo mecânico (G2) precisa de um edit seguro |
| G12 | contexto p/ modelo: esquecimento seletivo | compactação existe (summarize); sem drop por evento tipo condenser | ok para janelas atuais; dívida futura |

### Onde o jazz pode ser MELHOR que todos (diferencial defensável)
- **B1. Resolver+verificar com 0 tokens** — infra completa já paga; só falta reparo mecânico (G2).
- **B2. Um ativo, dois modos** — localização determinística serve o brain offline E vira system-reminder para o modelo online (compile-time intelligence na veia).
- **B3. Parar cedo** — pré-condição falha → blocked em ms, sem nenhuma chamada. Ninguém cobra essa economia porque ninguém mede custo de exploração falha.
- **B4. no_llm_rate como métrica de produto** — ninguém mais consegue medir porque ninguém mais tem o caminho.
- **B5. Handshake offline→online** — a tarefa começa offline (grátis, imediato), acumula evidência e entrega ao modelo quando volta — em vez de morrer no "sem rede".

---

## 5. Propostas (ordenadas por impacto/esforço)

Cada uma: mecanismo, arquivos, custo no Dell, métrica, impacto (1–5), esforço (S/M/L), risco.
Métricas = as do eval (no_llm_rate, sucesso verificado, false_success, false_blocks,
perguntas necessárias, comandos/tarefa, p50/p95, RSS) + as de produção (P9).

### 1. `localize` — localização hierárquica determinística — **impacto 5, esforço M**
**Mecanismo:** capacidade nova no registry com pipeline de 3 estágios:
(a) arquivos suspeitos — score sobre: identificadores/paths/quoted do pedido
(nameindex resolve + rg -l count), frames de traceback/falhas do outparse
(já em `brTestRes`), arquivo de teste↔implementação (niCore stem match),
co-change git (`git log --name-only -n 400` → co-ocorrência com candidatos
já pontuados; Zimmermann ICSE'04 — top-3 64%) e churn (Nagappan&Ball ICSE'05);
(b) elementos — tabela de símbolos do índice dentro dos top arquivos;
(c) linhas — `rg -n` no escopo e frames diretos. Saída ≤ 400 tok:
`path:line [sinais]` top-3, sempre com aviso "verificar antes de editar".
**Consumidores:** methods/fix.json e run_tests (contexto antes da falha),
`brResolve`, e a mesma nota injetada no 1º turno do modelo (B2).
**Arquivos:** `localize.go` (~350 LOC), caps.go (+cap), methods/*.json
(fix, run_tests), gitindex.go (reusa listagem), `git log` via runCmd read-only.
**Custo Dell:** índice já em RAM; 1-2 rg com --max-count + 1 git log bounded;
< 80 ms total, ~0 MB extra.
**Métrica:** hit@1/hit@5 do arquivo-alvo em fixtures com bug plantado (nova
categoria no tasks.json); comandos/tarefa; steps-to-first-relevant-read online.
**Risco:** médio — ranking errado ancora errado. Mitigação: top-3 (nunca 1),
rótulo "pode estar incompleto" como no prefetch; falha de localize não bloqueia
o método (best-effort).

### 2. `fix.mechanical` — reparo sem LLM para classes de falha frequentes — **impacto 5, esforço M**
**Mecanismo:** capacidade `patch_suggest` gera candidatos de patch **por regra
sobre a falha parseada** (outparse já entrega arquivo:linha:mensagem):
(a) `undefined|NameError|no such symbol` → símbolo mais próximo no índice
(niEnvScore/Jaro) → import/renomear uso; (b) assertion `expected X got Y`
em literal **só quando o teste é de caracterização gerado pelo snapshot_cmd**
ou marcado — nunca em teste protegido (hgKind decide, não heurística);
(c) identificador typo → nearest def a distância ≤2; (d) campo de versão.
Aplica via edit tipado novo do brain (`str_replace` com old/new + diff de
confirmação — G11), depois **verify obrigatório** (`tests_passed` do método).
Sem verificador rodando depois → não aplica (honestidade > conveniência).
Só roda offline/forçado ou quando não há candidato com credencial — com modelo
disponível o fix.diagnose atual continua preferido (modelo > regra).
**Arquivos:** `repair.go` (~300 LOC), `edit.go`-like `brStrReplace` (~80),
caps.go, methods/fix.json (+`fix.mechanical` priority 15 > diagnose 10),
recover.go (rvFixFailingTest encadeia mecânico→diagnose→blocked).
**Custo Dell:** 0 tokens; regex + índice, < 50 ms por candidato; ≤3 candidatos.
**Métrica:** no_llm_rate na nova categoria "bugfix mecânico" (5-8 tarefas novas
no eval); resgate falha→verificado; **false_success deve ficar 0** (verify decide).
**Risco:** alto→controlado. Patch ruim vira `failed`/`unverified`, nunca
`succeeded`; o risco real é sugerir patch certo-parcial e gastar o budget —
cap 1 tentativa mecânica por tarefa.

### 3. `jazz stats` — no_llm_rate em produção — **impacto 4, esforço S**
**Mecanismo:** contador por sessão e agregado: turnos totais vs atendidos por
brain/fast-path/menu/script/level-0 (RunResult.Model já diz `"offline-brain"`,
tool `"brain"`, fast path registra `dbg("fastpath")`); gravado em
`dataDir()/stats.json` sob flock (padrão model-scores.json). `jazz stats`
mostra tabela PT: % sem-LLM por semana, por intent, comandos/tarefa; alimenta
sementes do judge e o `improve`.
**Arquivos:** `stats.go` (~120), headless.go/jsonui.go (stats no JSON), doctor line.
**Custo Dell:** 1 append JSON pequeno por turno; leitura sob demanda.
**Métrica:** cria a métrica `no_llm_rate` real; efeito indireto em todas as
demais (sem medir, não melhora).
**Risco:** nenhum.

### 4. Resume de tarefa cross-modo (offline→online, entre sessões) — **impacto 4, esforço M**
**Mecanismo:** quando a tarefa para em `needs_input`/`blocked`/`failed` por
falta de modelo (ou o usuário sai), gravar snapshot resumível
`{goal, intent, plan, evidence, failed_method, pending_question}` em
`.jazz/tasks/<id>.json` (o TaskState já emite eventos; falta o retomável).
Gatilhos: `olLeave` (modelo voltou → oferece "continuar tarefa X de onde parou:
evidências já colhidas"), `jazz -c`, e `-p` com novo exit code `needs_input` +
`--resume-task`. Continuação = handoff note determinística (formato
agent_recover.go) como system-reminder + plano restante; não re-executa passos
done.
**Arquivos:** `taskresume.go` (~200), task.go (marshal), brain.go (gravar
outcome em stop), offline.go (`olLeave` hook), headless.go.
**Custo Dell:** KBs de JSON por tarefa; 0 no caminho feliz.
**Métrica:** taxa de resgate (tarefas concluídas após retomada / que pararam);
false_success (retomada com evidência < re-fazer sem evidência); perguntas
repetidas (deve cair).
**Risco:** baixo-médio — snapshot velho mente (arquivo mudou) → re-verificar
fatos citados no resume antes de agir (1 stat por evidência, ms).

### 5. `jazz nlu mine` — flywheel de cobertura do NLU — **impacto 4, esforço M**
**Mecanismo:** subcomando que minera sessões JSONL: pedidos com intent `none`
ou confidence < 0.6, pedidos que caíram no modelo, redigitações do usuário
(mesmo alvo, outra forma — o olPick/feedback mostra a leitura aceita).
Cluster por bigramas+NB próprio → propõe (i) novos `examples`/`rules` para
nlu/intents.json, (ii) entradas de `slang`/`concepts` para lexicon.json,
(iii) frases para blind_test.json. Roda **na máquina de dev** (não no Dell) —
compile-time intelligence: o LLM forte forja os assets na mineração assistida;
o `--propose` imprime diff JSON, nunca auto-aplica. Gate: `intents_test` +
blind test deve passar; dataset accuracy reportada antes/depois.
**Arquivos:** `nlumine.go` (~300), main.go (subcomando), nlu/*_test.go (gate).
**Custo Dell:** zero (desenvolvimento); runtime afetado: +KBs de JSON embutido.
**Métrica:** cobertura entendida em corpus real (novo campo no relatório do
mine); no_llm_rate; perguntas/tarefa.
**Risco:** médio — overfit às frases vistas. Mitigação: blind_test rotativo e
separado, exemplos confirmados por menu têm peso 2×, regra nova exige ≥2 ocorrências.

### 6. `toolBudget` — saída dimensionada pela janela do modelo — **impacto 4, esforço S**
**Mecanismo:** implementar a recomendação #1 de `grep-is-all-you-need.md`:
`truncate.go` ganha `toolBudget(win)=clamp(0.08·W·3.5B,6KB,30KB)`; no handler
bash, `rg|grep|git grep` usa `truncateGrep` (≤5 hits/arquivo + `[+N more in M
files; full: <spill>]`) — o 30KB fixo atual come ⅓ de uma janela 32k em 2 greps.
**Arquivos:** truncate.go (+~60), tools.go (dispatch por 1ª palavra), config
`--tool-budget`.
**Custo Dell:** 0; menos bytes movidos.
**Métrica:** tokens de entrada/tarefa, compactions/tarefa, pass em janela ≤32k.
**Risco:** baixo — head+tail continua para não-rg; spill é fallback.

### 7. `jazz run <recipe>` — grafos declarativos replayáveis (A3 do tocomas) — **impacto 4, esforço M**
**Mecanismo:** `.jazz/recipes/*.json` + `~/.config/jazz-cli/recipes/`:
`{name, match[], slots{from: arg|regex|ask|cap}, steps[{id,cap,in,needs,on_fail}], verify}`.
Diferença-chave vs methods/*.json: **recipes são dados de usuário/projeto**
(trust gate), não biblioteca embutida; validação no load (acíclico, cap existe,
slot resolvível); cada step passa por `Permit`; `on_fail:"stop|brain"` entrega
ao brain com a evidência. Promoção: método verificado ≥2× em contextos
distintos → candidato a recipe (metade do §7 da spec — a outra metade fica
como proposta posterior de G5).
**Arquivos:** `recipe.go` (~250), main.go, caps.go (resolver nome→cap), trust.
**Custo Dell:** idêntico a métodos (ms); zero LLM quando todos os slots são
determinísticos.
**Métrica:** no_llm_rate em tarefas de rotina (release, bump, add-test);
conclusão verificada; LLM calls/tarefa alvo ≤1.
**Risco:** médio — recipe ruim. Mitigação: mesmo gate do plan cache
(2 wins/0 fails p/ promover; projeto recipes atrás de `jazz trust`).

### 8. `env_card` — snapshot estrutural pré-1ª chamada (lição Meta-Harness TB2) — **impacto 3, esforço S**
**Mecanismo:** reminder ≤400 tokens no 1º turno do modelo (ao lado de
`acPrefetchNote`, append-only→cache seguro): stack+test/build/lint/fmt
detectados (fpDetect já mede), git estado (branch/dirty/último commit),
top-level dirs, hosts/serviços relevantes do epProfile. Meta-Harness ganhou
7/89 tarefas TB2 com exatamente isso antes da 1ª chamada.
**Arquivos:** `envcard.go` (~120), agent.go (`Agent.Run` junto ao prefetch),
flag `--env-card` default off até A/B.
**Custo Dell:** reusa fpDetect+epProfile+1 git status com cap 2s; ~200 ms.
**Métrica:** steps até 1º read relevante, tokens/tarefa, pass em
terminal-tasks/v2.
**Risco:** baixo-médio — fato velho ancora. Mitigação: só fatos re-medidos no
turno (nunca cacheados entre turnos), ≤400 tok, "may be stale" no cabeçalho.

### 9. Verifier registry por domínio + anti-flake — **impacto 3, esforço S-M**
**Mecanismo:** brVerify vira registry nomeado (map `string→func`) — hoje um
switch de 11 cases. Novos checks: `file_contains` (regex em arquivo),
`symbol_defined` (nameindex), `cmd_output_matches`, `port_listens`,
`git_clean`. Anti-flake: falha com assinatura de flake (timeout, EADDRINUSE,
contagem de falhas diferente entre 2 runs) → 1 re-run com alvo menor antes de
`failed` (estende rvTimeout existente). Verificador desconhecido no método →
`unverified`, como hoje.
**Arquivos:** methods.go (brVerify→dispatch), `verifiers.go` (~150), recover.go.
**Custo Dell:** ms; re-run só em flake (segundos, raro).
**Métrica:** false_success (deve ficar 0 — mais checks cobrem mais métodos),
tarefas failed por flake, unverified→succeeded.
**Risco:** baixo.

### Não fazer (registro explícito)
- **Embeddings/vector DB** — sem ganho demonstrado para código, inviável em
  2 GB; lexical vence nos dados [S5, S11].
- **Supervisor LLM por tick / multi-agente** — 720 calls/h vs ~200 free cap;
  Shepherd: custo de supervisão excede o worker em tarefa curta [S4].
- **Best-of-n cego** — não fecha gap sem seletor [S4]; nosso seletor é execução.
- **tree-sitter/cgo** — quebra binário 386 estático; regex-símbolo já existe.
- **Resumo por LLM como gerente de contexto principal** — OpenHands paga 1
  chamada grande por condensação; append-only + truncate + handoff já cobrem.
- **Cloud handoff/VMs** — infra hospedada fora do escopo.

---

## Fontes

- [S1] Vals AI, SWE-bench Verified leaderboard — vals.ai/benchmarks/swebench (set/2026: custo, tokens e duração por tarefa; Opus 4.8 mini-SWE 88.6 vs Claude Code 85.8).
- [S2] tbench.ai leaderboard terminal-bench@2.0 (web.archive.org 2026-03-10): Forge 78.4, Droid 77.3, Simple Codex 75.1, Terminus-KIRA 74.7.
- [S3] Merrill, Shaw, Carlini et al., "Terminal-Bench: Benchmarking Agents on Hard, Realistic Tasks in Command Line Interfaces", arXiv 2601.11868 (89 tarefas, solução humana + testes abrangentes por tarefa; frontier <65%).
- [S4] Yu, Chong, Nandi…, Manning, "Shepherd: Programmable Meta-Agents via Reversible Agentic Execution Traces", arXiv 2605.10913 — ver `shepherd-meta-agents.md` (heurística de loop, escada inject→handoff→discard, App. D hindsight, best-of-5 não fecha gap).
- [S5] Sen et al. (PwC), "Is Grep All You Need? How Agent Harnesses Reshape Agentic Search", arXiv 2605.15184 — ver `grep-is-all-you-need.md` (mesmo modelo ±20 pts entre harnesses; grep inline vence vector inline 10/10; entrega em arquivo derruba).
- [S6] mini-SWE-agent: swebench.com/post-250820-mini-roulette + repo (bash-only, <100 linhas de laço, append-only, subprocess.run; README "simplest, smallest, most readable agent").
- [S7] Claude Code Docs, "Explore the context window" e "How Claude remembers" — code.claude.com/docs/en/context-window, /en/memory (compact: re-injeta CLAUDE.md, memória, plano e ≤5 arquivos recentes lidos do disco).
- [S8] Claude Code Docs, "How Claude Code uses prompt caching" — code.claude.com/docs/en/prompt-caching (camadas de cache; `/rewind` volta a prefixo cacheado; compact custa mais em sessão fria).
- [S9] openai/codex codex-rs: core/src/safety.rs (`assess_patch_safety`), tools/handlers/apply_patch.rs, PR #1705 (apply_patch sob sandbox, hardlink/TOCTOU) e #18296 (regressão sandbox).
- [S10] opencode.ai/docs/agents (build/plan/general/explore; permission ruleset por papel: "tools the LLM can't see, it can't call") + docs/tools.
- [S11] Aider repomap — aider.chat/docs/repomap.html e github.com/Aider-AI/aider `repomap.py` (PageRank personalizado, orçamento de tokens, multiplicadores por menção/chat); edit-formats por capacidade (whole/diff/udiff); auto-lint/test reflection.
- [S12] Yang, Jimenez, Wettig, Lieret, Yao, Narasimhan, Press, "SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering", arXiv 2405.15793 + docs/background/aci.md (ações compactas, feedback conciso, linter no edit, output vazio explícito).
- [S13] OpenHands SDK docs — docs.openhands.dev/sdk/arch/condenser e /arch/agent; "Context Condensation" blog (resumo preservando pontas; custo quadrático→linear); arXiv 2511.03690 (event-sourcing, security analyzer).
- [S14] Xia et al., "Agentless: Demystifying LLM-based Software Engineering Agents", arXiv 2407.01489 (localização hierárquica; N patches; seleção por testes de reprodução+regressão; 32% SWE-bench Lite).
- [S15] CodeMonkeys, "Scaling Test-Time Compute for Software Engineering", arXiv 2501.14723 (serial+parallel; votação por testes gerados; 57.4% a ~$2300; ensemble selection 66.2%).
- [S16] "Scaling Test-Time Compute for Agentic Coding", arXiv 2604.16529 (resumos de trajetória; RTV; Opus 70.9→77.6 Verified, 46.9→59.1 TB2).
- [S17] "SWE-Replay: Efficient Test-Time Scaling for SE Agents", arXiv 2601.22129 (ramificar de passos críticos; −17.4% custo).
- [S18] Zimmermann et al., "Mining Version Histories to Guide Software Changes", ICSE 2004 (co-change: top-3 64%); Nagappan & Ball, ICSE 2005 (churn prediz defeitos) — citados em `offline-brain-research.md` C.

*Nota de método: números de leaderboard em [S1]/[S2] são snapshots públicos e
variam com versão de harness/modelo; o que se aproveita aqui é a ordem de
magnitude e o padrão (custo/tarefa declarado, testes por tarefa), não o rank.*
