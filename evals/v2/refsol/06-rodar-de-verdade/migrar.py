"""Migra os cadastros antigos (dados/*.csv) e imprime um checksum de conferência.

Regras do formato antigo (documento de migração, seção 2):
- separador ';', cabeçalho id;nome;valor.
- Codificação: UTF-8, às vezes com BOM; os arquivos exportados pela filial antiga vêm em
  Latin-1. Tente UTF-8 (aceitando BOM) e, se não decodificar, use Latin-1.
- valor usa vírgula decimal ("1234,5"). Linha com valor vazio é registro cancelado e
  deve ser IGNORADA.
- nome: tire os espaços das pontas e troque espaços internos repetidos por um só.

Saída: uma linha 'arquivo: N registros' por arquivo e, no fim, 'CHECKSUM FINAL: <sha256>',
o sha256 das linhas canônicas 'arquivo|id|nome|valor' (valor com 2 casas), na ordem dos
arquivos e, dentro de cada arquivo, por id crescente.
"""
import glob
import hashlib
import os
import re
import sys


def ler(caminho):
    with open(caminho, "rb") as f:
        dados = f.read()
    try:
        return dados.decode("utf-8-sig")
    except UnicodeDecodeError:
        return dados.decode("latin-1")


def registros(caminho):
    linhas = ler(caminho).splitlines()
    cabecalho = linhas[0].split(";")
    for linha in linhas[1:]:
        if not linha.strip():
            continue
        campos = dict(zip(cabecalho, linha.split(";")))
        if not campos.get("valor", "").strip():
            continue
        nome = re.sub(r"\s+", " ", campos["nome"].strip())
        valor = float(campos["valor"].replace(",", "."))
        yield int(campos["id"]), nome, valor


def main(pasta="dados"):
    h = hashlib.sha256()
    arquivos = sorted(glob.glob(os.path.join(pasta, "*.csv")))
    if not arquivos:
        print(f"nenhum CSV em {pasta}", file=sys.stderr)
        return 1
    for caminho in arquivos:
        nome_arq = os.path.basename(caminho)
        regs = sorted(registros(caminho))
        for id_, nome, valor in regs:
            h.update(f"{nome_arq}|{id_}|{nome}|{valor:.2f}\n".encode("utf-8"))
        print(f"{nome_arq}: {len(regs)} registros")
    print(f"CHECKSUM FINAL: {h.hexdigest()}")
    return 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
