# Observabilidade e trabalho que não se perde

Três arquivos por sessão, todos em `~/.local/share/jazz-cli/sessions/<projeto>/` (ou
`$JAZZ_HOME/data/sessions/...`), modo 0600, só acrescentados (append-only):

| Arquivo | O que é | Quem lê |
|---|---|---|
| `<id>.jsonl` | a sessão: árvore de entradas (mensagens, compactações, rewinds, ramos) + registros write-ahead das ferramentas + registro de fim | `-c`, `-r`, `--resume-last-in`, `/tree`, `/resume` |
| `<id>.events.jsonl` | fluxo de eventos: requisições, ferramentas, guardas, checkpoints, orçamento, skills, erros | `jazz trace`, `jazz sessions`, `--metrics-out` |
| `<id>.handoff.md` | nota de passagem de uma execução que não terminou | você, e o `-r` (a nota vai na próxima mensagem) |

O log de auditoria (global, com cadeia de hash) é outro arquivo: veja [governance.md](governance.md).

## Nenhum trabalho perdido

1. **Persistir antes de enviar** (regra do pi): a mensagem do usuário é gravada antes da requisição;
   a resposta do modelo (texto, raciocínio e chamadas de ferramenta, mesmo cortadas) é gravada antes de
   qualquer ferramenta rodar.
2. **Write-ahead das ferramentas**: `tool_start` (id, nome, sha dos argumentos, passo) antes de rodar;
   `tool_result` (resultado até 64 KB, redigido) no instante em que a ferramenta volta, antes de
   qualquer nota do harness. Se o jazz morre entre a ferramenta e o histórico, o `-r` recupera o
   resultado do write-ahead em vez de `TOOL_OUTCOME_UNKNOWN`.
3. **Resposta cortada no meio do stream** (prazo, Ctrl+C): o texto e as chamadas de ferramenta que
   já chegaram ficam no histórico como texto (`[tool call write cut while being written...]`), nunca
   como chamada executável.
4. **Fim anormal** (`--timeout`, erro de provedor, 429 esgotado, Ctrl+C/SIGTERM, crash, disjuntor
   de bloqueios, limite de orçamento/passos, laço, `--until`/qualidade falhando): checkpoint do
   shadow-git com rastreio, nota de passagem (`<id>.handoff.md`: tarefa, arquivos mudados, últimas
   ferramentas e como terminaram, última verificação, todos abertos, próximo passo), registro `end`
   na sessão e fsync de sessão, eventos e auditoria. O id da sessão e o comando de retomada saem no
   stderr e no resultado JSON (`outcome`, `checkpoint`, `handoff`, `resume`).
5. **Retomar**: `jazz -p -r <id>` (ou `-c`, ou `--resume-last-in <pasta>`) recarrega a conversa
   inteira e põe a nota de passagem na frente da próxima mensagem do usuário (nunca no prompt de
   sistema: o cache continua valendo). Sem prompt, o jazz continua sozinho de onde parou.
   `-r <id>@<entrada>` retoma num ponto qualquer da árvore (novo ramo).
6. **Prazo com folga** (`-p --timeout`): a `timeout-grace` (padrão 10% do prazo, entre 5 s e 60 s;
   `--timeout-grace D`) antes do fim, nenhuma requisição nova começa; a ferramenta em curso termina,
   as seguintes do mesmo lote não começam (`TOOL_NOT_STARTED`), e a execução termina como
   incompleta (saída 124) com checkpoint e nota. Uma requisição que já estava em curso continua até o
   prazo duro; se ele chegar, o que já tinha sido transmitido fica salvo (item 3).

Códigos de saída do `-p`: `0` concluída · `5` negada · qualquer outro = incompleta (retomável).
O detalhe continua no código (124 prazo, 1 erro, 3 passos, 4 laço, 6 cortada, 130 interrompida…).

Para runners de avaliação (pass@2): a segunda tentativa roda `jazz -p --resume-last-in <pasta-da-tarefa>`
e continua a primeira em vez de recomeçar; `--metrics-out arq.json` grava os totais sem precisar ler logs.

## Esquema dos eventos (`<id>.events.jsonl`, `v` = 1)

