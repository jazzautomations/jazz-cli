# Indução de skill/método e raciocínio baseado em casos sem LLM em runtime

**Escopo.** Como transformar resoluções bem-sucedidas (traços de capacidades) em **métodos HTN lineares**, **casos** (`cases.jsonl`) e **macro-operadores** que o jazz reusa offline, sem LLM no caminho crítico. Aterrissagem: `methods.go`, `caps.go`, `recover.go`, `plancache.go`, `improve.go`.

**Regra de ouro (repetida porque ela decide tudo abaixo).** *Se o LLM volta ao caminho crítico em runtime, é dependência, não asset.* Inteligência cara fica em **tempo de compilação** (asset-forge / Method Forge, máquina dev ou Fable); em runtime só entra o que é estático, verificado e barato: JSON, JSONL, índice por palavra-chave.

**Baseline medido no d610** (`results/dell-0.9-progress.md`): suite offline 39/39, `false_success` 0, 34/39 sem modelo, p50 ≈ 14–19 ms, p95 ≈ 362–387 ms, RSS ≤ 59 MB. Nenhum número abaixo é claim novo de medição: o que é **paper** está citado; o que é **proposta** está marcado como alvo.

---

## 1. O problema, em uma frase

O jazz já *executa* métodos lineares verificados (`methods.go`) e já *guarda* planos verificados (`plancache.go`), mas **não sabe induzir novos métodos a partir das próprias vitórias**: `docs/design/offline-brain.md:79-85` especifica candidatos em `~/.local/share/jazz-cli/methods/candidates/` + promoção por `jazz improve --promote`, e **nada disso existe no código** (busca confirma: não há `learn.go` nem pipeline de promoção). Este documento é a pesquisa para fechar esse gap.

Três tradições de literatura cobrem o problema:

1. **Simbólica clássica (1983–2019)** — HTN-MAKER, CaMeL, CHEF, CBR 4-R, PRIAR, Macro-FF, EBL/chunking, PbD, FAMA/LOCM/XLearn. Escrita para planejadores determinísticos; nenhuma depende de LLM. É a espinha dorsal do Method Forge.
2. **Bibliotecas de skills na era-LLM (2022–2026)** — Voyager, LATM, CREATOR, TroVE, ASI, SkillWeaver, AWM, Synapse, ExpeL, Reflexion, AutoManual, A-MEM, AgentReuse, APC. Quase toda a *indução* usa LLM — mas isso é compatível com a regra de ouro: **indução em compile-time**. O que não pode vazar para runtime é recuperação por embedding, adaptação por modelo e auto-verificação neural.
3. **Destilação/rotulagem fraca (2017–2024)** — DSPy, Snorkel, STaR, analogical prompting. Ensinam como o produto de um professor caro vira artefato barato e como agregar sinais fracos de verificação em rótulos de confiança.

---

## 2. Resumos técnicos

### 2.1 Linhagem clássica: aprender métodos e casos sem LLM

**CHEF — Hammond, *Case-Based Planning: Viewing Planning as a Memory Task*, Academic Press, 1989 (sem arXiv).**
- *Método:* memória de episódios indexada por **meta** e por **tipo de falha**. Novo objetivo → recupera o plano mais parecido → adapta por substituição → *simula* a execução → quando a simulação falha, um **critic** indexado pela falha propõe o reparo; o reparo é guardado junto ao plano.
- *Número-chave:* sistema demonstrado em receitas (domínio de culinária); a contribuição é arquitetural, não benchmark.
- *Lição que importa:* **o índice primário não é similaridade de texto — é a falha**. Casos valem mais quando rotulados por *como* quebraram e *como* foram consertados. No jazz isso já tem nome: `rvClassify` (`recover.go:53`) produz exatamente esse rótulo (`name_not_found`, `test_failed`, `permission_denied`, …, `recover.go:18-32`).
- *Limitação:* adaptação e simulação eram manuais/específicas do domínio; sem verificação objetiva externa.

**Ciclo 4-R — Aamodt & Plaza, *Case-Based Reasoning: Foundational Issues, Methodological Variations, and System Approaches*, AI Communications 7(1), 1994.**
- *Método:* **Retrieve → Reuse → Revise → Retain**. A lição durável é separar *retain* de *reuse*: o caso só entra na base depois de revisado — e revisão é onde mora a verificação.
- *Mapeamento:* Retrieve = `pcMatch` (`plancache.go:352`); Reuse = injeção/execução de método; Revise = `brVerify` (`methods.go:1680`) + screen estático proposto; Retain = `pcStore` (`plancache.go:433`) + promoção gated.
- *Limitação:* o ciclo não diz como revisionar automaticamente — o jazz responde com verificação determinística, não com LLM.

**Analogia derivacional — Carbonell, *Learning by Analogy*, Machine Learning, 1983; e PRIAR — Kambhampati & Hendler, *A Validation-Structure-Based Theory of Plan Modification and Reuse*, AIJ 55(2-3), 1992.**
- *Método:* guardar a **derivação** (sequência de decisões + justificativas causais), não só o plano final; reutilizar re-ligando links causais e *refazendo* os que não valem no novo estado inicial.
- *Lição que importa:* um traço do jazz já é uma derivação: sequência de `mhStep{Cap,In,Out}` com `Out` consumido por passos posteriores. Reusar caso = revalidar esses links contra o contexto atual — exatamente o que o screen estático da §6 faz com `Pre`/`Effects`.
- *Limitação:* exige modelo causal completo; no jazz o modelo é parcial (campos `Pre`/`Effects` por capability, `caps.go:67-80`), o que restringe a checagem — e está correto que seja assim.

**Nebel & Koehler, *Plan Reuse versus Plan Generation: A Theoretical and Empirical Analysis*, AIJ 76(1-2):427-454, 1995.**
- *Resultado central (o antídoto ao hype):* reuso de plano **pode ser tão difícil quanto planejar do zero** no pior caso; o reuso só compensa quando a biblioteca é restrita ao domínio e a recuperação é conservadora.
- *Consequência para o jazz:* é a justificativa formal para (a) métodos **lineares** (sem backtracking de planejador), (b) retrieval com filtro duro por `intent`/classe/assinatura de repo, (c) `Pre`/`Effects` como **screen** — nunca como busca.

**Explicação-based learning e o "utility problem" — Minton, *Selectively Generalizing Plans for Problem-Solving* (MACROLET), IJCAI 1985; Minton, *Quantitative Results Concerning the Utility of Explanation-Based Learning*, AAAI 1988; Minton et al., *Explanation-Based Learning: An Alternative View*, Machine Learning 4(2), 1989.**
- *Método:* generalizar um traço explicando por que cada passo foi necessário (regressão do objetivo pelos efeitos); a regra aprendida vira controle de busca.
- *Número/lição-chave (Minton 1988):* **o utility problem** — regras aprendidas demais *degradam* o sistema (match caro + regras inúteis). Filtro obrigatório por utilidade medida.
- *Mapeamento:* é o argumento canônico para `pcMaxPlans = 100`, `pcMaxInject = 3` (`plancache.go:43-45`), para o trim λ ∝ log n do TroVE, e para o nosso portão de suporte/consistência em macros (§5.3, P5).

**Chunking — Laird, Rosenbloom & Newell, *Chunking in Soar*, Machine Learning 1(1), 1986.**
- *Método:* toda vez que o resolvedor cai num *impasse* e o resolve num subgoal, compila o caminho da solução numa nova produção — skill = conhecimento compilado de um impasse resolvido.
- *Lição que importa:* **induzir exatamente onde houve dificuldade**, não onde foi fácil. Para o jazz: candidatos a método valem mais se vierem de tarefas que passaram por `recover.go` e terminaram verificadas — a recuperação é o impasse; a resolução verificada é o chunk.
- *Limitação:* EBL/chunking assume modelo correto; com modelo parcial, o chunk pode não transferir — daí o held-out gate.

