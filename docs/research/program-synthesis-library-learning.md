# Síntese de programa e library learning para forjar assets (`forge.go`)

Pesquisa de 04/11/2026 para a 0.9.x. Continua
[offline-brain-research.md](offline-brain-research.md) (HTN/BT/NLU/índices — implementado),
[offline-on-device-intelligence.md](offline-on-device-intelligence.md) (propostas A–H; a A já nomeava
`jazz forge` como stub "1 chamada tool-less → candidatos"), [metaharness-self-improvement.md](metaharness-self-improvement.md)
(portão held-out, superfície editável), [tocomas-plan-reuse.md](tocomas-plan-reuse.md) (cache de planos,
receitas) e [math-offline-brain.md](math-offline-brain.md) (limiares/calibração). Este doc entrega a
**máquina de síntese** que aquela proposta A deixou em aberto: como os traços verificados e os misses do
dono viram **scripts, parsers e métodos determinísticos validados** — program synthesis e library
learning, não "LLM escreve JSON e a gente confia". Nada aqui repete o que os docs irmãos fundamentaram
(HTN-MAKER, Korf, APC/AgentReuse, Self-Harness/DGM, calibração): cito o doc, não o paper.

Convenções: cada afirmação traz fonte primária (arXiv id / DOI / venue). **(estimativa)** = número meu,
não medido. `file:line` refere-se à branch `harness/offline-primitives` @ `584494d`. Alvo: Dell
(i386, 1 CPU, 2 GB, linux/386, Go estático). Regra de ouro (vigente): **LLM no caminho crítico de
runtime = dependência, não asset** — LLM só pode produzir o artefato uma vez; o artefato roda para
sempre sem token.

---

## 1. O problema que este doc resolve

O cérebro offline resolve exatamente o que a biblioteca cobre: 50 intenções gramáticais
(`nlu/intents.json`, 644 exemplos), 46 arquivos de método (`methods/*.json`), 24 capacidades Go +
`script:*` (`methods.go:303-343`) e 19 scripts (`jazz-scripts/`). Tudo isso foi escrito à mão. A
cobertura é o produto — cada miss (`brNotUnderstood`, `brain.go:214-222`; `brHeadlessLevel0` devolve
`nil` e vai pro modelo, `brain.go:604-643`) é uma tarefa que custa uma requisição de 20–60 s num modelo
grátis, ou uma tela de "não entendi" no Dell.

O corpus de treino já existe e está sendo gravado hoje: eventos `task` no JSONL da sessão
(`tkSessionSink`, `task.go:258-284`), stream de eventos `.events.jsonl` (`obs.go:201` `obEmit`),
planos verificados (`acTurnDone`/`pcBuild`, `plancache.go:377-552`), sessões carregáveis por
`imLoadSession`/`imProjectRuns` (`improve_load.go:68-156, 421-445`) e as escolhas do usuário em menus
(`offers.go`). A pergunta deste doc: **que algoritmos transformam esse corpus em assets executáveis
validados, com portão held-out, rodando o mineiro no próprio Dell?**

A tese: o pipeline certo é o do DreamCoder — *resolver → comprimir → reusar mais rápido* — com cada
componente neural trocado por um determinístico que cabe num Pentium M, e com a verificação objetiva
existente (`brVerify`, `eoOne`) no lugar que o Snorkel reservava ao modelo generativo.

---

## 2. Papers e técnicas (número-chave + limitação por item)

### 2.1 DreamCoder — wake/sleep com library learning (arXiv:2006.08381)

