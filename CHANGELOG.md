# Changelog

Resumo honesto, tirado do `git log`. Nenhuma dessas versões foi publicada como release no GitHub
ainda; as datas são as dos commits. Formato livre, uma linha por mudança relevante.

## Não publicado (branch `turbo`)

- Confirmação com uma tecla em toda pergunta sim/não da TUI: `enter`/`s` sim, `a` sempre, `n`/`esc`
  não, `v` ver (comando inteiro ou diff), `e` não + instrução. Acabou o `sim` + enter dos comandos
  perigosos. Pergunta numa linha com o motivo de risco; texto sendo digitado quando ela abre ainda
  exige palavra + enter.
- Modo `turbo` (`--turbo`, `--full-auto`, `"mode": "turbo"`, Shift+Tab): com sandbox do sistema, não
  pergunta nada que fica no projeto; pergunta só fora do projeto, host novo de egress (uma vez),
  segredo, git destrutivo, pacote marcado, comando catastrófico, regra ask/hook/injeção. Sem sandbox
  vira `auto` e avisa. Determinístico (sem modelo). A tela inicial recomenda turbo quando há sandbox.
  Pasta não confiada continua pedindo confirmação pra bash que muda algo (o red-team 11 derrubou a
  versão que liberava isso contando só com o sandbox).
- "Sempre" de pergunta com motivo de risco guardado por projeto em `.jazz/approvals.json` (hash de
  classe + alvo; só vale se o jazz escreveu); `/approvals` lista e `/approvals clear` apaga.
- Auditoria: `auto_approval` com a regra que liberou cada ação sem pergunta.
- `evals/redteam/run.sh` aceita `MODE=turbo`. Rodada no Mac (`kilo/poolside/laguna-s-2.1:free`, sandbox
  `sandbox-exec`): 14/14 BLOCKED. Tarefas v2 01/05/10 com `--turbo` (pasta confiada com `jazz trust --yes`):
  3/3 PASS, nenhuma pergunta, 27 `auto_approval` na auditoria.

## 0.7.0 (2026-10-21, não publicado como release; inclui o que foi para a 0.6.0)

- Ferramentas em paralelo: numa resposta com várias chamadas, as de leitura (`read`, bash só
  leitura, `fetch`, scripts só leitura) entre duas que mudam algo começam juntas; cada uma ainda
  passa por hooks, guardas, permissão e cerca na ordem do modelo, e o resultado entra no histórico
  na ordem original. Escrita, edição, bash que muda algo, MCP e `task` são barreiras. `--parallel-tools
  auto|on|off|N`, `JAZZ_PARALLEL_TOOLS`, config `tools.parallel`. Medido nas tarefas v2 01/05/09: as
  ferramentas somam 0,4–0,8 s de execuções de 3–9 min, então o ganho de tempo ali é desprezível.
- Corrigidas três condições de corrida achadas com `-race` (texto da mensagem lido pela goroutine das
  ofertas enquanto era reescrito; vigia de conexão e sonda offline lendo globais que os testes trocam).
- TUI para máquinas lentas (Dell, 1 núcleo): a resposta é desenhada em quadros por uma
  goroutine própria (≤ 20 fps, ≤ 5 fps em respostas longas, metade disso no modo econômico) em vez
  de uma escrita por token; linha de status sempre visível sob o texto (modelo, fase da requisição,
  tokens, "parado há Ns", o que você está digitando); larguras em células (emoji/CJK), cursor
  estacionado no redesenho (redimensionar não deixa pedaços da caixa), teclas em rajada viram um
  redesenho; `--low-power` / `JAZZ_LOW_POWER`, automático com 1 CPU; GOGC 200 com 1 CPU.
- Rede honesta: fases `conectando` / `aguardando 1º token` / `gerando`; sem conexão em 3 s com a
  sonda falhando = sem rede na hora; no interativo isso encerra o turno e liga o modo offline
  (banner do que funciona sem modelo, contagem no rodapé, sonda a cada 30 s e volta sozinho);
  troca de modelo dita com o motivo (`… não respondeu em 20s → tentando …`); prazo do 1º byte de
  20 s no interativo; modelo grátis mais rápido primeiro quando você não fixou um; linha de início
  com modelo e estado dos provedores.
- Menus numerados de ações determinísticas (offline, modelo lento, `!comando` que falhou offline) e a
  ferramenta `ask_user` (interativo: menu; `-p`: não bloqueia, `--ask-stdin` lê do stdin).
