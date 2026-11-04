# Negentropia como objetivo? Active inference, free energy e o que sobra depois do teste

**O dono pergunta: "minimizar surpresa / impor ordem" pode ser a FUNÇÃO OBJETIVO do agente offline — ou reduz ao
que já temos?**

Resposta curta: **reduz, mas não ao que ele acha.** Reduz a três coisas que ele já tem ou já decidiu ter: (a) `-log p`,
que é log-loss e é a tese MDL do projeto; (b) utilidade esperada com custos assimétricos, que o código **não** faz
(usa um limiar único para 50 intenções com riscos de 1 a 30000 ms); (c) um termo epistêmico que é **valor da
informação** — Lindley 1956, não Friston — e que só é computável sob um modelo de verossimilhança que o jazz não
possui. A parte "negentropia" é contabilidade física correta e irrelevante por 19 ordens de grandeza.

Pesquisa de 04/11/2026. Continua [unifying-math-offline.md](unifying-math-offline.md) (entropia × Chow × calibração),
[math-offline-brain.md](math-offline-brain.md) (ECE, Platt, isotônico, limiares) e
[offline-brain-research.md](offline-brain-research.md). **Não repete** a prova de que entropia não é a variável de
decisão (unifying §3.1) nem as propostas P1–P9 de calibração (math-offline-brain §5). O objeto aqui é outro:
**active inference / free-energy principle como seleção de AÇÃO**, e a física da negentropia como objetivo.

Convenções: `file:line` refere-se ao `main` @ `584494d`. **Medido** = número já registrado em `results/`
(`dell-0.9-progress.md`), não re-medido nesta sessão. **(estimativa)** = conta minha. "D610" = Dell Latitude D610,
Pentium M 1 núcleo, 2 GB, `linux/386`, ~2 W (valor do dono), Go 1.27.2.

Este documento é adversarial por encomenda. Onde a ideia se sustenta, diz **LOAD-BEARING** e mostra a lei. Onde é
metáfora boa, diz **METÁFORA ÚTIL** e mostra o que ela esconde. Onde não compila, diz **VIAGEM** — e por quê.

---

## 0. Placar

| # | Peça | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | "Minimizar surpresa" como objetivo do agente | **VIAGEM** como objetivo · **LOAD-BEARING** como `-log p` | Surpresa só existe se existe `p(o)`. O agente não prevê observações; ele satisfaz um verificador (`brain.go:363`). Sem preferências, minimizar surpresa = não fazer nada (dark room). Com preferências, vira utilidade. |
| 2 | Free energy variacional (VFE) como função de perda | **LOAD-BEARING**, mas já está no código | `VFE = -log p + KL(q‖p)`; com `q = p` é `-log p` = log-loss. `calib.go:233` (Newton de Platt) e `nuTrain` (`nlu.go:1363`) já minimizam exatamente isso. Nome novo, conta velha. |
| 3 | Expected free energy (EFE) como seleção de ação | **REBRANDING — provado** | EFE = EU + λ·I(s;o) com **λ = 1 forçado** pela convenção `u = log p(o\|C)` (§3.2). EU deixa λ livre; EFE o fixa. É **subconjunto** de EU, não superconjunto. Os autores escrevem isso: "reduz a decision theory" sem ambiguidade/risco e "reduz a Bayesian design" sem preferências (Sajid et al. 2022). |
| 4 | EFE decide algo que EU+Chow não decide | **NÃO — provado com número** | Com o modelo de menu, a regra é `(1-q_top)·ℓ + λ·H(a) > c_ask + (1-p_in)·ℓ_hand`. O único termo que EFE acrescenta é `H` em bits, e ele só muda a decisão via λ. Nos dois pares concretos, `λ=1` está **~4× além** do ponto em que a política vira, e `p_in` (cobertura do menu) — que EFE não tem — é quem ordena (§4.3). |
| 5 | Equilibrar explorar × agir | **VIAGEM** no runtime | Não há aprendizado em runtime — a forja aprende offline. O análogo não é bandit, é "perguntar/sondar antes de agir", que é VOI, não exploração. |
| 6 | Valor epistêmico de perguntar | **LOAD-BEARING** — e é a única peça nova | `EVSI` de Lindley 1956. Aplicável a 2 ações que o jazz já tem: menu (`methods.go:1157`) e sonda de fato (`brain.go:225`). Falta o modelo `p(resposta\|intenção)`. |
| 7 | Negentropia (Schrödinger, Prigogine, Brillouin) | **METÁFORA ÚTIL** como contabilidade · **VIAGEM** como objetivo | `ε = ΔI/ΔS₀ ≤ 1` é identidade, não meta. No D610 `ε ≈ 6×10⁻²⁰` (§6.3): otimizar joule é medir a fonte, não a inteligência. |
| 8 | "Demônio de Maxwell mais barato" | **METÁFORA ÚTIL** com mapa exato | Medir = `signals.jsonl` (`signals.go:52`); apagar = comprimir a trajetória em asset. Bennett 1982: o passo irreversível é o apagamento — e ele é pago em tokens na forja. |
| 9 | Máxima ordem por joule/token | **joule: VIAGEM** · **token: LOAD-BEARING** | 32,3k tokens → 0 e 14–21 s → 0,13 s (medido). Essa é a compressão real. O joule é 19 ordens de grandeza longe do limite e nunca é o recurso escasso. |

**Resumo de uma frase:** a intuição do dono está certa no *fenômeno* (o agente exporta desordem para impor ordem
local) e errada na *lei* (não há teorema de negentropia que selecione ação). O que seleciona ação é perda esperada
sob posterior calibrado; o único pedaço disso que o jazz ainda não tem é o **preço da informação**, e ele tem nome
desde 1956.

---

## 1. A tese, decomposta

O dono está dizendo algo como:

> "O agente é uma bomba de negentropia: impõe ordem local (tarefa resolvida e verificada) exportando desordem
> (calor/custo), como a vida. Offline + comprimido + determinístico é o demônio de Maxwell mais barato: máxima
> ordem por joule/token. Então 'minimizar surpresa' pode ser a função objetivo."

Isso não é testável nesse nível. Decomposto em afirmações que **são**:

- **T1 (objetivo).** Existe uma quantidade escalar, computável por pedido, cuja minimização produz o comportamento
  desejado do agente offline, e essa quantidade é "surpresa" (`-log p` de algo).
