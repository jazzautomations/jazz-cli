# solução de referência: sobe em porta livre em segundo plano, consulta, grava e para.
set -e
python3 servidor.py --porta 0 > srv.out 2> srv.err &
pid=$!
for i in $(seq 1 50); do grep -q 'ouvindo em' srv.out 2>/dev/null && break; sleep 0.2; done
url=$(sed -n 's/^ouvindo em //p' srv.out)
python3 - "$url" <<'PY'
import json, sys, urllib.request
base = sys.argv[1]
tok = json.load(urllib.request.urlopen(base + "/token"))["token"]
cot = json.load(urllib.request.urlopen(base + "/cotacoes?token=" + tok))["cotacoes"]
with open("cotacoes.csv", "w") as f:
    f.write("moeda;valor\n")
    for c in sorted(cot, key=lambda c: c["moeda"]):
        f.write(f"{c['moeda']};{c['valor']}\n")
PY
kill "$pid"; wait "$pid" || true
