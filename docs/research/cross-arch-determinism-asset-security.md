# Determinismo cross-arch e segurança do asset: o `nb.bin` que ainda não existe

**Pesquisa de 04/11/2026.** Alvo: d610 (i686, 1 CPU, 2 GB, `linux/386`), forja em amd64. Continua
[unifying-math-offline.md](unifying-math-offline.md) (calibração, MDL, o que é teorema), [math-offline-brain.md](math-offline-brain.md)
(números, ECE, PAV), [offline-on-device-intelligence.md](offline-on-device-intelligence.md) (asset-forge) e
[harness-sota-gap.md](harness-sota-gap.md) (portão held-out). Não repete o pipeline NLU nem o layout dos índices.

Convenções: `file:line` refere-se ao `main` @ `584494d` deste workspace. **Medido** = número produzido nesta sessão, com o
comando ao lado. **Conta** = estimativa minha, não medida. "D610" = Pentium M Dothan 1,7 GHz, 1 núcleo, 2 MB de L2,
`linux/386`, Go 1.27.2. Identificadores de código em inglês; prosa em pt-BR.

---

## 0. Tese e placar

> **Tese.** Determinismo cross-arch é *pré-condição* de segurança do asset, e nenhum dos dois se resolve com um
> portão estatístico. (a) O caminho de *decisão* do jazz já é inteiro — argmax, resolução de nome, margem — e é
> determinístico; mas a *confiança* e a *calibração* tocam `math.Exp`/`math.Log`, cujas implementações diferem entre
> amd64 (asm SLEEF, com FMA opcional em runtime) e 386 (Go puro). Logo "mesmo asset, mesma entrada, mesma predição"
> é **falso bit-a-bit hoje**, e pode ser tornado verdadeiro com um contrato numérico inteiro e um teste golden
> cross-arch. (b) Assinatura ed25519 com chave separada garante *procedência*, não *comportamento*: o portão held-out
> mede risco médio numa amostra finita, e um asset correto-malicioso — backdoor de gatilho, mapa de calibração que só
> sobe a confiança num slice, método que chama um script do projeto — passa. A defesa é em camadas. (c) O buraco mais
> caro hoje não é o `nb.bin` (que não existe) nem o portão (que não está ligado): é que `calib.go` está morto e
> `forge_gate.go` é uma biblioteca sem pipeline. Antes de assinar assets, é preciso ligar o caminho e definir o
> contrato numérico.

| # | Pergunta | Resposta curta | Onde |
|---|---|---|---|
| 1 | "Os floats batem entre amd64 e i386?" | **Não bit-a-bit** para confiança/calibração; **sim** para argmax, nome e margem, que são inteiros. | §2.3–2.6 |
| 2 | "O i386 usa x87 de 80 bits?" | **Não** para `+ - * / sqrt`: Go 1.16 removeu `GO386=387`; o 386 emite SSE2 (`ADDSD`/`MULSD`). Usa x87 só em `Floor`/`Ceil`/`Trunc`. | §2.1 |
| 3 | "Então de onde vem a divergência?" | De `math.Exp`/`math.Log` (asm no amd64, Go puro no 386) e de **FMA** (o compilador funde `x*y+z` com `GOAMD64=v3`; `math.Exp` do amd64 escolhe kernel FMA em runtime). | §2.3–2.4 |
| 4 | "Os hashes batem?" | **Sim**, se forem `uint32`/`uint64` explícitos, little-endian explícito, sem iteração de mapa no resultado. FNV-1a do NB e `splitHash` são estáveis. | §2.8 |
| 5 | "Dá para garantir predição idêntica?" | **Sim**, com decisão em inteiros, confiança quantizada, calibração em nós inteiros, formato fixo LE e um teste golden rodado nos dois arcos. | §2.7, P1–P3, P8 |
| 6 | "Build reprodutível resolve?" | **Não.** Build byte-idêntico é necessário para supply chain, mas o mesmo binário pode divergir em runtime (`math.Exp` escolhe FMA pela CPU). | §2.5 |
| 7 | "ed25519 com chave separada resolve o asset?" | Garante **procedência e integridade**, não comportamento. Chave separada é obrigatória; sozinha, não basta. | §3.5 |
| 8 | "O portão held-out pega asset correto-malicioso?" | **Não.** Ele limita risco médio numa amostra; um gatilho fora da amostra passa. Pior: o portão é *white-box* para a forja (corpus e seed no repo). | §3.3–3.4 |
| 9 | "O que o portão não pega além disso?" | Custo (p95/RSS), capacidades (o eval é mais restritivo que a produção), verificador fraco/autocertificado, coverage gaming, contaminação do held-out pelo pré-treino, múltiplas comparações. | §3.3 |
| 10 | "Qual o risco de confusão de trust por pasta?" | Alto: um repo clonado pode colocar `.jazz/brain/methods/*.json` que chamam `file_op delete`, `git_op push`, `pkg_cmd`, `script:`; o prompt de "confiar na pasta" não enumera isso. | §3.6 |
| 11 | "Qual o primeiro passo?" | Ligar o caminho (P1/P2/P3) e o teste golden (P8); depois assinatura (P4/P5) e endurecimento do portão (P6/P7). | §5 |
| 12 | "O que não fazer?" | Não chamar o portão de prova de segurança; não usar a chave de release para assets; não deixar float no caminho de decisão; não tratar asset JSON como "dado". | §6 |

---

## 1. O que existe hoje (e o que não existe)

O desenho de "forja amd64 → `nb.bin` → d610" **ainda não está no código**. O que existe:

- **NB treinado em runtime, não carregado de arquivo.** `nuGet()` compila o dataset embutido (`nlu.go:151-158`); o
  primeiro `classify` dispara `m.nbOnce.Do(func() { m.nb = m.nuTrain(nil) })` (`nlu.go:1429-1431`). A tabela é
  `[]int16` de `classes × nuBuckets` (`nlu.go:1395`) e o prior `[]int32` (`nlu.go:1396`). Ou seja: hoje o "asset" é
  o JSON embutido, e o treino roda **no d610**, com `math.Log` do 386. Não há `nb.bin`, loader, versão nem hash.
- **Calibração morta.** `ECE`, `BrierScore`, `PlattFit`, `IsotonicFit`, `ReliabilityBins` só são chamados por
  `calib_test.go`; nenhum caminho de produção usa (`calib.go:164,198,220,281`). A doc
  [unifying-math-offline.md](unifying-math-offline.md) §2 já dizia isso; o grep confirma.
- **Portão held-out é uma biblioteca pura, sem chamador de produção.** `Gate` e `Split` só aparecem em
  `forge_gate.go` e `forge_gate_test.go` (`forge_gate.go:92,143`). Não há `jazz forge`, não há promoção, não há
  assinatura.
- **Ledger de sinais é escrito e lido, mas ninguém consome.** `sigAppend`/`sigRead` (`signals.go:52,84`) existem;
  não há chamador de produção no repo. O ledger é a matéria-prima da forja, mas a forja não está ligada.
- **Trust de projeto já existe e é o precedente certo.** `IsTrusted(root, kind, content)` guarda `sha256` do conteúdo
  por raiz absoluta (`trust.go:91-102`); `scripts.go:141` só carrega `.jazz/scripts` se `kind="scripts"` estiver
  confiado; `scProjectBlob` (`scripts.go:1048-1069`) é o modelo de "hash do que roda". O asset deve seguir o mesmo
  padrão, com um manifesto fechado (P5).
- **Update de release já tem ed25519 com chave embutida e verificação antes de usar.** `updatePubKey`
  (`update.go:53`), `pfVerifySums` (`update.go:89-102`), `pfVerifyManifest` (`update.go:112-120`). É o padrão a
  copiar — **com outra chave** (§3.5).

Consequência: este documento analisa o **mecanismo que a forja vai precisar**, não um arquivo que já está no disco.
Onde eu digo "o `nb.bin`", leia "o artefato quantizado que substituirá `nuTrain` no runtime".

---

## 2. Determinismo cross-arch

### 2.1 A premissa do x87 está errada — mas tem um núcleo de verdade

A pergunta natural é "o i386 usa x87 de 80 bits e o amd64 usa SSE, então os floats divergem?". Para Go, a resposta é
**não**:

- Go 1.15 foi a última release com `GO386=387`; a Go 1.16 **removeu** o modo x87 e passou a exigir SSE2 (ou
  `GO386=softfloat` para CPUs sem SSE2) [S6][S7][S8]. O Pentium M Dothan tem SSE2, então o d610 compila no modo
  `sse2` default.
- O backend 386 do compilador emite `ADDSD`/`SUBSD`/`MULSD`/`DIVSD`/`SQRTSD` para `float64`, e os registradores
  `X0..X7` são SSE2 [S11]. **Medido:** no binário `linux/386` deste repo, `math.exp` (330 bytes, em `0x813d800`)
  contém `addsd`/`mulsd`/`subsd` e **nenhuma** instrução x87; há 471 instruções SSE2 de double contra 10 x87 no
  binário inteiro, e as 10 estão em `runtime.asminit` (`fldcw`) e `math.archCeil` (`fldl`/`frndint`).
- **Núcleo de verdade:** `Floor`, `Ceil` e `Trunc` **usam x87** no 386 (`math/floor_386.s`: `FLDCW` + `FRNDINT`),
  enquanto o amd64 usa `floor_amd64.s` (SSE2). Para doubles já representáveis e resultados inteiros, o resultado é
  exato e idêntico; mas é uma diferença real de implementação, e `math.Round` é puro bit-twiddling
  (`math/floor.go:90-121`), então `Split`/`gate` não são afetados. Não use `Floor`/`Ceil` em caminho de decisão sem
  um teste golden.

Ou seja: **o problema não é precisão estendida do x87; é algoritmo de biblioteca e FMA.** Isso muda a defesa: não
adianta `-ffloat-store` nem "forçar double"; é preciso tirar a dependência de `math.Exp`/`math.Log` do caminho de
decisão.

### 2.2 O que é bit-idêntico por IEEE-754 — e o que o Go permite

Sob IEEE-754 [S1][S2][S3], com os **mesmos operandos, a mesma ordem de avaliação e o mesmo modo de arredondamento**,
`+ - * / sqrt` são corretamente arredondados e dão o mesmo bit pattern em qualquer arquitetura. O que quebra essa
garantia:

1. **FMA (fused multiply-add).** A especificação de Go permite que a implementação combine `x*y + z` numa instrução
   só, sem arredondar o produto intermediário [S9][S10]. **Medido:** com `GOAMD64=v1`, `func f(x,y,z float64) float64
   { return x*y + z }` compila para `mulsd` + `addsd`; com `GOAMD64=v3`, compila para `vfmadd231sd`. O contraexemplo
   clássico: `x = 1+2^-27`, `y = 1-2^-27`, `z = -1`. Separado: `x*y` arredonda para `1`, `1-1 = 0`
   (`0x0000000000000000`). FMA: `-2^-54 = -5.5511151231257827e-17` (`0xbc90000000000000`). **Mesma fonte, mesma
   entrada, resultado diferente.**
2. **`math.Exp`/`math.Log` específicos de arco.** `math.Exp` tem `haveArchExp=true` em amd64/arm64/loong64/riscv64/
   s390x e `false` no 386 (`math/exp_asm.go`, `math/exp_noasm.go`) [S12]. No amd64, `exp_amd64.s` é uma implementação
   SLEEF/Shibata [S13] e escolhe o kernel FMA em runtime (`CMPB ·useFMA(SB), $1; JE avxfma`) — ou seja, **duas
   máquinas amd64 podem divergir** conforme a CPU tenha FMA. No 386, `math.Exp` chama o `exp()` puro do FreeBSD
   (`math/exp.go:96-135`), que é outro algoritmo. `math.Log` é asm no amd64/s390x e puro no 386
   (`math/log_asm.go`, `math/log_stub.go`).
   **Medido:** no binário 386, os símbolos são `math.exp` e `math.log`; no amd64, `math.archExp.abi0` e
   `math.archLog.abi0`. São funções diferentes no binário final, não uma escolha de compilador que "some".
3. **Ordem de soma não associativa.** `a+b+c` depende da ordem; somar em ordem diferente muda o resultado. Se a ordem
   vier de iteração de mapa, muda **entre execuções na mesma máquina** (o Go não especifica ordem de mapa [S45]).
4. **Ordem de `sort` com empate.** `sort.Slice` não é estável; se a chave é float e dois valores são iguais (ou
   diferem por 1 ulp), a ordem pode mudar e mudar o ajuste.
5. **Conversão float→int e saturação.** `int16(v)` trunca em direção a zero; se `v` está a 1 ulp de um inteiro, o
   valor quantizado pode diferir. Isso é discreto: um `int16` diferente **pode** virar um argmax diferente, não só
   um último dígito.

### 2.3 Prova 1: `math.Exp` do amd64 e do 386 divergem em 14% dos pontos

Copiei literalmente `exp()`/`expmulti()` de `math/exp.go` (a função que o 386 compila) para um programa amd64 e
comparei com `math.Exp` (a asm que o amd64 compila) numa grade de `[-2, 0]` com passo `1e-6`. **Medido:**

```
diffs = 280.261 em 2.000.001 pontos (14,0%)
x = -1.999978
  amd64 asm : 0x3fc152c39daf67d0  0.13533826064559529
  386 puro  : 0x3fc152c39daf67cf  0.13533826064559526   (1 ulp)
x = -1.999963
  amd64 asm : 0x3fc152d4a544b5da
  386 puro  : 0x3fc152d4a544b5db   (-1 ulp)
```

O intervalo `[-2,0]` é exatamente o domínio do expoente do softmax: `(s_c - s_best)/nuScale * nuBeta / n`
(`nlu.go:1474`), com `nuScale=256`, `nuBeta=6`, `n` = número de n-gramas. Ou seja, **a confiança do `predict` não é
bit-idêntica entre os dois arcos**. O `math.Log` não divergiu nos 3.000.000 de pontos que testei em `(0,30]`
(passo `1e-5`), mas isso é uma observação, não uma garantia: são implementações diferentes.

**O que isso NÃO prova:** que o *argmax* muda. O argmax é `int32` (`scores[ci] > scores[best]`, `nlu.go:1462-1466`),
sem float; logo é idêntico. O que muda é o número `Confidence` e, em casos de fronteira, a decisão `>= nuMinConf`
(`nlu.go:45,62-64`) ou `>= brLevel0Min` (`brain.go:596`). A probabilidade de um ponto de grade inteiro cair a 1 ulp
de `0.6`/`0.85` é ínfima; mas "ínfima" não é "zero", e um asset malicioso pode escolher os scores para mirar a
fronteira. O contrato inteiro (§2.7) elimina a categoria inteira.

### 2.4 Prova 2: FMA muda o resultado — inclusive entre dois amd64

O contraexemplo acima (`0` vs `-2^-54`) é a prova mais curta de que "mesmo binário, mesma entrada" não é suficiente
se o conjunto de instruções difere. No jazz, as expressões sensíveis a FMA são:

- `calib.go:239-244` — `gA += d * xs[i]`, `hAA += w * xs[i] * xs[i]`, etc. (gradiente/Hessiano do Platt).
- `calib.go:253-254` — `(hBB*gA - hAB*gB) / det` (passo de Newton).
- `calib.go:266` — `sigmoid(a*logit(p) + b)` (aplicação do Platt; `a*x+b` é um multiply-add).
- `calib.go:175` — `ece += (float64(b.N)/float64(n)) * math.Abs(...)` (medição).
- `calib.go:208-210` — `sum += d*d` (Brier; medição).

Se a calibração for **ajustada** na forja com `GOAMD64=v3` e consumida no d610, os parâmetros podem diferir. Se for
ajustada e **quantizada em inteiros** na forja, o runtime não tem FMA; o problema vira só de procedência. A regra:
**ajustar uma vez, quantizar, assinar; nunca reajustar no alvo.**

### 2.5 Prova 3: o build é reprodutível — e isso não basta

**Medido:** dois builds `GOOS=linux GOARCH=386 CGO_ENABLED=0 go build -trimpath -buildvcs=false`, um deles com
`GOCACHE` limpo e `-a`, produziram o **mesmo sha256**:

```
dd2199ae802b8021e0819acf79aedae6a45431f543f7b6bef6aa36dd03691ef8  jazz386-a
dd2199ae802b8021e0819acf79aedae6a45431f543f7b6bef6aa36dd03691ef8  jazz386-c
build id: VCQ6C_DJdqBSuAIGqwgg/Dqa8AyCsofMLaQgW776E/qTcLgzVBrLZoMD-aYGCu/12auhd7TWE14Tf-1xZYa
tamanho: 20.107.327 bytes
```

Isso é reprodutibilidade **de build**: mesmo fonte, mesma toolchain (`go1.27.2`), mesmas flags, sem VCS stamping.
(`GOAMD64` não se aplica a `GOARCH=386`; o build é 386 puro.) `-trimpath` remove caminhos absolutos;
`-buildvcs=false` remove o carimbo de VCS [S14]. O `go.mod` diz `go 1.27.2` (`go.mod:3`), que implica
`toolchain go1.27.2`, mas `GOTOOLCHAIN=auto` pode
usar uma toolchain mais nova se o `go` local for mais novo [S15]; para reprodutibilidade, fixe
`GOTOOLCHAIN=go1.27.2` (ou `local` com exatamente essa toolchain). A literatura de reproducible builds é clara:
o objetivo é permitir que terceiros reconstruam o binário e comparem [S16][S17].

**O que isso não resolve:** um binário byte-idêntico pode se comportar diferente na mesma entrada, porque
`math.Exp` do amd64 escolhe o kernel FMA **em runtime** pela CPU (`useFMA`), e porque `GOAMD64=v3` no build muda a
geração de código. Reprodutibilidade de build é sobre *o arquivo*; determinismo de execução é sobre *a execução*.
São propriedades diferentes, e a segunda é a que importa para o asset.

### 2.6 Onde o jazz toca float no caminho de decisão

| Local | Float? | Determinístico entre amd64/386? | Por quê |
|---|---|---|---|
| `nlu.go:1438-1466` (argmax) | não (int32) | **sim** | soma inteira, comparação estrita, ordem fixa de `nb.classes` |
| `nlu.go:1470-1477` (confiança) | sim (`math.Exp`) | **não** | `math.Exp` asm vs puro (§2.3) |
| `nlu.go:1404,1416,1422` (`nuTrain`) | sim (`math.Log`) | **não garantido** | `math.Log` asm vs puro; quantiza em `int16` |
| `calib.go:220-262` (`PlattFit`) | sim (`math.Log/Exp`, FMA) | **não** | Newton com multiply-add; FMA (§2.4) |
| `calib.go:265-267` (`PlattApply`) | sim (`math.Log/Exp`, FMA) | **não** | `a*logit+b` + `math.Exp` |
| `calib.go:281-321` (`IsotonicFit`) | sim (sort/somas) | **não se ajustado no alvo** | ordem por `P` float; somas não associativas |
| `calib.go:326-353` (`IsotonicApply`) | sim (`+ - * /`) | **sim**, se os nós forem os mesmos bits | sem `Exp`; IEEE correto; cuidado com FMA |
| `calib.go:164-212` (ECE/Brier) | sim | **não** (último ulp) | medição, não decisão |
| `forge_gate.go:112` (`math.Round`) | sim | **sim** | `Round` é bit-twiddling exato |
| `forge_gate.go:46-51` (métricas) | `float64` de razões de int | **sim** | divisão de ints, mesmos operandos |
| `nameindex.go`, `fuzzy.go`, `methods.go:1132-1155` | não (int) | **sim** | `niBound`, `fzDist`, margem `fzSlip*8` |
| `nlu.go:186-188` (ordem de slang) | — | **não entre execuções** | itera mapa e ordena só por comprimento; empate fica na ordem do mapa [S45] |

Duas leituras importantes:

1. **O argmax já é determinístico.** A parte que decide *qual* intenção é inteira. A parte que decide *se age* é
   float. É por isso que o eval 39/39 funciona e a calibração ainda é o buraco: a decisão de agir é a que precisa de
   `p` significando algo, e é a que não é reproduzível.
2. **A ordem de `m.slang` é um não-determinismo de runtime, não de arco.** `nuCompile` faz
   `for k, v := range lx.Slang` (`nlu.go:179-185`) e depois `sort.Slice` só por comprimento (`nlu.go:186-188`). Se
   dois phrases tiverem o mesmo primeiro token e o mesmo comprimento, a ordem relativa é a da iteração do mapa.
   Hoje isso é benigno porque `matchPhrase` só aceita o phrase inteiro (`nlu.go:397-409`), e dois phrases distintos
   do mesmo comprimento não podem casar no mesmo ponto; mas é um invariante frágil. O conserto é barato: ordenar por
   `(len desc, lexicográfico)` e nunca depender da ordem do mapa. O mesmo vale para qualquer futuro asset de léxico.