- **T2 (ação).** A seleção de ação por *expected free energy* decide casos que utilidade esperada + opção de
  rejeição (Chow) + posterior calibrado não decidem.
- **T3 (exploração).** O agente precisa equilibrar explorar × agir, e EFE faz isso de graça.
- **T4 (informação).** O valor de uma pergunta é a redução de surpresa esperada, e isso conecta o menu top-k ao
  "quando perguntar".
- **T5 (física).** "Ordem por joule" é uma métrica com conteúdo para esta máquina.
- **T6 (compressão).** Offline comprimido é a forma mais barata de impor ordem por token.

T1–T6 são o roteiro. §2 mata T1, §3–§4 matam T2 e T3, §5 salva o que sobra de T4, §6 mata T5 e dá conteúdo a T6.

---

## 2. "Surpresa" no código: onde `-log p` existe e onde não

### 2.1 Onde existe — e é log-loss

A free energy variacional é

```
F = E_q[log q(s) - log p(o,s)] = KL(q(s) ‖ p(s|o)) - log p(o)  ≥  -log p(o)
```

com igualdade quando `q` é a posterior exata. Minimizar `F` sobre `q` é inferência; sobre parâmetros, é maximizar
`log p(o)`. Para um classificador, `-log p(y|x)` é a log-loss. O jazz já minimiza isso em dois lugares:

- `calib.go:220` `PlattFit` — Newton damped sobre `L = -Σ[y ln p' + (1-y) ln(1-p')] + (λ/2)(a²+b²)` (`calib.go:233-260`);
- `nlu.go:1363` `nuTrain` — tabela de log-probabilidades por balde, com suavização de Laplace (MLE = mínima
  log-loss no treino).

Ou seja: **T1 na forma "VFE" não é uma ideia nova a implementar; é a função de perda que já está escrita** — mesmo
que `PlattFit` ainda não tenha chamador fora de teste, a conta existe e é a mesma. O que
muda o comportamento não é chamá-la de free energy; é *usá-la para decidir* — e a decisão não sai de `-log p`.

### 2.2 Onde não existe — e por que o objetivo não é surpresa

O agente não tem um modelo generativo de observações futuras. Ele tem um **verificador**. `brExecute`
(`brain.go:261`) roda um método e termina em `tkSucceeded` só depois de `brVerify` (`brain.go:363`,
`methods.go:1680`). O critério de sucesso é a saída de um comando (`exit_zero`, `outparse`, `file_exists`), não a
probabilidade de uma observação. Não há `p(o)` para o qual calcular `-log p(o)`.

E isso não é leitura minha: a própria fonte define a grandeza em termos do que o jazz não tem. Friston (2010), nos
pontos-chave: "Surprise rests on predictions about sensations, which depend on an internal generative model of the
world. Although surprise cannot be measured directly, a free-energy bound on surprise can be." Sem modelo
generativo de sensações, não há surpresa a limitar — há só um comando que passou ou não passou.

Isso não é um detalhe de implementação; é o que separa o jazz da FEP. A FEP supõe que o agente tem um modelo do
mundo e que "surpresa" é a divergência entre o previsto e o observado. O jazz supõe o contrário: o mundo é
consultado por comando, e a verdade é o exit code. **A grandeza que o agente minimiza é perda esperada sob o
verificador, e o verificador é ground truth, não likelihood.**

E se você insistir em "minimizar surpresa" como objetivo único, o resultado é conhecido e é o oposto do desejado:
o **dark room problem** (Sun & Firestone 2020). Um agente que só minimiza erro de predição escolhe o ambiente mais
previsível — para o jazz, "não entendi o pedido" ou "não tenho método", que é exatamente o comportamento que o
código combate (`brNotUnderstood`, `brain.go:214`; `false_success == 0` como restrição bloqueante em
`forge_gate.go:155`). A resposta padrão da FEP é "preferências sobre observações" — que é uma função de utilidade
com outro nome. Ou seja: T1 ou é indefinida, ou degenera, ou volta para EU. Não há quarta saída.

### 2.3 O código já tem uma "surpresa" — binária, e ela só serve para parar

`recover.go:180-236`: `rvExplore` conta comandos de leitura que não trouxeram fato novo. "Fato novo" é
`rvFactKey(out)` — FNV-1a sobre as linhas normalizadas, ordem-independente (`recover.go:198-220`). Três leituras
seguidas sem fato novo → para e pergunta (`recover.go:242`).

Isto é a coisa mais próxima de "surpresa" no código: um teste de novidade de 64 bits, sem probabilidade, sem
gradiente. E o seu papel é **terminação**, não seleção: ele nunca escolhe qual comando rodar, só detecta que a
sequência ficou estéril. É a versão degenerada de `I(fato; comando)`: binária, e usada como orçamento.

**Veredito §2:** T1 é VIAGEM como objetivo. Como `-log p`, é LOAD-BEARING e já está implementada. A "surpresa" que
existe no código é um detector de novidade usado para parar — o que é honesto e suficiente para o que ele faz.

---

## 3. Active inference como seleção de ação: o que é, e por que é um caso particular

### 3.1 A definição

Active inference seleciona políticas `π` minimizando a **expected free energy** (EFE). Na forma epistêmica/pragmática
(Da Costa et al. 2020, eq. 10 e §2.3; Parr, Pezzulo & Friston 2022, cap. 4):

```
G(π) = E_{q(o,s|π)}[ log q(s|π) - log p(o,s|C) ]

     = - E_{q(o|π)}[ KL( q(s|o,π) ‖ q(s|π) ) ]  -  E_{q(o|π)}[ log p(o|C) ]
       \_________________ termo epistêmico ___/     \___ termo pragmático ____/

     = - I(s ; o | π)  -  E[ log p(o|C) ]
```

e a política é escolhida por `Q(π) = σ(-G(π))`. A decomposição alternativa, algebricamente idêntica, é
`G = risco + ambiguidade` com `risco = KL(q(o|π) ‖ p(o|C))` e `ambiguidade = E_q[H[p(o|s)]]` (Da Costa et al. 2020).

O ponto que interessa: **o primeiro termo é informação mútua**. Não "entropia", não "surpresa": informação entre o
que se observa e o estado oculto, sob a política.

