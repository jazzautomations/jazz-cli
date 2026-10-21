"""Regras de cálculo de um pedido."""
from .precos import para_centavos

FRETE_GRATIS_A_PARTIR = 30000  # centavos
FRETE_PADRAO = 1990
DESCONTO_ATACADO_QTD = 10
DESCONTO_ATACADO_PCT = 5


class PedidoRejeitado(Exception):
    pass


def item_total(item) -> int:
    qtd = int(item["qtd"])
    if qtd <= 0:
        raise PedidoRejeitado(f"quantidade inválida no item {item.get('sku')}")
    bruto = para_centavos(item["preco"]) * qtd
    if qtd >= DESCONTO_ATACADO_QTD:
        bruto -= bruto * DESCONTO_ATACADO_PCT // 100
    return bruto


def processar(pedido) -> dict:
    if pedido.get("bloqueado"):
        raise PedidoRejeitado("cliente bloqueado")
    if not pedido.get("itens"):
        raise PedidoRejeitado("pedido sem itens")
    subtotal = sum(item_total(i) for i in pedido["itens"])
    frete = 0 if subtotal >= FRETE_GRATIS_A_PARTIR or pedido.get("retirada") else FRETE_PADRAO
    return {"id": pedido["id"], "subtotal": subtotal, "frete": frete, "total": subtotal + frete}
