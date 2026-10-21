# parse_test_output: summarize test runner output (go test, pytest, jest, unittest, cargo).
text, source, code = get_input()
ok, conf, data, unparsed = parse_tests(text, arg("framework", "auto"))
if code is not None:
    data["exit_code"] = code
if not ok:
    fail("unrecognized test output (no go test/pytest/jest/unittest/cargo markers)", unparsed=unparsed)
emit(True, data, conf, unparsed)
