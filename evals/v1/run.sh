#!/bin/bash
# Bateria v1 do jazz (feita para rodar na máquina alvo, ex.: um linux/386 antigo): cada tarefa numa pasta nova (nada é apagado), jazz -p com timeout, verificador independente.
#   LIMIT=600     prazo por tarefa (s)
#   LANES=N       até N tarefas ao mesmo tempo (padrão 1); com N>1 os verificadores rodam no fim, em ordem
#   JAZZ=caminho  binário (padrão: jazz no PATH);  JAZZ_ARGS="…" flags extras (ex.: "--omni-judge on")
set -u
EV="$(cd "$(dirname "$0")" && pwd)"; RUN="$HOME/jazz-eval-results/$(date +%Y%m%d-%H%M%S)"; mkdir -p "$RUN"
MODEL="${1:-}"; LIMIT="${LIMIT:-600}"; LANES="${LANES:-1}"; JAZZ="${JAZZ:-jazz}"; JAZZ_ARGS="${JAZZ_ARGS:-}"
pass=0; n=0
run_agent() { # $1 = tarefa
  local name=$1 w="$RUN/work/$1" prompt s rc
  prompt=$(cat "$EV/tasks/$name/PROMPT"); s=$(date +%s)
  # shellcheck disable=SC2086
  ( cd "$w"; timeout "$LIMIT" "$JAZZ" -p --yolo -v ${MODEL:+-m "$MODEL"} $JAZZ_ARGS "$prompt" > "$RUN/$name.log" 2>&1 ); rc=$?
  echo "$rc" > "$RUN/$name.rc"; echo $(( $(date +%s) - s )) > "$RUN/$name.dt"
}
check_task() {
  local name=$1 t="$EV/tasks/$1" w="$RUN/work/$1" ok tools
  tools=$(grep -oE "[0-9]+ ferramentas?" "$RUN/$name.log" | tail -1 | grep -oE "^[0-9]+")
  if ( cd "$w"; bash "$t/CHECK" ) >/dev/null 2>&1; then ok=PASS; pass=$((pass+1)); else ok=FAIL; fi
  n=$((n+1)); printf "%-14s %-5s %5ss  tools=%s rc=%s\n" "$name" "$ok" "$(cat "$RUN/$name.dt")" "${tools:-?}" "$(cat "$RUN/$name.rc")" | tee -a "$RUN/summary.txt"
}
names=()
for t in "$EV"/tasks/*/; do
  name=$(basename "$t"); w="$RUN/work/$name"; mkdir -p "$w"; cp -r "$t"/. "$w"/
  ( cd "$w"; for f in test_calc.py misterio.py; do [ -f "$f" ] && cp "$f" ".orig_$f"; done; rm -f PROMPT CHECK )
  names+=("$name")
done
s0=$(date +%s)
if [ "$LANES" -le 1 ]; then
  for name in "${names[@]}"; do run_agent "$name"; check_task "$name"; done
else
  for name in "${names[@]}"; do
    while [ "$(jobs -rp | wc -l | tr -d ' ')" -ge "$LANES" ]; do sleep 1; done # bash 3.2: no wait -n
    run_agent "$name" & sleep 2
  done
  wait
  for name in "${names[@]}"; do check_task "$name"; done
fi
echo "TOTAL: $pass/$n  (parede: $(( $(date +%s) - s0 ))s, lanes: $LANES)" | tee -a "$RUN/summary.txt"; echo "resultados: $RUN"
