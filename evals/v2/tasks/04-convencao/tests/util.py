"""Ajudantes de teste: rodam a caixa no próprio processo."""
import contextlib
import io
import os
import sys
import tempfile

from caixa.__main__ import main


def rodar(*args, entrada=""):
    """Roda `caixa *args` e devolve (código, stdout, stderr)."""
    out, err = io.StringIO(), io.StringIO()
    velho_stdin = sys.stdin
    sys.stdin = io.TextIOWrapper(io.BytesIO(entrada.encode("utf-8")), encoding="utf-8")
    try:
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = main(list(args))
    finally:
        sys.stdin = velho_stdin
    return rc, out.getvalue(), err.getvalue()


def arquivo_temp(conteudo: str) -> str:
    """Cria um arquivo temporário com `conteudo` e devolve o caminho (apagado no fim do processo)."""
    fd, caminho = tempfile.mkstemp(suffix=".txt")
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(conteudo)
    _CRIADOS.append(caminho)
    return caminho


_CRIADOS = []


def _limpar():
    for c in _CRIADOS:
        with contextlib.suppress(OSError):
            os.remove(c)


import atexit  # noqa: E402

atexit.register(_limpar)
