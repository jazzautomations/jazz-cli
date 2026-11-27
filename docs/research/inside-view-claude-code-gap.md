# Visão de dentro: o harness do jazz, o loop do agente e o que falta para passar o Claude Code

17/11/2026, jazz v0.14.0 (main em 73a039c). Escrito por um modelo rodando dentro do
próprio harness, em uma sessão real que falhou duas vezes antes de dar certo.
Cada item diz se foi **confirmado** (li a linha), **observado** (vi acontecer na
sessão) ou **inferido** (deduzido de números). Nenhum código foi alterado.

Li por inteiro: o loop `Agent.Run` (`agent.go:420-1063`), compactação
(`agent.go:2700-2930`), o cabeçalho de design de 30 módulos, `weak-models.md`,
`study-synthesis.md`, `benchmarks.md` e as seções de gap do `harness-sota-gap.md`.
Não li: `nlu.go`, `brain.go`, `methods.go` (cérebro offline), `ui*.go`, providers
além dos trechos citados, e `acChain` (roteamento) além do cabeçalho.

## 1. Resposta curta: dá para passar o Claude Code?

Em três eixos o jazz já está na frente, e isso está no código, não em promessa:

- **"Pronto" é verificado, não declarado.** Guards de conclusão honesta
  (`guard_honest.go`: testes protegidos, anti-hardcode, testes rodados depois da
  última edição), checklist de qualidade (`quality.go`), entregáveis nomeados
  (`deliverables.go`), `--until`. O `weak-models.md` mediu 0 falso-sucesso em 45
  execuções com modelos grátis fracos.
- **Funciona sem modelo.** Cérebro offline com 122/122 no eval do release
  (`benchmarks.md`, 16/11), e assume quando a cadeia inteira cai (`synergy.go`).
- **Roteia entre modelos e provedores.** Juiz UCB1 por tipo de tarefa
  (`judge.go`), cadeia de fallback, fusão lead/sidekick, verificador.

Onde está atrás: economia de contexto (seção 3), robustez de subagente (seção 4),
previsibilidade do loop (seção 2) e ecossistema (IDE, plugins, tipos de agente em
arquivo). E falta a prova: o próprio `benchmarks.md` diz que nenhuma comparação
com número publicado é válida ainda. Não existe no repo uma rodada lado a lado
com o mesmo modelo nos dois harnesses. Sem ela, "melhor que o Claude Code" é
opinião. A seção 7 diz como medir.

## 2. O loop do agente (`agent.go:420-1063`)

O que ele faz bem (confirmado): reenvio silencioso de resposta vazia com espera
crescente (`:729`), chamada de tool escrita como texto vira chamada de verdade
(`:688`, `textcalls.go`), write cortado no limite de saída é continuado e
emendado (`:710`, `cutoff.go`), leituras de um mesmo lote rodam em paralelo
(`partools.go`), detecção de laço por repetição e por ciclo (`:996-1022`),
handoff para contexto limpo em vez de matar o turno (`agent_recover.go`).

### 2.1 Três saídas encerram o turno sem resposta do modelo (P0, confirmado e observado)
- Teto de tokens/custo: `:569-573` anexa "Pare agora: responda…" e dá `return`.
- Laço: `:1018-1020` anexa "Você ficou repetindo…" e dá `return`.
- Prazo: `:546-549` retorna `StopTimeout` direto.

O limite de passos faz o certo (`:575-579` + `:752-758`): uma última chamada com
ferramentas recusadas. Os outros três deixam o usuário olhando para um aviso.

Pior, o lembrete fica no histórico. Observado nesta sessão: o turno 1 morreu no
teto; no turno 2, com orçamento novo, a instrução "sem chamar mais ferramentas"
estava colada antes da mensagem nova do usuário, e eu respondi sem ferramenta
nenhuma. Logo depois o nudge de todo (`:867`) mandou continuar. Dois
lembretes contraditórios em sequência; um modelo fraco trava aí.

