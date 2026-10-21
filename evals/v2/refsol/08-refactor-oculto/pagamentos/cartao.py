"""Autorização de pagamento com cartão (parcelado)."""
from decimal import ROUND_HALF_UP, Decimal

from . import comum
from .comum import formatar_cpf, normalizar_documento, validar_cpf

VALOR_MINIMO = 1.00
VALOR_MAXIMO = 20000.00
MAX_PARCELAS = 12
JUROS_MES = Decimal("0.0199")  # a partir da 4ª parcela


def autorizar(valor: float, cpf: str, parcelas: int = 1) -> dict:
    valor = comum.validar_valor(valor, VALOR_MINIMO, VALOR_MAXIMO, "cartão")
    titular = formatar_cpf(cpf)
    if not 1 <= parcelas <= MAX_PARCELAS:
        raise ValueError(f"parcelas deve estar entre 1 e {MAX_PARCELAS}")
    total = Decimal(str(valor))
    if parcelas > 3:
        total *= (1 + JUROS_MES) ** parcelas
    parcela = total / parcelas
    return {
        "valor": comum.formatar_reais(valor, comercial=True),
        "total": comum.formatar_reais(total, comercial=True),
        "parcela": comum.formatar_reais(parcela, comercial=True),
        "parcelas": parcelas,
        "titular": titular,
    }
