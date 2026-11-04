# license_scan: what the project says it is licensed under, from three sources that
# should agree: LICENSE/LICENCE/COPYING* files (identified by key phrases, best effort,
# with a confidence), the license field of manifests (package.json, composer.json,
# pyproject.toml, setup.cfg, Cargo.toml, *.gemspec) and SPDX-License-Identifier headers
# in the first 20 lines of code files. Disagreements are listed as conflicts.
# Read-only, deterministic, no network. Python 3.8+ stdlib only.
import time

T0 = time.time()
BUDGET_S = 4.0            # wall-clock budget: stop walking, mark truncated
MAX_FILES = 20000         # files visited at most
MAX_LICENSE_BYTES = 256 << 10
HEADER_BYTES = 8192       # enough for 20 lines of header
HEADER_LINES = 20
SKIP = SKIP_DIRS | set(["testdata", "dist", "vendor", "node_modules", "build", "target"])

LICENSE_RE = re.compile(r"^(licen[cs]e|copying|unlicense)([.\-_].*)?$", re.I)
MANIFESTS = set(["package.json", "composer.json", "pyproject.toml", "setup.cfg", "Cargo.toml"])
CODE_EXTS = set("py go js jsx mjs cjs ts tsx rs java kt kts swift c h cpp cc cxx hpp hh cs rb php "
                "sh bash zsh sql lua scala dart css scss less vue svelte m mm pl pm".split())
SPDX_RE = re.compile(r"SPDX-License-Identifier:\s*(\([^)\n]{1,160}\)|[A-Za-z0-9][A-Za-z0-9.+\-]*"
                     r"(?:\s+(?:AND|OR|WITH)\s+[A-Za-z0-9][A-Za-z0-9.+\-]*)*)", re.I)
CLASSIFIER_RE = re.compile(r"License :: OSI Approved :: ([^\"'\n,\]]+)")

# spellings seen in manifests and classifiers -> SPDX id (upper-cased for comparison)
ALIASES = {
    "MIT LICENSE": "MIT", "THE MIT LICENSE": "MIT", "EXPAT": "MIT",
    "APACHE": "APACHE-2.0", "APACHE2": "APACHE-2.0", "APACHE-2": "APACHE-2.0", "APACHE 2.0": "APACHE-2.0",
    "APACHE LICENSE 2.0": "APACHE-2.0", "APACHE SOFTWARE LICENSE": "APACHE-2.0", "ASL 2.0": "APACHE-2.0",
    "GPL2": "GPL-2.0", "GPLV2": "GPL-2.0", "GPL-2": "GPL-2.0", "GPL3": "GPL-3.0", "GPLV3": "GPL-3.0", "GPL-3": "GPL-3.0",
    "GNU GENERAL PUBLIC LICENSE V2 (GPLV2)": "GPL-2.0", "GNU GENERAL PUBLIC LICENSE V3 (GPLV3)": "GPL-3.0",
    "GNU GENERAL PUBLIC LICENSE V2 OR LATER (GPLV2+)": "GPL-2.0", "GNU GENERAL PUBLIC LICENSE V3 OR LATER (GPLV3+)": "GPL-3.0",
    "LGPL3": "LGPL-3.0", "LGPLV3": "LGPL-3.0", "LGPL-3": "LGPL-3.0", "LGPLV2.1": "LGPL-2.1", "LGPL-2": "LGPL-2.0",
    "GNU LESSER GENERAL PUBLIC LICENSE V3 (LGPLV3)": "LGPL-3.0", "GNU LESSER GENERAL PUBLIC LICENSE V2 OR LATER (LGPLV2+)": "LGPL-2.0",
    "BSD3": "BSD-3-CLAUSE", "BSD-3": "BSD-3-CLAUSE", "NEW BSD": "BSD-3-CLAUSE", "MODIFIED BSD": "BSD-3-CLAUSE",
    "BSD2": "BSD-2-CLAUSE", "BSD-2": "BSD-2-CLAUSE", "SIMPLIFIED BSD": "BSD-2-CLAUSE", "BSD LICENSE": "BSD",
    "MPL2": "MPL-2.0", "MPL-2": "MPL-2.0", "MPL 2.0": "MPL-2.0", "MOZILLA PUBLIC LICENSE 2.0 (MPL 2.0)": "MPL-2.0",
    "ISC LICENSE": "ISC", "ISC LICENSE (ISCL)": "ISC", "THE UNLICENSE": "UNLICENSE", "THE UNLICENSE (UNLICENSE)": "UNLICENSE",
    "CC0": "CC0-1.0", "CC0 1.0": "CC0-1.0",
}