**Macro-operadores — Botea, Enzenberger, Müller & Schaeffer, *Macro-FF: Improving AI Planning with Automatically Learned Macro-Operators*, JAIR 24:581-621, 2005 (arXiv:1109.2154, depósito 2011).**
- *Método em 4 passos:* (1) análise estrutural do domínio (componentes no grafo de causalidade); (2) geração de macros; (3) **filtragem e ranqueamento**; (4) uso nas buscas futuras. Macros geradas a partir de soluções anteriores no mesmo domínio.
- *Número-chave:* "grande redução do esforço de busca" nos domínios de IPC onde estrutura é inferível — e, honestamente, **pouco ou nenhum ganho onde não é**: macro só compensa quando a estrutura se repete.
- *Lição que importa:* a etapa 3 (filtro/rank) é o produto; gerar macro é trivial, filtrar é o trabalho. Versão jazz: suporte ≥ N casos, consistência de outcome ≥ θ, comprimento 2..6, top-k pequeno (P5).
- *Limitação:* otimiza busca de planejador; nosso executor é linear — o ganho é *granularidade de promoção* (uma subsequência frequentemente verificada vira asset próprio), não profundidade de busca.

**Mineração de sequências — Agrawal & Srikant, *Mining Sequential Patterns*, ICDE 1995 (GSP).**
- *Método:* Apriori sobre sequências: candidatos de tamanho k a partir de frequentes de tamanho k−1, poda por suporte mínimo.
- *Uso proposto:* exatamente este algoritmo sobre a sequência canônica de `Cap` por caso (§5.3). Custo em Go: trivial para centenas de casos — O(Σ|traço| × candidatos), microssegundos no d610.

**XLearn — Reddy & Tadepalli, *Learning Goal-Decomposition Rules Using Exercises*, ICML 1997.**
- *Método:* aprende regras de decomposição de meta via EBL sobre *exercícios* resolvidos; a regra diz quando um subgoal alimenta outro.
- *Lição que importa:* decomposição aprendida = pares (subgoal, ordem) com precondições observadas — mesmo formato de um `mhMethod` linear com `Pre`. Reforça que linearidade + precondição observada é uma forma induzível clássica.

**CaMeL / CaMeL++ — Ilghami, Nau, Muñoz-Avila & Aha, *CaMeL: Learning Method Preconditions for HTN Planning*, AIPS 2002; Ilghami et al., *Learning Approximate Preconditions for Methods in Hierarchical Plans*, ICML 2005.**
- *Método:* dados métodos sem `Pre`, aprende as precondições por **version spaces** (candidate elimination) sobre traços positivos/negativos: conjunto mais geral e mais específico consistentes com os exemplos.
- *Número-chave (ICML-05):* CaMeL **converge primeiro nos métodos mais frequentemente necessários** — i.e., aprendizado incremental paga dividendos antes da convergência total; e CaMeL++ troca 100% de precisão por convergência rápida (precondição aproximada) quando samples são poucos.
- *Lição que importa:* (a) `Pre` de método candidato não precisa nascer perfeito — nasce **conservador** (interseção dos fatos observados nos casos bem-sucedidos) e afrouxa com dados; (b) priorizar promoção pela frequência de necessidade, não pela ordem de chegada.
- *Limitação:* version spaces exigem traços etiquetados positivo/negativo e linguagem de hipóteses fechada — no jazz os rótulos vêm de `brVerify`/`rvClassify` e a linguagem é `mhCond{Fact,Op,Val}` (`methods.go:42-46`), fechada por construção.

**HTN-MAKER — Hogg, Muñoz-Avila & Kuter, *HTN-MAKER: Learning HTNs with Minimal Additional Knowledge Engineering Required*, AAAI 2008; versão de journal: Hogg, Muñoz-Avila & Kuter, *Learning Hierarchical Task Models from Input Traces*, Computational Intelligence 32(1):111-146, 2016.**
- *Método (o algoritmo que importa, ICAPS-07 workshop + AAAI-08):* percorre o plano **para frente**, gerando o estado após cada operador; quando o estado corrente inclui **todos os efeitos de uma tarefa anotada**, aprende um método para ela. Como subtarefas entram operadores ou métodos já aprendidos **cujos efeitos fornecem um efeito da tarefa ou uma precondição de subtarefa posterior**; operadores irrelevantes são pulados. `Pre` do método = união das precondições das subtarefas não satisfeitas pelo estado inicial. Métodos curtos são aprendidos antes e servem de subtarefa dos longos. Sound e complete sob as premissas; converge em domínios clássicos.
- *Lição que importa:* é o **esqueleto do Method Forge** — com duas simplificações enormes: (a) nossos "operadores" são capabilities com `Effects` declarados (`caps.go:67-80`), então "estado inclui efeito" vira lookup de string, não unificação STRIPS; (b) só precisamos de profundidade 1 (método linear sobre capacidades), não hierarquia recursiva — embora nada impeça que um método promovido seja chamável como macro por outro.
- *Limitação:* precisa de tarefas anotadas (quais efeitos a tarefa quer). No jazz, a anotação é o `intent`+`Verify` da execução: o `mhCheck` que fechou a tarefa define o "efeito-objetivo". E precisa de modelo razoável — nosso `Effects`/`Pre` é parcial por desenho, então o screen da §6 é conservador.

**Modelos de ação a partir de traços — LOCM (Cresswell, McCluskey & West, *Acquiring Planning Domain Models Using LOCM*, Knowledge Engineering Review 28(2), 2013) e FAMA (Aineto, Jiménez & Onaindia, *Learning Action Models with Minimal Observability*, AIJ 272, 2019; versão preliminar arXiv:1903.01153).**
- *LOCM:* induz esquemas de ação a partir das **transições de estado dos objetos** entre ações consecutivas — sem nomes de ação, só padrões de transição. Ideia transferível: quando um `Cap` do jazz não declara `Effects`, eles podem ser *inferidos* por diferença de contexto observado entre antes/depois do passo — mas só como anotação candidata, nunca como verdade.
- *FAMA:* compila o aprendizado de modelos STRIPS *com observabilidade parcial* num problema de planejamento clássico; avalia modelos sintática e semanticamente. Transferível só conceitualmente: as execuções do jazz são **totalmente observadas** pelo harness (cada `Cap` e seu `Out` ficam no evento da sessão), então precisamos de algo mais fraco que FAMA — reconciliação, não indução de modelo.

**Programming by Demonstration — Cypher (ed.), *Watch What I Do*, MIT Press, 1993; Allen et al., *PLOW: A Collaborative Task Learning Agent*, AAAI 2007; Tecuci, *Building Intelligent Agents*, Academic Press, 1998.**
- *Método:* o usuário executa; o sistema grava o traço, generaliza constantes→variáveis, e replay parametrizado. PLOW adiciona diálogo misto-iniciativa e aprende o procedimento inteiro de UMA demonstração.
- *Lição que importa:* (a) **uma demonstração basta para candidatar** — a generalização de slots vem dos spans de entidade que `nlu.go` já extrai (entidades vêem `In`/`Out` → viram variáveis do método); (b) o professor (usuário ou LLM em compile-time) demonstra, o runtime replaya. É exatamente a filosofia do forge.
- *Limitação:* PbD clássico não verifica; replay cego falha silenciosamente — o jazz fecha isso com `brVerify` obrigatório e `verify_unavailable` honesto.

**Surveys:** Spalzzi, *A Survey on Case-Based Planning*, AI Communications 14(1), 2001 — taxonomia (case-based vs transformational vs derivational analogy); útil como mapa, não como método.

### 2.2 Era-LLM: indução por modelo em compile-time, reuse como artefato

