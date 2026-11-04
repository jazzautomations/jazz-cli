# Memória de longo prazo e retrieval sem embeddings (memstore + nameindex)

Pesquisa de 04/11/2026, depois de `offline-brain-research.md` (implementado na 0.9),
`offline-on-device-intelligence.md` (P9 — índice invertido é o piso de retrieval; propostas
B trigrama e C casos/CBR), `tocomas-plan-reuse.md` (reuso de plano: AgentReuse ~30% de
pedidos repetidos/similares, APC templates), `grep-is-all-you-need.md` (grep inline > vector
inline em memória de chat, com ressalvas), `harness-sota-gap.md` (memória dos agentes SOTA:
CLAUDE.md, re-injeção pós-compact de arquivos recentes), `ux-offline-flow-continuity.md`
(memória declarada na UX — propostas G/I deles) e `low-level-performance.md` (layout de
memória e índice persistido no d610). O que ficou de fora e é o tema deste doc: **a loja de
memória em si** — o que gravar (fatos do usuário, fatos do repo, casos), como recuperar sem
embedding e sem LLM (BM25/trigrama puro-Go), e como esquecer/consolidar, tudo offline.
Aterrissa em `nameindex.go` + um `memstore.go` novo.

Convenções: cada afirmação traz fonte primária. **(estimativa)** marca número meu, não
medido. "Custo em 1 núcleo" vale para o d610 (Pentium M 1,7 GHz, linux/386, 2 GB, HDD),
mesma máquina de `results/dell-0.9-progress.md`. Identificadores em inglês.

---

## 1. Estado da arte: papers e técnicas

Cada item: o que é, o mecanismo, o número-chave, a limitação honesta. Ao final, uma seção
para o retrieval clássico (sem vetor).

### 1.1 MemGPT — memória virtual paginada pelo próprio LLM

*MemGPT: Towards LLMs as Operating Systems*, Packer et al., arXiv:2310.08560 (UC Berkeley,
ICML 2024).

**Mecanismo.** O contexto (prompt tokens) é a "RAM"; o armazenamento externo é o "disco".
Main context = instruções de sistema (read-only) + *working context* (memória editável:
blocos `persona`/`human` que o próprio LLM reescreve via função) + fila FIFO com
mensagens/funções recentes. External context = *recall storage* (histórico completo de
mensagens, buscável por texto/data) + *archival storage* (documentos arbitrários; no paper,
Postgres+pgvector). Um *queue manager* vigia o tamanho: a ~70% do limite de flush injeta
aviso de sistema ("pressionado por memória, salve o que importa"); a 100% despeja ~50% da
fila para o recall storage. O LLM decide **ele mesmo** quando paginar, via function calls
(`core_memory_append/replace`, `archival_memory_insert/search`, `recall_memory_search`,
`conversation_search_date`) e encadeia passos com `request_heartbeat=true` — uma tarefa de
recuperação multi-hop vira N inferências.

**Números-chave.**
- *Deep Memory Retrieval* (pergunta sobre sessão 1–5 de chat multi-sessão MSC): GPT-4 base
  32.1% → **92.5%** com MemGPT; GPT-4 Turbo 35.3% → 93.4%; GPT-3.5 38.7% → 66.9% (juiz
  GPT-4; ROUGE-L 0.296 → 0.814 no GPT-4).
- *Nested KV retrieval* (140 pares UUID, ~8k tokens; o valor é chave do próximo salto):
  GPT-3.5 → 0% já em 1 nível; GPT-4/4-Turbo → 0% em 3 níveis; MemGPT+GPT-4 **independe da
  profundidade** (pagina iterativamente pelo índice).
- Document QA (Wikipedia dump): acurácia estável quando o corpus excede a janela, onde
  truncamento degrada.

**Limitações.** (a) Toda decisão de paginação é uma inferência — a memória é *gerenciada
pelo modelo*, logo o mecanismo central é dependência de LLM no caminho crítico (pela regra
de ouro, inadotável em runtime); (b) o archival do paper é pgvector (embedding); (c) DMR é
chat multi-sessão (MSC), não tarefa de código; (d) custo por mensagem cresce com chamadas
de memória.

**O que sobra offline:** a **arquitetura de camadas** (working/recall/archival) e a
**política de pressão** (warn-70% → flush-50%) — a política pode ser determinística, como
já é a compactação do jazz (`agent.go:501-560`). A decisão *o que salvar* sai do LLM e vira
regra + verificação (§4-B).

### 1.2 A-MEM — Zettelkasten agentic com links e evolução

*A-MEM: Agentic Memory for LLM Agents*, Xu et al., arXiv:2502.12110 (Rutgers, NeurIPS 2025).

**Mecanismo.** Cada memória é uma nota atômica mᵢ = {conteúdo Cᵢ, keywords Kᵢ, tags Gᵢ,
descrição contextual Xᵢ, links Lᵢ, embedding Eᵢ} — K/G/X gerados por LLM na escrita,
Eᵢ = all-MiniLM-L6-v2. Duas operações na inserção: *link generation* (top-k por embedding →
LLM decide quais vizinhos viram links) e *memory evolution* (o LLM reescreve X/K/G das
notas vizinhas à luz da nova — consolidação contínua). Retrieval: embedding da query →
top-k; notas linkadas entram **de graça** no contexto.

