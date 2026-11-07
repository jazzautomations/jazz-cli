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
try:
    import grp
except ImportError:
    grp = None


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
want_exec_all = as_bool(ARGS.get("everyone_exec"), False)
want_uread = as_bool(ARGS.get("user_readable"), False)
want_aread = as_bool(ARGS.get("world_readable"), False)
owner = arg("owner")
not_owner = arg("not_owner")
group = arg("group")
not_group = arg("not_group")
no_owner = as_bool(ARGS.get("no_owner"), False)
no_group = as_bool(ARGS.get("no_group"), False)
orphan = as_bool(ARGS.get("orphan"), False)
not_exec = as_bool(ARGS.get("not_executable"), False)
not_ww = as_bool(ARGS.get("not_world_writable"), False)
not_mode = as_bool(ARGS.get("not_mode"), False)
for lbl, val in (("owner", owner), ("not_owner", not_owner),
                 ("group", group), ("not_group", not_group)):
    if val is not None:
        val = str(val)
        if not val or any(c in val for c in "/\\ \t"):
            fail("%s must be a plain name" % lbl)
include_hidden = as_bool(ARGS.get("include_hidden"), False)
try:
    limit = int(arg("limit", 200))
except (TypeError, ValueError):
    fail("limit must be an integer")
limit = max(1, min(limit, 5000))

if (exact is None and not (want_ww or want_suid or want_sgid or want_exec
        or want_exec_all or want_uread or want_aread
        or owner or not_owner or group or not_group or no_owner or no_group
        or orphan or not_exec or not_ww)):
    fail("pass a filter: mode/not_mode, world_writable, suid, sgid, "
         "executable/not_executable, everyone_exec, "
         "user_readable/world_readable, owner/not_owner, group/not_group, "
         "no_owner, no_group or orphan")

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
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
not_owner_uid = None
if not_owner:
    if not pwd:
        fail("owner lookup unavailable on this platform")
    try:
        not_owner_uid = pwd.getpwnam(not_owner).pw_uid
    except KeyError:
        fail("no such user: %s" % not_owner)
group_gid = None
if group:
    if not grp:
        fail("group lookup unavailable on this platform")
    try:
        group_gid = grp.getgrnam(group).gr_gid
    except KeyError:
        fail("no such group: %s" % group)
not_group_gid = None
if not_group:
    if not grp:
        fail("group lookup unavailable on this platform")
    try:
        not_group_gid = grp.getgrnam(not_group).gr_gid
    except KeyError:
        fail("no such group: %s" % not_group)


def gname(gid):
    if not grp:
        return None
    try:
        return grp.getgrgid(gid).gr_name
    except KeyError:
        return None


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
    if want_exec_all and (m & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)) != (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH):
        return False
    if want_uread and not (m & stat.S_IRUSR):
        return False
    if want_aread and (m & (stat.S_IRUSR | stat.S_IRGRP | stat.S_IROTH)) != (stat.S_IRUSR | stat.S_IRGRP | stat.S_IROTH):
        return False
    if not_exec and (m & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)):
        return False
    if not_ww and (m & stat.S_IWOTH):
        return False
    if exact is not None and not_mode and stat.S_IMODE(m) == exact:
        return False
    if owner_uid is not None and st.st_uid != owner_uid:
        return False
    if not_owner_uid is not None and st.st_uid == not_owner_uid:
        return False
    if group_gid is not None and st.st_gid != group_gid:
        return False
    if not_group_gid is not None and st.st_gid == not_group_gid:
        return False
    if no_owner and uname(st.st_uid) is not None:
        return False
    if no_group and gname(st.st_gid) is not None:
        return False
    if orphan and uname(st.st_uid) is not None and gname(st.st_gid) is not None:
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
