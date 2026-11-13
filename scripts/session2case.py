#!/usr/bin/env python3
"""session2case.py: turns a real jazz session into a replay case draft.

    scripts/session2case.py <sessao.jsonl | id> [--name NOME] [--origin TEXTO]
                            [--out DIR] [--mask-entropy] [--stdout]

Reads <id>.jsonl (messages) and <id>.events.jsonl (requests) and writes
testdata/replay/<NOME>/case.json (or prints it with --stdout). The draft keeps
the user's turns, the requested model and a provider script rebuilt in request
order (real 429/410/400 bodies, the model's real replies and tool calls).

Secrets never reach the case:
  1. every string is redacted with the patterns of redact.go (provider keys,
     tokens, PEM blocks, Bearer/Basic, URL userinfo, key=value secrets) and of
     consent.go nlCredRules (the sudo password typed in chat, "a senha é X");
     home paths become ~ and the harness reminders are dropped;
  2. then the whole output is scanned again with a wider net (the same
     prefixes plus anything high-entropy). If something still looks like a
     secret the script FAILS (exit 3) and prints only a masked preview. Pass
     --mask-entropy to also redact the generic high-entropy runs and try again.

The draft is NOT a test yet: it carries "todo" (why, front, expect) and the
replay loader refuses any case with a todo, so a human writes the assertions
before the case becomes part of the gate. TestSession2Case keeps these rules in
step with redact.go (every positive fixture of redact_test.go must vanish).

Needs only the Python 3 standard library.
"""

import argparse
import glob
import json
import math
import os
import re
import sys

MARK = "[REDACTED]"

# ---------- redaction (mirrors redact.go rdRules, mode "on") ----------

_PEM = re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----", re.S)
# a block cut before its END line (truncated output): the base64 lines that follow
_PEM_CUT = re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:(?:\r?\n|\\n)[A-Za-z0-9+/=]+)*")

# provider/vendor tokens with a fixed shape (high confidence)
_PREFIX = [
    r"\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b",            # aws-key
    r"sk-ant-[A-Za-z0-9_\-]{20,}",                           # anthropic-key
    r"sk-or-[A-Za-z0-9_\-]{20,}",                            # openrouter-key
    r"\bsk-(?:proj-|svcacct-|admin-|None-)?[A-Za-z0-9_\-]{20,}",  # openai-key
    r"\bgh[pousr]_[A-Za-z0-9]{30,}",                         # github-token
    r"\bgithub_pat_[A-Za-z0-9_]{30,}",
    r"\bgl(?:pat|dt|ptt|rt|soat)-[A-Za-z0-9_\-]{16,}",       # gitlab-token
    r"\bxox[abposr]-[A-Za-z0-9\-]{10,}",                     # slack-token
    r"https://hooks\.slack\.com/services/[A-Za-z0-9/_\-]+",  # slack-webhook
    r"https://(?:discord(?:app)?\.com)/api/webhooks/[0-9]+/[A-Za-z0-9_\-]+",
    r"\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{10,}",        # stripe-key (pk_ too: harmless)
    r"\bwhsec_[A-Za-z0-9]{20,}",
    r"\bSK[0-9a-f]{32}\b",                                   # twilio-key
    r"\bSG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}",       # sendgrid-key
    r"\bAIza[A-Za-z0-9_\-]{30,}",                            # google-api-key
    r"\bya29\.[A-Za-z0-9_\-]{20,}",                          # google-oauth
    r"\bgsk_[A-Za-z0-9]{20,}",                               # groq-key
    r"\bhf_[A-Za-z0-9]{30,}",                                # hf-token
    r"\bnpm_[A-Za-z0-9]{30,}",                               # npm-token
    r"\bxai-[A-Za-z0-9]{30,}",                               # xai-key
    r"\bnvapi-[A-Za-z0-9_\-]{20,}",                          # nvidia (keys.go presets)
    r"\bcsk-[A-Za-z0-9]{20,}",                               # cerebras
    r"\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{8,}",  # jwt
    r"[A-Za-z0-9]{3}8Q~[A-Za-z0-9_.~\-]{30,}",               # azure client secret
]
_PREFIX_RE = [re.compile(p) for p in _PREFIX]

