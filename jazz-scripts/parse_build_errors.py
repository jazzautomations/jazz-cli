# parse_build_errors: compiler/linter diagnostics (gcc/clang/go/tsc/eslint/npm/cargo) as a list.
text, source, code = get_input()
lines = text.splitlines()

R_GCC = re.compile(r"^(?:\./)?([^\s:()]+\.[A-Za-z0-9+]+):(\d+)(?::(\d+))?: ?(?:(fatal error|error|warning|note|vet)(?:\[([\w-]+)\])?: )?(.+)$")
R_TSC_PAREN = re.compile(r"^(\S+?\.[jt]sx?)\((\d+),(\d+)\): (error|warning) (TS\d+): (.*)$")
R_TSC_DASH = re.compile(r"^(\S+?\.[jt]sx?):(\d+):(\d+) - (error|warning) (TS\d+): (.*)$")
R_ESL_FILE = re.compile(r"^(/\S+|[\w.][\w./-]*\.(?:[jt]sx?|vue|mjs|cjs))$")
R_ESL_ITEM = re.compile(r"^\s+(\d+):(\d+)\s+(error|warning)\s+(.+?)(?:\s{2,}([\w@/-]+))?$")
R_RUST_HDR = re.compile(r"^(error|warning)(?:\[(E\d+)\])?: (.+)$")
R_RUST_LOC = re.compile(r"^\s*--> ([^:]+):(\d+):(\d+)$")
R_NPM = re.compile(r"^npm (?:ERR!|error) (?:code (\S+)|(.*))$")
R_CONTEXT = re.compile(r"^\s*(\d+\s*)?[|^~]|^\s*\^|^\s+\|\s|^\s*= (note|help):|^\s*\d+ (problems?|errors?|warnings?)|"
                       r"^✖ \d+ problem|^# \S+$|^In file included from|^\S+: In (function|member function)|"
                       r"^Found \d+ errors?|^make(\[\d+\])?: \*\*\*|^error: could not compile|^\s*Compiling |"
                       r"^warning: build failed|^For more information about this error|^Some errors have detailed")

errs, ex, esl_file, rust = [], set(), None, None
kinds = set()
for i, l in enumerate(lines):
    m = R_TSC_PAREN.match(l) or R_TSC_DASH.match(l)
    if m:
        ex.add(i)
        kinds.add("tsc")
        errs.append({"file": m.group(1), "line": int(m.group(2)), "col": int(m.group(3)), "severity": m.group(4),
                     "code": m.group(5), "msg": clip(m.group(6), 300)})
        continue
    m = R_RUST_HDR.match(l)
    if m and not l.startswith("error: could not compile"):
        ex.add(i)
        kinds.add("cargo")
        rust = {"file": "", "line": 0, "col": 0, "severity": m.group(1), "code": m.group(2) or "", "msg": clip(m.group(3), 300)}
        errs.append(rust)
        continue
    m = R_RUST_LOC.match(l)
    if m and rust is not None:
        ex.add(i)
        if not rust["file"]:
            rust["file"], rust["line"], rust["col"] = m.group(1), int(m.group(2)), int(m.group(3))
        continue
    m = R_ESL_ITEM.match(l)
    if m and esl_file:
        ex.add(i)
        kinds.add("eslint")
        errs.append({"file": esl_file, "line": int(m.group(1)), "col": int(m.group(2)), "severity": m.group(3),
                     "code": m.group(5) or "", "msg": clip(m.group(4).strip(), 300)})
        continue
    m = R_GCC.match(l)
    if m and not l.startswith(("http:", "https:")):
        ex.add(i)
        kinds.add("go" if m.group(1).endswith(".go") else "cc")
        sev = m.group(4) or "error"
        if sev == "note":
            continue
        errs.append({"file": m.group(1), "line": int(m.group(2)), "col": int(m.group(3) or 0),
                     "severity": "error" if sev in ("fatal error", "vet") else sev, "code": m.group(5) or "",
                     "msg": clip(m.group(6), 300)})
        continue
    m = R_ESL_FILE.match(l)
    if m:
        ex.add(i)
        esl_file = rel(m.group(1))
        continue
    m = R_NPM.match(l)
    if m:
        ex.add(i)
        kinds.add("npm")
        if m.group(1):
            errs.append({"file": "", "line": 0, "col": 0, "severity": "error", "code": m.group(1), "msg": ""})
        elif errs and errs[-1]["file"] == "" and not errs[-1]["msg"] and m.group(2).strip():
            errs[-1]["msg"] = clip(m.group(2).strip(), 300)
        continue
    if R_CONTEXT.match(l) or not l.strip():
        ex.add(i)
        continue
    if l.startswith((" ", "\t")) and errs:
        ex.add(i)  # continuation / source excerpt under a diagnostic
        continue

nb = [i for i, l in enumerate(lines) if l.strip()]
unparsed = [lines[i] for i in nb if i not in ex]
conf = (len(nb) - len(unparsed)) / float(len(nb)) if nb else 0.0
data = {"errors": [e for e in errs if e["severity"] == "error"][:40],
        "warnings": [e for e in errs if e["severity"] == "warning"][:20],
        "error_count": sum(1 for e in errs if e["severity"] == "error"),
        "warning_count": sum(1 for e in errs if e["severity"] == "warning"),
        "tools": sorted(kinds)}
if code is not None:
    data["exit_code"] = code
if not errs:
    if code == 0 or (code is None and nb == []):
        emit(True, data, 1.0 if code == 0 else 0.5, unparsed[-10:], ["no diagnostics found"])
    fail("no compiler/linter diagnostics recognized", data=data, unparsed=unparsed)
emit(True, data, conf, unparsed)
