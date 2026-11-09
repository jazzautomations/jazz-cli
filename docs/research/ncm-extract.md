# NCM (Next-Concept Modeling) — extrato para o jazz-cli

**Verificado:** arXiv:2610.14162, *"Next-Concept Modeling: Fast, Parallel Coarse-to-Fine Text Generation"*, Kumbong, Khare, Mlauzi, Woen, Ding, Buchanan, Arora (Together AI), Mirhoseini, Ré — Stanford Hazy Research + Together AI. Submetido 2026-10-09, cs.CL. Código em github.com/HazyResearch/ncm (MIT, ~1.5k stars), checkpoints em huggingface.co/hazyresearch/ncm-checkpoints. **Acrônimo checado:** o "NCM" de 2025/2026 é este. As outras expansões (Neural Conversational Model, Vinyals & Le 2015) são antigas e irrelevantes.

## 1. O que NCM é de fato

**Arquitetura (dois estágios, ambos treinados por dataset):**

1. **Tokenizer = VQ-VAE residual multiescala.** Codifica texto em K níveis de códigos discretos, do grosso ao fino: nível k tem ℓ_k códigos com ℓ₁ < ℓ₂ < … < ℓ_K = L (tamanho do alvo). Cada nível quantiza o *resíduo* deixado pelos anteriores. **Nested dropout** no treino (corta nos primeiros k níveis) força os níveis grossos a carregar informação útil sozinhos. Tokenizers de 15–144M params; curiosamente o encoder tem **0 blocos transformer** — é embed+projeção; o decoder tem 1–4 blocos.
2. **Gerador = transformer que produz a hierarquia nível a nível.** No nível k, prevê *todos* os ℓ_k códigos em paralelo (uma forward pass), condicionado na **reconstrução cumulativa** ẑ^(k−1) dos níveis anteriores — não nos códigos crus. Atenção *block-diagonal* (cada bloco de entrada só atende a si mesmo, pois já resume os níveis anteriores) — 1.4× mais rápido e com PPL melhor que block-causal na ablação. Decode final = argmax do decoder do VQ.

**Geração = K passes sequenciais (K=8–32) em vez de L passes AR, e os passes grossos processam sequências curtíssimas** — é daí que vem a velocidade.

**Tamanho e treino:** geradores de ~25M (Sudoku) a ~120M params; estudo de escala preliminar até 725M. AdamW, bf16, batch 256–4096, 50–350k updates. **~4–5× menos tokens de treino** que os baselines de difusão (13.4B vs 66B no LM1B, contando tokenizer+gerador). Hipótese dos autores: a *ordem fixa* grosso→fino é mais fácil de aprender que as muitas ordens de geração que a difusão precisa cobrir.

**Resultados (a preços justos: modelos ~100M, benchmarks de difusão, não chat):**

- LM1B: Gen.PPL *melhor* que o melhor DLM (DUO) com **11× menos NFE, 21× mais rápido** (H100). OWT: par, 10× mais rápido.
- XSum: ROUGE-L 28.0 sem guidance vs 26.6 do melhor baseline (AURORA-LM) com 16× menos NFE; com **MBR** (K=16 candidatos) 29.6, batendo o baseline *guiado* (28.6). WMT14: BLEU 22.6/25.2-MBR vs 25.2 do AR.
- **Sudoku: 99.5/96.5/86.1% (fácil/médio/difícil) com 12 NFE** — melhor que AR (~1% no difícil) e que o melhor DLM (FMLM+, 81.3% com 16 NFE). Autores: grosso→fino serve tarefas de restrição global, porque níveis posteriores *corrigem* a solução parcial, ao contrário do commit esquerda→direita do AR.
- Tokenizer: 98.8–100% de reconstrução exata. **Falhas típicas: entidades nomeadas, numerais, diacríticos, usernames, markup** — substituições por itens "parecidos" do codebook.

