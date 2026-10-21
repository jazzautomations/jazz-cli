"""Funções auxiliares compartilhadas pelos meios de pagamento."""
from decimal import ROUND_HALF_UP, Decimal


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


def formatar_reais(valor, comercial: bool = False) -> str:
    """1234.5 -> 'R$ 1.234,50'.

    comercial=False: round() do float (boleto e pix).
    comercial=True: Decimal com meio para cima (fatura do cartão).
    """
    if comercial:
        v = Decimal(str(valor)).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP)
    else:
        v = round(valor, 2)
    inteiro, frac = f"{v:.2f}".split(".")
    inteiro = f"{int(inteiro):,}".replace(",", ".")
    return f"R$ {inteiro},{frac}"


def validar_valor(valor, minimo: float, maximo, meio: str) -> float:
    """maximo=None: sem teto."""
    if not isinstance(valor, (int, float)) or isinstance(valor, bool):
        raise TypeError("valor deve ser numérico")
    if valor < minimo or (maximo is not None and valor > maximo):
        raise ValueError(f"valor fora dos limites do {meio}: {valor}")
    return float(valor)
