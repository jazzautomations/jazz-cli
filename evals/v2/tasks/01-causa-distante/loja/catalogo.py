"""Catálogo de produtos (dados/catalogo.csv)."""
from .dinheiro import parse_brl
from .modelos import Produto
from .util import ler_csv, normalizar_codigo


class ProdutoDesconhecido(KeyError):
    pass


def carregar_catalogo(caminho) -> dict:
    """Carrega o catálogo como {codigo_normalizado: Produto}.

    Se um código aparece mais de uma vez, a ÚLTIMA linha vence: é assim que o
    fornecedor manda atualização de preço (acrescenta a linha nova no fim).
    """
    catalogo = {}
    for linha in ler_csv(caminho):
        codigo = normalizar_codigo(linha["codigo"])
        catalogo[codigo] = Produto(
            codigo=codigo,
            nome=linha["nome"],
            categoria=linha["categoria"],
            preco=parse_brl(linha["preco"]),
        )
    return catalogo


def buscar(catalogo: dict, codigo: str) -> Produto:
    chave = normalizar_codigo(codigo)
    try:
        return catalogo[chave]
    except KeyError:
        raise ProdutoDesconhecido(f"produto não cadastrado: {codigo!r}") from None
