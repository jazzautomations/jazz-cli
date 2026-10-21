"""ICMS simplificado por UF de destino. Livros são imunes."""
from decimal import Decimal

from .dinheiro import ZERO, porcentagem
from .util import chave_categoria

ALIQUOTAS = {
    "SP": Decimal("18"),
    "RJ": Decimal("20"),
    "MG": Decimal("18"),
    "PR": Decimal("19.5"),
    "BA": Decimal("20.5"),
}
ALIQUOTA_PADRAO = Decimal("17")
IMUNES = {"livros"}


def aliquota(uf: str) -> Decimal:
    return ALIQUOTAS.get(uf.upper(), ALIQUOTA_PADRAO)


def imposto(base, uf: str, categoria: str):
    if chave_categoria(categoria) in IMUNES or base <= 0:
        return ZERO
    return porcentagem(base, aliquota(uf))
