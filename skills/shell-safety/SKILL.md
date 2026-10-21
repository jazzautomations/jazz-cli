---
name: shell-safety
description: Safe shell commands. Use before rm/mv over many files, sed -i, find -exec, chmod/chown, kill, curl|sh, writing outside the project, long or background jobs.
---
# Shell safety
- Quote every variable ("$f"). Use `--` before user paths. Use `set -euo pipefail` in scripts you write.
- Destructive ops: run the dry form first (`ls`/`find ... -print`, `rsync -n`, `sed -n 's/x/y/p'` before `sed -i`). Never `rm -rf` on a computed path without checking it is non-empty and inside the project.
- Prefer the edit tool to sed -i on code. sed -i needs `-i ''` on macOS and `-i` on GNU; `perl -pi -e` works on both.
- Stdin is /dev/null: use non-interactive flags (-y, --yes, CI=1, --no-pager, GIT_PAGER=cat).
- Long jobs: run them with bash background:true (or `cmd > /tmp/x.log 2>&1 &`), then check with `tail`. Kill only PIDs you started.
- Big output: pipe to `| tail -50` or `| rg pattern`. Never cat a big log.
- Never pipe a download into a shell, touch ~/.ssh or shell rc files, print secrets or env dumps, or use sudo unless the user asked.
- On slow machines: avoid `find /`, `du` on home, full rebuilds when a package build is enough, and parallel heavy jobs.
