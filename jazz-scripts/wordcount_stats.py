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
words = re.findall(r"\b\w+\b", src.lower())
from collections import Counter
counts = Counter(words)
truncated |= len(counts) > 200
finish({"lines": len(src.splitlines()), "words": len(words), "characters": len(src),
        "frequencies": [{"word": w, "count": n} for w, n in sorted(counts.items())[:200]]},
       "%d words, %d lines, %d characters" % (len(words), len(src.splitlines()), len(src)))
