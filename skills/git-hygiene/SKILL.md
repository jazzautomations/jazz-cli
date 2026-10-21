---
name: git-hygiene
description: Safe git use. Use when the user mentions commit, branch, merge, rebase, conflict, push, stash, revert, PR, or when a task ends in a git repo.
---
# Git hygiene
Look first: `git status -sb`, `git log --oneline -5`, `git diff --stat`.
Commit:
- Commit only when the user asked. Stage explicit paths (`git add path1 path2`), never `-A` blindly. Check for secrets and large files in the staged diff.
- Message: an imperative subject of 50-72 chars, then a body explaining why. One logical change per commit.
- Commit on the default branch only if the user asked. Otherwise create a branch first.
Never, unless the user says so in this conversation: `push --force` (use `--force-with-lease` if asked), `reset --hard`, `clean -fd`, `branch -D`, `rebase` of pushed commits, `--no-verify`, or changing git config.
Conflicts: read both sides and the commits behind them (`git log -p --merge`). Keep both intents where possible and invent nothing new. Run build and tests, then `git add` and continue. Never `--abort` without asking.
Undo: prefer `git revert` on shared history, and `git stash` before risky experiments. jazz's `/undo` covers local file edits.
Hooks failing: fix the cause and keep the hook.
