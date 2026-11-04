# git_branch_status: local branches at a glance, from refs only (no fetch, no network).
# One `git for-each-ref` over refs/heads gives upstream, ahead/behind (as the last fetch
# left them), last commit time and author; one `git branch --no-merged <default>` tells
# which branches still carry unmerged work. Stale = last commit older than stale_days;
# gone = upstream configured but no longer present. Python 3.8+ stdlib only.
import re
import time

T0 = time.time()
BUDGET_S = 4.0
GIT_TIMEOUT = 5
DEFAULT_CANDIDATES = ("main", "master", "trunk", "develop")
TRACK_RE = re.compile(r"(ahead|behind) (\d+)")

top = path_in_root(arg("path", "."))
if os.path.isfile(top):
    top = os.path.dirname(top) or "."
if not os.path.isdir(top):
    fail("path not found: %s" % rel(top))


def clamp_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    return max(lo, min(hi, v))


stale_days = clamp_int("stale_days", 30, 0, 36500)
limit = clamp_int("limit", 50, 1, 500)


def git(*args, **kw):
    return run_argv(["git"] + list(args), timeout=GIT_TIMEOUT, cwd=kw.get("cwd", top))


code, out, err = git("rev-parse", "--show-toplevel")
if code == 127:
    fail("git not installed")
if code != 0:
    fail("not a git repository")
toplevel = out.strip()
warnings = []
truncated = False

# --- HEAD: branch name, or detached at a commit ---
detached = False
code, out, _ = git("symbolic-ref", "--short", "-q", "HEAD")
current = out.strip() if code == 0 else ""
if not current:
    detached = True
    code, out, _ = git("rev-parse", "--short", "HEAD")
    current = out.strip() if code == 0 else ""

# --- local branches ---
fmt = "%(refname:short)%00%(upstream:short)%00%(upstream:track,nobracket)%00%(committerdate:unix)%00%(authorname)%00%(objectname:short)"
code, out, err = git("for-each-ref", "--format=" + fmt, "refs/heads")
if code == 124:
    fail("git for-each-ref timed out")
if code != 0:
    fail("git for-each-ref failed: %s" % clip(err.strip().splitlines()[-1] if err.strip() else "exit %d" % code, 160))
now = int(time.time())
rows = []
for line in out.splitlines():
    parts = line.split("\0")
    if len(parts) != 6 or not parts[0]:
        continue
    name, upstream, track, cdate, author, obj = parts
    ahead = behind = 0
    for what, num in TRACK_RE.findall(track):
        if what == "ahead":
            ahead = int(num)
        else:
            behind = int(num)
    try:
        last = int(cdate)
    except ValueError:
        last = 0
    rows.append({
        "name": name,
        "upstream": upstream or None,
        "gone": track.strip() == "gone",
        "ahead": ahead,
        "behind": behind,
        "last_commit_unix": last,
        "days": max(0, (now - last) // 86400) if last else 0,
        "author": author,
        "commit": obj,
        "merged": False,
    })
names = set(r["name"] for r in rows)
if not rows:
    warnings.append("repository has no branches with commits")

# --- default branch: origin/HEAD if it points at a local branch, else a usual name ---
default_branch = None
code, out, _ = git("symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD")
if code == 0 and out.strip():
    cand = out.strip().split("/", 1)[-1]
    if cand in names:
        default_branch = cand
if default_branch is None:
    for cand in DEFAULT_CANDIDATES:
        if cand in names:
            default_branch = cand
            break
if default_branch is None and current in names:
    default_branch = current
    if rows:
        warnings.append("no main/master/origin/HEAD found; using current branch as default")

# --- merged: everything not listed by --no-merged <default> ---
unmerged = set()
if default_branch:
    code, out, err = git("branch", "--no-merged", default_branch, "--format=%(refname:short)")
    if code == 0:
        unmerged = set(l.strip() for l in out.splitlines() if l.strip())
    else:
        warnings.append("could not compute merged state: %s" % clip(err.strip(), 120))
    for r in rows:
        r["merged"] = r["name"] not in unmerged
if time.time() - T0 > BUDGET_S:
    truncated = True
    warnings.append("time budget reached")

rows.sort(key=lambda r: (-r["days"], r["name"]))
stale = sorted(r["name"] for r in rows if r["name"] != default_branch and r["last_commit_unix"] and r["days"] >= stale_days)
gone = sorted(r["name"] for r in rows if r["gone"])
unmerged_list = sorted(r["name"] for r in rows if not r["merged"])
head_row = next((r for r in rows if r["name"] == current), None) if not detached else None
if len(rows) > limit:
    truncated = True

data = {
    "current": current,
    "detached": detached,
    "default_branch": default_branch,
    "upstream": head_row["upstream"] if head_row else None,
    "ahead": head_row["ahead"] if head_row else 0,
    "behind": head_row["behind"] if head_row else 0,
    "branches": rows[:limit],
    "total": len(rows),
    "stale": stale,
    "stale_days": stale_days,
    "unmerged": unmerged_list,
    "gone": gone,
    "truncated": truncated,
    "ms": int((time.time() - T0) * 1000),
}

# --- text ---
if detached:
    head = "detached at %s" % (current or "?")
elif head_row and head_row["upstream"]:
    head = "on %s (ahead %d, behind %d of %s%s)" % (current, head_row["ahead"], head_row["behind"], head_row["upstream"], ", gone" if head_row["gone"] else "")
else:
    head = "on %s (no upstream)" % (current or "unborn branch")
parts = ["%d branch%s" % (len(rows), "" if len(rows) == 1 else "es")]
if unmerged_list:
    parts.append("%d unmerged into %s" % (len(unmerged_list), default_branch))
if stale:
    parts.append("%d stale (>=%dd)" % (len(stale), stale_days))
if gone:
    parts.append("%d upstream gone" % len(gone))
data["text"] = head + "; " + ", ".join(parts)

emit(True, data, 0.9 if rows else 0.4, [], warnings)
