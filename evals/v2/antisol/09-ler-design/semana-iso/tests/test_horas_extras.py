import unittest
from datetime import date, time

from ponto.calculo import horas_extras
from ponto.modelo import Registro


def r(dia, e, s):
    return Registro(date.fromisoformat(dia), time.fromisoformat(e), time.fromisoformat(s))


class TestHorasExtras(unittest.TestCase):
    def test_semana_de_quarta_a_terca(self):
        regs = [r(f"2026-10-{d:02d}", "08:00", "16:30") for d in (7, 8, 9, 12, 13)]  # 5 x 510
        self.assertEqual(horas_extras(regs, date(2026, 10, 10)), 150)
        self.assertEqual(horas_extras(regs, date(2026, 10, 6)), 0)

    def test_domingo_em_dobro_e_tolerancia(self):
        self.assertEqual(horas_extras([r("2026-10-11", "09:00", "09:30")], date(2026, 10, 11)), 60)
        self.assertEqual(horas_extras([r("2026-10-11", "09:00", "09:05")], date(2026, 10, 11)), 0)
