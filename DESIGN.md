# jazz-cli — decisões de design

Agente de código CLI em Go, binário estático único (roda até em i686 / Pentium M com 2 GB).
Decisões tiradas da leitura do código-fonte de pi-mono, openai/codex, sst/opencode,
charmbracelet/crush, google-gemini/gemini-cli e deepseek-ai/deepseek-harness (dsh),
mais a documentação oficial do Claude Code e do Aider (out/2026).

## O que copiamos (e de quem)

| Área | Decisão | Origem |
|---|---|---|
| Ferramentas | Núcleo mínimo: `read`, `write`, `edit`, `bash` + `todo`. Busca via `rg`/`fd` no bash. Prompt+schemas < ~1,2k tokens. | pi (4 tools); Claude Code tirou Grep/Glob por padrão; dsh benchmark = só bash |
| Edit | `old_string`/`new_string`/`replace_all` + lote `edits[]`. Casa exato; se falhar, casa em espaço normalizado (NFKC, trim de fim de linha, aspas/travessões/espaços Unicode → ASCII) e reescreve **só as linhas tocadas**, copiando o resto do original byte a byte. Todos os edits casados contra o original (atômico), detecção de sobreposição, preserva CRLF/BOM. Mensagens de erro do pi. | pi `edit-diff.ts`; opencode replacers |
| Edit seguro | Recusa sobrescrever/editar arquivo que mudou desde a última leitura (stale). Remove prefixo de número de linha que o modelo copia do `read`. | dsh `FS_STALE_VERSION`; Claude Code |
| Bash | Shell **persistente** (cwd, env, venv persistem). Cada comando vira uma linha `eval $'…' </dev/null` entre marcadores com nonce; sempre anexa `[exit code N]`. Timeout/cancelamento/`exit` ⇒ **reset, nunca conserto**, e o modelo é avisado. Grupo de processos morto inteiro. | dsh `tool-bash-persistent` |
| Saída longa | Corta 20% começo / 80% fim, salva a saída completa em arquivo e diz o caminho (modelo usa `rg`/`read offset`). Read: 2000 linhas / 50 KB, rodapé com `offset` pra continuar. | gemini-cli (20/80), pi (2000/50KB), dsh spill |
| Cache de prompt | Histórico só-acréscimo, prompt de sistema **fixo por sessão** (data/ambiente congelados no início). Nada de podar resultados antigos por requisição. Anthropic: `cache_control` no sistema, tools e fim da conversa. | dsh "Model Experience"; Anthropic |
| Compactação | Gatilho `min(0,8·W, W − maxOut − 16k)`; mantém ~20k tokens recentes intactos; resumo com o template do pi (Goal/Constraints/Progress/Decisions/Next/Critical Context), pedido como **continuação exata do prefixo** pra reaproveitar o cache. Estouro de contexto ⇒ compacta e tenta de novo. | pi, dsh, opencode |
| Loop | Hash de (tool, args, resultado): 3 repetições ⇒ aviso no resultado; 6 ⇒ encerra o turno. | crush `loop_detection.go`, gemini |
| Robustez com modelo fraco | Conserta JSON de args (cercas, vírgula sobrando, JSON-dentro-de-string), aliases de parâmetros, coerção de tipos, erro de validação devolvido como resultado com os args recebidos; tool desconhecida lista as válidas; tool call em texto (`<tool_call>`) é recuperada; `<think>` vira raciocínio. | pi `validation.ts`, experiência própria |
| Raciocínio | OpenRouter: `reasoning_details` devolvido intacto. DeepSeek: `reasoning_content` devolvido em todo assistant quando há tools (senão HTTP 400). | OpenRouter docs, DeepSeek API |
| Cancelamento | ESC/Ctrl+C cancela via context; toda tool call pendente ganha resultado sintético (`não iniciada` vs `resultado desconhecido`) pra histórico ficar válido. Texto digitado durante o turno entra na fila como "steering". | dsh agent-loop, pi |
| Sessão e trabalho salvo | Sessão JSONL em árvore (`id`/`parentId`, migração na leitura, `/tree`, `-r id@entrada`); grava antes de enviar e o resultado de cada ferramenta assim que volta (write-ahead `tool_start`/`tool_result`). Fim anormal ⇒ checkpoint rastreado + nota de passagem + registro `end`; `-r`/`-c`/`--resume-last-in` continuam dali; folga do `--timeout`. Fluxo de eventos por sessão, auditoria com cadeia de hash, `jazz trace/sessions/audit/changes` (docs/observability.md, docs/governance.md). | pi `session-manager.ts` (árvore, persistência), Claude Code (checkpoints), experiência própria (benchmark que perdeu 52/54 testes no corte) |
| Fallback | Lista de modelos; 429/5xx/402/404 ⇒ tenta de novo e depois o próximo; fallback fica grudado por até 3 min dentro do turno (depois a cadeia recomeça do principal; um `-p` é um turno só e não pode ficar inteiro no último fallback); modelo que travou no primeiro byte vai pro fim da fila por 3 min. OpenRouter: `provider.require_parameters` quando há tools. | OpenRouter docs |
| Instruções | `AGENTS.md` global + da raiz do git até o cwd (um por pasta: AGENTS.override.md › AGENTS.md › CLAUDE.md), 32 KiB. Regras de escopo do Codex no prompt. | Codex, pi, Claude Code |
| Regras do prompt | "continue até resolver", "não chute", verifique do teste mais específico ao mais amplo, não releia arquivo depois de editar, frase curta antes de agir, resposta final curta. | Codex `prompt.md` |
| Sessões | JSONL só-acréscimo por projeto, `-c` continua, `/resume`, `-p` headless. | pi, Codex `exec` |
| Undo | Snapshot dos arquivos antes de cada write/edit por turno, mais checkpoints shadow-git (`checkpoint.go`): repo bare em `dataDir()/checkpoints/<slug>.git` com a raiz como work tree; snapshot (só `update-index` + `write-tree`, sem commit) no início do turno e depois de cada bash que pode escrever. `/undo` e `/rewind` voltam só os caminhos que o turno mudou, inclusive via bash (`sed -i`, `> arquivo`), e preservam edições manuais entre turnos. Respeita `.gitignore`, pula arquivos > 1 MB e desliga com > 20k arquivos, sem git ou com git < 2.25 (aí só write/edit). Só no modo interativo. | Q CLI `checkpoint.rs`, Claude Code checkpoints |
| Skills | Do usuário: `SKILL.md` (frontmatter name/description) em `~/.config/jazz-cli/skills` e `.jazz/skills` (lê `.claude/skills` do projeto também; `~/.claude/skills` só com `skills.claude_dir`); lista de no máximo 12 × 120 caracteres no prompt, o modelo lê sob demanda. Embutidas (`skills.go`, `go:embed`): 13 corpos ≤ 400 tokens (`frontend-craft` ≤ 800: só entra em trabalho de UI), escolhidos pelo harness (regex PT/EN no pedido, globs do arquivo editado, sinais da saída do bash, fase do turno), sem classificador LLM; uma por turno, três por sessão, como `<system-reminder>` só-acréscimo na mensagem do usuário ou no resultado que disparou; o histórico é o registro (volta depois de compactar). `skills: auto\|manual\|off`, `JAZZ_SKILLS`, `/skill <nome>`. Núcleo `<craft>` (~170 tokens) fixo dentro de `<rules>`. | Claude Code, Hermes, Karpathy, ponytail, old-coder |
| Steering | `.jazz/steering/*.md` (`inclusion: always\|fileMatch\|auto\|manual`, `fileMatchPattern`) e `.cursor/rules/*.mdc` (`alwaysApply`, `globs`). `always` entra no prompt congelado; `auto`/`manual` viram lista como as skills; `fileMatch` entra uma vez como `<system-reminder>` no resultado do primeiro read/edit/write que casa (só-acréscimo, cache intacto; o próprio histórico registra o que já entrou, então depois de compactar ou voltar a regra entra de novo). Regras do projeto passam pelo portão de confiança (tipo `steering`). | Kiro steering, Cursor rules |
| Login | ChatGPT via código de dispositivo (máquina sem navegador) + PKCE loopback; Responses API em `chatgpt.com/backend-api/codex`. | Codex `login/`, opencode |

## O que rejeitamos

- **apply_patch do Codex**: só modelos GPT são treinados nele; outros erram muito. str_replace funciona com todos.
- **Prompt gigante e dezenas de tools** (Claude Code ~15k tokens): ruim pra modelo grátis e contexto pequeno.
- **YOLO puro do pi**: mantemos um detector de comando destrutivo que pede confirmação (rm -rf amplo, mkfs, dd em disco, push --force, curl|sh, sudo).
- **Lista de comandos banidos do Crush** (curl, wget, ssh…): bloqueia trabalho legítimo.
- **Arquiteto/editor do Aider** (dois modelos): dobra custo e latência.
- **Edit "fixer" por LLM do Gemini**: chamada extra cara; o match tolerante resolve quase tudo.
- **Podar resultados antigos a cada requisição** (opencode): quebra o cache de prompt.
- **OAuth da assinatura Claude**: os termos da Anthropic proíbem fora do Claude Code. Anthropic entra só por chave de API.
- **TUI pesada (Bubble Tea)**: editor de linha próprio + ANSI, leve e sem flicker em SSH.

## O que entrou na v0.2

