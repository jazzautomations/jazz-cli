# Paridade de UX: como fazer o offline parecer tão vivo quanto o online

Pesquisa de UX para o modo offline do jazz (sem LLM), com mapeamento file:line do que já existe,
princípios de HCI com fonte e propostas implementáveis em Go no hardware-alvo: Dell Dell, Linux i686
32-bit, 1 CPU, 2 GB RAM, terminal possivelmente lento (SSH), sem cor (`TERM=dumb`), sem Unicode.

Tese: o offline não deve parecer um "modo quebrado". Ele já resolve a maioria dos pedidos por via
determinística; o que falta é a *coreografia* — ack imediato, progresso nomeado, eco da interpretação,
prova visível e próximos passos. Nada disso exige modelo: é interface. Onde o online parece "alguém
trabalhando", o offline hoje parece "o programa pensando se vai morrer". A diferença é barata de fechar.

Escopo: só interativo (TUI). Headless (`-p`, stream-json) aparece onde importa, mas as propostas
miram o REPL. Nenhuma proposta adiciona dependência, goroutine de render nova ou uso de memória
mensurável — tudo reutiliza `Thinking`/`WatchTurn`/`olAction`/`Choose` que já existem.

---

## 1. Princípios (com fonte)

### P1 — Responda em <100 ms; trabalhe depois
O limiar de "reagiu instantaneamente" é ~0,1 s; até 1 s o fluxo de pensamento continua; acima de 10 s
a atenção vai embora e o usuário precisa de previsão de término (Nielsen, *Response Times: The 3
Important Limits*, nngroup.com/articles/response-times-3-important-limits/). O primeiro frame após o
Enter vale mais que o primeiro resultado: Maister chama isso de "people want to get started" — ser
*acknowledged* acalma mais do que ser atendido rápido (ver P3). Aplicação: `Thinking("preparando")`
antes de qualquer parse/exec offline, exatamente como o online já faz (main.go:1329-1331).

### P2 — Espera ocupada e explicada parece mais curta
Maister, *The Psychology of Waiting Lines* (davidmaister.com/articles/5/52/): tempo ocupado < tempo
desocupado; espera incerta > espera finita; espera inexplicada > espera explicada. Um spinner sem fase
é "unexplained + uncertain". Um spinner com `rodando go test ./...` + última linha de saída + tempo
decorridos é ocupado e explicado. NN/g (*Progress Indicators Make a Slow System Less Insufferable*,
nngroup.com/articles/progress-indicators/): animação em loop só serve para 1–10 s; acima disso,
mostre etapas/progresso real — um loop infinito é indistinguível de sistema travado e leva ao abandono.

