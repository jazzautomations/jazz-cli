# Reuso de plano e trajetória verificado: o que a literatura sustenta e o que o jazz deve forjar

**Escopo.** Este documento trata de uma coisa só: transformar uma trajetória que terminou **verificada** em um artefato reusável — plano, template, método — e executá-lo **sem LLM em runtime** no d610 (i686, 1 CPU, 2 GB, offline). Ele não é uma revisão geral de agentes. Cada paper abaixo foi lido em abstract + método + resultados + ablações, exceto onde marcado.

**Regra de ouro (do blueprint do asset-forge), usada como corte epistemológico:**

> Se o LLM volta ao caminho crítico em runtime, é **dependência**, não **asset**.

Três testes operacionais para classificar qualquer mecanismo:

1. **Teste do offline:** roda com `JAZZ_OFFLINE=1`, rede bloqueada e nenhum provider configurado?
2. **Teste do slot:** alguma etapa precisa de um LLM para resolver um slot, escolher um arquivo ou adaptar o plano ao estado atual? Se sim, é dependência (a menos que a resolução seja determinística: `nameindex`, `brPick`, `fpDetect`).
3. **Teste do gate:** o resultado pode ser aceito ou rejeitado por um verificador objetivo (exit code, `tests_passed`, `file_exists`, `script_exit_zero`) sem juiz-LLM? Se não, não entra no cache verificado.

**O que foi lido de verdade.** TOCOMAS (2609.37953), CoMemBench (2609.32192), AgentReuse (2512.21309), APC (2506.14852), AWM (2409.07429), Voyager (2305.16291), ExpeL (2308.10144), Reflexion (2303.11366), Generative Agents (2304.03442), AutoGuide (2403.08978), TravelPlanner (2402.01622), LLM+P (2304.11477), AgentRR (2505.17716), LOOP (2605.14237), TVCache (2602.10986), SAGA (2610.06964) e o survey de skill libraries dinâmicas (2607.10113). Para o LOOP, o HTML não existe; li o PDF. Para CoMemBench, o HTML v2. Onde a evidência é fraca (n pequeno, sem sementes, auto-relato), está dito na ficha.

**Convenção de citação.** Todo paper é identificado por `arXiv id` + título completo + ano na sua ficha (§2.x) e na referência (§11); as citações inline repetem o id e o ano, e a primeira menção de cada seção traz o título curto. Isso evita repetir o título a cada frase sem perder a rastreabilidade.

**O que este documento não faz.** Não inventa números, não trata benchmark de outro domínio como prova de ganho no jazz, e não chama de "transferível" nada que exija embedding, GPU ou juiz-LLM em runtime.

---

## 1. Síntese em uma tabela

| Sistema (arXiv, ano) | O que guarda | Como casa | Como verifica | Promoção | Poda | Número-chave |
|---|---|---|---|---|---|---|
| **TOCOMAS** 2609.37953, 2026 | regiões de responsabilidade, handoffs, memória | topologia + afinidade 0.45–0.60 | checks estruturais + reward | só se bate o pai | evolução 1 mudança/tarefa | WorkBench 77.68 vs 62.17 (ADAS) |
| **CoMemBench** 2609.32192, 2026 | benchmark de fronteiras de memória | — | avaliadores nativos | — | — | 800 workflows, 800 desafios de isolamento |
| **AgentReuse** 2512.21309, 2025 | plano estruturado por intenção | intenção + parâmetros removidos + similaridade | resposta igual à do agente | imediata (cache) | — | 93% de reuso efetivo; F1 0.9718 |
| **APC** 2506.14852, 2025 | template de plano | keyword exata (GPT-4o-mini) | execução concluída | imediata | LRU + capacidade | −50,31% custo, −27,28% latência |
| **AWM** 2409.07429, 2024 | workflow abstrato (sub-rotina) | recuperação de workflows | avaliador do benchmark | offline ou online | memória crescente | +24,6% / +51,1% SR relativo |
| **Voyager** 2305.16291, 2023 | skill = código executável | embedding da descrição, top-5 | self-verification (LLM) | só após verificação | biblioteca cresce | 3,3× itens únicos |
| **ExpeL** 2308.10144, 2023 | insights + trajetórias de sucesso | similaridade de tarefa (kNN) | sucesso da tarefa | contagem de votos | remove insight com contagem 0 | ALFWorld 50 vs 55 (insight vs retrieve) |
| **Reflexion** 2303.11366, 2023 | reflexão verbal episódica | tarefa corrente (mesma tarefa) | sucesso na retentativa | imediata | memória = últimas 3 | 91% pass@1 HumanEval vs 80% |
| **Generative Agents** 2304.03442, 2023 | memory stream + reflexões | recência × importância × relevância | — | — | — | decay 0.995/h; α=1 nos três |
| **AutoGuide** 2403.08978, 2024 | guideline condicionada a contexto | contexto da trajetória + top-k | sucesso no benchmark | offline | — | WebArena 47,1% vs ExpeL 21,8% |
| **TravelPlanner** 2402.01622, 2024 | — (benchmark) | — | avaliador de restrições | — | — | GPT-4: 0,6% de sucesso |
| **LLM+P** 2304.11477, 2023 | plano PDDL | — | planejador clássico (som/completo) | — | — | Blocksworld 15→90% |
| **AgentRR** 2505.17716, 2025 | experiência multi-nível + check functions | nível adequado ao contexto | check function (código) | — (paper de visão) | — | só estudo de caso |
| **LOOP** 2605.14237, 2026 | skill branch-free parametrizada | template | replay determinístico | após validação | remoção manual (`--remove`) | 99% sucesso, 8,7× latência |
| **TVCache** 2602.10986, 2026 | grafo de chamadas de ferramenta | longest-prefix + estado | estado idêntico | — | snapshot seletivo | até 70% hit, 6,9× |
| **SAGA** 2610.06964, 2026 | episódios → procedimentos → princípios | k=3 por nível | evidence-grounding checker | evidência de ≥2 tipos de tarefa | — | trajetória crua 63,7 vs sem memória 48,4 |
| **Survey** 2607.10113, 2026 | 124 papers, ciclo de vida | — | — | admissão é o mecanismo | Prune/Merge/Rerank | retrieval plano degrada com dezenas–centenas |

---

## 2. Fichas técnicas

### 2.1 TOCOMAS — *Topological Coherence for Self-evolving Multi-agent Systems* (arXiv 2609.37953, 2026)

**Método.** Recebe um grafo de tarefa `T=(U,D)` **já dado**; mapeia cada nó para um pacote mínimo de ferramentas; funde nós em "regiões de responsabilidade" por afinidade (0,45 fixo; pico em 0,60) exigindo que o grafo quociente continue **acíclico**; deriva handoffs só onde uma dependência cruza donos diferentes; memória com visibilidade por região; evolução online propõe **uma** mudança estrutural por tarefa e a mantém apenas se passar checks estruturais e a recompensa bater a do pai (`R = Metrics − β·Cost`).

**Números.** Qwen3.8-27B: BBEH 54,78; WorkBench 77,68; SWE-bench Verified 48,00; CoMemBench SR 14,30. DeepSeek-V4-Flash: 55,20 / 61,45 / 49,00 / 14,00. Chamada direta ao LLM: 19,13 / 19,28 / 9,80 / 0,00 (Tabela 1).

**A ablação que importa.** (Tabela 2) sem organização topológica: VNCR −14,33; com handoffs fixos em vez de derivados: VHS −40,85 e CoMemBench SR **0**; com memória globalmente visível: ICS −19,96; sem evolução: WorkBench −6,52. O orçamento de memória **k=3 é o pico**; k=8 piora — mais contexto não é melhor contexto.

**Limitação.** O grafo de tarefa é dado: a decomposição, a parte difícil, está fora do método. CoMemBench é dos mesmos autores. Não há sementes, variância nem intervalos de confiança; a linha de base de chamada direta em SWE-bench (9,8%) é implausivelmente baixa para um modelo de 27B com ferramentas. É multi-agente; nada disso roda em 1 CPU.

**O que sobra para o jazz.** Três ideias, nenhuma delas a arquitetura: (a) **screen estrutural** com aciclicidade e cobertura de ferramentas; (b) **promoção só se bater o pai**; (c) **k=3** e escopo limitado de contexto. A "coerência topológica" é, na prática, agrupamento guloso + controle de acesso.

### 2.2 CoMemBench — *Benchmarking Collaborative Memory Boundaries across Multi-Agent Workflow Topologies* (arXiv 2609.32192, 2026)

**Método.** 800 workflows em 4 domínios (ferramentas com estado, mudança de código em repositório, recuperação offline, matemática formal), 800 desafios de isolamento **casados** (uma variante limpa e uma poluída por workflow). Mede conclusão, progresso verificado por nó (VNCR), confiabilidade de handoff (VHS), robustez de isolamento (ICS) e custo em tokens. Portões de certificação incluem **aciclicidade fora das arestas de revisão declaradas**, igualdade entre controle e poluído, e o plano-ouro nunca entra no prompt.

**Números.** 800 workflows limpos + 800 desafios; 6 operadores de violação: *sibling crosswire*, *incompatible payload*, *unverified result*, *completed-as-pending*, *wrong entity*, *stale version*. O achado é o trade-off: contexto mais amplo melhora disponibilidade e **enfraquece isolamento**; o ranking dos sistemas muda com a topologia.