_AUTH_HDR = re.compile(r"(?i)\b(bearer|basic)\s+([A-Za-z0-9._~+/=\-]{8,})")
_URL_USERINFO = re.compile(r"([A-Za-z][A-Za-z0-9+.\-]*://[^\s:/@]+:)([^\s@/]{3,})(@)")

# key = value secrets (env files, yaml, json, query strings, connection strings)
_KEYNAME = (r"[A-Za-z0-9_.\-]*(?:secret|token|passw(?:or)?d|passwd|pwd|senha|auth|credential|"
            r"api[_\-]?key|access[_\-]?key|private[_\-]?key|accountkey|sharedaccesskey|_key)[A-Za-z0-9_.\-]*"
            r"|sig")
_KV = re.compile(r"(?i)(?<![A-Za-z0-9])(" + _KEYNAME + r")([\"']?\s*(?:=|:)\s*[\"']?)(?!\[REDACTED)([^\s\"',;&}<>]{4,})")

# sudo credential typed in chat (consent.go nlCredRules)
_CRED = [
    re.compile(r"(?i)\bsenha\b[^.\n]{0,40}?(?:é|eh|=|:)\s*[\"']?([^\s\"',;]{3,64})"),
    re.compile(r"(?i)\b(?:sudo|root|admin)\s+(?:senha|pass|password|pw|credencial|credential)s?\s*[:=]?\s*[\"']?([^\s\"',;]{3,64})"),
    re.compile(r"(?i)\b(?:password|pass|pw|credencial|credentials?)\s*[:=]\s*[\"']?([^\s\"',;]{3,64})"),
    re.compile(r"(?im)^[ \t]*(?:root|sudo|admin)\s+[a-zA-Z_][\w.\-]{0,31}\s+([^\s\"',;]{3,64})[ \t]*[!.]?[ \t]*$"),
]
_CRED_STOP = {"é", "eh", "e", "is", "sim", "nao", "não", "ok", "tudo", "isso", "essa", "aquilo", "a", "o", "as", "os",
              "um", "uma", "the", "my", "your", "muito", "forte", "fraca", "fraco", "boa", "bom", "grande", "dificil",
              "facil", "secreta", "segura", "secreto", "mesmo", "certa", "errada", "senha", "password", "sudo", "root",
              "admin", "user", "usuario", "usuário", "login", "wifi"}

# values that are clearly not secrets (keep the case readable)
_NOT_SECRET = re.compile(r"(?i)^(?:\d+|true|false|null|none|nil|bearer|basic|\$\{?\w+\}?|<[^>]*>|\[redacted[^\]]*\]|changeme|example|"
                         r"string|os\.\w+.*|process\.env.*|cfg\.\w+|opts\.\w+|config\.\w+|self\.\w+|this\.\w+)$")

_REMINDER = re.compile(r"\s*<system-reminder>.*?</system-reminder>\s*", re.S)
_HOME = re.compile(r"(/Users/|/home/)[^/\s\"']+")

_ENTROPY_TOKEN = re.compile(r"[A-Za-z0-9+/_=\-]{24,}")


def _entropy(s):
    n = len(s)
    counts = {}
    for ch in s:
        counts[ch] = counts.get(ch, 0) + 1
    return -sum(c / n * math.log2(c / n) for c in counts.values())


def _looks_random(tok):
    """generic high-entropy run: three character classes and high entropy
    (hex digests and plain words have at most two classes)."""
    if MARK in tok or tok.startswith("REDACTED"):
        return False
    classes = sum(bool(re.search(p, tok)) for p in (r"[a-z]", r"[A-Z]", r"[0-9]"))
    return classes >= 3 and _entropy(tok) >= 4.0 and not re.fullmatch(r"[A-Za-z]+(?:[-_][A-Za-z]+)*", tok)


