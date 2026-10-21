"""Modelo de uma nota."""
from dataclasses import dataclass, field


class NotaInvalida(ValueError):
    pass


@dataclass
class Nota:
    id: int
    texto: str
    tags: list = field(default_factory=list)
    feita: bool = False

    def __post_init__(self):
        self.texto = self.texto.strip()
        if not self.texto:
            raise NotaInvalida("texto vazio")
        self.tags = sorted({t.strip().lower() for t in self.tags if t.strip()})

    def tem_tag(self, tag: str) -> bool:
        return tag.strip().lower() in self.tags