**A ablação que importa.** Não é uma ablação de método; é a construção de desafios casados que separa "faltou handoff" de "veio lixo". É o desenho experimental mais limpo da área para provar que reuso de artefato errado **muda o comportamento verificado**.

**Limitação.** É benchmark, não método; mesmos autores do TOCOMAS; workflows sintéticos a partir de grafos de fonte; multi-agente.

**O que sobra.** Os 6 operadores são uma **checklist pronta** para os modos de falha de reuso de plano no jazz: versão obsoleta, resultado não verificado, entidade errada, payload incompatível, etapa já concluída e ramo cruzado. Cada um vira um teste em `planscreen_test.go`.

### 2.3 AgentReuse — *A Plan Reuse Mechanism for LLM-Driven Agent* (arXiv 2512.21309, 2025)

**Método.** Classifica a intenção, **identifica e remove os parâmetros** da requisição (slot filling), e só então mede similaridade dentro da categoria de intenção (BERT-base-chinês + m3e-small), com limiar γ=0,75. Se casa, reusa o plano estruturado; se não, gera e armazena.

**Números.** ~30% das requisições reais são idênticas ou similares. F1 0,9718; acurácia 0,9459; **reuso efetivo 93%** (93 de 100 testes: 20 requisições × 5 repetições); latência −93,12%; custo extra de 23,489 ms/requisição e ~500 MB de VRAM.

**A ablação que importa.** `WithArgs` (sem extrair parâmetros) tem recall 0,8262 contra 0,9513 do AgentReuse. `OneIntent` (buscar em todo o cache, não só na intenção) fica 17,66% mais lento e perde precisão. No sweep de γ, subir o limiar aumenta precisão e derruba recall.

**Limitação.** Vinte requisições, domínio AIoT chinês, ground truth = "a resposta foi igual", sem held-out, e o casamento depende de embeddings que não cabem no d610. O 93% é um teto otimista, não uma taxa que se espera em código.

**O que sobra.** **Tirar os parâmetros antes de casar** é o achado mais subestimado da área e o mais barato de implementar em Go. Restringir a busca por intenção também.

### 2.4 APC — *Agentic Plan Caching* (arXiv 2506.14852, 2025; NeurIPS 2025)

**Método.** Ao fim de uma execução concluída, extrai um **template de plano** do log: filtro por regra (descarta raciocínio verboso) + filtro por LLM leve (remove nomes, números, contexto). Na requisição, um LLM leve extrai uma **keyword de intenção** e faz **match exato** contra o cache; um planner pequeno adapta o template ao contexto.

**Números.** −50,31% de custo e −27,28% de latência mantendo 96,61% do desempenho; overhead de 1,04% (1,31% no pior caso, hit rate zero). FinanceBench: hit rate 46% com 100 entradas; cache de 1 entrada → 2% de hit; 10 → 13%; 50 → 45%. GAIA: −76,42% de custo com −0,61% de acurácia.

**A ablação que importa.** **Full-history caching** (guardar a execução inteira em vez do template) cai para **72,00%** de acurácia contra **85,50%** do template, e custa mais ($1,99 vs $1,86). Cache semântico por similaridade de query tem muitos falsos positivos e degrada a acurácia nos hits. Match exato de keyword foi escolhido justamente para evitar falso positivo.

**Limitação.** Precisa de LLM na extração **e** na adaptação; só agentes Plan-Act; o espaço de keywords é rígido; não é offline.

**O que sobra.** Template filtrado > histórico completo; **match exato/keyword para precisão**; desligar o cache quando o hit rate é persistentemente baixo; LRU e capacidade importam. O resto é dependência disfarçada.

### 2.5 AWM — *Agent Workflow Memory* (arXiv 2409.07429, 2024)

**Método.** Induz **workflows** (sub-rotinas) de trajetórias: granularidade mais fina que a instrução ("buscar produto"), com valores específicos abstraídos em placeholders (`{product-name}`). Offline (induz de exemplos anotados antes do teste) ou online (induz das próprias predições julgadas corretas por um avaliador).

**Números.** +24,6% de SR relativo no Mind2Web e +51,1% no WebArena; em cross-task, cross-website e cross-domain, +8,9 a +14,0 pontos absolutos conforme o gap de distribuição cresce. No WebArena, o gap sobre o baseline chega a 22,5 pontos depois de dezenas de exemplos.

**A ablação que importa.** Workflow **abstrato** contra exemplos concretos recuperados (Synapse): +5,0 de element accuracy e +4,0 de step SR. Workflows aleatórios não trazem o ganho. O action F1 fica ligeiramente **pior**: seguir o workflow cegamente atrapalha quando o estado do ambiente diverge.

**Limitação.** A indução é por LLM; o modo online precisa de um avaliador; é DOM de web; aderência excessiva ao workflow custa ação.

**O que sobra.** Abstrair > recuperar exemplo cru; placeholders; e a arquitetura **offline induction** é exatamente o asset-forge. O modo online é dependência.

### 2.6 Voyager — *An Open-Ended Embodied Agent with LLMs* (arXiv 2305.16291, 2023)

**Método.** Biblioteca de skills como **código executável**, indexada pelo embedding da descrição; recuperação top-5; prompting iterativo com feedback do ambiente, erro de execução e **self-verification**; só comita a skill depois que a verificação confirma a tarefa.

**Números.** 3,3× mais itens únicos, 2,3× mais distância, tech tree de madeira 15,3× mais rápido; única a chegar ao diamante. Zero-shot em mundo novo: resolve todas as tarefas; baselines não resolvem nenhuma em 50 iterações.

**A ablação que importa.** Sem a skill library, ainda resolve os tiers iniciais, mas **nunca** chega ao diamante (N/A vs 102 iterações, 1/3 dos trials). A biblioteca também melhora o AutoGPT quando plugada — o artefato é portátil.

**Limitação.** A verificação é o próprio GPT-4 julgando; embeddings; Minecraft; um domínio. É o exemplo mais citado de "skill library" e o que menos prova sobre verificação objetiva.

**O que sobra.** Comitar só depois de verificado; artefato executável; composição de skills simples em complexas. A verificação, no jazz, tem que ser exit code — não um juiz.

### 2.7 ExpeL — *LLM Agents are Experiential Learners* (arXiv 2308.10144, 2023)

**Método.** Coleta experiências (com Reflexion para retentativa), extrai **insights** em linguagem natural com quatro operadores — `ADD`, `EDIT`, `UPVOTE`, `DOWNVOTE` — e uma contagem de importância (novo insight começa com 2; sobe com upvote/edit, desce com downvote, **é removido ao chegar a zero**). No teste, concatena os insights e recupera top-k trajetórias de sucesso por similaridade (Faiss + all-mpnet-base-v2).

**Números.** HotpotQA: 36% só insights vs 31% só recuperação; ALFWorld: 50% vs 55%; WebShop: 37% vs 38%. Transferência HotpotQA→FEVER: ReAct 63% → ExpeL Transfer 70%. No HotpotQA empata com Reflexion (40% vs 39%) **sem retentativas**.

**A ablação que importa.** Insights e recuperação são **complementares** e o ganho depende do domínio: raciocínio quer abstração, ação quer trajetória concreta. GPT-4 extrai melhor que GPT-3.5.

**Limitação.** Extração por GPT-4; embeddings; insights são texto livre, não executável; validação em 4 folds.

**O que sobra.** O **contador com remoção em zero** é uma política de poda determinística e baratíssima — o antecedente direto do win-rate com limite inferior. E guardar as duas formas (lição + trajetória) é melhor que escolher uma.

### 2.8 Reflexion — *Language Agents with Verbal Reinforcement Learning* (arXiv 2303.11366, 2023)

**Método.** Agente executa, avalia, gera **reflexão verbal** sobre a falha, guarda em memória episódica e retenta. Sem atualização de pesos.

**Números.** 91% pass@1 no HumanEval contra 80% do GPT-4; ALFWorld 130/134 tarefas; +22% ALFWorld, +20% HotpotQA, +11% HumanEval sobre baselines fortes.

**A ablação que importa.** Sem self-reflection e sem geração de testes, o ganho cai; a memória é truncada nas **últimas 3 reflexões** para não estourar contexto.

**Limitação.** Auto-avaliação; melhora **dentro** da tarefa, não entre tarefas; sem garantia formal.

**O que sobra.** k pequeno; falha vira texto curto e reusável. No jazz, a "reflexão" tem que ser produzida em tempo de forja (offline) e ancorada em `rvCat` + exit code, não em auto-julgamento.

### 2.9 Generative Agents — *Interactive Simulacra of Human Behavior* (arXiv 2304.03442, 2023)

**Método.** Memory stream com observações, reflexões e planos. Recuperação = `α_recency·recency + α_importance·importance + α_relevance·relevance`, todos normalizados em [0,1], α=1. Recência é decaimento exponencial (fator 0,995 por hora); importância é inteiro 1–10 **gerado pelo LLM**; relevância é cosseno de embeddings. Reflexão é disparada quando a soma de importância dos eventos recentes passa de 150.

