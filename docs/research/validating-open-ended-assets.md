# Validar asset forjado de final aberto: oráculos parciais, refutação e arbitragem

Pesquisa de 04/11/2026. Este documento ataca um buraco específico: **`forge_gate.go` valida
classificação, e o forge vai forjar coisas que não são classificação** — método HTN (`mhMethod`),
script/parser (`scDef`) e verificador (`mhCheck`). Para esses três não existe "a classe certa" no
held-out. Não há `Want`. O portão atual compara `Accuracy` e `Coverage` sobre `Example{Input, Want}`
(`forge_gate.go:38-51`), e essa abstração simplesmente não tem referente quando o asset é um plano
de ação ou um predicado sobre o mundo.

Continua, sem repetir: [verification-oracles-deep.md](verification-oracles-deep.md) (como verificar
*a ação*: DSL, MRs, kill-rate, PASS_TO_PASS), [plan-reuse-deep.md](plan-reuse-deep.md) (ranking de
plano, gate de promoção), [skill-induction-deep.md](skill-induction-deep.md) (§5.5, promoção em 3
estágios), [program-synthesis-library-learning.md](program-synthesis-library-learning.md) (§G, o
portão held-out compartilhado), [unifying-math-offline.md](unifying-math-offline.md) (§3.10, `ΔJ` e
MDL) e [math-offline-brain.md](math-offline-brain.md) (Chow, calibração). O objeto aqui é outro:
**o que significa "validado" quando não há rótulo, e quem ganha quando dois assets casam o mesmo
pedido.**

Convenções: `file:line` refere-se ao `main` desta sessão. **Medido** = número já em `results/`.
**(estimativa)** = conta minha, não medida. "Dell" = i686, 1 CPU, 2 GB, `linux/386`, Go stdlib.
Identificadores de código em inglês. Prova minha é marcada como tal; onde não há resposta boa, o
texto diz "não sei" em vez de inventar uma.

---

## 0. Placar

| # | Afirmação implícita | Veredito | A razão em uma linha |
|---|---|---|---|
| 1 | O held-out de `forge_gate.go` valida qualquer asset | **FALSO como está** | `Example.Want` não existe para método/script/verificador; a *métrica* é de classificação, o *split* não é. |
| 2 | Dá para saber se um asset de final aberto está certo | **NÃO em geral** | Decidir propriedade semântica não trivial é indecidível (Rice 1953); testar mostra presença, não ausência (Dijkstra 1972). |
| 3 | Oráculo metamórfico resolve | **PARCIAL** | É condição *necessária*, não suficiente (Chen 1998). `sort` por valor absoluto passa em invariância, idempotência e tamanho, e falha só na ordenação. |
| 4 | Teste diferencial dá a resposta | **NÃO** | Divergência é evidência sonora de *algum* bug; concordância não é prova — falhas coincidentes são muito acima da independência (Knight & Leveson 1986). |
| 5 | Property-based testing cobre | **SÓ O SUPORTE DO GERADOR** | Passar `n` casos só diz que a região de falha tem probabilidade `< ~1/n` *sob a distribuição do gerador*. |
| 6 | Snapshot/characterization valida | **VALIDA DRIFT, NÃO CORREÇÃO** | Um snapshot tirado de um asset errado congela o erro: poder zero contra erro constante, poder total contra mudança (Feathers 2004). |
| 7 | Shadow-git valida escrita | **VALIDA SUPORTE, NÃO INTENÇÃO** | `support(Δ) ⊆ Effects` pega efeito colateral; não pega `rename` implementado como copiar+truncar (só uma MR de conteúdo pega). |
| 8 | O asset pode declarar o próprio verificador | **NÃO — é o furo central** | `verify: cmd_ran` passa com exit 1 (`methods.go:1709-1715`); o avaliador não pode usar o `verify` do candidato como verdade. |
| 9 | `Priority` decide quem ganha | **FALSO como critério** | Prioridade é *prior*, não rank; a ordem ótima de uma cascata é `p/c` (Smith 1956), provado por troca de adjacentes. |
| 10 | Win-rate decide | **SÓ COM LCB E CONDICIONAMENTO** | Média de vitórias sem limite inferior e sem condicionar em `(intent, preSig, shape)` cai em Simpson (1951). |
| 11 | Recência decide | **SÓ SOB DRIFT** | Sob estacionariedade, pesos desiguais só aumentam a variância (Cauchy–Schwarz); decaimento é aposta em deriva. |
| 12 | Especificidade decide | **É CONDICIONAMENTO, NÃO QUALIDADE** | Um método específico com `n=1` parece 100%; o LCB o rebaixa sozinho, sem regra extra. |
| 13 | Uma bateria de `k` sondas prova | **NÃO PROVA; DIMENSIONA** | União-limite: com `N` candidatos e `q` de passe casual, `k ≥ ln(N/α)/ln(1/q)` é o piso, não a garantia. |

**Resumo de uma frase:** para asset de final aberto, validação não é *medir acerto* — é **buscar
refutação com orçamento declarado, sobre uma bateria cujo poder é medido, com um avaliador que o
asset não controla**. E arbitragem não é prioridade estática: é **estimativa conservadora de
utilidade por custo, com a segurança como restrição lexicográfica**.

---

## 1. O erro de categoria

### 1.1 O que o portão mede hoje — e por que não generaliza

`forge_gate.go` é um motor puro com avaliador injetado:

```go
type Example struct { ID, Input, Want, Group string }      // forge_gate.go:38
type Metrics struct { Accuracy float64; FalseSuccess int; Coverage float64; N int } // :46
type EvalFn  func(ex []Example) Metrics                     // :56
func Gate(examples []Example, baseline, candidate EvalFn, heldOutFrac float64, seed int64) (bool, GateReport) // :143
```

A regra é boa e deve sobreviver: *nem held-in nem held-out regridem, ao menos um melhora, e
`false_success == 0` bloqueia* (`forge_gate.go:152-171`). O problema é o **tipo do domínio**.

Em classificação, validação é estimar `P(ŷ(x) = y)` numa amostra held-out. O objeto `y` existe, é
discreto e foi rotulado. Para um método HTN não existe `y`: existe um **estado de mundo** antes
(`w`) e depois (`w'`), e um **contrato** `C = (pre, post, effects)`. A pergunta não é "acertou a
classe?", é "`C(w, run(asset, w))` vale para os `w` que importam?". Três consequências:

1. **`Want` não tem preenchimento honesto.** Preencher `Want` com a saída do baseline transforma o
   gate num teste de *igualdade ao pai* — que rejeita toda melhoria (o candidato tem que ser
   diferente para melhorar) e aceita toda cópia. Preencher com a saída do LLM forte transforma o
   gate num teste de *imitação*, que é a definição de dependência que a filosofia quer eliminar.
2. **`Accuracy` não é a métrica.** O evento elementar deixa de ser "acertou/errou" e passa a ser
   "foi refutado/não foi refutado por esta sonda". São contagens discretas; `gateEps = 0.01`
   (`forge_gate.go:34`) é uma tolerância de ruído contínuo aplicada a um domínio discreto — não tem
   significado.
3. **`Coverage` fica ambíguo.** Cobertura de quê? No código atual, "cobertura" é um número que o
   `EvalFn` inventa; nada no repo a define. Para asset de final aberto, a definição que faz sentido
   é outra (§6.1): **largura de observação do verificador**, não cobertura de tarefas.

O detalhe que torna a correção barata: **nada em produção chama `Gate`.** `grep -rnE '\bGate\('`
encontra a definição (`forge_gate.go:143`) e quatro chamadas em `forge_gate_test.go` — mais nada. O
arquivo é um motor sem chamador e sem subcomando `jazz forge` ligado. Ou seja: **este é o momento
mais barato da história do repo para consertar a abstração** — não há compatibilidade a preservar.
`Gate` pode continuar existindo para assets de classificação (exemplos NLU, léxico) e um `GateOpen`
nasce ao lado, para os demais.

### 1.2 O que sobrevive do held-out: o split, não o rótulo

O que é reutilizável é `Split` (`forge_gate.go:92-126`) e `splitHash` (`:72-79`): hash FNV-1a
determinístico de `(seed, ID)`, estratificado por `Group`, com fração exata por grupo. Isso é
exatamente o primitivo certo — **trocando a unidade**: de `Example` para `Probe`. O `Group` deixa de
ser "família de intenção" e passa a ser **repositório ou família de fixture**.

Por que o grupo importa mais aqui do que em classificação: fixtures capturadas de sessões reais são
**clusterizadas** (mesmo repo, mesmo usuário, mesmos hábitos). Um split por caso superestima a
generalização. O efeito de desenho de Kish (Kish, *Survey Sampling*, Wiley, 1965) quantifica:
`DEFF = 1 + (m-1)ρ`, onde `m` é o tamanho do cluster e `ρ` a correlação intra-classe. Com `m=8` e
`ρ=0.3`, `DEFF = 3.1`: **20 casos valem ~6,5 observações efetivas**. É a justificativa matemática do
"distinct-context ×2" que `skill-induction-deep.md` §5.5 já propõe por intuição — e um aviso de que
uma bateria de 20 sondas em 2 repos é, na prática, uma bateria de 6.

### 1.3 Definições que o resto do documento usa

| Termo | Definição operacional |
|---|---|
| **asset** | `mhMethod` (plano sobre caps registradas), `scDef` (script + sidecar) ou `mhCheck` (predicado sobre mundo). |
| **contrato** | `C = (pre, post, effects)`: o que o asset promete, independente do que ele declara em `verify`. |
| **sonda** (`Probe`) | uma tentativa determinística de refutar: monta um mundo, roda o asset, julga o mundo resultante. |
| **testemunha** (`Witness`) | a descrição mínima do mundo + passo em que o contrato falhou. |
| **refutação** | uma sonda que o baseline sobrevive e o candidato não. É o evento elementar. |
| **sobrevivência** | o asset não foi refutado pela sonda. **Não** significa "correto" (§2.3). |
| **kill-rate** | fração de mutantes de mundo que o verificador rejeita (§2.4). Poder observado do oráculo. |

