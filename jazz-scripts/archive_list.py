# archive_list: what is inside a .zip/.whl/.jar or .tar(.gz/.bz2/.xz) WITHOUT extracting
# anything (zipfile/tarfile read-only, headers only). Flags entries that would escape the
# target dir (absolute path, "..", symlink/hardlink pointing outside) and warns about
# decompression bombs (uncompressed/compressed ratio > 100). Python 3.8+ stdlib only.
import stat
import tarfile
import time
import zipfile

T0 = time.time()
BUDGET_S = 4.0
MAX_BYTES = 1 << 30      # archives above 1 GB are refused (headers alone can take minutes)
MAX_ENTRIES = 200000     # stop walking the archive here, mark truncated
BOMB_RATIO = 100.0
ACCEPTED = ".zip .whl .jar .tar .tar.gz .tgz .tar.bz2 .tbz2 .tar.xz .txz"

if arg("path") is None:
    fail("path is required (accepted: %s)" % ACCEPTED)
p = path_in_root(arg("path"))
try:
    limit = max(1, min(5000, int(arg("limit", 200))))
except (TypeError, ValueError):
    fail("limit must be an integer")
if os.path.islink(p):
    fail("path is a symlink (not followed): %s" % rel(p))
if not os.path.isfile(p):
    fail("not a file: %s" % rel(p))
try:
    archive_size = os.path.getsize(p)
    with open(p, "rb") as f:
        magic = f.read(6)
except (IOError, OSError) as e:
    fail("cannot read: %s" % e)
if archive_size > MAX_BYTES:
    fail("archive is %d MB, above the 1 GB limit" % (archive_size >> 20))


def detect_kind(path, magic):
    """'zip' | 'tar' | 'tar.gz' | 'tar.bz2' | 'tar.xz' | None, by magic bytes then extension."""
    low = os.path.basename(path).lower()
    if magic[:4] in (b"PK\x03\x04", b"PK\x05\x06", b"PK\x07\x08"):
        return "zip"
    if magic[:2] == b"\x1f\x8b":
        return "tar.gz"
    if magic[:3] == b"BZh":
        return "tar.bz2"
    if magic[:6] == b"\xfd7zXZ\x00":
        return "tar.xz"
    if low.endswith((".tar", ".zip", ".whl", ".jar", ".tgz", ".tbz2", ".txz")) or ".tar." in low:
        # extension says archive but the magic is unknown: let tarfile sniff (plain .tar has
        # its magic at offset 257), zipfile for the zip family
        if low.endswith((".zip", ".whl", ".jar")):
            return "zip" if zipfile.is_zipfile(path) else None
        try:
            if tarfile.is_tarfile(path):
                return "tar"
        except (IOError, OSError, tarfile.TarError, EOFError):
            return None
    return None


kind = detect_kind(p, magic)
if kind is None:
    fail("unknown archive format: %s (accepted: %s)" % (rel(p), ACCEPTED))

WIN_DRIVE = re.compile(r"^[A-Za-z]:")


def unsafe_name(name):
    """True when extracting this name could land outside the target directory."""
    n = name.replace("\\", "/")
    if n.startswith("/") or WIN_DRIVE.match(n):
        return True
    return any(part == ".." for part in n.split("/"))


def unsafe_link(member_name, target):
    """Symlink/hardlink target: absolute, or climbing above the archive root."""
    t = target.replace("\\", "/")
    if t.startswith("/") or WIN_DRIVE.match(t):
        return True
    base = os.path.dirname(member_name.replace("\\", "/"))
    depth = 0
    for part in (base + "/" + t).split("/"):
        if part == "..":
            depth -= 1
            if depth < 0:
                return True
        elif part and part != ".":
            depth += 1
    return False


entries, warnings = [], []
count = total = 0
compressed = 0
truncated = False
unsafe_count = encrypted = 0


def over_budget():
    return time.time() - T0 > BUDGET_S


