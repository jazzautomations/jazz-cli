# log_errors_recent: recent errors from the system journal or from a log file,
# grouped by normalized message (numbers, hex, paths, ips -> <n>, <hex>, <path>, <ip>)
# with counts, first/last occurrence and the unit/process. Read-only: the only
# subprocess is `journalctl` (a query); files are read by tail (2 MB), never written.
# Python 3.8+ stdlib only. No network. Wall-clock budget ~4 s, then truncated:true.
import datetime
import shutil
import time

T0 = time.time()
BUDGET_S = 4.0
TAIL_BYTES = 2 << 20      # how much of a file is read (its end)
MAX_LINES = 60000         # hard cap on lines considered, then truncated
SYSLOG_FILES = ["/var/log/syslog", "/var/log/messages"]

warnings = []

# ---------------- args ----------------
try:
    minutes = int(arg("minutes", 60))
except (TypeError, ValueError):
    fail("minutes must be an integer")
minutes = 0 if minutes < 0 else (10080 if minutes > 10080 else minutes)  # 0 = no window (whole file tail)
source = str(arg("source", "auto") or "auto").lower()
if source not in ("auto", "journal", "file"):
    fail("source must be auto, journal or file")
path_arg = str(arg("path", "") or "")
try:
    limit = int(arg("limit", 50))
except (TypeError, ValueError):
    fail("limit must be an integer")
limit = 1 if limit < 1 else (500 if limit > 500 else limit)
min_level = str(arg("min_level", "err") or "err").lower()
min_level = {"error": "err", "warn": "warning"}.get(min_level, min_level)
if min_level not in ("err", "warning"):
    fail("min_level must be err or warning")
if source == "file" and not path_arg:
    fail("path is required when source is file")
if path_arg and source == "auto":
    source = "file"  # a path given = the user means that file
if minutes == 0 and source != "file":
    minutes = 10080  # the journal always gets a window: 7 days at most

