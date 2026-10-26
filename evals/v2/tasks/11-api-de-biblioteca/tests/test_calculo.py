import unittest
from decimal import Decimal

from carteira.calculo import total


class TestTotal(unittest.TestCase):
    def test_total(self):
        c = {"saldo": Decimal("10.50"), "ativos": [{"qtd": 2, "preco": Decimal("1.25")}]}
        self.assertEqual(total(c), Decimal("13.00"))
