# api_map: the project's public API (functions, classes, methods, types) with signatures and the
# first line of their doc comment, by regex (no import, no parser). Python 3.8+ stdlib only.
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
query = str(arg("query", "") or "")
lang_only = arg("lang", "any")
limit = int(arg("limit", 12 if query else 150))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

MAX_FILES = 8000        # source files parsed at most
MAX_BYTES = 512 << 10   # bigger files are skipped (generated/minified)
PER_FILE = 40           # symbols kept per file
BUDGET_S = 4.0          # wall-clock budget: stop walking, mark truncated (Go side waits 6 s)
LANG = {".py": "py", ".go": "go", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js", ".ts": "js", ".tsx": "js",
        ".mts": "js", ".rs": "rs"}
TEST_RE = re.compile(r"(^test_.*\.py$|_test\.(py|go)$|\.(test|spec)\.[jt]sx?$|\.d\.ts$|\.min\.js$)")

PY_DEF = re.compile(r"^([ \t]*)(async[ \t]+)?(def|class)[ \t]+([A-Za-z_]\w*)")
GO_FUNC = re.compile(r"^func[ \t]*(\([ \t]*\w*[ \t]*\*?[ \t]*(\w+)(\[[^\]]*\])?[ \t]*\))?[ \t]*([A-Z]\w*)")
GO_TYPE = re.compile(r"^type[ \t]+([A-Z]\w*)(\[[^\]]*\])?[ \t]+(struct|interface|func\b|[\w.*\[\]]+)")
JS_DECL = [
    ("func", re.compile(r"^export[ \t]+(?:default[ \t]+)?(?:async[ \t]+)?function\*?[ \t]*([A-Za-z_$][\w$]*)")),
    ("class", re.compile(r"^export[ \t]+(?:default[ \t]+)?(?:abstract[ \t]+)?class[ \t]+([A-Za-z_$][\w$]*)")),
    ("func", re.compile(r"^export[ \t]+(?:const|let|var)[ \t]+([A-Za-z_$][\w$]*)[ \t]*(?::[^=]+)?=[ \t]*(?:async[ \t]*)?(?:\([^)]*\)|[\w$]+)[ \t]*(?::[^=]+)?=>")),
    ("type", re.compile(r"^export[ \t]+(?:declare[ \t]+)?(?:interface|type|enum)[ \t]+([A-Za-z_$][\w$]*)")),
    ("func", re.compile(r"^(?:module\.)?exports\.([A-Za-z_$][\w$]*)[ \t]*=[ \t]*(?:async[ \t]*)?function")),
]
RS_DECL = re.compile(r"^[ \t]*pub(?:\([^)]*\))?[ \t]+(?:async[ \t]+)?(?:unsafe[ \t]+)?(?:const[ \t]+)?(fn|struct|enum|trait|type)[ \t]+(\w+)")


def collect_sig(lines, i, start_col=0, maxlines=8, with_end=False):
    """Join lines from i until the parentheses opened on line i close (bounded)."""
    out, depth, seen, j = [], 0, False, i
    for j in range(i, min(len(lines), i + maxlines)):
        s = lines[j] if j > i else lines[j][start_col:]
        out.append(s.strip())
        for ch in s:
            if ch in "([":
                depth += 1
                seen = True
            elif ch in ")]":
                depth -= 1
        if (seen and depth <= 0) or not seen:
            break  # parentheses closed, or none opened on the first line
    sig = re.sub(r"\s+", " ", " ".join(out))
    if with_end:
        return sig, j
    return sig


def first_doc_line(text):
    for l in text.strip().splitlines():
        l = l.strip().strip("*/").strip()
        if l:
            return clip(l, 140)
    return ""


