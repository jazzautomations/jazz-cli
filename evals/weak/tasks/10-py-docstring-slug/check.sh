#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
python3 -m unittest test_slug >/dev/null 2>&1 || fail "test_slug falha"
exit 0
