# parse_log_lines: counts by level, first/last timestamp and the most frequent messages (deduplicated).
text, source, code = get_input()
lines = text.splitlines()

TS = [
    re.compile(r"(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?)"),
    re.compile(r"(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)"),
    re.compile(r"^((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [ \d]\d \d{2}:\d{2}:\d{2})"),
    re.compile(r"^\[?(\d{2}:\d{2}:\d{2}(?:[.,]\d+)?)\]?\s"),
    re.compile(r"\[(\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4})\]"),
]
# a level is any case when tagged (level=x, [x], <x>, "level":"x") and upper case when bare,
# so prose ("a stack trace") is not mistaken for a TRACE entry
_LV = r"TRACE|DEBUG|INFO|NOTICE|WARNING|WARN|ERROR|ERR|FATAL|CRITICAL|CRIT|PANIC|SEVERE"
LEVEL = re.compile(r"(?:(?i:\blevel=[\"']?|\[|<|\"level\":\s*\")(?P<a>(?i:" + _LV + r"))(?![A-Za-z])|\b(?P<b>" + _LV + r")\b)")
NORM = {"WARNING": "WARN", "ERR": "ERROR", "CRITICAL": "FATAL", "CRIT": "FATAL", "PANIC": "FATAL", "SEVERE": "ERROR",
        "NOTICE": "INFO"}
RANK = {"TRACE": 0, "DEBUG": 1, "INFO": 2, "WARN": 3, "ERROR": 4, "FATAL": 5}
VAR = re.compile(r"0x[0-9a-fA-F]+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F-]{14,}|\b[0-9a-f]{12,}\b|\d+(?:\.\d+)*")
CONT = re.compile(r"^(\s+|Traceback|\tat |Caused by)")

want = str(arg("level", "")).upper()
want = NORM.get(want, want)
if want and want not in RANK:
    fail("level must be one of trace, debug, info, warn, error, fatal")
since = str(arg("since", ""))

counts, msgs, first, last = {}, {}, None, None
explained, kept, nb = 0, 0, 0
unparsed = []
prev_level = None
for l in lines:
    if not l.strip():
        continue
    nb += 1
    ts = None
    for r in TS:
        m = r.search(l[:60])
        if m:
            ts = m.group(1)
            break
    lm = LEVEL.search(l[:120])
    lname = (lm.group("a") or lm.group("b")).upper() if lm else None
    lvl = NORM.get(lname, lname) if lm else None
    if not ts and not lvl:
        if CONT.match(l) and prev_level:
            explained += 1  # continuation of the previous entry (stack trace, wrapped message)
            continue
        if len(unparsed) < MAX_UNPARSED:
            unparsed.append(l)
        continue
    explained += 1
    lvl = lvl or "UNKNOWN"
    prev_level = lvl
    if since and ts and ts < since:
        continue
    if want and RANK.get(lvl, -1) < RANK[want]:
        continue
    kept += 1
    if ts:
        first = first or ts
        last = ts
    counts[lvl] = counts.get(lvl, 0) + 1
    body = l.replace(ts, "", 1) if ts else l
    body = LEVEL.sub("", body, count=1)
    key = VAR.sub("#", body).strip(" -:[]|")[:160]
    e = msgs.get(key)
    if e is None:
        msgs[key] = e = {"level": lvl, "count": 0, "example": clip(l.strip(), 240)}
    e["count"] += 1

if nb == 0:
    fail("empty input")
conf = explained / float(nb)
top = sorted(msgs.values(), key=lambda e: (-RANK.get(e["level"], -1), -e["count"]))[:15]
data = {"lines": nb, "matched": kept, "counts": counts, "first_ts": first, "last_ts": last, "top": top,
        "distinct_messages": len(msgs)}
if explained == 0:
    fail("no timestamps or log levels recognized", data=data, unparsed=unparsed)
emit(True, data, conf, unparsed)
