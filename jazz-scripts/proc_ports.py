# proc_ports: listening TCP ports and processes read straight from /proc,
# no ps/ss/netstat/lsof. Owner of a port = the pid whose /proc/<pid>/fd holds
# the socket inode. Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0

if not os.path.isdir("/proc"):
    fail("no /proc on this system")

_port = arg("port")
want_port = int(_port) if _port not in (None, "") else None
if want_port is not None and (want_port < 1 or want_port > 65535):
    fail("port must be 1..65535")
_name = arg("name")
name_filter = str(_name) if _name not in (None, "") else None
limit = int(arg("limit", 100))
limit = 1 if limit < 1 else (2000 if limit > 2000 else limit)

warnings = []


def parse_net(path, proto, out):
    """Append LISTEN (state 0A) sockets from a /proc/net/tcp* table."""
    try:
        with open(path) as f:
            lines = f.read().splitlines()
    except (IOError, OSError):
        return  # no IPv6 table, etc. — silent
    for line in lines[1:]:
        parts = line.split()
        if len(parts) < 10 or parts[3].upper() != "0A":
            continue
        local = parts[1]
        try:
            port = int(local.rsplit(":", 1)[1], 16)
            inode = int(parts[9])
        except (IndexError, ValueError):
            continue
        out.append({"proto": proto, "port": port, "inode": inode})


listening = []
parse_net("/proc/net/tcp", "tcp", listening)
parse_net("/proc/net/tcp6", "tcp6", listening)

needed = set(e["inode"] for e in listening if e["inode"] > 0)
inode_pid = {}   # socket inode -> owning pid
procinfo = {}    # pid -> {comm, ppid, cmdline}
skipped = 0


def read_stat(pid):
    """(comm, ppid, cmdline) for a pid, or None if it vanished / EACCES."""
    try:
        with open("/proc/%d/stat" % pid) as f:
            data = f.read()
    except (IOError, OSError):
        return None
    lo, hi = data.find("("), data.rfind(")")
    if lo < 0 or hi < 0:
        return None
    comm = data[lo + 1:hi]
    rest = data[hi + 2:].split()
    ppid = 0
    if len(rest) >= 2:
        try:
            ppid = int(rest[1])
        except ValueError:
            ppid = 0
    cmdline = ""
    try:
        with open("/proc/%d/cmdline" % pid, "rb") as f:
            cmdline = f.read().replace(b"\x00", b" ").decode("utf-8", "replace").strip()
    except (IOError, OSError):
        cmdline = ""
    return comm, ppid, cmdline or ("[%s]" % comm)


def resolve_fds(pid):
    """Map any needed socket inode owned by pid (ignore EACCES / exit)."""
    if not needed:
        return
    fddir = "/proc/%d/fd" % pid
    try:
        fds = os.listdir(fddir)
    except (IOError, OSError):
        return
    for fd in fds:
        try:
            link = os.readlink(os.path.join(fddir, fd))
        except (IOError, OSError):
            continue
        if link.startswith("socket:["):
            try:
                ino = int(link[8:-1])
            except ValueError:
                continue
            if ino in needed and ino not in inode_pid:
                inode_pid[ino] = pid


pids = sorted(int(n) for n in os.listdir("/proc") if n.isdigit())
truncated = False
for pid in pids:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached")
        break
    info = read_stat(pid)
    if info is None:
        skipped += 1
        continue
    comm, ppid, cmdline = info
    procinfo[pid] = {"comm": comm, "ppid": ppid, "cmdline": cmdline}
    resolve_fds(pid)

if skipped:
    warnings.append("%d process(es) skipped (no permission or exited)" % skipped)

for e in listening:
    pid = inode_pid.get(e["inode"], 0)
    e["pid"] = pid
    e["comm"] = procinfo.get(pid, {}).get("comm", "") if pid else ""

listening.sort(key=lambda e: (e["port"], e["proto"], e["inode"]))

# owner is resolved from the full list, before any limit truncation
owner = None
if want_port is not None:
    for e in listening:
        if e["port"] == want_port:
            pi = procinfo.get(e["pid"], {})
            owner = {"port": want_port, "pid": e["pid"], "comm": e["comm"],
                     "cmdline": pi.get("cmdline", "")}
            break

processes = []
for pid in pids:
    pi = procinfo.get(pid)
    if pi is None:
        continue
    if name_filter is not None and name_filter.lower() not in (pi["comm"] + " " + pi["cmdline"]).lower():
        continue
    processes.append({"pid": pid, "ppid": pi["ppid"], "comm": pi["comm"], "cmdline": pi["cmdline"]})

if len(processes) > limit:
    processes = processes[:limit]
    truncated = True
if len(listening) > limit:
    listening = listening[:limit]
    truncated = True

ms = int((time.time() - T0) * 1000)
data = {"listening": listening, "processes": processes, "truncated": truncated, "ms": ms}
if want_port is not None:
    data["owner"] = owner

if want_port is not None:
    if owner and owner["pid"]:
        text = "port %d owned by pid %d (%s)" % (want_port, owner["pid"], owner["comm"] or "?")
    elif owner:
        text = "port %d is listening but the owner pid is hidden" % want_port
    else:
        text = "port %d is not a listening TCP socket" % want_port
else:
    text = "%d listening TCP socket(s), %d process(es)" % (len(listening), len(processes))
    if name_filter is not None:
        text += " matching %r" % name_filter
data["text"] = text

emit(True, data, 1.0 if not skipped else 0.9, [], warnings)
