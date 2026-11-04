# env_tools: which toolchains/binaries this machine has, with versions. Fixed
# list of well-known tools (or args.tools), located with shutil.which and probed
# with `<bin> --version` (or `go version`, `ssh -V`, `java -version`) through
# run_argv only — a query, never a build. Budgeted: 2 s per probe, 4 s total;
# whatever does not fit keeps version null and sets truncated. Python 3.8+ stdlib.
import platform
import shutil
import time

T0 = time.time()
BUDGET_S = 4.0
PROBE_S = 2.0

# Order = relevance: when the budget runs out, the least important tools are
# the ones left without a version.
KNOWN = ["git", "python3", "go", "node", "npm", "docker", "podman", "gcc", "make",
         "cargo", "rustc", "java", "ruby", "php", "ssh", "rg", "jq", "curl"]
# Tools whose version flag is not `--version`.
PROBE_ARGS = {"go": ["version"], "ssh": ["-V"], "java": ["-version"]}
NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$")
# a dotted number not glued to a previous digit/dot: go1.27.2, v22.1.0, OpenSSH_9.6p1
VERSION_RE = re.compile(r"(?<![\d.])(\d+\.\d+(?:\.\d+)*(?:[a-z]\d+)?)")

warnings = []


def int_arg(name, default, lo, hi):
    """Integer arg clamped to lo..hi; a non-number fails honestly."""
    v = arg(name, default)
    try:
        v = int(v)
    except (TypeError, ValueError):
        fail("%s must be an integer (got %s)" % (name, clip(v, 40)))
    return lo if v < lo else (hi if v > hi else v)

_tools = arg("tools")
if _tools in (None, ""):
    names = list(KNOWN)
elif isinstance(_tools, list):
    names = []
    for n in _tools:
        n = str(n).strip()
        if not n:
            continue
        if not NAME_RE.match(n):
            # a path or shell-looking token is not a tool name; refuse rather
            # than execute something outside PATH
            fail("invalid tool name: %s" % clip(n, 60))
        if n not in names:
            names.append(n)
    if not names:
        fail("tools must be a non-empty list of binary names")
else:
    fail("tools must be a list of binary names")

limit = int_arg("limit", 50, 1, 50)


def first_line(s):
    for line in s.splitlines():
        if line.strip():
            return line.strip()
    return ""


def parse_version(out):
    """First dotted version number in the output, else null."""
    m = VERSION_RE.search(out)
    return m.group(1) if m else None


present = []
missing = []
for n in names:
    p = shutil.which(n)
    if p:
        present.append((n, p))
    else:
        missing.append(n)

n_present = len(present)
truncated = False
if len(present) > limit:
    present = present[:limit]
    truncated = True
    warnings.append("limit reached (%d): remaining present tools not probed" % limit)

tools = []
for n, p in present:
    entry = {"name": n, "path": p, "version": None, "ok": False, "error": None}
    remaining = BUDGET_S - (time.time() - T0)
    if remaining < 0.1:
        truncated = True
        entry["error"] = "time budget"
        tools.append(entry)
        continue
    argv = [p] + PROBE_ARGS.get(n, ["--version"])
    code, out, err = run_argv(argv, timeout=min(PROBE_S, remaining))
    combined = (out + "\n" + err).strip()
    if code == 124:
        entry["error"] = "timeout"
        truncated = True
    elif code == 127:
        entry["error"] = "cannot execute"
    else:
        ver = parse_version(first_line(combined)) or parse_version(combined)
        entry["version"] = ver
        if code == 0 and ver:
            entry["ok"] = True
        elif code != 0:
            entry["error"] = "exit %d" % code
        else:
            entry["error"] = "no version in output: %s" % clip(first_line(combined), 60)
    tools.append(entry)

if any(t["error"] in ("time budget", "timeout") for t in tools):
    warnings.append("time budget reached: some versions are null")

osinfo = {"system": platform.system() or None, "release": platform.release() or None,
          "machine": platform.machine() or None, "python": platform.python_version()}

n_total = len(names)
shown = []
for t in tools[:6]:
    shown.append("%s %s" % (t["name"], t["version"] if t["version"] else "?"))
text = "%d of %d tools present" % (n_present, n_total)
if shown:
    text += ": " + ", ".join(shown) + (" …" if len(tools) > 6 else "")
if missing:
    text += "; missing: " + ", ".join(missing[:10]) + (" +%d" % (len(missing) - 10) if len(missing) > 10 else "")
if truncated:
    text += " (truncated)"

ms = int((time.time() - T0) * 1000)
data = {"tools": tools, "missing": missing, "os": osinfo, "truncated": truncated,
        "ms": ms, "text": text}
emit(True, data, 1.0 if not truncated else 0.8, [], warnings)
