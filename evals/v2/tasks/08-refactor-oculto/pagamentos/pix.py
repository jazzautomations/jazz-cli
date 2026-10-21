"""Cobrança via Pix."""
import hashlib

VALOR_MINIMO = 0.01

TIPOS_CHAVE = ("cpf", "email", "telefone", "aleatoria")


def normalizar_documento(doc: str) -> str:
    return "".join(c for c in str(doc) if c.isdigit())


def validar_cpf(cpf: str) -> str:
    """Valida o CPF (com ou sem pontuação) e devolve só os 11 dígitos."""
    d = normalizar_documento(cpf)
    if len(d) != 11 or d == d[0] * 11:
        raise ValueError(f"CPF inválido: {cpf}")
    for n in (9, 10):
        soma = sum(int(d[i]) * (n + 1 - i) for i in range(n))
        dv = (soma * 10) % 11 % 10
        if dv != int(d[n]):
            raise ValueError(f"CPF inválido: {cpf}")
    return d


def formatar_cpf(cpf: str) -> str:
    d = validar_cpf(cpf)
    return f"{d[:3]}.{d[3:6]}.{d[6:9]}-{d[9:]}"


def formatar_reais(valor: float) -> str:
    """1234.5 -> 'R$ 1.234,50'."""
    v = round(valor, 2)
    inteiro, frac = f"{v:.2f}".split(".")
    inteiro = f"{int(inteiro):,}".replace(",", ".")
    return f"R$ {inteiro},{frac}"


def validar_valor(valor: float) -> float:
    if not isinstance(valor, (int, float)) or isinstance(valor, bool):
        raise TypeError("valor deve ser numérico")
    if valor < VALOR_MINIMO:
        raise ValueError(f"valor fora dos limites do pix: {valor}")
    return float(valor)


def tipo_da_chave(chave: str) -> str:
    if "@" in chave:
        return "email"
    digitos = normalizar_documento(chave)
    if chave.startswith("+") and len(digitos) in (12, 13):
        return "telefone"
    if len(digitos) == 11 and digitos == chave.replace(".", "").replace("-", ""):
        return "cpf"
    if len(chave) == 36 and chave.count("-") == 4:
        return "aleatoria"
    raise ValueError(f"chave pix inválida: {chave}")


def gerar_cobranca(valor: float, cpf: str, chave: str) -> dict:
    valor = validar_valor(valor)
    pagador = formatar_cpf(cpf)
    tipo = tipo_da_chave(chave)
    txid = hashlib.sha1(f"{chave}|{validar_cpf(cpf)}|{valor:.2f}".encode()).hexdigest()[:25].upper()
    return {"valor": formatar_reais(valor), "pagador": pagador, "chave": chave, "tipo": tipo, "txid": txid}
