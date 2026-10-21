# detect_project: languages, build/test/lint/fmt commands, entry points, file count, git.
# Keep the command choice in sync with fpDetect (intents.go): a Go test cross-checks both.
top = path_in_root(arg("path", "."))
if not os.path.isdir(top):
    fail("not a directory: %s" % rel(top))


def has(name):
    return os.path.exists(os.path.join(top, name))


def read(name, limit=256 << 10):
    try:
        with open(os.path.join(top, name), "rb") as f:
            return f.read(limit).decode("utf-8", "replace")
    except (IOError, OSError):
        return ""


langs, markers, entry, warn = [], [], [], []
cmds = {"test": "", "build": "", "lint": "", "fmt": ""}
pm = ""


def put(k, v):
    if v and not cmds[k]:
        cmds[k] = v


make_targets = set(re.findall(r"(?m)^([A-Za-z][\w-]*)\s*:(?!=)", read("Makefile"))) if has("Makefile") else set()
if has("go.mod"):
    langs.append("go")
    markers.append("go.mod")
if has("Cargo.toml"):
    langs.append("rust")
    markers.append("Cargo.toml")
if has("package.json"):
    langs.append("js")
    markers.append("package.json")
py_markers = [m for m in ("pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "pytest.ini", "tox.ini") if has(m)]
if py_markers:
    langs.append("python")
    markers.extend(py_markers)
if has("Makefile"):
    markers.append("Makefile")

# one language: its native commands win; none or several: the Makefile targets win
use_make = has("Makefile") and len(langs) != 1
if use_make:
    for k in ("test", "build", "lint", "fmt"):
        if k in make_targets:
            put(k, "make " + k)

if "go" in langs:
    put("test", "go test ./...")
    put("build", "go build ./...")
    put("lint", "go vet ./...")
    put("fmt", "gofmt -l .")
    if has("main.go"):
        entry.append("main.go")
    if os.path.isdir(os.path.join(top, "cmd")):
        entry.extend(sorted("cmd/" + d for d in os.listdir(os.path.join(top, "cmd")))[:10])
if "rust" in langs:
    put("test", "cargo test")
    put("build", "cargo build")
    put("lint", "cargo clippy")
    put("fmt", "cargo fmt --check")
    for e in ("src/main.rs", "src/lib.rs"):
        if has(e):
            entry.append(e)
if "js" in langs:
    pm = "npm"
    for lock, name in (("pnpm-lock.yaml", "pnpm"), ("yarn.lock", "yarn"), ("bun.lockb", "bun"), ("bun.lock", "bun")):
        if has(lock):
            pm = name
            break
    try:
        pkg = json.loads(read("package.json") or "{}")
    except ValueError:
        pkg = {}
        warn.append("package.json is not valid JSON")
    scripts = pkg.get("scripts") if isinstance(pkg, dict) and isinstance(pkg.get("scripts"), dict) else {}
    run = pm + " run "
    t = scripts.get("test", "")
    if t and "no test specified" not in t:
        put("test", pm + " test")
    if "build" in scripts:
        put("build", run + "build")
    if "lint" in scripts:
        put("lint", run + "lint")
    for k in ("format", "fmt"):
        if k in scripts:
            put("fmt", run + k)
            break
    if isinstance(pkg, dict) and isinstance(pkg.get("main"), str):
        entry.append(pkg["main"])
if "python" in langs:
    pyproject = read("pyproject.toml")
    pytest = has("pytest.ini") or has("conftest.py") or "[tool.pytest" in pyproject or "[pytest]" in read("tox.ini") \
        or "[tool:pytest]" in read("setup.cfg") or "pytest" in read("requirements.txt") \
        or "pytest" in read("requirements-dev.txt")
    put("test", "python3 -m pytest" if pytest else "python3 -m unittest discover")
    if "[tool.ruff" in pyproject or has("ruff.toml") or has(".ruff.toml"):
        put("lint", "ruff check .")
        put("fmt", "ruff format --check .")
    elif has(".flake8") or "[flake8]" in read("setup.cfg") or "[flake8]" in read("tox.ini"):
        put("lint", "python3 -m flake8")
    for e in ("main.py", "app.py", "manage.py", "__main__.py"):
        if has(e):
            entry.append(e)
if not use_make and has("Makefile"):
    for k in ("test", "build", "lint", "fmt"):
        if k in make_targets:
            put(k, "make " + k)

files = 0
for _ in walk_files(top, limit=20000):
    files += 1
if files >= 20000:
    warn.append("stopped counting at 20000 files")
git = False
d = top
while True:
    if os.path.exists(os.path.join(d, ".git")):
        git = True
        break
    up = os.path.dirname(d)
    if up == d:
        break
    d = up
data = {"root": rel(top), "langs": langs, "markers": markers, "test": cmds["test"], "build": cmds["build"],
        "lint": cmds["lint"], "fmt": cmds["fmt"], "package_manager": pm, "entry_points": entry[:10],
        "make_targets": sorted(make_targets)[:30], "files": files, "git": git}
if not langs and not make_targets:
    warn.append("no project markers found (go.mod, package.json, Cargo.toml, pyproject.toml, Makefile ...)")
emit(True, data, 1.0 if langs or make_targets else 0.5, [], warn)
