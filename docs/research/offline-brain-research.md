# Cérebro offline da 0.9: fundamentos (agentes clássicos, NLU sem transformer, entender o projeto)

Pesquisa de 26/10/2026 para a 0.9 ("cérebro offline"): um agente de código autônomo que entende o pedido e o
projeto **sem LLM** (o LLM vira opcional, níveis 0–4), em Linux i386 com 2 GB, só com a stdlib do Go e scripts
python3 stdlib. Continua `harness-first-offline.md` (tese e máquina de estados) e parte do que já existe:
`intents.go`/`intents.json` (regex full-match → comando), `fuzzy.go` (Damerau com teclado ABNT2/QWERTY),
`nameindex.go` (índice de arquivos + símbolos por regex, ranqueado por contexto), `route_task.go` (11 tipos por
regex + reparo de typo), `offers.go` (menu numerado offline), `outparse.go` (parsers go/pytest/unittest/jest/
cargo/build/trace) e os 20 scripts de `jazz-scripts/` (manifesto JSON com `args`/`output`/`effect`).

Convenções: cada afirmação traz o link da fonte primária. **(estimativa)** marca números meus, não medidos.
"Custo em 1 núcleo" é estimativa para um Pentium M/Atom ~1,6 GHz (classe do D610), salvo indicação.

---

## A) Projetar agentes autônomos antes (e sem) LLMs

