# Matemática do cérebro offline: calibração, informação, suavização, distâncias, orçamentos

Pesquisa de 04/11/2026 para a 0.9.x. Continua [offline-brain-research.md](offline-brain-research.md) (algoritmos e arquitetura) e [low-level-performance.md](low-level-performance.md) (constantes medidas de máquina); este documento cobre o terceiro braço: a **matemática das decisões** — de onde saem `Confidence`, `Score`, margens e limiares, se esses números significam o que dizem, e os orçamentos que os mantêm viáveis no Dell (i386, 1 CPU, 2 GB). Não repete HTN/BT, o pipeline NLU nem o layout dos índices.

Convenções: `file:line` refere-se a `main` @ `e0e3ec3` (branch `harness/wave1` na escrita). *(estimativa)* marca número meu, não medido. "Medido" = medições já reportadas (§4). Status no mapa (§3): **feito** / **parcial** / **só pesquisado**.

---

## 1. Princípios operacionais

**P1 — Confiança declarada não é probabilidade; calibração se mede (ECE/MCE) e se corrige depois.**
Um classificador é calibrado quando `P(acertou | p̂ = p) = p`. A medida padrão é `ECE = Σ_m (|B_m|/n)·|acc(B_m) − conf(B_m)|` sobre bins de confiança, e `MCE = max_m |acc − conf|` no pior bin. `Intent.Confidence` hoje é uma função heurística (0.9, 0.97, `0.75·p`, `0.55+0.45p`, caps 0.5/×0.6 — `nlu.go:922-1034`); ninguém sabe seu ECE. Bins de largura fixa subestimam o erro com amostras pequenas — usar bins por quantis e reportar tamanho por bin. Fonte: Guo et al., *On Calibration of Modern Neural Networks*, ICML 2017 ([arXiv:1706.04599](https://arxiv.org/abs/1706.04599)); Vaicenavicius et al., *Evaluating model calibration in classification*, AISTATS 2019 ([PMLR v89](https://proceedings.mlr.press/v89/vaicenavicius19a.html)).

**P2 — Naive Bayes decide bem e probabiliza mal: separar o ranking da probabilidade.**
Sob perda 0-1 o argmax do NB sobrevive mesmo quando as probabilidades estão quebradas (independência falsa penaliza a magnitude, não a ordem); empiricamente o NB empurra os scores para 0/1 — é o classificador menos calibrado do benchmark clássico. Consequência direta: `predict` (`nlu.go:1398-1441`) pode continuar escolhendo a classe certa enquanto o `p` que ela devolve não autoriza nenhuma decisão de segurança. Fonte: Domingos & Pazzani, *On the Optimality of the Simple Bayesian Classifier under Zero-One Loss*, MLJ 1997 ([doi:10.1023/A:1007413511361](https://doi.org/10.1023/A:1007413511361)); Niculescu-Mizil & Caruana, *Predicting Good Probabilities with Supervised Learning*, ICML 2005 ([doi:10.1145/1102351.1102430](https://doi.org/10.1145/1102351.1102430)); Rennie et al., *Tackling the Poor Assumptions of Naive Bayes Text Classifiers*, ICML 2003 ([PDF](https://cdn.aaai.org/ICML/2003/ICML03-081.pdf)).

**P3 — Correção monotônica nunca muda o argmax: calibrar é grátis e seguro.**
Platt (sigmoide, 2 parâmetros), temperatura (1 parâmetro), beta-calibration (3 parâmetros) e isotônico/PAV (não-paramétrico) são todos mapas crescentes do score — mudam `Confidence`, nunca a classe escolhida nem a ordem dos `Alt`. Logo: o custo de calibrar é uma tabela ou uma sigmoide em runtime O(1), e o risco é zero para a classificação em si; o risco vive inteiro na qualidade dos dados de ajuste. Para NB especificamente, a família beta (`logit(p_cal) = a·ln p̂ + b·ln(1−p̂) + c`) corrige a distorção "scores nos extremos" que o logistic piora. Fonte: Platt, *Probabilistic Outputs for SVMs*, 1999 ([citeseer:10.1.1.41.1639](http://citeseerx.ist.psu.edu/viewdoc/summary?doi=10.1.1.41.1639)); Kull, Silva Filho & Flach, *Beta calibration*, AISTATS 2017 ([PMLR v54](https://proceedings.mlr.press/v54/kull17a.html)); Zadrozny & Elkan, *Transforming classifier scores into accurate multiclass probability estimates*, KDD 2002 ([doi:10.1145/775047.775151](https://doi.org/10.1145/775047.775151)).

**P4 — Abster é uma decisão com custo: limiares saem da curva risco–cobertura, não de constantes.**
A regra ótima de Chow — rejeitar quando `max p < 1−t`, com `t` = custo da rejeição — torna `nuMinConf = 0.6` (`nlu.go:45`) e `brLevel0Min = 0.85` (`brain.go:593`) o que eles são: palpites. O procedimento correto é varrer τ, plotar erro × cobertura (`no_llm_rate`) e escolher o ponto sob constraints operacionais (`false_success = 0`, `false_blocks` ≤ orçamento, perguntas ≈ perguntas necessárias). Idem para as margens de `niTop` (`nameindex.go:710`) e `brPick` (`methods.go:1132`). Fonte: Chow, *On optimum recognition error and reject tradeoff*, IEEE Trans. Inf. Theory 1970 ([doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)); El-Yaniv & Wiener, *On the Foundations of Noise-free Selective Classification*, JMLR 2010 ([jmlr.org/papers/v11/el-yaniv10a](https://www.jmlr.org/papers/v11/el-yaniv10a.html)); Geifman & El-Yaniv, *Selective Classification for DNNs*, NeurIPS 2017 ([arXiv:1705.08500](https://arxiv.org/abs/1705.08500)).

**P5 — Suavização minimax: α = 1/2 (KT/Jeffreys), não 1; e α é hiperparâmetro, não lei.**
`p̂(g|c) = (c_g + α)/(N_c + αV)`: Laplace α=1 superestima o nunca-visto; α=1/2 é o estimador de Jeffreys/Krichevsky–Trofimov, com regret minimax `(V/2N)·log(1/α) + o(1)` para amostras pequenas — exatamente o regime de ~13 exemplos/classe do jazz. `nuTrain` já usa `alpha = 0.5` (`nlu.go:1361`), mas fixo: o valor certo se escolhe por likelihood held-out (LOO), que o próprio `skip` hook (`nlu.go:1327`) já permite. Good-Turing (`P(não-visto) = N₁/N`, contagens reestimadas `r* = (r+1)N_{r+1}/N_r`) é a ferramenta para estimar **massa** de OOV, não probabilidade por feature. Fonte: Krichevsky & Trofimov, *The Performance of Universal Encoding*, IEEE TIT 1981 ([doi:10.1109/TIT.1981.1056331](https://doi.org/10.1109/TIT.1981.1056331)); Good 1953 / Gale & Sampson, *Good-Turing Frequency Estimation Without Tears*, JQL 1995 ([doi:10.1080/09296179508590051](https://doi.org/10.1080/09296179508590051)); Chen & Goodman, *An empirical study of smoothing techniques for language modeling*, Computer Speech & Language 1999 ([doi:10.1006/csla.1999.0128](https://doi.org/10.1006/csla.1999.0128)).

**P6 — Seleção de features corta ruído e custo: ranquear por IG/χ², nunca por MI nem por intuição.**
No estudo canônico de texto, IG e χ² se correlacionam fortemente e ambos esmagam MI — que é enviesado para termos raros, exatamente o que sobra quando o vocabulário obedece Zipf e o corpus tem 644 exemplos. Para NB a seleção tem duplo efeito: menos features aleatórias somando ruído nos log-scores (posterior menos extremo, ECE menor) e menos tabela/queries em runtime. Aplicado ao jazz: a "feature" é o **balde** de 2^14, não o n-gram — seleção e relatório de colisão andam juntos. Fonte: Yang & Pedersen, *A Comparative Study on Feature Selection in Text Categorization*, ICML 1997 ([mlanthology](https://mlanthology.org/icml/1997/yang1997icml-comparative/)); Forman, *An Extensive Empirical Study of Feature Selection Metrics for Text Classification*, JMLR 2003 ([jmlr.org/papers/v3/forman03a](https://jmlr.org/papers/v3/forman03a.html)); Manning, Raghavan & Schütze, *IIR*, cap. 13 ([nlp.stanford.edu/IR-book](https://nlp.stanford.edu/IR-book/)).

**P7 — Toda soma de log-probs precisa de um contrato numérico: domínio, escala, clamp, subtração do máximo.**
O softmax com shift pelo máximo (`nlu.go:1434-1441`) é matematicamente idêntico e numericamente seguro — `sum ≥ 1`, `exp ≤ 1`, sem overflow; a variante division-free tem erro maior e não deve ser usada. A tabela int16 em 1/256 nat (zero ≈ −2521 unidades, clamp −32000, `nlu.go:1368-1384`) limita o erro de quantização a `≤ n·(1/512)` nats por classe no pior caso (~0.2 nats para n≈100): margens de decisão abaixo disso são ruído e devem ser tratadas como empate. Fonte: Blanchard, Higham & Higham, *Accurately computing the log-sum-exp and softmax functions*, IMA J. Numer. Anal. 2021 ([doi:10.1093/imanum/draa038](https://doi.org/10.1093/imanum/draa038), [arXiv:1909.03469](https://arxiv.org/abs/1909.03469)).

**P8 — Distância de edição é um modelo de erro, não uma geometria: escolha o formalismo pelo erro que ele pune.**
Levenshtein/Wagner–Fischer cobre ins/del/sub; ~80% dos erros reais de digitação são edições simples incluindo transposição adjacente, o que motiva Damerau. Existem dois "Damerau": OSA (nenhuma substring re-editada — o que `fzDist` implementa, `fuzzy.go:150-284`) e o DL verdadeiro de Lowrance & Wagner, que é métrica mas custa estado extra. OSA viola a desigualdade triangular em casos raros — aceitável como ranqueador, não como prova. Os pesos `fzEdit=4`/`fzSlip=2` (`fuzzy.go:107-108`) transformam a DP num modelo tipado de erro (tecla vizinha/transposição custa metade); os limites `fzLimit` (`fuzzy.go:288-298`) são o orçamento de erro por comprimento. Fonte: Damerau, CACM 1964 ([doi:10.1145/363958.363994](https://doi.org/10.1145/363958.363994)); Lowrance & Wagner, *An Extension of the String-to-String Correction Problem*, JACM 1975 ([doi:10.1145/321879.321880](https://doi.org/10.1145/321879.321880)); Navarro, *A Guided Tour to Approximate String Matching*, ACM CSUR 2001 ([doi:10.1145/375360.375365](https://doi.org/10.1145/375360.375365)).

**P9 — Filtros são lower bounds admissíveis; a métrica decide.**
O q-gram lemma: `ed(a,b) ≤ k` implica que a e b compartilham `≥ max(|a|,|b|) − q + 1 − k·q` q-grams — um bound de rejeição que nunca descarta o candidato verdadeiro. `niBound` (`nameindex.go:155`) é da mesma família (comprimento + popcount da máscara de letras). Regras: (a) o filtro nunca pode superestimar a distância — sanidade se testa por exaustão em pares rotulados; (b) filtro não rankeia, só descarta — ranking é da DP nos sobreviventes; (c) o ganho se mede em `rejects/DP` e ns/descarte, não em "parecia razoável". Fonte: Ukkonen, *Approximate string matching with q-grams and maximal matches*, TCS 1992 (survey: Navarro 2001, acima); Boytsov, *Indexing methods for approximate dictionary searching*, ACM JEA 2011 ([doi:10.1145/1963190.1963191](https://doi.org/10.1145/1963190.1963191)).

**P10 — Jaro–Winkler rankeia nomes; não certifica. Usar como desempate, nunca como override.**
JW (`J = (m/la + m/lb + (m−t/2)/m)/3`, boost `+ℓ·0.1·(1−J)`, prefixo `ℓ ≤ 4`) nasceu de record linkage para nomes curtos — onde o bound por comprimento é fraco e o prefixo carrega evidência. Não é métrica e o boost de prefixo é heurístico; no benchmark de name-matching o melhor resultado veio do híbrido SoftTFIDF, não de JW puro. O jazz já tem `fzJaroWinkler` inteiro ×1000 (`nameindex.go:959-1009`) — usado só em `niEnvScore` com corte 0.88 (`nameindex.go:1014-1028`). Fonte: Winkler, *String Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter Model*, ASA Proceedings 1990 ([PDF](http://www.asasrms.org/Proceedings/papers/1990_056.pdf)); Cohen, Ravikumar & Fienberg, *A comparison of string distance metrics for name-matching tasks*, IIWeb 2003 ([PDF](http://www.cs.cmu.edu/~wcohen/postscript/ijcai-ws-2003.pdf)).

**P11 — Similaridade de conjuntos tem três coeficientes com vieses opostos: Jaccard, cosseno, overlap — nomeie o que está usando.**
Jaccard `|A∩B|/|A∪B|` pune assimetria de tamanho; cosseno `|A∩B|/√(|A||B|)` é o meio-termo; Szymkiewicz–Simpson/overlap `|A∩B|/min(|A|,|B|)` vale 1 sempre que um conjunto contém o outro — `imOverlap` (`improve.go:171`) é esse, e portanto superestima similaridade entre trechos de tamanhos muito diferentes. Cada coeficiente responde uma pergunta diferente ("iguais?" / "mesma direção?" / "um contém?") e produz rankings diferentes em paths de comprimentos distintos; escolher é decisão, não detalhe. Fonte: Manning, Raghavan & Schütze, *IIR*, cap. 3 e 6 ([nlp.stanford.edu/IR-book](https://nlp.stanford.edu/IR-book/)).

**P12 — O() paga o algoritmo; a constante paga o hardware — orce as duas.**
No i386 todo `math.Log`/`math.Exp` é software: o perfil do Dell mediu `nuTrain` comendo 60% da CPU via 786 mil logs por partida (48 classes × 2^14 baldes); a correção foi observar que quase todos os baldes têm o mesmo valor — um log por classe mais um por balde ocupado (`nlu.go:1365-1385`). Mesma lição do fuzzy: bound barato descarta antes da DP. Com N pequeno (50 intents, ~100 n-grams, ~10³-10⁴ paths), o fator constante decide mais que a assintótica — tabelas byte/int16 + iteração sequencial batem estruturas pointer-heavy. Fonte: [low-level-performance.md](low-level-performance.md) e `results/dell-0.9-progress.md` (medidos); princípio clássico em Bentley, *Writing Efficient Programs*.

**P13 — Orçamento é por pedido, não por componente: complexidade se soma.**
O usuário não vê `O(Classe·nGram)` nem `O(F·bound)`; vê o p50 do pedido inteiro (medido: 14 ms no eval, ~130 ms cold com tudo). Cada estágio recebe uma fatia (§2.6); uma otimização que melhora um estágio mas adiciona alloc/pointer-chase em outro frequentemente zera o ganho. A unidade de trabalho do orçamento é o pedido — `brRun` → `Parse` → `resolve` → `verify` — e a moeda é ms wall no Dothan + picos de RSS, não contagem teórica de operações. Fonte: mesma disciplina de [benchmarks.md](benchmarks.md) e `results/dell-0.9-progress.md`.

---

## 2. Fundamentos

### 2.1 Calibração e limiares de decisão

**Definição.** Um score `p̂(x)` é calibrado se `E[y | p̂ = p] = p` — entre os pedidos aos quais o sistema atribui confiança p, a fração correta deve ser p. Para o brain isso não é estética: `Intent.Understood()` (`nlu.go:61-64`), o gate `brLevel0Min` (`brain.go:593`), a decisão de agir sem perguntar em `brPick` (`methods.go:1132`) e o próprio `false_success` do eval (`evaloffline.go:399-407`) são funções de threshold sobre confiança. Se `p̂` superestima, `false_success` sobe; se subestima, `no_llm_rate` cai e perguntas inúteis sobem.

**Métricas.**

- `ECE = Σ_{m=1..M} (|B_m|/n)·|acc(B_m) − conf(B_m)|` — erro médio ponderado por bin.
- `MCE = max_m |acc(B_m) − conf(B_m)|` — pior bin; para nós, o bin que contém 0.6 e 0.85 é o operacionalmente importante.
- `Brier = (1/n)Σ(p̂_i − y_i)²` — calibração + refinamento juntos.
- `NLL = −(1/n)Σ log p̂_i(y_i)` — pune confiança errada no extremo; é o que Platt/temp/isotônico minimizam.
- Reliability diagram por **canal** (ver abaixo), bins por quantis (amostras ~700-1000: bins fixos de 0.1 vão ter bins com 3 pontos e ECE subestimado — o "lower bound trap" de Vaicenavicius et al. 2019).

**Mapas de correção** (todos monotônicos no score — P3):

| Método | Forma | Parâmetros | Fit | Quando |
|---|---|---|---|---|
| Platt | `p_cal = σ(a·s + b)` | 2 | NLL por Newton/grid | poucos dados, distorção simétrica |
| Temperatura | `q_k = softmax(z_k/T)` | 1 | NLL 1-D | score já é log-like; preserva tudo |
| Beta | `logit(p_cal) = a·ln p̂ + b·ln(1−p̂) + c` | 3 | NLL | NB — scores nos extremos, skew |
| Isotônico/PAV | `p_cal = g(s)`, g crescente | ~nós | PAV O(n) | dados ≥ ~10³; overfitta abaixo |

O PAV (Pool Adjacent Violators): ordena pontos por score, e onde `y_i` realizado viola a monotonicidade (`y_i < y_{i−1}` na média do bloco) funde os blocos — O(n) com uma pilha. Produz uma escada de nós `(s_j, p_j)`; em runtime é interpolação linear sobre ~16-32 nós, ou seja, um `int16[]` embutido.

**Canais.** A confiança do `Parse` é composta por canais com qualidade diferente (`nlu.go:922-967`): regra única → 0.9; regra+NB concordando → 0.97; NB disse `none` com p>0.9 → 0.7; sem regra → `p·0.75` (×0.5 se veto); empate → `0.55+0.45p`; e caps: `TypoOnly` ≤ 0.5, shape inválido ×0.6, resíduo ≥3 ×0.6 (`nlu.go:994-1030`). Calibrar um número único sobre essa mistura é medir a média de distribuições diferentes — cada canal precisa de sua própria curva (ou ao menos da sua própria constante). Mesma história do outro lado: `brResolve`/`brPick`/`niTop` decidem por **margem em unidades de score** (`fzSlip·8`, `2·fzSlip·8` — `methods.go:1055`, `:1151`; `nameindex.go:710-720`), não por probabilidade; transformar margem em p̂ exige seu próprio mapa (§5, P7).

**Chow / risco-cobertura.** Com custo `t` de rejeição e custo 1 de erro, a regra ótima é rejeitar quando `1 − max p > t`, i.e., `max p < 1−t`. A curva risk(τ) × coverage(τ) se constrói varrendo τ sobre corpus rotulado; `nuMinConf` e `brLevel0Min` devem ser lidos como dois pontos dessa curva com objetivos diferentes — `nuMinConf` minimiza custo de pergunta desnecessária + erro, `brLevel0Min` é um gate de segurança (`false_success = 0` é constraint, não objetivo). Margens de `niTop`/`brPick` são a mesma decisão em unidades de score: a pergunta "vale agir sem perguntar" tem resposta na curva, não na constante `fzSlip`.

### 2.2 Teoria da informação e seleção de features no NB

**Quantidades** (feature = presença/ausência do balde b no exemplo; classe = intent):

- Entropia da classe: `H(C) = −Σ_c p(c)·log p(c)` — com 48 classes úteis e priors quase uniformes, H(C) ≈ log 48 ≈ 3.87 nats.
- Entropia condicional: `H(C|b) = p(b)·H(C|b=1) + (1−p(b))·H(C|b=0)`.
- Ganho de informação: `IG(b) = H(C) − H(C|b)` — redução de incerteza ao observar o balde.
- Informação mútua pontual/binária: `MI(b,c) = Σ p(b,c)·log(p(b,c)/(p(b)p(c)))` — enviesada para b raro.
- Qui-quadrado (tabela 2×2 por (b,c): A=docs da classe com b, B=outras classes com b, C=classe sem b, D=resto): `χ²(b,c) = N(AD−BC)²/((A+C)(B+D)(A+B)(C+D))` — o denominador pune features raras.
- Document frequency: `DF(b)` = #exemplos contendo b.

**O que a literatura diz** (P6): IG≈χ²≈DF ≫ MI para NB/texto; cortar o ~90% do rabo do vocabulário mantém ou melhora a acurácia. Para o jazz o efeito secundário importa tanto quanto o primário: cada feature sem sinal adiciona `log` de uma razão próxima de 1 mas variável — ruído somado n vezes que empurra o posterior para extremos (P2) e gasta loads no `predict`.

**O que já existe para calcular isso:** `nuTrain` já materializa `counts[ci][b]` e `totals[ci]` (`nlu.go:1329-1353`) — IG(b) é um laço sobre as células não-nulas (~10³-10⁴, *(estimativa)*), i.e., centésimos de segundo no Dell. Sem custo de infraestrutura.

**Colisões e o hashing trick.** A feature real é o balde `FNV-1a mod 2^14` (`nlu.go:1308-1324`), não o n-gram. Com D n-grams distintos *(estimativa: ~8-15k sobre os 644 exemplos)*, colisões `~C(D,2)/16384` ≈ 10³ pares *(estimativa)* fundem contagens. Weinberger et al. provam que produtos internos são preservados em esperança — classificação aguenta — mas **contagens individuais se distorcem**, então: (a) IG deve ser calculado por balde (incorpora o efeito real da colisão), (b) um relatório de colisão entre n-grams de classes diferentes é informação de debug valiosa, (c) subir para 2^15 baldes dobra a tabela (1.5→3 MB) — trade-off explícito, não default.

**Viés de raridade:** com ~13 exemplos/classe, a maioria dos baldes ocupados tem contagem 1-2. Piso `DF ≥ 2` antes do IG, ou preferir χ² — ambos os cortes já absorvem isso melhor que MI.

### 2.3 Suavização

**Lidstone/KT (hoje).** `p̂(g|c) = (c_g + α)/(N_c + αV)` com `α = 0.5`, `V = nuBuckets = 2^14` (`nlu.go:1361-1364`). O denominador `N_c + 8192`: com `N_c ≈ 100 n-grams × ~13 exemplos ≈ 1300`, `den ≈ 9492` e `p̂_zero ≈ 5.3e-5` ≈ −9.85 nats → −2521 unidades na escala 1/256. Interpretação: cada n-gram do pedido nunca visto na classe custa ~1.1 nats relativos a vê-lo uma vez — punição forte e desejada, mas inteiramente determinada por `αV` no denominador, um número que hoje é acidental.

**O detalhe semântico de V.** Num modelo multinomial sobre o alfabeto de features, `V` deve ser o número de símbolos possíveis = 2^14 baldes — o código está tecnicamente correto para o espaço hasheado. Mas `αV = 8192` também é a "força do prior": domina `N_c` pequeno. α certo vem de held-out NLL, não de convenção; o `skip` hook existe exatamente para LOO (`nlu.go:1327`, `nlu.go:1339`).

**Good-Turing.** Contagens reestimadas `r* = (r+1)·N_{r+1}/N_r` e `P(massa nunca vista) = N₁/N`. Com corpus de 644 exemplos, GT por feature é instável (N_{r+1} esparsos — Gale & Sampson exigem suavizar os próprios N_r), mas `N₁_c/N_c` **por classe** é um estimador barato e útil de "quanto vocabulário da classe ainda está por vir" — sinal de OOV por pedido: se a fração de n-grams do pedido em baldes vazios em todas as classes é alta, o vetor de scores degenera para `prior + n·zero_c`, e hoje isso só aparece indiretamente.

**Degrade por construção.** Se todos os n-grams do pedido caem em baldes vazios/compartilhados: `scores[c] ≈ prior[c] + n·zero_c` ≈ constante → `sum ≈ C` → `p ≈ 1/48 ≈ 0.02` → `conf ≈ 0.016` → "não entendido" → ofertas/modelo. O rebaixamento é seguro sem código extra; documentar como invariante e **testá-lo** (§5, P6: tornar o sinal explícito e calibrado em vez de emergente).

### 2.4 Estabilidade numérica

**Representação.** `table[c][b] = int16(log p · 256)` — escala 1/256 nat, faixa ±127.9 nats, clamp −32000 (`nlu.go:1295`, `:1368-1384`). Soma em `int32`: `n·|score| ≤ 100×32000 ≈ 3.2M ≪ 2³¹` — sem overflow nem em texto patológico. Erro de quantização ≤ 1/512 nat por entrada; acumulado ≤ `n/512` ≈ 0.2 nats para n≈100 → **epsilon de decisão**: diferenças de score < ~50 unidades (~0.2 nat) são indistinguíveis de ruído de quantização e devem contar como empate (alimenta P4/P9 em §5).

**Softmax.** `p_best = 1/Σ_c exp((s_c − s_best)·νβ/(n·νScale))` (`nlu.go:1434-1441`). Dois mecanismos em um:

1. **Shift pelo máximo** — exp ≤ 1, sum ≥ 1: nunca overflow, underflow inofensivo (termo→0). É a forma recomendada; a variante sem divisão tem erro maior.
2. **Normalização por comprimento** — o expoente é `Δnats·(β/n)`, i.e., `T_eff = n/β` nats: a temperatura efetiva cresce linearmente com o número de n-grams. Com β=6: n=10 → T≈1.7 (afiado), n=100 → T≈16.7 (achatado). Isso neutraliza o viés do NB de somar evidência como iid (o "length effect" que Rennie et al. tratam de outra forma) — mas é uma escolha implícita: **β é um hiperparâmetro de calibração disfarçado de constante** (§5, P2).

**Float no i386.** `math.Log`/`math.Exp` são software: ~786k logs no `nuTrain` mediram 60% da CPU de um pedido; a deduplicação por valor (`zero` por classe + um log por balde ocupado) resolveu (`results/dell-0.9-progress.md:37-38`). Em `predict`, ≤ 48 `math.Exp` por pedido é aceitável (~µs); se o perfil um dia reclamar, exp inteiro via LUT 256×int16 + shift é a substituição óbvia — só com medida.

### 2.5 Distâncias de edição e similaridades

**Família.**

| Métrica | Definição | Métrica? | Uso no jazz |
|---|---|---|---|
| Levenshtein (Wagner–Fischer) | ins/del/sub custo 1, DP O(mn) | sim | base do `fzDist` |
| OSA | + transposição adjacente, substring não re-editada | não (falha rara na desig. triangular) | `fuzzy.go:150-284` |
| DL verdadeiro | transposições irrestritas, estado por caractere | sim | — (custo sem ganho p/ typos) |
| Jaro | `J = (m/la + m/lb + (m−t/2)/m)/3`, janela `max/2−1` | não | `nameindex.go:961` (inteiro ×1000) |
| Jaro–Winkler | `J + ℓ·0.1·(1−J)`, `ℓ ≤ 4` | não | `niEnvScore` corte 880 (`nameindex.go:1023`) |
| q-gram bound | `ed ≤ k ⇒ shared ≥ max(l)−q+1−kq` | lower bound | `niBound` análogo (len+máscara, `nameindex.go:155`) |
| Jaccard / cosseno / overlap | `∩/∪` ; `∩/√AB` ; `∩/min` | coeficientes | `imOverlap` = overlap (`improve.go:171`) |

**O modelo de erro do jazz.** `fzDist` é OSA ponderado: qualquer edição custa `fzEdit=4`, exceto substituição por tecla vizinha e transposição, que custam `fzSlip=2` (`fuzzy.go:104-139`); `fzLimit(n)` dá o orçamento: ≤3 runes → 2, ≤4 → 4, ≤6 → 6, senão 8 (`fuzzy.go:288-298`). Isso é um modelo gerador de typos disfarçado de distância: "quão provável é o usuário ter digitado a por b" ≈ "edições baratas". Correto como ranqueador; não é métrica nem probabilidade.

**Onde cada peça atua.**

- Typo repair de tokens: `repair` (`nlu.go:747`) + `fzIntent`/`fzNearWord` (`fuzzy.go:301-380`) — vocab de 612 palavras-conceito, limite por comprimento.
- Resolução de entidades: `niResolveEntity` → `resolveAll` score aditivo `d + penalidades − bônus` (ext/hint/touched/recency) → `niTop` margem `< fzSlip` vira empate (`nameindex.go:514-720`).
- Decisão de agir: `brResolve` conta `Tied` por margem `< 2·fzSlip·8`; `brPick` age sozinho se exato-único, único-candidato-seguro (`brSafeTypo`, `methods.go:1095`) ou líder claro `≥ fzSlip·8`; senão `brChoose` → menu ≤5 (`methods.go:1043-1202`, `offers.go` `olMenuMax`).
- Ambiente (bin/host/service): `niEnvScore` aceita Damerau dentro do limite **ou** JW ≥ 0.88 para `len(q) ≥ 5` — o único lugar onde JW vive hoje.

**q-gram e bounds.** `niBound(qm, ql, m, l)` = diferença de comprimento × `fzEdit` + `popcount(maskXOR)/2 × fzSlip` — um bound por comprimento + alfabeto, constante no tempo. É da família do q-gram lemma (P9): admissível por construção, descarta a maioria dos 5k paths antes da DP. A extensão óbvia (bound por contagem de 2-grams/3-grams) precisa de contagens por arquivo (~40 B/path em uint16 packed *(estimativa)*) — §5, P8.

### 2.6 Orçamentos de complexidade (O() e constantes no Dell)

Orçamento por pedido (P13): alvo ~20 ms warm, ~150 ms cold-path completo. Constantes medidas em `results/dell-0.9-progress.md` e [low-level-performance.md](low-level-performance.md); *(estimativa)* onde não medido.

| Estágio | O() | Constante / fatos | Orçamento (est) |
|---|---|---|---|
| `nuTokenize`+`normalize`+fold (`nlu.go:291-417`) | O(L), L≤400 B | byte-level, sem regexp no hot path | ~5 µs |
| Tags/conceitos por token | O(T·tags) | map[string] pequenos | ~2 µs |
| `spot` Aho–Corasick (`nlu.go:601-708`, `:1484+`) | O(L + matches) | byte automaton, sem alloc por nó | ~10 µs |
| `repair` typo (`nlu.go:747`, `fuzzy.go:301`) | O(V·dp), V=612 | bound de comprimento antes da DP; DP ASCII stack-array 0-alloc (~0.4 µs/palavra medido) | ~50 µs |
| `rules` (`nlu.go:838-891`) | O(I·R·G·T) ≈ 50×2×2×15 | ~3k `has()` byte-scan | ~30 µs |
| NB `predict` (`nlu.go:1398`) | O(C·n) = 48×~100 | ~5k loads int16 + adds; ≤48 `math.Exp` | ~20 µs |
| NB `nuTrain` lazy (`nlu.go:1327`) | O(C·B + ex·n) | 786k→~1k logs já corrigido; ~100-200 ms uma vez (medido) | amortizado |
| `niResolveEntity` (`nameindex.go:1035`) | O(F·bound + K·mn) | F≈5k paths; bound O(1); DP só sobreviventes; era 26 ms/15k allocs antes das otimizações | ~2-5 ms |
| `brResolve`+`brPick`+menu | O(K) | K≤8 cands | ~0 |
| Total típico | | medido: p50 14 ms, p95 362 ms no eval | ✓ dentro do alvo |

**Memória** (386 = 32-bit: `int`=32b, `int16`/`uint8` contam de verdade):

| Estrutura | Tamanho |
|---|---|
| `nb.table` int16 | 48×16384×2 B = **1.5 MB** |
| `counts` durante treino uint16 | ~1.5 MB (descartável) |
| `seen`-bitmap global (proposta) | 16384/8 = **2 KB** |
| `niIndex` ~5k paths | ~0.2 MB + strings |
| AC automato | ~nós×~8 B |
| RSS total medido | **59 MB** (eval), 27 MB pedido único — headroom grande dentro de 2 GB, mas o sistema + buffers do HDD comem ~1 GB: teto prático ~1.4 GB |

**Verdades do i386:** sem POPCNT (`popcount` = SWAR ~6 ops), FPU lenta (log/exp software), L1/L2 pequenos (32K/1-2M Dothan) — iteração sequencial sobre `int16[]` denso bate qualquer estrutura com ponteiros; `uint32` masks e `int32` acumuladores já são o padrão do código e devem continuar.

---

## 3. Mapa concreto: matemática → código

| Onde | O que é | Status |
|---|---|---|
| `nlu.go:45` | `nuMinConf = 0.6` — limiar "entendido" | feito — constante, não calibrada |
| `nlu.go:47-64` | `Intent{Confidence,Evidence,Alt,TypoOnly}`, `Understood()` | feito |
| `nlu.go:897-971` | `Parse`: ramos regra/classificador/empate | feito |
| `nlu.go:930-934` | sem regra → `p·0.75`; veto ×0.5 | parcial — fatores fixos, nunca estimados |
| `nlu.go:935-949` | regra única 0.9 / +NB 0.97 / `none`>0.9 → 0.7 | parcial — idem |
| `nlu.go:950-967` | empate → `0.55+0.45·p`, `Alt` | parcial — idem |
| `nlu.go:994-1020` | `TypoOnly` cap 0.5 | feito — cap estrutural correto |
| `nlu.go:1021-1030` | shape ×0.6; residual ≥3 ×0.6 | parcial — heurístico, não medido |
| `nlu.go:1031` | arredonda 2 casas | feito |
| `nlu.go:1301-1324` | `nuNB`: int16 table, FNV-1a → 2^14 baldes, n=3-5-grams | feito |
| `nlu.go:1327-1354` | `nuTrain` contagens `uint16` saturadas, `skip` hook p/ LOO | feito |
| `nlu.go:1361-1387` | Lidstone `α=0.5` sobre V=2^14, prior Laplace+1, log dedup | parcial — α fixo, não ajustado por NLL |
| `nlu.go:1368-1384` | clamp −32000 | feito |
| `nlu.go:1398-1441` | `predict`: int32 accum + max-shift softmax, `β=6`, T=n/β | parcial — estável, não calibrado |
| `nlu.go:1444-1547` | Aho–Corasick byte-automaton | feito |
| `nlu.go:41-45` | embed `nlu/intents.json` + `lexicon.json`, lazy train | feito |
| `fuzzy.go:104-139` | modelo de custo fzEdit=4/fzSlip=2, teclado | feito |
| `fuzzy.go:150-284` | `fzDist` OSA ponderado, ASCII 0-alloc + early-exit | feito |
| `fuzzy.go:288-305` | `fzLimit`, `fzNearWord` | feito |
| `fuzzy.go:349-380` | `fzIntent` verbos typo | feito |
| `nameindex.go:155-165` | `niBound` lower bound len+máscara | feito |
| `nameindex.go:514-654` | `resolveAll` score aditivo int | parcial — score ≠ probabilidade |
| `nameindex.go:710-720` | `niTop` margem < fzSlip → empate | parcial — margem em unidades, não p̂ |
| `nameindex.go:959-1028` | `fzJaroWinkler` ×1000 + `niEnvScore` JW≥880 | parcial — JW só em env |
| `nameindex.go:821` | `niSimilarPaths` "parecidos" | feito |
| `methods.go:1043-1089` | `brResolve`: Tied < 2·fzSlip·8, evidência | feito |
| `methods.go:1095-1128` | `brSafeTypo`, `brSubseq` | feito |
| `methods.go:1132-1155` | `brPick`: exato-único / único-safe / líder ≥fzSlip·8 | feito — limiares por unidade de score |
| `methods.go:1157-1205` | `brChoose` → menu ≤5 | feito |
| `brain.go:140` | confirmação do usuário → Confidence = nuMinConf | feito |
| `brain.go:593` | `brLevel0Min = 0.85` | feito — constante, não calibrada |
| `brain.go:598-736` | headless/interactive L0 gating + offline exec | feito |
| `route_task.go:236-352` | `rtClassifyRaw` scores aditivos, empate → Ambiguous | parcial — routing, sem probabilidade |
| `improve.go:171-230` | `imOverlap`/`imArgOverlap` (coef. overlap) | parcial — bias de conjunto pequeno (P11) |
| `judge.go:441-495` | UCB1 `mean + 0.2·√(lnN/max(x.N,0.5))` | feito — domínio diferente (roteador online) |
| `evaloffline.go:79-116` | métricas: success, `false_success`, model-free, p50/p95, RSS | feito |
| `evaloffline.go:399-407` | classificação `false_success` | feito |
| ECE/MCE/reliability/Brier/NLL | — | **só pesquisado** |
| Platt / isotônico / beta / temperature | — | **só pesquisado** (β informal existe) |
| IG/χ²/MI feature selection | — | **só pesquisado** (insumos já existem em `counts`) |
| Good-Turing / sinal OOV | — | **só pesquisado** |
| `learn.go` (online ensina offline, spec §7) | — | **não implementado** — ver `docs/design/offline-brain.md` |

---

## 4. Linha de base medida (Dell)

De `results/dell-0.9-progress.md` — referência para toda proposta:

- `jazz eval offline`: **39/39** · `false_success` **0** · **34/39** sem modelo · perguntas 3/3 · 0.13 comandos/tarefa · **p50 14 ms · p95 362 ms · RSS 59 MB**.
- `jazz -p --offline "abre o README.md"`: **120-139 ms** total, 0 GC, 0 tokens, RSS pico 27 MB.
- `nuTrain` era 60% da CPU de um pedido: 786 mil `math.Log`; fix = 1 log/classe + 1/balde ocupado.
- `nlu/intents.json`: 50 intents, 644 exemplos positivos, 63 negativos, 91 regras; `lexicon.json`: 216 fillers, 159 slang, 63 conceitos, 612 palavras; blind test 100 frases, piso 70%.
- Fuzzy: `fzDist` era ~6.1 µs/432 B/3 allocs; protótipo ASCII stack-array ~408 ns/0 allocs (medido em máquina de dev — no Dothan multiplicar por ~5-15×, *(estimativa)*).

---

## 5. Propostas

Resumo — detalhes em cada item. Métricas sempre no harness de `evaloffline.go` + corpus de resolução.

| # | Proposta | Métrica principal | Impacto | Esforço | Risco |
|---|---|---|---|---|---|
| P1 | Harness de calibração (ECE/MCE/Brier/NLL por canal) | ECE por canal medido | 5 | M | baixo |
| P2 | Ajuste de `nuBeta` por NLL (LOO) | NLL↓ ECE↓, limiares certos | 4 | S | baixo |
| P3 | Mapa de confiança por canal (isotônico/Platt int16) | ECE↓, false_blocks↓ | 4 | M | médio |
| P4 | Limiares pela curva risco–cobertura | `no_llm_rate`↑ sob FS=0 | 4 | S-M | médio |
| P5 | Seleção de features IG/χ² + relatório de colisão | blind acc↑, ECE↓, treino RAM↓ | 3 | M | médio |
| P6 | α por LOO-NLL + sinal OOV (N₁/N) | NLL↓, OOV conf correto | 3 | S-M | baixo |
| P7 | Resolver probabilístico (Platt sobre features do score) | perguntas↓ sem FS | 4 | M | médio |
| P8 | JW como desempate no resolver de arquivos | perguntas↓ em nomes curtos | 3 | S | baixo-médio |
| P9 | Instrumentação de decisão (margem, OOV, por-estágio) | habilita tudo; regressões | 4 | S | nulo |

**P1 — Harness de calibração.**
Mecanismo: flag/subcomando que roda o corpus rotulado (blind 100 frases + `evals/offline/tasks.json` + LOO dos 644 exemplos) e registra `(canal, conf, acertou)`; calcula ECE/MCE/Brier/NLL com **bins por quantis** (10-15) e despeja a reliability table em `results/`. Canal ∈ {rule-only, rule+NB, NB-only, tie, vetoed, typo-only, env-resolve, file-resolve}. Arquivos: novo `calibrate.go` + ganchos de leitura em `nlu.go` (Parse já devolve `Evidence` com o canal implícito — formalizar um campo `ConfChannel`) + `evaloffline.go`. Custo 32-bit: zero no caminho de pedido (só roda em eval); ~50-200 ms de eval extra. Métrica: ECE/MCE por canal reportados — pré-requisito de P2-P4. Impacto 5, esforço M, risco baixo (aditivo, não muda decisões).

**P2 — Temperatura: `nuBeta` ajustado por NLL.**
Mecanismo: tratar `nuBeta=6.0` como o que é — temperatura `T=n/β`. Grid search ou Newton 1-D minimizando NLL do `predict` sobre os exemplos com LOO (`nuTrain(skip)` já aceita o predicado, `nlu.go:1327`). Opcional: β por comprimento `β(n)` se NLL mostrar dependência. Arquivos: `nlu.go` (constante + doc), `calibrate.go` (fit). Custo: 0 em runtime; fit = algumas centenas de `nuTrain`+`predict` em dev (não no Dell — é asset forjado). Métrica: NLL/ECE do canal NB-only ↓ → decisões nos limiares atuais ficam corretas mais frequentemente → `no_llm_rate`↑ sem `false_success`. Impacto 4, esforço S, risco baixo — argmax imutável (P3); única decisão é a constante.

**P3 — Mapa de confiança pós-parse por canal, em inteiros.**
Mecanismo: gerador (dev) ajusta isotônico (PAV, ~16 nós) ou beta-calibration por canal sobre dados de P1+P4; emite `nlu/calib.json` embutido com nós `(conf_in int16, conf_out int16)` e clips `[min,max]` por canal. `Parse` substitui os fatores fixos (`·0.75`, `×0.5`, `0.55+0.45p`, caps) pela interpolação linear int16 sobre o nó — ~10 linhas, O(1), zero alloc. Fallback: sem asset → comportamento atual (identidade). Arquivos: `nlu.go` (apply), `nlu/calib.json` (asset), `calibrate.go` (fit). Custo 32-bit: ~100 B RAM, 1 interpolação ~10ns. Métrica: ECE↓; `false_blocks` e perguntas passam a refletir frequências reais por canal. Impacto 4, esforço M, risco médio — dados de ajuste viesados distorcem; mitigar com clip por canal, LOO e fallback identidade; PAV só com ≥~50 pontos/canal, senão Platt.

**P4 — Limiares pela curva risco–cobertura.**
Mecanismo: varrer `τ ∈ {0.5..0.95}` (e margens `μ ∈ {1..4}·fzSlip·8`) sobre corpus rotulado; para cada τ reportar `no_llm_rate`, `false_success`, `false_blocks`, perguntas, `no_llm` headless vs interativo separados. Escolher τ* que maximiza cobertura com `false_success=0` e folga (τ_final = τ*+δ, δ = meio-bin). Aplicar a `nuMinConf`, `brLevel0Min`, margens `niTop`/`brPick`, e critérios de headless (`brain.go:598-624`). Arquivos: `calibrate.go` (sweep) + constantes em `nlu.go`/`brain.go`/`methods.go`/`nameindex.go`. Custo: 0 runtime. Métrica: `no_llm_rate` real ↑ com FS=0; perguntas ≈ necessárias. Impacto 4, esforço S-M, risco médio — overfit ao corpus pequeno → bootstrap/LOO e folga conservadora.

**P5 — Seleção de features por IG/χ² + relatório de colisão.**
Mecanismo: em `nuTrain`, antes de emitir a tabela, calcular `DF(b)` e `IG(b)` (ou χ² máximo sobre classes) laçando só baldes ocupados de `counts`; baldes com `DF<2` e `IG≈0` recebem `zero` — ou, melhor, a tabela vira `(bitmap + int16[] denso)` se ≥30% dos baldes morrem. Relatório: top-K baldes por IG, pares de n-grams distintos colidindo no mesmo balde com classes conflitantes (debug flag). Arquivos: `nlu.go` (`nuTrain`), `calibrate.go` (relatório). Custo: treino +O(C·B_nz) <1 s; predict idêntico ou mais barato (índice mais denso quente no cache). Métrica: blind accuracy mantida/↑, ECE↓ (menos ruído somado), RAM de treino↓. Impacto 3, esforço M, risco médio — seleção overfitta em corpus pequeno: gate conservador (corta só IG≈0/DF<2) e validação LOO+blind antes de adotar.

**P6 — α por LOO + sinal explícito de OOV (Good-Turing por classe).**
Mecanismo: grid `α ∈ {0.1,0.25,0.5,1,2}` × LOO-NLL → constante ajustada; por classe, `N₁_c/N_c` (fração de massa em singletons) entra no asset. Em `predict`, contar n-grams do pedido em baldes globalmente vazios (bitmap `seen` 2 KB, OU de todas as linhas) → `oov_frac`; registrá-la em `Evidence` e alimentar como feature/clip no mapa de confiança (P3). Arquivos: `nlu.go` (`nuTrain`, `predict`, `Evidence`), `calibrate.go`. Custo: treino ×|grid| em dev; predict +n·1 bit-lookup (~2µs). Métrica: NLL/ECE↓; pedidos OOV recebem confiança honesta → `false_blocks` e perguntas calibradas em entrada fora-do-domínio. Impacto 3, esforço S-M, risco baixo.

**P7 — Resolver probabilístico.**
Mecanismo: transformar o score inteiro de `resolveAll`/`niTop` em probabilidade de "o top-1 é o arquivo certo": features já existentes — `d`, margem, `Exact`, bônus ext/hint/touched/recency, tamanho relativo — alimentam um Platt/logístico pequeno (ou isotônico) ajustado sobre corpus de resoluções rotuladas (gerar corpus: aplicar typos sintéticos em paths reais do repo de teste + decisões reais logadas). `brPick` passa a exigir `p̂_resolve ≥ τ_r` para agir sem perguntar; `brChoose` ordena por p̂. Arquivos: `nameindex.go` (extrair features), `methods.go` (`brPick`/`brChoose`), `calibrate.go` (fit + asset). Custo: O(1)/candidato (~K≤8). Métrica: perguntas por resolução ↓ sem errar pick; `false_success`=0 preservado; `cmds/task` estável. Impacto 4, esforço M, risco médio — exige dataset de resolução honesto; typos sintéticos devem imitar o teclado (`fzNeighbour`, transposições), não ruído uniforme.

**P8 — Jaro–Winkler como desempate no resolver de arquivos.**
Mecanismo: hoje JW só existe em `niEnvScore` (corte 880, `nameindex.go:1014-1028`). Estender: na zona de empate de `brPick`/`niTop` (margem < `fzSlip·8`), computar `fzJaroWinkler(query, basename)` dos top-3; auto-pick só se `jw[0] ≥ 880 && jw[1] < 800`, com `Why="jw"`. Nunca override de distância alta — JW decide só dentro da zona onde o bound por comprimento é fraco (nomes curtos, P10). Arquivos: `nameindex.go` (`niTop`/`niCand`), `methods.go` (`brPick`). Custo: ≤3 chamadas O(la·win) ~ µs. Métrica: perguntas por resolução ↓ em basenames curtos comuns; `false_success` inalterado. Impacto 3, esforço S, risco baixo-médio — JW não é métrica: confinar estritamente à zona de empate.

**P9 — Instrumentação de decisão.**
Mecanismo: contadores/struct leve registrando por pedido: `conf` do top1, margem top1−top2 em nats (já temos os scores), `oov_frac`, `rejects/DP` do resolver (eficácia do bound), tempo por estágio (timer por fase, já há `JAZZ_CPUPROFILE` para CPU). Expostos em `brResult` debug e agregados no eval. Arquivos: `nlu.go`, `nameindex.go`, `brain.go`, `evaloffline.go`. Custo: ~10 contadores int, ~0. Métrica: torna P1-P8 mensuráveis e cria sinais de regressão (margem média caindo = dataset/modelo degradando). Impacto 4, esforço S, risco nulo.

---

## 6. O que não fazer

- **Embeddings/transformers/LLM local**: peso de modelo, RAM, deps e CPU — fora do envelope; a filosofia é asset forjado em compile-time.
- **Calibrar em runtime**: os dados por usuário são poucos e viesados; o mapa é asset (forjado com corpus), runtime só interpola.
- **Bins fixos de ECE com n<10³**: subestimação documentada — quantis ou nada.
- **Mudar classificação via calibração**: mapas monotônicos não tocam o argmax; se a classe erra, o problema é feature/modelo, não o mapa.
- **DL verdadeiro no lugar de OSA**: ganho semântico ~zero para typos adjacentes, custo de estado real.
- **JW como métrica principal**: não-métrico, boost de prefixo heurístico — desempate apenas.
- **MI como ranking de features**: viés de raridade documentado (Forman 2003) — IG/χ²/DF.
- **Laplace α=1 "por padrão"**: superestima não-vistos; KT/Jeffreys é o default informado, o resto é hiperparâmetro por NLL.
- **Confiança única global**: os canais têm distribuições diferentes; média de curvas calibradas não é curva calibrada.
- **`math.Log`/`math.Exp` por elemento em hot path**: lição já paga (786k logs → ~1k); qualquer LUT/fixed-point novo só com perfil mostrando.

## Fontes

- Guo, Pleiss, Sun, Weinberger — *On Calibration of Modern Neural Networks*, ICML 2017. [arXiv:1706.04599](https://arxiv.org/abs/1706.04599)
- Vaicenavicius, Widmann, Andersson, Lindsten, Roll, Schön — *Evaluating model calibration in classification*, AISTATS 2019. [PMLR v89](https://proceedings.mlr.press/v89/vaicenavicius19a.html)
- Naeini, Cooper, Hauskrecht — *Obtaining Well Calibrated Probabilities Using Bayesian Binning*, AAAI 2015. [ojs.aaai.org](https://ojs.aaai.org/index.php/AAAI/article/view/9602)
- Niculescu-Mizil & Caruana — *Predicting Good Probabilities with Supervised Learning*, ICML 2005. [doi:10.1145/1102351.1102430](https://doi.org/10.1145/1102351.1102430)
- Platt — *Probabilistic Outputs for Support Vector Machines and Comparisons to Regularized Likelihood Methods*, 1999. [citeseer:10.1.1.41.1639](http://citeseerx.ist.psu.edu/viewdoc/summary?doi=10.1.1.41.1639)
- Zadrozny & Elkan — *Obtaining calibrated probability estimates from decision trees and naive Bayesian classifiers*, ICML 2001. [PDF](https://cs.fit.edu/~pkc/ml/related/zadrozny-icml01.pdf); e *Transforming classifier scores into accurate multiclass probability estimates*, KDD 2002. [doi:10.1145/775047.775151](https://doi.org/10.1145/775047.775151)
- Kull, Silva Filho, Flach — *Beta calibration: a well-founded and easily implemented improvement on logistic calibration for binary classifiers*, AISTATS 2017. [PMLR v54](https://proceedings.mlr.press/v54/kull17a.html)
- Chow — *On optimum recognition error and reject tradeoff*, IEEE Trans. Inf. Theory 16(1), 1970. [doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)
- El-Yaniv & Wiener — *On the Foundations of Noise-free Selective Classification*, JMLR 11:1605-1641, 2010. [jmlr.org/papers/v11/el-yaniv10a](https://www.jmlr.org/papers/v11/el-yaniv10a.html)
- Geifman & El-Yaniv — *Selective Classification for Deep Neural Networks*, NeurIPS 2017. [arXiv:1705.08500](https://arxiv.org/abs/1705.08500)
- Domingos & Pazzani — *On the Optimality of the Simple Bayesian Classifier under Zero-One Loss*, Machine Learning 29:103-130, 1997. [doi:10.1023/A:1007413511361](https://doi.org/10.1023/A:1007413511361)
- Rennie, Shih, Teevan, Karger — *Tackling the Poor Assumptions of Naive Bayes Text Classifiers*, ICML 2003. [PDF](https://cdn.aaai.org/ICML/2003/ICML03-081.pdf)
- Yang & Pedersen — *A Comparative Study on Feature Selection in Text Categorization*, ICML 1997. [mlanthology](https://mlanthology.org/icml/1997/yang1997icml-comparative/)
- Forman — *An Extensive Empirical Study of Feature Selection Metrics for Text Classification*, JMLR 3:1289-1305, 2003. [jmlr.org/papers/v3/forman03a](https://jmlr.org/papers/v3/forman03a.html)
- Manning, Raghavan, Schütze — *Introduction to Information Retrieval*, Cambridge 2008 (caps. 3, 6, 13). [nlp.stanford.edu/IR-book](https://nlp.stanford.edu/IR-book/)
- Weinberger, Dasgupta, Langford, Smola, Attenberg — *Feature Hashing for Large Scale Multitask Learning*, ICML 2009. [PDF](https://icml.cc/Conferences/2009/papers/407.pdf) / [arXiv:0902.2206](https://arxiv.org/abs/0902.2206)
- Krichevsky & Trofimov — *The Performance of Universal Encoding*, IEEE Trans. Inf. Theory 27(2):199-207, 1981. [doi:10.1109/TIT.1981.1056331](https://doi.org/10.1109/TIT.1981.1056331)
- Gale & Sampson — *Good-Turing Frequency Estimation Without Tears*, J. Quantitative Linguistics 2(3), 1995. [doi:10.1080/09296179508590051](https://doi.org/10.1080/09296179508590051) / [PDF](https://www.grsampson.net/AGTf.pdf)
- Chen & Goodman — *An empirical study of smoothing techniques for language modeling*, Computer Speech & Language 13(4):359-394, 1999. [doi:10.1006/csla.1999.0128](https://doi.org/10.1006/csla.1999.0128)
- Blanchard, Higham, Higham — *Accurately computing the log-sum-exp and softmax functions*, IMA J. Numer. Anal. 41(4):2311-2330, 2021. [doi:10.1093/imanum/draa038](https://doi.org/10.1093/imanum/draa038) / [arXiv:1909.03469](https://arxiv.org/abs/1909.03469)
- Damerau — *A technique for computer detection and correction of spelling errors*, CACM 7(3), 1964. [doi:10.1145/363958.363994](https://doi.org/10.1145/363958.363994)
- Lowrance & Wagner — *An Extension of the String-to-String Correction Problem*, JACM 22(2):177-183, 1975. [doi:10.1145/321879.321880](https://doi.org/10.1145/321879.321880)
- Navarro — *A Guided Tour to Approximate String Matching*, ACM Computing Surveys 33(1):31-88, 2001. [doi:10.1145/375360.375365](https://doi.org/10.1145/375360.375365)
- Boytsov — *Indexing methods for approximate dictionary searching: Comparative analysis*, ACM JEA 16, 2011. [doi:10.1145/1963190.1963191](https://doi.org/10.1145/1963190.1963191)
- Winkler — *String Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter Model of Record Linkage*, ASA Proceedings, 1990. [PDF](http://www.asasrms.org/Proceedings/papers/1990_056.pdf)
- Cohen, Ravikumar, Fienberg — *A comparison of string distance metrics for name-matching tasks*, IIWeb/AAAI 2003. [PDF](http://www.cs.cmu.edu/~wcohen/postscript/ijcai-ws-2003.pdf)
