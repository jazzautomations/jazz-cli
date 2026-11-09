#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go build -o pingui . >/dev/null 2>&1 || fail "go build falha"
out=$(./pingui)
[ "$out" = "servidor na porta 9095" ] || fail "saida de ./pingui: $out"
grep -q "Porta      = 9095" config.go && grep -q "MostrarLog = false" config.go || fail "config.go sem os valores pedidos"
diff -q main.go "$T/main.go" >/dev/null || fail "main.go foi mexido"
exit 0
