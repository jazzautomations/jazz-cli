from decimal import Decimal


def total(carteira):
    """Valor total dos ativos (qtd x preço) mais o saldo."""
    t = Decimal(carteira.get("saldo", 0))
    for a in carteira.get("ativos", []):
        t += Decimal(a["qtd"]) * Decimal(a["preco"])
    return t
