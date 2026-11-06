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

src = source()

def invalid_constant(value):
    raise ValueError("non-finite JSON number")

def finite_float(value):
    import math
    number = float(value)
    if not math.isfinite(number):
        raise ValueError("non-finite JSON number")
    return number

try:
    rows = json.loads(src, parse_constant=invalid_constant, parse_float=finite_float)
except (ValueError, RecursionError):
    fail("invalid JSON")
if truncated: fail("JSON exceeds input budget", data={"truncated": True})
if not isinstance(rows, list) or any(not isinstance(r, dict) for r in rows):
    fail("table requires an array of objects")
truncated |= len(rows) > 200
rows = rows[:200]
columns = sorted({k for r in rows for k in r})
truncated |= len(columns) > 30
columns = columns[:30]
def cell(v):
    return clip(json.dumps(v, ensure_ascii=False, sort_keys=True) if not isinstance(v, str) else v.replace("\n", " ").replace("\r", " ").replace("\t", " "), 80)
values = [[cell(r.get(k)) for k in columns] for r in rows]
widths = [max([len(k)] + [len(r[i]) for r in values]) for i,k in enumerate(columns)]
lines = [" | ".join(k.ljust(widths[i]) for i,k in enumerate(columns))]
lines += [" | ".join(v.ljust(widths[i]) for i,v in enumerate(r)) for r in values]
table = "\n".join(lines)
finish({"columns": columns, "rows": len(rows), "table": table}, clip(table, 1500) if table else "0 rows")
