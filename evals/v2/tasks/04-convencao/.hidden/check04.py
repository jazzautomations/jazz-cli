"""Tarefa 04: o comando novo segue as convenções da caixa (descobertas lendo o código)."""
import ast
import json
import os
import subprocess
import sys
import tempfile

T = sys.argv[1]


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


def caixa(*args, entrada=""):
    p = subprocess.run([sys.executable, "-m", "caixa", *args], input=entrada, capture_output=True,
                       text=True, cwd=os.getcwd(), timeout=60)
    return p.returncode, p.stdout, p.stderr


# 1) módulo próprio em caixa/comandos/, registrado com @registrar("contar") em executar()
caminho = os.path.join("caixa", "comandos", "contar.py")
if not os.path.isfile(caminho):
    fail("comando não está em caixa/comandos/contar.py (convenção: um módulo por comando)")
arvore = ast.parse(open(caminho, encoding="utf-8").read())
ok = False
for n in arvore.body:
    if isinstance(n, ast.FunctionDef) and n.name == "executar":
        for d in n.decorator_list:
            if (isinstance(d, ast.Call) and getattr(d.func, "id", getattr(d.func, "attr", "")) == "registrar"
                    and d.args and isinstance(d.args[0], ast.Constant) and d.args[0].value == "contar"):
                ok = ok or bool(ast.get_docstring(n))
if not ok:
    fail("contar.py não define executar() com docstring e @registrar(\"contar\")")
src = open(caminho, encoding="utf-8").read()
if "saida.resultado" not in src or "saida.erro" not in src:
    fail("contar.py não usa saida.resultado/saida.erro")

# 2) framework intocado (só comandos/__init__.py podia mudar)
for rel in ("caixa/saida.py", "caixa/arquivos.py", "caixa/registro.py", "caixa/__main__.py",
            "caixa/comandos/maiusculas.py", "caixa/comandos/unicas.py", "caixa/comandos/hash.py",
            "caixa/comandos/frequencia.py"):
    if open(os.path.join(T, rel), "rb").read() != open(rel, "rb").read():
        fail(f"{rel} foi alterado (não era preciso mexer no framework)")
for rel in os.listdir(os.path.join(T, "tests")):
    if rel.endswith(".py") and open(os.path.join(T, "tests", rel), "rb").read() != open(os.path.join("tests", rel), "rb").read():
        fail(f"tests/{rel} foi alterado")

# 3) comportamento
d = tempfile.mkdtemp(prefix="chk04-", dir=os.path.dirname(os.getcwd()))
f = os.path.join(d, "x.txt")
with open(f, "w", encoding="utf-8") as fh:
    fh.write("um dois\ntres\n")
esperado = {"linhas": 2, "palavras": 3, "caracteres": 13}
rc, out, err = caixa("contar", f)
if rc != 0:
    fail(f"contar saiu com {rc}: {err.strip()}")
if sorted(out.splitlines()) != sorted(f"{k}: {v}" for k, v in esperado.items()):
    fail(f"saída em texto fora do padrão 'chave: valor': {out!r}")
rc, out, err = caixa("--json", "contar", f)
try:
    if json.loads(out) != esperado:
        fail(f"--json errado: {out!r}")
except ValueError:
    fail(f"--json não é JSON: {out!r}")
rc, out, err = caixa("--json", "contar", "-", entrada="a b c\n")
try:
    if rc != 0 or json.loads(out) != {"linhas": 1, "palavras": 3, "caracteres": 6}:
        fail(f"'-' (entrada padrão) não funciona: rc={rc} {out!r} {err.strip()}")
except ValueError:
    fail(f"'-' (entrada padrão) não funciona: {out!r} {err.strip()}")
falta = os.path.join(d, "nao-existe.txt")
rc, out, err = caixa("contar", falta)
if rc != 2 or err != f"caixa: erro: arquivo não encontrado: {falta}\n":
    fail(f"erro de arquivo inexistente fora do padrão: rc={rc} stderr={err!r}")
rc, out, _ = caixa("ajuda")
linhas = [l for l in out.splitlines() if l.split()[:1] == ["contar"]]
if not linhas or len(linhas[0].split()) < 2:
    fail("contar não aparece na ajuda com resumo")

# 4) teste no padrão (tests/test_contar.py usando tests.util.rodar) e suíte verde
tp = os.path.join("tests", "test_contar.py")
if not os.path.isfile(tp) or "rodar(" not in open(tp, encoding="utf-8").read():
    fail("falta tests/test_contar.py usando rodar() de tests/util.py")
p = subprocess.run([sys.executable, "-m", "unittest", "-q"], capture_output=True, text=True, timeout=120)
if p.returncode != 0:
    fail("python3 -m unittest falha")
print("PASS")