**Números.** Não há número de tarefa; a avaliação é de "credibilidade" por entrevista. A ablação mostra que observação, planejamento e reflexão contribuem cada um de forma crítica.

**Limitação.** Importância por LLM e relevância por embedding: os dois componentes caros. É simulação social, não agente de código.

**O que sobra.** O **trio** recência/importância/relevância como prior de ranking — mas com proxies determinísticos: BM25 no lugar de embedding, idade no lugar de recência de acesso, utilidade verificada no lugar de importância julgada.

### 2.10 AutoGuide — *Automated Generation and Selection of Context-Aware Guidelines* (arXiv 2403.08978, 2024)

**Método.** A partir de **pares contrastivos** de trajetórias (sucesso vs falha), gera guidelines com estrutura condicional ("quando o contexto é X, faça Y"), identifica o contexto no teste e recupera só as guidelines aplicáveis (top-k).

**Números.** WebArena-Reddit: AutoGuide 47,1%, ExpeL 21,8%, ReAct 8,0%. ALFWorld e WebShop também lideram em reward e SR.

**A ablação que importa.** O ganho vem da **seleção por contexto**, não da quantidade de guidelines: o ExpeL injeta todas e se confunde com guidelines irrelevantes.

**Limitação.** Extração com GPT-4-turbo e identificação de contexto em runtime; os autores declaram que **não reportam variância**.

**O que sobra.** Recuperar por **condição de aplicabilidade**, não por similaridade global. No jazz isso é literal: o plano carrega `pre` (fatos) e só é injetado se `mhApplicable` passar.

### 2.11 TravelPlanner — *A Benchmark for Real-World Planning with Language Agents* (arXiv 2402.01622, 2024)

**Método.** 1.225 intenções de viagem, ~4 milhões de registros, 6 ferramentas, restrições de ambiente, de senso comum e hard; avaliação por scripts, não por resposta única.

**Números.** **GPT-4 acerta 0,6%** das tarefas com todas as restrições; os outros LLMs testados não completam nenhuma. As falhas dominantes são erro de argumento na ferramenta, laço morto, alucinação e perda de restrição global.

**A ablação que importa.** Estratégias de planejamento (ReAct, Reflexion) não salvam: o problema é rastrear restrições e usar a ferramenta certa. Um plano plausível sem checagem de restrição é inútil.

**Limitação.** É benchmark, não método; domínio de viagem.

**O que sobra.** Duas exigências para qualquer cache de plano: (a) o verificador precisa codificar as restrições **hard**, não só "terminou"; (b) erro de argumento é o modo de falha número um — logo, validação de slot é parte do screen, não luxo.

### 2.12 LLM+P — *Empowering LLMs with Optimal Planning Proficiency* (arXiv 2304.11477, 2023)

**Método.** LLM traduz linguagem natural para PDDL; um planejador clássico (som e completo) resolve; o LLM traduz de volta.

**Números.** Blocksworld 15% → 90%; Grippers 35% → 95%; Storage 0% → 85% (com um par problema/PDDL como contexto). Sem contexto, LLM+P cai a 0% nos sete domínios da Tabela I.

**A ablação que importa.** O contexto de tradução é decisivo; e a maior parte dos fracassos do LLM+P vem de **PDDL mal especificado** (condição inicial faltando), não do planejador.

**Limitação.** Exige modelo de domínio formal; o LLM continua no caminho crítico para traduzir.

**O que sobra.** Separar **propor** de **validar** com um checker sólido. E o aviso: ao mover a garantia para o checker, a falha migra para a **tradução/slot**. No jazz, é `resolve_name` e o preenchimento de `$slot.*` que passam a ser o ponto frágil — e é ali que se mede.

### 2.13 AgentRR — *LLM Agents with Record & Replay* (arXiv 2505.17716, 2025)

**Método.** Record → summary → replay. A experiência é **multi-nível**: baixo nível = sequência concreta de ações/API; alto nível = procedimento geral. Cada experiência ganha **check functions** que verificam integridade do fluxo, pré-condições de estado, restrições de parâmetro e invariantes; replay que viola pré-condição é bloqueado. Filosofia declarada: *bounded intelligence*.

**Números.** **Não há benchmark**: é um estudo de caso de preenchimento de formulário. Os autores dizem que o CUA da OpenAI leva ~3 min e ainda falha; não há taxa medida.

**A ablação que importa.** Não existe. O valor está no desenho: a check function rejeita uma chamada "tecnicamente possível" porque nenhuma trajetória válida mostrou aquela ordem.

**Limitação.** Paper de posição/visão com um caso; sem medição; web.

**O que sobra.** Check functions por experiência e a escada de níveis. Honestamente: é a **especificação** que o jazz deve implementar, não evidência de que funciona.

### 2.14 LOOP — *One-Shot Recording and Deterministic Replay* (arXiv 2605.14237, 2026)

**Método.** Primeira execução com LLM é interceptada e gravada. Um algoritmo **greedy length-descending** extrai templates: substitui substrings variáveis por `{{current_time}}`, `{{current_date}}`, `{{step_N_result}}`. O resultado é uma skill **branch-free** e parametrizada. Toda execução seguinte resolve variáveis e reproduz a sequência, **sem LLM**. Se qualquer etapa falhar, degrada para execução normal com LLM.

**Números.** 99% de sucesso combinado e 93,3%–99,98% de redução de tokens; latência 7,82 s → 0,90 s (8,7×), p99 16,7 s → 1,4 s, variância 13,2× menor; 100 replays com sequência de ferramentas 100% idêntica.

**A ablação que importa.** Não é uma ablação estatística; é a **tabela de degradação**: skill vazia, skill com `edit_file`, resultado com keyword de erro, skill sem `write_file`, timeout, exceção — em todos, cai para o LLM e **nunca trava**. O caso "contém `edit_file` → não gera skill" é a regra mais importante do paper para o jazz.

**Limitação.** 8 páginas, auto-relatado, tarefas periódicas estreitas, sem replicação independente. O "99%" é quase por construção: se o LLM sai do caminho, o determinismo sobe. A extração de template é frágil quando o formato da saída muda.

**O que sobra.** Replay **branch-free**, recusa de skills com edição, e escada de degradação. É o blueprint de engenharia mais próximo do que o jazz precisa — mas só para tarefas rotineiras.

### 2.15 TVCache — *A Stateful Tool-Value Cache* (arXiv 2602.10986, 2026)

**Método.** Grafo de chamadas de ferramenta (TCG); cada nó guarda chamada, resultado e, opcionalmente, snapshot do estado. Lookup por **longest-prefix match**; hit só quando a trajetória inteira casa (garantindo estado idêntico); em match parcial, forka o sandbox do último nó e executa só o resto. Snapshot só quando o custo de reexecutar supera o de guardar.

**Números.** Até 70% de hit rate, 6,9× de redução na mediana do tempo de ferramenta, sem degradação de reward no pós-treino.

**A ablação que importa.** O exemplo do paper: `cat foo.py`, patch, `cat foo.py` de novo. Um cache stateless devolve o valor **velho** na segunda leitura e degrada silenciosamente. Estado não é detalhe.

**Limitação.** É pós-treino de RL, com sandboxes; não é planejamento.

**O que sobra.** Duas coisas: **fingerprint de estado** antes de reusar, e **reuso de prefixo** (replay do prefixo read-only, reexecução da cauda). A segunda é diretamente implementável no jazz; não encontrei, na literatura que li, quem aplique reuso de prefixo a código.

### 2.16 SAGA — *Principles that Guide, Actions that Inform* (arXiv 2610.06964, 2026)

**Método.** Hierarquia de conhecimento: descrições episódicas → **procedimentos reutilizáveis** → **princípios** com condições explícitas de aplicabilidade, todos ligados à evidência de execução. Um *evidence-grounding checker* valida sintaxe de citação, integridade da fonte e links pai-filho; um princípio cross-task exige evidência de **≥2 tipos de tarefa**. Na execução, o princípio recuperado é **instanciado** em orientação específica e usado para gerar checks que regulam a ação.

**Números (ScienceWorld, média; Qwen3.8-Max / DeepSeek-v4-Flash).** Sem memória: 48,4 / 25,0. Trajetória crua (L0): **63,7 / 54,8**. Episódico+procedimentos (L1+L2): 64,5 / 55,0. L1+L2+L3 injetado direto: 63,9 / 54,5. L1+L2 + L3 **instanciado**: 66,2.

**A ablação que importa.** Três resultados que valem mais que o método: (1) **trajetória crua já entrega quase todo o ganho** (+15,3 e +29,8 pontos sobre não ter memória); (2) abstrair para procedimento quase não acrescenta (64,5 vs 63,7); (3) **despejar princípios diretamente piora** (63,9 < 64,5); só a instanciação recupera o valor. k=3 em cada nível.

**Limitação.** Abstração e instanciação por LLM; dois benchmarks de ambiente; sem código.

**O que sobra.** O critério de promoção "evidência de ≥2 tarefas" e a regra "abstração só vale se for instanciada". Também o realismo: o cache de trajetória do jazz é a maior parte do valor; o Method Forge é ganho marginal **se** não for instanciado por tarefa.

### 2.17 Survey — *Dynamic Agent Skills: A Lifecycle Survey and Taxonomy* (arXiv 2607.10113, 2026)

