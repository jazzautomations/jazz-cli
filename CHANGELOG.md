# Changelog

## Não lançado

### Noção de tempo dentro da execução

- O prompt de sistema traz a hora de início da sessão (fixa, o cache do prompt continua valendo).
- Entre os passos, no máximo a cada 2 min, o modelo recebe `relógio: agora 20:31 · rodando há 7m ·
  faltam 3m de 10m` (o "faltam" só com `--timeout`). Antes ele sabia a data, mas não a hora, nem quanto
  tempo já tinha gasto, nem quanto restava até o aviso final de 3/4 do prazo.

## 0.12.1

### Cardápio v3 (sistema/ops): 48 scripts, e o cérebro offline usa mais 9 sem modelo

- 10 scripts novos: env_tools, uptime_load, mem_top, net_ifaces, fs_recent, archive_list, hash_verify,
  pkg_installed, cron_list, log_errors_recent. Só leitura e só consulta: `/proc` e `/sys` em vez de
  `free`/`ip`/`ps`; o MAC sai mascarado por padrão; `archive_list` marca `..`, caminho absoluto e link
  que escapa da pasta, e avisa de bomba de descompressão; `hash_verify` nunca diz "ok" sem ter lido o
  arquivo inteiro.
- Offline, sem modelo: uptime/carga, quem come memória (`mem_usage` agora usa `mem_top`), arquivos
  mudados recentemente, conteúdo de zip/tar, checksum, pacotes instalados, cron/timers, erros recentes
  do log, interfaces e IPs. 8 intents novos e 6 tarefas no eval.
- Na máquina de desenvolvimento, `jazz eval offline`: 101/101 · falso sucesso 0 · 99/101 sem modelo ·
  perguntas = necessárias.

## 0.12.0

### Cardápio offline: 38 scripts determinísticos, e o cérebro offline usa 26 deles sem modelo

- 18 scripts novos em `jazz-scripts/` (v1: config_get, dotenv_check, proc_ports, disk_usage, file_hash_diff,
  service_status, git_cochange, todo_scan · v2: git_blame_hot, git_branch_status, git_conflict_scan, lint_run,
  symbol_refs, large_funcs, dup_code, license_scan, dep_graph, dep_outdated). Todos stdlib, sem rede, só
  consulta, saída determinística com orçamento e `truncated`, `text` de uma linha que responde a pergunta.
  Oráculos honestos: dono de porta ilegível é `null` (não `pid 0`), processo casa por nome exato (nunca
  substring), `dotenv_check` só emite NOMES de variáveis. Mapa completo em `docs/research/cardapio-offline.md`.
- Cérebro offline ligado ao cardápio: 17 conceitos e 14 intents novos (`todo_scan`, `git_cochange`, `git_blame`,
  `git_branch_status`, `git_conflict_scan`, `dotenv_check`, `config_get`, `file_snapshot`, `dep_graph`,
  `dep_outdated`, `dup_code`, `license_scan`, `large_funcs`, `symbol_refs`), métodos `script:<nome>` em
  `methods/`; `list_ports` lê `/proc` (sem `ss`), `disk_usage` usa statvfs (sem `df`), `lint` cai nos linters
  instalados quando o projeto não tem comando. "quantas portas TCP estão escutando?" responde em < 1 s com 0
  tokens na máquina de referência (antes ia pro modelo). A primeira linha da resposta é o resumo do script.
- Fallbacks `.no_python` só disparam quando python3 falta de verdade (antes diziam "precisa de python3" quando
  o script tinha falhado por outro motivo — inclusive em `find`). `$goal` em métodos: o pedido inteiro vai
  pro script, que casa com dados reais (`config_get` acha a chave e o arquivo).
- Split de pedido composto ("A e B") só quando cada parte é lida com confiança de ação; "procura por TODO e
  FIXME" é uma tarefa, não duas.

### Confiança calibrada (0.13)

