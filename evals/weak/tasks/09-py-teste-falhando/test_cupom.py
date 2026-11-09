import unittest
from cupom import desconto


class TestDesconto(unittest.TestCase):
    def test_basico(self):
        self.assertEqual(desconto(19900, 10), 17910)

    def test_sem_desconto(self):
        self.assertEqual(desconto(19900, 0), 19900)

    def test_metade(self):
        self.assertEqual(desconto(50000, 50), 25000)


if __name__ == "__main__":
    unittest.main()
