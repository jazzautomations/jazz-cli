# O harness como bomba de negentropia: termodinâmica da computação aplicada ao jazz

**A intuição do dono está certa no balanço e errada no mecanismo. O jazz não é um demônio de Maxwell barato; é um
compressor com dívida — e a conta que decide a arquitetura é `N*`, o número de reusos que paga a forja.**

Pesquisa de 04/11/2026. Continua [unifying-math-offline.md](unifying-math-offline.md) (informação, MDL, o que virou
teorema), [math-offline-brain.md](math-offline-brain.md) (calibração, informação, suavização) e
[decision-policy-calibrated.md](decision-policy-calibrated.md) (posterior calibrado vs entropia). Não repete o
pipeline NLU nem o layout dos índices. O objeto aqui é outro: **a termodinâmica física da computação, aplicada ao
jazz, com o veredito de engenharia de cada peça.**

Convenções: `file:line` refere-se ao `main` @ `584494d`. **Medido** = número já registrado em `results/`, não
re-medido nesta sessão. **(conta)** = aritmética minha a partir de constantes publicadas, mostrada passo a passo.
**(estimativa)** = ordem de grandeza, não medida. "D610" = Dell Latitude D610, Pentium M 735 Dothan, 1,7 GHz,
1 núcleo, 2 MB de L2, 90 nm, TDP 21 W, `linux/386`, Go 1.27.2.

Este documento é adversarial por encomenda. Onde a ideia vira engenharia, ele diz **LOAD-BEARING** e mostra a lei, o
número e o `file:line` onde ela encosta no código. Onde é boa de pensar e não de implementar, diz **METÁFORA ÚTIL**
e mostra o que a metáfora esconde. Onde é falsa, diz **VIAGEM** — e, quando for o caso, "não compila, é motivação".

---

## 0. Placar

| # | Peça que o dono levantou | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | Landauer: apagar 1 bit custa `kT·ln2` | **LOAD-BEARING** como limite inferior · **VIAGEM** como alavanca | A lei é verdadeira e medida (Bérut 2012), mas o D610 opera **4,3·10¹²** acima dela por instrução. Um limite 12 ordens de grandeza folgado não ordena duas implementações. |
| 2 | "Menos bits apagados = menos energia" como alvo de projeto | **VIAGEM** | É a diferença entre `10⁻¹⁴ J` e `10⁻¹ J`; nenhum instrumento no D610 (nem proxy) enxerga a primeira parcela. O alvo correto é instrução e byte movido. |
| 3 | Computação reversível (Bennett 1973) | **VIAGEM** no jazz | Exige clock adiabático e recuperação de carga; não há como expressar isso em Go nem no Pentium M. E o ganho seria sobre uma parcela que já é 10⁻¹² do orçamento. |
| 4 | "Determinístico ⇒ reversível ⇒ barato" | **FALSO** (correção) | Determinismo não é reversibilidade lógica. O que o determinismo compra é **reuso** (cache hit), e é aí que mora o joule — não em kT. |
| 5 | Demônio de Maxwell | **METÁFORA ÚTIL**, errada de um jeito instrutivo | O demônio *extrai* trabalho de informação; o jazz *gasta* trabalho para produzir ordem. É uma geladeira, não um motor. A resolução (Bennett/Landauer) é a conta de apagar a memória — e essa conta, no jazz, tem nome: forja. |
| 6 | "O demônio de Maxwell mais barato" | **VIAGEM** como física · **LOAD-BEARING** como economia | O número certo não é uma eficiência termodinâmica; é `N* = tokens_da_forja / tokens_por_tarefa_online`. Esse número decide se a arquitetura inteira se paga. |
| 7 | Negentropia (Schrödinger 1944) | **METÁFORA ÚTIL** | Schrödinger escreveu "entropia negativa" e depois corrigiu: o que se importa é **energia livre**, não entropia negativa. Vale como figura de linguagem; não gera uma linha de código. |
| 8 | Estruturas dissipativas (Prigogine) | **METÁFORA ÚTIL** · **VIAGEM** se promovida a teorema | O teorema da produção mínima de entropia vale **perto do equilíbrio, regime linear** — exatamente o regime que as estruturas dissipativas *quebram*. Citá-lo para "estrutura que minimiza dissipação" é inverter o teorema. |
| 9 | Princípio de energia livre (Friston) | **METÁFORA** · **LOAD-BEARING** só no pedaço `−log p` | `−log p` é log-loss, que é `calib.go` e MDL. O FEP propriamente dito (inferência ativa, Markov blanket) não descreve um classificador discriminativo como o NB do jazz. |
| 10 | Termodinâmica da predição (Still 2012) | **LOAD-BEARING** como forma · **VIAGEM** como escala | O teorema é real: informação não-preditiva encarece a memória. É a justificativa teórica de descartar bits — a `10⁻²¹ J` o bit. |
| 11 | LLM vs CPU determinística: custo real | **LOAD-BEARING** — e é o único lugar onde a energia tem dentes | 32,3k tokens vs 0 tokens na mesma tarefa medida. Fator `10⁴–10⁵` em joules. Mas a causa é arquitetura (FLOPs, bytes, modelo), não termodinâmica. |
| 12 | Métrica "tarefas verificadas por token" | **LOAD-BEARING**, mensurável hoje | `obs.go:925` já soma `tokens_in/out`; `evaloffline.go:91` já conta sucesso verificado e `false_success`. Falta dividir. |
| 13 | Métrica "por joule / watt-hora" | **LOAD-BEARING grosseira** | Discrimina online × offline (`10⁴`), não duas variantes offline (`~1 J` cada, dominadas pelo *startup*). Como número fino no D610, é ruído. |
| 14 | Métrica "bits apagados" | **VIAGEM** | Ver #2. Reportaria `10⁻¹⁴ J` para a tabela NB inteira; o piso de medição é `10⁻³ J`. |
| 15 | "Entropia do artefato" como medida de ordem | **VIAGEM** (inverte o sinal) | Um `.gz` tem ~8 bits/byte de entropia e é **mais** ordenado que o original. Densidade de entropia não é ordem; comprimento de descrição é. |
| 16 | Princípio "determinismo/compressão/reuso ⇒ menos energia" | **LOAD-BEARING com derivação corrigida** | Verdadeiro por instruções/IPC/reuso de token, não por kT. A conclusão sobrevive; a prova que o dono dá, não. |
| 17 | A forja como investimento amortizado | **LOAD-BEARING** — é a melhor ideia do lote | `E_total = E_forge + N·E_offline` vs `N·E_llm`; ponto de equilíbrio `N*`. Mede-se em tokens, hoje, sem hardware de energia. |

**Resumo de uma frase:** a termodinâmica explica *por que* a computação custa algo, mas a 12 ordens de grandeza de
folga ela não decide *o que* fazer. O que decide é a escada de custo real — token ≫ instrução ≫ byte ≫ bit — e a
única peça da metáfora que vira engenharia é a **amortização da forja**.

---

## 1. A tese, decomposta

O dono está dizendo algo como:

> "O agente é uma bomba de negentropia: impõe ordem local (tarefa resolvida e verificada) exportando desordem
> (calor/custo), como a vida. Offline + comprimido + determinístico é o demônio de Maxwell mais barato: máxima
> ordem por joule/token."

Isso não é testável nesse nível. Decomposto em seis afirmações que **são**:

- **T1 (física).** Produzir uma tarefa verificada reduz a entropia de algum sistema, exportando entropia para o
  ambiente.
- **T2 (limite).** O custo energético mínimo de apagar informação é `kT·ln2` por bit, logo menos bits apagados
  significa menos energia.
- **T3 (mecanismo).** O caminho offline apaga menos bits que o LLM e por isso é o mais barato por tarefa.
- **T4 (alavanca).** Existe uma métrica competitiva de "ordem por joule/token" que decide arquitetura.
- **T5 (princípio).** Favorecer determinismo, compressão e reuso reduz energia — e isso é um princípio de projeto.
- **T6 (economia).** O ganho do offline é uma dívida paga em tempo de compilação e amortizada em `N` tarefas.

A tese "mestra" é a conjunção. O resto do documento testa cada uma. O resultado, adiantado:

| Afirmação | Status | Por quê |
|---|---|---|
| T1 | verdadeira e trivial | A 2ª lei não é violada por software. Nenhuma decisão sai daí. |
| T2 | verdadeira e irrelevante | O limite é 12 ordens abaixo do regime de operação. |
| T3 | **falsa** | O LLM não é limitado por Landauer; é limitado por FLOPs e bytes. O offline ganha por outra razão. |
| T4 | verdadeira | Mas a métrica é de *computação*, não de *termodinâmica*. |
| T5 | verdadeira com prova trocada | Instruções e reuso, não bits apagados. |
| T6 | verdadeira e é o núcleo | `N*` é o único número que a metáfora produz. |

### 1.1 O que "negentropia" pode significar — e qual das três é mensurável

A palavra cobre três quantidades diferentes, e a metáfora funciona porque as três andam juntas. Elas não são a mesma
coisa, e só duas são medíveis:

