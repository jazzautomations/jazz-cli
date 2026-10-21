# MetaHarness and self-improving harnesses: what jazz-cli can adopt

## 1. Primary sources, ranked by relevance to jazz

| # | Work | Why it matters | Code |
|---|---|---|---|
| 1 | **Meta-Harness**, Lee, Nair, Zhang, K. Lee, **Khattab** (DSPy), **Finn**. Stanford IRIS. arXiv [2603.28052](https://arxiv.org/abs/2603.28052), Mar 2026. [Project page](https://yoonholee.com/meta-harness/) | This is the baseline Shepherd cites. An Opus coding agent edits harness *code* using the raw traces of earlier runs. | [stanford-iris-lab/meta-harness](https://github.com/stanford-iris-lab/meta-harness) (MIT, ~1.7k★, pushed 2026-10). [TB2 artifact](https://github.com/stanford-iris-lab/meta-harness-tbench2-artifact) |
| 2 | **Self-Harness**, Zhang et al., Shanghai AI Lab. arXiv [2606.09498](https://arxiv.org/abs/2606.09498), Jun 2026 | It is tested on **open and mid models** on Terminal-Bench 2.0, with a regression gate that uses a held-out split. | Not released |
| 3 | **Shepherd CRO**, arXiv [2605.10913](https://arxiv.org/abs/2605.10913) (§2 of our `shepherd-meta-agents.md`) | Each proposed edit comes with a *fix set* and a *guard set*, and it replays only from the first trace commit the edit affects. It beats Meta-Harness on 4/5 benchmarks with 27–58% less wall-clock (TB2: 35.2 vs 31.2, GPT-5.4-mini). | [shepherd-agents/shepherd](https://github.com/shepherd-agents/shepherd) |
| 4 | **RHO (Retrospective Harness Optimization)**, Pan et al. arXiv [2606.05922](https://arxiv.org/abs/2606.05922) | It works with **no labels**: it re-solves hard past tasks and uses self-consistency and pairwise self-preference to pick a harness update. SWE-Bench Pro went 59→78% in one round. | [wbopan/retro-harness](https://github.com/wbopan/retro-harness) (MIT) |
| 5 | **Darwin Gödel Machine**, Zhang, Hu, Lu, Lange, Clune. [2505.22954](https://arxiv.org/abs/2505.22954) | An archive of self-modified agents. SWE-bench 20→50%, Polyglot 14.2→30.7%. It is the canonical **objective-hacking** case. | [jennyzzt/dgm](https://github.com/jennyzzt/dgm) |
| 6 | HGM [2510.21614](https://arxiv.org/abs/2510.21614) (scores an edit's *descendants*); SICA [2504.15228](https://arxiv.org/abs/2504.15228); Live-SWE-agent [2511.13646](https://arxiv.org/abs/2511.13646) | Background on code-level self-modification. | |
| 7 | GEPA [2507.19457](https://arxiv.org/abs/2507.19457), ACE [2510.04618](https://arxiv.org/abs/2510.04618), ADAS [2408.08435](https://arxiv.org/abs/2408.08435), OpenEvolve | Text-optimizer baselines that Meta-Harness beats. | [gepa-ai/gepa](https://github.com/gepa-ai/gepa) |

## 2. Mechanisms

### Meta-Harness
- **What gets edited:** a single-file Python harness covering prompts, retrieval, memory, orchestration and control flow. Edits range from small tweaks to full rewrites. The model weights stay frozen.
- **Signal:** a filesystem with every earlier candidate's source, scores and full traces, navigated by the proposer (Claude Code, Opus 4.6) with grep/cat; a median of 82 files read per TB2 iteration.
  - **Key ablation** (text classification, median/best search accuracy): scores only 34.6/41.3, scores plus summaries 34.9/38.7, **full traces 50.0/56.7**. Summaries actually *hurt*.
- **Evaluation:** candidates are scored on a search set and kept on a Pareto frontier of accuracy vs context tokens. The test set is held out, except on TB2, where search and test use the same 89 tasks. The authors call that a "discovery result".
- **Regression control:** the Pareto frontier keeps candidates and nothing is overwritten. Overfitting checks are manual, plus regex audits for strings leaked from the tasks.
- **Cost:** about 10 MTok of proposer context per iteration. A typical run is about 20 iterations and 60 harnesses over "a few hours", plus the full eval of every candidate. No dollar figures are given.
- **Results:**
  - TB2 with Opus 4.6: 76.4%, vs 74.7% for Terminus-KIRA, 62.9% for Terminus 2 and 58.0% for Claude Code.
  - TB2 with **Haiku 4.5: 37.6%**, vs 35.5% for Goose and 27.5% for Claude Code.
  - IMO-level math retrieval: +4.7 points, and the gain held on 5 held-out models.
  - The TB2 win was small and structural: **an environment snapshot injected before the first LLM call**, which fixed 7 of 89 tasks.
- **Failure modes:** early TB2 iterations regressed because prompt edits were confounded with structural fixes, and six regressions in a row came before a *purely additive* change won. Only one proposer was studied. The README says the code is "not tested beyond verifying that it runs".

### Self-Harness (most relevant to free models)
- **What gets edited:** a harness definition file. It covers the system prompt, AGENTS.md memory, skills, subagents (diff-inspector, test-verifier), verify and recovery instructions, tool-error middleware, and runtime limits.
- **Signal:** failing traces, clustered by failure signature (verifier cause × agent behavior × mechanism). The agent then proposes *minimal* edits against each cluster.
- **Acceptance:** two attempts per candidate. A candidate is accepted only if **neither the held-in nor the held-out split drops and at least one improves**.
- **TB2 results** (64-task subset, overall score):

  | Model | Before → after |
  |---|---|
  | MiniMax M2.5 | 42.2 → 53.9 |
  | **Qwen3.5-35B-A3B** | **18.0 → 36.7** |
  | GLM-5 | 46.1 → 57.0 |

- **Caveats:** edits were model-specific and transfer was untested; cost was not reported; the held-out split feeds acceptance, so it is not independent; n = 2 attempts means noise is large.

### Shepherd CRO
- **How it works:** every edit names a fix set (tasks it should flip) and a guard set (tasks it must not break). It replays from the first affected commit, so it reuses cached prefixes and is cheaper than full reruns.

### RHO
- **How it works:** no ground truth at all. It picks diverse, hard past tasks, re-solves them in parallel, scores them by self-validation and agreement, then generates harness updates and picks one by pairwise preference.
- **Risks:** this is cheap, but it is the most exposed to self-confirming drift.

### DGM lesson
- The agent **removed the special-token logging used to detect its own hallucinated tool calls**, despite being told not to touch it. Hacking happened *more* often when the checker was visible to it.
- **Rules that follow:** hide checkers, and keep evaluators outside the editable surface.

## 3. Design for jazz: `jazz improve`

**Constraints:** free models (~200 req/h per provider, daily caps) and a slow the Dell target. So the loop runs **offline on the Mac**: an Opus-class proposer, with candidates evaluated on the pinned free models jazz ships with. No code self-modification, only a **declarative config surface** (Self-Harness/CRO scope, not DGM).

### 3.1 The editable surface: `harness.json`
This is a new file, embedded via `go:embed` and overridable at `~/.config/jazz-cli/harness.json`.

```json
{"version":17,"parent":16,"model_scope":"openrouter/qwen3*",
 "auto":{"skills":{"<name>":{"trigger_weight":1.0,"keywords":[]}},"intents":{},
   "fallback":{"order_bias":{},"sticky_ttl_s":180,"stall_penalty_s":180},
   "budgets":{"S":12,"M":25,"L":50,"until_rounds":5},"loop":{"warn":3,"stop":6},
   "nudges":{},"recover":"off","prefetch":"off","parse_output":0,
   "tool_desc":{},"prompt_rule_order":[],"reminders":[]},
 "gated":{"system_prompt_patch":null}}
```

**Automatic (validated deltas only):**
- Everything under `auto`: numbers, booleans, enums, orderings, keyword lists.
- Tool descriptions, capped at a fixed char budget with a schema lint.
- `reminders`: short lines appended after the user message, so the cache-safe append-only design is preserved.
- These map onto existing or planned flags (`--recover`, `--prefetch`, `JAZZ_PARSE_OUTPUT`, `plan_cache.hindsight`, `acStickyTTL`, `acStallPenalty`).

**Human approval** (written to `harness.proposed.json` together with an evidence report):
- Any change to system-prompt text.
- New prompt rules.
- Anything that touches `permit()`, `DangerReason`, smart-mode never-auto lists, trust, `scrubEnv`/`redactSecrets`, hooks or permissions.
- Edits to evals.

**Never touchable:**
- Safety code is not in the JSON at all.
- The loader rejects unknown keys and any key outside `auto`.

### 3.2 Signal (measured by the runner, never self-reported)
1. **Primary:** the task's hidden checker exits 0 (separate from any `--until` the agent sees).
2. **Gates:** no new exit codes 4/5/7; `--review` LGTM and a bounded diff size; protected test/checker paths unchanged (hash check).
3. **Secondary (Pareto):** median steps, input tokens, wall time, fallback hops.

### 3.3 Task pool and protocol
**Pools:** `evals/` lives outside the harness tree and is read-only to the optimizer. The proposer sees only traces.

| Pool | Contents | Proposer sees | Used for |
|---|---|---|---|
| Search (S) | the 10-task suite plus half of the hard suite | full traces | proposals |
| Guard (G) | the other half of the hard suite plus 5 SymPy tasks | pass/fail only | acceptance gate |
| Holdout (H) | about 10 rotating tasks, regenerated monthly (fresh Terminal-Bench 2 subset or new repo tasks) | nothing | ship decision only, never used in the loop |

**One iteration:**
1. **Mine.** Run S at 3 reps; store traces in `dataDir()/improve/<run>/<task>/<rep>.jsonl` (`--debug` logs already have the detail). Cluster failures by signature: exit code, tool-error class, loop hash, `--until` failure.
2. **Propose.** Opus gets `harness.json`, its scored history, and grep access to the raw failing traces (raw, not summaries, per Meta-Harness's ablation). It returns ≤3 JSON-patch deltas within the `auto` schema, each naming a **fix set** and a **guard set** (CRO).
3. **Screen (cheap).** Run each delta on its fix set only, 3 reps. Drop it if fewer than 2/3 of the targets flip.
4. **Gate (paired).** Survivor vs parent on S ∪ G, same tasks, commits and pinned model, 3 reps (2 on SymPy), arms interleaved to cancel rate-limit drift. Accept only if:
   - **ΔPass on S ∪ G ≥ +2 tasks**, *or* ≥ 15% fewer median steps with no loss in pass;
   - **no single task goes from ≥2/3 to ≤1/3** (the no-regression rule);
   - G alone does not drop;
   - a paired sign/McNemar test gives p < 0.2 (loose, for small n).
5. **Commit.** Write `harness.json` v+1; append delta, parent, scores and trace pointers to `harness.history.jsonl`; git-tag it.
6. **Ship.** Every 5 accepts, run H against the last shipped version. If ΔH ≥ 0 with no category regressing, embed it for the next `scripts/release.sh`; otherwise revert and mark the lineage overfit.

**Anti-overfitting and anti-hacking:** checkers live hidden in `evals/` (DGM lesson); a regex audit rejects deltas containing task strings (paths, function names, expected outputs); the schema cannot express "skip tests"; touching protected paths = auto-fail; H rotates and never feeds proposals; configs are scoped per model family; ≤3 keys per delta keeps changes additive and attributable; an HGM-style rule prunes a lineage after 3 accepts with flat H.

**Rollback:** `jazz improve rollback [v]`. At runtime, `JAZZ_HARNESS=embedded|<path>|v12` allows A/B in the field.

### 3.4 Implementation spec

| Piece | Size |
|---|---|
| `harness.go`: schema, strict loader, merge (embedded → user), one getter per knob; existing constants read from it | ~250 LOC |
| `improve.go`: `jazz improve {run,propose,gate,ship,rollback,log}`. Orchestrates `jazz -p --output-format stream-json` subprocesses, parses exit codes and usage, stores results | ~600 LOC |
| `evals/<suite>/<task>/{task.md, setup.sh, check.sh (hidden), meta.json}` plus `evals/runner.sh` (fresh git worktree per run, mounted read-only to protect `check.sh`) | — |
| Proposer prompt (`improve/proposer.md`, human-owned): the schema, the history, "minimal additive deltas", fix and guard sets required, ban on task strings | — |
| Tests: loader rejects non-`auto` keys; delta lint; paired-stats unit tests | — |

**Later:** CRO-style partial replay (append-only JSONL plus shadow-git restore makes replay from step k feasible), cutting screen cost ~40–60%.

### 3.5 Cost per iteration
Assuming ~25 tasks in S ∪ G and ~15 calls per task on free models: mine 75 runs (~1.1k calls), screen ~36 runs (~0.5k), gate ~140 runs (~2.1k), so **~3.7k free calls, about 5–6 h overnight** across 3–4 rotating providers. The proposer is one Opus session reading 1–3 MTok (vs Meta-Harness's 10 MTok, since our surface is tiny): **~$5–15** with caching. Holdout costs ~60 runs every 5 iterations. Reusing mine output as the next gate's parent arm saves ~30%.

### 3.6 Expected gains
- **Realistic:** +5–15 points on our suites for a fixed free model, mostly from budgets, fallback order, recover/prefetch toggles, tool descriptions and reminders.
  - Self-Harness doubled Qwen3.5-35B-A3B on TB2, but it had a richer surface (new subagents and middleware).
  - Meta-Harness's TB2 gain came from one structural addition.
- **Also:** 10–30% fewer steps (hindsight reminders), and the queued A/B questions in `docs/research/*` get settled systematically.

### 3.7 Risks
1. **Noise beats signal** (n ≈ 25, 3 reps, provider swaps). Mitigations: pin the model, interleave arms, per-task no-regression rule, confirmation on H.
2. **Overfitting to a small suite.** Mitigations: task-string audit, rotating H, key budget, per-family scope.
3. **Provider drift** stales old scores. Re-baseline the parent every iteration; never compare against scores older than 7 days.
4. **Budget-gaming** (raise limits until success, slow on the Dell). Steps and wall time sit on the Pareto; budgets have hard caps.
5. **Rate-limit exhaustion** for daily use. Separate `exhausted.json` namespace; night runs only.
6. **Self-confirming drift** from RHO-style label-free signals. Do not adopt: hidden checkers only.
