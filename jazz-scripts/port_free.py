# port_free: bind test (definitive) plus who listens (ss, lsof or netstat, whichever exists).
import shutil
import socket

port = int(arg("port", 0))
if port < 1 or port > 65535:
    fail("port must be 1..65535")
hosts = [str(arg("host"))] if arg("host") else ["127.0.0.1", "0.0.0.0"]


def can_bind(h):
    fam = socket.AF_INET6 if ":" in h else socket.AF_INET
    s = socket.socket(fam, socket.SOCK_STREAM)
    try:
        # no SO_REUSEADDR: a port in TIME_WAIT still counts as busy, like a server would see it
        s.bind((h, port))
        return True, ""
    except socket.error as e:
        return False, str(e)
    finally:
        s.close()


free, errors, denied = True, [], False
for h in hosts:
    ok, err = can_bind(h)
    if not ok:
        if "Permission denied" in err or "Errno 13" in err:
            denied = True  # privileged port without root: the bind test says nothing
        else:
            free = False
        errors.append("%s: %s" % (h, err))

listeners, tool, warn = [], "", []
if shutil.which("ss"):
    tool = "ss"
    code, out, _ = run_argv(["ss", "-Hltnp", "sport = :%d" % port], timeout=10)
    for l in out.splitlines():
        parts = l.split()
        addr = parts[3] if len(parts) > 3 else ""
        for m in re.finditer(r'\("([^"]+)",pid=(\d+)', l):
            listeners.append({"pid": int(m.group(2)), "cmd": m.group(1), "addr": addr})
        if not re.search(r"pid=\d+", l) and addr:
            listeners.append({"pid": 0, "cmd": "", "addr": addr})
elif shutil.which("lsof"):
    tool = "lsof"
    code, out, _ = run_argv(["lsof", "-nP", "-iTCP:%d" % port, "-sTCP:LISTEN", "-Fpcn"], timeout=10)
    cur = {}
    for l in out.splitlines():
        if l.startswith("p"):
            cur = {"pid": int(l[1:] or 0), "cmd": "", "addr": ""}
            listeners.append(cur)
        elif l.startswith("c") and cur:
            cur["cmd"] = l[1:]
        elif l.startswith("n") and cur:
            cur["addr"] = l[1:]
elif shutil.which("netstat"):
    tool = "netstat"
    code, out, _ = run_argv(["netstat", "-ltnp"], timeout=10)
    for l in out.splitlines():
        parts = l.split()
        if len(parts) >= 4 and re.search(r"[:.]%d$" % port, parts[3]) and "LISTEN" in l:
            pid, cmd = 0, ""
            m = re.search(r"(\d+)/(\S+)", l)
            if m:
                pid, cmd = int(m.group(1)), m.group(2)
            listeners.append({"pid": pid, "cmd": cmd, "addr": parts[3]})
else:
    warn.append("ss, lsof and netstat are missing: owner unknown")
conf = 1.0
if denied:
    # only the listener table can answer for a privileged port
    free = free and not listeners
    warn.append("port %d is privileged: bind test needs root; answer comes from %s" % (port, tool or "nothing"))
    conf = 0.9 if tool else 0.3
if not free and listeners and all(x["pid"] == 0 for x in listeners):
    warn.append("the owner pid is hidden (another user's process)")
data = {"port": port, "free": free, "listeners": listeners[:10], "tool": tool}
if errors:
    data["bind_errors"] = errors
emit(True, data, conf, [], warn)
