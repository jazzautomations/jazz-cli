# net_ifaces: network interfaces straight from /sys/class/net and /proc/net
# (no ip/ifconfig): state, mtu, MAC (masked by default), IPv4/IPv6 addresses,
# rx/tx bytes/packets/errors, link speed, and the default-route interface.
# Python 3.8+ stdlib only. No network traffic, no subprocess.
import ipaddress
import socket
import struct
import time

T0 = time.time()
BUDGET_S = 4.0
SYS_NET = "/sys/class/net"

if not os.path.isdir(SYS_NET):
    fail("no %s on this system (Linux only)" % SYS_NET)


def as_bool(v, default):
    if v is None:
        return default
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() not in ("0", "false", "no", "off", "")


_name = arg("name")
name_filter = str(_name).strip() if _name not in (None, "") else None
include_lo = as_bool(ARGS.get("include_lo"), False)
full_mac = as_bool(ARGS.get("full_mac"), False)

IFF_UP = 0x1
warnings = []


def read_sys(iface, attr):
    """Text of /sys/class/net/<iface>/<attr>, or None (EINVAL on wifi speed, etc.)."""
    try:
        with open(os.path.join(SYS_NET, iface, attr)) as f:
            return f.read().strip()
    except (IOError, OSError):
        return None


def read_int(iface, attr, base=10):
    v = read_sys(iface, attr)
    if v is None:
        return None
    try:
        return int(v, base)
    except ValueError:
        return None


def mask_mac(mac):
    """OUI (vendor) kept, device half hidden: aa:bb:cc:xx:xx:xx."""
    parts = mac.split(":")
    if len(parts) != 6:
        return mac
    return ":".join(parts[:3] + ["xx", "xx", "xx"])


def human(n):
    """Decimal units, one decimal: 1234567 -> '1.2 MB'."""
    if n is None:
        return "?"
    units = ["B", "kB", "MB", "GB", "TB"]
    x = float(n)
    for u in units:
        if x < 1000 or u == units[-1]:
            return ("%d %s" % (x, u)) if u == "B" else ("%.1f %s" % (x, u))
        x /= 1000.0
    return "%d B" % n


def parse_dev():
    """/proc/net/dev -> {iface: {rx_bytes, rx_packets, rx_errors, tx_bytes, tx_packets, tx_errors}}."""
    out = {}
    try:
        with open("/proc/net/dev") as f:
            lines = f.read().splitlines()
    except (IOError, OSError):
        warnings.append("cannot read /proc/net/dev")
        return out
    for line in lines[2:]:
        if ":" not in line:
            continue
        name, rest = line.split(":", 1)
        cols = rest.split()
        if len(cols) < 11:
            continue
        try:
            out[name.strip()] = {
                "rx_bytes": int(cols[0]), "rx_packets": int(cols[1]), "rx_errors": int(cols[2]),
                "tx_bytes": int(cols[8]), "tx_packets": int(cols[9]), "tx_errors": int(cols[10])}
        except ValueError:
            continue
    return out


def hex_le_ipv4(h):
    """'010FA8C0' (little-endian hex as in /proc/net/route) -> '192.168.15.1'."""
    return str(ipaddress.IPv4Address(struct.unpack("<I", bytes.fromhex(h))[0]))


def parse_route():
    """/proc/net/route -> (default iface or None, [(network, iface)] sorted longest prefix first)."""
    default, nets = None, []
    best_metric = None
    try:
        with open("/proc/net/route") as f:
            lines = f.read().splitlines()
    except (IOError, OSError):
        warnings.append("cannot read /proc/net/route")
        return None, []
    for line in lines[1:]:
        cols = line.split()
        if len(cols) < 8:
            continue
        iface, dest, flags, metric, mask = cols[0], cols[1], cols[3], cols[6], cols[7]
        try:
            flags_i, metric_i = int(flags, 16), int(metric)
            if not flags_i & 0x1:  # RTF_UP
                continue
            if dest == "00000000" and mask == "00000000":
                if best_metric is None or metric_i < best_metric:
                    default, best_metric = iface, metric_i
                continue
            net = ipaddress.IPv4Network("%s/%s" % (hex_le_ipv4(dest), hex_le_ipv4(mask)), strict=False)
        except (ValueError, struct.error):
            continue
        nets.append((net, iface))
    nets.sort(key=lambda t: (-t[0].prefixlen, int(t[0].network_address), t[1]))
    return default, nets


def parse_fib_trie_local():
    """Local IPv4 addresses: '/32 host LOCAL' leaves of the Local table in /proc/net/fib_trie."""
    addrs = set()
    try:
        with open("/proc/net/fib_trie") as f:
            lines = f.read().splitlines()
    except (IOError, OSError):
        warnings.append("cannot read /proc/net/fib_trie")
        return addrs
    in_local, last = False, None
    for line in lines:
        s = line.strip()
        if s.endswith(":") and not s.startswith(("|--", "+--")):
            in_local = s == "Local:"
            last = None
            continue
        if not in_local:
            continue
        if s.startswith("|--"):
            last = s[3:].strip()
            continue
        if s.startswith("/32 host LOCAL") and last:
            try:
                addrs.add(str(ipaddress.IPv4Address(last)))
            except ValueError:
                pass
    return addrs


