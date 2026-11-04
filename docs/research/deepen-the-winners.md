# deepen-the-winners — das três descobertas validadas ao experimento

Commit de referência: `a5ffe0f`. `file:line` refere-se a essa revisão.
Máquina de prova: Dell D610 — Pentium M 1.7 GHz, 1 núcleo, 2 MB L2, 2 GB RAM,
linux/386, Go 1.27.2. Forja pode rodar em amd64; **medida de runtime só vale no
Dell**.

Este documento **não reabre** o que já está decidido. Carrega como premissa
(não como tese a provar):

- Moeda de decisão = posterior **calibrado**, não entropia
  (`unifying-math-offline.md` §6/§7: LOAD-BEARING; entropia=compressão:
  METÁFORA ÚTIL).
- Objetivo da biblioteca: `J(L) = Ê[L] + λ·|L|` — MDL empírico
  (`unifying-math-offline.md` §3, LOAD-BEARING).
- Verificação objetiva (`brVerify`) é a base de confiança; falso sucesso é o
  inimigo nº 1 (`verification-oracles-deep.md`, `offline-on-device-intelligence.md`).
- Corpus de trajetórias = `obs.go`/`task.go` eventos + `plancache.go` planos
  verificados (`plan-reuse-deep.md`).
- Pipeline de forja = sinais → candidato → gate held-out → overlay
  (`program-synthesis-library-learning.md` C1–C6, `skill-induction-deep.md` §7).
- Constantes atuais são palpites a refutar, não verdades
  (`unifying-math-offline.md` §7.5): `nuMinConf=0.6` (`nlu.go:45`),
  `brLevel0Min=0.85` (`brain.go:596`), margem `fzSlip*8` (`methods.go:1151`).

O que falta — e é o que este documento fecha: (a) a **contabilidade** da forja
em tokens por classe, (b) o **algoritmo final** agir/perguntar/menu ligando
`calib.go` a `mhMethod.Risk`, (c) o sentido **operacional** de "asset mínimo".

**Nota honesta sobre o estado do código.** As fundações estão commitadas mas
estão **mortas**: `Gate` (`forge_gate.go:143`), `sigAppend`/`sigRead*`
(`signals.go:52–120`), todo `calib.go` e `backchain.go` só são chamados em
`*_test.go`. `mhMethod.Risk` é parseado (`methods.go:70`) e **nunca lido**.
Ou seja: os três vencedores já têm teorema e têm maquinário — falta a fiação.
Este doc especifica a fiação e o experimento que prova que ela paga.

---

## 0. Placar

| # | Descoberta | Veredito | O que falta para ser implementável |
|---|---|---|---|
| 1 | Forja amortizada `E_forge + N·E_offline ≪ N·E_llm` | **LOAD-BEARING** | medir `E_llm`/`E_offline`/`E_forge` por classe em tokens; ligar o ledger; `N*` medido |
| 2 | Decisão = posterior calibrado | **LOAD-BEARING** | os 4 mapas por canal + `mhMethod.Risk` como custo + menu como conjunto conformal |
| 3 | Compressão = asset | **LOAD-BEARING** | "mínimo" = 1-minimal via ddmin; ponte `eoRun → Gate`; vida do asset (uso, falha, quarentena) |

Nenhum é METÁFORA nem VIAGEM: cada um deriva um número que o Dell mede em um
comando `jazz eval …`.

---

## 1. FORJA AMORTIZADA — do slogan à contabilidade por classe

### 1.1 O modelo

Três energias, todas em **tokens** (a moeda que o usuário paga; segundos e
dólares são conversões externas que mudam por provedor/modelo):

- `E_llm(c)`: tokens totais (In+Out) que o caminho do modelo gasta por pedido
  da classe de tarefa `c`, sem o asset.
- `E_offline(c)`: tokens por pedido resolvido pelo asset. Por construção ≈ 0
  (o cérebro não fala com modelo); o custo real residual é ms de CPU + perguntas
  ao usuário, contados à parte.
- `E_forge(a)`: tokens gastos **uma vez** para produzir o asset `a` (chamadas
  de modelo na forja + avaliação) + segundos de revisão humana (ledger
  separado, não somável).

Asset `a` cobrindo fração `ρ_c` dos pedidos da classe `c`, usado `N` vezes:

```
custo_sem_forja = N · E_llm(c)
custo_com_forja = E_forge(a) + N·ρ_c·E_offline(c) + N·(1−ρ_c)·E_llm(c)
economia(N)     = N·ρ_c·(E_llm(c) − E_offline(c)) − E_forge(a)
```

### 1.2 Teorema do break-even

**N\* (por classe).** A forja compensa iff `economia(N) > 0` ⇔

```
N > N*(a,c) = E_forge(a) / [ ρ_c · (E_llm(c) − E_offline(c)) ]        (1)
```

Com `E_offline ≈ 0` e `ρ_c ≈ 1`: `N* ≈ E_forge/E_llm`. Três casos de borda,
todos com decisão correta embutida na fórmula:

- `E_llm ≤ E_offline`: denominador ≤ 0, `N*` indefinido → **nunca forjar**
  (asset que custa tanto quanto o modelo não é asset).
- `ρ_c ≈ 0` (asset correto mas nunca disparado): `N* → ∞` → não forjar. A
  demanda importa mais que o custo — medida pelo ledger, não por intuição.
- Asset que erra com probabilidade `q`: cada uso errado custa `C_rec`
  (refazer, possivelmente via LLM). Economia esperada por uso vira
  `(1−q)·ρ·ΔE − q·C_rec`. O gate `FalseSuccess == 0` (`forge_gate.go:155`)
  existe exatamente para forçar `q ≈ 0` no suporte medido — **o gate é parte
  da economia**, não burocracia.

