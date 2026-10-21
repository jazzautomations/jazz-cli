# csv_stats: row count and per-column type, min/max, nulls, distinct and top values.
import csv
import io

p = path_in_root(arg("file", ""))
try:
    raw = read_text(p, limit=32 << 20)
except (IOError, OSError) as e:
    fail("cannot read file: %s" % e)
if raw.startswith("﻿"):
    raw = raw[1:]
sample = raw[:20000]
delim = arg("delimiter")
if not delim:
    try:
        delim = csv.Sniffer().sniff(sample, delimiters=",;\t|").delimiter
    except csv.Error:
        delim = ","
try:
    has_header = csv.Sniffer().has_header(sample) if arg("header") is None else bool(ARGS["header"])
except csv.Error:
    has_header = True
rows = csv.reader(io.StringIO(raw), delimiter=delim)
header = None
cols = []
NULLS = set(["", "na", "n/a", "nan", "null", "none", "nil", "-"])
INT = re.compile(r"^[-+]?\d+$")
FLOAT = re.compile(r"^[-+]?(\d+\.\d*|\.\d+|\d+)([eE][-+]?\d+)?$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?)?")
BOOL = set(["true", "false", "yes", "no", "t", "f", "y", "n"])
n, ragged, limit = 0, 0, 200000
for r in rows:
    if header is None:
        if has_header:
            header = [h.strip() for h in r]
            cols = [None] * len(header)
            continue
        header = ["col%d" % (i + 1) for i in range(len(r))]
        cols = [None] * len(header)
    n += 1
    if n > limit:
        break
    if len(r) != len(header):
        ragged += 1
    for i, v in enumerate(r[:len(header)]):
        c = cols[i]
        if c is None:
            c = cols[i] = {"nulls": 0, "int": 0, "float": 0, "date": 0, "bool": 0, "str": 0, "min": None, "max": None,
                           "smin": None, "smax": None, "vals": {}}
        v = v.strip()
        if v.lower() in NULLS:
            c["nulls"] += 1
            continue
        if INT.match(v) or FLOAT.match(v):
            c["int" if INT.match(v) else "float"] += 1
            x = float(v)
            c["min"] = x if c["min"] is None else min(c["min"], x)
            c["max"] = x if c["max"] is None else max(c["max"], x)
        elif DATE.match(v):
            c["date"] += 1
        elif v.lower() in BOOL:
            c["bool"] += 1
        else:
            c["str"] += 1
        c["smin"] = v if c["smin"] is None else min(c["smin"], v)
        c["smax"] = v if c["smax"] is None else max(c["smax"], v)
        if len(c["vals"]) < 5000 or v in c["vals"]:
            c["vals"][v] = c["vals"].get(v, 0) + 1
if header is None:
    fail("empty CSV")
want = arg("cols")
if isinstance(want, str):
    want = [x.strip() for x in want.split(",") if x.strip()]
out = []
for i, name in enumerate(header):
    if want and name not in want and str(i) not in want:
        continue
    c = cols[i] if i < len(cols) else None
    if c is None:
        out.append({"name": name, "type": "empty", "nulls": n})
        continue
    filled = n - c["nulls"]
    kinds = [(k, c[k]) for k in ("int", "float", "date", "bool", "str") if c[k]]
    if not kinds:
        typ = "empty"
    elif c["str"] == 0 and c["date"] == 0 and c["bool"] == 0:
        typ = "float" if c["float"] else "int"
    else:
        typ = max(kinds, key=lambda kv: kv[1])[0] if len(kinds) == 1 or max(k[1] for k in kinds) >= 0.95 * filled else "mixed"
    e = {"name": name, "type": typ, "nulls": c["nulls"], "distinct": len(c["vals"]),
         "top": [{"value": clip(k, 60), "count": v} for k, v in sorted(c["vals"].items(), key=lambda kv: -kv[1])[:3]]}
    if typ in ("int", "float"):
        e["min"], e["max"] = c["min"], c["max"]
        if typ == "int":
            e["min"], e["max"] = int(c["min"]), int(c["max"])
    elif c["smin"] is not None:
        e["min"], e["max"] = clip(c["smin"], 60), clip(c["smax"], 60)
    out.append(e)
warn = []
if n > limit:
    warn.append("stopped after %d rows" % limit)
if ragged:
    warn.append("%d rows have a different number of fields than the header" % ragged)
if want and not out:
    fail("none of the requested columns exist; columns: %s" % ", ".join(header[:40]))
emit(True, {"file": rel(p), "rows": min(n, limit), "delimiter": delim, "header": has_header, "columns": out[:60]},
     1.0 if not ragged else max(0.5, 1 - ragged / float(max(n, 1))), [], warn)
