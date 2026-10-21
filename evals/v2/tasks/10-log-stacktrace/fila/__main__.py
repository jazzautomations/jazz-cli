import logging
import sys

from .principal import log, processar_lote

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)-5s %(name)s %(message)s")
if len(sys.argv) != 3:
    print("uso: python3 -m fila ENTRADA SAIDA", file=sys.stderr)
    sys.exit(2)
try:
    n = processar_lote(sys.argv[1], sys.argv[2])
except Exception:
    log.exception("erro não tratado")
    log.critical("worker encerrado após erro não tratado")
    sys.exit(1)
log.info("%d pedidos processados", n)