**Voyager — Wang et al., *Voyager: An Open-Ended Embodied Agent with Large Language Models*, arXiv:2305.16291, 2023 (TMLR 2024).**
- *Método:* 3 componentes — currículo automático (GPT-4 propõe tarefas progressivas), **skill library** (programas JS verificados por auto-verificação, indexados por embedding da descrição), prompting iterativo com feedback de execução.
- *Número-chave:* 3,3× mais itens únicos, 2,3× distância, milestones até 15,3× mais rápidos vs SOTA anterior em Minecraft; biblioteca transfere zero-shot para mundo novo.
- *Ablação que importa:* sem skill library o desempenho **estagna no estágio final** da exploração; e skills aprendidas *aceleram o AutoGPT* quando injetadas nele — skill como artefato transferível entre agentes.
- *Limitação:* tudo é GPT-4 — escrita, verificação, índice (embedding). Em runtime no d610: inadotável como está; como padrão de forge (verificar→guardar→compor), é a referência.

**LATM — Cai et al., *Large Language Models as Tool Makers*, arXiv:2305.17126, 2023.**
- *Método:* modelo forte faz *tool making* (escreve função reutilizável + dispatcher que decide quando usar); modelo leve faz *tool using*. Cache funcional: guarda a *capacidade* para uma classe de requests, não a resposta.
- *Número-chave:* GPT-3.5-usuário + GPT-4-maker ≈ desempenho de GPT-4 integral, com custo de inferência drasticamente menor (o custo do maker amortiza sobre muitos usos).
- *Lição que importa:* **divisão maker/user é a arquitetura do jazz**: Fable/forge = maker; brain offline = user com zero parâmetros. O "dispatcher" deles vira nosso `pcMatch`/`lrPick` determinístico.
- *Limitação:* o user ainda é LLM; no jazz o user é código Go — mais radical e ainda mais barato.

**CREATOR — Qian et al., *CREATOR: Tool Creation for Disentangling Abstract and Concrete Reasoning of LLMs*, arXiv:2305.14318, 2023 (EMNLP Findings).**
- *Método:* separa criação de ferramenta (raciocínio abstrato) de uso (execução concreta); cada problema gera ferramenta própria com documentação.
- *Número-chave:* supera CoT/PoT/tool-use em MATH e TabMWP.
- *Limitação (relatada pelos próprios sucessores):* gera **centenas** de ferramentas — difícil de reusar e verificar (TroVE §1 cita isso explicitamente). Para o jazz: prova de que induzir sem filtro de reuso vira lixo, não biblioteca.

**TroVE — Wang, Fried & Neubig, *TroVE: Inducing Verifiable and Efficient Toolboxes for Solving Programmatic Tasks*, arXiv:2401.12869, 2024 (ICML).**
- *Método:* toolbox compartilhada cresce **em streaming**: por exemplo do dataset, o LM amostra K respostas em 3 modos (IMPORT existente / CREATE novo / SKIP primitivo); seleção por **concordância de execução** (resposta mais frequente entre os que executam) desempatada por **menor número de operações**; **trim periódico** remove funções usadas < λ vezes, λ = ½·log₁₀(n).
- *Número-chave:* 11 datasets; toolbox 79–98% menor que métodos anteriores com acurácia igual ou melhor que CodeLLaMa e pipelines GPT; verificação humana 31% mais rápida e 13% mais correta (soluções com funções compostas são mais curtas).
- *Ablação que importa:* sem trimming a toolbox cresce e a acurácia cai — utility problem moderno, confirma Minton 1988; robusto à ordem dos exemplos (streaming online é viável, não só batch).
- *Limitação:* cada função é gerada por LM — no jazz a "função" é um `mhMethod` JSON e a seleção é por wins/fails objetivos, não por concordância de amostras; mesmo assim, **a fórmula do trim e a ordem grow→select→trim transferem 1:1**.

**ASI — Wang, Gandhi, Neubig & Fried, *Inducing Programmatic Skills for Agentic Tasks*, arXiv:2504.06821, 2025 (COLM).** — o paper mais próximo do nosso problema.
- *Método:* após resolver uma tarefa, limpa o episódio (remove passos com erro de execução, comprime pensamentos), induz **programas-skill** que embrulham subsequências do traço, e os verifica com **test trajectory**: reescreve o traço substituindo subsequências pelas chamadas de skill, **trunca o sufixo após a última chamada de skill** (anti-sucesso-espúrio — um `send_msg` final responderia certo mesmo com skill quebrada), executa o prefixo e deixa o agente terminar. Só promove se 3 checks passarem: (1) corretude da tarefa, (2) pelo menos uma chamada de skill usada, (3) toda chamada causou mudança de ambiente.
- *Número-chave:* +23,5 pts de success rate vs baseline estático e +11,3 vs AWM (skill textual) no WebArena; −10,7–15,3% de passos; em tarefas longas −9,5/−5,6 passos e +20,7–38,9% de acerto.
- *Ablação que importa (Tabela 3):* (unverified, text) 32,6 → (verified, program) em memória 36,4 → (verified, text) como ação 39,0 → (verified, program) como ação 40,1. **Verificação vale ~4–7 pontos; formato programa como ação soma mais ~1–4.** Programa verificável > texto confiável.
- *Limitação:* indução, rewriting e avaliador são LM; a verificação usa *re-execução no ambiente*, que no jazz custa uma rodada de dry-run — viável em forge-time, caro demais para cada promoção (daí held-out em fixtures).

**SkillWeaver — Zheng et al., *SkillWeaver: Web Agents Can Self-Improve by Discovering and Honing Skills*, arXiv:2504.07079, 2025.**
- *Método:* explora o ambiente, pratica skills descobertas, e destila a prática em **APIs** robustas (programa + docstring); currículo de auto-propostas.
- *Número-chave:* +31,8% relativo em WebArena, +39,8% em sites reais; skills de agente forte transferem para fraco com até +54,3%.
- *Lição que importa:* **skill honing** — a skill candidata é exercitada antes de virar API. No jazz isso é o held-out replay em contextos distintos (P6).
- *Limitação:* descoberta via LLM e "self-proposal" — forge-time only.

**AWM — Wang, Mao, Fried & Neubig, *Agent Workflow Memory*, arXiv:2409.07429, 2024.**
- *Método:* induz **workflows** (rotinas textuais) de exemplos de treino (offline AWM) ou de queries de teste (online AWM) e injeta os relevantes no contexto do agente.
- *Número-chave:* +24,6% relativo Mind2Web, +51,1% WebArena; online generaliza cross-task/site/domínio com +8,9–14,0 pts conforme o gap cresce.
- *Ablação/lição que importa:* workflows **textuais** ajudam mesmo sem executabilidade — mas o ASI mostra que programa verificado supera texto (+11,3 pts). Para o jazz: `pcPlan` injetado = workflow textual; `mhMethod` induzido = workflow programático. Os dois níveis coexistem, com o programático acima na hierarquia de confiança.
- *Limitação:* indução e seleção por LM; texto sem verificação pode ensinar errado.

**Synapse — Zheng et al., *Synapse: Trajectory-as-Exemplar Prompting with Memory for Computer Control*, arXiv:2306.07863, 2023 (ICLR 2024).**
- *Método:* abstração de estado + **traço completo como exemplar** + memória de exemplares com retrieval por embedding.
- *Número-chave:* 99,2% em MiniWoB++ (relativo +10%) treinando com demos de só 48/64 tarefas — **generaliza para tarefas nunca demonstradas**; +56% relativo em step-success em Mind2Web.
- *Lição que importa:* o traço inteiro (não o resumo) é o que transfere — o nosso `cases.jsonl` guarda o traço completo, e o hint injetado pode referenciar o caso; retrieval por embedding trocamos por BM25 puro-Go + filtro duro (§5.1).
- *Limitação:* embeddings e exemplar-prompting são runtime-LLM — forma não transfere, conteúdo sim.

