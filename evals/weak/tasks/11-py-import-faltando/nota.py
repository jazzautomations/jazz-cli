def limpa(s):
    """Quebra por virgula e normaliza espacos de cada parte."""
    partes = []
    for pedaco in s.split(","):
        limpo = re.sub(r"\s+", " ", pedaco).strip()
        partes.append(limpo)
    return partes
