# Cérebro offline do jazz (0.9) — especificação

Autor: Fable (coordenação), 26/10/2026. Insumos: `docs/research/harness-first-offline.md` (tese e mapa para o código)
e `docs/research/offline-brain-research.md` (software autônomo clássico, NLU sem transformer, contexto de código).
Esta spec é o contrato de implementação. Regra geral: **o harness não pode ficar burro quando o modelo é burro ou
não existe.** Tudo aqui roda sem LLM, em linux/386 com 2 GB, e é medido no Dell.

## 1. O que muda de verdade

Hoje: pedido → (fast path `intents.go` → comando) ou → LLM → ferramentas. O LLM é quem "entende".
0.9: pedido → **interpretador** (intenção + slots + entidades do ambiente/projeto) → **estado de tarefa** explícito →
**métodos** (decomposição determinística com pré-condições e verificador) → executor tipado → **verificação por
evidência** → recuperação por categoria. O LLM entra só nos buracos: classificar quando a gramática não cobre,
propor um método novo, escrever código. E sempre como proposta, nunca como ordem.

## 2. Interpretador (NLU clássico, PT e EN)

Pipeline em `nlu.go` (+ dados em `nlu/*.json`, embutidos com `embed`):
1. Normalização: minúsculas, remoção de acentos, Snowball-PT light (sufixos -ar/-er/-ir/-ando/-ção/-mente), gírias
   ("dale", "bora", "roda aí", "mete marcha") → verbos canônicos (`run`, `go`, `fix`).
2. Entidades (Aho-Corasick sobre o `nameindex` já existente + **novo índice de ambiente**): arquivos, símbolos,
   pacotes, comandos no PATH, hosts SSH (`~/.ssh/config`), peers Tailscale (`tailscale status --json`, só leitura),
   serviços (systemd/runit/launchd), contêineres (docker/podman ps), portas abertas, envs. Fuzzy (Damerau ≤ 2 /
   Jaro-Winkler ≥ 0,88) com desempate por frequência e por tipo esperado do slot. "talscle" → `tailscale` (binário) e
   nunca um nome literal novo. Entidade desconhecida com confiança baixa = **pergunta decisiva**, não invenção.
3. Gramática semântica (regras + slots, estilo Phoenix/Snips): ~40 intenções de dev/terminal/ambiente:
   `abrir(arquivo)`, `achar(símbolo|texto)`, `rodar_testes(alvo?)`, `explicar(arquivo|símbolo)`, `instalar(dep)`,
   `atualizar(dep|jazz)`, `status(git|serviço|host)`, `conectar(host)`, `subir(serviço|contêiner)`, `logs(x)`,
   `diff`, `commit(msg?)`, `desfazer`, `renomear(a,b)`, `criar(arquivo|teste)`, `corrigir(erro|teste)`, …
   Cada regra tem exemplos positivos/negativos em `nlu/intents.json` (dataset = teste).
4. Classificador de apoio: naive Bayes multinomial sobre n-gramas de caracteres (3–5) treinado dos exemplos, só para
   desempatar quando ≥ 2 regras casam ou nenhuma casa com confiança; saída sempre com probabilidade.
5. Resultado: `Intent{Name, Slots, Confidence, Evidence[]}`; abaixo de 0,6 → LLM (se houver) ou pergunta.

Métrica: acurácia no dataset (≥ 95 % nos exemplos, ≥ 85 % em frases novas de um conjunto cego de 100 frases que eu,
Fable, escrevo em `nlu/blind_test.json`, incluindo erros de digitação e gírias).

## 3. Estado de tarefa e máquina de estados (`task.go`)

`TaskState{ID, Goal, Intent, Status, Facts map, Unknowns[], Plan []TaskStep, Evidence []Ev, Decisions[], Attempts,
Budget{cmds, llmCalls, seconds}}`; `TaskStep{ID, Method, Inputs, Pre[], Action, Expected, Verifier, Status,
EvidenceIDs}`. Estados: new → investigating → (needs_input | planning) → ready → running → verifying →
(recovering | blocked) → succeeded | failed | cancelled. Transições em tabela, testadas. Persistido no JSONL da
sessão (um evento por transição), aparece no `/tree`, `jazz trace` e no handoff.

## 4. Biblioteca de métodos (HTN leve, `methods.go` + `methods/*.json`)

