# UX do modo offline: fluxo, continuidade e recuperação

Pesquisa de UX (HCI aplicada) para o jazz, 2026-11. Escopo: o caminho offline
(`JAZZ_OFFLINE=1`, `--offline`, queda de rede, sem credencial) no interativo,
e onde ele fica atrás do caminho online em **fluxo** — o que acontece entre um
turno e outro, entre um passo e o próximo, entre um erro e a recuperação.

Máquina-alvo: **d610** — Linux i686, 32-bit, 1 CPU, 2 GB RAM, TTY possivelmente
lento (`TERM=dumb`, SSH serial, sem cor). Toda proposta aqui precisa rodar nisso
sem TUI de tela cheia: menus por número/seta, saída incremental por linhas,
zero redraw caro.

Filosofia: **inteligência em tempo de compilação**. O LLM forja os assets
(métodos em `methods/*.json`, exemplos da NLU em `nlu_examples.json`, planos
verificados em `plancache.go`, fórmulas de `nu_formulas.go`); offline o
runtime só reusa. Nenhuma proposta desta nota pode depender de chamada ao
modelo — toda a "memória", confirmação e recuperação sai de estado já
persistido (sessão JSONL, checkpoints shadow-git, tarefas) mais estruturas
baratas em RAM.

Estado atual relevante: o cérebro offline resolve a maior parte do tráfego sem
modelo (`docs/research/offline-brain-research.md` relata ~97% no corpus), mas o
fluxo em volta dele tem três buracos de continuidade:

1. **Miss → parágrafo morto.** Headless: `brNotUnderstood` imprime "não tenho
   certeza do pedido (talvez X, confiança 0.50)" e acabou (brain.go:211-219).
   Interativo: o menu genérico `olMenu` aparece (offline.go:383-397) **sem os
   candidatos que a própria NLU computou** (`in.Alt`, nlu.go:56,963-967, é
   jogado fora), e quando uma tarefa termina `failed`/`blocked`/`needs_input`
   o encerramento é texto + status line — nenhuma ação acionável (brain.go:720-
   724, methods.go:212-217).
2. **Sem diálogo entre turnos.** Cada `brRun` parseia o turno isoladamente
   (brain.go:49-69); o único estado conversacional é `a.menu`, que dura um
   turno (offers.go:334,341). "e o d610?", "agora no parser", "2" como
   resposta a uma pergunta suspensa — tudo vira pedido novo e cai no miss.
3. **O trabalho offline é invisível.** `brInteractive` não grava mensagens na
   sessão (brain.go:698-726 — só `task` events via `tkSessionSink`); logo
   `/resume` replay, `/tree`, `/rewind` picker e o título da sessão ignoram
   tudo que o cérebro fez. Online, o turno inteiro vira `a.Msgs` e é
   retomável/restaurável (agent.go:460, 2400-2414, main.go:1767-1787).

Este documento: (1) princípios de UX com fonte; (2) mapa file:line do que já
existe, online e offline; (3) 9 propostas concretas com mocks; (4) o que NÃO
fazer; (5) como medir com o harness que já existe (`jazz eval offline`,
evaloffline.go).

---

## 1. Princípios de UX (com fonte)

