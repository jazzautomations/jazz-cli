import hashlib

from .. import saida
from ..arquivos import ArquivoNaoEncontrado, ler_bytes
from ..registro import registrar


def configurar(p):
    p.add_argument("arquivo", help="caminho do arquivo ou - para a entrada padrão")


@registrar("hash")
def executar(args) -> int:
    """Mostra o sha256 e o tamanho em bytes do arquivo."""
    try:
        dados = ler_bytes(args.arquivo)
    except ArquivoNaoEncontrado:
        return saida.erro(f"arquivo não encontrado: {args.arquivo}")
    return saida.resultado({"sha256": hashlib.sha256(dados).hexdigest(), "bytes": len(dados)}, args)