**Por que break-even e não regra arbitrária** — este é o problema clássico
*rent-or-buy* (ski rental). Karlin, Manasse, Rudolph & Sleator (1988) provaram:
a regra "alugue até o aluguel acumulado atingir o preço de compra `B`, então
compre" é `2 − 1/B`-competitiva contra demanda **adversarial**, ótima entre
determinísticas; randomizada atinge `e/(e−1) ≈ 1,58` (Karlin, Manasse,
McGeoch & Owicki 1994). Ou seja: mesmo sem saber `N` futuro, forjar quando o
gasto contrafactual acumulado da classe atinge `E_forge` tem razão competitiva
≤ 2 — o pior caso é pagar o dobro do ótimo, nunca mais.

**Refinamento com dados.** Jazz não está no regime adversarial: o ledger mede a
demanda. Com `f_c` = pedidos/semana da classe `c` (de `signals.jsonl` +
sessões), a regra de Bayes domina o bound competitivo:

```
forjar(a)  iff  Ê[N|T]·ρ_c·ΔE_c > E_forge(a)      (horizonte T)
           iff  f_c·T·ρ_c·ΔE_c > E_forge(a)      (demanda estacionária)
```

Mantenha o bound `2−1/B` como sanity check do primeiro asset de cada classe
(quando `f_c` ainda não foi medido); depois é NPV puro.

### 1.3 As três energias no jazz, medido vs. estimado

**`E_llm` — um número medido, o resto estimado.** O único ponto medido é a
classe `atalho`: "abre o README.md" custou **32,3k tokens / 2 requests /
14,3–21,5 s** no baseline 0.8.1 (`results/dell-0.9-progress.md`). Isso é um
pedido, um dia, um provedor — não uma constante universal. O protocolo (§1.5)
mede `E_llm(c)` por classe a partir do corpus de sessões: `imLoadSession`
já extrai `Usage{InputTokens, OutputTokens}` por mensagem
(`improve_load.go:68–149`, `types.go:43–57`, `session.go:299–310`); falta
agregar por classe. Para tarefas novas, contrafactual determinístico via
`estTokens` (`agent.go:275–291`, caracteres×10/35 — estimativa grossa,
rotular como tal).

**`E_offline` — medido.** Eval: p50 ≈ 14 ms, RSS ~15–20 MB
(`results/dell-0.9-progress.md`); tokens = 0. O custo residual não-token é
`Questions`: um menu custa uma interação do usuário — contado por tarefa no
eval (`eoGot.Questions`/`eoRow.QNeeded`, `evaloffline.go:71, 88`) e
precificado no §2.

**`E_forge` — hoje não medido; o ledger tem que começar a medir.** Estimativa
por classe de asset (a confirmar no primeiro forja real):

| Classe de asset | `E_forge` estimado | `ΔE` por uso (atalho) | `N*` |
|---|---|---|---|
| `calib-map` (`nlu/calib.json`) | ~0 tokens (cômputo puro, `calib.go`) | n/a (melhora decisão, não substitui pedido) | 0 — não é asset de economia, é asset de política |
| `nlu-example`/`lexicon` | ~0,5–2k (texto curto forjado + eval) | ~32k quando converte miss→hit | < 1 uso |
| `intent-rule`/`method` forjado | ~3–10k (LLM escreve JSON + gate) | ~32k | < 1 uso |
| `script`/macro (passos minerados) | ~2–8k | ~30–100k (tarefa multi-passo) | < 1 uso |
| **Infra** (ledger+gate+eval, PR P1/P6) | custo de engenharia | amortiza sobre **todos** os assets | o primeiro asset paga |

**A conclusão que inverte a intuição.** Com os preços atuais, `N* < 1` para
quase todo asset correto que será usado: a forja se paga no primeiro uso.
**`N*` não é o gargalo.** Os gargalos reais são `ρ_c` (o asset é de fato
disparado?) e `q` (o asset erra?). Isso desloca a métrica de "vale forjar?"
para duas perguntas medíveis: *cobertura futura* (demanda estimada por
sinais) e *taxa de erro* (gate held-out + sinais de falha em produção). O
segundo gargalo implícito é a **externalidade de biblioteca** — cada asset
novo pode tornar o match mais lento/errado (utility problem, Minton 1988);
é o que o termo `λ·|L|` de `J(L)` precifica e o `Gate` mede na prática via
regressão held-out.

### 1.4 O experimento exato no Dell

Quatro peças, cada uma um número novo no relatório:

**E1 — ledger vivo.** `sigAppend` nos pontos de decisão do cérebro (hoje zero
callers em produção): miss em `brNotUnderstood` (`brain.go:149`) e no-method
(`brain.go:155`), retornos `nil` do gate online `brHeadlessLevel0`
(`brain.go:610–611, 624–626`) e do interativo `brLevel0Interactive`
(`brain.go:655–656, 680–681`), além de `asset_use`/`asset_fail` quando um
asset de overlay decidiu o resultado e depois verificou/falhou (ver P1 em
§5). Kinds novos: `asset_use`, `asset_fail`, `forge_cost`.
`Signal` (`signals.go:26–36`) já tem `Kind/Class/RepoSig/Text/Chosen/
Verified/Extra` — `Extra` carrega `{conf, channel, ms, bytes}`.

