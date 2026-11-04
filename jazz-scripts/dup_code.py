# dup_code: near-duplicate code blocks found by hashing windows of normalized lines
# (simplified winnowing). Every line is stripped, whitespace-collapsed and freed of
# its trailing line comment; blank and brace-only lines are dropped. Each window of
# `window` significant lines is sha1-hashed; a hash seen in >=2 places is a duplicate
# and adjacent windows shared by the same set of places merge into one bigger block.
# Read-only, deterministic, no network. Python 3.8+ stdlib only.
import hashlib
import time

T0 = time.time()
BUDGET_S = 4.0            # wall-clock budget: stop reading files, mark truncated
MAX_FILES = 20000         # files visited at most
MAX_BYTES = 512 << 10     # bigger files are skipped (bundles, generated code)
MAX_WINDOWS = 1000000     # windows hashed at most (memory bound), mark truncated
MAX_SAMPLE = 120          # chars of the block's first line kept
SKIP = SKIP_DIRS | set(["testdata", "dist", "vendor", "node_modules", "build", "target"])

# language -> extensions; the first language that lists an extension owns it.
LANG_EXTS = [
    ("python", ["py"]), ("go", ["go"]), ("javascript", ["js", "jsx", "mjs", "cjs"]),
    ("typescript", ["ts", "tsx"]), ("rust", ["rs"]), ("java", ["java"]),
    ("kotlin", ["kt", "kts"]), ("swift", ["swift"]), ("c", ["c", "h"]),
    ("cpp", ["cpp", "cc", "cxx", "hpp", "hh", "hxx"]), ("csharp", ["cs"]),
    ("ruby", ["rb"]), ("php", ["php"]), ("shell", ["sh", "bash", "zsh"]),
    ("sql", ["sql"]), ("lua", ["lua"]), ("scala", ["scala"]), ("dart", ["dart"]),
    ("css", ["css", "scss", "less"]), ("vue", ["vue"]), ("svelte", ["svelte"]),
    ("objc", ["m", "mm"]), ("perl", ["pl", "pm"]),
]
LANG_ALIASES = {"py": "python", "js": "javascript", "node": "javascript", "ts": "typescript",
                "rs": "rust", "golang": "go", "sh": "shell", "bash": "shell", "c++": "cpp",
                "cs": "csharp", "c#": "csharp", "rb": "ruby", "kt": "kotlin", "objective-c": "objc"}
EXT_LANG = {}
for _lang, _exts in LANG_EXTS:
    for _e in _exts:
        EXT_LANG.setdefault(_e, _lang)

# line-comment markers per language (block comments are handled by shape only)
HASH_LANGS = set(["python", "ruby", "shell", "perl"])
DASH_LANGS = set(["sql", "lua"])
SLASH_LANGS = set(["go", "javascript", "typescript", "rust", "java", "kotlin", "swift", "c", "cpp",
                   "csharp", "php", "scala", "dart", "vue", "svelte", "objc"])
SHAPE_RE = re.compile(r"^[{}()\[\];,]*$")       # lines that are only punctuation
BLOCK_RE = re.compile(r"^(/\*|\*/|\*)")          # lines of a C-style block comment


def to_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    if v < lo or v > hi:
        fail("%s must be between %d and %d" % (name, lo, hi))
    return v


top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
window = to_int("window", 6, 3, 50)
min_block = to_int("min_block", 6, 1, 500)
limit = to_int("limit", 100, 1, 1000)

lang = arg("lang")
only_exts = None
if lang is not None:
    s = str(lang).strip().lower().lstrip(".")
    s = LANG_ALIASES.get(s, s)
    if s in EXT_LANG and s not in dict(LANG_EXTS):
        s = EXT_LANG[s]
    if s not in dict(LANG_EXTS):
        fail("unknown lang %r (use one of: %s)" % (lang, ", ".join(l for l, _ in LANG_EXTS)))
    only_exts = set(dict(LANG_EXTS)[s])


def markers_for(lang_name):
    m = []
    if lang_name in SLASH_LANGS:
        m.append("//")
    if lang_name in HASH_LANGS or lang_name == "php":
        m.append("#")
    if lang_name in DASH_LANGS:
        m.append("--")
    return tuple(m)


def strip_comment(line, markers):
    """Cut a trailing line comment unless the marker sits inside a quoted string
    (quote parity before the marker: best effort, good enough for hashing)."""
    for m in markers:
        idx = line.find(m)
        while idx != -1:
            before = line[:idx]
            if before.count('"') % 2 == 0 and before.count("'") % 2 == 0 and before.count("`") % 2 == 0:
                line = before
                break
            idx = line.find(m, idx + 1)
    return line


def significant_lines(src, lang_name):
    """[(lineno, normalized text)] for the lines that carry code."""
    markers = markers_for(lang_name)
    c_like = lang_name in SLASH_LANGS or lang_name == "css"
    out = []
    for ln, raw in enumerate(src.splitlines(), 1):
        s = raw.strip()
        if not s:
            continue
        if c_like and BLOCK_RE.match(s):
            continue
        if markers:
            s = strip_comment(s, markers).strip()
        s = re.sub(r"\s+", " ", s)
        if not s or SHAPE_RE.match(s):
            continue
        out.append((ln, s))
    return out


