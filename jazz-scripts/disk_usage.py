# disk_usage: filesystem usage (os.statvfs) and the biggest directories/files
# under a path (os.walk), no du/df. Budgeted and deterministic. Python 3.8+ stdlib.
import stat as statmod
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_ENTRIES = 50000

top = os.path.normpath(path_in_root(arg("path", ".")))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

topn = int(arg("top", 15))
topn = 1 if topn < 1 else (200 if topn > 200 else topn)
min_bytes = int(float(arg("min_mb", 0) or 0) * 1024 * 1024)
skip_git = arg("skip_git", True)
if isinstance(skip_git, str):
    skip_git = skip_git.strip().lower() not in ("0", "false", "no", "")

warnings = []

fs = {"total": 0, "used": 0, "free": 0, "used_pct": 0.0}
try:
    st = os.statvfs(top)
    frsize = st.f_frsize or st.f_bsize
    total = st.f_blocks * frsize
    free = st.f_bfree * frsize
    used = total - free
    fs = {"total": total, "used": used, "free": free,
          "used_pct": round(100.0 * used / total, 1) if total else 0.0}
except OSError as e:
    warnings.append("statvfs failed: %s" % e)

walk_root = top if os.path.isdir(top) else os.path.dirname(top) or "."
sizes = {}   # directory -> cumulative bytes of its subtree
files = []   # (path, bytes)
scanned = 0
truncated = False

for dirpath, dirs, filenames in os.walk(walk_root, topdown=True, followlinks=False):
    if skip_git:
        dirs[:] = [d for d in dirs if d != ".git"]
    dirs.sort()
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached")
        break
    for fn in sorted(filenames):
        fp = os.path.join(dirpath, fn)
        try:
            fst = os.lstat(fp)
        except OSError:
            continue
        if not statmod.S_ISREG(fst.st_mode):
            continue  # skip symlinks, sockets, fifos, devices
        sz = fst.st_size
        scanned += 1
        files.append((fp, sz))
        d = dirpath
        while True:
            sizes[d] = sizes.get(d, 0) + sz
            if d == walk_root:
                break
            nd = os.path.dirname(d)
            if nd == d or len(nd) < len(walk_root):
                break
            d = nd
        if scanned >= MAX_ENTRIES:
            truncated = True
            warnings.append("entry budget reached (%d)" % MAX_ENTRIES)
            break
    if truncated:
        break

dir_items = sorted(((p, b) for p, b in sizes.items() if b >= min_bytes),
                   key=lambda x: (-x[1], x[0]))
top_dirs = [{"path": rel(p), "bytes": b} for p, b in dir_items[:topn]]

file_items = sorted(((p, b) for p, b in files if b >= min_bytes),
                    key=lambda x: (-x[1], x[0]))
top_files = [{"path": rel(p), "bytes": b} for p, b in file_items[:topn]]

ms = int((time.time() - T0) * 1000)


def human(n):
    n = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024.0 or unit == "TB":
            return ("%d B" % int(n)) if unit == "B" else ("%.1f %s" % (n, unit))
        n /= 1024.0


text = "fs %s used of %s (%.1f%%); scanned %d file(s)" % (
    human(fs["used"]), human(fs["total"]), fs["used_pct"], scanned)
if top_dirs:
    text += "; biggest dir %s (%s)" % (top_dirs[0]["path"], human(top_dirs[0]["bytes"]))

data = {"fs": fs, "dirs": top_dirs, "files": top_files,
        "scanned": scanned, "truncated": truncated, "ms": ms, "text": text}
emit(True, data, 1.0, [], warnings)
