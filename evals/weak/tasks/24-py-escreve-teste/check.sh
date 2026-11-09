#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
[ -f test_palindromo.py ] || fail "test_palindromo.py nao existe"
grep -q "eh_palindromo" test_palindromo.py || fail "o teste nem usa eh_palindromo"
python3 -B -m unittest -v >/dev/null 2>&1 || fail "python3 -m unittest falha"
cp palindromo.py palindromo.py.antibak
sed -i "s/limpo == limpo/limpo != limpo/" palindromo.py
rm -rf __pycache__
if python3 -B -m unittest >/dev/null 2>&1; then mv palindromo.py.antibak palindromo.py; fail "teste passa com eh_palindromo invertido — teste fraco"; fi
mv palindromo.py.antibak palindromo.py
exit 0
