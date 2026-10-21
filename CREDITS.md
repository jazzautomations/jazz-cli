# Créditos e inspirações

O jazz-cli foi construído estudando o código, a documentação e os artigos de outros projetos.
É justo dizer de onde vieram as ideias e, quando há código derivado, sob qual licença.

## Código derivado (com licença em `third_party/`)

- **pi** (Mario Zechner, MIT, github.com/badlogic/pi-mono): o motor de edição tolerante
  (`edit.go`) é um porte para Go do `edit-diff.ts`, com as mesmas mensagens de erro. O conjunto
  mínimo de ferramentas também vem do pi. Licença em [`third_party/pi/LICENSE`](third_party/pi/LICENSE).
  O modelo de sessão em árvore (`id`/`parentId` em toda entrada, migração na leitura, `/tree`,
  continuar de qualquer ponto) e a regra "gravar antes de enviar, gravar o resultado da ferramenta
  assim que chega" vêm do `session-manager.ts`/`agent-session.ts` do pi (ideia e formato, sem código
  copiado); comparação em [`docs/research/pi-sessions.md`](docs/research/pi-sessions.md).
- **Impeccable** (Paul Bakaus, Apache-2.0, github.com/pbakaus/impeccable, v4.7): a skill
  `frontend-craft` (piso de qualidade, proibições, os modos Persuadir/Operar/Ler/Experiência e a
  regra "uma olhada, corrige o lote, olha de novo") e as regras do `ui_lint` (texto em gradiente,
  paleta violeta, brilho sem deslocamento, faixa lateral, emoji como ícone, frases de marketing,
  imagem quebrada, movimento sem `prefers-reduced-motion`…) vêm da skill e do detector dele. O
  corpo da skill é uma reescrita curta, não cópia; o `ui_lint.py` é uma reimplementação em regex (o
  detector original é Rust/JS e renderiza a página) e reaproveita parte da lista de frases de
  marketing. Licença em [`third_party/impeccable/LICENSE`](third_party/impeccable/LICENSE), aviso
  (Apache-2.0 §4) em [`third_party/impeccable/NOTICE`](third_party/impeccable/NOTICE).

## Inspiração (ideias e compatibilidade, sem código copiado)

- **OpenAI Codex CLI** (Apache-2.0): regras de prompt (continue até resolver, não chute, verifique
  do específico para o amplo), login por código de dispositivo, descoberta de `AGENTS.md`.
- **opencode** (sst, MIT): cascata de busca tolerante no edit, roteamento de provedores.
- **DeepSeek Harness** (MIT): bash persistente com marcadores, "reset, never repair", cuidado com o
  cache de prefixo.
- **Crush** (Charm, MIT) e **Gemini CLI** (Google, Apache-2.0): detector de laço, truncamento de saída.
- **Claude Code** (Anthropic): formato de hooks, permissões, `.mcp.json`, checkpoints, skills e
  `CLAUDE.md`, reproduzidos para compatibilidade (o jazz lê os mesmos arquivos). Nenhum código.
- **Kiro CLI**, **Devin CLI**, **IBM Bob Shell**: steering condicional, modo smart, `--until`,
  revisão em contexto limpo ([`docs/research/bob-kiro-devin.md`](docs/research/bob-kiro-devin.md)).
- Skills embutidas: ideias condensadas e reescritas de **Karpathy guidelines**
  (multica-ai/andrej-karpathy-skills), **ponytail** (DietrichGebert, MIT), **old-coder**
  (AmazingAng), **mattpocock/skills**, **obra/superpowers** e **everything-claude-code**. A origem de
  cada regra está marcada em [`docs/research/skill-bodies.md`](docs/research/skill-bodies.md).
  Os orçamentos do harness (processo enxuto, saída que não precisa de revisão) vêm de ponytail e
  old-coder.
- **Nord**: paleta de cores do terminal.

## Artigos

- **Is Grep All You Need? How Agent Harnesses Reshape Agentic Search.** Sen, Kasturi, Lumer, Gulati,
  Subbiah. arXiv:2605.15184, 2026. Base da busca "grep primeiro" e do `--prefetch grep` experimental
  ([`docs/research/grep-is-all-you-need.md`](docs/research/grep-is-all-you-need.md)).
- **TOCOMAS: Topological Coherence for Self-evolving Multi-agent Systems.** arXiv:2609.37953, 2026.
  Estudado; a parte aproveitada (cache de planos verificados) veio mais de **AgentReuse**
  (arXiv:2512.21309) e **Agentic Plan Caching** (arXiv:2506.14852)
  ([`docs/research/tocomas-plan-reuse.md`](docs/research/tocomas-plan-reuse.md)).
- **Shepherd: Enabling Programmable Meta-Agents via Reversible Agentic Execution Traces.** Yu, Chong,
  Nandi, Soylu, Sun, Manning, Shi. arXiv:2605.10913, 2026. Origem do `--recover` (passagem/descarte
  quando trava) e da dica de *hindsight*
  ([`docs/research/shepherd-meta-agents.md`](docs/research/shepherd-meta-agents.md)).
- **Meta-Harness.** Lee, Nair, Zhang, K. Lee, Khattab, Finn. arXiv:2603.28052, 2026. Desenho
  (ainda não implementado) do `jazz improve`
  ([`docs/research/metaharness-self-improvement.md`](docs/research/metaharness-self-improvement.md)).

Os números citados desses artigos estão nos documentos de pesquisa, com a seção de origem.

## Dependências

Em tempo de execução: apenas `golang.org/x/sys` e `golang.org/x/text` (BSD-3-Clause).
