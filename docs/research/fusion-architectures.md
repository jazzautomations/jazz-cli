# Arquiteturas de fusão multi-modelo: Sakana Fugu, OpenRouter Fusion, Devin Fusion

Pesquisa em fontes primárias, buscadas em 2026-10-20. Objetivo: decidir o que o jazz-cli copia
dessas três arquiteturas e o que fica de fora.

O jazz-cli roda num Dell com 1 núcleo lento, contra um pool de modelos **gratuitos**
(kilo gateway: `poolside/laguna-s-2.1:free`, `minimax-m3.1-flash:free`, cohere
`north-mini-code:free`, nvidia nemotron free; groq/gemini/openrouter são opcionais, com chave).
Cada requisição leva de 20 a 60 s. A CPU fica ociosa, e o gargalo é a latência dos modelos e a
cota do free tier.

Sobre o método: as páginas foram lidas via WebFetch, que entrega um resumo do conteúdo e não o
texto bruto. As citações entre aspas são trechos curtos que vieram nesse resumo. Os números
foram conferidos contra tabelas que o próprio resumo transcreveu. Valores que só apareciam em
imagens ou gráficos não foram lidos e estão marcados.

---

## 1. Sakana Fugu: orquestrador treinado sobre um pool de LLMs

### Mecanismo
- O Fugu é exposto como **um único modelo**: "Both models can be accessed via a single
  OpenAI-compatible API" (sakana.ai/fugu-release). Por trás, "Sakana Fugu is itself a language
  model trained to call various LLMs in an agent pool", e ele pode chamar instâncias de si mesmo.
  Seleção de modelo, delegação, verificação e síntese ficam internas. O próprio Fugu decide se
  responde direto ou se monta uma equipe.
- Há duas camadas, segundo o relatório técnico (arXiv 2606.21228, v1 de 19/06/2026, v2 de
  23/06/2026):
  - **Fugu**, focado em latência. Escolhe **um** worker por entrada (ou por turno, em tarefas
    agênticas). A latência é "comparable to a direct call to a frontier model". Herda do TRINITY,
    mas "Unlike the Trinity coordinator, Fugu does not assign roles". Treino em duas etapas.
    Primeiro, SFT com perda KL contra uma distribuição softmax derivada das recompensas médias
    de cada worker, atualizando só uma cabeça de seleção leve e escalas de valores singulares.
    Depois, busca evolutiva (sep-CMA-ES) maximizando a recompensa terminal 0/1 em tarefas de
    código multi-turno.
  - **Fugu-Ultra**, focado em qualidade. Herda do Conductor. Monta workflows multiagente de
    **até 5 passos**: escreve subtarefas em linguagem natural, atribui IDs de workers e define
    listas de acesso. Os papéis emergem das subtarefas (exemplos do relatório: GPT como
    builder, Opus como debugger, Gemini/GPT como agregador). Treino com **GRPO sem penalidade
    KL**: recompensa 0 para saída não parseável, 0,5 para workflow bem-formado mas errado e
    1 para resposta correta. Pool de treino: Gemini-3.1-Pro, Claude-Opus-4.8 e GPT-5.5. Troca
    "additional latency for higher quality".
- Papers de origem (ICLR 2026, ambos citados na página de lançamento):
  - **TRINITY: An Evolved LLM Coordinator** (arXiv 2512.04695). Coordenador de cerca de 0,6B
    parâmetros mais uma cabeça de cerca de 10K parâmetros. Em múltiplos turnos, atribui a cada
    LLM escolhido um dos papéis **Thinker, Worker ou Verifier**. É otimizado por estratégia
    evolutiva (sep-CMA-ES), e não por RL como método principal. Os autores argumentam que ES
    supera RL e imitation learning em alta dimensão com orçamento apertado. Reporta 86,2% no
    LiveCodeBench.
  - **Learning to Orchestrate Agents in Natural Language with the Conductor**
    (arXiv 2512.04388). Conductor de 7B **treinado por RL** fim a fim, em pools de agentes
    aleatorizados. Produz a topologia de comunicação e as instruções para cada worker, e pode
    escolher a si mesmo como worker. O abstract não traz números.

