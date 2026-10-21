import unittest
from datetime import date

from biblioteca.calendario import dias_corridos, eh_feriado, somar_dias_uteis


class TestCalendario(unittest.TestCase):
    def test_semana_simples(self):
        # segunda 05/10/2026 + 2 dias úteis = quarta 07/10
        self.assertEqual(somar_dias_uteis(date(2026, 10, 5), 2), date(2026, 10, 7))

    def test_pula_fim_de_semana(self):
        # quinta 15/10/2026 + 3 dias úteis = sex 16, seg 19, ter 20
        self.assertEqual(somar_dias_uteis(date(2026, 10, 15), 3), date(2026, 10, 20))

    def test_pula_feriado(self):
        # sexta 09/10/2026 + 1 = segunda 12/10 é feriado -> terça 13/10
        self.assertEqual(somar_dias_uteis(date(2026, 10, 9), 1), date(2026, 10, 13))

    def test_feriado(self):
        self.assertTrue(eh_feriado(date(2026, 11, 20)))
        self.assertEqual(dias_corridos(date(2026, 1, 30), date(2026, 2, 2)), 3)
