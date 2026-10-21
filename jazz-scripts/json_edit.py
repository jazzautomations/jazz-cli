# json_edit: get/set/del one value in a JSON file by path, keeping key order and indentation.
from collections import OrderedDict

p = path_in_root(arg("file", ""))
op = arg("op", "get")
path = str(arg("path", ""))


def parse_path(s):
    if s.startswith("/"):  # JSON pointer
        return [x.replace("~1", "/").replace("~0", "~") for x in s.split("/")[1:]]
    out = []
    for m in re.finditer(r"\[(\d+|-)\]|\[\"((?:[^\"\\]|\\.)*)\"\]|([^.\[\]]+)", s):
        if m.group(1) is not None:
            out.append(int(m.group(1)) if m.group(1) != "-" else "-")
        elif m.group(2) is not None:
            out.append(m.group(2))
        else:
            out.append(m.group(3))
    return out


def step(node, k, create=False):
    if isinstance(node, list):
        try:
            i = int(k)
        except (TypeError, ValueError):
            raise KeyError("index %r into a list" % (k,))
        if i < 0 or i >= len(node):
            raise KeyError("index %d out of range (len %d)" % (i, len(node)))
        return node[i]
    if isinstance(node, dict):
        if str(k) not in node:
            if create:
                node[str(k)] = OrderedDict()
            else:
                raise KeyError("key %r not found" % (k,))
        return node[str(k)]
    raise KeyError("cannot index into %s" % type(node).__name__)


try:
    if os.path.getsize(p) > (8 << 20):
        fail("file over 8 MB")
    raw = read_text(p)
except (IOError, OSError) as e:
    fail("cannot read file: %s" % e)
try:
    doc = json.loads(raw, object_pairs_hook=OrderedDict)
except ValueError as e:
    fail("invalid JSON: %s" % e)
keys = parse_path(path)
if op != "get" and not keys:
    fail("set/del need a non-empty path")
try:
    parent = doc
    for k in keys[:-1]:
        parent = step(parent, k, create=(op == "set"))
    last = keys[-1] if keys else None
    if op == "get":
        val = step(parent, last) if keys else doc
        emit(True, {"file": rel(p), "path": path, "value": val, "type": type(val).__name__})
    old = None
    if isinstance(parent, list):
        if last == "-" and op == "set":
            parent.append(None)
            last = len(parent) - 1
        i = int(last)
        if i < 0 or i >= len(parent):
            raise KeyError("index %d out of range (len %d)" % (i, len(parent)))
        old = parent[i]
        if op == "set":
            parent[i] = ARGS.get("value")
        else:
            del parent[i]
    elif isinstance(parent, dict):
        old = parent.get(str(last))
        if op == "set":
            parent[str(last)] = ARGS.get("value")
        else:
            if str(last) not in parent:
                raise KeyError("key %r not found" % (last,))
            del parent[str(last)]
    else:
        raise KeyError("cannot index into %s" % type(parent).__name__)
except (KeyError, ValueError) as e:
    fail(str(e).strip("'\""))

m = re.search(r"\n([ \t]+)\S", raw)
indent = m.group(1) if m else ("" if "\n" not in raw.strip() else "  ")
if "\t" in indent:
    indent = "\t"
text = json.dumps(doc, indent=indent if indent else None, ensure_ascii=False,
                  separators=(",", ": ") if indent else (", ", ": "))
if raw.endswith("\n"):
    text += "\n"
tmp = p + ".jazz-tmp"
with open(tmp, "w", encoding="utf-8") as f:
    f.write(text)
try:
    os.chmod(tmp, os.stat(p).st_mode & 0o7777)
except OSError:
    pass
os.replace(tmp, p)
emit(True, {"file": rel(p), "path": path, "op": op, "old": old, "new": ARGS.get("value") if op == "set" else None})
