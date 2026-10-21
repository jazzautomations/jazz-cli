"""Como as notas aparecem no terminal."""


def linha(nota) -> str:
    """'#3 comprar café [casa, urgente]'; nota feita ganha ' (feita)' no fim."""
    s = f"#{nota.id} {nota.texto}"
    if nota.tags:
        s += " [" + ", ".join(nota.tags) + "]"
    if nota.feita:
        s += " (feita)"
    return s


def ordenar(notas) -> list:
    """Pendentes primeiro, depois feitas; dentro de cada grupo, por id."""
    return sorted(notas, key=lambda n: (n.feita, n.id))


def listagem(notas) -> str:
    notas = ordenar(notas)
    if not notas:
        return "nenhuma nota"
    return "\n".join(linha(n) for n in notas)
