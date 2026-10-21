"""Consolida os pedidos do mês por categoria."""
from pathlib import Path

from .catalogo import carregar_catalogo
from .descontos import carregar_cupons, descontos_do_pedido
from .dinheiro import ZERO, formatar
from .impostos import imposto
from .pedidos import carregar_pedidos, precificar
from .validacao import validar_catalogo, validar_pedidos

CAMPOS = ("bruto", "desconto", "imposto", "liquido")


def consolidar(pedidos: list, cupons: dict) -> dict:
    """{categoria: {"bruto", "desconto", "imposto", "liquido", "itens"}}."""
    linhas = {}
    for pedido in pedidos:
        descontos = descontos_do_pedido(pedido, cupons)
        for cat, bruto in pedido.bruto_por_categoria().items():
            desc = descontos.get(cat, ZERO)
            base = bruto - desc
            imp = imposto(base, pedido.uf, cat)
            l = linhas.setdefault(cat, {c: ZERO for c in CAMPOS} | {"itens": 0})
            l["bruto"] += bruto
            l["desconto"] += desc
            l["imposto"] += imp
            l["liquido"] += base + imp
            l["itens"] += sum(i.quantidade for i in pedido.itens if i.categoria == cat)
    return linhas


def gerar(pasta, avisos=None) -> dict:
    """Lê catalogo.csv, pedidos.csv e cupons.json de `pasta` e consolida.

    Se `avisos` for uma lista, recebe as mensagens de validação dos dados.
    """
    pasta = Path(pasta)
    catalogo = carregar_catalogo(pasta / "catalogo.csv")
    cupons = carregar_cupons(pasta / "cupons.json")
    pedidos = precificar(carregar_pedidos(pasta / "pedidos.csv"), catalogo)
    if avisos is not None:
        avisos.extend(validar_catalogo(catalogo))
        avisos.extend(validar_pedidos(pedidos, cupons))
    return consolidar(pedidos, cupons)


def renderizar(linhas: dict, so_categoria: str = "") -> str:
    out = [f"{'categoria':<12} {'itens':>5} {'bruto':>14} {'desconto':>12} {'imposto':>12} {'liquido':>14}"]
    total = {c: ZERO for c in CAMPOS} | {"itens": 0}
    for cat in sorted(linhas):
        if so_categoria and cat.lower() != so_categoria.lower():
            continue
        l = linhas[cat]
        out.append(
            f"{cat:<12} {l['itens']:>5} {formatar(l['bruto']):>14} {formatar(l['desconto']):>12} "
            f"{formatar(l['imposto']):>12} {formatar(l['liquido']):>14}"
        )
        for c in total:
            total[c] += l[c]
    out.append(
        f"{'TOTAL':<12} {total['itens']:>5} {formatar(total['bruto']):>14} {formatar(total['desconto']):>12} "
        f"{formatar(total['imposto']):>12} {formatar(total['liquido']):>14}"
    )
    return "\n".join(out)
