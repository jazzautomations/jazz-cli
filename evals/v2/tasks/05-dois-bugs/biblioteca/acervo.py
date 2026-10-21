"""Acervo em memória, indexado por ISBN."""
from . import isbn as _isbn


class Acervo:
    def __init__(self):
        self._livros = {}
        self._exemplares = {}

    def cadastrar(self, isbn: str, titulo: str, exemplares: int = 1) -> str:
        chave = _isbn.limpar(isbn)
        if not _isbn.valido(chave):
            raise ValueError(f"ISBN inválido: {isbn}")
        if exemplares < 1:
            raise ValueError("exemplares deve ser >= 1")
        self._livros[chave] = titulo
        self._exemplares[chave] = self._exemplares.get(chave, 0) + exemplares
        return chave

    def disponiveis(self, isbn: str) -> int:
        return self._exemplares.get(_isbn.limpar(isbn), 0)

    def retirar(self, isbn: str) -> None:
        chave = _isbn.limpar(isbn)
        if self._exemplares.get(chave, 0) <= 0:
            raise LookupError(f"sem exemplares disponíveis: {isbn}")
        self._exemplares[chave] -= 1

    def devolver(self, isbn: str) -> None:
        chave = _isbn.limpar(isbn)
        if chave not in self._livros:
            raise LookupError(f"ISBN não cadastrado: {isbn}")
        self._exemplares[chave] += 1

    def titulos(self) -> list:
        return sorted(self._livros.values())