Cada linha é um objeto JSON. Campos sempre presentes: `v` (versão do esquema), `seq` (sequência na
sessão, continua entre execuções), `ts` (UTC, ms), `sid` (sessão), `kind`. Opcionais em todos:
`step` (requisição do turno), `sub` (true = subagente), `sub_id` (qual subagente: `sub1`, `sub2`…; os
filhos de um fan-out paralelo escrevem no mesmo arquivo do pai, intercalados). Um `kind` novo pode aparecer; campos novos
podem ser acrescentados; mudar o sentido de um campo sobe `v`. Pensado para virar OTLP depois:
`sid` = trace, `turn.*` / `request.*` / `tool.*` = spans (início/fim pareados por `step` e `id`),
o resto = eventos de span.

| kind | campos |
|---|---|
| `session.start` | `source` (startup/resume/fork), `model`, `cwd`, `version`, `pid`, `os`, `arch`, `leaf` |
| `turn.start` | `prompt_sha` (sha256 do pedido), `input_bytes`, `model`, `msgs` (tamanho do histórico) |
| `turn.end` | `stop` (StopReason), `steps`, `tools`, `dur_ms`, `in`, `out`, `cache_read`, `cache_write`, `cost`, `ctx_tokens` |
| `request.start` | `model`, `provider`, `cand` (índice na cadeia de fallback), `attempt`, `msgs`, `est_tokens` |
| `request.end` | `model`, `provider`, `cand`, `attempt`, `latency_ms`, `in`, `out`, `cache_read`, `cache_write`, `cost`, `finish` (finish_reason), `tool_calls`, `text_bytes`; com erro: `error`, `status`, `daily`, `payload` (300 chars, redigido), `retry_after_ms` |
| `tool.start` | `id`, `name`, `args_sha`, `args_bytes`, `lane` (`par` = já rodando em paralelo com as vizinhas, `seq` = vai rodar em ordem; `partools.go`) |
| `tool.end` | `id`, `name`, `dur_ms`, `bytes`, `is_err`, `ran`, `lane` (`par` = o resultado veio da execução paralela; `seq` = rodou em ordem, inclusive quando a paralela foi descartada), `denied`?, `blocked`?, `truncated`? (saída longa foi para arquivo) |
| `guard` (decisão) | `guard` (permission/hook/honest/budget/cancel), `tool`, `target`, `reason`, `outcome` (refused/denied) |
| `guard` (diagnóstico) | `src` (guard/injection/supply/smart/blocked) + os campos do módulo |
| `approval` | `tool`, `target`, `reason`, `answer` (allow/allow_always/deny) |
| `checkpoint` | `tree`, `commit`, `reason` (turn-start/step/abnormal-end:<status>), `changed`, `error`? |
| `deadline.soft` | `why` |
| `run.end` | `status`, `outcome` (completed/incomplete/denied), `exit`, `dur_ms`, `in`, `out`, `cache_read`, `cost`, `model`, `checkpoint`?, `handoff`?, `error`? |
| `budget` | `src`, tamanho/tipo da tarefa, passos, exploração, recusas de reescrita/releitura |
| `skill` | `at` (turn/tool), `pick`, `cands`, `phase` |
| `compact` | `cut`, `msgs`, `model` |
| `recover` | `why`, `mode`, `discard`, `reverted`, `files`, `step`, `note_bytes` |
| `provider` | `src` (stall/netwait/sticky) e detalhes (ref, prazo do primeiro byte, volta) |
| `subagent.start` / `subagent.end` | `sub_id`, `work` (worker que edita), `files`, `session` (sessão do filho) / `sub_id`, `stop`, `dur_ms`, `in`, `out`, `cost`, `is_err` |
| `repair`, `nudge`, `quality`, `until`, `review`, `plan_cache`, `fastpath` | diagnóstico do módulo (`src` + campos) |

Os eventos diagnósticos são cópias pequenas do log de debug (`dbg`), feitas sempre, mesmo sem
`--debug` (corpo de requisição e SSE continuam só no log de debug).

## Sessão em árvore (`<id>.jsonl`, formato v2)

