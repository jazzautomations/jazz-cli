# Modelos fracos: como o jazz-cli se sai (medição + propostas para a arnês)

Branch `harness/weak-models`, 5 de novembro de 2026. Objetivo: medir como o jazz-cli
se comporta com modelos *free* pequenos (Nemotron 3.5 Lightning e North-Mini-Code) e
listar, com evidência dos transcripts, os modos de falha que a arnês poderia compensar.
Nada no agente foi alterado nesta tarefa: só a bateria nova (`evals/weak/`) e este
documento.

**Resumo:**
- Bateria de 24 tarefas fixas; cada tarefa monta um mini-repo descartável e um
  verificador independente (`check.sh` — roda testes/`make`/`git log` e grep; nunca lê
  o que o agente afirma). 24/24 validadas com solução de referência antes dos runs.
- Modelos: `nvidia/nemotron-3.5-lightning:free` e `cohere/north-mini-code:free`
  (vivos tanto no openrouter quanto no kilo). 1 tentativa por tarefa, prazo 300 s.
- Nemotron 3.5 Lightning com `JAZZ_BRAIN=on`: **18/24** (as 6 restantes: 3 falhas
  reais, 3 sem cota). Com `JAZZ_BRAIN=off`, nas 10 tarefas com dados nos dois braços:
  **10/10 off** contra 8/10 on. Off passa nas duas tarefas que on falhou (05 e 06).
- North-Mini-Code é muito mais fraco: on 1/12 (2 falhas reais + cota), off 4/12
  (as 4 com janela de cota passaram, incluindo as 2 que on tentou e falhou).
- **Falso sucesso: 0** em todas as baterias: em toda falha o jazz saiu com erro
  (rc 1/3/9/124) ou execução incompleta — nunca declarou "feito" com o verificador
  em vermelho. O oposto também é achado forte (§3.1): em vários aprovados o
  resultado do check é verde **mas o jazz saiu rc≠0 e "execução incompleta"**.
  Para modelos fracos, o `exit code` é ruidoso; o `check` independente é a única
  referência confiável.
- Runs marcados "cota" (rc 1, ~0 tokens, 2-5 s, limite diário do provedor free)
  não contam como falha do modelo.

## 1. Método

### Bateria (`evals/weak/tasks/`)
24 tarefas pequenas e reais (Go / Python / shell / git):

| # | tarefa | tipo |
|---|--------|------|
| 01 | Go teste unitário falhando (acumulador) | Go |
| 02 | Go import faltando (`strings`) | Go |
| 03 | Go build quebrado (divisão int/float64) | Go |
| 04 | Go renomear função em 3 arquivos (incl. `main` em `cmd/`) | Go |
| 05 | Go formato de moeda em centavos (chamadas em vários arquivos) | Go |
| 06 | Go flag `--grite` na CLI | Go |
| 07 | Go função a partir de docstring (`RevPalavras`) | Go |
| 08 | Go atualizar constantes em `config.go` | Go |
| 09 | py teste falhando (desconto) | Python |
| 10 | py slug de docstring | Python |
| 11 | py import faltando (`re`) | Python |
| 12 | py flag `--maiusculas` (argparse) | Python |
| 13 | py timeout em `settings.json` | Python |
| 14 | py renomear método em 2 módulos | Python |
| 15 | py trocar chamada de função deprecada | Python |
| 16 | py contar ERROs do log → `total.txt` | Python/log |
| 17 | sh quoting com espaço no caminho (`backup.sh`) | shell |
| 18 | sh filtrar `[ERRO]` do log → `erros.log` + `total.txt` | shell/log |
| 19 | git: branch `melhorias` + commit com mensagem exata | git |
| 20 | git: tirar arquivo do rastreio + `.gitignore` + commit | git |
| 21 | Makefile: python2 → python3 | build |
| 22 | py subir versão em `versao.py` + `README.md` | Python |
| 23 | go escrever `TestPar` (check com mutação contra teste fraco) | Go |
| 24 | py escrever testes de `eh_palindromo` (check com mutação) | Python |

Cada tarefa: `prompt.txt` (pedido curto em PT, tom de usuário real), `setup.sh`
(monta o repo descartável em `$1`) e `check.sh` (verificação independente: roda
`go test`/`make`/`python3 -m unittest`/`git log`, diff contra a cópia original que só
o verificador vê, grep no resultado — sai 0 só se o resultado está de fato certo).
As tarefas 23 e 24 ainda aplicam uma mutação no código se o teste do agente passar:
se o teste continua verde com a função invertida, o teste era fraco e o check falha.
Antes dos runs, as 24 validadas com solução de referência de 1-4 linhas:
setup → check falha; solução → check passa (24/24).

