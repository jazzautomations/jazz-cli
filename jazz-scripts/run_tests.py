# run_tests: run the project's test command (jazz fills cmd from project detection) and parse it.
cmd = arg("cmd")
if not cmd:
    fail("no test command: the project type was not detected; pass cmd")
code, out = run(str(cmd), timeout=int(arg("timeout", 55)))
ok, conf, data, unparsed = parse_tests(out, arg("framework", "auto"))
if not ok:
    tail = nonblank(out.splitlines())[-20:]
    emit(True, {"cmd": cmd, "exit_code": code, "status": "pass" if code == 0 else "fail", "framework": None},
         0.3, tail, ["output not recognized; showing the last lines"])
data["cmd"] = cmd
data["exit_code"] = code
if code != 0 and data.get("status") == "pass":
    data["status"] = "fail"
emit(True, data, conf, unparsed)