**Método.** Auditoria de 124 papers (2023–2026) organizada em oito estágios de ciclo de vida (aquisição de evidência, proposta, verificação/admissão, armazenamento, recuperação/composição, manutenção, destilação/portabilidade, governança) e dez operadores (`Add`, `Refine`, `Merge`, `Split`, `Prune`, `Distill`, `Abstract`, `Compose`, `Rewrite`, `Rerank`).

**Números/afirmações.** Admissão e reparo são repetidamente importantes; a qualidade do verificador afeta materialmente o resultado; **retrieval plano degrada em bibliotecas de dezenas a centenas de skills** (o mecanismo é estável: distratores crescem mais rápido que utilidade); benchmarks subnotificam trajetória da biblioteca, gap uso-utilidade e superfícies de segurança. Cita o gate de rollback do PSN: reverter se o sucesso em 3 tarefas recentes cair mais de **20%**.

**Limitação.** É survey, com evidência graduada; não gera experimento novo.

**O que sobra.** O vocabulário e a ordem: implementar `Add`, `Prune`, `Rerank`, `Abstract` primeiro; guardar linhagem e rollback; medir uso vs utilidade, não só hit rate.

---

## 3. Transferível vs dependente de LLM

O corte é a regra de ouro, aplicada mecanismo por mecanismo.

| Mecanismo | Origem | Offline no d610? | Por quê |
|---|---|---|---|
| Remover parâmetros/slots antes de casar | AgentReuse 2512.21309 | **Sim** | regex + `nuGet().Parse` já existem; determinístico |
| Match exato de keyword/intenção | APC 2506.14852 | **Sim** | BM25 + NB (`nlu.go:1429`) sem embedding |
| Template filtrado (não histórico completo) | APC; AWM 2409.07429 | **Sim** | poda de passos read-only e generalização de path |
| Placeholders de slot em workflow | AWM 2409.07429 | **Sim** | `$slot.x` já é a linguagem de `methods/*.json` |
| Screen estrutural (DAG, cap existe, slot resolvível) | TOCOMAS 2609.37953; CoMemBench 2609.32192 | **Sim** | topo sort + `cbRegistry` + `brFacts` |
| Verificação objetiva antes de guardar | Voyager 2305.16291 (a ideia, não o juiz) | **Sim** | `--until`, `brVerify` (`methods.go:1680`) |
| Contador de votos com remoção em zero | ExpeL 2308.10144 | **Sim** | inteiros; vira Wilson LB |
| Poda por utilidade e capacidade | Survey 2607.10113; APC | **Sim** | O(n) por turno |
| Fingerprint de estado antes do replay | TVCache 2602.10986 | **Sim** | git HEAD + hash de dirty + versões |
| Replay branch-free de passos read-only | LOOP 2605.14237 | **Sim** | `brCap` + `permit()`; sem LLM |
| Prefixo read-only reusado, cauda reexecutada | TVCache 2602.10986 | **Sim** | longest-prefix sobre o IR |
| Escada de degradação (replay → método → modelo → perguntar) | LOOP; AgentRR 2505.17716 | **Sim** | já é a filosofia de `recover.go` |
| k=3 | TOCOMAS; SAGA; Reflexion | **Sim** | constante |
| Guidelines condicionadas a contexto | AutoGuide 2403.08978 | **Parcial** | a *seleção* por `pre` é offline; a *geração* não |
| Reflexão sobre falha | Reflexion 2303.11366 | **Parcial** | só em tempo de forja; em runtime, `rvCat` |
| Abstração episódio→procedimento→princípio | SAGA 2610.06964 | **Parcial** | o artefato é offline; a instanciação precisa ser determinística |
| **Extração de keyword por LLM** | APC | **Não** | LLM em runtime |
| **Planner pequeno que adapta o template** | APC | **Não** | LLM em runtime |
| **Extração de insights/guidelines por LLM** | ExpeL; AutoGuide | **Não** (runtime) / **Sim** (forja) | só offline-time |
| **Indução online de workflow** | AWM | **Não** | precisa de avaliador + LLM por tarefa |
| **Embeddings / vector store** | AgentReuse; Voyager; ExpeL; Generative Agents | **Não** | 500 MB de VRAM no AgentReuse; CPU/memória do d610 |
| **Self-verification por LLM** | Voyager; Reflexion | **Não** | juiz caro e não confiável; viola o teste do gate |
| **Importância 1–10 julgada por LLM** | Generative Agents | **Não** | substituível por utilidade verificada |
| **Cache semântico de resposta** | baselines do APC | **Não** | falsos positivos degradam acurácia |
| **Histórico completo como contexto** | APC | **Não** | 72,00% vs 85,50% e mais caro |
| **Topologia multi-agente / regiões** | TOCOMAS | **Não** | multiplica chamadas; grafo de tarefa não existe no jazz |
| **Evolução estrutural online com reward de 1 run** | TOCOMAS | **Não** | ruído; 2× chamadas |
| **Replay de trajetória com edição arbitrária** | LOOP recusa | **Não** | conteúdo do edit não é parametrizável |

---

## 4. As nove lições que sobrevivem ao corte

Cada lição vem com o número que a sustenta e a consequência para o jazz.

**L1. Trajetória crua é a maior parte do ganho.** SAGA: 48,4 → 63,7 (Qwen) e 25,0 → 54,8 (DeepSeek) só recuperando episódios, antes de qualquer abstração (arXiv 2610.06964, 2026). Consequência: o `plancache.go` atual, que guarda um template de trajetória, está mais perto do que rende do que qualquer "method library" sofisticada. Não mate o cache para construir a forja.

**L2. Abstração sem instanciação pode piorar.** SAGA: L1+L2 = 64,5; injetar princípios direto = 63,9; instanciar = 66,2. AWM mostra o mesmo por outro ângulo: workflow abstrato ganha de exemplo concreto em element accuracy (+5,0), mas o action F1 piora porque o agente segue o workflow quando devia divergir. Consequência: um método forjado precisa de **precondições e slots revalidados no estado atual**; senão é texto bonito que atrapalha.

**L3. Template > histórico completo.** APC: 85,50% vs 72,00% no FinanceBench, com custo maior no histórico completo (arXiv 2506.14852, 2025). Consequência: não injete a trajetória inteira; o `pcBuild` já descarta exploração read-only — mantenha e reforce.

**L4. Tire os parâmetros antes de casar.** AgentReuse: recall 0,9513 com extração de parâmetros vs 0,8262 sem (arXiv 2512.21309, 2025). Consequência: `pcTerms` tokeniza a requisição inteira — inclusive os componentes de path, porque o split é por não-letra/dígito — e `pcDocTerms` (`plancache.go:233`) acrescenta o nome-base dos arquivos; nenhum dos dois remove o **valor** do slot. Duas tarefas iguais em arquivos diferentes parecem distintas, e duas tarefas diferentes no mesmo arquivo parecem iguais. É o ajuste de melhor relação ganho/esforço do documento.

**L5. Para precisão, match exato/keyword; similaridade de texto é fábrica de falso positivo.** APC escolheu match exato de keyword justamente para evitar falso positivo; cache semântico degrada a acurácia nos hits. Consequência: nada de embeddings; BM25 + intenção + shape.

**L6. k=3.** TOCOMAS: k=3 é o pico, k=8 declina (arXiv 2609.37953, 2026). SAGA usa k=3 por nível; Reflexion trunca em 3 reflexões. Consequência: `pcMaxInject = 3` está certo; não aumente "porque cabe no contexto".

**L7. Verificação objetiva é a fronteira entre reuso e alucinação.** TravelPlanner: GPT-4 em 0,6% quando o critério é restrição real (arXiv 2402.01622, 2024). Voyager só comita skill após verificação — mas a verificação é um LLM, e é aí que o paper é fraco. LLM+P mostra o padrão certo: um checker sólido (planejador) e o LLM só traduz. Consequência: `verified` no jazz tem que significar exit 0, `tests_passed` ou `file_exists`; nunca "o modelo disse".

**L8. Admissão e poda são o mecanismo de seleção.** O survey de 124 papers coloca admissão/reparo como padrão recorrente e mostra que retrieval plano degrada em dezenas–centenas de skills (arXiv 2607.10113, 2026). ExpeL já tinha o contador com remoção em zero. Consequência: `pcEvictFails = 2` é uma política cega; troque por limite inferior de Wilson + uso + idade.

**L9. Estado importa.** TVCache: cache stateless devolve leitura velha e degrada em silêncio (arXiv 2602.10986, 2026); CoMemBench tem o operador *stale version* como violação canônica (arXiv 2609.32192, 2026). Consequência: um plano sem fingerprint do repositório não é um asset, é uma aposta.

---

## 5. Mapeamento no jazz (file:line)

### 5.1 O que existe

