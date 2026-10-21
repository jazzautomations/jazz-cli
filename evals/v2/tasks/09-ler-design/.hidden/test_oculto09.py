import os
import sys
import unittest
from datetime import date, time

sys.path.insert(0, os.getcwd())
from ponto.calculo import horas_extras  # noqa: E402
from ponto.modelo import Registro  # noqa: E402


def R(d, e, s):
    y, m, dd = map(int, d.split("-"))
    he, me = map(int, e.split(":"))
    hs, ms = map(int, s.split(":"))
    return Registro(date(y, m, dd), time(he, me), time(hs, ms))


SEMANA = [
    R("2026-10-05", "08:00", "18:00"),  # segunda (semana anterior: qua 30/09 a ter 06/10)
    R("2026-10-07", "08:00", "18:00"),  # quarta: 600
    R("2026-10-08", "08:00", "18:00"),  # quinta: 600
    R("2026-10-09", "07:00", "12:00"),  # sexta: 300 + 300
    R("2026-10-09", "13:00", "18:00"),
    R("2026-10-10", "09:00", "17:20"),  # sábado: 500
    R("2026-10-11", "10:00", "11:00"),  # domingo: 60 (x2)
    R("2026-10-12", "14:00", "17:20"),  # segunda: 200
]


class Oculto(unittest.TestCase):
    def test_semana_quarta_a_terca(self):
        for ref in ("2026-10-07", "2026-10-09", "2026-10-11", "2026-10-13"):
            with self.subTest(ref=ref):
                self.assertEqual(horas_extras(SEMANA, date.fromisoformat(ref)), 220)

    def test_semana_anterior(self):
        self.assertEqual(horas_extras(SEMANA, date(2026, 10, 6)), 0)
        self.assertEqual(horas_extras(SEMANA, date(2026, 9, 30)), 0)

    def test_tipo_int(self):
        self.assertIs(type(horas_extras(SEMANA, date(2026, 10, 9))), int)

    def test_tolerancia(self):
        base = [R(f"2026-10-{d:02d}", "08:00", "16:00") for d in (14, 15, 16, 19, 20)]  # 5 x 480 = 2400
        self.assertEqual(horas_extras(base, date(2026, 10, 14)), 0)
        self.assertEqual(horas_extras(base + [R("2026-10-17", "08:00", "08:10")], date(2026, 10, 14)), 0)
        self.assertEqual(horas_extras(base + [R("2026-10-17", "08:00", "08:11")], date(2026, 10, 14)), 11)
        self.assertEqual(horas_extras(base + [R("2026-10-18", "08:00", "08:05")], date(2026, 10, 14)), 0)
        self.assertEqual(horas_extras(base + [R("2026-10-18", "08:00", "08:06")], date(2026, 10, 14)), 12)

    def test_domingo_nao_entra_na_jornada(self):
        regs = [R("2026-10-18", "08:00", "18:00"), R("2026-10-14", "08:00", "18:00")]
        self.assertEqual(horas_extras(regs, date(2026, 10, 20)), 1200)
        base = [R(f"2026-10-{d:02d}", "08:00", "16:00") for d in (14, 15, 16, 19, 20)]  # 2400
        self.assertEqual(horas_extras(base + [R("2026-10-18", "09:00", "10:00")], date(2026, 10, 16)), 120)

    def test_madrugada_fica_no_dia_da_entrada(self):
        base = [R(f"2026-10-{d:02d}", "08:00", "16:00") for d in (7, 8, 9, 12)]  # 1920
        regs = base + [R("2026-10-13", "22:00", "06:30")]  # terça 510 -> 2430
        self.assertEqual(horas_extras(regs, date(2026, 10, 13)), 30)
        self.assertEqual(horas_extras(regs, date(2026, 10, 14)), 0)

    def test_vazio(self):
        self.assertEqual(horas_extras([], date(2026, 10, 7)), 0)


if __name__ == "__main__":
    unittest.main()
