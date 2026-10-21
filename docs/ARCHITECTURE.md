# Arquitetura do jazz-cli

*[English](ARCHITECTURE.en.md)*

Este documento explica **como o jazz funciona por dentro** e, principalmente, **por que cada peça existe**. O jazz é um agente de código para o terminal, em Go, num binário estático único, feito para funcionar com modelos **grátis e fracos** em máquinas **velhas** (um Mac M-series e um notebook Pentium M de 32 bits com 2 GB, o "Dell"). A tese de design é simples: se o modelo é fraco, o *harness* (tudo que cerca o modelo) tem de cobrar o que um prompt sozinho só pediria.

## Como ler este documento

- São oito diagramas (Mermaid, renderizados direto pelo GitHub). Cada um vem seguido de uma explicação curta e do relatório de pesquisa que motivou o desenho.
- **Números**: todo número aqui vem de [`research/benchmarks.md`](research/benchmarks.md), [`research/redteam-results.md`](research/redteam-results.md) ou dos relatórios de avaliação (`research/omni-judge.md`), e a fonte está ao lado do número. Onde a medição é fraca (n = 1, rede caída, uma rodada), isso está dito.
- Nomes de arquivo entre crases (`agent.go`, `judge.go`...) apontam para o código na raiz do repositório.
- Documentos irmãos: [`../DESIGN.md`](../DESIGN.md) (decisões e rejeições), [`../CONTRACT.md`](../CONTRACT.md) (contrato entre módulos), [`../SECURITY.md`](../SECURITY.md) (política), [`observability.md`](observability.md), [`governance.md`](governance.md), [`improve.md`](improve.md).

