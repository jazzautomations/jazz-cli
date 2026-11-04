# dep_graph: import graph of the project (Python, Go, JS/TS) by line regex — no parser, no
# execution, no network. Nodes are Python modules, Go packages and JS/TS files; an edge goes
# from the importer to the imported module. Internal = resolves to a file/dir of the project
# (or starts with the go.mod module / package.json name); everything else is an external and
# only counted. Cycles are the back edges of one iterative DFS (a sample, capped at 20).
# One walk does both the index (paths of every source file, for resolution) and the raw
# import extraction of the files under `path`; resolution runs afterwards and is cheap.
# Test files are skipped (like api_map). Python 3.8+ stdlib only.
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
lang_only = str(arg("lang", "auto") or "auto")
limit = int(arg("limit", 2000))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
if lang_only not in ("auto", "py", "go", "js"):
    fail("lang must be one of auto, py, go, js")


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


internal_only = as_bool(arg("internal_only"), True)

MAX_FILES = 5000        # source files parsed at most
MAX_INDEX = 50000       # paths indexed at most (resolution targets)
MAX_BYTES = 512 << 10   # bigger files are skipped (generated/minified)
MAX_CYCLES = 20
BUDGET_S = 4.0          # wall-clock budget for the walk: stop, mark truncated (Go side waits 60 s)
SKIP = set("node_modules vendor dist build target __pycache__ venv env coverage third_party Pods testdata".split())
LANG = {".py": "py", ".go": "go", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js", ".ts": "js",
        ".tsx": "js", ".mts": "js"}
