"""Modelos de dados da loja."""
from dataclasses import dataclass, field
from decimal import Decimal


@dataclass(frozen=True)
class Produto:
    codigo: str
    nome: str
    categoria: str
    preco: Decimal


@dataclass
class ItemPedido:
    codigo: str
    quantidade: int
    preco_unitario: Decimal = Decimal("0")
    categoria: str = ""

    @property
    def subtotal(self) -> Decimal:
        return self.preco_unitario * self.quantidade


@dataclass
class Pedido:
    numero: str
    cliente: str
    uf: str
    cupom: str = ""
    itens: list = field(default_factory=list)

    def categorias(self) -> set:
        return {i.categoria for i in self.itens}

    def bruto_por_categoria(self) -> dict:
        tot = {}
        for item in self.itens:
            tot[item.categoria] = tot.get(item.categoria, Decimal("0")) + item.subtotal
        return tot
