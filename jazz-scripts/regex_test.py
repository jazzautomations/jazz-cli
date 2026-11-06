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

def pattern_arg():
    pattern = arg("pattern")
    if pattern is None:
        m = re.search(r'["\'](.*?)["\']', str(arg("query", "")))
        pattern = m.group(1) if m else None
    if not isinstance(pattern, str) or not pattern or len(pattern) > 512:
        fail("pass a pattern of 1..512 characters (or quote it in the request)")
    return pattern

src = source()
import signal
pattern = pattern_arg()
if not hasattr(signal, "setitimer"):
    fail("regex timeout unavailable on this platform")
try:
    rx = re.compile(pattern)
except re.error:
    fail("invalid regular expression")
class MatchTimeout(Exception):
    pass
def timeout(signum, frame):
    raise MatchTimeout()
signal.signal(signal.SIGALRM, timeout)
items = []
lines = src.splitlines()
source_truncated = truncated
for number, line in enumerate(lines, 1):
    if number > 1000 or time.monotonic() - START > 3.5:
        truncated = True
        break
    incomplete = source_truncated and number == len(lines) and not src.endswith(("\n", "\r"))
    if len(line) > 4096 or incomplete:
        truncated = True
        items.append({"line": number, "matched": None, "span": None,
                      "timed_out": False, "input_truncated": True})
        continue
    matched, span, timed_out = None, None, False
    try:
        signal.setitimer(signal.ITIMER_REAL, 0.025)
        m = rx.search(line)
        matched = bool(m)
        span = list(m.span()) if m else None
    except MatchTimeout:
        timed_out = True
        truncated = True
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
    items.append({"line": number, "matched": matched, "span": span, "timed_out": timed_out, "input_truncated": False})
finish({"items": items, "matches": sum(x["matched"] is True for x in items)},
       "%d matching line(s), %d timed out" % (sum(x["matched"] is True for x in items), sum(x["timed_out"] for x in items)))