### Resultados medidos (Tabela 1 do relatório técnico, autorreportados)
| Benchmark | Fugu-Ultra | Fugu | Melhor modelo isolado na tabela |
|---|---|---|---|
| SWE-Bench Pro | 73,7 | 59,0 | Opus 4.8: 69,2 |
| Terminal Bench 2.1 | 82,1 | 80,2 | GPT-5.5: 78,2 |
| LiveCodeBench | 93,2 | 92,9 | n/d |
| GPQA Diamond | 95,5 | 95,5 | Gemini 3.1: 94,3 |
| Humanity's Last Exam | 50,0 | 47,2 | GPT-5.5: 41,4 |

O relatório não traz número de chamadas nem latência medida por camada. Li apenas os primeiros
100 mil caracteres do HTML. A página de lançamento só mostra os números em imagens.

### O que o jazz-cli ADOTA
- **Um conductor por trás de uma interface de "um modelo só".** O usuário chama o jazz, e o
  jazz decide qual modelo grátis atende cada passo.
- **A ideia de recompensa por tarefa verificável.** O placar do jazz atualiza estatísticas com
  sinais objetivos: teste passou, build ok, tool call parseável. O espírito é o mesmo da
  recompensa 0/0,5/1 do Fugu-Ultra, mas usada como contagem e não como gradiente.
- **A divisão em duas camadas latência vs. qualidade.** No jazz, o modo normal é um worker por
  passo, como o Fugu. `--fusion` e `--panel` funcionam como uma "camada ultra" opcional.

### O que NÃO adota, e por quê
- **Política aprendida.** O "conductor" do jazz é um **bandit UCB1** sobre estatísticas por
  (modelo, tipo de tarefa), persistidas em `model-scores.json` e semeadas com os resultados de
  eval medidos. Não há rede, SFT, ES nem RL, e não existe política generalizável entre
  instalações. As estatísticas são locais de cada instalação. O tipo de tarefa vem de um
  **classificador determinístico por regex**, e não dos estados ocultos de um LM como no
  TRINITY. Isso é bem mais fraco que o Fugu: não lê a consulta, só a categoria. Em troca, é
  barato, auditável e roda num núcleo de 2006.
- **Workflows de até 5 passos com topologia gerada.** Com 20 a 60 s por chamada, um workflow de
  5 passos significa minutos por resposta, e a cota grátis não aguenta.
- **Pool de modelos de fronteira.** Os ganhos do Fugu vêm de orquestrar Opus/GPT/Gemini. Nada
  indica que se transfiram para um pool de modelos free menores.

---

## 2. OpenRouter Fusion: painel paralelo, juiz e síntese

### Mecanismo
- Linha do tempo: o May Release Spotlight (publicado em 01/06/2026) anuncia "Model Fusion" em
  uma frase, "Route your prompt to multiple models in parallel and synthesize their responses
  into a single, higher-quality answer", disponível como plugin de API, server tool e no
  chatroom. O post com benchmark, "Surpassing Frontier Performance with Fusion", saiu em
  12/06/2026, e o explainer em 10/09/2026.
- Fluxo (explainer e docs):
  1. O **modelo chamador** recebe o prompt com a ferramenta Fusion e decide se a invoca.
  2. Um **painel** de 1 a 8 modelos responde em paralelo, cada um com web search e fetch.
  3. O **juiz/analista**, que roda sempre em temperatura 0, compara as respostas em vez de votar:
     consenso, contradições, lacunas de cobertura, insights únicos e pontos cegos. Nas palavras
     do explainer, "Three models repeating the same unsupported claim don't automatically make
     that claim correct."
  4. O modelo chamador escreve a resposta final a partir da análise.
