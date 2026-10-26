#!/bin/bash
# Bateria v2 do jazz (feita para rodar na máquina alvo, ex.: um linux/386 antigo). Cada tarefa roda numa pasta nova em
# $HOME/jazz-eval-results/v2-<data>/work/<tarefa> (nada é apagado), com jazz -p e prazo.
# O verificador fica FORA da pasta de trabalho: PROMPT, CHECK, CLEANUP e .hidden/ não são
# copiados, e a pasta tasks/ fica sem permissão de leitura enquanto o agente roda
# (a permissão volta no fim, mesmo com Ctrl+C).
#
# Uso: ./run.sh [modelo]
#   LIMIT=900      prazo por tarefa em segundos (vira --timeout do jazz)
#   ONLY="01 07"   roda só as tarefas cujo nome começa com esses prefixos
#   JAZZ=caminho   binário do jazz (padrão: jazz no PATH ou ~/.local/bin/jazz)
#   LANES=N        roda até N tarefas ao mesmo tempo (padrão 1: uma por vez, tempos comparáveis).
#                  Com N>1 todas as pastas de trabalho são preparadas antes, tasks/ fica trancada
#                  durante todos os agentes, e os verificadores rodam depois, em ordem. Cada lane
#                  usa o mesmo JAZZ_HOME (ratelimit.json e model-scores.json são mesclados sob flock:
#                  testado em judge_test.go); LANE_HOME=1 dá a cada tarefa uma cópia própria do JAZZ_HOME.
#   JAZZ_ARGS="…"  flags extras pro jazz (ex.: "--omni-judge on --verifier on")
set -u
EV="$(cd "$(dirname "$0")" && pwd)"
RUN="$HOME/jazz-eval-results/v2-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RUN/work"
MODEL="${1:-}"; LIMIT="${LIMIT:-900}"; ONLY="${ONLY:-}"; JAZZ="${JAZZ:-jazz}"; LANES="${LANES:-1}"; JAZZ_ARGS="${JAZZ_ARGS:-}"
LANE_HOME="${LANE_HOME:-0}"
command -v "$JAZZ" >/dev/null 2>&1 || JAZZ="$HOME/.local/bin/jazz"
# timeout portátil: coreutils timeout, gtimeout (Homebrew) ou alarm do perl (sem -k)
tmo() { local k=$1 secs=$2; shift 2; if command -v timeout >/dev/null 2>&1; then timeout -k "$k" "$secs" "$@"; elif command -v gtimeout >/dev/null 2>&1; then gtimeout -k "$k" "$secs" "$@"; else perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; fi; }

MODO_TASKS=$(stat -c %a "$EV/tasks" 2>/dev/null || stat -f %Lp "$EV/tasks")
unlock() { chmod "$MODO_TASKS" "$EV/tasks" 2>/dev/null; }
trap unlock EXIT
trap 'unlock; exit 130' INT TERM

copy_task() { # $1 = pasta da tarefa, $2 = pasta de trabalho
  local f b
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    b=$(basename "$f")
    case "$b" in PROMPT|CHECK|CLEANUP|.hidden|__pycache__) continue ;; esac
    cp -R "$f" "$2"/
  done
}

pass=0; n=0
echo "jazz: $("$JAZZ" --version 2>&1 | head -1)  modelo: ${MODEL:-padrão}  prazo: ${LIMIT}s  lanes: $LANES${JAZZ_ARGS:+  args: $JAZZ_ARGS}" | tee "$RUN/summary.txt"

names=()
for t in "$EV"/tasks/*/; do
  t=${t%/}; name=$(basename "$t")
  if [ -n "$ONLY" ]; then
    sel=; for o in $ONLY; do case "$name" in "$o"*) sel=1 ;; esac; done
    [ -n "$sel" ] || continue
  fi
  names+=("$name")
done

# run_agent NAME: one jazz -p in its work dir; writes $RUN/NAME.rc and NAME.dt
run_agent() {
  local name=$1 w="$RUN/work/$1" prompt s rc home
  prompt=$(cat "$RUN/.prompts/$name"); s=$(date +%s)
  home="${JAZZ_HOME:-}"
  if [ "$LANE_HOME" = 1 ] && [ -n "${JAZZ_HOME:-}" ]; then
    home="$RUN/homes/$name"; mkdir -p "$RUN/homes"; cp -R "$JAZZ_HOME" "$home"
  fi
  # shellcheck disable=SC2086
  ( cd "$w" && JAZZ_HOME="$home" tmo 10 $((LIMIT + 60)) "$JAZZ" -p --yolo -v --timeout "${LIMIT}s" ${MODEL:+-m "$MODEL"} $JAZZ_ARGS "$prompt" \
      </dev/null >"$RUN/$name.log" 2>&1 ); rc=$?
  echo "$rc" > "$RUN/$name.rc"; echo $(( $(date +%s) - s )) > "$RUN/$name.dt"
}

# check_task NAME: hidden checker + cleanup, one summary line
check_task() {
  local name=$1 t="$EV/tasks/$1" w="$RUN/work/$1" res tools rc dt
  rc=$(cat "$RUN/$name.rc" 2>/dev/null || echo "?"); dt=$(cat "$RUN/$name.dt" 2>/dev/null || echo "?")
  tools=$(grep -oE "[0-9]+ ferramentas?" "$RUN/$name.log" | tail -1 | grep -oE "^[0-9]+")
  if ( cd "$w" && TASK="$t" bash "$t/CHECK" ) >"$RUN/$name.check" 2>&1; then res=PASS; pass=$((pass + 1)); else res=FAIL; fi
  if [ -f "$t/CLEANUP" ]; then ( cd "$w" && TASK="$t" bash "$t/CLEANUP" ) >>"$RUN/$name.check" 2>&1; fi
  n=$((n + 1))
  printf "%-24s %-5s %5ss  tools=%-3s rc=%-3s %s\n" "$name" "$res" "$dt" "${tools:-?}" "$rc" \
    "$(grep -E '^(PASS|FAIL)' "$RUN/$name.check" | tail -1)" | tee -a "$RUN/summary.txt"
}

[ ${#names[@]} -gt 0 ] || { echo "nenhuma tarefa selecionada (ONLY=$ONLY)"; exit 1; }
mkdir -p "$RUN/.prompts"
for name in "${names[@]}"; do
  w="$RUN/work/$name"; mkdir -p "$w"; copy_task "$EV/tasks/$name" "$w"
  cp "$EV/tasks/$name/PROMPT" "$RUN/.prompts/$name"
done

s0=$(date +%s)
if [ "$LANES" -le 1 ]; then
  for name in "${names[@]}"; do
    chmod 000 "$EV/tasks"; run_agent "$name"; unlock
    check_task "$name"
  done
else
  chmod 000 "$EV/tasks"
  for name in "${names[@]}"; do
    # bash 3.2 (macOS) has no wait -n: poll the running jobs
    while [ "$(jobs -rp | wc -l | tr -d ' ')" -ge "$LANES" ]; do sleep 1; done
    run_agent "$name" &
    sleep 2 # stagger the first requests a little
  done
  wait
  unlock
  for name in "${names[@]}"; do check_task "$name"; done
fi
echo "TOTAL: $pass/$n  (parede: $(( $(date +%s) - s0 ))s, lanes: $LANES)" | tee -a "$RUN/summary.txt"; echo "resultados: $RUN"
