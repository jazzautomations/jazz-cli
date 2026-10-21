# Bob Shell, Kiro CLI and Devin CLI compared with jazz-cli

I did not modify the jazz-cli repo. Sources were the official docs, the changelogs, and a clone of the Amazon Q CLI source at `<scratch>/repos2/amazon-q-developer-cli`. Bob Shell, Kiro CLI and Devin CLI are all closed source. The only one of the four codebases I could read is the Q CLI, which is open source (Apache/MIT) but now only gets security fixes.

## 1. IBM Bob Shell (`bob`)
- **What it is:** the terminal client for IBM Bob, IBM's multi-model coding agent (Project Bob was shown at TechXchange 2025; general availability was 28 Apr 2026). Version 1.0.1 shipped in Mar 2026. Version 2.0 (Aug 2026) runs on the same agent and harness as the Bob IDE, and tasks are shared with the IDE. It is a Node 24+ package, installed with `curl …/bobshell.sh | bash`. Platforms include z/OS and Linux on IBM Z. [docs](https://bob.ibm.com/docs/shell), [changelog](https://bob.ibm.com/docs/shell/changelog), [Aug 2026 blog](https://bob.ibm.com/blog/august-2026-release)
- **Execution:** tools run on your machine. The models are cloud-hosted and routed by IBM (Claude, Mistral, Granite and others, per press reports: [devops.com](https://devops.com/ibm-bob-takes-ai-coding-assistants-to-the-next-level/)).
- **Commands:** `bob chat` (interactive), `bob run` (headless), `bob mcp add|add-json|list|remove`, `bob acp`.
- **Headless:** `--format pretty|json|stream-json`. The json form includes a `stats` object with tokens, cost and tool calls. Other flags: `--max-cost <bobcoins>`, `--max-turns`, `--disable-tool-groups execute,mcp`, `--disable-subagents`, `--resume latest`, `--trust`. All tools are pre-approved in `bob run`. [non-interactive docs](https://bob.ibm.com/docs/shell/getting-started/start-bobshell-non-interactive)
- **Custom modes:** defined in `~/.bob/custom_modes.yaml` or `.bob/custom_modes.yaml`. Keys are `slug`, `roleDefinition`, `whenToUse`, `customInstructions` and `groups` (read, edit, browser, command, mcp). The edit group can be limited with `fileRegex`. Per-mode rules go in `.bob/rules-<slug>/`. Select a mode with `--chat-mode=` or `/mode`. [custom modes](https://bob.ibm.com/docs/shell/configuration/custom-modes-bobshell)
- **Other features:** Skills, subagents, `PreCompact`/`PostCompact` hooks, HTTPS hook handlers, `EnforcedHooks` policy, MCP OAuth, background processes, `office_read`/`office_edit` tools (docx/xlsx/pptx), `/export` (md/json), `/vim`, Tab to queue messages, `/manage-secrets`.
- **Settings:** `~/.bob/settings/settings.json`. Logs go to `~/.bob/logs/shell/`. Instructions come from `AGENTS.md` (global, then each folder up the tree). [configuration](https://bob.ibm.com/docs/shell/configuration/configuring)
- **Auth:** IBMid/SSO in the browser for interactive use. `BOB_API_KEY` is required for `bob run`.
- **Pricing:** Trial (50 Bobcoins for 30 days), Pro $20, Pro+ $60, Ultra $200. [pricing](https://bob.ibm.com/pricing)
- **Unverified:**
  - The "45% productivity" figure comes from IBM's own internal testing.
  - The routing criteria are not documented.
  - I suspect v1 was a fork of Gemini CLI, because setting names like `general.checkpointing.enabled`, `vimMode` and `hideBanner` match Gemini CLI. The docs never say so.

## 2. Kiro CLI (`kiro-cli`, the successor to `q chat`)
- **What it is:** a closed-source product. The Q CLI repo says it is "no longer actively maintained … now available as Kiro CLI".
- **Execution:** local by default. `--cloud` runs it in a cloud sandbox that keeps going after you disconnect and can be resumed from elsewhere. [V3 new features](https://kiro.dev/docs/cli/v3/new-features/)
- **Tools:** `read_file`, `list_directory`, `file_search`, `grep_search`, `fs_write`, `fs_append`, `str_replace`, `delete_file`, `execute_bash`, `web_search`, `web_fetch`, `invoke_sub_agent`, `disclose_context`, `introspect`, `code`, `knowledge` (experimental). The `code` tool combines a built-in tree-sitter layer (18 languages: symbol search, outline, AST pattern search and rewrite) with optional LSP (references, rename, diagnostics). It is set up with `/code init`, which writes `.kiro/settings/lsp.json`. [tools](https://kiro.dev/docs/tools/), [code intelligence](https://kiro.dev/docs/tools/code-intelligence/)
- **Specs:** `/spec new` writes `requirements.md` (or `bugfix.md`), `design.md` and `tasks.md`, with an approval gate after each phase. At a gate, `Ctrl+X` lets you add line comments that go back to the agent as one revision request. "Quick Spec" skips the gates. Running all tasks builds a dependency graph and runs independent tasks in parallel waves. [specs](https://kiro.dev/docs/specs/)
- **Steering:** files in `.kiro/steering/` or `~/.kiro/steering/`, with front matter `inclusion: always|fileMatch|manual|auto` and `fileMatchPattern`. Files can reference others with `#[[file:path:10-20]]`. AGENTS.md is always loaded. [steering](https://kiro.dev/docs/steering/)
- **Hooks:** JSON files in `.kiro/hooks/` and `~/.kiro/hooks/` (`version: v1`). Triggers include `PostFileSave` and Pre/PostToolUse. An action is either `command` or `agent` (inject a prompt). A `Stop` hook can include a `confirm` block, and an optional `confirmCommand` can decide whether to show it. [hooks](https://kiro.dev/docs/hooks/)
- **Agents and permissions:**
  - Custom agents are Markdown files whose front matter holds MCP servers, permissions and resources; the body is the system prompt.
  - Untrusted workspaces load no project agents, steering or MCP, and every shell command prompts.
  - `/tools trust-all` approves everything for the session only.
  - `--sandbox` runs commands in a local sandbox and exits if it cannot start.
- **Subagents:** run in parallel. `Ctrl+G` opens a monitor for them.
- **Tangents:** `/tangent [name|ls|root|merge]` branches the conversation into a tree, and `merge` injects only a summary into the destination. [tangent](https://kiro.dev/docs/cli/v3/tangent/)
- **Memory and knowledge:** memory is titled records, with modes "use and learn", "use only" and off. [memory](https://kiro.dev/docs/memory/) Knowledge bases are added with `/knowledge add --index-type Fast|Best`; Fast is BM25, Best uses MiniLM embeddings (Q CLI `docs/knowledge-management.md`).
- **Checkpoints:** the Q CLI source (`checkpoint.rs`) uses a shadow bare git repo with `--work-tree`, so it can capture changes made by bash. Kiro's own docs describe file snapshots only and say they do not track changes from bash or MCP. [checkpoints](https://kiro.dev/docs/checkpoints/)
- **Terminal UI:**
  - Mermaid diagrams drawn as terminal art; long pastes collapse into a chip.
  - Approvals queue one at a time with Yes/Trust/No or free-text feedback.
  - `Ctrl+T` opens the transcript in `$PAGER`; `/copy` works over SSH.
  - Theme follows the terminal background automatically.
  - `/voice` uses local transcription.
  [terminal UI](https://kiro.dev/docs/cli/terminal-ui/)
- **Headless:** `--no-interactive`, `KIRO_API_KEY`, `--output-format stream-json`, `--require-mcp-startup` (exit code 3). [headless](https://kiro.dev/docs/cli/headless/)
- **Pricing:** Free (50 credits) up to Power $200. Sign-in with AWS Builder ID or social login. [pricing](https://kiro.dev/pricing/)
- **Caveats:** the docs disagree on whether the todo tool is GA or experimental. How `auto` inclusion picks files is "harness-determined" and not documented.

## 3. Devin CLI (`devin`, Cognition)
- **What it is:** a local agent that can move work to cloud Devin VMs: `--cloud`, `/handoff` (sends the conversation, the branch and any uncommitted diff), `devin ssh <session>`. It replaced the Windsurf CLI path ("Legacy Windsurf Auth"). [CLI pages index](https://docs.devin.ai/llms.txt), [handoff](https://docs.devin.ai/cli/handoff.md)
- **Permission modes:** Normal, Accept Edits, **Smart**, Bypass, and Autonomous (requires `--sandbox`). Smart mode lets "a fast model" decide whether non-edit actions are safe. Some things are never auto-approved: package installs, git commands that change state, rm/sudo, dotenv files and key material. [essential commands](https://docs.devin.ai/cli/essential-commands.md), [permissions](https://docs.devin.ai/cli/reference/permissions.md)
- **Permission rules:** `Read()`, `Write()`, `Exec()` (whole-word prefix match) and `Fetch()` (URL Pattern), in `.devin/config.json` or `config.local.json`.
- **Sandbox:** bubblewrap plus socat on Linux. Only the workspace and granted `Write()` scopes are writable, paths with a `Read()` deny are hidden, and an optional domain allowlist goes through a loopback proxy. It fails closed: if the sandbox cannot start, the CLI does not start. [sandbox](https://docs.devin.ai/cli/sandbox.md)
- **Model routing:**
  - **Adaptive** routes each request to a model and charges a flat $0.50/$2 per million tokens. [adaptive](https://docs.devin.ai/cli/adaptive.md)
  - **Fusion** pairs a frontier "lead" that plans and reviews with a cheap "sidekick" that writes code and runs tests. [fusion](https://docs.devin.ai/cli/fusion.md)
  - **Refusal fallback** switches to another model when a provider refuses under its usage policy.
- **Subagents:** profiles in `.devin/agents/<name>.md` with front matter `name`, `description`, `model`, `allowed-tools`, `max-nesting`. They can run in the foreground or background; `Ctrl+B` sends one to the background. Background subagents only get tools already approved in the session; anything else is auto-denied. Subagents can be resumed. [subagents](https://docs.devin.ai/cli/subagents.md)
- **Commands:**
  - `/goal`: keeps working on an objective across turns.
  - `/loop`: each review of the diff runs in a fresh read-only subagent.
  - `/btw`: a side chat that runs in parallel with the main agent.
  - `/recap`: a short summary of the session.
  - `/steps`, `/fork [step]`, `/revert <step>`.
  - `notify` setting: never, smart or always. [changelog](https://docs.devin.ai/cli/changelog/stable.md)
- **Hooks:** add `PermissionRequest`, `PostCompaction` and `SessionEnd` events. [lifecycle hooks](https://docs.devin.ai/cli/extensibility/hooks/lifecycle-hooks.md)
- **Config import:** `read_config_from` reads Claude Code, Cursor (`.mdc`) and Windsurf rules, skills and MCP config. [config import](https://docs.devin.ai/cli/reference/configuration/read-config-from.md)
- **Other:** plugins, ACP (`devin acp` for Zed, JetBrains and Xcode), OpenTelemetry export.
- **Unverified:** the Smart classifier's inputs, Adaptive's routing criteria and Fusion's savings are not disclosed. Pricing for the CLI itself is not documented.

## Gaps jazz-cli should adopt (ranked)
Every item below is something jazz-cli does not have today. Check CONTRACT.md §1 before assigning work, because it sets which files each owner may edit.

1. **[must] Shadow-git checkpoints that also capture bash changes** (Q CLI `checkpoint.rs`). Today `/undo` and `/rewind` miss changes made through bash, and weak models often edit with `sed -i` or `> file`.
   - Add a new `checkpoint.go` that creates a bare repo at `dataDir()/checkpoints/<projectSlug>.git`.
   - Snapshot at the start of each turn and after each bash call that writes: `git --git-dir=… --work-tree=root add -A && commit -q --allow-empty -m turnN`. Respect `.gitignore`, skip files over 1 MB, and turn it off when the tree has more than 20k files or there is no git binary. Fall back to the existing snapshots in that case.
   - `/rewind n` and `/undo` call `checkout <tag> -- .` and then delete files that were added since.
2. **[must] "Smart" permission mode** (Devin). It removes most prompts while keeping safety, and the cost is one short, cacheable classifier call.
   - In `perms.go`, add `ModeSmart`. Edits inside the workspace are auto-approved.
   - For other calls, send a request with no tools to `omni/fast` containing only the command and the cwd, and require the answer `SAFE` or `ASK`. Cache the verdict per derived rule prefix for the session. A timeout or unclear answer means ASK.
   - Never auto-approve when `DangerReason`, git mutations, package installs, `.env`, keys or jazz's own config are involved.
   - Add the mode to the Shift+Tab cycle and `/mode`.
3. **[must] Conditional steering** (Kiro `inclusion: fileMatch|manual|auto`). With small context windows, rules should only load when they are relevant.
   - In `prompt.go`, read `.jazz/steering/*.md` and `.cursor/rules/*.mdc` (Cursor's `globs` key). `always` files go into the frozen system prompt. `auto` and `manual` files are listed the way skills are.
   - For `fileMatch`, the first time a read, edit or write touches a matching path, append the file once as a `<system-reminder>` on the tool result in `agent.go`. This only appends, so the prompt cache stays valid. Track what was injected per session.
4. **[should] Spec workflow** (Kiro `/spec new`). Splitting work into explicit phases helps weak models most. Ship it mostly as prompts:
   - `/spec <name>` runs in plan mode and writes `.jazz/specs/<name>/requirements.md` (EARS-style acceptance criteria), then `design.md`, then `tasks.md` (checkboxes), asking for approval after each.
   - `/spec run` executes one unchecked task per turn and ticks it off, with a test command per task.
   - Put the code in `ui.go` (commands) plus built-in templates embedded in `prompt.go`.
5. **[should] `/loop` review in a fresh context** (Devin). A model reviewing its own output in the same context misses its own mistakes.
   - After a turn ends with edits, run `runTask` (read-only) with "review `git diff`, list concrete defects or reply LGTM".
   - Feed any defects back as the next user turn. Stop after N rounds or on LGTM. Require a clean git tree at the start. Add a headless `--review N` flag.
6. **[should] `--until "<cmd>"` and `/goal`** (Devin `/goal`, plus the Kiro/Q Stop hook). This gives an objective test for "done", which counters the free-model habit of claiming success.
   - In `agent.go`, when a turn stops, run the command in the shell. If it exits non-zero, append its output and continue, up to `--max-steps`.
   - `/goal` stores the objective in the session JSONL and re-adds it as a reminder after compaction.
7. **[should] OS sandbox** (Devin `--sandbox`, Kiro `--sandbox`, both fail closed). It makes yolo or autonomous runs with flaky free models safe.
   - In `shell.go`, when `--sandbox` is set, start the persistent bash under `bwrap --ro-bind / / --bind $root $root --bind $TMP $TMP --dev /dev --proc /proc [--unshare-net]` on Linux, or `sandbox-exec -p <profile>` on macOS. Refuse to start if neither exists.
   - Add a mode where commands inside the sandbox run without prompts.
8. **[should] Parallel subagents and subagent profiles** (Devin, Kiro). Spreading work across several free providers at once gets around per-provider rate limits; the work is network-bound, so it is fine on the Pentium M.
   - In `agent.go`, when one batch contains several `task` calls, run them in goroutines (cap 3, setting `jazz.subagents.max`) and keep results in the original order.
   - Load `.jazz/agents/*.md` and `.claude/agents/*.md` with front matter `name`, `description`, `model`, `tools`, and add an optional `agent` parameter to `taskSpec`. Background subagents inherit approvals only, as in Devin.
9. **[should] Persistent memory** (Kiro memory). Saves rediscovering build commands every session.
   - Store `dataDir()/memory/<slug>.md` and load it into the frozen system prompt (8 KB cap).
   - Add `/remember <text>` and `/memory [edit|off]`. An optional `memory: learn` setting has the Stop path ask `omni/fast` to extract at most 3 durable facts, which are shown to the user before saving.
10. **[should] `/btw` side question** (Devin `/btw`, Kiro tangent). Answers a question without growing the main context.
    - Send one request with no tools over the current history plus the question. This reuses the cached prefix, and the answer is never appended.
    - Add `/btw merge` to append a one-paragraph summary. The code goes in `ui.go` plus a small `Agent.Side()` in `agent.go`.
11. **[nice] More hook events** (Devin, Bob): `PreCompact`/`PostCompact` (re-inject critical context), `SessionEnd`, and `PermissionRequest` (an external script decides, for example approval from a phone). Add these in `hooks.go` next to the existing event dispatch.
12. **[nice] Refusal and content-filter fallback** (Devin). In the providers, map `finish_reason: content_filter` and Anthropic `stop_reason: refusal` to the omni fallback error so the next candidate is tried (`provider_*.go`, `omni.go`).
13. **[nice] Headless guards** (Bob, Kiro): `--max-tokens`/`--max-cost` budgets (jazz already counts usage for `/cost`), `--require-mcp` with a new exit code, and a `stats` object in the JSON output (`headless.go`, `jsonui.go`).
14. **[nice] Terminal notifications** (Devin `notify`). Send OSC 9 / OSC 777 (or `notify-send` when present) when a turn takes more than 30 s or an approval is waiting. This extends `TermUI.Bell()` and adds `notify: never|smart|always`.
15. **[nice] Lightweight code map** (Kiro `/code overview`). Tree-sitter needs cgo, which conflicts with the static 386 binary, so use regex symbol extraction for Go, Python, JS/TS and Rust instead. Expose it as `/map` plus a `jazz map` subcommand the model can run through bash; this adds no new tool and keeps the prompt small.

## Left out on purpose
- **Cloud handoff and VMs** (Devin, Kiro): need hosted infrastructure.
- **ACP editor server** (all three): valuable but large; worth it only if Zed or JetBrains use matters.
- **Mermaid rendering and voice:** too heavy for the target machine.
- **Semantic knowledge index:** MiniLM embeddings are too heavy for the Pentium M, and BM25 adds little over `rg`.
- **Plain Fusion/Adaptive:** jazz's DESIGN.md already rejects two-model pairing on cost and latency grounds. Gap #5 gets most of the benefit by using the second model only for review.