- `jazz --worktree [nome]`, `jazz worktree list|merge|rm`, `/worktree`.
- Testes em PTY real (86x27, xterm e xterm-256color) com emulador VT e reflow como o VTE.
- Exportação OTLP/HTTP JSON (`--otlp-endpoint`, config `otlp`): sessão = trace, turno/requisição/
  ferramenta/subagente = spans, guardas como eventos de span, métricas de tokens/custo/latência.
  Assíncrona, em lotes, descarta com contador e um aviso se o coletor cair; desligada por padrão.
  Veja `docs/observability.md`.
- MCP resources e prompts: ferramentas `mcp_resource` e `mcp_prompt` (listar/ler/buscar), só contagens no prompt de sistema, blob grande em arquivo de spill, resultado sanitizado como não confiável, hash anti rug-pull cobre recursos/templates/prompts, `/mcp resources` e `/mcp prompts`.
- Subagentes em paralelo: várias chamadas `task` no mesmo lote (ou `tasks[]` numa chamada) rodam juntas,
  até `subagents.parallel` (auto 3 remoto / 2 local; `--subagents-parallel`), cada uma com contexto,
  sessão (ramo `<sessão>.sub/`), shell e parte do orçamento próprios; relatórios na ordem; `files` faz um
  worker que edita, com cadeado por caminho; Esc/prazo cancelam todos e guardam o parcial;
  `SubagentStatusLines()` para a UI. Medido: 3 filhos, 218–286 s com 3 em paralelo contra 473–508 s
  (cortado pelo prazo de 600 s) um por vez.
- `jazz improve`: lições entre sessões tiradas das execuções medidas (sessão, eventos, logs do
  `evals/v2`). Extração determinística + uma chamada ao modelo barato, ≤ 3 lições de uma linha com
  evidência `(sessão <id>)`; proposta primeiro, `--apply` grava em `.jazz/lessons.md` (projeto) e
  `~/.config/jazz-cli/lessons.md` (global); `improve.auto` grava só projeto, ≤ 3 por execução, com
  histórico no arquivo e `lesson.applied` na auditoria. As lições entram no início da sessão logo
  depois do AGENTS.md e ficam congeladas (cache intacto); `--lessons off` desliga.
  `jazz improve --harness` gera `.jazz/improve-report.md` (proposta): falhas recorrentes, regras e
  gatilhos candidatos, config, 429/cortes/latência por modelo. O cache de planos agora guarda a
  classe da tarefa e a assinatura do repo e dá uma dica "da última vez" na primeira mensagem.
  Detalhes em `docs/improve.md`.
- Nenhum trabalho perdido: write-ahead de cada ferramenta na sessão; em todo fim anormal do `-p`
  (prazo, erro de provedor, 429, Ctrl+C, crash, disjuntor, orçamento) checkpoint rastreado, nota de
  passagem e registro de fim; `jazz -p -r <id>` / `-c` / `--resume-last-in <pasta>` continuam de onde
  parou (sem prompt, sozinho). `--timeout-grace`: antes do prazo, nenhuma requisição nova. Saída:
  0 concluída, 5 negada, o resto incompleta; o JSON traz `outcome`, `checkpoint`, `handoff`, `resume`.
- Sessão em árvore (formato v2, do pi): `id`/`parentId` em toda entrada, arquivos antigos migrados na
  leitura, `/tree` e `-r <id>@<entrada>`; o assistente grava modelo, provedor, tokens, fim e latência.
- Observabilidade: `<sessão>.events.jsonl` (requisições, ferramentas, guardas, checkpoints, orçamento,
  skills, compactação, recuperações, erros com payload), `jazz trace`, `jazz sessions`,
  `--metrics-out`. Esquema em `docs/observability.md`.
- Governança: log de auditoria com cadeia de hash (`jazz audit [--verify]`): config em vigor e
  mudanças do config.json, confiança, bloqueios/negações/aprovações, toda mudança de arquivo com hash
  antes/depois, todo comando de shell, rede por host, atualizações. `jazz changes <arquivo>` diz qual
  sessão/passo/modelo mudou cada trecho (commits em `refs/jazz/trace` do repositório-sombra).
  Checkpoints agora também no `-p` (`JAZZ_CHECKPOINTS=0` desliga).
- Juiz omni (`docs/research/omni-judge.md`): classificador de tarefa por regras (11 tipos, S/M/L,
  sinais do repo), placar por (modelo, tipo) em `model-scores.json` gravado ao fim de cada turno,
  seleção UCB1 com exploração ε atrás de `--omni-judge on`, sementes das evals medidas,
  `jazz models --scores`, registros de debug `"kind":"route"`. Papéis `roles` (planner, editor,
  verifier), `--verifier`, `--hedge` (pedidos pequenos a 2 modelos: p50 do classificador 8,4 s →
  1,3 s no Mac), `--panel` e `--fusion` (lead/sidekick com contexto próprio); os três off por
  padrão (medidos, sem ganho comprovado). `LANES=N` nas evals v1/v2.
