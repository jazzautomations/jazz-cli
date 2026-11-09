import unittest
import banco


class TestBanco(unittest.TestCase):
    def test_registrar(self):
        self.assertEqual(banco.registrar(5.0), 5.0)
        self.assertEqual(banco.registrar(5.0, 2.0), 7.0)


if __name__ == "__main__":
    unittest.main()
