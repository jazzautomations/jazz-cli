# text_count: how many times a literal string or regex occurs in a file or
# inline text — "how often does 'error' appear", "count matches". Reports
# occurrences, matching lines and the densest lines; regex gets the same
# 25 ms per-line guard as regex_test. Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0
MAX_LINES = 200000
MAX_LINE = 8192
TOP_LINES = 5

pat = arg("pattern")
if pat is None:
    fail("pass pattern (the text to count)")
pat = str(pat)
if not pat:
    fail("pattern must not be empty")
if len(pat) > 512:
    fail("pattern too long (max 512 chars)")


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


is_regex = as_bool(ARGS.get("regex"), False)
ignore_case = as_bool(ARGS.get("ignore_case"), False)

import signal
rx = None
if is_regex:
    if not hasattr(signal, "setitimer"):
        fail("regex timeout unavailable on this platform")
    flags = re.IGNORECASE if ignore_case else 0
    try:
        rx = re.compile(pat, flags)
    except re.error as e:
        fail("bad regex: %s" % e)


class MatchTimeout(Exception):
    pass


def on_alarm(signum, frame):
    raise MatchTimeout()


if is_regex:
    signal.signal(signal.SIGALRM, on_alarm)


def bounded_path(value):
    p = os.path.realpath(path_in_root(value))
    if os.path.commonpath([os.path.realpath(ROOT), p]) != os.path.realpath(ROOT):
        fail("path outside project root — use jazz -C <dir> to run there, or pass a path inside the project")
    return p


if "cmd" in ARGS:
    fail("cmd input is not supported")
if arg("file") is not None:
    bounded_path(arg("file"))

needle = pat.lower() if ignore_case else pat
text, src, _ = get_input()

total = lines_hit = scanned = 0
per_line = []
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
    if rx is not None:
        try:
            signal.setitimer(signal.ITIMER_REAL, 0.025)
            n = sum(1 for m in rx.finditer(line) if m.group(0) != "")
        except MatchTimeout:
            truncated = True
            warnings.append("regex timed out on line %d; skipped" % ln)
            continue
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
    else:
        n = (line.lower() if ignore_case else line).count(needle)
    if n:
        total += n
        lines_hit += 1
        per_line.append((n, ln))

per_line.sort(key=lambda x: (-x[0], x[1]))
top = [{"line": ln, "matches": n} for n, ln in per_line[:TOP_LINES]]

text_out = "%d occurrence(s) of %s in %s (%d line(s) of %d)" % (
    total, ("regex " if is_regex else "") + repr(pat)[:60], src, lines_hit, scanned)
if truncated:
    text_out += " [truncated]"

emit(True, {"count": total, "lines_matched": lines_hit, "lines_scanned": scanned,
            "top_lines": top, "pattern": pat, "regex": is_regex,
            "truncated": truncated, "text": text_out},
     1.0 if not truncated else 0.6, [], warnings)
