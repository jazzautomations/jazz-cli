# calib-v2 — relatório recuperado do transcript

## Corpus final (relatório sobrevivente)

- 5.444 amostras, 3.915 hits, 55 negativos censurados
- miss/error rate 27,4% (corpus realista, não fácil)
- seed 42, construção 5-fold, zero prompts de avaliação no treino
- fontes: examples 1.035, splice 1.011, filler 1.005, synonym 850,
  drop 743, typo 686, negative 111, gate 3
- partições: 60% fit / 20% method-select / 20% teste intocado, por grupo

(Ativo anterior: 901 amostras, 854 hits, ECE raw 0.047 → cal 0.046,
Brier raw 0.058 → cal 0.055.)

## Resultado held-out por banda de risco

- banda read-only: 510 acts, 93 errados (18,2% erro), 1 ask, 194 misses
- banda mutação/interpretação: 98 acts, 9 errados (9,2% erro), 137 asks, 80 misses

## Decisão de aceitação: permaneceu OPT-IN

ECE melhorou e vetos/negativos seguraram, mas os gates de avaliação falharam:

- falso-sucesso: 0 → **1** (`dell-readme`, borda 301ms/300ms)
- perguntas: 6/6 → 8/6 (`delete-file` ganhou pergunta desnecessária)
- tasks: 111/111 → 108/111

Mecanismo da falha: `rule_only` raw 0.90 calibrou para 0.78, cruzando
abaixo do threshold de mutação 0.85 — `impossible-deploy` virou miss em vez
de bloqueado. Recomendação do relatório: manter opt-in até haver misses
reais suficientes para distinguir scores rule_only bons e ruins.

## Custos Chow (relatório)

pergunta=3, erro read-only=10 → 0.70; erro mutação=20 → 0.85; floor 0.60.
