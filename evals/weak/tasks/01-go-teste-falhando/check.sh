#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
diff -q soma_test.go "$T/soma_test.go" >/dev/null || fail "soma_test.go foi mexido"
diff -q soma.go "$T/soma.go" >/dev/null && fail "soma.go intocado — era o codigo a consertar"
exit 0