**ExpeL — Zhao et al., *ExpeL: LLM Agents Are Experiential Learners*, arXiv:2308.10144, 2023 (AAAI-24).**
- *Método:* coleta experiências (sucessos e falhas) em tarefas de treino; extrai **insights** textuais comparando pares sucesso/falha; em inferência, recupera experiências similares + insights.
- *Número-chave:* melhora consistente com acúmulo de experiência em HotpotQA/ALFWorld/WebShop; transfere entre tarefas.
- *Lição que importa:* o par (falha, sucesso) na mesma classe de tarefa é a unidade didática — no jazz isso já existe como traços `improve_load.go` + `pcPlan.fails`/`wins`; o "insight" textual vira `Hint` do `pcPlan` (`plancache.go:65`) — hoje gerado por hindsight LLM, substituível por template determinístico (P8).
- *Limitação:* extração de insights é LLM.

**Reflexion — Shinn et al., *Reflexion: Language Agents with Verbal Reinforcement Learning*, arXiv:2303.11366, 2023 (NeurIPS).**
- *Método:* falha → reflexão textual guardada em memória episódica → condiciona a próxima tentativa.
- *Número-chave:* HumanEval pass@1 ~91% com GPT-4 (+~11 pts vs baseline); ganhos em ALFWorld.
- *Lição que importa:* é o ancestral de `lessons.md`: nota curta de falha reutilizável. A diferença de design do jazz é melhor: lessons são **human-readable, capped, auditadas** (`lessons.go:40-53,241-329`) e escritas só via `jazz improve` — versão offline honesta do reflexion.
- *Limitação:* a reflexão é LM-gerada e episódica (sem persistência estruturada); no jazz o papel equivalente é case+lesson com provenance.

**AutoManual — Chen et al., *AutoManual: Constructing Instruction Manuals by LLM Agents via Interactive Environmental Learning*, arXiv:2405.16247, 2024 (NeurIPS).**
- *Método:* Planner interage; Builder mantém **regras tipadas** com 4 atributos (tipo, conteúdo, exemplo, **validation logs**); Formulator compila o manual. Anti-alucinação: *case-conditioned prompting* — o Builder só escreve regra ancorada em caso concreto.
- *Número-chave:* 97,4% GPT-4 / **86,2% GPT-3.5** em ALFWorld com uma só demonstração — o manual escrito pelo forte guia o fraco. Teacher→student operacional.
- *Lição que importa:* regra sem *validation log* não entra no manual — no jazz: método/caso sem `wins` verificados não promove. E o caso-condicionado vira: toda lesson/método citando `case_id` de provenance.
- *Limitação:* três agentes LM — forge only.

**A-MEM — Xu et al., *A-MEM: Agentic Memory for LLM Agents*, arXiv:2502.12110, 2025 (NeurIPS).**
- *Método:* memória estilo Zettelkasten: notas com atributos estruturados, links por similaridade, e **evolução** — novas notas reescrevem contexto/tags de antigas.
- *Lição que importa:* a única peça transferível é "a memória reescreve a si mesma" — no jazz: um caso novo pode **rebaixar** wins de um método antigo (demote), e `pcScore` já implementa wins/fails (`plancache.go:405-418`).
- *Limitação:* indexação por embedding + linkagem por LLM — não transfere.

**AgentReuse — Li, Wu & Tan, *A Plan Reuse Mechanism for LLM-Driven Agent*, arXiv:2512.21309, 2025 (versão inglesa de artigo J. Comput. Res. Dev. 2024).**
- *Método:* ~30% dos requests reais são idênticos ou similares; classificação de intenção decide similaridade e reusa plano.
- *Número-chave:* 93% de reuso efetivo, F1 0,9718, −93,12% de latência vs sem reuso.
- *Lição que importa:* valida a premissa inteira do documento — **classificador de intent + reuso = a maior fatia dos ganhos**. O jazz tem 50 intents e `nuMinConf=0.6` (`nlu.go:45`): o gargalo não é reconhecer, é ter o que reusar.
- *Limitação:* o classificador deles é treinado; o nosso é `nlu.go` — já offline.

**APC — Zhang et al., *Agentic Plan Caching: Test-Time Memory for Fast and Cost-Efficient LLM Agents*, arXiv:2506.14852, 2025 (NeurIPS).**
- *Método:* extrai **templates de plano** de execuções completas; casa requests novos por **extração de palavras-chave**; um modelo leve adapta o template ao contexto.
- *Número-chave:* −50,31% custo, −27,28% latência mantendo desempenho.
- *Lição que importa:* keyword matching basta para o *match* — valida `pcMatch`; a *adaptação* deles é LM — no jazz a adaptação é binding de slots (`mhStep.In`) + screen de precondições, não reescrita.
- *Limitação:* adaptação por modelo no caminho do request — dependência, não asset.

**TOCOMAS / CoMemBench / AgentReuse no contexto:** já cobertos em `docs/research/tocomas-plan-reuse.md` — este doc não repete; mantém só o que muda o desenho (k=3; provenance; structural screen; benchmark do mesmo autor).

### 2.3 Destilação professor→aluno e rotulagem programática

**DSPy — Khattab et al., *DSPy: Compiling Declarative Language Model Calls into Self-Improving Pipelines*, arXiv:2310.03714, 2023.**
- *Método:* pipelines LM como módulos parametrizados; **compilador** faz bootstrap de demonstrações few-shot (gera traços com o próprio pipeline, **filtra pelos que passam na métrica**, usa-os como demonstrações otimizadas) e busca instruções — tudo em compile-time.
- *Número-chave:* em minutos de compilação, GPT-3.5/llama2-13b superam few-shot padrão em >25%/65% relativo e pipelines com demos de especialista em até 5–46%/16–40%; compilado para **T5-770M** compete com chains GPT-3.5 escritas à mão.
- *Lição que importa:* **é a tese do documento**: "compilar" = gerar traços com o modelo forte, manter só os que passam na métrica (o nosso `brVerify`), congelar como demonstração (o nosso `mhMethod`/`pcPlan`). BootstrapFewShot do DSPy ≡ extrator de casos do forge com gate de verificação.
- *Limitação:* a métrica e o gerador são LM-runtime durante compile — perfeito para o forge, proibido em runtime.

**Snorkel — Ratner et al., *Snorkel: Rapid Training Data Creation with Weak Supervision*, arXiv:1711.10160, 2017 (VLDB).**
- *Método:* usuários escrevem **labeling functions** (heurísticas programáticas de acurácia/correlação desconhecidas); um modelo generativo denoisa os votos e produz rótulos probabilísticos para treinar o aluno — sem ground truth manual.
- *Número-chave:* modelos 2,8× mais rápidos e +45,5% de desempenho vs 7h de rotulagem manual; dentro de ~3,6% de conjuntos curados à mão.
- *Lição que importa:* o jazz já tem labeling functions **perfeitas** — `brVerify` (`methods.go:1680`) e `rvClassify` (`recover.go:53`) são heurísticas determinísticas com ground truth de fato (o processo rodou ou não). O que Snorkel acrescenta: quando um check não existe, múltiplos sinais fracos (`cmd_ran` + `file_exists` + `exit_zero`) podem ser *agregados* com confiança declarada — em vez de fingir verificação que não há. E o princípio "programmatic labels > hand labels" legitima rotular casos por código.
- *Limitação:* o denoising probabilístico não é necessário onde há oráculo — usar só onde `verifier_unavailable` e mesmo assim marcar como *não-verificado*, nunca como sucesso.

