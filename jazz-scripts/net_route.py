# net_route: the kernel IPv4/IPv6 routing table — "show the routing table",
# "which gateway for this subnet". Reads /proc/net/route and
# /proc/net/ipv6_route directly (no `ip route`), sorted: default route first,
# then longest prefix, then iface — deterministic. Linux /proc only.
# Python 3.8+ stdlib only.
import ipaddress

if not os.path.isdir("/proc"):
    fail("no /proc on this system (needs Linux procfs)")

warnings = []
routes = []


def hex_ip(h):
    """8-hex-digit little-endian IPv4 as stored in /proc/net/route -> dotted quad."""
    try:
        b = bytes.fromhex(h)
        return ".".join(str(x) for x in reversed(b))
    except ValueError:
        return None


def mask_bits(h):
    try:
        return bin(int(h, 16)).count("1")
    except ValueError:
        return None


# ---- IPv4 -----------------------------------------------------------------
try:
    with open("/proc/net/route") as f:
        next(f, None)
        for line in f:
            parts = line.split()
            if len(parts) < 8:
                continue
            iface, dest, gw, flags, _, _, _, mask = parts[:8]
            if not (int(flags, 16) & 0x1):  # RTF_UP
                continue
            bits = mask_bits(mask)
            if bits is None:
                continue
            network = hex_ip(dest) or dest
            gateway = hex_ip(gw) or gw
            default = dest == "00000000" and bits == 0
            routes.append({
                "dst": "default" if default else "%s/%d" % (network, bits),
                "gateway": None if gw == "00000000" else gateway,
                "iface": iface, "family": "ipv4", "default": bool(default)})
except (IOError, OSError):
    warnings.append("cannot read /proc/net/route")

# ---- IPv6 -----------------------------------------------------------------
def v6(h):
    try:
        return str(ipaddress.IPv6Address(int(h, 16)))
    except ValueError:
        return None


try:
    with open("/proc/net/ipv6_route") as f:
        for line in f:
            p = line.split()
            if len(p) < 10:
                continue
            dest, plen, _src, _slen, gw, _metric, _ref, _use, flags, iface = p[:10]
            if not (int(flags, 16) & 0x1):
                continue
            bits = int(plen, 16)
            addr = v6(dest)
            if addr is None:
                continue
            dst = "default" if bits == 0 else "%s/%d" % (addr, bits)
            routes.append({"dst": dst, "iface": iface,
                           "gateway": None if int(gw, 16) == 0 else v6(gw),
                           "family": "ipv6", "default": bits == 0})
except (IOError, OSError):
    pass  # no ipv6 is common, not a warning

# /proc/net/ipv6_route repeats a dst per metric; metric is dropped, so dedupe
uniq, seen = [], set()
for r in routes:
    k = (r["dst"], r["gateway"], r["iface"], r["family"])
    if k not in seen:
        seen.add(k)
        uniq.append(r)
routes = uniq
routes.sort(key=lambda r: (not r["default"], r["family"], r["iface"], r["dst"]))
count = len(routes)
defaults = [r for r in routes if r["default"]]
text = "%d route(s)" % count
if defaults:
    d = defaults[0]
    text += "; default via %s dev %s" % (d["gateway"] or "link", d["iface"])
else:
    text += "; no default route"
if count > 1:
    text += "; first: %s" % routes[0]["dst"]

emit(True, {"routes": routes, "count": count,
            "truncated": False, "text": text}, 1.0, [], warnings)
