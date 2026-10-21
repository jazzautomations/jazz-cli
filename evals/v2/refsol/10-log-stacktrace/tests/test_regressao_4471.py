"""Regressão: o pedido 4471 (preço '1.234,56', com separador de milhar) derrubou o worker em 18/10/2026."""
import unittest

from fila.precos import para_centavos
from fila.processador import processar

PEDIDO_4471 = {"id": 4471, "cliente": "Padaria Pão Quente", "itens": [
    {"sku": "FAR-25", "qtd": 3, "preco": "1.234,56"}, {"sku": "FER-01", "qtd": 12, "preco": "8,90"}],
    "retirada": False}


class TestRegressao4471(unittest.TestCase):
    def test_separador_de_milhar(self):
        self.assertEqual(para_centavos("1.234,56"), 123456)
        self.assertEqual(para_centavos("R$ 1.000.000,00"), 100000000)

    def test_pedido_4471(self):
        self.assertEqual(processar(PEDIDO_4471), {"id": 4471, "subtotal": 380514, "frete": 0, "total": 380514})
