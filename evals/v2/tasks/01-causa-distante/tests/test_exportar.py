import json
import unittest
from decimal import Decimal

from loja.exportar import para_csv, para_json

LINHAS = {
    "Cabos": {"bruto": Decimal("10"), "desconto": Decimal("1.5"), "imposto": Decimal("1.53"),
              "liquido": Decimal("10.03"), "itens": 2},
    "Abas": {"bruto": Decimal("5"), "desconto": Decimal("0"), "imposto": Decimal("0"),
             "liquido": Decimal("5"), "itens": 1},
}


class TestExportar(unittest.TestCase):
    def test_csv_ordenado(self):
        linhas = para_csv(LINHAS).splitlines()
        self.assertEqual(linhas[0], "categoria;itens;bruto;desconto;imposto;liquido")
        self.assertEqual(linhas[1], "Abas;1;5.00;0.00;0.00;5.00")
        self.assertEqual(linhas[2], "Cabos;2;10.00;1.50;1.53;10.03")

    def test_json(self):
        dados = json.loads(para_json(LINHAS))
        self.assertEqual([d["categoria"] for d in dados], ["Abas", "Cabos"])
        self.assertEqual(dados[1]["liquido"], "10.03")
