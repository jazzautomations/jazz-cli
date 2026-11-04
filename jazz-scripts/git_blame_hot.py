# git_blame_hot: who wrote what, and where the rewrites cluster, for one file or a line
# range. One `git blame --line-porcelain` (bounded by -L when a range is given) plus one
# `git log -n N -- file` for context; no `git log -L`, no network. A line is "hot" when
# its ±HOT_RADIUS neighbourhood was written by many distinct recent commits: interleaved
# authorship is where code keeps getting rewritten. Python 3.8+ stdlib only.
import re
import time

T0 = time.time()
BUDGET_S = 4.0        # wall-clock budget for the whole script
GIT_TIMEOUT = 5       # seconds per git call
HOT_RADIUS = 3        # lines on each side counted in a line's neighbourhood
MAX_AUTHORS = 20      # author rows returned (more -> truncated)
MAX_HOT = 10          # hot line rows returned
MAX_LINES = 20000     # blame lines parsed before stopping (whole-file blame of a huge file)
SHA_RE = re.compile(r"^[0-9a-f]{40,64} ")


def clamp_int(name, default, lo, hi, allow_none=False):
    v = arg(name, default)
    if v is None and allow_none:
        return None
    try:
        v = int(v)
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    return max(lo, min(hi, v))


p = arg("path")
if p is None:
    fail("path is required: the file to blame")
target = path_in_root(p)
if not os.path.isfile(target):
    fail("file not found: %s" % rel(target))
start = clamp_int("start", None, 1, 10 ** 9, allow_none=True)
end = clamp_int("end", None, 1, 10 ** 9, allow_none=True)
n = clamp_int("n", 50, 1, 2000)
if start is not None and end is not None and end < start:
    fail("end must be >= start")

# --- repository root and the file's path inside it ---
code, out, err = run_argv(["git", "rev-parse", "--show-toplevel"], timeout=GIT_TIMEOUT, cwd=os.path.dirname(target) or ".")
if code == 127:
    fail("git not installed")
if code != 0:
    fail("not a git repository")
toplevel = out.strip()
try:
    gitpath = os.path.relpath(os.path.realpath(target), os.path.realpath(toplevel))
except ValueError:
    gitpath = ".."
if gitpath.startswith(".."):
    fail("file is outside the repository: %s" % rel(target))
gitpath = gitpath.replace(os.sep, "/")

warnings = []
truncated = False

# --- context: the file's last n commits (newest first); the newest is last_change ---
argv = ["git", "log", "-n", str(n), "--format=%H%x00%an%x00%aI", "--", gitpath]
code, out, err = run_argv(argv, timeout=GIT_TIMEOUT, cwd=toplevel)
recent = []  # [(sha, author, date)]
if code == 0:
    for line in out.splitlines():
        parts = line.split("\0")
        if len(parts) == 3 and parts[0]:
            recent.append((parts[0], parts[1], parts[2]))
elif code == 124:
    warnings.append("git log timed out; no commit context")
    truncated = True
elif "does not have any commits" in err or "bad default revision" in err:
    warnings.append("repository has no commits")
if not recent and code == 0:
    fail("file is not tracked by git: %s" % gitpath)
recent_set = set(sha for sha, _, _ in recent)

# --- blame ---
argv = ["git", "-c", "core.quotepath=false", "blame", "--line-porcelain"]
if start is not None or end is not None:
    lo = start if start is not None else 1
    hi = end if end is not None else ""
    argv += ["-L", "%d,%s" % (lo, hi)]
argv += ["--", gitpath]
code, out, err = run_argv(argv, timeout=GIT_TIMEOUT, cwd=toplevel)
if code == 124:
    fail("git blame timed out after %ds" % GIT_TIMEOUT)
