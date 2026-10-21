"""Laço principal do worker."""
import json
import logging

from . import processador

log = logging.getLogger("worker.principal")


def processar_lote(entrada, saida) -> int:
    n = 0
    with open(entrada, encoding="utf-8") as fe, open(saida, "a", encoding="utf-8") as fs:
        for linha in fe:
            if not linha.strip():
                continue
            pedido = json.loads(linha)
            logging.getLogger("worker.processador").info("pedido id=%s recebido", pedido.get("id"))
            try:
                resultado = processador.processar(pedido)
            except processador.PedidoRejeitado as e:
                log.warning("pedido id=%s rejeitado: %s", pedido.get("id"), e)
                continue
            except Exception:
                log.error("falha ao processar pedido id=%s payload=%s", pedido.get("id"),
                          json.dumps(pedido, ensure_ascii=False))
                raise
            fs.write(json.dumps(resultado) + "\n")
            n += 1
    return n
