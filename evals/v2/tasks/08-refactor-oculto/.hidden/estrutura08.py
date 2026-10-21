"""Tarefa 08: as funções auxiliares saíram dos três módulos e vivem em pagamentos/comum.py."""
import ast
import sys

AUX = {"normalizar_documento", "validar_cpf", "formatar_cpf", "formatar_reais", "validar_valor"}


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


try:
    comum = ast.parse(open("pagamentos/comum.py", encoding="utf-8").read())
except OSError:
    fail("pagamentos/comum.py não existe")
defs_comum = {n.name for n in ast.walk(comum) if isinstance(n, ast.FunctionDef)}
for nome in ("normalizar_documento", "validar_cpf", "formatar_cpf", "formatar_reais"):
    if nome not in defs_comum:
        fail(f"comum.py não define {nome}")
for mod in ("boleto", "pix", "cartao"):
    src = open(f"pagamentos/{mod}.py", encoding="utf-8").read()
    arv = ast.parse(src)
    dup = sorted(n.name for n in ast.walk(arv) if isinstance(n, ast.FunctionDef) and n.name in AUX)
    if dup:
        fail(f"{mod}.py ainda define {dup}")
    usa = any(isinstance(n, ast.ImportFrom) and (n.module or "").endswith("comum") for n in ast.walk(arv)) or \
        any(isinstance(n, ast.ImportFrom) and any(a.name == "comum" for a in n.names) for n in ast.walk(arv))
    if not usa:
        fail(f"{mod}.py não importa de comum")
    if "def validar_cpf" in src or src.count("% 11 % 10"):
        fail(f"{mod}.py ainda tem lógica de CPF copiada")
print("ok: estrutura")