- `nlu_calib.go` + `nlu/calib.json`: a confiança que o cérebro compara com os limiares agora é probabilidade
  calibrada (isotônica), forjada com K-fold sobre os exemplos da gramática (o classificador nunca vê o
  exemplo que pontua), negativos e prompts do eval — 901 amostras. `Intent.Raw` guarda o score cru;
  **opt-in nesta versão** (`JAZZ_NLU_CALIB=on`): as amostras ainda são os exemplos da gramática (95% de
  acerto) e o piso isotônico levantava leituras fracas acima do limiar — próximo passo é amostrar dos misses reais; regenerar: `JAZZ_UPDATE_CALIB=1 go test -run 'TestNLUCalibAsset$'`.
  O que a curva mostrou: score cru entre 0,16 e 0,82 acerta ~60–64%; regra sem classificador (0,90) acerta
  93%; regra e classificador de acordo (0,97) acerta 99%.
- Limiar de ação por faixa de risco sobre a confiança calibrada (Chow assimétrico, `brActMin`): leitura
  verificável age com menos certeza do que mutação; a pergunta fica pra quando o erro custa.

### Provedores e rede

- `jazz omni health`: sonda cada modelo free com 1 token e explica o erro — chave recusada/expirada (401, e o
  400 "valid API key" do Gemini) × modelo morto (404/descontinuado) × cota (429) × fora (5xx). Catálogo sem
  ids mortos; Pollinations sem chave; `kiloFastFree` atualizado. CI diário `free-models-health.yml` abre
  issue quando um modelo free some.
- `--fresh` / `JAZZ_FRESH`: janela de contexto independente da memória (sem AGENTS.md, lições e memória
  automática) — modo incógnito ao lado do modo com memória.
- Espera de rede com teto (`netwait`): numa queda, espera no máximo `min(limite, 1/4 do orçamento)` por vez e
  15 min no total — antes uma queda comia o orçamento inteiro (raiz dos timeouts em lote).
- Compactação com fallback determinístico quando o resumo do modelo vem vazio; prazo de execução
  (`RunDeadline`) com parada suave e nota de retomada.

- ChatGPT (assinatura): ids de modelo atuais (`gpt-6.1-sol` padrão, `gpt-6.1-terra`, `gpt-6-luna`; o
  `gpt-5.5-codex` não existe mais). Sem `jazz login chatgpt`, o jazz usa a sessão do Codex CLI
  (`~/.codex/auth.json` / `CODEX_HOME`) só para leitura — nunca renova o token dele (a rotação
  deslogaria o Codex); expirada, avisa pra rodar `codex` uma vez ou logar.

### Confirmação, sandbox e segurança

- Confirmação de intenção só quando a LEITURA é incerta (typo-only ou confiança abaixo do limiar); mutação
  entendida com confiança alta não ganha pergunta extra — o consentimento é o gate de permissão na execução.
- `--yolo` compartilha o namespace de PID do host (gerência de processo); fora dele continua isolado
  (`JAZZ_SANDBOX_HOST_PID=1` liga à parte).
- `.gitleaks.toml` com regras pra hosts de tailnet, caminhos de home e ids de WhatsApp. O repo é público.

### Medido na máquina de referência (1 núcleo, 2 GB, linux/386)

`jazz eval offline`: 95 tarefas (eram 75) · 95/95 com sucesso verificado · falso sucesso 0 · 93/95 sem
modelo · perguntas = necessárias · p50 ~40 ms · p95 ~400 ms · RSS ~140 MB. Suíte Go inteira ok.
Passagem de bastão e armadilhas: `docs/MAINTAINERS-HANDOFF.md`.

## 0.11.0

### O agente sabe onde está e onde cada saída nasceu

- `env_probe` identifica a máquina de verdade: hostname, usuário, distro, kernel, modelo da CPU, modelo DMI e
  uptime (só `ReadFile`, sem processo extra) — `jazz env` e o `env_status` do cérebro mostram
  `host · usuário · modelo · distro · kernel · CPU · uptime`. Perguntar pela própria máquina
  ("status do labbox" estando nela) responde "é ESTA máquina", nunca um ssh pra si mesmo.
- Proveniência de execução: depois que a sessão roda `ssh`/`tailscale ssh`/`scp`/`rsync`, todo resultado de
  ferramenta declara onde rodou — `[ran on REMOTE host …]` ou `[ran LOCALLY on …]` — para o modelo não atribuir
  saída local ao host remoto (bug observado numa sessão real).