**E2 — contrafactual de tokens no eval.** `jazz eval offline --tokens`: para
cada tarefa tratada sem modelo, `tokens_saved = Ê_llm(cat)` = média de
In+Out por classe no corpus de sessões (via `imScan`, `improve_load.go:334`)
+ reconstrução do 1º request (`estTokens`) quando a classe não tem histórico.
`eoSummary` (`evaloffline.go:91–107`) ganha `tokens_saved` total e por `cat`.
**Número-alvo:** `tokens_saved ≥ 20k` médio por tarefa `atalho` tratada
(conservador vs. os 32,3k medidos); relatório marca "estimado" vs "medido".

**E3 — relatório de N\*.** `jazz forge report` lê `signals.jsonl` +
ledger de `forge_cost` e imprime por classe: `f_c` (pedidos/semana),
`ΔE_c`, `N*`, `N_obs` (usos medidos), semanas para break-even. **Número-alvo:**
tabela reproduzível em 2 runs; `N_obs` cresce e confirma `ρ_c` previsto ±50%.

**E4 — gate como EvalFn.** A peça que falta para o loop: `eoRunOverlay(tasks,
overlayDir)` retorna `Metrics{Accuracy, FalseSuccess, Coverage, N}` —
assinatura de `EvalFn` (`forge_gate.go:56`) — e `Gate` (`forge_gate.go:143`)
promove/rejeita. `Split` (`forge_gate.go:92–126`) já é determinístico
(FNV estratificado). **Número-alvo:** fixture com um asset bom (promovido,
+1 tarefa held-out tratada) e um plantado ruim (rejeitado, falso sucesso ou
regressão detectada) — prova do loop fechado em < 60 s no Dell.

**Sucesso do eixo inteiro:** (i) `signals.jsonl` cresce em produção;
(ii) `tokens_saved` por run impresso; (iii) primeira linha `N_obs ≥ N*`
registrada; (iv) `false_success` permanece 0.

### 1.5 Ordem de forja

Com `n` candidatos e orçamento de forja `B` tokens: mochila 0/1 sobre valor
`V_a = Ê[N|T]·ρ·ΔE`. Ganância por razão `V_a/E_forge(a)` — comparada ao
melhor item único — é a 2-aproximação clássica de mochila, e com `n` pequeno
a exaustiva é trivial; a ordem repetida sob orçamento mensal é um problema
online (Kalai–Vempala 2005). Na prática: `jazz forge report` ordena, humano
promove topo-a-fundo. **Nunca** forjar antes do ledger medir demanda — infra
primeiro (P1), depois mineradores.

### 1.6 Menor PR que prova

**P-ledger** (P1+P2+P7 de §5 combinados): signals ligados + `--tokens` no eval +
`forge report`. ~500 linhas, stdlib puro, sem mudança de comportamento do
cérebro. Provado quando `jazz eval offline --tokens` imprime `tokens_saved`
e `signals.jsonl` tem ≥1 `miss` e ≥1 `asset_use` após um dia de uso.

---

## 2. POLÍTICA CALIBRADA — o algoritmo final agir/perguntar/menu

### 2.1 Estado atual: constantes que escondem uma teoria

Hoje três decisões independentes, todas binárias, todas com constantes mágicas:

```
intent:   Understood() ⇔ conf ≥ 0.6            (nlu.go:45, 62–64)
resolve:  brPick auto-pick ⇔ margem > 16 dist  (methods.go:1151, fzSlip*8)
online:   intercepta modelo ⇔ conf ≥ 0.85      (brain.go:596, 604–643)
```

Cada uma é um caso degenerado da mesma regra. **Descompilando os custos
implícitos** (teorema de Chow 1970 + Elkan 2001: rejeitar/agir-ótimos vêm de
razões de custo, não de probabilidades absolutas):

- `0.6` ⇔ `c_ask/C_err = 0.4` para risco baixo;
- `0.85` ⇔ `c_defer/C_err = 0.15` no gate online.

As constantes *já são* razões de custo — só que escolhidas a olho, globais
(mesma τ para `open_file` e `kill_process`), e com `conf` não-calibrada
(`nlu.go:1063–1067`: o número é heurístico de canal, não posterior).

### 2.2 A regra de três vias

Por pedido, após `Parse` (`nlu.go:928`) e filtro de aplicabilidade
(`mhApplicable`, `methods.go:141–153`), escolha `a* ∈ {act, menu, defer}`
minimizando custo esperado:

```
p̂      = calib_map[channel](in.Confidence)    // P(intent correto | canal)
p̂_sel  = calib_map[resolve](margem niTop)     // P(pick de entidade correto)
p_ok   = p̂ · p̂_sel
C(m)   = w[risk(m)] · C_base                  // w = {low:1, med:4, high:16}

E[act]   = (1 − p_ok)·C(m) + c_run(m)         // c_run ≈ CostMS normalizado ≈ 0
E[menu]  = c_ask + (1 − p̂_menu)·C(m)          // menu cobre resposta c/ prob p̂_menu
E[defer] = E_llm(c)·κ   (online)  |  c_giveup (offline)

a* = argmin
```

**Teorema (Chow–Elkan instanciado).** Quando o menu resolve com certeza
(`p̂_menu ≈ 1`, caso típico: 3 opções claras), `E[menu] ≈ c_ask` e

```
act ⇔ p_ok ≥ 1 − c_ask/C(m) ≡ τ(risk)                 (2)
```

