# pkg_installed: what is installed, per package manager, read-only. System: dpkg-query -W
# or rpm -qa. Python: pip list --format=json (pip3, pip, then python3 -m pip). Node:
# npm ls -g --json --depth 0 (global) and node_modules/*/package.json read directly
# (project, no npm). Go: the require block of go.mod, else the modules in go.sum, plus
# `go version`. Only LISTING commands run (argv, no shell); nothing is installed, removed
# or fetched. Each manager gets at most 5 s inside an 8 s total budget: what does not fit
# is reported skipped:true and truncated:true. Unknown version = null. Python 3.8+ stdlib.
import shutil
import time

T0 = time.time()
BUDGET_S = 8.0
MANAGER_TIMEOUT = 5.0
MAX_PROJECT_PKGS = 20000
# run order: cheap local reads first, npm -g (the slowest) last; output keeps ORDER.
ORDER = ["dpkg", "rpm", "pip", "npm", "npm(project)", "go"]
RUN_ORDER = ["dpkg", "rpm", "pip", "npm(project)", "go", "npm"]
CHOICES = ("auto", "dpkg", "rpm", "pip", "npm", "go")

manager = str(arg("manager", "auto")).lower()
if manager not in CHOICES:
    fail("manager must be one of: %s" % ", ".join(CHOICES))
needle = str(arg("name", "")).strip().lower()
try:
    limit = int(arg("limit", 300))
except (TypeError, ValueError):
    fail("limit must be an integer")
if limit < 1:
    fail("limit must be >= 1")
top = os.path.normpath(path_in_root(arg("path", ".")))
if not os.path.isdir(top):
    fail("path is not a directory: %s" % rel(top))

warnings, unparsed = [], []
truncated = False     # list cut by limit, or a manager skipped / partial
incomplete = False    # a manager was skipped, timed out or stopped at the budget


def remaining():
    return BUDGET_S - (time.time() - T0)


def tool_timeout():
    return max(0.5, min(MANAGER_TIMEOUT, remaining()))


def ver(v):
    v = "" if v is None else str(v).strip()
    return v or None


def pkg(mgr, name, version):
    return {"manager": mgr, "name": str(name).strip(), "version": ver(version)}


# ---- availability (no subprocess) ----
def pip_argv():
    for b in ("pip3", "pip"):
        if shutil.which(b):
            return [b]
    return [sys.executable, "-m", "pip"] if sys.executable else None


def has_project_node_modules():
    return os.path.isdir(os.path.join(top, "node_modules"))


def go_source():
    for n in ("go.mod", "go.sum"):
        if os.path.isfile(os.path.join(top, n)):
            return n
    return None


AVAILABLE = {
    "dpkg": bool(shutil.which("dpkg-query")),
    "rpm": bool(shutil.which("rpm")),
    "pip": pip_argv() is not None,
    "npm": bool(shutil.which("npm")),
    "npm(project)": has_project_node_modules(),
    "go": go_source() is not None,
}


# ---- listers: each returns (packages, note|None); note explains an empty/partial list ----
def run_list(argv):
    """run_argv under the manager timeout; (code, stdout, stderr)."""
    return run_argv(argv, timeout=tool_timeout())


def list_dpkg():
    code, out, err = run_list(["dpkg-query", "-W", "-f=${binary:Package}\\t${Version}\\t${Status}\\n"])
    if code == 124:
        return [], "timeout"
    if code != 0 and not out.strip():
        return [], "dpkg-query exited %d: %s" % (code, clip(err.strip(), 120))
    pk = []
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) < 3:
            if line.strip():
                unparsed.append(line)
            continue
        if not parts[2].strip().endswith(" installed"):
            continue  # config-files / half-installed / not-installed
        pk.append(pkg("dpkg", parts[0], parts[1]))
    return pk, None


