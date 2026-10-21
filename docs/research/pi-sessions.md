# Sessões, persistência e eventos do pi (pi-mono) — o que o jazz adotou

Fonte lida em 2026-10-20 (branch `main` de github.com/badlogic/pi-mono, MIT, Mario Zechner):
`packages/coding-agent/src/core/session-manager.ts`, `src/core/agent-session.ts`,
`src/core/extensions/types.ts`, `packages/agent/src/agent-loop.ts`, `docs/session-format.md`,
`docs/sessions.md`. Nenhum código foi copiado; o formato do jazz é próprio, inspirado no modelo do pi.

## O que o pi faz

**Arquivo de sessão.** JSONL em `~/.pi/agent/sessions/--<cwd>--/<timestamp>_<uuid>.jsonl`. Primeira
linha é o cabeçalho (`type:"session"`, `version` 3, `id`, `cwd`, `parentSession` quando veio de um
fork). Toda outra entrada tem `id` (8 hex), `parentId` (null na raiz) e `timestamp` ISO. Tipos:
`message` (user/assistant/toolResult/system/custom), `model_change`, `thinking_level_change`, `usage`,
`compaction` (`summary`, `firstKeptEntryId`, `tokensBefore`, `usage`), `context_edit`,
`branch_summary`, `custom`, `custom_message`, `label`, `session_info`. Versões 1 (linear) e 2 são
migradas na leitura: a v1 ganha ids e cada entrada vira filha da anterior.

**Árvore.** A "folha" é a posição atual (a última entrada ao carregar). `branch(id)` move a folha (só
em memória: o próximo append vira filho dela); `resetLeaf()` permite nova raiz; `branchWithSummary`
grava um `branch_summary` com o resumo (feito por LLM) do ramo abandonado. O contexto do modelo é o
caminho folha→raiz, respeitando a compactação mais recente do caminho e os `context_edit`.
Comandos: `/tree` (navega dentro do arquivo), `/fork` (sessão nova a partir de uma mensagem),
`/clone` (copia o ramo ativo); CLI `--continue` (mais recente da pasta), `--resume` (seletor),
`--session <caminho|id>`, `--fork`, `--no-session`, `--session-id`.

**Quando grava.** `appendMessage` roda no `message_end` de cada mensagem: a do usuário antes da
requisição sair, a do assistente quando o stream termina (inclusive `stopReason` `aborted`/`error`, com
o que chegou), e cada `toolResult` assim que chega. O arquivo só é criado quando aparece a primeira
mensagem de usuário/assistente; daí em diante é `appendFileSync` por entrada.

**`stopReason: "length"`.** No `agent-loop.ts`, se a resposta cortada traz chamadas de ferramenta,
todas falham sem executar (`failToolCallsFromTruncatedMessage`), porque os argumentos podem estar
cortados. Não achei continuação automática de texto cortado.

**Retentativa.** Configurável (`enabled`, `maxRetries`, atraso exponencial `retryDelayMs`), com eventos
`auto_retry_start`/`auto_retry_end`. A tentativa que falhou fica no histórico cru, escondida do modelo
por um `context_edit` (`_omitRecoveryAttempt`); o contador de tentativas vive em memória (não sobrevive a
reiniciar o processo). Estouro de contexto dispara compactação e nova tentativa uma vez.

**Por mensagem.** A mensagem do assistente guarda `api`, `provider`, `model`, `usage` (input, output,
cacheRead, cacheWrite, totalTokens, custo detalhado), `stopReason`, `timestamp`, `thinkingLevel`. Uso
que não é mensagem (aquecer cache, resumo) vai numa entrada `usage`.

**Eventos de extensão.** `session_start`, `session_shutdown`, `session_before_switch/fork/tree/compact`,
`session_compact`, `session_compact_failed`, `session_tree`, `session_info_changed`, `agent_start`,
`agent_end`, `agent_before_settle`, `agent_settled`, `before_agent_start`, `turn_start`, `turn_end`,
`message_start`, `message_update`, `message_end`, `tool_call`, `tool_execution_start/update/end`,
`tool_result`, `before_provider_request`, `before_provider_headers`, `after_provider_response`,
`provider_stream_event`, `context`, `model_select`, `thinking_level_select`, `input`, `user_bash`,
`project_trust`, `resources_discover`, `mcp_servers_change`, `ui_prompt_start/end`. São ganchos
em processo (TypeScript) que podem observar e alterar.

## O que o jazz adotou

