# file_split: divide um arquivo TEXTO em pedaços de N linhas — semântica de
# `split -l N file x` (pedaços xaa, xab, ...) dentro de <arquivo>.parts/ ao
# lado da fonte. Determinístico, sem shell, confinado ao root do projeto.
# Divisão por bytes/quantidade de partes fica fora de escopo: só linhas.
# Python 3.8+ stdlib only.
MAX_BYTES = 256 << 20
MAX_CHUNKS = 4096


def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
    return p


def suffix(i):
    # a convenção do split: aa, ab, ..., az, ba, ..., zz, aaa, ... base-26
    s = ""
    i += 1
    while i > 0:
        i, r = divmod(i - 1, 26)
        s = chr(ord("a") + r) + s
    return s if len(s) >= 2 else "a" + s


n = arg("lines")
try:
    n = int(n)
except (TypeError, ValueError):
    fail("lines must be an integer — split é por linhas (ex.: 'partes de 500 linhas'); por bytes ou quantidade de partes não é suportado")
if n < 1 or n > (1 << 24):
    fail("lines must be 1..%d" % (1 << 24))

src = bounded_path(arg("file", ""))
if not os.path.isfile(src):
    fail("not a regular file: %s" % rel(src))
try:
    if os.path.getsize(src) > MAX_BYTES:
        fail("file too big (%d bytes, cap %d)" % (os.path.getsize(src), MAX_BYTES))
    with open(src, "rb") as f:
        data = f.read()
except OSError as e:
    fail("cannot read file: %s" % e)
if not data:
    fail("file is empty — nothing to split: %s" % rel(src))
if b"\x00" in data[: 1 << 16]:
    fail("arquivo binário — split por linhas é só pra texto; dividir por bytes/pedaços não é suportado")

lines = data.splitlines(keepends=True)
if len(lines) > MAX_CHUNKS * n:
    fail("split geraria %d pedaços (teto %d) — aumente lines ou divida uma fatia" % ((len(lines) + n - 1) // n, MAX_CHUNKS))

prefix = str(arg("prefix", "x"))
if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]{0,15}", prefix):
    fail("prefix must be a short name like 'x' or 'chunk'")
# default: <arquivo>.parts/ ao lado da fonte (a convenção do split é cwd —
# aqui o "cwd" natural é o diretório do próprio alvo, sempre dentro do root)
outdir_arg = arg("outdir")
if not outdir_arg:
    outdir_arg = os.path.join(os.path.dirname(rel(src)), os.path.basename(src) + ".parts")
    if outdir_arg.startswith(os.sep):
        outdir_arg = outdir_arg[1:]
outdir = bounded_path(outdir_arg)
if os.path.commonpath([os.path.realpath(ROOT), outdir]) != os.path.realpath(ROOT):
    fail("outdir outside project root")
if os.path.exists(outdir) and not os.path.isdir(outdir):
    fail("outdir exists and is not a directory: %s" % rel(outdir))
try:
    os.makedirs(outdir, exist_ok=True)
    # chunks de uma rodada anterior com o mesmo prefixo são artefato nosso —
    # limpa pra soma de linhas conferir exata, nunca sobra de split maior.
    for stale in os.listdir(outdir):
        if re.fullmatch(re.escape(prefix) + r"[a-z]{2,8}", stale):
            os.remove(os.path.join(outdir, stale))
except OSError as e:
    fail("cannot prepare outdir: %s" % e)

files = []
total = 0
for i in range(0, len(lines), n):
    chunk = lines[i:i + n]
    p = os.path.join(outdir, prefix + suffix(len(files)))
    try:
        with open(p, "wb") as f:
            f.write(b"".join(chunk))
    except OSError as e:
        fail("cannot write %s: %s" % (rel(p), e))
    cnt = sum(1 for l in chunk if l.endswith(b"\n")) + (1 if chunk and not chunk[-1].endswith(b"\n") else 0)
    files.append({"path": rel(p).replace(os.sep, "/"), "lines": cnt})
    total += cnt

text = "%s → %d pedaço(s) de %d linha(s) em %s/ (%s)" % (
    rel(src), len(files), n, rel(outdir),
    "+".join(str(f["lines"]) for f in files[:8]) + ("+…" if len(files) > 8 else ""))
emit(True, {"source": rel(src).replace(os.sep, "/"), "lines": n,
            "dir": rel(outdir).replace(os.sep, "/"), "files": files,
            "count": len(files), "lines_source": len(lines),
            "lines_total": total, "text": text})