- Não há recursão: painel e analista não podem chamar Fusion de novo. Existe um preset
  `general-fast`/`openrouter/fusion-flash` com painel de latência homogênea para turnos agênticos.

### Resultados medidos (DRACO, deep research, 100 tarefas, autorreportados)
| Configuração | Score |
|---|---|
| Fusion: Fable 5 + GPT-5.5 (sintetizado por Opus 4.8) | 69,0% |
| Fusion: Opus 4.8 + GPT-5.5 + Gemini 3.1 Pro | 68,3% |
| Fusion: Opus 4.8 + GPT-5.5 | 67,6% |
| Fusion: Opus 4.8 + Opus 4.8 (auto-fusão) | 65,5% |
| Solo: Fable 5 (93 de 100 tarefas, por filtro de conteúdo) | 65,3% |
| Fusion: Gemini 3 Flash + Kimi K2.6 + DeepSeek V4 Pro | 64,7% |
| Solo: Opus 4.8 | 58,8% |

- Custo: o painel padrão de 3 modelos custa "roughly 4–5×" uma completion. O painel barato ficou
  a cerca de 1% do Fable 5 "while being 50% of the cost" (o valor absoluto do custo só aparece
  em gráfico, não lido).
- Latência: "often 2-3x longer than a standard call" quando Fusion é acionado.
- **Divisão 3/4 síntese e 1/4 diversidade: NÃO consta das fontes primárias.** O post de junho
  diz apenas "a meaningful chunk of Fusion's lift comes from the synthesis step itself". O
  "~75%" aparece só em cobertura secundária (resumo de busca, não verificado). Ele **bate com uma
  conta feita sobre a tabela**: a auto-fusão rende +6,7 pp (58,8 → 65,5), e Opus + GPT-5.5 rende
  +8,8 pp (→ 67,6). 6,7/8,8 ≈ 76%. É uma inferência derivada de uma única execução num
  benchmark de deep research, e não uma afirmação da OpenRouter.
- Ressalvas da própria fonte: DRACO mede pesquisa, não código. O explainer desaconselha Fusion
  em caminhos sensíveis a latência, em cargas que exigem reprodutibilidade (evals, regressão) e
  em tarefas simples.

### O que o jazz-cli ADOTA
- **Painel com síntese (`--panel on|off`, padrão off)** apenas em passos difíceis e **sem tool
  call**: planejamento de tarefas L, causa-raiz depois de 2 execuções de teste falhas e resumos de
  compactação. São 2 a 3 candidatos grátis em paralelo, e o lead sintetiza.
- **Juiz que compara em vez de votar.** O prompt de síntese pede consenso, contradições e lacunas
  de forma explícita, sem maioria simples.
- **O dado de que a síntese pesa mais que a diversidade** justifica painéis pequenos (2
  candidatos), mesmo com modelos parecidos. Fica pendente medir isso no nosso eval.
- **Hedging:** requisições pequenas e idempotentes vão para 2 candidatos, e vence a primeira
  resposta completa. Não há síntese aqui. É só para cortar a cauda de latência de 20 a 60 s.

### O que NÃO adota, e por quê
- **Fusion em chamadas comuns de ferramenta.** Um custo de 2 a 3× em latência sobre 20 a 60 s é
  inaceitável no loop de edição.
- **Painel de 8 modelos ou web search por painelista.** Isso estoura os rate limits do free tier.
  Painel paralelo consome cota N+1 vezes. É o custo real no nosso contexto, já que não pagamos em
  dinheiro.
- **Etapa separada de juiz mais chamador.** No jazz, o lead faz análise e síntese numa única
  chamada para economizar uma rodada de 20 a 60 s.

---

## 3. Devin Fusion (Cognition): lead e sidekick

### Mecanismo
- O post "Devin Fusion" saiu em 29/06/2026 e o post "Introducing Fusion in Devin Desktop & CLI"
  em 11/09/2026.
