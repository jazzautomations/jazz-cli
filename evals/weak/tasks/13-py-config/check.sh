#!/bin/bash
set -u
T="${TASK:?TASK nao definido}"
fail() { echo "FALHOU: $*"; exit 1; }
out=$(python3 app.py) || fail "python3 app.py erro"
[ "$out" = "180" ] || fail "app.py imprimiu: $out"
python3 -c 'import json,sys; c=json.load(open("settings.json")); sys.exit(0 if (c.get("timeout")==180 and c.get("host")=="localhost" and c.get("porta")==5000) else 1)' || fail "settings.json incompleto"
exit 0
