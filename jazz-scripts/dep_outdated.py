# dep_outdated: what the manifest asks for versus what the lockfile fixed — offline, no
# registry, no npm/pip/go. Pairs package.json x (package-lock.json | npm-shrinkwrap.json |
# yarn.lock | pnpm-lock.yaml), pyproject.toml / requirements*.txt x (poetry.lock | uv.lock |
# pdm.lock | requirements lock with ==) and go.mod x go.sum. Reports deps without a lock
# entry (unlocked), loose ranges (^ ~ * >= ...) and lock pins outside the spec (mismatched).
# Version checks are equality/prefix only (exact, ^, ~, x-ranges, == prefixes); range
# operators (>=, <, ||, -) are never evaluated. Python 3.8+ stdlib only.
import time

T0 = time.time()
top = os.path.normpath(path_in_root(arg("path", ".")))
limit = int(arg("limit", 200))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))

BUDGET_S = 4.0
MAX_BYTES = 16 << 20     # lockfiles above this are skipped
MAX_MANIFESTS = 20
MAX_DEPTH = 2            # manifest discovery depth below path
SKIP = set("node_modules vendor dist build target __pycache__ venv env coverage third_party Pods testdata".split())
MANIFEST_RE = re.compile(r"^(package\.json|pyproject\.toml|go\.mod|requirements[\w.-]*\.txt)$")
DEVISH = set("dev develop development test tests testing lint docs typing".split())
warnings, unparsed = [], []
confidence = 1.0


def lower_conf(c):
    global confidence
    confidence = min(confidence, c)


def read_file(p):
    try:
        if os.path.getsize(p) > MAX_BYTES:
            warnings.append("%s skipped: over %d MB" % (rel(p), MAX_BYTES >> 20))
            return None
        with open(p, "rb") as f:
            return f.read().decode("utf-8", "replace")
    except (IOError, OSError) as e:
        warnings.append("%s: %s" % (rel(p), e))
        return None


def norm_py(name):
    return re.sub(r"[-_.]+", "-", name.strip().lower())


def nums(v):
    """Leading numeric dot components of a version: 'v1.2.3-beta' -> [1, 2, 3]."""
    v = str(v).strip().lstrip("v=")
    out = []
    for part in v.split("."):
        m = re.match(r"^\d+", part)
        if not m:
            break
        out.append(int(m.group(0)))
        if m.group(0) != part:
            break
    return out


def is_exact_version(s):
    return re.match(r"^v?\d+(\.\d+)*([-+][\w.-]*)?$", s) is not None


# ---- spec classification: ("exact", version) | ("loose", None) | ("other", None) ----
def classify(kind, spec):
    s = str(spec).strip()
    if kind == "go":
        return "exact", s
    if kind == "npm" or kind == "poetry":
        if re.match(r"^(@|npm:|file:|link:|git\+|git:|github:|https?:|workspace:|portal:|patch:)", s) or "/" in s:
            return "other", None
        if s in ("", "*", "latest", "x", "X"):
            return "loose", None
        if s.startswith("=") and is_exact_version(s[1:]):
            return "exact", s.lstrip("=").lstrip("v")
        if is_exact_version(s):
            return "exact", s.lstrip("v")
        return "loose", None
    # pip / PEP 508
    if s.startswith("@") or "://" in s:
        return "other", None
    if s == "":
        return "loose", None
    clauses = [c.strip() for c in s.split(",") if c.strip()]
    if len(clauses) == 1:
        m = re.match(r"^===?\s*([\w.+!-]+)$", clauses[0])
        if m and not m.group(1).endswith(".*"):
            return "exact", m.group(1)
    return "loose", None


