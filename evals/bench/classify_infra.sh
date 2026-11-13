#!/bin/bash
# classify_infra: separa falha real de tentativa inválida (rede/cota/troca de
# modelo/hang) lendo as métricas e o log -v do jazz -p. Compartilhado por
# evals/bench/lib.sh e evals/v2/run.sh.
#
#   classify_infra LOG RC LIMIT [RESULT] [DT] [METRICS] [MODEL]
#     LOG     log -v da tarefa (agent.log / <tarefa>.log)
#     RC      código de saída do agente (124 = prazo)
#     LIMIT   prazo da tarefa em segundos (o --timeout do jazz)
#     RESULT  veredito do verificador (PASS/FAIL); sem ele, PASS se RC=0, senão FAIL
#     DT      duração medida em segundos (opcional; LIMIT+60 ou mais = vigilante externo)
#     METRICS o --metrics-out da tarefa (opcional; precisa de python3)
#     MODEL   o modelo pedido (-m); vazio = não confere troca
#   Imprime uma palavra: PASS | FAIL | FAIL-HARNESS | SUBST | INFRA-NOSTART | INFRA? | HANG?
#   CLASSIFY_VERSION (ambiente) = versão do jazz da rodada, para o FAIL-HARNESS
#   (sem ela, a versão vem de uma linha "jazz v0.x.y" do log, se houver).
#
# Regra, nesta ordem (métricas primeiro: o texto PT do log muda, o JSON não):
#   SUBST  as métricas mostram request OK servido por modelo diferente do pedido:
#          a nota não é do modelo pedido, nem o PASS (CLASSIFY_SERVED diz quem serviu).
#   PASS do verificador vale (a tarefa passou, com ou sem rede no meio).
#   FAIL-HARNESS  jazz >= 0.13.5 e a última linha de erro é "falha de rede: Failed to
#          parse tool call": o #33 voltou (tool call recusada tratada como rede).
#          É falha VÁLIDA (culpa do jazz, não da infra).
#   INFRA-NOSTART  métricas com 0 request OK: o modelo nunca respondeu, nada foi medido.
#   INFRA? se RC=124 e a ÚLTIMA linha de rede é "esperando a rede voltar" sem
#          "voltou"/"desisti" depois (morreu esperando), OU se a soma dos
#          "a rede voltou depois de Xs" + "não voltou em Xs — desisti" (espera
#          final) chega a 50% do LIMIT: a rede comeu o prazo, não é o modelo.
#   INFRA? também quando o agente saiu com erro (RC≠0, sem prazo) e a última linha de erro do jazz é do
#          provedor: cota ("limite de requisições atingido"), crédito, credencial, pedido recusado ou
#          falha de rede. Rodada v2 de 10/11/2026 na 0.13.4: 8 de 11 tarefas morreram assim e
#          entraram como FAIL válido (2/11 publicado seria 2/3).
#   HANG?  se RC=124 sem o "tempo esgotado" do próprio jazz no log (o 124 veio
#          do vigilante externo, LIMIT+60), ou DT >= LIMIT+60.
#   senão  FAIL real (ex.: sympy-16106: 9 min sem rede em 45 min continua FAIL).
#
# Pode ser executado direto: bash classify_infra.sh LOG RC LIMIT [RESULT] [DT] [METRICS] [MODEL]
# Quando é "sourced", depois da chamada CLASSIFY_RES guarda a palavra impressa,
# CLASSIFY_KIND o tipo da inválida (rede | cota | troca | hang; "" = válida),
# CLASSIFY_SERVED os modelos que serviram no lugar (SUBST) e CLASSIFY_NET_S os
# segundos de espera de rede somados (sem subshell: use
# `classify_infra ... >/dev/null; res=$CLASSIFY_RES`); classify_count soma
# CLASSIFY_KIND em CLASSIFY_N_REDE/COTA/TROCA/HANG para a linha de totais.

# classify_dur_s: duração do Go arredondada a segundos (1h2m3s, 9m0s, 45s) -> segundos
classify_dur_s() {
  local d=$1 h=0 m=0 s=0
  [[ $d =~ ^([0-9]+)h ]] && h=${BASH_REMATCH[1]}
  [[ $d =~ ([0-9]+)m([0-9]|$) ]] && m=${BASH_REMATCH[1]}
  [[ $d =~ ([0-9]+)s$ ]] && s=${BASH_REMATCH[1]}
  echo $(( h * 3600 + m * 60 + s ))
}

