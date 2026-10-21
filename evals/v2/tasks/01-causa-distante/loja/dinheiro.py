"""Valores monetários: sempre Decimal, arredondamento comercial (meio para cima)."""
from decimal import Decimal, ROUND_HALF_UP

CENTAVO = Decimal("0.01")
ZERO = Decimal("0")


def parse_brl(texto: str) -> Decimal:
    """'1.234,56' -> Decimal('1234.56'); aceita também 'R$ 12,90'."""
    t = texto.replace("R$", "").strip().replace(".", "").replace(",", ".")
    if not t:
        raise ValueError("valor vazio")
    return Decimal(t)


def arredondar(valor: Decimal) -> Decimal:
    return valor.quantize(CENTAVO, rounding=ROUND_HALF_UP)


def porcentagem(valor: Decimal, pct) -> Decimal:
    """pct% de valor, arredondado ao centavo."""
    return arredondar(valor * Decimal(str(pct)) / Decimal(100))


def formatar(valor: Decimal) -> str:
    """Decimal('1234.5') -> 'R$ 1.234,50'."""
    v = arredondar(valor)
    sinal = "-" if v < 0 else ""
    inteiro, _, frac = f"{abs(v):.2f}".partition(".")
    grupos = []
    while inteiro:
        grupos.insert(0, inteiro[-3:])
        inteiro = inteiro[:-3]
    return f"{sinal}R$ {'.'.join(grupos)},{frac}"
