import banco


def linha(txt):
    valor = float(txt)
    return "+" + str(banco.registra(valor, 0.0))
