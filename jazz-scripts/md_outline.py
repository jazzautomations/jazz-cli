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
from urllib.parse import unquote
headings, links, anchors = [], [], set()
counts = {}
fence = None
lines = src.splitlines()
for number, line in enumerate(lines, 1):
    if time.monotonic() - START > 3.5:
        truncated = True
        break
    if len(line) > 4096:
        line = line[:4096]
        truncated = True
    m = re.match(r"^\s{0,3}(`{3,}|~{3,})", line)
    if m:
        marker = m.group(1)
        if fence is None:
            fence = marker
        elif marker[0] == fence[0] and len(marker) >= len(fence):
            fence = None
        continue
    if fence:
        continue
    m = re.match(r"^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$", line)
    level = len(m.group(1)) if m else None
    title = m.group(2) if m else None
    if not m and line.strip() and number < len(lines):
        underline = re.fullmatch(r" {0,3}(=+|-+)\s*", lines[number])
        if underline:
            title = line.strip()
            level = 1 if underline.group(1)[0] == "=" else 2
    if title is not None:
        slug = re.sub(r"[^\w\- ]", "", title.lower()).replace(" ", "-")
        n = counts.get(slug, 0)
        counts[slug] = n+1
        anchor = slug + ("-%d" % n if n else "")
        anchors.add(anchor)
        headings.append({"line": number, "level": level, "title": title, "anchor": anchor})
    for m in re.finditer(r"\[([^]\n]*)\]\(([^)\s]+)\)", line):
        links.append({"line": number, "label": m.group(1), "target": m.group(2)})
broken = [x for x in links if x["target"].startswith("#") and unquote(x["target"][1:]) not in anchors]
truncated |= max(len(headings), len(links), len(broken)) > 1000
finish({"headings": headings[:1000], "links": links[:1000], "broken_anchors": broken[:1000]},
       "%d headings, %d links, %d broken anchors" % (len(headings), len(links), len(broken)))
