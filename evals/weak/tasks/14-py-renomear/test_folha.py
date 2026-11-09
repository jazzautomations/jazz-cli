import unittest
from funcionario import Funcionario
from folha import resumo


class TestFolha(unittest.TestCase):
    def test_liquido(self):
        f = Funcionario("operador", "b1")
        self.assertEqual(f.salario_liquido(1000.0), 890.0)

    def test_resumo(self):
        self.assertIn("liquido 890.0", resumo(1000.0))


if __name__ == "__main__":
    unittest.main()
