import unittest

from tests.util import arquivo_temp, rodar


class TestContar(unittest.TestCase):
    def test_contar(self):
        self.assertEqual(rodar("contar", arquivo_temp("a b\n"))[1], "1 2 4\n")