### 2.7 O contrato numérico que torna a predição idêntica

O objetivo não é "usar float com cuidado"; é **não usar float no caminho de decisão**. Proposta de contrato:

**C1 — Decisão em inteiros.** `Intent.Confidence` continua `float64` para exibição, mas ganha
`ConfQ int32` (permile, 0..1000) e `Understood()` passa a comparar `ConfQ >= 600`. `brLevel0Min` vira `850`.
`brNotUnderstood` imprime `float64(ConfQ)/1000` (divisão por 1000 é corretamente arredondada e idêntica nos dois
arcos).

**C2 — Softmax inteiro.** Em vez de `math.Exp` por classe, uma tabela `expQ` pré-computada:
`expQ[k] = round(exp(-k/1024) * 65536)` para `k = 0..16384` (64 KB `int32`, ou 32 KB `int16`), e
`expQ[k]=0` para `k>16384`. Para cada classe, `k = round((scores[best]-scores[ci]) * 6 * 1024 / (256 * n))`;
`somaQ += expQ[k]` em `int64`. Como `expQ[0]=65536` representa `1.0`, a confiança em permile é
`ConfQ = round(1000 * 65536 / somaQ)` (ou seja, `65.536.000 / somaQ`); não use `1e6/somaQ` — a escala estaria
errada por 65,536×. A tabela é um **asset assinado**, gerada uma vez na forja com uma regra de arredondamento
documentada (half-even) e nunca recalculada no d610. Custo: 50 lookups + somas inteiras por pedido, mais barato que
50 `math.Exp` soft-float no Pentium M; 64 KB cabem na L2. Erro máximo: um `expQ` com 16 bits de fração tem erro
relativo ≤ 2^-16; a confiança em permile tem erro ≤ 1 permile, suficiente para limiares de 0,6/0,85. Prove o bound
no teste (`TestExpQError`), incluindo o caso de `somaQ` pequeno (uma classe só) e o clamp em 0..1000.

**C3 — Calibração em nós inteiros.** `calib.bin` guarda `[]struct{ P, Y uint16 }` em Q12 (0..4095), monotônico,
com `P` estritamente crescente. `ApplyQ(pQ12, nodes)` faz interpolação linear inteira:
`y = y0 + ((p-p0)*(y1-y0) + (p1-p0)/2) / (p1-p0)`, com `int32` (4095*4095 = 16,7M < 2^31). Sem `Exp`, sem `Log`,
sem FMA. Um "orçamento de deslocamento" `max|Y-P| <= δ` (ex.: 0,05) limita quantas decisões o mapa pode virar
(§3.4). Ajuste na forja com `PlattFit`/`IsotonicFit` (float, tudo bem — é offline), quantize, assine; o d610 só
interpola.

**C4 — Formato fixo, LE, sem float.** `nb.bin`/`calib.bin` com magic, versão, `uint16/uint32` explícitos,
`binary.LittleEndian`, `sha256` do payload, sem `encoding/gob`, sem `unsafe`, sem endian nativo, sem `float64`
serializado em decimal. Se um dia um float for inevitável, serialize os **bits** (`math.Float64bits`) e nunca faça
aritmética com ele no runtime.

**C5 — Ordem determinística.** Toda soma em ordem fixa (slice, não mapa); todo `sort` com tie-breaker explícito
(ID ou string); nenhuma iteração de mapa que afete saída; `sort.SliceStable` só com chave total.

**C6 — Teste golden cross-arch.** Um corpus fixo de entradas (as 75 tarefas + `nlu/blind_test.json` + casos
sintéticos) com o resultado esperado em **bits**: `Intent.Name`, `ConfQ`, `TypoOnly`, `Missing`, `Slots` ordenados,
`Evidence` ordenada, `Status`. O teste roda em amd64 e em 386 (qemu-i386 no CI ou no d610) e falha em qualquer
diferença. É a única prova real; o resto é argumento.

### 2.8 Perigos de 32 bits (e onde o código já está certo)

- **`int` é 32-bit no 386 e 64-bit no amd64.** Qualquer valor que caiba em `int` no amd64 e estoure no 386 muda de
  comportamento. O código do NB usa `uint32` explícito no FNV (`nlu.go:1351-1357`), `int32` nos scores
  (`nlu.go:1438`), `int16` na tabela (`nlu.go:1395`) e `int32` no prior (`nlu.go:1396`). A soma máxima é
  `|prior| + n*32768`; com `n` ~ centenas, fica abaixo de 2^31. **Está certo**, mas o formato novo deve declarar
  larguras, não confiar em `int`.
- **Hash FNV-1a é estável.** `nuNgrams` usa `uint32(2166136261)`, `h ^= uint32(b[k]); h *= 16777619` e
  `h & (nuBuckets-1)` (`nlu.go:1344-1361`). Tudo `uint32`, sem `int`, sem dependência de arco. `splitHash` usa
  `hash/fnv.New64a` + `binary.LittleEndian` (`forge_gate.go:72-78`); `hash/fnv` é Go puro, determinístico.
- **Cuidado com `int` em produtos de hash.** Se um asset futuro usar Rabin-Karp com `2^31-1`, o produto de dois
  números de 31 bits não cabe em `int` no 386; use `uint64` antes do `%` (a doc
  [unifying-math-offline.md](unifying-math-offline.md) §3.9 já avisa). Não introduza rolling hash sem largura
  explícita.
- **`int16` satura em `-32000`** (`nlu.go:1405-1420`): likelihoods muito baixas achatam. Isso é uma escolha de
  quantização, não um bug de arco; mas o formato novo deve documentar a saturação e o erro máximo.
- **`math.Round` é seguro** (`math/floor.go:90-121`), mas `math.Floor`/`Ceil` usam x87 no 386 (§2.1). Não use
  `Floor`/`Ceil` para índices de bin; use `int`/`Round` com clamp.
- **`time.Now`/`rand` não entram na predição.** O `splitHash` usa seed + ID, não relógio; o `Gate` usa
  `math.Round` e comparações. Bom. O formato de asset não deve incluir timestamp não assinado que afete a predição.

---

## 3. Segurança do asset

### 3.1 "Asset JSON" é código

O ponto que desarma a intuição "JSON é dado, não executa": um `mhMethod` é um programa numa linguagem pequena cujas
instruções são **capabilities**. `Steps[].Cap` resolve para `brCaps` (`methods.go:303-329`), que inclui:

- `file_op` (`methods.go:512-604`): create/write/append/mkdir/delete/rename.
- `git_op` (`methods.go:605-700`): commit/checkout/pull/push/stash.
- `run_tests` (`methods.go:1369+`): roda o comando de teste do projeto — código do repo.
- `proc_kill` (`methods.go:701-748`), `pkg_cmd` (`methods.go:749-801`), `container_op` (`methods.go:802-869`).
- `script:<name>` (`methods.go:339-341`): roda um script Python/sh registrado (`scripts.go:49-63`), com efeito
  declarado `read|write|exec|git|net` (`scripts.go:45,54`).

`mhBadCmd` (`methods.go:115-122`) só valida `Cmds` literais contra `cbReadCmds` (`caps.go:573-595`); **não** olha
`Steps[].Cap`. Um método pode chamar `git_op push` ou `file_op delete` sem declarar nenhum `Cmds`. O eval offline
usa um `Permit` mais restritivo (`evaloffline.go:317-333`: leitura, allowlist, teste/build/lint/fmt, file ops), mas
a produção usa `ag.PermitCommand`, que em `--yolo`/trusted pode liberar mais. **O portão avalia um asset sob uma
política mais restritiva que a produção** — logo um asset que passa no eval pode fazer mais no d610. Isso é um
achado, não um detalhe.

Consequência: o asset precisa de um **manifesto de capacidades** e de uma política de capacidade, não só de um
`mhBadCmd`. É a P7.

### 3.2 Modelo de ameaça

| Ator | O que pode fazer | Ativo | Defesa |
|---|---|---|---|
| LLM da forja | propor qualquer JSON/asset; ler o repo e o eval | exemplos, léxico, métodos, calibração | tratar como não confiável; lint + portão + revisão; nunca `--promote` automático |
| Máquina da forja comprometida | assinar qualquer coisa com a chave de asset | todos os assets | chave offline/HSM, transparência, rotação; limite: compromisso da forja é grave |
| Espelho/transporte | servir bytes velhos ou trocados | `nb.bin`, `calib.bin` | assinatura, expiração, high-water de versão (TUF) [S21] |
| Repo clonado | `.jazz/brain/methods/*.json`, `.jazz/scripts`, testes, README | métodos, scripts, exemplos | trust explícito por manifesto, política de caps, sandbox, sem shadowing |
| Mesmo usuário / malware local | editar `dataDir()`, `trusted.json`, `signals.jsonl` | tudo | **fora de escopo** (mesmo uid); diga isso honestamente |
| Rede | MITM em http | update/assets | https ou tailnet, como `pfUpdateURLOK` (`update.go:66-85`) |
| Pré-treino do professor | eval pode ter sido memorizado | held-out | blind set secreto, holdout por tempo, dedupe [S29] |
| Insider/dono | assina | tudo | log de auditoria, transparência |

O limite honesto: **se o atacante tem o mesmo uid, acabou.** O que dá para proteger é o limite "conteúdo de projeto
vs. configuração do usuário" — que é exatamente o limite que `trust.go` já tenta impor.

### 3.3 O que o portão held-out pega — e o que não pega

O `Gate` (`forge_gate.go:143-172`) aceita se: split não degenerado, `FalseSuccess == 0` nos dois lados, accuracy não
regride além de `gateEps=0.01`, e **uma** das métricas do held-out melhora estritamente. O que ele **pega**:

- overfit grosseiro (accuracy held-out cai);
- regressão no held-in;
- falso sucesso **tal como o `EvalFn` o define** (bloqueante);
- melhora nenhuma (rejeita empate).

O que ele **não pega**:

1. **Backdoor de gatilho.** O asset é igual ao baseline em todos os exemplos do eval e diferente num gatilho fora
   dele. `L_S(f) = L_S(f0)`; com uma melhora benigna qualquer em `S`, o portão aceita. É a classe BadNets/Poison
   Frogs: desempenho de validação intacto, comportamento alterado no gatilho [S24][S25]. Em modelos de instrução,
   ~100 exemplos envenenados bastam para um gatilho [S26]; e o comportamento pode persistir a treino de segurança
   [S27].
