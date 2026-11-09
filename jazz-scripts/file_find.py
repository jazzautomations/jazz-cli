# file_find: find files/dirs under a path by name glob, extension, type, size
# range, mtime window and emptiness — the read-only half of `find`. os.walk with
# a time budget, never follows symlinks, skips .git/node_modules/vendor and (by
# default) hidden entries. Answer is the matching paths, sorted for stable output.
# Python 3.8+ stdlib only.
import fnmatch
import stat
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_SCAN = 300000
SKIP = SKIP_DIRS | set([".git", ".hg", ".svn", ".tox", ".venv", ".cache"])


def as_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    if v < lo or v > hi:
        fail("%s must be %d..%d" % (name, lo, hi))
    return v


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


name = arg("name")
if name is None:
    name = arg("namepat") # 'names contain X' → *X* via fnmatch
if name is not None:
    name = str(name)
# "(case insensitive)": globs/ext já casam dobrados por padrão; o flag
# só muda a regex (sensível como no grep). "case sensitive" pedido é
# impossível de honrar — o match de nome é insensitivo — recusa honesta.
icase = str(arg("icase", "")).strip().lower()
if icase == "sensitive":
    fail("case-sensitive name matching is not supported (names match case-insensitively)")
name_re = arg("name_re")
if name_re is not None:
    name_re = str(name_re)
    if len(name_re) > 200:
        fail("name_re too long")
    try:
        flags = re.IGNORECASE if icase in ("1", "true", "yes") else 0
        name_re = re.compile(name_re, flags)
    except re.error as e:
        # glob-shaped text leaked into the regex slot ("pattern '*.rb'") —
        # fnmatch resolves it honestly instead of dying on the regex
        if any(c in name_re for c in "*?["):
            if not name:
                name = name_re
            name_re = None
        else:
            fail("name_re is not a valid regex: %s" % e)
exclude = arg("exclude")
if exclude is not None:
    # "a|b|c" — um glob por operando da cláusula ("except a and b")
    exclude = [g for g in str(exclude).lower().split("|") if g]
    if not exclude or sum(len(g) for g in exclude) > 120 or any(len(g) > 60 for g in exclude):
        fail("exclude must be short globs like '.git*' or '*_test.go'")
min_depth = as_int("min_depth", 0, 0, 200)
max_depth = as_int("max_depth", 200, 0, 200)
if min_depth > max_depth:
    fail("min_depth is greater than max_depth")
ext = arg("ext")
exts = []
if ext is not None:
    # lista ".cpp,.c,.h" — "all .cpp, .c, .h files" filtra por qualquer uma
    for piece in str(ext).lower().split(","):
        piece = piece.strip().lstrip("*.")
        if not piece or not re.fullmatch(r"[0-9a-z_]+", piece):
            fail("ext must be a plain extension like 'log' or '.py'")
        exts.append(piece)
kind = str(arg("type", "any")).lower()
if kind not in ("any", "file", "dir", "link"):
    fail("type must be any, file, dir or link")
follow_links = as_bool(ARGS.get("follow"), False)
# symlink quebrado/cíclico: o lstat vê o link, o stat do alvo falha
orphan_only = as_bool(ARGS.get("orphan"), False)
# contagem de hardlinks: "exactly 2 links" → st_nlink == N
links_eq = as_int("links", -1, -1, 1 << 30)
min_size = as_int("min_size", 0, 0, 1 << 40)
max_size = as_int("max_size", 1 << 40, 0, 1 << 40)
if min_size > max_size:
    fail("min_size is greater than max_size")
newer_min = arg("newer_min")
older_min = arg("older_min")
newer_ts = older_ts = None
now = time.time()
if newer_min is not None:
    newer_ts = now - as_int("newer_min", 0, 0, 10 * 365 * 24 * 60) * 60
if older_min is not None:
    older_ts = now - as_int("older_min", 0, 0, 10 * 365 * 24 * 60) * 60


