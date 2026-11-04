# todo_scan: TODO/FIXME/XXX/HACK/BUG markers in source and docs, as a sorted list the
# brain can reason over (where the known debt is, which tag, what it says). Regex only,
# case-sensitive, skips .git/node_modules/vendor/dist and binaries. Python 3.8+ stdlib only.
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

DEFAULT_TAGS = ["TODO", "FIXME", "XXX", "HACK", "BUG"]
EXTS = set("py go js ts tsx rs java c cpp h rb sh md yaml yml toml json".split())
EXTS |= set(["jsx", "mjs", "cjs", "hpp", "cc", "kt", "swift", "php", "bash", "zsh", "sql"])
BUDGET_S = 4.0          # wall-clock budget: stop walking, mark truncated
MAX_FILES = 20000       # files visited at most
MAX_BYTES = 1 << 20     # bigger files are skipped (bundles, lockfiles, data)
MAX_TEXT = 160          # chars of the marker's text kept
TEST_RE = re.compile(r"(^test_.*\.py$|_test\.(py|go|rb)$|\.(test|spec)\.[jt]sx?$|^conftest\.py$)")
TEST_DIRS = set(["test", "tests", "__tests__", "testdata", "spec", "fixtures"])


def as_list(v):
    if v is None:
        return []
    if isinstance(v, (list, tuple)):
        return [str(x).strip() for x in v if str(x).strip()]
    return [s for s in re.split(r"[\s,]+", str(v)) if s]


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


tags = [t.upper() for t in as_list(arg("tags"))] or DEFAULT_TAGS
tags = sorted(set(tags), key=tags.index)
for t in tags:
    if not re.match(r"^[A-Z][A-Z0-9_]{1,15}$", t):
        fail("tag must be 2..16 letters, digits or underscore: %r" % t)
try:
    limit = max(1, min(2000, int(arg("limit", 200))))
except (TypeError, ValueError):
    fail("limit must be an integer")
include_tests = as_bool(ARGS.get("include_tests"), True)

# marker = tag as a whole word (DEBUG is not BUG, todos is not TODO), optional "(owner)",
# then ":" / "-" / space / end of line; what follows is the text.
TAG_RE = re.compile(r"(?<![A-Za-z0-9_])(%s)\b(?:\s*\(([^)]{1,40})\))?[ \t]*[:\-]?[ \t]*(.*)$" % "|".join(re.escape(t) for t in tags))
PROBE = tuple(tags)


def is_test_path(p):
    r = rel(p).replace(os.sep, "/")
    parts = r.split("/")
    if any(d in TEST_DIRS for d in parts[:-1]):
        return True
    return bool(TEST_RE.search(parts[-1]))


def clean_text(s):
    s = s.strip()
    s = re.sub(r"\s*(\*/|-->|-\}|\}\})\s*$", "", s)  # closing comment markers
    s = re.sub(r"\s+", " ", s)
    return clip(s, MAX_TEXT)


if os.path.isfile(top):
    candidates = [top]
else:
    candidates = []
    for p in walk_files(top, limit=MAX_FILES):
        ext = os.path.splitext(p)[1].lower().lstrip(".")
        if ext not in EXTS and os.path.basename(p) not in ("Makefile", "Dockerfile"):
            continue
        candidates.append(p)

items, by_tag = [], dict((t, 0) for t in tags)
files_scanned, files_with, truncated, warnings = 0, 0, False, []
for p in candidates:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % files_scanned)
        break
    if not include_tests and is_test_path(p):
        continue
    try:
        if os.path.getsize(p) > MAX_BYTES:
            continue
        with open(p, "rb") as f:
            raw = f.read()
    except (IOError, OSError):
        continue
    if b"\x00" in raw[:4096]:
        continue  # binary
    files_scanned += 1
    src = raw.decode("utf-8", "replace")
    if not any(t in src for t in PROBE):
        continue
    hit = False
    for ln, line in enumerate(src.splitlines(), 1):
        if not any(t in line for t in PROBE):
            continue
        m = TAG_RE.search(line)
        if not m:
            continue
        tag = m.group(1)
        by_tag[tag] += 1
        hit = True
        text = clean_text(m.group(3))
        if m.group(2):
            text = ("(%s) " % m.group(2).strip()) + text
        items.append({"file": rel(p).replace(os.sep, "/"), "line": ln, "tag": tag, "text": text})
    if hit:
        files_with += 1
if len(candidates) >= MAX_FILES:
    truncated = True
    warnings.append("stopped at %d files" % MAX_FILES)

items.sort(key=lambda it: (it["file"], it["line"]))
total = len(items)
if total > limit:
    truncated = True
    warnings.append("%d markers; showing %d (raise limit or narrow path)" % (total, limit))
items = items[:limit]
by_tag = dict((t, by_tag[t]) for t in sorted(by_tag))

counts = ", ".join("%s %d" % (t, c) for t, c in sorted(by_tag.items(), key=lambda kv: (-kv[1], kv[0])) if c)
text = "%d marker(s) in %d of %d files: %s" % (total, files_with, files_scanned, counts or "none")
if not include_tests:
    text += " (tests excluded)"
emit(True, {"files_scanned": files_scanned, "files_with_markers": files_with, "total": total, "items": items,
            "by_tag": by_tag, "truncated": truncated, "text": text},
     1.0 if files_scanned else 0.3, [], warnings)
