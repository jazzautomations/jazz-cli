"""Encoder and decoder of fastjson 2.x."""

import json as _std
from decimal import Decimal


class FastJSONError(ValueError):
    """Invalid JSON input, or an object that cannot be encoded."""


def decode(data, *, decimal=False):
    """Parse JSON text (str) or UTF-8 bytes into Python objects.

    decimal=True returns every number with a fraction or exponent as
    decimal.Decimal, keeping its digits ("10.50" -> Decimal("10.50")).
    Integers are always int.
    """
    if isinstance(data, (bytes, bytearray)):
        data = bytes(data).decode("utf-8")
    if not isinstance(data, str):
        raise FastJSONError("decode() takes str or bytes, not %s" % type(data).__name__)
    try:
        return _std.loads(data, parse_float=Decimal if decimal else float)
    except ValueError as e:
        raise FastJSONError(str(e))


def _enc(obj, indent, sort, level, out):
    if obj is None:
        out.append("null")
    elif obj is True:
        out.append("true")
    elif obj is False:
        out.append("false")
    elif isinstance(obj, Decimal):
        if not obj.is_finite():
            raise FastJSONError("cannot encode %s" % obj)
        out.append(str(obj))
    elif isinstance(obj, int):
        out.append(str(int(obj)))
    elif isinstance(obj, float):
        if obj != obj or obj in (float("inf"), float("-inf")):
            raise FastJSONError("cannot encode %r" % obj)
        out.append(repr(obj))
    elif isinstance(obj, str):
        out.append(_std.dumps(obj, ensure_ascii=False))
    elif isinstance(obj, (list, tuple)):
        if not obj:
            out.append("[]")
            return
        out.append("[")
        for i, v in enumerate(obj):
            if i:
                out.append(",")
            if indent:
                out.append("\n" + " " * (indent * (level + 1)))
            _enc(v, indent, sort, level + 1, out)
        if indent:
            out.append("\n" + " " * (indent * level))
        out.append("]")
    elif isinstance(obj, dict):
        if not obj:
            out.append("{}")
            return
        keys = list(obj.keys())
        for k in keys:
            if not isinstance(k, str):
                raise FastJSONError("object keys must be str, not %s" % type(k).__name__)
        if sort:
            keys.sort()
        out.append("{")
        for i, k in enumerate(keys):
            if i:
                out.append(",")
            if indent:
                out.append("\n" + " " * (indent * (level + 1)))
            out.append(_std.dumps(k, ensure_ascii=False))
            out.append(": " if indent else ":")
            _enc(obj[k], indent, sort, level + 1, out)
        if indent:
            out.append("\n" + " " * (indent * level))
        out.append("}")
    else:
        raise FastJSONError("cannot encode %s" % type(obj).__name__)


def encode(obj, *, indent=0, sort=False):
    """Serialize obj to JSON as UTF-8 **bytes** (not str).

    indent=N pretty-prints with N spaces per level (0 = compact, no spaces).
    sort=True writes object keys in alphabetical order.
    Decimal values are written with all their digits (Decimal("10.50") -> 10.50).
    No trailing newline.
    """
    out = []
    _enc(obj, int(indent or 0), bool(sort), 0, out)
    return "".join(out).encode("utf-8")
