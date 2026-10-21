import unittest

from tests.util import arquivo_temp, rodar


class TestUnicas(unittest.TestCase):
    def test_remove_repetidas(self):
        rc, out, _ = rodar("unicas", arquivo_temp("a\nb\na\nc\nb\n"))
        self.assertEqual((rc, out), (0, "a\nb\nc\n"))

    def test_ignorar_caixa(self):
        self.assertEqual(rodar("unicas", "--ignorar-caixa", "-", entrada="Abc\nabc\nX\n")[1], "Abc\nX\n")

    def test_json(self):
        self.assertEqual(rodar("--json", "unicas", "-", entrada="x\nx\ny\n")[1], '["x", "y"]\n')
