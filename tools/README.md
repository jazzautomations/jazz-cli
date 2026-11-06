# tools/ — a esteira offline↔online

O loop completo que ensina o cérebro offline com o professor online.
Tudo medido, nada anedótico: cada pedra na prateleira tem prova.

```
corpus de pedidos → harvest.py (professor online verifica)
→ plan-cache (wins/testemunhas) → jazz distill (portão)
→ methods.overlay.json (métodos aprendidos) → runner.py mede cobertura
```

## Layout do workdir (`JAZZ_WORK`, default `/tmp/v5-work`)

```
corpus/corpus-2k5.jsonl   2684 pedidos (id, req, cat)
fixture/                  diretório de trabalho dos runs (confiável)
jh-harvest/               JAZZ_HOME compartilhado da colheita
                          (auth.json + config.json + trusted.json + plans + overlay)
jh/<id>/                  JAZZ_HOME isolado por linha do benchmark
results.*.jsonl           resultado de cada medição
harvest.*.jsonl           log resumível da colheita (por id)
```

## corpus-bench/runner.py — medir cobertura

Roda cada pedido do corpus contra o cérebro offline real e grava
`intent`/`status` por linha. Cada linha ganha um `JAZZ_HOME` isolado
semeado com `data/forge/methods.overlay.json` + `trusted.json` do
`BENCH_SEED` — é assim que os `learned_*` respondem no benchmark.

```sh
JAZZ_WORK=/tmp/v5-work BENCH_OUT=/tmp/v5-work/results.v15.jsonl \
    python3 tools/corpus-bench/runner.py
# resume sozinho por id — rerodar continua de onde parou
```

compare.py dif dua medições (VERDICT por id em `verdicts.py`);
analyze.py quebra por categoria/intent/motivo.

## harvest/harvest.py — colher planos

Roda linhas recusadas/falhas do corpus ONLINE (modelo configurável)
num `JAZZ_HOME` compartilhado: planos verificados acumulam wins e
testemunhas — o mesmo shape em textos diferentes funde numa família
destilável. Filtra placeholders e pedidos fora-do-root pra não gastar
chamada em negação garantida.

```sh
HARVEST_LIMIT=80 HARVEST_REPEAT=2 HARVEST_PAR=3 \
HARVEST_MODEL='anthropic/claude-haiku-5-5' \
HARVEST_RESULTS=/tmp/v5-work/results.rest.jsonl \
HARVEST_OUT=/tmp/v5-work/harvest.batch8.jsonl \
    python3 tools/harvest/harvest.py
# depois: JAZZ_HOME=<jh> jazz distill   (repete até a fila esvaziar)
```

`JAZZ_PC_MIN_STEPS=1` deixa procedimentos curtos contarem; o portão de
destilação só promove plano verificado ≥2 vezes, e cada veredito
carimba o plano (o mesmo veredito na mesma evidência não reabre).

## O que NÃO entra aqui

- `corpus-2k5.jsonl` e `fixture/` são dados derivados (nl2bash + escritos
  à mão) — a receita vive em `build_corpus.py`; promova quando for hora
  de selar a versão pública do benchmark.
- Credenciais: `jh-harvest/auth.json` é local e nunca se comita.
