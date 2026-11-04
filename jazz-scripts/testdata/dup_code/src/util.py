"""Nothing here repeats anything else in the tree."""


def slug(s):
    return "-".join(s.lower().split())


def pad(s, n):
    return s + " " * max(0, n - len(s))