| Quantidade | Unidade | O que mede | No jazz | Mensurável? |
|---|---|---|---|---|
| **(a) Entropia termodinâmica** | J/K | desordem física | sempre **aumenta** (2ª lei); o CPU esquenta | sim, com medidor; não no D610 |
| **(b) Informação de Shannon / comprimento de descrição** | bits | tamanho do modelo | a biblioteca de assets tem `L` bits | sim: `du -sb`, `len()` |
| **(c) "Ordem" como sucesso** | booleano | a tarefa foi resolvida **e** verificada | `Success && !FalseSuccess` | sim: `evaloffline.go:78-79` |

A confusão perigosa é entre (a) e (b). (a) é uma grandeza física com sinal garantido; (b) é uma grandeza
combinatória que pode cair sem que (a) caia. Quando o dono diz "o agente impõe ordem local", ele está falando de
**(c)** — um booleano — e pagando por ela em **(a)**. A ponte entre as duas é o custo de computar, e essa ponte não
passa por Landauer: passa por instruções e bytes. É o que a §3.1 mostra com número.

O resto deste documento usa uma regra fixa: **quando uma afirmação só faz sentido em (a), ela não vira código.**

---

## 2. O que foi lido e o que foi medido

Código lido para esta análise: `nlu.go` (tabela NB, `predict`), `brain.go` (`brRun`, `brExecute`, `brVerify`,
`brRecover`, nível 0), `methods.go` (`mhMethod`, `brVerify`), `caps.go` (`cbMinimalIntervention`), `calib.go`,
`forge_gate.go`, `signals.go`, `evaloffline.go`, `evaloffline_rss_unix.go`, `obs.go` (`obSummary`), `types.go`
(`Usage`), `plancache.go`, `budget.go`.

Medições já registradas que uso como âncora (todas de `results/dell-0.9-progress.md`, D610 ocioso, load < 0,7):

| Medida | Número | Onde |
|---|---|---|
| `jazz -p --offline "abre o README.md"` | **120–139 ms · 0 requisições · 0 tokens · RSS 27 MB** | idem |
| a mesma tarefa **com** modelo (0.8.1) | **14,3–21,5 s · 2 requisições · 32,3k tokens** | idem |
| `jazz -p --offline "git status"` | 133–203 ms | idem |
| `jazz eval offline` | **39/39**, falso sucesso 0, 34/39 sem modelo, p50 14 ms, p95 362 ms, RSS 59 MB | idem |
| CPU de "abre o README.md" antes/depois de `nuTrain` | 200 ms → **90 ms**; 786 mil `math.Log` por partida | idem |
| Tabela NB | 50 classes × 16 384 baldes × `int16` = **1 638 400 B** = **78,1% da L2** | `nlu.go:1330,1395` |
| Biblioteca de métodos | **49 arquivos, 45 099 B** | `methods/*.json` |
| Corpus de avaliação | **75 tarefas** | `evals/offline/tasks.json` |

Duas descobertas de leitura que mudam o diagnóstico e precisam ser ditas de saída:

1. **`signals.go` é código morto.** `sigAppend`/`sigRead`/`sigCount` não são chamados por nenhum caminho de
   produção — só por `signals_test.go`. O ledger que deveria registrar onde o cérebro errou (`miss`,
   `verifier_missing`, `unknown_intent`, `new_method`) existe, está correto e **não está ligado**.
2. **`forge_gate.go` é código morto.** `Gate` e `Split` não são chamados fora de `forge_gate_test.go`; não existe
   `jazz forge` em `main.go`. O portão held-out existe e é bom; o laço que ele deveria fechar não existe.

Isso importa porque a única peça da metáfora que vira engenharia (§4.5, `N*`) é exatamente o laço
**sinal → forja → portão → asset**, e ele está desligado. A metáfora não falha por ser física demais; falha por
estar a dois cabos desconectados.

---

## 3. Conceito a conceito

### 3.1 Landauer: a lei é verdadeira, a alavanca não existe

**Veredito: LOAD-BEARING como limite inferior; VIAGEM como critério de projeto.**

#### A lei

Landauer (1961) argumenta que apagar um bit — uma operação **logicamente irreversível**, que leva dois estados a
um — dissipa pelo menos

```
Q ≥ kT·ln2 = 1,380649·10⁻²³ × 300 × 0,6931 ≈ 2,87·10⁻²¹ J   (a 300 K)
```