def comment_above(lines, i, prefix):
    """First line of the comment block right above line i (// or /// or /** */)."""
    j = i - 1
    while j >= 0 and re.match(r"^\s*(#\[|@)", lines[j]):  # attributes / decorators
        j -= 1
    block = []
    while j >= 0:
        s = lines[j].strip()
        if s.startswith(prefix):
            block.insert(0, s[len(prefix):].strip())
            j -= 1
            continue
        if s.endswith("*/"):
            k = j
            while k >= 0 and "/**" not in lines[k] and "/*" not in lines[k]:
                k -= 1
            if k >= 0:
                body = " ".join(x.strip().lstrip("/*").strip() for x in lines[k:j + 1])
                block.insert(0, body.replace("*/", "").strip())
        break
    for b in block:
        if b and not b.startswith("@"):
            return clip(b, 140)
    return ""


def py_symbols(lines):
    out, classes = [], []  # classes: stack of (indent, name)
    for i, l in enumerate(lines):
        m = PY_DEF.match(l)
        if not m:
            continue
        indent = len(m.group(1).expandtabs(4))
        kind, name = m.group(3), m.group(4)
        while classes and classes[-1][0] >= indent:
            classes.pop()
        owner = classes[-1][1] if classes else ""
        if kind == "class":
            classes.append((indent, name))
        if name.startswith("_") and name != "__init__":
            continue
        if indent > 0 and not owner:
            continue  # nested function
        if any(c[1].startswith("_") for c in classes[:-1] if kind == "class") or (owner.startswith("_")):
            continue
        sig, end = collect_sig(lines, i, with_end=True)
        while not lines[end].rstrip().endswith(":") and end + 1 < len(lines) and end < i + 8:
            end += 1  # return annotation on the next line
        sig = re.sub(r"^(async\s+)?(def|class)\s+", "", sig.rstrip(":").strip())
        doc = ""
        k = end + 1
        while k < len(lines) and not lines[k].strip():
            k += 1
        if k < len(lines):
            dm = re.match(r'^\s*[rRuUbB]{0,2}("""|\'\'\')(.*)', lines[k])
            if dm:
                rest = dm.group(2).split(dm.group(1))[0].strip()
                if not rest and k + 1 < len(lines):
                    rest = lines[k + 1].strip().split(dm.group(1))[0].strip()
                doc = clip(rest, 140)
        k_ = "method" if owner and kind == "def" else ("class" if kind == "class" else "func")
        full = (owner + "." + name) if owner and kind == "def" else name
        if owner and kind == "def":
            sig = owner + "." + sig
        out.append((i + 1, k_, full, clip(sig, 200), doc))
    return out


def go_symbols(lines):
    out = []
    for i, l in enumerate(lines):
        if not (l.startswith("func") or l.startswith("type")):
            continue
        m = GO_FUNC.match(l)
        if m:
            recv, name = m.group(2), m.group(4)
            if recv and not recv[:1].isupper():
                continue  # method of an unexported type
            j, parts = i, []
            while j < len(lines) and j < i + 8:  # up to the opening brace (multi-line params)
                parts.append(lines[j].strip())
                if "{" in lines[j] or not lines[j].rstrip().endswith(","):
                    break
                j += 1
            sig = re.sub(r"\s*\{.*$", "", " ".join(parts)).strip()
            kind = "method" if recv else "func"
            full = (recv + "." + name) if recv else name
            out.append((i + 1, kind, full, clip(sig, 200), comment_above(lines, i, "//")))
            continue
        m = GO_TYPE.match(l)
        if m:
            out.append((i + 1, "type", m.group(1), clip(l.rstrip("{ ").strip(), 200), comment_above(lines, i, "//")))
    return out


def js_symbols(lines):
    out = []
    for i, l in enumerate(lines):
        if not (l.startswith("export") or l.startswith("module.exports") or l.startswith("exports.")):
            continue
        for kind, rx in JS_DECL:
            m = rx.match(l)
            if m:
                sig = collect_sig(lines, i) if "(" in l else l.strip()
                sig = re.sub(r"\s*\{.*$", "", sig).strip()
                out.append((i + 1, kind, m.group(1), clip(sig, 200), comment_above(lines, i, "//")))
                break
    return out


def rs_symbols(lines):
    out = []
    for i, l in enumerate(lines):
        if "pub" not in l:
            continue
        m = RS_DECL.match(l)
        if m:
            sig = collect_sig(lines, i) if m.group(1) == "fn" else l.strip()
            sig = re.sub(r"\s*[{;].*$", "", sig).strip()
            out.append((i + 1, m.group(1), m.group(2), clip(sig, 200), comment_above(lines, i, "///")))
    return out