**Licença/disponibilidade:** MIT. Repo **só tem código de avaliação** (`evaluate.py`, `scripts/`, `models/`, `tokenization/` — **sem training code**). Checkpoints são task-specific: xsum, wmt14, sudoku×3, lm1b×3, tinystories, owt (~8 GB total). **Não existe modelo de chat/instrução, não existe API hospedada, nada em PT-BR.** Requer PyTorch+CUDA; port comunitário MLX (janmus1c/ncm-mlx) roda os checkpoints em Apple Silicon, só inferência.

**Ceticismos válidos:** speedups são *vs modelos de difusão*, não vs AR servido em produção (teste independente: ~4× vs AR em batch 1, AR ganha em batch 64). Sem streaming — nada legível até o último nível. Saída de tamanho fixo (mesmo uma resposta de uma linha atravessa K níveis — limitação admitida, "input-dependent schedule" é future work). Nada no paper é sobre tool-use, recusa ou verificação externa — a ponte com o jazz é analógica.

## 2. Técnicas que transferem para o jazz

NCM **não é adotável como componente** (CUDA, centenas de MB por tarefa, sem PT-BR, eval-only; um Pentium M não roda nem o tokenizer). O valor é que o paper é, em essência, **evidência empírica da tese de design do jazz**: gerar restringindo cada passo a um codebook discreto pequeno, em ordem grosso→fino, com robustez a erro acumulado, bate geração livre. O "cardápio de intents" *é* um codebook; slots preenchidos por enumeração do filesystem *são* codebooks.

| # | Ideia do NCM | Aplicação concreta no jazz |
|---|---|---|
| 1 | **Decode grosso→fino em dois níveis** (intent primeiro, slots depois). O coarse code fixa a estrutura; níveis finos só refinam. | Formalizar `m.Parse` como 2 estágios: nível 1 = classificar intent dentro do cardápio (codebook ~dezenas); nível 2 = preencher slots conforme schema *daquele intent*. Cada estágio tem conjunto válido pequeno → menos jeitos de errar e verify separado por nível. |
| 2 | **MBR → seleção por verify.** NCM amostra K candidatos e escolhe o mais parecido com os demais (consenso). Consensus ≠ correção; no jazz dá pra fazer melhor. | Quando o parse vier ambíguo/baixa confiança: enumerar top-K hipóteses (intent+slots), rodar verify/barato dry-check em cada uma (path existe? flag válida? operandos cobertos?), executar a primeira com evidência; nenhuma → `dropped`/`needs_input`. Converte diversidade desperdiçada em confiabilidade — sem modelo melhor. |
| 3 | **Tokenizer erra exatamente em entidades/numerais** — compressão aprendida troca itens por "parecidos". | Regra já parcialmente seguida, agora com justificativa: **paths, números, flags e identificadores nunca são gerados livremente** — extraídos verbatim do input do usuário ou escolhidos de um conjunto enumerado (`fd`/`ls`/schema), i.e. um codebook real. Auditar slots de path hoje preenchidos por geração livre. |
| 4 | **Conditioning corruption** (treinar com parte dos códigos de entrada trocados por aleatórios do mesmo codebook; alvo limpo). Moderada ajuda muito; excessiva destrói o sinal. É o remédio deles para *exposure bias* — erro acumulando entre níveis. | Versão eval: **corruption-injection tests**. Nas baterias offline, injetar upstream corrompido (slot errado, path inexistente, intent trocado) e medir se verify+guardas pegam antes do `succeeded`. Mede cobertura das guardas, não só happy path. Moderado: poucos casos por cluster, não fuzz massivo. |
| 5 | **Nested dropout → degradação graceful.** Níveis grossos devem valer sozinhos porque o treino corta a hierarquia em k aleatório. | Saída parcial útil: se o pipeline morre depois do intent com metade dos slots, o resultado deve virar `needs_input` rico ("entendi X, falta Y") em vez de falha opaca. Parse parcial é produto, não lixo. |
| 6 | **Limitação admitida: K fixo pra todo input.** O paper paga a hierarquia inteira até pra continuação curta. | O jazz já resolve isso e o paper reforça: **custo ∝ dificuldade** — parse determinístico = 0 chamadas de modelo; ambíguo = 1 chamada barata; irredutível = pergunta. Nunca escalar o caminho caro pra input que o barato resolve. |
| 7 | **Block-diagonal: cada estágio só vê o resumo do anterior**, não o histórico cru. | Reforça a tese de contexto mínimo (cf. grep-is-all-you-need.md): cada camada do harness recebe a *reconstrução destilada* da anterior, não o log integral — modelos fracos afogam em contexto. |
| 8 | **Métrica de diversidade junto com qualidade** (reportam entropia H ao lado de Gen.PPL porque PPL baixa pode ser comprada com colapso de diversidade). | Nos evals do parser: medir também a *distribuição* de intents emitidos — um extrator que sempre chuta o intent mais frequente parece bom no agregado e é inútil. Adicionar contagem de intents distintos/entropia ao relatório de eval. |
| 9 | **Uma ordem canônica fixa simplifica o problema** (hipótese dos autores p/ eficiência de treino). | Quando o jazz pedir saída a modelo fraco, impor **um** formato canônico com campos em ordem fixa (grammar/JSON-schema), nunca formato livre. Menos ordens possíveis = menos erro. |

