"""Leitura e gravação da carteira em JSON (com a lib fastjson do projeto)."""


def carregar(caminho):
    """Lê a carteira de `caminho`; valores fracionários como Decimal."""
    raise NotImplementedError


def salvar(carteira, caminho):
    """Grava `carteira` em `caminho`: 2 espaços de indentação, chaves em ordem."""
    raise NotImplementedError
