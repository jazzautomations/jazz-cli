import unittest
from decimal import Decimal

from loja.descontos import descontos_do_pedido
from loja.modelos import ItemPedido, Pedido

CUPONS = {
    "P10": {"tipo": "percentual", "valor": 10, "categorias": ["Ferragens"]},
    "F15": {"tipo": "fixo", "valor": "15,00", "categorias": ["Cabos", "Livros"]},
}


def pedido(cupom, *itens):
    p = Pedido(numero="1", cliente="x", uf="SP", cupom=cupom)
    for cat, preco, q in itens:
        p.itens.append(ItemPedido(codigo="X", quantidade=q, preco_unitario=Decimal(preco), categoria=cat))
    return p


class TestDescontos(unittest.TestCase):
    def test_percentual(self):
        d = descontos_do_pedido(pedido("P10", ("Ferragens", "12.90", 3), ("Cabos", "10", 1)), CUPONS)
        self.assertEqual(d, {"Ferragens": Decimal("3.87")})

    def test_fixo_maior_categoria(self):
        d = descontos_do_pedido(pedido("F15", ("Cabos", "10", 1), ("Livros", "40", 1)), CUPONS)
        self.assertEqual(d, {"Livros": Decimal("15.00")})

    def test_fixo_nao_negativo(self):
        d = descontos_do_pedido(pedido("F15", ("Cabos", "9.90", 1)), CUPONS)
        self.assertEqual(d, {"Cabos": Decimal("9.90")})

    def test_sem_cupom(self):
        self.assertEqual(descontos_do_pedido(pedido("", ("Cabos", "10", 1)), CUPONS), {})
