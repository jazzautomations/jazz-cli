import unittest

from fila.precos import formatar, para_centavos
from fila.processador import PedidoRejeitado, processar


class TestPrecos(unittest.TestCase):
    def test_texto_e_numero(self):
        self.assertEqual(para_centavos("12,90"), 1290)
        self.assertEqual(para_centavos("R$ 7,00"), 700)
        self.assertEqual(para_centavos(12.9), 1290)
        self.assertEqual(para_centavos(3), 300)

    def test_formatar(self):
        self.assertEqual(formatar(123456), "R$ 1.234,56")
        self.assertEqual(formatar(5), "R$ 0,05")


class TestProcessador(unittest.TestCase):
    def test_frete(self):
        r = processar({"id": 1, "itens": [{"sku": "A", "qtd": 2, "preco": "10,00"}]})
        self.assertEqual(r, {"id": 1, "subtotal": 2000, "frete": 1990, "total": 3990})

    def test_atacado_e_frete_gratis(self):
        r = processar({"id": 2, "itens": [{"sku": "B", "qtd": 10, "preco": "35,00"}]})
        self.assertEqual(r["subtotal"], 33250)
        self.assertEqual(r["frete"], 0)

    def test_rejeitado(self):
        with self.assertRaises(PedidoRejeitado):
            processar({"id": 3, "itens": [], "bloqueado": False})
