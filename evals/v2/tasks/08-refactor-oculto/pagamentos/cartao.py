"""Autorização de pagamento com cartão (parcelado)."""
from decimal import ROUND_HALF_UP, Decimal

VALOR_MINIMO = 1.00
VALOR_MAXIMO = 20000.00
MAX_PARCELAS = 12
JUROS_MES = Decimal("0.0199")  # a partir da 4ª parcela


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


def formatar_reais(valor) -> str:
    """1234.5 -> 'R$ 1.234,50' (arredondamento comercial, como na fatura do cartão)."""
    v = Decimal(str(valor)).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP)
    inteiro, frac = f"{v:.2f}".split(".")
    inteiro = f"{int(inteiro):,}".replace(",", ".")
    return f"R$ {inteiro},{frac}"


def validar_valor(valor: float) -> float:
    if not isinstance(valor, (int, float)) or isinstance(valor, bool):
        raise TypeError("valor deve ser numérico")
    if valor < VALOR_MINIMO or valor > VALOR_MAXIMO:
        raise ValueError(f"valor fora dos limites do cartão: {valor}")
    return float(valor)


def autorizar(valor: float, cpf: str, parcelas: int = 1) -> dict:
    valor = validar_valor(valor)
    titular = formatar_cpf(cpf)
    if not 1 <= parcelas <= MAX_PARCELAS:
        raise ValueError(f"parcelas deve estar entre 1 e {MAX_PARCELAS}")
    total = Decimal(str(valor))
    if parcelas > 3:
        total *= (1 + JUROS_MES) ** parcelas
    parcela = total / parcelas
    return {
        "valor": formatar_reais(valor),
        "total": formatar_reais(total),
        "parcela": formatar_reais(parcela),
        "parcelas": parcelas,
        "titular": titular,
    }
