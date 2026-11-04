# config_get: read a key from a config file (.json/.yaml/.toml/.ini/.env) with no
# third-party dependency. Never executes anything; deterministic; stdlib only.
key = arg("key")  # None = whole document
fmt = (arg("format", "auto") or "auto").lower()
query = str(arg("query", "") or "")  # the request as typed: picks the file and the key from real data
warnings = []

# Common config file names, in preference order, for when no (valid) path is given.
CANDIDATES = ["config.yaml", "config.yml", "config.json", "config.toml", "settings.yaml", "settings.yml",
              "settings.json", "settings.toml", "app.yaml", "app.json", "pyproject.toml", "package.json",
              "Cargo.toml", "setup.cfg", "tox.ini", ".env"]
p_arg = str(arg("path", "") or "")
p = path_in_root(p_arg) if p_arg else None
if p is None or not os.path.isfile(p):
    root = path_in_root(".")
    qtoks = set(x.lower() for x in re.findall(r"[\w.\-]+", query))
    present = [c for c in CANDIDATES if os.path.isfile(os.path.join(root, c))]
    named = [c for c in present if c.lower() in qtoks]
    pick = named[0] if named else (present[0] if present else None)
    if pick is None:
        fail("config file not found: %s" % (p_arg or "no path given and no config.*/settings.*/pyproject/package.json here"))
    if p_arg:
        warnings.append("path %r not found; using %s" % (p_arg, pick))
    elif not named and len(present) > 1:
        warnings.append("several config files here; using %s (pass path)" % pick)
    p = os.path.join(root, pick)

try:
    size = os.path.getsize(p)
except (IOError, OSError) as e:
    fail("cannot read file: %s" % e)
if size > (2 << 20):
    fail("file over 2 MB")
try:
    raw = read_text(p)
except (IOError, OSError) as e:
    fail("cannot read file: %s" % e)

unparsed = []


def detect_format(path):
    base = os.path.basename(path).lower()
    ext = os.path.splitext(base)[1]
    if base == ".env" or base.startswith(".env."):
        return "env"
    return {
        ".json": "json",
        ".yaml": "yaml", ".yml": "yaml",
        ".toml": "toml",
        ".ini": "ini", ".cfg": "ini", ".conf": "ini",
        ".env": "env",
    }.get(ext, "")


if fmt == "auto":
    fmt = detect_format(p)
    if not fmt:
        fail("cannot determine format from %r; pass format" % rel(p))

# ---------------- JSON ----------------


def parse_json(text):
    try:
        return json.loads(text)
    except ValueError as e:
        fail("invalid JSON: %s" % e)


# ---------------- ENV ----------------


def parse_env(text):
    d = {}
    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith("#"):
            continue
        if s.startswith("export ") or s.startswith("export\t"):
            s = s[7:].lstrip()
        if "=" not in s:
            continue
        k, _, v = s.partition("=")
        k = k.strip()
        if not k:
            continue
        v = v.strip()
        if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
            v = v[1:-1]
        d[k] = v
    return d


# ---------------- INI / CFG ----------------


def parse_ini(text):
    import configparser
    cp = configparser.ConfigParser(interpolation=None, strict=False)
    cp.optionxform = str  # keep key case
    try:
        cp.read_string(text)
    except configparser.Error as e:
        fail("invalid INI: %s" % e)
    out = {}
    if cp.defaults():
        out.update({k: v for k, v in cp.defaults().items()})
    for sec in cp.sections():
        out[sec] = {k: v for k, v in cp.items(sec)}
    return out


# ---------------- TOML ----------------


def parse_toml(text):
    try:
        import tomllib
    except ImportError:
        return parse_toml_min(text)
    try:
        return tomllib.loads(text)
    except Exception as e:  # tomllib.TOMLDecodeError
        fail("invalid TOML: %s" % e)


