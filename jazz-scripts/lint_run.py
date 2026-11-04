# lint_run: run the QUERY linters that are already installed (gofmt -l, go vet, ruff check or
# flake8, eslint, shellcheck) and parse their output into one uniform list of findings.
# Detection is by shutil.which only: nothing is installed, nothing is fixed, no network.
# Each tool runs without a shell (argv list) under a shared wall-clock budget so the whole
# script stays inside the 60 s the harness allows. Python 3.8+ stdlib only.
import shutil
import time

T0 = time.time()
top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
try:
    limit = max(1, min(5000, int(arg("limit", 300))))
except (TypeError, ValueError):
    fail("limit must be an integer")


def as_list(v):
    if v is None:
        return []
    if isinstance(v, (list, tuple)):
        return [str(x).strip() for x in v if str(x).strip()]
    return [s for s in re.split(r"[\s,]+", str(v)) if s]


BUDGET_S = 55.0         # whole script (the Go side kills the run at 60 s)
TOOL_TIMEOUT = 60       # upper bound for one tool; the remaining budget wins when smaller
VERSION_TIMEOUT = 5
MAX_SH_FILES = 400      # shellcheck wants explicit files; walk at most this many *.sh

ALIASES = {"go": "go vet", "vet": "go vet", "govet": "go vet", "ruff check": "ruff"}
ORDER = ["gofmt", "go vet", "ruff", "flake8", "eslint", "shellcheck"]
BINARY = {"gofmt": "gofmt", "go vet": "go", "ruff": "ruff", "flake8": "flake8",
          "eslint": "eslint", "shellcheck": "shellcheck"}
VERSION_ARGV = {"go vet": ["go", "version"], "ruff": ["ruff", "--version"], "flake8": ["flake8", "--version"],
                "eslint": ["eslint", "--version"], "shellcheck": ["shellcheck", "--version"]}

requested = [ALIASES.get(t.lower(), t.lower()) for t in as_list(arg("tools"))]
for t in requested:
    if t not in BINARY:
        fail("unknown tool %r (known: %s)" % (t, ", ".join(ORDER)))
explicit = bool(requested)
wanted = requested or list(ORDER)
wanted = sorted(set(wanted), key=ORDER.index)
if not explicit and shutil.which("ruff"):
    wanted = [t for t in wanted if t != "flake8"]  # ruff covers python; flake8 is the fallback
elif not explicit and shutil.which("flake8"):
    wanted = [t for t in wanted if t != "ruff"]

present, missing = [], []
for t in wanted:
    if shutil.which(BINARY[t]):
        present.append(t)
    else:
        missing.append(t)


def remaining():
    return BUDGET_S - (time.time() - T0)


def version_of(tool):
    argv = VERSION_ARGV.get(tool)
    if not argv or remaining() < 2:
        return None
    code, out, err = run_argv(argv, timeout=VERSION_TIMEOUT)
    text = (out or err).strip()
    if code != 0 or not text:
        return None
    if tool == "shellcheck":
        m = re.search(r"version:\s*(\S+)", text)
        return m.group(1) if m else clip(text.splitlines()[0], 60)
    return clip(text.splitlines()[0], 60)


def sev(s):
    s = str(s or "").lower()
    if s in ("error", "fatal", "2"):
        return "error"
    if s in ("warning", "warn", "1"):
        return "warning"
    return "info"


def finding(tool, f, line, col, code, msg, severity="warning"):
    f = str(f or "")
    if f and os.path.isabs(f):
        f = rel(f)
    return {"tool": tool, "file": f.replace(os.sep, "/"), "line": int(line or 0), "col": int(col or 0),
            "code": str(code or ""), "msg": clip(str(msg or "").strip(), 300), "severity": sev(severity)}


def parse_json(out):
    try:
        v = json.loads(out)
    except ValueError:
        return None
    return v if isinstance(v, list) else None


R_GO = re.compile(r"^(?:\./)?([^\s:]+\.go):(\d+)(?::(\d+))?: (.+)$")
R_FLAKE = re.compile(r"^(.+?):(\d+):(\d+): ([A-Z]+\d+) (.+)$")


def run_gofmt():
    code, out, err = run_argv([BINARY["gofmt"], "-l", top], timeout=tool_timeout())
    fs = []
    for line in out.splitlines():
        line = line.strip()
        if line:
            fs.append(finding("gofmt", line, 0, 0, "gofmt", "file is not gofmt-formatted"))
    for line in err.splitlines():  # parse errors keep gofmt from formatting: report them as errors
        m = R_GO.match(line.strip())
        if m:
            fs.append(finding("gofmt", m.group(1), m.group(2), m.group(3), "syntax", m.group(4), "error"))
    return code, fs, out, err


