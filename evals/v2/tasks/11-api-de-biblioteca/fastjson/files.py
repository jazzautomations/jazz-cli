"""File helpers of fastjson 2.x."""

from ._codec import decode, encode


def read(path, *, decimal=False):
    """Read and decode the JSON file at path (UTF-8). See decode() for decimal."""
    with open(path, "rb") as f:
        return decode(f.read(), decimal=decimal)


def write(path, obj, *, indent=0, sort=False, newline=True):
    """Encode obj and write it to path. Note the order: path first, then obj.

    indent and sort as in encode(); newline=True ends the file with "\\n".
    """
    data = encode(obj, indent=indent, sort=sort)
    with open(path, "wb") as f:
        f.write(data + (b"\n" if newline else b""))
