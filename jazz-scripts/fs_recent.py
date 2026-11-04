# fs_recent: files modified in the last N minutes under a path, newest first, so the
# brain can answer "what changed just now?" without git. os.walk with a time budget,
# never follows symlinks, skips .git/node_modules/vendor/dist and (by default) hidden
# entries. Python 3.8+ stdlib only.
import datetime
import stat
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_FILES = 200000
MAX_MINUTES = 10080  # 7 days
SKIP = SKIP_DIRS | set([".git", ".hg", ".svn", ".tox", ".mypy_cache", ".ruff_cache",
                        ".venv", ".cache", "site-packages"])


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


def as_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    if v < lo or v > hi:
        fail("%s must be %d..%d" % (name, lo, hi))
    return v


minutes = as_int("minutes", 60, 1, MAX_MINUTES)
limit = as_int("limit", 100, 1, 5000)
include_hidden = as_bool(ARGS.get("include_hidden"), False)
top = path_in_root(arg("path", "."))
if os.path.islink(top):
    fail("path is a symlink (not followed): %s" % rel(top))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

NOW = time.time()
cutoff = NOW - minutes * 60
hidden = lambda n: n.startswith(".")


def iso_local(ts):
    dt = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).astimezone()
    return dt.isoformat(timespec="seconds")


def consider(p, st, hits):
    if st.st_mtime >= cutoff:
        hits.append((st.st_mtime, rel(p).replace(os.sep, "/"), st.st_size))


hits, scanned, truncated, warnings = [], 0, False, []

if os.path.isfile(top):
    try:
        consider(top, os.lstat(top), hits)
        scanned = 1
    except (IOError, OSError) as e:
        fail("cannot stat: %s" % e)
else:
    stop = False
    for d, dirs, files in os.walk(top, followlinks=False):
        pruned = []
        for x in dirs:
            if x in SKIP or (not include_hidden and hidden(x)):
                continue
            if os.path.islink(os.path.join(d, x)):
                continue
            pruned.append(x)
        dirs[:] = sorted(pruned)
        for f in sorted(files):
            if not include_hidden and hidden(f):
                continue
            p = os.path.join(d, f)
            try:
                st = os.lstat(p)
            except (IOError, OSError):
                continue
            if not stat.S_ISREG(st.st_mode):
                continue  # symlink, fifo, socket: not a file edit
            scanned += 1
            consider(p, st, hits)
            if scanned >= MAX_FILES:
                truncated, stop = True, True
                warnings.append("stopped at %d files" % MAX_FILES)
                break
        if stop:
            break
        if time.time() - T0 > BUDGET_S:
            truncated = True
            warnings.append("time budget reached after %d files" % scanned)
            break

# newest first; ties broken by path so the output is stable
hits.sort(key=lambda h: (-h[0], h[1]))
count = len(hits)
if count > limit:
    truncated = True
    warnings.append("%d files changed; showing %d (raise limit or lower minutes)" % (count, limit))
    hits = hits[:limit]

files = []
for mtime, path, size in hits:
    age = max(0.0, (NOW - mtime) / 60.0)
    files.append({"path": path, "mtime_iso": iso_local(mtime), "age_min": round(age, 1), "size": size})

if count:
    newest = files[0]
    a = newest["age_min"]
    age_txt = "%d min ago" % int(round(a)) if a < 120 else "%.1f h ago" % (a / 60.0)
    text = "%d file(s) changed in the last %d min (newest: %s, %s)" % (count, minutes, newest["path"], age_txt)
else:
    text = "no file changed in the last %d min (%d scanned)" % (minutes, scanned)
if truncated:
    text += " [truncated]"

emit(True, {"files": files, "count": count, "files_scanned": scanned, "minutes": minutes,
            "truncated": truncated, "text": text}, 1.0 if not truncated else 0.6, [], warnings)
