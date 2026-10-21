"""Tarefa 10: conversão de preço consertada e o pedido do log processa certo."""
import os
import sys

sys.path.insert(0, os.getcwd())
from fila.precos import para_centavos  # noqa: E402
from fila.processador import processar  # noqa: E402


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


casos = {"1.234,56": 123456, "12,90": 1290, "R$ 1.000.000,00": 100000000, "0,99": 99, " 7,00 ": 700,
         "R$ 7,00": 700, "999,99": 99999, "10.000,50": 1000050, 12.9: 1290, 3: 300, 1500.5: 150050}
for entrada, esperado in casos.items():
    try:
        got = para_centavos(entrada)
    except Exception as e:  # noqa: BLE001
        fail(f"para_centavos({entrada!r}) levantou {type(e).__name__}: {e}")
    if got != esperado:
        fail(f"para_centavos({entrada!r}) = {got!r}, esperado {esperado}")
p = {"id": 4471, "cliente": "Padaria Pão Quente", "itens": [
    {"sku": "FAR-25", "qtd": 3, "preco": "1.234,56"}, {"sku": "FER-01", "qtd": 12, "preco": "8,90"}],
    "retirada": False}
if processar(p) != {"id": 4471, "subtotal": 380514, "frete": 0, "total": 380514}:
    fail(f"pedido 4471 com total errado: {processar(p)}")
print("ok")