Fonte: Landauer, *Irreversibility and heat generation in the computing process*, IBM J. Res. Dev. 5(3):183–191,
1961, [doi:10.1147/rd.53.0183](https://doi.org/10.1147/rd.53.0183).

A verificação experimental veio 51 anos depois: Bérut, Arakelyan, Petrosyan, Ciliberto, Dillenschneider & Lutz,
*Experimental verification of Landauer's principle linking information and thermodynamics*, Nature 483:187–189,
2012, [doi:10.1038/nature10872](https://doi.org/10.1038/nature10872). Uma partícula coloidal numa armadilha óptica
de poço duplo, ciclo de apagamento cada vez mais lento: o calor médio dissipado **satura no limite de Landauer** no
limite de ciclos longos. Repetido com precisão maior em Jun, Hutzler & Bechhoefer, PRL 113:190601, 2014,
[doi:10.1103/PhysRevLett.113.190601](https://doi.org/10.1103/PhysRevLett.113.190601).

Honestidade sobre o status: a lei **não** é derivável só da segunda lei sem hipóteses auxiliares; Norton, *Eaters
of the lotus: Landauer's principle and the return of Maxwell's demon*, Stud. Hist. Phil. Mod. Phys. 36:375–411,
2005, [doi:10.1016/j.shpsb.2004.12.002](https://doi.org/10.1016/j.shpsb.2004.12.002), monta o contraexemplo clássico,
e Bennett responde em *Notes on Landauer's principle, reversible computation, and Maxwell's Demon*, ibid. 34:501–510,
2003, [doi:10.1016/S1355-2198(03)00039-X](https://doi.org/10.1016/S1355-2198(03)00039-X). Para o que segue, isso é
irrelevante: mesmo tomando a lei na forma mais forte, ela é um **limite inferior folgado**.

#### A escada de custo real

A pergunta que decide engenharia não é "qual é o limite?", é "onde estou em relação a ele?". A tabela abaixo põe
tudo na mesma unidade — múltiplos de `kT` a 300 K — com a fonte de cada linha.

| Operação | Energia | em `kT` (300 K) | Fonte |
|---|---|---|---|
| apagar 1 bit (limite de Landauer) | 2,87·10⁻²¹ J | **1** | Landauer 1961; medido em Bérut 2012 |
| somar dois `int32` em 45 nm | 10⁻¹³ J (0,1 pJ) | 3,5·10⁷ | Horowitz, ISSCC 2014 |
| ler 32 bits da SRAM (8 KB) | 5·10⁻¹² J | 1,7·10⁹ | idem |
| ler 32 bits da DRAM | 6,4·10⁻¹⁰ J | 2,2·10¹¹ | idem |
| **1 instrução no D610** (21 W / 1,7 GHz) | 1,2·10⁻⁸ J | **4,3·10¹²** | conta |
| **1 token de LLM** (LLaMA-65B, 8×V100) | ~3 J | ~10²¹ | Samsi 2023 |
| 1 query BLOOM-176B | ~1,4·10⁴ J | ~5·10²⁴ | Luccioni 2024 |

Fontes: Horowitz, *Computing's energy problem (and what we can do about it)*, ISSCC 2014,
[doi:10.1109/ISSCC.2014.6757323](https://doi.org/10.1109/ISSCC.2014.6757323) — 0,1 pJ para `int32` add, 5 pJ para
leitura de 32 bits de SRAM, 640 pJ para leitura de 32 bits de DRAM. Samsi et al., *From Words to Watts*,
IEEE HPEC 2023, [arXiv:2310.03003](https://arxiv.org/abs/2310.03003) — "3 to 4 J per token" para LLaMA-65B
fragmentado. Luccioni, Jernite & Strubell, *Power Hungry Processing*, FAccT 2024,
[doi:10.1145/3630106.3658542](https://doi.org/10.1145/3630106.3658542),
[arXiv:2311.16863](https://arxiv.org/abs/2311.16863) — 0,004 kWh por query no BLOOM-176B, geração de texto em média
0,042 kWh por 1000 inferências.

A conta do D610, explícita: 21 W de TDP a 1,7 GHz dão `21 / 1,7·10⁹ = 1,24·10⁻⁸ J` por ciclo; com IPC ≈ 1, é a
energia por instrução aposentada. Dividindo por `kT·ln2`: `1,24·10⁻⁸ / 2,87·10⁻²¹ ≈ 4,3·10¹²`.

**É a única linha que importa neste documento.** O D610 está **quatro trilhões de vezes** acima do limite de
Landauer por instrução. Qualquer decisão de implementação que o jazz possa tomar muda o número de instruções por
tarefa (fator 2, 10, no máximo 100) — e não muda nada que se aproxime da folga de `10¹²`. Otimizar "bits apagados"
é escolher o único termo da soma que é invisível.

#### O que isso faz com T2 e T3

- **T2** ("menos bits apagados = menos energia") é verdadeira e não decidível: o termo de Landauer é `10⁻¹⁴ J` para
  a tabela NB inteira (`1,28·10⁷ bits × 2,87·10⁻²¹`), contra `~1 J` de CPU por tarefa. Um fator `10¹⁴` de
  irrelevância.
- **T3** ("o offline apaga menos bits que o LLM, logo é mais barato") é **falsa como mecanismo**. O LLM não é caro
  porque apaga bits; é caro porque executa `~2·N_params` FLOPs por token e move o modelo inteiro pela memória a cada
  passo. A energia de um token de 65B é `~10²¹ kT`, ou seja, `~10⁹` vezes a energia de ler o mesmo dado da DRAM.
  Nenhuma contabilidade de bits apagados reproduz esse número; a contabilidade de FLOPs e bytes, sim.

O que **é** load-bearing nessa vizinhança: a lei de Landauer diz que a computação tem um preço físico mínimo, e
portanto que "grátis" não existe. Ela é a razão pela qual a pergunta do dono é legítima. Ela não é a razão pela
qual uma resposta é melhor que outra.

---

### 3.2 Computação reversível (Bennett): certa, e inaplicável aqui

**Veredito: VIAGEM no jazz.**

Bennett, *Logical reversibility of computation*, IBM J. Res. Dev. 17(6):525–532, 1973,
[doi:10.1147/rd.176.0525](https://doi.org/10.1147/rd.176.0525), mostra que uma máquina de Turing logicamente
reversível pode computar qualquer função computável, com custo de espaço, mas **sem apagar informação** — e
portanto, em princípio, sem o custo de `kT·ln2` por passo. A revisão canônica é Bennett, *The thermodynamics of
computation — a review*, Int. J. Theor. Phys. 21:905–940, 1982,
[doi:10.1007/BF02084158](https://doi.org/10.1007/BF02084158).

Por que não compila no jazz, em três camadas:

1. **Não há como expressar isso em Go.** Reversibilidade lógica exige que cada operação tenha inversa e que o
   garbage seja mantido (não apagado). O compilador Go, o runtime, o GC, a pilha, o kernel — todos apagam estado
   sem controle do programa. Um `for` com contador já apaga bits a cada incremento.
2. **Não há hardware para isso no D610.** Computação adiabática de verdade exige recuperação de carga em CMOS
   (relógio ressonante, `~100 MHz`, fontes de tensão controladas). O Pentium M não tem nada disso. O ganho teórico
   é sobre a energia de comutação, que na prática é `10⁷ kT` por porta — e mesmo os protótipos adiabáticos ficam
   ordens acima do limite.
3. **O ganho seria sobre a parcela errada.** Mesmo que fosse possível, você economizaria `kT` por bit num sistema
   que gasta `4·10¹² kT` por instrução. É aspirar o chão de uma fábrica.

O que sobra de aproveitável — e não é a reversibilidade, é a **não-destruição de estado útil**: não recalcular o que
já se sabe. Isso aparece no jazz como *cache* e *reuso*, e é a §5. Reversibilidade lógica é o nome físico
disso; o nome de engenharia é "não jogue fora o resultado".

**Correção que precisa ser registrada:** determinismo **não** implica reversibilidade. Um programa determinístico
pode sobrescrever memória à vontade (e o jazz sobrescreve). O que o determinismo compra é **reprodutibilidade**, e
reprodutibilidade compra **reuso** — cache hit, plano cacheado, asset que se aplica de novo. O joule economizado
pelo determinismo não está no bit não apagado; está na instrução não executada e no token não comprado. Essa
distinção é o coração do §5.

Para a fronteira moderna (o que é custo de apagar, de medir, de computar, com flutuações), a revisão é Wolpert,
*The stochastic thermodynamics of computation*, J. Phys. A 52:193001, 2019,
[doi:10.1088/1751-8121/ab0850](https://doi.org/10.1088/1751-8121/ab0850). Ela confirma a leitura acima: o custo
depende de **quantas operações irreversíveis** o dispositivo faz, e não de uma propriedade global do algoritmo.

---

### 3.3 O demônio de Maxwell: a metáfora está invertida

**Veredito: METÁFORA ÚTIL; "o demônio mais barato" é VIAGEM (erro de categoria).**

#### O que o demônio é

Szilard (1929) descreve um motor de uma partícula: um ser inteligente mede de que lado a molécula está, insere uma
divisória e extrai `kT·ln2` de trabalho por bit de informação obtido — aparentemente de graça, violando a segunda
lei. Fonte: Szilard, *Über die Entropieverminderung in einem thermodynamischen System bei Eingriffen intelligenter
Wesen*, Z. Phys. 53:840–856, 1929, [doi:10.1007/BF01341281](https://doi.org/10.1007/BF01341281).

A resolução (Bennett 1982; Landauer 1961) é que o demônio **precisa de memória**, e a memória precisa ser apagada
para o ciclo recomeçar. O apagamento custa `kT·ln2` por bit. O balanço fecha: nenhum trabalho líquido é extraído.
A generalização quantitativa é Sagawa & Ueda, *Minimal energy cost for thermodynamic information processing*,
PRL 100:080403, 2008, [doi:10.1103/PhysRevLett.100.080403](https://doi.org/10.1103/PhysRevLett.100.080403): com
medição e feedback, o trabalho extraível é limitado por `kT·I`, onde `I` é a informação mútua obtida. A revisão
moderna é Parrondo, Horowitz & Sagawa, *Thermodynamics of information*, Nat. Phys. 11:131–139, 2015,
[doi:10.1038/nphys3230](https://doi.org/10.1038/nphys3230).

#### Por que o jazz não é um demônio

Um demônio tem figura de mérito **trabalho extraído por bit obtido**. O jazz tem figura de mérito **ordem produzida
por joule gasto**. Os sinais são opostos: o demônio converte informação em trabalho; o jazz converte trabalho em
informação (um artefato verificado). Chamar o jazz de demônio é chamar uma geladeira de motor térmico — os dois
usam o mesmo ciclo, com o fluxo invertido.

A analogia correta é **geladeira**: retira entropia de uma região pequena (o estado da tarefa) ao preço de entropia
maior fora (o CPU, a tomada, o datacenter do modelo). Isso é verdadeiro, é a segunda lei, e não gera uma linha de
código. O COP de uma geladeira é `Q_frio / W`; o "COP" do jazz seria `bits_de_ordem / J`, que tem unidades
incompatíveis sem uma taxa. Você é obrigado a escolher: **bits por joule** ou **tarefas por joule**. As duas são
definíveis; nenhuma é uma eficiência termodinâmica.

#### O que sobra — e é muito

A resolução de Bennett tem uma consequência que **é** engenharia: **o custo do demônio é o custo de apagar a
memória, e ele é pago adiantado.** No jazz, a memória do demônio é a biblioteca de assets, e a conta de apagá-la é
a **forja**: o trabalho de LLM em tempo de compilação que destila sinais em métodos. Essa é a §4.5, e é o único
lugar do documento em que a metáfora produz um número que decide arquitetura.

---

### 3.4 Schrödinger: negentropia é figura de linguagem, e ele sabia

**Veredito: METÁFORA ÚTIL. Não compila; é motivação.**

Em *What is Life?* (Cambridge University Press, 1944), cap. 6 ("Order, Disorder and Entropy"), Schrödinger escreve
que o organismo "se alimenta de entropia negativa" — suga ordem do ambiente para se manter ordenado. Ele mesmo
registra que a expressão é desajeitada, e em edições posteriores nota que o termo correto seria **energia livre**.
A formalização da ideia como "princípio de negentropia da informação" é Brillouin, *The negentropy principle of
information*, J. Appl. Phys. 24(9):1152–1163, 1953,
[doi:10.1063/1.1721463](https://doi.org/10.1063/1.1721463) — e é lá que a conta fica explícita: informação adquirida
custa entropia.

A correção moderna importa: um organismo não importa "entropia negativa" (que não é uma substância); importa
**energia livre** — fótons de baixa entropia do Sol, ligações químicas — e exporta calor e resíduos de alta
entropia. A produção total de entropia do sistema + ambiente é positiva. A vida é uma máquina de degradar energia
livre mantendo estrutura local; é isso que a torna compatível com a segunda lei, não uma violação dela.

Para o jazz, o mapeamento honesto:

| Schrödinger | jazz |
|---|---|
| energia livre importada | eletricidade da tomada; tokens comprados |
| estrutura local mantida | a biblioteca de assets + a tarefa verificada |
| entropia exportada | calor do CPU, custo do datacenter, CO₂ |
| "negentropia" | nada de novo: é a mesma conta, com outro nome |

A consequência de engenharia é **zero** e a consequência de contabilidade é **total**: se a ordem local é paga com
entropia exportada, então "offline é barato" só pode significar "alguém pagou antes". Esse alguém é a forja. É a
mesma conclusão da §3.3, por outro caminho — o que é um bom sinal de que a conclusão é robusta e a metáfora é
decorativa.

---

### 3.5 Prigogine: o teorema citado ao contrário

**Veredito: METÁFORA ÚTIL com referente real; VIAGEM se promovida a teorema.**

Prigogine (Nobel de Química 1977, *Time, Structure and Fluctuations*,
[nobelprize.org](https://www.nobelprize.org/prizes/chemistry/1977/prigogine/lecture/); e Glansdorff & Prigogine,
*Thermodynamic Theory of Structure, Stability and Fluctuations*, Wiley, 1971) mostra que sistemas abertos, longe
do equilíbrio, podem formar **estruturas dissipativas**: padrões macroscópicos estáveis (Bénard, relógios químicos)
mantidos por fluxo contínuo de energia e matéria, com produção de entropia positiva e irreversível.

O erro comum — e o dono está a um passo dele — é citar junto o **teorema da produção mínima de entropia**. Esse
teorema vale no regime **linear** da termodinâmica irreversível (fluxos lineares nas forças, relações de reciprocidade
de Onsager, forças externas fixas): ali o estado estacionário minimiza a produção de entropia. As estruturas
dissipativas existem justamente **porque esse regime quebrou** — são fenômenos não-lineares, longe do equilíbrio,
onde o teorema não se aplica. Usar Prigogine para dizer "a estrutura ótima minimiza a dissipação" é citar o teorema
que a estrutura viola.

O que o jazz tem de verdade, e é interessante: **um laço de realimentação com referente no código** —

```
   erro (miss / verifier_missing / unknown_intent)     ← entropia que entra
        ↓ signals.go:26 (Signal), signals.go:52 (sigAppend)
   forja em tempo de compilação (Fable, caro em tokens)
        ↓ forge_gate.go:143 (Gate: held-in/held-out, false_success bloqueante)
   asset durável (methods/*.json)
        ↓ methods.go:60 (mhMethod), brain.go:259 (brExecute)
   menos erro na próxima tarefa
```

É um ciclo autocatalítico no sentido fraco: o produto (asset) reduz a causa que gera o produto (erro). Isso é
*análogo* a uma estrutura dissipativa. Mas não há dinâmica não-linear, não há bifurcação, não há análise de
estabilidade, e — fato de implementação — **o laço está aberto**: `sigAppend` não é chamado em produção e `Gate` não
é chamado fora de teste (§2). A metáfora descreve um sistema que o código ainda não é.

Veredito operacional: a metáfora justifica **fechar o laço** (proposta P3). Não justifica nenhuma matemática de
Prigogine. Se alguém propuser "medir a produção de entropia do laço", a resposta é: não compila, é motivação.

---

### 3.6 Friston: o pedaço que carrega peso é o log-loss

**Veredito: METÁFORA como princípio; LOAD-BEARING apenas na identidade `surpresa = −log p`.**

Friston, *The free-energy principle: a unified brain theory?*, Nat. Rev. Neurosci. 11:127–138, 2010,
[doi:10.1038/nrn2787](https://doi.org/10.1038/nrn2787), propõe que sistemas biológicos minimizam **energia livre
variacional** `F = E_q[−log p(o,s)] − H[q]`, que é um limite superior para a surpresa `−log p(o)` e se decompõe em
`surpresa + KL(q‖p)`. Minimizar `F` aproxima inferência bayesiana e, com ação, "inferência ativa".

O que é matemática sólida: `F` é o negativo da evidência inferior (ELBO), e a desigualdade `−log p(o) ≤ F` é um
teorema. O que é tese contestada: que cérebros (ou agentes) *implementam* isso. A crítica padrão é que o FEP é
difícil de falsificar (Colombo & Wright, *First principles in the life sciences*, Synthese 2021,
[doi:10.1007/s11229-019-02424-1](https://doi.org/10.1007/s11229-019-02424-1)).

Para o jazz, a tradução é curta e vale a pena porque conecta com a tese de calibração já provada em
[unifying-math-offline.md](unifying-math-offline.md) §3.2:

- "surpresa" de uma tarefa = `−log P(sucesso | pedido)`. Isso é **log-loss**, e log-loss é exatamente o que
  `calib.go:198` (`BrierScore`) e `calib.go:164` (`ECE`) medem, com o par `(p̂, acerto)`.
- minimizar surpresa = calibrar + acertar. Não é um princípio novo; é a regra de Chow com custo, que já está no
  documento anterior.
- MDL e energia livre se encontram aqui: `−log p(dados | modelo) + L(modelo)` é comprimento de descrição **e** é
  energia livre variacional com `q` degenerada. A coincidência é real e já foi explorada em
  [unifying-math-offline.md](unifying-math-offline.md) §5.

O que **não** compila: inferência ativa, Markov blankets, "o agente minimiza a energia livre do seu modelo do mundo".
O NB do jazz (`nlu.go:1395`) é um classificador **discriminativo** com tabela de contagens; não há modelo generativo
de observações, não há `q`, não há ação que muda a observação. Aplicar FEP aqui é vestir a matemática de outra
arquitetura.

Veredito: use Friston para dizer "acertar com confiança honesta é minimizar surpresa", e pare aí. A parte que vira
código já está em `calib.go` — e está desligada (ver [unifying-math-offline.md](unifying-math-offline.md) §2).

---

### 3.7 Still et al.: o único teorema que liga compressão a dissipação

**Veredito: LOAD-BEARING como forma; VIAGEM como escala no D610.**

Still, Sivak, Bell & Crooks, *Thermodynamics of prediction*, PRL 109:120604, 2012,
[doi:10.1103/PhysRevLett.109.120604](https://doi.org/10.1103/PhysRevLett.109.120604), provam algo que nenhuma das
outras peças prova: um sistema que mantém memória do ambiente dissipa, no mínimo, uma quantidade proporcional à
**informação não-preditiva** que carrega. Em palavras: memória que não ajuda a prever o futuro é "nostalgia", e
nostalgia custa energia. O resultado refina Landauer:

```
Q ≥ kT·ln2·I_apagada + I_não-preditiva
```

Isso é o parente físico mais próximo da intuição do dono ("comprimir = economizar"). E ele diz a coisa certa:
**descarte os bits que não predizem**. A tradução para o jazz é direta e já existe em parte:

- a tabela NB tem `1 638 400 B` (`nlu.go:1395`) para 50 intenções e 644 exemplos. Boa parte desses baldes está
  vazia ou carrega contagem que não muda o argmax. São bits não-preditivos.
- a biblioteca de métodos tem `45 099 B` e é o que generaliza. A razão `1,6 MB / 45 KB ≈ 36` diz onde está o peso
  morto.

Mas — e é aqui que a honestidade morde — a `kT` por bit, esses `1,28·10⁷` bits custam `3,7·10⁻¹⁴ J`. O ganho
termodinâmico real de apagar a tabela inteira é **14 ordens de grandeza abaixo** do custo de uma tarefa. O teorema
é verdadeiro e a escala é irrelevante. O que **é** relevante é o corolário de engenharia, que não precisa de `kT`:
**bits não-preditivos custam tempo de CPU e cache**. A tabela de 1,6 MB ocupa 78% da L2 do D610 e disputa espaço
com o parser, o binário e a pilha; cada n-grama toca 50 linhas de cache espalhadas por 1,6 MB
(`nlu.go:1444`). O custo aparece como **IPC**, não como entropia — e IPC vira joule porque `E ≈ P·t` e
`t = instruções/IPC`.

Ou seja: Still et al. dão a **forma** correta do princípio ("descarte o que não prediz"), e o D610 dá a **escala**
correta do mecanismo ("o que não prediz ocupa cache e tempo"). As duas coisas apontam para o mesmo lugar. É o
melhor caso do documento: metáfora e engenharia concordam, por razões diferentes.

---

### 3.8 LLM vs CPU determinística: aqui a energia tem dentes

**Veredito: LOAD-BEARING — mas é arquitetura, não termodinâmica.**

A comparação medida no D610, mesma tarefa ("abre o README.md"):

| | offline (0.9) | com modelo (0.8.1) | razão |
|---|---|---|---|
| tempo | 120–139 ms | 14,3–21,5 s | ~120× |
| tokens | **0** | **32,3k** | ∞ |
| requisições | 0 | 2 | — |
| energia (conta) | 0,26–2,7 J | 32–97 kJ | **1,2·10⁴ – 3,7·10⁵** |

A conta da energia offline: `0,13 s × 2 W = 0,26 J` (se o consumo marginal é o do dono, ~2 W) até
`0,13 s × 21 W = 2,7 J` (TDP cheio). A conta online: `32,3k tokens × 1 J = 32 kJ` (conservador) até
`× 3 J = 97 kJ` (Samsi). O número conservador já é `10⁴`; o agressivo, `3,7·10⁵`.

Traduzindo para uma imagem verificável: **97 kJ é o D610 rodando a plena carga por 77 minutos** (`97 000 / 21 =
4 619 s`). Uma pergunta sobre um README custa mais que uma hora de laptop. Com o número conservador, 26 minutos.

Por que isso acontece — e a explicação não é termodinâmica:

1. **FLOPs.** Um modelo de 65B gasta `~2·6,5·10¹⁰ = 1,3·10¹¹` FLOPs por token. O D610, a 1,7 GHz e ~2 FLOPs/ciclo,
   faria `~3,4·10⁹` FLOP/s. Um token do modelo = **38 segundos** de D610. (E o D610 não tem SSE3: o único caminho
   vetorial é SSE2, 2 `float64` por instrução, quando o compilador consegue vetorizar — o que Go faz pouco.)
2. **Bytes.** Cada token relê os pesos que couberem no cache; o modelo não cabe em 2 GB de RAM nem em 2 MB de L2.
   A energia de mover bytes domina a de multiplicar (Horowitz: DRAM 640 pJ vs `int32` add 0,1 pJ, fator 6 400).
   Isso é o *roofline*: Williams, Waterman & Patterson, *Roofline: an insightful visual performance model for
   multicore architectures*, CACM 52(4):65–76, 2009,
   [doi:10.1145/1498765.1498785](https://doi.org/10.1145/1498765.1498785).
3. **Paralelismo desperdiçado.** O modelo roda em 8 GPUs a 250 W cada = 2 kW de TDP para produzir 200 tokens.
   O D610 produz a mesma resposta com 1 núcleo e 2 GB.

Nada disso é Landauer. É a razão pela qual a intuição do dono **funciona na prática**: ele está comparando
arquiteturas separadas por `10⁴`, e a termodinâmica é o pano de fundo decorativo. Se ele atribuir o ganho a
"menos bits apagados", vai otimizar a variável errada na próxima decisão — e a próxima decisão (qual variante
offline escolher) é decidida por instruções e cache, não por bits.

---

## 4. A pergunta-chave: isso vira métrica?

**Resposta curta: sim, três métricas reais — e nenhuma delas é entropia.**

### 4.1 As três candidatas, e o que cada uma discrimina

| Métrica | Unidade | Discrimina o quê | Vira código? |
|---|---|---|---|
| `VPK` — tarefas verificadas por 10³ tokens | tarefas/ktok | online × offline, e variantes de prompt/reuso | **sim, hoje** |
| `VPS` — tarefas verificadas por segundo de CPU | tarefas/s | variantes offline entre si | **sim, hoje** |
| `VPJ` — tarefas verificadas por joule | tarefas/J | online × offline (grosseiro); offline × offline (ruído) | sim, com proxy |
| `BPT` — bits de descrição por tarefa coberta | bits/tarefa | qualidade da biblioteca de assets | sim (`du`) |
| `N*` — reusos para pagar a forja | adimensional | se a arquitetura se paga | sim, com contabilidade de tokens |
| `kT·bits` — custo de Landauer | J | nada | **não** |

A distinção que salva o projeto: `VPK` e `VPS` são **métricas de computação**, não de termodinâmica. Elas são
legítimas, competitivas e auditáveis. Chamá-las de "negentropia" não as torna melhores; chamá-las assim convida a
incluir `kT·bits` na conta, que é o termo que arruína tudo.

### 4.2 `VPK`: tarefas verificadas por 10³ tokens

**Já é computável com o que existe.** A infraestrutura está toda lá:

- tokens: `types.go:43` (`Usage.In/Out/CacheRead/CacheWrite`), somados por sessão em `obs.go:925` (`obSummary`),
  agregados em `obs.go:1016`, escritos por `--metrics-out` (`main.go:872`).
- tarefas verificadas: `evaloffline.go:91` (`eoSummary`) tem `Success`, `FalseSuccess`, `NoLLM`, `NoLLMRate`; o
  `eoRow` (`evaloffline.go:79`) tem `Success` e `FalseSuccess` por tarefa (`evaloffline.go:85-86`).
- teto de custo: `budget.go` já limita passos por turno (`bgLean` com `hardSteps` em `budget.go:225`). Isso é um
  **teto** de gasto, não uma medida de eficiência — mas é o lugar onde um `VPK` por turno se pendura.
- o que falta é uma divisão.

A definição que eu adotaria, e que é defensável:

```
VPK = (tarefas com Success && !FalseSuccess) / ((tokens_in + tokens_out) / 1000)
```

com duas salvaguardas: (a) `false_success > 0` **invalida** o número do run inteiro, não só o numerador — é a mesma
regra de `forge_gate.go:155`, e é o que impede a métrica de ser gameável respondendo rápido e errado; (b) a
cobertura (`NoLLMRate`) vai junto, porque `VPK` sem cobertura é uma razão entre dois números que podem ambos cair.

Números de partida (medido + estimativa): no run de 39 tarefas, 34 foram resolvidas sem modelo e 5 precisaram dele.
**Se** as 5 custaram os mesmos 32,3k tokens da tarefa de README (estimativa — não foram medidas), o run inteiro
custou `~1,6·10⁵` tokens para 39 tarefas verificadas → `VPK ≈ 0,24`. Com o caminho offline completo (0 tokens) a
razão fica **indefinida** (divisão por zero), e é por isso que o número útil não é o infinito: é **quantas tarefas
ainda precisam do modelo** (5/39 = 12,8%), que é o que a forja tem de atacar.

Um detalhe que impede a métrica de virar vaidade: **`VPK` não é comparável entre suítes**. 39 tarefas de fixture não
são 39 tarefas reais. Ela é comparável entre *execuções da mesma suíte* e entre *variantes da mesma tarefa* — que é
exatamente o uso que o `forge_gate.go` já faz com accuracy e coverage.

### 4.3 `VPS` e `VPJ`: o que dá para medir no D610 sem medidor de energia

A receita, em ordem de custo crescente:

1. **CPU-time por `getrusage` (já usado no repo).** `evaloffline_rss_unix.go:12` já chama
   `syscall.Getrusage(syscall.RUSAGE_SELF, &ru)` para o RSS. O mesmo `Rusage` traz `Utime` e `Stime` (e
   `RUSAGE_CHILDREN` traz o dos filhos — que no jazz é a maior parte, porque `git status`, `bwrap` e `tailscale`
   rodam como subprocesso). Delta antes/depois de cada tarefa dá **CPU-seconds**, que é a grandeza física certa:
   `E = P · t_cpu` com `P` aproximadamente constante. Custo no 386: duas syscalls, ~2 µs, contra p50 de 14 ms —
   0,014%.
2. **Instruções.** No Pentium M, `perf stat -e instructions` provavelmente não funciona (PMU P6, suporte de kernel
   irregular em 32 bits). O caminho que funciona é `valgrind --tool=callgrind`, que conta `Ir` por instrumentação:
   ~50× de slowdown, então uma tarefa de 90 ms vira ~5 s — aceitável para uma rodada de calibração, não para a
   suíte inteira. Uso: uma vez por release, para calibrar `instruções ≈ a · t_cpu + b` e confirmar que `t_cpu` é
   proxy fiel.
3. **Joules de verdade, sem medidor.** O D610 tem bateria. `energy_now` em `/sys/class/power_supply/BAT0/` (µWh)
   amostrado antes/depois de `N` repetições, com o mesmo `N` em idle para subtrair o piso da plataforma. Isso dá
   joules medidos, não estimados. Cuidados: bateria saudável, brilho fixo, wifi off, `N` grande o bastante para a
   granularidade do contador, e a mesma carga de fundo. É a única forma de `VPJ` ser um número e não uma conta de
   guardanapo.

**O limite honesto do `VPJ` no D610:** o piso da plataforma domina. Se o sistema ocioso consome ~2 W (medida do
dono) e a tarefa dura 130 ms, o custo *marginal* é `0,26 J` — mas o custo *total* do intervalo é o mesmo, porque a
máquina está ligada de qualquer jeito. Duas variantes offline que diferem em 20% de CPU diferem em `~0,05 J` por
tarefa, o que é ruído contra o piso. **Conclusão: `VPJ` serve para comparar online × offline (`10⁴`) e não serve
para comparar offline × offline.** Para isso existe `VPS`, que é preciso porque `t_cpu` não tem piso.

### 4.4 `BPT`: bits de descrição por tarefa coberta

A parte de "compressão" da tese, na única forma que não se engana: **comprimento de descrição**, não densidade de
entropia. O princípio é o MDL de Rissanen, *Modeling by shortest data description*, Automatica 14(5):465–471, 1978,
[doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5): entre modelos que explicam os
mesmos dados, o mais curto é o que generaliza.

```
BPT = 8 · (bytes de methods/*.json + bytes de dados duráveis) / (tarefas verificadas cobertas)
```

Medido hoje: `45 099 B × 8 = 360 792 bits` para 49 métodos e 75 tarefas de avaliação — mas a cobertura real é
menor que 75 (nem toda tarefa usa método). O número honesto exige contar, por tarefa, se ela foi resolvida por um
método (`eoGot.Method` já registra isso, `evaloffline.go:69`).

**A armadilha que precisa ser nomeada:** "entropia do artefato" — `H(bytes)/8` — **inverte o sinal**. Um arquivo
`.gz` tem ~8 bits/byte de entropia e é mais ordenado (menor) que o original, que tem ~4,5. Se a métrica for
densidade de entropia, comprimir *piora* o número. Ordem é **comprimento**, não densidade. Quem propuser medir
"entropia do código" está medindo a coisa errada com o nome certo.

### 4.5 `N*`: a conta do demônio, e a única peça que decide arquitetura

Aqui a metáfora vira engenharia de verdade. O raciocínio:

- A forja gasta tokens **uma vez**, em tempo de compilação, para produzir um asset.
- Cada uso offline do asset custa ~0 tokens.
- O caminho sem asset custa `T_online` tokens por tarefa.

Logo:

```
E_total(forja) = T_forge + N · T_offline
E_total(sem forja) = N · T_online

N* = T_forge / (T_online − T_offline) ≈ T_forge / T_online     (T_offline ≈ 0)
```

`N*` é o número de reusos a partir do qual a forja se paga. Ele é **exatamente** o análogo do custo de apagar a
memória do demônio: o demônio de Bennett não viola a segunda lei porque paga `kT·ln2` por bit ao apagar; o jazz não
é mágica porque paga `T_forge` ao forjar. A diferença é que o demônio paga em joules e o jazz paga em tokens — e
tokens são medíveis hoje.

Ordem de grandeza (estimativa, porque a forja não está ligada e não há telemetria dela): uma sessão de forja que
produz um método custa entre `5·10⁴` e `2·10⁵` tokens. Com `T_online = 32,3k` (tarefa trivial medida), `N*` fica
entre **1,5 e 6 usos**. Para tarefas agênticas reais (`10⁵–10⁶` tokens por tarefa), `N* < 1`: o asset se paga no
primeiro uso.

Esse é o resultado mais forte do documento, e ele é chato de propósito: **a bomba de negentropia não é um demônio,
é um investimento com payback de poucos usos.** Se `N* < 1`, a forja é obrigatória; se `N*` é grande, a forja é
luxo. A decisão de arquitetura sai daí, e não de `kT`.

### 4.6 O que a métrica **não** pode ser

Três candidatas que parecem científicas e não são:

1. **`kT·bits_apagados` por tarefa.** Reportaria `~10⁻¹⁴ J` para a tabela NB e `~10⁻²⁰ J` para uma resposta. O piso
   de qualquer medição no D610 é `~10⁻³ J`. A métrica é 11 ordens abaixo do ruído: sempre zero, sempre "ótima".
   Não compila.
2. **Entropia de Shannon do artefato.** Inverte o sinal (§4.4). Não compila.
3. **"COP do demônio"** (`bits_de_ordem / J`). Unidades incompatíveis sem taxa; e a taxa que se escolhe (bits por
   segundo? por tarefa?) determina o resultado. Vira um número arbitrário com nome físico. Não compila.

E uma que é legítima mas traiçoeira: **`VPJ` como número fino**. Ver §4.3: o piso da plataforma come qualquer
diferença abaixo de `~0,1 J`, que é onde todas as variantes offline vivem.

---

## 5. Princípio de design: a escada, não o bit

**Veredito: LOAD-BEARING, com a derivação trocada.** O dono está certo na conclusão ("determinismo, compressão e
reuso economizam energia") e errado na prova ("menos bits apagados"). A prova correta é uma ordenação de alavancas
por ordem de grandeza, e ela é acionável.

### 5.1 A ordenação

Da §3.1, em joules por unidade de trabalho:

```
token de LLM      ~3 J          ← 10²¹ kT   ← alavanca 1: não chame o modelo
instrução (D610)  ~1,2·10⁻⁸ J   ← 4·10¹² kT  ← alavanca 2: execute menos
byte de DRAM      ~6,4·10⁻¹⁰ J  ← 2·10¹¹ kT  ← alavanca 3: toque menos memória
bit apagado       ~2,9·10⁻²¹ J  ← 1 kT       ← alavanca 4: ignore
```

Cada alavanca é `10³–10⁹` vezes a seguinte. Um princípio de projeto que não respeita essa ordem é decorativo.

### 5.2 As regras, cada uma com gancho no código

| # | Regra | Gancho existente | Métrica |
|---|---|---|---|
| R1 | **Não chame o modelo se um caminho verificado existe.** | `brain.go:596` `brLevel0Min`, `brain.go:610` | `NoLLMRate`, `VPK` |
| R2 | **Reuse o que já foi verificado** (plano, asset, resposta). | `plancache.go:389` (`pcStore` só com verificação), `plancache.go:405` (`pcScore` wins/fails) | `VPK`, hit rate |
| R3 | **Comprima o que precisa ficar na memória quente.** A tabela NB é 78% da L2. | `nlu.go:1330` (`nuBuckets`), `nlu.go:1395` | `VPS`, `Ir` |
| R4 | **Não recalcule o que pode ser memoizado** — e meça o hit. | `plancache.go:42` (`pcMinSteps`), `lessons_reuse.go` | tokens economizados |
| R5 | **Cada tarefa só conta se verificada.** Ordem sem verificação é dívida. | `methods.go:1678` (`brVerify`), `forge_gate.go:155` | `FalseSuccess` (bloqueante) |
| R6 | **Meça CPU-time, não wall-time.** Wall-time inclui espera de I/O e de rede, que não é energia do CPU. | `evaloffline_rss_unix.go:12` | `VPS` |
| R7 | **Ação mínima suficiente: não execute o que não precisa rodar.** | `caps.go:553` (`cbMinimalIntervention`) | `commands_per_task` (0,13 medido) |

R3 é a única que fala de memória, e é a que mais se parece com "compressão = energia". O mecanismo é **IPC**, não
`kT`: uma tabela de 1,6 MB em 2 MB de L2, tocada com 50 acessos aleatórios por n-grama (`nlu.go:1444`), expulsa o
working set do parser. Cada falta vira stall, o stall vira tempo, o tempo vira joule — porque `E ≈ P·t`. A
hipótese é testável (§6, P5) e não é óbvia: pode ser que a tabela já esteja bem comportada, e o teste dirá.

R6 merece ênfase porque é onde a medição honesta começa: o p50 de 14 ms da suíte é *wall-time* e inclui `git status`
(17–18 ms medidos isolados) e `bwrap` (9 ms). O CPU-time do jazz é uma fração disso. Reportar wall-time como
energia superestima por um fator que varia com o disco do D610 — o que torna qualquer comparação entre variantes
inútil.

R7 é a regra mais subestimada e a que o repo já pratica melhor: `cbMinimalIntervention` (`caps.go:553`) recusa ação
desproporcional ao objetivo, e a suíte mede **0,13 comandos por tarefa**. A conta que justifica: um `go build` de
30 s no D610 custa `30 × 21 = 630 J`; a resposta offline custa `~2,7 J`. **Uma compilação desnecessária custa 230
vezes a tarefa inteira.** Nenhuma otimização de tabela NB, nenhum ajuste de bits apagados, chega perto disso. É a
mesma escada da §5.1 vista de baixo: a alavanca não é comprimir o que roda, é não rodar.

### 5.3 O que o princípio **não** autoriza

- Não autoriza "reduzir bits apagados" como critério de aceitação de um patch. Nenhum teste consegue medir isso.
- Não autoriza "computação reversível" como roadmap. Ver §3.2.
- Não autoriza tratar `VPJ` como número de marketing. No D610 ele é grosseiro (§4.3).
- Não autoriza chamar a métrica de "negentropia". O nome convida a incluir o termo errado; `VPK`/`VPS` são
  honestos e igualmente vendáveis.

---

## 6. Propostas em Go para o D610

Escala de impacto: **5** = muda decisão de arquitetura; **4** = muda o que se otimiza; **3** = dá visibilidade;
**2** = higiene; **1** = cosmético. Esforço: **S** (< 1 dia), **M** (1–3 dias), **L** (> 3 dias). "Custo 32-bit" é
memória/tempo no i386, não complexidade.

### P1 — `eoSummary` ganha CPU-time e as razões

**Mecanismo.** `getrusage(RUSAGE_SELF)` + `getrusage(RUSAGE_CHILDREN)` antes e depois de cada tarefa; somar
`Utime+Stime` dos dois. Adicionar a `eoSummary` (`evaloffline.go:91`): `CPUMs`, `TasksPerCPUSecond`, `Tokens`,
`TasksPerKTok`. A agregação já existe em `evaloffline.go:218-245`; é somar mais dois acumuladores.

**Arquivos.** `evaloffline.go` (struct + agregação + escrita do JSON em `evaloffline.go:155`); novo
`evaloffline_cpu_unix.go` (build tag `!windows`) e `evaloffline_cpu_windows.go` (stub que devolve 0, como já se faz
com RSS).

**Custo 32-bit.** Duas syscalls por tarefa (~2 µs) contra p50 de 14 ms; 4 campos `float64`/`int64` = 32 B no
relatório. Irrelevante.

**Métrica.** `VPS` e `VPK` por run, no mesmo JSON que já vai para `results/offline-<time>.json`.

**Impacto 4 · Esforço S · Risco baixo.** O único risco é confundir `RUSAGE_CHILDREN` (que é cumulativo do processo,
não por filho) — o delta funciona, mas é preciso medir antes/depois, não ler absoluto.

### P2 — `jazz cost`: o relatório que junta eval + obs

**Mecanismo.** Um comando que lê o último `results/offline-*.json` e um `--metrics-out` JSON, e imprime a tabela
competitiva: `VPK`, `VPS`, `VPJ` (com `P` configurável), `NoLLMRate`, `FalseSuccess`. Função pura
`costReport(offline eoReport, session obSummary, watts float64) costRow`, testável sem I/O.

**Arquivos.** Novo `cost.go` (~200 linhas); `main.go` no bloco de subcomandos (`main.go:512` é o `case "eval"`,
o lugar natural é ao lado).

**Custo 32-bit.** Só leitura de JSON; pico de RSS dominado pelo arquivo (os relatórios têm centenas de KB).

**Métrica.** O número que se coloca num README ou num gate de release.

**Impacto 4 · Esforço M · Risco baixo.** Risco real: virar dashboard. A defesa é o formato de uma linha e o
`false_success` bloqueante.

### P3 — ligar o ledger de sinais (fechar o laço)

**Mecanismo.** Chamar `sigAppend` (`signals.go:52`) nos três pontos onde o cérebro falha de forma observável:
`brNotUnderstood` (`brain.go:214`), a falha de verificação em `brRecover` (`brain.go:428`, com `why` em `Extra`), e
a recusa do nível 0 (`brain.go:610`, quando a confiança fica abaixo de `brLevel0Min`). Preencher `Kind`
(`miss|verifier_missing|unknown_intent|new_method`), `Class`, `Text`, `Verified: false`.

**Arquivos.** `brain.go` (3 chamadas), `signals.go` (já pronto).

**Custo 32-bit.** Um `O_APPEND` + `flock` por falha, ~50–200 µs no D610 (disco de 2004). Só em caminho de erro;
no caminho feliz, zero. Memória: nenhuma.

**Métrica.** `sinais por 100 tarefas` = taxa de vazamento de entropia. É o numerador que a forja consome; depois
da forja, a queda dessa taxa é a medida de sucesso do asset.

**Impacto 5 · Esforço M · Risco baixo-médio.** Riscos: contenção de `flock` se muitos processos falharem juntos
(o padrão já é usado por `scores.go`/`ratelimit.go`, então é conhecido); e crescimento sem poda do `.jsonl` — precisa
de rotação por tamanho ou por idade, senão o `sigRead` de 1 MiB por linha vira lento em anos.

### P4 — contabilidade de tokens da forja e `N*`

**Mecanismo.** A forja já produz assets; falta registrar quanto custou. Um struct `AssetCost{Asset, TokensIn,
TokensOut, TasksCovered, VerifiedWins}` em `dataDir()/forge-cost.jsonl`, e a função `payback(c AssetCost, tOnline
float64) float64` devolvendo `N*`. Alimentar `TasksCovered`/`VerifiedWins` com `pcScore` (`plancache.go:405`), que
já conta wins/fails por plano.

**Arquivos.** Novo `forge_cost.go` (~150 linhas); `plancache.go` para ler wins.

**Custo 32-bit.** Desprezível (um JSON por asset).

**Métrica.** `N*` por asset; mediana de `N*` da biblioteca. É o número que decide se a forja continua.

**Impacto 4 · Esforço S · Risco médio.** Depende de P3 (sem sinais, não há o que forjar) e de a forja existir como
processo; hoje ela é externa e não instrumentada. O risco é medir `T_forge` por estimativa e apresentar como
medição — a distinção `(medido)`/`(estimativa)` tem de ir no JSON.

### P5 — ablação do tamanho da tabela NB (a hipótese de compressão)

**Mecanismo.** Rodar a suíte de 75 tarefas (`evals/offline/tasks.json`) com `nuBuckets = 1<<14` (atual), `1<<13`,
`1<<12`, e com a tabela em `uint8` saturante por classe (metade dos bytes). Medir `CPUMs` (P1) e accuracy. Aceitar
uma variante só se passar `Gate` (`forge_gate.go:143`) contra a baseline: sem regressão em held-in/held-out,
`false_success == 0`, melhoria estrita em pelo menos um.

**Arquivos.** `nlu.go:1330` (constante), `nlu.go:1395` (tipo da tabela), `nlu.go:1444` (leitura). A ablação é um
`go test -bench` ou uma flag de ambiente, não um fork.

**Custo 32-bit.** `1<<12` × 50 × 2 B = 409 600 B (20% da L2, contra 78%). `uint8` = 819 200 B. A memória cai;
a precisão pode cair junto — é o que o teste decide.

**Métrica.** `VPS` (tarefas/s de CPU) com accuracy constante. Se `VPS` subir > 10% sem perder accuracy, a hipótese
"compressão = energia" ganhou o primeiro número real do projeto.

**Impacto 4 · Esforço M · Risco médio-alto.** Risco de accuracy: a tabela menor colide mais n-gramas. O `Gate`
existe exatamente para isso; sem ele, não faça.

### P6 — joules medidos por descarga de bateria

**Mecanismo.** Script `scripts/energy-Dell.sh`: amostra `energy_now` de `/sys/class/power_supply/BAT0/`, roda
`N=200` repetições de uma tarefa fixa, amostra de novo, repete em idle, subtrai. Saída: `J/tarefa` com intervalo.

**Arquivos.** Script novo; nenhuma mudança em Go (usa o binário como está).

**Custo 32-bit.** Zero em runtime; ~10 min de parede por medição.

**Métrica.** `VPJ` medido, não estimado — a única forma de o número sobreviver a auditoria.

**Impacto 3 · Esforço S · Risco médio.** Bateria envelhecida, brilho, wifi, carga de fundo. Serve para a manchete
("uma tarefa offline custa X joules, uma online custa 10⁴ X"), não para A/B fino.

### P7 — contagem de instruções para calibrar o proxy

**Mecanismo.** Rodar uma tarefa sob `valgrind --tool=callgrind` uma vez por release; extrair `Ir`; ajustar
`Ir ≈ a·CPUMs + b` e reportar `R²`. Se `R² > 0,95`, `CPUMs` é proxy validado e o resto do projeto pode confiar nele.

**Arquivos.** Script; nenhum código de produção.

**Custo 32-bit.** `valgrind` no D610: ~50× slowdown, memória ~10× — uma tarefa de 90 ms vira ~5 s e ~100 MB. Ok
para uma tarefa; não para a suíte.

**Métrica.** `R²` do proxy. Sem isso, `VPS` é uma correlação não auditada.

**Impacto 2 · Esforço S · Risco baixo** (exceto `valgrind` em 32 bits, que precisa estar instalado).

---

## 7. O que NÃO fazer

1. **Não instrumentar "bits apagados".** Não há contador, não há proxy, e o número seria 11 ordens abaixo do ruído.
   Qualquer PR que proponha isso deve ser rejeitado com a §3.1.
2. **Não colocar computação reversível no roadmap.** Bennett 1973 é lindo e não roda em Go sobre um Pentium M.
3. **Não usar "entropia do artefato" como métrica de ordem.** Inverte o sinal (§4.4).
4. **Não reportar `VPJ` de variantes offline como se fosse discriminativo.** O piso da plataforma come a diferença.
   Use `VPS`.
5. **Não citar Prigogine para justificar minimização de dissipação.** O teorema vale no regime linear; as
   estruturas dissipativas são a sua negação. Citar ao contrário é o tipo de erro que destrói a credibilidade do
   resto do documento.
6. **Não chamar a métrica de "negentropia".** Chame de `VPK`/`VPS`. O nome físico convida o termo errado para a
   conta.
7. **Não medir wall-time e chamar de energia.** Wall-time inclui `git status`, `bwrap`, I/O de disco de 2004 e
   espera de rede; nada disso é o CPU do jazz.
8. **Não otimizar a tabela NB sem o `Gate`.** A ablação de P5 é a única forma de saber se "compressão" ganhou ou
   perdeu; sem o portão, é fé.
9. **Não apresentar `N*` estimado como medido.** Enquanto a forja não tiver telemetria de tokens, `N*` é
   `(estimativa)` e deve estar marcado.
10. **Não prometer "máxima ordem por joule" como slogan.** A promessa verificável é "ordem verificada por token", e
    ela é mais fraca e mais útil.

---

## 8. Veredito

**A tese do dono é 30% engenharia e 70% motivação — e os 30% são os 30% certos.**

O que **vira código e número**, hoje, sem hardware novo:

- **`VPK` — tarefas verificadas por 10³ tokens.** A infraestrutura já existe (`types.go:43`, `obs.go:925`,
  `evaloffline.go:91`); falta uma divisão. É a métrica que decide a arquitetura, e ela é honesta porque
  `false_success` é bloqueante.
- **`VPS` — tarefas verificadas por segundo de CPU.** `getrusage` já é usado no repo; é preciso somar `Utime/Stime`
  e `RUSAGE_CHILDREN`. É a métrica fina, para comparar variantes offline.
- **`N*` — reusos para pagar a forja.** A única peça da metáfora que decide algo grande. `N* ≈ 1,5–6` para tarefas
  triviais (estimativa); `< 1` para tarefas agênticas reais. **A bomba de negentropia é um investimento com payback
  de poucos usos, e esse é um resultado melhor que o slogan.**
- **O princípio de design, com a prova corrigida:** não chame o modelo; execute menos instruções; toque menos
  memória; reuse o verificado. Nessa ordem, porque cada degrau é `10³–10⁹` o anterior. "Menos bits apagados" é o
  quarto degrau, `10¹²` abaixo do primeiro, e não deve aparecer em nenhum critério de aceitação.

O que **não compila** e deve ficar na seção de motivação de um README, nunca num design doc:

- o demônio de Maxwell como modelo do agente (o fluxo é invertido: é uma geladeira, não um motor);
- negentropia de Schrödinger como mecanismo (é energia livre, e ele sabia);
- estruturas dissipativas de Prigogine como teorema aplicável (o teorema citado é o que a estrutura viola);
- o princípio de energia livre de Friston como arquitetura (o NB é discriminativo; só o `−log p` carrega);
- computação reversível como caminho de eficiência (a folga é de `10¹²`);
- `kT·bits` como métrica (invisível por 11 ordens).

**A frase que eu escreveria no README**, e que é verdadeira sem dourar:

> O jazz impõe ordem local — uma tarefa resolvida e verificada — gastando o mínimo de trabalho por unidade de
> ordem: zero tokens quando um asset verificado se aplica, ~10⁸ instruções quando não. A termodinâmica garante que
> essa ordem tem preço; a engenharia mostra que o preço é pago em instruções e tokens, não em bits apagados. O
> número que decide a arquitetura é `N*`: quantos reusos pagam a forja. Hoje, poucos.

E a resposta direta à pergunta-chave, sem rodeio: **sim, produz métrica real e útil — `VPK`, `VPS` e `N*` — e não,
o princípio de projeto não é "menos bits apagados"; é "menos instruções, menos bytes, menos tokens, mais reuso". A
conclusão do dono sobrevive; a derivação dele, não. É a diferença entre um documento que se defende numa revisão e
um que só soa bem.

---

## Apêndice A — as contas, linha por linha

Para quem quiser auditar os números sem reabrir o documento:

```
kT a 300 K                 = 1,380649e-23 × 300        = 4,1419e-21 J
kT·ln2                     = 4,1419e-21 × 0,693147     = 2,8712e-21 J
1 J em unidades de Landauer= 1 / 2,8712e-21            = 3,48e20 bits apagados

D610: 21 W / 1,7e9 Hz      = 1,235e-8 J por ciclo
     / 2,8712e-21          = 4,30e12 kT por ciclo (IPC 1)
     instruções por joule  = 1 / 1,235e-8             = 8,10e7

45 nm, Horowitz 2014:
  int32 add  0,1 pJ        = 1,0e-13 J  → 3,5e7 kT
  SRAM 32b   5 pJ          = 5,0e-12 J  → 1,7e9 kT
  DRAM 32b   640 pJ        = 6,4e-10 J  → 2,2e11 kT

LLM, Samsi 2023 (LLaMA-65B, 8×V100):
  3 J/token                → 3 / 2,8712e-21 = 1,04e21 kT
  FLOPs/token ≈ 2×6,5e10   = 1,3e11
  D610 a ~2 FLOP/ciclo     = 3,4e9 FLOP/s
  1 token do modelo        = 1,3e11 / 3,4e9 ≈ 38 s de D610

tarefa "abre o README.md":
  offline  0,13 s × 2 W    = 0,26 J   (marginal mínimo)
           0,13 s × 21 W   = 2,73 J   (TDP cheio)
  online   32,3k × 1 J     = 32,3 kJ  (conservador)
           32,3k × 3 J     = 96,9 kJ  (Samsi)
  razão                    1,2e4 … 3,7e5
  96,9 kJ / 21 W           = 4 614 s = 77 min de D610 a plena carga

tabela NB                  = 50 × 16 384 × 2 B = 1 638 400 B
  fração da L2 (2 MB)      = 1 638 400 / 2 097 152 = 78,1%
  bits                     = 1,31e7
  custo de Landauer        = 1,31e7 × 2,8712e-21 = 3,76e-14 J
  custo real de tocar      ≈ 4,1e5 palavras × 640 pJ = 2,6e-7 J
  razão real/Landauer      ≈ 7e6

biblioteca de métodos      = 45 099 B × 8 = 360 792 bits
  vs tabela NB             = 1 638 400 / 45 099 ≈ 36×
```

## Apêndice B — fontes primárias

- Landauer, R. *Irreversibility and heat generation in the computing process.* IBM J. Res. Dev. 5(3):183–191, 1961. [doi:10.1147/rd.53.0183](https://doi.org/10.1147/rd.53.0183)
- Bennett, C. H. *Logical reversibility of computation.* IBM J. Res. Dev. 17(6):525–532, 1973. [doi:10.1147/rd.176.0525](https://doi.org/10.1147/rd.176.0525)
- Bennett, C. H. *The thermodynamics of computation — a review.* Int. J. Theor. Phys. 21:905–940, 1982. [doi:10.1007/BF02084158](https://doi.org/10.1007/BF02084158)
- Bennett, C. H. *Notes on Landauer's principle, reversible computation, and Maxwell's Demon.* Stud. Hist. Phil. Mod. Phys. 34:501–510, 2003. [doi:10.1016/S1355-2198(03)00039-X](https://doi.org/10.1016/S1355-2198(03)00039-X)
- Szilard, L. *Über die Entropieverminderung in einem thermodynamischen System bei Eingriffen intelligenter Wesen.* Z. Phys. 53:840–856, 1929. [doi:10.1007/BF01341281](https://doi.org/10.1007/BF01341281)
- Brillouin, L. *The negentropy principle of information.* J. Appl. Phys. 24(9):1152–1163, 1953. [doi:10.1063/1.1721463](https://doi.org/10.1063/1.1721463)
- Bérut, A. et al. *Experimental verification of Landauer's principle linking information and thermodynamics.* Nature 483:187–189, 2012. [doi:10.1038/nature10872](https://doi.org/10.1038/nature10872)
- Jun, Y., Hutzler, N. & Bechhoefer, J. *High-precision test of Landauer's principle in a feedback trap.* PRL 113:190601, 2014. [doi:10.1103/PhysRevLett.113.190601](https://doi.org/10.1103/PhysRevLett.113.190601)
- Norton, J. D. *Eaters of the lotus: Landauer's principle and the return of Maxwell's demon.* Stud. Hist. Phil. Mod. Phys. 36:375–411, 2005. [doi:10.1016/j.shpsb.2004.12.002](https://doi.org/10.1016/j.shpsb.2004.12.002)
- Sagawa, T. & Ueda, M. *Minimal energy cost for thermodynamic information processing.* PRL 100:080403, 2008. [doi:10.1103/PhysRevLett.100.080403](https://doi.org/10.1103/PhysRevLett.100.080403)
- Parrondo, J. M. R., Horowitz, J. M. & Sagawa, T. *Thermodynamics of information.* Nat. Phys. 11:131–139, 2015. [doi:10.1038/nphys3230](https://doi.org/10.1038/nphys3230)
- Still, S., Sivak, D. A., Bell, A. J. & Crooks, G. E. *Thermodynamics of prediction.* PRL 109:120604, 2012. [doi:10.1103/PhysRevLett.109.120604](https://doi.org/10.1103/PhysRevLett.109.120604)
- Wolpert, D. H. *The stochastic thermodynamics of computation.* J. Phys. A 52:193001, 2019. [doi:10.1088/1751-8121/ab0850](https://doi.org/10.1088/1751-8121/ab0850)
- Schrödinger, E. *What is Life?* Cambridge University Press, 1944 (cap. 6).
- Prigogine, I. *Time, Structure and Fluctuations* (Nobel Lecture, 1977). [nobelprize.org](https://www.nobelprize.org/prizes/chemistry/1977/prigogine/lecture/)
- Glansdorff, P. & Prigogine, I. *Thermodynamic Theory of Structure, Stability and Fluctuations.* Wiley, 1971.
- Friston, K. *The free-energy principle: a unified brain theory?* Nat. Rev. Neurosci. 11:127–138, 2010. [doi:10.1038/nrn2787](https://doi.org/10.1038/nrn2787)
- Colombo, M. & Wright, C. *First principles in the life sciences: the free-energy principle, organicism, and mechanism.* Synthese 198:3463–3488, 2021. [doi:10.1007/s11229-019-02424-1](https://doi.org/10.1007/s11229-019-02424-1)
- Horowitz, M. *Computing's energy problem (and what we can do about it).* ISSCC 2014. [doi:10.1109/ISSCC.2014.6757323](https://doi.org/10.1109/ISSCC.2014.6757323)
- Williams, S., Waterman, A. & Patterson, D. *Roofline: an insightful visual performance model for multicore architectures.* CACM 52(4):65–76, 2009. [doi:10.1145/1498765.1498785](https://doi.org/10.1145/1498765.1498785)
- Samsi, S. et al. *From Words to Watts: Benchmarking the Energy Costs of Large Language Model Inference.* IEEE HPEC 2023. [arXiv:2310.03003](https://arxiv.org/abs/2310.03003) · [doi:10.1109/HPEC58863.2023.10363447](https://doi.org/10.1109/HPEC58863.2023.10363447)
- Luccioni, A. S., Jernite, Y. & Strubell, E. *Power Hungry Processing: Watts Driving the Cost of AI Deployment?* FAccT 2024. [arXiv:2311.16863](https://arxiv.org/abs/2311.16863) · [doi:10.1145/3630106.3658542](https://doi.org/10.1145/3630106.3658542)
- Rissanen, J. *Modeling by shortest data description.* Automatica 14(5):465–471, 1978. [doi:10.1016/0005-1098(78)90005-5](https://doi.org/10.1016/0005-1098(78)90005-5)
- Intel, *Pentium M Processor 735* (Dothan, 90 nm, 1,7 GHz, 2 MB L2, TDP 21 W), datasheet 252612.