Conserto: (a) nesses três ramos, uma chamada final com `noToolUse` e uns 5% do
teto reservados; (b) no começo do turno seguinte, um lembrete que anula o
anterior ("o turno passado foi cortado por X; o orçamento foi renovado").

### 2.2 A cadeia de portões reabre o turno um por vez (P1, confirmado)
Quando o modelo para sem chamar tool, a ordem é: vazio (`:809`), ação fingida
(`:846`), anúncio sem ação até 2× (`:860`), todo aberto (`:867`), sem
verificação (`:874`), steer, fila, hook Stop até 3× (`:887`), e então (`:899`)
`acStopGate` (`agent_loop.go:155-199`): conclusão honesta, entregáveis,
qualidade, `--until`, verificador, review. Cada um custa uma ida e volta inteira
com o contexto todo, e o modelo só descobre o próximo depois de resolver o
anterior.

Conserto: avaliar todos os portões determinísticos na mesma parada e mandar
**um** lembrete consolidado (todos abertos + não verificou + achados de qualidade
+ entregável faltando). Uma reabertura em vez de quatro.

### 2.3 Qualquer `bash` conta como verificação (P2, confirmado)
`:984-987`: depois de editar código, qualquer chamada de bash zera `editedCode` e
marca `nudgedVerify`. Um `ls` cancela o nudge. O guard forte (`hgDoneGate`) cobre
quando o projeto tem comando de teste conhecido; sem ele, passa. Conserto: só
zerar quando `bgIsTestCmd` (`budget.go:784`) ou o comando cita o arquivo editado.

### 2.4 Laço só pega chamada idêntica (P2, confirmado)
`:996-1010`: hash de (tool, args, resultado), janela de 10, para em 6 repetições
ou ciclo de período até 4. Comando quase igual com o mesmo erro passa. O
`weak-models.md` §3.10 registrou 31 bash seguidos assim. Conserto: segundo hash
pela assinatura do erro (o `outparse.go` já extrai) e aviso em "mesmo erro 3×
com comandos diferentes".

### 2.5 Lembretes em duas línguas (P2, confirmado)
`budget.go:329` registra que modelo fraco respondeu em inglês 31–40% das vezes
quando as notas eram em inglês, e por isso o orçamento é em português. Mas
`fakeActionNudge` e `stallNudge` (`agent_guard.go:8,11`), os avisos de corte
(`:771`, `:798`) e as mensagens do `guard_honest.go` continuam em inglês.

### 2.6 `Run` é uma função de 640 linhas com ~15 flags locais (P2, confirmado)
`nudgedVerify`, `nudgedTodo`, `nudgedEmpty`, `nudgedFake`, `stallNudges`,
`emptyResends`, `textConts`, `callTruncs`, `stopHooks`, `boostOut`,
`emptyRetried`, `compactFailStep`… O `harness-sota-gap.md:52-55` cita a lição de
que o kernel que ganha é mínimo. Não precisa reescrever: extrair (1) decoradores
de entrada, (2) lista de portões de parada com interface única, (3) lista de
notas pós-tool. Isso é o que permite 2.2 e ligar/desligar portão por nível de
modelo. Os `agent_*_test.go` seguram a regressão.

## 3. Economia de contexto

### 3.1 Todo o MCP em toda requisição (P0, confirmado)
`agent.go:229` anexa `a.mcp.Specs()` inteiro para pai e filho; `prompt.go:84-94`
repete cada tool no `<tools>` (descrição cortada em 120 runas);
`subagent.go:118-125` lista os nomes de novo. Sem teto, sem carga sob demanda.
Com as ~230 tools do `rea` desta sessão, cada requisição já nasce com dezenas de
milhares de tokens (inferido: os filhos gastaram mais de 50k por requisição).

Conserto: índice de uma linha por tool mais uma tool `mcp_find` que ativa poucos
schemas por vez; teto de schemas ativos; subagente recebe só o que o pai listar.

