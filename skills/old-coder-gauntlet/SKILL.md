---
name: old-coder-gauntlet
description: High-assurance mode. Use when the user says "reliable", "prove it", "I won't read the code", "production-ready", or explicitly asks for high assurance on money, auth, crypto, data migration or concurrency code.
---
# Gauntlet (high assurance)
The user will trust the evidence, not read the code.
1. SPEC: write concrete scenarios (input, then exact expected output), the edge and error cases, what must NOT change, and any new dependency with a one-line justification. Show the spec and wait for an explicit approval. In headless mode, write "spec approval: not obtained".
2. Per behavior: RED (seen failing), then GREEN (minimal code), then refactor while green with assertions frozen.
3. Run every gauntlet layer that applies, and give a reason for each skipped one: full test suite (zero NEW failures against the baseline), typecheck, lint, coverage of changed lines, 3 hand mutants (flip a comparison, drop a branch, off-by-one) that each must make a test fail (restore after), property tests for parsers and serialization, one real run of the program, and a scan of the diff for secrets.
4. EVIDENCE: map each scenario to the test that covers it and give each layer's command and numbers from one fresh final run.
Anti-gaming: never weaken a test, never mock the unit under test, never edit a test and the code in the same step, never print "pass" from a check you never saw fail.
