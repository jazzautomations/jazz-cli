"""Como as notas aparecem no terminal."""
from .modelo import PRIORIDADES


def linha(nota) -> str:
    """'#3 comprar café [casa, urgente]'; nota feita ganha ' (feita)' no fim.

    Prioridade alta/baixa aparece logo depois do id: '#3 (alta) comprar café'.
    """
    s = f"#{nota.id} "
    if nota.prioridade != "normal":
        s += f"({nota.prioridade}) "
    s += nota.texto
    if nota.tags:
        s += " [" + ", ".join(nota.tags) + "]"
    if nota.feita:
        s += " (feita)"
    return s


def ordenar(notas) -> list:
    """Pendentes primeiro (alta, normal, baixa; por id em cada nível), depois feitas por id."""
    return sorted(notas, key=lambda n: (n.feita, 0 if n.feita else PRIORIDADES.index(n.prioridade), n.id))


def listagem(notas) -> str:
    notas = ordenar(notas)
    if not notas:
        return "nenhuma nota"
    return "\n".join(linha(n) for n in notas)
