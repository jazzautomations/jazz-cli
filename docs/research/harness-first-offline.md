# Jazz como agente autônomo: harness primeiro, LLM opcional

Origem: documento de pesquisa gerado pelo dono com o ChatGPT em 24/10/2026 a partir do repositório público,
salvo aqui em 26/10/2026 como insumo da versão 0.9 (modo offline "de verdade"). Não descreve código existente:
é proposta. O que já existe no jazz e o que falta estão marcados na seção "Mapa para o código" no fim.

## Tese

O harness controla o trabalho; o modelo (pequeno, grande, local, remoto) é componente opcional e substituível.
Sem LLM, o jazz resolve tudo que tem capacidade determinística, procedimento reutilizável e verificador. Com LLM,
o modelo propõe, classifica, resume ou sugere; o harness decide, autoriza, executa, verifica e registra.

Componentes: orquestração determinística com estado explícito; investigação do projeto (índices, testes, docs,
histórico); decomposição hierárquica (objetivo → subtarefas com entradas, saídas, pré-condições, verificador);
perguntas só quando a resposta muda o plano e não pode ser inferida; execução por ferramentas tipadas e
reversíveis; verificação por evidência (testes, códigos de saída, diffs, invariantes), nunca "pronto" por texto;
LLM roteado por nível (0 sem modelo → 4 remoto) só quando o valor justifica; biblioteca de capacidades aprendidas
(procedimento + pré-condições + verificação + proveniência), promovidas só após validação.

## Princípios

Harness primeiro; determinístico onde possível; probabilístico onde agrega; planos explícitos e verificáveis;
perguntas mínimas e decisivas; falha é observação; verificação independente da ação; segurança e reversibilidade
por padrão; portabilidade (386, pouca RAM, sem dependências).

## Referências citadas (conceituais)

HTN: Bercher/Alford/Höller, IJCAI 2019 (https://ijcai.org/proceedings/2019/875); Langley, AAAI 2025
(https://doi.org/10.1609/aaai.v39i27.35091). ReAct (https://arxiv.org/abs/2210.03629). Plan-and-Solve
(https://aclanthology.org/2023.acl-long.147/). Reflexion (https://arxiv.org/abs/2303.11366). Voyager
(https://arxiv.org/abs/2305.16291). SWE-agent (https://arxiv.org/abs/2405.15793). Small LLMs are weak tool
learners (https://aclanthology.org/2024.emnlp-main.929/); SLMs for agentic systems (https://arxiv.org/abs/2510.03847).
Automata-based constraints for decoding (Google Research, CoLM 2024). Testing planning claims against formal
semantics (https://arxiv.org/abs/2610.22469). MAS-ProVe (https://proceedings.mlr.press/v306/venkataramani26a.html).
SWE-bench (https://arxiv.org/abs/2310.06770), SWE-bench Verified, SWE-Bench Pro Verified
(https://arxiv.org/abs/2609.08149). Review of agent paradigms (https://arxiv.org/abs/2406.05804).

## Arquitetura proposta

Pedido → interpretador de intenção/slots → estado da tarefa + contexto → investigador local → decompositor
(métodos HTN leves, regras) → [pergunta decisiva se faltar informação] → [roteador de LLM se necessário] →
validador de plano e políticas → executor tipado → observador + verificadores → sucesso/recuperação →
resultado auditável + capacidade candidata.

Estado explícito: `TaskState{Goal, Status, Context, KnownFacts, Unknowns, Plan[], Evidence[], Decisions[], Attempts}`;
`TaskStep{Goal, DependsOn, Inputs, Action, Preconditions, Expected, Verifier, Status, Attempts, EvidenceIDs}`.
Máquina de estados: new → investigating → needs_input/planning → ready → running → verifying → recovering/
blocked → succeeded/failed/cancelled, com transições válidas e testadas. Registro de capacidades com schema,
risco, pré-condições, efeitos, custo, dry-run, verificador, timeout e recuperação. Recuperação por categoria
observável (nome não encontrado, comando inexistente, teste falhou, timeout, edição conflitante, plano impossível,
orçamento esgotado, verificação indisponível).

Níveis de roteamento: 0 sem modelo (git status, abrir arquivo, rodar teste); 1 regras/heurísticas; 2 LLM local
pequeno (classificar, extrair campos, próxima ação entre ações registradas); 3 modelo local maior (plano,
hipótese); 4 remoto se permitido. Pedidos ao modelo sempre pequenos e com schema; proposta ≠ ordem.

## Avaliação

Métricas: sucesso verificado; falso sucesso; resolução sem LLM; tokens e chamadas por tarefa; perguntas feitas vs
necessárias; precisão de ferramenta; taxa de recuperação; regressões; reversibilidade; latência/memória; taxa de
bloqueio correto. Suíte por níveis: atalhos claros; referências imprecisas; tarefas compostas; ambiguidade real;
falhas recuperáveis; tarefas impossíveis; segurança; persistência; robustez com LLM desligado/pequeno/maior;
generalização em repositórios inéditos. Testes negativos: saber parar.

## Plano incremental

Fase 0 inventário/linha de base → Fase 1 tarefa estruturada e persistente → Fase 2 decomposição determinística em
três domínios (arquivos/símbolos; desenvolvimento; terminal/ambiente) → Fase 3 esclarecimento contextual →
Fase 4 roteador de raciocínio opcional → Fase 5 verificadores e recuperação → Fase 6 biblioteca de capacidades
aprendidas. Primeira fatia vertical: "localizar arquivo descrito de forma imprecisa → confirmar se ambíguo →
abrir/inspecionar → devolver evidências"; depois uma tarefa web (detectar stack, achar padrão, plano pequeno,
incremento, verificadores).

## Mapa para o código (26/10/2026)

| Proposta | No jazz hoje | Falta |
|---|---|---|
| fast paths determinísticos | `intents.go`/`intents.json`, `offers.go` (menus) | intenção → capacidade/método, não só → comando |
| classificação de tarefa | `route_task.go` (11 tipos, S/M/L) | ligar classificação a famílias de métodos e verificadores |
| resolução de nomes por contexto | `nameindex.go`, `fuzzy.go` | usar como fonte de evidência do investigador |
| estado de tarefa persistente | sessão em árvore, checkpoints, `todo`, plancache, handoff | `TaskState`/`TaskStep` explícitos + máquina de estados |
| decomposição | `plan-small`/`study-first` skills (texto) | métodos HTN leves com pré-condições e verificadores |
| perguntas decisivas | `ask_user` + menus numerados | política "investigar antes de perguntar" codificada |
| roteador de LLM | omni + juiz (UCB1), hedging, papéis | níveis 0–4 com "não chamar modelo" como primeira opção; modelo local via servidor OpenAI-compatível |
| verificação por evidência | honest-done, quality gate, diff guard, outparse | verificadores reutilizáveis por domínio e estado "não verificado" explícito |
| recuperação | spiral breaker, cutoff continuation, resume | recuperação por categoria de erro |
| capacidades aprendidas | `jazz improve` (lições), plancache | procedimentos versionados com pré-condições e verificação |
| métricas | `jazz trace`, placar do juiz, evals v1/v2/redteam | taxa sem-LLM, falso sucesso, perguntas necessárias, bloqueio correto |