### P3 — Indicador determinístico sempre que der; fase honesta quando não der
Apple HIG (*Progress indicators*, developer.apple.com/design/human-interface-guidelines/progress-indicators):
prefera indicador determinado (etapas/progresso real) ao indeterminado; quando usar indeterminado, seja
preciso. Material 3 (m3.material.io/components/progress-indicators/guidelines): <200 ms não precisa de
indicador, 200 ms–5 s indicador de carregamento, >5 s indicador de progresso. Harrison, Yeo & Hudson
(*Faster Progress Bars*, CHI'10, dl.acm.org/doi/10.1145/1753326.1753556): a percepção de duração é
manipulável pela forma do progresso — mas isso vale para *forma*, não para *conteúdo inventado*;
barra que anda de verdade sempre vence. Consequência: passos reais do método (`passo 2/4 · git status`)
são o determinado nativo do cérebro offline — é só renderizá-los.

### P4 — Visibilidade de estado, sem alarme permanente
Heurística #1 de Nielsen (*Visibility of System Status*, nngroup.com/articles/visibility-system-status/):
o sistema deve informar o que está acontecendo, onde o usuário já está olhando, o mais rápido possível.
Erros de modo existem quando o estado não está no locus da atenção (Sellen & Norman, *The Psychology of
Slips*, 1992; Norman, *Categorization of Action Slips*, Psychological Review 88(1), 1981 — "mode error"
é slip de intenção por estado invisível). Regra prática: modo persistente no footer/statusline;
transição de modo é evento (banner/notice) e some; ícone de alerta contínuo treina o usuário a ignorar
alerta de verdade.

### P5 — Confiança calibrada: mostre processo e prova, não só o veredito
Lee & See, *Trust in Automation: Designing for Appropriate Reliance* (Human Factors 46(1), 2004,
doi.org/10.1518/hfes.46.1.50_30392): a confiança se calibra quando a interface expõe *performance*
(o que fez), *processo* (como fez) e *propósito* (pra que) — não só o resultado. Parasuraman, Sheridan
& Wickens (*A Model for Types and Levels of Human Interaction with Automation*, IEEE TSMC-A 30(3),
2000, doi.org/10.1109/3468.844354): automatize aquisição/análise/implementação; deixe a *seleção de
decisão* com o humano quando o custo do erro é alto. Tradução para o jazz: "feito e verificado" sem
prova é subconfiança; "✓ conferi: 42 testes ok · exit 0" é confiança calibrada.

### P6 — Perguntar custa caro; pergunte só quando a resposta muda a ação
Horvitz, *Principles of Mixed-Initiative User Interfaces* (CHI'99, erichorvitz.com/chi99horvitz.pdf):
escopo de precisão proporcional à incerteza, considerar custo de agir e de não agir, timing do diálogo,
evitar a armadilha "tudo ou nada". Rao & Daumé III, *Learning to Ask Good Questions: Ranking
Clarification Questions using Neural Expected Value of Perfect Information* (ACL 2018,
aclanthology.org/P18-1255/): a boa pergunta é aquela cuja resposta esperada mais muda o resultado
(EVPI). Pergunta que não muda nada é fricção pura — e o jazz já mede isso (`evals`: perguntas feitas
vs necessárias). Corolário: confirmar "entendi X?" em *tudo* é tão ruim quanto nunca confirmar.

### P7 — Ecoe a interpretação antes de agir; confirme com UMA tecla só quando precisa
O golfo entre intenção e ação (Norman, *Design of Everyday Things* — gulf of execution/evaluation) se
fecha com *feedforward*: dizer o que será feito na linguagem do usuário. "Recognition rather than
recall" (heurística #6, nngroup.com/articles/ten-usability-heuristics/): escolher numa lista é mais
barato que lembrar e digitar. Keystroke-Level Model (Card, Moran & Newell, *The Psychology of HCI*,
1983): uma tecla ≈ 0,2 s; digitar uma frase são segundos e erros. O jazz já tem o padrão ouro:
`brTypoConfirm` ("li 'atualzia' como 'atualiza' — atualizar o jazz?", brain.go:197-209) via `ui.Choose`
com setas/letra/dígito/Enter/Esc e graça de 250 ms + pausa de digitação 600 ms (menu.go:451-463,
ui.go:727-734). Estender o mesmo gesto — nunca "digite sim/não".

### P8 — O miss honesto nomeia o que tentou e entrega caminhos, não desculpas
NN/g *Error-Message Guidelines* (nngroup.com/articles/error-message-guidelines/): visível, preciso,
construtivo, sem culpa. O artigo sobre 404s (nngroup.com/articles/improving-dreaded-404-error-message/)
chega a recomendar spell-check no erro — "did you mean" reduziu 40% dos 404 num estudo citado. Um
parágrafo "não entendi, posso fazer: …" é um 404: descreve a incapacidade, não o próximo passo. O miss
bom diz *o que foi entendido* ("talvez `run_tests`, confiança 0,5") + top-k de ações prováveis +
ações contextuais + saída (`/online`).

### P9 — Progressive disclosure: poucas opções na cara, o resto a uma tecla
Nielsen, *Progressive Disclosure* (nngroup.com/articles/progressive-disclosure/): mostre só as opções
importantes primeiro; especializadas, sob pedido. Revisão recente (jakobnielsenphd.substack.com/p/
progressive-disclosure): rótulo honesto na porta, nível 1 cobre ~80% dos casos, no máximo 2 níveis.
Aplicação: menu de miss com 3–5 itens (`olMenuMax` já é 5, offers.go), item "mais opções" abrindo a
lista completa — nunca despejar a tabela de capacidades no terminal.

### P10 — Poucas escolhas, ordenadas por probabilidade; default óbvio
Lei de Hick-Hyman: tempo de decisão RT = a + b·log2(n+1) (Hick 1952; Hyman 1953; resumo em
ixdf.org/literature/article/hick-s-law-making-the-choice-easier-for-users). A "rule of large menus"
do ESR (*The Art of Unix Usability*, catb.org/esr/writings/taouu/html/ch04s03.html): como o crescimento
é logarítmico, UM menu bem ordenado vence submenus — e item frequente seleciona-se muito mais rápido
que a lei sugere, então ordene por probabilidade e faça do primeiro o Enter-default.

### P11 — Offline é qualidade, não fallback: comunique a economia
Local-first software (Kleppmann, Wiggins, van Hardenberg, McGranaghan, Ink & Switch 2019,
inkandswitch.com/essay/local-first/): "fast" — operações sem round-trip de rede — é o ideal #1, não
uma degradação. O cérebro offline entrega latência ~0, custo 0, reprodutibilidade e privacidade: isso
é *feat*. A linha de status que diz só "sem modelo" enquadra a vitória como perda; acrescentar
"0 tokens · resposta local" muda o frame sem mentir nada.

### P12 — Conversa, não ritual: teclado, fuzzy, e o programa que "faz o que eu quis"
Command Line Interface Guidelines (clig.dev): human-first, "conversation as the norm", descoberta,
robustez em terminal ruim. História das command palettes (Guay, techinch.com/blog/the-history-of-command-palettes):
a mágica da paleta é "dizer o que queria e o programa fazer" — fuzzy match, sem decorar comando. O
jazz já encarna isso (palette `/`, `fzOffer` "você quis dizer", fuzzy no NLU): o offline deve falar a
mesma língua — opção acionável com número/letra, não prosa.

---

## 2. Mapa file:line — o que o jazz já faz

Legenda: **feito** = existe e funciona · **parcial** = existe mas com furo para o caso offline ·
**só pesquisado** = capacidade nos dados/backend, sem UX ainda.

### 2.1 Online (referência — é com isto que o offline tem que empatar)

| Comportamento | Onde | Status |
|---|---|---|
| Ack imediato `Thinking("preparando")` antes de qualquer trabalho | main.go:1329-1331 | feito |
| Statusline viva: elapsed, modelo, fase, tokens ↓, "esc pra interromper", texto digitado | ui.go:848-871 (`Thinking`), ui.go:874-923 (`spinLine`), ui.go:925-932 (`StopThinking`) | feito |
| Fases nomeadas: `conectando` / `aguardando 1º token` / `gerando` / `parado há Ns` | ui_live.go:391-404, 425-442 | feito |
| Budget de redraw p/ TTY lento (8 fps normal / 2 fps low-power; redraw só do glyph) | ui_live.go:83-104, 56-74 (`uiGlyphOnly`), 294-337 (`liveFast`) | feito |
| Menu colapsado no statusline após 5 s sem 1º token; dígito executa ação | ui_offers.go:14, 25-30, 34-49; offers.go:565-578 | feito |
| Confirmação de intenção do atalho online: "⚡ atalho: `cmd`" + Choose | offline.go:429-435 | feito |
| Execução com WatchTurn + `Thinking("rodando")` + `ShellOutput` + aviso de exit/tempo | offline.go:440-453; ui.go:934-950 | feito |
| Tool calls como linhas com dot verde/vermelho, duração ≥1 s, `⎿` resumo | ui.go:986-998 (`ToolStart`), 1000-1084 (`ToolEnd`) | feito |
| Fila durante o turno: digitar → fila/steer; fila visível sob o statusline | ui.go:1725+ (`WatchTurn`), ui.go:1778 (`qKey`), queue.go:436-501, 617-645 | feito |
| Menus boxed: setas/letra/dígito/Enter/Esc; TERM=dumb imprime uma vez e aceita número | menu.go:401-483 (dumb: 405-420); graça/idle: 451-463 | feito |
| Confirmação de intenção com typo de 1 tecla ("li 'x' como 'y'") | brain.go:136-144 (gate), 197-209 (texto), 663-674 (Ask→Choose) | feito |
| Permissões em lote, grace period, batch | menu.go:451-463; ui.go:727-734; ui_batch.go | feito |
| Resumo de turno "✻ T · N ferramentas · ctx N% · $" | ui.go:1683-1706 (`uiTurnLine`) | feito |
| Footer: modo · modelo · preço/grátis · ctx% · mcp · jobs · "? atalhos" | ui.go:2203-2266 (`Footer`); main.go:1241-1266 | feito |
| startLine: modelo · ~Ns/resposta · reserva · flaky/dead · probe bg | offers.go:630-669; main.go:1160 | feito |
| `/undo`, `/rewind` (checkpoint por turno, bash rastreado) | checkpoint.go (cpAfterBash ~:464); UX.md | feito |
| UX de bloqueio do guard: o quê/porquê/próximo passo, quebra de espiral | blockux.go | feito |
| Level-0 online: intent read-only entendido resolve sem modelo + Notice | brain.go:646-693 | feito |

### 2.2 Offline (o que já existe)

| Comportamento | Onde | Status |
|---|---|---|
| Banner ao entrar: motivo + o que funciona + scripts + "verifico a rede a cada 30 s" | offline.go:310-324 → 198-217; `olScriptsView` 243-262 | feito |
| Footer offline com countdown de probe e `/online` | offers.go:673-688 (`olFooter`); main.go:1252-1256 | feito (mas com ⚠ permanente — ver gap G7) |
| Probe de fundo 30 s + aviso "rede de volta → modo online" | offers.go:587-610 (`olWatch`), ~605 (AsyncNotice); ui_offers.go:89-101 | feito |
| Escolha por número/prefixo na próxima mensagem (menu não bloqueia digitação) | offers.go:253-277 (`olPick`), 330-347 (`olOffer`/`olTakePick`); offline.go:348-351 | feito |
| Menu contextual de miss: arquivos citados, erro file:line, termo de busca, testes, git, resumo, /online | offers.go:158-209 (`olMenu`), 280-294 (`olMenuView`), 313-327 (`olMenuState`, git 2 s cap) | feito (genérico — ver G3) |
| Ação executada "como tool call": `ToolStart`+`ToolEnd`+corpo + permissão antes | offers.go:351-413 (`olRun`), 424-440 (`olExec`, sh -c, cap 256 KB) | parcial — exec em si é cega (ver G1/G6) |
| Cérebro offline no REPL: parse → método → verificação → statusline | brain.go:698-726 (`brInteractive`) | parcial — sem feedback durante `brRun` (G1) |
| Statusline pós-execução "· cérebro offline: X · feito e verificado · N evidências · M comandos · P perguntas" | brain.go:482-492 (`brStatusLine`) | feito (sem prova/economia — ver G5/G9) |
| Pergunta interativa do cérebro via `ui.Choose` + "Nenhum desses" | brain.go:704-715 | feito |
| Confirmação TypoOnly de 1 tecla | brain.go:136-144, 197-209 | parcial — opção "Mandar pro modelo" é beco sem saída offline (G2) |
| `!cmd` com UX completa (WatchTurn+Thinking+ShellOutput) e menu pós-falha | main.go:1416-1436 (menu: 1434-1435 via `olShellFailMenu`, offers.go:211-241) | feito |
| `/script` com `Thinking`+WatchTurn | offline.go:504-523 | feito |
| Undo coberto (BeginTurn/AfterWrite/Undo no brEnv) | methods.go:169-174; brain.go:699-700 | feito |
| Verificação real: `succeeded` exige evidência; métodos têm `Verify` | task.go:160-162; methods.go:54-72 (`mhCheck`, `mhMethod.Verify`); brVerify ~methods.go:1680 | feito (backend) → só pesquisado na UX (G4/G5) |
| Multi-intenção ("a e depois b") | brain.go:90-109 (`brRunMulti`) | feito |
| Fuzzy slash "você quis dizer" | fuzzy_cmd.go:224-238 | feito |
| Probe antes de tentar o modelo ("testando a rede") | offline.go:390-397 | feito |
| Hint de typo em símbolo após busca vazia | offers.go:404-407; nameindex.go:908-923 | feito |
| Estado de tarefa explícito com plano por passos e decisões | task.go (Plan/Evidence/Decisions); "interpretado: evidências" task.go:165 | só pesquisado — nunca renderizado (G4) |
| Alternativas de intenção (top-k) `in.Alt` | nlu.go:56; populado nlu.go:946-947, 963-964 | só pesquisado — nunca vai pra UI (G3) |
| Frases honestas de miss ("talvez X conf Y" / "entendi X mas não tenho método" / lista de capacidades) | brain.go:211-219 (`brNotUnderstood`), 152-153 | só pesquisado — `brInteractive` descarta (brain.go:717-719) |

### 2.3 Onde o offline fica atrás (gaps)

**G1 — Silêncio no trabalho.** Online: `Thinking` em <1 frame + fases + Esc (main.go:1331,
ui_live.go:425-442). Offline: `brRun` roda no escuro (brain.go:716), `olExec` é `CombinedOutput`
seco (offers.go:394, 424-440). `!` e `/script` já têm o padrão (main.go:1420-1425,
offline.go:518-523) — `olRun` e `brInteractive` são os únicos caminhos *sem* statusline.

**G2 — Sem eco de intenção.** Online: "⚡ atalho: `go test ./...`" + Choose (offline.go:429-435).
Offline: roda direto, e o usuário descobre a interpretação só pelo comando impresso (offline.go:420-427,
offers.go:392). E o confirm de typo oferece "Mandar pro modelo" que não existe offline
(brain.go:138) — escolhê-lo cai no miss e o texto do miss é descartado (brain.go:146-148 → 717-719).

**G3 — Miss sem memória.** O que o NLU entendeu (`in.Alt`, confiança, "entendi X mas não tenho
método") é jogado fora no interativo (brain.go:717-719) e vira um menu genérico
(offline.go:383-395). No headless vira parágrafo morto (brain.go:211-219 → main.go:889-892).
É o "parágrafo morto" do brief: headless literal; interativo, meio-morto (menu sem contexto do pedido).

**G4 — Passos invisíveis.** O plano do método existe (`TaskState.Plan`), mas nenhum passo renderiza;
o usuário vê só o bloco final (brain.go:720-722). Headless emite `ToolStart("brain")` retroativo
(brain.go:549). O online mostra cada tool call conforme acontece (ui.go:986-1084).

**G5 — Prova enterrada.** "feito e verificado" (brain.go:486-487) é o veredito sem a prova; os checks
(`brChecks`, brain.go:473-479; `mhMethod.Verify`, methods.go:67) não viram linhas "conferi: …".

**G6 — Sem Esc e sem fila.** `olExec` e `brRun` não instalam `WatchTurn`: Esc não cancela
(o `ctx` vem de `send`, não é cancelável pelo usuário) e o que se digita vai pro buffer do TTY, não pra
fila — exatamente o contrário do online (ui.go:1725+; queue.go:436-501).

**G7 — Enquadramento de alarme.** Footer offline usa `⚠` dourado permanente (main.go:1252-1256) e o
texto "sem rede · nova tentativa em Ns" (offers.go:683-687) descreve falha, não modo. `startLine`
anuncia "modelo X · ~Ns por resposta" mesmo com offline forçado (chamada em main.go:1160 *antes* de
`olStart` em 1161).

**G8 — Sem continuidade.** Depois de `olRun` falhar, não há próximo passo (só o notice de exit);
menu pós-falha existe só para `!cmd` (main.go:1434-1435). Resultado `failed`/`unverified` do cérebro
termina a conversa (brain.go:723). O online tem o modelo propondo "quer que eu olhe o erro?".

**G9 — Economia invisível.** `brStatusLine` conta evidências/comandos/perguntas (brain.go:491) mas
não diz "0 tokens" nem compara com os "~Ns por resposta" que o startLine prometia para o modelo
(offers.go:630-669): a vitória determinística sai muda.

**G10 — stream-json sem liveness.** `Thinking`/`StopThinking` são no-ops no jsonui (progresso não é
evento), então integrações headless perdem o heartbeat que o TUI tem.

---

## 3. Propostas

Ordenadas por impacto/esforço. Custos assumem o Dell (1 CPU i686, 2 GB, TTY lento): "bytes" = saída
extra; "CPU" = trabalho por request; nenhuma proposta adiciona goroutine nova nem dependência.

### P1 — Statusline real em TODA execução offline
**Mecanismo.** Em `olBefore`, embrulhar `brRun` e `olRun` com o mesmo trio do `!`/online:
`WatchTurn(cancel)` + `Thinking(fase)` + `StopThinking`. Para `olExec`, trocar `CombinedOutput` por
`Shell.Run(ctx, cmd, …, ui.ShellOutput)` (igual main.go:1423) ou manter `CombinedOutput` e só
mostrar a última linha no spinner via `Phase()`; preferir `Shell.Run` = streaming real. Fases:
`interpretando` (antes do parse NLU), `rodando <cmd>` (ações), `verificando` (checks de método).
Esc cancela (ctx já existe em `olExec`); teclas viram fila (WatchTurn já resolve).

```
ANTES (pedido "rode os testes" offline):
> rode os testes
· go test ./...                                       <- a linha da chamada aparece, e então…
                                                        ~1,4 s de cursor morto: travou?
ok    example.com/proj    0.9s                          <- resultado despejado de uma vez
· exit 0 · 1.4s · atalho test
· cérebro offline: run_tests · feito e verificado · 3 evidências · 1 comando · 0 perguntas

DEPOIS:
> rode os testes
· go test ./...
⠋ rodando · 0.4s · ok proj (2/3)                       <- vivo desde o 1º frame; esc cancela;
ok    example.com/proj    0.9s                               digitar vai pra fila
· exit 0 · 1.4s · atalho test
· cérebro offline: run_tests · feito e verificado · 3 evidências · 1 comando · 0 perguntas
```
**Arquivos.** offline.go:419-427 (`olFastPath` offline → Thinking antes do olRun), offers.go:351-413
(`olRun` → WatchTurn+Thinking+Shell.Run), brain.go:704-726 (`brInteractive` → envolver brRun com
Thinking("interpretando")→Phase por estágio), ui_live.go (fases novas; reusa `Phase`).
**Custo Dell.** +1 ctx cancel, reuse da goroutine de render existente; `Shell.Run` stream já é o
caminho de `!`. Bytes: mesmo orçamento do online (~310 B/s waiting, medido em ui_bench_test.go).
**Métrica.** tempo até 1º byte visível (<100 ms), abandono em ação >5 s, "achou que travou" (suporte).
**Impacto 5 · Esforço S · Risco baixo** — padrão já provado em 3 call sites.

### P2 — Eco de intenção sempre; confirmação de 1 tecla só quando precisa
**Mecanismo.** Antes de executar, imprimir o feedforward: `· entendi "rode os testes" como run_tests —
go test ./...`. Regra graduada (Horvitz: precisão ∝ incerteza; P6):
- confiança alta + comando read-only (`mhMethod.Risk=="low"` ou `cbReadCmd`) → só eco, executa;
- `in.TypoOnly` ou risco médio/alto → Choose de 1 tecla (`Rodar agora` / `Outra ação` / `Cancelar`);
- **bug fix:** a opção "Mandar pro modelo" (brain.go:138) vira "Escolher outra ação" quando
`a.ol.on||a.ol.forced` — hoje é um botão que leva a lugar nenhum.

```
ANTES (typo, offline):
> atualzia o jazz
sem modelo · nova tentativa em 18s · /online agora       <- interpretação invisível; menu genérico

DEPOIS:
> atualzia o jazz
li "atualzia" como "atualiza" — atualizar o jazz?
  1 Rodar agora          s
  2 Escolher outra ação  o
(esc cancela)
```
**Arquivos.** brain.go:136-144 (opções do Ask por contexto), 197-209 (texto), 698-715 (env.Ask),
offline.go:419-427 (eco antes de olRun no fast path), offers.go:392 (ToolStart já mostra o cmd —
complementar com a intenção).
**Custo Dell.** 1 linha no caminho rápido; Choose = 1 frame. Zero trabalho extra.
**Métrica.** perguntas feitas vs necessárias (eval já mede); taxa de "não era isso" (cancel após eco);
tempo até 1ª ação.
**Impacto 4 · Esforço S · Risco médio** — uma linha a mais por request pode enjoar se redundante com o
ToolStart; mitigar fundindo: `· atalho run_tests → go test ./...` já serve de eco + execução.

### P3 — Miss honesto com top-k: o menu lembra o pedido
**Mecanismo.** Hoje `brInteractive` joga fora `r.Text` quando `!Handled` (brain.go:717-719). Em vez:
(1) imprimir a linha honesta do que se entendeu — "não tenho certeza (talvez `run_tests`, confiança
0,52)", "entendi `open_file` mas não tenho método offline", ou "não entendi" puro; (2) montar o menu
com top-k real: para cada `in.Alt` que tem método em `mhLibrary()`, um `olAction{Kind:"brain"}` que
roda aquele intent direto; (3) os itens contextuais de `olMenu` como fallback; (4) `/online`/esperar
quando aplicável. Título do menu = o pedido do usuário, para ancorar.

```
ANTES (headless -p --offline "sobe o front"):
não entendi o pedido sem o modelo. Posso: listar e buscar arquivos, resumir o projeto,
status/diff/log do git, rodar testes/build/lint/format, criar/editar/renomear/apagar
arquivos (com /undo), processos/portas/disco/memória, serviços, containers, instalar
dependências, commit/push/pull com confirmação

DEPOIS (interativo "sobe o front"):
> sobe o front
· quase entendi: talvez service_start (confiança 0,54) — não tenho método offline pra "front"
sem modelo agora (testo a rede de novo em 22 s) — "sobe o front", o que dá pra fazer:
  1 service_start nginx                    s
  2 docker compose up (container web)      d
  3 listar portas/serviços                 l
  4 esperar o modelo (rede voltou?)        m
digite o número (ou o começo da opção); qualquer outro texto vai como pedido normal
```
**Arquivos.** brain.go:717-719 (não descartar; retornar a explicação), 152-153, 211-219; nlu.go:56,
946-964 (`in.Alt` já populado); offers.go:158-209 (olAction Kind "brain" — pode reaproveitar olRun
com um mini-despacho ou chamar brRun com texto sintético); offline.go:379-398 (montar o menu).
**Custo Dell.** `in.Alt` já computado; `mhLibrary()` já carregado (sync.Once). Bytes: ~6 linhas.
**Métrica.** miss→ação (share de picks no menu vs retype), abandono pós-miss, "menu certo no top-1".
**Impacto 5 · Esforço M · Risco médio** — sugestão errada de Alt parece burra; gate: só oferecer Alt
com método existente e margem mínima vs. none.

### P4 — "Conferi": a prova da verificação em linguagem humana
**Mecanismo.** `brVerify` já roda `mhMethod.Verify` (methods.go:67, ~1680); cada check bem-sucedido
vira uma linha `⎿ conferi: <check> — <evidência>` no fim do corpo, e `brStatusLine` ganha a prova
curta: "feito e verificado · porta 8080 respondeu em 40 ms". Checks: `exit 0`, `port_free`→"porta
8080 respondeu", `tests`→"42 ok · 0 falhas" (dados de olTestSummary), `file_exists`→"arquivo existe".
Sem prova, NUNCA dizer verificado — `tkUnverified` já existe e diz "feito, NÃO verificado"
(brain.go:486): manter a distinção visível é a confiança calibrada de Lee & See (P5).

```
DEPOIS:
· criar pasta docs
· conferi: docs/ existe (stat) · 12 ms
· cérebro offline: create · feito e verificado · 2 evidências · 1 comando · 0 perguntas · 0 tokens

ANTES:
· criar pasta docs
· cérebro offline: create · feito e verificado · 2 evidências · 1 comando · 0 perguntas
```
**Arquivos.** methods.go (brVerify → emitir linhas via env ou coletar no Task), brain.go:482-492
(status line com prova curta), task.go:160-165 (evidência → texto humano), offers.go:396-413
(sumário de ação já faz "exit 0 · T" — estender com "conferi" quando houver check).
**Custo Dell.** Formatação de strings do que já é computado. ~2 linhas de saída.
**Métrica.** % de tasks `succeeded` com ≥1 linha de prova visível; re-verificação manual do usuário
(usuário rodando o check de novo depois → sinal de desconfiança).
**Impacto 4 · Esforço S-M · Risco baixo.**

### P5 — Chip de modo de 3 estados, sem sirene
**Mecanismo.** O footer offline hoje é sempre `⚠ sem rede · …` (main.go:1252-1256). Trocar por chip
de estado com vocabulário próprio e glyph neutro:
- online → como hoje (modelo aparece, sem marca extra);
- offline forçado (`--offline`/JAZZ_OFFLINE) → `⚡ offline · /online volta` (bolt = "modo rápido");
- offline por queda → `◌ sem rede · tentando de novo em Ns · /online agora`;
- instável (probe flap: alternou nas últimas N sondas) → `~ rede instável · ficando no offline`.
`olBanner`/`AsyncNotice` continuam sendo os *eventos*; o footer vira *estado* (P4). `startLine`:
quando `ol.forced` já é conhecido, imprimir "offline: atalhos determinísticos + cérebro ativos —
/online volta" em vez de "modelo X · ~Ns" (main.go:1160 vs 1161).

```
ANTES (forçado):
  ⚠ sem rede · nova tentativa em 12s · /online agora · ⚙ auto · ? atalhos

DEPOIS:
  ⚡ offline · modo rápido · /online volta · ⚙ auto · ? atalhos     (forçado)
  ◌ sem rede · tentando em 12s · /online agora · ⚙ auto · ? atalhos (queda)
  ~ rede instável · ficando no offline · ⚙ auto · ? atalhos         (flap)
```
**Arquivos.** offers.go:673-688 (`olFooter`: 3 ramos + flap), main.go:1252-1256 (glyph por estado,
não warn fixo), offers.go:630-669 (`startLine` offline-aware), ui.go:285 (`uiModeTag` pode ganhar
"offline" como tag de primeiro nível).
**Custo Dell.** Strings; o flap conta probes já existentes (`olWatch` guarda resultados).
**Métrica.** compreensão do estado ("sei em que modo estou" → survey/benchmark); queixas de "modo
quebrado"; tempo até descobrir `/online` (hoje depende do hint no footer — manter).
**Impacto 3 · Esforço S · Risco baixo** — cuidado para não esconder a informação "sem rede": a
contagem de probe e o `/online` continuam sempre presentes.

### P6 — Passos do método como tool-lines
**Mecanismo.** O plano do método já é por passos (`TaskState.Plan`); o backend decide cada passo e
marca status. Adicionar `env.StepUI func(i, n int, name string, ok bool)` chamado pelo executor
entre passos: renderiza `● passo 2/4 · git status` / `  ⎿ ok · 40 ms` (ou `✗ falhou — motivo`).
Gate: só para métodos com >1 passo OU quando um passo leva >300 ms — um método de 1 passo rápido não
merece ruído. É o mesmo vocabulário visual dos tool calls online (ui.go:986-1084): paridade de
sintaxe = paridade percebida.

```
DEPOIS (método kill_process, 3 passos):
● passo 1/3 · achar dono da porta 8080
  ⎿ pid 1337 (node) · 38 ms
● passo 2/3 · confirmar kill         <- pergunta via Choose, já existe
● passo 3/3 · kill 1337
  ⎿ ok · 21 ms
· conferi: porta 8080 livre · 40 ms
· cérebro offline: kill_process · feito e verificado · 4 evidências · 1 comando · 1 pergunta
```
**Arquivos.** brain.go:258-310 (`brExecute`: o loop de passos já marca `ps.Status` running/failed —
chamar `env.StepUI` ali dentro), methods.go:158-182 (`brEnv` + campo `StepUI`), brain.go:698-726
(instalar `env.StepUI` apontando para `ui.ToolStart`/`ToolEnd` leves ou `ui.print` direto), ui.go
(helper `stepLine` se ToolStart for pesado).
**Custo Dell.** 1–2 linhas por passo; nada de goroutine. Se `ui.print` for caro em dumb, acumular e
imprimir em bloco — dumb já imprime o menu inteiro de uma vez.
**Métrica.** percepção de travamento em método multi-passo; "sei o que ele está fazendo" vs. bloco
final único; abandono em task >5 s.
**Impacto 4 · Esforço M · Risco baixo** — respeitar o gate para não virar log spam (P9 anti-ruído).

### P7 — Selo de economia: "· 0 tokens" e o "⚡" consistente
**Mecanismo.** `brStatusLine` ganha o sufixo `· 0 tokens` (brain.go:491) e, quando o startLine anunciou
"~Ns por resposta" do modelo, uma comparação honesta ocasional: `· resposta local · 0 tokens · 1.4s`
(o modelo prometia ~8s e quota). O fast path offline marca o ToolStart com `⚡ atalho test`
(offers.go:392/`olAction.Label`) para casar com o "⚡ atalho" online (offline.go:429). Frase de
enquadramento no banner: trocar "sem modelo" por "modo rápido local" *junto* da informação de rede —
nunca esconder o estado (P4/P5).
**Arquivos.** brain.go:482-492; offers.go:351-413 (Label), 630-669 (startLine); offline.go:198-217
(`olBanner` — já lista o que funciona; acrescentar o selo).
**Custo Dell.** Strings.
**Métrica.** enquadramento percebido ("offline é limitação" → "offline é modo"), uso repetido do
`/offline` voluntário (sinal de preferência).
**Impacto 3 · Esforço S · Risco baixo** — não exagerar a ponto de soar marketing; uma marca por turno basta.

### P8 — Continuidade: o menu de "e agora?" depois de cada resultado
**Mecanismo.** Hoje o menu pós-falha existe só para `!cmd` (main.go:1434-1435). Generalizar: depois de
`olRun` com exit≠0 OU `brRun` com `failed`/`unverified`/`blocked`, oferecer 2–3 próximos passos via
o MESMO `a.menu`+`olTakePick` (zero protocolo novo):
- testes falharam → "localizar o erro (file:line)" (já derivado por `olMenu` via `ui.lastErrText()`),
  "rodar só o pacote que falhou", "procurar o símbolo";
- comando falhou → `olShellFailMenu` (já existe: sudo/manual/erro/procurar);
- unverified → "ver o que faltou verificar" / "rodar o check manualmente";
- sucesso → nada (menu só em não-sucesso: P9 — não perguntar sem custo-benefício; P6).

```
DEPOIS:
> rode os testes
· go test ./...
--- FAIL: TestParse (0.02s)
    parser_test.go:41: ...
· exit 1 · 1.2s · atalho test
testes falharam — quer:
  1 abrir parser_test.go:41                 o
  2 rodar go test -run TestParse ./...      r
  3 procurar "ParseConfig"                  p
digite o número (ou o começo da opção); qualquer outro texto vai como pedido normal
```
**Arquivos.** offers.go:351-413 (`olRun`: falha → olOffer com itens derivados de `olMenuState`),
brain.go:716-723 (status terminal não-sucesso → menu), offers.go:212-241 (`olShellFailMenu` como
template), offline.go:383-398 (mesmo mecanismo do miss).
**Custo Dell.** Reusa `olMenuState` (git probe com cap 2 s já existe — reutilizar o resultado, não
refazer). ~4 linhas de saída só quando falha.
**Métrica.** conversão para próxima ação útil (turnos até o objetivo), "saber o que fazer depois do
erro" (error-recovery do NN/g #9), false_blocks percebido.
**Impacto 4 · Esforço M · Risco baixo** — garantir que o menu some no próximo input normal
(`olTakePick` já limpa, offers.go:340-347).

---

## 4. O que NÃO fazer

1. **Não fabrique latência nem progresso.** Sleep pra "animar", spinner sem trabalho real, barras
   percentuais estimadas de mentira — destrói confiança calibrada (Lee & See) e no Dell é só custo.
   O offline é *realmente* rápido; mostre isso.
2. **Não deixe loop infinito sem fase.** NN/g: acima de ~10 s um spinner nu parece travamento e leva a
   abandono. Se vai passar de 10 s, nomeie a fase e os segundos — o `Phase()` já existe.
3. **Não use ⚠/vermelho permanente para estado normal.** Alerta contínuo vira ruído (alarm fatigue) e
   enquadra o modo como erro (P4). `⚠` é para evento e atenção real; modo é glyph neutro.
4. **Não finja que foi o modelo.** Esconder "sem modelo" pra parecer online destrói confiança quando
   a resposta for mais fraca — e elimina o enquadramento de economia (P11). Digamos quem respondeu.
5. **Não invente um segundo protocolo de escolha.** Reusar `olAction`/`olPick`/`olOffer`/`olTakePick`
   (número/prefixo, não bloqueia digitação). Um menu boxed `mnSpec` para cada oferta rápida seria
   fricção; um boxed para confirmação de risco já é o padrão (menu.go).
6. **Não confirme tudo.** "entendi X?" em todo request mata o fast path (Horvitz: custo da pergunta;
   Rao & Daumé: EVPI). Regra: confirma quando incerto (typo, baixa margem, Alt próximos) ou quando
   arriscado (`mhMethod.Risk` ≠ low); do contrário, só ecoe.
7. **Não bloqueie a digitação durante execução.** O usuário já pode digitar durante turno online
   (fila). Fechar o input offline é regressão, não robustez — `WatchTurn` de graça resolve.
8. **Não re-execute trabalho caro por request.** `fpDetect`, `git status` probe (2 s cap,
   offers.go:321-325), parse NLU — cada um uma vez por request e compartilhado entre olMenu, fast path
   e brain. No 1-CPU do Dell, uma segunda sonda de git é 2 s a mais de tela parada.
9. **Não dependa de cor/Unicode/cursor.** `TERM=dumb`, sem cor, SSH: o menu já imprime linhas e aceita
   número; o spinner tem glyph ASCII (`| / - \`); propostas novas devem funcionar com `+---`/`ok`/`x`.
   `olMenuView`/`mnSpec` já degrada — manter a disciplina.
10. **Não adicione goroutine de render nem alt-screen.** O live region existe e é medido
    (ui_bench_test.go: ~310 B/s waiting, ~1,1 KB/s streaming). Reusar `Thinking`/`Phase`/`liveFast`;
    um segundo renderizador compete pelo 1 CPU e pelo TTY.
11. **Não transforme miss em parágrafo morto.** O problema declarado do brief: nunca devolver só texto
    explicando incapacidade. Título honesto + ações. (Headless hoje faz exatamente isso —
    brain.go:211-219 + main.go:889-892 — o fix é P3.)
12. **Não ofereça caminho morto.** "Mandar pro modelo" offline (bug atual, brain.go:138), `/online`
    quando o probe já disse que não tem rede, "tentar de novo" sem mudança — cada um é uma promessa
    quebrada. Cada item do menu tem que executar de verdade.
13. **Não rode ação sem permissão.** Menu → `olAction` → `PermitCommand` antes do Cmd
    (offers.go:388-390 já faz no caminho shell; o novo Kind "brain" passa pelo `env.Permit` do brain).
    "1 tecla" nunca pula o guard.
14. **Não despeje o catálogo de capacidades.** O parágrafo do miss (brain.go:215-218) lista 10+
    habilidades — é a antítese do progressive disclosure (P9). Top-k com default, resto sob "mais".
15. **Não confunda "sucesso" com "verificado".** `tkUnverified` existe justamente para isso
    (brain.go:486); dizer "feito e verificado" sem check é a desonestidade que o modo offline não
    pode ter — é o diferencial dele (P5).
16. **Não quebre a fila durante menus.** O menu de miss não deve comer a próxima mensagem do usuário
    como pick se ela não parecer pick — `olPick` já exige dígito/prefixo exato (offers.go:253-277);
    manter essa especificidade.

---

## 5. Métricas de paridade (como saber que chegou)

A suíte `jazz eval offline` já mede perguntas feitas vs necessárias, taxa de resolução sem LLM,
p50/p95 de latência e false_blocks — o que falta medir é o *percebido*:

| Métrica | Hoje | Alvo |
|---|---|---|
| Tempo até 1º byte visível pós-Enter (offline) | trabalho inteiro (~0,5–2 s de silêncio) | <100 ms (1 frame) |
| Cobertura de statusline durante execução offline | `!` e `/script` têm; `olRun`/`brRun` não | 100% dos caminhos |
| Miss → ação oferecida com contexto do pedido | menu genérico (interativo), parágrafo (headless) | top-k do `in.Alt` + contextuais |
| Confirmação de intenção visível | só TypoOnly (e com opção morta) | eco sempre; 1 tecla quando incerto/risco |
| Prova de verificação visível | "verificado" comprimido no statusline | 1 linha `conferi:` por check |
| Perguntas com opção impossível (bug) | "Mandar pro modelo" offline | 0 |
| Enquadramento do modo | "⚠ sem rede" contínuo | chip neutro de estado + economia "0 tokens" |
| Esc/fila durante ação offline | não funciona | idem online |

## 6. Referências

- Nielsen, J. — *Response Times: The 3 Important Limits*. NN/g. nngroup.com/articles/response-times-3-important-limits/
- Nielsen, J. — *10 Usability Heuristics* (#1 visibilidade, #6 reconhecimento>memória, #9 erros). nngroup.com/articles/ten-usability-heuristics/
- Nielsen, J. — *Progressive Disclosure*. nngroup.com/articles/progressive-disclosure/
- Nielsen, J. — *Progressive Disclosure: From Training Wheels to AI Agents* (revisão 2025). jakobnielsenphd.substack.com/p/progressive-disclosure
- NN/g — *Progress Indicators Make a Slow System Less Insufferable*. nngroup.com/articles/progress-indicators/
- NN/g — *Error-Message Guidelines*. nngroup.com/articles/error-message-guidelines/
- NN/g — *Improving the Dreaded 404 Error Message*. nngroup.com/articles/improving-dreaded-404-error-message/
- Maister, D. — *The Psychology of Waiting Lines* (8 proposições: tempo ocupado, começar, ansiedade,
  incerteza, explicação, justiça, valor, grupo). davidmaister.com/articles/5/52/
- Harrison, C., Yeo, Z., Hudson, S. — *Faster Progress Bars: Manipulating Perceived Duration with
  Visual Augmentations*. CHI'10. dl.acm.org/doi/10.1145/1753326.1753556
- Apple — *Human Interface Guidelines: Progress indicators* (determinado>indeterminado).
  developer.apple.com/design/human-interface-guidelines/progress-indicators
- Material Design 3 — *Progress indicators* (<200 ms nada; 200 ms–5 s loading; >5 s progresso).
  m3.material.io/components/progress-indicators/guidelines
- Lee, J., See, K. — *Trust in Automation: Designing for Appropriate Reliance*. Human Factors 46(1),
  2004. doi.org/10.1518/hfes.46.1.50_30392
- Parasuraman, R., Sheridan, T., Wickens, C. — *A Model for Types and Levels of Human Interaction
  with Automation*. IEEE TSMC-A 30(3), 2000. doi.org/10.1109/3468.844354
- Horvitz, E. — *Principles of Mixed-Initiative User Interfaces*. CHI'99. erichorvitz.com/chi99horvitz.pdf
- Rao, S., Daumé III, H. — *Learning to Ask Good Questions: Ranking Clarification Questions using
  Neural Expected Value of Perfect Information*. ACL 2018. aclanthology.org/P18-1255/
- Sellen, A., Norman, D. — *The Psychology of Slips* (mode errors). Em *Experimental Slips and Human
  Error*, Plenum, 1992. microsoft.com/en-us/research/publication/experimental-slips-human-error-exploring-architecture-volition/
- Norman, D. — *Categorization of Action Slips*. Psychological Review 88(1), 1981.
- Card, S., Moran, T., Newell, A. — *The Psychology of Human-Computer Interaction* (KLM: 1 tecla ≈ 0,2 s). 1983.
- Hick, W.; Hyman, R. — *Hick-Hyman law* (RT = a+b·log2(n+1)). Resumo: ixdf.org/literature/article/hick-s-law-making-the-choice-easier-for-users
- Raymond, E. — *The Art of Unix Usability* (regra dos menus grandes). catb.org/esr/writings/taouu/html/ch04s03.html
- Kleppmann, M., Wiggins, A., van Hardenberg, P., McGranaghan, M. — *Local-first software: You own
  your data, in spite of the cloud*. Ink & Switch, 2019. inkandswitch.com/essay/local-first/
- *Command Line Interface Guidelines* (human-first, conversa, descoberta, robustez). clig.dev
- Guay, M. — *The History of Command Palettes* (fuzzy, "diga o que quer"). techinch.com/blog/the-history-of-command-palettes
