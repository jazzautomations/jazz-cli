"""Pedidos do mês (dados/pedidos.csv): uma linha por item, agrupadas pelo número."""
from .catalogo import buscar
from .modelos import ItemPedido, Pedido
from .util import ler_csv, parse_quantidade


def carregar_pedidos(caminho) -> list:
    pedidos = {}
    for linha in ler_csv(caminho):
        numero = linha["pedido"]
        if numero not in pedidos:
            pedidos[numero] = Pedido(
                numero=numero,
                cliente=linha["cliente"],
                uf=linha["uf"].upper(),
                cupom=linha.get("cupom", "").upper(),
            )
        pedidos[numero].itens.append(
            ItemPedido(codigo=linha["codigo"], quantidade=parse_quantidade(linha["qtd"]))
        )
    return list(pedidos.values())


def precificar(pedidos: list, catalogo: dict) -> list:
    """Preenche preço unitário e categoria de cada item a partir do catálogo."""
    for pedido in pedidos:
        for item in pedido.itens:
            produto = buscar(catalogo, item.codigo)
            item.preco_unitario = produto.preco
            item.categoria = produto.categoria
    return pedidos