| Capacidade | Onde | Estado |
|---|---|---|
| Estrutura do plano (task, keys, files, steps, dead, verify, wins, fails) | `plancache.go:56-73` (`pcPlan`) | **Feito** |
| Persistência atômica por projeto, 0600, cap 100 | `plancache.go:105-158` (`pcPath`, `pcLoad`, `pcSave`) | **Feito** |
| Tokenização PT/EN, fold de acento, stemming leve | `plancache.go:162-229` (`pcTerms`) | **Feito** |
| BM25 puro em Go, filtro de cobertura ≥40%, bônus por wins | `plancache.go:244-301` (`pcMatch`) | **Feito** |
| Render ≤ 2100 chars, no máximo 3 planos, hint só no primeiro | `plancache.go:305-337` (`pcRender`) | **Feito** |
| Injeção depois da mensagem do usuário (histórico append-only) | `agent.go:463-466`, `plancache.go:343-364` (`pcInject`) | **Feito** |
| Contabilidade win/fail e evicção em 2 falhas | `plancache.go:367-429` (`pcFailed`, `pcScore`) | **Feito** |
| Dedup por Jaccard ≥0,7, mantém a trajetória mais curta | `plancache.go:433-474` (`pcStore`, `pcJaccard`) | **Feito** |
| Extração de template: descarta leitura, funde repetição, coleta becos sem saída | `plancache.go:479-552` (`pcBuild`) | **Feito (parcial)** |
| Hindsight opcional por LLM forte, ≤1500 chars | `plancache.go:561-653` (`hdWrite`, `hdStrongRef`) | **Feito** |
| Match por classe de tarefa + assinatura de repo, primeira volta | `lessons_reuse.go:28-152` (`lrTaskClass`, `lrRepoSig`, `lrPick`, `lrHint`) | **Feito** |
| Anti-envenenamento de plano/lição (regex + injScan) | `hardening2.go:958-986` (`h2PoisonReason`, `h2PlanPoison`) | **Feito** |
| Estado da volta: `untilPassed`, `gateOK`, `verifyOK`, `verifyCmd`, log de 80 eventos | `agent_recover.go:84-116` (`rcTurn`, `rcState`) | **Feito** |
| Classificação de falha por categoria observável | `recover.go:53-81` (`rvClassify`) | **Feito** |
| Tabela de recuperação com limites por categoria | `recover.go:108-149` (`rvTable`, `rvDecide`) | **Feito** |
| Orçamento de exploração (3 leituras sem fato novo) | `recover.go:181-236` (`rvExplore`, `rvFactKey`) | **Feito** |
| Métodos HTN leves, pré-condições, prioridade, verificação | `methods.go:60-153` (`mhMethod`, `mhHolds`, `mhApplicable`) | **Feito** |
| Loader embutido + validação de comando read-only | `methods.go:81-122` (`mhLibrary`, `mhBadCmd`) | **Feito** |
| Executor Pyhop com recuperação e próximo método | `brain.go:261-424` (`brExecute`) | **Feito** |
| Fatos do ambiente (`slot.*`, `project.*`, `bin.*`, `env.online`) | `brain.go:225-257` (`brFacts`) | **Feito** |
| Verificadores objetivos (`tests_passed`, `exit_zero`, `file_exists`, …) | `methods.go:1680-1762` (`brVerify`) | **Feito** |
| Intenção → métodos | `brain.go:130-188` (`brRunIntent`) | **Feito** |
| Níveis 0 (read-only sem modelo) | `brain.go:579-700` (`brLevel0Intents`, `brHeadlessLevel0`, `brLevel0Interactive`) | **Feito** |
| Capacidades registradas e disponibilidade por perfil | `caps.go:110-197` (`cbRegistry`, `cbAvailable`) | **Feito** |
| Allowlist de comandos read-only | `caps.go:573-608` (`cbReadCmds`, `cbReadCmd`) | **Feito** |
| Permissão única para shell/escrita | `agent.go:1618` (`permit`), `offline.go:290` (`PermitCommand`) | **Feito** |
| Harness offline com no_llm_rate, false_success, cmds/tarefa, p50/p95, RSS | `evaloffline.go:91-107`, `279` (`eoSummary`, `eoOne`) | **Feito** |

### 5.2 O buraco central

**O cache de plano hoje só existe para turnos do modelo.** `acTurnDone` é chamado em `agent.go:409`, no loop do agente com LLM; `pcInject` em `agent.go:466`. O cérebro offline (`brRunIntent`, `brain.go:130`; `brHeadless`, `brain.go:500`) **não consulta `pcMatch` nem grava `pcStore`**. Além disso, `pcMinSteps = 12` (`plancache.go:42`) conta passos de modelo, e uma tarefa do cérebro tem zero. Resultado: o subsistema que deveria provar "reuso offline" é, por construção, um subsistema online.

Isso não é um detalhe de implementação; é a diferença entre o que o documento anterior propunha (A4: "injete planos como lembrete") e a meta atual ("reuso verificado que roda offline"). As propostas P6–P8 fecham esse buraco; P1–P5 tornam o artefato confiável o bastante para ser executado.

### 5.3 O que é só pesquisa

| Capacidade | Estado | Onde entraria |
|---|---|---|
| IR tipado de plano (cap, in, out, deps, efeito) | **Só pesquisado** | `planir.go` (novo), `pcBuild` |
| Screen estrutural (DAG, cap existe, slot resolvível) | **Só pesquisado** | `planscreen.go` (novo), `pcStore` |
| Fingerprint de estado do repo | **Parcial**: `lrRepoSig` é grosso (marcadores + extensões) | `planstate.go` (novo) |
| Matching por esqueleto/intenção/shape | **Parcial**: só BM25 textual | `pcTerms`, `pcMatch` |
| Poda por win-rate com limite inferior | **Parcial**: `Fails >= 2` | `pcScore`, `pcSave` |
| Replay de plano sem LLM | **Só pesquisado** | `planreplay.go` (novo) |
| Method Forge (plano → `mhMethod`) | **Só pesquisado** | `methodforge.go` (novo), `mhLibrary` |
| Gate held-out antes de promover | **Só pesquisado** | `plangate.go` (novo), `evaloffline.go` |
| Becos sem saída por categoria de recuperação | **Parcial**: texto livre em `Dead` | `pcBuild`, `recover.go` |
| Métricas de reuso no eval | **Parcial**: `eoSummary` não tem hit/warm | `evaloffline.go` |

---

## 6. Propostas

Dez propostas, todas implementáveis em Go puro, todas alimentando o cache verificado ou a forja. A ordem apresentada é a ordem de **dependência**, não de impacto; a ordem de ataque está no §7.

Convenções de custo: "d610" = i686, 1 CPU, 2 GB, binário 386. `int`/ponteiro = 4 B. Nenhuma proposta adiciona dependência externa; tudo é `encoding/json`, `sort`, `math`, `hash/fnv`, `crypto/sha256` — já no go.mod indireto via stdlib.

### 6.0 Definições de métrica (valem para todas as propostas)

| Métrica | Definição operacional | Onde medir |
|---|---|---|
| `no_llm_rate` | requisições **respondidas** sem nenhuma chamada a provider, divididas pelo total. Atenção: `eoSummary.NoLLM` (`evaloffline.go:232-233`) conta também resposta bloqueada; para reuso, reportar separado `answered_no_llm` e `blocked_no_llm`, senão a taxa infla | `evaloffline.go:91` |
| `reuso efetivo %` | `reuse_wins / reuse_hits`, onde hit = turno em que um plano foi injetado **ou** executado e win = terminou com verificador objetivo exit 0. Reportar também `reuse_hits / turns` (cobertura) | novo campo em `eoSummary` |
| `comandos/tarefa` | comandos de shell executados por tarefa (`CmdsPerTask`), contando replay e métodos | `evaloffline.go:240` |
| `p50` | mediana da latência de parede por tarefa, **dentro** do processo (sem start de processo), separando frio e quente | `evaloffline.go:242` |
| `false_success` | tarefas declaradas `succeeded` sem verificador objetivo que tenha passado, dividido por N. Meta dura: **0** | `evaloffline.go:94` |
| `bad_reuse` | turno com plano injetado/executado que termina em falha objetiva ou em resultado diferente do esperado pelo avaliador | novo campo |

---

### P1 — IR tipado de plano (`piPlan`)

**Mecanismo.** Hoje `pcPlan.Steps` é `[]string` ("bash `go test ./...` (exit 0)"), inútil para executar. Introduzir um IR:

```go
type piStep struct {
    ID    string            `json:"id"`
    Cap   string            `json:"cap"`   // read_file, read_cmd, run_tests, proj_cmd, file_op, git_op
    In    map[string]string `json:"in"`    // "$slot.path", "$src", literal
    Out   string            `json:"out,omitempty"`
    Deps  []string          `json:"deps,omitempty"`
    Fx    string            `json:"fx"`    // read | write | exec
    Check []mhCheck         `json:"check,omitempty"`
}
type piPlan struct {
    pcPlan
    Intent string            `json:"intent,omitempty"`
    Shape  string            `json:"shape,omitempty"`  // tipos de slot ordenados: "file+num"
    Slots  map[string]piSlot `json:"slots,omitempty"`
    IR     []piStep          `json:"ir,omitempty"`
    State  piState           `json:"state,omitempty"`
    Grade  string            `json:"grade,omitempty"`  // A (replay) | B (hint)
}
```

