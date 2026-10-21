# Shared test-output parser (prepended to parse_test_output and run_tests).
# parse_tests(text, framework) -> (ok, confidence, data, unparsed)

_TP = {
    # go test
    "go_fail": re.compile(r"^\s*--- FAIL: (\S+) \(([\d.]+)s\)"),
    "go_pass": re.compile(r"^\s*--- PASS: (\S+)"),
    "go_skip": re.compile(r"^\s*--- SKIP: (\S+)"),
    "go_run": re.compile(r"^\s*=== (RUN|PAUSE|CONT|NAME)\s+(\S+)"),
    "go_pkg_ok": re.compile(r"^ok\s+(\S+)\s+(\(cached\)|[\d.]+s)"),
    "go_pkg_fail": re.compile(r"^FAIL\s+(\S+)\s+([\d.]+s|\[[^\]]+\])"),
    "go_bare": re.compile(r"^(PASS|FAIL)$"),
    "go_nofiles": re.compile(r"^\?\s+\S+\s+\[no test files\]"),
    "go_loc": re.compile(r"^\s+([\w./-]+\.go):(\d+): ?(.*)"),
    "go_panic": re.compile(r"^panic: (.*)"),
    "go_build": re.compile(r"^(?:\./)?([\w./-]+\.go):(\d+):(\d+): (.*)"),
    "go_pkghdr": re.compile(r"^# \S+"),
    "go_exit": re.compile(r"^exit status \d+$"),
    # pytest
    "py_summary": re.compile(r"^=*\s*((?:\d+ (?:failed|passed|skipped|errors?|xfailed|xpassed|warnings?|deselected)(?:, )?)+) in [\d.]+s(?: \([^)]*\))?\s*=*$"),
    "py_failed": re.compile(r"^(FAILED|ERROR) (\S+?)(?:::(\S+))?(?: - (.*))?$"),
    "py_progress": re.compile(r"^(?:[\w/.-]+\.py ?)?[.FEsxX]+\s*(?:\[\s*\d+%\])?$"),
    "py_section": re.compile(r"^={3,} ?(.*?) ?={3,}$"),
    "py_testhdr": re.compile(r"^_{3,} (\S.*?) _{3,}$"),
    "py_loc": re.compile(r"^([\w/.-]+\.py):(\d+): (\w+)"),
    "py_e": re.compile(r"^E\s+(.*)"),
    "py_meta": re.compile(r"^(platform |rootdir: |plugins: |cachedir: |collected \d+|collecting |configfile: )"),
    # jest
    "js_suite": re.compile(r"^\s*(PASS|FAIL)\s+(\S+)"),
    "js_tests": re.compile(r"^Tests:\s+(.*)"),
    "js_meta": re.compile(r"^(Test Suites:|Snapshots:|Time:|Ran all test suites)"),
    "js_bullet": re.compile(r"^\s*● (.+)$"),
    "js_at": re.compile(r"^\s*at (?:.*?\()?((?:/|\.|[\w-])[^\s():]*):(\d+):(\d+)\)?$"),
    "js_check": re.compile(r"^\s*[✓✕√×○]\s"),
    # unittest
    "ut_ran": re.compile(r"^Ran (\d+) tests? in [\d.]+s"),
    "ut_ok": re.compile(r"^OK(?: \((.*)\))?$"),
    "ut_failed": re.compile(r"^FAILED \((.*)\)$"),
    "ut_hdr": re.compile(r"^(FAIL|ERROR): (\S+)(?: \(([^)]*)\))?"),
    "ut_sep": re.compile(r"^(=+|-+)$"),
    "ut_dots": re.compile(r"^[.EFsxu]+$"),
    "tb_start": re.compile(r"^Traceback \(most recent call last\):"),
    "tb_frame": re.compile(r'^\s+File "([^"]+)", line (\d+)(?:, in (\S+))?'),
    "tb_exc": re.compile(r"^([A-Za-z_][\w.]*(?:Error|Exception|Exit|Interrupt|Warning)|AssertionError)(?:: (.*))?$"),
    # cargo
    "rs_running": re.compile(r"^running (\d+) tests?$"),
    "rs_test": re.compile(r"^test (\S+) \.\.\. (ok|FAILED|ignored)$"),
    "rs_result": re.compile(r"^test result: (ok|FAILED)\. (\d+) passed; (\d+) failed; (\d+) ignored"),
    "rs_stdout": re.compile(r"^---- (\S+) stdout ----$"),
    "rs_panic": re.compile(r"^thread '([^']+)' panicked at (?:'(.*)', )?([\w/.-]+):(\d+):(\d+):?$"),
    "rs_meta": re.compile(r"^\s*(Compiling|Finished|Running|Doc-tests|Blocking|Downloaded|Updating|note: run with|failures:$|error: test failed)"),
    "rs_name": re.compile(r"^\s{4}(\S+)$"),
}

