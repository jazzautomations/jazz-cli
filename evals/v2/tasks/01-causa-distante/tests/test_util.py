import unittest

from loja.util import chave_categoria, normalizar_codigo, parse_quantidade


class TestUtil(unittest.TestCase):
    def test_normalizar_codigo(self):
        self.assertEqual(normalizar_codigo(" sku-ab12 "), "AB12")
        self.assertEqual(normalizar_codigo("CB2"), "CB2")
        self.assertEqual(normalizar_codigo("SKU-L01"), "L01")

    def test_chave_categoria(self):
        self.assertEqual(chave_categoria(" Ferramentas "), "ferramentas")
        self.assertEqual(chave_categoria("Eletrônicos"), "eletronicos")

    def test_quantidade(self):
        self.assertEqual(parse_quantidade("3"), 3)
        with self.assertRaises(ValueError):
            parse_quantidade("0")
