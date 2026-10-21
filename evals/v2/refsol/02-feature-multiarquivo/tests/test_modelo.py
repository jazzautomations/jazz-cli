import unittest

from notas.modelo import Nota, NotaInvalida


class TestNota(unittest.TestCase):
    def test_normaliza_tags(self):
        n = Nota(id=1, texto="  oi ", tags=["Casa", "casa", " URGENTE", ""])
        self.assertEqual(n.texto, "oi")
        self.assertEqual(n.tags, ["casa", "urgente"])

    def test_texto_vazio(self):
        with self.assertRaises(NotaInvalida):
            Nota(id=1, texto="   ")

    def test_tem_tag(self):
        self.assertTrue(Nota(id=1, texto="x", tags=["casa"]).tem_tag(" CASA"))