def satisfies(kind, spec, locked):
    """True/False when a simple check applies (exact, ^, ~, x-range, == prefix), else None."""
    s = str(spec).strip()
    lk = nums(locked)
    if kind == "go":
        return s == locked
    if kind in ("npm", "poetry"):
        if " " in s or "||" in s:
            return None  # ranges: not evaluated
        if s.startswith("^"):
            a = nums(s[1:])
            if not a:
                return None
            a3 = a + [0] * (3 - len(a))
            if a3[0] != 0:
                return lk[:1] == a3[:1]
            if a3[1] != 0 and len(a) >= 2:
                return lk[:2] == a3[:2]
            return lk[:len(a)] == a
        if s.startswith("~"):
            a = nums(s.lstrip("~>"))
            if not a:
                return None
            pre = a[:2] if len(a) >= 2 else a[:1]
            return lk[:len(pre)] == pre
        m = re.match(r"^v?(\d+(?:\.\d+)*)(?:\.[xX*])*$", s)
        if m and (s.endswith(("x", "X", "*")) or s.count(".") < 2):
            pre = nums(m.group(1))
            return lk[:len(pre)] == pre
        k, v = classify(kind, s)
        if k == "exact":
            return str(locked).lstrip("v") == v
        return None
    # pip
    res = None
    for c in [c.strip() for c in s.split(",") if c.strip()]:
        m = re.match(r"^(===?|~=)\s*([\w.+!*-]+)$", c)
        if not m:
            continue
        op, v = m.group(1), m.group(2)
        if op == "~=":
            pre = nums(v)[:-1] or nums(v)
            ok = lk[:len(pre)] == pre
        elif v.endswith(".*"):
            pre = nums(v[:-2])
            ok = lk[:len(pre)] == pre
        else:  # PEP 440: 1.0 == 1.0.0 (trailing zeros), else literal
            ok = str(locked) == v or (canon(locked) == canon(v) and nums(v) != [])
        res = ok if res is None else (res and ok)
    return res


def canon(v):
    n = nums(v)
    while n and n[-1] == 0:
        n.pop()
    return n


# ---- manifest parsers: list of {"name","spec","dev"} ----
def parse_req_line(line):
    """PEP 508 subset: name[extras] (spec) ; markers -> (name, spec) or None."""
    s = line.split(" #", 1)[0].strip()
    s = s.split(";", 1)[0].strip()
    m = re.match(r"^([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*(.*)$", s)
    if not m:
        return None
    spec = m.group(3).strip().strip("()").replace(" ", "")
    return norm_py(m.group(1)), spec


def parse_requirements(p, dev=False, depth=0):
    text = read_file(p)
    out = []
    if text is None:
        return out
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith(("-r ", "--requirement ", "-c ", "--constraint ")) and depth < 1:
            inc = os.path.normpath(os.path.join(os.path.dirname(p), line.split(None, 1)[1].strip()))
            if os.path.isfile(inc) and not os.path.isabs(rel(inc)):
                out.extend(parse_requirements(inc, dev, depth + 1))
            continue
        if line.startswith("-") or "://" in line or line.startswith("."):
            unparsed.append("%s: %s" % (rel(p), clip(line, 120)))
            continue
        r = parse_req_line(line)
        if r and r[0]:
            out.append({"name": r[0], "spec": r[1], "dev": dev})
        else:
            unparsed.append("%s: %s" % (rel(p), clip(line, 120)))
    return out


def strip_comment(line):
    out, q = [], None
    for ch in line:
        if q:
            out.append(ch)
            if ch == q:
                q = None
        elif ch in "\"'":
            q = ch
            out.append(ch)
        elif ch == "#":
            break
        else:
            out.append(ch)
    return "".join(out)


def toml_tables(text):
    """Minimal TOML: {table: [(key, raw value)]}; multi-line arrays/inline tables joined."""
    tables, cur = {}, ""
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = strip_comment(lines[i]).strip()
        i += 1
        if not line:
            continue
        m = re.match(r"^\[\[?\s*([^\]]+?)\s*\]\]?$", line)
        if m:
            cur = m.group(1).replace('"', "")
            continue
        m = re.match(r"^(\"[^\"]+\"|'[^']+'|[\w.-]+)\s*=\s*(.*)$", line)
        if not m:
            continue
        key, val = m.group(1).strip("\"'"), m.group(2).strip()
        n = 0
        while (val.count("[") - val.count("]") > 0 or val.count("{") - val.count("}") > 0) and i < len(lines) and n < 400:
            val += " " + strip_comment(lines[i]).strip()
            i += 1
            n += 1
        tables.setdefault(cur, []).append((key, val))
    return tables


def str_array(val):
    return [a or b for a, b in re.findall(r"\"([^\"]*)\"|'([^']*)'", val)]


