# hash_verify: check a file (or every file listed in a checksum file) against its
# expected digest. Sources, in order: the `expected` arg, the `sums` arg, then sidecars
# next to the file (<file>.sha256/.sha1/.md5/.sha512, SHA256SUMS, sha256sum.txt,
# MD5SUMS, CHECKSUMS ...). Formats: GNU "<hex>  <name>" / "<hex> *<name>", BSD
# "SHA256 (name) = <hex>", or a bare hex digest. Files are read in 1 MB blocks under a
# 4 s budget: a file that could not be read to the end is reported match:null (never
# "ok" on a partial read). A mismatch is a successful run (ok:true, match:false).
# Python 3.8+ stdlib only. Read-only, deterministic, no network.
import hashlib
import time

T0 = time.time()
BUDGET_S = 4.0
CHUNK = 1 << 20
MAX_SUMS_BYTES = 4 << 20   # a checksum file above this is not a checksum file
MAX_ENTRIES = 2000         # entries verified from one checksum file

ALGOS = ("md5", "sha1", "sha256", "sha512")
HEX_LEN = {32: "md5", 40: "sha1", 64: "sha256", 128: "sha512"}
EXT_ALGO = {".md5": "md5", ".sha1": "sha1", ".sha256": "sha256", ".sha512": "sha512"}
# checksum files that live beside the target and list several files
R_SUMS_NAME = re.compile(r"^(?:(md5|sha1|sha256|sha512)sums?|checksums?|sums)(?:\.txt)?$", re.I)
R_HEX = re.compile(r"^[0-9a-fA-F]+$")
R_GNU = re.compile(r"^\\?([0-9a-fA-F]{32,128})\s+[* ]?(\S.*)$")
R_BSD = re.compile(r"^(MD5|SHA1|SHA256|SHA512)\s*\((.+)\)\s*=\s*([0-9a-fA-F]{32,128})\s*$", re.I)

raw_path = arg("path")
if raw_path is None:
    fail("path is required (a file to verify or a checksum file)")
path = os.path.normpath(path_in_root(raw_path))
algo_arg = str(arg("algo", "auto")).lower()
if algo_arg not in ("auto", "md5", "sha1", "sha256"):
    fail("algo must be auto, sha256, sha1 or md5")
sums_arg = arg("sums")
expected_arg = arg("expected")
warnings, unparsed = [], []
truncated = False


def remaining():
    return BUDGET_S - (time.time() - T0)


def norm_hex(s):
    s = str(s).strip().lower()
    for a in ALGOS:
        if s.startswith(a + ":"):
            s = s[len(a) + 1:]
    return s


def algo_of_hex(h, hint=None):
    """Algorithm a digest belongs to: explicit hint (BSD tag / file extension) wins when
    its length agrees with the hex; otherwise the length decides; unknown -> None."""
    by_len = HEX_LEN.get(len(h))
    if hint and hint in ALGOS:
        return hint if by_len in (None, hint) else by_len
    return by_len


def read_small(p):
    try:
        if os.path.getsize(p) > MAX_SUMS_BYTES:
            return None
        with open(p, "rb") as f:
            return f.read().decode("utf-8", "replace")
    except (IOError, OSError):
        return None


def parse_sums(text, algo_hint=None):
    """-> (entries, bad_lines). entry = (name|None, hex, algo|None). name None = bare digest."""
    entries, bad = [], []
    for raw in text.splitlines():
        line = raw.strip().lstrip("﻿")
        if not line or line.startswith("#"):
            continue
        m = R_BSD.match(line)
        if m:
            h = m.group(3).lower()
            entries.append((m.group(2).strip(), h, algo_of_hex(h, m.group(1).lower())))
            continue
        m = R_GNU.match(line)
        if m:
            h = m.group(1).lower()
            name = m.group(2).strip()
            if name.startswith("*"):
                name = name[1:]
            entries.append((name, h, algo_of_hex(h, algo_hint)))
            continue
        if R_HEX.match(line) and len(line) in HEX_LEN:
            entries.append((None, line.lower(), algo_of_hex(line.lower(), algo_hint)))
            continue
        bad.append(line)
    return entries, bad


def entry_name_norm(name):
    n = name.replace("\\", "/")
    while n.startswith("./"):
        n = n[2:]
    return n


def names_for(target, base_dir):
    """Names under which `target` may appear in a checksum file in base_dir."""
    out = {os.path.basename(target)}
    try:
        r = os.path.relpath(target, base_dir)
        if not r.startswith(".."):
            out.add(r.replace(os.sep, "/"))
    except ValueError:
        pass
    return out


