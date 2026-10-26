import sys

from .arquivo import carregar
from .calculo import total


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    c = carregar(argv[0])
    print("%s: %s %s" % (c.get("titular", "?"), total(c), c.get("moeda", "")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
