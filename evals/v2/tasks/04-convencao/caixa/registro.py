"""Registro dos comandos. Cada comando é um módulo em caixa/comandos/."""

COMANDOS = {}


def registrar(nome):
    """Decorador: registra `executar(args) -> int` como o comando `nome`.

    O módulo do comando pode definir `configurar(parser)` para declarar argumentos.
    A primeira linha da docstring de `executar` vira o resumo mostrado em `caixa ajuda`.
    """
    def deco(func):
        if nome in COMANDOS:
            raise RuntimeError(f"comando duplicado: {nome}")
        COMANDOS[nome] = func
        return func
    return deco


def resumo(func) -> str:
    return (func.__doc__ or "").strip().splitlines()[0] if func.__doc__ else ""
