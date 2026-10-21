"""Toda saída dos comandos passa por aqui (texto ou --json)."""
import json
import sys


def erro(mensagem: str) -> int:
    """Mostra o erro no padrão da caixa e devolve o código de saída 2."""
    print(f"caixa: erro: {mensagem}", file=sys.stderr)
    return 2


def resultado(dados, args) -> int:
    """Imprime o resultado do comando e devolve 0.

    Com --json: um objeto JSON numa linha. Sem: dict vira 'chave: valor' por linha,
    lista vira um item por linha, o resto é impresso como está.
    """
    if getattr(args, "json", False):
        print(json.dumps(dados, ensure_ascii=False, sort_keys=True))
    elif isinstance(dados, dict):
        for chave, valor in dados.items():
            print(f"{chave}: {valor}")
    elif isinstance(dados, list):
        for item in dados:
            print(item)
    else:
        print(dados)
    return 0
