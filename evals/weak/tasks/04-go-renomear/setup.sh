#!/bin/bash
set -u
# Tarefa 04-go-renomear: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"catalogo.go"
	"catalogo_test.go"
	"cmd/grita/main.go"
	"go.mod"
	"relatorio.go"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
