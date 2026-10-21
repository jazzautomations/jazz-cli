from dataclasses import dataclass
from datetime import date, time


@dataclass(frozen=True)
class Registro:
    dia: date
    entrada: time
    saida: time

    def minutos(self) -> int:
        """Duração em minutos; saída menor que a entrada = passou da meia-noite."""
        e = self.entrada.hour * 60 + self.entrada.minute
        s = self.saida.hour * 60 + self.saida.minute
        if s < e:
            s += 24 * 60
        return s - e
