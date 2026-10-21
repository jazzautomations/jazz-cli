# extract_stacktrace: the last stack trace in a log (Python, Go, JS/TS, Java) with project frames marked.
text, source, code = get_input()
lines = text.splitlines()

PY_START = re.compile(r"^\s*Traceback \(most recent call last\):")
PY_FRAME = re.compile(r'^\s*File "([^"]+)", line (\d+)(?:, in (.+))?$')
PY_EXC = re.compile(r"^([A-Za-z_][\w.]*)(?:: (.*))?$")
GO_PANIC = re.compile(r"^(panic|fatal error): (.*)$")
GO_GR = re.compile(r"^goroutine \d+ \[[^\]]+\]:$")
GO_FUNC = re.compile(r"^([\w./*()%-]+(?:\.[\w*()%-]+)+)\((.*)\)$")
GO_LOC = re.compile(r"^\t(\S+\.go):(\d+)(?: \+0x[0-9a-f]+)?$")
JS_EXC = re.compile(r"^(?:Uncaught )?([A-Z]\w*(?:Error|Exception)|Error)(?: \[\w+\])?: (.*)$")
JS_AT = re.compile(r"^\s+at (?:(?:async )?(.+?) \()?((?:file://)?[^\s()]+?):(\d+):(\d+)\)?$")
JAVA_EXC = re.compile(r'^(?:Exception in thread "[^"]*" |Caused by: )?([a-z][\w$]*(?:\.[\w$]+)+(?:Exception|Error))(?:: (.*))?$')
JAVA_AT = re.compile(r"^\s+at ([\w$.<>]+)\(([^:)]+)(?::(\d+))?\)$")
JAVA_MORE = re.compile(r"^\s+\.\.\. \d+ more$")


def in_project(path):
    p = path[7:] if path.startswith("file://") else path
    if not p or p.startswith("<") or "site-packages" in p or "node_modules" in p or "/lib/python" in p \
            or "/usr/lib/" in p or "/go/pkg/mod/" in p or "/libexec/src/" in p or p.startswith("node:") \
            or "/internal/" in p and p.startswith("/usr/"):
        return False
    full = path_in_root(p)
    return os.path.abspath(full).startswith(os.path.abspath(ROOT) + os.sep) and os.path.exists(full)


traces = []  # (lang, start, end, exception, message, frames)
i, n = 0, len(lines)
while i < n:
    l = lines[i]
    if PY_START.match(l):
        start, frames, j = i, [], i + 1
        while j < n:
            m = PY_FRAME.match(lines[j])
            if m:
                frames.append({"file": m.group(1), "line": int(m.group(2)), "func": m.group(3) or ""})
                j += 1
                if j < n and lines[j].startswith("    ") and not PY_FRAME.match(lines[j]):
                    j += 1  # source line
                while j < n and re.match(r"^\s+[\^~ ]+$", lines[j]):
                    j += 1  # 3.11+ caret markers
                continue
            break
        exc, msg = "", ""
        if j < n:
            m = PY_EXC.match(lines[j].strip())
            if m:
                exc, msg = m.group(1), m.group(2) or ""
                j += 1
        traces.append(("python", start, j, exc, msg, frames))
        i = j
        continue
    m = GO_PANIC.match(l)
    if m:
        start, exc, msg, frames, j, gr = i, m.group(1), m.group(2), [], i + 1, 0
        while j < n and j < i + 400:
            s = lines[j]
            if GO_GR.match(s):
                gr += 1
                if gr > 1:
                    break  # only the first (panicking) goroutine
                j += 1
                continue
            lm = GO_LOC.match(lines[j + 1]) if j + 1 < n else None
            fm = GO_FUNC.match(s)
            if lm and (fm or s.startswith("created by ")):
                if fm:
                    frames.append({"file": lm.group(1), "line": int(lm.group(2)), "func": fm.group(1)})
                j += 2
                continue
            if not s.strip() and frames:
                break
            if not s.strip() or s.startswith(("[signal", "exit status", "[recovered]")):
                j += 1
                continue
            break
        traces.append(("go", start, j, exc, msg, frames))
        i = j
        continue
    m = JAVA_EXC.match(l)
    if m and i + 1 < n and JAVA_AT.match(lines[i + 1]):
        start, frames, j = i, [], i + 1
        while j < n and (JAVA_AT.match(lines[j]) or JAVA_MORE.match(lines[j])):
            am = JAVA_AT.match(lines[j])
            if am:
                frames.append({"file": am.group(2), "line": int(am.group(3) or 0), "func": am.group(1)})
            j += 1
        traces.append(("java", start, j, m.group(1), m.group(2) or "", frames))
        i = j
        continue
    m = JS_EXC.match(l.strip())
    if m and i + 1 < n and JS_AT.match(lines[i + 1]):
        start, frames, j = i, [], i + 1
        while j < n and JS_AT.match(lines[j]):
            am = JS_AT.match(lines[j])
            f = am.group(2)
            frames.append({"file": f[7:] if f.startswith("file://") else f, "line": int(am.group(3)), "func": am.group(1) or ""})
            j += 1
        traces.append(("js", start, j, m.group(1), m.group(2), frames))
        i = j
        continue
    i += 1

if not traces:
    fail("no stack trace found (Python, Go, JS/TS, Java)", unparsed=nonblank(lines)[-10:])

lang, start, end, exc, msg, frames = traces[-1]
for f in frames:
    f["in_project"] = in_project(f["file"])
    if f["in_project"]:
        f["file"] = rel(path_in_root(f["file"][7:] if f["file"].startswith("file://") else f["file"]))
own = [f for f in frames if f["in_project"]]
# Python lists the innermost frame last; Go, JS and Java list it first
innermost = (own[-1] if lang == "python" else own[0]) if own else None
block = nonblank(lines[start:end])
conf = 0.95 if frames and exc else (0.7 if frames else 0.5)
data = {"lang": lang, "exception": exc, "message": clip(msg, 400), "frames": frames[-25:] if lang == "python" else frames[:25],
        "frame_count": len(frames), "first_project_frame": innermost, "traces_found": len(traces)}
warn = []
if len(traces) > 1:
    warn.append("%d traces in the input; this is the last one" % len(traces))
emit(True, data, conf, [], warn)