**Números-chave** (LoCoMo, F1 %; média ranking sobre 6 modelos):
- GPT-4o-mini + A-MEM: MultiHop 27.02 / Temporal 45.85 / OpenDomain 12.14 / SingleHop
  44.65 / Adversarial 50.03 — **melhor média em todas as 5 categorias** vs LoCoMo,
  ReadAgent, MemoryBank e MemGPT; com ~**2.520 tokens** de contexto vs 16.977 do MemGPT e
  16.910 do LoCoMo (**−85%**). DialSim: F1 3.45 vs 2.55 (LoCoMo) e 1.18 (MemGPT).
- Ablação (GPT-4o-mini): sem link+evolution MultiHop cai 27.02 → 9.65 e Temporal 45.85 →
  24.55 — o ganho vem da **organização**, não só do retrieval.
- Custo: ~1.200 tokens e 5,4 s por operação de memória (GPT-4o-mini); 1,1 s com Llama-3.2-1B
  local.

**Limitações.** Cada escrita custa várias chamadas LLM + embeddings — dependência tripla
no caminho crítico; k de retrieval tem trade-off saturável; sem evolução real sem LLM.

**O que sobra offline:** o **schema da nota** (conteúdo + keywords + tags + links tipados)
— K/G saem de `pcTerms` + caminhos + intent, não de LLM; **links por similaridade** viram
arestas por co-termo/co-arquivo; **evolução por LLM** vira consolidação determinística
(dedup Jaccard + wins/fails, §4-F). A lição quantitativa fica: organização vale mais que
recall bruto — uma loja flat BM25 sem estrutura deixa valor na mesa.

### 1.3 Generative Agents — recency × importance × relevance

*Generative Agents: Interactive Simulacra of Human Behavior*, Park et al.,
arXiv:2304.03442 (Stanford/Google, UIST 2023).

**Mecanismo.** *Memory stream*: log append-only de observações em prosa; cada entrada
{descrição, last_accessed, importance}. Retrieval score = αr·recency + αi·importance +
αrel·relevance (α=1, cada termo min-max [0,1]): recency = 0.995^(horas desde último
acesso); importance = inteiro 1–10 **atribuído pelo LLM na escrita**; relevance = cosseno
entre embeddings da query e da descrição. *Reflection*: quando a soma de importância dos
eventos recentes passa do limiar (~150), o LLM gera 3 perguntas, recupera, sintetiza
insights — que entram no stream como memórias novas (consolidação hierárquica).

**Números-chave.** Ablação da arquitetura em avaliação de "believability": remover
observação, reflexão ou planejamento derruba a pontuação — cada componente contribui
(na entrevista, a condição sem reflexão+planejamento é a mais fraca; o paper conclui que a
arquitetura completa é necessária).

**Limitações.** Benchmark é de verossimilhança subjetiva em simulação de 25 agentes/2 dias;
importance é nota subjetiva de LLM; relevance é embedding; nada é verificado objetivamente.

**O que sobra offline:** a **forma do score** — três sinais baratos combinados. Em jazz os
três têm substitutos objetivos: recency = decaimento exponencial sobre `last_access`;
relevance = BM25 (`pcMatch`); importance = **medida, não opinião** — wins−fails de
verificação real (`pcScore`), pino do usuário, fonte (observado > inferido). A nossa
importância é *melhor* que a deles para engenharia, só menos expressiva para prosa.

### 1.4 HippoRAG — KG sem esquema + Personalized PageRank

*HippoRAG: Neurobiologically Inspired Long-Term Memory for LLMs*, Jiménez Gutiérrez et al.,
arXiv:2405.14831 (Ohio State, NeurIPS 2024).

**Mecanismo.** Indexação offline: LLM (GPT-3.5) faz OpenIE por passagem → triplas
(sujeito, relação, objeto) num KG sem esquema; um encoder denso adiciona arestas de
sinonímia E′ entre sintagmas com cosseno > τ=0.8. Retrieval: NER da query (LLM) → casar
entidades aos nós via encoder → PPR sobre o KG com damping 0.5 semeado nos nós da query →
score de passagem = massa PPR agregada nos seus nós. Multi-hop vira um passeio aleatório —
um salto de grafo substitui iterações de retrieval+LLM.

**Números-chave** (R@2/R@5, 1k questões dev):
| corpus | BM25 | ColBERTv2 | HippoRAG(ColBERTv2) |
|---|---|---|---|
| MuSiQue | 32.3/41.2 | 37.9/49.2 | **40.9/51.9** |
| 2Wiki | 51.8/61.9 | 59.2/68.2 | **70.7/89.1** |
| HotpotQA | 55.4/72.2 | 64.7/79.3 | 60.5/77.7 |
- IRCoT+HippoRAG: 2Wiki R@5 93.9 vs 74.4 (IRCoT+ColBERTv2) — ganho complementar.
- Single-step ≈ IRCoT iterativo a **10–30× mais barato e 6–13× mais rápido**.
- Escala do grafo (MuSiQue): 11.656 passagens → 91.729 nós, 21.714 arestas de relação E,
  mais **191.636 arestas de sinônimo E′** — as arestas caras dominam.

**Limitações.** OpenIE=LLM e sinônimos=encoder: a parte que diferencia é a mais cara; em
HotpotQA perde para ColBERTv2 (grafo ajuda onde há integração multi-hop de verdade);
hype flag moderada — ganhos concentrados em 2Wiki.

**O que sobra offline:** **PPR é barato** — power iteration sobre grafo esparso em Go é
~40 linhas e O(iterações × arestas); o que não roda é a *construção* (OpenIE/encoder). O
grafo jazz sai de estrutura, não de linguagem: memória↔arquivo, memória↔intenção,
termo↔memória, arquivo↔arquivo (import, teste-por-`niCore`, co-mudança git — este último já
"só pesquisado" em `offline-brain-research.md` §C). Expansão de 1 salto dos seeds BM25 já
captura boa parte do efeito; PPR completo é opcional (§4-H).

