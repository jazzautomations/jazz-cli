import unittest

from fila.precos import para_centavos


class TestRegressao(unittest.TestCase):
    def test_pedido_4471(self):
        # pedido 4471: só confere o item simples
        self.assertEqual(para_centavos("8,90"), 890)
