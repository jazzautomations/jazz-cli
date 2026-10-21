"""Tarefa 07: cotacoes.csv veio de uma resposta real do servidor e nenhum servidor ficou vivo."""
import hashlib
import os
import subprocess
import sys

MODO = sys.argv[1] if len(sys.argv) > 1 else "check"


def vivo_servidor(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return False
    cmd = ""
    try:
        with open(f"/proc/{pid}/cmdline", "rb") as f:
            cmd = f.read().replace(b"\0", b" ").decode(errors="replace")
    except OSError:
        cmd = subprocess.run(["ps", "-p", str(pid), "-o", "command="], capture_output=True, text=True).stdout
    if "servidor.py" not in cmd:
        return False
    try:  # zumbi não conta como vivo
        with open(f"/proc/{pid}/stat") as f:
            if f.read().rsplit(")", 1)[1].split()[0] == "Z":
                return False
    except OSError:
        st = subprocess.run(["ps", "-p", str(pid), "-o", "stat="], capture_output=True, text=True).stdout
        if st.startswith("Z"):
            return False
    return True


def auditoria():
    try:
        return [l.split() for l in open(".servidor.audit", encoding="utf-8") if l.strip()]
    except OSError:
        return []


aud = auditoria()
pids = sorted({int(l[1]) for l in aud if l[0] == "start"})

if MODO == "cleanup":
    import signal
    import time
    for pid in pids:
        if vivo_servidor(pid):
            os.kill(pid, signal.SIGTERM)
            time.sleep(1)
            if vivo_servidor(pid):
                os.kill(pid, signal.SIGKILL)
            print(f"cleanup: servidor {pid} encerrado pelo avaliador")
    sys.exit(0)


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


if not pids:
    fail("o servidor nunca foi iniciado (sem registro em .servidor.audit)")
vivos = [p for p in pids if vivo_servidor(p)]
if vivos:
    fail(f"servidor ainda rodando: pid {vivos}")
servidos = {l[2] for l in aud if l[0] == "served" and len(l) > 2}
if not servidos:
    fail("/cotacoes nunca foi consultado com token válido")
try:
    bruto = open("cotacoes.csv", encoding="utf-8").read()
except OSError:
    fail("cotacoes.csv não existe")
linhas = [l.rstrip() for l in bruto.replace("\r\n", "\n").split("\n")]
while linhas and not linhas[-1]:
    linhas.pop()
canon = "\n".join(linhas) + "\n"
if hashlib.sha256(canon.encode()).hexdigest() not in servidos:
    fail("cotacoes.csv não bate com nenhuma resposta do servidor (formato, ordem ou valores)")
print("PASS")