Compilação a partir de `rc.log` (o log já existe, cap 80 em `agent_recover.go:35`): (1) manter **todos** os passos bem-sucedidos, inclusive leituras; (2) eliminação de código morto — descartar leitura cujo `Out` nenhum passo posterior consome; (3) dobrar repetições consecutivas; (4) mapear ferramenta→cap (`read`→`read_file`, `bash` read-only em `cbReadCmds`→`read_cmd`, bash de teste/build→`run_tests`/`proj_cmd`, `edit`/`write`→`file_op`); (5) renomear variáveis para `$s1`, `$s2`; (6) generalizar paths para `$slot.<tipo>` com o tipo vindo de `nuEntity`/extensão.

**Arquivos.** `planir.go` (novo); `plancache.go:479` (`pcBuild`) chama `piCompile(rc.log)`; `plancache.go:56` (`pcPlan` ganha `IR`, `Intent`, `Shape`, `Grade`); `plancache_test.go` (golden de compilação).

**Custo no d610.** ~40 B por passo em memória; 100 planos × 12 passos ≈ 50 KB; JSON +2–4 KB/plano; decodificação de 400 KB ≈ 3–8 ms no Pentium M — aceitável por turno, mas cachear em memória por (mtime, tamanho) para não repetir.

**Métrica.** Cobertura do IR (% de planos armazenados que compilam), passos por IR, taxa de replay bem-sucedido, false_success.

**Impacto 5 · Esforço M · Risco baixo.** Sem IR, P2, P6 e P7 não existem.

---

### P2 — Screen estrutural antes de guardar/promover

**Mecanismo.** Uma função pura `psScreen(p piPlan, facts map[string]string, reg []Capability) (grade string, why string)`. Dez checagens, derivadas do TOCOMAS (aciclicidade/cobertura), do CoMemBench (os 6 operadores) e do `mhBadCmd` existente:

1. **DAG:** `Deps` forma grafo acíclico (Kahn); passo que depende de si ou de ciclo → rejeita.
2. **Cap existe:** `brCap(step.Cap) != nil` ou `script:<nome>` registrado.
3. **Cap disponível:** `cbAvailable` no perfil atual (binário presente).
4. **Slot resolvível:** todo `$slot.x` é produzido por `Out` anterior, é fato de `brFacts` (`slot.`, `project.`, `bin.`, `env.online`, `hint`) ou está declarado em `Slots` com resolvedor conhecido (`nameindex`, `arg`, `literal`).
5. **Comando literal:** todo `read_cmd` está em `cbReadCmds` (`caps.go:573`); nada de shell livre.
6. **Path seguro:** `brSafePath` não retorna erro; sem `..`, `~`, absoluto fora do root.
7. **Efeito declarado:** `Fx=="write"` exige `permit` e marca o passo como não-replayable por padrão; `Fx=="read"` não pode chamar `file_op`.
8. **Verificador objetivo:** existe e é de `tests_passed | exit_zero | file_exists | file_gone | script_exit_zero`.
9. **Idempotência:** passo de escrita precisa de `file_exists`/`file_gone`/`once` (evita *completed-as-pending*).
10. **Estado:** `State.Head` e `State.Dirty` presentes (ver P3).

Rejeição gera `auditRec("plan.screen_rejected", {reason, task, step})` e o plano **não** é gravado. Grade: `A` se todos os passos são `read`/`exec` read-only ou escrita determinística; `B` caso contrário (só hint).

**Arquivos.** `planscreen.go` (novo); `plancache.go:433` (`pcStore`) chama o screen completo; `plancache.go:110` (`pcLoad`) roda o subconjunto sem fatos (DAG, cap existe, verificador); `methods.go:115` (`mhBadCmd`) reusado; `planscreen_test.go` com um caso por operador do CoMemBench.

**Custo no d610.** O(V+E) com V≤12; <1 ms; zero alocação além do mapa de grau.

**Métrica.** Taxa de rejeição por motivo; false_success de planos grau A (meta: 0); bad-reuse rate.

**Impacto 5 · Esforço S–M · Risco baixo.** É a proposta que mais reduz dano por unidade de esforço.

---

### P3 — Fingerprint de estado e pré-condições de replay

**Mecanismo.** `piState` guarda: `Head` (git HEAD), `Dirty` (sha256 de `git status --porcelain` ordenado, ou de (path, size, mtime) dos arquivos tocados quando não há git), `Tools` (versões de go/node/python do `epProfile`), `Repo` (o `lrRepoSig` atual), `Root` (slug). Antes de reusar:

- HEAD igual e dirty igual nos arquivos que o plano toca → replay liberado;
- HEAD igual, dirty diferente só em arquivos não tocados → replay liberado com nota;
- HEAD diferente ou dirty nos arquivos do plano → **rebaixa para hint** (nunca executa).

Pré-condições por passo: `file_exists`, `file_gone`, `bin.X`, `project.test`, `env.online`, checadas antes da execução. Prefixo read-only: se os primeiros k passos só leem e os arquivos estão inalterados desde o fingerprint, eles são pulados e viram evidência ("já li X, Y"), não comandos.

**Arquivos.** `planstate.go` (novo); `plancache.go:56` (`pcPlan.State`); `lessons_reuse.go:40` (`lrRepoSig` passa a incluir HEAD); `envprobe.go` (versões já medidas); `brain.go:225` (`brFacts` já tem `bin.*`/`project.*`).

**Custo no d610.** Um `git status --porcelain` por turno **só quando há match** (~10–30 ms em repo pequeno); cachear por (HEAD, mtime do index). Hash: sha256 de ≤200 linhas, <1 ms.

**Métrica.** Stale-hit rate (match que falha o estado), replay failure rate, false_success.

**Impacto 4 · Esforço M · Risco baixo.**

---

### P4 — Matching por esqueleto + intenção + shape

**Mecanismo.** Três filtros em série, do mais barato ao mais caro:

1. **Intenção** (`nlu.Parse`, `nlu.go:928`): se `Confidence ≥ 0.6` e `Name != "none"`, só planos com `Intent` igual ou vazio passam. Reusar o `Intent` já calculado pelo cérebro (passar por parâmetro) para não parsear duas vezes.
2. **Esqueleto:** substituir no texto da requisição paths, identificadores, números, strings entre aspas, branches e hosts por `<file>`, `<id>`, `<num>`, `<str>`, `<branch>`, `<host>`; indexar só o esqueleto. Nomes de arquivo entram como termo fraco (peso 0,5) via `pcDocTerms`.
3. **Shape:** chave = tipos de slot ordenados + intenção (ex.: `file+num|run_tests`). Match exato de shape dá bônus; exige ≥2 termos distintos **ou** shape igual (mantém a regra de 40% de cobertura de `pcMatch:289`).

**Arquivos.** `plancache.go:197` (`pcTerms` ganha `pcSkeleton`), `plancache.go:233` (`pcDocTerms`), `plancache.go:244` (`pcMatch`); `nlu.go:928` (`Parse`); `plancache_test.go` (casos "mesmo arquivo, tarefa diferente" e "arquivos diferentes, mesma tarefa").

**Custo no d610.** Uma passada de regex sobre ≤200 chars; desprezível. BM25 inalterado.

**Métrica.** Precisão de reuso = wins/(wins+fails) nos turnos com match; bad-reuse rate; reuso efetivo % (hits que terminam verificados / hits).

**Impacto 4 · Esforço S–M · Risco médio** (esqueleto agressivo demais derruba recall; medir com P10).

---

### P5 — Ranking e poda por utilidade (Wilson), recência e proveniência

**Mecanismo.** Substituir o bônus `1 + 0.1*min(wins,5)` (`plancache.go:292`) e a evicção `Fails >= 2` (`plancache.go:418`) por:

```
n       = wins + fails
pHat    = (wins + 0.5) / (n + 1)                 // suavização de Jeffreys
utility = pHat - 0.7*sqrt(pHat*(1-pHat)/(n+1))   // limite inferior estilo Wilson, z=0.7
score   = 0.60*log1p(bm25)/log1p(maxBM25)
        + 0.25*clamp01(utility)
        + 0.15*0.5^(ageDays/45)
```

Um `sqrt` por plano; nada de beta inversa. z=0,7 (não 1,96) porque o cache é pequeno e o custo de reter um plano duvidoso é menor que o de matar um bom cedo. Com 1 vitória e 0 falhas, `utility ≈ 0,54`: por isso as regras abaixo exigem uso mínimo antes de julgar.

- **Evict** se `uses ≥ 4` e `utility < 0.40`; ou 2 falhas consecutivas; ou `wins == 0` e idade > 120 dias.
- **Demote** (hint-only, não apaga) se `uses ≥ 3` e `utility < 0.55`.
- **Cap:** 30 por intenção, 100 por projeto (`pcMaxPlans`), mantendo melhor LB e mais recente.
- **Proveniência:** plano com verificador objetivo e evidência ganha +0,05; plano sem verificador nunca passa de grade B.

Sem min-max (instável com n pequeno): `log1p` e exponencial são monótonas e baratas.

**Arquivos.** `plancache.go:405` (`pcScore` passa a contar `uses`), `plancache.go:129` (`pcSave` poda), `plancache.go:292` (ranking), `lessons_reuse.go:99` (`lrPick` usa LB em vez de `wins-fails`).

**Custo no d610.** Um `math.Sqrt` por plano; 100 planos → microssegundos.

**Métrica.** Reuse success rate; tamanho do cache; churn de evicção; p50 dos turnos com match.