### 1.5 MemoryBank — esquecimento de Ebbinghaus operacionalizado

*MemoryBank: Enhancing LLMs with Long-Term Memory*, Zhong et al., arXiv:2305.10250
(AAAI 2024).

**Mecanismo.** Modelo de força S: inicia em 1; cada recall bem-sucedido faz S+=1 e zera t
(tempo desde último acesso); probabilidade de retenção segue a curva de Ebbinghaus
R = e^(−t/S) — memória lembrada recentemente e muitas vezes esquece devagar; nunca lembrada
some rápido. Retrieval = FAISS dense; retrato do usuário = sumário LLM diário.

**Números-chave.** Qualitativo (companion bot SiliconFriend); a contribuição citável é o
mecanismo, não o benchmark.

**Limitações.** Esquecimento como traço de personalidade, não de correção; retrieval denso.

**O que sobra offline:** a fórmula toda — S/t são campos de 8 bytes cada na entrada, e o
esquecimento vira **score decay + tombstone**, não perda real silenciosa. No jazz,
"esquecer" precisa ser auditável (o usuário vê o que foi esquecido), então: decay afeta
ranking; tombstone apaga de verdade (§4-F).

### 1.6 Mem0 e Zep — memória de produção (extração por LLM)

*Mem0*, Chhikara et al., arXiv:2504.19413 (ECAI 2025): pipeline ADD→extração LLM de fatos
salientes→consolidação (update/delete dedup). LOCOMO: +26% relativo sobre OpenAI;
**−91% p95 latência e −90% tokens vs full-context**; variante com grafo +2%. (O README deles
descreve uma versão mais nova com retrieval híbrido BM25+semântico+entidade fundidos —
claim de vendor, mas sinal de que mesmo quem vende vetor adicionou lexical.)

*Zep/Graphiti*, Rasmussen et al., arXiv:2501.13956 (Zep AI): grafo temporal de episódios —
entidades/relações com intervalo de validade [t válido, t invalidado); dedup por LLM.
DMR 94.8 vs MemGPT 93.4; LongMemEval +18.5% acurácia, −90% latência. **Paper de produto
(self-reported), sem ablação pública dos custos de construção.**

**O que sobra offline:** (a) a distinção Mem0 **fato ≠ episódio** — a loja guarda fatos
destilados, o episódio (sessão JSONL) fica como evidência/proveniência; (b) Zep **validade
temporal** — fatos carregam `valid_from`/`superseded_at`; contradição é resolvida por
supersede determinístico (mais novo vence) sem reescrever história; (c) extração sem LLM =
padrões + efeito verificado (§4-B).

### 1.7 LongMemEval — o benchmark que mede o que falha

*LongMemEval*, Wu et al., arXiv:2410.10813 (UCLA/Tencent, ICLR 2025): 500 questões sobre
históricos ~115k tokens (~40 sessões) em 5 habilidades — extração, raciocínio
multi-sessão, temporal, atualização de conhecimento (supersede), abstenção. Assistentes
comerciais **perdem ~30% de acurácia** em histórico longo. Decomposição do design:
indexing→retrieval→reading; as otimizações que mais pagam: **session decomposition**
(granularidade fina: indexar fatos/turnos, não a sessão inteira), **fact-augmented key
expansion** (indexar o fato extraído junto ao episódio) e **time-aware query expansion**.
O corpus-mãe (LoCoMo, arXiv:2402.17753, ACL 2024 — 300 turnos, ~9k tokens, até 35 sessões)
reporta gap de 56% para humanos, e **73% em raciocínio temporal** — temporalidade é onde
retrieval lexical puro mais sofre (§4-G).

Nota de método: `grep-is-all-you-need.md` já documentou que em LongMemEval-S *grep inline
vence vector inline em 10/10 pares modelo×harness*, com a ressalva n=116 e confound do
pre-load vetorial — leia-se "lexical compete com denso", não "lexical domina".

### 1.8 Sleep-time compute — a validação externa da filosofia

*Sleep-time Compute*, Lin et al., arXiv:2504.13171 (Letta/UC Berkeley): o agente "pensa"
entre interações — reescreve contexto cru em contexto aprendido (fatos, índices, rascunhos)
antes da query. Mesma acurácia com **~5× menos compute em test-time**; escalar compute de
sonho dá +13% (GSM) e +18% (AIME); amortizado em multi-query, −2.5× custo por query;
ganho **correlaciona com a previsibilidade da query**. É a regra de ouro deste repositório
publicada por outro grupo: o caro roda fora do caminho crítico, o barato reusa. No jazz:
`jazz improve`/`forge` (assets) e as consolidações do memstore são o "sono"; o retrieval
BM25 é o "acordado".

### 1.9 Retrieval clássico sem vetor (o piso, já citado — detalhes que faltavam)

- **BM25/Okapi.** Robertson & Zaragoza, *The Probabilistic Relevance Framework: BM25 and
  Beyond*, FnTIR 3(4), 2009: score = Σ idf(t)·tf·(k1+1)/(tf + k1·(1−b+b·|d|/avgdl));
  idf clássico ln((N−df+0.5)/(df+0.5)) pode ir negativo — a variante +1
  (ln(1+(N−df+0.5)/(df+0.5)), a do Lucene) é **exatamente a do `pcMatch`**
  (`plancache.go:286`); k1=1.2, b=0.75 são os defaults de Lucene/Tantivy e do jazz.
  Ressalva própria do paper: BM25 assume termos independentes — frases e entidades
  compostas precisam de termos conjuntos (bigramas de path) ou da camada de contexto
  (`niTokens`, que já temos).
