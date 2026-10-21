# Omni judge: modelo por tarefa, papéis, hedging, painel e fusão (2026-10-20)

Contexto medido no alvo (Dell, 1 núcleo): durante uma eval a CPU fica 84% ociosa e sobra
1,5 GB de RAM. O gargalo é a latência do modelo grátis (20–60 s por requisição), não o hardware.
Daí: escolher o melhor modelo por tipo de tarefa, paralelizar o que é barato e independente, e
nunca pagar latência extra no passo comum. Pesquisa de referência: `fusion-architectures.md`.

## Peças (código)

| peça | arquivo | padrão |
|---|---|---|
| classificador de tarefa (regex + sinais do repo; LLM barato só se ambíguo) | `route_task.go` | sempre classifica |
| placar por (modelo, tipo) + bandit UCB1 (o "conductor") | `judge.go`, seção `tasks` de `model-scores.json` | registro **on**; seleção `--omni-judge off` |
| papéis `roles: {planner, editor, verifier}` (`auto` = juiz) | `judge.go`, `judge_hedge.go` | auto |
| verificador (2º modelo revisa o diff antes do fim) | `judge_hedge.go` | `--verifier off` |
| hedging de pedidos pequenos (2 candidatos, vence o 1º) | `judge_hedge.go` | liga junto com o juiz |
| painel + síntese (plano de tarefa L; causa raiz após 2 testes falhos) | `judge_fusion.go` | `--panel off` |
| fusão lead/sidekick (contextos separados e persistentes) | `judge_fusion.go` | `--fusion off` |
| lanes paralelas nas evals | `evals/v1/run.sh`, `evals/v2/run.sh` | `LANES=1` |

Ganchos em código existente (uma chamada cada): `Agent.Run` → `ojStartTurn`; `chatWithFallback` →
`ojOrder`; `summarize` → `ojHedgedSummary`; `toolSpecs`/`execTool`/`permit` → sidekick;
`acStopGate` → `ojVerifyGate`; `hgAfterTool` → `ojAfterTest`; `bgEndTurn` → `ojEndTurn`;
`rlEnd` → `ojObserveReq`; `bgScoreOK/Stall` → contadores do turno; `smDefaultClassify` → hedge.
Toda decisão vira um registro de debug `"kind":"route"` (`role` = classify | editor | lead |
fallback | outcome | classifier | smart-classifier | compact | verifier | panel | panel-synth |
sidekick), com candidatos, scores, escolhido e motivo — para o stream de eventos da
observabilidade consumir.

## 1. Classificador

Determinístico primeiro: 11 tipos (`bugfix, debug_log, feature, refactor, big_file, design_doc,
explain, shell, frontend, config, general`). Cada regra soma pontos; o maior vence, empate é
desfeito por especificidade (debug_log > big_file > design_doc > refactor > …) e marcado
`ambiguous`. Nenhuma regra com ≥ 2 pontos → `general` + `ambiguous`. Sinais do repo (caminhada
de no máximo 3000 entradas, cacheada por sessão): linguagens, nº de arquivos, testes e docs de
design presentes, e o **tamanho dos arquivos citados** (≥ 1200 linhas → `big_file`). Tamanho
S/M/L vem de `bgClassify` (budget.go), com ajustes (feature com ≥ 4 bullets → L).
Só com o juiz ligado e pedido ambíguo há uma chamada barata (`rtLLMPrompt`, hedged, 20 s).
Testes (`route_task_test.go`): os 10 prompts da v2 (com o repo de cada tarefa), os 10 da v1, os
14 do redteam, 14 terminal-tasks, 3 polyglot e 4 swebench (trechos em `testdata/route/`) — todos
com o tipo esperado. Os pedidos "faça o que o arquivo manda" do redteam ficam `general`/ambíguo
de propósito.

## 2. Placar e seleção (o "juiz")

Por (modelo, tipo), somas mescladas sob flock: turnos ok / falha / timeout / corte, requisições,
latência das requisições bem-sucedidas, 429, stalls de primeiro byte, tempo de parede, custo.
**Sucesso** = resposta final (`StopDone`) depois de passar a guarda de conclusão honesta e o
quality gate, e — se o turno editou código e o projeto tem comando de teste — testes rodados
depois da última edição. `quality_failed`, `until_failed`, `step_limit`, `loop`,
`unverified`, `error` = falha; `timeout` e `truncated` têm contadores próprios; interrupção,
negação e orçamento não contam (decisão do usuário). O turno é atribuído ao modelo que deu a
resposta final; requisições/429/stalls de outros modelos do turno contam só nos deles.
Registro automático no fim de cada `-p` e de cada turno interativo (`bgEndTurn`).

