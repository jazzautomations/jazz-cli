"""Cupons de desconto (dados/cupons.json).

Formato:
    {"CODIGO": {"tipo": "percentual", "valor": 10, "categorias": ["Ferragens"]},
     "CODIGO2": {"tipo": "fixo", "valor": "15,00", "categorias": ["Cabos"]}}

- percentual: valor% sobre o bruto de cada categoria elegível do pedido;
- fixo: abate o valor do bruto da categoria elegível (nunca abaixo de zero);
  se houver mais de uma categoria elegível no pedido, vale a de maior bruto.
"""
import json

from .dinheiro import ZERO, parse_brl, porcentagem
from .util import chave_categoria


def carregar_cupons(caminho) -> dict:
    with open(caminho, encoding="utf-8") as f:
        brutos = json.load(f)
    return {codigo.upper(): regra for codigo, regra in brutos.items()}


def descontos_do_pedido(pedido, cupons: dict) -> dict:
    """Retorna {categoria: desconto} para o pedido (vazio se não há cupom válido)."""
    if not pedido.cupom or pedido.cupom not in cupons:
        return {}
    regra = cupons[pedido.cupom]
    elegiveis = {chave_categoria(c) for c in regra.get("categorias", [])}
    bruto = pedido.bruto_por_categoria()
    alvo = {cat: v for cat, v in bruto.items() if chave_categoria(cat) in elegiveis}
    if not alvo:
        return {}
    if regra["tipo"] == "percentual":
        return {cat: porcentagem(v, regra["valor"]) for cat, v in alvo.items()}
    if regra["tipo"] == "fixo":
        cat = max(alvo, key=lambda c: (alvo[c], c))
        return {cat: min(alvo[cat], parse_brl(str(regra["valor"])))}
    raise ValueError(f"tipo de cupom desconhecido: {regra['tipo']!r}")


def total_desconto(descontos: dict):
    return sum(descontos.values(), ZERO)
