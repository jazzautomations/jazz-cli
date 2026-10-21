# jazz skills, harness policy and script library: research report

The full skill bodies, ready for go:embed, were drafted in a scratch file (now in [`skill-bodies.md`](skill-bodies.md)). Each line of that file is tagged with its source. Source clones were kept in a scratch directory (not committed).

## A. Sources

| Source | Valuable | Fluff / not portable |
|---|---|---|
| **mattpocock/skills** (github.com/mattpocock/skills, 22 installed in ~/.claude/skills) | `diagnosing-bugs`: "build a tight red loop first; no command, no hypothesis", ranked falsifiable hypotheses, tagged debug logs. `tdd`: vertical slices, a test only at an agreed seam, anti-tautology, mock only at boundaries. `code-review`: a baseline of Fowler smells. `writing-great-skills` is the best meta-guide: description = triggers only, leading words (*tight*, *red*), no-op test, prompt the positive. | The issue-tracker, grill and triage flows assume a strong model, sub-agents in parallel, and a human answering questions one at a time. Bodies run 500 to 2000 words. |
| **Karpathy**: primary source is the tweet x.com/karpathy/status/2015883857489522876. Derived skill: github.com/multica-ai/andrej-karpathy-skills (221k stars, one CLAUDE.md, installed as `karpathy-guidelines`). | Four rules in 370 words: think before coding (surface assumptions), simplicity first, surgical changes ("every changed line traces to the request"), goal-driven execution with a verify check per step. These are the highest value per token of any source. | Nothing. It only lacks enforcement. |
| **ponytail** (github.com/DietrichGebert/ponytail, about 168k stars, MIT) | A "lazy senior dev" ladder: does it need to exist, then the codebase has it, then stdlib, then an installed dependency, then one line, then the minimum. Plus "lazy about the solution, never about the change", grep every caller, `shortcut:` comments, and "never cut validation/security/data-loss handling". It claims −45% tokens and −53% code on Opus. | **Its own benchmark says it does not transfer to llama3.2 3B**: the effect is inside the noise floor (`benchmarks/results/2026-06-15-llama3.2-local.md`). Hooks, statusline and 20 agent ports are packaging. The rules are worth taking. The prompt alone is not enough for weak models, so the harness has to enforce the budget. |
| **old-coder** (github.com/AmazingAng/old-coder, about 750 stars) | It is the Uncle Bob persona, built on his tweet "don't read the agent's code, surround it with extreme constraints". Flow: SPEC (approved) → RED → GREEN → REFACTOR (assertions frozen) → GAUNTLET (suite, types, lint, changed-line coverage, mutation, property tests, real run, secrets) → EVIDENCE. Anti-gaming rules: never weaken a test, never edit test and code in the same step, checkers must fail closed and be proven able to go red. | 3.5k words plus 5k of references. Mutation testing and the approval ceremony are too heavy for every task. Keep it as an opt-in mode. |
| **obra/superpowers** | `verification-before-completion` (an "iron law", a table of claim → required evidence, red-flag words like "should"), and `systematic-debugging`. | `using-superpowers` forces a skill check before every reply, which costs tokens on every turn. Several skills are 3 to 5k words built around subagent-driven development. |
| **anthropics/skills** | The format reference: frontmatter `name`/`description`, progressive disclosure into sibling files, `skill-creator`. | Almost all document or creative skills (pptx, docx, art). Nothing generic for engineering. |
| **ECC** (affaan-m/everything-claude-code, fork installed) | `verification-loop` (build → types → lint → tests → security as phases), `strategic-compact`, `regex-vs-llm-structured-text` (regex for 95%+, LLM only for low-confidence leftovers), `safety-guard`. | Most of it is stack-specific (Next/Prisma/Redis), marketing, or "80% coverage" dogma. |
| Not found | No separate "oldcoder", "uncle-bob skill" or "pony-tail" repos were needed: the two names above are the real repos. Ports (`dsh-ponytail`, `karpathy-ponytail-skills`, `ponytail-lite`) only repackage them. | |

