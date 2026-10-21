# http_check: one HTTP request to a loopback/LAN URL (remote only with allow_remote). Never follows
# redirects (the Location is reported) and ignores proxy variables.
import ipaddress
import socket
import time

try:
    from urllib.parse import urlsplit
    import urllib.request as ureq
    import urllib.error as uerr
except ImportError:  # pragma: no cover - Python 2
    fail("python 3 is required")

url = str(arg("url", "")).strip()
if not re.match(r"^https?://", url):
    url = "http://" + url
u = urlsplit(url)
host = u.hostname or ""
if not host:
    fail("url has no host")
try:
    port = u.port
except ValueError:
    fail("bad port in url")
timeout = int(arg("timeout", 10))
method = str(arg("method", "GET")).upper()

try:
    infos = socket.getaddrinfo(host, port or (443 if u.scheme == "https" else 80), 0, socket.SOCK_STREAM)
except socket.error as e:
    fail("cannot resolve %s: %s" % (host, e))
ips = sorted(set(i[4][0] for i in infos))


def private(ip):
    a = ipaddress.ip_address(ip.split("%")[0])
    if a.version == 4 and a in ipaddress.ip_network("100.64.0.0/10"):
        return True  # CGNAT / tailnet
    return a.is_loopback or a.is_private or a.is_link_local


if not all(private(ip) for ip in ips) and not ARGS.get("allow_remote"):
    fail("%s resolves to a public address (%s); http_check is for local/LAN services unless allow_remote is true"
         % (host, ", ".join(ips[:3])))


class NoRedirect(ureq.HTTPRedirectHandler):
    def redirect_request(self, *a, **kw):
        return None


opener = ureq.build_opener(ureq.ProxyHandler({}), NoRedirect())
req = ureq.Request(url, method=method, headers={"User-Agent": "jazz-http-check/1"})
t0 = time.time()
status, headers, body, err = 0, {}, b"", ""
try:
    resp = opener.open(req, timeout=timeout)
    status, headers = resp.getcode(), resp.headers
    body = resp.read(4096) if method == "GET" else b""
except uerr.HTTPError as e:
    status, headers = e.code, e.headers
    try:
        body = e.read(4096)
    except Exception:
        body = b""
except (uerr.URLError, socket.error, ValueError) as e:
    err = str(getattr(e, "reason", e))
ms = int((time.time() - t0) * 1000)
if not status:
    fail("request failed: %s" % err, data={"url": url, "ms": ms, "ips": ips})
keep = ("content-type", "content-length", "location", "server", "cache-control", "www-authenticate", "set-cookie")
hdr = {}
for k in keep:
    v = headers.get(k) if headers is not None else None
    if v:
        hdr[k] = clip(v if k != "set-cookie" else re.sub(r"=[^;]*", "=…", v), 200)
data = {"url": url, "status": status, "ms": ms, "headers": hdr, "ips": ips,
        "body_head": clip(body.decode("utf-8", "replace"), 600)}
exp = arg("expect_status")
warn = []
if exp is not None:
    data["matches_expected"] = status == int(exp)
    if status != int(exp):
        warn.append("expected status %s, got %d" % (exp, status))
if 300 <= status < 400:
    warn.append("redirect not followed: %s" % hdr.get("location", "?"))
emit(True, data, 1.0, [], warn)
