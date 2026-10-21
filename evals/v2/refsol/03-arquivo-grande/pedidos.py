"""Tipos de pedido do sistema de vendas.

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


class PedidoVarejo:
    """Pedido do canal varejo."""

    TIPO = "varejo"
    FRETE_FIXO = 12.90
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoOnline:
    """Pedido do canal online."""

    TIPO = "online"
    FRETE_FIXO = 9.90
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoAtacadoLegado:
    """Pedido do canal atacado_legado."""

    TIPO = "atacado_legado"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 2.50

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoAssinatura:
    """Pedido do canal assinatura."""

    TIPO = "assinatura"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoMarketplace:
    """Pedido do canal marketplace."""

    TIPO = "marketplace"
    FRETE_FIXO = 14.90
    TAXA_SERVICO_PCT = 4.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoCorporativo:
    """Pedido do canal corporativo."""

    TIPO = "corporativo"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 1.50

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoGoverno:
    """Pedido do canal governo."""

    TIPO = "governo"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoExportacao:
    """Pedido do canal exportacao."""

    TIPO = "exportacao"
    FRETE_FIXO = 89.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoRevenda:
    """Pedido do canal revenda."""

    TIPO = "revenda"
    FRETE_FIXO = 19.90
    TAXA_SERVICO_PCT = 3.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoFranquia:
    """Pedido do canal franquia."""

    TIPO = "franquia"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 2.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoAtacado:
    """Pedido do canal atacado."""

    TIPO = "atacado"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 2.50

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
        if percentual > 30:
            percentual = 30
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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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
        if quantidade < 10:
            raise ValueError("quantidade mínima é 10 unidades")
        return quantidade


class PedidoBalcao:
    """Pedido do canal balcao."""

    TIPO = "balcao"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoTelevendas:
    """Pedido do canal televendas."""

    TIPO = "televendas"
    FRETE_FIXO = 12.90
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoConsignado:
    """Pedido do canal consignado."""

    TIPO = "consignado"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 5.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoAmostra:
    """Pedido do canal amostra."""

    TIPO = "amostra"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


class PedidoDoacao:
    """Pedido do canal doacao."""

    TIPO = "doacao"
    FRETE_FIXO = 0.00
    TAXA_SERVICO_PCT = 0.00

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
        linhas = [f"[{self.TIPO}] {self.cliente or 'sem cliente'} em {self.data}"]
        for i in self.itens:
            linhas.append(f"  {i.sku} x{i.quantidade} = {i.subtotal():.2f}")
        linhas.append(f"  total: {self.total():.2f}")
        return "\n".join(linhas)

    def para_dict(self):
        return {
            "tipo": self.TIPO,
            "cliente": self.cliente,
            "data": self.data,
            "itens": [[i.sku, i.preco, i.quantidade] for i in self.itens],
            "desconto": self.desconto,
            "cancelado": self.cancelado,
            "observacoes": list(self.observacoes),
        }

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


TIPOS = {"varejo": PedidoVarejo, "online": PedidoOnline, "atacado_legado": PedidoAtacadoLegado, "assinatura": PedidoAssinatura, "marketplace": PedidoMarketplace, "corporativo": PedidoCorporativo, "governo": PedidoGoverno, "exportacao": PedidoExportacao, "revenda": PedidoRevenda, "franquia": PedidoFranquia, "atacado": PedidoAtacado, "balcao": PedidoBalcao, "televendas": PedidoTelevendas, "consignado": PedidoConsignado, "amostra": PedidoAmostra, "doacao": PedidoDoacao}


def criar(tipo, cliente=""):
    try:
        return TIPOS[tipo](cliente)
    except KeyError:
        raise ValueError("tipo de pedido desconhecido: " + tipo) from None
