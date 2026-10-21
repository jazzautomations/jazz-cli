# fila

Worker que consome os pedidos da fila (um JSON por linha) e grava o total de cada um.

    python3 -m fila entrada.jsonl saida.jsonl

Em produção roda em /opt/fila; os logs vão para logs/worker-AAAA-MM-DD.log.
Testes: `python3 -m unittest`