- Prompt de sistema carrega `user@host · modelo · CPU` e a regra "tools run on THIS machine; remote hosts are
  reached only via explicit ssh".
- `self_info`: "quem é você", "versão do jazz" respondem deterministicamente (versão, binário, modo, cobertura
  do cérebro), sem modelo.
- Cérebro level-0 também no modo online interativo: intenções só-leitura entendidas com confiança ≥ 0,85 são
  respondidas sem chamada de modelo; `explain`/`project_summary` viram grounding (fatos verificados injetados
  como contexto) em vez de resposta final; "Mandar pro modelo" no atalho é respeitado.
- Palavrões/interjeições são fillers — "porra" não vira typo de "porta" nem divide em multi-intent.
- "whoami", "que usuário", "em que máquina estamos" → `env_status`; `dispositivo`/`aparelho`/`hostname` no
  vocabulário de ambiente.
- `@` picker usa índice reconstruído na hora quando a construção é barata (arquivos recém-criados aparecem).
- Eval offline: 75 tarefas, suporte a `contains_any` (a resposta certa depende da máquina onde roda).
- Privacidade: nomes de máquinas/hosts dos exemplos e testes viram genéricos; o histórico público foi reescrito
  sem dados de ambiente real.


Histórico de mudanças. Números de desempenho são medidos no Dell Latitude D610 (Pentium M 1,73 GHz, 2 GB, Linux 386), a máquina de referência; detalhes em `results/`.

## 0.9.0

### Cérebro offline (harness primeiro, modelo opcional)

- Sem modelo (`--offline`, sem rede ou sem credencial), um pedido que não é atalho passa por um interpretador
  clássico (`nlu.go`, `nlu/*.json`: 47 intenções, 609 exemplos, entidades do projeto e da máquina por
  Aho-Corasick + distância tolerante a digitação, naive Bayes de n-gramas só para desempate; "talscle status" vira
  `tailscale status`, "labbx" vira o host `labbox`). Confiança abaixo de 0,6 não age.
- Tarefa com estado explícito (`task.go`, transições gravadas na sessão e no `jazz trace`, estado "não verificado"
  quando não há verificador) executada por métodos com pré-condições e verificador (`methods/*.json`): `open_file`,
  `run_tests`, `git_status`, `env_status`, `tool_info`, `tool_status`, `connect_host`. Recuperação por categoria de
  falha observável (`recover.go`) e limite de três olhadas sem fato novo antes de perguntar.
- No `-p`, mesmo com modelo, um pedido claro só de leitura (confiança ≥ 0,85: "abre o README.md", "git status",
  "como está o ambiente", "tenho o docker?") é respondido sem nenhuma requisição; `JAZZ_BRAIN=off` desliga.
  Comandos só de leitura do registro de capacidades (`tailscale status`, `docker ps`…) rodam até em pasta não
  confiável, por lista exata.
- `jazz env [--json] [--refresh]`: perfil só-leitura da máquina (SO, núcleos, RAM, Termux/WSL/contêiner,
  ferramentas, hosts SSH, máquinas Tailscale, serviços, contêineres, portas), cache de 10 min; alimenta o cérebro.
- `jazz eval offline [--out DIR] [--only id,…] [-v]`: 39 tarefas com o modelo desligado. Dell: 39/39 com sucesso
  verificado, falso sucesso 0, 34/39 sem modelo, p50 14 ms. Contrato em `docs/design/offline-brain.md`.
- Dell, `jazz -p "abre o README.md"`: 14–21 s e 32k tokens na 0.8.1 → 120–139 ms e 0 tokens; `git status`: 35 s → 133–203 ms.

### Menor intervenção e capacidades de modelo

- Em qualquer modo, `yolo` e `turbo` inclusive, escrever em `/etc`, mexer no kernel, em binários do sistema, em
  `~/.config/containers` ou na configuração do Docker/Podman/WSL é CRÍTICO (confirmação explícita); numa tarefa de
  contêiner, remover/reinstalar o motor é bloqueado como desproporcional. Origem: um usuário no WSL teve o Podman
  quebrado pela 0.7.
