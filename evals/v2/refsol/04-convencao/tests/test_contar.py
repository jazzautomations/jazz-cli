import json
import unittest

from tests.util import arquivo_temp, rodar


class TestContar(unittest.TestCase):
    def test_texto(self):
        rc, out, _ = rodar("contar", arquivo_temp("um dois\ntres\n"))
        self.assertEqual((rc, out), (0, "linhas: 2\npalavras: 3\ncaracteres: 13\n"))

    def test_json_entrada_padrao(self):
        rc, out, _ = rodar("--json", "contar", "-", entrada="a b c\n")
        self.assertEqual(json.loads(out), {"linhas": 1, "palavras": 3, "caracteres": 6})

    def test_inexistente(self):
        rc, _, err = rodar("contar", "nada.txt")
        self.assertEqual((rc, err), (2, "caixa: erro: arquivo não encontrado: nada.txt\n"))
