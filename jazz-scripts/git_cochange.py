# git_cochange: change history as bug-localization evidence. Churn (commits per file,
# Nagappan & Ball ICSE'05) and co-change (files that are committed together, Zimmermann
# et al. ICSE'04: "when a.go changes, b.go changes too"). One `git log --name-only`
# over the last N non-merge commits, no network. Python 3.8+ stdlib only.
import itertools
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
if not os.path.isdir(top):
    fail("path not found: %s" % rel(top))


def as_list(v):
    if v is None:
        return []
    if isinstance(v, (list, tuple)):
        return [str(x).strip() for x in v if str(x).strip()]
    return [s for s in re.split(r"[\s,]+", str(v)) if s]


def clamp_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    return max(lo, min(hi, v))


n = clamp_int("n", 400, 1, 2000)
limit = clamp_int("limit", 20, 1, 200)
seeds = as_list(arg("files"))

BUDGET_S = 4.0          # wall-clock budget for the whole script (the Go side waits up to 60 s)
GIT_TIMEOUT = 5         # seconds for the single git call
MAX_COMMIT_FILES = 50   # bigger commits (mass renames, vendoring) count for churn but not for pairs
MAX_PAIRS = 200000      # pair-counter size cap: stop counting pairs beyond it, mark truncated
HASH_RE = re.compile(r"^[0-9a-f]{40,64}$")

# --- repository root, so seeds given relative to the project map onto git's paths ---
code, out, err = run_argv(["git", "rev-parse", "--show-toplevel"], timeout=GIT_TIMEOUT, cwd=top)
if code == 127:
    fail("git not installed")
if code != 0:
    fail("not a git repository")
toplevel = out.strip()

argv = ["git", "-c", "core.quotepath=false", "log", "--name-only", "--no-merges",
        "--pretty=format:%H", "-n", str(n)]
subtree = os.path.realpath(top) != os.path.realpath(toplevel)
if subtree:
    argv += ["--", "."]  # a subdirectory: only its history, paths still relative to the repo root
code, out, err = run_argv(argv, timeout=GIT_TIMEOUT, cwd=top)
warnings = []
truncated = False
if code == 124:
    if not out.strip():
        fail("git log timed out")
    truncated = True
    warnings.append("git log timed out after %ds; partial history" % GIT_TIMEOUT)
elif code != 0:
    if "does not have any commits" in err or "bad default revision" in err:
        out = ""
        warnings.append("repository has no commits")
    else:
        fail("git log failed: %s" % clip(err.strip().splitlines()[-1] if err.strip() else "exit %d" % code, 160))

# --- parse: a hash line opens a commit, the non-blank lines below it are its files ---
commits = []  # list of sorted unique file lists
cur = None
for line in out.splitlines():
    line = line.rstrip()
    if not line:
        continue
    if HASH_RE.match(line):
        cur = set()
        commits.append(cur)
    elif cur is not None:
        cur.add(line)
commits = [sorted(c) for c in commits]

# --- churn and pairs ---
churn = {}
pairs = {}
skipped_big = 0
for i, files in enumerate(commits):
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d of %d commits" % (i, len(commits)))
        commits = commits[:i]
        break
    for f in files:
        churn[f] = churn.get(f, 0) + 1
    if len(files) > MAX_COMMIT_FILES:
        skipped_big += 1
        continue
    if len(pairs) > MAX_PAIRS:
        truncated = True
        continue
    for a, b in itertools.combinations(files, 2):
        pairs[(a, b)] = pairs.get((a, b), 0) + 1
if skipped_big:
    warnings.append("%d commit(s) touching more than %d files were left out of pair counting" % (skipped_big, MAX_COMMIT_FILES))


# --- seeds: match an exact repo path, a project-relative path or a basename/suffix ---
def seed_keys(s):
    keys = {s.strip("/"), s.lstrip("./")}
    p = path_in_root(s)
    try:
        r = os.path.relpath(p, toplevel)
    except ValueError:
        r = ""
    if r and not r.startswith(".."):
        keys.add(r.replace(os.sep, "/"))
    return set(k for k in keys if k)


seed_set = set()
for s in seeds:
    seed_set |= seed_keys(s)


def is_seed(f):
    return any(f == k or f.endswith("/" + k) for k in seed_set)


seed_files = sorted(f for f in churn if is_seed(f)) if seeds else []
if seeds and not seed_files:
    warnings.append("none of the seed files appears in the last %d commits" % len(commits))

churn_rows = [{"file": f, "commits": c} for f, c in churn.items()]
churn_rows.sort(key=lambda r: (-r["commits"], r["file"]))

rows = []
for (a, b), c in pairs.items():
    if seeds and not (is_seed(a) or is_seed(b)):
        continue
    rows.append({"a": a, "b": b, "count": c, "support": round(c / float(min(churn[a], churn[b])), 2)})
rows.sort(key=lambda r: (-r["count"], -r["support"], r["a"], r["b"]))
if len(rows) > limit or len(churn_rows) > limit:
    truncated = True

data = {
    "commits": len(commits),
    "files": len(churn),
    "churn": churn_rows[:limit],
    "cochange": rows[:limit],
    "truncated": truncated,
    "ms": int((time.time() - T0) * 1000),
}
if seeds:
    data["seed_files"] = [{"file": f, "commits": churn[f]} for f in seed_files]

# --- text ---
lines = []
scope = rel(top) if subtree else "repository"
lines.append("%d commits, %d files in %s%s" % (len(commits), len(churn), scope, " (seeds: %s)" % ", ".join(seeds) if seeds else ""))
if churn_rows:
    lines.append("churn: " + ", ".join("%s (%d)" % (r["file"], r["commits"]) for r in churn_rows[:5]))
if rows:
    lines.append("co-change: " + "; ".join("%s + %s x%d (%.2f)" % (r["a"], r["b"], r["count"], r["support"]) for r in rows[:5]))
elif commits:
    lines.append("co-change: no pair found")
data["text"] = "\n".join(lines)

emit(True, data, 0.9 if commits else 0.3, [], warnings)
