# dir_diff: compare two directory trees — entries only in A, only in B, and
# files present in both whose content differs (size, then sha256). The
# read-only half of "diff -r"/"sync these folders": nothing is copied or
# deleted. os.walk, no symlinks, 4s budget, deterministic order.
# Python 3.8+ stdlib only.
import hashlib
import stat
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_SCAN = 150000
MAX_HASH = 64 << 20
MAX_LIST = 200
SKIP = SKIP_DIRS | set([".git", ".hg", ".svn", ".tox", ".venv", ".cache"])

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root")
    return p


a_s, b_s = arg("a"), arg("b")
if a_s is None or b_s is None:
    fail("pass a and b (two directories inside the project root)")
for label, s in (("a", a_s), ("b", b_s)):
    if os.path.islink(path_in_root(s)):
        fail("%s is a symlink (not followed): %s" % (label, rel(path_in_root(s))))
a_root, b_root = bounded_path(a_s), bounded_path(b_s)
for label, p in (("a", a_root), ("b", b_root)):
    if not os.path.isdir(p):
        fail("%s is not a directory: %s" % (label, rel(p)))

warnings = []


def snapshot(root):
    out = {}
    truncated = False
    n = 0
    for d, dirs, files in os.walk(root, followlinks=False):
        pruned = []
        for x in dirs:
            if x in SKIP or x.startswith("."):
                continue
            if os.path.islink(os.path.join(d, x)):
                continue
            pruned.append(x)
        dirs[:] = sorted(pruned)
        for x in pruned:
            p = os.path.join(d, x)
            try:
                st = os.lstat(p)
            except (IOError, OSError):
                continue
            r = os.path.relpath(p, root).replace(os.sep, "/")
            out[r] = ("dir", 0)
            n += 1
        for f in sorted(files):
            if f.startswith("."):
                continue
            p = os.path.join(d, f)
            try:
                st = os.lstat(p)
            except (IOError, OSError):
                continue
            if not stat.S_ISREG(st.st_mode):
                continue
            r = os.path.relpath(p, root).replace(os.sep, "/")
            out[r] = ("file", st.st_size)
            n += 1
        if n >= MAX_SCAN or time.time() - T0 > BUDGET_S:
            return out, True
    return out, False


sa, ta = snapshot(a_root)
sb, tb = snapshot(b_root)
truncated = ta or tb
if ta:
    warnings.append("scan of a stopped early")
if tb:
    warnings.append("scan of b stopped early")

only_a = sorted(r for r in sa if r not in sb)
only_b = sorted(r for r in sb if r not in sa)
type_clash = sorted(r for r in sa if r in sb and sa[r][0] != sb[r][0])
common_files = [r for r in sa if r in sb and sa[r][0] == "file" and sb[r][0] == "file"]
size_diff = [r for r in common_files if sa[r][1] != sb[r][1]]

def digest(p):
    h = hashlib.sha256()
    with open(p, "rb") as fh:
        h.update(fh.read(MAX_HASH))
    return h.hexdigest()

content_diff = []
for r in sorted(common_files):
    if sa[r][1] != sb[r][1] or sa[r][1] > MAX_HASH:
        continue
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached during hashing")
        break
    pa, pb = os.path.join(a_root, r), os.path.join(b_root, r)
    try:
        if digest(pa) != digest(pb):
            content_diff.append(r)
    except (IOError, OSError):
        continue

diff_all = sorted(set(size_diff) | set(content_diff) | set(type_clash))


def clip_list(xs):
    return xs[:MAX_LIST]


identical = len(common_files) - len(set(size_diff) | set(content_diff))
a_only_files = [r for r in only_a]
b_only_files = [r for r in only_b]
if len(only_a) > MAX_LIST or len(only_b) > MAX_LIST or len(diff_all) > MAX_LIST:
    truncated = True
    warnings.append("list(s) capped at %d entries" % MAX_LIST)

n_a, n_b, n_d = len(only_a), len(only_b), len(diff_all)
text = "%d only in a, %d only in b, %d differ, %d identical" % (n_a, n_b, n_d, identical)
if n_a:
    text += "; a-only e.g. %s" % clip(only_a[0], 50)
if n_b:
    text += "; b-only e.g. %s" % clip(only_b[0], 50)
if not (n_a or n_b or n_d):
    text = "trees are identical (%d entries)" % len(sa)
if truncated:
    text += " [truncated]"

emit(True, {"only_a": clip_list(a_only_files), "only_b": clip_list(b_only_files),
            "differ": clip_list(diff_all), "identical": identical,
            "only_a_count": n_a, "only_b_count": n_b, "differ_count": n_d,
            "a_entries": len(sa), "b_entries": len(sb),
            "truncated": truncated, "text": text},
     1.0 if not truncated else 0.6, [], warnings)
