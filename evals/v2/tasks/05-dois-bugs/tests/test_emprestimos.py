import unittest
from datetime import date
from decimal import Decimal

from biblioteca.emprestimos import Emprestimo


class TestEmprestimos(unittest.TestCase):
    def test_prazo_revista(self):
        e = Emprestimo("9788535902778", "ana", "revista", date(2026, 10, 1))  # quinta
        self.assertEqual(e.prazo(), date(2026, 10, 6))  # sex 2, seg 5, ter 6

    def test_multa(self):
        e = Emprestimo("9788535902778", "ana", "revista", date(2026, 10, 1))
        self.assertEqual(e.multa(date(2026, 10, 6)), Decimal("0.00"))
        self.assertEqual(e.multa(date(2026, 10, 9)), Decimal("1.50"))
        self.assertEqual(e.multa(date(2026, 12, 31)), Decimal("10.00"))

    def test_tipo_invalido(self):
        with self.assertRaises(ValueError):
            Emprestimo("x", "y", "vinil", date(2026, 1, 5)).prazo()