| Área | O que mudou |
|---|---|
| Confiança do projeto | `.mcp.json`, hooks e regras `allow` do projeto só valem depois de o usuário confiar (sha256 por pasta em `~/.config/jazz-cli/trusted.json`, 0600). Interativo pergunta uma vez mostrando comandos/URLs; `-p` ignora e avisa no stderr; `jazz trust [--yes]` para CI. Mudou o arquivo, pergunta de novo. Servidor MCP do projeto nunca substitui um global de mesmo nome, e `${VAR}` em config de projeto só expande variáveis seguras ou da lista `project_env` do `mcp.json` global — chave de provedor nunca. Regras de steering do projeto também passam (tipo `steering`). AGENTS.md, skills e comandos do projeto não passavam pelo portão até a rodada de segurança (agora: tipo `instructions`, ver "Segurança para o código aberto"). Regras `deny`/`ask` do projeto sempre valem (só deixam o jazz mais cuidadoso). |
| Segredos | O shell, os servidores MCP e os hooks recebem o ambiente sem chaves (`scrubEnv`, inclui o `key_env` de provedores customizados). Toda saída de ferramenta (built-in e MCP), arquivos de spill, export e log de debug passam por `redactSecrets` (valores conhecidos de env/auth.json + formatos de token). Sessões são apagadas depois de `session_days` (padrão 30; negativo = nunca) e `/forget` apaga a atual. Chave salva aparece só como `…abcd`. |
| Permissões (G11) | Regras persistentes no formato do Claude Code: `Bash(npm test:*)`, `Edit(src/**)`, `Read(.env)`, `WebFetch(domain:go.dev)`, `mcp__srv__*`, em `config.json`, `~/.claude/settings.json`, `.claude/settings.json` e `.jazz/settings.json`. deny > ask > allow; comando composto só é liberado se cada pedaço for; `DangerReason` sempre pergunta. `[t]` deriva a regra (primeiras 2 palavras de cada comando, ou a pasta do arquivo; interpretador/shell/awk/sed/find = só o comando exato; arquivo na home fora do projeto = só o próprio arquivo), mostra a regra na opção e salva em `.jazz/settings.json`. Regra allow nunca cobre redirecionamento pra arquivo (`>`/`>>`); deny/ask enxergam através de `/bin/rm`, `sudo`, `eval` e opções globais do git. `/permissions` lista, remove e adiciona; `--allow-tools` vira regra allow. |
| Hooks (G10) | `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `SessionStart` no formato do `settings.json` do Claude Code (matcher regex sobre `Bash`/`Edit`/`Write`/`Read`/`mcp__…`). `bash -c` na raiz, JSON no stdin, em sequência, 60 s. Saída 2 bloqueia (stderr vai pro modelo); JSON no stdout com `decision`, `reason`, `hookSpecificOutput.additionalContext`/`updatedInput`/`permissionDecision`. A saída do SessionStart entra como nota no primeiro turno, nunca no prompt de sistema. |
| MCP | Status/erro/tools sob mutex (`State()`), nomes únicos (sufixo `_2`), `enabled_tools`/`disabled_tools`, `notifications/tools/list_changed` acrescenta tools só no fim (cache), escrita no stdin por uma goroutine só (sem deadlock com pipe cheio), linha máxima de 32 MB, `notifications/cancelled` também em HTTP, imagem do MCP vira anexo, `Close` síncrono (SIGTERM → 2 s → SIGKILL só se o processo ainda existe). Specs ficam estáveis mesmo se um servidor cair: a chamada falha com mensagem clara. |
| Headless (G5) | `--output-format` text · json · stream-json, `--max-steps`, `--allow-tools`, `--system-append`, `-c`/`-r` com `-p`. Códigos: 0 ok, 1 erro, 2 uso, 3 limite de passos, 4 laço, 5 negado, 6 cortado, 7 não verificada, 8 `--until` falhando (v0.3), 124 tempo esgotado, 130 interrompido. Jobs em segundo plano são encerrados no fim do `-p`. |
| MCP resources/prompts | Duas ferramentas, não uma por recurso: `mcp_resource {server, uri, subscribe}` e `mcp_prompt {server, name, args}` (`uri`/`name` vazio = listar). Só existem se algum servidor pronto declara a capacidade; o prompt de sistema e o spec levam só contagens congeladas na montagem do `MCPManager` (cache estável; `list_changed` não mexe nos specs). `resources/list`, `templates/list` e `prompts/list` paginados no connect (falha de lista não derruba o servidor). Leitura: texto vira texto (truncado com spill), blob textual é decodificado, imagem ≤ 4 MB vira anexo, o resto vai decodificado para `spill/mcp-*.bin` (0600) e o modelo só vê um resumo. Resultado vai por `grMarkResult` + `<untrusted_tool_output server=…>` como qualquer MCP; a URI aparece sempre com o servidor (`srv: uri` no pedido de confirmação). Somente leitura: liberado no plano e em subagente. Rug-pull: `pfMCPCatalogHash` (trust.go) fixa o hash de recursos+templates+prompts de cada servidor na montagem; mudou ⇒ `Poisoned` ⇒ pede confirmação (fora do yolo). `notifications/resources/updated` marca o recurso assinado como `updated` na listagem. `disable_resources`/`disable_prompts` por servidor, `JAZZ_MCP_RESOURCES=off` global. `/mcp resources` e `/mcp prompts`. | `mcp_res.go`, `trust.go`, `mcp.go`, `agent.go` |
| Debug e crash (G6) | `--debug`/`JAZZ_DEBUG=1`: JSON lines em `dataDir()/logs` (0600, gira em 20 MB, guarda 5), corpo das requisições com o prompt de sistema completo uma vez e depois só o sha256, SSE limitado a 256 KB por resposta, tráfego MCP, hooks. Pânico em qualquer goroutine (`safeGo`) restaura o terminal, mata shell e MCP, grava `crash-<data>.txt` e sai com 70. `jazz bug` gera um `.tar.gz` com doctor, config mascarada, último log e (se você aceitar) a última sessão. |
| Sinais | SIGHUP/SIGTERM cancelam o turno e matam o grupo de processos do shell sem esperar o mutex: a queda do SSH para o agente na hora. |
| Atualização (G19) | `jazz update [--check]` de releases do GitHub ou de `update_url` (JSON de release ou pasta HTTP com `VERSION`, `checksums.txt` e `jazz-<os>-<arch>`, gerada por `scripts/release.sh`). Verifica sha256 (e, se o binário tem `updatePubKey` embutida, a assinatura ed25519 de `checksums.txt.sig`, feita por `scripts/sign`) e troca o binário com rename atômico; `update_url` em http só vale pra loopback ou tailnet (100.64/10); sem permissão, mostra o comando `install`. Checagem diária em segundo plano com cache em `update.json`. `deploy-ssh.sh` instala em `~/.local/bin` sem sudo (`--sudo` opcional) e garante o PATH no `~/.bashrc`; `scripts/install.sh` é o instalador `curl | sh`. |
| Interativo | `/permissions`, `/forget`, `/rewind`, `/fork`, `/export`, `/context`, `/omni`, `/jobs`, `/mode plan`, Shift+Tab/Ctrl+T alterna auto → turbo → smart → plan → ask só nesta sessão; fim de turno em plan pergunta se executa. `/exit` e `/clear` perguntam antes de encerrar jobs em segundo plano. CLI: `jazz provider add`, `jazz omni setup`, `jazz export`, `jazz trust`, `jazz update`, `jazz bug`. |

## O que entrou na v0.3

Quatro lacunas da comparação com Bob Shell, Kiro CLI e Devin CLI (`docs/research/bob-kiro-devin.md`, itens 1, 2, 3, 5 e 6).

| Área | O que mudou |
|---|---|
| Checkpoints shadow-git | `/undo` e `/rewind` agora desfazem também o que o bash mudou (`sed -i`, `> arquivo`, `rm`). Repo bare em `dataDir()/checkpoints/<slug>.git`, índice próprio por processo, sem commit (só `update-index` + `write-tree`, sem identidade git) e sem auto-gc. Restauração por arquivo: só os caminhos que o turno mudou voltam, então edição manual entre turnos sobrevive; arquivos criados no turno são apagados (e pastas que ficaram vazias). Desliga sozinho (volta pros snapshots de write/edit) com > 20k arquivos, sem git, git < 2.25 ou qualquer erro do git. Só no interativo. Detalhe na linha Undo acima. |
| Modo smart | Edição dentro do projeto e comando só-leitura rodam direto; o resto vai num pedido **sem tools** a um modelo rápido (fast do omni, grátis primeiro; sem omni, a própria cadeia da sessão) só com a ação, a cwd e a raiz, que responde `SAFE` ou `ASK`. Veredito guardado por regra derivada (`Bash(go build:*)`, `WebFetch(domain:x)`, ferramenta MCP) na sessão; curl/mv/cp/docker/make… só pelo comando exato. Timeout (12 s), erro ou resposta confusa = pergunta, sem cache. Nunca libera sozinho: `DangerReason`, git/gh que altera, instalação de pacote, rm/sudo/kill/ssh, `.env`/chaves/credenciais, config do jazz/Claude (inclusive rodar `jazz`), edição fora do projeto ou com o projeto na home. Regras allow/deny/ask e hooks valem como nos outros modos. | Devin CLI (Smart) |
| Steering | Regras condicionais Kiro/Cursor (linha Steering acima). Exceção à regra "texto não passa pelo portão": regra `fileMatch` entra no meio do turno com autoridade de `<system-reminder>`, então steering do projeto exige confiança (tipo `steering`). |
| `--until` / `/until` | Quando o modelo vai parar, o comando roda no shell persistente como `(cd <raiz> && cmd)` (cwd/env do modelo intactos, 10 min por execução). Saída ≠ 0 volta pro modelo (6 KB) e o turno continua, no máximo `--until-rounds` (padrão 5) vezes e dentro do `--max-steps`; esgotou, para com `until_failed` (código 8) e avisa o próximo turno. Não roda depois de ferramenta negada, em plan nem em subagente. É a prova objetiva de "pronto" contra o modelo grátis que diz que terminou. |
| `--review` / `/review` | Depois de um turno que mudou arquivos num repo git, o subagente só-leitura (`runTask`, contexto novo) revisa `git diff <base>`; base = `git stash create` no início do turno (não mexe em refs nem arquivos), HEAD ou árvore vazia. Defeitos concretos voltam pro modelo uma vez; `LGTM` encerra. Fora de git, não roda. Uma rodada por turno (não `--review N`). |
| `/goal` | Objetivo fixo gravado como linha `{"t":"goal"}` no JSONL da sessão; volta com `-c`/`/resume`, segue no `/fork`, sobrevive à compactação (vai no resumo) e entra como lembrete curto no começo de cada turno, nunca no prompt de sistema (cache). `/clear` esquece o objetivo mas mantém `/until` e `/review`. |
| Fallback temporário | O fallback que funcionou fica grudado por no máximo 3 min (`acStickyTTL`); depois a cadeia recomeça do principal, e quem travou no primeiro byte fica no fim por 3 min (`acStallPenalty`, antes 10). Motivo: um `-p` é um turno só, e na bateria v2 um 429 passageiro deixou a tarefa inteira no último fallback, o mais fraco. Também: a saída guardada do bash respeita o limite mesmo com pedaços de 64 KiB do pipe (só aparecia no linux/386). |
| Contrato | `SetMode` aceita `smart`; nova parada `until_failed` (8); novo tipo de confiança `steering`; `Toolbox.Checkpoints`. Registrado em `CONTRACT.md` §12. |

### Roteador omni

Com `omni.enabled`, cada requisição percorre uma cadeia de candidatos montada por `Config.Candidates`:
primeiro o modelo grudado do turno (fallback que já funcionou), depois o modelo principal, depois
`fallback` da config e por fim todos os provedores com credencial. A estratégia `free-first` (padrão)
põe os modelos grátis na frente; `weighted` usa o `weight` de cada provedor; `ordered` segue a ordem da
config. Erros 429/402/5xx com `Retry-After` curto esperam e tentam de novo; cota diária esgotada marca o
candidato em `exhausted.json` até o reset e ele sai da cadeia (volta sozinho depois). `omni.exclude`
tira provedores ou refs (glob). `jazz omni setup` mostra o que você tem, liga o omni e sugere chaves
grátis; `/omni` mostra a ordem de tentativa atual.

## O que entrou na v0.4

Cinco frentes construídas em paralelo e integradas: skills embutidas, parsers de saída, orçamento +
qualidade, scripts/atalhos/offline e recuperação + cache de planos. Regra de sempre: o prompt de
sistema fica byte a byte igual na sessão (cache); comportamento novo entra como lembrete na
mensagem do usuário ou no resultado de ferramenta, e cada coisa tem chave pra A/B.

### Chaves da v0.4 (todas)

Precedência: flag > variável de ambiente > `config.json` > padrão. **A/B** = chave usada nas
baterias (`jazz-eval-v2`, braço A = padrões, braço B = tudo desligado como na v0.3).

| Chave (`config.json`) | Flag | Env | Padrão | Valores | A/B |
|---|---|---|---|---|---|
| (prompt) núcleo `<craft>` | — | `JAZZ_CRAFT` | on | on · off | **A/B** |
| `skills` | `/skill` | `JAZZ_SKILLS` | `auto` | auto · manual · off · `{mode,claude_dir,threshold,per_turn,per_session}` (padrão `false`, 3, 1, 3) | **A/B** |
| `parse_output` | `--parse-output[=false]` | `JAZZ_PARSE_OUTPUT` | on | on · off | **A/B** |
| `grep_trunc` | `--grep-trunc` | `JAZZ_GREP_TRUNC` | `grouped` | grouped · headtail | **A/B** |
| `tool_budget` | `--tool-budget` | `JAZZ_TOOL_BUDGET` | `auto` (8% da janela, 6–30 KB) | auto · N · 12k | **A/B** (B = 30k, o limite da v0.3) |
| `prefetch` | `--prefetch` | `JAZZ_PREFETCH` | `off` | off · grep | experimental, fora do A/B |
| `budget` | `--budget` | `JAZZ_BUDGET` | `lean` | lean · normal · off | **A/B** |
| `quality` | `--quality` | `JAZZ_QUALITY` | `standard` | standard · strict · off | **A/B** |
| `net_wait` | — | `JAZZ_NET_WAIT` | on | on · off | infra: ligado nos dois braços |
| `fallback_order` | — | `JAZZ_FALLBACK_ORDER` | `score` | score · config | **A/B** (B = config) |
| `scripts` | `--scripts` | `JAZZ_SCRIPTS` | on | on · off | **A/B** |
| `fastpaths` | `--fast-path` (no `-p`, padrão desligado) | — | on no interativo | on · off | **A/B** (no `-p` só com a flag) |
| — | `--offline` | `JAZZ_OFFLINE` | automático | 1 força | fora do A/B |
| `recover` | `--recover` | `JAZZ_RECOVER` | `on` | on · handoff · discard · off | **A/B** |
| `cutoff_continue` | `--cutoff-continue` | `JAZZ_CUTOFF_CONTINUE` | on | on · off | **A/B** |
| `max_output_tokens` | — | — | 0 (limite do provedor, se conhecido) | N | teto do aumento adaptativo |
| `plan_cache` | — | `JAZZ_PLAN_CACHE` | on | on · off | **A/B** |
| `hindsight` | — | `JAZZ_HINDSIGHT` | off | off · on · `provedor/modelo` | fora do A/B (chamada extra) |

Braço B (v0.3) de uma vez só:
`JAZZ_CRAFT=off JAZZ_SKILLS=off JAZZ_PARSE_OUTPUT=0 JAZZ_GREP_TRUNC=headtail JAZZ_TOOL_BUDGET=30k JAZZ_BUDGET=off JAZZ_QUALITY=off JAZZ_FALLBACK_ORDER=config JAZZ_SCRIPTS=off JAZZ_RECOVER=off JAZZ_PLAN_CACHE=off`.

### Como as frentes conversam

- **Ordem no fim do turno**: hooks `Stop` do usuário → checagem de qualidade → `--until` → `--review`;
  o `--recover` entra dentro do `--until` (mesma falha 3×) e no detector de laço/erros seguidos.
- **Mensagem do usuário**: lembretes na frente (orçamento, `/goal`, prefetch) e atrás (skill, cache
  de planos), tudo só-acréscimo. As checagens que leem o pedido (`acRecap`, classificador do
  orçamento, pontuação das skills) tiram os `<system-reminder>` antes, então uma pergunta de recap
  com skill ou plano anexado continua sendo recap.
- **Script conta como ferramenta no orçamento**: script só-leitura sem comando (`find_symbol`,
  `tree_summary`, `git_state`…) é leitura/busca pro limite de exploração; `json_edit` (escrita) e
  `snapshot_cmd` com comando que escreve contam como edição (o `json_edit` também entra na checagem
  de qualidade com pré-imagem); `run_tests` e comando de teste contam como teste rodado.
- **Passagem do `--recover`**: o contexto novo pode olhar de novo (limite de exploração e índice de
  releitura zeram; o orçamento de passos continua).
- **Cache de planos**: `quality_failed` depois de reaproveitar um plano conta como falha do plano.
- **Skills e parser de saída**: a skill de depuração continua disparando sobre a saída já resumida
  (o resumo mantém as falhas e o `[exit code N]`).
- **Rede**: `net_wait` espera a rede voltar dentro da requisição; se mesmo assim falhar por rede ou
  credencial, o interativo entra no modo offline (atalhos e `/script` sem modelo).

### Saída de ferramenta (parsers, limite pela janela, corte do rg)

| Área | O que mudou | Origem |
|---|---|---|
| Corte no limite de saída | `cutoff.go`: `write`/`edit` cortado dentro de `content`/`new_string` (finish `length`/`max_tokens`/`incomplete` ou o erro "Tool calls cutoff by max_tokens", com a chamada parcial guardada pelo provedor openai/anthropic/codex) não é mais jogado fora: guarda até a última linha completa, pede numa mensagem de usuário comum (prompt de sistema e prefixo do histórico intactos, cache preservado; `tool_choice: none`) só as linhas que faltam a partir das últimas 20, tira cerca de código e repetição, junta e roda a ferramenta uma vez; no histórico fica só a chamada completa. Até 3 continuações; depois, a recusa `TOOL_NOT_EXECUTED` de antes. Depois de qualquer corte a próxima requisição pede o dobro de `max_tokens`, até o limite conhecido do provedor (catálogo de modelos, `max_out` do preset, `max_output_tokens`) e metade da janela; limite desconhecido = sem aumento. Regra fixa no prompt: arquivo novo com mais de ~150 linhas vai em esqueleto + `edit` com `edits[]`. | benchmark aider-polyglot-python no Dell (forth, dot-dsl) |
| Saída de ferramenta | `outparse.go`: parsers em Go (regex, sem Python, 1 MB no máximo) pra go test, pytest, unittest, jest/vitest, cargo test, erros de go/gcc/clang/tsc/rustc/SyntaxError, stack trace de Python/Go/Node e log com data/nível, na convenção `{ok, confidence, data, unparsed[], warnings[]}`; `confidence` = fração das linhas não vazias explicadas (no máximo 0,5 sem a linha-assinatura do formato: o resumo final, um diagnóstico com arquivo:linha, frames). Resultado do bash > 2 KB com confiança ≥ 0,8 vira resumo (≤ 40 linhas) + linhas não explicadas + últimas 30 + caminho do spill, só se couber no limite e (quando o bruto também cabe) economizar ≥ 25%; senão, o 20/80. `outshape.go`: limite do resultado = 8% da janela (3,5 bytes/token, entre 6 e 30 KB); `rg`/`grep` (último pipeline, mesmo com `\| head`) cortado por arquivo (ordem mantida, até 5 por arquivo, "+N more … in M files", spill). `prefetch.go`: `--prefetch grep`, um `rg -F` de 3 s no primeiro turno com até 6 termos do pedido (crase, CamelCase/snake_case, caminhos, frames, texto entre aspas), termo com mais de 40 resultados é descartado, até 12 resultados, 3 por arquivo, ≤ 1000 tokens, como `<system-reminder>` na mensagem do usuário (cache intacto). Flags: `--parse-output` (padrão ligado), `--grep-trunc` (padrão `grouped`), `--tool-budget` (padrão auto), `--prefetch` (padrão `off`); flag > env > `config.json`. Fixtures reais e de controle negativo em `testdata/outparse`. | skills-personas-scripts.md C.6/D; grep-is-all-you-need.md rec. 1 e 2 |

### Orçamento (ponytail) e barra de qualidade (old-coder)

`docs/research/skills-personas-scripts.md` seção B, cobrados pelo harness porque modelo fraco
ignora prosa. Tudo entra em lembretes e resultados de ferramenta (prompt de sistema intacto) e
tem chave pra A/B.

| Área | O que faz | Chave (padrão) |
|---|---|---|
| Orçamento (`budget.go`) | Classifica tamanho P/M/G e tipo (bugfix/novo/refatoração/pergunta/sensível) por regex no pedido, sem LLM. Passos 12/25/50 (refatoração +1 tamanho): lembrete em 70% e 100%, parada no dobro (nunca acima do `--max-steps`). Exploração 4/8/15 leituras/buscas antes da 1ª edição: aviso ao atingir, recusa no dobro. Releitura inteira de arquivo inalterado vira ponteiro pro resultado anterior (se ele ainda está no histórico). `write` que mantém > 50% das linhas de um arquivo existente (10+ linhas) é recusado uma vez. Lembrete do turno com a regra de resposta final ≤ 8 linhas e a dica do tipo de tarefa. | `budget: lean\|normal\|off` (`lean`), `--budget`, `JAZZ_BUDGET` |
| Qualidade (`quality.go`) | `acQualityGate` antes do `--until`/`--review`: checklist sobre as mudanças do turno via write/edit (pré-imagem guardada antes da 1ª mudança; mudança só por bash não entra). Itens: testes depois da última edição (sem `--until`), prints de debug, TODO sem `shortcut:`, código comentado 3+ linhas, função > 60 (strict 40), nomes descartáveis (strict), lint/tipo só em linha nova (gofmt + go vet, ruff/pyflakes, eslint local), teste tocado com lógica nova (bugfix só se há teste por perto; refatoração não, mas a contagem de testes não pode cair). Uma rodada de retorno; a segunda falha para com `quality_failed` (9). | `quality: off\|standard\|strict` (`standard`), `--quality`, `JAZZ_QUALITY` |
| Queda de rede (`netwait.go`) | Erro de transporte (status 0) ou stream parado ⇒ sonda TCP em 1.1.1.1:443, no proxy HTTPS e nos hosts remotos da cadeia. Algum conecta: segue o fallback normal. Nenhum: espera com backoff (3→30 s) até a rede voltar ou o `--timeout`, e tenta o mesmo candidato sem penalidade de travada. Provedor local/LAN nunca conta como queda. | `net_wait: on\|off` (`on`), `JAZZ_NET_WAIT` |
| Nota por modelo (`scores.go`) | Passou/falhou no `--until` e na checagem, travadas e latência média por ref em `dataDir()/model-scores.json` (flock + rename atômico). Candidatos depois do primeiro são ordenados por nota (Laplace, faixas de 0,1, mínimo 3 observações), desempate por velocidade, dentro de cada faixa de preço (grátis nunca perde a frente). | `fallback_order: score\|config` (`score`), `JAZZ_FALLBACK_ORDER` |

### Scripts, atalhos e modo offline (pesquisa: `skills-personas-scripts.md` §D, `tocomas-plan-reuse.md` A1–A2)

| Área | Decisão | Flag / config (padrão) |
|---|---|---|
| Ferramenta `script` | Uma tool `{name, args}`; descrição de 3 linhas, nomes no `enum` e assinaturas compactas (`find_symbol(symbol,kind?,path?)`) na descrição do parâmetro: ~400 tokens pros 18. Schema JSON por script (`type`, `enum`, `required`, `anyOf` de required, `additionalProperties:false`, `minimum/maximum`) com coerção de modelo fraco ("5" → 5, "true" → true, lista → string, chave por sinônimo); erro devolve o schema. Python 3 stdlib via `python3 -I` ou `sh`, cwd na raiz, 60 s, grupo de processos morto no timeout e no fim, `scrubEnv`, stdout lido até 2 MB e cortado em 8 KB **sem quebrar o JSON** (corta pela metade o maior array/string e avisa em `warnings`). Prelúdio (`lib/core.py`/`core.sh`) + libs + corpo montados num arquivo só em `dataDir()/scripts/<versão>/`, sha256 conferido a cada execução (cópia editada é reescrita). Spec montado uma vez por Toolbox, depois da lista built-in e antes de `task` (cache). Efeito por script (`read`/`write`/`exec`/`git`/`net`, `effect_by_op`): só-leitura roda em plan e no subagente; `cmd` passa por `DangerReason`, regras `Bash(...)` (deny vale até em yolo) e pelo classificador do smart; `json_edit` tira snapshot pro `/undo`; os outros acionam o checkpoint shadow-git. | `"scripts": "on"` · `--scripts off` |
| Atalhos | `intents.json` embutido (+ `~/.config/jazz-cli/intents.json`, + `.jazz/intents.json` confiado), regex de casamento inteiro, sem acento, sem pontuação e sem "por favor"; comando = template com `{{test}}` `{{build}}` `{{lint}}` `{{fmt}}` (detecção igual à do `detect_project`, conferida em teste), `{{git}}` (só em repo), `{{search}}` (rg ou grep) e slots validados e com aspas POSIX. Variável vazia, slot fora do padrão ou dois intents = não dispara. Roda no shell persistente depois de `permit()`; sucesso vira nota pro próximo turno, falha vai pro modelo como lembrete. | `"fastpaths": "on"` (interativo, com Enter) · `--fast-path` (`-p`) |
| Offline | Erro de rede/DNS/401/403/sem chave (não 429/5xx/travada) ou nenhum candidato com credencial ⇒ modo offline com aviso; a cada 60 s ou `/online` um dial TCP de 1,5 s nos endpoints dos candidatos (sem requisição, sem cota) libera a próxima mensagem pro modelo; resposta do modelo sai do modo. `-p`: atalho resolve com 0; sem atalho, 1 com mensagem. | `JAZZ_OFFLINE=1` · `--offline` · `/offline` |

### Recuperação de travadas e cache de planos

| Área | O que mudou |
|---|---|
| `--recover` (padrão `on`) | Escada do Shepherd depois do "inject" (`docs/research/shepherd-meta-agents.md` A1), sem chamada extra: laço (6×), `--until` com a mesma assinatura de falha 3 rodadas seguidas (números mascarados, ainda com rodadas sobrando) ou 8 erros de ferramenta seguidos (grep sem resultado não conta) ⇒ `acHandoffNote` monta uma nota fixa (pedido, `/goal`, arquivos do turno, últimas 15 chamadas em ~150 bytes, última saída do `--until` ≤ 1,5 KB, o que falhou) e `acHardReset` troca o histórico inteiro por ela (prompt de sistema intacto; uma perda de cache), zera hashes de laço e nudges e o turno segue no `--max-steps` restante. Uma vez por turno, precisa de ≥ 3 passos sobrando. Discard: falhas (`FAIL`, `--- FAIL`, `failed`, `Error`) da última rodada > da primeira ⇒ desfaz o turno antes (`rcRevertTurn`: snapshots de write/edit + checkpoint shadow-git quando ligado). Motivo gravado como `{"t":"recover"}` no JSONL e no log de debug. `handoff`/`discard`/`off`, `JAZZ_RECOVER`, `"recover"`. Padrão `on` (o doc pedia `off` até medir; a tarefa pediu ligado): medir com `off` no A/B. |
| Cache de planos (`plan_cache`, padrão `on`) | APC/AgentReuse + "só guarda o verificado" do TOCOMAS (`tocomas-plan-reuse.md` A4): turno verificado (`--until` passou, `MarkVerified` de outro portão, ou último build/teste no bash com 0 e nada editado depois) com ≥ 12 passos ⇒ modelo em `dataDir()/plans/<slug>.jsonl` (palavras-chave, arquivos, abordagem sem exploração só-leitura, becos sem saída, verificação, `{wins,fails}`; caminhos ficam literais, o arquivo já é por projeto). Pedido novo ⇒ BM25 em Go puro (≥ 2 palavras em comum e ≥ 40% da consulta), até 3 planos em ≤ 600 tokens como `<system-reminder>` no fim da mensagem do usuário (só-acréscimo). Falha objetiva (8, 4, 3, 7, 6) depois de reaproveitar ⇒ `fails++`; 2 apagam. Sem promoção a receita (A3 do tocomas ainda não existe). |
| Hindsight (`hindsight`, padrão `off`) | Shepherd A3: depois de guardar um plano, uma chamada sem tools ao modelo mais forte da cadeia (prior `q` ≥ 78, ou pago sem prior quando `omni.paid` ≠ `never`; ou o `provedor/modelo` da config) escreve dica ≤ 1500 caracteres (arquivo certo, abordagem, beco sem saída, sem código). Só a do melhor plano entra no lembrete. |

### Lições entre sessões (`jazz improve`, `docs/improve.md`)

| Área | O que mudou |
|---|---|
| Loja de lições (`lessons`, padrão `on`) | `.jazz/lessons.md` (projeto, raiz git) e `configDir()/lessons.md` (global): markdown comum, uma lição por linha `- [tag] texto (sessão <id>)`, até 40, histórico em `## Histórico` (nunca injetado). Entra no início da sessão logo depois do AGENTS.md (`acSteeringPrompt` → `lsPromptBlock`), congelada para a sessão: mesmas lições ⇒ mesmo bloco byte a byte. Lida como AGENTS.md (`pfSafeRead`, redação, sanitização, marca de injeção), sem portão de confiança: é o mesmo modelo de ameaça do AGENTS.md. `--lessons`, `JAZZ_LESSONS`. |
| Extrator (`jazz improve`) | Determinístico primeiro (sessão + eventos + logs do `evals/v2`): comando trocado depois de falhar, erro repetido de ferramenta (assinatura normalizada), teste consertado, guardas, disjuntor, reversões do harness, cortes de saída, laços, `--until`/qualidade, prazos e 429 (esses dois só no relatório). Depois uma chamada barata (fila do classificador smart) escreve ≤ 3 lições; escopo e evidência vêm do achado; dedupe por texto normalizado ou sobreposição de termos ≥ 0,6. Proposta primeiro; `--apply` grava; `improve.auto` (padrão `false`) grava só projeto, ≤ 3 por execução. Cada gravação: linha no histórico + `lesson.applied` na auditoria. |
| Relatório (`--harness`) | Placar por tarefa, classes de falha com sessões, candidatas a regra de prompt e a gatilho de skill, ajustes de config, por modelo 429/cortes/prazos/latência p50–p95. Rotulado proposta. O jazz não edita o próprio código (lição do DGM em `metaharness-self-improvement.md`). |
| "Da última vez" | Reaproveita o cache de planos: cada plano guarda classe da tarefa (`bgClassify`) e assinatura do repo (`lrRepoSig`); na primeira mensagem da sessão, um plano verificado da mesma classe e assinatura vira uma linha de dica. Vai na mensagem do usuário, não no prompt de sistema, para o prefixo não depender da tarefa. |

