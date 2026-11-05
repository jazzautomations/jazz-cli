# file_users: which processes have a path open — "who has db.sqlite open",
# "what is using this file/dir". Walks /proc/<pid>/fd reading links and
# compares realpaths, comm taken from /proc/<pid>/comm (exact match on the
# resolved path, never substring). Hidden owners (permission denied) are
# reported as hidden_pids, never as pid 0. Linux /proc only, 4s budget.
# Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0

if not os.path.isdir("/proc"):
    fail("no /proc on this system (needs Linux procfs)")

target = arg("path")
if target is None:
    fail("pass path (file or directory inside the project root)")


def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root")
    return p


tp = bounded_path(target)
if not os.path.exists(tp):
    fail("path not found: %s" % rel(tp))
try:
    treal = os.path.realpath(tp)
except (IOError, OSError) as e:
    fail("cannot resolve path: %s" % e)


def comm_of(pid):
    try:
        with open("/proc/%d/comm" % pid) as f:
            return f.read().strip() or None
    except (IOError, OSError):
        return None


holders = {}
hidden = 0
truncated = False
pids = []
for n in os.listdir("/proc"):
    if n.isdigit():
        pids.append(int(n))
pids.sort()

for pid in pids:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        break
    fddir = "/proc/%d/fd" % pid
    try:
        fds = os.listdir(fddir)
    except (IOError, OSError):
        hidden += 1
        continue
    matched = deleted = 0
    for fd in fds:
        try:
            link = os.readlink(os.path.join(fddir, fd))
        except (IOError, OSError):
            continue
        was_deleted = link.endswith(" (deleted)")
        if was_deleted:
            link = link[:-10]
        try:
            same = os.path.realpath(link) == treal
        except (IOError, OSError):
            same = link == treal
        if same:
            matched += 1
            deleted += 1 if was_deleted else 0
    if matched:
        holders[pid] = {"pid": pid, "comm": comm_of(pid), "fds": matched,
                        "deleted_fds": deleted}

out = [holders[p] for p in sorted(holders)]
count = len(out)
text = "%d process(es) holding %s" % (count, rel(tp))
if count:
    text += ": " + ", ".join("%s(%d)" % (h["comm"] or "?", h["pid"]) for h in out[:5])
if hidden:
    text += "; %d pid(s) not inspectable (need root)" % hidden
if truncated:
    text += " [truncated]"

emit(True, {"holders": out, "count": count, "hidden_pids": hidden,
            "path": rel(tp), "truncated": truncated, "text": text},
     1.0 if not truncated else 0.7)
