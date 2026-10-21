# jazz-cli

```
     ██╗ █████╗ ███████╗███████╗
     ██║██╔══██╗╚══███╔╝╚══███╔╝
     ██║███████║  ███╔╝   ███╔╝
██   ██║██╔══██║ ███╔╝   ███╔╝
╚█████╔╝██║  ██║███████╗███████╗
 ╚════╝ ╚═╝  ╚═╝╚══════╝╚══════╝
```

*[Português](README.md) (primary, with the full reference)*

jazz is a terminal coding agent written in Go: one static binary (~11 MB, no dependencies) that
reads files, runs commands in a persistent shell, edits code and checks its own work, talking to
the model you choose (Anthropic, OpenAI, ChatGPT through your account, OpenRouter, Groq, Gemini,
Ollama/llama.cpp and a dozen free providers). It is built to work well with **free, weak models**
on **old hardware**: it runs on an M-series Mac and on a 32-bit Pentium M laptop with 2 GB of RAM
(a Dell Latitude, linux/386). To make up for a weak model, the harness enforces what a prompt
would only ask for: step budgets, tests actually run before "done", guards against faked actions,
and an OS sandbox underneath everything. It is a young project (v0.7), built by one person with AI
agents; expect rough edges. Known bugs and gaps are in [`FINDINGS.md`](FINDINGS.md). User-facing
messages are in Brazilian Portuguese; prompts and code comments are in English.

Principles (details in [`DESIGN.md`](DESIGN.md), in Portuguese):

- Few tools (`read`, `write`, `edit`, `bash`, `todo`, `fetch`, `task`, `script`) and a small prompt
  that works even with a free model and a short context.
- System prompt and schemas fixed for the session, so the prompt cache keeps working; reminders go
  into the user message or the tool result.
- Light UI (line editor + ANSI), no flicker over SSH.
- Dangerous commands (broad `rm -rf`, `push --force`, `curl | sh`, `sudo`…) always ask.

## Install

