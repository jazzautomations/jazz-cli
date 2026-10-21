# jazz script prelude (prepended to every Python script by the harness).
# Python 3.6+ stdlib only. Contract: read the JSON args object from stdin,
# write ONE JSON object to stdout:
#   {"ok": bool, "confidence": 0..1, "data": {...}, "unparsed": [...], "warnings": [...]}
import json
import os
import re
import subprocess
import sys

try:
    ARGS = json.loads(sys.stdin.read() or "{}")
except ValueError:
    ARGS = {}
if not isinstance(ARGS, dict):
    ARGS = {}
ROOT = os.environ.get("JAZZ_ROOT") or os.getcwd()
MAX_UNPARSED = 30


def emit(ok=True, data=None, confidence=1.0, unparsed=None, warnings=None, error=None):
    out = {
        "ok": bool(ok),
        "confidence": round(max(0.0, min(1.0, float(confidence))), 2),
        "data": data if data is not None else {},
        "unparsed": [clip(u, 200) for u in (unparsed or [])[:MAX_UNPARSED]],
        "warnings": warnings or [],
    }
    if error:
        out["error"] = error
    sys.stdout.write(json.dumps(out, ensure_ascii=False, separators=(",", ":")))
    sys.stdout.flush()
    sys.exit(0)


def fail(msg, **kw):
    kw.setdefault("confidence", 0.0)
    emit(ok=False, error=msg, **kw)


def clip(s, n):
    s = str(s)
    return s if len(s) <= n else s[: n - 1] + "…"


def arg(name, default=None):
    v = ARGS.get(name)
    return default if v is None or v == "" else v


def path_in_root(p):
    p = os.path.expanduser(str(p))
    return p if os.path.isabs(p) else os.path.join(ROOT, p)


def rel(p):
    try:
        r = os.path.relpath(p, ROOT)
    except ValueError:
        return p
    return p if r.startswith("..") else r


def run(cmd, timeout=50, cwd=None):
    """Run a shell command; returns (exit code, combined stdout+stderr)."""
    try:
        p = subprocess.Popen(cmd, shell=True, cwd=cwd or ROOT, stdin=subprocess.DEVNULL,
                             stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    except OSError as e:
        return 127, str(e)
    try:
        out, _ = p.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        p.kill()
        out, _ = p.communicate()
        return 124, out.decode("utf-8", "replace")[-(4 << 20):] + "\n[timeout after %ss]" % timeout
    return p.returncode, out.decode("utf-8", "replace")[-(4 << 20):]


def run_argv(argv, timeout=50, cwd=None):
    """Run argv without a shell; returns (exit code, stdout, stderr)."""
    try:
        p = subprocess.Popen(argv, cwd=cwd or ROOT, stdin=subprocess.DEVNULL,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except OSError as e:
        return 127, "", str(e)
    try:
        out, err = p.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        p.kill()
        out, err = p.communicate()
        return 124, out.decode("utf-8", "replace"), "timeout"
    return p.returncode, out.decode("utf-8", "replace"), err.decode("utf-8", "replace")


def read_text(p, limit=8 << 20):
    with open(p, "rb") as f:
        f.seek(0, 2)
        size = f.tell()
        f.seek(max(0, size - limit))
        return f.read().decode("utf-8", "replace")


def get_input():
    """The text to parse: args.text, args.file (last 8 MB) or the output of args.cmd.
    Returns (text, source, exit code or None)."""
    if arg("text") is not None:
        return str(ARGS["text"]), "text", None
    if arg("file") is not None:
        p = path_in_root(ARGS["file"])
        try:
            return read_text(p), rel(p), None
        except (IOError, OSError) as e:
            fail("cannot read file: %s" % e)
    if arg("cmd") is not None:
        code, out = run(str(ARGS["cmd"]))
        return out, "cmd", code
    fail("pass one of: file, cmd, text")


def nonblank(lines):
    return [l for l in lines if l.strip()]


SKIP_DIRS = set(("node_modules vendor dist build target __pycache__ venv env coverage "
                 "third_party Pods").split())


def walk_files(top, limit=20000):
    """Yield file paths under top, skipping hidden, vendored and generated dirs."""
    n = 0
    for d, dirs, files in os.walk(top):
        dirs[:] = sorted(x for x in dirs if x not in SKIP_DIRS and not x.startswith("."))
        for f in sorted(files):
            n += 1
            if n > limit:
                return
            yield os.path.join(d, f)