def to_int(name, default, lo, hi):
    try:
        v = int(arg(name, default))
    except (TypeError, ValueError):
        fail("%s must be an integer" % name)
    if v < lo or v > hi:
        fail("%s must be between %d and %d" % (name, lo, hi))
    return v


top = path_in_root(arg("path", "."))
if not os.path.exists(top):
    fail("path not found: %s" % rel(top))
limit = to_int("limit", 500, 1, MAX_FILES)


def canon(s):
    """Upper-cased SPDX id for comparison: aliases folded, -only/-or-later/+ dropped."""
    s = re.sub(r"\s+", " ", str(s).strip()).upper()
    s = ALIASES.get(s, s)
    return re.sub(r"(-ONLY|-OR-LATER|\+)$", "", s)


def tokens(expr):
    """Canonical ids inside an SPDX expression ("MIT OR Apache-2.0" -> {MIT, APACHE-2.0})."""
    return set(canon(t) for t in re.split(r"\s+(?:AND|OR|WITH)\s+|[()]", str(expr), flags=re.I) if t.strip())


def compatible(ref_ids, expr):
    """True when at least one id of expr is one of ref_ids (BSD variants count as one family)."""
    for t in tokens(expr):
        for r in ref_ids:
            if t == r or (t.startswith("BSD") and r.startswith("BSD")):
                return True
    return False


# ---------- license text identification ----------

def gnu_id(fam, where):
    m = re.search(r"version (\d(?:\.\d)?)", where)
    if not m:
        return fam, 0.5
    ver = m.group(1)
    if "." not in ver:
        ver += ".0"
    return "%s-%s" % (fam, ver), 0.9


def identify(text):
    """(SPDX id, confidence) for a license text; ('unknown', 0.0) when no phrase matches."""
    m = SPDX_RE.search(text)
    if m:
        return m.group(1).strip("()"), 0.99
    norm = re.sub(r"\s+", " ", text).lower()
    head = norm[:600]
    if "permission is hereby granted, free of charge, to any person obtaining a copy" in norm:
        return "MIT", 0.95 if "without restriction" in norm else 0.8
    if "permission to use, copy, modify, and/or distribute this software for any purpose" in norm:
        return "ISC", 0.95
    if "this is free and unencumbered software released into the public domain" in norm:
        return "Unlicense", 0.95
    if "cc0 1.0 universal" in norm or ("creative commons" in norm and "cc0" in norm):
        return "CC0-1.0", 0.9
    if "mozilla public license" in head:
        return ("MPL-2.0", 0.95) if "2.0" in head else ("MPL", 0.5)
    if "apache license" in norm and "version 2.0" in norm:
        return "Apache-2.0", 0.95
    if "redistribution and use in source and binary forms" in norm:
        if "all advertising materials" in norm:
            return "BSD-4-Clause", 0.9
        if "neither the name" in norm or "may not be used to endorse" in norm:
            return "BSD-3-Clause", 0.9
        return "BSD-2-Clause", 0.85
    # GNU family: the title decides (the GPL text itself mentions the LGPL further down)
    title = norm[:200]
    if "lesser general public license" in title or "library general public license" in title:
        return gnu_id("LGPL", head)
    if "affero general public license" in title:
        return gnu_id("AGPL", head)
    if "general public license" in title:
        return gnu_id("GPL", head)
    m = re.search(r"under the terms of the gnu (lesser |library |affero )?general public license.{0,120}?"
                  r"(?:either )?version (\d(?:\.\d)?)", norm)
    if m:  # the short notice form, as copied into COPYING by hand
        fam = {"lesser ": "LGPL", "library ": "LGPL", "affero ": "AGPL", None: "GPL"}[m.group(1)]
        ver = m.group(2) + ("" if "." in m.group(2) else ".0")
        return "%s-%s" % (fam, ver), 0.7
    if head.startswith("mit license") or head.startswith("the mit license"):
        return "MIT", 0.6
    if head.startswith("isc license"):
        return "ISC", 0.6
    if head.startswith("bsd"):
        return "BSD", 0.5
    return "unknown", 0.0


