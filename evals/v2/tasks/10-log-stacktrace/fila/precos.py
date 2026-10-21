"""Conversão de preços para centavos.

Os pedidos do ERP mandam o preço como texto no formato brasileiro ("12,90", "1.234,56",
"R$ 7,00"); os pedidos do site mandam número JSON (12.9).
"""


def para_centavos(valor) -> int:
    if isinstance(valor, bool):
        raise TypeError("preço booleano")
    if isinstance(valor, (int, float)):
        return int(round(valor * 100))
    texto = str(valor).replace("R$", "").strip()
    return int(round(float(texto.replace(",", ".")) * 100))


def formatar(centavos: int) -> str:
    reais, cent = divmod(abs(centavos), 100)
    sinal = "-" if centavos < 0 else ""
    return f"{sinal}R$ {reais:,}".replace(",", ".") + f",{cent:02d}"
