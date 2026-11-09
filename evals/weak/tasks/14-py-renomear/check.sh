#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
python3 -m unittest test_folha >/dev/null 2>&1 || fail "test_folha falha"
if grep -rn "sal_liquido" --include="*.py" . >/dev/null 2>&1; then fail "sobrou sal_liquido"; fi
exit 0
