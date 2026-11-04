# large_funcs: functions and methods longer than N lines, returned as DATA the brain can
# reason over (the quality gate today reports this only as prose). Python bodies are measured
# by indentation; Go/JS/TS/Rust by simple balanced-brace counting that skips strings and
# comments — best-effort, so each row carries a per-language confidence. Walk + regex, bounded
# by a 4 s budget. Python 3.8+ stdlib only. No network.
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
lang_only = str(arg("lang", "any") or "any")
try:
    min_lines = max(1, min(100000, int(arg("min_lines", 60))))
except (TypeError, ValueError):
    fail("min_lines must be an integer")
try:
    limit = max(1, min(5000, int(arg("limit", 100))))
except (TypeError, ValueError):
    fail("limit must be an integer")

MAX_FILES = 20000
MAX_BYTES = 512 << 10
BUDGET_S = 4.0

LANG = {".py": "py", ".go": "go", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
        ".ts": "js", ".tsx": "js", ".rs": "rs"}
CONF = {"py": 0.95, "go": 0.85, "js": 0.7, "rs": 0.85}
BLOCK_LANGS = set(["go", "js", "rs"])
LINE_COMMENT = {"go": "//", "js": "//", "rs": "//", "py": "#"}

PY_DEF = re.compile(r"^([ \t]*)(?:async[ \t]+)?def[ \t]+([A-Za-z_]\w*)")
GO_FUNC = re.compile(r"^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]")
RS_FN = re.compile(r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+\"[^\"]*\"\s+)?fn\s+([A-Za-z_]\w*)")
JS_FUNC = re.compile(r"^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)")
JS_ARROW = re.compile(r"^\s*(?:export\s+)?(?:default\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?\([^;=]*\)\s*(?::[^=]+)?=>\s*\{")
JS_METHOD = re.compile(r"^\s*(?:public\s+|private\s+|protected\s+|static\s+|async\s+|get\s+|set\s+|readonly\s+|override\s+)*([A-Za-z_$][\w$]*)\s*\([^;=]*\)\s*(?::[^={]+)?\{\s*$")
JS_KEYWORDS = set(("if else for while switch catch return function do try finally "
                   "with case default throw new typeof delete void yield await").split())


def brace_span(lines, i, lang, maxlines=20000):
    """Line index where the function body opened at/after line i closes. Skips braces inside
    strings and comments, best-effort. Returns i if no brace block is found."""
    depth = 0
    started = False
    in_block = False
    instr = None
    j = i
    end = i
    while j < len(lines) and j < i + maxlines:
        line = lines[j]
        k, n = 0, len(line)
        while k < n:
            c = line[k]
            if in_block:
                if c == "*" and k + 1 < n and line[k + 1] == "/":
                    in_block = False
                    k += 2
                    continue
                k += 1
                continue
            if instr:
                if c == "\\" and instr in ('"', "'") and k + 1 < n:
                    k += 2
                    continue
                if c == instr:
                    instr = None
                k += 1
                continue
            if lang in BLOCK_LANGS and c == "/" and k + 1 < n and line[k + 1] == "*":
                in_block = True
                k += 2
                continue
            if c == "/" and k + 1 < n and line[k + 1] == "/":
                break
            if c in ('"', "'", "`"):
                instr = c
                k += 1
                continue
            if c == "{":
                depth += 1
                started = True
            elif c == "}":
                depth -= 1
                if started and depth <= 0:
                    return j
            k += 1
        end = j
        j += 1
    return end if started else i


def py_funcs(lines):
    out = []
    for i, l in enumerate(lines):
        m = PY_DEF.match(l)
        if not m:
            continue
        indent = len(m.group(1).expandtabs(4))
        name = m.group(2)
        j, last = i + 1, i
        while j < len(lines):
            sline = lines[j]
            if not sline.strip():
                j += 1
                continue
            lead = sline[:len(sline) - len(sline.lstrip())]
            if len(lead.expandtabs(4)) <= indent:
                break
            last = j
            j += 1
        out.append((i + 1, name, last - i + 1))
    return out


def brace_funcs(lines, lang):
    out = []
    for i, l in enumerate(lines):
        st = l.lstrip()
        if st.startswith(("//", "/*", "*", "#")):
            continue
        name = None
        if lang == "go":
            m = GO_FUNC.match(l)
            if m:
                name = m.group(1)
        elif lang == "rs":
            m = RS_FN.match(l)
            if m:
                name = m.group(1)
        else:  # js/ts
            m = JS_FUNC.match(l) or JS_ARROW.match(l)
            if m:
                name = m.group(1)
            else:
                m = JS_METHOD.match(l)
                if m and m.group(1) not in JS_KEYWORDS:
                    name = m.group(1)
        if not name:
            continue
        end = brace_span(lines, i, lang)
        cnt = end - i + 1
        if cnt >= 1:
            out.append((i + 1, name, cnt))
    return out


if os.path.isfile(top):
    files = [top]
else:
    files = []
    for p in walk_files(top, limit=MAX_FILES):
        lg = LANG.get(os.path.splitext(p)[1].lower())
        if not lg or (lang_only != "any" and lg != lang_only):
            continue
        files.append(p)

funcs, files_scanned, truncated, warnings = [], 0, False, []
for p in files:
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
        continue
    files_scanned += 1
    lines = raw.decode("utf-8", "replace").splitlines()
    found = py_funcs(lines) if lg == "py" else brace_funcs(lines, lg)
    rp = rel(p).replace(os.sep, "/")
    for (ln, name, cnt) in found:
        if cnt >= min_lines:
            funcs.append({"file": rp, "line": ln, "name": name, "lines": cnt,
                          "lang": lg, "confidence": CONF[lg]})

if len(files) >= MAX_FILES:
    truncated = True
    warnings.append("stopped at %d files" % MAX_FILES)

funcs.sort(key=lambda f: (-f["lines"], f["file"], f["line"]))
count = len(funcs)
if count > limit:
    truncated = True
    warnings.append("%d functions over %d lines; showing %d" % (count, min_lines, limit))
    funcs = funcs[:limit]

if funcs:
    top_f = funcs[0]
    text = "%d function%s over %d lines (max: %s %s:%d %d lines)" % (
        count, "" if count == 1 else "s", min_lines,
        top_f["name"], top_f["file"], top_f["line"], top_f["lines"])
else:
    text = "no function over %d lines in %d files" % (min_lines, files_scanned)

data = {"funcs": funcs, "count": count, "files_scanned": files_scanned,
        "truncated": truncated, "ms": int((time.time() - T0) * 1000), "text": text}
emit(True, data, 0.9 if files_scanned else 0.3, [], warnings)
