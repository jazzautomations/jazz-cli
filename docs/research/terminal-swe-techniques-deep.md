# Técnicas de SWE-bench Verified e Terminal-Bench, a fundo

## Localização determinística (arquivo→elemento→linha) e reparo mecânico no jazz offline

**Data:** 2026-11-04 · **Alvo:** Dell (i686, 1 CPU, 2 GB, offline, Go estático linux/386)
**Regra de ouro:** LLM no caminho crítico de runtime é dependência, não asset. LLM em tempo de
compilação forja asset (pesos, receitas, fixtures, exemplos de NLU); o offline reusa sem token.

**Relação com os docs anteriores.** Este texto não repete `harness-sota-gap.md`: ele assume os
princípios P5 (localização é o gargalo; decompor repo→arquivo→elemento→linha), P7 (verificação é
execução, não declaração) e as lacunas G1/G2/G11, e vai mais fundo em três pontos que lá eram
uma linha: (a) o que cada paper realmente mediu, com método, números e ablações; (b) um modelo de
sinais determinísticos para localizar sem LLM, com calibração e abstenção; (c) a fronteira exata do
reparo mecânico, incluindo o que `mechrepair.go` já tem e o que falta para virar caminho de runtime.
Assume também `grep-is-all-you-need.md` (lexical vs vetorial, entrega inline; prefetch #2),
`offline-brain-research.md` C3 (semente de co-mudança), `low-level-performance.md` §5 (layout do
índice), `math-offline-brain.md` §2.1/§2.6 (calibração e orçamentos) e `tocomas-plan-reuse.md`
(reuso de plano). O delta está na §3 (pilha determinística), §4 (regra de ouro aplicada), §6
(propostas com métrica e custo 32-bit) e §8 (protocolo de falsificação).

**Nota de método.** Para cada paper foram lidos abstract, método, resultados e ablações nas versões
HTML/PDF públicas; os números vêm da tabela/seção citada, não de resumos de terceiros. Onde só o
abstract estava disponível, está dito. Números de leaderboard de 2026 (Vals AI, tbench.ai) não são
comparáveis aos papers de 2024–2026: servem para ordem de grandeza, não para rank. O que se aproveita
aqui é o *mecanismo* e a *ablação*, não a posição.

---

## 1. O que os dois benchmarks medem — e as armadilhas de métrica

### 1.1 SWE-bench, Lite e Verified

| benchmark | tamanho | domínio | oráculo | o que mede | o que esconde |
|---|---|---|---|---|---|
| SWE-bench [S1] | 2.294 issues | 12 repos Python | `FAIL_TO_PASS` + `PASS_TO_PASS`, escondidos do agente | patch que faz os dois passarem | tarefas impossíveis/ambíguas; testes superespecíficos; contaminação |
| SWE-bench Lite | 300 | subconjunto de bugfix | idem | comparação barata | seleção enviesada; poucos repos |
| SWE-bench Verified [S2] | 500 | 12 repos Python | idem, human-validated | issues com informação suficiente | 93 devs anotaram 1.699 amostras; 196 fáceis (<15 min), 45 difíceis (>1 h); testes ainda são oráculo incompleto |

O ponto operacional: o agente **não vê os testes**. Ele recebe a issue e o repo; a resolução é
decidida por testes que ele não pode ler. Isso separa dois problemas: localizar/reparar (o que o
agente faz) e passar no oráculo (o que o benchmark mede). Um patch plausível que passa nos testes
pode estar semanticamente errado — a §2.3 mostra que 34,6% dos patches plausíveis do AutoCodeRover
não eram corretos. Logo, `tests_passed` é evidência de *plausibilidade*, não de correção.

### 1.2 Terminal-Bench 2.0

89 tarefas, cada uma com ambiente próprio, solução humana e testes abrangentes [S3]. O paper reporta
frontier <65%: Codex CLI + GPT-5.2 63%, Terminus 2 + Opus 4.5 58%, Terminus 2 + Gemini 3 Pro 57%;
o melhor open-weight (Kimi K2 Thinking) 36%. O leaderboard de mar/2026 já mostra 78,4% (Forge/Gemini
3.1 Pro) — outro snapshot, outro harness; não misturar.

Três achados que importam para o jazz (§4 do paper):

1. **Falha de comando é dominada por binário ausente.** 24,1% de todas as falhas de comando são
   "executável não instalado ou fora do PATH"; 9,6% são falhas ao rodar o executável. A taxa de erro
   por comando varia de 9,2% (Grok 4) a 26,7% (GPT-OSS-120B), em 3.800 falhas amostradas.
2. **Mais turnos/tokens não predizem sucesso.** Não há correlação entre turnos médios e resolução,
   nem entre tokens gerados e resolução (§4.1, Figs. 35–36). O gargalo é qualidade de ação e
   verificação, não volume.
3. **A taxonomia de falha é Execution/Coherence/Verification**, com subclasses como *disobey task
   specification*, *step repetition*, *unaware of termination conditions*, *context loss*,
   *premature termination*, *no or irrelevant verification*, *reasoning–action mismatch* e *weak
   verification* (App. C). Verificação fraca é uma categoria de primeira classe.

Terminus 2 (App. F) é o contraponto minimalista: laço em que o modelo emite teclas executadas num
tmux dentro de Docker, sem ferramentas dedicadas de edição/busca; só bash, mais um módulo de
sumarização de contexto. Ele chega a 58% com Opus 4.5. Isso não prova que bash-only é ótimo; prova
que um laço mínimo, com ambiente e testes corretos, compete. O jazz offline é ainda mais restrito:
bash + índice + verificação, sem modelo.

### 1.3 As quatro armadilhas de métrica

| armadilha | exemplo | consequência |
|---|---|---|
| **"Contains GT" vs "Acc@k estrito"** | Agentless: arquivo 81,67% "contém o GT" [S4, Tabela 2]; LocAgent: Acc@5 88,32% exigindo **todos** os locais [S5, Tabela 6] | comparar 81,7 com 88,3 é comparar conjuntos diferentes; não fazer |
| **pass@1 vs pass@k** | SWE-agent: 17,94% pass@1 → 32,67% pass@6 em 6 runs [S6, Tabela 10] | test-time compute barato muda o número; reportar k |
| **plausível vs correto** | AutoCodeRover: 78 plausíveis, 51 corretos (65,4%) [S7, §6.1] | `tests_passed` não é correção; auditar amostra |
| **snapshot de leaderboard vs paper** | TB2 paper 63% máximo; tbench.ai mar/2026 78,4% [S3 vs harness-sota-gap S2] | não usar rank de época diferente como ablação |

### 1.4 Onde os harnesses frontier falham — e qual alavanca é determinística

| falha | evidência | alavanca offline do jazz | já existe? |
|---|---|---|---|
| não rodam offline | todos | brain determinístico | sim (`brain.go:41`) |
| binário ausente | 24,1% das falhas de comando no TB2 [S3, §4.5] | preflight de comando + remediação exata | parcial (`caps.go:161`, `methods.go:1376`) |
| verificação fraca/ausente | taxonomia TB2; 52% dos insucessos do SWE-agent são implementação errada/overespecífica [S6, B.4] | verificador por execução, `--until`, registry | parcial (`methods.go:1680`, `agent_loop.go:196`) |
| recuperação de edição falha | 23,4% dos insucessos do SWE-agent [S6, B.4] | edição tipada + lint + checkpoint + revert | parcial (`edit.go:107`, `checkpoint.go:257`) |
| localização em linha falha | Agentless+RepoGraph: linha 34,3% com LLM [S8, Tabela 3] | top-3 regiões + abstenção + verificação | não (`G1`) |
| não param cedo | TB2 *unaware of termination* | pré-condição de método falha em ms | sim (`methods.go:141`) |

A conclusão honesta: o jazz não vai ganhar do Claude Code/Codex em síntese de patch para SWE-bench.
Pode ganhar no **substrato determinístico** — pré-voo, localização com abstenção, reparo mecânico
verificado, verificação e parada precoce — e pode *reduzir* o custo deles quando o modelo volta
(B2 do `harness-sota-gap.md`).

---

## 2. Técnicas, paper por paper

### 2.1 Agentless (arXiv 2407.01489, Xia et al., 2024) — o pipeline fixo vence o agente livre

**Método.** Três fases, sem o LLM decidir ações [S4, §3]:

1. **Localizar arquivos.** Serializa a árvore do repo (formato `tree`), pede ao LLM os top-N arquivos
   suspeitos; em paralelo, filtra pastas irrelevantes por LLM, chunka os arquivos restantes, embeda
   issue e chunks, e recupera por cosseno; combina as duas listas por interseção/frequência.
2. **Localizar elementos.** Para cada arquivo, monta um *skeleton* (só cabeçalhos de classes/funções,
   campos e assinaturas, comentários de módulo/classe) e pede ao LLM a lista de classes/funções
   relacionadas.
3. **Localizar edição.** Dá o código completo dos elementos e pede classes/funções/linhas finais.
4. **Reparar.** Amostra N patches por conjunto de locais; contexto = janela em volta do elemento.
5. **Validar.** Gera testes de reprodução por LLM, executa no código original, seleciona o teste que
   reproduz; usa reprodução + regressão para ranquear patches; voto majoritário como fallback.

**Números-chave.** 32,00% (96/300) no Lite, US$ 0,70/tarefa [S4, abstract]; 38,80% (194/500) no
Verified com GPT-4o [S4, Tabela 6]. Localização (contém GT): arquivo 81,67%, elemento 58,33%,
edição 50,67% no modo greedy; LoC médio 3.424 → 698 → 189; custo por etapa US$ 0,06 + 0,02 + 0,06
[S4, Tabela 2].

**Ablações que importam.**

| ablação | resultado | leitura |
|---|---|---|
| arquivo: só prompting / só embedding / combinado | 78,67% / 70,33% (com filtro) / **81,67%** | os dois sinais são complementares; nenhum sozinho basta |
| elemento: arquivo inteiro vs skeleton | 53,67% vs **58,33%**; 778 vs 698 LoC; US$ 0,15 vs 0,02 | contexto maior piora e custa 7,5× |
| edição: direto do arquivo vs hierárquico | 47,00% vs **50,67%** | o passo intermediário de elemento não é cerimônia |
| edição: multi-sample merged vs separado | 56,33% contém GT (merged) vs 49,67% por amostra | merged aumenta recall mas infla contexto; separado + seleção ganha no fim |
| reparo: 1 greedy vs 4×10 amostras | 80 vs **96** fixes | mais amostras só pagam com seletor |
| todos os patches (sem escolher) | 126 (42,0%) | teto superior; seleção é o gargalo |
| seleção: voto / +regressão / +reprodução | 77 / 81 / **96** | reprodução vale +15 fixes, +US$ 0,25 |

**O que transfere offline.** A hierarquia (arquivo→elemento→linha), o skeleton como representação
compacta, top-N em vez de top-1, e a seleção por execução. O que **não** transfere: o LLM em cada
etapa, embeddings e a geração de testes de reprodução.

**Limitação.** LLM em todas as fases; embeddings; só Python/issue→patch; a geração de teste de
reprodução é caríssima e falha; o Lite tem patches GT exatos e issues enganosas (o paper cria o
Lite-SS para isso). O 81,67% é "contém", não "acertou todos" — não comparar com Acc@k estrito.

### 2.2 SWE-agent (arXiv 2405.15793, Yang et al., 2024) — a ACI é o produto

**Método.** Um laço ReAct com interface dedicada [S6, §2–3]: *file viewer* de 100 linhas com
`open/goto/scroll`; *editor* que aplica substituição multi-linha e roda **linter** antes de aceitar;
*busca* que lista arquivos/trechos de forma sumarizada; histórico das últimas 5 observações;
guardrails contra edições inválidas; feedback "informativo mas conciso". O baseline é shell-only.

**Números-chave.** 12,47% (286/2.294) no SWE-bench completo e 18,00% (54/300) no Lite, US$ 1,67/tarefa
no Lite; shell-only 11,00%; shell-only sem demonstração 7,33% [S6, Tabela 2]. O abstract fala em
"+10,7 pp" contra o baseline; a Tabela 2 mostra +7,0 pp contra shell-only com demonstração e +10,67
contra shell-only sem demonstração — a diferença é a linha de base, não um erro de conta. HumanEvalFix
87,7% [S6, Tabela 2].

**Ablações (Lite, GPT-4 Turbo) [S6, Tabela 3].**

| componente | variante | % resolvido |
|---|---|---|
| editor | edição padrão / com linting / sem editor | 15,0 / **18,0** / 10,3 |
| busca | sumarizada / iterativa / sem busca | **18,0** / 12,0 / 15,7 |
| file viewer | 30 linhas / 100 linhas / arquivo inteiro | 14,3 / **18,0** / 12,7 |
| contexto | últimas 5 obs. / histórico completo / sem demo | **18,0** / 15,0 / 16,3 |

**Modos de falha [S6, B.4].** 52,0% dos insucessos são implementação incorreta ou overespecífica;
23,4% são falha de recuperação de edição (loop de edits inválidos); o resto (<25%) inclui não achar
o arquivo, não achar o local, estourar orçamento e desistir. Ou seja: **localização não é o único
gargalo; reparo e recuperação de edição pesam mais**.

**O que transfere offline.** Os princípios de ACI: janela de 100 linhas, lint no edit, busca
sumarizada (lista, não despeja trechos), últimas 5 observações, output vazio explícito. O jazz já
tem parte disso (`truncate.go`, `edit.go`, `guard_honest.go`); o que falta é usar o linter como
pré-condição do reparo mecânico, não como conselho ao modelo.

**Limitação.** Era GPT-4 Turbo; 2.294 tarefas; nenhum modo offline; custo 8–13× o RAG; a ACI é
avaliada com um modelo forte e não necessariamente transfere para modelos pequenos. E, como o
próprio paper mostra, o gargalo maior é reparo, não busca.

### 2.3 AutoCodeRover (arXiv 2404.05427, Zhang et al., ISSTA 2024) — estrutura AST + SBFL

**Método.** O LLM extrai palavras-chave da issue e chama APIs de busca **em nível de AST**:
`search_class`, `search_class_in_file`, `search_method`, `search_method_in_class`,
`search_method_in_file`, `search_code`, `search_code_in_file`; busca estratificada (classe →
método → código) e iterativa, no máximo 10 rodadas [S7, §4.2–4.3]. Se há suíte de testes, roda
**SBFL** (Ochiai) em nível de método e passa os top-5 métodos como dicas adicionais; o patch é
validado pela suíte, com até 3 retries [S7, §4.4–4.5].

**Números-chave.** 19,00% (57/300) no Lite, 195 s e 37k tokens (US$ 0,43) por tarefa; com SBFL,
22,00% (66/300); @3, 26,00% (78/300), 520 s e 112k tokens. SWE-agent no mesmo quadro: 18,00%,
245k tokens, US$ 2,51 [S7, Tabela 2]. No completo, 12,42% @1. Correção: 65,4% dos 78 plausíveis
(51 corretos) [S7, §6.1].

**Taxonomia de falha (300 tarefas) [S7, §6.3].** 26,0% sucesso; **29,3% localizaram todos os
métodos certos mas geraram patch errado**; 20,0% acertaram o arquivo e erraram o local; o resto é
arquivo errado/sem patch. A leitura para o jazz: localização correta é necessária e **não
suficiente**; o maior bucket isolado é reparo.

**Ablação SBFL.** +3 pp (19→22), 7 tarefas resolvidas só com SBFL; o LLM não usa cegamente o top-1,
e sim os métodos 3º/5º que casam com a issue [S7, §6.2]. SBFL precisa da suíte completa do
desenvolvedor; em tarefa real, muitas vezes não há.

**O que transfere offline.** A ideia de APIs de busca estruturais (nós podemos fazer com regex de
símbolos + spans, sem AST), a taxonomia de triagem (local certo/patch errado vs local errado), e
SBFL como capacidade **opcional** quando há cobertura. O que não transfere: o LLM escolhendo APIs,
o parser AST Python, e a validação por suíte completa como padrão.

**Limitação.** Só Python/AST; o agente LLM decide a busca; APIs não implementadas causaram 23
tarefas que o SWE-agent resolveu [S7, §6.1]; SBFL depende de testes e de instrumentação; o custo
de 37k tokens ainda é 37k tokens.

### 2.4 OpenHands / OpenDevin (arXiv 2407.16741, Wang et al., ICLR 2025) — evento, sandbox, plataforma

**Método.** Arquitetura de *event stream* (tudo é evento imutável), runtime Docker com bash, browser
e IPython, skills como ACI extensível, delegação multi-agente, 15 benchmarks integrados [S9, §2].
O agente padrão é CodeAct: o modelo emite código/ações, o runtime executa e devolve observação.

**Números-chave.** CodeActAgent v1.8 com Claude 3.5 Sonnet: 26,0% no SWE-bench Lite, US$ 1,10/tarefa
[S9, §4.2]; 15 benchmarks; 2,1 mil contribuições de 188 contribuidores. A localização é dirigida
pelo agente (bash/editor), sem índice determinístico próprio.

**O que transfere offline.** Event-sourcing para auditoria/replay (o jazz já grava eventos de tarefa
em JSONL, `task.go:256`), sandbox e permissão por ação. O *Condenser* já está coberto em
`harness-sota-gap.md` §2; não repetir. O que não transfere: a pilha Python/Docker, o custo, e a
dependência de modelo em todas as ações.

**Limitação.** Pesado para 2 GB; sem offline; sem localização determinística; o event stream é
ótimo para auditabilidade, mas não reduz tokens sozinho.

### 2.5 RepoGraph (arXiv 2410.14684, Ouyang et al., ICLR 2025) — grafo de linhas, 1-hop é o ponto

**Método.** Grafo em nível de linha: cada nó é uma linha; arestas representam definição–referência,
import, contenção e herança. Constrói o grafo do repo, indexa, e recupera o **ego-grafo k-hop** a
partir de palavras-chave da issue; achata o subgrafo ou pede um resumo ao LLM; injeta no Agentless
ou no SWE-agent [S8, §3].

**Números-chave.** Média de 1.419,3 nós e 26.392,1 arestas por repo. Ganho absoluto: Agentless
27,33→29,67 (+2,34), SWE-agent 18,33→20,33 (+2,00), AutoCodeRover 19,00→21,33 (+2,33), RAG
2,67→5,33 (+2,66); a “melhora relativa média de 32,8%” do abstract é puxada pelo RAG (2,67→5,33,
+99,6% relativo); nos métodos fortes o ganho relativo é 8,6–12,3% [S8, Tabela 2]. Localização do
Agentless: arquivo 68,7→74,3, função 51,0→54,0, linha 34,3→36,7 [S8, Tabela 3].

**Ablação de contexto [S8, Tabela 4].**

| variante | nós/arestas | tokens | resolve |
|---|---|---|---|
| 1-hop achatado | 11,6 / 37,1 | 2.310,7 | **29,67** |
| 1-hop resumido por LLM | 11,6 / 37,1 | 717,5 | 28,33 |
| 2-hop achatado | 54,5 / 89,9 | 10.505,3 | 26,00 |
| 2-hop resumido | 54,5 / 89,9 | 1.229,2 | 28,67 |

A lição é contraintuitiva e valiosa: **mais grafo não é melhor**. 2-hop achatado é pior que o
baseline e gasta 4,5× mais tokens. 1-hop achatado é o ótimo; resumir 1-hop perde informação.

**O que transfere offline.** A expansão 1-hop a partir de uma semente (símbolo/arquivo), com cap
de ~12 nós, é implementável com `rg` e regex, sem parser. O grafo completo de 26k arestas não é
viável no Dell (parser + memória), mas o ego-grafo de 1-hop é. O que não transfere: resumo por LLM
e construção de grafo em nível de linha.

**Limitação.** Parser Python; o ganho é modesto (+2,3 pp); o resumo por LLM é dependência; 2-hop
é ruído; o custo para agentes sobe (+US$ 0,13–0,18) [S8, §4.2].

### 2.6 LocAgent (arXiv 2503.09089, Chen et al., 2025) — o baseline BM25 é o número mais útil

**Método.** Grafo dirigido heterogêneo com nós de arquivo/classe/função e arestas de contenção,
import, invocação e herança; índice **BM25** sobre IDs de entidade; ferramentas `SearchEntity`
(BM25), `TraverseGraph` (BFS tipado) e `RetrieveEntity` (código); o agente LLM decide a navegação;
Qwen2.5 7B/32B fine-tuned para localizar [S5, §3]. Cria o Loc-Bench (660 exemplos; bug, feature,
segurança, performance) para reduzir contaminação.

**Números-chave (SWE-bench Lite, 274/300 tarefas; Acc@k exige **todos** os locais) [S5, Tabela 4].**

| método | modelo | arquivo Acc@1/3/5 | função Acc@5/10 |
|---|---|---|---|
| BM25 puro | — | 38,69 / 51,82 / **61,68** | 31,75 / 36,86 |
| CodeRankEmbed | — | 52,55 / 77,74 / **84,67** | 51,82 / 58,76 |
| Agentless | Claude 3.5 | 72,63 / 79,20 / 79,56 | 58,76 / 58,76 |
| MoatlessTools | GPT-4o | 73,36 / 84,31 / 85,04 | 57,30 / 59,49 |
| SWE-agent | Claude 3.5 | 77,37 / 87,23 / 90,15 | 64,23 / 64,60 |
| OpenHands | Claude 3.5 | 76,28 / 89,78 / 90,15 | 68,25 / 70,07 |
| LocAgent | Claude 3.5 | 77,74 / 91,97 / **94,16** | 73,36 / **77,37** |
| LocAgent | Qwen2.5-32B ft | 75,91 / 90,51 / 92,70 | 71,90 / 77,01 |

**Ablações (Qwen2.5-7B ft; arquivo Acc@5 / módulo Acc@10 / função Acc@10) [S5, Tabela 6].**

| variante | arquivo | módulo | função |
|---|---|---|---|
| completo | **88,32** | **82,85** | **71,53** |
| sem `TraverseGraph` | 86,13 | 78,47 | 66,06 |
| só relação `contain` | 86,50 | 79,56 | 66,42 |
| `TraverseGraph` 1-hop | 86,86 | 80,29 | 66,79 |
| sem `RetrieveEntity` | 87,59 | 81,39 | 69,34 |
| sem `SearchEntity` | 68,98 | 61,31 | 53,28 |
| sem índice BM25 | 75,18 | 68,98 | 60,22 |

**O que transfere offline.** O número que mais importa para o jazz é o **BM25 puro: 38,69% Acc@1 e
61,68% Acc@5 em arquivo**, sem LLM e sem embeddings. Isso é o piso realista de um localizador
lexical. O grafo adiciona ~2–7 pp (função 66,79→71,53 com multi-hop), mas exige decisão do agente.
O `SearchEntity` (BM25) é o componente mais crítico: removê-lo derruba arquivo de 88,32 para 68,98.
O que não transfere: o agente LLM decidindo hops e os modelos fine-tuned.

**Limitação.** O agente decide a navegação; o grafo é construído por parser; o benchmark é Python;
Acc@k estrito é mais duro que "contains GT"; os modelos fine-tuned não são offline. E o BM25 usa
**conteúdo** dos arquivos, não só nomes — nosso índice de nomes tende a ficar abaixo de 38,69/61,68
até lermos conteúdo (ver §3.1).

### 2.7 Moatless Tools (repo, 2025) e SWE-Search (arXiv 2410.20285, 2025) — ferramentas > raciocínio

**Método.** Moatless parte de uma tese explícita: "em vez de confiar que o agente raciocina até a
solução, construa boas ferramentas que inserem o contexto certo no prompt e tratam a resposta"
[README]. O fluxo tem ações de inspeção (achar classe/função/trecho, ver código), busca semântica
com embeddings (Voyage) e um "code inspector". O SWE-Search é a versão de pesquisa: MCTS com
`Value Agent` (avaliação numérica + qualitativa) e `Discriminator Agent` (debate multi-agente)
[S10].

**Números-chave.** 70,8% no SWE-bench com Claude 4 Sonnet, US$ 0,63/instância [README, 2026];
39,0% no Lite com Claude 3.5 (jan/2025) e 30,67% com DeepSeek V3 [experiments.moatless.ai]. Na
localização do LocAgent, Moatless com GPT-4o: arquivo 85,04 Acc@5, função 59,49 Acc@10 [S5, Tabela
4]. SWE-Search: +23% relativo em cinco modelos [S10, abstract].

**O que transfere offline.** O desenho de ferramentas de inspeção (ver classe/função/trecho, não
arquivo inteiro), e a ideia de que a qualidade do contexto inserido importa mais que o laço. O que
não transfere: o vector store (embeddings) e o MCTS multi-agente (muitas chamadas de LLM).

**Limitação.** Moatless atual não tem paper revisado; a versão de 70,8% depende de Claude 4 Sonnet
e de embeddings; o índice vetorial fica stale quando o repo muda; o SWE-Search é caro e depende de
valor/discriminador LLM. Para 2 GB/offline, é dependência em todos os componentes centrais.

### 2.8 Co-mudança (Zimmermann et al., ICSE 2004) — 64% no top-3, mas 15% de recall

**Método.** Minera o histórico de versões em **transações de mudança** e deriva regras de associação
"programadores que mudaram X também mudaram Y" com suporte e confiança; o protótipo ROSE sugere
mudanças relacionadas depois de uma mudança inicial [S11, §2–4]. Funciona em nível de arquivo e de
função/variável.

**Números-chave.** Em oito projetos open-source, com uma mudança inicial, o ROSE prevê 26% dos
arquivos seguintes (precisão) e 15% do total de mudanças (recall); **as três sugestões do topo
contêm um local correto em 64% dos casos**; consulta em no máximo 0,5 s [S11, §6.4; S12]. Os
limiares de navegação do experimento foram suporte 1 e confiança 0,1.

**O que transfere offline.** É o sinal histórico mais diretamente útil: dado o arquivo que falhou
(ou o arquivo mais bem pontuado pelo léxico), os arquivos que co-mudam com ele são candidatos. O
top-3 a 64% é forte para um sinal que custa um `git log` e não depende de modelo. Mas 15% de recall
significa que ele **não substitui** o léxico: é booster.

**Limitação.** Precisa de histórico e de uma mudança inicial; cold start em repo novo; merges,
squashes e renomeações criam pares espúrios; commits gigantes (>30–50 arquivos) geram co-ocorrência
falsa; os projetos de 2004 são C/C++ grandes, não repos pequenos de 2026; associação não é causação.
Regras de suporte 1/confiança 0,1 são ruidosas — para uso offline, exigir suporte ≥2 e confiança
≥0,3 (calibrar) e ignorar commits grandes.

### 2.9 Churn relativo (Nagappan & Ball, ICSE 2005) — 89% para "provável defeito", não para "onde"

**Método.** Define métricas de churn **relativo** (LOC mudado / LOC total, deletado / total, arquivos
mudados / contagem de arquivos, contagem de churn / arquivos mudados etc.), ajusta regressão e
prediz densidade de defeitos por binário no Windows Server 2003 [S13].

**Número-chave.** O conjunto de métricas relativas discrimina binários fault-prone vs não fault-prone
com **89,0% de acurácia**; churn absoluto é mau preditor [S13, abstract].

**O que transfere offline.** Apenas como **desempate**: um arquivo com churn relativo alto é mais
provável de conter defeito, mas isso não diz *onde* no arquivo nem *qual* arquivo para uma issue
específica. Peso baixo, nunca sozinho. Normalizar por tamanho e idade, excluir vendor/gerado, dar
decay temporal.

**Limitação.** Nível de binário, densidade de defeitos, um único produto industrial; não é
localização; depende de histórico; churn absoluto engana; em repo pequeno, o sinal é fraco.

### 2.10 Terminal-Bench 2.0 (arXiv 2601.11868, Merrill et al., 2026) — o que o terminal ensina

**Método.** 89 tarefas, ambiente Docker único por tarefa, solução humana e testes abrangentes;
Harbor como harness; Terminus 2 como agente de referência (bash/teclas em tmux + sumarização de
contexto) [S3, §2–3, App. F]. A análise de erro usa Docent + LLM judge, com 93% de Cohen's κ em 20
traços de calibração e 90% de concordância com 120 traços humanos.

**Números-chave.** <65% no paper; 24,1% das falhas de comando são binário ausente/fora do PATH;
9,6% são falha ao executar; taxa de erro por comando 9,2–26,7%; sem correlação turnos/tokens com
sucesso [S3, §4]. A taxonomia Execution/Coherence/Verification e as subclasses estão em App. C.

**O que transfere offline.** (a) Pré-voo de binário: se 24,1% das falhas são comando que nem existe,
um `exec.LookPath` antes de executar elimina uma fração grande de erros sem modelo; (b) verificação
explícita e condição de terminação: o brain já tem `--until` e verificador, mas o modelo não é
obrigado a usá-los; (c) laço mínimo: Terminus 2 mostra que bash-only compete, então o brain não
precisa de uma API rica para ser útil; (d) não medir sucesso por número de turnos.

**Limitação.** Internet liberada (contaminação possível); 89 tarefas; Docker/Harbor; o paper é de
jan/2026 e o leaderboard de mar/2026 já mudou; não é offline nem 32-bit.

---

## 3. A pilha determinística de localização

### 3.1 Sinais, força e custo

O localizador offline não tem um sinal mágico; tem vários sinais fracos que, combinados com
abstenção, são úteis. A tabela abaixo é a hipótese de trabalho; os pesos e a ordem serão calibrados
(§8), não assumidos.

| sinal | fonte no jazz | força esperada | custo Dell | modo de falha |
|---|---|---|---|---|
| frame de falha (`file:line`) | `outparse.go:1625` → `opTrace.Frames`; `methods.go:1408` | altíssima quando existe | 0 (já parseado) | issue-only não tem frame; teste pode falhar em arquivo de teste |
| caminho/símbolo exato citado | `nameindex.go:520` `resolveAll`, `:1035` `niResolveEntity` | alta precisão, baixa recall | <1 ms (índice quente) | nome ambíguo; typo; símbolo duplicado |
| teste↔implementação (`niCore`) | `nameindex.go:185` `niCore`, `:167` `niTestAffix` | média | <1 ms | monorepo com nomes iguais; teste de integração |
| BM25 sobre nomes de path+símbolo | novo, sobre `niIndex.files`/`symbols()` | média | 2–8 ms | vocabulário da issue não casa com identificador |
| BM25 sobre conteúdo (top-N) | `rg -c`/`rg -n` limitado | média-alta | 30–200 ms quente; 0,3–1,5 s frio | HDD, arquivos grandes, termos genéricos |
| co-mudança | `git log --name-only` (novo) | média (top-3 64% com semente) | 50–300 ms quente; 0,5–1,5 s frio | histórico curto, merges, commits grandes |
| churn relativo | `git log --numstat` (novo) | fraca (desempate) | compartilha o mesmo `git log` | hotspots antigos, gerado/vendor |
| expansão 1-hop (call/import) | `rg` + regex | média | 20–60 ms | regex confunde chamada com declaração |
| recência/tocado na sessão | `nameindex.go:686` `recency`, `niTouch` | fraca | <1 ms | viés para o que acabou de mudar |
| tipo da issue (bug/feature/perf/security) | léxico determinístico | fraca | <1 ms | overfit de palavra |

**Piso realista.** O BM25 puro do LocAgent, com **conteúdo** e 274 tarefas, dá 38,69% Acc@1 e
61,68% Acc@5 em arquivo [S5, Tabela 4]. Nosso índice de nomes, sem conteúdo, deve ficar abaixo
disso. A meta do `localize` não é 90%; é bater o BM25 de nomes e o `niResolveEntity` atuais, com
abstenção honesta, e medir. Se não bater, a proposta é falsificada e deve sair.

### 3.2 Hierarquia e granularidade: por que não prometer linha exata

Agentless mostra o funil: 81,67% dos arquivos contêm o GT, 58,33% dos elementos, 50,67% dos locais
de edição no modo greedy [S4, Tabela 2]. Com LLM e contexto completo, o RepoGraph mede **34,3% de
acerto exato de linha** para o Agentless [S8, Tabela 3]. AutoCodeRover: 20,0% dos casos acertam o
arquivo e erram o local [S7, §6.3]. Ou seja: linha exata é o passo mais difícil até para modelos
fortes.

Consequência de projeto: o `localize` offline deve devolver **regiões** (símbolo + span + até 3
linhas candidatas), não uma linha única. A métrica deve ser `line_hit@1±5` (a linha prevista está a
até 5 linhas de alguma linha do patch GT) e `element_hit@3`, não "linha exata". Prometer linha exata
é vender o que os papers não entregam.

### 3.3 Fusão de sinais: soma de ranks, pesos congelados, abstenção

Não usar probabilidade calibrável; usar **rank fusion** inteira, barata e interpretável:

```
para cada arquivo f:
  score(f) = Σ_s  w_s * norm_s(f)          // norm_s ∈ [0,100], por rank dentro do sinal
  conf(f)  = clamp(score(f) - margin_penalty, 0, 100)
top-3 por score; se score(top1) < T_abs ou (score(top1)-score(top2)) < T_margin:
  devolver "sem sinal suficiente" + top-3 marcados como baixa confiança (ou nil)
```

Pesos iniciais (a calibrar; não são verdade): frame 40, caminho exato 35, símbolo exato 30,
teste↔impl 12, BM25 nomes 20, BM25 conteúdo 15, co-mudança 12, 1-hop 8, recência 6, churn 4,
tipo de issue 4. A soma não é 100 de propósito: é um modelo linear de evidências, não uma
probabilidade. `T_abs` e `T_margin` saem da curva precision/abstention nos fixtures (§8); a
abstenção é uma feature, não um bug.

Por que rank e não score bruto: os sinais têm escalas incompatíveis (distância de edição, contagem
BM25, confiança de regra). Rank fusion evita normalização frágil e mantém tudo em `int16`/`int32`
no 386. A calibração de limiares segue `math-offline-brain.md` §2.1 (curva de decisão), sem
repetir a derivação.

### 3.4 Expansão 1-hop, nunca 2-hop

RepoGraph: 1-hop achatado 29,67% com 2.310,7 tokens; 2-hop achatado 26,00% com 10.505,3 tokens
[S8, Tabela 4]. A interpretação: o ego-grafo de 1-hop ao redor de um símbolo certo é contexto útil;
2-hop traz o repo inteiro e afoga o sinal. No jazz, 1-hop = para cada semente (símbolo/arquivo):
(a) linhas de import que apontam para o arquivo; (b) chamadas `nome(` no repo, limitadas a 20 hits;
(c) definições de símbolos citados no corpo do elemento, via regex. Cap de 12 nós, dedupe por
arquivo, peso baixo. Se o cap estourar, descartar o sinal em vez de truncar aleatoriamente.

### 3.5 Co-mudança: engenharia, não só a regra

A proposta C3 de `offline-brain-research.md` já fixou a semente: `git log -n 500 --name-only`,
ignorar commits com >30 arquivos, suporte ≥2, confiança ≥0,1, cache `cochange.json`. Este doc
avança no que a paper não resolve:

1. **Uma passada de git para dois sinais.** `git log --no-merges --numstat -z -M --format=...` dá,
   por commit, `added\tdeleted\tpath` e a lista de paths; co-mudança e churn saem da mesma passada.
   Renomeações com `-M` aparecem como registro especial (old/new) no `-z`; parsear. Se o git do
   Dell não emitir o par como esperado, rodar uma segunda passada `--name-status -z -M` só para o
   mapa de rename — nunca duas passadas para a mesma coisa.
2. **Excluir merges e commits gigantes.** `--no-merges`; descartar commits com >50 arquivos
   (ajustar); commits de release/format/lint geram co-ocorrência espúria.
3. **Normalizar renomeações.** `--name-status -z -M` dá `R100 old new`; usar o path novo e mapear
   o antigo. Sem isso, o histórico some depois de um rename.
4. **Suporte/confiança/lift.** Guardar `(seed, cand, support, confidence, lift)`; exigir
   `support ≥ 2`, `confidence ≥ 0,3`, `lift ≥ 1,5` (calibrar). O ROSE usou 1/0,1 para navegação;
   para ranking automático, mais conservador.
5. **Recência.** Peso `2^(-idade/180d)` por commit; regra antiga ainda vale, mas menos.
6. **Cache por HEAD.** `dataDir()/cochange/<hash(root+HEAD)>.json`; invalidar quando HEAD muda;
   rebuild bounded (400–500 commits) com timeout de 2 s. Sem git, o sinal fica indisponível e o
   localizador não bloqueia.
7. **Semente.** O arquivo do frame de falha (se houver), senão o top-1 lexical. Nunca semear com
   todos os arquivos: vira ruído.

O número de Zimmermann (top-3 64%) é com uma mudança inicial; no nosso caso, a "mudança inicial" é
a falha/arquivo suspeito. É exatamente o cenário de `fix` com teste falhando. Para issue-only, o
sinal é mais fraco.

### 3.6 Churn: desempate, não ranking

Nagappan & Ball medem densidade de defeitos por binário, não localização. O uso honesto: se dois
arquivos têm score lexical próximo, o de maior churn relativo sobe; se o churn for a única evidência,
não promover. Fórmula: `churn_rel = (added+deleted) / (loc+1)`, normalizado por rank no repo;
`commits_rel = commits / idade_em_semanas`; combinar com peso 4 e cap. Excluir `vendor/`, `third_party/`,
`generated/`, `*.pb.go`, `dist/`, `build/`. Sem histórico, pular.

### 3.7 A fronteira do reparo mecânico

`mechrepair.go` hoje tem três funções e **nenhum chamador de runtime** (só `mechrepair_test.go`):
`mrParseGoErrors` (regex `file:line:col`), `mrClassify` (6 classes) e `mrRecipe` (2 receitas:
`goimports -w .` e `go mod tidy`). O resto volta `""` e escala para o modelo. Isso é uma biblioteca
órfã, não uma capacidade. A tabela abaixo define o que é mecânico de verdade e o que não é.

| linguagem | diagnóstico | mecânico? | operador determinístico | risco | verificação |
|---|---|---|---|---|---|
| Go | `imported and not used` | sim | remover a linha do import (ou `goimports -w`) | baixo | `go build` |
| Go | `declared and not used: x` | sim | inserir `_ = x` após a declaração (preserva efeitos colaterais) | baixo | `go build` |
| Go | `missing go.sum entry` | condicional | `go mod tidy` só se o module cache estiver completo (`GOPROXY=off`) | médio | `go build` |
| Go | `undefined: X` | condicional | se X tem um único símbolo a distância ≤2, renomear o uso; se X é membro de pacote importável, adicionar import | alto | `go build` + testes |
| Go | `not enough/too many arguments` | não | exige semântica de chamada | — | escalar |
| Go | `syntax error` | não | ambíguo | — | escalar |
| Python | `ModuleNotFoundError: No module named 'x'` | condicional | se `x` é módulo local, corrigir o path; se é terceiro, bloquear com comando de install (offline: sem rede) | médio | import/teste |
| Python | `NameError: name 'x' is not defined` | condicional | único símbolo a distância ≤2 → renomear uso | alto | teste |
| Python | `unused import` (pyflakes/ruff) | sim | remover a linha | baixo | lint/teste |
| Python | `IndentationError`/`TabError` | não | ambíguo | — | escalar |
| Python | `AttributeError: 'X' has no attribute 'y'` | condicional | único atributo próximo na classe → renomear | alto | teste |
| JS/TS | `ReferenceError: x is not defined` | condicional | único símbolo próximo → renomear | alto | teste |
| JS/TS | `Cannot find module './x'` | condicional | se existe com outra caixa/extensão, corrigir o path; senão bloquear com install | médio | teste |
| JS/TS | `Unexpected token` | não | parser | — | escalar |
| JS/TS | ESLint `no-unused-vars` | sim | remover/prefixar `_` | baixo | lint/teste |
| Rust | `unused import` | sim | remover a linha | baixo | `cargo build` |
| Rust | `cannot find value x` | condicional | único símbolo próximo → renomear | alto | build/teste |
| Rust | `mismatched types`/borrow checker | não | exige semântica | — | escalar |
| testes | `expected X got Y` | **não** em teste protegido | nunca editar asserção; se for snapshot gerado, rodar o comando de update | — | teste |
| testes | timeout/flake | não | re-run com alvo menor, não patch | — | 2 runs |

Duas regras não negociáveis:

- **Nunca aplicar sem verificação.** O operador só conta como sucesso se o mesmo verificador que
  detectou a falha rodar de novo e passar. Sem verificador → `unverified`, não `succeeded`.
- **Nunca editar teste protegido.** `hgKind` (`guard_honest.go:246`) classifica `test`, `testcfg`,
  `ci`, `section`; o brain hoje **não** passa por `hgPreTool`/`hgState.judge` ao escrever
  (`brFileOp`, `methods.go:512`). Antes de ligar `fix.mechanical`, o write do brain precisa de um
  guard equivalente (`hgBrainWrite`), senão o caminho mecânico vira bypass do guardrail.

Um detalhe de implementação: `outparse.go` já devolve `opDiag{File,Line,Col,Severity,Code,Msg}` para
Go, gcc, tsc, rustc, Python e Node (`outparse.go:450`, `:1050–1111`). `mechrepair.go` reparseia o
texto com `mrGoDiag`, duplicando e cobrindo só Go. O caminho certo é consumir `opResult.Data.Diags`
e mapear `(format, code, msg)` → receita; `mrParseGoErrors` fica como fallback para texto cru.

### 3.8 Honestidade: testes são oráculo incompleto

`tests_passed` prova plausibilidade, não correção (AutoCodeRover: 65,4% dos plausíveis corretos).
O SWE-bench Verified existe porque testes podem ser superespecíficos e issues ambíguas [S2]. Então:

- Métrica de produto: `verified_success` (passou no verificador) **e** `correct_rate` em amostra
  auditada manualmente. Reportar as duas; nunca só a primeira.
- `false_success` deve ser 0 por construção: o status `succeeded` exige evidência (`task.go:160`).
- `false_blocks` deve ser 0: o localizador não pode bloquear uma tarefa que o brain resolveria; a
  abstenção é permitida, o bloqueio não.
- Não chamar patch plausível de "correto" no relatório; chamar de "verificado".

---

## 4. Regra de ouro: o que roda offline e o que é dependência

| técnica | classificação | por quê | evidência |
|---|---|---|---|
| índice de nomes/paths/símbolos | **offline asset** | estruturas persistidas, sem modelo | `nameindex.go:92`, `nameindex_idx.go:63` |
| BM25 sobre nomes e paths | **offline asset** | matemática inteira, sem modelo | LocAgent BM25 38,69/61,68 [S5] |
| spans de símbolos por regex | **offline asset** | regex por linguagem, sem AST | `niSymRE` `nameindex.go:199` |
| frames de traceback | **offline asset** | `outparse` já parseia | `outparse.go:1625`, `opFrame` |
| teste↔implementação | **offline asset** | `niCore`/`niTestAffix` | `nameindex.go:185` |
| co-mudança | **offline asset** | regras sobre `git log` | Zimmermann 64% top-3 [S11] |
| churn relativo | **offline asset** | `git log --numstat` | Nagappan 89% [S13] |
| expansão 1-hop | **offline asset** | `rg` + regex, cap 12 | RepoGraph 1-hop 29,67 [S8] |
| pré-voo de binário | **offline asset** | `exec.LookPath` + perfil | TB2 24,1% [S3] |
| receitas de reparo | **offline asset** | mapa erro→operador, congelado | §3.7; `mechrepair.go:45` |
| aplicação de patch + verify + revert | **offline asset** | `edit.go` + testes + checkpoint | `edit.go:107`, `checkpoint.go:257` |
| pesos do localizador | **compile-time asset** | calibrados na máquina de dev, congelados no binário | §3.3, §8 |
| fixtures de eval | **compile-time asset** | LLM forte forja uma vez; offline reusa | `evals/offline/tasks.json` |
| expansão semântica da issue | **dependência** | precisa de LLM para parafrasear/entender intenção | Agentless/AutoCodeRover |
| síntese de patch | **dependência** | raciocínio sobre semântica; não é mecânico | AutoCodeRover 29,3% local certo/patch errado |
| geração de teste de reprodução | **dependência** | LLM gera e valida; caro | Agentless +US$ 0,25 [S4] |
| resumo de subgrafo | **dependência** | LLM summarizer; 1-hop flat é melhor | RepoGraph 28,33 vs 29,67 [S8] |
| embeddings/vector store | **dependência** | modelo + índice denso + stale | Moatless/LocAgent/Agentless |
| SBFL | **offline opcional, dependente de instrumentação** | matemática determinística, mas precisa de cobertura | AutoCodeRover +3 pp [S7] |
| MCTS/SWE-Search | **dependência** | value/discriminator LLM, muitas chamadas | SWE-Search [S10] |
| resumo de contexto por LLM | **dependência** | 1 chamada grande por compactação | OpenHands/harness-sota-gap |
| tree-sitter/AST parser | **dependência de build** | cgo/parser grande; quebra 386 estático | `low-level-performance.md` §2 |

A linha divisória é operacional: **se a técnica precisa de um modelo para produzir a resposta em
runtime, é dependência; se produz a resposta com dados já no disco/binário, é asset.** O localizador
e o reparo mecânico são assets; a síntese de patch e a expansão semântica são dependências.

---

## 5. Mapa no jazz: feito, parcial, só pesquisado

| capacidade | arquivo:line | status | o que falta |
|---|---|---|---|
| índice de nomes/paths | `nameindex.go:92` `niGet`, `:63` `niBuild` | feito | — |
| índice persistido, layout SoA | `nameindex_idx.go:63`, `:412`, `:487` | feito | — |
| símbolos top-level (nome, sem linha) | `nameindex.go:224` `symbols()`, `:199` `niSymRE` | parcial | **não guarda linha**; linha exige `rg` |
| resolução fuzzy contextual | `nameindex.go:520` `resolveAll`, `:1035` `niResolveEntity` | feito | ranking é para nome, não para suspeito de issue |
| nota de nomes para o usuário | `nameindex.go:723` `niNote` | feito | é correção de typo, não localização |
| teste↔implementação | `nameindex.go:185` `niCore`, `:167` `niTestAffix` | feito | usado no resolve, não como sinal de localização |
| listagem via `.git/index` | `gitindex.go:82` `giParse`, `:290` `niListGit` | feito | — |
| parse de saída de teste/build/trace | `outparse.go:1625` `opParse`; `opTrace`/`opFrame` | feito | brain usa `Failures`, não `Traces` |
| contexto de falha `file:line` ±3 | `methods.go:1408` `brTestContext` | parcial | mostra 3 falhas; não ranqueia frames; não usa co-change |
| resolução de alvo de teste | `methods.go:1043` `brResolve`, `:1369` `brRunTests` | feito | — |
| verificação por método | `methods.go:1680` `brVerify` (switch de 11 checks) | parcial | registry por domínio; checks de localização |
| recuperação por categoria | `recover.go:53` `rvClassify`, `:108` `rvTable`, `:134` `rvDecide` | feito | — |
| laço do brain | `brain.go:261` `brExecute`, `:428` `brRecover` | feito | — |
| file_op do brain | `methods.go:512` `brFileOp` | parcial | sem `str_replace`; **sem guard de teste protegido** |
| edição tipada (modelo) | `edit.go:107` `applyEdits` | feito | não exposta ao brain |
| checkpoint/undo | `checkpoint.go:257` `Snapshot`, `:367` `Restore`, `:466` `cpAfterBash` | feito | — |
| guard honesto (modelo) | `guard_honest.go:246` `hgKind`, `:381` `judge`, `:428` `refusal` | feito | não cobre write do brain |
| mechrepair (parse/classify/recipe) | `mechrepair.go:29`, `:45`, `:66` | **órfão** | sem chamador de runtime; só teste |
| `fix.diagnose` | `methods/fix.json` | parcial | doc declara "reparo vai pelo modelo"; para após diagnóstico |
| `run_tests` | `methods/run_tests.json` | feito | — |
| plano cache/hindsight | `plancache.go:244` `pcMatch`, `:433` `pcStore`, `:561` `hdWrite` | feito | não é localização |
| prefetch lexical | `prefetch.go:34` `acPrefetchNote`, `:145` `acPrefetch` | feito, default off | só grep de identificadores; não top-3 com abstenção |
| eval offline | `evaloffline.go:119` `cmdEval`, `:171` `eoRun`; summary `:91` | feito | sem hit@k, sem `no_llm_success_rate` |
| co-mudança | — | **só pesquisado** | `offline-brain-research.md` C3; sem código |
| churn | — | **só pesquisado** | sem código |
| BM25 sobre código | — | **só pesquisado** | `plancache.go` tem BM25 de planos, não de código |
| grafo de chamadas/imports | — | **só pesquisado** | `offline-brain-research.md` propõe; sem código |
| SBFL | — | **só pesquisado** | sem cobertura/instrumentação |
| localize como capacidade | — | **só pesquisado** | G1 do `harness-sota-gap.md` |
| `fix.mechanical` | — | **só pesquisado** | G2; `mechrepair.go` é a semente |

### Achados incômodos (honestidade de engenharia)

1. **`mechrepair.go` não é usado por ninguém.** `grep` de `mrParseGoErrors|mrClassify|mrRecipe`
   só encontra `mechrepair_test.go`. É uma biblioteca testada e órfã; o `fix.json` continua dizendo
   que reparo "vai pelo modelo".
2. **O brain escreve sem o guard honesto.** `brFileOp` (`methods.go:512`) faz `os.WriteFile`/
   `os.Remove`/`os.Rename` atrás de `brPermit` e checkpoint, mas não chama `hgKind`/`hgState.judge`.
   Um `fix.mechanical` que escrevesse em teste protegido seria um bypass; isso precisa ser fechado
   antes da proposta P6.
3. **`outparse` já ranqueia o que o brain ignora.** `opTrace.Frames` tem `InProject`, `Func`,
   `File`, `Line`; `brTestContext` só olha `Data.Failures` e mostra as 3 primeiras. O sinal mais
   forte de localização está parseado e não usado.
4. **O índice de símbolos não tem linha.** `niSym` é `{Name, Key, Rel}`; para `path:line` é preciso
   `rg -n` no arquivo. Isso é aceitável (linha é volátil), mas o custo tem que entrar no orçamento.
5. **`no_llm_rate` conta bloqueio como resposta sem LLM** (`evaloffline.go:233`). Um localizador
   que abstém sobe a métrica sem resolver nada. Falta `no_llm_success_rate`.
6. **`fix.diagnose` para no diagnóstico.** O método roda testes e `test_context`, com verificador
   `tests_passed`; se os testes falham, o status é `failed` e o modelo assume. Não há caminho
   offline entre "sei o arquivo/linha" e "apliquei e verifiquei".
7. **O prefetch é grep de identificador, não localização.** `acPrefetchNote` injeta hits de `rg`;
   não ranqueia suspeitos, não usa frame/co-change, não abstém. A proposta P8 é o delta.

---

## 6. Propostas para o Dell

Resumo ordenado por impacto/esforço. Todas são Go puro, sem cgo, sem rede; custos são estimativas
a medir no Dell, não números medidos. "Comandos/tarefa" conta comandos do usuário; comandos internos
do localizador entram em `internal_cmds` (novo) para não mascarar a métrica.

| # | proposta | impacto | esforço | risco | LOC aprox. | RAM extra | p50 alvo (quente) |
|---|---|---|---|---|---|---|---|
| P1 | eval de localização + métricas | 5 | S–M | baixo | +120 | 0 | 0 (só eval) |
| P2 | `localize` (arquivo→elemento→linha) | 5 | M | médio | +350 | <1 MB | <80 ms |
| P3 | ranking de frames de traceback | 4 | S | baixo | +80 | 0 | <5 ms |
| P4 | co-mudança | 4 | M | médio | +200 | <2 MB | <5 ms quente; 0,3–1,5 s frio |
| P5 | churn como desempate | 3 | S–M | médio | +120 | <0,5 MB | compartilha git log |
| P6 | `fix.mechanical` + guard + verify | 5 | M | alto→controlado | +300 | 0 | +1 build/test |
| P7 | pré-voo de comando | 4 | S | baixo–médio | +180 | 0 | <1 ms |
| P8 | `localize` no 1º turno do modelo | 4 | S | médio | +60 | 0 | +80 ms |
| P9 | pickaxe/blame para regressões | 3 | S | baixo | +100 | 0 | 0,1–1 s frio |

### P1. Eval de localização e métricas honestas — impacto 5, esforço S–M, risco baixo

**Mecanismo.** Nova categoria `localização` no `evals/offline/tasks.json` (24–30 fixtures: 8 Go,
8 Python, 8 JS/TS), cada uma com issue textual, arquivo/elemento/linha GT, distratores e,
opcionalmente, um teste que falha. Três subcategorias: `issue-only`, `falha` (com traceback) e
`regressão` (com histórico git semeado). O runner passa o texto ao `localize` e compara com o GT.

**Métricas novas no `eoSummary` (`evaloffline.go:91`):** `file_hit@1/3/5`, `element_hit@3`,
`line_hit@1±5`, `mrr`, `abstain_rate`, `no_llm_success_rate` (sucesso verificado **e** zero chamadas
de modelo / N), `internal_cmds_per_task`. `no_llm_rate` continua, mas não é mais a métrica principal.

**Arquivos.** `evaloffline.go` (+~80), `evals/offline/tasks.json` (+ fixtures), `localize_test.go`
(novo), `evaloffline_test.go` (golden do summary).

**Custo Dell.** Só roda no eval (máquina de dev ou Dell quando o dono pedir); 0 no caminho de
produção. Fixtures são JSON embutido (~30 KB).

**Métrica de aceitação.** O runner tem que reproduzir o baseline BM25 de nomes; se o `localize`
não bater o baseline em `file_hit@5`, a proposta P2 não entra. `false_success` e `false_blocks`
continuam gate de CI (`evaloffline.go:164`).

**Falsificação.** Se `file_hit@5` do `localize` < `file_hit@5` do `niResolveEntity` atual, remover
o ranking fundido e manter só o resolvedor. Sem essa comparação, não há ciência.

### P2. `localize` — pipeline determinístico arquivo→elemento→linha — impacto 5, esforço M, risco médio

**Mecanismo.**

1. `locTerms(goal)`: reusa `niTokens` (`nameindex.go:399`), acrescenta spans entre backticks,
   caminhos, nomes `CamelCase`/`snake_case`, strings de erro e termos de tipo de issue; stopwords;
   cap 8 termos.
2. `locSeeds`: exatos via `niResolveEntity` (`nameindex.go:1035`); frames via `opTrace.Frames`
   (`outparse.go:90`); BM25 de nomes sobre `niIndex.files`/`symbols()`; top-20.
3. `locContent` (opcional, se o orçamento permitir): `rg -c -i -e t1 ...` nos 20 candidatos, não no
   repo inteiro; `rg -c` conta linhas com match, então o tf do BM25 é aproximado (1 por linha); se
   precisar de tf real, `rg -o` + contagem, só nos top-5. Normaliza por tamanho; descarta termo com
   >40 hits (é genérico).
4. `locExpand1`: 1-hop por import/chamada/definição; cap 12 nós; descarta se estourar.
5. `locHistory`: co-mudança (P4) e churn (P5) se `Budget.History` e o repo tem git.
6. `locFuse`: rank fusion inteira (§3.3); top-3; abstenção por `T_abs`/`T_margin`.
7. `locElement`: spans de símbolos nos top-3 via `niSymRE` com linha (nova função
   `niSymLines(rel)`, regex + `bufio.Scanner`, sem AST); rank por sobreposição de termos e por
   contenção do frame.
8. `locLine`: `rg -n` dos termos dentro dos top-3 elementos; frame exato; ±5 linhas; devolve até 3
   regiões.
9. Saída ≤400 tokens: `path:line symbol [sinais] (confiança)`, com "verificar antes de editar".

**Arquivos.** `localize.go` (~350 LOC), `localize_test.go`, `methods.go` (cap `localize` no
`brCaps`, `:303`), `methods/fix.json` (step 0), `methods/run_tests.json` (contexto antes da falha),
`caps.go` (`Capability` read-only), `nameindex.go` (nova `niSymLines`), `gitindex.go` (reuso).

**Custo Dell.** Índice quente <1 ms; BM25 de nomes 2–8 ms; `rg` em 20 arquivos 30–200 ms quente,
0,3–1,5 s frio; git history 0,3–1,5 s frio (cache). Memória transitória <1 MB; sem alocação por
arquivo no índice (layout existente). Binário +~120 KB. Tudo `int32`/`int16`; offsets `uint32` como
no índice atual.

**Métrica.** `file_hit@1/3/5`, `element_hit@3`, `line_hit@1±5`, `abstain_rate`, p50/p95, RSS,
`internal_cmds`; downstream: `verified_success` do `fix.diagnose` (deve subir), `commands/task`
(usuário não deve pagar comando extra).

**Risco.** Ancoragem: top-3 errado ancora o brain/modelo. Mitigação: abstenção, rótulo "verificar",
nunca editar sem verificação, top-3 em vez de top-1, e A/B contra o resolvedor atual.

**Dependências.** P1 (medir), P3 (frames), P4/P5 (histórico). P2 sozinho já entrega valor com
léxico + frames.

### P3. Ranking de frames de traceback — impacto 4, esforço S, risco baixo

**Mecanismo.** `outparse` já produz `opTrace.Frames` com `InProject`, `Func`, `File`, `Line`
(`outparse.go:90`). Hoje `brTestContext` (`methods.go:1408`) ignora `Traces` e mostra as 3 primeiras
`Failures`. Trocar por: filtrar `InProject`, descartar framework de teste, dedupe por arquivo, e
ordenar conforme a linguagem — em Python/JS, o frame **mais fundo** (onde a exceção ocorreu); em
Go, o **primeiro** frame in-project do topo (panic site), não o chamador mais profundo. Mostrar
top-3 frames com ±3 linhas; usar o frame escolhido como semente do `localize`. Para Go, o frame do
teste é `_test.go`; o frame útil é o primeiro `InProject` que não é `_test.go`.

**Arquivos.** `methods.go` (`brTestContext`), `localize.go` (seed), `outparse.go` (se faltar
`InProject` em algum parser), `methods_test.go`.

**Custo Dell.** <5 ms; 0 comandos extras; 0 memória. É o maior ganho por LOC do conjunto.

**Métrica.** `frame_hit@1` (arquivo do frame in-project = arquivo GT) em fixtures com traceback;
`steps` até o primeiro read relevante; `false_success` (não pode aumentar).

**Risco.** Traceback de teste pode apontar para o teste, não para a implementação; usar
`hgKind`/`niCore` para mapear teste→impl. Se não houver frame in-project, não emitir.

### P4. Co-mudança determinística — impacto 4, esforço M, risco médio

**Mecanismo.** Uma passada `git log --no-merges --numstat --format=... -n 400` (timeout 2 s), parse
por commit, exclusão de commits >50 arquivos, normalização de rename via `--name-status -z`, contagem
de pares `(seed,cand)` com suporte/confiança/lift e decay de 180 dias. Cache
`dataDir()/cochange/<hash(root+HEAD)>.json`; invalida por HEAD. Saída: top-3 candidatos por seed,
com `support/confidence/lift`. Sem git ou sem histórico: sinal indisponível, não bloqueia.

**Arquivos.** `cochange.go` (~200), `cochange_test.go` (fixture git com commits sintéticos),
`localize.go` (consumo), `gitindex.go` (helper de execução read-only), `caps.go` (allowlist do
comando).

**Custo Dell.** 400 commits × ~5 arquivos = ~2.000 paths; parse <1 MB; cache ~100–300 KB. Quente
<5 ms; frio 0,3–1,5 s (HDD), com timeout. 32-bit: usar slices ordenadas, não mapas gigantes; cap
50k pares.

**Métrica.** `cochange_recall@3` (o arquivo GT está no top-3 dado o seed) nos fixtures de
regressão; delta em `file_hit@1` quando combinado; p50/p95; taxa de indisponibilidade (sem git).

**Risco.** Histórico ruidoso, merges, commits de release, rename. Mitigação: `--no-merges`, cap de
arquivos, suporte ≥2, confiança ≥0,3, lift ≥1,5, decay; nunca usar co-mudança sozinha.

**Dependência.** P1 (fixtures com histórico), P2 (consumo). A proposta C3 de
`offline-brain-research.md` já fixou a base; isto é o refinamento e a avaliação.

### P5. Churn relativo como desempate — impacto 3, esforço S–M, risco médio

**Mecanismo.** Da mesma passada do P4 (`--numstat`), por arquivo: `added`, `deleted`, `commits`,
`loc` atual (via `os.Stat` ou contagem de linhas no índice de símbolos), `churn_rel =
(added+deleted)/(loc+1)`, `commits_rel = commits/semanas`. Normalizar por rank; peso 4 no
`locFuse`, nunca sozinho. Excluir `vendor/`, `third_party/`, `generated/`, `dist/`, `build/`,
`*.pb.go`, `*.min.js`.

**Arquivos.** `churn.go` (~120), `churn_test.go`, `cochange.go` (parser compartilhado),
`localize.go`.

**Custo Dell.** Sem custo extra de git se compartilhado com P4; 20k arquivos × 16 B ≈ 320 KB;
cálculo <5 ms.

**Métrica.** Delta em `file_hit@1` com/sem churn; taxa de promoção indevida (arquivo só por churn);
p50. Se o delta for ≤1 pp, remover o sinal.

**Risco.** Hotspot antigo domina; arquivo gerado; repo pequeno sem histórico. Mitigação: peso baixo,
exclusões, decay, calibração.

### P6. `fix.mechanical` — reparo sem LLM, com guard e verificação — impacto 5, esforço M, risco alto→controlado

**Mecanismo.**

1. `fix.diagnose` roda testes/build e produz `brTestRes`/`opResult` (já existe).
2. Nova cap `mech_repair` consome `opResult.Data.Diags` (`outparse.go:450`) e/ou `Failures`;
   `mrClassify` estendido por `(format, code, msg)`; `mrRecipe` devolve operador + argumentos.
3. Aplica **um** candidato via `brStrReplace` novo (old/new + diff), usando `applyEdits`
   (`edit.go:107`) com verificação de que o old existe uma vez; antes, `hgBrainWrite` valida
   `hgKind`/`hgState.judge` (teste protegido/asserção).
4. Roda o mesmo verificador (build/teste) no arquivo alterado; se passar, `succeeded` com evidência;
   se falhar, `Restore` do checkpoint (`checkpoint.go:367`) e `failed` (não `unverified`).
5. Cap 1 tentativa mecânica por tarefa; sem candidato único/seguro, escala para `fix.diagnose`/
   modelo. Sem verificador, não aplica.

**Receitas iniciais (baixo risco):** Go unused import, Go unused var (`_ = x`), Go `go.sum` (só
com cache completo), Python unused import, JS ESLint unused, Rust unused import. Receitas de
risco alto (`undefined`/`NameError`/`ReferenceError`) ficam atrás de flag e de um candidato único
a distância ≤2; se houver dúvida, não aplica.

**Arquivos.** `repair.go` (~250), `mechrepair.go` (+~120, consumir `opDiag`, novas receitas),
`methods.go` (cap + `brStrReplace` ~80), `methods/fix.json` (`fix.mechanical` priority 15,
`fix.diagnose` 10), `guard_honest.go` (`hgBrainWrite`), `recover.go` (cadeia
`rvFixFailingTest` → mecânico → diagnose → blocked), `checkpoint.go` (reuso).

**Custo Dell.** 0 tokens; 1 build/teste por tentativa (o mesmo que o diagnóstico já paga); regex +
índice <10 ms; 1 checkpoint (KB). Binário +~100 KB.

**Métrica.** `no_llm_success_rate` na categoria "bugfix mecânico" (5–8 tarefas novas no eval);
`verified_success`; `false_success` **deve ficar 0**; `false_blocks` (recusou quando havia receita
segura) **deve ficar 0**; `reverts`; comandos/tarefa; p50.

**Risco.** Patch errado que passa no teste (plausível, não correto); edição em teste protegido;
`go mod tidy` sem rede. Mitigação: verify + revert + cap 1 + guard + só receitas de baixo risco por
padrão; auditoria manual de 30 patches.

**Dependência.** P1 (medir), P3 (frames para o diagnóstico), guard `hgBrainWrite`. Sem o guard,
não ligar.

### P7. Pré-voo de comando — impacto 4, esforço S, risco baixo–médio

**Mecanismo.** Antes de `sh.Run` (`tools.go:1138`), analisar o primeiro comando de cada segmento
(`;`, `&&`, `|`, subshell) com um divisor **conservador**: só divide em separadores não citados e
não escapados; se houver `$(`, backtick, heredoc ou quoting que não fecha, não pré-valida aquele
segmento (melhor não bloquear do que bloquear errado). Para cada comando: ignorar builtins (`cd`, `export`, `source`, `alias`, `set`, `test`,
`[`, `:`, `true`, `false`, `echo`, `printf`, `read`, `ulimit`, `wait`, `trap`, `type`, `command`,
`builtin`, `eval`, `exec`, `return`, `shift`, `getopts`, `local`, `declare`, `typeset`, `unset`,
`hash`, `pwd`, `jobs`, `fg`, `bg`, `kill`, `suspend`, `logout`, `times`, `history`, `help`);
ignorar comandos com `$`, backtick ou glob dinâmico; se `exec.LookPath` falhar, bloquear com
mensagem exata: binário, alternativa fuzzy do `epProfile` (`niEnvScore`), e comando de install do
gerenciador detectado. Não bloquear writes, não bloquear `rm`.

**Arquivos.** `preflight.go` (~180), `tools.go` (hook antes de `sh.Run`), `methods.go` (`runCmd`),
`caps.go` (`cbReadCmd`/allowlist), `preflight_test.go`.

**Custo Dell.** `LookPath` ~50 µs por comando; 0 processo extra; 0 memória.

**Métrica.** `command_failures/task` (deve cair; TB2 24,1% é o teto), `commands/task`, p50,
`false_blocks` (comando válido bloqueado) **deve ficar 0** em fixtures; taxa de remediação aceita.

**Risco.** Bloquear comando que existe como função/alias do shell, ou com path dinâmico. Mitigação:
lista de builtins, pular `$`/backtick, `command -v` como fallback opcional, e nunca bloquear `cd`.

### P8. `localize` no primeiro turno do modelo — impacto 4, esforço S, risco médio

**Mecanismo.** Reusar P2 para injetar uma nota ≤400 tokens no primeiro turno do modelo, ao lado do
prefetch (`agent.go:452`, `prefetch.go:34`), com top-3 + confiança + "verificar". Default off;
flag `--localize-prefetch`; A/B. Diferença em relação ao prefetch #2 de `grep-is-all-you-need.md`:
não é grep de identificador, é top-3 com abstenção, frame/co-change e confiança; e a métrica é
`file_hit` do que o modelo lê, não só steps.

**Arquivos.** `prefetch.go` (+~60) ou `localize.go` (render), `agent.go` (chamada), `config.go`
(flag), `prefetch_test.go`.

**Custo Dell.** +80 ms no primeiro turno (índice quente), 0 no resto; ≤400 tokens de contexto.

**Métrica.** `steps` até o primeiro read relevante, `tokens/tarefa`, `file_hit@1` do primeiro read,
pass rate em terminal-tasks; p50.

**Risco.** Ancoragem; nota errada desperdiça contexto. Mitigação: só injetar se confiança alta;
rótulo "pode estar incompleto"; A/B; nunca no system prompt (cache append-only).

### P9. Pickaxe/blame para regressões — impacto 3, esforço S, risco baixo

**Mecanismo.** Se a issue tem marcadores de regressão ("parou de funcionar", "depois de", "desde a
versão", "regression", "used to work"), rodar `git log -S'<símbolo>' --oneline -n 20 -- <path>`
ou `-G<regex>`; se a issue cita linha, `git blame --line-porcelain -L a,b -- path`; ranquear
arquivos tocados pelo commit que introduziu a mudança. Cap 2 arquivos, timeout 2 s; sem git,
pular.

**Arquivos.** `localize.go` (+~100), `gitindex.go` (exec read-only), `caps.go` (allowlist),
`localize_test.go`.

**Custo Dell.** 1–2 comandos git; 0,1–1 s frio; 0 memória; cache por HEAD opcional.

**Métrica.** `file_hit@1` na subcategoria `regressão`; comandos internos; p95.

**Risco.** Pickaxe é literal (rename quebra); blame é lento em arquivo grande; histórico raso.
Mitigação: `-G` fallback, cap, timeout, só quando o léxico de regressão aparece.

---

## 7. O que NÃO adotar

1. **Embeddings/vector store em runtime.** LocAgent mostra que CodeRankEmbed bate BM25 em arquivo
   (84,67 vs 61,68 Acc@5) [S5, Tabela 4] — então não dá para dizer que lexical sempre vence. Mas o
   custo é um modelo + índice denso + rebuild a cada mudança, inviável em 2 GB/1 CPU e contrário à
   regra de ouro. Se um dia entrar, entra como asset forjado no build, não como dependência de
   runtime.
2. **Expansão 2-hop do grafo.** RepoGraph: 26,00% vs 29,67% do 1-hop, com 10.505 vs 2.311 tokens
   [S8, Tabela 4]. Mais contexto degrada.
3. **Resumo de subgrafo por LLM em runtime.** 1-hop resumido 28,33% vs 1-hop achatado 29,67%
   [S8, Tabela 4]; além de depender de modelo, perde informação.
4. **MCTS/SWE-Search como laço padrão.** +23% relativo, mas com value agent, discriminator e muitas
   chamadas [S10]; caro e dependente de LLM. O seletor barato do jazz é execução de teste.
5. **Geração de teste de reprodução por LLM em runtime.** É o maior ganho do Agentless (+15 fixes),
   mas custa +US$ 0,25/tarefa e é dependência [S4, Tabela 4]. No máximo, fixtures forjadas no build.
6. **SBFL como padrão.** +3 pp no AutoCodeRover, mas exige a suíte completa do desenvolvedor e
   instrumentação de cobertura [S7, §6.2]. No Dell, ferramentas de cobertura para múltiplas
   linguagens são peso morto; deixar como capacidade opcional quando `go test -cover`/`coverage.py`
   já estiverem no projeto.
7. **Churn como ranking principal.** Nagappan mede densidade de defeitos por binário, não localização
   [S13]; churn absoluto é mau preditor. Só desempate.
8. **Arquivo inteiro como contexto.** Agentless: arquivo completo 53,67% vs skeleton 58,33%, com
   7,5× o custo [S4, Tabela 2]; SWE-agent: arquivo inteiro 12,7% vs 100 linhas 18,0% [S6, Tabela 3].
9. **tree-sitter/AST/cgo.** Quebra o binário 386 estático; regex de símbolos + spans cobre o que
   precisamos (Go/Py/JS/Rust), com menos precisão assumida.
10. **Best-of-n cego.** Sem seletor, não fecha gap (já registrado em `harness-sota-gap.md` §4/S4).
    O seletor tem que ser execução, não voto de LLM.
11. **LLM como localizador de runtime.** Mesmo que acerte mais, zera `no_llm_rate` e viola a regra
    de ouro. O LLM pode forjar pesos/fixtures no build; não pode decidir o arquivo em runtime.
12. **Aplicar patch sem verificador.** O risco de `false_success` é inaceitável; `tests_passed`
    como prova de correção é categoria errada (§3.8).
13. **Co-mudança em merges/commits gigantes.** Gera pares espúrios; excluir e capar.
14. **Prometer linha exata em issue-only.** 34,3% de linha com LLM [S8, Tabela 3]; offline será
    menor. Entregar região e medir `line_hit@1±5`.
15. **Usar `no_llm_rate` sozinho como sucesso.** Bloqueio conta como sem LLM
    (`evaloffline.go:233`); reportar `no_llm_success_rate` junto.

---

## 8. Calibração, falsificação e protocolo de medição

### 8.1 Pré-registro

Antes de codar P2/P4/P5/P6, escrever no doc de eval:

- Hipótese H1: `localize` (léxico + frames + co-mudança) bate o `niResolveEntity` atual em
  `file_hit@5` por ≥10 pp nos fixtures `falha`.
- H2: co-mudança adiciona ≥3 pp em `file_hit@1` nos fixtures `regressão`, e ≤1 pp em `issue-only`.
- H3: churn adiciona ≥1 pp em `file_hit@1`; se não, sai.
- H4: `fix.mechanical` resolve ≥30% da categoria "bugfix mecânico" sem LLM, com `false_success=0`.
- H5: pré-voo reduz `command_failures/task` em ≥15% sem `false_blocks`.

Se uma hipótese falhar, a proposta correspondente é removida — não "ajustada até passar" no mesmo
conjunto.

### 8.2 Conjunto de avaliação

- 24–30 fixtures, 3 linguagens, 3 subcategorias; GT por arquivo/elemento/linha; distratores
  (arquivos com nomes parecidos, docs, testes).
- Split `dev`/`blind`: pesos e limiares são ajustados no `dev`; o `blind` só é olhado uma vez.
  O `blind` não entra no tuning. Sem isso, é overfit.
- Fixtures de histórico: repo git sintético com 30–50 commits, incluindo merges, rename e um commit
  gigante, para testar a robustez do P4.
- Fixtures de falha: tracebacks Go/Python/JS reais, com frames de framework e in-project.

### 8.3 Estatística

- Taxas com n=30 têm intervalo de Wilson de ~±17 pp; não comparar diferenças de 2–3 pp sem
  McNemar/intervalo. Reportar n e IC.
- Para A/B pareado (mesma tarefa, dois braços), usar McNemar; para contagem de comandos, mediana e
  bootstrap.
- p50/p95 com ≥20 repetições no Dell; RSS de pico (`eoPeakRSSKB`) antes/depois; tamanho do binário.
- Toda estimativa deste doc é hipótese até medida no Dell; não transformar estimativa em número.

### 8.4 Ablação obrigatória

Para o `localize`, rodar cinco braços: (a) só `niResolveEntity`; (b) + BM25 nomes; (c) + frames;
(d) + co-mudança; (e) + churn/1-hop. Se um braço não melhora `file_hit@5` ou `line_hit@1±5`, sai.
Para o `fix.mechanical`, rodar por classe de erro: se uma receita tem `false_success>0` ou
`reverts>50%`, sai.

### 8.5 Auditoria de correção

- Amostra de 30 patches aplicados: dois revisores (ou um revisor + checklist) classificam
  `correto`/`plausível`/`errado`; reportar `correct_rate` separado de `verified_success`.
- Critério de correção: o patch muda a causa, não só o sintoma; não enfraquece teste; não altera
  comportamento não relacionado. Em fixtures, comparar com o patch GT por intenção, não por texto.

### 8.6 Gates de honestidade e segurança

- `false_success=0` e `false_blocks=0` no CI (`evaloffline.go:164`).
- Nenhum write em teste protegido (`hgKind`) sem confirmação explícita; `hgBrainWrite` antes de P6.
- Nenhum patch aplicado sem verificador; nenhum `succeeded` sem evidência (`task.go:160`).
- Nenhum comando de rede no caminho offline; `go mod tidy` só com cache completo.
- Nenhum segredo em log; comandos exibidos com `sanitizeTerm` onde já se aplica.

---

## 9. Fontes

- **[S1]** Jimenez, Yang, Wettig, Yao, Pei, Press, Narasimhan. *SWE-bench: Can Language Models
  Resolve Real-World GitHub Issues?* arXiv:2310.06770, ICLR 2024. 2.294 tarefas, 12 repos Python;
  `FAIL_TO_PASS`/`PASS_TO_PASS`. (Abstract e §2.)
- **[S2]** Chowdhury et al. (OpenAI). *Introducing SWE-bench Verified.* Blog, 13 ago. 2024
  (atualizado 24 fev. 2025). 500 amostras; 93 devs; 1.699 anotadas; 196 fáceis, 45 difíceis;
  GPT-4o 33,2%; Agentless dobrou o 16% anterior no SWE-bench (blog). Números de scaffolds
  diferentes não são comparáveis ao 38,8% do paper do Agentless (Tabela 6). (Seções “SWE-bench
  Verified” e “Annotation Results”.)
- **[S3]** Merrill, Shaw, Carlini et al. *Terminal-Bench: Benchmarking Agents on Hard, Realistic
  Tasks in Command Line Interfaces.* arXiv:2601.11868, jan. 2026. 89 tarefas; <65%; Codex CLI 63%,
  Terminus 2 58%, Gemini 3 Pro 57%; 24,1% falhas de comando por binário ausente; 9,6% ao executar;
  taxa 9,2–26,7%; sem correlação turnos/tokens. (§4, §4.5, App. C/F.)
- **[S4]** Xia, Deng, Dunn, Zhang. *Agentless: Demystifying LLM-based Software Engineering Agents.*
  arXiv:2407.01489, 2024 (v2 out. 2024). 32,00% Lite (96/300), US$ 0,70; 38,80% Verified (194/500);
  localização 81,67/58,33/50,67; ablações Tabelas 2–4. (§3–5.)
- **[S5]** Chen, Tang, Deng et al. *LocAgent: Graph-Guided LLM Agents for Code Localization.*
  arXiv:2503.09089, 2025. BM25 38,69/51,82/61,68; LocAgent 94,16/77,37; ablações Tabela 6;
  274/300 tarefas; Acc@k estrito. (§5, Tabelas 4 e 6.)
- **[S6]** Yang, Jimenez, Wettig, Lieret, Yao, Narasimhan, Press. *SWE-agent: Agent-Computer
  Interfaces Enable Automated Software Engineering.* arXiv:2405.15793, 2024 (v3 nov. 2024).
  12,47% full; 18,00% Lite; ablações Tabela 3; falhas B.4; pass@6 32,67. (§5, App. B.)
- **[S7]** Zhang, Ruan, Fan, Roychoudhury. *AutoCodeRover: Autonomous Program Improvement.*
  arXiv:2404.05427, ISSTA 2024. 19% Lite; 22% com SBFL; 26% @3; 37k tokens/US$ 0,43; taxonomia
  §6.3; correção 65,4%. (§4–6.)
- **[S8]** Ouyang, Yu, Ma et al. *RepoGraph: Enhancing AI Software Engineering with Repository-level
  Code Graph.* arXiv:2410.14684, ICLR 2025. 1.419,3 nós/26.392,1 arestas; +2,34 pp Agentless;
  1-hop 29,67 vs 2-hop 26,00; Tabelas 2–4. (§3–5.)
- **[S9]** Wang, Li, Song et al. *OpenHands: An Open Platform for AI Software Developers as
  Generalist Agents.* arXiv:2407.16741, ICLR 2025. Event stream, sandbox, CodeAct, 15 benchmarks;
  26,0% Lite com Claude 3.5, US$ 1,10. (§2, §4.2.)
- **[S10]** Antoniades, Örwall, Zhang, Xie, Goyal, Wang. *SWE-Search: Enhancing Software Agents with
  Monte Carlo Tree Search and Iterative Refinement.* arXiv:2410.20285, ICLR 2025. MCTS + Value
  Agent + Discriminator; +23% relativo em cinco modelos. (Abstract; repo `moatless-tree-search`.)
- **[S11]** Zimmermann, Weißgerber, Diehl, Zeller. *Mining Version Histories to Guide Software
  Changes.* ICSE 2004, DOI 10.1109/ICSE.2004.1317478. Recall 15%, precisão 26%, top-3 64%, 8
  projetos, consulta ≤0,5 s. (§6.4; ver também [S12].)
- **[S12]** Zimmermann et al. *Mining Version Histories* (WSR 2004, resumo estendido), PDF
  `zimmermann-wsr-2004.pdf`: consulta ≤0,5 s; top-3 64%; recall ~15%.
- **[S13]** Nagappan, Ball. *Use of Relative Code Churn Measures to Predict System Defect Density.*
  ICSE 2005, DOI 10.1145/1062455.1062514. 89,0% de acurácia fault/not-fault-prone; Windows Server
  2003; churn absoluto mau preditor. (Abstract.)
- **[S14]** Moatless Tools, `github.com/aorwall/moatless-tools` (README, 2025–2026): 70,8% com
  Claude 4 Sonnet, US$ 0,63/instância; 39,0% Lite com Claude 3.5 (jan/2025); 30,67% DeepSeek V3
  Lite. `experiments.moatless.ai`. (Sem paper revisado para a versão atual.)
- **[S15]** `docs/research/harness-sota-gap.md` (P5/P7, G1/G2/G11, propostas 1/2/9),
  `grep-is-all-you-need.md` (lexical vs vetorial; prefetch #2), `offline-brain-research.md` C3
  (co-mudança), `low-level-performance.md` §5 (layout do índice), `math-offline-brain.md`
  §2.1/§2.6 (calibração/orçamentos), `tocomas-plan-reuse.md` (reuso de plano).
- **[S16]** Código do jazz citado: `nameindex.go`, `nameindex_idx.go`, `gitindex.go`, `outparse.go`,
  `methods.go`, `brain.go`, `recover.go`, `edit.go`, `checkpoint.go`, `guard_honest.go`,
  `mechrepair.go`, `evaloffline.go`, `caps.go`, `tools.go`, `prefetch.go`, `plancache.go`,
  `task.go`, `methods/fix.json`, `methods/run_tests.json`, `evals/offline/tasks.json`.

### Nota final de honestidade

Os números deste doc medem **outros sistemas, em Python, com LLM**. Nenhum deles foi reproduzido no
Dell. As estimativas de custo são hipóteses de engenharia, marcadas como tal. O que se afirma com
confiança é narrower: (a) BM25 lexical é um piso útil e barato; (b) 1-hop é melhor que 2-hop;
(c) co-mudança é booster, não substituto; (d) churn é desempate; (e) reparo mecânico é uma fração
pequena, mas verificável, do espaço de reparo; (f) a maior parte do ganho offline vem de parar cedo,
pré-voo e verificação, não de "entender" a issue. Se o eval do jazz desmentir qualquer uma dessas,
o doc deve ser corrigido — não o eval.
