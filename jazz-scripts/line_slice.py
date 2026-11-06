# line_slice: first N, last N or a 1-based inclusive line range of a file or
# inline text — "tail the last 50 lines", "print lines 10000 to 10010". Reads
# at most 8 MB, caps output at 500 lines x 200 chars, never writes.
# Python 3.8+ stdlib only.

MAX_OUT = 500
MAX_LINE = 200

start = arg("start")
end = arg("end")
tail = arg("tail")

if tail is not None and (start is not None or end is not None):
    fail("pass either tail or start/end, not both")
if tail is None and start is None and end is None:
    fail("pass tail, start/end or start alone")

try:
    tail = int(tail) if tail is not None else None
    start = int(start) if start is not None else None
    end = int(end) if end is not None else None
except (TypeError, ValueError):
    fail("tail, start and end must be integers")

if tail is not None and not (1 <= tail <= 100000):
    fail("tail must be 1..100000")
if start is not None and start < 1:
    fail("start must be >= 1")
if end is not None and end < 1:
    fail("end must be >= 1")
if start is not None and end is not None and end < start:
    fail("end must be >= start")

def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
    return p


if "cmd" in ARGS:
    fail("cmd input is not supported")
if arg("file") is not None:
    bounded_path(arg("file"))

text, src, _ = get_input()
lines = text.splitlines()
total = len(lines)

if tail is not None:
    lo = max(1, total - tail + 1)
    hi = total
    label = "last %d" % tail
else:
    lo = start or 1
    hi = end if end is not None else lo
    if lo > total:
        emit(True, {"lines": [], "shown": 0, "total_lines": total,
                    "start": lo, "end": hi, "truncated": False,
                    "text": "%s has %d line(s); start %d is past the end" % (src, total, lo)},
             1.0, [], [])
    hi = min(hi, total)
    label = "lines %d-%d" % (lo, hi)

out = []
truncated = False
n = 0
for ln in range(lo, hi + 1):
    if n >= MAX_OUT:
        truncated = True
        break
    n += 1
    line = lines[ln - 1]
    out.append({"line": ln, "text": clip(line, MAX_LINE)})

shown = len(out)
text_out = "%s of %s: %d line(s) shown of %d total" % (label, src, shown, total)
if truncated:
    text_out += " [truncated]"

emit(True, {"lines": out, "shown": shown, "total_lines": total,
            "start": lo, "end": min(hi, lo + shown - 1) if shown else hi,
            "truncated": truncated, "text": text_out},
     1.0 if not truncated else 0.8)
