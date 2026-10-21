import unittest

import pedidos


class TestPedidos(unittest.TestCase):
    def test_todos_os_tipos(self):
        for tipo, cls in pedidos.TIPOS.items():
            p = pedidos.criar(tipo, "Ana")
            p.adicionar_item("A1", 10.0, 12)
            p.adicionar_item("A1", 10.0, 10)
            self.assertEqual(p.quantidade_total(), 22, tipo)
            self.assertEqual(cls.de_dict(p.para_dict()).para_json(), p.para_json())

    def test_varejo(self):
        p = pedidos.PedidoVarejo("Bia")
        p.adicionar_item("X", 100.0, 1)
        self.assertEqual(p.aplicar_desconto(80), 50.0)
        self.assertEqual(p.total(), 62.9)
        with self.assertRaises(ValueError):
            p.adicionar_item("Y", 1.0, 0)

    def test_tipo_desconhecido(self):
        with self.assertRaises(ValueError):
            pedidos.criar("nada")


if __name__ == "__main__":
    unittest.main()