### 3.2 A prova: EFE = EU + λ·I, e λ vem da convenção, não de um teorema

Seja `u(o)` uma utilidade em unidades arbitrárias e `p(o|C) ∝ exp(u(o)/λ)` a prior de preferência, com `λ > 0` em
unidades de `u` por nat. Então

```
- E[log p(o|C)] = -E[u(o)]/λ + log Z,        Z = Σ_o exp(u(o)/λ)
```

`log Z` não depende da política, então sai do `argmin`. Substituindo em `G = -I(s;o|π) - E[log p(o|C)]`:

```
argmin_π G(π) = argmax_π { E[u(o) | π] + λ · I(s ; o | π) }        (★)
```

**Utilidade esperada mais informação mútua, com um câmbio `λ` entre bits e unidades de utilidade.** A pergunta
central é: quem escolhe `λ`?

- **EU** deixa `λ` livre. Se a informação não vale nada, `λ = 0` e (★) é utilidade esperada pura. Se vale, você
  calcula o EVSI — a redução esperada de perda — e não precisa de `λ`: o valor já sai em unidades de perda
  (Lindley 1956; Howard 1966).
- **EFE fixa `λ = 1`.** Não por teorema, mas porque a formulação *define* a preferência como log-probabilidade
  (`u = log p(o|C)`, em nats). A normalização de `p(o|C)` força a escala: um nat de preferência passa a valer
  exatamente um nat de informação. Isso é uma **convenção de unidades**, não uma lei da natureza — e é a única
  coisa que a EFE acrescenta a (★).

Portanto a inclusão é na direção contrária à que a intuição sugere:

```
EFE (λ = 1)  ⊂  EU com valor da informação (λ livre)
```

EFE não é um superconjunto de utilidade esperada. É o **caso particular** em que o preço da informação foi fixado
por convenção. Ela decide *menos*, porque remove o grau de liberdade que importa.

A leitura de (★), então:

- Se a ação **não muda o que você observa sobre `s`**, `I(s;o|π)` é constante entre ações e `argmin G = argmax EU`,
  para qualquer `λ`. Active inference é utilidade esperada. Ponto.
- Se a ação **muda o que você observa**, o termo extra é o valor esperado da informação — que em teoria da decisão
  se chama EVSI e tem 1956 como data.
- Se você **não sabe** `p(o|s)`, `I(s;o)` não é computável, e (★) não te dá nada além de EU.

Os próprios autores dizem isso, sem rodeio (Sajid, Da Costa, Parr & Friston 2022, resumo):

> "When removing prior outcomes preferences from expected free energy, active inference reduces to optimal Bayesian
> design, i.e., information gain maximization. Conversely, active inference reduces to Bayesian decision theory in
> the absence of ambiguity and relative risk, i.e., expected utility maximization."

Traduzindo: EFE é a **união de duas coisas conhecidas**, não uma terceira. A contribuição real do programa é
mostrar que as duas cabem numa fórmula só, com um câmbio fixo. Elegante — e não um lever novo para quem já pode
escrever as duas com o câmbio que o problema pede.

### 3.3 Onde EFE genuinamente difere de EU (e por que não salva o jazz)

Três diferenças reais, nenhuma delas útil aqui:

1. **Sensibilidade a risco.** O termo `risco = KL(q(o|π)‖p(o|C))` penaliza variância de resultado, não só
   expectativa. EU com utilidade linear não faz isso. No jazz, risco é **restrição dura**, não penalidade suave:
   `cbRisk` (`caps.go:34-40`), confirmação para `high`, `--yolo` como escape, `false_success == 0` bloqueante
   (`forge_gate.go:155`). Trocar restrição por penalidade KL seria um retrocesso de segurança.
2. **O câmbio `λ = 1`.** Ele obriga a medir preferência em nats, o que só é possível inventando uma `p(o|C)` cujo
   log seja a sua utilidade. Para o jazz, a utilidade é "verificador passou" (0/1) mais risco e custo em
   milissegundos — não uma log-probabilidade. A §4.2 mostra o preço disso: com `λ = 1`, o termo de informação
   **domina** os termos de perda (≈1,9 bits contra perdas de 1–2 unidades) e o agente pergunta quase sempre.
3. **É um bound, não a perda.** `G ≥` perda verdadeira em geral. Para o jazz, o verificador dá a perda exata
   (passou/não passou). Trocar exato por bound é perder precisão de graça.

**Veredito §3:** T2 é **REBRANDING**, e a prova é (★) mais a citação dos autores: EFE é EU com `λ = 1`. T3
(explorar × agir) não se aplica: não há política sendo aprendida em runtime; a única "exploração" possível é gastar
um comando ou uma pergunta para reduzir incerteza **antes** de uma decisão única. Isso é VOI, não RL.

---

## 4. A regra no jazz: o que EFE faz que EU+Chow não faz

### 4.1 O espaço de ação real

Por pedido, o jazz escolhe entre três coisas (não entre políticas):

```
act(m)    executar o método m do intent lido        brain.go:261-385
ask(M)    mostrar um menu M de até 5 opções         methods.go:1157 (brChoose), offers.go:56 (olMenuMax)
handoff   dizer o que falta e parar                 brain.go:391-423, rvDecide (recover.go:134)
```

O estado oculto é a intenção `i ∈ {1..50}` (`nlu/intents.json`: 50 intenções, 644 exemplos). A posterior `q(i)` é
o softmax temperado de `predict` (`nlu.go:1470-1477`) — hoje **não calibrada** (o `p` de `nlu.go:1474` é
`1/Σ exp(Δscore·β/n)`, e `calib.go` está morto: nenhum uso fora de teste). Custo e risco de cada método já estão
declarados na biblioteca: `risk` e `cost_ms` em `methods/*.json` (`git_push.run`: `high`, 30000 ms;
`git_log.repo`: `low`, 300 ms).

### 4.2 O único `p(o|s)` que o jazz pode justificar — e a regra com λ explícito

Para calcular `I(i;a)` é preciso `p(a|i)`: a probabilidade de o usuário responder `a` quando a intenção verdadeira é
`i`. O jazz não tem esse dado e não pode estimá-lo de 644 exemplos (não há registro de interações com menu). O único
modelo defensável sem dados novos é o **modelo de menu**:

