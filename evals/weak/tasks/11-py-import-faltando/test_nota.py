import unittest
from nota import limpa


class TestLimpa(unittest.TestCase):
    def test_basico(self):
        self.assertEqual(limpa("a ,  b,c"), ["a", "b", "c"])

    def test_vazio(self):
        self.assertEqual(limpa(""), [""])

    def test_espacos_internos(self):
        self.assertEqual(limpa("  x   y ,z"), ["x y", "z"])


if __name__ == "__main__":
    unittest.main()
