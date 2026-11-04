# Compressão, MDL e códigos: a matemática do que já deu bom, medida no repo

**Kolmogorov → surrogate real, MDL de Rissanen → portão held-out, Kraft/Huffman → orçamento de nomes e atalhos,
aritmética → a ponte NLL=bits, rate-distortion → tolerância de asset, NCD → dedup sem embedding.**
Cada peça com veredito, prova ou medição, e o `file:line` que decide.

Pesquisa de 04/11/2026. Aprofunda **um único braço** de [unifying-math-offline.md](unifying-math-offline.md)
(§3.10, "forge = compressão MDL") e complementa [math-offline-brain.md](math-offline-brain.md) (calibração) e
[program-synthesis-library-learning.md](program-synthesis-library-learning.md) / [skill-induction-deep.md](skill-induction-deep.md)
(como assets nascem). Não repete a cascata, o NLU nem a decisão de Chow: aqui o objeto é **o custo de descrever** —
quantos bits um asset vale, quantos bits ele economiza, e quando "curto o bastante" é *provado* generalizar.

Nota de escopo honesta: o nome do arquivo diz "geometria da informação", mas o conteúdo é **teoria da informação +
complexidade algorítmica**. Geometria da informação no sentido estrito (Amari: variedades estatísticas, métrica de
Fisher-Rao, conexões duais) é **VIAGEM** para este sistema: o simplex do softmax tem geometria de Fisher, mas nenhuma
decisão do jazz melhora por saber disso — e o índice de resolubilidade de Barron & Cover (§3) já cobre a parte útil
(“o Fisher cobra `k/2·log n` por parâmetro”).

Convenções: `file:line` refere-se ao `main` no estado da escrita. **Medido nesta sessão** = números que produzi
agora com `zlib`/deflate nível 9 sobre os arquivos do repo — são *contagens de bytes*, invariantes de máquina; o
equivalente em Go é `compress/flate` com `BestCompression` (stdlib, confirmado: `flate.NewWriterDict` existe em
go1.27.2). **(estimativa)** = conta minha, não medida. "D610" = Dell Latitude D610, Pentium M Dothan 1,7 GHz,
1 núcleo, 2 MB de L2, 2 GB de RAM, `linux/386`, Go 1.27.2; a forja roda em amd64, o D610 só reusa os assets.

Este documento é adversarial por encomenda: **LOAD-BEARING** = há teorema e ele decide algo no jazz;
**METÁFORA ÚTIL** = enquadra mas não computa; **VIAGEM** = falso ou sem consequência — e digo por quê.

---

## 0. Placar

| # | Peça | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | Kolmogorov `K(x)` como ideal | **LOAD-BEARING como régua; VIAGEM como procedimento** | Incomputável (Li & Vitányi); mas `K(x) ≤ C(x)+O(1)` faz de qualquer compressor real uma cota superior computável — é o surrogate que o portão usa. |
| 2 | Compressor real como proxy de `K` | **LOAD-BEARING** (deflate medido) | A biblioteca inteira de assets: 160 278 B crus → 57 578 B deflate (35,9%); métodos 45 099 → 6 810 B. Proxy unilateral: superestima `K`, nunca subestima. |
| 3 | MDL de duas partes `L(H)+L(D\|H)` | **LOAD-BEARING** (é o portão) | `L(a\|lib)` medido por compressão com dicionário: 60–261 B por método. `L(D\|H)` = NLL held-out em bits (via aritmética, §5). Mesma unidade → `λ=1`. |
| 4 | "Simples o bastante pra generalizar" | **LOAD-BEARING** (com ressalva assintótica) | Barron & Cover: o estimador de complexidade mínima é consistente com taxa limitada pelo índice de resolubilidade `KL + L(H)/n`. Preço de parâmetro `(k/2)log₂n`: ~3,1 bits a n=75. |
| 5 | Desigualdade de Kraft | **LOAD-BEARING** (viabilidade de namespace) | 42 comandos + 10 aliases = 52 nomes sobre ~36 atalhos de 1 caractere: Σᵢ36^{−1} = 52/36 = 1,44 > 1 — *inviável*; por isso comandos são palavras + fuzzy, não teclas. |
| 6 | Huffman | **LOAD-BEARING como cota; METÁFORA no menu** | `H_D ≤ E[L*] < H_D+1` vale para qualquer código de prefixo — limita os caracteres esperados de *digitar-para-filtrar* nos pickers. No menu de 1 tecla é vazio (ver §4 e unifying §3.4). |
| 7 | Codificação aritmética | **LOAD-BEARING o teorema; VIAGEM a implementação** | `ℓ(x) ≤ −log₂ p(x) + 2` ⇒ NLL *é* o comprimento do código — `L(D\|H)` computável sem serializar nada. Implementar um codec aritmético no i386 é desnecessário: flate já é LZ77+Huffman. |
| 8 | Rate-distortion `R(D)` de Shannon | **METÁFORA ÚTIL** (incomputável aqui) | `R(D)=min I(X;X̂)` s.a. `E[d]≤D` exige a distribuição-fonte. O que mede é a **curva operacional** bytes×distorção (ablação/quantização) — e ela existe. |
| 9 | Alocação Lagrangiana `D+λR` | **LOAD-BEARING** | Shoham & Gersho: no ótimo, todas as taxas marginais igualam `λ`. O objetivo do forge `Ê+λ·L` é literalmente isto — e em unidades NLL `λ=1`. |
| 10 | Information Bottleneck (Tishby) | **METÁFORA ÚTIL** | Nomeia a distorção certa ("preservar `I(asset; sucesso)`"), mas `I` também não é computável. A curva de ablação é o proxy. |
| 11 | NCD para dedup | **LOAD-BEARING com piso de tamanho** | Medido: pares da mesma família 0,13–0,35; famílias distintas 0,55–0,84; `NCD(x,x)≈0,04`. Em strings de ~14 B vira ruído (0,2 por 1 char). Sem embedding, zero token — mas é flag de revisão, não deleção. |

**Resumo de uma frase:** a parte que deu bom não é "compressão" no abstrato — são três contas concretas:
`(a)` o preço de um asset em bits contra a biblioteca (surrogate de Kolmogorov com dicionário), `(b)` o preço dos
dados sob a biblioteca em bits (NLL via teorema da aritmética), `(c)` o break-even `N* = ΔL/Δb̄` — que é *a mesma
forma funcional* da amortização `E_forge + N·E_offline` já provada, só que trocando tokens por bits.

---

## 1. A tese, decomposta

O dono quer aprofundar a matemática do que já deu bom: **inteligência = compressão/MDL**. Decomposta em
afirmações testáveis:

- **C1.** "Asset bom = asset curto que ainda resolve" é uma afirmação formal: é MDL de duas partes, e a resposta a
  "simples o bastante pra generalizar" é um critério computável, não um palpite.
- **C2.** Existe um surrogate computável de `K(x)` bom o bastante para cobrar preço de descrição por asset —
  e ele é `C(x|dicionário)`, não `|x|` nem `gzip(x)` cru.
- **C3.** O segundo termo do MDL, `L(D|H)`, não exige escrever um compressor: pela codificação aritmética,
  ele *é* o NLL do modelo em bits, `−Σ log₂ p̂(yᵢ|xᵢ)`.
- **C4.** Atalhos de menu, aliases e pickers vivem sob a desigualdade de Kraft: nomes curtos são recurso escasso,
  e a viabilidade de um desenho se checa com `Σ D^{−lᵢ} ≤ 1` antes de discutir UX.
- **C5.** "Quanto um asset pode perder e ainda funcionar" é uma pergunta de rate-distortion; Shannon não a
  responde (falta a fonte), mas a curva operacional responde — por ablação e quantização medidas.
- **C6.** Dedup de asset sem embedding existe: é a NCD, com ressalvas de tamanho e de "compressor normal".

O resto do documento prova ou mede cada uma.

---

## 2. Kolmogorov e os surrogates que o D610 pode pagar