### Subagentes em paralelo (`subagent.go`)

| Área | Decisão |
|---|---|
| Fan-out | Várias chamadas `task` seguidas no mesmo lote, ou uma `task` com `tasks[]` (`{goal, files?, acceptance?, description?}`, até 8), rodam juntas. Cada chamada passa pelo portão (hooks, guardas, permissão, write-ahead) **em ordem, antes** de qualquer filho começar; chamadas que não são `task` antes e depois mantêm a ordem do lote. Relatórios voltam na ordem original (`## [k/n] rótulo` quando é `tasks[]`, ≤ 24 KB juntos). |
| Limite | `subagents.parallel` (config) · `--subagents-parallel N` · `JAZZ_SUBAGENTS_PARALLEL`; padrão auto: 3 com modelo remoto (espera de rede), 2 com servidor local/loopback (CPU). `1` = um por vez (o comportamento antigo; é o A/B). Despachante FIFO: começa o filho pronto de menor índice. |
| Filho | Contexto novo, sessão própria `<sessão>.sub/<id>.<subN>-<rand>.jsonl` (meta `parent`, `parent_entry`, `sub`; fora da lista de sessões), shell persistente próprio (cwd/env não vazam), mesmo `Toolbox` (cerca, estado de leitura, snapshots do `/undo`), MCP, regras, hooks e sandbox do pai. `sub_id` em todo evento; `subagent.start/end`. Prompt de sistema fixo por tipo (pesquisa ou worker): o pedido vai na mensagem do usuário (cache). |
| Orçamento | O que sobra de `max_turn_tokens`/`max_turn_cost` no início do fan-out é dividido igualmente; o filho que passa da sua parte para como o pai pararia. O uso dos filhos entra nos totais do pai. |
| Escrita | Sem `files`: pesquisa só-leitura (como antes). Com `files`: worker, write/edit e bash pelas regras normais (plan força pesquisa). Filhos com arquivos declarados em comum: o segundo só começa quando o primeiro termina. Em execução, o primeiro filho que escreve/edita um caminho é dono dele até terminar; outro que toca o mesmo caminho espera; uma espera que fecharia ciclo é recusada (`PATH_LOCKED`), sem deadlock. Escrita por bash (`cat > x`) não passa pelo cadeado: só a ordem dos `files` declarados cobre. Os guardas honestos não rodam dentro do filho (como no sidekick), mas o `hgCheckSnap` do pai roda depois do `task` e desfaz teste protegido mexido. |
| Cancelamento | Esc/Ctrl+C e o prazo duro cancelam todos; a resposta parcial fica na sessão do filho (com `end`) e volta ao pai como "interrupted … Partial report"; filho que não começou recebe `TOOL_NOT_STARTED`. A folga do prazo (`--timeout-grace`) do pai vale para os filhos. |
| UI | `SubagentStatusLines()` (uma linha por filho: id · rótulo · tempo · última ferramenta ou estado) e `SubagentStatusHook` para a UI interativa redesenhar; as chamadas dos filhos à UI do pai são serializadas (aprovação com trava própria). `-p -v` imprime as linhas a cada 15 s. |