2. **Gatilho white-box.** O corpus e o seed estão no repo; a forja (LLM) pode ler ambos. O `Split` é determinístico
   por `splitHash(seed, ID)` (`forge_gate.go:72-126`), então o atacante sabe exatamente qual exemplo é held-out. O
   "held-out" não é secreto. Pior: o `Gate` recebe `seed` e `heldOutFrac` do chamador (`forge_gate.go:143`); um
   chamador malicioso escolhe uma seed que põe os casos difíceis no held-in. O portão não deriva a seed do corpus.
3. **Held-out minúsculo.** `Split` só rejeita held-in/held-out vazio (`forge_gate.go:153`). Com `frac=0.01` e 100
   exemplos, o held-out tem 1 exemplo; o portão "passa" sem poder estatístico. Não há mínimo por grupo nem teste de
   poder.
4. **Verificador fraco ou autocertificado.** `brVerify` (`methods.go:1680-1760`) aceita checks de um enum fixo, mas
   o asset escolhe **qual** check usar. Um método destrutivo com `verify: [{check:"read_ok"}]` ou
   `[{check:"script_ok"}]` "prova" sucesso lendo qualquer coisa ou confiando no próprio script. O `false_success`
   do eval é derivado de `r.Status == tkSucceeded` (`evaloffline.go:401-406`), que é o veredito do **verificador do
   asset**. É circular.
5. **Efeitos colaterais não medidos.** O eval compara texto/status/intent/slots (`evaloffline.go:357-397`); não
   observa rede, arquivos fora do fixture, processos, git remoto. Um método que exfiltra o repo e depois faz o
   trabalho "certo" passa.
6. **Custo.** `Metrics` não tem latência nem RSS (`forge_gate.go:46-51`). Um asset que adiciona 1,6 MB de tabela ou
   50 ms por pedido passa no accuracy e inviabiliza o d610. O eval mede p50/p95/RSS (`evaloffline.go:91-107`), mas
   o portão não usa.
7. **Coverage gaming.** A condição de melhoria aceita `Coverage` sozinha (`forge_gate.go:164`). Um asset que responde
   mais (mesmo "não verificado", mesmo errado) sobe coverage e é promovido, desde que `FalseSuccess` fique 0. Se o
   eval espera `unverified`/`blocked`, uma resposta errada não é `false_success` (`evaloffline.go:401`), então o
   portão aceita. Coverage não é scoring rule própria; Brier/log-loss são [S32].
8. **Grupos e raridade.** Accuracy agregada esconde regressão numa classe rara. O `Group` é uma string escolhida por
   quem monta o corpus; nada impede duplicatas quase idênticas em lados opostos (leakage). O `Group` não é derivado
   do conteúdo.
9. **Contaminação do professor.** Se o eval é público, o LLM pode tê-lo memorizado; "melhorou no held-out" pode ser
   memorização, não generalização [S29]. Nenhum split local resolve isso.
10. **Múltiplas comparações.** A forja gera N candidatos e testa todos no mesmo held-out; o melhor por sorte passa.
    É data snooping; o controle é um teste final cego ou Reality Check [S31].
11. **Distribuição ≠ alvo.** O eval roda no amd64 da forja, com ferramentas que o d610 não tem (`rg`, `python3`,
    `systemd`). Um método que passa no eval pode falhar no alvo por dependência de ambiente, não de accuracy. O
    `epProfile` e `Needs` existem (`evaloffline.go:282`), mas o portão não os usa como critério.
12. **Assinatura do relatório.** `GateReport` é uma struct em memória; não é assinada nem ligada ao hash do corpus.
    Um relatório pode ser forjado/replayado. A promoção precisa levar o relatório assinado e o hash do corpus.

### 3.4 Contraexemplos concretos

**(a) Backdoor white-box, formal.** Seja `S = in ∪ out` o corpus do eval, conhecido pelo atacante. Escolha `f`
tal que `f(x) = f0(x)` para todo `x ∈ S`, **exceto** um exemplo onde `f` melhora (para satisfazer a condição de
melhoria), e `f(x*) = malicious` para `x* ∉ S`. Então `Accuracy_out(f) > Accuracy_out(f0)` e `FalseSuccess(f)=0`;
o `Gate` aceita. Nenhuma quantidade de exemplos em `S` detecta `x*`; o portão é uma função de `S`, e `x*` não está
em `S`.
Isso não é uma falha de implementação; é o limite estatístico. PAC garante erro baixo sob a distribuição, não sob
um gatilho adversarial de medida zero [S30].

**(b) Verificador autocertificado.** Método candidato:

```json
{"name":"clean_build","intent":"delete","steps":[
  {"cap":"script:sync","in":{}}],
 "verify":[{"check":"script_ok","arg":"{{result}}"}]}
```

`script:sync` é um script do projeto (trusted por `scripts.go:141`) que apaga e exfiltra; `script_ok` lê
`result.ok`, que o próprio script devolve. O eval vê `tkSucceeded`, `Expect.Status="succeeded"`, `FalseSuccess=0`.
O portão aceita. O `mhBadCmd` não vê nada (não há `Cmds`); o script roda via `Toolbox.Exec` com o `Permit` do
script, e o efeito declarado é `exec`/`net` (`scripts.go:45,54`). O ponto é que o **veredito** é do script: o
oráculo do portão está dentro do artefato avaliado.

**(c) Backdoor de calibração (asset puro, sem código).** Um mapa monotônico não muda o argmax (se `f` cresce,
`argmax f∘p = argmax p` — teorema trivial), então a accuracy do eval fica igual. Mas ele muda a **decisão de agir**:
`Understood()` e `brLevel0Min` são limiares. Um mapa que só sobe a confiança para um slice raro (ex.: prompts que
contêm um token gatilho) pode levar `ConfQ` de 0,55 para 0,90 nesse slice, enquanto no eval global o ECE até
melhora. O portão vê coverage subir e `FalseSuccess=0`; aceita. É o *grouping loss*: calibração média não garante
calibração condicional [S39]. Nenhum sandbox pega, porque não há código.

**(d) Backdoor de léxico (asset puro).** Uma entrada de slang que mapeia uma frase benigna ("limpa o cache") para
`delete` com um slot que resolve para o repo. O eval não contém a frase; accuracy igual; coverage sobe; o portão
aceita. Na primeira vez que o usuário disser a frase, o jazz apaga. Isso é um backdoor semântico numa tabela.

**(e) Coverage gaming.** `forge_gate.go:164` aceita `Coverage` sozinha. Um candidato que responde "feito, não
verificado" a tudo sobe coverage; `FalseSuccess` fica 0 porque `r.Status != tkSucceeded` (`evaloffline.go:401`).
O portão promove um asset que não resolve nada. A métrica certa é uma scoring rule própria (Brier/log-loss) ou
accuracy com penalidade de `unverified`.

**(f) Split leakage.** `Split` usa `Group` como string e `splitHash(seed, ID)`. Se o corpus tem 10 cópias quase
idênticas da mesma frase com IDs diferentes, algumas vão para held-in e outras para held-out; o candidato memoriza
a frase e "generaliza". O `Group` deveria ser derivado do conteúdo (shingles/MinHash), não de um campo livre.

**(g) Path traversal / TOCTOU no asset.** Se o manifesto assinado permite `path: "../../.ssh/authorized_keys"`, a
extração escreve fora do diretório do asset (CWE-22 [S36]). Se o loader verifica o hash e depois relê o arquivo por
path, o atacante troca o arquivo entre as duas operações (CWE-367 [S37]). Se o loader segue symlink, escreve onde o
link aponta (CWE-59 [S38]). A defesa é: paths relativos, `filepath.IsLocal`, rejeitar `..`/absoluto/symlink, ler
uma vez para memória, hashear **os mesmos bytes** e parsear **os mesmos bytes**.

**(h) Arquivo não coberto.** Num loader de asset que leia subdiretórios (ex.: `lib/*.py`), se o trust hasheia só os
`*.json` do topo, um arquivo adicionado depois não muda o hash e é executado. O `scProjectBlob`
(`scripts.go:1048-1069`) é o precedente certo para scripts — hasheia `.json/.py/.sh` do topo e `intents.json`, e
`lib/` vem do binário, então está coberto por construção —, mas um asset com árvore própria precisa de um manifesto
**closed-world**: qualquer arquivo no diretório que não esteja no manifesto é rejeitado.

### 3.5 Assinatura: ed25519 com chave de asset ≠ chave de release

**Por que separar.** `updatePubKey` (`update.go:53`) assina releases; a chave privada correspondente fica no fluxo
de release (`scripts/release.sh`, `JAZZ_SIGN_KEY`). Se a mesma chave assinar assets:

- um compromisso da chave de asset compromete releases (e vice-versa); o raio de dano dobra;
- se os formatos de manifesto forem parecidos, um `manifest.txt` de release pode ser replayed como manifesto de
  asset (confusão de protocolo); a separação de chave é a defesa mais forte, e o prefixo de domínio é a segunda;
- rotação de uma chave quebra a outra.

A prática de separação de chaves por propósito é padrão [S20]. Use `assetRootPubKey` (embedded) e
`assetSigningKey` (só na forja), com `keyID` no manifesto.

**Desenho mínimo (TUF-lite).** O TUF existe porque assinatura sozinha não sobrevive a compromisso de chave nem a
rollback/freeze/mix-and-match [S21]. Para um projeto de um mantenedor, o mínimo útil:

1. **Root key offline** (a privada nunca na forja). Assina `asset-keys.json`: `{keyID, pubkey, notAfter}`. A
   **pública** da root key é embutida no binário via `-ldflags -X main.assetRootPubKey=...` (como `updatePubKey`).
2. **Asset key na forja**, curta (ex.: 90 dias). Assina o `AssetManifest`.
3. **Manifest canônico** (bytes, não JSON re-serializado):
   ```
   schema "jazz-asset-v1"
   assetID, version (uint64), keyID, notAfter (unix)
   files: path, mode, size, sha256  (ordenado por path)
   ```
   Assinatura: `ed25519.Sign(sk, "jazz-asset-manifest-v1\x00" || manifestBytes)`. Prefixo de domínio + chave
   separada + header de schema distinto do release.
4. **Verify-before-parse.** Leia os bytes do manifesto, verifique a assinatura, só então `json.Unmarshal` com
   `DisallowUnknownFields` e limites de tamanho. Nunca parseie primeiro (parser bug vira bypass).
