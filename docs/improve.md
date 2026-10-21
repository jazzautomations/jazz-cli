# jazz improve: o harness aprende com as próprias execuções (com segurança)

O jazz guarda tudo o que faz: a sessão em árvore, o fluxo de eventos e o log de auditoria
([observability.md](observability.md), [governance.md](governance.md)). `jazz improve` lê esses
arquivos e transforma o que deu errado em **lições curtas**, que voltam no início das próximas
sessões. É a parte segura do MetaHarness/Self-Harness
([research/metaharness-self-improvement.md](research/metaharness-self-improvement.md)): o jazz
aprende texto revisável, **nunca muda o próprio código**, e o que mexeria no harness (regra de
prompt, gatilho de skill, config) sai como relatório para um humano decidir.

## O que ele aprende

Primeiro uma extração determinística, sem modelo, sobre as últimas execuções:

| classe | sinal | escopo |
|---|---|---|
| `cmd-fixed` | comando que não existe (127, `command not found`, módulo Python faltando) ou com opção inválida, e que depois funcionou de outra forma (`rg` → `grep`, `pytest` → `python3 -m unittest`, outras flags do mesmo programa) | projeto |
| `tool-error` | o mesmo erro de ferramenta ≥ 2× (assinatura com caminhos reduzidos e números mascarados): `edit` com argumentos inválidos, `write` recusado, orçamento de exploração esgotado, `old_string` que não bate, ambiente (`Permission denied`, porta em uso…) | global para erros do jazz, projeto para o ambiente |
| `test-fixed` | teste que falhou e passou depois, e os arquivos mudados no meio | projeto |
| `guard` / `spiral` | guarda que recusou a mesma coisa ≥ 2×; disjuntor de bloqueios | global |
| `revert` | o harness reverteu mudança num teste existente | projeto |
| `cutoff` | resposta cortada no limite de saída (`finish=length`), com o tamanho | global (modelo) |
| `loop` | execução que parou em laço, com a chamada repetida | projeto |
| `until` / `quality` | `--until` falhando em várias rodadas; checagem de qualidade com problemas | projeto |
| `timeout`, `rate-limit`, `self-revert` | prazo estourado (e qual requisição estava em curso), HTTP 429 por modelo, edição desfeita na mesma execução | só no relatório: nada que o modelo possa fazer, ou ambíguo (uma prova de mutação também desfaz a edição) |

Saída de comando de tarefa (o `ValueError` do bug que o modelo está caçando) não vira lição: para
`bash`, só erros de ambiente contam. Texto que parece injeção (`injScan`) é descartado, segredos são
redigidos.

Depois, **uma** chamada sem ferramentas ao modelo barato (a mesma fila do classificador do modo
smart: rápido e grátis primeiro) escreve até **3 lições de uma linha**, com a regra "acionável,
específica deste projeto ou desta ferramenta, e que ainda não exista". O jazz valida o que volta:
linha no formato, sessão citada que exista nos achados (senão usa a do achado mais parecido), sem
cara de injeção, e não repetida. O **escopo vem do achado, não do modelo**: um achado global
reescrito como "projeto" continua global (senão escaparia da revisão). Sem modelo (offline,
`--no-model`, `improve.no_model`, erro) os melhores achados determinísticos viram as lições.

Dedupe: texto normalizado igual (minúsculas, sem acento e pontuação) ou sobreposição de termos
≥ 0,6 (`|A∩B| / min(|A|,|B|)` sobre as palavras do `pcTerms`, sem embeddings).

## Onde escreve

| arquivo | o quê |
|---|---|
| `.jazz/lessons.md` (na raiz do repositório git, ou da pasta) | lições do projeto: markdown comum, compatível com AGENTS.md, versionável |
| `~/.config/jazz-cli/lessons.md` (`$JAZZ_HOME/lessons.md`) | lições globais: ferramenta, modelos, provedores |
| `~/.local/share/jazz-cli/improve/<projeto>.proposals.json` | últimas propostas não aplicadas (o `--apply` usa estas, sem chamar o modelo de novo, por 24 h) |
| `.jazz/improve-report.md` | relatório do `--harness` |
| log de auditoria | um registro `lesson.applied` por lição gravada (caminho, via, tag, texto, sessão, sha256) |

Formato de `lessons.md`:

```markdown
# Lições do jazz

Uma lição por linha: `- [tag] texto (sessão <id>)`. …

- [comando] use `grep` em vez de `rg` aqui (`rg` falhou: command not found) (sessão 261020-080319-56df)
- [teste] rode os testes com `python3 -m unittest` (sessão 261020-055950-e94b)

## Histórico

- 2026-10-20 · sessão 261020-080319-56df · auto · [comando] use `grep` em vez de `rg` aqui …
```

Só as linhas de lição (`- …` antes de `## Histórico`, até 40) entram no prompt; a prosa e o
histórico não.