def run_govet():
    cwd = top if os.path.isdir(top) else os.path.dirname(top)
    target = "./..." if os.path.isdir(top) else os.path.basename(top)
    code, out, err = run_argv([BINARY["go vet"], "vet", target], timeout=tool_timeout(), cwd=cwd)
    fs = []
    for line in (err + "\n" + out).splitlines():
        m = R_GO.match(line.strip())
        if m and not line.startswith("#"):
            msg = m.group(4)
            fs.append(finding("go vet", os.path.join(rel(cwd), m.group(1)) if rel(cwd) not in (".", "") else m.group(1),
                              m.group(2), m.group(3), "vet", msg, "warning"))
    return code, fs, out, err


def run_ruff():
    code, out, err = run_argv([BINARY["ruff"], "check", "--output-format", "json", "--exit-zero", top], timeout=tool_timeout())
    fs = []
    items = parse_json(out)
    if items is None and out.strip():  # older ruff: text lines "file:line:col: CODE msg"
        for line in out.splitlines():
            m = R_FLAKE.match(line.strip())
            if m:
                fs.append(finding("ruff", m.group(1), m.group(2), m.group(3), m.group(4), m.group(5)))
        return code, fs, out, err
    for it in items or []:
        if not isinstance(it, dict):
            continue
        loc = it.get("location") or {}
        fs.append(finding("ruff", it.get("filename"), loc.get("row"), loc.get("column"), it.get("code"), it.get("message")))
    return code, fs, out, err


def run_flake8():
    code, out, err = run_argv([BINARY["flake8"], "--", top], timeout=tool_timeout())
    fs = []
    for line in out.splitlines():
        m = R_FLAKE.match(line.strip())
        if m:
            severity = "error" if m.group(4)[:1] in ("E", "F") else "warning"
            fs.append(finding("flake8", m.group(1), m.group(2), m.group(3), m.group(4), m.group(5), severity))
    return code, fs, out, err


def run_eslint():
    code, out, err = run_argv([BINARY["eslint"], "--format", "json", "--no-error-on-unmatched-pattern", "--", top],
                              timeout=tool_timeout())
    fs = []
    for entry in parse_json(out) or []:
        if not isinstance(entry, dict):
            continue
        fpath = entry.get("filePath", "")
        for m in entry.get("messages") or []:
            if not isinstance(m, dict):
                continue
            fs.append(finding("eslint", fpath, m.get("line"), m.get("column"), m.get("ruleId") or "",
                              m.get("message"), m.get("severity")))
    return code, fs, out, err


def run_shellcheck():
    if os.path.isfile(top):
        targets = [top]
    else:
        targets = []
        for p in walk_files(top, limit=50000):
            if p.endswith((".sh", ".bash")):
                targets.append(p)
                if len(targets) >= MAX_SH_FILES:
                    break
    if not targets:
        return 0, [], "", ""
    code, out, err = run_argv([BINARY["shellcheck"], "-f", "json", "--"] + targets, timeout=tool_timeout())
    fs = []
    for it in parse_json(out) or []:
        if not isinstance(it, dict):
            continue
        fs.append(finding("shellcheck", it.get("file"), it.get("line"), it.get("column"),
                          "SC%s" % it.get("code", ""), it.get("message"), it.get("level")))
    return code, fs, out, err


def tool_timeout():
    return max(1, int(min(TOOL_TIMEOUT, remaining())))


RUNNERS = {"gofmt": run_gofmt, "go vet": run_govet, "ruff": run_ruff, "flake8": run_flake8,
           "eslint": run_eslint, "shellcheck": run_shellcheck}

tools_run, findings, warnings, unparsed, truncated = [], [], [], [], False
for t in present:
    if remaining() < 2:
        truncated = True
        warnings.append("time budget reached before %s" % t)
        break
    t0 = time.time()
    code, fs, out, err = RUNNERS[t]()
    ms = int((time.time() - t0) * 1000)
    if code == 124:
        truncated = True
        warnings.append("%s timed out after %d ms (partial output parsed)" % (t, ms))
    elif code == 127:
        warnings.append("%s could not be started: %s" % (t, clip(err, 120)))
    elif code not in (0, 1) and not fs:
        tail = [l for l in (err or out).splitlines() if l.strip()][-5:]
        unparsed.extend(tail)
        warnings.append("%s exited %d without parseable findings" % (t, code))
    tools_run.append({"tool": t, "version": version_of(t), "exit": code, "findings": len(fs), "ms": ms})
    findings.extend(fs)

findings.sort(key=lambda f: (f["file"], f["line"], f["col"], f["tool"], f["code"]))
total = len(findings)
if total > limit:
    truncated = True
    warnings.append("%d findings; showing %d (raise limit or narrow path)" % (total, limit))
    findings = findings[:limit]
if not present:
    warnings.append("no linter available on this machine (looked for: %s)" % ", ".join(wanted))

data = {"tools_run": tools_run, "tools_missing": missing, "findings": findings,
        "total": total, "truncated": truncated, "ms": int((time.time() - T0) * 1000)}
data["text"] = "%d finding%s from %d tool%s (%d missing)" % (
    total, "" if total == 1 else "s", len(tools_run), "" if len(tools_run) == 1 else "s", len(missing))
emit(True, data, 0.9 if tools_run else 0.4, unparsed, warnings)
