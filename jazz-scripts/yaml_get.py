# yaml_get: read one value from a YAML file (simple subset: maps, lists, scalars, block scalars, flow lists).
# Anchors, aliases, tags and multi-document files are reported in unparsed[] instead of guessed.
p = path_in_root(arg("file", ""))
path = str(arg("path", ""))
try:
    raw = read_text(p)
except (IOError, OSError) as e:
    fail("cannot read file: %s" % e)

unparsed, warnings = [], []
toks = []  # [indent, text, lineno]
docs = 0
lines = raw.splitlines()
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
        continue  # directives
    # strip comments that are not inside quotes
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
    text = "\n".join(body) if style.startswith("|") else " ".join(x.strip() for x in body if x.strip())
    if not style.endswith("-"):
        text += "\n"
    return text, j


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
    order = []
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
        if k not in d:
            order.append(k)
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


def parse_path(s):
    out = []
    for m in re.finditer(r"\[(\d+)\]|\[\"([^\"]*)\"\]|([^.\[\]]+)", s):
        out.append(int(m.group(1)) if m.group(1) is not None else (m.group(2) if m.group(2) is not None else m.group(3)))
    return out


node = doc
for k in parse_path(path):
    if isinstance(node, list) and isinstance(k, int) and 0 <= k < len(node):
        node = node[k]
    elif isinstance(node, list) and isinstance(k, str) and k.isdigit() and int(k) < len(node):
        node = node[int(k)]
    elif isinstance(node, dict) and str(k) in node:
        node = node[str(k)]
    else:
        fail("path not found: %s (at %r)" % (path, k), unparsed=unparsed, warnings=warnings)
if unparsed and not warnings:
    warnings.append("anchors/aliases/tags are not resolved; values that use them may be wrong")
emit(True, {"file": rel(p), "path": path, "value": node, "type": type(node).__name__ if node is not None else "null"},
     0.6 if unparsed else 1.0, unparsed, warnings)