- Dois agentes rodam em paralelo. O **lead** (fronteira) guarda o plano, interpreta
  ambiguidades, delega, monitora e faz a revisão final. O **sidekick** (mais barato) explora o
  código, implementa, roda testes e reporta. Os dois trocam só "briefs, results, and feedback",
  e o brief leva restrições e critérios de sucesso. Nunca trocam a conversa inteira.
- **Contexto persistente por modelo.** Cada agente "builds its own persistent context, taking
  full advantage of prompt caching". O lead "can take control back when the sidekick is out of
  its depth".
- **Roteamento dinâmico (post de junho).** Classificadores leves sinalizam quando trocar de
  agente ou de modelo. A troca acontece **na compactação de contexto**, "which would trigger a
  cache miss anyway". A crítica ao roteamento ingênuo: "They look nice on most benchmarks but
  fail to write code you'd actually merge". O post de setembro acrescenta que trocar de modelo
  no meio da tarefa quebra o cache e que "the initial prompt isn't enough to know the difficulty
  of the task".

### Resultados medidos (autorreportados)
- Post de junho: **35% de redução de custo** na publicação original, depois revisado para
  "até 60%" no FrontierCode. A Fusion com Fable 5 sai 41% mais barata que o harness puro com
  Fable 5. No FrontierCode 1.1 Extended, a Devin Fusion marca 63,1 a $1,35 por tarefa, contra
  Fable 5 (xhigh) com 64,9 a $10,53 e Opus 5 com 63,6 a $3,51. Checagem interna: 88% dos PRs
  mergeados de usuários com Fusion foram conduzidos inteiramente pelo roteador automático.
- Post de setembro, Fable 5.1 sozinho vs. Fusion (Fable 5.1 + SWE-2):

  | Benchmark | Score solo → Fusion | Δ custo |
  |---|---|---|
  | DeepSWE 1.1 | 64,3 → 63,1 | **−46%** |
  | Terminal-Bench 4 | 57,6 → 56,1 | −23% |
  | SWE-Atlas QnA | 64,8 → 65,9 | −34% |
  | Vals Code Migration | 54,6 → 57,3 | −41% |
  | FrontierCode 1.1 Ext. | 63,6 → 63,5 | −38% |

  No Artificial Analysis Coding Agent Index v1.5: 31% mais barato que o Claude Code (Opus 5.5) e
  39% mais barato que o Codex. A faixa "35 a 46%" do briefing é coerente: 35% é o número
  original de junho e 46% o máximo da tabela de setembro. Em alguns benchmarks a perda de
  qualidade é de 1 a 6 pontos, e não zero (ex.: Terminal-Bench 4 com Astra cai de 55,6 para 50,0).

### O que o jazz-cli ADOTA
- **Lead/sidekick sobre a maquinaria de subagente existente (ferramenta nova `sidekick`, irmã da `task`, que pode editar) (`--fusion on|off`, padrão off
  até ser medido).** O lead é o modelo com melhor placar no bandit para o tipo de tarefa, e
  planeja e revisa. O sidekick é um modelo rápido que recebe um **handoff compacto** (objetivo,
  arquivos, critérios de aceite, restrições), trabalha **no próprio contexto** e devolve um
  relatório compacto. O lead revisa o diff antes de dar a tarefa como concluída, no papel de
  Verifier, à moda do TRINITY.
- **Nada de mandar a conversa inteira entre modelos.** Só brief e relatório, o que também poupa
  tokens, e no free tier isso significa cota.
- **Trocar de modelo só em fronteiras naturais** (compactação, novo subagente), sem troca
  arbitrária no meio do turno.
- **Ceticismo com roteamento ingênuo.** O bandit escolhe o modelo, mas o critério de "feito" é a
  revisão do lead com testes, e não o roteamento em si.

