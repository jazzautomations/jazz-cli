#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
grep -rE "CalcPreco" --include="*.go" . >/dev/null && fail "sobrou CalcPreco em algum .go"
grep -q "func CalcularPreco" catalogo.go || fail "funcao nova nao existe"
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
go build ./... >/dev/null 2>&1 || fail "go build ./... falha"
diff -q catalogo_test.go "$T/catalogo_test.go" >/dev/null || fail "catalogo_test.go foi mexido"
exit 0