def toml_scalar(v):
    v = v.strip()
    if not v:
        return ""
    if v[0] in "\"'":
        q = v[0]
        end = v.find(q, 1)
        if end != -1:
            s = v[1:end]
            return s if q == "'" else s.encode().decode("unicode_escape", "replace")
        return v.strip("\"'")
    if v.startswith("[") and v.endswith("]"):
        inner = v[1:-1].strip()
        if not inner:
            return []
        return [toml_scalar(x) for x in split_top(inner)]
    low = v.lower()
    if low == "true":
        return True
    if low == "false":
        return False
    vv = v.replace("_", "")
    if re.match(r"^[-+]?\d+$", vv):
        try:
            return int(vv, 0) if vv.lower().startswith(("0x", "0o", "0b", "+0x", "-0x")) else int(vv)
        except ValueError:
            return v
    if re.match(r"^[-+]?(\d+\.\d*|\.\d+|\d+(\.\d*)?[eE][-+]?\d+)$", vv):
        return float(vv)
    return v


def split_top(s):
    out, depth, buf, q = [], 0, [], None
    for ch in s:
        if q:
            buf.append(ch)
            if ch == q:
                q = None
            continue
        if ch in "\"'":
            q = ch
            buf.append(ch)
        elif ch in "[{":
            depth += 1
            buf.append(ch)
        elif ch in "]}":
            depth -= 1
            buf.append(ch)
        elif ch == "," and depth == 0:
            out.append("".join(buf).strip())
            buf = []
        else:
            buf.append(ch)
    if "".join(buf).strip():
        out.append("".join(buf).strip())
    return out


def toml_path(root, dotted):
    node = root
    for part in dotted:
        part = part.strip().strip("\"'")
        node = node.setdefault(part, {})
    return node


def parse_toml_min(text):
    warnings.append("toml parsed with the minimal built-in parser (tomllib unavailable)")
    root = {}
    cur = root
    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith("#"):
            continue
        if s.startswith("[["):
            name = s.strip("[]").strip()
            parent = toml_path(root, name.split(".")[:-1])
            leaf = name.split(".")[-1].strip().strip("\"'")
            arr = parent.setdefault(leaf, [])
            if not isinstance(arr, list):
                arr = parent[leaf] = [arr]
            cur = {}
            arr.append(cur)
            continue
        if s.startswith("["):
            name = s.strip("[]").strip()
            cur = toml_path(root, name.split("."))
            continue
        if "=" not in s:
            unparsed.append(line)
            continue
        k, _, v = s.partition("=")
        k = k.strip()
        v = v.split(" #", 1)[0].strip() if v.lstrip()[:1] not in "\"'[" else v.strip()
        if "." in k and k[0] not in "\"'":
            node = toml_path(cur, k.split(".")[:-1])
            node[k.split(".")[-1].strip()] = toml_scalar(v)
        else:
            cur[k.strip("\"'")] = toml_scalar(v)
    return root


# ---------------- YAML (safe subset; adapted from yaml_get) ----------------


