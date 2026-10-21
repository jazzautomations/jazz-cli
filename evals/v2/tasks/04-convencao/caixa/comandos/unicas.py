from .. import saida
from ..arquivos import ArquivoNaoEncontrado, ler_texto
from ..registro import registrar


def configurar(p):
    p.add_argument("arquivo", help="caminho do arquivo ou - para a entrada padrão")
    p.add_argument("--ignorar-caixa", action="store_true", help="'Abc' e 'abc' contam como iguais")


@registrar("unicas")
def executar(args) -> int:
    """Remove linhas repetidas, mantendo a primeira ocorrência."""
    try:
        texto = ler_texto(args.arquivo)
    except ArquivoNaoEncontrado:
        return saida.erro(f"arquivo não encontrado: {args.arquivo}")
    vistas, saida_linhas = set(), []
    for linha in texto.splitlines():
        chave = linha.casefold() if args.ignorar_caixa else linha
        if chave not in vistas:
            vistas.add(chave)
            saida_linhas.append(linha)
    return saida.resultado(saida_linhas, args)
