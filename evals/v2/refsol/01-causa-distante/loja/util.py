"""Funções utilitárias compartilhadas pelos módulos da loja."""
import csv
import unicodedata

PREFIXO = "SKU-"


def normalizar_codigo(codigo: str) -> str:
    """Normaliza um código de produto.

    Os códigos chegam de várias fontes (planilha do fornecedor, site, PDV):
    com ou sem o prefixo "SKU-", em minúsculas, com espaços em volta.
    A forma canônica é sem o prefixo e em maiúsculas: " sku-ab12 " -> "AB12".
    """
    return codigo.strip().upper().removeprefix(PREFIXO)


def sem_acentos(texto: str) -> str:
    nfkd = unicodedata.normalize("NFKD", texto)
    return "".join(c for c in nfkd if not unicodedata.combining(c))


def chave_categoria(nome: str) -> str:
    """Chave de comparação de categorias: sem acento, minúscula, sem espaços nas pontas."""
    return sem_acentos(nome).strip().lower()


def ler_csv(caminho) -> list:
    """Lê um CSV separado por ';' (com cabeçalho). Ignora linhas vazias e comentários '#'."""
    with open(caminho, encoding="utf-8", newline="") as f:
        linhas = [l for l in f if l.strip() and not l.lstrip().startswith("#")]
    leitor = csv.DictReader(linhas, delimiter=";")
    out = []
    for registro in leitor:
        out.append({k.strip(): (v or "").strip() for k, v in registro.items()})
    return out


def parse_quantidade(texto: str) -> int:
    q = int(texto)
    if q <= 0:
        raise ValueError(f"quantidade inválida: {texto!r}")
    return q
