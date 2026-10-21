"""Geração de boleto."""
import hashlib
from datetime import date

from . import comum
from .comum import formatar_cpf, normalizar_documento, validar_cpf

VALOR_MINIMO = 1.00
VALOR_MAXIMO = 50000.00


def gerar_boleto(valor: float, cpf: str, vencimento: date) -> dict:
    valor = comum.validar_valor(valor, VALOR_MINIMO, VALOR_MAXIMO, "boleto")
    pagador = formatar_cpf(cpf)
    if vencimento < date(2000, 1, 1):
        raise ValueError("vencimento inválido")
    fator = (vencimento - date(1997, 10, 7)).days % 10000
    centavos = int(round(valor * 100))
    base = f"23790{fator:04d}{centavos:010d}{validar_cpf(cpf)}"
    dv = int(hashlib.sha256(base.encode()).hexdigest(), 16) % 10
    return {
        "linha": f"{base[:5]}.{base[5:10]} {base[10:20]} {base[20:]}{dv}",
        "valor": comum.formatar_reais(valor),
        "pagador": pagador,
        "vencimento": vencimento.strftime("%d/%m/%Y"),
    }
