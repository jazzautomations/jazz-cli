#!/bin/bash
set -u
# Tarefa 19-git-branch-commit: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"README.md"
	"versao.txt"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
git -C "$d" init -q -b main
git -C "$d" config user.name Tarefa
git -C "$d" config user.email tarefa@exemplo.invalid
git -C "$d" add -A
git -C "$d" commit -qm "primeiro estado"
