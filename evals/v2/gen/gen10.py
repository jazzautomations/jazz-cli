"""Gera a fila de entrada da tarefa 10 (determinístico)."""
import json
import random
import sys

rnd = random.Random(4471)
CLIENTES = ["Mercado Bom Preço", "Padaria Pão Quente", "Bar do Zé", "Lanchonete Sabor", "Empório Sul",
            "Cantina da Nona", "Hortifruti Verde", "Açougue Boi Gordo", "Pizzaria Forno a Lenha"]
SKUS = ["FAR-25", "FER-01", "ACU-05", "OLE-09", "SAL-01", "CAF-50", "LEI-12", "OVO-30", "MAN-02"]
ids = sorted(rnd.sample(range(3000, 4471), 1200)) + [4471] + sorted(rnd.sample(range(4472, 4800), 200))
out = []
for i in ids:
    if i == 4471:
        out.append({"id": 4471, "cliente": "Padaria Pão Quente", "itens": [
            {"sku": "FAR-25", "qtd": 3, "preco": "1.234,56"}, {"sku": "FER-01", "qtd": 12, "preco": "8,90"}],
            "retirada": False})
        continue
    itens = []
    for _ in range(rnd.randint(1, 4)):
        c = rnd.randint(50, 99999)
        preco = f"{c // 100},{c % 100:02d}" if rnd.random() < 0.85 else c / 100
        itens.append({"sku": rnd.choice(SKUS), "qtd": rnd.choice([1, 1, 2, 3, 5, 10, 12]), "preco": preco})
    p = {"id": i, "cliente": rnd.choice(CLIENTES), "itens": itens, "retirada": rnd.random() < 0.2}
    if rnd.random() < 0.01:
        p["bloqueado"] = True
    out.append(p)
for p in out:
    print(json.dumps(p, ensure_ascii=False))