## 3. Ranking por impacto esperado no jazz

1. **#2 — Seleção verify-first de K candidatos** (maior impacto, alinhado ao invariante do projeto: nada de consenso sem evidência; ataca under-refusal E falso-sucesso ao mesmo tempo).
2. **#1 — Cardápio como hierarquia de codebooks** (barato, é formalizar o que já existe: classify → slot-fill com conjunto válido por estágio; reduz o espaço de erro por chamada de modelo fraco).
3. **#3 — Slots críticos só por enumeração/verbatim** (kill-switch de alucinação de path/número; o paper mostra que compressor aprendido erra *exatamente* nisso).
4. **#4 — Corruption-injection nos evals** (transforma a guarda de "existe" em "medida": quantos % dos upstreams corrompidos o verify pega).
5. **#5+#6 — Parcial→needs_input rico e custo por dificuldade** (já meio implementados; o paper é argumento de design pra não regredir).

### Não adotar
- **NCM como runtime/dependência:** CUDA+PyTorch, ~GB de checkpoint por tarefa, sem PT-BR, eval-only. Inviável no Pentium M e fora do escopo offline-first.
- **Geração paralela/difusão como backend local:** mesma razão; o jazz resolve custo por *roteamento e determinismo*, não por arquitetura nova de modelo.
- **MBR por similaridade como critério de seleção:** consenso sem verify é falso-sucesso elegante. No jazz, seleção é por evidência.
- **VQ/embedding retrieval de intents:** comprime justamente o que não pode errar (nomes/números); o cardápio fica lexical e determinístico.

## Fontes
- arxiv.org/abs/2610.14162 (+ HTML v1): §§1–5, Appendices A–J (arquitetura, Tabelas 1–11, ablações F.1–F.4, failure cases G, limitações I).
- github.com/HazyResearch/ncm (README, MIT; repo eval-only — `evaluate.py`, `scripts/`, `models/`, `tokenization/`; sem training code).
- huggingface.co/hazyresearch/ncm-checkpoints (pares tokenizer+generator por tarefa).
- Cobertura independente: bdtechtalks (2026-11-06), rits.shanghai.nyu.edu, goremy.ai (teste próprio: ~4× vs AR batch-1, AR vence em batch-64; sem streaming; tamanho fixo), alpha-digital.de (sem API/nuvem; ~1.5 GB p/ 725M), janmus1c.github.io/ncm-mlx (port MLX, paridade com paper em Sudoku/TinyStories).
