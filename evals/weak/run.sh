#!/bin/bash
# Bateria de modelos fracos: cada tarefa e um mini-repo descartavel; roda o jazz em
# modo headless (-p) com prazo, depois o verificador independente da tarefa.
# O verificador nunca confia na palavra do agente: roda testes/build e grep.
#
# Uso (um modelo por vez):
#   go build -o /tmp/jz-weak .            (na raiz do repo, depois do pgrep zerar)
#   MODEL=pollinations/openai-fast JAZZ_BRAIN=on   ./evals/weak/run.sh
#   MODEL=pollinations/openai-fast JAZZ_BRAIN=off  ./evals/weak/run.sh   # ablação
#   MODEL=vazio (padrão do jazz) ...
#
# Variáveis: MODEL (modelo), JAZZ (binário), LIMIT (prazo s, def. 300 = 5m),
# ONLY ("01 07" prefixos), TAG (etiqueta da pasta de resultados),
# SAIDA (pasta de resultados, def. /tmp/jz-weak-results/weak-TAG),
# JAZZ_ARGS (flags extras, def. --yolo: modo headless pede auto-aprovação).
set -u
EV="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$EV/../.." && pwd)"
JAZZ="${JAZZ:-/tmp/jz-weak}"
MODEL="${MODEL:-}"
BRAIN="${JAZZ_BRAIN:-on}"
LIMIT="${LIMIT:-300}"
ONLY="${ONLY:-}"
TAG="${TAG:-$(date +%Y%m%d-%H%M%S)}"
RUN="${SAIDA:-/tmp/jz-weak-results/weak-$TAG}"
JAZZ_ARGS="${JAZZ_ARGS:---yolo}"

command -v "$JAZZ" >/dev/null 2>&1 || { echo "jazz não está em $JAZZ; rode:";
  echo "  cd $ROOT && CGO_ENABLED=0 GOMAXPROCS=2 go build -p=1 -o /tmp/jz-weak ."; exit 1; }

# timeout portátil (coreutils, gtimeout ou alarm do perl)
tmo() { local k=$1 secs=$2; shift 2
  if command -v timeout >/dev/null 2>&1; then timeout -k "$k" "$secs" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout -k "$k" "$secs" "$@"
  else perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; fi; }

mkdir -p "$RUN/work"
RES="$RUN/summary.tsv"
echo -e "tarefa\tsituacao\tfalso\tsegs\trc\ttok_out\tferramentas\tfim" > "$RES"

names=()
for t in "$EV"/tasks/*/; do
  t=${t%/}; name=$(basename "$t")
  if [ -n "$ONLY" ]; then
    sel=; for o in $ONLY; do case "$name" in "$o"*) sel=1 ;; esac; done
    [ -n "$sel" ] || continue
  fi
  names+=("$name")
done
[ ${#names[@]} -gt 0 ] || { echo "nenhuma tarefa (ONLY=$ONLY)"; exit 1; }

# caches compartilhados fora do HOME descartável (o go não precisa recompilar stdlib 24x)
CACHE="${GOCACHE:-/tmp/jz-weak-cache/go-build}"
GPATH="${GOPATH:-/tmp/jz-weak-cache/gopath}"
mkdir -p "$CACHE" "$GPATH" "$RUN/homes"

total=0; ok=0; falsos=0
s0=$(date +%s)
for name in "${names[@]}"; do
  t="$EV/tasks/$name"; w="$RUN/work/$name"; h="$RUN/homes/$name"
  mkdir -p "$w" "$h"
  bash "$t/setup.sh" "$w" >/dev/null 2>&1 || { echo "$name: setup falhou"; continue; }
  s=$(date +%s)
  ( cd "$w" \
    && HOME="$h" GOCACHE="$CACHE" GOPATH="$GPATH" GOMODCACHE="$GPATH/pkg/mod" \
       GOTOOLCHAIN=local GOPROXY=off JAZZ_BRAIN="$BRAIN" \
    && tmo 20 $((LIMIT + 60)) "$JAZZ" -p $JAZZ_ARGS --timeout "${LIMIT}s" \
       --metrics-out "$RUN/$name.metrics.json" ${MODEL:+-m "$MODEL"} \
       "$(cat "$t/prompt.txt")" </dev/null >"$RUN/$name.log" 2>&1 ); rc=$?
  dt=$(( $(date +%s) - s ))

  # verificação independente (a tarefa, não a palavra do agente)
  if ( cd "$w" && TASK="$t" bash "$t/check.sh" ) >"$RUN/$name.check" 2>&1; then res=PASS; ok=$((ok + 1)); else res=FALHA; fi
  falso=0
  if [ "$res" = "FALHA" ] && [ "$rc" -eq 0 ]; then falso=1; falsos=$((falsos + 1)); fi
  total=$((total + 1))

  read -r to tools situacao <<<"$(python3 - "$RUN/$name.metrics.json" <<'PYEOF'
import json, sys
try:
    m = json.load(open(sys.argv[1]))
except Exception:
    print("0 0 --"); sys.exit()
print(m.get("tokens_out", 0), m.get("tool_calls", 0),
      (m.get("outcome") or m.get("status") or "--").replace(" ", "_"))
PYEOF
)"

  cat "$RUN/$name.check" >>"$RUN/$name.log" 2>/dev/null || true
  # linha em TSV (igual ao cabeçalho de summary.tsv)
  printf "%-24s %-6s falso=%d %4ds rc=%d tokout=%-6s ferr=%-3s %s\n" \
    "$name" "$res" "$falso" "$dt" "$rc" "$to" "$tools" "$situacao"
  printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" "$name" "$res" "$falso" "$dt" "$rc" "$to" "$tools" "$situacao" >>"$RES"
done
echo "== $RUN  bateria: $total tarefas, $ok passaram, $falsos falso-sucesso, modelo=${MODEL:-padrão}, JAZZ_BRAIN=$BRAIN, prazo=${LIMIT}s, parede=$(( $(date +%s) - s0 ))s" | tee -a "$RES"
