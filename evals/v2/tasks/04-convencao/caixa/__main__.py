import argparse
import sys

from . import comandos
from .registro import COMANDOS, resumo


def montar_parser(modulos: dict) -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="caixa")
    p.add_argument("--json", action="store_true", help="saída em JSON")
    sub = p.add_subparsers(dest="comando", required=True)
    sub.add_parser("ajuda", help="lista os comandos")
    for nome, modulo in modulos.items():
        sp = sub.add_parser(nome, help=resumo(COMANDOS[nome]))
        if hasattr(modulo, "configurar"):
            modulo.configurar(sp)
    return p


def ajuda() -> int:
    largura = max(len(n) for n in COMANDOS)
    for nome, func in COMANDOS.items():
        print(f"{nome:<{largura}}  {resumo(func)}")
    return 0


def main(argv=None) -> int:
    modulos = comandos.carregar()
    args = montar_parser(modulos).parse_args(argv)
    if args.comando == "ajuda":
        return ajuda()
    return COMANDOS[args.comando](args)


if __name__ == "__main__":
    sys.exit(main())
