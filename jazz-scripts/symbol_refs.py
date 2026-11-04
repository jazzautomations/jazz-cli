# symbol_refs: every USE of a symbol (not just where it is defined), with a word-boundary
# regex over source files — no rg, no parser. Reports file, line, column and the line text,
# and marks best-effort whether each hit sits in a comment or a string. Definition lines are
# collected separately and left out of the reference list unless include_defs is set.
# Walk + regex, bounded by a 4 s budget. Python 3.8+ stdlib only. No network.
import time

T0 = time.time()
sym = str(arg("symbol", "") or "")
if not re.match(r"^[A-Za-z_$][\w$]*$", sym):
    fail("symbol must be a plain identifier (letters, digits, _ or $)")
top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
lang_only = str(arg("lang", "any") or "any")


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


try:
    limit = max(1, min(5000, int(arg("limit", 300))))
except (TypeError, ValueError):
    fail("limit must be an integer")
include_defs = as_bool(ARGS.get("include_defs"), False)

MAX_FILES = 20000       # files visited at most
MAX_BYTES = 512 << 10   # bigger files are skipped (generated/minified/data)
BUDGET_S = 4.0          # wall-clock budget: stop walking, mark truncated
MAX_TEXT = 200          # chars of the line text kept

LANG = {".py": "py", ".go": "go", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
        ".ts": "js", ".tsx": "js", ".rs": "rs", ".java": "java", ".kt": "java",
        ".c": "c", ".h": "c", ".cc": "c", ".cpp": "c", ".hpp": "c", ".cxx": "c",
        ".rb": "rb", ".sh": "sh", ".bash": "sh"}
LINE_COMMENT = {"py": "#", "rb": "#", "sh": "#", "go": "//", "js": "//",
                "rs": "//", "c": "//", "java": "//"}
BLOCK_LANGS = set(["go", "js", "rs", "c", "java"])

s = re.escape(sym)
WORD = re.compile(r"(?<![\w$])%s(?![\w$])" % s)
# Per-language definition patterns: a line that *declares* the symbol.
DEFS = {
    "go": [r"^\s*func\s+(\([^)]*\)\s*)?%s\s*[\[(]", r"^\s*type\s+%s\b",
           r"^\s*(var|const)\s+%s\b", r"^\s*%s\s*(,\s*\w+\s*)*:="],
    "py": [r"^\s*(async\s+)?def\s+%s\s*\(", r"^\s*class\s+%s\b", r"^\s*%s\s*(:[^=]+)?=[^=]"],
    "js": [r"^\s*(export\s+)?(default\s+)?(async\s+)?function\s*\*?\s*%s\s*[(<]",
           r"^\s*(export\s+)?(default\s+)?(abstract\s+)?class\s+%s\b",
           r"^\s*(export\s+)?(declare\s+)?(interface|type|enum)\s+%s\b",
           r"^\s*(export\s+)?(const|let|var)\s+%s\b"],
    "rs": [r"^\s*(pub(\([^)]*\))?\s+)?(async\s+)?(unsafe\s+)?fn\s+%s\b",
           r"^\s*(pub(\([^)]*\))?\s+)?(struct|enum|trait|type|union)\s+%s\b",
           r"^\s*(pub\s+)?(static|const)\s+%s\b"],
    "c": [r"^[\w\s\*]+[\s\*]%s\s*\([^;]*$", r"^\s*(typedef\s+)?(struct|enum|union)\s+%s\b",
          r"^\s*#\s*define\s+%s\b"],
    "java": [r"^\s*(public|private|protected|abstract|final|static|\s)*(class|interface|enum|record)\s+%s\b",
             r"^\s*(public|private|protected|static|final|abstract|\s)+[\w<>\[\], ?]+\s+%s\s*\("],
    "rb": [r"^\s*def\s+(self\.)?%s\b", r"^\s*(class|module)\s+%s\b"],
    "sh": [r"^\s*(function\s+)?%s\s*\(\)", r"^\s*function\s+%s\b"],
}
DEF_RE = dict((lg, [re.compile(p % s) for p in pats]) for lg, pats in DEFS.items())


