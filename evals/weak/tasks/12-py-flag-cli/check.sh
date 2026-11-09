#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
python3 safra.py a b c > o1 2>/dev/null || fail "safra.py da erro"
[ "$(cat o1)" = "a,b,c" ] || fail "sem flag mudou: $(cat o1)"
python3 safra.py --sep + a b > o2 2>/dev/null || fail "erro no --sep"
[ "$(cat o2)" = "a+b" ] || fail "--sep mudou: $(cat o2)"
python3 safra.py --maiusculas a b c > o3 2>/dev/null || fail "erro no --maiusculas"
[ "$(cat o3)" = "A,B,C" ] || fail "--maiusculas: $(cat o3)"
python3 safra.py --sep + --maiusculas a b > o4 2>/dev/null || fail "erro nas duas flags"
[ "$(cat o4)" = "A+B" ] || fail "duas flags: $(cat o4)"
rm -f o1 o2 o3 o4
exit 0
