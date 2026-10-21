"""Conta métodos test_* em tests/*.py (via ast) e diz se algum menciona prioridade."""
import ast
import pathlib
import sys

total = 0
prio = 0
for p in sorted(pathlib.Path(sys.argv[1]).glob("test*.py")):
    src = p.read_text(encoding="utf-8")
    for node in ast.walk(ast.parse(src)):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name.startswith("test"):
            total += 1
            if "prioridade" in ast.get_source_segment(src, node):
                prio += 1
print(total, prio)
