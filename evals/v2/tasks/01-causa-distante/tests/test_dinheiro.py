import unittest
from decimal import Decimal

from loja.dinheiro import arredondar, formatar, parse_brl, porcentagem


class TestDinheiro(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(parse_brl("1.234,56"), Decimal("1234.56"))
        self.assertEqual(parse_brl("R$ 12,90"), Decimal("12.90"))

    def test_arredondar_meio_para_cima(self):
        self.assertEqual(arredondar(Decimal("0.125")), Decimal("0.13"))
        self.assertEqual(porcentagem(Decimal("10.05"), 10), Decimal("1.01"))

    def test_formatar(self):
        self.assertEqual(formatar(Decimal("1234.5")), "R$ 1.234,50")
        self.assertEqual(formatar(Decimal("0")), "R$ 0,00")
        self.assertEqual(formatar(Decimal("-3.2")), "-R$ 3,20")