Medido neste Mac (M4, rede doméstica), `kilo/poolside/laguna-s-2.1:free`, `-p --timeout 600s`, pedido
"explique a arquitetura … um subagente por diretório: docs/, evals/, skills/" numa cópia do repo
(3 filhos; 2 execuções por valor, intercaladas). Tempo do `task` (do primeiro filho começar ao último acabar):

| parallel | execução 1 | execução 2 | fim |
|---|---|---|---|
| 1 | 508 s (3º filho cortado pelo prazo) | 473 s (3º filho cortado) | as duas estouraram os 600 s (saída 124) |
| 2 | 466 s | 527 s (um filho cortado) | 1 concluída, 1 estourou |
| 3 | 286 s¹ | 218 s | as duas concluídas (534 s e 351 s no total) |

¹ pasta de trabalho num caminho longo (o modelo errava dígitos do caminho e o portão da cerca negava
leituras); as outras cinco rodaram em `~/jazz-eval-results/pa-par/`. Com 1 e 2 o tempo é o teto do prazo, não o
custo real: a soma dos filhos passou de 790 s nas execuções com 2. A variação de um filho para outro é
grande (66 s a 368 s) e quase todos param no limite de 25 passos: com este modelo grátis o ganho é de
duração do mais lento, não de qualidade. Não medido: no Dell nem com servidor local.

