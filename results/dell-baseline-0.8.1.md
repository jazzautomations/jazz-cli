# Linha de base 0.8.1 no Dell D610 (26/10/2026)

Máquina: Pentium M 1.73 GHz (1 núcleo), 2 GB, antiX kernel 5.10.240-antix.1-486-smp, linux/386, HDD.
Binário: release assinado 0.8.1, 13.131.900 bytes. Repositório de teste: ~/jazz-cli-src (868 arquivos, sem .git).
Relógio: `date +%s%N` em volta do comando (GNU time não existe no antiX). Caches quentes (sem sudo para drop_caches).

| medida | valor |
|---|---|
| `jazz version` quente | 83 / 83 / 151 ms |
| `jazz version` pico RSS | 15,0 MB |
| init do pacote main (GODEBUG=inittrace) | 129 ms relógio, 5,84 MB, 58.391 alocações |
| ciclos de GC antes de imprimir `version` | 2 |
| `jazz script` (lista, sem modelo) | 100 / 118 / 124 ms |
| `jazz -p "abre o README.md"` | 21,5 / 14,3 / 15,3 s — **foi para o modelo** (fast path não casou): 2 requisições, 32,3k tokens de entrada (README inteiro, 65 KB, enviado), RSS 20,4 MB |
| `jazz -p "git status"` | 36,1 / 34,6 s — foi para o modelo e depois o comando foi bloqueado (pasta não confiável, -p sem confirmação) |

Conclusão: na 0.8.1 o "modo offline" só cobre os atalhos exatos de `intents.go`; frases naturais simples ("abre o
README.md", "git status") viram chamadas de modelo de 15–36 s e dezenas de milhares de tokens. Meta da 0.9 (spec
`docs/design/offline-brain.md`): essas duas frases resolvidas sem modelo em < 300 ms e 0 tokens; `version` < 40 ms
com 0 GC (regex preguiçoso, GC cedo); `init main` < 1 MB.
