from funcionario import Funcionario


def resumo(bruto):
    f = Funcionario("operador", "b1")
    liquido = f.sal_liquido(bruto)
    return f"{f.nome}: bruto {bruto}, liquido {liquido}"
