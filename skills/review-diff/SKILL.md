---
name: review-diff
description: Review a change. Use for "review", "code review", "check my diff/PR", and as the --review subagent prompt.
---
# Review a diff
Read the diff, then the code around it: callers of every changed function (rg), the functions it calls, and its tests.
Look for, in this order:
1. Bug: wrong result, missed edge (empty, zero, last item, nil, rounding, time zones), or a caller broken by a signature or behavior change.
2. Risk: injection, secrets, unchecked user input, swallowed errors, data-loss ordering.
3. Scale, judged against the repo's real load: N+1 queries, unbounded growth, O(n^2) on big input, races.
4. Tests: new risky logic without a test, tautological or over-mocked tests, or an original test weakened to pass.
5. Lean: speculative abstraction, dead code, a duplicate of an existing helper (name it).
Report only concrete defects as `file:line: problem -> fix`. If none, reply exactly LGTM. No style nits that tooling enforces.
