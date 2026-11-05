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
import csv
import io
try:
    delimiter = arg("delimiter", ",")
    if not isinstance(delimiter, str) or len(delimiter) != 1:
        fail("delimiter must be one character")
    reader = csv.reader(io.StringIO(src), delimiter=delimiter, strict=True)
    headers = next(reader, [])
    if not headers or len(set(headers)) != len(headers) or any(not h for h in headers):
        fail("CSV requires unique nonempty headers")
    rows = []
    for values in reader:
        if len(values) != len(headers):
            fail("CSV row width differs from header")
        if len(rows) >= 1000 or time.monotonic() - START > 3.5:
            truncated = True
            break
        rows.append(dict(sorted(zip(headers, values))))
except (csv.Error, TypeError):
    fail("invalid CSV")
finish({"columns": sorted(headers), "rows": rows}, "%d CSV row(s), columns: %s" % (len(rows), ", ".join(sorted(headers))))
