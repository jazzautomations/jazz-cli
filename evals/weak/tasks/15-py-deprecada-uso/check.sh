#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
python3 -m unittest test_banco >/dev/null 2>&1 || fail "test_banco falha"
if grep -rnE "(^|[^_a-zA-Z])registra\(" --include="*.py" . >/dev/null 2>&1; then fail "sobrou registra("; fi
exit 0
