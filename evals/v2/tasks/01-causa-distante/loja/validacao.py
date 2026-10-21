"""Validações dos dados de entrada. Problemas viram avisos; nada é corrigido aqui."""
from decimal import Decimal

from .impostos import ALIQUOTAS

UFS = {
    "AC", "AL", "AP", "AM", "BA", "CE", "DF", "ES", "GO", "MA", "MT", "MS", "MG", "PA",
    "PB", "PR", "PE", "PI", "RJ", "RN", "RS", "RO", "RR", "SC", "SP", "SE", "TO",
}


def validar_catalogo(catalogo: dict) -> list:
    avisos = []
    for codigo, produto in sorted(catalogo.items()):
        if produto.preco <= Decimal("0"):
            avisos.append(f"catálogo: {codigo} com preço não positivo ({produto.preco})")
        if not produto.categoria:
            avisos.append(f"catálogo: {codigo} sem categoria")
    return avisos


def validar_pedidos(pedidos: list, cupons: dict) -> list:
    avisos = []
    vistos = set()
    for pedido in pedidos:
        if pedido.numero in vistos:
            avisos.append(f"pedido {pedido.numero} repetido")
        vistos.add(pedido.numero)
        if pedido.uf not in UFS:
            avisos.append(f"pedido {pedido.numero}: UF desconhecida {pedido.uf!r}")
        elif pedido.uf not in ALIQUOTAS:
            avisos.append(f"pedido {pedido.numero}: UF {pedido.uf} usa a alíquota padrão")
        if pedido.cupom and pedido.cupom not in cupons:
            avisos.append(f"pedido {pedido.numero}: cupom {pedido.cupom!r} não existe")
        if not pedido.itens:
            avisos.append(f"pedido {pedido.numero} sem itens")
    return avisos
