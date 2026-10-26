"""fastjson 2: a small JSON codec with exact decimals.

The 2.x API is ``decode``/``encode`` (bytes) and ``read``/``write`` (files).
The 1.x functions ``loads``, ``dumps``, ``load`` and ``dump`` were removed in
2.0; see docs/migracao-1x.md.
"""

from ._codec import FastJSONError, decode, encode
from .files import read, write

__version__ = "2.1.0"
__all__ = ["decode", "encode", "read", "write", "FastJSONError", "__version__"]