# ---------- manifests ----------

def json_license(obj):
    lic = obj.get("license") if isinstance(obj, dict) else None
    if isinstance(lic, dict):
        lic = lic.get("type")
    if isinstance(lic, list):
        lic = " OR ".join(str(x.get("type") if isinstance(x, dict) else x) for x in lic if x)
    if not lic and isinstance(obj, dict) and isinstance(obj.get("licenses"), list):
        lic = " OR ".join(str(x.get("type") if isinstance(x, dict) else x) for x in obj["licenses"] if x)
    return str(lic).strip() if lic else None


def toml_value(v):
    v = v.strip()
    m = re.match(r"""^(["'])(.*?)\1""", v)
    if m:
        return m.group(2).strip() or None
    if v.startswith("{"):  # inline table: { text = "MIT" } or { file = "LICENSE" } (file: see license files)
        m = re.search(r"""\btext\s*=\s*(["'])(.*?)\1""", v)
        return m.group(2).strip() if m else None
    return None


def toml_license(text, sections):
    sec, found = None, None
    for line in text.splitlines():
        s = line.strip()
        m = re.match(r"^\[\s*([^\]]+?)\s*\]", s)
        if m:
            sec = m.group(1)
            continue
        if sec in sections:
            m = re.match(r"^license\s*=\s*(.+)$", s)
            if m and found is None:
                found = toml_value(m.group(1))
    return found


def cfg_license(text):
    sec, found = None, None
    for line in text.splitlines():
        s = line.strip()
        m = re.match(r"^\[\s*([^\]]+?)\s*\]", s)
        if m:
            sec = m.group(1)
            continue
        if sec == "metadata":
            m = re.match(r"^license\s*[=:]\s*(.+)$", s)
            if m and found is None:
                found = m.group(1).strip().strip("\"'") or None
    return found


def classifier_license(text):
    for m in CLASSIFIER_RE.finditer(text):
        c = canon(m.group(1))
        if c != m.group(1).strip().upper():  # only spellings the alias table knows
            return c
    return None


def manifest_license(name, text):
    if name in ("package.json", "composer.json"):
        try:
            return json_license(json.loads(text))
        except ValueError:
            return None
    if name == "pyproject.toml":
        return toml_license(text, ("project", "tool.poetry")) or classifier_license(text)
    if name == "Cargo.toml":
        return toml_license(text, ("package",))
    if name == "setup.cfg":
        return cfg_license(text) or classifier_license(text)
    if name.endswith(".gemspec"):
        m = re.search(r"""\.licenses?\s*=\s*\[?\s*(["'])([^"']+)\1""", text)
        return m.group(2).strip() if m else None
    return None


# ---------- walk ----------

def iter_files(top):
    if os.path.isfile(top):
        yield top
        return
    n = 0
    for d, dirs, files in os.walk(top):
        dirs[:] = sorted(x for x in dirs if x not in SKIP and not x.startswith("."))
        for f in sorted(files):
            n += 1
            if n > MAX_FILES:
                return
            yield os.path.join(d, f)


def read_head(p, nbytes):
    with open(p, "rb") as f:
        raw = f.read(nbytes)
    if b"\x00" in raw[:4096]:
        return None
    return raw.decode("utf-8", "replace")


def relpath(p):
    return rel(p).replace(os.sep, "/")


TOP_DIR = top if os.path.isdir(top) else os.path.dirname(top)


def dir_key(f):
    """Directory of a reported file, relative to the scanned root ('' = the root itself)."""
    d = os.path.relpath(os.path.dirname(path_in_root(f)), TOP_DIR).replace(os.sep, "/")
    return "" if d == "." else d


