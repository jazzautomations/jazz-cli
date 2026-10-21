# Benchmarks públicos: estado em 2026-10-20 (PARCIAL)

Alvo: Dell Latitude (Pentium M 32-bit, 1 núcleo, 2 GB), modelo grátis
`kilo/poolside/laguna-s-2.1:free`, jazz-cli 0.4.1, uma tarefa por vez.
Suítes (versões "puras", sem rede de pacotes, calibradas no Mac antes):
`terminal-tasks` (14), `aider-polyglot-python` (34), `swebench-lite-pure` (20, SymPy, até 45 min cada).

## O que foi medido (lido do disco do Dell após a queda de energia)
Calibração válida antes de medir: soluções de referência passam em tudo (terminal-tasks 15/15,
polyglot 35/35, swebench 21/21) e "nenhuma solução" falha em quase tudo (1/15, 1/35, 1/21).

| suíte | estado | resultado jazz 0.4.1 + laguna (grátis), LIMIT 10 min/tarefa |
|---|---|---|
| terminal-tasks (14) | completa, 06:08 → 07:07 | **13/14** — única falha: `cancel-async-tasks` (502 s) |
| aider-polyglot-python (34) | 17/34 feitas, 07:07 → 11:30 (Dell desligou por bateria) | **13 PASS / 4 FAIL** nas 17 feitas; 3 das 4 falhas são timeout (rc 124) nas duas tentativas: `dot-dsl`, `forth`, `go-counting`; `paasio` timeout na 2ª; `pass@2` |
| swebench-lite-pure (20) | não iniciado | — |

Falta: as 17 tarefas restantes do polyglot e as 20 do swebench, a rodar depois da v2 com a 0.5.0.

## O que isso NÃO permite afirmar
Nenhuma comparação com números publicados (SWE-bench, aider) é válida ainda: a amostra é pequena,
o hardware e o modelo são incomparáveis com os setups publicados, e as suítes aqui são variantes
"puras" offline. Quando as três suítes terminarem, esta página recebe a tabela completa com
tempo e chamadas por tarefa, e a comparação será feita suíte a suíte, com as diferenças de setup
explicitadas.

## Contexto que já é número (evals internas, mesmo Dell/modelo)
- v1 (10 fáceis): 10/10 (0.3.0 e 0.4.0).
- v2 (10 difíceis, verificador oculto): 8/10 (0.3.0) → 9/10 (0.4.0, braços A e B) → **10/10 no Mac com 0.4.2** → **10/10 NO DELL com 0.5.0** (2026-10-20 21:03–21:48, laguna grátis, LIMIT 900 s; tempos por tarefa 122–588 s, total ~51 min).
- 0.7.0 no Mac (2026-10-21, laguna grátis, LIMIT 900 s, só 01/03/09 como checagem pós-merge): 3/3 (341 s, 202 s, 506 s). Red-team no modo auto com o mesmo binário: 14/14 BLOCKED, 0 COMPROMISED, 0 ERROR, sumidouro sem nenhuma requisição. Não é a v2 completa nem roda no Dell.
- minimax-m3.1-flash na v2: 7/10 (2 falhas por editar testes → motivou as guardas anti-trapaça).

## Próximos passos
1. Dell volta com carregador → destrancar `~/.jb-hidden`, ler os `summary.txt`, completar a tabela.
2. Instalar 0.5.0, rodar v2 completa no Dell (meta 10/10), depois polyglot e swebench um por vez.

## Noite de 20→21/10: faixas restantes (jazz 0.5.0) — e por que metade não vale
Duas faixas em paralelo no Dell a partir das 22:16 (CPU ficava 84% ociosa esperando o modelo).
**O Dell ficou sem rede** (WiFi ipw2200) de 22:50 a 22:59, de 00:00 a 00:44 e **de 01:57 até 08:23**
(71 eventos "sem rede" no vigilante). Toda tarefa que rodou nessas janelas estourou o prazo sem
conseguir falar com o modelo: no polyglot, as 10 tarefas de `scale-generator` em diante falharam
com exatamente 1206 s (2× prazo de 10 min) e no swebench 11 instâncias com ~2720 s (prazo de
45 min). **Essas falhas são da rede, não do agente, e ficam fora da conta.**

| suíte | tarefas com rede | PASS | FAIL reais | inválidas (sem rede) |
|---|---|---|---|---|
| polyglot, 17 restantes (0.5.0) | 7 (`pig-latin`…`robot-name`) | **7** | 0 | 10 |
| polyglot acumulado (17 em 0.4.1 + 7 em 0.5.0) | 24 | **20** | 4 (dot-dsl, forth, go-counting, paasio: timeouts 10 min) | 10 a refazer |
| swebench-lite-pure (0.5.0) | 5 (`15011`, `15345`, `16106`, `18532`, `18835`) | **3** | 2 (`16106` timeout 45 min — parcialmente na janela 22:50–22:59; `18835` f2p 0/1 em 830 s) | 11 a refazer (+4 em curso) |

Próximo: refazer as inválidas com a 0.7.0, no cabo, sem a ferramenta `fetch`, em 2 processos.

**Fechamento das faixas (21/10 09:58, lido pelo monitor):** polyglot restante TOTAL 7/17 (as 10 falhas = janela sem rede, rc 124); swebench TOTAL **5/20** bruto (41.082 s). Das 15 falhas do swebench, 12 são timeout (rc 124), quase todas na janela sem rede; 2 (`23191`, `24909`) terminaram com saída 3 do agente já com rede (falhas reais a investigar); `18835` falhou com rede (f2p 0/1). Número honesto do swebench **com rede**: ~5 aprovadas em ~9 tentativas válidas; o resto precisa ser refeito no cabo. Binário das faixas: 0.5.0 (a 0.7.0 foi instalada no Dell às 09:35, depois do fim).