try:
    if kind == "zip":
        with zipfile.ZipFile(p, "r") as zf:
            infos = zf.infolist()
            for zi in infos:
                if count >= MAX_ENTRIES or over_budget():
                    truncated = True
                    break
                count += 1
                is_dir = zi.is_dir()
                bad = unsafe_name(zi.filename)
                if zi.flag_bits & 0x1:
                    encrypted += 1
                total += zi.file_size
                compressed += zi.compress_size
                unsafe_count += 1 if bad else 0
                entries.append({"name": zi.filename, "type": "dir" if is_dir else "file",
                                "size": zi.file_size, "compressed": zi.compress_size,
                                "is_dir": is_dir, "unsafe": bad})
            if count < len(infos):
                truncated = True
        compressed_size = compressed
    else:
        # streaming header walk; member data is never read
        with tarfile.open(p, mode="r:*") as tf:
            for m in tf:
                if count >= MAX_ENTRIES or over_budget():
                    truncated = True
                    break
                count += 1
                if m.isdir():
                    typ = "dir"
                elif m.issym():
                    typ = "symlink"
                elif m.islnk():
                    typ = "hardlink"
                elif m.isreg():
                    typ = "file"
                else:
                    typ = "other"  # char/block device, fifo
                bad = unsafe_name(m.name)
                if typ in ("symlink", "hardlink") and m.linkname and unsafe_link(m.name, m.linkname):
                    bad = True
                if typ == "other":
                    bad = True  # device nodes / fifos have no business in a code archive
                total += m.size if typ == "file" else 0
                unsafe_count += 1 if bad else 0
                e = {"name": m.name, "type": typ, "size": m.size if typ == "file" else 0,
                     "compressed": None, "is_dir": typ == "dir", "unsafe": bad}
                if typ in ("symlink", "hardlink"):
                    e["link"] = m.linkname
                entries.append(e)
        compressed_size = archive_size if kind != "tar" else None
except (zipfile.BadZipFile, tarfile.TarError, EOFError, OSError, ValueError) as e:
    fail("cannot read %s archive %s: %s" % (kind, rel(p), clip(e, 160)))

if truncated:
    warnings.append("stopped after %d entries (time/size budget)" % count)
if encrypted:
    warnings.append("%d encrypted entr%s" % (encrypted, "y" if encrypted == 1 else "ies"))

ratio = None
if compressed_size:
    ratio = round(total / float(compressed_size), 1)
    if ratio > BOMB_RATIO:
        warnings.append("compression ratio %.0f:1 — possible decompression bomb, do not extract blindly" % ratio)
if unsafe_count:
    warnings.append("%d unsafe entr%s (absolute path, '..' or link escaping the root): extract only with a path filter"
                    % (unsafe_count, "y" if unsafe_count == 1 else "ies"))

# unsafe first so they survive the limit cut, then by name: deterministic
entries.sort(key=lambda e: (not e["unsafe"], e["name"]))
if len(entries) > limit:
    truncated = True
    warnings.append("%d entries; showing %d (raise limit)" % (len(entries), limit))
    entries = entries[:limit]


def human(n):
    units = ["B", "kB", "MB", "GB", "TB"]
    x = float(n)
    for u in units:
        if x < 1000 or u == units[-1]:
            return ("%d %s" % (x, u)) if u == "B" else ("%.1f %s" % (x, u))
        x /= 1000.0
    return "%d B" % n


if compressed_size:
    size_txt = "%s → %s (ratio %s)" % (human(compressed_size), human(total), ratio)
else:
    size_txt = "%s uncompressed" % human(total)
text = "%s: %d entr%s, %s, %d unsafe" % (kind, count, "y" if count == 1 else "ies", size_txt, unsafe_count)
if ratio is not None and ratio > BOMB_RATIO:
    text += " · BOMB?"
if truncated:
    text += " [truncated]"

emit(True, {"kind": kind, "path": rel(p), "entries": entries, "count": count, "total_size": total,
            "compressed_size": compressed_size, "ratio": ratio, "unsafe_count": unsafe_count,
            "truncated": truncated, "text": text},
     1.0 if not truncated else 0.6, [], warnings)
