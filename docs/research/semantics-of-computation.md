# Semântica da computação no cérebro offline: o que é teorema e o que é enfeite

**Teste adversarial da pergunta "como representar o SIGNIFICADO de um pedido sem LLM", conceito por conceito —
com veredito, fonte primária e o `file:line` que decide.**

Pesquisa de 04/11/2026. Continua [unifying-math-offline.md](unifying-math-offline.md) (informação/MDL/busca),
[math-offline-brain.md](math-offline-brain.md) (calibração, distâncias, orçamentos),
[offline-brain-research.md](offline-brain-research.md) (agentes clássicos, NLU sem transformer) e
[verification-oracles-deep.md](verification-oracles-deep.md) (oráculos). O objeto aqui é o terceiro braço que
faltava: **semântica formal** — denotacional (Scott–Strachey), operacional (Plotkin SOS), axiomática (Hoare),
lambda-cálculo, Curry–Howard, teoria de tipos, teoria de categorias e a tradição de semântica executável de
língua natural (Montague → gramáticas semânticas → semantic parsing). Não repete o pipeline NLU nem a
matemática de decisão; pergunta-se outra coisa: **o que a teoria do significado cobra — e o que ela paga — no
cérebro que já existe**.

Convenções: `file:line` refere-se a `main` @ `a5ffe0f` (branch `harness/offline-primitives` na escrita).
**Medido** = número já registrado em `results/` ou contado agora sobre o código. **(estimativa)** = conta
minha, não medida. "Dell" = Dell Latitude D610, Pentium M 1,7 GHz, 1 núcleo, 2 GB, `linux/386`. Vereditos:
**LOAD-BEARING** = sustenta teorema e vira código/métrica · **METÁFORA** = enquadra mas não decide nada ·
**VIAGEM** = não compila no Dell.

---

## 0. Placar