- **Índice invertido por segmentos (Lucene/Tantivy).** Índice = coleção de **segmentos
  imutáveis** (WORM); delete = tombstone num bitmap; merge em background regrava e apaga
  deletados; `meta.json` comita atomicamente; term dict = FST; postings = delta+bitpack
  (+varint na cauda); cada segmento tem DocIds compactos locais (Tantivy
  ARCHITECTURE.md; fulmicoton, "Behold Tantivy" parts 1–2). Lição operacional para o
  memstore: **append-only + tombstones + merge determinístico** é o modelo de
  persistência que já serve `nameindex_idx.go` — não inventar outro.
- **Trigrama de conteúdo.** Cox 2012 (citado em P9 do doc anterior: kernel 3.1.3,
  "hello world" → 36.972→25 candidatos); Zoekt = trigramas posicionais, índice ~3× corpus,
  meta sub-50 ms em ~2 GB. Para o memstore (≤ ~2k docs de uma linha) trigrama é overkill —
  BM25 com termos por palavra basta; trigrama volta a ser relevante só para o índice de
  **conteúdo do repo** (proposta B já registrada, não repetida aqui).
- **ripgrep.** BurntSushi, "ripgrep is faster than {grep, ag, git grep, ucg, pt, sift}"
  (set/2016): força bruta com extração de literais internos + memchr SIMD e fallback —
  no d610 SSE2-only o memchr ainda funciona (é word-at-a-time, não vectorizado), então
  `rg` permanece o fallback honesto quando o índice não cobre; `grep-is-all-you-need.md`
  já analisou o caso agentic.

---

## 2. Regra de ouro: o que roda OFFLINE vs o que é dependência

| componente | roda offline sem LLM? | substituto no jazz |
|---|---|---|
| tiers de memória (working/recall/archival) | **sim** — já existe: lições/approvals = working; sessions JSONL = recall; plans+repo index = archival | `lessons.go`, `session.go`, `plancache.go`, `nameindex_idx.go` |
| política de pressão (warn→flush) | **sim** — já determinística | `agent.go:501-560` (compactação) |
| paging decidida pelo LLM (function calls de memória) | **não** — dependência | política determinística: escrever no fim do turno verificado + explicit "lembra" (§4-B/E) |
| nota estruturada {conteúdo, keywords, tags, links} (A-MEM) | **parcial** — schema sim; preenchimento por LLM não | keywords=`pcTerms`, tags={kind, intent, repo-sig, arquivos}; links=co-termo/co-arquivo |
| link generation + memory evolution (A-MEM) | **não** — LLM por escrita | arestas estruturais + consolidação Jaccard/wins-fails (§4-F) |
| recency decay (GenAgents, MemoryBank) | **sim** — aritmética | exp(−Δt/meia-vida) por kind (§4-D) |
| importance por nota LLM 1–10 | **não** — dependência | importância objetiva: wins−fails verificados, pinned, fonte (observado>inferido) |
| relevance por embedding | **não** — dependência | BM25 (`pcMatch`) + termos conjuntos |
| reflection/consolidação por LLM | **não** em runtime; **sim** forjado (sleep-time) | `jazz improve`/`forge` reescreve memória entre sessões (§4-F) |
| PPR sobre grafo (HippoRAG) | **sim** — ~40 linhas, O(iter·arestas) | grafo estrutural (mem↔arquivo↔intent↔termo) (§4-H) |
| OpenIE/NER por LLM p/ construir grafo | **não** — dependência | entidades = nameindex+envprobe+intents já parseados |
| validade temporal de fatos (Zep) | **sim** — campos | `valid_from`/`superseded_at`, supersede newest-wins (§4-G) |
| extração de fatos por LLM (Mem0/A-MEM) | **não** — dependência | extração determinística de resultado verificado + padrões explícitos (§4-B) |
| session decomposition p/ retrieval (LongMemEval) | **sim** | entradas granulares (fato/caso), não sessão inteira |
| embeddings qualquer | **não** — não cabe e não há ganho para código (P9, grep-doc) | BM25+trigrama+grafo estrutural |
| benchmark de memória (LongMemEval/LoCoMo/DMR) | n/a — é de chat | suíte própria `memória` em `evals/offline/tasks.json` (§4-I) |

Resumo em uma linha: **toda a aritmética de memória (score, decay, PPR, merge) roda
offline; toda a semântica de escrita/link/reflexão dos papers é LLM — no jazz vira forja
entre sessões ou regra determinística.**

---

## 3. Mapa no jazz (file:line, feito/parcial/só pesquisado)

