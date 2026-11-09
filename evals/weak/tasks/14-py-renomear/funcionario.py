class Funcionario:
    def __init__(self, nome, carga):
        self.nome = nome
        self.carga = carga

    def sal_liquido(self, bruto):
        inss = bruto * 0.11
        return round(bruto - inss, 2)
