# file_hash_diff: verification oracle. Snapshot a file or directory (sha256 + size
# + mtime per file, relative paths, no symlinks, .git skipped) and later diff the
# current state against a saved snapshot to PROVE which files an action changed.
# Python 3.8+ stdlib only. Deterministic, no network.
import hashlib
import time

T0 = time.time()
BUDGET_S = 4.0

top = path_in_root(arg("path", "."))
op = str(arg("op", "snapshot"))
if op not in ("snapshot", "diff"):
    fail("op must be 'snapshot' or 'diff'")
try:
    limit = int(arg("limit", 20000))
except (TypeError, ValueError):
    fail("limit must be an integer")
if limit < 1:
    fail("limit must be >= 1")
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

CHUNK = 1 << 20


def sha256_of(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        while True:
            b = f.read(CHUNK)
            if not b:
                break
            h.update(b)
    return h.hexdigest()


def iter_targets(top, limit):
    """Yield regular files under top (sorted, hidden/vendored/.git skipped),
    or just top itself when it is a file. Never follows symlinks."""
    if os.path.islink(top):
        return
    if os.path.isfile(top):
        yield top
        return
    for p in walk_files(top, limit=limit + 1):
        if os.path.islink(p):
            continue
        yield p


def build_snapshot(top, limit):
    entries, truncated, warnings = [], False, []
    n = 0
    for p in iter_targets(top, limit):
        if time.time() - T0 > BUDGET_S:
            truncated = True
            warnings.append("time budget reached after %d files" % n)
            break
        n += 1
        if n > limit:
            truncated = True
            warnings.append("file limit %d reached" % limit)
            break
        try:
            st = os.stat(p)
            sha = sha256_of(p)
        except (IOError, OSError):
            continue
        entries.append({"path": rel(p), "sha256": sha, "size": st.st_size,
                        "mtime": int(st.st_mtime)})
    entries.sort(key=lambda e: e["path"])
    # root_sha256 hashes the ordered set by content identity (path+sha+size),
    # NOT mtime, so an unchanged tree hashes the same on every run.
    acc = hashlib.sha256()
    for e in entries:
        acc.update(("%s\0%s\0%d\n" % (e["path"], e["sha256"], e["size"])).encode("utf-8"))
    return entries, acc.hexdigest(), truncated, warnings


def load_base(raw):
    """base is a snapshot's JSON (as a string) OR a path to a .json snapshot file.
    Accepts either the raw data object or a full {ok,data,...} emit envelope."""
    s = str(raw)
    obj = None
    if s.lstrip()[:1] in ("{", "["):
        try:
            obj = json.loads(s)
        except ValueError:
            obj = None
    if obj is None:
        bp = path_in_root(s)
        try:
            obj = json.loads(read_text(bp))
        except (IOError, OSError) as e:
            fail("cannot read base snapshot: %s" % e)
        except ValueError as e:
            fail("base is neither snapshot JSON nor a readable .json file: %s" % e)
    if isinstance(obj, dict) and "entries" not in obj and isinstance(obj.get("data"), dict):
        obj = obj["data"]
    if not isinstance(obj, dict) or not isinstance(obj.get("entries"), list):
        fail("base snapshot has no 'entries' list")
    return obj


entries, root_sha, truncated, warnings = build_snapshot(top, limit)

if op == "snapshot":
    data = {"root": rel(top), "files": len(entries), "entries": entries,
            "root_sha256": root_sha, "truncated": truncated}
    data["text"] = "%d files, root_sha256 %s%s" % (
        len(entries), root_sha[:12], " (truncated)" if truncated else "")
    emit(True, data, 0.9 if not truncated else 0.6, [], warnings)

# op == "diff": current state vs base snapshot
if arg("base") is None:
    fail("op=diff needs 'base' (a snapshot JSON string or a .json path)")
base = load_base(ARGS["base"])
old = {}
for e in base.get("entries", []):
    if isinstance(e, dict) and "path" in e:
        old[e["path"]] = str(e.get("sha256", ""))
new = {e["path"]: e["sha256"] for e in entries}

added = sorted(p for p in new if p not in old)
removed = sorted(p for p in old if p not in new)
changed = sorted(
    ({"path": p, "old_sha": old[p], "new_sha": new[p]} for p in new if p in old and old[p] != new[p]),
    key=lambda c: c["path"])
unchanged = sum(1 for p in new if p in old and old[p] == new[p])
same = not added and not removed and not changed

data = {"root": rel(top), "added": added, "removed": removed, "changed": changed,
        "unchanged": unchanged, "same": same, "truncated": truncated}
if same:
    data["text"] = "no changes"
else:
    data["text"] = "%d changed, %d added, %d removed" % (len(changed), len(added), len(removed))
emit(True, data, 0.9 if not truncated else 0.6, [], warnings)
