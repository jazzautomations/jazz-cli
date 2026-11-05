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
# Only git's check mode is allowed. Never create a patch or touch a target.
if truncated: fail("patch exceeds budget", data={"truncated": True})
if not arg("file") or "text" in ARGS:
    fail("diff check requires a patch file")
patch = bounded_path(arg("file"))
# Validate target paths as well as the patch path; reject external/symlink targets.
for line in src.splitlines():
    if line.startswith(("--- ", "+++ ")):
        name = line[4:].split("\t")[0]
        if name == "/dev/null": continue
        if not name.startswith(("a/", "b/")):
            fail("patch paths must use a/ and b/ prefixes")
        bounded_path(name[2:])
code, out, err = run_argv(["git", "apply", "--check", "--", patch], timeout=3.5)
if code == 127: fail("git unavailable")
if code == 124:
    truncated = True
    finish({"applies": None}, "Patch check timed out; applicability unknown")
finish({"applies": code == 0, "diagnostic": clip(err, 1000)}, "Patch applies cleanly (dry-run)" if code == 0 else "Patch does not apply (dry-run)")
