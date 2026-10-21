def gerar(vendas):
    total_a = 0
    for v in vendas:
        if v["regiao"] == "A":
            total_a = total_a + v["valor"]
    total_b = 0
    for v in vendas:
        if v["regiao"] == "B":
            total_b = total_b + v["valor"]
    total_c = 0
    for v in vendas:
        if v["regiao"] == "C":
            total_c = total_c + v["valor"]
    return {"A": total_a, "B": total_b, "C": total_c}
