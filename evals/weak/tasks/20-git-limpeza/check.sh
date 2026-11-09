#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
git log --format=%s -1 | grep -q "^limpa logs$" || fail "ultimo commit: $(git log --format=%s -1)"
[ -f logs.txt ] || fail "logs.txt apagado do disco (era pra seguir la)"
git ls-files | grep -q "^logs.txt$" && fail "logs.txt ainda rastreado"
grep -q "logs.txt" .gitignore || fail ".gitignore sem logs.txt"
git check-ignore -q logs.txt || fail "logs.txt nao esta sendo ignorado"
git ls-files | grep -q "^mudanca.txt$" || fail "mudanca.txt sumiu do indice"
exit 0
