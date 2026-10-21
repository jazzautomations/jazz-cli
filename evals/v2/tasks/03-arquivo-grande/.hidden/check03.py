"""Tarefa 03: só PedidoAtacado.aplicar_desconto e .validar_quantidade podem mudar (comparação por AST)."""
import ast
import os
import sys

ALVO = "PedidoAtacado"
METODOS = {"aplicar_desconto", "validar_quantidade"}


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


orig = ast.parse(open(sys.argv[1], encoding="utf-8").read())
try:
    novo = ast.parse(open("pedidos.py", encoding="utf-8").read())
except SyntaxError as e:
    fail(f"pedidos.py não compila: {e}")


def top(tree):
    out = []
    for n in tree.body:
        nome = getattr(n, "name", None)
        out.append((nome, n))
    return out


to, tn = top(orig), top(novo)
if [k for k, _ in to] != [k for k, _ in tn]:
    fail("estrutura de nível superior mudou (classes/funções adicionadas, removidas ou reordenadas)")
for (nome, a), (_, b) in zip(to, tn):
    if nome != ALVO:
        if ast.dump(a) != ast.dump(b):
            fail(f"{nome or type(a).__name__} foi alterado e não podia")
        continue
    ma = [(getattr(x, "name", None), x) for x in a.body]
    mb = [(getattr(x, "name", None), x) for x in b.body]
    if [k for k, _ in ma] != [k for k, _ in mb]:
        fail("membros de PedidoAtacado adicionados/removidos/reordenados")
    for (m, x), (_, y) in zip(ma, mb):
        if m not in METODOS and ast.dump(x) != ast.dump(y):
            fail(f"PedidoAtacado.{m} foi alterado e não podia")

sys.path.insert(0, os.getcwd())
import pedidos  # noqa: E402

P = pedidos.PedidoAtacado
p = P("x")
p.adicionar_item("A", 100.0, 10)
if p.aplicar_desconto(40) != 300.0:
    fail(f"desconto de 40% deveria travar em 30% (300.0), deu {p.desconto}")
if p.aplicar_desconto(30) != 300.0 or p.aplicar_desconto(20) != 200.0:
    fail("descontos até 30% deveriam valer integralmente")
try:
    p.aplicar_desconto(-1)
    fail("desconto negativo deveria dar erro")
except ValueError:
    pass
for q in (1, 9):
    try:
        P("y").adicionar_item("B", 1.0, q)
        fail(f"quantidade {q} deveria dar erro")
    except ValueError as e:
        if str(e) != "quantidade mínima é 10 unidades":
            fail(f"mensagem errada: {e!r}")
P("z").adicionar_item("B", 1.0, 10)
try:
    P("w").adicionar_item("B", 1.0, 10.0)
    fail("quantidade não inteira deveria dar TypeError")
except TypeError:
    pass
# os outros tipos continuam com 50% e mínimo 1 (garantido pelo AST, mas confere)
for nome in ("PedidoAtacadoLegado", "PedidoVarejo", "PedidoDoacao"):
    q = getattr(pedidos, nome)("x")
    q.adicionar_item("A", 100.0, 1)
    if q.aplicar_desconto(60) != 50.0:
        fail(f"{nome} perdeu o teto de 50%")
print("PASS")