### 3.2 O teto do turno conta leitura de cache pelo valor cheio (P1, confirmado)
`provider_anthropic.go:319` soma `InputTokens + CacheRead + CacheCreate` em
`Usage.In`, e `security.go:57,78` compara `In + Out` com 4.000.000. Um prefixo
grande e cacheado, que custa pouco, estoura o teto de tokens bem antes do teto
de custo (US$ 5). Conserto: o teto de tokens olhar não-cacheado + saída, ou pesar
leitura de cache; o teto de custo já cobre o resto.

### 3.3 Resultados antigos de tool ficam inteiros até 80% da janela (P1, confirmado)
A compactação dispara em `min(janela − saída − 16k, 80%)` (`agent.go:325-336`) e
é boa: resumo estruturado, últimos 20k literais, âncoras determinísticas com as
palavras do usuário e os arquivos tocados (`compact_harden.go`), digest sem
modelo como último recurso. Mas a visão em camadas que troca resultado antigo
por um stub (`ctxview.go`) está desligada por padrão (`outshape.go:17`). Até a
compactação, toda requisição reenvia tudo. Já existe o replay para medir
(`evalctx.go`); falta rodar e ligar o modo `edited`, aplicando o corte em degraus
para não invalidar o cache a cada passo.

Depois de compactar, só os nomes dos arquivos voltam. O Claude Code reinjeta o
conteúdo dos arquivos recentes lido do disco (`harness-sota-gap.md:60-62`).

### 3.4 `script` e `fact` são dois zoológicos dentro de um schema (P2, observado)
No meu prompt, `script` lista ~90 helpers e `fact` ~55 checagens na descrição,
com sobreposição entre si e com `bash` (três jeitos de fazer grep, dois de ver
git). Para modelo fraco é ruído na escolha da tool.

## 4. Subagentes

Nesta sessão: 3 filhos de pesquisa, 3,9M tokens, três retornos "ended (step_limit)
without a report". Causas, todas confirmadas no código:

- **Sem resposta final no teto** (2.1). `subagent.go:912-914` devolve a frase
  vazia. Inferido: pararam pelo teto, porque a parte de cada um (`saSplitCap`,
  `subagent.go:454`) dava ~1,33M e os três terminaram entre 1,27M e 1,33M.
- **Sem aviso de orçamento.** `budget.go:422-427`: `bgTurnState` devolve `nil`
  para subagente, então as notas de 70% e 100% (`budget.go:384-400`) nunca
  chegam nele. São 25 passos fixos (`subagent.go:96`), 30 para worker (`:781-783`).
- **MCP inteiro** (3.1), mesmo sendo "pesquisa só-leitura".
- **Ponteiro inútil.** O rodapé diz "its partial work is in session X"
  (`subagent.go:884-886`), mas o modelo é bloqueado de ler sessões (observado).

Conserto, do mais barato ao mais caro:
1. aviso aos 70% dos passos e aos 70% dos tokens: "comece a escrever o relatório";
2. resposta final forçada no teto (2.1);
3. se ainda vier vazio, devolver `acDigestHistory` do filho (`agent.go:2851` já
   faz esse resumo determinístico para a compactação);
4. MCP só quando o pai passar a lista;
5. tipos de subagente em arquivo (prompt, tools, modelo), que o Claude Code tem e
   aqui não achei (`rg` por `agents/*.md|loadAgents` vazio).

O que já é bom: sessão própria por filho, shell próprio, dono único por arquivo
entre workers, teto dividido (`subagent.go:1-29`).

## 5. Modelo pequeno

Já existe muita mecânica feita para ele (confirmado): reparo de argumentos
(`argfix.go`), tool call em texto e modo sem function calling (`config.go:54`,
`textcalls.go`), continuação de write cortado (`cutoff.go`), reenvio de resposta
vazia, nudges de ação fingida e de anúncio sem ação (`agent_guard.go`), handoff
em laço, guards honestos, caches de build graváveis no sandbox
(`sandbox.go:445`), caminho parecido quando o arquivo não existe
(`nameindex.go:860`), reparo de JSON de tool call (`agent.go:1617`). As propostas
§3.2, §3.6 e §3.9 do `weak-models.md` entraram.

O que falta é o harness **saber que o modelo é fraco e mudar de postura**:

