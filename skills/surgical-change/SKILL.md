---
name: surgical-change
description: Minimal, surgical edits. Use for any edit to existing code, "fix", "change", "add a flag", "simplest", "yagni", or when the diff grows past ~40 lines.
---
# Surgical change
Before editing, list the reach of the change: callers (rg the symbol), tests, fixtures, config, exports. That list is the scope. Features outside it are not.
Pick the first option that fully works:
1. Does it need to exist? If not, skip it and say so in one line.
2. Already in this codebase? Use it the way neighbouring code does.
3. Standard library or platform feature.
4. A dependency already installed.
5. The minimum new code.
Rules:
- Match the file's existing style, naming and structure, even when you would do it differently.
- Leave adjacent code, comments and formatting alone. Mention unrelated dead code; leave it in place.
- Remove only the imports, variables and functions that YOUR change made unused.
- No new abstraction, option, wrapper or config unless the user asked for it. Three similar lines are fine.
- Code you move keeps its error handling and validation.
- A known shortcut gets a comment: `shortcut: <limit>, <when to upgrade>`.
Check before finishing: run `git diff --stat`. If any file or hunk does not trace to the request, revert that hunk.
Last line of the reply: what you skipped or did not check.