ou seja: **um τ por classe de risco, derivado, não escolhido.** Com
`c_ask = 0,4·C_base`: τ(low)=0,60 (coincide com `nuMinConf` — o palpite atual
estava calibrado para risco baixo), τ(med)≈0,90, τ(high)≈0,975. A decisão de
"hoje `kill_process` age com a mesma confiança que `open_file`" vira
impossível: `mhMethod.Risk` (`methods.go:70`, **morto hoje**) entra como
multiplicador de custo — nada além disso, por desenho: risco não muda a
probabilidade de acerto, muda o preço do erro (Elkan 2001, razão de custos
como único grau de liberdade). `w` inicial {1,4,16} reflete reversibilidade
(med = escrita revertível via git; high = kill/push/install, irreversível ou
externo) e será **ajustado pelo sweep do eval**, não por debate — só a razão
importa. Observação honesta: comandos perigosos ainda passam por `caps.go`
(`cbParseRisk` `:47–57`) e `Permit`; a política é a *primeira* linha de custo,
o guardrail a última — um falso positivo de intent em método `high` custa no
mínimo um prompt de permissão espúrio e confiança do usuário.

`risk(m)` combina com o risco dos passos (`cbRisk`, `caps.go:34–45`): proposta
`risk_efetivo = max(m.Risk, max_risk_dos_passos)` — um método `low` com passo
`bash` herda o custo do passo.

### 2.3 Menu = conjunto conformal (a única novidade matemática)

O que o menu atual não tem: garantia de que a resposta certa **está nele**.
`in.Alt` (`nlu.go:56`) guarda ≤3 nomes de intents, **sem scores**
(`nlu.go:982–983, 999–1000`). Com calibração por canal + `predictTopK`
(scores nos runner-ups), o menu vira um conjunto de previsão conformal
estilo APS (Romano, Sesia & Candès, NeurIPS 2020; Angelopoulos & Bates 2021):

```
nonconformidade: s_i = 1 − p̂(y_i | x_i)   (ou massa acumulada APS)
quantil:         q̂ = ⌈(n+1)(1−α)⌉/n-ésimo quantil dos s_i de calibração
menu:            C_α(x) = intents ordenados por p̂ até massa ≥ q̂
```

**Teorema (cobertura).** Sob troca-permutável dos pontos de calibração,
`P(intent_verdadeiro ∈ C_α(x)) ≥ 1 − α` **marginalmente**, com `|C_α|`
adaptativo (fácil → 1 opção = age; difícil → k opções = pergunta). Prova:
quantil empírico corrigido `(n+1)` = estatística de ordem de variáveis
permutáveis (Angelopoulos & Bates 2021, §2.1). É o único lugar onde entropia-
como-incerteza entra legitimamente: o **tamanho** do menu reflete a dispersão
do posterior — mas o gatilho continua sendo `p_ok` calibrado, não H.

**Caveats honestos:** (i) cobertura é marginal, não condicional — um menu com
90% global pode cobrir 60% numa classe rara; mitigar estratificando por canal
(quando `n_canal ≥ ~100`). (ii) `n` total de calibração ≈ 750 (644 exemplos +
100 cegos + prompts do eval) → α=0,1 usa o quantil dos ~75 piores: variância
alta; reportar IC. (iii) Menu de **entidade** (`brChoose`, `methods.go:1157`)
usa a mesma máquina com margem→P como score — unifica os dois menus sob uma
garantia só.

### 2.4 Lema menu-antes-do-modelo

**Afirmação.** Na região coberta pelo menu (`intent ∈ C_α(x)`, prob ≥ 1−α),
`menu` domina `defer-online`: `c_ask` (segundos do usuário, ~0 tokens)
≪ `E_llm(c)` (~32k tokens medidos na classe atalho). Logo `defer` só é ótimo
na cauda `α` + quando o menu é informativamente vazio (`|C_α|=1` já é `act`;
`|C_α|` grande demais = pergunta inútil → defer).

**Consequência arquitetural.** A política final é **menu-first**: perguntar ao
humano antes de perguntar ao modelo — exatamente o que `brLevel0*`
(`brain.go:604–696`) implementa de forma ad-hoc. A contribuição é tornar o
"antes" *derivado*: τ_defer = 1 − `c_defer/C(m)` com `c_defer` = custo real
medido da classe (a ponte com §1: **o custo do defer É o `E_llm` do modelo
amortizado** — as duas descobertas usam a mesma moeda).

E o caso `defer-offline` (`c_giveup`): pedido não atendido = falha no eval,
precificado = 1·C_base por padrão (falso-bloqueio dói como erro leve) —
também sweep-tável. Madras, Pitassi & Zemel (NeurIPS 2018) e Mozannar &
Sontag (ICML 2020) dão o frame: deferir ótimo exige comparar P(brain certo)
vs. P(modelo certo)·custos — com o modelo fora do caminho crítico, "defer"
offline = dar up honestamente, e a métrica `handled=false` do eval já o
contabiliza sem falso sucesso.

### 2.5 VOI para slots e a segunda decisão

`brAskSlot` (`methods.go:1222`) já pergunta slot faltante; a regra de Horvitz/
Howard (value of information): perguntar iff
`E[Δcusto | resposta] > c_ask`. Com slots required vs. opcionais isso é
binário-hoje e razoável; o upgrade medível é *qual* pergunta: a que minimiza
custo esperado restante — ordenar slots por `E[ΔE_llm evitado]` quando o
ledger existir. Marcar como fase 2; não bloqueia o algoritmo.

### 2.6 Teste que prova no Dell

**T1 — `jazz eval calib`** (novo): replay de `Parse` sobre 644 exemplos
(`nlu/intents.json`) + 100 cegos (`nlu/blind_test.json`) + 75 prompts do eval.
Rotula `(channel, conf, predicted==want)`; canal derivado de `Evidence`
(hoje string: "regra…", "empate…", "classificador…" — promover a campo
`Intent.Channel` para não depender de parsing). Saída: tabela ECE/MCE/Brier
por canal (`calib.go:164,182,198`), fits Platt (`calib.go:220`) com LOO
(isotônico só `n_canal ≥ 50` — já decidido), emite `nlu/calib.json` (asset,
~200–500 B). **Alvo:** ECE pós-Platt < 0,15 por canal com `n ≥ 30`; relatório
marca canais com dados insuficientes em vez de forjar mapa.

