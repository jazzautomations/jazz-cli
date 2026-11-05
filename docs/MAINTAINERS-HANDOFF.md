# Passagem de bastão — branch `harness/offline-primitives` (nov/2026)

Quem pega isto continua um trabalho em andamento. Leia inteiro antes de tocar em código.
Regra número um deste repo: **é público**. Nada de hostname, IP, caminho de home, e-mail,
telefone, nome de pessoa real ou id de sessão em commit, doc, fixture ou mensagem de commit.
`.gitleaks.toml` tem regras pra tailnet/home/whatsapp; a auditoria manual é
`grep -rniE 'nome1|nome2|100\.[0-9]+\.|/home/'` no diff antes de qualquer commit.

## O que é este branch

Base: v0.11.0. Tema: o **cérebro offline** (sem LLM) resolve o máximo de pedidos reais na
máquina fraca de referência (1 núcleo, 2 GB, linux/386) com **zero tokens**, e o LLM só entra
quando o offline não dá. Filosofia: *inteligência em tempo de compilação* — o modelo forte
paga uma vez por um ASSET (script, gramática, calibração) que o runtime reusa pra sempre.
Se o LLM volta pro caminho crítico, é dependência, não asset.

Commits, do mais antigo pro mais novo (`git log 54294b5..HEAD`):
1. onda1 (14 itens): netwait com teto, compactação com fallback determinístico, deadline,
   sandbox `--yolo` com PID do host (opt-in), confirmação de intenção sem "cried-confirm".
2. offline-prim: gate held-out (forge_gate.go), procfs, mechrepair, calib.go (matemática),
   backchain — fundações, nem tudo ligado ainda (ver "Pendente").
3. providers: catálogo free limpo, `jazz omni health` (entende o erro: chave × morto × cota ×
   fora), CI diário `free-models-health.yml`, Pollinations sem chave, `--fresh`/`JAZZ_FRESH`.
4. **cardápio v1+v2 (18 scripts novos, 38 embarcados)** + **ligação ao cérebro offline**:
   17 conceitos no léxico, 14 intents, métodos `script:<nome>`, fixture `cardapio` no eval
   (95 tarefas). Dos 38, **26 têm método offline**; os 12 sem (parse_*, run_tests, json_edit,
   yaml_get, csv_stats, extract_*, git_safe, http_check, snapshot_cmd, ui_lint) são ferramentas
   do LLM ou rodam por dentro de outros — `grep -L 'script:<nome>' methods/*.json` lista. UX: `data.text` do script é a primeira linha da resposta.
5. **calibração (0.13, em fechamento)**: `nlu_calib.go` + `nlu/calib.json` — a confiança que
   o cérebro compara com `brLevel0Min` (0.85) e `nuMinConf` (0.6) agora é probabilidade
   calibrada (isotônica, K-fold sobre os próprios exemplos). `Intent.Raw` guarda o score cru.

## Números que valem (medidos na máquina fraca, não aqui)

`jazz eval offline`: **93/93 · falso sucesso 0 · sem LLM 91/93 · perguntas 5 (necessárias 5)
· p50 ~40 ms · p95 ~400 ms · RSS ~140 MB**. Suíte Go inteira: ok em ~255 s.
Baseline antes deste branch: 75/75 (sem as 18 tarefas do cardápio).
Qualquer mudança que derrube `falso sucesso 0` ou suba `perguntas` acima de `necessárias`
está errada até prova em contrário.

## O ciclo (não pule etapas)

1. Edite. `gofmt -l .` vazio, `go vet .` limpo.
2. Local (se a máquina aguentar): `go test -run '<subconjunto>' .` e `go build -o /tmp/j . &&
   /tmp/j eval offline`. Esta máquina de desenvolvimento tem pouca RAM: `GOMAXPROCS=2 GOGC=50
   go test -p=1`, um `go test` por vez; a suíte inteira só na máquina fraca.