Seleção (só com `--omni-judge on`): candidatos = a cadeia que o roteador já filtrou por `Need`
(tools, janela, refs mortos, política free/paid; pagos só com `omni.paid: always`). Score UCB1:
`média + 0,2·√(ln N / n) − 0,10·min(1, parede/900 s) − 0,15·min(1, 4·taxa429) − 0,30 se o
limitador diz "não pronto" − 1 se pago`. Com probabilidade ε (padrão 0,1; `judge.epsilon`,
`JAZZ_JUDGE_EPSILON`) o 1º lugar vai para outro candidato grátis e pronto. A cadeia de fallback
depois do escolhido é reordenada pelo mesmo score: um stall de primeiro byte cai no próximo
melhor (o caminho de stall já existia; agora a ordem é a do placar).
`jazz models --scores` imprime a tabela (PT) e as sementes.

### Sementes (cold start) e procedência

Cada tarefa medida vale 0,5 pseudo-turno (outra versão/hardware), no máximo 6; a linha do tipo
e a linha "*" do modelo são somadas. Fontes:
- `poolside/laguna-s-2.1:free` — v2 no Mac, jazz 0.4.1–0.4.3, `~/jazz-eval-results/v2-20261020-*`:
  21 PASS em 21 execuções (01 3/3, 02 1/1, 03 4/4, 04 1/1, 05 1/1, 06 1/1, 07 1/1, 08 2/2,
  09 3/3, 10 3/3; parede média ~330 s); terminal-tasks no Dell 13/14 (a falha é feature);
  polyglot 13/17 feitas (= design_doc); v1 10/10 (`benchmarks.md`).
- `minimax-m3.1-flash:free` — v2 no Dell 7/10, as 2 falhas conhecidas editaram testes (hoje
  recusado pelas guardas); Mac 3/3 em 01 (185 s), 03 (62 s), 08 (444 s).
- Todo o resto (cohere north-mini-code, nemotron, gpt-oss, deepseek, glm, gemini…): **sem
  medição**; prior fraco de 1 pseudo-turno = Q do preset ·0,8/100 e parede suposta de 450 s.
Resultado no pool keyless do kilo: laguna lidera todos os tipos no cold start (teste
`TestOjColdStartPrefersMeasured`); exploração ε e as medições locais mudam isso com o tempo.

**Diferença honesta para o Conductor da Sakana**: aqui não há política aprendida nem treino
(nem RL, nem CMA-ES). É estatística por instalação sobre um tipo de tarefa vindo de regex; o
"modelo de mundo" é uma tabela de contagens. Barato, explicável, e fraco onde o Conductor é forte
(decompor a tarefa e compor modelos num workflow).

## 3. Papéis

`roles.planner` (classificador e pedidos pequenos; auto = tier fast/cadeia, o mais rápido
medido primeiro), `roles.editor` (auto = escolha do juiz), `roles.verifier` (auto = melhor
candidato grátis ≠ editor). A chave é `roles` e não `models` porque `models` já guarda as
configurações por modelo. O verificador é **um** pedido sem ferramentas: pedido + diff do turno
(snapshot do Toolbox; funciona sem git, ao contrário do `--review`), resposta LGTM ou lista; os
defeitos voltam uma vez ao editor.

## 4. Hedging

Pedidos pequenos e idempotentes (classificador de tarefa, classificador do modo smart, resumo
de compactação, verificador) vão a 2 candidatos ao mesmo tempo; o primeiro com resposta não
vazia vence, o outro é cancelado (contexto). Candidato que o limitador recusa não é disparado;
falha dos dois → próximos dois. O turno do editor nunca é hedged. Medição: seção abaixo.

## 5. Painel e fusão

