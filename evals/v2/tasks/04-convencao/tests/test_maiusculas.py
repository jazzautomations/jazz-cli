import unittest

from tests.util import arquivo_temp, rodar


class TestMaiusculas(unittest.TestCase):
    def test_arquivo(self):
        rc, out, _ = rodar("maiusculas", arquivo_temp("olá\nmundo\n"))
        self.assertEqual((rc, out), (0, "OLÁ\nMUNDO\n"))

    def test_entrada_padrao(self):
        self.assertEqual(rodar("maiusculas", "-", entrada="abc")[1], "ABC\n")

    def test_inexistente(self):
        rc, out, err = rodar("maiusculas", "/nao/existe.txt")
        self.assertEqual(rc, 2)
        self.assertEqual(err, "caixa: erro: arquivo não encontrado: /nao/existe.txt\n")