# classify_ver_ge A B: versão A (x.y.z) >= B
classify_ver_ge() {
  local a=$1 b=$2 i x y
  for i in 1 2 3; do
    x=$(echo "$a" | cut -d. -f"$i"); y=$(echo "$b" | cut -d. -f"$i")
    x=${x%%[!0-9]*}; y=${y%%[!0-9]*}
    [ "${x:-0}" -gt "${y:-0}" ] && return 0
    [ "${x:-0}" -lt "${y:-0}" ] && return 1
  done
  return 0
}

# classify_same_model ASKED SERVED: SERVED is the model asked for. The metrics
# carry the resolved ref ("gemini/gemini-3.8-flash"); a bare provider in -m
# ("-m gemini") resolves to that provider's default (Config.ResolveModel), so
# any model of the same provider is the one asked for, not a substitution.
classify_same_model() {
  [ "$2" = "$1" ] && return 0
  case $1 in */*) return 1 ;; esac
  case $2 in "$1"/*) return 0 ;; esac
  return 1
}

# classify_metrics FILE: "OK MODELOS" de um --metrics-out (OK = requests - request_errors;
# MODELOS = os que serviram request OK, separados por vírgula, "-" = nenhum)
classify_metrics() {
  python3 -I - "$1" <<'PY'
import json, sys
try:
    m = json.load(open(sys.argv[1], encoding="utf-8"))
    req, err = int(m.get("requests") or 0), int(m.get("request_errors") or 0)
    print(req - err, ",".join(sorted(k for k, v in (m.get("models") or {}).items() if k and v)) or "-")
except (OSError, ValueError, AttributeError, TypeError):
    print("? -")
PY
}

classify_infra() {
  local log=$1 rc=${2:-?} limit=${3:-0} result=${4:-} dt=${5:-} metrics=${6:-} model=${7:-}
  local line last= net=0 jazz_timeout=0 dur ok=? served=- ver=${CLASSIFY_VERSION:-}
  CLASSIFY_RES=; CLASSIFY_KIND=; CLASSIFY_SERVED=; CLASSIFY_NET_S=0
  if [ -z "$result" ]; then
    if [ "$rc" = 0 ]; then result=PASS; else result=FAIL; fi
  fi
  if [ -n "$metrics" ] && [ -r "$metrics" ] && command -v python3 >/dev/null 2>&1; then
    read -r ok served < <(classify_metrics "$metrics")
  fi
  if [ -r "$log" ]; then
    # só as linhas que interessam (grep -a: o log pode ter bytes fora do UTF-8)
    while IFS= read -r line; do
      case $line in
        *"esperando a rede voltar"*) last=wait ;;
        *"a rede voltou depois de "*)
          last=back; dur=${line##*"a rede voltou depois de "}; dur=${dur%% *}
          net=$(( net + $(classify_dur_s "$dur") )) ;;
        *"desisti de esperar"*)
          last=gaveup; dur=${line##*" voltou em "}; dur=${dur%% *}
          net=$(( net + $(classify_dur_s "$dur") )) ;;
        *"tempo esgotado"*) jazz_timeout=1 ;;
      esac
    done < <(grep -a -E 'esperando a rede voltar|a rede voltou depois de|desisti de esperar|tempo esgotado' "$log" 2>/dev/null)
    [ -n "$ver" ] || ver=$(grep -a -o -E -m1 'jazz(-cli)?:? v?[0-9]+\.[0-9]+\.[0-9]+' "$log" 2>/dev/null | grep -o -E '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
  fi
  # o provedor matou a rodada: cota, crédito, credencial, pedido recusado ou resposta do modelo que o
  # provedor não conseguiu entregar (chamada de ferramenta inválida). O agente não chegou a trabalhar
  # ou foi cortado no meio por algo que não é dele. Só a linha FINAL de erro do jazz conta.
  local provdead=0 lastjazz= quota=0
  if [ -r "$log" ]; then
    lastjazz=$(grep -a '^jazz: ' "$log" 2>/dev/null | grep -a -v -E 'execução incompleta|retome com|pasta não confiável|^jazz: aviso' | tail -n 1)
  fi
  if [ "$rc" != 0 ] && [ "$rc" != 124 ] &&
     printf '%s' "$lastjazz" | grep -a -q -E 'limite de requisições atingido|cota diária esgotada|sem crédito nesse provedor|credencial recusada pelo provedor|o provedor recusou o pedido|falha de rede:|nenhuma conexão|: HTTP 5[0-9][0-9]|o modelo parou de responder|reserva\(s\) também falharam'; then
    provdead=1
  fi
  if printf '%s' "$lastjazz" | grep -a -q -i -E 'limite de requisições|cota|crédito|sign up|/key|omni add|HTTP 429'; then quota=1; fi
  CLASSIFY_NET_S=$net
  local other= m
  if [ -n "$model" ] && [ "$served" != "-" ]; then
    for m in ${served//,/ }; do classify_same_model "$model" "$m" || other="${other:+$other,}$m"; done
  fi
  if [ -n "$other" ]; then
    CLASSIFY_RES=SUBST; CLASSIFY_KIND=troca; CLASSIFY_SERVED=$other
  elif [ "$result" = PASS ]; then
    CLASSIFY_RES=PASS
  elif [ -n "$ver" ] && classify_ver_ge "$ver" 0.13.5 && printf '%s' "$lastjazz" | grep -a -q 'falha de rede: Failed to parse tool call'; then
    CLASSIFY_RES=FAIL-HARNESS
  elif [ "$ok" = 0 ]; then
    CLASSIFY_RES=INFRA-NOSTART
    if [ "$quota" = 1 ]; then CLASSIFY_KIND=cota; else CLASSIFY_KIND=rede; fi
  elif [ "$rc" = 124 ] && [ "$last" = wait ]; then
    CLASSIFY_RES="INFRA?"; CLASSIFY_KIND=rede
  elif [ "$limit" -gt 0 ] 2>/dev/null && [ $(( net * 2 )) -ge "$limit" ]; then
    CLASSIFY_RES="INFRA?"; CLASSIFY_KIND=rede
  elif [ "$provdead" = 1 ]; then
    CLASSIFY_RES="INFRA?"
    if [ "$quota" = 1 ]; then CLASSIFY_KIND=cota; else CLASSIFY_KIND=rede; fi
  elif [ "$rc" = 124 ] && [ "$jazz_timeout" = 0 ]; then
    CLASSIFY_RES="HANG?"; CLASSIFY_KIND=hang
  elif [ "$rc" = 124 ] && [ -n "$dt" ] && [ "$dt" -ge $(( limit + 60 )) ] 2>/dev/null; then
    CLASSIFY_RES="HANG?"; CLASSIFY_KIND=hang
  else
    CLASSIFY_RES=$result
  fi
  echo "$CLASSIFY_RES"
}

# classify_count: soma o CLASSIFY_KIND da última chamada nos contadores da linha de totais.
CLASSIFY_N_REDE=0; CLASSIFY_N_COTA=0; CLASSIFY_N_TROCA=0; CLASSIFY_N_HANG=0
classify_count() {
  case $CLASSIFY_KIND in
    rede) CLASSIFY_N_REDE=$(( CLASSIFY_N_REDE + 1 )) ;;
    cota) CLASSIFY_N_COTA=$(( CLASSIFY_N_COTA + 1 )) ;;
    troca) CLASSIFY_N_TROCA=$(( CLASSIFY_N_TROCA + 1 )) ;;
    hang) CLASSIFY_N_HANG=$(( CLASSIFY_N_HANG + 1 )) ;;
  esac
}

# classify_total_line PASS N REDE COTA TROCA HANG: a linha final dos runners.
#   TOTAL bruto X/N · VÁLIDAS Y/V (inválidas: rede R / cota Q / troca S / hang H)
classify_total_line() {
  local pass=$1 n=$2 rede=${3:-0} cota=${4:-0} troca=${5:-0} hang=${6:-0} inv
  inv=$(( rede + cota + troca + hang ))
  echo "TOTAL bruto $pass/$n · VÁLIDAS $pass/$(( n - inv )) (inválidas: rede $rede / cota $cota / troca $troca / hang $hang)"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  if [ $# -lt 3 ]; then echo "uso: $0 LOG RC LIMIT [RESULT] [DT] [METRICS] [MODEL]" >&2; exit 2; fi
  classify_infra "$@"
fi