**STaR — Zelikman et al., *STaR: Bootstrapping Reasoning with Reasoning*, arXiv:2203.14465, 2022 (NeurIPS).**
- *Método:* gera racionais para muitas questões; se a resposta está errada, tenta de novo com a resposta correta dada (rationalization); **fine-tuna só nos racionais que chegaram à resposta correta**; repete. Bootstrap com verificação de resultado como filtro.
- *Número-chave:* com ~10 exemplos iniciais, iguala fine-tuning de modelo 30× maior em CommonsenseQA.
- *Lição que importa:* **o filtro "guardar só traço com resultado verificado" é o motor** — o jazz já o tem (`pcStore` só roda com `verified`, `plancache.go:389`). A variante "rationalization" (dada a resposta, reconstruir o caminho) sugere: casos que falharam podem gerar caso-negativo rotulado por `rvClassify` — a falha também é dado (CHEF).
- *Limitação:* iteração com gradiente; para nós a iteração é forge→eval→promote.

**Analogical prompting — Yasunaga et al., *Large Language Models as Analogical Reasoners*, arXiv:2310.01714, 2023 (ICLR 2024).**
- *Método:* o modelo **gera seus próprios exemplares** relevantes antes de resolver (auto-exemplares) — CBR dentro do prompt.
- *Número-chave:* ganhos consistentes sobre 0-shot CoT em GSM8K e benchmarks de código.
- *Lição que importa:* "retrieve-then-solve" de CBR também funciona quando o exemplar é sintético — para o jazz significa que casos gerados sinteticamente em forge-time (fixtures do `evaloffline.go:410-467` executados) são material legítimo de indução, não só sessões reais.
- *Limitação:* tudo LM; é confirmação conceitual, não mecanismo.

### 2.4 Contraste: representações latentes (o que fica de fora por construção)

**LatPlan — Asai & Fukunaga, *Classical Planning in Deep Latent Space*, arXiv:1705.00154, 2017 (AAAI-18).**
- *Método:* VAE aprende representação proposicional de imagens (State Autoencoder) + Action Autoencoder/Discriminator que induz símbolos e modelos de ação → gera PDDL → planejador clássico resolve.
- *Por que citar:* é a *forma arquitetural* correta — subsimbólico → simbólico → planejador — mas o substrato errado: VAE treinado + planejador PDDL não roda no d610 e não temos o problema deles (nossas ações já são simbólicas: `Cap` tipadas).
- *Transferível:* só a moral — **compile o conhecimento difuso em estrutura simbólica verificável e planeje no nível simbólico**. É o que o forge faz com traços.

**TACO-RL — Rosete-Beas et al., *Latent Plans for Task-Agnostic Offline Reinforcement Learning*, arXiv:2209.08959, 2022 (CoRL).**
- *Método:* política de baixo nível aprende skills latentes por imitação de dados task-agnostic; política de alto nível (offline RL, hindsight relabeling) **emenda** ("stitches") skills latentes em sequências longas.
- *Número-chave:* ordem de magnitude sobre baselines em metas temporais longas; uma política para 25 tarefas de manipulação real.
- *Lição que importa:* composição linear de primitivas aprendidas resolve horizontes que a primitiva sozinha não alcança — evidência externa para macros/métodos compostos.
- *Não transfere:* espaço latente contínuo, RL offline, políticas neurais — nada disso existe no d610.

---

## 3. Separação explícita: o que transfere vs o que é dependência

| Padrão | Fonte | Status para o jazz |
|---|---|---|
| Traço verificado como única fonte de asset | STaR (filtro), ASI (checks), `pcStore` | **transfere — já parcialmente feito** |
| Método = segmento linear de traço cujos efeitos fecham a tarefa | HTN-MAKER | **transfere — novo** |
| Precondição conservadora por interseção de contextos bem-sucedidos | CaMeL (version spaces) | **transfere — novo** |
| Caso indexado por (intent, classe, assinatura, falha) + traço completo | CHEF, Synapse, 4-R | **transfere — novo** |
| Retrieval: filtro duro + BM25/keyword, top-k ≤ 3 | APC, TOCOMAS (k=3), Nebel & Koehler | **transfere — parcial (`pcMatch`)** |
| Macro = subsequência contígua frequente e consistente | Macro-FF, GSP | **transfere — novo** |
| Trim/evicção por utilidade (λ ∝ log n; fails ≥ 2) | TroVE, Minton 1988, `pcScore` | **transfere — parcial** |
| Verificação por re-execução truncada anti-sucesso-espúrio | ASI | **transfere — novo (forge)** |
| Held-out/distinct-context replay antes de promover | ASI, SkillWeaver (honing), spec §7 | **transfere — novo** |
| Regra/manual com validation log e caso-âncora | AutoManual | **transfere — novo** |
| Indução/especialização por LLM, curriculum, self-proposal | Voyager, SkillWeaver, TroVE, CREATOR | **dependência — forge only** |
| Retrieval por embedding (skills, exemplares, A-MEM) | Voyager, Synapse, A-MEM | **dependência — substituir por BM25** |
| Adaptação de template por modelo no request | APC | **dependência — substituir por binding+screen** |
| Auto-verificação neural / avaliador LM | ASI, Reflexion | **dependência — substituir por `brVerify`** |
| Multi-agente em runtime, grafos de memória evolutivos | TOCOMAS, A-MEM | **dependência — recusado** |
| Espaço latente neural (VAE, RL offline) | LatPlan, TACO-RL | **não se aplica** |

Leitura honesta: **tudo que faz o sistema aprender é barato de guardar e caro de produzir** — e o caro já acontece em dois lugares: (a) o LLM quando existe (sessões `-p` online ou Fable em forge), (b) o harness determinístico quando não existe (execuções offline verificadas, `evaloffline.go`). O Method Forge consome as duas fontes indistintamente: o que entra em `cases.jsonl` é traço verificado, ponto.

---

## 4. Mapa file:line — o que já existe no jazz