### Runner (`evals/weak/run.sh`)
Por tarefa: monta o repo num diretório temporário; roda (HOME descartável por tarefa,
caches de Go compartilhados fora do HOME):

```
jazz -p --yolo --timeout 300s --metrics-out <run>/<tarefa>.metrics.json \
     -m <modelo> "$(cat prompt.txt)" < /dev/null
```

Depois roda `check.sh` e anota por tarefa: sucesso (check verde), **falso-sucesso**
(rc do jazz == 0 mas check vermelho), segundos, rc, tokens in/out, ferramentas,
recoveries, request_errors e o status final (`completed`/`incomplete`…). `--yolo` é
necessário: sem ele o modo `-p` bloqueia quase todo bash em pasta não confiável
("use --allow-tools, --yolo ou jazz trust --yes") — medimos capacidade do modelo+arnês,
não o atrito de aprovação. `analisa.py` agrega os transcripts (`*.events.jsonl`).
O jazz usado foi compilado desta árvore (`CGO_ENABLED=0 GOMAXPROCS=2 go build -p=1
-o /tmp/jz-weak .`, jazz-cli 0.12.1); `GOTOOLCHAIN=local`, `GOPROXY=off` (as tarefas
não têm dependência externa); sandbox do jazz (bubblewrap, modo auto) ativo em
todos os runs.

### Modelos vivos (`jazz omni health`, 5 nov 2026)
Do catálogo de 12 free: 6 vivos no começo do dia (kilo + openrouter: north-mini-code,
nemotron-3-ultra, nemotron-3.5-lightning, beam-free); os outros com chave morta
(401/400). Durante o dia as cotas free se esgotam e voltam em janelas (de ~20 min a
um ciclo de horas), o que limitou a cobertura — cada célula sem dados da tabela é
"cota". Nemotron-3-ultra e beam-free evaporaram antes do primeiro run;
gpt-oss-120b queimou o limite no meio da primeira bateria.

Usados no relatório: `openrouter/nvidia/nemotron-3.5-lightning:free`,
`kilo/nvidia/nemotron-3.5-lightning:free`, `openrouter/cohere/north-mini-code:free`,
`kilo/cohere/north-mini-code:free` — sempre 1 tentativa por tarefa por modelo.

## 2. Resultados

Legenda: **PASS** = `check.sh` verde (a tarefa foi feita de verdade; o status do
jazz pode ter saído `incomplete` — ver §3.1). FALHA = check vermelho com trabalho
real do modelo. "cota" = run morto na janela free (início de run morto ou corte
no meio; não é modo de falha). Colunas: s = duração do run; tok in/out; reqs =
requisições ao modelo; os números vêm de `--metrics-out`.