def iter_code_files(top):
    if os.path.isfile(top):
        yield top
        return
    n = 0
    for d, dirs, files in os.walk(top):
        dirs[:] = sorted(x for x in dirs if x not in SKIP and not x.startswith("."))
        for f in sorted(files):
            n += 1
            if n > MAX_FILES:
                return
            yield os.path.join(d, f)


def lang_of(p):
    base = os.path.basename(p)
    ext = os.path.splitext(base)[1].lower().lstrip(".")
    if ext not in EXT_LANG:
        return None
    if ".min." in base.lower() or base.lower().endswith((".d.ts", ".pb.go", ".generated.ts")):
        return None  # minified/generated: hashes nothing a human would deduplicate
    if only_exts is not None and ext not in only_exts:
        return None
    return EXT_LANG[ext]


def window_key(block):
    return int.from_bytes(hashlib.sha1(block.encode("utf-8")).digest()[:8], "big")


# pass 1: significant lines and window hashes per file; count every hash
files = []          # [(relpath, sig_lines, keys)]
counts = {}         # key -> occurrences
n_windows, files_scanned, truncated, warnings = 0, 0, False, []
for p in iter_code_files(top):
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % files_scanned)
        break
    if n_windows >= MAX_WINDOWS:
        truncated = True
        warnings.append("window limit %d reached after %d files" % (MAX_WINDOWS, files_scanned))
        break
    lang_name = lang_of(p)
    if lang_name is None:
        continue
    try:
        if os.path.islink(p) or os.path.getsize(p) > MAX_BYTES:
            continue
        with open(p, "rb") as f:
            raw = f.read()
    except (IOError, OSError):
        continue
    if b"\x00" in raw[:4096]:
        continue  # binary
    files_scanned += 1
    sig = significant_lines(raw.decode("utf-8", "replace"), lang_name)
    keys = []
    for i in range(len(sig) - window + 1):
        k = window_key("\n".join(t for _, t in sig[i:i + window]))
        keys.append(k)
        counts[k] = counts.get(k, 0) + 1
    n_windows += len(keys)
    files.append((rel(p).replace(os.sep, "/"), sig, keys))

# pass 2: places of every duplicated hash, in (file, index) order
occ = {}
for fi, (_, _, keys) in enumerate(files):
    for i, k in enumerate(keys):
        if counts.get(k, 0) >= 2:
            occ.setdefault(k, []).append((fi, i))

# pass 3: merge adjacent windows shared by the same set of places into blocks
consumed = set()
groups = []
for fi, (_, _, keys) in enumerate(files):
    for i, k in enumerate(keys):
        if k not in occ or (fi, i) in consumed:
            continue
        places = occ[k]
        ext = 0
        while True:
            nxt = [(f, j + ext + 1) for f, j in places]
            if any(j >= len(files[f][2]) for f, j in nxt):
                break
            if occ.get(files[nxt[0][0]][2][nxt[0][1]]) != nxt:
                break
            ext += 1
        lines = window + ext
        for f, j in places:
            for step in range(ext + 1):
                consumed.add((f, j + step))
        occs = []
        for f, j in places:
            sig = files[f][1]
            occs.append({"file": files[f][0], "start": sig[j][0], "end": sig[j + lines - 1][0]})
        occs.sort(key=lambda o: (o["file"], o["start"]))
        kept, last = [], None
        for o in occs:  # a block repeated inside itself: keep non-overlapping copies only
            if last is not None and last["file"] == o["file"] and o["start"] <= last["end"]:
                continue
            kept.append(o)
            last = o
        if len(kept) < 2 or lines < min_block:
            continue
        f0, j0 = places[0]
        groups.append({"lines": lines, "occurrences": kept, "sample": clip(files[f0][1][j0][1], MAX_SAMPLE)})

groups.sort(key=lambda g: (-g["lines"], g["occurrences"][0]["file"], g["occurrences"][0]["start"]))
total_dup_lines = sum(g["lines"] * (len(g["occurrences"]) - 1) for g in groups)
total_groups = len(groups)
if total_groups > limit:
    truncated = True
    warnings.append("%d groups; showing %d (raise limit or narrow path)" % (total_groups, limit))
    groups = groups[:limit]

text = "%d duplicate groups, %d lines" % (total_groups, total_dup_lines)
text += " (%d files scanned, window %d)" % (files_scanned, window)
if truncated:
    text += " [truncated]"
emit(True, {"groups": groups, "total_groups": total_groups, "total_dup_lines": total_dup_lines,
            "files_scanned": files_scanned, "window": window, "truncated": truncated, "text": text},
     (0.6 if truncated else 1.0) if files_scanned else 0.3, [], warnings)
