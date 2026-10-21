def f(xs):
    r = {}
    for x in xs:
        r[x % 3] = r.get(x % 3, 0) + x
    return max(r, key=r.get)
