import re
from collections import Counter

from .. import saida
from ..arquivos import ArquivoNaoEncontrado, ler_texto
from ..registro import registrar


def configurar(p):
    p.add_argument("arquivo", help="caminho do arquivo ou - para a entrada padrão")
    p.add_argument("--top", type=int, default=5, help="quantas palavras mostrar (padrão 5)")


@registrar("frequencia")
def executar(args) -> int:
    """Mostra as palavras mais frequentes do arquivo."""
    if args.top < 1:
        return saida.erro("--top precisa ser pelo menos 1")
    try:
        texto = ler_texto(args.arquivo)
    except ArquivoNaoEncontrado:
        return saida.erro(f"arquivo não encontrado: {args.arquivo}")
    palavras = re.findall(r"\w+", texto.lower())
    mais = Counter(palavras).most_common(args.top)
    return saida.resultado(dict(mais), args)