Toda entrada tem `id` (8 hex) e `parentId` (vazio na raiz), além de `ts` (ms). A posição atual é a
última entrada; `/rewind`, `/tree <id>` e `-r <id>@<entrada>` movem a posição e a próxima mensagem
abre um ramo — os ramos abandonados ficam no arquivo. Entradas: `meta` (`v`:2), `msg` (assistente com
`model`, `provider`, `usage`, `stop`, `latency_ms`), `compact`, `model`, `rewind`, `goal`, `recover`,
`branch` (`target`), `tool_start`, `tool_result`, `end` (`status`, `outcome`, `checkpoint`,
`handoff`, `note`). Cada subagente (`task`) tem a sua sessão em `<id>.sub/<id>.<sub>-<rand>.jsonl`: o `meta` traz `parent`
(a sessão do pai), `parent_entry` (a entrada do pai de onde o filho saiu) e `sub`; termina com `end`. Não
é um `.jsonl` da pasta, então `-c`, `/resume` e `jazz sessions` não a listam; é onde fica o trabalho
parcial de um filho cancelado. Arquivos antigos (sem `id`) são migrados na leitura: cada entrada vira filha da
linha anterior, com o mesmo resultado de antes. Detalhes e comparação com o pi em
[research/pi-sessions.md](research/pi-sessions.md).

## Comandos

- `jazz trace [id] [--json]`: linha do tempo em português com tempos relativos, duração de cada
  requisição e ferramenta, bloqueios, checkpoints, e totais (requisições, tokens, custo, ferramentas,
  tempo ativo). `--json` despeja o arquivo de eventos cru.
- `jazz sessions [--json] [-n N | --all]`: sessões desta pasta com situação (concluída/incompleta/
  negada/aberta), status, tokens, custo, tempo ativo e o pedido.
- `jazz -p ... --metrics-out arq.json`: os mesmos totais (`obSummary`) de uma execução headless.
- `jazz changes <arquivo>` (ou `jazz blame`): quem mudou o arquivo (veja governance.md).
- `/tree` (interativo): a árvore da sessão; `/tree <id>` continua daquele ponto.

## Exportação OTLP (opcional, desligada por padrão)

O mesmo fluxo de eventos pode ser enviado a um coletor OpenTelemetry por OTLP/HTTP **JSON** (sem
protobuf, sem dependência nova). Código em `otlp.go`.

```
jazz -p "..." --otlp-endpoint http://localhost:4318
```

ou em `config.json`:

```json
{ "otlp": { "endpoint": "http://localhost:4318",
            "headers": { "Authorization": "Bearer SEU_TOKEN" },
            "metrics": true, "batch_size": 64, "flush_ms": 2000, "queue_size": 4096 } }
```

O flag vence o config. `JAZZ_OTLP_HEADERS="K=V,K2=V2"` acrescenta cabeçalhos sem pôr o token no
arquivo. O endpoint é a base (`/v1/traces` e `/v1/metrics` são acrescentados).

**Traces** (`POST <endpoint>/v1/traces`): a sessão é o trace (`traceId` = sha256 do `sid`).

| span | de onde vem | atributos principais |
|---|---|---|
| `session` | `session.start` até `run.end` / fim do processo | modelo, `source`, resultado da execução |
| `turn` | `turn.start`/`turn.end` | tokens, custo, passos, `stop` |
| `request <modelo>` (client) | `request.end` | `gen_ai.request.model`, `gen_ai.system`, `gen_ai.usage.input_tokens/output_tokens`, `jazz.tokens.cache_read/cache_write`, `jazz.latency_ms`, `gen_ai.response.finish_reasons`, `jazz.cost`; erro vira status 2 |
| `tool <nome>` | `tool.start`/`tool.end` | `jazz.tool.name/id/args_sha/args_bytes/bytes/truncated/is_error/denied/blocked` |
| `subagent` | turno com `sub:true`, filho da ferramenta que o lançou | igual a `turn`; seus requests e tools ficam dentro dele |