### Nemotron 3.5 Lightning · JAZZ_BRAIN=on (openrouter p/ 01-12; kilo p/ 13-21)
| # | tarefa | resultado | status jazz | s | tok in/out | reqs | notas |
|---|---|---|---|---|---|---|---|
| 01 | Go teste | **PASS** | completed rc=0 | 27 | 55k / 1,6k | 10 | |
| 02 | Go import | **PASS** | incomplete rc=3 | 125 | 105k / 3,6k | 20 | §3.1 (rc=3 mas job correto) |
| 03 | Go build | **PASS** | completed rc=0 | 101 | 60k / 1,1k | 13 | |
| 04 | Go renomear | **PASS** | completed rc=0 | 71 | 119k / 2,2k | 23 | |
| 05 | Go moeda | FALHA | incomplete rc=3 | 112 | 105k / 4,7k | 20 | orçamento de passos esgotou (§3.5) |
| 06 | Go flag | FALHA | incomplete rc=1 | 116 | 91k / 1,7k | 24 | caminho alucinado no edit (§3.6) |
| 07 | Go docstring | **PASS** | incomplete rc=1 | 230 | 29k / 1,7k | 24 | tool call malformada (§3.9) |
| 08 | Go config | **PASS** | incomplete rc=124 | 300 | 41k / 0,7k | 25 | beiral do prazo (`--timeout 300s`) |
| 09 | py teste | **PASS** | completed rc=0 | 86 | 25k / 0,6k | 14 | |
| 10 | py slug | **PASS** | completed rc=0 | 274 | 33k / 1,0k | 23 | |
| 11 | py import | **PASS** | incomplete rc=1 | 259 | 29k / 0,5k | 24 | §3.9 |
| 12 | py flag | **PASS** | completed rc=0 | 219 | 42k / 0,9k | 26 | |
| 13 | py config | **PASS** | completed rc=0 | 13 | 36k / 0,8k | 6 | (kilo) |
| 14 | py renomear | **PASS** | completed rc=0 | 29 | 78k / 2,5k | 10 | (kilo) |
| 15 | py deprecada | **PASS** | completed rc=0 | 37 | 77k / 2,3k | 10 | (kilo) |
| 16 | py log | **PASS** | completed rc=0 | 12 | 32k / 0,6k | 5 | (kilo) |
| 17 | sh quoting | **PASS** | completed rc=0 | 72 | 64k / 1,6k | 9 | (kilo) |
| 18 | sh log | **PASS** | completed rc=0 | 56 | 60k / 1,2k | 9 | (kilo) |
| 19 | git branch | **PASS** | completed rc=0 | 43 | 97k / 2,5k | 20 | (kilo) |
| 20 | git limpeza | **PASS** | completed rc=0 | 32 | 105k / 1,2k | 16 | (kilo) |
| 21 | Makefile | FALHA | incomplete rc=1 | 33 | 82k / 2,7k | 17 | test guard recusou a única correção (§3.3) |
| 22 | py versão | _cota_ | rc=1 | 2 | 0 | 5 | |
| 23 | go teste novo | _cota_ | rc=1 | 2 | 0 | 5 | |
| 24 | py testes novos | _cota_ | rc=1 | 2 | 0 | 5 | |

**18 PASS / 3 falha real / 3 sem cota; 0 falso-sucesso.**
Mediana de 01-12: 121 s; mediana do kilo 13-21: 33 s.

### Nemotron 3.5 Lightning · JAZZ_BRAIN=off (kilo, tarefas 01-12)
| # | resultado | status jazz | s | tok in/out | reqs | nota |
|---|---|---|---|---|---|---|
| 01 | **PASS** | completed rc=0 | 40 | 97k / 2,2k | 13 | |
| 02 | **PASS** | completed rc=0 | 44 | 95k / 1,9k | 13 | |
| 03 | **PASS** | incomplete rc=9 | 146 | 203k / 4,4k | 23 | §3.1; qualidade pediu `go test` extra |
| 04 | **PASS** | completed rc=0 | 73 | 182k / 4,9k | 20 | |
| 05 | **PASS** | completed rc=0 | 29 | 43k / 1,7k | 6 | ← FALHA com cérebro |
| 06 | **PASS** | completed rc=0 | 122 | 145k / 4,8k | 25 | ← FALHA com cérebro |
| 07 | **PASS** | completed rc=0 | 245 | 32k / 0,9k | 18 | |
| 08 | **PASS** | incomplete rc=124 | 289 | 51k / 0,8k | 29 | beiral do prazo |
| 09 | **PASS** | completed rc=0 | 93 | 25k / 0,6k | 12 | |
| 10 | **PASS** | incomplete rc=1 | 229 | 34k / 1,3k | 20 | §3.1 |
| 11 | _cota_ | rc=1 | 5 | 0 | 0 | |
| 12 | _cota_ | rc=1 | 6 | 0 | 0 | |

**10/10 off vs 8/10 on nas 10 tarefas com dados nos dois braços.**
Off pediu ~45 % mais tokens_in por tarefa (90,7 k vs 61,5 k — sem a triagem
offline o contexto cresce mais), mas resolveu. Nas tarefas exploratórias (05 e
06) o guia offline atrapalhou mais do que ajudou o modelo fraco (ver §3.5).

### North-Mini-Code (Cohere) · brain on (openrouter, 01-12)
| # | resultado | status jazz | s | tok in/out | reqs | notas |
|---|---|---|---|---|---|---|
| 01 | **PASS** | completed rc=0 | 68 | 76k / 1,7k | 15 | |
| 02 | FALHA | incomplete rc=1 | 102 | 77k / 4,2k | 21 | bloqueios do guard (§3.4, tag com espaço) |
| 03 | FALHA | incomplete rc=1 | 28 | 54k / 1,2k | 16 | bloqueio ao ler cache + rate limit no fim |
| 04 | _cota_ | rc=1 | 4 | 4,5k / 0,1k | 6 | |
| 05-12 | _cota_ | rc=1 | 2-5 | 0 | 0 | |

