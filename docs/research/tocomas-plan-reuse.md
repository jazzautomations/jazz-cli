# "tocomas": research report and what jazz-cli can borrow

## 1. What "tocomas" is: evidence and ranking

1. **TOCOMAS: "Topological Coherence for Self-evolving Multi-agent Systems"**, arXiv **2609.37953** (v1, 29 Sep 2026, Chongqing Univ. of Posts & Telecom), https://arxiv.org/abs/2609.37953. This is the **only** arXiv result for `all:TOCOMAS` and for `all:tocoma`. It is very likely the paper the user saw.
2. Nothing else carries the name. GitHub repo and user search, PyPI and npm (`tocomas`, `toco-mas`, `tocoma`, `tokomas`) all came back empty or unrelated (a colon-cleanser product, personal repos). The paper has **no code link**.

**It does not match the description "a Python + JSON CLI agent that doesn't depend on an LLM provider."** TOCOMAS is an LLM multi-agent research framework. Every agent has a backbone LLM, accessed "through OpenAI-compatible APIs". The only sense in which it is "provider-independent" is that it runs on any OpenAI-compatible backbone. It is not a CLI tool, and the paper never mentions Python or JSON. If the user saw a separate LLM-free tool, we need the link. **I could not find one, so I am not inventing it.** Two arXiv papers cover the "fewer or no LLM calls" angle the user cares about, and I use them below:
- **AgentReuse**, arXiv 2512.21309 (https://arxiv.org/abs/2512.21309): about 30% of real agent requests are identical or similar. It reuses plans by intent classification and reports 93% effective reuse and −93% latency.
- **Agentic Plan Caching (APC)**, arXiv 2506.14852 (https://arxiv.org/abs/2506.14852): it extracts plan templates from finished runs, matches new requests by **keyword extraction**, and has a small model adapt the template. It reports −50% cost and −27% latency.

## 2. TOCOMAS in depth

**Problem.** Multi-agent systems that evolve their roles, communication graph and memory separately end up with responsibilities, handoffs and memory permissions that no longer match the task's dependency structure. The paper calls the needed agreement "topological coherence."

**Mechanisms.**
- **Tool grounding.** The method takes an **upstream-provided** task graph `T=(U,D)` and maps each node to a minimal tool bundle Γ(u) from a public tool graph. Constraints are capability coverage, I/O compatibility, side effects and "provider continuity."
- **Responsibility regions.** Nodes are merged greedily with a pairwise affinity built from four signals: tool overlap, I/O compatibility, dependency adjacency and semantic similarity. The affinity threshold is 0.45, and the sweep peaks at 0.60. A merge is allowed only if the quotient graph stays **acyclic** and capacity limits hold. Each region goes to an agent whose tools cover Γ(S), and one agent may own several regions.
- **Handoffs.** Communication edges are required only where a dependency crosses two different owners. Each handoff passes only the artifact the consumer needs. A gate checks relevance, confidence and conflict, then delivers, rejects or escalates to a verifier. A failed predecessor blocks or retries the downstream step.
- **Memory.** Each record carries owner, region and provenance. Retrieval filters by visibility first, then takes the top-k semantic seeds (k=3) and expands them over the typed graph by K hops. When a region is reassigned, its memory moves with it.
- **Evolution.** After each task, feedback proposes **one** structural change. The change is re-run on that same task and kept only if it passes structural checks and its reward beats the parent's by a margin. Reward is R = Metrics − β·Cost.

**Results (Table 1).** Backbones were Qwen3.8-27B and DeepSeek-V4-Flash, plus GPT-6-luna for a separate comparison.

| Benchmark | TOCOMAS (Qwen) | Best baseline (Qwen) | TOCOMAS (DeepSeek) | Direct LLM call (Qwen) |
|---|---|---|---|---|
| BBEH | 54.78 | 46.52 Peer Review | 55.20 | 19.13 |
| WorkBench | 77.68 | 62.17 ADAS | 61.45 | 19.28 |
| SWE-bench Verified | 48.00 | 40.00 EvoMAS | 49.00 | 9.80 |
| CoMemBench SR | 14.3 | 4.0 SMoA | 14.0 | 0.0 |

**Ablations (WorkBench SR / CoMemBench).**
- Without the topological organization: VNCR −14.33.
- With fixed predecessor handoffs instead of derived ones: VHS −40.85 and CoMemBench SR drops to 0.
- With all memory globally visible: ICS −19.96.
- Without evolution: WorkBench −6.52.
- k=3 beats k=8, so more retrieved context hurt.

**Cost.** Token use is **higher than a direct LLM call** at the same progress. It is lower than Peer Review and SMoA on WorkBench, and above ADAS. Evolution adds one extra full candidate run per task, which is roughly 2× the calls in the evolving phase.

**Limitations and hype flags.**
- The task graph is *given*. The hardest part, decomposition, is outside the method.
- CoMemBench (arXiv 2609.32192) comes from the **same authors**.
- The direct-call SWE-bench Verified baseline (9.8%) is implausibly low for a 27B model with tools, which inflates the size of the gains.
- There are no seeds, variance or confidence intervals. The paper is under review, has no code, and the authors disclose AI-assisted drafting.
- "Qwen3.8-27B" and "GPT-6-luna" cannot be checked independently from here.
- Its "topological" vocabulary mostly dresses up greedy clustering plus access control.

**Verdict.** The architecture itself does not transfer to a single-agent CLI on a Pentium M. A few of its principles do.

## 3. Mapping onto jazz-cli

**Constraints:** 32-bit Pentium M with 2 GB RAM, free models limited to about 200 requests/hour per IP, frequent provider stalls. jazz-cli is single-agent with a `task` subagent and has **no eval harness in the repo**: only `go test` and the `-p` exit codes.

### Adopt, ranked by expected impact

**A1. Deterministic fast paths for common intents (works with no LLM). Impact: high.**
- **Files:** `intent.go`, and `intents.json` embedded via `go:embed` and overridable from `~/.config/jazz-cli/intents.json` and `.jazz/intents.json`. Project files go through the trust gate.
- **Intents:** regex or keyword patterns in PT and EN, such as "rode os testes", "run tests", "git status", "o que mudou", "build", "lint", "onde está X".
- **Command choice:** detect the project from files (`go.mod` → `go test ./...`, `package.json` → `npm test`, `Cargo.toml`, `pyproject`, `Makefile`).
- **Execution:** run the command through the persistent shell **and the existing `permit()`**. Only read-only or allow-listed commands run without asking.
- **When it fires:** only when the match is unambiguous. Interactive mode shows `⚡ atalho: go test ./...  [Enter/esc→modelo]`. In `-p`, it fires only with `--fast-path`.
- **Failure handoff:** if the command fails, hand the output to the LLM as the first turn. This saves the "explore and decide what to run" steps.
- **Measure:** build a corpus of about 50 real prompts from the session JSONLs. Report the fraction served without an LLM, false positives (target 0 for writes), and wall time compared with an LLM turn on the Dell.

**A2. Offline / no-key mode. Impact: high for "useful when nothing is reachable."**
- **Trigger:** `Config.Candidates` is empty, every candidate fails with network or auth errors, or `JAZZ_OFFLINE=1`.
- **Behavior:** jazz enters `offline` mode instead of exiting with an error. Available there: A1 intents, A3 recipes, `!cmd`, `rg`/`fd` search shown in a friendly format, `/undo`, sessions, and an optional local llama.cpp provider if one is configured.
- **Model fallback:** an omni ping every 60 s returns to model mode.
- **Files:** `main_loop.go` (mode switch) and `doctor` (report).
- **Measure:** an eval run with network blocked must succeed on the intent and recipe tasks, with exit code 0 rather than 1.

**A3. JSON recipes: a tool-grounded task graph, the transferable part of TOCOMAS. Impact: medium-high.**
- **Location:** `.jazz/recipes/*.json` and `~/.config/jazz-cli/recipes/`.
- **Format:**
  `{"name","match":["keywords"],"slots":{"pkg":{"from":"arg|regex|ask|llm"}},"steps":[{"id","tool":"bash|read|edit|write","args":{…"{{pkg}}"…},"needs":["id"],"on_fail":"stop|llm"}],"verify":"go test ./{{pkg}}"}`
- **Validation at load time:** the graph is acyclic, every tool exists, and every slot is resolvable. This is TOCOMAS's "structural screen."
- **Running:** `jazz run <recipe> [slots]` or `/recipe`. Each step goes through `permit()` and hooks. A slot marked `llm` gets **one** short, tool-less request to the fast omni model, the same pattern smart mode already uses.
- **On failure:** pass the step output plus the remaining plan to the normal agent loop (`on_fail:"llm"`).
- **Files:** `recipe.go` and `recipe_test.go`.
- **Measure:** run 10 routine recipes (bump version, add test, release, deploy-ssh). Report LLM calls per task (target ≤ 1), pass rate on `verify`, and time.

**A4. Verified plan cache (APC/AgentReuse plus TOCOMAS's "keep only if it beats the parent"). Impact: medium-high on repeated work.**
- **When to save:** after a turn whose `--until` passed, or whose final verification bash exited 0.
- **What to save:** a template. Read-only exploration steps are dropped and paths are generalized into slots.
- **Where:** `dataDir()/plans/<slug>.jsonl`. Each entry holds the normalized keywords, the repo fingerprint (marker files plus top-level layout), the step list, the `verify` command and `{wins, fails}`.
- **Matching:** keyword or BM25 scoring in pure Go. No embeddings, which matters on the Pentium M.
- **On a match:** add **at most 3** past plans (TOCOMAS shows k=3 is optimal) as a short `<system-reminder>` after the user message. The history stays append-only, so the prompt cache keeps working.
- **Promotion:** a plan reaches recipe grade (A3, replayable offline) only after ≥ 2 wins and 0 fails. Any failure demotes it. Store only on verified success, never because the model says it is done.
- **Files:** `plancache.go`, hooked into the turn-end in `agent_loop.go`.
- **Measure:** A/B with the cache off and on, over repeated or similar eval tasks. Report steps per turn, tokens, wall time and pass rate. Also count bad reuses (a cached plan injected when the task then fails).

**A5. A typed handoff contract for `task` subagents. Impact: medium.** TOCOMAS's biggest ablation loss came from broken consumer boundaries.
- **Subagent output:** it must end with `{"status":"done|blocked","artifacts":["path"],"evidence":{"cmd","exit"}}`. The weak-model JSON repair in `textcalls.go` handles malformed output.
- **Parent checks:** in `subagent.go`, the parent confirms the artifacts exist and may re-run `evidence.cmd`.
- **Parent context:** it receives only the summary and artifact list, not the subagent's transcript. This is the "projected artifact" idea.
- **Measure:** on multi-step evals, count subagent claims that turned out false, from the review and verification logs.

### Do not adopt

- **Responsibility domains or a multi-agent organization.** They multiply LLM calls. That is fatal at about 200 requests/hour on free models and slow on the Dell. They also need a task graph that nobody in jazz-cli produces.
- **Online structural evolution.** It re-runs every task once more (about 2× calls), and its reward is noisy with single runs. The only part worth keeping is "keep only what is verified," which is A4.
- **The typed K-hop semantic memory graph.** It needs embeddings, which is infeasible locally on a Pentium M. Keyword or BM25 covers jazz-cli's case.

### How to measure (none of this exists yet)

- **Harness:** add `scripts/eval.sh` (or `eval/` with fixture repos). Each task is `{repo, prompt, until}` and runs as `jazz -p --output-format json --until … --timeout 10m`.
- **Logged per task:** exit code (0 / 3 / 4 / 7 / 8 / 124), steps, LLM calls, tokens, wall time on the Dell, and the network mode (online or blocked).
- **Variance:** use ≥ 3 seeds or repeats, because free models are noisy. TOCOMAS itself reported no variance.
- **Pairing:** it could be paired with the existing `~/local-ai` eval suite.
