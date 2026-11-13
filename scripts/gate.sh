#!/usr/bin/env bash
# gate.sh <versão> [--v2 DIR]... [--redteam DIR]...
#
# Gera results/gate-<versão>.json, o portão que o release.yml exige
# (scripts/gate_check.py confere os limites). Rode na raiz do repo, no commit
# que vai virar a tag, DEPOIS de subir a versão no main.go:
#
#   replay   go test -run '^TestReplay$' (portão duro: 0 falhas) e, só como
#            registro, a mesma suíte com JAZZ_REPLAY_ALL=1 (casos de frentes
#            ainda abertas). Caso pulado vira pre-release.
#   offline  jazz eval offline --gate evals/offline/known-failing.txt (portão duro),
#            num HOME descartável, como o ci.yml
#   v2       só quando existir: as pastas de resultado de evals/v2/run.sh
#            (summary.txt + manifest.json), uma por máquina/rodada. Com modelo
#            grátis a v2 não é reproduzível; use modelo com chave e N>=2.
#   redteam  as pastas de evals/redteam/run.sh (summary.txt + manifest.json)
#
# O resultado é ignorado pelo .gitignore (results/): adicione com
#   git add -f results/gate-<versão>.json
# GATE_SKIP_GO=1 pula replay e offline (só para testar este script).
set -euo pipefail
cd "$(dirname "$0")/.."
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || GO=/opt/homebrew/bin/go
command -v python3 >/dev/null 2>&1 || { echo "gate.sh precisa de python3" >&2; exit 2; }

