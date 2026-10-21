from .. import saida
from ..arquivos import ArquivoNaoEncontrado, ler_texto
from ..registro import registrar


def configurar(p):
    p.add_argument("arquivo", help="caminho do arquivo ou - para a entrada padrão")


@registrar("contar")
def executar(args) -> int:
    """Conta linhas, palavras e caracteres do arquivo."""
    try:
        texto = ler_texto(args.arquivo)
    except ArquivoNaoEncontrado:
        return saida.erro(f"arquivo não encontrado: {args.arquivo}")
    return saida.resultado(
        {"linhas": len(texto.splitlines()), "palavras": len(texto.split()), "caracteres": len(texto)}, args
    )