PARSE = {"py": py_symbols, "go": go_symbols, "js": js_symbols, "rs": rs_symbols}
PREFILTER = {"py": ("def ", "class "), "go": ("func ", "type "), "js": ("export",), "rs": ("pub ",)}

if os.path.isfile(top):
    files = [top]
else:
    files = []
    for p in walk_files(top, limit=200000):
        ext = os.path.splitext(p)[1].lower()
        lg = LANG.get(ext)
        if not lg or (lang_only != "any" and lg != lang_only) or TEST_RE.search(os.path.basename(p)):
            continue
        files.append(p)
        if len(files) >= MAX_FILES:
            break

syms, nfiles, truncated, warnings = [], 0, len(files) >= MAX_FILES, []
for p in files:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % nfiles)
        break
    lg = LANG.get(os.path.splitext(p)[1].lower())
    if not lg:
        continue
    try:
        if os.path.getsize(p) > MAX_BYTES:
            continue
        with open(p, "rb") as f:
            src = f.read().decode("utf-8", "replace")
    except (IOError, OSError):
        continue
    nfiles += 1
    if not any(k in src for k in PREFILTER[lg]):
        continue
    found = PARSE[lg](src.splitlines())
    for (ln, kind, name, sig, doc) in found[:PER_FILE]:
        syms.append({"file": rel(p), "line": ln, "kind": kind, "name": name, "sig": sig, "doc": doc})


def subwords(s):
    s = re.sub(r"([a-z0-9])([A-Z])", r"\1 \2", s)
    return [w for w in re.split(r"[^A-Za-z0-9]+", s.lower()) if len(w) >= 3]


STOP = set("the and for with from that this into use using add new make get set fix test tests file files code "
           "uma para com que dos das nos nas por como sem mais todo toda ser ter ver use usar faca crie adicione "
           "implemente implementar funcao metodo classe arquivo arquivos projeto teste testes".split())
if query:
    ids = set(re.findall(r"[A-Za-z_][\w]*", query))
    terms = set(w for w in subwords(query) if w not in STOP)
    exact = set(i.lower() for i in ids if len(i) >= 3 and i.lower() not in STOP)
    probe = tuple(terms | exact)
    for s in syms:
        blob = (s["name"] + " " + s["sig"] + " " + s["doc"] + " " + s["file"]).lower()
        if not any(t in blob for t in probe):  # cheap substring filter before word splitting
            s["score"] = 0
            continue
        name_l = s["name"].lower()
        last = name_l.split(".")[-1]
        sc = 0.0
        if last in exact or name_l in exact:
            sc += 10
        nw = set(subwords(s["name"]))
        sc += 3 * len(nw & terms)
        sw = set(subwords(s["sig"])) - nw
        sc += 1.0 * len(sw & terms)
        dw = set(subwords(s["doc"]))
        sc += 1.0 * len(dw & terms)
        fw = set(subwords(s["file"]))
        sc += 1.0 * len(fw & terms)
        s["score"] = round(sc, 1)
    ranked = sorted((s for s in syms if s["score"] > 0), key=lambda s: (-s["score"], s["file"], s["line"]))
    matches = ranked[:limit]
else:
    matches = syms[:limit]

lines_out = []
for s in matches:
    sig = s["sig"]
    lead = "" if sig.split(" ", 1)[0] in ("func", "type", "fn", "struct", "enum", "trait", "class", "def", "pub", "export") else s["kind"] + " "
    l = "%s:%d %s%s" % (s["file"], s["line"], lead, sig)
    if s["doc"]:
        l += " — " + s["doc"]
    lines_out.append(clip(l, 260))
emit(data={"files": nfiles, "symbols": len(syms), "truncated": truncated, "ms": int((time.time() - T0) * 1000),
           "matches": matches, "text": "\n".join(lines_out)},
     confidence=0.8 if matches else 0.5, warnings=warnings)