_FRAMEWORKS = ("gotest", "pytest", "jest", "unittest", "cargo")


def _tp_detect(lines):
    score = dict((f, 0) for f in _FRAMEWORKS)
    for l in lines:
        if _TP["go_fail"].match(l) or _TP["go_pkg_ok"].match(l) or _TP["go_pkg_fail"].match(l) or _TP["go_run"].match(l):
            score["gotest"] += 3
        if _TP["py_summary"].match(l) or _TP["py_failed"].match(l) or _TP["py_testhdr"].match(l) or "test session starts" in l:
            score["pytest"] += 3
        if _TP["js_tests"].match(l) or _TP["js_suite"].match(l) and ("/" in l or ".js" in l or ".ts" in l):
            score["jest"] += 3
        if _TP["ut_ran"].match(l) or _TP["ut_hdr"].match(l) and "(" in l:
            score["unittest"] += 3
        if _TP["rs_result"].match(l) or _TP["rs_test"].match(l) or _TP["rs_running"].match(l):
            score["cargo"] += 3
    best = max(_FRAMEWORKS, key=lambda f: score[f])
    return best if score[best] > 0 else None


def _num(pat, s):
    m = re.search(r"(\d+) " + pat, s)
    return int(m.group(1)) if m else 0


def _in_project(path):
    if not path or "site-packages" in path or "node_modules" in path or "/go/src/" in path or path.startswith("<"):
        return False
    p = path_in_root(path)
    return os.path.abspath(p).startswith(os.path.abspath(ROOT) + os.sep) and os.path.exists(p)


def parse_tests(text, framework="auto"):
    lines = text.splitlines()
    fw = framework if framework and framework != "auto" else _tp_detect(lines)
    if fw not in _FRAMEWORKS:
        return False, 0.0, {"framework": None}, nonblank(lines)[:MAX_UNPARSED]
    fn = {"gotest": _tp_go, "pytest": _tp_pytest, "jest": _tp_jest, "unittest": _tp_unittest, "cargo": _tp_cargo}[fw]
    data, explained, summary = fn(lines)
    data["framework"] = fw
    data["failures"] = data.get("failures", [])[:30]
    nb = [i for i, l in enumerate(lines) if l.strip()]
    unparsed = [lines[i] for i in nb if i not in explained]
    conf = (len(nb) - len(unparsed)) / float(len(nb)) if nb else 0.0
    if not summary:
        conf = min(conf, 0.75)
    if data.get("failed", 0) == 0 and data.get("passed", 0) == 0 and not data.get("build_errors"):
        conf = min(conf, 0.5)
    data["status"] = "fail" if data.get("failed") or data.get("build_errors") else "pass"
    return True, conf, data, unparsed


def _tp_go(lines):
    ex, fails, msgs = set(), [], {}
    passed = skipped = 0
    pk_ok = pk_fail = 0
    build, cur, summary, panic = [], None, False, None
    for i, l in enumerate(lines):
        m = _TP["go_fail"].match(l)
        if m:
            ex.add(i)
            cur = m.group(1)
            fails.append(cur)
            continue
        m = _TP["go_pass"].match(l) or _TP["go_skip"].match(l)
        if m:
            ex.add(i)
            if "SKIP" in l:
                skipped += 1
            else:
                passed += 1
            cur = m.group(1)
            continue
        m = _TP["go_run"].match(l)
        if m:
            ex.add(i)
            cur = m.group(2)
            continue
        if _TP["go_pkg_ok"].match(l):
            ex.add(i)
            pk_ok += 1
            summary = True
            continue
        if _TP["go_pkg_fail"].match(l):
            ex.add(i)
            pk_fail += 1
            summary = True
            continue
        if _TP["go_bare"].match(l) or _TP["go_nofiles"].match(l) or _TP["go_pkghdr"].match(l) or _TP["go_exit"].match(l):
            ex.add(i)
            summary = summary or bool(_TP["go_bare"].match(l))
            continue
        m = _TP["go_loc"].match(l)
        if m and cur:
            ex.add(i)
            msgs.setdefault(cur, []).append((m.group(1), int(m.group(2)), m.group(3)))
            continue
        m = _TP["go_build"].match(l)
        if m:
            ex.add(i)
            build.append({"file": m.group(1), "line": int(m.group(2)), "col": int(m.group(3)), "msg": clip(m.group(4), 300)})
            continue
        m = _TP["go_panic"].match(l)
        if m:
            ex.add(i)
            panic = m.group(1)
            continue
        if cur and l.startswith("        ") and msgs.get(cur):
            # continuation of a multi-line t.Error message
            ex.add(i)
            continue
    failures = []
    for t in fails:
        loc = (msgs.get(t) or [("", 0, "")])[0]
        failures.append({"test": t, "file": loc[0], "line": loc[1], "message": clip(loc[2] or (panic or ""), 300)})
    # a parent test fails when a subtest fails: count leaves only
    leaves = [f for f in failures if not any(o["test"].startswith(f["test"] + "/") for o in failures)]
    data = {"passed": passed, "failed": len(leaves), "skipped": skipped, "failures": leaves,
            "packages": {"ok": pk_ok, "failed": pk_fail}}
    if build:
        data["build_errors"] = build[:20]
    if panic:
        data["panic"] = clip(panic, 300)
    if not passed and pk_ok:
        data["passed"] = 0
    if not leaves and pk_fail and not build:
        data["failed"] = pk_fail
    return data, ex, summary


