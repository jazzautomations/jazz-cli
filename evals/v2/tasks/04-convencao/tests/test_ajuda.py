import unittest

from tests.util import rodar


class TestAjuda(unittest.TestCase):
    def test_lista_comandos(self):
        rc, out, _ = rodar("ajuda")
        self.assertEqual(rc, 0)
        nomes = [l.split()[0] for l in out.splitlines()]
        self.assertEqual(nomes[:4], ["maiusculas", "unicas", "hash", "frequencia"])
        self.assertIn("sha256", out)