def parse_date(s, side):
    # "2014-08-25" | "cur-03-01" (ano corrente; piso que cai no futuro
    # volta um ano — "after February 1" em março quer ESTE ano)
    s = str(s).strip()
    inclusive = s.endswith("~")
    s = s.rstrip("~")
    m = re.fullmatch(r"(cur|next|\d{4})-(\d{2})-(\d{2})", s)
    if not m:
        fail("%s must be a date like 2014-08-25" % side)
    y, mo, dy = m.group(1), int(m.group(2)), int(m.group(3))
    ty = time.localtime(now)
    if y == "cur":
        y = ty.tm_year
        if (mo, dy) > (ty.tm_mon, ty.tm_mday) and side == "newer_date":
            y -= 1  # o piso absoluto mais recente é o do ano passado
    elif y == "next":
        y = ty.tm_year + 1
    else:
        y = int(y)
    if not (1 <= mo <= 12 and 1 <= dy <= 31):
        fail("%s out of range" % side)
    ts = time.mktime((y, mo, dy, 0, 0, 0, 0, 0, -1))
    if inclusive:
        ts += 86400
    return ts


if arg("newer_date") is not None:
    newer_ts = parse_date(arg("newer_date"), "newer_date")
if arg("older_date") is not None:
    older_ts = parse_date(arg("older_date"), "older_date")
if newer_ts and older_ts and newer_ts >= older_ts:
    fail("newer bound must be a wider window than older bound")
TFIELD = {"mtime": "st_mtime", "atime": "st_atime", "ctime": "st_ctime"}
tfield = str(arg("time_field", "mtime")).lower()
if tfield not in TFIELD:
    fail("time_field must be mtime, atime or ctime")
TSKEY = TFIELD[tfield]
# arquivo-referência: "newer than report.txt" compara com o timestamp
# do próprio arquivo, não com a janela em minutos
newer_file = arg("newer_file")
older_file = arg("older_file")
empty_only = as_bool(ARGS.get("empty"), False)
include_hidden = as_bool(ARGS.get("include_hidden"), False)
sort_by = str(arg("sort", "name")).lower()
if sort_by not in ("name", "size", "mtime"):
    fail("sort must be name, size or mtime")
limit = as_int("limit", 200, 1, 5000)
# filtro por contagem de linhas: "only one line", "more than 10 lines"
lines_exact = as_int("lines", -1, -1, 1 << 30)
min_lines = as_int("min_lines", 0, 0, 1 << 30)
max_lines = as_int("max_lines", 1 << 30, 0, 1 << 30)
if lines_exact >= 0:
    min_lines = max_lines = lines_exact
line_filter = min_lines > 0 or max_lines != (1 << 30)
if min_lines > max_lines:
    fail("min_lines is greater than max_lines")


def count_lines(p, stop_above):
    # conta \n com saída antecipada: arquivo acima do teto não precisa
    # ser lido até o fim (log de 2GB não trava o filtro)
    n = 0
    try:
        with open(p, "rb") as fh:
            for chunk in iter(lambda: fh.read(1 << 20), b""):
                n += chunk.count(b"\n")
                if stop_above >= 0 and n > stop_above:
                    return n
    except (IOError, OSError):
        return -1
    return n


def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
    return p


tops_raw = arg("path", ".").split("|")
top_arg = path_in_root(tops_raw[0])
top_is_link = os.path.islink(top_arg)
# busca de links (type/orphan/links) enxerga o próprio top-symlink;
# follow resolve e caminha (o bounded_path já confina o realpath)
if top_is_link and not (follow_links or kind == "link" or
                        orphan_only or links_eq >= 0):
    fail("path is a symlink (not followed): %s" % rel(top_arg))
tops = [bounded_path(v) for v in tops_raw]
for tp in tops:
    if not os.path.exists(tp) and not os.path.islink(tp):
        fail("path not found: %s" % rel(tp))