## Segurança para o código aberto (revisão `docs/research/security-review-oss.md`)

Premissa: `DangerReason`/`IsReadOnly`/smart são heurísticas, não sandbox. A rodada fecha os achados F1–F9 e põe guardrails "de fábrica" em volta das ferramentas.

| Achado / área | Decisão | Onde |
|---|---|---|
| F1 pasta não confiável | Tipos de confiança novos `folder` (marca fixa) e `instructions` (hash de AGENTS.override/AGENTS/CLAUDE/JAZZ.md da raiz do git até a pasta, `.jazz`/`.claude` skills e commands). Primeira vez: um prompt só lista tudo; sem confiar, `auto`/`smart` viram `ask` (flag explícita ou `yolo`/`ask` persistido ficam). Headless: `Agent.UntrustedBash` = bash que não é só leitura precisa de regra allow (`--allow-tools`) ou `--yolo`, senão Approve→Deny. `/trust` (status · revoke) recarrega regras e hooks na hora; MCP/steering na próxima sessão. | `trust.go` `pfTrustCollect`, `pfTrustGateWith`, `pfFolderTrusted`; `security.go` `pfUntrustedMode`, `App.trustCmd`; `agent.go` `permit` |
| F2 substituições | Tokenizer próprio (`shellparse.go`): aspas simples/duplas/`$'…'`, escapes, `$(…)` e `<(…)` com parênteses balanceados, crases, `(subshell)`, `{ grupo; }`, palavras-chave (`if/then/do`), comentários, heredoc (corpo vira comando se a linha roda um shell, código se alimenta um interpretador, e só substituições se o delimitador não tem aspas), `eval`, `sh -c`, `find -exec`. Código inline de interpretador (`python -c`, `node -e`, `perl -e`, awk) vai pra `inlineCodeDanger` (strings passadas a `os.system`/`subprocess` reanalisadas, `rmtree` da home, rede + exec, rede + segredo). Entrada que não balanceia ⇒ `DangerReason` = "não consigo analisar". | `shellparse.go` `shellParse`; `safety.go` `DangerReason`, `downloadExecReason`, `inlineCodeDanger` |
| F3 o que roda | `pfRefFiles` acha caminhos (`./x`, `$CLAUDE_PROJECT_DIR/x`, extensões de script) em hooks, command/args do MCP (relativos ao `cwd`) e scripts; o sha256 de cada arquivo dentro do projeto entra no conteúdo confiado (sem arquivos, o hash antigo continua válido). Hooks do projeto reconferem antes de cada execução. TOCTOU: `pfTrustStore` grava os bytes mostrados. | `trust.go`, `hooks.go` `HookSet.Run` |
| F4 privacidade | Antes do primeiro pedido a um provedor `Train`: interativo mostra `OmniPrivacyNotice` uma vez e grava `privacy_ack`; headless escreve uma linha no stderr por execução. Só com `Agent.PrivacyCheck` (main liga; testes não gravam config). | `security.go` `privacyNotice`; `agent.go` `chatWithFallback` |
| F5 segredos | `secretEnvNames` ampliado (sufixos genéricos, `SECRET` em qualquer posição, nomes conhecidos, `SSH_AUTH_SOCK`), valor com senha em URL também sai do ambiente; formatos novos na redação (gh*_, glpat, npm_, Slack, ya29, Stripe, webhooks, senha em URL, `KEY=valor` estilo dotenv). `SecretReason` pede confirmação para ler credenciais pelo bash (globs e `$HOME` expandidos); `secretPath` cobre `.env*` (menos .example/.sample/…), `*.pem`, `id_rsa*`, `.git/config`, `/proc/*/environ`. Testes usam formatos `TESTKEY` e PEM montado em tempo de execução. | `safety.go` |
| F6 instruções | AGENTS/CLAUDE/JAZZ.md, SKILL.md, comandos e steering do projeto: só arquivo comum (Lstat, sem symlink) e realmente dentro do projeto (`pfSafeRead`/`pfInsideRoot`), ≤ 1 MB; segredos redigidos e texto sanitizado antes do prompt. | `trust.go`, `prompt.go`, `tools.go` `instrNote`, `main.go` `customCommands`, `steering.go` |
| F7 atualização | Assinatura obrigatória sobre `manifest.txt` (cabeçalho, `version`, sha256 por asset); sem chave embutida não atualiza; versão assinada ≠ anunciada ou não mais nova ⇒ recusa (sem downgrade/replay); `pfUpdateClient.CheckRedirect` reaplica `pfUpdateURLOK`. `scripts/sign` grava `.sig` e `.minisig` (minisign legado "Ed"); `release.sh` falha sem `JAZZ_SIGN_KEY` e embute a chave no binário e no `install.sh`, que verifica com minisign ou OpenSSL 3. | `update.go`, `scripts/` |
| F8 /undo | `restoreFile`: link no lugar do arquivo é removido e o conteúdo volta por temp + rename; pasta que virou link para fora do projeto é recusada (também no shadow-git, antes do checkout/remoção). | `tools.go`, `checkpoint.go` `cpParentInside` |
| F9 caminhos e jobs | Lista protegida maior (LaunchAgents, vim/nvim, tmux, cargo, `.envrc`, `.vscode/tasks.json`, `.gitmodules`, AGENTS.md/CLAUDE.md em qualquer pasta do projeto, `.jazz`/`.claude` skills/commands/steering/scripts). `max_jobs` (padrão 8). Classificador smart: ação com texto dirigido a ele ("answer SAFE", "ignore previous") pede confirmação sem consultar o modelo; o assunto não fecha `<action>`; o prompt diz que ele só vê a ação. | `safety.go`, `shell.go`, `smart.go` |
| Guardrail: pacotes | Parser de instalação por segmento; consulta registro (existência, data de criação, downloads quando há) + OSV (MAL- sempre; CRITICAL só com versão exata); typosquat por OSA ≤ 2 (≤ 1 até 5 letras) contra `guardrails_top.txt` embutido (PyPI top 500, npm high-impact + frameworks, crates top 300, gems/go/composer curados), ignorado para pacote com mais de 1 ano e uso. Até 4 consultas paralelas, 15 s por comando, cache em disco. Achado vira `reason` (regra allow não pula; só cobre "sem rede"). `sgFetch` é trocado nos testes (`TestMain` desliga a rede). | `guardrails.go` |
| Guardrail: injection | `injScan` (regex de baixo custo; imperativo comum só conta quando fala com uma IA, pede segredo ou se esconde). Acerto marca o resultado, avisa e liga `gr.injection` (por turno): ação que muda algo ganha `reason`, inclusive no auto (no yolo só com `strict`). Instrução do projeto com acerto entra embrulhada no prompt (determinístico, cache intacto) e arma o primeiro turno. | `guardrails.go`, `agent.go` |
| Bloqueio que orienta | Toda recusa (permit, hook, egress, pasta não confiável, injeção, fetch local, cerca) vira `BLOCKED: <o quê> — <por quê>. Continue … Do not retry or look for workarounds.` em inglês; o português fica no terminal. `ToolOutput.Blocked` alimenta o disjuntor: 3 no mesmo alvo (caminho, host, classe de comando) ou 6 no turno ⇒ `StopDenied` com relatório, sem nova chamada ao modelo (no `-p` o relatório é a resposta final). Guarda de testes ganha a mesma frase mas não conta (o modelo deve seguir e consertar o código). Depois de injeção, `write`/`edit` em arquivo comum do projeto não pede confirmação; as linhas injetadas chegam como marcador. | `blockux.go`, `agent.go`, `guardrails.go`, `web.go` |
| Cerca da pasta | Raiz = pasta inicial + topo do git quando a pasta não é ignorada nele (pasta de eval dentro de `results/` fica de fora). Fora: sistema, caches de toolchain, temporários (só se o projeto não mora num), spill/jobs/skills do jazz; config/dados do jazz nunca. Bash: palavras com `/`, `~` ou `..` que existem no disco, `cd` seguido dentro do comando. | `fence.go` |
| Guardrail: sanitização | `sanitizeForModel` em todo resultado, nas instruções do projeto e no texto colado: invisíveis, bidi, tags (decodificadas pro aviso), seletores de variação portadores, escapes, homóglifos em palavra latina; ZWJ/VS16 de emoji preservados. HTML escondido sai de páginas (`fetch`, antes do `htmlToText`) e de MCP; em arquivos lidos só é varrido. `jazz sanitize` mostra o relatório. | `sanitize.go`, `web.go` |