def parse_pyproject(p):
    text = read_file(p)
    out = []
    if text is None:
        return out
    t = toml_tables(text)
    for key, val in t.get("project", []):
        if key == "dependencies":
            for s in str_array(val):
                r = parse_req_line(s)
                if r and r[0]:
                    out.append({"name": r[0], "spec": r[1], "dev": False})
    for group, val in t.get("project.optional-dependencies", []):
        for s in str_array(val):
            r = parse_req_line(s)
            if r and r[0]:
                out.append({"name": r[0], "spec": r[1], "dev": group.lower() in DEVISH})
    for group, val in t.get("dependency-groups", []):
        for s in str_array(val):
            r = parse_req_line(s)
            if r and r[0]:
                out.append({"name": r[0], "spec": r[1], "dev": True})
    for table, rows in sorted(t.items()):
        if not table.startswith("tool.poetry"):
            continue
        if table == "tool.poetry.dependencies":
            dev = False
        elif table == "tool.poetry.dev-dependencies" or re.match(r"^tool\.poetry\.group\.[^.]+\.dependencies$", table):
            dev = True
        else:
            continue
        for name, val in rows:
            if name.lower() == "python":
                continue
            v = val.strip()
            if v.startswith("{") or v.startswith("["):
                m = re.search(r"\bversion\s*=\s*\"([^\"]*)\"", v)
                spec = m.group(1) if m else ("@" + ("git" if "git" in v else "path"))
            else:
                spec = v.strip("\"'")
            out.append({"name": norm_py(name), "spec": spec, "dev": dev, "kind": "poetry"})
    return out


def parse_package_json(p):
    text = read_file(p)
    out = []
    if text is None:
        return out
    try:
        d = json.loads(text)
    except ValueError as e:
        warnings.append("%s: invalid JSON (%s)" % (rel(p), e))
        lower_conf(0.5)
        return out
    if not isinstance(d, dict):
        return out
    for sect, dev in (("dependencies", False), ("optionalDependencies", False), ("devDependencies", True)):
        block = d.get(sect)
        if isinstance(block, dict):
            for name in sorted(block):
                out.append({"name": name, "spec": str(block[name]), "dev": dev})
    return out


def parse_go_mod(p):
    text = read_file(p)
    out, indirect = [], []
    if text is None:
        return out, indirect
    in_block = False
    for raw in text.splitlines():
        line = raw.split("//", 1)[0].strip()
        ind = "// indirect" in raw
        if in_block:
            if line.startswith(")"):
                in_block = False
                continue
            parts = line.split()
            if len(parts) >= 2:
                out.append({"name": parts[0], "spec": parts[1], "dev": False})
                if ind:
                    indirect.append(parts[0])
            continue
        if re.match(r"^require\s*\($", line):
            in_block = True
            continue
        m = re.match(r"^require\s+(\S+)\s+(\S+)$", line)
        if m:
            out.append({"name": m.group(1), "spec": m.group(2), "dev": False})
            if ind:
                indirect.append(m.group(1))
    return out, indirect


# ---- lock parsers: {name: version} (go: {module: set(versions)}) ----
def lock_package_lock(p):
    text = read_file(p)
    if text is None:
        return None
    try:
        d = json.loads(text)
    except ValueError as e:
        warnings.append("%s: invalid JSON (%s)" % (rel(p), e))
        return None
    out = {}
    pk = d.get("packages") if isinstance(d, dict) else None
    if isinstance(pk, dict):
        for k in sorted(pk):
            if k.startswith("node_modules/") and k.count("node_modules/") == 1:
                v = pk[k].get("version") if isinstance(pk[k], dict) else None
                out[k[len("node_modules/"):]] = str(v) if v else "link"
        return out
    deps = d.get("dependencies") if isinstance(d, dict) else None
    if isinstance(deps, dict):
        for k in sorted(deps):
            v = deps[k].get("version") if isinstance(deps[k], dict) else None
            out[k] = str(v) if v else "link"
    return out


def lock_yarn(p):
    text = read_file(p)
    if text is None:
        return None
    out, names = {}, []
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        if not raw[0].isspace() and raw.rstrip().endswith(":"):
            names = []
            for ent in raw.rstrip()[:-1].split(","):
                ent = ent.strip().strip("\"'")
                ent = re.sub(r"@npm:", "@", ent)
                if "@" in ent[1:]:
                    names.append(ent[: ent.rindex("@")])
            continue
        m = re.match(r"^\s+version:?\s+\"?([^\"\s]+)\"?", raw)
        if m and names:
            for n in names:
                out.setdefault(n, m.group(1))
            names = []
    return out


