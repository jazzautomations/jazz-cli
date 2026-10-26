#!/bin/sh
set -eu
cat > carteira/arquivo.py <<'PY'
"""Leitura e gravação da carteira em JSON (com a lib fastjson do projeto)."""

import fastjson


def carregar(caminho):
    """Lê a carteira de `caminho`; valores fracionários como Decimal."""
    return fastjson.read(caminho, decimal=True)


def salvar(carteira, caminho):
    """Grava `carteira` em `caminho`: 2 espaços de indentação, chaves em ordem."""
    fastjson.write(caminho, carteira, indent=2, sort=True)
PY
cat > tests/test_arquivo.py <<'PY'
import tempfile
import unittest
from decimal import Decimal
from pathlib import Path

from carteira.arquivo import carregar, salvar


class TestArquivo(unittest.TestCase):
    def test_carregar_preserva_decimais(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "carteira.json"
            path.write_text('{"saldo":10.50,"ativos":[{"qtd":2,"preco":1.25}]}', encoding="utf-8")
            got = carregar(path)
        self.assertEqual(got["saldo"], Decimal("10.50"))
        self.assertEqual(got["ativos"][0]["preco"], Decimal("1.25"))
        self.assertIs(type(got["ativos"][0]["qtd"]), int)

    def test_salvar_preserva_formato_e_escala(self):
        got = {"saldo": Decimal("10.50"), "ativos": [{"qtd": 2, "preco": Decimal("1.25")}]}
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "carteira.json"
            salvar(got, path)
            data = path.read_bytes()
        self.assertEqual(data, b'{\n  "ativos": [\n    {\n      "preco": 1.25,\n      "qtd": 2\n    }\n  ],\n  "saldo": 10.50\n}\n')


if __name__ == "__main__":
    unittest.main()
PY
