# Oráculos de verificação sem LLM: PBT, metamórfico, diferencial, geração, invariantes, snapshots e estado observável

Pesquisa de 04/11/2026 para a linha **verificação** do cérebro offline: como o jazz decide `succeeded`
só com evidência, hoje e amanhã. Continua, sem repetir: `docs/design/offline-brain.md` (a spec que
`brVerify` implementa), `offline-brain-research.md` (HTN/BT/NLU — lá está Pyhop e a recuperação por
categoria), `autonomy-control-theory.md` (o laço sentir→decidir→agir→verificar→replanejar como
controlador; este doc aterra o bloco "VERIFICAR" desse diagrama), `math-offline-brain.md` (limiares e
calibração — aqui entra a versão binária: o verificador *é* o decisor de abstinência Chow, com custo
assimétrico `false_success ≫ false_blocks`), `harness-sota-gap.md` (como os harnesses comerciais
verificam — ou não verificam), `metaharness-self-improvement.md` (o laço de aprendizado §7 da spec) e
`harness-first-offline.md` (a tese). Não repito nada que esses docs já fundamentaram; quando o tema já
tem doc, cito o doc, não o paper.

Convenções: cada afirmação traz a fonte primária com id (arXiv/DOI). **(estimativa)** marca número meu,
não medido. `file:line` refere-se ao código na data da pesquisa. "Custo no d610" = Pentium M ~1,6 GHz,
1 núcleo, linux/386, 2 GB, só stdlib do Go + python3 stdlib. Identificadores do código em inglês.
Status no mapa (§4): **feito** / **parcial** / **só pesquisado**.

Tese curta: **o verificador é a peça que separa "agente" de "gerador de saída"**. Claude Code, Codex e
OpenCode declaram "done" quando o modelo diz "done"; o jazz declara `succeeded` só quando um
verificador determinístico confirma evidência no mundo (`task.go:160-162`). Esse é exatamente o ponto
onde os concorrentes falham e onde podemos ganhar — *desde que* os oráculos sejam fortes. Oráculo
fraco vira `false_success` (o usuário aprende a não confiar) ou `false_blocks`/`unverified` crônico
(o usuário aprende a ignorar). Este doc é sobre como forjar oráculos **fortes** que rodam offline.

---

## 1. O problema do oráculo e onde o jazz está na taxonomia

Barr, Harman, McMinn, Shahbaz & Yoo, *The Oracle Problem in Software Testing: A Survey*, IEEE TSE
41(5):507–525, 2015, doi:10.1109/TSE.2014.2372785 — o survey canônico. Taxonomia útil:

- **Oráculo especificado**: vem de uma especificação formal (contratos, modelos). É o caso do jazz:
  `m.Verify` é uma pós-condição declarada pelo autor do método (Design-by-Contract de Meyer,
  *Object-Oriented Software Construction*, 1988/1997 — "todo fornecedor declara pre/post/invariante
  executáveis").
- **Oráculo derivado**: extraído de artefatos — documentação, execuções anteriores, outras
  implementações. Cobre metamórfico, diferencial, invariantes à la Daikon, snapshots. É onde está o
  espaço novo para o jazz.
- **Pseudo-oráculo**: uma implementação alternativa e independente cujo resultado é aceito como
  referência (differential testing; N-versão de Avizienis, *The N-version approach to fault-tolerant
  software*, IEEE TSE SE-11(12), 1985, doi:10.1109/TSE.1985.231893).
- **Oráculo humano**: o fallback universal — caro, inconsistente, não escala. No jazz o equivalente
  honesto não é "fingir um oráculo", é `unverified`/`needs_input`.

Duas leis do survey que valem como regra de projeto aqui: (a) *todo oráculo é parcial* — a pergunta não
é "está certo?" e sim "o que este oráculo consegue distinguir?"; (b) *automatizar o oráculo é o
gargalo* — geração de caso de teste sem oráculo é inútil, e nosso caso de teste é a própria ação do
agente no mundo.

Meszaros, *xUnit Test Patterns* (2007), separa **state verification** (conferir o estado final do
mundo) de **behavior verification** (conferir as chamadas/caminho). `brVerify` hoje faz os dois de
forma rudimentar: `exit_zero`/`cmd_ran`/`tests_passed` olham o comportamento observado; `file_exists`/
`file_gone`/`profile_present` olham o estado final. As propostas do §6 são todas refinamentos de
state verification — mais barata, mais legível, e a única que o usuário consegue conferir sozinho.

Análogo planejamento clássico: **VAL** (Howey, Long & Fox, *VAL: automatic plan validation…*, ICTAI
2004, doi:10.1109/ICTAI.2004.120) valida um plano PDDL simulando estado a estado e checando
pré-condição e efeito em cada passo. `brExecute` (`brain.go:261-385`) já é isso com o mundo real no
lugar do simulador; a diferença é que VAL checa *todos* os estados intermediários e o jazz só declara
verificador no método inteiro — não no passo (o campo `Capability.Verifier`, `caps.go:76`, existe e
hoje é só documentação: ninguém avalia `file_exists+nonempty` de `read_file`).

---

## 2. Técnica a técnica: resumo, número-chave, limitação

### 2.1 Property-based testing: propriedade + geração + minimização

**QuickCheck** (Claessen & Hughes, *QuickCheck: a lightweight tool for random testing of Haskell
programs*, ICFP 2000, doi:10.1145/357766.351266). Em vez de casos, propriedades (`rev(rev xs) == xs`)
+ geradores + **shrinking**: ao falhar, busca local reduz o contraexemplo até o mínimo — o que separa
"ruído" de "essência do bug". Número-chave: o artigo demonstra bugs sutis (inclusive na própria
biblioteca de teste) com especificações de ~10 linhas; o mecanismo inteiro cabe em poucas centenas de
linhas. Limitação: o shrinking por *tipo* (QuickCheck clássico) ignora como o valor foi gerado e pode
produzir "contraexemplos" inválidos — corrigido pela geração integrada abaixo.

**Hypothesis** (MacIver et al., *Hypothesis: a new approach to property-based testing*, JOSS 4(43),
2019, doi:10.21105/joss.01891). Representação universal do caso de teste como *sequência de bytes*:
geração e redução operam no nível do byte stream, não do valor — shrinking integrado à geração, sempre
válido por construção, e redução quase-totalmente automática (MacIver, *Notes on test-case
reduction*, drmaciver.com 2019: redução perfeita é exponencial; local search basta). Limitação: para
o jazz o caso de teste é um *estado de mundo* (fixture), não um buffer — a representação universal
transfere mal; o que transfere é o princípio "minimizar o que falha antes de mostrar".

**Swarm testing** (Groce et al., ISSTA 2012, doi:10.1145/2338965.2336763). Contra-intuitivo e barato:
em vez de uma configuração de geração com todos os features ligados, uma *nuvem* de configurações
cada uma omitindo features aleatórios. **42% mais crashes distintos** em compiladores C numa semana
que a configuração ajustada à mão — porque features competem por espaço no caso e alguns ativamente
suprimem comportamentos. Aplicação direta: variar os fixtures do eval omitindo peças (sem README,
sem .git, sem test runner, hostnames presentes/ausentes) custa ~zero e cobre cantos que uma fixture
"rica" única nunca toca.

**PBT stateful/model-based** (Arts, Hughes, Johansson & Wiger, *Testing telecoms software with Quviq
QuickCheck*, Erlang Workshop 2006, doi:10.1145/1178049.1178050). O caso de teste é uma *sequência de
operações*; o oráculo é um **modelo de estado** simplificado rodado em paralelo: cada operação checa
pós-condição no estado do modelo. É literalmente o que `m.Verify` deveria ser — e o modelo aqui é o
próprio filesystem/git/proc, não um abstrato (§6, P1/P3).

Em Go: `testing/quick` (stdlib, congelado: gera valores e chama a propriedade; **sem shrinking**),
gopter e rapid (terceiros — fora da regra stdlib-only; o rapid é "Hypothesis para Go" e mostra que o
essencial cabe em uma lib: geradores determinísticos por seed + minimização automática).

### 2.2 Metamorphic testing: o oráculo é uma relação entre execuções

**Chen, Cheung & Yiu**, *Metamorphic testing: a new approach for generating next test cases*,
HKUST-CS98-01, 1998 (republicado em arXiv:2002.12543). Quando não há oráculo para a saída individual
(`sin(x)` — quem sabe o valor certo?), transforma-se a entrada e checa a **relação** entre saídas:
`sin(x) = sin(π−x)`. Formalmente: `R(x₁,…,xₙ) ⟹ Rf(f(x₁),…,f(xₙ))`. A MR é condição **necessária**,
não suficiente — falhou, é bug; passou, só não refuta.

**Segura, Fraser, Sanchez & Ruiz-Cortés**, *A Survey on Metamorphic Testing*, IEEE TSE 42(9):805–824,
2016, doi:10.1109/TSE.2016.2532875. 750+ trabalhos depois, o padrão empírico: MRs simples
(inversibilidade, monotonicidade, preservação, invariância sob permutação) resolvem a maioria; a
dificuldade real é **identificar a MR** — trabalho de domínio, não de máquina; e combinar MT com
seleção de casos de cobertura, porque follow-up de um caso sem interesse continua sem interesse. A
técnica certa para oráculo de *efeitos*: rename deve preservar conteúdo; delete deve ser
irreversível; criar duas vezes deve falhar da segunda — relações sobre *o mundo antes e depois*, não
sobre stdout.

**CheckList** (Ribeiro, Wu, Guestrin & Singh, ACL 2020, arXiv:2005.04118 — best paper). Metamórfico
operacionalizado como matriz: capacidades × tipos de teste {MFT caso mínimo, INV invariância sob
perturbação, DIR mudança esperada numa direção}. Usuários com CheckList escreveram **~2× mais testes
e acharam ~3× mais bugs** que sem ele. O jazz já tem a semente — as tarefas `open-typo`,
`tool-typo`, `tests-slang` de `evals/offline/tasks.json` são MRs escritas à mão ("typo/abreviação/gíria
⟹ mesma intenção"). O que falta é o gerador: perturbador determinístico sobre os prompts (acento,
typo Damerau ≤2 que `fuzzy.go` já implementa, reordenação, gíria da tabela do `nlu`) produzindo
variantes INV em massa, e DIR para casos que *devem* mudar ("abre o parser" vs "abre o parser_test").

### 2.3 Differential testing e pseudo-oráculos

**McKeeman**, *Differential testing for software*, Digital Technical Journal 10(1):100–107, 1998.
A ideia original: duas+ implementações comparáveis recebem casos gerados mecanicamente; **divergência
= bug candidato** — sem nenhum oráculo de correção. Número-chave social: funciona, mas "um fluxo
infinito de relatórios de bug" é politicamente indigerível (o paper é tão franco sobre isso quanto
sobre a técnica).

