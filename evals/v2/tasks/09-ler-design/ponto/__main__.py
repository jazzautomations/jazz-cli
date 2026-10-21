import sys

from .calculo import minutos_por_dia
from .datas import hhmm, nome_dia
from .leitura import ler_arquivo


def main(argv=None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) != 1:
        print("uso: python3 -m ponto ARQUIVO", file=sys.stderr)
        return 2
    for dia, m in minutos_por_dia(ler_arquivo(argv[0])).items():
        print(f"{dia.isoformat()} {nome_dia(dia):<8} {hhmm(m)}")
    return 0


sys.exit(main())