```
p(a = i | i) = 1            se i ∈ M   (o usuário reconhece a opção certa quando ela está lá)
p(a = ⊥ | i) = 1            se i ∉ M   (a opção certa não está no menu: ele não pode escolhê-la)
```

`a` é determinístico dado `i`, logo `I(i;a) = H(a)`, a entropia da **resposta**:

```
p_in = Σ_{i∈M} q(i)
H(a) = - Σ_{i∈M} q_i log q_i  -  (1 - p_in) log(1 - p_in)
```

As perdas são `E[loss|act] = (1-q_top)·ℓ(top)`, `E[loss|ask] = c_ask + (1-p_in)·ℓ_hand`,
`E[loss|hand] = ℓ_hand`. Substituindo em (★):

```
ask  ⟺  (1 - q_top)·ℓ(top)  +  λ·H(a)  >  c_ask + (1 - p_in)·ℓ_hand        (◆)
```

Em `λ = 0` isso é **exatamente Chow sensível a custo** com custo de rejeição `c_ask` — a forma que o código já tem,
com um `c` só. O único termo que EFE acrescenta é `λ·H(a)`. Tudo o que segue é sobre esse termo.

### 4.3 Dois contraexemplos com número

**Caso 1 — EFE fabrica uma decisão a partir de bits que não mudam a perda.** Duas posteriores, mesmo `q_top`,
mesma `p_in`; a única diferença é que o menu de `P` tem um item a mais, carregando 0,2 bits de detalhe irrelevante:

```
P:  M = {1,2,3},  q_M = (0.40, 0.10, 0.10),  fora 0.40   →  q_top 0.40  p_in 0.60  H(a) 1.722
Q:  M = {1,2},    q_M = (0.40, 0.20),        fora 0.40   →  q_top 0.40  p_in 0.60  H(a) 1.522
```

Custos `ℓ(top) = 2`, `ℓ_hand = 1`, `c_ask = 1.2`. Lado esquerdo de (◆) sem o termo epistêmico: `1.2`. Lado direito:
`1.6` nos dois casos.

| λ | P | Q | política |
|---|---|---|---|
| 0 | 1.200 < 1.600 | 1.200 < 1.600 | **age** nos dois |
| 0,25 | 1.200+0.431 = **1.631 > 1.600** | 1.200+0.381 = 1.581 < 1.600 | P **pergunta**, Q **age** |
| 1 | 1.200+1.722 = 2.922 | 1.200+1.522 = 2.722 | **pergunta** nos dois |

Os pontos de virada são `λ_P = 0.4/1.722 = 0.232` e `λ_Q = 0.4/1.522 = 0.263`. Entre eles, **0,2 bits de
granularidade do menu decidem** — e não mudam nada na perda: a opção extra não altera a ação correta nem a chance
de acertar. Em `λ = 1`, os dois perguntam, com o termo de informação valendo mais que a perda inteira.

**Caso 2 — entropia e valor ordenam ao contrário.** Mesmo `q_top = 0.40`, menus top-2, `c_ask = 0.9`:

```
R: q = (0.40, 0.15, 0.15, 0.15, 0.15)   H_total 2.171   p_in 0.55   →  E[ask] 1.35 > E[act] 1.2  → age
S: q = (0.40, 0.40, 0.10, 0.10)         H_total 1.722   p_in 0.80   →  E[ask] 1.10 < E[act] 1.2  → pergunta
```

`R` é a posterior **mais incerta** (2,17 bits contra 1,72) e é justamente onde perguntar **não** vale a pena: a
cauda espalhada em três intenções não cabe num menu de duas, então `p_in` é 0,55 e metade do turno é desperdiçada.
Qualquer regra que pergunte "quanto de surpresa a pergunta remove" acerta o sinal errado. A regra correta usa
`p_in`, não `H`.

### 4.4 O que isso significa

1. **EFE acrescenta exatamente um termo a Chow: `λ·H(a)`.** Não é um mecanismo novo; é um termo com preço.
2. **O preço é convenção.** `λ = 1` só é "natural" se a utilidade for log-probabilidade. Em unidades de perda
   (ms, risco, tarefa falhada), `λ = 1` é 4× a 19× além do ponto em que a decisão vira, nos casos acima.
3. **Pago o preço certo, o termo deixa de ser EFE e volta a ser EVSI.** EVSI não tem `λ`: ele mede a redução de
   perda diretamente, em unidades de perda. É por isso que a literatura de decisão não precisa do conceito de
   free energy para perguntar "vale a pena perguntar?".
4. **`p_in` — a cobertura do menu — é a variável que EFE não tem e o jazz precisa.** Ela não é entropia, não é
   informação mútua, e é grátis: é a soma das probabilidades das opções mostradas.

**A tabela que responde à pergunta do dono:**

| Decisão | Chow (forma no código) | EU com custos | EFE (λ=1) | Jazz hoje |
|---|---|---|---|---|
| agir × abster | `max p ≥ 1-c` | sim | sim | `nuMinConf=0.6` (`nlu.go:45`), `brLevel0Min=0.85` (`brain.go:596`) |
| custo assimétrico por ação | não (um `c`) | **sim** | sim | **não** — mesmo limiar para `git_push` e `git_log` |
| perguntar × agir quando a resposta resolve | sim, se `c = c_ask` | sim | sim | implicitamente, via limiar |
| qual pergunta / qual menu | não | sim (com `p(o\|s)`) | sim, mas pago em bits | top-k por score, sem `p_in` (`methods.go:1157`) |
| perguntar quando o menu pode errar | não | **sim** | sim, se `λ` for calibrado | **não** |
| sondar um fato antes de agir | não | sim | sim | **não** — `brFacts` coleta conjunto fixo (`brain.go:225`) |

O jazz está no degrau de baixo. Subir é útil. **Subir é EU, não FEP.**

---

## 5. O que sobra de verdade: a única peça nova

### 5.1 A peça

`ask ⟺ c_ask + (1-p_in)·ℓ_handoff < (1-q_top)·ℓ(top)`, com `ℓ` **por método**, derivado de `risk` + `cost_ms` que
já estão na biblioteca. É (◆) com `λ = 0`: o valor da pergunta medido em unidades de perda, sem bits e sem câmbio.

O que isso tem que o código não tem:

