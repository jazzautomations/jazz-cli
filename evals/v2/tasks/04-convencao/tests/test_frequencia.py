import json
import unittest

from tests.util import arquivo_temp, rodar


class TestFrequencia(unittest.TestCase):
    def test_top(self):
        rc, out, _ = rodar("--json", "frequencia", "--top", "2", arquivo_temp("a b a c b a"))
        self.assertEqual((rc, json.loads(out)), (0, {"a": 3, "b": 2}))

    def test_top_invalido(self):
        rc, _, err = rodar("frequencia", "--top", "0", "-")
        self.assertEqual(rc, 2)
        self.assertEqual(err, "caixa: erro: --top precisa ser pelo menos 1\n")
