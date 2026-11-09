#!/bin/bash
set -u
# Tarefa 21-makefile-corrigir: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"Makefile"
	"calc.py"
	"rascunho.txt"
	"test_calc.py"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