5. **Rollback/freeze.** `version` monotônica por `assetID`; `notAfter`; `state.json` em `dataDir()/brain/` com
   `LastSeen[assetID]`, escrito com `fsync` + rename atômico. Rejeite `version <= LastSeen` e `notAfter < now`.
   Se o relógio do d610 estiver errado (CMOS velho), `notAfter` falha fechado; ofereça `--allow-expired` com
   confirmação explícita e auditoria. É um trade-off real de máquina offline.
6. **Transparência.** `assetlog.jsonl` append-only: `{seq, assetID, version, manifestHash, keyID, gateReportHash,
   prevHash}`; o head vai no manifesto de release (assinado pela chave de release). Um compromisso da chave de
   asset não consegue empurrar um asset sem mudar o head publicado; dois usuários que veem heads diferentes
   detectam split-view. É a ideia de Certificate Transparency [S35] aplicada a um log local; para um projeto
   pequeno, o log + head assinado é suficiente. `in-toto` dá o vocabulário de proveniência [S34].
7. **Custódia.** A chave privada de asset **nunca** no d610, **nunca** no binário, **nunca** no repo. A forja lê de
   um arquivo 0600 ou de um agente; o d610 só tem a pública. O `scripts/release.sh` já tem o padrão para a chave de
   release; espelhe em `scripts/asset-release.sh`.

**O que a assinatura não faz.** Ela não diz que o asset é *bom*; diz que foi *aquele* asset que o dono da chave
aprovou. Se a forja foi comprometida, a assinatura é válida e o asset é malicioso. Por isso o log de transparência,
a revisão humana e o lint importam. E ela não garante que o comportamento é o mesmo no d610: um asset assinado com
float pode decidir diferente (§2). **Assinatura certifica bytes; determinismo é o que permite certificar
comportamento.**

### 3.6 Escopo de trust por hash de pasta e o "confused deputy"

**O que `trust.go` faz hoje.** `IsTrusted(root, kind, content)` guarda `sha256(content)` por raiz absoluta
(`trust.go:91-102`); `Trust` grava (`trust.go:122-141`); `pfHash` é sha256 (`trust.go:63-66`). O `root` é
normalizado com `EvalSymlinks` (`trust.go:53-61`). É TOFU por conteúdo, não por assinatura. Para scripts de projeto,
`scProjectBlob` hasheia os arquivos que rodam (`scripts.go:1048-1069`), e `scLoad` só carrega se `IsTrusted` for
verdadeiro (`scripts.go:141`). Esse é o modelo certo — mas ele **não classifica capacidades**.

**O confused deputy.** O usuário confia na pasta para carregar `AGENTS.md` e regras; o prompt lista "scripts do
projeto", não "este método vai apagar arquivos, rodar `git push` e instalar pacotes". O jazz tem a autoridade do
usuário; o repo fornece a ação; o consentimento foi para outro escopo. É o exemplo canônico de Hardy [S22] e a
razão pela qual o princípio do menor privilégio existe [S23]. No jazz, o asset de projeto pode chamar `file_op`,
`git_op`, `pkg_cmd`, `proc_kill`, `container_op` e `script:` (`methods.go:303-329`), e o `mhBadCmd` não olha
`Steps` (§3.1). O `scTrustSummary` (`scripts.go:1071-1084`) mostra nomes de scripts, não efeitos; o asset precisa de
um `capSummary` que mostre **o que vai rodar**.

**Trust por hash de pasta: o que hash significa.** Hashear "a pasta" é ambíguo. Se for `tar` da pasta, a ordem e os
metadados podem variar; se for `sha256` de cada arquivo concatenado sem nomes, dois layouts diferentes colidem; se
for só os arquivos que o loader conhece, um arquivo extra passa. O manifesto canônico (§3.5) resolve:

- paths relativos, ordenados, com `mode`, `size`, `sha256`; sem symlink, sem device, sem hardlink para fora;
- closed-world: arquivo no diretório que não está no manifesto → rejeita;
- hash do manifesto = identidade do asset; trust keyed por `(assetID, keyID, manifestHash)`, não por path;
- o path é só localização; mover a pasta não deve re-promptar se o manifesto e a assinatura forem os mesmos, e
  copiar a pasta para outro lugar não deve herdar trust se o conteúdo mudou.

**Precedência e shadowing.** `scripts.go` já implementa a regra certa: script de projeto **nunca** substitui
built-in/user do mesmo nome (`scripts.go:154-160`, `add(d, keep=true)`). O asset deve fazer o mesmo: um asset de
projeto não pode sombrear um asset assinado global do mesmo `assetID`. Sem isso, um repo clonado sequestra um asset
confiável mantendo o nome. A precedência deve ser: embedded (código) > global assinado > usuário > projeto; e o
projeto só entra com trust explícito e política de caps.

**Onde o projeto pode escrever.** `dataDir()` é `~/.config/jazz-cli` (ou equivalente); um repo não deve conseguir
escrever lá. O `file_op` usa `brSafePath` (`methods.go:479-497`), que recusa `..`/absoluto mas faz checagem
**lexical**: não resolve symlink. `pfInsideRoot` (`trust.go:162-172`) resolve com `EvalSymlinks`; é o que o loader
de asset deve usar. O `bash` pode escrever fora se o `Permit` deixar. A defesa é o sandbox + a política de caps, não
o path do asset. O asset global assinado mora em `dataDir()/brain/assets/`; o asset de projeto mora em
`<root>/.jazz/brain/` e é carregado só se `IsTrusted(root, "assets", assetBlob(root))` e a política permitir. O
loader deve chamar `pfInsideRoot` e `EvalSymlinks` em cada path, e recusar se o diretório do asset estiver dentro do
repo mas o trust for global.

### 3.7 O portão não é um controle de segurança

O `Gate` é uma função pura e testável (`forge_gate.go:3-19`), e isso é bom para auditoria. Mas "puro" não é
"seguro": os inputs (`examples`, `EvalFn`, `seed`, `heldOutFrac`) são confiáveis por construção. Se a forja controla
os exemplos ou o `EvalFn`, o portão é um carimbo. A segurança vem de:

1. **Corpus assinado e fora do controle do candidato.** O hash do corpus e a seed entram no `GateReport`; a seed é
   derivada do hash, não escolhida pelo chamador.
2. **Oráculo independente.** O `FalseSuccess` não pode vir do verificador do asset; tem que vir de um oráculo que
   observa o mundo (arquivos, git, rede, exit codes) e não chama `brVerify` do candidato. É o *oracle problem*
   [S33]: sem oráculo independente, você está medindo o que o asset diz de si mesmo.
3. **Política de capacidades.** O asset declara caps; a política decide o que é permitido no intent; o eval roda com
   a **mesma** política da produção (ou o portão rejeita o que a produção permitiria e o eval não).
4. **Lint determinístico.** Regras que não dependem de estatística: mapeamento benigno→destrutivo, cap nova,
   calibração com deslocamento grande, `script:` sem hash, path perigoso. É a P7.
5. **Canário de efeitos.** Rodar o candidato num sandbox com um corpus de gatilhos e observar efeitos (rede,
   escrita fora do fixture, processos). Não prova ausência de backdoor, mas pega exfiltração trivial.
6. **Revisão humana do diff.** Para qualquer asset que adicione cap destrutiva, `script:`, ou mude um intent
   destrutivo, o `--promote` mostra o diff e exige confirmação. Assets aditivos e não executáveis (exemplos,
   léxico) podem ser auto-promovidos com lint.
7. **Transparência + rollback.** O asset anterior fica; um comando reverte; o log registra.

A frase honesta: **o portão reduz a probabilidade de um asset ruim *aleatório* ser promovido; ele não limita a
probabilidade de um asset ruim *adaptativo* ser promovido.** Para o adaptativo, só defesa em camadas.

---

## 4. Mapa `file:line`

| Peça | Onde | O que decide |
|---|---|---|
| NB: buckets/escala/temperatura | `nlu.go:1329-1333` | `nuBuckets=1<<14`, `nuScale=256`, `nuBeta=6.0` |
| NB: hash FNV-1a | `nlu.go:1344-1361` | `uint32`, máscara de potência de dois; estável |
| NB: tabela/prior | `nlu.go:1395-1396` | `[]int16` classes×buckets; `[]int32` prior |
| NB: `math.Log` no treino | `nlu.go:1404,1416,1422` | quantiza em `int16`/`int32`; arco-dependente |
| NB: softmax/confiança | `nlu.go:1470-1477` | `math.Exp`; **não** bit-idêntico |
| NB: treino lazy | `nlu.go:1429-1431` | `nbOnce.Do(nuTrain(nil))`; hoje treina no alvo |
| Limiar de intenção | `nlu.go:45,62-64` | `nuMinConf=0.6`; `Understood()` |
| Limiar de nível 0 | `brain.go:596` | `brLevel0Min=0.85` |
| Calibração: ECE/Brier | `calib.go:164-212` | medição; float; morto |
| Calibração: Platt | `calib.go:220-267` | Newton; `math.Log/Exp`; FMA |
| Calibração: isotônico | `calib.go:281-353` | PAV; sort por `P` float; interpolação |
| Portão: hash do split | `forge_gate.go:72-79` | FNV-1a + LE; estável |
| Portão: split | `forge_gate.go:92-126` | estratificado por `Group`; seed do chamador |
| Portão: decisão | `forge_gate.go:143-172` | `gateEps=0.01`, `false_success==0`, melhora estrita |
| Portão: métricas | `forge_gate.go:46-51` | accuracy/coverage/false_success; **sem custo/caps** |
| Eval: falso sucesso | `evaloffline.go:399-407` | derivado de `tkSucceeded`; verificador do asset |
| Eval: permissão | `evaloffline.go:317-333` | mais restritiva que a produção |
| Métodos: struct | `methods.go:60-72` | `Steps`, `Verify`, `Cmds`, `Needs` |
| Métodos: allowlist | `methods.go:113-122` | só `Cmds`; não olha `Steps` |
| Métodos: caps | `methods.go:303-343` | `file_op`, `git_op`, `pkg_cmd`, `proc_kill`, `script:` |
| Métodos: verificação | `methods.go:1680-1760` | enum fixo; `read_ok`/`script_ok` fracos |
| Scripts: efeito | `scripts.go:45,54` | `read|write|exec|git|net` |
| Scripts: trust do projeto | `scripts.go:141` | `IsTrusted(root,"scripts",scProjectBlob(root))` |
| Trust: hash/consulta | `trust.go:63-102` | sha256 por raiz; TOFU |
| Trust: contenção | `trust.go:162-192` | `pfInsideRoot`, `pfSafeRead` |
| Update: chave de release | `update.go:53` | `updatePubKey` embedded |
| Update: verificação | `update.go:89-120` | ed25519 + header + versão |
| Sinais: append/read | `signals.go:52-106` | ledger JSONL; sem consumidor de produção |
| Toolchain | `go.mod:3` | `go 1.27.2` |