def looks_like_sums_file(p):
    """True when p is named like a checksum file, or (sums arg absent) when its whole
    content parses as checksum lines that name files."""
    b = os.path.basename(p)
    if R_SUMS_NAME.match(b) or os.path.splitext(b)[1].lower() in EXT_ALGO:
        return True
    text = read_small(p)
    if text is None or not text.strip():
        return False
    entries, bad = parse_sums(text)
    return bool(entries) and not bad and all(e[0] is not None for e in entries)


# ---- the work list: [(file_abs, algo|None, expected_hex, source, note)] ----
jobs = []


def add_job(f, algo, h, source, note=None):
    jobs.append({"file": os.path.normpath(f), "algo": algo, "expected": h, "source": source, "note": note})


def jobs_from_sums_file(sp, only_target=None):
    """Every entry of checksum file sp, or only those naming only_target (abs path).
    A bare digest in a sidecar <x>.sha256 applies to <x>."""
    text = read_small(sp)
    if text is None:
        warnings.append("cannot read %s (missing or over %d MB)" % (rel(sp), MAX_SUMS_BYTES >> 20))
        return 0
    base = os.path.basename(sp)
    ext = os.path.splitext(base)[1].lower()
    hint = EXT_ALGO.get(ext)
    if not hint:
        m = R_SUMS_NAME.match(base)
        if m and m.group(1):
            hint = m.group(1).lower()
    entries, bad = parse_sums(text, hint)
    for b in bad[:10]:
        unparsed.append("%s: %s" % (rel(sp), b))
    if len(bad) > 10:
        warnings.append("%s: %d unparsed lines" % (rel(sp), len(bad)))
    d = os.path.dirname(sp)
    wanted = names_for(only_target, d) if only_target else None
    n = 0
    for name, h, algo in entries:
        if name is None:
            # bare digest: sidecar <x>.sha256 -> <x>; otherwise only_target when given
            if hint and ext in EXT_ALGO:
                f = sp[: -len(ext)]
            elif only_target:
                f = only_target
            else:
                unparsed.append("%s: bare digest without a file name" % rel(sp))
                continue
        else:
            nn = entry_name_norm(name)
            f = nn if os.path.isabs(nn) else os.path.join(d, nn)
        if wanted is not None:
            if name is not None and entry_name_norm(name) not in wanted:
                continue
            if name is None and os.path.normpath(f) != os.path.normpath(only_target):
                continue
            f = only_target  # the entry names the file the caller asked about
        if algo_arg != "auto" and algo != algo_arg:
            continue
        add_job(f, algo, h, rel(sp))
        n += 1
        if n >= MAX_ENTRIES:
            warnings.append("%s: more than %d entries; the rest were not verified" % (rel(sp), MAX_ENTRIES))
            break
    return n


def sidecar_candidates(target):
    """Sidecars for target, deterministic order: <target>.<algo> then the directory's
    SHA256SUMS-style files (case-insensitive, sorted)."""
    out = []
    for ext in (".sha256", ".sha1", ".md5", ".sha512"):
        if algo_arg != "auto" and EXT_ALGO[ext] != algo_arg:
            continue
        if os.path.isfile(target + ext):
            out.append(target + ext)
    d = os.path.dirname(target) or "."
    try:
        listing = sorted(os.listdir(d))
    except OSError:
        listing = []
    for b in listing:
        m = R_SUMS_NAME.match(b)
        if not m:
            continue
        if algo_arg != "auto" and m.group(1) and m.group(1).lower() != algo_arg:
            continue
        p = os.path.join(d, b)
        if os.path.isfile(p) and os.path.normpath(p) != os.path.normpath(target):
            out.append(p)
    return out


if not os.path.exists(path):
    fail("path not found: %s" % rel(path))
if os.path.isdir(path):
    fail("path is a directory; pass a file or a checksum file (SHA256SUMS): %s" % rel(path))

if expected_arg is not None:
    h = norm_hex(expected_arg)
    if not R_HEX.match(h) or len(h) not in HEX_LEN:
        fail("expected is not a hex digest of 32/40/64/128 chars")
    algo = algo_of_hex(h, None if algo_arg == "auto" else algo_arg)
    if algo_arg != "auto" and algo != algo_arg:
        fail("expected has %d hex chars, which is %s, not %s" % (len(h), algo, algo_arg))
    add_job(path, algo, h, "arg:expected")
elif sums_arg is not None:
    sp = os.path.normpath(path_in_root(sums_arg))
    if not os.path.isfile(sp):
        fail("sums file not found: %s" % rel(sp))
    if jobs_from_sums_file(sp, only_target=path) == 0:
        add_job(path, None, None, rel(sp), "not listed in %s" % rel(sp))