### 1.4 A mudança de métrica, sem eufemismo

Sai `Accuracy/100`; entra:

```
survival(S)  = sobreviventes / sondas executáveis
refuted(S)   = sondas que produziram testemunha
unavailable  = sondas que não puderam rodar (mundo inviável, cap ausente)
cost_ms      = mediana pareada de custo por sonda
```

`survival` **não é** `P(correto)`. É `P(não refutado por esta bateria)`, e depende inteiramente da
bateria. O relatório tem que dizer isso na string de motivo — o precedente já existe e é bom:
`Gate` escreve o motivo com números (`forge_gate.go:154-170`). `GateOpen` deve escrever
`"não refutado em 14/14 sondas (9 held-out, 5 held-in), custo +3 ms"`, nunca `"correto"`.

---

## 2. O problema do oráculo, sem eufemismo

### 2.1 Todo oráculo é parcial — e a DSL tem um limite provável

Barr, Harman, McMinn, Shahbaz & Yoo, *The Oracle Problem in Software Testing: A Survey*, IEEE TSE
41(5):507–525, 2015, [doi:10.1109/TSE.2014.2372785](https://doi.org/10.1109/TSE.2014.2372785): a
pergunta certa não é "está certo?", é **"o que este oráculo consegue distinguir?"**. A resposta para
o jazz é um teorema pequeno e útil.

**Teorema 1 (indistinguibilidade da DSL).** Seja `W` o conjunto de estados de mundo observáveis. Um
verificador `V` é uma composição booleana de folhas `c_1..c_k`, cada `c_i : W → {pass, fail,
unavail}` determinística. Defina `w ~_V w'` sse `c_i(w) = c_i(w')` para todo `i`. Então `V(w) =
V(w')`. Em particular, **`all/any/not` sobre as mesmas folhas não separa nenhum par de mundos que
as folhas já não separassem**.

*Prova.* `V` é uma função do vetor `(c_1(w),…,c_k(w))`; se os vetores são iguais, a função devolve o
mesmo valor. `all/any/not` são funções desse vetor, logo também devolvem o mesmo. ∎

**Corolário (o que compra poder).** Só uma **folha nova** — uma leitura nova do mundo — aumenta a
partição. Composição compra *política* (que combinação é aceitável), não *observação*. No jazz isso
importa duas vezes: (a) a DSL já tem negações como folhas (`file_exists`/`file_gone`, `exit_zero`/
`cmd_ran`, `methods.go:1687-1715`), então `not` compra quase nada; (b) a proposta P1 de
`verification-oracles-deep.md` (árvore `all/any/not` + comparadores) é **necessária para
expressividade e insuficiente para poder** — o ganho real vem dos checks novos (`file_sha256`,
`count_eq`, `git_head_changed`), não da árvore. Isso não invalida P1; muda a prioridade.

Consequência prática para validação de asset: **medir o poder do verificador é medir quantas folhas
distintas ele lê**, não quantos `all` ele aninha. É o que a proposta de `VerifyBreadth` (§6.1) faz.

### 2.2 Não existe bateria completa (e por que o jazz escapa só em parte)

**Teorema 2 (diagonalização, com hipóteses explícitas).** Seja `P` uma bateria finita de sondas, cada
uma examinando um conjunto finito de estados de mundo. Se a família de assets for rica o suficiente
para expressar "se o mundo pertence ao conjunto sondado, comporte-se corretamente; senão, não",
então existe um asset errado que passa em todas as sondas.

*Prova.* A união dos estados sondados é finita; enumere-a. Construa o asset que consulta essa lista e
desvia. Fora dela, faz o errado. ∎

Este é o argumento de Weyuker, *On Testing Non-Testable Programs*, The Computer Journal
25(4):465–470, 1982,
[doi:10.1093/comjnl/25.4.465](https://doi.org/10.1093/comjnl/25.4.465), e o caso particular de Rice
1953, *Classes of recursively enumerable sets and their decision problems*, TAMS 74:358–366,
[doi:10.1090/S0002-9947-1953-0053041-6](https://doi.org/10.1090/S0002-9947-1953-0053041-6):
propriedade semântica não trivial é indecidível.

**A parte desconfortável para o jazz:** a família `mhMethod` é fechada (passos sobre caps
registradas), e isso *deveria* bloquear a diagonalização — um método não pode perguntar "estou sendo
sondado?". Mas **`mhCond` aceita `op:"eq"` com literal** (`methods.go:42-46`, `:133-134`). Ou seja,
um asset forjado **pode** especializar por valor de slot:

```json
{"name": "open.evalfix", "intent": "open_file", "priority": 99,
 "pre": [{"fact": "slot.path", "op": "eq", "val": "README.md"}], ...}
```

Isso não é hipótese: é a sintaxe que o loader aceita hoje. A defesa não é teórica, é de processo:
**as fixtures das sondas têm que ter nomes/paths aleatórios por execução**, e o gate tem que rejeitar
método cujas pré-condições contenham literais que aparecem no corpus de forja. O repo já tem o
detector para *código* (`hgHardcodeCheck`, `guard_honest.go:1086`), mas **não tem o equivalente para
`methods/*.json`** — e é uma checagem de ~30 linhas, porque `mhCond.Val` é string e o corpus é uma
lista.

Sem essa defesa, "a superfície fechada é a segurança" é falso: a superfície é fechada em *ações*, não
em *condições*. Não encontrei essa distinção escrita nos docs irmãos que li, e é o achado mais
acionável desta seção.

### 2.3 Refutação, não confirmação

Dijkstra, *The Humble Programmer* (EWD340), CACM 15(10):859–866, 1972,
[doi:10.1145/355604.361591](https://doi.org/10.1145/355604.361591): teste mostra presença de bug, não
ausência. Popper, *Conjectures and Refutations*, Routledge, 1963: ciência é conjectura + tentativa de
refutação; a assimetria é lógica, não estatística.

A tradução para o portão é literal e muda o vocabulário:

- `Gate` hoje diz `"aceito: ... melhorou"` (`forge_gate.go:169`). Para classificação isso é
  defensável (há estimativa de acurácia).
- `GateOpen` deve dizer `"não refutado"` e listar a bateria. A diferença não é cosmética: se o
  relatório diz "aceito", o próximo leitor assume garantia que não existe; se diz "não refutado em
  `k` sondas de custo `c`", ele sabe exatamente o que foi comprado.

O corolário operacional: **o gate não pode ser a única evidência de promoção.** Precisa de
`distinct-context ×2` (skill-induction §5.5) e de demote por falha em uso (`fails ≥ 2`), porque a
única coisa que aumenta a confiança depois da promoção é *sobreviver a mundos novos*.

### 2.4 O único lugar onde a verdade é construível: mutação do verificador

Para método e script, o oráculo é observado. Para **verificador**, não: nós *construímos* o mundo
errado. É a inversão que resolve o problema do oráculo no único ponto onde ele é resolvível.

**Mecanismo.** Seja `M` um conjunto de mutantes de mundo construídos a partir do estado pós-sucesso
`w*`: arquivo apagado, arquivo truncado, conteúdo trocado, `HEAD` não movido, unit ativa mas morta,
exit 0 com zero testes. Seja `V` o verificador do asset. Defina

```
kill(V, M) = |{ m ∈ M : V(m) = fail }| / |M|
```

**Direção sonora.** Se `V` aceita `m` e `m` **não é equivalente** a `w*` (i.e., existe algum aspecto
do contrato que `m` viola), então `V` é **inseguro naquele mutante** — testemunha concreta de fraqueza.
Isso é som, não estatística: um mutante aceito é prova.

**Direção não sonora.** `kill < 1` **não** prova fraqueza, porque alguns mutantes são
*equivalentes* (o mundo mutado ainda satisfaz o contrato). Decidir equivalência de mutantes é
indecidível no caso geral — é Rice 1953 de novo, e é o mesmo problema de equivalência de programas
tratado por Budd & Angluin, *Two notions of correctness and their relation to testing*, Acta
Informatica 18:31–45, 1982,
[doi:10.1007/BF00625279](https://doi.org/10.1007/BF00625279); a literatura de mutação chama isso de
*equivalent mutant problem* desde DeMillo, Lipton & Sayward, *Hints on Test Data Selection*, Computer
11(4):34–41, 1978,
[doi:10.1109/C-M.1978.218136](https://doi.org/10.1109/C-M.1978.218136).

Conclusão honesta: **kill-rate é limite inferior de força, e só se usa comparativamente** (candidato
vs pai, na mesma `M`) ou como piso (`kill == 0` ⇒ rejeita). Nunca como "100% = correto". É a mesma
conclusão de `verification-oracles-deep.md` P5, mas com o uso corrigido: **comparativo, não absoluto**.

Custo: mutantes de mundo são baratos (criar/apagar/truncar arquivo; mover `HEAD` num repo temporário).
10 mutantes × 20 métodos × ~50 ms de shell-out ≈ 10 s por asset, em tempo de forja, na máquina amd64.

### 2.5 O ataque do oráculo auto-declarado (o furo central do desenho atual)

Um asset forjado escolhe o próprio `verify`. Exemplo real do repo:

```json
"verify": [{"check": "cmd_ran", "arg": "$g"}]     // methods/git_commit.json
```

`cmd_ran` passa se `Exit >= 0 && Exit != 126 && Exit != 127` (`methods.go:1709-1715`). Um `git
commit` que falha com exit 1 **passou**. O método declara `succeeded` (`brain.go:363-366`) com
evidência que não confirma nada.

Por que isso é fatal para o portão: se o `EvalFn` do `Gate` rodar `brRun` e ler o status, ele herda
o ponto cego do `verify` do candidato. O candidato controla o próprio oráculo, e o gate vira teatro.
O repo **já acerta** no eval: `eoOne` calcula `FalseSuccess` a partir de `expect` — a expectativa do
*harness*, não do asset (`evaloffline.go:399-406`). A regra que daí se extrai, e que precisa virar
invariante escrita:

> **O avaliador nunca lê o veredito do asset. Ele lê o mundo com o próprio contrato.**

Duas implementações concretas:

1. `GateOpen` recebe `Judge func(root) string` por sonda, escrito no harness (`evals/probes/*.json`),
   nunca derivado de `m.Verify`.
2. Todo candidato carrega `VerifyStrength float64` calculado por mutação (§2.4) **antes** de rodar a
   bateria. `VerifyStrength == 0` ⇒ rejeita, independentemente de sobrevivência. Um asset que não
   consegue reprovar nada não é um asset; é um `report`.

Custo: uma tabela de mutantes por intent (~40 linhas de JSON) + um laço de ~20 linhas. Impacto: alto,
porque fecha a única porta pela qual um asset envenenado entra com métrica limpa.

---

## 3. As cinco ferramentas: o que cada uma prova e o que ela não prova

### 3.1 Metamórfico: relação entre execuções

Chen, Cheung & Yiu, *Metamorphic testing: a new approach for generating next test cases*,
HKUST-CS98-01, 1998 (republicado [arXiv:2002.12543](https://arxiv.org/abs/2002.12543)). A forma:

```
R(x₁,…,xₙ)  ⟹  R_f(f(x₁),…,f(xₙ))
```

**O que prova.** Se a relação falha, há bug. É **som**.
**O que não prova.** Se passa, nada. É **condição necessária, não suficiente** — o próprio paper
diz. Contraexemplo fechado, meu, que vale decorar:

```
f(x) = ordena por |x|        (permutation-invariante, idempotente, preserva tamanho)
MR1: permutação da entrada preserva a saída        → passa
MR2: idempotência  f(f(x)) = f(x)                  → passa
MR3: |f(x)| = |x|                                   → passa
MR4: adjacente  f_i ≤ f_{i+1}  (ordem crescente)    → FALHA   ([-3, 1] → [1, -3])
```

Três MRs "óbvias" de `sort` passam numa função que ordena pela coisa errada. Só a MR que **fixa a
semântica** (ordenação) pega. Ou seja: a completude do conjunto de MRs é equivalente a ter o oráculo
de novo. MR é uma forma barata de *aumentar a chance de refutar*, não de *substituir a
especificação*. Segura, Fraser, Sanchez & Ruiz-Cortés, *A Survey on Metamorphic Testing*, IEEE TSE
42(9):805–824, 2016,
[doi:10.1109/TSE.2016.2532875](https://doi.org/10.1109/TSE.2016.2532875): MRs simples dominam, e a
dificuldade real é **identificar** a MR — trabalho de domínio, não de máquina.

**Composição.** Se as MRs fossem independentes e cada uma pegasse uma fração `d_i` dos bugs, a
detecção com `k` MRs seria `1 - ∏(1-d_i)`. Elas **não são** independentes (compartilham a mesma
leitura do mundo), então isso é **limite superior**, não estimativa. Dizer o contrário é o erro
clássico de multiplicar probabilidades correlacionadas.

**Catálogo que o jazz consegue rodar hoje** (todos observáveis por estado, sem LLM):

| Asset | MR | Custo |
|---|---|---|
| `rename` | `preserves_content(a,b)`: sha256 pós == pré; `gone_iff(a,b)` | <5 ms (hash já no frame) |
| `delete_file` | `gone_iff(a, "")`; segunda execução falha limpo (não silencioso) | <5 ms |
| `create` | `second_run_noop`: rodar de novo não muda estado nem falha feio | 1 passo extra |
| `run_tests` | suíte com falha injetada ⟹ `Failed > 0`; exit 0 com 0 testes ⟹ `unavailable` | 1 execução |
| `git_commit` | `HEAD` mudou; `git log -1 --format=%s` == msg; árvore limpa | ~30 ms |
| `scDef` csv/parse | permutação de linhas preserva agregados; linha duplicada dobra contagem | ~10 ms |

**Custo no Dell.** `second_run_noop` dobra o custo do último passo; habilitar por método, nunca
global (idempotência real depende do mundo — `systemd` flapado reprova a segunda sem culpa). As
demais são leitura de estado já coletada.

### 3.2 Diferencial: concordância não é correção

McKeeman, *Differential testing for software*, Digital Technical Journal 10(1):100–107, 1998:
duas implementações recebem os mesmos casos; **divergência = bug candidato**.

**O que prova.** Divergência ⇒ pelo menos uma está errada. Som.
**O que não prova.** Concordância ⇒ nada. Duas implementações podem errar igual.

Isso não é pessimismo teórico; é medido. Knight & Leveson, *An experimental evaluation of the
assumption of independence in multiversion programming*, IEEE TSE SE-12(1):96–109, 1986,
[doi:10.1109/TSE.1986.6312924](https://doi.org/10.1109/TSE.1986.6312924): **27 versões independentes,
1 milhão de testes, falhas coincidentes substancialmente acima do esperado sob independência**.
Littlewood & Miller, *Conceptual modeling of coincident failures in multiversion software*, IEEE TSE
15(12):1596–1614, 1989,
[doi:10.1109/32.58771](https://doi.org/10.1109/32.58771): a dependência tem estrutura; diversidade
*methodology* ajuda, diversidade de time não.

Portanto o diferencial no jazz só é defensável como **detector**, nunca como **juiz**. Pares úteis
que já existem:

- `script:git_state` (python, parseia `git status --porcelain`) vs `read_cmd "git status --short
  --branch"` — dois caminhos para o mesmo fato;
- `port_owner` por procfs vs `ss`/`netstat`;
- contagem de `opParse` (`outparse.go:47-56`) vs contagem do script `run_tests`.

Divergência nesses pares é **bug real de parser ou de perfil** — valor duplo. Concordância só diz
"dois caminhos que compartilham a mesma suposição concordam". É exatamente o que Knight & Leveson
mostram que falha.

**Custo.** +1 comando read-only (~30–100 ms no Dell, estimativa). Nunca em atalho de <300 ms.

### 3.3 Property-based: a propriedade é `∀`, e o gerador é o oráculo de verdade

QuickCheck: Claessen & Hughes, ICFP 2000,
[doi:10.1145/357766.351266](https://doi.org/10.1145/357766.351266) — propriedade + gerador +
*shrinking*. Hypothesis: MacIver et al., JOSS 4(43), 2019,
[doi:10.21105/joss.01891](https://doi.org/10.21105/joss.01891) — caso como sequência de bytes, redução
integrada à geração.

**O que prova.** Nada além de "não falhou nos `n` casos gerados". A conta é direta: se a região de
falha tem probabilidade `p` sob a distribuição do gerador, a chance de não amostrá-la é `(1-p)^n`.
Para detectar `p = 0.01` com 95% de confiança: `n ≈ ln(0.05)/ln(0.99) ≈ 299`.

**O que isso esconde.** Se o gerador **nunca produz** a região de falha, `n` não ajuda. O oráculo real
é o *suporte* do gerador, não a propriedade. Isso é a versão estatística do Teorema 2. Consequência
de projeto: **a qualidade de uma sonda PBT é a cobertura do gerador, e cobertura de gerador se mede
por mutação do gerador, não por contagem de casos.**

**O que transfere para método.** A forma *stateful/model-based* (Arts, Hughes, Johansson & Wiger,
*Testing telecoms software with Quviq QuickCheck*, Erlang Workshop 2006,
[doi:10.1145/1178049.1178050](https://doi.org/10.1145/1178049.1178050)): o caso é uma **sequência de
operações** e o oráculo é um modelo de estado simplificado rodado em paralelo. É literalmente
`brExecute` + um modelo do filesystem/git. O que **não** transfere é o shrinking: nosso contraexemplo
é um mundo, e minimizar mundo é caro e pouco comprável. O que se aproveita é o princípio "mostre o
mínimo que evidencia a falha" — implementado por **ordenação**, não por busca: reportar a testemunha
com menos arquivos e o menor índice de passo.

**Go.** `testing/quick` é stdlib e congelado: gera e chama a propriedade, **sem shrinking**. Para o
jazz isso basta, porque as propriedades são sobre dados pequenos (JSON de script, linhas de CSV,
args de cap).

### 3.4 Replay em fixture capturada: caracterização, não especificação

Feathers, *Working Effectively with Legacy Code*, Prentice Hall, 2004: characterization test fixa o
comportamento **atual**, não o desejado. É a forma industrial mais honesta de oráculo derivado — não
diz "está certo", diz "mudou".

**O que prova, formalmente.** Um snapshot `s` tirado no tempo `t0` detecta diferença entre
`f_{t0}(x)` e `f_{t1}(x)`. Seu poder contra um bug **presente em `t0`** é **zero** (o snapshot
codifica o bug). Seu poder contra *drift* é total. Essas duas frases são a definição inteira; não há
mais nada a provar.

**A fragilidade é empírica e domina.** Fujita et al., *Snapshot testing in practice*, JSS
204:111797, 2023,
[doi:10.1016/j.jss.2023.111797](https://doi.org/10.1016/j.jss.2023.111797): fácil de criar, bom
contra regressão, **falso-positivo e fragilidade são a queixa número 1**. Estudo de efetividade
recente ([arXiv:2610.29549](https://arxiv.org/abs/2610.29549), 340 repos): snapshot + testes
tradicionais ≈ 86,7% de cobertura mediana e 1,3× mais mutantes mortos — **complementares, não
substitutos**. Byte-exato sem máscara semântica é armadilha; no jazz, hostname/versão/data têm que
ser mascarados ou o oráculo vira flaky.

**A distinção que este doc adiciona: capturada ≠ inventada.** Uma fixture *inventada* codifica as
suposições de quem forjou. Uma fixture *capturada* (de `signals.jsonl`, do JSONL de sessão, de
`results/`) carrega o mundo como ele era. Só a segunda serve para validar generalização. O mecanismo
existe: `eoOne` já monta `brEnv` com `Permit`, `Ask` (respostas gravadas), `BeginTurn`/`AfterWrite`
e `eoFixture` (`evaloffline.go:279-360`, `:411-455`). Replay = chamar `eoOne` com o mesmo fixture e
as mesmas respostas, e comparar o **contrato**, não o texto.

**Custo.** Replay em processo: 5–300 ms por sonda (dominado por shell-out). 20 sondas ≈ 1–5 s no
amd64; no Dell, 2–3× isso. Fora do caminho do pedido.

### 3.5 Sandbox com efeito observado: shadow-git como oráculo de suporte

A infra já existe e é boa: `cpAfterBash` (`checkpoint.go:464`) tira snapshot antes/depois, calcula
`ChangedDetail` (path + blob antes/depois) e registra o conjunto de paths que o turno mudou. É um
oráculo de **estado** de graça.

**O que prova.** `support(Δ) ⊆ Effects` declarado. Isto é o `PASS_TO_PASS` do jazz: "só o declarado
mudou". É **som** para efeito colateral.

**O que não prova.** Intenção. Contraexemplo fechado:

```
rename a b  implementado como:  cp a b ; truncate a
```

- `file_exists(b)` → passa
- `file_gone(a)` → passa
- `support(Δ) = {a, b} ⊆ Effects` → passa
- **conteúdo de `b` foi preservado?** → não. Só a MR `preserves_content` pega.

Ou seja: efeito observado e MR são **ortogonais**, e a interseção das duas é o que dá confiança
mínima. Um portão que só olha `support(Δ)` aprova um asset que destrói dados.

**O detalhe do Dell.** Sondas de *correção* podem rodar na máquina amd64 de forja. Sondas de *custo*
têm que rodar no alvo: um asset que "melhora" o custo no amd64 e regride 3× no Pentium M passa no
gate e piora a vida do usuário. Regra: `cost_ms` medido no Dell (ou num perfil 386 equivalente), e o
gate usa **mediana pareada**, não média — a cauda de shell-out envenena a média.

### 3.6 Consolidado

| Instrumento | Prova (som) | Não prova | Assets que cobre | Custo Dell |
|---|---|---|---|---|
| Metamórfico | falha da relação ⇒ bug | passar ⇒ correto | método, script | <5 ms a 1 passo extra |
| Diferencial | divergência ⇒ ≥1 errado | concordância | script, parser, perfil | +1 comando read-only |
| PBT | falha na amostra | cobertura fora do gerador | script, parser, DSL | ms por caso |
| Replay capturado | mudança de comportamento | correção no momento da captura | método, script | 5–300 ms |
| Shadow-git (suporte) | escrita fora do declarado | intenção/conteúdo | método com escrita | ~0 (já coletado) |
| Mutação do verificador | mutante aceito ⇒ oráculo inseguro | `kill<1` ⇒ fraco | verificador | ~50 ms/mutante |

Nenhuma linha desta tabela prova "correto". Todas provam "errado" em algum aspecto. **É assim que
deve ser, e o relatório tem que dizer isso.**

---

## 4. Por tipo de asset: o que é validável, honestamente

### 4.1 Método HTN (`mhMethod`)

Cadeia de validação, do mais barato ao mais caro:

1. **Screen estático** (custo ~0): `mhBadCmd` (`methods.go:115-122`), caps existem
   (`brCap != nil`), `cbAvailable` no perfil, `brSafePath`, DAG trivial (método é flat, `brain.go:278-280`),
   **e o novo check de literais memorizados** (§2.2).
2. **Replay em fixture capturada** (§3.4): o método roda, o contrato é julgado por sonda externa.
3. **MR do intent** (§3.1): `preserves_content`, `gone_iff`, `second_run_noop`.
4. **Efeito observado** (§3.5): `support(Δ) ⊆ Effects`.
5. **`VerifyStrength`** (§2.4): mutação do `verify` declarado.

O elo mais fraco é o passo 5, porque o `verify` é escolhido pelo candidato. Sem ele, 1–4 validam o
*comportamento observado no fixture* e deixam o candidato declarar sucesso em qualquer desvio.

**O que fica sem resposta:** "o usuário queria isso?". Nenhuma sonda responde. Por isso `needs_input`
e `unverified` não são falhas do sistema; são o estado honesto (`task.go:156-162`).

### 4.2 Script/parser (`scDef`)

É o **melhor caso**, porque entrada e saída são dados:

| Sonda | O que refuta |
|---|---|
| Schema (`Args`, `data` do JSON) | script que devolve forma errada |
| PBT sobre o domínio declarado | crash, não-determinismo, off-by-one |
| Diferencial vs caminho independente | parser que discorda de `git`/`ss`/`unittest` |
| Golden capturado | drift silencioso |
| MR de invariância | `csv_stats` que depende da ordem das linhas; `extract_versions` que duplica |

Mesmo aqui, o buraco é o **fora do domínio**: um parser de `git status` validado em 200 saídas
capturadas não sabe nada sobre a saída do git 3.0. PBT com gerador derivado de gramática ajuda, mas o
gerador é o limite (§3.3). Dizer "validado" para script é sempre "validado no suporte observado".

### 4.3 Verificador (`mhCheck`)

A recursão para aqui. Não há oráculo para o oráculo além de **mutação** (§2.4), e mutação só dá
limite inferior. Portanto:

- verificador forjado **nunca** se auto-certifica;
- entra com `VerifyStrength` medido e revisão humana do diff (o fluxo `improve --promote` já existe);
- `kill == 0` ⇒ rejeição automática, sem discussão.

É o único ponto do sistema onde a resposta honesta é "a confiança vem de revisão humana + mutação, e
não há terceira opção". Não inventar uma.

### 4.4 O que fica `unverified` para sempre

Intenção, gosto, "ficou bom", "o usuário quis assim". Nenhuma das cinco ferramentas toca nisso.
O erro a evitar é o oposto: usar uma métrica forte (kill-rate alto, 100% de MRs) para *sugerir* que
essas dimensões foram validadas. Elas não foram. O relatório deve ter uma seção explícita
`not_covered`.

---

## 5. Arbitragem: quem ganha quando dois assets casam o mesmo pedido

### 5.1 O que o código faz hoje

| Peça | Comportamento | Problema |
|---|---|---|
| `mhLibrary` | ordena por `Priority` desc, estável (`methods.go:107`) | prioridade é estática, escrita à mão, sem contexto |
| `brExecute` | primeiro aplicável; em falha, `next` (`brain.go:268-357`) | ordem não olha custo nem histórico |
| `plancache` | `score *= 1 + 0.1*min(wins,5)` (`plancache.go:292`); evicta em `fails ≥ 2` (`:418`) | bônus satura em 5; sem limite inferior; sem custo |
| `lessons_reuse` | `lrPick` ordena por `wins - fails`; P5 do doc irmão propõe LB | diferença bruta é ruído com `n` pequeno |

Nenhum desses é *errado*; todos são **subespecificados**. O que segue é o que a matemática permite
dizer com precisão.

### 5.2 A ordem ótima de uma cascata é `p/c` — e isso é teorema

Modele a execução como: tenta `m_1`; se verificado, para; se falha, tenta `m_2`; etc. Cada `m_i`
sucede com probabilidade `p_i` e custa `c_i > 0`, independente dos anteriores.

**Teorema 3 (troca de adjacentes).** Para um conjunto fixo, a ordem que minimiza o custo esperado
até o primeiro sucesso é `p_i / c_i` decrescente.

*Prova.* Considere duas ordens que diferem só na posição de `i` e `j`, com o resto `R` depois dos
dois. Custo esperado de `(i,j,R)`:

```
E_ij = c_i + (1-p_i)·c_j + (1-p_i)(1-p_j)·E[R]
E_ji = c_j + (1-p_j)·c_i + (1-p_i)(1-p_j)·E[R]
E_ij - E_ji = c_i + c_j - p_i c_j - c_j - c_i + p_j c_i = p_j c_i - p_i c_j
```

`E_ij ≤ E_ji ⇔ p_i c_j ≥ p_j c_i ⇔ p_i/c_i ≥ p_j/c_j`. Como trocas de adjacentes geram todas as
permutações, a ordem `p/c` decrescente é ótima. ∎

É a mesma forma da regra de Smith, *Various optimizers for single-stage production*, Naval Research
Logistics Quarterly 3(1-2):59–66, 1956,
[doi:10.1002/nav.3800030106](https://doi.org/10.1002/nav.3800030106) (razão entre ganho e custo,
provada por troca de adjacentes), aqui derivada para tentativas que podem falhar.

**Hipóteses, e onde elas quebram no jazz:**

- *Independência.* Falha de `m_1` pode mudar o mundo (escreveu pela metade), então `p_2 | falha_1 ≠
  p_2`. Para métodos **read-only** o teorema é exato; para escrita é heurística. Isso justifica a
  regra do doc irmão "replay read-only primeiro" (`plan-reuse-deep.md` P6) — e agora com prova.
- *Custo aditivo.* Se a falha de `m_1` causa dano (escreveu errado), o custo efetivo é
  `c_i + (1-p_i)·damage_i` — o dano entra no **denominador** da razão `p/c`. Um método com `p` alto e
  dano alto pode perder para um com `p` menor e zero dano.
- *`p` conhecido.* Não é. Usa-se estimativa conservadora (§5.4).

**O que isso implica para o código:** `Priority` não pode ser o rank. Ela é, na melhor das hipóteses,
uma estimativa grosseira de `p/c` feita pelo autor. O lugar certo dela é **como prior** (§5.3).

### 5.3 Prioridade como prior, não como chave

Modelo Beta-Bernoulli: `p ~ Beta(α₀, β₀)`, observações Bernoulli. Posterior: `Beta(α₀+w, β₀+f)`.
Média posterior:

```
p̂ = (w + α₀) / (n + α₀ + β₀),   n = w + f
```

Mapeie a prioridade do autor em `(α₀, β₀)`, com força total pequena para o dado dominar rápido. Por
exemplo, `α₀ = 4·prio/11`, `β₀ = 4 - α₀` (força 4): `priority: 10` começa em `p̂ ≈ 0.91`;
`priority: 1` em `≈ 0.09`. Depois o dado manda — com força 4, quatro observações já empatam a prior.

**Lema 4 (a prior é esquecida a `O(1/n)`).**

```
|p̂ - w/n| = |α₀(n-w) - β₀w| / (n(n+α₀+β₀))  ≤  max(α₀,β₀) / (n+α₀+β₀)
```

*Prova.* Álgebra direta; `0 ≤ w ≤ n`. ∎

Ou seja: com `max(α₀,β₀)=2`, em `n=20` a distância entre a média posterior e a empírica é ≤
`2/23 ≈ 0.087`; em `n=200`, ≤ `0.0099`. **A prioridade do autor deixa de importar na proporção em
que o asset é usado.** Isso resolve o conflito "prioridade estática vs win-rate medido" sem hack: não
é escolher um dos dois, é o teorema de Bayes fazendo a transição.

### 5.4 LCB, não UCB — e por que `z = 0.7` não é intervalo de confiança

O custo é assimétrico: `false_success` é catastrófico (o usuário deixa de confiar), um comando extra
é barato. A decisão é **maximizar utilidade sob restrição de segurança**, não explorar. Nesse regime
o estimador certo é o **limite inferior**.

Hoeffding 1963, *Probability inequalities for sums of bounded random variables*, JASA 58(301):13–30,
[doi:10.1080/01621459.1963.10500830](https://doi.org/10.1080/01621459.1963.10500830):

```
P(p̂ - p ≥ ε) ≤ exp(-2nε²)   ⟹   p ≥ p̂ - sqrt(ln(1/δ) / (2n))  com prob. ≥ 1-δ
```

Wilson 1927, *Probable inference, the law of succession, and statistical inference*, JASA
22(158):209–212,
[doi:10.1080/01621459.1927.10502953](https://doi.org/10.1080/01621459.1927.10502953), dá o intervalo
de placar mais justo para `n` pequeno:

```
centro = (p̂ + z²/(2n)) / (1 + z²/n)
meia   = z/(1+z²/n) · sqrt( p̂(1-p̂)/n + z²/(4n²) )
LCB    = centro - meia
```

**Honestidade sobre o doc irmão:** `plan-reuse-deep.md` P5 usa `z = 0.7` e chama de "limite inferior
estilo Wilson". `z = 0.7` corresponde a ~76% de confiança unilateral (`α ≈ 0.24`) — **não é um
intervalo de confiança de 95%**, é uma penalização de encolhimento. Isso pode ser uma escolha
defensável (cache pequeno, custo de matar um bom cedo), mas o nome tem que ser `shrinkage`, não
`LB`. Para uma garantia de verdade: `z = 1.28` (90%) ou `z = 1.645` (95%). Recomendo `z = 1.28` no
gate e `z = 0.7` só no ranking de UI, com o nome certo em cada lugar.

**A restrição não entra na soma.** `false_success == 0` é uma **restrição**, não um termo com peso.
Isso é decisão sob restrição (Altman, *Constrained Markov Decision Processes*, Chapman & Hall, 1999):
o conjunto viável é `{a : P(false_success | a) = 0}` e a otimização é dentro dele. Ordem
lexicográfica, não soma ponderada — e por isso `Gate` já está certo ao bloquear
(`forge_gate.go:155-157`) em vez de penalizar. A regra é boa e deve ser preservada em `GateOpen`.

**Onde entra exploração (e por que LCB não congela o sistema).** Um asset novo tem `LCB` baixo e
nunca seria escolhido — cold start. A resposta não é trocar LCB por UCB (que aposta no desconhecido
justamente onde o custo do erro é alto). É **explorar onde errar é reversível**: método read-only,
replay em shadow (`Grade B`, sem efeito), ou pedido explícito do usuário. Para escrita, o asset novo
só entra depois de promoção no gate. Isso mantém a garantia e ainda dá dados: `Uses` cresce nos
contextos seguros, e o `LCB` sobe sozinho quando o asset prova ali. Sem essa separação, "LCB" vira
"nunca experimente", que é o outro extremo errado.

### 5.5 Especificidade é condicionamento, não qualidade

Um método mais específico casa um subconjunto menor de pedidos. Sua taxa de vitória é
`P(sucesso | intent, pre_i)`, uma probabilidade **condicionada a um evento diferente**. Comparar
`P(sucesso|pre_A)` com `P(sucesso|pre_B)` e escolher o maior é o erro de Simpson (Simpson, *The
interpretation of interaction in contingency tables*, JRSS-B 13(2):238–241, 1951,
[doi:10.1111/j.2517-6161.1951.tb00088.x](https://doi.org/10.1111/j.2517-6161.1951.tb00088.x)): a
marginal pode inverter a ordem das condicionais.

**Consequência de esquema:** as estatísticas têm que ser chaveadas por
`(intent, preSignature, shape)`, não por `intent`. `preSignature` = hash ordenado dos pares
`(Fact, Op, Val)` de `m.Pre` + `m.Needs`; `shape` = tipos de slot ordenados (o doc irmão já propõe
`Shape` em `piPlan`). Sem isso, um método específico herda a média de um genérico e o número mente.

**E o `n=1`?** Não precisa de regra especial. O LCB resolve: um método com 1 vitória e 0 falhas tem
`Beta(2,1)`, média 0.667, desvio ~0.24; com `z=1.28`, `LCB ≈ 0.36`. Perde para um genérico com 45/50
(`Beta(46,6)`, `LCB ≈ 0.83`). A "especificidade" só desempata **depois** que o LCB empatou — e aí ela
é um sinal legítimo de *matching*, não de qualidade.

### 5.6 Recência: decaimento é uma aposta em deriva

**Lema 5 (pesos iguais minimizam variância).** Para observações independentes com variância `σ²` e
pesos convexos `w_i` (`Σw_i = 1`), `Var(Σ w_i x_i) = σ² Σ w_i² ≥ σ²/n`, com igualdade sse
`w_i = 1/n`.

*Prova.* Cauchy–Schwarz: `(Σ w_i·1)² ≤ (Σ w_i²)(Σ 1) = n Σ w_i²`; como `Σw_i = 1`, `Σw_i² ≥ 1/n`. ∎

Logo, sob **estacionariedade**, qualquer decaimento por idade (`0.5^(age/45)`, P5 do doc irmão)
**aumenta** a variância do estimador e joga dados fora. Só compensa se `p` muda com o tempo. Deriva
existe (versões de ferramenta, providers, repos): Gama, Žliobaitė, Bifet, Pechenizkiy & Bouchachia,
*A survey on concept drift adaptation*, ACM Computing Surveys 46(4):44, 2014,
[doi:10.1145/2523813](https://doi.org/10.1145/2523813). Mas **assumir deriva sempre** é pagar variância
em todo lugar para cobrir um evento raro.

**A alternativa com matemática:** detectar deriva, não cronometrá-la. CUSUM (Page, *Continuous
inspection schemes*, Biometrika 41(1-2):100–115, 1954,
[doi:10.1093/biomet/41.1-2.100](https://doi.org/10.1093/biomet/41.1-2.100)):

```
S_t = max(0, S_{t-1} + (x_t - μ₀) - k)      // x_t = 1 se falhou; μ₀ = taxa de falha esperada
```

Três inteiros por asset. Sob estacionariedade, `S_t` fica perto de 0 e **nenhum dado é descartado**.
Se o asset piora (`μ₁ > μ₀`), `S_t` cresce à taxa `(μ₁-μ₀)-k` e dispara em ~`h/((μ₁-μ₀)-k)`
observações. No alarme, zera `wins/fails` e marca `regime++`. Isso é estritamente melhor que decair
por relógio: mantém o `n` grande quando o mundo está parado, e reage rápido quando não está. Custo:
~10 linhas e 3 `int` por asset.

### 5.7 A ordem final (lexicográfica, e por quê)

```
rank(m | x) = ( 1 if screen(m) passes else 0,         // segurança: barreira dura
                1 if falseSuccess(m) == 0 else 0,      // histórico de mentira
                LCB(p̂_m, z=1.28),                      // qualidade estimada
                specificity(m, x),                     // matching (desempate)
                -costMS(m),                            // custo
                lastUsed(m),                           // recência (desempate)
                m.Name )                               // determinismo
```

Ordenação **decrescente** nessa tupla: o primeiro componente é 1 só para quem passou no screen; o
segundo, 1 só para quem nunca mentiu; o resto compara como número. Comparação lexicográfica, do
primeiro componente para o último. **Prova de que é ordem total e determinística:** cada componente é
um valor numérico ou string totalmente ordenado; a comparação lexicográfica de tuplas sobre ordens
totais é uma ordem total (propriedade padrão de ordem lexicográfica); o último componente (`m.Name`)
é único por biblioteca, então não há empate. Logo o `sort` é determinístico e não tem ciclo. ∎ (Prova
trivial, mas necessária: o código hoje depende de `SliceStable` com `Priority` empatada —
`methods.go:107` — e um comparador não-total produz ordem dependente do input.)

**Por que não colapsar num score só.** Uma ordem lexicográfica sobre um produto com duas dimensões
não-triviais **não admite representação por uma função de utilidade real** (ela não é separável nem
tem ordem densa) — é o resultado clássico resenhado por Fishburn, *Lexicographic orders, utilities
and decision rules: a survey*, Management Science 20(11):1442–1471, 1974,
[doi:10.1287/mnsc.20.11.1442](https://doi.org/10.1287/mnsc.20.11.1442). Qualquer soma ponderada
`Σ λ_i f_i` aceita trocar segurança por velocidade para algum `λ`; a ordem lexicográfica não. Como a
segurança é restrição (§5.4), **o comparador tem que ficar separado** — é exatamente o que `Gate` faz
com `false_success` e o que um "score único" desfaria.

**Arbitragem entre tipos.** Replay read-only, método, script, plano em cache e modelo podem casar o
mesmo pedido. A ordem entre tipos é por **classe de risco** primeiro (`read` < `write` < `exec` <
`net`), depois pelo mesmo `rank` dentro da classe. Justificativa: o custo do erro não é comparável
entre classes, então `p/c` entre classes compararia coisas diferentes — o mesmo argumento de
condicionamento de §5.5.

### 5.8 A métrica da arbitragem: regret, não acurácia

Com uma sequência de pedidos e resultados conhecidos (o ledger de `signals.jsonl` + stats), a
política de arbitragem se avalia por **arrependimento**:

```
regret = Σ_t [ u(best_t | x_t) - u(chosen_t | x_t) ]
```

Isso é o enquadramento padrão de bandit (Auer, Cesa-Bianchi & Fischer, *Finite-time analysis of the
multiarmed bandit problem*, Machine Learning 47:235–256, 2002,
[doi:10.1023/A:1013689704352](https://doi.org/10.1023/A:1013689704352)) e é medível offline, sem
rodar nada novo: cada linha do ledger já diz qual método foi escolhido e se verificou. Um `regret`
que não cai depois de 200 pedidos significa que a arbitragem não está aprendendo — e aí o problema é
o esquema de chaves (§5.5), não o `z`.

---

## 6. Aterrissagem no código

### 6.0 Mapa file:line

| Peça | file:line | Status | Nota |
|---|---|---|---|
| `Gate` (comparação held-in/held-out) | `forge_gate.go:143` | **feito** | motor puro, **sem chamador em produção** |
| `Split` determinístico estratificado | `forge_gate.go:92-126` | **feito** | reusar como `SplitProbes` |
| `splitHash` FNV-1a de `(seed, ID)` | `forge_gate.go:72-79` | **feito** | primitivo do split |
| `gateEps = 0.01` | `forge_gate.go:34` | **parcial** | ruído contínuo; inútil para contagem de refutações |
| `Example`/`Metrics`/`EvalFn` | `forge_gate.go:38-56` | **feito** | abstração só de classificação (`Want`, `Accuracy`) |
| `mhMethod` (`Pre`/`Steps`/`Verify`/`Needs`/`Cmds`) | `methods.go:60-72` | **feito** | unidade do asset de método |
| `mhCond` com `op:"eq"` e literal | `methods.go:42-46`, `:133-134` | **feito** | **canal de memorização** (§2.2) |
| `mhLibrary` ordena por `Priority` | `methods.go:105-108` | **feito** | ordem estática, sem contexto |
| `mhBadCmd` (allowlist de comando) | `methods.go:115-122` | **feito** | screen barato |
| `brExecute` (primeiro aplicável, `next`) | `brain.go:261-357` | **feito** | cascata; ordem não olha `p/c` |
| `brVerify` (10 checks, conjunção plana) | `methods.go:1680-1763` | **feito** | sem frame, sem relação, primeiro-falha |
| `Capability.Effects` / `.Verifier` | `caps.go:74-76` | **parcial** | `Verifier` existe e **não é avaliado** |
| `cbReadCmds` / `cbReadCmd` | `caps.go:567-600` | **feito** | superfície read-only fechada |
| shadow-git (`cpAfterBash`, `ChangedDetail`) | `checkpoint.go:464-500` | **feito** | oráculo de suporte de graça |
| `succeeded` exige evidência | `task.go:156-162` | **feito** | a lei fundamental |
| `false_success` medido pelo harness | `evaloffline.go:399-406` | **feito** | **precedente correto**: não lê o asset |
| `eoOne` (env + fixture + respostas) | `evaloffline.go:279-360`, `:411-455` | **feito** | reusar como runner de sonda |
| ledger de sinais | `signals.go:52-107` | **feito** | fonte de fixtures capturadas |
| `Probe`/`Witness`/`GateOpen` | — | **ausente** | §6.1 |
| `AssetStat`/`mhRank` | — | **ausente** | §6.2 |
| `VerifyStrength` (mutação do verificador) | — | **ausente** | §2.4, §6.3 |
| `evals/probes/*.json` | — | **ausente** | §6.4 |
| anti-memorização de `mhCond` | — | **ausente** | §2.2, V4 |

Resumo do mapa: a infraestrutura de *observação* (shadow-git, evidência, `eoOne`, `signals`) está
pronta e é boa; o que falta é a **camada de decisão sobre asset de final aberto** — `Probe`,
`GateOpen`, `AssetStat` e a força do verificador. Nada disso exige dependência nova.

### 6.1 `forge_gate.go`: `Probe`, `GateOpen`, e a regra lexicográfica

O arquivo continua com `Gate` (classificação, intocado) e ganha o irmão. Esboço, stdlib apenas:

```go
// Probe is one attempt to refute an asset. It has no Want: it builds a world,
// lets the asset act, and judges the world. Judge returns "" when the asset
// survived, or a one-line witness.
type Probe struct {
	ID     string
	Kind   string // mr | diff | pbt | replay | effect | mutant
	Group  string // repo_sig or fixture family: the split unit, not the intent
	CostMS int
	Setup  func(root string) error
	Run    func(ctx context.Context, root string, a Asset) error
	Judge  func(ctx context.Context, root string, a Asset) string
}

type Asset interface {
	ID() string
	// Contract is the harness-owned postcondition. It must NOT be the
	// asset's own Verify: that is the self-declared-oracle attack (§2.5).
	Contract() []mhCheck
}

type OpenMetrics struct {
	Run, Survived, Refuted, Unavail int
	CostMS                          int
	Witness                         []string // capped, smallest first
}

type OpenReport struct {
	Accept  bool
	Reason  string
	Base    OpenMetrics
	Cand    OpenMetrics
	Refuted []string // candidate witnesses the baseline did not produce
}
```

Regra de decisão, em ordem (todas bloqueantes):

```go
func GateOpen(probes []Probe, base, cand Asset, budgetMS int) (bool, OpenReport) {
	// 1. split estratificado por Group, reusando splitHash (forge_gate.go:72)
	in, out := SplitProbes(probes, 0.40, 42) // 0.40: com 20 sondas, 8 held-out (o piso)
	// 2. degenerado: menos de 8 sondas held-out não sustenta decisão (§6.4)
	// 3. refutação nova: qualquer witness que o baseline não produziu -> rejeita
	// 4. VerifyStrength(cand) == 0 -> rejeita (o asset não reprova nada)
	// 5. custo: mediana pareada de cand > base*(1+slack) -> rejeita (slack=0.15)
	// 6. sem melhoria estrita (sobrevivência ou custo) -> rejeita
	// 7. aceita: "não refutado em k/k sondas (h held-out), custo -x ms"
}
```

Três decisões de desenho que precisam de justificativa escrita no código:

- **Sem `eps`.** `gateEps` (`:34`) é ruído contínuo. Aqui o evento é discreto; a tolerância é
  `unavailable`, não um epsilon. Uma sonda que não rodou não é sobrevivência.
- **`Coverage` redefinido para asset de final aberto.** `VerifyBreadth(m) = |O ∩ D| / |D|`, onde `D`
  são as dimensões que o contrato declara mutáveis (`Effects` dos caps + paths citados) e `O` são as
  que o verificador realmente lê. É estático, custa ~0, e dá sentido ao termo "melhorou cobertura"
  que hoje é um número inventado pelo `EvalFn`. Só conta como melhoria se `unavailable` e
  `false_blocks` não subirem.
- **`SplitProbes` reusa `splitHash`.** Mesma função, mesma garantia de determinismo (`:72-79`). Não
  duplicar o hash.

Testes que o `GateOpen` precisa (todos em processo, sem rede):

1. candidato com 1 refutação nova ⇒ rejeita, e o motivo cita a testemunha;
2. candidato sem refutação e 20% mais rápido ⇒ aceita;
3. candidato sem refutação e 20% mais lento ⇒ rejeita;
4. sonda `unavailable` no candidato e não no baseline ⇒ não conta como sobrevivência;
5. `VerifyStrength == 0` ⇒ rejeita mesmo com 0 refutações;
6. split degenerado (6 sondas) ⇒ rejeita com motivo explícito;
7. determinismo: mesma seed ⇒ mesmo relatório;
8. baseline refutado e candidato também ⇒ rejeita (não é melhoria; é o mesmo buraco).

### 6.2 `methods.go` + `brain.go`: ranking por estatística, não por prioridade

```go
// AssetStat is the evidence for one asset in one match class.
type AssetStat struct {
	ID    string
	Class string // intent|preSig|shape  (§5.5)
	Wins, Fails, Uses int // Uses = decisões resolvidas (verificado ou falhou)
	CostMS int
	LastTS int64
	PriorA, PriorB float64 // from mhMethod.Priority (§5.3)
}

func statMean(s AssetStat) float64 { return (float64(s.Wins) + s.PriorA) / (float64(s.Uses) + s.PriorA + s.PriorB) }
func statVar(s AssetStat) float64  { m := statMean(s); return m * (1 - m) / (float64(s.Uses) + s.PriorA + s.PriorB + 1) }
func statLCB(s AssetStat, z float64) float64 { return statMean(s) - z*math.Sqrt(statVar(s)) }
```

Ponto de inserção: `brRunIntent` (`brain.go:152-153`) carrega `lib[in.Name]` e hoje usa a ordem do
loader. Passa a:

```go
methods := mhRank(lib[in.Name], statsFor(in.Name, in.Slots)) // cópia ordenada
```

**Bug a evitar:** `lib[in.Name]` é o slice do cache global (`mhOnce`, `methods.go:74-78`). Ordenar
in-place muta estado compartilhado entre pedidos e goroutines. `mhRank` **tem que copiar** antes de
`sort.Slice`. Isso não é teórico: `mhLibrary` já ordena uma vez no loader (`:105-108`) e a fatia é
reusada em todo pedido.

Custo no Dell: `n ≤ 8` métodos por intent, um `sqrt` cada → microssegundos. Um `sort.Slice` de 8
elementos é ~200 ns. RSS: `AssetStat` ~64 B × 50 intents × 8 ≈ 25 KB, carregado uma vez.

### 6.3 `brVerify`: frame + relação + força medida

O avaliador (`methods.go:1680-1763`) hoje é conjunção plana, primeiro-falha, sem pré-estado. As
extensões de DSL (frame, `preserves_content`, `gone_iff`) já estão especificadas em
`verification-oracles-deep.md` P1/P3 — **não repito**. O que este doc adiciona é o que fecha o ciclo
de validação:

1. **`VerifyStrength` como campo do método** (não do runtime): calculado em forja por mutação,
   gravado no JSON ao lado de `verify`. O `GateOpen` lê. O runtime não calcula nada.
2. **`brVerify` não pode sozinho levar a `succeeded`** um asset forjado com `VerifyStrength < floor`
   (sugestão: 0.6 em mutantes não-equivalentes). Ele devolve `ok=false, unavailable=true` e o
   chamador mapeia para `unverified` (`brain.go:364-370`) — o estado honesto (`task.go:160-162`).
3. **Contrato separado do `verify`.** O harness julga; o asset declara. Se os dois coincidem, é
   coincidência, não certificação.

### 6.4 Runner de sondas: reusar `eoOne`, não inventar harness

`eoOne` (`evaloffline.go:279-360`) já monta `brEnv` com `Root`, `Permit`, `Ask` com respostas
gravadas, `BeginTurn`/`AfterWrite`, e `eoFixture` (`:411-455`) já cria o mundo. O runner de sonda é
uma variação de ~60 linhas: em vez de comparar `r.Text` com `expect.contains`, ele chama
`probe.Judge(root, asset)` depois do `brRun`. Nada de novo processo, nada de rede.

**Tamanho mínimo da bateria held-out.** União-limite: se um asset errado passa numa sonda com
probabilidade `q`, a chance de passar em `k` é `q^k`; com `N` candidatos, `P(algum passa) ≤ N·q^k`.
Para `≤ α`:

```
k ≥ ln(N/α) / ln(1/q)
```

`q = 1/2`, `N = 1`, `α = 0.05` → `k ≥ 5`. `N = 20` → `k ≥ 9`. **Recomendação: held-out ≥
max(8, ceil(ln(N/0.05)/ln 2))**, e nunca menos de 2 por `Group`. Isso é piso sob independência; como
as sondas são correlacionadas, trate como piso e prefira 12–16.

**Onde capturar as fixtures.** `signals.jsonl` (`signals.go:52-78`) já registra `miss`,
`correction`, `verifier_missing`, com `Text`, `Chosen`, `RepoSig`. Um `jazz forge capture` extrai
`(prompt, fixture fingerprint, answers, post-state)` desses sinais e de `results/offline-*.json`.
Fixture inventada não vale (§3.4).

---

## 7. Propostas (Go, Dell, stdlib)

Impacto 1–5; esforço S/M/L; risco baixo/médio/alto. Métricas no vocabulário de `eoSummary`
(`evaloffline.go:91-107`) + as novas: `refutations`, `verify_kill_rate`, `survival_heldout`,
`arbitration_regret`.

| # | Proposta | Mecanismo | Arquivos | Custo 32-bit | Métrica | Impacto | Esforço | Risco |
|---|---|---|---|---|---|---|---|---|
| **V1** | `GateOpen` + `Probe`/`Witness` | split de sondas, refutação nova bloqueia, custo pareado | `forge_gate.go` (~180 ln), `forge_gate_test.go` | forge-only; ~1–5 s por asset | `refutations` held-out, `survival_heldout` | **5** | M | baixo |
| **V2** | Bateria de sondas por tipo + mutantes de verificador | `evals/probes/*.json` + gerador; `kill(V,M)` comparativo | `probes.go`, `evals/probes/`, `methods/*.json` (+`verify_strength`) | ~50 ms/mutante; 10×20 ≈ 10 s/asset em forja | `verify_kill_rate` (piso 0, alvo ≥0.6 relativo) | **5** | M | baixo-médio |
| **V3** | Defesa do oráculo auto-declarado | avaliador nunca lê `m.Verify`; rejeita `VerifyStrength==0` | `forge_gate.go`, `evaloffline.go` (novo judge) | 0 em runtime | `false_success` (deve ficar 0) | **5** | S–M | baixo |
| **V4** | Anti-memorização de `mhCond` | rejeita `op:eq` com literal presente no corpus de forja; fixtures com path aleatório | `methods.go` (screen), `forge_screen` | 0 runtime; O(#conds) em forja | métodos rejeitados por literal | **4** | S | baixo |
| **V5** | `AssetStat` + `mhRank` | prioridade como `Beta(α₀,β₀)`; LCB `z=1.28`; custo | `methods.go` (`mhRank`), `brain.go:152` | µs/pedido; ~25 KB RSS | `arbitration_regret`, `stale_pick_rate` | **4** | S | baixo |
| **V6** | Chave de estatística `(intent,preSig,shape)` | evita Simpson; `shape` dos slots | `methods.go`, `lessons_reuse.go` | hash O(#conds) no lookup | regret por classe; win-rate por subpop | **4** | S | baixo |
| **V7** | CUSUM em vez de decaimento por idade | `S_t` por asset; reset no alarme | `methods.go` (`AssetStat`), `plancache.go` (mesma ideia) | 3 `int`/asset; O(1) | detecção de deriva vs variância do estimador | **3** | S | baixo |
| **V8** | Sondas de efeito via shadow-git | `support(Δ) ⊆ Effects` como judge | `checkpoint.go` (expor `ChangedDetail`), `probes.go` | ~0 (já coletado) | `collateral_violations` | **4** | S–M | médio |
| **V9** | Diferencial de scripts | pares `git_state`/`read_cmd`, `port_owner`/`ss`, `opParse`/script | `probes.go`, `methods/*.json` | +1 read-only (~30–100 ms) | divergências (cada uma é bug real) | **3** | M | médio |
| **V10** | `jazz forge report` com ledger de refutação | refutações por asset/sonda; regret; cobertura da bateria | `forge.go` (subcomando), `signals.go` | desprezível | qualidade da decisão de roadmap | **2** | S | nulo |

**Ordem de ataque:** V3 → V1 → V2 → V4 (fecha o furo, depois mede) → V5 → V6 → V8 → V7 → V9 → V10.
V3 antes de V1 de propósito: de nada adianta um portão novo se o avaliador herda o `verify` do
candidato.

**Critérios de parada (alvos, a confirmar em medição):** `false_success` permanece 0;
`refutations` held-out = 0 nos promovidos; `verify_kill_rate` do promovido ≥ o do pai na mesma `M`;
`arbitration_regret` cai monotonicamente em 200 pedidos; p50 de pedido com ranking não sobe >2 ms no
Dell.

---

## 8. O que NÃO fazer

1. **Não preencher `Example.Want` com a saída do baseline nem do LLM.** O primeiro vira teste de
   igualdade ao pai (rejeita toda melhoria); o segundo vira imitação, que é a dependência que a
   filosofia quer eliminar. Se não há `Want`, não use `Gate` — use `GateOpen`.
2. **Não deixar o asset declarar o próprio oráculo.** `verify: cmd_ran` passa com exit 1. O
   avaliador lê o mundo com contrato próprio (`evaloffline.go:399-406` é o precedente correto).
3. **Não usar LLM-as-judge no portão.** Nem para "desempatar". É dependência no caminho crítico e,
   pior, um avaliador que o candidato pode influenciar por texto.
4. **Não tratar `kill < 1` como prova de fraqueza.** Mutantes equivalentes existem e a equivalência
   é indecidível (Rice 1953). Kill-rate é comparativo e piso, não nota.
5. **Não tratar concordância diferencial como correção.** Knight & Leveson 1986: 27 versões
   independentes falham juntas muito acima da independência. Divergência é sinal; concordância é
   silêncio.
6. **Não somar as MRs como se fossem independentes.** `1-∏(1-d_i)` é limite superior; elas
   compartilham a mesma leitura do mundo.
7. **Não usar snapshot byte-exato sem máscara.** Fujita 2023: fragilidade/falso-positivo é a queixa
   dominante. Hostname, versão e data têm que ser mascarados, ou o oráculo vira flaky e o usuário
   aprende a ignorar `unverified`.
8. **Não ordenar por `Priority`.** Prioridade é prior Bayesiana, não rank. A ordem ótima da cascata é
   `p/c` (Smith 1956); prioridade estática ignora custo e contexto.
9. **Não usar decaimento por idade como default.** Sob estacionariedade ele só aumenta variância
   (Lema 5). Detecte deriva com CUSUM; não a presuma com relógio.
10. **Não colapsar segurança num score.** `false_success = 0` é restrição, não termo. Ordem
    lexicográfica não é representável por utilidade real (Fishburn 1974) — e é por isso que ela
    protege.
11. **Não rodar PBT/mutação no caminho do pedido.** Tudo isso é forja (amd64). O Dell paga só o
    lookup (~µs) e o frame (stat+sha256, <20 ms típico).
12. **Não medir custo no amd64.** Um asset que "melhora" no amd64 e regride 3× no Pentium M passa no
    gate e piora a vida do usuário. `cost_ms` no alvo, mediana pareada.

---

## 9. Veredito e lacunas honestas

**O que este documento estabelece com prova:**

- `Accuracy`/`Example.Want` não têm referente para asset de final aberto; o *split* sobrevive, a
  métrica não (§1, §1.2).
- Composição de verificador não aumenta poder de observação; só folha nova aumenta (Teorema 1).
- A ordem ótima de uma cascata de tentativas é `p/c` (Teorema 3, troca de adjacentes).
- A prioridade do autor é esquecida a `O(1/n)` sob o modelo Beta-Bernoulli (Lema 4).
- Pesos desiguais (recência) só aumentam variância sob estacionariedade (Lema 5).
- O `verify` declarado pelo asset é um canal de ataque concreto, com exemplo no repo (`cmd_ran`).
- `mhCond op:eq` é um canal de memorização concreto, e o detector existente cobre código, não
  métodos.

**O que não tem resposta boa, e eu não vou fingir que tem:**

1. **"Este asset está correto?"** Não é decidível em geral (Rice 1953) e não é estimável sem uma
   distribuição de mundos que não temos. A resposta honesta é "não refutado em `k` sondas", e o
   número `k` é uma escolha de engenharia, não um teorema.
2. **Quantas sondas bastam?** A fórmula `k ≥ ln(N/α)/ln(1/q)` depende de `q`, que ninguém conhece, e
   assume independência que as sondas não têm. É um piso, não uma garantia.
3. **Como validar intenção?** Não dá. Nenhuma das cinco ferramentas toca "o usuário queria isso".
   `needs_input` é a resposta, e ela é correta.
4. **Qual o `ρ` (correlação intra-repo) real?** Não medido. O exemplo `ρ=0.3` é ilustrativo. Medir
   exige rodar a bateria em ≥10 repos e decompor a variância — trabalho de eval, não de paper.
5. **`kill-rate` de quanto é "bom"?** Não há número universal. A literatura de mutação usa
   comparative adequacy, não absoluto (Papadakis et al., *Mutation testing advances: an analysis and
   survey*, Advances in Computers 112:271–328, 2019,
   [doi:10.1016/bs.adcom.2018.03.015](https://doi.org/10.1016/bs.adcom.2018.03.015)). O gate deve
   comparar candidato vs pai, e usar 0 como piso.

**A frase honesta para o README:** *"o forge não prova que um asset está certo; ele tenta refutá-lo
com uma bateria cujo poder é medido, sob um avaliador que o asset não controla, e só promove o que
sobrevive e é mais barato — depois de o verificador do próprio asset ter sido mutado."* É menos
vendável que "validação held-out", e é verdadeiro.

---

## 10. Fontes

**Oráculo e teste**
- Barr, Harman, McMinn, Shahbaz & Yoo — *The Oracle Problem in Software Testing: A Survey*. IEEE TSE 41(5):507–525, 2015. [doi:10.1109/TSE.2014.2372785](https://doi.org/10.1109/TSE.2014.2372785)
- Weyuker — *On Testing Non-Testable Programs*. The Computer Journal 25(4):465–470, 1982. [doi:10.1093/comjnl/25.4.465](https://doi.org/10.1093/comjnl/25.4.465)
- Dijkstra — *The Humble Programmer* (EWD340). CACM 15(10):859–866, 1972. [doi:10.1145/355604.361591](https://doi.org/10.1145/355604.361591)
- Rice — *Classes of recursively enumerable sets and their decision problems*. TAMS 74:358–366, 1953. [doi:10.1090/S0002-9947-1953-0053041-6](https://doi.org/10.1090/S0002-9947-1953-0053041-6)
- Popper — *Conjectures and Refutations*. Routledge, 1963.
- De Millo, Lipton & Perlis — *Social processes and proofs of theorems and programs*. CACM 22(5):271–280, 1979. [doi:10.1145/359104.359106](https://doi.org/10.1145/359104.359106)

**Metamórfico e diferencial**
- Chen, Cheung & Yiu — *Metamorphic testing: a new approach for generating next test cases*. HKUST-CS98-01, 1998. [arXiv:2002.12543](https://arxiv.org/abs/2002.12543)
- Segura, Fraser, Sanchez & Ruiz-Cortés — *A Survey on Metamorphic Testing*. IEEE TSE 42(9):805–824, 2016. [doi:10.1109/TSE.2016.2532875](https://doi.org/10.1109/TSE.2016.2532875)
- Chen, Kuo, Liu, Poon, Towey, Tse & Zhou — *Metamorphic Testing: A Review of Challenges and Opportunities*. ACM CSUR 51(1):4, 2018. [doi:10.1145/3143561](https://doi.org/10.1145/3143561)
- McKeeman — *Differential testing for software*. Digital Technical Journal 10(1):100–107, 1998.
- Knight & Leveson — *An experimental evaluation of the assumption of independence in multiversion programming*. IEEE TSE SE-12(1):96–109, 1986. [doi:10.1109/TSE.1986.6312924](https://doi.org/10.1109/TSE.1986.6312924)
- Littlewood & Miller — *Conceptual modeling of coincident failures in multiversion software*. IEEE TSE 15(12):1596–1614, 1989. [doi:10.1109/32.58771](https://doi.org/10.1109/32.58771)
- Avizienis — *The N-version approach to fault-tolerant software*. IEEE TSE SE-11(12):1491–1501, 1985. [doi:10.1109/TSE.1985.231893](https://doi.org/10.1109/TSE.1985.231893)
- Yang, Chen, Eide & Regehr — *Finding and understanding bugs in C compilers* (CSmith). PLDI 2011. [doi:10.1145/1993498.1993532](https://doi.org/10.1145/1993498.1993532)
- Le, Afshari & Su — *Compiler validation via equivalence modulo inputs* (EMI). PLDI 2014. [doi:10.1145/2594291.2594334](https://doi.org/10.1145/2594291.2594334)
- Pei, Cao, Yang & Jana — *DeepXplore*. SOSP 2017. [arXiv:1705.06640](https://arxiv.org/abs/1705.06640)

**PBT, mutação, snapshot**
- Claessen & Hughes — *QuickCheck: a lightweight tool for random testing of Haskell programs*. ICFP 2000. [doi:10.1145/357766.351266](https://doi.org/10.1145/357766.351266)
- MacIver et al. — *Hypothesis: a new approach to property-based testing*. JOSS 4(43), 2019. [doi:10.21105/joss.01891](https://doi.org/10.21105/joss.01891)
- Arts, Hughes, Johansson & Wiger — *Testing telecoms software with Quviq QuickCheck*. Erlang Workshop 2006. [doi:10.1145/1178049.1178050](https://doi.org/10.1145/1178049.1178050)
- DeMillo, Lipton & Sayward — *Hints on Test Data Selection*. Computer 11(4):34–41, 1978. [doi:10.1109/C-M.1978.218136](https://doi.org/10.1109/C-M.1978.218136)
- Fraser & Zeller — *Mutation-driven generation of unit tests and oracles* (μtest). IEEE TSE 38(2):278–292, 2012. [doi:10.1109/TSE.2011.93](https://doi.org/10.1109/TSE.2011.93)
- Papadakis, Kintis, Zhang, Jia, Traon & Harman — *Mutation testing advances: an analysis and survey*. Advances in Computers 112:271–328, 2019. [doi:10.1016/bs.adcom.2018.03.015](https://doi.org/10.1016/bs.adcom.2018.03.015)
- Budd & Angluin — *Two notions of correctness and their relation to testing*. Acta Informatica 18:31–45, 1982. [doi:10.1007/BF00625279](https://doi.org/10.1007/BF00625279)
- Feathers — *Working Effectively with Legacy Code*. Prentice Hall, 2004.
- Meszaros — *xUnit Test Patterns*. Addison-Wesley, 2007.
- Fujita et al. — *Snapshot testing in practice*. JSS 204:111797, 2023. [doi:10.1016/j.jss.2023.111797](https://doi.org/10.1016/j.jss.2023.111797)
- *Snapshot effectiveness* — [arXiv:2610.29549](https://arxiv.org/abs/2610.29549)

**Decisão, estatística, arbitragem**
- Chow — *On optimum recognition error and reject tradeoff*. IEEE TIT 16(1):41–46, 1970. [doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)
- Wald — *Sequential tests of statistical hypotheses*. Annals of Mathematical Statistics 16(2):117–186, 1945. [doi:10.1214/aoms/1177731118](https://doi.org/10.1214/aoms/1177731118)
- Smith — *Various optimizers for single-stage production*. Naval Research Logistics Quarterly 3(1-2):59–66, 1956. [doi:10.1002/nav.3800030106](https://doi.org/10.1002/nav.3800030106)
- Wilson — *Probable inference, the law of succession, and statistical inference*. JASA 22(158):209–212, 1927. [doi:10.1080/01621459.1927.10502953](https://doi.org/10.1080/01621459.1927.10502953)
- Hoeffding — *Probability inequalities for sums of bounded random variables*. JASA 58(301):13–30, 1963. [doi:10.1080/01621459.1963.10500830](https://doi.org/10.1080/01621459.1963.10500830)
- Page — *Continuous inspection schemes*. Biometrika 41(1-2):100–115, 1954. [doi:10.1093/biomet/41.1-2.100](https://doi.org/10.1093/biomet/41.1-2.100)
- Gama, Žliobaitė, Bifet, Pechenizkiy & Bouchachia — *A survey on concept drift adaptation*. ACM CSUR 46(4):44, 2014. [doi:10.1145/2523813](https://doi.org/10.1145/2523813)
- Simpson — *The interpretation of interaction in contingency tables*. JRSS-B 13(2):238–241, 1951. [doi:10.1111/j.2517-6161.1951.tb00088.x](https://doi.org/10.1111/j.2517-6161.1951.tb00088.x)
- Fishburn — *Lexicographic orders, utilities and decision rules: a survey*. Management Science 20(11):1442–1471, 1974. [doi:10.1287/mnsc.20.11.1442](https://doi.org/10.1287/mnsc.20.11.1442)
- Auer, Cesa-Bianchi & Fischer — *Finite-time analysis of the multiarmed bandit problem*. Machine Learning 47:235–256, 2002. [doi:10.1023/A:1013689704352](https://doi.org/10.1023/A:1013689704352)
- Altman — *Constrained Markov Decision Processes*. Chapman & Hall, 1999.
- Kish — *Survey Sampling*. Wiley, 1965.
- Howey, Long & Fox — *VAL: automatic plan validation*. ICTAI 2004. [doi:10.1109/ICTAI.2004.120](https://doi.org/10.1109/ICTAI.2004.120)
- Wolpert & Macready — *No free lunch theorems for optimization*. IEEE Trans. Evol. Comput. 1(1):67–82, 1997. [doi:10.1109/4235.585893](https://doi.org/10.1109/4235.585893)

**Docs irmãos citados (não repetidos)**
`verification-oracles-deep.md` (DSL, MRs, kill-rate, P1–P10) · `plan-reuse-deep.md` (P5 Wilson, P8
gate) · `skill-induction-deep.md` (§5.5 promoção em 3 estágios) ·
`program-synthesis-library-learning.md` (§G portão compartilhado) · `unifying-math-offline.md`
(§3.10 `ΔJ`, §6 P1 calibração) · `math-offline-brain.md` (Chow, curva risco–cobertura) ·
`metaharness-self-improvement.md` (Self-Harness arXiv:2606.09498; Shepherd CRO arXiv:2605.10913;
DGM arXiv:2505.22954, lição "esconda o checker").
