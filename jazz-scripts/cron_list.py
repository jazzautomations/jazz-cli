# cron_list: every schedule this machine knows, read-only: the user's crontab
# (`crontab -l`), /etc/crontab and /etc/cron.d/* (when readable), the names in
# /etc/cron.{hourly,daily,weekly,monthly} and systemd timers (`systemctl list-timers`).
# Each cron line carries its raw schedule plus a short human reading. The only
# subprocesses are query commands; nothing is ever changed. A crontab-format file
# can be parsed instead via `path` (testing, or a crontab kept in a repo).
# Python 3.8+ stdlib only. No network. Budget ~4 s, then truncated:true.
import shutil
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_FILE = 1 << 20
CMD_CLIP = 80
PERIODIC_DIRS = ["hourly", "daily", "weekly", "monthly"]

warnings = []
truncated = False
permission_denied = False

user_only = bool(arg("user_only", False))
try:
    limit = int(arg("limit", 200))
except (TypeError, ValueError):
    fail("limit must be an integer")
limit = 1 if limit < 1 else (2000 if limit > 2000 else limit)
path_arg = str(arg("path", "") or "")
fmt = str(arg("format", "user") or "user").lower()
if fmt not in ("user", "system"):
    fail("format must be user or system")

# ---------------- schedule -> human ----------------
SPECIALS = {"@reboot": "at boot", "@yearly": "yearly (Jan 1 00:00)", "@annually": "yearly (Jan 1 00:00)",
            "@monthly": "monthly (day 1 00:00)", "@weekly": "weekly (Sun 00:00)", "@daily": "daily at 00:00",
            "@midnight": "daily at 00:00", "@hourly": "hourly at :00"}
DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]
MON = ["", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
DOW_NAMES = {n.lower(): i for i, n in enumerate(DOW[:7])}
MON_NAMES = {n.lower(): i for i, n in enumerate(MON) if n}
RX_FIELD = re.compile(r"^(\*|\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*|[a-z]{3}(?:,[a-z]{3})*|[a-z]{3}-[a-z]{3})(?:/(\d+))?$", re.I)


def _names(field, table):
    """mon/dow names -> numbers so the describer sees one shape."""
    def one(tok):
        t = tok.lower()
        if "-" in t:
            a, b = t.split("-", 1)
            return "%s-%s" % (table.get(a, a), table.get(b, b))
        return str(table.get(t, tok))
    base, _, step = field.partition("/")
    out = ",".join(one(x) for x in base.split(","))
    return out + ("/" + step if step else "")


def _simple(field):
    """(kind, value): ('any',None) '*', ('step',n) '*/n', ('one',n) 'n', ('list',[..]) or ('range',(a,b)) else ('other',s)."""
    if field == "*":
        return "any", None
    m = re.match(r"^\*/(\d+)$", field)
    if m:
        return "step", int(m.group(1))
    if re.match(r"^\d+$", field):
        return "one", int(field)
    m = re.match(r"^(\d+)-(\d+)$", field)
    if m:
        return "range", (int(m.group(1)), int(m.group(2)))
    if re.match(r"^\d+(,\d+)+$", field):
        return "list", [int(x) for x in field.split(",")]
    return "other", field


def _dow_text(kind, val):
    if kind == "one":
        return "on " + DOW[val if val < 8 else 0]
    if kind == "range":
        a, b = val
        if (a, b) == (1, 5):
            return "on weekdays"
        if (a, b) in ((0, 6), (1, 7), (0, 7)):
            return "daily"
        return "on %s-%s" % (DOW[min(a, 7)], DOW[min(b, 7)])
    if kind == "list":
        return "on " + ",".join(DOW[min(d, 7)] for d in val)
    return None


def _time_text(kmi, vmi, kho, vho):
    """the time-of-day half: (text, is_clock) where is_clock = 'at HH:MM' shape."""
    if kmi == "any" and kho == "any":
        return "every minute", False
    if kmi == "step" and kho == "any":
        return "every %d min" % vmi, False
    if kmi == "one" and kho == "any":
        return "hourly at :%02d" % vmi, False
    if kmi == "one" and kho == "step":
        return "every %d h at :%02d" % (vho, vmi), False
    if kmi == "one" and kho == "range":
        return "hourly at :%02d from %02d to %02d h" % (vmi, vho[0], vho[1]), False
    if kmi == "step" and kho == "range":
        return "every %d min from %02d to %02d h" % (vmi, vho[0], vho[1]), False
    if kmi == "one" and kho == "one":
        return "at %02d:%02d" % (vho, vmi), True
    if kmi == "one" and kho == "list":
        return "at " + ",".join("%02d:%02d" % (h, vmi) for h in vho), True
    return None, False


def _days_text(kdom, vdom, kmon, vmon, kdow, vdow):
    """the day half: 'daily', 'weekly on Sun', 'on weekdays', 'monthly on day 1', 'yearly on Jun 15' or None."""
    if kdom == "any" and kmon == "any":
        return "daily" if kdow == "any" else _dow_text(kdow, vdow)
    if kdow != "any":
        return None
    if kmon == "any" and kdom == "one":
        return "monthly on day %d" % vdom
    if kmon == "any" and kdom == "list":
        return "monthly on days " + ",".join(str(x) for x in vdom)
    if kmon == "one" and kdom == "one":
        return "yearly on %s %d" % (MON[vmon] if 0 < vmon < 13 else str(vmon), vdom)
    return None


def human_schedule(sched):
    """'*/5 * * * *' -> 'every 5 min'; '0 3 * * *' -> 'daily at 03:00'; '30 7 * * 1-5' ->
    'on weekdays at 07:30'. Anything the small grammar does not cover is 'custom' (never a guess)."""
    s = sched.strip()
    if s.lower() in SPECIALS:
        return SPECIALS[s.lower()]
    f = s.split()
    if len(f) != 5:
        return "custom"
    for x in f:
        if not RX_FIELD.match(x):
            return "custom"
    mi, ho, dom, mon, dow = f
    kmi, vmi = _simple(mi)
    kho, vho = _simple(ho)
    kdom, vdom = _simple(dom)
    kmon, vmon = _simple(_names(mon, MON_NAMES))
    kdow, vdow = _simple(_names(dow, DOW_NAMES))
    when, clock = _time_text(kmi, vmi, kho, vho)
    days = _days_text(kdom, vdom, kmon, vmon, kdow, vdow)
    if when is None or days is None:
        return "custom"
    if clock:
        if kdow == "one":
            days = "weekly " + days       # "weekly on Sun at 22:45"
        return days + " " + when          # "daily at 03:00", "on weekdays at 07:30"
    if days == "daily":
        return when                       # "every 5 min"
    return when + " " + days              # "every 10 min from 08 to 18 h on weekdays"


# ---------------- crontab parsing ----------------
RX_SPECIAL = re.compile(r"^(@\w+)\s+(.*)$")
RX_USER = re.compile(r"^[A-Za-z_][\w.@-]*$")


def parse_crontab(text, with_user, source):
    """Lines of a crontab: (entries, env). with_user: the system format (user before the command)."""
    entries, env = [], {}
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        m = re.match(r"^([A-Za-z_]\w*)\s*=\s*(.*)$", line)
        if m and not line.startswith("*") and not line[0].isdigit() and not line.startswith("@"):
            env[m.group(1)] = m.group(2).strip().strip('"').strip("'")
            continue
        m = RX_SPECIAL.match(line)
        if m:
            sched, rest = m.group(1), m.group(2)
        else:
            f = line.split(None, 5)
            if len(f) < 6:
                continue
            sched, rest = " ".join(f[:5]), f[5]
        user = None
        if with_user:
            parts = rest.split(None, 1)
            if len(parts) == 2 and RX_USER.match(parts[0]):
                user, rest = parts[0], parts[1]
            elif len(parts) == 1 and RX_USER.match(parts[0]):
                user, rest = parts[0], ""
        e = {"schedule": sched, "human": human_schedule(sched), "command": clip(rest.strip(), CMD_CLIP),
             "source": source}
        if with_user:
            e["user"] = user
        entries.append(e)
    return entries, env


def read_small(p):
    """text of a small file, or (None, reason)."""
    try:
        if os.path.getsize(p) > MAX_FILE:
            return None, "too big"
        with open(p, "rb") as fh:
            return fh.read().decode("utf-8", "replace"), None
    except (IOError, OSError) as e:
        return None, str(e)


def over_budget():
    return time.time() - T0 > BUDGET_S


crontab, system, timers = [], [], []
periodic = {k: [] for k in PERIODIC_DIRS}
sources = []

# ---------------- a file given: parse it and stop (no system queries) ----------------
if path_arg:
    p = path_in_root(path_arg)
    if not os.path.isfile(p):
        fail("file not found: %s" % rel(p))
    text, err = read_small(p)
    if text is None:
        fail("cannot read %s: %s" % (rel(p), err))
    ents, env = parse_crontab(text, fmt == "system", "file:" + rel(p))
    if fmt == "system":
        system = ents
    else:
        crontab = ents
    sources.append("file:" + rel(p))
else:
    # ---------------- user crontab ----------------
    if shutil.which("crontab"):
        code, out, err = run_argv(["crontab", "-l"], timeout=3)
        low = (err or "").lower()
        if code == 124:
            warnings.append("crontab -l timed out")
            truncated = True
        elif code == 0:
            crontab, _ = parse_crontab(out, False, "crontab")
            sources.append("crontab")
        elif "no crontab for" in low or "no crontab" in low:
            sources.append("crontab")  # empty is a valid answer, not an error
        elif "not allowed" in low or "permission" in low or "must be privileged" in low:
            permission_denied = True
            warnings.append("crontab -l: " + clip(err.strip().splitlines()[-1] if err.strip() else "permission denied", 120))
        else:
            warnings.append("crontab -l failed: " + clip((err or out).strip().splitlines()[-1] if (err or out).strip() else "rc %d" % code, 120))
    else:
        warnings.append("crontab not installed")

    if not user_only:
        # ---------------- /etc/crontab and /etc/cron.d ----------------
        files = []
        if os.path.isfile("/etc/crontab"):
            files.append("/etc/crontab")
        if os.path.isdir("/etc/cron.d"):
            try:
                for n in sorted(os.listdir("/etc/cron.d")):
                    if n.startswith(".") or n.endswith("~") or "." in n and n.rsplit(".", 1)[1] in ("dpkg-dist", "dpkg-old", "rpmsave", "rpmnew", "disabled"):
                        continue  # cron itself skips these names
                    fp = os.path.join("/etc/cron.d", n)
                    if os.path.isfile(fp):
                        files.append(fp)
            except OSError as e:
                permission_denied = True
                warnings.append("/etc/cron.d: " + str(e))
        for fp in files:
            if over_budget():
                truncated = True
                break
            text, err = read_small(fp)
            if text is None:
                if "ermission" in (err or ""):
                    permission_denied = True
                warnings.append("%s: %s" % (fp, err))
                continue
            ents, _ = parse_crontab(text, True, fp)
            system.extend(ents)
            sources.append(fp)

        # ---------------- /etc/cron.{hourly,daily,weekly,monthly}: names only ----------------
        for k in PERIODIC_DIRS:
            d = "/etc/cron." + k
            if not os.path.isdir(d):
                continue
            try:
                names = sorted(n for n in os.listdir(d) if not n.startswith(".") and n != "placeholder"
                               and os.path.isfile(os.path.join(d, n)))
            except OSError as e:
                permission_denied = True
                warnings.append("%s: %s" % (d, e))
                continue
            periodic[k] = names
            sources.append(d)

        # ---------------- systemd timers ----------------
        if shutil.which("systemctl") and not over_budget():
            left = max(0.5, BUDGET_S + 0.5 - (time.time() - T0))
            code, out, err = run_argv(["systemctl", "list-timers", "--all", "--no-pager", "--no-legend"],
                                      timeout=min(3, left))
            if code == 124:
                warnings.append("systemctl list-timers timed out")
                truncated = True
            elif code != 0 and not out.strip():
                warnings.append("systemctl list-timers: " + clip((err or "rc %d" % code).strip().splitlines()[-1], 120))
            else:
                # NEXT LEFT LAST PASSED UNIT ACTIVATES; dates look like "Wed 2026-11-04 18:10:00 -03"
                RX_DATE = r"(?:[A-Z][a-z]{2} \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?: \S+)?|-|n/a)"
                rx = re.compile(r"^\s*(?P<next>" + RX_DATE + r")\s+(?P<left>.+?)\s+(?P<last>" + RX_DATE +
                                r")\s+(?P<passed>.+?)\s+(?P<unit>\S+\.timer)\s+(?P<act>\S*)\s*$")
                for line in out.splitlines():
                    if not line.strip():
                        continue
                    m = rx.match(line)
                    if not m:
                        toks = line.split()
                        if len(toks) >= 2 and toks[-2].endswith(".timer"):
                            timers.append({"unit": toks[-2], "next": None, "left": None, "last": None,
                                           "activates": toks[-1] or None})
                        continue
                    nxt, lft, lst = m.group("next"), m.group("left").strip(), m.group("last")
                    timers.append({"unit": m.group("unit"),
                                   "next": None if nxt in ("-", "n/a") else nxt,
                                   "left": None if lft in ("-", "n/a") else lft.replace(" left", ""),
                                   "last": None if lst in ("-", "n/a") else lst,
                                   "activates": m.group("act") or None})
                sources.append("systemd")
        # no systemctl here: timers stay empty, silently (not every box runs systemd)

# ---------------- output ----------------
crontab.sort(key=lambda e: (e["schedule"], e["command"]))
system.sort(key=lambda e: (e["source"], e["schedule"], e.get("user") or "", e["command"]))
# timers: soonest first (list-timers already sorts by NEXT), the ones with no next run last by name
with_next = [t for t in timers if t["next"]]
without = sorted((t for t in timers if not t["next"]), key=lambda t: t["unit"])
timers = with_next + without
n_cron, n_sys, n_tim = len(crontab), len(system), len(timers)
if n_cron + n_sys + n_tim > limit:
    truncated = True
    room = limit
    crontab, room = crontab[:room], max(0, room - len(crontab))
    system, room = system[:room], max(0, room - len(system))
    timers = timers[:room]

parts = ["user crontab %d job%s" % (n_cron, "" if n_cron == 1 else "s")]
if not user_only and not path_arg:
    parts.append("system %d" % n_sys)
    tx = "%d timers" % n_tim
    if with_next:
        t0 = with_next[0]
        tx += " (next: %s in %s)" % (t0["unit"][:-6] if t0["unit"].endswith(".timer") else t0["unit"],
                                      t0["left"] or "?")
    parts.append(tx)
    np = sum(len(v) for v in periodic.values())
    if np:
        parts.append("%d periodic scripts" % np)
elif path_arg:
    parts = ["%s: %d job%s" % (rel(path_in_root(path_arg)), n_cron + n_sys, "" if n_cron + n_sys == 1 else "s")]
text = ", ".join(parts)
if permission_denied:
    text += " (some sources not readable)"
if truncated:
    text += " (truncated)"

data = {"crontab": crontab, "system": system, "timers": timers, "periodic": periodic,
        "sources": sources, "truncated": truncated, "permission_denied": permission_denied,
        "ms": int((time.time() - T0) * 1000), "text": text}
conf = 0.9 if sources else 0.4
emit(True, data, conf, [], warnings)