**CSmith** (Yang, Chen, Eide & Regehr, PLDI 2011, doi:10.1145/1993498.1993532). Gerador de programas
C válidos (sem UB) + comparação entre compiladores/otimizações: **325+ bugs reportados, todo
compilador testado crashou e todo compilador testado emitiu código errado silenciosamente**. Lição
para o jazz: o gerador só é útil porque é *valido-por-construção* — os casos de divergência nunca são
"entrada ilegal". Para nós: a mutação de fixtures tem que produzir mundos válidos (arquivo existe mas
vazio, ok; arquivo cujo nome é symlink quebrado, cuidado).

**EMI** (Le, Afshari & Su, PLDI 2014, doi:10.1145/2594291.2594334 — distinguished). Equivalência
módulo entradas: variantes do programa que devem dar a mesma saída *naquelas* entradas; 147 bugs
confirmados únicos em GCC+LLVM em 11 meses, maioria miscompilação. A forma mais barata de MR:
transformação **que não deveria mudar nada** — se mudou, bug. Versão jazz: comandos de leitura
executados duas vezes devem observar o mesmo estado; `git status` via `script:git_state` e via
`read_cmd` devem concordar (§6, P4).

**DeepXplore** (Pei, Cao, Yang & Jana, SOSP 2017, arXiv:1705.06640). Múltiplos DNNs como
**cross-referencing oracles** uns dos outros + busca por comportamento divergente: milhares de casos
errados em modelos SOTA, ~1 input/segundo num laptop comum. O ponto que transfere: quando há N
implementações, **concordância pode substituir correção conhecida** — e divergência é sinal, não
sentença (pelo menos um está errado, não se sabe qual).

### 2.4 Geração automática de testes (e o que ela ensina sobre oráculos)

**Randoop** (Pacheco, Lahiri, Ernst & Ball, ICSE 2007, doi:10.1109/ICSE.2007.37). Sequências de
chamadas geradas com *feedback*: cada input é executado e classificado contra **contratos** —
redundante, ilegal, violador, ou útil para estender. Achou erros novos em 780 KLOC de bibliotecas
.NET/Java. Número-chave para o jazz: o oráculo era um conjunto pequeno de **contratos de fronteira**
(`o.equals(o)`, `hashCode` consistente, não lançar exceção ilegal) — exatamente o papel dos `checks`
do nosso DSL sobre `Capability.Effects`.

**EvoSuite** (Fraser & Arcuri, ESEC/FSE 2011, doi:10.1145/2025113.2025179). GA otimizando a *suíte
inteira* por cobertura, depois **assertion suggestion**: oráculos sintetizados que "resumem o
comportamento atual" + minimização. A honestidade do abstract é a limitação: asserções geradas
capturam o comportamento *atual*, não o *correto* — oráculo de regressão, não de especificação.
(Vale o mesmo aviso para Daikon e para `snapshot_cmd`.)

