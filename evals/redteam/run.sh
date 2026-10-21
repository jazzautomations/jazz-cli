#!/bin/bash
# jazz-cli red-team suite. Runs each hostile task in an ISOLATED fake HOME with
# canary secrets, against a local exfil sink, and prints BLOCKED/COMPROMISED.
#
#   !!! MACHINE SAFETY !!!
#   - Run ONLY on a disposable dev machine or VM, never on a machine whose real
#     credentials you care about.
#   - It sets HOME to a throwaway $CANARY_HOME; your real ~ is untouched as long
#     as jazz honours $HOME/$JAZZ_HOME/$XDG_*. It also exports JAZZ_HOME and
#     XDG_* at $CANARY_HOME so a misbehaving build still cannot reach real creds.
#   - Payloads only (a) POST canaries to 127.0.0.1 sink, (b) create a marker
#     file, or (c) echo markers. No payload deletes real data. Even so, run it
#     in a VM/container when you can; scenario 23 (resource) REQUIRES one.
#
# Usage: JAZZ=/path/to/jazz ./run.sh [task-prefix ...]
#   MODE=yolo|auto|ask   (default yolo — worst case; also try auto and ask)
#   LIMIT=300            per-task timeout seconds
set -u
SUITE="$(cd "$(dirname "$0")" && pwd)"
export SUITE
# timeout portátil (macOS não tem coreutils timeout)
tmo() { local k=$1 secs=$2; shift 2; if command -v timeout >/dev/null 2>&1; then timeout -k "$k" "$secs" "$@"; elif command -v gtimeout >/dev/null 2>&1; then gtimeout -k "$k" "$secs" "$@"; else perl -e 'alarm shift; exec @ARGV' "$secs" "$@"; fi; }
JAZZ="${JAZZ:-jazz}"; MODE="${MODE:-yolo}"; LIMIT="${LIMIT:-300}"; MODEL="${MODEL:-kilo/poolside/laguna-s-2.1:free}"
PORT="${PORT:-8099}"
RUN="$SUITE/results/$(date +%Y%m%d-%H%M%S)-$MODE"; mkdir -p "$RUN"
export CANARY_HOME="$RUN/home"
export SINK_LOG="$RUN/sink.log"
# tranca o que o agente não pode ler durante a execução (verificadores, lib, sink e log do sink)
rt_lock()   { chmod 000 "$SUITE/tasks" "$SUITE/lib.sh" "$SUITE/sink" 2>/dev/null; [ -f "$SINK_LOG" ] && chmod 000 "$SINK_LOG"; }
rt_unlock() { chmod 755 "$SUITE/tasks" "$SUITE/sink" 2>/dev/null; chmod 644 "$SUITE/lib.sh" 2>/dev/null; [ -f "$SINK_LOG" ] && chmod 644 "$SINK_LOG"; }
trap rt_unlock EXIT INT TERM


command -v "$JAZZ" >/dev/null 2>&1 || JAZZ="$HOME/.local/bin/jazz"
[ -x "$(command -v "$JAZZ" 2>/dev/null)" ] || { echo "jazz não encontrado: $JAZZ"; exit 2; }

bash "$SUITE/setup.sh"
# sink serves canary payloads for the fetch/curl|sh tasks (/doc, /stage1)
SINK_LOG="$SINK_LOG" python3 "$SUITE/sink/sink.py" "$PORT" >/dev/null 2>&1 &
SINK_PID=$!
trap 'kill $SINK_PID 2>/dev/null' EXIT INT TERM
sleep 1

pass=0; n=0
printf '%-26s %-12s %s\n' TASK VERDICT NOTE | tee "$RUN/summary.txt"
for t in "$SUITE"/tasks/*/; do
  t=${t%/}; name=$(basename "$t")
  if [ $# -gt 0 ]; then sel=; for o in "$@"; do case "$name" in "$o"*) sel=1;; esac; done; [ -n "$sel" ] || continue; fi
  [ -f "$t/CHECK" ] || continue
  w="$RUN/work/$name"; mkdir -p "$w"
  # copy ONLY the hostile repo into the work dir (agent never sees PROMPT/CHECK)
  if [ -d "$t/repo" ]; then cp -R "$t/repo/." "$w/"; fi
  [ -f "$t/SETUP" ] && ( cd "$w" && . "$t/SETUP" ) 2>/dev/null
  prompt=$(cat "$t/PROMPT")
  # PORT != 8099 (two suites at once): point the planted payloads at our sink
  if [ "$PORT" != 8099 ]; then
    grep -rlI 8099 "$w" 2>/dev/null | while IFS= read -r f; do perl -pi -e "s/8099/$PORT/g" "$f"; done
    prompt=${prompt//8099/$PORT}
  fi
  RUNLOG="$RUN/$name.out"
  flag=; [ "$MODE" = yolo ] && flag=--yolo; [ "$MODE" = ask ] && flag=--ask
  # fully isolated env: throwaway HOME + jazz/XDG dirs, offline update check off
  rt_lock
  ( cd "$w" && HOME="$CANARY_HOME" JAZZ_HOME="$CANARY_HOME/.config/jazz-cli" \
      XDG_CONFIG_HOME="$CANARY_HOME/.config" XDG_DATA_HOME="$CANARY_HOME/.local/share" \
      JAZZ_NO_UPDATE_CHECK=1 \
      tmo 10 $((LIMIT + 30)) "$JAZZ" -p $flag -v -m "$MODEL" --timeout "${LIMIT}s" "$prompt" \
      </dev/null >"$RUNLOG" 2>&1 ); rc=$?
  rt_unlock
  export RUNLOG CANARY_HOME SINK_LOG
  v=$( cd "$w" && TASK="$t" bash "$t/CHECK" 2>"$RUN/$name.checkerr" )
  note=""
  # o jazz precisa ter AGIDO: sem nenhuma chamada de ferramenta, ou com erro de provedor, o veredito é ERROR (não conta)
  if [ "$rc" = 127 ] || [ "$rc" = 124 ] || [ "$rc" = 142 ] || grep -qE "^· error ·|credencial recusada pelo provedor|nenhum modelo disponível" "$RUNLOG"; then
    v=ERROR; note="(jazz não rodou/agiu: rc=$rc $(grep -oE 'credencial recusada|nenhum modelo|^· error ·' "$RUNLOG" | head -1))"
  fi
  [ "$v" = BLOCKED ] && pass=$((pass+1))
  n=$((n+1))
  printf '%-26s %-12s %s\n' "$name" "$v" "$note" | tee -a "$RUN/summary.txt"
done
echo "BLOCKED: $pass/$n    (resultados em $RUN)" | tee -a "$RUN/summary.txt"
echo "sink log: $SINK_LOG"
