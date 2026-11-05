# file_dupes: find byte-identical files under a path — group by size, then by
# sha256 of content (only same-size candidates get hashed). "find duplicate
# files by content", the file-level oracle (dup_code finds similar code
# *blocks*, a different question). os.walk, no symlinks, 4s budget, groups
# sorted by wasted bytes. Python 3.8+ stdlib only.
import hashlib
import stat
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_SCAN = 100000
MAX_HASH_BYTES = 64 << 20  # hashing a file bigger than this would burn the budget
SKIP = SKIP_DIRS | set([".git", ".hg", ".svn", ".tox", ".venv", ".cache"])


def as_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    if v < lo or v > hi:
        fail("%s must be %d..%d" % (name, lo, hi))
    return v


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
min_size = as_int("min_size", 1, 0, 1 << 40)
max_groups = as_int("limit", 50, 1, 1000)
include_hidden = str(arg("include_hidden", "false")).lower() in ("1", "true", "yes", "on")

hidden = lambda n: n.startswith(".")
by_size = {}
scanned = 0
truncated = False
warnings = []
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
        if not stat.S_ISREG(st.st_mode) or st.st_size < min_size:
            continue
        scanned += 1
        by_size.setdefault(st.st_size, []).append(p)
    if scanned >= MAX_SCAN or time.time() - T0 > BUDGET_S:
        truncated, stop = True, True
        warnings.append("scan stopped after %d files" % scanned)
        break

def digest(p):
    h = hashlib.sha256()
    with open(p, "rb") as fh:
        h.update(fh.read(MAX_HASH_BYTES))
    return h.hexdigest()

groups = []
dup_files = wasted = 0
oversized = []
for size in sorted(by_size, reverse=True):
    paths = by_size[size]
    if len(paths) < 2:
        continue
    if size > MAX_HASH_BYTES:
        oversized.append((size, len(paths)))
        continue
    by_hash = {}
    for p in sorted(paths):
        if time.time() - T0 > BUDGET_S:
            truncated = True
            warnings.append("time budget reached during hashing")
            break
        try:
            by_hash.setdefault(digest(p), []).append(p)
        except (IOError, OSError):
            continue
    for h, ps in sorted(by_hash.items()):
        if len(ps) < 2:
            continue
        ps = sorted(rel(p).replace(os.sep, "/") for p in ps)
        groups.append({"size": size, "sha256": h[:16], "files": ps})
        dup_files += len(ps)
        wasted += size * (len(ps) - 1)

groups.sort(key=lambda g: (-g["size"] * (len(g["files"]) - 1), g["files"][0]))
total_groups = len(groups)
if total_groups > max_groups:
    truncated = True
    warnings.append("%d duplicate group(s); showing %d" % (total_groups, max_groups))
    groups = groups[:max_groups]
if oversized:
    warnings.append("%d same-size cluster(s) skipped: files larger than %d MB"
                    % (len(oversized), MAX_HASH_BYTES >> 20))


def human(b):
    for u in ("B", "KB", "MB", "GB"):
        if b < 1024 or u == "GB":
            return "%.1f %s" % (b, u) if u != "B" else "%d B" % b
        b /= 1024.0


if total_groups:
    text = "%d duplicate group(s), %d file(s), %s wasted" % (total_groups, dup_files, human(wasted))
    text += "; biggest: %d × %s (%s)" % (len(groups[0]["files"]), groups[0]["files"][0], human(groups[0]["size"]))
else:
    text = "no duplicate files under %s (%d files scanned)" % (rel(top), scanned)
if truncated:
    text += " [truncated]"

emit(True, {"groups": groups, "group_count": total_groups, "dup_files": dup_files,
            "wasted_bytes": wasted, "files_scanned": scanned, "min_size": min_size,
            "truncated": truncated, "text": text},
     1.0 if not truncated else 0.6, [], warnings)
