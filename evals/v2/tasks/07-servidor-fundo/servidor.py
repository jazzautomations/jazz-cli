"""Servidor HTTP de cotações de TESTE (escuta só em 127.0.0.1).

Uso:
    python3 servidor.py --porta N      (N = 0 escolhe uma porta livre)

Assim que estiver pronto imprime UMA linha:
    ouvindo em http://127.0.0.1:<porta>
e fica rodando até receber SIGTERM ou SIGINT (Ctrl+C). Ele não termina sozinho.

Endpoints:
    GET /token              -> {"token": "<texto>"}
    GET /cotacoes?token=T   -> {"base": "BRL", "cotacoes": [{"moeda": "USD", "valor": "5.4321"}, ...]}

O token só vale para a instância do servidor que o emitiu (reiniciar o servidor invalida
os tokens antigos). Os valores mudam a cada instância; são strings e devem ser usados
exatamente como vieram.
"""
import argparse
import hashlib
import json
import os
import secrets
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

AQUI = os.path.dirname(os.path.abspath(__file__))
AUDITORIA = os.path.join(AQUI, ".servidor.audit")
MOEDAS = ["USD", "EUR", "GBP", "JPY", "ARS", "CLP", "CHF", "CAD"]
BASE = {"USD": 5.41, "EUR": 6.02, "GBP": 7.11, "JPY": 0.0362, "ARS": 0.0058, "CLP": 0.0059,
        "CHF": 6.38, "CAD": 3.95}


def auditar(linha):
    with open(AUDITORIA, "a", encoding="utf-8") as f:
        f.write(linha + "\n")


class Estado:
    def __init__(self):
        self.semente = secrets.token_bytes(16)
        self.tokens = set()

    def cotacoes(self):
        out = []
        for m in MOEDAS:
            h = int.from_bytes(hashlib.sha256(self.semente + m.encode()).digest()[:4], "big")
            fator = 0.9 + (h % 20000) / 100000
            out.append({"moeda": m, "valor": f"{BASE[m] * fator:.4f}"})
        return out


ESTADO = Estado()


class Handler(BaseHTTPRequestHandler):
    def _json(self, status, obj):
        corpo = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(corpo)))
        self.end_headers()
        self.wfile.write(corpo)

    def do_GET(self):
        url = urlparse(self.path)
        if url.path == "/token":
            tok = secrets.token_hex(8)
            ESTADO.tokens.add(tok)
            return self._json(200, {"token": tok})
        if url.path == "/cotacoes":
            tok = parse_qs(url.query).get("token", [""])[0]
            if tok not in ESTADO.tokens:
                return self._json(403, {"erro": "token inválido ou de outra instância"})
            cot = ESTADO.cotacoes()
            csv = "moeda;valor\n" + "".join(f"{c['moeda']};{c['valor']}\n" for c in sorted(cot, key=lambda c: c["moeda"]))
            auditar(f"served {os.getpid()} {hashlib.sha256(csv.encode()).hexdigest()}")
            return self._json(200, {"base": "BRL", "cotacoes": cot})
        return self._json(404, {"erro": "não encontrado"})

    def log_message(self, fmt, *args):
        sys.stderr.write("[servidor] " + (fmt % args) + "\n")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--porta", type=int, required=True)
    args = p.parse_args()
    try:
        srv = ThreadingHTTPServer(("127.0.0.1", args.porta), Handler)
    except OSError as e:
        print(f"erro: não consegui abrir a porta {args.porta}: {e}", file=sys.stderr)
        return 1
    auditar(f"start {os.getpid()}")

    def parar(signum, frame):
        threading.Thread(target=srv.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, parar)
    signal.signal(signal.SIGINT, parar)
    print(f"ouvindo em http://127.0.0.1:{srv.server_address[1]}", flush=True)
    try:
        srv.serve_forever()
    finally:
        srv.server_close()
        auditar(f"stop {os.getpid()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
