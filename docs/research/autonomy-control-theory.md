# Teoria de sistemas autônomos sem IA: controle, cibernética e garantias no laço do jazz

Pesquisa de 04/11/2026. Não repete `offline-brain-research.md` (agentes clássicos, NLU sem transformer,
entender o projeto — lá já estão Brooks/subsunção, STRIPS/HTN/Pyhop, BT, OTP, systemd, Rete) nem
`harness-first-offline.md` (tese e mapa). Aqui o tema é outro: **a teoria que dá GARANTIA** —
terminação, convergência, estabilidade, idempotência — para um laço autônomo
*sentir → decidir → agir → verificar → replanejar* rodando sem LLM no Dell
(Linux i686, 1 núcleo, 2 GB, WiFi instável, binário linux/386, só stdlib do Go).

Convenções: como nos docs irmãos, **(estimativa)** marca números não medidos; `file:line` aponta o
código na data da pesquisa. "Custo" é estimado para um Pentium M/Atom ~1,6 GHz.

O laço do jazz já é, estruturalmente, um controlador em malha fechada; o que falta são as peças que
a teoria prescreve para as bordas — amostras ruidosas, integração de erro, estagnação medida,
modelo interno envelhecendo, e uma prova de cobertura. O diagrama:

```
        setpoint r = "pronto" declarado e verificável (--until exit 0, verify do método)
            │
            ▼ e = r − y  (o erro: verificador falhando, pré-condição aberta, slot vazio)
   ┌────────────────────┐    u    ┌─────────────────┐
   │ DECIDIR            │ ──────► │ AGIR            │
   │ nlu.Parse → intenção         │ capacidades     │
   │ mhApplicable → método        │ tipadas (caps.go│
   │ rvDecide → recuperação       │ com efeito/risco│
   └────────────────────┘         │ declarado)      │
            ▲                     └────────┬────────┘
            │ y = medição                  │ efeito no mundo
   ┌────────┴──────────┐            (fs, git, processos,
   │ VERIFICAR         │ ◄─────────  rede, saída de cmd)
   │ brVerify/outparse/│   y
   │ fatos/evidência   │
   └───────────────────┘
   SENTIR = envprobe, nameindex, outparse, rvClassify, shell/exit codes
   REPLANEJAR = rvTable (next_method/retry_smaller/ask/handoff), acHardReset, perguntar
   CONHECIMENTO (K do MAPE-K) = task.Facts, plancache, lessons, sessão JSONL
```

Duas leituras honestas antes de começar: (1) o laço é de eventos discretos, não tempo-contínuo —
importamos a **arquitetura** (realimentação, setpoint, histerese, anti-windup), não funções de
transferência; (2) a terminação do laço offline já é **estrutural**: passos finitos por método,
métodos finitos por intenção, contadores de tentativa por categoria e orçamento de comandos
(`task.go:148`, `Budget{Cmds:8, Seconds:600}`) tornam infinito impossível. As propostas abaixo não
compram terminação — compram **convergência mais cedo, com menos comandos, e provas de cobertura**.

---

## Princípios operacionais

### P1 — Malha fechada: só se corrige o que se mede; o erro define a ação