| mecanismo | onde | estado |
|---|---|---|
| índice de nomes do repo: SoA pointer-free, blob único, persistido `.idx`, validade por dir/mtime | `nameindex.go:63-81`, `nameindex_idx.go:36-95,253-352,385-534` | feito (nomes+símbolos; sem conteúdo, sem memória) |
| resolução fuzzy com contexto (shape/hint/verbo/dir/ext/touched/recency) | `nameindex.go:399-720,1035-1228` | feito |
| símbolos top-level por regex (lazy) | `nameindex.go:199-255` | feito |
| recência de sessão ("já mexemos nele") | `nameindex.go:259-296,616-619` | parcial — volátil, morre com o processo; sessões seguintes não lembram |
| BM25 puro-Go (tokenizer `pcTerms`, stoplist PT/EN, df por consulta) | `plancache.go:160-301` | feito — recomputa stats por query (ok p/ N≤~2k; indexar se crescer) |
| plan cache verificado: template, wins/fails, evict 2 fails, dedup Jaccard≥0.7, ≤3 injetados ≤600 tok | `plancache.go:41-53,343-552` | feito — **só alimenta o modelo**; não serve o brain offline nem guarda fatos |
| hindsight hint por modelo forte (opcional, forja) | `plancache.go:561-601` | feito (LLM — caminho de forja, não runtime) |
| lições markdown + injeção session-start + audit | `lessons.go:40-329` | feito (prosa; retrieval = ler tudo, não ranquear por pedido) |
| reuso por classe de tarefa + repo signature ("da última vez") | `lessons_reuse.go:28-152` | feito (1 linha; sem ranking multi-caso, sem esquecimento) |
| fatos locais p/ perguntas frequentes (branch, test cmd, pkg manager, exists) | `askfacts.go:35-176` | feito — hardcoded; **não aprende** fatos novos |
| aprovações lembradas (class,target) com trust store | `approvals.go:34-204` | feito — memória simples + precedente de confiança para arquivos de memória |
| transcript por sessão (recall storage do MemGPT) | `session.go:193-308,413-505` | feito — **não indexado** para retrieval |
| compactação (pressão de contexto determinística) | `agent.go:501-560` | feito — análogo ao queue-manager, sem a parte de "salvar antes" |
| slot de injeção cache-safe no user-turn | `agent.go:466` (`pcInject`+`niInject`+`dcInject`), `plancache.go:341-364` | feito — é aqui que memória injeta |
| prefetch lexical no 1º turno | `prefetch.go:27-40` | feito (flag off até A/B) |
| casos CBR no brain | — | só pesquisado (proposta C, `offline-on-device-intelligence.md` §4) — **absorvido pelo §4-A daqui** |
| memória do usuário (core memory / fatos) | — | ausente |
| esquecimento (decay/tombstone/consolidação) | `pcMaxPlans=100` + wins/fails | parcial — cap existe; sem decay temporal, sem tombstone, sem merge |
| validade temporal em memória | — | ausente |
| grafo de memória / PPR | repomap só pesquisado (`offline-brain-research.md` §C) | só pesquisado |
| intents `remember`/`forget`/`recall` | — | ausente (54 intents hoje; `mem_usage` cobre só RAM) |
| suíte de memória no eval | `evals/offline/tasks.json` (75 tarefas, nenhuma de memória) | ausente |
| veneno de memória (prompt injection via store) | `pfSafeRead`, `h2PoisonReason`/`h2PlanPoison`, `injScan` em `lsRender`/`pcLoad` | mecanismos feitos — aplicar ao memstore (lição `approvals.go:103-106`: arquivo de memória de projeto só vale se o jazz o escreveu) |

---

## 4. Propostas (ordenadas por impacto esperado)

Todas Go stdlib, medidas no d610. Métrica = o que a proposta move em `jazz eval offline`
(no_llm_rate, sucesso verificado, false_success, false_blocks, perguntas, comandos/tarefa,
p50/p95, RSS) + métricas próprias quando indicado (hit@k, precisão de extração, stale-rate).

### A. `memstore.go` — a loja unificada de memória (a peça que falta)

- **Mecanismo.** Um JSONL por escopo: `dataDir()/mem/<projectSlug>.jsonl` (projeto) e
  `configDir()/memory.jsonl` (global/usuário). Entrada:
  ```go
  type memEntry struct {
      ID       string   // hex sha256(text+kind+time)[:6]
      Kind     string   // plan | case | user_fact | repo_fact | dead_end | binding
      Text     string   // a memória (uma linha; o que se recupera)
      Keys     []string // termos indexados: pcTerms(text) + stems de arquivos + intent
      Files    []string // paths relacionados (join com nameindex)
      Intent   string   // intenção que a gerou ("" p/ user_fact)
      Repo     string   // lrRepoSig quando gravada
      Src      string   // verified | observed | user | improve | forge (peso de importância)
      Wins, Fails int
      Created, LastAccess, ValidFrom, Superseded int64 // unix
      Session  string   // proveniência (sessão que a produziu)
  }
  ```
  Substitui/absorve: proposta C de `offline-on-device-intelligence.md` (case binding vira
  `Kind=case`/`binding`), fatos do askfacts aprendidos (`repo_fact`), dead ends já
  registrados em `pcPlan.Dead` (`dead_end` próprio, recuperável isolado), e fatos do
  usuário (`user_fact`). Planos ficam onde estão (plancache.go continua dono deles); o
  memstore lê `pcLoad` como mais uma fonte ranqueável — um índice, várias lojas, um score.
  Índice invertido em RAM `map[string][]uint32` reconstruído quando o mtime do JSONL muda
  (o mesmo truque de `niGet`); persistência append-only + compactação à la Tantivy (§4-F).
- **Arquivos:** novo `memstore.go` (~350–450 ln), `memindex` embutido (postings+stats
  cacheados); leitores em `askfacts.go`, `plancache.go`, `brain.go`.