**T2 — sweep de τ.** `jazz eval calib --sweep`: τ ∈ {0,5..0,99} × risco ∈
{low,med,high} → para cada ponto: coverage, questions_asked/needed,
false_success sobre o eval. **Alvo:** existe τ med/high com `false_success=0`
e `Q_asked/Q_needed ≤ 1,2`; os τ adotados passam a ser **artefato do eval**
(commitados em `nlu/calib.json`), não constantes em `nlu.go:45`.

**T3 — cobertura do menu.** Com `predictTopK` + calib.json: medir
`P(want ∈ C_α)` empírica no cego+exemplos. **Alvo:** ≥ 0,90 a α=0,1; tamanho
mediano do menu ≤ 3; reportar cobertura por canal onde `n` permite.

**Sucesso do eixo:** as três constantes `0.6/0.85/16` viram saídas de medida;
`false_success=0` mantido; perguntas ≤ 20% acima do necessário.

### 2.7 Menor PR que prova

**P-policy** (P3+P4 de §5): `eval calib` + `Intent.Channel` + `calib.json` +
`brDecide` substituindo os três `if conf ≥ const`. O menu conformal fica para
o PR seguinte (precisa de `predictTopK`); a versão mínima já prova τ por
risco com `false_success=0`.

---

## 3. COMPRESSÃO=ASSET — trajetória → asset mínimo validado

### 3.1 O pipeline, com o que já existe e o que falta

`program-synthesis-library-learning.md` especificou C1–C6. Aqui a versão
executável, cada passo com sua fonte de dados **já existente**:

```
C0 corpus:    signals.jsonl (miss|correction|cmd_fixed — signals.go:28)
              + obs task events (obEmit "task", task.go:279)
              + planos verificados (pcStore, plancache.go:433; Wins/Fails :56–76)
C1 candidato: anti-unificação por (intent, padrão de args): valores que
              variam entre traces viram slots tipados (kinds de nuHit, nlu.go:66)
              — exige ≥ k=3 traces suporte [estimativa; sweep]
C2 minimizar: ddmin sobre {passos} ∪ {bindings} ∪ {pré-condições}
              teste Θ = replay nos traces-suporte → 1-minimal
C3 verificar: herdar checks verify: dos métodos-fonte + pós-condições
              observadas no trace (file_exists/exit_zero/… — brVerify
              methods.go:1680–1763). Sem verify: não é asset, é hipótese.
C4 screen:    replay held-in + ΔJ > 0 (bytes do asset vs. miss→hit convertido)
C5 gate:      Gate(baseline=eval, candidate=eval+overlay) (forge_gate.go:143)
C6 promover:  overlay dir + entrada no ledger (forge_cost, bytes, report)
vida:         asset_use/asset_fail signals → 2 fails → quarentena
              (precedente: pcEvictFails=2, plancache.go:46,416–419;
              h2PlanPoison, hardening2.go:960–986)
```

### 3.2 "Mínimo" tem definição operacional: ddmin

"Asset mínimo" não é Kolmogorov (VIAGEM, já decidido) nem estética. É
**1-minimalidade contra um teste objetivo**: Zeller & Hildebrandt (TSE 2002)
— ddmin remove elementos enquanto o teste passa, produzindo conjunto onde
**nenhum elemento único é removível** mantendo validade. Em jazz:

- universo = passos do método candidato + slots + pré-condições (~10
  elementos — ddmin custa O(n) testes para 1-minimal, O(n²) pior caso; cada
  teste é replay in-process de strings → **microssegundos, trivial no Dell**);
- Θ = "ainda verifica nos traces-suporte" — o *mesmo* `brVerify`.

Honestidade: 1-minimal ≠ globalmente mínimo (mínimos locais múltiplos); é o
sentido barato e reproduzível de "sem gordura", e empilha com o gate — o
ΔJ/held-out rejeita minimizações que cortaram demais (sobre-generalização
é detectada como falha held-out ou falso sucesso, exatamente o mecanismo
anti-sucesso-espúrio de ASI — Wang, Gandhi, Neubig & Fried,
arXiv:2504.06821: validar contra trajetória truncada).

### 3.3 Onde MDL entra de verdade

`J(L) = Ê[L] + λ·|L|` concretiza-se assim, sem abstrair demais: `|L|` = bytes
do asset promovido (medido, ~200–800 B por método); `Ê[L]` = custo esperado
de decisão com o asset — medido pelo próprio `Gate` como `Metrics` no
held-out (accuracy/coverage/false_success já são o funcional). O gate *é* o
teste de aceitação MDL vestido de código; ddmin produz o candidato que
minimiza a descrição **sujeito a** validade. Os três vencedores se fecham:
compressão (§3) gera o asset → gate (§1, `EvalFn`) mede sua economia →
política calibrada (§2) decide seu uso em produção.

### 3.4 O que NÃO existe ainda (honesto)

O minerador automático (C1) não existe — `pcStore` guarda planos verificados
mas ninguém anti-unifica. **Não é o menor PR.** A tese "compressão=asset" é
provada pelo *loop* aceitar asset mínimo e rejeitar asset gordo/errado —
com candidato escrito à mão. O minerador vem depois, guiado pelo ledger
(quais classes realmente dão `miss` — §1 decide onde minerar, não
entusiasmo).

