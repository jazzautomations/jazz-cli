# tree_summary: directories down to depth with file counts and lines of code by extension.
top = path_in_root(arg("path", "."))
depth = int(arg("depth", 2))
if not os.path.isdir(top):
    fail("not a directory: %s" % rel(top))
BIN = set(".png .jpg .jpeg .gif .webp .ico .pdf .zip .gz .tgz .xz .bz2 .7z .jar .class .o .a .so .dylib .dll .exe "
          ".bin .woff .woff2 .ttf .otf .mp3 .mp4 .mov .wav .sqlite .db .pyc .lock".split())

dirs = {}
total_files = total_loc = 0
truncated = False
for p in walk_files(top, limit=20000):
    total_files += 1
    d = os.path.relpath(os.path.dirname(p), top)
    parts = [] if d == "." else d.split(os.sep)
    key = "." if not parts else "/".join(parts[:depth]) if depth > 0 else "."
    ext = os.path.splitext(p)[1].lower() or os.path.basename(p)
    loc = 0
    if ext not in BIN:
        try:
            if os.path.getsize(p) <= (1 << 20):
                with open(p, "rb") as f:
                    loc = f.read().count(b"\n")
        except (IOError, OSError):
            pass
    total_loc += loc
    e = dirs.setdefault(key, {"path": key, "files": 0, "loc": 0, "by_ext": {}})
    e["files"] += 1
    e["loc"] += loc
    x = e["by_ext"].setdefault(ext, [0, 0])
    x[0] += 1
    x[1] += loc
if total_files >= 20000:
    truncated = True
out = []
for k in sorted(dirs):
    e = dirs[k]
    top_ext = sorted(e["by_ext"].items(), key=lambda kv: (-kv[1][1], -kv[1][0]))[:5]
    e["by_ext"] = dict((x, {"files": v[0], "loc": v[1]}) for x, v in top_ext)
    out.append(e)
out = sorted(out, key=lambda e: e["path"])[:80]
warn = ["stopped at 20000 files"] if truncated else []
if len(dirs) > 80:
    warn.append("%d directories; showing 80" % len(dirs))
emit(True, {"root": rel(top), "depth": depth, "files": total_files, "loc": total_loc, "dirs": out}, 1.0, [], warn)