1. **Custo assimétrico.** Hoje `git_push` (risk high, 30 s) e `git_log` (risk low, 0,3 s) passam pelo mesmo 0.6.
   Um limiar único não consegue expressar que errar push é ~30× pior que errar log (§8, P1). Isso é Chow com perda 0-1
   aplicado a um problema que não é 0-1.
2. **`p_in`, a cobertura do menu.** A única quantidade genuinamente nova, e ela é grátis: `p_in` é a soma das
   probabilidades das opções mostradas. Se o menu cobre 95% da massa, perguntar quase sempre resolve; se cobre 40%,
   perguntar é teatro. Hoje `brChoose` mostra as 5 primeiras por score e não sabe (nem pergunta) quanto da massa
   ficou de fora (`methods.go:1195-1200`).
3. **O preço da informação aparece explicitamente.** `c_ask` deixa de ser "o limiar que alguém escolheu" e vira um
   parâmetro com significado: quantas unidades de perda vale um turno do usuário.

### 5.2 O que NÃO é computável (e por que não tentar)

`I(i;a)` para uma pergunta aberta exige `p(resposta|intenção)` — um modelo do usuário. Estimar isso de 644 exemplos
é impossível; estimar com um LLM é **colocar o LLM no caminho crítico**, que é a violação da regra de ouro do
projeto. Consequência: qualquer proposta de "EFE completo" no jazz ou (a) usa o modelo de menu e vira (◆), cujo
único conteúdo além de Chow é um termo de bits com preço fixo, ou (b) embute um LLM para prever a resposta do
usuário — e aí não é offline.

A segunda ação com observação-antes-da-ação é a **sonda de fato** (headless, sem usuário). Aí `p(o|s)` é
`P(fato | estado do repo)`, que *é* estimável — mas só com dados que o jazz ainda não registra. Ver P2.

### 5.3 O gancho que já existe e ninguém lê

Duas estruturas de informação já estão no código, completas, e **sem consumidor**:

- `Capability.Effects` (`caps.go:67-77`) declara `read|write|exec|git|net|fact:<nome>`. É um modelo de **que
  informação cada ação produz**. Nenhum código lê `Effects` (só atribui: `caps.go:84-139`).
- `mhMethod.Pre` (`methods.go:42-47`) declara **que fatos o método precisa**; `mhApplicable` (`methods.go:141`) os
  checa contra o mapa de `brFacts` (`brain.go:225`).

Junto, isso é um grafo fato→ação: "esta capacidade produz `fact:env`", "este método precisa de `bin.git`". O
planejador de VOI não precisa de teoria nova; precisa **ligar o que já está declarado**. É a mesma situação de
`Intent.Alt` (`nlu.go:56`), que é escrito em três lugares (`nlu.go:982, 999`) e **nunca lido**: o menu top-k de
intenções não existe — o menu que existe é de entidades (`brChoose`).

**Veredito §5:** T4 é **LOAD-BEARING**, mas o conteúdo é Lindley 1956 + Howard 1966, não Friston. A parte que
"conecta o menu top-k ao quando perguntar" é real e é exatamente `p_in`; a parte "valor = redução de surpresa
esperada" está **errada** — valor = redução de perda esperada, e as duas divergem sempre que a perda não é uniforme
(contraexemplos §4.3).

---

## 6. Negentropia: a física, os números, e o que ela decide

### 6.1 O que a física realmente diz

- **Schrödinger (1944).** O organismo "se alimenta de entropia negativa". A afirmação correta é contábil: para
  manter entropia interna baixa, o sistema exporta entropia. É `dS_total ≥ 0` reescrito. **Restrição, não
  objetivo** — nenhuma ação é escolhida por ela.
- **Prigogine (Nicolis & Prigogine 1977).** Estruturas dissipativas se mantêm longe do equilíbrio exportando
  entropia. É uma descrição de uma classe de sistemas físicos, não um princípio de projeto de software. Não diz
  qual método rodar.
- **Brillouin (1953).** `S = S₀ - I`, e a eficiência de uma observação é `ε = ΔI/ΔS₀ ≤ 1`. Esta é a forma
  **quantitativa** da metáfora, e é a única utilizável: dá um número para "ordem por desordem exportada".
- **Landauer (1961) / Bennett (1982).** Apagar um bit custa no mínimo `kT ln 2`. O demônio de Maxwell não viola a
  segunda lei porque precisa **apagar** o registro da medição; o passo irreversível é o apagamento, não a medição.

### 6.2 O mapa exato do demônio no jazz

| Demônio | Jazz |
|---|---|
| medir o sistema | `signals.jsonl` — `sigAppend` (`signals.go:52`) registra onde o cérebro errou ou foi corrigido |
| decidir com a medição | forja offline (Fable) — fora do caminho crítico |
| apagar o registro (o passo irreversível) | **comprimir a trajetória do LLM em um asset** (método, script, calibração) |
| pagar a conta | tokens na forja, uma vez; zero no runtime |

O mapa é bom, e é o único lugar onde "negentropia" tem conteúdo de engenharia: **a forja move a exportação de
entropia do runtime para o compile-time, e amortiza.** Isso é uma afirmação mensurável (T6), não uma lei.

### 6.3 Os números (e por que "por joule" é VIAGEM)

Constantes: `kT ln 2` a 300 K = `1.380649e-23 × 300 × ln 2` = **2,871×10⁻²¹ J/bit**.

Decisão entre 50 intenções = `log₂ 50` = **5,64 bits** de ordem local.

| caminho | tempo (medido) | energia no D610 @2 W | bits no limite de Landauer | `ε = ΔI/ΔS₀` |
|---|---|---|---|---|
| 0.9 offline, `abre o README.md` | 120–139 ms | 0,26 J | 9,1×10¹⁹ | **6,2×10⁻²⁰** |
| 0.8.1 online, mesmo pedido | 14,3–21,5 s | ~36 J (só espera) | 1,3×10²² | ~4,5×10⁻²² |

O D610 gasta **~1,6×10¹⁹ vezes** o mínimo termodinâmico por decisão. `ε` é 20 ordens de grandeza abaixo de 1. Não
existe otimização de "ordem por joule" que meça algo além da fonte de alimentação: o tempo do D610 nesse caminho é
**espera de rede**, não computação, e a conta de verdade é paga no datacenter que roda o modelo.