elif looks_like_sums_file(path):
    if jobs_from_sums_file(path) == 0:
        fail("no %schecksum entries found in %s" % ("" if algo_arg == "auto" else algo_arg + " ", rel(path)))
else:
    found = 0
    for sp in sidecar_candidates(path):
        found += jobs_from_sums_file(sp, only_target=path)
    if found == 0:
        fail("no checksum found for %s (pass expected=<hex> or sums=<file>; looked for %s.sha256/.sha1/.md5 "
             "and SHA256SUMS/MD5SUMS/CHECKSUMS in %s)" % (rel(path), os.path.basename(path), rel(os.path.dirname(path)) or "."))

# ---- hash each file once for all the algorithms its jobs need ----
seen = set()
uniq = []
for j in jobs:
    k = (j["file"], j["algo"], j["expected"])
    if k not in seen:
        seen.add(k)
        uniq.append(j)
jobs = sorted(uniq, key=lambda j: (j["file"], j["algo"] or "", j["expected"] or ""))

digests = {}   # file -> {algo: hex} | ("error", reason)


def digest_file(f, algos):
    """Read f once, feeding every requested algo. Returns ({algo: hex}, None) or
    (None, reason). Stops at the time budget: partial reads never yield a digest."""
    hs = {}
    for a in algos:
        try:
            hs[a] = hashlib.new(a)
        except ValueError:
            return None, "unsupported algorithm %s" % a
    try:
        size = os.path.getsize(f)
        done = 0
        with open(f, "rb") as fh:
            while True:
                if remaining() <= 0:
                    return None, "time budget reached after %d of %d MB" % (done >> 20, size >> 20)
                b = fh.read(CHUNK)
                if not b:
                    break
                done += len(b)
                for h in hs.values():
                    h.update(b)
    except (IOError, OSError) as e:
        return None, "unreadable: %s" % clip(getattr(e, "strerror", None) or e, 80)
    return {a: h.hexdigest() for a, h in hs.items()}, None


by_file = {}
for j in jobs:
    by_file.setdefault(j["file"], []).append(j)

results = []
ok_count = fail_count = missing_count = 0
for f in sorted(by_file):
    group = by_file[f]
    needed = sorted(set(j["algo"] for j in group if j["algo"] and j["expected"]))
    exists = os.path.isfile(f)
    got, why = None, None
    if exists and needed:
        if remaining() <= 0:
            truncated = True
            why = "skipped: time budget"
        else:
            got, why = digest_file(f, needed)
            if got is None and why.startswith("time budget"):
                truncated = True
    for j in group:
        r = {"file": rel(f), "algo": j["algo"], "expected": j["expected"], "actual": None,
             "match": None, "reason": "", "source": j["source"]}
        if j["note"]:
            r["reason"] = j["note"]
        elif not exists:
            r["reason"] = "file not found"
            missing_count += 1
        elif not j["algo"]:
            r["reason"] = "unknown algorithm for a %d-char digest" % len(j["expected"] or "")
        elif got is None:
            r["reason"] = why
        else:
            r["actual"] = got[j["algo"]]
            r["match"] = r["actual"] == j["expected"]
            r["reason"] = "match" if r["match"] else "mismatch"
            if r["match"]:
                ok_count += 1
            else:
                fail_count += 1
        results.append(r)

results.sort(key=lambda r: (r["file"], r["algo"] or "", r["expected"] or ""))
undecided = sum(1 for r in results if r["match"] is None and r["reason"] != "file not found")
algos_used = sorted(set(r["algo"] for r in results if r["algo"]))
algo_txt = "/".join(algos_used) if algos_used else "?"

if fail_count:
    bad = [os.path.basename(r["file"]) for r in results if r["match"] is False]
    text = "MISMATCH: %s (%s)" % (", ".join(bad[:3]) if len(bad) <= 3 else "%d files" % len(bad), algo_txt)
else:
    text = "%d of %d files match (%s)" % (ok_count, len(results), algo_txt)
if missing_count:
    text += ", %d missing" % missing_count
if undecided:
    text += ", %d unverified" % undecided
    warnings.append("%d result(s) undecided (not listed, unreadable or time budget): see reason" % undecided)

data = {"results": results, "ok_count": ok_count, "fail_count": fail_count,
        "missing_count": missing_count, "truncated": truncated, "text": text}
emit(True, data, 1.0 if not undecided else 0.5, unparsed, warnings)
