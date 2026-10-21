"""Modelo de uma nota."""
from dataclasses import dataclass, field


PRIORIDADES = ("alta", "normal", "baixa")


class NotaInvalida(ValueError):
    pass


@dataclass
class Nota:
    id: int
    texto: str
    tags: list = field(default_factory=list)
    feita: bool = False
    prioridade: str = "normal"

    def __post_init__(self):
        self.texto = self.texto.strip()
        if not self.texto:
            raise NotaInvalida("texto vazio")
        self.tags = sorted({t.strip().lower() for t in self.tags if t.strip()})
        self.prioridade = (self.prioridade or "normal").strip().lower()
        if self.prioridade not in PRIORIDADES:
            raise NotaInvalida(f"prioridade inválida: {self.prioridade!r}")

    def tem_tag(self, tag: str) -> bool:
        return tag.strip().lower() in self.tags