### 2.1 O teorema e a direção da cota

`K_U(x)` = comprimento do menor programa que emite `x` numa máquina universal `U`. Dois fatos definem o uso:

1. **`K` é incomputável** (caso contrário o paradoxo de Berry — "o menor número indescritível em menos de n
   palavras" — resolve o halting problem). Li & Vitányi, *An Introduction to Kolmogorov Complexity and Its
   Applications*, Springer 2008, [doi:10.1007/978-0-387-49820-1](https://doi.org/10.1007/978-0-387-49820-1), §1.7,
   2.3. Portanto: nenhum benchmark jamais "mede `K`".
2. **Todo compressor computável é cota superior.** Se `D` descomprime e `C(x)` é o programa comprimido,
   `K_U(x) ≤ |C(x)| + c_D + O(1)` — o descompressor cabe numa constante. E nenhuma cota inferior computável
   existe. Ou seja: `C(x)` **superestima** `K(x)`, sempre; pode estar grotescamente acima (os dígitos de π
   comprimem como ruído para o deflate mas têm `K ≈ log n`). O surrogate é **unilateral** — honesto para cobrar
   *custo de descrição*, desonesto para alegar "aproximamos K".

**Veredito: LOAD-BEARING como régua (o preço cobrado é real e no sentido seguro — caro demais, nunca barato
demais); VIAGEM como promessa de "medir a complexidade verdadeira".**

### 2.2 O que o surrogate mede no repo, de fato (medido nesta sessão)

Deflate nível 9, streams crus (`zlib` wbits=-15; em Go: `compress/flate`, `BestCompression`):

| Objeto | cru | deflate | razão |
|---|---|---|---|
| `methods/` (49 arquivos, concatenados) | 45 099 B | **6 810 B** | 15,1% |
| `nlu/intents.json` | 39 425 B | 7 886 B | 20,0% |
| `nlu/lexicon.json` | 16 000 B | 4 456 B | 27,9% |
| `nlu/blind_test.json` | 6 251 B | 1 791 B | 28,7% |
| `evals/offline/tasks.json` | 19 218 B | 3 917 B | 20,4% |
| **corpus embutido total** (methods+nlu+skills+triggers+diffrules+guardrails_top) | **160 278 B** | **57 578 B** | **35,9%** |
| `deflate("")` (stream vazio) | 0 | 2 B | overhead do stream |

Leitura: o "cérebro offline" inteiro — biblioteca de métodos, gramática NLU, skills, regras de diff e guardas —
**cabe em ~58 KB comprimidos**. Esse é o surrogate honesto de `K(biblioteca)`: o cérebro é um programa de
~58 KB que resolve 75 tarefas sem token. A filosofia "inteligência em tempo de compilação" tem um número:
o conhecimento reutilizado custa ~58 KB no dispositivo, não 13 MB de binário nem chamadas de API.
(Para o enquadramento "LLM como compressor": Delétang et al., *Language Modeling Is Compression*, ICLR 2024,
[arXiv:2309.10668](https://arxiv.org/abs/2309.10668) — todo modelo preditivo é um compressor e vice-versa;
Chinchilla-70B comprime ImageNet a 43% melhor que PNG. A comparação correta no jazz é de *tamanho de
programa*, não de pesos: o asset é código curto especializado, não distilação de representação.)

### 2.3 Qual compressor usar, e a armadilha do dicionário

Hierarquia de surrogates por força (e por custo no D610):

| Surrogate | Classe | Custo | Nota |
|---|---|---|---|
| `len(x)` | nenhum | 0 | ignora toda regularidade — é a "taxa nominal" |
| Huffman ordem-0 / `len` após canônico | estatístico | ~O(n) | captura só frequência de byte |
| **deflate (`compress/flate`)** | LZ77+dicionário implícito | janela 32 KB | **escolha do jazz** — stdlib, medido acima |
| PPM (Cleary & Witten 1984, [doi:10.1109/TCOM.1984.1096090](https://doi.org/10.1109/TCOM.1984.1096090)) | Markov adaptativo + aritmética | estado de contexto | melhor em texto curto; fora da stdlib |
| CTW (Willems, Shtarkov & Tjalkens 1995, [doi:10.1109/18.382012](https://doi.org/10.1109/18.382012)) | mistura de árvores | linear em n, memória modesta | ótimo contra fontes-árvore; implementável mas não trivial |
| PAQ/CM | mistura de especialistas | ~GB de RAM | **inviável no D610** |
| LLM (Delétang) | fronteira | rede | **VIAGEM**: LLM no caminho crítico = dependência |

O detalhe que muda tudo para assets: `K(x)` não é o preço certo — o portão cobra `K(x | biblioteca)`, a
descrição **condicional**. Em deflate isso é literal: `flate.NewWriterDict` comprime `x` usando `dict` como
janela pré-carregada, aproximando `C(x|dict)`. Medido (marginal de cada método contra o resto da biblioteca):

| método | cru | sozinho | `C(·\|lib∖{·})` |
|---|---|---|---|
| `disk_usage.json` | 326 B | 209 B | **60 B** |
| `container_up.json` | 858 B | 299 B | **108 B** |
| `create.json` | 875 B | 337 B | **110 B** |
| `delete_file.json` | 886 B | 330 B | **124 B** |
| `container_list.json` | 1 101 B | 374 B | **134 B** |
| `build.json` | 1 223 B | 485 B | **195 B** |
| `connect_host.json` | 1 088 B | 507 B | **261 B** |

Um método novo não custa seus ~900 B crus nem seus ~400 B comprimidos: **custa ~100–260 B (800–2 100 bits)
contra a biblioteca que já existe** — o schema JSON, os nomes de caps e os campos já são "vocabulário pago".
Ressalva de implementação: a janela do deflate é 32 KB; dicts maiores têm a cauda ignorada — então o
dicionário deve ser os ≤32 KB mais relevantes (mesma família de intent + schema comum), não a biblioteca crua
de 160 KB.

---

## 3. MDL de Rissanen: "simples o bastante" é um critério computável

### 3.1 O princípio e o teorema que o liga à generalização

MDL (Rissanen, *Modeling by shortest data description*, Automatica 14(5):465–471, 1978,
[doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5)): escolher `H` minimizando

```
L₂(H, D) = L(H) + L(D | H)
```

`L(H)` = comprimento da hipótese; `L(D|H)` = comprimento dos dados codificados com ela. Forma didática:
Grünwald, *The Minimum Description Length Principle*, MIT Press 2007,
[doi:10.7551/mitpress/4643.001.0001](https://doi.org/10.7551/mitpress/4643.001.0001); revisão aplicada:
Hansen & Yu, *Model Selection and the Principle of Minimum Description Length*, JASA 96(454):746–774, 2001,
[doi:10.1198/016214501753168398](https://doi.org/10.1198/016214501753168398).

**Por que "curto" generaliza (o teorema que o dono precisa):** Barron & Cover, *Minimum Complexity Density
Estimation*, IEEE Trans. Inf. Theory 37(4):1034–1054, 1991,
[doi:10.1109/18.86996](https://doi.org/10.1109/18.86996): o estimador de complexidade mínima é **consistente**
e sua taxa de convergência é limitada pelo **índice de resolubilidade**

```
R_n = min_H [ KL(P ‖ H) + L(H)/n ]
```

— o melhor compromisso entre *erro de aproximação* (quão bem `H` descreve a fonte) e *erro de estimação*
(quanto custa descrever `H` com `n` amostras). Traduzindo para o jazz: **um asset "simples o bastante" é aquele
cujo `ΔL` é pago pelo ganho em `L(D|H)` na taxa ≥ 1 por tarefa observada** — e overfitting é, literalmente,
"pagar descrição demais por ganho de menos". É a formalização exata do anti-overfit que o dono pediu.

Ressalva adversarial: Barron–Cover é assintótico. Com `n = 75` tarefas o teorema é *guia de forma*, não
garantia — por isso o portão mantém os bloqueantes atuais (`false_success = 0`, não-regressão,
melhoria em held-out — `forge_gate.go:152-171`) e o MDL entra como **critério de preço**, não como
substituto da validação.

### 3.2 O preço universal do parâmetro: `(k/2)·log n`

Rissanen 1984, *Universal coding, information, prediction, and estimation*, IEEE Trans. Inf. Theory
30(4):629–636, [doi:10.1109/TIT.1984.1056936](https://doi.org/10.1109/TIT.1984.1056936): para famílias
paramétricas de `k` graus de liberdade, **nenhum** código universal desce abaixo de

```
redundância ≈ (k/2)·log₂ n + O(1)   bits
```

(exceto para subconjunto de medida nula dos parâmetros). É o mesmo número do BIC (Schwarz, *Estimating the
dimension of a model*, Ann. Statist. 6(2):461–464, 1978,
[doi:10.1214/aos/1176344136](https://doi.org/10.1214/aos/1176344136)) e o piso que o CTW atinge
(Willems et al. 1995, acima). No jazz: `n = 75` tarefas → cada grau de liberdade novo custa
`½·log₂75 ≈ 3,1` bits; `n = 644` exemplos NLU → `≈ 4,7` bits. **Todo "parâmetro livre" de um asset — cada
campo novo, cada regra nova, cada nó de calibração — tem um mínimo de entrada**: se não economiza mais do
que isso em `L(D|H)`, é complexidade que se paga sozinha. (Refinamento: a complexidade paramétrica exata é a
do stochastic complexity/NML — Rissanen, *Fisher information and stochastic complexity*, IEEE Trans. Inf.
Theory 42(1):40–47, 1996, [doi:10.1109/18.481776](https://doi.org/10.1109/18.481776) — intratável aqui; o
código de duas partes com `L` fixo é a versão computável, e é o que usamos.)

### 3.3 A ponte que faz o MDL computável: NLL = bits

O termo `L(D|H)` parece exigir um codec. Não exige — e essa é a contribuição central deste doc.
Pela codificação aritmética (§5), codificar `x` com probabilidade `p(x)` custa `≤ −log₂ p(x) + 2` bits
**no total**. Então, no corpus held-out `D = {(xᵢ, yᵢ)}`:

```
L(D | H) ≈ Σᵢ −log₂ p̂(yᵢ | xᵢ)  =  NLL em bits
```

*é* o comprimento da descrição dos rótulos sob o modelo — sem serializar byte nenhum. O `p̂` já existe:
`predict` (`nlu.go:1434-1478`) devolve `p` temperado; para ser usado como código ele precisa ser *calibrado*
(um código que atribui `p̂=0,97` ao que acerta 60% é um código ruim — paga bits demais; é o argumento
information-theoretic para ligar `calib.go`, hoje morto — ver unifying §3.2 e P1/P2 de lá).

**E as unidades fecham sem `λ` arbitrário:** `L(H)` em bits = `8·C_dict(a|lib)`; `L(D|H)` em bits = NLL.
O gate MDL é **adimensional**:

```
aceitar  ⇔  8·C_dict(a | L)  +  [ NLL_out(L ∪ {a}) − NLL_out(L) ]  <  0
```

Note que isto *remove* a ambiguidade do `λ` do objetivo `J = Ê + λ·L(L)` formulado em unifying §3.10: quando o
custo do erro é expresso em bits (log-loss, uma *proper scoring rule*, mais fina que accuracy 0/1), `λ = 1`
por definição — os dois termos são o mesmo número.

### 3.4 A conta brutal — e por que ela vira o `N*` que já deu bom

Held-out típico do portão: fração 0,25 → ~19 tarefas de 75 (`Gate(exs, base, cand, 0.25, 42)` em
`forge_gate_test.go`). Quanto vale "consertar uma tarefa" em bits? Com `p̂` calibrado, virar uma tarefa de
"falha confiante" (`p̂ ≈ 0,1`) para "sucesso confiante" (`p̂ ≈ 0,9`) economiza `log₂(9) ≈ 3,2` bits
*(estimativa; o número exato sai da instrumentação de P1)*. Um asset com `ΔL = 100 B = 800` bits precisaria
de **~250 viradas de tarefa** — em 19 exemplos held-out, **literalmente nenhum asset passa o MDL estrito num
único eval**. Isso não é bug do critério: é o critério dizendo a verdade — a descrição se paga na *vida* do
asset, não no corpus de validação.

A forma honesta é amortizar, e aí aparece a estrutura que já deu bom:

```
asset paga-se  ⇔  Δb̄ · N  >  L(a | lib)        N* = L(a | lib) / Δb̄
```

`Δb̄` = ganho médio de NLL por tarefa (medido no held-out), `N` = nº de tarefas futuras dessa distribuição.
**É a mesma forma funcional do break-even de tokens** `E_forge + N·E_offline ≤ N·E_llm` que o projeto já
provou — `E_forge` virou `L(a|lib)` bits de descrição, `E_offline` virou `−Δb̄` bits por tarefa. O portão
reporta `N*` junto do veredito: assets só se pagam se (a) `ΔL` pequeno (≤ ~300 B marginais, como os métodos
medidos) **e** (b) efeito frequente. Asset grande + efeito raro = nunca se paga → rejeitado pela aritmética,
não por opinião. **Isso é "simples o bastante pra generalizar" operacionalizado.**

### 3.5 A tabela adversarial: onde o MDL morde e onde não

| Asset | `L(H)` honesto | `L(D\|H)` medível? | Veredito MDL |
|---|---|---|---|
| Método novo (`methods/*.json`) | `C_dict` marginal: **60–261 B** medido | sim, via NLL/sucesso calibrado | **LOAD-BEARING** — o caso real |
| Exemplos NLU novos | bytes do exemplo | sim, NLL do classificador | **LOAD-BEARING** — paga por exemplo que conserta |
| Nós de calibração (~16 int16 ≈ 32 B) | trivial | sim, ECE/NLL | **LOAD-BEARING** — melhor custo-benefício da fila |
| Léxico/regras | bytes do delta | sim | **LOAD-BEARING** |
| Prosa (lições, `doc` de método) | bytes de texto | **não** — prosa não é código de decisão | **METÁFORA** — `doc` só é exibido (`apimap.go:114`); é lossless-cortável |
| Tabela NB (1,64 MB) | esparso ≈ baldes ≠ `zero` | sim | ver §6.4 — é pergunta de rate-distortion, não de aceitação |

---

## 4. Kraft e Huffman: atalhos são um código, e 52 nomes não cabem em 36 teclas

### 4.1 A desigualdade e sua prova de 6 linhas

**Kraft (1949) / McMillan (1956).** Para alfabeto `D`-ário, existe código de prefixo (instantâneo) com
comprimentos `l₁…l_n` **se e somente se**

```
Σᵢ D^{−lᵢ} ≤ 1.
```

Kraft provou a direção para códigos de prefixo (tese de mestrado MIT, *A device for quantizing, grouping and
coding amplitude-modulated pulses*, 1949); McMillan estendeu para **toda** código univocamente decodificável
— McMillan, *Two inequalities implied by unique decipherability*, IRE Trans. Inf. Theory 2(4):115–116, 1956,
[doi:10.1109/TIT.1956.1056818](https://doi.org/10.1109/TIT.1956.1056818). Consequência: códigos não-prefixo
não ganham nada — a fronteira é a mesma.

**Prova (intervalos, Cover & Thomas cap. 5, Teorema 5.2.1 —
[PDF](https://ftp.esat.kuleuven.be/pub/pub/SISTA/decock/voor_xander/referenties/Cover&Thomas/5.pdf)):**
cada palavra-código `c` de comprimento `l` corresponde ao cilindro `[0.c, 0.c + D^{−l})` em `[0,1)`, largura
`D^{−l}` (as `D`-árias expansões que começam com `c`). Prefixo-livre ⇒ cilindros disjuntos ⇒ soma das
larguras ≤ 1. Reciprocamente, dados `lᵢ` com `Σ D^{−lᵢ} ≤ 1`: ordene crescente, alinhe cilindros da esquerda,
leia os `lᵢ` primeiros dígitos do endereço de cada cilindro — é um código de prefixo com esses comprimentos. ∎

**Corolário (Shannon 1948, fonte-conversa):** para qualquer código univocamente decodificável,
`E[L] ≥ H_D(X)`. Combinando com Huffman abaixo: `H_D(X) ≤ E[L*] < H_D(X) + 1`.

### 4.2 Huffman: o lema da troca em 4 linhas

**Huffman 1952** (*A Method for the Construction of Minimum-Redundancy Codes*, Proc. IRE 40(9):1098–1101,
[doi:10.1109/JRPROC.1952.273898](https://doi.org/10.1109/JRPROC.1952.273898)): existe código ótimo em que os
dois símbolos menos prováveis são irmãos no nível mais profundo (se num código ótimo um símbolo mais provável
está mais fundo que um menos provável, trocar seus codewords não aumenta `E[L]` — repita até os dois
menores estarem juntos no fundo); funda-os num meta-símbolo e induza — o algoritmo guloso resultante é ótimo
entre códigos de prefixo. A cota `E[L] < H+1` sai de `lᵢ = ⌈log_D(1/pᵢ)⌉`, que satisfaz Kraft. ∎

### 4.3 Onde isso morde no jazz — com números medidos

**O namespace de atalhos já está sob Kraft.** Contados no repo: **42 comandos** (`slashCommands`,
`main.go:1505-1550`) + **10 aliases** (`fzAliases`, `fuzzy_cmd.go:14-16`) = **52 nomes**. Alfabeto de atalho
de 1 caractere ≈ 36 (`a-z0-9`). Kraft:

```
52 × 36⁻¹ = 1,44 > 1   →   INVIÁVEL
```

Dar atalho de 1 caractere a todo comando é *impossível por teorema* — não por escolha de UX. É por isso que
comandos são palavras completas + resolução fuzzy (`fzResolve`, `fuzzy_cmd.go:45-80`): o "código" de um
comando é seu **prefixo único mais curto**, e completar por Tab (`completeCmd`, `main.go:1550-1558`) é
literalmente navegar uma árvore de prefixos. Kraft é a ferramenta de *design*: qualquer proposta de conjunto
de atalhos se checa primeiro com `Σ D^{−lᵢ} ≤ 1` (proposta P7 — custo zero, roda em teste).

**Menu de 1 tecla: Kraft satisfeito de graça, Huffman vazio — como já demonstrado.** Em `menu.go`/`confirm.go`,
cada `mnItem` tem `Key`/`Keys` (`menu.go:29-30`) ou dígito: custo 1 toque para qualquer item. É um código de
comprimento fixo; a desigualdade só exige `#itens ≤ #teclas`. O teorema que ordena o menu é o da busca linear
(Knuth, `p` decrescente — unifying §3.4; `offers.go:158-175` monta em ordem fixa, não por frequência).
**Veredito para o menu: METÁFORA** — e o doc anterior já o disse; aqui só registro que *Kraft também se
aplica* (é a checagem de colisão de `Key`), embora trivial.

**Onde Huffman é cota de verdade: digitar-para-filtrar nos pickers.** `mnSpec.Filter` consome letras e
refiltra a lista (`menu.go:78-97`); o usuário para de digitar quando o alvo é distinguível. A sequência
digitada que identifica o item `i` é, literalmente, **a palavra-código de `i` sobre o alfabeto do teclado** —
e todo esquema de "digite até desambiguar" é um código de prefixo, logo

```
E[caracteres digitados] ≥ H(p_escolha) / log₂ |alfabeto|
```

Para `pickTurn` (mensagens, `confirm.go:184-195`), pickers de sessão/modelo: o limite inferior é entrópico;
o que o sistema controla é (a) a **ordem** (probabilidade decrescente — Knuth, de novo) e (b) os **nomes**,
que determinam os prefixos discriminantes. Não dá para atribuir codewords livremente — a semântica
substring-match do filtro restringe o código — então o teorema funciona como **cota e direção**, não como
algoritmo. **Veredito: LOAD-BEARING como bound; a implementação é ordenação + nomes curtos discriminantes.**

---

## 5. Codificação aritmética: o teorema que o jazz usa sem implementar

### 5.1 O mecanismo e o teorema

Aritmética (Rissanen, *Generalized Kraft inequality and arithmetic coding*, IBM J. Res. Dev. 20(3):198–203,
1976, [doi:10.1147/rd.203.0198](https://doi.org/10.1147/rd.203.0198); forma prática: Witten, Neal & Cleary,
*Arithmetic coding for data compression*, CACM 30(6):520–540, 1987,
[doi:10.1145/214762.214771](https://doi.org/10.1145/214762.214771)): mantém-se um intervalo `[low, high)` em
`[0,1)`, subdividido proporcionalmente às probabilidades do modelo; cada símbolo estreita o intervalo; no fim,
qualquer número dentro dele decodifica a mensagem. O intervalo final tem largura `p(xⁿ)`, e

```
ℓ(xⁿ) = ⌈log₂ (1/p(xⁿ))⌉ + 1   ⇒   ℓ(xⁿ) ≤ −log₂ p(xⁿ) + 2  bits no TOTAL
```

— overhead de ≤2 bits por *mensagem inteira*, não por símbolo (vs. Huffman: até ~1 bit **por símbolo**).
Cover & Thomas, cap. 13. O slogan de Witten–Neal–Cleary é o ponto arquitetural: **"separa o modelo do
canal"** — qualquer `p̂` vira código, e qualquer código define um `p̂`.

### 5.2 Consequência para o jazz: o MDL é computável — o codec, não

O §3.3 usa exatamente esse teorema: `L(D|H) = −Σ log₂ p̂(yᵢ|xᵢ) + O(1)`. **Não precisamos implementar
aritmética para cobrar esse comprimento** — o NLL *é* o comprimento, a menos de 2 bits por mensagem (e, se
fôssemos pedantes, o código de Shannon-Fano-Elias correspondente existe). A frase operacional:

> **log-loss calibrado e comprimento de descrição são o mesmo número.** Avaliar `p̂` em held-out é medir
> `L(D|H)` — a metade "cara" do MDL sai de graça numa varredura do corpus.

Implementar um codificador aritmético no jazz é **VIAGEM**, com motivo concreto: (a) `compress/flate` já é
LZ77 + Huffman dinâmico — em corpos de KB a redundância extra de Huffman sobre a entropia é irrisória perante
o que o modelo de contexto ganha; (b) no i386 a aritmética de carry/renormalização em 32 bits é exatamente o
tipo de código que gera bug sutil sem benefício mensurável; (c) o uso do teorema é *analítico* (converter
NLL↔bits), não transmissão real. **O teorema é LOAD-BEARING; o codec é VIAGEM.**

---

## 6. Rate-distortion: quanto um asset pode perder

### 6.1 O teorema de Shannon — e por que ele não responde nossa pergunta

`R(D) = min_{p(x̂|x): E[d(x,x̂)] ≤ D} I(X; X̂)` (Shannon, *Coding theorems for a discrete source with a
fidelity criterion*, IRE Nat. Conv. Rec. 7, 1959,
[doi:10.1109/9780470544242.ch21](https://doi.org/10.1109/9780470544242.ch21); Blahut 1972 dá o algoritmo de
cálculo, [doi:10.1109/TIT.1972.1054855](https://doi.org/10.1109/TIT.1972.1054855)). Para o jazz falta o
ingrediente essencial: **a distribuição da fonte** (de "bibliotecas possíveis" ou de "inputs"). `R(D)` é um
envelope teórico; o que temos é **um ponto de operação por variante**. Veredito para a função: **METÁFORA
ÚTIL** — enquadra a pergunta "quanto pode perder", não a responde.

### 6.2 O que responde: a curva operacional e o Lagrangiano

A engenharia de codecs usa **R-D operacional**: medir `(taxa, distorção)` dos pontos realmente implementáveis
e tomar a envoltória — Ortega & Ramchandran, *Rate-distortion methods for image and video compression*,
IEEE Signal Processing Mag. 15(6):23–50, 1998,
[doi:10.1109/79.733495](https://doi.org/10.1109/79.733495). E a alocação ótima: **Shoham & Gersho**,
*Efficient bit allocation for an arbitrary set of quantizers*, IEEE Trans. ASSP 36(9):1445–1453, 1988,
[doi:10.1109/29.90373](https://doi.org/10.1109/29.90373): varrer `λ` e minimizar `D_i + λR_i` por
componente; no ótimo, todos os pontos operados têm declive marginal `λ`. **É literalmente a forma do
objetivo do forge.** Cada asset é um "quantizador" da distribuição de tarefas: `R_i` = seus bytes, `D_i` =
o que falta sem ele. A regra de alocação de Shoham–Gersho vira regra de biblioteca: *cada asset carrega seu
preço em bits e sua redução de distorção — no ponto ótimo, nenhum asset tem custo-benefício pior que a margem*.

A distorção honesta para o jazz não é MSE — é `Δ` de custo de tarefa held-out (sucesso, NLL, perguntas).
Essa é uma medida **comportamental**, não geométrica: tudo bem — a curva operacional não exige métrica de
distorção formal, só um número medido.

**Information Bottleneck (Tishby, Pereira & Bialek 1999/2000,
[arXiv:physics/0004057](https://arxiv.org/abs/physics/0004057))** é o nome correto para "o que pode ser
perdido": comprimir `X` preservando `I(X̃; Y)` com `Y` = sucesso da tarefa. É a versão R-D "com a distorção
certa" — mas `I` também não é computável no nosso regime, então fica como **METÁFORA ÚTIL** que aponta para o
procedimento real: a **curva de ablação** (§6.4).

### 6.3 Onde a perda é grátis, e onde custa (medido)

Nos métodos, 6 262 B de 45 099 B (14%) são o campo `doc` (`methods.go:63`) — prosa exibida só em
`apimap.go:114-115`, **cega ao comportamento**: remover/truncar `doc` tem *distorção zero* de execução.
É "perda" que não é perda — o primeiro ponto da curva operacional é trivial. (Guarda: `doc` pode ser
evidência para humanos/forja; cortar só no payload de runtime, não na fonte.)

### 6.4 O experimento canônico: quantizar a tabela NB

A tabela do classificador é o maior asset único: `nuBuckets = 2^14`, `nuScale = 256` (1/256 nat), `int16`
por (classe, balde) → 50 × 16 384 × 2 B = **1 638 400 B** (`nlu.go:1330-1422`), ~78% da L2 do D610
(número já registrado em unifying §2). Pergunta R-D: **quanto `D` a tabela tolera?** Pontos da curva
operacional a medir:

| Variante | taxa | distorção a medir |
|---|---|---|
| int16 @1/256 nat (hoje) | 1 638 400 B | 0 (referência) |
| int8 @1/128 nat (`nuScale=128`) | 819 200 B | # argmax-flips nos 644 exemplos + blind; ΔECE |
| linhas esparsas (só baldes ≠ `zero` + base compartilhada) | ~O(baldes ocupados) — estimativa: a maioria dos baldes é o valor `zero` (`nlu.go:1399-1416`) | flips + latência de lookup (1 branch/balde) |
| poda de features (IG/χ², P6 de math-offline-brain) | − até ~90% das linhas | flips + ECE |

A "tolerância do método" é exatamente a resposta: se int8 não vira nenhum argmax no corpus e deixa a ECE
intacta, a tabela estava com ~800 KB de "descrição" não paga — e a margem de decisão (`predict` soma
`int32` sobre ~100 n-gramas, `nlu.go:1444`) nos diz o ruído de quantização admissível: `n·(passo/2)` nats.
A curva `(bytes, flips)` é o rate-distortion operacional do jazz — **LOAD-BEARING como procedimento,
METÁFORA como fórmula**.

---

## 7. NCD: dedup de asset sem embedding

### 7.1 Da distância de informação à versão computável

Bennett, Gács, Li, Vitányi & Zurek, *Information distance*, IEEE Trans. Inf. Theory 44(4):1407–1423, 1998,
[doi:10.1109/18.681318](https://doi.org/10.1109/18.681318): `E(x,y) = max{K(x|y*), K(y|x*)}` — métrica
universal de dissimilaridade (minoriza toda distância computável, a menos de constantes). Versão normalizada:
Li, Chen, Li, Ma & Vitányi, *The similarity metric*, IEEE Trans. Inf. Theory 50(12):3250–3264, 2004,
[doi:10.1109/TIT.2004.838101](https://doi.org/10.1109/TIT.2004.838101). Substituindo `K` por compressor
real: **NCD** de Cilibrasi & Vitányi, *Clustering by compression*, IEEE Trans. Inf. Theory 51(4):1523–1545,
2005, [doi:10.1109/TIT.2005.844059](https://doi.org/10.1109/TIT.2005.844059),
[arXiv:cs/0312044](https://arxiv.org/abs/cs/0312044):

```
NCD(x,y) = [ C(xy) − min(C(x), C(y)) ] / max(C(x), C(y))
```

Válida sob axiomas de "compressor normal" (idempotência `C(xx)=C(x)`, monotonicidade, simetria,
distributividade). **Nenhum embedding, nenhum token, stdlib pura** — encaixa na regra de ouro offline.

### 7.2 Medido nesta sessão (deflate-9, stream cru)

| par | NCD concat | NCD dict |
|---|---|---|
| `service_start` × `service_stop` | **0,127** | **0,137** |
| `disk_usage` × `mem_usage` | **0,307** | 0,348 |
| `build` × `run_tests` | 0,472 | 0,534 |
| `git_status` × `git_log` | 0,550 | 0,575 |
| `container_up` × `container_list` | 0,658 | 0,739 |
| `git_status` × `disk_usage` (famílias distintas) | **0,801** | 0,838 |
| `x` × `x` (idempotência) | 0,035–0,045 | — |

Separação clara: mesma família ≤ ~0,35; famílias distintas ≥ ~0,5. `NCD(x,x) ≈ 0,04` ≈ normal o bastante
em ~1–2 KB. **E o piso de tamanho é real:** em strings de ~14 B, 1 caractere de diferença já dá 0,2 — NCD
desce para o ruído quando o próprio objeto cabe no overhead do código. Regra operacional: **pisar mínimo
~200 B** ou normalizar por padding; abaixo disso, dedup por chave/normalização textual, não por compressor.
Cebrián, Alfonseca & Ortega, *Common pitfalls using the normalized compression distance*, Communications in
Information & Systems 5(4):367–384, 2005,
[doi:10.4310/CIS.2005.v5.n4.a1](https://doi.org/10.4310/CIS.2005.v5.n4.a1): os compressores reais falham
idempotência quando o objeto excede a janela e enviesam por tamanho — exatamente o que a tabela mostra.

### 7.3 Variante com dicionário — e onde aplicar

Para objetos pequenos, `C(y|x)` (dict) é melhor condicionado que `C(xy)`: a janela já vem "quente". Medido
acima — `NCD_dict` acompanha a concat dentro de ~0,08 e separa igual. Em Go: `flate.NewWriterDict`
(§2.3, janela ≤32 KB). Simetriza por `max(D(x,y), D(y,x))` — aproxima o `max` de `E`.

**Onde vira código:**

- `improve.go:251` — dedup de lições hoje é chave exata `Class|Key`; near-dups com chaves diferentes passam.
  NCD como **segunda passada de flag** (revisão humana decide o merge — nunca auto-delete).
- `methods/*.json` — 49 arquivos; pares `service_start/stop` (0,13) e `disk/mem_usage` (0,31) já acendem:
  dedup **estrutural** (comparar o JSON canônico sem `doc`) indica assets que poderiam ser parametrizados
  num só — liga com [program-synthesis-library-learning.md](program-synthesis-library-learning.md) e o
  crescimento de biblioteca de [skill-induction-deep.md](skill-induction-deep.md).
- `evals/offline/tasks.json` — 75 tarefas; near-dups de prompt **inflam** cobertura (`no_llm_rate` mede
  tarefas distintas, não variações do mesmo prompt). NCD no campo `prompt` como lint do corpus.
- `skills/*/SKILL.md` — 14 corpos de skill; mesmo procedimento.

**Veredito: LOAD-BEARING como flag de revisão à escala da biblioteca (separação medida, custo ~zero, zero
dependências); VIAGEM como deleção automática e como métrica semântica** — deflate mede substrings comuns;
dois métodos com o mesmo schema e caps diferentes são "superficialmente" parecidos, o que para dedup é
aproximadamente certo mas não é julgamento semântico.

---

## 8. Mapa file:line consolidado

| Conceito | Onde mora no código | Linha |
|---|---|---|
| Portão held-out (base do MDL) | `forge_gate.go` — `Metrics`, `EvalFn`, `Gate`, `Split` | :46-56, :143-172, :92-126 |
| NLL potencial (`p` por predição) | `nlu.go` — `predict`, scores `int32` em 1/256 nat, `nuScale`/`nuBeta` | :1434-1478, :1444, :1330-1332 |
| Calibração (pré-requisito do `p̂`-como-código) | `calib.go` — `ECE`/`BrierScore`/`PlattFit`/`IsotonicFit` (**morto hoje**) | :164, :198, :220, :281 |
| Tabela NB (alvo de quantização R-D) | `nlu.go` — `nb.table` int16, `zero` por classe, `prior` int32 | :1395-1422 |
| Assets (biblioteca `L`) | `methods/*.json` — `mhMethod{…, Doc}`, `mhLibrary`, `mhBadCmd` | `methods.go`:60-72, :81-111, :115-122 |
| Métricas do eval (onde o Δ se mede) | `evaloffline.go` — `eoGot.Conf`, `eoSummary` | :64-77, :91-107 |
| Dedup por chave exata (hoje) | `improve.go` — `k := c.Class + "\|" + c.Key`; `lessons.go` cap | :251, `lessons.go`:243 |
| Namespace sob Kraft | `slashCommands` (42), `fzAliases` (10), `completeCmd`, `fzResolve` | `main.go`:1505-1558, `fuzzy_cmd.go`:14-80 |
| Atalhos de menu (`l=1`) | `mnItem.Key/Keys`, `cliChoose` match | `menu.go`:29-30, `confirm.go`:221-235 |
| Filter pickers (`l` variável = prefixo discriminante) | `mnSpec.Filter`, `mnState.refilter`, `pickTurn` | `menu.go`:42,78-97, `confirm.go`:184-195 |
| Ledger de sinais (corpus p/ forja) | `signals.go` — `Signal`, `sigAppend`, `sigRead` | :26-36, :52-78, :84-106 |
| Compressão já viva no repo (precedente) | `gitindex.go` — prefix compression do index v4; `bug.go` gzip; `buildtarget.go` gzip | :126-137, `bug.go`:79, `buildtarget.go`:560 |
| Ledger de custo por pedido | `evaloffline.go` `eoSummary` (p50/p95/RSS, no_llm, false_success) | :91-107 |

---

## 9. O que dá para implementar em Go no Dell

Ordenado por valor esperado. Métrica = a que move `jazz eval offline` (`no_llm_rate`, `false_success`,
`false_blocks`, perguntas, `commands_per_task`, p50/p95, RSS) + os números novos (`NLL_bits`, `ΔL`,
`N*`). Tudo stdlib; forja em amd64, runtime em i386.

### P1 — NLL por tarefa no eval (o medidor que destrava tudo)

**Mecanismo.** Registrar em `eoRow`/`eoGot` (`evaloffline.go:64-89`) o `p̂` que `Parse` atribuiu à intenção
escolhida (já tem `Conf` — falta a probabilidade crua por canal); adicionar `NLLBits` por tarefa e no
`eoSummary` (`evaloffline.go:91-107`). Saída em `results/`.

**Arquivos.** `evaloffline.go` (+~30 linhas), `nlu.go` (expor `p̂` por canal — já retornado por `predict`,
basta não descartar).

**Custo 32-bit.** Zero — logging.

**Métrica concreta.** `nll_bits_per_task` no relatório; habilita P2 e o trabalho de calibração (unifying P1/P2).

**Impacto 4 · Esforço S · Risco baixo** (aditivo).

### P2 — Portão MDL de duas partes no `forge_gate`

**Mecanismo.** `Metrics` (`forge_gate.go:46-51`) ganha `NLLBits float64`; `EvalFn` o preenche com
`Σ −log₂ p̂(outcomeᵢ)`. Critério novo, *somando* aos bloqueantes existentes (`false_success == 0`,
não-regressão `gateEps`, melhoria held-out — `forge_gate.go:152-171`): aceitar exige
`8·ΔL_dict + (Cand.NLLBits_out − Base.NLLBits_out) < 0`, com `ΔL_dict` de P3. `GateReport.Reason` imprime a
decomposição: `ΔL`, `ΔNLL_in`, `ΔNLL_out`, `N* = ΔL/Δb̄` (bits por tarefa, projetado).

**Arquivos.** `forge_gate.go` (+~60 linhas), adaptador em `evaloffline.go`.

**Custo 32-bit.** Nada em runtime; forja em amd64.

**Métrica concreta.** Assets aceitos/rejeitados com justificativa em bits; `N*` mediano; `false_success` = 0
preservado.

**Impacto 5 · Esforço M · Risco médio** — depende de `p̂` honesto (P1 + calibração); fallback defensivo:
clamp `p̂ ∈ [ε, 1−ε]` para o código ficar limitado, e marcar o NLL como "código aproximado" até calib.go
estar ligado.

### P3 — `mdl.go`: surrogate canônico `C(x | dict)`

**Mecanismo.** Novo arquivo `mdl.go` (~80 linhas): `mdlCanonical(asset)` — JSON com chaves ordenadas e
campos cegos ao comportamento (`doc`) removidos para a *medição*; `mdlDict()` — concatenação canônica da
biblioteca por família de intent (respeitando a janela de 32 KB do deflate); `mdlMarginal(a) =
8·(C_dict(a) − C_dict(∅))` bits. Usado por P2 (preço de `a`) e como distância base em P4.

**Arquivos.** `mdl.go` novo; leitura dos assets via `mhFS`/`nuFS` embeds (`methods.go:39`, `nlu.go`) —
read-only, sem tocar em load.

**Custo 32-bit.** Forja-only: deflate-9 sobre ≤ ~50 KB → dezenas a ~300 ms em amd64 (estimativa); no D610
seria ~segundos, mas nunca roda lá.

**Métrica concreta.** `ΔL` bits por candidato impresso no `GateReport`; reproduz a tabela §2.3
(60–261 B/método) como teste de sanidade.

**Impacto 4 · Esforço S–M · Risco baixo.**

### P4 — `ncd.go`: dedup por NCD como flag de revisão

**Mecanismo.** `ncdPair(x,y)` via variante dict (`max` das duas direções, §7.3); subcomando de lint na forja:
pares com `NCD < θ` → relatório para revisão. `θ` inicial ≈ 0,35 pela separação medida (0,13 mesma família /
0,55+ cruzada) — afinar numa amostra rotulada antes de confiar. Aplicar a: `methods/*.json` (canônico sem
`doc`), prompts de `evals/offline/tasks.json`, lições (`improve.go:251` — a chave exata continua como fast
path; NCD é a segunda passada), corpos de skill. **Regra:** flag, nunca delete; piso de objeto ≥ ~200 B.

**Arquivos.** `ncd.go` novo (~70 linhas) + gancho em `improve.go`/lint de forja.

**Custo 32-bit.** `O(k²)` compressões de ≤2 KB: k≈50 → ~1,2 s amd64 (estimativa); forja-only.

**Métrica concreta.** #pares flagrados, precisão em amostra revisada, #tarefas near-dup no corpus de eval.

**Impacto 3 · Esforço M · Risco médio** (ruído em objeto pequeno — mitigado pelo piso e pela revisão).

### P5 — Curva R-D operacional da biblioteca (`jazz eval offline --ablate`)

**Mecanismo.** Para cada grupo de assets (cada `methods/*.json`; blocos de exemplos por intent; léxico),
re-rodar o eval held-out **sem** o grupo: registrar `(bytes_liberados, tarefas_perdidas, ΔNLL)`. Envoltória =
mapa de alocação de bits (§6.2); assets com `Δganho = 0` = peso morto flagrado.

**Arquivos.** Flag em `evaloffline.go`; filtros de load em `methods.go:81-111` e `nlu.go` (carregar corpus
sem um subconjunto — o `skip` hook de `nuTrain`, `nlu.go:1363`, já existe e é o gancho natural para LOO).

**Custo 32-bit.** ~60 ablações × eval — minutos em amd64, forja-only.

**Métrica concreta.** Tabela `(bytes, Δsucesso, ΔNLL)` por asset em `results/`; #assets abaixo da fronteira.

**Impacto 4 · Esforço M · Risco baixo.**

### P6 — Quantização da tabela NB (o rate-distortion medível)

**Mecanismo.** Dois pontos da curva §6.4: (a) `int8` @1/128 nat (`nuScale 256→128`); (b) linhas esparsas
(`baldes ≠ zero` + `zero` compartilhado por classe). Medir: bytes de tabela, #argmax-flips nos 644 exemplos
+ `blind_test.json`, ΔECE (se P1 ligado), ns/`Parse` no D610. Critério de aceite: **0 flips no corpus e
ECE não piora**; margens < ruído de quantização (`n·passo/2`) passam a contar como empate.

**Arquivos.** `nlu.go` (:1330-1422 — layout da tabela) + bench.

**Custo 32-bit.** Mesma contagem de lookups; `int8` corta a tabela para ~39% da L2; esparso troca um branch
por balde ocupado — medir, não assumir.

**Métrica concreta.** `table_bytes`, `argmax_flips`, `ece_delta`, p50 `Parse` no D610.

**Impacto 3 · Esforço M · Risco médio** (regressão no laço interno — o benchmark decide).

### P7 — Kraft-check do namespace de atalhos (teste, custo zero)

**Mecanismo.** Teste que: (a) asserta `Σ D^{−lᵢ} ≤ 1` sobre o conjunto efetivo de atalhos (alfabeto ~36) por
menu/comando; (b) asserta unicidade de `Key`/`Keys` por `mnSpec` (colisão = violação de Kraft com `l=1`);
(c) reporta `Σ pᵢ·lᵢ` (comprimento esperado digitado) sob um arquivo de frequência de comandos, se/quando
telemetria existir. Hoje: 52 nomes → `l=1` universal já é *provadamente* impossível (1,44 > 1) — o teste
documenta a fronteira e impede regressões (`/q` duplicado, alias ambíguo).

**Arquivos.** `fuzzy_cmd_test.go`/`menu_test.go` novos asserts.

**Custo 32-bit.** Zero em runtime.

**Métrica concreta.** #violações (alvo 0); delta de comprimento esperado ao mudar aliases.

**Impacto 2–3 · Esforço S · Risco baixo.**

### P8 — Ordenação de pickers/offers por frequência medida

**Mecanismo.** `offers.go:158-175` monta em ordem fixa; pickers usam ordem de inserção. Com telemetria de
posição escolhida (contador por `Kind`/`Tag`), ordenar por `p` decrescente — o ótimo da busca linear
(Knuth §6.1; a cota entrópica de §4.3 diz quanto ainda há a ganhar). **Só** se a telemetria mostrar que a
seleção dominante é por leitura/scroll — se for atalho de tecla, ganho ≈ 0 (unifying §3.4, P8 de lá).

**Arquivos.** `offers.go`, `menu.go`, contadores em `obs.go`.

**Custo 32-bit.** Um `sort` por menu — nada.

**Métrica concreta.** posição média escolhida / toques por seleção (requer a telemetria primeiro).

**Impacto 2 · Esforço S · Risco baixo** — honestamente, depois de P1–P6.

---

## 10. O que NÃO fazer

- **Não implementar codificação aritmética.** O teorema `NLL = bits` é o que o MDL precisa; o deflate da
  stdlib já é LZ77+Huffman com redundância irrisória em KB. Codec aritmético em i386 = carry de 32 bits,
  bug sutil, ganho zero. (§5.2)
- **Não "medir K(x)".** Incomputável; `C(x)` é cota superior unilateral. Reportar `C_dict` como *preço de
  descrição*, nunca como "complexidade verdadeira". (§2.1)
- **Não usar MDL estrito sobre 75 tarefas como critério único.** A conta (§3.4) mostra que nada passaria;
  os bloqueantes de `forge_gate.go` continuam, `N*` é relatório, e `Δb̄` exige suposição de tráfego —
  dizer isso no `Reason`.
- **Não adicionar dependência de compressor** (zstd, brotli, PAQ, bzip2-write — na stdlib `compress/bzip2`
  só descomprime). `compress/flate` cobre o surrogate; PAQ é inviável em 2 GB.
- **Não usar NCD para deletar.** É flag de revisão: deflate mede substrings comuns, não semântica; abaixo de
  ~200 B vira ruído (medido §7.2); `NCD(x,x) ≈ 0,04` já mostra o compressor não-perfeitamente-normal.
- **Não usar embeddings para dedup** — viola a regra de ouro (modelo no caminho = dependência) e NCD cobre o
  caso a custo zero.
- **Não Huffman-izar o menu de 1 tecla.** Todos `l=1`: vazio (unifying §3.4). A cota entrópica só serve
  para pickers de filtragem — e lá é bound, não algoritmo.
- **Não otimizar `R(D)`.** Incomputável sem a fonte; a curva operacional (ablação/quantização) é a resposta
  medível. (§6.1-6.2)
- **Não quantizar `prior`/scores sem medir flips.** Margens < `n·passo/2` viram ruído → tratar como empate,
  não como diferença.
- **Não dedupar sobre JSON cru.** Formatação/envolvimento domina; canonicalizar primeiro (chaves ordenadas,
  `doc` fora quando a pergunta é comportamento). (§7.3, P3)
- **Não invocar "geometria da informação" (Amari) aqui.** O simplex do softmax tem métrica de Fisher, mas
  nenhum limiar, menu ou portão melhora por isso — VIAGEM; o único resíduo útil (o preço `k/2·log n`) já
  chegou por Rissanen, não por geometria.

---

## 11. Veredito final

**O que deu bom sobrevive ao aprofundamento — e fica mais barato de dizer.** "Inteligência = compressão/MDL"
desce a três contas concretas, todas medidas ou provadas acima:

1. **Preço de asset = `C_dict(a | biblioteca)` bits** — surrogate de Kolmogorov *condicional*, unilateral no
   sentido seguro, e medido: 60–261 B por método hoje.
2. **Preço dos dados = NLL held-out em bits** — pela codificação aritmética, sem implementar codec nenhum;
   o que mata dois pássaros: exige o `p̂` calibrado que já era a prioridade.
3. **Aceite = `ΔL + ΔNLL < 0`, com `N* = ΔL/Δb̄` reportado** — e `N*` é a *mesma forma* do break-even de
   tokens que o projeto já provou. O MDL não é uma filosofia nova em cima da forja: é a forja reescrita em
   unidades de bits, onde "simples o bastante pra generalizar" deixa de ser slogan e vira desigualdade.

Kraft/Huffman caem em dois lugares menores mas reais: **viabilidade de namespace** (52 nomes > 36 teclas —
por isso fuzzy + prefixo, não atalhos) e **cota de digitação nos pickers**. Rate-distortion cai na **curva
operacional** — `doc` é grátis cortar (14% dos bytes de método), a tabela NB é o experimento pendente.
NCD cai em **dedup com revisão** — separação medida de ~4× entre famílias, piso de ~200 B, zero embedding.

A frase para o README, honesta como a anterior: **"a biblioteca de assets é um código de duas partes — cada
asset paga seus bits de descrição economizando bits de erro em tarefas held-out."**

---

## Fontes

**Complexidade de Kolmogorov e surrogates**
- Kolmogorov, A.N. — *Three approaches to the quantitative definition of information*. Problems of Information Transmission 1(1):1–7, 1965.
- Solomonoff, R.J. — *A formal theory of inductive inference. Part I*. Information and Control 7(1):1–22, 1964. [doi:10.1016/S0019-9958(64)90223-2](https://doi.org/10.1016/S0019-9958(64)90223-2)
- Li, M. & Vitányi, P. — *An Introduction to Kolmogorov Complexity and Its Applications*, Springer 2008. [doi:10.1007/978-0-387-49820-1](https://doi.org/10.1007/978-0-387-49820-1)
- Delétang, G. et al. — *Language Modeling Is Compression*. ICLR 2024. [arXiv:2309.10668](https://arxiv.org/abs/2309.10668)
- Cleary, J. & Witten, I. — *Data compression using adaptive coding and partial string matching* (PPM). IEEE Trans. Commun. 32(4):396–402, 1984. [doi:10.1109/TCOM.1984.1096090](https://doi.org/10.1109/TCOM.1984.1096090)
- Willems, F.M.J., Shtarkov, Y.M. & Tjalkens, T.J. — *The context-tree weighting method: basic properties* (CTW). IEEE Trans. Inf. Theory 41(3):653–664, 1995. [doi:10.1109/18.382012](https://doi.org/10.1109/18.382012)

**MDL e seleção de modelo**
- Rissanen, J. — *Modeling by shortest data description*. Automatica 14(5):465–471, 1978. [doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5)
- Rissanen, J. — *Universal coding, information, prediction, and estimation*. IEEE Trans. Inf. Theory 30(4):629–636, 1984. [doi:10.1109/TIT.1984.1056936](https://doi.org/10.1109/TIT.1984.1056936)
- Rissanen, J. — *Fisher information and stochastic complexity*. IEEE Trans. Inf. Theory 42(1):40–47, 1996. [doi:10.1109/18.481776](https://doi.org/10.1109/18.481776)
- Grünwald, P. — *The Minimum Description Length Principle*. MIT Press 2007. [doi:10.7551/mitpress/4643.001.0001](https://doi.org/10.7551/mitpress/4643.001.0001)
- Hansen, M.H. & Yu, B. — *Model Selection and the Principle of Minimum Description Length*. JASA 96(454):746–774, 2001. [doi:10.1198/016214501753168398](https://doi.org/10.1198/016214501753168398)
- Barron, A.R. & Cover, T.M. — *Minimum complexity density estimation*. IEEE Trans. Inf. Theory 37(4):1034–1054, 1991. [doi:10.1109/18.86996](https://doi.org/10.1109/18.86996)
- Schwarz, G. — *Estimating the dimension of a model*. Ann. Statist. 6(2):461–464, 1978. [doi:10.1214/aos/1176344136](https://doi.org/10.1214/aos/1176344136)

**Códigos de prefixo, Kraft, Huffman, aritmética**
- Kraft, L.G. — *A device for quantizing, grouping and coding amplitude-modulated pulses*. Tese de mestrado, MIT, 1949.
- McMillan, B. — *Two inequalities implied by unique decipherability*. IRE Trans. Inf. Theory 2(4):115–116, 1956. [doi:10.1109/TIT.1956.1056818](https://doi.org/10.1109/TIT.1956.1056818)
- Huffman, D.A. — *A Method for the Construction of Minimum-Redundancy Codes*. Proc. IRE 40(9):1098–1101, 1952. [doi:10.1109/JRPROC.1952.273898](https://doi.org/10.1109/JRPROC.1952.273898)
- Shannon, C.E. — *A Mathematical Theory of Communication*. Bell Syst. Tech. J. 27, 1948. [doi:10.1002/j.1538-7305.1948.tb01338.x](https://doi.org/10.1002/j.1538-7305.1948.tb01338.x)
- Cover, T. & Thomas, J. — *Elements of Information Theory*, 2ª ed., Wiley 2006, caps. 5 e 13. [PDF cap. 5](https://ftp.esat.kuleuven.be/pub/pub/SISTA/decock/voor_xander/referenties/Cover&Thomas/5.pdf)
- Rissanen, J. — *Generalized Kraft inequality and arithmetic coding*. IBM J. Res. Dev. 20(3):198–203, 1976. [doi:10.1147/rd.203.0198](https://doi.org/10.1147/rd.203.0198)
- Witten, I.H., Neal, R.M. & Cleary, J.G. — *Arithmetic coding for data compression*. CACM 30(6):520–540, 1987. [doi:10.1145/214762.214771](https://doi.org/10.1145/214762.214771)
- Knuth, D. — *The Art of Computer Programming*, Vol. 3, §6.1 (ordenação estática ótima da busca linear).

**Rate-distortion e alocação de bits**
- Shannon, C.E. — *Coding theorems for a discrete source with a fidelity criterion*. IRE Nat. Conv. Rec. 7, 1959. [doi:10.1109/9780470544242.ch21](https://doi.org/10.1109/9780470544242.ch21)
- Blahut, R.E. — *Computation of channel capacity and rate-distortion functions*. IEEE Trans. Inf. Theory 18(4):460–473, 1972. [doi:10.1109/TIT.1972.1054855](https://doi.org/10.1109/TIT.1972.1054855)
- Shoham, Y. & Gersho, A. — *Efficient bit allocation for an arbitrary set of quantizers*. IEEE Trans. ASSP 36(9):1445–1453, 1988. [doi:10.1109/29.90373](https://doi.org/10.1109/29.90373)
- Ortega, A. & Ramchandran, K. — *Rate-distortion methods for image and video compression: an overview*. IEEE Signal Processing Mag. 15(6):23–50, 1998. [doi:10.1109/79.733495](https://doi.org/10.1109/79.733495)
- Tishby, N., Pereira, F.C. & Bialek, W. — *The information bottleneck method*. Allerton 1999 / [arXiv:physics/0004057](https://arxiv.org/abs/physics/0004057)

**Distância de informação e NCD**
- Bennett, C.H., Gács, P., Li, M., Vitányi, P.M.B. & Zurek, W.H. — *Information distance*. IEEE Trans. Inf. Theory 44(4):1407–1423, 1998. [doi:10.1109/18.681318](https://doi.org/10.1109/18.681318)
- Li, M., Chen, X., Li, X., Ma, B. & Vitányi, P.M.B. — *The similarity metric*. IEEE Trans. Inf. Theory 50(12):3250–3264, 2004. [doi:10.1109/TIT.2004.838101](https://doi.org/10.1109/TIT.2004.838101); [arXiv:cs/0111054](https://arxiv.org/pdf/cs/0111054)
- Cilibrasi, R. & Vitányi, P.M.B. — *Clustering by compression*. IEEE Trans. Inf. Theory 51(4):1523–1545, 2005. [doi:10.1109/TIT.2005.844059](https://doi.org/10.1109/TIT.2005.844059); [arXiv:cs/0312044](https://arxiv.org/abs/cs/0312044)
- Cebrián, M., Alfonseca, M. & Ortega, A. — *Common pitfalls using the normalized compression distance: what to watch out for in a compressor*. Communications in Information & Systems 5(4):367–384, 2005. [doi:10.4310/CIS.2005.v5.n4.a1](https://doi.org/10.4310/CIS.2005.v5.n4.a1)
