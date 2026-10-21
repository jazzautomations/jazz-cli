# Shepherd (Stanford NLP, 2026): what it is and what jazz-cli can borrow

## 1. Which "Shepherd"?

The name is ambiguous. I ranked the candidates by Stanford affiliation and relevance to terminal or coding agents:

1. **Shepherd: Enabling Programmable Meta-Agents via Reversible Agentic Execution Traces.** Yu, Chong, Nandi, Soylu, Sun, **Manning** (Stanford) and Shi (Northeastern). arXiv 2605.10913, v3 dated 24 Jun 2026. https://arxiv.org/abs/2605.10913. Site: https://shepherd-agents.ai. Code: https://github.com/shepherd-agents/shepherd (MIT licence, about 2.5k stars, last push 2026-09-09). **This is the one**: Stanford authors, and it is evaluated on Terminal-Bench 2.0 and SWE-Bench Verified.
2. SWE-Shepherd, a process reward model (PRM) for code agents (arXiv 2604.10493). Web-Shepherd, a PRM for web agents (2505.15277). GUI-Shepherd (2509.23738). These are different groups, and each one needs a trained reward model, which we can't host on the Dell.
3. "LLM Shepherding" (2601.22132, cost routing) and "Shepherd: A Critic for LM Generation" (2308.04592, 2023) are off-target.

## 2. The paper in depth

**Problem.** Meta-agents supervise, repair or train other agents. To do that they need to watch a run, intercept it, fork it, revert it and modify it. Today's substrates (OpenHands event streams, AgentGit, BranchFS) only expose transcripts or filesystem snapshots, so every meta-agent builds its own tooling.

**Mechanism.** Shepherd is a Python substrate built on four primitives:
- **Task:** a typed `@agent` function.
- **Effect:** every LLM call, tool call or environment change is a typed event. It is split into an *intent* and an *outcome*, so a supervisor can deny an action before it happens. Each effect also has a reversibility tier.
- **Scope:** fork, merge and discard, backed by an overlay copy-on-write filesystem.
- **Trace:** a Git-like commit graph of the run. Any past agent-plus-environment state can be checked out byte for byte.

Fork and revert take about 140 ms (5× faster than `docker commit`), and prompt-cache hits stay around 95% on replay.

