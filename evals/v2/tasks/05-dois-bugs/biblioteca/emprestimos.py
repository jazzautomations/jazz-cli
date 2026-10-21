"""Empréstimos: prazo de devolução e multa por atraso."""
from dataclasses import dataclass
from datetime import date
from decimal import Decimal

from .calendario import dias_corridos, somar_dias_uteis

PRAZO_DIAS_UTEIS = {"livro": 10, "revista": 3, "dvd": 5}
MULTA_DIA = Decimal("0.50")
MULTA_TETO = Decimal("10.00")


@dataclass
class Emprestimo:
    isbn: str
    leitor: str
    tipo: str
    retirada: date

    def prazo(self) -> date:
        try:
            dias = PRAZO_DIAS_UTEIS[self.tipo]
        except KeyError:
            raise ValueError(f"tipo desconhecido: {self.tipo}") from None
        return somar_dias_uteis(self.retirada, dias)

    def multa(self, devolucao: date) -> Decimal:
        """R$ 0,50 por dia CORRIDO de atraso depois do prazo, até R$ 10,00."""
        atraso = dias_corridos(self.prazo(), devolucao)
        if atraso <= 0:
            return Decimal("0.00")
        return min(MULTA_DIA * atraso, MULTA_TETO).quantize(Decimal("0.01"))
