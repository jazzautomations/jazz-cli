#!/bin/bash
set -u
# Tarefa 24-py-escreve-teste: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"palindromo.py"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
