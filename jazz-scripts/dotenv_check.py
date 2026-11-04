# dotenv_check: compare a .env against its example and report missing/extra/empty
# variables. Reports only variable NAMES, never values (they may be secrets).
# Read-only, deterministic, stdlib only.
target = path_in_root(arg("path", "."))
example_arg = arg("example")
required = arg("required") or []
if isinstance(required, str):
    required = [required]
required = [str(x).strip() for x in required if str(x).strip()]

warnings = []


def parse_env_file(fpath):
    """Return (order, values) — distinct names in first-seen order (last value
    wins) — plus the list of names that appear more than once."""
    order, values, dups = [], {}, []
    try:
        if os.path.getsize(fpath) > (2 << 20):
            warnings.append("%s over 2 MB; truncated read" % rel(fpath))
        text = read_text(fpath)
    except (IOError, OSError) as e:
        fail("cannot read %s: %s" % (rel(fpath), e))
    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith("#"):
            continue
        if s.startswith("export ") or s.startswith("export\t"):
            s = s[7:].lstrip()
        if "=" not in s:
            continue
        k, _, v = s.partition("=")
        k = k.strip()
        if not k:
            continue
        v = v.strip()
        if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
            v = v[1:-1]
        elif v[:1] not in "\"'" and " #" in v:
            v = v.split(" #", 1)[0].strip()
        if k in values:
            if k not in dups:
                dups.append(k)
        else:
            order.append(k)
        values[k] = v
    return order, values, dups


# ---- locate the .env file ----
if os.path.isdir(target):
    env_path = os.path.join(target, ".env")
    base_dir = target
else:
    env_path = target
    base_dir = os.path.dirname(target) or "."
env_exists = os.path.isfile(env_path)

# ---- locate the example file ----
if example_arg:
    ex_path = path_in_root(example_arg)
    ex_exists = os.path.isfile(ex_path)
    if not ex_exists:
        warnings.append("example file not found: %s" % rel(ex_path))
else:
    ex_path = None
    for cand in (".env.example", ".env.sample", ".env.template"):
        c = os.path.join(base_dir, cand)
        if os.path.isfile(c):
            ex_path = c
            ex_exists = True
            break
    else:
        ex_exists = False

if not env_exists and not ex_exists:
    emit(
        ok=True,
        data={"env_file": None, "example_file": None, "missing": [], "extra": [],
              "empty": [], "duplicated": [], "count": 0, "ok_all": True,
              "text": "no .env or example file found"},
        confidence=1.0,
        warnings=["no .env or example file found under %s" % rel(base_dir)],
    )

env_order, env_vals, env_dups = parse_env_file(env_path) if env_exists else ([], {}, [])
env_set = set(env_order)

ex_order = []
if ex_exists:
    ex_order, _ex_vals, _ex_dups = parse_env_file(ex_path)
ex_set = set(ex_order)

# ---- compute the report (names only) ----
need, seen = [], set()
for name in ex_order + required:
    if name not in seen:
        seen.add(name)
        if name not in env_set:
            need.append(name)

extra = [name for name in env_order if ex_exists and name not in ex_set]
empty = [name for name in env_order if env_vals.get(name, "") == ""]
duplicated = list(env_dups)

ok_all = not (need or empty or duplicated)
text = "%d missing, %d extra, %d empty" % (len(need), len(extra), len(empty))

emit(
    ok=True,
    data={
        "env_file": rel(env_path) if env_exists else None,
        "example_file": rel(ex_path) if ex_exists else None,
        "missing": need,
        "extra": extra,
        "empty": empty,
        "duplicated": duplicated,
        "count": len(env_order),
        "ok_all": ok_all,
        "text": text,
    },
    confidence=1.0,
    warnings=warnings,
)
