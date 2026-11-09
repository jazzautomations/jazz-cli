#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
python3 -m unittest test_cupom >/dev/null 2>&1 || python3 -m unittest >/dev/null 2>&1 || fail "python3 -m unittest falha"
diff -q test_cupom.py "$T/test_cupom.py" >/dev/null || fail "test_cupom.py foi mexido"
diff -q cupom.py "$T/cupom.py" >/dev/null && fail "cupom.py intocado — era o codigo a consertar"
exit 0