**Portanto:** "máxima ordem por joule" é VIAGEM. A grandeza física nunca é o recurso escasso nesta máquina; ela é
~19 ordens de grandeza folgada. Otimizar o denominador errado é a definição de métrica de vaidade.

### 6.4 O que tem conteúdo: ordem por token, e a amortização

O recurso escasso é o token. Medido (`results/dell-0.9-progress.md`):

```
"abre o README.md"   0.8.1: 14,3–21,5 s · 2 requisições · 32,3k tokens
                     0.9:   0,120–0,139 s · 0 requisições · 0 tokens
"jazz eval offline"  39/39 · falso sucesso 0 · 34/39 sem modelo · perguntas 3/3 · p50 14 ms
```

`32.300 → 0` é a compressão. O modelo de amortização é o que dá sentido a "inteligência em tempo de compilação":

```
C(N) = C_forge / N + C_runtime

offline vence quando   N > C_forge / (C_llm - C_offline)
```

Com `C_llm = 32,3k tokens` por pedido e `C_offline = 0`, o ponto de equilíbrio é `N = 1` **se o asset cobre um
pedido**. Na suíte, 34 de 39 pedidos são cobertos sem modelo: o asset se paga 34 vezes por execução do eval. É
essa a afirmação que a intuição do dono está tentando fazer — e ela é verdadeira, e ela **não precisa de Friston
nem de Landauer para ser dita**.

**Veredito §6:** T5 é **VIAGEM** (números em §6.3). T6 é **LOAD-BEARING**, mas é a tese MDL que o projeto já tem
(unifying-math-offline §4, veredito 18) — reescrita em unidades de token, que é onde ela morde.

---

## 7. Mapeamento `file:line`

| Símbolo | Onde | Estado |
|---|---|---|
| `nuMinConf = 0.6` | `nlu.go:45` | limiar único para 50 intenções; ignora risco e custo |
| `Intent.Understood` | `nlu.go:61-64` | gate de Chow com `c` arbitrário |
| `Intent.Alt` | `nlu.go:56` (escrito em `982, 999`) | **morto** — nenhum leitor fora de `nlu.go` |
| `Intent.Confidence` | `nlu.go:1067` | heurística (0.9, 0.97, `0.75p`, `0.55+0.45p`, caps) |
| `predict` (softmax) | `nlu.go:1434-1478` | `p = 1/Σexp(Δ/256·β/n)`; o **vetor completo** já é calculado e descartado |
| `nuBeta = 6.0` | `nlu.go:1332` | temperatura disfarçada de constante |
| `PlattFit` / `IsotonicFit` | `calib.go:220` / `calib.go:281` | **mortos** — nenhum uso fora de teste |
| `brRunIntent` gate | `brain.go:130-151` | confirma digitação; o resto vai para `Understood` |
| `brLevel0Min = 0.85` | `brain.go:596` | segundo limiar, também único |
| `brFacts` | `brain.go:225-257` | conjunto **fixo** de fatos, coletado sempre |
| `brExecute` | `brain.go:261-424` | primeiro método aplicável; ordem = `priority` |
| `brVerify` | `brain.go:363`, `methods.go:1680` | **o objetivo real**: satisfazer o verificador |
| `mhApplicable` | `methods.go:141` | pré-condições sobre fatos |
| `brPick` | `methods.go:1132` | age sem perguntar por margem de score (`fzSlip·8`) |
| `brChoose` | `methods.go:1157-1211` | menu top-5; **não** calcula massa coberta |
| `brAskSlot` | `methods.go:1222` | pergunta aberta, sem valor calculado |
| `rvDecide` | `recover.go:134-149` | tabela de recuperação: ação por categoria × tentativa |
| `rvExplore` / `rvFactKey` | `recover.go:180-236` | "surpresa" binária, usada só para parar |
| `cbRisk` | `caps.go:34-40` | low/med/high/critical — a fonte natural de `ℓ` |
| `Capability.Effects` | `caps.go:67-77` (dados em `84-139`) | **morto** — declara `fact:<nome>`, ninguém lê |
| `methods/*.json` `risk`,`cost_ms` | ex. `git_push.json` (`high`, 30000), `git_log.json` (`low`, 300) | custo assimétrico já declarado, não usado na decisão |
| `Gate` | `forge_gate.go:143-172` | `false_success == 0` bloqueante — a restrição dura |
| `eoSummary` | `evaloffline.go:91-108` | `no_llm_rate`, `questions_asked`, `questions_needed`, `false_success` |

---

## 8. Propostas

| # | Proposta | Métrica principal | Impacto | Esforço | Risco |
|---|---|---|---|---|---|
| P1 | Gate de ação por perda esperada (`voi.go`): `act` × `ask` × `handoff` com `ℓ` por método e `p_in` | `false_success` = 0 preservado; `questions_asked/needed` ↓; cobertura ↑ | 4 | S-M | médio |
| P2 | Ledger de decisão (`signals` kind `decision`) — os dados que tornam P1 calibrável e P3 aprendível | amostras por (intent, canal, método, desfecho) | 3 | S | nulo |
| P3 | Contabilidade de negentropia no eval: bits/token, `ε` vs Landauer, amortização | `bits/verified task`, `tokens/verified task`, `ε` | 2 | S | baixo |

### P1 — Gate de ação por perda esperada

**Mecanismo.** Em `brRunIntent`, antes de `brExecute`, escolher `argmin` entre três ações:

```
E[act]  = (1 - q_top) · ℓ(m_top)          // executa o método do intent lido
E[ask]  = c_ask + (1 - p_in) · ℓ_handoff  // menu com os k melhores; se a verdade não está, cai no handoff
E[hand] = ℓ_handoff
```

`q` = posterior completa (não só o máximo). `ℓ(m) = w_risk[risk(m)] · (1 + min(cost_ms/10000, 3))`, com
`w_risk = {low:1, med:3, high:8, critical:16}`, `c_ask = 1`, `ℓ_handoff = 2`. Isso dá `ℓ(git_log) ≈ 1,03` e
`ℓ(git_push) ≈ 32` — errar push é **~30×** pior que errar log, e o limiar efetivo de confiança para push vira
`q_top > 0,97` contra `0,6` para log. `p_in` = massa das `k` opções do menu.

