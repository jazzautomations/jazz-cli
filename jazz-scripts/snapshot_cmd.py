# snapshot_cmd: characterization tests. The first run saves the command's stdout under .jazz/snap/<name>;
# later runs report same/changed with a unified diff (update=true accepts the new output).
import difflib

name = str(arg("name", ""))
if not re.match(r"^[\w][\w.-]{0,80}$", name) or ".." in name:
    fail("name must be letters, digits, '-', '_' or '.' (no path)")
cmd = str(arg("cmd", ""))
if not cmd.strip():
    fail("cmd is required")
snapdir = os.path.join(ROOT, ".jazz", "snap")
path = os.path.join(snapdir, name)

try:
    p = subprocess.Popen(cmd, shell=True, cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                         stderr=subprocess.STDOUT if ARGS.get("stderr") else subprocess.PIPE)
    out, err = p.communicate(timeout=50)
    code = p.returncode
except subprocess.TimeoutExpired:
    p.kill()
    p.communicate()
    fail("command timed out after 50s")
except OSError as e:
    fail("cannot run command: %s" % e)
new = out.decode("utf-8", "replace")
err_tail = nonblank((err or b"").decode("utf-8", "replace").splitlines())[-5:]


def save(text):
    if not os.path.isdir(snapdir):
        os.makedirs(snapdir)
    tmp = path + ".jazz-tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(text)
    os.replace(tmp, path)


data = {"name": name, "path": rel(path), "exit_code": code, "bytes": len(new.encode("utf-8"))}
warn = []
if code != 0:
    warn.append("command exited %d" % code)
if not os.path.exists(path):
    save(new)
    data["status"] = "created"
    emit(True, data, 1.0, err_tail, warn)
try:
    old = read_text(path)
except (IOError, OSError) as e:
    fail("cannot read snapshot: %s" % e)
if old == new:
    data["status"] = "same"
    emit(True, data, 1.0, err_tail, warn)
diff = list(difflib.unified_diff(old.splitlines(), new.splitlines(), "snapshot", "now", lineterm="", n=2))
data["added"] = sum(1 for l in diff if l.startswith("+") and not l.startswith("+++"))
data["removed"] = sum(1 for l in diff if l.startswith("-") and not l.startswith("---"))
data["diff"] = clip("\n".join(diff[:120]), 4000)
if ARGS.get("update"):
    save(new)
    data["status"] = "updated"
else:
    data["status"] = "changed"
emit(True, data, 1.0, err_tail, warn)
