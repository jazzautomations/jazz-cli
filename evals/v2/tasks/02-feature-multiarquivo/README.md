# notas

Bloco de notas de linha de comando. As notas ficam em `notas.json` (ou no arquivo de `--arquivo`).

    python3 -m notas add "comprar café" --tag casa
    python3 -m notas list
    python3 -m notas list --tag casa
    python3 -m notas feito 2
    python3 -m notas rm 3

Testes: `python3 -m unittest`