3. Commit pequeno, mensagem com os números medidos.
4. Gate na máquina fraca: `git bundle create /tmp/x.bundle <branch>` → copie → lá:
   `git fetch /tmp/x.bundle <branch>:tmpbranch && git checkout tmpbranch && go build ... &&
   ./jazz eval offline </dev/null && go test -count=1 -timeout 1500s .`
   **Sempre `</dev/null` em qualquer `jazz -p`** dentro de script via `ssh 'bash -s' < arquivo`:
   o jazz herda o stdin e engole o resto do script como prompt (aconteceu).
   Eval ANTES da suíte (latência pós-suíte dá falso `max_ms`). Instalação é passo separado,
   só com gate verde: `cp ~/.local/bin/jazz ~/.local/bin/jazz.prev` antes de trocar.
5. Lá o toolchain reescreve `go.mod` (tira `// indirect`): `git checkout go.mod` depois.
   Nunca commite esse go.mod.

Sonda barata sem compilar testes: binário + `JAZZ_OFFLINE=1 jazz -p --offline "frase"
</dev/null` com `HOME` apontando pra pasta descartável; `jazz trace <sessão>` mostra
"interpretado: regra X / empate / classificador escolheu". Pra ver tokens/conceitos use um
teste descartável que chama `nuGet().Parse` (apague antes do commit; `go test -v`, senão a
saída some).

## Contrato de script do cardápio (revisão linha a linha, sem exceção)

- `jazz-scripts/<nome>.py` + `<nome>.json` (name, version, run:"python3", effect, description
  em inglês **≤120 caracteres**, args = JSON schema `additionalProperties:false` com description
  por arg, output{data,text}). `go:embed` registra tudo em `jazz-scripts/*`.
- Python 3.8+ stdlib puro. Subprocess SÓ via `run_argv(lista, timeout)` e SÓ comandos de
  consulta; nunca `shell=True`, `eval`, `exec`, `pickle`, rede. Todo path por `path_in_root`.
  Saída ordenada/determinística, orçamento ~4 s com `truncated:true`, `emit`/`fail`, `text`
  curto em `data` (é a 1ª linha que o usuário lê).
- effect `read` pra git só-leitura (`git` faz o gate bloquear em subagente/plan e confirmar no
  smart). `exec` só pra linters/consultas de ferramentas já instaladas (`shutil.which`).
- Oráculo não pode mentir: processo casa por `comm`/`argv[0]`/token exato, nunca substring
  (um `vim svc.conf` virava "serviço rodando"); dono oculto é `pid: null`, não `pid 0`.
- `scripts_test.go`: `scShipped` (ordem do registro = alfabética) + 1 smoke por script em
  `TestScriptsEachBuiltin` (pule no Windows o que usa /proc, statvfs, git). Testes profundos
  em `<nome>_cardapio_test.go` (`//go:build !windows` quando preciso). Orçamento do spec
  pro LLM: 800 tokens (`TestScriptSpecCompactAndStable`).
- Fixtures sem nome de pessoa/empresa real (use ana/bruno/httpkit). Grep de privacidade
  case-insensitive.
- Agentes paralelos NÃO editam arquivo existente; roster/smokes/ligação são do maestro, numa
  edição só. Commite por path explícito enquanto outros escrevem na mesma pasta.

## Como um script vira capacidade offline (ligação)

1. `nlu/lexicon.json` → conceito(s) novo(s) (palavras já minúsculas, sem acento; frases
   multi-palavra funcionam; `.env` tokeniza COM o ponto; `todos` é `list`, `tira` é `delete`,
   `tem` é `have`).
2. `nlu/intents.json` → intent com `rules` (conceitos + `@file`/`@dir`/`@number`; regra com
   2 conceitos vence regra com 1), ≥10 `examples` (treinam o naive Bayes e são o teste
   `TestNLUDatasetAccuracy` ≥95%), `negatives`. **Não declare slot opcional de kind dir/file
   se o script não precisa**: um slot livre recebe "a palavra que nenhuma regra explicou"
   (`quantos` virou path e o script falhou).