# ---------------- normalization ----------------
# order matters: paths hold digits, uuids hold hex, ips are digits
NORM_RULES = [
    (re.compile(r"(?<![\w.])/(?:[\w.@+-]+/)*[\w.@+-]*"), "<path>"),
    (re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b"), "<hex>"),
    (re.compile(r"\b0x[0-9a-fA-F]+\b|\b[0-9a-f]{8,}\b|\b[0-9A-F]{8,}\b"), "<hex>"),
    (re.compile(r"\b\d{1,3}(?:\.\d{1,3}){3}\b"), "<ip>"),
    (re.compile(r"\d+(?:[.,]\d+)*"), "<n>"),
]


_norm_cache = {}


def normalize(msg):
    s = _norm_cache.get(msg)
    if s is not None:
        return s
    s = msg
    for rx, rep in NORM_RULES:
        s = rx.sub(rep, s)
    s = re.sub(r"\s+", " ", s).strip()[:160]
    if len(_norm_cache) < 20000:
        _norm_cache[msg] = s
    return s


# ---------------- timestamps ----------------
MONTHS = {m: i + 1 for i, m in enumerate("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split())}
RX_ISO = re.compile(r"(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(?:[.,]\d+)?(Z|[+-]\d{2}:?\d{2})?")
RX_SLASH = re.compile(r"(\d{4})/(\d{2})/(\d{2}) (\d{2}):(\d{2}):(\d{2})")
RX_SYSLOG = re.compile(r"^([A-Z][a-z]{2}) {1,2}(\d{1,2}) (\d{2}):(\d{2}):(\d{2})")
RX_CLF = re.compile(r"\[(\d{2})/([A-Z][a-z]{2})/(\d{4}):(\d{2}):(\d{2}):(\d{2}) ([+-]\d{4})\]")
NOW = datetime.datetime.now(datetime.timezone.utc)
LOCAL_TZ = datetime.datetime.now().astimezone().tzinfo


def _tz(s):
    if not s:
        return LOCAL_TZ
    if s == "Z":
        return datetime.timezone.utc
    sign = 1 if s[0] == "+" else -1
    digits = s[1:].replace(":", "")
    return datetime.timezone(sign * datetime.timedelta(hours=int(digits[:2]), minutes=int(digits[2:4] or 0)))


def parse_ts(line):
    """(iso-ish text as found, aware datetime or None, rest of line) for the leading timestamp."""
    head = line[:64]
    m = RX_ISO.search(head)
    if m:
        try:
            dt = datetime.datetime(int(m.group(1)), int(m.group(2)), int(m.group(3)), int(m.group(4)),
                                   int(m.group(5)), int(m.group(6)), tzinfo=_tz(m.group(7)))
        except ValueError:
            dt = None
        return m.group(0), dt, (line[:m.start()] + line[m.end():]).strip()
    m = RX_SLASH.search(head)
    if m:
        try:
            dt = datetime.datetime(*[int(g) for g in m.groups()], tzinfo=LOCAL_TZ)
        except ValueError:
            dt = None
        return m.group(0), dt, (line[:m.start()] + line[m.end():]).strip()
    m = RX_SYSLOG.match(head)
    if m and m.group(1) in MONTHS:
        year = datetime.datetime.now().year
        try:
            dt = datetime.datetime(year, MONTHS[m.group(1)], int(m.group(2)), int(m.group(3)), int(m.group(4)),
                                   int(m.group(5)), tzinfo=LOCAL_TZ)
            if dt > NOW + datetime.timedelta(days=1):
                dt = dt.replace(year=year - 1)  # no year in syslog: a future date means last year
        except ValueError:
            dt = None
        return m.group(0), dt, line[m.end():].strip()
    m = RX_CLF.search(head)
    if m and m.group(2) in MONTHS:
        try:
            dt = datetime.datetime(int(m.group(3)), MONTHS[m.group(2)], int(m.group(1)), int(m.group(4)),
                                   int(m.group(5)), int(m.group(6)), tzinfo=_tz(m.group(7)))
        except ValueError:
            dt = None
        return m.group(0), dt, (line[:m.start()] + line[m.end():]).strip()
    return None, None, line.strip()


# ---------------- levels ----------------
_LV = r"TRACE|DEBUG|INFO|NOTICE|WARNING|WARN|ERROR|ERR|FATAL|CRITICAL|CRIT|PANIC|SEVERE|EMERG|ALERT"
LEVEL = re.compile(r"(?:(?i:\blevel=[\"']?|\[|<|\"level\":\s*\")(?P<a>(?i:" + _LV + r"))(?![A-Za-z])|\b(?P<b>" + _LV + r")\b)")
LEVEL_NORM = {"WARNING": "warning", "WARN": "warning", "ERROR": "err", "ERR": "err", "SEVERE": "err",
              "FATAL": "crit", "CRITICAL": "crit", "CRIT": "crit", "PANIC": "crit", "EMERG": "crit", "ALERT": "crit"}
KEEP = {"crit": 0, "err": 1, "warning": 2}
MIN_RANK = KEEP[min_level]
# syslog-style files carry no priority: a keyword heuristic stands in (confidence drops)
KW_ERR = re.compile(r"\b(error|errors|failed|failure|fatal|critical|panic|segfault|denied|refused|oops|traceback|exception)\b", re.I)
KW_SKIP = re.compile(r": (Starting|Started|Finished|Stopping|Stopped|Reached target) ")
KW_WARN = re.compile(r"\b(warn|warning|deprecated|retry|retrying|timeout|timed out)\b", re.I)
RX_HEAD_LEVEL = re.compile(r"^(" + _LV + r")\b\s*[:\-]\s*", re.I)
# "unit[pid]: msg" / "unit: msg" (journal short-iso and syslog, after the host token)
RX_UNIT = re.compile(r"^(?P<unit>[\w.@/-]+?)(?:\[(?P<pid>\d+)\])?:(?: (?P<msg>.*))?$")
RX_HOSTED = re.compile(r"^[\w.-]+ [\w.@/-]+?(?:\[\d+\])?:(?: .*)?$")


def level_of(body):
    m = LEVEL.search(body[:120])
    if not m:
        return None, body
    name = (m.group("a") or m.group("b")).upper()
    return LEVEL_NORM.get(name, "info"), (body[:m.start()] + body[m.end():]).strip(" -:[]|")


def level_by_keyword(body):
    if KW_SKIP.search(body):
        return None  # "Starting x.service - Check for errors" is a systemd title, not an error
    if KW_ERR.search(body):
        return "err"
    if KW_WARN.search(body):
        return "warning"
    return None


def split_unit(body, hosted):
    """unit/process name and message from a journal/syslog body ('host unit[pid]: msg').
    hosted: the first token is the hostname and is dropped (never shown in a sample);
    a continuation line of a multi-line entry has no unit and keeps its text."""
    if hosted:
        parts = body.split(" ", 1)
        body = parts[1] if len(parts) == 2 else ""
    m = RX_UNIT.match(body)
    if not m:
        return None, body.strip()
    return m.group("unit"), (m.group("msg") or "").strip()


# ---------------- grouping ----------------
groups = {}
total_lines = 0
truncated = False
permission_denied = False


def over_budget():
    return time.time() - T0 > BUDGET_S


def add(level, unit, msg, ts_text, dt):
    if not msg:
        return  # continuation line of a multi-line entry ("kernel:" alone)
    key = (unit or "", normalize(msg))
    g = groups.get(key)
    sort_ts = dt.isoformat() if dt else (ts_text or "")
    if g is None:
        g = groups[key] = {"count": 0, "unit": unit, "level": level, "sample": key[1],
                           "example": clip(msg, 200), "first": ts_text, "last": ts_text, "_f": sort_ts, "_l": sort_ts}
    g["count"] += 1
    if KEEP.get(level, 9) < KEEP.get(g["level"], 9):
        g["level"] = level
    if sort_ts and (not g["_f"] or sort_ts < g["_f"]):
        g["_f"], g["first"] = sort_ts, ts_text
    if sort_ts and (not g["_l"] or sort_ts > g["_l"]):
        g["_l"], g["last"] = sort_ts, ts_text


# ---------------- journal ----------------
def journal_denied(err):
    e = err.lower()
    return ("not seeing messages" in e or "permission denied" in e or "no journal files were opened" in e
            or "not permitted" in e or "access denied" in e)


def read_journal():
    """Returns (lines_seen, ok). Each priority band is one query so every line has a true level."""
    global truncated, permission_denied
    bands = [("crit", "emerg..crit"), ("err", "err..err")]
    if MIN_RANK >= KEEP["warning"]:
        bands.append(("warning", "warning..warning"))
    seen = 0
    any_ok = False
    for level, prio in bands:
        if over_budget():
            truncated = True
            break
        left = max(0.5, BUDGET_S + 1.0 - (time.time() - T0))
        argv = ["journalctl", "-p", prio, "--since", "-%dmin" % minutes, "--no-pager", "-o", "short-iso"]
        code, out, err = run_argv(argv, timeout=min(5, left))
        if code == 124:
            truncated = True
            warnings.append("journalctl timed out (%s)" % prio)
            continue
        if journal_denied(err):
            permission_denied = True
        if code != 0 and not out.strip():
            if not permission_denied and err.strip():
                warnings.append("journalctl: " + clip(err.strip().splitlines()[-1], 120))
            continue
        any_ok = True
        for line in out.splitlines():
            if not line or line.startswith("-- "):
                continue  # "-- No entries --", "-- Boot ... --"
            seen += 1
            if seen > MAX_LINES or (seen % 500 == 0 and over_budget()):
                truncated = True
                warnings.append("journal: stopped after %d lines (budget)" % (seen - 1))
                break
            ts_text, dt, body = parse_ts(line)
            unit, msg = split_unit(body, hosted=True)
            add(level, unit, msg, ts_text, dt)
    return seen, any_ok


# ---------------- file ----------------
def read_file(p, since_dt):
    """Returns (lines_seen, levelled, keyword_fallback). Lines at or above min_level inside the window."""
    global truncated
    try:
        text = read_text(p, TAIL_BYTES)
    except (IOError, OSError) as e:
        return None, False, False, str(e)
    lines = text.splitlines()
    if len(text) >= TAIL_BYTES and lines:
        lines = lines[1:]  # the first line of a tail is usually cut in half
        truncated = True
    # first pass over a sample: does the file carry levels at all (else keyword fallback,
    # said so in warnings) and is it syslog-shaped ("host unit[pid]: msg", host dropped)?
    sample = [l for l in lines[:2000] if l.strip()]
    has_levels = any(LEVEL.search(l[:120]) for l in sample)
    probe = sample[:200]
    hosted = bool(probe) and sum(1 for l in probe if RX_HOSTED.match(parse_ts(l)[2])) * 2 >= len(probe)
    seen = 0
    no_ts = 0
    for line in lines:
        if not line.strip():
            continue
        if seen >= MAX_LINES or over_budget():
            truncated = True
            break
        seen += 1
        ts_text, dt, body = parse_ts(line)
        if dt is None:
            no_ts += 1
        elif since_dt is not None and dt < since_dt:
            continue
        if has_levels:
            level, body2 = level_of(body)
            unit, msg = split_unit(body2, hosted)
            if level is None:
                m = RX_HEAD_LEVEL.match(msg)  # "sshd[1]: error: ..." (lowercase prefix, no tag)
                if not m:
                    continue
                level = LEVEL_NORM.get(m.group(1).upper(), "info")
                msg = msg[m.end():].strip()
        else:
            level = level_by_keyword(body)
            unit, msg = split_unit(body, hosted)
        if level is None or KEEP.get(level, 9) > MIN_RANK:
            continue
        add(level, unit, msg, ts_text, dt)
    if no_ts and no_ts == seen and since_dt is not None:
        warnings.append("no timestamps in %s: window not applied" % rel(p))
    return seen, has_levels, not has_levels, None


since_dt = (NOW - datetime.timedelta(minutes=minutes)) if minutes else None
source_used = None
confidence = 0.3
journal_seen = 0

if source in ("auto", "journal"):
    if shutil.which("journalctl"):
        journal_seen, ok = read_journal()
        if ok and not permission_denied:
            source_used = "journal"
            total_lines = journal_seen
            confidence = 0.9
        elif permission_denied:
            warnings.append("journal not fully readable (not in systemd-journal/adm group?); trying syslog files")
            if ok and journal_seen:
                warnings.append("journal: %d entries visible from this user only" % journal_seen)
    elif source == "journal":
        fail("journalctl not installed", data={"source_used": None, "window_min": minutes,
                                               "permission_denied": False})
    else:
        warnings.append("journalctl not installed; trying syslog files")

if source == "file":
    p = path_in_root(path_arg)
    if not os.path.isfile(p):
        fail("file not found: %s" % rel(p), data={"source_used": None, "window_min": minutes,
                                                   "permission_denied": False})
    seen, levelled, kw, err = read_file(p, since_dt)
    if err is not None:
        permission_denied = permission_denied or ("ermission" in err)
        fail("cannot read file: %s" % err, data={"source_used": None, "window_min": minutes,
                                                   "permission_denied": permission_denied})
    source_used = "file:" + rel(p)
    total_lines = seen
    confidence = 0.85 if levelled else 0.6
    if kw:
        warnings.append("%s has no log levels: errors matched by keywords (error/failed/fatal/denied...)" % rel(p))
elif source_used is None:
    # journal unusable (absent or not readable): the classic syslog files, first readable one wins
    for p in SYSLOG_FILES:
        if not os.path.isfile(p):
            continue
        seen, levelled, kw, err = read_file(p, since_dt)
        if err is not None:
            if "ermission" in err:
                permission_denied = True
            continue
        source_used = "file:" + p
        total_lines = seen
        confidence = 0.6
        warnings.append("%s has no priorities: errors matched by keywords (error/failed/fatal/denied...)" % p)
        break
    if source_used is None:
        if permission_denied:
            warnings.append("no readable error source: journal and syslog files need more permissions")
        else:
            warnings.append("no error source found (no journal, no /var/log/syslog or /var/log/messages)")

# ---------------- output ----------------
rows = sorted(groups.values(), key=lambda g: (-g["count"], KEEP.get(g["level"], 9), g["unit"] or "", g["sample"]))
distinct = len(rows)
if len(rows) > limit:
    rows = rows[:limit]
    truncated = True
for g in rows:
    g.pop("_f", None)
    g.pop("_l", None)
matched = sum(g["count"] for g in groups.values())
label = "error" if min_level == "err" else "warning/error"
window_txt = ("the last %d min" % minutes) if minutes else "the whole file"

if source_used is None:
    text = "no readable error source"
elif matched == 0:
    text = "no %ss in %s" % (label, window_txt)
else:
    parts = []
    for g in rows[:3]:
        who = g["unit"] or "?"
        parts.append("%s ×%d '%s'" % (who, g["count"], clip(g["sample"], 48)))
    text = "%d %s lines in %s, %d distinct: %s" % (matched, label, window_txt, distinct, ", ".join(parts))
    if distinct > 3:
        text += ", …"
if truncated:
    text += " (truncated)"

data = {"source_used": source_used, "window_min": minutes, "min_level": min_level, "groups": rows,
        "matched": matched, "distinct": distinct, "total_lines": total_lines, "truncated": truncated,
        "permission_denied": permission_denied, "ms": int((time.time() - T0) * 1000), "text": text}
emit(True, data, confidence, [], warnings)