def _tp_pytest(lines):
    ex, failures, locs = set(), [], {}
    counts, summary = {}, False
    section, hdr = "", None
    for i, l in enumerate(lines):
        m = _TP["py_summary"].match(l)
        if m:
            ex.add(i)
            summary = True
            s = m.group(1)
            for k in ("failed", "passed", "skipped", "xfailed", "xpassed", "deselected"):
                counts[k] = _num(k, s)
            counts["errors"] = _num("errors?", s)
            continue
        m = _TP["py_section"].match(l)
        if m:
            ex.add(i)
            section = m.group(1).lower()
            continue
        m = _TP["py_failed"].match(l)
        if m and (section.startswith("short test summary") or "::" in l):
            ex.add(i)
            failures.append({"test": m.group(2) + ("::" + m.group(3) if m.group(3) else ""), "file": m.group(2),
                             "line": 0, "message": clip(m.group(4) or "", 300), "kind": m.group(1)})
            continue
        m = _TP["py_testhdr"].match(l)
        if m:
            ex.add(i)
            hdr = m.group(1).split("[")[0].split(".")[-1].replace("ERROR at setup of ", "")
            continue
        if _TP["py_meta"].match(l) or (_TP["py_progress"].match(l) and not section.startswith(("fail", "error"))):
            ex.add(i)
            continue
        m = _TP["py_loc"].match(l)
        if m and hdr:
            ex.add(i)
            locs.setdefault(hdr, []).append((m.group(1), int(m.group(2))))
            continue
        if section.startswith(("failures", "errors", "warnings summary")) or (hdr and section.startswith("fail")):
            ex.add(i)  # source excerpt, E lines, captured output inside a failure block
            m = _TP["py_e"].match(l)
            if m and hdr:
                locs.setdefault(hdr + "#msg", []).append(m.group(1))
            continue
    for f in failures:
        name = f["test"].split("::")[-1].split("[")[0]
        loc = locs.get(name) or []
        same = [x for x in loc if x[0] == f["file"]] or loc
        if same:
            f["line"] = same[-1][1]
        if not f["message"] and locs.get(name + "#msg"):
            f["message"] = clip(locs[name + "#msg"][0], 300)
    data = {"passed": counts.get("passed", 0), "failed": counts.get("failed", 0) + counts.get("errors", 0),
            "skipped": counts.get("skipped", 0), "failures": failures}
    if not summary and failures:
        data["failed"] = len(failures)
    return data, ex, summary


def _tp_jest(lines):
    ex, failures, counts, summary = set(), [], {}, False
    cur, suite = None, ""
    for i, l in enumerate(lines):
        m = _TP["js_tests"].match(l)
        if m:
            ex.add(i)
            summary = True
            s = m.group(1)
            for k in ("failed", "passed", "skipped", "todo", "total"):
                counts[k] = _num(k, s)
            cur = None
            continue
        if _TP["js_meta"].match(l):
            ex.add(i)
            cur = None
            continue
        m = _TP["js_suite"].match(l)
        if m and not l.startswith("    "):
            ex.add(i)
            suite = m.group(2)
            cur = None
            continue
        m = _TP["js_bullet"].match(l)
        if m:
            ex.add(i)
            if m.group(1).startswith("Console"):
                cur = {"console": True}
                continue
            cur = {"test": m.group(1).replace(" › ", " > "), "file": suite, "line": 0, "message": ""}
            failures.append(cur)
            continue
        if _TP["js_check"].match(l):
            ex.add(i)
            continue
        if cur is not None:
            ex.add(i)
            if cur.get("console"):
                continue
            m = _TP["js_at"].match(l)
            if m and not cur["line"] and "node_modules" not in m.group(1):
                cur["file"], cur["line"] = rel(m.group(1)), int(m.group(2))
            elif not cur["message"] and l.strip() and not m:
                cur["message"] = clip(l.strip(), 300)
    data = {"passed": counts.get("passed", 0), "failed": counts.get("failed", len(failures)),
            "skipped": counts.get("skipped", 0) + counts.get("todo", 0), "failures": failures}
    return data, ex, summary


