#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
[ -f par_test.go ] || fail "par_test.go nao existe"
grep -q "TestPar" par_test.go || fail "sem TestPar no par_test.go"
grep -q "par(" par_test.go || fail "o teste nem chama par()"
go test ./... >/dev/null 2>&1 || fail "go test ./... falha"
cp par.go par.go.antibak
sed -i "s/return n%2 == 0/return n%2 == 1/" par.go
if go test ./... >/dev/null 2>&1; then mv par.go.antibak par.go; fail "teste passa mesmo com par() invertido — teste fraco"; fi
mv par.go.antibak par.go
exit 0