Decisões de guarda (`guard`), aprovações, checkpoints, compactações, recuperações e avisos de prazo
viram **eventos de span** (`guard.permission`, `guard.honest`, `approval`...) na ferramenta em curso com
o mesmo nome, ou no turno. Só vai o que já está no fluxo de eventos (hashes e tamanhos, nunca o
texto do pedido nem o conteúdo de arquivos); todo atributo texto passa por `redactSecrets`.

**Métricas** (`POST <endpoint>/v1/metrics`, temporalidade delta, desligue com `"metrics": false`):
`jazz.tokens` (atributos `type` = input/output/cache_read/cache_write, `model`), `jazz.cost` (USD),
`jazz.requests` (`model`, `status`), `jazz.tool.calls` (`name`, `status`) e os histogramas
`jazz.request.duration` e `jazz.tool.duration` (ms; limites 50, 100, 250, 500, 1000, 2500, 5000, 10000,
30000, 60000, 120000).

**Nunca atrapalha o agente**: `emit` só faz um envio não bloqueante a um canal (fila de
`queue_size`); um worker junta em lotes de `batch_size` spans (ou a cada `flush_ms`) e posta com prazo de
5 s. Fila cheia ou endpoint fora: o lote é descartado e contado, e sai **uma** linha de aviso no stderr
(`jazz: otlp: não consegui exportar para ...`), sem repetir. Não há reenvio: o arquivo
`<id>.events.jsonl` continua sendo a fonte completa. No fim da execução (`Agent.Close`) o que sobrou é
enviado, com no máximo 3 s de espera.

### Apontar para um coletor local (sem instalar nada aqui)

Qualquer receptor OTLP/HTTP serve. Duas opções via Docker, na máquina que você quiser:

```sh
# Jaeger all-in-one: UI em http://localhost:16686, OTLP/HTTP em :4318
docker run --rm -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one:latest

# ou um otel-collector que só imprime o que recebe (config mínima)
cat > otel.yaml <<'EOF'
receivers: { otlp: { protocols: { http: { endpoint: 0.0.0.0:4318 } } } }
exporters: { debug: { verbosity: detailed } }
service:
  pipelines:
    traces:  { receivers: [otlp], exporters: [debug] }
    metrics: { receivers: [otlp], exporters: [debug] }
EOF
docker run --rm -p 4318:4318 -v $PWD/otel.yaml:/etc/otelcol/config.yaml otel/opentelemetry-collector:latest

# depois
jazz -p "rode os testes" --otlp-endpoint http://localhost:4318
```

Se o receptor rejeitar métricas (o Jaeger é só de traces; não testado aqui), vira só o aviso único; use
`"metrics": false` para evitar. Para uma máquina remota, faça um túnel (`ssh -L 4318:localhost:4318 host`) em vez de abrir a porta.

## Custo (medido)

`go test -bench 'BenchmarkObsEmit|BenchmarkAuditRec' -benchtime 2s` num Apple M4 (darwin/arm64):

| operação | tempo | taxa | alocação |
|---|---|---|---|
| 1 evento (`tool.end`) | 2,8 µs | ~355 mil/s | 1,3 KB, 39 allocs |
| 1 registro de auditoria (lock + leitura da cauda + hash + escrita) | 66 µs | ~15 mil/s | 15,6 KB, 73 allocs |

Tamanho medido numa execução real (12 requisições, 15 ferramentas): 81 eventos, ~250 B cada (20 KB),
37 registros de auditoria, ~530 B cada. Por chamada de ferramenta: 2 eventos e até 3 registros de
auditoria, mais um checkpoint (`git write-tree`) quando o passo mudou arquivos. Nenhum fsync no
caminho normal: as linhas vão direto ao kernel (sem buffer em espaço de usuário), então um crash do
jazz não perde nada; fsync só no fim anormal. No Dell (i686, disco lento) os números não foram
medidos; estimando 20–40× mais lento, fica em ~10 mil eventos/s e ~400 registros de auditoria/s,
desprezível perto de uma requisição ao modelo (segundos). O que pode pesar no Dell é o checkpoint por
passo em projeto grande: ele já se desliga sozinho acima de 20 mil arquivos; `JAZZ_CHECKPOINTS=0`
desliga no `-p`.
