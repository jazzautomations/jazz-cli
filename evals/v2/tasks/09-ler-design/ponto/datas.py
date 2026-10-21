"""Ajudantes de data para EXIBIÇÃO."""
from datetime import date, timedelta

DIAS = ["segunda", "terça", "quarta", "quinta", "sexta", "sábado", "domingo"]


def semana_iso(d: date) -> tuple:
    """(segunda, domingo) da semana ISO de `d` — usada nos relatórios."""
    inicio = d - timedelta(days=d.weekday())
    return inicio, inicio + timedelta(days=6)


def nome_dia(d: date) -> str:
    return DIAS[d.weekday()]


def hhmm(minutos: int) -> str:
    sinal = "-" if minutos < 0 else ""
    m = abs(minutos)
    return f"{sinal}{m // 60:02d}:{m % 60:02d}"