def scan_line(line, lang, in_block, want):
    """Best-effort char mask for a single line. Returns (str_mask, com_mask, in_block_after).
    Masks are built only when want is true; in_block carries /* */ state across lines."""
    n = len(line)
    smask = [False] * n if want else None
    cmask = [False] * n if want else None
    lc = LINE_COMMENT.get(lang)
    has_block = lang in BLOCK_LANGS
    instr = None
    i = 0
    while i < n:
        c = line[i]
        if in_block:
            if want:
                cmask[i] = True
            if has_block and c == "*" and i + 1 < n and line[i + 1] == "/":
                if want:
                    cmask[i + 1] = True
                in_block = False
                i += 2
                continue
            i += 1
            continue
        if instr:
            if want:
                smask[i] = True
            if c == "\\" and instr in ('"', "'") and i + 1 < n:
                if want:
                    smask[i + 1] = True
                i += 2
                continue
            if c == instr:
                instr = None
            i += 1
            continue
        if has_block and c == "/" and i + 1 < n and line[i + 1] == "*":
            in_block = True
            if want:
                cmask[i] = cmask[i + 1] = True
            i += 2
            continue
        if lc and line.startswith(lc, i):
            if want:
                for k in range(i, n):
                    cmask[k] = True
            break
        if c in ('"', "'", "`"):
            instr = c
            if want:
                smask[i] = True
            i += 1
            continue
        i += 1
    return smask, cmask, in_block


def is_def_line(lang, text):
    for rx in DEF_RE.get(lang, []):
        if rx.search(text):
            return True
    return False


if os.path.isfile(top):
    candidates = [top]
else:
    candidates = []
    for p in walk_files(top, limit=MAX_FILES):
        lg = LANG.get(os.path.splitext(p)[1].lower())
        if not lg or (lang_only != "any" and lg != lang_only):
            continue
        candidates.append(p)

refs, defs, by_file = [], [], {}
files_scanned, truncated, warnings = 0, False, []
ref_total = 0
for p in candidates:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % files_scanned)
        break
    lg = LANG.get(os.path.splitext(p)[1].lower())
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
    if sym not in src:
        continue
    rp = rel(p).replace(os.sep, "/")
    in_block = False
    hits_here = 0
    for ln, line in enumerate(src.splitlines(), 1):
        want = sym in line
        _, cmask, in_block_next = scan_line(line, lg, in_block, want)
        smask = _
        if not want:
            in_block = in_block_next
            continue
        defline = is_def_line(lg, line)
        for m in WORD.finditer(line):
            col = m.start()
            if defline:
                # record once; the definition itself is not a reference
                continue
            in_s = bool(smask[col]) if smask is not None and col < len(smask) else False
            in_c = bool(cmask[col]) if cmask is not None and col < len(cmask) else False
            ref_total += 1
            if len(refs) < limit:
                refs.append({"file": rp, "line": ln, "col": col + 1,
                             "text": clip(line.strip(), MAX_TEXT),
                             "in_comment": in_c, "in_string": in_s})
            hits_here += 1
        if defline:
            defs.append({"file": rp, "line": ln})
            if include_defs:
                for m in WORD.finditer(line):
                    col = m.start()
                    in_s = bool(smask[col]) if smask is not None and col < len(smask) else False
                    in_c = bool(cmask[col]) if cmask is not None and col < len(cmask) else False
                    ref_total += 1
                    if len(refs) < limit:
                        refs.append({"file": rp, "line": ln, "col": col + 1,
                                     "text": clip(line.strip(), MAX_TEXT),
                                     "in_comment": in_c, "in_string": in_s})
                    hits_here += 1
        in_block = in_block_next
    if hits_here:
        by_file[rp] = by_file.get(rp, 0) + hits_here

if len(candidates) >= MAX_FILES:
    truncated = True
    warnings.append("stopped at %d files" % MAX_FILES)
if ref_total > len(refs):
    truncated = True
    warnings.append("%d references; showing %d (raise limit or narrow path)" % (ref_total, len(refs)))

refs.sort(key=lambda r: (r["file"], r["line"], r["col"]))
defs.sort(key=lambda d: (d["file"], d["line"]))
top_files = sorted(by_file.items(), key=lambda kv: (-kv[1], kv[0]))[:20]

data = {
    "symbol": sym,
    "files": len(by_file),
    "refs": refs,
    "by_file": dict(top_files),
    "defs": defs[:200],
    "truncated": truncated,
    "ms": int((time.time() - T0) * 1000),
}
data["text"] = "%d ref%s in %d file%s" % (ref_total, "" if ref_total == 1 else "s",
                                          len(by_file), "" if len(by_file) == 1 else "s")
emit(True, data, 0.85 if ref_total else (0.6 if defs else 0.5), [], warnings)
