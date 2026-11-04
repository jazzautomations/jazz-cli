# A teoria da informação unifica o cérebro offline e o asset-forge?

**Teste adversarial da tese, conceito por conceito — com veredito, fonte primária e o `file:line` que decide.**

Pesquisa de 04/11/2026. Continua [math-offline-brain.md](math-offline-brain.md) (calibração, informação, suavização),
[offline-brain-research.md](offline-brain-research.md) (agentes clássicos, NLU sem transformer) e
[autonomy-control-theory.md](autonomy-control-theory.md) (controle, terminação). Não repete o pipeline NLU nem o
layout dos índices; o objeto aqui é outro: **a tese de que TEORIA DA INFORMAÇÃO + MDL/COMPRESSÃO + BUSCA são o
mesmo motor**, e o que sobra dela depois de bater no código.

Convenções: `file:line` refere-se ao `main` @ `584494d`. **Medido** = número já registrado em `results/`
(`dell-0.9-progress.md`, `low-level-performance.md`), não re-medido nesta sessão. **(estimativa)** = conta minha,
não medida. "D610" = Dell Latitude D610, Pentium M Dothan 1,7 GHz, 1 núcleo, 2 MB de L2, FSB 400 MHz, 2 GB de RAM,
`linux/386`, Go 1.27.2.

Este documento é adversarial por encomenda. Onde a ideia se sustenta, ele diz **LOAD-BEARING** e mostra o teorema.
Onde ela é só uma metáfora boa, diz **METÁFORA ÚTIL** e mostra o que a metáfora esconde. Onde ela é falsa, diz
**VIAGEM** — e, quando for o caso, "é bullshit e aqui está o porquê".

---

## 0. Placar

| # | Peça que o dono levantou | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | Entropia de Shannon → decidir agir/perguntar | **METÁFORA ÚTIL** para agir/perguntar; **LOAD-BEARING** para escolher a pergunta | A regra ótima de agir/perguntar é limiar no **máximo posterior** (Chow), não na entropia; para >2 classes as duas ordens nem coincidem. Entropia é o limite inferior de *quantas* perguntas binárias faltam (twenty questions). |
| 2 | Calibração e entropia são a mesma coisa | **LOAD-BEARING — a separação** | São funcionais de objetos diferentes: calibração é propriedade de `(p̂, acerto)`; entropia é propriedade de `p̂` sozinho. Prova por exemplo em §3.2. |
| 3 | Bayes / naive Bayes / Good-Turing / Kneser-Ney | **LOAD-BEARING** (NB como classificador) · **METÁFORA** (o "posterior" do código) | NB é a escolha certa com 644 exemplos (Ng & Jordan); mas o `p` de `nlu.go:1474` é um softmax temperado — e, no ramo de empate, **condicionado ao conjunto de empatados**, não uma posterior sobre 50 intenções. |
| 4 | Calibração (ECE, Platt, isotônico) | **LOAD-BEARING** | Mapas monotônicos não mudam o argmax; o risco é só nos dados de ajuste. `calib.go` existe e está **morto** (nenhum uso fora de teste). |
| 5 | Huffman / menu top-k como código de prefixo ótimo | **METÁFORA** | Menu com tecla única custa 1 toque para qualquer item; Huffman é vazio. A ordenação ótima é a da **busca linear** (outro teorema). |
| 6 | Ordem de testes como árvore entrópica (ID3/C4.5) | **METÁFORA ÚTIL** | ID3 é guloso; a árvore ótima é NP-completa (Hyafil & Rivest 1976). Ganho por custo é heurística, não teorema. |
| 7 | Xadrez: minimax, alpha-beta | **VIAGEM** para o adversarial · **LOAD-BEARING** para branch-and-bound + bound admissível | Não há adversário. Mas `niBound` (`nameindex.go:155`) é um lower bound admissível usado para podar — o princípio do A*, aplicado a um filtro. |
| 8 | Iterative deepening / IDA* | **METÁFORA ÚTIL** | Há orçamento e `maxDepth`; não há `f = g + h` nem limiar iterativo. |
| 9 | Afunilamento / cascata / custo-benefício | **LOAD-BEARING** (arquitetura) · **METÁFORA** (SPRT, Viola-Jones) | A ordem barato→caro é real e está no código; os teoremas exigem hipóteses que o jazz não tem. |
| 10 | Complexidade O() e constantes no i386 | **LOAD-BEARING** | 1,6 MB de tabela NB = 78% da L2; 786 mil `math.Log` por partida já foram medidos. O() esconde exatamente o que decide. |
| 11 | HTN como decomposição | **METÁFORA ÚTIL** | `mhMethod` é plano **flat** com pré-condições; não há tarefa composta nem recursão. |
| 12 | MAPE-K | **METÁFORA ÚTIL** | O laço existe; o nome não carrega matemática. |
| 13 | Lyapunov para terminação | **VIAGEM** como matemática · **LOAD-BEARING** como ranking function | O que prova terminação é uma variante bem-fundada (Floyd 1967), não `V(x)`. O orçamento já é essa variante. |
| 14 | STRIPS = proposicional | **LOAD-BEARING** para o jazz | Operadores são ground (sem variáveis). O STRIPS original tinha esquemas com variáveis; a afirmação é sobre a implementação, não sobre o artigo. |
| 15 | Regras = Horn | **METÁFORA ÚTIL** | Operadores do planner são Horn definidos; a gramática NLU com `any` (disjunção) não é Horn. |
| 16 | Validação de método = satisfatibilidade | **VIAGEM** | É schema + allowlist + avaliação ground, `O(#conds)`. Nenhum SAT é necessário ou usado. |
| 17 | Primos de Mersenne | **VIAGEM** como núcleo · **LOAD-BEARING** só como encanamento | `2^31-1` serve para Rabin-Karp; MT19937 tem período de Mersenne, não módulo. O código usa FNV-1a e máscara de potência de dois. |
| 18 | **Tese mestra:** forge = compressão MDL/Kolmogorov | **LOAD-BEARING parcial** (objetivo) · **VIAGEM** ("cada asset é um pedaço do LLM") | MDL/DreamCoder é mecanismo real para bibliotecas de programas. Não há mapa de decodificação de um asset de KB para os pesos do LLM; Kolmogorov é incomputável. |
| 19 | Rate-distortion | **METÁFORA ÚTIL** | Enquadra o trade-off; `R(D)` não é computável sem a distribuição-fonte. |

**Resumo de uma frase:** a tese está certa no *objetivo* (minimizar custo esperado sob incerteza com um orçamento de
descrição) e errada no *motor* (não existe um teorema único; são quatro papéis num mesmo problema de decisão).
Teoria da informação é a **moeda**; o motor é cascata + calibração + busca limitada + biblioteca de métodos.

---

## 1. A tese, decomposta

O dono está dizendo algo como:

> "TEORIA DA INFORMAÇÃO + MDL/COMPRESSÃO + BUSCA unificam o cérebro offline e o asset-forge."

Isso não é uma afirmação testável enquanto está nesse nível. Decomposta em cinco afirmações que **são** testáveis:

- **T1 (decisão).** A decisão agir/perguntar/entregar-ao-modelo é uma decisão ótima de teoria da informação, e a
  entropia da distribuição de intenções é a variável que a governa.
- **T2 (probabilidade).** O que o cérebro chama de `Confidence` é uma probabilidade; Bayes, suavização e calibração
  a tornam uma probabilidade *utilizável*.
- **T3 (interface).** Menu/confirmação top-k é um código de prefixo ótimo (Huffman); a ordem dos testes é uma árvore
  de decisão entrópica (ID3/C4.5).
- **T4 (busca).** A busca adversarial do xadrez (minimax, alpha-beta, aprofundamento iterativo, A*/IDA*) transfere
  para o planejador single-agent do jazz.
- **T5 (forge).** O asset-forge é compressão MDL/Kolmogorov do LLM: cada asset é "um pedaço do LLM comprimido num
  código curto offline".

A tese "mestra" é a conjunção. Vou testar cada uma e, no fim, dizer qual conjunção sobrevive.

### 1.1 O núcleo que realmente unifica (e que o dono não formulou)

Existe uma formulação correta e única, e ela não é "entropia". É **decisão Bayesiana com opção de rejeição sob
custo**:

```
x = pedido;  Y = intenção verdadeira;  A(x) = ações disponíveis (agir, perguntar, entregar ao modelo)
a* = argmin_{a ∈ A(x)}  E[ C(a, Y) | x ]
```

Com a matriz de custo `C(acertar)=0`, `C(agir errado)=1`, `C(perguntar)=c`:

```
E[C(agir em i)] = 1 − P(Y=i | x)
E[C(perguntar)] = c
agir  ⇔  max_i P(Y=i | x) ≥ 1 − c
```

Essa é a regra de Chow (1970). É o único lugar em que probabilidade, abstenção e custo se encontram de forma
**exata**. Entropia não aparece. Calibração aparece porque `P(Y=i|x)` precisa ser a probabilidade verdadeira, não
um score. Busca aparece porque `A(x)` não é dado: é gerado por um planejador sobre métodos. MDL aparece porque a
biblioteca de métodos é um asset com comprimento de descrição. **Os quatro componentes são quatro papéis no mesmo
problema de decisão — não quatro nomes para a mesma coisa.** Essa é a unificação honesta, e é mais fraca (e mais
útil) do que a que o dono está vendendo.

O resto do documento testa o que acontece quando se tenta promover a metáfora a teorema.

---

## 2. O que foi lido e o que foi medido

Código lido de ponta a ponta para esta análise: `nlu.go` (Parse, nuTrain, predict), `brain.go` (brRun, brExecute,
níveis 0/1), `methods.go` (mhMethod, mhHolds, brExecute, brPick, brVerify), `caps.go`, `nameindex.go` (niBound,
resolveAll, niTop), `backchain.go` (Plan/solve), `task.go` (tkTransitions, budget), `calib.go`, `menu.go`,
`offers.go`, `evaloffline.go`, `fuzzy.go`, `intents.go`, `offline.go`.

Medições já registradas que uso como âncora:

| Medida | Número | Onde |
|---|---|---|
| `jazz eval offline` no D610 (39 tarefas da época) | **39/39**, falso sucesso 0, 34/39 sem modelo, perguntas 3/3, p50 **14 ms**, p95 362 ms, RSS 59 MB | `results/dell-0.9-progress.md` |
| CPU de "abre o README.md" antes/depois do fix de `nuTrain` | 200 ms → **90 ms**; 786 mil `math.Log` por partida | idem |
| Tabela NB | 50 classes × 16 384 baldes × int16 = **1 638 400 B** = **78,1% da L2** | conta sobre `nlu/intents.json` + `nlu.go:1330,1395` |
| Corpus NLU | 50 intenções, **644 exemplos**, 12,88 exemplos/intenção | `nlu/intents.json` |
| Corpus de avaliação | **75 tarefas** hoje (o 39/39 foi medido antes do crescimento) | `evals/offline/tasks.json` |
| `fzDist` | 6 103 ns/op, 432 B/op, 3 allocs (M4); `resolve` de 1 token em 5k arquivos = 26 ms | `low-level-performance.md` A4/A7 |
| `init` do pacote `main` (antes) | 9,45 MB, 58 880 allocs, ~5 ms | `low-level-performance.md` A1 |

