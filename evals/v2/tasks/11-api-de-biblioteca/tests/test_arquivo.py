import tempfile
import unittest
from decimal import Decimal
from pathlib import Path

from carteira.arquivo import carregar, salvar


class TestArquivo(unittest.TestCase):
    def test_carregar_preserva_decimais_nos_niveis_aninhados(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "carteira.json"
            path.write_text('{"saldo":10.50,"ativos":[{"qtd":2,"preco":1.25}]}', encoding="utf-8")
            carteira = carregar(path)
        self.assertEqual(carteira["saldo"], Decimal("10.50"))
        self.assertEqual(carteira["ativos"][0]["preco"], Decimal("1.25"))
        self.assertIs(type(carteira["ativos"][0]["qtd"]), int)

    def test_salvar_usa_fastjson_com_indentacao_ordem_e_escala(self):
        carteira = {"saldo": Decimal("10.50"), "ativos": [{"qtd": 2, "preco": Decimal("1.25")}]}
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "carteira.json"
            salvar(carteira, path)
            got = path.read_bytes()
        self.assertEqual(got, b'{\n  "ativos": [\n    {\n      "preco": 1.25,\n      "qtd": 2\n    }\n  ],\n  "saldo": 10.50\n}\n')


if __name__ == "__main__":
    unittest.main()
