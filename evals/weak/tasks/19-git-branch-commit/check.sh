#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
git rev-parse --verify melhorias >/dev/null 2>&1 || fail "branch melhorias nao existe"
[ "$(git show melhorias:versao.txt)" = "2.0 FINAL" ] || fail "versao.txt em melhorias: $(git show melhorias:versao.txt 2>/dev/null)"
msg=$(git log -1 --format=%s melhorias); [ "$msg" = "versao 2.0 FINAL" ] || fail "mensagem do commit: $msg"
[ "$(git rev-list --count melhorias)" = "2" ] || fail "esperava 2 commits em melhorias"
[ "$(git show melhorias~1:versao.txt)" = "1.5" ] || fail "o commit mexeu no estado anterior"
git show --format= --name-only melhorias | grep -v "^$" | grep -q "versao.txt" || fail "commit sem versao.txt"
outras=$(git show --format= --name-only melhorias | grep -v "^$" | grep -cv "versao.txt"); [ "$outras" = "0" ] || fail "commit mexeu em outros arquivos"
exit 0
