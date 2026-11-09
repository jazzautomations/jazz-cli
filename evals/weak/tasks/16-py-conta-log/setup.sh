#!/bin/bash
set -u
# Tarefa 16-py-conta-log: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"app.log"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