def parse_if_inet6():
    """/proc/net/if_inet6 -> {iface: ['addr/prefix', ...]} (sorted)."""
    out = {}
    try:
        with open("/proc/net/if_inet6") as f:
            lines = f.read().splitlines()
    except (IOError, OSError):
        return out  # IPv6 disabled: silent, ipv6 stays []
    for line in lines:
        cols = line.split()
        if len(cols) < 6:
            continue
        try:
            addr = ipaddress.IPv6Address(int(cols[0], 16))
            plen = int(cols[2], 16)
        except ValueError:
            continue
        out.setdefault(cols[5], []).append("%s/%d" % (addr.compressed, plen))
    for k in out:
        out[k].sort()
    return out


def ioctl_ipv4(iface):
    """Primary IPv4 of iface via SIOCGIFADDR on a throwaway UDP socket (no packet is
    ever sent). Fallback for addresses the main route table cannot attribute, e.g. a
    VPN whose routes live in another table. None when the iface has no IPv4."""
    try:
        import fcntl
    except ImportError:
        return None
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    except OSError:
        return None
    try:
        req = struct.pack("256s", iface.encode("utf-8")[:15])
        res = fcntl.ioctl(s.fileno(), 0x8915, req)  # SIOCGIFADDR
        return socket.inet_ntoa(res[20:24])
    except (OSError, ValueError):
        return None
    finally:
        s.close()


try:
    names = sorted(os.listdir(SYS_NET))
except (IOError, OSError) as e:
    fail("cannot list %s: %s" % (SYS_NET, e))

if name_filter is not None:
    exact = [n for n in names if n == name_filter]
    names = exact or [n for n in names if name_filter.lower() in n.lower()]
    if not names:
        emit(True, {"ifaces": [], "default_route_iface": None, "truncated": False,
                    "text": "no interface matches %r" % name_filter}, 0.9, [], warnings)
elif not include_lo:
    names = [n for n in names if n != "lo"]

dev = parse_dev()
default_iface, nets = parse_route()
local4 = parse_fib_trie_local()
inet6 = parse_if_inet6()

# attribute each local IPv4 to the iface owning the longest matching route
ipv4_by_iface = {}
unattributed = []
for a in sorted(local4, key=lambda x: int(ipaddress.IPv4Address(x))):
    ip = ipaddress.IPv4Address(a)
    owner = "lo" if ip.is_loopback else None
    if owner is None:
        for net, iface in nets:
            if ip in net:
                owner = iface
                break
    if owner is None:
        unattributed.append(a)
    else:
        ipv4_by_iface.setdefault(owner, []).append(a)

ifaces, truncated = [], False
for n in names:
    if time.time() - T0 > BUDGET_S:
        truncated = True
        warnings.append("time budget reached after %d interfaces" % len(ifaces))
        break
    flags = read_int(n, "flags", 16) or 0
    carrier = read_int(n, "carrier")
    speed = read_int(n, "speed")
    mac = read_sys(n, "address") or ""
    if not mac or set(mac) <= set("0:"):
        mac = None  # lo / tun have no hardware address
    elif not full_mac:
        mac = mask_mac(mac)
    v4 = list(ipv4_by_iface.get(n, []))
    if not v4 and unattributed:
        got = ioctl_ipv4(n)
        if got in unattributed:
            unattributed.remove(got)
            v4 = [got]
    ifaces.append({
        "name": n,
        "state": read_sys(n, "operstate") or "unknown",
        "up": bool(flags & IFF_UP) and carrier == 1,
        "carrier": None if carrier is None else carrier == 1,
        "mtu": read_int(n, "mtu"),
        "mac": mac,
        "ipv4": v4,
        "ipv6": inet6.get(n, []),
        "rx_bytes": dev.get(n, {}).get("rx_bytes"),
        "tx_bytes": dev.get(n, {}).get("tx_bytes"),
        "rx_packets": dev.get(n, {}).get("rx_packets"),
        "tx_packets": dev.get(n, {}).get("tx_packets"),
        "rx_errors": dev.get(n, {}).get("rx_errors"),
        "tx_errors": dev.get(n, {}).get("tx_errors"),
        "speed_mbps": speed if (speed is not None and speed > 0) else None,
    })

if unattributed and (name_filter is None):
    warnings.append("%d local IPv4 address(es) not attributed to an interface" % len(unattributed))

up = [i for i in ifaces if i["up"]]
if up:
    bits = []
    for i in up[:6]:
        addr = i["ipv4"][0] if i["ipv4"] else ("no IPv4" if not i["ipv6"] else "IPv6 only")
        bits.append("%s %s (rx %s, tx %s)" % (i["name"], addr, human(i["rx_bytes"]), human(i["tx_bytes"])))
    text = "%d iface(s) up: %s%s" % (len(up), "; ".join(bits), "; …" if len(up) > 6 else "")
else:
    text = "no interface up (%d listed)" % len(ifaces)
if default_iface:
    text += "; default via %s" % default_iface
else:
    text += "; no default route"
if any(i["tx_errors"] or i["rx_errors"] for i in ifaces):
    text += " · errors on " + ", ".join(i["name"] for i in ifaces if i["tx_errors"] or i["rx_errors"])

emit(True, {"ifaces": ifaces, "default_route_iface": default_iface, "truncated": truncated,
            "text": text}, 1.0 if not truncated else 0.6, [], warnings)