### 3.5 Menor PR que prova

**P-asset** (P6+P8 de §5): `eoRunOverlay→Gate` + `forge_ddmin.go` + fixture
overlay (1 método mínimo forjado à mão + 1 variante gorda/errada plantada).
Provado quando o gate promove o mínimo, rejeita o plantado, e `signals.jsonl`
registra `asset_use` do promovido em produção.

---

## 4. Mapa file:line (o que existe vs. o que falta)

| Peça | Arquivo:linha | Estado |
|---|---|---|
| `nuMinConf=0.6`, `Understood` | `nlu.go:45, 62–64` | constante → derivar por risco |
| canais do `Parse` (regra/empate/NB/shape/residual) | `nlu.go:954–1066` | existe como `Evidence` string; falta campo `Channel` |
| softmax posterior `nuBeta=6`, `predict` | `nlu.go:1332, 1434–1478` | existe; falta `predictTopK` com scores |
| `Intent.Alt` (≤3 nomes, **sem score**) | `nlu.go:57, 982–983, 999–1000` | meio-menu; falta scores p/ conformal |
| `mhMethod.Risk` | `methods.go:70` | parseado, **nunca lido** |
| `brPick` margem `fzSlip*8=16` | `methods.go:1151` | constante → calibrar margem→P |
| `brChoose` menu de entidade | `methods.go:1157–1210` | existe; falta garantia de cobertura |
| `brAskSlot` | `methods.go:1222` | existe; VOI ordenado = fase 2 |
| `brVerify` (oráculos objetivos) | `methods.go:1680–1763` | existe; teste Θ do ddmin |
| `brLevel0Min=0.85`, gates online | `brain.go:596, 604–696` | constante → τ_defer derivado |
| pontos de miss p/ ledger | `brain.go:149, 155, 610–611, 624–626, 655–656, 680–681` | mapeados; **sem sigAppend** |
| `calib.go` (bins/ECE/Brier/Platt/isotônico) | `calib.go:114–330` | completo, **morto** (só testes) |
| `Gate`/`Split`/`EvalFn`/`Metrics` | `forge_gate.go:38–173` | completo, **morto** (só testes) |
| `Signal`/`sigAppend`/`sigRead*` | `signals.go:26–131` | completo, **morto** (só testes) |
| `cbRisk`/`cbParseRisk` por ferramenta | `caps.go:34–57, 94–106` | vivo; combinar com `m.Risk` |
| eval harness + `eoSummary` + `false_success` | `evaloffline.go:64–107, 171–407` | vivo; falta `--tokens`/`--calib`/`--overlay` |
| `estTokens` (chars×10/35) | `agent.go:275–291` | vivo; usar p/ contrafactual |
| `Usage` por mensagem / `imLoadSession` | `types.go:43–57`, `improve_load.go:68–149` | vivo; falta agregar por classe |
| corpus: `obEmit`/task, `pcStore` | `task.go:279`, `plancache.go:433` | vivo |
| quarentena/poison precedente | `hardening2.go:960–986` | vivo; padrão p/ asset_fail |
| `dataDir()` → `signals.jsonl` | `config.go:363–378` | vivo |
| corpora: 75 tarefas / 100 cegos | `evals/offline/tasks.json`, `nlu/blind_test.json` | vivos |
| baseline medido: 32,3k tokens atalho | `results/dell-0.9-progress.md` | 1 ponto; generalizar por classe |

---

## 5. Propostas Go (Dell, stdlib, em ordem de dependência)

Custo 386 presume: alocações pequenas, `math.Log/Exp` contados, arquivos JSONL
streaming (`bufio`, padrão `sigRead` `signals.go:84–106`).

**P1 — ledger vivo (signals ligados).** Mecanismo: `sigAppend` nos 6 pontos de
miss/decisão de `brain.go` + kind `asset_use` emitido quando o método escolhido
veio de overlay (`mhMethod` ganha `Origin string` setado no load) + kind
`forge_cost` gravado pelo comando de forja. Arquivos: `brain.go` (~40 ln),
`methods.go`/`intents.go` loaders (~20 ln), `signals.go` (+2 kinds). Custo:
1 append JSONL ~200 B por evento, fsync por lote — desprezível vs. 14 ms p50.
Métrica: `signals.jsonl` não-vazio; `sigCount()` por kind; miss/semana por
`Class`. Impacto 5 (sem ele nada mede). Esforço S. Risco: nulo (append-only,
falha = log e segue, padrão já em `sigAppend`).

**P2 — `jazz eval offline --tokens`.** Mecanismo: por tarefa `handled`,
`tokens_saved = Ê_llm[cat]` do corpus de sessões (agregar `Usage` por classe
via `imScan`/`imLoadSession`, `improve_load.go:334`) ou `estTokens` do 1º request reconstruído;
`eoSummary` imprime total + por `cat`, marcando medido vs. estimado.
Arquivos: `evaloffline.go` (+~120 ln), `improve_load.go` (+~60 ln agregador).
Custo: leitura streaming de sessões uma vez por run; nada em runtime.
Métrica: `tokens_saved` por run; alvo ≥ 20k médio `atalho`. Impacto 4.
Esforço S. Risco: estimativa grossa de `estTokens` — mitigado rotulando.

