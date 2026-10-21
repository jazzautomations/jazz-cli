---
name: debug-loop
description: Root-cause debugging. Use on errors, stack traces, failing tests, "broken", "crash", "doesn't work", "flaky", "slow", "debug".
---
# Debug loop
Phase 1, build a red loop (this is most of the work). Find ONE command that goes red on THIS bug: a failing test, a CLI call with a fixture, a curl, or a replayed input. Run it and quote the output. Make it tight: seconds, deterministic, and it asserts the user's exact symptom. Until you have this command, read code only to build the loop.
Phase 2, minimise. Cut inputs and steps one at a time and keep only what makes it go red.
Phase 3, hypothesise. Write 2-4 ranked causes, each with a prediction: "if X, then changing Y makes it pass".
Phase 4, probe. Change one variable per run. Tag every debug print `[DBG-jz]` so cleanup is one rg.
Phase 5, fix and lock. Turn the repro into a failing test where a real seam exists, watch it fail, fix the root cause in the shared code (rg every caller first), then watch it pass. Re-run the original loop. Fix the code, not the original tests.
Phase 6, clean up. `rg -n "DBG-jz"` must return nothing. Name the confirmed cause in the final message.
Read a trace from the innermost project frame outward; filter long test logs with `| rg -n "FAIL|Error|panic"`.
If you cannot build a loop after 3 tries, stop and list what you tried and what you need from the user.
Perf bugs: measure a baseline first, then bisect. Logs are the wrong tool for perf.
