# diff_quality: the old-coder checklist over the working-tree diff (vs HEAD or base):
# debug prints, new TODO/FIXME, commented-out code, long functions that were touched, tests touched.
base = str(arg("base", "HEAD"))
if base.startswith("-"):
    fail("base may not start with '-'")
max_lines = int(arg("max_func_lines", 80))
code, _, err = run_argv(["git", "rev-parse", "--show-toplevel"], timeout=10)
if code != 0:
    fail("not a git repository")
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
if run_argv(["git", "rev-parse", "--verify", "-q", base + "^{commit}"], timeout=10)[0] != 0:
    if base != "HEAD":
        fail("unknown base: %s" % base)
    base = EMPTY_TREE  # no commits yet: everything is new
code, diff, err = run_argv(["git", "-c", "core.quotepath=off", "diff", "--no-color", "--no-ext-diff", "-U0", base, "--"],
                           timeout=40)
if code != 0:
    fail("git diff failed: %s" % clip(err.strip(), 200))
_, untracked, _ = run_argv(["git", "-c", "core.quotepath=off", "ls-files", "--others", "--exclude-standard"], timeout=20)

added = {}  # file -> [(line, text)]
removed = 0
cur, ln = None, 0
for l in diff.splitlines():
    if l.startswith("+++ "):
        p = l[4:]
        cur = None if p == "/dev/null" else (p[2:] if p.startswith("b/") else p)
        if cur is not None:
            added.setdefault(cur, [])
        continue
    if l.startswith("--- "):
        continue
    m = re.match(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@", l)
    if m:
        ln = int(m.group(1))
        continue
    if l.startswith("+") and cur is not None:
        added[cur].append((ln, l[1:]))
        ln += 1
    elif l.startswith("-"):
        removed += 1
for p in untracked.splitlines():
    full = os.path.join(ROOT, p)
    try:
        if os.path.getsize(full) > (1 << 20):
            continue
        with open(full, "rb") as f:
            raw = f.read()
    except (IOError, OSError):
        continue
    if b"\0" in raw[:8000]:
        continue
    added[p] = [(i + 1, t) for i, t in enumerate(raw.decode("utf-8", "replace").splitlines())]

TEST = re.compile(r"(_test\.go$|(^|/)test_[^/]*\.py$|_test\.py$|\.(test|spec)\.[jt]sx?$|(^|/)(tests?|__tests__|spec)/|Test\.java$|_spec\.rb$)")
DOC = re.compile(r"\.(md|txt|rst|adoc)$|(^|/)(docs?|LICENSE|CHANGELOG)", re.I)
SRC = re.compile(r"\.(go|py|js|jsx|ts|tsx|mjs|cjs|rs|c|h|cc|cpp|hpp|java|kt|rb|php|cs|swift|sh|lua)$")
DEBUG = {
    "py": re.compile(r"^\s*(print\(|breakpoint\(\)|import i?pdb|i?pdb\.set_trace\(|pprint\()"),
    "js": re.compile(r"\bconsole\.(log|debug|trace|dir)\(|^\s*debugger\b"),
    "go": re.compile(r"^\s*(println\(|print\(|spew\.Dump\(|fmt\.Print(ln|f)?\(\s*\"(DEBUG|debug|XXX|>>>|here|aqui))"),
    "rs": re.compile(r"\bdbg!\(|^\s*e?println!\(\"(DEBUG|debug|>>>)"),
    "java": re.compile(r"System\.(out|err)\.print(ln)?\(|\.printStackTrace\(\)"),
    "rb": re.compile(r"\b(binding\.pry|byebug|debugger)\b|^\s*(p|pp) "),
    "php": re.compile(r"\b(var_dump|print_r|dd)\("),
    "c": re.compile(r"^\s*printf\(\"(DEBUG|debug|>>>)"),
}
EXT = {"py": "py", "js": "js", "jsx": "js", "ts": "js", "tsx": "js", "mjs": "js", "cjs": "js", "vue": "js", "go": "go",
       "rs": "rs", "java": "java", "kt": "java", "rb": "rb", "php": "php", "c": "c", "h": "c", "cc": "c", "cpp": "c"}
TODO = re.compile(r"\b(TODO|FIXME|XXX|HACK)\b")
COMMENT = re.compile(r"^\s*(//+|#+|/\*+|\*|--)\s?(.*)$")
CODEY = re.compile(r"^(if|for|while|return|func|def|var|let|const|import|from|class|else|elif|try|except|switch|case)\b.*[:{(;]|"
                   r"[;{}]\s*$|^[\w.\[\]]+\s*(:=|=|\+=|-=)\s*\S|^[\w.]+\([^)]*\)\s*;?$")

issues = []


def issue(kind, f, line, text):
    issues.append({"kind": kind, "file": f, "line": line, "text": clip(text.strip(), 160)})


def lang_of(f):
    return EXT.get(f.rsplit(".", 1)[-1].lower()) if "." in f else None


def functions(f, lang):
    """(name, start, end) of functions in the new file, by indentation (py) or braces (others)."""
    try:
        with open(os.path.join(ROOT, f), "rb") as fh:
            lines = fh.read(2 << 20).decode("utf-8", "replace").splitlines()
    except (IOError, OSError):
        return []
    out = []
    if lang == "py":
        starts = [(i, len(m.group(1)), m.group(2)) for i, l in enumerate(lines)
                  for m in [re.match(r"^(\s*)(?:async\s+)?def\s+(\w+)", l)] if m]
        for i, ind, name in starts:
            end = len(lines)
            for j in range(i + 1, len(lines)):
                s = lines[j]
                if s.strip() and len(s) - len(s.lstrip()) <= ind and not s.lstrip().startswith(("#", ")", "]")):
                    end = j
                    break
            out.append((name, i + 1, end))
        return out
    pat = {"go": r"^func\s+(?:\([^)]*\)\s*)?(\w+)", "js": r"^\s*(?:export\s+)?(?:async\s+)?function\s*\*?\s*(\w+)|^\s*(?:const|let)\s+(\w+)\s*=\s*(?:async\s*)?\(",
           "rs": r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)", "java": r"^\s*(?:public|private|protected|static|\s)+[\w<>\[\]]+\s+(\w+)\s*\([^;]*$",
           "c": r"^[\w\s\*]+[\s\*](\w+)\s*\([^;]*$", "php": r"^\s*(?:public|private|protected|static|\s)*function\s+(\w+)",
           "rb": None}.get(lang)
    if not pat:
        return []
    rx = re.compile(pat)
    for i, l in enumerate(lines):
        m = rx.match(l)
        if not m:
            continue
        depth, seen, end = 0, False, None
        for j in range(i, min(len(lines), i + 3000)):
            s = re.sub(r"\"(?:[^\"\\]|\\.)*\"|'(?:[^'\\]|\\.)*'|//.*$", "", lines[j])
            depth += s.count("{") - s.count("}")
            seen = seen or "{" in s
            if seen and depth <= 0:
                end = j + 1
                break
        if end:
            out.append((next(g for g in m.groups() if g), i + 1, end))
    return out


tests_touched = code_touched = False
total_added = 0
for f, lines in sorted(added.items()):
    total_added += len(lines)
    if TEST.search(f):
        tests_touched = True
    elif SRC.search(f) and not DOC.search(f):
        code_touched = True
    lang = lang_of(f)
    dbg_re = DEBUG.get(lang)
    is_test = bool(TEST.search(f))
    run_comment = 0
    for line, text in lines:
        if TODO.search(text):
            issue("todo", f, line, text)
        if dbg_re and not is_test and dbg_re.search(text) and not COMMENT.match(text):
            issue("debug_print", f, line, text)
        m = COMMENT.match(text) if lang else None
        if m and CODEY.search(m.group(2).strip()) and not text.lstrip().startswith(("#!", "# -*-", "//go:", "// +build", "#include", "#define", "#if", "#endif", "#pragma")):
            run_comment += 1
            if run_comment == 1:
                issue("commented_code", f, line, text)
        else:
            run_comment = 0
    if lang and lines and os.path.exists(os.path.join(ROOT, f)):
        touched = set(l for l, _ in lines)
        for name, start, end in functions(f, lang):
            if end - start + 1 > max_lines and any(start <= t <= end for t in touched):
                issue("long_func", f, start, "%s is %d lines (limit %d)" % (name, end - start + 1, max_lines))
if code_touched and not tests_touched:
    issue("no_test", "", 0, "source files changed but no test file was touched")
counts = {}
for i in issues:
    counts[i["kind"]] = counts.get(i["kind"], 0) + 1
warn = []
if total_added > 400:
    warn.append("large diff: %d added lines; consider splitting" % total_added)
if len(issues) > 40:
    warn.append("%d issues; showing 40" % len(issues))
emit(True, {"base": "(empty tree)" if base == EMPTY_TREE else base, "files": sorted(added)[:60], "added": total_added,
            "removed": removed, "issues": issues[:40], "counts": counts, "tests_touched": tests_touched,
            "code_touched": code_touched}, 1.0, [], warn)
