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
from urllib.parse import urlsplit, parse_qsl
items = []
for value in sorted(set(src.splitlines()))[:1000]:
    if not value.strip():
        continue
    try:
        u = urlsplit(value.strip())
        port = u.port
        items.append({"scheme": u.scheme or None, "host": u.hostname, "port": port,
                      "path": u.path, "query": sorted(parse_qsl(u.query, keep_blank_values=True)),
                      "fragment": u.fragment or None})
    except ValueError:
        fail("invalid URL")
truncated |= len(src.splitlines()) > 1000
finish({"items": items}, "%d URL(s) parsed: %s" % (len(items), ", ".join(x["path"] for x in items[:5])))