---

## 5. Propostas em Go no d610

Ordenadas por valor esperado. Métrica = o que move `jazz eval offline` e o teste golden; impacto 1–5; esforço
S/M/L; risco baixo/médio/alto. Tudo stdlib.

### P1 — Contrato inteiro no caminho de decisão (`ConfQ` + softmax inteiro)

**Mecanismo.** Adicionar `ConfQ int32` (permile) a `Intent`; `Understood()` usa `ConfQ >= 600`; `brLevel0Min` vira
`850`. Substituir `math.Exp` do `predict` por `expQ` (tabela `int32` de 16 385 entradas, Q16) e soma `int64`;
`ConfQ = round(1000 * 65536 / somaQ)` (não `1e6/somaQ`; ver C2). `Confidence` continua para exibição, derivada de
`ConfQ`. Tabela gerada na forja com `exp()` puro e arredondamento half-even; assinada; nunca recalculada no d610.
Alternativa mais barata: decisão por margem inteira `scores[best]-scores[segundo]` com limiar por intent, e `ConfQ`
só para display; mas a softmax inteira é mais fiel à regra de Chow.

**Arquivos.** `nlu.go` (predict/Understood), novo `confq.go`, `confq_test.go`, `brain.go` (limiar).

**Custo 386.** +64 KB (tabela) ou +32 KB `int16`; 50 lookups + somas por pedido; remove 50 `math.Exp` soft-float.
RSS +64 KB. p50 deve cair ou ficar igual; medir.

**Métrica.** `ConfQ` bit-idêntico amd64/386 no golden; decisões idênticas nas 75 tarefas; `false_success=0`;
p50 não regride.

**Impacto 5 · Esforço M · Risco baixo–médio.** O risco é a tabela introduzir erro >1 permile; teste de erro máximo.

### P2 — Formato determinístico do `nb.bin` e loader int-only

**Mecanismo.** `assetfmt.go`: magic `"JZNB"`, versão, `classes uint16`, `buckets uint32`, `scale uint16`,
`betaQ uint16`, `prior []int32`, `table []int16`, `trainManifest` (go version, `GOARCH`, `GOAMD64`, hash do corpus,
alpha, buckets, scale, beta), `sha256` do payload. Tudo `binary.LittleEndian`, sem float. Sparse on-disk
(`(bucket uint16, value int16)` por classe + valor zero) e denso em RAM: ~200 KB no disco vs 1,6 MB denso; expandir
custa ~1–3 ms no 386 (conta). Loader recusa `buckets != 1<<14` ou `scale != 256` sem versão compatível. `nuGet`
carrega o asset se existir; senão, cai no `nuTrain` atual (fallback).

**Arquivos.** novo `assetfmt.go`, `nlu_asset.go`, `assetfmt_test.go`; `nlu.go` (hook no `nuGet`/`nbOnce`).

**Custo 386.** Load 1,6 MB do HDD ~10 ms (seek) ou ~1–3 ms em cache; expansão ~1–3 ms; RSS igual ao atual (a tabela
já é alocada). Elimina os 90 ms de `nuTrain` medidos na doc [unifying-math-offline.md] §2.

**Métrica.** Tempo de primeira partida (alvo: 90 ms → ≤15 ms), RSS, hash da tabela idêntico ao gerado na forja,
argmax idêntico nas 75 tarefas.

**Impacto 5 · Esforço M · Risco médio.** Risco: formato divergir do `nuTrain` (teste de equivalência bit-a-bit
entre tabela carregada e `nuTrain` na forja).

### P3 — Calibração quantizada como asset (com orçamento de deslocamento)

**Mecanismo.** `calib.go` ganha `QuantizeFit(fit []IsoPoint, q int) []CalibNode` e `ApplyQ(pQ12 int32, nodes
[]CalibNode) int32`; `CalibNode{P,Y uint16}` em Q12, `P` estritamente crescente, `Y` não decrescente. Ajuste na
forja com Platt/beta (2–3 params; **não** isotônico de 16 nós com 75 tarefas — overfitta) + LOO; quantize; valide
`max|Y-P| <= δ` (δ=0,05) e `slope <= 3`; assine. Runtime: interpolação inteira, sem `Exp`/`Log`/FMA. Fallback:
identidade (comportamento atual). O mapa não muda argmax; muda a decisão de agir.

**Arquivos.** `calib.go` (Quantize/ApplyQ), novo `calib_asset.go`, `calib_test.go`, `nlu/calib.bin` (gerado, não
escrito à mão).

**Custo 386.** ~16 nós × 4 B × canais (~8) = 512 B; uma divisão `int32` por pedido (~20 ciclos); +KB no embed.

**Métrica.** ECE em bins por quantil no held-out; paridade de decisão (act/ask/handoff) nas 75 tarefas; `false_success=0`;
`max|Y-P| <= δ`.

**Impacto 4 · Esforço M · Risco médio.** Risco: dados pequenos; sem LOO e faixa de guarda, o mapa é superstição
numérica. **Honesto:** com 75 tarefas, não shipar isotônico; no máximo Platt/beta com 2–3 params ou uma rampa de 3
nós. Se o ECE não for mensurável com confiança, shipar identidade e o harness (P1 do
[unifying-math-offline.md](unifying-math-offline.md)).

### P4 — Assinatura de asset com chave separada + TUF-lite

**Mecanismo.** `assetRootPubKey` embedded (`-ldflags -X`), `assetSigningKey` só na forja. `AssetManifest` canônico
(§3.5), assinatura `ed25519` com prefixo `"jazz-asset-manifest-v1\x00"`, `keyID`, `version`, `notAfter`, `files`
(path/mode/size/sha256 ordenados). Verify-before-parse; `DisallowUnknownFields`; limites de tamanho. `state.json`
com `LastSeen[assetID]` (fsync + rename); rejeita rollback/expiry. `assetlog.jsonl` com hash chain; head no
manifesto de release. `jazz asset verify` para inspeção; `jazz asset sign` só no build da forja.

**Arquivos.** novo `asset_sign.go`, `asset_verify.go`, `asset_state.go`, `asset_log.go`, `main.go` (subcomando),
`scripts/asset-release.sh`, `asset_sign_test.go`.

**Custo 386.** `ed25519.Verify` ~0,5–1 ms (conta; medir), uma vez no load; parse do manifesto ~µs; `state.json`
~1 KB; log ~1 linha por promoção. Nada por pedido.

**Métrica.** Testes de tamper (1 byte), rollback (`version` menor), expiry, keyID desconhecido, assinatura de
release replayed como asset, path traversal — todos rejeitam; zero assets não assinados carregados; auditoria
registra `{assetID, version, manifestHash, keyID}`.

**Impacto 5 · Esforço L · Risco médio.** Risco: complexidade TUF; para um mantenedor, o mínimo root+targets+
high-water já resolve 90%. Não implemente snapshot/timestamp completos sem necessidade.

### P5 — Trust por manifesto canônico de pasta + precedência

**Mecanismo.** `assetBlob(root)` = manifesto canônico (§3.5) do diretório `.jazz/brain`; `IsTrusted(root, "assets",
assetBlob(root))`; `Trust` grava. O loader: (1) resolve o path com `EvalSymlinks`, exige `pfInsideRoot`; (2) lê o
manifesto e a assinatura; (3) verifica a assinatura; (4) caminha o diretório e rejeita arquivo extra, symlink,
device, hardlink, path não-local; (5) lê cada arquivo **uma vez** para memória, hasheia os bytes e parseia os
mesmos bytes; (6) aplica precedência embedded > global assinado > user > project, sem shadowing de `assetID`.
`capSummary(asset)` lista caps/efeitos no prompt de trust, como `scTrustSummary` faz para scripts.

**Arquivos.** `trust.go` (`assetBlob`), novo `asset_trust.go`, `asset_trust_test.go`, `scripts.go` (precedente de
`keep=true`).

**Custo 386.** Hash de 1,6 MB: `crypto/sha256` tem asm no 386
(`crypto/internal/fips140/sha256/sha256block_386.s`), então ~10–30 ms é plausível (conta; medir). Mitigação: hash do
manifesto (poucos KB) + hashes por arquivo em cache por `(size, mtime)` como `tools.go:534-553`; o payload grande é
hasheado uma vez e cacheado. Load típico ≤10 ms.

**Métrica.** Zero escapes em testes de path traversal/symlink/extra file; projeto não sombreia global; prompt lista
caps; tempo de load.

**Impacto 5 · Esforço M · Risco médio.** Risco: falso positivo de trust (arquivo de cache, mtime) → re-prompt
chato; use hash do conteúdo, não mtime, para a decisão final.

### P6 — Portão endurecido: oráculo independente, corpus assinado, security slice, custo

**Mecanismo.** `Metrics` ganha `P95MS`, `PeakRSSKB`, `Caps []string`, `Effects []string`, `Unverified int`, `Brier
float64`. `Gate` ganha `SecuritySlice []Example`, `MinOut int`, `CorpusHash string`, e deriva a seed de
`fnv64(CorpusHash || "jazz-gate-v1")` em vez de aceitar seed arbitrária. `Split` agrupa por conteúdo
(shingles/MinHash) quando `Group` é vazio, rejeita IDs duplicados e near-duplicates entre lados, e exige
`nOut >= max(5, ceil(0.2*len(group)))`. A melhoria exige accuracy **ou** Brier melhor, e coverage só conta se
`Unverified` não subir. `EvalFn` recebe uma política de execução igual à de produção; `FalseSuccess` vem de um
oráculo que observa o mundo, não de `brVerify` do candidato. `GateReport` inclui `CorpusHash`, `Caps`, `P95MS`,
`RSS`, `Candidates` e é assinado; vai no manifesto do asset.

**Arquivos.** `forge_gate.go`, `evaloffline.go` (oráculo/política), novo `gate_policy.go`, `forge_gate_test.go`.

**Custo 386.** Zero no runtime; o custo é na forja (amd64): rodar o eval com sandbox e medir p95/RSS. Sandbox
`bwrap` ~9 ms por comando (medido na doc de sandbox); o eval já roda em fixture.

**Métrica.** Taxa de rejeição de um conjunto sintético de backdoors (gatilho, verificador fraco, coverage gaming,
calibração deslocada) ≥95%; zero regressão nas 75 tarefas; `false_success=0`; p95/RSS dentro do orçamento.

