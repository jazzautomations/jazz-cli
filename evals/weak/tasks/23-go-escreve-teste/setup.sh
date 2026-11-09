#!/bin/bash
set -u
# Tarefa 23-go-escreve-teste: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"go.mod"
	"par.go"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
