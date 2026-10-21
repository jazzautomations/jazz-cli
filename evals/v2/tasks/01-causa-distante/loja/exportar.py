"""Exporta o relatório consolidado em CSV ou JSON (valores com ponto decimal)."""
import csv
import io
import json

from .relatorio import CAMPOS


def _linhas_ordenadas(linhas: dict):
    for cat in sorted(linhas):
        l = linhas[cat]
        yield cat, l["itens"], [l[c] for c in CAMPOS]


def para_csv(linhas: dict) -> str:
    buf = io.StringIO()
    w = csv.writer(buf, delimiter=";", lineterminator="\n")
    w.writerow(["categoria", "itens", *CAMPOS])
    for cat, itens, valores in _linhas_ordenadas(linhas):
        w.writerow([cat, itens, *(f"{v:.2f}" for v in valores)])
    return buf.getvalue()


def para_json(linhas: dict) -> str:
    dados = [
        {"categoria": cat, "itens": itens, **{c: f"{v:.2f}" for c, v in zip(CAMPOS, valores)}}
        for cat, itens, valores in _linhas_ordenadas(linhas)
    ]
    return json.dumps(dados, ensure_ascii=False, indent=2)
