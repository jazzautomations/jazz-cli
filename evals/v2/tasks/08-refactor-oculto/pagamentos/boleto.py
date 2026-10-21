"""Geração de boleto."""
import hashlib
from datetime import date

VALOR_MINIMO = 1.00
VALOR_MAXIMO = 50000.00


def normalizar_documento(doc: str) -> str:
    return "".join(c for c in str(doc) if c.isdigit())


def validar_cpf(cpf: str) -> str:
    """Valida o CPF (com ou sem pontuação) e devolve só os 11 dígitos."""
    d = normalizar_documento(cpf)
    if len(d) != 11 or d == d[0] * 11:
        raise ValueError(f"CPF inválido: {cpf}")
    for n in (9, 10):
        soma = sum(int(d[i]) * (n + 1 - i) for i in range(n))
        dv = (soma * 10) % 11 % 10
        if dv != int(d[n]):
            raise ValueError(f"CPF inválido: {cpf}")
    return d


def formatar_cpf(cpf: str) -> str:
    d = validar_cpf(cpf)
    return f"{d[:3]}.{d[3:6]}.{d[6:9]}-{d[9:]}"


def formatar_reais(valor: float) -> str:
    """1234.5 -> 'R$ 1.234,50'."""
    v = round(valor, 2)
    inteiro, frac = f"{v:.2f}".split(".")
    inteiro = f"{int(inteiro):,}".replace(",", ".")
    return f"R$ {inteiro},{frac}"


def validar_valor(valor: float) -> float:
    if not isinstance(valor, (int, float)) or isinstance(valor, bool):
        raise TypeError("valor deve ser numérico")
    if valor < VALOR_MINIMO or valor > VALOR_MAXIMO:
        raise ValueError(f"valor fora dos limites do boleto: {valor}")
    return float(valor)


def gerar_boleto(valor: float, cpf: str, vencimento: date) -> dict:
    valor = validar_valor(valor)
    pagador = formatar_cpf(cpf)
    if vencimento < date(2000, 1, 1):
        raise ValueError("vencimento inválido")
    fator = (vencimento - date(1997, 10, 7)).days % 10000
    centavos = int(round(valor * 100))
    base = f"23790{fator:04d}{centavos:010d}{validar_cpf(cpf)}"
    dv = int(hashlib.sha256(base.encode()).hexdigest(), 16) % 10
    return {
        "linha": f"{base[:5]}.{base[5:10]} {base[10:20]} {base[20:]}{dv}",
        "valor": formatar_reais(valor),
        "pagador": pagador,
        "vencimento": vencimento.strftime("%d/%m/%Y"),
    }