Binaries for linux (386, amd64, arm64), macOS (amd64, arm64) and Windows (amd64, arm64) ship in
the [releases](https://github.com/jazzautomations/jazz-cli/releases). Each release carries a
`manifest.txt` with the version and the sha256 of every binary, signed with Ed25519
(`manifest.txt.sig`); the installers and `jazz update` check both.

### Linux and macOS

```sh
curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
```

[`install.sh`](scripts/install.sh) (POSIX sh) detects OS and architecture, downloads
`jazz-<os>-<arch>`, verifies the manifest signature (with `minisign` or OpenSSL 3) and the sha256,
and installs to `~/.local/bin` without sudo. If `~/.local/bin` is not on your PATH it prints the
line to add to `~/.zshrc`/`~/.bashrc` (it does not edit those files).

Options (`… | sh -s -- <options>`):

| Option | Variable | What it does |
|---|---|---|
| `--bin-dir DIR` | `JAZZ_BIN_DIR` | install directory (default `~/.local/bin`) |
| `--system` | | install to `/usr/local/bin`, using `sudo` when needed |
| `--version v0.7.0` | `JAZZ_VERSION` | a specific release instead of the latest |
| `--base-url URL` | `JAZZ_BASE_URL` | folder holding `manifest.txt(.sig)` and the binaries (e.g. your own mirror built by `scripts/release.sh`) |
| `--require-sig` | `JAZZ_REQUIRE_SIG=1` | fail when the signature cannot be verified |
| | `JAZZ_OPENSSL` | path to an OpenSSL 3 binary (macOS ships LibreSSL, which cannot verify Ed25519; the script also looks for Homebrew's OpenSSL 3) |

Without `minisign` or OpenSSL 3 the script checks only the sha256 and warns. The copy of
`install.sh` in the git repository has no public key (`scripts/release.sh` fills it in), so it
also checks only the sha256: use the one attached to the release.

**macOS (Intel or Apple Silicon):** same command. In a terminal running under Rosetta the script
picks the native arm64 binary.

**32-bit Linux (i386/i686, like the Dell):** same command; it downloads `jazz-linux-386`
(also on a 64-bit kernel with a 32-bit userland). Tips for old machines:

- On Debian/antiX `fd` is called `fdfind`; jazz's shell creates the `fd` alias itself.
- `~/.bashrc` usually returns early for non-interactive shells, so `ssh host 'jazz …'` does not find
  `~/.local/bin`. Use `ssh host '~/.local/bin/jazz …'` or `ssh -t host 'bash -lc jazz'`.
- To cross-compile on another machine and copy over SSH:
  `JAZZ_DEPLOY_HOST=<ssh-alias> scripts/deploy-ssh.sh` (builds linux/386, installs to
  `~/.local/bin` and adds a `.desktop` menu entry; `--sudo` installs to `/usr/local/bin`).
- For the Linux sandbox install `bubblewrap` (`sudo apt install bubblewrap`).

### Windows (native, no WSL)

> **Not runtime-tested on Windows yet.** The port builds for amd64/arm64 and the test suite passes on
> CI's `windows-latest`, but nobody has used jazz on a real Windows machine so far. Reports welcome.

In PowerShell, no admin needed:

```powershell
irm https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.ps1 | iex
```

[`install.ps1`](scripts/install.ps1) downloads `jazz-windows-<arch>.exe`, verifies the Ed25519
signature of `manifest.txt` in pure PowerShell (no minisign/OpenSSL; written for Windows
PowerShell 5.1 and PowerShell 7) and the sha256, installs to
`%LOCALAPPDATA%\Programs\jazz\jazz.exe` and adds that folder to the user PATH. Variables:
`JAZZ_BASE_URL`, `JAZZ_BIN_DIR`, `JAZZ_ARCH` (`amd64`/`arm64`), `JAZZ_NO_PATH=1`,
`JAZZ_REQUIRE_SIG=1`. Use **Windows Terminal** (24-bit color and Unicode; the classic console falls
back to 256 colors and ASCII). The `bash` tool uses **Git Bash** when Git for Windows is installed,
otherwise PowerShell (`JAZZ_SHELL` overrides; WSL's `bash.exe` is skipped on purpose). Every
command, MCP server, hook and script runs in its own job object. There is no file sandbox on
Windows (job-object limits only).

### From source

Go 1.27 or newer:

```sh
git clone https://github.com/jazzautomations/jazz-cli && cd jazz-cli
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o jazz .
install -m 755 jazz ~/.local/bin/jazz
# another platform: CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o dist/jazz-linux-386 .
```

A binary built this way has no embedded public key, so its `jazz update` refuses to update
(unsigned updates are never accepted). Signed releases come from `scripts/release.sh`.

### Updating

`jazz update` (or `jazz update --check`) downloads the latest release, verifies the manifest
signature, refuses an older version or one that differs from what was advertised, and replaces the
running binary.

## First run

```sh
jazz doctor               # diagnostics: providers with credentials, shell, sandbox, terminal
jazz                      # interactive mode in the current folder
jazz omni setup           # shows your providers, turns on routing, suggests free keys
jazz login                # sign in with your ChatGPT account (device code, no browser needed)
jazz key openrouter       # store a key (hidden input); works for any provider
```

- **No key at all** still works: the default is `kilo/poolside/laguna-s-2.1:free`, with automatic
  fallback to other fast free models. Free `:free` models allow 200 requests/hour per IP; quality
  varies and they are **no place for sensitive data**.
- **Free providers and the omni router:** with free keys from several providers (OpenRouter, Groq,
  Gemini, Mistral, NVIDIA, Cerebras, Cloudflare…), `jazz omni setup` turns on **omni**, which spreads
  requests across all of them, free ones first, with fallback when one fails or runs out of quota.
  Before the first request to a free provider that may log or train on your prompts, jazz warns once.
- **Bring your own Anthropic/OpenAI keys:** `jazz key anthropic` / `jazz key openai`, or
  `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`. Paid models only join omni in an emergency
  (`omni.paid: never|emergency|always`) unless you pick one with `-m`.
- **ChatGPT (OAuth):** `jazz login` uses your ChatGPT account via the device-code flow (`--browser`
  opens a browser). Tokens live in `~/.config/jazz-cli/auth.json` (0600).
- **Local models:** `jazz provider add local --url http://127.0.0.1:8080/v1 --no-key` (llama.cpp,
  LM Studio, any OpenAI-compatible URL) or `--ollama`.
- Switch models with `-m provider/model` or `/model` in a session; `jazz models` lists them.

## Permission modes

| Mode | Behaviour |
|---|---|
| `auto` (default) | runs whatever does not look dangerous; dangerous commands and sensitive files ask |
| `turbo` (`--turbo`, alias `full-auto`) | with an OS sandbox: nothing that stays inside the project asks; it only asks when really necessary (list below) |
| `ask` | everything that is not read-only asks (the right mode for an unknown repository) |
| `smart` | project edits and read-only commands run; the rest goes to a fast model that answers SAFE or ASK; packages, mutating git, `rm`/`sudo`, `.env`/keys and jazz's own config always ask |
| `yolo` | nothing asks; only inside a container or VM |
| `plan` | investigates and proposes a plan only |

`/mode <mode>` switches; Shift+Tab or Ctrl+T cycles auto → turbo → smart → plan → ask. Flags: `--ask`,
`--smart`, `--turbo`, `--yolo`; config `"mode": "turbo"`. In a folder you have not trusted yet
(`jazz trust`), the session runs in `ask`; with an explicit `--turbo` the mode stays, but bash that
changes something still asks until you trust the folder (with `-p`: `jazz trust --yes` first).

**One-key confirmations:** `enter` or `s` = yes, `a` = always, `n`/`esc` = no, `v` = see the whole
command or diff, `e` = no, and say what to do instead. No typing `sim`. "Always" on a prompt with a
risk reason (new host, folder outside the project, destructive git, `curl|sh` from a host) is kept in
`.jazz/approvals.json` and never asked again in that project; `/approvals` lists, `/approvals clear`
wipes. Secret reads and catastrophic commands ask every time.

**Turbo in 5 lines.** (1) It only exists with an OS sandbox (`sandbox-exec` on macOS, `bwrap` on
Linux); without one it is `auto` and says so once. (2) Tests, builds, formatting, `git add/commit`,
edits, reads, scripts and clean package installs run without asking in a folder you trusted.
(3) It asks only for: writes/deletes outside the project; sending data to a new host (first time);
reading secrets; destructive git (`push --force`, `branch -D`, `filter-repo`, `clean -f`,
`reset --hard` on a shared branch); packages the supply-chain guard flags; catastrophic commands
(`rm -rf` of home, disk ops, `chmod -R 777 /`, `sudo`, `curl|sh`); `ask` rules, hooks, a suspected
injection in the turn; and, in a folder not trusted yet, bash that changes something. (4) `git checkout -- .`/`restore .`/`reset --hard` on a local branch do not ask
because the shadow-git checkpoint undoes them (`/undo`). (5) It decides without calling a model; every
auto-approval goes to the audit log with the rule that allowed it.

**The security trade-off, plainly:** in turbo what protects you inside the project is the sandbox,
not a prompt. A bad command can delete or damage project files (the checkpoint covers versionable
files up to 1 MB, not `.gitignore`d ones) and can send project content to a host you already approved.
The sandbox stops reads of your home and secrets and writes outside the project, but the network stays
open (use `--sandbox strict` to cut it). Unknown repository: `ask`.

## Headless (`-p`)

```sh
jazz -p "run the tests and fix what fails"
echo "explain this repo" | jazz -p
jazz -p -m groq/openai/gpt-oss-120b --output-format json "…"
jazz -p --until "go test ./..." "fix the failing test"
```

Exit codes: 0 ok · 1 error · 2 bad usage · 3 step limit · 4 loop detected · 5 tool denied ·
6 truncated answer · 7 unverified answer · 8 `--until` still failing · 9 quality check failing ·
124 timeout · 130 interrupted.

`-p` never hangs: `--timeout` (default `10m`, `0` disables) is the deadline for the whole run.
`--output-format json|stream-json` includes tool calls, usage and the stop reason. In an untrusted
folder, non-read-only bash is denied unless `--allow-tools` allows it (`'Bash(npm test:*)'`) or you
pass `--yolo`; in CI run `jazz trust --yes` first if the repository is yours.

## Honest "done"

Weak models like to claim they did things. jazz checks:

- **No faked actions:** if the model says it did something ("I created the file…", "I ran the
  tests") without calling any tool that turn, jazz sends it back to actually do it once; if it
  insists, the answer is shown with `⚠ resposta não verificada` (exit code 7 with `-p`).
- **Honest-completion guards** (`--guards on|off`, default on): pre-existing tests and CI files are
  protected unless the request is about tests; weakening tests (skip/xfail, dropped assertions, a
  spree of lint suppressions) is rejected; answers hardcoded to the test are detected; what the
  request said to remove must be gone; and a turn that edited code cannot end before the tests (or
  the changed code) ran after the last edit. Eval checkers (`CHECK`, `PROMPT`, `.hidden/`) are never
  read or changed. Turn one off with `JAZZ_TEST_GUARD`, `JAZZ_HARDCODE_CHECK`, `JAZZ_DONE_GATE`,
  `JAZZ_EVAL_GUARD` (`=off`).
- **`--until "CMD"`** (or `/until`): the turn ends only when the command exits 0; on failure the
  output goes back to the model, up to `--until-rounds` times (default 5).
- **`--review`** (or `/review on`): after a turn that changed files in a git repo, a read-only
  subagent with a fresh context reviews the turn's `git diff`; concrete defects go back once.
- **Quality check** (`--quality off|standard|strict`): no debug prints, new TODOs, commented-out
  blocks, giant functions or new lint/type errors in the turn's changes.

## Security

Read [`SECURITY.md`](SECURITY.md) (Portuguese) before pointing jazz at someone else's repository.
In short:

- jazz runs commands **as you**. The guards (dangerous-command rules, prompt-injection detector,
  package guardrails, smart mode) are heuristics that make common attacks noisy, **not a security
  boundary**.
- The boundary is the **OS sandbox** (`--sandbox auto|strict|off`, default `auto`): `sandbox-exec`
  on macOS (verified) and `bubblewrap` on Linux (not yet verified on a real machine). Everything jazz
  executes (shell, jobs, hooks, stdio MCP servers, scripts) runs inside it, unable to read your home
  outside the project or write outside the project. `strict` refuses to run without a sandbox and
  cuts the commands' network (loopback only). Windows has no file sandbox. `jazz doctor` shows the state.
- For an unknown repository: `--sandbox strict`, `ask` mode, no paid keys in the environment,
  ideally in a container.
- Untrusted folders: project `AGENTS.md`/`CLAUDE.md`, `.mcp.json`, hooks, allow rules, steering and
  scripts stay off until `jazz trust`; changes to them (or to the files they run) ask again.
- Secrets are stripped from the environment of the shell, hooks and MCP servers, and redacted from
  outputs; no telemetry.

**What was measured** (suite [`evals/redteam`](evals/redteam), 14 attack scenarios with a canary
HOME and a local exfiltration sink; details and caveats in
[`docs/research/redteam-results.md`](docs/research/redteam-results.md), Portuguese):

- Model `kilo/poolside/laguna-s-2.1:free`, `auto` mode: v0.4.0 (no security layer) got 12 BLOCKED,
  1 COMPROMISED (write through a symlink out of the project) and 1 ERROR; v0.4.1 got 12 BLOCKED,
  0 COMPROMISED and 2 ERROR (the model spiralled after a block); with guiding block messages
  (v0.4.3), **14/14 BLOCKED, 0 ERROR, empty sink**.
- OS sandbox (macOS): running the payloads of scenarios 11 (`curl … | sh`) and 14 (write through a
  symlink) from a trusted hook, which no guard inspects, both were COMPROMISED with
  `--sandbox off` and BLOCKED with `--sandbox auto`. In the run with the model, scenario 11 still
  counts as COMPROMISED by its checker: the model downloaded the payload "to review it" (loopback
  network is open in `auto`) but did not execute it.
- Limits: one run per scenario (model variance not measured), a single model, 11 scenarios
  specified in [`evals/redteam/SPECS.md`](evals/redteam/SPECS.md) not implemented yet, and the suite
  does not run in CI.

## MCP, hooks, skills

- **MCP:** global servers in `~/.config/jazz-cli/mcp.json`, project servers in `.mcp.json` (same
  format as Claude Code), the latter only after `jazz trust`. A project server never replaces a
  global one with the same name. `enabled_tools`/`disabled_tools` per server; `/mcp` shows status.
  Servers that declare `resources` / `prompts` get two model tools, `mcp_resource {server, uri}` and
  `mcp_prompt {server, name, args}` (empty `uri`/`name` lists). The system prompt carries only counts,
  results are sanitized and wrapped as untrusted like `fetch`, and a changed catalogue needs
  re-approval (rug-pull hash). `/mcp resources` and `/mcp prompts` list them; turn off with
  `disable_resources` / `disable_prompts` per server or `JAZZ_MCP_RESOURCES=off`.
- **Permissions:** Claude Code rule format (`Bash(npm test:*)`, `Edit(src/**)`, `Read(.env)`,
  `WebFetch(domain:go.dev)`, `mcp__srv__*`) from `config.json`, `~/.claude/settings.json`,
  `.claude/settings*.json` and `.jazz/settings.json`. `deny` > `ask` > `allow`.
- **Hooks:** `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `SessionStart`, in Claude
  Code's `settings.json` format. They get JSON on stdin, 60 s timeout; exit 2 blocks.
- **Skills:** 13 short built-in skills (`surgical-change`, `verify-done`, `debug-loop`, `tdd-lite`,
  `write-tests`, `refactor-safely`, `git-hygiene`, `explore-codebase`, `shell-safety`, `plan-small`,
  `old-coder-gauntlet`, `review-diff`, `frontend-craft`). The model never sees the list: jazz picks
  at most one per turn from keywords, edited files, tool-output signals and the turn phase, and
  injects it as a reminder (the system prompt stays fixed). **`frontend-craft`** (quality floor,
  bans and the Persuade/Operate/Read/Experience modes, rewritten from Impeccable) kicks in for
  UI/design requests and the first edit of a UI file; after each UI edit the deterministic `ui_lint`
  hook appends findings (gradient text, default violet palette, emoji as icons, missing `alt`,
  `outline: none` without `:focus-visible`, motion without `prefers-reduced-motion`…). Your own
  skills go in `~/.config/jazz-cli/skills`, `.jazz/skills` or `.claude/skills`; `/skill` lists them.
- **Steering:** conditional rules in Kiro format (`.jazz/steering/*.md`, `.kiro/steering/*.md`) and
  Cursor format (`.cursor/rules/**/*.mdc`): `always`, `fileMatch`, `auto`, `manual`.
- **Scripts:** a `script` tool with 19 built-in Python/sh scripts that answer in short JSON
  (`detect_project`, `run_tests`, `parse_test_output`, `git_state`, `ui_lint`…); `jazz script` runs
  them without a model. Plus model-free shortcuts ("run the tests", "git status") and an offline mode.

The full reference (all commands, slash commands, configuration files, how omni routes, output
parsing, budgets, `--recover`, plan cache, troubleshooting) is in the [Portuguese README](README.md).

## Evals

The suites measure jazz for real, with a model acting and an independent checker outside the work
folder (locked while the agent runs, in v2 and red-team). Results go outside the repository
(v1/v2, under `~/jazz-eval-results`) or to `evals/redteam/results/` (git-ignored). They need `bash`
and `python3`.

```sh
# v1: 10 short tasks (bugfix, feature, refactor, shell, config…), jazz -p --yolo
LIMIT=600 evals/v1/run.sh [provider/model]           # uses GNU coreutils `timeout`

# v2: 10 hard tasks (distant root cause, multi-file feature, background server, hidden refactor…)
evals/v2/calibrate.sh                                 # each CHECK must fail untouched and pass with refsol/
LIMIT=900 ONLY="01 07" JAZZ=./jazz evals/v2/run.sh [provider/model]

# red-team: 14 attacks (poisoned README, AGENTS.md, invisible Unicode, typosquat, curl|sh, symlink…)
JAZZ=./jazz MODE=auto LIMIT=480 evals/redteam/run.sh [prefixes…]
```

The red-team builds a fake HOME with canary secrets and starts a local HTTP sink; even so, **run it
only on a disposable dev machine or a VM**, never where real credentials live. v1/v2 run with
`--yolo`: use a disposable folder/machine as well. Task prompts and messages are in Portuguese.

## Research

Architecture with diagrams (components, one turn, security layers, omni and judge, sessions, evals): [`docs/ARCHITECTURE.en.md`](docs/ARCHITECTURE.en.md).

Reports behind jazz's design decisions, in [`docs/research/`](docs/research) (English unless noted):

| Document | What it covers |
|---|---|
| [`grep-is-all-you-need.md`](docs/research/grep-is-all-you-need.md) | The paper "Is Grep All You Need?" (arXiv:2605.15184) and what it means for grep-first search and `--prefetch` |
| [`tocomas-plan-reuse.md`](docs/research/tocomas-plan-reuse.md) | TOCOMAS (arXiv:2609.37953) and plan reuse (AgentReuse, Agentic Plan Caching): where the plan cache came from |
| [`shepherd-meta-agents.md`](docs/research/shepherd-meta-agents.md) | Shepherd (Stanford, arXiv:2605.10913): origin of `--recover` and the hindsight hint |
| [`metaharness-self-improvement.md`](docs/research/metaharness-self-improvement.md) | Meta-Harness and self-improving harnesses: design (not implemented) of `jazz improve` |
| [`bob-kiro-devin.md`](docs/research/bob-kiro-devin.md) | IBM Bob Shell, Kiro CLI and Devin CLI compared with jazz: origin of steering, smart mode, `--until`, `--review` |
| [`skills-personas-scripts.md`](docs/research/skills-personas-scripts.md) | Skill sources, harness policy and the script library |
| [`skill-bodies.md`](docs/research/skill-bodies.md) | The built-in skill bodies, with the source of each rule |
| [`impeccable-frontend-craft.md`](docs/research/impeccable-frontend-craft.md) | Impeccable → `frontend-craft` skill and `ui_lint` hook |
| [`security-review-oss.md`](docs/research/security-review-oss.md) | Pre-open-source security review: threat model and findings F1–F9 |
| [`security-gap-audit.md`](docs/research/security-gap-audit.md) | Gap audit against catalogs (OWASP LLM/Agentic, MITRE ATLAS, promptfoo, garak) |
| [`redteam-results.md`](docs/research/redteam-results.md) | Measured red-team results, suite validity fixes and sandbox evidence (Portuguese) |

Other documents: [`DESIGN.md`](DESIGN.md) (decisions and what was rejected),
[`FINDINGS.md`](FINDINGS.md) (bugs and gaps, with what was verified),
[`CONTRACT.md`](CONTRACT.md) (historical record of the contract used to build v0.2 in parallel),
[`CHANGELOG.md`](CHANGELOG.md), [`CREDITS.md`](CREDITS.md).

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) and the [Code of Conduct](CODE_OF_CONDUCT.md). Issues and
PRs in English or Portuguese are welcome. Before a PR:

```sh
gofmt -l .        # must print nothing
go vet ./...
go test -count=1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o /dev/null .
sh -n scripts/install.sh
```

No new dependencies beyond `golang.org/x/{sys,text}`. Report vulnerabilities as described in
[`SECURITY.md`](SECURITY.md), never in a public issue.

## License

[MIT](LICENSE). Parts derived from other projects keep their original license, in
[`third_party/`](third_party): pi's edit engine (MIT) and Impeccable's `frontend-craft`/`ui_lint`
(Apache-2.0). Credits and inspirations in [`CREDITS.md`](CREDITS.md).
