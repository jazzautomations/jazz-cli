"""Streaming helpers (fastjson 2.1): one JSON document per line."""

from ._codec import decode, encode


def iter_lines(fp, *, decimal=False):
    """Yield one decoded object per non-empty line of a text or binary file."""
    for line in fp:
        if isinstance(line, bytes):
            line = line.decode("utf-8")
        if line.strip():
            yield decode(line, decimal=decimal)


def write_lines(fp, objs, *, sort=False):
    """Write each object as compact JSON on its own line (binary file)."""
    for o in objs:
        fp.write(encode(o, sort=sort) + b"\n")
