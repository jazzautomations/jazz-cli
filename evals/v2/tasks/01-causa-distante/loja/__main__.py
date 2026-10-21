import argparse
import sys

from .catalogo import ProdutoDesconhecido
from .exportar import para_csv, para_json
from .relatorio import gerar, renderizar


def main(argv=None) -> int:
    p = argparse.ArgumentParser(prog="loja")
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("relatorio", help="relatório do mês por categoria")
    r.add_argument("pasta")
    r.add_argument("--categoria", default="")
    r.add_argument("--formato", choices=("texto", "csv", "json"), default="texto")
    r.add_argument("--avisos", action="store_true", help="mostra avisos de validação no stderr")
    args = p.parse_args(argv)
    avisos = []
    try:
        linhas = gerar(args.pasta, avisos)
    except (OSError, ProdutoDesconhecido, ValueError) as e:
        print(f"erro: {e}", file=sys.stderr)
        return 1
    if args.avisos:
        for a in avisos:
            print(f"aviso: {a}", file=sys.stderr)
    if args.formato == "csv":
        print(para_csv(linhas), end="")
    elif args.formato == "json":
        print(para_json(linhas))
    else:
        print(renderizar(linhas, args.categoria))
    return 0


if __name__ == "__main__":
    sys.exit(main())
