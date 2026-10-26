# 0.9 no Dell D610 — progresso medido (26/10/2026)

Mesma máquina e método de `dell-baseline-0.8.1.md`. Binário de desenvolvimento linux/386 (main 61e3260), não instalado.

| medida | 0.8.1 | 0.9 (61e3260) |
|---|---|---|
| `jazz -p "abre o README.md"` | 14,3–21,5 s · 2 requisições · 32,3k tokens | **0,31–0,33 s · 0 requisições · 0 tokens** |
| `jazz -p "git status"` (pasta sem .git) | 34,6–36,1 s · foi ao modelo e bloqueou | **0,52–0,58 s · 0 requisições** · resposta correta |
| `jazz -p "talscle status"` (com ou sem --offline) | não existia (iria ao modelo com o nome literal) | **0,81–0,93 s · 0 requisições** · intenção `tool_status` · roda `tailscale status` e mostra os peers (allowlist exata de comandos só-leitura) |
| `jazz -p --offline "status"` (sem .git) | — | perfil do ambiente, sem erro de git |
| `jazz version` | 83–151 ms | 84–100 ms (otimizações de partida em curso) |
| `jazz env` | — | frio 314 ms · quente 100–144 ms |
| `jazz eval offline` | — | **39/39** · falso sucesso 0 · 34/39 sem modelo · perguntas 3/3 · 0,13 comandos/tarefa · p50 19 ms · p95 387 ms |
| suíte completa de testes (386, -short) | 0 falhas | 0 falhas |
| `jazz doctor --sandbox-probe` | — | tudo ✓: escrita no projeto e no tmp privado OK; em $HOME e /tmp negada |

Bugs encontrados só no Dell nesta rodada (nenhum aparecia no Mac): bwrap não via a pasta de trabalho fora da raiz
(fixtures em /tmp); `go` ausente virava "testes falharam" em vez de "ferramenta ausente"; binário fora de ~/.local/bin
invisível no sandbox; "talscle status" classificado como git. Todos corrigidos e re-medidos acima.

## Rodada final (main ab1efdf, 26/10 07:30–07:45) — após perf-09 + nuTrain + ramo via .git/HEAD

`~/jazz-cli-src` agora é repositório git (915 arquivos, 1 commit) para medir `git status` de verdade. Dell ocioso (load < 0,7),
5 repetições, binário linux/386 de desenvolvimento, `date +%s%N`.

| medida | 0.8.1 | 0.9 (61e3260) | 0.9 (ab1efdf) |
|---|---|---|---|
| `jazz version` | 83–151 ms · 2 GCs · init main 5,8 MB / 58k allocs | 84–100 ms | **15–16 ms · 0 GC · init main 145 KB / 1.572 allocs** |
| `jazz -p --offline "abre o README.md"` | 14,3–21,5 s · 32,3k tokens | 0,31–0,33 s | **120–139 ms · 0 GC · 0 tokens** · RSS pico 27 MB |
| `jazz -p --offline "git status"` (repo) | 34,6–36,1 s · bloqueou | 0,52–0,58 s (sem .git) | **133–203 ms** · responde `## master` |
| `jazz -p "talscle status"` | iria ao modelo | 0,81–0,93 s | **189–366 ms** · roda `tailscale status` (24 ms sozinho) |
| `jazz -p --offline "abre o nlu.go"` | — | — | 430–499 ms sob carga; não re-medido ocioso |
| `jazz env` | — | frio 314 ms · quente 100–144 ms | **26–30 ms** (sonda do sandbox em cache; frio 1ª vez ~375 ms) |
| `jazz eval offline` | — | 39/39 · p50 19 ms | **39/39 · falso sucesso 0 · 34/39 sem modelo · p50 14 ms · p95 362 ms · RSS 59 MB** |
| suíte 386 completa (-short) | 0 falhas | 0 falhas | **0 falhas** (2 rodadas) |

Onde foi o tempo (perfil `JAZZ_CPUPROFILE` no Dell, "abre o README.md"): antes do ajuste, 60% da CPU era `nuTrain` —
48 classes × 16.384 baldes = 786 mil `math.Log` por partida (no 386 o log é software). Agora é um log por classe mais
um por balde ocupado, tabela idêntica bit a bit. CPU total do comando: 200 ms → 90 ms. Filhos medidos isolados no Dell:
`git status --porcelain` 17–18 ms, `git log -5` 4–5 ms, `git rev-parse` 3–4 ms, `bwrap … true` 9 ms, `tailscale status` 24 ms.
O `-p` ainda roda `git status` e `git log` para montar o prompt de sistema antes do caminho curto (o ramo já vem de
`.git/HEAD` sem exec); deixar o prompt preguiçoso muda a ordem de escrita da sessão e fica para depois.

Cuidado ao medir: logo depois da suíte de testes (ou com `tailscaled` em pico) os mesmos comandos deram 0,42–0,49 s
com a mesma CPU; só vale medida com load < 1 e 5 repetições.

Falha achada só no Dell nesta rodada: `TestSandboxBwrapDellArgv` incluía o próprio binário de teste no argv porque o
binário cruzado não termina em `.test` (corrigido com stub no teste).
