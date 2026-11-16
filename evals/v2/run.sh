#!/bin/bash
# Bateria v2 do jazz (feita para rodar na máquina alvo, ex.: um linux/386 antigo). Cada tarefa roda numa pasta nova em
# $HOME/jazz-eval-results/v2-<data>/work/<tarefa> (nada é apagado), com jazz -p e prazo.
# O verificador fica FORA da pasta de trabalho: PROMPT, CHECK, CLEANUP e .hidden/ não são
# copiados, e a pasta tasks/ fica sem permissão de leitura enquanto o agente roda
# (a permissão volta no fim, mesmo com Ctrl+C).
#
# A nota tem de medir o JAZZ com o modelo pedido, não a cota do pool grátis (evals/bench/evalguard.sh):
#   - trava: uma rodada por máquina ($HOME/jazz-eval-results/.lock); duas no mesmo pool invalidam as duas
#   - JAZZ_HOME isolado por rodada (só config.json e auth.json, modo 600), com plan cache e lições off:
#     a rodada não grava plano/lição no HOME de verdade; as sessões ficam em $RUN/sessions
#   - modelo preso: --strict-model quando o jazz tem, senão fallback [MODELO] e omni off no config isolado
#   - teste prévio: 1 pedido mínimo antes; modelo que não responde aborta com "modelo indisponível"
#     sem queimar tarefa (PREFLIGHT=0 pula, e fica registrado no manifest)
#   - disjuntor: tarefa que termina com 0 request OK faz as seguintes virarem SKIP; o fim imprime o
#     ONLY="…" para retomar
#   - classificação pelas métricas (--metrics-out): SUBST (outro modelo serviu), INFRA-NOSTART
#     (0 request OK), FAIL-HARNESS (#33 de volta) além de INFRA?/HANG? (classify_infra.sh)
#   - manifest.json com versão, sha do binário, commit, host, uname, python, modelo e chaves JAZZ_*
#
# Uso: ./run.sh [modelo]
#   LIMIT=900      prazo por tarefa em segundos (vira --timeout do jazz)
#   ONLY="01 07"   roda só as tarefas cujo nome começa com esses prefixos
#   JAZZ=caminho   binário do jazz (padrão: jazz no PATH ou ~/.local/bin/jazz)
#   LANES=N        roda até N tarefas ao mesmo tempo (padrão 1: uma por vez, tempos comparáveis).
#                  Com N>1 todas as pastas de trabalho são preparadas antes, tasks/ fica trancada
#                  durante todos os agentes, e os verificadores rodam depois, em ordem. Cada lane
#                  usa o mesmo JAZZ_HOME isolado (ratelimit.json e model-scores.json são mesclados sob
#                  flock: testado em judge_test.go); LANE_HOME=1 dá a cada tarefa uma cópia própria dele.
#   JAZZ_ARGS="…"  flags extras pro jazz (ex.: "--omni-judge on --verifier on")
#   TASKS=pasta    as tarefas (padrão: evals/v2/tasks; os testes usam uma cópia)
#   PREFLIGHT=0    pula o teste prévio (só para depurar o runner)
set -u
EV="$(cd "$(dirname "$0")" && pwd)"
TASKS="${TASKS:-$EV/tasks}"
# classify_infra: FAIL real vs. INFRA?/INFRA-NOSTART/SUBST vs. HANG?
. "$EV/../bench/classify_infra.sh"
. "$EV/../bench/evalguard.sh"
MODEL="${1:-}"; LIMIT="${LIMIT:-900}"; ONLY="${ONLY:-}"; JAZZ="${JAZZ:-jazz}"; LANES="${LANES:-1}"; JAZZ_ARGS="${JAZZ_ARGS:-}"
LANE_HOME="${LANE_HOME:-0}"; PREFLIGHT="${PREFLIGHT:-1}"
command -v "$JAZZ" >/dev/null 2>&1 || JAZZ="$HOME/.local/bin/jazz"
tmo() { eg_tmo "$@"; }

eg_lock "$HOME/jazz-eval-results" || exit 2
RUN="$HOME/jazz-eval-results/v2-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RUN/work"

MODO_TASKS=$(stat -c %a "$TASKS" 2>/dev/null || stat -f %Lp "$TASKS")
unlock() { chmod "$MODO_TASKS" "$TASKS" 2>/dev/null; }
finish() { unlock; eg_home_cleanup "$RUN"; eg_unlock; }
trap finish EXIT
trap 'finish; exit 130' INT TERM

