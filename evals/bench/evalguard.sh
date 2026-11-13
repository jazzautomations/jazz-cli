#!/bin/bash
# evalguard.sh: as travas que fazem um eval medir o JAZZ e não a cota do pool
# grátis (F0 do plano 0.14). Compartilhado por evals/v2/run.sh,
# evals/redteam/run.sh e evals/bench/lib.sh. Precisa de $JAZZ (o binário).
#
#   eg_lock DIR            uma rodada por máquina (mkdir DIR/.lock); falha se já há outra
#   eg_unlock
#   eg_home                JAZZ_HOME novo (mktemp, 700) só com config.json e auth.json (600);
#                          exporta JAZZ_PLAN_CACHE=off JAZZ_LESSONS=off JAZZ_NO_UPDATE_CHECK=1:
#                          nada da rodada vira plano/lição no HOME de verdade
#   eg_home_cleanup [DIR]  move as sessões da rodada para DIR/sessions e apaga o JAZZ_HOME
#                          (que tem as credenciais)
#   eg_strict MODEL        prende o modelo: --strict-model quando o jazz tem (EG_STRICT), e
#                          sempre JAZZ_STRICT_MODEL=1 + fallback [MODEL] e omni off no config
#                          isolado (o paliativo de antes do F1). EG_STRICT_HOW diz qual valeu.
#   eg_metrics FILE        imprime "OK REQ ERR MODELOS" de um --metrics-out (MODELOS = os que
#                          serviram request OK, separados por vírgula; "-" = nenhum)
#   eg_preflight MODEL DIR OUT
#                          1 pedido mínimo antes de queimar tarefas; volta 1 com "modelo
#                          indisponível" (ou "modelo trocado") quando o modelo não responde
#   eg_dead FILE           verdadeiro quando a tarefa terminou com 0 request OK (disjuntor)
#   eg_manifest FILE [k=v ...]
#                          manifest.json: versão, sha do binário, commit dos evals, host,
#                          uname, python, modelo, modo de trava e as chaves JAZZ_* ativas
#                          (valor de chave com cara de segredo vira [REDACTED])
#
# python3 é usado para ler/gravar JSON; sem ele a rodada segue sem trava de
# config e sem leitura de métricas (com aviso).

eg_tmo() { local k=$1 secs=$2; shift 2; if command -v timeout >/dev/null 2>&1; then timeout -k "$k" "$secs" "$@"; elif command -v gtimeout >/dev/null 2>&1; then gtimeout -k "$k" "$secs" "$@"; else perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; fi; }
eg_py() { command -v python3 >/dev/null 2>&1; }

EG_LOCK=
eg_lock() {
  mkdir -p "$1" || return 1
  if mkdir "$1/.lock" 2>/dev/null; then
    EG_LOCK="$1/.lock"
    echo "pid $$ desde $(date '+%Y-%m-%d %H:%M:%S') em $(hostname 2>/dev/null)" > "$EG_LOCK/owner"
    return 0
  fi
  echo "outra rodada de eval já está rodando nesta máquina ($(cat "$1/.lock/owner" 2>/dev/null || echo '?')):" >&2
  echo "  duas rodadas no mesmo pool dividem a cota e invalidam as duas. Se ela morreu, apague $1/.lock" >&2
  return 1
}
eg_unlock() { [ -z "$EG_LOCK" ] || rm -rf "$EG_LOCK"; EG_LOCK=; }

EG_HOME=
eg_home() {
  local src="${JAZZ_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/jazz-cli}" f
  EG_HOME=$(mktemp -d "${TMPDIR:-/tmp}/jazz-eval-home.XXXXXX") || return 1
  chmod 700 "$EG_HOME"
  for f in config.json auth.json; do
    if [ -f "$src/$f" ]; then cp "$src/$f" "$EG_HOME/$f" && chmod 600 "$EG_HOME/$f"; fi
  done
  export JAZZ_HOME="$EG_HOME" JAZZ_PLAN_CACHE=off JAZZ_LESSONS=off JAZZ_NO_UPDATE_CHECK=1
}
eg_home_cleanup() {
  [ -n "$EG_HOME" ] && [ -d "$EG_HOME" ] || return 0
  if [ -n "${1:-}" ] && [ -d "$EG_HOME/data/sessions" ]; then
    mkdir -p "$1" && cp -R "$EG_HOME/data/sessions" "$1/" 2>/dev/null
  fi
  rm -rf "$EG_HOME"; EG_HOME=
}

EG_STRICT=; EG_STRICT_HOW=none
eg_strict() {
  local model=${1:-}
  EG_STRICT=; EG_STRICT_HOW=none
  [ -n "$model" ] || return 0
  export JAZZ_STRICT_MODEL=1
  if "$JAZZ" --help 2>&1 | grep -q -- '--strict-model'; then EG_STRICT=--strict-model; EG_STRICT_HOW=flag; fi
  if ! eg_py || [ -z "$EG_HOME" ]; then
    [ "$EG_STRICT_HOW" = flag ] || { EG_STRICT_HOW=check-only; echo "aviso: sem python3 o modelo não fica preso no config; só a checagem SUBST das métricas vale" >&2; }
    return 0
  fi
  python3 -I - "$EG_HOME/config.json" "$model" <<'PY' || return 1
import json, os, sys
p, m = sys.argv[1], sys.argv[2]
c = {}
if os.path.exists(p):
    with open(p, encoding="utf-8") as f:
        c = json.load(f)
c["fallback"] = [m]  # só o modelo pedido: nenhuma cadeia grátis implícita entra
o = c.get("omni")
if not isinstance(o, dict):
    o = {}
o["enabled"] = False
c["omni"] = o
fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as f:
    json.dump(c, f, indent=2)
PY
  [ "$EG_STRICT_HOW" = flag ] && EG_STRICT_HOW=flag+config || EG_STRICT_HOW=config
}

