import banco


def compensar(entradas, saldo=0.0):
    for e in entradas:
        saldo = banco.registra(e, saldo)
    return saldo
