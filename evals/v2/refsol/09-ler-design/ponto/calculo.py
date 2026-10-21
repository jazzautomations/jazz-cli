"""Cálculos do ponto. Regras de negócio: DESIGN.md."""
from collections import defaultdict
from datetime import timedelta

JORNADA_SEMANAL = 2400
TOLERANCIA = 10
QUARTA = 2
DOMINGO = 6


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
    inicio = referencia - timedelta(days=(referencia.weekday() - QUARTA) % 7)
    fim = inicio + timedelta(days=6)
    normal = domingo = 0
    for r in registros:
        if inicio <= r.dia <= fim:
            if r.dia.weekday() == DOMINGO:
                domingo += r.minutos()
            else:
                normal += r.minutos()
    extra = max(0, normal - JORNADA_SEMANAL) + 2 * domingo
    return 0 if extra <= TOLERANCIA else extra