- `ratelimit.json` perdia contagem diária/mensal com vários processos (36 de 60 num teste com 4
  processos): agora cada processo soma seus incrementos ao valor do arquivo. Nomes de spill e de
  log de job incluem o pid (colidiam entre processos no mesmo segundo).

- `jazz doctor` no Linux: o teste ao vivo do sandbox acusava "config do jazz legível" à toa (o
  bwrap esconde a pasta com um tmpfs vazio, e o teste só fazia `ls` nela). Agora tenta ler os
  arquivos de verdade, um canário e um controle; `jazz doctor -v` mostra o argv do sandbox e
  `jazz doctor --sandbox-probe` lista cada caminho testado.
- `write`/`edit` cortado no limite de saída é continuado e emendado (até 3 continuações, cache
  intacto) em vez de recusado; orçamento de saída adaptativo depois de um corte (`max_output_tokens`,
  `max_out` dos modelos grátis da Kilo); regra de esqueleto + `edits[]` para arquivo novo grande.
  `--cutoff-continue off` / `JAZZ_CUTOFF_CONTINUE=off` / `"cutoff_continue": "off"` volta ao antigo.
- Preparação para código aberto: README em português e inglês, `scripts/install.sh` com
  `--system`/`--version`/`--require-sig`, workflow de release que gera o manifesto, assina quando há
  chave e publica os instaladores, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, este arquivo, aviso MIT
  do pi em `third_party/`, limpeza de caminhos pessoais e de resultados de eval versionados.

## 0.5.0 (2026-10-20)

- Sandbox do sistema para tudo que o jazz executa: `--sandbox auto|strict|off` (`sandbox-exec` no
  macOS, verificado; `bubblewrap` no Linux, ainda não verificado em máquina real; Windows só job object).
- Skill embutida `frontend-craft` (derivada do Impeccable) e hook determinístico `ui_lint` depois de
  editar arquivos de UI.
- Red-team: correções de validade (o sumidouro não registrava nada com o log trancado, o cenário 11
  não servia o payload, o 14 rodava sem symlink) e evidência medida do sandbox.

## 0.4.3 (2026-10-20)

- Bloqueio que orienta ("siga a tarefa sem isso"), disjuntor de tentativas bloqueadas e cerca da
  pasta do projeto; edição permitida no arquivo marcado como injeção.
- Red-team: 14/14 BLOCKED, 0 ERROR numa rodada (uma repetição por cenário).

## 0.4.2 (2026-10-20)

- Guardas de conclusão honesta (anti-trapaça: teste apagado, teste enfraquecido, cache contado como teste).
- Porte nativo para Windows (amd64/arm64, Git Bash ou PowerShell, job objects, `install.ps1` com
  verificação Ed25519 em PowerShell puro) e CI no `windows-latest`. Ainda não testado em uso real.
- Correções vindas da eval v2.

## 0.4.1 (2026-10-20)

- Rodada de segurança: confiança da pasta, guardrails (pacotes, prompt injection, baixar-e-executar),
  sanitização de Unicode invisível, egress, atualização assinada (Ed25519), redação ampliada.
- Baterias de eval v1/v2 e suíte red-team.

## 0.4.0 (2026-10-19)

- Skills embutidas escolhidas pelo harness e núcleo `<craft>` no prompt.
- Saída resumida por parsers em Go (testes, build, stack trace, log); corte do `rg` por arquivo.
- Orçamento de passos, checagem de qualidade no fim do turno, espera de rede, fallback ordenado por nota.
- Ferramenta `script` (scripts embutidos em Python/sh), atalhos sem modelo e modo offline.
- `--recover` (passagem/descarte quando trava) e cache de planos verificados.

## 0.3.0 (2026-10-19)

- Checkpoints shadow-git (`/undo` e `/rewind` cobrem mudanças via bash), modo `smart`, steering
  condicional (formatos Kiro/Cursor), `--until`, `/goal` e `--review`.
- Trava contra ação fingida, `--timeout` global no `-p`, visual novo (paleta Nord).

## 0.2.0 (2026-10-19)

- Roteador omni, limitador de cota e presets de provedores grátis.
- Confiança de projeto, permissões no formato do Claude Code, hooks, log de debug, `jazz update` e MCP robusto.
- Subagente, modo plano, tools em texto para modelos sem function calling, rewind/fork/export.

## 0.1.0 (2026-10-19)

- Núcleo do agente: provedores, ferramentas (`read`, `write`, `edit`, `bash`, `todo`),
  shell persistente, MCP, interface de terminal e sessões.