def redact(s, mask_entropy=False):
    """returns s with every secret replaced by [REDACTED]."""
    if not s:
        return s
    s = _REMINDER.sub("\n", s).strip("\n") if "<system-reminder>" in s else s
    s = _PEM.sub(MARK, s)
    s = _PEM_CUT.sub(MARK, s)
    for rx in _PREFIX_RE:
        s = rx.sub(MARK, s)
    s = _AUTH_HDR.sub(lambda m: m.group(1) + " " + MARK, s)
    s = _URL_USERINFO.sub(lambda m: m.group(1) + MARK + m.group(3), s)

    def kv(m):
        val = m.group(3)
        if _NOT_SECRET.match(val):
            return m.group(0)
        return m.group(1) + m.group(2) + MARK
    s = _KV.sub(kv, s)
    for _ in range(4):
        hit = False
        for rx in _CRED:
            for m in rx.finditer(s):
                val = m.group(1)
                if val.lower() in _CRED_STOP or MARK in val or val.startswith("<"):
                    continue
                s = s[:m.start(1)] + MARK + s[m.end(1):]
                hit = True
                break
            if hit:
                break
        if not hit:
            break
    if mask_entropy:
        s = _ENTROPY_TOKEN.sub(lambda m: MARK if _looks_random(m.group(0)) else m.group(0), s)
    s = _HOME.sub("~", s)
    return s


def leftovers(text):
    """the wider final scan: what still looks like a secret (masked previews)."""
    out = []
    for rx in _PREFIX_RE + [_PEM, _PEM_CUT]:
        for m in rx.finditer(text):
            out.append(m.group(0))
    for m in _ENTROPY_TOKEN.finditer(text):
        if _looks_random(m.group(0)):
            out.append(m.group(0))
    for rx in _CRED:
        for m in rx.finditer(text):
            v = m.group(1)
            if v.lower() not in _CRED_STOP and MARK not in v and not v.startswith("<"):
                out.append(v)
    return [t[:4] + "…(%d)" % len(t) for t in out]


def deep(v, f):
    if isinstance(v, str):
        return f(v)
    if isinstance(v, list):
        return [deep(x, f) for x in v]
    if isinstance(v, dict):
        return {k: deep(x, f) for k, x in v.items()}
    return v


# ---------- session reading ----------

def find_session(arg):
    if os.path.isfile(arg):
        return arg
    roots = []
    if os.environ.get("JAZZ_HOME"):
        roots.append(os.path.join(os.environ["JAZZ_HOME"], "sessions"))
    xdg = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    roots.append(os.path.join(xdg, "jazz-cli", "sessions"))
    for r in roots:
        hits = glob.glob(os.path.join(r, "**", arg + ".jsonl"), recursive=True)
        if hits:
            return hits[0]
    sys.exit("sessão não encontrada: %s" % arg)


def read_jsonl(path):
    out = []
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except ValueError:
                    pass  # torn line at the end of a crashed run
    except FileNotFoundError:
        pass
    return out


