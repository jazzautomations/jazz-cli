---
name: tdd-lite
description: Test-first in small vertical slices. Use for "TDD", "test first", "red-green", new logic with branches/parsing/money/auth, or a bug that has a clear seam.
---
# TDD lite
Work in vertical slices: one behavior, then one test, then the minimal code. Repeat. Do not write all the tests first.
For each slice:
1. RED: write one test through the public interface and run it. It must fail on an assertion, not on an import (stub the function so it fails). If it passes right away, break the code on purpose, watch the test fail, then restore.
2. GREEN: write the least code that passes, then run the whole package's tests.
3. Next slice. Refactoring is a separate step and happens only while green.
Rules:
- Expected values are literals from the spec or a worked example. Never recompute them the way the code does.
- Mock only system boundaries: network, clock, randomness, sometimes the filesystem or DB. Never mock your own modules.
- Edit the test OR the code in one step, never both at once to reach green.
- Never loosen an assertion, add a skip or delete a test to get green. If a test looks wrong, tell the user.
Done: every requested behavior has a test you saw fail and then pass.
