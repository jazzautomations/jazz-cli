#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
./backup.sh >/dev/null 2>&1 || fail "./backup.sh falha"
[ -f "backup/nota1.txt" ] && [ -f "backup/nota 2.txt" ] || fail "backup nao copiou os dois txt"
[ ! -f "backup/rascunho.md" ] || fail "copiou rascunho.md (nao devia)"
./backup.sh 2>/dev/null | grep -q "copiado: docs tutoriais/nota 2.txt" || fail "linha do arquivo com espaco nao aparece"
exit 0