**Piso duro.** `voiDecide` nunca devolve `act` com `q_top < nuMinConf`, qualquer que seja a perda: `false_success = 0`
é restrição, não termo do objetivo (§3.3). O VOI decide *acima* do piso, não o substitui.

Três detalhes que fazem isso ser barato e seguro:

1. **A posterior completa já é calculada.** `predict` (`nlu.go:1470-1477`) soma `exp(Δ/256·β/n)` sobre as 50
   classes e devolve só `1/sum`. Expor o vetor custa 50 stores em `[50]float32` (200 B) e **zero transcendental
   extra** — os `exp` já foram pagos. No Dothan, ~50 `exp` ≈ 2–3 µs *(estimativa)*; o pedido mede p50 14 ms.
2. **`ℓ` sai da biblioteca, não de uma tabela autoral.** `risk` e `cost_ms` já estão em cada método
   (`methods/*.json`), e `cbRisk` já tem quatro níveis (`caps.go:34-40`). Nada novo para manter.
3. **Fallback = comportamento atual.** Sem `nlu/calib.json` (ver math-offline-brain P3) ou sem asset de custos,
   `voiDecide` devolve `act` sempre que `q_top ≥ nuMinConf`, e `ask` abaixo — idêntico ao de hoje. A flag
   `JAZZ_VOI=off` volta ao limiar único.

**Arquivos.** novo `voi.go` (~120 linhas: `voiDecide`, `voiLoss`, `voiPosterior`); `nlu.go` (`predict` devolve o
vetor via parâmetro de saída, sem alocar); `brain.go` (`brRunIntent` chama `voiDecide` no lugar de
`in.Understood()`); `methods.go` (`brChoose` recebe `p_in` e corta o menu onde a massa para de crescer);
`nlu/voi.json` (asset: `w_risk`, `c_ask`, `ℓ_handoff`).

**Custo 32-bit.** 200 B de pilha para o vetor + ~50 B de tabela; nenhuma alocação no caminho; ~3 µs. Sem `float64`
em laço quente além do que `predict` já faz. Nada de `math.Log` novo (lição de `nuTrain`: 786 mil logs → 90 ms de
CPU no D610).

**Métrica.** No eval: `false_success` continua 0 (restrição), `questions_asked/needed`, `success`. **O problema:**
a suíte tem 39 tarefas e já está em 3/3 perguntas — **não há folga para medir ganho**. O ganho tem de ser medido no
corpus cego de ~100 frases (math-offline-brain P1) com ambiguidade injetada (permutar exemplos entre intenções
vizinhas até `q_top ∈ [0.4, 0.7]`) e no replay de `signals.jsonl`. A suíte de 39 é guarda de regressão, não
instrumento de ganho. Dizer o contrário seria mentir com um número.

**Impacto 4, esforço S-M, risco médio.** Risco real: `q` não calibrada torna a comparação de perdas teatro — P1
**depende** de math-offline-brain P3/P4. Sem calibração, o `0.6` é tão arbitrário quanto o `c_ask`; a diferença é
que `c_ask` tem significado e o `0.6` não.

### P2 — Ledger de decisão

**Mecanismo.** `sigAppend(Signal{Kind:"decision", ...})` por pedido, com `Extra`:
`conf`, `channel`, `p_in`, `k`, `intent`, `method`, `outcome` (succeeded/unverified/failed/needs_input),
`questions`, `commands`, `ms`. `Signal.Extra` já é `map[string]string` (`signals.go:35`) — cabe sem mudar o schema.

**Por quê.** É o único caminho para `P(falha | método, fatos)` e para calibrar `c_ask` e `ℓ`. Sem isso, P1 é uma
regra com parâmetros escolhidos no dedo — e a §5.2 já disse que um modelo de usuário não é estimável. A
alternativa é medir, e a medição custa uma linha por pedido.

**Arquivos.** `signals.go` (nada: `Kind` e `Extra` já existem), `brain.go` (`brRunIntent` no fim), `evaloffline.go`
(agregação no `eoSummary`). **Custo:** ~1 escrita append por pedido, já sob mutex + flock (`signals.go:64-77`);
~200 B/linha. **Métrica:** número de amostras por célula (intent × método × desfecho) — pré-requisito de qualquer
VOI futuro. **Impacto 3, esforço S, risco nulo** (aditivo, fora do caminho de decisão).

### P3 — Contabilidade de negentropia no eval

**Mecanismo.** No `eoSummary` (`evaloffline.go:92-108`), três números por execução:

```
bits_per_task      = log2(50) por decisão de intent (constante; o ponto é o denominador)
tokens_per_task    = tokens pagos (0 no offline; medido no online)
eps                = bits_per_task / (joules / (kT ln2)),  joules = watts × segundos do p50
amortization       = C_forge / N + C_runtime   (N = pedidos cobertos sem modelo)
```

**Por quê.** É a maneira honesta de manter a intuição do dono: ela vira um número que **pode cair**, em vez de uma
lei que sempre se confirma. E ela expõe o fato de que `ε ≈ 6×10⁻²⁰` — o que encerra a conversa sobre joule com
dado, não com retórica.

**Arquivos.** `evaloffline.go` (3 campos em `eoSummary`), `results/` (uma linha no relatório). **Custo:** zero no
runtime; 3 divisões no relatório. **Métrica:** `bits/verified task`, `tokens/verified task`, `ε`. **Impacto 2,
esforço S, risco baixo** — impacto 2 de propósito: é contabilidade, não decisão.

---

## 9. O que não fazer

- **EFE com peso 1 na informação mútua.** Paga informação irrelevante (50 intenções, slots que não mudam a ação) e
  não tem como ser calibrado. Se for para ter termo epistêmico, ele é VOI com peso = perda, não `I(s;o)` com peso 1.
- **"Minimizar surpresa" como objetivo.** Sem preferências, o ótimo é o dark room ("não entendi"); com
  preferências, é EU com outro nome. Não existe a terceira opção que a intuição procura.
- **Modelo generativo do usuário para calcular `p(resposta|intenção)`.** Não é identificável de 644 exemplos, e
  estimá-lo com LLM põe o LLM no caminho crítico — a violação da regra de ouro.