3. `methods/<intent>.json` → `{"cap":"script:<nome>","in":{...},"out":"res"}` + verify
   `script_ok`; `$slot.x`, `$goal` (o pedido inteiro — o script casa com dados reais, ver
   config_get `query`). Fallback `.no_python` SEMPRE com `pre bin.python3 unset`, senão ele
   mente "precisa de python3" quando o script falhou por outro motivo.
4. `brain.go` → `brLevel0Intents` (só leitura, verificável).
5. `evals/offline/tasks.json` → tarefa(s) na fixture `cardapio` (`evaloffline.go` eoFixture)
   com `intent`, `contains`, `questions:0`. Mais um controle negativo quando um conceito novo
   colide com palavra comum.
6. Split multi-intent ("A e B") exige confiança ≥ `brLevel0Min` em cada parte.

## Pendente, em ordem (o que vale mais primeiro)

1. **Calibração — fechar**: `nlu/calib.json` forjado (901 amostras; raw ECE 0.047 → 0.046;
   a curva mostra que scores crus 0.16–0.82 acertam só ~60–64%, regra-só 0.90 acerta 93%,
   regra+classificador 0.97 acerta 99%). Falta: gate na máquina fraca com a confiança
   calibrada (perguntas devem ficar = necessárias), decidir a **política de Chow por faixa de
   risco** (`docs/research/decision-policy-calibrated.md`): agir sem perguntar quando
   P(certo) ≥ limiar(risco) — leitura pode aceitar menos (0.7?), mutação exige mais; o gate
   de permissão continua sendo a proteção de mutação. Regenerar o asset sempre que a
   gramática mudar: `JAZZ_UPDATE_CALIB=1 go test -run 'TestNLUCalibAsset$' .`
   (o teste sem a env verifica que o asset embarcado ainda ajuda; `JAZZ_NLU_CALIB=off`
   desliga em runtime).
2. **0.13 restante**: risco do método como carga (não só rótulo), validação estática dos
   métodos (`Pre`/`Effects` hoje são metadados mortos), `signals.jsonl` emitindo nos pontos
   de miss (signals.go já existe), `exit_in`/colateral nos verifiers.
3. **0.14 asset-forge**: usar `forge_gate.go` (held-out) como portão de todo asset gerado
   (exemplos da gramática, calibração, scripts): só entra o que melhora fora da amostra.
4. **Cardápio v3 (sistema/ops) e v4 (texto/dados)** — lista em
   `docs/research/cardapio-offline.md`. Uma onda por vez, 4 agentes × 2–3 scripts, mesmo
   contrato acima, revisão linha a linha, depois a ligação (léxico/intent/método/eval) pra
   CADA script — script sem intent só o LLM alcança.
5. Reservado ao dono do repo: bump 0.12.0, `git push`, `jazz login chatgpt`, renovar chaves
   de provider recusadas, afrouxar qualquer guardrail.

## Armadilhas já pagas (não pague de novo)

- Confirm "cried-confirm": `brNeedsConfirm` confirma SÓ leitura incerta (typo-only ou
  conf < 0.85); mutação é protegida pelo gate de permissão, não por pergunta dupla.
- `netwait` sem teto comia o orçamento inteiro numa queda de rede (raiz dos timeouts do
  SWE-bench).
- Modelos free mudam de id: `jazz omni health` + CI diário; não hardcode ids em testes sem
  olhar o catálogo.
- O `go test` desta máquina de desenvolvimento morre por memória com outros processos
  pesados abertos; a máquina fraca com 2 GB roda a suíte inteira em ~4 min — use-a.
- Script que roda na mão mas "falha" no cérebro: olhe os `slots` no `.jsonl` da sessão
  (`"slots": {"dir": "quantos"}`) antes de culpar o script.