## Comandos

- `jazz improve` — aprende com a última sessão desta pasta (`-n N` as N últimas, `-n 0` todas,
  `--session ID`, `--from PASTA` para sessões/resultados de outro lugar). Mostra os achados e as
  propostas com evidência e **não grava nada** (num terminal pergunta `aplicar? [s/N]`).
- `jazz improve --apply` — grava as propostas (projeto e global) com linha no histórico e registro
  na auditoria.
- `improve.auto: true` no config — ao fim de todo `jazz -p`, aprende com aquela execução e grava
  **só lições de projeto**, no máximo 3 por execução; as globais ficam como proposta
  (`jazz improve -r <sessão> --apply`). Padrão: desligado.
- `jazz improve --harness [--from PASTA]…` — agrega todas as execuções (sessões, eventos e a saída
  do `evals/v2/run.sh`: `summary.txt` + `<tarefa>.log`) num relatório: placar por tarefa, classes
  de falha com contagem e sessões, candidatas a regra de prompt, palavras e tipos de arquivo que
  teriam disparado uma skill, ajustes de config, e por modelo requisições, erros, 429, cortes,
  prazos e latência p50/p95. Rotulado **"proposta — revisar antes de aplicar"**; caminhos da home
  aparecem como `~`. `--out -` imprime em vez de gravar.
- `--lessons off` (ou `JAZZ_LESSONS=off`, `"lessons": "off"`) — não injeta lições nesta execução.

## Como revisar e desfazer

As lições são texto comum: abra `.jazz/lessons.md` e apague a linha. O histórico diz de qual sessão
cada uma veio (`jazz trace <sessão>` mostra a execução inteira); `jazz audit --kind lesson` lista
toda gravação. Para comparar com e sem: rode a mesma tarefa com `--lessons off`.

## Reaproveitar o que deu certo ("da última vez")

O cache de planos (`plancache.go`) já guarda um modelo de toda execução verificada com ≥ 12 passos
e reinjeta até 3 quando o pedido novo tem palavras em comum. Agora cada plano guarda também a
**classe da tarefa** (tamanho e tipo do `bgClassify`, ex. `M/bugfix`), a **assinatura do
repositório** (arquivos marcadores como `go.mod`/`package.json`, os tipos de arquivo da raiz, pasta
de testes) e a sessão. Na **primeira** mensagem de uma sessão, se houver um plano verificado do
mesmo projeto com a mesma classe e assinatura (e que a busca por palavras não trouxe), entra uma
linha `da última vez (…, sessão X): "tarefa" — dica/abordagem; verificado com \`cmd\``.

## Cache de prompt

- As lições entram **uma vez**, quando o prompt de sistema é montado no início da sessão, logo
  depois do bloco do AGENTS.md (pelo `acSteeringPrompt`), e nunca mudam no meio da sessão: uma
  lição gravada por outra execução só aparece na próxima sessão. Sessão retomada (`-r`, `-c`) usa o
  prompt com que nasceu.
- O bloco depende só das linhas de lição (sem data, sem tarefa): duas sessões com as mesmas lições
  têm o mesmo bloco, byte a byte (teste `TestLessonsCacheStability`). Entre sessões o prefixo só
  muda quando uma lição muda.
- A linha "da última vez" depende da tarefa, então **não** vai no prompt de sistema: vai na
  primeira mensagem do usuário (histórico só-acréscimo), como a nota de passagem do `-r`. Assim o
  prompt de sistema continua igual entre sessões com tarefas diferentes.

## Segurança

Lições chegam ao modelo com peso de instrução de projeto, como o AGENTS.md, e são tratadas igual:
`pfSafeRead` (nada de symlink para fora do projeto), segredos redigidos, texto invisível removido e
o bloco marcado `<untrusted suspected_injection=…>` quando parece injeção. A cadeia de ataque
"saída de ferramenta → lição → prompt" é cortada em três pontos: os achados levam só a linha de
erro normalizada (≤ 120 caracteres) e são descartados se parecerem injeção; a resposta do modelo
passa pelo mesmo filtro; e o `improve.auto` só grava escopo de projeto, ≤ 3 por execução, com
histórico e auditoria. Nada aqui altera código, prompt de sistema, skills ou config do jazz.

## Limites (honestos)

- Os achados são heurísticos: um erro repetido de propósito (testar uma guarda) vira candidato; o
  modelo barato e o `--apply` são os filtros.
- `test-fixed` e `quality` costumam ser pouco acionáveis; aparecem como candidatos, raramente como
  lição.
- Sessões antigas (antes da v0.6) não têm eventos nem `is_err`: o erro é deduzido do texto do
  resultado, e latência só aparece com eventos ou metadados de mensagem.
- n = 1 por tarefa não mede melhora; o relatório dá contagens, não taxas.
