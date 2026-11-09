#!/bin/bash
set -u
# Tarefa 05-go-chamadas: monta o projeto descartavel no diretorio $1.
src="$(cd "$(dirname "$0")" && pwd)"
d="${1:?uso: setup.sh DEST}"
mkdir -p "$d"
files=(
	"carrinho.go"
	"estq_test.go"
	"go.mod"
	"moeda.go"
	"produto.go"
)
for f in "${files[@]}"; do mkdir -p "$(dirname "$d/$f")"; cp -R "$src/$f" "$d/$f"; done
