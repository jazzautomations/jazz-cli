import inspect
import re
import sys
import tempfile
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path.cwd()))
from carteira import arquivo


def fail(message):
    print("FAIL:", message)
    raise SystemExit(1)


source = inspect.getsource(arquivo)
if re.search(r"(?m)^\s*(?:import\s+json\b|from\s+json\b)", source):
    fail("carteira.arquivo deve usar fastjson, não json da biblioteca padrão")

with tempfile.TemporaryDirectory() as d:
    path = Path(d) / "entrada.json"
    path.write_text('{"saldo":10.50,"ativos":[{"qtd":2,"preco":1.25}],"marcador":10.50}', encoding="utf-8")
    got = arquivo.carregar(path)
    if got != {"saldo": Decimal("10.50"), "ativos": [{"qtd": 2, "preco": Decimal("1.25")}], "marcador": Decimal("10.50")}:
        fail(f"carregar devolveu valores errados: {got!r}")
    if type(got["ativos"][0]["qtd"]) is not int:
        fail("inteiros devem continuar int")
    out = Path(d) / "saida.json"
    arquivo.salvar({"saldo": Decimal("10.50"), "ativos": [{"qtd": 2, "preco": Decimal("1.25")}]}, out)
    want = b'{\n  "ativos": [\n    {\n      "preco": 1.25,\n      "qtd": 2\n    }\n  ],\n  "saldo": 10.50\n}\n'
    if out.read_bytes() != want:
        fail(f"salvar divergiu de JSON ordenado/indentado ou perdeu escala: {out.read_bytes()!r}")

print("ok")
