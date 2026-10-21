---
name: write-tests
description: Writing good tests. Use when asked to add/write/improve tests or coverage, or when creating a *_test.*, test_*.py, *.spec.* file.
---
# Writing tests
First read 1-2 existing tests in the repo and copy their framework, layout, naming and helpers. Find the test command in the README, Makefile, package.json or CI config.
A good test:
- exercises behavior through the public interface and survives refactors;
- has a name that states the behavior ("rejects empty cart"), not the method;
- covers one logical case, with literal expected values;
- covers the edges: empty, zero, one, last item, unicode, negatives, time zones, error paths.
Bad signs: asserting call counts or call order, testing private functions, checking the DB directly instead of reading back through the API, snapshots nobody checked.
Table-driven tests are fine when the cases share one shape (Go: `[]struct{name, in, want}`).
Add new cases; leave the assertions of existing tests unchanged unless the user asked to change them.
Coverage: aim at the lines you changed, not a global percentage.
Proof that a test is real: break the code under test once (flip a condition), watch the test fail, restore. Quote that run.
Run only the new test file first, then the package.
