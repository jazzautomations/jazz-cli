# Confirmação de intenção e desambiguação: a UX do "entendi X como Y" no modo offline

Pesquisa de 04/11/2026 para a UX offline do jazz (máquina-alvo d610: Linux i686, 1 CPU, 2 GB,
TTY possivelmente lento/sem cor). Tema: como o cérebro offline (`brain.go` + `nlu.go` +
`methods.go`) confirma o que entendeu antes de agir, resolve ambiguidade com a menor pergunta
possível, e converte o honest-miss em próxima ação — em vez do parágrafo morto de hoje.

Convenções (igual `offline-brain-research.md`): cada afirmação traz fonte primária ou `file:line`
do código. **(estimativa)** marca número meu, não medido. "Custo" assume o d610: 1 núcleo,
stdout com latência de serial/SSH, sem garantia de cor nem de Unicode.

---

## 1. O problema em três telas (estado atual)

**Miss interativo hoje.** O usuário pede algo fora da gramática; `brRun` devolve
`Handled=false` e `brInteractive` retorna `false` sem imprimir nada (brain.go:717-719);
`olBefore` cai então no `olOffer` genérico (offline.go:382-397) que só conhece arquivos/termos
do texto cru — a hipótese do NLU (`in.Name`, `in.Alt`, `in.Confidence`) é **descartada**:

```
› refatora o login pra usar JWT

  ⚠ offline: nenhum provedor alcançável — o que dá pra fazer sem modelo:
    1  procurar 'login' no código     rg -n --no-heading -S -- 'login' . | head -60
    2  rodar os testes e listar falhas   go test ./...
    3  mostrar git status/diff resumido  git status --short --branch && git diff --stat
    4  resumo do projeto
    5  /online — tentar o modelo de novo
  digite o número (ou o começo da opção); qualquer outro texto vai como pedido normal
```