**P3 — `jazz eval calib` + `nlu/calib.json`.** Mecanismo: replay `Parse` no
corpus (644+100+75), pares `(channel, conf, ok)`, ECE/MCE/Brier por canal,
Platt LOO (isotônico só `n≥50`), emite asset; `Parse` aplica mapa no fim
(`nlu.go:1067` antes do round). `Intent` ganha `Channel string` (populado nos
branches `nlu.go:954–1066`). Arquivos: novo `evalcalib.go` (~350 ln),
`nlu.go` (+~80 ln), `evaloffline.go` (subcomando). Custo: 750 parses ~ms —
one-shot; runtime = lookup em ≤32 floats, 1 `math.Exp` Platt — desprezível.
Métrica: ECE por canal < 0,15 pós-fit (LOO, não in-sample); `calib.json`
≤ 1 KB. Impacto 5 (destrava P4/P5/P9). Esforço M. Risco: overfit em canais
raros → reportar `n` e não fitar `n<30`.

**P4 — `brDecide` (Chow × risco).** Mecanismo: função pura
`brDecide(in, methods, calib) → act|ask|defer` implementando (2);
`w={low:1,med:4,high:16}` tabela; `risk_efetivo = max(m.Risk, risco passos)`.
Substitui `Understood()` nos 3 call-sites e alimenta `brLevel0*` com τ_defer.
Arquivos: novo `brdecide.go` (~220 ln), `brain.go` (+~60 ln), `methods.go`
(+~30 ln). Custo: 2 lookups float por pedido — zero. Métrica: tabela
τ×risco→{coverage, Q_ratio, false_success} via `--sweep` de T2; alvo
`false_success=0`, `Q_asked/Q_needed ≤ 1,2`. Impacto 4. Esforço M.
Risco médio: caminho quente — mitigar com flag `JAZZ_POLICY=legacy|calib`
e default legacy até o sweep provar.

**P5 — `predictTopK` + menu conformal.** Mecanismo: `predict` devolve top-k
com posteriores (mesma softmax `nlu.go:1474`); `in.Alt` vira
`Alt []struct{Name, P}`; `C_α` por q̂ de calibração; `brChoose`/`brRunIntent`
consomem o conjunto (inclui `|C|=1` → age). Arquivos: `nlu.go` (+~100 ln),
`methods.go`/`brain.go` menu paths (+~80 ln), `evalcalib.go` (q̂). Custo:
k×50 somas extras — trivial; `C_α` ≤ 5. Métrica: cobertura empírica
`P(want∈C_α) ≥ 0,9` no cego+exemplos; menu mediano ≤ 3; IC reportado.
Impacto 3. Esforço M. Risco médio: n pequeno por canal (caveats §2.3) —
estratificar só onde `n≥100`, senão global.

**P6 — `eoRunOverlay→Gate` + `jazz forge eval`.** Mecanismo: carrega eval
corpus + overlay dir candidata → `Metrics` (assinatura `EvalFn`); `Gate`
compara vs. baseline embutida; comando `jazz forge eval <overlay>` imprime
veredicto + `GateReport`. Arquivos: `forge_gate.go` (+~100 ln adapter),
`evaloffline.go` (+~60 ln), fixture `testdata/overlay_{good,bad}`.
Custo: 2× eval run (~30 s no Dell por varredura) — aceitável, roda na forja.
Métrica: good promovido / bad rejeitado (falso sucesso plantado detectado);
determinístico (mesmo seed → mesmo split, `splitHash` FNV `:69–72`).
Impacto 5 (fecha o loop da forja). Esforço M. Risco baixo.

**P7 — `jazz forge report` (N\* por classe).** Mecanismo: `sigRead` agrega
`f_c`, `asset_use`; ledger `forge_cost` por asset; tabela
`classe → f_c, ΔE, N*, N_obs, semanas→*`. Arquivos: novo `forge_report.go`
(~250 ln). Custo: 1 passada streaming no JSONL — ms. Métrica: tabela
reproduzível; primeira linha `N_obs ≥ N*` registrada = prova histórica do
eixo 1. Impacto 4. Esforço S. Risco: demanda ruidosa em semanas iniciais —
reportar IC/`n` por linha.

**P8 — `forge_ddmin` (asset 1-minimal).** Mecanismo: ddmin sobre
passos/bindings/pré-cond do candidato; teste Θ = replay verifica nos
traces-suporte (reuso `brVerify` + fixture `eoFixture`-like, sem fork).
Arquivos: novo `forge_ddmin.go` (~200 ln). Custo: ~20 replays in-process de
~10 elementos — microssegundos na teoria, ms na prática. Métrica:
% elementos removidos; gate ainda promove; `L(L)` bytes final. Impacto 3.
Esforço M. Risco baixo-médio: 1-minimal ≠ global (documentado §3.2).

**P9 — margem de resolução calibrada (`brPick`).** Mecanismo: coleta
`(margem niTop, correto)` no replay do eval (respostas `want` já existem);
fita mapa margem→P; substitui `fzSlip*8` por `p̂_sel ≥ τ(risk)` dentro de
`brDecide`. Arquivos: `nameindex.go`/`methods.go` (+~60 ln), `evalcalib.go`
(+~80 ln). Custo: idem P3. Métrica: menu-rate ↓ ≥ 20% a taxa de
falso-pick constante (alvo 0). Impacto 3. Esforço M. Risco médio: canal de
evidência diferente (distância, não probabilidade) — mapa próprio, não
reusar mapa de intent (já decidido: canais separados).

Ordem: **P1, P3** (medição) → **P2, P7** (economia visível) → **P6**
(loop fechado) → **P4** (política viva, flag) → **P5, P9** (menu/margem) →
**P8** (minimização; C1-minerador depois, quando o ledger disser onde falta).

---

## 6. O que NÃO fazer

- **Não reabrir** entropia-como-decisão, menus-Huffman, SAT, minimax,
  Lyapunov, Mersenne-core — vereditos já dados em `unifying-math-offline.md`.