| Recurso | Local | Estado | Nota |
|---|---|---|---|
| Método linear `{name,intent,priority,pre,steps,verify,risk,cost}` | `methods.go:42-72` (`mhCond` 42, `mhStep` 48, `mhCheck` 54, `mhMethod` 60) | **feito** | formato-alvo do asset induzido |
| Comandos literais de método ⊆ allowlist read-only | `methods.go:69` (`Cmds`), checagem no load `methods.go:80-110` | **feito** | screen de segurança já existente — candidato que declara comando fora de `cbReadCmds` morre no load |
| Execução Pyhop-style first-applicable + recover | `methods.go` (executor), `recover.go` | **feito** | linear, sem backtracking — compatível com Nebel & Koehler |
| Verificadores determinísticos (`file_exists`, `tests_passed`, `exit_zero`, `script_ok`, …) | `methods.go:1680` `brVerify` | **feito** | oráculo do forge e do runtime |
| Capability registry com `Pre`/`Effects`/`Verifier`/`Risk`/`Needs`/`DryRun`/`Undo` | `caps.go:67-80`, mapa de tools `caps.go:82-88` | **feito** | metadado suficiente para screen estático |
| Allowlist de comandos read-only + marcação de contexto | `caps.go:573` (`cbReadCmds`), `caps.go:614` (`cbReadCtx`), `caps.go:622` (`cbReadGranted`) | **feito** | invariante de segurança do forge |
| Cache de plano verificado (guarda só se verified, BM25-like, k≤3, evict fails≥2) | `plancache.go:42-46` consts, `56-73` `pcPlan`, `110` `pcLoad`, `352` `pcMatch`, `389` gate de store, `405-418` `pcScore`, `433` `pcStore` | **feito** | já implementa Retrieve+Retain do 4-R com gate |
| Hindsight por modelo extra (opcional, off default) | `plancache.go:554-628` | **feito — LLM** | separar: hint por template determinístico proposto em P8 |
| Lessons human-readable, caps 40/300B/64KiB, só via `jazz improve` | `lessons.go:40-53`, write `lessons.go:241-329` | **feito** | formato para regra-âncora de caso (AutoManual) |
| Classe de tarefa + assinatura de repo + pick por margem de wins | `lessons_reuse.go:27-83`, `lrPick` `88`, hint `111-132` | **feito** | chave de indexação de caso pronta |
| Categorias de falha observável + classificação | `recover.go:18-32`, `rvObs` `35`, `rvClassify` `53` | **feito** | índice de falha estilo CHEF |
| Ações de recovery com stop automático | `recover.go:132-149` | **feito** | honestidade: ask/handoff/unverified não executam |
| Budget de exploração | `recover.go:180-244` | **feito** | protege reuso de virar thrash |
| Dispatch multi-intent + intents level-0 read-only | `brain.go:40-68`, `574-622`, confirmação `brain.go:143` | **feito** | ponto de injeção de caso |
| NLU offline (regras→fuzzy→NB char-ngram), `nuMinConf=0.6` | `nlu.go:45,63` | **feito** | classificador de intent para retrieval (AgentReuse) |
| Extração determinística de candidatos de lesson | `improve.go:53-73,589-606`, auto-apply `1030-1067` | **feito** | padrão do extrator de casos |
| Scanners de sessões/eventos/eval/logs | `improve_load.go:331-409` | **feito** | fonte de dados do forge — tudo que o forge precisa já é gravado |
| Eval offline com métricas (success, false_success, perguntas, cmds, p50/p95, RSS) | `evaloffline.go:86-94,199-299,402-405`, fixtures `410-467`, relatório `248` | **feito** | held-out gate usa isto |
| Menus determinísticos offline | `offers.go` | **feito** | UX de fallback quando nenhum caso/método aplica |
| **Indução de método a partir de traço** | — | **não existe** | gap central (spec `offline-brain.md:79-85` não implementada) |
| `cases.jsonl` (traço+falha+provenance) | — | **não existe** | `pcPlan` guarda template, não o caso completo |
| Screen estático de candidato (Pre/Effects/Cmds/Verifier) | — | **não existe** | pieces existem; a checagem combinada não |
| Macro-operadores por frequência | — | **não existe** | — |
| Portão held-out + promoção/demote de métodos | — | **não existe** | spec §7 descreve; código não |
| Rerank de método por evidência de casos | — | **parcial** | `lrPick` ordena *planos*; métodos ordenam só por `priority` (`methods.go:80-110`) |
| Reparo indexado por categoria de falha (CHEF) | — | **parcial** | `rvClassify` rotula; nada consome o rótulo como índice de fix |

---

## 5. Arquitetura proposta: traço → caso → método/macro → screen → held-out → promote

```
            compile-time (máquina dev / Fable / `jazz forge`)          runtime (d610, offline)
  sessões -p ──┐
  eval fixtures ┼─→ [E1 extrator] ─→ cases.jsonl ─→ [E2 minerador] ─┐   brain.go
  logs/demote  ─┘      (win/fail,    (traço+índices)   (n-gramas,   │      │ intent (nlu.go)
                        rvClassify)                    frequência)  ▼      ▼
                                                          ┌─ casos ─→ retrieve (filtro+BM25,k≤3)
                                                          │           → hint ou método
                                                          │
                                              candidatos ─┴─→ [E3 screen estático]
                                              methods/*.json     (schema/Pre/Effects/
                                              candidates/*.json   Cmds/Verifier/Risk)
                                                                  │ passa
                                                                  ▼
                                                    [E4 held-out: dry-run + 2 contextos
                                                     distintos + fixture eval]
                                                                  │ passa
                                                    promote ─→ methods/learned/*.json ─→ mhLibrary
                                                                  │ fails ≥ 2 em reuso
                                                    demote  ←───────┘ (pcScore-style)
```

O runtime continua executando **o mesmo executor linear** (`methods.go`): nada de planner, nada de busca, nada de LLM. Toda a novidade é (a) como candidatos são fabricados e filtrados em forge-time, (b) como casos alimentam retrieve/rerank/hints, (c) como promoção é gated.

### 5.1 `cases.jsonl` — a unidade de memória

Caso = traço completo + índices de recuperação + rótulos de falha + provenance. Esquema proposto (uma linha por execução registrada, ~1–4 KB):

```json
{
 "v":1, "id":"c_2026-02-19_ab12", "session":"s_…", "src":"p|eval|forge|manual",
 "req_norm":"rode os testes", "intent":"run_tests", "conf":0.83,
 "slots":{"path":"."}, "task_class":"s/test", "repo_sig":"go|mod=jazzcli",
 "env":{"os":"linux","arch":"386","cmds":["go","git"]},
 "trace":[
   {"cap":"detect_test_runner","in":{},"out":{"runner":"go"},"ms":4,"ok":true},
   {"cap":"run_tests","in":{"cmd":"go test ./..."},"out":{"exit":0},"ms":2100,"ok":true}],
 "verify":[{"check":"tests_passed","ok":true}],
 "outcome":"win", "false_success":false,
 "recovery":[{"cat":"command_missing","at":1,"fix":"fallback npm"}],
 "wins":3,"fails":0, "method":"run_tests_go", "prov":{"from":"p","parent":null}
}
```

- **Retrieval sem embeddings (32-bit-safe):** bucket por `intent`; dentro do bucket, filtro duro por `task_class`/`repo_sig`/`env.cmds`; ranking pelo scorer tipo-BM25 já usado em `pcMatch` sobre `req_norm`+slots; top-k ≤ 3; tie-break por `wins−fails`. Índice invertido em memória: centenas de casos = dezenas de KB — irrelevante para 2 GB.
- **Casos negativos:** `outcome:"fail"` com `recovery.cat` vira entrada didática (CHEF): nunca servem para induzir método, mas servem para hint de reparo (P8) e para demote.
- **Retention:** só entra caso com `verify[*].ok==true` (STaR) ou `outcome=="fail"` rotulado — nada de traço não-verificado.

### 5.2 Indução de método linear (HTN-MAKER-lite)

Para cada caso `win` (ou união de casos do mesmo intent):

1. **Reconstruir o "estado" como conjunto de fatos** = `env` + `Out` registrados + `Effects` declarados das capabilities já executadas (`caps.go:67-80`). Não inferimos efeito não-declarado (LOCM-style inference fica como anotação candidata, nunca premissa).
2. **Varrer para frente** o traço; marcar o menor prefixo cujo conjunto de fatos cobre o objetivo definido pelo `verify` final (HTN-MAKER: "estado ⊇ efeitos da tarefa").
3. **Podar passos irrelevantes:** um `Cap` só fica se algum `Out` seu é consumido por `In` de passo posterior, se contribui `Effect` usado pelo objetivo, ou se é read-only com `Out` consumido — probes órfãs saem (exatamente a regra de HTN-MAKER).
4. **Generalizar slots:** valores de `In` que batem com entidades extraídas do `req_norm`/`slots` viram variáveis `{{slot}}`; o resto fica literal.
5. **`Pre` conservador (CaMeL):** interseção dos fatos contextuais presentes em todos os casos-fonte daquele intent — ex.: `project.test` existe se presente em todos; fatos não-unânimes viram `Pre` opcionais documentados ou caem fora.
6. **`Verify` herdado** do(s) caso(s)-fonte (o `mhCheck` que fechou a execução); se o caso usou `cmd_ran` + `exit_zero`, o método candidato pede pelo menos `exit_zero`.
7. Emitir `mhMethod` JSON em `candidates/` com `"prov":{"src":"forge","cases":[ids],"support":N}`.

### 5.3 Screen estático — `Pre`/`Effects` como filtro, não como planner