def parse_yaml(text):
    toks = []  # [indent, text, lineno]
    docs = 0
    lines = text.splitlines()
    for no, l in enumerate(lines, 1):
        s = l.rstrip()
        if not s.strip():
            toks.append([None, "", no])
            continue
        if s.strip() in ("---", "...") or s.startswith("--- "):
            docs += 1
            if docs > 1 or any(t[0] is not None for t in toks):
                unparsed.append(l)
                warnings.append("multi-document YAML: only the first document is read")
                break
            continue
        if re.match(r"^\s*%", s):
            continue
        out, q = [], None
        for i, ch in enumerate(s):
            if q:
                if ch == q:
                    q = None
            elif ch in "\"'" and (i == 0 or s[i - 1] in " :[{,-"):
                q = ch
            elif ch == "#" and (i == 0 or s[i - 1] in " \t"):
                break
            out.append(ch)
        s = "".join(out).rstrip()
        if not s.strip():
            continue
        if re.search(r"(^|[\s:\-\[,])[&*][\w-]+|<<:|(^|\s)!!?\w", s):
            unparsed.append(l)
        toks.append([len(s) - len(s.lstrip(" ")), s.strip(), no])

    KEY = re.compile(r"^(\"(?:[^\"\\]|\\.)*\"|'(?:[^']|'')*'|[^\s\"'#\[\]{},][^#]*?)\s*:(?:\s+(.*)|$)")

    def scalar(v):
        v = v.strip()
        v = re.sub(r"^&[\w-]+\s*", "", v)
        if re.match(r"^![\w!]*\s", v):
            v = v.split(None, 1)[1]
        if v.startswith("*"):
            return None
        if v.startswith('"'):
            try:
                return json.loads(v)
            except ValueError:
                return v.strip('"')
        if v.startswith("'"):
            return v[1:-1].replace("''", "'") if v.endswith("'") else v[1:]
        if v.startswith("[") and v.endswith("]"):
            inner = v[1:-1].strip()
            return [scalar(x) for x in re.findall(r"\"(?:[^\"\\]|\\.)*\"|'(?:[^']|'')*'|[^,]+", inner) if x.strip()] if inner else []
        if v.startswith("{") and v.endswith("}"):
            d = {}
            for part in re.findall(r"(?:[^,\"']|\"(?:[^\"\\]|\\.)*\"|'(?:[^']|'')*')+", v[1:-1]):
                if ":" in part:
                    k, x = part.split(":", 1)
                    d[str(scalar(k))] = scalar(x)
            return d
        if v in ("~", "null", "Null", "NULL", ""):
            return None
        if v in ("true", "True", "TRUE"):
            return True
        if v in ("false", "False", "FALSE"):
            return False
        if re.match(r"^[-+]?\d+$", v):
            return int(v)
        if re.match(r"^[-+]?(\d+\.\d*|\.\d+|\d+(\.\d*)?[eE][-+]?\d+)$", v):
            return float(v)
        return v

    def nxt(i):
        while i < len(toks) and toks[i][0] is None:
            i += 1
        return i

    def block_scalar(i, indent, style):
        buf, j = [], i
        while j < len(toks) and (toks[j][0] is None or toks[j][0] > indent):
            buf.append(lines[toks[j][2] - 1])
            j += 1
        while buf and not buf[-1].strip():
            buf.pop()
        ind = min([len(b) - len(b.lstrip(" ")) for b in buf if b.strip()] or [0])
        body = [b[ind:] for b in buf]
        txt = "\n".join(body) if style.startswith("|") else " ".join(x.strip() for x in body if x.strip())
        if not style.endswith("-"):
            txt += "\n"
        return txt, j

    def parse_block(i, indent):
        i = nxt(i)
        if i >= len(toks) or toks[i][0] < indent:
            return None, i
        if toks[i][1] == "-" or toks[i][1].startswith("- "):
            return parse_list(i, toks[i][0])
        return parse_map(i, toks[i][0])

    def parse_value(rest, i, indent):
        if rest is None or rest == "":
            j = nxt(i + 1)
            if j < len(toks) and toks[j][0] is not None and toks[j][0] == indent and (toks[j][1] == "-" or toks[j][1].startswith("- ")):
                return parse_list(j, indent)
            if j < len(toks) and toks[j][0] > indent:
                return parse_block(j, toks[j][0])
            return None, i + 1
        if re.match(r"^[|>][-+]?\d*$", rest):
            return block_scalar(i + 1, indent, rest)
        return scalar(rest), i + 1

    def parse_map(i, indent):
        d = {}
        while True:
            i = nxt(i)
            if i >= len(toks) or toks[i][0] != indent or toks[i][1].startswith("- ") or toks[i][1] == "-":
                break
            m = KEY.match(toks[i][1])
            if not m:
                unparsed.append(lines[toks[i][2] - 1])
                i += 1
                continue
            k = m.group(1)
            if k[0] in "\"'":
                k = str(scalar(k))
            val, i = parse_value(m.group(2), i, indent)
            d[k] = val
        return d, i

    def parse_list(i, indent):
        out = []
        while True:
            i = nxt(i)
            if i >= len(toks) or toks[i][0] != indent or not (toks[i][1] == "-" or toks[i][1].startswith("- ")):
                break
            rest = toks[i][1][1:].strip()
            if not rest:
                j = nxt(i + 1)
                if j < len(toks) and toks[j][0] > indent:
                    v, i = parse_block(j, toks[j][0])
                else:
                    v, i = None, i + 1
                out.append(v)
                continue
            if KEY.match(rest) and rest[0] not in "[{":
                toks[i] = [indent + (len(toks[i][1]) - len(rest)), rest, toks[i][2]]
                v, i = parse_map(i, toks[i][0])
                out.append(v)
                continue
            out.append(scalar(rest))
            i += 1
        return out, i

    doc, _ = parse_block(0, 0)
    if unparsed:
        warnings.append("anchors/aliases/tags are not resolved; values that use them may be wrong")
    return doc