### O que NÃO adota, e por quê
- **Classificadores aprendidos de troca dinâmica.** Usamos regex e UCB1.
- **Os ganhos de prompt caching** que motivam o contexto persistente: os endpoints grátis não
  garantem cache. No jazz, o contexto separado serve para foco e economia de tokens, não para
  cache hit.
- **Lead de fronteira de verdade.** Nosso "lead" é só o melhor modelo grátis segundo o placar.
  A diferença de capacidade entre lead e sidekick é pequena, então não se espera reproduzir os
  números da Cognition. Por isso o padrão é off até haver eval pareado.

---

## Síntese para o jazz-cli

| Elemento | Fonte de inspiração | No jazz | Padrão |
|---|---|---|---|
| Conductor | Fugu/Conductor/TRINITY | UCB1 por (modelo, tipo-regex), `model-scores.json`, semeado do eval | registro on; seleção `--omni-judge off` |
| Lead/sidekick + verifier | Devin Fusion, papel Verifier do TRINITY | via `task`, handoff compacto, revisão do diff | `--fusion off` |
| Painel + síntese | OpenRouter Fusion | 2 a 3 grátis, só em passos difíceis sem tool | `--panel off` |
| Hedging | (nenhuma das três; prática de cauda de latência) | 2 candidatos, vence o primeiro completo | junto com `--omni-judge` |

Riscos: os painéis e o hedging multiplicam o consumo da cota grátis (429). Todos os ganhos
citados são autorreportados, medidos com modelos de fronteira, e não se transferem
automaticamente para modelos free.

---

## Fontes (todas buscadas em 2026-10-20)
- https://arxiv.org/abs/2606.21228 (Sakana Fugu Technical Report, abstract)
- https://arxiv.org/html/2606.21228 (relatório completo em HTML; lidos os primeiros 100k caracteres)
- https://sakana.ai/fugu-release/
- https://arxiv.org/abs/2512.04695 (TRINITY)
- https://arxiv.org/abs/2512.04388 (Conductor)
- https://openrouter.ai/blog/announcements/may-release-spotlight/ (o link antigo https://openrouter.ai/announcements/may-release-spotlight só redireciona)
- https://openrouter.ai/blog/announcements/fusion-beats-frontier/
- https://openrouter.ai/blog/insights/fusion-explainer/
- https://openrouter.ai/docs/guides/features/plugins/fusion
- https://cognition.com/blog/devin-fusion
- https://cognition.com/blog/fusion-in-devin-cli
- https://docs.devin.ai/cli/fusion.md

## Não verificado
- **"~3/4 do ganho vem da síntese, 1/4 da diversidade".** Não está em nenhuma fonte primária da
  OpenRouter. Aparece só em cobertura secundária vista em resultado de busca, sem abrir a página.
  É compatível com a conta 6,7/8,8 ≈ 76% sobre a tabela da OpenRouter, mas essa conta é inferência
  minha.
- **Papéis Thinker/Worker/Verifier no Fugu.** São do TRINITY. O relatório do Fugu diz
  explicitamente que o Fugu **não** atribui papéis, e no Fugu-Ultra os papéis são emergentes.
- **O "Conductor" como orquestrador do Fugu.** A página de lançamento não usa o termo, e o
  relatório liga o Conductor (treinado por RL/GRPO) só ao **Fugu-Ultra**. O Fugu base usa SFT
  mais evolução (sep-CMA-ES), não RL.
- **Números de benchmark e custo que só aparecem em imagens** (página sakana.ai e gráfico de
  custo da OpenRouter): não lidos.
- **Latência medida e número de chamadas por camada do Fugu:** o relatório não traz.
- **Os últimos ~27k caracteres do HTML do relatório do Fugu** não foram lidos.
- **Detalhes do handoff na documentação do Devin** (docs.devin.ai): a página não descreve o
  conteúdo do brief. A descrição veio dos posts do blog.
- **Data exata de lançamento da OpenRouter Fusion (12/06 vs 17/06):** usei a data do post
  primário, 12/06/2026. A de 17/06 vem de cobertura secundária.