def build(sess_path, name, origin):
    entries = read_jsonl(sess_path)
    events = read_jsonl(sess_path[: -len(".jsonl")] + ".events.jsonl")
    model, mode, turns, replies = "", "", [], []
    for e in entries:
        t = e.get("t")
        if t == "meta" and not model:
            model = e.get("model", "")
        elif t == "model" and e.get("model"):
            model = model or e["model"]
        elif t == "msg" and e.get("msg"):
            m = e["msg"]
            if m.get("role") == "user":
                txt = _REMINDER.sub("\n", m.get("content", "")).strip()
                if txt:
                    turns.append(txt)
            elif m.get("role") == "assistant":
                replies.append(m)
    for ev in events:
        if ev.get("kind") == "turn.start":
            mode = mode or ev.get("mode", "")
            model = model or ev.get("model", "")

    seq, refs = [], []
    ri = 0
    for ev in events:
        if ev.get("kind") != "request.end":
            continue
        refs.append("%s %s" % (ev.get("model", "?"), ev.get("status", "ok") if ev.get("error") else "ok"))
        if ev.get("error"):
            st = int(ev.get("status") or 0)
            body = ev.get("payload") or ev.get("error", "")
            if st:
                seq.append({"status": st, "body": body})
            else:
                seq.append({"err_chunk": body})
            continue
        if ri >= len(replies):
            seq.append({"text": "(resposta não gravada)"})
            continue
        m = replies[ri]
        ri += 1
        r = {}
        if m.get("content"):
            r["text"] = m["content"]
        calls = []
        for tc in m.get("tool_calls") or []:
            raw = tc.get("args", "")
            try:
                args = json.loads(raw) if raw else {}
            except ValueError:
                args = raw  # JSON quebrado do modelo: fica cru, byte a byte
            calls.append({"name": tc.get("name", ""), "args": args})
        if calls:
            r["calls"] = calls
        seq.append(r or {"text": ""})

    case = {
        "name": name,
        "origin": origin,
        "why": "",
        "front": "",
        "todo": [
            "why: o que a sessão real mostrou (1 frase)",
            "front: F1..F4 se o conserto ainda não entrou, ou vazio",
            "expect: as asserções (o que NÃO pode acontecer de novo)",
            "provider.seq: conferir a ordem e cortar o que não importa",
            "requests originais: " + ", ".join(refs[:40]),
        ],
        "entry": "agent",
        "mode": mode or "auto",
        "model": model,
        "provider": {"seq": seq},
        "turns": turns,
        "expect": {},
    }
    return case


def main():
    ap = argparse.ArgumentParser(description="converte uma sessão real em rascunho de caso de replay (com segredo redigido)")
    ap.add_argument("session", help="caminho do <id>.jsonl ou o id da sessão")
    ap.add_argument("--name", help="nome do caso (pasta em testdata/replay)")
    ap.add_argument("--origin", help="máquina + sessão (default: o id)")
    ap.add_argument("--out", default=os.path.join("testdata", "replay"), help="pasta dos casos")
    ap.add_argument("--stdout", action="store_true", help="imprime o case.json em vez de gravar")
    ap.add_argument("--mask-entropy", action="store_true", help="também redige sequências genéricas de alta entropia")
    a = ap.parse_args()

    path = find_session(a.session)
    sid = os.path.basename(path)[: -len(".jsonl")]
    name = a.name or sid
    if not re.fullmatch(r"[a-z0-9][a-z0-9\-]*", name):
        sys.exit("nome do caso inválido (use minúsculas, números e '-'): %r" % name)
    case = build(path, name, a.origin or sid)
    case = deep(case, lambda s: redact(s, a.mask_entropy))
    text = json.dumps(case, ensure_ascii=False, indent=2) + "\n"
    left = leftovers(text)
    if left:
        sys.stderr.write("RECUSADO: ainda há %d trecho(s) com cara de segredo: %s\n" % (len(left), ", ".join(left[:10])))
        sys.stderr.write("revise a sessão; para redigir também sequências de alta entropia use --mask-entropy\n")
        sys.exit(3)
    if a.stdout:
        sys.stdout.write(text)
        return
    d = os.path.join(a.out, name)
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, "case.json")
    if os.path.exists(p):
        sys.exit("já existe: %s (apague ou use outro --name)" % p)
    with open(p, "w", encoding="utf-8") as f:
        f.write(text)
    print("rascunho: %s — preencha os campos de todo; o replay recusa o caso até lá" % p)


if __name__ == "__main__":
    main()
