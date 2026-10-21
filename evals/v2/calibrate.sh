#!/bin/bash
# Calibração (roda na máquina de desenvolvimento, não na máquina alvo das evals): para cada tarefa, o CHECK precisa FALHAR na
# tarefa intocada e PASSAR com a solução de referência (refsol/<tarefa>: arquivos que
# substituem/criam + solve.sh opcional, executado na pasta de trabalho).
# Uso: ./calibrate.sh [prefixos...]   PY=python3.14 ./calibrate.sh  (troca o python3 do PATH)
set -u
EV="$(cd "$(dirname "$0")" && pwd)"
OUT="${OUT:-$EV/../calib-v2/$(date +%Y%m%d-%H%M%S)}"; mkdir -p "$OUT"
if [ -n "${PY:-}" ]; then mkdir -p "$OUT/bin"; ln -sf "$(command -v "$PY")" "$OUT/bin/python3"; export PATH="$OUT/bin:$PATH"; fi
echo "python3 = $(python3 --version)  saída: $OUT"
copy_task() {
  local f b
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    b=$(basename "$f")
    case "$b" in PROMPT|CHECK|CLEANUP|.hidden|__pycache__|solve.sh) continue ;; esac
    cp -R "$f" "$2"/
  done
}
bad=0
for t in "$EV"/tasks/*/; do
  t=${t%/}; name=$(basename "$t")
  if [ $# -gt 0 ]; then sel=; for o in "$@"; do case "$name" in "$o"*) sel=1 ;; esac; done; [ -n "$sel" ] || continue; fi
  o="$OUT/$name/orig"; r="$OUT/$name/ref"; mkdir -p "$o" "$r"
  copy_task "$t" "$o"; copy_task "$t" "$r"
  ( cd "$o" && TASK="$t" bash "$t/CHECK" ) >"$OUT/$name/orig.check" 2>&1; ro=$?
  [ -f "$t/CLEANUP" ] && ( cd "$o" && TASK="$t" bash "$t/CLEANUP" ) >/dev/null 2>&1
  copy_task "$EV/refsol/$name" "$r"
  if [ -f "$EV/refsol/$name/solve.sh" ]; then ( cd "$r" && bash "$EV/refsol/$name/solve.sh" ) >"$OUT/$name/solve.log" 2>&1; fi
  ( cd "$r" && TASK="$t" bash "$t/CHECK" ) >"$OUT/$name/ref.check" 2>&1; rr=$?
  [ -f "$t/CLEANUP" ] && ( cd "$r" && TASK="$t" bash "$t/CLEANUP" ) >/dev/null 2>&1
  verdict=OK; { [ $ro -ne 0 ] && [ $rr -eq 0 ]; } || { verdict=BAD; bad=$((bad + 1)); }
  printf "%-24s intocada=%-4s ref=%-4s %s | %s | %s\n" "$name" "$([ $ro -eq 0 ] && echo PASS || echo FAIL)" \
    "$([ $rr -eq 0 ] && echo PASS || echo FAIL)" "$verdict" "$(tail -1 "$OUT/$name/orig.check")" "$(tail -1 "$OUT/$name/ref.check")"
  # antisoluções (atalhos/trapaças típicas): o CHECK tem que FALHAR em todas
  for a in "$EV/antisol/$name"/*/; do
    [ -d "$a" ] || continue
    a=${a%/}; v=$(basename "$a"); w="$OUT/$name/anti-$v"; mkdir -p "$w"
    copy_task "$t" "$w"; copy_task "$a" "$w"
    if [ -f "$a/solve.sh" ]; then ( cd "$w" && bash "$a/solve.sh" ) >"$w.solve.log" 2>&1; fi
    ( cd "$w" && TASK="$t" bash "$t/CHECK" ) >"$w.check" 2>&1; ra=$?
    [ -f "$t/CLEANUP" ] && ( cd "$w" && TASK="$t" bash "$t/CLEANUP" ) >>"$w.check" 2>&1
    av=OK; [ $ra -ne 0 ] || { av=BAD; bad=$((bad + 1)); }
    printf "%-24s   antisol %-22s %-4s %s | %s\n" "" "$v" "$([ $ra -eq 0 ] && echo PASS || echo FAIL)" "$av" \
      "$(grep -E '^(PASS|FAIL)' "$w.check" | tail -1)"
  done
done
echo "calibração com problema: $bad"