## Sandbox do sistema (`sandbox.go`)

Defesa em profundidade **por baixo** das guardas (DangerReason, cerca do projeto, portão de egress,
regras): elas decidem o que o modelo pode pedir; o sandbox limita o que um processo filho consegue
tocar quando algo passa (yolo, bypass de heurística, hook ou servidor MCP malicioso). Vale para
tudo que o jazz executa: shell persistente, jobs em segundo plano, hooks, servidores MCP stdio,
scripts embutidos (`script`), checadores de qualidade (eslint carrega config do projeto) e o shell
dos subagentes (é o mesmo `Toolbox`). **Não** cobre as ferramentas internas `read`/`write`/`edit`
nem o `fetch`, que rodam dentro do próprio processo do jazz: essas continuam só com a cerca.

| Chave | Valores | Padrão |
|---|---|---|
| `--sandbox` · `JAZZ_SANDBOX` · `sandbox` | `auto` (melhor backend; nenhum ⇒ roda sem e o `jazz doctor` avisa) · `strict` (sem backend ⇒ recusa rodar; filhos só com rede loopback) · `off` | `auto` |
| `JAZZ_SANDBOX_NET` · `sandbox_net` | `on` · `off` (só loopback) | `on` em auto, `off` em strict |

Precedência: flag > env > config. O jazz em si (provedores grátis) sempre tem rede; só os filhos
perdem.

**Embrulho na criação, não por comando.** `Sandbox.Wrap(cmd)` troca `cmd.Path/Args` por
`sandbox-exec … -- prog args` ou `bwrap … -- prog args` depois de `Dir`/`Env` e antes do `Start`.
O shell persistente é embrulhado uma vez: tudo que ele roda herda o sandbox, e o protocolo de
marcador, o cwd reportado e o "reset, nunca conserto" ficam intactos (o reset sobe outro shell já
embrulhado; `kill(-pgid)` mata o `sandbox-exec`/`bwrap` líder, e com `--die-with-parent` +
`--unshare-pid` o namespace inteiro morre junto). `scrubEnv` continua; o sandbox só acrescenta
`TMPDIR=<tmp privado>` (e, em strict, `GOCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` dentro dele).

**Política (macOS e Linux)**: leitura de tudo do sistema; `$HOME` ilegível exceto o projeto e as
pastas de toolchain (`go`, `.cargo`, `.rustup`, `.nvm`, `.pyenv`, `.local/bin`, `.gitconfig`,
`.config/git`…); segredos (`.ssh`, `.aws`, `.gnupg`, `.kube`, `.docker`, `.netrc`, `.npmrc`,
`.config/gh`, chaveiros, perfis de navegador, `.claude`, históricos do shell) e as pastas de
config/dados do jazz ilegíveis **mesmo dentro do projeto**, exceto spill, logs de job, scripts
extraídos e skills. Escrita: projeto (+ topo do git quando o projeto é subpasta rastreada), tmp
privado (`$TMPDIR/jazz-sbx-<uid>/<pid>-…`, podado depois de 24 h), spill/jobs e, só em `auto`, os
caches de pacote (`go/pkg/mod`, `Library/Caches/go-build`, `.npm/_cacache`, `.cargo/registry`…;
envenenar cache é o custo aceito — read-only quebra todo `go build`/`npm install`). Projeto = `$HOME`
(ou acima): a home fica visível, a lista de segredos continua escondida.

| Backend | Como | Estado |
|---|---|---|
| macOS `sandbox-exec` | Perfil SBPL gerado (`deny default`, caminhos como `-D P0=…`, sem problema de aspas). Nega `mach-lookup` de chaveiro, área de transferência, LaunchServices (`open -a Terminal x.command`), Apple events e TCC; `signal` só no mesmo sandbox; sem `/dev/tty` (sem injeção de tecla no terminal); socket do launchd (ssh-agent) negado. Rede off = bind/inbound/outbound só em `localhost`, unix socket só no projeto/tmp, sem mDNSResponder (sem DNS). macOS ignora `TMPDIR` no `mktemp`: a pasta temporária do usuário (`getconf DARWIN_USER_TEMP_DIR`) também é gravável. Deprecado pela Apple, mas é o que Chrome/Codex usam. | **verificado neste Mac** (testes + evals + red-team) |
| Linux `bwrap` | `--die-with-parent --unshare-pid --new-session [--unshare-net] --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --tmpfs $XDG_RUNTIME_DIR --tmpfs $HOME`, depois os caches (`--ro-bind-try`/`--bind-try`), tmp privado, projeto (`--bind`), segredos e pastas do jazz por cima (`--tmpfs` / `/dev/null`), spill/jobs/scripts/skills de volta, `--chdir`. Sem bwrap (ou sem user namespaces) = `auto` roda sem e avisa. Com `--unshare-pid` o `$!` do shell é um pid de dentro: jobs saem do shell e sobem por `startDetached` (como no Windows), com o ambiente exportado do shell (`env -0`). | args testados em unidade (argv exato do cenário do Dell + modelo da ordem de montagem do bwrap); bwrap cru verificado no Dell; argv do jazz a confirmar lá com `jazz doctor --sandbox-probe` |
| Windows job object | Sem isolamento de arquivos. Em `auto`, todo job object do jazz ganha `DIE_ON_UNHANDLED_EXCEPTION`, `ACTIVE_PROCESS` (512) e restrições de UI (área de transferência, desktop, átomos globais, handles USER de fora, logoff, parâmetros do sistema). `strict` recusa (não é sandbox). Plano: AppContainer/LPAC com capacidade só pra pasta do projeto (ACL), via `CreateProcess` com `PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES` — exige `os/exec` próprio (StartupInfoEx), daí fora desta rodada. | compila + `go vet`; **não executado** |