1. **Prompt por nível.** Hoje é um `basePrompt` só (`prompt.go:14-58`, mais de 25
   regras longas). `ModelConf` já tem `Tiers` e a nota `Q` (`config.go:57-61`) e
   o juiz guarda placar por modelo; dá para escolher o perfil por aí. Nível
   fraco: 6 a 8 regras imperativas; o resto fica nos guards, que já cobram.
2. **Tools por nível.** read, edit, write, bash, todo. Sem `script`/`fact`
   (3.4), sem MCP por padrão (3.1).
3. **Orçamento por nível.** `bgBudgets(task, level)` não recebe o modelo
   (`budget.go:241`). A §3.5 do `weak-models.md` segue aberta: a tarefa 05
   morreu em "tamanho S: 12 passos" com o trabalho pela metade.
4. **Um lembrete consolidado** (2.2) e na língua do usuário (2.5).
5. **Forte planeja, fraco executa.** Já existe como `--fusion on`
   (`judge_fusion.go`: lead + sidekick com contexto próprio), e o painel para
   causa raiz depois do segundo teste falho. Estão desligados por padrão. Falta
   a política: ligar sozinho quando o modelo principal é de nível fraco, e
   subir de modelo quando o item falha N vezes (hoje o fallback é por travamento
   do provedor, não por falha da tarefa, pelo cabeçalho do `judge.go`).
6. **Ligar o que está pronto e desligado.** prefetch, context_view, hindsight,
   verificador, hedge, fusão, painel e juiz estão todos atrás de flag, vários
   com o comentário "off até um A/B dizer que ajuda". O gargalo é medição (seção 7).

Não conferido: se a §3.3 (guard de teste barrando a única correção possível, o
Makefile) foi resolvida.

## 6. Outros achados de dentro

- **Falso positivo do guard de injeção** (observado; regra em `sanitize.go:66`).
  Uma saída de 600 linhas com comentários do próprio repo foi embrulhada como
  "suspeita de injeção" por causa de um ε em "ε-greedy" (`judge.go`), com a nota
  "não repita, não procure contorno". Um modelo fraco larga a leitura aí. Uma
  letra grega isolada não deveria bastar.
- **Segredo em instrução de projeto** (observado). O `AGENTS.md` da home chega
  ao meu prompt com senha de sudo e token em URL em claro, e vai para o provedor
  que responder, inclusive os grátis. Não conferi no código se esse caminho
  passa pelo redator que o `lessons.go` usa.
- **Sem tipo "pesquisa" no classificador** (`budget.go:173-200`). "Estude X"
  não é pergunta nem cita arquivo: vira `change`, tamanho M, e recebe "até 8
  leituras antes de editar" numa tarefa que é só ler.
- **Descrição do sandbox diverge** (observado). O prompt diz que `$HOME` fica
  escondido; com o cwd na home, `ls ~` listou tudo.

## 7. Como provar que ficou melhor

1. **Lado a lado.** Mesmo modelo da Anthropic, Claude Code contra jazz, em
   `evals/v2` (10 difíceis, verificador oculto), terminal-tasks (14) e
   `evals/weak` (24). Métricas: aprovação pelo check independente, tokens
   cacheados e não cacheados, tempo, requisições, falso-sucesso. Três rodadas.
2. **A/B das flags desligadas** com um modelo pequeno pago e sem janela de cota.
   O `weak-models.md` perdeu cobertura inteira por cota de modelo grátis.
3. **Casos de regressão desta sessão:** fan-out de 3 subagentes com servidor MCP
   de 200+ tools; turno cortado no teto tem que terminar com resposta; turno
   seguinte a um corte tem que poder usar ferramenta.
4. **Portão online no release**, como o offline já tem (122/122, replay 35/35).

## 8. Ordem de ataque

