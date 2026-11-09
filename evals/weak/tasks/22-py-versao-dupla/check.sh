#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
grep -q "1.8.2" versao.py || fail "versao.py sem 1.8.2"
grep -q "Instalado: v1.8.2" README.md || fail "README sem Instalado: v1.8.2"
if grep -q "1.4.0" versao.py README.md; then fail "sobrou 1.4.0"; fi
python3 -m unittest test_versao >/dev/null 2>&1 || python3 -m unittest >/dev/null 2>&1 || fail "unittest falha"
exit 0