**Three demos (each a "proof of existence", by the authors' own description):**
- **Runtime supervisor (CooperBench, 479 pairs).** The workers are two Haiku 4.5 agents in parallel. A supervisor (Sonnet 4.6 or Opus 4.7) is called every 5 s on a snapshot under 3K tokens. Per agent, the snapshot holds the last 25 tool-call summaries (about 150 bytes each), the error streak, the files touched, and how long ago it last intervened. The supervisor picks one of four actions, in rising cost:
  - **none** (the default; "over-intervention destroys progress");
  - **inject**: append one user message, so the cache is kept;
  - **handoff**: a fresh session, but the files stay;
  - **discard**: a fresh session plus the filesystem rolled back.

  Its heuristic: "10+ identical calls = loop; 3–5 retries is normal." Pass rate rose from 28.8% (coop) to 45.3% with Sonnet and 54.7% with Opus. The solo baseline is 57.2%. The supervisor's overhead was 1.4–4.3 min per pair.
- **Counterfactual Replay Optimization (CRO).** The executor is GPT-5.4-mini and the optimizer is GPT-5.4. CRO proposes harness edits, each with a "fix set" and a "guard set" of tasks. It replays only from the first commit the edit affects. It beat MetaHarness on 4 of 5 benchmarks with 27–58% less wall-clock, and scored 35.2 vs 31.2 on Terminal-Bench 2.0.
- **Meta-agent-guided Tree-GRPO.** Opus picks the fork turn, defined as "the earliest turn where a downstream wrong choice was made, following the causal chain". It must give a concrete bash command, not vague advice. On Terminal-Bench 2.0 this gave +5.2 pts on Qwen3.5-35B-A3B (39.4 vs 34.2) and +3.4 pts on Nemotron-3-Super. This is the only part tested on small open models, and it is training only.

**Appendix D: trajectory compression (the most useful part for us).** After a passing run, GPT-5.4-xhigh reads the trace and returns `{fork_step, hint ≤1500 chars, rationale}` or `no_prune`. The hint names the file path, the approach and the dead end to skip, without giving the solution. The worker then restarts from `fork_step` with the hint added to its system prompt.

| | Runs with a shorter passing rerun | Model calls per run, before → after |
|---|---|---|
| SWE-bench Verified | 68–82% | 21.4→8.9 and 19.0→8.8 |
| Terminal-Bench 2.0 | 68–77% | 15.8→7.1 and 11.4→5.2 |

A best-of-5 control without the meta-agent did **not** close the gap. The same procedure also rescued failed baselines: 12/57 and 13/48 on Terminal-Bench, 10 and 13 on SWE-bench. About ⅓ of forks were full restarts (`f=0`).

**Limitations.**
- The supervisor and the optimizer are strong frontier models, and on short tasks the supervision cost can exceed the worker's.
- No weak model was ever tested as the supervisor.
- Whether a compression hint carries over to *other* tasks is explicitly left open.

## 3. Mapping onto jazz-cli

**What we already have:**
- **Inject:** the verify, todo, fake-action and empty nudges, steering and `<system-reminder>`s, all append-only and cache-safe.
- **Intent gating:** `permit()`, smart mode's SAFE/ASK, PreToolUse hooks.
- **Revert:** shadow-git `cpSnapshot`/`cpRestore`, `/undo`, `/rewind`.
- **Session fork:** `/fork`.
- **Outcome verifier:** `--until` and `--review`.

Our loop detector hashes (tool, args, result), which is stricter than an identical call, so warning at 3 and stopping at 6 already fits their heuristic.

**Skip:**
- The LLM supervisor on every tick: 720 calls/h against a free-tier ceiling of about 200 req/h.
- Typed effects and the Lean semantics.
- Overlay copy-on-write forking (shadow-git already covers it).
- Parallel workers.
- Tree-GRPO, which is training only.

**New and measurable:** the *escalation ladder past inject*, and *hindsight hints*. Both use 0–1 extra LLM calls.

### A1. Deterministic handoff and discard on stall (0 extra LLM calls)

Today a loop at 6 stops the turn (code 4), and when `--until` runs out of rounds the turn stops too (code 8). Shepherd's ladder says to restart the context before giving up.

- **Trigger:** `StopLoop` would fire, **or** `--until` has failed for 3 consecutive rounds with the same failing-test signature, **or** there have been 8 or more tool errors in a row. At most one handoff per turn.
- **Handoff, in a new `agent_recover.go`:**
  - `acHandoffNote(lt)` builds a deterministic note of about 1.5K tokens or less, with no LLM call. It holds the user request and `/goal`, the files changed (`acTurnChanges`), the last 15 tool summaries in Shepherd's ~150-byte format (tool, target, exit, short error), the last `--until` output (≤1.5 KB), and "Approach X failed; do not repeat `<looped call>`".
  - We then call a new `Compact`-like path, `acHardReset(note)`. It replaces the history after the frozen system prompt with that single note, keeps the files, and resets the loop hashes and nudges. The turn then continues inside the remaining `--max-steps`.
  - The point is to give a weak model a clean, short context. The cost is one prompt-cache miss.
- **Discard:** if the latest `--until` output has more failing items than the first round (counted with a simple regex over FAIL, `--- FAIL`, `failed`, `Error`), first call `cpRestore(turn-start snap)`. The note then says "previous attempt reverted".
- **Flag:** `--recover off|handoff|discard`, `JAZZ_RECOVER`. The default stays `off` until it has been measured.
- **Measure:** on the 10-task suite, the hard suite and SymPy, with a pinned model and 3 or more paired reps, compare:
  - the share of runs ending with code 4 or 8 (expected to drop);
  - pass rate (McNemar test);
  - steps, LLM calls and wall time on the Dell;
  - pass rate *after* a handoff fired.

  If post-handoff passes are under 10% of fired cases, the ladder doesn't pay and we drop it.

### A2. Hindsight retry when `--until` is exhausted (1 extra call plus a rerun, opt-in)

This is the rescue path from Appendix D.

- **Trigger:** `--until` has failed for all its rounds, A1 has already been used, and at least 40% of the step budget is left.
- **Call:** one tool-less request to the *strongest* candidate in the omni chain. We can't rely on a weak model for this; Shepherd never tested one.
  - Input: the A1 trail (≤3K tokens) plus the `--until` output.
  - Output: `{"fork_step":int,"hint":"≤1200 chars: exact path, approach, dead end; no code","no_prune":bool}`. It goes through the existing JSON repair in `textcalls.go`.
  - The prompt asks for the earliest wrong turn in the causal chain and a concrete command, as in Tree-GRPO.
- **Rerun:**
  - `fork_step` maps to a per-step tree hash. That needs a new `[]string` of snapshot trees in `turnSnap`, filled by `cpAfterBash`. We restore to it, cut the messages to that step, and append the hint as a `<system-reminder>`, not in the system prompt, so the cache holds.
  - If `fork_step` is 0, or the fork is not resolvable, we do a full revert and restart from the user message.
- **Budget:** 1 call, 1 rerun per turn, no recursion.
- **Flag:** `--hindsight`.
- **Measure:** rescue rate among runs that would otherwise exit with code 8, plus extra calls per rescue. Keep it if the rescue rate is ≥15% at ≤1.3× calls on fired tasks.

### A3. Hindsight memo on verified success, feeding the planned plan cache (1 call per long success)

This extends A4 from `tocomas-plan-reuse.md`. The plan cache currently stores the step template. Shepherd shows that the *hindsight hint* (right file, approach, skipped dead ends) is what halves trajectories, and that resampling without it does not.

- **Trigger:** the turn passed `--until` or ended with a verification bash at exit 0, **and** it took 12 or more steps. Compression gains are concentrated in long runs, and short ones aren't worth a request.
- **Store:** the hint in `dataDir()/plans/<slug>.jsonl` next to the template, with `{wins,fails}`.
- **Inject:** at most one hint for a matching future task (BM25), as a `<system-reminder>`. A failure after injection counts as `fails++`, and two fails evicts the memo. This guards against the transfer gap the paper left open.
- **Flag:** `plan_cache.hindsight=on|off`.
- **Measure:** use a repeated-task protocol. Pass 1 builds the memos; pass 2 runs the same suite plus paraphrased variants, A/B against templates only. Report median steps (we hope for about −30%), pass rate, and the bad-reuse rate.

### A4 (low priority, dev tooling). Counterfactual replay

`jazz replay --session S --from-step k` restores the shadow-git tree and message prefix at step k, then runs only the suffix. This saves free-tier requests when A/B-testing nudges. As in CRO, each change carries a fix set and a guard set. Skip it for `basePrompt` edits, which affect the whole run.

*Sources: arXiv 2605.10913v3 (sections 3–5, App. A, D, E, F), https://arxiv.org/html/2605.10913v3; repo metadata via the GitHub API; jazz-cli `README.md`, `DESIGN.md`, `docs/research/*.md`, `agent.go`, `agent_loop.go`, `checkpoint.go` (read only).*