- **Não** usar USD como moeda: muda por provedor/modelo; tokens é estável.
  Reportar `Cost` à parte se quiser dólares.
- **Não** alegar cobertura conformal **condicional** (por classe/canal) com
  `n` pequeno — a garantia é marginal; estratificar só com `n ≥ ~100`.
- **Não** tratar os 32,3k tokens como constante — é 1 ponto de 1 classe;
  `E_llm(c)` é distribuição por classe, medida no corpus.
- **Não** precificar `E_offline = 0` esquecendo a pergunta: cada menu cobra
  `c_ask` do usuário; o eval já conta `Q` — inclua na economia.
- **Não** promover asset por NPV: NPV **ordena a fila**; admissão é só pelo
  `Gate` (held-out + `false_success=0`). Nunca nessa ordem inversa.
- **Não** fitar mapa de calibração global entre canais, nem isotônico com
  `n < 50` por canal — decidido.
- **Não** reusar o mapa de intent para margem de entidade — evidências de
  natureza diferente (posterior vs. distância de edição); canais separados.
- **Não** construir o minerador C1 antes do ledger medir `f_c`: forjar asset
  para classe sem demanda é desperdício com veredito bonito.
- **Não** usar a regra adversarial de ski-rental quando já há demanda medida —
  o bound 2-competitivo é o fallback do primeiro asset; depois é NPV.
- **Não** colocar LLM no caminho crítico para nada disso — regra de ouro. A
  forja pode usar modelo; o runtime nunca.
- **Não** adicionar `E_forge` e segundos de revisão humana na mesma coluna —
  ledgers separados (`forge_cost.tokens` vs. `forge_cost.review_s`).
- **Não** deixar `asset_use` atribuir vitória ao NB por exemplo forjado
  individual (mistura de votes) — atribuir só quando regra/método de overlay
  decidiu; honestidade de atribuição > contagem bonita.

---

## 7. Fontes

- Karlin, Manasse, Rudolph & Sleator, "Competitive snoopy caching",
  *Algorithmica* 3, 1988 — rent-or-buy determinístico, razão `2−1/B`.
- Karlin, Manasse, McGeoch & Owicki, "Competitive randomized algorithms for
  nonuniform problems", *Algorithmica* 11, 1994 — ski-rental randomizado
  `e/(e−1)`.
- Gershman & Goodman, "Amortized inference in probabilistic reasoning",
  *CogSci* 36, 2014 — o frame compila-uma-vez/reusa-sempre.
- Chen, Zaharia & Zou, "FrugalGPT", *TMLR* 2024, arXiv:2305.05176 — cascata
  barato→caro com até 98% de corte de custo: evidência externa de que
  `ΔE` por classe existe e é grande.
- Chow, "On optimum recognition error and reject tradeoff", *IEEE Trans.
  Inf. Theory* 16(1), 1970 — regra de rejeição por razão de custo.
- Elkan, "The foundations of cost-sensitive learning", *IJCAI* 2001 —
  τ = razão de custos; `mhMethod.Risk` como multiplicador.
- Madras, Pitassi & Zemel, "Predict responsibly: improving fairness and
  accuracy by learning to defer", *NeurIPS* 2018, arXiv:1711.06664;
  Mozannar & Sontag, *ICML* 2020, arXiv:2006.01862 — defer ótimo compara
  as duas decisoras.
- Romano, Sesia & Candès, "Classification with valid and adaptive coverage",
  *NeurIPS* 2020, arXiv:2006.02544 (APS); Angelopoulos & Bates, "Conformal
  prediction: a gentle introduction", *Found. Trends ML* 2023,
  arXiv:2107.07511 — menu = conjunto conformal, teorema de cobertura.
- Horvitz, "Reasoning about beliefs and actions under computational
  resource constraints", *UAI* 1987; Howard, "Information value theory",
  *IEEE Trans. SSC* 2(1), 1966 — VOI: perguntar iff valor da resposta > custo.
- Zeller & Hildebrandt, "Simplifying and isolating failure-inducing input",
  *IEEE TSE* 28(2), 2002 — ddmin = definição operacional de "mínimo".
- Rissanen, "Modeling by shortest data description", *Automatica* 14,
  1978 — MDL; Minton, "Quantitative results concerning the utility of
  explanation-based learning", *AAAI* 1988 — utility problem: a
  externalidade de biblioteca que `λ·|L|` precifica.
- Guo et al., "On calibration of modern neural networks", *ICML* 2017,
  arXiv:1706.04599; Vaicenavicius et al., "Evaluating model calibration in
  classification", *PMLR* 89, 2019; Platt 1999; Niculescu-Mizil & Caruana,
  *ICML* 2005 — Platt em n pequeno; ECE como diagnóstico, não objetivo.
- Kalai & Vempala, "Efficient algorithms for online decision problems",
  *JCSS* 71, 2005 — ganância repetida ≈ ótimo (ordem de forja).
- Ellis et al., "DreamCoder", arXiv:2006.08381; Bowers et al., "Stitch",
  arXiv:2211.16605 / DOI:10.1145/3571234; Nye et al., "babble",
  arXiv:2212.04596 — compressão biblioteca = descoberta de skill (contexto).
- Cai et al., "LATM", arXiv:2305.17126; Snell et al., "Sleep-time compute",
  arXiv:2504.13171; Khattab et al., "DSPy", arXiv:2310.03714; Wang, Gandhi,
  Neubig & Fried, "Inducing Programmatic Skills for Agentic Tasks" (ASI),
  arXiv:2504.06821 (COLM 2025) — forja-fora-do-caminho-crítico e validação por
  trajetória truncada (contexto, já citados nos docs anteriores).
