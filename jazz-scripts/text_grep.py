# Python 3.8+ standard library; bounded, read-only text processing.
import time
START = time.monotonic()
truncated = False
MAX_BYTES = 262144

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
    return p

def source():
    global truncated
    if "text" in ARGS:
        value = ARGS["text"]
        if not isinstance(value, str):
            fail("text must be a string")
        truncated = len(value.encode("utf-8")) > MAX_BYTES
        return value.encode("utf-8")[:MAX_BYTES].decode("utf-8", "replace")
    if not arg("file") and arg("path"):
        ARGS["file"] = arg("path")  # for_each injeta path por item
    if not arg("file"):
        fail("pass file or text")
    try:
        p = bounded_path(arg("file"))
        if not os.path.isfile(p):
            fail("input must be a regular file")
        with open(p, "rb") as f:
            raw = f.read(MAX_BYTES + 1)
        truncated = len(raw) > MAX_BYTES
        return raw[:MAX_BYTES].decode("utf-8", "replace")
    except OSError:
        fail("cannot read file")

def finish(data, text):
    data.update(text=clip(text, 1000), truncated=truncated)
    emit(True, data)

def pattern_arg():
    pattern = arg("pattern")
    if pattern is None:
        m = re.search(r'["\'](.*?)["\']', str(arg("query", "")))
        pattern = m.group(1) if m else None
    if not isinstance(pattern, str) or not pattern or len(pattern) > 512:
        fail("pass a pattern of 1..512 characters (or quote it in the request)")
    return pattern

src = source()
pattern = pattern_arg()
try:
    context = int(arg("context", 2))
    limit = int(arg("limit", 200))
    if not 0 <= context <= 10 or not 1 <= limit <= 1000: raise ValueError()
except (ValueError, TypeError):
    fail("invalid context or limit")
lines = src.splitlines()
items = []
for i, line in enumerate(lines):
    if time.monotonic() - START > 3.5:
        truncated = True
        break
    if pattern in line:
        if len(items) >= limit:
            truncated = True
            break
        items.append({"line": i+1, "match": clip(line, 500), "before": [clip(s,500) for s in lines[max(0,i-context):i]], "after": [clip(s,500) for s in lines[i+1:i+1+context]]})
finish({"items": items, "matches": len(items)}, "%d literal match(es): %s" % (len(items), "; ".join("%d: %s" % (x["line"], x["match"]) for x in items[:5])))