Nem "não entendi" aparece. O menu é acionável, mas genérico: não usa `in.Name` ("talvez
explain") nem `in.Alt` — que `nlu.go` **já calcula** (nlu.go:56, 946-948, 963-964) e ninguém
consome em lugar nenhum do repositório.

**Miss headless hoje** (`jazz -p --offline`): `brNotUnderstood` devolve um parágrafo de
capacidades (brain.go:211-219) ou `"não tenho certeza do pedido (talvez %s, confiança %.2f)"`
— texto terminal, zero ação:

```
não entendi o pedido sem o modelo. Posso: listar e buscar arquivos, resumir o projeto,
status/diff/log do git, rodar testes/build/lint/format, criar/editar/renomear/apagar
arquivos (com /undo), processos/portas/disco/memória, serviços, containers, instalar
dependências, commit/push/pull com confirmação
```

**Confirmação hoje.** Só existe para `TypoOnly` (a intenção inteira apoiada numa palavra
reparada): `brRunIntent` chama `env.Ask(brTypoConfirm(in), {"Rodar agora","Mandar pro
modelo"})` (brain.go:136-144). Dois problemas: (a) offline, "Mandar pro modelo" é opção morta
— escolhê-la derruba no menu genérico; (b) o bando entre `nuMinConf=0,6` (nlu.go:44) e
`brLevel0Min=0,85` (brain.go:593) **age direto sem nenhum eco** — uma leitura com conf 0,62 de
"apaga o arquivo" roda sem o usuário ver a leitura. O online é melhor aqui só no atalho:
`olFastPath` online pergunta `⚡ atalho: <cmd>` (offline.go:429-438); offline roda sem
mostrar a leitura (offline.go:420-427).

---

## 2. Princípios (com fonte)

**UX-I. Perguntar custa caro: só quando a resposta muda a ação.** Todo diálogo interrompe o
fluxo; pergunta que não muda plano é ruído e ensina o usuário a responder no automático.
Horvitz chama isso de "dialog to resolve *key* uncertainties" dentro de uma análise de
utilidade esperada (P4/P5 dos 12 princípios). A Amazon mediu o mesmo trade-off no Alexa:
perguntar em toda ambiguidade "hampering the user experience" — o modelo deles dispara a
pergunta só quando necessário para a satisfação. Fontes: Horvitz, "Principles of
Mixed-Initiative User Interfaces", CHI '99
([dl.acm.org/doi/10.1145/302979.303030](https://dl.acm.org/doi/10.1145/302979.303030),
[PDF](https://erichorvitz.com/chi99horvitz.pdf); os 12 princípios tabulados nos slides
[Stanford CS329x](http://web.stanford.edu/class/cs329x/slides/2627/Lecture7_human_agent_collaboration.pdf));
Kim, Wang, Lee, Kim, "Deciding Whether to Ask Clarifying Questions in Large-Scale Spoken
Language Understanding", ASRU 2021 ([arXiv:2109.12451](https://arxiv.org/abs/2109.12451)).
O próprio `harness-first-offline.md` já diz "perguntas mínimas e decisivas" e o eval mede
`questions` por tarefa (evaloffline.go:355-356, 389).

**UX-II. Agir ou perguntar é decisão sobre conf × custo do erro, não threshold único.**
"Ideal action under uncertainty hinges on expected utility — balancing costs and benefits of
acting" (Horvitz P4). Uma leitura conf 0,62 de `open_file` (só leitura, reversível) pode agir
com eco; a mesma confiança em `delete_file` (risk `"high"` — methods/*.json têm o campo,
parseado em methods.go:70) pede confirmação. É o mesmo raciocínio do `ask_user` escrito na
spec (askuser.go:27-28: "only when the task is genuinely ambiguous and the answer changes
the work") e do `brPick` (methods.go:1130-1155: um candidato distante é "confirm, never
assume"). Fonte: ibid. + o princípio de *commensurate effort* — esforço da interação
proporcional à consequência (CS Field Guide,
[csfieldguide.org.nz/en/chapters/human-computer-interface/interface-usability](https://www.csfieldguide.org.nz/en/chapters/human-computer-interaction/interface-usability/)).

**UX-III. Confirmação de uma tecla; nunca uma palavra digitada.** O padrão do terminal é
`[y/n/e/esc]`: o zsh inventou o quarteto `correct 'sl' to 'ls' [nyae]?` — **y**es, **n**o,
**a**bort, **e**dit (zsh `SPROMPT`, [zsh.sourceforge.io Intro §16](https://zsh.sourceforge.io/Intro/intro_16.html);
explicado em [stackoverflow.com/q/800182](https://stackoverflow.com/questions/800182/what-does-nyae-mean-in-zsh)).
O git convergiu para o mesmo lugar: `help.autocorrect=prompt` mostra `Run 'checkout' instead
[y/N]?` (Git 2.34, commit `dc66e3c`; [documentação](https://git-scm.com/docs/git-config#Documentation/git-config.txt-helpautoCorrect)).
thefuck mostra a correção inteira antes de rodar (`cmd [enter/↑/↓/ctrl+c]`,
[github.com/nvbn/thefuck](https://github.com/nvbn/thefuck)). gum/bubbletea instituíram o
confirm y/n + Enter + Esc em Go ([charmbracelet/gum confirm.go](https://github.com/charmbracelet/gum/blob/main/confirm/confirm.go)).
O jazz já implementa isso melhor que todos: dígito, letra `Key`/`Keys`, j/k, setas, Enter na
opção destacada, Esc cancela (menu.go:103-187) e `cliChoose` degrada para número/letra/primeira
palavra fora do TUI (confirm.go:203-244). A regra interna já é "Nothing here asks for a typed
word" (menu.go:8).

**UX-IV. "Você quis dizer" se dá com top-k, não com sim/não cego nem lista exaustiva.** O git
mostra `Did you mean one of these?` com os candidatos ranqueados por Damerau-Levenshtein
ponderado (swap=0, ins=1, sub=2, del=4 — commit `8af84da`, 2008) e oferece rodar um deles; a
paleta de comandos nasceu exatamente para isso: Sublime Text 2 (jul/2011) aplicou o fuzzy
matching do Goto Anything à Command Palette, e o VS Code herdou com o Quick Pick e seus
prefixos `>` `@` `:` `#` ([Interface Lineage: Command Palette](https://interfacelineage.com/command-palette);
issue de fuzzy no VS Code [microsoft/vscode#1964](https://github.com/microsoft/vscode/issues/1964)
— com a lição: ranking precisa ser **estável e memorizável**, fuzzy demais atrapalha). O
padrão terminal é o filtro incremental com feedback por tecla (fzf: "narrow down the list as
you type" com algoritmo v2 + tiebreak, [junegunn.github.io/fzf](https://junegunn.github.io/fzf/reference/)).
O jazz já tem as duas metades: `fzResolve`/`fzOffer` para `/comandos` (fuzzy_cmd.go:149-239)
e `edRankCommands` com 4 tiers estáveis (editor_palette.go:80-131). Falta aplicar o mesmo a
**intenções**, não só a nomes de comando.

**UX-V. Escopo do serviço proporcional à incerteza (scope precision).** Incerto do todo, certo
de uma parte: ofereça o subconjunto seguro em vez de tudo ou nada. Horvitz P8 — "scoping
precision of service to match uncertainty": com confiança baixa o sistema estreita o que faz
(ler o arquivo em vez de editá-lo; mostrar o diff em vez de commitar). Fonte: Horvitz CHI '99,
ibid. No jazz isso já aparece dentro da resolução de entidade: `brPick` só age com nome exato,
candidato único seguro ou líder com margem ≥ `fzSlip*8` (methods.go:1142-1154) — aplicar o
mesmo à **intenção** é a generalização natural.

**UX-VI. Reversibilidade muda o cálculo: ação com undo barato pede menos confirmação.**
"Emergency exit" é a heurística #3 do Nielsen (user control and freedom: "support Undo and
Redo"), e a NN/g recomenda desfazer em vez de confirmar quando o erro provável é *slip*.
Fontes: NN/g, "10 Usability Heuristics" ([nngroup.com/articles/ten-usability-heuristics](https://www.nngroup.com/articles/ten-usability-heuristics/));
"Preventing User Errors" ([nngroup.com/articles/user-mistakes](https://www.nngroup.com/articles/user-mistakes/)).
O jazz já tem a infraestrutura exata: shadow-git cobre escritas do cérebro (`BeginTurn`/
`AfterWrite`/`Undo` no `brEnv`, methods.go:169-174; ligado em brain.go:262-263, 499-500,
699-700; checkpoint.go). Falta a UX dizer isso — "coberto por /undo" — e a política cobrar
menos confirmação do que é reversível.

**UX-VII. "The Computer That Cried Confirm": confirmação demais causa mais erro, não menos.**
Diálogo de confirmação interrompe e atrasa; repetido vira reflexo de apertar Enter — "a design
intended to prevent errors can actually increase them". Confirmar exceção e consequência
irreversível, nunca rotina. Fonte: NN/g, "Preventing User Errors: Avoiding Conscious
Mistakes" ([link](https://www.nngroup.com/articles/user-mistakes/)) e "Modal & Nonmodal
Dialogs: When (& When Not) to Use Them" (modal só para erro crítico/irreversível,
[nngroup.com/articles/modal-nonmodal-dialog](https://www.nngroup.com/articles/modal-nonmodal-dialog/)).
Corolário para o d610: opção perigosa longe da benigna e visualmente distinta (NN/g
"Dangerous UX: Consequential Options Close to Benign Options",
[nngroup.com/articles/proximity-consequential-options](https://www.nngroup.com/articles/proximity-consequential-options/))
— no menu do jazz, `não` nunca deve ser a tecla vizinha de `sim`.

**UX-VIII. Feedback <100 ms e estado sempre visível.** 0,1 s é o limite de "resposta
instantânea"; 1 s o limite do fluxo de pensamento; acima disso o feedback precisa existir e
mostrar progresso (Nielsen, *Usability Engineering* 1993;
[nngroup.com/articles/response-times-3-important-limits](https://www.nngroup.com/articles/response-times-3-important-limits/)).
Heurística #1: visibility of system status. O jazz já honra isso na engine (status line com
orçamento de bytes/s para SSH lento, docs/UX.md §7; `ui.Thinking` usado no probe de rede em
offline.go:391-393). O cérebro offline, porém, executa passos em silêncio — `cx.say` acumula
num `strings.Builder` e só sai no fim (methods.go:212-217, brain.go:170).

**UX-IX. Mostrar por que o sistema fez o que fez — evidência, não mágica.** Amershi G11 (CHI
2019): "make clear why the system did what it did"; G16: "convey the consequences of user
actions". O jazz já guarda `Intent.Evidence[]` (nlu.go:52) e o task registra "interpretado:
<evidências>" (brain.go:165) — a matéria-prima do "li 'atualzia' como 'atualiza'" já existe
(`brTypoConfirm`, brain.go:197-209); falta exibi-la de forma consistente em todas as ações.
Fonte: Amershi et al., "Guidelines for Human-AI Interaction", CHI 2019
([microsoft.com/en-us/research/publication/guidelines-for-human-ai-interaction](https://www.microsoft.com/en-us/research/publication/guidelines-for-human-ai-interaction/),
[PDF](https://www.microsoft.com/en-us/research/wp-content/uploads/2019/01/HAI.Guidelines.CHI2019.v10.share_.pdf)).

**UX-X. Dizer o que o sistema sabe fazer — como ações, não como parágrafo.** Amershi G1/G2:
capacidade e calibração esperada, no ponto de uso. O honest-miss certo não é uma lista de
capacidades em prosa (`brNotUnderstood`, brain.go:215-218) — é um menu em que cada
capacidade é clicável. O próprio `olBanner` já faz isso melhor: ensina pedidos prontos e
atalhos (offline.go:196-217). Fonte: ibid.

**UX-XI. Recuperação eficiente: corrigir deve ser mais fácil que ter pedido.** Amershi
G8/G9 — efficient dismissal e efficient correction: [e]ditar o pedido (o `e` do `[nyae]`),
menu que nunca bloqueia texto livre, Esc universal. No jazz, `olPick` já deixa qualquer texto
não-pick virar prompt normal (offers.go:253-277) — a invariante certa; falta o "editar o
pedido" como saída explícita.

**UX-XII. Memória da interação recente: a resposta tem que aterrissar na pergunta que foi
feita.** Horvitz P11 (memory of recent interactions); Amershi G12 (remember recent
interactions). Hoje uma tarefa que para em `needs_input` (task.go:25) grava a transição na
sessão, mas o `App` não guarda a tarefa pendente: a resposta do usuário vira um prompt novo
sem contexto. `AskText` nem está ligado no interativo — `brInteractive` só instala `env.Ask`
(brain.go:704-714), então `brAskSlot` (methods.go:1222-1236) e o slot faltante de
brain.go:388-392 saem como texto solto. A invariante boa já existe para menus: `a.menu` +
`olTakePick` (main.go:1012, offers.go:340-347, consumido em offline.go:348-351) — estender
para tarefa pendente é o mesmo padrão um nível acima.

**UX-XIII. Interatividade só onde existe terminal; em TTY burro, uma escrita e uma resposta.**
CLIG: "only use prompts if stdin is a TTY", "if --no-input is passed, don't prompt", "let
the user escape" ([clig.dev](https://clig.dev/), seção Interactivity). O jazz já implementa:
`env.Ask==nil` headless = nunca pergunta (brain.go:134-136), menus em `t.dumb` imprimem uma
vez e respondem por número (menu.go:405-419), `cliChoose` degrada fora do TUI
(confirm.go:203-244), ASCII total com `JAZZ_ASCII`/`TERM=linux` (docs/UX.md §10). Novas
confirmações precisam nascer dentro dessas invariantes — nenhum widget novo que assuma ANSI.

---

## 3. Mapa do que já existe (file:line) e onde o offline fica atrás

Legenda: **feito** = funciona hoje · **parcial** = existe mas com buraco · **só pesquisado** =
a peça existe, a UX não usa.

| Capacidade | Online (com modelo) | Offline | Estado / lacuna |
|---|---|---|---|
| Widget de pergunta 1-tecla (setas, dígito, letra, Esc, anti-slip 250 ms/600 ms) | `menu.go` inteiro; `uiApproveGrace/Idle` ui.go:727-734 | mesmo widget via `env.Ask` brain.go:704-714 | **feito** — reutilizar para tudo abaixo |
| Confirmação antes de rodar atalho | `olFastPath`: `⚡ atalho: cmd` + Choose (offline.go:429-438) | roda direto, sem eco (offline.go:420-427) | **parcial**: offline não mostra a leitura nem confirma nada |
| Confirmação "li X como Y" (typo) | TypoOnly→Ask (brain.go:136-144) só dentro do nível-0 via `brLevel0Interactive` | mesmo código via `brInteractive` | **parcial**: opção "Mandar pro modelo" é morta offline; só dispara em typo-only, não em conf média; sem [e]ditar |
| Banda de confiança | `brLevel0Min`=0,85 gateia o atalho determinístico (brain.go:593, 652) | `nuMinConf`=0,6: abaixo miss, acima **age sem eco** (nlu.go:44, 62-64) | **parcial**: a banda 0,6–0,85 não tem UX própria offline |
| "Você quis dizer" para `/comando` | `fzFixCommand`+`fzOffer`: menu top-3 (fuzzy_cmd.go:149-239; main.go:1566) | idem (comando é modo-agnóstico) | **feito** — mas só p/ slash commands |
| Top-k de intenções no miss | n/a (o modelo responde) | `in.Alt` calculado e **nunca lido** (nlu.go:946-948, 963-964); `in.Name`+conf descartados no miss interativo (brain.go:146, 717-719) | **só pesquisado**: dados existem, UI não usa |
| Menu acionável no miss | n/a | `olOffer`+`olMenu`: itens por texto/projeto, não por hipótese (offers.go:158-209; offline.go:383-397) | **parcial**: menu existe, genérico; headless ainda imprime o parágrafo `brNotUnderstood` (brain.go:211-219, 543-544) |
| Desambiguação de entidade | `brChoose`: menu ≤5 + "Nenhum desses" (methods.go:1157-1209; env.Ask brain.go:663-674) | idem (mesmo env.Ask) | **parcial**: funciona; "Nenhum desses"→`needs_input` com lista em texto (methods.go:1197-1205), sem widget nem retomada; sem evidência por candidato |
| Slot faltando | idem | `brAskSlot` usa `env.AskText` — **nil no interativo** (brain.go:704-714) → `needs_input` + texto; `Missing`→"falta X: diga qual" (brain.go:388-392) | **parcial/quebrado**: pergunta sai como prosa e a resposta vira prompt novo (sem estado) |
| Continuidade entre turnos | fila de msgs + steering (docs/UX.md §7) | `a.menu`+`olTakePick` retoma **menu** pendente (offers.go:340-347; offline.go:348-351); **tarefa** `needs_input` não é retomada | **parcial**: padrão existe 1 nível abaixo do necessário |
| Progresso da execução | ToolStart/ToolEnd por ferramenta | `cx.say` acumula e só imprime no fim (methods.go:212-217; brain.go:170, 720-722); spinner só no probe (offline.go:391-393) | **parcial**: execução do método é silenciosa até o status final |
| Verificação/undo anunciado | diff+`/undo`+`/rewind` | verificação existe (`brVerify`, status line brain.go:482-492); shadow-git cobre escritas (env.BeginTurn/Undo brain.go:262-263, 699-700) | **parcial**: usuário não vê que a ação é desfazível |
| Fato respondido sem perguntar | `askFromFacts` responde do projeto (askuser.go:111-115, askfacts.go) | precondições de método leem fatos (`brFacts`, brain.go:222-254) — mesma ideia | **feito** no nível método |
| Block→ação seguinte | `blockMsg`/`blockNote` com "o que fazer" (blockux.go:26, 149-170) | recuperação por categoria `rvDecide` já gera próximo passo (recover.go; brain.go:394-410) | **feito** — modelo a seguir para o miss |
| Medição de perguntas | — | eval `expect.questions`, `Answers`, `Texts` (evaloffline.go:286-288, 311-351, 389) | **feito** — instrumentação já mede "perguntas feitas vs necessárias" |

---

## 4. Propostas

Ordenadas por valor/custo. Cada uma cabe no 386: nenhuma adiciona dependência, todas reutilizam
`menu.go`/`olAction`/`brEnv`. Impacto 1–5; esforço S/M/L.

### P1 — Confirmação de intenção em banda dupla (conf × risco do método)

**Mecanismo.** Hoje a decisão é binária (`conf ≥ 0,6` age; `< 0,6` miss) e `TypoOnly` é o único
caso confirmado. Proposta: depois de escolher o método em `brRunIntent` (o método já carrega
`risk: low|med|high` — methods.go:70, methods/*.json), decidir por uma matriz:

| | risk `low` (leitura) | risk `med`/`high` (escrita, git, processo, rede) |
|---|---|---|
| `conf ≥ 0,85` e não TypoOnly | age + eco P4 | confirma (1 tecla) |
| `0,5 ≤ conf < 0,85` ou TypoOnly | confirma (1 tecla) | confirma (1 tecla) |
| `< 0,5` | miss → P2 | miss → P2 |

Confirmar = o mesmo menu de sempre (`mnSpec`), título `"entendi <leitura> — <desc do intent>"`,
corpo com a evidência (`in.Evidence`, brain.go:165), e três saídas: **Enter/s** roda, **e**
edita o pedido (P5), **n/Esc** cai no menu de alternativas (P2) em vez de morrer. Offline,
não existe "mandar pro modelo": a 3ª opção vira "escolher outra coisa" (ou `/online` quando
houver probe positivo). A permissão de comando (`PermitCommand`, offline.go:290-295) continua
independente: a confirmação é de **intenção**, não substitui guardrail (princípio 2 da spec —
"não enfraquece guardrail").

**Antes** (conf 0,72, risco med — hoje roda mudo):

```
› apaga os logs antigos
● Bash(find . -name '*.log' -mtime +30 -delete)
  ⎿  (apagou 41 arquivos)
```

**Depois:**

```
› apaga os logs antigos

╭─ confirma ────────────────────────────────────────────────────────╮
│ Entendi: apagar arquivo · alvo '*.log' antigos                    │
│ (regra delete_file#2 · risco alto · coberto por /undo)            │
│                                                                   │
│ ▸ 1. Sim, apagar                                                  │
│   2. Editar o pedido                                              │
│   3. Não — ver outras opções                                      │
╰───────────────────────────────────────────────────────────────────╯
  enter/s faz · e edita · n/esc não
```

TTY burro/`TERM=dumb`: imprime uma vez, responde por número (menu.go:405-419 já garante).

**Arquivos.** `brain.go` (gate em `brRunIntent` após `mhLibrary`, ~linha 149-158; reusar
`env.Ask` com opções rotuladas por modo); `methods.go` (`mhMethod.Risk` já parseado); novo
`intents.go`-level helper `brConfirmSpec(in, method, online)`; `nlu.go` intacto.

**Custo d610.** Um render de menu ≈ 1–2 KB de escrita (comparável a qualquer `Choose` atual);
zero syscalls novos. **(estimativa)** <5 ms.

**Métrica.** `perguntas feitas vs necessárias` (o eval já conta — evaloffline.go:355); taxa
de respostas `n`/`e` por banda de conf (false-confirm percebido: se a banda 0,6–0,85 gera >30%
de "não", o limiar sobe); `tempo até primeira ação` nos atalhos (não pode cair: conf ≥0,85
low-risk nunca pergunta).

**Impacto 5 · Esforço M · Risco:** over-asking → fadiga (UX-VII); mitigação: banda alta nunca
pergunta em `low`, e a resposta `n` registra (P9) para recalibrar.

### P2 — Honest-miss vira menu top-k (mata o parágrafo morto)

**Mecanismo.** `brRunIntent` devolve o `in` para o chamador (já está em `res.Intent`). No
interativo, `brInteractive` repassa `in` a um builder que compõe o menu do miss:

1. **hipótese principal** quando `in.Name != "none" && conf > 0`: `"acho que é: <desc> — conf
   X.XX"` como `olAction{Kind:"brain", Intent:in.Name}` — roda `brRunIntent` com a leitura
   aceita (mesmo caminho que a confirmação TypoOnly já usa, brain.go:141-144);
2. **alternativas** `in.Alt[:2]` idem (o campo existe, hoje é lixo);
3. **ações contextuais** do `olMenu` atual (arquivo citado, termo de busca, testes) — herda
   offers.go:158-209;
4. **saídas**: `editar o pedido` (P5), `/online` ou `esperar`.

Headless (`-p`): sem menu — mas o texto vira oferta executável em vez de parágrafo: hipótese
com conf + 3 pedidos reformuláveis de exemplo. `olMenuMax=5` vira ~6; manter ≤ 1 tela (UX-XIII).

**Antes (interativo):** ver §1 — menu genérico, hipótese descartada.
**Antes (headless):** parágrafo `brNotUnderstood`.

**Depois (interativo):**

```
› refatora o login pra usar JWT

  ⚠ sem certeza do pedido — o que eu acho / o que dá pra fazer:
    1  acho que é: explicar arquivo ou símbolo     (conf 0.52)
    2  procurar 'login' no código     rg -n --no-heading -S -- 'login' . | head -60
    3  rodar os testes e listar falhas   go test ./...
    4  editar o pedido
    5  /online — tentar o modelo de novo
  1-5 roda · qualquer outro texto vai como pedido normal
```

**Depois (headless):**

```
não tenho certeza do pedido sem o modelo. Minha melhor leitura: explicar arquivo
ou símbolo (conf 0.52). Tente:
  jazz -p --offline "explica o auth.go" · "procura JWT no código" · "roda os testes"
```

**Arquivos.** `brain.go` (brInteractive repassa `in`; `brNotUnderstood` ganha cauda acionável);
`offline.go` (`olBefore` chama builder em vez de `olMenu` puro); `offers.go` (`olAction.Kind`
"brain" + caso em `olRun` que invoca `brRunIntent` com `in` forçado — ~30 linhas).

**Custo d610.** Zero syscalls além dos que `olMenuState` já faz (um `git status` com cap de
2 s — offers.go:320-325; considerar reusar o `fpDetect` já calculado). **(estimativa)** <20 ms
típico.

**Métrica.** `% de misses que viram ação em 1 passo` (hoje ~0 — é dead end ou prompt genérico);
`abandono` medido por "usuário fecha a sessão dentro de 1 turno após miss"; taxa de acerto da
hipótese 1 (calibra o topo da banda).

**Impacto 5 · Esforço M · Risco:** hipótese errada em 1º lugar pode induzir clique errado —
mitigar: hipótese nunca é `Sel=0` quando `conf < 0,6` e risco ≥ med (o item 1 vira o mais
seguro, não o mais provável — UX-II/UX-VII).

### P3 — Continuidade: tarefa `needs_input` retomada no turno seguinte

**Mecanismo.** Campo `App.pendingBrain *TaskState` (ao lado de `a.menu`, main.go:1012). Quando
`brRun` termina `tkNeedsInput`, guarda `{Goal, Intent, Slots, Missing}`. No topo de
`olBefore` (depois de `olTakePick`, offline.go:348-356): se há tarefa pendente, tenta
**preencher o slot** com a resposta — re-parse: se a msg nova parseia intenção entendida com
conf alta e ≠ complemento do slot, vira tarefa nova (a velha é abandonada com 1 linha);
senão, o texto inteiro vira valor do `Missing[0]` e `brRunIntent` roda com `Slots` mesclados.

`tkNeedsInput → tkInvestigating` já é transição legal (task.go:43). E ligar `env.AskText`
interativo (hoje nil, brain.go:704-714) a um widget real: menu com os candidatos conhecidos
+ "Outro (escrever)" que abre linha — o mesmo desenho de `AskUser` (askuser.go:171-218,
`mnText` menu.go:50-52, 181-184). **Menor pergunta possível**: quando o slot tem ≤5 candidatos
plausíveis (índice de nomes, hosts, serviços), menu; texto livre só quando não há candidato.

**Antes:**

```
› abre o arquivo
falta target: diga qual (ex.: o nome do arquivo)
· cérebro offline: open_file · preciso de uma resposta

› o de teste
  ⚠ offline: nenhum provedor alcançável — o que dá pra fazer sem modelo:   ← morreu
```

**Depois:**

```
› abre o arquivo

╭─ pergunta ───────────────────────────────────────────────╮
│ Qual arquivo?                                            │
│ ▸ 1. parser.go          citado agora há pouco            │
│   2. parser_test.go                                      │
│   3. Outro nome (escrever)                               │
╰──────────────────────────────────────────────────────────╯
  ↑↓ escolhe · enter confirma · esc pula

● Qual arquivo?  parser_test.go
  parser_test.go · 2,4 KB · go
  …
· cérebro offline: open_file · feito e verificado · 2 evidências
```

E com resposta fora do menu:

```
› conecta via ssh
· falta host — qual máquina? (hosts conhecidos: labbox, dell, pi4 · /online p/ outro)

› labbox
● host: labbox · ssh labbox …
```

**Arquivos.** `main.go` (`pendingBrain` + reset em `/clear`, `/rewind`, troca de modo);
`brain.go` (`brInteractive` retorna o task; `env.AskText` interativo); `offline.go`
(olBefore: tentativa de slot-fill antes do parse normal); `methods.go` (`brChoose` no
caminho "nenhum desses" → `AskText` quando houver).

**Custo d610.** Um re-parse por resposta (~ms — `Parse` é Aho-Corasick + regras + Bayes com
log inteiro, já medido p50 14–19 ms no Dell — results/dell-0.9-progress.md). RAM: um
`TaskState` pequeno.

**Métrica.** `needs_input → succeeded em ≤1 turno extra` (hoje ~0); perguntas/tarefa (eval);
`respostas capturadas` vs `perdidas` (resposta que caiu em miss).

**Impacto 5 · Esforço M · Risco:** sequestro de prompt — uma msg nova de outro assunto
preencher o slot errado; mitigação: se o re-parse entende outra intenção com conf ≥ 0,85,
a tarefa pendente é descartada (log) e a nova vence — nunca bloquear o turno.

### P4 — Eco não bloqueante "· entendi: X" em toda ação offline

**Mecanismo.** Uma linha dim **antes** de executar, quando a ação vai direto (conf ≥ 0,85
low-risk, fast path offline, item de menu). Não é pergunta — é o mesmo feedback que o online
dá de graça ao mostrar o tool call com o comando. Forma: `· entendi: rodar os testes
(go test ./...)` ou `· entendi 'parsr' como parser.go`. O usuário que vê a leitura errada
ainda pode Esc/Ctrl+C — mas em geral nem precisa agir (UX-VIII: <100 ms, custo de atenção ~0).

**Antes:**

```
› roda os tetes
● Bash(go test ./...)
  ⎿  ok  jazzcli  0.4s
```

(o que foi entendido — e que `'tetes'` foi reparado — só aparece enterrado no evidence)

**Depois:**

```
› roda os tetes
· entendi: rodar testes · 'tetes' → 'testes' (go test ./...)
● Bash(go test ./...)
  ⎿  ok  jazzcli  0.4s
· cérebro offline: run_tests · feito e verificado · 2 evidências · 1 comando
```

**Arquivos.** `brain.go` (`brInteractive`: 1 linha a partir de `in.Evidence`/`descOf` antes de
`brExecute`); `offline.go` (`olFastPath` offline: eco antes de `olRun`, ~linha 420-427);
`offers.go` (`olRun`: eco do `act.Label`+`act.Cmd` — já tem ToolStart, adicionar só a
leitura). Reusar `brTypoConfirm` para formatar reparos (brain.go:197-209).

**Custo d610.** 1 linha de saída (~80 B); zero. **(estimativa)** imperceptível até em serial
9600 (10 ms). Em `t.dumb` sai como texto normal.

**Métrica.** `tempo até primeira saída` (<100 ms garantido); taxa de interrupção logo após o
eco (sinal de leitura errada = proxy de miscalibração, alimenta P9).

**Impacto 3 · Esforço S · Risco:** ruído em cada turno — mitigar: só 1 linha, dim, e nunca
quando a resposta é imediata demais para ler (<50 ms já fica redundante? manter: custa 80 B).

### P5 — Tecla `e`: "editar o pedido" volta o texto corrigido pra caixa

**Mecanismo.** O `e` do `[nyae]` (UX-III): na confirmação P1 e no menu de miss P2, opção
"Editar o pedido" → `mnSpec.Alt['e']` (menu.go:46) → o `App` preenche a caixa de entrada com
o texto **já reparado** (`nuCanonText` ou `fzRepairLine` aplicados — o texto corrigido existe:
nlu.go:1039-1049, intents.go:409). O usuário vê "o que o jazz acha que ele quis dizer",
ajusta uma palavra e manda — muito mais barato que redigitar (UX-XI). Também serve no
"Nenhum desses" da desambiguação.

**Antes:** sem saída — ou aceita a leitura errada, ou `n` + redigita tudo.

**Depois:**

```
› atualzia o jazz
  (menu de confirmação P1)
  e ▸
  ● editar o pedido
│ › atualiza o jazz▏          ← caixa preenchida com a leitura corrigida
```

**Arquivos.** `menu.go` (Alt key — já suportado); `brain.go`/`offline.go` (o Ask/menu
retorna código "edit"); editor.go (setter do buffer — já existe para Ctrl+R/palette pick:
editor_palette.go:327-344 `edPop.pick` é o modelo).

**Custo d610.** Zero (troca de string no buffer).

**Métrica.** `% confirmações respondidas com e` — mede diretamente onde o NLU erra por pouco
(vale mais que `n`, que não diz o quanto errou); candidatos de dataset: cada `e` + envio é
uma (frase_digitada → frase_correta) par grátis para `nlu/intents.json` (espírito de
learn.go §7: o uso online ensina o offline — aqui a **correção** ensina).

**Impacto 3 · Esforço S-M · Risco:** baixo.

### P6 — Progresso por passo durante a execução do método

**Mecanismo.** O cérebro executa 1–3 passos em silêncio (método escolhido → passos →
verificação — brain.go:264-381). Gancho `env.OnStep func(m mhMethod, i, n int, label string)`
no `brEnv` (methods.go:158-182); interativo → `ui.Thinking`/Notice de 1 linha por passo
("método run_tests.go · passo 2/3 · verificando"); `t.dumb` → imprime a linha e pronto.
Sem spinner novo — reusar o existente (ui_offers.go/ui.go), que já respeita orçamento de
redesenho para SSH (docs/UX.md §7: ~310 B/s esperando).

**Antes:**

```
› roda os testes
                        ← 8–40 s de silêncio no d610
· cérebro offline: run_tests · feito e verificado · 3 evidências · 1 comando
```

**Depois:**

```
› roda os testes
· entendi: rodar testes · método run_tests.go
● Bash(go test ./...)  12.4s          ← ToolStart/ToolEnd reais já existem p/ caps de comando
· verificando: test_report
· cérebro offline: run_tests · feito e verificado · 3 evidências · 1 comando
```

**Arquivos.** `methods.go` (`brExecute` chama `cx.env.OnStep` — brain.go:290-336);
`brain.go` (liga o hook ao `a.ui`); `task.go` intacto (transições já dão o texto).

**Custo d610.** ≤4 linhas por tarefa; nada de redraw contínuo.

**Métrica.** abandono durante tarefa >10 s (Esc/Ctrl+C no meio = frustração por silêncio);
"tempo percebido" não mede — proxy: reclamação medida por sessões que não interrompem.

**Impacto 3 · Esforço S · Risco:** log demais em método longo — cap: 1 linha por passo, máx
n=8 (budget já é 8 cmds — task.go:148).

### P7 — Desambiguação de entidade com evidência por candidato + "nenhum" acionável

**Mecanismo.** `brChoose` já monta o menu ≤5 (methods.go:1184-1209). Três melhorias de 1
tela: (a) `mnItem.Hint` com **por que** cada candidato aparece — o `niEntity.Why[]`/Score já
existe (nameindex.go:935-940): "modificado há 2 h", "citado no turno anterior", "único .go
com o prefixo"; (b) item "Nenhum desses" hoje derruba em `needs_input` com a lista impressa
em prosa (methods.go:1197-1205) — fazer abrir `AskText` interativo ("diga o nome certo"),
caindo na P3; (c) `v`/`mnSpec.View` mostra os 5 com detalhe completo (path, mtime, kind).

**Antes:**

```
╭─ pergunta ───────────────────────────────────────────╮
│ Qual arquivo você quis dizer com 'handler'?          │
│ ▸ 1. api/handler.go                                  │
│   2. handler_test.go                                 │
│   3. web/handler.js                                  │
│   4. Nenhum desses                                   │
╰──────────────────────────────────────────────────────╯
  (escolher 4 → texto: "responda com o número (ou o nome certo)" → fim)
```

**Depois:**

```
╭─ pergunta ───────────────────────────────────────────╮
│ Qual arquivo você quis dizer com 'handler'?          │
│ ▸ 1. api/handler.go      você abriu antes · há 2 h   │
│   2. handler_test.go     teste · 4 KB                │
│   3. web/handler.js      outra linguagem             │
│   4. Outro (escrever o nome)                         │
╰──────────────────────────────────────────────────────╯
  ↑↓ escolhe · enter confirma · v detalhes · esc pula
```

**Arquivos.** `methods.go` (`brChoose`: Hint de `e.Why`, rótulo do item "outro", `AskText`
no fallback); `nameindex.go` (`niEntity.Why` já populado — só surfacing).

**Custo d610.** Strings já calculadas; zero syscalls.

**Métrica.** `% desambiguações que terminam em needs_input` (hoje: todo "nenhum" vira);
`turnos até o arquivo certo`; picks por posição (o ranking certo → posição 1 domina).

**Impacto 3 · Esforço S · Risco:** Hint longo demais — `oneLine` e cap 40 col.

### P8 — Pós-ação: veredito + "/undo desfaz" explícito + próxima ação

**Mecanismo.** `brStatusLine` já resume `feito e verificado · N evidências` (brain.go:482-492).
Acrescentar quando couber: `· /undo desfaz` (se `env.Undo != nil` e a tarefa escreveu —
`AfterWrite` já registra, methods.go:169-174) e, em falha/`needs_input`, **a próxima ação**
como item de menu P2 em vez de texto ("instalar? (capacidade install_tool, com
confirmação)" — brain.go:403 — vira `olAction{Kind:"brain"}` clicável). A ação seguinte já
existe como dados (`rvDecide` → `act`, brain.go:372-375, 397); é surfacing, não lógica nova.
Espelha o que `blockux.go` fez para o modelo: block→instrução acionável (blockux.go:25-56).

**Antes:**

```
· cérebro offline: rename · feito e verificado · 3 evidências · 2 comandos · 0 perguntas
```

(renomeou — o usuário não sabe que dá pra desfazer sem git)

**Depois:**

```
· cérebro offline: rename · feito e verificado · 3 evidências · /undo desfaz
```

e no bloqueio por falta de ferramenta:

```
  ⚠ o rg não está no PATH — o que dá pra fazer:
    1  instalar ripgrep (apt/cargo — com confirmação)
    2  procurar com grep mesmo   grep -rnI -- 'ParseConfig' . | head -60
    3  outra coisa
```

**Arquivos.** `brain.go` (status line + coleta do `act` de `rvDecide`); `recover.go`
(expõe a ação sugerida — hoje fica em texto, brain.go:397-407); `offers.go` (items do
bloqueio no mesmo `olOffer`).

**Custo d610.** Zero — strings.

**Métrica.** uso de `/undo` pós-ação (descoberta do recurso — hoje ~0 offline); `blocked →
ação seguinte` em 1 tecla vs redigitação.

**Impacto 4 · Esforço S · Risco:** sugerir ação que o guard bloquearia de novo — passar o
mesmo `PermitCommand` antes de oferecer (oferta nunca promete o que a guarda nega).

### P9 — Calibração medida: log conf×acerto por intent → banda adaptativa

**Mecanismo.** Os limites 0,6/0,85 são hoje constantes globais (`nuMinConf`, `brLevel0Min`).
Registrar por sessão `{"t":"brconf","intent","conf","typo","asked","answer","outcome"}`
(no `tkSessionSink`/obs, task.go:258-281, obEmit) — sessão é auditável já. Com N sessões:
por intent, taxa real de acerto por banda de conf; a banda de confirmação P1 deixa de ser
global e vira por-intent (`list_files` acerta com 0,62 → age; `delete_file` erra com 0,8 →
confirma sempre). É o "learn by observing" (Horvitz P12) e a validação de threshold contra
resultado real que os guias de HITL pedem — nunca calibrar sobre conf crua. Alimenta também
o eval: `jazz eval offline` mede `questions` hoje; passa a medir `perguntas evitáveis` =
confirmações respondidas `n`/`e`.

**Arquivos.** `obs.go`/`task.go` (evento novo); `brain.go`/`offline.go` (emissão);
`evaloffline.go` (métrica nova); análise offline no `jazz improve`/`lessons_reuse.go`
(já existe infra de lições — lessons.go).

**Custo d610.** 1 linha JSONL por turno (~200 B); análise offline fora do caminho quente.

**Métrica.** É a própria métrica: matriz conf×acerto por intent publicada em `results/`
(como `dell-0.9-progress.md`).

**Impacto 4 · Esforço L · Risco:** nenhum para UX; requer volume de uso real para calibrar
(até lá, bandas conservadoras da P1).

---

## 5. Resumo das propostas

| # | Proposta | Impacto | Esforço | Risco principal | Métrica-chave |
|---|---|---|---|---|---|
| P1 | Confirmação conf×risco "entendi X — [Enter]/[e]/[n]" | 5 | M | cried-confirm | perguntas feitas vs necessárias; % `n` |
| P2 | Miss → menu top-k acionável (inter+headless) | 5 | M | hipótese errada em 1º | % miss→ação em 1 passo; abandono |
| P3 | `needs_input` retomada + AskText interativo | 5 | M | sequestro de prompt | needs_input→success ≤1 turno |
| P4 | Eco "· entendi:" <100 ms não bloqueante | 3 | S | ruído | tempo até 1ª saída; Esc pós-eco |
| P5 | [e]ditar pedido → caixa preenchida | 3 | S-M | — | % respostas `e` (dataset grátis) |
| P6 | Progresso por passo do método | 3 | S | log demais | abandono em tarefa >10 s |
| P7 | Desambiguação com evidência + "outro"→AskText | 3 | S | hint longo | desambiguação→needs_input; posição do pick |
| P8 | Veredito + "/undo desfaz" + próxima ação | 4 | S | sugerir o bloqueável | uso de /undo; blocked→ação 1 tecla |
| P9 | Calibração conf×acerto por intent | 4 | L | n/a p/ UX | matriz conf×acerto em results/ |

Ordem sugerida: **P4+P1** (o coração: eco + confirmação em banda — S+M, muda tudo que se
percebe) → **P2** (o miss acionável) → **P3** (continuidade — a que mais diferencia de
Claude Code/Codex offline, porque exige estado) → **P7+P8** (acabamento) → **P5, P6** →
**P9** (quando houver volume para calibrar). Cada uma é independente e verificável com
`jazz eval offline` + PTY tests (`ui_pty_confirm_test.go` já é o padrão para testar menu).

---

## 6. O que NÃO fazer

1. **Não perguntar o que já é certo.** Confirmar atalho claro ("git status", conf 0,97,
   risk low) é exatamente o cried-confirm do NN/g (UX-VII). A banda alta de P1 existe para
   isso — confirmação rotineira destrói a única vantagem que o offline tem sobre o online:
   velocidade.
2. **Não inventar slot nem entidade.** A regra do NLU é explícita: "entities are never
   invented" (nlu.go:25-27). Um menu que sugere arquivo inexistente quebra a confiança mais
   que um miss. Candidato de menu sai do `nameindex`/perfil, nunca de interpolação.
3. **Não agir em TypoOnly/leitura fraca sem confirmar — nem "só dessa vez".** Já é a regra
   de `nlu.go:986-1019` e `brain.go:136`; a tentação de "é só leitura" reintroduz ação
   silenciosa sobre palpite ortográfico. Leitura barata errada ainda é errada à mostra.
4. **Nunca pedir palavra digitada como confirmação** ("digite SIM para continuar"). A regra
   interna é "Nothing here asks for a typed word" (menu.go:8; confirm.go:7). Texto digitado
   é para **slot** (AskText), não para consentimento.
5. **Não bloquear a caixa de entrada.** Menu pendente nunca pode comer um prompt novo:
   `olPick` já devolve "não é pick → vira pedido normal" (offers.go:253-277) — a mesma
   invariante vale para P3 (resposta fora do slot → nova intenção vence).
6. **Não assumir cor, Unicode nem ANSI.** `t.ascii`, `t.dumb`, `NO_COLOR`, `TERM=linux`
   são caminhos de primeira classe (menu.go:314-317, 405-419; docs/UX.md §10). Mock novo
   nasce nos dois modos; `[nyae]` funciona sem nenhuma caixa desenhada.
7. **Não fazer syscall nem Stat/rede para montar menu de miss.** O menu tem que aparecer em
   <100 ms (UX-VIII). `olMentionedFiles` já limita Stat por token (offers.go:121-135) —
   hipóteses `in.Alt` custam zero porque já foram calculadas no `Parse`.
8. **Não perguntar em headless sem flag.** `env.Ask==nil` ⇒ nunca bloquear (brain.go:134-136;
   CLIG). `jazz -p` responde com oferta textual (P2), não com pergunta.
9. **Não deixar o menu crescer.** ≤1 tela, ≤5-6 itens (`olMenuMax`, offers.go:56). Mais
   opções = mais leitura = mais erro; top-k é 3 hipóteses + saídas, não o catálogo de 48
   intents (`nlu/intents.json`).
10. **Não esconder a incerteza nem despejá-la crua.** "talvez X" honesto quando importa
    (decisão); mas `conf 0.52` em cada linha vira ruído — mostrar o número onde ele muda a
    escolha (menu de miss), não no eco de rotina (P4).
11. **Não pular a guarda ao confirmar.** A confirmação de intenção autoriza **executar o
    método** — cada comando/escrita continua passando por `PermitCommand`, guards e auditoria
    (offline.go:290-295). Um "sim" para "entendi X" nunca é carta branca para `rm -rf`.
12. **Não buscar "inteligência" em runtime.** Nada de chamar LLM local/remoto para ranquear
    menu ou redigir pergunta — fere a filosofia (inteligência em tempo de compilação) e o
    d610 não tem nem os ciclos nem a certeza de rede. Todo texto de menu vem de `desc`,
    `Evidence` e templates já embutidos.
13. **Não transformar o typo em alias silencioso.** Se o usuário confirma a correção uma
    vez, não gravar "atualzia→atualiza" para sempre sem ele saber — aliases implícitos são
    o anti-padrão documentado do autocorrect (git exige opt-in `help.autocorrect`; o
    equivalente jazz é a rota de promoção de `nlu/intents.json` via revisão — learn.go §7).
14. **Não reexecutar automaticamente depois de editar.** O `e` devolve a caixa **preenchida**
    — quem envia é o usuário (UX-XI: correção barata, controle do usuário). Reenviar sozinho
    transforma sugestão em ordem.

---

## Fontes

- Horvitz, E. "Principles of Mixed-Initiative User Interfaces". CHI '99, pp. 159-166.
  https://dl.acm.org/doi/10.1145/302979.303030 · https://erichorvitz.com/chi99horvitz.pdf
  (os 12 princípios: http://web.stanford.edu/class/cs329x/slides/2627/Lecture7_human_agent_collaboration.pdf)
- Amershi, S. et al. "Guidelines for Human-AI Interaction" (G1–G18). CHI 2019.
  https://www.microsoft.com/en-us/research/publication/guidelines-for-human-ai-interaction/
- Kim, J.-K., Wang, G., Lee, S., Kim, Y.-B. "Deciding Whether to Ask Clarifying Questions in
  Large-Scale Spoken Language Understanding". ASRU 2021. https://arxiv.org/abs/2109.12451
- Nielsen, J. *Usability Engineering* (1993) — limites 0,1/1/10 s.
  https://www.nngroup.com/articles/response-times-3-important-limits/
- NN/g — "Preventing User Errors: Avoiding Conscious Mistakes" (o "cried confirm").
  https://www.nngroup.com/articles/user-mistakes/
- NN/g — "Modal & Nonmodal Dialogs: When (& When Not) to Use Them".
  https://www.nngroup.com/articles/modal-nonmodal-dialog/
- NN/g — "Dangerous UX: Consequential Options Close to Benign Options".
  https://www.nngroup.com/articles/proximity-consequential-options/
- NN/g — "10 Usability Heuristics for User Interface Design" (#1 status, #3 undo/exit, #5
  error prevention). https://www.nngroup.com/articles/ten-usability-heuristics/
- zsh — spelling correction `[nyae]` (`SPROMPT`, `CORRECT`/`CORRECTALL`).
  https://zsh.sourceforge.io/Intro/intro_16.html
- git — `help.autocorrect` (0 | N decissegundos | immediate | prompt | never), "Did you mean
  this?". https://git-scm.com/docs/git-config · DWIM/Damerau-Levenshtein commit
  https://github.com/git/git/commit/8af84dadb142f7321ff0ce8690385e99da8ede2f · prompt
  interativo (2.34) commit dc66e3c.
- thefuck — correção do comando anterior por regras, confirmação `[enter/↑/↓/ctrl+c]`.
  https://github.com/nvbn/thefuck
- Command palette — linhagem Sublime Text 2 (2011, fuzzy do Goto Anything) → VS Code Quick
  Pick (prefixos `>` `@` `:` `#`). https://interfacelineage.com/command-palette ·
  https://github.com/microsoft/vscode/issues/1964
- fzf — filtro incremental em terminal (algo v2, tiebreak, esquema por contexto).
  https://junegunn.github.io/fzf/reference/
- Command Line Interface Guidelines (human-first; prompts só em TTY; deixar o usuário
  escapar; sugerir o próximo comando). https://clig.dev/
- Principle of commensurate effort — CS Field Guide, HCI chapter.
  https://www.csfieldguide.org.nz/en/chapters/human-computer-interaction/interface-usability/
- Internos: `docs/design/offline-brain.md` (spec §1-§10), `docs/research/harness-first-offline.md`
  ("perguntas mínimas e decisivas"), `docs/research/offline-brain-research.md`,
  `results/dell-0.9-progress.md` (latências medidas), `docs/UX.md` (telas reais 86x27),
  `CHANGELOG.md`.
