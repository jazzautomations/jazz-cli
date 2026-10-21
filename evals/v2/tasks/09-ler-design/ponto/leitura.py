"""Leitura do arquivo de marcações."""
from datetime import date, time

from .modelo import Registro


def ler_linha(linha: str) -> Registro:
    partes = linha.split()
    if len(partes) != 3:
        raise ValueError(f"linha inválida: {linha!r}")
    dia = date.fromisoformat(partes[0])
    entrada = time.fromisoformat(partes[1])
    saida = time.fromisoformat(partes[2])
    return Registro(dia, entrada, saida)


def ler_arquivo(caminho) -> list:
    with open(caminho, encoding="utf-8") as f:
        return [ler_linha(l) for l in f if l.strip() and not l.startswith("#")]
