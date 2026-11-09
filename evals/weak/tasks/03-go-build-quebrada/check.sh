#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go build ./... >/dev/null 2>&1 || fail "go build ./... nao compila"
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
diff -q r_test.go "$T/r_test.go" >/dev/null || fail "r_test.go foi mexido"
exit 0