- Registro de capacidades por modelo: imagem ou áudio com um modelo que não vê/ouve pede a troca (`/model`) em
  vez de inventar. Ações de teclado/mouse são sempre críticas.

### Desempenho (estudo `docs/research/low-level-performance.md`)

- Regexps de pacote preguiçosas: `init main` de 9,5 MB / 59k alocações para 145 KB / 1,6k no 386.
- GC ajustado antes do `init` em máquina de um núcleo e desligado no caminho curto (`internal/early`);
  `jazz version` no Dell: 84–100 ms e 2 GCs → 15–16 ms e 0 GC.
- Distância de edição ASCII sem alocação (13×), filtros de limite inferior antes da distância, caminhos ASCII em
  normalização e tokenização, laços no lugar de regexps nos pontos quentes.
- `jazz -c` lê só a sessão mais nova (por mtime) e a lista usa `<id>.meta`; sonda do sandbox em cache por
  binário/kernel/uid por 7 dias (`jazz doctor` sempre sonda); lista de arquivos a partir do `.git/index`; índice de
  nomes em arrays sem ponteiro persistido em `.idx` com CRC; `gitRoot` memoizado; ramo lido de `.git/HEAD`.
- Treino do classificador: um `math.Log` por classe mais um por balde ocupado em vez de 786 mil por partida.
- `JAZZ_CPUPROFILE=arquivo` e `JAZZ_TRACE=arquivo` perfilam o processo inteiro (gravados em qualquer saída).

### Pool grátis e provedores

- 429 de cota põe o provedor em espera até o reset informado (ou 15 min) sem punir o juiz; `jazz omni status`
  mostra o pool, as esperas e os limites conhecidos (`docs/FREE-AI.md`, com fontes). GitHub Models aposentado em
  30/07/2026; Hugging Face só com plano pago.

### Sandbox

- Pasta de trabalho fora da raiz do projeto é montada sob `/tmp` no `bwrap`; o próprio executável do jazz fica
  visível; `bwrap:` / saídas 126 e 127 viram "ferramenta ausente", não "teste falhou".

### Interface

- Primeira execução sem chave: três passos (como rodar: grátis/Anthropic/OpenAI/ChatGPT/Ollama →
  chave escondida → pedido de teste com o resultado na tela); `jazz setup` refaz. Sem terminal não pergunta.
- `/model` abre uma lista por provedor: ↑↓, número (dois dígitos com 10+ linhas), filtro, grátis/pago,
  latência medida, "sem chave" pede a chave ali. `/omni` sozinho: menu liga/desliga e modelo por papel.
- Menos idas e voltas: confirmações da mesma resposta numa caixa numerada (Enter pra tudo);
  `ask_user` sobre branch, comando de teste, gerenciador de pacotes ou "arquivo existe?" é
  respondida pelo projeto; métrica `prompts` no `--metrics-out` e no resumo do `evals/v2`.
- `jazz sessions` no terminal é a lista com ↑↓/número; `jazz resume <n|id> [--full]`; conversa
  retomada maior que a tela é paginada (espaço, enter, q pula pro fim).
- Status line mais barata no SSH (só o spinner é redesenhado entre segundos): ~800 → ~310 B/s
  esperando, ~3,0 → ~1,1 KB/s no streaming. Rodapé com grátis/custo; ferramenta lenta mostra a
  duração; `TERM=dumb` sem movimento de cursor.

## 0.8.1

