# A experiência interativa do jazz

Passo a passo do que aparece na tela quando você roda `jazz` numa pasta, com os textos exatos.
As telas abaixo foram capturadas de uma sessão real num PTY de 86x27 (o terminal do Dell), com um
modelo falso (`fake/m1`) e o sandbox ativo; só o caminho da pasta foi trocado por
`~/projetos/app`. Cores e negrito não aparecem aqui.

Sumário: [início](#1-início) · [modos](#2-modos) · [caixa de entrada](#3-caixa-de-entrada) ·
[paleta](#4-paleta-de-comandos) · [modelos](#modelo-model-e-omni) · [@arquivo](#5-arquivo) · [perguntas](#6-perguntas) ·
[trabalho e fila](#7-enquanto-o-agente-trabalha) · [sessões](#8-sessões) ·
[atalhos](#9-atalhos) · [configuração](#10-o-que-dá-pra-mudar)

## 1. Início

### Primeira vez (nenhuma chave)

Sem `config.json` e sem chave nenhuma (variável de ambiente, `auth.json` ou login do ChatGPT), o
`jazz` abre três passos antes de tudo. `jazz setup` abre os mesmos passos quando quiser.

```
  ✻ bem-vindo ao jazz  três passos e você está programando (jazz setup refaz)

╭─ 1/3 · como rodar ────────────────────────────────────────────────────────────────╮
│ Como você quer rodar o jazz?                                                      │
│ ▸ 1. Grátis (pool de modelos grátis)  sem chave, começa já; a qualidade varia     │
│   2. Chave da Anthropic (Claude)  pago; a chave começa com sk-ant-                │
│   3. Chave da OpenAI  pago; a chave começa com sk-                                │
│   4. Login do ChatGPT (Plus/Pro)  usa o seu plano, sem chave                      │
│   5. Ollama local (no seu computador)  sem internet nem chave; precisa do ollama… │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ↑↓ ou nº escolhe · enter confirma · esc pula (fica no grátis)
```

Passo 2: a chave é colada escondida (só aparecem 12 pontos e o tamanho) e guardada como no
`jazz key`; o login do ChatGPT mostra o código; o Ollama lista os modelos baixados numa lista.
Passo 3: um pedido real mínimo (o mesmo de `jazz doctor --provider`), com o resultado na tela:

```
  ● 1/3 · Chave da OpenAI
  crie a chave em https://platform.openai.com/api-keys
  2/3 · cole a chave de openai (fica escondida) › •••••••••••• (28 caracteres)
  · chave de openai salva (…fXYZ) em ~/.config/jazz-cli/auth.json
  3/3 · testando openai/m1…
  provedor openai (openai) · https://api.openai.com/v1
  modelo   m1
  chave    …fXYZ (de ~/.config/jazz-cli/auth.json)
  ✓ HTTP 200 em 13 ms · stop stop · 0+0 tokens · resposta: ok
  · pronto: modelo openai/m1 (salvo como padrão) · /model troca quando quiser
```

Se o teste falha: `Escolher outra opção` (volta ao passo 1) ou `Seguir assim mesmo`. Esc no passo 1
fica no grátis e grava a config (não abre de novo sozinho). Sem terminal (`jazz -p`, pipe) nada
disso aparece; `JAZZ_ONBOARD=off` desliga.

### Pasta nova

Na primeira vez numa pasta, antes de qualquer coisa do projeto carregar, aparece o menu de
confiança. Pasta sem configuração própria recebe a versão curta:

```
╭─ pasta nova ──────────────────────────────────────────────────────────────────────╮
│ ~/projetos/app                                                                    │
│                                                                                   │
│ Confiando, a sessão roda no modo turbo. Sem confiar, no modo ask (pergunta antes… │
│                                                                                   │
│ Primeira vez nesta pasta. Confiar?                                                │
│ ▸ 1. Sim, confiar nesta pasta  lembrado nas próximas vezes                        │
│   2. Só nesta sessão                                                              │
│   3. Não, modo ask  pergunta antes de cada mudança                                │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ↑↓ escolhe · enter confirma · esc não
```

- Enter (ou `s`/`y`/`1`): `● pasta confiável: ~/projetos/app`, lembrado nas próximas vezes.
- `2` (ou `o`): `● pasta confiável só nesta sessão: ~/projetos/app`.
- `3`, `n` ou Esc: `● pasta não confiada: modo ask nesta sessão (/trust confia)`.

Pasta que traz configuração (AGENTS.md, `.mcp.json`, hooks, scripts, steering) recebe a caixa
`─ confiança ─` com o título `Confiar nesta pasta e ativar a configuração dela?`, a frase
`Este projeto traz configuração que roda comandos, libera ferramentas ou dá instruções ao modelo:`,
a lista do que vai carregar e `Só confie se você conhece este repositório.`; `v` mostra a lista
inteira. Com `"trust_default": "always"` a pasta sem configuração é confiada sem perguntar.

Teclas nos primeiros 250 ms depois que o menu abre são ignoradas (uma tecla que já vinha no buffer
não responde por você).

### Boas-vindas

```
╭───────────────────────────────────────────────────────────────────────────────────╮
│ ✻ jazz  v0.8.0 · agente de código no terminal                                     │
│                                                                                   │
│   modelo  fake/m1                                                                 │
│   pasta   ~/projetos/app                                                          │
│   modo    » turbo                                                                 │
│            sandbox ativo: roda sem perguntar o que fica no projeto; pergunta fora │
│            dele, rede nova, segredos, git destrutivo e pacote suspeito            │
│                                                                                   │
│   /help ajuda  ·  esc interrompe  ·  @ menciona arquivo                           │
│   ! roda no shell  ·  shift+tab troca o modo  ·  ? atalhos                        │
╰───────────────────────────────────────────────────────────────────────────────────╯

╭───────────────────────────────────────────────────────────────────────────────────╮
│ ›                                                                                 │
╰───────────────────────────────────────────────────────────────────────────────────╯
  » turbo · fake/m1 · ? atalhos
```

A linha embaixo de `modo` explica o modo de início **uma vez por modo** (a config guarda
`mode_intro`); nas próximas vezes ela some. Os textos, por modo:

| Modo | Explicação na primeira vez |
|---|---|
| turbo (com sandbox) | `sandbox ativo: roda sem perguntar o que fica no projeto; pergunta fora dele, rede nova, segredos, git destrutivo e pacote suspeito` |
| turbo (sem sandbox) | `modo turbo sem sandbox do sistema (sandbox-exec/bwrap): usando as regras do auto (pergunta em comando perigoso, saída de dados e fora do projeto)` |
| auto (com sandbox) | `pergunta antes de comando perigoso, rede nova e fora do projeto · /mode turbo pergunta menos (sandbox ativo)` |
| auto (sem sandbox) | `pergunta antes de comando perigoso, rede nova e fora do projeto (sem sandbox do sistema: turbo indisponível)` |
| ask | `pergunta antes de cada mudança (arquivo, comando, rede)` |
| plan | `só investiga e planeja; nada muda até você aprovar o plano` |
| smart | `um modelo rápido decide o que precisa de confirmação` |
| yolo | `não pergunta nada: use só em pasta descartável` |

Depois de visto, se você está em `auto` com sandbox disponível, a linha do modo mostra
`· recomendado: /mode turbo (sandbox ativo, só pergunta o necessário)`.

Abaixo de 40 colunas a caixa vira uma linha: `✻ jazz fake/m1 · ~/projetos/app`. Com
`--banner` ou terminal de 100+ colunas aparece o logo grande.

Com `jazz -c` ou `jazz -r <id>` a ordem é: menu de confiança (se for o caso), boas-vindas,
conversa retomada, aviso `· sessão <id> retomada (N mensagens): <título>`. Com `jazz -r` sem id,
a [lista de sessões](#8-sessões) abre logo abaixo das boas-vindas.

## 2. Modos

O padrão, sem `mode` na config: **turbo** quando o sandbox do sistema isola (`sandbox-exec` no
macOS, `bwrap` no Linux), senão **auto**. `jazz -p` é sempre `auto`. Um `"mode"` na config vence
o padrão (configs antigas que gravaram `"mode": "auto"` ficam em auto até você apagar a linha ou
usar `/mode turbo`). Pasta não confiada roda em `ask`.

O modo aparece no rodapé, sempre:

| Rodapé | Modo |
|---|---|
| `» turbo · fake/m1 · ? atalhos` | turbo |
| `▸▸ auto · fake/m1 · ? atalhos` | auto |
| `‖ plano · fake/m1 · ? atalhos` | plan |
| `▸ ask · fake/m1 · ? atalhos` | ask |

Shift+Tab (ou Ctrl+T) troca só nesta sessão, no ciclo turbo → auto → plan → ask → turbo. Sem
sandbox o turbo sai do ciclo (seria igual ao auto). `smart` e `yolo` só com `/mode smart`,
`/mode yolo` (ou `--smart`, `--yolo`). `/mode <modo>` troca e grava na config.

Depois do contexto usado, o rodapé ganha `ctx N%` (`» turbo · fake/m1 · ctx 1% · ? atalhos`). Em
terminal estreito ele encolhe: some primeiro o MCP, depois o contexto, o provedor e o modelo; o
modo e `? atalhos` ficam.

No fim do modo `plan` aparece um menu:

```
Executar o plano?
▸ 1. Sim, executar (modo turbo)
  2. Sim, perguntando antes de cada mudança (modo ask)
  3. Não, continuar planejando
```

e, ao escolher, `· modo turbo: executando o plano`.

## 3. Caixa de entrada

A caixa cresce com o texto. Enter envia; Shift+Enter, Alt+Enter ou `\`+Enter quebram linha (em
roxterm/VTE o Shift+Enter costuma chegar como Enter comum: use `\`+Enter ou Alt+Enter). Colar
texto de várias linhas mantém tudo como está, inclusive indentação e tabs.

`?` com a linha vazia mostra os atalhos embaixo da caixa (em duas colunas quando cabem); `?` de
novo esconde:

```
  ! roda no shell                     @ menciona arquivo
  / comandos                          esc limpa · 2× interrompe
  ↑ ↓ histórico (filtra pelo início)  ctrl+r busca no histórico
  shift+enter quebra linha (\ enter)  shift+tab troca o modo
  ctrl+o expande/recolhe a saída      ctrl+g abre no editor
  ctrl+u ctrl+k apaga até início/fim  alt+← → pula palavra
  ctrl+c 2× sai
```

Histórico: ↑↓ mostram só as entradas que começam pelo que já está digitado (com a linha vazia,
todas). Num texto de várias linhas, ↑↓ andam entre as linhas antes de ir ao histórico. Ctrl+R
abre a busca no lugar da caixa:

```
(busca) '': oi tudo bem  ctrl+r anterior · enter usa · esc cancela
```

Digitar refina; Ctrl+R de novo vai para o resultado anterior; sem resultado aparece
`nada encontrado`; Enter põe o resultado na caixa (não envia); Esc cancela.

`!comando` roda direto no shell, sem passar pelo modelo.

## 4. Paleta de comandos

`/` abre a paleta logo embaixo da caixa:

```
╭───────────────────────────────────────────────────────────────────────────────────╮
│ › /                                                                               │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ▸ /help         esta ajuda
    /model        escolhe o modelo numa lista (↑↓ ou nº, digite filtra) · /model anthr…
    /models       lista modelos de um provedor: /models openrouter
    /omni         roteamento entre provedores: /omni [on|off|strategy free-first|wei…
    /key          salva uma chave de API (escondida): /key openrouter
    /login        login com a conta do ChatGPT (--browser pra fluxo no navegador)
    … +35   ↑↓ escolhe · enter usa · tab completa · esc fecha
```

Digitar filtra com busca aproximada, e o nome exato vem primeiro:

```
│ › /cmp                                                                            │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ▸ /compact  compacta o contexto agora (opcional: foco do resumo)
    ↑↓ escolhe · enter usa · tab completa · esc fecha
```

`/modle` + Enter roda `/model`; `/mode` + Enter roda `/mode` (não `/model`). ↑↓ movem a
seleção, Tab completa na caixa com um espaço no fim (`› /models `), Enter roda, Esc fecha a
paleta (outro Esc limpa a linha). Comandos próprios (`.md` em `~/.config/jazz-cli/commands`,
`.jazz/commands`, `.claude/commands`) entram na mesma lista.

### Modelo: /model e /omni

`/model` sozinho abre a lista de modelos, agrupada por provedor: o provedor atual primeiro, depois
o omni, os que têm chave, os locais e os sem chave (só o modelo padrão de cada um):

```
› /model
  modelo atual: fake/m1 · escolha outro (↑↓, nº ou digite pra filtrar)
  fake
  ▸  1 fake/m1  pago  atual
     2 fake/m2  pago
     3 fake/m3  pago
  omni · todos os seus provedores, grátis primeiro
     4 omni/coder  roteado
     …
  kilo · grátis (pool, sem chave)
     9 kilo/poolside/laguna-s-2.1:free  grátis
  anthropic · sem chave (enter pede a chave)
    15 anthropic/claude-sonnet-5-5  pago
  chatgpt · sem login (enter faz o login)
    17 chatgpt/gpt-5.5-codex  assinatura
  1/33 … +16  ↑↓ ou nº escolhe · enter abre · esc cancela · digite filtra
```

- ↑↓ (j/k) movem; Enter escolhe e grava como padrão (`· modelo: fake/m2 (salvo como padrão)`).
- Número: até 9 linhas, o dígito escolhe na hora; com 10 ou mais, o primeiro dígito marca a linha
  e espera (`nº 1_ (outro dígito ou enter)`): `1` `2` = linha 12, `1` Enter = linha 1. Um número
  que não pode crescer (`3` numa lista de 20) escolhe direto.
- Digitar filtra (`/` antes, para procurar j, k ou dígitos). Esc limpa o filtro; outro Esc cancela
  (`· modelo atual: fake/m1 (nada mudou)`).
- Cada linha diz `grátis`, `pago` (com o preço por milhão quando o catálogo diz), `assinatura`, o
  contexto e a latência medida (média dos pedidos que deram certo).
- Escolher um modelo `sem chave` pede a chave ali mesmo (escondida, como `/key`); `sem login`
  abre o login do ChatGPT; um servidor local que não respondeu avisa como iniciar.
- `/model provedor/modelo` continua trocando direto. Sem terminal (ou `session_picker: off`) volta
  a lista simples.

`/omni` sozinho abre um menu: ligar/desligar o omni, e um modelo por papel (Planejador, Editor,
Verificador). O papel abre a mesma lista, com `auto` (o omni escolhe por tarefa) em primeiro.
`/omni status`, `/omni on`, `/omni strategy …` continuam como antes.

## 5. @arquivo

`@` abre a lista de arquivos e pastas do projeto, com busca aproximada sobre o índice de nomes:

```
│ › olha @ser                                                                       │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ▸ cmd/server/server.go
    cmd/server/
    ↑↓ escolhe · enter usa · tab completa · esc fecha
```

Enter ou Tab inserem `@cmd/server/server.go ` na caixa. Escolher uma pasta mantém a lista aberta
dentro dela. O arquivo mencionado vai junto com a mensagem.

## 6. Perguntas

Toda pergunta é um menu numa caixa. ↑↓ (ou j/k) e Enter escolhem, o número escolhe direto e cada
menu tem atalhos de uma tecla. Enter é sempre a opção destacada. Nunca é preciso digitar `sim`.

### Permissão

```
› rode perigoso

╭─ Bash ────────────────────────────────────────────────────────────────────────────╮
│ git branch -D jazz-pty-nada                                                       │
│                                                                                   │
│ ⚠ apaga branch do git                                                             │
│                                                                                   │
│ Rodar este comando?                                                               │
│ ▸ 1. Sim                                                                          │
│   2. Sim, e não perguntar de novo neste projeto para git: git branch -D jazz-pty… │
│   3. Não, e me diga o que fazer                                                   │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ↑↓ enter · s sim · a sempre · n/esc não · e instruir · v ver tudo
```

- A caixa mostra o comando exato (ou o diff da edição) e o motivo de risco inteiro em vermelho,
  uma linha por problema; nunca é cortado. `v` imprime o comando, o diff e o motivo inteiros
  acima do menu, que continua aberto.
- `s`/`y`/`1` sim · `a`/`t`/`2` sempre · `n`/Esc não · `e`/`3` não e escrever uma instrução para
  o agente.
- Tecla nos primeiros 250 ms, ou enquanto você ainda está digitando (sem uma pausa de 600 ms),
  não responde. Isso não muda o que Enter quer dizer.
- Depois da resposta a caixa some e fica uma linha: `● sim · Bash git branch -D …` ou
  `● não · Bash git branch -D jazz-pty-nada`, e o resultado da ferramenta mostra `⎿  negado`.

O que a opção 2 lembra (`always_scope: auto`): edição/escrita vale nesta sessão
(`nesta sessão para Edit(<pasta>/**)`); comando, domínio, ferramenta MCP e script viram regra do
projeto em `.jazz/settings.json` (`neste projeto para Bash(npm test:*)`); um motivo de risco vai
para `.jazz/approvals.json` (`neste projeto para <classe>: <alvo>`). Ler segredo, comando
catastrófico, regra `ask`, hook e injeção suspeita perguntam sempre.

### Várias confirmações de uma vez

Quando uma resposta do modelo traz duas ou mais ações que precisam de confirmação, elas vêm numa
caixa só, numerada, com o risco de cada uma em vermelho:

```
╭─ 2 confirmações ──────────────────────────────────────────────────────────────────╮
│ 1. Bash  git branch -D jazz-pty-a                                                 │
│    ⚠ apaga branch do git                                                          │
│ 2. Bash  git branch -D jazz-pty-b                                                 │
│    ⚠ apaga branch do git                                                          │
│                                                                                   │
│ Fazer estas 2 ações?                                                              │
│ ▸ 1. Sim, as 2                                                                    │
│   2. Uma por uma                                                                  │
│   3. Não, nenhuma                                                                 │
╰───────────────────────────────────────────────────────────────────────────────────╯
  enter sim pra tudo · u uma por uma · n/esc nenhuma · v ver tudo
```

Enter = sim para todas (`● sim pra tudo · 2 ações`); `u` volta às perguntas uma por uma; `n` ou
Esc recusa todas. Cada ação continua passando pelas regras, guardas e auditoria dela; só a
pergunta é juntada. Não vale nos modos `smart`, `yolo` e `plan`. `JAZZ_APPROVE_BATCH=off` desliga.

### Perguntas que o projeto responde

Uma `ask_user` que pergunta um fato do projeto não chega até você: branch atual (git), comando de
teste (`go.mod`, `package.json`, `Cargo.toml`, pytest, Makefile…), gerenciador de pacotes (pelo
lockfile) e "o arquivo X existe?". A linha mostra de onde veio:

```
● Pergunta(Qual o comando de teste do projeto?)
  ⎿  respondida pelo projeto (go.mod): go test ./...
```

Pergunta de preferência ou intenção ("qual banco você prefere?", "quer que eu crie uma branch?")
continua indo para você. Com opções, o fato precisa bater com uma delas. `JAZZ_ASK_FACTS=off`
desliga. O prompt de sistema também pede ao modelo para checar esses fatos com uma ferramenta,
juntar as perguntas numa só e mostrar o plano uma vez.

### `ask_user`

```
● Pergunta(Qual banco usar?)

╭─ pergunta ────────────────────────────────────────────────────────────────────────╮
│ Qual banco usar?                                                                  │
│ ▸ 1. SQLite  arquivo local                                                        │
│   2. Postgres                                                                     │
│   3. Outra resposta (escrever)                                                    │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ↑↓ enter escolhe · ou comece a digitar a resposta · esc pula
```

Depois: `● Qual banco usar?  SQLite` e `⎿  resposta: SQLite`.

### Outras

Usam o mesmo menu: jobs em segundo plano ao sair (`N job(s) em segundo plano rodando. Encerrar?`
com `Sim, encerrar e sair` / `Não, sair e deixar rodando`) e no `/clear` (`Sim, encerrar` /
`Não, deixar rodando`); `/forget` (`Apagar do disco a sessão <id>?`, `Sim, apagar` / `Não`); o
atalho determinístico `⚡ atalho`; `/rewind`; e, fora da sessão, `jazz trust`,
`jazz worktree merge` e `jazz improve` (sem terminal, eles caem para uma pergunta em linha).
Um menu cancelado com Esc deixa `● cancelado`.

## 7. Enquanto o agente trabalha

A resposta aparece com `●` e markdown quebrado na largura; cada ferramenta como
`● Bash(go test ./...)` com o resultado em `⎿` (até 3 linhas; Ctrl+O expande e recolhe). Uma
ferramenta que levou 1 s ou mais mostra o tempo no fim da linha: `● Bash(go test ./...) 2.1s`.
Edição mostra o diff com `-` vermelho e `+` verde (fundo tingido só com 256 cores ou mais). No
fim do turno, uma linha de totais: `✻ 1.1s · 1 ferramenta · ctx 1%`.

O rodapé da caixa mostra também `grátis` (modelo grátis) ou o custo da sessão (`$0.031`); é a
primeira coisa a sumir quando falta espaço.

A linha de status (spinner, tempo, tokens) é feita pra SSH lento: entre um segundo e outro só o
caractere do spinner é redesenhado, e o texto que chega na mesma linha é escrito no fim dela, sem
reescrever o resto. Medido em `ui_bench_test.go` (`TestUIRedrawBudget`, 86 colunas): esperando o
modelo ~310 B/s (limite 600), streaming a 30 tokens/s ~1,1 KB/s além do próprio texto (limite
2,5 KB/s). Sem terminal não há spinner.

Você continua digitando durante o trabalho. Enter não interrompe: a mensagem entra na fila,
mostrada embaixo da linha de status (`na fila (2): …`, um item por linha, com a dica
`↑↓ editar · del remove · tab agora/depois · esc esc limpa`). Mensagem curta (até 200 caracteres,
ou começando com `!`, `ei`, `opa`, `na verdade`, `espera`) entra **agora**, na próxima fronteira
de ferramenta; mensagem longa (ou começando com `depois:` ou `+`) vira o **próximo turno**. Se uma
pergunta abre enquanto você digitava, o texto aparece acima da caixa (`↳ na fila: …`) e volta para
a caixa de entrada depois.

Esc: com texto digitado, o primeiro Esc limpa esse texto e o segundo interrompe; sem texto, um Esc
já interrompe (`interrompendo…`). Ctrl+C interrompe sempre. Turno interrompido devolve a fila
para a caixa.

Ctrl+O expande a saída inteira da última ferramenta; Ctrl+O de novo recolhe, se a saída expandida
ainda é a última coisa na tela (senão expande de novo). `/expand` só expande.

## 8. Sessões

Fora da sessão: `jazz sessions` no terminal abre a mesma lista (↑↓ ou o número) e retoma a
escolhida; `jazz sessions --list` (ou sem terminal, ou com `--json`, `-n`, `--all`) imprime a
tabela. `jazz resume 2` retoma a segunda mais nova (o número da lista), `jazz resume <id>` uma
sessão pelo id, `jazz resume` sozinho abre a lista; `--full` mostra a conversa inteira sem
paginar. Sem terminal, `jazz resume` imprime a conversa em texto e sai (não pergunta nada).

`/resume`, `/sessions` e `jazz -r` sem id abrem a lista no lugar, mais nova primeiro:

```
› /resume
  sessões em ~/projetos/app
  ▸ 1 Tudo bem  agora  atual
        turbo · 2 msgs · concluída · ~/projetos/app
    2 Rode perigoso  agora
        turbo · 8 msgs · concluída · ~/projetos/app
  1/2  ↑↓ ou nº escolhe · enter abre · esc cancela · digite filtra · tab: todas as pastas
```

- Cada linha: título, data relativa (`agora`, `há 5 min`, `há 2 h`, `ontem`, `há 3 dias`,
  `12 out`), `atual` na sessão em uso; embaixo modo · mensagens · tokens · custo · situação
  (`concluída`, `incompleta`, `negada`) · pasta.
- ↑↓, j/k, PgUp/PgDn movem; Enter abre; o número abre direto (com 10 ou mais linhas, dois
  dígitos: `1` `2` = linha 12, ou o dígito e Enter); digitar filtra (tolerante a erro de
  digitação; `/` começa um filtro para procurar j, k ou dígitos); Esc limpa o filtro, e com o
  filtro vazio cancela; Tab mostra todas as pastas, agrupadas. Sessão de outra pasta não é
  carregada: `essa sessão é de ~/outra — abra lá: jazz -C ~/outra -r <id>`.
- A lista é apagada ao fechar. `/resume <id>` abre direto.

Ao abrir, a conversa é desenhada de novo, numa escrita só, sem rodar nada:

```
› rode perigoso

● Bash(git branch -D jazz-pty-nada)
  ⎿  BLOCKED: bash `git branch -D jazz-pty-nada` — the user denied it (it is a destr…

● pronto: 300 linhas.

› pergunte algo

● Pergunta(Qual banco usar?)
  ⎿  The user answered: SQLite

● pronto: 300 linhas.

  ✻ conversa anterior · 8 mensagens · 2 ferramentas
  · sessão 261021-170442-e529 retomada (8 mensagens): Rode perigoso
```

Conversa maior que a tela é mostrada uma página por vez, sem tela alternativa (o que você leu fica
no histórico do terminal):

```
› PEDIDO-07 sobre o parser
 20% · espaço próxima página · enter +1 linha · q pula pro fim
```

Espaço (ou PgDn, `f`) mostra a próxima página, Enter (↓, `j`) mais uma linha, `q` (Esc, `G`) pula
para o fim: aparece `… 49 linhas puladas (/expand mostra)` e a última meia tela, com o aviso de
sessão retomada embaixo. `/expand` mostra a conversa toda. Com `jazz resume --full`, sem
terminal ou em `TERM=dumb`, tudo sai de uma vez.

Cada ferramenta vira uma linha verde ou vermelha a partir do resultado gravado (o texto é o que o
modelo recebeu, por isso pode estar em inglês). Só as últimas 200 entradas aparecem, com
`… (N mais acima, /expand)` em cima; `/expand` mostra o resto (`resume_replay`: número, `all`,
`off`).

Títulos: 5–8 palavras do primeiro pedido, sem chamar modelo. `/rename banco de dados do app`
troca (`· sessão 261021-170442-e529: banco de dados do app`); `/rename` sozinho abre o título
atual para editar.

`/tree` mostra a árvore da sessão na mesma lista:

```
› /tree
  árvore da sessão · enter continua dali (novo ramo)
    1 ● você · rode perigoso  23996a59
    2 ● jazz · [bash]  7ae2450c
    3 ● jazz · pronto: 300 linhas.  2f393c77
    4 ● você · pergunte algo  48798e3f
    5 ● jazz · [ask_user]  4f3e2119
  ▸ 6 ● jazz · pronto: 300 linhas.  f80f0742  atual
  6/6  ↑↓ ou nº escolhe · enter abre · esc cancela · digite filtra
```

Enter continua daquela mensagem como um ramo novo, e o ramo é desenhado de novo.

`/rewind` volta a conversa (e os arquivos) para antes de uma mensagem sua:

```
╭─ mensagens ───────────────────────────────────────────────────────────────────────╮
│ Voltar para antes de qual mensagem?                                               │
│ filtrar: ▏                                                                        │
│   rode perigoso                                                                   │
│ ▸ pergunte algo                                                                   │
╰───────────────────────────────────────────────────────────────────────────────────╯
  ↑↓ escolhe · digite pra filtrar · enter abre · esc cancela
```

## 9. Atalhos

| Tecla | Na caixa de entrada | Durante o trabalho | Em menus e listas |
|---|---|---|---|
| Enter | envia | põe na fila | escolhe a opção destacada |
| Shift+Enter · Alt+Enter · `\`+Enter | quebra linha | — | — |
| Esc | fecha paleta/lista; limpa a linha | limpa o texto digitado; sem texto, interrompe | não / cancela (lista: limpa o filtro primeiro) |
| Ctrl+C | limpa; 2× sai | interrompe | cancela |
| Ctrl+D | sai (linha vazia) | — | — |
| ↑ ↓ (j k em menus) | histórico filtrado pelo início | editam a fila | movem |
| PgUp PgDn | — | — | movem uma página |
| 1–9 | — | — | escolhem direto (listas com 10+: dois dígitos, ou dígito + Enter) |
| Espaço · Enter · `q` | — | — | conversa retomada: próxima página · +1 linha · pula pro fim |
| `/` | paleta de comandos | — | começa um filtro (lista de sessões) |
| `@` | lista de arquivos | — | — |
| `!` | roda no shell | — | — |
| `?` | lista de atalhos (linha vazia) | — | — |
| Tab | completa (paleta, @, comando) | alterna agora/depois na fila | lista de sessões: todas as pastas |
| Shift+Tab · Ctrl+T | troca o modo | — | — |
| Ctrl+R | busca no histórico | — | — |
| Ctrl+O | expande/recolhe a última saída | — | — |
| Ctrl+G | abre a mensagem no `$EDITOR` | — | — |
| Ctrl+A/E · Ctrl+U/K · Ctrl+W | início/fim · apaga até início/fim · apaga palavra (por linha) | — | — |
| Alt+← → (Option no macOS) | pula palavra | — | — |
| `s` `a` `n` `e` `v` | — | — | permissão: sim · sempre · não · instruir · ver tudo |

## 10. O que dá pra mudar

| Config (`config.json`) | Variável | Padrão | Efeito |
|---|---|---|---|
| `mode` | — | (vazio) | fixa o modo de início; vazio = turbo com sandbox, senão auto |
| `trust_default` | — | `prompt` | `always` confia sem perguntar em pasta sem configuração própria |
| `always_scope` | — | `auto` | `session` / `project`: onde "não perguntar de novo" fica |
| `session_picker` | `JAZZ_SESSION_PICKER` | `on` | `off` volta à lista numerada |
| `resume_replay` | `JAZZ_RESUME_REPLAY` | `200` | número, `all` ou `off` |
| `ui.palette` | `JAZZ_PALETTE` | `fuzzy` | `prefix` filtra só pelo início do nome |
| `ui.mention_picker` | `JAZZ_MENTION_PICKER` | `on` | `off` desliga a lista do `@` |
| `ui.history_prefix` | `JAZZ_HISTORY_PREFIX` | `on` | `off`: ↑↓ percorre o histórico todo |
| — | `JAZZ_ASCII=1` / `TERM=linux` | — | tudo em ASCII (`cima/baixo`, `2x`) |
| — | `TERM=dumb` | — | ASCII, sem status ao vivo, entrada sem caixa, menus impressos uma vez e respondidos pelo número |
| — | `NO_COLOR` | — | sem cores (com 16 cores, os papéis usam os slots ANSI da paleta Nord) |
| — | `JAZZ_ONBOARD=off` | — | a primeira execução não abre os três passos (`jazz setup` abre sempre) |
| — | `JAZZ_APPROVE_BATCH=off` | — | uma confirmação por ação, mesmo várias na mesma resposta |
| — | `JAZZ_ASK_FACTS=off` | — | toda `ask_user` vai para você, mesmo as que o projeto responde |

Nada disso entra no system prompt: o cache de prompt continua estável entre turnos.