Ellis, Wong, Nye, Sablé-Meyer, Morales, Cary, Hewitt, Solar-Lezama, Tenenbaum. Preprint 2020; versão
de revista *Phil. Trans. R. Soc. A* 381:20220050 (2023); antecessor direto EC² ("Learning Libraries of
Subroutines for Neurally-Guided Bayesian Program Induction", NeurIPS 2018).

**Mecanismo.** Três fases em loop: **wake** resolve tarefas por enumeração dirigida por tipo, podada
por um modelo neural de reconhecimento treinado nos pares entrada/saída; **abstraction-sleep**
comprime os programas achados via refatoração/anti-unificação, promovendo fragmentos reutilizados a
novos primitivos da DSL aprendida (objetivo: descrição mínima, MDL); **dream-sleep** treina a rede de
reconhecimento em "fantasias" amostradas da gramática aprendida — a biblioteca é o modelo generativo,
a rede aprende a invertê-lo.

**Números-chave.** Edição de texto (108 problemas SyGuS 2017): **3,7% → 79,6%** de resolvidos em
orçamento de 10 min após aprendizado (tempo médio de busca 235 s → 40 s); no orçamento da competição
(1 h, 8 CPUs) o DreamCoder resolve **84,3%** vs 82,4% do CVC4, melhor sintetizador do campeonato.
Listas: 218 tarefas 50/50 treino/teste, ~20 rotinas aprendidas (filter → máximo → n-ésimo maior →
sort); o sort é achado em ≤ 10 min com a biblioteca vs ~10^72 anos de busca bruta (5 chamadas vs 32
primitivos). Média 54,1 s / mediana 15,0 s por tarefa resolvida; ~1 dia de treino em **20–100 CPUs**
por domínio. Nos domínios criativos (LOGO, torres) nenhuma alternativa passa de 60% em held-out contra
~100% do sistema completo.

**Ablações que importam para nós.** (a) Sem library learning a cobertura cai forte — a biblioteca é o
ganho, não a rede; (b) baseline **Memorize** (soluções inteiras na biblioteca, sem refatoração)
performa mal: **guardar o traço cru ≠ aprender o método** — a compressão é o ato de aprendizado;
(c) cada componente contribui (a rede acelera, a biblioteca viabiliza), e os dois se bootstrapam.

**Limitação.** 20–100 CPUs × ~1 dia por domínio, e a busca é enumeração exponencial amortizada por
rede neural: incompatível com 1 núcleo e com Go stdlib 386 (sem inferência NN). O que transfere é a
**forma do loop** — corpus de soluções → compressão por MDL → biblioteca cresce → problemas antes
caros viram baratos — e a lição Memorize: `plancache.go` guarda prosa, não abstração.

### 2.2 Stitch — síntese top-down guiada por corpus (arXiv:2211.16605)

Bowers, Olausson, Wong, Grand, Tenenbaum, Ellis, Solar-Lezama. POPL 2023 (PACMPL 7:1182-1213,
[DOI 10.1145/3571234](https://doi.org/10.1145/3571234)).

**Mecanismo.** Em vez de reescrever bottom-up como o DreamCoder, o Stitch busca a abstração λ
**top-down**, guiada pelo corpus: expande a forma da abstração candidata e usa casamento de padrão
sintático das abstrações intermediárias para podar o que não pode aparecer no corpus. O critério é
compressão: a biblioteca que mais reduz o tamanho total corpus+biblioteca.

**Números-chave.** **3–4 ordens de magnitude mais rápido e 2 ordens menos memória** que o aprendizado
dedutivo de biblioteca do DreamCoder, com compressividade comparável ou melhor; escala para corpora de
centenas de programas complexos intratáveis antes; **robusto a parada antecipada** (early stopping
degrada suavemente) — ou seja, o algoritmo certo para orçamento apertado: corta-se quando acaba o tempo.

**Limitação.** Opera sobre expressões funcionais (λ-cálculo/β-redução) com correspondência sintática —
nosso corpus são *sequências* de chamadas de capacidades com efeitos e slots, não termos λ. A versão
jazz usa a ideia (busca guiada por corpus, objetivo MDL, early-stop) sobre um problema mais simples:
subsequências quentes de uma linha de tokens (§5.C).

### 2.3 babble — library learning módulo teoria (arXiv:2212.04596)

Cao, Kunkel, Nandi, Willsey, Tatlock, Polikarpova. POPL 2023
([DOI 10.1145/3571207](https://doi.org/10.1145/3571207)).

**Mecanismo.** LLMT: e-graphs + saturação por igualdade com uma teoria equacional do domínio +
**anti-unificação sobre e-classes** para gerar candidatos a abstração robustos a variação sintática.
Anti-unificação (Plotkin, "A note on inductive generalization", *Machine Intelligence* 5, 1970;
Reynolds, mesma edição) é o dual da unificação: o padrão mais específico que casa dois termos —
`2+1` e `3+1` → `?x + 1`.

**Números-chave.** Compressão melhor que o estado da arte **ordens de magnitude mais rápido**;
aprende funções reutilizáveis em entradas antes fora de alcance.

**Limitação.** Exige uma teoria equacional por domínio (quem escreve as regras?) e infraestrutura de
e-graphs (egg/Rust) — dependência pesada e sem porte para Go/386. Registrado em §7 (não adotar); a
anti-unificação **sintática** simples — sem teoria — é a parte que reaproveitamos para generalizar
args de chamadas (§5.C/E).

### 2.4 LILO — LLM documenta a biblioteca aprendida (arXiv:2310.19791)

Grand, Wong, Bowers, Olausson, Liu, Tenenbaum, Andreas. ICLR 2024.

**Mecanismo.** Library Induction from Language Observations: síntese guiada por LLM + busca
enumerativa, compressão via Stitch, e **AutoDoc** — o LLM escreve nome e docstring de cada abstração
a partir de exemplos de uso.

**Números-chave.** Supera DreamCoder em edição de strings, raciocínio de cena e composição gráfica;
AutoDoc **melhora o desempenho de síntese** — nomes legíveis ajudam o sintetizador a usar as
abstrações certas.

**Limitação + lição.** O LLM entra no *produtor* (nomeia/documenta o que a compressão achou), não no
*produto*. Para o jazz: o campo `doc` de `mhMethod` (`methods.go:63`) e `description` de `scDef`
(`scripts.go:56`) cumprem o mesmo papel — o dono e o futuro `jazz forge` leem a biblioteca por esses
textos. AutoDoc-lite = uma chamada tool-less em tempo de forja (mesma forma de `hdWrite`,
`plancache.go:556-631`), com template determinístico como fallback.

### 2.5 DSPy — compilar pipelines LM em programas auto-melhorantes (arXiv:2310.03714)

Khattab et al., Stanford, 2023.

**Mecanismo.** Módulos declarativos LM → o compilador otimiza prompts/demos/pesos contra uma métrica.
O otimizador central (BootstrapFewShot e sucessores) **gera demonstrações a partir dos próprios traços
do pipeline que passam na métrica** e seleciona por desempenho no dev set. É a regra de ouro
formalizada: o LLM produz, a métrica decide, o compilado roda.

**Números-chave.** Pipelines compilados superam few-shot manual em **>25% (GPT-3.5) e >65%
(llama2-13b-chat)** e demos de especialistas em **5–46% e 16–40%**; programas compilados para
T5-770M/llama2-13b ficam competitivos com chains de especialista em GPT-3.5.

**Limitação.** Pressupõe chamadas LM no pipeline compilado — aqui o "pipeline compilado" é
**zero-LM**; o que transfere: (a) o traço só vira asset se passou na métrica (nosso `verified`,
`plancache.go:381-389`); (b) seleção em dev set, nunca no que serviu de geração (portão §5.G);
(c) "compilar uma vez" como unidade de custo — `jazz forge` é nosso compiler pass.

### 2.6 Snorkel e Snuba — supervisão fraca e heurísticas sintetizadas

**Snorkel** (Ratner, Bach, Ehrenberg, Fries, Wu, Ré. PVLDB 11(3):269-282, 2017;
arXiv:1711.10160). Usuários escrevem **labeling functions** (LFs) de acurácia e correlação
desconhecidas; um modelo generativo estima as acurácias **sem ground truth** e produz rótulos
probabilísticos que treinam o modelo final. Números: +132% médio sobre heurísticas anteriores; a
3,60% do desempenho de rotulagem manual massiva; estudo com usuários: 2,8× mais rápido, +45,5%.

**Snuba** (Varma & Ré, PVLDB 12(3):223-236, 2019;
[DOI 10.14778/3291264.3291268](https://doi.org/10.14778/3291264.3291268)). Sintetiza heurísticas
automaticamente: com um conjunto pequeno rotulado + um grande não rotulado, cada heurística gerada
**rotula só o subconjunto em que é acurada** (abstenção); o laço sintetiza → seleciona → verifica até
cobrir o corpus, com garantia estatística de parada antes de degradar. Números: < 5 min por tarefa;
até +9,74 F1 sobre as melhores heurísticas escritas à mão; +14,35 F1 sobre semi-supervisionado.

**Limitação.** O modelo generativo do Snorkel existe para estimar acurácia de LF **na ausência de
verdade** — e nós temos verdade: `tkSucceeded` com verificador passando é ground truth, e para misses
a "verdade" é a escolha do dono. O que transfere: (a) tratar regras/NLU/LLM como LFs fracas que votam
— e onde votam errado, abstêm; (b) Snuba é o molde exato do minerador de misses (§5.D): sintetizar a
regra que cobre **só onde ela acerta**, iterar, parar antes de degradar; (c) abstenção como cidadã de
primeira classe — já é nosso `nuMinConf`/`brLevel0Min` (`nlu.go:45`, `brain.go:596`; curva
risco-cobertura em math-offline-brain.md §P4).

### 2.7 FlashFill, FlashMeta/PROSE e ranking — PBE industrial

**FlashFill** (Gulwani, POPL 2011,
[DOI 10.1145/1926385.1926423](https://doi.org/10.1145/1926385.1926423)). Síntese de transformações de
string a partir de 1–3 exemplos entrada→saída numa DSL fechada (Substring, ConstPos, Case,
Concatenate, Loop, Position-por-regex). A técnica é **version space algebra** — a resposta é um
conjunto comprimido de todos os programas consistentes (DAG), não um programa — com ranking por
"mais simples/mais provável". Sintetiza em fração de segundo; detecta ruído; modo interativo pede
output nos inputs ambíguos. A exigência do Excel — **fração de segundo + um exemplo na maioria dos
casos** — virou ~1 milhão de invocações/semana.

**Ranking aprendido** (Singh & Gulwani, "Predicting a Correct Program in Programming by Example",
CAV 2015): função de ranking supervisionada corta a média de exemplos necessários de **4,17 → 1,48** e
resolve **74% dos benchmarks com um único exemplo** — a lição: no PBE o ranking é tão importante
quanto a busca.

**FlashMeta/PROSE** (Polozov & Gulwani, OOPSLA 2015, "A Framework for Inductive Program Synthesis").
O meta-algoritmo **D4** (data-driven domain-specific deduction): deduz para trás a especificação de
cada parâmetro de operador dado o exemplo no operador inteiro — **witness functions = semântica
inversa** escrita uma vez por operador da DSL. 10+ aplicações industriais (FlashExtract, FlashRelate,
etc.) como instâncias do mesmo esqueleto.

**Limitação.** PBE só existe dentro de uma DSL projetada para isso: cada operador precisa de
witness/semântica inversa; fora da DSL não há síntese. Para o jazz a DSL correta não é "código" — é a
**linguagem dos métodos e dos args**: generalizar `args` observados em slots (`$slot.target`,
`$hint`) é a nossa version space; e os parsers de `outparse.go`/`jazz-scripts/lib/testparse.py` são o
alvo natural de um PBE-lite de extração (§5.E).

### 2.8 LatPlan — aprender o modelo de ação a partir de transições (arXiv:1705.00154)

Asai & Fukunaga, AAAI 2018 ("Classical Planning in Deep Latent Space").

**Mecanismo.** Dado pares de imagens (transições) sem rótulo: State Autoencoder → representação
proposicional; **Action Model Acquisition** aprende os símbolos de ação e seus **modelos implícitos
(pré-condições/efeitos)** das transições; um planejador clássico resolve no PDDL aprendido. Avaliado
em 8-puzzle, Hanoi, LightsOut.

**Limitação.** A implementação é neural (VAE/AMA) — fora de questão no Dell. Mas a *forma* do
problema é exatamente a nossa e já é simbólica: nossas transições `(fat antes, cap(args), fat depois,
exit, obs)` vêm tipadas do executor (`brExecute` registra via `task.Fact`/`AddEvidence`,
`task.go:184-197`). "Aprender o modelo de ação" vira **contagem determinística**: os fatos que sempre
precedem `cap X` com sucesso são candidatos a `pre`; os bins necessários, a `needs`; a checagem que
separou sucesso de falha, a `verify` (§5.F). Nada de autoencoder.

### 2.9 Indução de gramática — ADIOS, Sequitur, fusão bayesiana

**ADIOS** (Solan, Horn, Ruppin, Edelman, "Unsupervised learning of natural languages", PNAS
102(33):11629-34, 2005; [DOI 10.1073/pnas.0409746102](https://doi.org/10.1073/pnas.0409746102)).
Corpus → grafo de caminhos; o procedimento MEX escolhe **padrões significativos** por fluxo
contexto-sensitivo; generalização estruturada produz classes de equivalência; o resultado é uma CFG
(padrões + classes + léxico) que gera frases novas gramaticais — insupervisionado, avaliado em
gramáticas artificiais de milhares de regras e em PT/EN/proteínas.

**Sequitur** (Nevill-Manning & Witten, "Identifying Hierarchical Structure in Sequences: A
Linear-time Algorithm", JAIR 7:67-82, 1997). Compressão gramatical online O(n): duas restrições —
**digram uniqueness** (todo digrama ocorre uma vez) e **rule utility** (toda regra usada ≥ 2×) —
inferem hierarquia de um stream único. É o algoritmo de compressão de subsequências mais barato que
existe: uma passada, sem corpus em RAM além do próprio stream.

**Bayesian model merging** (Stolcke & Omohundro, "Inducing Probabilistic Grammars by Bayesian Model
Merging", ICGI-2/LNAI 862, 1994). Funde estados de um HMM/gramática inicial maximizando a posterior —
o ancestral bayesiano do "comprimir = aprender".

**Leitura para o jazz.** `nlu/intents.json` **já é uma gramática semântica** (conceitos, frases de
conceito, `@kind`, slots) — induzir regras novas = o problema ADIOS restrito: no corpus de misses
tokenizados, o subpercurso compartilhado significativo vira padrão, e as posições variantes de mesmo
tipo viram classe de equivalência = slot (§5.D). E a compressão de traços usa a forma Sequitur sobre o
stream de chamadas de capacidade (§5.C). BMM/PCFG: §7, não adotar — precisão importa mais que
generalidade num corpus de dezenas de frases.

### 2.10 MemPrompt — misses do dono como memória de correção (arXiv:2201.06009)

Madaan et al., EMNLP 2022 ("Memory-assisted Prompt Editing with User Feedback"). Memória de pares
`(consulta mal-entendida → feedback do usuário)` recuperada em consultas novas parecidas — o usuário
não precisa saber a resposta, só que o *entendimento* estava errado. No jazz: a correção do dono
(menu escolhido, typo confirmado, reformulação após "não entendi") é o dado rotulado mais valioso que
existe — Snuba o trata como rótulo caro a maximizar; nós o tratamos como prova para promover (§5).

### 2.11 O que já está fundamentado nos docs irmãos (citado, não repetido)

Macro-operadores de Korf 1985, EBL (DeJong & Mooney 1986), HTN-MAKER (Hogg et al., AAAI 2008) e CBR
(Aamodt & Plaza 1994): [offline-on-device-intelligence.md](offline-on-device-intelligence.md) §P6.
APC/AgentReuse e receitas: [tocomas-plan-reuse.md](tocomas-plan-reuse.md) §3. Portão held-in/held-out
do Self-Harness, superfície editável `harness.json`, lição DGM (checker escondido):
[metaharness-self-improvement.md](metaharness-self-improvement.md). Limiar de abstenção (Chow) e
risco-cobertura: [math-offline-brain.md](math-offline-brain.md) §P4. Voyager (skill library com LLM):
[harness-first-offline.md](harness-first-offline.md).

---

## 3. Regra de ouro: o que roda OFFLINE sem LLM vs o que é dependência

| componente | offline no Dell? | observação |
|---|---|---|
| mineração de traços/misses (`imScan`, `imLoadSession`, obEvents) | sim | já existe (`improve_load.go:334-445`) |
| compressão de subsequências (Sequitur/anti-unificação sintática) | sim | nova; O(stream), sem corpus em RAM |
| clusterização de misses + generalização de regra (ADIOS-lite) | sim | nova; `nuCanonText` + n-gramas já existem |
| mineração de `pre`/`needs`/`verify` por contagem (LatPlan-lite) | sim | nova; contadores sobre eventos |
| PBE-lite em DSL fechada (transformações/parsers) | sim | nova; ms por candidato |
| portão held-out (replay em fixture) | sim | nova; reusa `eoFixture`/`brEnv` (`evaloffline.go:302-351`) |
| promoção human-gated + overlays | sim | padrão já existe (`fpIntents`, `scLoad`) |
| AutoDoc (nome/docstring bonitos) | **não** — opcional na forja | dependência; fallback = template determinístico |
| corpo de script novo (python3) | **não** — LLM propõe na forja | dependência no produtor; o .py promovido é asset determinístico no runtime |
| exemplos NLU extra para intenção nova | **não** — LLM ajuda a escrever | dependência; cada exemplo é validado parseando na própria intenção (§5.D) |
| recognition model neural / dreaming | não | **não adotar** (§7) — substituído por NB + nameindex + BM25 |
| e-graphs / saturação / teoria equacional | não | **não adotar** (§7) |
| modelo generativo de acurácia de LF (Snorkel) | não | **não adotar** — `verified` é ground truth |

Síntese: **toda a máquina de produção determinística roda no Dell** (o `jazz forge` inteiro, menos os
opcionais marcados). O LLM, quando usado, é labeling function e redator de documentação **na forja** —
uma chamada tool-less, batelada, com fallback. Nada do que o runtime carrega depende dele.

---

## 4. Mapa no código (file:line — feito / parcial / só pesquisado)

| mecanismo | onde no jazz | estado |
|---|---|---|
| métodos HTN-lite embutidos (name/intent/pre/steps/verify/needs/cmds/risk) | `methods.go:60-72`, `methods/*.json` (46 arquivos) | feito — **sem overlay, sem proveniência/wins** |
| biblioteca só-embed (`mhLibrary` não lê disco) | `methods.go:74-111` | parcial — overlay é proposta A (§6) |
| gate de segurança de método (`cmds` só read-only declarados) | `methods.go:115-122` (`mhBadCmd`), `caps.go:573-594` (`cbReadCmds`) | feito — regra de quarentena do forge |
| executor Pyhop + substituição `$slot`/`$var` | `brain.go:261-426`, `methods.go:220-256` | feito |
| catálogo de verificadores | `methods.go:1680-1763` (`brVerify`) | feito — fonte dos checks mineráveis |
| capacidades Go + dispatch `script:*` | `methods.go:303-343` (`brCaps`/`brCap`/`brScript`) | feito — o alfabeto do corpus de traços |
| recuperação por categoria observável | `recover.go:53-151` (`rvClassify`/`rvTable`/`rvDecide`) | feito — rotula falhas p/ o minerador |
| eventos de tarefa na sessão + stream `.events.jsonl` | `task.go:184-215, 258-284`, `obs.go:200-201` | feito — **o corpus** |
| NLU: gramática+slots+NB, só embed | `nlu.go:151-229` (`nuGet`/`nuCompile`), `nlu/intents.json` | feito — **sem overlay de exemplos/léxico** |
| canonização de prompt p/ clustering | `nlu.go:1075-1090` (`nuCanonText`) | feito |
| overlay de assets (builtin < user < projeto confiável) | `intents.go:251-324` (`fpIntents`), `scripts.go:113-195` (`scLoad`/`loadDir`), `trust.go:91,407` | feito — modelo a copiar p/ métodos e NLU |
| scripts com sidecar validado + policy | `scripts.go:50-103, 382-458` (`scDef`/`scValidate`), `jazz-scripts/` | feito — formato dos scripts forjados |
| plan cache verificado (só prosa p/ modelo) | `plancache.go:244-303` (BM25), `377-460` (`acTurnDone`/`pcScore`/`pcStore`) | feito — `pcBuild` guarda `Steps` como texto, **não executável** |
| hindsight via modelo forte (1 chamada tool-less) | `plancache.go:556-631` (`hdWrite`) | feito — forma do AutoDoc-lite |
| mineração de sessões → candidatos de lição (prosa) | `improve_load.go:68-156, 334-445`, `improve.go:280-575` | feito — loaders e normalização reutilizáveis |
| promoção `--apply` com confirmação | `improve.go:801-843` (`imApply`), `lessons.go:244+` (`lsAppend`) | feito — padrão da UX de promote |
| veneno de plano (audit de strings armazenadas) | `hardening2.go:960-986` (`h2PoisonReason`/`h2PlanPoison`) | feito — auditor reutilizável p/ assets |
| eval offline c/ fixture + Permit/Ask simulados | `evaloffline.go:171-251, 279-410` (`eoRun`/`eoOne`) | feito — o portão held-out (§5.G) |
| log de misses dedicado | — | **falta**: miss hoje é `nil` silencioso (`brain.go:610-612, 624-626, 681`) |
| `jazz forge` / `learn.go` (spec §7) | — | só pesquisado (este doc §5-6) |
| compressão de traços → macro-métodos | — | só pesquisado (§2.1/2.2/2.9 → §6.C) |
| mineração de misses → regras/exemplos | — | só pesquisado (§2.6/2.9 → §6.D) |
| PBE de transforms/parsers | — | só pesquisado (§2.7 → §6.E) |
| mineração de pre/needs/verify | — | só pesquisado (§2.8 → §6.F) |

---

## 5. O fluxo `jazz forge` (design de referência)

`jazz forge [--dry] [--promote] [--self]` — offline, roda no Dell, em lote (noite ou sob demanda).
Nenhum passo chama LLM por padrão; os dois opcionais (AutoDoc, corpo de script) ficam atrás de flag e
têm fallback determinístico.

```
fontes               normalizar           candidatos              portão              promover
────────────────────────────────────────────────────────────────────────────────────────────
sessões/*.jsonl  ┐                       C1 macro-método   ┐      schema+mhBadCmd  ┐
 .events.jsonl   │  fgTrace{intent,      C2 regra+exemplos │      veneno h2        │   human gate
 task events     ├─► slots, steps[]} ──► C3 léxico         ├─►   held-out replay   ├─► overlay
 pcPlans (win)   │  (cap,args,obs)       C4 variante       │      guard=suíte eo   │   (diff+confirma)
 misses (novo)   │                       C5 script (opc.)  │      wins/fails       │   → dataDir()/forge/
 respostas menu  ┘                       C6 pre/needs/ver. ┘      false_success=0   ┘   + .jazz/ (confiança)
```

**Fontes.** (1) `task` events com `tkSucceeded` (`task.go:20-53`); (2) `pcPlan` com wins (`plancache.go:56-76`);
(3) sessões do modelo via `imScan`/`imLoadSession`; (4) **miss log novo** — `obEmit("miss", {text, parse,
channel})` nos três pontos que hoje devolvem silêncio (`brain.go:610`, `:624`, `:681`) + escolhas de
menu e confirmações de typo (o MemPrompt local). Sem (4) o pipeline de misses morre na fonte.

**Normalização — `fgTrace`.** Cada execução → sequência tipada de chamadas: `bash`→`read_cmd|proj_cmd|cmd`
(via `IsReadOnly`/`cbReadCmd`, `winsafety.go:60`, `caps.go:594`), `read`→`read_file`, `edit/write`→`file_op`,
`script`→`script:<nome>`, passos de método → a cap registrada. Args são **anti-unificados contra o que já se
sabe**: valores iguais a `Intent.Slots`, hits de entidade (`niResolveEntity`, `nameindex.go:1035`), caminhos
do índice → placeholders (`$slot.*`, `@file`, `@sym`). Esse é o "termo" do nosso Stitch — mais raso que
λ-cálculo, e por isso mais barato.

**Candidatos.** Seis geradores determinísticos (detalhe em §6.C–F): C1 macro-métodos de subsequências
quentes; C2 regras de intenção + exemplos dos misses; C3 entradas de léxico de correções repetidas;
C4 variantes de método (mesmo intent, outra sequência de caps); C5 scripts (template; corpo opcional
via LLM na forja); C6 `pre`/`needs`/`verify`/`doc` minerados de transições.

**Portão.** Tudo passa por quatro crivos, nesta ordem (barato → caro): (i) schema + `mhBadCmd` +
veneno `h2PlanPoison`; (ii) execução seca em fixture (`eoFixture`, `evaloffline.go:411+`) com
`brEnv`/`Permit` sintéticos — o replay descobre se o candidato *roda*, não se o dono gostou;
(iii) **held-out**: os traços/misses de suporte são divididos — a regra é aprendida na metade e tem
que acertar na outra (aceitação estilo Self-Harness: nada cai no held-in nem no held-out, ao menos um
melhora — metaharness doc §2); (iv) **guard**: a suíte `evals/offline/tasks.json` inteira roda com o
overlay aplicado — zero regressões, `false_success = 0` é constraint, não objetivo.

**Promoção.** `jazz forge --promote` lista cada candidato com evidência (traços de suporte, diff do
asset, resultados do portão) e confirma — o mesmo molde de `imApply` (`improve.go:801-843`). Assets
aprovados vão para `dataDir()/forge/{methods,nlu,scripts}/` (escopo usuário) ou `.jazz/` (escopo
projeto, atrás de `IsTrusted` como hoje). `wins/fails` à la `pcScore` (`plancache.go:405-429`) rebaixa
de volta ao quarentenado `forge/candidates/` — que o runtime **nunca** carrega.

---

## 6. Propostas (ordenadas por impacto esperado)

Todas Go stdlib, medidas no Dell. Métrica = a que move em `jazz eval offline` + métricas de forge
(candidatos → promovidos → rebaixados; misses/semana; cobertura por asset).

### A. Overlays de assets (methods + NLU) — desbloqueia todo o resto
- **Mecanismo:** `mhLibrary` v2: merge `embed → dataDir()/forge/methods → ~/.config/jazz-cli/methods
  → .jazz/methods (confiável)`, name-keyed, mesmo molde de `fpBuild` (`intents.go:263-324`) e
  `scLoad` (`scripts.go:140-143`); `mhMethod` ganha `Src` (builtin|forged|user|project) e
  `Wins/Fails` persistidos; execução com falha de verify rebaixa após N=2 (à la `pcEvictFails`).
  `nuCompile` v2: append de `examples` e merge de `slang/concepts/fillers` de
  `dataDir()/forge/nlu/`; **regras de intents existentes são imutáveis por overlay** — overlay só
  adiciona exemplos e intenções novas (superfície segura).
- **Arquivos:** `methods.go:74-111`, `nlu.go:151-229`; novo `forge_overlay.go` (~200 ln); loaders de
  candidatos separados do runtime.
- **Custo 386:** merge de dezenas de JSONs < 50 ms; RAM ~0.
- **Métrica:** pré-requisito de B–G; `no_llm_rate` sobe quando promovidos entram.
- **Impacto 5 · Esforço M · Risco médio** (asset forjado executando → quarentena + gate + demote).

### B. `forge.go`: o laço resolver→comprimir→reusar (o DreamCoder-shape offline)
- **Mecanismo:** subcomando `jazz forge` orquestrando §5: carrega fontes (reusa `imScan`,
  `imLoadSession`, `pcLoad`), emite `fgTrace`, roda geradores C–F, roda o portão (G), imprime o
  relatório. `--promote` = human gate; `--self` = só corpus do projeto atual (escreve em `.jazz/`).
  Proveniência obrigatória em todo asset: `{forged_by, corpus_hash, sessions[], gate:{held_in,held_out,
  guard}, wins}`.
- **Arquivos:** novo `forge.go` (~500 ln); `improve.go` (registrar subcomando), `obs.go` (evento
  `miss`), `brain.go` (3 pontos de emissão, ~20 ln).
- **Custo 386:** batch; segundos a ~1 min por 10³ sessões (estimativa; IO-bound em JSONL).
- **Métrica:** `no_llm_rate` ↑ a cada release de assets; `sucesso verificado` ↑; misses ↓.
- **Impacto 5 · Esforço L · Risco médio.**

### C. Compressão de subsequências quentes → macro-métodos (Sequitur/Stitch-lite)
- **Mecanismo:** stream de caps por sessão verificada; mineração de subsequências frequentes
  (Sequitur sobre o stream, ou n-gramas com suporte ≥3 e janela fixa — a forma mais simples que
  funciona); para cada subsequência quente: anti-unificar args entre ocorrências → slots; `pre` =
  fatos exigidos pela 1ª cap; `needs` = bins observados; `verify` = o check do último passo ou
  `exit_zero`; `risk` = máximo dos passos; `cmds` só se todos forem read (`mhBadCmd` decide).
  Objetivo MDL-lite: só promove se `ocorrências × (len−1) > custo de descrição` — a lição
  anti-Memorize (§2.1).
- **Arquivos:** novo `forge_macro.go` (~300 ln); depende de A e G.
- **Custo 386:** O(total de passos); < 1 s por corpus típico (estimativa).
- **Métrica:** `comandos/tarefa` ↓, `p50` ↓ (macro pula choose/report intermediários), `no_llm_rate` ↑.
- **Impacto 4 · Esforço M · Risco médio** (macro super-generalizado → MDL + replay no portão).

### D. Snuba/ADIOS-lite: misses → regras de intenção e exemplos
- **Mecanismo:** miss log + prompts não-entendidos das sessões → canonizar (`nuCanonText`) → cluster
  por Jaccard de char-n-gramas (reusa `nuNgrams`) ≥ ~0,5. Por cluster ≥ 3: subsequência compartilhada
  significativa (à la MEX, mas simplificada: maior subseq. comum ponderada por cobertura) → padrão;
  posições variantes do mesmo kind (`@file`, `@host`, número) → slot ou `(a|b)`; tokens que só variam
  por sinonímia do léxico → conceito. Emite regra candidata + os membros como exemplos + os exemplos
  negativos = membros de clusters vizinhos. Portão NLU: casa ≥2/3 do held-out do cluster, não rouba
  nenhum dos 644 exemplos nem os 100 cegos, não derruba o piso (≥95% dataset, ≥70% cego).
- **Arquivos:** novo `forge_intent.go` (~350 ln); `brain.go` (miss log, ~20 ln); `nlu.go` (overlay, A).
- **Custo 386:** clustering O(m²) sobre m ≤ ~10³ misses → ~10⁶ comparações baratas (estimativa, ~1 s).
- **Métrica:** `no_llm_rate` ↑↑ (cobertura cresce nos pedidos reais do dono), `false_success` = 0.
- **Impacto 5 · Esforço M · Risco médio** (cluster espúrio → suporte mínimo + held-out do cluster).

### E. PBE-lite: exemplos do dono → transforms/parsers determinísticos
- **Mecanismo:** DSL fechada ~12 operadores (`re_extract`, `slice`, `replace`, `lower/upper`,
  `basename/dirname/ext`, `join_path`, `trim`, `split`, `nth`) com semântica inversa rasa (à la
  witness functions: output conhecido restringe input). Fontes de exemplos: pares arg→efeito
  observados em comandos repetidos (ex.: mesmo sed/awk manual 3×), hunks de diff idiomáticos,
  extrações de log que o dono fez na mão. Saída: (a) template de args para métodos (o uso mais
  comum), (b) regex candidata para `outparse.go`/script, (c) — só com LLM na forja — corpo python de
  script novo. Ranking por MDL-lite (programa menor primeiro), aprendendo com a lição CAV-15:
  validar no par held-out, não só no exemplo dado.
- **Arquivos:** novo `forge_pbe.go` (~400 ln); `outparse.go` (ponto de injeção de regex candidatas);
  `jazz-scripts/` (novo sidecar C5).
- **Custo 386:** enumeração podada por VSA-lite em DSL de 12 ops → ms por candidato (estimativa).
- **Métrica:** `comandos/tarefa` ↓ (menos tentativa-erro manual), `sucesso verificado` ↑ em tarefas de
  transformação.
- **Impacto 3 · Esforço L · Risco médio-alto** (generalização errada → held-out + promote humano).

### F. LatPlan-lite: `pre`/`needs`/`verify` minerados de transições
- **Mecanismo:** sobre `fgTrace` + eventos: contagem `(fato antes, cap, exit)` → `pre`/`needs`
  candidatos por suporte+confiança (fato presente em ≥90% dos sucessos da cap, ausente nas falhas);
  `(cap → check que passou)` → `verify`; `(rvCat → ação que desfez)` → entrada da tabela de
  recuperação (`recover.go:108-122`). Útil duplamente: gera os `pre` de C1/C4 e auditável contra os
  `pre` escritos à mão (drift da biblioteca).
- **Arquivos:** novo `forge_pre.go` (~200 ln); `recover.go` (sugestões, não autoload).
- **Custo 386:** contadores; desprezível.
- **Métrica:** `false_blocks` ↓ (menos método falhando por pre ausente), `sucesso verificado` ↑.
- **Impacto 3 · Esforço M · Risco médio** (correlação espúria → suporte mínimo + χ² simples).

### G. Portão held-out compartilhado + replay (a máquina de aceitação)
- **Mecanismo:** env `brEnv` sintético no molde de `eoOne` (`evaloffline.go:279-351`): fixture por
  fingerprint do trace (`fpDetect` + layout), `Permit` = política do eval (linha 317-334), `Ask` =
  respostas gravadas do trace. Split determinístico dos suportos (hash par → held-in, ímpar →
  held-out). Regras de aceitação (Self-Harness): ≥2/3 do fix-set vira, nada cai no guard (suíte eo
  inteira com overlay), `false_success` = 0. Auditor de veneno `h2PlanPoison` + novo `h2AssetPoison`
  sobre `doc`/`description`/args.
- **Arquivos:** novo `forge_gate.go` (~250 ln); `evaloffline.go` (env `JAZZ_FORGE_DIR` p/ overlay no
  eval, ~30 ln); `hardening2.go` (~40 ln).
- **Custo 386:** replay = custo do `eoOne` por candidato (~15–400 ms) — lote de 20 candidatos ~
  segundos.
- **Métrica:** é o portão; sem ele nada promove. `false_success` = 0 e guard-sem-regressão são
  constraints.
- **Impacto 4 · Esforço M · Risco baixo.**

### H. `jazz forge report`: cobertura e fila de misses
- **Mecanismo:** histograma de misses clusterizados (dá o próximo alvo), assets por proveniência,
  promovidos/rebaixados por período, Δ de `no_llm_rate` desde o último forge — o painel que fecha o
  loop e evidencia o que falta sintetizar (inclui "misses que pedem LLM/cap nova" — fora do escopo
  automático, viram trabalho humano).
- **Arquivos:** `forge.go` (subcomando report, ~150 ln).
- **Custo 386:** desprezível.
- **Métrica:** qualidade da decisão de roadmap ↑ (o que falta vira número, não impressão).
- **Impacto 2 · Esforço S · Risco nenhum.**

**Ordem sugerida:** A → B+G (esqueleto que já promove à mão o que existe) → D (misses são o ganho
maior: o dono sente a cobertura crescer) → C → F → E → H.

---

## 7. Não adotar (e por quê)

- **Wake-sleep completo com recognition neural e dreaming.** O DreamCoder precisa de 20–100 CPUs × ~1
  dia por domínio e de inferência/treino de NN — sem stdlib Go/386 e sem orçamento no Dell. Nosso
  "recognition" já existe e é determinístico: NB (`nlu.go:1329-1482`), nameindex e BM25. Ficamos com
  a forma do loop, não com a rede.
- **e-graphs / saturação por igualdade / babble.** Depende de teoria equacional escrita à mão por
  domínio e de infraestrutura (egg/Rust) sem porte para Go puro 386. Nosso corpus são sequências
  planas de chamadas — anti-unificação sintática basta.
- **DSPy teleprompters/otimizadores.** Otimizam chamadas LM; o nosso runtime não tem chamada LM para
  otimizar. Ficam a forma "traço verificado → demo/asset" e "seleciona no dev set" (já em §5).
- **Modelo generativo do Snorkel (estimar acurácia de LF sem verdade).** Onde há checker
  (`brVerify`/`eoOne`) a verdade existe — estimar seria pior. Onde não há, abstém-se (Chow,
  math-offline-brain §P4). Fica a forma Snuba: regra cobre só onde acerta.
- **LatPlan neural (SAE/AMA com VAE).** Sem NN no Dell. A versão contadora (F) captura a ideia que
  presta: o modelo de ação se aprende de transições tipadas.
- **Indução de gramática geral (BMM/PCFG, ADIOS completo).** Corpus de dezenas–centenas de frases e
  custo de falso positivo alto (regra errada captura pedido) → só a versão restrita D, com portão
  duro (≥95% dataset, ≥70% cego, sem roubar exemplos).
- **SyGuS de propósito geral / síntese de código Go.** Enumeração aberta não cabe e a semântica não
  verifica offline; PBE só na DSL fechada (E) e métodos só sobre caps registradas — a superfície
  executável continua fechada.
- **Promoção Memorize (guardar traço cru).** Ablação do DreamCoder: soluções inteiras na biblioteca
  não generalizam. `pcPlan` já cobre reuso de prosa para o modelo; o forge só promove o comprimido.
- **LLM inventando intents/métodos sem suporte de corpus.** Toda candidata precisa de traços/misses
  de suporte — a prosa do modelo não é evidência (mesma regra do `acTurnDone`: só entra o que
  verificou). LLM na forja = AutoDoc e corpo de script, nunca autoridade de existência.
- **AutoDoc como requisito.** `doc`/`description` com template determinístico é obrigatório; a versão
  LLM é enfeite opcional. Nome ruim não impede promoção; docstring alucinada impede.

---

## 8. Fontes consolidadas

Síntese/library learning: DreamCoder arXiv:2006.08381 (+ RSTA 381:20220050, 2023) · EC² NeurIPS 2018
(Ellis et al., 7816-7826) · Stitch arXiv:2211.16605 (POPL 2023, DOI 10.1145/3571234) · babble
arXiv:2212.04596 (POPL 2023, DOI 10.1145/3571207) · LILO arXiv:2310.19791 (ICLR 2024) · anti-unificação
Plotkin/Reynolds, *Machine Intelligence* 5, 1970. Compilação LM: DSPy arXiv:2310.03714. Supervisão
fraca: Snorkel arXiv:1711.10160 (PVLDB 11(3), 2017) · Snuba Varma & Ré PVLDB 12(3):223-236, 2019
(DOI 10.14778/3291264.3291268). PBE: FlashFill Gulwani POPL 2011 (DOI 10.1145/1926385.1926423) ·
ranking Singh & Gulwani CAV 2015 · FlashMeta/PROSE Polozov & Gulwani OOPSLA 2015. Modelo de ação:
LatPlan arXiv:1705.00154 (AAAI 2018). Indução de gramática: ADIOS PNAS 102(33) 2005 (DOI
10.1073/pnas.0409746102) · Sequitur JAIR 7:67-82, 1997 · Stolcke & Omohundro ICGI 1994 (LNAI 862).
Correção do dono: MemPrompt arXiv:2201.06009 (EMNLP 2022). Docs irmãos citados:
offline-on-device-intelligence.md (P6 macro-ops/HTN-MAKER/CBR; proposta A `jazz forge`) ·
metaharness-self-improvement.md (portão Self-Harness, DGM) · tocomas-plan-reuse.md (APC/AgentReuse) ·
math-offline-brain.md (Chow/risco-cobertura) · offline-brain-research.md (HTN/BT) ·
harness-first-offline.md (tese).
