---
name: verify-done
description: Evidence before claiming done. Use before saying fixed/done/passing, before commit, or when the user says "make sure", "test it", "verify".
---
# Verify before done
1. IDENTIFY the command that proves the claim, from most specific to broadest: the one test, then the package, then the build, then a real run of the program.
2. RUN it fresh, after your last edit. Results from earlier runs are stale.
3. READ the exit code and the failure count. Pipe long logs through `| tail -40` or `| rg -n "FAIL|Error|passed|failed"` instead of reading them whole.
4. REPORT with numbers: "go test ./pkg: 41 passed, 0 failed (exit 0)".
Required evidence:
| claim          | evidence                                   |
| tests pass     | test command output, 0 failures            |
| builds         | build command exit 0 (lint passing is not enough) |
| bug fixed      | the original repro now passes              |
| new test works | you saw it fail before the fix              |
If the repo already had failures, record them first and hold zero NEW failures. Mention the old ones; leave them unfixed.
Never change an original test to turn it green. If nothing can be run, say "not verified" and why.