### North-Mini-Code (Cohere) · brain off (openrouter, 01-12)
| # | resultado | status jazz | s | tok in/out | reqs | notas |
|---|---|---|---|---|---|---|
| 01 | **PASS** | completed rc=0 | 75 | 96k / 3,7k | 25 | |
| 02 | **PASS** | incomplete rc=3 | 56 | 96k / 2,1k | 21 | §3.1 |
| 03 | **PASS** | incomplete rc=9 | 183 | 76k / 2,0k | 22 | §3.1 |
| 04 | **PASS** | incomplete rc=1 | 174 | 262k / 4,8k | 33 | |
| 05 | _cota_ | rc=1 (meio) | 5 | 0,05k | 7 | |
| 06-12 | _cota_ | rc=1 | 3-6 | 0 | 0 | |

**Cohere: 4/4 com o cérebro desligado nas tarefas 01-04; com o cérebro, das 3
tarefas com trabalho (01-03), apenas a 01 passou e as outras 2 falharam.
Padrão idêntico ao do Lightning: off > on para modelos fracos.**

### Números por bateria (métricas `--metrics-out`; só runs com tokens > 0)
| bateria | runs | mediana s | reqs/ tarefa | req_err | ferr_err | tok_in total |
|---|---|---|---|---|---|---|
| lightning on (openrouter 01-12 + micro 22)* | 13 | 116 | 19,5 | 112 | 44 | 738 k |
| lightning on (kilo 13-21) | 9 | 33 | 11,3 | 5 | 9 | 631 k |
| lightning off (kilo 01-12) | 10 | 108 | 17,9 | 56 | 53 | 907 k |
| cohere on (openrouter) | 4 | 48 | 14,5 | 16 | 20 | 211 k |
| cohere off (openrouter) | 5 | 75 | 21,6 | 20 | 30 | 640 k |
| cohere off (kilo, 01-04) | 4 | 76 | 25,2 | 7 | 45 | 781 k |

(\* inclui uma micro-execução pré-cota da tarefa 22: rc=1, 67 tokens.)
Total: **45 execuções com tokens > 0, 0 falso-sucesso.**

## 3. Modos de falha observados → propostas de arnês

Evidência de `work/*.log` + agregados `*.metrics.json` (45 runs com trabalho em 6
segmentos de bateria; contagens de `request_errors`/`tool_errors`/`recoveries` via
`analisa.py` + leitura dos transcripts).

### 3.1 "execução incompleta" com rc≠0 em tarefa CORRETA (o maior sinal de ruído)
Recorrente: entre 20 e 30 % dos **runs aprovados** (on: 02, 07, 11; off: 03, 10;
cohere off: 02, 03, 04) terminam rc=1/3/9, `out=incomplete` e mesmo assim com
`check.sh` verde — o trabalho está certo; a "incomplete" veio do ciclo final
(ou do orçamento de tempo) e não de diff incorreto.
**Proposta:** para o judge da arnês: **exit code do `jazz -p` não é métrica de
sucesso para modelos fracos** — use sempre o verificador independente. No jazz,
quase sem custo: em `-p`/`--yolo`, quando o modelo já executou o teste de
verificação (ou `go build`/`make`) e apenas não gerou mais tool calls, o run
termina em "orçamento de passos/turno encerrado" com rc=1/3/9; a proposta é
registrar `status=completed` e rc=0 no `--metrics-out` quando a última ferramenta
executada foi um teste/build com saída verde; caso contrário, manter incomplete.

### 3.2 sandbox com `GOCACHE` só de leitura em $HOME: erro que o modelo declara como desculpa final
Apareceu em 4 runs (em ambos os modelos e nos dois braços do cérebro):
- Lightning on, 05 (FALHA): nota final — "Cannot run `go test ./...` due to
  read-only filesystem preventing go build cache operations".
- Lightning off, 04 (PASS, com o job correto): nota final — "The `go test`/`go vet`
  commands cannot run due to the host's read-only `/home` filesystem blocking
  Go's build cache, but the code changes are syntactically correct and the rename
  is fully verified via grep" (a verificação real com `go test` funcionou; o
  modelo substituiu-a pelo grep no relato).
- Cohere off (kilo), 03 (FALHA): `open $HOME/.cache/go-build/: read-only file
  system`.