| # | o quê | onde | tamanho |
|---|---|---|---|
| 1 | resposta final em teto/laço/prazo + anular lembrete velho | `agent.go:546,569,1020`, `subagent.go:912` | P |
| 2 | MCP sob demanda, com teto | `agent.go:229`, `prompt.go:84`, `subagent.go:118` | M |
| 3 | subagente: aviso aos 70%, digest de fallback | `budget.go:422`, `subagent.go:96,912` | P |
| 4 | teto de tokens sem contar cache cheio | `security.go:78`, `provider_anthropic.go:319` | P |
| 5 | portões de parada em um lembrete só | `agent.go:809-899`, `agent_loop.go:155` | M |
| 6 | perfil por nível: prompt, tools, orçamento | `prompt.go:14`, `budget.go:241`, `config.go:50` | M |
| 7 | ligar context_view após replay; reinjetar arquivos pós-compactação | `outshape.go:17`, `compact_harden.go` | P-M |
| 8 | lado a lado + A/B com modelo pago pequeno | `evals/` | M |
| 9 | tipo "pesquisa"; lembretes na língua do usuário; nudge de verificação só com teste; laço por assinatura de erro | `budget.go:173`, `agent_guard.go:8`, `agent.go:986,996` | P cada |
| 10 | homóglifo isolado não dispara | `sanitize.go:66` | P |
| 11 | quebrar `Run` em listas de portões | `agent.go:420` | M-G |
| 12 | tipos de agente em arquivo, 4 eventos de hook, diagnóstico pós-edição | `subagent.go`, `hooks.go:29` | M |

Os itens 1 a 4 são pequenos e teriam evitado as duas falhas desta sessão.

## 9. Correções e limites

A primeira versão deste arquivo dizia que não havia divisão planejador/executor
(existe, `--fusion`, desligada) e deixava as §3.2/3.6/3.9 do `weak-models.md`
como não conferidas (entraram).

Não rodei build nem testes, não medi o tamanho real do bloco de MCP, não li o
cérebro offline, a UI nem o roteador. As afirmações sobre o Claude Code vêm do
que eu sei do produto e do `harness-sota-gap.md`, não de teste lado a lado.
Hooks faltantes (SubagentStop, PreCompact, Notification, SessionEnd) vêm de
`hooks.go:29-33`. Não conferido: notebook, IDE, plugins, stream-json no
headless, OAuth de MCP.

## 10. Economia de tokens e eficiência (medido)

Fonte: os `*.metrics.json` de `~/jazz-eval-results` (eval v2, 10–11/11/2026),
49 execuções com tokens, modelos grátis ou baratos (gpt-oss-120b no groq,
north-mini-code, nemotron-3.5-lightning), sem MCP. Agregado com um script
python de uma vez; os números abaixo saem direto dele.

| medida | valor |
|---|---|
| tokens de entrada / saída | 14.933.407 / 331.378 (45 para 1) |
| entrada lida de cache | 27% (várias execuções com 0%) |
| requisições | 1.147, das quais 218 com erro |
| entrada por requisição | média geral 16.075; por execução: mediana 7.256, menor 4.424, maior 80.241 |
| tool calls por requisição | 1,03 |
| tool calls com erro | 138 de 955 (14%) |
| requisições por execução | mediana 17; p90 46 |
| compactações | 0 em 49 execuções |
| tempo | 64% esperando o modelo, 1% em ferramentas, 36% no resto |

### 10.1 O que os números dizem

1. **Economia de tokens é economia de entrada.** A saída é 2% do total.
2. **O prompt base é enxuto.** O piso é 4,4k tokens por requisição
   (`basePrompt` tem 6,9 KB, mais as tools nativas). O problema não está aí.
3. **O custo é quadrático e nada segura.** As 12 execuções com 30 ou mais
   requisições somam 66% de toda a entrada (9,85M de 14,93M). Nenhuma chegou a
   compactar: o gatilho é 80% da janela (`agent.go:325-336`) e, até lá, todo
   resultado antigo é reenviado inteiro.
4. **O pior caso mostra o mecanismo.** `10-log-stacktrace` gastou 2,73M tokens
   com 80k por requisição e 0% de cache. No log da execução: o modelo leu um
   arquivo de log de 2.931 linhas em 5 pedaços de ~650 linhas (`read` devolve até
   50 KB, `truncate.go:18`), e os 5 pedaços viajaram em cada uma das ~30
   requisições seguintes. Um `rg` pelo traceback custaria uma fração disso.