Candidato é rejeitado se **qualquer** um destes falhar (todas checagens O(passos), microssegundos):

- **S1 existência:** todo `mhStep.Cap` ∈ registry (`caps.go:82-88`); todo `In` respeita o schema da capability.
- **S2 comandos:** todo `Cmds[]` ∈ `cbReadCmds` (`caps.go:573`) — invariante já aplicada aos métodos embutidos (`methods.go:69`); candidato que precisa de comando novo morre aqui, sem exceção.
- **S3 cadeia Pre/Effects (alcance, não busca):** percorrer os passos em ordem; o `Pre` de cada passo deve ser coberto por `env`/slots ou por `Out`/`Effects` de passo anterior — check de *atingibilidade no traço*, nunca busca em espaço de estados.
- **S4 cobertura do verify:** a união de `Effects`/`Out` dos passos deve prover o que `Verify` exige — método que não pode produzir sua evidência é rejeitado.
- **S5 verifier disponível:** todo `mhCheck.Check` resolvível por `brVerify` (`methods.go:1680`); senão → `verifier_unavailable` → não promove (estado honesto, `recover.go`).
- **S6 risco/needs:** `risk` do método = max dos passos; `Needs` ⊆ `env.cmds` observado; `risk` > limiar → exige `dry-run` no held-out ou rejeita.
- **S7 anti-overfit:** `support` ≥ 2 casos **ou** 1 caso + replay em fixture distinto.

A diferença para "usar Pre/Effects como planner" é load-bearing: **nunca enumeramos estados nem procuramos sequências** — só validamos que uma sequência *já observada e verificada* continua coerente. É PRIAR adaptado a um modelo parcial.

### 5.4 Macro-operadores por frequência

- Mineração (GSP, §2.1): sequência canônica de `Cap` por caso; n-gramas contíguos n ∈ [2..6]; suporte ≥ 3 casos (ou ≥ 2 repositórios); consistência ≥ 80% `win` nos traços que o contêm; rank por `freq × n`.
- Materialização: macro vira um `mhMethod` candidato **cujos passos são inline** — o executor runtime permanece idêntico e cada método é autocontido (rollback trivial: apagar o arquivo). Alternativa futura: registrar macro como `Capability` só se tiver interface de slots uniforme e verifier próprio — evitar por ora (superfície de risco).
- Macros que falham em reuso (fails≥2) são demovidos como qualquer método (TroVE-trim/Minton: frequência de *uso bem-sucedido*, não de ocorrência).

### 5.5 Held-out e promoção

Portão em 3 estágios (spec `offline-brain.md:79-85` concretizada):

1. **Screen estático** (§5.3) — rejeito imediato, custo ~0.
2. **Dry-run/fixture:** método executa no sandbox de `evaloffline.go` sobre fixture da mesma classe de tarefa, com `DryRun`/verifier quando disponível — custo: uma execução de suite (~segundos–minutos no d610, em forge-time).
3. **Distinct-context ×2:** replay verificado em ≥2 contextos diferentes (outro `repo_sig` ou outro slot-value), com `false_success==0`; só então `jazz improve --promote <id>` move `candidates/x.json` → `methods/learned/x.json` (dir novo, lido por `mhLibrary` depois dos embutidos, com `priority` inicial = base −1 para nunca ofuscar método curado).
4. **Vida pós-promoção:** `wins`/`fails` por execução reusada (mesmo contador de `pcScore`); `fails ≥ 2` → demote para `candidates/` com nota de causa (`rvClassify` da falha). Provenance permanece no JSON.

---

## 6. Propostas implementáveis (Go, d610)

Métricas no vocabulário do repo (`evaloffline.go:248`): `no_llm_rate`, **reuso efetivo %** (tarefas fechadas reusando plano/método/caso ÷ resolvidas), `cmds/tarefa` (`CmdsPerTask`), `p50`/`p95` (`P50MS/P95MS`), `false_success`, perguntas. Todos os números abaixo são **alvos/estimativas**, não medições.

| # | Proposta | Mecanismo | Arquivos | Custo 32-bit | Métrica primária | Impacto | Esforço | Risco |
|---|---|---|---|---|---|---|---|---|
| **P1** | `jazz forge cases`: extrator de casos | Varre sessões/eventos/evals (reusa scanners `improve_load.go:331-409`), materializa `cases.jsonl` com trace+índices+`rvClassify`+provenance; idempotente, incremental por `session` | novo `forge_cases.go`; `improve_load.go`, `recover.go:53`, `lessons_reuse.go:27-83` | forge-only; ~1–4 KB/caso; índice < 100 KB | insumos para tudo; sem efeito direto | 5 | M | baixo — append-only, sem tocar em runtime |
| **P2** | Retrieval de casos em runtime | Bucket por `intent` → filtro `class/sig/env` → BM25-like (`pcMatch`) → k≤3; injeta hint do caso vencedor ou seleciona `pcPlan` associado | novo `cases.go`; reusa `plancache.go:352`; injeção em `brain.go`/`plancache.go:303-352` | O(casos do bucket)·palavras; µs–ms | reuso efetivo % ↑, cmds/tarefa ↓ | 5 | M | médio — hint enganoso; mitigado por filtro duro + wins−fails |
| **P3** | Method Forge: traço→`mhMethod` candidato | Algoritmo §5.2 (HTN-MAKER-lite: varredura forward, poda de probes órfãs, slots, Pre conservador) | novo `forge_method.go`; escreve `methods/candidates/*.json`; loader novo em `methods.go:80-110` (candidates listados, nunca executados) | forge-only | cobertura de intents sem método hoje | 5 | M-L | médio — generalização errada de slots; S7 + held-out |
| **P4** | Screen estático S1–S7 | §5.3; rejeita candidato inválido antes de qualquer execução | novo `forge_screen.go`; metadados `caps.go:67-80`, `cbReadCmds` `caps.go:573`, nomes de check `methods.go:1680` | forge-only; O(passos) | % candidatos rejeitados antes do held-out (qualidade do funil) | 5 | M | baixo — falso-rejeito atrasa indução, não corrompe |
| **P5** | Macro-operadores por frequência | §5.4: n-gramas [2..6], suporte≥3, consistência≥80%, inline nos métodos | novo `forge_macro.go`; consome `cases.jsonl` | forge-only | cmds/tarefa ↓ em tarefas compostas | 3 | M | médio — utility problem (Minton 1988); mitigado por trim+fails |
| **P6** | Portão held-out + promote/demote | §5.5: dry-run fixture + 2 contextos + `jazz improve --promote`; demote em fails≥2 com nota `rvClassify` | `improve.go` (subcomando), loader `methods.go`, `evaloffline.go` para fixtures | forge-only; uma suite por promoção | false_success = 0 mantido; % promoções que sobrevivem 30 dias | 5 | M | baixo-médio — gate conservador pode bloquear métodos bons (aceitável) |
| **P7** | Rerank de método por evidência | Estende `lrPick` (`lessons_reuse.go:88`) para ordenar `mhMethod` por wins−fails no mesmo (intent,class,sig) antes de `priority` | `methods.go` (ordenação em `mhTry`), `lessons_reuse.go`, `cases.go` | runtime: O(métodos do intent) | no_llm_rate ↑ (escolhe o método que já provou naquele contexto) | 4 | S | médio — feedback local míope; bounds: nunca rebaixar abaixo de método sem dados |
| **P8** | Reparo indexado por falha (CHEF) | Caso-negativo `(cat, contexto, fix-verificado)`; quando `rvClassify` repete `cat` no mesmo contexto, injeta hint determinístico do fix — substitui hindsight-LLM (`plancache.go:554-628`) por template | `recover.go`, `cases.go`, `improve.go` | runtime: lookup O(1) | taxa de recovery sem pergunta ↑; perguntas ↓ | 4 | M | médio — fix errado piora; só injeta hints com wins>0 |
| **P9** | Eval de indução | Fixtures novas cobrindo métodos induzidos + relatório `reuse%`; suite roda em forge e em CI do d610 | `evaloffline.go`, `results/` | forge/CI | suite 39/39 + induzidos; false_success 0 | 4 | S | baixo |

