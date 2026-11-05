# perm_find: files/dirs under a path selected by permission bits and owner —
# "which files are 777", "suid bit set", "world-writable". Pure lstat walk,
# no chmod, 4s budget, sorted output. Python 3.8+ stdlib only.
import stat
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_SCAN = 300000
SKIP = SKIP_DIRS | set([".git", ".hg", ".svn", ".tox", ".venv", ".cache"])

try:
    import pwd
except ImportError:
    pwd = None


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


mode_s = arg("mode")
exact = None
if mode_s is not None:
    mode_s = str(mode_s).strip().lstrip("0") or "0"
    if not all(c in "01234567" for c in mode_s) or len(mode_s) > 4:
        fail("mode must be an octal like 777 or 4755")
    exact = int(mode_s, 8)

kind = str(arg("type", "any")).lower()
if kind not in ("any", "file", "dir"):
    fail("type must be any, file or dir")
want_ww = as_bool(ARGS.get("world_writable"), False)
want_suid = as_bool(ARGS.get("suid"), False)
want_sgid = as_bool(ARGS.get("sgid"), False)
want_exec = as_bool(ARGS.get("executable"), False)
owner = arg("owner")
if owner is not None:
    owner = str(owner)
    if not owner or any(c in owner for c in "/\\ \t"):
        fail("owner must be a plain user name")
include_hidden = as_bool(ARGS.get("include_hidden"), False)
try:
    limit = int(arg("limit", 200))
except (TypeError, ValueError):
    fail("limit must be an integer")
limit = max(1, min(limit, 5000))

if exact is None and not (want_ww or want_suid or want_sgid or want_exec or owner):
    fail("pass a filter: mode, world_writable, suid, sgid, executable or owner")

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root")
    return p


top_arg = path_in_root(arg("path", "."))
if os.path.islink(top_arg):
    fail("path is a symlink (not followed): %s" % rel(top_arg))
top = bounded_path(arg("path", "."))
if not os.path.isdir(top):
    fail("path is not a directory: %s" % rel(top))

owner_uid = None
if owner:
    if not pwd:
        fail("owner lookup unavailable on this platform")
    try:
        owner_uid = pwd.getpwnam(owner).pw_uid
    except KeyError:
        fail("no such user: %s" % owner)


def uname(uid):
    if not pwd:
        return None
    try:
        return pwd.getpwuid(uid).pw_name
    except KeyError:
        return None


def match(st):
    m = st.st_mode
    is_dir = stat.S_ISDIR(m)
    if not (is_dir or stat.S_ISREG(m)):
        return False
    if kind == "file" and is_dir:
        return False
    if kind == "dir" and not is_dir:
        return False
    if exact is not None and stat.S_IMODE(m) != exact:
        return False
    if want_ww and not (m & stat.S_IWOTH):
        return False
    if want_suid and not (m & stat.S_ISUID):
        return False
    if want_sgid and not (m & stat.S_ISGID):
        return False
    if want_exec and not (m & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)):
        return False
    if owner_uid is not None and st.st_uid != owner_uid:
        return False
    return True


hits, scanned, truncated, warnings = [], 0, False, []
stop = False
hidden = lambda n: n.startswith(".")
for d, dirs, files in os.walk(top, followlinks=False):
    pruned = []
    for x in dirs:
        if x in SKIP or (not include_hidden and hidden(x)):
            continue
        if os.path.islink(os.path.join(d, x)):
            continue
        pruned.append(x)
    dirs[:] = sorted(pruned)
    for x in dirs + files:
        if not include_hidden and hidden(x):
            continue
        p = os.path.join(d, x)
        try:
            st = os.lstat(p)
        except (IOError, OSError):
            continue
        scanned += 1
        if match(st):
            hits.append((rel(p).replace(os.sep, "/"), stat.S_IMODE(st.st_mode),
                         uname(st.st_uid), "dir" if stat.S_ISDIR(st.st_mode) else "file"))
    if scanned >= MAX_SCAN or time.time() - T0 > BUDGET_S:
        truncated, stop = True, True
        warnings.append("stopped after %d entries" % scanned)
        break

hits.sort()
count = len(hits)
if count > limit:
    truncated = True
    warnings.append("%d match(es); showing %d (raise limit)" % (count, limit))
    hits = hits[:limit]

out = [{"path": p, "mode": "%04o" % m, "owner": o, "type": t} for p, m, o, t in hits]
text = "%d match(es)" % count
if count:
    text += "; first: %s (%s)" % (out[0]["path"], out[0]["mode"])
else:
    text += " under %s (%d scanned)" % (rel(top), scanned)
if truncated:
    text += " [truncated]"

emit(True, {"files": out, "count": count, "scanned": scanned,
            "truncated": truncated, "text": text},
     1.0 if not truncated else 0.6, [], warnings)