**Subsunção (Brooks, 1986).** Controle em camadas de competência: cada camada é um conjunto de máquinas de
estado simples que lê sensores e age; camadas superiores *suprimem* entradas ou *inibem* saídas das inferiores,
sem modelo central do mundo. A camada 0 (evitar obstáculos) funciona sozinha; as de cima só acrescentam.
Fonte: Brooks, "A robust layered control system for a mobile robot", IEEE J. Robotics & Automation 2(1), 1986
([doi:10.1109/JRA.1986.1087032](https://doi.org/10.1109/JRA.1986.1087032); AI Memo 864,
[dspace.mit.edu/handle/1721.1/6432](https://dspace.mit.edu/handle/1721.1/6432)).

**BDI / PRS / Jason.** Crenças (fatos), desejos (objetivos), intenções (planos em execução). O interpretador PRS
é um laço: perceber → atualizar crenças → escolher planos *pré-escritos* cujo gatilho e contexto casam → executar
um passo → reconsiderar. Planos são receitas da biblioteca, não geradas na hora. Fontes: Georgeff & Lansky,
"Reactive reasoning and planning", AAAI-87 ([mlanthology](https://mlanthology.org/aaai/1987/georgeff1987aaai-reactive));
Rao & Georgeff, "BDI Agents: From Theory to Practice", ICMAS-95
([aaai.org](https://cdn.aaai.org/ICMAS/1995/ICMAS95-042.pdf)); Jason/AgentSpeak, plano no formato
`+!objetivo : contexto <- corpo.` ([jason-lang.github.io](https://jason-lang.github.io/)).

**Quadro-negro (Hearsay-II).** Fontes de conhecimento independentes leem e escrevem hipóteses num quadro
compartilhado em níveis (sinal → sílaba → palavra → frase); um agendador escolhe qual fonte disparar pela
pontuação da hipótese. Fonte: Erman, Hayes-Roth, Lesser, Reddy, ACM Computing Surveys 12(2), 1980
([doi:10.1145/356810.356816](https://doi.org/10.1145/356810.356816)).

**STRIPS / HTN (SHOP2, Pyhop).** STRIPS: ação = pré-condições + lista de adição + lista de remoção
([Fikes & Nilsson 1971, doi:10.1016/0004-3702(71)90010-5](https://doi.org/10.1016/0004-3702(71)90010-5)).
HTN: tarefas compostas são decompostas por *métodos* até virarem ações primitivas, na ordem em que serão
executadas (SHOP2: [Nau et al., JAIR 20, 2003](https://www.jair.org/index.php/jair/article/view/10362)).
O núcleo do Pyhop 1.2.2 (`seek_plan`) tem ~20 linhas: se a primeira tarefa é operador, aplica numa cópia do
estado e recursa; senão tenta cada método em ordem — método devolve `False` ou lista de subtarefas que vão para
a frente da fila; falha volta (backtracking)
([cópia do Pyhop 1.2.2, Apache 2.0](https://web.stanford.edu/class/cs379c/resources/dialogical/zanax_HOP.py)).
O sucessor GTPyhop mistura tarefas, objetivos e ações na mesma lista e traz Run-Lazy-Lookahead (planeja,
executa, replaneja ao falhar) ([github.com/dananau/GTPyhop](https://github.com/dananau/GTPyhop)).

**Árvores de comportamento.** Raiz emite *ticks*; nó devolve Success/Failure/Running. Sequence (→) falha no
primeiro filho que falha; Fallback (?) para no primeiro que dá certo; Decorator limita tentativas/tempo;
Condition nunca devolve Running. Os autores mostram que BT generaliza FSM (sem "goto"), subsunção (Fallback por
prioridade) e programas teleo-reativos, e propõem *backchaining* (pós-condição ← Fallback[condição, ação com
pré-condições]). Fonte: Colledanchise & Ögren, *Behavior Trees in Robotics and AI*, CRC 2018
([arXiv:1709.00084](https://arxiv.org/abs/1709.00084)).

**Supervisão Erlang/OTP.** Estratégias `one_for_one` (padrão), `one_for_all`, `rest_for_one`; intensidade
padrão 1 reinício em 5 s, acima disso o supervisor desiste e escala ao pai; filhos `permanent`/`transient`
(reinicia só em saída anormal)/`temporary` ([erlang.org/doc/system/sup_princ.html](https://www.erlang.org/doc/system/sup_princ.html)).
Filosofia "let it crash": o trabalhador não trata todo erro; um nível acima restaura estado conhecido
([Armstrong, tese 2003](https://erlang.org/download/armstrong_thesis_2003.pdf)).

**systemd.** `Restart=` no/on-success/on-failure/on-abnormal/on-abort/on-watchdog/always; `RestartSec` padrão
100 ms; `RestartSteps`+`RestartMaxDelaySec` dão recuo exponencial (10→20→40→80→160 s); `WatchdogSec` exige
`WATCHDOG=1` periódico ou o serviço é dado como falho; `StartLimitBurst/IntervalSec` limitam tentativas
([systemd.service(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html),
[systemd.unit(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html)).

**Sistemas de produção (OPS5/Rete, CLIPS).** Regras SE-padrão-ENTÃO-ação sobre uma memória de trabalho; Rete
compila os padrões numa rede que guarda casamentos parciais, de modo que o custo por ciclo depende do que mudou,
não do total de fatos ([Forgy 1982, doi:10.1016/0004-3702(82)90020-0](https://doi.org/10.1016/0004-3702(82)90020-0));
resolução de conflito por especificidade/recência; CLIPS é a implementação de referência em C
([clipsrules.net](https://www.clipsrules.net/)).

### O que adotar no jazz (A)

1. **HTN-lite em Go, porte direto do `seek_plan`** (~150 linhas Go com o registro, estimativa): `Method{Task,
   Pre []Cond, Subtasks []Step}`, operadores = capacidades (scripts/ferramentas). Sem estado copiado em
   profundidade: o "estado" é um mapa `facts map[string]string` (cópia rasa por ramo, KB). Profundidade máx. 8,
   ≤ 200 nós expandidos por pedido → < 1 ms em 1 núcleo (estimativa). Métodos em JSON (formato na proposta).
2. **Planejar–executar–replanejar ao estilo Run-Lazy-Lookahead** (GTPyhop): não simular efeitos de ferramentas
   reais; executar o primeiro passo, observar, replanejar. É o laço PRS com a biblioteca de métodos como "planos".
3. **Execução do plano como behaviour tree mínima** (Sequence/Fallback/Retry(n)/Timeout(d) + Condition/Action),
   ~200 linhas Go (estimativa). Pré-condição falhou → Fallback tenta o próximo método. Running = ferramenta em
   curso (o jazz já é assíncrono no shell).
4. **Fatos num quadro-negro único por tarefa** (`.jazz/tasks/<id>.json`): investigadores (nameindex,
   detect_project, git_state, outparse) escrevem fatos com `source` e `confidence`; o agendador escolhe o próximo
   investigador pela lacuna que mais muda o plano (só fatos citados em pré-condições pendentes).
5. **Recuperação com semântica OTP/systemd**: por passo, `restart: transient`, `intensity: 2/120s`, recuo
   1→2→4 s, depois escala ao método pai (Fallback) e por fim a "perguntar ao usuário" ou "subir de nível de LLM".
   Watchdog: passo sem saída nova por `watchdog_s` = falha (o jazz já tem detector de travada).
6. **Regras de produção sem Rete**: com < 300 regras e < 1000 fatos, varredura linear com índice por
   predicado basta (< 1 ms, estimativa); Rete só se passar disso. Resolução de conflito: especificidade, depois
   prioridade declarada — igual ao `rtPriority` de `route_task.go`.
7. **Subsunção nos níveis 0–4**: camada 0 (fast path) sempre ativa; camadas de cima só suprimem a 0 quando têm
   confiança maior. Nunca o contrário: o LLM não desliga verificadores.

---

## B) Entender linguagem natural sem transformer (PT + EN, 1 núcleo)

**SHRDLU (Winograd, 1971).** Significado = procedimento executável sobre um modelo do mundo (blocos); o parser
consulta o mundo para desambiguar ("a pirâmide" → existe só uma? senão pergunta "qual?"). Fonte: AI TR-235,
[dspace.mit.edu/handle/1721.1/7095](https://dspace.mit.edu/handle/1721.1/7095). Para o jazz: **o mundo é o
repositório**; "o teste do parser" é uma consulta ao índice, e a ambiguidade se resolve contando candidatos.

**Gramáticas semânticas e frames.** LIFER (Hendrix, 1977) trocou categorias sintáticas por categorias do domínio
(`<SHIP>`, `<ATTRIBUTE>`) e permitia ao usuário definir paráfrases em tempo de uso ([Hendrix et al., ACM TODS
3(2), 1978, doi:10.1145/320251.320253](https://doi.org/10.1145/320251.320253)). Phoenix (Ward, CMU, ATIS) casa
fragmentos com redes de transição recursivas e preenche *slots* de *frames*, ignorando o que não entende
("parse as much of the input as possible") ([Ward 1994, ICSLP](https://www.isca-archive.org/icslp_1994/ward94_icslp.html);
[Ward 1989, HLT](https://aclanthology.org/H89-1018/)).

**Chart/Earley.** Earley analisa qualquer CFG em O(n³), O(n²) para não ambíguas e O(n) para a maioria das
LR(k) ([Earley 1970, CACM, doi:10.1145/362007.362035](https://doi.org/10.1145/362007.362035)). Com pedidos de
5–20 palavras, até o pior caso é barato; tolerância = permitir pular palavras (como Phoenix).

**Aho–Corasick e transdutores.** Aho–Corasick acha todas as ocorrências de um dicionário de padrões em tempo
linear no texto + nº de ocorrências ([CACM 1975, doi:10.1145/360825.360855](https://doi.org/10.1145/360825.360855)).
OpenFst generaliza para transdutores ponderados (normalização, reescrita) ([openfst.org](https://www.openfst.org/)).
Nota: o `regexp` do Go é RE2 (tempo linear, sem backtracking) ([pkg.go.dev/regexp](https://pkg.go.dev/regexp)).

**Snips NLU (open source, determinístico + probabilístico).** Pipeline: (1) parser determinístico com regex
gerada dos exemplos de treino (F1 = 1,0 nos exemplos); (2) se nada casou, regressão logística para intenção e um
CRF linear por intenção para slots, com features de n-gramas, forma da palavra, *gazetteers* por entidade,
clusters de palavras (Brown/word2vec k-means reduzem ~50 mil palavras a centenas de clusters) e entidades
embutidas via Rustling (port de Duckling). Modelos ocupam "algumas centenas de KB de RAM" nos casos comuns;
inferência 1,26 ms num MacBook Pro e 60 ms num Raspberry Pi 3, 220 ms num Pi Zero. Com 70 exemplos, F1 de slots
0,79 (melhor que Luis/Dialogflow/Wit no teste deles); 10→500 exemplos sobem F1 de slots em 32 pontos
([Coucke et al., arXiv:1805.10190](https://arxiv.org/abs/1805.10190); [github.com/snips/snips-nlu](https://github.com/snips/snips-nlu)).
Formato do dataset: intenções com exemplos anotados em linha (`[slot:entidade](valor)`) + entidades com valores
e sinônimos ([docs](https://snips-nlu.readthedocs.io/en/latest/dataset.html)).

**Rasa 1.x (pré-DIET).** Template `supervised_embeddings`: WhitespaceTokenizer, RegexFeaturizer,
CRFEntityExtractor, EntitySynonymMapper, dois CountVectorsFeaturizer (palavras e `char_wb` n-gramas 1–4) e
EmbeddingIntentClassifier; `pretrained_embeddings_spacy` usa SklearnIntentClassifier
([legacy-docs-v1.rasa.com](https://legacy-docs-v1.rasa.com/nlu/choosing-a-pipeline/)). Lição: n-gramas de
caracteres 1–4 dão robustez a typo e flexão sem stemmer.

**CRF.** Rotulagem de sequência condicional; para slots, BIO/BILOU por token
([Lafferty, McCallum, Pereira, ICML 2001](https://repository.upenn.edu/cis_papers/159/)).

**fastText.** Classificador linear sobre bag of n-gramas, treina em minutos em CPU
([Joulin et al., arXiv:1607.01759](https://arxiv.org/abs/1607.01759)); vetores com subpalavras (n-gramas de
caracteres 3–6) dão vetor até para palavra nunca vista — gíria, typo
([Bojanowski et al., TACL 2017, arXiv:1607.04606](https://arxiv.org/abs/1607.04606)). FastText.zip (poda de
features + quantização por produto + hashing + retreino) gera classificadores **< 100 kB** sem perda notável
([arXiv:1612.03651](https://arxiv.org/abs/1612.03651); [Facebook Engineering, 2017](https://engineering.fb.com/2017/05/02/ml-applications/expanded-fasttext-library-now-fits-on-smaller-memory-devices/)).
Vetores prontos PT/EN (cc.pt.300) têm vários GB ([Grave et al., arXiv:1802.06893](https://arxiv.org/abs/1802.06893)):
inviáveis em 2 GB sem poda; um subconjunto de 20 mil palavras × 64 dim × int8 ≈ 1,3 MB (estimativa).

**Snowball PT.** Regiões RV/R1/R2; passo 1 sufixos padrão (-amente, -idade…), passo 2 verbais só se o 1 não
removeu nada, passos residuais; ã/õ protegidos no prelúdio ([snowballstem.org](https://snowballstem.org/algorithms/portuguese/stemmer.html)).
Ex.: químicas → químic. Algoritmo puro de tabelas, ~300 linhas portadas (estimativa).

**Levenshtein/Damerau.** Damerau (1964) mostrou que ~80% dos erros são uma única inserção, remoção, troca ou
transposição ([CACM, doi:10.1145/363958.363994](https://doi.org/10.1145/363958.363994)). Autômatos de Levenshtein
de grau n se constroem em tempo linear na palavra e, cruzados com um trie, devolvem exatamente as palavras a
distância ≤ n ([Schulz & Mihov, IJDAR 5, 2002](https://doi.org/10.1007/s10032-002-0082-8)).

**Estado de diálogo por frames.** Rastreamento de estado = distribuição sobre valores de cada slot, atualizada a
cada turno ([DSTC, Williams et al., SIGDIAL 2013](https://aclanthology.org/W13-4065/)); sistemas frame-based
(RavenClaw) perguntam pelo slot obrigatório vazio de maior prioridade
([Bohus & Rudnicky 2009, doi:10.1016/j.csl.2008.10.001](https://doi.org/10.1016/j.csl.2008.10.001)).

### O que adotar no jazz (B)

1. **Duas passagens à la Snips**: passagem 1 = os regex atuais de `intents.json` (determinístico, F1=1 no que
   cobre); passagem 2 = classificador linear (regressão logística multinomial ou NB) sobre features hash, só se a
   1 não casou. Mesmo formato de exemplos para as duas: o regex é gerado dos exemplos anotados.
2. **Features baratas**: palavras normalizadas (`fpNormalize` + `fzFold`), radical Snowball PT/EN, n-gramas de
   caracteres `char_wb` 2–4 (lição Rasa), bigramas, e **features do mundo**: "token casa arquivo do índice",
   "token casa símbolo", "token é caminho". Hashing em 2^16 baldes × 25 intenções × float32 = 6,5 MB; com int8
   ≈ 1,6 MB, ou 2^14 baldes ≈ 400 kB (estimativa). Inferência = soma de ~100 pesos: < 0,1 ms (estimativa).
3. **Treino offline em Go puro** (SGD logístico, 10 épocas, alguns milhares de exemplos): segundos em 1 núcleo
   (estimativa). Arquivo `data/nlu/model.bin` versionado + `examples.jsonl` como fonte.
4. **Slots sem CRF primeiro**: gazetteers dinâmicos do próprio repositório (arquivos, símbolos, scripts,
   comandos do projeto) via Aho–Corasick sobre o texto normalizado + `nameindex` para o resto. CRF (Viterbi
   linear) só se a avaliação mostrar slots livres mal extraídos; ~300 linhas Go (estimativa).
5. **Sinônimos/gíria**: tabela curada `data/nlu/lexicon.json` (`"toca"→"rodar"`, `"zoado"→"quebrado"`) — já há
   `fpSynonymLead`/`fzIntent`; embeddings só como opcional ≤ 5 MB (20k palavras PT+EN, 64 dim, int8,
   quantizados de fastText), usados para *sugerir* entradas do léxico, não para decidir.
6. **Correção ortográfica**: manter o Damerau com custo de teclado de `fuzzy.go`; para vocabulário > 50 mil
   (símbolos de monorepo), trocar a varredura por trie + autômato de Levenshtein grau 1–2 (Schulz–Mihov).
7. **SHRDLU como regra de ambiguidade**: resolver referência contra o mundo; 1 candidato → agir; 2–3 → pergunta
   com menu numerado (já existe em `offers.go`); 0 → dizer que não achou e mostrar os 3 mais próximos.
8. **Frame da tarefa** com slots obrigatórios por intenção; pergunta só pelo slot obrigatório vazio que o
   investigador não conseguiu preencher (regra RavenClaw + "investigar antes de perguntar").

---

## C) Entender o projeto e o ambiente sem LLM

**ctags.** Formato `nome<TAB>arquivo<TAB>endereço;"<TAB>kind:f<TAB>class:X…`, ordenado por nome para busca
binária; pseudo-tags `!_TAG_FILE_FORMAT`, `!_TAG_FILE_SORTED` ([tags(5)](https://docs.ctags.io/en/latest/man/tags.5.html)).
Universal-ctags aceita parsers por regex declarativos (`--regex-<LANG>`, optlib)
([ctags-optlib(7)](https://docs.ctags.io/en/latest/man/ctags-optlib.7.html)) — mesmo espírito de `niSymRE`.

**tree-sitter.** Consultas `tags.scm` com capturas `@name.definition.*` / `@name.reference.*`
([code navigation](https://tree-sitter.github.io/tree-sitter/4-code-navigation.html)). As ligações Go usuais
exigem cgo. Alternativa pura-Go: `gotreesitter` (sem cgo, 206 gramáticas convertidas de `parser.c`, benchmarks
próprios não verificados) ([pkg.go.dev](https://pkg.go.dev/github.com/odvcencio/gotreesitter)); custo provável é
o tamanho do binário com as tabelas (não medido).

**Repo map do Aider (PageRank, sem LLM).** Lido em [`aider/repomap.py`](https://github.com/Aider-AI/aider/blob/main/aider/repomap.py):
tags def/ref por tree-sitter (refs por Pygments quando a linguagem só dá defs); grafo arquivo→arquivo onde cada
identificador definido em A e referenciado em B cria aresta B→A com peso `mul·sqrt(nrefs)`; `mul` ×10 se o
identificador foi citado no pedido, ×10 se tem ≥ 8 caracteres em snake/camel/kebab, ×0,1 se começa com `_`,
×0,1 se definido em > 5 arquivos, ×50 se quem referencia é arquivo da conversa; PageRank personalizado
(`100/N` para arquivos da conversa, citados ou cujo nome bate com identificador citado); o rank de cada nó é
repartido pelas arestas de saída para dar rank a pares (arquivo, símbolo); busca binária no nº de tags até
caber no orçamento de tokens (erro aceito 15%). Cache por mtime. Explicação: [aider.chat/2023/10/22/repomap.html](https://aider.chat/2023/10/22/repomap.html).

**Linguist / tokei.** Ordem de decisão: override `.gitattributes` → modeline → nome de arquivo → shebang →
extensão → cabeçalho XML → seção de man → heurísticas (regex por extensão ambígua, `heuristics.yml`) →
Bayes ingênuo; antes disso descarta binário, vendored, gerado e documentação
([how-linguist-works](https://github.com/github-linguist/linguist/blob/main/docs/how-linguist-works.md)).
tokei usa uma tabela JSON de linguagens com extensões, comentários e strings
([languages.json](https://github.com/XAMPPRocky/tokei/blob/master/languages.json)).

**Detecção de build/projeto.** Marcadores: Bazel `MODULE.bazel`/`WORKSPACE` ([bazel.build](https://bazel.build/external/overview));
Nx `nx.json` + `project.json`/`package.json` por projeto ([nx.dev](https://nx.dev/reference/project-configuration));
Pants `pants.toml` + `BUILD` ([pantsbuild.org](https://www.pantsbuild.org/stable/docs/using-pants/key-concepts/targets-and-build-files));
Earthly `Earthfile` ([docs.earthly.dev](https://docs.earthly.dev/docs/earthfile)); direnv carrega `.envrc`
subindo diretórios, só depois de `direnv allow` ([direnv.net](https://direnv.net/)). O jazz já faz isso por
marcadores em `fpDetect` e `detect_project.py`. Lockfiles (`go.sum`, `package-lock.json`, `pnpm-lock.yaml`,
`yarn.lock`, `Cargo.lock`, `poetry.lock`, `uv.lock`) dizem o gerenciador real.

**Grafo de imports e "ir para definição" sem LSP.** Regex por linguagem para `import`/`require`/`use`/
`#include` e resolução de caminho por convenção (Go: módulo de `go.mod`; JS: relativo + `index.*`; Python:
pacote por diretório). Definição: busca exata na tabela de símbolos; empate → mesmo diretório, depois arquivo
importado pelo atual, depois maior rank no PageRank (heurística minha, combina Aider + ctags).

**Sinais do git.** Co-mudança: regras de associação sobre commits (suporte, confiança); o ROSE previu 26% dos
arquivos seguintes e acertou um local no top-3 em 64% dos casos ([Zimmermann et al., ICSE 2004](https://www.st.uni-trier.de/diehl/pubs/icse04.pdf)).
Churn relativo prediz densidade de defeitos ([Nagappan & Ball, ICSE 2005, doi:10.1145/1062455.1062514](https://doi.org/10.1145/1062455.1062514)).
Dados: `git log --name-only --format=%H` ([git-log](https://git-scm.com/docs/git-log)), `git blame --porcelain`
([git-blame](https://git-scm.com/docs/git-blame)).

**Descoberta de testes.** Go: `*_test.go`, `func TestXxx` ([cmd/go](https://pkg.go.dev/cmd/go#hdr-Test_packages));
pytest: `test_*.py`/`*_test.py`, funções `test*`, classes `Test*` sem `__init__`
([pytest](https://docs.pytest.org/en/stable/explanation/goodpractices.html#conventions-for-python-test-discovery));
Jest: `**/__tests__/**/*.[jt]s?(x)` e `**/?(*.)+(spec|test).[jt]s?(x)` ([testMatch](https://jestjs.io/docs/configuration#testmatch-arraystring));
Cargo: `#[test]` em `src/` e `tests/*.rs` ([cargo](https://doc.rust-lang.org/cargo/guide/tests.html));
RSpec: `spec/**/*_spec.rb` ([RSpec Configuration#pattern](https://rspec.info/documentation/3.13/rspec-core/RSpec/Core/Configuration.html));
Maven Surefire: `**/Test*.java`, `**/*Test.java`, `**/*Tests.java`, `**/*TestCase.java`
([surefire](https://maven.apache.org/surefire/maven-surefire-plugin/examples/inclusion-exclusion.html)).

**Erro → arquivo:linha.** `outparse.go` cobre go test, pytest, unittest, jest/vitest, cargo, builds e traces
Python/Node/Go; **não** cobre JUnit/Maven, RSpec, mocha ou TAP (o script `extract_stacktrace.py` cobre trace
Java). Formatos: JUnit XML é o denominador comum (pytest `--junitxml`, Jest `jest-junit`, Surefire
`target/surefire-reports/*.xml`); TAP é linha a linha `ok N`/`not ok N` ([testanything.org](https://testanything.org/tap-version-14-specification.html)).

**Ambiente, com sondagem segura (só leitura local).** Container: `/.dockerenv`, `/run/.containerenv` (Podman,
[containers.conf/podman docs](https://docs.podman.io/en/latest/markdown/podman-run.1.html)),
`systemd-detect-virt --container` ([man](https://www.freedesktop.org/software/systemd/man/latest/systemd-detect-virt.html));
WSL: `WSL_DISTRO_NAME`/`WSL_INTEROP` e "microsoft" em `/proc/sys/kernel/osrelease`
([Microsoft, WSL issue #423](https://github.com/microsoft/WSL/issues/423)); Termux: `TERMUX_VERSION` e
`PREFIX=/data/data/com.termux/files/usr` ([wiki.termux.com](https://wiki.termux.com/wiki/Getting_started)) — já há
`termux.go`. Portas: `/proc/net/tcp{,6}` (estado `0A` = LISTEN) sem rodar nada ([proc(5)](https://man7.org/linux/man-pages/man5/proc.5.html));
`port_free.py` já usa ss/lsof/netstat. Variáveis de ambiente: só *nomes* (nunca valores) entram em fatos.

### O que adotar no jazz (C)

1. **Índice persistente `data/index/<hash-raiz>/tags.tsv`** no formato ctags estendido (ordenado, busca
   binária, pseudo-tags com versão e mtime-máx), gerado pelos regex de `niSymRE` estendidos a refs. Repo de
   5 mil arquivos / 50 MB: varredura inicial ~3–6 s em 1 núcleo, incremental por mtime < 200 ms; ~30 bytes por
   tag × 100 mil tags ≈ 3 MB (estimativas). Sem tree-sitter na 0.9; reavaliar `gotreesitter` medindo o binário 386.
2. **Repo map PageRank em Go puro** (porte do algoritmo do Aider, mesmos multiplicadores): grafo esparso em
   listas de adjacência; iteração de potência, damping 0,85, 30 iterações; 5 mil nós × 50 mil arestas ≈ 1,5 M
   multiplicações → ~10–30 ms (estimativa). Personalização = arquivos citados/tocados (`niTouch`). Saída: ranking
   de arquivos para o investigador e para o desempate de `nameindex`.
3. **Co-mudança do git**: `git log -n 500 --name-only` → contagens de pares (ignorar commits com > 30
   arquivos); arquivo `data/index/<h>/cochange.json` com top-10 por arquivo, `support ≥ 2`, `confidence ≥ 0,1`
   (limiar do ROSE). Custo: ~1 s no primeiro uso, incremental pelo último hash (estimativa).
4. **Detecção de linguagem à Linguist simplificada**: ordem nome → shebang → extensão → heurística regex só para
   extensões ambíguas (`.h`, `.m`, `.pl`, `.ts`); filtrar vendored/gerados por padrões (lista `vendor.yml` do
   Linguist como referência). Sem Bayes.
5. **Mapa de testes**: tabela `data/conventions/tests.json` (glob de arquivo + regex de caso + comando de
   execução filtrada por ecossistema) e relação arquivo→teste por nome (`niCore`) + import + co-mudança.
6. **outparse**: acrescentar parser JUnit XML (`encoding/xml`, cobre Maven/Gradle/pytest/jest-junit) e TAP;
   RSpec/mocha via seus reporters de texto só se aparecerem nas avaliações.
7. **Sonda de ambiente** `env_probe` (Go, sem rede, sem executar binários do projeto): container, WSL, Termux,
   SO/arch, RAM livre (`/proc/meminfo`), portas LISTEN (`/proc/net/tcp`), ferramentas no PATH (`exec.LookPath`),
   nomes de variáveis relevantes. < 20 ms (estimativa). Resultado vira fatos com `source:"env_probe"`.

---

## Projeto do cérebro offline (proposta)

**Componentes** (todos Go stdlib; scripts python3 continuam como capacidades):
`nlu` (normaliza → intents.json regex → classificador linear → slots por gazetteer/nameindex) ·
`world` (quadro-negro de fatos: índice ctags, repo map, co-change, detect_project, env_probe, git_state) ·
`planner` (HTN-lite, porte do `seek_plan` [A]) · `exec` (BT mínima + capacidades tipadas, permissões atuais) ·
`verify` (verificadores por domínio: código de saída, outparse, diff vazio/não vazio, arquivo existe) ·
`recover` (tabela abaixo) · `router` (níveis 0–4) · `learn` (métodos candidatos → promovidos após N sucessos).

**Arquivos** (diretório de dados `~/.local/share/jazz-cli/brain/`; projeto pode sobrepor em `.jazz/brain/`,
mesma regra de confiança de `intents.json`): `nlu/examples.jsonl`, `nlu/lexicon.json`, `nlu/model.bin`;
`methods/*.json`; `capabilities.json`; `recovery.json`; `index/<hash>/{tags.tsv,graph.bin,cochange.json}`;
`tasks/<id>.json` (TaskState de `harness-first-offline.md`).

**Método HTN-lite** (inspirado em Pyhop/SHOP2 + BT backchaining [A]):
```json
{"task":"open_file","name":"by_fuzzy_name","priority":10,
 "pre":[{"fact":"slot.target","op":"set"}],
 "steps":[{"cap":"resolve_name","in":{"q":"$slot.target"},"out":"cands"},
          {"if":"len(cands)==0","task":"report_not_found"},
          {"if":"len(cands)>1","task":"confirm_choice","in":{"options":"$cands"},"out":"target"},
          {"cap":"read_file","in":{"path":"$target"},"out":"content"}],
 "verify":[{"check":"file_exists","arg":"$target"},{"check":"nonempty","arg":"$content"}],
 "provenance":{"source":"builtin","version":1,"successes":0}}
```
Condições são uma mini-linguagem fechada (`set`, `eq`, `len`, `exists`, `matches`), sem `eval`.

**Registro de capacidades** — gerado dos manifestos atuais de `jazz-scripts/*.json` mais as ferramentas Go:
```json
{"name":"find_symbol","kind":"script","effect":"read","risk":0,
 "args":{...schema do manifesto...},"output":{...},
 "pre":["lang_supported"],"effects":["fact:symbol.defs"],
 "cost":{"p50_ms":300,"mem_mb":15},"timeout_s":20,"dry_run":true,
 "verifier":"defs_nonempty_or_reported","recover":"name_not_found"}
```
`effect` já existe (read/write/exec/git/net) e mapeia para risco 0/2/3/2/3 e para a confirmação atual;
`cost` é medido e atualizado pelo próprio jazz (média móvel), não declarado à mão.

**Máquina de estados da tarefa** (de `harness-first-offline.md`): `new → investigating → (needs_input |
planning) → ready → running → verifying → (succeeded | recovering → running | blocked | failed)`; `cancelled`
de qualquer estado. Só `verifying` leva a `succeeded`, e só com evidência anexada (sem "pronto" por texto).

**Tabela de recuperação** (`recovery.json`, semântica OTP/systemd [A]):

| categoria observável | detecção | ação (em ordem) | limite |
|---|---|---|---|
| nome não encontrado | 0 candidatos | sugerir 3 mais próximos (Damerau) → perguntar | 1 |
| ambíguo | 2–3 candidatos empatados | menu numerado | 1 pergunta |
| comando inexistente | exit 127 / "not found" | outro comando do detect_project → oferecer instalar (confirmação) | 2 |
| teste falhou | outparse ok, falhas>0 | anexar file:line → método `fix_test` (nível ≥ 2) ou relatar | — |
| timeout / sem saída | watchdog | reiniciar com recuo 1→2→4 s | 2 em 120 s |
| permissão negada | EACCES / recusa do usuário | não repetir; método alternativo ou `blocked` | 0 |
| plano impossível | nenhum método com pré-condições | subir um nível no roteador → `blocked` com o que falta | 1 |
| verificação indisponível | sem verificador aplicável | concluir como `unverified` explícito | — |

**Regra de roteamento 0→4**: nível 0 se `intents.json` casa (hoje) ou o classificador dá p ≥ 0,85 e todos os
slots obrigatórios estão preenchidos e existe método aplicável; nível 1 (regras + HTN completo) se p ≥ 0,6 ou a
lacuna é preenchível por investigador; nível 2–3 (LLM local, se configurado) só para: escolher intenção entre
as top-3 quando p < 0,6, extrair slot livre, ou propor passo quando nenhum método aplica — sempre com saída
restrita às capacidades registradas; nível 4 (remoto) só se permitido e o nível 3 falhou ou não existe. Sem LLM
disponível, os níveis 2–4 viram "perguntar ao usuário com menu" (offers.go). Limiares são estimativas a calibrar
com a suíte de avaliação.

**Primeira fatia vertical: "arquivo impreciso → confirmar → abrir → evidência"**
```
entrada: pedido p (ex.: "abre o teste do parsr")
1. n ← normaliza(p)                          # fpNormalize + fzFold + léxico
2. (intent, prob) ← nlu(n); se intent ∉ {open_file, show_file, explain_file} e prob < 0,6 → offers (menu)
3. slots ← gazetteer(n, índice) ∪ niTokens(n) ; cues ← {teste, função, arquivo}
4. cands ← resolve(slot.target, cues)         # nameindex.resolve + rank PageRank + touched + recência
5. se |cands| = 0: mostrar 3 mais próximos (niSimilarPaths); estado needs_input; fim
6. se |cands| > 1 e score(c1) − score(c2) < margem: menu numerado com motivo de cada (niWhy); aguardar
7. alvo ← c1; ler arquivo (limite de linhas); se slot.symbol → saltar para a definição (tags.tsv)
8. evidência ← {caminho, linhas mostradas, por que este (≤ 3 motivos), alternativas descartadas, hash do conteúdo}
9. verificar: arquivo existe, lido sem erro, conteúdo não vazio → succeeded; senão recovering (tabela)
10. registrar em tasks/<id>.json; se o usuário escolheu no menu, gravar par (texto → arquivo) como exemplo
    candidato em nlu/examples.jsonl (aprendizado só após 3 confirmações iguais)
```
Custo esperado em 1 núcleo: passos 1–4 < 10 ms com índice quente, ~3–6 s na primeira indexação (estimativa).
Métricas da fatia: acerto no top-1, perguntas feitas vs necessárias, zero arquivos inventados.

---

## O que não consegui fonte primária

- Números exatos de tamanho/precisão do FastText.zip por dataset: o PDF não foi legível nesta sessão; uso o
  resumo do abstract/post (< 100 kB, "duas ordens de grandeza menos memória").
- Artigo ICASSP 1991 do Phoenix: citado via Ward 1989/1994 e Ward & Young 1993.
- Desempenho e tamanho de binário do `gotreesitter`: só benchmarks do próprio autor.
- Valores padrão de `StartLimitBurst`/`StartLimitIntervalSec`: apontado para systemd.unit(5), não conferido.
- Todos os custos "em 1 núcleo" e tamanhos de modelo/índice deste documento são estimativas; medir no D610.