**Impacto 4 · Esforço S · Risco baixo.** O survey mostra que retrieval plano degrada com dezenas–centenas de entradas; a poda é o que impede o cache de virar distrator.

---

### P6 — Replay read-only e reuso de prefixo

**Mecanismo.** `prReplay(ctx, cx, p)` executa o IR em ordem:

- Passo `read`/`exec` read-only → `brCap` (nunca shell livre), com timeout por passo e `permit()`.
- Passo `write` → **aborta o replay** (regra do LOOP: skill com `edit_file` não é skill) e devolve o controle ao cérebro/modelo. Exceção: `file_op` com template determinístico (ex.: bump de versão com regex ancorada) e `--allow-tools`.
- Falha em qualquer passo → para, registra `rvCat`, e segue a escada: **replay → método do cérebro → modelo → perguntar**. Nunca "trava".
- Prefixo: se os primeiros k passos são `read` e o estado não mudou, eles são pulados e reportados como evidência; a cauda é executada.
- Verificação final: o `verify` do plano, via `brVerify`-like; sem exit 0, o resultado é `unverified`, jamais `succeeded`.

Integração: `brRunIntent` (`brain.go:130`) tenta `prReplay` **antes** de `mhLibrary` quando há plano grau A com match e estado válido; `brExecute` (`brain.go:261`) executa a cauda como um método sintético.

**Arquivos.** `planreplay.go` (novo); `brain.go:130` e `brain.go:500`; `brain.go:261` (executor reusado); `caps.go:594` (`cbReadCmd`).

**Custo no d610.** Custo dominado pelos comandos; +1 decode de plano; zero processo extra. Um `read_file` replay é ~1–3 ms; um `go test` é o que já seria.

**Métrica.** `no_llm_rate` real (share de requisições respondidas por replay), comandos/tarefa, p50, false_success.

**Impacto 5 · Esforço L · Risco médio-alto** (replay de escrita é onde se perde confiança; por isso o default é recusar).

---

### P7 — Method Forge: compilar plano verificado em `mhMethod`

**Mecanismo.** `mfCompile(p piPlan) (mhMethod, bool)`:

- Só grau A e só com gate (P8) aprovado.
- `Intent` do plano (ou `lrTaskClass` se ausente); `Priority = 100 + round(10*utility)` — acima dos embutidos (máx. 11 hoje).
- `Pre` a partir dos fatos usados (`project.test` set, `slot.target` set, `bin.X` set).
- `Steps` = IR mapeado para caps; `Verify` = verificador do plano; `Risk` = `low` se todos os passos read, `med` caso contrário; `CostMS` medido.
- Grava `dataDir()/methods/<slug>-<intent>.json` (não embutido), no mesmo schema de `methods/*.json`, com um campo extra `origin` (ignorado por versões antigas).
- Loader: `mhLibrary` carrega embutidos e depois `mhUserLibrary()`; em colisão de nome, o método de usuário vence **somente** se o gate passou e o screen passa no estado atual.
- CLI `jazz forge [--list|--apply|--rollback]`; toda escrita vai para o audit log com hash do plano de origem.

**Arquivos.** `methodforge.go` (novo); `methods.go:81` (`mhLibrary`), `methods.go:60` (`mhMethod` ganha `Origin`); `brain.go:130`; `main.go` (subcomando).

**Custo no d610.** String/JSON puro; <10 ms; arquivo ≤ 20 KB por intenção; 50 intenções ≈ 1 MB em disco, carregado uma vez (`mhOnce`).

**Métrica.** `no_llm_rate` no eval, comandos/tarefa, p50, false_success, número de métodos promovidos e sobreviventes após 20 usos.

**Impacto 5 · Esforço L · Risco médio** (método genérico demais vira ruído; o gate é o freio).

---

### P8 — Gate held-out de promoção ("só mantém se bate o pai")

**Mecanismo.** `pgGate(cand, parent, holdout)` roda as duas variantes nos **mesmos** ≥3 casos (textos distintos, mesma intenção, fixtures em cópias temporárias), em pares, e promove somente se **todas** valem:

1. `false_success(cand) == 0`;
2. `sucessos(cand) ≥ sucessos(parent)` no par;
3. `cmds(cand) ≤ cmds(parent)` **e** `steps(cand) ≤ steps(parent)`, com pelo menos uma desigualdade estrita;
4. `wilsonLB(cand) ≥ 0.5`;
5. nenhuma negação de permissão;
6. screen estrutural passa no estado atual do repo.

Rollback: se nas 3 últimas utilizações o sucesso cair >20% (gate do PSN citado no survey), ou 2 falhas consecutivas, o método é rebaixado a hint e o pai volta. Tudo com `auditRec` e hash.

**Arquivos.** `plangate.go` (novo); `evaloffline.go:279` (`eoOne` reusado para rodar fixture); `evals/offline/tasks.json` (categoria `reuse`); `methodforge.go` (só chama o gate).

**Custo no d610.** 2 braços × 3–5 tarefas × 1–3 s ≈ 6–30 s, mais setup das fixtures (10–40 s no total) **em tempo de forja**, nunca em runtime. É exatamente o "inteligência em tempo de compilação".

**Métrica.** Win-rate do método promovido vs pai; taxa de rollback; false_success.

**Impacto 5 · Esforço M–L · Risco médio** (precisa de fixtures bons; sem eles, o gate é teatro).

---

### P9 — Becos sem saída por categoria de recuperação

**Mecanismo.** `pcBuild` já coleta comandos que falharam e nunca tiveram sucesso (`plancache.go:512-518`), mas como texto. Guardar `{rvCat, cmd, errLine}` usando `rvClassify` (`recover.go:53`) sobre a observação. Na injeção, renderizar "pule: `cmd` (command_missing)". No replay, pré-checar o fato correspondente (`bin.X` para `command_missing`, `file_exists` para `name_not_found`) para que o passo nem rode. Lições ganham tag `[cat]` para o `improve` agregar por categoria.

**Arquivos.** `plancache.go:479` (`pcBuild`), `plancache.go:305` (`pcRender`), `recover.go:53`, `lessons.go` (tag).

**Custo no d610.** Desprezível (≤4 becos por plano, já limitado por `pcMaxDeadEnds`).

**Métrica.** Comandos/tarefa, passos até o primeiro sucesso, taxa de repetição do mesmo erro.

**Impacto 3 · Esforço S · Risco baixo.**

---

### P10 — Métricas de reuso no `jazz eval offline`

**Mecanismo.** Categoria `reuse` no `tasks.json`: cada tarefa roda **frio** (dir de planos vazio) e **quente** (após uma execução verificada). Campos novos em `eoSummary`: `reuse_hits`, `reuse_wins`, `reuse_fails`, `effective_reuse = wins/hits`, `cold_no_llm_rate`, `warm_no_llm_rate`, `warm_p50_ms`, `replay_false_success`. A/B por `JAZZ_PLAN_CACHE=on|off`.

**Arquivos.** `evaloffline.go:91` (`eoSummary`), `evaloffline.go:279` (`eoOne`), `evals/offline/tasks.json`.

**Custo no d610.** Só no harness; dobra o tempo do suite (frio+quente), ainda minutos.

**Métrica.** É o instrumento: sem ele, P1–P9 são opinião.

**Impacto 4 · Esforço M · Risco baixo.**

---

### 6.1 Tabela-resumo

| # | Proposta | Alimenta | Impacto | Esforço | Risco | Depende de |
|---|---|---|---|---|---|---|
| P1 | IR tipado | cache + forja | 5 | M | baixo | — |
| P2 | Screen estrutural | cache + forja | 5 | S–M | baixo | P1 |
| P3 | Fingerprint de estado | cache + replay | 4 | M | baixo | P1 |
| P4 | Esqueleto + intenção | cache | 4 | S–M | médio | — |
| P5 | Wilson + recência | cache | 4 | S | baixo | — |
| P6 | Replay read-only | runtime | 5 | L | médio-alto | P1–P3 |
| P7 | Method Forge | forja | 5 | L | médio | P1, P2, P8 |
| P8 | Gate held-out | forja | 5 | M–L | médio | P10 |
| P9 | Becos por categoria | cache | 3 | S | baixo | — |
| P10 | Métricas de reuso | medição | 4 | M | baixo | — |

---

## 7. Ordem de ataque e critérios de parada

**Como isso é o asset-forge.** As etapas 1–2 produzem o asset; a etapa 3 o consome; a etapa 4 é o portão held-out. O LLM pode aparecer na forja (hindsight, compilação assistida de um plano em método) — é o papel do Fable no blueprint, inteligência em tempo de compilação — mas nunca no replay. O artefato é o `pcPlan` de grau A e o JSON de `methods/`; se um deles precisa de LLM para resolver um slot em runtime, não é asset.

**Etapa 1 (barato, reduz dano imediato):** P2 + P5 + P10. O screen impede plano ruim de entrar; o Wilson impede plano ruim de ficar; o eval impede autoengano. Nada disso precisa do IR.

**Etapa 2 (o artefato):** P1 + P3 + P4. IR, estado e matching por esqueleto. Aqui o cache deixa de ser "lembrete de texto" e vira objeto executável com pré-condições.

**Etapa 3 (o runtime):** P6. Replay read-only atrás de flag (`--replay`), default off até o eval mostrar false_success = 0.