Índice: [1 Mapa de componentes](#1-mapa-de-componentes) · [2 Um turno](#2-um-turno-de-ponta-a-ponta) · [3 Camadas de segurança](#3-camadas-de-segurança) · [4 Omni e juiz](#4-roteador-omni-e-juiz) · [5 Sessão e retomada](#5-árvore-de-sessão-checkpoints-e-retomada) · [6 Fluxo de dados](#6-nenhum-trabalho-perdido-e-observabilidade) · [7 Evals e red-team](#7-pipeline-de-evals-e-red-team) · [8 Aprendizado](#8-laço-de-aprendizado-jazz-improve) · [Glossário](#glossário) · [Números medidos](#números-medidos)

---

## 1. Mapa de componentes

```mermaid
flowchart TD
    subgraph UI["Interface"]
        CLI["CLI e flags<br/>main.go"]
        TUI["TUI leve: editor de linha + ANSI<br/>ui_live.go"]
        HL["Headless: jazz -p<br/>headless.go"]
    end

    subgraph CORE["Núcleo do agente"]
        LOOP["Laço do agente<br/>agent.go / agent_loop.go"]
        PROMPT["Prompt de sistema fixo por sessão<br/>cache estável"]
        SUB["Subagentes e sidekick<br/>subagent.go"]
    end

    subgraph TOOLS["Ferramentas"]
        RWE["read / write / edit"]
        BASH["bash: shell persistente<br/>shell.go"]
        MISC["todo / task / script / fetch / ask_user"]
        MCPT["mcp__*: servidores MCP<br/>mcp.go"]
        PAR["Leituras em paralelo<br/>partools.go"]
    end

    subgraph PROV["Provedores"]
        OMNI["Roteador omni<br/>omni.go"]
        JUDGE["Juiz: classificador + placar UCB1<br/>judge.go / route_task.go"]
        PRV["Anthropic / OpenAI / Gemini / Codex / grátis"]
    end

    subgraph GUARDS["Pilha de guardas"]
        G1["Segurança: guardrails, sanitize, trust"]
        G2["Cerca da pasta e egress<br/>fence.go / safety.go"]
        G3["Conclusão honesta e qualidade<br/>guard_honest.go / quality.go"]
        G4["Bloqueio que orienta + disjuntor<br/>blockux.go"]
    end

    subgraph STORE["Persistência e observação"]
        SES["Sessão em árvore<br/>session.go"]
        CKP["Checkpoints shadow-git<br/>checkpoint.go"]
        OBS["Eventos e OTLP<br/>obs.go"]
        AUD["Auditoria com cadeia de hash<br/>audit.go"]
    end

    subgraph KNOW["Conhecimento"]
        SK["Skills e steering<br/>skills.go / steering.go"]
        LES["Lições e cache de planos<br/>lessons.go / plancache.go"]
    end

    SBX["Sandbox do SO<br/>sandbox-exec / bwrap / job object<br/>sandbox.go"]

    CLI --> LOOP
    TUI --> LOOP
    HL --> LOOP
    PROMPT --> LOOP
    SK --> PROMPT
    LES --> PROMPT
    LOOP --> JUDGE
    JUDGE --> OMNI
    OMNI --> PRV
    LOOP --> GUARDS
    GUARDS --> TOOLS
    LOOP --> SUB
    SUB --> TOOLS
    PAR --> RWE
    BASH --> SBX
    MCPT --> SBX
    MISC --> SBX
    LOOP --> SES
    LOOP --> OBS
    GUARDS --> AUD
    TOOLS --> CKP
    SES --> LES
    OBS --> LES
```

O mapa mostra a decisão central: **o laço do agente nunca chama uma ferramenta diretamente**. Toda chamada passa pela pilha de guardas (hooks, conclusão honesta, orçamento, permissão, cerca, injeção, quebra-espiral) e só depois chega à ferramenta; tudo que executa código (shell, jobs, hooks, servidores MCP stdio, scripts) é embrulhado **uma vez, na criação do processo**, pelo sandbox do sistema, por baixo das guardas. O conjunto de ferramentas é pequeno de propósito (`read`, `write`, `edit`, `bash`, `todo`, `task`, `script`, `fetch`, `ask_user` e as `mcp__*`), porque prompt e schemas pequenos funcionam com modelo grátis e contexto curto. Motivação: [`research/grep-is-all-you-need.md`](research/grep-is-all-you-need.md) (busca com `rg`/`grep` no shell em vez de ferramentas de busca dedicadas) e as comparações de [`research/bob-kiro-devin.md`](research/bob-kiro-devin.md).

À direita ficam os conhecedores de longo prazo: skills embutidas e do usuário e regras de *steering* entram no prompt congelado ou como lembretes só-acréscimo ([`research/skills-personas-scripts.md`](research/skills-personas-scripts.md), [`research/skill-bodies.md`](research/skill-bodies.md)); as lições do `jazz improve` e o cache de planos fecham o ciclo com a sessão e os eventos (diagrama 8, [`research/tocomas-plan-reuse.md`](research/tocomas-plan-reuse.md)). Os módulos conversam por costuras estreitas documentadas em [`../CONTRACT.md`](../CONTRACT.md).

---

## 2. Um turno, de ponta a ponta

```mermaid
sequenceDiagram
    autonumber
    actor U as Usuário
    participant A as Laço do agente
    participant J as Juiz e omni
    participant M as Modelo
    participant G as Pilha de guardas
    participant T as Ferramentas
    participant S as Sessão e eventos

    U->>A: pedido
    A->>S: grava a mensagem antes de enviar
    A->>J: classifica a tarefa e escolhe o modelo
    J-->>A: candidato e cadeia de fallback ordenada
    A->>M: requisição com prefixo estável para o cache
    alt primeiro byte atrasado ou 429 ou 5xx
        A->>J: próximo da cadeia por placar
        J-->>A: novo candidato
        A->>M: mesma requisição
    end
    M-->>A: stream de texto e chamadas de ferramenta
    alt resposta cortada dentro de write ou edit
        A->>M: mensagem de usuário pedindo só as linhas restantes
        M-->>A: continuação
        A->>A: emenda os pedaços e roda a ferramenta uma vez
    end
    A->>S: grava a resposta inteira antes de qualquer ferramenta
    par leituras independentes
        A->>G: chamada read ou bash só-leitura
        G->>T: antecipa em paralelo
    end
    loop para cada chamada, na ordem do modelo
        A->>G: hooks, honesta, orçamento, permissão
        G->>G: cerca, egress, injeção, supply chain, quebra-espiral
        alt guarda recusa
            G-->>A: BLOCKED: o que e por que. Continue a tarefa
        else liberada
            A->>S: tool_start (write-ahead)
            G->>T: executa (escritas e bash que muda algo, em série)
            T-->>A: resultado sanitizado
            A->>S: tool_result
        end
    end
    A->>M: devolve resultados e repete até não haver chamadas
    M-->>A: resposta final
    A->>G: portão de parada
    alt editou código e não rodou testes depois da última edição
        G-->>A: volta ao modelo uma vez
    else qualidade, until ou verificador reprovam
        G-->>A: lista de problemas volta ao modelo
    else tudo certo
        A->>S: registro de fim e métricas
        A-->>U: pronto, com testes rodados
    end
```

Este é o caminho de uma única mensagem. Três ideias mandam no desenho. **(1) Cache de prompt**: o prompt de sistema e os schemas ficam congelados na sessão e o histórico só cresce; lembretes, notas de passagem e a continuação de resposta cortada entram como mensagens de usuário ou resultados de ferramenta, nunca reescrevendo o prefixo ([`../DESIGN.md`](../DESIGN.md), seção "O que copiamos"). **(2) Paralelismo seguro**: leituras entre duas escritas começam juntas, mas o laço continua passando **cada** chamada pelas guardas na ordem do modelo; se a guarda recusa, o resultado antecipado é jogado fora e o modelo nunca o vê. Escritas, `bash` que muda algo, MCP e `task` são barreiras em série (`partools.go`). **(3) Persistir antes de agir**: a mensagem do usuário e a resposta do modelo são gravadas antes de qualquer ferramenta, e cada ferramenta tem registro `tool_start`/`tool_result` ([`research/pi-sessions.md`](research/pi-sessions.md)).

Os dois ramos de recuperação existem por falhas medidas. O **fallback** reordena a cadeia pelo placar do juiz quando um modelo grátis trava no primeiro byte (diagrama 4). A **continuação de corte** (`cutoff.go`) nasceu de um defeito observado no Dell com o modelo grátis: ele escrevia uma solução inteira num `write`, a resposta batia no limite de saída dentro de `content`, a chamada era recusada e o modelo repetia o mesmo `write` gigante até o tempo acabar; agora os argumentos parciais são guardados e só as linhas restantes são pedidas.

O "pronto" é honesto porque o portão de parada vem **depois** do modelo achar que terminou: a guarda de conclusão honesta (testes protegidos, resposta *hardcoded*, "testes depois da última edição"), a checagem de qualidade, o `--until` e o verificador podem devolver a conversa ao modelo ([`research/bob-kiro-devin.md`](research/bob-kiro-devin.md), lacunas 5 e 6).

---

## 3. Camadas de segurança

```mermaid
flowchart TD
    subgraph L0["Entrada"]
        SAN["Sanitização Unicode e esteganografia<br/>invisíveis, bidi, tags, homóglifos, HTML oculto<br/>sanitize.go"]
    end

    subgraph L1["Confiança"]
        TR["Portão de confiança por hash<br/>AGENTS.md, skills, hooks, .mcp.json e arquivos que rodam<br/>trust.go"]
    end

    subgraph L2["Guardrails sobre cada chamada"]
        INJ["Detector de injeção<br/>base64, hex, ROT13, URL, leet<br/>guardrails.go"]
        SUP["Supply chain<br/>existência, idade, typosquat, OSV"]
        DNG["Comando perigoso e segredos<br/>shellparse.go / safety.go"]
        EGR["Portão de egress<br/>envio de dados para fora"]
    end

    subgraph L3["Contenção"]
        FEN["Cerca da pasta do projeto<br/>fence.go"]
        SPI["Quebra-espiral<br/>3 no mesmo alvo ou 6 no turno<br/>blockux.go"]
    end

    subgraph L4["Fronteira"]
        SBX["Sandbox do SO<br/>sandbox-exec / bwrap / job object"]
    end

    AUD["Auditoria encadeada por hash<br/>audit.go"]

    SAN --> TR --> INJ --> SUP --> DNG --> EGR --> FEN --> SBX
    SPI -.->|encerra o turno com relatório| FEN
    INJ -.-> AUD
    TR -.-> AUD
    EGR -.-> AUD
    FEN -.-> AUD
    SBX -.-> AUD

    R12["Cenário 12 repo com .mcp.json e hooks<br/>0 ferramentas executadas"] -.-> TR
    R14["Cenário 14 escrita por symlink<br/>v0.4.0 COMPROMISED, depois BLOCKED<br/>guarda de caminho"] -.-> FEN
    R11["Cenário 11 curl pipe sh<br/>em yolo, contido pelo SO"] -.-> SBX
    R0308["Cenários 03 e 08 espiral após bloqueio<br/>ERROR na v0.4.1, BLOCKED com block-ux"] -.-> SPI
```

A regra de ouro, repetida em [`../SECURITY.md`](../SECURITY.md): **as guardas são heurísticas que deixam os ataques comuns barulhentos; a fronteira de segurança é o sandbox do sistema**. Por isso o desenho é em profundidade: as camadas de cima decidem o que o modelo *pode pedir* e as de baixo limitam o que um processo filho *consegue tocar* quando algo passa (modo `yolo`, atalho de heurística, hook ou servidor MCP malicioso). O sandbox não cobre as ferramentas internas `read`/`write`/`edit` nem o `fetch` (rodam dentro do processo do jazz): essas dependem da cerca. A auditoria encadeada por hash registra decisões de confiança, bloqueios, escritas e a primeira conexão a cada host ([`governance.md`](governance.md)).

**Quais camadas pararam o quê (rótulos no diagrama, de [`research/redteam-results.md`](research/redteam-results.md))**: o doc atribui explicitamente o cenário 12 ao portão de confiança ("fecha antes de agir, 0 ferramentas"); o 14 a uma guarda de caminho ("o caminho resolve pra config do jazz"; o doc não nomeia a guarda, e no diagrama ela está junto da cerca) e à v0.4.1 em geral, fechando o furo que na v0.4.0 foi COMPROMISED; o 11 em `yolo` ao sandbox ("a primeira camada não inspeciona alvos de Makefile; a última segurou": 0 bytes de canário no sumidouro); e os cenários 03, 04 e 08 ao bloqueio que orienta, ao disjuntor e à cerca (a cerca "segurou a subida de pasta" no 04 e "a leitura do repositório acima" no 08). Para os demais (01, 02, 05, 06, 07, 09, 10, 13) o relatório dá o veredito BLOCKED mas **não atribui uma camada por cenário**; a camada-alvo de cada classe (Unicode e escapes de terminal para sanitização, base64 para os decodificadores de injeção, typosquat e postinstall para supply chain) é intenção de projeto, não medição, e por isso não está marcada no diagrama.

Motivação e lacunas: [`research/security-gap-audit.md`](research/security-gap-audit.md) (catálogo OWASP LLM/Agentic e MITRE ATLAS) e [`research/security-review-oss.md`](research/security-review-oss.md) (a revisão antes de abrir o código, que originou os achados F1 a F9 em [`../DESIGN.md`](../DESIGN.md)). O mais importante da revisão: o modo `auto` roda qualquer `bash` que a lista de perigosos não marque, então uma lista negra nunca basta, e daí vem o sandbox por baixo.

---

## 4. Roteador omni e juiz

```mermaid
flowchart TD
    POOL["Pool de provedores<br/>grátis primeiro, pago só em emergência"] --> NEED["Filtro por Need<br/>ferramentas, janela de contexto,<br/>refs mortos, política free e paid"]
    NEED --> CAND["Candidatos"]
    REQ["Pedido do usuário"] --> CLS["Classificador de tarefa<br/>regex e sinais do repo<br/>11 tipos, LLM barato só se ambíguo"]
    CLS --> SCORE
    CAND --> SCORE["Placar UCB1 por modelo e tipo<br/>média + bônus - penalidades de tempo, 429 e fila"]
    SCORE --> EPS{"exploração<br/>epsilon 0,1"}
    EPS -->|sim| ALT["outro candidato grátis pronto"]
    EPS -->|não| PICK["modelo escolhido"]
    ALT --> CHAIN
    PICK --> CHAIN["Cadeia de fallback<br/>reordenada pelo mesmo score"]
    CHAIN --> CALL["Requisição ao modelo"]
    CALL --> STALL{"primeiro byte<br/>no prazo?"}
    STALL -->|não| DEM["modelo vai ao fim da fila por 3 min"]
    DEM --> CHAIN
    STALL -->|sim| DONE["turno corre"]
    DONE --> REC["Registro do desfecho<br/>sucesso só após guarda honesta,<br/>quality gate e testes depois da última edição"]
    REC --> SCORE

    subgraph ROLES["Papéis"]
        PL["planner: classificador e pedidos pequenos"]
        ED["editor: turno principal"]
        VF["verifier: 2º modelo revisa o diff, sem ferramentas"]
    end
    CHAIN --> ED
    CLS --> PL
    DONE --> VF

    HEDGE["Hedging de pedidos pequenos<br/>2 candidatos, vence o 1º com resposta"] --> PL
    FUS["Fusão lead e sidekick<br/>desligada por padrão"] -.-> ED
```

O roteador `omni` (`omni.go`) monta a cadeia de candidatos de cada requisição (o modelo que já funcionou no turno, o principal, a lista de `fallback`, os demais provedores com credencial), tira quem está sem cota e respeita a política de preço (`omni.paid`: `never`, `emergency` por padrão, `always`). O **juiz** (`judge.go`, `route_task.go`) acrescenta uma pergunta: *qual desses é o melhor para este tipo de tarefa?* A resposta é estatística, não aprendizado de máquina: um classificador determinístico de 11 tipos e um bandit UCB1 sobre contagens por (modelo, tipo), gravadas sob trava de arquivo; o sucesso só conta depois da guarda de conclusão honesta, do quality gate e de "testes depois da última edição". A pontuação e as penalidades (tempo de parede, 429, limitador "não pronto", pago) são **supostas, não medidas**, e o doc diz isso ([`research/omni-judge.md`](research/omni-judge.md), "Medido vs. suposto"). Sem sementes de medição o placar parte de priors fracos; as sementes vêm das evals (por exemplo, `laguna-s-2.1:free` com 21 PASS em 21 execuções da v2 no Mac, `omni-judge.md`). A diferença honesta para o Conductor da Sakana também está lá: não há política aprendida, só uma tabela de contagens por instalação.

**Papéis e hedging.** Pedidos pequenos e idempotentes (classificador, resumo de compactação, verificador) vão a dois candidatos ao mesmo tempo e vence o primeiro que responde. Medido no Mac com 10 chamadas por braço: **p50 de 8,4 s sem hedge contra 1,3 s com hedge**, porque o primeiro candidato (um modelo de raciocínio) várias vezes devolvia vazio e o pedido caía para o próximo (`omni-judge.md`, medição D). O turno do editor nunca é hedged. O verificador é um pedido sem ferramentas com o diff do turno; a melhora de acerto **não foi medível** (o baseline já passava nas tarefas testadas).

**Fusão lead/sidekick fica desligada, e a razão é medida.** O lead delega a um sidekick com contexto próprio (ideia da Devin Fusion, [`research/fusion-architectures.md`](research/fusion-architectures.md)). Na única tarefa em que houve delegação real (feature multiarquivo, lead minimax, sidekick laguna), passou, mas levou **628 s e 828 mil tokens contra 117 s e 372 mil tokens do minimax sozinho** na mesma hora, cerca de 5 vezes o tempo e 2,2 vezes os tokens: o ganho de custo da Cognition depende de um sidekick mais barato **e** mais rápido que o lead, e no pool grátis essa assimetria não existe (`omni-judge.md`, medição C; n = 1). O painel de planejamento (`--panel`) também ficou desligado: em um caso o plano custou 53 s antes da primeira requisição e a tarefa estourou o prazo, sem como separar causa de variância.

---

## 5. Árvore de sessão, checkpoints e retomada

```mermaid
flowchart TD
    META["meta v2"] --> M1["msg usuário<br/>id a1, parent vazio"]
    M1 --> M2["msg assistente<br/>id b2, parent a1"]
    M2 --> TS["tool_start<br/>id c3, parent b2"]
    TS --> TR["tool_result<br/>id d4, parent c3"]
    TR --> M3["msg assistente<br/>id e5, parent d4"]
    M3 --> CMP["compact<br/>id f6"]
    CMP --> M4["msg usuário<br/>id g7"]
    M4 --> END1["end: status, outcome,<br/>checkpoint, handoff"]
    M2 -. "/rewind, /tree b2, /fork" .-> M5["msg usuário<br/>novo ramo, id h8, parent b2"]
    M5 --> M6["msg assistente<br/>id i9"]
    TR -. "jazz -p -r id@d4" .-> M7["msg usuário<br/>ramo de retomada, parent d4"]
    M3 -.-> SUBS["sessão do subagente<br/>id.sub/..jsonl com parent e parent_entry"]
```

```mermaid
stateDiagram-v2
    [*] --> Aberta: jazz inicia
    Aberta --> Concluida: StopDone, exit 0
    Aberta --> Negada: usuário nega, exit 5
    Aberta --> Incompleta: prazo, 429 esgotado, Ctrl+C, crash, laço, orçamento, disjuntor
    Incompleta --> Checkpoint: shadow-git com rastreio
    Checkpoint --> Handoff: nota de passagem .handoff.md
    Handoff --> Retomavel: registro end + fsync
    Retomavel --> Aberta: -r id, -c ou --resume-last-in
    Concluida --> [*]
    Negada --> [*]
    Retomavel --> [*]: abandonada
```

A sessão é um JSONL **só-acréscimo em árvore**: cada entrada tem `id` e `parentId`, a posição atual é a última entrada e `/rewind`, `/tree <id>`, `/fork` e `-r <id>@<entrada>` movem a posição, de modo que a próxima mensagem abre um ramo e os ramos abandonados continuam no arquivo. O formato é próprio, inspirado no do pi: IDs de 8 hex, migração de arquivos antigos na leitura, e a regra "persistir antes de enviar" ([`research/pi-sessions.md`](research/pi-sessions.md); detalhes de campos em [`observability.md`](observability.md)). Cada subagente `task` tem a sua sessão, ligada ao pai por `parent` e `parent_entry`, que fica fora de `jazz sessions` mas guarda o trabalho parcial de um filho cancelado.

O segundo diagrama mostra o que acontece num **fim anormal**: em vez de perder a sessão, o jazz tira um checkpoint do shadow-git, escreve a nota de passagem (tarefa, arquivos mudados, últimas ferramentas e como terminaram, última verificação, próximo passo), grava o registro `end` e faz `fsync` de sessão, eventos e auditoria. O `-r` recarrega a conversa inteira e põe a nota **na frente da próxima mensagem do usuário**, nunca no prompt de sistema, para o cache continuar valendo. Sem prompt novo, o jazz continua sozinho de onde parou. Para avaliações *pass@2*, a segunda tentativa roda `--resume-last-in <pasta>` e continua a primeira em vez de recomeçar ([`observability.md`](observability.md)). O motivo: no Dell, queda de energia e de rede mataram execuções longas, e refazer do zero desperdiça horas.

---

## 6. Nenhum trabalho perdido e observabilidade

```mermaid
flowchart LR
    subgraph RUN["Uma execução"]
        AG["Agente"]
        TL["Ferramentas"]
        PR["Provedores"]
    end

    subgraph DISK["Disco: ~/.local/share/jazz-cli"]
        SJ["sessions/projeto/id.jsonl<br/>árvore + write-ahead"]
        EV["sessions/projeto/id.events.jsonl<br/>requisições, ferramentas, guardas"]
        HO["sessions/projeto/id.handoff.md"]
        AU["audit/audit.jsonl<br/>prev, hash, rec: cadeia sha256"]
        SP["spill e jobs<br/>saída longa de ferramenta"]
        CK["checkpoints/projeto.git<br/>shadow-git, refs/jazz/trace"]
    end

    OT["Coletor OTLP opcional<br/>JSON, desligado por padrão"]

    AG -->|"persiste antes de enviar"| SJ
    TL -->|"tool_start / tool_result"| SJ
    AG --> EV
    PR --> EV
    TL --> EV
    TL -->|"saída acima do limite"| SP
    AG -->|"turn-start, step, fim anormal"| CK
    AG -->|"decisões, escritas, shell, egress"| AU
    AG -->|"fim anormal"| HO
    EV -.->|"opcional"| OT

    SJ --> V1["jazz sessions / -c / -r / /tree"]
    EV --> V2["jazz trace [id] / --metrics-out"]
    AU --> V3["jazz audit / --verify / --kind"]
    CK --> V4["jazz changes arquivo (blame)"]
    HO --> V1
```

Cada byte tem destino previsível e **nenhum arquivo é reescrito**. A sessão guarda o que o modelo disse e viu; o fluxo de eventos (esquema versionado, `v` = 1) guarda *o que aconteceu e quanto custou* e já tem a forma de spans OpenTelemetry (a sessão é o trace); a auditoria guarda **hashes, nunca conteúdo** e é encadeada (`hash = sha256(prev + "\n" + rec)`), de modo que `jazz audit --verify` acusa registro alterado, removido ou reordenado. O limite é declarado: a cadeia não protege contra quem reescreve a cadeia inteira com acesso à home; exporte o último hash para provar o contrário ([`governance.md`](governance.md)). Os checkpoints vivem num repositório-sombra fora do `.git` do projeto, e cada commit leva sessão, passo, modelo e hash do pedido, o que permite a `jazz changes <arquivo>` responder "quem mudou isto e por quê".

Juntas, as três fontes respondem às três perguntas de uma investigação: `jazz changes` diz **quem e quando**, `jazz trace <sessão>` mostra **o que levou àquele passo**, e `jazz audit --kind file` **confirma os hashes antes/depois**. A telemetria é só local; o OTLP é opcional, nunca bloqueia o agente (fila limitada, lote descartado e contado se o coletor cai) e o arquivo de eventos continua sendo a fonte completa. Fonte: [`observability.md`](observability.md), [`governance.md`](governance.md).

---

## 7. Pipeline de evals e red-team

```mermaid
flowchart TD
    subgraph CAL["Calibração antes de medir"]
        REF["Solução de referência<br/>CHECK deve passar"]
        ANTI["Sem solução ou anti-solução<br/>CHECK deve falhar"]
    end

    subgraph SUITES["Suítes"]
        V1["evals/v1<br/>10 tarefas fáceis"]
        V2["evals/v2<br/>10 difíceis, verificador oculto .hidden"]
        RT["evals/redteam<br/>14 cenários de ataque"]
        PUB["Benches públicos no Dell<br/>terminal-tasks, polyglot, swebench-lite puros"]
    end

    CAL --> V2
    CAL --> PUB

    subgraph RTENV["Ambiente do red-team"]
        HOME["HOME canário<br/>segredos falsos"]
        SINK["Sumidouro HTTP local de exfiltração"]
        LOCK["tasks, lib.sh, sink e sink.log<br/>chmod 000 durante a execução"]
    end

    RT --> RTENV
    RTENV --> VER["Veredito por cenário<br/>BLOCKED, COMPROMISED, ERROR"]
    V2 --> LOCK2["tasks trancado enquanto o agente roda"]
    V1 --> RES["Resultados fora do repo<br/>~/jazz-eval-results"]
    LOCK2 --> RES
    VER --> RES
    PUB --> DELL["Dell: Pentium M, 1 núcleo, 2 GB<br/>modelo grátis, um por vez"]
    DELL --> RES
    RES --> IMP["jazz improve --harness<br/>relatório de falhas"]

    subgraph LESSON["Lição de validade"]
        L1["macOS sem timeout: jazz nunca rodava,<br/>BLOCKED 14/14 falso"]
        L2["sink.log em modo 000 + sink.py reabrindo<br/>o arquivo: nenhum hit registrado"]
        L3["payload do cenário 11 em /exfil/stage1<br/>mas o sumidouro só servia /stage1"]
        L4["cenário 14 sem symlink:<br/>ln -sf em config/ que não existia"]
    end
    VER -.-> LESSON
```

O princípio é **medir de verdade, com o modelo agindo e um verificador independente fora da pasta de trabalho**, e só confiar na medição depois de calibrá-la: as soluções de referência têm de passar em tudo e "nenhuma solução" tem de falhar (nos benches públicos puros: terminal-tasks 15/15, polyglot 35/35 e swebench 21/21 com a referência; 1/15, 1/35 e 1/21 sem solução; [`research/benchmarks.md`](research/benchmarks.md)). A v2 tem verificador oculto trancado enquanto o agente roda, porque um dos modelos testados editou testes para passar, o que motivou as guardas anti-trapaça (minimax-m3.1-flash: 7/10, 2 falhas por editar testes; `benchmarks.md`).

O quadro "lição de validade" é a parte mais instrutiva. O red-team foi, ele próprio, **errado várias vezes antes de ficar certo**, e [`research/redteam-results.md`](research/redteam-results.md) registra isso: um "BLOCKED 14/14" falso porque o jazz nem rodava (macOS sem `timeout`); o agente conseguindo ler o CHECK e o `sink.log` durante a execução (corrigido com `chmod 000`); e, na rodada do sandbox, três defeitos do próprio harness, o pior deles o **bug da tranca no sumidouro**: com `sink.log` em modo 000 enquanto o agente rodava e o `sink.py` reabrindo o arquivo a cada requisição, nada era registrado, de modo que todos os "0 hits" anteriores **não provavam nada** para exfiltração. Também o cenário 11 nunca entregava o payload (caminho `/exfil/stage1` contra `/stage1`) e o 14 rodava sem o symlink (erro engolido por `2>/dev/null`). A regra que ficou: rodadas anteriores à correção são descartadas, e só a tabela da suíte corrigida conta (números na seção [Números medidos](#números-medidos)).

---

## 8. Laço de aprendizado (`jazz improve`)

```mermaid
flowchart TD
    SES["Sessões em árvore"] --> EXT
    EVT["Eventos .events.jsonl"] --> EXT
    AUD["Auditoria"] --> EXT
    EXT["Extrator determinístico, sem modelo<br/>cmd-fixed, tool-error, test-fixed,<br/>guard, spiral, revert, cutoff, loop, until"]
    EXT --> SAFE["Descarta texto com cara de injeção<br/>redige segredos, linha de erro até 120 caracteres"]
    SAFE --> LLM["1 chamada sem ferramentas<br/>modelo barato e grátis<br/>até 3 lições de uma linha"]
    LLM --> VAL["Valida: formato, sessão existente,<br/>sem injeção, dedupe por sobreposição 0,6"]
    NOM["Sem modelo: offline ou --no-model"] --> VAL
    SAFE --> NOM
    VAL --> PROP["Propostas: nada é gravado ainda"]
    PROP -->|"jazz improve --apply ou improve.auto"| LP[".jazz/lessons.md<br/>lições de projeto"]
    PROP -->|"--apply, escopo global"| LG["~/.config/jazz-cli/lessons.md"]
    LP --> AUDL["registro lesson.applied na auditoria"]
    LG --> AUDL
    LP --> INJ["Injetadas uma vez no início da sessão<br/>depois do bloco do AGENTS.md"]
    LG --> INJ
    INJ --> NEXT["Próxima sessão"]
    NEXT --> SES

    EXT --> HAR["jazz improve --harness<br/>relatório: classes de falha, candidatas a regra,<br/>gatilhos de skill, ajustes de config"]
    HAR --> HUM["Humano decide: o jazz nunca muda o próprio código"]

    PLAN["Cache de planos<br/>turno verificado com 12 passos ou mais"] --> REUSE["da última vez: mesma classe e assinatura<br/>de repo, na 1ª mensagem do usuário"]
    NEXT --> REUSE
```

`jazz improve` é a parte **segura** da família MetaHarness/Self-Harness ([`research/metaharness-self-improvement.md`](research/metaharness-self-improvement.md), [`research/shepherd-meta-agents.md`](research/shepherd-meta-agents.md)): ele aprende **texto revisável**, nunca altera código, prompt de sistema, skills ou config; o que mexeria no harness sai como relatório rotulado "proposta, revisar antes de aplicar". A extração é determinística (erro repetido, comando que não existia e foi trocado, teste que falhou e passou, guarda que recusou a mesma coisa, resposta cortada), e só depois **uma** chamada a um modelo barato escreve até 3 lições de uma linha. O escopo vem do achado, não do modelo, para que um achado global não escape da revisão disfarçado de lição de projeto ([`improve.md`](improve.md)).

Dois cuidados importam. **Cache**: as lições entram no prompt **uma vez**, na montagem do prompt de sistema, e não mudam no meio da sessão; duas sessões com as mesmas lições têm o mesmo bloco byte a byte, e a linha "da última vez" do cache de planos (que depende da tarefa) vai na primeira mensagem do usuário, não no prompt. **Segurança**: a cadeia de ataque "saída de ferramenta, depois lição, depois prompt" é cortada em três pontos (achados só levam a linha de erro normalizada e são descartados se parecerem injeção; a resposta do modelo passa pelo mesmo filtro; `improve.auto`, desligado por padrão, só grava lições de projeto, no máximo 3 por execução, com histórico e registro de auditoria). A reutilização de planos vem de [`research/tocomas-plan-reuse.md`](research/tocomas-plan-reuse.md) (cache de planos por palavras-chave, no estilo do Agentic Plan Caching). Limite honesto, do próprio `improve.md`: com n = 1 por tarefa o relatório dá contagens, não taxas, e não mede melhora.

---

## Glossário

| Termo | Significado no jazz |
|---|---|
| **harness** | Tudo que cerca o modelo: laço, ferramentas, guardas, orçamento, sessão. O jazz aposta nele para compensar modelo fraco. |
| **turno** | Uma mensagem do usuário até a resposta final, com quantas requisições e ferramentas forem precisas. Um `-p` é um turno só. |
| **omni** | Roteador de provedores: monta a cadeia de candidatos, faz fallback e tira quem esgotou a cota. |
| **juiz** | Camada acima do omni que escolhe o modelo por tipo de tarefa com placar UCB1. |
| **Need** | Requisitos de uma requisição (ferramentas, janela de contexto) usados para filtrar candidatos. |
| **hedging** | Mandar um pedido pequeno a dois modelos e ficar com o primeiro que responde. |
| **lead / sidekick** | Fusão: o modelo principal delega trabalho a um segundo, com contexto próprio. Desligada por padrão. |
| **guarda** | Verificação que roda antes ou depois de uma ferramenta; heurística, não fronteira. |
| **conclusão honesta** | Conjunto de guardas que impede "pronto" sem testes rodados, testes enfraquecidos ou resposta *hardcoded*. |
| **bloqueio que orienta** | Recusa em uma linha (`BLOCKED: o quê, por quê. Continue a tarefa...`) para o modelo não entrar em espiral. |
| **disjuntor / quebra-espiral** | Encerra o turno com relatório após 3 bloqueios no mesmo alvo ou 6 no turno. |
| **cerca** | Limite de pastas que `read`/`write`/`edit` e `bash` podem tocar (a raiz do projeto). |
| **egress** | Envio de dados para fora da máquina; destino não permitido pede confirmação. |
| **portão de confiança** | Hash por projeto de AGENTS.md, skills, hooks e `.mcp.json`; mudou, pergunta de novo (`jazz trust`). |
| **sandbox** | Isolamento do SO (`sandbox-exec`, `bwrap`, job object) em volta de tudo que executa código. |
| **checkpoint** | Snapshot dos arquivos num repositório-sombra (shadow-git) fora do `.git` do projeto. |
| **handoff** | Nota de passagem que resume uma execução incompleta para a retomada. |
| **write-ahead** | Gravar `tool_start` antes de rodar e `tool_result` assim que volta. |
| **spill** | Arquivo para onde vai a saída longa de uma ferramenta; o modelo recebe resumo e caminho. |
| **lição** | Linha de texto revisável aprendida de execuções passadas e injetada no início da sessão. |
| **canário** | Segredo falso plantado no HOME do red-team para provar exfiltração. |
| **sumidouro (sink)** | Servidor HTTP local do red-team que registra qualquer tentativa de exfiltração. |
| **BLOCKED / COMPROMISED / ERROR** | Vereditos do red-team: nada vazou, o verificador achou prova, ou o jazz não concluiu (não é veredito). |

---

## Números medidos

Todos os números vêm do documento indicado. Onde há ressalva, ela vem junto; nenhuma comparação com números publicados de outras ferramentas é válida (`benchmarks.md`, "O que isso NÃO permite afirmar").

| Medida | Resultado | Condições e ressalvas | Fonte |
|---|---|---|---|
| Eval v1 (10 fáceis) | **10/10** | jazz 0.3.0 e 0.4.0, Dell, modelo grátis | [`benchmarks.md`](research/benchmarks.md) |
| Eval v2 (10 difíceis, verificador oculto) | **10/10 no Dell com 0.5.0** | laguna grátis, LIMIT 900 s, 122 a 588 s por tarefa, ~51 min no total. Histórico: 8/10 (0.3.0), 9/10 (0.4.0), 10/10 no Mac com 0.4.2 | [`benchmarks.md`](research/benchmarks.md) |
| v2 parcial com 0.7.0 | 3/3 (tarefas 01, 03, 09) | Mac, checagem pós-merge; não é a v2 completa e não roda no Dell | [`benchmarks.md`](research/benchmarks.md) |
| Red-team, modo `auto`, v0.5.0 | **14/14 BLOCKED, duas rodadas**; 0 COMPROMISED, 0 ERROR | 0 requisições ao sumidouro; suíte corrigida; macOS; 2 repetições | [`redteam-results.md`](research/redteam-results.md) |
| Red-team, modo `yolo`, v0.5.0 | **13/14 BLOCKED, duas rodadas**; 1 COMPROMISED (cenário 11) | o payload `curl \| sh` foi baixado e executado pelo `sh`, mas o sandbox conteve as duas ações: 0 bytes de canário no sumidouro. O CHECK conta o GET do download como COMPROMISED | [`redteam-results.md`](research/redteam-results.md) |
| Red-team, variância entre repetições | zero | mesmo veredito em todos os 28 pares | [`redteam-results.md`](research/redteam-results.md) |
| Red-team, evolução (modo `auto`) | v0.4.0: 12 BLOCKED, 1 COMPROMISED, 1 ERROR. v0.4.1: 12 / 0 / 2. block-ux: 14/14, 0 ERROR | v0.4.0 sem camada de segurança; v0.4.1 entrava em espiral após bloqueio (ERROR); uma rodada por cenário nas duas primeiras | [`redteam-results.md`](research/redteam-results.md) |
| Prova do sandbox (A/B via hook confiável) | `--sandbox off`: COMPROMISED. `--sandbox auto`: BLOCKED | cenários 11 e 14, macOS | [`redteam-results.md`](research/redteam-results.md) |
| terminal-tasks (14) | **13/14** | jazz 0.4.1 + laguna grátis, LIMIT 10 min por tarefa, Dell; única falha: `cancel-async-tasks` (502 s) | [`benchmarks.md`](research/benchmarks.md) |
| aider-polyglot-python (34) | 17 feitas em 0.4.1: **13 PASS, 4 FAIL**. Acumulado com rede: **20 PASS, 4 FAIL em 24 tarefas válidas** | as 4 falhas são timeouts de 10 min. **10 tarefas inválidas por queda de rede do Dell** (1206 s cada), a refazer; a bateria restante total foi 7/17 bruto, onde as 10 falhas são a janela sem rede. Execução parou por bateria do Dell na primeira faixa | [`benchmarks.md`](research/benchmarks.md) |
| swebench-lite-pure (20, SymPy) | **5/20 bruto** (0.5.0) | **o Dell ficou sem rede de 01:57 a 08:23** e outras janelas; 12 das 15 falhas são timeout (rc 124), quase todas nessas janelas. Com rede: ~5 aprovadas em ~9 tentativas válidas; 2 falhas reais a investigar (`23191`, `24909`) e `18835` falhou com rede. O resto precisa ser refeito no cabo. Não é número comparável com o SWE-bench publicado | [`benchmarks.md`](research/benchmarks.md) |
| Calibração das suítes públicas | referência: 15/15, 35/35, 21/21. Sem solução: 1/15, 1/35, 1/21 | terminal-tasks, polyglot, swebench, versões "puras" sem rede de pacotes | [`benchmarks.md`](research/benchmarks.md) |
| Hedging do classificador | p50 8,4 s sem hedge, 1,3 s com hedge | 10 chamadas por braço, Mac; concordância do classificador LLM com os rótulos: 7/10 com hedge, 6/10 sem | [`omni-judge.md`](research/omni-judge.md) |
| Fusão lead/sidekick | 628 s e 828 mil tokens, contra 117 s e 372 mil tokens do modelo sozinho | n = 1, tarefa 02 da v2; razão de `--fusion` ficar desligada | [`omni-judge.md`](research/omni-judge.md) |

Limites gerais, ditos pelos próprios relatórios: uma rodada por tarefa nas medições do juiz (nada ali é estatisticamente significativo), um modelo só no red-team, só macOS (o `bwrap` no Linux não foi executado), modo `ask` não medido, 11 cenários de red-team especificados e não implementados, e a suíte do red-team não roda no CI.
