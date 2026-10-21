"""Cálculos do ponto. Regras de negócio: DESIGN.md."""
from collections import defaultdict


def minutos_por_dia(registros) -> dict:
    """{dia: minutos trabalhados}, somando as marcações de cada dia."""
    tot = defaultdict(int)
    for r in registros:
        tot[r.dia] += r.minutos()
    return dict(sorted(tot.items()))


def total_minutos(registros) -> int:
    return sum(r.minutos() for r in registros)


def horas_extras(registros, referencia) -> int:
    """Minutos de hora extra da semana de apuração que contém a data `referencia`.

    Regras de negócio: DESIGN.md, seção 3.
    """
    raise NotImplementedError