| Comportamento do pi | No jazz | Situação |
|---|---|---|
| `id` + `parentId` em toda entrada, árvore num arquivo só | `id` (8 hex) e `parentId` em toda entrada, `meta.v = 2` | igual |
| Migração v1 → árvore na leitura | entradas sem id ganham `L<linha>` e a linha anterior como pai; o resultado é o mesmo de antes (testado com rewind, compactação e linha cortada) | igual (ids sintéticos estáveis, o arquivo não é reescrito) |
| Folha = última entrada; contexto = caminho folha→raiz | `sessPath` + `sessReplay` | igual |
| `/tree` e continuar de qualquer ponto | `/tree` lista (● caminho ativo, ← atual); `/tree <id>` e `-r <sessão>@<entrada>` continuam dali | igual; o jazz grava uma entrada `branch` na hora (no pi a escolha só fica gravada no próximo append) |
| Voltar atrás não apaga o ramo | `/rewind` move a folha e o ramo antigo fica no arquivo; a entrada `rewind` continua sendo escrita para jazz antigos lerem igual | igual |
| `/fork` com sessão-mãe no cabeçalho | `meta.parent` (id da sessão-mãe) | igual (id em vez de caminho) |
| `--continue`, `--session <id>` | `-c`, `-r <id>` (prefixo serve), `--resume-last-in <pasta>` | igual + extra para runners de avaliação |
| Gravar antes de enviar; resultado de ferramenta assim que chega | mensagem do usuário antes da requisição; resposta antes de qualquer ferramenta; `tool_start` antes de rodar e `tool_result` no retorno, antes das notas do harness | igual + `tool_start` (o pi não tem) e recuperação do resultado na leitura |
| Assistente abortado gravado com o que chegou | texto + chamadas de ferramenta parciais viram texto (`[tool call … cut while being written]`) | igual para Ctrl+C/prazo; ver "não igual" para erro de stream |
| `length` com ferramentas ⇒ não executa | já era assim (`TOOL_NOT_EXECUTED`); o jazz ainda pede continuação do texto cortado até 2× | igual + continuação |
| model/provider/usage/stopReason por mensagem | `model`, `provider`, `usage` (com custo quando há), `stop`, `latency_ms`, `ts` na entrada do assistente | igual (sem `api` e `thinkingLevel`) |
| Eventos do ciclo | fluxo `<id>.events.jsonl` (`turn.*`, `request.*`, `tool.*`, `checkpoint`, `guard`, `compact`, `recover`, `run.end`…) | equivalente para observar; não é API de extensão |

## O que não foi adotado (e por quê)

- **`branch_summary` por LLM** ao trocar de ramo: custa uma chamada de modelo e o jazz roda com
  modelos grátis lentos; o ramo abandonado fica no arquivo e aparece no `/tree`. Pode vir depois.
- **`context_edit`, `label`, `session_info` (nome), `custom`/`custom_message`, `thinking_level_change`,
  entrada `usage`**: o jazz não tem extensões em processo (tem hooks no formato do Claude Code); o uso
  que não é mensagem vai para o fluxo de eventos.
- **Tentativa falha escondida por `context_edit`**: o jazz não põe a tentativa falha no histórico; ela
  fica no fluxo de eventos (`request.end` com `error`, `status`, `payload`). O contador de retentativas
  também é de memória.
- **Prompt como mensagens `system` versionadas**: o jazz guarda o prompt de sistema inteiro no `meta`
  e nunca o muda dentro da sessão (regra do cache); o que muda vai em `<system-reminder>`.
- **Arquivo criado só na primeira mensagem**: o jazz cria no início da sessão (o fluxo de eventos e a
  auditoria precisam do id desde o começo).
- **API de extensões** (os ~40 eventos com poder de alterar): fora de escopo; o fluxo de eventos é
  só leitura e foi desenhado para exportar (OTLP) depois.

## O que o jazz faz além

Nota de passagem e checkpoint em todo fim anormal, folga do prazo no `-p`, registro `end` com a
situação (concluída/incompleta/negada) e retomada automática pela nota, `--metrics-out`, `jazz trace`,
`jazz sessions`, log de auditoria com cadeia de hash e `jazz changes` por arquivo
([observability.md](../observability.md), [governance.md](../governance.md)).

## Ainda não igual

- **Erro no meio do stream (não cancelamento)**: quando o provedor cai depois de já ter mandado
  texto, `chatWithFallback` devolve só o erro e o parcial se perde (o pi grava a mensagem com
  `stopReason: error`). Está na área do ramo `cutoff-continue` (provedores/`agent.go`); o contrato
  pedido é: em erro de stream, devolver a `ChatResponse` parcial (texto e `ToolCalls` acumuladas) junto
  com o erro, e o laço grava como no cancelamento (`obPartialText`).
- **Chamadas de ferramenta parciais no cancelamento**: o laço já grava `resp.ToolCalls` parciais, mas
  o provedor OpenAI só devolve o texto num erro (os acumuladores de argumentos são descartados);
  falta o mesmo ajuste no `provider_openai.go`.
