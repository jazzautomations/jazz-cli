import os
import sys
import unittest
from datetime import date, timedelta

sys.path.insert(0, os.getcwd())
from biblioteca import isbn  # noqa: E402
from biblioteca.calendario import eh_dia_util, somar_dias_uteis  # noqa: E402
from biblioteca.emprestimos import Emprestimo  # noqa: E402


class Oculto(unittest.TestCase):
    def test_fim_de_semana_nao_e_util(self):
        self.assertFalse(eh_dia_util(date(2026, 10, 17)))  # sábado
        self.assertFalse(eh_dia_util(date(2026, 10, 18)))  # domingo
        self.assertTrue(eh_dia_util(date(2026, 10, 16)))
        self.assertEqual(somar_dias_uteis(date(2026, 10, 17), 1), date(2026, 10, 19))
        self.assertEqual(somar_dias_uteis(date(2026, 10, 30), 10), date(2026, 11, 16))

    def test_dias_uteis_em_massa(self):
        d = date(2026, 3, 2)
        for n in range(0, 40):
            r = somar_dias_uteis(d, n)
            self.assertLess(r.weekday(), 5) if n else None
            uteis = sum(1 for k in range(1, (r - d).days + 1)
                        if eh_dia_util(d + timedelta(days=k)))
            self.assertEqual(uteis, n)

    def test_prazo_livro(self):
        e = Emprestimo("9788535902778", "x", "livro", date(2026, 12, 18))  # sexta
        self.assertEqual(e.prazo(), date(2027, 1, 5))  # 25/12 e 01/01 são feriados

    def test_digito_zero(self):
        self.assertEqual(isbn.digito_verificador("978100000003"), 0)
        self.assertEqual(isbn.digito_verificador("978030640615"), 7)
        for i in range(100000000, 100000400):
            self.assertIn(isbn.digito_verificador("978" + str(i)), range(10))
        self.assertTrue(isbn.valido("9781000000030"))
        self.assertFalse(isbn.valido("9781000000031"))
        self.assertEqual(isbn.formatar("9788582170410"), "978-85-821-7041-0")


if __name__ == "__main__":
    unittest.main()