Um controlador não atua no que acha que o mundo é: mede a saída `y`, compara com o setpoint `r`,
e atua no erro `e = r − y`. A consequência operacional é dura: **"pronto" é um predicado medido,
não uma afirmação**. Fontes: Åström & Murray, *Feedback Systems*, 2ª ed., caps. 1–2 (edição
aberta, [fbsbook.org](https://www.fbswiki.org/wiki/index.php/Feedback_Systems:_An_Introduction_for_Scientists_and_Engineers),
[PDF](http://www.cds.caltech.edu/~murray/books/AM08/pdf/fbs-public_24Jul2020.pdf)); Wiener,
*Cybernetics, or Control and Communication in the Animal and the Machine* (Hermann/MIT Press, 1948;
2ª ed. 1961) — o livro que nomeou o campo a partir do laço de realimentação.

No laço do jazz: o setpoint é declarado antes da ação (`--until <cmd>` deve sair 0;
`verify` do método lista checagens). O erro é sempre observável — exit code, arquivo existe,
testes parseados — nunca "o modelo disse que fez". Instrumenta a etapa **verificar** e já existe:
`acUntilCheck` (`agent_loop.go:198`), `brVerify` (`methods.go:1680`), `hgDoneGate`
(`guard_honest.go:1335`), guarda de afirmação sem ferramenta (`agent_guard.go`).

### P2 — Estabilidade de Lyapunov: convergência se prova com uma função que só desce

Lyapunov (1892): se existe uma função escalar `V(x) ≥ 0`, `V=0` só no objetivo, e toda iteração
reduz `V` estritamente, o sistema converge em passos limitados; quando `V` estaciona, a política
tem que **mudar de regime** — outro método, outro contexto, escalar ao usuário — não repetir.
Fontes: Åström & Murray, caps. 4–5 (mesma referência acima); para a forma didática, qualquer
Khalil, *Nonlinear Systems*. A analogia é honesta se lida como **certificado de não-estagnação**,
não prova de alcançabilidade (indecidível em geral).

No laço: `V(tarefa)` ≈ `w1·passos_pendentes + w2·verificações_falhando + w3·incógnitas_abertas`.
O jazz já detecta estagnação **qualitativamente** — mesma assinatura de falha 3×
(`rcUntilSig`/`sigRun`, `agent_recover.go:191` e `322`), 3 leituras sem fato novo
(`rvExplore`, `recover.go:223`), ciclo de chamadas idênticas (`acLoopLen`, `agent.go:978`) —
mas cada detector é local e não há um escalar de progresso por tarefa nem no cérebro offline.
Instrumenta **replanejar**.

### P3 — Zona morta e histerese: transição de modo não usa amostra única de sensor ruidoso

Comparadores práticos têm zona morta (erro abaixo de ε não move o atuador) e histerese
(limiar de ligar ≠ limiar de desligar) para não *chattering* — oscilar entre regimes a cada
leitura ruidosa. É a diferença entre "degrau" e "dois degraus". Fonte: Åström & Murray,
caps. 11–13 (não-linearidades, anti-windup, robustez).

No laço do jazz: a transição offline↔online é hoje amostra única — um `net.DialTimeout` de 1,5 s
que responde liga `olBack` e a próxima mensagem já vai pro modelo (`offers.go:603`), e um erro
classificado como offline entra no modo (`offline.go:403`). Num WiFi instável (o caso do Dell)
isso flapia por turno: dial ok → tenta modelo → falha → offline → dial ok → … Instrumenta
**decidir** (transições de modo) e **sentir** (o fato `env.online`, hoje binário e de amostra
única, `envprobe.go:203`).

### P4 — Anti-windup: erro integral acima do limite vira mudança de regime, não mais tentativa

Num controlador PI o integrador acumula erro; saturado (atuador no limite) ele "enrola" e o
sistema sobressai — por isso anti-windup congela a integral na saturação. Versão agente: tentativas
são a integral do erro; acima de um teto **global** (não por categoria) elas devem forçar saída
para estado seguro — explicar e parar, handoff — em vez de esgotar a planta. E retry seguro exige
efeito idempotente; sem idempotência, retry é side effect duplicado. Fontes: Åström & Murray §11;
Brooker, "Timeouts, retries, and backoff with jitter", *Amazon Builders' Library*
([aws.amazon.com](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/)):
recuo exponencial com teto e jitter, **retry numa única camada da pilha**, APIs idempotentes ou
com token — e circuit breaker cedo demais.

No laço: o jazz limita tentativas por categoria (`rvTable` `Limit`, `recover.go:108`), por alvo e
por turno (`blockTracker`, `blockux.go:149`), por tamanho de tarefa (`bgStepLimit`, `budget.go:271`),
e por orçamento (`tkBudget`, `task.go:90`). O que falta é o **integral global**: 2 falhas em cada
uma de 3 categorias = 6 tentativas, todas sob o limite local, mas zero progresso. Instrumenta
**replanejar**.

### P5 — Lei da variedade requerida: só variedade destrói variedade

Ashby (1956): o regulador `R` precisa de pelo menos tantas contra-ações quantas as perturbações
`D` que precisa compensar; `V(E) ≥ V(D) − V(R) − K`. Operacionalmente: **toda perturbação
observável precisa de uma resposta registrada, senão a falha é por construção**, não por
acaso — e a cobertura é uma matriz auditável `D × R`, não uma esperança. Fonte: Ashby,
*An Introduction to Cybernetics*, Chapman & Hall, 1956, cap. 11 ("only variety can destroy
variety", p. 207; edição eletrônica na Principia Cybernetica:
[pespmc1.vub.ac.be/ASHBBOOK.html](https://pespmc1.vub.ac.be/ASHBBOOK.html),
[lei da variedade](https://pespmc1.vub.ac.be/REQVAR.html)).

No laço do jazz: `D` = as 11 categorias de `rvTable` + as intenções do NLU; `R` = as ações de
recuperação + os métodos de `methods/*.json` + as capacidades do registro (`cbRegistry`,
`caps.go:110`). O conjunto de `R` é rico; o que não existe é a **auditoria** que prova que todo `D`
tem `R` — hoje um typo num nome de `verify` no JSON só aparece em runtime como "unverified"
(`methods.go:1759`). Instrumenta **decidir** e o build (prova de cobertura em tempo de
compilação — a filosofia do projeto).

### P6 — Teorema do bom regulador: controlar exige um modelo interno fiel

Conant & Ashby (1970) provaram que todo regulador ótimo e simples é homomorfo ao sistema
regulado — ou seja, contém um modelo dele. Corolário operacional: **modelo velho degrada a
regulação**; fatos derivados do mundo precisam de validade temporal e invalidação por escrita.
Fonte: Conant & Ashby, "Every good regulator of a system must be a model of that system",
*Int. J. Systems Science* 1(2):89–97 ([doi:10.1080/00207727008920220](https://doi.org/10.1080/00207727008920220)).

No laço: o modelo interno é `envprobe` (perfil do ambiente, `envprobe.go:203`), `nameindex`
(arquivos/símbolos) e `task.Facts` (`brFacts`, `brain.go:222`). O modelo é medido **uma vez por
tarefa**: se um passo escreve/apaga arquivo, os fatos sobre o mundo ficam velhos até a próxima
tarefa — e verificadores podem ler mundo do passado. Instrumenta **sentir** (frescor e
invalidação do modelo).

### P7 — Auto-estabilização: de qualquer estado, chegar ao estado legítimo em passos limitados

Dijkstra (1974): um sistema é auto-estabilizante quando "independentemente do estado inicial, é
garantido chegar a um estado legítimo em um número finito de passos" — as duas propriedades são
**fecho** (estado legítimo só gera estado legítimo) e **convergência** (de qualquer estado se
chega a um legítimo). Schneider (1993) formalizou e deu os métodos de projeto. Fontes: Dijkstra,
"Self-stabilizing systems in spite of distributed control", *CACM* 17(11):643–644, 1974
([doi:10.1145/361179.361202](https://dl.acm.org/doi/10.1145/361179.361202); transcrição EWD426,
[cs.utexas.edu](https://www.cs.utexas.edu/~EWD/transcriptions/EWD04xx/EWD426.html); a versão
completa é EWD391 de 1973); Schneider, "Self-stabilization", *ACM Computing Surveys* 25(1):45–67
([doi:10.1145/151254.151256](https://dl.acm.org/doi/10.1145/151254.151256)).

No laço: o **estado legítimo** da tarefa é definível barato — status ∈ `tkTransitions`, todo passo
referencia uma capacidade registrada, todo passo "done" tem evidência, contadores dentro do
orçamento. O fecho já existe (a tabela recusa transição ilegal, `task.go:156`; `must` grava bug
como decisão em vez de panicar, `task.go:176`); a convergência parcial já existe (handoff/discard
recomeça o contexto de estado ilegítimo, `acHardReset`, `agent_recover.go:476`; arquivos restauram
ao estado anterior, `rcRevertTurn`, `agent_recover.go:518`). Falta o **verificador do invariante**
que detecta estado ilegítimo antes de propagá-lo. Instrumenta o laço inteiro.

### P8 — Máquina de estados explícita: terminação se analisa na tabela de transições

Estados finitos com transições por tabela e estados terminais absorventes fazem da análise de
*liveness* uma propriedade local e testável — e a hierarquia/ortogonalidade de Harel permite
super-estados ("ativo" ⊃ {running, verifying, recovering}) que herdam transições comuns
(cancelamento, timeout). Fonte: Harel, "Statecharts: a visual formalism for complex systems",
*Science of Computer Programming* 8(3):231–274, 1987
([doi:10.1016/0167-6423(87)90035-9](https://doi.org/10.1016/0167-6423(87)90035-9)).

No laço: `tkTransitions` (`task.go:40`) já é a tabela fechada com terminais absorventes
(`tkTerminal`, `task.go:53`) e "succeeded" exige evidência (`task.go:160`) — garantia de
**não-falso-sucesso estrutural**, a mais forte do repositório. Cada passo já é uma mini-FSM
(pending/running/done/failed, `TaskStep.Status`, `task.go:107`). Instrumenta **decidir** e
**replanejar**.

### P9 — STRIPS: efeitos declarados, pré-condição re-checada antes de cada efeito

STRIPS (Fikes & Nilsson, *Artificial Intelligence* 2(3–4), 1971,
[doi:10.1016/0004-3702(71)90010-5](https://doi.org/10.1016/0004-3702(71)90010-5)) separa o
operador em pré-condições e listas de adição/remoção sobre um modelo; a correção do executor vem
de re-avaliar o modelo — em mundo dinâmico, **antes de cada ação**, não só na escolha do plano.
O jazz implementa a variante HTN-ordenada (Pyhop/SHOP2, já coberta em
`offline-brain-research.md` §A): primeiro método aplicável, backtracking para o próximo
(`brExecute`, `brain.go:258`).

No laço: `mhApplicable` (`methods.go:141`) avalia as pré-condições **uma vez**, na seleção do
método; os passos seguintes rodam sob a suposição de que o mundo não mudou — e mudou, porque os
próprios passos escrevem. Instrumenta **decidir** (granularidade da re-checagem).

### P10 — Análise meios-fins: o próximo operador é o que reduz a diferença medida

O GPS não buscava "qualquer operador válido": mantinha uma tabela diferença↔operador e aplicava o
operador cuja função era reduzir a diferença observada entre estado atual e objetivo. Fonte:
Ernst & Newell, *GPS: A Case Study in Generality and Problem Solving*, Academic Press, 1969
([archive.org](https://archive.org/details/gpscasestudyinge0000erns)); Newell, Shaw & Simon, 1959.

No laço: a "diferença" é exatamente a categoria de falha de `rvClassify` (`recover.go:53`). Quando
`command_missing` derruba um método, o próximo deveria ser o que **não depende do mesmo binário**,
não simplesmente o seguinte na prioridade estática (`brain.go:265`, `methods.go:107`). Ordenação
por diferença é o laço de recuperação fechado pelo erro, não pelo índice. Instrumenta
**replanejar**.

### P11 — Subsunção: camadas inibem para baixo; nenhuma camada desliga outra

Brooks (1986, já coberto em `offline-brain-research.md` §A — aqui o ângulo da **garantia**):
cada camada funciona sozinha e camadas superiores só *suprimem entradas* ou *inibem saídas* das
inferiores. A arquitetura resultante tem uma propriedade rara: remover a camada de cima degrada
suavemente, nunca quebra a de baixo — é o "degradação graciosa" formal. E a regra de segurança
inverte a intuição: **a camada mais alta é a de inibição**, não a de decisão.

No laço do jazz, a hierarquia já existe de fato e vale explicitar: **L−1 guardas**
(`permit`, `guard_honest`, `cbMinimalIntervention` — inibem atuação, nunca desligadas pelo modelo);
**L0 reflexos** (fast paths `intents.go:397`, métodos do cérebro — suprem o modelo quando a
confiança ≥ 0,85, `brain.go:593`); **L1 cache/procedimentos** (plancache, lessons — sugerem);
**L2 modelo** (propõe, nunca ordena: `PermitCommand` media tudo, `offline.go:290`). Falta a
tabela que torne a prioridade auditável. Instrumenta **decidir**.

### P12 — MAPE-K: o laço inteiro corre sobre conhecimento compartilhado e versionado

O blueprint da IBM divide o gerenciador autonômico em Monitor–Analyze–Plan–Execute sobre uma base
de **Knowledge** comum — políticas, histórico, topologia — com sensores e efetuadores nas bordas;
a contribuição chave é que o conhecimento é *first-class*: compartilhado, com proveniência, e o que
liga os quatro estágios. Fontes: IBM, *An Architectural Blueprint for Autonomic Computing*, 2003
([white paper](https://www.jroller.org/autonomic/pdfs/ACwpFinal.pdf)); Kephart & Chess,
"The Vision of Autonomic Computing", *IEEE Computer* 36(1):41–50, 2003
([doi:10.1109/MC.2003.1160055](https://doi.org/10.1109/MC.2003.1160055)).

No laço: Monitor = `envprobe`/`obs.go`/shell; Analyze = `nlu.Parse` + `rvClassify`;
Plan = `methods/*.json`; Execute = `caps.go`/scripts; K = `task.Facts` + `plancache.go` +
`lessons.go` + JSONL de sessão. O MAPE-K fecha direitinho; a fragilidade é K disperso — fatos sem
proveniência/TTL (P6) e o monitor com amostra única (P3). Instrumenta a **estrutura toda**.

### P13 — Teleo-reatividade: re-avaliar o objetivo continuamente, não só no fim do plano

Nilsson (1994): um programa teleo-reativo é uma lista ordenada de regras condição→ação avaliada
**continuamente** sobre o mundo; a execução dura até a condição-objetivo, e ação durativa que já
atingiu o objetivo cessa sozinha. Propriedade: resposta imediata a mudança de mundo e
**encerramento precoce** quando o setpoint já vale — nem um passo a mais. Fonte: Nilsson,
"Teleo-Reactive Programs for Agent Control", *JAIR* 1:139–158, 1994
([doi:10.1613/jair.30](https://doi.org/10.1613/jair.30), [arXiv:cs/9401101](https://arxiv.org/abs/cs/9401101)).

No laço: `brExecute` re-avalia só **depois de falha** (a tabela de recuperação é reativa);
verificar no fim do método (`brain.go:359`) é a leitura tardia — se o passo 1 de 4 já satisfez o
setpoint, os outros 3 rodam à toa; e se a pré-condição do método deixou de valer no passo 2,
nada nota (P9). Instrumenta **agir** e **verificar** (re-avaliação por passo).

### P14 — Idempotência como contrato de efeito: verificar primeiro, repetir de graça

Efeito idempotente é o que pode ser aplicado N vezes com o mesmo resultado — e portanto retentado
de graça; efeito não-idempotente exige token de unicidade ou compensação (undo). O teste de
idempotência é o próprio setpoint: **se o verificador já vale antes da ação, a ação é no-op** —
o que dá de graça tanto segurança de retry quanto deduplicação de pedidos repetidos.
Fontes: Brooker (mesma referência de P4 — "APIs com side effects não são seguras para retry a
menos que provoquem idempotência"); a disciplina de efeitos já declarada em `Capability.Effects`
(`caps.go:74`).

No laço: o jazz tem compensação (`checkpoint.go`, `/undo`, `Undo` nas capacidades) mas não marca
idempotência nem faz a checagem pré-ação: "apague X" quando X já não existe roda comandos e
verificação como se o mundo pedisse trabalho. Instrumenta **agir** e **verificar**.

---

## Mapa para o código (estado: **feito** / **parcial** / **só pesquisado**)

| Princípio | Onde está no jazz | Estado |
|---|---|---|
| P1 setpoint explícito + erro medido | `--until` com rodadas e timeout (`agent_loop.go:29-37,198-247`); `verify` por método (`methods.go:1680-1763`); `hgDoneGate` exige teste pós-edição (`guard_honest.go:1335`); afirmação sem ferramenta → nudge (`agent_guard.go`) | **feito** |
| P2 estagnação medida | Assinatura de falha e repetição (`rcUntilSig`, `agent_recover.go:191`; `sigRun≥3`, `:322-348`); streak sem fato novo (`rvExplore`, `recover.go:223-236`); ciclo de chamadas período ≤4 (`acLoopLen`, `agent.go:978`); budget (`task.go:90`) | **parcial** — detectores qualitativos locais; sem escalar `V` por tarefa no cérebro offline |
| P3 histerese/debounce | Probes periódicos existem (`olProbeEvery` 30 s `offline.go:31`; `olBgEvery` 30 s `offers.go:583`; `olProbe` dial paralelo `offline.go:162`) | **parcial** — amostra única decide entrada e saída; flapável em WiFi ruim |
| P4 anti-windup | Limites por categoria (`rvTable.Limit`, `recover.go:108-120`), por alvo/turno (`blockNote`, `blockux.go:149-169`), por tamanho (`bgStepLimit`, `budget.go:271`), de exploração (`rvDefaultExplore=3`, `recover.go:189`) | **parcial** — falta integral global por tarefa |
| P5 variedade requerida | Arsenal tipado: `cbRegistry` (`caps.go:110`), 49 `methods/*.json`, scripts como capacidades (`caps.go:126-145`), menu de ofertas (`offers.go:314`) | **parcial** — cobertura existe, não é provada; verify-name typo degrada a "unverified" em runtime (`methods.go:1758`) |
| P6 modelo interno | `epProfile` com TTL e cache (`envprobe.go:199-237`), `brFacts` (`brain.go:222-254`), `tkFactsFromEnv` (`task.go:216`) | **parcial** — fatos medidos 1× por tarefa; sem invalidação por escrita |
| P7 auto-estabilização | Fecho: tabela de transições + `must` não-panics (`task.go:40-181`); convergência: `acHardReset` (`agent_recover.go:476`), `rcRevertTurn` (`:518`), checkpoints por turno (`checkpoint.go`) | **parcial** — sem verificador do invariante de estado legítimo |
| P8 FSM explícita | `tkTransitions`, terminais absorventes, "succeeded" exige evidência (`task.go:40-59,160`); passos são mini-FSMs (`task.go:107`); eventos persistidos (`tkSessionSink`, `task.go:258`) | **feito** |
| P9 STRIPS/HTN | Métodos com `pre`/`steps`/`verify` (`methods/*.json`, `methods.go:60-72`), resolução Pyhop (`brExecute`, `brain.go:258-421`), `mhApplicable` (`methods.go:141`) | **parcial** — pré-condição avaliada 1× na seleção |
| P10 meios-fins | Próximo método = próxima prioridade (`brain.go:265`); recuperação por categoria (`rvNextMethod`, `recover.go:111-119`) | **só pesquisado** — ordenação não depende da diferença observada |
| P11 subsunção | L0 suprime o modelo (`brLevel0Intents`, `brain.go:576`; `brHeadlessLevel0`, `:601`); guardas não desligáveis (`guard_honest.go:27-31`); modelo só propõe (`PermitCommand`, `offline.go:290`); espiral de bloqueio (`blockux.go`) | **parcial** — camadas existem, sem tabela de inibição auditável |
| P12 MAPE-K | M: envprobe/`obs`/eventos; A: `nlu.Parse` (`nlu.go:897`) + `rvClassify`; P: methods; E: caps/shell; K: `task.Facts`, `pcStore` só em sucesso verificado (`plancache.go:377-433`), lessons | **feito** estrutural; **parcial** em K (disperso, sem TTL) |
| P13 teleo-reativo | Recuperação reativa por passo (`brRecover`, `brain.go:425`); `retry_smaller` re-executa o passo (`brain.go:345`) | **só pesquisado** — objetivo e pré-condições não re-avaliados por passo |
| P14 idempotência | Compensação: checkpoints por turno + `/undo` (`checkpoint.go`); `Undo` declarado em caps (`caps.go:85-87`); `BeginTurn`/`AfterWrite` (`brain.go:498`) | **parcial** — sem flag idempotente nem checagem pré-ação |

**Garantias que o código já entrega de graça** (vale registrar para não repropor): terminação
estrutural (métodos e passos finitos + `attempts` + `Budget`); não-falso-sucesso por construção
(`To(succeeded)` sem evidência falha, `task.go:160`); "unverified" explícito quando nada pode
checar (`methods.go:1682`, `rvVerifierUnavailable`); tudo persistido por transição (auditável via
`jazz trace`, `task.go:258-303`); e o gate de CI que trata falso-bloqueio como falha
(`evaloffline.go:164`).

---

## Propostas (ordenadas por impacto esperado no Dell)

### A1 — Histerese e debounce no sensor de rede (P3, P12-Monitor)

- **Mecanismo.** (a) `olWatch`/`olProbe`: exigir **2 sucessos consecutivos** de dial (janela ≤ 90 s)
  para setar `olBack`; uma falha zera o contador. (b) `olAfter`/`olEnter`: erros de rede
  (`DNS`, `dial`, `unreachable`, `timeout`) entram em offline só na **2ª ocorrência consecutiva**
  ou em probe falho; `401/403`/`no key` entram imediato — não são ruído, são fato. (c) `epProfile`:
  guardar score EWMA (α=0,4) do dial em `envprobe.json`; `Online` vira fato ternário —
  `env.online = 1 | flaky | unset` — e métodos que precisam de rede tratam `flaky` como "tenta,
  mas avisa", não como bloqueio (a linguagem de condições já tem `eq`, `methods.go:125-139`).
- **Arquivos.** `offline.go:160-183` (`olProbe`), `offline.go:403-415` (`olAfter`),
  `offers.go:587-609` (`olWatch`), `envprobe.go:203-237` (persistir score), `methods/*.json`
  (needs de rede aceitam `flaky`).
- **Custo no 32-bit.** 2 ints + 1 float32 no perfil; mesmos dials de hoje (≤ 1×1,5 s a cada 30 s);
  < 1 KB RSS; zero syscalls novos.
- **Métrica.** `false_blocks` ↓ (modo certo na 1ª tentativa); `no_llm_rate` sem quedas por flap;
  `p50` estável entre turnos no WiFi ruim.
- **Impacto 4 · Esforço S–M · Risco baixo** (tudo atrás do `epTTL`/env flags existentes;
  suíte `eval offline` cobre os dois sentidos do modo).

### A2 — Curto-circuito pelo setpoint: verificar antes de agir e sair cedo (P14, P13)

- **Mecanismo.** Particionar os `check` de `brVerify` em **checagens de estado**
  (`file_exists`, `file_gone`, `profile_present`, `status_obtained`, `read_ok`) e **checagens de
  resultado** (`exit_zero`, `cmd_ran`, `tests_passed`, `script_*`). (a) *Entrada idempotente*:
  antes do passo 1, avaliar checagens de estado cujo `arg` não referencia saída de passo
  (`$out`); se todas passam → responder "já estava pronto" + evidência, zero comandos.
  (b) *Saída antecipada*: após cada passo, re-avaliar o subconjunto avaliável; se o método
  inteiro já verifica → pular para `tkVerifying` sem os passos restantes.
- **Arquivos.** `brain.go:274-360` (laço de passos e o ponto de verificação), `methods.go:1680-1763`
  (`brVerify` ganha `mhCheckEvaluable(c, vars)`); `methods/*.json` (nenhuma mudança de formato).
- **Custo.** ≤ 1 `stat` por checagem de estado; µs por tentativa de substituição; sem alocação
  relevante.
- **Métrica.** `conclusão verificada` ↑ em pedidos repetidos ("apaga o X" quando X já não existe:
  hoje = N comandos, proposto = 1 stat); `comandos/tarefa` ↓; `p50` ↓.
- **Impacto 4 · Esforço M · Risco médio-baixo** (só curto-circuita quando **todas** as checagens
  avaliáveis passam — conservador por construção; a suíte mede exatamente isto).

### A3 — Escalar de progresso V(tarefa) com janela de estagnação (P2)

- **Mecanismo.** `tkProgress(t) = 4·steps_pending + 8·steps_failed + 6·len(Unknowns) + 1·attempts`
  (pesos inteiros). Calculado a cada passo e emitido no `tkEvent` (aparece no `jazz trace`).
  Regra de controle: se `V` não decresceu estritamente após **W=3 comandos** gastos → o laço está
  estagnado e **muda de regime**: próximo método, depois pergunta, depois `blocked` — nunca
  repetir. Se `V` **cresce** após um passo mutador (regressão: mais falhas/unknowns que antes) →
  preferir o caminho "discard/handoff" da tabela (o que `rcCountFails > first` já faz no laço
  online, `agent_recover.go:363` — mesma ideia, agora offline).
- **Arquivos.** `brain.go:290-352` (chama por passo), `task.go` (`tkProgress` + campo no
  `tkEvent`), `recover.go` (unifica o sinal com `rvExplore` — o streak vira caso particular).
- **Custo.** ~50 operações de inteiros por passo; < 1 ms (estimativa); nenhum syscall.
- **Métrica.** `comandos/tarefa` ↓ nas tarefas impossíveis/espiral (para em 3 comandos sem
  progresso, não em 8); `false_success` inalterado (para mais cedo e honesto); `recuperação` na
  suíte.
- **Impacto 4 · Esforço M · Risco médio** (pesos/janela calibrados na suíte; gate: `false_blocks`
  tem que continuar 0 em `results/offline-*.json`).

### A4 — Auditoria de variedade: prova de cobertura D×R em build-time (P5)

- **Mecanismo.** `mhCoverage()` rodado em `go test` e em `jazz eval offline --coverage`:
  (1) toda intenção do NLU (`nlu/intents.json`) tem ≥ 1 método, ou está marcada `model_only` no
  dataset — hoje vira "entendi mas não tenho método" (`brain.go:152`), honesto mas cego;
  (2) todo nome de `check` usado em `verify` existe no switch de `brVerify` — hoje um typo cai no
  `default` e degrada a "unverified" **em runtime** (`methods.go:1758`), deve ser erro de carga
  via `mhErr` (mesmo mecanismo de `mhBadCmd`, `methods.go:115`);
  (3) toda capacidade referenciada nos `steps` existe em `cbRegistry`/`brCaps`;
  (4) por intenção, ≥ 1 método cujo `Needs`∩perfil ≠ ∅ num perfil mínimo — nenhum ponto único de
  falha por binário ausente; (5) toda linha de `rvTable` é alcançável por alguma saída de
  `rvClassify` (linha morta = cobertura ilusória).
- **Arquivos.** `methods.go:81-122` (estender `mhErr` a checks desconhecidos — ~10 linhas já na
  carga), novo `coverage_test.go`, `evaloffline.go` (flag `--coverage`), `recover.go` (tabela
  exportada para o teste).
- **Custo.** Zero em runtime; ~150 linhas de teste/audit (estimativa). Forjado em build —
  filosofia da casa.
- **Métrica.** `no_llm_rate` ↑ estrutural (expõe e impede buracos de cobertura);
  `false_success`/`unverified` espúrios ↓ (typos de verify pegos no build).
- **Impacto 4 · Esforço M · Risco baixo** (só lê estruturas; pode falhar CI, nunca o agente).

### A5 — Invalidação de fatos por escrita: modelo interno fresco (P6)

- **Mecanismo.** `TaskState` ganha `epoch` (int) e fatos ganham classe: derivados-do-mundo
  (`file.*`, `dir.*`, `candidates.*`, `git.*`, `env.online`) vs declarados (`slot.*`, `hint`,
  `project.*` — nunca expiram). Capacidades mutadoras (`write`, `edit`, `delete`, `rename`,
  bash com efeito — já detectado pelo hook `AfterWrite`/checkpoint, `brain.go:498`) incrementam
  `epoch`. Pré-condições e verificadores que leem fato derivado com epoch velho re-sentem **só
  aquela chave** (stat/nameindex/dial), não o perfil inteiro.
- **Arquivos.** `task.go` (`Fact` + epoch), `brain.go:222-254` (`brFacts` por chave sob demanda),
  pontos de mutação em `methods.go` (`brFileWrite`/`brDelete`/bash steps).
- **Custo.** 1 mapa extra + re-sense pontual (as mesmas primitivas de hoje, amortizadas).
- **Métrica.** `false_success` ↓ (verificação não lê mundo anterior à mutação); categorias de
  recuperação corretas ↑ (menos `edit_conflict` espúrio por fato velho).
- **Impacto 4 · Esforço M · Risco médio** (sobre-invalidez custa comandos — mitigar invalidando
  por chave/classe, nunca o mapa inteiro).

### A6 — Integral de erro global (anti-windup entre categorias) (P4)

- **Mecanismo.** `brCtx.attempts` é por categoria; adicionar `errTotal` incrementado em todo
  `rvDecide`. `errTotal > Γ` (sugestão inicial 5, calibrar na suíte) força o terminal de regime —
  `explain_stop`/`handoff` conforme a última categoria — mesmo que cada categoria esteja sob o
  próprio `Limit`. Mata a espiral que troca de categoria para continuar tentando.
- **Arquivos.** `brain.go:425-464` (`brRecover`), `recover.go` (constante + doc), `methods.go`
  (`brCtx`).
- **Custo.** 1 inteiro.
- **Métrica.** `comandos/tarefa` ↓ em inputs adversariais; `false_blocks` **corretos** ↑
  (termina limpo em vez de thrash) — vigiando o contador para não virar bloqueio cedo demais.
- **Impacto 3 · Esforço S · Risco médio** (Γ baixo demais = falso-bloqueio; calibrar na suíte e
  medir `questions_needed`/`blocked` por categoria).

### A7 — Evidência por produtor, não por recência (P1, P7)

- **Mecanismo.** Hoje `ps.EvidenceIDs = brLastEvidence(t)` (`brain.go:313`) anexa ao passo o
  **último** item de evidência global — que pode ter sido produzido por um passo anterior. Mudar
  para o intervalo `Evidence[lo:hi]` produzido **durante** `fn`. E passos de capacidades cujo
  `Effects` declara `fact:*` ou que alimentam `verify` devem produzir ≥ 1 evidência — senão o
  passo termina "done-sem-evidência" e a verificação do método responde por ele (alimenta o já
  existente `unverified`, nunca `succeeded`).
- **Arquivos.** `brain.go:312-318`, `methods.go` (`brStep`/`brCaps` declaram `evKind` esperado).
- **Custo.** 2 ints; ~20 linhas.
- **Métrica.** `false_success` ↓ (evidência real por passo); trilha de auditoria correta no
  `jazz trace`/`/tree`.
- **Impacto 3 · Esforço S · Risco baixo.**

### A8 — Re-checagem de pré-condições por passo (P9, P13)

- **Mecanismo.** Antes de executar o passo `si`, re-avaliar `mhApplicable(m, cx.facts)` (barato:
  lookup de mapa). Violação → não executa passo em mundo mudado; despacha para a tabela de
  recuperação como falha observável (`plan_impossible` ou a categoria do fato que sumiu, ex.:
  `bin.X` apagou → `command_missing`) → `next_method`. Fecha o laço sentir→decidir na
  granularidade do passo.
- **Arquivos.** `brain.go:289-304` (logo antes de `fn := brCap`).
- **Custo.** µs por passo.
- **Métrica.** `false_blocks` corretos; `comandos/tarefa` ↓ em planos de 2+ passos.
- **Impacto 3 · Esforço S · Risco baixo** (depende de A5 para que os fatos estejam frescos —
  ordenar atrás dela ou com re-sense pontual próprio).

### A9 — Ordenação por diferença na troca de método (P10)

- **Mecanismo.** Ao falhar com categoria `C`, reordenar os métodos restantes: primeiro os que
  **diferem do que falhou** — `Needs`/`Bins` diferentes (outro binário, não o mesmo ausente),
  primeira capacidade diferente, conjunto de `verify` diferente; a prioridade estática vira
  desempate. Tabela diferença↔método de 5 linhas por categoria, forjada no JSON (ou derivada dos
  campos já existentes).
- **Arquivos.** `brain.go:264-277` (iteração de métodos recebe `cat` da última falha),
  `methods.go` (comparação sobre `mhMethod.Needs`/`Steps`/`Verify`).
- **Custo.** O(#métodos da intenção) comparações por troca; trivial.
- **Métrica.** taxa de recuperação na categoria `falha recuperável` da suíte ↑;
  `comandos/tarefa` ↓.
- **Impacto 3 · Esforço S · Risco baixo.**

### A10 — Verificador do estado legítimo (P7, P8)

- **Mecanismo.** `tkCheckLegit(t)` em ~20 lookups: `Status` ∈ conjunto legal; todo `step.Action` ∈
  `brCaps`∪`script:*`; passo `done` tem evidência; `UsedCmds ≤ Cmds`; passo `running` existe só em
  `tkRunning`. Rodar após cada mutação de `t` (custa µs; sempre ligado, não só em debug).
  Violação → `dbg` + reset conservador do passo infrator para `pending` (auto-estabilização
  local) ou `tkBlocked` com explicação — estado ilegítimo vira evento, nunca propagação.
- **Arquivos.** `task.go` (nova função), `brain.go:290-352` (chamadas).
- **Custo.** µs; ~50 linhas + teste de injeção de corrupção.
- **Métrica.** Defesa em profundidade: bug de planner vira `blocked` explicado, não corrupção
  silenciosa; apoio às métricas da suíte (nada reporta status impossível).
- **Impacto 2–3 · Esforço S · Risco baixo.**

---

## O que não adotar (e por quê)

- **PID literal / sintonia de ganhos.** O laço é de eventos discretos; importa a estrutura
  (realimentação, histerese, anti-windup), não o controle contínuo. Transformar `attempts` em
  "ganho proporcional" é numerologia sem medição.
- **Supervisores online ou múltiplos laços concorrentes** (meta-agente olhando o agente). Custa
  chamadas de modelo ou, offline, só adiciona estado; a "camada de inibição" de P11 entrega o
  mesmo benefício dentro do processo, de graça.
- **Model checking formal do TaskState** (TLA+/SPIN). A máquina tem ~10 estados e a tabela já é
  testada (`task_test.go`); o retorno é sobre a *implementação*, não sobre a especificação —
  A10 cobre o gap real por 1/100 do custo.
- **Replanejamento a cada tick (replanning contínuo estilo T-R puro).** Reavaliar por passo (A8 +
  A2) captura 90% do benefício a custo ~zero; reavaliar a cada comando lido compra latência sem
  informação nova — `rvExplore` já provou que olhar de novo sem fato novo é desperdício.

## Referências (todas verificadas em 04/11/2026)

- Åström & Murray, *Feedback Systems*, 2ª ed.: [fbswiki](https://www.fbswiki.org/wiki/index.php/Feedback_Systems:_An_Introduction_for_Scientists_and_Engineers) · [PDF completo](http://www.cds.caltech.edu/~murray/books/AM08/pdf/fbs-public_24Jul2020.pdf)
- Wiener, *Cybernetics* (Hermann/MIT Press, 1948; 2ª ed. 1961) — referência de livro.
- Ashby, *An Introduction to Cybernetics* (1956): [livro online](https://pespmc1.vub.ac.be/ASHBBOOK.html) · [lei da variedade](https://pespmc1.vub.ac.be/REQVAR.html)
- Conant & Ashby, "Every good regulator…" (1970): [doi:10.1080/00207727008920220](https://doi.org/10.1080/00207727008920220)
- Dijkstra, "Self-stabilizing systems in spite of distributed control" (CACM 1974): [doi:10.1145/361179.361202](https://dl.acm.org/doi/10.1145/361179.361202) · [EWD426](https://www.cs.utexas.edu/~EWD/transcriptions/EWD04xx/EWD426.html)
- Schneider, "Self-stabilization" (ACM Comp. Surveys 1993): [doi:10.1145/151254.151256](https://dl.acm.org/doi/10.1145/151254.151256) · [PDF PSU](https://www.cse.psu.edu/~buu1/teaching/fall05/papers/p45-schneider.pdf)
- Harel, "Statecharts" (SCP 1987): [doi:10.1016/0167-6423(87)90035-9](https://doi.org/10.1016/0167-6423(87)90035-9)
- Fikes & Nilsson, STRIPS (AI 1971): [doi:10.1016/0004-3702(71)90010-5](https://doi.org/10.1016/0004-3702(71)90010-5)
- Ernst & Newell, *GPS* (Academic Press 1969): [archive.org](https://archive.org/details/gpscasestudyinge0000erns)
- Nau et al., SHOP2 (JAIR 2003): [jair.org](https://www.jair.org/index.php/jair/article/view/10362) · Pyhop 1.2.2: [stanford](https://web.stanford.edu/class/cs379c/resources/dialogical/zanax_HOP.py)
- Brooks, "A robust layered control system…" (1986): [doi:10.1109/JRA.1986.1087032](https://doi.org/10.1109/JRA.1986.1087032)
- Nilsson, "Teleo-Reactive Programs for Agent Control" (JAIR 1994): [doi:10.1613/jair.30](https://doi.org/10.1613/jair.30) · [arXiv:cs/9401101](https://arxiv.org/abs/cs/9401101)
- Kephart & Chess, "The Vision of Autonomic Computing" (IEEE Computer 2003): [doi:10.1109/MC.2003.1160055](https://doi.org/10.1109/MC.2003.1160055) · [PDF CMU](https://www.cs.cmu.edu/~15849g/readings/kephart03.pdf)
- IBM, *An Architectural Blueprint for Autonomic Computing* (2003): [white paper](https://www.jroller.org/autonomic/pdfs/ACwpFinal.pdf)
- Brooker, "Timeouts, retries, and backoff with jitter" (AWS Builders' Library): [aws.amazon.com](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/)
