# text_extract: pull every match of a named pattern (ipv4, email, url, date,
# number, hex, uuid) or a custom regex out of a file or inline text — the
# "extract all X from Y" request. No network, stdlib re only, 4s budget with
# a hard cap on scanned lines and emitted matches; a bad regex fails honestly.
# Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_LINES = 50000
MAX_LINE = 8192
MAX_MATCHES = 500
MAX_UNIQUE = 200

PATTERNS = {
    "ipv4": r"\b(?:\d{1,3}\.){3}\d{1,3}\b",
    "ipv6": r"\b(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}\b",
    "email": r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b",
    "url": r"\bhttps?://[^\s'\"<>)]+",
    "date": r"\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?)?\b",
    "number": r"(?<![\w.])-?\d+(?:\.\d+)?(?![\w.])",
    "hex": r"\b0x[0-9a-fA-F]+\b|\b[0-9a-fA-F]{8,}\b",
    "uuid": r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b",
}


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


kind = arg("pattern")
rx = arg("regex")
if (kind is None) == (rx is None):
    fail("pass exactly one of: pattern (%s) or regex" % "/".join(sorted(PATTERNS)))
if kind is not None:
    kind = str(kind).lower()
    if kind not in PATTERNS:
        fail("unknown pattern %r; one of: %s" % (kind, "/".join(sorted(PATTERNS))))
    source = "pattern:" + kind
    body = PATTERNS[kind]
else:
    rx = str(rx)
    if len(rx) > 512:
        fail("regex too long (max 512 chars)")
    source = "regex:" + rx[:60]
    body = rx
import signal
if not hasattr(signal, "setitimer"):
    fail("regex timeout unavailable on this platform")

flags = re.IGNORECASE if as_bool(ARGS.get("ignore_case"), False) else 0
try:
    rxobj = re.compile(body, flags)
except re.error as e:
    fail("bad regex: %s" % e)
unique_only = as_bool(ARGS.get("unique"), False)


class MatchTimeout(Exception):
    pass


def on_alarm(signum, frame):
    raise MatchTimeout()


signal.signal(signal.SIGALRM, on_alarm)


def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root")
    return p


if "cmd" in ARGS:
    fail("cmd input is not supported")
if arg("file") is not None:
    bounded_path(arg("file"))

text, src, _ = get_input()
limit = int(arg("limit", MAX_MATCHES)) if str(arg("limit", "")).isdigit() else MAX_MATCHES
limit = max(1, min(limit, MAX_MATCHES))

matches, seen, order = [], set(), []
total = lines_hit = scanned = 0
truncated = False
warnings = []
for ln, line in enumerate(text.splitlines(), 1):
    if ln > MAX_LINES:
        truncated = True
        warnings.append("stopped at %d lines" % MAX_LINES)
        break
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d lines" % scanned)
        break
    scanned += 1
    line = line[:MAX_LINE]
    try:
        signal.setitimer(signal.ITIMER_REAL, 0.025)
        found = [m.group(0) for m in rxobj.finditer(line)]
    except MatchTimeout:
        truncated = True
        warnings.append("regex timed out on line %d; skipped" % ln)
        continue
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
    if not found:
        continue
    lines_hit += 1
    for m in found:
        if m == "":
            continue
        total += 1
        if m not in seen:
            seen.add(m)
            order.append(m)
        if len(matches) < limit:
            matches.append({"match": m, "line": ln})
        else:
            truncated = True

if unique_only:
    out = [{"match": m} for m in order[:MAX_UNIQUE]]
    if len(order) > MAX_UNIQUE:
        truncated = True
        warnings.append("%d unique values; showing %d" % (len(order), MAX_UNIQUE))
else:
    out = matches

text_out = "%d match(es) of %s in %s" % (total, source, src)
if total:
    text_out += " (%d unique, %d line(s))" % (len(seen), lines_hit)
    if order:
        text_out += "; first: %s" % clip(order[0], 60)
if truncated:
    text_out += " [truncated]"

emit(True, {"matches": out, "total": total, "unique": len(seen),
            "lines_matched": lines_hit, "lines_scanned": scanned,
            "pattern": kind or None, "regex": None if kind else rx,
            "truncated": truncated, "text": text_out},
     1.0 if not truncated else 0.6, [], warnings)