top = tops[0]
def ref_ts(p):
    # o arquivo-referência vive dentro do scan root — fora disso é fuga
    if os.path.isabs(p) or ".." in p.split(os.sep):
        fail("reference file must be inside the search root")
    try:
        st = os.stat(os.path.join(top, p))
    except OSError:
        fail("reference file not found: %s" % p)
    return getattr(st, TSKEY)
# arquivo-referência usa comparação ESTRITA — o próprio arquivo não é
# "mais novo que si mesmo" (semântica do find -newer)
newer_strict = older_strict = False
if newer_file is not None:
    newer_ts = ref_ts(newer_file)
    newer_strict = True
if older_file is not None:
    older_ts = ref_ts(older_file)
    older_strict = True
if not (name or name_re or exclude or ext or kind != "any" or min_size or max_size != (1 << 40)
        or newer_ts or older_ts or empty_only or min_depth or max_depth != 200 or sort_by != "name"
        or line_filter or newer_file or older_file or orphan_only or
        links_eq >= 0):
    # "find all files under X" is a literal request — unbounded in intent,
    # still bounded by limit/MAX_SCAN like every other query
    name = "*"

if name:
    name_l = name.lower()
    # 'named X' é match EXATO do basename — o wrap *X* faria "file" casar
    # file1/myfile (falso positivo). Substring vem pelo slot namepat.
if name_re and name:
    # regex named the real pattern; a residual 'name' ("names match
    # expression") is cue noise — regex wins
    name = None


def depth_of(p):
    rel_p = os.path.relpath(p, top)
    if rel_p == ".":
        return 0
    return rel_p.count(os.sep) + 1


def match(p, base, st):
    is_dir = stat.S_ISDIR(st.st_mode)
    is_file = stat.S_ISREG(st.st_mode)
    is_link = stat.S_ISLNK(st.st_mode)
    if not (is_dir or is_file or is_link):
        return False  # fifo, socket: not a find hit
    # links só entram quando pedidos — type:link, orphan, links:N ou
    # a enumeração "any" (find(1) lista symlinks do mesmo jeito)
    if is_link and kind in ("file", "dir") and not orphan_only:
        return False
    d = depth_of(p)
    if not (min_depth <= d <= max_depth):
        return False
    if kind == "file" and not is_file:
        return False
    if kind == "dir" and not is_dir:
        return False
    if kind == "link" and not is_link:
        return False
    # quebrado/cíclico: existe o lstat do link, falha o stat do alvo
    if orphan_only and not (is_link and not os.path.exists(p)):
        return False
    if links_eq >= 0 and st.st_nlink != links_eq:
        return False
    if name and not fnmatch.fnmatch(base.lower(), name_l):
        return False
    if name_re and not name_re.search(base):
        return False
    if exclude and any(fnmatch.fnmatch(base.lower(), g)
                       or fnmatch.fnmatch(p.lower(), g)
                       or fnmatch.fnmatch(p.lower(), "*" + g + "*")
                       for g in exclude):
        return False
    if exts and (is_dir or not any(base.lower().endswith("." + e) for e in exts)):
        return False
    if is_file and not (min_size <= st.st_size <= max_size):
        return False
    if is_file and line_filter:
        n = count_lines(p, max_lines)
        if n < 0 or not (min_lines <= n <= max_lines):
            return False
    if newer_ts and (getattr(st, TSKEY) <= newer_ts if newer_strict
                     else getattr(st, TSKEY) < newer_ts):
        return False
    if older_ts and (getattr(st, TSKEY) >= older_ts if older_strict
                     else getattr(st, TSKEY) > older_ts):
        return False
    if empty_only:
        if is_file:
            if st.st_size != 0:
                return False
        else:
            try:
                if os.listdir(p):
                    return False
            except (IOError, OSError):
                return False
    return True


hits, scanned, truncated, warnings = [], 0, False, []
stop = False
hidden = lambda n: n.startswith(".")

