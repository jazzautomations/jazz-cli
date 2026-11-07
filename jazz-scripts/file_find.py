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
if name is not None:
    name = str(name)
name_re = arg("name_re")
if name_re is not None:
    name_re = str(name_re)
    if len(name_re) > 200:
        fail("name_re too long")
    try:
        name_re = re.compile(name_re)
    except re.error as e:
        fail("name_re is not a valid regex: %s" % e)
exclude = arg("exclude")
if exclude is not None:
    exclude = str(exclude).lower()
    if not exclude or len(exclude) > 120:
        fail("exclude must be a short glob like '.git*' or '*_test.go'")
min_depth = as_int("min_depth", 0, 0, 200)
max_depth = as_int("max_depth", 200, 0, 200)
if min_depth > max_depth:
    fail("min_depth is greater than max_depth")
ext = arg("ext")
if ext is not None:
    ext = str(ext).lower().lstrip("*.")
    if not ext or not re.fullmatch(r"[0-9a-z_]+", ext):
        fail("ext must be a plain extension like 'log' or '.py'")
kind = str(arg("type", "any")).lower()
if kind not in ("any", "file", "dir"):
    fail("type must be any, file or dir")
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
if newer_ts and older_ts and newer_ts >= older_ts:
    fail("newer_min must be a wider window than older_min")
TFIELD = {"mtime": "st_mtime", "atime": "st_atime", "ctime": "st_ctime"}
tfield = str(arg("time_field", "mtime")).lower()
if tfield not in TFIELD:
    fail("time_field must be mtime, atime or ctime")
TSKEY = TFIELD[tfield]
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


top_arg = path_in_root(arg("path", "."))
if os.path.islink(top_arg):
    fail("path is a symlink (not followed): %s" % rel(top_arg))
top = bounded_path(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
if not (name or name_re or exclude or ext or kind != "any" or min_size or max_size != (1 << 40)
        or newer_ts or older_ts or empty_only or min_depth or max_depth != 200 or sort_by != "name"
        or line_filter):
    # "find all files under X" is a literal request — unbounded in intent,
    # still bounded by limit/MAX_SCAN like every other query
    name = "*"

if name:
    name_l = name.lower()
    if not any(c in name_l for c in "*?["):
        name_l = "*" + name_l + "*"
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
    if not (is_dir or is_file):
        return False  # symlink, fifo, socket: not a find hit
    d = depth_of(p)
    if not (min_depth <= d <= max_depth):
        return False
    if kind == "file" and not is_file:
        return False
    if kind == "dir" and not is_dir:
        return False
    if name and not fnmatch.fnmatch(base.lower(), name_l):
        return False
    if name_re and not name_re.search(base):
        return False
    if exclude and (fnmatch.fnmatch(base.lower(), exclude)
                    or fnmatch.fnmatch(p.lower(), exclude)
                    or fnmatch.fnmatch(p.lower(), "*" + exclude + "*")):
        return False
    if ext and (is_dir or not base.lower().endswith("." + ext)):
        return False
    if is_file and not (min_size <= st.st_size <= max_size):
        return False
    if is_file and line_filter:
        n = count_lines(p, max_lines)
        if n < 0 or not (min_lines <= n <= max_lines):
            return False
    if newer_ts and getattr(st, TSKEY) < newer_ts:
        return False
    if older_ts and getattr(st, TSKEY) > older_ts:
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

if os.path.isfile(top):
    try:
        st = os.lstat(top)
        if match(top, os.path.basename(top), st):
            hits.append((rel(top).replace(os.sep, "/"), st.st_size, "file", st.st_mtime))
        scanned = 1
    except (IOError, OSError) as e:
        fail("cannot stat: %s" % e)
else:
    for d, dirs, files in os.walk(top, followlinks=False):
        pruned = []
        for x in dirs:
            if x in SKIP or (not include_hidden and hidden(x)):
                continue
            if os.path.islink(os.path.join(d, x)):
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
                hits.append((rel(p).replace(os.sep, "/"), st.st_size, "file", st.st_mtime))
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
