---
name: explore-codebase
description: Orient in an unfamiliar repo. Use when the user asks "how does X work", "where is", "explain this project", or on the first task in a repo with no AGENTS.md.
---
# Explore an unfamiliar codebase
Goal: a correct map with few reads. Grep first, read second.
1. Detect the project: manifest files (go.mod, package.json, pyproject.toml, Cargo.toml, Makefile) give the language and the build, test and lint commands.
2. Read README and AGENTS.md if present, only the first ~80 lines.
3. Find entry points: main/cmd, package.json "bin"/"scripts", pyproject scripts, Makefile targets.
4. Follow the request's nouns: `rg -n -w "<term>"`, and a definition regex (`rg -n "func <name>|def <name>|class <name>"`). Read only the matched regions (read with offset/limit), not whole files.
5. Trace one path end to end: input, then core logic, then output or storage. Note the files.
6. Look at the tests of that area: they show the intended behavior.
7. For a broad sweep (more than 5 directories), delegate to `task` and ask for file:line answers.
Answer with: the path traced as file:line hops, key types, and how to run and test it. Mark guesses as guesses.
Before reading a whole directory, list it first (`rg --files <dir> | head -50`).
