#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
diff -q moeda.go "$T/moeda.go" >/dev/null && fail "moeda.go intocado — o conserto e centralizado nele"
diff -q estq_test.go "$T/estq_test.go" >/dev/null || fail "estq_test.go foi mexido"
diff -q produto.go "$T/produto.go" >/dev/null || fail "produto.go foi mexido (era pra ser sem tocar nas chamadas)"
exit 0