**Format lessons for jazz:**
- The description is the trigger and nothing else.
- The body is imperative, about 150 to 350 tokens, and ends with a checkable completion criterion.
- Reference material goes behind a pointer.
- Write the target behavior positively.
- No "MUST use before anything" meta-skills.

## B. Built-in skill set (12 + always-on core)

| # | name | trigger (description) | load | ~tokens |
|---|---|---|---|---|
| 0 | `craft` core | (none: always present) | **always on**, inside `<rules>` | 170 |
| 1 | surgical-change | any edit to existing code, "fix/change/simplest/yagni", diff > 40 lines | on-demand (auto-injected when the diff grows) | 260 |
| 2 | verify-done | before claiming done/fixed/passing or committing, "prove it" | on-demand + Stop gate | 220 |
| 3 | debug-loop | error, stack trace, failing test, crash, flaky, slow | on-demand | 330 |
| 4 | tdd-lite | "TDD/test first/red-green", new branchy/parsing/money logic | on-demand | 250 |
| 5 | write-tests | add/improve tests, creating `*_test.*`/`test_*.py`/`*.spec.*` | on-demand (also by file type) | 240 |
| 6 | refactor-safely | refactor/clean up/extract/rename/split/dedupe | on-demand | 260 |
| 7 | git-hygiene | commit/branch/merge/rebase/conflict/push/PR | on-demand | 250 |
| 8 | explore-codebase | "how does X work/where is/explain", first task in a repo with no AGENTS.md | on-demand | 250 |
| 9 | shell-safety | rm/mv many files, sed -i, find -exec, kill, curl\|sh, background jobs | 2 lines always on; detail on-demand | 230 |
| 10 | plan-small | build/implement/add support, fuzzy asks, 3+ files | on-demand | 200 |
| 11 | old-coder-gauntlet | "reliable/prove it/production/I won't read it", money/auth/migrations/concurrency | on-demand, or `--quality strict` | 300 |
| 12 | review-diff | "review", and it is the `--review` subagent prompt | on-demand | 220 |

Always-on cost is about 200 tokens. The full set is about 3k tokens, but at most one skill (about 330 tokens) is loaded per turn. That stays well inside the "prompt + schemas < 1.2k" spirit of DESIGN.md, because the skill list itself is not shown (see C).

### Ponytail and old-coder as two complementary modes

They look like they conflict: rigor against minimalism. They govern different things, so the real conflict is small.
- **Ponytail = process budget.** It controls how many steps and tokens the agent spends getting there.
- **Old-coder = output bar.** It controls what must be true before the turn may end.

The remaining conflict is "minimal diff" against "refactor while green". Resolve it by scope: refactor only code that your own change wrote or touched, never the neighbouring code.

**Ponytail defaults** (`budget: lean`, the default; enforced by the harness, not only by the prompt):
- Task size is classified from the prompt and repo signals: S = one file or symbol named, M = default, L = "feature/refactor/migrate" or 3+ files.
- Step budget is S 12, M 25, L 50. Soft warning at 70%, a `<system-reminder>` to wrap up at 100%, and the hard limit stays `--max-steps`.
- Exploration budget is S 4, M 8, L 15 read/rg calls before the first edit. Past it, inject "you have enough context: edit or ask".
- **No re-reading** of an unchanged file. The harness already tracks reads in `stampData`. A second full `read` of an unchanged file returns "unchanged since your read at step N; use offset/limit or rg".
- Prefer `edit` over `write`. If `write` would replace an existing file with >50% of it unchanged, refuse it with "use edit".
- Final reply ≤ 8 lines: what changed, the evidence, and one line on what was skipped (the ponytail "skipped/risk" tail).
- **Metrics:** tokens in and out, steps, read/rg calls, reread rate, and diff LOC per solved task. Pass rate must stay the same or improve.

