# find_symbol: definitions of a symbol (per-language regexes over rg/grep hits) plus a reference count.
import shutil

sym = str(arg("symbol", ""))
if not re.match(r"^[A-Za-z_$][\w$]*$", sym):
    fail("symbol must be a plain identifier (letters, digits, _ or $)")
kind = arg("kind", "any")
where = path_in_root(arg("path", "."))
if not os.path.exists(where):
    fail("path not found: %s" % rel(where))
s = re.escape(sym)
DEFS = {
    "go": [("func", r"^\s*func\s+(\([^)]*\)\s*)?%s\s*[\[(]"), ("type", r"^\s*type\s+%s\b"),
           ("type", r"^\t%s\s+(struct|interface)\b"), ("var", r"^\s*(var|const)\s+%s\b"),
           ("var", r"^\s*%s\s*(,\s*\w+\s*)*:=")],
    "py": [("func", r"^\s*(async\s+)?def\s+%s\s*\("), ("class", r"^\s*class\s+%s\b"), ("var", r"^%s\s*(:[^=]+)?=[^=]")],
    "js": [("func", r"^\s*(export\s+)?(default\s+)?(async\s+)?function\s*\*?\s*%s\s*[(<]"),
           ("class", r"^\s*(export\s+)?(default\s+)?(abstract\s+)?class\s+%s\b"),
           ("type", r"^\s*(export\s+)?(declare\s+)?(interface|type|enum)\s+%s\b"),
           ("var", r"^\s*(export\s+)?(const|let|var)\s+%s\b"),
           ("func", r"^\s*(public|private|protected|static|async|get|set|\s)*%s\s*\([^)]*\)\s*(:\s*[\w<>\[\]|, ]+)?\s*\{")],
    "rs": [("func", r"^\s*(pub(\([^)]*\))?\s+)?(async\s+)?(unsafe\s+)?fn\s+%s\b"),
           ("type", r"^\s*(pub(\([^)]*\))?\s+)?(struct|enum|trait|type|union)\s+%s\b"),
           ("var", r"^\s*(pub\s+)?(static|const)\s+%s\b"), ("func", r"^\s*macro_rules!\s*%s\b")],
    "c": [("func", r"^[\w\s\*]+[\s\*]%s\s*\([^;]*$"), ("type", r"^\s*(typedef\s+)?(struct|enum|union|class)\s+%s\b"),
          ("var", r"^\s*#\s*define\s+%s\b")],
    "java": [("class", r"^\s*(public|private|protected|abstract|final|static|\s)*(class|interface|enum|record)\s+%s\b"),
             ("func", r"^\s*(public|private|protected|static|final|abstract|synchronized|\s)+[\w<>\[\], ?]+\s+%s\s*\(")],
    "rb": [("func", r"^\s*def\s+(self\.)?%s\b"), ("class", r"^\s*(class|module)\s+%s\b")],
    "sh": [("func", r"^\s*(function\s+)?%s\s*\(\)"), ("func", r"^\s*function\s+%s\b")],
}
EXT = {".go": "go", ".py": "py", ".js": "js", ".jsx": "js", ".ts": "js", ".tsx": "js", ".mjs": "js", ".cjs": "js",
       ".vue": "js", ".rs": "rs", ".c": "c", ".h": "c", ".cc": "c", ".cpp": "c", ".hpp": "c", ".cxx": "c",
       ".java": "java", ".kt": "java", ".scala": "java", ".cs": "java", ".rb": "rb", ".sh": "sh", ".bash": "sh"}
COMPILED = dict((lang, [(k, re.compile(p % s)) for k, p in pats]) for lang, pats in DEFS.items())
KIND_OK = {"any": None, "func": ("func",), "type": ("type", "class"), "class": ("class", "type"), "var": ("var",)}

rg = shutil.which("rg")
if rg:
    argv = [rg, "-n", "-w", "-F", "--no-heading", "--color", "never", "--max-columns", "300", "--max-filesize", "2M",
            "-g", "!vendor", "-g", "!node_modules", "-g", "!dist", "-g", "!*.min.js", "--", sym, where]
else:
    argv = ["grep", "-rnwFI", "--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude-dir=vendor",
            "--exclude-dir=dist", "--", sym, where]
code, out, err = run_argv(argv, timeout=40)
if code not in (0, 1):
    fail("search failed: %s" % clip(err.strip(), 200))
hits = out.splitlines()
truncated = len(hits) > 5000
hits = hits[:5000]
defs, ref_files, refs = [], {}, 0
for h in hits:
    m = re.match(r"^(.*?):(\d+):(.*)$", h)
    if not m:
        continue
    f, ln, txt = m.group(1), int(m.group(2)), m.group(3)
    lang = EXT.get(os.path.splitext(f)[1].lower())
    hit_kind = None
    for k, r in COMPILED.get(lang, []):
        if r.search(txt):
            hit_kind = k
            break
    if hit_kind and (KIND_OK[kind] is None or hit_kind in KIND_OK[kind]):
        defs.append({"file": rel(f), "line": ln, "kind": hit_kind, "text": clip(txt.strip(), 160)})
    else:
        refs += 1
        ref_files[rel(f)] = ref_files.get(rel(f), 0) + 1
top = sorted(ref_files.items(), key=lambda kv: -kv[1])[:10]
data = {"symbol": sym, "definitions": defs[:20], "refs": refs, "ref_files": [{"file": f, "count": c} for f, c in top],
        "files_with_refs": len(ref_files), "search": "rg" if rg else "grep"}
warn = []
if truncated:
    warn.append("over 5000 hits; counts are partial")
if not defs:
    warn.append("no definition matched the language patterns")
emit(True, data, 0.9 if defs else 0.6, [], warn)