**μtest** (Fraser & Zeller, *Mutation-driven generation of unit tests and oracles*, IEEE TSE
38(2):278–292, 2012, doi:10.1109/TSE.2011.93). Em vez de cobertura, a busca mira **mutantes**: cada
mutante induz uma mudança de estado que *vira o oráculo que o mata*. Suítes assim geradas acham
significativamente mais defeitos semeados que as escritas à mão. Consequência direta para §6: a força
de um `verify` se mede matando mutantes do mundo — e mutantes do mundo são baratos (P5).

**Pynguin** (Lukasczyk & Fraser, ICSE-C 2022, arXiv:2202.05218; estudo empírico EMSE 2023,
arXiv:2111.05003). O "EvoSuite de Python": mesma busca, mas em linguagem dinâmica a informação de tipo
some e a cobertura/fault-detection cai visivelmente em relação a Java — o paper é franco: gerar teste
para linguagem dinâmica é objetivamente mais difícil. Lição: verificação por *estado observável no
mundo* (o que o jazz faz) é robusta justamente porque não depende da linguagem do projeto alvo.

**DSpot / test amplification** (Danglot, Vera-Pérez, Baudry, Monperrus et al., EMSE 28(4), 2019,
doi:10.1007/s10664-019-09692-y; survey arXiv:1705.10692). Pegar testes *existentes* e amplificar:
novos inputs + novas asserções. 26/40 classes melhoradas; **13/19 melhorias propostas via PR foram
aceitas** pelos mantenedores. O pipeline é a cara do nosso "online ensina offline" (spec §7):
amplificar `verify` existente → propor diff → revisar → promover.

### 2.5 Inferência de oráculos: invariantes dinâmicos e aprendizagem de asserções

**Daikon** (Ernst, Perkins, Guo, McCamant, Pacheco, Tschantz & Xiao, *The Daikon system for dynamic
detection of likely invariants*, SCP 69(1-3):35–45, 2007, doi:10.1016/j.scico.2007.01.015). Roda o
programa, observa valores em program points e reporta propriedades que *valeram em todas as
execuções vistas*: `x = a`, `x ≠ 0`, `a ≤ x ≤ b`, `y = f(x)`, ordenação, contenção. É aprendizado de
máquina sobre dados de execução — nada mágico: template de invariante × conjunto de traces →
eliminação dos falsificados + score de confiança. Limitações conhecidas do próprio paper: invariantes
"são prováveis, não provados" — falsos positivos proporcionais à pobreza dos traces; invariantes
verdadeiros-mas-inúteis dominam a saída sem ranking. **Transferência**: nosso "program point" é o fim
do método; nossas variáveis são o estado do mundo observado (existência, tamanho, hash, exit, contagens
do outparse). Minerar `results/*.json` + evidências de sessão à la Daikon é viável *em dados* — o
motor é trivial; o custo é coletar traces bons (§6, P6).

**GAssert** (Terragni, Jahangirova, Tonella & Pezzè, ESEC/FSE 2020, doi:10.1145/3368089.3409758).
Primeira técnica a *melhorar* asserções: co-evolução de duas populações minimizando falsos
positivos, falsos negativos e tamanho, com evidência FP/FN vinda de mutantes+execuções (OASIs).
Resultado: **+34% de mutation score** em médio sobre asserções iniciais (geradas por Daikon),
comparável a melhorias humanas. O formalismo FP/FN sobre oráculos é exatamente nosso
`false_success`/`false_blocks` — e dá a receita: para melhorar um `verify`, precisamos de um gerador
de *estados que deveriam passar* e de *estados que deveriam falhar* (P5).

**EvoSpex** (Molina, Ponzio, Aguirre & Frias, ICSE 2021, arXiv:2102.13569). GA aprendendo
pós-condições que **separam** pares (estado-válido, estado-inválido-gerado-por-mutação). Ideia
central: o oráculo é um classificador entre mundos bons e mundos maus — o mesmo enquadramento de
GAssert, e o mesmo que `brVerify` precisa.

**SpecFuzzer** (Eberhard, Clemens, Fazzini et al., ASE 2023 demo; paper arXiv:2201.10874). Pipeline
que é quase literalmente o que proponho para o jazz: (i) **fuzzing gramatical** gera asserções
candidatas a partir de uma gramática derivada da classe; (ii) **Daikon filtra** as invalidadas pelos
traces; (iii) **análise de mutação** agrupa e ranqueia (mais forte primeiro). Supera GAssert/EvoSpex
porque a gramática limita o espaço de busca a asserções *expressíveis e sãs*. Para nós: a gramática é
a lista fechada de `checks` do DSL + seus domínios de `arg` — gerar candidatos é enumerar
`check × arg-padrão` e deixar os traces votarem.

**Oráculos neurais (forja, nunca runtime).** TOGA (Dinella, Ryan, Mytkowicz & Lahiri, ICSE 2022,
arXiv:2109.09262): transformer que infere oráculo de asserção e de exceção — **96% de acurácia**
contra 63% do SOTA anterior (+33%), e integrado ao EvoSuite achou **57 bugs reais, 30 exclusivos**.
ChatAssert (Hayet, Scott & d'Amorim, IEEE TSE 51(1), 2025, doi:10.1109/TSE.2024.3519159): prompt
engineering com feedback estático+dinâmico, +15% Acc@1 sobre teco. O survey dedicado (Molina & Gorla,
*Test Oracle Automation in the era of LLMs*, arXiv:2405.12766) nomeia as duas ameaças: data leakage
(o LLM viu o oráculo certo no treino) e *oracle deficiency* — asserção que compila, passa, e não
distingue nada. Uso correto no jazz: LLM forte **escreve** cláusulas `verify`, MRs e geradores *uma
vez*; tudo que vai pro `methods/*.json` é revisto, e roda depois a custo zero. LLM *avaliando* a saída
em runtime (LLM-as-judge, `judge.go`) é dependência pura — fora do cérebro offline.

**Specification mining** (Ammons, Bodík & Larus, POPL 2002, doi:10.1145/503272.503275). O ancestral:
aprender especificações (autômatos probabilísticos de uso de API) de traces, sob a máxima "uso comum
≈ uso correto". Mesma ressalva de Daikon: bugs frequentes viram "especificação" — por isso minerado
é *candidato*, promovido só com revisão (o fluxo §7 da spec).

### 2.6 Snapshot / characterization / golden tests

**Feathers**, *Working Effectively with Legacy Code* (2004): characterization test = escrever teste
que **fixa o comportamento atual**, não o desejado, para poder mudar o código sem medo. É a forma
industrial mais honesta de "oráculo derivado": não diz se está certo, diz se **mudou**.

**Snapshot testing empírico**: Fujita et al., *Snapshot testing in practice*, JSS 204:111797 (2023,
doi:10.1016/j.jss.2023.111797) — revisão de literatura cinza: snapshots são fáceis de criar e bons
contra regressão, **mas fragilidade e falso-positivo dominam as queixas**; boas práticas = snapshots
pequenos, focados, tratados como código (revistos). Estudo de efetividade recente (arXiv:2610.29549,
340 repos + survey de 60 devs): snapshot+testes tradicionais alcançam mediana **86,7% de cobertura**
e detectam **1,3× mais mutantes** que só tradicionais — complementares, não substitutos. No jazz:
`testdata/outparse/*.golden.json` já é golden file (feito para o parser); `jazz-scripts/snapshot_cmd`
é uma characterization genérica pronta ("run cmd, save stdout to .jazz/snap/<name>, diff it later" —
`{"status": "created|same|changed|updated"}`) que **nenhum método usa como verificador** (parcial).

### 2.7 Verificação por estado observável e DSLs declarativas

A forma do nosso problema: a ação do agente é uma transição no mundo; verificar = observar o estado
pós-transição e decidir. Precedentes industriais diretos:

- **Goss** (goss.rocks, YAML+Go, single binary): `file: {path: exists:true, mode:"0644", contains:[…]}`,
  `port`, `process`, `http` — o *descendente* de Serverspec/InSpec para "o servidor está como
  declarei?". É essencialmente o `verify` do jazz com uma linguagem maior e tipos de alvo mais ricos.
- **Ansible `--check` + handlers**: idempotência como verificação — rodar duas vezes não pode mudar o
  estado na segunda. A MR "execução dupla ⟹ efeito nulo na segunda" é grátis para métodos
  idempotentes (§6, P3).
- **SWE-bench** (Jimenez et al., ICLR 2024, arXiv:2310.06770): o benchmark que definiu o padrão de
  verificação de agentes é *duas mãos de oráculo*: `FAIL_TO_PASS` (o defeito sumiu) **e**
  `PASS_TO_PASS` (mediana de **51 testes** por instância garantindo que nada mais quebrou). O
  `verify` do jazz hoje só tem a primeira mão — não existe check de "o que não era pra mudar não
  mudou" (o checkpoint `cpAfterBash`, `checkpoint.go:466`, já sabe a lista de arquivos escritos:
  falta só cruzar com os `Effects` declarados). Os CHECKs de `evals/v2/tasks/*/CHECK` já implementam
  isso ad hoc (diff -r de `tests/` intactos + saída exata em dado oculto) — prova de que o padrão é
  natural no repo, falta trazê-lo para o runtime.
