#!/bin/bash
PVDIR="docs tutoriais"
DESTINO=backup
mkdir -p "$DESTINO"
for arq in "$PVDIR"/*.txt; do
    cp $arq $DESTINO
    echo "copiado: $arq"
done