**P1 — Undo é uma intenção do usuário, não um comando do sistema.**
Abowd & Dix, *Giving undo attention*, Interacting with Computers 4(3), 1992
[doi:10.1016/0953-5438(92)90021-7](https://doi.org/10.1016/0953-5438(92)90021-7) ·
[PDF](https://alandix.com/academic/papers/undo92/undo.pdf).
Conclusão central: modelar undo como "reverter a última operação" falha porque
o usuário quer *estado de volta*, não *operação revertida* — e o sistema deve
mostrar o escopo do que será desfeito antes/ao desfazer. Para o jazz: `/undo`
precisa dizer **o quê volta** (turno, arquivos) — hoje restaura cego e só
depois lista paths (main.go:1752-1766, tools.go:678-690).

**P2 — Reversibilidade boa dispensa confirmação; irreversível exige atrito
proporcional.**
Nielsen, heurísticas #3 (controle/liberdade: "suporte Undo e Redo") e #5
(prevenção de erro) — [nngroup.com/articles/ten-usability-heuristics](https://www.nngroup.com/articles/ten-usability-heuristics/);
Leeman, *A formal approach to undo operations*, ACM TOPLAS 8(1), 1986
[doi:10.1145/5001.5005](https://dl.acm.org/doi/10.1145/5001.5005) (checkpoint
como primitiva formal de recovery). Como o shadow-git já torna todo turno que
escreve reversível (checkpoint.go:8-22, 466-510), o jazz pode rodar passos
mutantes com **1 confirmação no plano** em vez de confirmação por passo —
o erro se desfaz; a exceção é o que o checkpoint não cobre (fora do projeto,
symlink, `BashTracked()==false`, checkpoint.go:537-540).

**P3 — Incerteza vira pergunta barata; certeza vira ação direta.**
Horvitz, *Principles of Mixed-Initiative User Interfaces*, CHI'99
[doi:10.1145/302979.303030](https://dl.acm.org/doi/10.1145/302979.303030) ·
[PDF](https://erichorvitz.com/chi99horvitz.pdf).
Princípios diretamente aplicáveis: *considerar incerteza sobre os objetivos do
usuário* e *minimizar o custo de palpites errados*. Confiança baixa ou ação
mutante → menu de 1 tecla; confiança alta e read-only → executa. A NLU já
emite `in.Conf` e `in.Alt` — hoje só `TypoOnly` vira confirmação
(brain.go:137-145, nlu.go:986-993).

**P4 — Elipse e anáfora são a norma, não a exceção.**
Carbonell, *Discourse Pragmatics and Ellipsis Resolution in Task-Oriented
Natural Language Interfaces*, ACL 1983
[aclanthology.org/P83-1025](https://aclanthology.org/P83-1025/).
Estudo empírico: usuários de interfaces task-oriented **preferem enunciados
fragmentados mesmo instruídos a falar completo**; resolver elipse exige
caseframe — herdar predicado e slots do turno anterior, substituir só os
slots mencionados. É exatamente "e o d610?": verbo/slots herdados, `host`
trocado. Sem isso, todo follow-up natural cai no miss.

**P5 — Quebra de comunicação é normal; reparo é colaborativo.**
Hirst, McRoy, Heeman, Edmonds & Horton, *Repairing conversational
misunderstandings and non-understandings*, Speech Communication 15, 1994
[doi:10.1016/0167-6393(94)90073-6](https://doi.org/10.1016/0167-6393(94)90073-6)
· [PDF](http://ftp.cs.toronto.edu/pub/gh/Hirst-etal-1994.pdf).
Três mecanismos de reparo: *refashioning* (reformular), *suggestion*
(sugerir referente), *elaboration* (pedir esclarecimento). Traduzidos: miss
mostra top-k com descrições (suggestion), confirmação mostra "li A como B"
(refashioning), `needs_input` re-pergunta com menu (elaboration). O que nunca
pode: terminar em "não entendi" sem nenhum dos três.

**P6 — Erro bom diz o quê, por quê, e qual o próximo passo — e o próximo
passo é uma ação.**
Nielsen #9 (recognize/diagnose/recover) +
Google dev style, *Writing Helpful Error Messages*
[developers.google.com/tech-writing/error-messages](https://developers.google.com/tech-writing/error-messages)
("what went wrong?" + "how to fix it?", tom positivo, progressive disclosure).
`rvTextPT` já acerta o o-quê/por-quê em pt-BR claro por categoria
(recover.go:152-176); falta o terceiro elemento como **item de menu**, não
conselho em prosa.

**P7 — Feedback dentro dos limites 0.1s/1s/10s; acima de 10s, progresso +
trabalho em paralelo.**
Nielsen, *Response Time Limits*
[nngroup.com/articles/response-times-3-important-limits](https://www.nngroup.com/articles/response-times-3-important-limits/)
(raiz: Miller 1968, Card 1991). No d610 1-CPU, qualquer `go test` passa de
10s → a tarefa offline **precisa** emitir linha por passo e aceitar input
enquanto roda. Hoje `brInteractive` roda o plano inteiro num buffer e só
imprime no fim (methods.go:212-217 → brain.go:720-723): falha clara contra
este limite; online já cumpre (spinner + `●`/`⎿` por chamada, UX.md §7).

**P8 — Progresso honesto já melhora tempo percebido.**
Harrison, Yeo & Hudson, *Faster progress bars: manipulating perceived
duration with visual augmentations*, CHI'10
[doi:10.1145/1753326.1753556](https://dl.acm.org/doi/10.1145/1753326.1753556).
Duração *percebida* é o que importa e indicadores estruturados reduzem ~11%.
O plano do brain já é uma barra de progresso real (`t.Plan`, task.go:99-109):
imprimir `● 2/5 <step> ⎿ <result>` custa uma linha por passo e dá movimento
sem spinner falso.

**P9 — O usuário reusa; a interface deve explorar a recorrência.**
Greenberg, *The Computer User as Toolsmith*, Cambridge UP 1993
[cambridge.org](https://www.cambridge.org/core/books/computer-user-as-toolsmith/1FFFEE518175E1014E57C7B6B1493863).
Estudo de logs de csh: taxa de recorrência de comandos é alta e os últimos
enviados são os mais prováveis de repetir — histórico é affordance de
primeira classe, não arquivo escondido. jazz já honra no nível de *strings*
(Ctrl+R, prefixo `↑`, editor_palette.go:351-389); falta no nível de
*tarefas* ("refaz o de antes", continuar `needs_input`).

**P10 — Relacionamento = persistência explícita entre interações.**
Bickmore & Picard, *Establishing and maintaining long-term human-computer
relationships*, ACM TOCHI 12(4), 2005
[doi:10.1145/1067860.1067867](https://dl.acm.org/doi/10.1145/1067860.1067867)
· [PDF](https://www.ccs.neu.edu/home/bickmore/publications/toCHI.pdf).
Agentes que **declaram** o que lembram (histórico, continuidade, expectativa
futura) são mais confiáveis e mantêm engajamento. Para o jazz: o welcome e o
resume devem *mostrar* memória ("última sessão: 'arruma o parser' · 3 tarefas
verificadas · 1 ficou sem resposta"), não só carregá-la em silêncio.

**P11 — Dois golfos: mostre o que vai acontecer (execution) e o que
aconteceu (evaluation).**
Norman, *Design of Everyday Things* —
resumo NN/g: [nngroup.com/articles/two-ux-gulfs-evaluation-execution](https://www.nngroup.com/articles/two-ux-gulfs-evaluation-execution/).
Antes: preview do plano (passos, o que muta). Depois: resultado **verificado**
+ o que mudou. Offline já ganha em evaluation (status line "feito e
verificado / NÃO verificado", brain.go:482-492 — o modelo online pode
*alegar* sucesso sem evidência); perde feio em execution (plano executa sem
nunca aparecer na tela, brain.go:265-352).

**P12 — Recognition rather than recall.**
Nielsen #6 (mesmo link de P2). O usuário reconhece "run_tests" numa lista
mais fácil do que lembra a formulação que a NLU entende. Menu top-k > parágrafo
ensinando a pedir de novo. O jazz já prova que funciona: `fzOffer` para
comandos `/` ambíguos (fuzzy_cmd.go:224-239) e `olMenu` para pedidos sem match
— falta alimentar esses menus com os candidatos da NLU.

**P13 — Interatividade só onde existe TTY; toda escolha tem caminho dumb.**
Command Line Interface Guidelines, [clig.dev](https://clig.dev/)
(prompt só em TTY; `--no-input`/flags sempre bypassáveis; sem cor/animação
fora de TTY). Toda proposta precisa de caminho `TERM=dumb`/pipe: menu vira
lista numerada (cliMenu já faz isso, confirm.go:246-267), confirmação vira
`jazz -p --allow-*` headless.

**P14 — Uma tecla resolve; zero teclas quando não há escolha real.**
Síntese de P2+P3+P12 aplicada ao widget que o jazz já tem: setas/número/
enter/esc (`cliChoose`, confirm.go:203-244; menus, menu.go:103-187). O padrão
online maduro é o `⚡ atalho: cmd → [Rodar agora | Mandar pro modelo]`
(offline.go:429-435) — a mesma "1 tecla" deve existir para confirmação de
intenção, preview de plano e recuperação no offline. O que não é escolha real
(read-only de confiança alta) não gasta tecla nenhuma — o nível-0 já roda
direto (brain.go:646-693).

Referências adjacentes úteis: Shneiderman, *Eight Golden Rules* ("permit easy
reversal of actions", [cs.umd.edu/users/ben/goldenrules.html](https://www.cs.umd.edu/users/ben/goldenrules.html));
Carroll & Rosson, *The paradox of the active user*, 1987 (usuários agem antes
de entender — menus e previews têm que funcionar para quem não leu doc);
checkpointing por git em agente de terminal já é padrão de fábrica — o
próprio `checkpoint.go:12-15` cita `AmazonQ/cli checkpoint.rs` como
antecessor; JetBrains *Local History* é a referência de "undo entre
reinícios" de IDE.

---

## 2. O que o jazz já faz — mapa file:line

Capacidade × estado: **feito** / **parcial** / **só pesquisado** / **gap**.

### Fluxo de turno e confirmação

| Capacidade | Online | Offline |
|---|---|---|
| Confirmação de intenção "entendi A como B" | `⚡ atalho` + `[Rodar agora / Mandar pro modelo]` antes de rodar fast path — offline.go:429-435 · typo-only confirma via `brTypoConfirm` — brain.go:137-145 | **gap**: fast path roda sem perguntar (offline.go:419-427); typo-only confirma (mesmo código); `in.Alt` computado mas nunca mostrado (nlu.go:56,963-967) |
| Preview de plano antes de executar | `/mode plan` → resumo + `Choose("Executar o plano?")` — main.go:1357-1377 | **gap**: `t.Plan` montado e executado direto (brain.go:265-352); risco dos métodos existe no JSON (`"risk": "med"`) e nunca vai pra tela |
| Progresso por passo visível | `● Bash(...) ⎿` por tool call + spinner + fila (UX.md §7, ui_live.go) | **gap**: `brInteractive` bufferiza tudo e imprime no fim (methods.go:212-217, brain.go:720-723); só o fast path usa `Thinking`+`WatchTurn` (offline.go:441-447); steps com comando externo ecoam `$ cmd` — mas atrasado, dentro do buffer (`cx.say`, methods.go:358,461) |
| Cancelamento de turno | `a.cancelTurn` + `WatchTurn` (Esc/j → cancela modelo) main.go:1331-1354 | **parcial**: comandos shell respeitam `ctx` (env.HostRun → Bash), mas não há watcher nem linha "esc cancela"; brain não testa `ctx.Err()` entre steps |
| Multi-intenção | modelo decide | **parcial**: `brRunMulti` executa em sequência com separador `» ` (brain.go:90-109) — sem preview nem confirmação da cadeia mutante |
| Menus de espera enquanto trabalha | `olWaitOffers` após 5s: dígito roda ação durante o turno (offers.go:568-578, ui_offers.go:14-49) | **parcial**: menu aparece entre turnos (a.menu), não durante a tarefa |
| Fila de steer durante trabalho | fila com `j`/`p`/`ctrl+o`, queue.go:36-68 | **gap**: input durante `brInteractive` não é drenado — brain roda síncrono dentro do loop de leitura |

### Miss, recuperação e erro

| Capacidade | Online | Offline |
|---|---|---|
| Pedido não entendido | nunca acontece (modelo responde qualquer coisa) | **parcial**: menu genérico `olMenu` (offers.go:155-209, offline.go:383-397) — mas sem candidatos `in.Alt`; headless é parágrafo morto (brain.go:211-219) |
| Intenção entendida sem método | modelo improvisa | **parcial**: texto "entendi 'X', mas ainda não tenho um método offline pra isso" + menu (brain.go:149-154 → olOffer) |
| Recuperação de passo falho | modelo re-tenta; `agent_recover.go` propõe handoff/discard quando o turno estalla | **parcial**: `rvTable` categórica auto-resolve (recover.go:108-149) mas quando esgota vira texto morto "manda esse erro + descreve o objetivo" (brain.go:408-420) — sem menu |
| Resposta a pergunta suspensa | o modelo lembra a pergunta no histórico | **gap**: `Ask` retornando `-1` imprime lista numerada **em texto** (methods.go:1194-1217); digitar "2" no turno seguinte é parseado como pedido novo → miss |
| Erro de `!cmd` | — | **feito**: `olShellFailMenu` (offers.go:211-241, main.go:1434-1436) |
| Erro de ação do menu offline | — | **gap**: `olRun` imprime corpo com `ActionBody`; se falha, `ToolEnd(IsErr)` e nada mais — não oferece recuperação como `!cmd` faz (offers.go:351-413) |
| Você-quis-dizer | `fzOffer` com top-3 (fuzzy_cmd.go:149-239) — comandos `/` | **feito** no escopo de `/cmd`; gap para linguagem natural |

### Undo, rewind, checkpoints

| Capacidade | Online | Offline |
|---|---|---|
| Snapshot por turno | `BeginTurn(len(a.Msgs))` antes de enviar (agent.go:460); turno mutante → `cpAfterBash` (checkpoint.go:466-510); write/edit via `AfterWrite` | **feito**: `env.BeginTurn` + `env.AfterWrite` (brain.go:700, methods.go:155-172) — mas `msgIndex` não cresce offline (ver abaixo) |
| `/undo` | restaura paths do último turno que escreveu (tools.go:678-690, main.go:1752-1766); cobre bash via shadow-git | **feito** (mesma infra; `env.Undo`, método `undo.json`, methods.go:830-837) — **parcial**: cego (não mostra o que vai voltar antes), sem `/redo`, coverage de bash só avisado no "nada pra desfazer" |
| `/rewind` | picker de turnos do usuário + restaura arquivos + `Prefill` do prompt (main.go:1767-1787, confirm.go:184-195, agent.go:2416-2434) | **gap**: `UserTurns` itera `a.Msgs` — turnos de brain não entram → picker vazio ("nenhuma mensagem sua pra voltar", main.go:1770) |
| Undo entre reinícios | **parcial**: `t.turns` é RAM (tools.go:91,106-113); `Reset()` no resume (agent.go:2393-2395); árvores persistem em `dataDir()/checkpoints` (checkpoint.go:51-59,257-273) mas o mapa turno→árvore morre com o processo | igual |
| Proveniência | `jazz changes <file>` lê `refs/jazz/trace` (checkpoint_trace.go:36-151,289-340) | **feito** (commits registram passos do brain também: `brCtx.record` → sink; task.go:258-303) — parcial: sem UI interativa pra restaurar a partir do trace |

### Memória e continuidade

| Capacidade | Online | Offline |
|---|---|---|
| Contexto entre turnos | histórico completo no modelo (`a.Msgs`) | **gap**: `nuGet().Parse` por turno isolado (brain.go:49-69); `a.menu` é o único estado e dura 1 turno (offers.go:330-347); `nuWorld` só tem entidades do ambiente, não do diálogo (nlu.go:443-541) |
| Trabalho visível na sessão | tudo vira mensagem; replay completo | **gap**: `brInteractive` não appenda (brain.go:698-726); `brHeadless` appenda (brain.go:529-548) — interativo ficou pra trás; task events existem no JSONL mas não renderizam no replay (task.go:258-284) |
| Retomar sessão | `/resume`, `-c`, `/tree` com preview (sessions_ui.go:1131+,1275-1326) | **parcial**: sessão retoma mas replay não mostra o que o brain fez; tarefa `needs_input` não sobrevive |
| Reuso de planos verificados | `plancache.go` (BM25 top-3, injeta no prompt) | **só pesquisado**: métodos são o equivalente offline; cache de *execuções* verificadas (§7 de docs/design/offline-brain.md) não implementado |
| "O agente lembra de mim" na abertura | welcome mostra projeto/sessão (main.go welcome) | **parcial**: banner offline lista comandos mas não a sessão anterior nem tarefas pendentes (offline.go:198-217); footer persistente mostra só `sem rede · /online` (offers.go:673-688) |
| Verificação honesta | modelo pode alegar sucesso; `--until`/gates mitigam | **feito e à frente do online**: status line com `feito e verificado / NÃO verificado` + contagens de evidência/comando/pergunta (brain.go:482-492) |

### Resumo dos gaps, por peso

1. **Sem preview de plano mutante offline** — o pior golfo de execução:
   `run_tests` e `install_dep` recebem o mesmo tratamento visual (nenhum).
2. **Tarefa terminal não oferece caminho** — `failed`/`blocked`/`needs_input`
   fecha em texto; o usuário tem que *lembrar* que existe `/undo`, `!cmd`, etc.
3. **Zero continuidade de diálogo** — anáfora/elipse impossível; pergunta
   suspensa morre; follow-up vira miss.
4. **Undo invisível e sem redo** — existe, é bom tecnicamente, e ninguém vê;
   após `-c`/restart vira "nada pra desfazer".
5. **Progresso mudo** — tarefa multi-passo é um buraco negro de N segundos
   numa CPU fraca (viola P7 direto).
6. **Sessão cega ao offline** — todo o trabalho some do replay/rewind/título.

---

## 3. Propostas

Ordem: custo/impacto. Cada proposta lista mecanismo, mock (antes/depois),
arquivos, custo no d610, métricas no harness, impacto (1-5), esforço (S/M/L),
risco. Todos os mocks usam o vocabulário visual atual: `●`/`⎿`/`›`/`▸`/box
`╭─ Tag ─╮`/status `· x · y`/avisos `⚠`/`exit N` — nenhum elemento novo, nenhum
alt-screen, tudo funciona como lista numerada em `TERM=dumb` (cliMenu,
confirm.go:246-267).

### Prop A — Recuperação acionável no fim da tarefa (o "e agora?" automático)

**Mecanismo.** Quando `brRun` retorna status terminal não-OK
(`tkFailed`/`tkBlocked`/`tkNeedsInput` — incluindo `needs_input` não
respondido), `brInteractive` gera um `olOffer` derivado da **categoria
rv** (já classificada, recover.go:108-149) + itens do contexto da tarefa
(primeiro arquivo:linha do teste que falhou, candidatos do slot não resolvido,
`!cmd` equivalente, `/undo` se o turno escreveu, "continuar online" se a sonda
subiu). O menu persiste em `a.menu` como qualquer oferta — dígito executa,
texto normal passa (olPick, offers.go:253-277). `rvActionText`/`rvTextPT`
fornecem a frase; o item vira `olAct`/tool call real.

**Mock.**

```
ANTES (hoje):
› roda os testes
  2 testes falharam: TestParse (parser_test.go:41), TestLex (lexer_test.go:12)
  · cérebro offline: run_tests · falhou · 4 evidência(s) · 1 comando(s)
›                       ← dead end: o usuário precisa lembrar o que pedir

DEPOIS:
› roda os testes
  ● run_tests: go test ./... ⎿ exit 1 · 2 falhas
  TestParse (parser_test.go:41) · TestLex (lexer_test.go:12)
  o que faço?  1  abrir parser_test.go:41
               2  ver diff do que mudou (checkpoint)
               3  rodar de novo com -v
  · cérebro offline: run_tests · falhou · 4 evidências · 1 comando
› 2 ↵                       ← dígito roda; texto normal = pedido novo
  ● bash: git -C .jazz-checkpoints/app.git diff ... ⎿ ...
```

**Arquivos.** brain.go (`brInteractive`, ~720-726 — chama builder quando
`!ok`), offers.go (novo `olRecoverMenu(t *TaskState, cat rvCat)` ~60 linhas,
reusa `olOffer`/`olAction`), recover.go (mapa categoria→ações, ~40 linhas).

**Custo d610/TTY.** Zero novo: mesma pipeline `olMenuView` (~2 KB escritos,
nenhuma alocação quente). Nenhuma dependência.

**Métricas.** *recovery-completion* = % de tarefas `failed/blocked` seguidas
de ação do menu que termina `ok`; *abandono* = sessões fechadas ≤60s após
status ruim; questions_asked (harness já conta) não deve subir — menu não é
pergunta; false_blocks inalterado. Instrumentar via `TaskEvent` extra
(`recover_offered`, `recover_picked`) no JSONL — evaloffline já itera
`ev.T=="task"` (evaloffline.go:91-107, task.go:258-284).

**Impacto 5 · Esforço M · Risco baixo.** Menu errado é no máximo ignorado
(digitável por cima); nenhum caminho novo executa sem a ação explícita.

### Prop B — Confirmação de intenção em 1 tecla, com `in.Alt` na tela

**Mecanismo.** Em `brInteractive`, depois de `brRunIntent` montar `in`:
quando `in.Conf ∈ [nuMinConf, ~0.9)` **ou** o método resolvido tem `risk ≥
"med"` (o JSON já declara; `mhApplicable` escolhe antes de rodar), renderizar
box de confirmação via `ui.Choose`: linha-1 "entendi: <desc do intent>
(<slots>)"; opções = confirma + até 2 `in.Alt` + "não é nada disso → menu".
Confiança alta read-only permanece livre (nível-0 já é, e manter). Reusa o
padrão que `brTypoConfirm` provou (brain.go:197-209) e o widget online do
`⚡ atalho` (offline.go:429-435). Se o usuário escolher um Alt, re-rodar
`brRunIntent` com aquele `Intent` sintético — sem re-parsear.

**Mock.**

```
ANTES:
› cria o arquivo confgi.yml
  (TypoOnly já confirma hoje — mas 'reinicia o nginx' ou
   'atualiza os pacotes' rolam direto, e miss cai no menu genérico
   sem mostrar os palpites da NLU)

DEPOIS (mutante ou conf<0.9):
› instala o pacote requests
╭─ cérebro offline ────────────────────────────────────────────╮
│ entendi: install_dep · pacote 'requests'                     │
│ ▸ 1. Instalar (pip install requests)                         │
│   2. Era outra coisa? (menu do que eu sei fazer)             │
╰──────────────────────────────────────────────────────────────╯
  ↑↓ enter confirma · esc cancela — read-only nunca pergunta

DEPOIS (miss, headless vira interativo-equivalente):
› roda a bagaça
╭─ cérebro offline ────────────────────────────────────────────╮
│ não tenho certeza — você quis:                               │
│ ▸ 1. run_tests — rodar os testes do projeto                  │
│   2. build — compilar o projeto                              │
│   3. Nenhum desses (mostra o que sei fazer)                  │
╰──────────────────────────────────────────────────────────────╯
```

**Arquivos.** brain.go (`brInteractive`/`brRunIntent`, ~30 linhas + helper
`brConfirmIntent`), nlu.go (expõe `in.Alt` desc via `Desc()` — já existe),
methods.go (`mhLibrary` lookup de `Risk` por intent — já carregado).

**Custo d610/TTY.** Um `cliChoose` (~2 KB) nos casos incertos/mutantes; zero
em read-only confiante. Nenhuma syscall nova.

**Métricas.** *misroute rate* (tarefas desfeitas com `/undo` imediato ou
corrigidas em ≤1 turno — proxy de intenção errada executada) deve cair;
questions_asked vs needed do evaloffline sobe ~1 nos casos incertos — é o
comportamento *desejado*; *tempo até primeira ação* não muda em read-only.

**Impacto 5 · Esforço M · Risco médio.** Fatiga de confirmação é o risco —
mitigado pelo limiar de confiança e por só perguntar em mutante/incerto;
medir `esc` rate do box no JSONL (meta: <20%).

### Prop C — Preview do plano mutante antes de executar

**Mecanismo.** Em `brExecute`, depois que `mhApplicable` escolhe o método e
*todos os steps estão materializados* em `t.Plan` (brain.go:265-287) e **antes**
de iterar: se o plano contém ≥1 step com capability mutante (`sh.run`,
`sh.script` não-read-only, `jazz.undo`…) **ou** `method.Risk ≥ med`, imprimir
card do plano (1 linha por step: índice, descrição curta, comando resolvido,
tag `lê`/`muda`) + `Choose` de 1 tecla: `[Executar | Só explicar | Cancelar]`.
"Só explicar" imprime `m.About`/o passo a passo sem rodar. Read-only puro
(`open_file`, `git_status`, `run_tests` marcado read-only…) nunca mostra card
— P14. Equivalente offline do `Choose("Executar o plano?")` online
(main.go:1357-1377).

**Mock.**

```
ANTES:
› instala o pacote requests
  (silêncio ~8s; texto do plano inteiro aparece depois)

DEPOIS:
› instala o pacote requests
╭─ plano · install_dep: requests ──────────────────────────────╮
│  1. achar gerenciador (lockfile→pip)            lê           │
│  2. pip install requests                        muda ambiente │
│  3. python -c 'import requests'                 verifica     │
│ ▸ enter=executar · e=explicar · esc=cancelar                 │
╰──────────────────────────────────────────────────────────────╯
› enter ↵
  ● 1/3 lockfile ⎿ pip
  ● 2/3 pip install requests ⎿ exit 0 · 6.1s
  ● 3/3 import requests ⎿ ok
  · cérebro offline: install_dep · feito e verificado · 3 evidências
```

**Arquivos.** brain.go (`brExecute`, ~50 linhas entre 265-312), methods.go
(tabela cap→`lê|muda` via `cbRegistry`, ~15 linhas).

**Custo d610/TTY.** Card ~8 linhas × 70 col; os passos já estão em memória.
Sem custo de CPU mensurável.

**Métricas.** *cancel-rate* do card (se alto → planos ruins, sinal de produto);
*abandono* após preview vs. abandono durante execução cega; tempo até primeira
ação cai pro caminho que confirma (nenhum shell antes do card → negativo:
feedback em <1s em vez de ~5s de silêncio → P7).

**Impacto 5 · Esforço M · Risco médio.** O risco é dupla confirmação com a
permissão do shell (`env.Permit` → `PermitCommand`) — deduplicar: o card
substitui a primeira confirmação de comando do plano quando `--allow-*` já
daria bypass, ou marcar "este passo vai pedir permissão" na linha do step.

### Prop D — Continuidade de turno: caseframe + anáfora ("e o d610?")

**Mecanismo.** Guardar em `App` um `brLast{Intent, Slots, TaskID, Status, t}`.
Em `olBefore`/`brInteractive`, antes de parsear normal: (1) se o texto é
marcador explícito (`de novo`, `refaz`, `outro`, `agora no X`, `e o X?`,
`mesmo pro Y`) **ou** o parse falhou/`in.Conf` baixa **e** o texto traz
entidades novas (hits de `nuWorld`) — tentar **merge caseframe**: intent e
verbo herdados de `brLast`, slots herdados menos os que o texto redefine;
`brFill` roda sobre o merged. Resultado SEMPRE passa pelo box de B/C:
"entendi: run_tests, agora com host=d610 — continuo? [1 sim 2 não]". TTL
~10 min ou até 3 turnos, o que vier primeiro; só na mesma sessão; marcadores
explícitos têm precedência sobre parse bem-sucedido ("e o d610?" que parseia
como algo fraco ainda assim herda). Se merged não resolve slots → cai pro
caminho normal (miss com menu).

**Mock.**

```
ANTES:
› roda os testes
  ● run_tests ⎿ 2 falhas ...
› e no d610?
  · cérebro offline: não entendi → menu genérico

DEPOIS:
› e no d610?
╭─ continuo o de antes? ───────────────────────────────────────╮
│ entendi: run_tests, agora no host d610                       │
│ ▸ 1. Roda lá (ssh d610 'cd app && go test ./...')            │
│   2. Não — pedido novo                                       │
╰──────────────────────────────────────────────────────────────╯
› de novo                       ← marcador explícito, mesma tarefa
  ● run_tests ⎿ ...
```

**Arquivos.** brain.go (`brInteractive` + struct `brLast` no App, ~80
linhas), nlu.go (helper `nuMergeSlots(in, last) bool` ~40 linhas — slots já
são `map[string]string`), offline.go (check antes do fast path).

**Custo d610/TTY.** Uma cópia de mapa pequena + comparações de string.
Desprezível.

**Métricas.** *follow-up resolution rate*: % de turnos com marcador/anáfora
que terminam `ok` (hoje ~0). questions_avoided ≈ slots que não precisaram ser
reperguntados. *context-bleed rate*: usuário escolhe "2. não — pedido novo"
ou `esc` no box → o bleed é visível e reversível (P3); meta <10% dos merges
rejeitados.

**Impacto 5 · Esforço M · Risco médio-alto.** Herança errada é o risco clássico
de anáfora — por isso o merge nunca executa sem o box de confirmação e nunca
herda entre intents mutantes diferentes. Carbonell mostra que usuários
*aceitam* elipse — mas o preço de um merge errado tem que ser uma tecla, não
uma ação.

### Prop E — Tarefa pendente viva: `needs_input` espera resposta de verdade

**Mecanismo.** Quando `Ask` retorna `-1` (esc/fechou) ou a tarefa termina
`needs_input`/`blocked` com pergunta aberta, persistir
`a.brPending{TaskID, Intent, Question, Options[], t}` — e como `TaskEvent` na
sessão (task.go:258-284 já serializa qualquer evento; adicionar tipo
`pending`). No próximo input, antes de tudo: se o texto casa uma opção
(dígito ou prefixo — mesmo `olPick`), retomar a tarefa no slot que faltava e
continuar `brExecute` de onde parou (os steps já executados não re-rodão —
`t.Steps` persiste o estado; tarefa curta o suficiente para re-rodar read-only
se necessário). `esquece`/`esc`/texto longo limpa o pendente e segue fluxo
normal (mostrando "· pendência descartada: <task>"). Após `-c`/`--resume`,
se o último evento `task` foi `needs_input`+question, a primeira linha da
sessão é o box "ainda sobre: <goal> — <pergunta> [opções]".

**Mock.**

```
ANTES:
› build
  Qual build você quis dizer com 'build'?
  ▸ 1. go build ./...   2. make build     (esc)
  · cérebro offline: build · precisa de você · 2 evidências
› 2
  · cérebro offline: não entendi → menu    ← a resposta morreu

DEPOIS:
› build
  Qual build você quis dizer com 'build'?
  ▸ 1. go build ./...   2. make build     (esc)
  · pendente: qual build? — responda 1/2 ou peça outra coisa
› 2
  ● retomando 'build': make build ⎿ exit 0 · 4.2s
  · cérebro offline: build · feito e verificado · 2 evidências
```

**Arquivos.** methods.go (path `Ask==-1` do `choose`/`brAskSlot`, ~1194-1205,
+ `brPending` struct, ~70 linhas), brain.go (`brInteractive` guarda
`a.brPending`), task.go (evento `pending`/`resume`, ~20 linhas), offline.go
(`olBefore`: check `a.brPending` antes de `olTakePick`, ~25 linhas),
sessions_ui.go/`cmdContinue` (box de retomada, ~30 linhas).

**Custo d610/TTY.** Um struct + um append JSONL por pendência. Nada.

**Métricas.** *suspended-task completion*: % de `needs_input` que completam
dentro da sessão (hoje ≈0); *abandono pós-pergunta* cai; questions_needed
(evaloffline) finalmente corresponde às respondidas de verdade.

**Impacto 5 · Esforço M · Risco médio.** Falso-resume (usuário digitou "2"
querendo outra coisa) — mitigado porque a linha `· pendente:` fica na tela
até resolver e `esquece`/`esc` sempre sai; e a retomada passa pelo card C se
mutante.

### Prop F — Progresso ao vivo: linha por passo + "esc cancela"

**Mecanismo.** `brCtx` ganha `Progress func(step TaskStep, res string,
dur time.Duration)`; `brInteractive` injeta um renderer que, no TTY, imprime
`● i/n <desc>: <cmd-curto> ⎿ <exit/res> · <dur>` ao completar cada step e
liga `a.ui.Thinking(<desc>)` durante (reuso do spinner existente; em
`TERM=dumb` só as linhas). Rodar o brain sob o mesmo `context.WithCancel` +
`WatchTurn` do `send` (main.go:1331-1354) para `esc`/`j` cancelarem entre
steps (`ctx.Err()` testado no topo do loop, brain.go:312). O buffer final
continua: resumo/erros/perguntas imprimem uma vez no fim — progresso ≠
transcript completo.

**Mock.**

```
ANTES:
› build e roda os testes
  (~40s de nada; texto todo de uma vez)

DEPOIS:
› build e roda os testes
  ✻ build: go build ./...          (working — esc para)
  ● 1/2 build ⎿ exit 0 · 3.4s
  ● 2/2 run_tests: go test ./... ⎿ exit 1 · 8.9s · 2 falhas
  2 testes falharam: TestParse (parser_test.go:41) ...
  · cérebro offline: multi · falhou · 5 evidências · 2 comandos
```

**Arquivos.** methods.go (campo `Progress` em `brEnv`/`brCtx`, chamadas em
`brExecute`/`brStep` — ~20 linhas), brain.go (`brInteractive` liga
Thinking+renderer, ~25 linhas), main.go (`send`: envolver `a.send` offline
com `a.WatchTurn` — cuidado: olBefore roda antes do turno; mover ou duplicar
o watch, ~15 linhas).

**Custo d610/TTY.** 1 linha (~80 B) por step + spinner já existente. Em TTY
lento cada linha é barata; o spinner já tem budget de redraw.

**Métricas.** *tempo até primeira ação visível* (hoje = duração total da
tarefa; depois <1s); *abandono durante tarefa* (kill/ctrl+c no meio);
latência percebida (survey proxy; Harrison, P8). RSS/latência reais não mudam.

**Impacto 4 · Esforço M · Risco baixo-médio.** O cuidado é o `WatchTurn`
disparar `cancelTurn` no `send` — o brain já está dentro do contexto do turno
(`a.send` chamou `olBefore` dentro dele); precisa testar esc-cancela-step e
não derrubar o app.

### Prop G — Turnos offline na sessão: replay, rewind e título enxergam o brain

**Mecanismo.** Espelhar `brHeadless` (brain.go:529-548): `brInteractive`
appenda `{role:user, text}` e `{role:assistant, text:+statusline}` na sessão
quando existe — com `src:"offline"` nas `sessExtra` para o replay renderizar
compacto (`● brain(run_tests) ⎿ verificado` em vez do texto inteiro do turno
online). Consequências automáticas: `UserTurns` vê turnos offline → `/rewind`
picker ganha opções; `sessAutoTitle` funciona; `/tree` ramifica; `/export`
inclui. O `msgIndex` dos checkpoints passa a ser real offline também
(`len(a.Msgs)` cresce → `RewindTo` restaura o turno certo). `jazz changes`
já cobre os steps (trace commits existem).

**Mock.**

```
DEPOIS (jazz -c na mesma pasta):
  ✻ retomada: 9 mensagens · há 2 h
  ────────────────────────────
  › roda os testes
  ● brain(run_tests) ⎿ falhou · 2 evidências    ← turno offline, visível
  › e no d610?
  ● brain(run_tests→host=d610) ⎿ verificado
```

**Arquivos.** brain.go (`brInteractive` ~15 linhas — os appends que
`brHeadless` já faz), session.go/sessions_ui.go (render `src==offline`
compacto no replay, ~20 linhas), agent.go (nada — `UserTurns` pega de graça).

**Custo d610/TTY.** 2 appends JSONL por turno; replay +N linhas. Nada quente.

**Métricas.** % de turnos offline presentes no replay de sessões retomadas
(hoje 0%); uso de `/rewind` sobre turno offline (hoje impossível); precisão
de `msgIndex` em `RewindTo` — hoje todos os turnos brain compartilham o
mesmo `len(a.Msgs)` (que nunca cresce offline, brain.go:700), então o
rewind não distingue turno offline de turno offline; depois, restaurar "o
turno X" restaura o X certo.

**Impacto 4 · Esforço S · Risco médio.** Replay não pode poluir: por isso o
render compacto; e turnos offline não devem ir ao modelo quando voltar
online (já vão em `a.Msgs`? — atenção: appendar em `a.Msgs` faz o conteúdo
entrar no contexto do modelo na próxima request online; decidir: usar só
`sess.Append` sem `a.Msgs` e mapear `UserTurns`/`RewindTo` para a sessão, ou
appendar nos dois com flag que o builder de prompt filtra. A segunda opção é
mais simples e o custo de contexto é pequeno).

### Prop H — Undo visível: preview do escopo + `/redo` + persistência entre reinícios

**Mecanismo.** Três camadas, independentes:
(a) **Preview**: `/undo` (e o item de menu equivalente) abre um box listando o
turno que será desfeito — pedido, N arquivos, paths, e `+bash: k paths` —
com `[1 desfazer 2 escolher arquivos 3 esc]`; só então `tools.Undo()`. O
`turnSnap` já tem tudo (tools.go:106-113: `Pre`, `Post`, `Bash`,
`BashCovered`).
(b) **`/redo`**: `Undo()` empilha o `turnSnap` removido em `t.undone`;
`/redo` re-aplica `Post`/`Bash` (as árvores já estão em disco — reverter o
undo é `Restore` do estado pós). Qualquer write novo limpa `undone`
(semântica clássica de editores).
(c) **Persistência**: ao fechar `BeginTurn`/`cpAfterBash`, appendar uma linha
`{session, msgIndex, promptCurto, pre, post, paths[], bashCount}` em
`dataDir()/checkpoints/<slug>.turns.jsonl`; em `NewTools`/`LoadSessionAt`,
se a sessão bate, reidratar `t.turns` (as árvores `pre/post` persistem — o
repositório git já está em disco). `/undo` passa a sobreviver a `jazz -c` e
crash — igual Local History de IDE, mas por turno.

**Mock.**

```
ANTES:
› /undo
  · restaurado: parser.go, parser_test.go        ← descobri depois
› jazz -c ... › /undo
  · nada pra desfazer                            ← turnos eram RAM

DEPOIS:
› /undo
╭─ desfazer ───────────────────────────────────────────────────╮
│ turno "cria o parser" · 2 arquivos · checkpoint ok           │
│   parser.go        criado → vai sumir                        │
│   parser_test.go   criado → vai sumir                        │
│   + bash: gen/out.txt → vai sumir                            │
│ ▸ 1. Desfazer · 2. Escolher arquivos · esc=cancelar          │
╰──────────────────────────────────────────────────────────────╯
  · restaurado: parser.go, parser_test.go, gen/out.txt
  · /redo volta tudo
```

**Arquivos.** tools.go (`Undo` devolve preview antes; `Redo`, `undone`,
`persistTurn`/`loadTurns`, ~120 linhas), checkpoint.go (helpers de listar
árvore `post` → paths, ~40 linhas), main.go (`/redo`, `/undo` interativo,
~30 linhas), offers.go (`!undo`/`undo` no `olMenu` já existe).

**Custo d610/TTY.** Preview = `ls` da árvore persistida (já em memória? não
— `git ls-tree -r --name-only <post>` 1 fork, ~5ms no d610) ou reuso de
`t.lastBash[]` + `AfterWrite` paths gravados no turno. turns.jsonl ~150
B/turno. Aceitável.

**Métricas.** *undo usage* (se ninguém usa, invisível demais); *undo→redo
imediato* = taxa de arrependimento do undo (preview errado); "nada pra
desfazer" após `-c` → deve zerar; `false_undo` (usuário achou que voltava
mais/menos) via `esc` no preview.

**Impacto 5 · Esforço L · Risco baixo-médio.** Reidratar turnos exige cuidado
com `Reset()` (agent.go:2393-2395 zera a estrutura no resume — a proposta
depende de repovoar de `turns.jsonl` filtrado pela sessão); e `/redo` deve
ser honesto sobre bash não-coberto (`BashTracked()`).

### Prop I — Memória declarada na abertura: "o agente lembra de mim"

**Mecanismo.** Complemento barato do E+G: no welcome/`olBanner` (e na
primeira linha de `-c`), uma linha de continuidade montada de dados que já
existem — `ListSessions` (sessions_ui.go) + últimos `TaskEvent`s: `última
sessão: "<título>" · há 2 h · 3 tarefas offline verificadas · 1 pendente
(qual build?)`. Se houver pendência (E), imprimir a pergunta como menu já
na abertura. Se nunca houve sessão, nada (sem inventar). Também: footer
transient na retomada `· retomei a sessão "x" — última tarefa: run_tests ✓`
(linha única, expira em 2 turnos).

**Mock.**

```
DEPOIS:
╭──────────────────────────────────────────────────────────────╮
│ ✻ jazz  v0.9 · agente de código no terminal                  │
│   pasta   ~/projetos/app                                     │
│   sem rede → modo offline · /online quando voltar            │
│   última  "arruma o parser" · há 2 h · 3 tarefas verificadas │
│           ⚠ 1 ficou sem resposta: qual build? (1/2/esc)      │
╰──────────────────────────────────────────────────────────────╯
```

**Arquivos.** main.go (welcome/startLine + `cmdContinue`, ~30 linhas),
offers.go (`olBanner`/`olFooter`, ~20 linhas), task.go (contar últimos
eventos — helper `tkLast(sess)` ~30 linhas lendo o fim do JSONL).

**Custo d610/TTY.** Um scan do diretório de sessões (já feito em
`/sessions`) + tail do último JSONL (read de poucos KB, fseek). Nada no
caminho quente.

**Métricas.** % de sessões que retomam via a linha de continuidade;
*satisfação proxy*: o usuário continua trabalhando na mesma sessão em vez de
recomeçar — "continuation rate". Bickmore (P10): memória declarada é o que
constrói a confiança, não memória silenciosa.

**Impacto 3 · Esforço S · Risco baixo.** Nunca inventar sessão ("primeira vez
aqui" é estado válido); título da sessão pode conter path do usuário — já é
local, ok.

---

### Matriz de decisão

| Prop | Tema | Impacto | Esforço | Risco | Depende de |
|---|---|---|---|---|---|
| A — recuperação acionável | error-recovery HCI | 5 | M | baixo | — |
| B — confirmação 1-tecla + Alt | mixed-initiative | 5 | M | médio | — |
| C — preview de plano mutante | gulf of execution | 5 | M | médio | B (box) |
| D — anáfora/caseframe | continuidade | 5 | M | médio-alto | B |
| E — tarefa pendente viva | retomada | 5 | M | médio | G (idealmente) |
| F — progresso por passo | feedback 1s/10s | 4 | M | baixo-médio | — |
| G — brain na sessão | memória/rewind | 4 | S | médio | — |
| H — undo visível + redo + persistência | undo UX | 5 | L | baixo-médio | — |
| I — memória declarada | "lembra de mim" | 3 | S | baixo | E,G |

Sequência sugerida: A, G, B → C, D, E → F, I → H (H é o maior em código,
mas independe de tudo).

---

## 4. O que NÃO fazer

- **Não confirmar tudo.** Confirmação universal → `enter` cego (o
  "auto-approve" que já existia no modo permissivo é evidência desse padrão).
  Read-only de confiança alta continua livre (P14); o box só aparece quando
  há escolha real ou risco real.
- **Não executar elipse sem mostrar o merge.** Anáfora herdada invisível é
  como uma ação destrutiva silenciosa — o box "entendi: X com slot=Y" é o
  preço mínimo (P4 + P3). Se não couber numa linha, não herda.
- **Não usar prosa para pedir ação.** Parágrafo "você pode desfazer com
  /undo ou rodar !foo" no fim de erro é o dead-end de hoje com outras
  palavras: a ação tem que ser item de menu a 1 dígito (P6, P12).
- **Não depender de rede no caminho offline.** Nenhuma proposta pode disparar
  HTTP "só pra checar"; a sonda TCP já existente (`olProbe`, offline.go:160,
  loop em offers.go:587+) é o teto. Inteligência em tempo de compilação: se
  precisou de modelo, é falha de design, não falta de rede.
- **Não TUI pesada.** Nada de alt-screen, ncurses, spinner custom, box que
  redesenha a tela inteira. Orçamento: menus reutilizam `cliChoose`/menu.go;
  progresso é uma linha por step; `TERM=dumb`/`NO_COLOR`/pipe têm que sair
  iguais em conteúdo, só menos ornamentados (P13).
- **Não fingir memória.** A linha "última sessão" só aparece com dados
  reais; pendência só se o evento `needs_input` existe. Falso "lembro de
  você" corrói mais que ausência — Bickmore: memória declarada *correta*.
- **Não deixar o pendente assombrar.** `brPending` expira (TTL/turnos),
  `esquece` limpa, e sempre mostra o que está esperando. Um estado
  invisível que captura o próximo input é pior que nenhum.
- **Não restaurar o mundo inteiro no undo.** `Restore` já é por-path e pula
  symlinks/fora-do-projeto (checkpoint.go:367-414); manter exatamente isso —
  `/undo` de turno nunca vira `git reset --hard`.
- **Não esconder coverage.** Se `BashTracked()==false` ou checkpoint
  desabilitado, a tela diz — hoje só diz no "nada pra desfazer"
  (main.go:1757-1763). Coverage é parte do resultado, não rodapé de erro.
- **Não gastar RAM/disco.** Menus ≤5 itens (olMenuMax), pendência = 1
  struct, turns.jsonl append-only com cap (~100 turnos como `maxTurns`),
  nada de embeddings/BM25 ao vivo no d610 — plancache offline fica pra
  quando houver build-time index (§7 offline-brain).
- **Não acoplar ao modelo.** Box de confirmação, merge de anáfora, menu de
  recuperação — tudo determinístico. Se um dia houver modelo, ele *melhora*
  os candidatos, não *habilita* o fluxo.

---

## 5. Medição

O harness `jazz eval offline` (evaloffline.go) já mede: `verified`,
`false_success`, `block_correct`, `no_llm`, `questions asked vs needed`,
`commands/task`, latência p50/p95, `peak_rss_kb`. Estender com eventos de
UX no JSONL (mesmo `TaskEvent`, task.go:258-284 — custo zero, já iterável):

| Métrica nova | Evento | O que prova |
|---|---|---|
| `recover_offered` / `recover_picked` | Prop A | % de falhas com caminho acionado |
| `confirm_shown` / `confirm_alt_picked` / `confirm_esc` | B | cobertura da confirmação; taxa de escape = ruído |
| `plan_preview_shown` / `plan_cancelled` | C | planos que o usuário rejeitou |
| `ellipsis_merged` / `merge_rejected` | D | anáfora resolvida vs. bleed |
| `pending_set` / `pending_resumed` / `pending_dropped` | E | tarefas suspensas que completam |
| `turns_offline_in_replay` | G | continuidade entre sessões |
| `undo_preview_shown` / `undo_done` / `redo_done` | H | uso real do undo vs. descoberta |
| `ttfa_ms` (primeira ação visível) | F | ≤1s mesmo em tarefa de 40s (P7) |

Proxies de satisfação/abandono sem telemetria externa: abandono = sessão
termina ≤60s após status ruim sem ação; satisfação = taxa de retomada da
mesma sessão (I) + esc-rate dos boxes (B) + completude de pendências (E).
`false_blocks` permanece: bloquear quando podia agir continua sendo o erro
mais caro — por isso recuperação sempre oferece ação, nunca "parado".

Critério de sucesso agregado: no corpus do `evaloffline`, depois de A+B+C+E:
(1) todo turno terminal não-OK gera exatamente uma escolha acionável ou um
motivo explícito de não haver; (2) `questions_needed - questions_answered`
→ 0 dentro da sessão; (3) zero regressão em `verified`/`false_success`;
(4) `ttfa_ms` <1s em p95 no d610.