5. **Ninguém agrupa chamadas.** 1,03 tool por requisição. O `partools.go` roda
   leituras do mesmo lote em paralelo, mas quase nunca há lote. O `basePrompt`
   não pede lote de chamadas independentes (só "vários edits num arquivo" e o
   todo), e `parallel_tool_calls` só é enviado no provider do codex
   (`provider_codex.go:130`).
6. **14% das chamadas falham**, e cada falha é uma ida e volta inteira. No mesmo
   log, nos primeiros 40 passos: `read` sem `path` duas vezes, `edit` com JSON
   inválido, `edit` sem `path` duas vezes. O re-pedido silencioso de chamada
   vazia existe, mas vale uma vez por turno (`agent.go:531`).
7. **Cache depende do provedor.** No caminho Anthropic há três marcas (system,
   última tool, último bloco: `provider_anthropic.go:111,219,226`). No caminho
   OpenAI-compatível, só OpenRouter para Anthropic/Gemini (`provider_openai.go:87`);
   o resto fica por conta do cache automático de cada provedor.
8. **Ferramenta não é gargalo de tempo; requisição e cota são.** 1% do tempo em
   ferramentas. Os logs têm 113 linhas de 429 e esperas de até 48 s; os 36% de
   "resto" são, na maior parte, essa espera (inferido: o campo não separa).

Nesta sessão, com o servidor `rea` (~230 tools), o piso por requisição sai de
4,4k para dezenas de milhares de tokens (estimado, seção 3.1). O `AGENTS.md` da
home tem 10,6 KB e entra em toda requisição, com notas de DAW e de iPhone que
quase nenhuma tarefa usa.

### 10.2 O que fazer, por retorno

| # | o quê | ataca | onde |
|---|---|---|---|
| 1 | stub de resultado antigo ligado por padrão, em degraus (25%, 50% da janela) para não quebrar o cache a cada passo | 3, 4 | `ctxview.go`, `outshape.go:17`; medir antes com o replay do `evalctx.go` |
| 2 | MCP sob demanda | piso ×10 em sessão com MCP grande | `agent.go:229`, `prompt.go:84` |
| 3 | leitura dimensionada pela janela, como o bash já é (8% da janela, 6–30 KB); log ou dado grande devolve começo + resumo do `outparse.go` + dica de `rg` | 4 | `truncate.go:18`, `tools.go:963` |
| 4 | lote de chamadas: regra no prompt, `parallel_tool_calls` onde o provedor aceita, `read` com lista de caminhos | 5 | `prompt.go:14`, `provider_openai.go`, `tools.go:145` |
| 5 | ligar o prefetch por grep, que corta os primeiros passos de "achar" | 5 | `prefetch.go` (off) |
| 6 | re-pedido silencioso de chamada sem argumento mais de uma vez por turno; portões de parada em um lembrete só | 6 | `agent.go:531,700`, seção 2.2 |
| 7 | teto de tokens sem contar leitura de cache pelo valor cheio | turno morto cedo | `security.go:78` |
| 8 | `AGENTS.md` grande: aviso de tamanho e seções por tópico como steering `auto`, lidas só quando a tarefa bate | piso | `steering.go` (o mecanismo existe) |
| 9 | no 429, trocar de modelo em vez de esperar, quando houver cadeia | 8 | roteador; nas execuções do eval o modelo estava fixado, então não sei se isso pesa fora dele |
| 10 | métrica de produto: tokens por tarefa verificada, entrada por requisição, % de cache, tools por requisição, % de erro | tudo | o `--metrics-out` já grava; falta agregar e pôr no portão de release |

Subagente só economiza quando o prefixo dele é pequeno. Com o MCP inteiro
(`agent.go:229`), cada passo de um filho custa o mesmo piso do pai, e três
filhos triplicam esse piso.

Não medido: quanto cada item economiza. Os itens 1, 3 e 4 dá para estimar com o
replay das sessões gravadas, que eu não consigo ler de dentro do harness.
