#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
exit 0