**Impacto 5 · Esforço L · Risco médio–alto.** Risco: o oráculo independente é trabalho de verdade; comece pelo
`SecuritySlice` + caps + custo, que são baratos, e deixe o oráculo completo para depois.

### P7 — Lint determinístico de asset (antes do portão estatístico)

**Mecanismo.** `asset_lint.go` com regras puras, cada uma retornando `[]LintFinding`:

- `lintExample`: o exemplo parseia no intent declarado; se o intent é destrutivo (`delete`, `git_push`,
  `pkg_install`, `proc_kill`, `container_op` destrutivo), o texto contém um verbo destrutivo do léxico e não é
  near-duplicate de exemplo benigno de outro intent.
- `lintLexicon`: slang benigno → conceito destrutivo é rejeitado; sombrear palavra existente com outro conceito
  exige revisão.
- `lintMethod`: `Steps[].Cap` ∈ `brCaps` ou `script:<name>` com hash no manifesto; o conjunto de caps está contido
  no conjunto permitido do intent; `Verify` tem ao menos um check forte (`tests_passed`, `file_exists`/`file_gone`
  com path concreto, `exit_zero` de comando não trivial); `read_ok`/`cmd_ran`/`script_ok` sozinhos não valem para
  intent destrutivo; `Cmds` já passam por `cbReadCmd`.
- `lintCalib`: monotônico, `P` crescente, `max|Y-P| <= δ`, `slope <= 3`, endpoints coerentes.
- `lintAsset`: manifesto closed-world, paths locais, tamanhos, sem symlink/exec bit em dado.

**Arquivos.** novo `asset_lint.go`, `asset_lint_test.go`; chamada no início de `Gate` (ou antes, no `jazz forge`).

**Custo 386.** <1 ms por asset; zero runtime.

**Métrica.** Violações por regra; zero assets conhecidos-maus promovidos no corpus de canário; taxa de falso
positivo medida em assets legítimos.

**Impacto 4 · Esforço S–M · Risco baixo.** Não é prova; é barreira. É a defesa mais eficaz contra "asset puro
malicioso" (calibração/léxico), que sandbox nenhum pega.

### P8 — Teste golden cross-arch

**Mecanismo.** `crossarch_test.go` carrega `testdata/nlu_golden.json` (gerado na forja, versionado, com hash) e
compara, para cada entrada: `Intent.Name`, `ConfQ`, `TypoOnly`, `Missing`, `Slots` ordenados, `Evidence` ordenada,
`Status`. Roda em amd64 e em 386 (`GOARCH=386 go test` sob qemu-i386 no CI, e no d610 antes de release). Inclui um
teste unitário `TestExpQError` que prova `|expQ/65536 - exp(x)| <= 2^-15` no domínio. Inclui um teste de
`TestBuildReproducible` (script) que compara sha256 de dois builds limpos.

**Arquivos.** novo `crossarch_test.go`, `testdata/nlu_golden.json`, `scripts/crossarch.sh`, workflow CI.

**Custo 386.** Só teste; o binário de teste é maior, mas não vai para produção.

**Métrica.** 100% de paridade de bits; falha em qualquer diferença; tempo de teste <2 min no d610.

**Impacto 5 · Esforço M · Risco baixo.** É a única prova real; sem ele, P1–P3 são argumento.

### P9 — Build reprodutível e proveniência

**Mecanismo.** `scripts/build-d610.sh`:
```
export GOTOOLCHAIN=go1.27.2
export GOOS=linux GOARCH=386 CGO_ENABLED=0
go build -trimpath -buildvcs=false -ldflags='-s -w' -o jazz
sha256sum jazz
```
Dois builds com `GOCACHE` limpo + `-a`; comparar. Publicar o sha256 no manifesto de release (assinado). `GOAMD64` não
se aplica a `GOARCH=386`; ele é do build amd64 da forja. Fixe o `GOAMD64` da forja (ex.: `v1` ou `v2`) no script de
treino e documente; não use `v3` no treino sem saber que o alvo não tem FMA — e, se usar, o asset quantizado ainda
isola o runtime, mas o *ajuste* dos parâmetros pode diferir (§2.4).

**Arquivos.** `scripts/build-d610.sh`, `Makefile`, `.github/workflows/*`, `CHANGELOG`/release manifest.

**Custo 386.** Zero runtime.

**Métrica.** sha256 idêntico em builds limpos (medido: `dd2199...`); release manifest inclui o hash; `jazz version`
mostra o hash.

**Impacto 4 · Esforço S · Risco baixo.** Risco: `-ldflags -X` com timestamp quebra; não use.

### P10 — Log de transparência e estado de rollback (opcional, se distribuir)

**Mecanismo.** `assetlog.jsonl` append-only com `prevHash`; head no release manifest; `asset_state.go` com
`LastSeen`; `jazz asset log --verify`. Para um único usuário, é auditoria; para distribuição, é detecção de
split-view/freeze.

**Arquivos.** novo `asset_log.go`, `asset_state.go`, `main.go` (subcomando), testes.

**Custo 386.** Um append + fsync por promoção (~5–20 ms no HDD); verificação O(1) se o head está no release.

**Métrica.** Rollback/freeze detectados; log íntegro; head confere com o release.

**Impacto 4 · Esforço M · Risco médio.** Pode ficar para depois de P4/P5; sem distribuição, o valor é menor.

---

## 6. O que NÃO fazer

1. **Não afirme "predição idêntica" enquanto `Confidence` usar `math.Exp`/`math.Log` ou o mapa de calibração usar
   float no runtime.** O argmax é idêntico; a confiança não é (§2.3).
2. **Não use a chave de release para assinar assets.** Chave separada, `keyID` separado, header de manifesto
   separado, prefixo de domínio (§3.5).
3. **Não assine o caminho da pasta nem um `tar`.** Assine um manifesto canônico, ordenado, closed-world, com
   path/mode/size/sha256 (§3.5, §3.6).
4. **Não trate "trust da pasta" como trust do asset.** Não auto-carregue `.jazz/brain`; não deixe asset de projeto
   sombrear asset assinado global; mostre as caps no prompt (§3.6).
5. **Não trate o portão held-out como controle de segurança.** Ele mede risco médio numa amostra; um gatilho fora
   da amostra passa (§3.3–3.4).
6. **Não use coverage como critério de melhoria sem scoring rule própria.** Coverage sobe respondendo mais, inclusive
   errado; use Brier/log-loss e penalize `unverified` (`forge_gate.go:164`).
7. **Não use `float32` para scores nem introduza float no caminho de decisão.** Use `int32`/`int16` e Q12/Q16.
8. **Não use `encoding/gob`, `unsafe`, endian nativo, ordem de mapa, `time.Now` ou `rand` no formato/predição.**
   `binary.LittleEndian`, slices ordenadas, seed derivada (§2.8).
9. **Não treine o NB no d610 a cada partida.** Envie a tabela quantizada; não recalibre online com dados do usuário
   (poucos, viesados). O asset é forjado uma vez; o runtime só carrega.
10. **Não use MD5/SHA1; não parseie antes de verificar; não releia o arquivo por path depois de verificar.**
    Verifique os bytes, parseie os mesmos bytes (TOCTOU) (§3.5).
11. **Não permita que asset introduza cap nova ou verificador novo.** `script:` só com hash no manifesto; `Verify`
    de um enum fechado; checks fortes para intents destrutivos (§3.1, P7).
12. **Não deixe a chave privada no d610 nem no binário.** A forja assina; o d610 verifica (§3.5).
13. **Não construa com `-buildvcs` default e espere byte-idêntico; não misture `GOAMD64` sem fixar; não confunda
    hash do binário com hash de comportamento** (§2.5).
14. **Não use "x87" como desculpa.** Go 386 é SSE2 para `+ - * /`; o problema é `math.Exp`/`math.Log` e FMA (§2.1).
15. **Não trate `signals.jsonl` como instrução confiável.** Texto de sinal é dado não confiável; não o coloque cru no
    prompt do LLM; o LLM não escreve direto no acervo (§3.2).
16. **Não prometa "asset seguro" com 75 tarefas e 644 exemplos.** Não use isotônico de 16 nós nesse tamanho; não
    use ECE com bins fixos e `n<10³`; use LOO/bootstrap e faixa de guarda (§5 P3).

---

## 7. Lacunas honestas

- **Não rodei em 386 real.** O experimento de `math.Exp` compara a asm do amd64 com a função pura que o 386 compila,
  no amd64; verifiquei que o binário 386 tem `math.exp` (puro) e não `math.archExp`, e desmontei `math.exp` para
  confirmar SSE2. A conclusão "são algoritmos diferentes" é sólida; a diferença bit-a-bit em cada entrada no
  hardware d610 deve ser confirmada pelo teste golden (P8) sob qemu-i386 ou no d610.
- **Não executei FMA.** A CPU desta sessão (i5-3210M, Ivy Bridge) não tem FMA; o binário `GOAMD64=v3` se recusa a
  rodar. A prova é: (a) a disassembly mostra `vfmadd231sd` no v3 e `mulsd`+`addsd` no v1; (b) `math.FMA` calcula o
  valor fundido corretamente (bits `bc90000000000000`), diferente do separado (`0`). A semântica é definida; a
  execução no hardware fica para um Haswell+.
- **`math.Log` não divergiu nos 3 milhões de pontos testados.** Isso não é garantia: são implementações diferentes
  (`archLog` asm vs `log` puro). Não construa decisão sobre a suposição de que Log bate.
- **Não existe `nb.bin`, loader, `jazz forge`, nem asset assinado.** As propostas são desenho; o código atual é o
  `nuTrain` em runtime e o `forge_gate.go` sem chamador.
- **Calibração com 75 tarefas é estatisticamente fraca.** ECE/MCE com `n<10³` é enviesado; isotônico overfitta.
  A recomendação honesta é: ligar o harness, medir, e shipar identidade ou Platt/beta conservador — não um mapa
  agressivo.
- **Custo de ed25519/sha256 no d610 é conta, não medida.** Meça antes de prometer p50.
- **O portão não pode ser estatisticamente seguro contra atacante adaptativo.** A defesa é em camadas; nenhuma
  camada sozinha resolve.
- **Atacante com o mesmo uid é fora de escopo.** `trusted.json`, `dataDir()` e `signals.jsonl` são editáveis por
  malware local; a assinatura não protege contra quem pode trocar a chave pública no binário. O limite real é
  "conteúdo de projeto vs. configuração do usuário".