def lock_pnpm(p, importer):
    """Best-effort: importers block (v6/v9: name -> version:, v5: name: version)."""
    text = read_file(p)
    if text is None:
        return None
    out, cur_imp, cur_sec, cur_name, in_importers = {}, None, None, None, False
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        s = raw.strip()
        if indent == 0:
            in_importers = s == "importers:"
            cur_imp = "." if (not in_importers and s in ("dependencies:", "devDependencies:", "optionalDependencies:")) else None
            cur_sec = s[:-1] if cur_imp else None
            continue
        if in_importers:
            if indent == 2:
                cur_imp = s.rstrip(":").strip("\"'")
                cur_sec = None
                continue
            if indent == 4:
                cur_sec = s.rstrip(":")
                continue
            if cur_imp != importer or cur_sec not in ("dependencies", "devDependencies", "optionalDependencies"):
                continue
            if indent == 6:
                m = re.match(r"^(\"?[^\s:\"]+\"?):\s*(\S*)$", s)
                if m:
                    cur_name = m.group(1).strip("\"'")
                    if m.group(2):
                        out[cur_name] = re.split(r"[(_]", m.group(2).strip("\"'"))[0]
                continue
            if indent == 8 and cur_name:
                m = re.match(r"^version:\s*(\S+)$", s)
                if m:
                    out[cur_name] = re.split(r"[(_]", m.group(1).strip("\"'"))[0]
        elif cur_imp == "." and importer == "." and indent == 2 and cur_sec:
            m = re.match(r"^(\"?[^\s:\"]+\"?):\s*(\S+)$", s)
            if m:
                out[m.group(1).strip("\"'")] = re.split(r"[(_]", m.group(2).strip("\"'"))[0]
    warnings.append("%s parsed best-effort (importer %s)" % (rel(p), importer))
    lower_conf(0.7)
    return out


def lock_toml_packages(p):
    """poetry.lock / uv.lock / pdm.lock: [[package]] name/version."""
    text = read_file(p)
    if text is None:
        return None
    out, in_pkg, name, ver = {}, False, None, None
    for raw in text.splitlines():
        line = strip_comment(raw).strip()
        if line.startswith("["):
            if in_pkg and name and ver:
                out.setdefault(norm_py(name), ver)
            in_pkg = line == "[[package]]"
            name = ver = None
            continue
        if not in_pkg:
            continue
        m = re.match(r"^(name|version)\s*=\s*\"([^\"]*)\"", line)
        if m:
            if m.group(1) == "name":
                name = m.group(2)
            else:
                ver = m.group(2)
    if in_pkg and name and ver:
        out.setdefault(norm_py(name), ver)
    return out


def lock_requirements(p):
    out = {}
    for d in parse_requirements(p):
        k, v = classify("pip", d["spec"])
        if k == "exact":
            out.setdefault(d["name"], v)
    return out


def lock_go_sum(p):
    text = read_file(p)
    if text is None:
        return None
    out = {}
    for raw in text.splitlines():
        parts = raw.split()
        if len(parts) >= 2:
            out.setdefault(parts[0], set()).add(parts[1][:-7] if parts[1].endswith("/go.mod") else parts[1])
    return out


def fully_pinned(p):
    saved = len(unparsed)
    deps = parse_requirements(p)
    del unparsed[saved:]
    return bool(deps) and all(classify("pip", d["spec"])[0] == "exact" for d in deps)


# ---- discovery ----
def find_up(start, names):
    d = start
    while True:
        for n in names:
            p = os.path.join(d, n)
            if os.path.isfile(p):
                return p
        if d == top or os.path.dirname(d) == d or not os.path.isdir(top) or not d.startswith(top):
            return None
        d = os.path.dirname(d)


manifests = []
if os.path.isfile(top):
    manifests = [top] if MANIFEST_RE.match(os.path.basename(top)) else []
    if not manifests:
        fail("not a supported manifest: %s" % rel(top))
else:
    for d, dirs, files in os.walk(top):
        depth = os.path.relpath(d, top).count(os.sep) + (0 if d == top else 1)
        dirs[:] = sorted(x for x in dirs if x not in SKIP and not x.startswith(".")) if depth < MAX_DEPTH else []
        for f in sorted(files):
            if MANIFEST_RE.match(f) and "lock" not in f.lower():
                manifests.append(os.path.join(d, f))
        if len(manifests) >= MAX_MANIFESTS or time.time() - T0 > BUDGET_S:
            break
manifests = sorted(manifests)
truncated = len(manifests) >= MAX_MANIFESTS

NPM_LOCKS = ["package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml"]
PY_LOCKS = ["poetry.lock", "uv.lock", "pdm.lock", "requirements.lock", "requirements-lock.txt"]
REQ_LOCKS = ["requirements.lock", "requirements-lock.txt"]

