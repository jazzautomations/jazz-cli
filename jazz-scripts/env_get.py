# env_get: environment variables of the jazz process — "show the env vars",
# "what is GOPATH set to". Lists names by default; a single name lookup shows
# the value ONLY when the name does not look like a secret (KEY, SECRET, TOKEN,
# PASS, PWD, AUTH, CRED, PRIVATE, CERT): those are reported as set/unset with
# redacted:true, never printed. Python 3.8+ stdlib only.
import os

SECRET_BITS = ("KEY", "SECRET", "TOKEN", "PASS", "PWD", "AUTH",
               "CRED", "PRIVATE", "CERT", "SEED", "MNEMONIC")


def sensitive(name):
    n = name.upper()
    return any(b in n for b in SECRET_BITS)


name = arg("name")
prefix = arg("prefix")
if name is not None and prefix is not None:
    fail("pass name or prefix, not both")

env = dict(os.environ)

if name is not None:
    name = str(name)
    if not name or "=" in name:
        fail("name must be a plain variable name")
    if name in env:
        if sensitive(name):
            emit(True, {"name": name, "set": True, "redacted": True,
                        "truncated": False, "text": "%s is set (value redacted)" % name}, 1.0)
        else:
            emit(True, {"name": name, "set": True, "redacted": False,
                        "value": env[name],
                        "truncated": False, "text": "%s=%s" % (name, clip(env[name], 120))}, 1.0)
    else:
        emit(True, {"name": name, "set": False,
                    "truncated": False, "text": "%s is not set" % name}, 1.0)

pfx = str(prefix).upper() if prefix is not None else None
names = sorted(n for n in env if pfx is None or n.upper().startswith(pfx))
vars_out = [{"name": n, "redacted": sensitive(n)} for n in names]
n_red = sum(1 for v in vars_out if v["redacted"])

scope = "matching %s*" % pfx if pfx else "in the environment"
text = "%d variable(s) %s" % (len(vars_out), scope)
if n_red:
    text += " (%d secret-named, values never shown)" % n_red
if vars_out:
    text += "; e.g. %s" % ", ".join(v["name"] for v in vars_out[:5])

emit(True, {"vars": vars_out, "count": len(vars_out), "secret_named": n_red,
            "truncated": False, "text": text}, 1.0)