Dell (antiX, Debian 13 i386, kernel 5.10 antiX, runit): `bwrap` não depende de systemd;
o pacote `bubblewrap` existe para i386 (`sudo apt install bubblewrap`). Não verificado se o kernel
antiX permite user namespaces sem root: se `jazz doctor` disser "bwrap não consegue criar
namespaces", `sudo sysctl kernel.unprivileged_userns_clone=1` (kernel Debian) ou `chmod u+s
/usr/bin/bwrap` resolvem. Sem nada disso o `auto` segue sem sandbox.

`jazz doctor`: linha do backend + estado e um teste ao vivo: um `/bin/sh` isolado tenta ler
(`cat`) os arquivos da config e dos dados do jazz (até 6 por pasta, `auth.json`/`config.json`
primeiro) e um canário gravado na pasta de dados só durante o teste, lista as duas pastas (só pode
aparecer o liberado: `skills`, `scripts`, `spill`, `jobs`), lê um controle no temporário privado
(tem que ler) e confere que o binário do jazz continua executável. O teste antigo fazia `ls` da
pasta de config e acusava vazamento falso no Linux: o bwrap esconde a pasta montando um tmpfs
vazio *nela*, então a pasta existe (vazia) e o `ls` dá certo; o conteúdo nunca esteve visível
(medido no Dell com bwrap cru). `jazz doctor -v` (ou `--debug`) mostra o argv exato do
`bwrap`/`sandbox-exec`; `jazz doctor --sandbox-probe` lista cada caminho testado e o resultado.
Teste rápido de ponta a ponta: `jazz --sandbox strict -p 'echo ok'`.

Lacunas conhecidas: leitura fora de `$HOME` é livre (`/tmp`, `/etc`, outros projetos fora da home);
`file-read-metadata` liberado no macOS (dá pra saber que `~/.ssh/id_rsa` existe, não ler); rede em
`auto` é aberta (o portão de egress e as guardas continuam sendo a defesa contra exfiltração); o
`osascript` com Apple event negado trava até o timeout do comando em vez de falhar na hora; MCP
servidor que precisa de rede quebra em `strict` (use `sandbox_net: on`); caches graváveis em `auto`.

## Porte para Windows (nativo, sem WSL)

Regra: Linux/macOS não mudam. Tudo que é de SO fica atrás de build tag (`//go:build !windows` / `windows`) ou de uma função pura com `runtime.GOOS`, testável em qualquer SO.

| Área | Unix | Windows | Onde |
|---|---|---|---|
| Terminal | termios (`ICANON/ECHO/ISIG` off) + `poll` | `SetConsoleMode`: tira `ECHO/LINE/PROCESSED_INPUT`, pede `ENABLE_VIRTUAL_TERMINAL_INPUT` e, na saída, `ENABLE_VIRTUAL_TERMINAL_PROCESSING` (sem `DISABLE_NEWLINE_AUTO_RETURN`, pra `\n` continuar sendo CRLF). Leitura por `ReadConsoleInputW` com `WaitForSingleObject(200 ms)` (o `Pause` do `$EDITOR` continua funcionando); cada registro vira os mesmos bytes de um xterm (`winKeyBytes`: UTF-16 → UTF-8, setas/Home/End/Del sem VT input viram `ESC [ …`, Alt = ESC + tecla, Shift+Tab = `ESC [ Z`), então o parser de `term.go` é um só. Largura: `GetConsoleScreenBufferInfo`. Resize: sem SIGWINCH, um vigia compara a largura a cada 250 ms. | `term_unix.go`, `term_windows.go`, `term.go` |
| Tema | `TERM`/`COLORTERM` | `winTheme`: sem VT ⇒ sem cor e ASCII; Windows Terminal (`WT_SESSION`) ou `TERM_PROGRAM` ⇒ truecolor e Unicode; ConHost com VT ⇒ 256 cores e ASCII (fontes padrão sem ⎿/✻). Interativo sem VT ⇒ erro claro (a interface precisa mover o cursor). | `ui.go` |
| Ctrl+C | `ISIG` off: Ctrl+C é a tecla 0x03 | `ENABLE_PROCESSED_INPUT` off: idem. Fora do modo raw (headless, `$EDITOR`) o handler de console do runtime do Go entrega `CTRL_C`/`CTRL_BREAK` como `os.Interrupt` e `CTRL_CLOSE` como `SIGTERM`, que o `main` já trata. | `term_windows.go`, `main.go` |
| Árvore de processos | `Setpgid` + `kill(-pgid)` | `CREATE_NEW_PROCESS_GROUP` + um *job object* por árvore (`procAttach` logo após `Start`); `TerminateJobObject` mata tudo. Shell/MCP/hooks/scripts com `KILL_ON_JOB_CLOSE` (jazz caiu ⇒ o SO fecha o handle e mata a árvore); jobs em segundo plano sem (como o `setsid`). "SIGTERM" = `CTRL_BREAK` ao grupo; vivo = `WaitForSingleObject` no handle guardado (imune a reuso de PID). Lacuna: o filho roda alguns µs antes de entrar no job. | `proc_unix.go`, `proc_windows.go` |
| Shell persistente | bash `--noprofile --norc` (ou `/bin/sh`) | `pickWindowsShell`: `JAZZ_SHELL` › bash de verdade (nunca o `bash.exe` do WSL em System32/WindowsApps) › Git em Program Files › `pwsh` › `powershell.exe`. Mesmo protocolo de marcador (nonce por chamada, código de saída, cwd, "reset, never repair"). Git Bash: cwd por `pwd -W` (C:/…, limpo para C:\…). PowerShell: `-NoLogo -NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -Command -`; o comando vai em base64 UTF-8 (o stdin do PowerShell usa a code page do console) e é *dot-sourced* (variáveis, funções e `Set-Location` persistem); saída = `$LASTEXITCODE` do último nativo, 1 se escapou erro terminante ou se `$Error` cresceu; marcador aceita código negativo (NTSTATUS). Quoting por dialeto (`$'…'` / `'…'` com aspas e aspas tipográficas dobradas); `--until` usa `subshellAt` (subshell / `Push-Location`). Hooks e jobs no Windows: `oneShotArgv` (`bash -c` / `-EncodedCommand` UTF-16LE). | `shell_dialect.go`, `shell.go`, `hooks.go`, `agent_loop.go` |
| Jobs | `( setsid bash -c … & echo $! )` no shell | `Shell.startDetached`: processo próprio (o `$!` do MSYS não é PID do Windows), cwd e ambiente do shell, stdin nulo, log por append, job object sem kill-on-close; `kill` = `TerminateJobObject` (pega também o que o líder deixou). | `shell.go` |
| Segurança de comandos | `shellparse` + regras | As mesmas regras rodam também na cópia com `\` → `/` (o parser de bash leria `C:\Users\me\.ssh` como `C:Usersme.ssh`); vence o veredito mais estrito. `winDangerReason` cobre PowerShell/cmd (Remove-Item -Recurse -Force ou alvo amplo, `rd /s /q`, format/diskpart/Clear-Disk, `iwr`/`irm`/WebClient + `iex`, `-EncodedCommand`, Set-ExecutionPolicy, registro, schtasks/ScheduledTask, contas, desligar, Defender, firewall, UAC, serviços, takeown/icacls, vssadmin/bcdedit, limpar logs, pasta Inicializar, `$PROFILE`, baixar-e-executar, credencial + envio). `winReadOnly`: allowlist de cmdlets que só leem, sem `$ ( ) { } > & @ %` nem crase, com checagem de arquivo secreto; só vale quando o shell é PowerShell. | `winsafety.go`, `safety.go` |
| Caminhos | case-sensitive (exceto macOS) | `pathEq`/`pathUnder` sem caixa; `protectedPath` + Inicializar, perfis do PowerShell, WindowsApps, `%SystemRoot%`, Program Files, ProgramData\Microsoft, `NTUSER.DAT`; `secretPath` + Credentials/Protect/Vault, perfis de Chrome/Edge/Firefox, GitHub CLI, `.azure`, `System32\config`. Nomes de variável de ambiente comparados em maiúsculas (`envKey`). | `safety.go`, `winsafety.go` |
| Arquivos | `~/.config/jazz-cli`, `~/.local/share/jazz-cli`, 0600 | `%APPDATA%\jazz-cli` e `%LOCALAPPDATA%\jazz-cli` (`JAZZ_HOME`/XDG continuam valendo). 0600 no Windows só mexe no bit somente-leitura: a proteção real é a ACL herdada do perfil (sem ACL própria — melhor esforço). `flock` → `LockFileEx` no primeiro byte. | `config.go`, `lock_*.go` |
| Atualização | rename sobre o binário | asset `jazz-windows-<arch>.exe`; o `.exe` em uso não pode ser sobrescrito mas pode ser renomeado: vira `.old` (apagado no próximo start) e o novo assume o nome; falhou ⇒ volta o antigo. `install.ps1`: Ed25519 (RFC 8032) em PowerShell puro com `System.Numerics.BigInteger` — o .NET não tem Ed25519 —, testado contra assinaturas do `crypto/ed25519` do Go (`install_ps1_test.go`). | `update.go`, `scripts/install.ps1`, `scripts/release.sh` |
| /proc | memória, CPU, zumbis | `GlobalMemoryStatusEx`, `ProcessorNameString` e `ProductName` do registro; zumbi não existe. | `prompt_unix.go`, `proc_windows.go` |

Testes: os que dependem de bash, permissões POSIX, symlinks ou do tema Unix ficaram com `//go:build !windows`; o harness (`harness_test.go`) e os testes puros rodam nos dois. Testes só-Windows: tradução de teclas, fallbacks do console sem console (CI usa pipes), job object matando neto, `LockFileEx`, pastas, caixa de caminhos/ambiente, shell detectado, job em segundo plano. Os testes do dialeto PowerShell e do `install.ps1` rodam no Windows e em qualquer SO com `JAZZ_TEST_PWSH=<pwsh>` (o CI Linux passa o `pwsh` do runner).

