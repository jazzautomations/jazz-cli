import unittest
from datetime import date, time

from ponto.calculo import minutos_por_dia, total_minutos
from ponto.leitura import ler_linha
from ponto.modelo import Registro


class TestCalculo(unittest.TestCase):
    def test_madrugada_pertence_ao_dia_da_entrada(self):
        r = ler_linha("2026-10-10 22:00 02:00")
        self.assertEqual((r.dia, r.minutos()), (date(2026, 10, 10), 240))

    def test_minutos_por_dia(self):
        regs = [Registro(date(2026, 10, 5), time(8, 0), time(12, 0)),
                Registro(date(2026, 10, 5), time(13, 0), time(18, 30)),
                Registro(date(2026, 10, 6), time(8, 10), time(17, 0))]
        self.assertEqual(minutos_por_dia(regs), {date(2026, 10, 5): 570, date(2026, 10, 6): 530})
        self.assertEqual(total_minutos(regs), 1100)
