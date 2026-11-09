# Auditoria do modo offline — relatório de pesquisa (subagente, 09/11/2026)

**Escopo lido de verdade:** nlu.go (15.260 linhas), methods.go (4.049), brain.go, task.go, recover.go, nameindex.go, plancache.go, nlu_calib.go, signals.go+signals_emit.go, forge.go, forge_gate.go, nlu_overlay.go, distill.go, alias.go, alexandria.go, evaloffline.go, evals/offline/tasks.json (112 tarefas), methods/*.json (103), intents.go, caps.go, nlu_slotguard_test.go, docs/research/jev-build-time-tickets.md, CHANGELOG. Pesquisa externa: Snips NLU, Rhasspy, Almond/Genie, Rasa/fastText.

**Ressalva:** números citados são reportados pelo repo (CHANGELOG/tickets), não re-medidos nesta auditoria.

## 1. Mapa do que existe hoje

| Componente | Arquivo | O que é |
|---|---|---|
| Parser NLU | nlu.go (~15,3k linhas) | Monólito: tokenize→normalize→spot(Aho-Corasick)→rules→fill(slots)→NB fallback. ~100 intents |
| Gramática | nlu/intents.json, lexicon.json | Regras, exemplos, negativos, slots tipados por intent |
| Classificador | nlu.go:9311-9474 | NB multinomial sobre char 3-5-grams hashados em 2^14 baldes, tabela int16, prior congelado nos exemplos embarcados |
| Calibração | nlu_calib.go+calib.json | Isotônica, opt-in (JAZZ_NLU_CALIB) — asset treinado em exemplos da gramática, infla leituras fracas |
| Segundo parser | intents.go:223-330 | fpIntent: regex (?i)^(padrão)$ por intent (built-in < user < .jazz/ confiável). Coexiste com nuModel no brain |
| Métodos | methods.go + methods/*.json (103) | name/intent/pre/steps/verify/risk/needs sobre capacidades registradas; sem verificador → unverified |
| Capacidades | caps.go | Registry com risk (low→critical), needs (OS/bins/net), verifier, undo, timeout |
| Cérebro | brain.go | brRun/brRunIntent: multi-intent, brActMin por banda de risco, brPlanChain, brSlotIgnored, handoff |
| Task FSM | task.go | Estados explícitos, tkTerminal, AddEvidence com IDs, persistência JSONL + event stream |
| Recuperação | recover.go | Categorias observáveis (name-not-found, ambiguidade, missing cmd…); orçamento de exploração = 3 |
| Planejador | backchain.go | STRIPS determinístico, ≤4 ops, em produção via brPlanChain |
| Índices | nameindex.go, gitindex.go | SoA+blob de strings, caps 20k/3k; leitura direta de .git/index |
| Planos online | plancache.go | BM25 puro-Go, só planos verificados, alimenta o path do modelo — offline não consome |
| Aprendizado | signals*.go→forge*.go→nlu_overlay.go; distill.go; alias.go; alexandria.go | Ledger → correções src=tty ≥2 sessões → gate held-out → overlay de exemplos; destilação replay-safe; Alexandria = biblioteca causal com refutações |
| Eval | evaloffline.go + tasks.json (112) | Fixtures sintéticos, mesmo path de jazz -p --offline; mede succeeded/falso-sucesso/no-model/perguntas/latência/RSS |

## 2. O que é genuinamente forte

1. **A invariante é estrutural.** succeeded exige verify+evidência cobrindo todos os operandos; T8 fechou a classe residual ("operando citado que não alimenta slot" → Dropped → ≤unverified, nuDroppedOps nlu.go:2092). Corpus redo1051: zero falso-sucesso.
2. **Confiança honesta em camadas** (nlu.go:2143-2150): regra pura = 0.9; regra+NB = 0.97; NB sozinho = p×0.75; veto = ×0.5 — o mesmo pipeline do Snips (Deterministic→Probabilistic) validado em RPi3, com fallback NB determinístico sobre asset embarcado em vez de CRF.
3. **Guardas tipadas com teste permanente** (nlu_slotguard_test.go): cada fix de cluster vira guarda.
4. **Aprendizado com higiene:** prior do NB congelado nos exemplos embarcados (a causa real de "forgetting" é prior drift); forge exige src=tty, ≥2 sessões, anti-trigger, held-out seed fixa; overlay só acrescenta exemplos com SHA da gramática; intents aprendidas cedem a qualquer candidato determinístico; destilação só de planos verificados.
5. **Abstenção honesta:** nuTaskEvidence bloqueia chitchat; "pedido criativo sem modelo" explica o que falta; ambiguidade → menu, nunca chute.
6. **Engenharia para hardware fraco:** log-odds inteiros, AC byte-level sem mapas por nó, índice pointer-free, .git/index direto, fpSafeArg como byte loop. linux/386 é alvo declarado.

## 3. Fraquezas honestas

1. **nlu.go é um monólito de 15.260 linhas** — lexer, stemmer, spotter, motor de regras, extratores de slot hand-coded por domínio, NB e AC no mesmo arquivo. Cada fix aumenta o acoplamento.
2. **Duas representações de intenção coexistem**: nuModel (gramática+NB) e fpIntent (regex por intent), ambas alcançam o brain — dois formatos, duas semânticas de slot, dois lugares pra um pedido escapar.
3. **Under-refusal residual medido e grande**: ~450 dos 1051 doable abaixo do gate (T5, frente ativa).
4. **Confiança é amálgama de constantes** (0.9/0.97/0.75/0.5/0.7) escolhidas por raciocínio; calib que corrigiria está off porque o asset infla leituras fracas — circular sem labels reais.
5. **Cold-start não medido**: nbOnce treina o NB no primeiro parse (~100 classes × 16k baldes) — num Pentium M pode custar segundos; nenhum número existe (T7 aberto).
6. **methods.go (4.049 linhas)** mistura schema loader, executor, FS, Git, containers, verificadores e checagens de segurança — segundo monólito.
7. **Validação de assets é em runtime**: m.loadErr só aparece quando o usuário pergunta.
8. **Docs internas envelhecem**: study-synthesis dizia "backchain sem chamador" — falso, brPlanChain chama Plan desde brain.go:~480. O que mais está assim?

## 4. Números reportados pelo repo (não re-medidos)

- jazz eval offline 0.13.x (máquina de dev): 101/101 · falso sucesso 0 · 99/101 sem modelo · perguntas 6/6 necessárias.
- Corpus redo1051: FALSE_SUCCESS 41→0; residual ~450 doable (T5).
- Cega de 100 frases (nlu/blind_test.json): metodologia correta; número atual não registrado.
- Não há benchmark de parse commitado nem results/ novos; T7 (medir no D610 real) aberto.

## 5. Top 10 otimizações (impacto × esforço) — excluindo o que já está na fila

| # | Otimização | Impacto | Esforço | Pré-req | Como medir |
|---|---|---|---|---|---|
| 1 | **jazz parse "<texto>" / jazz why** — CLI que imprime a leitura sem executar: intent, regra#+score, NB p, vetos, Dropped, Missing, banda e limiar | Alto (auditabilidade + toda depuração NLU/T5) | Baixo — m.Parse já devolve tudo em Evidence | nenhum | pedidos do ledger reproduzem a leitura linha-a-linha |
| 2 | **Gate CI de validação estática de assets**: methods/*.json → intent/slots/verifiers existem; calib SHA = SHA da gramática; overlay schema | Alto (mata "erro só em runtime") | Baixo-Médio | nenhum | teste falha ao corromper qualquer JSON; zero loadErr no eval |
| 3 | **Benchmark commitado do parser**: cold vs warm (inclui nbOnce), com índice, cap de RSS; baseline com gate ±20% | Alto — única forma de alegar "roda num Pentium M" sem o Dell (T7) | Médio | #1 ajuda a perfilar | p50/p95 + RSS em CI; regressão vira FAIL |
| 4 | **Convergência nuModel × fpIntent**: mínimo — fronteira documentada + teste de não-contradição; ideal — fpIntent vira fonte de regras do nuModel | Alto (elimina classe de divergência) | Médio | #1 | corpus cego pelos dois caminhos: zero divergência |
| 5 | **Modularizar nlu.go** por estágio (tokenize, spot/AC, rules, fill/slots, NB, gates) sem mudar comportamento | Alto p/ manutenção | Médio-Alto mecânico | suíte atual verde como oráculo | diff zero em blind_test+eval |
| 6 | **Matriz estática slot↔método**: slots extraídos sem consumidor; needs sem alimentador; operandos objetivos sem caminho pro Dropped — generaliza o T8 | Alto (anti-regressão preventiva) | Médio | #2 | relatório determinístico; cada entrada vira bug ou exceção |
| 7 | **Gate de colisão da gramática embarcada**: forge_gate sobre o asset base; colisões conhecidas publicadas | Médio-Alto | Médio | nenhum | lista commitada; regra nova que colide → FAIL |
| 8 | **Corpus metamórfico determinístico**: geradores puros (seed) de variações de superfície sobre as 112 tasks | Médio | Baixo-Médio | léxico como sinônimos | eval N× variações; falso-sucesso segue 0 |
| 9 | **Cold-start como asset**: NB+AC serializados em build-time | Médio (só importa no hw-alvo) | Médio | **#3 primeiro** — senão é otimização cega | tempo até 1ª resposta |
| 10 | **Recusa explicável**: quando falha por pouco, diz o quê — "faltou slot dir", "veto: X não é Y". Diagnóstico, não cobertura | Médio | Baixo-Médio | #1 | % de recusas com causa nomeada |

## 6. O que NÃO fazer

1. **Nada de modelo em runtime — de nenhum tamanho** (nem DIET, nem fastText, nem GGUF "só pra classificar"). ML legítimo: só em build-time, destilando para asset (padrão Jev).
2. **Não adotar CRF do Snips** como segundo estágio — o jazz já tem a arquitetura certa; CRF traz dependência/float/não-determinismo por migalhas de recall.
3. **Lição Genie ao contrário**: 95% em paráfrase sintética vs <20% em dados reais — não gerar exemplos sintéticos para "treinar" e chamar de mundo real.
4. **Não ligar JAZZ_NLU_CALIB por padrão** sem labels reais — opt-in é a decisão correta até o refit (P3).
5. **Não afrouxar guarda pra subir cobertura** — under-refusal se resolve por extração, nunca por limiar.
6. **Não quebrar a invariante** — T1 aberto: replan fraco herda verde sem carregar sub-objetivos dropados.
7. **Não criar terceira gramática** nem expandir fpIntent sem resolver a convergência (item 4).
8. **Não indexar o mundo** — caps 20k/3k do nameindex são corretos pro alvo; sem bound é OOM no Latitude.
9. **Não confiar em doc velha** — "backchain sem chamador" já estava errado; o código manda.

**Veredito:** a arquitetura central — gramática determinística + fallback estatístico pequeno + verify/evidência como selo — é a mesma forma que Snips e Rhasspy validaram para embedded, com disciplina de honestidade que vai além de ambos. As fraquezas reais não são de conceito: são monolito (nlu.go), duplicação (fpIntent), validação tardia de assets e ausência de números medidos de latência/memória — tornando os itens 1–3 o próximo passo mais barato e valioso.
