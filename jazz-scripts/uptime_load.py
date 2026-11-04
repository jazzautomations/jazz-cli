# uptime_load: uptime, load average and CPU busy % read straight from
# /proc/uptime, /proc/loadavg and /proc/stat (two samples, args.sample_ms
# apart), no uptime/top/mpstat. Unknown values are null, never a fake 0.
# Python 3.8+ stdlib only.
import time

T0 = time.time()

if not os.path.exists("/proc/uptime"):
    fail("no /proc on this system (uptime/loadavg need Linux procfs)")

warnings = []


def int_arg(name, default, lo, hi):
    """Integer arg clamped to lo..hi; a non-number fails honestly."""
    v = arg(name, default)
    try:
        v = int(v)
    except (TypeError, ValueError):
        fail("%s must be an integer (got %s)" % (name, clip(v, 40)))
    return lo if v < lo else (hi if v > hi else v)

sample_ms = int_arg("sample_ms", 200, 50, 1000)


def read_first_line(path):
    try:
        with open(path) as f:
            return f.readline().strip()
    except (IOError, OSError) as e:
        warnings.append("cannot read %s: %s" % (path, e.__class__.__name__))
        return None


# --- uptime ---------------------------------------------------------------
uptime_s = None
line = read_first_line("/proc/uptime")
if line:
    try:
        uptime_s = float(line.split()[0])
    except (IndexError, ValueError):
        warnings.append("unexpected /proc/uptime format")
if uptime_s is None:
    fail("cannot read uptime from /proc/uptime", warnings=warnings)


def uptime_text(secs, units=3):
    """'3d 4h 12m' (up to `units` leading non-zero units); '<1m' under a minute."""
    secs = int(secs)
    d, rem = divmod(secs, 86400)
    h, rem = divmod(rem, 3600)
    m = rem // 60
    parts = []
    for v, u in ((d, "d"), (h, "h"), (m, "m")):
        if v or parts:
            parts.append("%d%s" % (v, u))
    if not parts:
        return "<1m"
    return " ".join(parts[:units])


# --- load average + process counts ---------------------------------------
load = None
procs = {"running": None, "total": None}
line = read_first_line("/proc/loadavg")
if line:
    parts = line.split()
    try:
        load = {"1": float(parts[0]), "5": float(parts[1]), "15": float(parts[2])}
    except (IndexError, ValueError):
        warnings.append("unexpected /proc/loadavg format")
    try:
        r, t = parts[3].split("/")
        procs = {"running": int(r), "total": int(t)}
    except (IndexError, ValueError):
        pass

# --- cpu count ------------------------------------------------------------
cpus = os.cpu_count()


def read_cpu_line():
    """Aggregate 'cpu' jiffies from /proc/stat as a list of ints, or None."""
    try:
        with open("/proc/stat") as f:
            for l in f:
                if l.startswith("cpu "):
                    return [int(x) for x in l.split()[1:]]
    except (IOError, OSError, ValueError):
        return None
    return None


if cpus is None:
    # fall back to counting cpuN lines
    try:
        with open("/proc/stat") as f:
            n = sum(1 for l in f if l.startswith("cpu") and not l.startswith("cpu "))
        cpus = n or None
    except (IOError, OSError):
        cpus = None

# --- cpu % over two samples ----------------------------------------------
cpu_pct = {"user": None, "system": None, "idle": None, "iowait": None, "busy": None}
a = read_cpu_line()
if a is None:
    warnings.append("cannot read /proc/stat: cpu_pct unknown")
else:
    time.sleep(sample_ms / 1000.0)
    b = read_cpu_line()
    if b is None or len(a) < 5 or len(b) < 5:
        warnings.append("cannot sample /proc/stat twice: cpu_pct unknown")
    else:
        # user nice system idle iowait irq softirq steal [guest guest_nice]
        n = min(len(a), len(b), 8)
        d = [b[i] - a[i] for i in range(n)]
        total = sum(d)
        if total <= 0:
            warnings.append("no cpu ticks elapsed in %d ms: cpu_pct unknown" % sample_ms)
        else:
            def pct(x):
                return round(100.0 * x / total, 1)
            user = d[0] + d[1]
            system = d[2] + (d[5] if n > 5 else 0) + (d[6] if n > 6 else 0) + (d[7] if n > 7 else 0)
            idle, iowait = d[3], d[4]
            cpu_pct = {"user": pct(user), "system": pct(system), "idle": pct(idle),
                       "iowait": pct(iowait), "busy": pct(total - idle - iowait)}

# --- text -----------------------------------------------------------------
text = "up " + uptime_text(uptime_s, units=2)
if load:
    text += ", load %.2f/%.2f/%.2f" % (load["1"], load["5"], load["15"])
else:
    text += ", load unknown"
if cpus:
    text += " on %d cpu%s" % (cpus, "" if cpus == 1 else "s")
if cpu_pct["busy"] is not None:
    text += " (%d%% busy)" % round(cpu_pct["busy"])
elif cpus and load:
    text += " (cpu % unknown)"

ms = int((time.time() - T0) * 1000)
data = {"uptime_s": int(uptime_s), "uptime_text": uptime_text(uptime_s), "load": load,
        "cpus": cpus, "cpu_pct": cpu_pct, "procs": procs, "sample_ms": sample_ms,
        "ms": ms, "text": text}
emit(True, data, 1.0 if not warnings else 0.8, [], warnings)