Duas descobertas de leitura que mudam o veredito e que precisam ser ditas de saída:

1. **`calib.go` é código morto.** `ECE`, `BrierScore`, `PlattFit`, `IsotonicFit`, `ReliabilityBins` não são chamados
   por nenhum caminho de produção — só por `calib_test.go`. A matemática de calibração existe; o mecanismo não está
   ligado.
2. **`backchain.go` é código morto.** `Plan`/`solve` só são chamados por `backchain_test.go`. O planejador STRIPS com
   branch-and-bound existe, está correto e **não está ligado a nenhuma intenção**. A busca que roda de verdade no
   cérebro é outra: a varredura de `nameindex` com bound admissível e a escolha linear de método por prioridade.

Isso não é detalhe: a tese "BUSCA unifica o cérebro" é, hoje, falsa como fato de implementação. A única busca viva é
a de nomes; o planner é pesquisa não integrada.

---

## 3. Conceito a conceito

### 3.1 Entropia de Shannon, Chow e a decisão de perguntar

**Veredito: METÁFORA ÚTIL.** A parte que carrega peso é a **regra de Chow**, que não é entropia. A parte "entropia da
distribuição de intenções" não existe no código e, quando existisse, não ordenaria os casos como Chow ordena.

#### O teorema

Chow (1970), para `c` classes, perda 0–1 e custo de rejeição `C_r` (normalizado), prova que a regra que minimiza o
risco é:

```
rejeitar  ⇔  max_i P(G_i | x) < 1 − t,     t = (C_r − C_c) / (C_e − C_c)
```

