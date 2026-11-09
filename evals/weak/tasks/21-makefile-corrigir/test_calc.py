import unittest
from calc import dobro


class TestDobro(unittest.TestCase):
    def test_dobro(self):
        self.assertEqual(dobro(4), 8)
        self.assertEqual(dobro(0), 0)


if __name__ == "__main__":
    unittest.main()