- **POMDP / planejamento sobre políticas no runtime.** Não há distribuição sobre estados futuros; há um plano
  determinístico com ramos de recuperação (`rvDecide`). A maquinaria de EFE não tem sobre o que planejar, e o custo
  em 32-bit/2 GB é absurdo para o ganho.
- **Trocar restrição de risco por penalidade KL.** `cbRisk` + confirmação + `false_success == 0` são restrições
  duras; o termo de risco do EFE é penalidade suave. Suavizar segurança para caber numa fórmula é o pior negócio
  do documento.
- **Otimizar joule.** `ε ≈ 6×10⁻²⁰`; a grandeza nunca é escassa. Se quiser uma métrica de eficiência, é
  token/tarefa verificada.
- **Chamar `calib.go` de "free energy" e considerar o assunto resolvido.** `PlattFit` minimiza log-loss, que é
  `-log p`; isso é a *percepção*, não a *ação*. O código continua sem regra de ação sensível a custo.
- **Usar entropia da posterior como sinal de "devo perguntar".** Já provado errado em multiclasse
  (unifying-math-offline §3.1) e errado de novo no nível da ação (§4.3: entropia e valor ordenam ao contrário).
- **Menu top-k por score sem `p_in`.** Mostrar 5 opções que cobrem 40% da massa é pior que não perguntar: gasta um
  turno e ainda erra. `brChoose` hoje não sabe quanto deixou de fora.

---

## 10. Fontes

**Active inference / FEP**
- Friston, K. — *The free-energy principle: a unified brain theory?* Nature Reviews Neuroscience 11:127–138, 2010. [doi:10.1038/nrn2787](https://doi.org/10.1038/nrn2787)
- Da Costa, L., Parr, T., Sajid, N., Veselic, S., Neacsu, V., Friston, K. — *Active inference on discrete state-spaces: a synthesis.* Journal of Mathematical Psychology 99:102447, 2020. [doi:10.1016/j.jmp.2020.102447](https://doi.org/10.1016/j.jmp.2020.102447) · [arXiv:2001.07203](https://arxiv.org/abs/2001.07203)
- Sajid, N., Ball, P. J., Parr, T., Friston, K. — *Active inference: demystified and compared.* Neural Computation 33(3):674–712, 2021. [doi:10.1162/neco_a_01357](https://doi.org/10.1162/neco_a_01357) · [arXiv:1909.10863](https://arxiv.org/abs/1909.10863)
- Sajid, N., Da Costa, L., Parr, T., Friston, K. — *Active inference, Bayesian optimal design, and expected utility.* In *The Drive for Knowledge*, Cambridge UP, pp. 124–146, 2022. [doi:10.1017/9781009026949.007](https://doi.org/10.1017/9781009026949.007) · [arXiv:2110.04074](https://arxiv.org/abs/2110.04074) — **a fonte do teorema dos casos-limite (§3.2)**
- Parr, T., Pezzulo, G., Friston, K. — *Active Inference: The Free Energy Principle in Mind, Brain, and Behavior.* MIT Press, 2022. [doi:10.7551/mitpress/12441.001.0001](https://doi.org/10.7551/mitpress/12441.001.0001)

**Crítica**
- Sun, Z., Firestone, C. — *The Dark Room Problem.* Trends in Cognitive Sciences 24(5):346–348, 2020. [doi:10.1016/j.tics.2020.02.006](https://doi.org/10.1016/j.tics.2020.02.006)
- Bruineberg, J., Dołęga, K., Dewhurst, J., Baltieri, M. — *The Emperor's New Markov Blankets.* Behavioral and Brain Sciences 45:e183, 2022. [doi:10.1017/S0140525X21002351](https://doi.org/10.1017/S0140525X21002351) — a confusão entre uso epistêmico e uso metafísico do formalismo

**Decisão e valor da informação**
- Chow, C. K. — *On optimum recognition error and reject tradeoff.* IEEE Trans. Inf. Theory 16(1):41–46, 1970. [doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)
- Lindley, D. V. — *On a measure of the information provided by an experiment.* Ann. Math. Statist. 27(4):986–1005, 1956. [doi:10.1214/aoms/1177728069](https://doi.org/10.1214/aoms/1177728069) — **EVSI; o "termo epistêmico" com 70 anos**
- Howard, R. A. — *Information value theory.* IEEE Trans. Systems Science and Cybernetics SSC-2(1):22–26, 1966. [doi:10.1109/TSSC.1966.300074](https://doi.org/10.1109/TSSC.1966.300074) — informação só tem valor acoplada à consequência
- Kappen, H. J., Gómez, V., Opper, M. — *Optimal control as a graphical model inference problem.* Machine Learning 87:159–182, 2012. [doi:10.1007/s10994-012-5278-7](https://doi.org/10.1007/s10994-012-5278-7) — controle-como-inferência: a ponte que faz EFE virar EU com entropia
- Levine, S. — *Reinforcement learning and control as probabilistic inference: tutorial and review.* [arXiv:1805.00909](https://arxiv.org/abs/1805.00909), 2018.

**Termodinâmica da informação**
- Schrödinger, E. — *What is Life?* Cambridge University Press, 1944. (cap. 6: "negative entropy")
- Brillouin, L. — *The negentropy principle of information.* J. Appl. Phys. 24(9):1152–1163, 1953. [doi:10.1063/1.1721463](https://doi.org/10.1063/1.1721463) — `ε = ΔI/ΔS₀ ≤ 1`
- Landauer, R. — *Irreversibility and heat generation in the computing process.* IBM J. Res. Dev. 5(3):183–191, 1961. [doi:10.1147/rd.53.0183](https://doi.org/10.1147/rd.53.0183)
- Bennett, C. H. — *The thermodynamics of computation — a review.* Int. J. Theor. Phys. 21(12):905–940, 1982. [doi:10.1007/BF02084158](https://doi.org/10.1007/BF02084158) — o demônio paga no apagamento
- Nicolis, G., Prigogine, I. — *Self-Organization in Nonequilibrium Systems.* Wiley, 1977. ISBN 0-471-02401-5.

**Medições internas**
- `results/dell-0.9-progress.md` — 32,3k tokens → 0; 14,3–21,5 s → 120–139 ms; eval 39/39, FS 0, 34/39 sem modelo, perguntas 3/3, p50 14 ms.
