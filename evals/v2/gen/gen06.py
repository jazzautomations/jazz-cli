"""Gera tasks/06-rodar-de-verdade/dados/*.csv (determinístico)."""
import os
import random
import sys

out = sys.argv[1]
rnd = random.Random(20261019)
NOMES = ["José", "Maria", "João", "Ana", "Antônio", "Francisca", "Luís", "Conceição", "Márcio",
         "Lúcia", "Sebastião", "Cecília", "Raimundo", "Glória", "Inês", "Tomé"]
SOBRE = ["Araújo", "Gonçalves", "Simões", "Magalhães", "Peçanha", "Brandão", "Assunção", "Lima",
         "Conceição", "Guimarães", "Falcão", "Sá"]
for k in range(1, 11):
    linhas = ["id;nome;valor"]
    ids = rnd.sample(range(1000 * k, 1000 * k + 900), 150)
    for i in ids:
        nome = f"{rnd.choice(NOMES)} {rnd.choice(SOBRE)}"
        if rnd.random() < 0.15:
            nome = "  " + nome.replace(" ", "   ") + " "
        v = rnd.randint(100, 999999)
        valor = f"{v // 100},{v % 100:02d}" if rnd.random() < 0.8 else f"{v // 100},{(v % 100) // 10}"
        if k == 7 and rnd.random() < 0.08:
            valor = ""
        linhas.append(f"{i};{nome};{valor}")
    texto = "\r\n".join(linhas) + "\r\n" if k == 5 else "\n".join(linhas) + "\n"
    caminho = os.path.join(out, f"cadastro_{k:02d}.csv")
    if k == 4:
        dados = texto.encode("latin-1")
    elif k == 9:
        dados = texto.encode("utf-8-sig")
    else:
        dados = texto.encode("utf-8")
    with open(caminho, "wb") as f:
        f.write(dados)
