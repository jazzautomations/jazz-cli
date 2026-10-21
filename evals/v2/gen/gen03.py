"""Gera tasks/03-arquivo-grande/pedidos.py (determinístico)."""
import sys

CLASSES = [
    ("PedidoVarejo", "varejo", "12.90", "0.00"),
    ("PedidoOnline", "online", "9.90", "0.00"),
    ("PedidoAtacadoLegado", "atacado_legado", "0.00", "2.50"),
    ("PedidoAssinatura", "assinatura", "0.00", "0.00"),
    ("PedidoMarketplace", "marketplace", "14.90", "4.00"),
    ("PedidoCorporativo", "corporativo", "0.00", "1.50"),
    ("PedidoGoverno", "governo", "0.00", "0.00"),
    ("PedidoExportacao", "exportacao", "89.00", "0.00"),
    ("PedidoRevenda", "revenda", "19.90", "3.00"),
    ("PedidoFranquia", "franquia", "0.00", "2.00"),
    ("PedidoAtacado", "atacado", "0.00", "2.50"),
    ("PedidoBalcao", "balcao", "0.00", "0.00"),
    ("PedidoTelevendas", "televendas", "12.90", "0.00"),
    ("PedidoConsignado", "consignado", "0.00", "5.00"),
    ("PedidoAmostra", "amostra", "0.00", "0.00"),
    ("PedidoDoacao", "doacao", "0.00", "0.00"),
]

HEADER = '''"""Tipos de pedido do sistema de vendas.

Código gerado pelo antigo gerador de pedidos (2019) e mantido à mão desde então.
Cada tipo de pedido tem a SUA cópia dos métodos, de propósito: as equipes de cada
canal mudam as regras do seu tipo sem afetar os outros. NÃO unifique as classes
nem mova métodos para uma classe base.
"""
import json
from datetime import date

ARREDONDAMENTO = 2


def _arredondar(valor):
    return round(valor + 0.0, ARREDONDAMENTO)


def _hoje():
    return date.today().isoformat()


class ItemDePedido:
    def __init__(self, sku, preco, quantidade):
        self.sku = sku
        self.preco = preco
        self.quantidade = quantidade

    def subtotal(self):
        return _arredondar(self.preco * self.quantidade)

    def __repr__(self):
        return f"ItemDePedido({self.sku!r}, {self.preco!r}, {self.quantidade!r})"
'''

BODY = '''

class {nome}:
    """Pedido do canal {tipo}."""

    TIPO = "{tipo}"
    FRETE_FIXO = {frete}
    TAXA_SERVICO_PCT = {taxa}

    def __init__(self, cliente="", data=None):
        self.cliente = cliente
        self.data = data or _hoje()
        self.itens = []
        self.desconto = 0.0
        self.observacoes = []
        self.cancelado = False

    def adicionar_item(self, sku, preco, quantidade=1):
        """Adiciona um item; se o SKU já existe, soma a quantidade."""
        self.validar_quantidade(quantidade)
        if preco < 0:
            raise ValueError("preço negativo")
        for item in self.itens:
            if item.sku == sku:
                item.quantidade += quantidade
                return item
        item = ItemDePedido(sku, preco, quantidade)
        self.itens.append(item)
        return item

    def remover_item(self, sku):
        antes = len(self.itens)
        self.itens = [i for i in self.itens if i.sku != sku]
        if len(self.itens) == antes:
            raise KeyError(sku)

    def subtotal(self):
        return _arredondar(sum(i.subtotal() for i in self.itens))

    def aplicar_desconto(self, percentual):
        """Aplica um desconto percentual sobre o subtotal."""
        if percentual < 0:
            raise ValueError("desconto negativo")
        if percentual > 50:
            percentual = 50
        self.desconto = _arredondar(self.subtotal() * percentual / 100)
        return self.desconto

    def taxa_servico(self):
        return _arredondar(self.subtotal() * self.TAXA_SERVICO_PCT / 100)

    def calcular_frete(self):
        if not self.itens:
            return 0.0
        return self.FRETE_FIXO

    def total(self):
        if self.cancelado:
            return 0.0
        return _arredondar(self.subtotal() - self.desconto + self.taxa_servico() + self.calcular_frete())

    def cancelar(self, motivo):
        if self.cancelado:
            raise RuntimeError("pedido já cancelado")
        self.cancelado = True
        self.observacoes.append("cancelado: " + motivo)

    def anotar(self, texto):
        texto = texto.strip()
        if texto:
            self.observacoes.append(texto)

    def quantidade_total(self):
        return sum(i.quantidade for i in self.itens)

    def resumo(self):
        linhas = [f"[{{self.TIPO}}] {{self.cliente or 'sem cliente'}} em {{self.data}}"]
        for i in self.itens:
            linhas.append(f"  {{i.sku}} x{{i.quantidade}} = {{i.subtotal():.2f}}")
        linhas.append(f"  total: {{self.total():.2f}}")
        return "\\n".join(linhas)

    def para_dict(self):
        return {{
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }}

    def para_json(self):
        return json.dumps(self.para_dict(), ensure_ascii=False, sort_keys=True)

    @classmethod
    def de_dict(cls, d):
        if d.get("tipo") != cls.TIPO:
            raise ValueError("tipo incompatível: " + str(d.get("tipo")))
        p = cls(d.get("cliente", ""), d.get("data"))
        for sku, preco, qtd in d.get("itens", []):
            p.adicionar_item(sku, preco, qtd)
        p.desconto = d.get("desconto", 0.0)
        p.cancelado = d.get("cancelado", False)
        p.observacoes = list(d.get("observacoes", []))
        return p

    def validar_quantidade(self, quantidade):
        if not isinstance(quantidade, int):
            raise TypeError("quantidade deve ser inteira")
        if quantidade < 1:
            raise ValueError("quantidade mínima é 1 unidade")
        return quantidade
'''

FOOTER = '''

TIPOS = {{{tipos}}}


def criar(tipo, cliente=""):
    try:
        return TIPOS[tipo](cliente)
    except KeyError:
        raise ValueError("tipo de pedido desconhecido: " + tipo) from None
'''

out = [HEADER]
for nome, tipo, frete, taxa in CLASSES:
    out.append(BODY.format(nome=nome, tipo=tipo, frete=frete, taxa=taxa))
out.append(FOOTER.format(tipos=", ".join(f'"{t}": {n}' for n, t, _, _ in CLASSES)))
sys.stdout.write("".join(out))
