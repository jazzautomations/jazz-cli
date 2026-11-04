# Escala de assets e cobertura: a teoria honesta de "muitos assets bons"

Pesquisa de 04/11/2026. Continua [math-offline-brain.md](math-offline-brain.md) (calibração, informação,
orçamentos), [offline-brain-research.md](offline-brain-research.md) (algoritmos), [offline-on-device-intelligence.md](offline-on-device-intelligence.md)
(a conta do Dell e a proposta `jazz forge`) e [tocomas-plan-reuse.md](tocomas-plan-reuse.md) (reuso de planos).
Este documento faz a pergunta que os outros evitam: **quantos assets valem a pena, quais, e como saber** —
com a matemática da cobertura, os dados de distribuição de pedidos e o custo medido no alvo.

Convenções: `file:line` refere-se a esta árvore. **Medido** = medido nesta sessão ou em `results/`;
**(estimativa)** = número meu, não medido; **Dell medido** = `results/dell-0.9-progress.md`. As medições de
escala foram feitas em amd64 (Go 1.27.2) e extrapoladas para o Dell com fator ~3,2 — estimativa de CPU
calibrada pelo cold start medido no Dell (~130 ms), não uma razão limpa; a extrapolação está marcada. Nada
aqui foi rodado no Dell nesta sessão.

---

## 0. Tese, e o veredito em uma tabela

A afirmação do dono, "porrada de assets é o moat", decompõe-se em três claims independentes:

- **(A)** os pedidos reais têm cauda pesada, então poucos assets cobrem muito e a cauda é longa → vale
  acumular.
- **(B)** mais assets = mais inteligência.
- **(C)** os assets são o moat.

**Veredito curto.** (A) é verdadeira para **strings**, e falsa como está enunciada para **planos** — e é a
distribuição de planos que decide quantos métodos valem a pena. (B) é falsa sob orçamento fixo de verificação:
o asset marginal com verificador fraco tem valor esperado negativo. (C) é falsa: um método JSON é commodity;
o moat é a **fábrica + o portão + o medidor de cobertura**, nessa ordem.

A tabela abaixo é o resumo adversarial; cada linha é defendida numa seção.

| Conceito | Veredito | Por quê, em uma linha |
|---|---|---|
| Zipf em query logs | **LOAD-BEARING** | Frequência de consulta é uma das poucas propriedades de IR que realmente se ajusta a power law entre 15 distribuições testadas (Petersen 2016); medido na AOL (§2.1). |
| Cauda "infinita" | **METÁFORA ÚTIL** | Heaps: tipos distintos crescem ~n^0,5–0,6, não infinito; por usuário o vocabulário satura (§2.1). |
| "~30% repetidos" (AgentReuse) | **VIAGEM** para CLI de código | É assistente de IoT; "similar" é definido pelo classificador deles (circular); a evidência direta em código é ~7% (§2.2). |
| Reuso tem duas fatias | **LOAD-BEARING** | 16–23% de todas as consultas são repetição do mesmo usuário; num CLI single-user, 100% da repetição é local → cache (`plancache.go`), não biblioteca (§2.2). |
| Cobertura é submodular | **LOAD-BEARING** | Função de cobertura é monótona submodular; greedy dá 1−1/e (Nemhauser 1978) (§3.2). |
| Facility location | **LOAD-BEARING, opcional** | Modela "melhor asset por pedido"; útil só se há pool de candidatos (Cornuejols 1977) (§4.4). |
| Conflito preserva submodularidade | **É BULLSHIT** | Inserir asset no meio de uma ordem pode reduzir cobertura; a função deixa de ser monótona (§3.3). |
| MinHash/SimHash para dedup | **LOAD-BEARING** para quase-duplicata | Broder 1997 / Charikar 2002; não vê paráfrase (§4.3). |
| "Dedup semântico sem embedding" via MinHash | **É BULLSHIT** | MinHash é lexical; paráfrase com Jaccard 0,3 passa (§4.3). |
| Dedup por assinatura de parse | **LOAD-BEARING, grosseiro** | Colapsa 100 strings em 55 assinaturas (medido); é o espaço em que o modelo realmente decide (§4.3). |
| Portão held-out / FDR | **LOAD-BEARING** | Benjamini–Hochberg; mas o teste certo é não-inferioridade num guard set, não caça de significância (§4.5). |
| Winner's curse | **LOAD-BEARING** | Selecionar o máximo de M candidatos ruidosos superestima o ganho (Cawley & Talbot 2010) (§4.5). |
| Win-rate Beta / SPRT | **LOAD-BEARING** | Wald 1945; 5 vitórias seguidas dão limite inferior 0,607; barato e implementável (§4.6). |
| Bradley–Terry | **METÁFORA ÚTIL** | Ranquear dezenas de assets por pares é overkill com 50 intenções (§4.6). |
| Successive halving | **LOAD-BEARING** | Avaliar 1.000 candidatos com orçamento ~n log n em vez de n·F (Jamieson 2016) (§4.6). |
| Concept drift / ADWIN | **METÁFORA ÚTIL** | Poucas amostras por asset para teste de janela; use decaimento + TTL + evento de ambiente (§4.7). |
| MDL / DreamCoder | **METÁFORA ÚTIL** | O critério de compressão é certo; o wake-sleep não se aplica a métodos JSON (§4.1). |
| Working set (Denning) | **LOAD-BEARING** como distinção operacional | Boot paga o tamanho da biblioteca; runtime paga o working set (2–3 métodos) — medir os dois separa 250 de 10.000 (§5.2). |
| Goodhart | **LOAD-BEARING** | Otimizar `no_llm_rate` sozinho degrada `false_success` (Manheim 2018) (§5.4). |
| Composição > enumeração | **LOAD-BEARING** | Com N capacidades o planner compõe planos (crescimento combinatório); M métodos enumeram M planos (§6). |

---

## 1. O que é um asset, e quanto ele custa

"Asset" no jazz não é uma coisa só. Cada classe tem custo e efeito diferentes, e a política de escala tem de
ser por classe, não global.

| Classe | Onde vive | Contagem hoje | Custo unitário medido | O que compra |
|---|---|---|---|---|
| Intenção + regras | `nlu/intents.json` | 50 intenções | 32 KB de tabela NB + ~1 ms de treino por classe (amd64; ~3 ms Dell) | uma **classe** de pedido |
| Exemplo NLU | `nlu/intents.json` (`examples`) | 644 | ~43 µs de treino (amd64) | uma **superfície** dentro da classe |
| Entrada de léxico | `nlu/lexicon.json` | 612 palavras de conceito + 159 gírias (16 KB) | desprezível | gíria/sinônimo → conceito |
| Método | `methods/*.json` | 103 (máx. 5 por intenção, média 2,1) | ~26 µs de parse (amd64), ~0,1 ms Dell | um **plano** para a classe |
| Capacidade | `caps.go` (`cbBrainCaps`, `cbToolCaps`) | ~15 + scripts | desprezível | um **passo** componível |
| Plano em cache | `plancache.go` | ≤100 por projeto (`pcMaxPlans`, `plancache.go:45`) | ~1 KB | um plano **deste** projeto |

A assimetria fundamental, e ela governa todo o resto:

> **Breadth (mais intenções) é caro; depth (mais exemplos e métodos dentro de uma intenção) é barato.**

A tabela NB é `classes × 2^14 × 2 bytes` (`nlu.go:1330`, `nlu.go:1395`): 32 KB por classe, sempre, independente
do número de exemplos. Já um exemplo custa tempo de treino linear e **zero memória**; um método custa JSON
linear e **zero tabela**. Medido nesta sessão (amd64):

| classes | exemplos/classe | tabela NB | `nuTrain` |
|---|---|---|---|
| 50 (hoje) | 13 (644 total) | 1,6 MB | 40 ms |
| 100 | 13 | 3,2 MB | 99 ms |
| 200 | 13 | 6,4 MB | 162 ms |
| 400 | 13 | 12,8 MB | 358 ms |
| 800 | 13 | 25,6 MB | 472–542 ms |

E o parse de métodos: 1.000 = 26 ms, 5.000 = 141 ms, 20.000 = 585 ms (amd64). No teto realista de 250
métodos, são ~7 ms. Ou seja: **250 métodos custam ~14× menos CPU de boot do que 100 intenções novas** (7 ms
vs 99 ms). O limite de escala do jazz não é o número de métodos; é o número de **classes** e a **força do
verificador** (§4.2).

Isto já responde metade da pergunta do dono: "porrada de assets" só é barata se for porrada **dentro** das
classes existentes. Porrada de intenções novas é a mais cara de todas as opções.

---

## 2. A distribuição de pedidos: o que a literatura realmente mostra

### 2.1 Query logs: Zipf é real, mas é Zipf de *strings*

A lei de Zipf (1949) diz que a frequência do i-ésimo item é proporcional a `i^-s`. Em logs de busca, isso é
uma das regularidades mais replicadas — e também uma das mais mal citadas. O estudo estatisticamente correto
é Petersen, Simonsen & Lioma (2016): eles ajustam power law e outras 15 distribuições a 28 datasets de IR e
concluem que **frequência de consulta é um dos poucos casos em que a power law é de fato o melhor ajuste**;
as demais propriedades (comprimento de documento, frequência de termo etc.) são melhor ajustadas por Inverse
Gaussian, GEV, Negative Binomial ou Yule. Ou seja: "query log é Zipf" é verdade; "tudo em IR é Zipf" é
folclore.