Não verificado em Windows de verdade (só compilação cruzada e `go vet` com `GOOS=windows`, mais o PowerShell 7 do macOS para o dialeto e o instalador): console, job objects, `LockFileEx`, Git Bash. O job `windows-latest` do CI é a primeira execução real. Lacunas conhecidas: sob PowerShell o stdin dos programas nativos é o pipe do jazz (programa que lê stdin trava até o timeout; trocar o `STD_INPUT_HANDLE` exigiria P/Invoke dentro do PowerShell); saídas de programas que escrevem direto no console (`CONOUT$`) aparecem por cima da interface; jobs em segundo plano não herdam variáveis exportadas no shell.

### Ainda não (TODO)

- Ambiente em allowlist quando `CI=true` (a revisão sugere; hoje é denylist ampliada).
- Perguntar antes de qualquer escrita fora da raiz do projeto no modo `auto` (F9, sugestão).
- ~~`--sandbox`~~: feito, ver "Sandbox do sistema". Ainda falta: egress só para o host do provedor (hoje a rede do filho é tudo ou só loopback) e Landlock/seccomp no Linux sem bubblewrap.

- **OAuth de servidores MCP remotos (G18 b)**: 401 + `WWW-Authenticate`, descoberta, registro dinâmico e PKCE com `/mcp login <servidor>` (aceitando colar a URL de retorno numa máquina sem navegador). Hoje só cabeçalhos estáticos (`headers` com `${VAR}`).
- ~~MCP resources/prompts (G18 d)~~: feito, ver a linha "MCP resources/prompts" da tabela. Falta: comandos `/servidor:prompt` que injetam o prompt como turno do usuário, `resources/unsubscribe` e persistir o hash do catálogo entre sessões.
- `--input-format stream-json` no headless.

## frontend-craft e ui_lint (depois da v0.4.3)

| Área | O que mudou | Origem |
|---|---|---|
| Skill `frontend-craft` | 13ª skill embutida: o brief manda; 4 modos (Persuadir, Operar, Ler, Experiência) escolhidos pela superfície, não pelo produto; reflexos (hierarquia, escala de espaço 4px, escala de tipo, contraste 4,5:1, movimento com propósito e `prefers-reduced-motion`, estados, responsivo, a11y, texto do produto); proibições genéricas; "termine o entregável; renderize nos tamanhos de entrega quando houver renderizador; uma olhada → corrige o lote → olha de novo". Gatilho: palavras PT/EN (design, ui, ux, landing, dashboard, css, site, tela, página, componente, estilo…) e globs de UI na edição. Corpo ≤ 800 tokens (o dobro das outras), porque só entra em trabalho de UI. | Impeccable v4.7 (Paul Bakaus, Apache-2.0), reescrito; `docs/research/impeccable-frontend-craft.md` |
| `ui_lint` (script + hook) | Script nº 19, só regex e biblioteca padrão (Python 3.6+), 24 regras determinísticas. Hook do harness (`uilint.go`): depois de `write`/`edit` bem-sucedido num arquivo de UI, roda o script (15 s) e anexa até 12 achados ao resultado da ferramenta como `<system-reminder>`; limpo não acrescenta nada; mesmos achados viram uma linha; arquivo consertado ganha "is clean now". Roda mesmo com `--scripts off` (precisa de `python3`). `--ui-lint on\|off`, `JAZZ_UI_LINT`, config `ui_lint`, padrão on. | Hook PostToolUse do Impeccable (camada imediata), só a parte que regex decide sem renderizar |

## Ferramentas em paralelo (`partools.go`)

| Área | O que mudou |
|---|---|
| Lote com N>1 chamadas | As chamadas de leitura (`read`, bash só-leitura pelo `IsReadOnly`, `fetch`, script só-leitura) entre duas chamadas que mudam algo começam juntas, limitadas por um semáforo, antes de o laço chegar nelas. O laço continua o mesmo: cada chamada passa, na ordem do modelo, por hooks, guardas honestas, orçamento, permissão, cerca, injeção e quebra-espiral; só quando a porta deixa rodar o `execTool` pega o resultado já pronto. Recusou, o resultado é jogado fora e o modelo nunca vê. Resultados entram no histórico na ordem original (transcrição determinística). |
| Barreiras | write, edit, bash que muda algo, script que muda algo, MCP, `task` e sidekick rodam em ordem; nada depois deles começa antes de terminarem (leitura depois de edit vê o edit; dois edits nunca se sobrepõem). `todo` não quebra o segmento, mas roda em ordem. |
| Sem efeito antecipado | O `read` antecipado adia o carimbo de leitura (o que libera um edit depois) e a nota do AGENTS.md até ser pego. Nada começa antes da porta quando ela poderia perguntar ou classificar: modo ask/smart para rede, regra ask/deny, cerca, credenciais, egress, injeção suspeita no turno (fetch/script) ou qualquer hook `PreToolUse`. |
| Bash | O shell persistente é um só e não abrimos outro: bash só-leitura antecipado roda num shell curto (`bash -c`) com cwd e ambiente exportado do persistente (capturado uma vez por geração do shell), a mesma linha de init, o mesmo `scrubEnv` e o mesmo sandbox (`sbWrapCmd`). Saída 127 (função/alias que só o persistente tem) roda de novo no persistente; é só-leitura. PowerShell e MSYS ficam no persistente. |
| Chave | `tools.parallel` no `config.json`, `JAZZ_PARALLEL_TOOLS`, `--parallel-tools`: `auto` (padrão: min(4, 2×CPUs); com 1 CPU só as chamadas de rede, 2 por vez) · `on` (min(4, 2×CPUs) também com 1 CPU) · `off` · N (1–16). Eventos `tool.start`/`tool.end` ganham `lane` (`par`/`seq`). |

## Modo turbo e confirmação com uma tecla (`turbo.go`, `approvals.go`)

Queixa do dono: digitar `sim` + enter a cada comando é fricção, e o `auto` ainda pergunta coisas que
o sandbox já segura. Decisões:

| Tema | Decisão | Onde |
|---|---|---|
| Uma tecla | `enter`/`s` sim · `a` sempre · `n`/`esc` não · `v` ver (comando inteiro, diff do edit) · `e` não + instrução. Pergunta numa linha só, estilo Nord, com o motivo de risco em vermelho; o detalhe curto vai numa linha, o longo na caixa de sempre (bug 10). Mantidos: carência de 250 ms e a regra de 600 ms sem tecla antes da resposta (letra no meio de uma palavra não responde). Texto sendo digitado quando a pergunta abre: resposta por palavra + enter (`apTypedChoice`). `Ask` (s/n) aceita enter = sim. | `ui.go` `Approve`, `apKeyChoice`, `apPromptLine` |
| Turbo | `auto` + duas folgas, só com sandbox que isola (`Isolates()`): (1) `checkout -- .`/`checkout -f`/`restore .` e `reset --hard` em branch não compartilhada (sem upstream, nome fora de main/master/trunk/develop/release*/prod*) quando o checkpoint shadow-git está ativo; nunca com `cd`, `git -C` ou outro perigo no mesmo comando; (2) host de egress aprovado uma vez fica lembrado no projeto. Sem sandbox: regras do `auto` + aviso único. Nunca chama modelo (o smart chama). **F1 continua**: a primeira versão liberava bash em pasta não confiada contando com o sandbox; o red-team 11 (`make setup` → `curl|sh` do Makefile) deu COMPROMISED (o sandbox conteve o payload, mas ele rodou) e a folga saiu. | `turbo.go`, `agent.go` `permit` |
| "Sempre" com motivo | Par (classe, alvo) com hash em `.jazz/approvals.json`: `egress/<host>`, `fence/<pasta>`, `supply/<comando>`, `git/<comando exato>`, `pipe-shell/<host>`. Arquivo só vale com o hash no armazenamento de confiança (repositório não traz aprovação pronta); caminho protegido. Nunca guardados: segredo, arquivo protegido, config do jazz, catastrófico, rug-pull de MCP, injeção no turno, regra `ask`. Duas razões na mesma chamada (ex.: egress + cerca) não usam o armazenamento. Modo `ask` ignora o armazenamento. | `approvals.go` |
| Auditoria | `auto_approval {tool, target, rule}` para toda chamada que muda algo liberada sem pergunta no turbo (`turbo:projeto+sandbox`, `turbo:git-discard-checkpoint`) e, em qualquer modo, quando o armazenamento (`approvals:<classe>`), uma regra allow (`perm:<regra>`) ou um hook liberou o que pediria confirmação. | `turbo.go` `apRecordAuto` |
| Smart vs turbo | Não são redundantes: o smart pergunta **mais** que o auto (manda ao modelo o que o auto rodaria direto); o turbo pergunta **menos** e é determinístico. Mantidos os dois; o classificador do smart só roda com `mode == "smart"`. | `smart.go` |
| Padrão | O padrão continua `auto`. A tela de boas-vindas recomenda `/mode turbo` quando há sandbox. Proposta: virar padrão quando houver sandbox, depois de rodar o red-team no Dell (bwrap). | `ui.go` `Welcome`, `turboWelcomeHint` |

