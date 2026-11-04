# Regenerates sample.zip and sample.tar.gz (run from this directory: python3 make_fixtures.py).
# Deterministic: fixed timestamps, fixed order, no real names or addresses.
# sample.zip   : a dir, 2 files, a "../evil" entry and an absolute "/etc/passwd" entry (unsafe)
# sample.tar.gz: a dir, 2 files, a symlink to "../../outside" (unsafe), a safe symlink
import io
import tarfile
import zipfile

TS = (2026, 1, 2, 3, 4, 6)
MTIME = 1767323046


def add_zip(zf, name, data, is_dir=False):
    zi = zipfile.ZipInfo(name if not is_dir or name.endswith("/") else name + "/", date_time=TS)
    zi.compress_type = zipfile.ZIP_DEFLATED
    zi.external_attr = (0o755 << 16) | 0x10 if is_dir else 0o644 << 16
    zf.writestr(zi, b"" if is_dir else data)


with zipfile.ZipFile("sample.zip", "w") as zf:
    add_zip(zf, "pkg/", b"", is_dir=True)
    add_zip(zf, "pkg/app.py", b"print('hello from ana')\n" * 40)
    add_zip(zf, "pkg/notes.txt", b"host 192.0.2.10 serves the demo\n" * 20)
    add_zip(zf, "../evil", b"#!/bin/sh\necho pwned\n")
    add_zip(zf, "/etc/passwd", b"root:x:0:0\n")


def tinfo(name, typ=tarfile.REGTYPE, size=0, link=""):
    ti = tarfile.TarInfo(name)
    ti.type = typ
    ti.size = size
    ti.mtime = MTIME
    ti.mode = 0o755 if typ in (tarfile.DIRTYPE, tarfile.SYMTYPE) else 0o644
    ti.uid = ti.gid = 1000
    ti.uname = "bruno"
    ti.gname = "bruno"
    ti.linkname = link
    return ti


with tarfile.open("sample.tar.gz", "w:gz") as tf:
    tf.addfile(tinfo("site/", tarfile.DIRTYPE))
    body = b"<p>demo at 198.51.100.7</p>\n" * 30
    tf.addfile(tinfo("site/index.html", size=len(body)), io.BytesIO(body))
    body2 = b"body{margin:0}\n" * 10
    tf.addfile(tinfo("site/style.css", size=len(body2)), io.BytesIO(body2))
    tf.addfile(tinfo("site/current", tarfile.SYMTYPE, link="index.html"))
    tf.addfile(tinfo("site/escape", tarfile.SYMTYPE, link="../../outside"))
