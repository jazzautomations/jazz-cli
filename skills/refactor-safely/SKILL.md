---
name: refactor-safely
description: Behavior-preserving restructuring. Use for "refactor", "clean up", "extract", "rename", "split this file", "simplify", "dedupe".
---
# Refactor safely
A refactor changes structure, never behavior. If an assertion must change, it is a feature change, so stop and say so.
1. Baseline: run the tests and record the result. No tests on the code you will touch? First write a characterization test that pins the current output (for a CLI, save its output to a file and diff against it), then refactor.
2. Take small steps, one move each: rename, extract function, inline, move, or delete dead code. Run the tests after each move.
3. Change no test files in an implementation refactor. Test-only refactors are a separate step, with assertions unchanged.
4. Rename: `rg -n -w <name>` to find every use, including strings, docs and config. Then edit them all in one batch with edit's edits[].
5. Stop when the asked-for smell is gone. Leave other code alone.
Smells worth fixing when asked: unclear names, duplicated logic, a long function doing several things, the same switch in many places, a speculative abstraction with one caller (inline it), a middle-man that only delegates.
Done: the same tests pass with the same counts as the baseline, and the diff contains no behavior change.