**Old-coder defaults** (`quality: standard`; `strict` adds skill 11). This is a done-checklist run in the Stop path (`acStopGate`) as deterministic scripts, not by the model:
1. Tests were run after the last edit. The harness knows this from the bash history. If not, inject verify-done once.
2. `rg` over the diff finds no debug prints: `[DBG-jz]`, `console.log`, `fmt.Println("DEBUG`, `print(` added in non-test code, `breakpoint()`, `debugger`.
3. No new TODO/FIXME without a `shortcut:` tag.
4. Function size: an added or changed function over 60 lines (strict: 40) is flagged.
5. Names: no new identifiers like `tmp|foo|data2|x1` outside tests.
6. No commented-out code blocks of 3 or more lines in the diff.
7. Lint or typecheck runs if the project has it, found by `detect_project`, and must give zero NEW errors.
8. New non-trivial logic (a branch, loop or parser) has at least one test file touched. This is the ponytail rule too.

Failures go back to the model **once** as a structured list, the same way `--review` does it. A second failure ends with stop reason `quality_failed`, reported, not blocking in interactive mode.

**Metric:** a *hidden* checker run after the eval (the same script plus gofmt/ruff/eslint where available, a dead-code count, and mutation of 3 hand-picked lines per task). The score is the share of solved tasks that pass the checklist. The model never sees this checker.

**Picking by task type:**

| Task | Mode |
|---|---|
| Bugfix | lean + debug-loop. The Stop checklist requires a regression test only if a seam exists. |
| Greenfield / new feature | lean exploration, but the full checklist, plus tdd-lite for logic. |
| Refactor | refactor-safely. The checklist requires the baseline test counts to be equal. The step budget is raised one size. |
| High-stakes path (auth/money/migrations), detected by path regex or keywords | `strict` + gauntlet. |

## C. Harness policy

**1. Hide the skill list from the model.** Today `skillsBlock` lists up to 40 skills from ~/.claude/skills (57+ ECC/Pocock entries at 200 chars each, which is several k tokens), and the model must choose and read one. Weak models choose badly. Built-ins get selected by the harness. User skills stay listed, but capped at 12 and 120 chars, and only from `.jazz/skills` + `~/.config/jazz-cli/skills`. `~/.claude/skills` becomes opt-in (`skills.claude_dir: true`).

**2. Triggering.** This is new code in `skills.go`. Each built-in has embedded `triggers.json`:
```json
{"name":"debug-loop","kw":["\\b(erro|error|traceback|panic|crash|broken|quebr|falh|flaky|n[ãa]o funciona)"],
 "files":["*_test.go","test_*.py"],"tool_signals":["exit_nonzero","stacktrace"],"phase":["explore","edit"],"priority":80}
```
- Score = keyword hits in the user message (Portuguese and English) + file globs touched + tool signals (a nonzero exit, the parser detecting a stack trace or test failure, the diff passing N lines) + phase (explore / edit / verify / stop, known from tool history).
- If the best score clears a threshold, inject that skill. Ties go to the higher priority.
- No LLM classifier, because the step budget matters more.

**3. Injection, cache-safe.** Reuse the steering `fileMatch` mechanism (agent.go:614, `acSteeringNote`).
- The skill body is appended once as `<system-reminder>Skill "debug-loop" applies: …</system-reminder>` to the user message at the start of the turn (keyword triggers) or to the triggering tool result (signal triggers).
- The system prompt stays byte-identical and the history stays append-only.
- A marker in the history means it is not re-injected, and it comes back after compaction (same as `acSteerInjected`).

**4. Budget.**
- At most **one** skill per turn, plus the Stop-gate reminder.
- At most 3 per session before compaction.
- Skill text is ≤ 400 tokens, enforced by a test.
- `skills: off|auto|manual` in config, and `/skill <name>` to force one.

**5. Hooks and loops interplay.** Order at stop:
1. user Stop hooks (they can block),
2. old-coder checklist,
3. `--until` command,
4. `--review` (review-diff skill as the subagent prompt).

Each loop counts toward `--max-steps` and the ponytail budget. Rounds per check: checklist 1, until 5, review 1. `/goal` stays in the per-turn reminder; skills never go there. When `--until` is set, the checklist skips item 1, because `--until` is stronger.