Painel (`--panel on`): só passos difíceis sem ferramenta — plano de tarefa tamanho L (antes da
1ª requisição) e causa raiz depois da 2ª execução de testes falha no turno. 2–3 candidatos grátis
em paralelo, síntese pelo modelo do turno. Compactação usa hedging, não painel: o painel
triplicaria um pedido que já é o maior do turno.
Fusão (`--fusion on`): o modelo principal é o lead (`fusion.lead` ou o juiz) e ganha a ferramenta
`sidekick` (goal, files, acceptance, constraints). O sidekick (`fusion.sidekick` ou o grátis mais
rápido ≠ lead) roda em **contexto próprio que persiste na sessão**, pode editar e rodar comandos,
compartilha as guardas de conclusão honesta do lead (testes protegidos), e devolve relatório de
até 12 linhas. Os arquivos que ele mudou entram no turno do lead (quality gate e "testes depois
da última edição" os veem).

## 6. Lanes

`LANES=N ./run.sh` (v1 e v2): todas as pastas são preparadas antes, `tasks/` fica trancada
durante todos os agentes, até N `jazz -p` simultâneos (polling de `jobs`, sem `wait -n` por causa
do bash 3.2 do macOS), verificadores no fim em ordem. Mesmo `JAZZ_HOME` (padrão) ou
`LANE_HOME=1` (cópia por tarefa). Bench (`work/bench/*/run.sh`): não alterados; para
paralelizar, rode N processos com `TASKS` disjuntos e `OUT` diferentes.
Concorrência verificada (`TestOjScoresConcurrentProcesses`, 4 processos × 15):
- `model-scores.json`: seguro (flock + soma de deltas).
- `ratelimit.json`: **perdia contagem** — o contador diário/mensal era mesclado com `max()`, então
  dois processos contavam 36 de 60 requisições. Corrigido: cada processo soma seus incrementos
  ainda não gravados ao valor do arquivo. As janelas de minuto/hora já eram união de timestamps.
- `spill/out-HHMMSS-N.txt` e `jobs/HHMMSS-jN.log` colidiam entre processos no mesmo segundo
  (contador por processo): o nome agora inclui o pid.
- sessões (`<data>-<aleatório>`) e `plans/<projeto>.jsonl` não colidem (pasta de trabalho diferente).

## Medições (Mac, pool keyless do kilo, 2026-10-20)

Binário deste branch, `kilo` sem chave (laguna, minimax, cohere north-mini-code, nemotron super e
lightning), `JAZZ_HOME` copiado de `work/jhome-a`, shim de `timeout`, LIMIT 900 s. Uma
execução por célula: n minúsculo, nada aqui é estatisticamente significativo.

**A. Juiz + verificador, v2 01/05/10** (`--omni-judge on --verifier on`, sequencial,
`~/jazz-eval-results/v2-20261020-220143`): **3/3 PASS** (169 s, 169 s, 244 s; 10/8/16
requisições). O juiz escolheu laguna nas 3 (UCB 1,05/1,05/1,04 contra 0,84 do 2º). Verificador:
01 e 05 LGTM pelo minimax (hedge cohere+minimax, 5,2 s e 3,4 s; ~530–580 tokens de entrada,
96–276 de saída: custo desprezível perto dos 90–120 mil do turno); em 10 os dois candidatos
(cohere, nemotron) devolveram resposta vazia e o verificador foi pulado → agora tenta até 4
candidatos e dá 4000 tokens de saída. Melhora de taxa de acerto **não medível**: o baseline já
passa nessas 3 (laguna 21/21 nas execuções anteriores da v2).

**B. Lanes, v2 03/04/06/07** (`LANES=2 --omni-judge on`, `v2-20261020-221240`): **4/4 PASS**,
parede 682 s contra 1020 s de soma das tarefas (1,5×). Nenhum 429, nenhuma linha de limite. A
exploração ε disparou uma vez: 04 (feature) foi para minimax e passou em 48 s/12 requisições.

**C. Fusão e painel, v2 02 e 09** (três braços em paralelo às 22:24; depois mais dois às 22:49):

| braço | 02 (feature multiarquivo, L) | 09 (ler design, S) |
|---|---|---|
| baseline (22:24, 3 processos) | PASS 574 s · 34 req · 849 k tok | PASS 440 s · 22 req · 494 k tok |
| `--fusion on`, nota "pode delegar" | PASS 792 s · 34 req · 1 190 k tok | PASS 637 s · 22 req · 574 k tok |
| `--panel on` | **FAIL** (timeout 899 s) · 42 req · 771 k tok | PASS 439 s · 17 req · 339 k tok |
| baseline (22:49, 2 processos) | PASS 408 s · 21 req · 429 k tok | PASS 512 s · 19 req · 411 k tok |
| `--fusion on`, nota diretiva | PASS 507 s · 16 req · 451 k tok | PASS 554 s · 15 req · 362 k tok |
| `--fusion on`, lead **minimax**, sidekick laguna (23:07, sozinho) | PASS 745 s · 13 req do lead · 921 k tok (lead+sidekick) · 2 delegações | — |
| `--fusion on`, lead minimax, sidekick laguna, registro corrigido (23:22, 2 processos) | PASS 628 s · 17 req do lead · 828 k tok · 2 delegações, 5+3 arquivos registrados | — |
| minimax sozinho (`roles.editor`, mesmo horário) | **PASS 117 s · 20 req · 372 k tok** | — |

- **Com laguna como lead, nenhuma delegação**: 0 chamadas de `sidekick` em 4 execuções, nem com a
  nota diretiva (a ferramenta estava na lista, conferido em teste). Esses braços mediram só o
  custo da nota extra.
- **Com minimax como lead (`fusion.lead`), o fluxo à la Devin aconteceu**: o lead leu os 4
  arquivos, delegou a implementação com um handoff detalhado (goal/files/acceptance/constraints),
  revisou o diff lendo os arquivos, delegou os testes num 2º item (o contexto do sidekick
  persistiu: 67 → 77 mensagens) e terminou: PASS. O lead fez 13 requisições; o sidekick gastou
  396 s/37 ferramentas e 224 s/4 ferramentas. Defeito achado ao vivo: o sidekick escreveu os
  arquivos com `cat > arquivo` no bash, que os snapshots de write/edit não veem, então os
  arquivos não foram registrados no turno do lead (`changed: null`). Corrigido: carimbo da
  árvore (tamanho, mtime, conteúdo se ≤ 256 KB) antes e depois do sidekick; teste cobre os 3
  caminhos (edit, write, bash). O sidekick também gastou ~12 chamadas brigando com o sandbox para
  escrever em `/tmp` — assunto do sandbox, não do juiz.
- **Rodada com o registro corrigido** (lead minimax, sidekick laguna): os arquivos do sidekick
  entraram no turno do lead (`changed` com 5 e 3 arquivos; `tool_counts` bash 14/read 12 e
  bash 5/read 3 — ele não usa write/edit). PASS em 628 s e 828 k tokens, contra **117 s e 372 k
  tokens do minimax sozinho** na mesma hora. Aqui a fusão custa ~5× o tempo e ~2,2× os tokens:
  o sidekick (laguna) é o modelo lento, e o lead relê tudo para revisar. O ganho de custo da
  Cognition depende de um sidekick mais barato **e** mais rápido que o lead; no pool grátis não
  existe essa assimetria. Conclusão: `--fusion` continua off; o placar (que já viu minimax
  passar 02 e 04 em 1–2 min) é o caminho que mais promete para feature.
- **Painel**: o plano de 02 (3 respostas + síntese) custou 53 s e 4 requisições antes da 1ª
  requisição do turno; a tarefa estourou o prazo (falha no teste oculto de ordenação). Uma
  execução não separa "o plano atrapalhou" de variância. A causa raiz após 2 testes falhos não
  disparou em nenhuma execução.
- **429**: nenhum em nenhuma execução (até 5 processos simultâneos no kilo sem chave).
- Tempos de parede variam muito com a carga: o mesmo baseline de 02 levou 574 s e 408 s.

**D. Hedging do classificador, 10 chamadas por braço** (`TestOjLiveHedgeLatency`, prompts da v2,
ordem alternada, candidatos laguna, minimax, cohere): **p50 8,4 s sem hedge → 1,3 s com hedge**.
Sem hedge, o 1º candidato (laguna, raciocínio) várias vezes devolveu vazio com 300 tokens de
saída e o pedido caiu para o próximo — o hedge esconde exatamente essa cauda. Concordância do
classificador LLM com os rótulos esperados: 7/10 com hedge, 6/10 sem (erra big_file, design_doc,
debug_log); o classificador de regras acerta os 10, mas foi ajustado nesses mesmos prompts.

**Placar depois de A+B** (`jazz models --scores`, `JAZZ_HOME=work/jhome-oj`): laguna
bugfix 2/2, debug_log 1/1, big_file 1/1, shell 2/2; minimax feature 1/1; linhas de 0 turnos
para os modelos que só serviram o verificador (contam requisições, não resultado).

## Medido vs. suposto

Medido: os números acima (Mac, kilo sem chave, n = 1 por célula); a perda de contagem do
`ratelimit.json` sob concorrência (36/60) e a correção (60/60); a colisão de nomes de spill/jobs
(por leitura do código; o teste cobre `model-scores.json` e `ratelimit.json`).
Suposto: os pesos do UCB (c = 0,2, penalidades 0,10/0,15/0,30, 450 s para modelo nunca medido),
o peso 0,5 por tarefa das sementes, o ε = 0,1; que o tipo de tarefa por regex é o eixo certo
para separar modelos; que o placar converge com poucas dezenas de turnos por tipo.
Não medido: o juiz no Dell (só Mac); qualquer modelo com chave (groq, gemini, openrouter); o
verificador encontrando um defeito real; o sidekick numa tarefa real; o painel de causa raiz;
o ganho do resumo de compactação hedged (aconteceu 1 vez, tarefa 10 da bateria A: laguna venceu
em 10,6 s e o minimax foi cancelado; sem par sem hedge para comparar); latência de primeiro byte separada
(o placar guarda a latência da requisição inteira e os stalls).
Contabilidade: no hedge só o uso do vencedor entra no custo do turno; os tokens que o perdedor já
gerou antes do cancelamento não são contados (a cota do provedor gasta-os mesmo assim).
O `-race` achou uma corrida no painel (soma de uso em goroutines); corrigida depois das execuções
da bateria C (o binário delas tinha a corrida; nenhuma falha observada).
Limitação conhecida: depois do TTL de "fallback grudado" (3 min) o turno volta para `a.Model`, não
para o modelo escolhido pelo juiz (mudança em `agent.go` evitada para não conflitar).
