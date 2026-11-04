# git_conflict_scan: conflicts left behind. Two sources: the index (`git diff
# --diff-filter=U`: paths still unmerged) and the working tree (lines starting with
# <<<<<<<, |||||||, ======= or >>>>>>>). A bare `=======` only counts inside an open
# `<<<<<<<` block, so reStructuredText/Markdown underlines are not conflicts. Skips
# hidden and vendored dirs, symlinks, binaries (NUL in the first 8 KB) and files over
# MAX_FILE_BYTES. Python 3.8+ stdlib only.
import re
import time

T0 = time.time()
BUDGET_S = 4.0
GIT_TIMEOUT = 5
MAX_FILES = 20000
MAX_FILE_BYTES = 2 << 20
SNIFF = 8192
MARKER_RE = re.compile(rb"^(<{7}|\|{7}|={7}|>{7})(?:[ \t]|$)")

top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
try:
    limit = max(1, min(5000, int(arg("limit", 200))))
except (TypeError, ValueError):
    fail("limit must be an integer")

cwd = top if os.path.isdir(top) else os.path.dirname(top) or "."
code, out, err = run_argv(["git", "rev-parse", "--show-toplevel"], timeout=GIT_TIMEOUT, cwd=cwd)
if code == 127:
    fail("git not installed")
if code != 0:
    fail("not a git repository")
warnings = []
truncated = False

# --- index: paths in unmerged state, relative to the scanned path ---
argv = ["git", "-c", "core.quotepath=false", "diff", "--name-only", "--diff-filter=U", "--relative"]
if os.path.isfile(top):
    argv += ["--", os.path.basename(top)]
code, out, err = run_argv(argv, timeout=GIT_TIMEOUT, cwd=cwd)
in_index = []
if code == 0:
    in_index = sorted(set(rel(os.path.join(cwd, l.strip())) for l in out.splitlines() if l.strip()))
elif code == 124:
    warnings.append("git diff timed out; index state unknown")
    truncated = True
else:
    warnings.append("git diff failed: %s" % clip(err.strip().splitlines()[-1] if err.strip() else "exit %d" % code, 120))


def scan(path):
    """Conflict markers in one file: [(line, marker)], or None when skipped."""
    try:
        if os.path.islink(path) or os.path.getsize(path) > MAX_FILE_BYTES:
            return None
        with open(path, "rb") as f:
            head = f.read(SNIFF)
            if b"\0" in head:
                return None
            rest = f.read()
    except (IOError, OSError):
        return None
    found = []
    open_block = False
    for i, line in enumerate((head + rest).splitlines(), 1):
        m = MARKER_RE.match(line)
        if not m:
            continue
        marker = m.group(1).decode("ascii")
        if marker == "<<<<<<<":
            open_block = True
        elif marker in ("=======", "|||||||"):
            if not open_block:
                continue
        elif marker == ">>>>>>>":
            open_block = False
        found.append((i, marker))
    return found


# --- working tree ---
in_files = []
files_scanned = 0
files_with = 0
count = 0
candidates = [top] if os.path.isfile(top) else walk_files(top, limit=MAX_FILES)
for path in candidates:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % files_scanned)
        break
    hits = scan(path)
    if hits is None:
        continue
    files_scanned += 1
    if not hits:
        continue
    files_with += 1
    count += len(hits)
    r = rel(path)
    for line, marker in hits:
        in_files.append({"file": r, "line": line, "marker": marker})
if files_scanned >= MAX_FILES:
    truncated = True
    warnings.append("stopped at %d files" % MAX_FILES)
in_files.sort(key=lambda h: (h["file"], h["line"]))
if len(in_files) > limit:
    truncated = True
    in_files = in_files[:limit]

clean = not in_index and count == 0
data = {
    "in_index": in_index,
    "in_files": in_files,
    "count": count,
    "files_with_markers": files_with,
    "files_scanned": files_scanned,
    "clean": clean,
    "truncated": truncated,
    "ms": int((time.time() - T0) * 1000),
}
if clean:
    text = "clean"
else:
    parts = []
    if count:
        parts.append("%d conflict marker%s in %d file%s" % (count, "" if count == 1 else "s", files_with, "" if files_with == 1 else "s"))
    if in_index:
        parts.append("%d file%s unmerged in the index" % (len(in_index), "" if len(in_index) == 1 else "s"))
    text = "; ".join(parts)
data["text"] = text

emit(True, data, 0.6 if truncated else 0.95, [], warnings)
