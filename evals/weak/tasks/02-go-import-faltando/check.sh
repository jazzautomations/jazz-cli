#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go test ./... >/dev/null 2>&1 || fail "go test ./... nao compila/passa"
grep -q "strings\." texto.go || fail "uso de strings sumiu?"
exit 0
