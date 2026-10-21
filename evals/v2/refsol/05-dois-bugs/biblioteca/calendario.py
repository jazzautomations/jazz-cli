"""Dias úteis (segunda a sexta, menos feriados)."""
from datetime import date, timedelta

FERIADOS_FIXOS = {(1, 1), (4, 21), (5, 1), (9, 7), (10, 12), (11, 2), (11, 15), (11, 20), (12, 25)}


def eh_feriado(d: date, extras=()) -> bool:
    return (d.month, d.day) in FERIADOS_FIXOS or d in extras


def eh_dia_util(d: date, extras=()) -> bool:
    return d.weekday() < 5 and not eh_feriado(d, extras)


def somar_dias_uteis(inicio: date, n: int, extras=()) -> date:
    """Data que cai `n` dias úteis depois de `inicio` (o próprio dia não conta)."""
    if n < 0:
        raise ValueError("n negativo")
    d = inicio
    while n > 0:
        d += timedelta(days=1)
        if eh_dia_util(d, extras):
            n -= 1
    return d


def dias_corridos(de: date, ate: date) -> int:
    return (ate - de).days
