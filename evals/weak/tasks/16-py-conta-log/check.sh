#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
[ -f total.txt ] || fail "total.txt nao existe"
[ "$(tr -d "\r" < total.txt | tail -1)" = "ERROS=7" ] || fail "total.txt = $(cat total.txt), esperava ERROS=7"
exit 0