- 0.8.1: correções de CI Linux/Windows e gitleaks
- Sandbox Linux (`bwrap`): escrita fora do projeto, do temporário privado, de spill/jobs e dos caches agora falha (`$HOME`, `/tmp` e as pastas escondidas são tmpfs remontados só-leitura); antes ia para um tmpfs descartável e sumia. Caches de build ausentes no host vão para o temporário privado. `jazz doctor --sandbox-probe` testa escrita no projeto/tmp privado (deve funcionar) e em `$HOME`/`/tmp` (deve falhar).
- Guarda do estado privado no Windows: `cat C:\Users\...\trusted.json` passava (o parser POSIX comia as barras invertidas antes da guarda); agora a linha também é lida com `\` como separador, pelo splitter de cmd/PowerShell, com `$env:X`/`%X%` e caminhos entre aspas com espaço.

## 0.8.0 — candidato em validação

### Android e guardas

- Asset nativo `android/arm64`, instalador e updater correspondentes; regressões de `$HOME` no Termux. Cross-build validado; teste em aparelho pendente.
- Yolo sem sandbox exige consentimento no interativo ou `--ack-no-sandbox` no headless.
- Acesso direto detectado ao estado privado bloqueado em todos os modos; egress de arquivos sensíveis detectado exige confirmação nova. Parsing de shell não substitui isolamento do sistema.
- Scrollback de ferramenta colado pede confirmação para envio como citação não confiável. Aprovações de caminhos externos guardam o alvo exibido.

### Provedores e diagnóstico

- Recusa do provedor (Anthropic `stop_reason: "refusal"`, OpenAI `content_filter`/`refusal`) encerra o
  turno com "o provedor recusou: <motivo>" e a sugestão de reformular ou trocar de modelo (`/model`),
  sem tentar outro modelo por conta própria. Erros 4xx mostram a mensagem do provedor
  ("o provedor recusou o pedido (HTTP 400: …)"); saldo zerado da Anthropic (HTTP 400 "credit balance")
  conta como falta de crédito.
- `jazz doctor --provider <nome>[/modelo]`: um pedido real mínimo pelo mesmo código do agente; mostra
  de onde veio a chave (variável ou `auth.json`, só os 4 últimos caracteres), o status HTTP e o corpo
  do erro sem segredos.
- Anthropic: com `effort` definido, modelos Claude 4.6+ recebem `thinking: {type: "adaptive"}` +
  `output_config.effort` em vez de `budget_tokens` (recusado com HTTP 400 nos 4.7+ e 5.x) e não
  recebem `temperature`; os blocos de thinking assinados voltam no laço de ferramentas. Teste de
  conformidade com servidor Anthropic falso que valida cabeçalhos, corpo e o stream SSE.
- Binário `linux/armv7` (GOARM=7) para ARM 32 bits (Raspberry Pi 2/3 com sistema 32 bits, TV boxes);
  instalador e `jazz update` reconhecem `armv7l`/`armhf`. Cross-build validado; sem teste em aparelho.

### Próximo foco

- Menos idas e voltas por tarefa com modelos fortes (relato do dono com Claude: "dá muita volta à
  toa"). Medir com o placar do juiz (pedidos por tarefa) e `jazz trace` antes de mudar o laço.

### Redação de segredos

- Segredos reconhecidos são redigidos na fronteira do provedor (OpenAI-compatível/kilo/omni,
  Anthropic, Codex; subagentes, hedge/painel/verificador e compactação passam por ela). Segredos em
  arquivos comuns, logs, diffs e saídas de comando viram marcadores estáveis `<secreto:tipo#n>` com
  mapa reverso só em memória; o marcador usado pelo modelo em `bash`/`write`/`edit` é trocado pelo
  valor real antes da execução local. ~30 padrões (AWS, GCP, Azure, GitHub, GitLab, Slack, Stripe,
  Twilio, SendGrid, OpenAI/Anthropic/kilo, JWT, PEM, Bearer/Basic, URL com senha, `password=`/
  `senha=`, `.env` de alta entropia, credenciais locais) com entropia e lista de falsos óbvios
  (`CANARY`, `example`, `changeme`…). `redact: on|off|strict`, `--redact`, `JAZZ_REDACT`, `/redact`,
  `/redact allow` (HMAC por máquina em `.jazz/redact-allow.json`). Aviso único, "N segredos retidos"
  no total do turno, eventos `redact.hit {kind, where}` e auditoria só com o tipo. ~0,4 ms por 10 KB
  de código denso em credenciais (M-series). Provedor em `localhost` não é redigido; a saída para o
  usuário também não.

### Interface, contexto e execução

Interação no nível do Claude Code. Junta confirmações em menu, turbo condicionado ao sandbox do
sistema, sessões, editor de entrada, documentação por versão, study-first e fila de mensagens.
Passo a passo da tela com os textos exatos em [docs/UX.md](docs/UX.md). Esta versão ainda não foi
publicada como release.

### Confirmações e modo padrão (branch `confirm`)

