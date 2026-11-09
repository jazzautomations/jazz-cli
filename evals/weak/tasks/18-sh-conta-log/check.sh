#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
[ -s erros.log ] || fail "erros.log vazio ou ausente"
[ "$(wc -l < erros.log)" = "3" ] || fail "erros.log tem $(wc -l < erros.log) linhas, esperava 3"
grep -q "\[ERRO\] fila cheia" erros.log || fail "erros.log sem a linha da fila"
grep -q "\[OK\]" erros.log && fail "erros.log tem linha OK"
[ "$(tr -d "\r" < total.txt | tail -1)" = "erros=3" ] || fail "total.txt = $(cat total.txt)"
exit 0
