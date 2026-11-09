#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
make test >/dev/null 2>&1 || fail "make test falha (python3?)"
grep -q "python3" Makefile || fail "Makefile sem python3"
if grep -q "python2" Makefile; then fail "Makefile ainda tem python2"; fi
grep -q "python2" rascunho.txt || true
diff -q Makefile "$T/Makefile" >/dev/null && fail "Makefile intocado"
exit 0