**Ordem sugerida:** P1 → P3 → P4 → P6 (espinha do forge) → P2 → P7 → P8 → P5 → P9 em paralelo. P5 por último de propósito: macro sem massa de casos é otimização prematura — Macro-FF só paga onde a estrutura se repete.

**Ganho composto esperado (alvo, não medido):** com ~30% de requests similares (evidência AgentReuse) e intents já reconhecidos a ~offline-level, reuso efetivo na faixa de dezenas de por cento das tarefas repetidas é plausível; cada reuso elimina a cadeia inteira de planejamento — p50 por tarefa reutilizada tende ao custo de execute+verify (~100–200 ms nos padrões medidos hoje), mantendo `no_llm_rate` intacto e `false_success=0` pelo screen+held-out.

---

## 7. O que NÃO adotar (dependências disfarçadas)

1. **Embedding retrieval de qualquer natureza** (Voyager skill index, Synapse exemplar memory, A-MEM): exige modelo de embedding no runtime — no d610 vira dependência de rede ou de dezenas de MB de pesos + CPU. Substituto já provado: keyword/BM25 (`pcMatch`, APC).
2. **Adaptação de plano por LLM no request** (APC `adapt`, "template + small model"): o custo está exatamente no lugar errado. Substituto: binding de slots + S3 (revalidação de cadeia) — se não liga, não reusa.
3. **Auto-verificação neural / reward model / LLM-judge** (ASI `V_L`, Reflexion, "LLM-as-judge" em geral): sucesso auto-declarado é `false_success` esperando para acontecer. O jazz só aceita `brVerify` + exit codes + estado de FS.
4. **Multi-agente/orquestração em runtime** (TOCOMAS e afins): já rejeitado no doc anterior; além de LLM por agente, adiciona grafo de responsabilidade que nada ganha num CLI único.
5. **Planner geral com Pre/Effects** (usar `caps.go` como domínio PDDL + busca): Nebel & Koehler — vira o problema que queremos evitar (espaço de estados, backtracking, garantias nulas com modelo parcial). Screen linear, nunca planner.
6. **Macros/métodos como capacidades executáveis opacas sem verifier próprio**: asset não-verificável é dívida. Todo asset promovido carrega `Verify` que `brVerify` resolve.
7. **Aprendizado de modelo de ação induzido de observação parcial** (FAMA/LOCM completos): desnecessário — nossas execuções são totalmente observadas pelo harness. No máximo anotação candidata, nunca premissa de screen.
8. **Espaço latente / RL offline / VAE** (LatPlan, TACO-RL): substrato neural não roda no d610 e nossas ações já são simbólicas.
9. **Curriculum/self-proposal em runtime** (Voyager, SkillWeaver): gerar tarefas novas é trabalho de forge-time; em runtime o curriculum é a fila real do usuário.
10. **Unbounded growth da biblioteca**: sem trim, qualquer biblioteca aprendida degrada (Minton 1988; TroVE ablação). Caps + evict + demote são invariantes, não opcionais.
11. **`hindsight` por LLM no caminho normal**: manter opt-in explicitamente marcado como dependência (`plancache.go:554-628`), nunca default; o equivalente offline é P8.

---

## 8. Protocolo de avaliação no d610

1. Implementar P1–P4; rodar `jazz forge` sobre sessões existentes + fixtures; reportar: candidatos gerados, % rejeitados no screen (S1–S7 por motivo), % promovidos.
2. `jazz eval offline` estendido: duplicar fixtures marcando quais exigem método induzido; medir suite completa — métricas exatas de `evaloffline.go:248`.
3. Critérios de sucesso (alvos, a confirmar em medição): `false_success` permanece **0**; `no_llm_rate` não cai; `reuso efetivo %` > 0 mensurável no corpus de repetição; `cmds/tarefa` menor em tarefas reusadas vs baseline sem casos; `p50` de tarefa reusada ≤ p50 atual.
4. Ablações internas obrigatórias (porque a literatura manda): sem S7/held-out (esperado: métodos frágeis promovidos); sem demote (esperado: decaimento lento da biblioteca); retrieval só por intent vs intent+sig (esperado: hints irrelevantes em repo diverso).

---

## 9. Referências

**Clássicos (sem arXiv; venue citado):**
- Hammond, *Case-Based Planning*, Academic Press, 1989 (CHEF).
- Aamodt & Plaza, AI Communications 7(1), 1994 (4-R).
- Carbonell, *Learning by Analogy*, Machine Learning, 1983.
- Kambhampati & Hendler, AIJ 55(2-3), 1992 (PRIAR).
- Nebel & Koehler, AIJ 76(1-2):427-454, 1995.
- Minton, IJCAI 1985 (MACROLET); Minton, AAAI 1988 (utility problem); Minton et al., ML 4(2), 1989 (EBL).
- Laird, Rosenbloom & Newell, ML 1(1), 1986 (chunking).
- Agrawal & Srikant, ICDE 1995 (GSP).
- Reddy & Tadepalli, ICML 1997 (XLearn).
- Ilghami, Nau, Muñoz-Avila & Aha, AIPS 2002 (CaMeL); Ilghami et al., ICML 2005 (CaMeL++).
- Hogg, Muñoz-Avila & Kuter, AAAI 2008 (HTN-MAKER); journal: Computational Intelligence 32(1), 2016.
- Cresswell, McCluskey & West, KER 28(2), 2013 (LOCM).
- Aineto, Jiménez & Onaindia, AIJ 272, 2019 (FAMA; arXiv:1903.01153 preliminar).
- Cypher (ed.), MIT Press, 1993 (Watch What I Do); Allen et al., AAAI 2007 (PLOW); Tecuci, Academic Press, 1998 (Disciple).
- Spalzzi, AI Communications 14(1), 2001 (survey case-based planning).

**arXiv:**
- arXiv:2303.11366 Reflexion (2023) · arXiv:2305.14318 CREATOR (2023) · arXiv:2305.16291 Voyager (2023) · arXiv:2305.17126 LATM (2023) · arXiv:2306.07863 Synapse (2023) · arXiv:2308.10144 ExpeL (2023/AAAI-24) · arXiv:2310.01714 Analogical Reasoners (2023) · arXiv:2310.03714 DSPy (2023) · arXiv:2401.12869 TroVE (2024) · arXiv:2405.16247 AutoManual (2024) · arXiv:2409.07429 AWM (2024) · arXiv:2502.12110 A-MEM (2025) · arXiv:2504.06821 ASI (2025) · arXiv:2504.07079 SkillWeaver (2025) · arXiv:2506.14852 APC (2025) · arXiv:2512.21309 AgentReuse (2025) · arXiv:2203.14465 STaR (2022) · arXiv:2209.08959 TACO-RL (2022) · arXiv:1711.10160 Snorkel (2017) · arXiv:1705.00154 LatPlan (2017) · arXiv:1109.2154 Macro-FF (depósito 2011; JAIR 2005) · arXiv:1903.01153 FAMA preliminar (2019) · arXiv:2609.37953 TOCOMAS / arXiv:2609.32192 CoMemBench (2026, ver doc anterior).

**Internos:** `docs/research/tocomas-plan-reuse.md` (reuso k=3, screen estrutural), `docs/research/math-offline-brain.md` (calibração de limiares), `docs/design/offline-brain.md:79-85` (spec §7 — gap confirmado), `results/dell-0.9-progress.md` (baseline d610).