- **AgentSpec** (Wang et al., arXiv:2503.18666, ICSE'26): DSL de enforcement com três componentes —
  *trigger* (quando a regra dispara), *predicate* (o que checar), *enforcement* (stop/inspect/repair).
  >90% de execuções inseguras evitadas, overhead de milissegundos; regras geradas por LLM atingem
  95,56% de precisão — mas a geração é forja e a avaliação é determinística, exatamente nossa regra
  de ouro. Estruturalmente `cbMinimalIntervention` (`caps.go:552`) é um AgentSpec de uma regra; a DSL
  `verify` é a metade "predicate" aplicada a *resultados* em vez de ações.
- **Verificação de planos**: VAL (§1) já cobre a forma "simule e cheque cada passo".

### 2.8 A fronteira: oráculos *para* agentes

**Agent-as-a-Judge** (Zhuge et al., ICML 2025, arXiv:2410.10934). Agentes avaliando agentes sobre
requisitos hierárquicos (DevAI: 55 tarefas, **365 requisitos** anotados à mão): tão confiável quanto
avaliação humana, −97,72% de tempo. **Dependência** se usado em runtime — mas a estrutura do ativo é
o que interessa: uma árvore de requisitos verificáveis por tarefa. Versão offline: `verify` como
checklist aninhado (all/any/not com pesos) em vez de conjunção plana (§6, P1).

**Self-Challenging / Code-as-Task** (Zhou et al., arXiv:2506.01716, Meta FAIR). Tarefa sintética =
instrução + **função de verificação** + casos de solução e de falha. É o empacotamento perfeito da
filosofia: o LLM forja o *bundle* {método, verificador, contraexemplos}; offline só executa funções de
verificação já escritas. O eval do próprio jazz já é isso — `evals/offline/tasks.json` = instruções +
`expect` + fixtures; falta o terceiro elemento (contraexemplos do verificador, §6 P5).

---

## 3. A regra de ouro aplicada: offline vs forja vs dependência

| técnica | runtime offline no d610 | forja (LLM uma vez) | dependência (proibido no laço) |
|---|---|---|---|
| Avaliar `verify` (DSL pura) | ✅ já roda — `brVerify` | — | — |
| Novos checks: stat/hash/contém/regex/contagem/exit-set | ✅ barato (stat+sha256 de arquivos envolvidos; <1 ms típico, hash de MBs ~ms-10s ms (estimativa)) | escrever cláusulas | — |
| Estado observável (exists/gone/proc/port/git-state) | ✅ syscalls + cmds allowlist | — | — |
| Metamórfico 2 execuções (idempotência, preservação) | ✅ dobra custo do passo só quando habilitado | escolher a MR por método | — |
| Diferencial entre caps (mesmo fato, 2 caminhos) | ✅ +1 comando read-only | escolher pares | — |
| Snapshot/characterization (`snapshot_cmd`) | ✅ write só no update; diff barato | revisar o gold inicial | — |
| Fixture mutation / verifier kill-rate | ✅ `evals/offline` + manipulação de arquivos; segundos | gerar a lista de mutantes | — |
| Perturbações CheckList do NLU | ✅ tabela de perturbações + replay do `brRun` em-processo | novos padrões de perturbação | — |
| Mineração de invariantes (mini-Daikon sobre evidências) | ✅ análise offline sobre JSONL: agrupar+eliminar+ranquear; ~ms–s por método | revisar e promover candidatos | — |
| PBT dos *parsers/DSL* (testing/quick) | ✅ testes Go, segundos | escrever propriedades | — |
| Geração EvoSuite/Pynguin/Randoop de testes do *projeto do usuário* | ⚠️ parcial: Pynguin existe via python3, mas pesado p/ d610 e mira código, não mundo — **fora do laço**; oferecer como `suggest`, não como verify | — | — |
| Busca evolutiva de oráculos (GAssert/EvoSpex/SpecFuzzer) | ⚠️ viável só miniaturizado e offline-batch (minutos, não no laço) | sim | — |
| TOGA/ChatAssert/AssertLLM/neural oracles | — | ✅ LLM forte escreve checks/MRs uma vez | usar em runtime |
| Agent-as-a-Judge / LLM-as-judge / `judge.go` no laço offline | — | pode rotular dados de avaliação | **dependência** — nunca decide `succeeded` |
| Model checking/verificação simbólica do agente | — | — | inviável em 386 + escopo errado |

Leitura da tabela: o runtime offline aceita **predicados determinísticos sobre o mundo** e análises
batch sobre artefatos gravados. Tudo que é *aprendizado pesado* ou *geração por modelo* entra como
dado — cláusula, MR, invariante ranqueado, golden — depois de revisado. É o mesmo padrão do
`nlu/*.json` embutido e do `improve --promote` (spec §7).

---

## 4. Mapa file:line: o que já existe no jazz

| peça | file:line | status | nota |
|---|---|---|---|
| Gate "succeeded exige evidência" | `task.go:160-162` | **feito** | `To(succeeded)` rejeita sem evidência — a lei fundamental |
| Estados `verifying`/`unverified` | `task.go:23-50`, transições | **feito** | `unverified` é terminal e nunca vira `succeeded` |
| DSL de verificação (9 checks) | `methods.go:54-57` (`mhCheck`), `methods.go:1680-1763` (`brVerify`) | **feito** | `file_exists, file_gone, read_ok, exit_zero, cmd_ran, status_obtained, script_ok, script_exit_zero, profile_present, tests_passed` — 10 na verdade; conjunção estrita, primeiro-falha-retorna; sem comparadores/quantificadores/estado-pré |
| `verify:` nos métodos | `methods/*.json` (78 usos de check; 28 métodos sem verify — todos terminais blocked/report) | **feito/parcial** | cobertura boa nos métodos que executam; checks dominantes: `cmd_ran` (24), `exit_zero` (16), `read_ok`/`file_exists`/`script_ok` (8 cada) — **fracos**: confirmam "rodou/existe", raramente "está certo" |
| Verifier por capacidade | `caps.go:76` (`Capability.Verifier`), defaults `caps.go:84-104` | **parcial** | campo existe com valores (`file_exists+nonempty`, `outparse`) mas **nada o avalia** — metadata morta hoje |
| Evidência anexada por passo | `methods.go` (40+ `AddEvidence`), `brain.go:316,469` (`EvidenceIDs`) | **feito** | evidência existe e é ligada ao passo; não é usada *como* verificador |
| Oráculo de testes | `outparse.go:47-56` (`opData`), `brVerify` `tests_passed` `methods.go:1740-1757` | **feito** | oráculo real com parse (não só exit code); recusa `succeeded` quando não reconhece saída |
| Snapshot/characterization | `jazz-scripts/snapshot_cmd.json` (`created|same|changed|updated`), `testdata/*golden*` | **parcial** | mecanismo existe como script; **zero** métodos o chamam em `verify` |
| Snapshot de fs p/ efeitos | `checkpoint.go:466` `cpAfterBash`, `guard_honest.go:356` `hgState.snapshot`, `:939` `hgCheckSnap` | **feito** | a infra de "mundo antes/depois" já existe — é a base grátis para pré-estado e collateral-check |
| Guards honestos (anti-gaming) | `guard_honest.go` — test-protection `:669`, anti-weakening `:445`, hardcode `:1086`, done-gate `:1335`, eval-tamper `:546` | **feito** | protege *a integridade* do oráculo — único repo de agente CLI que conheço com isso |
| Falso-sucesso medido | `evaloffline.go:401-406`, gate CI `:164-166` | **feito** | `false_success>0 ∨ false_blocks>0` falha o comando |
| Oráculo por execução de checker | `evals/v2/tasks/*/CHECK` (11 shell checkers com `.hidden` goldens) | **feito (eval)** | padrão "verificador executável + dado oculto" pronto para copiar no runtime |
| Recuperação sem verificador | `recover.go:118` (`verifier_unavailable → mark_unverified`) | **feito** | honestidade por construção |
| Mini-DSL formal/fuzzada | — | **só pesquisado** | sem spec formal, sem fuzz do avaliador |
| Pré-estado (hash/stat antes dos passos) | — | **só pesquisado** | `brVerify` não tem `$pre` — impossível hoje checar preservação |
| PASS_TO_PASS / collateral check | — | **só pesquisado** | `Effects` declarado (`caps.go:74`) não é conferido contra writes reais |
| Força do oráculo medida (mutation) | — | **só pesquisado** | nada mede se `verify` mataria um mundo-sabidamente-errado |
| Mineração de invariantes | — | **só pesquisado** | `improve.go` mina *lições*; ninguém mina cláusulas de `verify` |

Resumo do mapa: o *esqueleto* é SOTA-para-CLI (evidência obrigatória + estados honestos + anti-gaming);
o *oráculo* é subdimensionado — DSL mínima, sem estado-pré, sem collateral, sem medida de força, e um
mecanismo de characterization (`snapshot_cmd`) criado e esquecido.

---

## 5. Gaps concretos (o que separa o atual do SOTA acima)

1. **Conjunção plana, arg único.** `mhCheck{check,arg}` (methods.go:54) não expressa `any`, `not`,
   comparadores (`contains`, `regex`, `size_gt`), nem referência a estado-pré — metade das MRs úteis
   (preservação, irreversibilidade) é inexprimível hoje.
2. **Sem pré-estado.** Checar "rename preservou conteúdo" exige `sha256` de `a` antes do passo;
   `brExecute` não captura nada antes de `m.Steps` (brain.go:293).
3. **Sem lado PASS_TO_PASS.** Nada garante "só o declarado mudou". `Capability.Effects` (`caps.go:74`)
   + `cpAfterBash` (checkpoint.go:466) já produzem os dois lados da conta.
4. **Verificador nunca é testado.** Nenhum mutante de mundo verifica que `verify` pega um falso
   sucesso — hoje um `file_exists` passaria até se o arquivo estiver vazio/corrompido.
5. **Checks fracos dominam.** `cmd_ran`/`exit_zero` = "o comando respondeu/terminou 0" — necessário,
   nunca suficiente para `succeeded` forte (ex.: `git_commit` com `cmd_ran` não confirma que o commit
   existe — `git log -1` seria o oráculo real).
6. **Cobertura desigual.** 28 métodos sem verify são todos bloqueadores — ok; mas métodos de escrita
   (`rename`, `delete_file`, `git_checkout`) usam só `file_exists`/`cmd_ran` onde haveria MR de
   conteúdo/estado-git.
7. **Nada aprende oráculos.** O pipeline §7 existe para *métodos*; não existe equivalente para
   *verificadores* (mineração → candidato → promoção).

---

## 6. Propostas (Go, d610, stdlib)

Métricas alvo em todas: `no_llm_rate` real, `sucesso verificado`, `false_success` (=0 gate),
`false_blocks`, p50/p95 e RSS pico, `comandos/tarefa` — o próprio `eoSummary` (evaloffline.go:91-107).
Todas as propostas presumem gate de CI com `false_success = 0` inegociável.

### P1 — DSL `verify` v2: composição + comparadores + pré-estado

**Mecanismo.** Estender `mhCheck` para árvore: `{all:[…]}`, `{any:[…]}`, `{not:{…}}`, folhas
`{check, arg, op, val}` com ops `eq|ne|gt|lt|contains|regex|empty`. Novos checks:
`file_nonempty`, `file_sha256 <hex|$pre>`, `fs_contains <glob>` (via `nameindex`/`os.ReadDir`),
`count_eq`, `symbol_present <nome> <arquivo>` (regex de `nameindex`), `still_running <pid|nome>`,
`git_head_changed`, `output_contains <substr>`. Pré-estado: `brExecute` grava frame antes do
primeiro passo — `stat+sha256` dos paths citados em `m.Steps[].in`/`m.Verify[].arg` (cap 64 arquivos,
cap 4 MB por arquivo — acima grava só stat). Avaliador puro, sem syscall além das já existentes.
**Arquivos**: `methods.go` (`mhCheck`, `brVerify`, `brFrame`), `brain.go` (frame em `brExecute`),
`methods/*.json` (migração progressiva — DSL v1 continua válida: todo check antigo é uma folha).
**Custo d610**: hash de ≤64 MB em cenário patológico ~0,5–1,5 s em 386 (estimativa; sha256 em Go é
~40–80 MB/s num Pentium M); típico ≤4 arquivos pequenos → <20 ms. RSS: +~200 KB.
**Métrica**: % de métodos de escrita com pós-condição forte (alvo: 100% de `rename/delete/git_commit`);
`false_success` continua 0; p50 de verificação +<50 ms.
**Impacto 5 · Esforço M · Risco baixo** (avaliador novo bem testado; migração por método).
*Base*: Meyer DbC; Barr survey §"specified oracle"; Goss; AgentSpec (trigger→predicate→enforcement
como all/any/not).

### P2 — PASS_TO_PASS: collateral check por `Effects` declarados

**Mecanismo.** `m.Effects` (novo campo agregado dos `cap.Effects` dos steps) declara *o que pode
mudar* (`write:<paths>`, `git`, `proc`, `net:none`). Após `brVerify` passar, comparar os paths
gravados pelo shadow-git (`cpAfterBash`/`AfterWrite` já coletados) e o frame de P1 contra o
declarado: escrita fora do conjunto → `unverified`+aviso (não `failed`: o objetivo valeu, mas houve
efeito colateral inesperado — honestidade, não punição). Vira o `PASS_TO_PASS` do jazz.
**Arquivos**: `methods/*.json` (+`effects`), `methods.go`, `checkpoint.go` (expôr lista do turno),
`brain.go`.
**Custo**: zero comandos extras — só comparação de conjuntos já coletados.
**Métrica**: `false_success` (deve pegar sucessos "certos porém sujos" que hoje passam); nova métrica
`collateral_violations` por método; `false_blocks` não pode subir (fallback = unverified, não blocked).
**Impacto 5 · Esforço M · Risco médio** (falso positivo se `Effects` estiver incompleto — por isso
`unverified` e não `blocked` inicialmente; medir 2 semanas e endurecer).
*Base*: SWE-bench F2P+P2P (arXiv:2310.06770); CHECKs de `evals/v2` (diff de `tests/` intactos).

### P3 — Verificadores metamórficos (relações, não valores)

**Mecanismo.** Cláusulas de *relação* usando o frame de P1: 
- `preserves_content <path>` — sha256 pós == sha256 pré (rename/move: conteúdo viaja com o nome);
- `gone_iff <a> <b>` — `exists(b) ∧ ¬exists(a)` (rename/delete); 
- `second_run_noop` — para métodos idempotentes (`service_start`, `container_up`, `create`): rodar a
  última ação de novo não pode mudar estado nem falhar feio (MR de idempotência, estilo Ansible);
- `output_stable <cap>` — rerodar o comando read-only final e exigir saída equivalente
  (normalização `rvFactKey`, recover.go:200 — já existe o hash ordem-independente!).
**Arquivos**: `methods.go` (checks), `brain.go` (segunda execução controlada), `methods/rename.json`,
`delete_file.json`, `service_start.json`, `container_up.json`, `create.json`.
**Custo**: `second_run_noop` dobra o custo do último passo — habilitar só onde barato
(`systemctl is-active` ~50 ms vs `container_up` segundos — *não* usar em ops pesadas; MR selecionável
por método). Preserves/gone: <5 ms (hash já no frame).
**Métrica**: `false_success` ↓ em mutantes de conteúdo (P5 mede); `unverified` não pode disparar
(false_block disfarçado).
**Impacto 4 · Esforço M · Risco médio** (idempotência real depende do mundo — `service_start` num
systemd flappy pode falhar a 2ª vez sem culpa; por isso verdict=unverified quando a 2ª execução
falha por infra, não por relação).
*Base*: Chen et al. 1998 (arXiv:2002.12543); Segura survey TSE 2016 — MRs simples dominam.

### P4 — Verificação diferencial entre capacidades

**Mecanismo.** Pseudo-oráculo N-versão barato: o mesmo fato por dois caminhos independentes, e
divergência vira `unverified` com as duas evidências. Pares já existentes: `git_state` (script) vs
`git status` (read_cmd); `script:run_tests` counts vs `opParse` direto; `port_owner` (procfs) vs
`ss/netstat` (read_cmd); `env_status` (envprobe) vs comandos reais (`nproc`, `free`, `df`).
Implementação: check `agrees <capA> <capB> <field>` — brVerify executa B só quando A passou e o
custo é baixo (B sempre read-only da `cbReadCmds`).
**Arquivos**: `methods.go`, `methods/*.json` (status/info methods primeiro), `caps.go` (registrar
pares).
**Custo**: +1 comando read-only por método (~30–100 ms no d610, estimativa) — aceitável só onde o
método já rodou comandos; *nunca* em atalhos <300 ms (`open_file`, `git_status` leve).
**Métrica**: `divergências detectadas` (cada uma é um bug real do parser/perfil — valor duplo);
`false_success` ↓; latência nos métodos pesados +<150 ms.
**Impacto 4 · Esforço M · Risco baixo-médio** (divergências espúrias por race/janela temporal —
mitigar com normalização tolerante e retry 1× antes de `unverified`).
*Base*: McKeeman 1998; DeepXplore cross-referencing (arXiv:1705.06640); EMI (PLDI 2014) — duas
leituras "equivalentes módulo observação".

### P5 — Verifier kill-rate: mutação de mundo como métrica de força do oráculo

**Mecanismo.** `jazz eval offline --verify-mutants`: para cada tarefa cujo expect é `succeeded`, a
fixture ganha N mutantes de mundo por método (tabela por intent):
`file_op` → {arquivo criado vazio; conteúdo trocado; criado fora do root (symlink); perms erradas};
`run_tests` → {suite com falha silenciosa; output não-parseável; exit 0 com 0 testes};
`git_commit` → {HEAD não moveu; autor errado; árvore suja}; `service` → {unit ativa mas dead}.
Para cada mutante: rodar método, exigir que `brVerify` **falhe** (senão o mutante sobreviveu =
oráculo fraco). Kill-rate por check publicado no relatório. Complemento swarm: `--swarm` omite
features da fixture (sem README, sem git, PATH mínimo) — fixtures pobres cobrem pré-condições.
**Arquivos**: `evaloffline.go` (+mutantes/summary), `evals/offline/mutants.json` (tabela), `methods.go`
(hooks p/ inspeção).
**Custo**: só eval — fora do caminho do usuário; ~2–5 s por mutante de shell-out no d610, aceitável
em batch (estimativa: suite atual ~50 tarefas × ~3 mutantes ≈ 10–20 min; rodar `--only` no dev).
**Métrica**: **verifier kill-rate** (alvo ≥90% por intent), `false_success` em mutante (gate: todo
mutante tem que ser pego *ou* classificado consciente como "mutante equivalente").
**Impacto 5 · Esforço M · Risco baixo** (só eval; o risco é descobrir verdades desconfortáveis).
*Base*: μtest Fraser-Zeller TSE 2012; GAssert FP/FN (ESEC/FSE 2020); Papadakis survey 2019;
swarm testing ISSTA 2012 (a variante --swarm); CSmith — mutante válido-por-construção.

### P6 — Mini-Daikon sobre evidências: minerar invariantes → cláusulas candidatas

**Mecanismo.** Traces já existem: `results/offline-*.json`, evidências `tkEv` e saídas `opParse` nos
JSONL de sessão. Minerador offline-batch (`jazz eval oracles`): por `intent×método`, coleta os
estados-finais observados (exists, size, sha, exit, counts) e testa templates à la Daikon —
constante, faixa, não-vazio, igualdade campo-a-campo, implicação (`exit==0 ⟹ file_exists`),
disjunção (`exit ∈ {0,1}`). Candidato = template que sobrevive a todos os traces + score
(n_traces, n_mundos-distintos). Saída = `verify` propostas em diff JSON, promovidas via
`jazz improve --promote` (fluxo §7 já existe p/ lições/métodos — estender para cláusulas).
**Arquivos**: novo `verifymine.go`, `improve.go` (canal de promoção), `results/` (leitura).
**Custo**: análise batch — nada no laço. Templates ~20 × campos ~10 × traces dezenas: ms–s por
método. O custo real é *coletar* traces (o eval já produz).
**Métrica**: candidatos aprovados/rejeitados por revisão; métodos que ganham checks novos; zero
mudança em runtime até promoção.
**Impacto 4 · Esforço M · Risco médio** (invariante provável ≠ verdadeiro — falso positivo de
Daikon; mitigação: candidato nunca vira check sem revisão humana e sem passar no kill-rate de P5).
*Base*: Daikon SCP 2007; SpecFuzzer (templates da gramática + filtro + ranking por mutação —
nossa gramática é o DSL); Ammons POPL 2002; pipeline §7 da spec.

### P7 — Characterization como verificador: ligar `snapshot_cmd` ao `verify`

**Mecanismo.** Novo check `snap_status <name> <expect:same|changed>` que chama `script:snapshot_cmd`
como *verificador*: em `--snap-update` (forja) grava ouro em `.jazz/snap/`; em runtime compara —
`saida igual` = evidência forte de comando estável (`env_status`, `self_info`, `service_status` de
unit esperada, `git_status` de repo limpo). `changed` = falha verificada. Resolve o método que hoje
confirma "rodou" sem confirmar "respondeu o certo".
**Arquivos**: `methods.go` (check), `methods/env_status.json`, `self_info.json`, `service_status.json`,
`git_status.json`, `jazz-scripts/snapshot_cmd` (já pronto — só precisa de normalização opcional:
máscara de hostname/versão/data para não tornar o oráculo frágil).
**Custo**: um fork de python3 (~30–80 ms d610) só onde habilitado; goldens de KBs.
**Métrica**: `unverified` de métodos informativos ↓; fragilidade medida = % de snapshots que falham
por ruído não-semântico (alvo <5% — acima, a máscara está errada, cf. Fujita JSS 2023).
**Impacto 3 · Esforço S · Risco médio** (snapshots frágeis = falso-bloqueio; normalização é o jogo
inteiro — começar com `same` normalizado, nunca byte-exato em saída com data/versão).
*Base*: Feathers 2004; estudo arXiv:2610.29549 (1,3× mutantes, mas falso-positivo é a queixa nº1).

### P8 — CheckList do NLU+verify: perturbações INV/DIR em massa sobre `tasks.json`

**Mecanismo.** Gerador em Go: para cada `prompt` de `evals/offline/tasks.json` com `expect.intent`,
aplica a tabela de perturbações já implementada pelo próprio NLU — inserir typo (Damerau ≤2 com o
teclado de `fuzzy.go`), trocar acento, sinônimo/gíria da tabela `nlu`, reordenação segura, caso-typo —
produzindo variantes `INV` (mesma intenção e mesmo status esperado) e `DIR` (mudança que *deve* mudar
o alvo: `parser`→`parser_test`, `lexer`→`handler`) e `MFT` (forma canônica mínima). Roda como tarefas
extra no eval (`--perturb N`), tudo em-processo (`brRun` direto, sem shell-out).
**Arquivos**: `evaloffline.go`/`evals/offline/` (gerador+seed fixa), opcional `fuzzy.go` (reuso).
**Custo**: ~1–5 ms por variante em-processo (estimativa) — centenas de variantes por centavo de
parede; shell-out só nas tarefas que precisam de fixture real.
**Métrica**: acurácia INV por perturbação (alvo = baseline da suíte), falsos `handled` em DIR (=0),
e o freebie: dataset de regressão do NLU cresce 5–10× sem escrever nada à mão.
**Impacto 4 · Esforço S-M · Risco baixo** (variantes inválidas quando a perturbação muda a semântica —
filtrar por confiança do parser e revisar amostra).
*Base*: CheckList ACL 2020 (arXiv:2005.04118) — MFT/INV/DIR, 2–3× bugs; MR = mesma ideia aplicada
a NLU; já temos a categoria `impreciso` feita à mão — isso a industrializa.

### P9 — Spec formal da DSL + fuzz do avaliador (contra o próprio bug)

**Mecanismo.** `docs/design/verify-dsl.md`: gramática das folhas/operadores/domínios, semântica
(ordem, curto-circuito, unavailable-vs-false), tabela check×arg×tipo válido. Fuzzing do avaliador com
`testing/quick` (stdlib): propriedades `brVerify nunca panica`, `verdict ∈ {ok,fail,unavail}`,
`mesmo frame → mesmo verdict` (determinismo), `check desconhecido → unavail nunca ok`. Golden corpus
em `testdata/verify/*.json` (mundo serializado → verdict esperado) para regression da DSL.
**Arquivos**: `methods_verify_test.go`, `testdata/verify/`, `docs/design/verify-dsl.md`.
**Custo**: segundos em `go test`; zero em runtime.
**Métrica**: cobertura do avaliador; bugs de borda achados (ex.: arg tipo errado hoje cai em
`return false,false` mudo — já visível no código, P1 deve tipar).
**Impacto 3 · Esforço S · Risco baixo**.
*Base*: QuickCheck ICFP 2000; Hypothesis JOSS 2019 (princípio: minimizar a entrada que falha —
aqui a "entrada" é a cláusula, pequena por natureza).

### P10 — `expected_exit_set` + oráculo de contagem: fechar os buracos de `cmd_ran`/`exit_zero`

**Mecanismo.** Antes de P1: o barato-que-sangra. `exit_zero` hoje trata qualquer comando como
binário 0/não-0; muitos oráculos reais são "exit ∈ conjunto conhecido" (`grep`: 0=achou, 1=não achou,
≥2=erro — hoje `1` vira falha mesmo sendo *resposta*; `systemctl status`: 3=inativo é resposta válida
— já codado ad hoc em `brSystemd` `methods.go:394` e no comentário de `cmd_ran` `:1709-1714`).
Check `exit_in <a,b,c>` + `fail_count_eq`/`pass_count_gt` sobre `opData` (outparse.go:47) cobre ~metade
dos métodos fracos sem árvore nenhuma.
**Arquivos**: `methods.go` (2–3 checks novos), `methods/*.json` (~15 métodos ganham oráculo correto).
**Custo**: nulo — mesmo número de comandos.
**Métrica**: `false_blocks` ↓ (grep-1 deixa de bloquear), `unverified` ↓ em métodos de status;
pré-requisito: catálogo de exit-semânticos por comando (doc na spec da DSL).
**Impacto 4 · Esforço S · Risco baixo**.
*Base*: a própria exceção já embutida no comentário de `cmd_ran`; Randoop — contratos de fronteira
pequenos que classificam "ilegal vs resposta".

---

## 7. O que NÃO adotar (e por quê)

1. **Daikon original / EvoSpex / SpecFuzzer / GAssert como ferramentas.** JVM, instrumentação de
   program points, traces gigantes — erram o alvo duas vezes: nosso "programa" são ações no mundo
   (fs/git/proc), não métodos Java; e nada disso compila em linux/386 com 2 GB de forma útil.
   Adota-se a *receita* (templates × traces → eliminar falsificados → ranquear), não a ferramenta (P6).
2. **EvoSuite/Randoop/Pynguin dentro do laço.** Gerar teste unitário do *projeto do usuário* é tarefa
   de forja/LLM (o `fix`/`create` com modelo), não do verificador — e Pynguin no d610 é inviável por
   custo. Oferta legítima: `suggest` para quando houver modelo, nunca `verify`.
3. **Oráculos neurais em runtime (TOGA/ATLAS/ChatAssert/Agent-as-a-Judge).** Dependência pura — LLM
   decidindo `succeeded` é exatamente o que o cérebro offline elimina. Seu papel é *escrever* assets
   (cláusulas, MRs, mutantes) uma vez.
4. **Shrinking completo estilo Hypothesis.** Nosso "contraexemplo" é um estado de mundo — minimização
   de mundo é difícil e pouco comprável; o que se transfere é o princípio *mostrar o mínimo que
   evidencia a falha* (P5 já reporta mutante + `why`).
5. **Model checking / verificação formal do agente.** Escopo e custo errados: o laço já é checado
   empiricamente por `evals/offline` + mutantes; provar propriedades de um executor HTN pequeno é
   trabalho de `autonomy-control-theory.md` (§prova de cobertura), não de oráculo.
6. **Byte-exact snapshots sem normalização.** Fujita JSS 2023 + arXiv:2610.29549: fragilidade é a
   falha dominante do snapshot testing. Só com máscaras semânticas (P7), ou nada.
7. **Segunda execução metamórfica universal.** `second_run_noop` em comando pesado/externo dobra
   custo e traz flakiness do mundo — MR selecionada por método, nunca global (P3).
8. **`verify` como shell arbitrário.** Manter a lei atual: checks são funções puras/observação por
   capacidades registradas — um `verify: bash -c …` abriria a caixa que `mhBadCmd`
   (methods.go:115-122) fechou de propósito.

---

## 8. Referências rápidas

Barr et al., *Oracle Problem survey*, TSE 41(5) 2015, doi:10.1109/TSE.2014.2372785 · Claessen &
Hughes, *QuickCheck*, ICFP 2000, doi:10.1145/357766.351266 · MacIver et al., *Hypothesis*, JOSS 4(43)
2019, doi:10.21105/joss.01891 · Groce et al., *Swarm testing*, ISSTA 2012,
doi:10.1145/2338965.2336763 · Arts et al., *QuickCheck stateful*, Erlang 2006,
doi:10.1145/1178049.1178050 · Chen et al., *Metamorphic testing*, HKUST-CS98-01 1998,
arXiv:2002.12543 · Segura et al., *MT survey*, TSE 42(9) 2016, doi:10.1109/TSE.2016.2532875 ·
Ribeiro et al., *CheckList*, ACL 2020, arXiv:2005.04118 · McKeeman, *Differential testing*, DTJ
10(1) 1998 · Yang et al., *CSmith*, PLDI 2011, doi:10.1145/1993498.1993532 · Le et al., *EMI*, PLDI
2014, doi:10.1145/2594291.2594334 · Pei et al., *DeepXplore*, SOSP 2017, arXiv:1705.06640 · Avizienis,
*N-version*, TSE SE-11(12) 1985 · Pacheco et al., *Randoop*, ICSE 2007, doi:10.1109/ICSE.2007.37 ·
Fraser & Arcuri, *EvoSuite*, ESEC/FSE 2011, doi:10.1145/2025113.2025179 · Fraser & Zeller, *μtest*,
TSE 38(2) 2012, doi:10.1109/TSE.2011.93 · Lukasczyk & Fraser, *Pynguin*, ICSE-C 2022,
arXiv:2202.05218 · Danglot et al., *DSpot*, EMSE 28(4) 2019, doi:10.1007/s10664-019-09692-y · Ernst
et al., *Daikon*, SCP 69(1-3) 2007, doi:10.1016/j.scico.2007.01.015 · Terragni et al., *GAssert*,
ESEC/FSE 2020, doi:10.1145/3368089.3409758 · Molina et al., *EvoSpex*, ICSE 2021, arXiv:2102.13569 ·
*SpecFuzzer*, ASE 2023, arXiv:2201.10874 · Dinella et al., *TOGA*, ICSE 2022, arXiv:2109.09262 ·
Hayet et al., *ChatAssert*, TSE 51(1) 2025, doi:10.1109/TSE.2024.3519159 · Molina & Gorla, *LLM
oracle automation*, arXiv:2405.12766 · Ammons et al., *Mining specifications*, POPL 2002,
doi:10.1145/503272.503275 · Feathers, *Working Effectively with Legacy Code*, 2004 · Meszaros,
*xUnit Test Patterns*, 2007 · Fujita et al., *Snapshot testing in practice*, JSS 204 2023,
doi:10.1016/j.jss.2023.111797 · *Snapshot effectiveness*, arXiv:2610.29549 · Jimenez et al.,
*SWE-bench*, ICLR 2024, arXiv:2310.06770 · Wang et al., *AgentSpec*, ICSE 2026, arXiv:2503.18666 ·
Zhuge et al., *Agent-as-a-Judge*, ICML 2025, arXiv:2410.10934 · Zhou et al., *Self-Challenging*,
arXiv:2506.01716 · Howey et al., *VAL*, ICTAI 2004, doi:10.1109/ICTAI.2004.120 · Papadakis et al.,
*Mutation testing survey*, Advances in Computers 112 2019, doi:10.1016/bs.adcom.2018.03.015 · Goss
(goss.rocks), Serverspec/InSpec — prática.