Método = `{name, intent, pre: [cond], steps: [ação|submétodo], verify: verificador, risk: low|med|high|critical,
undo: ação?, cost: ms estimado}`. Ações são **capacidades registradas** (§5), nunca shell livre. Resolução estilo
Pyhop: primeiro método cujas pré-condições valem; falha de passo → recuperação (§6) → próximo método → bloqueado com
explicação. Métodos iniciais (todos offline):
- `open_file`: resolve nome impreciso (§2) → se ambíguo, lista ≤ 5 candidatos numerados → abre → devolve trecho +
  evidência (caminho, tamanho, mtime, símbolos). **Primeira fatia vertical; tem que ser perfeita.**
- `run_tests`: detecta runner (já existe em `fpTestCommand`) → roda → parse (`outparse`) → resume falhas → se falhou,
  localiza os arquivos dos testes falhos e abre o contexto.
- `fix_failing_test` (com LLM opcional no passo "propor patch"; sem LLM: mostra evidência e para).
- `env_status`: git, serviços, portas, versões de toolchain, uso de disco/RAM.
- `connect_host`, `service_logs`, `container_up` (**menor intervenção**: nunca mexer em kernel, binários, /etc,
  ~/.config/containers para subir um contêiner; se a ação proposta é desproporcional ao objetivo, bloquear com
  explicação — regra vinda do feedback do César).
- `install_dep`: lockfile → gerenciador → `guardrails` (typosquat/OSV) → instalar → verificar import/build.
- `update_jazz`, `git_status`, `git_commit`, `undo_last`.

## 5. Registro de capacidades (`caps.go`)

`Capability{Name, Schema (entrada), Risk, Pre, Effects, DryRun bool, Verifier, Timeout, Undo, Needs
(os, binários, rede)}`. Toda ferramenta e todo script da biblioteca determinística vira capacidade. Modelos também
declaram capacidades (`vision`, `audio`, `tools`, `long_context`): o harness **nunca envia o que o modelo não
suporta**; sem candidato que atenda → resposta determinística ("este modelo não vê imagem; trocar para X?").

## 6. Recuperação por categoria (`recover.go`)

Categoria observável → ação: `name_not_found` → fuzzy + pergunta; `command_missing` → sugerir instalação pela
capacidade `install_tool` (confirmar); `test_failed` → `fix_failing_test`; `timeout` → reduzir alvo / repetir 1x;
`edit_conflict` → reler e reaplicar; `plan_impossible` → explicar e parar; `budget_exhausted` → handoff;
`verifier_unavailable` → marcar "não verificado" (nunca "pronto"). Limite de exploração: N comandos "pra ver" sem
progresso (sem fato novo) → para e pergunta (espiral de exploração, feedback do César).

## 7. Online ensina o offline (`learn.go`)

Toda tarefa concluída **com verificação** com LLM gera um **método candidato**: intenção + slots + sequência de
capacidades + verificador + proveniência (sessão, modelo). Fica em `~/.local/share/jazz-cli/methods/candidates/`.
Promoção para a biblioteca só com: 2 execuções verificadas em contextos distintos + dry-run OK + revisão `jazz improve
--promote` (lista, mostra diff, confirma). Candidato nunca executa sozinho antes de promovido. Frases novas do usuário
que o LLM classificou com sucesso viram exemplos candidatos do NLU (mesmo fluxo de promoção).

## 8. Perfil de ambiente (`envprobe.go`, só leitura)

OS/arch, CPUs, RAM, Termux/WSL/contêiner, toolchains e versões, PATH, hosts, serviços, portas, rede (on/offline),
latência medida por provedor. Entra como `Facts` iniciais de toda tarefa e no planejador: 1 núcleo → métodos
sequenciais e timeouts maiores; offline → só capacidades sem rede; WSL → regras de contenção extras.

## 9. Medição (tudo no Dell, binário 386)

`jazz eval offline`: suíte de 60 tarefas sem modelo (atalhos claros, referências imprecisas, compostas, ambíguas,
falhas recuperáveis, impossíveis, segurança). Métricas: sucesso verificado, falso sucesso (deve ser 0), taxa sem-LLM,
perguntas feitas vs necessárias, comandos por tarefa, latência p50/p95, RAM pico. Linha de base 0.8.1 primeiro, depois
0.9. Nada de número que não venha de uma rodada registrada em `results/`.

## 10. Ordem de entrega

1. §8 envprobe + índice de ambiente no `nameindex` ("talscle"). 2. §2 interpretador com dataset. 3. §3 estado.
4. §5 registro + §4 `open_file`, `run_tests`, `env_status`. 5. §6 recuperação + limite de exploração.
6. §9 eval offline e linha de base. 7. §4 resto dos métodos. 8. §7 aprendizado. Cada passo com testes e medido.