e que, para uma taxa de rejeição fixada, nenhuma outra regra tem erro menor. A regra é ótima **sob a distribuição
verdadeira**; com `p̂` estimado, a otimalidade se perde (Fumera, Roli & Giacinto 2000; El-Yaniv & Wiener 2010
registram isso explicitamente). Fonte: Chow, *On optimum recognition error and reject tradeoff*, IEEE Trans. Inf. Theory 16(1), 1970,
[doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406). A derivação ponto-a-ponto (minimizar
`E[ℓ]` em cada `x`, com `ℓ` = 0–1–custo de rejeição) está em El-Yaniv & Wiener, JMLR 11:1605–1641, 2010,
[PDF](https://jmlr.csail.mit.edu/papers/volume11/el-yaniv10a/el-yaniv10a.pdf); e em Charoenphakdee et al.,
ICML 2021, [PDF](http://proceedings.mlr.press/v139/charoenphakdee21a/charoenphakdee21a.pdf), Def. 1.

O ponto que derruba a versão "entropia" da tese: **Chow usa o máximo posterior, não a entropia.** Para duas classes
`p` e `1−p`, `H(p)` é estritamente monótona em `max(p,1−p)` no ramo relevante (`p ≥ 1/2`), então limiar em entropia
e limiar em `max p` induzem a **mesma família de regiões de aceitação** — são a mesma regra reparametrizada. Para
três ou mais classes, **não são**. Contraexemplo (conta fechada, verifiquei):

```
p = (0.50, 0.25, 0.25):  H = 1.500 bits,  max = 0.50
q = (0.40, 0.40, 0.20):  H = 1.522 bits,  max = 0.40
```

`q` tem **menor** máximo posterior e **maior** entropia. Uma regra de Chow com `τ = 0.45` aceita `p` e rejeita `q`;
uma regra de entropia com `τ = 1.51` rejeita `p` e aceita `q`. As duas regras discordam. O jazz tem 50 intenções:
é o regime multiclasse, não o binário. Logo, "entropia da distribuição de intenções" **não é** a variável de decisão
de Chow; é uma quantidade diferente que, por acidente, coincide no caso binário.

**O que a entropia carrega de verdade — e é outro problema.** Para *escolher a próxima pergunta* (twenty
questions), a entropia é o limite inferior do número esperado de perguntas binárias: `H(Y) ≤ E[n] < H(Y) + 1` para
um código de prefixo ótimo (Cover & Thomas, cap. 5, Teorema 5.8.1). Isso é LOAD-BEARING — mas para o **design do
menu/pergunta**, não para a decisão de *se* perguntar. São dois problemas distintos, e o dono está misturando os
dois.

#### O que o código faz

`nlu.go:45` fixa `nuMinConf = 0.6`; `brain.go:596` fixa `brLevel0Min = 0.85`. Esses são os `1 − t` de Chow, mas
**escolhidos à mão**, não derivados de custo. `Understood()` (`nlu.go:62-64`) é literalmente "nome != none e
confiança ≥ 0.6" — a forma da regra de Chow com um `t` arbitrário.

E a "distribuição" não é, em geral, uma distribuição sobre as 50 intenções. Em `predict` (`nlu.go:1434-1478`), o
softmax é calculado **só sobre `among`**:

```go
for ci := range nb.classes {
    if allowed(ci) {
        sum += math.Exp(float64(scores[ci]-scores[best]) / nuScale * nuBeta / float64(n))
    }
}
return nb.classes[best], 1 / sum
```

Quando `among != nil` — o ramo de empate, `classify(canon, tied)` em `nlu.go:993` — o `p` devolvido é a massa
**condicionada ao conjunto de empatados**, não a posterior global. Quando `among == nil` (sem regra, e o ramo de
regra única), ele é sobre todas as classes, mas ainda temperado: a temperatura é `nuBeta / n` (`nlu.go:1474`), com
`nuBeta = 6.0` (`nlu.go:1332`) e `n` = número de n-gramas do pedido. Dividir por `n` normaliza a evidência *por
n-grama*; é uma escolha de design, não uma probabilidade — e dilui evidência concentrada: um único n-grama forte num
pedido longo contribui só `1/n` para a média. Pior, a confiança final não é nem função de `p` em todos os
ramos:

- **regra única** (`nlu.go:971-980`): `conf = 0.9` fixo; `0.97` se o NB concorda; `0.7` se o NB veta com
  `p > 0.9`. O `p` só decide o veto; a confiança é uma constante.
- **sem regra** (`nlu.go:966-969`): `conf = 0.75·p` (p sobre todas as classes).
- **empate** (`nlu.go:993-995`): `conf = 0.55 + 0.45·p` (p condicionado aos empatados).
- **cap de typo** (`nlu.go:1053`): `conf = min(conf, 0.5)`; **nome não-plausível** e **resíduo** (`nlu.go:1059,1064`):
  `conf *= 0.6`.

Ou seja:

- a "entropia da distribuição de intenções" não está definida no código — em um ramo o `p` é condicionado, no outro
  é temperado, e num terceiro a confiança nem usa `p`;
- a "confiança" é um remendo de constantes e mapas afins, não uma posterior;
- a forma Chow está lá; o conteúdo probabilístico, não.

#### O que isso significa para a decisão agir/perguntar

A decisão correta é: **agir se o custo esperado de agir for menor que o custo de perguntar**. Isso exige
`P(acertar | agir)`, que é calibração, não entropia. Um modelo perfeitamente calibrado pode ter entropia alta
(ambiguidade real) e ainda assim valer a pena agir; um modelo overconfident pode ter entropia baixa e errar. A
entropia mede a *forma* da distribuição; a calibração mede se a *magnitude* dela é verdadeira. São coisas separadas
— §3.2 prova.

#### Onde a ideia se sustenta

A parte LOAD-BEARING é a **curva risco–cobertura** (El-Yaniv & Wiener 2010): varrer o limiar, medir erro entre os
aceitos × cobertura, escolher o ponto sob restrições. O tratamento geral dos modelos de custo/abstenção está em
Franc, Průša & Voráček 2023. O `jazz eval offline` já mede exatamente isso, com nomes
diferentes: `no_llm_rate` (cobertura), `false_success` (risco), `questions_asked`/`questions_needed` (custo de
perguntar) — `evaloffline.go:94-101`. O que falta é (a) o `p` ser calibrado para que o limiar signifique algo e
(b) o limiar ser escolhido na curva, não em `0.6`/`0.85`.

**Conclusão da peça:** troque "entropia" por "máximo posterior calibrado" e a ideia fica correta. Mantenha
"entropia" e você tem uma metáfora que, em multiclasse, aponta para a regra errada.

---

### 3.2 Calibração e entropia são coisas separadas — prova

**Veredito: LOAD-BEARING (a separação).** A pergunta do dono sugere que calibração e entropia poderiam ser o mesmo
fenômeno ("incerteza da distribuição"). Não são. A prova é curta e não precisa de citação.

**Definições.** Seja `p̂(x) ∈ Δ^{C−1}` o vetor de scores e `Y` a classe verdadeira.

- **Calibração (top-label/reliability):** para todo `p`, `P(Y = argmax p̂ | p̂ = p) = max p̂`. É uma propriedade do par
  `(p̂, acerto)`.
- **Entropia:** `H(p̂) = −Σ p̂_i log p̂_i`. É uma propriedade de `p̂` sozinho.

**Prova de que uma não determina a outra.** Fixe 100 pedidos e o mesmo vetor `p̂ = (0.9, 0.1)` para todos. A entropia
é `H = 0.469` bits, constante. Se 90 acertam, o modelo é calibrado; se 60 acertam, não é. **Mesma entropia, calibração
diferente.** Reciprocamente, um modelo calibrado pode emitir `p̂ = (1,0)` em pedidos fáceis (`H = 0`) e
`p̂ = (0.5,0.5)` em pedidos ambíguos (`H = 1`), mantendo `P(acerto|p̂) = max p̂` em cada nível. **Calibração perfeita,
entropias diferentes.** Logo, calibração e entropia são ortogonais: nenhuma implica a outra. E a calibração
top-label é, ela própria, incompleta: o *grouping loss* mostra que dois inputs com o mesmo score podem ter taxas de
acerto diferentes (Perez-Lebel, Le Morvan & Varoquaux, ICLR 2023,
[arXiv:2210.16315](https://arxiv.org/abs/2210.16315)). Ou seja: nem entropia, nem calibração sozinhas esgotam a
incerteza; para decidir, você precisa da calibração (no mínimo) e de features de contexto que separem os grupos.

**Por que importa para o jazz.** A decisão de agir precisa de `P(acertar)`. A entropia não dá isso. A calibração dá.
É por isso que `calib.go` (ECE em `calib.go:164`, Brier em `:198`, Platt em `:220`, isotônico em `:281`) é a peça
certa — e é por isso que ela estar **desligada** é o buraco mais caro do cérebro offline.

**O que é teorema e o que é hiperparâmetro na calibração:**

| Peça | Status | Fonte |
|---|---|---|
| ECE/MCE medem o gap; Brier é *proper scoring rule* | **definição/medida** (não teorema) | Naeini, Cooper & Hauskrecht, AAAI 2015; Guo et al., ICML 2017, [arXiv:1706.04599](https://arxiv.org/abs/1706.04599) |
| Um mapa estritamente crescente preserva o argmax | **teorema trivial** (se `f` cresce, `argmax f∘p = argmax p`) | decorrência de definição; é a base de Platt/isotônico |
| Platt (sigmoide, 2 params) é a correção certa | **modelo**, não teorema | Platt 1999; Kull, Silva Filho & Flach, AISTATS 2017, [PMLR v54](https://proceedings.mlr.press/v54/kull17a.html) |
| Isotônico (PAV) é ótimo sob monotonicidade | **teorema** (PAV minimiza quadrático sob ordem) | Ayer et al. 1955; Zadrozny & Elkan, KDD 2002, [doi:10.1145/775047.775151](https://doi.org/10.1145/775047.775151) |
| Bins de largura fixa com `n < 10³` | **enviesado**; usar quantis | Vaicenavicius et al., AISTATS 2019, [PMLR v89](https://proceedings.mlr.press/v89/vaicenavicius19a.html) |
| O `p` do NB é calibrado | **falso**; NB é o clássico mal calibrado | Niculescu-Mizil & Caruana, ICML 2005, [doi:10.1145/1102351.1102430](https://doi.org/10.1145/1102351.1102430) |

**Onde a calibração pode ser grátis e onde ela mente.** Como todo mapa monotônico preserva o argmax, calibrar não
muda a *classificação* — muda a *decisão de agir*. O risco inteiro está nos dados de ajuste: com 75 tarefas e ~644
exemplos, um isotônico de 16 nós overfitta. A regra prática: Platt/beta com poucos parâmetros até ~50 pontos por
canal; isotônico só acima disso; sempre LOO e faixa de guarda. Isso já está em `math-offline-brain.md` P3, e está
certo.

---

### 3.3 Bayes, naive Bayes, Good-Turing, Kneser-Ney: o que é teorema e o que é hiperparâmetro

**Veredito: LOAD-BEARING para o classificador; METÁFORA para o "posterior".**

#### O que é teorema

1. **NB pode ser ótimo sob perda 0–1 mesmo com independência falsa.** Domingos & Pazzani, *On the Optimality of the
   Simple Bayesian Classifier under Zero-One Loss*, Machine Learning 29:103–130, 1997,
   [doi:10.1023/A:1007413511361](https://doi.org/10.1023/A:1007413511361). A independência condicional distorce a
   *magnitude* da posterior, não necessariamente o *argmax*. Isso justifica usar NB para decidir, não para
   probabilizar.
2. **Com poucos dados, o gerativo converge mais rápido.** Ng & Jordan, NeurIPS 2001,
   [PDF](https://proceedings.neurips.cc/paper_files/paper/2001/file/7b7a53e239400a13bd6be6c91c4f6c4e-Paper.pdf):
   erro assintótico do gerativo é maior, mas ele chega lá com `O(log n)` exemplos, contra `O(n)` do discriminativo.
   Com 12,9 exemplos por intenção, NB é a escolha certa — não por moda, por taxa de convergência.
3. **KT/Jeffreys (`α = 1/2`) é minimax para predição sequencial de fonte i.i.d.** Krichevsky & Trofimov, IEEE TIT
   27(2):199–207, 1981, [doi:10.1109/TIT.1981.1056331](https://doi.org/10.1109/TIT.1981.1056331). A redundância é
   `(m−1)/2 · log n + O(1)` nats para alfabeto de tamanho `m` (a forma exata está em
   [Hanqing Li, universal coding notes](https://lihanqing1997.github.io/notes/information-theory/universal-source-coding-minimax-redundancy/)).
   **Mas atenção:** esse teorema é sobre a distribuição dos *baldes*, não sobre `P(intenção | texto)`. Ele justifica
   `α = 1/2` como default; não justifica a confiança do classificador.

#### O que é hiperparâmetro

- `α = 0.5` em `nlu.go:1397` é uma escolha defensável, não uma lei. O valor certo se escolhe por NLL held-out/LOO —
  e `nuTrain(skip)` (`nlu.go:1363`) já aceita o predicado de exclusão, então o mecanismo de LOO existe.
- `nuBeta = 6.0` (`nlu.go:1332`) é temperatura. Não há teorema que diga 6. É o parâmetro mais arbitrário do
  classificador, e o mais influente na confiança.
- `nuBuckets = 1<<14` (`nlu.go:1330`) é tamanho de tabela, não estatística. 16 384 baldes para 644 exemplos é
  sobre-parametrização maciça; funciona porque o NB é gerativo e suavizado, não porque 16k seja o número certo.

#### Good-Turing e Kneser-Ney: onde entram de verdade

- **Good-Turing** estima a **massa do não-visto**: `M0 = N1 / N` (fração de tipos que apareceram uma vez). É um
  *diagnóstico*, não um classificador. No jazz, serve para responder: "quanto da distribuição de n-gramas desta
  intenção eu nunca vi?" Se `N1/N` é alto, a likelihood dessa classe em entrada OOV é chute. Fonte: Good 1953,
  Biometrika 40(3–4):237–264, [doi:10.1093/biomet/40.3-4.237](https://doi.org/10.1093/biomet/40.3-4.237); Gale &
  Sampson, J. Quantitative Linguistics 2(3), 1995, [doi:10.1080/09296179508590051](https://doi.org/10.1080/09296179508590051).
- **Kneser-Ney** é suavização para **modelos de linguagem n-gram com backoff**: desconto absoluto + distribuição de
  continuação. Kneser & Ney, ICASSP 1995,
  [PDF](https://www-i6.informatik.rwth-aachen.de/publications/download/951/KneserReinhardNeyHermann--ImprovedBacking-offforM-gramLanguageModeling--1995.pdf).
  O NB do jazz é um produto de probabilidades por balde, não um LM sequencial com hierarquia de ordens. KN **não é
  drop-in**: você teria que trocar o modelo por um LM de caracteres interpolado por classe, com mais estado e mais
  custo. Pode ser melhor; não é a mesma coisa. Chamar isso de "Kneser-Ney no NB" seria confundir modelos. A comparação
empírica das suavizações (incluindo KN e GT) está em Chen & Goodman 1999.

#### O erro específico do código: `p` não é posterior

`predict` devolve `1/sum` de um softmax sobre `(s_c − s_best)/scale · β/n`, restrito a `among`. Três distorções
acumuladas:

1. **Condicionamento:** só as classes permitidas entram na soma. Dois pedidos com o mesmo texto podem ter `p`
   diferentes se o conjunto de empate for diferente.
2. **Temperatura e escala misturadas:** `β = 6` é arbitrário; e dividir a diferença de scores por `n` mistura duas
   escalas — o prior (`nb.prior`, que não cresce com `n`) domina em pedidos curtos e é diluído em pedidos longos. O
   resultado é uma média de log-verossimilhanças com um prior que desaparece, não uma posterior.
3. **Escala inteira:** `nuScale = 256` quantiza log-probabilidades em 1/256 nat; a soma em `float64` é exata o
   suficiente, mas o `int16` satura em `−32000` (`nlu.go:1405`), o que achata likelihoods muito baixas.

Nada disso impede o **argmax** de funcionar (é o que o eval mostra: 39/39). Impede o `p` de ser uma probabilidade.
É exatamente por isso que a peça de calibração é necessária — e por isso que "Bayes" não pode ser invocado como
justificativa do número que sai.

**Conclusão da peça:** NB é LOAD-BEARING como classificador; a palavra "posterior" no código é metáfora. GT é
diagnóstico útil; KN é outro modelo. `α` e `β` são hiperparâmetros, não teoremas.

---

### 3.4 Huffman, menu top-k e ordem de testes

**Veredito: METÁFORA.** A intuição "minimizar teclas esperadas" é boa; Huffman não é o teorema que a realiza neste
caso. A ordenação ótima de um menu linear é outro resultado.

#### O teorema de Huffman, exatamente

Para uma distribuição conhecida `p` sobre símbolos e um alfabeto `D`-ário com **custos de letra iguais**, o código de
Huffman é ótimo entre os códigos de prefixo, e

```
H_D(X) ≤ L_Huffman < H_D(X) + 1
```

Prova por indução com troca/trim (Cover & Thomas, *Elements of Information Theory*, 2ª ed., Teorema 5.8.1; o
enunciado e a prova estão no capítulo 5,
[PDF](https://ftp.esat.kuleuven.be/pub/pub/SISTA/decock/voor_xander/referenties/Cover&Thomas/5.pdf)). Fonte
original: Huffman, *A Method for the Construction of Minimum-Redundancy Codes*, Proc. IRE 40(9):1098–1101, 1952,
[doi:10.1109/JRPROC.1952.273898](https://doi.org/10.1109/JRPROC.1952.273898).

#### Por que o menu do jazz não é um código de Huffman

O menu (`menu.go:25-33`) mostra `k` itens e aceita: dígito (`1..k`), letra de atalho (`Key`/`Keys`), setas + Enter.
Para qualquer item, o usuário que conhece o atalho gasta **1 toque**. Um código de prefixo com todos os comprimentos
iguais a 1 é o código de comprimento fixo; Huffman, com custos iguais, atribui comprimento 1 a todos e **não tem o
que otimizar**. A ordem dos itens não muda o custo de seleção; muda o custo de *leitura/varredura*, que é outro
modelo.

Se o usuário navega com setas, o custo do item na posição `i` é `i` comparações, e

```
E[custo] = Σ_i i · p_i
```

é minimizado ordenando `p` decrescente. Esse é o **teorema da ordenação estática ótima da busca linear** — não
Huffman. Está em Knuth, TAOCP Vol. 3, §6.1; e é a base da literatura de self-organizing search (Bitner, *On
self-organizing sequential search heuristics*, CACM 1979,
[doi:10.1145/359997.360000](https://doi.org/10.1145/359997.360000), que enuncia: "cost is minimized when records are
in order of decreasing probability"). A fonte didática confirma a mesma fórmula
([OpenDSA](https://opendsa-server.cs.vt.edu/ODSA/Books/cnu/cpsc270/spring-2025/CNUCPSC270SP2025/html/SelfOrg.html)).

E `offers.go:158` **não** ordena por frequência: monta na ordem fixa "arquivos mencionados → erro → termo de busca →
testes → git → resumo". Se o objetivo é minimizar toques, a ordem certa é por frequência observada. Se o objetivo é
minimizar tempo de leitura, é outra métrica (e não é medida hoje).

Se o menu fosse **hierárquico** (categoria → item), aí sim seria uma árvore de decisão, e Huffman/ID3 teriam algo a
dizer — mas a árvore ótima é NP-completa (Hyafil & Rivest, *Constructing optimal binary decision trees is
NP-complete*, Inf. Process. Lett. 5(1):15–17, 1976,
[doi:10.1016/0020-0190(76)90095-8](https://doi.org/10.1016/0020-0190(76)90095-8)), e ID3 é guloso. Além disso, se os
custos das teclas diferem (letra vs seta vs modificador), o problema deixa de ser Huffman: a versão com custos de
letra desiguais não tem algoritmo polinomial conhecido e não se sabe se é NP-hard; há PTAS (Golin, Kenyon & Young,
STOC 2002, [PDF](https://cs.ucr.edu/~neal/publication/Golin02Huffman.pdf)) e DP para custos inteiros (Golin & Rote,
IEEE TIT 1998, [PDF](https://cse.hkust.edu.hk/~golin/pubs/Huffman2.pdf)).

#### Ordem de testes como árvore entrópica

ID3 escolhe o teste que maximiza o **ganho de informação** `I(p,n) − E(A)` (Quinlan, *Induction of Decision Trees*,
Machine Learning 1(1):81–106, 1986,
[doi:10.1007/BF00116251](https://doi.org/10.1007/BF00116251)). Isso é **guloso**, não ótimo, e ignora o custo do
teste. Variantes cost-sensitive usam `ganho/custo` (Norton 1989; Turney, *Cost-Sensitive Classification*, JAIR
3:369–409, 1995, [jair.org](https://jair.org/index.php/jair/article/view/10129); Davis et al., ECML 2006,
[PDF](https://www.cs.utexas.edu/~rossbach/pubs/davis-ecml06.pdf)). O pipeline do jazz é ordenado à mão
barato→caro — regex de `intents.go` → regras/Aho-Corasick → NB → resolução de nome → execução. Essa ordem é a
**forma** certa; chamá-la de "árvore entrópica" não acrescenta um teorema.

**Conclusão da peça:** Huffman no menu é metáfora (e, com tecla única, vazia). A ordenação por frequência é real mas
é a da busca linear. A árvore entrópica é uma heurística gulosa; o ótimo é NP-hard. Nada disso é o "código de
prefixo ótimo" que o dono imagina.

---

### 3.5 Xadrez, minimax, alpha-beta: o que transfere e o que não

**Veredito: VIAGEM para o adversarial; LOAD-BEARING para branch-and-bound + bound admissível.** O dono está metade
certo, e a metade certa é a que já está no código.

#### O que não transfere, e por quê

Minimax e alpha-beta são definidos sobre uma **árvore de jogo** com dois jogadores alternando `max` e `min`. A
correção e a "otimalidade" do alpha-beta (Knuth & Moore, *An analysis of alpha-beta pruning*, Artificial
Intelligence 6(4):293–326, 1975,
[doi:10.1016/0004-3702(75)90019-3](https://doi.org/10.1016/0004-3702(75)90019-3)) são provadas **para essa
estrutura**: o valor minimax é preservado, e a poda examina a árvore mínima sob uma ordenação. No jazz **não há
adversário**. O mundo não escolhe jogadas contra você; ele é estocástico/desconhecido. Aplicar alpha-beta exigiria
inventar um `min` — e o resultado seria um planejador pessimista que o eval não recompensa. Isso é VIAGEM.

O único adversário plausível é a **incerteza do ambiente** (binário ausente, rede caída, permissão negada). Modelar
isso como adversário é planejamento robusto/minimax regret — uma escolha de design legítima, mas **não está no
código** e não é o que o xadrez faz. Não confunda "o mundo pode falhar" com "o mundo joga contra".

#### O que transfere, e já está no código

1. **Branch-and-bound.** `backchain.go:129-200` explora candidatos ordenados por custo e corta quando
   `found && op.Cost >= bestCost` (`backchain.go:151-156`). Isso é branch-and-bound single-agent, o primo honesto do
   alpha-beta sem o `min`. **Mas é código morto:** só `backchain_test.go` chama `Plan`.
2. **Bound admissível (A*).** Hart, Nilsson & Raphael, *A Formal Basis for the Heuristic Determination of Minimum
   Cost Paths*, IEEE Trans. Syst. Sci. Cybern. 4(2):100–107, 1968,
   [doi:10.1109/TSSC.1968.300136](https://doi.org/10.1109/TSSC.1968.300136): com `h` admissível (nunca superestima),
   A* é ótimo e expande não mais nós que qualquer outro algoritmo com a mesma informação. `niBound`
   (`nameindex.go:155-168`) é um **lower bound provado** da distância de edição, derivado de diferença de comprimento
   e do "letter bag" (bits ligados); o comentário diz "it never exceeds the real distance" e há teste
   (`TestNiBoundIsLowerBound`). Ele é usado para **não** chamar `fzDist` em `nameindex.go:561,565,637,809,874`. Isso
   é poda por bound admissível — o mesmo princípio do A*, aplicado a um filtro, não a uma fila de prioridade — e é o
melhor exemplo de "busca" que o cérebro tem.
3. **Aprofundamento iterativo.** Korf, *Depth-first iterative-deepening: An optimal admissible tree search*,
   Artificial Intelligence 27(1):97–109, 1985,
   [doi:10.1016/0004-3702(85)90084-0](https://doi.org/10.1016/0004-3702(85)90084-0). IDA* usa limiares em
   `f = g + h`. O jazz tem `maxDepth` e orçamento (`Budget{Cmds:8, Seconds:600}`, `task.go:148`), mas não tem `h`
   sobre planos nem limiar iterativo. É **METÁFORA ÚTIL**: a ideia de escalar profundidade/orçamento existe; o
   algoritmo, não.

#### O que o dono deveria concluir

"Busca une o cérebro" é verdade no sentido estreito e útil: **toda decisão do cérebro é uma busca com poda sob
custo**. A instância viva é `niBound`; a instância morta é `backchain`. O que **não** une é o xadrez adversarial. Se
o dono quer investir em busca, o retorno está em (a) ligar o planner ou admitir que ele não paga o custo, e (b)
generalizar o bound admissível para o resolver de nomes/símbolos — não em alpha-beta.

---

### 3.6 Afunilamento, cascata e "informação por unidade de custo"

**Veredito: LOAD-BEARING como arquitetura; METÁFORA para os teoremas invocados (Viola-Jones, SPRT); heurística para
"ganho por custo".**

#### A cascata é real

O caminho de um pedido offline é:

```
intents.go (regex full-match)  →  nlu.Parse (regras + Aho-Corasick)  →  NB (só empate/sem regra)
→  nameindex (resolução de nome, bound admissível)  →  método (caps + verify)
```

Cada estágio é mais caro que o anterior, e o barato pode encerrar o caso. Isso é **coarse-to-fine com early exit** —
a forma da cascata de Viola & Jones, *Rapid object detection using a boosted cascade of simple features*, CVPR 2001,
[doi:10.1109/CVPR.2001.990517](https://doi.org/10.1109/CVPR.2001.990517). A fonte descreve exatamente o princípio:
"each window must pass every stage and most windows are rejected by the first one or two"; o objetivo é minimizar o
número esperado de features avaliadas. O `brRun`/`brExecute` faz isso. **LOAD-BEARING como arquitetura.**

#### Onde a analogia quebra

1. **Os estágios do Viola-Jones são aprendidos**, com limiares por estágio ajustados para uma taxa de falso
   positivo alvo. Os limiares do jazz são constantes à mão: `0.6` (`nlu.go:45`), `0.85` (`brain.go:596`), margem
   `fzSlip*8` (`methods.go:1151`), `fzSlip` (`nameindex.go:716`). A cascata é a forma; o *treino* da cascata não
   existe.
2. **SPRT não se aplica.** Wald (1945) e Wald & Wolfowitz (1948),
   [doi:10.1214/aoms/1177730197](https://doi.org/10.1214/aoms/1177730197), provam que o SPRT minimiza o número
   esperado de observações **entre testes com as mesmas probabilidades de erro**, para **duas hipóteses simples** com
   observações **i.i.d.** e custo por observação. O jazz tem 50 hipóteses, estágios que não são razões de
   verossimilhança e observações que não são i.i.d. Invocar SPRT aqui é decoração.
3. **"Informação ganha por unidade de custo" é heurística.** Não há teorema de otimalidade para árvores/cascatas
   cost-sensitive; a árvore ótima já é NP-hard sem custo. `ganho/custo` (Norton, Turney) é um critério guloso, e é
   útil como critério — não como prova.

#### O erro de custo concreto (e mensurável)

`nlu.Parse` chama `m.classify` **mesmo quando uma única regra já decidiu** (`nlu.go:974`), só para (a) subir a
confiança de `0.9` para `0.97` se o NB concorda, ou (b) derrubar para `0.7` se o NB diz "none" com `p > 0.9`. O
custo disso é a tabela de 1,6 MB e o `nuTrain` inteiro (786 mil `math.Log` antes do fix; 90 ms de CPU depois). O
benefício decisório:

- `0.9 → 0.97`: **irrelevante** para `nuMinConf = 0.6` e para `brLevel0Min = 0.85` (ambos já passam).
- `0.9 → 0.7`: **relevante** para `brLevel0Min = 0.85` (veta o nível 0) e irrelevante para `0.6`.

Ou seja: o estágio caro roda para produzir um veto que só importa no caminho online de nível 0, e o resto do tempo
produz um número que não muda nenhuma decisão. Isso é a definição de **baixo ganho por unidade de custo**. A
correção é barata: NB preguiçoso (só treina em empate/sem regra) + um veto barato (fração de n-gramas OOV, resíduo)
para o caso de uma regra. Métrica: mesmas decisões nas 75 tarefas, CPU p50 menor. Proposta P4 adiante.

**Conclusão da peça:** a cascata é a arquitetura certa e está no código. Os teoremas que o dono associa a ela não
se aplicam. O ganho real não está em "informação por custo" como slogan, mas em **medir** quais estágios mudam
decisões e desligar os que não mudam.

---

### 3.7 Complexidade, constantes e o i386

**Veredito: LOAD-BEARING.** Nesta máquina, `O()` é o começo da conversa, não o fim.

#### Os números que importam

- **32 bits:** `int` é 32-bit; multiplicações de hash estouram de propósito (`uint32` em `nuNgrams`,
  `nlu.go:1344-1361`). Qualquer rolling hash precisa de `uint64` para o produto — ver §3.9.
- **L2 = 2 MB.** A tabela NB é `50 × 16384 × 2 B = 1 638 400 B` (`nlu.go:1395`), **78,1% da L2**. Em `predict`, para
  cada n-grama o código toca 50 linhas com passo de 32 KB (`nb.table[ci*nuBuckets+b]`), o que mapeia todas no mesmo
  conjunto da L1 (32 KB, 8 vias) e gera conflito. O custo por n-grama é ~50 acessos, não 50 operações de ALU.
- **Sem POPCNT.** `niBound` usa `bits.OnesCount32` (`nameindex.go:163`); no Pentium M (SSE2, sem SSE4.2) o Go usa a
  implementação software. É barato, mas não é uma instrução.
- **`math.Log` é software.** O fix de `nuTrain` (um log por classe + um por balde ocupado, `nlu.go:1400-1420`)
  cortou 786 mil logs por partida e 200 ms → 90 ms de CPU. Esse é o tipo de constante que `O(n)` não vê.
- **Disco.** HDD 2,5" com ~10 ms de seek: qualquer índice que faça `stat` por arquivo paga isso. `nameindex` já
  evita `getdents` repetido com o índice persistido.

#### O que isso implica para as propostas

Uma proposta "O(log n)" que toque 1,6 MB por pedido pode perder para uma "O(n)" que caiba em 32 KB. No Dell, a
métrica que decide é **p50/p95 e RSS**, não a classe assintótica. O `evaloffline.go:94-101` já mede p50/p95/RSS;
use isso como critério de aceitação, não como relatório.

#### HTN, MAPE-K, Lyapunov

- **HTN:** `mhMethod` (`methods.go:60-79`) é `{name, intent, priority, pre, steps, verify, needs, cmds, risk,
  cost_ms}`. Os `steps` chamam capacidades (`caps.go:84-105`); **não há referência a outra tarefa composta**, não há
  recursão. Isso não é HTN no sentido SHOP2/Pyhop; é uma biblioteca de planos flat com pré-condições, escolhida por
  prioridade (`brain.go:261-340`). Os resultados de complexidade de HTN (Erol, Hendler & Nau, *Complexity results for
  HTN planning*, Ann. Math. Artif. Intell. 18:69–93, 1996,
  [doi:10.1007/BF02136175](https://doi.org/10.1007/BF02136175); undecidável em geral, NP-completo para redes
  acíclicas sem variáveis) **não se aplicam** porque a estrutura é acíclica por construção. O que vale é a disciplina
  de pré/pós/verificador, não a teoria. **METÁFORA ÚTIL.**
- **MAPE-K:** o laço M/A/P/E/K existe (M: `envprobe`/`obs`; A: `Parse`/`rvClassify`; P: methods; E: caps/shell; K:
  `task.Facts` + sessão). Kephart & Chess, *The vision of autonomic computing*, Computer 36(1):41–50, 2003,
  [doi:10.1109/MC.2003.1160055](https://doi.org/10.1109/MC.2003.1160055). É arquitetura real; o nome não carrega
  matemática. **METÁFORA ÚTIL.**
- **Lyapunov:** um `V(x)` de Lyapunov é para sistema dinâmico com métrica; `V` decresce ao longo das trajetórias. O
  laço do jazz é de eventos discretos. O objeto rigoroso de terminação é uma **ranking function** para um conjunto
  bem-fundado (Floyd, *Assigning meanings to programs*, 1967,
  [PDF](https://www.cse.chalmers.se/edu/year/2017/course/TDA384_LP1/files/lectures/additional-material/AssigningMeanings1967.pdf)):
  uma função que decresce estritamente a cada passo e é limitada inferiormente. O jazz já tem uma: o orçamento
  restante (`Cmds:8`, `Seconds:600`, `task.go:148`) e `maxDepth`; ambos decrescem e têm piso. Isso **prova**
  terminação; "Lyapunov" não acrescenta nada e ofusca o argumento. **VIAGEM como matemática; a ranking function é
  LOAD-BEARING e já está lá.**

---

### 3.8 Lógica: STRIPS, Horn e "validação = SAT"

**Veredito: STRIPS proposicional LOAD-BEARING (para o jazz); Horn METÁFORA ÚTIL; validação = SAT VIAGEM.**

- **STRIPS = proposicional.** Fikes & Nilsson, *STRIPS: A new approach to the application of theorem proving to
  problem solving*, Artificial Intelligence 2(3–4):189–208, 1971,
  [doi:10.1016/0004-3702(71)90010-5](https://doi.org/10.1016/0004-3702(71)90010-5). O STRIPS original usa
  **esquemas com variáveis e unificação**; "propositional STRIPS" é a versão *ground*. O `Operator` do jazz
  (`backchain.go:23-30`) tem `Pre/Add/Del []string` — fatos ground, sem variáveis. Então a afirmação é verdadeira
  **sobre a implementação**, não sobre o artigo. Bylander, *The computational complexity of propositional STRIPS
  planning*, Artificial Intelligence 69(1–2):165–204, 1994,
  [doi:10.1016/0004-3702(94)90081-7](https://doi.org/10.1016/0004-3702(94)90081-7): plan existence é PSPACE-completo
  em geral, mesmo com restrições severas; tratável com pré-condições positivas e um pós-condição. O planner do jazz
  é limitado por `maxDepth` e custo, então o pior caso não morde — mas ele é código morto (§2).
- **Regras = Horn.** Cláusula de Horn: no máximo um literal positivo. Os operadores do planner são implicações
  definidas (`Pre ∧ … → Add`), ou seja, Horn. A gramática NLU (`nuRule{All, Any, None}`) tem **disjunção** (`Any`) e
  negação (`None`); com `Any` não é Horn. Mas ela é **avaliada diretamente** (`rules`, `nlu.go:838`), não por
  resolução. Então "Horn" descreve o planner, não o NLU. Dowling & Gallier, *Linear-time algorithms for testing the
  satisfiability of propositional Horn formulae*, J. Logic Programming 1(3):267–284, 1984,
  [doi:10.1016/0743-1066(84)90014-1](https://doi.org/10.1016/0743-1066(84)90014-1): SAT de Horn proposicional é
  linear. Se algum dia você precisar checar consistência de fatos + regras, é linear — mas o código não faz isso.
- **Validação de método = satisfatibilidade.** Falso como implementado. `mhHolds` (`methods.go:125-139`) avalia uma
  condição ground num `map[string]string` concreto: `set/unset/eq/ne`, `O(1)`. `mhApplicable` (`methods.go:141-156`)
  é uma conjunção sobre os fatos. `mhBadCmd` (`methods.go:115`) checa a allowlist. O forge, quando existir, faria
  schema-check (caps existem, JSON válido, pré/pós bem formados). Nada disso é SAT. Viraria SAT só se você
  perguntasse "existe atribuição de fatos desconhecidos e slots que torne o método aplicável **e** o verificador
  verdadeiro", com disjunção/negação sobre incógnitas — e, com a linguagem fechada atual (`set/unset/eq/ne`, sem
  disjunção), ainda seria checagem de consistência polinomial. **VIAGEM.** A formalização útil é "método = pré/pós
  estilo Hoare com verificador executável", não "método = fórmula SAT".

---

### 3.9 Primos de Mersenne: a verdade

**Veredito: VIAGEM como núcleo de decisão; LOAD-BEARING apenas como encanamento, em dois lugares específicos.**

Fatos, sem enfeite:

1. **`2^31 − 1 = 2147483647` é primo** (o 8º primo de Mersenne). Cabe em `int32` (é `MaxInt32`). É usado em
   Rabin-Karp porque (a) o módulo é primo, o que uniformiza o hash de uma função fraca, e (b) a redução de Mersenne
   é barata (dobra os bits altos). Karp & Rabin, *Efficient randomized pattern-matching algorithms*, IBM J. Res.
   Dev. 31(2):249–260, 1987, [doi:10.1147/rd.312.0249](https://doi.org/10.1147/rd.312.0249).
2. **No i386, o produto de dois números de 31 bits não cabe em `int` (32-bit).** Uma implementação em Go precisa de
   `uint64` para o produto antes do `%`. Isso é uma armadilha real de portabilidade, não um detalhe.
3. **MT19937 não usa `2^31−1` como módulo.** O nome vem do **período** `2^19937 − 1`, que é um primo de Mersenne.
   Estado de 624 palavras de 32 bits, equidistribuição 623-dimensional. Matsumoto & Nishimura, ACM TOMACS 8(1):3–30,
   1998, [doi:10.1145/272991.272995](https://doi.org/10.1145/272991.272995). **Não é criptograficamente seguro**:
   é linear sobre GF(2); 624 saídas consecutivas permitem prever o resto
   ([FAQ do autor](https://www.math.sci.hiroshima-u.ac.jp/m-mat/MT/efaq.html)).
4. **O jazz não usa nenhum dos dois.** O hash do NB é FNV-1a (`nlu.go:1344-1361`) com máscara de potência de dois
   (`h & (nuBuckets-1)`, `nuBuckets = 1<<14`). `nameindex` usa bitmask de letras, não módulo primo. Não há
   `2147483647`, `MT19937` nem Rabin-Karp no código (grep vazio).

Onde faria sentido de verdade:

- **Índice de conteúdo por trigramas (proposta B de `offline-brain-research.md`).** Fingerprints Rabin-Karp com
  `2^31−1` no 386 (aritmética `uint64`) ou `2^61−1` no 64-bit; posting lists por trigrama; `regexp` confirma só os
  candidatos. Aí o primo de Mersenne é a escolha certa e resolve um problema real (buscar sem `rg`).
- **Amostragem determinística no eval.** Um PRNG semeado; use PCG/xoshiro, não MT19937 (mais estado, mais lento, e
  qualidade irrelevante para 75 tarefas). Nunca para segurança.

Onde é numerologia: "primos de Mersenne unificam o cérebro", "a entropia dos primos", "2^31−1 como limiar de
confiança". **É bullshit e aqui está o porquê:** não existe função de decisão, teorema ou mecanismo em que um primo
de Mersenne apareça no cérebro offline. O primo não entra na regra de Chow, não entra no NB (que usa FNV-1a com
máscara de `2^14`), não entra na calibração e não entra no MDL. Ele entraria apenas num rolling hash de índice de
conteúdo — e aí é encanamento. Se o dono quer usar
Mersenne para outra coisa, a única coisa defensável é o índice de conteúdo — e mesmo assim é encanamento, não núcleo.

---

### 3.10 A tese mestra: asset-forge = compressão MDL/Kolmogorov

**Veredito: LOAD-BEARING parcial como objetivo; VIAGEM na forma literal ("cada asset é um pedaço do LLM").**

#### O que é formalizável

**MDL (Rissanen 1978).** O melhor modelo para os dados `D` minimiza o código de duas partes:

```
L(H) + L(D | H)
```

onde `L(H)` é o comprimento da descrição da hipótese e `L(D|H)` o dos dados codificados com ela. Rissanen, *Modeling
by shortest data description*, Automatica 14(5):465–471, 1978,
[doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5); a forma de duas partes está em
Grünwald, *A Tutorial Introduction to the Minimum Description Length Principle*,
[PDF](https://homepages.cwi.nl/~paulv/course-kc/mdlintro.pdf), §1.3.

**Kolmogorov (1965) / Solomonoff (1964).** `K(x)` é o comprimento do menor programa que gera `x`. É **incomputável**
(Li & Vitányi, *An Introduction to Kolmogorov Complexity and Its Applications*, Springer, 2008,
[doi:10.1007/978-0-387-49820-1](https://doi.org/10.1007/978-0-387-49820-1)). O próprio Hutter diz: "The major
drawback of `K` as complexity measure is its incomputability. So in practical applications it has always to be
approximated, e.g. by Lempel-Ziv compression, or by CTW, or by using two-part codes like in MDL"
([Open Problems in Universal Induction](https://ar5iv.labs.arxiv.org/html/0907.0746)). Portanto "compressão de
Kolmogorov" não é uma operação que o forge possa executar. O que ele pode executar é **MDL com um código fixo**
(gzip, ou o tamanho em bytes do JSON).

**Cuidado com "inteligência = compressão".** Legg & Hutter definem inteligência universal em termos de compressão e
recompensa esperada, mas é uma definição assintótica e incomputável (AIXI). Ela não licencia dizer que um binário de
13 MB que comprime 644 exemplos é "inteligente"; licencia dizer que **a escolha de representação é o problema
central**. Fonte: Legg & Hutter, *Universal Intelligence: A Definition of Machine Intelligence*, Minds and Machines
17(4):391–444, 2007, [arXiv:0712.3329](https://arxiv.org/abs/0712.3329).

**DreamCoder (2021).** É o mecanismo real mais próximo do que o dono quer: sintetiza programas para tarefas, depois
refatora subexpressões comuns em primitivas novas, aceitando a refatoração se ela **reduz o MDL do corpus**. Ellis et
al., *DreamCoder: bootstrapping inductive program synthesis with wake-sleep library learning*, PLDI 2021,
[doi:10.1145/3453483.3454080](https://doi.org/10.1145/3453483.3454080); arXiv
[2006.08381](https://arxiv.org/abs/2006.08381). A documentação do compressor descreve exatamente o "sleep phase":
induzir uma gramática mais eficiente, abstrair fragmentos recorrentes, reduzir o MDL
([DeepWiki](https://deepwiki.com/lio-wong/dreamcoder/4-library-learning:-compression)). **Isso é LOAD-BEARING para
assets que são programas** (métodos, derivações de parse, scripts). Não é para prosa.

**Rate-distortion (Shannon 1959).** `R(D)` é a taxa mínima para distorção `D`. Shannon, *Coding theorems for a
discrete source with a fidelity criterion*, IRE Convention Record 7, 1959,
[doi:10.1109/9780470544242.ch21](https://doi.org/10.1109/9780470544242.ch21). Enquadra o trade-off "asset pequeno ×
falha alta"; **não** é um algoritmo, porque `R(D)` depende da distribuição-fonte, que você não tem.

**Destilação (Hinton 2015).** O aluno aprende a saída do professor (soft targets) numa distribuição. Hinton, Vinyals
& Dean, [arXiv:1503.02531](https://arxiv.org/abs/1503.02531). O que se preserva é **comportamento numa distribuição**,
não pesos. Não existe mapa de decodificação de um asset de KB para o LLM.

#### Onde a frase do dono é falsa

> "Cada asset é um pedaço do LLM comprimido num código curto offline."

Três objeções, em ordem de força:

1. **Não há mapa de decodificação.** Um asset é um programa/tabela que reproduz uma função em um domínio. Não é um
   subgrafo dos pesos, nem um fragmento que se possa "descomprimir" de volta. A metáfora sugere uma relação
   information-theoretic que não existe: não se pode reconstruir o LLM a partir dos assets, nem os assets a partir de
   um pedaço do LLM.
2. **Kolmogorov é incomputável.** "Comprimido por Kolmogorov" não é uma operação. O que existe é "comprimido sob um
   código escolhido" = MDL. A frase, tomada literalmente, não tem procedimento.
3. **No free lunch.** Wolpert & Macready, *No free lunch theorems for optimization*, IEEE Trans. Evol. Comput.
   1(1):67–82, 1997, [doi:10.1109/4235.585893](https://doi.org/10.1109/4235.585893): nenhuma biblioteca fixa é
   universalmente melhor; o ganho é específico do domínio. O forge pode ser ótimo para "agente de código em repo Go",
   não para "qualquer tarefa". Isso é aceitável — mas mata a pretensão de "compressão universal do LLM".

**É bullshit e aqui está o porquê:** a frase pressupõe um mapa de decodificação que não existe. Um asset de 2 KB não
é um subconjunto dos pesos; é uma função. Sem mapa, "pedaço do LLM" é uma metáfora sem referente — e, como
Kolmogorov é incomputável, nem sequer há um procedimento que a torne verdadeira.

A formulação correta, que sobrevive:

> **Cada asset é um resumo funcional com perda do comportamento do LLM forte numa distribuição de tarefas,
> selecionado porque reduz um custo esperado regularizado (equivalentemente, um objetivo MDL) em tarefas
> held-out.**

Isso é formalizável, implementável e útil. É destilação + MDL + seleção de modelo, não "compressão do LLM".

#### O objetivo que de fato une brain e forge

Seja `L` a biblioteca de assets (exemplos, léxico, métodos, nós de calibração) e `D` a distribuição de tarefas.
Defina:

```
J(L) = Ê_{x~D}[ custo do cérebro_L(x) ] + λ · L(L)
```

onde `custo` inclui falha, pergunta, comando e tempo (os campos de `evaloffline.go:94-101`), e `L(L)` é o tamanho
comprimido da biblioteca (bytes de JSON gzip, ou uma codificação fixa). O forge propõe um asset `a`; aceite se

```
ΔJ = [L(L ∪ {a}) − L(L)] + λ · [Ê_heldout(L ∪ {a}) − Ê_heldout(L)] < 0
```

Isso é **empirical risk minimization com regularizador MDL**. É a unificação honesta: o brain minimiza o primeiro
termo em runtime; o forge minimiza o segundo em compile-time; ambos sob o mesmo `J`. Note que não é "o mesmo
algoritmo" — é o mesmo **objetivo**. E é barato: 75 tarefas, assets de KB, gzip de stdlib.

Um teste adversarial desse objetivo contra o código atual: a tabela NB tem 1,6 MB para 644 exemplos. Em MDL de duas
partes, `L(H)` é enorme comparado a `L(D)` (os 644 exemplos são ~20 KB de texto, estimativa). Isso **não** significa que a tabela
seja ruim: ela é uma função determinística dos dados + `α`, e a maior parte das linhas é o mesmo valor `zero`
(`nlu.go:1400-1412`), que comprime a quase nada. Significa que o tamanho *deployado* e o tamanho *MDL* são coisas
diferentes, e que a pergunta certa é **marginal**: "este asset novo paga o seu `ΔL`?" — não "a tabela é grande?".
É exatamente o que `ΔJ` responde.

#### O que o forge pode forjar, por tipo de asset

| Asset | É programa? | MDL se aplica? | Como validar |
|---|---|---|---|
| Exemplos NLU (`examples.jsonl`) | não (dados) | sim, como `L(D\|H)` | parseia na própria intenção (`nuGet`), held-out |
| Léxico/gírias | não (tabela) | sim, marginal | não quebra intenções existentes; blind set |
| Métodos (`methods/*.json`) | **sim** (plano) | **sim, DreamCoder-like** | schema + caps existem + `mhBadCmd` + dry-run |
| Nós de calibração | não (mapa) | sim, poucos bytes | ECE held-out, LOO |
| Lições em prosa (`improve.go`) | não | **não** (prosa não é programa) | humano; não entra no `J` |

Isso mostra onde a tese funciona: **métodos** são o análogo real de "biblioteca de programas" do DreamCoder. Prosa
não é. Exemplos e léxico são dados, não compressão de programa.

---

## 4. Contra-argumentos (o caso mais forte contra a tese)

1. **A teoria da informação dá a moeda, não o motor.** Chow diz *que forma* a regra ótima tem; não diz *como*
   estimar `P(Y|x)`. MDL diz *que* objetivo o forge deve minimizar; não diz *como* propor assets. A parte difícil —
   o classificador, a cascata, a biblioteca de métodos, a calibração — é engenharia, e nenhum dos teoremas a
   resolve.
2. **As quatro peças não são um teorema só.** Entropia ≠ Chow em multiclasse; Huffman ≠ menu; minimax ≠ busca
   single-agent; MDL ≠ rate-distortion. A "unificação" é parentesco de família. Forçá-la produz afirmações erradas
   (abstenção por entropia, menu de Huffman, planner alpha-beta).
3. **O código contradiz várias das afirmações.** `calib.go` morto; `backchain.go` morto; `p` condicionado e
   temperado; menu não ordenado por frequência; nenhum primo de Mersenne; nenhum `forge.go`. A tese descreve um
   sistema que ainda não é o que está no repo.
4. **No Dell, os ganhos medidos são de engenharia, não de informação.** 200 ms → 90 ms veio de não chamar `math.Log`
   786 mil vezes; 14,3 s → 120 ms veio de não chamar o modelo. Nenhum desses ganhos veio de um teorema de
   informação. O argumento information-theoretic é útil para *decidir o que medir*, não para *produzir o ganho*.
5. **Amostra pequena.** 644 exemplos e 75 tarefas. ECE com `n < 10³` é enviesado; um limiar escolhido na curva
   risco–cobertura overfitta fácil. Qualquer proposta de calibração/limiar precisa de LOO/bootstrap e faixa de
   guarda, ou vira superstição numérica.
6. **O forge pode envenenar o cérebro.** Um asset forjado que "passa no parse" mas enviesa a confiança é pior que
   nenhum asset. A validação determinística (parseia na própria intenção) é necessária, não suficiente; o `ΔJ` em
   held-out é a defesa.

O que sobrevive a esses seis ataques: o **problema de decisão** (§1.1), a **calibração como significado de `p`**, a
**curva risco–cobertura como ponto de operação**, o **MDL como critério de aceitação de asset**, o **branch-and-bound
com bound admissível** e a **cascata barato→caro**. É um núcleo menor, mas é todo verdadeiro.

---

## 5. Onde o dono está tentando chegar (e o que exatamente faz sentido)

O dono está tentando construir um **agente que decide sob incerteza com um orçamento de computação e de descrição**,
com um **professor caro no compile-time** e um **aluno barato no runtime**. Traduzindo para as três peças:

1. **Runtime = classificador cost-sensitive com opção de rejeição.** A decisão agir/perguntar/entregar é Chow. O
   ponto de operação é a curva risco–cobertura. A confiança precisa ser calibrada para o limiar significar algo.
   Isso é sólido e é exatamente o que `evaloffline.go` já mede (com outros nomes).
2. **Compile-time = compressor com perda de comportamento.** O professor (LLM forte) é consultado offline; o asset é
   um resumo funcional (destilação/MDL/library learning). O critério de aceitação é `ΔJ < 0` em held-out. Isso é
   sólido para **métodos** (programas), plausível para exemplos/léxico, e não se aplica a prosa.
3. **Pipeline = cascata com early exit.** A ordem barato→caro é a forma certa. O que falta é medir o ganho marginal
   por custo de cada estágio e desligar o que não muda decisão. Isso é sólido e barato.

O que **não** faz sentido, e o dono deve abandonar:

- "Entropia da distribuição de intenções" como variável de decisão. Use `max p` calibrado.
- "Menu de Huffman". Use ordenação por frequência (busca linear), se medir que importa.
- "Alpha-beta/minimax no planner". Não há adversário. Use branch-and-bound + bound admissível, que já existe.
- "Lyapunov". Use ranking function/orçamento, que já existe.
- "Validação de método = SAT". É schema + allowlist + dry-run.
- "Primos de Mersenne unificam". São encanamento de hash, no máximo.
- "Cada asset é um pedaço do LLM". É resumo funcional, não fragmento de pesos.
- "Compressão de Kolmogorov" como procedimento. É MDL com código fixo.

**A frase honesta:** você está tentando chegar em um agente que minimiza custo esperado sob incerteza, com um
professor caro no compile-time e um aluno barato no runtime. Isso é um programa de pesquisa coerente. O que não é
coerente é chamar isso de "teoria da informação": a teoria da informação é a **moeda** (bits, custo esperado,
risco–cobertura); o **motor** é cascata + calibração + busca limitada + biblioteca de métodos.

---

## 6. O que dá para implementar em Go no Dell

Ordenado por valor esperado. Métrica = a que move `jazz eval offline` (`no_llm_rate`, sucesso verificado,
`false_success`, `false_blocks`, perguntas, comandos/tarefa, p50/p95, RSS). Impacto 1–5; esforço S/M/L; risco
baixo/médio/alto. Tudo stdlib.

### P1 — Ligar a calibração que já existe (harness, não decisão)

**Mecanismo.** Novo `calibrate.go` (ou subcomando `jazz eval calib`) que roda o corpus rotulado — 75 tarefas +
`nlu/blind_test.json` + LOO dos 644 exemplos — e registra `(canal, conf, acertou)`. Canais: `rule-only`,
`rule+nb-agree`, `rule+nb-veto`, `nb-only`, `tie`, `typo-only`, `resolve`, `env`. Calcula ECE/MCE/Brier/NLL com
**bins por quantil** (10–15) e despeja a tabela em `results/`. `calib.go` já tem `ECE` (`:164`), `BrierScore`
(`:198`), `PlattFit` (`:220`), `IsotonicFit` (`:281`); falta o chamador e o campo de canal em `Intent`.

**Métrica.** ECE/MCE por canal reportados; habilita P2–P3. **Impacto 5 · Esforço M · Risco baixo** (aditivo, não
muda decisão).

### P2 — Mapa de confiança por canal, como asset

**Mecanismo.** Ajustar Platt (ou beta) por canal com LOO; se ≥50 pontos, isotônico (PAV, ~16 nós). Emitir
`nlu/calib.json` com nós `(conf_in int16, conf_out int16)` e clips por canal; `Parse` interpola linearmente no nó,
O(1), zero alloc. Fallback: identidade (comportamento atual). Não tocar no argmax (mapa monotônico).

**Métrica.** ECE↓; `false_blocks`↓ com `no_llm_rate` constante; `false_success` = 0 preservado. **Impacto 4 · Esforço
M · Risco médio** (dados pequenos; clip + LOO + fallback).

### P3 — Limiar pela curva risco–cobertura, não por constante

**Mecanismo.** Varrer `τ ∈ {0.5..0.95}` e margens `μ ∈ {1..4}·fzSlip·8` sobre o corpus; para cada `τ`, reportar
`no_llm_rate`, `false_success`, `false_blocks`, perguntas. Escolher `τ*` que maximiza cobertura com
`false_success = 0` e folga `δ` (meio bin). Aplicar a `nuMinConf` (`nlu.go:45`), `brLevel0Min` (`brain.go:596`),
margem de `brPick` (`methods.go:1151`) e `niTop` (`nameindex.go:716`). Bootstrap/LOO obrigatório. **Atenção:** a
margem de `brPick` (`fzSlip*8` = 16 unidades de distância) e o empate de `niTop` (`fzSlip` = 2) não são
probabilidades; são limiares em unidades de distância de edição. Calibrá-los exige um modelo à parte (P7 de
`math-offline-brain.md`), não o mesmo mapa de confiança.

**Métrica.** `no_llm_rate`↑ com `false_success` = 0; perguntas ≈ `questions_needed`. **Impacto 4 · Esforço S–M ·
Risco médio** (overfit ao corpus pequeno; folga conservadora).

### P4 — NB preguiçoso + veto barato (o ganho de custo mais claro)

**Mecanismo.** Em `Parse`, no ramo de regra única (`nlu.go:971-980`), **não** chamar `classify` para o bump
0.9→0.97 (irrelevante para 0.6 e 0.85). Para o veto, usar um sinal barato: fração de n-gramas do pedido em baldes
globalmente vazios (`oov_frac`, bitmap de 2 KB) + resíduo (`residualCount`, `nlu.go:1063,1121`). Só treinar o NB quando
`len(matches)==0` ou empate. Medir decisões antes/depois nas 75 tarefas.

**Métrica.** CPU p50 do nível 0 ↓ (alvo: 90 ms → 40–50 ms no D610, estimativa), mesmas decisões, mesmo
`false_success`. **Impacto 3 · Esforço S · Risco baixo** (mudança local, fácil de reverter).

### P5 — Portão MDL para o forge

**Mecanismo.** Implementar `ΔJ` (§3.10) em `forge.go`: `L(L)` = bytes de JSON gzip da biblioteca; `Ê` = custo médio
nas tarefas held-out (falha + pergunta + comando + ms normalizados). Aceitar asset só se `ΔJ < 0` e o parse/schema
passar. Proveniência `{sessão, modelo, wins, ΔJ}`; `--promote` mostra o diff e confirma. Sem `forge.go` hoje — é a
peça que falta para a tese virar mecanismo.

**Métrica.** Assets aceitos/rejeitados; `ΔL` e `ΔÊ` por asset; `no_llm_rate`↑ sem `false_success`. **Impacto 4 ·
Esforço M · Risco baixo–médio** (asset envenenado é o risco; held-out é a defesa).

### P6 — Instrumentação de decisão por estágio

**Mecanismo.** Contadores por pedido: `conf` do top1, margem top1−top2, `oov_frac`, estágio que decidiu
(`regex|rule|nb|tie|typo|resolve`), tempo por estágio, e **se o estágio mudou a decisão** (comparar com o contrafactual
sem ele). Expor em `brResult` debug e agregar no eval. É o que torna P1–P5 mensuráveis.

**Métrica.** % de pedidos em que cada estágio muda a decisão; custo por estágio. **Impacto 4 · Esforço S · Risco
nulo.**

### P7 — Índice de conteúdo com Rabin-Karp (onde Mersenne faz sentido)

**Mecanismo.** Posting lists trigrama→fileID por arquivo de código ≤1 MB, persistidas em
`dataDir()/ni/<hash>.tri` com validação por dir/mtime (reusar `nameindex_idx.go`). Fingerprint Rabin-Karp com
`2^31−1` no 386, aritmética `uint64`; `regexp` (RE2) confirma só os candidatos. Alimenta `find_text`/`search` sem
`rg`.

**Métrica.** Latência de busca (p50/p95), recall vs `rg`, tamanho do índice, RSS. **Impacto 3 · Esforço L · Risco
médio** (corretude do filtro; colisões; validação).

### P8 — Ordenar o menu por frequência (só se telemetria mostrar que importa)

**Mecanismo.** `offers.go:158` monta a lista em ordem fixa. Ordenar por frequência observada de escolha (contador
por `Kind`), com desempate estável. É a **busca linear ótima**, não Huffman. Se o usuário usa atalho de tecla, o
ganho é ~0; se ele lê a lista, o ganho é real.

**Métrica.** Tempo até a escolha / posição escolhida (se houver telemetria). **Impacto 2 · Esforço S · Risco baixo.**
Honestamente: provavelmente não vale o código antes de P1–P5.

### O que não implementar

- **Abstenção por entropia** como regra separada. Use `max p` calibrado. Em multiclasse, entropia ordena diferente.
- **Huffman no menu.** Com tecla única, é vazio; com custos desiguais, é um problema sem algoritmo polinomial
  conhecido.
- **Alpha-beta/minimax.** Sem adversário.
- **SAT para validar método.** É schema + allowlist + dry-run.
- **Lyapunov formal.** Use ranking function/orçamento.
- **Mersenne como núcleo.** Só no índice de conteúdo.
- **"Comprimir o LLM em KB" como objetivo literal.** Use `ΔJ`.
- **Calibrar em runtime** com dados do usuário (poucos, viesados). O mapa é asset; runtime só interpola.
- **ECE com bins fixos e `n < 10³`.** Quantis ou nada.

---

## 7. Veredito final

**A tese, como slogan, é meia verdade.** A parte certa é o **objetivo**: um agente que minimiza custo esperado sob
incerteza, com um professor caro no compile-time e um aluno barato no runtime, e que sabe quando não sabe. A parte
errada é o **motor**: não há um teorema único; há quatro papéis num mesmo problema de decisão, e três das quatro
promoções (entropia→decisão, Huffman→menu, minimax→planner) apontam para a regra errada.

O que o dono deve levar deste documento:

1. **Troque entropia por `max p` calibrado.** A regra é Chow; a calibração é o que a torna utilizável. `calib.go`
   já existe e está desligado — ligue antes de qualquer outra coisa.
2. **Trate o forge como MDL, não como Kolmogorov.** `ΔJ < 0` em held-out é o critério; métodos são o análogo real
   de biblioteca de programas; prosa não é.
3. **Meça o ganho por estágio e desligue o que não muda decisão.** O NB rodando para um bump irrelevante é o
   exemplo mais claro de custo sem informação.
4. **Use a busca que já funciona:** bound admissível + branch-and-bound. Não importe alpha-beta sem adversário.
5. **Não confunda encanamento com núcleo:** Mersenne, FNV, `int16` são implementação; a decisão é outra.

Se o dono quer uma frase para o README, que seja esta: **"o cérebro offline é uma política de decisão
sensível a custo (cost-sensitive) com rejeição, calibrada, sobre uma cascata de testes baratos e uma biblioteca de
métodos forjada por MDL."** É menos glamourosa que "teoria da informação unifica tudo", e é verdadeira.

---

## Fontes

**Decisão e abstenção**
- Chow, C.K. — *On optimum recognition error and reject tradeoff*. IEEE Trans. Inf. Theory 16(1):41–46, 1970. [doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)
- El-Yaniv, R. & Wiener, Y. — *On the Foundations of Noise-free Selective Classification*. JMLR 11:1605–1641, 2010. [PDF](https://jmlr.csail.mit.edu/papers/volume11/el-yaniv10a/el-yaniv10a.pdf)
- Geifman, Y. & El-Yaniv, R. — *Selective Classification for Deep Neural Networks*. NeurIPS 2017. [arXiv:1705.08500](https://arxiv.org/abs/1705.08500)
- Charoenphakdee, N. et al. — *Classification with Rejection Based on Cost-sensitive Classification*. ICML 2021. [PDF](http://proceedings.mlr.press/v139/charoenphakdee21a/charoenphakdee21a.pdf)
- Fumera, G., Roli, F. & Giacinto, G. — *Reject option with multiple thresholds*. Pattern Recognition 33(12):2099–2101, 2000. [doi:10.1016/S0031-3203(00)00059-5](https://doi.org/10.1016/S0031-3203(00)00059-5)
- Franc, V., Průša, D. & Voráček, V. — *Optimal Strategies for Reject Option Classifiers*. JMLR 24, 2023. [PDF](http://www.jmlr.org/papers/volume24/21-0048/21-0048.pdf)

**Calibração e probabilidade**
- Guo, C., Pleiss, G., Sun, Y. & Weinberger, K. — *On Calibration of Modern Neural Networks*. ICML 2017. [arXiv:1706.04599](https://arxiv.org/abs/1706.04599)
- Vaicenavicius, J. et al. — *Evaluating model calibration in classification*. AISTATS 2019. [PMLR v89](https://proceedings.mlr.press/v89/vaicenavicius19a.html)
- Naeini, M.P., Cooper, G.F. & Hauskrecht, M. — *Obtaining Well Calibrated Probabilities Using Bayesian Binning*. AAAI 2015. [ojs.aaai.org](https://ojs.aaai.org/index.php/AAAI/article/view/9602)
- Niculescu-Mizil, A. & Caruana, R. — *Predicting Good Probabilities with Supervised Learning*. ICML 2005. [doi:10.1145/1102351.1102430](https://doi.org/10.1145/1102351.1102430)
- Platt, J. — *Probabilistic Outputs for Support Vector Machines and Comparisons to Regularized Likelihood Methods*, 1999. [citeseer:10.1.1.41.1639](http://citeseerx.ist.psu.edu/viewdoc/summary?doi=10.1.1.41.1639)
- Zadrozny, B. & Elkan, C. — *Transforming classifier scores into accurate multiclass probability estimates*. KDD 2002. [doi:10.1145/775047.775151](https://doi.org/10.1145/775047.775151)
- Kull, M., Silva Filho, T. & Flach, P. — *Beta calibration*. AISTATS 2017. [PMLR v54](https://proceedings.mlr.press/v54/kull17a.html)
- Perez-Lebel, A., Le Morvan, M. & Varoquaux, G. — *Beyond calibration: estimating the grouping loss of modern neural networks*. ICLR 2023. [arXiv:2210.16315](https://arxiv.org/abs/2210.16315)

**Naive Bayes e suavização**
- Domingos, P. & Pazzani, M. — *On the Optimality of the Simple Bayesian Classifier under Zero-One Loss*. Machine Learning 29:103–130, 1997. [doi:10.1023/A:1007413511361](https://doi.org/10.1023/A:1007413511361)
- Ng, A. & Jordan, M. — *On Discriminative vs. Generative Classifiers*. NeurIPS 2001. [PDF](https://proceedings.neurips.cc/paper_files/paper/2001/file/7b7a53e239400a13bd6be6c91c4f6c4e-Paper.pdf)
- Krichevsky, R. & Trofimov, V. — *The performance of universal encoding*. IEEE Trans. Inf. Theory 27(2):199–207, 1981. [doi:10.1109/TIT.1981.1056331](https://doi.org/10.1109/TIT.1981.1056331)
- Good, I.J. — *The population frequencies of species and the estimation of population parameters*. Biometrika 40(3–4):237–264, 1953. [doi:10.1093/biomet/40.3-4.237](https://doi.org/10.1093/biomet/40.3-4.237)
- Gale, W.A. & Sampson, G. — *Good-Turing frequency estimation without tears*. J. Quantitative Linguistics 2(3):217–237, 1995. [doi:10.1080/09296179508590051](https://doi.org/10.1080/09296179508590051)
- Kneser, R. & Ney, H. — *Improved backing-off for m-gram language modeling*. ICASSP 1995. [PDF](https://www-i6.informatik.rwth-aachen.de/publications/download/951/KneserReinhardNeyHermann--ImprovedBacking-offforM-gramLanguageModeling--1995.pdf)
- Chen, S. & Goodman, J. — *An empirical study of smoothing techniques for language modeling*. Computer Speech & Language 13(4):359–394, 1999. [doi:10.1006/csla.1999.0128](https://doi.org/10.1006/csla.1999.0128)

**Códigos, menus e árvores**
- Huffman, D.A. — *A Method for the Construction of Minimum-Redundancy Codes*. Proc. IRE 40(9):1098–1101, 1952. [doi:10.1109/JRPROC.1952.273898](https://doi.org/10.1109/JRPROC.1952.273898)
- Cover, T. & Thomas, J. — *Elements of Information Theory*, 2ª ed., Wiley 2006, cap. 5 (Teorema 5.8.1). [PDF cap. 5](https://ftp.esat.kuleuven.be/pub/pub/SISTA/decock/voor_xander/referenties/Cover&Thomas/5.pdf)
- Knuth, D. — *The Art of Computer Programming*, Vol. 3, §6.1 (busca sequencial; ordenação estática ótima).
- Bitner, J.R. — *On self-organizing sequential search heuristics*. CACM 22(2):86–95, 1979. [doi:10.1145/359997.360000](https://doi.org/10.1145/359997.360000)
- Quinlan, J.R. — *Induction of Decision Trees*. Machine Learning 1(1):81–106, 1986. [doi:10.1007/BF00116251](https://doi.org/10.1007/BF00116251)
- Hyafil, L. & Rivest, R. — *Constructing optimal binary decision trees is NP-complete*. Inf. Process. Lett. 5(1):15–17, 1976. [doi:10.1016/0020-0190(76)90095-8](https://doi.org/10.1016/0020-0190(76)90095-8)
- Golin, M., Kenyon, C. & Young, N. — *Huffman Coding with Unequal Letter Costs*. STOC 2002. [PDF](https://cs.ucr.edu/~neal/publication/Golin02Huffman.pdf)
- Golin, M. & Rote, G. — *A Dynamic Programming Algorithm for Constructing Optimal Prefix-Free Codes with Unequal Letter Costs*. IEEE Trans. Inf. Theory 44(5):1770–1781, 1998. [PDF](https://cse.hkust.edu.hk/~golin/pubs/Huffman2.pdf)
- Turney, P. — *Cost-Sensitive Classification: Empirical Evaluation of a Hybrid Genetic Decision Tree Induction Algorithm*. JAIR 3:369–409, 1995. [jair.org](https://jair.org/index.php/jair/article/view/10129)
- Norton, S.W. — *Generating better decision trees*. IJCAI 1989, pp. 484–488.

**Busca**
- Knuth, D. & Moore, R. — *An analysis of alpha-beta pruning*. Artificial Intelligence 6(4):293–326, 1975. [doi:10.1016/0004-3702(75)90019-3](https://doi.org/10.1016/0004-3702(75)90019-3)
- Hart, P., Nilsson, N. & Raphael, B. — *A Formal Basis for the Heuristic Determination of Minimum Cost Paths*. IEEE Trans. Syst. Sci. Cybern. 4(2):100–107, 1968. [doi:10.1109/TSSC.1968.300136](https://doi.org/10.1109/TSSC.1968.300136)
- Korf, R. — *Depth-first iterative-deepening: An optimal admissible tree search*. Artificial Intelligence 27(1):97–109, 1985. [doi:10.1016/0004-3702(85)90084-0](https://doi.org/10.1016/0004-3702(85)90084-0)
- Wald, A. — *Sequential tests of statistical hypotheses*. Ann. Math. Stat. 16(2):117–186, 1945.
- Wald, A. & Wolfowitz, J. — *Optimum Character of the Sequential Probability Ratio Test*. Ann. Math. Stat. 19(3):326–339, 1948. [doi:10.1214/aoms/1177730197](https://doi.org/10.1214/aoms/1177730197)
- Viola, P. & Jones, M. — *Rapid object detection using a boosted cascade of simple features*. CVPR 2001. [doi:10.1109/CVPR.2001.990517](https://doi.org/10.1109/CVPR.2001.990517)

**Lógica e planejamento**
- Fikes, R. & Nilsson, N. — *STRIPS: A new approach to the application of theorem proving to problem solving*. Artificial Intelligence 2(3–4):189–208, 1971. [doi:10.1016/0004-3702(71)90010-5](https://doi.org/10.1016/0004-3702(71)90010-5)
- Bylander, T. — *The computational complexity of propositional STRIPS planning*. Artificial Intelligence 69(1–2):165–204, 1994. [doi:10.1016/0004-3702(94)90081-7](https://doi.org/10.1016/0004-3702(94)90081-7)
- Erol, K., Hendler, J. & Nau, D. — *Complexity results for HTN planning*. Ann. Math. Artif. Intell. 18:69–93, 1996. [doi:10.1007/BF02136175](https://doi.org/10.1007/BF02136175)
- Dowling, W. & Gallier, J. — *Linear-time algorithms for testing the satisfiability of propositional Horn formulae*. J. Logic Programming 1(3):267–284, 1984. [doi:10.1016/0743-1066(84)90014-1](https://doi.org/10.1016/0743-1066(84)90014-1)
- Floyd, R. — *Assigning meanings to programs*. Proc. AMS Symp. Applied Math 19:19–32, 1967. [PDF](https://www.cse.chalmers.se/edu/year/2017/course/TDA384_LP1/files/lectures/additional-material/AssigningMeanings1967.pdf)
- Hoare, C.A.R. — *An axiomatic basis for computer programming*. CACM 12(10):576–580, 1969. [doi:10.1145/363235.363259](https://doi.org/10.1145/363235.363259)
- Lyapunov, A.M. — *The General Problem of the Stability of Motion*, 1892 (trad. Taylor & Francis, 1992).
- Kephart, J. & Chess, D. — *The vision of autonomic computing*. Computer 36(1):41–50, 2003. [doi:10.1109/MC.2003.1160055](https://doi.org/10.1109/MC.2003.1160055)

**Compressão, MDL e destilação**
- Rissanen, J. — *Modeling by shortest data description*. Automatica 14(5):465–471, 1978. [doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5)
- Grünwald, P. — *A Tutorial Introduction to the Minimum Description Length Principle*, 2005. [PDF](https://homepages.cwi.nl/~paulv/course-kc/mdlintro.pdf)
- Kolmogorov, A.N. — *Three approaches to the quantitative definition of information*. Problems of Information Transmission 1(1):1–7, 1965.
- Solomonoff, R.J. — *A formal theory of inductive inference. Part I*. Information and Control 7(1):1–22, 1964. [doi:10.1016/S0019-9958(64)90223-2](https://doi.org/10.1016/S0019-9958(64)90223-2)
- Li, M. & Vitányi, P. — *An Introduction to Kolmogorov Complexity and Its Applications*, Springer 2008. [doi:10.1007/978-0-387-49820-1](https://doi.org/10.1007/978-0-387-49820-1)
- Hutter, M. — *Open Problems in Universal Induction & Intelligence*, 2009. [arXiv:0907.0746](https://ar5iv.labs.arxiv.org/html/0907.0746)
- Ellis, K. et al. — *DreamCoder: bootstrapping inductive program synthesis with wake-sleep library learning*. PLDI 2021. [doi:10.1145/3453483.3454080](https://doi.org/10.1145/3453483.3454080); [arXiv:2006.08381](https://arxiv.org/abs/2006.08381)
- Shannon, C.E. — *Coding theorems for a discrete source with a fidelity criterion*. IRE Convention Record 7, 1959. [doi:10.1109/9780470544242.ch21](https://doi.org/10.1109/9780470544242.ch21)
- Hinton, G., Vinyals, O. & Dean, J. — *Distilling the Knowledge in a Neural Network*, 2015. [arXiv:1503.02531](https://arxiv.org/abs/1503.02531)
- Wolpert, D. & Macready, W. — *No free lunch theorems for optimization*. IEEE Trans. Evol. Comput. 1(1):67–82, 1997. [doi:10.1109/4235.585893](https://doi.org/10.1109/4235.585893)
- Legg, S. & Hutter, M. — *Universal Intelligence: A Definition of Machine Intelligence*. Minds and Machines 17(4):391–444, 2007. [arXiv:0712.3329](https://arxiv.org/abs/0712.3329)

**Hashing e aleatoriedade**
- Karp, R. & Rabin, M. — *Efficient randomized pattern-matching algorithms*. IBM J. Res. Dev. 31(2):249–260, 1987. [doi:10.1147/rd.312.0249](https://doi.org/10.1147/rd.312.0249)
- Matsumoto, M. & Nishimura, T. — *Mersenne Twister: A 623-Dimensionally Equidistributed Uniform Pseudo-Random Number Generator*. ACM TOMACS 8(1):3–30, 1998. [doi:10.1145/272991.272995](https://doi.org/10.1145/272991.272995)
- Matsumoto, M. — *Mersenne Twister FAQ* (não é criptograficamente seguro). [math.sci.hiroshima-u.ac.jp](https://www.math.sci.hiroshima-u.ac.jp/m-mat/MT/efaq.html)