def list_rpm():
    code, out, err = run_list(["rpm", "-qa", "--qf", "%{NAME}\\t%{VERSION}-%{RELEASE}\\n"])
    if code == 124:
        return [], "timeout"
    if code != 0 and not out.strip():
        return [], "rpm exited %d: %s" % (code, clip(err.strip(), 120))
    pk = []
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) < 2:
            if line.strip():
                unparsed.append(line)
            continue
        pk.append(pkg("rpm", parts[0], parts[1]))
    return pk, None


def list_pip():
    base = pip_argv()
    code, out, err = run_list(base + ["list", "--format=json", "--disable-pip-version-check"])
    if code == 124:
        return [], "timeout"
    try:
        items = json.loads(out)
    except ValueError:
        items = None
    if not isinstance(items, list):
        return [], "pip list gave no JSON (exit %d): %s" % (code, clip((err or out).strip(), 120))
    pk = []
    for it in items:
        if isinstance(it, dict) and it.get("name"):
            pk.append(pkg("pip", it["name"], it.get("version")))
    return pk, None


def list_npm_global():
    code, out, err = run_list(["npm", "ls", "-g", "--json", "--depth", "0"])
    if code == 124:
        return [], "timeout"
    try:
        tree = json.loads(out)
    except ValueError:
        tree = None
    if not isinstance(tree, dict):
        return [], "npm ls -g gave no JSON (exit %d): %s" % (code, clip((err or out).strip(), 120))
    pk = []
    deps = tree.get("dependencies") or {}
    for name in sorted(deps):
        d = deps[name] if isinstance(deps[name], dict) else {}
        pk.append(pkg("npm", name, d.get("version")))
    return pk, None


def read_pkg_json(p):
    try:
        if os.path.getsize(p) > (1 << 20):
            return None
        with open(p, "rb") as f:
            v = json.loads(f.read().decode("utf-8", "replace"))
    except (IOError, OSError, ValueError):
        return None
    return v if isinstance(v, dict) else None


def list_npm_project():
    """node_modules/<name>/package.json and node_modules/@scope/<name>/package.json,
    read directly. Nested node_modules are not descended (depth-0 view)."""
    nm = os.path.join(top, "node_modules")
    pk, n, note = [], 0, None
    try:
        entries = sorted(os.listdir(nm))
    except OSError as e:
        return [], "cannot list node_modules: %s" % e
    dirs = []
    for e in entries:
        if e.startswith("."):
            continue
        p = os.path.join(nm, e)
        if not os.path.isdir(p):
            continue
        if e.startswith("@"):
            try:
                for s in sorted(os.listdir(p)):
                    if not s.startswith(".") and os.path.isdir(os.path.join(p, s)):
                        dirs.append(("%s/%s" % (e, s), os.path.join(p, s)))
            except OSError:
                continue
        else:
            dirs.append((e, p))
    for name, p in dirs:
        if time.time() - T0 > BUDGET_S:
            note = "time budget reached after %d packages" % n
            break
        if n >= MAX_PROJECT_PKGS:
            note = "more than %d packages; the rest were not read" % MAX_PROJECT_PKGS
            break
        pj = read_pkg_json(os.path.join(p, "package.json"))
        if pj is None:
            continue  # a directory without a readable package.json is not a package
        n += 1
        pk.append(pkg("npm(project)", pj.get("name") or name, pj.get("version")))
    return pk, note


def list_go():
    src = go_source()
    p = os.path.join(top, src)
    try:
        with open(p, "rb") as f:
            text = f.read(8 << 20).decode("utf-8", "replace")
    except (IOError, OSError) as e:
        return [], "cannot read %s: %s" % (src, e)
    pairs = set()
    if src == "go.mod":
        in_block = False
        for raw in text.splitlines():
            line = raw.split("//", 1)[0].strip()
            if not line:
                continue
            if in_block:
                if line.startswith(")"):
                    in_block = False
                    continue
                parts = line.split()
                if len(parts) >= 2:
                    pairs.add((parts[0], parts[1]))
                continue
            if line == "require (":
                in_block = True
            elif line.startswith("require "):
                parts = line.split()
                if len(parts) >= 3:
                    pairs.add((parts[1], parts[2]))
    else:
        for raw in text.splitlines():
            parts = raw.split()
            if len(parts) >= 2:
                v = parts[1][:-7] if parts[1].endswith("/go.mod") else parts[1]
                pairs.add((parts[0], v))
    pk = [pkg("go", m, v) for m, v in sorted(pairs)]
    return pk, None if pk else "%s lists no modules" % src