eg_metrics() {
  if [ ! -r "${1:-}" ] || ! eg_py; then echo "? ? ? ?"; return 0; fi
  python3 -I - "$1" <<'PY'
import json, sys
try:
    m = json.load(open(sys.argv[1], encoding="utf-8"))
except (OSError, ValueError):
    print("? ? ? ?"); sys.exit(0)
req, err = int(m.get("requests") or 0), int(m.get("request_errors") or 0)
models = sorted(k for k, v in (m.get("models") or {}).items() if k and v)
print(req - err, req, err, ",".join(models) or "-")
PY
}

# eg_served_other MODEL MODELS: prints the served models that are not MODEL ("" = none)
eg_served_other() {
  local model=$1 list=$2 m out=
  [ -n "$model" ] && [ "$list" != "-" ] && [ "$list" != "?" ] || return 0
  local IFS=,
  # a bare provider in -m ("-m gemini") resolves to its default model: same provider = asked for
  for m in $list; do
    [ "$m" = "$model" ] && continue
    case $model in */*) ;; *) case $m in "$model"/*) continue ;; esac ;; esac
    out="${out:+$out,}$m"
  done
  printf '%s' "$out"
}

eg_preflight() {
  local model=$1 dir=$2 out=$3 rc ok req err served other why
  mkdir -p "$dir"
  # shellcheck disable=SC2086
  ( cd "$dir" && eg_tmo 10 180 "$JAZZ" -p --yolo $EG_STRICT ${model:+-m "$model"} --timeout 120s \
      --metrics-out "$out.metrics.json" 'responda apenas: ok' </dev/null >"$out.log" 2>&1 ); rc=$?
  read -r ok req err served <<EOF
$(eg_metrics "$out.metrics.json")
EOF
  if [ "$rc" != 0 ] || [ "$ok" = 0 ] || [ "$ok" = "?" ]; then
    why=$(grep -a '^jazz: ' "$out.log" 2>/dev/null | grep -a -v -E 'execução incompleta|retome com' | tail -n 1)
    echo "modelo indisponível: ${model:-padrão} não respondeu ao teste prévio (rc=$rc, requests ok=$ok de $req)${why:+ — $why}" >&2
    echo "  nenhuma tarefa foi rodada; log: $out.log" >&2
    return 1
  fi
  other=$(eg_served_other "$model" "$served")
  if [ -n "$other" ]; then
    echo "modelo trocado no teste prévio: pediu $model, respondeu $other — a nota não seria do modelo pedido" >&2
    return 1
  fi
  return 0
}

eg_dead() {
  local ok
  read -r ok _ <<EOF
$(eg_metrics "${1:-}")
EOF
  [ "$ok" = 0 ] || { [ ! -e "${1:-}" ] && eg_py; }
}

eg_manifest() {
  local file=$1; shift
  eg_py || { echo "aviso: sem python3, sem manifest.json" >&2; return 0; }
  local bin sha=
  bin=$(command -v "$JAZZ" 2>/dev/null || echo "$JAZZ")
  sha=$( (shasum -a 256 "$bin" 2>/dev/null || sha256sum "$bin" 2>/dev/null) | awk '{print $1}')
  EG_VERSION=$("$JAZZ" --version 2>/dev/null | head -1) \
  EG_SHA=$sha EG_BIN=$bin \
  EG_COMMIT=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse HEAD 2>/dev/null) \
  EG_UNAME=$(uname -a 2>/dev/null) EG_HOST=$(hostname 2>/dev/null) \
  EG_PY=$(python3 --version 2>&1) EG_STRICT_HOW=$EG_STRICT_HOW \
  python3 -I - "$file" "$@" <<'PY'
import datetime, json, os, re, sys
out = {
    "version": os.environ.get("EG_VERSION", ""),
    "binary": os.environ.get("EG_BIN", ""),
    "binary_sha256": os.environ.get("EG_SHA", ""),
    "evals_commit": os.environ.get("EG_COMMIT", ""),
    "host": os.environ.get("EG_HOST", ""),
    "uname": os.environ.get("EG_UNAME", ""),
    "python": os.environ.get("EG_PY", ""),
    "strict": os.environ.get("EG_STRICT_HOW", "none"),
    "started": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
}
for kv in sys.argv[2:]:
    k, _, v = kv.partition("=")
    out[k] = v
secret = re.compile(r"PASS|KEY|TOKEN|SECRET|CRED|AUTH", re.I)
out["jazz_env"] = {k: ("[REDACTED]" if secret.search(k) else v)
                   for k, v in sorted(os.environ.items()) if k.startswith("JAZZ_") and not k.startswith("EG_")}
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(out, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY
}
