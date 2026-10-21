"""Interface de linha de comando."""
import argparse
import sys

from . import armazenamento
from .formatacao import linha, listagem
from .modelo import PRIORIDADES, NotaInvalida


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="notas")
    p.add_argument("--arquivo", default="notas.json")
    sub = p.add_subparsers(dest="cmd", required=True)

    a = sub.add_parser("add", help="adiciona uma nota")
    a.add_argument("texto")
    a.add_argument("--tag", action="append", default=[])
    a.add_argument("--prioridade", choices=PRIORIDADES, default="normal")

    l = sub.add_parser("list", help="lista as notas")
    l.add_argument("--tag", default="")
    l.add_argument("--pendentes", action="store_true")
    l.add_argument("--prioridade", choices=PRIORIDADES, default="")

    pr = sub.add_parser("prioridade", help="muda a prioridade de uma nota")
    pr.add_argument("id", type=int)
    pr.add_argument("nivel", choices=PRIORIDADES)

    f = sub.add_parser("feito", help="marca uma nota como feita")
    f.add_argument("id", type=int)

    r = sub.add_parser("rm", help="remove uma nota")
    r.add_argument("id", type=int)
    return p


def main(argv=None) -> int:
    args = _parser().parse_args(argv)
    bloco = armazenamento.carregar(args.arquivo)
    try:
        if args.cmd == "add":
            nota = bloco.adicionar(args.texto, args.tag, args.prioridade)
            print(f"criada: {linha(nota)}")
        elif args.cmd == "list":
            notas = bloco.notas
            if args.tag:
                notas = [n for n in notas if n.tem_tag(args.tag)]
            if args.prioridade:
                notas = [n for n in notas if n.prioridade == args.prioridade]
            if args.pendentes:
                notas = [n for n in notas if not n.feita]
            print(listagem(notas))
            return 0
        elif args.cmd == "feito":
            nota = bloco.buscar(args.id)
            nota.feita = True
            print(f"feita: {linha(nota)}")
        elif args.cmd == "prioridade":
            nota = bloco.buscar(args.id)
            nota.prioridade = args.nivel
            print(f"alterada: {linha(nota)}")
        elif args.cmd == "rm":
            nota = bloco.remover(args.id)
            print(f"removida: #{nota.id}")
    except KeyError as e:
        print(f"erro: nota #{e.args[0]} não existe", file=sys.stderr)
        return 1
    except NotaInvalida as e:
        print(f"erro: {e}", file=sys.stderr)
        return 1
    armazenamento.salvar(bloco, args.arquivo)
    return 0