eg_home || { echo "não consegui criar o JAZZ_HOME isolado" >&2; exit 2; }
eg_strict "$MODEL" || { echo "não consegui prender o modelo no config isolado" >&2; exit 2; }
CLASSIFY_VERSION=$("$JAZZ" --version 2>/dev/null | grep -o -E '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
export CLASSIFY_VERSION

copy_task() { # $1 = pasta da tarefa, $2 = pasta de trabalho
  local f b
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    b=$(basename "$f")
    case "$b" in PROMPT|CHECK|CLEANUP|.hidden|__pycache__) continue ;; esac
    cp -R "$f" "$2"/
  done
}

pass=0; n=0; prompts_total=0; skipped=()
echo "jazz: $("$JAZZ" --version 2>&1 | head -1)  modelo: ${MODEL:-padrão}  prazo: ${LIMIT}s  lanes: $LANES  trava: $EG_STRICT_HOW${JAZZ_ARGS:+  args: $JAZZ_ARGS}" | tee "$RUN/summary.txt"

names=()
for t in "$TASKS"/*/; do
  t=${t%/}; name=$(basename "$t")
  if [ -n "$ONLY" ]; then
    sel=; for o in $ONLY; do case "$name" in "$o"*) sel=1 ;; esac; done
    [ -n "$sel" ] || continue
  fi
  names+=("$name")
done
[ ${#names[@]} -gt 0 ] || { echo "nenhuma tarefa selecionada (ONLY=$ONLY)"; exit 1; }

eg_manifest "$RUN/manifest.json" "suite=v2" "model=${MODEL:-}" "limit=$LIMIT" "lanes=$LANES" "only=$ONLY" \
  "jazz_args=$JAZZ_ARGS" "preflight=$PREFLIGHT" "tasks=${names[*]}"

if [ "$PREFLIGHT" != 0 ]; then
  if ! pf=$(eg_preflight "$MODEL" "$RUN/work/.preflight" "$RUN/preflight" 2>&1); then
    printf '%s\n' "$pf" | tee -a "$RUN/summary.txt" >&2
    exit 3
  fi
else
  echo "aviso: PREFLIGHT=0, sem teste prévio do modelo" | tee -a "$RUN/summary.txt"
fi

# run_agent NAME: one jazz -p in its work dir; writes $RUN/NAME.rc and NAME.dt
run_agent() {
  local name=$1 w="$RUN/work/$1" prompt s rc home
  prompt=$(cat "$RUN/.prompts/$name"); s=$(date +%s)
  home="$JAZZ_HOME"
  if [ "$LANE_HOME" = 1 ]; then
    home="$RUN/homes/$name"; mkdir -p "$RUN/homes"; cp -R "$JAZZ_HOME" "$home"; chmod 700 "$home"
  fi
  # shellcheck disable=SC2086
  ( cd "$w" && JAZZ_HOME="$home" tmo 10 $((LIMIT + 60)) "$JAZZ" -p --yolo -v --timeout "${LIMIT}s" --metrics-out "$RUN/$name.metrics.json" \
      $EG_STRICT ${MODEL:+-m "$MODEL"} $JAZZ_ARGS "$prompt" \
      </dev/null >"$RUN/$name.log" 2>&1 ); rc=$?
  if [ "$LANE_HOME" = 1 ]; then
    [ -d "$home/data/sessions" ] && mkdir -p "$RUN/sessions" && cp -R "$home/data/sessions/." "$RUN/sessions/" 2>/dev/null
    rm -rf "$home" # tem as credenciais
  fi
  echo "$rc" > "$RUN/$name.rc"; echo $(( $(date +%s) - s )) > "$RUN/$name.dt"
}

# check_task NAME: hidden checker + cleanup, one summary line
check_task() {
  local name=$1 t="$TASKS/$1" w="$RUN/work/$1" res tools rc dt prompts extra=
  rc=$(cat "$RUN/$name.rc" 2>/dev/null || echo "?"); dt=$(cat "$RUN/$name.dt" 2>/dev/null || echo "?")
  tools=$(grep -oE "[0-9]+ ferramentas?" "$RUN/$name.log" | tail -1 | grep -oE "^[0-9]+")
  # prompts: vezes que o usuário teria de responder (confirmações, ask_user, turno que termina em pergunta)
  prompts=$(grep -oE '"prompts": *[0-9]+' "$RUN/$name.metrics.json" 2>/dev/null | grep -oE "[0-9]+$")
  [ -n "$prompts" ] && prompts_total=$((prompts_total + prompts))
  if ( cd "$w" && TASK="$t" bash "$t/CHECK" ) >"$RUN/$name.check" 2>&1; then res=PASS; else res=FAIL; fi
  if [ -f "$t/CLEANUP" ]; then ( cd "$w" && TASK="$t" bash "$t/CLEANUP" ) >>"$RUN/$name.check" 2>&1; fi
  n=$((n + 1))
  # um FAIL só conta como falha do modelo se o modelo pedido serviu, respondeu ao menos uma vez,
  # a rede não comeu o prazo e o jazz não foi morto de fora
  classify_infra "$RUN/$name.log" "$rc" "$LIMIT" "$res" "$dt" "$RUN/$name.metrics.json" "$MODEL" >/dev/null; res=$CLASSIFY_RES
  classify_count
  [ "$res" = PASS ] && pass=$((pass + 1))
  [ "$CLASSIFY_NET_S" = 0 ] || extra="rede=${CLASSIFY_NET_S}s  "
  [ -z "$CLASSIFY_SERVED" ] || extra="${extra}serviu=$CLASSIFY_SERVED  "
  printf "%-24s %-6s %5ss  tools=%-3s prompts=%-2s rc=%-3s %s%s\n" "$name" "$res" "$dt" "${tools:-?}" "${prompts:-?}" "$rc" \
    "$extra" "$(grep -E '^(PASS|FAIL)' "$RUN/$name.check" | tail -1)" | tee -a "$RUN/summary.txt"
}

skip_task() {
  skipped+=("$1")
  printf "%-24s %-6s %s\n" "$1" SKIP "(disjuntor: o modelo parou de responder numa tarefa anterior)" | tee -a "$RUN/summary.txt"
}

mkdir -p "$RUN/.prompts"
for name in "${names[@]}"; do
  w="$RUN/work/$name"; mkdir -p "$w"; copy_task "$TASKS/$name" "$w"
  cp "$TASKS/$name/PROMPT" "$RUN/.prompts/$name"
done

s0=$(date +%s); tripped=
if [ "$LANES" -le 1 ]; then
  for name in "${names[@]}"; do
    if [ -n "$tripped" ]; then skip_task "$name"; continue; fi
    chmod 000 "$TASKS"; run_agent "$name"; unlock
    check_task "$name"
    eg_dead "$RUN/$name.metrics.json" && tripped=$name
  done
else
  chmod 000 "$TASKS"
  launched=()
  for name in "${names[@]}"; do
    # bash 3.2 (macOS) has no wait -n: poll the running jobs
    while [ "$(jobs -rp | wc -l | tr -d ' ')" -ge "$LANES" ]; do sleep 1; done
    for d in ${launched[@]+"${launched[@]}"}; do
      [ -f "$RUN/$d.rc" ] && eg_dead "$RUN/$d.metrics.json" && { tripped=$d; break; }
    done
    [ -n "$tripped" ] && break
    run_agent "$name" &
    launched+=("$name")
    sleep 2 # stagger the first requests a little
  done
  wait
  unlock
  for name in "${launched[@]}"; do check_task "$name"; done
  for name in "${names[@]}"; do
    case " ${launched[*]} " in *" $name "*) ;; *) skip_task "$name" ;; esac
  done
fi
extra=
[ ${#skipped[@]} = 0 ] || extra=", puladas: ${#skipped[@]}"
echo "$(classify_total_line "$pass" "$n" "$CLASSIFY_N_REDE" "$CLASSIFY_N_COTA" "$CLASSIFY_N_TROCA" "$CLASSIFY_N_HANG")  (parede: $(( $(date +%s) - s0 ))s, lanes: $LANES, prompts: $prompts_total em $n tarefas$extra)" | tee -a "$RUN/summary.txt"
if [ ${#skipped[@]} -gt 0 ]; then
  only=${tripped%%-*}; for name in "${skipped[@]}"; do only="$only ${name%%-*}"; done
  echo "disjuntor: $tripped terminou sem nenhum request OK; para retomar quando a cota voltar: ONLY=\"$only\" $0${MODEL:+ $MODEL}" | tee -a "$RUN/summary.txt"
fi
echo "resultados: $RUN"