JS_EXTS = [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts"]
TEST_RE = re.compile(r"(^test_.*\.py$|_test\.(py|go)$|^conftest\.py$|\.(test|spec)\.[jt]sx?$|\.d\.ts$|\.min\.js$)")

PY_IMPORT = re.compile(r"^\s*import\s+([\w.]+(?:\s+as\s+\w+)?(?:\s*,\s*[\w.]+(?:\s+as\s+\w+)?)*)")
PY_FROM = re.compile(r"^\s*from\s+(\.+[\w.]*|[\w.]+)\s+import\s+(.*)$")
GO_ONE = re.compile(r'^\s*import\s+(?:[\w.]+\s+)?"([^"]+)"')
GO_BLOCK = re.compile(r"^\s*import\s*\(")
GO_LINE = re.compile(r'^\s*(?:[\w.]+\s+)?"([^"]+)"')
JS_SPEC = re.compile(r"""(?:\bfrom\s*|\bimport\s*\(?\s*|\brequire\s*\(\s*)['"]([^'"\n]+)['"]""")

# project base: ids are relative to the project root when path is inside it (rel() keeps
# the absolute path for anything outside ROOT), else to the path itself
top = os.path.normpath(top)
inside = not os.path.isabs(rel(top))
base = ROOT if inside else (top if os.path.isdir(top) else os.path.dirname(top))


def read_first(p, pat):
    try:
        with open(p, "rb") as f:
            for raw in f:
                m = pat.match(raw.decode("utf-8", "replace"))
                if m:
                    return m.group(1)
    except (IOError, OSError):
        pass
    return ""


def read_json_name(p):
    try:
        with open(p, "rb") as f:
            d = json.loads(f.read().decode("utf-8", "replace"))
        n = d.get("name") if isinstance(d, dict) else None
        return n if isinstance(n, str) else ""
    except (IOError, OSError, ValueError):
        return ""


go_module = read_first(os.path.join(base, "go.mod"), re.compile(r"^\s*module\s+(\S+)"))
js_pkg = read_json_name(os.path.join(base, "package.json"))


# ---- raw import extraction (per language, line regex) ----
def py_names(rest, lines, i):
    """Names of 'from X import a, b as c, (d,\n e)' (continuation lines bounded)."""
    s = rest.split("#", 1)[0].strip()
    j = i
    while "(" in s and ")" not in s and j + 1 < len(lines) and j < i + 40:
        j += 1
        s += " " + lines[j].split("#", 1)[0].strip()
    s = s.replace("(", " ").replace(")", " ").replace("\\", " ")
    out = []
    for part in s.split(","):
        tok = part.split()
        if tok and tok[0] != "*":
            out.append(tok[0])
    return out, j


def py_raw(lines):
    """[("i", dotted)] for import, [("f", module, [names])] for from-import."""
    out, i = [], 0
    while i < len(lines):
        l = lines[i]
        m = PY_IMPORT.match(l)
        if m:
            for part in m.group(1).split(","):
                out.append(("i", part.split()[0], None))
            i += 1
            continue
        m = PY_FROM.match(l)
        if m:
            names, i = py_names(m.group(2), lines, i)
            out.append(("f", m.group(1), names))
        i += 1
    return out


def go_raw(lines):
    out, in_block = [], False
    for l in lines:
        if in_block:
            if l.strip().startswith(")"):
                in_block = False
                continue
            m = GO_LINE.match(l)
            if m:
                out.append(m.group(1))
            continue
        if GO_BLOCK.match(l):
            in_block = True
            continue
        m = GO_ONE.match(l)
        if m:
            out.append(m.group(1))
        elif l.startswith(("func ", "type ", "var ", "const ")):
            break  # imports are over
    return out


def js_raw(lines):
    out = []
    for l in lines:
        s = l.lstrip()
        if s.startswith(("//", "*", "/*")):
            continue
        for m in JS_SPEC.finditer(l):
            spec = m.group(1).strip()
            if spec and not spec.startswith(("http:", "https:", "data:")):
                out.append(spec)
    return out


RAW = {"py": py_raw, "go": go_raw, "js": js_raw}

# ---- pass 1: walk once; index every source path, read + extract the files under `path` ----
index_files = set()     # rel paths of source files (all langs)
py_pkg_dirs = set()     # rel dirs that hold __init__.py
go_pkg_dirs = set()     # rel dirs that hold non-test .go files
parsed = []             # (rel path, lang, raw imports), in walk order (sorted)
truncated, warnings, unparsed = False, [], []
n_index = nfiles = 0
top_rel = os.path.relpath(top, base).replace(os.sep, "/") if os.path.isdir(top) else None
stop = None
for d, dirs, files in os.walk(base):
    dirs[:] = sorted(x for x in dirs if x not in SKIP and not x.startswith("."))
    rd = os.path.relpath(d, base).replace(os.sep, "/")
    rd = "" if rd == "." else rd
    for f in sorted(files):
        lg = LANG.get(os.path.splitext(f)[1].lower())
        if not lg:
            continue
        rp = (rd + "/" + f) if rd else f
        n_index += 1
        if n_index > MAX_INDEX:
            stop = "index cap reached (%d paths)" % MAX_INDEX
            break
        index_files.add(rp)
        if f == "__init__.py":
            py_pkg_dirs.add(rd)
        if TEST_RE.search(f):
            continue
        if lg == "go":
            go_pkg_dirs.add(rd)
        if lang_only != "auto" and lg != lang_only:
            continue
        full = os.path.join(d, f)
        if not ((top_rel is None and full == top) or (top_rel is not None and (top_rel == "." or rp == top_rel or rp.startswith(top_rel + "/")))):
            continue
        if nfiles >= MAX_FILES:
            stop = "file cap reached (%d files)" % MAX_FILES
            break
        if time.time() - T0 > BUDGET_S:
            stop = "time budget reached after %d files" % nfiles
            break
        try:
            if os.path.getsize(full) > MAX_BYTES:
                continue
            with open(full, "rb") as fh:
                lines = fh.read().decode("utf-8", "replace").splitlines()
        except (IOError, OSError):
            continue
        nfiles += 1
        parsed.append((rp, lg, RAW[lg](lines)))
    if stop:
        break
if stop:
    truncated = True
    warnings.append(stop + "; index incomplete, some internal imports may count as external")

# ---- resolution tables ----
py_lookup = {}  # dotted alias -> node id (first registered wins; files are visited sorted)


def py_id(rp):
    parts = rp[:-3].split("/")
    if parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(parts) if parts else "__root__"


for rp in sorted(index_files):
    if rp.endswith(".py"):
        full_id = py_id(rp)
        parts = full_id.split(".")
        # aliases: strip leading dirs that are not packages (src layout, tools/, scripts/ ...)
        for i in range(len(parts)):
            py_lookup.setdefault(".".join(parts[i:]), full_id)
            if i + 1 < len(parts) and "/".join(parts[: i + 1]) in py_pkg_dirs:
                break


def go_id(rd):
    if go_module:
        return go_module if rd in ("", ".") else go_module + "/" + rd
    return rd or "."


def go_dir_of(imp):
    """Import path -> project dir (rel) when it is inside the go.mod module."""
    if go_module:
        if imp == go_module:
            return ""
        if imp.startswith(go_module + "/"):
            return imp[len(go_module) + 1:]
        return None
    for rd in sorted(go_pkg_dirs):  # no go.mod: match by directory suffix
        if rd and (imp == rd or imp.endswith("/" + rd)):
            return rd
    return None


def js_try(p):
    """Resolve a path without/with extension or as a directory index; returns a rel file or None."""
    p = os.path.normpath(p).replace(os.sep, "/")
    if p.startswith("../") or p == "..":
        return None
    if p in index_files:
        return p
    stem, ext = os.path.splitext(p)
    cands = [p + e for e in JS_EXTS]
    if ext in (".js", ".jsx", ".mjs", ".cjs"):
        cands += [stem + e for e in JS_EXTS]
    cands += [p + "/index" + e for e in JS_EXTS]
    for c in cands:
        if c in index_files:
            return c
    return None


def js_resolve(spec, from_dir):
    if spec.startswith("."):
        return js_try(os.path.join(from_dir, spec) if from_dir else spec)
    if spec.startswith("/"):
        return None
    rest = None
    if js_pkg and (spec == js_pkg or spec.startswith(js_pkg + "/")):
        rest = spec[len(js_pkg) + 1:] or "index"
    elif spec[:2] in ("@/", "~/"):
        rest = spec[2:]
    if rest is not None:
        return js_try(rest) or js_try("src/" + rest)
    return js_try(spec)  # bare path that exists in the project (baseUrl style)


def js_external(spec):
    if spec.startswith("@") and "/" in spec:
        return "/".join(spec.split("/")[:2])
    return spec.split("/")[0]


def go_external(imp):
    parts = imp.split("/")
    if "." in parts[0] and len(parts) >= 3:
        return "/".join(parts[:3])
    return imp if "." not in parts[0] else "/".join(parts[:2])


# ---- pass 2: resolve raw imports into edges ----
nodes = {}      # id -> {"id","file","lang"}
edges = set()   # (from, to)
externals = {}  # name -> count


def node(nid, file, lg):
    if nid not in nodes:
        nodes[nid] = {"id": nid, "file": file, "lang": lg}
    return nid


def add_edge(src, dst):
    if src != dst:
        edges.add((src, dst))


def add_external(src, name, lg):
    externals[name] = externals.get(name, 0) + 1
    if not internal_only:
        add_edge(src, node(name, "", lg))


def py_edge(src, dotted, relative=False):
    parts = [p for p in dotted.split(".") if p]
    if not parts:
        return
    for k in range(len(parts), 0, -1):
        nid = py_lookup.get(".".join(parts[:k]))
        if nid:
            if nid not in nodes:
                f = nid.replace(".", "/")
                f = f + "/__init__.py" if f in py_pkg_dirs else (f + ".py" if f + ".py" in index_files else f + "/")
                node(nid, f, "py")
            add_edge(src, nid)
            return
    if not relative:
        add_external(src, parts[0], "py")


def py_edges(src, rp, raw):
    pkg = py_id(rp).split(".")
    if not rp.endswith("__init__.py"):
        pkg = pkg[:-1]
    for kind, mod, names in raw:
        if kind == "i":
            py_edge(src, mod)
            continue
        if mod.startswith("."):
            dots = len(mod) - len(mod.lstrip("."))
            up = len(pkg) - (dots - 1)
            if up < 0:
                unparsed.append("%s: from %s import ..." % (rp, mod))
                continue
            head = pkg[:up] + ([mod.lstrip(".")] if mod.lstrip(".") else [])
            for n in names or [""]:
                py_edge(src, ".".join(head + ([n] if n else [])), relative=True)
        else:
            for n in names or [""]:
                py_edge(src, mod + ("." + n if n else ""))


def go_edges(src, raw):
    for imp in raw:
        rd = go_dir_of(imp)
        if rd is None:
            add_external(src, go_external(imp), "go")
        else:
            add_edge(src, node(go_id(rd), rd or ".", "go"))


def js_edges(src, rp, raw):
    from_dir = os.path.dirname(rp)
    for spec in raw:
        target = js_resolve(spec, from_dir)
        if target:
            add_edge(src, node(target, target, "js"))
        elif spec.startswith(".") or spec[:2] in ("@/", "~/"):
            unparsed.append("%s: unresolved %s" % (rp, spec))
        else:
            add_external(src, js_external(spec), "js")


for rp, lg, raw in parsed:
    if lg == "py":
        py_edges(node(py_lookup.get(py_id(rp)) or py_id(rp), rp, "py"), rp, raw)
    elif lg == "go":
        rd = os.path.dirname(rp)
        go_edges(node(go_id(rd), rd or ".", "go"), raw)
    else:
        js_edges(node(rp, rp, "js"), rp, raw)

# ---- graph stats ----
indeg = dict((n, 0) for n in nodes)
outdeg = dict((n, 0) for n in nodes)
adj = {}
for a, b in edges:
    outdeg[a] += 1
    indeg[b] += 1
    adj.setdefault(a, []).append(b)
for a in adj:
    adj[a].sort()

# cycles: back edges of an iterative DFS over sorted adjacency (a sample, not all cycles)
color, path, pos, found = {}, [], {}, {}
cap_hit = False
for s in sorted(nodes):
    if color.get(s) or cap_hit:
        continue
    color[s] = 1
    path.append(s)
    pos[s] = 0
    stack = [(s, iter(adj.get(s, ())))]
    while stack and not cap_hit:
        u, it = stack[-1]
        advanced = False
        for v in it:
            c = color.get(v, 0)
            if c == 0:
                color[v] = 1
                pos[v] = len(path)
                path.append(v)
                stack.append((v, iter(adj.get(v, ()))))
                advanced = True
                break
            if c == 1:
                cyc = path[pos[v]:]
                k = cyc.index(min(cyc))
                cyc = cyc[k:] + cyc[:k]
                found.setdefault(tuple(cyc), True)
                if len(found) >= MAX_CYCLES:
                    cap_hit = True
                    break
        if not advanced:
            stack.pop()
            color[u] = 2
            path.pop()
            pos.pop(u, None)
if cap_hit:
    warnings.append("cycle cap reached (%d): more cycles may exist" % MAX_CYCLES)
cycles = sorted((list(c) for c in found), key=lambda c: (len(c), c))

node_list = [{"id": n, "file": nodes[n]["file"], "lang": nodes[n]["lang"], "in": indeg[n], "out": outdeg[n]}
             for n in sorted(nodes)]
edge_list = [{"from": a, "to": b} for a, b in sorted(edges)]
if len(edge_list) > limit:
    edge_list = edge_list[:limit]
    truncated = True
    warnings.append("edges cut to limit %d of %d" % (limit, len(edges)))
ext_sorted = sorted(externals.items(), key=lambda kv: (-kv[1], kv[0]))[:50]
most = [{"id": n["id"], "in": n["in"]} for n in sorted(node_list, key=lambda n: (-n["in"], n["id"])) if n["in"] > 0][:10]
if nfiles and not go_module and any(n["lang"] == "go" for n in node_list):
    warnings.append("no go.mod: Go imports matched by directory suffix only")

emit(data={"files": nfiles, "nodes": node_list, "edges": edge_list, "cycles": cycles,
           "externals": dict(ext_sorted), "most_imported": most, "truncated": truncated,
           "ms": int((time.time() - T0) * 1000),
           "text": "%d files, %d edges, %d cycles" % (nfiles, len(edges), len(cycles))},
     confidence=(0.6 if stop else 0.85) if nfiles else 0.5, unparsed=unparsed, warnings=warnings)