- Toda pergunta é um menu numa caixa (↑↓/j k + Enter, número, atalhos `s` `a` `n`/Esc `e` `v`);
  nunca é preciso digitar `sim`. A pergunta de permissão mostra o comando exato ou o diff e o motivo
  de risco inteiro em vermelho, uma linha por problema. Respostas sim/não aceitam uma tecla; Enter
  é sempre a opção destacada.
- Sem `mode` na config, o interativo começa em `turbo` quando há sandbox que isola
  (`sandbox-exec`/`bwrap`), senão `auto`; `-p` continua `auto`. A tela de boas-vindas explica o
  modo na primeira vez (`mode_intro`). Shift+Tab: turbo → auto → plan → ask.
- "Sim, e não perguntar de novo": edição vale na sessão; comando, domínio, MCP e script viram
  regra do projeto em `.jazz/settings.json`; motivo de risco vai para `approvals.json`
  (`always_scope: auto|session|project`).
- Pasta nova: menu "Sim, confiar nesta pasta / Só nesta sessão / Não, modo ask"
  (`trust_default: always`). Plano, jobs ao sair, `/forget`, atalho, `ask_user`, `/rewind`,
  `jazz trust`, `jazz worktree merge` e `jazz improve` usam o mesmo menu.
- Configs antigas com `"mode": "auto"` gravado continuam em auto: apague a linha ou `/mode turbo`.

### Editor de entrada (branch `editor`)

- Paleta `/` com busca aproximada (`/cmp` → `/compact`, `/modle` → `/model`; nome exato primeiro,
  corrige `/mode` + Enter rodando `/model`); lista `@` de arquivos e pastas do projeto; ↑↓ do
  histórico filtra pelo que já está digitado; Ctrl+R mostra "nada encontrado"; Shift+Enter nas
  duas formas (`ESC[13;2u`, `ESC[27;2;13~`); Option+seta pula palavra; Ctrl+O expande e recolhe;
  com texto digitado durante o trabalho, o primeiro Esc limpa e o segundo interrompe; rodapé
  encolhe com a largura. Config `ui.palette`, `ui.mention_picker`, `ui.history_prefix`.

### Sessões (branch `sessions`)

- Sessões como no Claude Code: `/resume`, `/sessions` e `jazz -r` sem id abrem uma lista com ↑↓/j k,
  Enter, Esc, filtro ao digitar (tolerante a erro), 1–9 de atalho e Tab pra todas as pastas; cada
  linha tem título, data relativa, modo, mensagens, tokens, custo e situação. `/tree` usa a mesma
  lista. Retomar desenha a conversa de novo (pedidos, respostas em markdown, ferramentas recolhidas,
  totais; últimas 200 entradas, `resume_replay`), na ordem banner → conversa → aviso. Título de 5–8
  palavras gerado do primeiro pedido sem chamar modelo; `/rename` troca.

### Documentação, estudo e revisão

- Ferramenta `docs`: documentação por versão detectada nos manifests/lockfiles, fonte local e cache
  para uso offline; Context7 é opcional e sujeito a guardas explícitas de HTTP. A consulta retorna
  trechos relevantes, não baixa nem executa código.
- Skill study-first e orientação estática para tarefas médias e grandes: consultar instruções e
  código existente com leituras/busca antes da primeira edição. A nota dinâmica é adicionada ao
  contexto do usuário/ferramenta, sem alterar o prompt estável do sistema.
- `--review`/`/review`: subagente somente leitura examina o diff do turno; achados heurísticos voltam
  ao agente para uma tentativa de correção. Não é uma prova formal nem uma garantia de segurança.
- Redação de saída reconhece padrões conhecidos de segredo no limite da requisição; detecção é
  parcial e não garante reconhecer todos os segredos.

### Termux (branch `termux`)

- Suporte ao Termux (Android, sem root): detecção por `TERMUX_VERSION`/`PREFIX`; temp em `$TMPDIR` ou
  `$PREFIX/tmp`; shell de fallback `sh` do PATH (sem `/bin/sh` literal); sem sandbox do sistema (o
  `doctor` explica, o `turbo` cai no `auto`); modo de baixo consumo por padrão; `jazz doctor` confere
  python3/git/curl/openssl com a dica `pkg install`; `install.sh` instala em `$PREFIX/bin` sem sudo.
  Testado só com ambiente simulado, não em aparelho.