def _roots():
    # "asia|emea": cada raiz é varrida em sequência; top vira a raiz
    # corrente pra depth_of/rel calcularem contra a base certa
    for tp in tops:
        if os.path.isfile(tp):
            yield (tp, "__FILE__", None, None)
        else:
            for d, ds, fs in os.walk(tp, followlinks=follow_links):
                yield (tp, d, ds, fs)


# o próprio top pode ser um symlink ("find broken links in LINKED") —
# quando a busca é sobre links, o top entra como candidato além de
# abrir o passeio no caminho já resolvido
if top_is_link:
    try:
        st = os.lstat(top_arg)
        if match(top_arg, os.path.basename(top_arg.rstrip("/")), st):
            hits.append((rel(top_arg).replace(os.sep, "/"), st.st_size,
                         "link", st.st_mtime))
    except (IOError, OSError):
        pass

for top, d, dirs, files in _roots():
    if d == "__FILE__":
        try:
            st = os.lstat(top)
            if match(top, os.path.basename(top), st):
                hits.append((rel(top).replace(os.sep, "/"), st.st_size,
                             "link" if stat.S_ISLNK(st.st_mode) else "file",
                             st.st_mtime))
            scanned += 1
        except (IOError, OSError) as e:
            fail("cannot stat: %s" % e)
        continue
    pruned = []
    for x in dirs:
        if x in SKIP or (not include_hidden and hidden(x)):
            continue
        if os.path.islink(os.path.join(d, x)):
            # symlink de diretório é CANDIDATO nas buscas de link — e
            # com follow ele também desce (os.walk followlinks)
            if follow_links:
                pruned.append(x)
            p = os.path.join(d, x)
            try:
                st = os.lstat(p)
            except (IOError, OSError):
                continue
            scanned += 1
            if match(p, x, st):
                hits.append((rel(p).replace(os.sep, "/"), 0, "link",
                             st.st_mtime))
            continue
        pruned.append(x)
    dirs[:] = sorted(pruned)
    for x in dirs:
        p = os.path.join(d, x)
        try:
            st = os.lstat(p)
        except (IOError, OSError):
            continue
        scanned += 1
        if match(p, x, st):
            hits.append((rel(p).replace(os.sep, "/"), 0, "dir", st.st_mtime))
    for f in sorted(files):
        if not include_hidden and hidden(f):
            continue
        p = os.path.join(d, f)
        try:
            st = os.lstat(p)
        except (IOError, OSError):
            continue
        scanned += 1
        if match(p, f, st):
            hits.append((rel(p).replace(os.sep, "/"), st.st_size,
                         "link" if stat.S_ISLNK(st.st_mode) else "file",
                         st.st_mtime))
    if scanned >= MAX_SCAN:
        truncated, stop = True, True
        warnings.append("stopped at %d entries" % MAX_SCAN)
    if not stop and time.time() - T0 > BUDGET_S:
        truncated, stop = True, True
        warnings.append("time budget reached after %d entries" % scanned)
    if stop:
        break

if sort_by == "size":
    hits.sort(key=lambda h: (-h[1], h[0]))
elif sort_by == "mtime":
    hits.sort(key=lambda h: (-h[3], h[0]))
else:
    hits.sort()
count = len(hits)
if count > limit:
    truncated = True
    warnings.append("%d match(es); showing %d (raise limit)" % (count, limit))
    hits = hits[:limit]

files = [{"path": p, "size": s, "type": t} for p, s, t, _ in hits]
nfiles = sum(1 for h in hits if h[2] == "file")
ndirs = count - nfiles
text = "%d match(es)" % count
if count:
    text += " (%d file(s), %d dir(s)); first: %s" % (nfiles, ndirs, files[0]["path"])
else:
    text += " under %s (%d scanned)" % (rel(top), scanned)
if truncated:
    text += " [truncated]"

emit(True, {"files": files, "count": count, "scanned": scanned,
            "truncated": truncated, "text": text},
     1.0 if not truncated else 0.6, [], warnings)
