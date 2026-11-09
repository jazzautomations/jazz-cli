def eh_palindromo(s):
    """True se s lido de tras pra frente e igual (ignora espacos e maiusculas)."""
    limpo = "".join(c.lower() for c in s if c != " ")
    return limpo == limpo[::-1]
