#!/bin/bash
set -u
# Tarefa 17-sh-script-bug: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"backup.sh"
	"docs tutoriais/nota 2.txt"
	"docs tutoriais/nota1.txt"
	"docs tutoriais/rascunho.md"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
chmod +x "$d/backup.sh"
rm -rf "$d/backup"