## Não publicado (branch `backdoor-guard`)

- Revisão do próprio diff: no fim de cada turno o jazz revisa só os arquivos que mudou (linhas
  adicionadas) com um motor de regras em dados (`diffrules.json` embutido, pacotes em
  `~/.config/jazz-cli/diffrules.d` e `.jazz/diffrules.d` com confiança, tipo `diffrules`). Regras:
  segredo literal (CRITICAL); TLS/host desligado, dependência de URL git/tarball/registro
  desconhecido/typosquat, scripts de ciclo de vida, mudança em CI/CD, escrita fora do projeto em
  rc/cron/autostart (HIGH); Unicode invisível (MEDIUM). CRITICAL/HIGH perguntam com o trecho (`v`);
  "sempre" grava hash em `.jazz/scan-allow.json` (confiança + caminho protegido); "não" restaura os
  arquivos sinalizados; headless restaura, escreve `.jazz/scan-findings.json` e sai com 5.
  `diff_guard: on|off|strict`, `--diff-guard`, `JAZZ_DIFF_GUARD`; auditoria `diffguard.finding`.
  `jazz scan [--since ref] [--rules]` roda sob demanda; semgrep opcional (`jazz doctor` mostra).
  Red-team: cenário `15-tls-off`.

### Guardas de supply chain (branch `supply-fp`)

- Guarda de supply chain: OSV é consultado pela versão que será instalada (a fixada
  ou a que o registro resolve), então `axios`/`chalk`/`debug` atuais não são mais "maliciosos" (só as
  versões comprometidas); typosquat só bloqueia nome fora da lista popular, a distância 1-2 de um nome
  mais popular e que o registro diz ser novo (< 90 dias) ou pouco usado (checagem com limite de 2 s;
  sem rede = aviso); pinos do lockfile/`requirements` são confiáveis; scripts de ciclo de vida só
  avisam fora da lista popular; OSV bloqueia só HIGH/CRITICAL com correção, o resto é aviso uma vez;
  no `turbo` avisos nunca perguntam (inline + auditoria). `supply_chain: strict` volta ao antigo.

### Fila de mensagens (branch `msg-queue`)

- Fila de mensagens (como no Devin CLI, semântica do pi): o que você digita durante o trabalho entra
  numa fila visível embaixo do status (`na fila (N): …`) sem bloquear nem interromper. Itens
  **agora** (steer: até 200 caracteres, ou `!`/`ei`/`opa`/`na verdade`/`espera`) entram no turno na
  próxima fronteira de ferramenta como mensagem do usuário marcada; itens **depois** (follow-up:
  mais longos, ou `depois:`/`+`) viram o próximo turno quando o atual termina bem. ↑/↓ editam,
  Delete remove, Tab alterna o modo, `Esc Esc` limpa; turno interrompido devolve a fila ao editor.
  Gravado na sessão antes da próxima requisição; system prompt intocado. Eventos `queue.add`,
  `queue.edit`, `queue.consume`, `queue.drop` no `jazz trace`. Headless: `--follow-up "msg"`
  (repetível) e `JAZZ_STEER_FILE` (linhas = mensagens agora, lidas em cada fronteira) para evals.

## Não publicado (branch `typos`)

- Tolerância a erros de digitação, determinística: `/modle`→`/model`, `/mode trubo`→`/mode turbo`,
  ambíguo vira menu numerado (`fuzzy_cmd.go`); nomes do pedido (arquivo, caminho, função, teste)
  resolvidos contra um índice do repositório com contexto da frase, sessão e idade dos arquivos, e uma
  nota com o raciocínio na mensagem do usuário (`nameindex.go`); `read`/`edit` de caminho inexistente
  sugerem os parecidos, `write` avisa de gêmeo próximo, busca de símbolo vazia nos atalhos sugere
  símbolos próximos; atalhos offline e classificador de tarefa reparam typos e sinônimos
  (`roda os teste`, `git stauts`, `concerta`); uma regra estática de 1 linha no system prompt.

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
