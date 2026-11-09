#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
go build -o grita . >/dev/null 2>&1 || fail "go build -o grita . falha"
[ "$(./grita maria)" = "Ola, maria!" ] || fail "saida normal mudou"
[ "$(./grita)" = "Ola, mundo!" ] || fail "default mudou"
[ "$(./grita --grite maria)" = "OLA, MARIA!" ] || fail "--grite maria nao imprime OLA, MARIA!"
[ "$(./grita --grite)" = "OLA, MUNDO!" ] || fail "--grite sem nome nao imprime OLA, MUNDO!"
exit 0