PARSERS = {"json": parse_json, "yaml": parse_yaml, "toml": parse_toml, "ini": parse_ini, "env": parse_env}
if fmt not in PARSERS:
    fail("unknown format %r (use json|yaml|toml|ini|env)" % fmt)
doc = PARSERS[fmt](raw)
if doc is None:
    doc = {}


def parse_key(s):
    out = []
    for m in re.finditer(r"\[(\d+)\]|\[\"([^\"]*)\"\]|([^.\[\]]+)", s):
        out.append(int(m.group(1)) if m.group(1) is not None else (m.group(2) if m.group(2) is not None else m.group(3)))
    return out


def navigate(node, parts):
    for k in parts:
        if isinstance(node, dict):
            if str(k) in node:
                node = node[str(k)]
            elif k in node:
                node = node[k]
            else:
                return None, False
        elif isinstance(node, list):
            try:
                i = int(k)
            except (TypeError, ValueError):
                return None, False
            if 0 <= i < len(node):
                node = node[i]
            else:
                return None, False
        else:
            return None, False
    return node, True


def flat_keys(node, prefix="", depth=0, out=None):
    """dotted paths of a parsed document (depth <= 5, <= 2000 paths)."""
    if out is None:
        out = []
    if depth > 5 or len(out) >= 2000:
        return out
    if isinstance(node, dict):
        for k, v in node.items():
            path = "%s.%s" % (prefix, k) if prefix else str(k)
            out.append(path)
            flat_keys(v, path, depth + 1, out)
    elif isinstance(node, list):
        for i, v in enumerate(node[:50]):
            path = "%s[%d]" % (prefix, i)
            out.append(path)
            flat_keys(v, path, depth + 1, out)
    return out


if (key is None or str(key) == "") and query:
    # the key is whichever real path of the file the request names: a full
    # dotted match (database.host) beats a last-segment match (host), and a
    # longer word beats a shorter one; nothing named = whole document
    best = None
    paths = flat_keys(doc)
    for tok in re.findall(r"[\w.\-]+", query):
        tl = tok.lower().strip(".")
        if len(tl) < 2:
            continue
        for fk in paths:
            fl = fk.lower()
            if fl == tl:
                cand = (2, len(tl), fk)
            elif fl.rsplit(".", 1)[-1] == tl or fl.rsplit("[", 1)[0].rsplit(".", 1)[-1] == tl:
                cand = (1, len(tl), fk)
            else:
                continue
            if best is None or cand > best:
                best = cand
    if best:
        key = best[2]

if key is None or str(key) == "":
    value, found, key_out = doc, True, None
else:
    key_out = str(key)
    value, found = navigate(doc, parse_key(key_out))

if isinstance(value, dict):
    level_keys = [str(k) for k in list(value.keys())[:200]]
elif isinstance(value, list):
    level_keys = [str(i) for i in range(min(len(value), 200))]
else:
    level_keys = []


def short(v):
    if isinstance(v, (dict, list)):
        return clip(json.dumps(v, ensure_ascii=False, separators=(",", ":")), 200)
    return clip("null" if v is None else str(v), 200)


if key_out is None:
    text = "%s document: %d top-level key(s)" % (fmt, len(level_keys))
elif found:
    text = "%s = %s" % (key_out, short(value))
else:
    text = "%s not found" % key_out

emit(
    ok=True,
    data={
        "format_used": fmt,
        "key": key_out,
        "value": value if found else None,
        "found": found,
        "keys": level_keys,
        "text": text,
    },
    confidence=0.6 if unparsed else 1.0,
    unparsed=unparsed,
    warnings=warnings,
)
