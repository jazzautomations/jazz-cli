"""Leitura de entradas. Todo comando que lê arquivo aceita '-' para a entrada padrão."""
import sys


class ArquivoNaoEncontrado(Exception):
    pass


def ler_texto(caminho: str) -> str:
    if caminho == "-":
        return sys.stdin.read()
    try:
        with open(caminho, encoding="utf-8", errors="replace") as f:
            return f.read()
    except (FileNotFoundError, IsADirectoryError):
        raise ArquivoNaoEncontrado(caminho) from None


def ler_bytes(caminho: str) -> bytes:
    if caminho == "-":
        return sys.stdin.buffer.read()
    try:
        with open(caminho, "rb") as f:
            return f.read()
    except (FileNotFoundError, IsADirectoryError):
        raise ArquivoNaoEncontrado(caminho) from None