project_licenses, manifest_licenses, spdx_headers = [], [], {}
files_scanned, files_with, truncated, warnings = 0, 0, False, []
header_examples = {}       # id -> first file carrying it
n_visited = 0
for p in iter_files(top):
    n_visited += 1
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d files" % n_visited)
        break
    name = os.path.basename(p)
    if os.path.islink(p):
        continue
    if LICENSE_RE.match(name):
        try:
            text = read_head(p, MAX_LICENSE_BYTES)
        except (IOError, OSError):
            continue
        if text is None:
            continue
        lid, conf = identify(text)
        project_licenses.append({"file": relpath(p), "id": lid, "confidence": conf})
        continue
    if name in MANIFESTS or name.endswith(".gemspec"):
        try:
            text = read_head(p, MAX_LICENSE_BYTES)
        except (IOError, OSError):
            continue
        lid = manifest_license(name, text or "")
        if lid:
            manifest_licenses.append({"file": relpath(p), "id": clip(lid, 120)})
        continue
    ext = os.path.splitext(name)[1].lower().lstrip(".")
    if ext not in CODE_EXTS or ".min." in name.lower():
        continue
    if files_scanned >= limit:
        truncated = True
        if not any(w.startswith("file limit") for w in warnings):
            warnings.append("file limit %d reached; header counts are partial (raise limit)" % limit)
        continue
    try:
        text = read_head(p, HEADER_BYTES)
    except (IOError, OSError):
        continue
    if text is None:
        continue
    files_scanned += 1
    m = SPDX_RE.search("\n".join(text.splitlines()[:HEADER_LINES]))
    if m:
        hid = re.sub(r"\s+", " ", m.group(1).strip("()"))
        files_with += 1
        spdx_headers[hid] = spdx_headers.get(hid, 0) + 1
        header_examples.setdefault(hid, relpath(p))
if n_visited >= MAX_FILES:
    truncated = True
    warnings.append("stopped at %d files" % MAX_FILES)

project_licenses.sort(key=lambda e: (e["file"].count("/"), e["file"]))
manifest_licenses.sort(key=lambda e: (e["file"].count("/"), e["file"]))
spdx_headers = dict((k, spdx_headers[k]) for k in sorted(spdx_headers))

# ---------- conflicts ----------
# reference for a directory: its own license files (confidence >= 0.5), else the parent's
by_dir = {}
for e in project_licenses:
    if e["confidence"] >= 0.5:
        by_dir.setdefault(dir_key(e["file"]), []).append(e)


def reference(d):
    while True:
        if d in by_dir:
            return by_dir[d]
        if d == "":
            return []
        d = os.path.dirname(d)


def says(entries):
    return ", ".join("%s says %s" % (e["file"], e["id"]) for e in entries)


conflicts = []
root_ref = reference("")
root_ids = set(canon(e["id"]) for e in root_ref)
# two plain license files at the root that disagree (LICENSE-MIT + LICENSE-APACHE is dual licensing, not a conflict)
plain = [e for e in root_ref if not LICENSE_RE.match(os.path.basename(e["file"])).group(2)]
if len(set(canon(e["id"]) for e in plain)) > 1:
    conflicts.append(says(plain))
for m in manifest_licenses:
    ref = reference(dir_key(m["file"]))
    ids = set(canon(e["id"]) for e in ref)
    if ids and not compatible(ids, m["id"]):
        conflicts.append("%s, %s says %s" % (says(ref), m["file"], m["id"]))
if root_ids:
    for hid in sorted(spdx_headers):
        if not compatible(root_ids, hid):
            n = spdx_headers[hid]
            conflicts.append("%s, %d file%s carr%s SPDX %s (e.g. %s)" % (
                says(root_ref), n, "" if n == 1 else "s", "ies" if n == 1 else "y", hid, header_examples[hid]))
conflicts = [clip(c, 300) for c in conflicts]

if root_ref:
    proj = ", ".join("%s (%s)" % (e["id"], e["file"]) for e in root_ref)
elif project_licenses:
    proj = "no license identified at the root (%d license file(s) found)" % len(project_licenses)
else:
    proj = "no license file"
text = "project: %s; %d files with SPDX, %d without" % (proj, files_with, files_scanned - files_with)
if manifest_licenses:
    text += "; manifests: " + ", ".join("%s %s" % (m["file"], m["id"]) for m in manifest_licenses[:3])
if conflicts:
    text += "; %d conflict%s" % (len(conflicts), "" if len(conflicts) == 1 else "s")
if truncated:
    text += " [truncated]"

conf = 0.9 if root_ref else (0.6 if files_scanned or project_licenses or manifest_licenses else 0.3)
if truncated:
    conf = min(conf, 0.6)
emit(True, {"project_licenses": project_licenses, "manifest_licenses": manifest_licenses,
            "spdx_headers": spdx_headers, "files_with_header": files_with,
            "files_without_header": files_scanned - files_with, "files_scanned": files_scanned,
            "conflicts": conflicts, "truncated": truncated, "text": clip(text, 400)},
     conf, [], warnings)
