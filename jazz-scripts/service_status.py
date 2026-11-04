# service_status: verification oracle. Observe whether a service/process is up to
# PROVE a start/stop happened, instead of trusting a command's exit code. Read-only:
# it runs only query commands (systemctl show, sv status, docker inspect) and reads
# /proc. It NEVER changes state. Python 3.8+ stdlib only. No network.
import glob
import shutil

name = str(arg("name", "") or "")
if not name.strip():
    fail("name is required")
kind = str(arg("kind", "auto"))
if kind not in ("auto", "systemd", "runit", "docker", "process"):
    fail("kind must be auto, systemd, runit, docker or process")

NOT_FOUND = {"active": False, "state": "not found", "pid": None, "since": None, "raw": ""}


def result(active, state, pid=None, since=None, raw=""):
    return {"active": active, "state": state, "pid": pid, "since": since, "raw": clip(raw, 400)}


def check_systemd():
    if not shutil.which("systemctl"):
        return None
    props = ["LoadState", "ActiveState", "SubState", "MainPID", "ActiveEnterTimestamp"]
    argv = ["systemctl", "show", name, "--no-pager"] + ["--property=" + p for p in props]
    code, out, err = run_argv(argv, timeout=5)
    kv = {}
    for line in out.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            kv[k] = v
    if not kv or kv.get("LoadState") in ("not-found", "masked", ""):
        return None  # unit unknown to systemd: let the next backend try
    astate = kv.get("ActiveState", "") or "unknown"
    try:
        pid = int(kv.get("MainPID", "0") or "0")
    except ValueError:
        pid = 0
    since = kv.get("ActiveEnterTimestamp") or None
    return result(astate == "active", astate, pid or None, since,
                  "LoadState=%s ActiveState=%s SubState=%s" % (
                      kv.get("LoadState", ""), astate, kv.get("SubState", "")))


def check_runit():
    if not shutil.which("sv"):
        return None
    dirs = ["/etc/service", "/var/service", "/service",
            os.path.expanduser("~/.runit/service"), os.path.expanduser("~/runit/service")]
    svc = None
    for d in dirs:
        cand = os.path.join(d, name)
        if os.path.isdir(cand):
            svc = cand
            break
    if svc is None:
        return None
    code, out, err = run_argv(["sv", "status", svc], timeout=5)
    line = (out or err).strip()
    if not line:
        return None
    word = line.split(":", 1)[0].strip().lower()
    active = word == "run"
    m = re.search(r"\(pid (\d+)\)", line)
    pid = int(m.group(1)) if m else None
    state = "running" if active else (word or "down")
    return result(active, state, pid, None, line)


def check_docker():
    if not shutil.which("docker"):
        return None
    fmt = "{{.State.Running}}|{{.State.Status}}|{{.State.Pid}}|{{.State.StartedAt}}"
    code, out, err = run_argv(["docker", "inspect", "--format", fmt, name], timeout=5)
    out = out.strip()
    if code != 0 or "|" not in out:
        return None  # no such container
    running, status, pid_s, started = (out.split("|", 3) + ["", "", "", ""])[:4]
    try:
        pid = int(pid_s or "0")
    except ValueError:
        pid = 0
    active = running.strip().lower() == "true"
    return result(active, status.strip() or ("running" if active else "exited"),
                  pid or None, started.strip() or None, out)


def check_process():
    if not os.path.isdir("/proc"):
        return None
    me = os.getpid()
    matches = []
    for cmdline in sorted(glob.glob("/proc/[0-9]*/cmdline")):
        try:
            pid = int(cmdline.split("/")[2])
        except (IndexError, ValueError):
            continue
        if pid == me:
            continue
        try:
            with open(cmdline, "rb") as f:
                raw = f.read()
        except (IOError, OSError):
            continue
        cmd = raw.replace(b"\0", b" ").decode("utf-8", "replace").strip()
        if name in cmd:
            matches.append((pid, cmd))
    if not matches:
        return None
    matches.sort(key=lambda x: x[0])
    pid, cmd = matches[0]
    return result(True, "running", pid, None, cmd)


BACKENDS = {"systemd": check_systemd, "runit": check_runit,
            "docker": check_docker, "process": check_process}
ORDER = ["systemd", "runit", "docker", "process"]

if kind == "auto":
    seq = ORDER
else:
    seq = [kind]

checked, found, kind_used = [], None, None
for k in seq:
    checked.append(k)
    r = BACKENDS[k]()
    if r is not None:
        found, kind_used = r, k
        break

if found is None:
    found = dict(NOT_FOUND)
    kind_used = kind if kind != "auto" else "none"

data = {"name": name, "kind_used": kind_used, "active": found["active"],
        "state": found["state"], "pid": found["pid"], "since": found["since"],
        "raw": found["raw"], "checked": checked}
data["text"] = "%s: %s (%s)" % (name, found["state"], kind_used)
# A clean "not found" is a valid oracle answer, never a failure.
emit(True, data, 0.9 if found["state"] != "not found" else 0.6, [], [])