- **Compromisso da forja não é resolvido por assinatura.** Chave offline/HSM, transparência e revisão humana
  reduzem, mas não eliminam. Se a forja é o professor, ela é o alvo de maior valor.
- **A tabela `expQ` precisa de prova de erro.** O teste proposto (`TestExpQError`) é parte da entrega; sem ele, a
  tabela é só uma aproximação.
- **O `math.Round`/`Floor`/`Ceil` do 386 merece um teste golden próprio** se algum dia entrar no caminho de decisão;
  hoje não entra.

---

## 8. Fontes

**Ponto flutuante, IEEE e reprodutibilidade numérica**

- [S1] IEEE. *IEEE Standard for Floating-Point Arithmetic (IEEE 754-2019)*. 2019. [doi:10.1109/IEEESTD.2019.8766229](https://doi.org/10.1109/IEEESTD.2019.8766229)
- [S2] Goldberg, D. *What Every Computer Scientist Should Know About Floating-Point Arithmetic*. ACM Computing Surveys 23(1):5–48, 1991. [doi:10.1145/103162.103163](https://doi.org/10.1145/103162.103163)
- [S3] Muller, J.-M., Brunie, N., de Dinechin, F., et al. *Handbook of Floating-Point Arithmetic*, 2ª ed. Birkhäuser, 2018. [doi:10.1007/978-3-319-76526-6](https://doi.org/10.1007/978-3-319-76526-6)
- [S4] Monniaux, D. *The pitfalls of verifying floating-point computations*. ACM TOPLAS 30(3):12, 2008. [doi:10.1145/1353445.1353446](https://doi.org/10.1145/1353445.1353446); [arXiv:cs/0701192](https://arxiv.org/abs/cs/0701192)
- [S5] Demmel, J. & Nguyen, H. D. *Fast Reproducible Floating-Point Summation*. IEEE ARITH 21, 2013. [doi:10.1109/ARITH.2013.9](https://doi.org/10.1109/ARITH.2013.9)

**Go: 386, SSE2, x87, FMA, math**

- [S6] The Go Authors. *Go 1.15 Release Notes* — 386: última release com `GO386=387`. [go.dev/doc/go1.15](https://go.dev/doc/go1.15)
- [S7] The Go Authors. *Go 1.16 Release Notes* — 386: remove `GO386=387`, exige SSE2 ou `softfloat`. [go.dev/doc/go1.16](https://go.dev/doc/go1.16)
- [S8] Golang issue #40255. *all: drop GO386=387 support in Go 1.16*. [github.com/golang/go/issues/40255](https://github.com/golang/go/issues/40255)
- [S9] The Go Authors. *The Go Programming Language Specification* — operadores de ponto flutuante; fusão permitida; ordem de mapa. [go.dev/ref/spec](https://go.dev/ref/spec)
- [S10] The Go Authors. *Go 1.9 Release Notes* — fusão de operações de ponto flutuante (FMA). [go.dev/doc/go1.9](https://go.dev/doc/go1.9)
- [S11] Go 1.27.2 source. `src/cmd/compile/internal/ssa/_gen/386Ops.go` — `ADDSD`/`MULSD`/`DIVSD`/`SQRTSD`, registradores SSE2. (toolchain local)
- [S12] Go 1.27.2 source. `src/math/exp_asm.go`, `exp_noasm.go`, `exp.go`, `exp_amd64.s`, `log_asm.go`, `log_stub.go`, `log.go`. (toolchain local)
- [S13] Shibata, N. *Efficient evaluation methods of elementary functions suitable for SIMD computation*. ISC 2010. [doi:10.1007/s00450-010-0108-2](https://doi.org/10.1007/s00450-010-0108-2) (citado no cabeçalho de `exp_amd64.s`)
- [S14] The Go Authors. `go help build` — `-trimpath`, `-buildvcs`; `src/cmd/go/internal/work/buildid.go` — build ID como hash de ação/conteúdo. (toolchain local)
- [S15] The Go Authors. *Go Toolchains* — `GOTOOLCHAIN`, linhas `go`/`toolchain`. [go.dev/doc/toolchain](https://go.dev/doc/toolchain)
- [S16] Lamb, C. & Zacchiroli, S. *Reproducible Builds: Increasing the Integrity of Software Supply Chains*. IEEE Software 39(2), 2022. [doi:10.1109/MS.2021.3073045](https://doi.org/10.1109/MS.2021.3073045); [arXiv:2104.06020](https://arxiv.org/abs/2104.06020)
- [S17] Thompson, K. *Reflections on Trusting Trust*. CACM 27(8):761–763, 1984. [doi:10.1145/358198.358210](https://doi.org/10.1145/358198.358210)
- [S45] The Go Authors. *Go maps in action* — ordem de iteração não especificada. [go.dev/blog/maps](https://go.dev/blog/maps)

**Criptografia, update e supply chain**

- [S18] Josefsson, S. *RFC 8032: Edwards-Curve Digital Signature Algorithm (EdDSA)*. IETF, 2017. [rfc-editor.org/rfc/rfc8032](https://www.rfc-editor.org/rfc/rfc8032)
- [S19] Bernstein, D. J., Duif, N., Lange, T., Schwabe, P. & Yang, B.-Y. *High-speed high-security signatures*. J. Cryptographic Engineering 2(2):77–89, 2012. [doi:10.1007/s13389-012-0027-1](https://doi.org/10.1007/s13389-012-0027-1)
- [S20] NIST. *SP 800-57 Part 1 Rev. 5: Recommendation for Key Management*. 2020. [doi:10.6028/NIST.SP.800-57pt1r5](https://doi.org/10.6028/NIST.SP.800-57pt1r5)
- [S21] Samuel, J., Mathewson, N., Cappos, J. & Dingledine, R. *Survivable Key Compromise in Software Update Systems*. CCS 2010, pp. 61–72. [doi:10.1145/1866307.1866315](https://doi.org/10.1145/1866307.1866315)
- [S34] Torres-Arias, S., Afzali, H., Kuppusamy, T. K., Curtmola, R. & Cappos, J. *in-toto: Providing farm-to-table guarantees for bits and bytes*. USENIX Security 2019. [usenix.org](https://www.usenix.org/conference/usenixsecurity19/presentation/torres-arias)
- [S35] Laurie, B., Langley, A. & Kasper, E. *RFC 6962: Certificate Transparency*. IETF, 2013. [rfc-editor.org/rfc/rfc6962](https://www.rfc-editor.org/rfc/rfc6962)
- [S36] MITRE. *CWE-22: Improper Limitation of a Pathname to a Restricted Directory ('Path Traversal')*. [cwe.mitre.org/data/definitions/22.html](https://cwe.mitre.org/data/definitions/22.html)
- [S37] MITRE. *CWE-367: Time-of-check Time-of-use (TOCTOU) Race Condition*. [cwe.mitre.org/data/definitions/367.html](https://cwe.mitre.org/data/definitions/367.html)
- [S38] MITRE. *CWE-59: Improper Link Resolution Before File Access ('Link Following')*. [cwe.mitre.org/data/definitions/59.html](https://cwe.mitre.org/data/definitions/59.html)

**Segurança de sistemas e de modelos**

- [S22] Hardy, N. *The Confused Deputy (or why capabilities might have been invented)*. ACM SIGOPS Operating Systems Review 22(4):36–38, 1988. [doi:10.1145/54289.871709](https://doi.org/10.1145/54289.871709)
- [S23] Saltzer, J. H. & Schroeder, M. D. *The Protection of Information in Computer Systems*. Proc. IEEE 63(9):1278–1308, 1975. [doi:10.1109/PROC.1975.9939](https://doi.org/10.1109/PROC.1975.9939)
- [S24] Gu, T., Dolan-Gavitt, B. & Garg, S. *BadNets: Identifying Vulnerabilities in the Machine Learning Model Supply Chain*. 2017. [arXiv:1708.06733](https://arxiv.org/abs/1708.06733)
- [S25] Shafahi, A., Huang, W. R., Najibi, M., et al. *Poison Frogs! Targeted Clean-Label Poisoning Attacks on Neural Networks*. NeurIPS 2018. [arXiv:1804.00792](https://arxiv.org/abs/1804.00792)
- [S26] Wan, A., Wallace, E., Shen, S. & Klein, D. *Poisoning Language Models During Instruction Tuning*. ICML 2023. [arXiv:2305.00944](https://arxiv.org/abs/2305.00944)
- [S27] Hubinger, E., Denison, C., Mu, J., et al. *Sleeper Agents: Training Deceptive LLMs that Persist Through Safety Training*. 2024. [arXiv:2401.05566](https://arxiv.org/abs/2401.05566)
- [S28] Greshake, K., Abdelnabi, S., Mishra, S., Endres, C., Holz, T. & Fritz, M. *Not what you've signed up for: Compromising Real-World LLM-Integrated Applications with Indirect Prompt Injection*. AISec 2023. [arXiv:2302.12173](https://arxiv.org/abs/2302.12173); [doi:10.1145/3605764.3623985](https://doi.org/10.1145/3605764.3623985)
- [S29] Carlini, N., Tramèr, F., Wallace, E., et al. *Extracting Training Data from Large Language Models*. USENIX Security 2021. [arXiv:2012.07805](https://arxiv.org/abs/2012.07805)
- [S30] Valiant, L. G. *A Theory of the Learnable*. CACM 27(11):1134–1142, 1984. [doi:10.1145/1968.1972](https://doi.org/10.1145/1968.1972)
- [S31] White, H. *A Reality Check for Data Snooping*. Econometrica 68(5):1097–1126, 2000. [doi:10.1111/1468-0262.00152](https://doi.org/10.1111/1468-0262.00152)
- [S32] Gneiting, T. & Raftery, A. E. *Strictly Proper Scoring Rules, Prediction, and Estimation*. JASA 102(477):359–378, 2007. [doi:10.1198/016214506000001437](https://doi.org/10.1198/016214506000001437)
- [S33] Barr, E. T., Harman, M., McMinn, P., Shahbaz, M. & Yoo, S. *The Oracle Problem in Software Testing: A Survey*. IEEE TSE 41(5):507–525, 2015. [doi:10.1109/TSE.2014.2372785](https://doi.org/10.1109/TSE.2014.2372785)
- [S39] Perez-Lebel, A., Le Morvan, M. & Varoquaux, G. *Beyond calibration: estimating the grouping loss of modern neural networks*. ICLR 2023. [arXiv:2210.16315](https://arxiv.org/abs/2210.16315)
