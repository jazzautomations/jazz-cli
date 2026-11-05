# Python 3.8+ standard library; bounded, read-only text processing.
import time
START = time.monotonic()
truncated = False
MAX_BYTES = 262144

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root")
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
    value = json.loads(src, parse_constant=invalid_constant, parse_float=finite_float)
except (ValueError, RecursionError):
    fail("invalid or too deeply nested JSON", data={"truncated": truncated})
if truncated:
    fail("JSON exceeds input budget", data={"truncated": True})
nodes = 0
def infer(v, depth=0):
    global nodes, truncated
    nodes += 1
    if depth > 30 or nodes > 10000 or time.monotonic() - START > 3.5:
        truncated = True
        return {}
    if v is None: return {"type": "null"}
    if isinstance(v, bool): return {"type": "boolean"}
    if isinstance(v, int): return {"type": "integer"}
    if isinstance(v, float): return {"type": "number"}
    if isinstance(v, str): return {"type": "string"}
    if isinstance(v, dict):
        return {"type": "object", "properties": {k: infer(v[k], depth+1) for k in sorted(v)}, "required": sorted(v)}
    variants = {json.dumps(infer(x, depth+1), sort_keys=True) for x in v[:1000]}
    if len(v) > 1000: truncated = True
    schemas = [json.loads(x) for x in sorted(variants)]
    return {"type": "array", "items": schemas[0] if len(schemas) == 1 else ({"anyOf": schemas} if schemas else {})}
schema = infer(value)
finish({"schema": schema}, "Inferred %s schema from observed values" % schema.get("type", "unknown"))