- **Custo no 386:** 2k entradas ≈ 600 KB JSONL + ~1 MB RAM de índice (estimativa);
  rebuild do índice ~50–200 ms amortizado por sessão; query O(#termos·posting) <1 ms.
- **Métrica:** habilita todas as demais; sozinho: hit@1/hit@3 na suíte §4-I, RSS +≤2 MB.
- **Impacto 5 · Esforço M · Risco baixo-médio** (veneno → tratar como `lsRender`:
  `pfSafeRead` no projeto, `h2PoisonReason` na escrita/leitura, `injScan`/`untrusted`
  ao injetar, `Trust(root,"memory")` no arquivo do projeto como `approvals.go`).

### B. Write path determinístico: a memória que se grava sozinha (sem LLM)

- **Mecanismo.** No fim de turno/tarefa **verificada** (mesmo gate de `acTurnDone`), extrair
  entradas — como `pcBuild`, mas granulares (LongMemEval: session decomposition):
  (i) `binding`: slot textual → alvo resolvido ("o teste do parser"→`parser_test.go`) quando
  a resolução veio de menu/typo-confirm — a escolha do usuário é ouro;
  (ii) `repo_fact`: comando que passou e é detectável de novo ("make test-ci" saiu verde;
  `afTestCommand` hoje só tem tabela estática — a resposta aprendida vira fonte `verified`);
  (iii) `dead_end`: comando que falhou e nunca passou depois (já existe a lógica em
  `pcBuild` — gravar como entrada própria, não só dentro do plano);
  (iv) `case`: {intent, shape dos slots, método, verify} em `tkSucceeded` (proposta C
  absorvida). Tudo com `Src` e `Session` para audit. Sem LLM em lugar nenhum: o "fato" é o
  efeito observado + texto do pedido, `redactSecrets` antes de gravar.
  Escrita explícita: a entrada só vai para o disco após sucesso verificado ou palavra do
  usuário — **nunca por declaração do modelo** (mesma regra do plan cache).
- **Arquivos:** `memstore.go` (extração), `brain.go` (hook em `brRunIntent`/`acTurnDone`),
  `methods.go` (bindings escolhidos via menu → `resolve_name` retorna o par),
  `plancache.go` (dead ends migrados para o formato comum).
- **Custo no 386:** 1–4 appends por turno verificado; <1 ms + fsync.
- **Métrica:** cobertura de recall (fração de tarefas repetidas servidas por binding/case —
  AgentReuse reporta ~30% de pedidos similares, `tocomas-plan-reuse.md`), precisão de
  extração ≥95% (auditar amostra), comandos/tarefa ↓.
- **Impacto 5 · Esforço M · Risco médio** (extração errada → fato falso memorizado; mitigar
  com `Src=verified` só em gate objetivo + supersede).

### C. Score de retrieval = recency × importance × relevance, versão objetiva

- **Mecanismo.** Porte direto de Generative Agents com importância honesta:
  `score = w_rel·BM25norm + w_rec·exp(−Δt/H) + w_imp·imp` com tudo min-max por consulta.
  imp por `Src`: user/pinned=1.0, verified=0.8, observed=0.5, improve/forge=0.6;
  wins−fails modula ±0.2 (cap). H (meia-vida) por kind: binding 30 d, repo_fact 90 d,
  dead_end 60 d, user_fact ∞ (pinned até o usuário despinhar), plan usa a do plancache.
  Guardas do `pcMatch` mantidos (≥2 termos ou cobertura ≥40%). LastAccess atualizado em
  read (MemoryBank: recall reforça — S+=1 → aqui, `LastAccess=now`, decay reinicia).
- **Arquivos:** `memstore.go` (`memRank`), reusa `pcTerms`/`pcDocTerms`-like doc builder.
- **Custo no 386:** aritmética por entrada candidata; <0,5 ms.
- **Métrica:** stale-rate (fatos vencidos servidos) ↓ vs recency=0; precisão de ranking na
  suíte §4-I; `false_success`=0 preservado (memória errada nunca executa — só informa).
- **Impacto 4 · Esforço S · Risco baixo** — mas honesto: os pesos w_* são estimativa
  inicial; calibrar pelo cego da suíte de memória, não por feeling.

### D. Injeção: core block + reminder por turno + memória como fato local

- **Mecanismo.** Três pontos, todos append-only/cache-safe:
  (1) *core memory* — top-N `user_fact`+`repo_fact` pinned (≤ ~400 chars) no bloco de
  sessão ao lado das lições (`lsPromptBlock` análogo: bytes estáveis por sessão — o
  working context do MemGPT, sem o LLM editá-lo; quem edita é `jazz mem`/intento `pin`);
  (2) *reminder* — top-k≤3 por BM25 do pedido, no mesmo slot de `pcInject`/`niInject`
  (`agent.go:466`), formato `memRender` igual a `pcRender` com proveniência
  ("sessão X, verificado", "você disse em 12/10");
  (3) *no brain offline* — sem modelo para injetar, a memória serve: `askfacts` consulta
  `repo_fact` aprendidos antes de perguntar (ex.: "qual runner?" respondido pela memória
  verificada, não só pela tabela); menus de `choose` reordenam com `binding` ("da última
  vez você quis `labbox`" primeiro, com motivo — SHRDLU rule mantida: 1→age, 2-3→pergunta
  com a memória como evidência, 0→honesto).
- **Arquivos:** `memstore.go` (`memRender`, `memCoreBlock`), `agent.go` (hook na linha
  466), `askfacts.go` (fonte extra), `methods.go` (ordem de `choose`), `lessons.go`
  (render ao lado).
- **Custo no 386:** 1 query BM25 por turno; +≤600 tok no modelo, ~0 no brain.
- **Métrica:** no_llm_rate ↑ (recall vira resposta, não pergunta), perguntas necessárias ↓,
  steps até o primeiro `read` relevante ↓ (paralelo ao prefetch).
- **Impacto 4 · Esforço M · Risco médio** (memória injetada errada ancora — mitigar com
  "hint, not instruction" + proveniência + `injScan`, mesma postura de `pcRender`).

### E. Intents `remember`/`forget`/`recall` + `jazz mem`

- **Mecanismo.** Três intenções novas no dataset: `remember` ("lembra que o lint aqui é
  `make check`", "guarda que eu prefiro pnpm") → `user_fact`/`repo_fact` com `pinned`;
  `forget`/"esquece X" → BM25 lookup → 1 candidato = tombstone imediato, 2+ = menu com a
  memória mostrada; `recall`/"o que você lembra de X" → query + resposta direta com
  proveniência (o `memory_query` que falta para o modo offline responder sem modelo).
  `jazz mem [list|forget|pin|edit]` no CLI para inspeção (a "memória declarada" que
  `ux-offline-flow-continuity.md` I pediu na UX — aqui é o mecanismo por trás).
- **Arquivos:** `nlu/intents.json` (+~30 exemplos, PT/EN), `methods/*.json` (3 métodos
  caps `mem_store`/`mem_delete`/`mem_query`), `memstore.go`, `main.go` (subcomando).
- **Custo no 386:** trivial; NLU sem custo extra mensurável.
- **Métrica:** cobertura da suíte §4-I; perguntas ↓ em tarefas "o que era mesmo".
- **Impacto 4 · Esforço S-M · Risco baixo** (memória declarada errada existe mas é do
  usuário — corrigível com `forget`; privacidade: `redactSecrets` na escrita).

### F. Esquecimento e consolidação: decay, caps, tombstones, `mem compact`

- **Mecanismo.** Camadas à la Lucene/MemoryBank, sem LLM:
  (1) decay no ranking (§4-C) — esquecer primeiro é ranquear pior, não apagar;
  (2) tombstone real: `forget`, supersede por contradição (mesma `Kind`+sujeito-chave,
  mais novo vence — Zep-lite determinístico), e poison detectado;
  (3) caps por kind (ex.: 300 case, 200 repo_fact, 100 user_fact, 150 dead_end —
  estimativa inicial) com evict por (score ascendente, mais antigo), como `pcSave` já faz;
  (4) `jazz mem compact` (e hook no fim de sessão): reescreve o JSONL sem tombstoned,
  merge de quase-duplicatas (Jaccard de `Keys` ≥0,7, regra já em `pcStore`), re-ordena —
  é o merge de segmentos do Tantivy, em ~ms;
  (5) entre sessões, a consolidação *semântica* é trabalho de forja: `jazz improve`/LLM
  forte resume clusters em fatos canônicos quando online — sleep-time compute
  (arXiv:2504.13171) no nosso vocabulário; sem online, (1)–(4) bastam.
- **Arquivos:** `memstore.go` (compact/merge/supersede), `improve.go` (opção de forja),
  `main.go` (subcomando).
- **Custo no 386:** rewrite completo <10 ms para 2k entradas; roda em idle/fim de sessão.
- **Métrica:** stale-rate ↓, tamanho do store estável, hit@k mantido após N dias (suíte
  temporal §4-I).
- **Impacto 3 · Esforço S · Risco baixo** — cuidado: decay agressivo joga fora memória
  sazonal ("deploy mensal"); pinned e `Src=verified` têm meia-vida longa por isso.

### G. Validade temporal e consultas com escopo de tempo

- **Mecanismo.** Campos já no schema (`ValidFrom`/`Superseded`); consultas "da última
  vez"/"ontem"/"semana passada"/"em março" viram filtro/janela determinística de datas
  (parser de datas relativas, PT/EN — ~100 ln, sem LLM) sobre `LastAccess`/`Created`;
  "o que mudou desde X" = listar entradas com `Created>X`. Motivo: temporal é a categoria
  mais fraca do campo (LoCoMo: gap 73% p/ humanos; LongMemEval tem categoria própria e a
  otimização "time-aware expansion" foi das que mais pagaram) — barato para nós porque o
  escopo é estreito (datas, não causalidade).
- **Arquivos:** `memstore.go` (`memTimeScope`), `nlu.go` (slot `when` no intent `recall`),
  `methods/*.json`.
- **Custo no 386:** filtro O(N); desprezível.
- **Métrica:** acurácia na categoria temporal da suíte §4-I.
- **Impacto 3 · Esforço S · Risco baixo.**

### H. Expansão por grafo estrutural (HippoRAG-lite, sem embeddings)

- **Mecanismo.** Arestas derivadas, não aprendidas: entrada↔arquivo (`Files`),
  entrada↔intent, entrada↔termo de alto IDF (cap de grau, só termos com df≤10%N),
  arquivo↔arquivo (teste via `niCore`, co-touched na mesma sessão). Retrieval: seeds =
  top-k BM25 → +1 salto nos vizinhos com score×0,5 (ou PPR com damping 0,5 — ~40 ln —
  quando o grafo justificar; a versão 1-salto já captura "a memória que fala do arquivo
  que este caso tocou"). A ablação de A-MEM (MultiHop 27→9,7 sem links) diz que a estrutura
  vale — mas o nosso link é determinístico.
- **Arquivos:** `memstore.go` (`memGraph`, lazy build como `symbols()`), `nameindex.go`
  (arestas arquivo↔arquivo), `evaloffline.go` (métrica hit@k antes/depois).
- **Custo no 386:** grafo de ~2k nós/10–50k arestas ≈ 1–3 MB, build <200 ms lazy, query
  +O(k·grau).
- **Métrica:** hit@k em consultas multi-hop da suíte (ex.: fato sobre arquivo + caso que o
  tocou); honesto: pode não pagar — medir e descartar se Δ<2 pontos.
- **Impacto 3 · Esforço M · Risco médio** (ruído de aresta espúria → cap de grau + peso
  decaído; decisão por A/B na suíte).

### I. Suíte `memória` no `jazz eval offline` + métricas de retrieval

- **Mecanismo.** Categoria nova em `evals/offline/tasks.json` (~15 tarefas): fixture
  escreve `.jazz/memory.jsonl` semeado (fatos, um caso, um fato vencido) → prompts:
  "o que você lembra do parser", "lembra que o lint é `make check`" + segunda task que o
  usa, "esquece o fato X", "qual runner de teste" respondido só por memória aprendida,
  "da última vez, o que mexemos" (temporal), fato contraditório (supersede vence).
  Métricas novas no `eoSummary`: hit@1/hit@3 de `recall`, precisão de `forget`
  (tombstone certo), stale-rate, e os gates de sempre (`false_success`=0,
  `false_blocks`=0, p50, RSS).
- **Arquivos:** `evals/offline/tasks.json`, `evaloffline.go` (fixture seed + campos),
  `eoFixture` ganha `mem_seed` opcional.
- **Custo no 386:** zero em runtime.
- **Métrica:** é a métrica — sem ela, §4-A..H são opinião.
- **Impacto 3 · Esforço S · Risco nenhum.**

---

## 5. O que NÃO adotar (e por quê)

- **Embeddings/vetores em qualquer ponto do runtime** — não cabem no orçamento (P9) e não
  mostraram ganho onde conta (grep inline > vector inline 10/10, com ressalvas; BM25 basta
  para a escala do jazz). Inclui all-MiniLM "só 90 MB": é dependência de modelo no caminho
  crítico, ponto final.
- **Memória gerenciada pelo LLM (paging/escrita/evolução por function call)** — MemGPT,
  A-MEM, Mem0: o mecanismo de decisão é o modelo. Offline é dependência proibida; no
  caminho online continuar como está (o modelo já decide o que faz com tools próprias).
- **Reflection/evolução em runtime** — consolidação semântica fica no sleep-time
  (`jazz improve`/forge entre sessões), nunca por turno.
- **Grafo de conhecimento com NER/OpenIE de LLM (Zep/Graphiti, HippoRAG-index)** — custo de
  construção por escrito é LLM; nosso grafo é só de arestas estruturais (§4-H) e opcional.
- **Importância por nota subjetiva** — substituída por wins/fails verificados + pin; a
  versão LLM-1-10 é opinião, a nossa é medida (para o nosso domínio, melhor).
- **Índice de conteúdo por trigrama *para o memstore*** — overkill em ~2k docs de uma
  linha (BM25 por palavra basta); trigrama continua válido para conteúdo de **repo**
  (proposta B já registrada, não repita).
- **Lucene/Tantivy embutido via cgo/Rust** — binário 386, CGO, memória; o memstore
  append-only+merge cobre o necessário com ~0 deps.
- **Apagar sem tombstone** — "esquecer" não-auditável corrói confiança e apaga evidência;
  tombstone + `jazz mem` mostra o que saiu (precedente: `approvals.go` lista, `lessons.md`
  tem changelog).
- **Reescrever histórico de sessão para "economizar" memória** — sessions são recall
  storage append-only e evidência de audit; fatos vão ao memstore, sessão fica imutável.

---

## 6. Fontes

Papers de memória: Packer et al., *MemGPT*, arXiv:2310.08560 (ICML 2024) · Xu et al.,
*A-MEM*, arXiv:2502.12110 (NeurIPS 2025; código github.com/WujiangXu/A-mem) · Park et al.,
*Generative Agents*, arXiv:2304.03442 (UIST 2023) · Jiménez Gutiérrez et al., *HippoRAG*,
arXiv:2405.14831 (NeurIPS 2024; código OSU-NLP-Group/HippoRAG) · Zhong et al.,
*MemoryBank*, arXiv:2305.10250 (AAAI 2024) · Chhikara et al., *Mem0*, arXiv:2504.19413
(ECAI 2025) · Rasmussen et al., *Zep*, arXiv:2501.13956 (Zep AI — self-reported) ·
Maharana et al., *LoCoMo*, arXiv:2402.17753 (ACL 2024) · Wu et al., *LongMemEval*,
arXiv:2410.10813 (ICLR 2025) · Lin et al., *Sleep-time Compute*, arXiv:2504.13171
(Letta/UC Berkeley).

Retrieval clássico: Robertson & Zaragoza, FnTIR 3(4) 2009 (BM25; variante idf+1 do Lucene
= `plancache.go:286`) · Cox, "Regular Expression Matching with a Trigram Index",
swtch.com/~rsc/regexp/regexp4.html (2012) · Zoekt design.md (positional trigram, ~3×
corpus, sub-50 ms/2 GB) · BurntSushi, "ripgrep is faster than…" (set/2016) · Tantivy
ARCHITECTURE.md + fulmicoton "Behold Tantivy" (segmentos imutáveis, tombstones, merge,
FST+bitpack/varint). Reuso/casos já analisados: AgentReuse arXiv:2512.21309 e APC
arXiv:2506.14852 em `tocomas-plan-reuse.md`; AWM arXiv:2409.07429 e Aamodt & Plaza 1994
(CBR) em `offline-on-device-intelligence.md`.

Docs internos relacionados (não repetir): `offline-on-device-intelligence.md` (P9,
propostas B/C), `tocomas-plan-reuse.md`, `grep-is-all-you-need.md`, `harness-sota-gap.md`
(memória dos SOTA), `ux-offline-flow-continuity.md` (memória declarada na UX),
`math-offline-brain.md` (calibração de resolução), `low-level-performance.md` (índice
persistido/SoA no d610), `offline-brain-research.md` (§C repomap/co-mudança).