man_out, all_deps = [], []   # all_deps: (manifest rel, kind, dep, lock dict or None, self_lock)
used_as_lock = set()
indirect_all = []
for mp in manifests:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached")
        break
    name, d = os.path.basename(mp), os.path.dirname(mp)
    lock, lock_path, kind, deps, self_lock = None, None, None, [], False
    if name == "package.json":
        kind = "npm"
        deps = parse_package_json(mp)
        lock_path = find_up(d, NPM_LOCKS)
        if lock_path:
            ln = os.path.basename(lock_path)
            if ln in ("package-lock.json", "npm-shrinkwrap.json"):
                lock = lock_package_lock(lock_path)
            elif ln == "yarn.lock":
                lock = lock_yarn(lock_path)
            else:
                imp = os.path.relpath(d, os.path.dirname(lock_path)).replace(os.sep, "/")
                lock = lock_pnpm(lock_path, imp)
    elif name == "pyproject.toml":
        kind = "pip"
        deps = parse_pyproject(mp)
        lock_path = find_up(d, PY_LOCKS)
        if not lock_path and os.path.isfile(os.path.join(d, "requirements.txt")) and fully_pinned(os.path.join(d, "requirements.txt")):
            lock_path = os.path.join(d, "requirements.txt")
        if lock_path:
            used_as_lock.add(lock_path)
            if os.path.basename(lock_path).startswith("requirements"):
                lock = lock_requirements(lock_path)
            else:
                lock = lock_toml_packages(lock_path)
    elif name == "go.mod":
        kind = "go"
        deps, ind = parse_go_mod(mp)
        indirect_all.extend(ind)
        lock_path = os.path.join(d, "go.sum") if os.path.isfile(os.path.join(d, "go.sum")) else None
        if lock_path:
            lock = lock_go_sum(lock_path)
    else:
        if mp in used_as_lock:
            continue
        kind = "pip"
        deps = parse_requirements(mp, dev=bool(re.search(r"dev|test", name, re.I)))
        lock_path = find_up(d, REQ_LOCKS)
        if lock_path:
            lock = lock_requirements(lock_path)
        else:
            self_lock = True  # == pins in the file itself count as locked
    if not deps and kind == "pip" and name == "pyproject.toml":
        continue  # pyproject without dependency tables (build config only)
    if lock_path is None and not self_lock:
        warnings.append("%s: no lockfile found" % rel(mp))
        lower_conf(0.7)
    man_out.append({"file": rel(mp), "kind": kind, "lock": rel(lock_path) if lock_path else None, "deps": len(deps)})
    for dep in deps:
        all_deps.append((rel(mp), dep.get("kind", kind), dep, lock, self_lock))

# ---- compare ----
locked_n = 0
unlocked, loose, mismatched = set(), [], []
dev_names, nondev_names = set(), set()
range_warned = False
for mf, kind, dep, lock, self_lock in all_deps:
    name, spec = dep["name"], dep["spec"]
    (dev_names if dep["dev"] else nondev_names).add(name)
    cls, exact_v = classify(kind, spec)
    if cls == "loose":
        loose.append({"name": name, "spec": spec, "file": mf})
    elif cls == "other":
        unparsed.append("%s: %s %s" % (mf, name, clip(spec, 80)))
    locked_v = None
    if lock is not None and name in lock:
        locked_v = lock[name]
    elif self_lock and cls == "exact":
        locked_v = exact_v
    if locked_v is None:
        unlocked.add(name)
        continue
    locked_n += 1
    if kind == "go":
        if spec not in locked_v:
            mismatched.append({"name": name, "spec": spec, "locked": ",".join(sorted(locked_v)), "file": mf})
        continue
    if locked_v == "link":
        continue
    ok = satisfies(kind, spec, locked_v)
    if ok is False:
        mismatched.append({"name": name, "spec": spec, "locked": locked_v, "file": mf})
    elif ok is None and cls == "loose" and not range_warned:
        warnings.append("range specs (>=, <, ||, -) are not evaluated against the lock: only exact, ^, ~, x and == prefixes")
        range_warned = True

unlocked_l = sorted(unlocked)
loose.sort(key=lambda x: (x["name"], x["file"]))
mismatched.sort(key=lambda x: (x["name"], x["file"]))
dev_only = sorted(dev_names - nondev_names)
indirect_all = sorted(set(indirect_all))
for lst in (unlocked_l, loose, mismatched, dev_only, indirect_all):
    if len(lst) > limit:
        del lst[limit:]
        truncated = True
if not man_out:
    warnings.append("no manifest found under %s" % rel(top))
    lower_conf(0.5)
n_deps = len(all_deps)
emit(data={"manifests": man_out, "deps": n_deps, "locked": locked_n, "unlocked": unlocked_l, "loose": loose,
           "mismatched": mismatched, "dev_only": dev_only, "indirect": indirect_all, "truncated": truncated,
           "ms": int((time.time() - T0) * 1000),
           "text": "%d deps, %d unlocked, %d loose, %d mismatched" % (n_deps, len(unlocked_l), len(loose), len(mismatched))},
     confidence=confidence, unparsed=unparsed, warnings=warnings)
