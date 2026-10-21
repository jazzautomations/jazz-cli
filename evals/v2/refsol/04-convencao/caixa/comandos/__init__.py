"""Lista dos módulos de comando carregados pela caixa (em ordem de exibição na ajuda)."""
import importlib

MODULOS = ("maiusculas", "unicas", "hash", "frequencia", "contar")


def carregar() -> dict:
    """Importa os módulos (o decorador @registrar preenche o registro) e devolve {nome: módulo}."""
    return {nome: importlib.import_module(f"{__name__}.{nome}") for nome in MODULOS}
