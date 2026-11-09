import unittest
import versao


class TestVersao(unittest.TestCase):
    def test_formato(self):
        partes = versao.VERSAO.split(".")
        self.assertEqual(len(partes), 3)
        self.assertTrue(all(p.isdigit() for p in partes))


if __name__ == "__main__":
    unittest.main()