def _tp_unittest(lines):
    ex, failures, summary = set(), [], False
    ran, counts, cur, intb = 0, {}, None, False
    for i, l in enumerate(lines):
        m = _TP["ut_ran"].match(l)
        if m:
            ex.add(i)
            ran = int(m.group(1))
            cur = None
            continue
        m = _TP["ut_ok"].match(l) or _TP["ut_failed"].match(l)
        if m:
            ex.add(i)
            summary = True
            s = m.group(1) or ""
            for k in ("failures", "errors", "skipped", "expected failures"):
                mm = re.search(k + r"=(\d+)", s)
                counts[k] = int(mm.group(1)) if mm else 0
            continue
        m = _TP["ut_hdr"].match(l)
        if m:
            ex.add(i)
            cur = {"test": m.group(2) + (" (" + m.group(3) + ")" if m.group(3) else ""), "file": "", "line": 0,
                   "message": "", "kind": m.group(1)}
            failures.append(cur)
            intb = False
            continue
        if _TP["ut_sep"].match(l) or _TP["ut_dots"].match(l):
            ex.add(i)
            continue
        if cur is not None:
            ex.add(i)
            if _TP["tb_start"].match(l):
                intb = True
                continue
            m = _TP["tb_frame"].match(l)
            if m:
                if _in_project(m.group(1)) or not cur["file"]:
                    cur["file"], cur["line"] = rel(m.group(1)), int(m.group(2))
                continue
            m = _TP["tb_exc"].match(l)
            if m and intb:
                cur["message"] = clip(l.strip(), 300)
            continue
    nf = counts.get("failures", 0) + counts.get("errors", 0)
    if not summary:
        nf = len(failures)
    data = {"passed": max(0, ran - nf - counts.get("skipped", 0)), "failed": nf, "skipped": counts.get("skipped", 0),
            "failures": failures}
    return data, ex, summary and ran > 0


def _tp_cargo(lines):
    ex, failures, summary = set(), {}, False
    passed = failed = ignored = 0
    order, cur = [], None
    for i, l in enumerate(lines):
        m = _TP["rs_result"].match(l)
        if m:
            ex.add(i)
            summary = True
            passed += int(m.group(2))
            failed += int(m.group(3))
            ignored += int(m.group(4))
            cur = None
            continue
        m = _TP["rs_test"].match(l)
        if m:
            ex.add(i)
            if m.group(2) == "FAILED" and m.group(1) not in failures:
                failures[m.group(1)] = {"test": m.group(1), "file": "", "line": 0, "message": ""}
                order.append(m.group(1))
            continue
        m = _TP["rs_stdout"].match(l)
        if m:
            ex.add(i)
            cur = m.group(1)
            if cur not in failures:
                failures[cur] = {"test": cur, "file": "", "line": 0, "message": ""}
                order.append(cur)
            continue
        m = _TP["rs_panic"].match(l)
        if m:
            ex.add(i)
            f = failures.get(cur or m.group(1))
            if f is not None:
                f["file"], f["line"] = m.group(3), int(m.group(4))
                if m.group(2):
                    f["message"] = clip(m.group(2), 300)
            continue
        if _TP["rs_running"].match(l) or _TP["rs_meta"].match(l):
            ex.add(i)
            if l.strip() == "failures:":
                cur = None
            continue
        if _TP["rs_name"].match(l) and _TP["rs_name"].match(l).group(1) in failures:
            ex.add(i)
            continue
        if cur is not None:
            ex.add(i)
            f = failures.get(cur)
            if f is not None and not f["message"] and l.strip() and not l.startswith("note:"):
                f["message"] = clip(l.strip(), 300)
    fl = [failures[k] for k in order]
    data = {"passed": passed, "failed": failed or len(fl), "skipped": ignored, "failures": fl}
    return data, ex, summary
