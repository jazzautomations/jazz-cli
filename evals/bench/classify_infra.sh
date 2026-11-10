#!/bin/bash
# classify_infra: separa falha real de tentativa inválida (rede/hang) lendo o
# log -v do jazz -p. Compartilhado por evals/bench/lib.sh e evals/v2/run.sh.
#
#   classify_infra LOG RC LIMIT [RESULT] [DT]
#     LOG     log -v da tarefa (agent.log / <tarefa>.log)
#     RC      código de saída do agente (124 = prazo)
#     LIMIT   prazo da tarefa em segundos (o --timeout do jazz)
#     RESULT  veredito do verificador (PASS/FAIL); sem ele, PASS se RC=0, senão FAIL
#     DT      duração medida em segundos (opcional; LIMIT+60 ou mais = vigilante externo)
#   Imprime uma palavra: PASS | FAIL | INFRA? | HANG?
#
# Regra (fonte = LOG, linhas do netwait.go):
#   PASS do verificador vale sempre (a tarefa passou, com ou sem rede no meio).
#   INFRA? se RC=124 e a ÚLTIMA linha de rede é "esperando a rede voltar" sem
#          "voltou"/"desisti" depois (morreu esperando), OU se a soma dos
#          "a rede voltou depois de Xs" + "não voltou em Xs — desisti" (espera
#          final) chega a 50% do LIMIT: a rede comeu o prazo, não é o modelo.
#   HANG?  se RC=124 sem o "tempo esgotado" do próprio jazz no log (o 124 veio
#          do vigilante externo, LIMIT+60), ou DT >= LIMIT+60.
#   INFRA? também quando o agente saiu com erro (RC≠0, sem prazo) e a última linha de erro do jazz é do
#          provedor: cota ("limite de requisições atingido"), crédito, credencial, pedido recusado ou
#          falha de rede. Rodada v2 de 10/11/2026 na 0.13.4: 8 de 11 tarefas morreram assim e
#          entraram como FAIL válido (2/11 publicado seria 2/3).
#   senão  FAIL real (ex.: sympy-16106: 9 min sem rede em 45 min continua FAIL).
#
# Pode ser executado direto: bash classify_infra.sh LOG RC LIMIT [RESULT] [DT]
# Quando é "sourced", depois da chamada CLASSIFY_RES guarda a palavra impressa e
# CLASSIFY_NET_S os segundos de espera de rede somados (sem subshell: use
# `classify_infra ... >/dev/null; res=$CLASSIFY_RES`).

# classify_dur_s: duração do Go arredondada a segundos (1h2m3s, 9m0s, 45s) -> segundos
classify_dur_s() {
  local d=$1 h=0 m=0 s=0
  [[ $d =~ ^([0-9]+)h ]] && h=${BASH_REMATCH[1]}
  [[ $d =~ ([0-9]+)m([0-9]|$) ]] && m=${BASH_REMATCH[1]}
  [[ $d =~ ([0-9]+)s$ ]] && s=${BASH_REMATCH[1]}
  echo $(( h * 3600 + m * 60 + s ))
}

classify_infra() {
  local log=$1 rc=${2:-?} limit=${3:-0} result=${4:-} dt=${5:-}
  local line last= net=0 jazz_timeout=0 dur
  CLASSIFY_RES=; CLASSIFY_NET_S=0
  if [ -z "$result" ]; then
    if [ "$rc" = 0 ]; then result=PASS; else result=FAIL; fi
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
  fi
  # o provedor matou a rodada: cota, crédito, credencial, pedido recusado ou resposta do modelo que o
  # provedor não conseguiu entregar (chamada de ferramenta inválida). O agente não chegou a trabalhar
  # ou foi cortado no meio por algo que não é dele. Só a linha FINAL de erro do jazz conta.
  local provdead=0
  if [ -r "$log" ] && [ "$rc" != 0 ] && [ "$rc" != 124 ]; then
    if grep -a '^jazz: ' "$log" 2>/dev/null | grep -a -v -E 'execução incompleta|retome com|pasta não confiável|^jazz: aviso' | tail -n 1 |
       grep -a -q -E 'limite de requisições atingido|cota diária esgotada|sem crédito nesse provedor|credencial recusada pelo provedor|o provedor recusou o pedido|falha de rede:|nenhuma conexão|: HTTP 5[0-9][0-9]|o modelo parou de responder|reserva\(s\) também falharam'; then
      provdead=1
    fi
  fi
  CLASSIFY_NET_S=$net
  if [ "$result" = PASS ]; then
    CLASSIFY_RES=PASS
  elif [ "$rc" = 124 ] && [ "$last" = wait ]; then
    CLASSIFY_RES="INFRA?"
  elif [ "$limit" -gt 0 ] 2>/dev/null && [ $(( net * 2 )) -ge "$limit" ]; then
    CLASSIFY_RES="INFRA?"
  elif [ "$provdead" = 1 ]; then
    CLASSIFY_RES="INFRA?"
  elif [ "$rc" = 124 ] && [ "$jazz_timeout" = 0 ]; then
    CLASSIFY_RES="HANG?"
  elif [ "$rc" = 124 ] && [ -n "$dt" ] && [ "$dt" -ge $(( limit + 60 )) ] 2>/dev/null; then
    CLASSIFY_RES="HANG?"
  else
    CLASSIFY_RES=$result
  fi
  echo "$CLASSIFY_RES"
}

# classify_total_line PASS N INFRA HANG: a linha final dos runners.
#   TOTAL bruto X/N · VÁLIDAS Y/V (Z inválidas: rede R / hang H)
classify_total_line() {
  local pass=$1 n=$2 infra=${3:-0} hang=${4:-0} inv
  inv=$(( infra + hang ))
  echo "TOTAL bruto $pass/$n · VÁLIDAS $pass/$(( n - inv )) ($inv inválidas: rede $infra / hang $hang)"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  if [ $# -lt 3 ]; then echo "uso: $0 LOG RC LIMIT [RESULT] [DT]" >&2; exit 2; fi
  classify_infra "$@"
fi