if code != 0:
    msg = err.strip().splitlines()[-1] if err.strip() else "exit %d" % code
    if "no such path" in msg or "is outside repository" in msg:
        fail("file is not tracked by git: %s" % gitpath)
    if "file" in msg and "has only" in msg:
        fail("range is beyond the end of the file: %s" % clip(msg, 160))
    fail("git blame failed: %s" % clip(msg, 160))

# --- parse: a sha line opens a blame line, "author ..."/"author-time ..." annotate it,
# the tab-prefixed content line closes it ---
blame = []  # [(line_no, sha, author, author_time)]
sha = author = None
atime = 0
line_no = 0
for raw in out.splitlines():
    if raw.startswith("\t"):
        if sha is not None:
            blame.append((line_no, sha, author or "?", atime))
            if len(blame) >= MAX_LINES:
                truncated = True
                warnings.append("stopped after %d lines" % MAX_LINES)
                break
        sha = author = None
        atime = 0
        continue
    if SHA_RE.match(raw):
        parts = raw.split(" ")
        sha = parts[0]
        try:
            line_no = int(parts[2])
        except (IndexError, ValueError):
            line_no += 1
    elif raw.startswith("author "):
        author = raw[7:].strip()
        if sha and sha.strip("0") == "" and author == "Not Committed Yet":
            author = "(uncommitted)"
    elif raw.startswith("author-time "):
        try:
            atime = int(raw[12:].strip())
        except ValueError:
            atime = 0
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached while parsing blame")
        break

total = len(blame)

# --- authors ---
per_author = {}
for _, _, a, _ in blame:
    per_author[a] = per_author.get(a, 0) + 1
authors = [{"author": a, "lines": c, "pct": round(100.0 * c / total, 1) if total else 0.0} for a, c in per_author.items()]
authors.sort(key=lambda r: (-r["lines"], r["author"]))
if len(authors) > MAX_AUTHORS:
    truncated = True
    authors = authors[:MAX_AUTHORS]

# --- last change: newest commit of the file (log), else the newest blame commit ---
last_change = None
if recent:
    s, a, d = recent[0]
    last_change = {"commit": s[:12], "author": a, "date": d[:10]}
elif blame:
    newest = max(blame, key=lambda r: (r[3], r[1]))
    last_change = {"commit": newest[1][:12], "author": newest[2], "date": time.strftime("%Y-%m-%d", time.gmtime(newest[3]))}

# --- hot lines: distinct recent commits (the last n of the file) in the neighbourhood ---
shas = [s if (s in recent_set or not recent_set) else None for _, s, _, _ in blame]
distinct_in_range = len(set(s for _, s, _, _ in blame))
hot = []
for i, (ln, _, _, _) in enumerate(blame):
    window = set(s for s in shas[max(0, i - HOT_RADIUS):i + HOT_RADIUS + 1] if s)
    if len(window) >= 2:
        hot.append({"line": ln, "commits": len(window)})
hot.sort(key=lambda r: (-r["commits"], r["line"]))
hot_lines = hot[:MAX_HOT]

data = {
    "file": gitpath,
    "lines": total,
    "range": {"start": blame[0][0], "end": blame[-1][0]} if blame else {"start": 0, "end": 0},
    "authors": authors,
    "last_change": last_change,
    "commits": distinct_in_range,
    "recent_commits": len(recent),
    "hot_lines": hot_lines,
    "truncated": truncated,
    "ms": int((time.time() - T0) * 1000),
}

# --- text ---
if total:
    top = authors[0]
    text = "%d lines, top author %s (%s%%)" % (total, top["author"], top["pct"])
    if last_change:
        text += ", last change %s by %s" % (last_change["date"], last_change["author"])
    if hot_lines:
        text += "; hot: " + ", ".join("L%d (%d commits)" % (h["line"], h["commits"]) for h in hot_lines[:5])
else:
    text = "0 lines (empty file)"
    warnings.append("file has no lines in the requested range")
data["text"] = text

emit(True, data, 0.9 if total and not truncated else 0.5, [], warnings)