**Etapa 4 (a forja):** P7 + P8. Só depois que P6 provar que um plano grau A executa. A forja sem replay é só geração de JSON.

**P9 a qualquer momento** (uma tarde).

**Critérios de parada (kill criteria), antes de escrever código:**

- Se, após 200 turnos com match, `reuse_wins/(reuse_wins+reuse_fails) < 0.60` → desligar injeção automática e manter só hint explícito.
- Se qualquer `replay_false_success > 0` → desligar replay para aquela intenção e abrir o caso.
- Se `warm_p50 > cold_p50` no eval → o cache é overhead; reduzir `pcMaxInject` a 1 ou desligar.
- Se o gate P8 rejeitar >80% dos candidatos → o problema é o IR (P1), não o gate; não afrouxar o gate.

---

## 8. O que NÃO adotar (dependências disfarçadas)

1. **Embeddings e vector store** (AgentReuse 2512.21309; Voyager 2305.16291; ExpeL 2308.10144; Generative Agents 2304.03442). O AgentReuse reporta ~500 MB de VRAM só para os dois modelos; no d610, 2 GB divididos com o SO e o binário. Troque por BM25 + esqueleto: a literatura não mostra que embedding ganha o suficiente para pagar isso em código.
2. **LLM para extrair keyword em runtime** (APC 2506.14852). É o coração do APC e é exatamente o que a regra de ouro proíbe.
3. **Planner pequeno que adapta o template em runtime** (APC). Mesmo problema, mais caro.
4. **Self-verification por LLM** (Voyager; Reflexion 2303.11366). Não é verificação, é opinião; e falha silenciosamente quando o modelo erra com confiança. Use exit code.
5. **Extração de insights/guidelines em runtime** (ExpeL; AutoGuide 2403.08978). Permitido **só** em tempo de forja; em runtime, o artefato já está pronto.
6. **Indução online de workflow** (AWM 2409.07429). Precisa de LLM + avaliador por tarefa; é o oposto de offline.
7. **Cache semântico de resposta** (baselines do APC). Falso positivo é a regra, não a exceção; e o hit parece sucesso.
8. **Injeção de histórico completo** (APC). 72,00% vs 85,50% e mais caro. O `pcBuild` já está do lado certo.
9. **Topologia multi-agente e regiões de responsabilidade** (TOCOMAS). Multiplica chamadas e exige um grafo de tarefa que o jazz não tem.
10. **Evolução estrutural online com recompensa de uma execução** (TOCOMAS). O próprio paper não reporta variância; com 1 run, o "bateu o pai" é moeda.
11. **Replay de trajetória com edição arbitrária** (LOOP recusa `edit_file`). O conteúdo do edit não é parametrizável; fingir que é produz `file_op` escrevendo o arquivo errado.
12. **Cache de ferramenta stateless** (TVCache 2602.10986). Devolve leitura velha e corrompe o raciocínio sem erro visível.
13. **"99% de sucesso" como meta** (LOOP). É sucesso por construção num domínio estreito; usar como meta no jazz é definir a métrica para ganhar.
14. **Juiz-LLM no gate de promoção** (survey 2607.10113). O gate tem que ser executável e reproduzível; juiz-LLM reintroduz a dependência no ponto mais crítico.

---

## 9. Apêndice A — Números, com o ceticismo junto

| Paper | Benchmark | Número | n / sementes | Ressalva |
|---|---|---|---|---|
| TOCOMAS 2609.37953 | WorkBench SR | 77,68 vs 62,17 | sem sementes/CIs | baseline direto implausível; grafo dado |
| TOCOMAS | CoMemBench SR | 14,30 vs 4,00 | idem | benchmark dos mesmos autores |
| AgentReuse 2512.21309 | reuso efetivo | 93/100 | 20 req × 5 | domínio AIoT; sem held-out |
| AgentReuse | F1 / acc | 0,9718 / 0,9459 | idem | ground truth = resposta igual |
| APC 2506.14852 | custo / latência | −50,31% / −27,28% | múltiplos benchmarks | LLM em extração e adaptação |
| APC | template vs full history | 85,50 vs 72,00 | FinanceBench | — |
| AWM 2409.07429 | SR relativo | +24,6% / +51,1% | Mind2Web/WebArena | indução por LLM |
| Voyager 2305.16291 | itens únicos | 3,3× | 3 trials | verificação por LLM |
| ExpeL 2308.10144 | ALFWorld insight/retrieve | 50 / 55 | 4-fold | GPT-4; embeddings |
| Reflexion 2303.11366 | HumanEval pass@1 | 91% vs 80% | — | auto-avaliação |
| TravelPlanner 2402.01622 | sucesso GPT-4 | 0,6% | 1.000 test | benchmark, não método |
| LLM+P 2304.11477 | Blocksworld | 15→90% | 7 domínios | precisa de PDDL |
| LOOP 2605.14237 | tokens / latência | −93,3 a −99,98% / 8,7× | tarefas periódicas | auto-relatado; 8 páginas |
| TVCache 2602.10986 | hit / tempo | 70% / 6,9× | 3 workloads | pós-treino RL |
| SAGA 2610.06964 | ScienceWorld | 63,7 (cru) vs 48,4 (sem memória) | 2 backbones | abstração por LLM |

**O que os números não dizem.** Nenhum desses trabalhos mede o que o jazz precisa medir: taxa de acerto **sem LLM em runtime**, em código, em 32 bits, com estado de repositório mudando. O mais próximo é o LOOP, e o LOOP é estreito e auto-relatado. Portanto: use os papers para escolher o **desenho**, não para prever o **ganho**.

---

## 10. Apêndice B — Checklist de leitura (para quem for auditar)

Para cada paper, o que foi verificado: (1) o método está descrito o suficiente para reimplementar? (2) o número-chave tem n e variância? (3) a ablação isola o mecanismo ou o pacote? (4) a verificação é objetiva ou julgamento? (5) o artefato precisa de LLM em runtime? (6) o benchmark se parece com código em máquina fraca?

Onde a resposta de (4) ou (5) é "não/ sim", o mecanismo foi classificado como dependência, mesmo que o resultado seja impressionante. Foi o caso do Voyager (self-verification), do APC (keyword + planner), do ExpeL (insights), do AutoGuide (guidelines) e do AWM online.

---

## 11. Referências

1. S. Zhao et al. *Topological Coherence for Self-evolving Multi-agent Systems* (TOCOMAS). arXiv:2609.37953, 2026.
2. S. Zhao et al. *CoMemBench: Benchmarking Collaborative Memory Boundaries across Multi-Agent Workflow Topologies*. arXiv:2609.32192, 2026.
3. G. Li, R. Wu, H. Tan. *A Plan Reuse Mechanism for LLM-Driven Agent* (AgentReuse). arXiv:2512.21309, 2025.
4. Q. Zhang, M. Wornow, G. Wan, K. Olukotun. *Agentic Plan Caching: Test-Time Memory for Fast and Cost-Efficient LLM Agents*. arXiv:2506.14852, 2025 (NeurIPS 2025).
5. Z. Z. Wang, J. Mao, D. Fried, G. Neubig. *Agent Workflow Memory*. arXiv:2409.07429, 2024.
6. G. Wang et al. *Voyager: An Open-Ended Embodied Agent with Large Language Models*. arXiv:2305.16291, 2023.
7. A. Zhao, D. Huang, Q. Xu, M. Lin, Y.-J. Liu, G. Huang. *ExpeL: LLM Agents are Experiential Learners*. arXiv:2308.10144, 2023.
8. N. Shinn, F. Cassano, A. Gopinath, K. Narasimhan, S. Yao. *Reflexion: Language Agents with Verbal Reinforcement Learning*. arXiv:2303.11366, 2023.
9. J. S. Park et al. *Generative Agents: Interactive Simulacra of Human Behavior*. arXiv:2304.03442, 2023.
10. Y. Fu et al. *AutoGuide: Automated Generation and Selection of Context-Aware Guidelines for Large Language Model Agents*. arXiv:2403.08978, 2024 (NeurIPS 2024).
11. J. Xie et al. *TravelPlanner: A Benchmark for Real-World Planning with Language Agents*. arXiv:2402.01622, 2024 (ICML 2024).
12. B. Liu et al. *LLM+P: Empowering Large Language Models with Optimal Planning Proficiency*. arXiv:2304.11477, 2023.
13. *Get Experience from Practice: LLM Agents with Record & Replay* (AgentRR). arXiv:2505.17716, 2025.
14. X. Wang et al. *Good to Go: The LOOP Skill Engine That Hits 99% Success and Slashes Token Usage by 99% via One-Shot Recording and Deterministic Replay*. arXiv:2605.14237, 2026.
15. *TVCache: A Stateful Tool-Value Cache for Post-Training LLM Agents*. arXiv:2602.10986, 2026.
16. B. Ye et al. *Principles that Guide, Actions that Inform: Agent Evolution via Knowledge Abstraction* (SAGA). arXiv:2610.06964, 2026.
17. *Dynamic Agent Skills: A Lifecycle Survey and Taxonomy of Evolving Skill Libraries*. arXiv:2607.10113, 2026.
18. Documento interno anterior: `docs/research/tocomas-plan-reuse.md` (A1–A5 e o mapa inicial para o jazz).