**6. Deterministic parsing of tool output** (the regex-vs-LLM rule).
- When a bash result over 2 KB matches a known shape (go test, pytest, jest, unittest, cargo, gcc/clang, tsc, eslint, npm, a Python/Go/JS stack trace, timestamped logs), run the matching parser from D **before** truncation (tools.go:1018).
- The model gets the structured summary (≤ 40 lines), then the parser's `unparsed[]` lines, then the last 30 raw lines, plus the existing spill path to the full output.
- If confidence < 0.8, fall back to today's 20/80 head/tail truncation.
- Flag: `JAZZ_PARSE_OUTPUT=0|1` / `--parse-output`, for A/B runs on the 10-task suite, the hard suite and SWE-bench SymPy. Compare pass rate, tokens and steps.

**7. Code spec.**
- `skills.go`: `//go:embed skills/*/SKILL.md skills/*/triggers.json`; `loadBuiltinSkills()`; `scoreSkills(input, sig Signals) (Skill, bool)`; `(a *Agent) skillNote(phase, tc, out) string`; `skillInjected(name)`.
- `prompt.go`: add `<craft>` to `basePrompt`; change `skillsBlock` to user skills only, with the cap.
- `agent.go`: call `skillNote` next to `acSteeringNote` (line 614) and at turn start next to the goal reminder.
- `agent_loop.go`: `acQualityGate` before `acUntilCheck`.
- `budget.go`: task size, step and exploration counters, the reread guard.
- `outparse.go`: calls the embedded parsers. The hot ones are ported to Go so per-call Python startup cost on the Pentium M is avoided. The rest run through `script`.

## D. Script library (`jazz-scripts/`)

**Layout.** The scripts live in `jazz-scripts/<name>.py|.sh` and `jazz-scripts/<name>.json`. They are embedded with `//go:embed jazz-scripts`, extracted on first use to `dataDir()/scripts/<version>/`, and stamped with sha256.
- Python 3.6+ stdlib only. That covers old Debian/Big Sur, but `python3` must be detected first, and its absence means that script is unavailable. POSIX sh for git, ports and processes.
- Users add their own in `~/.config/jazz-cli/scripts/` and projects in `.jazz/scripts/`, with the same `.json` sidecar. Project scripts go through the trust gate (new type `scripts`).
- Version = jazz version + the `version` field in each schema.

**One tool:** `script {name, args}`.
- The harness validates `args` against the schema (reusing the `parseArgs`/coercion repair) and runs it with `python3 -I` or `sh`, cwd at the root, a 60 s timeout and the scrubbed env.
- It returns stdout JSON, clipped to 8 KB.
- The tool description lists only names and one-line descriptions, about 250 tokens for 18 scripts.

**Output convention:**
```json
{"ok":true,"confidence":0.97,"data":{...},"unparsed":["lines the regexes did not explain"],"warnings":[]}
```
- `confidence` = the share of non-blank input lines explained by the regexes, capped by format certainty.
- Only `unparsed[]` and the summary reach the model.

**Schema sidecar example:**
```json
{"name":"parse_test_output","version":1,"run":"python3","description":"Summarize pytest/go test/jest/unittest/cargo output: counts and failures with file:line",
 "args":{"type":"object","properties":{"file":{"type":"string"},"cmd":{"type":"string","description":"run this and parse it instead"},"framework":{"enum":["auto","pytest","gotest","jest","unittest","cargo"]}}},
 "output":{"data":{"framework":"str","passed":"int","failed":"int","skipped":"int","failures":[{"test":"str","file":"str","line":"int","message":"str"}]}}}
```

**Ship first (18):**

