TABELA = {"SP": 18, "RJ": 20, "MG": 18}
def aliquota(estado):
    return TABELA.get(estado, 17) / 100
