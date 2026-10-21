"""Cobrança via Pix."""
import hashlib

from . import comum
from .comum import formatar_cpf, normalizar_documento, validar_cpf

VALOR_MINIMO = 0.01

TIPOS_CHAVE = ("cpf", "email", "telefone", "aleatoria")


def tipo_da_chave(chave: str) -> str:
    if "@" in chave:
        return "email"
    digitos = normalizar_documento(chave)
    if chave.startswith("+") and len(digitos) in (12, 13):
        return "telefone"
    if len(digitos) == 11 and digitos == chave.replace(".", "").replace("-", ""):
        return "cpf"
    if len(chave) == 36 and chave.count("-") == 4:
        return "aleatoria"
    raise ValueError(f"chave pix inválida: {chave}")


def gerar_cobranca(valor: float, cpf: str, chave: str) -> dict:
    valor = comum.validar_valor(valor, VALOR_MINIMO, None, "pix")
    pagador = formatar_cpf(cpf)
    tipo = tipo_da_chave(chave)
    txid = hashlib.sha1(f"{chave}|{validar_cpf(cpf)}|{valor:.2f}".encode()).hexdigest()[:25].upper()
    return {"valor": comum.formatar_reais(valor), "pagador": pagador, "chave": chave, "tipo": tipo, "txid": txid}
