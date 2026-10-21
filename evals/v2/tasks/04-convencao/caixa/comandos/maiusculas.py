from .. import saida
from ..arquivos import ArquivoNaoEncontrado, ler_texto
from ..registro import registrar


def configurar(p):
    p.add_argument("arquivo", help="caminho do arquivo ou - para a entrada padrão")


@registrar("maiusculas")
def executar(args) -> int:
    """Converte o texto do arquivo para maiúsculas."""
    try:
        texto = ler_texto(args.arquivo)
    except ArquivoNaoEncontrado:
        return saida.erro(f"arquivo não encontrado: {args.arquivo}")
    return saida.resultado(texto.upper().rstrip("\n"), args)