| # | Peça | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | Semântica model-teórica para a NLU (mundo = modelo, slot = denotação) | **LOAD-BEARING** | É literalmente o que o código computa: `nuWorld` é um modelo de primeira ordem (`nlu.go:443`), `niResolveEntity` avalia a denotação (`nameindex.go:1035`), `brPick` implementa a regra "denotação unitária → age; não-unitária → pergunta" (`methods.go:1132`). É a análise de Russell de descrições definidas, rodando. |
| 2 | Semântica axiomática: `{Pre} método {Verify}` | **LOAD-BEARING** | O método já é um triplo de Hoare (`mhMethod.Pre`/`Verify`, `methods.go:60-72`); `brVerify` é a checagem de pós-condição em runtime (`methods.go:1680`). O teorema é fraco (prova de *uma* execução) mas é o contrato real do sistema — e a força dele se mede. |
| 3 | Semântica operacional: `TaskState` como sistema de transições | **LOAD-BEARING** | `tkTransitions` (`task.go:40-50`) *é* uma especificação SOS; `brExecute` é o interpretador que a executa. Dá o que SOS dá: indução estrutural, prova de terminação por função de ranking, invariantes testáveis. |
| 4 | Sistemas de efeitos (Lucassen–Gifford) | **LOAD-BEARING** | `Capability.Effects` existe e é a anotação certa (`caps.go:74`) — mas **ninguém lê**: o sistema de efeitos está declarado e desligado. Ligar = checar em O(passos) no load que métodos level-0 são só-leitura e que método com efeito `write/exec` tem verificador. |
| 5 | Curry–Howard: método verificado = prova | **LOAD-BEARING na fronteira, fraco no meio** | A leitura honesta é *proof-carrying execution* (Necula): a evidência é o termo de prova, `brVerify` o type-checker. A disciplina que paga de verdade é a da fronteira — **tipar a biblioteca**: o cheque estático achou um bug real hoje (`fix.diagnose` lê `$result` que ninguém produz). |
| 6 | Tipos: refinamentos, sortes, contratos | **LOAD-BEARING** | `mhCond` já é um refinamento sobre o ambiente (`methods.go:42`); slots já são sorted (`nuSlotDef.Kinds`, `nlu.go:84`); o que falta é o *type-checker* da DSL — 1 bug latente encontrado por 30 linhas de análise (§5). |
| 7 | STRIPS / operadores de ação (Fikes–Nilsson) | **LOAD-BEARING** | `backchain.go` implementa regressão STRIPS com custo mínimo — e é **código morto**: `Plan` só é chamada em teste. Ou liga (reparo de pré-condição) ou apaga. |
| 8 | Gramáticas semânticas (SHRDLU→LUNAR→CHAT-80→Phoenix→ATIS→Snips) | **LOAD-BEARING** | `nuRule`/`nuSlotDef` *são* uma gramática semântica com slots tipados — a tradição que provou em hardware pior que o Dell: CHAT-80 respondia geografia em <1 s em 1982; Snips rodava SLU em microcontrolador IoT. |
| 9 | Semantic parsing executável (Zettlemoyer–Collins, λ-DCS, overnight) | **LOAD-BEARING como forma e métrica · METÁFORA como aprendizado** | O pipeline texto→LF→execução→prova *é* um parser semântico executável. O que falta herdar é a métrica: acurácia de LF exata (intenção+slot+entidade), não só de intenção. Aprendizado por indução de CCG precisa de LF anotado que não temos — a forja é o caminho, não o parser. |
| 10 | Semântica denotacional plena (domínios, CPOs, pontos fixos) | **METÁFORA ÚTIL** | O DSL de métodos não tem recursão nem laço geral (só `retry_smaller` limitado, `brain.go:348`); não há ponto fixo pra tomar. O resíduo útil: composicionalidade e a ideia de `⟦método⟧ : Estado → Resultado` como especificação. |
| 11 | Lambda-cálculo e combinadores | **METÁFORA ÚTIL** | `subst` é avaliação por ambiente (`methods.go:220`); o LF é plano (frames, não λ-termos). Combinadores SKI para compor caps seriam um encoding, não um mecanismo. |
| 12 | Mônadas/Kleisli (Moggi) para composição de efeitos | **METÁFORA ÚTIL** | `brStep{val, obs, ok, why, stop}` é um cômputo monádico (State×Writer×controle); `brExecute` é a amarração Kleisli. Correto como descrição — mas nenhuma decisão muda por causa disso. Paga uma coisa: as leis de mônada são invariantes testáveis. |
| 13 | Lógica de separação / frame axioms | **METÁFORA ÚTIL com um dente** | O "frame" (o que um método NÃO muda) é o buraco real: nada garante que método level-0 não escreve. O'Hearn/Reiter dão o nome; o conserto é o sistema de efeitos da linha 4, não lógica nova. |
| 14 | Montague pleno: quantificadores, escopo, intensionalidade | **VIAGEM** | O domínio descarta quantificadores por design: `all`/`todos`/`some`/`any` estão na lista de **fillers** de `nlu/lexicon.json` — "apaga todos os testes" é parseado como "apaga os testes" e a cardinalidade é resolvida na denotação, não no LF. Sem quantificador não há escopo; PTQ inteiro não compila aqui. |
| 15 | CCG/λ-DCS *como parser aprendido em runtime* | **VIAGEM** | Induzir CCG precisa de centenas de pares sentença→LF (Zettlemoyer–Collins) ou pergunta→resposta (Berant–Liang); temos 644 exemplos sem LF. A gramática manual já cobre o domínio — e é verificável. |
| 16 | Tipos dependentes (Martin-Löf), provadores (Coq/Lean), lógica intensional, GOLOG, SMT para pré-condições | **VIAGEM** | Pre é conjunção ground, O(#conds); proposição = 49 nomes + 14 sortes. Nenhum SAT, provador ou λ-dependente compra nada a esse preço. |

**Resumo de uma frase:** a semântica não dá ao jazz um motor novo; dá **contratos verificáveis no motor que já
existe** — e a auditoria mostra que os contratos estão escritos (`Effects`, `Pre`, `Verifier`, `Operator`,
`Signal`) e **não ligados** (§5). A semântica executável é o que o pipeline já é; o que a teoria cobra é
provar isso por construção, não por esperança.

---

## 1. A pergunta, decomposta

> "Como o jazz representa o SIGNIFICADO de um pedido como forma lógica/tipo determinístico que casa com
> methods.go, SEM LLM em runtime?"

Em termos que a literatura responde, são quatro perguntas:

- **Q-LF.** Qual é a forma lógica? — Hoje: `Intent{Name, Slots, Confidence, Evidence, Ents, Missing, Hint}`
  (`nlu.go:48-59`). É uma *frame*: predicado de aridade 1-3 com slots sortados. Não é um λ-termo — e está
  certo que não seja (§3.8).
- **Q-Den.** Como o LF denota no mundo? — `niResolveEntity` calcula a denotação do slot no modelo `nuWorld`;
  `brPick` decide se a denotação é referencial. É semântica model-teórica rodando (§3.1).
- **Q-Contrato.** Como um método "casa" com o intent? — `mhApplicable` checa a pré-condição contra os fatos
  (`methods.go:141-153`); `brVerify` checa a pós-condição contra a execução (`methods.go:1680`). É o triplo
  de Hoare `{Pre} método {Verify}` (§3.3).
- **Q-Prova.** O que conta como "significado verificado"? — A execução `brExecute` produz evidências
  (`tkEv`, `task.go:79`); `To(succeeded)` exige ≥1 evidência (`task.go:160-162`). É a execução carregando a
  prova (§3.5).

E a pergunta meta: **o que de Curry–Howard/tipos vira validação estática do método forjado** — já que
`Effects`, `Pre` e `Verifier` existem e estão mortos?

---

## 2. A descoberta central: o pipeline já É um parser semântico executável

Lendo o código com os óculos de Montague, o cérebro offline é, peça por peça, a arquitetura clássica de
*executable semantic parsing* — sentença → forma lógica → denotação/programa → execução → resposta
verificada:

```
pedido
  │  nuTokenize + normalize (morfologia: fold, stem, gíria→conceito)        nlu.go:291,337
  ▼
LF: Intent{nome, slots: sorte→expr referencial, evidência}                 nlu.go:48,928
  │   gramática semântica (nuRule: all/any/none sobre conceitos+@sortes)    nlu.go:77-98,838
  │   + desempate NB (posterior temperado)                                  nlu.go:1337-1441
  ▼
denotação: hits (spot por Aho-Corasick sobre o mundo) → niResolveEntity    nlu.go:601; nameindex.go:1035
  │   |den|=1 → liga slot · >1 → pergunta decisiva · 0 → name_not_found    methods.go:1132,1157
  ▼
termo da DSL: mhMethod{pre: refinamento, steps: cadeia de caps, verify}    methods.go:60-72
  │   seleção: primeiro método com Pre⊨fatos (Pyhop flat)                  methods.go:141,261
  ▼
execução: small-step sobre TaskState (tkTransitions é a SOS)               task.go:40; brain.go:261
  │   recuperação por categoria observável (rvTable)                       recover.go:108
  ▼
prova: Evidence (tkEv) + brVerify (checagem do post na execução)           task.go:184; methods.go:1680
```

Duas consequências imediatas:

1. **Não falta semântica; falta disciplina de semântica.** Cada nível da torre já existe. O que falta é o que
   a teoria das linguagens sempre cobra: *checar os tipos na fronteira entre os níveis*. LF→denotação está
   ligado (o fill exige `kinds`); denotação→método está ligado (`pre`/`needs`); **método→caps está sem
   checagem** — `in`/`out`/`verify.arg` são strings num `map[string]string`, e `subst` devolve `""` para
   var indefinida em vez de erro (`methods.go:220-241`). O buraco semântico do sistema é esse: **a DSL de
   métodos não tem type-checker**.

2. **A biblioteca é o programa; o checker é o compilador.** Na filosofia do projeto (inteligência em tempo
   de compilação), `methods/*.json` + `nlu/*.json` são o asset forjado; a "compilação" desses assets é onde
   a semântica formal paga: validação estática, métrica de LF, auditoria de contratos — tudo O(biblioteca),
   uma vez, no load ou no CI, **zero custo por pedido no Dell**.

---

## 3. Conceito a conceito

### 3.1 Semântica model-teórica: o mundo é um modelo, o slot é uma denotação

**O que é.** Na tradição Tarski→Montague, significado é denotação numa estrutura M = ⟨D, I⟩: um domínio D de
indivíduos e uma interpretação I de predicados. Uma expressão referencial ("o parser.go") **presupõe**
existência e unicidade do referente (Russell, *On Denoting*, Mind 1905 — "the present King of France" falha
a presuposição); quando a presuposição falha, a frase não denota e a resposta certa não é agir — é perguntar.

**O código já é isso.** `nuWorld` é um modelo: `ents` = domínio, `Kind` = sorte de predicado (`nlu.go:437-469`),
construído de `nuRuntimeWorld` = índice de arquivos + perfil do ambiente (`nlu.go:558-597`). `spot` marca
tokens com entidades via Aho-Corasick — pattern-matching exato do modelo (`nlu.go:601`; autômato em
`nlu.go:1484-1570`; Aho & Corasick, CACM 1975, doi:10.1145/360825.360855). `niResolveEntity` faz a
avaliação *aproximada* da denotação — a expressão como digitada não bate literal com a chave do modelo, e a
denotação tolerante a typo é ranqueada por distância (`nameindex.go:1035-1136`). E `brPick`/`brChoose`
implementam exatamente a regra da descrição definida:

- denotação unitária sob disciplina de confiança → age (`methods.go:1142-1153`: exato-único,
  único-candidato-seguro via `brSafeTypo`, líder com margem ≥ `fzSlip·8`);
- denotação vazia → `name_not_found` → pergunta (`recover.go:109`);
- denotação multipla/empatada → `ambiguous` → menu ≤5 — a pergunta *é* a semântica: expressão não-referencial
  não tem valor pra agir sobre (`methods.go:1157-1210`).

**Teorema D (denotação decide quando perguntar).** Seja `⟦e⟧^M` a denotação de uma expressão referencial no
modelo corrente. A regra semanticamente correta é: agir sse `|⟦e⟧|=1` (unicidade de Russell); se `|⟦e⟧|>1`,
a expressão não denota — perguntar; se `|⟦e⟧|=0`, presuposição de existência falha — perguntar ou declarar
ausente. `brPick` é essa regra com `|=1` relaxada para "unitária até a margem calibrada". A teoria não muda
a regra — **explica por que ela é certa e onde ela mente**: quando o modelo está incompleto (arquivo novo
ainda não indexado), `|⟦e⟧|=0` é falso negativo de existência, não inexistência. O tratamento honesto já
existe parcialmente: `niGetFresh` re-indexa em miss (`nameindex.go:107`).

**O que isso compra.** Três coisas concretas: (a) a métrica correta de slot é *acurácia de denotação* (a
entidade ligada é a pretendida), não similaridade de string — P5; (b) a proveniência da denotação deve entrar
no LF (qual entidade o slot denota, ou "não-resolvido") — hoje `Slots` guarda o *texto como digitado*
(`nlu.go:1183`), e o método resolve de novo; (c) a pergunta decisiva é justificativa, não sintoma de fraqueza
— é o ramo `|⟦e⟧|>1` da regra correta.

Fontes: Russell 1905, *On Denoting*, Mind 14:479-493; Montague 1973, *The proper treatment of quantification
in ordinary English*, em Hintikka, Moravcsik & Suppes (eds.), *Approaches to Natural Language*, Reidel;
Dowty, Wall & Peters, *Introduction to Montague Semantics*, Reidel 1981 (doi:10.1007/978-94-009-9065-4);
para a leitura "denotação = conjunto no mundo" em parsing: Liang, Jordan & Klein, *Learning Dependency-Based
Compositional Semantics*, Comp. Linguistics 39(2), 2013 (arXiv:1109.6841); nota λ-DCS (arXiv:1309.4408).

### 3.2 Semântica operacional: `TaskState` é um sistema de transições — e isso paga uma prova

**O que é.** SOS (Plotkin, Aarhus 1981; reimpresso J. Log. Algebr. Program. 60-61:17-139, 2004,
doi:10.1016/j.jlap.2004.05.001) especifica execução como sistema de transições rotuladas
`⟨config⟩ → ⟨config'⟩` definido por regras estruturais. É a semântica mais próxima de implementação — e a
que dá provas por indução estrutural sobre a máquina.

**O código já é isso.** `tkTransitions` (`task.go:40-50`) é literalmente a tabela de regras
`de → {para}` de uma máquina de estados com terminais (`tkTerminal`, `task.go:53-59`); `To`/`must` é a função
de transição com a restrição registrada como invariante: *`succeeded` exige evidência* (`task.go:160-162` —
no vocabulário SOS: a regra `verifying→succeeded` tem a guarda `|Evidence|≥1`). `brExecute` é o small-step:
configuração = `(TaskState, fatos, vars, método corrente, índice do passo, orçamentos)`; cada iteração é uma
transição: disparo de cap (`brain.go:315`), falha → `brRecover` → próximo método ou estado de recuperação
(`brain.go:345-354,428-467`).

**Teorema T (terminação do executor — função de ranking de Floyd).** `brExecute` sempre termina.

*Medida.* `μ = ⟨|métodos após mi|, Σ_cat max(0, Limite_cat − attempts[cat]), Budget.Cmds − UsedCmds,
explore.Limit − streak⟩` na ordem lexicográfica.

*Argumento.* Cada iteração do laço de passos ou: (i) retorna (feito); (ii) avança `si` — consome um passo de
uma lista finita (≤ passos do método); (iii) falha o passo → `brRecover` incrementa `attempts[cat]`
(`brain.go:433`), que é limitado por `rvRule.Limit` (`recover.go:108-120`), e ou pára (`done`), ou marca
`next` — consome um método (a componente 1 cai), ou `retry_smaller` — consome um attempt de `rvTimeout`
(limite 1, `recover.go:113`); (iv) estoura orçamento (`BudgetLeft==0` → `rvBudgetExhausted` → `handoff`,
`recover.go:117`); (v) estoura streak de exploração (`rvExplore`, `recover.go:223-235`). Como cada componente
de μ é limitado inferiormente por 0 e cada iteração decresce μ na ordem lexicográfica, e `μ` é bem-fundada,
o laço pára — em ≤ `Σ métodos·passos + Σ_limites + Budget` transições *(estimativa: ordem 10² no pior caso;
muito abaixo do orçamento de comandos)*. É uma variante bem-fundada à Floyd 1967 (*Assigning Meanings to
Programs*, AMS Proc. Symp. Appl. Math. 19:19-32) — a mesma peça que [autonomy-control-theory.md](autonomy-control-theory.md)
chamou de ranking function. Não é um teorema profundo; é um teorema **cobrável por teste** (P8): toda
tarefa alcança um estado terminal, e todo não-terminal alcança um terminal (DFS sobre `tkTransitions`).

**Onde a teoria paga de verdade.** Dois lugares. Primeiro, a *especificação*: a tabela `tkTransitions` é a
documentação executável da máquina — já é assim que o código pensa. Segundo, a *prova de propriedade*:
propriedades de sistema de transição (alcançabilidade de terminal, ausência de deadlock fora de terminal,
determinismo das transições) são invariantes O(estados) — `tkTransitions` tem 10 vértices; checar é trivial
e vira teste permanente (P8). É o tipo de teorema que se *mantém verdadeiro por teste*, não por revisão.

Fontes: Plotkin 1981/2004 acima; Winskel, *The Formal Semantics of Programming Languages*, MIT Press 1993;
Nielson & Nielson, *Semantics with Applications*, Springer 2007 (doi:10.1007/978-1-84628-692-6).

### 3.3 Semântica axiomática: o método é um triplo de Hoare — com a honestidade da lógica de runtime

**O que é.** Hoare 1969 (CACM 12(10):576-580, doi:10.1145/363235.363259): `{P} C {Q}` — se a pré-condição P
vale antes de C e C termina, a pós-condição Q vale depois. Dijkstra 1975 deu o cálculo `wp(C,Q)` (CACM 18(8),
doi:10.1145/360933.360975); Meyer institucionalizou como *Design by Contract* (IEEE Computer 25(10):40-51,
1992, doi:10.1109/2.161279): pré/pós/ invariante como interface verificada em runtime.

**O código já é isso — na versão executável.** `mhMethod` = `{name, intent, pre, steps, verify, risk}`
(`methods.go:60-72`); `mhApplicable` checa `pre` contra `facts` antes de prometer (`methods.go:141-153`;
fatos montados em `brain.go:225-257`); `brVerify` checa `verify` depois de rodar (`methods.go:1680-1763`).

**Teorema H (soundness trivial — e o que ela NÃO diz).** Se `brVerify(m)` retorna `ok` após uma execução de
`m` a partir de σ com `Pre` valendo, então a proposição representada pelos checkers vale no estado final
dessa execução — *por construção* (o checker é a definição executável do post). Três leituras honestas:

1. É uma prova **existencial**: `∃ execução σ → σ'` testemunhada com evidência. O Hoare pleno é universal
   (`∀ σ⊨P . termina ∧ ⊨Q`) — o jazz não prova isso nem deve tentar: prova universal exigiria especificar o
   comportamento de `bash`/`systemd`, fora de escopo. O que o jazz tem é **proof-carrying execution** —
   a execução carrega o termo de prova (evidência), e o checker valida o termo contra o post (Necula,
   *Proof-Carrying Code*, POPL 1997, doi:10.1145/263699.263712). Curry–Howard mora aqui, não na fantasia de
   verificação total (§3.5).
2. **A força do teorema é a força do post.** `exit_zero` prova "saiu com 0", não "fez a coisa certa";
   `file_exists` prova existência, não conteúdo. Um método com post fraco tem teorema fraco — e a fraqueza
   é *mensurável*: auditoria de força de contrato por efeito declarado (P7).
3. **`needs`/`cmds` são parte do refinamento.** `mhBadCmd` (`methods.go:115-122`) já é uma checagem estática
   — o primeiro pedaço do type-checker que falta (P1): método com comando fora da allowlist é **excluído da
   biblioteca com erro nomeado em `mhErr`** (`methods.go:98-101`). Mas nota honesta: `brRunIntent` descarta o
   erro (`lib, _ := mhLibrary()`, `brain.go:152`) — o método inválido falha em silêncio no runtime e só os
   testes enxergam `mhErr` (`brain_test.go:391`). É o padrão a estender — e a disciplina de erro a fechar.

**Corolário prático.** A invariante "sucesso ⇒ evidência" (`task.go:160-162`) é a regra de consequência do
sistema: nenhuma execução termina "provada" sem termo. Isso já está testado? Não como teorema — `must`
registra violação como decisão "bug:" (`task.go:176-181`) em vez de falhar. P8.

Fontes: Hoare 1969; Dijkstra 1975; Meyer 1992; Floyd 1967 — todos acima. Para o frame problem (o que o
método *não* muda): McCarthy & Hayes 1969 (*Some Philosophical Problems from the Standpoint of AI*, Machine
Intelligence 4) e a solução de Reiter (successor state axioms, *Knowledge in Action*, MIT Press 2001) — no
jazz o frame é o **sistema de efeitos**, §3.6.

### 3.4 Lambda-cálculo e combinadores: o que sobra quando o LF é plano

**O que é.** λ-cálculo (Church 1930s; tipos simples, *A Formulation of the Simple Theory of Types*, JSL 1940,
doi:10.2307/2266170) é a notação universal de função; em semantic parsing é a notação das formas lógicas
(`λx. file(x) ∧ open(x)`). Combinadores (Schönfinkel, Curry) eliminam variáveis.

**No jazz.** O LF é um *frame*: `Intent{name: "open_file", slots: {target: "parser.go"}}` — um predicado
aplicado a argumentos, sem λ-abstração, sem variáveis ligadas, sem composição aninhada. Isso é uma escolha
correta, não uma limitação acidental: task-oriented dialogue (Snips, ATIS, frames de Fillmore) provou que
frames com slots sortados são o LF certo para domínios estreitos — a expressividade extra do λ completo só
compra ambiguidade que não existe no domínio (§3.8). O que do λ-cálculo *está* vivo: `subst` é avaliação por
ambiente — `in`/`out` são bindings, `cx.vars` o ambiente, `cx.subst` a substituição (`methods.go:220-241`).
E é exatamente aí que a semântica cobra: **substituição sem tipo e sem cheque é a fronteira sem checker** —
o bug `fix.diagnose` (§5) é um erro de escopo que um λ-disciplinado pegaria em compile-time.

**Veredito: METÁFORA ÚTIL.** Combinadores para compor caps (`K`, `S`, composição) seriam um *encoding* da
sequenciação que `Steps` já dá — ganho zero, custo de legibilidade total. A parte do λ que fica é a mais
simples: **o ambiente tem escopo e os tipos se checam antes de rodar**.

### 3.5 Curry–Howard: a leitura honesta, e o que ela paga

**O que é.** Proposições como tipos, provas como programas (Howard 1980, *The Formulae-as-Types Notion of
Construction*; exposição canônica: Wadler, *Propositions as Types*, CACM 58(12):75-84, 2015,
doi:10.1145/2699407). A correspondência é entre lógica construtiva e λ-cálculo tipado.

**A leitura no jazz, sem exagero.** A correspondência direta falha nos detalhes — e cada falha instrui:

- proposição ≈ **o intent como goal-type**: `open_file(target:file)` é a proposição "o arquivo target foi
  mostrado ao usuário". Métodos daquele intent são *candidatos a habitantes* do tipo — a biblioteca agrupa
  provas-candidatas por proposição (`mhLib[m.Intent]`, `methods.go:83-103`). OK.
- prova ≈ **a execução + evidência**: `tkEv` registros são os termos; `To(succeeded)` exige termo não-vazio
  (`task.go:160`). Mas a "prova" é de *uma* execução, não da proposição universal — é runtime verification,
  não matemática construtiva. A leitura correta é **proof-carrying execution** (Necula 1997, acima): a
  execução carrega evidência que um checker barato valida contra um post declarado. `brVerify` é o checker;
  `brChecks` nomeia o post (`brain.go:476`).
- tipo do passo ≈ **assinatura do cap**: `run_tests : (target:string?) → brTestRes`; `test_context :
  brTestRes → string`. Os passos têm tipos de saída implícitos no código Go (`brTestRes`, `brCmdRes`,
  `string`, `map`) — **não declarados na DSL**, logo incheçáveis. O `in` é `map[string]string` para todos:
  a DSL é *dynamicamente* tipada com coerção silenciosa (`cx.str`, `cx.subst`, `methods.go:243-248,220-241`).

**Teorema C (o type-checker da biblioteca é correto — e útil).** Defina o tipo de cada var como o produtor:
`out` de um step, namespaces `slot.*`/`fact.*`/`hint`. Regra de escopo: step `s_i` só pode referenciar
`$x` produzido por `s_j`, `j<i`, ou pelos namespaces. *Lema (cheque de dataflow):* se a biblioteca passa no
cheque, nenhum cap recebe `""` por var indefinida — por indução em `i`. *Aplicação hoje:* o cheque pega
`methods/fix.json` — `fix.diagnose` lê `"result": "$result"` mas `run_tests` declara `out: "t"`;
`cx.subst("$result")` → `""` → `brTestContext` recebe `""`, a asserção `.(brTestRes)` falha, o step sai
cedo sem contexto (`methods.go:1409-1412`). **Bug latente real**: o intent `fix` nunca mostra o contexto da
falha — e nenhum teste pega porque o passo "passa" (`ok:true`). Compare com `methods/run_tests.json`, que
faz `out:"result"` e `"$result"` corretamente — evidência de que o bug é um deslize, não design.

É aqui que Curry–Howard **paga de verdade no projeto**: "um método bem-tipado é um método que não referencia
premissas que não existem". E o achado é empírico, não retórico: 1 bug latente por 30 linhas de análise
estática sobre 140 steps (§5). P1 generaliza.

Fontes: Howard 1980; Wadler 2015; Necula 1997; Pierce, *Types and Programming Languages*, MIT Press 2002 —
para a disciplina de tipos aplicada.

### 3.6 Teoria de tipos aplicada: refinamentos, sortes e sistemas de efeitos

**Refinamentos.** Um tipo refinado é um tipo base + predicado (`{x:file | exists x}`) — Freeman & Pfenning,
*Refinement Types for ML*, PLDI 1991 (doi:10.1145/113445.113468). `mhCond` já é isso: `{"fact":"slot.target",
"op":"set"}` refina o ambiente (`methods.go:42-46`); `pre`+`needs` = o refinamento de entrada do método;
`nuSlotDef.Kinds` = as sortes do slot (`nlu.go:84-89` — 14 sortes: file, test, dir, symbol, message, number,
port, text, service, container, ssh_host, ts_peer, toolchain, bin). O que falta é a *verificação* dos
refinamentos: hoje `pre.fact` aceita qualquer string; `slot.X` pode referenciar slot que o intent não declara
— incheçável hoje, checável em O(#conds) com a tabela do dataset (P1). E `nuKindAllowed` já implementa uma
**hierarquia de sortes** (`test <: file`, `toolchain <: bin`, `ts_peer <: ssh_host`, `port <: number`,
`nlu.go:1261-1278`) — subtipagem discreta, já no código.

**Sistemas de efeitos.** Lucassen & Gifford, *Polymorphic Effect Systems*, POPL 1988
(doi:10.1145/73560.73564): anotar o efeito de cada expressão e checar estaticamente que os efeitos
declarados são **aproximação conservadora** dos reais; Talpin & Jouvelot, *The Type and Effect Discipline*,
Inf. Comput. 1994 (doi:10.1006/inco.1994.1046). No jazz: `Capability.Effects` = a anotação (`caps.go:74`,
vocabulário `read|write|exec|git|net|fact:*`); o efeito de um método = ∪ efeitos dos seus caps — **e ninguém
computa**: `Effects` é escrito em 3 lugares (`caps.go:90-104,139`) e **lido em zero**. O campo `Capability.Pre`
(`caps.go:73`) e `Capability.Verifier` (`caps.go:76`, valores como `"candidates_reported"`,
`"file_exists+nonempty"`, `"outparse"`) — idem: **metadados semânticos mortos**.

**Teorema E (soundness do cheque de efeitos — forma de Lucassen–Gifford).** Se `E(cap) ⊇` efeitos reais do
cap para todo cap, então `E(método) = ⋃_steps E(cap)` é aproximação conservadora dos efeitos do método —
checável em O(passos) no load. Corolários checáveis: (a) intent em `brLevel0Intents` (`brain.go:579-588`) ⇒
`E ⊆ {read, fact}` — **garante o contrato "level-0 é só-leitura" que hoje é uma convenção**: um método novo
com cap de `write`/`exec` num intent level-0 seria um furo silencioso do bypass de modelo; (b) `E ∩
{write,exec,git}` ≠ ∅ ⇒ método precisa de `verify` compatível (post sobre efeito) e `risk ≥ med`;
(c) `fact:X` nos efeitos ⇒ o método deve produzir evidência do fato. Custo: uma tabela `brCapEffects`
(~25 entradas, os caps de `brCaps` que hoje não declaram efeitos; `methods.go:303-333`) + ~100 linhas de
cheque no load. P2.

**Frame problem, resolvido por efeitos.** O que um método *não* muda: para a lógica clássica é o problema
difícil (McCarthy & Hayes 1969; solução de Reiter via successor-state axioms, *Knowledge in Action* 2001);
para um DSL com efeitos declarados é trivial — o frame é o complemento de `E`. O'Hearn, *Separation Logic*,
CACM 62(2), 2019 (doi:10.1145/3211968) formaliza a mesma ideia: o *frame rule* diz que efeito local não toca
o resto. No jazz: o checkpoint/`Undo` (`brEnv.Undo`, usado por `brUndo`, `methods.go:832`) já assume que só
`write`/`git` tocam arquivos — a invariante que o sistema de efeitos tornaria explícita e testável.

### 3.7 Teoria de categorias: onde paga e onde não

**Mônadas e Kleisli (Moggi).** Moggi, *Notions of Computation and Monads*, Inf. Comput. 93(1):55-92, 1991
(doi:10.1016/0890-5401(91)90052-4): cada "noção de computação" (estado, exceção, não-determinismo, saída)
é uma mônada; programas com efeito são flechas Kleisli `A → T B`. Leitura: `brStep{val, obs, ok, why, stop}`
(`methods.go:204-210`) é um cômputo `State(vars,facts,out) × Writer(evidence,obs) × Maybe-control(ok,stop)`;
`brExecute` é a amarração `>>=` com short-circuit. Correto — e **metáfora**: nenhuma decisão muda. O dente
real: as leis de mônada viram invariantes testáveis — associatividade (reordenar passos independentes não
muda resultado — hoje garantido só pela disciplina de `out` único por step), identidade esquerda (step com
`out` não-consumido é ruído — checável em P1 como warning). Wadler, *Comprehending Monads*, LFP 1990
(doi:10.1145/91556.91592) como leitura de fundo.

**Functores/algebras/coalgebras, adjunções, categorias de Kleisli em si:** nada paga. O pipeline é linear,
o domínio é finito e pequeno, e nenhum teorema categórico decide margem de `fzSlip`. **Veredito da teoria de
categorias como um todo: METÁFORA ÚTIL** — boa notação para o que já existe, zero mecanismo novo. Se um dia
o DSL ganhar *streaming* ou *interleaving* de métodos, aí Moggi volta a ser decisão; hoje não.

### 3.8 Semântica executável de língua natural: a tradição que o jazz já habita

**A linhagem certa.** Não é Montague-pleno; é a linha *semântica-como-execução* que resolveu domínios
estreitos há 50 anos:

- **Winograd, SHRDLU 1972** (*Understanding Natural Language*, Academic Press): mundo de blocos fechado,
  linguagem ↔ procedimentos — o ancestral do "intenção → método".
- **Woods, LUNAR 1977** (*Lunar Rocks in Natural English*, em *Linguistic Structures Processing*): gramática
  semântica → queries de base de dados sobre amostras da Apollo.
- **Hendrix et al., LADDER 1978** (ACM TODS 3(2):105-147): interface NL para dados navais; portável por
  gramática semântica + léxico de domínio.
- **Warren & Pereira, CHAT-80 1982** (Comp. Ling. 8(3-4):110-122, ACL Anthology J82-3002): gramática lógica
  (extraposition grammar) → Prolog → *otimização de consulta* → resposta; "most questions within the English
  subset answered in well under one second" — **em 1982**. É a existência do ponto "NL→forma→execução <1s"
  que o Dell precisa provar de novo.
- **Fillmore, frame semantics 1982** (*Linguistics in the Morning Calm*, Hanshin): frames com slots —
  a base teórica de intent+slots; **ATIS** (Dahl et al., HLT'94) e o parser Phoenix (Ward & Issar) levaram a
  frames de fala espontânea — a linha que o design doc cita como "estilo Phoenix/Snips"
  (`docs/design/offline-brain.md` §2).
- **Snips 2018** (Coucke et al., arXiv:1805.10190): NLU embedded de intent+slot em **microcontroladores IoT**
  — prova de hardware contemporânea de que a arquitetura roda abaixo do Dell.
- **Semantic parsing moderno:** Zettlemoyer & Collins, UAI 2005 (arXiv:1207.1420) — CCG probabilístico
  induzido de pares sentença→LF (GeoQuery); Kwiatkowski et al., EMNLP 2010 (ACL Anthology D10-1119) — CCG por
  unificação de ordem superior; Liang–Jordan–Klein λ-DCS (arXiv:1109.6841; 1309.4408) — a denotação como
  conjunto no mundo (§3.1); Berant et al., EMNLP 2013 (D13-1160) — parser de pares pergunta→resposta sem LF
  anotado; Artzi & Zettlemoyer, TACL 2013 (doi:10.1162/tacl_a_00209) — instruções→ações com supervisão fraca
  e *execução como grounding*; Wang, Berant & Liang, ACL 2015 (doi:10.3115/v1/P15-1129) — *Overnight*:
  gramática-semente gera "utterances canônicas" e crowdsourcing as parafraseia — **`nlu/intents.json` já é
  exatamente isso**: regras + `examples` canônicos + `negatives`, com `blind_test.json` como o teste de
  paráfrase; Dong & Lapata, ACL 2016 (arXiv:1601.01280) — seq2seq→LF (o que **não** fazer no Dell);
  Lin et al., NL2Bash, LREC 2018 (arXiv:1802.08979) — NL→bash via LF intermediário: o primo mais próximo do
  jazz, com a lição amarga de que entidades (flags, paths) são o gargalo, não o predicado.
- **Zelle & Mooney, AAAI 1996** (*Learning to Parse Database Queries Using ILP*): o benchmark GeoQuery que
  fundou a área — e a medida que o jazz deve copiar: **acurácia de LF exata**, não de rótulo.

**Dois teoremas de arquitetura que a literatura valida:**

1. *Domínio estreito ⇒ gramática manual bate aprendizado.* GeoQuery/ATIS/Overnight: parsers gramaticais
   competitivos com poucos exemplos por categoria; os aprendidos só ganham com dados na casa das centenas
   por construto. O jazz tem 644 exemplos / 50 intents ≈ 13 por intent — regime gramatical, não estatístico.
   A forja cresce a gramática (P9); o parser induzido, não.
2. *Execução é o oráculo mais barato.* Artzi–Zettlemoyer e Berant–Liang: a supervisão pode ser a
   **resposta/denotação**, não o LF — exatamente o que `brVerify` já fornece (a execução verificada é a
   verdade de treinamento). O asset-forge deve minerar `verify ok` como rótulo de LF (P4, P9).

**Veredito do bloco:** gramáticas semânticas + frames + denotação model-teórica = **LOAD-BEARING** (é o
próprio código, e a literatura mede que escala no hardware-alvo); o resto da tradição de parsing =
**METÁFORA** (bonita, mas a forja é o caminho de crescimento, não a indução de CCG em runtime).

### 3.9 Montague pleno e o que não compila

**VIAGEM, com o porquê escrito.** PTQ (Montague 1973) dá: intensionalidade, tempo, quantificadores gerais
(`every`, `a`, `the`, `most`), escopo de quantificador e pronomes — via λ-cálculo de ordem superior + lógica
intensional. No domínio do jazz os quantificadores são **descartados por design**: `all`, `todos`, `todas`,
`some`, `any`, `just` figuram na lista de *fillers* de `nlu/lexicon.json` — "apaga todos os testes" tem o
mesmo LF de "apaga os testes", e a cardinalidade vira problema de denotação (quais arquivos existem no
modelo), nunca de escopo no LF. Sem quantificador não há ambiguidade de escopo; um pedido é uma ação, não
uma sentença com dois quantificadores competindo; modalidade temporal não existe. **O único resíduo de
Montague que importa é o princípio de composicionalidade** — e ele já opera: `brSplitMulti` compõe conjunção
de LFs (`brain.go:76-89`, o status compõe por `max` de severidade, `brain.go:114-128`), e a anafora entre
partes ("abre o parser.go **e explica ele**") é o único fenômeno de discurso real e não-resolvido (P6).

---

## 4. Mapa concreto: teoria → `file:line`

| Teoria | Peça no jazz | Onde | Estado |
|---|---|---|---|
| Modelo M (domínio+sortes) | `nuWorld`/`nuEnt`/`nuTagOf` | `nlu.go:437-469` | vivo |
| denotação de expressão | `spot` (AC) + `niResolveEntity` | `nlu.go:601`; `nameindex.go:1035` | vivo |
| regra da descrição definida | `brPick`/`brChoose` | `methods.go:1132-1210` | vivo |
| LF/frame tipado | `Intent`/`nuIntentDef`/`nuSlotDef` | `nlu.go:48-98` | vivo |
| gramática semântica | `nuRule` (all/any/none sobre conceitos+@tags) | `nlu.go:77-82,838-892` | vivo |
| refinamento de entrada | `mhCond`/`mhApplicable` + `brFacts` | `methods.go:42-46,141-153`; `brain.go:225` | vivo |
| triplo de Hoare | `mhMethod{Pre,Steps,Verify}` + `brVerify` | `methods.go:60-72,1680-1763` | vivo |
| termo de prova | `tkEv`/`AddEvidence` + guarda em `To` | `task.go:79-88,184,160-162` | vivo |
| SOS | `tkTransitions`/`tkCanMove` | `task.go:40-75` | vivo |
| função de ranking | `rvTable`/`rvDecide`/`rvExplore` | `recover.go:108-149,180-236` | vivo |
| STRIPS (operadores) | `Operator`/`Plan` | `backchain.go:20-124` | **morto** (só teste) |
| sistema de efeitos | `Capability.Effects` | `caps.go:74,90-104,139` | **morto** (escrito, nunca lido) |
| refinamento de cap | `Capability.Pre` | `caps.go:73` | **morto** (nunca escrito nem lido) |
| post por cap | `Capability.Verifier` | `caps.go:76,84-102` | **morto** (declarado, nunca consultado) |
| ledger de sinais p/ forja | `Signal`/`sigAppend` | `signals.go:26-78` | **morto** (ninguém appends) |
| calibração de confiança | `ECE`/`PlattFit`/`IsotonicFit` | `calib.go` inteiro | **morto** (só teste) |
| portão held-out | `Gate`/`Split` | `forge_gate.go:92-173` | vivo, sem insumo (§5) |
| ambiente/substituição | `brCtx.vars` + `subst` | `methods.go:190,220-241` | vivo, **sem tipos** |
| conjunção de LFs | `brSplitMulti`/`brRunMulti` | `brain.go:76-128` | vivo |

---

## 5. Achados: a camada semântica está forjada e desligada

Auditoria direta sobre o código (contados: 103 métodos em `methods/*.json` cobrindo 49 dos 50 intents do
dataset, 140 steps; dataset: 50 intents, 14 sortes de slot, 19 slots required, 644 exemplos):

1. **Bug de dataflow latente.** `methods/fix.json` — `fix.diagnose`: step `test_context` lê `"result":
   "$result"`; o produtor `run_tests` declara `out: "t"`. `subst("$result")` devolve `""` (silêncio
   total — `methods.go:237-240`); `brTestContext` recebe "", a asserção de tipo falha, sai cedo
   (`methods.go:1409-1412`). **O intent `fix` nunca exibe contexto de falha** e o passo "passa" — bug
   invisível a testes de comportamento. Um cheque de escopo O(steps) pega isso no load. É a demonstração
   empírica de que a validação estática paga *hoje*.
2. **`subst` é coerção sem aviso.** `cx.str`/`subst` devolvem `""` para var indefinida ou sorte errada
   (`methods.go:220-248`): erros de "tipo" da DSL são silenciosos em runtime. Mesmo padrão no load:
   `mhLibrary` registra `mhErr` mas `brRunIntent` descarta (`lib, _ := mhLibrary()`, `brain.go:152`) — um
   método rejeitado desaparece sem rastro no caminho do usuário. O contrato certo é o do §3.5: tipar por
   produtor (`out` → sorte do cap) e exigir no load, com o erro saindo do log para a decisão da tarefa.
3. **Campos semânticos mortos no registro.** `Capability.Effects`/`Pre`/`Verifier` (`caps.go:73-76`) estão
   declarados e nunca consultados — `brVerify` só conhece a lista por-método (`methods.go:1684-1759`), e
   nada valida que o `Verifier` de um cap condiz com seu nome. Metadados semânticos como documentação
   enferrujada.
4. **`backchain.Plan` é código morto.** O planner STRIPS inteiro (`backchain.go:90-187`, com indexação por
   `addIdx`, delta-stack, branch-and-bound determinístico) só roda em teste. Foi forjado e não ligado:
   nenhum caminho de `brExecute`/`brRecover` o chama.
5. **`sigAppend` é um escritor sem chamadores.** O ledger de sinais (`signals.go`) — o insumo do
   asset-forge — não é alimentado por nada: nem `brNotUnderstood` (`brain.go:214`, o `miss` natural), nem
   `brChoose` quando o usuário corrige o top-1 (`correction`), nem `rvVerifierUnavailable`
   (`verifier_missing`). O portão `Gate` (`forge_gate.go:143`) existe e não tem o que processar.
6. **`calib.go` inteiro é morto** (como já notado em unifying-math: a matemática de calibração existe,
   nenhum call-site) — junta ao padrão: **o projeto já forja assets semânticos antes de ligá-los**.

O padrão unificado: a semântica formal não precisa ser *adicionada* ao jazz; precisa ser **ligada**. Isso
define o formato das propostas: tudo é cheque no load/forja + métrica no eval; nada roda por pedido.

---

## 6. Propostas

Resumo — detalhes em cada item. Métricas sempre em `evaloffline.go` (`eoSummary`, `evaloffline.go:91-107`)
+ `nlu/blind_test.json` + CI. Escala de impacto 1-5; esforço S/M/L; todos rodam em load-time/forja ou
eval — **custo por pedido no Dell ≈ 0**.

| # | Proposta | Métrica principal | Impacto | Esforço | Risco |
|---|---|---|---|---|---|
| P1 | Type-checker da biblioteca de métodos (escopo, caps, sortes, namespaces) | erros latentes pegos no load (hoje: 1) | 5 | M | baixo |
| P2 | Sistema de efeitos ligado (`brCapEffects` + cheques level-0/risk/verify) | % métodos com efeitos checados; 0 violações level-0 | 4 | M | médio |
| P3 | Ligar o ledger de sinais (5 call-sites de `sigAppend`) | sinais/dia registrados → insumo da forja | 4 | S | baixo |
| P4 | Reparo de pré-condição via `Plan` (backchain ligado, só caps seguros) | plan_impossible → succeeded | 3 | M | médio-alto |
| P5 | Denotação explícita no Intent + métrica LF-match no blind test | acurácia de LF (intent+slot+entidade) | 3 | M | baixo |
| P6 | Anafora/elipse entre partes multi-intent | sucesso multi-parte; perguntas economizadas | 3 | S | médio |
| P7 | Auditoria de força de contrato (verify × efeito) | % métodos com post adequado ao efeito | 3 | S | baixo |
| P8 | Invariantes semânticas como testes (terminação, determinismo, escopo) | suíte verde; regressões pegas | 4 | S | nulo |
| P9 | Forja de gramática: sinais → regras/exemplos candidatos → `Gate` | blind acc ↑ com FS=0 através do portão | 4 | L | médio |

**P1 — Type-checker da biblioteca de métodos.**
Mecanismo: `semantics_check.go` — função pura `mhCheckLibrary(lib, dataset, reg) []error` rodada em
`mhLibrary` (warn→`mhErr` como `mhBadCmd` já faz, `methods.go:98-101`) e em `methods_check_test.go`.
Cheques, todos O(steps): (a) `cap` ∈ `brCaps` ∪ `script:*` bem-formado; (b) **escopo**: `$x` em `in`/`verify`
⇒ produzido por `out` anterior ou namespace `slot.|fact.|hint` (pega `fix.diagnose`); (c) `slot.X` ⇒ X ∈
slots do intent no dataset (precisa do `nuGet().data.Intents`); (d) `pre.fact`/`needs` ∈ namespace fechado
(`slot.*`,`hint`,`project.*`,`bin.*`,`env.*`,`ask.*` — mesmo conjunto de `brFacts`, `brain.go:225-256`);
(e) `out` único por método; (f) `risk` ∈ {low,med,high,critical}, `cost_ms ≥ 0`, `check` ∈ nomes de
`brVerify` (o `default` de `methods.go:1758` hoje é armadilha silenciosa: check desconhecido = `unavailable`,
método nunca verifica); (g) `$var` definido mas nunca consumido → warning de ruído (lei de identidade da
mônada, §3.7). Arquivos: novo `semantics_check.go` + teste; ~150 linhas. Custo 32-bit: <1 ms no load,
0 por pedido. Métrica: erros latentes encontrados (hoje ≥1) → 0 e CI travado. Impacto 5 — é o pedaço de
Curry–Howard que literalmente compila.

**P2 — Sistema de efeitos ligado.**
Mecanismo: declarar `brCapEffects` para os ~25 caps de `brCaps` sem efeitos hoje (`methods.go:303-333`);
calcular `E(m) = ⋃ steps` no load; cheques: (a) `intent ∈ brLevel0Intents` ⇒ `E ⊆ {read,fact:*}` — o
contrato "level-0 nunca escreve" vira invariante, não convenção (`brain.go:579-588`); (b) `E ∩
{write,exec,git}` ⇒ `verify` não-vazio com check compatível (tabela check↔efeito da P7) e `risk ≥ med`;
(c) `fact:X` declarado ⇒ evidência do fato produzida (aproximação: step out/AddEvidence). Arquivos:
`caps.go`/`methods.go` + `semantics_check.go`. Custo: O(steps) no load. Métrica: % da biblioteca com
efeitos checados; 0 métodos level-0 com efeito de escrita (invariante de segurança, não convenção).
Risco médio: efeitos declaratórios podem mentir sobre os caps Go — mitigar listando os efeitos no doc de
cada cap e cobrindo os ~10 caps de escrita com testes de invariante.

**P3 — Ligar o ledger de sinais.**
Mecanismo: cinco call-sites, todos O(1) (`sigAppend`, `signals.go:52`): (a) `brNotUnderstood` →
`Kind:"miss"` (`brain.go:214`); (b) `brChoose` com `idx ≠ pick` → `Kind:"correction"`, `Chosen` =
escolha do usuário (`methods.go:1197-1209`); (c) `rvVerifierUnavailable` → `verifier_missing` com o check
faltante em `Extra` (`recover.go:118`); (d) intent entendido sem método → `unknown_intent`
(`brain.go:154-156`); (e) `false_success`/regressão do eval → `cmd_fixed`. Arquivos: `brain.go`,
`methods.go`, `recover.go`; ~40 linhas. Custo: 1 append JSONL por evento raro — ~0. Métrica: sinais/dia por
kind no Dell; o denominador de P9. Risco baixo — append-only, mesma disciplina de `scores.go`.

**P4 — Reparo de pré-condição via backchain.**
Mecanismo: quando `tried==0` por `mhApplicable` falhar (`brain.go:386-396`), extrair os fatos não
satisfeitos dos métodos do intent; traduzir caps que *estabelecem* fatos para `Operator`: `install_dep`/
`install_tool` → `Add:["bin.X"]`; `ask_slot` → `Add:["slot.X"]`; `choose` → `Add:["slot.X"]` (via resposta);
`env_status` → `Add:["fact.env"]`. Rodar `Plan(facts_set, missing, ops, 3)`; se plano existe e só usa caps
low/med → executá-lo como pré-fase (com confirmação para caps de escrita/instalação — nunca auto). Arquivos:
`brain.go` (ponto de chamada), `caps.go` (tabela cap→Add), `backchain.go` (já pronto). Custo: plano ≤3 ops
sobre ≤10 operadores — µs. Métrica: % de `plan_impossible` resgatados para `succeeded`; perguntas
necessárias ↓. Risco médio-alto: planos que mutam ambiente precisam do gate de permissão normal — restringir
a caps DryRun ou confirmados; medir primeiro: pode render pouco (poucos fatos são estabelecíveis por caps),
e aí o veredito honesto é **apagar backchain.go** — ciência medida, não esperança.

**P5 — Denotação explícita + métrica de LF.**
Mecanismo: (a) `Intent` ganha `SlotsResolved map[string]string` — a entidade ligada após resolução (ou
`"unresolved"`); `brResolve`/`brChoose` escrevem a denotação escolhida de volta no LF (`methods.go:1043,
1208`); (b) o eval/blind test passa a anotar o LF esperado (intent+slot+entidade) — hoje `expect.intent` +
`contains`, não mede *denotação correta*; (c) métrica nova: LF exact-match (o denominador honesto da
acurácia semântica — o "GeoQuery accuracy" do jazz). Arquivos: `nlu.go` (`Intent`), `methods.go`
(`brResolve`,`brChoose`), `evaloffline.go`, `nlu/blind_test.json`. Custo: ~0. Métrica: acurácia de LF vs
acurácia de intent — mede quanto do erro remanescente é denotação, não classificação. Impacto 3, esforço M,
risco baixo.

**P6 — Anafora/elipse entre partes multi-intent.**
Mecanismo: em `brRunMulti` (`brain.go:93-112`), quando a parte k tem `Missing` não-vazio e a parte k−1
preencheu um slot da mesma sorte (ou produziu entidade via `choose`/`resolve`), herdar:
`"abre o parser.go e explica ele"` → `explain{target: parser.go}`. Implementar como
`brInheritSlots(ins[i-1], ins[i], dataset)`: só slots required faltantes, só mesma sorte, registrando
`Evidence` "herdado de '…'". Arquivos: `brain.go`, `nlu.go`. Custo: O(#partes·#slots). Métrica: sucesso em
suite multi-parte com elipse (adicionar ~10 casos a `evals/offline/tasks.json`); perguntas desnecessárias ↓.
Risco médio: antecedente errado → pede confirmação (a regra de denotação já protege: se herdado resolve a
denotação unitária, é o referente; se não, pergunta — mesmo custo de hoje).

**P7 — Auditoria de força de contrato.**
Mecanismo: tabela check→efeito-que-prova (`file_exists`/`file_gone`→write; `exit_zero`/`cmd_ran`→exec;
`tests_passed`→exec+domínio; `script_exit_zero`→exec; `read_ok`/`status_obtained`→read; `profile_present`→
fact). Relatório: método com `E∩{write}` mas verify ∉ {file_exists, file_gone} → "post fraco para o efeito";
método com `E∩{exec}` e verify só `status_obtained` → idem. Saída: `jazz methods audit` (ou seção no
`semantics_check`) listando cobertura de contrato da biblioteca — o mapa de "quão forte é o teorema" por
método. Arquivos: `semantics_check.go`. Custo: O(steps), load-time. Métrica: % métodos com post adequado ao
efeito (meta: 100% dos com `write`). Impacto 3, esforço S, risco baixo — é relatório, não gate (no início).

**P8 — Invariantes semânticas como testes.**
Mecanismo: `semantics_test.go` cobrindo como propriedades: (a) *alcançabilidade terminal* — DFS sobre
`tkTransitions`: todo não-terminal alcança um terminal (10 vértices); (b) *terminação efetiva* — fuzzer de
falhas de step (fake caps) sobre `brExecute` com bound de iterações: sempre termina < bound; (c)
*determinismo de Parse* — `Parse(t,w) ≡ Parse(t,w)` sobre o corpus e o blind test (puro, deve ser); (d)
*escopo* — P1 aplicado a `methods/*.json` embarcado; (e) *guarda de evidência* — nenhum caminho de teste
alcança `succeeded` com `len(Evidence)==0` (a invariante de `task.go:160` testada, não assumida); (f)
`subst` nunca panica (fuzz de `$vars`). Arquivos: novo teste + fakes. Custo: segundos de CI. Métrica: suíte
verde; a prova de terminação da §3.2 vira propriedade testada. Impacto 4 — transforma os teoremas em
regressão permanente.

**P9 — Forja de gramática (o LF como asset).**
Mecanismo: no tempo de forja (amd64, não Dell): minerar `signals.jsonl` `miss`/`correction`; para misses com
intent alto (`Alt` ou NB p>τ), gerar candidatos — novas `rules`/`examples`/`concepts` para
`nlu/*.json`; avaliar com `Gate` (`forge_gate.go:143`) sobre o dataset+blind como `EvalFn` (parse accuracy
held-in/held-out, `FalseSuccess` = entendimento errado executado). Promovido = asset novo; é "Online ensina
o offline" do design doc §7, agora para a gramática. Arquivos: `signals.go` consumidor, `forge_gate.go`,
novo `nlu_forge.go` (dev). Custo: só forja. Métrica: blind accuracy ↑ através do portão com
`false_success=0`; sinais reprocessados ↓. Impacto 4, esforço L, risco médio — é o único caminho correto de
crescimento do LF: **forjado, portado, nunca induzido em runtime**.

---

## 7. O que não fazer

- **Montague pleno / quantificadores / escopo.** O léxico não tem quantificador; criar máquina de escopo
  para zero instâncias é enfeite (§3.9).
- **CCG ou λ-DCS induzidos em runtime.** Precisam de centenas de pares sentença→LF ou pergunta→resposta
  anotados; temos 644 exemplos sem LF. A forja (P9) é o caminho — com portão, não com parser estatístico.
- **Seq2seq/Transformer para o LF (Dong–Lapata).** Derrota o propósito: 50 intents × gramática manual já
  cobre; modelo neural no Dell é a dependência que a filosofia proíbe.
- **Tipos dependentes / provadores (Coq, Lean, Agda) na DSL.** A lógica das pré-condições é conjunção
  *ground* sobre strings — `mhHolds` avalia em O(1); SAT/SMT/provador compra rigor que já existe de graça.
- **Codificação SKI/λ dos caps, mônadas explícitas em Go.** O `>>=` já é `for st := range m.Steps` —
  reescrever `brExecute` como "interpretador monádico" é traduzir, não melhorar.
- **Verificação universal de métodos (Hoare ∀).** Exigiria modelar `bash`, `systemd`, permissões — fora do
  orçamento do projeto inteiro. Proof-carrying execution é o contrato real (§3.3).
- **Herança múltipla de sortes / ontologia de entidades.** 14 sortes com a hierarquia mínima de
  `nuKindAllowed` bastam; uma taxonomia OWL de "arquivo é documento é artefato" não decide nenhuma margem.
- **Sair do frame para λ-termos.** O LF plano é uma *escolha certa* (task-oriented dialogue): aninhar
  estrutura sem quantificador só multiplica o espaço de parses errados.
- **Métrica só de intent.** Continuar medindo "acertou o nome do intent" sem medir a denotação do slot é
  medir metade da semântica — P5 corrige; não adiar para sempre.

---

## Fontes

**Semântica de linguagens de programação**
- Scott & Strachey, *Toward a Mathematical Semantics for Computer Languages*, PRG-6, Oxford 1971.
- Strachey, *Fundamental Concepts in Programming Languages*, HOSC 13(1-2), 2000 (doi:10.1023/A:1010000313106).
- Scott, *Data Types as Lattices*, SIAM J. Comput. 5(3):522-587, 1976 (doi:10.1137/0205037).
- Plotkin, *A Structural Approach to Operational Semantics*, Aarhus DAIMI FN-19 1981; J. Log. Algebr.
  Program. 60-61:17-139, 2004 (doi:10.1016/j.jlap.2004.05.001).
- Winskel, *The Formal Semantics of Programming Languages*, MIT Press 1993.
- Nielson & Nielson, *Semantics with Applications: An Appetizer*, Springer 2007
  (doi:10.1007/978-1-84628-692-6).

**Semântica axiomática e verificação**
- Floyd, *Assigning Meanings to Programs*, Proc. Symp. Appl. Math. 19:19-32, AMS 1967.
- Hoare, *An Axiomatic Basis for Computer Programming*, CACM 12(10):576-580, 1969
  (doi:10.1145/363235.363259).
- Dijkstra, *Guarded Commands, Nondeterminacy and Formal Derivation of Programs*, CACM 18(8):453-457, 1975
  (doi:10.1145/360933.360975).
- Meyer, *Applying "Design by Contract"*, IEEE Computer 25(10):40-51, 1992 (doi:10.1109/2.161279).
- Necula, *Proof-Carrying Code*, POPL 1997 (doi:10.1145/263699.263712).
- O'Hearn, *Separation Logic*, CACM 62(2):86-95, 2019 (doi:10.1145/3211968).
- McCarthy & Hayes, *Some Philosophical Problems from the Standpoint of AI*, Machine Intelligence 4, 1969.
- Reiter, *Knowledge in Action*, MIT Press 2001.

**Lambda-cálculo, tipos, Curry–Howard, efeitos**
- Church, *A Formulation of the Simple Theory of Types*, JSL 5(2):56-68, 1940 (doi:10.2307/2266170).
- Howard, *The Formulae-as-Types Notion of Construction*, em *To H.B. Curry: Essays on Combinatory Logic*,
  Academic Press 1980.
- Wadler, *Propositions as Types*, CACM 58(12):75-84, 2015 (doi:10.1145/2699407).
- Pierce, *Types and Programming Languages*, MIT Press 2002.
- Freeman & Pfenning, *Refinement Types for ML*, PLDI 1991 (doi:10.1145/113445.113468).
- Lucassen & Gifford, *Polymorphic Effect Systems*, POPL 1988 (doi:10.1145/73560.73564).
- Talpin & Jouvelot, *The Type and Effect Discipline*, Inf. Comput. 111(2):245-296, 1994
  (doi:10.1006/inco.1994.1046).

**Teoria de categorias / mônadas**
- Moggi, *Notions of Computation and Monads*, Inf. Comput. 93(1):55-92, 1991
  (doi:10.1016/0890-5401(91)90052-4).
- Wadler, *Comprehending Monads*, LFP 1990 (doi:10.1145/91556.91592).

**Semântica de língua natural**
- Russell, *On Denoting*, Mind 14:479-493, 1905.
- Montague, *The Proper Treatment of Quantification in Ordinary English*, em Hintikka, Moravcsik & Suppes
  (eds.), *Approaches to Natural Language*, Reidel 1973 (reimpresso em *Formal Philosophy*, Yale 1974).
- Dowty, Wall & Peters, *Introduction to Montague Semantics*, Reidel 1981 (doi:10.1007/978-94-009-9065-4).
- Fillmore, *Frame Semantics*, em *Linguistics in the Morning Calm*, Hanshin 1982.
- Winograd, *Understanding Natural Language*, Academic Press 1972.
- Woods, *Lunar Rocks in Natural English: Explorations in Natural Language Question Answering*, em
  *Linguistic Structures Processing*, North-Holland 1977.
- Hendrix, Sacerdoti, Sagalowicz & Slocum, *Developing a Natural Language Interface to Complex Data*, ACM
  TODS 3(2):105-147, 1978.
- Warren & Pereira, *An Efficient Easily Adaptable System for Interpreting Natural Language Queries*
  (CHAT-80), Comp. Linguistics 8(3-4):110-122, 1982 (ACL Anthology J82-3002).

**Semantic parsing**
- Zelle & Mooney, *Learning to Parse Database Queries Using Inductive Logic Programming*, AAAI 1996.
- Zettlemoyer & Collins, *Learning to Map Sentences to Logical Form*, UAI 2005 (arXiv:1207.1420).
- Kwiatkowski, Zettlemoyer, Goldwater & Steedman, *Inducing Probabilistic CCG Grammars from Logical Form
  with Higher-Order Unification*, EMNLP 2010 (ACL Anthology D10-1119).
- Steedman, *The Syntactic Process*, MIT Press 2000.
- Liang, Jordan & Klein, *Learning Dependency-Based Compositional Semantics*, Comp. Linguistics 39(2),
  2013 (arXiv:1109.6841); Liang, *Lambda Dependency-Based Compositional Semantics* (arXiv:1309.4408).
- Berant, Chou, Frostig & Liang, *Semantic Parsing on Freebase from Question-Answer Pairs*, EMNLP 2013
  (ACL Anthology D13-1160).
- Artzi & Zettlemoyer, *Weakly Supervised Learning of Semantic Parsers for Mapping Instructions to
  Actions*, TACL 1:49-62, 2013 (doi:10.1162/tacl_a_00209).
- Wang, Berant & Liang, *Building a Semantic Parser Overnight*, ACL 2015 (doi:10.3115/v1/P15-1129).
- Dong & Lapata, *Language to Logical Form with Neural Attention*, ACL 2016 (arXiv:1601.01280;
  doi:10.18653/v1/P16-1004).
- Lin, Wang, Zettlemoyer & Ernst, *NL2Bash: A Corpus and Semantic Parser for Natural Language Interface to
  the Linux Operating System*, LREC 2018 (arXiv:1802.08979).
- Coucke et al., *Snips Voice Platform: an Embedded Spoken Language Understanding System*, 2018
  (arXiv:1805.10190).
- Aho & Corasick, *Efficient String Matching: An Aid to Bibliographic Search*, CACM 18(6), 1975
  (doi:10.1145/360825.360855).

**Planejamento clássico**
- Fikes & Nilsson, *STRIPS: A New Approach to the Application of Theorem Proving to Problem Solving*,
  AIJ 2(3-4):189-208, 1971 (doi:10.1016/0004-3702(71)90010-5).
- Erol, Hendler & Nau, *HTN Planning: Complexity and Expressivity*, AAAI 1994.
- Newell & Simon, *Human Problem Solving*, Prentice-Hall 1972 (means-ends).