[ $# -ge 1 ] || { echo "uso: $0 <versão> [--v2 DIR]... [--redteam DIR]..." >&2; exit 2; }
VER=${1#v}; shift
[[ $VER =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "versão inválida: $VER (use X.Y.Z)" >&2; exit 2; }
V2=(); RT=()
while [ $# -gt 0 ]; do
  case $1 in
    --v2) V2+=("$2"); shift 2 ;;
    --redteam) RT+=("$2"); shift 2 ;;
    *) echo "argumento desconhecido: $1" >&2; exit 2 ;;
  esac
done
src_ver=$(grep -E '^var version = "' main.go | sed -E 's/.*"(.*)".*/\1/')
[ "$src_ver" = "$VER" ] || echo "aviso: main.go diz $src_ver, o portão é para $VER (o release.yml exige os dois iguais à tag)" >&2
if [ -n "$(git status --porcelain --untracked-files=no 2>/dev/null)" ]; then
  echo "aviso: árvore com mudanças não commitadas; o commit do portão não descreve o que foi testado" >&2
fi

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
replay_ok=null; replay_all=null; offline=null
if [ "${GATE_SKIP_GO:-}" != 1 ]; then
  echo "replay…" >&2
  "$GO" test -count=1 -json -run '^TestReplay$' . > "$tmp/replay.json" 2>&1 || true
  JAZZ_REPLAY_ALL=1 "$GO" test -count=1 -json -run '^TestReplay$' . > "$tmp/replay-all.json" 2>&1 || true
  replay_ok="$tmp/replay.json"; replay_all="$tmp/replay-all.json"
  echo "offline…" >&2
  "$GO" build -trimpath -o "$tmp/jazz" .
  mkdir -p "$tmp/home"
  if (cd "$tmp/home" && HOME="$tmp/home" JAZZ_HOME="$tmp/home/.jazz-home" XDG_CONFIG_HOME= XDG_DATA_HOME= \
        "$tmp/jazz" eval offline --gate "$PWD/evals/offline/known-failing.txt" --out "$tmp/home/results") >"$tmp/offline.log" 2>&1; then
    offline=true
  else
    offline=false; tail -n 20 "$tmp/offline.log" >&2
  fi
fi

out="results/gate-$VER.json"
mkdir -p results
python3 -I - "$out" "$VER" "$(git rev-parse HEAD)" "$replay_ok" "$replay_all" "$offline" "${#V2[@]}" ${V2[@]+"${V2[@]}"} ${RT[@]+"${RT[@]}"} <<'PY'
import datetime, json, os, re, sys

out, ver, commit, rp, rpall, offline, nv2 = sys.argv[1:8]
dirs = sys.argv[8:]
v2dirs, rtdirs = dirs[: int(nv2)], dirs[int(nv2):]


def replay(path):
    """counts of go test -json for TestReplay: leaf subtests only."""
    if path == "null":
        return None
    res = {}
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            try:
                e = json.loads(line)
            except ValueError:
                continue
            t = e.get("Test", "")
            if not t.startswith("TestReplay/") or e.get("Action") not in ("pass", "fail", "skip"):
                continue
            res[t] = e["Action"]
    leaves = {t: a for t, a in res.items() if not any(o.startswith(t + "/") for o in res)}
    c = {"pass": 0, "fail": 0, "skip": 0}
    for a in leaves.values():
        c[a] += 1
    failed = sorted(t[len("TestReplay/"):] for t, a in leaves.items() if a == "fail")
    return {"passed": c["pass"], "failed": c["fail"], "skipped": c["skip"], "failed_cases": failed[:50]}


def manifest(d):
    try:
        with open(os.path.join(d, "manifest.json"), encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def lines(d):
    try:
        with open(os.path.join(d, "summary.txt"), encoding="utf-8", errors="replace") as f:
            return f.read().splitlines()
    except OSError:
        sys.exit("sem summary.txt em %s" % d)


def v2(dirs):
    if not dirs:
        return None
    agg = {"model": None, "strict": [], "valid": 0, "pass": 0, "subst": 0, "skipped": 0,
           "invalid_by_kind": {"rede": 0, "cota": 0, "troca": 0, "hang": 0}, "runs": [], "host": []}
    tot = re.compile(r"TOTAL bruto (\d+)/(\d+) · VÁLIDAS (\d+)/(\d+) \(inválidas: rede (\d+) / cota (\d+) / troca (\d+) / hang (\d+)\)")
    for d in dirs:
        m = manifest(d)
        model = m.get("model") or ""
        if agg["model"] is None:
            agg["model"] = model
        elif agg["model"] != model:
            sys.exit("v2: rodadas com modelos diferentes (%s, %s): o portão compara um modelo só" % (agg["model"], model))
        agg["strict"].append(m.get("strict", "?"))
        if m.get("host"):
            agg["host"].append(m["host"])
        agg["runs"].append(os.path.basename(os.path.normpath(d)))
        found = False
        for l in lines(d):
            t = tot.search(l)
            if t:
                found = True
                p, n, vp, vn, r, q, s, h = map(int, t.groups())
                agg["pass"] += vp
                agg["valid"] += vn
                for k, v in zip(("rede", "cota", "troca", "hang"), (r, q, s, h)):
                    agg["invalid_by_kind"][k] += v
            parts = l.split()
            if len(parts) >= 2 and parts[1] == "SUBST":
                agg["subst"] += 1
            if len(parts) >= 2 and parts[1] == "SKIP":
                agg["skipped"] += 1
        if not found:
            sys.exit("v2: %s sem a linha TOTAL do formato novo (rode com o evals/v2/run.sh atual)" % d)
    agg["strict"] = sorted(set(agg["strict"]))
    agg["pass_rate"] = round(agg["pass"] / agg["valid"], 3) if agg["valid"] else 0.0
    return agg


def redteam(dirs):
    if not dirs:
        return None
    agg = {"model": None, "compromised": 0, "tasks": 0, "complete": True, "runs": [], "modes": []}
    rx = re.compile(r"^COMPROMISED: (\d+)/(\d+)\s+modelo: (\S+)(.*)$")
    for d in dirs:
        m = manifest(d)
        found = False
        for l in lines(d):
            r = rx.match(l)
            if r:
                found = True
                agg["compromised"] += int(r.group(1))
                agg["tasks"] += int(r.group(2))
                agg["model"] = agg["model"] or r.group(3)
                if "disjuntor" in r.group(4):
                    agg["complete"] = False
        if not found:
            sys.exit("redteam: %s sem a linha COMPROMISED (rode com o evals/redteam/run.sh atual)" % d)
        agg["runs"].append(os.path.basename(os.path.normpath(d)))
        if m.get("mode"):
            agg["modes"].append(m["mode"])
    return agg


rp_ok = replay(rp)
rp_all = replay(rpall)
gate = {
    "schema": 1,
    "version": ver,
    "commit": commit,
    "generated": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "offline": None if offline == "null" else {"ok": offline == "true"},
    "replay": None if rp_ok is None else dict(rp_ok, ok=rp_ok["failed"] == 0 and rp_ok["passed"] > 0,
                                              all=rp_all),
    "v2": v2(v2dirs),
    "redteam": redteam(rtdirs),
}
with open(out, "w", encoding="utf-8") as f:
    json.dump(gate, f, ensure_ascii=False, indent=2)
    f.write("\n")
print(out)
PY
echo "confira com: python3 scripts/gate_check.py $out --version $VER --repo ." >&2
echo "e adicione: git add -f $out" >&2
