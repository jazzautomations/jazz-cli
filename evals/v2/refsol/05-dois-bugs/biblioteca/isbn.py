"""Validação e formatação de ISBN-13."""


def limpar(isbn: str) -> str:
    return "".join(c for c in isbn if c.isdigit())


def digito_verificador(doze: str) -> int:
    """Dígito verificador do ISBN-13 a partir dos 12 primeiros dígitos (pesos 1,3,1,3...)."""
    if len(doze) != 12 or not doze.isdigit():
        raise ValueError("são necessários 12 dígitos")
    soma = sum(int(d) * (1 if i % 2 == 0 else 3) for i, d in enumerate(doze))
    return (10 - soma % 10) % 10


def valido(isbn: str) -> bool:
    s = limpar(isbn)
    if len(s) != 13 or not s.startswith(("978", "979")):
        return False
    return digito_verificador(s[:12]) == int(s[12])


def formatar(isbn: str) -> str:
    """'9788535902778' -> '978-85-359-0277-8' (agrupamento fixo usado nas etiquetas)."""
    s = limpar(isbn)
    if not valido(s):
        raise ValueError(f"ISBN inválido: {isbn}")
    return f"{s[:3]}-{s[3:5]}-{s[5:8]}-{s[8:12]}-{s[12]}"