Números clássicos, para calibrar a intuição:

- **AltaVista, 575 milhões de consultas não vazias em 6 semanas** (Silverstein et al. 1999): só **13,6%** das
  consultas aparecem mais de três vezes; as **25 mais frequentes somam 1,5%** do volume; 75% das sessões não
  têm reformulação.
- **AOL 2006** (20 milhões de consultas, ~650 mil usuários; ~10 milhões distintas no log completo — ~50% de
  strings únicas).
- **Excite/Vivisimo** (Xie & O'Hallaron 2002): 42,75% e 32,05% das consultas são repetidas; cada repetida é
  submetida 4,49 e 3,20 vezes; **37,35%** (Excite) das repetições são do **mesmo usuário**.

Jansen & Spink (2006), comparando nove logs de 1997–2002 (~1 bilhão de consultas), mostram o que **não** mudou:
~55–60% das sessões têm uma única consulta e ~27–30% das consultas têm um só termo. A forma da distribuição é
estável; o que muda é o conteúdo. E o ajuste correto de uma power law é MLE com cutoff escolhido por
Kolmogorov–Smirnov, não regressão visual no log-log (Clauset, Shalizi & Newman 2009) — a inclinação de uma
reta ajustada a olho é enviesada, e é o erro que produz "expoente de Zipf" onde não há.

O último número é o mais importante para o jazz, e ele foi mal lido no doc anterior. Das consultas do Excite,
42,75% são repetidas e **37,35% das repetições são do mesmo usuário** → ~16% de *todas* as consultas são
repetição do mesmo usuário, e ~27% são repetição entre usuários. No Vivisimo é o contrário: 32,05% repetidas,
**70,58%** delas do mesmo usuário → ~23% do total é local e ~9% é global. A divisão varia com a população; não
há uma resposta única.

O que isso significa para o jazz: num CLI **single-user**, repetição entre usuários não existe — 100% da
repetição é local, e o mecanismo certo é o cache (`plancache.go`), não mais assets globais. Numa biblioteca
distribuída a muitos usuários, a fatia global (9–27%) é o que uma biblioteca de assets captura, e é
exatamente a fatia que **não** existe no Dell de um usuário. O valor da biblioteca, no Dell, não é repetição:
é cobertura de pedidos de primeira vez.

Para não depender de números de segunda mão, medi a curva de cobertura numa amostra real da AOL
(`user-ct-test-collection-02.txt.gz`, 3.614.507 consultas não vazias, contagem exata de strings após
lowercase). Cobertura de top-k = fração das **ocorrências** cobertas pelas k strings distintas mais frequentes
— o cenário mais generoso possível para um asset por string (não funde paráfrases):

| k (assets) | cobertura | ganho ao dobrar k |
|---|---|---|
| 100 | 8,6% | — |
| 200 | 9,9% | +1,3 pp |
| 400 | 11,5% | +1,6 pp |
| 800 | 13,5% | +2,0 pp |
| 1.600 | 16,0% | +2,5 pp |
| 3.200 | 19,1% | +3,1 pp |
| 6.400 | 22,8% | +3,8 pp |
| 12.800 | 27,5% | +4,6 pp |
| 25.600 | 33,3% | +5,8 pp |
| 51.200 | 40,6% | +7,3 pp |
| 102.400 | 49,8% | +9,2 pp |
| 204.800 | 61,2% | +11,4 pp |
| 409.600 | 74,6% | +13,4 pp |
| 819.200 | 88,2% | +13,6 pp |

Nesta amostra: 34,4% das strings são únicas; 60,5% aparecem uma vez; 14,7% aparecem mais de três vezes (o
número da AltaVista, 13,6%, replica bem). A taxa de únicas é menor que os ~50% do log completo da AOL porque
cada arquivo cobre um subconjunto de usuários; o que importa aqui é a **forma da cauda**, não o nível. A
inclinação local de `C(k)` entre 10³ e 10⁶ é `d log C / d log k ≈ 0,27`, isto é, `C(k) ∝ k^0,27`.

Duas leituras, e elas são opostas:

1. **Por asset, o retorno é decrescente.** Cada asset novo cobre uma fatia menor que o anterior. É o que a
   intuição de "retorno decrescente" capta.
2. **Por dobra de investimento, o retorno é crescente** (+1,3 pp → +13,6 pp por dobra). Isso não é paradoxo:
   com cauda muito pesada, cada dobra adiciona mais assets do que a anterior, e a massa absoluta cresce. O
   dono está certo de que "continuar adicionando paga" **em cobertura de string**; está errado ao concluir
   que isso justifica a pilha, porque o custo por asset (verificação, manutenção, conflito) também cresce, e
   o número absoluto para 90% é ~10⁶ assets.

**Veredito: LOAD-BEARING para a forma da demanda por strings.** A power law é real, medida, e reproduzida.
**METÁFORA ÚTIL para "cauda infinita"**: Heaps (1978) mostra que o vocabulário cresce como `V(n) ∝ n^β` com
β ≈ 0,5 para texto e ~0,6 para a web; não é infinito, é sublinear — e, por usuário, satura. "Infinito" é
licença poética; o número que importa é o expoente e o custo.

### 2.2 Agentes de código: a evidência é mais fraca do que o doc anterior sugere

O número "~30% dos pedidos são idênticos ou similares" vem de **AgentReuse** (Li, Wu & Tan, arXiv:2512.21309),
que analisa um assistente pessoal de IoT (Xiao Ai / Blue Heart V), não um agente de código. Além disso, a
similaridade é definida **pelo classificador de intenção do próprio método** — a taxa de 93% de "reuso
efetivo" é medida contra a própria noção de similaridade que o sistema impõe. Isso não é fraude, mas é
circular o suficiente para não servir de base quantitativa para o jazz.

A evidência direta mais próxima de código é o estudo de telemetria do Transform Code no Google (Nam et al.,
arXiv:2504.20196): **~7% dos pedidos são re-prompt idêntico ou edição mínima**. É uma feature de edição em
IDE, não um CLI agente, mas é código real e usuários reais — e o número é 4× menor que o de IoT.

E a evidência de carga de agentes de código (TraceLab, arXiv:2606.30560; 665.453 passos, 8.058 sessões,
Claude Code + Codex) mostra concentração **extrema no nível de ferramenta**:

- as **3 ferramentas mais chamadas somam 85,0%** (Claude) e **85,3%** (Codex) das chamadas;
- **executar comando = 73,4%** de todas as chamadas; escrita/edição de arquivo 10,4%; leitura/busca 9,3%;
- **1.235 executáveis distintos** aparecem nos comandos de shell, mas `echo` (18,1%), `grep` (10,1%), `sed`
  (9,1%), `head` (6,4%), `tail` (4,2%), `python-script` (4,2%) e `rg` (3,0%) dominam; a cauda é longa e fina.

**Veredito: VIAGEM** para "30% de repetição justifica muitos assets num CLI de código". O que a TraceLab
mostra é o contrário do que a pilha sugere: a **cabeça é minúscula** (3 ferramentas = 85%) e a cauda é feita
de ferramentas especializadas que um planner deve **compor**, não enumerar. E **LOAD-BEARING** para a
distinção entre as duas fatias de reuso: num CLI single-user, a repetição é 100% local → `plancache.go`; a
biblioteca global cobre a fatia entre usuários, que no Dell não existe.

### 2.3 A armadilha dos dois níveis: strings vs planos

Um pedido `r` tem dois componentes: a **classe** `c` (intenção) e a **superfície** `s` (as palavras). Vale

```
P(r) = P(c) · P(s | c)
```

Um **método** cobre `(c, shape de slots)` para **todas** as superfícies; um **exemplo NLU** cobre uma região
de `s|c`. A cauda pesada que a literatura mede é a de `s|c`. A cauda que decide quantos métodos valem a pena é
a de `c` — uma taxonomia **finita** que o próprio projeto define.

Medi o colapso no conjunto cego do repo (`nlu/blind_test.json`, 100 frases escritas antes do ajuste, 48
intenções): **100 strings distintas → 55 assinaturas distintas** de `(intenção, nomes de slots)`, com
**90/100** classificadas na intenção esperada. Fator de colapso 1,82×. Esse é um **limite inferior**: o
conjunto cego é estratificado (~2 por intenção); tráfego natural concentra em poucas intenções e colapsaria
mais.

Se a distribuição de intenções for Zipf com 50 classes (hipótese, não medição):

| s | top 5 | top 10 | top 20 | top 30 |
|---|---|---|---|---|
| 0,8 | 40% | 55% | 72% | 84% |
| 1,0 | 51% | 65% | 80% | 89% |
| 1,2 | 62% | 75% | 86% | 93% |

Compare com a tabela da AOL: no nível de **plano**, 10 assets cobrem 65%; no nível de **string**, 100 assets
cobrem 8,6%. A diferença de 1–2 ordens de magnitude é o argumento central deste documento:

> **A cauda que justifica "porrada de assets" é a cauda de superfícies. A cauda que justifica "porrada de
> métodos" é a cauda de planos. São distribuições diferentes, e o discurso do dono as confunde.**

O trabalho do parser é justamente comprimir `s` em `c`; cada real de esforço gasto em generalização NLU
substitui uma quantidade enorme de assets por string. É por isso que o `nuGet().Parse` (`nlu.go:928`) é o
asset mais alavancado do sistema, e não os 103 métodos.

**Veredito: LOAD-BEARING como decomposição** (é a matemática certa para decidir onde investir). **VIAGEM
transferir o expoente de string para planos** — são objetos diferentes, e o doc anterior
([offline-on-device-intelligence.md](offline-on-device-intelligence.md)) usa o 30% como se fossem o mesmo.

---

## 3. Cobertura marginal: definição, matemática e o que dá para medir

### 3.1 Definição

Seja `P` a distribuição de pedidos e `S_a` o conjunto de pedidos que o asset `a` resolve **corretamente**.
Para um conjunto `A`:

```
C(A) = P( ∪_{a∈A} S_a )
Δ(a | A) = C(A ∪ {a}) − C(A) = P( S_a \ ∪_{b∈A} S_b )
```

`Δ` é a **cobertura marginal**: a fração de tráfego real que o asset novo passa a resolver e que nenhum outro
resolvia. É a única quantidade que responde "vale a pena este asset?", e ela não existe no repo hoje: não há
log agregado de pedidos, nem contagem por intenção. `improve_load.go` varre sessões, mas para avaliar runs, não
para estimar `P(c)`.

### 3.2 Submodularidade: onde a matemática carrega

Se os assets não conflitam e acertam deterministicamente, `C` é uma **função de cobertura**, monótona e
submodular:

```
A ⊆ B  ⇒  C(A∪{a}) − C(A) ≥ C(B∪{a}) − C(B)
```

porque `P(S_a \ ∪A) ≥ P(S_a \ ∪B)`. Consequências, todas teoremas:

- maximizar `C` com orçamento `|A| ≤ K` por greedy dá `≥ (1 − 1/e) ≈ 63,2%` do ótimo (Nemhauser, Wolsey &
  Fisher 1978);
- a variante **facility location** (`f(S) = Σ_r max_{a∈S} u(a,r)`) também é submodular e o mesmo greedy vale
  (Cornuejols, Fisher & Nemhauser 1977);
- greedy preguiçoso (CELF) explora a submodularidade para não reavaliar tudo (Leskovec et al. 2007);
- o survey de Krause & Golovin (2014) cobre as variantes (matroid, cobertura, facility location) e as
  garantias que sobrevivem a restrições.

**Veredito: LOAD-BEARING.** O teorema é exatamente o que se quer: garante que selecionar assets por ganho
marginal é perto do ótimo, e que a ordem de seleção importa menos que a cobertura. Mas as premissas — sem
conflito, acerto determinístico — são falsas no jazz, e é aí que a coisa quebra.

### 3.3 Conflito quebra a submodularidade — e é aqui que o discurso do dono é bullshit

No jazz, métodos da mesma intenção são tentados em ordem de prioridade e o **primeiro aplicável vence**
(`methods.go:107` ordena por `Priority`; `brain.go:268-272` percorre e para no primeiro `mhApplicable`). A
cobertura de uma **lista ordenada** não é a cobertura de um conjunto: se um asset novo é inserido no meio, ele
**rouba** pedidos dos assets de baixo, e a cobertura pode **cair**.

Formalmente: seja `S_a^< = S_a \ ∪_{b<a} S_b` a região exclusiva do asset na posição `a`. Para uma lista
`L`, `C(L) = Σ_a q_a · P(S_a^<)`, onde `q_a` é a probabilidade de acertar naquela região. **Anexar** ao fim
é seguro (`Δ ≥ 0`, a região exclusiva é não-negativa). **Inserir** na posição `i` transfere a região
`S_new ∩ (∪_{b≥i} S_b)` dos assets de baixo para o novo; a cobertura só não cai se `q_new(r) ≥ q_old(r)`
para todo `r` nessa região. Isso é um **teste de dominância**, não uma heurística.

Ou seja: a afirmação "como cobertura é submodular, basta ir empilhando" **é bullshit e aqui está o porquê** —
a função que o código realmente implementa não é a função de cobertura; é uma composição ordenada, e a
submodularidade não sobrevive à inserção. A consequência prática é boa: **anexar é grátis; promover é
perigoso**. A política (§7) explora exatamente essa assimetria.

### 3.4 O piso de medição: por que "cobertura marginal por asset" não é mensurável no Dell

`Δ` é uma probabilidade. Com `n` pedidos no log e `X` novos cobertos, `Δ̂ = X/n` tem erro padrão
`√(Δ(1−Δ)/n)`. Para detectar `Δ` com 95% de confiança e 20% de erro relativo:

```
n ≈ (1,96/0,20)² · (1−Δ)/Δ ≈ 96/Δ
```

| Δ | n necessário |
|---|---|
| 5% | ~1.920 |
| 1% | ~9.600 |
| 0,5% | ~19.200 |
| 0,1% | ~96.000 |

Um usuário único no Dell gera, digamos, ~1.000 pedidos/mês (estimativa). O piso de detecção é **~10% de
cobertura marginal** — nenhum asset individual chega perto. Mesmo agregando 50 classes, o piso por classe é
~2%. **Conclusão dura: no Dell de um usuário, cobertura marginal por asset é ruído.** O que é mensurável é:

1. **win-rate condicional ao disparo** — se o asset disparou `k` vezes, a incerteza é ~`1/√k` (Wilson;
   Brown, Cai & DasGupta 2001), e `k=20` já dá um intervalo utilizável; e
2. **cobertura por classe**, com `n` grande o suficiente e prior hierárquico entre classes.

É a mesma lógica da regra de Chow (1970) para abstenção: rejeitar quando a evidência é fraca; aqui, "rejeitar"
é não promover o asset, e o custo de rejeição é o custo de continuar usando o incumbente.

Isso muda o desenho do portão: não se promove um asset por "ele cobre 0,3%"; promove-se por "ele não quebrou
nenhum caso do guard set e ganhou os casos que devia", e a cobertura é auditada por classe, não por asset.

### 3.5 O ponto ótimo é econômico, não matemático

O valor esperado de um asset, por horizonte `L` meses:

```
EV = Δ · N_mês · t_salvo · L  −  C_setup  −  C_manut · L
```

com `N_mês` pedidos/mês, `t_salvo` segundos salvos por pedido coberto (uma chamada de modelo evitada: 20–60 s
medidos em [offline-on-device-intelligence.md](offline-on-device-intelligence.md)), `C_setup` o custo de
forjar+validar+revisar e `C_manut` o custo mensal de reverificação. O ponto de parada é

```
Δ* = (C_setup/L + C_manut) / (N_mês · t_salvo)
```

`t_salvo` é uma **média da mistura**, não uma constante: ~30 s para o pedido que iria ao modelo, ~0 para o que
já cai num fast path (`intents.go`), e **mais** que 30 s para o que hoje falha e vira pergunta. A conta abaixo
usa 30 s como caso conservador para o primeiro tipo; se a maioria dos pedidos cobertos já é fast path, o piso
sobe e a política fica mais restritiva, não menos.

Com `C_setup` = 30–60 min (estimativa), `L` = 12 meses, `C_manut` = 10 min/mês (estimativa), `t_salvo` = 30 s:

| N_mês | Δ* (payback em 12 meses) |
|---|---|
| 1.000 (usuário único Dell) | **2,5–3,0%** |
| 10.000 (base instalada pequena) | **0,25–0,30%** |
| 100.000 | **0,03%** |

Agora cruze com as curvas de cobertura:

- **Nível de plano** (50 intenções, Zipf 1,0): os 10 primeiros cobrem 65% → média 6,5% por asset. **Acima do
  piso de 2,5–3%.** Vale forjar.
- **Nível de string** (AOL): os 100 primeiros cobrem 8,6% → média 0,086% por asset. **30× abaixo do piso.**
  Não vale.

Isto é a resposta quantitativa à pergunta "qual o ponto ótimo": **não há um número universal; há um número em
função do volume de tráfego e do custo de verificação.** No Dell de um usuário, o ótimo é concentrar
profundidade em ~10–20 intenções (3–6 métodos cada, com fallback de ambiente) e deixar as demais com 1–2 —
total ~100–250 métodos; numa base instalada de 10 mil usuários, ~1.000–3.000 assets passam a pagar. Nunca 10
mil num alvo de 1 CPU e 2 GB — não por matemática de cobertura, mas por manutenção (§4.7) e boot (§5.2).

**Veredito: LOAD-BEARING.** A conta é elementar e é a única que responde à pergunta. Ela também mostra que o
"retorno crescente por dobra" da §2.1 é irrelevante para a decisão: o que decide é o custo por asset, e ele
não cai com a escala da biblioteca.

---

## 4. Forjar muitos assets bons sem veneno

### 4.1 A fábrica e o portão: o que existe de verdade

O blueprint está em `docs/design/offline-brain.md:82-84`: toda tarefa concluída **com verificação** gera um
método candidato com `{sessão, modelo}` de proveniência; promoção só com **2 execuções verificadas em
contextos distintos + dry-run OK + revisão `jazz improve --promote`**. A proposta operacional está em
[offline-on-device-intelligence.md](offline-on-device-intelligence.md) §4.A (`jazz forge`), ainda **não
implementada**.

Correções honestas, porque o discurso corrente as pressupõe implementadas:

- **`provenance.go` não é proveniência de asset.** É proveniência de *execução*: marca se um comando rodou
  local ou remoto (`provenance.go:8-14`). Útil, mas não tem nada a ver com "de onde veio este método".
- **Não há asset provenance no código.** Só a linha de design acima e a proposta `{sessão, modelo, wins}`.
- **Não há win-rate de asset.** O que existe é win-rate de **modelo** (`scores.go:30`, `bgScore.value()` em
  `scores.go:49`: Laplace + mínimo de 3 observações) e de **plano em cache** (`plancache.go:405`,
  `pcScore` com `wins++/fails++` e evicção em 2 falhas, `plancache.go:46`). O blueprint é real, mas é de
  plano, não de método.
- **O "gate" de 2 execuções é o elo fraco**, e a §4.2 mostra por quê.

**Veredito: LOAD-BEARING como desenho; não implementado.** A fábrica é a peça certa. O problema não é a
quantidade de assets que ela gera; é a **força do verificador** que decide o que passa.

Há um critério teórico útil para decidir **o que** forjar, e ele não é o wake-sleep do DreamCoder: é o
**MDL** (Rissanen 1978). Uma abstração vale se comprime o corpus:

```
ΔL(a) = L(D) − L(D | a) − L(a)
```

Para métodos, `L(a)` é o JSON do método e `L(D|a)` é o custo de descrever os planos que ele substitui. Um
método que substitui 40 passos de plano por 1 chamada comprime; um método que só renomeia o que uma capacidade
já faz não comprime — e é candidato a virar **capacidade**, não método. DreamCoder (Ellis et al. 2020) usa o
mesmo critério para crescer uma biblioteca de abstrações e é a melhor analogia disponível; o "wake-sleep" e a
busca neural **não** se aplicam a JSON determinístico. **METÁFORA ÚTIL** com uma fórmula carregável.

### 4.2 Força do verificador: o número que muda tudo

Classifiquei os 103 métodos pelo verificador mais forte que declaram:

| Classe de verificador | Métodos | Exemplos | O que prova |
|---|---|---|---|
| **Efeito** (`file_exists`, `file_gone`, `tests_passed`, `script_exit_zero`) | **13 (12,6%)** | `create.file`, `delete_file.op`, `run_tests.detected`, `rename.op` | que o estado pretendido existe |
| **Vivacidade** (`cmd_ran`, `exit_zero`, `script_ok`, `read_ok`, `status_obtained`, `profile_present`) | **62 (60,2%)** | `list_ports.ss`, `port_owner.check`, `explain.api_map` | que algo rodou |
| **Nenhum** | **28 (27,2%)** | `connect_host.resolve`, `explain.ask`, `git_stash.not_repo` | nada (terminam `unverified` por desenho) |

`cmd_ran` significa literalmente "exit não é 126/127" (`methods.go:1709`): um comando que falha com exit 1
"passa". `exit_zero` é melhor, mas não prova efeito: `git stash` sem nada para guardar sai 0. E "efeito" aqui
é generoso em alguns casos: `rename.op` checa que o destino existe, não que a origem sumiu.

Se um método ruim passa num verificador fraco com probabilidade `q`, exigir `k` execuções independentes dá
`q^k`. Para `k=2`:

| q (verificador) | q² | falsos promovidos em 1.000 candidatos |
|---|---|---|
| 0,9 (`cmd_ran`) | 0,81 | 810 |
| 0,5 (`exit_zero`) | 0,25 | 250 |
| 0,1 (efeito, com fixture frouxa) | 0,01 | 10 |
| 0,02 (efeito, fixture estrita) | 0,0004 | 0,4 |

Para uma taxa de promoção falsa ≤1% com `k=2`, é preciso `q ≤ 10%`. **Só os 13 métodos com verificador de
efeito qualificam.** E as execuções não são independentes: mesma fixture, mesmo ambiente, mesmo corpus —
o `k` efetivo é menor que 2. A afirmação "2 execuções verificadas é um portão" **é bullshit e aqui está o
porquê**: para 87% dos métodos, ela é um smoke test, não uma verificação.

**Veredito: LOAD-BEARING.** É o gargalo real de "muitos assets bons". A fábrica pode gerar 10 mil métodos; o
sistema não consegue **saber** que 10 mil estão certos. O número de assets bons é limitado pelo número de
verificadores fortes, não pela capacidade de geração.

Há uma saída parcial, e ela tem nome: **weak supervision**. O LLM que forja é uma *labeling function*
imperfeita; o verificador determinístico é o *label model*. Snorkel (Ratner et al. 2019) mostra que dá para
estimar a acurácia de fontes fracas pelo **acordo entre fontes independentes** — duas gerações que concordam
têm erro menor que uma. Isso dá base teórica ao "2 contextos distintos" do design, mas com uma condição dura:
as fontes têm de ser **independentes**. Duas chamadas ao mesmo modelo, com o mesmo prompt e a mesma fixture,
não são duas fontes; são uma com ruído. Independência real = modelo diferente, ou fixture diferente, ou
temperatura/prompt que force caminhos distintos. Sem isso, `q^k` não se aplica e o acordo é teatro.

### 4.3 Dedup sem embedding: o que é real e o que é conversa

**MinHash** (Broder 1997): para conjuntos `A`, `B`, a probabilidade de colisão do mínimo de uma permutação
aleatória é a Jaccard `|A∩B|/|A∪B|`. Com `b` bandas de `r` hashes, a probabilidade de colisão é
`1 − (1 − s^r)^b`; o limiar de 50% fica em `s ≈ (1/b)^{1/r}`. Para `t = 0,8`, `b = 10`, `r = 10` (100
hashes, ~400 B por exemplo). **SimHash** (Charikar 2002; Manku et al. 2007) dá um fingerprint de 64 bits e
distância de Hamming ≤3 para quase-duplicata.

O que isso resolve: **quase-duplicata lexical**. O que isso **não** resolve: paráfrase. "Abra o parser.go",
"me mostra o parser.go" e "quero ver o parser.go" têm Jaccard de 3-gramas baixo (~0,2–0,4, estimativa) e
passam pelo MinHash como distintos. Dizer que MinHash faz "dedup semântico sem embedding" **é bullshit e aqui
está o porquê**: MinHash mede sobreposição de shingles, não significado; o nome "semântico" é apropriação
indevida.

O dedup que funciona sem embedding é outro: **assinatura de parse**. Rode `nuGet().Parse` nos dois exemplos;
se dão a mesma `(intent, conjunto de slots, kinds de entidade)`, são o mesmo asset **para o modelo**, mesmo
que as palavras sejam diferentes. Foi o que medi: 100 strings → 55 assinaturas. É grosseiro (não distingue
"abre o a.go" de "abre o b.go" além do slot), mas é exatamente o espaço em que o NB e as regras decidem.

Receita de três estágios, barata e suficiente:

1. **hash exato** (SHA-256 do texto canônico) → remove duplicata literal;
2. **MinHash Jaccard ≥ 0,85** → remove quase-duplicata lexical (conservador: paráfrase fica);
3. **assinatura de parse igual + mesma intenção** → remove redundância *para o classificador*, marcando o
   exemplo como "redundante" em vez de apagar (o NB ganha com paráfrase; só perde com duplicata exata).

Não super-deduplique: para o NB, paráfrases são regularização; duplicatas exatas inflam contagens e o prior.
O limiar deve ser alto (0,85), não 0,5.

O repo já tem o precedente certo: `pcJaccard` (`plancache.go:462`) com limiar 0,7 para fundir planos
(`plancache.go:431`). É a mesma ideia, no espaço de keywords, e já roda sem embedding.

**Veredito: LOAD-BEARING para quase-duplicata; METÁFORA ÚTIL para "diversidade semântica"; VIAGEM para
embeddings** — não cabem no orçamento e não foram necessários para o que o repo já faz.

### 4.4 Diversidade e seleção: facility location, com o aviso certo

Se você tem um pool de `M` candidatos e quer escolher `K` que maximizem a cobertura do espaço de paráfrases,
o problema é facility location:

```
f(S) = Σ_{x∈pool} max_{s∈S} sim(x, s)
```

submodular, greedy 1−1/e (Cornuejols 1977; Nemhauser 1978). O greedy preguiçoso (CELF, Leskovec 2007) evita
reavaliar todos a cada passo. A implementação em Go é ~80 linhas com `sort` e um heap.

O aviso honesto: **`f` garante cobertura do pool, não da realidade.** O pool foi gerado pelo mesmo LLM que
tem os mesmos vieses; diversidade no pool não inventa modos ausentes. A diversidade que importa vem da
**análise de erro no conjunto cego** e do guard set, não da seleção submodular. Use facility location para
**não desperdiçar** verificação com 40 paráfrases da mesma frase; não para decidir o que o usuário pede.

**Veredito: LOAD-BEARING, opcional.** É a ferramenta certa para o problema errado se usada sozinha.

### 4.5 Portão held-out em escala: o que a estatística realmente permite

Com `M` candidatos testados a `α = 0,05`, o número esperado de falsos positivos é `0,05·M`: 50 em 1.000.
Bonferroni (`α/M = 5e-5`) exige amostras enormes; Benjamini–Hochberg (1995) controla a **proporção** de
falsas descobertas em `q`, é mais poderoso, mas exige `p-valores` e dependência controlada. Para assets, a
pergunta não é "é melhor que o acaso?" — é "**não é pior que o incumbente?**". O teste certo é
**não-inferioridade** num **guard set**:

```
promover  ⇔  IC95%(win_novo − win_incumbente) > −δ   e   nenhum caso do guard set regride
```

Isso não sofre a explosão de múltiplas comparações porque não está caçando significância; está **guardando
contra regressão**. É a mesma disciplina de "fix set / guard set" de
[metaharness-self-improvement.md](metaharness-self-improvement.md), e é o que o `pcScore` já faz de forma
bruta (2 falhas → evicção).

O **winner's curse** é o outro lado: selecionar o máximo de `M` estimativas ruidosas superestima o ganho
(Cawley & Talbot 2010). Se você testa 1.000 candidatos e promove o melhor, o ganho medido no mesmo conjunto
é enviesado para cima; no conjunto seguinte, ele encolhe. A correção é sempre validar o promovido num
conjunto **não usado na seleção** — o `nlu/blind_test.json` é exatamente isso, e deve continuar intocado.

E o piso de resolução: o conjunto cego tem `n=100`; 90% de acerto dá IC95% Wilson de **[82,6%, 94,5%]**
(meia-largura ~6 pp). **Não é possível detectar uma melhora de 2 pp com 100 itens**; seriam necessários
~2.400. Portanto o portão não pode ser "a acurácia do cego subiu 2 pp". Tem de ser "não quebrou nenhum item
do guard set" + "corrigiu o item alvo". É menos glamouroso e é o único estatisticamente honesto com `n=100`.

**Veredito: LOAD-BEARING.** A estatística é exata e condena o portão ingênuo ("testa muitos, fica com os
vencedores"). O portão por não-inferioridade + guard set é implementável e barato.

### 4.6 Win-rate e poda: a parte mais barata e mais subestimada

Com posterior Beta(1+w, 1+l) (Laplace), o limite inferior de 95% após `w` vitórias e `l` derrotas:

| w | l | limite inferior 5% |
|---|---|---|
| 3 | 0 | 0,473 |
| 5 | 0 | 0,607 |
| 8 | 0 | 0,717 |
| 8 | 1 | 0,606 |
| 10 | 2 | 0,590 |

Ou seja: **3 vitórias não bastam** para afirmar "melhor que moeda" (o limite inferior fica abaixo de 0,5); 5
vitórias seguidas bastam. O design atual fala em "3 confirmações iguais"
([offline-brain-research.md](offline-brain-research.md):364) — está no limite; 5 é o número defensável.

Para parar cedo, **SPRT** (Wald 1945): com `H0: p = p0`, `H1: p = p1`, limiares
`A = log((1−β)/α)`, `B = log(β/(1−α))`, o número esperado de amostras é ~`(p1−p0)^-2`; para `p0=0,5`,
`p1=0,8`, `α=β=0,05`, ~20–30 observações (estimativa). Implementável em ~40 linhas de Go, e é o que permite **não**
gastar 100 fixtures em cada um dos 1.000 candidatos.

**Successive halving** (Jamieson & Talwalkar 2016) é o multiplicador da fábrica: avalie 1.000 candidatos em 1
fixture, mantenha os 500 melhores, dobre para 2, mantenha 250, e assim por diante até 100 fixtures nos ~7
finalistas. Custo `O(n log n)` em vez de `O(n·F)`: no esquema uniforme (100 fixtures × 1.000 candidatos =
100 mil avaliações), o halving gasta ~7,6 mil — **~13× menos** para a mesma decisão final.

**Bradley–Terry** (1952) ranqueia `M` assets por comparações par a par (`P(a vence b) = θ_a/(θ_a+θ_b)`), mas
exige `O(M²)` comparações; com 50 intenções e 2–3 métodos cada, o ranqueamento contra o incumbente basta.
**METÁFORA ÚTIL**, não ferramenta.

O repo já tem os dois padrões: `bgScore` (Laplace + mínimo 3 observações, `scores.go:30-55`) para modelos e
`pcScore` (wins/fails + evicção em 2 falhas, `plancache.go:405`) para planos. **Estender para métodos é
copiar o padrão, não inventar.**

**Veredito: LOAD-BEARING.** É a peça de menor esforço e maior impacto de todo o documento.

### 4.7 Aposentadoria de stale: a matemática é de manutenção, não de drift

"Stale" no jazz não é deriva estatística de conceito (Gama et al. 2014) — é **ambiente que mudou**:
`docker-compose` v1 → `docker compose`, `service` → `systemctl`, `pip` → `uv`, `npm` → `pnpm`. O código já
captura binários ausentes (`Needs`/`cbAvailable`, `caps.go:159`) e fatos (`project.test`, `bin.*`,
`brain.go:225-257`), mas **não captura semântica mudada**: o binário existe, o comando roda, o resultado está
errado.

O modelo certo é de **confiabilidade/manutenção**, não de ADWIN. Se a probabilidade de apodrecer por mês é
`h`, a vida útil esperada é `1/h`; com `h = 5%/mês`, meia-vida ~14 meses. O custo de manutenção é **linear no
tamanho da pilha**:

```
M* = B / (h · c)
```

com `B` = orçamento de reverificações/mês e `c` = custo por reverificação. Com `B = 20` e `h = 5%`,
`M* = 400`. Esse é o teto de manutenção de um maintainer solo — e coincide com o teto de métodos da §7. Com
10.000 assets, seriam 500 reverificações/mês: **inviável**, independentemente de CPU e RAM.

ADWIN (Bifet & Gavaldà 2007) é a ferramenta certa para detectar mudança em fluxo com muitas amostras; com
~20 disparos por asset, não há amostra para um teste de janela. **METÁFORA ÚTIL**; o que funciona é
decaimento exponencial do win-rate (meia-vida ~90 dias) + TTL + **gatilho de mudança de ambiente** (hash do
`epProfile`).

**Veredito: LOAD-BEARING como conta de manutenção; METÁFORA ÚTIL para ADWIN.**

---

## 5. Risco da pilha grande

### 5.1 Conflito de match: dois métodos para a mesma intenção

**No NLU.** As regras competem **globalmente**: cada intenção escolhe sua melhor regra, com
`score = 10·|All| + W + 5·(Any casou)` (`nlu.go:871-876`), e o resultado é ordenado por score
(`nlu.go:887`). Não há margem por intenção nem desempate por prioridade declarada; empate cai na ordem do
JSON. Consequência: **uma regra nova e mais específica rouba tráfego** de uma intenção existente, e o único
portão é `nuMinConf = 0.6` (`nlu.go:45`, `nlu.go:63`). Não há detector de conflito.

**Nos métodos.** Prioridade total, primeiro aplicável vence (`methods.go:107`, `brain.go:268-272`); empate cai
na ordem alfabética do nome do arquivo (porque `mhLibrary` itera `ReadDir`, que é ordenado). Inserir um método
com prioridade alta e pré-condição frouxa **sombreia** o existente; inserir com prioridade baixa pode torná-lo
**código morto** (nunca aplicável). Nenhum dos dois é detectado hoje.

O teste certo é **shadow test**, e ele é barato: para cada fixture do corpus, rode a biblioteca com e sem o
candidato e compare **qual método venceu** e **qual foi o resultado**. Se algum fixture que passava mudou de
vencedor ou regrediu, rejeite. Custo `O(F·M)` por candidato — com `F=300` fixtures e `M=250` métodos, ~75 mil
comparações, que em Go é <1 s. É o portão que falta.

**Veredito: LOAD-BEARING.** O risco é real, cresce com a pilha, e o código atual não o mede.

### 5.2 Custo de boot no 32-bit: medido, e é o teto de breadth

Medições desta sessão (amd64) e extrapolação para o Dell (fator ~3,2):

| cenário | tabela NB | `nuTrain` amd64 | `nuTrain` Dell (est.) | cold start Dell (est.) |
|---|---|---|---|---|
| 50 intenções (hoje) | 1,6 MB | 40 ms | ~130 ms (est.) | ~130 ms (medido) |
| 100 | 3,2 MB | 99 ms | ~320 ms | ~320 ms |
| 200 | 6,4 MB | 162 ms | ~520 ms | ~520 ms |
| 400 | 12,8 MB | 358 ms | ~1,1 s | ~1,1 s |
| 800 | 25,6 MB | 472–542 ms | ~1,5–1,7 s | ~1,5–1,7 s |

O p50 do eval é **14 ms** (Dell medido). Um boot de 1,1 s é 80× o p50; 1,7 s é 120×. A memória em si não é o
problema (25,6 MB num processo de 2 GB, e o binário já usa 59 MB de RSS no eval); o **tempo** é. E o custo é
por **classe**, não por exemplo nem por método: 250 métodos custam ~7 ms de parse (10.000 custariam ~300 ms),
100 intenções custam ~99 ms de treino (Dell: ~320 ms).

A distinção de Denning (1968) esclarece o que está sendo pago: no **boot**, o custo é o tamanho da biblioteca
(todo o JSON é parseado); no **runtime**, o custo é o *working set* — os 2–3 métodos da intenção casada. Uma
biblioteca de 10 mil métodos tem working set pequeno e boot grande; uma de 250 tem os dois pequenos. É por isso
que o teto é de boot, não de memória.

Duas saídas, ambas alinhadas com a filosofia "inteligência em tempo de compilação":

1. **Pré-computar a tabela NB** (`nlu/nb.bin`, já proposto em
   [offline-brain-research.md](offline-brain-research.md):298): `go generate` treina, o runtime só lê. O boot
   vira I/O de 1,6–6,4 MB (~poucos ms), e o teto de exemplos sobe muito. Custo: um passo de build e um hash
   de invalidação.
2. **Cap duro de intenções** em 64 (96 com tabela pré-computada). O ganho de cobertura de novas intenções
   segue a curva de plano (§2.3), que satura rápido; o custo de boot é linear e visível.

**Veredito: LOAD-BEARING.** É a restrição medida mais dura do documento, e ela é sobre **breadth**, não sobre
profundidade.

### 5.3 Staleness: o imposto que cresce com a pilha

Cada método é uma promessa sobre um ambiente. O repo já tem `Needs` e `Pre`, que detectam **ausência** de
binário, mas não **mudança de semântica**. Exemplos reais: `docker-compose` (v1) vs `docker compose` (v2);
`service` vs `systemctl`; `python3 -m pip` vs `uv pip`; `npm test` vs `pnpm test`. Um método que usa o
comando antigo continua "verificando" com `exit_zero` e falha silenciosamente no efeito.

O imposto é `M · h` reverificações/mês (§4.7). A pilha não só fica velha; ela **acumula** dívida de
verificação numa taxa proporcional ao seu tamanho. É o argumento mais forte contra "10 mil assets": não é que
não caibam; é que não podem ser mantidos.

### 5.4 Overfitting ao corpus e Goodhart

O corpus tem 644 exemplos e 50 intenções; o cego tem 100 itens e 90% de acerto. Com `n=100`, o IC é ±5,9 pp
(§4.5). Qualquer "ganho" menor que isso é ruído. Se o `forge` gera exemplos a partir das mesmas sessões que
alimentam o eval, há **vazamento**: o número sobe e a realidade não.

Goodhart (Manheim & Garrabrant 2018) é o risco sistêmico: otimizar `no_llm_rate` (`evaloffline.go:98`) sem
`false_success` (`evaloffline.go:94`) como restrição dura produz um cérebro que responde tudo e erra mais.
O repo já trata `false_success = 0` como constraint (resultado medido: 0 em 39/39), e essa é a disciplina
certa: **cobertura é objetivo; correção é restrição.**

**Veredito: LOAD-BEARING.** O portão tem de ser assimétrico: nunca trocar precisão por cobertura.

### 5.5 Veneno: o que o código filtra e o que não filtra

`h2PlanPoison` (`hardening2.go:976`) filtra **injeção de prompt** em planos (texto que manda o agente
exfiltrar etc.). Isso é veneno de segurança, não veneno **semântico**. Um método forjado que roda
`rm -rf` no diretório errado, ou que "verifica" com `exit_zero`, passa pelo filtro de injeção e pelo gate de
2 execuções.

A assimetria de perdas é o que manda: o ganho de um asset é linear (segundos salvos), a perda de um asset ruim
pode ser ruína (repo destruído, credencial vazada). Com perda assimétrica, o valor esperado é negativo a
qualquer `P(errado)` não trivial. Por isso a regra é **precisão primeiro, cobertura depois**, e por isso
`false_success` é constraint e não métrica.

---

## 6. Veredito: o moat é a fábrica + a cobertura medida, não a pilha

### Onde o dono tem razão

1. **A cauda é real e medida.** A AOL e a TraceLab confirmam: poucos itens cobrem muito, e a cauda é longa.
   Ignorar a cauda é pior do que superestimá-la.
2. **Por dobra de investimento, a cobertura de string cresce** (+1,3 → +13,6 pp por dobra). A intuição
   "continuar adicionando paga" não é ingênua; é empiricamente verdadeira no espaço de strings.
3. **Inteligência em tempo de compilação é a arquitetura certa** para o Dell: 0 tokens, 14 ms de p50, funciona
   offline. O LLM forte forjando assets e o runtime só interpretando é a única conta que fecha em 1 CPU/2 GB.
4. **A fábrica é a peça diferenciadora.** Ninguém gera 10 mil métodos que passam num verificador forte; gerar
   10 mil métodos é trivial.

### Onde ele exagera

1. **"~30% repetidos"** — é assistente de IoT, com similaridade definida pelo próprio classificador; em código,
   a evidência direta é ~7%. E 16–23% de todas as consultas são repetição do mesmo usuário; num CLI
   single-user, toda repetição é local → cache, não biblioteca.
2. **"A cauda é infinita"** — Heaps: `V(n) ∝ n^0,5–0,6`, sublinear; por usuário, satura. O número que importa
   é o custo por asset, não a existência de cauda.
3. **"Mais assets = mais inteligência"** — falso sob orçamento fixo de verificação. 87% dos métodos atuais só
   provam que algo rodou; um asset novo com verificador fraco tem EV negativo.
4. **"Os assets são o moat"** — um JSON de método é copiável em segundos. O moat é o **medidor de cobertura**
   (saber o que falta), o **portão** (saber o que é bom) e a **fábrica** (transformar vitória online em asset
   offline), nessa ordem.

### O que é bullshit

- **"Dedup semântico sem embedding" via MinHash.** MinHash é lexical; paráfrase passa. O nome "semântico" é
  indevido. (O dedup por assinatura de parse é real, mas é sintático, não semântico.)
- **"2 execuções verificadas" como portão** quando 87% dos verificadores são `cmd_ran`/`exit_zero`. É smoke
  test, não verificação.
- **"Porrada de assets" como estratégia.** Sem medidor de cobertura e teste de conflito, é uma afirmação
  infalsificável: qualquer resultado confirma. Estratégia é o que se pode perder.
- **"Cobertura é submodular, então basta empilhar."** A função que o código executa é ordenada; inserção pode
  regredir. A submodularidade vale para o conjunto, não para a lista.

### O argumento que resolve a disputa: composição > enumeração

Um **método** enumera um plano. Uma **capacidade** é um passo que o planner (`brExecute`, `brain.go:261`)
compõe com outros. O espaço de sequências cresce **combinatorialmente** com o número de capacidades e
**linearmente** com o número de métodos: com 15 capacidades, sequências de até 5 passos são 15⁵ ≈ 760 mil
(a maioria inválida — o ponto é a ordem de crescimento, não a contagem); com 30, 24 milhões. Com ~15
capacidades e 103 métodos, o sistema já tem mais planos *possíveis* do que métodos; com 30 capacidades, muito
mais.

Portanto: **breadth pertence às capacidades; depth pertence aos métodos; e a cauda de planos se ataca com
composição, não com enumeração.** "Porrada de assets" deveria significar "porrada de capacidades + métodos que
as compõem", não "porrada de métodos". Essa é a correção mais importante do documento — e é a que o dono
aceitaria, porque preserva a intuição (mais cobertura) e muda o objeto (composição em vez de lista).

---

## 7. Política concreta para o Dell

### 7.1 Caps por classe

| Classe | Cap | Hoje | Racional |
|---|---|---|---|
| Intenções | **64** (96 com tabela NB pré-computada) | 50 | 32 KB e ~3 ms de boot por classe; 100 intenções ~320 ms no Dell |
| Métodos por intenção | **2–3 típico, 6 duro** | 2,1 médio, 5 máx | profundidade é barata; o custo é conflito/teste, não CPU |
| Métodos totais | **250** | 103 | `M* = B/(h·c) = 400`; 250 deixa folga para reverificação |
| Exemplos por intenção | **20–40, duro 40** | 12,9 médio | ~43 µs/exemplo; 2.000 exemplos ~280 ms Dell (ou 0 com tabela pré-computada) |
| Exemplos totais | **2.000** | 644 | cabe no orçamento de treino e no corpus de teste |
| Entradas de léxico | ~2.000, podadas por uso | 771 | custo desprezível; podar as que nunca casam em 90 dias |
| Planos em cache | **100/projeto** (já existe) | `pcMaxPlans` | precedente do repo (`plancache.go:45`) |
| Capacidades | **sem cap** (crescer) | ~15 | é onde breadth deve morar (§6) |

O total fica em ~3.000 assets (64 intenções + ~250 métodos + ~2.000 exemplos + léxico), mas o número que
importa para a economia é o de **métodos** (~250): exemplos e léxico não passam pelo portão caro e custam
quase nada. É o ponto em que a cobertura marginal de plano cai abaixo de ~2,5% e a manutenção passa de 20
reverificações/mês (estimativas da §3.5/§4.7) — ou seja, o ponto em que o retorno deixa de pagar o imposto.

### 7.2 Critério de parada (por classe e global)

Há **três tipos de asset** com critérios diferentes; misturá-los é o erro que produz tanto a pilha inútil
quanto a parada cedo demais.

**(a) Plano novo (método que cobre uma intenção ou um shape de slots que ninguém cobria).**
Parar quando `Δ < Δ*` da §3.5 — **2,5–3%** no Dell single-user, 0,25–0,3% com 10 mil usuários. Como a
distribuição de planos satura (top 10 ≈ 65%), isso naturalmente limita a profundidade a ~10–20 intenções.
É o critério que responde "quantos assets".

**(b) Fallback de ambiente (mesma intenção, `Needs`/`Pre` distintos: `bin.systemctl` vs `bin.service`,
`bin.podman` vs `bin.docker`, `bin.uv` vs `bin.pip`).** Não conta para cobertura marginal — no ambiente atual
ele cobre 0% que o primário não cobre. Conta para **robustez**: o valor é a opção de não falhar quando o
ambiente muda. Critério: cobre um `epProfile` distinto e tem verificador de efeito; **cap 2 por intenção**.
Sem esse cap, "fallback" vira desculpa para empilhar.

**(c) Exemplo NLU.** Parar quando 20 exemplos candidatos não corrigem nenhum item do fix set (cego/eval) e
não aumentam a contagem de assinaturas de parse distintas. O critério é **fix set vazio**, não cobertura
agregada — com `n=100` no cego, ganho agregado < 5,9 pp é ruído (§4.5).

**Paradas transversais**, qualquer uma basta:

1. **Win-rate**: limite inferior de 95% ≤ 0,5 (Beta(1+w, 1+l)), ou SPRT aceita `H0` (não é melhor que o
   incumbente).
2. **Conflito**: o shadow test encontrou qualquer fixture em que o vencedor mudou e o resultado regrediu.
3. **Custo**: `Δ · N_mês · t_salvo < C_manut` (§3.5).

**Parada global**: quando nenhuma classe tem candidato com `Δ ≥ Δ*` **e** os últimos 20 exemplos promoveram
0 correções no fix set. Nesse ponto, o próximo esforço deve ir para **capacidades** e **generalização NLU**,
não para mais métodos.

### 7.3 Portão de promoção

| Requisito | Regra |
|---|---|
| Verificador | **Efeito** (`file_exists`, `file_gone`, `tests_passed`, `script_exit_zero` com artefato esperado) para promoção automática; vivacidade só com revisão manual e ≤1 por intenção |
| Execuções | 2 contextos **distintos** (fixtures diferentes, perfis de ambiente diferentes) **e** 5 vitórias no total (Beta ≥ 0,607) |
| Shadow test | 0 regressões no guard set; candidato deve ser o vencedor **único** em pelo menos 1 fixture |
| Proveniência | `{sessão, modelo, data, wins, fails, verifier}` gravada no asset; asset sem proveniência não promove |
| Conflito | prioridade declarada; inserção no meio exige teste de dominância na região roubada (§3.3) |

### 7.4 Aposentadoria

- **90 dias** sem vitória e sem disparo → quarentena (não executa, fica no arquivo).
- **180 dias** em quarentena → arquivo morto (removido do embed).
- **Mudança de ambiente** (hash do `epProfile` mudou: versão de binário, init system, gerenciador de pacotes)
  → re-verificar todos os métodos daquela classe antes de usar; falha → quarentena.
- **Decaimento**: `w ← 0,5^(dias/90) · w`, idem `l`; o win-rate envelhece.

### 7.5 O que implementar em Go (métrica/impacto/esforço/risco)

**A. `assetcov.go` — medidor de cobertura (o que falta, primeiro).**
Lê sessões via o scanner de `improve_load.go`, roda `nuGet().Parse` em cada pedido, agrega por intenção e por
`(intent, slot-shape)`, calcula `C(k)` e a marginal dos últimos N assets; emite JSON em
`dataDir()/brain/coverage.json`. ~250 linhas. **Métrica**: `marginal_cov_pp` por asset/classe; habilita a §7.2.
**Impacto 5 · Esforço M · Risco baixo** (só leitura).

**B. `assetgate.go` — shadow test + fix/guard sets.**
Recebe um candidato, roda o corpus de fixtures com e sem ele, reporta vencedor e resultado por fixture;
rejeita em qualquer regressão. ~200 linhas + fixtures. **Métrica**: `conflict_rate` (deve ser 0),
`unique_win_fixtures` (deve ser ≥1). **Impacto 5 · Esforço M · Risco baixo.**

**C. `assetscore.go` — win-rate por asset.**
Generaliza `bgScore`/`pcScore`: `{wins, fails, last_win, verifier, provenance}` por asset, Beta inferior,
SPRT, decaimento. ~150 linhas. **Métrica**: `win_rate_lb`, `stale_days`. **Impacto 4 · Esforço S · Risco
baixo.**

**D. Tabela NB pré-computada (`nlu/nb.bin`).**
`go generate` treina `nuTrain`, grava `classes × 2^14 int16` + hash dos exemplos; `nuGet` lê e valida o hash;
fallback para treino se ausente. ~120 linhas + passo de build. **Métrica**: `cold_ms` (de ~130 ms para ~10–20 ms
estimados com 50 classes; permite 2.000 exemplos sem custo de boot). **Impacto 4 · Esforço M · Risco médio**
(tabela stale se o hash não for checado; binário +1,6–6,4 MB).

**E. `forge --dedup`.**
Três estágios: SHA-256 → MinHash (b=10, r=10, Jaccard 0,85, força bruta) → assinatura de parse. ~180 linhas.
**Métrica**: `dup_rate` por lote, `distinct_signatures`. **Impacto 3 · Esforço S · Risco baixo.**

**F. Aposentadoria no `jazz improve`.**
TTL + decaimento + gatilho de `epProfile`; relatório `jazz improve --assets`. ~100 linhas.
**Métrica**: `quarantined`, `reverify_queue`. **Impacto 3 · Esforço S · Risco baixo.**

**G. Checagem de caps em teste, não em runtime.**
`TestAssetCaps` falha o build se intenções > 64, métodos > 250, exemplos > 2.000, ou se um método novo não
tem verificador de efeito e não está marcado `manual`. ~50 linhas. **Métrica**: build quebrado é o gate mais
barato que existe. **Impacto 3 · Esforço S · Risco nenhum.**

Ordem de implementação: **A → B → C → G → F → E → D**. A primeiro porque sem `P(c)` não há cobertura; B
porque sem shadow test a pilha cresce com regressão silenciosa; D por último porque só faz sentido quando o
número de exemplos justificar o passo de build.

### 7.6 O que NÃO construir

- **Embeddings/vetores** para dedup ou retrieval: não cabem no orçamento e o repo já resolve com Jaccard +
  parse (ver [offline-on-device-intelligence.md](offline-on-device-intelligence.md) "não-propostas").
- **LSH/banding** para MinHash: com `n < 10^5`, força bruta é mais rápida que a estrutura e é **exata**; LSH
  é aproximada e tem falso negativo. Complexidade para um regime que o Dell não alcança.
- **10.000 métodos**: manutenção `M·h` inviável (§4.7) e boot desnecessário (§5.2); a cauda de planos se
  ataca por composição (§6).
- **ADWIN / testes de janela por asset**: amostras insuficientes (§4.7).
- **"Dedup semântico"** como serviço: o que existe é quase-duplicata lexical + assinatura sintática; chamar
  de semântico é vender o que não se tem.
- **Assets por string**: a economia da §3.5 mostra que cada um cobre 30× menos do que o piso exige.

---

## 8. Contra-argumentos (steelman) e respostas

**C1. "Um asset que nunca dispara custa quase nada; um asset que falta custa uma chamada de modelo de 20–60 s.
Logo, o EV é positivo mesmo para cobertura minúscula."**
Verdade para o **custo de runtime**; falso para o **custo de verificação e manutenção**, que é pago antes e
depois. A §3.5 mostra que, com 30–60 min de setup e 10 min/mês, a cobertura de equilíbrio é ~2,5–3% no Dell.
Um asset de 0,1% tem EV negativo em 12 meses, mesmo que o runtime seja grátis. Se o asset for **gerado e
nunca verificado**, o custo é ainda maior: ele pode disparar errado (assimetria de perdas, §5.5).

**C2. "A cauda é onde está a diferenciação; os concorrentes cobrem a cabeça."**
Verdade — mas a cauda de **planos** é finita e composível. A cauda de **strings** é enorme e não é onde um
método agrega; é onde o parser agrega. Investir na cauda de strings com métodos é pagar 30× pelo mesmo
resultado. Investir na cauda de planos com **capacidades** é o movimento certo.

**C3. "Mais exemplos sempre melhoram o NLU."**
Até o ponto em que o NB satura: 2^14 baldes fixos, colisões crescentes, prior por classe diluído com mais
classes. Profundidade (exemplos por intenção) ajuda; breadth (intenções novas) dilui. E o custo de boot é por
classe, não por exemplo — o que reforça: **muitos exemplos, poucas intenções**.

**C4. "O portão de 2 execuções é o que o design diz; se está no design, está bom."**
O design não mediu a força dos verificadores. 87% dos métodos atuais só provam que algo rodou. Um
portão é tão forte quanto o seu verificador mais fraco; `q^k` com `q=0,9` e `k=2` promove 81% dos ruins. O
design precisa de uma cláusula de **verificador de efeito obrigatório**, não de mais execuções.

**C5. "Se a base instalada crescer, a economia muda e a pilha se paga."**
Verdade, e é a única circunstância em que "porrada de assets" faz sentido: com 10 mil usuários, o piso
econômico cai para ~0,25% e 1.000–3.000 assets pagam. Mas isso exige **verificação centralizada** (o custo de
setup não é pago 10 mil vezes) e um corpus de fixtures muito maior. É uma decisão de produto, não de código:
enquanto o jazz for single-user no Dell, o teto é ~250 métodos.

---

## 9. Limitações: o que este documento não pode afirmar

- **Não há log de pedidos no repo.** A distribuição `P(c)` do jazz é **desconhecida**. As curvas de intenção
  da §2.3 são hipótese (Zipf), não medição. O primeiro item da política (A) existe para fechar essa lacuna.
- **A AOL é busca web, não código.** A forma da cauda é evidência, não prova; o expoente local (0,27) é de
  strings de busca em 2006.
- **A TraceLab não publica o texto dos pedidos** (privacidade); só as distribuições de ferramentas/comandos.
  Não é possível medir repetição de pedidos nela.
- **O colapso 100→55** foi medido num conjunto cego **estratificado** (~2 por intenção), que é o melhor caso
  para não colapsar; o número real de tráfego deve ser maior, mas não foi medido.
- **As extrapolações para o Dell** usam fator ~3,2 calibrado pelo cold start (~130 ms), não uma razão limpa;
  a variância é grande e o Dell não foi rodado nesta sessão.
- **Os custos de setup/manutenção** (30–60 min, 10 min/mês) são estimativas; a §3.5 mostra a sensibilidade
  (o piso vai de ~1,3% a ~5% conforme o custo).
- **O "~30% repetidos" não foi replicado** em código; o 7% do Transform Code é a melhor evidência direta, e
  também não é um CLI.
- **A força do verificador foi classificada por inspeção dos JSON**, não por execução de cada método; um
  `exit_zero` pode ser forte em contextos específicos (ex.: `go build`), mas a classificação conservadora é a
  correta para um portão.

---

## 10. Referências

**Distribuição de pedidos e reuso**
- Zipf, G. K. (1949). *Human Behavior and the Principle of Least Effort*. Addison-Wesley.
- Heaps, H. S. (1978). *Information Retrieval: Computational and Theoretical Aspects*. Academic Press.
- Silverstein, C., Henzinger, M., Marais, H., & Moricz, M. (1999). Analysis of a very large web search engine
  query log. *SIGIR Forum*, 33(1), 6–12. https://doi.org/10.1145/331403.331405
- Jansen, B. J., & Spink, A. (2006). How are we searching the World Wide Web? *Information Processing &
  Management*, 42(1), 248–263. https://doi.org/10.1016/j.ipm.2004.10.007
- Beitzel, S. M., Jensen, E. C., Chowdhury, A., Frieder, O., & Grossman, D. (2007). Temporal analysis of a
  very large topically categorized web query log. *JASIST*, 58(2), 166–178. https://doi.org/10.1002/asi.20464
- Xie, Y., & O'Hallaron, D. (2002). Locality in search engine queries and its implications for caching.
  *INFOCOM 2002*. https://doi.org/10.1109/INFCOM.2002.1019374
- Petersen, C., Simonsen, J. G., & Lioma, C. (2016). Power law distributions in information retrieval.
  *ACM TOIS*, 34(2), 1–37. https://doi.org/10.1145/2816815
- Clauset, A., Shalizi, C. R., & Newman, M. E. J. (2009). Power-law distributions in empirical data.
  *SIAM Review*, 51(4), 661–703. https://doi.org/10.1137/070710111
- Li, G., Wu, R., & Tan, H. (2025). A plan reuse mechanism for LLM-driven agent. arXiv:2512.21309.
  https://arxiv.org/abs/2512.21309
- Nam, D., et al. (2025). Understanding and supporting how developers prompt for LLM-powered code editing in
  practice. arXiv:2504.20196. https://arxiv.org/abs/2504.20196
- Zhu, K., et al. (2026). TraceLab: Characterizing coding agent workloads for LLM serving. arXiv:2606.30560.
  https://arxiv.org/abs/2606.30560

**Cobertura, seleção e submodularidade**
- Nemhauser, G. L., Wolsey, L. A., & Fisher, M. L. (1978). An analysis of approximations for maximizing
  submodular set functions—I. *Mathematical Programming*, 14, 265–294. https://doi.org/10.1007/BF01588971
- Cornuejols, G., Fisher, M. L., & Nemhauser, G. L. (1977). Location of bank accounts to optimize float.
  *Management Science*, 23(8), 789–810. https://doi.org/10.1287/mnsc.23.8.789
- Krause, A., & Golovin, D. (2014). Submodular function maximization. In *Tractability*, 71–104.
  https://doi.org/10.1017/CBO9781139177801.004
- Leskovec, J., Krause, A., Guestrin, C., Faloutsos, C., VanBriesen, J., & Glance, N. (2007). Cost-effective
  outbreak detection in networks. *KDD 2007*, 420–429. https://doi.org/10.1145/1281192.1281239
- Rissanen, J. (1978). Modeling by shortest data description. *Automatica*, 14(5), 465–471.
  https://doi.org/10.1016/0005-1098(78)90005-5
- Ellis, K., et al. (2020). DreamCoder: Growing generalizable, interpretable knowledge with wake-sleep
  Bayesian program learning. arXiv:2006.08381. https://arxiv.org/abs/2006.08381

**Dedup e near-duplicate**
- Broder, A. Z. (1997). On the resemblance and containment of documents. *SEQUENCES 1997*, 21–29.
  https://doi.org/10.1109/SEQUEN.1997.666900
- Charikar, M. S. (2002). Similarity estimation techniques from rounding algorithms. *STOC 2002*, 380–388.
  https://doi.org/10.1145/509907.509965
- Manku, G. S., Jain, A., & Das Sarma, A. (2007). Detecting near-duplicates for web crawling. *WWW 2007*,
  141–150. https://doi.org/10.1145/1242572.1242592

**Estatística de avaliação e promoção**
- Benjamini, Y., & Hochberg, Y. (1995). Controlling the false discovery rate. *JRSS-B*, 57(1), 289–300.
  https://doi.org/10.1111/j.2517-6161.1995.tb02031.x
- Cawley, G. C., & Talbot, N. L. C. (2010). On over-fitting in model selection and subsequent selection bias
  in performance evaluation. *JMLR*, 11, 2079–2107. https://www.jmlr.org/papers/v11/cawley10a.html
- Wald, A. (1945). Sequential tests of statistical hypotheses. *Annals of Mathematical Statistics*, 16(2),
  117–186. https://doi.org/10.1214/aoms/1177731118
- Bradley, R. A., & Terry, M. E. (1952). Rank analysis of incomplete block designs. *Biometrika*, 39(3–4),
  324–345. https://doi.org/10.1093/biomet/39.3-4.324
- Brown, L. D., Cai, T. T., & DasGupta, A. (2001). Interval estimation for a binomial proportion.
  *Statistical Science*, 16(2), 101–133. https://doi.org/10.1214/ss/1009213286
- Jamieson, K., & Talwalkar, A. (2016). Non-stochastic best arm identification and hyperparameter
  optimization. *AISTATS 2016*, PMLR 51:240–248. https://proceedings.mlr.press/v51/jamieson16.html
- Manheim, D., & Garrabrant, S. (2018). Categorizing variants of Goodhart's law. arXiv:1803.04585.
  https://arxiv.org/abs/1803.04585
- Chow, C. K. (1970). On optimum recognition error and reject tradeoff. *IEEE Trans. Information Theory*,
  16(1), 41–46. https://doi.org/10.1109/TIT.1970.1054406

**Manutenção, drift e sistema**
- Gama, J., Žliobaitė, I., Bifet, A., Pechenizkiy, M., & Bouchachia, A. (2014). A survey on concept drift
  adaptation. *ACM Computing Surveys*, 46(4), 44. https://doi.org/10.1145/2523813
- Bifet, A., & Gavaldà, R. (2007). Learning from time-changing data with adaptive windowing. *SDM 2007*,
  443–448. https://doi.org/10.1137/1.9781611972771.42
- Denning, P. J. (1968). The working set model for program behavior. *Communications of the ACM*, 11(5),
  323–333. https://doi.org/10.1145/363095.363141
- Ratner, A., Bach, S. H., Ehrenberg, H., Fries, J., Wu, S., & Ré, C. (2019). Snorkel: Rapid training data
  creation with weak supervision. *The VLDB Journal*, 29, 709–730.
  https://doi.org/10.1007/s00778-019-00552-1

**No repo**
- `docs/design/offline-brain.md` §7 (promoção de candidatos), §9 (métricas).
- `docs/research/math-offline-brain.md` (calibração, Chow, orçamentos).
- `docs/research/offline-brain-research.md` (projeto do cérebro, `nlu/model.bin`).
- `docs/research/offline-on-device-intelligence.md` §4.A (`jazz forge`).
- `docs/research/tocomas-plan-reuse.md` (AgentReuse, APC).
- `docs/research/metaharness-self-improvement.md` (fix set / guard set).
- `results/dell-0.9-progress.md` (p50 14 ms, RSS 59 MB, 39/39).