O sandbox liga `HOME` só de leitura: `go build`/`go test` quebram no primeiro
cache-refresh e o modelo fraco troca o teste por justificativa ("syntactically
correct… verified via grep") em vez de redirecionar `GOCACHE` para um caminho
gravável — perde o loop de verificação inteiro.
**Proposta:** injetar `GOCACHE`, `GOMODCACHE` e `PIP_CACHE_DIR` apontando para a
tmp privada do sandbox ao abrir `-p`/`--yolo` em modo auto (o strict-auto já
espera isso). Adicional barato: ao primeiro erro "read-only file system" num
cache de build, o guard responde "aviso de arnê: eu reconfiguro GOCACHE/
GOMODCACHE no ambiente; tente de novo" — o modelo se re-orienta em 1 passo.

### 3.3 `test guard` recusa A ÚNICA correção possível (bloqueio que mata a tarefa)
Tarefa 21 (`Makefile: python2 → python3`): o conserto correto é editar o Makefile,
mas a test guard recusa 3 edits seguidos — "this changes the test configuration
in Makefile and the user did not ask to change tests" — e o modelo queima o
restante do orçamento re-tentando o mesmo edit (eventos: `guard refused:edit` ×3 +
"guard de testes: alteração em Makefile desfeita"). O run termina rc=1 e a tarefa
nunca se conclui, apesar de a correção de uma linha estar clara (o próprio modelo
a escreveu). Em modelos fortes a guarda evita trapaça; em fracos ela
*encerra* a tarefa que só exige a edição bloqueada.
**Proposta:** distinguir mexer no TESTE que valida o resultado (recusar — claro)
de ajustar o invocador/Makefile que define como os testes correm (permitir, com
aviso no log). Ou: após 2 recusas do guard no mesmo alvo em modo `-p`, escalar
explicitamente no stdout "o guard bloqueou X; permitir?" (o operador está
presente no `-p`).

### 3.4 caminho com espaço quebra a raiz do guard de projeto
Nos runs com TAG contendo espaço (`weak-H or-cohere-on`), `cd` para o próprio
projeto e `ls -R` no pai foram bloqueados — "fora da pasta do projeto:
/tmp/jz-weak-results/weak-H or-cohere-on" — apesar de estarem exatamente no projeto
do agente. Custo: ~5 bloqueios nas primeiras 6 requisições (cohere on, 02) —
o modelo entra em loop de exploração sem conseguir progresso.
**Propostas:**
1. (arnês, lado da bateria) TAG sem espaço — já corrigido nos runs seguintes.
2. (jazz) o guard calcula a raiz a partir do `cwd` real, limpo via
   `filepath.Clean`/`EvalSymlinks`, em vez de comparação de prefixo de string.

### 3.5 Orçamento de passos por tamanho de repositório (S: 12 passos) morre cedo
Lightning on, 05: "orçamento de passos da tarefa esgotado (tamanho S: 12 passos,
parada em ~1,5×)" após ~20 requisições, com trabalho não terminado. A arnês mede
tamanho pelo repo, não pela exploração do modelo: um modelo fraco busca muito mais
antes de acertar. **Proposta:** multiplicador por nível de modelo (a tabela
`model-scores` que o jazz já tem) — para weak score, S × 2,0. O handoff permite
continuar, mas no `-p` de tarefa curta o orçamento morre primeiro.

### 3.6 caminho alucinado num edit → bloqueio, sem conselho de correção
Lightning on, 06: o agente chamou `edit` com
`/tmp/jz-weak-results/weak-mnvidia-brain-on/work/06-go-flag-cli/main.go` — nome
que não existe (o real é `weak-mnvidia-brainon`, sem hífen) — e a tarefa falhou
sem a correção aplicada (o modelo descreveu a flag corretamente; nunca a aplicou).
**Proposta:** quando `edit`/`write` recebe um caminho inexistente e existe um
caminho a 1-2 edições dentro do projeto, o jazz responde com o erro "caminho não
existe; o mais próximo é X" em vez de somente "fora do projeto" — com a sugestão
o modelo fraco se re-orienta em um passo (já há lógica fuzzy para comandos).

### 3.7 guard-block em caminho real por string de "fora do projeto"
Cohere on, 02: o modelo precisa `cd` para o diretório de trabalho mas recebe
"bloqueado (modo -p sem confirmação)… fora da pasta do projeto" (TAG com espaço,
§3.4) — além disso recomendou `read /tmp/jz-weak-results/weak-H…` para "verificar
o estado do run" e foi bloqueado; 5 bloqueios seguidos sem progresso.
Mecanismo único de 3.4; correção única cobre os dois.

