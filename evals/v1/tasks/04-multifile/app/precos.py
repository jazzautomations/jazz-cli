from app.impostos import aliquota
def preco_final(base, estado):
    return round(base * (1 + aliquota(estado)), 2)
