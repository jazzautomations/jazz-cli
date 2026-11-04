# mem_top: memory totals from /proc/meminfo and the top-N processes by RSS
# read from /proc/<pid>/statm + comm/cmdline, no free/ps/top. Processes we
# cannot read (EACCES, exited mid-scan) are skipped and counted, never faked.
# Budgeted (4 s), sorted by RSS desc then pid. Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0

if not os.path.exists("/proc/meminfo"):
    fail("no /proc on this system (meminfo/statm need Linux procfs)")

warnings = []


def int_arg(name, default, lo, hi):
    """Integer arg clamped to lo..hi; a non-number fails honestly."""
    v = arg(name, default)
    try:
        v = int(v)
    except (TypeError, ValueError):
        fail("%s must be an integer (got %s)" % (name, clip(v, 40)))
    return lo if v < lo else (hi if v > hi else v)

topn = int_arg("top", 10, 1, 50)
_name = arg("name")
name_filter = str(_name).lower() if _name not in (None, "") else None

# --- /proc/meminfo --------------------------------------------------------
mi = {}
try:
    with open("/proc/meminfo") as f:
        for line in f:
            k, _, v = line.partition(":")
            v = v.strip().split()
            if v:
                try:
                    mi[k.strip()] = int(v[0])  # kB
                except ValueError:
                    pass
except (IOError, OSError) as e:
    fail("cannot read /proc/meminfo: %s" % e)


def mb(kb):
    return None if kb is None else round(kb / 1024.0, 1)


total_kb = mi.get("MemTotal")
if not total_kb:
    fail("MemTotal missing from /proc/meminfo")
avail_kb = mi.get("MemAvailable")
if avail_kb is None:
    # kernels < 3.14: the classic estimate; say so
    avail_kb = mi.get("MemFree", 0) + mi.get("Buffers", 0) + mi.get("Cached", 0)
    warnings.append("MemAvailable missing: estimated as free+buffers+cached")
used_kb = max(0, total_kb - avail_kb)
swap_total_kb = mi.get("SwapTotal")
swap_free_kb = mi.get("SwapFree")
swap_used_kb = None
if swap_total_kb is not None and swap_free_kb is not None:
    swap_used_kb = max(0, swap_total_kb - swap_free_kb)

mem = {"total_mb": mb(total_kb), "available_mb": mb(avail_kb), "used_mb": mb(used_kb),
       "used_pct": round(100.0 * used_kb / total_kb, 1),
       "free_mb": mb(mi.get("MemFree")), "buffers_mb": mb(mi.get("Buffers")),
       "cached_mb": mb(mi.get("Cached")),
       "swap_total_mb": mb(swap_total_kb), "swap_used_mb": mb(swap_used_kb)}

# --- processes by RSS -----------------------------------------------------
try:
    page = os.sysconf("SC_PAGE_SIZE")
except (ValueError, OSError, AttributeError):
    page = 4096
    warnings.append("page size unknown: assuming 4096")

procs = []
scanned = 0
skipped = 0
truncated = False
pids = sorted(int(n) for n in os.listdir("/proc") if n.isdigit())
for pid in pids:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached: process list incomplete")
        break
    try:
        with open("/proc/%d/statm" % pid) as f:
            fields = f.read().split()
        rss_kb = int(fields[1]) * page // 1024
    except (IOError, OSError, IndexError, ValueError):
        skipped += 1  # EACCES (hidepid) or exited between listdir and open
        continue
    scanned += 1
    comm = ""
    try:
        with open("/proc/%d/comm" % pid) as f:
            comm = f.read().strip()
    except (IOError, OSError):
        pass
    cmdline = ""
    try:
        with open("/proc/%d/cmdline" % pid, "rb") as f:
            cmdline = f.read().replace(b"\x00", b" ").decode("utf-8", "replace").strip()
    except (IOError, OSError):
        pass
    if not cmdline:
        cmdline = "[%s]" % (comm or "?")  # kernel thread / zombie
    if name_filter is not None and name_filter not in (comm + " " + cmdline).lower():
        continue
    procs.append((rss_kb, pid, comm or "?", cmdline))

matched = len(procs)
procs.sort(key=lambda p: (-p[0], p[1]))
top = [{"pid": pid, "rss_mb": mb(rss_kb), "comm": comm, "cmdline": clip(cmdline, 160)}
       for rss_kb, pid, comm, cmdline in procs[:topn]]
if skipped:
    warnings.append("%d process(es) skipped (no permission or exited)" % skipped)

# --- text -----------------------------------------------------------------


def gb(kb):
    return "%.1f GB" % (kb / 1048576.0)


text = "%s total, %s available (%d%% used)" % (gb(total_kb), gb(avail_kb), round(mem["used_pct"]))
if swap_used_kb:
    text += ", swap %d MB used" % round(swap_used_kb / 1024.0)
if name_filter is not None:
    text += "; %d process(es) matching %r" % (matched, name_filter)
if top:
    shown = ", ".join("%s %d MB" % (p["comm"], round(p["rss_mb"])) for p in top[:3])
    text += "; top: " + shown + (", …" if len(top) > 3 else "")
elif name_filter is None:
    text += "; no readable processes"
if truncated:
    text += " (truncated)"

ms = int((time.time() - T0) * 1000)
data = {"mem": mem, "top": top, "scanned": scanned, "matched": matched, "skipped": skipped,
        "truncated": truncated, "ms": ms, "text": text}
emit(True, data, 1.0 if not (skipped or truncated) else 0.9, [], warnings)