| script | args → data |
|---|---|
| detect_project | – → langs, build/test/lint/fmt cmds, entry points, file count, git? |
| run_tests | target? → runs the detected cmd + parse_test_output |
| parse_test_output | file\|cmd, framework → counts, failures[] |
| parse_build_errors | file\|cmd → errors[{file,line,col,code,msg}] (gcc/clang/go/tsc/eslint/npm/cargo) |
| extract_stacktrace | file\|text → lang, exception, message, frames[{file,line,func,in_project}] |
| parse_log_lines | file, level?, since? → counts by level, first/last ts, top messages (dedup) |
| find_symbol | name, kind? → definitions[{file,line,kind}], refs count (rg + per-language def regexes) |
| tree_summary | path, depth → dirs with file counts/LOC by extension, ignoring vendor |
| json_edit | file, path, op(get/set/del), value → old/new value (keeps indent) |
| yaml_get | file, path → value (a simple-YAML subset; unparsed[] on anchors/multi-doc) |
| csv_stats | file, cols? → rows, per-column type/min/max/nulls/top |
| git_state | – → branch, ahead/behind, staged/unstaged/untracked, last 5 commits, conflict files |
| git_safe | op(stash/branch/revert-file/commit-paths), args → refuses force/reset/clean |
| port_free | port → free?, pid, cmd |
| http_check | url, expect_status? → status, ms, headers subset, body head (loopback/LAN only unless allowed) |
| extract_versions | file\|text → semver/urls/emails found, with positions |
| snapshot_cmd | cmd, name → saves or diffs stdout against `.jazz/snap/name` (characterization tests) |
| diff_quality | base? → the old-coder checklist: debug prints, long funcs, TODOs, commented code, test touched |

**Why it saves steps and tokens.**
- Today "run tests → read 30 KB → scroll the spill → rg → read" costs 3 to 5 round trips and 5 to 15k tokens. `run_tests` answers in one call with about 300 tokens.
- `detect_project`, `git_state` and `find_symbol` each replace 2 to 4 exploratory bash calls.
- Each saved round trip is a free-tier request (limit about 200/h) and, on the Dell, seconds of prefill.

**Measure it.** Paired arms (`--scripts off|on`, `--parse-output 0|1`) on the same commits, ≥3 reps, 10-task + hard suites + SymPy. Report:
- pass rate
- total tokens
- steps
- wall time
- bash calls before the first edit
- share of `script` calls that returned ok with confidence ≥ 0.8

Add a unit suite: golden fixtures per parser (real logs in `jazz-scripts/testdata`), each with a negative control that must produce `ok:false` or confidence < 0.8 (old-coder's "fail closed" rule).

## E. Priority and risks

| # | Item | Impact | Effort |
|---|---|---|---|
| 1 | Always-on `<craft>` + stop listing ~/.claude/skills by default | high (cuts thousands of prompt tokens, sharper rules) | XS |
| 2 | `outparse.go` (test/build/stack-trace parsers in Go) + A/B flag | high | S |
| 3 | `skills.go` embed + keyword/signal trigger + one-per-turn injection | high | M |
| 4 | Ponytail budget (reread guard, exploration cap, write→edit) | med-high | S |
| 5 | Old-coder Stop checklist (`diff_quality`) | med-high | S |
| 6 | `script` tool + 18 scripts + trust gate for project scripts | med | M |
| 7 | Strict gauntlet mode, hidden quality checker in evals | med | M |

**Risks.**
- **Prompt bloat.** Keep always-on ≤ 200 tokens and add a test asserting each skill ≤ 400 tokens.
- **Cache breakage.** Never put skills or a dynamic skill list into the system prompt. Inject only in append-only messages.
- **Wrong-skill triggers.** Use a score threshold, priority and max one per turn, and log trigger decisions in `--debug` for tuning. A wrong 250-token skill is cheap, but a wrong *gauntlet* is expensive, so it only fires on explicit keywords.
- **Weak models ignore prose.** Ponytail's own data shows that on 3B models the enforcement must live in the harness (budgets, Stop checklist, parsers), not in the skill text.
- **Python missing or old on targets.** Detect it and degrade. Hot parsers live in Go.
- **Checklist false positives.** Debug-print regexes in legitimate CLIs need a "report once, don't loop" rule and per-project overrides in `.jazz/settings.json`.
