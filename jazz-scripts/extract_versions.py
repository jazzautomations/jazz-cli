# extract_versions: semver strings, URLs and emails with their line:col (1-based) positions.
text, source, code = get_input()
PATS = {
    # semver.org grammar, optional leading v, not glued to other digits/dots
    "semver": re.compile(r"(?<![\w.])v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
                         r"(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][\dA-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][\dA-Za-z-]*))*))?"
                         r"(?:\+([\dA-Za-z-]+(?:\.[\dA-Za-z-]+)*))?(?![\w.]*\d)"),
    "url": re.compile(r"\b(?:https?|ftp)://[^\s<>\"'`)\]]+"),
    "email": re.compile(r"(?<![\w.+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b"),
}
want = [k.strip() for k in str(arg("kinds", "semver,url,email")).split(",") if k.strip()]
bad = [k for k in want if k not in PATS]
if bad:
    fail("unknown kinds: %s (use semver, url, email)" % ", ".join(bad))
out = dict((k, []) for k in want)
counts = dict((k, 0) for k in want)
for no, line in enumerate(text.splitlines(), 1):
    for k in want:
        for m in PATS[k].finditer(line):
            v = m.group(0)
            if k == "url":
                v = v.rstrip(".,;:!?")
            counts[k] += 1
            if len(out[k]) < 60:
                out[k].append({"value": clip(v, 200), "line": no, "col": m.start() + 1})
uniq = dict((k, len(set(x["value"] for x in out[k]))) for k in want)
data = dict(out)
data["counts"] = counts
data["distinct"] = uniq
warn = ["%s: %d matches, showing 60" % (k, counts[k]) for k in want if counts[k] > 60]
emit(True, data, 1.0, [], warn)