### 3.8 declaração final do modelo desalinhada com a realidade do repositório
O modelo pode escrever "done: ..." com o repo em estado errado (cohere on, 02–03;
lightning on, 05/06 com orçamento esgotado). O check independente identifica
imediatamente (e o rc já sai rc=1/3 "incomplete", portanto não é falso-sucesso no
sentido estrito). **Proposta de arnês:** instrumento de estudo: adicionar ao
`--metrics-out` um campo `claim_final` com a última frase do modelo e a dupla
(claim, check passou?) por tarefa — medir quantos "done" eram falsos em modelos
fracos para dimensionar quando o jazz deve re-ler o diff antes de aceitar o
encerramento.

### 3.9 tool call JSON malformada encerra como "falha de rede" em vez de retry
Lightning on, 07 e 11 (2 eventos): 07 → "jazz: falha de rede: Parsing failed";
11 → "jazz: falha de rede: Failed to parse tool call arguments as JSON";
ambos terminam em `execução incompleta (error)` + handoff gerado. O turno morre.
**Proposta:** degradar para mensagem de sistema: "sua última tool call <nome> tinha
JSON inválido (<pos>); reenvie corretamente" e deixar o modelo reenviar — só corta
após 2 falhas seguidas. O canal de recover já existe (`recoveries` nas métricas);
basta extendê-lo para o caso parse-error.

### 3.10 achados menores
- Loop de chamadas idênticas: cohere off (kilo), 03 → 40 tool calls em 162 s
  (31 bash, 6 read, 1 edit) e 45 tool_errors nas métricas — o run gira em bash
  repetidos até o erro trocar. Sugerir campo `tool_calls` distinto nas métricas e
  um nudge por repetição sustentada ("chamou X 3× com args idênticos; leia o
  diff do erro antes da próxima").
- Os checkpoints/handoff funcionam bem: cada run interrompido gerou `handoff.md`
  com nota de continuação (recurso que salvou várias retomadas). Único item a
  testar no futuro: retomada automática em `-p`.
- `--timeout 300s` produziu rc=124 nos 2 runs que esticaram além (tarefas 08
  nos dois braços do Lightning). O `check.sh` roda igualmente; e como a arnês
  usa apenas o check como juiz de sucesso, nenhum run rc=124 foi rotulado
  FALHA sem justificação — mas o jazz podia, e devia, distinguir "timeout do
  runner" de "erro do agente" no `--metrics-out`.

## 4. Notas de método

- Cota free diária: janelas de ~20-45 min; a família inteira entra em cota em
  poucas tarefas. As tabelas mantêm os runs de cota descritos (rc=1, ~0 tokens,
  2-6 s, ferramentas 0).
- O `run.sh` imprime `bateria … JAZZ_BRAIN=<valor real da env>`; a primeira
  rodada tinha um bug de leitura (`BRAIN` x `JAZZ_BRAIN`) que imprimiu rótulo
  errado em 3 baterias antigas (dados descartados; nomes de diretório antigos
  evitam colisão). Os valores do relatório vêm de runs cujo rótulo foi conferido
  contra a env real.
- O timeout do runner (300 s) e o prazo do jazz batem, e ambos produzem rc=124
  (2 runs: tarefa 08 nos dois braços do Lightning) — anotado como "beiral do
  prazo" nas tabelas.

## 5. Reproduzir

```
cd <repo> && CGO_ENABLED=0 GOMAXPROCS=2 go build -p=1 -o /tmp/jz-weak .
JAZZ=/tmp/jz-weak \
MODEL=openrouter/nvidia/nemotron-3.5-lightning:free JAZZ_BRAIN=on TAG=ml-on \
  bash evals/weak/run.sh
JAZZ=/tmp/jz-weak \
MODEL=openrouter/nvidia/nemotron-3.5-lightning:free JAZZ_BRAIN=off TAG=ml-off \
  bash evals/weak/run.sh
python3 evals/weak/analisa.py <pasta-das-baterias>
```
Transcripts completos por tarefa em `<pasta>/homes/<tarefa>/…/sessions/*/*.jsonl`
(sessão + eventos), trabalho final em `<pasta>/work/<tarefa>`, totais em
`summary.tsv`. O `analisa.py` imprime por tarefa: reqs, ferramentas, erros de
ferramenta/requisição, tentativas, recuperações, guardas e repetições ≥3.