LISTERS = {"dpkg": list_dpkg, "rpm": list_rpm, "pip": list_pip, "npm": list_npm_global,
           "npm(project)": list_npm_project, "go": list_go}

if manager == "auto":
    wanted = list(ORDER)
elif manager == "npm":
    wanted = ["npm", "npm(project)"]
else:
    wanted = [manager]

managers = {}
for m in ORDER:
    managers[m] = {"name": m, "available": AVAILABLE[m], "count": 0, "ms": 0, "skipped": False,
                   "version": None, "source": None}
for m in wanted:
    if not AVAILABLE[m] and manager != "auto":
        warnings.append("%s: not available (%s)" % (m, {
            "dpkg": "dpkg-query not in PATH", "rpm": "rpm not in PATH", "pip": "no pip/pip3/python3 -m pip",
            "npm": "npm not in PATH", "npm(project)": "no node_modules in %s" % rel(top),
            "go": "no go.mod/go.sum in %s" % rel(top)}[m]))

packages = []
for m in RUN_ORDER:
    if m not in wanted or not AVAILABLE[m]:
        continue
    entry = managers[m]
    entry["source"] = {"dpkg": "dpkg-query", "rpm": "rpm -qa", "pip": " ".join(pip_argv() or []) + " list",
                       "npm": "npm ls -g", "npm(project)": "node_modules/*/package.json",
                       "go": go_source()}[m]
    if remaining() < 0.5:
        entry["skipped"] = True
        truncated = incomplete = True
        warnings.append("%s skipped: time budget reached" % m)
        continue
    t0 = time.time()
    pk, note = LISTERS[m]()
    entry["ms"] = int((time.time() - t0) * 1000)
    if note == "timeout":
        entry["skipped"] = True
        truncated = incomplete = True
        warnings.append("%s timed out after %d ms" % (m, entry["ms"]))
        continue
    if note:
        warnings.append("%s: %s" % (m, note))
        if note.startswith("time budget") or note.startswith("more than"):
            truncated = incomplete = True
    entry["count"] = len(pk)
    packages.extend(pk)
    if m == "go" and shutil.which("go") and remaining() > 1:
        code, out, _ = run_argv(["go", "version"], timeout=min(5, int(remaining())))
        mm = re.search(r"\bgo\d+(\.\d+)*\S*", out)
        entry["version"] = mm.group(0) if code == 0 and mm else None

if needle:
    packages = [p for p in packages if needle in p["name"].lower()]
packages.sort(key=lambda p: (ORDER.index(p["manager"]), p["name"].lower(), p["version"] or ""))
total = len(packages)
if total > limit:
    truncated = True
    warnings.append("%d packages; showing %d (raise limit or filter by name)" % (total, limit))
    packages = packages[:limit]

ran = [managers[m] for m in ORDER if m in wanted and AVAILABLE[m]]
parts = ["%s %d" % (e["name"], e["count"]) for e in ran if not e["skipped"]]
skipped = [e["name"] for e in ran if e["skipped"]]
if parts:
    text = ", ".join(parts) + " packages"
elif ran:
    text = "no package list"
else:
    text = "no package manager available" if manager == "auto" else "%s not available" % manager
if needle:
    text += "; %d match '%s'" % (total, clip(needle, 40))
if skipped:
    text += " (%s skipped)" % ", ".join(skipped)

data = {"managers": [managers[m] for m in ORDER if m in wanted], "packages": packages, "total": total,
        "truncated": truncated, "text": text}
emit(True, data, 0.9 if ran and not incomplete else (0.6 if ran else 0.4), unparsed, warnings)
