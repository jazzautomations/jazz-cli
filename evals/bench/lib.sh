#!/bin/bash
# Helpers compartilhados dos runners de bench público (terminal-tasks, polyglot,
# swebench-lite puro). Port do ~/bench/common/lib.sh usado no Dell, para o Dell
# adotar depois: record() e bench_finish() com a classificação INFRA?/HANG? de
# classify_infra.sh (o 5/20 do swebench e o 7/17 do polyglot misturavam falhas
# reais com janelas sem rede; docs/research/benchmarks.md).
#
# Uso (no run.sh de uma suíte):
#   . "$(dirname "$0")/../bench/lib.sh"
#   bench_init "$RUN/summary.txt" "$LIMIT"
#   ... por tarefa: roda o agente com `timeout -k 10 $((LIMIT+60)) jazz -p --timeout ${LIMIT}s ...`
#       > "$RUN/$name/agent.log"; rc=$?; roda o verificador -> PASS/FAIL
#   record NAME RESULT RC DT LOG [DETALHE]
#   bench_finish [EXTRA]        # "TOTAL bruto X/N · VÁLIDAS Y/V (Z inválidas: rede R / hang H)"
#
# record imprime (e anexa ao summary) uma linha por tarefa, no mesmo formato
# que evals/v2/run.sh (improve_load.go lê "NOME PASS|FAIL DTs tools=.. rc=.."):
#   NOME  PASS|FAIL|INFRA?|HANG?  DTs  tools=T  rc=RC  [rede=Xs]  DETALHE
# INFRA?/HANG? ficam fora do PASS|FAIL de propósito: tentativa inválida não é
# falha do modelo e o jazz improve --harness não deve aprender com ela.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/classify_infra.sh"

BENCH_SUMMARY=""; BENCH_LIMIT=0
BENCH_N=0; BENCH_PASS=0; BENCH_INFRA=0; BENCH_HANG=0; BENCH_T0=0

# bench_init SUMMARY LIMIT: onde anexar as linhas e o prazo por tarefa (segundos).
bench_init() {
  BENCH_SUMMARY=$1; BENCH_LIMIT=${2:-0}
  BENCH_N=0; BENCH_PASS=0; BENCH_INFRA=0; BENCH_HANG=0; BENCH_T0=$(date +%s)
  [ -z "$BENCH_SUMMARY" ] || : >> "$BENCH_SUMMARY"
}

# bench_tee LINE: imprime e anexa ao summary (quando há um).
bench_tee() {
  if [ -n "$BENCH_SUMMARY" ]; then printf '%s\n' "$1" | tee -a "$BENCH_SUMMARY"; else printf '%s\n' "$1"; fi
}

# record NAME RESULT RC DT LOG [DETALHE]: classifica e acumula uma tarefa.
# RESULT é o veredito do verificador (PASS/FAIL); vazio = PASS se RC=0, senão FAIL.
record() {
  local name=$1 result=${2:-} rc=${3:-?} dt=${4:-?} log=${5:-} detail=${6:-} res tools net= line
  classify_infra "$log" "$rc" "$BENCH_LIMIT" "$result" "$dt" >/dev/null; res=$CLASSIFY_RES
  [ "$CLASSIFY_NET_S" = 0 ] || net="rede=${CLASSIFY_NET_S}s"
  tools=$(grep -a -oE "[0-9]+ ferramentas?" "$log" 2>/dev/null | tail -1 | grep -oE "^[0-9]+")
  BENCH_N=$(( BENCH_N + 1 ))
  case $res in
    PASS) BENCH_PASS=$(( BENCH_PASS + 1 )) ;;
    "INFRA?") BENCH_INFRA=$(( BENCH_INFRA + 1 )) ;;
    "HANG?") BENCH_HANG=$(( BENCH_HANG + 1 )) ;;
  esac
  line=$(printf "%-24s %-6s %5ss  tools=%-3s rc=%-3s %s%s" "$name" "$res" "$dt" "${tools:-?}" "$rc" "${net:+$net  }" "$detail")
  bench_tee "${line%"${line##*[! ]}"}"
}

# bench_finish [EXTRA]: a linha final; EXTRA vai entre parênteses no fim (ex.: "lanes: 2").
bench_finish() {
  local extra=${1:-} wall
  wall=$(( $(date +%s) - BENCH_T0 ))
  bench_tee "$(classify_total_line "$BENCH_PASS" "$BENCH_N" "$BENCH_INFRA" "$BENCH_HANG")  (parede: ${wall}s${extra:+, $extra})"
}
