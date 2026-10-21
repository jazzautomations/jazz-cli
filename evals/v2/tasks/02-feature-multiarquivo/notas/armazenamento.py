"""Leitura e gravação do arquivo de notas (JSON).

Formato em disco (versão 1):
    {"versao": 1, "proximo_id": 4, "notas": [{"id": 1, "texto": "...", "tags": [...], "feita": false}]}
"""
import json
import os
from pathlib import Path

from .modelo import Nota

VERSAO = 1


def nota_para_dict(nota: Nota) -> dict:
    return {"id": nota.id, "texto": nota.texto, "tags": list(nota.tags), "feita": nota.feita}


def nota_de_dict(d: dict) -> Nota:
    return Nota(id=int(d["id"]), texto=d["texto"], tags=d.get("tags", []), feita=bool(d.get("feita", False)))


class Bloco:
    def __init__(self, notas=None, proximo_id=1):
        self.notas = list(notas or [])
        self.proximo_id = proximo_id

    def adicionar(self, texto: str, tags=()) -> Nota:
        nota = Nota(id=self.proximo_id, texto=texto, tags=list(tags))
        self.notas.append(nota)
        self.proximo_id += 1
        return nota

    def buscar(self, id_: int) -> Nota:
        for n in self.notas:
            if n.id == id_:
                return n
        raise KeyError(id_)

    def remover(self, id_: int) -> Nota:
        nota = self.buscar(id_)
        self.notas.remove(nota)
        return nota


def carregar(caminho) -> Bloco:
    caminho = Path(caminho)
    if not caminho.exists():
        return Bloco()
    with open(caminho, encoding="utf-8") as f:
        dados = json.load(f)
    notas = [nota_de_dict(d) for d in dados.get("notas", [])]
    proximo = dados.get("proximo_id", max((n.id for n in notas), default=0) + 1)
    return Bloco(notas, proximo)


def salvar(bloco: Bloco, caminho) -> None:
    dados = {"versao": VERSAO, "proximo_id": bloco.proximo_id, "notas": [nota_para_dict(n) for n in bloco.notas]}
    tmp = f"{caminho}.tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(dados, f, ensure_ascii=False, indent=2)
    os.replace(tmp, caminho)
